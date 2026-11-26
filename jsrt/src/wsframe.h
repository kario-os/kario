/* WebSocket framing (RFC 6455) for kario-jsrt's client (ws.c): the reader of
 * what a server sends, which is untrusted input with limits of its own,
 * the writer of what the app sends, and the opening handshake's key. No
 * engine and no network here, so the fuzz harness (fuzz_ws.c) links it as
 * it is. */
#ifndef KARIO_WSFRAME_H
#define KARIO_WSFRAME_H

#include <stddef.h>
#include <stdint.h>

/* The largest message the reader assembles (text or binary, all its
 * fragments together); a larger one fails the connection with 1009. */
#define WS_MAX_MESSAGE (4u << 20)

enum {
	WS_OP_CONT = 0,
	WS_OP_TEXT = 1,
	WS_OP_BINARY = 2,
	WS_OP_CLOSE = 8,
	WS_OP_PING = 9,
	WS_OP_PONG = 10,
};

/* What ws_feed found. */
enum {
	WS_EV_NONE,   /* nothing whole yet */
	WS_EV_TEXT,   /* a text message (valid UTF-8): data, len */
	WS_EV_BINARY, /* a binary message: data, len */
	WS_EV_PING,   /* data, len (at most 125) */
	WS_EV_PONG,
	WS_EV_CLOSE, /* code (1005 when the frame had none), reason (UTF-8), len */
	WS_EV_ERROR, /* the server broke the protocol: code (1002, 1007, 1009) and why */
};

typedef struct {
	int kind;
	const uint8_t *data; /* valid until the next ws_feed */
	size_t len;
	int code;
	const char *why;
} ws_event;

typedef struct {
	size_t max_message;
	/* the frame being read */
	uint8_t head[14];
	size_t nhead;
	int opcode, fin;
	uint64_t left; /* payload bytes still to come */
	uint8_t ctl[125];
	size_t nctl;
	/* the message being assembled */
	int msg; /* its opcode, 0 when none */
	uint8_t *buf;
	size_t len, cap;
	int failed;
} ws_reader;

void ws_reader_init(ws_reader *r, size_t max_message);
void ws_reader_free(ws_reader *r);
/* ws_feed reads from p (n bytes) up to the end of the next message or
 * control frame: the bytes it took, and in ev what it found. After an
 * error it takes nothing more. */
size_t ws_feed(ws_reader *r, const uint8_t *p, size_t n, ws_event *ev);

/* ws_frame writes a client frame (masked with mask) of payload into out,
 * which has room for len + 14 bytes: the frame's length. */
size_t ws_frame(uint8_t *out, int opcode, const uint8_t *payload, size_t len, const uint8_t mask[4]);

/* ws_accept is the Sec-WebSocket-Accept a server answers key with
 * (base64 of SHA-1 of key and the protocol's GUID): 28 characters and a
 * terminating zero. */
void ws_accept(const char *key, char out[29]);

/* ws_base64 encodes n bytes of p into out (4 * ((n + 2) / 3) + 1 bytes). */
void ws_base64(const uint8_t *p, size_t n, char *out);

/* ws_utf8 says whether p (n bytes) is valid UTF-8. */
int ws_utf8(const uint8_t *p, size_t n);

#endif
