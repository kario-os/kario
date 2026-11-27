/* WebSocket for apps whose manifest asks for "net" (22-developer-api.md
 * §9.4): wss:// (and ws:// to the device's own loopback), the browser's
 * interface. libcurl connects (CONNECT_ONLY: the same TLS, CAs and
 * per-app rules as fetch, whatever version of libcurl the image has), then
 * a worker thread speaks the opening handshake and the framing itself
 * (wsframe.c) over curl_easy_send and curl_easy_recv, and hands the loop
 * what came on the eventfd fetch uses.
 *
 * A keepalive WebSocket (21-push.md §8.3) is the app's one connection
 * that the system watches while it is frozen: the runtime asks the Shell
 * (net.keepalive) to dial, one call at a time, and libcurl speaks TLS on
 * the socket that comes back in place of one of its own. */
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/eventfd.h>
#include <sys/random.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <unistd.h>

#include "net.h"
#include "wsframe.h"

#define WS_MAX_OPEN 4
#define WS_MAX_SEND (4u << 20)
#define WS_MAX_BUFFERED (16u << 20)
/* What came and waits for the app: past it the worker stops reading the
 * socket until the app catches up. */
#define WS_MAX_INBOX (8u << 20)
#define WS_HANDSHAKE_MS 30000
#define WS_CLOSING_MS 5000
/* The heartbeat intervals net.keepalive takes (ui.KeepaliveMinSec, MaxSec). */
#define KEEPALIVE_MIN_S 60
#define KEEPALIVE_MAX_S 3600

enum { WS_CONNECTING, WS_OPEN, WS_CLOSING, WS_CLOSED };
enum { IN_OPEN, IN_TEXT, IN_BINARY, IN_ERROR, IN_CLOSE };

typedef struct ws_msg ws_msg;
struct ws_msg {
	ws_msg *next;
	int kind, code, clean;
	size_t len;
	char data[];
};

struct ws_conn {
	ws_conn *next;  /* the done list (net->mu) */
	ws_conn *lnext; /* the live list (loop) */
	jsrt_net *net;
	int wake, efd, posted;
	long id;
	/* The request, written before the worker starts. */
	char *connect_url, *host, *path, *protocols;
	struct curl_slist *headers;
	/* Under net->mu. */
	ws_msg *in, *in_tail;
	size_t in_bytes;
	dstr out;
	size_t out_taken, buffered;
	int closing; /* a close frame is queued */
	int cancel, finished;
	char *protocol;
	/* The sockets libcurl opened, for a cancel to shut while it connects. */
	curl_socket_t socks[4];
	int nsocks;
	/* Why a cancel is told (net->mu): aborted, replaced. */
	const char *dropped;
	/* A keepalive WebSocket's heartbeat interval (0: another), host and
	 * port; the socket the Shell dialled, libcurl's once it asks (net->mu;
	 * -1: none), and its address for libcurl's resolver, written before
	 * the worker starts. */
	int keepalive, keep_port;
	char *keep_host;
	int keep_fd;
	struct curl_slist *keep_resolve;
	/* The loop's own. */
	int state, worker, alive;
	ws_conn *keep_next; /* net->keep_wait */
	int keep_has;       /* it got its socket */
	JSValue obj, url, listeners, signal, on_abort;
};

static JSClassID ws_class;

static void wake_worker(ws_conn *c)
{
	uint64_t one = 1;
	ssize_t w = write(c->efd, &one, sizeof one);
	(void)w;
}

/* cancel drops the connection: the worker wakes, and libcurl, if it is
 * still connecting, finds its sockets shut. */
static void cancel(ws_conn *c, const char *why)
{
	pthread_mutex_lock(&c->net->mu);
	c->cancel = 1;
	c->dropped = why;
	for (int i = 0; i < c->nsocks; i++)
		shutdown(c->socks[i], SHUT_RDWR);
	pthread_mutex_unlock(&c->net->mu);
	wake_worker(c);
}

/* libcurl opens and closes its sockets here, under net->mu, so a cancel
 * never shuts a descriptor that has been closed and reused. */
static curl_socket_t open_sock(void *arg, curlsocktype purpose, struct curl_sockaddr *a)
{
	ws_conn *c = arg;
	pthread_mutex_lock(&c->net->mu);
	curl_socket_t s = CURL_SOCKET_BAD;
	if (!c->cancel && c->keep_fd >= 0) {
		s = c->keep_fd;
		c->keep_fd = -1;
	} else if (!c->cancel && !c->keepalive) {
		/* A keepalive WebSocket has the Shell's socket and no other. */
		s = socket(a->family, a->socktype | SOCK_CLOEXEC, a->protocol);
	}
	if (s != CURL_SOCKET_BAD && c->nsocks < (int)(sizeof c->socks / sizeof c->socks[0]))
		c->socks[c->nsocks++] = s;
	pthread_mutex_unlock(&c->net->mu);
	return s;
}

static int close_sock(void *arg, curl_socket_t s)
{
	ws_conn *c = arg;
	pthread_mutex_lock(&c->net->mu);
	for (int i = 0; i < c->nsocks; i++)
		if (c->socks[i] == s) {
			c->socks[i] = c->socks[--c->nsocks];
			break;
		}
	int r = close(s);
	pthread_mutex_unlock(&c->net->mu);
	return r;
}

static int sock_connected(void *arg, curl_socket_t s, curlsocktype purpose) { return CURL_SOCKOPT_ALREADY_CONNECTED; }

/* While the name is looked up there is no socket yet: libcurl asks here. */
static int connect_progress(void *arg, curl_off_t dt, curl_off_t dn, curl_off_t ut, curl_off_t un)
{
	ws_conn *c = arg;
	pthread_mutex_lock(&c->net->mu);
	int stop = c->cancel;
	pthread_mutex_unlock(&c->net->mu);
	return stop;
}

/* ---- the worker ---- */

/* inbox gives the loop an event (under net->mu). */
static void inbox(ws_conn *c, int kind, const void *data, size_t len, int code, int clean)
{
	ws_msg *m = malloc(sizeof *m + len + 1);
	if (!m)
		return;
	m->next = NULL;
	m->kind = kind;
	m->code = code;
	m->clean = clean;
	m->len = len;
	if (len)
		memcpy(m->data, data, len);
	m->data[len] = 0;
	if (c->in_tail)
		c->in_tail->next = m;
	else
		c->in = m;
	c->in_tail = m;
	c->in_bytes += len;
	if (!c->posted) {
		c->posted = 1;
		c->next = c->net->ws_done;
		c->net->ws_done = c;
	}
	jsrt_net_wake(c->wake);
}

static void mask(uint8_t m[4])
{
	if (getrandom(m, 4, 0) != 4)
		memset(m, 0x5a, 4);
}

/* queue appends a frame to what goes out (under net->mu). */
static int queue(ws_conn *c, int op, const uint8_t *p, size_t n)
{
	uint8_t *f = malloc(n + 14), m[4];
	if (!f)
		return -1;
	mask(m);
	size_t len = ws_frame(f, op, p, n, m);
	dstr_put(&c->out, (const char *)f, len);
	free(f);
	return 0;
}

static void queue_close(ws_conn *c, int code, const char *why)
{
	uint8_t p[125];
	size_t n = 0;
	if (code) {
		p[0] = (uint8_t)(code >> 8);
		p[1] = (uint8_t)code;
		n = 2;
		size_t w = why ? strlen(why) : 0;
		if (w > 123)
			w = 123;
		if (w)
			memcpy(p + 2, why, w);
		n += w;
	}
	queue(c, WS_OP_CLOSE, p, n);
	c->closing = 1;
}

static int64_t left(int64_t deadline)
{
	int64_t d = deadline - jsrt_now_ms();
	return d < 0 ? 0 : d;
}

/* wait_sock waits until the socket can be read (or written) or the
 * worker is woken; 0 on time out. */
static int wait_sock(ws_conn *c, curl_socket_t s, short events, int64_t deadline)
{
	struct pollfd p[2] = {{.fd = s, .events = events}, {.fd = c->efd, .events = POLLIN}};
	int n = poll(p, 2, deadline < 0 ? -1 : (int)left(deadline));
	if (n > 0 && p[1].revents) {
		uint64_t v;
		ssize_t r = read(c->efd, &v, sizeof v);
		(void)r;
	}
	return n;
}

static int send_all(ws_conn *c, CURL *h, curl_socket_t s, const char *p, size_t n, int64_t deadline)
{
	while (n) {
		size_t sent = 0;
		CURLcode e = curl_easy_send(h, p, n, &sent);
		if (e == CURLE_AGAIN) {
			if (wait_sock(c, s, POLLOUT, deadline) == 0)
				return -1;
			continue;
		}
		if (e != CURLE_OK)
			return -1;
		p += sent;
		n -= sent;
	}
	return 0;
}

/* handshake speaks the opening handshake: NULL, or why it failed. head
 * gets the bytes after the response's headers (the first frames). */
static const char *handshake(ws_conn *c, CURL *h, curl_socket_t s, dstr *got, size_t *body, int64_t deadline)
{
	uint8_t raw[16];
	char key[25], want[29];
	if (getrandom(raw, sizeof raw, 0) != sizeof raw)
		return "no random key";
	ws_base64(raw, sizeof raw, key);
	ws_accept(key, want);
	dstr req = {0};
	dstr_puts(&req, "GET ");
	dstr_puts(&req, c->path);
	dstr_puts(&req, " HTTP/1.1\r\nHost: ");
	dstr_puts(&req, c->host);
	dstr_puts(&req, "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: ");
	dstr_puts(&req, key);
	dstr_puts(&req, "\r\nUser-Agent: kario-jsrt\r\n");
	if (c->protocols) {
		dstr_puts(&req, "Sec-WebSocket-Protocol: ");
		dstr_puts(&req, c->protocols);
		dstr_puts(&req, "\r\n");
	}
	for (struct curl_slist *l = c->headers; l; l = l->next) {
		dstr_puts(&req, l->data);
		dstr_puts(&req, "\r\n");
	}
	dstr_puts(&req, "\r\n");
	int err = send_all(c, h, s, req.s, req.len, deadline);
	free(req.s);
	if (err)
		return "the opening handshake could not be sent";
	char buf[4096];
	for (;;) {
		char *end = got->s ? strstr(got->s, "\r\n\r\n") : NULL;
		if (end) {
			*body = (size_t)(end + 4 - got->s);
			break;
		}
		if (got->len > FETCH_MAX_HEAD)
			return "the server's answer to the handshake is over 64 KB";
		size_t n = 0;
		CURLcode e = curl_easy_recv(h, buf, sizeof buf - 1, &n);
		if (e == CURLE_AGAIN) {
			if (wait_sock(c, s, POLLIN, deadline) == 0)
				return "the opening handshake timed out";
			pthread_mutex_lock(&c->net->mu);
			int cancel = c->cancel;
			pthread_mutex_unlock(&c->net->mu);
			if (cancel)
				return "aborted";
			continue;
		}
		if (e != CURLE_OK || n == 0)
			return "the server closed the connection during the handshake";
		dstr_put(got, buf, n);
	}
	/* Bytes past the headers are frames; the headers end with \r\n\r\n. */
	char *head = strndup(got->s, *body - 2);
	if (!head)
		return "out of memory";
	char *protocol = NULL;
	const char *why = ws_check_answer(head, want, c->protocols, &protocol);
	free(head);
	if (protocol) {
		pthread_mutex_lock(&c->net->mu);
		c->protocol = protocol;
		pthread_mutex_unlock(&c->net->mu);
	}
	return why;
}

/* How the connection goes, the worker's. */
typedef struct {
	ws_reader rd;
	int code, clean, peer_closed, failed;
	const char *why;
	dstr reason;
} ws_state;

/* take feeds what came to the reader and acts on what it finds. */
static void take(ws_conn *c, ws_state *st, const uint8_t *p, size_t n)
{
	for (size_t at = 0; at < n && !st->peer_closed && !st->failed;) {
		ws_event ev;
		at += ws_feed(&st->rd, p + at, n - at, &ev);
		if (ev.kind == WS_EV_NONE)
			break;
		pthread_mutex_lock(&c->net->mu);
		switch (ev.kind) {
		case WS_EV_TEXT:
		case WS_EV_BINARY:
			inbox(c, ev.kind == WS_EV_TEXT ? IN_TEXT : IN_BINARY, ev.data, ev.len, 0, 0);
			break;
		case WS_EV_PING:
			if (!c->closing)
				queue(c, WS_OP_PONG, ev.data, ev.len);
			break;
		case WS_EV_CLOSE:
			st->code = ev.code;
			dstr_put(&st->reason, (const char *)ev.data, ev.len);
			st->clean = st->peer_closed = 1;
			/* Echo the close, unless it answers the app's own. */
			if (!c->closing)
				queue_close(c, ev.code == 1005 ? 0 : ev.code, NULL);
			break;
		case WS_EV_ERROR:
			st->code = ev.code;
			st->why = ev.why;
			st->failed = 1;
			if (!c->closing)
				queue_close(c, ev.code, ev.why);
			break;
		}
		pthread_mutex_unlock(&c->net->mu);
	}
}

static void *ws_worker(void *arg)
{
	ws_conn *c = arg;
	jsrt_net *net = c->net;
	int64_t deadline = jsrt_now_ms() + WS_HANDSHAKE_MS;
	char err[CURL_ERROR_SIZE] = "";
	ws_state st = {.code = 1006};
	ws_reader_init(&st.rd, WS_MAX_MESSAGE);
	dstr got = {0};
	CURL *h = curl_easy_init();
	curl_socket_t s = CURL_SOCKET_BAD;
	if (!h) {
		st.why = "libcurl did not start";
	} else {
		curl_easy_setopt(h, CURLOPT_URL, c->connect_url);
		jsrt_net_curl(h);
		curl_easy_setopt(h, CURLOPT_ERRORBUFFER, err);
		curl_easy_setopt(h, CURLOPT_CONNECT_ONLY, 1L);
		/* The upgrade is HTTP/1.1's: a server offered h2 over ALPN would
		 * take the connection for HTTP/2. */
		curl_easy_setopt(h, CURLOPT_HTTP_VERSION, (long)CURL_HTTP_VERSION_1_1);
		curl_easy_setopt(h, CURLOPT_CONNECTTIMEOUT_MS, 15000L);
		curl_easy_setopt(h, CURLOPT_TIMEOUT_MS, (long)WS_HANDSHAKE_MS);
		curl_easy_setopt(h, CURLOPT_OPENSOCKETFUNCTION, open_sock);
		curl_easy_setopt(h, CURLOPT_OPENSOCKETDATA, c);
		curl_easy_setopt(h, CURLOPT_CLOSESOCKETFUNCTION, close_sock);
		curl_easy_setopt(h, CURLOPT_CLOSESOCKETDATA, c);
		curl_easy_setopt(h, CURLOPT_NOPROGRESS, 0L);
		curl_easy_setopt(h, CURLOPT_XFERINFOFUNCTION, connect_progress);
		curl_easy_setopt(h, CURLOPT_XFERINFODATA, c);
		if (c->keepalive) {
			curl_easy_setopt(h, CURLOPT_SOCKOPTFUNCTION, sock_connected);
			if (c->keep_resolve)
				curl_easy_setopt(h, CURLOPT_RESOLVE, c->keep_resolve);
		}
		CURLcode e = curl_easy_perform(h);
		pthread_mutex_lock(&net->mu);
		int cancelled = c->cancel;
		pthread_mutex_unlock(&net->mu);
		if (cancelled)
			st.why = "aborted";
		else if (e != CURLE_OK)
			st.why = err[0] ? err : curl_easy_strerror(e);
		else if (curl_easy_getinfo(h, CURLINFO_ACTIVESOCKET, &s) != CURLE_OK || s == CURL_SOCKET_BAD)
			st.why = "no socket";
	}
	size_t first = 0;
	if (!st.why)
		st.why = handshake(c, h, s, &got, &first, deadline);
	int aborted = st.why && !strcmp(st.why, "aborted");
	if (!st.why) {
		pthread_mutex_lock(&net->mu);
		inbox(c, IN_OPEN, c->protocol ? c->protocol : "", c->protocol ? strlen(c->protocol) : 0, 0, 0);
		pthread_mutex_unlock(&net->mu);
		take(c, &st, (const uint8_t *)got.s + first, got.len - first);
	}
	uint8_t wbuf[16384], rbuf[16384];
	size_t wlen = 0, woff = 0;
	int64_t closing_since = -1;
	while (!st.why || st.failed) {
		pthread_mutex_lock(&net->mu);
		int cancel = c->cancel, full = c->in_bytes >= WS_MAX_INBOX;
		/* The worker writes from a buffer of its own: a TLS write that
		 * has to be retried is retried with the same bytes. */
		if (!wlen && c->out.len > c->out_taken) {
			wlen = c->out.len - c->out_taken < sizeof wbuf ? c->out.len - c->out_taken : sizeof wbuf;
			memcpy(wbuf, c->out.s + c->out_taken, wlen);
			woff = 0;
			c->out_taken += wlen;
			if (c->out_taken == c->out.len)
				c->out.len = c->out_taken = 0;
		}
		if (c->closing && closing_since < 0)
			closing_since = jsrt_now_ms();
		pthread_mutex_unlock(&net->mu);
		if (cancel) {
			aborted = 1;
			break;
		}
		if ((st.peer_closed || st.failed) && !wlen)
			break;
		int progress = 0;
		if (wlen) {
			size_t sent = 0;
			CURLcode e = curl_easy_send(h, wbuf + woff, wlen - woff, &sent);
			if (e != CURLE_OK && e != CURLE_AGAIN) {
				st.why = "the connection broke";
				break;
			}
			woff += sent;
			progress = sent > 0;
			pthread_mutex_lock(&net->mu);
			c->buffered = c->buffered > sent ? c->buffered - sent : 0;
			pthread_mutex_unlock(&net->mu);
			if (woff == wlen)
				wlen = 0;
		}
		if (!full && !st.peer_closed && !st.failed) {
			size_t n = 0;
			CURLcode e = curl_easy_recv(h, rbuf, sizeof rbuf, &n);
			if (e == CURLE_OK && n == 0) {
				st.why = "the server closed the connection without a close frame";
				break;
			}
			if (e != CURLE_OK && e != CURLE_AGAIN) {
				st.why = "the connection broke";
				break;
			}
			if (e == CURLE_OK) {
				take(c, &st, rbuf, n);
				progress = 1;
			}
		}
		if (progress)
			continue;
		if (closing_since >= 0 && jsrt_now_ms() - closing_since > WS_CLOSING_MS) {
			st.why = "the server did not answer the close";
			break;
		}
		wait_sock(c, s, (short)((full ? 0 : POLLIN) | (wlen ? POLLOUT : 0)), closing_since >= 0 ? closing_since + WS_CLOSING_MS : -1);
	}
	if (h)
		curl_easy_cleanup(h);
	ws_reader_free(&st.rd);
	free(got.s);
	pthread_mutex_lock(&net->mu);
	if (c->keep_fd >= 0) {
		close(c->keep_fd);
		c->keep_fd = -1;
	}
	/* A cancel shuts the socket: what failed after it is the cancel. */
	if (aborted || c->cancel) {
		const char *why = c->dropped ? c->dropped : "aborted";
		inbox(c, IN_CLOSE, why, strlen(why), 1006, 0);
	} else if (st.clean && !st.failed) {
		inbox(c, IN_CLOSE, st.reason.s ? st.reason.s : "", st.reason.len, st.code, 1);
	} else {
		const char *why = st.why ? st.why : "the connection broke";
		inbox(c, IN_ERROR, why, strlen(why), 0, 0);
		inbox(c, IN_CLOSE, why, strlen(why), st.failed ? st.code : 1006, 0);
	}
	c->finished = 1;
	pthread_mutex_unlock(&net->mu);
	free(st.reason.s);
	return NULL;
}

/* ---- the loop's side ---- */

static void conn_free(JSRuntime *rt, ws_conn *c)
{
	for (ws_msg *m = c->in, *next; m; m = next) {
		next = m->next;
		free(m);
	}
	JS_FreeValueRT(rt, c->url);
	JS_FreeValueRT(rt, c->listeners);
	JS_FreeValueRT(rt, c->signal);
	JS_FreeValueRT(rt, c->on_abort);
	free(c->connect_url);
	free(c->host);
	free(c->path);
	free(c->protocols);
	free(c->protocol);
	free(c->out.s);
	curl_slist_free_all(c->headers);
	free(c->keep_host);
	curl_slist_free_all(c->keep_resolve);
	if (c->keep_fd >= 0)
		close(c->keep_fd);
	if (c->efd >= 0)
		close(c->efd);
	free(c);
}

/* Collected: the object was let go once the close was told, by then the
 * worker had finished; or the worker never started. */
static void ws_finalizer(JSRuntime *rt, JSValueConst val)
{
	ws_conn *c = JS_GetOpaque(val, ws_class);
	if (!c)
		return;
	for (ws_conn **p = &c->net->ws_live; *p; p = &(*p)->lnext)
		if (*p == c) {
			*p = c->lnext;
			break;
		}
	conn_free(rt, c);
}

static void ws_mark(JSRuntime *rt, JSValueConst val, JS_MarkFunc *mark)
{
	ws_conn *c = JS_GetOpaque(val, ws_class);
	if (c) {
		JS_MarkValue(rt, c->url, mark);
		JS_MarkValue(rt, c->listeners, mark);
		JS_MarkValue(rt, c->signal, mark);
		JS_MarkValue(rt, c->on_abort, mark);
	}
}

static void unlisten(JSContext *ctx, ws_conn *c)
{
	if (!JS_IsUndefined(c->signal))
		jsrt_signal_listen(ctx, c->signal, c->on_abort, 0);
	JS_FreeValue(ctx, c->signal);
	JS_FreeValue(ctx, c->on_abort);
	c->signal = c->on_abort = JS_UNDEFINED;
}

static void fire(JSContext *ctx, ws_conn *c, const char *type, JSValue ev)
{
	JS_SetPropertyStr(ctx, ev, "type", JS_NewString(ctx, type));
	JS_SetPropertyStr(ctx, ev, "target", JS_DupValue(ctx, c->obj));
	jsrt_fire(ctx, c->obj, &c->listeners, type, ev);
	JS_FreeValue(ctx, ev);
}

static int start_worker(ws_conn *c)
{
	pthread_attr_t attr;
	pthread_attr_init(&attr);
	pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
	pthread_t t;
	int err = pthread_create(&t, &attr, ws_worker, c);
	pthread_attr_destroy(&attr);
	return err;
}

/* ---- keepalive WebSockets ---- */

/* told ends a connection that has no worker: its close, after an error
 * when why is one, comes with the next drain. */
static void told(ws_conn *c, const char *why, int error)
{
	pthread_mutex_lock(&c->net->mu);
	if (error)
		inbox(c, IN_ERROR, why, strlen(why), 0, 0);
	inbox(c, IN_CLOSE, why, strlen(why), 1006, 0);
	c->finished = 1;
	pthread_mutex_unlock(&c->net->mu);
}

/* unqueue takes c off the keepalive WebSockets waiting for their socket:
 * whether it was there. */
static int unqueue(jsrt_net *net, ws_conn *c)
{
	for (ws_conn **p = &net->keep_wait; *p; p = &(*p)->keep_next)
		if (*p == c) {
			*p = c->keep_next;
			c->keep_next = NULL;
			return 1;
		}
	return 0;
}

static void json_str(dstr *d, const char *s)
{
	dstr_puts(d, "\"");
	for (; *s; s++) {
		if (*s == '"' || *s == '\\')
			dstr_puts(d, "\\");
		dstr_put(d, s, 1);
	}
	dstr_puts(d, "\"");
}

/* split_host is a URL authority's host (without brackets) and port. */
static int split_host(const char *auth, int secure, char **host, int *port)
{
	const char *h = auth, *end;
	size_t n;
	if (*h == '[') {
		if (!(end = strchr(h, ']')))
			return -1;
		h++;
		n = (size_t)(end - h);
		end++;
	} else {
		n = strcspn(h, ":");
		end = h + n;
	}
	*port = secure ? 443 : 80;
	if (*end == ':') {
		char *e;
		long p = strtol(end + 1, &e, 10);
		if (*e || p < 1 || p > 65535)
			return -1;
		*port = (int)p;
	} else if (*end) {
		return -1;
	}
	if (!n || !(*host = strndup(h, n)))
		return -1;
	return 0;
}

/* release has the Shell let go of the socket it holds for a keepalive
 * WebSocket that has ended. */
static void release(jsrt *r)
{
	jsrt_call_own(r, "net.keepalive", "{\"close\":true}", NULL, 0);
	r->net->keep_cur = 0;
	r->net->keep_ended = 0;
}

static void keep_reply(jsrt *r, long id, const char *err, JSValueConst data, int *fds, int nfds);

/* ask sends the oldest waiting keepalive WebSocket's net.keepalive once
 * no other is out: the replies then come in the order the Shell replaces
 * its connections. */
static void ask(jsrt *r)
{
	jsrt_net *net = r->net;
	while (net->keep_wait && !net->keep_out) {
		ws_conn *c = net->keep_wait;
		dstr a = {0};
		char tail[64];
		dstr_puts(&a, "{\"host\":");
		json_str(&a, c->keep_host);
		snprintf(tail, sizeof tail, ",\"port\":%d,\"every_s\":%d}", c->keep_port, c->keepalive);
		dstr_puts(&a, tail);
		long id = jsrt_call_own(r, "net.keepalive", a.s, keep_reply, c->id);
		free(a.s);
		if (id >= 0) {
			net->keep_out = c->id;
			return;
		}
		unqueue(net, c);
		told(c, "connection lost", 1);
	}
}

/* replace closes the keepalive WebSockets besides c (NULL: all) that have
 * had their socket: the Shell holds another now. */
static void replace(jsrt_net *net, ws_conn *c)
{
	for (ws_conn *w = net->ws_live; w; w = w->lnext) {
		if (w == c || !w->keep_has || w->state == WS_CLOSING || w->state == WS_CLOSED)
			continue;
		int open = w->state == WS_OPEN;
		w->state = WS_CLOSING;
		if (open) {
			pthread_mutex_lock(&net->mu);
			queue_close(w, 1001, "replaced");
			pthread_mutex_unlock(&net->mu);
			wake_worker(w);
		} else {
			cancel(w, "replaced");
		}
	}
}

/* keep_start hands c's worker the socket the Shell dialled. */
static void keep_start(ws_conn *c, int fd)
{
	c->keep_has = 1;
	/* A host name resolves to where the Shell connected, with no lookup. */
	unsigned char lit[sizeof(struct in6_addr)];
	struct sockaddr_storage sa;
	socklen_t sl = sizeof sa;
	char ip[INET6_ADDRSTRLEN];
	if (inet_pton(AF_INET, c->keep_host, lit) != 1 && inet_pton(AF_INET6, c->keep_host, lit) != 1 &&
	    !getpeername(fd, (struct sockaddr *)&sa, &sl) && (sa.ss_family == AF_INET || sa.ss_family == AF_INET6)) {
		int v6 = sa.ss_family == AF_INET6;
		const void *addr = v6 ? (const void *)&((struct sockaddr_in6 *)&sa)->sin6_addr : (const void *)&((struct sockaddr_in *)&sa)->sin_addr;
		if (inet_ntop(sa.ss_family, addr, ip, sizeof ip)) {
			char entry[512];
			snprintf(entry, sizeof entry, v6 ? "%s:%d:[%s]" : "%s:%d:%s", c->keep_host, c->keep_port, ip);
			c->keep_resolve = curl_slist_append(NULL, entry);
		}
	}
	c->keep_fd = fd;
	int err = start_worker(c);
	if (err) {
		close(fd);
		c->keep_fd = -1;
		told(c, strerror(err), 1);
	}
}

static int stream_socket(int fd)
{
	struct stat st;
	int type = 0;
	socklen_t tl = sizeof type;
	return !fstat(fd, &st) && S_ISSOCK(st.st_mode) && !getsockopt(fd, SOL_SOCKET, SO_TYPE, &type, &tl) && type == SOCK_STREAM;
}

/* keep_reply is the Shell's answer to the call for the keepalive WebSocket
 * id: on success the Shell holds the socket it hands over, and none of
 * the one before, also when id was cancelled meanwhile. */
static void keep_reply(jsrt *r, long id, const char *err, JSValueConst data, int *fds, int nfds)
{
	jsrt_net *net = r->net;
	net->keep_out = 0;
	int fd = -1;
	if (!err && nfds == 1 && stream_socket(fds[0])) {
		fd = fds[0];
		nfds = 0;
	} else if (!err) {
		err = "net.keepalive: the reply has no socket";
	}
	jsrt_close_fds(fds, nfds);
	ws_conn *c = net->keep_wait && net->keep_wait->id == id ? net->keep_wait : NULL;
	if (c)
		unqueue(net, c);
	if (fd >= 0) {
		net->keep_cur = id;
		net->keep_ended = !c;
		replace(net, c);
		if (c)
			keep_start(c, fd);
		else
			close(fd);
	} else if (c) {
		told(c, err, 1);
	}
	if (net->keep_wait)
		ask(r);
	else if (net->keep_cur && net->keep_ended)
		release(r);
}

/* keep_closed: a keepalive WebSocket's close is told. */
static void keep_closed(jsrt *r, ws_conn *c)
{
	jsrt_net *net = r->net;
	unqueue(net, c);
	if (net->keep_cur != c->id)
		return;
	net->keep_ended = 1;
	if (!net->keep_out)
		release(r);
}

void jsrt_ws_keepalive_event(jsrt *r, const char *value)
{
	if (r->net && (!strcmp(value, "closed") || !strcmp(value, "lost"))) {
		r->net->keep_cur = 0;
		r->net->keep_ended = 0;
	}
}

/* drop ends a connection without the closing handshake: at once for a
 * keepalive WebSocket still waiting for its socket. */
static void drop(ws_conn *c)
{
	c->state = WS_CLOSING;
	if (unqueue(c->net, c))
		told(c, "aborted", 0);
	else
		cancel(c, "aborted");
}

void jsrt_ws_drain(jsrt *r, ws_conn *list)
{
	JSContext *ctx = r->ctx;
	ws_conn *ordered = NULL;
	while (list) {
		ws_conn *next = list->next;
		list->next = ordered;
		ordered = list;
		list = next;
	}
	for (ws_conn *c = ordered, *next; c; c = next) {
		next = c->next;
		pthread_mutex_lock(&r->net->mu);
		ws_msg *m = c->in;
		c->in = c->in_tail = NULL;
		c->posted = 0;
		pthread_mutex_unlock(&r->net->mu);
		JSValue obj = JS_DupValue(ctx, c->obj);
		while (m) {
			ws_msg *after = m->next;
			JSValue ev = JS_NewObject(ctx);
			switch (m->kind) {
			case IN_OPEN:
				c->state = WS_OPEN;
				JS_DefinePropertyValueStr(ctx, c->obj, "protocol", JS_NewStringLen(ctx, m->data, m->len), JS_PROP_CONFIGURABLE);
				fire(ctx, c, "open", ev);
				break;
			case IN_TEXT:
				JS_SetPropertyStr(ctx, ev, "data", JS_NewStringLen(ctx, m->data, m->len));
				fire(ctx, c, "message", ev);
				break;
			case IN_BINARY:
				JS_SetPropertyStr(ctx, ev, "data", JS_NewArrayBufferCopy(ctx, (const uint8_t *)m->data, m->len));
				fire(ctx, c, "message", ev);
				break;
			case IN_ERROR:
				JS_SetPropertyStr(ctx, ev, "message", JS_NewString(ctx, m->data));
				fire(ctx, c, "error", ev);
				break;
			case IN_CLOSE:
				c->state = WS_CLOSED;
				r->net->ws_open--;
				if (c->keepalive)
					keep_closed(r, c);
				unlisten(ctx, c);
				JS_SetPropertyStr(ctx, ev, "code", JS_NewInt32(ctx, m->code));
				JS_SetPropertyStr(ctx, ev, "reason", JS_NewStringLen(ctx, m->data, m->len));
				JS_SetPropertyStr(ctx, ev, "wasClean", JS_NewBool(ctx, m->clean));
				fire(ctx, c, "close", ev);
				/* Let go: the object is collected once the app has. */
				JS_FreeValue(ctx, c->obj);
				c->obj = JS_UNDEFINED;
				break;
			default:
				JS_FreeValue(ctx, ev);
			}
			pthread_mutex_lock(&r->net->mu);
			int was_full = c->in_bytes >= WS_MAX_INBOX;
			c->in_bytes -= m->len;
			if (was_full && c->in_bytes < WS_MAX_INBOX && c->state != WS_CLOSED)
				wake_worker(c);
			pthread_mutex_unlock(&r->net->mu);
			free(m);
			m = after;
			jsrt_run_jobs(r);
		}
		JS_FreeValue(ctx, obj);
	}
}

static ws_conn *ws_of(JSContext *ctx, JSValueConst v) { return JS_GetOpaque2(ctx, v, ws_class); }

/* on_abort is a WebSocket's abort listener (data[0] its id): the
 * connection is dropped at once, without the closing handshake. */
static JSValue on_abort(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic, JSValueConst *data)
{
	jsrt *r = jsrt_of(ctx);
	int64_t id = 0;
	JS_ToInt64(ctx, &id, data[0]);
	ws_conn *c = r->net ? r->net->ws_live : NULL;
	while (c && c->id != id)
		c = c->lnext;
	if (!c || c->state == WS_CLOSED)
		return JS_UNDEFINED;
	drop(c);
	unlisten(ctx, c);
	return JS_UNDEFINED;
}

static int is_token(const char *s, size_t n)
{
	if (!n)
		return 0;
	for (size_t i = 0; i < n; i++)
		if (s[i] <= 0x20 || s[i] >= 0x7f || strchr("()<>@,;:\\\"/[]?={}", s[i]))
			return 0;
	return 1;
}

/* protocols_of joins the protocols the app offers (a string or an array
 * of them) with ", ": NULL for none, or the reason in *bad. */
static char *protocols_of(JSContext *ctx, JSValueConst v, const char **bad)
{
	if (JS_IsUndefined(v))
		return NULL;
	dstr d = {0};
	int64_t n = 1;
	int arr = JS_IsArray(v) > 0;
	if (arr && JS_GetLength(ctx, v, &n))
		n = 0;
	if (n > 16) {
		*bad = "at most 16 protocols";
		return NULL;
	}
	for (int64_t i = 0; i < n && !*bad; i++) {
		JSValue p = arr ? JS_GetPropertyInt64(ctx, v, i) : JS_DupValue(ctx, v);
		size_t len;
		const char *s = JS_IsString(p) ? JS_ToCStringLen(ctx, &len, p) : NULL;
		if (!s || !is_token(s, len))
			*bad = "a protocol that is not a token";
		else if (d.s && ws_has_token(d.s, d.len, s))
			*bad = "a protocol given twice";
		else {
			if (d.len)
				dstr_puts(&d, ", ");
			dstr_put(&d, s, len);
		}
		JS_FreeCString(ctx, s);
		JS_FreeValue(ctx, p);
	}
	if (*bad || !d.len) {
		free(d.s);
		return NULL;
	}
	return d.s;
}

static JSValue ws_ctor(JSContext *ctx, JSValueConst new_target, int argc, JSValueConst *argv)
{
	jsrt *r = jsrt_of(ctx);
	jsrt_net *net = jsrt_net_get(r, "WebSocket");
	if (!net)
		return JS_EXCEPTION;
	if (argc < 1 || !JS_IsString(argv[0]))
		return JS_ThrowTypeError(ctx, "WebSocket: the URL is a string");
	const char *url = JS_ToCString(ctx, argv[0]);
	if (!url)
		return JS_EXCEPTION;
	const char *bad = jsrt_net_url_check(url, 1);
	if (bad) {
		JS_ThrowSyntaxError(ctx, "WebSocket: %s", bad);
		JS_FreeCString(ctx, url);
		return JS_EXCEPTION;
	}
	ws_conn *c = calloc(1, sizeof *c);
	if (!c) {
		JS_FreeCString(ctx, url);
		return JS_ThrowOutOfMemory(ctx);
	}
	c->net = net;
	c->wake = r->net_fd;
	c->efd = c->keep_fd = -1;
	c->obj = c->url = c->listeners = c->signal = c->on_abort = JS_UNDEFINED;
	int secure = !strncasecmp(url, "wss://", 6);
	const char *auth = url + (secure ? 6 : 5);
	size_t alen = strcspn(auth, "/?");
	c->host = strndup(auth, alen);
	c->path = auth[alen] == '/' ? strdup(auth + alen) : auth[alen] == '?' ? NULL : strdup("/");
	if (!c->path && auth[alen] == '?') {
		dstr p = {0};
		dstr_puts(&p, "/");
		dstr_puts(&p, auth + alen);
		c->path = p.s;
	}
	dstr cu = {0};
	dstr_puts(&cu, secure ? "https://" : "http://");
	dstr_puts(&cu, c->host ? c->host : "");
	dstr_puts(&cu, "/");
	c->connect_url = cu.s;
	c->url = JS_NewString(ctx, url);
	JS_FreeCString(ctx, url);
	bad = NULL;
	c->protocols = protocols_of(ctx, argc > 1 ? argv[1] : JS_UNDEFINED, &bad);
	JSValueConst o = argc > 2 ? argv[2] : JS_UNDEFINED;
	if (!bad && !JS_IsUndefined(o) && !JS_IsNull(o) && !JS_IsObject(o))
		bad = "the options are an object";
	if (!bad && JS_IsObject(o)) {
		int typed = 0;
		JSValue h = JS_GetPropertyStr(ctx, o, "headers");
		bad = jsrt_net_headers(ctx, h, &c->headers, &typed, 1);
		JS_FreeValue(ctx, h);
		JSValue s = JS_GetPropertyStr(ctx, o, "signal");
		if (!bad && !JS_IsUndefined(s) && !JS_IsNull(s)) {
			if (jsrt_signal_state(ctx, s, NULL) < 0)
				bad = "signal is an AbortSignal";
			else
				c->signal = JS_DupValue(ctx, s);
		}
		JS_FreeValue(ctx, s);
		JSValue k = JS_GetPropertyStr(ctx, o, "keepalive");
		double every = 0;
		if (!bad && !JS_IsUndefined(k) && !JS_IsNull(k)) {
			if (!jsrt_net_keepalive())
				bad = "the app's manifest does not ask for the \"keepalive\" permission";
			else if (!JS_IsNumber(k) || JS_ToFloat64(ctx, &every, k) || !(every >= KEEPALIVE_MIN_S && every <= KEEPALIVE_MAX_S) ||
				 every != (int)every)
				bad = "keepalive is the heartbeat's interval in whole seconds, 60 to 3600";
			else if (split_host(c->host, secure, &c->keep_host, &c->keep_port))
				bad = "a URL with a bad host or port";
			else
				c->keepalive = (int)every;
		}
		JS_FreeValue(ctx, k);
	}
	if (!bad && net->ws_open >= WS_MAX_OPEN)
		bad = "at most 4 WebSockets at a time";
	if (!bad && (c->efd = eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK)) < 0)
		bad = strerror(errno);
	JSValue reason = JS_UNDEFINED;
	if (bad || (!JS_IsUndefined(c->signal) && jsrt_signal_state(ctx, c->signal, &reason) > 0)) {
		conn_free(JS_GetRuntime(ctx), c);
		if (!bad)
			return JS_Throw(ctx, reason);
		return JS_ThrowTypeError(ctx, "WebSocket: %s", bad);
	}
	JSValue proto = JS_GetPropertyStr(ctx, new_target, "prototype");
	JSValue obj = JS_NewObjectProtoClass(ctx, proto, ws_class);
	JS_FreeValue(ctx, proto);
	if (JS_IsException(obj)) {
		conn_free(JS_GetRuntime(ctx), c);
		return obj;
	}
	JS_SetOpaque(obj, c);
	JS_DefinePropertyValueStr(ctx, obj, "protocol", JS_NewString(ctx, ""), JS_PROP_CONFIGURABLE);
	pthread_mutex_lock(&net->mu);
	c->id = ++net->next_id;
	pthread_mutex_unlock(&net->mu);
	c->lnext = net->ws_live;
	net->ws_live = c;
	if (!JS_IsUndefined(c->signal)) {
		JSValue id = JS_NewInt64(ctx, c->id);
		c->on_abort = JS_NewCFunctionData(ctx, on_abort, 0, 0, 1, (JSValueConst *)&id);
		jsrt_signal_listen(ctx, c->signal, c->on_abort, 1);
	}
	if (c->keepalive) {
		ws_conn **p = &net->keep_wait;
		while (*p)
			p = &(*p)->keep_next;
		*p = c;
		ask(r);
	} else {
		int err = start_worker(c);
		if (err) {
			JS_FreeValue(ctx, obj);
			return JS_ThrowTypeError(ctx, "WebSocket: %s", strerror(err));
		}
	}
	/* Held until the close is told, as a browser keeps an open socket. */
	c->obj = JS_DupValue(ctx, obj);
	net->ws_open++;
	return obj;
}

static JSValue ws_send(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	ws_conn *c = ws_of(ctx, this_val);
	if (!c)
		return JS_EXCEPTION;
	if (c->state == WS_CONNECTING)
		return JS_ThrowTypeError(ctx, "WebSocket.send: the connection is not open yet");
	if (c->state != WS_OPEN)
		return JS_UNDEFINED;
	JSValueConst v = argc ? argv[0] : JS_UNDEFINED;
	size_t len;
	char *own = NULL;
	const uint8_t *p;
	int op = WS_OP_BINARY;
	if (JS_IsString(v)) {
		if (!(own = jsrt_utf8(ctx, v, &len)))
			return JS_EXCEPTION;
		p = (const uint8_t *)own;
		op = WS_OP_TEXT;
	} else if (!(p = jsrt_bytes_of(ctx, v, &len))) {
		return JS_ThrowTypeError(ctx, "WebSocket.send: the data is a string, an ArrayBuffer or a view on one");
	}
	if (len > WS_MAX_SEND) {
		free(own);
		return JS_ThrowRangeError(ctx, "WebSocket.send: a message over %u MB", WS_MAX_SEND >> 20);
	}
	size_t frame = len + (len < 126 ? 6 : len < 65536 ? 8 : 14);
	pthread_mutex_lock(&c->net->mu);
	int over = c->buffered + frame > WS_MAX_BUFFERED;
	if (!over && queue(c, op, p, len) == 0)
		c->buffered += frame;
	pthread_mutex_unlock(&c->net->mu);
	free(own);
	if (over)
		return JS_ThrowRangeError(ctx, "WebSocket.send: over %u MB waits to be sent (bufferedAmount)", WS_MAX_BUFFERED >> 20);
	wake_worker(c);
	return JS_UNDEFINED;
}

static JSValue ws_close(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	ws_conn *c = ws_of(ctx, this_val);
	if (!c)
		return JS_EXCEPTION;
	int32_t code = 0;
	if (argc > 0 && !JS_IsUndefined(argv[0])) {
		if (JS_ToInt32(ctx, &code, argv[0]))
			return JS_EXCEPTION;
		if (code != 1000 && (code < 3000 || code > 4999))
			return JS_ThrowTypeError(ctx, "WebSocket.close: the code is 1000, or from 3000 to 4999");
	}
	char *why = NULL;
	size_t wlen = 0;
	if (argc > 1 && !JS_IsUndefined(argv[1])) {
		if (!(why = jsrt_utf8(ctx, argv[1], &wlen)))
			return JS_EXCEPTION;
		if (wlen > 123) {
			free(why);
			return JS_ThrowSyntaxError(ctx, "WebSocket.close: the reason is over 123 bytes");
		}
		if (wlen && !code)
			code = 1000;
	}
	if (c->state == WS_CONNECTING) {
		drop(c);
	} else if (c->state == WS_OPEN) {
		c->state = WS_CLOSING;
		pthread_mutex_lock(&c->net->mu);
		queue_close(c, code, why);
		pthread_mutex_unlock(&c->net->mu);
		wake_worker(c);
	}
	free(why);
	return JS_UNDEFINED;
}

static JSValue ws_listen(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int add)
{
	ws_conn *c = ws_of(ctx, this_val);
	if (!c || jsrt_listen(ctx, &c->listeners, argc, argv, add) < 0)
		return JS_EXCEPTION;
	return JS_UNDEFINED;
}

enum { WS_GET_STATE, WS_GET_URL, WS_GET_BUFFERED, WS_GET_EXTENSIONS, WS_GET_BINARY };

static JSValue ws_get(JSContext *ctx, JSValueConst this_val, int magic)
{
	ws_conn *c = ws_of(ctx, this_val);
	if (!c)
		return JS_EXCEPTION;
	switch (magic) {
	case WS_GET_STATE:
		return JS_NewInt32(ctx, c->state);
	case WS_GET_URL:
		return JS_DupValue(ctx, c->url);
	case WS_GET_BUFFERED: {
		pthread_mutex_lock(&c->net->mu);
		size_t b = c->buffered;
		pthread_mutex_unlock(&c->net->mu);
		return JS_NewFloat64(ctx, (double)b);
	}
	case WS_GET_EXTENSIONS:
		return JS_NewString(ctx, "");
	default:
		return JS_NewString(ctx, "arraybuffer");
	}
}

static JSValue ws_get_binary(JSContext *ctx, JSValueConst this_val) { return ws_get(ctx, this_val, WS_GET_BINARY); }

static JSValue ws_set_binary(JSContext *ctx, JSValueConst this_val, JSValueConst v)
{
	const char *s = JS_ToCString(ctx, v);
	int ok = s && !strcmp(s, "arraybuffer");
	JS_FreeCString(ctx, s);
	if (!ok)
		return JS_ThrowTypeError(ctx, "WebSocket.binaryType: only \"arraybuffer\" (messages do not come as Blobs)");
	return JS_UNDEFINED;
}

static const JSCFunctionListEntry ws_proto[] = {
	JS_CFUNC_DEF("send", 1, ws_send),
	JS_CFUNC_DEF("close", 0, ws_close),
	JS_CFUNC_MAGIC_DEF("addEventListener", 2, ws_listen, 1),
	JS_CFUNC_MAGIC_DEF("removeEventListener", 2, ws_listen, 0),
	JS_CGETSET_MAGIC_DEF("readyState", ws_get, NULL, WS_GET_STATE),
	JS_CGETSET_MAGIC_DEF("url", ws_get, NULL, WS_GET_URL),
	JS_CGETSET_MAGIC_DEF("bufferedAmount", ws_get, NULL, WS_GET_BUFFERED),
	JS_CGETSET_MAGIC_DEF("extensions", ws_get, NULL, WS_GET_EXTENSIONS),
	JS_CGETSET_DEF("binaryType", ws_get_binary, ws_set_binary),
	JS_PROP_INT32_DEF("CONNECTING", WS_CONNECTING, 0),
	JS_PROP_INT32_DEF("OPEN", WS_OPEN, 0),
	JS_PROP_INT32_DEF("CLOSING", WS_CLOSING, 0),
	JS_PROP_INT32_DEF("CLOSED", WS_CLOSED, 0),
	JS_PROP_STRING_DEF("[Symbol.toStringTag]", "WebSocket", JS_PROP_CONFIGURABLE),
};

static const JSCFunctionListEntry ws_static[] = {
	JS_PROP_INT32_DEF("CONNECTING", WS_CONNECTING, 0),
	JS_PROP_INT32_DEF("OPEN", WS_OPEN, 0),
	JS_PROP_INT32_DEF("CLOSING", WS_CLOSING, 0),
	JS_PROP_INT32_DEF("CLOSED", WS_CLOSED, 0),
};

JSValue jsrt_websocket_ctor(JSContext *ctx)
{
	JSRuntime *rt = JS_GetRuntime(ctx);
	JSClassDef def = {.class_name = "WebSocket", .finalizer = ws_finalizer, .gc_mark = ws_mark};
	JS_NewClassID(rt, &ws_class);
	JS_NewClass(rt, ws_class, &def);
	return jsrt_class(ctx, "WebSocket", ws_ctor, 1, ws_class, ws_proto, sizeof ws_proto / sizeof ws_proto[0], ws_static,
			  sizeof ws_static / sizeof ws_static[0]);
}

/* ---- the teardown ---- */

void jsrt_ws_stop(jsrt *r)
{
	for (ws_conn *c = r->net->ws_live; c; c = c->lnext)
		if (!JS_IsUndefined(c->obj)) {
			cancel(c, "aborted");
			unlisten(r->ctx, c);
		}
}

int jsrt_ws_busy(jsrt_net *net)
{
	int busy = 0;
	pthread_mutex_lock(&net->mu);
	for (ws_conn *c = net->ws_live; c && !busy; c = c->lnext)
		busy = !JS_IsUndefined(c->obj) && !c->finished;
	pthread_mutex_unlock(&net->mu);
	return busy;
}

void jsrt_ws_release(jsrt *r)
{
	for (ws_conn *c = r->net->ws_live; c; c = c->lnext) {
		pthread_mutex_lock(&r->net->mu);
		int finished = c->finished;
		pthread_mutex_unlock(&r->net->mu);
		if (finished && !JS_IsUndefined(c->obj)) {
			JS_FreeValue(r->ctx, c->obj);
			c->obj = JS_UNDEFINED;
		}
	}
}
