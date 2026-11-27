/* The few web interfaces the network needs, as globals the way browsers and
 * Node.js have them, so the same code runs on both engines: AbortController
 * and AbortSignal (cancel a fetch, a WebSocket, anything that takes a
 * signal), TextEncoder and TextDecoder (UTF-8 only: a streamed body's
 * chunks to text); ReadableStream is web.js's (net.c). Plus the listeners
 * of the objects that fire events (AbortSignal here, WebSocket in ws.c). */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

#include "jsrt.h"
#include "utf8dec.h"

/* ---- UTF-8 ---- */

char *jsrt_utf8(JSContext *ctx, JSValueConst v, size_t *len)
{
	size_t n;
	const char *s = JS_ToCStringLen(ctx, &n, v);
	if (!s)
		return NULL;
	char *out = malloc(n + 1);
	if (!out) {
		JS_FreeCString(ctx, s);
		JS_ThrowOutOfMemory(ctx);
		return NULL;
	}
	memcpy(out, s, n + 1);
	JS_FreeCString(ctx, s);
	/* QuickJS writes a lone surrogate as its three bytes (ED A0-BF xx);
	 * UTF-8 has U+FFFD there, three bytes as well. */
	for (size_t i = 0; i + 2 < n; i++) {
		if ((unsigned char)out[i] == 0xed && (unsigned char)out[i + 1] >= 0xa0) {
			memcpy(out + i, "\xef\xbf\xbd", 3);
			i += 2;
		}
	}
	*len = n;
	return out;
}

JSValue jsrt_decode_utf8(JSContext *ctx, const uint8_t *p, size_t n)
{
	utf8_decoder d = {0};
	char *s;
	size_t len;
	if (utf8_decode(&d, p, n, 0, &s, &len) < 0)
		return JS_ThrowOutOfMemory(ctx);
	JSValue v = JS_NewStringLen(ctx, s, len);
	free(s);
	return v;
}

/* ---- listeners ---- */

int jsrt_listen(JSContext *ctx, JSValue *list, int argc, JSValueConst *argv, int add)
{
	if (argc < 2)
		return 0;
	JSValueConst fn = argv[1];
	if (!JS_IsFunction(ctx, fn))
		return 0;
	const char *type = JS_ToCString(ctx, argv[0]);
	if (!type)
		return -1;
	int once = 0;
	if (add && argc > 2 && JS_IsObject(argv[2])) {
		JSValue o = JS_GetPropertyStr(ctx, argv[2], "once");
		once = JS_ToBool(ctx, o) > 0;
		JS_FreeValue(ctx, o);
	}
	if (JS_IsUndefined(*list))
		*list = JS_NewArray(ctx);
	int64_t n = 0;
	JS_GetLength(ctx, *list, &n);
	JSValue keep = JS_NewArray(ctx);
	uint32_t k = 0;
	int found = 0;
	for (int64_t i = 0; i < n; i++) {
		JSValue e = JS_GetPropertyInt64(ctx, *list, i);
		JSValue t = JS_GetPropertyUint32(ctx, e, 0), f = JS_GetPropertyUint32(ctx, e, 1);
		const char *ts = JS_ToCString(ctx, t);
		int same = ts && !strcmp(ts, type) && JS_IsStrictEqual(ctx, f, fn);
		JS_FreeCString(ctx, ts);
		JS_FreeValue(ctx, t);
		JS_FreeValue(ctx, f);
		found |= same;
		if (add || !same)
			JS_SetPropertyUint32(ctx, keep, k++, e);
		else
			JS_FreeValue(ctx, e);
	}
	if (add && !found) {
		JSValue e = JS_NewArray(ctx);
		JS_SetPropertyUint32(ctx, e, 0, JS_NewString(ctx, type));
		JS_SetPropertyUint32(ctx, e, 1, JS_DupValue(ctx, fn));
		JS_SetPropertyUint32(ctx, e, 2, JS_NewBool(ctx, once));
		JS_SetPropertyUint32(ctx, keep, k++, e);
	}
	JS_FreeCString(ctx, type);
	JS_FreeValue(ctx, *list);
	*list = keep;
	return 0;
}

void jsrt_fire(JSContext *ctx, JSValueConst target, JSValue *list, const char *type, JSValueConst event)
{
	char on[32];
	snprintf(on, sizeof on, "on%s", type);
	JSValue h = JS_GetPropertyStr(ctx, target, on);
	if (JS_IsFunction(ctx, h)) {
		JSValue ret = JS_Call(ctx, h, target, 1, &event);
		if (JS_IsException(ret))
			jsrt_report(ctx, "uncaught exception in an event handler");
		JS_FreeValue(ctx, ret);
	}
	JS_FreeValue(ctx, h);
	if (JS_IsUndefined(*list))
		return;
	/* A listener may add or remove listeners: those of the moment fire. */
	JSValue now = JS_DupValue(ctx, *list);
	int64_t n = 0;
	JS_GetLength(ctx, now, &n);
	for (int64_t i = 0; i < n; i++) {
		JSValue e = JS_GetPropertyInt64(ctx, now, i);
		JSValue t = JS_GetPropertyUint32(ctx, e, 0), f = JS_GetPropertyUint32(ctx, e, 1), o = JS_GetPropertyUint32(ctx, e, 2);
		const char *ts = JS_ToCString(ctx, t);
		if (ts && !strcmp(ts, type)) {
			if (JS_ToBool(ctx, o) > 0) {
				JSValue args[2] = {t, f};
				jsrt_listen(ctx, list, 2, args, 0);
			}
			JSValue ret = JS_Call(ctx, f, target, 1, &event);
			if (JS_IsException(ret))
				jsrt_report(ctx, "uncaught exception in an event listener");
			JS_FreeValue(ctx, ret);
		}
		JS_FreeCString(ctx, ts);
		JS_FreeValue(ctx, t);
		JS_FreeValue(ctx, f);
		JS_FreeValue(ctx, o);
		JS_FreeValue(ctx, e);
	}
	JS_FreeValue(ctx, now);
}

/* ---- AbortSignal, AbortController ---- */

static JSClassID signal_class, controller_class;

typedef struct {
	int aborted;
	JSValue reason;
	JSValue listeners;
} ksignal;

static void signal_finalizer(JSRuntime *rt, JSValueConst val)
{
	ksignal *s = JS_GetOpaque(val, signal_class);
	if (s) {
		JS_FreeValueRT(rt, s->reason);
		JS_FreeValueRT(rt, s->listeners);
		free(s);
	}
}

static void signal_mark(JSRuntime *rt, JSValueConst val, JS_MarkFunc *mark)
{
	ksignal *s = JS_GetOpaque(val, signal_class);
	if (s) {
		JS_MarkValue(rt, s->reason, mark);
		JS_MarkValue(rt, s->listeners, mark);
	}
}

static void controller_finalizer(JSRuntime *rt, JSValueConst val)
{
	JSValue *sig = JS_GetOpaque(val, controller_class);
	if (sig) {
		JS_FreeValueRT(rt, *sig);
		free(sig);
	}
}

static void controller_mark(JSRuntime *rt, JSValueConst val, JS_MarkFunc *mark)
{
	JSValue *sig = JS_GetOpaque(val, controller_class);
	if (sig)
		JS_MarkValue(rt, *sig, mark);
}

static JSValue named_error(JSContext *ctx, const char *name, const char *message)
{
	JSValue e = JS_NewError(ctx);
	JS_DefinePropertyValueStr(ctx, e, "message", JS_NewString(ctx, message), JS_PROP_WRITABLE | JS_PROP_CONFIGURABLE);
	JS_DefinePropertyValueStr(ctx, e, "name", JS_NewString(ctx, name), JS_PROP_WRITABLE | JS_PROP_CONFIGURABLE);
	return e;
}

JSValue jsrt_abort_error(JSContext *ctx) { return named_error(ctx, "AbortError", "This operation was aborted"); }

static JSValue new_signal(JSContext *ctx)
{
	ksignal *s = calloc(1, sizeof *s);
	if (!s)
		return JS_ThrowOutOfMemory(ctx);
	s->reason = JS_UNDEFINED;
	s->listeners = JS_UNDEFINED;
	JSValue v = JS_NewObjectClass(ctx, signal_class);
	if (JS_IsException(v)) {
		free(s);
		return v;
	}
	JS_SetOpaque(v, s);
	return v;
}

/* signal_abort aborts sig with reason (an AbortError when undefined). */
static void signal_abort(JSContext *ctx, JSValueConst sig, JSValueConst reason)
{
	ksignal *s = JS_GetOpaque(sig, signal_class);
	if (!s || s->aborted)
		return;
	s->aborted = 1;
	s->reason = JS_IsUndefined(reason) ? jsrt_abort_error(ctx) : JS_DupValue(ctx, reason);
	JSValue ev = JS_NewObject(ctx);
	JS_SetPropertyStr(ctx, ev, "type", JS_NewString(ctx, "abort"));
	JS_SetPropertyStr(ctx, ev, "target", JS_DupValue(ctx, sig));
	JSValue keep = JS_DupValue(ctx, sig);
	jsrt_fire(ctx, sig, &s->listeners, "abort", ev);
	JS_FreeValue(ctx, keep);
	JS_FreeValue(ctx, ev);
}

int jsrt_signal_state(JSContext *ctx, JSValueConst v, JSValue *reason)
{
	ksignal *s = JS_GetOpaque(v, signal_class);
	if (!s)
		return -1;
	if (s->aborted && reason)
		*reason = JS_DupValue(ctx, s->reason);
	return s->aborted;
}

int jsrt_signal_listen(JSContext *ctx, JSValueConst sig, JSValueConst fn, int add)
{
	ksignal *s = JS_GetOpaque(sig, signal_class);
	if (!s)
		return -1;
	JSValue args[2] = {JS_NewString(ctx, "abort"), JS_DupValue(ctx, fn)};
	int r = jsrt_listen(ctx, &s->listeners, 2, args, add);
	JS_FreeValue(ctx, args[0]);
	JS_FreeValue(ctx, args[1]);
	return r;
}

static JSValue signal_ctor(JSContext *ctx, JSValueConst new_target, int argc, JSValueConst *argv)
{
	return JS_ThrowTypeError(ctx, "AbortSignal: made by an AbortController, AbortSignal.abort() or AbortSignal.timeout()");
}

static JSValue signal_get(JSContext *ctx, JSValueConst this_val, int magic)
{
	ksignal *s = JS_GetOpaque2(ctx, this_val, signal_class);
	if (!s)
		return JS_EXCEPTION;
	return magic ? JS_DupValue(ctx, s->reason) : JS_NewBool(ctx, s->aborted);
}

static JSValue signal_throw_if(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	ksignal *s = JS_GetOpaque2(ctx, this_val, signal_class);
	if (!s)
		return JS_EXCEPTION;
	if (s->aborted)
		return JS_Throw(ctx, JS_DupValue(ctx, s->reason));
	return JS_UNDEFINED;
}

static JSValue signal_listen(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int add)
{
	ksignal *s = JS_GetOpaque2(ctx, this_val, signal_class);
	if (!s || jsrt_listen(ctx, &s->listeners, argc, argv, add) < 0)
		return JS_EXCEPTION;
	return JS_UNDEFINED;
}

static JSValue signal_abort_static(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	JSValue v = new_signal(ctx);
	if (!JS_IsException(v))
		signal_abort(ctx, v, argc ? argv[0] : JS_UNDEFINED);
	return v;
}

static JSValue timeout_fire(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic, JSValueConst *data)
{
	JSValue e = named_error(ctx, "TimeoutError", "The operation was aborted due to timeout");
	signal_abort(ctx, data[0], e);
	JS_FreeValue(ctx, e);
	return JS_UNDEFINED;
}

static JSValue signal_timeout(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	double ms;
	if (argc < 1 || JS_ToFloat64(ctx, &ms, argv[0]))
		return JS_EXCEPTION;
	if (!(ms >= 0))
		return JS_ThrowTypeError(ctx, "AbortSignal.timeout: the time is a number of milliseconds");
	JSValue v = new_signal(ctx);
	if (JS_IsException(v))
		return v;
	JSValue fire = JS_NewCFunctionData(ctx, timeout_fire, 0, 0, 1, (JSValueConst *)&v);
	JSValue g = JS_GetGlobalObject(ctx);
	JSValue set = JS_GetPropertyStr(ctx, g, "setTimeout");
	JSValue args[2] = {fire, JS_NewFloat64(ctx, ms)};
	JS_FreeValue(ctx, JS_Call(ctx, set, g, 2, args));
	JS_FreeValue(ctx, set);
	JS_FreeValue(ctx, g);
	JS_FreeValue(ctx, fire);
	return v;
}

/* any_fire aborts data[0] (AbortSignal.any's) with data[1]'s reason. */
static JSValue any_fire(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic, JSValueConst *data)
{
	ksignal *from = JS_GetOpaque(data[1], signal_class);
	if (from && from->aborted)
		signal_abort(ctx, data[0], from->reason);
	return JS_UNDEFINED;
}

static JSValue signal_any(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	JSValue g = JS_GetGlobalObject(ctx);
	JSValue array = JS_GetPropertyStr(ctx, g, "Array");
	JSValue from = JS_GetPropertyStr(ctx, array, "from");
	JSValue list = argc ? JS_Call(ctx, from, array, 1, argv) : JS_ThrowTypeError(ctx, "AbortSignal.any: the signals are iterable");
	JS_FreeValue(ctx, from);
	JS_FreeValue(ctx, array);
	JS_FreeValue(ctx, g);
	if (JS_IsException(list))
		return list;
	int64_t n = 0;
	JS_GetLength(ctx, list, &n);
	JSValue v = JS_UNDEFINED;
	for (int64_t i = 0; i < n; i++) {
		JSValue s = JS_GetPropertyInt64(ctx, list, i);
		ksignal *k = JS_GetOpaque(s, signal_class);
		JS_FreeValue(ctx, s);
		if (!k) {
			JS_FreeValue(ctx, list);
			return JS_ThrowTypeError(ctx, "AbortSignal.any: each signal is an AbortSignal");
		}
		if (k->aborted && JS_IsUndefined(v)) {
			v = new_signal(ctx);
			if (!JS_IsException(v))
				signal_abort(ctx, v, k->reason);
		}
	}
	if (!JS_IsUndefined(v)) {
		JS_FreeValue(ctx, list);
		return v;
	}
	v = new_signal(ctx);
	for (int64_t i = 0; i < n && !JS_IsException(v); i++) {
		JSValue data[2] = {v, JS_GetPropertyInt64(ctx, list, i)};
		JSValue fire = JS_NewCFunctionData(ctx, any_fire, 0, 0, 2, (JSValueConst *)data);
		jsrt_signal_listen(ctx, data[1], fire, 1);
		JS_FreeValue(ctx, fire);
		JS_FreeValue(ctx, data[1]);
	}
	JS_FreeValue(ctx, list);
	return v;
}

static JSValue controller_ctor(JSContext *ctx, JSValueConst new_target, int argc, JSValueConst *argv)
{
	JSValue proto = JS_GetPropertyStr(ctx, new_target, "prototype");
	JSValue obj = JS_NewObjectProtoClass(ctx, proto, controller_class);
	JS_FreeValue(ctx, proto);
	if (JS_IsException(obj))
		return obj;
	JSValue *sig = malloc(sizeof *sig);
	if (!sig) {
		JS_FreeValue(ctx, obj);
		return JS_ThrowOutOfMemory(ctx);
	}
	*sig = new_signal(ctx);
	if (JS_IsException(*sig)) {
		free(sig);
		JS_FreeValue(ctx, obj);
		return JS_EXCEPTION;
	}
	JS_SetOpaque(obj, sig);
	return obj;
}

static JSValue controller_signal(JSContext *ctx, JSValueConst this_val)
{
	JSValue *sig = JS_GetOpaque2(ctx, this_val, controller_class);
	return sig ? JS_DupValue(ctx, *sig) : JS_EXCEPTION;
}

static JSValue controller_abort(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	JSValue *sig = JS_GetOpaque2(ctx, this_val, controller_class);
	if (!sig)
		return JS_EXCEPTION;
	signal_abort(ctx, *sig, argc ? argv[0] : JS_UNDEFINED);
	return JS_UNDEFINED;
}

static const JSCFunctionListEntry signal_proto[] = {
	JS_CGETSET_MAGIC_DEF("aborted", signal_get, NULL, 0),
	JS_CGETSET_MAGIC_DEF("reason", signal_get, NULL, 1),
	JS_CFUNC_DEF("throwIfAborted", 0, signal_throw_if),
	JS_CFUNC_MAGIC_DEF("addEventListener", 2, signal_listen, 1),
	JS_CFUNC_MAGIC_DEF("removeEventListener", 2, signal_listen, 0),
	JS_PROP_STRING_DEF("[Symbol.toStringTag]", "AbortSignal", JS_PROP_CONFIGURABLE),
};

static const JSCFunctionListEntry signal_static[] = {
	JS_CFUNC_DEF("abort", 0, signal_abort_static),
	JS_CFUNC_DEF("timeout", 1, signal_timeout),
	JS_CFUNC_DEF("any", 1, signal_any),
};

static const JSCFunctionListEntry controller_proto[] = {
	JS_CGETSET_DEF("signal", controller_signal, NULL),
	JS_CFUNC_DEF("abort", 0, controller_abort),
	JS_PROP_STRING_DEF("[Symbol.toStringTag]", "AbortController", JS_PROP_CONFIGURABLE),
};

/* ---- TextEncoder, TextDecoder ---- */

static JSClassID decoder_class;

static void decoder_finalizer(JSRuntime *rt, JSValueConst val) { free(JS_GetOpaque(val, decoder_class)); }

static JSValue encoder_ctor(JSContext *ctx, JSValueConst new_target, int argc, JSValueConst *argv)
{
	JSValue proto = JS_GetPropertyStr(ctx, new_target, "prototype");
	JSValue obj = JS_NewObjectProto(ctx, proto);
	JS_FreeValue(ctx, proto);
	return obj;
}

static JSValue encoder_encode(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	if (argc < 1 || JS_IsUndefined(argv[0]))
		return JS_NewUint8ArrayCopy(ctx, (const uint8_t *)"", 0);
	size_t n;
	char *s = jsrt_utf8(ctx, argv[0], &n);
	if (!s)
		return JS_EXCEPTION;
	JSValue v = JS_NewUint8ArrayCopy(ctx, (const uint8_t *)s, n);
	free(s);
	return v;
}

static JSValue decoder_ctor(JSContext *ctx, JSValueConst new_target, int argc, JSValueConst *argv)
{
	if (argc > 0 && !JS_IsUndefined(argv[0])) {
		const char *label = JS_ToCString(ctx, argv[0]);
		if (!label)
			return JS_EXCEPTION;
		const char *l = label;
		while (*l == ' ' || *l == '\t' || *l == '\n')
			l++;
		size_t n = strlen(l);
		while (n && (l[n - 1] == ' ' || l[n - 1] == '\t' || l[n - 1] == '\n'))
			n--;
		int ok = (n == 5 && !strncasecmp(l, "utf-8", 5)) || (n == 4 && !strncasecmp(l, "utf8", 4)) ||
			 (n == 18 && !strncasecmp(l, "unicode-1-1-utf-8", 18));
		JS_FreeCString(ctx, label);
		if (!ok)
			return JS_ThrowRangeError(ctx, "TextDecoder: only UTF-8");
	}
	utf8_decoder *d = calloc(1, sizeof *d);
	if (!d)
		return JS_ThrowOutOfMemory(ctx);
	if (argc > 1 && JS_IsObject(argv[1])) {
		JSValue f = JS_GetPropertyStr(ctx, argv[1], "fatal"), b = JS_GetPropertyStr(ctx, argv[1], "ignoreBOM");
		d->fatal = JS_ToBool(ctx, f) > 0;
		d->ignore_bom = JS_ToBool(ctx, b) > 0;
		JS_FreeValue(ctx, f);
		JS_FreeValue(ctx, b);
	}
	JSValue proto = JS_GetPropertyStr(ctx, new_target, "prototype");
	JSValue obj = JS_NewObjectProtoClass(ctx, proto, decoder_class);
	JS_FreeValue(ctx, proto);
	if (JS_IsException(obj)) {
		free(d);
		return obj;
	}
	JS_SetOpaque(obj, d);
	return obj;
}

/* bytes_of is the memory of an ArrayBuffer or a view on one. */
const uint8_t *jsrt_bytes_of(JSContext *ctx, JSValueConst v, size_t *len)
{
	size_t n;
	uint8_t *p = JS_GetArrayBuffer(ctx, &n, v);
	if (p) {
		*len = n;
		return p;
	}
	JS_FreeValue(ctx, JS_GetException(ctx));
	size_t off, bytes, per;
	JSValue ab = JS_GetTypedArrayBuffer(ctx, v, &off, &bytes, &per);
	if (JS_IsException(ab)) {
		JS_FreeValue(ctx, JS_GetException(ctx));
		return NULL;
	}
	size_t all;
	p = JS_GetArrayBuffer(ctx, &all, ab);
	JS_FreeValue(ctx, ab);
	if (!p || off + bytes > all)
		return NULL;
	*len = bytes;
	return p + off;
}

static JSValue decoder_decode(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	utf8_decoder *d = JS_GetOpaque2(ctx, this_val, decoder_class);
	if (!d)
		return JS_EXCEPTION;
	size_t n = 0;
	const uint8_t *in = (const uint8_t *)"";
	if (argc > 0 && !JS_IsUndefined(argv[0]) && !(in = jsrt_bytes_of(ctx, argv[0], &n)))
		return JS_ThrowTypeError(ctx, "TextDecoder.decode: the input is an ArrayBuffer or a view on one");
	int stream = 0;
	if (argc > 1 && JS_IsObject(argv[1])) {
		JSValue s = JS_GetPropertyStr(ctx, argv[1], "stream");
		stream = JS_ToBool(ctx, s) > 0;
		JS_FreeValue(ctx, s);
	}
	char *s;
	size_t len;
	int bad = utf8_decode(d, in, n, stream, &s, &len);
	if (bad < 0)
		return JS_ThrowOutOfMemory(ctx);
	if (bad && d->fatal)
		return JS_ThrowTypeError(ctx, "TextDecoder.decode: the data is not valid UTF-8");
	JSValue v = JS_NewStringLen(ctx, s, len);
	free(s);
	return v;
}

static JSValue decoder_get(JSContext *ctx, JSValueConst this_val, int magic)
{
	utf8_decoder *d = JS_GetOpaque2(ctx, this_val, decoder_class);
	if (!d)
		return JS_EXCEPTION;
	return magic ? JS_NewBool(ctx, magic == 1 ? d->fatal : d->ignore_bom) : JS_NewString(ctx, "utf-8");
}

static const JSCFunctionListEntry encoder_proto[] = {
	JS_CFUNC_DEF("encode", 0, encoder_encode),
	JS_PROP_STRING_DEF("encoding", "utf-8", JS_PROP_CONFIGURABLE),
	JS_PROP_STRING_DEF("[Symbol.toStringTag]", "TextEncoder", JS_PROP_CONFIGURABLE),
};

static const JSCFunctionListEntry decoder_proto[] = {
	JS_CFUNC_DEF("decode", 0, decoder_decode),
	JS_CGETSET_MAGIC_DEF("encoding", decoder_get, NULL, 0),
	JS_CGETSET_MAGIC_DEF("fatal", decoder_get, NULL, 1),
	JS_CGETSET_MAGIC_DEF("ignoreBOM", decoder_get, NULL, 2),
	JS_PROP_STRING_DEF("[Symbol.toStringTag]", "TextDecoder", JS_PROP_CONFIGURABLE),
};

/* ---- the globals ---- */

JSValue jsrt_class(JSContext *ctx, const char *name, JSCFunction *ctor, int length, JSClassID id, const JSCFunctionListEntry *proto, int nproto,
		   const JSCFunctionListEntry *statics, int nstatics)
{
	JSValue p = JS_NewObject(ctx);
	JS_SetPropertyFunctionList(ctx, p, proto, nproto);
	JSValue c = JS_NewCFunction2(ctx, ctor, name, length, JS_CFUNC_constructor, 0);
	JS_SetConstructor(ctx, c, p);
	if (statics)
		JS_SetPropertyFunctionList(ctx, c, statics, nstatics);
	if (id)
		JS_SetClassProto(ctx, id, p);
	else
		JS_FreeValue(ctx, p);
	return c;
}

void jsrt_web_init(JSContext *ctx)
{
	JSRuntime *rt = JS_GetRuntime(ctx);
	JSClassDef sd = {.class_name = "AbortSignal", .finalizer = signal_finalizer, .gc_mark = signal_mark};
	JSClassDef cd = {.class_name = "AbortController", .finalizer = controller_finalizer, .gc_mark = controller_mark};
	JSClassDef dd = {.class_name = "TextDecoder", .finalizer = decoder_finalizer};
	JS_NewClassID(rt, &signal_class);
	JS_NewClassID(rt, &controller_class);
	JS_NewClassID(rt, &decoder_class);
	JS_NewClass(rt, signal_class, &sd);
	JS_NewClass(rt, controller_class, &cd);
	JS_NewClass(rt, decoder_class, &dd);
	JSValue g = JS_GetGlobalObject(ctx);
#define N(a) (int)(sizeof a / sizeof a[0])
	JS_SetPropertyStr(ctx, g, "AbortSignal", jsrt_class(ctx, "AbortSignal", signal_ctor, 0, signal_class, signal_proto, N(signal_proto), signal_static, N(signal_static)));
	JS_SetPropertyStr(ctx, g, "AbortController", jsrt_class(ctx, "AbortController", controller_ctor, 0, controller_class, controller_proto, N(controller_proto), NULL, 0));
	JS_SetPropertyStr(ctx, g, "TextEncoder", jsrt_class(ctx, "TextEncoder", encoder_ctor, 0, 0, encoder_proto, N(encoder_proto), NULL, 0));
	JS_SetPropertyStr(ctx, g, "TextDecoder", jsrt_class(ctx, "TextDecoder", decoder_ctor, 0, decoder_class, decoder_proto, N(decoder_proto), NULL, 0));
#undef N
	JS_FreeValue(ctx, g);
}
