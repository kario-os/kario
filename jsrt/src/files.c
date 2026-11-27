/* Files a capability's reply hands over (22-developer-api.md §7) as
 * JavaScript sees them: each one a KarioFile among the result's "files"
 * (media.pick's photo, file.pick's file), or for an audio stream
 * (audio.stream, audio.record) its shared ring mapped as the result's
 * "ring", an AudioRing, as the Go SDK's GoAudio maps it (audio/ring.go). The
 * runtime owns every descriptor: a KarioFile closes its own on close() or
 * when it is collected, and what the app is not given is closed at once. */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

#include "jsrt.h"

/* A read() hands over at most this much; readAll() a file of at most this
 * much (the Files area's own limit), so a reply cannot make the app hold
 * more than it asked for. */
#define FILE_DEFAULT_READ (64 << 10)
#define FILE_MAX_READ (1 << 20)
#define FILE_MAX_ALL (16 << 20)
/* Files from replies open at once: past it a reply's files are closed and
 * its call fails, before the app's descriptor table (1024) runs out. */
#define FILES_MAX_OPEN 64

/* The ring's layout, as audio/ring.go writes it. */
#define RING_HEADER 64
#define RING_MAGIC 0x474e5250u
#define RING_RATE 48000
#define RING_MAX_SECS 10
#define RING_EOF 1u

static JSClassID file_class, ring_class;
static int open_files;

typedef struct {
	int fd;
} kfile;

typedef struct {
	uint8_t *mem;
	size_t size;
	uint64_t cap;
} kring;

static void file_close(kfile *f)
{
	if (f->fd >= 0) {
		close(f->fd);
		f->fd = -1;
		open_files--;
	}
}

static void file_finalizer(JSRuntime *rt, JSValueConst val)
{
	kfile *f = JS_GetOpaque(val, file_class);
	if (f) {
		file_close(f);
		free(f);
	}
}

static void ring_unmap(kring *g)
{
	if (g->mem) {
		munmap(g->mem, g->size);
		g->mem = NULL;
	}
}

static void ring_finalizer(JSRuntime *rt, JSValueConst val)
{
	kring *g = JS_GetOpaque(val, ring_class);
	if (g) {
		ring_unmap(g);
		free(g);
	}
}

static void *bytes_realloc(JSRuntime *rt, void *opaque, void *ptr, size_t size)
{
	if (!size) {
		free(ptr);
		return NULL;
	}
	return realloc(ptr, size);
}

/* bytes makes buf (malloc'd, cap bytes, n of them read) a Uint8Array. */
static JSValue bytes(JSContext *ctx, uint8_t *buf, size_t n)
{
	if (n == 0) {
		free(buf);
		return JS_NewUint8ArrayCopy(ctx, (const uint8_t *)"", 0);
	}
	uint8_t *fit = realloc(buf, n);
	if (fit)
		buf = fit;
	JSValue v = JS_NewUint8Array(ctx, buf, n, bytes_realloc, NULL, false);
	if (JS_IsException(v))
		free(buf);
	return v;
}

static kfile *file_of(JSContext *ctx, JSValueConst this_val, const char *what)
{
	kfile *f = JS_GetOpaque2(ctx, this_val, file_class);
	if (f && f->fd < 0) {
		JS_ThrowTypeError(ctx, "KarioFile.%s: the file is closed", what);
		return NULL;
	}
	return f;
}

static JSValue file_read(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	kfile *f = file_of(ctx, this_val, "read");
	if (!f)
		return JS_EXCEPTION;
	double max = FILE_DEFAULT_READ;
	if (argc > 0 && !JS_IsUndefined(argv[0])) {
		if (JS_ToFloat64(ctx, &max, argv[0]))
			return JS_EXCEPTION;
		if (!(max >= 1 && max <= FILE_MAX_READ))
			return JS_ThrowRangeError(ctx, "KarioFile.read: from 1 to %d bytes at a time", FILE_MAX_READ);
	}
	size_t n = (size_t)max;
	uint8_t *buf = malloc(n);
	if (!buf)
		return JS_ThrowOutOfMemory(ctx);
	ssize_t got;
	do
		got = read(f->fd, buf, n);
	while (got < 0 && errno == EINTR);
	if (got < 0) {
		int e = errno;
		free(buf);
		return JS_ThrowPlainError(ctx, "KarioFile.read: %s", strerror(e));
	}
	return bytes(ctx, buf, (size_t)got);
}

static JSValue file_read_all(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	kfile *f = file_of(ctx, this_val, "readAll");
	if (!f)
		return JS_EXCEPTION;
	struct stat st;
	if (fstat(f->fd, &st) < 0)
		return JS_ThrowPlainError(ctx, "KarioFile.readAll: %s", strerror(errno));
	if (st.st_size > FILE_MAX_ALL)
		return JS_ThrowRangeError(ctx, "KarioFile.readAll: the file is over %d MB; read it in parts (read())", FILE_MAX_ALL >> 20);
	size_t cap = (size_t)st.st_size + 1, n = 0;
	uint8_t *buf = malloc(cap);
	if (!buf)
		return JS_ThrowOutOfMemory(ctx);
	/* The size can change while it is read (a file still being written):
	 * the limit holds for what is read, not for what fstat said. */
	for (;;) {
		if (n == cap) {
			if (cap > FILE_MAX_ALL) {
				free(buf);
				return JS_ThrowRangeError(ctx, "KarioFile.readAll: the file is over %d MB; read it in parts (read())", FILE_MAX_ALL >> 20);
			}
			size_t more = cap * 2 > (size_t)FILE_MAX_ALL + 1 ? (size_t)FILE_MAX_ALL + 1 : cap * 2;
			uint8_t *p = realloc(buf, more);
			if (!p) {
				free(buf);
				return JS_ThrowOutOfMemory(ctx);
			}
			buf = p;
			cap = more;
		}
		ssize_t got = pread(f->fd, buf + n, cap - n, (off_t)n);
		if (got < 0 && errno == EINTR)
			continue;
		if (got < 0) {
			int e = errno;
			free(buf);
			return JS_ThrowPlainError(ctx, "KarioFile.readAll: %s", strerror(e));
		}
		if (got == 0)
			break;
		n += (size_t)got;
	}
	return bytes(ctx, buf, n);
}

static JSValue file_close_js(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	kfile *f = JS_GetOpaque2(ctx, this_val, file_class);
	if (!f)
		return JS_EXCEPTION;
	file_close(f);
	return JS_UNDEFINED;
}

static JSValue file_size(JSContext *ctx, JSValueConst this_val)
{
	kfile *f = file_of(ctx, this_val, "size");
	if (!f)
		return JS_EXCEPTION;
	struct stat st;
	if (fstat(f->fd, &st) < 0)
		return JS_ThrowPlainError(ctx, "KarioFile.size: %s", strerror(errno));
	return JS_NewFloat64(ctx, (double)st.st_size);
}

static JSValue file_closed(JSContext *ctx, JSValueConst this_val)
{
	kfile *f = JS_GetOpaque2(ctx, this_val, file_class);
	if (!f)
		return JS_EXCEPTION;
	return JS_NewBool(ctx, f->fd < 0);
}

static const JSCFunctionListEntry file_proto[] = {
	JS_CFUNC_DEF("read", 1, file_read),
	JS_CFUNC_DEF("readAll", 0, file_read_all),
	JS_CFUNC_DEF("close", 0, file_close_js),
	JS_CGETSET_DEF("size", file_size, NULL),
	JS_CGETSET_DEF("closed", file_closed, NULL),
	JS_PROP_STRING_DEF("[Symbol.toStringTag]", "KarioFile", JS_PROP_CONFIGURABLE),
};

/* ---- the audio ring ---- */

static uint64_t *ring_wpos(kring *g) { return (uint64_t *)(g->mem + 16); }
static uint64_t *ring_rpos(kring *g) { return (uint64_t *)(g->mem + 24); }
static uint32_t *ring_flags(kring *g) { return (uint32_t *)(g->mem + 32); }

static kring *ring_of(JSContext *ctx, JSValueConst this_val, const char *what)
{
	kring *g = JS_GetOpaque2(ctx, this_val, ring_class);
	if (g && !g->mem) {
		JS_ThrowTypeError(ctx, "AudioRing.%s: the ring is closed", what);
		return NULL;
	}
	return g;
}

/* samples is the Int16Array argument's memory and length. */
static int16_t *samples(JSContext *ctx, JSValueConst v, size_t *n, const char *what)
{
	if (JS_GetTypedArrayType(v) != JS_TYPED_ARRAY_INT16) {
		JS_ThrowTypeError(ctx, "AudioRing.%s: the samples are an Int16Array", what);
		return NULL;
	}
	size_t off, len, per;
	JSValue ab = JS_GetTypedArrayBuffer(ctx, v, &off, &len, &per);
	if (JS_IsException(ab))
		return NULL;
	size_t all;
	uint8_t *base = JS_GetArrayBuffer(ctx, &all, ab);
	JS_FreeValue(ctx, ab);
	if (!base || off + len > all) {
		JS_ThrowTypeError(ctx, "AudioRing.%s: a detached array", what);
		return NULL;
	}
	*n = len / 2;
	return (int16_t *)(base + off);
}

/* Neither side trusts the other's positions: indices are taken modulo the
 * capacity, so a peer garbles its own audio at worst. */
static JSValue ring_write(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	kring *g = ring_of(ctx, this_val, "write");
	size_t len;
	int16_t *p = g ? samples(ctx, argc ? argv[0] : JS_UNDEFINED, &len, "write") : NULL;
	if (!p)
		return JS_EXCEPTION;
	uint64_t w = __atomic_load_n(ring_wpos(g), __ATOMIC_ACQUIRE), rd = __atomic_load_n(ring_rpos(g), __ATOMIC_ACQUIRE);
	uint64_t used = w - rd < g->cap ? w - rd : g->cap, n = g->cap - used;
	if (len < n)
		n = len;
	int16_t *data = (int16_t *)(g->mem + RING_HEADER);
	for (uint64_t i = 0; i < n; i++)
		data[(w + i) % g->cap] = p[i];
	__atomic_store_n(ring_wpos(g), w + n, __ATOMIC_RELEASE);
	return JS_NewFloat64(ctx, (double)n);
}

static JSValue ring_read(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	kring *g = ring_of(ctx, this_val, "read");
	size_t len;
	int16_t *p = g ? samples(ctx, argc ? argv[0] : JS_UNDEFINED, &len, "read") : NULL;
	if (!p)
		return JS_EXCEPTION;
	uint64_t w = __atomic_load_n(ring_wpos(g), __ATOMIC_ACQUIRE), rd = __atomic_load_n(ring_rpos(g), __ATOMIC_ACQUIRE);
	if (w - rd > g->cap)
		rd = w - g->cap;
	uint64_t n = w - rd;
	if (len < n)
		n = len;
	const int16_t *data = (const int16_t *)(g->mem + RING_HEADER);
	for (uint64_t i = 0; i < n; i++)
		p[i] = data[(rd + i) % g->cap];
	__atomic_store_n(ring_rpos(g), rd + n, __ATOMIC_RELEASE);
	return JS_NewFloat64(ctx, (double)n);
}

static JSValue ring_finish(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	kring *g = ring_of(ctx, this_val, "finish");
	if (!g)
		return JS_EXCEPTION;
	__atomic_store_n(ring_flags(g), RING_EOF, __ATOMIC_RELEASE);
	return JS_UNDEFINED;
}

static JSValue ring_close(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	kring *g = JS_GetOpaque2(ctx, this_val, ring_class);
	if (!g)
		return JS_EXCEPTION;
	ring_unmap(g);
	return JS_UNDEFINED;
}

enum { RING_CAPACITY, RING_BUFFERED, RING_FINISHED, RING_CLOSED, RING_RATE_PROP };

static JSValue ring_get(JSContext *ctx, JSValueConst this_val, int magic)
{
	if (magic == RING_CLOSED) {
		kring *g = JS_GetOpaque2(ctx, this_val, ring_class);
		return g ? JS_NewBool(ctx, !g->mem) : JS_EXCEPTION;
	}
	if (magic == RING_RATE_PROP)
		return JS_NewInt32(ctx, RING_RATE);
	kring *g = ring_of(ctx, this_val, magic == RING_CAPACITY ? "capacity" : magic == RING_BUFFERED ? "buffered" : "finished");
	if (!g)
		return JS_EXCEPTION;
	if (magic == RING_CAPACITY)
		return JS_NewFloat64(ctx, (double)g->cap);
	if (magic == RING_FINISHED)
		return JS_NewBool(ctx, __atomic_load_n(ring_flags(g), __ATOMIC_ACQUIRE) & RING_EOF);
	uint64_t w = __atomic_load_n(ring_wpos(g), __ATOMIC_ACQUIRE), rd = __atomic_load_n(ring_rpos(g), __ATOMIC_ACQUIRE);
	return JS_NewFloat64(ctx, (double)(w - rd < g->cap ? w - rd : g->cap));
}

static const JSCFunctionListEntry ring_proto[] = {
	JS_CFUNC_DEF("write", 1, ring_write),
	JS_CFUNC_DEF("read", 1, ring_read),
	JS_CFUNC_DEF("finish", 0, ring_finish),
	JS_CFUNC_DEF("close", 0, ring_close),
	JS_CGETSET_MAGIC_DEF("capacity", ring_get, NULL, RING_CAPACITY),
	JS_CGETSET_MAGIC_DEF("buffered", ring_get, NULL, RING_BUFFERED),
	JS_CGETSET_MAGIC_DEF("finished", ring_get, NULL, RING_FINISHED),
	JS_CGETSET_MAGIC_DEF("closed", ring_get, NULL, RING_CLOSED),
	JS_CGETSET_MAGIC_DEF("rate", ring_get, NULL, RING_RATE_PROP),
	JS_PROP_STRING_DEF("[Symbol.toStringTag]", "AudioRing", JS_PROP_CONFIGURABLE),
};

/* map_ring maps a ring received with a reply, refusing one that could be
 * resized under the mapping or is not laid out as a ring (audio.MapRing). */
static kring *map_ring(int fd, const char **why)
{
	int seals = fcntl(fd, F_GET_SEALS);
	if (seals < 0 || !(seals & F_SEAL_SHRINK)) {
		*why = "the audio ring is not sealed against shrinking";
		return NULL;
	}
	struct stat st;
	if (fstat(fd, &st) < 0 || st.st_size < RING_HEADER + 2 || st.st_size > RING_HEADER + 2 * RING_RATE * RING_MAX_SECS) {
		*why = "the audio ring has a size no ring has";
		return NULL;
	}
	size_t size = (size_t)st.st_size;
	uint8_t *mem = mmap(NULL, size, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
	if (mem == MAP_FAILED) {
		*why = "the audio ring could not be mapped";
		return NULL;
	}
	uint32_t magic, version, frames;
	memcpy(&magic, mem, 4);
	memcpy(&version, mem + 4, 4);
	memcpy(&frames, mem + 12, 4);
	if (magic != RING_MAGIC || version != 1 || RING_HEADER + 2 * (uint64_t)frames != size || frames == 0) {
		munmap(mem, size);
		*why = "not an audio ring";
		return NULL;
	}
	kring *g = calloc(1, sizeof *g);
	if (!g) {
		munmap(mem, size);
		*why = "out of memory";
		return NULL;
	}
	g->mem = mem;
	g->size = size;
	g->cap = frames;
	return g;
}

void jsrt_files_init(JSContext *ctx)
{
	JSRuntime *rt = JS_GetRuntime(ctx);
	JSClassDef fdef = {.class_name = "KarioFile", .finalizer = file_finalizer};
	JSClassDef rdef = {.class_name = "AudioRing", .finalizer = ring_finalizer};
	JS_NewClassID(rt, &file_class);
	JS_NewClassID(rt, &ring_class);
	JS_NewClass(rt, file_class, &fdef);
	JS_NewClass(rt, ring_class, &rdef);
	JSValue fp = JS_NewObject(ctx), rp = JS_NewObject(ctx);
	JS_SetPropertyFunctionList(ctx, fp, file_proto, sizeof file_proto / sizeof file_proto[0]);
	JS_SetPropertyFunctionList(ctx, rp, ring_proto, sizeof ring_proto / sizeof ring_proto[0]);
	JS_SetClassProto(ctx, file_class, fp);
	JS_SetClassProto(ctx, ring_class, rp);
}

void jsrt_close_fds(int *fds, int n)
{
	for (int i = 0; i < n; i++)
		if (fds[i] >= 0)
			close(fds[i]);
}

static int audio_cap(const char *cap)
{
	return cap && (!strcmp(cap, "audio.stream") || !strcmp(cap, "audio.record"));
}

const char *jsrt_reply_files(jsrt *r, const char *cap, JSValueConst data, int *fds, int n)
{
	JSContext *ctx = r->ctx;
	const char *why = NULL;
	if (!JS_IsObject(data)) {
		fprintf(stderr, "kario-jsrt: %s: %d files with a result that is not an object, closed\n", cap, n);
		jsrt_close_fds(fds, n);
		return NULL;
	}
	if (audio_cap(cap)) {
		kring *g = n == 1 ? map_ring(fds[0], &why) : NULL;
		if (n != 1)
			why = "the audio stream came without its ring";
		jsrt_close_fds(fds, n);
		if (!g)
			return why;
		JSValue v = JS_NewObjectClass(ctx, ring_class);
		if (JS_IsException(v)) {
			JS_FreeValue(ctx, JS_GetException(ctx));
			munmap(g->mem, g->size);
			free(g);
			return "out of memory";
		}
		JS_SetOpaque(v, g);
		JS_DefinePropertyValueStr(ctx, data, "ring", v, JS_PROP_CONFIGURABLE | JS_PROP_WRITABLE);
		return NULL;
	}
	if (open_files + n > FILES_MAX_OPEN) {
		jsrt_close_fds(fds, n);
		return "too many files open from replies (at most 64): close() the ones done with";
	}
	JSValue arr = JS_NewArray(ctx);
	uint32_t k = 0;
	for (int i = 0; i < n; i++) {
		struct stat st;
		if (fstat(fds[i], &st) < 0 || !S_ISREG(st.st_mode)) {
			fprintf(stderr, "kario-jsrt: %s: a file that is not a regular file, closed\n", cap);
			close(fds[i]);
			continue;
		}
		kfile *f = malloc(sizeof *f);
		JSValue v = f ? JS_NewObjectClass(ctx, file_class) : JS_EXCEPTION;
		if (JS_IsException(v)) {
			JS_FreeValue(ctx, JS_GetException(ctx));
			free(f);
			close(fds[i]);
			continue;
		}
		f->fd = fds[i];
		open_files++;
		JS_SetOpaque(v, f);
		JS_SetPropertyUint32(ctx, arr, k++, v);
	}
	JS_DefinePropertyValueStr(ctx, data, "files", arr, JS_PROP_CONFIGURABLE | JS_PROP_WRITABLE);
	return NULL;
}
