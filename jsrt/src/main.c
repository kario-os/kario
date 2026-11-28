/*
 * kario-jsrt runs a TypeScript app, compiled by `kario build` into one
 * JavaScript module, on QuickJS-ng (29-app-development.md). The Shell starts
 * it in the app's sandbox with the module as its argument; it speaks the
 * phone.Ui protocol through the C interface (sdk/c) and gives the module
 * one import, "kario": pages, events, capabilities, translations, storage;
 * and the app's own native modules, "native:<name>" (native.c). QuickJS's
 * std and os modules are not built in and no other module can be imported,
 * so app code has no file, socket or process of its own.
 *
 *	kario-jsrt [--no-bytecode] APP/main.js   run (main.jsc beside it first)
 *	kario-jsrt --bytecode FILE APP/main.js   run FILE's bytecode of main.js
 *	kario-jsrt [--positions] --compile IN.js OUT.jsc|-
 *	                                         precompile (-: to standard output); with
 *	                                         every statement's position (an app a
 *	                                         debugger may attach to)
 *	kario-jsrt --version
 *
 * A store app's package carries JavaScript only: the Shell precompiles it
 * after installation into a directory the app cannot write and names the
 * file with --bytecode; the system image's apps have main.jsc beside.
 *
 * KARIO_JSRT_BUDGET_MS bounds one turn of JavaScript (the main module, the
 * handlers of one message, the timers due, the jobs they leave): past it the
 * engine interrupts the turn with an error no code can catch and says so on
 * stderr ("kario-jsrt: interrupted: ..."). The mini-program host sets it for
 * the programs it runs; unset, turns run as long as they take.
 *
 * The storage file is written on a thread of its own (store.c): the exit
 * and SIGTERM wait for the write under way; SIGTERM then ends the process
 * with exit status 0, as on the device it cannot with the signal.
 *
 * KARIO_JSRT_DEBUG_FD is the debugger's socket (debug.c), which the Shell
 * gives an app installed in developer mode that the computer debugs: the
 * module waits for the debugger before it runs. KARIO_JSRT_DEBUG_ATTACH_FD
 * is a socket on which the Shell may hand over such a socket later, to a
 * debugger that attaches to the running app.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <limits.h>
#include <libgen.h>
#include <poll.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "jsrt.h"

/* Bytecode files start with this, then the engine's version (one length
 * byte, the text): QuickJS bytecode only loads into the version that wrote
 * it, so another version's falls back to the source. */
#define BC_MAGIC "KJSC"

static int64_t budget_ms, turn_start;
static int turn_interrupted;

static void turn(void)
{
	turn_start = jsrt_now_ms();
	turn_interrupted = 0;
}

void jsrt_turn_restart(void) { turn(); }

static int over_budget(JSContext *ctx, void *opaque)
{
	if (jsrt_now_ms() - turn_start < budget_ms)
		return 0;
	if (!turn_interrupted) {
		fprintf(stderr, "kario-jsrt: interrupted: a turn ran over %lld ms\n", (long long)budget_ms);
		fflush(stderr);
		turn_interrupted = 1;
	}
	return 1;
}

static uint8_t *read_file(const char *path, size_t *len)
{
	FILE *f = fopen(path, "rbe");
	if (!f)
		return NULL;
	dstr d = {0};
	dstr_puts(&d, "");
	char buf[65536];
	size_t n;
	while ((n = fread(buf, 1, sizeof buf, f)) > 0)
		dstr_put(&d, buf, n);
	int err = ferror(f);
	fclose(f);
	if (err) {
		free(d.s);
		return NULL;
	}
	*len = d.len;
	return (uint8_t *)d.s;
}

static int ends_with(const char *s, const char *suffix)
{
	size_t n = strlen(s), m = strlen(suffix);
	return n >= m && !strcmp(s + n - m, suffix);
}

static const char *base_name(const char *path)
{
	const char *slash = strrchr(path, '/');
	return slash ? slash + 1 : path;
}

static JSModuleDef *load_module(JSContext *ctx, const char *name, void *opaque);

static int compile(const char *in, const char *out, int positions)
{
	JSRuntime *rt = JS_NewRuntime();
	JSContext *ctx = JS_NewContext(rt);
	JS_SetDebugPositions(rt, positions);
	jsrt r = {.rt = rt, .ctx = ctx, .app_id = "", .lang = "en", .writer_fd = -1, .native_fd = -1};
	JS_SetContextOpaque(ctx, &r);
	JS_SetModuleLoaderFunc(rt, NULL, load_module, &r);
	size_t n;
	uint8_t *src = read_file(in, &n);
	if (!src) {
		fprintf(stderr, "kario-jsrt: %s: %s\n", in, strerror(errno));
		JS_FreeContext(ctx);
		JS_FreeRuntime(rt);
		return 1;
	}
	JSValue fn = JS_Eval(ctx, (const char *)src, n, base_name(in), JS_EVAL_TYPE_MODULE | JS_EVAL_FLAG_COMPILE_ONLY);
	free(src);
	size_t size;
	uint8_t *bc = JS_IsException(fn) ? NULL : JS_WriteObject(ctx, &size, fn, JS_WRITE_OBJ_BYTECODE | JS_WRITE_OBJ_STRIP_SOURCE);
	JS_FreeValue(ctx, fn);
	if (!bc) {
		jsrt_report(ctx, in);
		JS_FreeContext(ctx);
		JS_FreeRuntime(rt);
		return 1;
	}
	const char *v = JS_GetVersion();
	unsigned char vlen = (unsigned char)strlen(v);
	if (!strcmp(out, "-")) {
		int err = fwrite(BC_MAGIC, 1, 4, stdout) != 4 || fwrite(&vlen, 1, 1, stdout) != 1 ||
			  fwrite(v, 1, vlen, stdout) != vlen || fwrite(bc, 1, size, stdout) != size || fflush(stdout) != 0;
		if (err)
			fprintf(stderr, "kario-jsrt: standard output: %s\n", strerror(errno));
		js_free(ctx, bc);
		JS_FreeContext(ctx);
		JS_FreeRuntime(rt);
		return err;
	}
	dstr tmp = {0};
	dstr_puts(&tmp, out);
	dstr_puts(&tmp, ".tmp");
	FILE *f = fopen(tmp.s, "wbe");
	int err = !f || fwrite(BC_MAGIC, 1, 4, f) != 4 || fwrite(&vlen, 1, 1, f) != 1 || fwrite(v, 1, vlen, f) != vlen ||
		  fwrite(bc, 1, size, f) != size;
	if (f)
		err |= fclose(f) != 0;
	if (!err)
		err = rename(tmp.s, out) < 0;
	if (err) {
		fprintf(stderr, "kario-jsrt: %s: %s\n", out, strerror(errno));
		unlink(tmp.s);
	}
	free(tmp.s);
	js_free(ctx, bc);
	JS_FreeContext(ctx);
	JS_FreeRuntime(rt);
	return err;
}

/* load_bytecode evaluates path's bytecode; JS_UNINITIALIZED when it is
 * missing or another engine version's. */
static JSValue load_bytecode(jsrt *r, const char *path)
{
	size_t n;
	uint8_t *buf = read_file(path, &n);
	if (!buf)
		return JS_UNINITIALIZED;
	const char *v = JS_GetVersion();
	size_t vlen = strlen(v);
	if (n < 5 || memcmp(buf, BC_MAGIC, 4) || buf[4] != vlen || n < 5 + vlen || memcmp(buf + 5, v, vlen)) {
		fprintf(stderr, "kario-jsrt: %s is not bytecode of QuickJS-ng %s; running the source\n", path, v);
		free(buf);
		return JS_UNINITIALIZED;
	}
	JSValue m = JS_ReadObject(r->ctx, buf + 5 + vlen, n - 5 - vlen, JS_READ_OBJ_BYTECODE);
	free(buf);
	if (JS_IsException(m))
		return m;
	if (JS_ResolveModule(r->ctx, m) < 0) {
		JS_FreeValue(r->ctx, m);
		return JS_EXCEPTION;
	}
	return JS_EvalFunction(r->ctx, m);
}

static JSValue load_main(jsrt *r, const char *path, int bytecode, const char *bc_file)
{
	if (ends_with(path, ".jsc")) {
		JSValue v = load_bytecode(r, path);
		if (JS_IsUninitialized(v))
			return JS_ThrowReferenceError(r->ctx, "%s: no bytecode for this engine", path);
		return v;
	}
	if (bytecode && bc_file) {
		JSValue v = load_bytecode(r, bc_file);
		if (!JS_IsUninitialized(v))
			return v;
	} else if (bytecode && ends_with(path, ".js")) {
		dstr bc = {0};
		dstr_puts(&bc, path);
		dstr_puts(&bc, "c");
		JSValue v = load_bytecode(r, bc.s);
		free(bc.s);
		if (!JS_IsUninitialized(v))
			return v;
	}
	size_t n;
	uint8_t *src = read_file(path, &n);
	if (!src)
		return JS_ThrowReferenceError(r->ctx, "%s: %s", path, strerror(errno));
	JSValue v = JS_Eval(r->ctx, (const char *)src, n, base_name(path), JS_EVAL_TYPE_MODULE);
	free(src);
	return v;
}

/* "kario", and the app's own native modules (native.c). */
static JSModuleDef *load_module(JSContext *ctx, const char *name, void *opaque)
{
	jsrt *r = opaque;
	if (!strcmp(name, "kario")) {
		if (!r->kario)
			r->kario = jsrt_kario_module(ctx);
		return r->kario;
	}
	if (!strncmp(name, JSRT_NATIVE_SCHEME, strlen(JSRT_NATIVE_SCHEME)) && r->app_dir)
		return jsrt_native_module(r, name);
	JS_ThrowReferenceError(ctx, "could not load module '%s': an app imports only \"kario\" and its native modules (\"native:<name>\")", name);
	return NULL;
}

/* Rejections without a handler yet: a promise rejected at once and awaited
 * right after (a refused fetch) gets its handler in the same turn, so only
 * those still unhandled once the turn's jobs ran are reported. */
#define MAX_REJECTIONS 64
static struct {
	JSValue promise, reason;
} rejected[MAX_REJECTIONS];
static int nrejected;

static void report_rejection(JSContext *ctx, JSValueConst reason)
{
	char *s = jsrt_format(ctx, reason);
	fprintf(stderr, "kario-jsrt: unhandled promise rejection: %s\n", s);
	fflush(stderr);
	free(s);
}

static void rejection(JSContext *ctx, JSValueConst promise, JSValueConst reason, bool handled, void *opaque)
{
	jsrt *r = opaque;
	if (JS_IsSameValue(ctx, promise, r->main))
		return;
	if (handled) {
		for (int i = 0; i < nrejected; i++) {
			if (JS_IsSameValue(ctx, rejected[i].promise, promise)) {
				JS_FreeValue(ctx, rejected[i].promise);
				JS_FreeValue(ctx, rejected[i].reason);
				rejected[i] = rejected[--nrejected];
				break;
			}
		}
		return;
	}
	if (nrejected == MAX_REJECTIONS) {
		report_rejection(ctx, reason);
		return;
	}
	rejected[nrejected].promise = JS_DupValue(ctx, promise);
	rejected[nrejected].reason = JS_DupValue(ctx, reason);
	nrejected++;
}

#ifdef JSRT_TEARDOWN
extern void __lsan_do_leak_check(void) __attribute__((weak));

static void teardown(jsrt *r)
{
	int64_t deadline = jsrt_now_ms() + JSRT_TEARDOWN_WAIT_MS;
	for (int i = 0; i < nrejected; i++) {
		JS_FreeValue(r->ctx, rejected[i].promise);
		JS_FreeValue(r->ctx, rejected[i].reason);
	}
	nrejected = 0;
	JS_FreeValue(r->ctx, r->main);
	r->main = JS_UNDEFINED;
	jsrt_debug_free(r);
	jsrt_kario_free(r);
#ifdef KARIO_JSRT_NET
	jsrt_net_stop(r, deadline);
#endif
	jsrt_native_free(r, deadline);
	jsrt_store_free(r);
	JS_FreeContext(r->ctx);
	JS_FreeRuntime(r->rt);
#ifdef KARIO_JSRT_NET
	jsrt_net_free(r);
#endif
	free(r->app_dir);
	r->app_dir = NULL;
}
#endif

/* end, once every write is done: with JSRT_TEARDOWN the process ends here
 * with status, after the teardown and LeakSanitizer's look at what is left,
 * and before any destructor runs under a worker that is still busy; other
 * builds return. */
static void end(jsrt *r, int status)
{
#ifdef JSRT_TEARDOWN
	teardown(r);
	if (__lsan_do_leak_check)
		__lsan_do_leak_check();
	_exit(status);
#endif
}

/* check_main reports the turn's unhandled rejections, and ends the process
 * when the main module failed (it threw at the top level, or a top-level
 * await rejected): the Shell records a crash. */
static void check_main(jsrt *r)
{
	for (int i = 0; i < nrejected; i++) {
		report_rejection(r->ctx, rejected[i].reason);
		JS_FreeValue(r->ctx, rejected[i].promise);
		JS_FreeValue(r->ctx, rejected[i].reason);
	}
	nrejected = 0;
	if (JS_IsUndefined(r->main))
		return;
	switch (JS_PromiseState(r->ctx, r->main)) {
	case JS_PROMISE_PENDING:
		return;
	case JS_PROMISE_REJECTED: {
		JSValue e = JS_PromiseResult(r->ctx, r->main);
		char *s = jsrt_format(r->ctx, e);
		fprintf(stderr, "kario-jsrt: the app failed to start: %s\n", s);
		free(s);
		JS_FreeValue(r->ctx, e);
		jsrt_store_wait(r);
		end(r, 1);
		exit(1);
	}
	default:
		JS_FreeValue(r->ctx, r->main);
		r->main = JS_UNDEFINED;
	}
}

/* drain hands the app the next message from the Shell, and runs its
 * handlers and the jobs they queued; the loop comes back for the rest
 * (phoneui_pending). One at a time, because phoneui answers the pings it
 * reads past: the Shell holds the user's next keys until the ping it sent
 * after an action is answered (shell/input.go), and one read along with
 * the action would let them act on the page the action is replacing. */
static void drain(jsrt *r)
{
	phoneui_msg m;
	int got = phoneui_try_next(r->ui, &m);
	if (got < 0)
		r->closed = 1;
	if (got <= 0)
		return;
	int fds[PHONEUI_MAX_FDS], nfds = 0;
	for (int i = 0; i < phoneui_fds(r->ui) && nfds < PHONEUI_MAX_FDS; i++) {
		int fd = phoneui_take_fd(r->ui, i);
		if (fd >= 0)
			fds[nfds++] = fd;
	}
	char *line = strdup(m.line);
	turn();
	if (line)
		jsrt_dispatch(r, line, fds, nfds);
	else
		jsrt_close_fds(fds, nfds);
	free(line);
	jsrt_run_jobs(r);
	check_main(r);
}

static void loop(jsrt *r)
{
	for (;;) {
		turn();
		jsrt_run_jobs(r);
		check_main(r);
		jsrt_fire_timers(r);
		check_main(r);
		if (r->closed)
			return;
		int timeout = -1;
		int64_t due = jsrt_next_due(r);
		if (due >= 0) {
			int64_t d = due - jsrt_now_ms();
			timeout = d < 0 ? 0 : d > INT_MAX ? INT_MAX : (int)d;
		}
		if (JS_IsJobPending(r->rt) || phoneui_pending(r->ui))
			timeout = 0;
		struct pollfd p[7] = {{.fd = r->fd, .events = POLLIN},
				      {.fd = r->native_fd, .events = POLLIN},
				      {.fd = r->net_fd, .events = POLLIN},
				      {.fd = r->writer_fd, .events = POLLIN},
				      {.fd = jsrt_debug_fd(r), .events = POLLIN},
				      {.fd = jsrt_debug_listen_fd(r), .events = POLLIN},
				      {.fd = jsrt_term_fd(), .events = POLLIN}};
		int n = poll(p, 7, timeout);
		if (n < 0 && errno != EINTR) {
			perror("kario-jsrt: poll");
			return;
		}
		if (n > 0 && p[6].revents)
			return;
		if (n > 0 && p[4].revents) {
			turn();
			jsrt_debug_service(r);
			check_main(r);
		}
		if (n > 0 && p[5].revents) {
			turn();
			jsrt_debug_accept(r);
			check_main(r);
		}
		if (n > 0 && p[1].revents) {
			turn();
			jsrt_native_drain(r);
			check_main(r);
		}
		if (n > 0 && p[3].revents) {
			turn();
			jsrt_store_drain(r);
			check_main(r);
		}
#ifdef KARIO_JSRT_NET
		if (n > 0 && p[2].revents) {
			turn();
			jsrt_net_drain(r);
			check_main(r);
		}
#endif
		if ((n > 0 && p[0].revents) || phoneui_pending(r->ui))
			drain(r);
		if (r->closed)
			return;
	}
}

int main(int argc, char **argv)
{
	int bytecode = 1, positions = 0;
	const char *path = NULL, *bc_file = NULL;
	for (int i = 1; i < argc; i++) {
		if (!strcmp(argv[i], "--version")) {
			printf("kario-jsrt (QuickJS-ng %s)\n", JS_GetVersion());
			return 0;
		} else if (!strcmp(argv[i], "--positions")) {
			positions = 1;
		} else if (!strcmp(argv[i], "--compile") && i + 2 < argc) {
			return compile(argv[i + 1], argv[i + 2], positions);
		} else if (!strcmp(argv[i], "--no-bytecode")) {
			bytecode = 0;
		} else if (!strcmp(argv[i], "--bytecode") && i + 1 < argc) {
			bc_file = argv[++i];
		} else if (argv[i][0] != '-' && !path) {
			path = argv[i];
		} else {
			path = NULL;
			break;
		}
	}
	if (!path) {
		fprintf(stderr, "usage: kario-jsrt [--no-bytecode | --bytecode FILE] APP.js | [--positions] --compile IN.js OUT.jsc|- | --version\n");
		return 2;
	}
	signal(SIGPIPE, SIG_IGN);
	jsrt_catch_term();
	jsrt r = {.main = JS_UNDEFINED, .catalog = JS_UNDEFINED, .store = JS_UNDEFINED, .writer_fd = -1, .native_fd = -1, .net_fd = -1};
	for (int i = 0; i < EV_COUNT; i++)
		r.on[i] = JS_UNDEFINED;
	const char *app = getenv("PHONE_APP_ID");
	r.app_id = app ? app : "";
	r.data_dir = getenv("PHONE_DATA_DIR");
	r.lang = jsrt_canonical(getenv("PHONE_LANG"));
	const char *clock = getenv("PHONE_CLOCK_OFFSET_MS");
	r.clock_ms = clock ? strtoll(clock, NULL, 10) : 0;
	char *abs = realpath(path, NULL);
	if (!abs) {
		fprintf(stderr, "kario-jsrt: %s: %s\n", path, strerror(errno));
		return 1;
	}
	r.app_dir = strdup(dirname(abs));
	free(abs);
	const char *fd = getenv("PHONE_UI_FD");
	r.ui = phoneui_open();
	if (!r.ui || !fd) {
		fprintf(stderr, "kario-jsrt: not started by phone-shell (no PHONE_UI_FD)\n");
		phoneui_close(r.ui);
		free(r.app_dir);
		return 1;
	}
	r.fd = atoi(fd);
	r.rt = JS_NewRuntime();
	r.ctx = JS_NewContext(r.rt);
	if (!r.rt || !r.ctx) {
		fprintf(stderr, "kario-jsrt: out of memory\n");
		return 1;
	}
	JS_SetContextOpaque(r.ctx, &r);
	JS_SetModuleLoaderFunc(r.rt, NULL, load_module, &r);
	JS_SetHostPromiseRejectionTracker(r.rt, rejection, &r);
	jsrt_add_globals(r.ctx);
	jsrt_web_init(r.ctx);
	jsrt_files_init(r.ctx);
#ifdef KARIO_JSRT_NET
	jsrt_net_init(&r);
#endif
	const char *attach_fd = getenv("KARIO_JSRT_DEBUG_ATTACH_FD");
	const char *debug_fd = getenv("KARIO_JSRT_DEBUG_FD");
	if (jsrt_native_preload(&r) < 0 ||
	    (attach_fd && *attach_fd && jsrt_debug_listen(&r, atoi(attach_fd), base_name(path)) < 0) ||
	    (debug_fd && *debug_fd && jsrt_debug_start(&r, atoi(debug_fd), base_name(path)) < 0)) {
		end(&r, 1);
		return 1;
	}
	const char *budget = getenv("KARIO_JSRT_BUDGET_MS");
	if (budget && atoll(budget) > 0) {
		budget_ms = atoll(budget);
		JS_SetInterruptHandler(r.rt, over_budget, NULL);
	}
	turn();
	r.main = load_main(&r, path, bytecode, bc_file);
	if (JS_IsException(r.main)) {
		jsrt_report(r.ctx, "the app failed to start");
		end(&r, 1);
		return 1;
	}
	loop(&r);
	jsrt_store_wait(&r);
	phoneui_close(r.ui);
	end(&r, 0);
	/* Native and fetch workers may still be inside a module or libcurl: no
	 * destructors under their feet. */
	if (r.native || r.net)
		_exit(0);
	return 0;
}
