/* fetch for apps whose manifest asks for "net" (22-developer-api.md §9.4):
 * HTTPS (and HTTP to the device's own loopback) through libcurl on worker
 * threads, in the app's own process, so the request has the app's uid,
 * network namespace, firewall rules and traffic accounting, as a Go app's
 * own connections do. The same rules and limits hold under Node.js
 * (sdk/ts/node/runtime.mjs).
 *
 * fetch() settles once the response's headers are in. Its body is read
 * whole (text(), json(), arrayBuffer(): at most 8 MB, as often as asked) or
 * as a stream (body.getReader().read(), for await over body: chunks of at
 * most 64 KB, with no limit on the whole), and the worker reads at most
 * 256 KB ahead of the app, so a stream of any length holds that much. An
 * AbortSignal cancels the request at any point; a body nobody holds any
 * more is cancelled when it is collected. */
#define _GNU_SOURCE
#include <ctype.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/eventfd.h>
#include <unistd.h>

#include "net.h"

#define FETCH_MAX_SEND (4 << 20)
#define FETCH_MAX_BODY (8 << 20)
#define FETCH_MAX_RUNNING 8
#define FETCH_MAX_REDIRECTS 5
#define FETCH_TIMEOUT_MS 30000
#define FETCH_TIMEOUT_MAX_MS 120000
#define FETCH_CHUNK (64 << 10)
#define FETCH_AHEAD (256 << 10)
#define COOKIE_FILE "kario-cookies.txt"

enum { BODY_UNDECIDED, BODY_WHOLE, BODY_STREAM };

struct fetch_req {
	fetch_req *next;  /* the done list (net->mu) */
	fetch_req *lnext; /* the live list (loop) */
	jsrt_net *net;
	int wake;
	int posted;
	long id;
	/* The request, written before the worker starts. */
	char *url, *method;
	struct curl_slist *headers;
	char *body;
	size_t body_len;
	int has_body, cookies, follows;
	long timeout_ms;
	int64_t start;
	/* The worker's, read by the loop under net->mu. */
	int headed, finished, location;
	CURLcode code;
	long status, redirects;
	char *final_url;
	dstr head;
	dstr buf;     /* body bytes not yet handed over, from taken on */
	size_t taken; /* of buf */
	size_t total;
	int too_big; /* 1: the body over 8 MB (read whole), 2: the headers over 64 KB */
	int timed_out;
	int64_t last_data;
	char err[CURL_ERROR_SIZE];
	/* The loop's, read by the worker under net->mu. */
	int mode, cancel, reading;
	int64_t read_since;
	pthread_cond_t cv;
	/* The loop's own. */
	int worker, handle, responded, body_done;
	JSValue resolve, reject;
	JSValue read_resolve, read_reject;
	JSValue whole, whole_resolve, whole_reject;
	JSValue signal, on_abort, reason;
};

static int permitted = -1;
static JSClassID body_class;

static void body_init(JSContext *ctx);

void jsrt_net_init(jsrt *r)
{
	JSContext *ctx = r->ctx;
	JSValue perms = jsrt_manifest_prop(r, "permissions");
	permitted = 0;
	int64_t n = 0;
	if (JS_IsArray(perms) > 0 && !JS_GetLength(ctx, perms, &n)) {
		for (int64_t i = 0; i < n && !permitted; i++) {
			JSValue p = JS_GetPropertyInt64(ctx, perms, i);
			const char *s = JS_ToCString(ctx, p);
			permitted = s && !strcmp(s, "net");
			JS_FreeCString(ctx, s);
			JS_FreeValue(ctx, p);
		}
	}
	JS_FreeValue(ctx, perms);
	/* OpenSSL loads its configuration and providers here, before
	 * jsrt_native_preload seals executable memory. */
	if (permitted && curl_global_init(CURL_GLOBAL_DEFAULT) != CURLE_OK) {
		fprintf(stderr, "kario-jsrt: libcurl did not start; fetch fails\n");
		permitted = 0;
	}
	body_init(ctx);
}

static void share_lock(CURL *h, curl_lock_data data, curl_lock_access access, void *arg) { pthread_mutex_lock(arg); }
static void share_unlock(CURL *h, curl_lock_data data, void *arg) { pthread_mutex_unlock(arg); }

jsrt_net *jsrt_net_get(jsrt *r, const char *what)
{
	if (permitted <= 0) {
		JS_ThrowTypeError(r->ctx, "%s: the app's manifest does not ask for the \"net\" permission", what);
		return NULL;
	}
	if (r->net)
		return r->net;
	jsrt_net *n = calloc(1, sizeof *n);
	int fd = n ? eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK) : -1;
	if (fd < 0) {
		JS_ThrowTypeError(r->ctx, "%s: %s", what, strerror(n ? errno : ENOMEM));
		free(n);
		return NULL;
	}
	pthread_mutex_init(&n->mu, NULL);
	pthread_mutex_init(&n->share_mu, NULL);
	n->share = curl_share_init();
	if (n->share) {
		curl_share_setopt(n->share, CURLSHOPT_LOCKFUNC, share_lock);
		curl_share_setopt(n->share, CURLSHOPT_UNLOCKFUNC, share_unlock);
		curl_share_setopt(n->share, CURLSHOPT_USERDATA, &n->share_mu);
		curl_share_setopt(n->share, CURLSHOPT_SHARE, CURL_LOCK_DATA_COOKIE);
	}
	if (r->data_dir && *r->data_dir) {
		dstr p = {0};
		dstr_puts(&p, r->data_dir);
		dstr_puts(&p, "/" COOKIE_FILE);
		n->jar = p.s;
	}
	r->net = n;
	r->net_fd = fd;
	return n;
}

void jsrt_net_wake(int fd)
{
	uint64_t one = 1;
	ssize_t w = write(fd, &one, sizeof one);
	(void)w;
}

void jsrt_net_curl(CURL *c)
{
	curl_easy_setopt(c, CURLOPT_NOSIGNAL, 1L);
#if LIBCURL_VERSION_NUM >= 0x075500
	curl_easy_setopt(c, CURLOPT_PROTOCOLS_STR, "http,https");
	curl_easy_setopt(c, CURLOPT_REDIR_PROTOCOLS_STR, "https");
#else
	curl_easy_setopt(c, CURLOPT_PROTOCOLS, (long)(CURLPROTO_HTTP | CURLPROTO_HTTPS));
	curl_easy_setopt(c, CURLOPT_REDIR_PROTOCOLS, (long)CURLPROTO_HTTPS);
#endif
	curl_easy_setopt(c, CURLOPT_USERAGENT, "kario-jsrt");
	const char *ca = getenv(FETCH_CA_ENV);
	if (ca && *ca)
		curl_easy_setopt(c, CURLOPT_CAINFO, ca);
}

static JSValue settled(JSContext *ctx, JSValue value, int ok)
{
	JSValue funcs[2];
	JSValue p = JS_NewPromiseCapability(ctx, funcs);
	if (!JS_IsException(p)) {
		JSValue v = JS_Call(ctx, funcs[ok ? 0 : 1], JS_UNDEFINED, 1, &value);
		JS_FreeValue(ctx, v);
	}
	JS_FreeValue(ctx, funcs[0]);
	JS_FreeValue(ctx, funcs[1]);
	JS_FreeValue(ctx, value);
	return p;
}

/* type_error is a TypeError "fetch: <why>", as a value. */
static JSValue type_error(JSContext *ctx, const char *why)
{
	JS_ThrowTypeError(ctx, "fetch: %s", why);
	return JS_GetException(ctx);
}

static JSValue refused(JSContext *ctx, const char *why) { return settled(ctx, type_error(ctx, why), 0); }

const char *jsrt_net_url_check(const char *url, int ws)
{
	size_t len = strlen(url);
	if (len > FETCH_MAX_URL)
		return "the URL is too long";
	for (size_t i = 0; i < len; i++)
		if ((unsigned char)url[i] <= 0x20 || url[i] == 0x7f)
			return "the URL has spaces or control characters";
	const char *s = ws ? "wss://" : "https://", *p = ws ? "ws://" : "http://";
	int secure = !strncasecmp(url, s, strlen(s)), plain = !strncasecmp(url, p, strlen(p));
	if (!secure && !plain)
		return ws ? "only wss:// URLs, and ws:// to the device's own loopback" : "only https:// URLs, and http:// to the device's own loopback";
	if (ws && strchr(url, '#'))
		return "a WebSocket URL has no fragment";
	const char *rest = url + strlen(secure ? s : p);
	size_t auth = strcspn(rest, "/?#");
	if (memchr(rest, '@', auth))
		return "a URL with a user name or password";
	if (auth == 0)
		return "a URL without a host";
	if (secure)
		return NULL;
	char host[256];
	size_t h;
	if (rest[0] == '[') {
		const char *end = memchr(rest, ']', auth);
		if (!end)
			return "a URL with a bad host";
		h = end - rest - 1;
		rest++;
	} else {
		h = strcspn(rest, ":/?#");
	}
	if (h == 0 || h >= sizeof host)
		return "a URL with a bad host";
	for (size_t i = 0; i < h; i++)
		host[i] = tolower((unsigned char)rest[i]);
	host[h] = 0;
	if (!strcmp(host, "localhost") || !strncmp(host, "127.", 4) || !strcmp(host, "::1"))
		return NULL;
	return ws ? "ws:// only to the device's own loopback (localhost, 127.0.0.1, [::1]); use wss://"
		  : "http:// only to the device's own loopback (localhost, 127.0.0.1, [::1]); use https://";
}

static const char *const methods[] = {"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"};

/* Headers libcurl or the network sets; an app's would contradict them. */
static const char *const forbidden[] = {"host", "content-length", "transfer-encoding", "connection",
	"expect", "upgrade", "te", "trailer", "keep-alive"};

static int token(const char *s)
{
	if (!*s)
		return 0;
	for (; *s; s++)
		if (!isalnum((unsigned char)*s) && !strchr("!#$%&'*+-.^_`|~", *s))
			return 0;
	return 1;
}

const char *jsrt_net_headers(JSContext *ctx, JSValueConst obj, struct curl_slist **list, int *typed, int ws)
{
	if (JS_IsUndefined(obj) || JS_IsNull(obj))
		return NULL;
	if (!JS_IsObject(obj) || JS_IsArray(obj) > 0)
		return "headers is an object of strings";
	JSPropertyEnum *props;
	uint32_t n;
	if (JS_GetOwnPropertyNames(ctx, &props, &n, obj, JS_GPN_STRING_MASK | JS_GPN_ENUM_ONLY))
		return "headers is an object of strings";
	const char *bad = NULL;
	if (n > FETCH_MAX_HEADERS)
		bad = "more than 64 headers";
	for (uint32_t i = 0; i < n && !bad; i++) {
		const char *name = JS_AtomToCString(ctx, props[i].atom);
		JSValue v = JS_GetProperty(ctx, obj, props[i].atom);
		const char *value = JS_IsString(v) ? JS_ToCString(ctx, v) : NULL;
		if (!name || !value) {
			bad = "headers is an object of strings";
		} else if (!token(name)) {
			bad = "a header name that is not a token";
		} else if (strpbrk(value, "\r\n") || strlen(name) + strlen(value) > 8192) {
			bad = "a header value with a line break, or over 8 KB";
		} else if (ws && !strncasecmp(name, "sec-websocket-", 14)) {
			bad = "a header of the handshake itself (Sec-WebSocket-…; protocols go in the second argument)";
		} else {
			for (size_t f = 0; f < sizeof forbidden / sizeof forbidden[0]; f++)
				if (!strcasecmp(name, forbidden[f]))
					bad = "a header the network sets itself (Host, Content-Length, Connection…)";
		}
		if (!bad) {
			if (!strcasecmp(name, "content-type"))
				*typed = 1;
			dstr line = {0};
			dstr_puts(&line, name);
			dstr_puts(&line, ": ");
			dstr_puts(&line, value);
			*list = curl_slist_append(*list, line.s);
			free(line.s);
		}
		JS_FreeCString(ctx, name);
		JS_FreeCString(ctx, value);
		JS_FreeValue(ctx, v);
	}
	for (uint32_t i = 0; i < n; i++)
		JS_FreeAtom(ctx, props[i].atom);
	js_free(ctx, props);
	return bad;
}

/* body: the init's body into q: a string (UTF-8, text/plain unless the app
 * says otherwise) or bytes (an ArrayBuffer or a typed array). */
static const char *body(JSContext *ctx, JSValueConst v, fetch_req *q, int *text)
{
	if (JS_IsUndefined(v) || JS_IsNull(v))
		return NULL;
	if (JS_IsString(v)) {
		size_t len;
		char *s = jsrt_utf8(ctx, v, &len);
		if (!s) {
			JS_FreeValue(ctx, JS_GetException(ctx));
			return "body is a string, an ArrayBuffer or a typed array";
		}
		*text = 1;
		if (len > FETCH_MAX_SEND) {
			free(s);
			return "the body is over 4 MB";
		}
		q->body = s;
		q->body_len = len;
		q->has_body = 1;
		return NULL;
	}
	size_t len;
	const uint8_t *p = jsrt_bytes_of(ctx, v, &len);
	if (!p)
		return "body is a string, an ArrayBuffer or a typed array";
	if (len > FETCH_MAX_SEND)
		return "the body is over 4 MB";
	q->body = malloc(len ? len : 1);
	if (!q->body)
		return "out of memory";
	memcpy(q->body, p, len);
	q->body_len = len;
	q->has_body = 1;
	return NULL;
}

/* post tells the loop q has news (under net->mu). */
static void post(fetch_req *q)
{
	if (q->posted)
		return;
	q->posted = 1;
	q->next = q->net->done;
	q->net->done = q;
	jsrt_net_wake(q->wake);
}

/* headed notes the response the app gets (under net->mu). */
static void headed(fetch_req *q, CURL *c)
{
	if (q->headed)
		return;
	char *u = NULL;
	curl_easy_getinfo(c, CURLINFO_RESPONSE_CODE, &q->status);
	curl_easy_getinfo(c, CURLINFO_REDIRECT_COUNT, &q->redirects);
	if (curl_easy_getinfo(c, CURLINFO_EFFECTIVE_URL, &u) == CURLE_OK && u)
		q->final_url = strdup(u);
	q->headed = 1;
	q->last_data = jsrt_now_ms();
	post(q);
}

typedef struct {
	fetch_req *q;
	CURL *c;
} transfer;

static size_t on_body(char *p, size_t size, size_t n, void *arg)
{
	transfer *t = arg;
	fetch_req *q = t->q;
	size_t len = size * n;
	jsrt_net *net = q->net;
	pthread_mutex_lock(&net->mu);
	headed(q, t->c);
	if (q->mode == BODY_WHOLE && q->total + len > FETCH_MAX_BODY)
		q->too_big = 1;
	if (q->cancel || q->too_big) {
		pthread_mutex_unlock(&net->mu);
		return 0;
	}
	dstr_put(&q->buf, p, len);
	q->total += len;
	if (q->reading)
		post(q);
	/* Read at most so far ahead of the app (until it reads whole). */
	while (q->mode != BODY_WHOLE && q->buf.len - q->taken >= FETCH_AHEAD && !q->cancel)
		pthread_cond_wait(&q->cv, &net->mu);
	q->last_data = jsrt_now_ms();
	int stop = q->cancel || (q->mode == BODY_WHOLE && q->total > FETCH_MAX_BODY);
	if (q->mode == BODY_WHOLE && q->total > FETCH_MAX_BODY)
		q->too_big = 1;
	pthread_mutex_unlock(&net->mu);
	return stop ? 0 : len;
}

static size_t on_head(char *p, size_t size, size_t n, void *arg)
{
	transfer *t = arg;
	fetch_req *q = t->q;
	size_t len = size * n;
	/* A new status line: a redirect's or a 100 Continue's headers go. */
	if (len >= 5 && !memcmp(p, "HTTP/", 5)) {
		q->head.len = 0;
		q->location = 0;
		return len;
	}
	if (len <= 2 && (p[0] == '\r' || p[0] == '\n')) {
		/* The end of a response's headers: the app's response unless
		 * libcurl goes on (1xx, a redirect it follows). */
		long status = 0;
		curl_easy_getinfo(t->c, CURLINFO_RESPONSE_CODE, &status);
		int redirect = status >= 300 && status < 400 && status != 304 && q->location && q->follows;
		if (status >= 200 && !redirect) {
			pthread_mutex_lock(&q->net->mu);
			headed(q, t->c);
			pthread_mutex_unlock(&q->net->mu);
		}
		return len;
	}
	if (q->head.len + len > FETCH_MAX_HEAD) {
		q->too_big = 2;
		return 0;
	}
	if (len >= 9 && !strncasecmp(p, "location:", 9))
		q->location = 1;
	pthread_mutex_lock(&q->net->mu);
	dstr_put(&q->head, p, len);
	pthread_mutex_unlock(&q->net->mu);
	return len;
}

/* on_progress enforces the timeout: until the response's headers (and,
 * read whole, its body), a request has timeout_ms; a body read as a stream
 * then has timeout_ms of silence while the app waits on it. */
static int on_progress(void *arg, curl_off_t dt, curl_off_t dn, curl_off_t ut, curl_off_t un)
{
	fetch_req *q = arg;
	int64_t now = jsrt_now_ms();
	pthread_mutex_lock(&q->net->mu);
	int stop = q->cancel;
	if (!stop && (!q->headed || q->mode == BODY_WHOLE) && now - q->start > q->timeout_ms)
		stop = q->timed_out = 1;
	int64_t since = q->last_data > q->read_since ? q->last_data : q->read_since;
	if (!stop && q->headed && q->mode == BODY_STREAM && q->reading && now - since > q->timeout_ms)
		stop = q->timed_out = 2;
	pthread_mutex_unlock(&q->net->mu);
	return stop;
}

static void *worker(void *arg)
{
	fetch_req *q = arg;
	jsrt_net *net = q->net;
	CURL *c = curl_easy_init();
	CURLcode code = CURLE_FAILED_INIT;
	if (c) {
		transfer t = {q, c};
		curl_easy_setopt(c, CURLOPT_URL, q->url);
		jsrt_net_curl(c);
		curl_easy_setopt(c, CURLOPT_ERRORBUFFER, q->err);
		curl_easy_setopt(c, CURLOPT_FOLLOWLOCATION, 1L);
		curl_easy_setopt(c, CURLOPT_MAXREDIRS, (long)FETCH_MAX_REDIRECTS);
		curl_easy_setopt(c, CURLOPT_CONNECTTIMEOUT_MS, q->timeout_ms < 15000 ? q->timeout_ms : 15000L);
		curl_easy_setopt(c, CURLOPT_NOPROGRESS, 0L);
		curl_easy_setopt(c, CURLOPT_XFERINFOFUNCTION, on_progress);
		curl_easy_setopt(c, CURLOPT_XFERINFODATA, q);
		curl_easy_setopt(c, CURLOPT_ACCEPT_ENCODING, "");
		curl_easy_setopt(c, CURLOPT_WRITEFUNCTION, on_body);
		curl_easy_setopt(c, CURLOPT_WRITEDATA, &t);
		curl_easy_setopt(c, CURLOPT_HEADERFUNCTION, on_head);
		curl_easy_setopt(c, CURLOPT_HEADERDATA, &t);
		if (q->cookies && net->share) {
			curl_easy_setopt(c, CURLOPT_SHARE, net->share);
			pthread_mutex_lock(&net->mu);
			int load = !net->jar_loaded;
			net->jar_loaded = 1;
			pthread_mutex_unlock(&net->mu);
			/* The jar outlives the process; a session cookie lives as
			 * long as the process does. */
			if (load && net->jar) {
				curl_easy_setopt(c, CURLOPT_COOKIEFILE, net->jar);
				curl_easy_setopt(c, CURLOPT_COOKIESESSION, 1L);
			}
			if (net->jar)
				curl_easy_setopt(c, CURLOPT_COOKIEJAR, net->jar);
		}
		/* GET, HEAD and POST follow redirects, POST turning into GET on
		 * 301, 302 and 303 as browsers do (libcurl's own POST); the other
		 * methods hand the 3xx to the app, as libcurl would keep a custom
		 * method across them. */
		if (!strcmp(q->method, "HEAD")) {
			curl_easy_setopt(c, CURLOPT_NOBODY, 1L);
		} else if (!strcmp(q->method, "POST")) {
			curl_easy_setopt(c, CURLOPT_POST, 1L);
			curl_easy_setopt(c, CURLOPT_POSTFIELDS, q->body ? q->body : "");
			curl_easy_setopt(c, CURLOPT_POSTFIELDSIZE_LARGE, (curl_off_t)q->body_len);
		} else if (strcmp(q->method, "GET")) {
			curl_easy_setopt(c, CURLOPT_FOLLOWLOCATION, 0L);
			curl_easy_setopt(c, CURLOPT_CUSTOMREQUEST, q->method);
			if (q->has_body) {
				curl_easy_setopt(c, CURLOPT_POSTFIELDS, q->body);
				curl_easy_setopt(c, CURLOPT_POSTFIELDSIZE_LARGE, (curl_off_t)q->body_len);
			}
		}
		curl_easy_setopt(c, CURLOPT_HTTPHEADER, q->headers);
		code = curl_easy_perform(c);
		if (code == CURLE_OK) {
			pthread_mutex_lock(&net->mu);
			headed(q, c);
			pthread_mutex_unlock(&net->mu);
		}
		/* The cookie jar is written here. */
		curl_easy_cleanup(c);
	}
	int wake = q->wake;
	pthread_mutex_lock(&net->mu);
	q->code = code;
	q->finished = 1;
	net->running--;
	if (!q->posted) {
		q->posted = 1;
		q->next = net->done;
		net->done = q;
	}
	pthread_mutex_unlock(&net->mu);
	jsrt_net_wake(wake);
	return NULL;
}

static const char *failure(fetch_req *q)
{
	if (q->too_big == 1)
		return "the response is over 8 MB";
	if (q->too_big == 2)
		return "the response's headers are over 64 KB";
	if (q->timed_out == 1)
		return "timed out";
	if (q->timed_out == 2)
		return "no data for the timeout while the body was read";
	if (q->code == CURLE_UNSUPPORTED_PROTOCOL)
		return "a redirect away from https:// (only https:// redirects are followed)";
	if (q->code == CURLE_TOO_MANY_REDIRECTS)
		return "more than 5 redirects";
	return q->err[0] ? q->err : curl_easy_strerror(q->code);
}

/* ---- the loop's side ---- */

static void call1(JSContext *ctx, JSValue *fn, JSValue *other, JSValue arg)
{
	JSValue f = *fn;
	*fn = JS_UNDEFINED;
	JS_FreeValue(ctx, *other);
	*other = JS_UNDEFINED;
	JS_FreeValue(ctx, JS_Call(ctx, f, JS_UNDEFINED, 1, (JSValueConst *)&arg));
	JS_FreeValue(ctx, f);
	JS_FreeValue(ctx, arg);
}

static void unlisten(JSContext *ctx, fetch_req *q)
{
	if (!JS_IsUndefined(q->signal))
		jsrt_signal_listen(ctx, q->signal, q->on_abort, 0);
	JS_FreeValue(ctx, q->signal);
	JS_FreeValue(ctx, q->on_abort);
	q->signal = q->on_abort = JS_UNDEFINED;
}

static void req_free(JSContext *ctx, fetch_req *q)
{
	jsrt_net *net = q->net;
	for (fetch_req **p = &net->live; *p; p = &(*p)->lnext)
		if (*p == q) {
			*p = q->lnext;
			break;
		}
	unlisten(ctx, q);
	JSValue *vs[] = {&q->resolve, &q->reject, &q->read_resolve, &q->read_reject, &q->whole, &q->whole_resolve, &q->whole_reject, &q->reason};
	for (size_t i = 0; i < sizeof vs / sizeof vs[0]; i++)
		JS_FreeValue(ctx, *vs[i]);
	free(q->url);
	free(q->method);
	curl_slist_free_all(q->headers);
	free(q->body);
	free(q->final_url);
	free(q->buf.s);
	free(q->head.s);
	pthread_cond_destroy(&q->cv);
	free(q);
}

static int pending(fetch_req *q)
{
	return !JS_IsUndefined(q->resolve) || !JS_IsUndefined(q->read_resolve) || !JS_IsUndefined(q->whole_resolve);
}

/* stop cancels the transfer (the worker sees it at its next callback). */
static void stop(fetch_req *q)
{
	pthread_mutex_lock(&q->net->mu);
	q->cancel = 1;
	pthread_cond_signal(&q->cv);
	pthread_mutex_unlock(&q->net->mu);
}

static JSValue chunk_result(JSContext *ctx, JSValue value, int done)
{
	JSValue o = JS_NewObject(ctx);
	JS_SetPropertyStr(ctx, o, "done", JS_NewBool(ctx, done));
	JS_SetPropertyStr(ctx, o, "value", value);
	return o;
}

/* failed is why q's body cannot be read: the abort's reason, or the
 * transfer's error. */
static JSValue failed(JSContext *ctx, fetch_req *q)
{
	if (!JS_IsUndefined(q->reason))
		return JS_DupValue(ctx, q->reason);
	return type_error(ctx, failure(q));
}

static JSValue response(JSContext *ctx, fetch_req *q);

/* settle hands over what has come for q's promises. */
static void settle(jsrt *r, fetch_req *q)
{
	JSContext *ctx = r->ctx;
	jsrt_net *net = q->net;
	pthread_mutex_lock(&net->mu);
	int headed_now = q->headed, finished = q->finished, ok = finished && q->code == CURLE_OK && !q->too_big;
	pthread_mutex_unlock(&net->mu);
	if (finished)
		q->worker = 0;
	if (!q->responded && (headed_now || finished)) {
		q->responded = 1;
		if (!JS_IsUndefined(q->resolve)) {
			if (headed_now && JS_IsUndefined(q->reason))
				call1(ctx, &q->resolve, &q->reject, response(ctx, q));
			else
				call1(ctx, &q->reject, &q->resolve, failed(ctx, q));
		}
	}
	if (!JS_IsUndefined(q->read_resolve)) {
		pthread_mutex_lock(&net->mu);
		size_t have = q->buf.len - q->taken;
		JSValue v = JS_UNDEFINED;
		if (have) {
			size_t n = have < FETCH_CHUNK ? have : FETCH_CHUNK;
			v = JS_NewUint8ArrayCopy(ctx, (const uint8_t *)q->buf.s + q->taken, n);
			q->taken += n;
			if (q->taken == q->buf.len) {
				q->buf.len = q->taken = 0;
			} else if (q->taken > FETCH_AHEAD) {
				memmove(q->buf.s, q->buf.s + q->taken, q->buf.len - q->taken);
				q->buf.len -= q->taken;
				q->taken = 0;
			}
			q->reading = 0;
			pthread_cond_signal(&q->cv);
		}
		pthread_mutex_unlock(&net->mu);
		if (have) {
			call1(ctx, &q->read_resolve, &q->read_reject, chunk_result(ctx, v, 0));
		} else if (finished && ok) {
			q->body_done = 1;
			call1(ctx, &q->read_resolve, &q->read_reject, chunk_result(ctx, JS_UNDEFINED, 1));
		} else if (finished) {
			q->body_done = 1;
			call1(ctx, &q->read_reject, &q->read_resolve, failed(ctx, q));
		}
	}
	if (!JS_IsUndefined(q->whole_resolve) && finished) {
		q->body_done = 1;
		if (ok && JS_IsUndefined(q->reason)) {
			JSValue ab = JS_NewArrayBufferCopy(ctx, (const uint8_t *)(q->buf.s ? q->buf.s : ""), q->buf.len);
			call1(ctx, &q->whole_resolve, &q->whole_reject, ab);
		} else {
			call1(ctx, &q->whole_reject, &q->whole_resolve, failed(ctx, q));
		}
		free(q->buf.s);
		q->buf = (dstr){0};
		q->taken = 0;
	}
	if (!q->worker && !q->handle && !pending(q))
		req_free(ctx, q);
	else if (!q->handle && !pending(q))
		stop(q);
}

void jsrt_net_drain(jsrt *r)
{
	uint64_t n;
	ssize_t got = read(r->net_fd, &n, sizeof n);
	(void)got;
	pthread_mutex_lock(&r->net->mu);
	fetch_req *list = r->net->done;
	ws_conn *wl = r->net->ws_done;
	r->net->done = NULL;
	r->net->ws_done = NULL;
	for (fetch_req *q = list; q; q = q->next)
		q->posted = 0;
	pthread_mutex_unlock(&r->net->mu);
	fetch_req *ordered = NULL;
	while (list) {
		fetch_req *next = list->next;
		list->next = ordered;
		ordered = list;
		list = next;
	}
	for (fetch_req *q = ordered, *next; q; q = next) {
		next = q->next;
		settle(r, q);
		jsrt_run_jobs(r);
	}
	jsrt_ws_drain(r, wl);
}

/* ---- the body: read whole or as a stream ---- */

static void body_finalizer(JSRuntime *rt, JSValueConst val)
{
	fetch_req *q = JS_GetOpaque(val, body_class);
	if (!q)
		return;
	/* Nobody holds the response: what it was fetching is not wanted.
	 * The loop frees q, with the context its values need. */
	pthread_mutex_lock(&q->net->mu);
	q->handle = 0;
	if (!pending(q))
		q->cancel = 1;
	pthread_cond_signal(&q->cv);
	if (!q->posted) {
		q->posted = 1;
		q->next = q->net->done;
		q->net->done = q;
	}
	pthread_mutex_unlock(&q->net->mu);
	jsrt_net_wake(q->wake);
}

static fetch_req *body_of(JSContext *ctx, JSValueConst v) { return JS_GetOpaque2(ctx, v, body_class); }

static void set_mode(fetch_req *q, int mode)
{
	pthread_mutex_lock(&q->net->mu);
	q->mode = mode;
	if (mode == BODY_STREAM) {
		q->reading = 1;
		q->read_since = jsrt_now_ms();
	}
	pthread_cond_signal(&q->cv);
	pthread_mutex_unlock(&q->net->mu);
}

static JSValue body_read(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	fetch_req *q = body_of(ctx, this_val);
	if (!q)
		return JS_EXCEPTION;
	if (q->mode == BODY_WHOLE)
		return refused(ctx, "the body was read whole (text(), json(), arrayBuffer())");
	if (!JS_IsUndefined(q->read_resolve))
		return refused(ctx, "a read of the body is waiting already");
	if (!JS_IsUndefined(q->reason))
		return settled(ctx, JS_DupValue(ctx, q->reason), 0);
	if (q->body_done)
		return settled(ctx, chunk_result(ctx, JS_UNDEFINED, 1), 1);
	JSValue funcs[2];
	JSValue p = JS_NewPromiseCapability(ctx, funcs);
	if (JS_IsException(p))
		return p;
	q->read_resolve = funcs[0];
	q->read_reject = funcs[1];
	set_mode(q, BODY_STREAM);
	settle(jsrt_of(ctx), q);
	return p;
}

static JSValue body_cancel(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic)
{
	fetch_req *q = body_of(ctx, this_val);
	if (!q)
		return JS_EXCEPTION;
	if (q->mode != BODY_WHOLE) {
		q->mode = BODY_STREAM;
		q->body_done = 1;
		stop(q);
		if (!JS_IsUndefined(q->read_resolve))
			call1(ctx, &q->read_resolve, &q->read_reject, chunk_result(ctx, JS_UNDEFINED, 1));
	}
	return settled(ctx, magic ? chunk_result(ctx, JS_UNDEFINED, 1) : JS_UNDEFINED, 1);
}

static JSValue body_self(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	if (!body_of(ctx, this_val))
		return JS_EXCEPTION;
	return JS_DupValue(ctx, this_val);
}

static JSValue body_release(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv) { return JS_UNDEFINED; }

static JSValue body_locked(JSContext *ctx, JSValueConst this_val)
{
	fetch_req *q = body_of(ctx, this_val);
	return q ? JS_NewBool(ctx, q->mode != BODY_UNDECIDED) : JS_EXCEPTION;
}

static const JSCFunctionListEntry body_proto[] = {
	JS_CFUNC_DEF("read", 0, body_read),
	JS_CFUNC_DEF("next", 0, body_read),
	JS_CFUNC_MAGIC_DEF("cancel", 0, body_cancel, 0),
	JS_CFUNC_MAGIC_DEF("return", 0, body_cancel, 1),
	JS_CFUNC_DEF("getReader", 0, body_self),
	JS_CFUNC_DEF("releaseLock", 0, body_release),
	JS_CFUNC_DEF("[Symbol.asyncIterator]", 0, body_self),
	JS_CGETSET_DEF("locked", body_locked, NULL),
	JS_PROP_STRING_DEF("[Symbol.toStringTag]", "ReadableStream", JS_PROP_CONFIGURABLE),
};

static void body_init(JSContext *ctx)
{
	JSClassDef def = {.class_name = "FetchBody", .finalizer = body_finalizer};
	JS_NewClassID(JS_GetRuntime(ctx), &body_class);
	JS_NewClass(JS_GetRuntime(ctx), body_class, &def);
	JSValue proto = JS_NewObject(ctx);
	JS_SetPropertyFunctionList(ctx, proto, body_proto, sizeof body_proto / sizeof body_proto[0]);
	JS_SetClassProto(ctx, body_class, proto);
}

/* convert turns the whole body (an ArrayBuffer) into what the reader
 * gives: text, JSON, the bytes. */
static JSValue convert(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic, JSValueConst *data)
{
	size_t len = 0;
	const uint8_t *p = argc ? JS_GetArrayBuffer(ctx, &len, argv[0]) : NULL;
	if (!p)
		len = 0;
	switch (magic) {
	case 0:
		return jsrt_decode_utf8(ctx, p, len);
	case 1: {
		char *s = malloc(len + 1);
		if (!s)
			return JS_ThrowOutOfMemory(ctx);
		if (len)
			memcpy(s, p, len);
		s[len] = 0;
		JSValue v = JS_ParseJSON(ctx, s, len, "response");
		free(s);
		return v;
	}
	default:
		return JS_NewArrayBufferCopy(ctx, p ? p : (const uint8_t *)"", len);
	}
}

/* read_whole: text(), json(), arrayBuffer() (data[0] is the body), each a
 * promise, each as often as asked. */
static JSValue read_whole(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic, JSValueConst *data)
{
	fetch_req *q = body_of(ctx, data[0]);
	if (!q)
		return JS_EXCEPTION;
	if (q->mode == BODY_STREAM)
		return refused(ctx, "the body was read as a stream");
	if (JS_IsUndefined(q->whole)) {
		if (!JS_IsUndefined(q->reason))
			return settled(ctx, JS_DupValue(ctx, q->reason), 0);
		JSValue funcs[2];
		q->whole = JS_NewPromiseCapability(ctx, funcs);
		if (JS_IsException(q->whole)) {
			q->whole = JS_UNDEFINED;
			return JS_EXCEPTION;
		}
		q->whole_resolve = funcs[0];
		q->whole_reject = funcs[1];
		pthread_mutex_lock(&q->net->mu);
		if (q->total > FETCH_MAX_BODY)
			q->too_big = 1;
		pthread_mutex_unlock(&q->net->mu);
		set_mode(q, BODY_WHOLE);
		JSValue keep = JS_DupValue(ctx, q->whole);
		settle(jsrt_of(ctx), q);
		JS_FreeValue(ctx, keep);
	}
	JSValue then = JS_GetPropertyStr(ctx, q->whole, "then");
	JSValue fn = JS_NewCFunctionData(ctx, convert, 1, magic, 0, NULL);
	JSValue p = JS_Call(ctx, then, q->whole, 1, (JSValueConst *)&fn);
	JS_FreeValue(ctx, fn);
	JS_FreeValue(ctx, then);
	return p;
}

static JSValue response(JSContext *ctx, fetch_req *q)
{
	JSValue handle = JS_NewObjectClass(ctx, body_class);
	if (JS_IsException(handle))
		return JS_GetException(ctx);
	JS_SetOpaque(handle, q);
	q->handle = 1;
	JSValue res = JS_NewObject(ctx);
	pthread_mutex_lock(&q->net->mu);
	long status = q->status, redirects = q->redirects;
	char *head = q->head.s ? strndup(q->head.s, q->head.len) : NULL;
	pthread_mutex_unlock(&q->net->mu);
	JS_SetPropertyStr(ctx, res, "ok", JS_NewBool(ctx, status >= 200 && status <= 299));
	JS_SetPropertyStr(ctx, res, "status", JS_NewInt32(ctx, (int32_t)status));
	JS_SetPropertyStr(ctx, res, "url", JS_NewString(ctx, q->final_url ? q->final_url : q->url));
	JS_SetPropertyStr(ctx, res, "redirected", JS_NewBool(ctx, redirects > 0));
	JSValue hs = JS_NewObject(ctx);
	char *save = NULL;
	for (char *line = head ? strtok_r(head, "\r\n", &save) : NULL; line; line = strtok_r(NULL, "\r\n", &save)) {
		char *colon = strchr(line, ':');
		if (!colon || colon == line)
			continue;
		*colon = 0;
		for (char *c = line; *c; c++)
			*c = tolower((unsigned char)*c);
		char *value = colon + 1;
		while (*value == ' ' || *value == '\t')
			value++;
		JSValue old = JS_GetPropertyStr(ctx, hs, line);
		dstr joined = {0};
		if (JS_IsString(old)) {
			const char *o = JS_ToCString(ctx, old);
			dstr_puts(&joined, o);
			dstr_puts(&joined, ", ");
			JS_FreeCString(ctx, o);
		}
		dstr_puts(&joined, value);
		JS_SetPropertyStr(ctx, hs, line, JS_NewString(ctx, joined.s));
		free(joined.s);
		JS_FreeValue(ctx, old);
	}
	free(head);
	JS_SetPropertyStr(ctx, res, "headers", hs);
	static const char *const readers[] = {"text", "json", "arrayBuffer"};
	for (int i = 0; i < 3; i++)
		JS_SetPropertyStr(ctx, res, readers[i], JS_NewCFunctionData(ctx, read_whole, 0, i, 1, (JSValueConst *)&handle));
	JS_SetPropertyStr(ctx, res, "body", handle);
	return res;
}

/* on_abort is the request's abort listener (data[0] its id). */
static JSValue on_abort(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic, JSValueConst *data)
{
	jsrt *r = jsrt_of(ctx);
	int64_t id = 0;
	JS_ToInt64(ctx, &id, data[0]);
	fetch_req *q = r->net ? r->net->live : NULL;
	while (q && q->id != id)
		q = q->lnext;
	if (!q || !JS_IsUndefined(q->reason))
		return JS_UNDEFINED;
	JSValue reason = JS_UNDEFINED;
	if (jsrt_signal_state(ctx, q->signal, &reason) <= 0)
		return JS_UNDEFINED;
	q->reason = reason;
	stop(q);
	if (!JS_IsUndefined(q->resolve)) {
		q->responded = 1;
		call1(ctx, &q->reject, &q->resolve, JS_DupValue(ctx, reason));
	}
	if (!JS_IsUndefined(q->read_resolve))
		call1(ctx, &q->read_reject, &q->read_resolve, JS_DupValue(ctx, reason));
	if (!JS_IsUndefined(q->whole_resolve))
		call1(ctx, &q->whole_reject, &q->whole_resolve, JS_DupValue(ctx, reason));
	q->body_done = 1;
	unlisten(ctx, q);
	return JS_UNDEFINED;
}

JSValue jsrt_fetch(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	jsrt *r = jsrt_of(ctx);
	if (permitted <= 0)
		return refused(ctx, "the app's manifest does not ask for the \"net\" permission");
	if (argc < 1 || !JS_IsString(argv[0]))
		return refused(ctx, "the URL is a string");
	jsrt_net *net = jsrt_net_get(r, "fetch");
	if (!net)
		return settled(ctx, JS_GetException(ctx), 0);
	fetch_req *q = calloc(1, sizeof *q);
	if (!q)
		return JS_ThrowOutOfMemory(ctx);
	q->net = net;
	q->wake = r->net_fd;
	q->resolve = q->reject = q->read_resolve = q->read_reject = JS_UNDEFINED;
	q->whole = q->whole_resolve = q->whole_reject = JS_UNDEFINED;
	q->signal = q->on_abort = q->reason = JS_UNDEFINED;
	q->timeout_ms = FETCH_TIMEOUT_MS;
	pthread_cond_init(&q->cv, NULL);
	const char *url = JS_ToCString(ctx, argv[0]);
	const char *bad = url ? jsrt_net_url_check(url, 0) : "the URL is a string";
	if (!bad)
		q->url = strdup(url);
	JS_FreeCString(ctx, url);
	JSValueConst init = argc > 1 ? argv[1] : JS_UNDEFINED;
	int typed = 0, text = 0;
	if (!bad && !JS_IsUndefined(init) && !JS_IsNull(init) && !JS_IsObject(init))
		bad = "the options are an object";
	if (!bad && JS_IsObject(init)) {
		JSValue m = JS_GetPropertyStr(ctx, init, "method");
		if (!JS_IsUndefined(m)) {
			const char *s = JS_IsString(m) ? JS_ToCString(ctx, m) : NULL;
			bad = "method is one of GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS";
			for (size_t i = 0; s && i < sizeof methods / sizeof methods[0]; i++)
				if (!strcasecmp(s, methods[i])) {
					q->method = strdup(methods[i]);
					bad = NULL;
				}
			JS_FreeCString(ctx, s);
		}
		JS_FreeValue(ctx, m);
		JSValue t = JS_GetPropertyStr(ctx, init, "timeout");
		double ms;
		if (!bad && !JS_IsUndefined(t)) {
			if (JS_ToFloat64(ctx, &ms, t) || !(ms >= 1 && ms <= FETCH_TIMEOUT_MAX_MS))
				bad = "timeout is from 1 to 120000 ms";
			else
				q->timeout_ms = (long)ms;
		}
		JS_FreeValue(ctx, t);
		if (!bad) {
			JSValue h = JS_GetPropertyStr(ctx, init, "headers");
			bad = jsrt_net_headers(ctx, h, &q->headers, &typed, 0);
			JS_FreeValue(ctx, h);
		}
		if (!bad) {
			JSValue b = JS_GetPropertyStr(ctx, init, "body");
			bad = body(ctx, b, q, &text);
			JS_FreeValue(ctx, b);
		}
		if (!bad) {
			JSValue c = JS_GetPropertyStr(ctx, init, "credentials");
			const char *s = JS_IsUndefined(c) ? NULL : JS_IsString(c) ? JS_ToCString(ctx, c) : "";
			if (s && strcmp(s, "omit") && strcmp(s, "same-origin") && strcmp(s, "include"))
				bad = "credentials is \"omit\", \"same-origin\" or \"include\"";
			q->cookies = s && !strcmp(s, "include");
			if (s && *s)
				JS_FreeCString(ctx, s);
			JS_FreeValue(ctx, c);
		}
		if (!bad) {
			JSValue s = JS_GetPropertyStr(ctx, init, "signal");
			if (!JS_IsUndefined(s) && !JS_IsNull(s)) {
				if (jsrt_signal_state(ctx, s, NULL) < 0)
					bad = "signal is an AbortSignal";
				else
					q->signal = JS_DupValue(ctx, s);
			}
			JS_FreeValue(ctx, s);
		}
	}
	if (!q->method)
		q->method = strdup("GET");
	if (!bad && q->has_body && (!strcmp(q->method, "GET") || !strcmp(q->method, "HEAD")))
		bad = "a GET or HEAD request has no body";
	if (bad) {
		req_free(ctx, q);
		return refused(ctx, bad);
	}
	JSValue reason = JS_UNDEFINED;
	if (!JS_IsUndefined(q->signal) && jsrt_signal_state(ctx, q->signal, &reason) > 0) {
		req_free(ctx, q);
		return settled(ctx, reason, 0);
	}
	q->follows = !strcmp(q->method, "GET") || !strcmp(q->method, "HEAD") || !strcmp(q->method, "POST");
	/* libcurl would say a form; an app's own Content-Type stands. */
	if (q->has_body && !typed)
		q->headers = curl_slist_append(q->headers, text ? "Content-Type: text/plain;charset=UTF-8" : "Content-Type:");
	q->headers = curl_slist_append(q->headers, "Expect:");
	pthread_mutex_lock(&net->mu);
	int busy = net->running >= FETCH_MAX_RUNNING;
	if (!busy)
		net->running++;
	q->id = ++net->next_id;
	pthread_mutex_unlock(&net->mu);
	if (busy) {
		req_free(ctx, q);
		return refused(ctx, "at most 8 requests at a time");
	}
	JSValue funcs[2];
	JSValue p = JS_NewPromiseCapability(ctx, funcs);
	if (JS_IsException(p)) {
		pthread_mutex_lock(&net->mu);
		net->running--;
		pthread_mutex_unlock(&net->mu);
		req_free(ctx, q);
		return p;
	}
	q->resolve = funcs[0];
	q->reject = funcs[1];
	q->lnext = net->live;
	net->live = q;
	if (!JS_IsUndefined(q->signal)) {
		JSValue id = JS_NewInt64(ctx, q->id);
		q->on_abort = JS_NewCFunctionData(ctx, on_abort, 0, 0, 1, (JSValueConst *)&id);
		jsrt_signal_listen(ctx, q->signal, q->on_abort, 1);
	}
	q->start = jsrt_now_ms();
	q->worker = 1;
	pthread_attr_t attr;
	pthread_attr_init(&attr);
	pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
	pthread_t t;
	int err = pthread_create(&t, &attr, worker, q);
	pthread_attr_destroy(&attr);
	if (err) {
		pthread_mutex_lock(&net->mu);
		net->running--;
		pthread_mutex_unlock(&net->mu);
		q->worker = 0;
		call1(ctx, &q->reject, &q->resolve, type_error(ctx, strerror(err)));
		req_free(ctx, q);
	}
	return p;
}

/* ---- Blob, File, FormData, Headers, Request, Response ---- */

#include "web_js.h"

/* The web's classes and the fetch taking them are web.js (sdk/ts/node,
 * which Node.js runs too), compiled the first time an app uses one: an app
 * that never does pays nothing at start. Until then the module exports
 * stand-ins, whose calls, new, prototype and statics compile it and go to
 * the real ones, which then take the stand-ins' place among the module's
 * exports (an import is a live binding). */
static const char *const web_names[] = {"Blob", "File", "FormData", "Headers", "Request", "Response", "fetch"};
enum { WEB_CLASSES = 6, WEB_FETCH = 6 };
static const char *const web_statics[] = {"prototype", "json", "redirect"};
static JSValue web_api, web_stand[WEB_CLASSES];
static int web_ready;

static JSValue web(JSContext *ctx, int which)
{
	jsrt *r = jsrt_of(ctx);
	if (!web_ready) {
		JSValue factory = JS_Eval(ctx, jsrt_web_js, sizeof jsrt_web_js - 1, "<kario:web>", JS_EVAL_TYPE_GLOBAL);
		if (JS_IsException(factory))
			return factory;
		JSValue core = JS_NewCFunction(ctx, jsrt_fetch, "fetch", 2);
		JSValue api = JS_Call(ctx, factory, JS_UNDEFINED, 1, (JSValueConst *)&core);
		JS_FreeValue(ctx, core);
		JS_FreeValue(ctx, factory);
		if (JS_IsException(api))
			return api;
		web_api = api;
		web_ready = 1;
		for (size_t i = 0; i < sizeof web_names / sizeof web_names[0]; i++)
			JS_SetModuleExport(ctx, r->kario, web_names[i], JS_GetPropertyStr(ctx, api, web_names[i]));
	}
	return JS_GetPropertyStr(ctx, web_api, web_names[which]);
}

static JSValue web_fetch(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv)
{
	JSValue f = web(ctx, WEB_FETCH);
	if (JS_IsException(f))
		return f;
	JSValue ret = JS_Call(ctx, f, JS_UNDEFINED, argc, argv);
	JS_FreeValue(ctx, f);
	return ret;
}

static JSValue web_new(JSContext *ctx, JSValueConst new_target, int argc, JSValueConst *argv, int magic)
{
	JSValue cls = web(ctx, magic);
	if (JS_IsException(cls))
		return cls;
	JSValue ret;
	if (JS_IsUndefined(new_target))
		ret = JS_Call(ctx, cls, JS_UNDEFINED, argc, argv);
	else if (JS_IsSameValue(ctx, new_target, web_stand[magic]))
		ret = JS_CallConstructor(ctx, cls, argc, argv);
	else
		ret = JS_CallConstructor2(ctx, cls, new_target, argc, argv);
	JS_FreeValue(ctx, cls);
	return ret;
}

/* magic: the class, and 16 times the member (web_statics). */
static JSValue web_member(JSContext *ctx, JSValueConst this_val, int argc, JSValueConst *argv, int magic)
{
	JSValue cls = web(ctx, magic & 15);
	if (JS_IsException(cls))
		return cls;
	JSValue v = JS_GetPropertyStr(ctx, cls, web_statics[magic >> 4]);
	JS_FreeValue(ctx, cls);
	return v;
}

void jsrt_web_exports(JSContext *ctx, JSModuleDef *m)
{
	for (int i = 0; i < WEB_CLASSES; i++) {
		JSValue c = JS_NewCFunction2(ctx, (JSCFunction *)(void *)web_new, web_names[i], 0, JS_CFUNC_constructor_or_func_magic, i);
		int members = strcmp(web_names[i], "Response") ? 1 : 3;
		for (int k = 0; k < members; k++) {
			JSAtom a = JS_NewAtom(ctx, web_statics[k]);
			JS_DefinePropertyGetSet(ctx, c, a, JS_NewCFunctionMagic(ctx, web_member, web_statics[k], 0, JS_CFUNC_generic_magic, i | k << 4),
						JS_UNDEFINED, JS_PROP_CONFIGURABLE);
			JS_FreeAtom(ctx, a);
		}
		web_stand[i] = JS_DupValue(ctx, c);
		JS_SetModuleExport(ctx, m, web_names[i], c);
	}
	JS_SetModuleExport(ctx, m, "fetch", JS_NewCFunction(ctx, web_fetch, "fetch", 2));
}
