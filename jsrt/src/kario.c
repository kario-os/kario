/* The one module app code sees ("kario") and the few globals of the event
 * loop (timers, console). Nothing here opens a file, a socket or a process
 * on the app's behalf except the storage file in its data directory, whose
 * path JavaScript never sees. */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include "jsrt.h"

/* At most this much JSON in an app's storage (as the mini-program host). */
#define MAX_STORAGE (1 << 20)
/* The Shell keeps at most this many pages; a push beyond replaces the top. */
#define MAX_STACK 32
#define STORAGE_FILE "kario-storage.json"

const char *const jsrt_event_names[EV_COUNT] = {
	[EV_START] = "start",
	[EV_ACTION] = "action",
	[EV_KEY] = "key",
	[EV_INPUT] = "input",
	[EV_POPPED] = "popped",
	[EV_FOREGROUND] = "foreground",
	[EV_BACKGROUND] = "background",
	[EV_SAVE] = "save",
	[EV_EVENT] = "event",
	[EV_NOTIF] = "notif",
	[EV_RESYNC] = "resync",
};

void dstr_put(dstr *d, const char *s, size_t n)
{
	if (d->len + n + 1 > d->cap) {
		size_t cap = d->cap ? d->cap : 256;
		while (d->len + n + 1 > cap)
			cap *= 2;
		char *p = realloc(d->s, cap);
		if (!p) {
			perror("kario-jsrt");
			exit(1);
		}
		d->s = p;
		d->cap = cap;
	}
	memcpy(d->s + d->len, s, n);
	d->len += n;
	d->s[d->len] = 0;
}

void dstr_puts(dstr *d, const char *s) { dstr_put(d, s, strlen(s)); }

int64_t jsrt_now_ms(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

/* ---- values to text ---- */

char *jsrt_stringify(JSContext *ctx, JSValueConst v)
{
	JSValue s = JS_JSONStringify(ctx, v, JS_UNDEFINED, JS_UNDEFINED);
	if (JS_IsException(s))
		return NULL;
	if (JS_IsUndefined(s)) {
		JS_ThrowTypeError(ctx, "not representable as JSON");
		return NULL;
	}
	size_t n;
	const char *c = JS_ToCStringLen(ctx, &n, s);
	JS_FreeValue(ctx, s);
	if (!c)
		return NULL;
	char *out = strndup(c, n);
	JS_FreeCString(ctx, c);
	return out;
}

static char *to_cstring(JSContext *ctx, JSValueConst v)
{
	const char *c = JS_ToCString(ctx, v);
	if (!c) {
		JS_FreeValue(ctx, JS_GetException(ctx));
		return strdup("?");
	}
	char *out = strdup(c);
	JS_FreeCString(ctx, c);
	return out;
}

/* jsrt_format is how console and error reports show a value. */
char *jsrt_format(JSContext *ctx, JSValueConst v)
{
	if (JS_IsString(v))
		return to_cstring(ctx, v);
	if (JS_IsError(v)) {
		dstr d = {0};
		char *s = to_cstring(ctx, v);
		dstr_puts(&d, s);
		free(s);
		JSValue stack = JS_GetPropertyStr(ctx, v, "stack");
		if (JS_IsString(stack)) {
			char *st = to_cstring(ctx, stack);
			if (*st) {
				dstr_puts(&d, "\n");
				dstr_puts(&d, st);
			}
			free(st);
		}
		JS_FreeValue(ctx, stack);
		return d.s;
	}
	if (JS_IsObject(v) && !JS_IsFunction(ctx, v)) {
		char *j = jsrt_stringify(ctx, v);
		if (j)
			return j;
		JS_FreeValue(ctx, JS_GetException(ctx));
	}
	return to_cstring(ctx, v);
}

void jsrt_report(JSContext *ctx, const char *what)
{
	JSValue e = JS_GetException(ctx);
	char *s = jsrt_format(ctx, e);
	fprintf(stderr, "kario-jsrt: %s: %s\n", what, s);
	fflush(stderr);
	free(s);
	JS_FreeValue(ctx, e);
}

char *jsrt_get_string(JSContext *ctx, JSValueConst obj, const char *name)
{
	JSValue v = JS_GetPropertyStr(ctx, obj, name);
	char *out = NULL;
	if (JS_IsString(v)) {
		const char *c = JS_ToCString(ctx, v);
		if (c) {
			out = strdup(c);
			JS_FreeCString(ctx, c);
		}
	}
	JS_FreeValue(ctx, v);
	return out;
}

JSValue jsrt_error(JSContext *ctx, const char *message, const char *cap)
{
	JSValue e = JS_NewError(ctx);
	JS_DefinePropertyValueStr(ctx, e, "message", JS_NewString(ctx, message), JS_PROP_WRITABLE | JS_PROP_CONFIGURABLE);
	if (cap)
		JS_DefinePropertyValueStr(ctx, e, "cap", JS_NewString(ctx, cap), JS_PROP_C_W_E);
	return e;
}

int jsrt_send(jsrt *r, const char *json)
{
	if (r->closed || phoneui_send(r->ui, json) < 0) {
		r->closed = 1;
		return -1;
	}
	return 0;
}

void jsrt_run_jobs(jsrt *r)
{
	JSContext *ctx;
	for (;;) {
		int n = JS_ExecutePendingJob(r->rt, &ctx);
		if (n == 0)
			return;
		if (n < 0)
			jsrt_report(ctx, "uncaught exception in a job");
	}
}

/* ---- pages ---- */

static void page_free(jsrt_page *p)
{
	free(p->id);
	free(p->json);
}

/* mirror records what a page message does to the Shell's stack. */
static void mirror(jsrt *r, const char *t, const char *id, const char *json)
{
	jsrt_page p = {strdup(id), strdup(json)};
	if (!strcmp(t, "update")) {
		for (size_t i = 0; i < r->nstack; i++) {
			if (!strcmp(r->stack[i].id, id)) {
				free(r->stack[i].json);
				r->stack[i].json = strdup(json);
			}
		}
		page_free(&p);
		return;
	}
	if (!strcmp(t, "reset")) {
		for (size_t i = 0; i < r->nstack; i++)
			page_free(&r->stack[i]);
		r->nstack = 0;
	}
	if (r->nstack && (!strcmp(t, "replace") || (!strcmp(t, "push") && r->nstack >= MAX_STACK))) {
		page_free(&r->stack[r->nstack - 1]);
		r->stack[r->nstack - 1] = p;
		return;
	}
	jsrt_page *s = realloc(r->stack, (r->nstack + 1) * sizeof *s);
	if (!s) {
		page_free(&p);
		return;
	}
	r->stack = s;
	r->stack[r->nstack++] = p;
}

static JSValue send_page(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic)
{
	static const char *const types[] = {"reset", "push", "replace", "update"};
	const char *t = types[magic];
	jsrt *r = jsrt_of(ctx);
	if (argc < 1 || !JS_IsObject(argv[0]) || JS_IsArray(argv[0]))
		return JS_ThrowTypeError(ctx, "%s: the argument is a screen object", t);
	char *id = jsrt_get_string(ctx, argv[0], "id");
	if (!id)
		return JS_ThrowTypeError(ctx, "%s: the screen needs an id", t);
	char *json = jsrt_stringify(ctx, argv[0]);
	if (!json) {
		free(id);
		return JS_EXCEPTION;
	}
	mirror(r, t, id, json);
	dstr d = {0};
	dstr_puts(&d, "{\"t\":\"");
	dstr_puts(&d, t);
	dstr_puts(&d, "\",\"screen\":");
	dstr_puts(&d, json);
	dstr_puts(&d, "}");
	jsrt_send(r, d.s);
	free(d.s);
	free(json);
	free(id);
	return JS_UNDEFINED;
}

static JSValue js_pop(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	jsrt_send(jsrt_of(ctx), "{\"t\":\"pop\"}");
	return JS_UNDEFINED;
}

/* send_value sends {"t": t, member: JSON of v}. */
static JSValue send_value(JSContext *ctx, const char *t, const char *member, JSValueConst v)
{
	char *json = jsrt_stringify(ctx, v);
	if (!json)
		return JS_EXCEPTION;
	dstr d = {0};
	dstr_puts(&d, "{\"t\":\"");
	dstr_puts(&d, t);
	dstr_puts(&d, "\",\"");
	dstr_puts(&d, member);
	dstr_puts(&d, "\":");
	dstr_puts(&d, json);
	dstr_puts(&d, "}");
	jsrt_send(jsrt_of(ctx), d.s);
	free(d.s);
	free(json);
	return JS_UNDEFINED;
}

static JSValue js_toast(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	JSValue s = JS_ToString(ctx, argc ? argv[0] : JS_UNDEFINED);
	if (JS_IsException(s))
		return s;
	JSValue ret = send_value(ctx, "toast", "text", s);
	JS_FreeValue(ctx, s);
	return ret;
}

static JSValue js_notify(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	if (argc < 1 || !JS_IsObject(argv[0]))
		return JS_ThrowTypeError(ctx, "notify: the argument is a notification object");
	return send_value(ctx, "notify", "notif", argv[0]);
}

/* ---- capabilities ---- */

static JSValue js_call(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	jsrt *r = jsrt_of(ctx);
	if (argc < 1 || !JS_IsString(argv[0]))
		return JS_ThrowTypeError(ctx, "call: the capability's name is a string");
	char *args = NULL;
	if (argc > 1 && !JS_IsUndefined(argv[1]) && !(args = jsrt_stringify(ctx, argv[1])))
		return JS_EXCEPTION;
	char *cap = jsrt_stringify(ctx, argv[0]);
	if (!cap) {
		free(args);
		return JS_EXCEPTION;
	}
	JSValue funcs[2];
	JSValue promise = JS_NewPromiseCapability(ctx, funcs);
	if (JS_IsException(promise)) {
		free(args);
		free(cap);
		return promise;
	}
	long id = ++r->next_req;
	char head[64];
	snprintf(head, sizeof head, "{\"t\":\"req\",\"req\":%ld,\"cap\":", id);
	dstr d = {0};
	dstr_puts(&d, head);
	dstr_puts(&d, cap);
	if (args) {
		dstr_puts(&d, ",\"args\":");
		dstr_puts(&d, args);
	}
	dstr_puts(&d, "}");
	const char *name = JS_ToCString(ctx, argv[0]);
	jsrt_call *calls = realloc(r->calls, (r->ncalls + 1) * sizeof *calls);
	if (jsrt_send(r, d.s) < 0 || !calls) {
		if (calls)
			r->calls = calls;
		JSValue e = jsrt_error(ctx, "connection lost", name);
		JSValue ret = JS_Call(ctx, funcs[1], JS_UNDEFINED, 1, (JSValueConst *)&e);
		JS_FreeValue(ctx, ret);
		JS_FreeValue(ctx, e);
		JS_FreeValue(ctx, funcs[0]);
		JS_FreeValue(ctx, funcs[1]);
	} else {
		r->calls = calls;
		r->calls[r->ncalls++] = (jsrt_call){id, strdup(name ? name : ""), funcs[0], funcs[1]};
	}
	JS_FreeCString(ctx, name);
	free(d.s);
	free(args);
	free(cap);
	return promise;
}

static JSValue js_on(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	jsrt *r = jsrt_of(ctx);
	const char *name = argc ? JS_ToCString(ctx, argv[0]) : NULL;
	if (!name)
		return JS_EXCEPTION;
	int ev = -1;
	for (int i = 0; i < EV_COUNT; i++)
		if (!strcmp(name, jsrt_event_names[i]))
			ev = i;
	if (ev < 0) {
		JSValue e = JS_ThrowTypeError(ctx, "on: no event \"%s\"", name);
		JS_FreeCString(ctx, name);
		return e;
	}
	JS_FreeCString(ctx, name);
	JSValueConst fn = argc > 1 ? argv[1] : JS_UNDEFINED;
	if (!JS_IsFunction(ctx, fn) && !JS_IsUndefined(fn) && !JS_IsNull(fn))
		return JS_ThrowTypeError(ctx, "on: the handler is a function");
	JS_FreeValue(ctx, r->on[ev]);
	r->on[ev] = JS_IsFunction(ctx, fn) ? JS_DupValue(ctx, fn) : JS_UNDEFINED;
	return JS_UNDEFINED;
}

static JSValue js_cancelled(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	if (argc < 1 || !JS_IsObject(argv[0]))
		return JS_FALSE;
	char *m = jsrt_get_string(ctx, argv[0], "message");
	static const char suffix[] = "cancelled";
	size_t n = m ? strlen(m) : 0;
	bool yes = n >= sizeof suffix - 1 && !strcmp(m + n - (sizeof suffix - 1), suffix);
	free(m);
	return JS_NewBool(ctx, yes);
}

/* ---- files the runtime reads and writes for the app ---- */

static char *read_text(const char *path, size_t *len)
{
	int fd = open(path, O_RDONLY | O_CLOEXEC);
	if (fd < 0)
		return NULL;
	dstr d = {0};
	dstr_puts(&d, "");
	char buf[16384];
	for (;;) {
		ssize_t n = read(fd, buf, sizeof buf);
		if (n < 0 && errno == EINTR)
			continue;
		if (n <= 0)
			break;
		dstr_put(&d, buf, (size_t)n);
	}
	close(fd);
	if (len)
		*len = d.len;
	return d.s;
}

/* copy_strings copies the string members of the JSON object in text into dst. */
static void copy_strings(JSContext *ctx, JSValueConst dst, const char *text, size_t len, const char *name)
{
	JSValue obj = JS_ParseJSON(ctx, text, len, name);
	if (JS_IsException(obj)) {
		jsrt_report(ctx, name);
		return;
	}
	JSPropertyEnum *tab;
	uint32_t n;
	if (JS_IsObject(obj) && !JS_GetOwnPropertyNames(ctx, &tab, &n, obj, JS_GPN_STRING_MASK | JS_GPN_ENUM_ONLY)) {
		for (uint32_t i = 0; i < n; i++) {
			JSValue v = JS_GetProperty(ctx, obj, tab[i].atom);
			if (JS_IsString(v))
				JS_DefinePropertyValue(ctx, dst, tab[i].atom, v, JS_PROP_C_W_E);
			else
				JS_FreeValue(ctx, v);
		}
		JS_FreePropertyEnum(ctx, tab, n);
	}
	JS_FreeValue(ctx, obj);
}

/* ---- translations ---- */

/* The app's catalogs, locale/<language>.json beside its code, merged along
 * the language's fallback chain (zh-Hant: zh-Hant, zh-Hans, English). */
static JSValue catalog(jsrt *r)
{
	if (!JS_IsUndefined(r->catalog))
		return r->catalog;
	JSContext *ctx = r->ctx;
	r->catalog = JS_NewObjectProto(ctx, JS_NULL);
	const char *chain[8];
	int n = jsrt_chain(r->lang, chain, 8);
	for (int i = n - 1; i >= 0; i--) {
		dstr path = {0};
		dstr_puts(&path, r->app_dir);
		dstr_puts(&path, "/locale/");
		dstr_puts(&path, chain[i]);
		dstr_puts(&path, ".json");
		size_t len;
		char *text = read_text(path.s, &len);
		if (text)
			copy_strings(ctx, r->catalog, text, len, path.s);
		free(text);
		free(path.s);
	}
	return r->catalog;
}

/* js_tr is msg in the device's language, with args in place of its verbs:
 * %s and %v any value, %d an integer, %% a percent sign (Go's fmt for the
 * verbs the catalogs use, so a message reads the same in every SDK). */
static JSValue js_tr(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	jsrt *r = jsrt_of(ctx);
	if (argc < 1)
		return JS_NewString(ctx, "");
	const char *msg = JS_ToCString(ctx, argv[0]);
	if (!msg)
		return JS_EXCEPTION;
	JSValue t = JS_GetPropertyStr(ctx, catalog(r), msg);
	const char *f = JS_IsString(t) ? JS_ToCString(ctx, t) : NULL;
	const char *p = f ? f : msg;
	dstr d = {0};
	dstr_puts(&d, "");
	int next = 1;
	for (; *p; p++) {
		if (*p != '%' || !p[1]) {
			dstr_put(&d, p, 1);
			continue;
		}
		char verb = *++p;
		if (verb == '%') {
			dstr_puts(&d, "%");
			continue;
		}
		if (verb != 's' && verb != 'v' && verb != 'd') {
			dstr_put(&d, p - 1, 2);
			continue;
		}
		if (next >= argc) {
			char miss[24];
			snprintf(miss, sizeof miss, "%%!%c(MISSING)", verb);
			dstr_puts(&d, miss);
			continue;
		}
		JSValueConst a = argv[next++];
		if (verb == 'd' && JS_IsNumber(a)) {
			double x;
			JS_ToFloat64(ctx, &x, a);
			char num[32];
			snprintf(num, sizeof num, "%.0f", isfinite(x) ? trunc(x) : 0);
			dstr_puts(&d, num);
			continue;
		}
		char *s = to_cstring(ctx, a);
		dstr_puts(&d, s);
		free(s);
	}
	JSValue out = JS_NewStringLen(ctx, d.s, d.len);
	free(d.s);
	if (f)
		JS_FreeCString(ctx, f);
	JS_FreeValue(ctx, t);
	JS_FreeCString(ctx, msg);
	return out;
}

/* ---- storage: a key-value store in the app's data directory ---- */

static int store_open(JSContext *ctx, jsrt *r)
{
	if (!JS_IsUndefined(r->store))
		return 0;
	if (!r->data_dir || !*r->data_dir) {
		JS_ThrowInternalError(ctx, "storage: the app has no data directory");
		return -1;
	}
	r->store = JS_NewObjectProto(ctx, JS_NULL);
	dstr path = {0};
	dstr_puts(&path, r->data_dir);
	dstr_puts(&path, "/" STORAGE_FILE);
	size_t len;
	char *text = read_text(path.s, &len);
	if (text)
		copy_strings(ctx, r->store, text, len, "storage");
	free(text);
	free(path.s);
	return 0;
}

/* store_write replaces the file whole (a new file, then a rename): a
 * process killed meanwhile leaves the old one, never half of the new. */
static int store_write(JSContext *ctx, jsrt *r)
{
	char *json = jsrt_stringify(ctx, r->store);
	if (!json)
		return -1;
	size_t n = strlen(json);
	if (n > MAX_STORAGE) {
		free(json);
		JS_ThrowRangeError(ctx, "storage: over %d KB", MAX_STORAGE >> 10);
		return -1;
	}
	dstr path = {0}, tmp = {0};
	dstr_puts(&path, r->data_dir);
	dstr_puts(&path, "/" STORAGE_FILE);
	dstr_puts(&tmp, path.s);
	dstr_puts(&tmp, ".tmp");
	int fd = open(tmp.s, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
	int err = fd < 0;
	for (size_t off = 0; !err && off < n;) {
		ssize_t w = write(fd, json + off, n - off);
		if (w < 0 && errno == EINTR)
			continue;
		if (w <= 0)
			err = 1;
		else
			off += (size_t)w;
	}
	if (fd >= 0) {
		err |= fsync(fd) < 0;
		err |= close(fd) < 0;
	}
	if (!err)
		err = rename(tmp.s, path.s) < 0;
	if (err) {
		JS_ThrowInternalError(ctx, "storage: %s", strerror(errno));
		unlink(tmp.s);
	}
	free(path.s);
	free(tmp.s);
	free(json);
	return err ? -1 : 0;
}

static JSValue js_storage_get(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	jsrt *r = jsrt_of(ctx);
	if (store_open(ctx, r) < 0)
		return JS_EXCEPTION;
	JSAtom key = JS_ValueToAtom(ctx, argc ? argv[0] : JS_UNDEFINED);
	if (key == JS_ATOM_NULL)
		return JS_EXCEPTION;
	JSValue v = JS_GetProperty(ctx, r->store, key);
	JS_FreeAtom(ctx, key);
	if (!JS_IsString(v)) {
		JS_FreeValue(ctx, v);
		return JS_UNDEFINED;
	}
	size_t n;
	const char *s = JS_ToCStringLen(ctx, &n, v);
	JS_FreeValue(ctx, v);
	if (!s)
		return JS_EXCEPTION;
	JSValue out = JS_ParseJSON(ctx, s, n, "storage");
	JS_FreeCString(ctx, s);
	return out;
}

/* store_put sets key to json (NULL removes it) and writes the file; on
 * failure the key keeps what it had. */
static JSValue store_put(JSContext *ctx, JSValueConst keyv, const char *json)
{
	jsrt *r = jsrt_of(ctx);
	if (store_open(ctx, r) < 0)
		return JS_EXCEPTION;
	JSAtom key = JS_ValueToAtom(ctx, keyv);
	if (key == JS_ATOM_NULL)
		return JS_EXCEPTION;
	JSValue old = JS_GetProperty(ctx, r->store, key);
	if (json)
		JS_DefinePropertyValue(ctx, r->store, key, JS_NewString(ctx, json), JS_PROP_C_W_E);
	else
		JS_DeleteProperty(ctx, r->store, key, 0);
	JSValue ret = JS_UNDEFINED;
	if (store_write(ctx, r) < 0) {
		if (JS_IsString(old))
			JS_DefinePropertyValue(ctx, r->store, key, JS_DupValue(ctx, old), JS_PROP_C_W_E);
		else
			JS_DeleteProperty(ctx, r->store, key, 0);
		ret = JS_EXCEPTION;
	}
	JS_FreeValue(ctx, old);
	JS_FreeAtom(ctx, key);
	return ret;
}

static JSValue js_storage_set(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	JSValueConst v = argc > 1 ? argv[1] : JS_UNDEFINED;
	if (JS_IsUndefined(v))
		return store_put(ctx, argc ? argv[0] : JS_UNDEFINED, NULL);
	char *json = jsrt_stringify(ctx, v);
	if (!json)
		return JS_EXCEPTION;
	JSValue ret = store_put(ctx, argc ? argv[0] : JS_UNDEFINED, json);
	free(json);
	return ret;
}

static JSValue js_storage_remove(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	return store_put(ctx, argc ? argv[0] : JS_UNDEFINED, NULL);
}

static JSValue js_storage_keys(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	jsrt *r = jsrt_of(ctx);
	if (store_open(ctx, r) < 0)
		return JS_EXCEPTION;
	JSPropertyEnum *tab;
	uint32_t n;
	if (JS_GetOwnPropertyNames(ctx, &tab, &n, r->store, JS_GPN_STRING_MASK | JS_GPN_ENUM_ONLY))
		return JS_EXCEPTION;
	JSValue arr = JS_NewArray(ctx);
	for (uint32_t i = 0; i < n; i++)
		JS_SetPropertyUint32(ctx, arr, i, JS_AtomToString(ctx, tab[i].atom));
	JS_FreePropertyEnum(ctx, tab, n);
	return arr;
}

static JSValue js_storage_clear(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	jsrt *r = jsrt_of(ctx);
	if (store_open(ctx, r) < 0)
		return JS_EXCEPTION;
	JSValue old = r->store;
	r->store = JS_NewObjectProto(ctx, JS_NULL);
	if (store_write(ctx, r) < 0) {
		JS_FreeValue(ctx, r->store);
		r->store = old;
		return JS_EXCEPTION;
	}
	JS_FreeValue(ctx, old);
	return JS_UNDEFINED;
}

static const JSCFunctionListEntry storage_funcs[] = {
	JS_CFUNC_DEF("get", 1, js_storage_get),
	JS_CFUNC_DEF("set", 2, js_storage_set),
	JS_CFUNC_DEF("remove", 1, js_storage_remove),
	JS_CFUNC_DEF("keys", 0, js_storage_keys),
	JS_CFUNC_DEF("clear", 0, js_storage_clear),
};

/* ---- the module ---- */

static const char *const exports[] = {
	"appId", "lang", "reset", "push", "replace", "update", "pop", "toast",
	"notify", "call", "on", "tr", "storage", "cancelled",
};

static int kario_init(JSContext *ctx, JSModuleDef *m)
{
	jsrt *r = jsrt_of(ctx);
	JS_SetModuleExport(ctx, m, "appId", JS_NewString(ctx, r->app_id));
	JS_SetModuleExport(ctx, m, "lang", JS_NewString(ctx, r->lang));
	JS_SetModuleExport(ctx, m, "reset", JS_NewCFunctionMagic(ctx, send_page, "reset", 1, JS_CFUNC_generic_magic, 0));
	JS_SetModuleExport(ctx, m, "push", JS_NewCFunctionMagic(ctx, send_page, "push", 1, JS_CFUNC_generic_magic, 1));
	JS_SetModuleExport(ctx, m, "replace", JS_NewCFunctionMagic(ctx, send_page, "replace", 1, JS_CFUNC_generic_magic, 2));
	JS_SetModuleExport(ctx, m, "update", JS_NewCFunctionMagic(ctx, send_page, "update", 1, JS_CFUNC_generic_magic, 3));
	JS_SetModuleExport(ctx, m, "pop", JS_NewCFunction(ctx, js_pop, "pop", 0));
	JS_SetModuleExport(ctx, m, "toast", JS_NewCFunction(ctx, js_toast, "toast", 1));
	JS_SetModuleExport(ctx, m, "notify", JS_NewCFunction(ctx, js_notify, "notify", 1));
	JS_SetModuleExport(ctx, m, "call", JS_NewCFunction(ctx, js_call, "call", 2));
	JS_SetModuleExport(ctx, m, "on", JS_NewCFunction(ctx, js_on, "on", 2));
	JS_SetModuleExport(ctx, m, "tr", JS_NewCFunction(ctx, js_tr, "tr", 1));
	JS_SetModuleExport(ctx, m, "cancelled", JS_NewCFunction(ctx, js_cancelled, "cancelled", 1));
	JSValue storage = JS_NewObject(ctx);
	JS_SetPropertyFunctionList(ctx, storage, storage_funcs, sizeof storage_funcs / sizeof storage_funcs[0]);
	JS_SetModuleExport(ctx, m, "storage", storage);
	return 0;
}

JSModuleDef *jsrt_kario_module(JSContext *ctx)
{
	JSModuleDef *m = JS_NewCModule(ctx, "kario", kario_init);
	if (!m)
		return NULL;
	for (size_t i = 0; i < sizeof exports / sizeof exports[0]; i++)
		JS_AddModuleExport(ctx, m, exports[i]);
	return m;
}

/* ---- timers ---- */

static void timer_free(JSContext *ctx, jsrt_timer *t)
{
	JS_FreeValue(ctx, t->fn);
	for (int i = 0; i < t->argc; i++)
		JS_FreeValue(ctx, t->argv[i]);
	free(t->argv);
}

static JSValue js_set_timer(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int interval)
{
	jsrt *r = jsrt_of(ctx);
	if (argc < 1 || !JS_IsFunction(ctx, argv[0]))
		return JS_ThrowTypeError(ctx, "%s: the callback is a function", interval ? "setInterval" : "setTimeout");
	double ms = 0;
	if (argc > 1 && JS_ToFloat64(ctx, &ms, argv[1]))
		return JS_EXCEPTION;
	if (!(ms >= 0))
		ms = 0;
	if (ms > INT32_MAX)
		ms = INT32_MAX;
	jsrt_timer *ts = realloc(r->timers, (r->ntimers + 1) * sizeof *ts);
	if (!ts)
		return JS_ThrowOutOfMemory(ctx);
	r->timers = ts;
	if (r->next_timer == INT32_MAX)
		r->next_timer = 0;
	jsrt_timer t = {++r->next_timer, jsrt_now_ms() + (int64_t)ms, interval ? (ms < 1 ? 1 : (int64_t)ms) : -1,
		JS_DupValue(ctx, argv[0]), argc > 2 ? argc - 2 : 0, NULL};
	if (t.argc) {
		t.argv = calloc((size_t)t.argc, sizeof *t.argv);
		for (int i = 0; t.argv && i < t.argc; i++)
			t.argv[i] = JS_DupValue(ctx, argv[i + 2]);
		if (!t.argv)
			t.argc = 0;
	}
	r->timers[r->ntimers++] = t;
	return JS_NewInt32(ctx, t.id);
}

static JSValue js_clear_timer(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	jsrt *r = jsrt_of(ctx);
	int32_t id;
	if (argc < 1 || !JS_IsNumber(argv[0]) || JS_ToInt32(ctx, &id, argv[0]))
		return JS_UNDEFINED;
	for (size_t i = 0; i < r->ntimers; i++) {
		if (r->timers[i].id == id) {
			timer_free(ctx, &r->timers[i]);
			memmove(&r->timers[i], &r->timers[i + 1], (r->ntimers - i - 1) * sizeof *r->timers);
			r->ntimers--;
			break;
		}
	}
	return JS_UNDEFINED;
}

int64_t jsrt_next_due(jsrt *r)
{
	int64_t due = -1;
	for (size_t i = 0; i < r->ntimers; i++)
		if (due < 0 || r->timers[i].due < due)
			due = r->timers[i].due;
	return due;
}

void jsrt_fire_timers(jsrt *r)
{
	JSContext *ctx = r->ctx;
	/* A bound, so timers that keep coming due do not starve the protocol. */
	for (int round = 0; round < 1000; round++) {
		int64_t now = jsrt_now_ms();
		size_t best = r->ntimers;
		for (size_t i = 0; i < r->ntimers; i++)
			if (r->timers[i].due <= now && (best == r->ntimers || r->timers[i].due < r->timers[best].due))
				best = i;
		if (best == r->ntimers)
			return;
		jsrt_timer *t = &r->timers[best];
		JSValue fn = JS_DupValue(ctx, t->fn);
		int argc = t->argc;
		JSValue *argv = argc ? calloc((size_t)argc, sizeof *argv) : NULL;
		for (int i = 0; argv && i < argc; i++)
			argv[i] = JS_DupValue(ctx, t->argv[i]);
		if (!argv)
			argc = 0;
		if (t->every >= 0) {
			t->due = now + t->every;
		} else {
			timer_free(ctx, t);
			memmove(t, t + 1, (r->ntimers - best - 1) * sizeof *t);
			r->ntimers--;
		}
		JSValue ret = JS_Call(ctx, fn, JS_UNDEFINED, argc, (JSValueConst *)argv);
		if (JS_IsException(ret))
			jsrt_report(ctx, "uncaught exception in a timer");
		JS_FreeValue(ctx, ret);
		JS_FreeValue(ctx, fn);
		for (int i = 0; i < argc; i++)
			JS_FreeValue(ctx, argv[i]);
		free(argv);
		jsrt_run_jobs(r);
	}
}

/* ---- console: lines on stderr, which the Shell keeps as the app's log ---- */

static JSValue js_console(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	dstr d = {0};
	dstr_puts(&d, "");
	for (int i = 0; i < argc; i++) {
		char *s = jsrt_format(ctx, argv[i]);
		if (i)
			dstr_puts(&d, " ");
		dstr_puts(&d, s);
		free(s);
	}
	dstr_puts(&d, "\n");
	fputs(d.s, stderr);
	fflush(stderr);
	free(d.s);
	return JS_UNDEFINED;
}

void jsrt_add_globals(JSContext *ctx)
{
	JSValue g = JS_GetGlobalObject(ctx);
	JSValue console = JS_NewObject(ctx);
	static const char *const levels[] = {"log", "info", "warn", "error", "debug"};
	for (size_t i = 0; i < sizeof levels / sizeof levels[0]; i++)
		JS_SetPropertyStr(ctx, console, levels[i], JS_NewCFunction(ctx, js_console, levels[i], 1));
	JS_SetPropertyStr(ctx, g, "console", console);
	JS_SetPropertyStr(ctx, g, "setTimeout", JS_NewCFunctionMagic(ctx, js_set_timer, "setTimeout", 2, JS_CFUNC_generic_magic, 0));
	JS_SetPropertyStr(ctx, g, "setInterval", JS_NewCFunctionMagic(ctx, js_set_timer, "setInterval", 2, JS_CFUNC_generic_magic, 1));
	JS_SetPropertyStr(ctx, g, "clearTimeout", JS_NewCFunction(ctx, js_clear_timer, "clearTimeout", 1));
	JS_SetPropertyStr(ctx, g, "clearInterval", JS_NewCFunction(ctx, js_clear_timer, "clearInterval", 1));
	JS_FreeValue(ctx, g);
}
