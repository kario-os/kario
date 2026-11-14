/* kario-jsrt: the JavaScript runtime of TypeScript apps (29-app-development.md). */
#ifndef KARIO_JSRT_H
#define KARIO_JSRT_H

#include <stddef.h>
#include <stdint.h>

#include "phoneui.h"
#include "quickjs.h"

/* What app code subscribes to with on(name, handler). */
enum {
	EV_START,
	EV_ACTION,
	EV_KEY,
	EV_INPUT,
	EV_POPPED,
	EV_FOREGROUND,
	EV_BACKGROUND,
	EV_SAVE,
	EV_EVENT,
	EV_NOTIF,
	EV_RESYNC,
	EV_COUNT
};

extern const char *const jsrt_event_names[EV_COUNT];

typedef struct {
	char *s;
	size_t len, cap;
} dstr;

void dstr_put(dstr *d, const char *s, size_t n);
void dstr_puts(dstr *d, const char *s);

typedef struct {
	long id;
	char *cap;
	JSValue resolve, reject;
} jsrt_call;

typedef struct {
	int32_t id;
	int64_t due;   /* CLOCK_MONOTONIC, ms */
	int64_t every; /* setInterval's period, -1 for setTimeout */
	JSValue fn;
	int argc;
	JSValue *argv;
} jsrt_timer;

/* A page as the Shell holds it, for a Shell that takes over (resync). */
typedef struct {
	char *id;
	char *json;
} jsrt_page;

typedef struct jsrt {
	JSRuntime *rt;
	JSContext *ctx;
	phoneui *ui;
	int fd;
	const char *app_id, *data_dir, *lang;
	char *app_dir;
	JSModuleDef *kario;
	JSValue main; /* the main module's evaluation (a promise) until it settles */
	JSValue on[EV_COUNT];
	jsrt_call *calls;
	size_t ncalls;
	long next_req;
	jsrt_timer *timers;
	size_t ntimers;
	int32_t next_timer;
	jsrt_page *stack;
	size_t nstack;
	JSValue catalog; /* merged translations, JS_UNDEFINED until the first tr() */
	JSValue store;   /* storage: key -> JSON text, JS_UNDEFINED until first used */
	int closed;
} jsrt;

static inline jsrt *jsrt_of(JSContext *ctx) { return JS_GetContextOpaque(ctx); }

/* kario.c */
JSModuleDef *jsrt_kario_module(JSContext *ctx);
void jsrt_add_globals(JSContext *ctx);
int64_t jsrt_now_ms(void);
int64_t jsrt_next_due(jsrt *r);
void jsrt_fire_timers(jsrt *r);
void jsrt_run_jobs(jsrt *r);
void jsrt_report(JSContext *ctx, const char *what);
char *jsrt_format(JSContext *ctx, JSValueConst v);
char *jsrt_stringify(JSContext *ctx, JSValueConst v);
char *jsrt_get_string(JSContext *ctx, JSValueConst obj, const char *name);
int jsrt_send(jsrt *r, const char *json);
JSValue jsrt_error(JSContext *ctx, const char *message, const char *cap);

/* dispatch.c */
void jsrt_dispatch(jsrt *r, const char *line);

/* lang.c */
const char *jsrt_canonical(const char *tag);
int jsrt_chain(const char *lang, const char **out, int max);

#endif
