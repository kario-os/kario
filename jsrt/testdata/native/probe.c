/* What native_test.go loads: every part of kario_native.h once. Built by
 * the test (cc -shared); -DPROBE_ABI=... claims another interface version,
 * -DPROBE_INIT_FAILS refuses to load, -DPROBE_RWX_INIT leaves writable
 * executable memory behind its init. */
#define _GNU_SOURCE
#include <ctype.h>
#include <dlfcn.h>
#include <errno.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/personality.h>
#include <unistd.h>

#include "kario_native.h"

static const kario_api *api;
static kario_module *self;
static pthread_t main_thread;

static void add(const kario_api *a, kario_call *c, void *data) { a->ret_number(c, a->number(c, 0) + a->number(c, 1)); }

static void upper(const kario_api *a, kario_call *c, void *data)
{
	size_t n;
	const char *s = a->string(c, 0, &n);
	if (!s) {
		a->fail(c, "upper: the argument is a string");
		return;
	}
	char *out = malloc(n + 1);
	for (size_t i = 0; i < n; i++)
		out[i] = (char)toupper((unsigned char)s[i]);
	a->ret_string(c, out, n);
	free(out);
}

/* invert changes the caller's bytes in place. */
static void invert(const kario_api *a, kario_call *c, void *data)
{
	size_t n;
	unsigned char *p = a->bytes(c, 0, &n);
	for (size_t i = 0; p && i < n; i++)
		p[i] = (unsigned char)(255 - p[i]);
	a->ret_number(c, (double)n);
}

static void reversed(const kario_api *a, kario_call *c, void *data)
{
	size_t n;
	const unsigned char *p = a->bytes(c, 0, &n);
	if (!p) {
		a->fail(c, "reversed: the argument is bytes");
		return;
	}
	unsigned char *out = a->ret_bytes(c, n);
	for (size_t i = 0; out && i < n; i++)
		out[i] = p[n - 1 - i];
}

static void echo(const kario_api *a, kario_call *c, void *data)
{
	size_t n;
	const char *j = a->json(c, 0, &n);
	if (j)
		a->ret_json(c, j, n);
}

static void types(const kario_api *a, kario_call *c, void *data)
{
	static const char code[] = "u0bnsyo";
	char out[64];
	int n = a->argc(c);
	for (int i = 0; i < n && i < 63; i++)
		out[i] = code[a->type(c, i)];
	out[n < 63 ? n : 63] = 0;
	a->ret_string(c, out, strlen(out));
}

static void fail(const kario_api *a, kario_call *c, void *data)
{
	a->ret_number(c, 1);
	a->fail(c, a->string(c, 0, NULL));
}

static void bad_json(const kario_api *a, kario_call *c, void *data) { a->ret_json(c, "{oops", 5); }

static void slow(const kario_api *a, kario_call *c, void *data)
{
	usleep((useconds_t)(a->number(c, 0) * 1000));
	char out[64];
	snprintf(out, sizeof out, "{\"worker\":%s}", pthread_equal(pthread_self(), main_thread) ? "false" : "true");
	a->ret_json(c, out, strlen(out));
}

static void *ticker(void *arg)
{
	int n = (int)(long)arg;
	for (int i = 0; i < n; i++) {
		char j[32];
		snprintf(j, sizeof j, "{\"i\":%d}", i);
		api->emit(self, "tick", j, strlen(j));
	}
	api->emit(self, "done", NULL, 0);
	return NULL;
}

static void start(const kario_api *a, kario_call *c, void *data)
{
	pthread_t t;
	if (pthread_create(&t, NULL, ticker, (void *)(long)a->number(c, 0)) != 0) {
		a->fail(c, "start: no thread");
		return;
	}
	pthread_detach(t);
}

/* fault is a frame of its own below crash, for the backtrace. */
__attribute__((noinline)) static void fault(volatile int *p) { *p = 1; }

static void crash(const kario_api *a, kario_call *c, void *data)
{
	fault(NULL);
	a->ret_number(c, 0);
}

static const char *error_name(int err) { return err == 0 ? "ok" : err == EPERM ? "EPERM" : strerror(err); }

/* exec tries what sealing stops, each by its errno: mapping executable
 * memory, making memory executable, a personality that would, running a
 * program; then dlopen of the library named by the argument. */
static void exec(const kario_api *a, kario_call *c, void *data)
{
	char out[1024];
	void *x = mmap(NULL, 4096, PROT_READ | PROT_EXEC, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	int map_err = x == MAP_FAILED ? errno : 0;
	void *w = mmap(NULL, 4096, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	int write_err = w == MAP_FAILED ? errno : 0;
	int prot_err = w != MAP_FAILED && mprotect(w, 4096, PROT_READ | PROT_EXEC) < 0 ? errno : 0;
	int persona = personality(0xffffffff);
	int persona_err = personality((unsigned long)persona | READ_IMPLIES_EXEC) == -1 ? errno : 0;
	personality((unsigned long)persona);
	/* No such program: unsealed, this fails with ENOENT instead of
	 * replacing the process. */
	char *argv[] = {"none", NULL};
	execv("/nonexistent/kario-probe", argv);
	int exec_err = errno;
	const char *lib = a->string(c, 0, NULL);
	void *h = lib ? dlopen(lib, RTLD_NOW | RTLD_LOCAL) : NULL;
	snprintf(out, sizeof out, "mmap %s, rw %s, mprotect %s, personality %s, exec %s, dlopen %s", error_name(map_err), error_name(write_err),
		 error_name(prot_err), error_name(persona_err), error_name(exec_err), h ? "ok" : "failed");
	a->ret_string(c, out, strlen(out));
}

static void *exiting(void *arg) { pthread_exit(arg); }

/* thread starts a thread that ends with pthread_exit, which glibc
 * unwinds with libgcc: after sealing it must not need to load it. */
static void thread(const kario_api *a, kario_call *c, void *data)
{
	pthread_t t;
	void *got = NULL;
	if (pthread_create(&t, NULL, exiting, (void *)7) != 0 || pthread_join(t, &got) != 0) {
		a->fail(c, "thread: no thread");
		return;
	}
	a->ret_number(c, (double)(long)got);
}

static int init(const kario_api *a, kario_module *m)
{
	api = a;
	self = m;
	main_thread = pthread_self();
	if (a->export_fn(m, "on", add, NULL, 0) == 0 || a->export_fn(m, "no-name", add, NULL, 0) == 0)
		return 9;
	if (a->export_fn(m, "add", add, NULL, 0) || a->export_fn(m, "add", add, NULL, 0) == 0)
		return 8;
	int err = a->export_fn(m, "upper", upper, NULL, 0) | a->export_fn(m, "invert", invert, NULL, 0) |
		  a->export_fn(m, "reversed", reversed, NULL, 0) | a->export_fn(m, "reversedAsync", reversed, NULL, KARIO_ASYNC) |
		  a->export_fn(m, "echo", echo, NULL, 0) | a->export_fn(m, "echoAsync", echo, NULL, KARIO_ASYNC) |
		  a->export_fn(m, "types", types, NULL, 0) | a->export_fn(m, "fail", fail, NULL, 0) |
		  a->export_fn(m, "failAsync", fail, NULL, KARIO_ASYNC) | a->export_fn(m, "badJson", bad_json, NULL, 0) |
		  a->export_fn(m, "slow", slow, NULL, KARIO_ASYNC) | a->export_fn(m, "start", start, NULL, 0) |
		  a->export_fn(m, "crash", crash, NULL, 0) | a->export_fn(m, "crashAsync", crash, NULL, KARIO_ASYNC) |
		  a->export_fn(m, "exec", exec, NULL, 0) | a->export_fn(m, "thread", thread, NULL, 0) |
		  a->export_string(m, "engine", a->engine) |
		  a->export_number(m, "abi", a->major * 100 + a->minor);
	if (err)
		return 6;
#ifdef PROBE_RWX_INIT
	if (mmap(NULL, 4096, PROT_READ | PROT_WRITE | PROT_EXEC, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0) == MAP_FAILED)
		return 5;
#endif
#ifdef PROBE_INIT_FAILS
	return 7;
#else
	return 0;
#endif
}

#ifdef PROBE_ABI
KARIO_NATIVE_EXPORT const uint32_t kario_native_abi = PROBE_ABI;
KARIO_NATIVE_EXPORT int kario_native_init(const kario_api *a, kario_module *m) { return init(a, m); }
#else
KARIO_NATIVE_MODULE(init)
#endif
