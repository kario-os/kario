/* kario-jsrt: the JavaScript runtime of TypeScript apps (29-app-development.md). */
#ifndef KARIO_JSRT_H
#define KARIO_JSRT_H

#include <stddef.h>
#include <stdint.h>

#include "phoneui.h"
#include "quickjs.h"

/* A build with AddressSanitizer or ThreadSanitizer (make jsrt-asan,
 * jsrt-tsan; -DJSRT_TEARDOWN=1 for another) tears the runtime down as the
 * process ends, so that LeakSanitizer finds what was lost rather than what
 * was still held, and ThreadSanitizer checks that it frees nothing a worker
 * still touches: SIGTERM then ends the loop instead of the process, and
 * every exit frees what it can (main.c). Other builds end at once, the
 * kernel taking the memory back: freeing it would only delay the exit and
 * wait on the workers. */
#if !defined(JSRT_TEARDOWN) && (defined(__SANITIZE_ADDRESS__) || defined(__SANITIZE_THREAD__))
#define JSRT_TEARDOWN 1
#endif

/* How long the teardown waits for fetch, WebSocket and native workers to
 * let go of what it would free; what one still holds then is kept. */
#define JSRT_TEARDOWN_WAIT_MS 200

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

struct jsrt;
/* The reply to a call of the runtime's own (jsrt_call_own): err is NULL on
 * success; the files that came with it are the callback's. */
typedef void jsrt_done(struct jsrt *r, long arg, const char *err, JSValueConst data, int *fds, int nfds);

typedef struct {
	long id;
	char *cap;
	JSValue resolve, reject; /* JS_UNDEFINED for the runtime's own call */
	jsrt_done *done;         /* the runtime's own call's (NULL: its reply is dropped) */
	long arg;
} jsrt_call;

typedef struct {
	int32_t id;
	int64_t due;   /* CLOCK_MONOTONIC, ms */
	int64_t every; /* setInterval's period, -1 for setTimeout */
	JSValue fn;
	int argc;
	JSValue *argv;
} jsrt_timer;

/* Imports of native modules (native.c): "native:<name>". */
#define JSRT_NATIVE_SCHEME "native:"

/* The lines a crash of an app with native modules leaves in its log, as
 * jsrt.ParseBacktrace and the Shell's crash records read them: the head,
 * one line a frame, the end. */
#define JSRT_BACKTRACE_HEAD "kario-jsrt: crashed"
#define JSRT_BACKTRACE_FRAME "kario-jsrt: #"
#define JSRT_BACKTRACE_END "kario-jsrt: end of backtrace"

/* kario.storage's file in the app's data directory (jsrt.StorageFile). */
#define JSRT_STORAGE_FILE "kario-storage.json"

typedef struct jsrt_native jsrt_native;
typedef struct jsrt_net jsrt_net;
typedef struct jsrt_writer jsrt_writer;
typedef struct jsrt_debug jsrt_debug;

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
	jsrt_writer *writer; /* its file's writer (store.c), NULL until the first save */
	int writer_fd;       /* the writer's wake-up of the loop (an eventfd), -1 until then */
	jsrt_native *native; /* NULL until the first native import */
	int native_fd;       /* its workers' wake-up (an eventfd), -1 until then */
	jsrt_net *net;       /* NULL until the first fetch or WebSocket */
	int net_fd;          /* their wake-up (an eventfd), -1 until then */
	jsrt_debug *debug;   /* NULL unless started with KARIO_JSRT_DEBUG_FD or _ATTACH_FD */
	int64_t clock_ms;    /* the system clock's offset Date adds (PHONE_CLOCK_OFFSET_MS, time.clock) */
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
/* jsrt_call_own sends a request of the runtime's own (args: JSON) that no
 * JavaScript sees: its id, or -1 when it could not go. */
long jsrt_call_own(jsrt *r, const char *cap, const char *args, jsrt_done *done, long arg);
JSValue jsrt_error(JSContext *ctx, const char *message, const char *cap);
/* The manifest's property name (JS_NULL without a manifest or the name). */
JSValue jsrt_manifest_prop(jsrt *r, const char *name);
/* The teardown (JSRT_TEARDOWN): handlers, calls, timers, pages,
 * translations and storage let go. */
void jsrt_kario_free(jsrt *r);

/* web.c: AbortController, AbortSignal, TextEncoder, TextDecoder. */
void jsrt_web_init(JSContext *ctx);
/* jsrt_utf8 is v as UTF-8 (malloc'd; a lone surrogate becomes U+FFFD). */
char *jsrt_utf8(JSContext *ctx, JSValueConst v, size_t *len);
/* jsrt_decode_utf8 is p as text: a byte order mark dropped, what is not
 * UTF-8 replaced by U+FFFD. */
JSValue jsrt_decode_utf8(JSContext *ctx, const uint8_t *p, size_t n);
/* jsrt_bytes_of is the memory of an ArrayBuffer or a view on one, or NULL. */
const uint8_t *jsrt_bytes_of(JSContext *ctx, JSValueConst v, size_t *len);
/* addEventListener (add) and removeEventListener on a list of listeners. */
int jsrt_listen(JSContext *ctx, JSValue *list, int argc, JSValueConst *argv, int add);
/* jsrt_fire calls target's on<type> and then type's listeners with event. */
void jsrt_fire(JSContext *ctx, JSValueConst target, JSValue *list, const char *type, JSValueConst event);
JSValue jsrt_abort_error(JSContext *ctx);
/* jsrt_signal_state: -1 when v is not an AbortSignal, else whether it is
 * aborted (with its reason in *reason, when asked). */
int jsrt_signal_state(JSContext *ctx, JSValueConst v, JSValue *reason);
int jsrt_signal_listen(JSContext *ctx, JSValueConst sig, JSValueConst fn, int add);
/* jsrt_class is a constructor with its prototype's (and its own) members. */
JSValue jsrt_class(JSContext *ctx, const char *name, JSCFunction *ctor, int length, JSClassID id, const JSCFunctionListEntry *proto, int nproto,
		   const JSCFunctionListEntry *statics, int nstatics);

/* net.c (with KARIO_JSRT_NET): fetch for apps with the "net" permission;
 * ws.c: WebSocket. */
JSValue jsrt_fetch(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv);
JSValue jsrt_websocket_ctor(JSContext *ctx);
/* The module's fetch, Blob, File, FormData, Headers, Request and Response
 * (sdk/ts/node/web.js over jsrt_fetch), compiled at their first use. */
void jsrt_web_exports(JSContext *ctx, JSModuleDef *m);
/* Before sealing (jsrt_native_preload): TLS and the resolver set up, for
 * an app that may use the network. */
void jsrt_net_init(jsrt *r);
void jsrt_net_drain(jsrt *r);
/* The teardown: stop cancels every request and WebSocket, lets go of their
 * values and waits until deadline for the workers to finish; free, once
 * the runtime is gone, frees what no worker can touch any more. */
void jsrt_net_stop(jsrt *r, int64_t deadline);
void jsrt_net_free(jsrt *r);

/* The Shell's keepalive event (closed, lost: it no longer holds the
 * keepalive WebSocket's socket), before the app's handler has it. */
void jsrt_ws_keepalive_event(jsrt *r, const char *value);

/* dispatch.c: a message, with the files that came with it (the runtime's
 * from then on). */
void jsrt_dispatch(jsrt *r, const char *line, int *fds, int nfds);

/* store.c: the storage file written on a thread of its own. jsrt_store_save
 * takes json (the store's whole JSON, malloc'd) to write after what is
 * queued, coalescing with saves not yet taken. flush() is the promise that
 * the saves made so far are on disk (rejected with the write's error).
 * jsrt_send_after_writes sends line (malloc'd) once they are, at once when
 * nothing is queued; jsrt_store_drain settles both when the writer wakes
 * the loop; jsrt_store_wait blocks until every write is done.
 * jsrt_catch_term makes SIGTERM end the process with exit status 0 once
 * every write is done, on any thread and in any turn; with JSRT_TEARDOWN it
 * makes jsrt_term_fd readable instead, for the loop to end (else -1).
 * jsrt_store_free (the teardown, after jsrt_store_wait) stops the writer. */
void jsrt_catch_term(void);
int jsrt_term_fd(void);
void jsrt_store_save(jsrt *r, char *json);
JSValue jsrt_store_flush(jsrt *r);
void jsrt_send_after_writes(jsrt *r, char *line);
void jsrt_store_drain(jsrt *r);
void jsrt_store_wait(jsrt *r);
void jsrt_store_free(jsrt *r);

/* files.c */
void jsrt_files_init(JSContext *ctx);
void jsrt_close_fds(int *fds, int n);
/* jsrt_reply_files puts the files of cap's successful reply on its data
 * (non-enumerable "files", or "ring" for an audio stream); the reason the
 * call fails instead, or NULL. The files are closed or the objects' own. */
const char *jsrt_reply_files(jsrt *r, const char *cap, JSValueConst data, int *fds, int n);

/* native.c */
JSModuleDef *jsrt_native_module(jsrt *r, const char *spec);
void jsrt_native_drain(jsrt *r);
/* Loads the modules the manifest declares, then seals; -1 (said on
 * stderr) when the app must not run. Apps that declare none are left as
 * they are. */
int jsrt_native_preload(jsrt *r);
/* The teardown: the jobs' and handlers' values let go, the jobs under way
 * waited for until deadline. A module's threads may run on, so the modules
 * stay loaded and their state stays. */
void jsrt_native_free(jsrt *r, int64_t deadline);

/* debug.c: the debugger on fd (KARIO_JSRT_DEBUG_FD), for an app installed
 * in developer mode. start says hello and waits for the debugger's "run"
 * (its breakpoints set first); main is the module's name in positions. */
int jsrt_debug_start(jsrt *r, int fd, const char *main);
/* listen: the Shell may hand a debugger's socket over fd while the app
 * runs (KARIO_JSRT_DEBUG_ATTACH_FD); code is compiled with every
 * statement's position from then on. listen_fd is that socket (-1: none),
 * accept takes what came on it, while no JavaScript runs. */
int jsrt_debug_listen(jsrt *r, int fd, const char *main);
int jsrt_debug_listen_fd(jsrt *r);
void jsrt_debug_accept(jsrt *r);
/* The socket while the debugger is there, else -1; service answers what
 * came on it while no JavaScript runs. */
int jsrt_debug_fd(jsrt *r);
void jsrt_debug_service(jsrt *r);
/* Console lines and error reports go to the debugger too. */
void jsrt_debug_output(jsrt *r, const char *category, const char *text);
/* The teardown: the debugger detached and freed. */
void jsrt_debug_free(jsrt *r);
/* main.c: a turn's time budget starts again (after a pause). */
void jsrt_turn_restart(void);

/* lang.c */
const char *jsrt_canonical(const char *tag);
int jsrt_chain(const char *lang, const char **out, int max);

#endif
