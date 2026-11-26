/* Native modules (kario_native.h, 22-developer-api.md §9.3): "native:<name>"
 * is lib/<arch>/<name>.so of the app's own directory, when the manifest
 * names it in "native". The installer has checked the package (jsrt.Check)
 * and the Shell its signature before the app started; here the runtime
 * refuses whatever it would not have: another name, another place, another
 * architecture, another version of the interface.
 *
 * The modules an app declares load as it starts, before its code runs; then
 * the process is sealed (seal below): no thread can map executable memory
 * any more, so no code from outside the package runs later. A crash in
 * native code writes a backtrace (on_crash) that kario symbolize maps to
 * functions and source lines. */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <elf.h>
#include <errno.h>
#include <fcntl.h>
#include <link.h>
#include <linux/audit.h>
#include <linux/filter.h>
#include <linux/seccomp.h>
#include <math.h>
#include <pthread.h>
#include <signal.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/eventfd.h>
#include <sys/mman.h>
#include <sys/personality.h>
#include <sys/prctl.h>
#include <sys/shm.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <ucontext.h>
#include <unistd.h>
#include <unwind.h>

#include "jsrt.h"
#include "kario_native.h"

#if defined(__aarch64__)
#define NATIVE_ARCH "arm64"
#define NATIVE_MACHINE EM_AARCH64
#define SEAL_ARCH AUDIT_ARCH_AARCH64
#elif defined(__x86_64__)
#define NATIVE_ARCH "x86_64"
#define NATIVE_MACHINE EM_X86_64
#define SEAL_ARCH AUDIT_ARCH_X86_64
#else
#define NATIVE_ARCH ""
#define NATIVE_MACHINE EM_NONE
#endif

/* Module names are file names (lib/<arch>/<name>.so) and manifest entries,
 * by the rule of jsrt.ValidNative. */
#define MAX_MODULE_NAME 32
#define MAX_WORKERS 4
/* Events a module's threads may have queued before emit refuses more. */
#define MAX_QUEUED_EVENTS 1024

enum { EXP_FN, EXP_NUMBER, EXP_STRING };

typedef struct {
	char *name;
	int kind;
	kario_fn fn;
	void *data;
	uint32_t flags;
	double number;
	char *string;
} export_t;

struct kario_module {
	jsrt *r;
	char name[MAX_MODULE_NAME + 1];
	int index;
	int loading;
	export_t *exports;
	size_t nexports;
	JSModuleDef *def;
	JSValue handlers; /* event -> function, set by the module's on() */
};

typedef struct {
	kario_type type;
	double number;
	int truth;
	const char *cstr; /* the engine's string (a plain call) */
	char *str;        /* a copy (an asynchronous call) */
	size_t slen;
	void *bytes;
	size_t blen;
	int bytes_owned;
	char *json;
	size_t jlen;
	int json_done;
} arg_t;

enum { RES_NONE, RES_NUMBER, RES_BOOL, RES_STRING, RES_BYTES, RES_JSON };

struct kario_call {
	JSContext *ctx; /* NULL on a worker */
	JSValueConst *argv;
	int argc;
	arg_t *args;
	int res;
	double number;
	char *str; /* a string or JSON result */
	size_t len;
	void *buf;
	size_t blen;
	char *err;
};

typedef struct job {
	struct job *next;
	kario_module *m;
	export_t *e;
	kario_call call;
	JSValue resolve, reject;
	char *event; /* an emitted event, else a call */
	char *json;
	size_t jlen;
} job_t;

/* A declared module that did not load as the app started: its import
 * throws this. */
typedef struct {
	char name[MAX_MODULE_NAME + 1];
	char *message;
} refused_t;

struct jsrt_native {
	kario_module **mods;
	size_t nmods;
	refused_t *refused;
	size_t nrefused;
	int sealed;
	JSValue manifest; /* the manifest's "native", JS_UNDEFINED until read */
	pthread_mutex_t mu;
	pthread_cond_t work;
	job_t *todo, **todo_tail;
	job_t *done, **done_tail;
	int workers, idle, queued_events;
};

static kario_api api;

static __thread const char *crash_mod, *crash_fn;

/* ---- names and files ---- */

static int is_alpha(char c) { return c == '_' || ((c | 32) >= 'a' && (c | 32) <= 'z'); }

static int is_lower_digit(char c) { return c == '_' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'); }

static int valid_export(const char *s)
{
	size_t n = s ? strlen(s) : 0;
	if (!n || n > KARIO_NATIVE_MAX_NAME || !is_alpha(s[0]))
		return 0;
	for (size_t i = 1; i < n; i++)
		if (!is_alpha(s[i]) && !(s[i] >= '0' && s[i] <= '9'))
			return 0;
	return 1;
}

static int valid_module(const char *s)
{
	size_t n = strlen(s);
	if (!n || n > MAX_MODULE_NAME || s[0] < 'a' || s[0] > 'z')
		return 0;
	for (size_t i = 1; i < n; i++)
		if (!is_lower_digit(s[i]))
			return 0;
	return 1;
}

static const char *machine_name(unsigned m)
{
	switch (m) {
	case EM_AARCH64:
		return "arm64";
	case EM_X86_64:
		return "x86_64";
	case EM_ARM:
		return "32-bit arm";
	case EM_386:
		return "32-bit x86";
	case EM_RISCV:
		return "riscv";
	}
	return "another machine";
}

/* exec_stack: the library's program headers ask for an executable stack
 * (PT_GNU_STACK with PF_X, or none at all), which glibc would give every
 * thread started afterwards: after sealing, no thread could start. */
static int exec_stack(int fd, const Elf64_Ehdr *h)
{
	if (h->e_phentsize != sizeof(Elf64_Phdr) || h->e_phnum == 0 || h->e_phnum > 256)
		return 1;
	for (unsigned i = 0; i < h->e_phnum; i++) {
		Elf64_Phdr ph;
		if (pread(fd, &ph, sizeof ph, (off_t)(h->e_phoff + i * sizeof ph)) != (ssize_t)sizeof ph)
			return 1;
		if (ph.p_type == PT_GNU_STACK)
			return (ph.p_flags & PF_X) != 0;
	}
	return 1;
}

/* check_elf: NULL for a 64-bit little-endian shared object of this
 * machine, else why not; a library built for the other architecture is the
 * usual mistake, and dlopen would only say "wrong ELF class". */
static const char *check_elf(const char *path, char *why, size_t n)
{
	int fd = open(path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
	if (fd < 0)
		return strerror(errno);
	unsigned char h[sizeof(Elf64_Ehdr)] = {0};
	ssize_t got = read(fd, h, sizeof h);
	const char *bad = NULL;
	unsigned type = h[16] | h[17] << 8, mach = h[18] | h[19] << 8;
	if (got < 20 || memcmp(h, ELFMAG, SELFMAG)) {
		bad = "not an ELF library";
	} else if (h[EI_DATA] != ELFDATA2LSB) {
		bad = "a big-endian library";
	} else if (h[EI_CLASS] != ELFCLASS64 || mach != NATIVE_MACHINE) {
		snprintf(why, n, "a library for %s, not %s", machine_name(mach), NATIVE_ARCH);
		bad = why;
	} else if (type != ET_DYN) {
		bad = "not a shared library (link it with -shared)";
	} else if (got != (ssize_t)sizeof h) {
		bad = "not an ELF library";
	} else {
		Elf64_Ehdr eh;
		memcpy(&eh, h, sizeof eh);
		if (exec_stack(fd, &eh))
			bad = "a library that asks for an executable stack (link it with -z noexecstack)";
	}
	close(fd);
	return bad;
}

/* manifest_native is the manifest's "native" (JS_NULL when there is none). */
static JSValue manifest_native(jsrt *r) { return jsrt_manifest_prop(r, "native"); }

/* declared: the app's manifest lists name in "native". */
static int declared(jsrt *r, const char *name)
{
	jsrt_native *n = r->native;
	JSContext *ctx = r->ctx;
	if (JS_IsUndefined(n->manifest))
		n->manifest = manifest_native(r);
	int64_t len = 0;
	if (!JS_IsArray(n->manifest) || JS_GetLength(ctx, n->manifest, &len) < 0)
		return 0;
	for (int64_t i = 0; i < len; i++) {
		JSValue v = JS_GetPropertyUint32(ctx, n->manifest, (uint32_t)i);
		const char *s = JS_IsString(v) ? JS_ToCString(ctx, v) : NULL;
		int same = s && !strcmp(s, name);
		JS_FreeCString(ctx, s);
		JS_FreeValue(ctx, v);
		if (same)
			return 1;
	}
	return 0;
}

/* ---- crashes ---- */

/* The signals a fault raises: a thread that blocks them dies of the fault
 * without on_crash's lines. */
static const int crash_signals[] = {SIGSEGV, SIGBUS, SIGILL, SIGFPE, SIGABRT};

#define MAX_OBJECTS 64
#define MAX_SEGMENTS 4
#define MAX_FRAMES 48
#define MAX_BUILD_ID 32
/* on_crash runs here, so a stack overflow still gets its lines; module
 * threads of their own have none and die without them. */
#define ALT_STACK (64 * 1024)

/* The code a backtrace names: the objects loaded by the time each module
 * was, which sealing keeps from changing afterwards. Only appended to, on
 * the engine's thread, and read by on_crash on any. */
typedef struct {
	uintptr_t bias;
	uintptr_t lo[MAX_SEGMENTS], hi[MAX_SEGMENTS];
	int nseg;
	char name[MAX_MODULE_NAME + 16];
	char build_id[2 * MAX_BUILD_ID + 1];
} code_object;

static code_object objects[MAX_OBJECTS];
static int nobjects;

typedef struct {
	const char *path, *module;
} note_arg;

static void hex_bytes(const unsigned char *p, size_t n, char *out)
{
	static const char digits[] = "0123456789abcdef";
	for (size_t i = 0; i < n; i++) {
		out[2 * i] = digits[p[i] >> 4];
		out[2 * i + 1] = digits[p[i] & 15];
	}
	out[2 * n] = 0;
}

/* read_build_id finds NT_GNU_BUILD_ID among a loaded segment's notes. */
static void read_build_id(const unsigned char *p, size_t len, char *out)
{
	while (len >= sizeof(Elf64_Nhdr)) {
		Elf64_Nhdr nh;
		memcpy(&nh, p, sizeof nh);
		size_t name = ((size_t)nh.n_namesz + 3) & ~(size_t)3, desc = ((size_t)nh.n_descsz + 3) & ~(size_t)3;
		size_t end = sizeof nh + name + desc;
		if (name > len || desc > len || end > len)
			return;
		if (nh.n_type == NT_GNU_BUILD_ID && nh.n_namesz == 4 && !memcmp(p + sizeof nh, "GNU", 4)) {
			hex_bytes(p + sizeof nh + name, nh.n_descsz < MAX_BUILD_ID ? nh.n_descsz : MAX_BUILD_ID, out);
			return;
		}
		p += end;
		len -= end;
	}
}

static int note_object(struct dl_phdr_info *info, size_t size, void *opaque)
{
	note_arg *a = opaque;
	int n = __atomic_load_n(&nobjects, __ATOMIC_ACQUIRE);
	if (n >= MAX_OBJECTS)
		return 1;
	code_object o = {.bias = info->dlpi_addr};
	for (int i = 0; i < info->dlpi_phnum; i++) {
		const ElfW(Phdr) *ph = &info->dlpi_phdr[i];
		if (ph->p_type == PT_LOAD && ph->p_flags & PF_X && o.nseg < MAX_SEGMENTS) {
			o.lo[o.nseg] = info->dlpi_addr + ph->p_vaddr;
			o.hi[o.nseg++] = info->dlpi_addr + ph->p_vaddr + ph->p_memsz;
		} else if (ph->p_type == PT_NOTE && !o.build_id[0]) {
			read_build_id((const unsigned char *)(info->dlpi_addr + ph->p_vaddr), ph->p_memsz, o.build_id);
		}
	}
	if (!o.nseg)
		return 0;
	for (int i = 0; i < n; i++)
		if (objects[i].lo[0] == o.lo[0])
			return 0;
	const char *name = info->dlpi_name;
	if (a->path && name && !strcmp(name, a->path)) {
		snprintf(o.name, sizeof o.name, "native:%s", a->module);
	} else if (!name || !*name) {
		snprintf(o.name, sizeof o.name, "%s", program_invocation_short_name);
	} else {
		const char *slash = strrchr(name, '/');
		snprintf(o.name, sizeof o.name, "%s", slash ? slash + 1 : name);
	}
	objects[n] = o;
	__atomic_store_n(&nobjects, n + 1, __ATOMIC_RELEASE);
	return 0;
}

/* note_objects adds what is loaded now; path is module's library. */
static void note_objects(const char *path, const char *module)
{
	note_arg a = {path, module};
	dl_iterate_phdr(note_object, &a);
}

static const code_object *object_at(uintptr_t pc)
{
	int n = __atomic_load_n(&nobjects, __ATOMIC_ACQUIRE);
	for (int i = 0; i < n; i++)
		for (int s = 0; s < objects[i].nseg; s++)
			if (pc >= objects[i].lo[s] && pc < objects[i].hi[s])
				return &objects[i];
	return NULL;
}

/* A line of on_crash's, built without stdio (nothing but write(2) is
 * safe in a signal handler). */
typedef struct {
	char buf[256];
	size_t n;
} line_t;

static void put(line_t *l, const char *s)
{
	while (s && *s && l->n < sizeof l->buf - 1)
		l->buf[l->n++] = *s++;
}

static void put_num(line_t *l, uintptr_t v, unsigned base)
{
	char d[24];
	int i = 0;
	do {
		d[i++] = "0123456789abcdef"[v % base];
		v /= base;
	} while (v);
	if (base == 16)
		put(l, "0x");
	while (i > 0 && l->n < sizeof l->buf - 1)
		l->buf[l->n++] = d[--i];
}

static void flush(line_t *l)
{
	l->buf[l->n++] = '\n';
	ssize_t w = write(2, l->buf, l->n);
	(void)w;
	l->n = 0;
}

static const char *signal_name(int sig)
{
	switch (sig) {
	case SIGSEGV:
		return "SIGSEGV";
	case SIGBUS:
		return "SIGBUS";
	case SIGILL:
		return "SIGILL";
	case SIGFPE:
		return "SIGFPE";
	case SIGABRT:
		return "SIGABRT";
	}
	return "a signal";
}

static uintptr_t context_pc(void *uc)
{
#if defined(__x86_64__)
	return (uintptr_t)((ucontext_t *)uc)->uc_mcontext.gregs[REG_RIP];
#elif defined(__aarch64__)
	return (uintptr_t)((ucontext_t *)uc)->uc_mcontext.pc;
#else
	return 0;
#endif
}

typedef struct {
	uintptr_t pc[MAX_FRAMES];
	int n;
} frames_t;

static _Unwind_Reason_Code unwind_frame(struct _Unwind_Context *ctx, void *opaque)
{
	frames_t *f = opaque;
	int before = 0;
	if (f->n == MAX_FRAMES)
		return _URC_END_OF_STACK;
	f->pc[f->n++] = _Unwind_GetIPInfo(ctx, &before);
	return _URC_NO_REASON;
}

/* frame_line: "#<i> <object> +0x<offset> build-id <hex>", the offset an
 * address of the object's ELF file (what its symbols and DWARF use). */
static void frame_line(line_t *l, int i, uintptr_t pc)
{
	const code_object *o = object_at(pc);
	put(l, JSRT_BACKTRACE_FRAME);
	put_num(l, (uintptr_t)i, 10);
	put(l, " ");
	if (!o) {
		put_num(l, pc, 16);
	} else {
		put(l, o->name);
		put(l, " +");
		put_num(l, pc - o->bias, 16);
		if (o->build_id[0]) {
			put(l, " build-id ");
			put(l, o->build_id);
		}
	}
	flush(l);
}

/* A crash in native code ends the app like any other; these lines in its
 * log say where, which the JavaScript stack cannot: the module and the
 * function being called (or the module whose own thread crashed), the
 * signal, and the native frames, innermost first. The Shell keeps them in
 * the crash record. The unwinder (libgcc's, from the tables every module
 * has) is the one part not on POSIX's list of what a signal handler may
 * call; it was run once before sealing, so it loads nothing now. */
static void on_crash(int sig, siginfo_t *si, void *uc)
{
	const char *mod = crash_mod, *fn = crash_fn;
	uintptr_t pc = context_pc(uc);
	const code_object *at = object_at(pc);
	line_t l = {.n = 0};
	put(&l, JSRT_BACKTRACE_HEAD);
	if (mod) {
		put(&l, " in native:");
		put(&l, mod);
		put(&l, ".");
		put(&l, fn);
	} else if (at && !strncmp(at->name, JSRT_NATIVE_SCHEME, strlen(JSRT_NATIVE_SCHEME))) {
		put(&l, " in ");
		put(&l, at->name);
		put(&l, " (a thread of its own)");
	}
	put(&l, ": ");
	put(&l, signal_name(sig));
	if (sig != SIGABRT) {
		put(&l, " at ");
		put_num(&l, (uintptr_t)si->si_addr, 16);
	}
	flush(&l);
	frames_t f = {.n = 0};
	_Unwind_Backtrace(unwind_frame, &f);
	int from = -1;
	for (int i = 0; i < f.n && from < 0; i++)
		if (f.pc[i] == pc)
			from = i;
	frame_line(&l, 0, pc);
	for (int i = from + 1, k = 1; from >= 0 && i < f.n && f.pc[i]; i++)
		frame_line(&l, k++, f.pc[i]);
	put(&l, JSRT_BACKTRACE_END);
	flush(&l);
	signal(sig, SIG_DFL);
	raise(sig);
	/* Still here: the first process of a pid namespace (an app on the
	 * device), which is not delivered a signal it raises with the default
	 * action. A fault comes again once this returns, and that one is; a
	 * raised signal (abort's) ends with 128 + its number, as the Shell's
	 * crash record reads it (shell/crash.go). */
	if (si->si_code <= 0)
		_exit(128 + sig);
}

static void alt_stack(void)
{
	stack_t ss = {.ss_size = ALT_STACK};
	ss.ss_sp = mmap(NULL, ALT_STACK, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	if (ss.ss_sp != MAP_FAILED)
		sigaltstack(&ss, NULL);
}

static void catch_crashes(void)
{
	struct sigaction sa = {.sa_sigaction = on_crash, .sa_flags = SA_SIGINFO | SA_ONSTACK | SA_RESETHAND | SA_NODEFER};
	sigemptyset(&sa.sa_mask);
	alt_stack();
	for (size_t i = 0; i < sizeof crash_signals / sizeof crash_signals[0]; i++)
		sigaction(crash_signals[i], &sa, NULL);
	frames_t f = {.n = 0};
	_Unwind_Backtrace(unwind_frame, &f);
	note_objects(NULL, NULL);
}

static void run_fn(kario_module *m, export_t *e, kario_call *c)
{
	const char *pm = crash_mod, *pf = crash_fn;
	crash_mod = m->name;
	crash_fn = e->name;
	e->fn(&api, c, e->data);
	crash_mod = pm;
	crash_fn = pf;
}

/* ---- the table the module gets ---- */

static int add_export(kario_module *m, const char *name, export_t e)
{
	if (!m || !m->loading || !valid_export(name) || !strcmp(name, "on") || m->nexports >= KARIO_NATIVE_MAX_EXPORTS)
		return -1;
	for (size_t i = 0; i < m->nexports; i++)
		if (!strcmp(m->exports[i].name, name))
			return -1;
	export_t *x = realloc(m->exports, (m->nexports + 1) * sizeof *x);
	if (!x)
		return -1;
	m->exports = x;
	if (!(e.name = strdup(name)))
		return -1;
	m->exports[m->nexports++] = e;
	return 0;
}

static int api_export_fn(kario_module *m, const char *name, kario_fn fn, void *data, uint32_t flags)
{
	if (!fn || flags & ~KARIO_ASYNC)
		return -1;
	return add_export(m, name, (export_t){.kind = EXP_FN, .fn = fn, .data = data, .flags = flags});
}

static int api_export_number(kario_module *m, const char *name, double v)
{
	return add_export(m, name, (export_t){.kind = EXP_NUMBER, .number = v});
}

static int api_export_string(kario_module *m, const char *name, const char *s)
{
	char *copy = s ? strdup(s) : NULL;
	if (!copy || add_export(m, name, (export_t){.kind = EXP_STRING, .string = copy}) < 0) {
		free(copy);
		return -1;
	}
	return 0;
}

static arg_t *arg(kario_call *c, int i) { return i >= 0 && i < c->argc ? &c->args[i] : NULL; }

static int api_argc(kario_call *c) { return c->argc; }

static kario_type api_type(kario_call *c, int i)
{
	arg_t *a = arg(c, i);
	return a ? a->type : KARIO_UNDEFINED;
}

static double api_number(kario_call *c, int i)
{
	arg_t *a = arg(c, i);
	return a && a->type == KARIO_NUMBER ? a->number : NAN;
}

static int api_boolean(kario_call *c, int i)
{
	arg_t *a = arg(c, i);
	return a ? a->truth : 0;
}

static const char *api_string(kario_call *c, int i, size_t *len)
{
	arg_t *a = arg(c, i);
	if (!a || a->type != KARIO_STRING)
		return NULL;
	if (!a->str && !a->cstr && c->ctx && !(a->cstr = JS_ToCStringLen(c->ctx, &a->slen, c->argv[i])))
		JS_FreeValue(c->ctx, JS_GetException(c->ctx));
	const char *s = a->str ? a->str : a->cstr;
	if (s && len)
		*len = a->slen;
	return s;
}

static void *api_bytes(kario_call *c, int i, size_t *len)
{
	arg_t *a = arg(c, i);
	if (!a || a->type != KARIO_BYTES)
		return NULL;
	if (len)
		*len = a->blen;
	return a->bytes;
}

/* json_quote writes s as a JSON string, as JSON.stringify does. */
static void json_quote(dstr *d, const char *s, size_t n)
{
	dstr_puts(d, "\"");
	for (size_t i = 0; i < n; i++) {
		unsigned char ch = (unsigned char)s[i];
		const char *esc = NULL;
		char u[8];
		switch (ch) {
		case '"':
			esc = "\\\"";
			break;
		case '\\':
			esc = "\\\\";
			break;
		case '\n':
			esc = "\\n";
			break;
		case '\r':
			esc = "\\r";
			break;
		case '\t':
			esc = "\\t";
			break;
		case '\b':
			esc = "\\b";
			break;
		case '\f':
			esc = "\\f";
			break;
		default:
			if (ch < 0x20) {
				snprintf(u, sizeof u, "\\u%04x", ch);
				esc = u;
			}
		}
		if (esc)
			dstr_puts(d, esc);
		else
			dstr_put(d, s + i, 1);
	}
	dstr_puts(d, "\"");
}

static const char *api_json(kario_call *c, int i, size_t *len)
{
	arg_t *a = arg(c, i);
	if (!a || a->type == KARIO_BYTES || a->type == KARIO_UNDEFINED)
		return NULL;
	if (!a->json_done) {
		a->json_done = 1;
		if (c->ctx) {
			if (!(a->json = jsrt_stringify(c->ctx, c->argv[i])))
				JS_FreeValue(c->ctx, JS_GetException(c->ctx));
		} else if (a->type == KARIO_STRING) {
			dstr d = {0};
			json_quote(&d, a->str, a->slen);
			a->json = d.s;
		}
		if (a->json)
			a->jlen = strlen(a->json);
	}
	if (a->json && len)
		*len = a->jlen;
	return a->json;
}

static void clear_result(kario_call *c)
{
	free(c->str);
	free(c->buf);
	c->str = NULL;
	c->buf = NULL;
	c->res = RES_NONE;
}

static void api_ret_number(kario_call *c, double v)
{
	clear_result(c);
	c->res = RES_NUMBER;
	c->number = v;
}

static void api_ret_bool(kario_call *c, int v)
{
	clear_result(c);
	c->res = RES_BOOL;
	c->number = v != 0;
}

static void set_text(kario_call *c, int res, const char *s, size_t n)
{
	clear_result(c);
	if (!s || !(c->str = malloc(n + 1)))
		return;
	memcpy(c->str, s, n);
	c->str[n] = 0;
	c->len = n;
	c->res = res;
}

static void api_ret_string(kario_call *c, const char *s, size_t n) { set_text(c, RES_STRING, s, n); }

static void api_ret_json(kario_call *c, const char *s, size_t n) { set_text(c, RES_JSON, s, n); }

static void *api_ret_bytes(kario_call *c, size_t n)
{
	clear_result(c);
	if (!(c->buf = calloc(1, n ? n : 1)))
		return NULL;
	c->blen = n;
	c->res = RES_BYTES;
	return c->buf;
}

static void api_fail(kario_call *c, const char *message)
{
	free(c->err);
	c->err = strdup(message && *message ? message : "failed");
}

/* finish hands a finished job (or an event) to the engine's thread. */
static void finish(jsrt *r, job_t *j)
{
	jsrt_native *n = r->native;
	pthread_mutex_lock(&n->mu);
	j->next = NULL;
	*n->done_tail = j;
	n->done_tail = &j->next;
	pthread_mutex_unlock(&n->mu);
	uint64_t one = 1;
	ssize_t w = write(r->native_fd, &one, sizeof one);
	(void)w;
}

static int api_emit(kario_module *m, const char *event, const char *json, size_t len)
{
	if (!m || !valid_export(event))
		return -1;
	jsrt_native *n = m->r->native;
	job_t *j = calloc(1, sizeof *j);
	if (!j)
		return -1;
	j->m = m;
	j->event = strdup(event);
	j->json = json ? strndup(json, len) : NULL;
	j->jlen = len;
	pthread_mutex_lock(&n->mu);
	int ok = j->event && (!json || j->json) && n->queued_events < MAX_QUEUED_EVENTS;
	if (ok)
		n->queued_events++;
	pthread_mutex_unlock(&n->mu);
	if (!ok) {
		free(j->event);
		free(j->json);
		free(j);
		return -1;
	}
	finish(m->r, j);
	return 0;
}

/* ---- calls ---- */

/* take_args reads the arguments' types; a plain call borrows the rest from
 * the engine when asked, an asynchronous one copies it all now. */
static int take_args(JSContext *ctx, kario_call *c, int argc, JSValueConst *argv, int copy)
{
	c->argc = argc;
	c->argv = argv;
	if (argc && !(c->args = calloc((size_t)argc, sizeof *c->args)))
		return -1;
	for (int i = 0; i < argc; i++) {
		arg_t *a = &c->args[i];
		JSValueConst v = argv[i];
		a->truth = JS_ToBool(ctx, v) > 0;
		if (JS_IsUndefined(v)) {
			a->type = KARIO_UNDEFINED;
		} else if (JS_IsNull(v)) {
			a->type = KARIO_NULL;
		} else if (JS_IsBool(v)) {
			a->type = KARIO_BOOL;
		} else if (JS_IsNumber(v)) {
			a->type = KARIO_NUMBER;
			JS_ToFloat64(ctx, &a->number, v);
		} else if (JS_IsString(v)) {
			a->type = KARIO_STRING;
			if (copy) {
				const char *s = JS_ToCStringLen(ctx, &a->slen, v);
				if (!s)
					return -1;
				a->str = malloc(a->slen + 1);
				if (a->str) {
					memcpy(a->str, s, a->slen + 1);
				}
				JS_FreeCString(ctx, s);
				if (!a->str)
					return -1;
			}
		} else {
			uint8_t *p = NULL;
			size_t len = 0;
			if (JS_IsArrayBuffer(v)) {
				p = JS_GetArrayBuffer(ctx, &len, v);
			} else if (JS_GetTypedArrayType(v) >= 0) {
				size_t off = 0, bpe = 0;
				JSValue ab = JS_GetTypedArrayBuffer(ctx, v, &off, &len, &bpe);
				if (!JS_IsException(ab)) {
					size_t all;
					uint8_t *base = JS_GetArrayBuffer(ctx, &all, ab);
					p = base ? base + off : NULL;
				}
				JS_FreeValue(ctx, ab);
			} else {
				a->type = KARIO_OBJECT;
			}
			if (a->type != KARIO_OBJECT) {
				a->type = KARIO_BYTES;
				if (!p) {
					/* A detached buffer: no bytes at all. */
					JS_FreeValue(ctx, JS_GetException(ctx));
					p = (uint8_t *)"";
					len = 0;
				}
				a->blen = len;
				a->bytes = p;
				if (copy) {
					if (!(a->bytes = malloc(len ? len : 1)))
						return -1;
					memcpy(a->bytes, p, len);
					a->bytes_owned = 1;
				}
			}
		}
		if (copy && a->type != KARIO_STRING && a->type != KARIO_BYTES && a->type != KARIO_UNDEFINED) {
			a->json_done = 1;
			if ((a->json = jsrt_stringify(ctx, v)))
				a->jlen = strlen(a->json);
			else
				JS_FreeValue(ctx, JS_GetException(ctx));
		}
	}
	if (copy) {
		c->argv = NULL;
		c->ctx = NULL;
	}
	return 0;
}

static void call_free(JSContext *ctx, kario_call *c)
{
	for (int i = 0; c->args && i < c->argc; i++) {
		arg_t *a = &c->args[i];
		if (a->cstr)
			JS_FreeCString(ctx, a->cstr);
		free(a->str);
		free(a->json);
		if (a->bytes_owned)
			free(a->bytes);
	}
	free(c->args);
	free(c->str);
	free(c->buf);
	free(c->err);
}

static void *abuf_realloc(JSRuntime *rt, void *opaque, void *ptr, size_t size)
{
	if (!size) {
		free(ptr);
		return NULL;
	}
	return realloc(ptr, size);
}

/* result is what a call that returned gives JavaScript: its value, or an
 * exception (JS_EXCEPTION, pending in ctx). */
static JSValue result(JSContext *ctx, kario_module *m, export_t *e, kario_call *c)
{
	if (c->err)
		return JS_Throw(ctx, jsrt_error(ctx, c->err, NULL));
	switch (c->res) {
	case RES_NUMBER:
		return JS_NewFloat64(ctx, c->number);
	case RES_BOOL:
		return JS_NewBool(ctx, c->number != 0);
	case RES_STRING:
		return JS_NewStringLen(ctx, c->str, c->len);
	case RES_BYTES: {
		JSValue v = JS_NewArrayBuffer(ctx, c->buf, c->blen, 0, abuf_realloc, NULL, false);
		if (!JS_IsException(v))
			c->buf = NULL;
		return v;
	}
	case RES_JSON: {
		char where[MAX_MODULE_NAME + KARIO_NATIVE_MAX_NAME + 16];
		snprintf(where, sizeof where, "native:%s.%s", m->name, e->name);
		return JS_ParseJSON(ctx, c->str, c->len, where);
	}
	}
	return JS_UNDEFINED;
}

static void *worker(void *opaque)
{
	jsrt *r = opaque;
	jsrt_native *n = r->native;
	sigset_t mask;
	sigfillset(&mask);
	for (size_t i = 0; i < sizeof crash_signals / sizeof crash_signals[0]; i++)
		sigdelset(&mask, crash_signals[i]);
	pthread_sigmask(SIG_BLOCK, &mask, NULL);
	alt_stack();
	pthread_mutex_lock(&n->mu);
	for (;;) {
		while (!n->todo) {
			n->idle++;
			pthread_cond_wait(&n->work, &n->mu);
			n->idle--;
		}
		job_t *j = n->todo;
		n->todo = j->next;
		if (!n->todo)
			n->todo_tail = &n->todo;
		pthread_mutex_unlock(&n->mu);
		run_fn(j->m, j->e, &j->call);
		finish(r, j);
		pthread_mutex_lock(&n->mu);
	}
	return NULL;
}

static JSValue start_async(JSContext *ctx, kario_module *m, export_t *e, int argc, JSValueConst *argv)
{
	jsrt *r = jsrt_of(ctx);
	jsrt_native *n = r->native;
	job_t *j = calloc(1, sizeof *j);
	if (!j)
		return JS_ThrowOutOfMemory(ctx);
	j->m = m;
	j->e = e;
	j->call.ctx = ctx;
	if (take_args(ctx, &j->call, argc, argv, 1) < 0) {
		call_free(ctx, &j->call);
		free(j);
		return JS_ThrowOutOfMemory(ctx);
	}
	JSValue funcs[2];
	JSValue promise = JS_NewPromiseCapability(ctx, funcs);
	if (JS_IsException(promise)) {
		call_free(ctx, &j->call);
		free(j);
		return promise;
	}
	j->resolve = funcs[0];
	j->reject = funcs[1];
	pthread_mutex_lock(&n->mu);
	j->next = NULL;
	*n->todo_tail = j;
	n->todo_tail = &j->next;
	int spawn = n->idle == 0 && n->workers < MAX_WORKERS;
	if (spawn)
		n->workers++;
	pthread_cond_signal(&n->work);
	pthread_mutex_unlock(&n->mu);
	if (spawn) {
		pthread_t t;
		if (pthread_create(&t, NULL, worker, r) == 0) {
			pthread_detach(t);
		} else {
			pthread_mutex_lock(&n->mu);
			n->workers--;
			int none = n->workers == 0;
			pthread_mutex_unlock(&n->mu);
			/* With no worker at all the call waits forever: say so now. */
			if (none)
				fprintf(stderr, "kario-jsrt: native:%s: no worker thread: %s\n", m->name, strerror(errno));
		}
	}
	return promise;
}

static JSValue native_fn(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic, JSValueConst *data)
{
	jsrt *r = jsrt_of(ctx);
	int32_t mi = 0;
	JS_ToInt32(ctx, &mi, data[0]);
	kario_module *m = r->native->mods[mi];
	export_t *e = &m->exports[magic];
	if (e->flags & KARIO_ASYNC)
		return start_async(ctx, m, e, argc, argv);
	kario_call c = {.ctx = ctx};
	if (take_args(ctx, &c, argc, argv, 0) < 0) {
		call_free(ctx, &c);
		return JS_ThrowOutOfMemory(ctx);
	}
	run_fn(m, e, &c);
	JSValue v = result(ctx, m, e, &c);
	call_free(ctx, &c);
	return v;
}

/* The module's on(event, handler): one handler an event, as kario's on. */
static JSValue native_on(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic, JSValueConst *data)
{
	jsrt *r = jsrt_of(ctx);
	int32_t mi = 0;
	JS_ToInt32(ctx, &mi, data[0]);
	kario_module *m = r->native->mods[mi];
	const char *name = argc ? JS_ToCString(ctx, argv[0]) : NULL;
	if (!name)
		return JS_ThrowTypeError(ctx, "on: the event's name is a string");
	JSValueConst fn = argc > 1 ? argv[1] : JS_UNDEFINED;
	JSValue ret = JS_UNDEFINED;
	if (JS_IsFunction(ctx, fn)) {
		JS_SetPropertyStr(ctx, m->handlers, name, JS_DupValue(ctx, fn));
	} else if (JS_IsUndefined(fn) || JS_IsNull(fn)) {
		JSAtom a = JS_NewAtom(ctx, name);
		JS_DeleteProperty(ctx, m->handlers, a, 0);
		JS_FreeAtom(ctx, a);
	} else {
		ret = JS_ThrowTypeError(ctx, "on: the handler is a function");
	}
	JS_FreeCString(ctx, name);
	return ret;
}

/* jsrt_native_drain settles what the workers finished and delivers the
 * modules' events, on the engine's thread. */
void jsrt_native_drain(jsrt *r)
{
	jsrt_native *n = r->native;
	if (!n)
		return;
	uint64_t count;
	ssize_t got = read(r->native_fd, &count, sizeof count);
	(void)got;
	pthread_mutex_lock(&n->mu);
	job_t *list = n->done;
	n->done = NULL;
	n->done_tail = &n->done;
	pthread_mutex_unlock(&n->mu);
	JSContext *ctx = r->ctx;
	while (list) {
		job_t *j = list;
		list = j->next;
		if (j->event) {
			pthread_mutex_lock(&n->mu);
			n->queued_events--;
			pthread_mutex_unlock(&n->mu);
			JSValue fn = JS_GetPropertyStr(ctx, j->m->handlers, j->event);
			if (JS_IsFunction(ctx, fn)) {
				char where[MAX_MODULE_NAME + KARIO_NATIVE_MAX_NAME + 32];
				snprintf(where, sizeof where, "native:%s event \"%s\"", j->m->name, j->event);
				JSValue v = j->json ? JS_ParseJSON(ctx, j->json, j->jlen, where) : JS_UNDEFINED;
				if (JS_IsException(v)) {
					jsrt_report(ctx, where);
				} else {
					JSValue ret = JS_Call(ctx, fn, JS_UNDEFINED, 1, (JSValueConst *)&v);
					if (JS_IsException(ret))
						jsrt_report(ctx, where);
					JS_FreeValue(ctx, ret);
				}
				JS_FreeValue(ctx, v);
			}
			JS_FreeValue(ctx, fn);
			free(j->event);
			free(j->json);
		} else {
			JSValue v = result(ctx, j->m, j->e, &j->call);
			JSValue ret;
			if (JS_IsException(v)) {
				JSValue err = JS_GetException(ctx);
				ret = JS_Call(ctx, j->reject, JS_UNDEFINED, 1, (JSValueConst *)&err);
				JS_FreeValue(ctx, err);
			} else {
				ret = JS_Call(ctx, j->resolve, JS_UNDEFINED, 1, (JSValueConst *)&v);
				JS_FreeValue(ctx, v);
			}
			JS_FreeValue(ctx, ret);
			JS_FreeValue(ctx, j->resolve);
			JS_FreeValue(ctx, j->reject);
			call_free(ctx, &j->call);
		}
		free(j);
		jsrt_run_jobs(r);
	}
}

/* ---- loading ---- */

static int module_init(JSContext *ctx, JSModuleDef *def)
{
	jsrt *r = jsrt_of(ctx);
	kario_module *m = NULL;
	for (size_t i = 0; i < r->native->nmods && !m; i++)
		if (r->native->mods[i]->def == def)
			m = r->native->mods[i];
	if (!m)
		return -1;
	JSValue data = JS_NewInt32(ctx, m->index);
	for (size_t i = 0; i < m->nexports; i++) {
		export_t *e = &m->exports[i];
		JSValue v;
		switch (e->kind) {
		case EXP_NUMBER:
			v = JS_NewFloat64(ctx, e->number);
			break;
		case EXP_STRING:
			v = JS_NewString(ctx, e->string);
			break;
		default:
			v = JS_NewCFunctionData(ctx, native_fn, 0, (int)i, 1, &data);
		}
		if (JS_SetModuleExport(ctx, def, e->name, v) < 0)
			return -1;
	}
	return JS_SetModuleExport(ctx, def, "on", JS_NewCFunctionData(ctx, native_on, 2, 0, 1, &data));
}

static int setup(jsrt *r)
{
	if (r->native)
		return 0;
	jsrt_native *n = calloc(1, sizeof *n);
	if (!n)
		return -1;
	int fd = eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
	if (fd < 0) {
		free(n);
		return -1;
	}
	pthread_mutex_init(&n->mu, NULL);
	pthread_cond_init(&n->work, NULL);
	n->todo_tail = &n->todo;
	n->done_tail = &n->done;
	n->manifest = JS_UNDEFINED;
	r->native = n;
	r->native_fd = fd;
	static char engine[48];
	snprintf(engine, sizeof engine, "QuickJS-ng %s", JS_GetVersion());
	api.size = sizeof api;
	api.major = KARIO_NATIVE_ABI_MAJOR;
	api.minor = KARIO_NATIVE_ABI_MINOR;
	api.engine = engine;
	api.export_fn = api_export_fn;
	api.export_number = api_export_number;
	api.export_string = api_export_string;
	api.argc = api_argc;
	api.type = api_type;
	api.number = api_number;
	api.boolean = api_boolean;
	api.string = api_string;
	api.bytes = api_bytes;
	api.json = api_json;
	api.ret_number = api_ret_number;
	api.ret_bool = api_ret_bool;
	api.ret_string = api_ret_string;
	api.ret_bytes = api_ret_bytes;
	api.ret_json = api_ret_json;
	api.fail = api_fail;
	api.emit = api_emit;
	catch_crashes();
	return 0;
}

#define refuse(...) (JS_ThrowReferenceError(ctx, __VA_ARGS__), (JSModuleDef *)NULL)

JSModuleDef *jsrt_native_module(jsrt *r, const char *spec)
{
	JSContext *ctx = r->ctx;
	const char *name = spec + strlen(JSRT_NATIVE_SCHEME);
	if (!valid_module(name))
		return refuse("could not load module '%s': a native module's name is [a-z][a-z0-9_]*, at most %d characters", spec, MAX_MODULE_NAME);
	if (!*NATIVE_ARCH)
		return refuse("%s: kario-jsrt loads no native modules on this architecture", spec);
	if (setup(r) < 0)
		return refuse("%s: %s", spec, strerror(errno));
	jsrt_native *n = r->native;
	for (size_t i = 0; i < n->nmods; i++)
		if (!strcmp(n->mods[i]->name, name))
			return n->mods[i]->def;
	if (!declared(r, name))
		return refuse("%s: the manifest does not declare it (\"native\": [\"%s\"])", spec, name);
	for (size_t i = 0; i < n->nrefused; i++)
		if (!strcmp(n->refused[i].name, name))
			return refuse("%s", n->refused[i].message);
	if (n->sealed)
		return refuse("%s: native modules load as the app starts, not later", spec);
	dstr rel = {0}, path = {0};
	dstr_puts(&rel, "lib/" NATIVE_ARCH "/");
	dstr_puts(&rel, name);
	dstr_puts(&rel, ".so");
	dstr_puts(&path, r->app_dir);
	dstr_puts(&path, "/");
	dstr_puts(&path, rel.s);
	JSModuleDef *def = NULL;
	struct stat st;
	char why[64];
	const char *bad;
	char *real = realpath(path.s, NULL);
	if (lstat(path.s, &st) < 0) {
		def = refuse("%s: no %s in the app's package (%s)", spec, rel.s, strerror(errno));
	} else if (!S_ISREG(st.st_mode) || !real || strcmp(real, path.s)) {
		def = refuse("%s: %s is not a file of the app's package", spec, rel.s);
	} else if ((bad = check_elf(path.s, why, sizeof why))) {
		def = refuse("%s: %s is %s", spec, rel.s, bad);
	} else {
		void *h = dlopen(path.s, RTLD_NOW | RTLD_LOCAL);
		const uint32_t *abi = h ? dlsym(h, "kario_native_abi") : NULL;
		int (*init)(const kario_api *, kario_module *) = h ? (int (*)(const kario_api *, kario_module *))dlsym(h, "kario_native_init") : NULL;
		kario_module *m = NULL;
		kario_module **mods = NULL;
		if (!h) {
			def = refuse("%s: %s", spec, dlerror());
		} else if (!abi || !init) {
			def = refuse("%s: %s is not a native module (no kario_native_abi or kario_native_init: KARIO_NATIVE_MODULE in kario_native.h)", spec, rel.s);
		} else if (*abi >> 16 != KARIO_NATIVE_ABI_MAJOR || (*abi & 0xffff) > KARIO_NATIVE_ABI_MINOR) {
			def = refuse("%s: built for the native interface %u.%u, kario-jsrt has %d.%d (rebuild it with this SDK's kario_native.h)", spec,
				     *abi >> 16, *abi & 0xffff, KARIO_NATIVE_ABI_MAJOR, KARIO_NATIVE_ABI_MINOR);
		} else if (!(m = calloc(1, sizeof *m)) || !(mods = realloc(n->mods, (n->nmods + 1) * sizeof *mods))) {
			free(m);
			def = refuse("%s: out of memory", spec);
		} else {
			n->mods = mods;
			m->r = r;
			m->index = (int)n->nmods;
			snprintf(m->name, sizeof m->name, "%s", name);
			m->handlers = JS_NewObjectProto(ctx, JS_NULL);
			m->loading = 1;
			note_objects(path.s, m->name);
			crash_mod = m->name;
			crash_fn = "kario_native_init";
			int rc = init(&api, m);
			crash_mod = crash_fn = NULL;
			m->loading = 0;
			if (rc != 0) {
				def = refuse("%s: kario_native_init returned %d", spec, rc);
			} else if (!(def = JS_NewCModule(ctx, spec, module_init))) {
				def = NULL;
			} else {
				for (size_t i = 0; i < m->nexports; i++)
					JS_AddModuleExport(ctx, def, m->exports[i].name);
				JS_AddModuleExport(ctx, def, "on");
				m->def = def;
				n->mods[n->nmods++] = m;
				m = NULL;
			}
			if (m) {
				/* Refused after its init ran: the library stays loaded
				 * (its threads may run), the module is not offered. */
				JS_FreeValue(ctx, m->handlers);
				free(m);
			}
		}
	}
	free(real);
	free(rel.s);
	free(path.s);
	return def;
}

/* ---- sealing ---- */

typedef struct {
	struct sock_filter f[64];
	unsigned short n;
} bpf_t;

#define DENIED (SECCOMP_RET_ERRNO | EPERM)

static void op(bpf_t *p, unsigned short code, unsigned char jt, unsigned char jf, uint32_t k)
{
	p->f[p->n++] = (struct sock_filter){code, jt, jf, k};
}

/* struct seccomp_data's args are 64 bits; the low half comes first on
 * both architectures (little-endian), and the flags tested live there. */
static uint32_t arg_low(int i) { return (uint32_t)(offsetof(struct seccomp_data, args) + 8 * i); }

static void deny(bpf_t *p, int nr)
{
	op(p, BPF_JMP | BPF_JEQ | BPF_K, 0, 1, (uint32_t)nr);
	op(p, BPF_RET | BPF_K, 0, 0, DENIED);
}

static void deny_flag(bpf_t *p, int nr, int arg, uint32_t flag)
{
	op(p, BPF_JMP | BPF_JEQ | BPF_K, 0, 4, (uint32_t)nr);
	op(p, BPF_LD | BPF_W | BPF_ABS, 0, 0, arg_low(arg));
	op(p, BPF_JMP | BPF_JSET | BPF_K, 0, 1, flag);
	op(p, BPF_RET | BPF_K, 0, 0, DENIED);
	op(p, BPF_RET | BPF_K, 0, 0, SECCOMP_RET_ALLOW);
}

/* seal installs the filter that keeps executable memory as it is, on every
 * thread (a module's init may have started some): no mapping or
 * protection change with PROT_EXEC, no executable System V shared memory,
 * no personality that makes readable memory executable, no exec of a
 * program (a memfd or a downloaded file would be code from outside the
 * package; kario-jsrt and its modules start none). Each fails with EPERM
 * and the app runs on. It stacks on the Shell's sandbox filter (sandbox/),
 * which knows nothing of it. */
static int seal(void)
{
	int persona = personality(0xffffffff);
	if (persona != -1 && persona & READ_IMPLIES_EXEC && personality((unsigned long)persona & ~(unsigned long)READ_IMPLIES_EXEC) == -1)
		return -1;
	bpf_t p = {.n = 0};
	op(&p, BPF_LD | BPF_W | BPF_ABS, 0, 0, offsetof(struct seccomp_data, arch));
	op(&p, BPF_JMP | BPF_JEQ | BPF_K, 1, 0, SEAL_ARCH);
	op(&p, BPF_RET | BPF_K, 0, 0, SECCOMP_RET_KILL_PROCESS);
	op(&p, BPF_LD | BPF_W | BPF_ABS, 0, 0, offsetof(struct seccomp_data, nr));
#ifdef __x86_64__
	/* x32 system calls share the architecture's audit value and would
	 * pass under other numbers. */
	op(&p, BPF_JMP | BPF_JGE | BPF_K, 0, 1, 0x40000000);
	op(&p, BPF_RET | BPF_K, 0, 0, DENIED);
#endif
	deny_flag(&p, SYS_mmap, 2, PROT_EXEC);
	deny_flag(&p, SYS_mprotect, 2, PROT_EXEC);
	deny_flag(&p, SYS_pkey_mprotect, 2, PROT_EXEC);
	deny_flag(&p, SYS_shmat, 2, SHM_EXEC);
	deny(&p, SYS_remap_file_pages);
	deny(&p, SYS_execve);
	deny(&p, SYS_execveat);
	/* personality(0xffffffff) only reads it. */
	op(&p, BPF_JMP | BPF_JEQ | BPF_K, 0, 5, SYS_personality);
	op(&p, BPF_LD | BPF_W | BPF_ABS, 0, 0, arg_low(0));
	op(&p, BPF_JMP | BPF_JEQ | BPF_K, 2, 0, 0xffffffff);
	op(&p, BPF_JMP | BPF_JSET | BPF_K, 0, 1, READ_IMPLIES_EXEC);
	op(&p, BPF_RET | BPF_K, 0, 0, DENIED);
	op(&p, BPF_RET | BPF_K, 0, 0, SECCOMP_RET_ALLOW);
	op(&p, BPF_RET | BPF_K, 0, 0, SECCOMP_RET_ALLOW);
	struct sock_fprog prog = {.len = p.n, .filter = p.f};
	if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0)
		return -1;
	return (int)syscall(SYS_seccomp, SECCOMP_SET_MODE_FILTER, SECCOMP_FILTER_FLAG_TSYNC, &prog) == 0 ? 0 : -1;
}

/* unsealed_code is the first executable mapping (a line of
 * /proc/self/maps) that sealing would not hold still, NULL when there is
 * none: memory both writable and executable, shared, anonymous, or a file
 * that is gone or in the app's data directory, any of which a module's
 * init could have made to change its code later. */
static char *unsealed_code(jsrt *r)
{
	FILE *f = fopen("/proc/self/maps", "re");
	if (!f)
		return NULL;
	char line[1024];
	size_t data = r->data_dir ? strlen(r->data_dir) : 0;
	char *bad = NULL;
	while (!bad && fgets(line, sizeof line, f)) {
		line[strcspn(line, "\n")] = 0;
		char perms[8] = "";
		int at = 0;
		if (sscanf(line, "%*s %7s %*s %*s %*s %n", perms, &at) < 1 || strlen(perms) < 4 || perms[2] != 'x')
			continue;
		const char *file = at ? line + at : "";
		int special = file[0] == '[';
		int writable = perms[1] == 'w', shared = perms[3] != 'p';
		int gone = file[0] != '/' && !special, deleted = strstr(file, "(deleted)") != NULL;
		int in_data = data && !strncmp(file, r->data_dir, data) && (file[data] == '/' || !file[data]);
		if (writable || shared || gone || deleted || in_data)
			bad = strdup(line);
	}
	fclose(f);
	return bad;
}

int jsrt_native_preload(jsrt *r)
{
	if (!*NATIVE_ARCH)
		return 0;
	JSContext *ctx = r->ctx;
	JSValue list = manifest_native(r);
	int64_t len = 0;
	if (!JS_IsArray(list) || JS_GetLength(ctx, list, &len) < 0 || len == 0) {
		JS_FreeValue(ctx, list);
		return 0;
	}
	if (setup(r) < 0) {
		JS_FreeValue(ctx, list);
		fprintf(stderr, "kario-jsrt: native modules: %s\n", strerror(errno));
		return -1;
	}
	jsrt_native *n = r->native;
	n->manifest = list;
	for (int64_t i = 0; i < len; i++) {
		JSValue v = JS_GetPropertyUint32(ctx, list, (uint32_t)i);
		const char *name = JS_IsString(v) ? JS_ToCString(ctx, v) : NULL;
		JS_FreeValue(ctx, v);
		if (!name || !valid_module(name)) {
			JS_FreeCString(ctx, name);
			continue;
		}
		dstr spec = {0};
		dstr_puts(&spec, JSRT_NATIVE_SCHEME);
		dstr_puts(&spec, name);
		if (!jsrt_native_module(r, spec.s)) {
			JSValue err = JS_GetException(ctx);
			JSValue msg = JS_GetPropertyStr(ctx, err, "message");
			const char *s = JS_ToCString(ctx, msg);
			refused_t *x = realloc(n->refused, (n->nrefused + 1) * sizeof *x);
			if (x) {
				n->refused = x;
				snprintf(x[n->nrefused].name, sizeof x[n->nrefused].name, "%s", name);
				x[n->nrefused].message = strdup(s ? s : spec.s);
				n->nrefused++;
			}
			JS_FreeCString(ctx, s);
			JS_FreeValue(ctx, msg);
			JS_FreeValue(ctx, err);
		}
		free(spec.s);
		JS_FreeCString(ctx, name);
	}
	note_objects(NULL, NULL);
	char *bad = unsealed_code(r);
	if (bad) {
		fprintf(stderr, "kario-jsrt: a native module left code that could still change (%s); kario-jsrt does not run it\n", bad);
		free(bad);
		return -1;
	}
	if (seal() < 0) {
		fprintf(stderr, "kario-jsrt: could not seal executable memory: %s\n", strerror(errno));
		return -1;
	}
	n->sealed = 1;
	return 0;
}
