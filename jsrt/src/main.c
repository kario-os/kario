/*
 * kario-jsrt runs a TypeScript app, compiled by `kario build` into one
 * JavaScript module, on QuickJS-ng (29-app-development.md). The Shell starts
 * it in the app's sandbox with the module as its argument; it speaks the
 * phone.Ui protocol through the C interface (sdk/c) and gives the module
 * one import, "kario": pages, events, capabilities, translations, storage.
 * QuickJS's std and os modules are not built in and no other module can be
 * imported, so app code has no file, socket or process of its own.
 *
 *	kario-jsrt [--no-bytecode] APP/main.js   run (main.jsc beside it first)
 *	kario-jsrt --compile IN.js OUT.jsc       precompile (the system's build)
 *	kario-jsrt --version
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

static int compile(const char *in, const char *out)
{
	JSRuntime *rt = JS_NewRuntime();
	JSContext *ctx = JS_NewContext(rt);
	jsrt r = {.rt = rt, .ctx = ctx, .app_id = "", .lang = "en"};
	JS_SetContextOpaque(ctx, &r);
	JS_SetModuleLoaderFunc(rt, NULL, load_module, &r);
	size_t n;
	uint8_t *src = read_file(in, &n);
	if (!src) {
		fprintf(stderr, "kario-jsrt: %s: %s\n", in, strerror(errno));
		return 1;
	}
	JSValue fn = JS_Eval(ctx, (const char *)src, n, base_name(in), JS_EVAL_TYPE_MODULE | JS_EVAL_FLAG_COMPILE_ONLY);
	free(src);
	if (JS_IsException(fn)) {
		jsrt_report(ctx, in);
		return 1;
	}
	size_t size;
	uint8_t *bc = JS_WriteObject(ctx, &size, fn, JS_WRITE_OBJ_BYTECODE | JS_WRITE_OBJ_STRIP_SOURCE);
	JS_FreeValue(ctx, fn);
	if (!bc) {
		jsrt_report(ctx, in);
		return 1;
	}
	const char *v = JS_GetVersion();
	unsigned char vlen = (unsigned char)strlen(v);
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

static JSValue load_main(jsrt *r, const char *path, int bytecode)
{
	if (ends_with(path, ".jsc")) {
		JSValue v = load_bytecode(r, path);
		if (JS_IsUninitialized(v))
			return JS_ThrowReferenceError(r->ctx, "%s: no bytecode for this engine", path);
		return v;
	}
	if (bytecode && ends_with(path, ".js")) {
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

/* The only module there is. */
static JSModuleDef *load_module(JSContext *ctx, const char *name, void *opaque)
{
	jsrt *r = opaque;
	if (!strcmp(name, "kario")) {
		if (!r->kario)
			r->kario = jsrt_kario_module(ctx);
		return r->kario;
	}
	JS_ThrowReferenceError(ctx, "could not load module '%s': an app imports only \"kario\"", name);
	return NULL;
}

static void rejection(JSContext *ctx, JSValueConst promise, JSValueConst reason, bool handled, void *opaque)
{
	jsrt *r = opaque;
	if (handled || JS_IsSameValue(ctx, promise, r->main))
		return;
	char *s = jsrt_format(ctx, reason);
	fprintf(stderr, "kario-jsrt: unhandled promise rejection: %s\n", s);
	fflush(stderr);
	free(s);
}

/* check_main ends the process when the main module failed (it threw at the
 * top level, or a top-level await rejected): the Shell records a crash. */
static void check_main(jsrt *r)
{
	if (JS_IsUndefined(r->main))
		return;
	switch (JS_PromiseState(r->ctx, r->main)) {
	case JS_PROMISE_PENDING:
		return;
	case JS_PROMISE_REJECTED: {
		JSValue e = JS_PromiseResult(r->ctx, r->main);
		char *s = jsrt_format(r->ctx, e);
		fprintf(stderr, "kario-jsrt: the app failed to start: %s\n", s);
		exit(1);
	}
	default:
		JS_FreeValue(r->ctx, r->main);
		r->main = JS_UNDEFINED;
	}
}

/* drain hands the app every message that has arrived (none waits inside
 * the C interface afterwards), and only then do handlers run. */
static void drain(jsrt *r)
{
	char **lines = NULL;
	size_t n = 0;
	for (;;) {
		phoneui_msg m;
		int got = phoneui_try_next(r->ui, &m);
		if (got < 0)
			r->closed = 1;
		if (got <= 0)
			break;
		char **l = realloc(lines, (n + 1) * sizeof *l);
		if (!l)
			break;
		lines = l;
		lines[n++] = strdup(m.line);
	}
	for (size_t i = 0; i < n; i++) {
		if (lines[i])
			jsrt_dispatch(r, lines[i]);
		free(lines[i]);
		jsrt_run_jobs(r);
		check_main(r);
	}
	free(lines);
}

static void loop(jsrt *r)
{
	for (;;) {
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
		struct pollfd p = {.fd = r->fd, .events = POLLIN};
		int n = poll(&p, 1, timeout);
		if (n < 0 && errno != EINTR) {
			perror("kario-jsrt: poll");
			return;
		}
		if (n > 0 || phoneui_pending(r->ui))
			drain(r);
		if (r->closed)
			return;
	}
}

int main(int argc, char **argv)
{
	int bytecode = 1;
	const char *path = NULL;
	for (int i = 1; i < argc; i++) {
		if (!strcmp(argv[i], "--version")) {
			printf("kario-jsrt (QuickJS-ng %s)\n", JS_GetVersion());
			return 0;
		} else if (!strcmp(argv[i], "--compile") && i + 2 < argc) {
			return compile(argv[i + 1], argv[i + 2]);
		} else if (!strcmp(argv[i], "--no-bytecode")) {
			bytecode = 0;
		} else if (argv[i][0] != '-' && !path) {
			path = argv[i];
		} else {
			path = NULL;
			break;
		}
	}
	if (!path) {
		fprintf(stderr, "usage: kario-jsrt [--no-bytecode] APP.js | --compile IN.js OUT.jsc | --version\n");
		return 2;
	}
	signal(SIGPIPE, SIG_IGN);
	jsrt r = {.main = JS_UNDEFINED, .catalog = JS_UNDEFINED, .store = JS_UNDEFINED};
	for (int i = 0; i < EV_COUNT; i++)
		r.on[i] = JS_UNDEFINED;
	const char *app = getenv("PHONE_APP_ID");
	r.app_id = app ? app : "";
	r.data_dir = getenv("PHONE_DATA_DIR");
	r.lang = jsrt_canonical(getenv("PHONE_LANG"));
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
	r.main = load_main(&r, path, bytecode);
	if (JS_IsException(r.main)) {
		jsrt_report(r.ctx, "the app failed to start");
		return 1;
	}
	loop(&r);
	phoneui_close(r.ui);
	return 0;
}
