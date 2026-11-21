/* Fuzz harness of kario-jsrt's WebSocket reader (tools/cfuzz): what a
 * server sends, from standard input, fed to the reader in pieces whose
 * sizes the input's first byte picks. Built with the sanitizers; a report,
 * a crash or a broken invariant fails the input:
 *   - every message is within the limit, a text message is UTF-8;
 *   - a close code is one an endpoint may send, or 1005;
 *   - after an error or a close the reader takes nothing more;
 *   - a message framed again (as a server frames it) reads back the same.
 * Built with KARIO_LIBFUZZER defined it is a libFuzzer target instead
 * (make fuzz-libfuzzer). */
#include "wsframe.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define LIMIT (64u << 10)

static void check(int ok, const char *what)
{
	if (!ok) {
		fprintf(stderr, "fuzz_ws: %s\n", what);
		abort();
	}
}

/* again frames a message as a server does (unmasked) and reads it back. */
static void again(int kind, const uint8_t *p, size_t n)
{
	uint8_t *f = malloc(n + 14);
	check(f != NULL, "out of memory");
	size_t h = 2;
	f[0] = (uint8_t)(0x80 | (kind == WS_EV_TEXT ? WS_OP_TEXT : WS_OP_BINARY));
	if (n < 126) {
		f[1] = (uint8_t)n;
	} else if (n < 65536) {
		f[1] = 126;
		f[2] = (uint8_t)(n >> 8);
		f[3] = (uint8_t)n;
		h = 4;
	} else {
		f[1] = 127;
		for (int i = 0; i < 8; i++)
			f[2 + i] = (uint8_t)((uint64_t)n >> (56 - 8 * i));
		h = 10;
	}
	memcpy(f + h, p, n);
	ws_reader r;
	ws_reader_init(&r, LIMIT);
	ws_event ev;
	size_t used = ws_feed(&r, f, h + n, &ev);
	check(used == h + n && ev.kind == kind && ev.len == n && (!n || !memcmp(ev.data, p, n)), "a message framed again reads back differently");
	ws_reader_free(&r);
	free(f);
}

/* frames feeds the reader what a server sent, in pieces. */
static void frames(const uint8_t *in, size_t n)
{
	if (!n)
		return;
	size_t piece = in[0] % 7 == 0 ? n : (size_t)(in[0] % 7) * (in[0] % 13 + 1);
	ws_reader r;
	ws_reader_init(&r, LIMIT);
	int over = 0;
	for (size_t at = 1; at < n;) {
		size_t len = n - at < piece ? n - at : piece;
		ws_event ev;
		size_t used = ws_feed(&r, in + at, len, &ev);
		check(used <= len, "took more than it was given");
		if (over) {
			check(used == 0 && ev.kind == WS_EV_NONE, "read on after an error or a close");
			break;
		}
		switch (ev.kind) {
		case WS_EV_TEXT:
			check(ws_utf8(ev.data, ev.len), "a text message that is not UTF-8");
			/* fall through */
		case WS_EV_BINARY:
			check(ev.len <= LIMIT, "a message over the limit");
			again(ev.kind, ev.data, ev.len);
			break;
		case WS_EV_PING:
		case WS_EV_PONG:
			check(ev.len <= 125, "a control frame over 125 bytes");
			break;
		case WS_EV_CLOSE:
			check(ev.code == 1005 || (ev.code >= 1000 && ev.code < 5000), "a close code out of range");
			over = 1;
			break;
		case WS_EV_ERROR:
			check(ev.code == 1002 || ev.code == 1007 || ev.code == 1009, "an error code out of range");
			over = 1;
			break;
		case WS_EV_NONE:
			check(used == len, "stopped without an event");
			break;
		}
		at += used;
		if (!used && !over)
			break;
	}
	ws_reader_free(&r);
}

#ifdef KARIO_LIBFUZZER
int LLVMFuzzerTestOneInput(const uint8_t *data, size_t len)
{
	frames(data, len);
	return 0;
}
#else
int main(void)
{
	size_t cap = 1 << 16, n = 0;
	uint8_t *in = malloc(cap);
	check(in != NULL, "out of memory");
	size_t got;
	while ((got = fread(in + n, 1, cap - n, stdin)) > 0) {
		n += got;
		if (n == cap) {
			cap *= 2;
			in = realloc(in, cap);
			check(in != NULL, "out of memory");
		}
	}
	frames(in, n);
	free(in);
	return 0;
}
#endif
