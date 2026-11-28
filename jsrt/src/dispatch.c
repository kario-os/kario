/* Messages from the Shell (22-developer-api.md §2) to the app's handlers. */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "jsrt.h"

/* The largest state a Shell keeps for a tombstone (ui.MaxSavedState). */
#define MAX_SAVED_STATE (64 << 10)

static JSValue emit(jsrt *r, int ev, int argc, JSValueConst *argv)
{
	if (JS_IsUndefined(r->on[ev]))
		return JS_UNDEFINED;
	JSValue fn = JS_DupValue(r->ctx, r->on[ev]);
	JSValue ret = JS_Call(r->ctx, fn, JS_UNDEFINED, argc, argv);
	JS_FreeValue(r->ctx, fn);
	if (JS_IsException(ret)) {
		char what[64];
		snprintf(what, sizeof what, "uncaught exception in on(\"%s\")", jsrt_event_names[ev]);
		jsrt_report(r->ctx, what);
		return JS_UNDEFINED;
	}
	return ret;
}

static void emit_free(jsrt *r, int ev, int argc, JSValueConst *argv)
{
	JS_FreeValue(r->ctx, emit(r, ev, argc, argv));
}

static JSValue prop_or(JSContext *ctx, JSValueConst obj, const char *name, JSValue dflt)
{
	JSValue v = JS_GetPropertyStr(ctx, obj, name);
	if (JS_IsUndefined(v) || JS_IsNull(v)) {
		JS_FreeValue(ctx, v);
		return dflt;
	}
	JS_FreeValue(ctx, dflt);
	return v;
}

static void action(jsrt *r, JSValueConst msg)
{
	JSContext *ctx = r->ctx;
	JSValue e = JS_NewObject(ctx);
	JS_SetPropertyStr(ctx, e, "screen", prop_or(ctx, msg, "screen_id", JS_NewString(ctx, "")));
	JS_SetPropertyStr(ctx, e, "node", prop_or(ctx, msg, "node", JS_NewString(ctx, "")));
	JS_SetPropertyStr(ctx, e, "action", prop_or(ctx, msg, "action", JS_NewString(ctx, "")));
	JS_SetPropertyStr(ctx, e, "value", prop_or(ctx, msg, "value", JS_NewString(ctx, "")));
	JS_SetPropertyStr(ctx, e, "fields", prop_or(ctx, msg, "fields", JS_NewObject(ctx)));
	char *id = jsrt_get_string(ctx, msg, "action");
	int ev = EV_ACTION;
	/* A focused canvas's keys and a live input's edits have handlers of
	 * their own; without one they are actions like any other. */
	if (id && !strcmp(id, "key") && !JS_IsUndefined(r->on[EV_KEY]))
		ev = EV_KEY;
	else if (id && !strcmp(id, "changed") && !JS_IsUndefined(r->on[EV_INPUT]))
		ev = EV_INPUT;
	if (ev == EV_KEY) {
		JSValue args[2] = {JS_GetPropertyStr(ctx, e, "value"), e};
		emit_free(r, ev, 2, args);
		JS_FreeValue(ctx, args[0]);
	} else {
		emit_free(r, ev, 1, (JSValueConst *)&e);
	}
	free(id);
	JS_FreeValue(ctx, e);
}

static void lifecycle(jsrt *r, JSValueConst msg)
{
	JSContext *ctx = r->ctx;
	char *state = jsrt_get_string(ctx, msg, "state");
	if (state && !strcmp(state, "background")) {
		JSValue saved = emit(r, EV_SAVE, 0, NULL);
		dstr d = {0};
		if (!JS_IsUndefined(saved)) {
			char *json = jsrt_stringify(ctx, saved);
			if (!json) {
				jsrt_report(ctx, "on(\"save\") returned what JSON cannot hold");
			} else if (strlen(json) > MAX_SAVED_STATE) {
				fprintf(stderr, "kario-jsrt: saved state not sent: %zu bytes (at most %d)\n", strlen(json), MAX_SAVED_STATE);
			} else {
				dstr_puts(&d, "{\"t\":\"state\",\"data\":");
				dstr_puts(&d, json);
				dstr_puts(&d, "}");
			}
			free(json);
		}
		JS_FreeValue(ctx, saved);
		emit_free(r, EV_BACKGROUND, 0, NULL);
		/* The Shell may end the app as soon as it has the state: what the
		 * handlers stored, their jobs included, is on disk first. */
		jsrt_run_jobs(r);
		if (d.s)
			jsrt_send_after_writes(r, d.s);
	} else if (state && !strcmp(state, "foreground")) {
		emit_free(r, EV_FOREGROUND, 0, NULL);
	}
	free(state);
}

/* own_result hands the reply to a call of the runtime's own to its
 * callback, the files with it. */
static void own_result(jsrt *r, jsrt_call *c, JSValueConst msg, int ok, int *fds, int nfds)
{
	JSContext *ctx = r->ctx;
	JSValue data = ok ? JS_GetPropertyStr(ctx, msg, "data") : JS_UNDEFINED;
	char *err = ok ? NULL : jsrt_get_string(ctx, msg, "err");
	if (c->done)
		c->done(r, c->arg, ok ? NULL : err ? err : "failed", data, fds, nfds);
	else
		jsrt_close_fds(fds, nfds);
	free(err);
	JS_FreeValue(ctx, data);
	free(c->cap);
}

static void result(jsrt *r, JSValueConst msg, int *fds, int nfds)
{
	JSContext *ctx = r->ctx;
	int64_t id = 0;
	JSValue req = JS_GetPropertyStr(ctx, msg, "req");
	JS_ToInt64(ctx, &id, req);
	JS_FreeValue(ctx, req);
	size_t i;
	for (i = 0; i < r->ncalls && r->calls[i].id != id; i++)
		;
	if (i == r->ncalls) {
		jsrt_close_fds(fds, nfds);
		return;
	}
	jsrt_call c = r->calls[i];
	memmove(&r->calls[i], &r->calls[i + 1], (r->ncalls - i - 1) * sizeof *r->calls);
	r->ncalls--;
	JSValue okv = JS_GetPropertyStr(ctx, msg, "ok");
	int ok = JS_ToBool(ctx, okv) > 0;
	JS_FreeValue(ctx, okv);
	if (JS_IsUndefined(c.resolve)) {
		own_result(r, &c, msg, ok, fds, nfds);
		return;
	}
	JSValue arg, ret;
	const char *refused = NULL;
	if (ok) {
		arg = JS_GetPropertyStr(ctx, msg, "data");
		if (nfds && (refused = jsrt_reply_files(r, c.cap, arg, fds, nfds))) {
			JS_FreeValue(ctx, arg);
			ok = 0;
		}
	} else {
		jsrt_close_fds(fds, nfds);
	}
	if (ok) {
		ret = JS_Call(ctx, c.resolve, JS_UNDEFINED, 1, (JSValueConst *)&arg);
	} else {
		char *err = refused ? NULL : jsrt_get_string(ctx, msg, "err");
		arg = jsrt_error(ctx, refused ? refused : err ? err : "failed", c.cap);
		free(err);
		ret = JS_Call(ctx, c.reject, JS_UNDEFINED, 1, (JSValueConst *)&arg);
	}
	JS_FreeValue(ctx, ret);
	JS_FreeValue(ctx, arg);
	JS_FreeValue(ctx, c.resolve);
	JS_FreeValue(ctx, c.reject);
	free(c.cap);
}

/* resync answers a Shell that took over the connection: calls the previous
 * one never answered fail, and the pages it held are sent again. */
static void resync(jsrt *r)
{
	JSContext *ctx = r->ctx;
	jsrt_call *calls = r->calls;
	size_t n = r->ncalls;
	r->calls = NULL;
	r->ncalls = 0;
	for (size_t i = 0; i < n; i++) {
		if (JS_IsUndefined(calls[i].resolve)) {
			if (calls[i].done)
				calls[i].done(r, calls[i].arg, "the shell restarted", JS_UNDEFINED, NULL, 0);
			free(calls[i].cap);
			continue;
		}
		JSValue e = jsrt_error(ctx, "the shell restarted", calls[i].cap);
		JS_FreeValue(ctx, JS_Call(ctx, calls[i].reject, JS_UNDEFINED, 1, (JSValueConst *)&e));
		JS_FreeValue(ctx, e);
		JS_FreeValue(ctx, calls[i].resolve);
		JS_FreeValue(ctx, calls[i].reject);
		free(calls[i].cap);
	}
	free(calls);
	for (size_t i = 0; i < r->nstack; i++) {
		dstr d = {0};
		dstr_puts(&d, i ? "{\"t\":\"push\",\"screen\":" : "{\"t\":\"reset\",\"screen\":");
		dstr_puts(&d, r->stack[i].json);
		dstr_puts(&d, "}");
		jsrt_send(r, d.s);
		free(d.s);
	}
	emit_free(r, EV_RESYNC, 0, NULL);
}

static void popped(jsrt *r, JSValueConst msg)
{
	char *id = jsrt_get_string(r->ctx, msg, "screen_id");
	if (id && r->nstack > 1 && !strcmp(r->stack[r->nstack - 1].id, id)) {
		r->nstack--;
		free(r->stack[r->nstack].id);
		free(r->stack[r->nstack].json);
	}
	JSValue arg = JS_NewString(r->ctx, id ? id : "");
	emit_free(r, EV_POPPED, 1, (JSValueConst *)&arg);
	JS_FreeValue(r->ctx, arg);
	free(id);
}

void jsrt_dispatch(jsrt *r, const char *line, int *fds, int nfds)
{
	JSContext *ctx = r->ctx;
	JSValue msg = JS_ParseJSON(ctx, line, strlen(line), "<shell>");
	if (JS_IsException(msg)) {
		jsrt_close_fds(fds, nfds);
		jsrt_report(ctx, "a message from the shell");
		return;
	}
	char *t = jsrt_get_string(ctx, msg, "t");
	if (t && !strcmp(t, "res")) {
		result(r, msg, fds, nfds);
		nfds = 0;
	}
	jsrt_close_fds(fds, nfds);
	if (!t) {
		JS_FreeValue(ctx, msg);
		return;
	}
	if (!strcmp(t, "start")) {
		JSValue args[2] = {prop_or(ctx, msg, "value", JS_NewString(ctx, "")), JS_GetPropertyStr(ctx, msg, "data")};
		emit_free(r, EV_START, 2, args);
		JS_FreeValue(ctx, args[0]);
		JS_FreeValue(ctx, args[1]);
	} else if (!strcmp(t, "action")) {
		action(r, msg);
	} else if (!strcmp(t, "lifecycle")) {
		lifecycle(r, msg);
	} else if (!strcmp(t, "popped")) {
		popped(r, msg);
	} else if (!strcmp(t, "event")) {
		char *ev = jsrt_get_string(ctx, msg, "event"), *v = jsrt_get_string(ctx, msg, "value"), *end = NULL;
		if (ev && v && !strcmp(ev, "time.clock")) {
			long long ms = strtoll(v, &end, 10);
			if (*v && !*end)
				r->clock_ms = ms;
		}
#ifdef KARIO_JSRT_NET
		if (ev && v && !strcmp(ev, "keepalive"))
			jsrt_ws_keepalive_event(r, v);
#endif
		free(ev);
		free(v);
		JSValue args[2] = {prop_or(ctx, msg, "event", JS_NewString(ctx, "")), prop_or(ctx, msg, "value", JS_NewString(ctx, ""))};
		emit_free(r, EV_EVENT, 2, args);
		JS_FreeValue(ctx, args[0]);
		JS_FreeValue(ctx, args[1]);
	} else if (!strcmp(t, "notif_action")) {
		JSValue n = JS_NewObject(ctx);
		JS_SetPropertyStr(ctx, n, "id", prop_or(ctx, msg, "value", JS_NewString(ctx, "")));
		JS_SetPropertyStr(ctx, n, "action", prop_or(ctx, msg, "action", JS_NewString(ctx, "")));
		JS_SetPropertyStr(ctx, n, "text", prop_or(ctx, msg, "text", JS_NewString(ctx, "")));
		emit_free(r, EV_NOTIF, 1, (JSValueConst *)&n);
		JS_FreeValue(ctx, n);
	} else if (!strcmp(t, "resync")) {
		resync(r);
	}
	free(t);
	JS_FreeValue(ctx, msg);
}
