/*
 * kario_native: native modules, the C, C++ or Rust libraries a TypeScript
 * app brings for what JavaScript does slowly (codecs, image processing,
 * cryptography, a database), imported as `import { f } from "native:<name>"`
 * (22-developer-api.md §9.3, 29-app-development.md §5.3).
 *
 * The interface is the engine's neither: a module includes this header
 * only, links nothing of the runtime and gets every function through the
 * table kario_native_init receives. The same library therefore loads in
 * kario-jsrt (QuickJS-ng) on the phone and in Node.js on the developer
 * machine (kario run, kario test), and survives a change of engine version
 * or of engine. Values cross as numbers, booleans, strings, bytes and JSON;
 * C code never holds a JavaScript object and never calls into JavaScript.
 *
 * A module is lib/<arch>/<name>.so in the app's signed package (arm64 on
 * the phone, x86_64 on a developer machine), named in the manifest's
 * "native"; it runs in the app's process with the app's sandbox and no
 * right of its own: what it needs of the system it asks for through the
 * app's JavaScript and kario.*, like any other app code.
 *
 * Memory: what the accessors return (strings, bytes, JSON) belongs to the
 * runtime and is valid until the function returns; do not free or keep
 * it. What the C code passes to ret_string, ret_json, fail and emit is
 * copied. ret_bytes hands out memory the result ArrayBuffer will own.
 *
 * Threads: the engine has one thread. A plain function runs on it, and
 * while it runs the app answers neither keys nor the system (after 5 s the
 * user is offered to close it): keep it to a few milliseconds. A function
 * exported with KARIO_ASYNC runs on one of the runtime's worker threads
 * (at most 4 per app, more calls wait) on copies of its arguments, and
 * JavaScript gets a Promise that settles on the engine's thread; such a
 * function may run on several threads at once. A module may start threads
 * of its own; they report through emit only. Call the table's other
 * functions only from inside a call, on the thread it runs on.
 *
 * Sealing: kario-jsrt loads every module the manifest declares before the
 * app's code runs (kario_native_init runs then, in the manifest's order),
 * and then no thread may make executable memory: mmap or mprotect with
 * PROT_EXEC, dlopen of a library, exec of a program all fail with EPERM.
 * Libraries a module links are loaded with it; a module whose init leaves
 * memory that could still change into code (writable and executable,
 * shared, anonymous) keeps the app from starting, and one that asks for an
 * executable stack is refused (link with -z noexecstack, the default).
 * Name lookups after sealing work with what libc has built in (glibc 2.34
 * and newer: files and DNS), not with NSS modules it would load.
 *
 * Crashes: a fatal signal in native code writes the module, the function
 * and the native frames (object, offset, build-id) to the app's log, which
 * the crash record keeps; kario symbolize maps them to source lines with
 * the unstripped library (keep it; link with --build-id, most toolchains'
 * default).
 */
#ifndef KARIO_NATIVE_H
#define KARIO_NATIVE_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* A runtime loads a module whose major version is its own and whose minor
 * version is not newer than its own; later minor versions only append to
 * the table. */
#define KARIO_NATIVE_ABI_MAJOR 1
#define KARIO_NATIVE_ABI_MINOR 0
#define KARIO_NATIVE_ABI ((uint32_t)KARIO_NATIVE_ABI_MAJOR << 16 | KARIO_NATIVE_ABI_MINOR)

/* Names of functions, values and events: [A-Za-z_][A-Za-z0-9_]*, at most
 * this long. A module exports at most KARIO_NATIVE_MAX_EXPORTS names; "on"
 * is the runtime's own (below). */
#define KARIO_NATIVE_MAX_NAME 64
#define KARIO_NATIVE_MAX_EXPORTS 256

/* What the module being loaded is; valid as long as the process lives, so
 * a module's threads may keep it for emit. */
typedef struct kario_module kario_module;
/* One call: its arguments and its result. */
typedef struct kario_call kario_call;

typedef enum {
	KARIO_UNDEFINED,
	KARIO_NULL,
	KARIO_BOOL,
	KARIO_NUMBER,
	KARIO_STRING,
	KARIO_BYTES, /* an ArrayBuffer, or a typed array's bytes */
	KARIO_OBJECT /* anything else (objects, arrays): read it as JSON */
} kario_type;

/* export_fn's flags. */
#define KARIO_ASYNC 1u

typedef struct kario_api kario_api;
typedef void (*kario_fn)(const kario_api *api, kario_call *call, void *data);

struct kario_api {
	/* The table's size in the runtime: members beyond it are absent. */
	uint32_t size;
	uint16_t major, minor;
	/* "QuickJS-ng 0.18.0", "Node.js 24.14.0": for a log line, not to
	 * test for. */
	const char *engine;

	/* Only inside kario_native_init. 0, or -1 for a bad or repeated name. */
	int (*export_fn)(kario_module *m, const char *name, kario_fn fn, void *data, uint32_t flags);
	int (*export_number)(kario_module *m, const char *name, double value);
	int (*export_string)(kario_module *m, const char *name, const char *utf8);

	/* The arguments. Missing ones are KARIO_UNDEFINED. */
	int (*argc)(kario_call *c);
	kario_type (*type)(kario_call *c, int i);
	/* NaN unless a number. */
	double (*number)(kario_call *c, int i);
	/* JavaScript's truthiness. */
	int (*boolean)(kario_call *c, int i);
	/* UTF-8 with a NUL after len bytes; NULL unless a string. */
	const char *(*string)(kario_call *c, int i, size_t *len);
	/* NULL unless bytes. A plain function gets the JavaScript memory
	 * itself, so it may change the bytes in place (the caller sees it);
	 * an asynchronous one a copy (JavaScript runs on meanwhile). */
	void *(*bytes)(kario_call *c, int i, size_t *len);
	/* Any argument as JSON text; NULL when JSON cannot hold it. */
	const char *(*json)(kario_call *c, int i, size_t *len);

	/* The result; the last one set counts. Without one: undefined. */
	void (*ret_number)(kario_call *c, double value);
	void (*ret_bool)(kario_call *c, int value);
	void (*ret_string)(kario_call *c, const char *utf8, size_t len);
	/* An ArrayBuffer of len bytes, zeroed, for the function to fill
	 * before it returns; NULL when memory is short. */
	void *(*ret_bytes)(kario_call *c, size_t len);
	/* Parsed once the function returned; text that is not JSON throws. */
	void (*ret_json)(kario_call *c, const char *json, size_t len);
	/* The call throws an Error with this message (an asynchronous
	 * one rejects), whatever else was set. */
	void (*fail)(kario_call *c, const char *message);

	/* Any thread, once kario_native_init has returned: the handlers
	 * JavaScript gave the module's on(event, handler) get the value of
	 * json (NULL: undefined) on the engine's thread, in order. -1 when
	 * the queue is full (JavaScript is not keeping up) or json is not
	 * JSON. */
	int (*emit)(kario_module *m, const char *event, const char *json, size_t len);
};

#if defined(__GNUC__)
#define KARIO_NATIVE_EXPORT __attribute__((visibility("default")))
#else
#define KARIO_NATIVE_EXPORT
#endif

#ifdef __cplusplus
#define KARIO_NATIVE_EXTERN extern "C"
#else
#define KARIO_NATIVE_EXTERN
#endif

/*
 * KARIO_NATIVE_MODULE(init) defines what the runtime looks up in the
 * library: the interface version it was built for and its entry point,
 * which calls init(api, m) once, on the engine's thread, before
 * JavaScript sees the module. init exports the names (export_*) and
 * returns 0; anything else refuses the module (the import throws).
 *
 *	static int init(const kario_api *api, kario_module *m)
 *	{
 *		return api->export_fn(m, "blur", blur, NULL, 0);
 *	}
 *	KARIO_NATIVE_MODULE(init)
 */
#define KARIO_NATIVE_MODULE(init)                                                                   \
	KARIO_NATIVE_EXTERN KARIO_NATIVE_EXPORT const uint32_t kario_native_abi = KARIO_NATIVE_ABI;     \
	KARIO_NATIVE_EXTERN KARIO_NATIVE_EXPORT int kario_native_init(const kario_api *api, kario_module *m) \
	{                                                                                               \
		return (init)(api, m);                                                                  \
	}

#ifdef __cplusplus
}
#endif

#endif
