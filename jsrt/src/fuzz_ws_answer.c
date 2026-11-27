/* Fuzz harness of kario-jsrt's check of a WebSocket server's answer to the
 * opening handshake (tools/cfuzz): the input's first byte picks the
 * protocols the client offered, the rest is what the server sent, cut at
 * its first blank line as ws.c cuts it. Built with the sanitizers; a
 * report, a crash or a broken invariant fails the input. An answer taken
 * has, by a parse of the harness's own:
 *   - a Sec-WebSocket-Accept that is the one for the client's key;
 *   - no Sec-WebSocket-Extensions (the client offers none);
 *   - no protocol if the client offered none, else exactly one it offered.
 * An answer refused gives a reason and no protocol.
 * Built with KARIO_LIBFUZZER defined it is a libFuzzer target instead
 * (make fuzz-libfuzzer). */
#include "wsframe.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>

static void check(int ok, const char *what)
{
	if (!ok) {
		fprintf(stderr, "fuzz_ws_answer: %s\n", what);
		abort();
	}
}

/* header finds the value of the first header line named name, trimmed. */
static const char *header(const char *head, const char *name, size_t *len)
{
	size_t n = strlen(name);
	const char *l = strstr(head, "\r\n");
	for (; l && l[2]; l = strstr(l + 2, "\r\n")) {
		const char *s = l + 2, *e = strstr(s, "\r\n");
		const char *colon = memchr(s, ':', (size_t)(e - s));
		if (!colon || (size_t)(colon - s) != n || strncasecmp(s, name, n))
			continue;
		s = colon + 1;
		while (s < e && (*s == ' ' || *s == '\t'))
			s++;
		while (e > s && (e[-1] == ' ' || e[-1] == '\t'))
			e--;
		*len = (size_t)(e - s);
		return s;
	}
	return NULL;
}

/* offered_one says whether p is exactly one of the ", "-separated list. */
static int offered_one(const char *list, const char *p)
{
	size_t n = strlen(p);
	for (const char *s = list; s;) {
		const char *e = strstr(s, ", ");
		size_t k = e ? (size_t)(e - s) : strlen(s);
		if (k == n && !memcmp(s, p, n))
			return 1;
		s = e ? e + 2 : NULL;
	}
	return 0;
}

static void answer(const uint8_t *in, size_t n)
{
	static const char *const offers[] = {NULL, "chat", "chat, superchat", "v1.kario, v2.kario"};
	if (!n)
		return;
	const char *offered = offers[in[0] % 4];
	char *got = malloc(n);
	check(got != NULL, "out of memory");
	memcpy(got, in + 1, n - 1);
	got[n - 1] = 0;
	char *end = strstr(got, "\r\n\r\n");
	if (!end) {
		free(got);
		return;
	}
	char *head = got;
	head[end + 2 - got] = 0;
	char want[29];
	ws_accept("dGhlIHNhbXBsZSBub25jZQ==", want);
	char *protocol = (char *)1;
	const char *why = ws_check_answer(head, want, offered, &protocol);
	if (why) {
		check(*why && !protocol, "refused without a reason, or with a protocol");
	} else {
		size_t len;
		const char *v = header(head, "sec-websocket-accept", &len);
		check(v && len == 28 && !memcmp(v, want, 28), "taken with a wrong Sec-WebSocket-Accept");
		check(!header(head, "sec-websocket-extensions", &len), "taken with an extension");
		check(!protocol || (offered && offered_one(offered, protocol)), "taken with a protocol the client did not offer");
		check(protocol || !header(head, "sec-websocket-protocol", &len), "a protocol the server chose is lost");
		free(protocol);
	}
	free(head);
}

#ifdef KARIO_LIBFUZZER
int LLVMFuzzerTestOneInput(const uint8_t *data, size_t len)
{
	answer(data, len);
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
	answer(in, n);
	free(in);
	return 0;
}
#endif
