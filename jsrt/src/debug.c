/*
 * The debugger of an app installed in developer mode (29-app-development.md
 * §3): the Shell starts kario-jsrt with KARIO_JSRT_DEBUG_FD, a socket whose
 * other end is the kario command on the computer (kario device debug, over
 * the developer link), and the engine's debug handler (kario-debugger.patch)
 * runs every instruction of the app through here. Or the Shell starts the
 * app with KARIO_JSRT_DEBUG_ATTACH_FD, a socket on which it may later hand
 * over such a socket (SCM_RIGHTS) while the app runs: the debugger attaches
 * without a restart, one at a time. On the socket go JSON lines: requests
 * {"id", "cmd", ...}, each answered {"id", "ok": true, ...} or {"id",
 * "error"}, and events {"event", ...}. Positions are main.js's, lines and
 * columns from 1; the computer maps them to the TypeScript through the
 * source map.
 *
 *	run {stopOnEntry}                    start the app (once breakpoints are set);
 *	                                     attached, run on (stopOnEntry: pause)
 *	setBreakpoints {file, breakpoints: [{line, condition, hitCondition, logMessage, group}]}
 *	setFunctionBreakpoints {breakpoints: [{name, condition, hitCondition}]}
 *	setExceptions {uncaught, all}
 *	pause | continue | next | stepIn | stepOut
 *	stack                                {frames: [{name, file, line, column} | {name, native}]}
 *	variables {frame, scope} | {ref[, filter, start, count]}
 *	                                     {variables: [{name, value, type, ref[, indexed, named, lazy]}]}
 *	evaluate {frame, expression}         {value, type, ref}  (no frame: global, running)
 *	setVariable {frame, scope, name, value} | {ref, name, value}
 *
 *	events: hello {engine, app, main, attached}, stopped {reason, text},
 *	        output {category, text[, file, line, column]}
 *
 * A Map's, a Set's or an array's description says how many entries it has
 * (indexed) and how many named properties beside them (named); variables
 * with filter "indexed" gives count of the entries from start, with
 * "named" the rest (a class's getters among them, found up the prototype
 * chain), with none the first 500 entries and the rest. A getter is lazy:
 * its ref's variables read it then, calling the app's code.
 *
 * scope is "local" (arguments and locals), "closure" (the outer variables
 * the function uses) or "global"; ref names an object a reply described,
 * until the app runs on. hitCondition is a count of the times the
 * breakpoint's condition held: "N" or ">=N" from the Nth on, "=N" or "==N"
 * only the Nth, ">N", "<N", "<=N", "%N" every Nth; breakpoints of one group
 * (one line of the TypeScript, several of main.js) count together. A
 * logpoint (logMessage, "{expression}" replaced by its value) writes an
 * output line of category "console" instead of stopping. A function
 * breakpoint stops as a function of that name (or of the last part of a
 * dotted name, "Counter.bump") is entered.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#include "jsrt.h"

/* How many instructions run between looks for a pause request. */
#define POLL_EVERY 4096
/* Frames whose last line is tracked (deeper ones count every line). */
#define TRACKED 256
#define MAX_FRAMES 256
#define MAX_REFS 100000

enum { RUN, STEP_IN, STEP_OVER, STEP_OUT };

enum { HIT_ANY, HIT_EQ, HIT_GT, HIT_GE, HIT_LT, HIT_LE, HIT_MOD };

typedef struct {
	int op, n, count;
} hits;

typedef struct {
	int file, line, group;
	char *condition, *log;
	hits hits;
} breakpoint;

typedef struct {
	char *name, *condition;
	hits hits;
} fn_breakpoint;

/* What the debugger keeps in a function (JSDebugPoint.data): its position
 * table, its name and, as of breakpoint generation gen, whether a line
 * breakpoint falls in its lines and a function breakpoint names it. One
 * block, as the engine frees it. */
typedef struct {
	uint32_t gen;
	int file, first, last, has_bp, has_fbp, hint, n;
	const char *name;
	JSDebugLine lines[];
} fninfo;

/* A frame's last instruction and line: a new line, or a jump back, is
 * where a breakpoint or a step stops. */
typedef struct {
	const void *frame;
	uint32_t pc;
	int line;
} tracked;

struct jsrt_debug {
	int fd;
	/* the Shell hands over a debugger's socket here (-1: none) */
	int listen;
	int attached;
	const char *main;
	dstr in;
	int closed, paused, busy, in_hook;
	int pause_req, entry;
	int mode, step_depth;
	const void *step_frame;
	const void *stop_frame;
	uint32_t stop_pc;
	int ticks;
	uint32_t gen;
	char **files;
	int nfiles;
	breakpoint *bps;
	int nbps;
	fn_breakpoint *fbps;
	int nfbps;
	int exc_uncaught, exc_all;
	/* the frame the last tracked instruction was in, NULL after others */
	const void *cur_frame;
	int depth;
	tracked track[TRACKED];
	JSValue helper; /* { preview, props } */
	JSValue evaluator;
	JSValue *refs;
	int nrefs;
};

static int file_id(jsrt_debug *d, const char *name)
{
	for (int i = 0; i < d->nfiles; i++)
		if (!strcmp(d->files[i], name))
			return i;
	char **grown = realloc(d->files, (d->nfiles + 1) * sizeof *grown);
	if (!grown)
		return -1;
	d->files = grown;
	d->files[d->nfiles] = strdup(name);
	return d->nfiles++;
}

/* ---- the socket ---- */

static void send_line(jsrt_debug *d, const char *s, size_t n)
{
	while (n > 0 && !d->closed) {
		ssize_t w = write(d->fd, s, n);
		if (w < 0 && errno == EINTR)
			continue;
		if (w <= 0) {
			d->closed = 1;
			return;
		}
		s += w;
		n -= (size_t)w;
	}
}

static void send_value(jsrt_debug *d, JSContext *ctx, JSValue v)
{
	JSValue s = JS_JSONStringify(ctx, v, JS_UNDEFINED, JS_UNDEFINED);
	JS_FreeValue(ctx, v);
	if (JS_IsString(s)) {
		size_t n;
		const char *c = JS_ToCStringLen(ctx, &n, s);
		if (c) {
			dstr line = {0};
			dstr_put(&line, c, n);
			dstr_puts(&line, "\n");
			send_line(d, line.s, line.len);
			free(line.s);
			JS_FreeCString(ctx, c);
		}
	} else {
		JS_FreeValue(ctx, JS_GetException(ctx));
	}
	JS_FreeValue(ctx, s);
}

static void set_str(JSContext *ctx, JSValue o, const char *k, const char *v)
{
	JS_SetPropertyStr(ctx, o, k, JS_NewString(ctx, v ? v : ""));
}

static void set_int(JSContext *ctx, JSValue o, const char *k, int64_t v)
{
	JS_SetPropertyStr(ctx, o, k, JS_NewInt64(ctx, v));
}

static void event(jsrt_debug *d, JSContext *ctx, const char *name, JSValue body)
{
	if (JS_IsUndefined(body))
		body = JS_NewObject(ctx);
	set_str(ctx, body, "event", name);
	send_value(d, ctx, body);
}

static void reply(jsrt_debug *d, JSContext *ctx, int64_t id, JSValue body)
{
	if (JS_IsUndefined(body))
		body = JS_NewObject(ctx);
	set_int(ctx, body, "id", id);
	JS_SetPropertyStr(ctx, body, "ok", JS_TRUE);
	send_value(d, ctx, body);
}

static void fail(jsrt_debug *d, JSContext *ctx, int64_t id, const char *why)
{
	JSValue body = JS_NewObject(ctx);
	set_int(ctx, body, "id", id);
	set_str(ctx, body, "error", why);
	send_value(d, ctx, body);
}

/* fill reads what the socket has (waiting for it with wait): -1 once the
 * computer has gone. */
static int fill(jsrt_debug *d, int wait)
{
	if (d->closed)
		return -1;
	struct pollfd p = {.fd = d->fd, .events = POLLIN};
	int n = poll(&p, 1, wait ? -1 : 0);
	if (n < 0)
		return errno == EINTR ? 0 : -1;
	if (n == 0)
		return 0;
	char buf[65536];
	ssize_t r = read(d->fd, buf, sizeof buf);
	if (r < 0)
		return errno == EINTR || errno == EAGAIN ? 0 : (d->closed = 1, -1);
	if (r == 0) {
		d->closed = 1;
		return -1;
	}
	if (d->in.len + (size_t)r > (16 << 20)) {
		d->closed = 1;
		return -1;
	}
	dstr_put(&d->in, buf, (size_t)r);
	return 1;
}

/* next_line takes a whole line from what was read (malloc'd), or NULL. */
static char *next_line(jsrt_debug *d)
{
	if (!d->in.s)
		return NULL;
	char *nl = memchr(d->in.s, '\n', d->in.len);
	if (!nl)
		return NULL;
	size_t n = (size_t)(nl - d->in.s);
	char *line = strndup(d->in.s, n);
	memmove(d->in.s, nl + 1, d->in.len - n - 1);
	d->in.len -= n + 1;
	d->in.s[d->in.len] = 0;
	return line;
}

/* ---- values ---- */

static char *exception_text(JSContext *ctx);
static int64_t get_int(JSContext *ctx, JSValueConst o, const char *k, int64_t def);

static void drop_refs(jsrt_debug *d, JSContext *ctx)
{
	for (int i = 0; i < d->nrefs; i++)
		JS_FreeValue(ctx, d->refs[i]);
	free(d->refs);
	d->refs = NULL;
	d->nrefs = 0;
}

static int add_ref(jsrt_debug *d, JSContext *ctx, JSValueConst v)
{
	if (d->nrefs >= MAX_REFS)
		return 0;
	JSValue *grown = realloc(d->refs, (d->nrefs + 1) * sizeof *grown);
	if (!grown)
		return 0;
	d->refs = grown;
	d->refs[d->nrefs++] = JS_DupValue(ctx, v);
	return d->nrefs;
}

static JSValue call_helper_n(jsrt_debug *d, JSContext *ctx, const char *fn, int argc, JSValueConst *argv)
{
	JSValue f = JS_GetPropertyStr(ctx, d->helper, fn);
	JSValue r = JS_Call(ctx, f, JS_UNDEFINED, argc, argv);
	JS_FreeValue(ctx, f);
	return r;
}

static JSValue call_helper(jsrt_debug *d, JSContext *ctx, const char *fn, JSValueConst arg)
{
	return call_helper_n(d, ctx, fn, 1, &arg);
}

/* describe is {name, value, type, ref} for v: its preview, and a reference
 * for an object to open. */
static JSValue describe(jsrt_debug *d, JSContext *ctx, const char *name, JSValueConst v)
{
	JSValue o = JS_NewObject(ctx);
	if (name)
		set_str(ctx, o, "name", name);
	JSValue p = call_helper(d, ctx, "preview", v);
	if (JS_IsException(p)) {
		JS_FreeValue(ctx, JS_GetException(ctx));
		set_str(ctx, o, "value", "?");
		set_str(ctx, o, "type", "object");
		set_int(ctx, o, "ref", 0);
		return o;
	}
	JS_SetPropertyStr(ctx, o, "value", JS_GetPropertyUint32(ctx, p, 0));
	JS_SetPropertyStr(ctx, o, "type", JS_GetPropertyUint32(ctx, p, 1));
	JSValue open = JS_GetPropertyUint32(ctx, p, 2);
	set_int(ctx, o, "ref", JS_ToBool(ctx, open) ? add_ref(d, ctx, v) : 0);
	/* A Map's, a Set's or an array's entries come in pages: how many, and
	 * how many named properties beside them. */
	if (JS_ToBool(ctx, open)) {
		JSValue n = call_helper(d, ctx, "size", v);
		int64_t indexed = JS_IsException(n) ? -1 : get_int(ctx, n, "0", -1);
		if (JS_IsException(n))
			JS_FreeValue(ctx, JS_GetException(ctx));
		else if (indexed >= 0) {
			set_int(ctx, o, "indexed", indexed);
			set_int(ctx, o, "named", get_int(ctx, n, "1", 0));
		}
		JS_FreeValue(ctx, n);
	}
	JS_FreeValue(ctx, open);
	JS_FreeValue(ctx, p);
	return o;
}

static int identifier(const char *s)
{
	if (!*s || (*s >= '0' && *s <= '9'))
		return 0;
	for (; *s; s++) {
		unsigned char c = (unsigned char)*s;
		if (!(c >= 0x80 || c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')))
			return 0;
	}
	return 1;
}

/* A variable of a frame as the debugger shows it. */
typedef struct {
	char *name;
	int closure, index, is_const, shadowed;
	JSValue value;
} var;

static void free_vars(JSContext *ctx, var *v, int n)
{
	for (int i = 0; i < n; i++) {
		free(v[i].name);
		JS_FreeValue(ctx, v[i].value);
	}
	free(v);
}

/* frame_vars appends the frame's variables of one kind: those with names a
 * program can write and a value, the innermost of each name (a block's
 * variables come after the function's). */
static void frame_vars(JSContext *ctx, int level, int closure, var **out, int *n)
{
	JSValue list = JS_DebugFrameVariables(ctx, level, closure);
	JSValue len = JS_GetPropertyStr(ctx, list, "length");
	int64_t count = 0;
	JS_ToInt64(ctx, &count, len);
	JS_FreeValue(ctx, len);
	int start = *n;
	for (int64_t i = 0; i < count; i++) {
		JSValue item = JS_GetPropertyUint32(ctx, list, (uint32_t)i);
		char *name = jsrt_get_string(ctx, item, "name");
		JSValue value = JS_GetPropertyStr(ctx, item, "value");
		int has = 0;
		JSAtom a = JS_NewAtom(ctx, "value");
		has = JS_HasProperty(ctx, item, a) > 0;
		JS_FreeAtom(ctx, a);
		if (!name || !identifier(name) || !has) {
			free(name);
			JS_FreeValue(ctx, value);
			JS_FreeValue(ctx, item);
			continue;
		}
		JSValue idx = JS_GetPropertyStr(ctx, item, "index"), cst = JS_GetPropertyStr(ctx, item, "const");
		int32_t index = 0;
		JS_ToInt32(ctx, &index, idx);
		int is_const = JS_ToBool(ctx, cst);
		JS_FreeValue(ctx, idx);
		JS_FreeValue(ctx, cst);
		JS_FreeValue(ctx, item);
		int k;
		for (k = start; k < *n && strcmp((*out)[k].name, name); k++)
			;
		if (k == *n) {
			var *grown = realloc(*out, (*n + 1) * sizeof *grown);
			if (!grown) {
				free(name);
				JS_FreeValue(ctx, value);
				break;
			}
			*out = grown;
			(*n)++;
		} else {
			free((*out)[k].name);
			JS_FreeValue(ctx, (*out)[k].value);
		}
		(*out)[k] = (var){.name = name, .closure = closure, .index = index, .is_const = is_const, .value = value};
	}
	JS_FreeValue(ctx, list);
}

/* scope_vars: the closure's variables, then the locals over them. */
static var *scope_vars(JSContext *ctx, int level, int *n)
{
	var *v = NULL;
	*n = 0;
	frame_vars(ctx, level, 1, &v, n);
	int outer = *n;
	frame_vars(ctx, level, 0, &v, n);
	for (int i = 0; i < outer; i++)
		for (int k = outer; k < *n; k++)
			if (!strcmp(v[i].name, v[k].name))
				v[i].shadowed = 1;
	return v;
}

static const char helper_src[] =
	"(function () {\n"
	"  const names = Object.getOwnPropertyNames, desc = Object.getOwnPropertyDescriptor, proto = Object.getPrototypeOf;\n"
	"  const isArray = Array.isArray, quote = JSON.stringify, MapC = Map, SetC = Set, ErrorC = Error, get = Reflect.get;\n"
	"  const getter = Symbol('getter');\n"
	"  const str = (v) => { try { return String(v); } catch (e) { return '?'; } };\n"
	"  const index = (k) => /^(0|[1-9][0-9]*)$/.test(k);\n"
	"  function preview(v) {\n"
	"    switch (typeof v) {\n"
	"    case 'undefined': return ['undefined', 'undefined', false];\n"
	"    case 'string': return [quote(v.length > 1000 ? v.slice(0, 1000) + '...' : v), 'string', false];\n"
	"    case 'number': case 'boolean': return [str(v), typeof v, false];\n"
	"    case 'bigint': return [str(v) + 'n', 'bigint', false];\n"
	"    case 'symbol': return [v.toString(), 'symbol', false];\n"
	"    case 'function': return ['function ' + (v.name || '(anonymous)'), 'function', true];\n"
	"    }\n"
	"    if (v === null) return ['null', 'object', false];\n"
	"    if (isArray(v)) return ['Array(' + v.length + ')', 'object', true];\n"
	"    if (v instanceof ErrorC) return [str(v), 'object', true];\n"
	"    let c = '';\n"
	"    try { const p = proto(v); c = (p && p.constructor && p.constructor.name) || ''; } catch (e) {}\n"
	"    if (v instanceof MapC || v instanceof SetC) return [c + '(' + v.size + ')', 'object', true];\n"
	"    return [c && c !== 'Object' ? c : 'Object', 'object', true];\n"
	"  }\n"
	"  const plain = (p) => p === null || p === Object.prototype || p === Array.prototype || p === Function.prototype || p === MapC.prototype || p === SetC.prototype;\n"
	"  function named(o) {\n"
	"    const out = [];\n"
	"    for (const k of names(o)) {\n"
	"      if (out.length >= 1000) break;\n"
	"      if (isArray(o) && index(k)) continue;\n"
	"      const d = desc(o, k);\n"
	"      if (d) out.push('value' in d ? [k, d.value, false] : [k, undefined, true]);\n"
	"    }\n"
	"    for (let p = proto(o); !plain(p) && out.length < 1000; p = proto(p))\n"
	"      for (const k of names(p)) { const d = desc(p, k); if (d && d.get && !out.some((e) => e[0] === k)) out.push([k, undefined, true]); }\n"
	"    const p = proto(o);\n"
	"    if (!plain(p)) out.push(['[[Prototype]]', p, false]);\n"
	"    return out;\n"
	"  }\n"
	"  function entries(o, from, n) {\n"
	"    const out = [];\n"
	"    let i = 0;\n"
	"    if (o instanceof MapC) { for (const [k, v] of o) { if (i >= from + n) break; if (i++ >= from) out.push(['[' + preview(k)[0] + ']', v, false]); } }\n"
	"    else if (o instanceof SetC) { for (const v of o) { if (i >= from + n) break; if (i >= from) out.push(['[' + i + ']', v, false]); i++; } }\n"
	"    else if (isArray(o)) { for (i = from; i < o.length && i < from + n; i++) { const d = desc(o, String(i)); out.push(d ? ('value' in d ? [String(i), d.value, false] : [String(i), undefined, true]) : [String(i), undefined, false]); } }\n"
	"    return out;\n"
	"  }\n"
	"  function props(o, filter, start, count) {\n"
	"    const from = start > 0 ? start : 0, n = count > 0 ? count : 500;\n"
	"    if (filter === 'indexed') return entries(o, from, n);\n"
	"    if (filter === 'named') return named(o);\n"
	"    const out = isArray(o) ? [] : entries(o, 0, 500);\n"
	"    if (isArray(o)) for (const k of names(o)) { if (out.length >= 1000) break; const d = desc(o, k); if (d && index(k)) out.push('value' in d ? [k, d.value, false] : [k, undefined, true]); }\n"
	"    return out.concat(named(o));\n"
	"  }\n"
	"  function size(o) {\n"
	"    const n = o instanceof MapC || o instanceof SetC ? o.size : isArray(o) ? o.length : -1;\n"
	"    return n < 0 ? [-1, 0] : [n, named(o).length];\n"
	"  }\n"
	"  const thunk = (o, k) => ({ [getter]: true, o, k });\n"
	"  const lazy = (t) => t !== null && typeof t === 'object' && t[getter] === true;\n"
	"  const read = (t) => get(t.o, t.k);\n"
	"  return { preview, props, size, thunk, lazy, read };\n"
	"})()";

/* Sloppy code: the scope object stands for the frame's variables. */
static const char evaluator_src[] =
	"(function (kario$scope, kario$expression) { with (kario$scope) { return eval(kario$expression); } })";

/* ---- frames ---- */

/* names: a function breakpoint's name is the function's, or ends in
 * ".<the function's>" (a method by its class). */
static int names(const char *bp, const char *fn)
{
	if (!*fn)
		return 0;
	if (!strcmp(bp, fn))
		return 1;
	const char *dot = strrchr(bp, '.');
	return dot && !strcmp(dot + 1, fn);
}

static fninfo *function_info(jsrt_debug *d, JSContext *ctx, const JSDebugPoint *at)
{
	fninfo *f = *at->data;
	if (!f) {
		JSDebugLine *lines;
		int n = JS_DebugFrameLines(ctx, 0, &lines);
		if (n < 1)
			return NULL;
		JSValue info = JS_DebugFrameInfo(ctx, 0);
		char *file = jsrt_get_string(ctx, info, "file"), *name = jsrt_get_string(ctx, info, "name");
		JS_FreeValue(ctx, info);
		size_t namelen = name ? strlen(name) : 0;
		f = js_malloc_rt(JS_GetRuntime(ctx), sizeof *f + (size_t)n * sizeof f->lines[0] + namelen + 1);
		if (!f) {
			js_free(ctx, lines);
			free(file);
			free(name);
			return NULL;
		}
		memcpy(f->lines, lines, (size_t)n * sizeof lines[0]);
		js_free(ctx, lines);
		char *own = (char *)&f->lines[n];
		memcpy(own, name ? name : "", namelen + 1);
		f->name = own;
		f->n = n;
		f->hint = 0;
		f->first = f->last = f->lines[0].line;
		for (int i = 0; i < n; i++) {
			if (f->lines[i].line < f->first)
				f->first = f->lines[i].line;
			if (f->lines[i].line > f->last)
				f->last = f->lines[i].line;
		}
		f->file = file_id(d, file ? file : "");
		free(file);
		free(name);
		f->gen = d->gen - 1;
		*at->data = f;
	}
	if (f->gen != d->gen) {
		f->gen = d->gen;
		f->has_bp = f->has_fbp = 0;
		for (int i = 0; i < d->nbps; i++)
			if (d->bps[i].file == f->file && d->bps[i].line >= f->first && d->bps[i].line <= f->last)
				f->has_bp = 1;
		for (int i = 0; i < d->nfbps; i++)
			if (names(d->fbps[i].name, f->name))
				f->has_fbp = 1;
	}
	return f;
}

static int line_at(fninfo *f, uint32_t pc)
{
	int i = f->hint;
	if (!(f->lines[i].pc <= pc && (i + 1 == f->n || pc < f->lines[i + 1].pc))) {
		int lo = 0, hi = f->n - 1;
		while (lo < hi) {
			int mid = (lo + hi + 1) / 2;
			if (f->lines[mid].pc <= pc)
				lo = mid;
			else
				hi = mid - 1;
		}
		i = f->hint = lo;
	}
	return f->lines[i].line;
}

/* ---- requests ---- */

static int64_t get_int(JSContext *ctx, JSValueConst o, const char *k, int64_t def)
{
	JSValue v = JS_GetPropertyStr(ctx, o, k);
	int64_t n = def;
	if (JS_IsNumber(v))
		JS_ToInt64(ctx, &n, v);
	JS_FreeValue(ctx, v);
	return n;
}

static int get_bool(JSContext *ctx, JSValueConst o, const char *k)
{
	JSValue v = JS_GetPropertyStr(ctx, o, k);
	int b = JS_ToBool(ctx, v) > 0;
	JS_FreeValue(ctx, v);
	return b;
}

/* parse_hits reads a hit condition (the top of this file); -1 when it is
 * none of those. */
static int parse_hits(const char *s, hits *h)
{
	*h = (hits){0};
	if (!s)
		return 0;
	while (*s == ' ' || *s == '\t')
		s++;
	if (!*s)
		return 0;
	static const struct {
		const char *op;
		int kind;
	} ops[] = {{">=", HIT_GE}, {"<=", HIT_LE}, {"==", HIT_EQ}, {">", HIT_GT}, {"<", HIT_LT}, {"=", HIT_EQ}, {"%", HIT_MOD}};
	h->op = HIT_GE;
	for (size_t i = 0; i < sizeof ops / sizeof ops[0]; i++) {
		size_t n = strlen(ops[i].op);
		if (!strncmp(s, ops[i].op, n)) {
			h->op = ops[i].kind;
			s += n;
			break;
		}
	}
	while (*s == ' ' || *s == '\t')
		s++;
	char *end;
	long n = (*s >= '0' && *s <= '9') ? strtol(s, &end, 10) : -1;
	if (n < 0 || n > 1000000000)
		return -1;
	while (*end == ' ' || *end == '\t')
		end++;
	if (*end || (h->op == HIT_MOD && n == 0))
		return -1;
	h->n = (int)n;
	return 0;
}

/* hit counts one more time the condition held: whether this one stops. */
static int hit(hits *h)
{
	int c = ++h->count;
	switch (h->op) {
	case HIT_EQ:
		return c == h->n;
	case HIT_GT:
		return c > h->n;
	case HIT_GE:
		return c >= h->n;
	case HIT_LT:
		return c < h->n;
	case HIT_LE:
		return c <= h->n;
	case HIT_MOD:
		return c % h->n == 0;
	}
	return 1;
}

/* opt_string is a string property, NULL when missing or empty. */
static char *opt_string(JSContext *ctx, JSValueConst o, const char *k)
{
	char *s = jsrt_get_string(ctx, o, k);
	if (s && !*s) {
		free(s);
		s = NULL;
	}
	return s;
}

/* checked is a reply's entry for one breakpoint: verified unless why. */
static JSValue checked(JSContext *ctx, int line, const char *why)
{
	JSValue o = JS_NewObject(ctx);
	if (line > 0)
		set_int(ctx, o, "line", line);
	JS_SetPropertyStr(ctx, o, "verified", JS_NewBool(ctx, !why));
	if (why)
		set_str(ctx, o, "message", why);
	return o;
}

static void set_breakpoints(jsrt_debug *d, JSContext *ctx, int64_t id, JSValueConst req)
{
	char *file = jsrt_get_string(ctx, req, "file");
	if (!file) {
		fail(d, ctx, id, "setBreakpoints: no file");
		return;
	}
	int f = file_id(d, file);
	free(file);
	int keep = 0;
	for (int i = 0; i < d->nbps; i++) {
		if (d->bps[i].file == f) {
			free(d->bps[i].condition);
			free(d->bps[i].log);
		} else {
			d->bps[keep++] = d->bps[i];
		}
	}
	d->nbps = keep;
	JSValue list = JS_GetPropertyStr(ctx, req, "breakpoints"), out = JS_NewArray(ctx);
	int64_t n = get_int(ctx, list, "length", 0);
	for (int64_t i = 0; i < n; i++) {
		JSValue b = JS_GetPropertyUint32(ctx, list, (uint32_t)i);
		int line = (int)get_int(ctx, b, "line", 0);
		char *cond = opt_string(ctx, b, "condition"), *log = opt_string(ctx, b, "logMessage");
		char *hc = opt_string(ctx, b, "hitCondition");
		hits h;
		const char *why = line <= 0 ? "no line" : parse_hits(hc, &h) < 0 ? "a hit count is N, =N, >N, >=N, <N, <=N or %N" : NULL;
		free(hc);
		breakpoint *grown = why ? NULL : realloc(d->bps, (d->nbps + 1) * sizeof *grown);
		if (!why && !grown)
			why = "out of memory";
		JS_SetPropertyUint32(ctx, out, (uint32_t)i, checked(ctx, line, why));
		if (grown) {
			d->bps = grown;
			d->bps[d->nbps++] = (breakpoint){.file = f, .line = line, .group = (int)get_int(ctx, b, "group", -1),
							 .condition = cond, .log = log, .hits = h};
		} else {
			free(cond);
			free(log);
		}
		JS_FreeValue(ctx, b);
	}
	JS_FreeValue(ctx, list);
	d->gen++;
	JSValue body = JS_NewObject(ctx);
	JS_SetPropertyStr(ctx, body, "breakpoints", out);
	reply(d, ctx, id, body);
}

static void free_fn_breakpoints(jsrt_debug *d)
{
	for (int i = 0; i < d->nfbps; i++) {
		free(d->fbps[i].name);
		free(d->fbps[i].condition);
	}
	free(d->fbps);
	d->fbps = NULL;
	d->nfbps = 0;
}

static void set_fn_breakpoints(jsrt_debug *d, JSContext *ctx, int64_t id, JSValueConst req)
{
	free_fn_breakpoints(d);
	JSValue list = JS_GetPropertyStr(ctx, req, "breakpoints"), out = JS_NewArray(ctx);
	int64_t n = get_int(ctx, list, "length", 0);
	for (int64_t i = 0; i < n; i++) {
		JSValue b = JS_GetPropertyUint32(ctx, list, (uint32_t)i);
		char *name = opt_string(ctx, b, "name"), *cond = opt_string(ctx, b, "condition"), *hc = opt_string(ctx, b, "hitCondition");
		hits h;
		const char *why = !name ? "no function name" : parse_hits(hc, &h) < 0 ? "a hit count is N, =N, >N, >=N, <N, <=N or %N" : NULL;
		free(hc);
		fn_breakpoint *grown = why ? NULL : realloc(d->fbps, (d->nfbps + 1) * sizeof *grown);
		if (!why && !grown)
			why = "out of memory";
		JS_SetPropertyUint32(ctx, out, (uint32_t)i, checked(ctx, 0, why));
		if (grown) {
			d->fbps = grown;
			d->fbps[d->nfbps++] = (fn_breakpoint){.name = name, .condition = cond, .hits = h};
		} else {
			free(name);
			free(cond);
		}
		JS_FreeValue(ctx, b);
	}
	JS_FreeValue(ctx, list);
	d->gen++;
	JSValue body = JS_NewObject(ctx);
	JS_SetPropertyStr(ctx, body, "breakpoints", out);
	reply(d, ctx, id, body);
}

static void stack(jsrt_debug *d, JSContext *ctx, int64_t id)
{
	JSValue frames = JS_NewArray(ctx);
	int n = JS_DebugFrameCount(ctx);
	for (int i = 0; i < n && i < MAX_FRAMES; i++)
		JS_SetPropertyUint32(ctx, frames, (uint32_t)i, JS_DebugFrameInfo(ctx, i));
	JSValue body = JS_NewObject(ctx);
	JS_SetPropertyStr(ctx, body, "frames", frames);
	set_int(ctx, body, "total", n);
	reply(d, ctx, id, body);
}

static void variables(jsrt_debug *d, JSContext *ctx, int64_t id, JSValueConst req)
{
	JSValue out = JS_NewArray(ctx);
	uint32_t k = 0;
	int64_t ref = get_int(ctx, req, "ref", 0);
	char *scope = jsrt_get_string(ctx, req, "scope");
	d->busy++;
	JSValue thunk = ref > 0 && ref <= d->nrefs ? call_helper(d, ctx, "lazy", d->refs[ref - 1]) : JS_FALSE;
	if (JS_IsException(thunk)) {
		JS_FreeValue(ctx, JS_GetException(ctx));
		thunk = JS_FALSE;
	}
	if (JS_ToBool(ctx, thunk) > 0) {
		/* A getter, read now that the editor shows it: runs the app's
		 * code, as Node's debugger does. */
		JSValue v = call_helper(d, ctx, "read", d->refs[ref - 1]);
		if (JS_IsException(v)) {
			char *s = exception_text(ctx);
			JSValue o = JS_NewObject(ctx);
			set_str(ctx, o, "name", "");
			set_str(ctx, o, "value", s ? s : "exception");
			set_str(ctx, o, "type", "error");
			set_int(ctx, o, "ref", 0);
			JS_SetPropertyUint32(ctx, out, k++, o);
			free(s);
		} else {
			JS_SetPropertyUint32(ctx, out, k++, describe(d, ctx, "", v));
		}
		JS_FreeValue(ctx, v);
	} else if (ref > 0 && ref <= d->nrefs) {
		char *filter = jsrt_get_string(ctx, req, "filter");
		JSValue args[4] = {d->refs[ref - 1], filter ? JS_NewString(ctx, filter) : JS_UNDEFINED, JS_GetPropertyStr(ctx, req, "start"),
				   JS_GetPropertyStr(ctx, req, "count")};
		free(filter);
		JSValue props = call_helper_n(d, ctx, "props", 4, args);
		for (int i = 1; i < 4; i++)
			JS_FreeValue(ctx, args[i]);
		int64_t n = JS_IsException(props) ? 0 : get_int(ctx, props, "length", 0);
		for (int64_t i = 0; i < n; i++) {
			JSValue e = JS_GetPropertyUint32(ctx, props, (uint32_t)i);
			JSValue kv = JS_GetPropertyUint32(ctx, e, 0), v = JS_GetPropertyUint32(ctx, e, 1);
			JSValue acc = JS_GetPropertyUint32(ctx, e, 2);
			const char *name = JS_ToCString(ctx, kv);
			JSValue o;
			if (JS_ToBool(ctx, acc)) {
				JSValue targs[2] = {d->refs[ref - 1], kv};
				JSValue t = call_helper_n(d, ctx, "thunk", 2, targs);
				o = JS_NewObject(ctx);
				set_str(ctx, o, "name", name);
				set_str(ctx, o, "value", "(...)");
				set_str(ctx, o, "type", "accessor");
				set_int(ctx, o, "ref", JS_IsException(t) ? 0 : add_ref(d, ctx, t));
				JS_SetPropertyStr(ctx, o, "lazy", JS_TRUE);
				if (JS_IsException(t))
					JS_FreeValue(ctx, JS_GetException(ctx));
				JS_FreeValue(ctx, t);
			} else {
				o = describe(d, ctx, name, v);
			}
			JS_SetPropertyUint32(ctx, out, k++, o);
			JS_FreeCString(ctx, name);
			JS_FreeValue(ctx, kv);
			JS_FreeValue(ctx, v);
			JS_FreeValue(ctx, acc);
			JS_FreeValue(ctx, e);
		}
		if (JS_IsException(props))
			JS_FreeValue(ctx, JS_GetException(ctx));
		JS_FreeValue(ctx, props);
	} else if (scope && !strcmp(scope, "global")) {
		JSValue g = JS_GetGlobalObject(ctx);
		JS_SetPropertyUint32(ctx, out, k++, describe(d, ctx, "globalThis", g));
		JS_FreeValue(ctx, g);
	} else if (scope && d->paused) {
		int n = 0, closure = !strcmp(scope, "closure");
		var *v = NULL;
		frame_vars(ctx, (int)get_int(ctx, req, "frame", 0), closure, &v, &n);
		for (int i = 0; i < n; i++)
			JS_SetPropertyUint32(ctx, out, k++, describe(d, ctx, v[i].name, v[i].value));
		free_vars(ctx, v, n);
	}
	d->busy--;
	free(scope);
	JSValue body = JS_NewObject(ctx);
	JS_SetPropertyStr(ctx, body, "variables", out);
	reply(d, ctx, id, body);
}

/* eval_in runs expr in the frame at level (-1: none) with its variables in
 * scope, and writes back those the code changed. */
static JSValue eval_in(jsrt_debug *d, JSContext *ctx, int level, const char *expr)
{
	int n = 0;
	var *v = level >= 0 ? scope_vars(ctx, level, &n) : NULL;
	JSValue scope = JS_NewObjectProto(ctx, JS_NULL), self = JS_UNDEFINED;
	for (int i = 0; i < n; i++) {
		if (!strcmp(v[i].name, "this")) {
			JS_FreeValue(ctx, self);
			self = JS_DupValue(ctx, v[i].value);
			continue;
		}
		JS_DefinePropertyValueStr(ctx, scope, v[i].name, JS_DupValue(ctx, v[i].value),
					  JS_PROP_ENUMERABLE | (v[i].is_const ? 0 : JS_PROP_WRITABLE | JS_PROP_CONFIGURABLE));
	}
	JSValue args[2] = {scope, JS_NewString(ctx, expr)};
	d->busy++;
	JSValue r = JS_Call(ctx, d->evaluator, self, 2, args);
	d->busy--;
	if (!JS_IsException(r)) {
		for (int i = 0; i < n; i++) {
			if (v[i].shadowed || v[i].is_const || !strcmp(v[i].name, "this"))
				continue;
			JSValue now = JS_GetPropertyStr(ctx, scope, v[i].name);
			if (!JS_IsSameValue(ctx, now, v[i].value))
				JS_DebugSetVariable(ctx, level, v[i].closure, v[i].index, JS_DupValue(ctx, now));
			JS_FreeValue(ctx, now);
		}
	}
	JS_FreeValue(ctx, args[1]);
	JS_FreeValue(ctx, scope);
	JS_FreeValue(ctx, self);
	free_vars(ctx, v, n);
	return r;
}

static char *exception_text(JSContext *ctx)
{
	JSValue e = JS_GetException(ctx);
	char *s = jsrt_format(ctx, e);
	JS_FreeValue(ctx, e);
	return s;
}

static void evaluate(jsrt_debug *d, JSContext *ctx, int64_t id, JSValueConst req)
{
	char *expr = jsrt_get_string(ctx, req, "expression");
	if (!expr) {
		fail(d, ctx, id, "evaluate: no expression");
		return;
	}
	if (!d->paused && d->in_hook) {
		free(expr);
		fail(d, ctx, id, "the app is running: pause it first");
		return;
	}
	JSValue r = eval_in(d, ctx, d->paused ? (int)get_int(ctx, req, "frame", 0) : -1, expr);
	free(expr);
	if (JS_IsException(r)) {
		char *s = exception_text(ctx);
		fail(d, ctx, id, s ? s : "exception");
		free(s);
		return;
	}
	d->busy++;
	JSValue body = describe(d, ctx, NULL, r);
	d->busy--;
	JS_FreeValue(ctx, r);
	reply(d, ctx, id, body);
}

static void set_variable(jsrt_debug *d, JSContext *ctx, int64_t id, JSValueConst req)
{
	char *name = jsrt_get_string(ctx, req, "name"), *value = jsrt_get_string(ctx, req, "value");
	int64_t ref = get_int(ctx, req, "ref", 0);
	int level = (int)get_int(ctx, req, "frame", 0);
	const char *err = NULL;
	JSValue now = JS_UNDEFINED;
	if (!name || !value || !d->paused) {
		err = "setVariable: needs name and value, paused";
	} else {
		JSValue r = eval_in(d, ctx, level, value);
		if (JS_IsException(r)) {
			char *s = exception_text(ctx);
			fail(d, ctx, id, s ? s : "exception");
			free(s);
			free(name);
			free(value);
			return;
		}
		if (ref > 0 && ref <= d->nrefs) {
			if (JS_SetPropertyStr(ctx, d->refs[ref - 1], name, JS_DupValue(ctx, r)) < 0) {
				JS_FreeValue(ctx, JS_GetException(ctx));
				err = "the property cannot be set";
			}
		} else {
			char *scope = jsrt_get_string(ctx, req, "scope");
			int n = 0, done = 0;
			var *v = NULL;
			frame_vars(ctx, level, scope && !strcmp(scope, "closure"), &v, &n);
			for (int i = 0; i < n && !done; i++)
				if (!strcmp(v[i].name, name))
					done = JS_DebugSetVariable(ctx, level, v[i].closure, v[i].index, JS_DupValue(ctx, r)) == 0 ? 1 : -1;
			free_vars(ctx, v, n);
			free(scope);
			if (done != 1)
				err = done ? "a constant, or not set yet" : "no such variable";
		}
		now = r;
	}
	free(name);
	free(value);
	if (err) {
		JS_FreeValue(ctx, now);
		fail(d, ctx, id, err);
		return;
	}
	d->busy++;
	JSValue body = describe(d, ctx, NULL, now);
	d->busy--;
	JS_FreeValue(ctx, now);
	reply(d, ctx, id, body);
}

static void resume(jsrt_debug *d, JSContext *ctx, int mode)
{
	d->mode = mode;
	d->paused = 0;
	if (mode != RUN) {
		JSDebugPoint at = {0};
		d->step_depth = JS_DebugFrameCount(ctx);
		d->step_frame = JS_DebugFramePoint(ctx, 0, &at) == 0 ? at.frame : NULL;
	}
}

/* handle answers one request; returns 1 once the request was "run". */
static int handle(jsrt_debug *d, JSContext *ctx, const char *line)
{
	JSValue req = JS_ParseJSON(ctx, line, strlen(line), "<debugger>");
	if (JS_IsException(req)) {
		JS_FreeValue(ctx, JS_GetException(ctx));
		fail(d, ctx, 0, "a request is one JSON object a line");
		return 0;
	}
	int64_t id = get_int(ctx, req, "id", 0);
	char *cmd = jsrt_get_string(ctx, req, "cmd");
	int ran = 0;
	if (!cmd) {
		fail(d, ctx, id, "no cmd");
	} else if (!strcmp(cmd, "run")) {
		if (get_bool(ctx, req, "stopOnEntry")) {
			d->pause_req = 1;
			d->entry = !d->attached;
		}
		ran = 1;
		reply(d, ctx, id, JS_UNDEFINED);
	} else if (!strcmp(cmd, "setBreakpoints")) {
		set_breakpoints(d, ctx, id, req);
	} else if (!strcmp(cmd, "setFunctionBreakpoints")) {
		set_fn_breakpoints(d, ctx, id, req);
	} else if (!strcmp(cmd, "setExceptions")) {
		d->exc_uncaught = get_bool(ctx, req, "uncaught");
		d->exc_all = get_bool(ctx, req, "all");
		reply(d, ctx, id, JS_UNDEFINED);
	} else if (!strcmp(cmd, "pause")) {
		if (!d->paused)
			d->pause_req = 1;
		reply(d, ctx, id, JS_UNDEFINED);
	} else if (!strcmp(cmd, "evaluate")) {
		evaluate(d, ctx, id, req);
	} else if (!strcmp(cmd, "variables")) {
		variables(d, ctx, id, req);
	} else if (!d->paused) {
		if (!strcmp(cmd, "continue"))
			reply(d, ctx, id, JS_UNDEFINED);
		else
			fail(d, ctx, id, "not paused");
	} else if (!strcmp(cmd, "continue") || !strcmp(cmd, "next") || !strcmp(cmd, "stepIn") || !strcmp(cmd, "stepOut")) {
		resume(d, ctx, !strcmp(cmd, "continue") ? RUN : !strcmp(cmd, "next") ? STEP_OVER : !strcmp(cmd, "stepIn") ? STEP_IN : STEP_OUT);
		reply(d, ctx, id, JS_UNDEFINED);
	} else if (!strcmp(cmd, "stack")) {
		stack(d, ctx, id);
	} else if (!strcmp(cmd, "setVariable")) {
		set_variable(d, ctx, id, req);
	} else {
		fail(d, ctx, id, "unknown cmd");
	}
	free(cmd);
	JS_FreeValue(ctx, req);
	return ran;
}

/* drain answers what has come; with wait, until the app may run on (it is
 * not paused, or a "run" came). */
static int drain(jsrt_debug *d, JSContext *ctx, int wait)
{
	int ran = 0;
	for (;;) {
		char *line;
		while ((line = next_line(d))) {
			ran |= handle(d, ctx, line);
			free(line);
			if (d->closed || (wait && (ran || !d->paused)))
				return ran;
		}
		int got = fill(d, wait);
		if (got < 0 || (got == 0 && !wait))
			return ran;
	}
}

static void detach(jsrt *r)
{
	jsrt_debug *d = r->debug;
	JS_SetDebugHandler(r->rt, NULL, NULL);
	drop_refs(d, r->ctx);
	for (int i = 0; i < d->nbps; i++) {
		free(d->bps[i].condition);
		free(d->bps[i].log);
	}
	d->nbps = 0;
	free_fn_breakpoints(d);
	d->paused = d->pause_req = 0;
	d->mode = RUN;
	if (d->fd >= 0)
		close(d->fd);
	d->fd = -1;
	d->closed = 1;
}

static void stop(jsrt *r, JSContext *ctx, const JSDebugPoint *at, const char *reason, const char *text)
{
	jsrt_debug *d = r->debug;
	d->paused = 1;
	d->pause_req = d->entry = 0;
	d->mode = RUN;
	d->stop_frame = at->frame;
	d->stop_pc = at->pc;
	d->cur_frame = NULL;
	d->busy++;
	JSValue body = JS_NewObject(ctx);
	set_str(ctx, body, "reason", reason);
	if (text)
		set_str(ctx, body, "text", text);
	event(d, ctx, "stopped", body);
	while (d->paused && !d->closed)
		drain(d, ctx, 1);
	drop_refs(d, ctx);
	d->busy--;
	if (d->closed)
		detach(r);
	jsrt_turn_restart();
}

/* condition_holds evaluates a breakpoint's condition in the frame; one that
 * throws does not stop, and says why. */
static int condition_holds(jsrt_debug *d, JSContext *ctx, const char *cond)
{
	d->busy++;
	JSValue v = eval_in(d, ctx, 0, cond);
	d->busy--;
	if (JS_IsException(v)) {
		d->busy++;
		char *s = exception_text(ctx);
		dstr msg = {0};
		dstr_puts(&msg, "breakpoint condition ");
		dstr_puts(&msg, cond);
		dstr_puts(&msg, ": ");
		dstr_puts(&msg, s ? s : "exception");
		dstr_puts(&msg, "\n");
		JSValue body = JS_NewObject(ctx);
		set_str(ctx, body, "category", "stderr");
		set_str(ctx, body, "text", msg.s);
		event(d, ctx, "output", body);
		free(msg.s);
		free(s);
		d->busy--;
		return 0;
	}
	int b = JS_ToBool(ctx, v) > 0;
	JS_FreeValue(ctx, v);
	return b;
}

/* logpoint writes msg with each {expression} evaluated in the frame. */
static void logpoint(jsrt_debug *d, JSContext *ctx, const char *msg)
{
	dstr out = {0};
	dstr_puts(&out, "");
	for (const char *p = msg; *p;) {
		const char *open = strchr(p, '{');
		if (!open) {
			dstr_puts(&out, p);
			break;
		}
		dstr_put(&out, p, (size_t)(open - p));
		int depth = 1;
		const char *q = open + 1;
		for (; *q && depth; q++)
			depth += *q == '{' ? 1 : *q == '}' ? -1 : 0;
		if (depth) {
			dstr_puts(&out, open);
			break;
		}
		char *expr = strndup(open + 1, (size_t)(q - open - 2));
		d->busy++;
		JSValue v = expr ? eval_in(d, ctx, 0, expr) : JS_ThrowOutOfMemory(ctx);
		char *text;
		if (JS_IsException(v)) {
			JSValue e = JS_GetException(ctx);
			const char *c = JS_ToCString(ctx, e);
			text = strdup(c ? c : "?");
			JS_FreeCString(ctx, c);
			JS_FreeValue(ctx, e);
		} else {
			text = jsrt_format(ctx, v);
		}
		JS_FreeValue(ctx, v);
		d->busy--;
		dstr_puts(&out, text ? text : "?");
		free(text);
		free(expr);
		p = q;
	}
	dstr_puts(&out, "\n");
	JSValue body = JS_NewObject(ctx), info = JS_DebugFrameInfo(ctx, 0);
	set_str(ctx, body, "category", "console");
	set_str(ctx, body, "text", out.s);
	JS_SetPropertyStr(ctx, body, "file", JS_GetPropertyStr(ctx, info, "file"));
	JS_SetPropertyStr(ctx, body, "line", JS_GetPropertyStr(ctx, info, "line"));
	JS_SetPropertyStr(ctx, body, "column", JS_GetPropertyStr(ctx, info, "column"));
	JS_FreeValue(ctx, info);
	event(d, ctx, "output", body);
	free(out.s);
}

/* group_hits are the counts a breakpoint shares with the others of its
 * group (the first one's). */
static hits *group_hits(jsrt_debug *d, int i)
{
	breakpoint *b = &d->bps[i];
	for (int k = 0; b->group >= 0 && k < i; k++)
		if (d->bps[k].file == b->file && d->bps[k].group == b->group)
			return &d->bps[k].hits;
	return &b->hits;
}

/* breakpoint_at: whether a breakpoint on the line stops here; logpoints
 * write their lines. */
static int breakpoint_at(jsrt_debug *d, JSContext *ctx, int file, int line)
{
	int stops = 0;
	for (int i = 0; i < d->nbps; i++) {
		breakpoint *b = &d->bps[i];
		if (b->file != file || b->line != line || (b->condition && !condition_holds(d, ctx, b->condition)))
			continue;
		if (!hit(group_hits(d, i)))
			continue;
		if (b->log)
			logpoint(d, ctx, b->log);
		else
			stops = 1;
	}
	return stops;
}

/* fn_breakpoint_at: whether a function breakpoint stops f as it is entered. */
static int fn_breakpoint_at(jsrt_debug *d, JSContext *ctx, fninfo *f)
{
	int stops = 0;
	for (int i = 0; i < d->nfbps; i++) {
		fn_breakpoint *b = &d->fbps[i];
		if (names(b->name, f->name) && (!b->condition || condition_holds(d, ctx, b->condition)) && hit(&b->hits))
			stops = 1;
	}
	return stops;
}

static void on_step(jsrt *r, JSContext *ctx, const JSDebugPoint *at)
{
	jsrt_debug *d = r->debug;
	if (d->stop_frame && (d->stop_frame != at->frame || d->stop_pc != at->pc))
		d->stop_frame = NULL;
	if (d->pause_req) {
		stop(r, ctx, at, d->entry ? "entry" : "pause", NULL);
		return;
	}
	fninfo *f = function_info(d, ctx, at);
	int entered = f && at->pc == 0 && f->has_fbp && fn_breakpoint_at(d, ctx, f);
	if (!f || (d->mode == RUN && !f->has_bp && !entered)) {
		d->cur_frame = NULL;
		return;
	}
	int line = line_at(f, at->pc);
	if (at->frame != d->cur_frame) {
		d->cur_frame = at->frame;
		d->depth = JS_DebugFrameCount(ctx);
		if (d->depth <= TRACKED && d->track[d->depth - 1].frame != at->frame)
			d->track[d->depth - 1] = (tracked){.frame = at->frame, .line = -1};
	}
	int newline = 1;
	if (d->depth <= TRACKED) {
		tracked *t = &d->track[d->depth - 1];
		newline = t->line != line || at->pc <= t->pc;
		t->pc = at->pc;
		t->line = line;
	}
	if (entered) {
		stop(r, ctx, at, "function breakpoint", NULL);
		return;
	}
	switch (d->mode) {
	case STEP_IN:
		if (newline || d->depth < d->step_depth) {
			stop(r, ctx, at, "step", NULL);
			return;
		}
		break;
	case STEP_OVER:
		if (d->depth < d->step_depth || (d->depth == d->step_depth && (at->frame != d->step_frame || newline))) {
			stop(r, ctx, at, "step", NULL);
			return;
		}
		break;
	case STEP_OUT:
		if (d->depth < d->step_depth || (d->depth == d->step_depth && at->frame != d->step_frame)) {
			stop(r, ctx, at, "step", NULL);
			return;
		}
		break;
	}
	if (newline && f->has_bp && breakpoint_at(d, ctx, f->file, line))
		stop(r, ctx, at, "breakpoint", NULL);
}

static void on_debug(JSContext *ctx, JSDebugEvent ev, const JSDebugPoint *at, void *opaque)
{
	jsrt *r = opaque;
	jsrt_debug *d = r->debug;
	if (d->busy || d->closed)
		return;
	d->in_hook++;
	if (++d->ticks >= POLL_EVERY) {
		d->ticks = 0;
		drain(d, ctx, 0);
		if (d->closed) {
			detach(r);
			d->in_hook--;
			return;
		}
	}
	switch (ev) {
	case JS_DEBUG_STEP:
		on_step(r, ctx, at);
		break;
	case JS_DEBUG_STATEMENT:
		if (d->stop_frame != at->frame || d->stop_pc != at->pc)
			stop(r, ctx, at, "debugger", NULL);
		break;
	case JS_DEBUG_EXCEPTION:
		if (d->exc_all || (d->exc_uncaught && !at->caught)) {
			d->busy++;
			char *s = jsrt_format(ctx, at->exception);
			d->busy--;
			stop(r, ctx, at, "exception", s);
			free(s);
		}
		break;
	}
	d->in_hook--;
}

int jsrt_debug_fd(jsrt *r) { return r->debug && !r->debug->closed ? r->debug->fd : -1; }

void jsrt_debug_service(jsrt *r)
{
	jsrt_debug *d = r->debug;
	if (!d || d->closed)
		return;
	drain(d, r->ctx, 0);
	if (d->closed)
		detach(r);
}

void jsrt_debug_output(jsrt *r, const char *category, const char *text)
{
	jsrt_debug *d = r ? r->debug : NULL;
	if (!d || d->closed)
		return;
	JSValue body = JS_NewObject(r->ctx);
	set_str(r->ctx, body, "category", category);
	set_str(r->ctx, body, "text", text);
	event(d, r->ctx, "output", body);
}

/* debugger is r's debugger, made the first time; its helpers are compiled
 * when the first session begins. */
static jsrt_debug *debugger(jsrt *r, const char *main)
{
	if (r->debug)
		return r->debug;
	jsrt_debug *d = calloc(1, sizeof *d);
	if (!d)
		return NULL;
	d->fd = d->listen = -1;
	d->closed = 1;
	d->main = main;
	d->helper = d->evaluator = JS_UNDEFINED;
	r->debug = d;
	return d;
}

/* session: the computer is on fd. Hello, its requests until "run", then
 * the handler sees every frame entered from here on. */
static int session(jsrt *r, int fd, int attached)
{
	jsrt_debug *d = r->debug;
	if (JS_IsUndefined(d->helper)) {
		d->helper = JS_Eval(r->ctx, helper_src, sizeof helper_src - 1, "<debugger>", JS_EVAL_TYPE_GLOBAL);
		d->evaluator = JS_Eval(r->ctx, evaluator_src, sizeof evaluator_src - 1, "<debugger>", JS_EVAL_TYPE_GLOBAL);
		if (JS_IsException(d->helper) || JS_IsException(d->evaluator)) {
			jsrt_report(r->ctx, "debugger");
			d->helper = d->evaluator = JS_UNDEFINED;
			close(fd);
			return -1;
		}
	}
	d->fd = fd;
	d->closed = d->paused = d->pause_req = d->entry = d->ticks = 0;
	d->attached = attached;
	d->mode = RUN;
	d->exc_uncaught = 1;
	d->exc_all = 0;
	d->cur_frame = d->stop_frame = NULL;
	memset(d->track, 0, sizeof d->track);
	d->in.len = 0;
	d->gen++;
	JSValue hello = JS_NewObject(r->ctx);
	char engine[64];
	snprintf(engine, sizeof engine, "QuickJS-ng %s", JS_GetVersion());
	set_str(r->ctx, hello, "engine", engine);
	set_str(r->ctx, hello, "app", r->app_id);
	set_str(r->ctx, hello, "main", d->main);
	JS_SetPropertyStr(r->ctx, hello, "attached", JS_NewBool(r->ctx, attached));
	event(d, r->ctx, "hello", hello);
	while (!d->closed && !drain(d, r->ctx, 1))
		;
	if (d->closed) {
		detach(r);
		return 0;
	}
	JS_SetDebugHandler(r->rt, on_debug, r);
	return 0;
}

int jsrt_debug_start(jsrt *r, int fd, const char *main)
{
	if (!debugger(r, main))
		return -1;
	return session(r, fd, 0);
}

int jsrt_debug_listen(jsrt *r, int fd, const char *main)
{
	jsrt_debug *d = debugger(r, main);
	if (!d)
		return -1;
	d->listen = fd;
	JS_SetDebugPositions(r->rt, 1);
	return 0;
}

int jsrt_debug_listen_fd(jsrt *r) { return r->debug ? r->debug->listen : -1; }

void jsrt_debug_accept(jsrt *r)
{
	jsrt_debug *d = r->debug;
	char byte;
	struct iovec iov = {.iov_base = &byte, .iov_len = 1};
	union {
		char buf[CMSG_SPACE(4 * sizeof(int))];
		struct cmsghdr align;
	} cbuf;
	struct msghdr mh = {.msg_iov = &iov, .msg_iovlen = 1, .msg_control = cbuf.buf, .msg_controllen = sizeof cbuf.buf};
	ssize_t n = recvmsg(d->listen, &mh, MSG_CMSG_CLOEXEC | MSG_DONTWAIT);
	if (n < 0 && (errno == EINTR || errno == EAGAIN))
		return;
	if (n <= 0) {
		close(d->listen);
		d->listen = -1;
		return;
	}
	int fd = -1;
	for (struct cmsghdr *c = CMSG_FIRSTHDR(&mh); c; c = CMSG_NXTHDR(&mh, c)) {
		if (c->cmsg_level != SOL_SOCKET || c->cmsg_type != SCM_RIGHTS)
			continue;
		int got[4];
		size_t k = (c->cmsg_len - CMSG_LEN(0)) / sizeof(int);
		memcpy(got, CMSG_DATA(c), k * sizeof(int));
		for (size_t i = 0; i < k; i++) {
			if (fd < 0)
				fd = got[i];
			else
				close(got[i]);
		}
	}
	if (fd < 0)
		return;
	if (!d->closed) {
		close(fd);
		return;
	}
	session(r, fd, 1);
}
