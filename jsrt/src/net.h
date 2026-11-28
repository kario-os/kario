/* What fetch (net.c) and WebSocket (ws.c) share: the permission, the URL
 * and header rules, the workers' list for the event loop, the cookie jar. */
#ifndef KARIO_JSRT_NET_H
#define KARIO_JSRT_NET_H

#include <pthread.h>

#include <curl/curl.h>

#include "fetchhead.h"
#include "jsrt.h"

/* A CA bundle in place of the system's, for tests; the Shell never sets it,
 * and an app cannot set its own environment. */
#define FETCH_CA_ENV "KARIO_JSRT_CA_FILE"
#define FETCH_MAX_URL 8192
#define FETCH_MAX_HEADERS 64

typedef struct fetch_req fetch_req;
typedef struct ws_conn ws_conn;

struct jsrt_net {
	pthread_mutex_t mu;
	fetch_req *done; /* requests with news for the loop */
	int running;
	ws_conn *ws_done;
	int ws_open;
	long next_id;
	/* The loop's own lists, for an abort listener to find its request. */
	fetch_req *live;
	ws_conn *ws_live;
	/* The keepalive WebSockets (ws.c), the loop's: the id of the one whose
	 * socket the Shell holds (0: none) and whether that one has ended; the
	 * ones waiting for their socket, oldest first; the id the call that is
	 * out (at most one) asks for. */
	long keep_cur, keep_out;
	int keep_ended;
	ws_conn *keep_wait;
	/* Cookies (credentials: "include"), shared by the workers' handles. */
	CURLSH *share;
	pthread_mutex_t share_mu;
	int jar_loaded;
	char *jar;
};

/* jsrt_net_get is the app's network state, made on first use; NULL with
 * the reason thrown when the app may not use the network. */
jsrt_net *jsrt_net_get(jsrt *r, const char *what);
/* The rules a URL obeys: NULL when it may go. ws: a WebSocket's (wss://,
 * ws:// to the loopback; no fragment). */
const char *jsrt_net_url_check(const char *url, int ws);
/* jsrt_net_headers adds the app's headers (an object of strings) to *list;
 * NULL when they may go. typed notes a Content-Type; ws refuses the
 * handshake's own (Sec-WebSocket-*). */
const char *jsrt_net_headers(JSContext *ctx, JSValueConst obj, struct curl_slist **list, int *typed, int ws);
/* jsrt_net_curl sets what every handle of the app has: the CA bundle of
 * tests, the protocols. */
void jsrt_net_curl(CURL *c);
/* jsrt_net_wake wakes the loop's poll (the eventfd). */
void jsrt_net_wake(int fd);
/* jsrt_net_keepalive: the manifest asks for "keepalive" too. */
int jsrt_net_keepalive(void);

/* ws.c: the WebSocket events that came, for the loop. */
void jsrt_ws_drain(jsrt *r, ws_conn *list);
/* The teardown (jsrt_net_stop): jsrt_ws_stop drops every open connection,
 * jsrt_ws_busy says whether a worker still has one, and jsrt_ws_release
 * lets go of those whose worker finished, which their objects' finalizers
 * then free with the runtime. */
void jsrt_ws_stop(jsrt *r);
int jsrt_ws_busy(jsrt_net *net);
void jsrt_ws_release(jsrt *r);

#endif
