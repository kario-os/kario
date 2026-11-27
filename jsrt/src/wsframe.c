/* WebSocket framing for kario-jsrt's client (see wsframe.h). */
#include "wsframe.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

void ws_reader_init(ws_reader *r, size_t max_message)
{
	memset(r, 0, sizeof *r);
	r->max_message = max_message ? max_message : WS_MAX_MESSAGE;
	r->opcode = -1;
}

void ws_reader_free(ws_reader *r)
{
	free(r->buf);
	r->buf = NULL;
	r->len = r->cap = 0;
}

int ws_utf8(const uint8_t *p, size_t n)
{
	for (size_t i = 0; i < n;) {
		uint8_t c = p[i];
		if (c < 0x80) {
			i++;
			continue;
		}
		int need;
		uint8_t lo = 0x80, hi = 0xbf;
		if (c >= 0xc2 && c <= 0xdf) {
			need = 1;
		} else if (c >= 0xe0 && c <= 0xef) {
			need = 2;
			if (c == 0xe0)
				lo = 0xa0;
			if (c == 0xed)
				hi = 0x9f;
		} else if (c >= 0xf0 && c <= 0xf4) {
			need = 3;
			if (c == 0xf0)
				lo = 0x90;
			if (c == 0xf4)
				hi = 0x8f;
		} else {
			return 0;
		}
		if ((size_t)need >= n - i)
			return 0;
		for (int k = 1; k <= need; k++) {
			if (p[i + k] < lo || p[i + k] > hi)
				return 0;
			lo = 0x80;
			hi = 0xbf;
		}
		i += (size_t)need + 1;
	}
	return 1;
}

static size_t fail(ws_reader *r, ws_event *ev, int code, const char *why, size_t used)
{
	r->failed = 1;
	ev->kind = WS_EV_ERROR;
	ev->code = code;
	ev->why = why;
	return used;
}

static int close_code_ok(int c)
{
	return (c >= 1000 && c <= 1003) || (c >= 1007 && c <= 1014) || (c >= 3000 && c <= 4999);
}

/* complete handles the end of a frame's payload: 1 when it made an event
 * (or failed), 0 when a fragmented message goes on. */
static int complete(ws_reader *r, ws_event *ev)
{
	int op = r->opcode;
	r->opcode = -1;
	switch (op) {
	case WS_OP_PING:
	case WS_OP_PONG:
		ev->kind = op == WS_OP_PING ? WS_EV_PING : WS_EV_PONG;
		ev->data = r->ctl;
		ev->len = r->nctl;
		return 1;
	case WS_OP_CLOSE:
		if (r->nctl == 1) {
			fail(r, ev, 1002, "a close frame of one byte", 0);
			return 1;
		}
		ev->code = 1005;
		ev->data = r->ctl;
		ev->len = 0;
		if (r->nctl >= 2) {
			ev->code = r->ctl[0] << 8 | r->ctl[1];
			ev->data = r->ctl + 2;
			ev->len = r->nctl - 2;
			if (!close_code_ok(ev->code)) {
				fail(r, ev, 1002, "a close code no endpoint sends", 0);
				return 1;
			}
			if (!ws_utf8(ev->data, ev->len)) {
				fail(r, ev, 1007, "a close reason that is not UTF-8", 0);
				return 1;
			}
		}
		ev->kind = WS_EV_CLOSE;
		/* Nothing follows a close frame. */
		r->failed = 1;
		return 1;
	}
	if (!r->fin)
		return 0;
	if (r->msg == WS_OP_TEXT && !ws_utf8(r->buf, r->len)) {
		fail(r, ev, 1007, "a text message that is not UTF-8", 0);
		return 1;
	}
	ev->kind = r->msg == WS_OP_TEXT ? WS_EV_TEXT : WS_EV_BINARY;
	ev->data = r->buf ? r->buf : (const uint8_t *)"";
	ev->len = r->len;
	r->msg = 0;
	return 1;
}

/* header parses the frame header in r->head: -1 when more bytes are
 * needed, 0 when the payload starts, 1 when it failed. */
static int header(ws_reader *r, ws_event *ev)
{
	size_t need = 2;
	if (r->nhead >= 2) {
		int len7 = r->head[1] & 0x7f;
		need += len7 == 126 ? 2 : len7 == 127 ? 8 : 0;
		if (r->head[1] & 0x80)
			need += 4;
	}
	if (r->nhead < need)
		return -1;
	int fin = r->head[0] >> 7, op = r->head[0] & 0x0f;
	if (r->head[0] & 0x70) {
		fail(r, ev, 1002, "a frame with reserved bits set (no extension was agreed)", 0);
		return 1;
	}
	if (r->head[1] & 0x80) {
		fail(r, ev, 1002, "a masked frame from the server", 0);
		return 1;
	}
	uint64_t len = r->head[1] & 0x7f;
	if (len == 126) {
		len = (uint64_t)r->head[2] << 8 | r->head[3];
	} else if (len == 127) {
		len = 0;
		for (int i = 0; i < 8; i++)
			len = len << 8 | r->head[2 + i];
		if (len >> 63) {
			fail(r, ev, 1002, "a frame length over 2^63", 0);
			return 1;
		}
	}
	if (op & 8) {
		if (op != WS_OP_CLOSE && op != WS_OP_PING && op != WS_OP_PONG) {
			fail(r, ev, 1002, "a frame with an unknown opcode", 0);
			return 1;
		}
		if (!fin) {
			fail(r, ev, 1002, "a fragmented control frame", 0);
			return 1;
		}
		if (len > sizeof r->ctl) {
			fail(r, ev, 1002, "a control frame over 125 bytes", 0);
			return 1;
		}
		r->nctl = 0;
	} else {
		if (op == WS_OP_CONT && !r->msg) {
			fail(r, ev, 1002, "a continuation frame without a message", 0);
			return 1;
		}
		if ((op == WS_OP_TEXT || op == WS_OP_BINARY) && r->msg) {
			fail(r, ev, 1002, "a new message before the last one ended", 0);
			return 1;
		}
		if (op != WS_OP_CONT && op != WS_OP_TEXT && op != WS_OP_BINARY) {
			fail(r, ev, 1002, "a frame with an unknown opcode", 0);
			return 1;
		}
		if (op != WS_OP_CONT) {
			r->msg = op;
			r->len = 0;
		}
		if (len > r->max_message - r->len) {
			fail(r, ev, 1009, "a message over the limit", 0);
			return 1;
		}
	}
	r->fin = fin;
	r->opcode = op;
	r->left = len;
	r->nhead = 0;
	return 0;
}

static int append(ws_reader *r, const uint8_t *p, size_t n)
{
	if (r->len + n > r->cap) {
		size_t cap = r->cap ? r->cap : 4096;
		while (cap < r->len + n)
			cap *= 2;
		if (cap > r->max_message)
			cap = r->max_message;
		uint8_t *b = realloc(r->buf, cap);
		if (!b)
			return -1;
		r->buf = b;
		r->cap = cap;
	}
	memcpy(r->buf + r->len, p, n);
	r->len += n;
	return 0;
}

size_t ws_feed(ws_reader *r, const uint8_t *p, size_t n, ws_event *ev)
{
	memset(ev, 0, sizeof *ev);
	size_t used = 0;
	if (r->failed)
		return 0;
	/* The last event's data is gone now: a big message's memory with it. */
	if (!r->msg && r->cap > (64u << 10))
		ws_reader_free(r);
	for (;;) {
		if (r->opcode < 0) {
			if (used == n)
				return used;
			r->head[r->nhead++] = p[used++];
			int h = header(r, ev);
			if (h < 0)
				continue;
			if (h > 0)
				return used;
		}
		size_t take = n - used < r->left ? n - used : (size_t)r->left;
		if (r->opcode & 8) {
			memcpy(r->ctl + r->nctl, p + used, take);
			r->nctl += take;
		} else if (take && append(r, p + used, take) < 0) {
			return fail(r, ev, 1009, "out of memory for a message", used);
		}
		used += take;
		r->left -= take;
		if (r->left)
			return used;
		if (complete(r, ev))
			return used;
	}
}

size_t ws_frame(uint8_t *out, int opcode, const uint8_t *payload, size_t len, const uint8_t mask[4])
{
	size_t h = 2;
	out[0] = (uint8_t)(0x80 | opcode);
	if (len < 126) {
		out[1] = (uint8_t)(0x80 | len);
	} else if (len < 65536) {
		out[1] = 0x80 | 126;
		out[2] = (uint8_t)(len >> 8);
		out[3] = (uint8_t)len;
		h = 4;
	} else {
		out[1] = 0x80 | 127;
		for (int i = 0; i < 8; i++)
			out[2 + i] = (uint8_t)((uint64_t)len >> (56 - 8 * i));
		h = 10;
	}
	memcpy(out + h, mask, 4);
	h += 4;
	for (size_t i = 0; i < len; i++)
		out[h + i] = payload[i] ^ mask[i & 3];
	return h + len;
}

/* ---- the handshake's key: SHA-1 and base64 ---- */

static uint32_t rol(uint32_t x, int n) { return x << n | x >> (32 - n); }

static void sha1_block(uint32_t h[5], const uint8_t *b)
{
	uint32_t w[80];
	for (int i = 0; i < 16; i++)
		w[i] = (uint32_t)b[4 * i] << 24 | (uint32_t)b[4 * i + 1] << 16 | (uint32_t)b[4 * i + 2] << 8 | b[4 * i + 3];
	for (int i = 16; i < 80; i++)
		w[i] = rol(w[i - 3] ^ w[i - 8] ^ w[i - 14] ^ w[i - 16], 1);
	uint32_t a = h[0], bb = h[1], c = h[2], d = h[3], e = h[4];
	for (int i = 0; i < 80; i++) {
		uint32_t f, k;
		if (i < 20) {
			f = (bb & c) | (~bb & d);
			k = 0x5a827999;
		} else if (i < 40) {
			f = bb ^ c ^ d;
			k = 0x6ed9eba1;
		} else if (i < 60) {
			f = (bb & c) | (bb & d) | (c & d);
			k = 0x8f1bbcdc;
		} else {
			f = bb ^ c ^ d;
			k = 0xca62c1d6;
		}
		uint32_t t = rol(a, 5) + f + e + k + w[i];
		e = d;
		d = c;
		c = rol(bb, 30);
		bb = a;
		a = t;
	}
	h[0] += a;
	h[1] += bb;
	h[2] += c;
	h[3] += d;
	h[4] += e;
}

static void sha1(const uint8_t *p, size_t n, uint8_t out[20])
{
	uint32_t h[5] = {0x67452301, 0xefcdab89, 0x98badcfe, 0x10325476, 0xc3d2e1f0};
	size_t i = 0;
	for (; i + 64 <= n; i += 64)
		sha1_block(h, p + i);
	uint8_t last[128] = {0};
	size_t rest = n - i;
	memcpy(last, p + i, rest);
	last[rest] = 0x80;
	size_t blocks = rest + 9 > 64 ? 2 : 1;
	uint64_t bits = (uint64_t)n * 8;
	for (int k = 0; k < 8; k++)
		last[blocks * 64 - 1 - k] = (uint8_t)(bits >> (8 * k));
	for (size_t b = 0; b < blocks; b++)
		sha1_block(h, last + 64 * b);
	for (int k = 0; k < 5; k++) {
		out[4 * k] = (uint8_t)(h[k] >> 24);
		out[4 * k + 1] = (uint8_t)(h[k] >> 16);
		out[4 * k + 2] = (uint8_t)(h[k] >> 8);
		out[4 * k + 3] = (uint8_t)h[k];
	}
}

void ws_base64(const uint8_t *p, size_t n, char *out)
{
	static const char abc[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
	size_t o = 0;
	for (size_t i = 0; i < n; i += 3) {
		uint32_t v = (uint32_t)p[i] << 16 | (i + 1 < n ? (uint32_t)p[i + 1] << 8 : 0) | (i + 2 < n ? p[i + 2] : 0);
		out[o++] = abc[v >> 18 & 63];
		out[o++] = abc[v >> 12 & 63];
		out[o++] = i + 1 < n ? abc[v >> 6 & 63] : '=';
		out[o++] = i + 2 < n ? abc[v & 63] : '=';
	}
	out[o] = 0;
}

void ws_accept(const char *key, char out[29])
{
	static const char guid[] = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
	uint8_t buf[128], sum[20];
	size_t k = strlen(key);
	if (k > sizeof buf - sizeof guid)
		k = sizeof buf - sizeof guid;
	memcpy(buf, key, k);
	memcpy(buf + k, guid, sizeof guid - 1);
	sha1(buf, k + sizeof guid - 1, sum);
	ws_base64(sum, 20, out);
}

/* header_value finds a response header's value (its line ends at \r\n). */
static const char *header_value(const char *head, const char *name, size_t *len)
{
	size_t n = strlen(name);
	for (const char *l = strstr(head, "\r\n"); l && l[2]; l = strstr(l + 2, "\r\n")) {
		const char *s = l + 2;
		if (!strncasecmp(s, name, n) && s[n] == ':') {
			s += n + 1;
			while (*s == ' ' || *s == '\t')
				s++;
			const char *e = strstr(s, "\r\n");
			while (e > s && (e[-1] == ' ' || e[-1] == '\t'))
				e--;
			*len = (size_t)(e - s);
			return s;
		}
	}
	return NULL;
}

int ws_has_token(const char *v, size_t n, const char *tok)
{
	size_t t = strlen(tok);
	for (size_t i = 0; i + t <= n; i++)
		if (!strncasecmp(v + i, tok, t) && (i == 0 || v[i - 1] == ',' || v[i - 1] == ' ') &&
		    (i + t == n || v[i + t] == ',' || v[i + t] == ' '))
			return 1;
	return 0;
}

/* offered_one says whether p (n bytes) is exactly one of the protocols the
 * client offered, which are tokens joined with ", ". */
static int offered_one(const char *offered, const char *p, size_t n)
{
	for (const char *s = offered; n;) {
		const char *e = strstr(s, ", ");
		size_t k = e ? (size_t)(e - s) : strlen(s);
		if (k == n && !memcmp(s, p, n))
			return 1;
		if (!e)
			return 0;
		s = e + 2;
	}
	return 0;
}

const char *ws_check_answer(const char *head, const char want[29], const char *offered, char **protocol)
{
	*protocol = NULL;
	int status = 0;
	const char *why = NULL;
	size_t n;
	const char *v;
	if (sscanf(head, "HTTP/1.%*d %d", &status) != 1 || status != 101) {
		why = "the server did not switch to WebSocket (no 101 answer)";
	} else if (!(v = header_value(head, "upgrade", &n)) || !ws_has_token(v, n, "websocket")) {
		why = "the server's answer has no Upgrade: websocket";
	} else if (!(v = header_value(head, "connection", &n)) || !ws_has_token(v, n, "upgrade")) {
		why = "the server's answer has no Connection: Upgrade";
	} else if (!(v = header_value(head, "sec-websocket-accept", &n)) || n != 28 || memcmp(v, want, 28)) {
		why = "the server's Sec-WebSocket-Accept is wrong";
	} else if (header_value(head, "sec-websocket-extensions", &n)) {
		why = "the server chose an extension the client did not offer";
	} else if ((v = header_value(head, "sec-websocket-protocol", &n))) {
		char *p = offered && offered_one(offered, v, n) ? malloc(n + 1) : NULL;
		if (!p) {
			why = "the server chose a protocol the client did not offer";
		} else {
			memcpy(p, v, n);
			p[n] = 0;
			*protocol = p;
		}
	}
	return why;
}
