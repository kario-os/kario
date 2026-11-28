/*
 * kario-native.node: native modules (kario_native.h) under Node.js, so an
 * app with native code runs and is tested on the developer machine as it
 * is, debugged in VS Code: the same library kario-jsrt loads on the phone,
 * through Node-API instead of QuickJS (native.mjs checks what kario-jsrt
 * checks before this loads it). kario run and kario test build it with the
 * machine's C compiler (cc -shared -fPIC native.c); Node-API is a stable C
 * interface of the node program, so the few functions used are declared
 * here and no Node headers are needed (-DKARIO_NODE_HEADERS checks these
 * declarations against node_api.h).
 *
 * Differences from kario-jsrt: asynchronous functions run on libuv's
 * thread pool (4 threads unless UV_THREADPOOL_SIZE says otherwise), shared
 * with Node's own work; there is no line in the log when native code
 * crashes (V8 keeps SIGSEGV for itself); modules load at their import, and
 * nothing is sealed afterwards (V8 makes machine code all along); Node.js
 * is no sandbox.
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <math.h>
#include <poll.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#include "kario_native.h"

#ifdef KARIO_NODE_HEADERS
#include <node_api.h>
#else
typedef struct napi_env__ *napi_env;
typedef struct napi_value__ *napi_value;
typedef struct napi_ref__ *napi_ref;
typedef struct napi_callback_info__ *napi_callback_info;
typedef struct napi_deferred__ *napi_deferred;
typedef struct napi_async_work__ *napi_async_work;
typedef struct napi_threadsafe_function__ *napi_threadsafe_function;
typedef enum { napi_ok } napi_status;
typedef enum {
	napi_undefined,
	napi_null,
	napi_boolean,
	napi_number,
	napi_string,
	napi_symbol,
	napi_object,
	napi_function,
	napi_external,
	napi_bigint
} napi_valuetype;
typedef enum {
	napi_int8_array,
	napi_uint8_array,
	napi_uint8_clamped_array,
	napi_int16_array,
	napi_uint16_array,
	napi_int32_array,
	napi_uint32_array,
	napi_float32_array,
	napi_float64_array,
	napi_bigint64_array,
	napi_biguint64_array,
	napi_float16_array
} napi_typedarray_type;
typedef enum { napi_tsfn_nonblocking, napi_tsfn_blocking } napi_threadsafe_function_call_mode;
typedef enum { napi_tsfn_release, napi_tsfn_abort } napi_threadsafe_function_release_mode;
typedef struct {
	uint32_t major, minor, patch;
	const char *release;
} napi_node_version;
typedef napi_value (*napi_callback)(napi_env env, napi_callback_info info);
typedef void (*napi_finalize)(napi_env env, void *data, void *hint);
typedef void (*napi_async_execute_callback)(napi_env env, void *data);
typedef void (*napi_async_complete_callback)(napi_env env, napi_status status, void *data);
typedef void (*napi_threadsafe_function_call_js)(napi_env env, napi_value js_callback, void *context, void *data);
#endif

/* With KARIO_NODE_HEADERS the compiler compares these with node_api.h's. */
napi_status napi_get_cb_info(napi_env, napi_callback_info, size_t *, napi_value *, napi_value *, void **);
napi_status napi_typeof(napi_env, napi_value, napi_valuetype *);
napi_status napi_get_value_double(napi_env, napi_value, double *);
napi_status napi_get_value_bool(napi_env, napi_value, bool *);
napi_status napi_coerce_to_bool(napi_env, napi_value, napi_value *);
napi_status napi_get_value_string_utf8(napi_env, napi_value, char *, size_t, size_t *);
napi_status napi_is_arraybuffer(napi_env, napi_value, bool *);
napi_status napi_get_arraybuffer_info(napi_env, napi_value, void **, size_t *);
napi_status napi_is_typedarray(napi_env, napi_value, bool *);
napi_status napi_get_typedarray_info(napi_env, napi_value, napi_typedarray_type *, size_t *, void **, napi_value *, size_t *);
napi_status napi_create_double(napi_env, double, napi_value *);
napi_status napi_get_boolean(napi_env, bool, napi_value *);
napi_status napi_create_string_utf8(napi_env, const char *, size_t, napi_value *);
napi_status napi_create_arraybuffer(napi_env, size_t, void **, napi_value *);
napi_status napi_get_undefined(napi_env, napi_value *);
napi_status napi_get_global(napi_env, napi_value *);
napi_status napi_get_named_property(napi_env, napi_value, const char *, napi_value *);
napi_status napi_set_named_property(napi_env, napi_value, const char *, napi_value);
napi_status napi_delete_property(napi_env, napi_value, napi_value, bool *);
napi_status napi_create_object(napi_env, napi_value *);
napi_status napi_create_function(napi_env, const char *, size_t, napi_callback, void *, napi_value *);
napi_status napi_call_function(napi_env, napi_value, napi_value, size_t, const napi_value *, napi_value *);
napi_status napi_throw_error(napi_env, const char *, const char *);
napi_status napi_create_error(napi_env, napi_value, napi_value, napi_value *);
napi_status napi_get_and_clear_last_exception(napi_env, napi_value *);
napi_status napi_is_exception_pending(napi_env, bool *);
napi_status napi_create_promise(napi_env, napi_deferred *, napi_value *);
napi_status napi_resolve_deferred(napi_env, napi_deferred, napi_value);
napi_status napi_reject_deferred(napi_env, napi_deferred, napi_value);
napi_status napi_create_reference(napi_env, napi_value, uint32_t, napi_ref *);
napi_status napi_get_reference_value(napi_env, napi_ref, napi_value *);
napi_status napi_create_async_work(napi_env, napi_value, napi_value, napi_async_execute_callback, napi_async_complete_callback, void *,
				   napi_async_work *);
napi_status napi_queue_async_work(napi_env, napi_async_work);
napi_status napi_delete_async_work(napi_env, napi_async_work);
napi_status napi_create_threadsafe_function(napi_env, napi_value, napi_value, napi_value, size_t, size_t, void *, napi_finalize, void *,
					    napi_threadsafe_function_call_js, napi_threadsafe_function *);
napi_status napi_call_threadsafe_function(napi_threadsafe_function, void *, napi_threadsafe_function_call_mode);
napi_status napi_unref_threadsafe_function(napi_env, napi_threadsafe_function);
napi_status napi_release_threadsafe_function(napi_threadsafe_function, napi_threadsafe_function_release_mode);
napi_status napi_create_array(napi_env, napi_value *);
napi_status napi_set_element(napi_env, napi_value, uint32_t, napi_value);
napi_status napi_get_node_version(napi_env, const napi_node_version **);

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
	struct kario_module *m;
} export_t;

struct kario_module {
	char spec[64];
	int loading;
	export_t *exports;
	size_t nexports;
	napi_ref handlers;
	napi_threadsafe_function tsfn;
	pthread_mutex_t mu;
	int queued;
};

typedef struct {
	kario_type type;
	double number;
	int truth;
	napi_value v; /* a plain call's */
	char *str;
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
	napi_env env; /* NULL on a worker */
	int argc;
	arg_t *args;
	int res;
	double number;
	char *str;
	size_t len;
	void *buf;
	size_t blen;
	char *err;
};

typedef struct {
	export_t *e;
	kario_call call;
	napi_deferred deferred;
	napi_async_work work;
} job_t;

typedef struct {
	char *event;
	char *json;
	size_t len;
} event_t;

static kario_api api;

/* ---- names ---- */

static int is_alpha(char c) { return c == '_' || ((c | 32) >= 'a' && (c | 32) <= 'z'); }

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

/* ---- JavaScript helpers ---- */

static napi_value global_fn(napi_env env, const char *object, const char *name, napi_value *recv)
{
	napi_value g, o, f;
	napi_get_global(env, &g);
	napi_get_named_property(env, g, object, &o);
	napi_get_named_property(env, o, name, &f);
	*recv = o;
	return f;
}

static char *stringify(napi_env env, napi_value v, size_t *len)
{
	napi_value recv, out;
	napi_value f = global_fn(env, "JSON", "stringify", &recv);
	if (napi_call_function(env, recv, f, 1, &v, &out) != napi_ok) {
		napi_value e;
		napi_get_and_clear_last_exception(env, &e);
		return NULL;
	}
	napi_valuetype t;
	napi_typeof(env, out, &t);
	if (t != napi_string)
		return NULL;
	size_t n;
	napi_get_value_string_utf8(env, out, NULL, 0, &n);
	char *s = malloc(n + 1);
	if (s)
		napi_get_value_string_utf8(env, out, s, n + 1, &n);
	if (len)
		*len = n;
	return s;
}

static napi_status parse(napi_env env, const char *json, size_t len, napi_value *out)
{
	napi_value recv, text;
	napi_value f = global_fn(env, "JSON", "parse", &recv);
	napi_create_string_utf8(env, json, len, &text);
	return napi_call_function(env, recv, f, 1, &text, out);
}

static napi_value undefined(napi_env env)
{
	napi_value u;
	napi_get_undefined(env, &u);
	return u;
}

/* ---- the table ---- */

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
	e.m = m;
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

static char *string_of(napi_env env, napi_value v, size_t *len)
{
	size_t n;
	if (napi_get_value_string_utf8(env, v, NULL, 0, &n) != napi_ok)
		return NULL;
	char *s = malloc(n + 1);
	if (s)
		napi_get_value_string_utf8(env, v, s, n + 1, &n);
	*len = n;
	return s;
}

static const char *api_string(kario_call *c, int i, size_t *len)
{
	arg_t *a = arg(c, i);
	if (!a || a->type != KARIO_STRING)
		return NULL;
	if (!a->str && c->env)
		a->str = string_of(c->env, a->v, &a->slen);
	if (a->str && len)
		*len = a->slen;
	return a->str;
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

static void json_quote(char **out, size_t *outlen, const char *s, size_t n)
{
	char *d = malloc(n * 6 + 3), *p = d;
	if (!d)
		return;
	*p++ = '"';
	for (size_t i = 0; i < n; i++) {
		unsigned char ch = (unsigned char)s[i];
		switch (ch) {
		case '"':
			p += sprintf(p, "\\\"");
			break;
		case '\\':
			p += sprintf(p, "\\\\");
			break;
		case '\n':
			p += sprintf(p, "\\n");
			break;
		case '\r':
			p += sprintf(p, "\\r");
			break;
		case '\t':
			p += sprintf(p, "\\t");
			break;
		case '\b':
			p += sprintf(p, "\\b");
			break;
		case '\f':
			p += sprintf(p, "\\f");
			break;
		default:
			if (ch < 0x20)
				p += sprintf(p, "\\u%04x", ch);
			else
				*p++ = (char)ch;
		}
	}
	*p++ = '"';
	*p = 0;
	*out = d;
	*outlen = (size_t)(p - d);
}

static const char *api_json(kario_call *c, int i, size_t *len)
{
	arg_t *a = arg(c, i);
	if (!a || a->type == KARIO_BYTES || a->type == KARIO_UNDEFINED)
		return NULL;
	if (!a->json_done) {
		a->json_done = 1;
		if (c->env)
			a->json = stringify(c->env, a->v, &a->jlen);
		else if (a->type == KARIO_STRING)
			json_quote(&a->json, &a->jlen, a->str, a->slen);
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

static int api_emit(kario_module *m, const char *event, const char *json, size_t len)
{
	if (!m || !m->tsfn || !valid_export(event))
		return -1;
	event_t *ev = calloc(1, sizeof *ev);
	if (!ev)
		return -1;
	ev->event = strdup(event);
	ev->json = json ? strndup(json, len) : NULL;
	ev->len = len;
	pthread_mutex_lock(&m->mu);
	int ok = ev->event && (!json || ev->json) && m->queued < MAX_QUEUED_EVENTS;
	if (ok)
		m->queued++;
	pthread_mutex_unlock(&m->mu);
	if (ok && napi_call_threadsafe_function(m->tsfn, ev, napi_tsfn_nonblocking) != napi_ok) {
		pthread_mutex_lock(&m->mu);
		m->queued--;
		pthread_mutex_unlock(&m->mu);
		ok = 0;
	}
	if (!ok) {
		free(ev->event);
		free(ev->json);
		free(ev);
		return -1;
	}
	return 0;
}

/* ---- calls ---- */

static size_t element_size(napi_typedarray_type t)
{
	switch (t) {
	case napi_int16_array:
	case napi_uint16_array:
	case napi_float16_array:
		return 2;
	case napi_int32_array:
	case napi_uint32_array:
	case napi_float32_array:
		return 4;
	case napi_float64_array:
	case napi_bigint64_array:
	case napi_biguint64_array:
		return 8;
	default:
		return 1;
	}
}

static int take_args(napi_env env, kario_call *c, size_t argc, napi_value *argv, int copy)
{
	c->argc = (int)argc;
	if (argc && !(c->args = calloc(argc, sizeof *c->args)))
		return -1;
	for (size_t i = 0; i < argc; i++) {
		arg_t *a = &c->args[i];
		napi_value v = argv[i], b;
		bool truth = false, is;
		napi_valuetype t;
		a->v = v;
		napi_coerce_to_bool(env, v, &b);
		napi_get_value_bool(env, b, &truth);
		a->truth = truth;
		napi_typeof(env, v, &t);
		switch (t) {
		case napi_undefined:
			a->type = KARIO_UNDEFINED;
			break;
		case napi_null:
			a->type = KARIO_NULL;
			break;
		case napi_boolean:
			a->type = KARIO_BOOL;
			break;
		case napi_number:
			a->type = KARIO_NUMBER;
			napi_get_value_double(env, v, &a->number);
			break;
		case napi_string:
			a->type = KARIO_STRING;
			if (copy && !(a->str = string_of(env, v, &a->slen)))
				return -1;
			break;
		default:
			a->type = KARIO_OBJECT;
			if (napi_is_arraybuffer(env, v, &is) == napi_ok && is) {
				a->type = KARIO_BYTES;
				napi_get_arraybuffer_info(env, v, &a->bytes, &a->blen);
			} else if (napi_is_typedarray(env, v, &is) == napi_ok && is) {
				napi_typedarray_type tt;
				size_t n, off;
				napi_value ab;
				a->type = KARIO_BYTES;
				napi_get_typedarray_info(env, v, &tt, &n, &a->bytes, &ab, &off);
				a->blen = n * element_size(tt);
			}
			if (a->type == KARIO_BYTES) {
				static char none[1];
				if (!a->bytes) {
					a->bytes = none;
					a->blen = 0;
				}
				if (copy) {
					void *p = malloc(a->blen ? a->blen : 1);
					if (!p)
						return -1;
					memcpy(p, a->bytes, a->blen);
					a->bytes = p;
					a->bytes_owned = 1;
				}
			}
		}
		if (copy && a->type != KARIO_STRING && a->type != KARIO_BYTES && a->type != KARIO_UNDEFINED) {
			a->json_done = 1;
			a->json = stringify(env, v, &a->jlen);
		}
	}
	if (copy)
		c->env = NULL;
	return 0;
}

static void call_free(kario_call *c)
{
	for (int i = 0; c->args && i < c->argc; i++) {
		free(c->args[i].str);
		free(c->args[i].json);
		if (c->args[i].bytes_owned)
			free(c->args[i].bytes);
	}
	free(c->args);
	free(c->str);
	free(c->buf);
	free(c->err);
}

/* result: the call's value, or NULL with an exception pending. */
static napi_value result(napi_env env, kario_call *c)
{
	napi_value v;
	if (c->err) {
		napi_throw_error(env, NULL, c->err);
		return NULL;
	}
	switch (c->res) {
	case RES_NUMBER:
		napi_create_double(env, c->number, &v);
		return v;
	case RES_BOOL:
		napi_get_boolean(env, c->number != 0, &v);
		return v;
	case RES_STRING:
		napi_create_string_utf8(env, c->str, c->len, &v);
		return v;
	case RES_BYTES: {
		void *p;
		if (napi_create_arraybuffer(env, c->blen, &p, &v) != napi_ok)
			return NULL;
		memcpy(p, c->buf, c->blen);
		return v;
	}
	case RES_JSON:
		return parse(env, c->str, c->len, &v) == napi_ok ? v : NULL;
	}
	return undefined(env);
}

static void execute(napi_env env, void *data)
{
	job_t *j = data;
	j->e->fn(&api, &j->call, j->e->data);
}

static void complete(napi_env env, napi_status status, void *data)
{
	job_t *j = data;
	napi_value v = result(env, &j->call);
	if (v) {
		napi_resolve_deferred(env, j->deferred, v);
	} else {
		napi_value e;
		napi_get_and_clear_last_exception(env, &e);
		napi_reject_deferred(env, j->deferred, e);
	}
	napi_delete_async_work(env, j->work);
	call_free(&j->call);
	free(j);
}

static napi_value call_fn(napi_env env, napi_callback_info info)
{
	size_t argc = 0;
	void *data;
	napi_get_cb_info(env, info, &argc, NULL, NULL, &data);
	napi_value *argv = argc ? calloc(argc, sizeof *argv) : NULL;
	if (argc && !argv) {
		napi_throw_error(env, NULL, "out of memory");
		return NULL;
	}
	napi_get_cb_info(env, info, &argc, argv, NULL, NULL);
	export_t *e = data;
	napi_value ret = NULL;
	if (e->flags & KARIO_ASYNC) {
		job_t *j = calloc(1, sizeof *j);
		napi_value name;
		if (!j || take_args(env, &j->call, argc, argv, 1) < 0) {
			if (j)
				call_free(&j->call);
			free(j);
			napi_throw_error(env, NULL, "out of memory");
		} else {
			j->e = e;
			napi_create_promise(env, &j->deferred, &ret);
			napi_create_string_utf8(env, e->name, strlen(e->name), &name);
			napi_create_async_work(env, NULL, name, execute, complete, j, &j->work);
			napi_queue_async_work(env, j->work);
		}
	} else {
		kario_call c = {.env = env};
		if (take_args(env, &c, argc, argv, 0) < 0) {
			napi_throw_error(env, NULL, "out of memory");
		} else {
			e->fn(&api, &c, e->data);
			ret = result(env, &c);
		}
		call_free(&c);
	}
	free(argv);
	return ret;
}

static napi_value on_fn(napi_env env, napi_callback_info info)
{
	size_t argc = 2;
	napi_value argv[2];
	void *data;
	napi_get_cb_info(env, info, &argc, argv, NULL, &data);
	kario_module *m = data;
	napi_valuetype t0 = napi_undefined, t1 = napi_undefined;
	if (argc > 0)
		napi_typeof(env, argv[0], &t0);
	if (argc > 1)
		napi_typeof(env, argv[1], &t1);
	if (t0 != napi_string) {
		napi_throw_error(env, NULL, "on: the event's name is a string");
		return NULL;
	}
	napi_value handlers;
	napi_get_reference_value(env, m->handlers, &handlers);
	size_t n;
	char *name = string_of(env, argv[0], &n);
	if (t1 == napi_function) {
		napi_set_named_property(env, handlers, name, argv[1]);
	} else if (t1 == napi_undefined || t1 == napi_null) {
		bool done;
		napi_delete_property(env, handlers, argv[0], &done);
	} else {
		napi_throw_error(env, NULL, "on: the handler is a function");
	}
	free(name);
	return NULL;
}

static void deliver(napi_env env, napi_value cb, void *context, void *data)
{
	kario_module *m = context;
	event_t *ev = data;
	pthread_mutex_lock(&m->mu);
	m->queued--;
	pthread_mutex_unlock(&m->mu);
	if (env) {
		napi_value handlers, fn, v = NULL;
		napi_valuetype t;
		napi_get_reference_value(env, m->handlers, &handlers);
		napi_get_named_property(env, handlers, ev->event, &fn);
		napi_typeof(env, fn, &t);
		if (t == napi_function) {
			if (!ev->json)
				v = undefined(env);
			else if (parse(env, ev->json, ev->len, &v) != napi_ok)
				v = NULL;
			napi_value ret;
			if (v)
				napi_call_function(env, undefined(env), fn, 1, &v, &ret);
			bool pending = false;
			napi_is_exception_pending(env, &pending);
			if (pending) {
				napi_value e;
				napi_get_and_clear_last_exception(env, &e);
				char *s = stringify(env, e, NULL);
				fprintf(stderr, "kario-node: %s event \"%s\": %s\n", m->spec, ev->event, s ? s : "an exception");
				free(s);
			}
		}
	}
	free(ev->event);
	free(ev->json);
	free(ev);
}

/* ---- loading ---- */

static void setup(napi_env env)
{
	static char engine[48];
	if (api.size)
		return;
	const napi_node_version *v = NULL;
	napi_get_node_version(env, &v);
	snprintf(engine, sizeof engine, "Node.js %u.%u.%u", v ? v->major : 0, v ? v->minor : 0, v ? v->patch : 0);
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
}

static napi_value refuse(napi_env env, const char *fmt, ...) __attribute__((format(printf, 2, 3)));

static napi_value refuse(napi_env env, const char *fmt, ...)
{
	char msg[512];
	va_list ap;
	va_start(ap, fmt);
	vsnprintf(msg, sizeof msg, fmt, ap);
	va_end(ap);
	napi_throw_error(env, NULL, msg);
	return NULL;
}

/* load(path, spec, rel): the module's exports, its on() among them; rel is
 * the library's path in the package, for the messages. */
static napi_value load(napi_env env, napi_callback_info info)
{
	size_t argc = 3, n;
	napi_value argv[3];
	napi_get_cb_info(env, info, &argc, argv, NULL, NULL);
	if (argc < 3)
		return refuse(env, "load(path, spec, rel)");
	setup(env);
	char *path = string_of(env, argv[0], &n), *spec = string_of(env, argv[1], &n), *rel = string_of(env, argv[2], &n);
	napi_value out = NULL;
	void *h = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	const uint32_t *abi = h ? dlsym(h, "kario_native_abi") : NULL;
	int (*init)(const kario_api *, kario_module *) = h ? (int (*)(const kario_api *, kario_module *))dlsym(h, "kario_native_init") : NULL;
	kario_module *m = NULL;
	if (!h) {
		refuse(env, "%s: %s", spec, dlerror());
	} else if (!abi || !init) {
		refuse(env, "%s: %s is not a native module (no kario_native_abi or kario_native_init: KARIO_NATIVE_MODULE in kario_native.h)", spec, rel);
	} else if (*abi >> 16 != KARIO_NATIVE_ABI_MAJOR || (*abi & 0xffff) > KARIO_NATIVE_ABI_MINOR) {
		refuse(env, "%s: built for the native interface %u.%u, kario-native.node has %d.%d (rebuild it with this SDK's kario_native.h)", spec,
		       *abi >> 16, *abi & 0xffff, KARIO_NATIVE_ABI_MAJOR, KARIO_NATIVE_ABI_MINOR);
	} else if (!(m = calloc(1, sizeof *m))) {
		refuse(env, "%s: out of memory", spec);
	} else {
		napi_value handlers, name;
		snprintf(m->spec, sizeof m->spec, "%s", spec);
		pthread_mutex_init(&m->mu, NULL);
		napi_create_object(env, &handlers);
		napi_create_reference(env, handlers, 1, &m->handlers);
		napi_create_string_utf8(env, spec, strlen(spec), &name);
		if (napi_create_threadsafe_function(env, NULL, NULL, name, 0, 1, NULL, NULL, m, deliver, &m->tsfn) == napi_ok)
			napi_unref_threadsafe_function(env, m->tsfn);
		m->loading = 1;
		int rc = init(&api, m);
		m->loading = 0;
		if (rc != 0) {
			refuse(env, "%s: kario_native_init returned %d", spec, rc);
		} else {
			napi_create_object(env, &out);
			for (size_t i = 0; i < m->nexports; i++) {
				export_t *e = &m->exports[i];
				napi_value v;
				if (e->kind == EXP_NUMBER)
					napi_create_double(env, e->number, &v);
				else if (e->kind == EXP_STRING)
					napi_create_string_utf8(env, e->string, strlen(e->string), &v);
				else
					napi_create_function(env, e->name, strlen(e->name), call_fn, e, &v);
				napi_set_named_property(env, out, e->name, v);
			}
			napi_value on;
			napi_create_function(env, "on", 2, on_fn, m, &on);
			napi_set_named_property(env, out, "on", on);
		}
	}
	free(path);
	free(spec);
	free(rel);
	return out;
}

/* ---- the Shell's socket with the files its messages carry ---- */

/* As libphoneui: at most this many files with one read (SCM_RIGHTS). */
#define UI_MAX_FDS 8

typedef struct {
	char *data;
	size_t len; /* 0: the connection is gone */
	int nfds;
	int fds[UI_MAX_FDS];
} ui_chunk;

typedef struct {
	int fd;
	napi_threadsafe_function tsfn;
} ui_reader;

static void ui_deliver(napi_env env, napi_value fn, void *context, void *data)
{
	ui_chunk *c = data;
	if (env && fn) {
		napi_value argv[2], ret;
		if (c->len) {
			void *p;
			napi_create_arraybuffer(env, c->len, &p, &argv[0]);
			memcpy(p, c->data, c->len);
		} else {
			napi_get_undefined(env, &argv[0]);
		}
		napi_create_array(env, &argv[1]);
		for (int i = 0; i < c->nfds; i++) {
			napi_value v;
			napi_create_double(env, c->fds[i], &v);
			napi_set_element(env, argv[1], (uint32_t)i, v);
		}
		napi_call_function(env, fn, fn, 2, argv, &ret);
	} else {
		for (int i = 0; i < c->nfds; i++)
			close(c->fds[i]);
	}
	free(c->data);
	free(c);
}

static void *ui_read_loop(void *arg)
{
	ui_reader *r = arg;
	for (;;) {
		ui_chunk *c = calloc(1, sizeof *c);
		char *buf = c ? malloc(65536) : NULL;
		if (!buf) {
			free(c);
			break;
		}
		union {
			struct cmsghdr h;
			char space[CMSG_SPACE(UI_MAX_FDS * sizeof(int))];
		} ctl;
		struct iovec iov = {.iov_base = buf, .iov_len = 65536};
		struct msghdr mh = {.msg_iov = &iov, .msg_iovlen = 1, .msg_control = ctl.space, .msg_controllen = sizeof ctl.space};
		struct pollfd p = {.fd = r->fd, .events = POLLIN};
		ssize_t n = poll(&p, 1, -1);
		if (n > 0)
			n = recvmsg(r->fd, &mh, MSG_CMSG_CLOEXEC | MSG_DONTWAIT);
		if (n < 0 && (errno == EINTR || errno == EAGAIN)) {
			free(buf);
			free(c);
			continue;
		}
		for (struct cmsghdr *h = n >= 0 ? CMSG_FIRSTHDR(&mh) : NULL; h; h = CMSG_NXTHDR(&mh, h)) {
			if (h->cmsg_level != SOL_SOCKET || h->cmsg_type != SCM_RIGHTS)
				continue;
			int nf = (int)((h->cmsg_len - CMSG_LEN(0)) / sizeof(int));
			for (int i = 0; i < nf; i++) {
				int fd;
				memcpy(&fd, CMSG_DATA(h) + i * sizeof(int), sizeof fd);
				if (c->nfds < UI_MAX_FDS)
					c->fds[c->nfds++] = fd;
				else
					close(fd);
			}
		}
		c->data = buf;
		c->len = n > 0 ? (size_t)n : 0;
		napi_call_threadsafe_function(r->tsfn, c, napi_tsfn_blocking);
		if (n <= 0)
			break;
	}
	napi_release_threadsafe_function(r->tsfn, napi_tsfn_release);
	free(r);
	return NULL;
}

/* uiRead(fd, onData): reads the Shell's socket on a thread of its own;
 * onData(bytes, fds) gets what each read brought (an ArrayBuffer and the
 * files that came with it), then (undefined, []) when the connection is
 * gone. Node's own sockets drop the files (net.Socket reads with read()). */
static napi_value ui_read(napi_env env, napi_callback_info info)
{
	size_t argc = 2;
	napi_value argv[2], name;
	napi_get_cb_info(env, info, &argc, argv, NULL, NULL);
	double fd;
	if (argc < 2 || napi_get_value_double(env, argv[0], &fd) != napi_ok)
		return refuse(env, "uiRead(fd, onData)");
	ui_reader *r = calloc(1, sizeof *r);
	if (!r)
		return refuse(env, "uiRead: out of memory");
	r->fd = (int)fd;
	napi_create_string_utf8(env, "kario-ui", 8, &name);
	if (napi_create_threadsafe_function(env, argv[1], NULL, name, 64, 1, NULL, NULL, NULL, ui_deliver, &r->tsfn) != napi_ok) {
		free(r);
		return refuse(env, "uiRead: no thread-safe function");
	}
	pthread_t t;
	pthread_attr_t attr;
	pthread_attr_init(&attr);
	pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
	int err = pthread_create(&t, &attr, ui_read_loop, r);
	pthread_attr_destroy(&attr);
	if (err) {
		napi_release_threadsafe_function(r->tsfn, napi_tsfn_abort);
		free(r);
		return refuse(env, "uiRead: %s", strerror(err));
	}
	napi_value undef;
	napi_get_undefined(env, &undef);
	return undef;
}

KARIO_NATIVE_EXTERN KARIO_NATIVE_EXPORT napi_value napi_register_module_v1(napi_env env, napi_value exports)
{
	napi_value f;
	napi_create_function(env, "load", 4, load, NULL, &f);
	napi_set_named_property(env, exports, "load", f);
	napi_create_function(env, "uiRead", 6, ui_read, NULL, &f);
	napi_set_named_property(env, exports, "uiRead", f);
	return exports;
}
