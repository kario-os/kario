/* The storage file's writer (kario.storage, kario.c): a thread of its own
 * writes the file, so a save costs the app's loop only its JSON however
 * long the flash takes to sync, as Android's SharedPreferences.apply().
 * Saves made while a write is under way coalesce into the next one, which
 * carries the newest content (the file is the whole store, so a write
 * makes every one before it moot); one thread writes a new file, syncs it,
 * renames it over the old and syncs the directory, so the file is always a
 * whole version and never an older one after a newer, power cut or not.
 * flush() and the state handed over on "background" wait for the writes
 * queued before them; the exit and SIGTERM wait for all, SIGTERM then
 * ending the process with exit status 0 (jsrt_catch_term). */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/eventfd.h>
#include <unistd.h>

#include "jsrt.h"

/* The thread only opens, writes, syncs and renames: a small stack keeps it
 * clear of a tight RLIMIT_DATA (the mini-program host's). */
#define WRITER_STACK (256 << 10)

typedef struct {
	long seq;
	JSValue resolve, reject;
} flush_t;

struct jsrt_writer {
	pthread_mutex_t mu;
	pthread_cond_t idle; /* broadcast after each write */
	char *dir, *path, *tmp;
	int thread;  /* 0: no thread could start, saves write at once */
	int work_fd; /* the thread's wake-up (an eventfd), -1 without it */
	/* Under mu. Saves are numbered; a write carries the newest save's. */
	char *next;           /* content no write has taken yet, or NULL */
	atomic_long queued;   /* the newest save (SIGTERM's handler reads it) */
	long done, ok;        /* the last write finished, the last that succeeded */
	int err;              /* errno of the last write that failed */
	atomic_int busy;      /* a write is queued or under way */
	atomic_int term;      /* SIGTERM came meanwhile */
	atomic_long term_seq; /* the newest save then */
	/* The engine's thread only. */
	flush_t *flushes; /* in the order of their saves */
	size_t nflushes;
	char *held;    /* a message to send once the write of held_seq is done */
	long held_seq;
};

static _Atomic(jsrt_writer *) term_writer;

/* sync_dir makes a rename in dir outlast a power cut. 0, or the errno;
 * EINVAL is a filesystem that does not sync directories. */
static int sync_dir(const char *dir)
{
	int fd = open(dir, O_RDONLY | O_DIRECTORY | O_CLOEXEC);
	if (fd < 0)
		return errno;
	int err = fsync(fd) < 0 && errno != EINVAL ? errno : 0;
	close(fd);
	return err;
}

/* write_file replaces the file whole (a new file, then a rename): a
 * process killed meanwhile leaves the old one, never half of the new.
 * 0, or the errno. */
static int write_file(jsrt_writer *w, const char *text)
{
	size_t n = strlen(text);
	int fd = open(w->tmp, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
	int err = fd < 0 ? errno : 0;
	for (size_t off = 0; !err && off < n;) {
		ssize_t k = write(fd, text + off, n - off);
		if (k < 0 && errno == EINTR)
			continue;
		if (k <= 0)
			err = k < 0 ? errno : EIO;
		else
			off += (size_t)k;
	}
	if (fd >= 0) {
		if (!err && fsync(fd) < 0)
			err = errno;
		if (close(fd) < 0 && !err)
			err = errno;
	}
	if (!err && rename(w->tmp, w->path) < 0)
		err = errno;
	if (err) {
		unlink(w->tmp);
		return err;
	}
	return sync_dir(w->dir);
}

static void finished(jsrt_writer *w, long seq, int err)
{
	w->done = seq;
	if (err)
		w->err = err;
	else
		w->ok = seq;
	if (err) {
		fprintf(stderr, "kario-jsrt: storage not saved: %s\n", strerror(err));
		fflush(stderr);
	}
}

/* on_term: SIGTERM while nothing is to be written ends the process at
 * once; otherwise the writer ends it once the saves made so far are on
 * disk (the main thread may be in a turn that does not end). With an exit,
 * not the signal again: the first process of a pid namespace (an app on
 * the device) is not delivered a signal it raises with the default action. */
static void on_term(int sig)
{
	jsrt_writer *w = atomic_load(&term_writer);
	if (w && atomic_load(&w->busy)) {
		int saved = errno;
		atomic_store(&w->term_seq, atomic_load(&w->queued));
		atomic_store(&w->term, 1);
		uint64_t one = 1;
		ssize_t n = write(w->work_fd, &one, sizeof one);
		(void)n;
		errno = saved;
		return;
	}
	_exit(0);
}

/* The first process of a pid namespace is not delivered a signal it has no
 * handler for either, so the handler is there from the start, not from the
 * first save. */
void jsrt_catch_term(void)
{
	struct sigaction sa = {.sa_handler = on_term, .sa_flags = SA_RESTART};
	sigemptyset(&sa.sa_mask);
	sigaction(SIGTERM, &sa, NULL);
}

static void *writer_main(void *opaque)
{
	jsrt *r = opaque;
	jsrt_writer *w = r->writer;
	sigset_t term;
	sigemptyset(&term);
	sigaddset(&term, SIGTERM);
	pthread_sigmask(SIG_BLOCK, &term, NULL);
	for (;;) {
		pthread_mutex_lock(&w->mu);
		char *text = w->next;
		long seq = atomic_load(&w->queued);
		w->next = NULL;
		if (!text)
			atomic_store(&w->busy, 0);
		pthread_mutex_unlock(&w->mu);
		if (!text) {
			if (atomic_load(&w->term))
				_exit(0);
			uint64_t n;
			while (read(w->work_fd, &n, sizeof n) < 0 && errno == EINTR)
				;
			continue;
		}
		int err = write_file(w, text);
		free(text);
		pthread_mutex_lock(&w->mu);
		finished(w, seq, err);
		pthread_cond_broadcast(&w->idle);
		pthread_mutex_unlock(&w->mu);
		if (atomic_load(&w->term) && seq >= atomic_load(&w->term_seq))
			_exit(0);
		uint64_t one = 1;
		ssize_t k = write(r->writer_fd, &one, sizeof one);
		(void)k;
	}
	return NULL;
}

static int start_thread(jsrt *r)
{
	jsrt_writer *w = r->writer;
	w->work_fd = eventfd(0, EFD_CLOEXEC);
	r->writer_fd = eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
	pthread_attr_t attr;
	pthread_attr_init(&attr);
	pthread_attr_setstacksize(&attr, WRITER_STACK);
	pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
	pthread_t t;
	int err = w->work_fd < 0 || r->writer_fd < 0 ? errno : pthread_create(&t, &attr, writer_main, r);
	pthread_attr_destroy(&attr);
	if (err) {
		if (w->work_fd >= 0)
			close(w->work_fd);
		if (r->writer_fd >= 0)
			close(r->writer_fd);
		w->work_fd = r->writer_fd = -1;
		fprintf(stderr, "kario-jsrt: storage: no writer thread (%s): saves wait for the disk\n", strerror(err));
		fflush(stderr);
		return -1;
	}
	atomic_store(&term_writer, w);
	return 0;
}

static jsrt_writer *writer(jsrt *r)
{
	if (r->writer)
		return r->writer;
	jsrt_writer *w = calloc(1, sizeof *w);
	if (!w)
		return NULL;
	dstr dir = {0}, path = {0}, tmp = {0};
	dstr_puts(&dir, r->data_dir);
	dstr_puts(&path, dir.s);
	dstr_puts(&path, "/" JSRT_STORAGE_FILE);
	dstr_puts(&tmp, path.s);
	dstr_puts(&tmp, ".tmp");
	w->dir = dir.s;
	w->path = path.s;
	w->tmp = tmp.s;
	pthread_mutex_init(&w->mu, NULL);
	pthread_cond_init(&w->idle, NULL);
	r->writer = w;
	w->thread = start_thread(r) == 0;
	return w;
}

void jsrt_store_save(jsrt *r, char *json)
{
	jsrt_writer *w = writer(r);
	if (!w) {
		fprintf(stderr, "kario-jsrt: storage not saved: %s\n", strerror(ENOMEM));
		free(json);
		return;
	}
	if (!w->thread) {
		long seq = atomic_fetch_add(&w->queued, 1) + 1;
		finished(w, seq, write_file(w, json));
		free(json);
		return;
	}
	pthread_mutex_lock(&w->mu);
	free(w->next);
	w->next = json;
	atomic_fetch_add(&w->queued, 1);
	atomic_store(&w->busy, 1);
	pthread_mutex_unlock(&w->mu);
	uint64_t one = 1;
	ssize_t n = write(w->work_fd, &one, sizeof one);
	(void)n;
}

/* outcome: whether the write of save seq is done (it, or a later one), and
 * then 0 when what it carried is on disk, else the errno. */
static int outcome(jsrt_writer *w, long seq, int *err)
{
	pthread_mutex_lock(&w->mu);
	int done = w->done >= seq;
	*err = w->ok >= seq ? 0 : w->err;
	pthread_mutex_unlock(&w->mu);
	return done;
}

static void settle(JSContext *ctx, JSValue resolve, JSValue reject, int err)
{
	JSValue arg = JS_UNDEFINED;
	if (err) {
		char msg[128];
		snprintf(msg, sizeof msg, "storage: %s", strerror(err));
		arg = jsrt_error(ctx, msg, NULL);
	}
	JS_FreeValue(ctx, JS_Call(ctx, err ? reject : resolve, JS_UNDEFINED, 1, (JSValueConst *)&arg));
	JS_FreeValue(ctx, arg);
	JS_FreeValue(ctx, resolve);
	JS_FreeValue(ctx, reject);
}

JSValue jsrt_store_flush(jsrt *r)
{
	JSContext *ctx = r->ctx;
	JSValue funcs[2];
	JSValue promise = JS_NewPromiseCapability(ctx, funcs);
	if (JS_IsException(promise))
		return promise;
	jsrt_writer *w = r->writer;
	long seq = w ? atomic_load(&w->queued) : 0;
	int err = 0;
	if (!w || outcome(w, seq, &err)) {
		settle(ctx, funcs[0], funcs[1], err);
		return promise;
	}
	flush_t *f = realloc(w->flushes, (w->nflushes + 1) * sizeof *f);
	if (!f) {
		settle(ctx, funcs[0], funcs[1], ENOMEM);
		return promise;
	}
	w->flushes = f;
	w->flushes[w->nflushes++] = (flush_t){seq, funcs[0], funcs[1]};
	return promise;
}

void jsrt_send_after_writes(jsrt *r, char *line)
{
	jsrt_writer *w = r->writer;
	int err;
	if (w && !outcome(w, atomic_load(&w->queued), &err)) {
		free(w->held);
		w->held = line;
		w->held_seq = atomic_load(&w->queued);
		return;
	}
	jsrt_send(r, line);
	free(line);
}

void jsrt_store_drain(jsrt *r)
{
	jsrt_writer *w = r->writer;
	if (!w)
		return;
	uint64_t count;
	ssize_t got = read(r->writer_fd, &count, sizeof count);
	(void)got;
	size_t i = 0;
	int err;
	for (; i < w->nflushes && outcome(w, w->flushes[i].seq, &err); i++)
		settle(r->ctx, w->flushes[i].resolve, w->flushes[i].reject, err);
	memmove(w->flushes, w->flushes + i, (w->nflushes - i) * sizeof *w->flushes);
	w->nflushes -= i;
	if (w->held && outcome(w, w->held_seq, &err)) {
		char *line = w->held;
		w->held = NULL;
		jsrt_send(r, line);
		free(line);
	}
	jsrt_run_jobs(r);
}

void jsrt_store_wait(jsrt *r)
{
	jsrt_writer *w = r->writer;
	if (!w)
		return;
	pthread_mutex_lock(&w->mu);
	while (w->done < atomic_load(&w->queued))
		pthread_cond_wait(&w->idle, &w->mu);
	pthread_mutex_unlock(&w->mu);
}
