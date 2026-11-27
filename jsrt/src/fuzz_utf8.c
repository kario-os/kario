/* Fuzz harness of kario-jsrt's TextDecoder (utf8dec.c, tools/cfuzz): the
 * input's first byte picks fatal (bit 0), ignoreBOM (bit 1) and the size
 * of the streamed chunks (the rest, 0 for one call without stream); the
 * rest is the bytes. The text decoded in those chunks, the text of one
 * call, and the text of the same decoder used once more must all be the
 * WHATWG decoder's, by the harness's own reading of its algorithm (the
 * Encoding Standard's UTF-8 decoder, step by step), refused with fatal
 * when and only when that has an error. Built with the sanitizers; a
 * report, a crash or a broken invariant fails the input. On stdout: T and
 * the text, or E when refused, for the Go driver to hold against Node.js's
 * TextDecoder. Built with KARIO_LIBFUZZER defined it is a libFuzzer target
 * instead (make fuzz-libfuzzer). */
#include "utf8dec.h"

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void check(int ok, const char *what)
{
	if (!ok) {
		fprintf(stderr, "fuzz_utf8: %s\n", what);
		abort();
	}
}

typedef struct {
	char *s;
	size_t len, cap;
	int refused;
} text;

static void put(text *t, const char *p, size_t n)
{
	if (t->len + n + 1 > t->cap) {
		t->cap = (t->len + n + 1) * 2;
		t->s = realloc(t->s, t->cap);
		check(t->s != NULL, "out of memory");
	}
	memcpy(t->s + t->len, p, n);
	t->len += n;
	t->s[t->len] = 0;
}

static void put_code_point(text *t, uint32_t c)
{
	char b[4];
	size_t n;
	if (c < 0x80) {
		b[0] = (char)c;
		n = 1;
	} else if (c < 0x800) {
		b[0] = (char)(0xc0 | c >> 6);
		b[1] = (char)(0x80 | (c & 0x3f));
		n = 2;
	} else if (c < 0x10000) {
		b[0] = (char)(0xe0 | c >> 12);
		b[1] = (char)(0x80 | (c >> 6 & 0x3f));
		b[2] = (char)(0x80 | (c & 0x3f));
		n = 3;
	} else {
		b[0] = (char)(0xf0 | c >> 18);
		b[1] = (char)(0x80 | (c >> 12 & 0x3f));
		b[2] = (char)(0x80 | (c >> 6 & 0x3f));
		b[3] = (char)(0x80 | (c & 0x3f));
		n = 4;
	}
	put(t, b, n);
}

/* whatwg is the Encoding Standard's UTF-8 decoder on all of p, and
 * TextDecoder's dropping of a leading U+FEFF unless ignore_bom. */
static text whatwg(const unsigned char *p, size_t n, int fatal, int ignore_bom)
{
	text t = {0};
	put(&t, "", 0);
	uint32_t cp = 0;
	int needed = 0, seen = 0, bom = !ignore_bom, errors = 0;
	unsigned lower = 0x80, upper = 0xbf;
	for (size_t i = 0; i <= n; i++) {
		if (i == n) {
			if (needed) {
				errors++;
				put_code_point(&t, 0xfffd);
			}
			break;
		}
		unsigned b = p[i];
		if (!needed) {
			if (b <= 0x7f) {
				put_code_point(&t, b);
			} else if (b >= 0xc2 && b <= 0xdf) {
				needed = 1;
				cp = b & 0x1f;
			} else if (b >= 0xe0 && b <= 0xef) {
				if (b == 0xe0)
					lower = 0xa0;
				if (b == 0xed)
					upper = 0x9f;
				needed = 2;
				cp = b & 0xf;
			} else if (b >= 0xf0 && b <= 0xf4) {
				if (b == 0xf0)
					lower = 0x90;
				if (b == 0xf4)
					upper = 0x8f;
				needed = 3;
				cp = b & 0x7;
			} else {
				errors++;
				put_code_point(&t, 0xfffd);
			}
			continue;
		}
		if (b < lower || b > upper) {
			cp = 0;
			needed = seen = 0;
			lower = 0x80;
			upper = 0xbf;
			i--; /* the byte goes back to the queue */
			errors++;
			put_code_point(&t, 0xfffd);
			continue;
		}
		lower = 0x80;
		upper = 0xbf;
		cp = cp << 6 | (b & 0x3f);
		if (++seen != needed)
			continue;
		put_code_point(&t, cp);
		cp = 0;
		needed = seen = 0;
	}
	if (bom && t.len >= 3 && !memcmp(t.s, "\xef\xbb\xbf", 3)) {
		memmove(t.s, t.s + 3, t.len - 2);
		t.len -= 3;
	}
	t.refused = fatal && errors;
	return t;
}

/* decode runs d over p in chunks of k bytes streamed and a last call
 * without stream (k 0: one call without stream). */
static text decode(utf8_decoder *d, const unsigned char *p, size_t n, size_t k)
{
	text t = {0};
	put(&t, "", 0);
	for (size_t at = 0; k && at < n; at += k) {
		char *s;
		size_t len;
		int r = utf8_decode(d, p + at, n - at < k ? n - at : k, 1, &s, &len);
		check(r >= 0, "out of memory");
		check(r == 0 || !d->fatal || !s, "refused with a text");
		if (r && d->fatal) {
			t.refused = 1;
			return t;
		}
		check(s && !s[len], "no text, or no NUL after it");
		put(&t, s, len);
		free(s);
	}
	char *s;
	size_t len;
	int r = utf8_decode(d, k ? NULL : p, k ? 0 : n, 0, &s, &len);
	check(r >= 0, "out of memory");
	if (r && d->fatal) {
		check(!s, "refused with a text");
		t.refused = 1;
		return t;
	}
	put(&t, s, len);
	free(s);
	check(d->npending == 0 && d->bom_seen == 0, "the decoder does not start over after a call without stream");
	return t;
}

static int same(const text *a, const text *b)
{
	return a->refused == b->refused && (a->refused || (a->len == b->len && !memcmp(a->s, b->s, a->len)));
}

static void decoder(const uint8_t *in, size_t n, int print)
{
	if (!n)
		return;
	int fatal = in[0] & 1, ignore_bom = in[0] >> 1 & 1;
	size_t k = in[0] >> 2;
	const unsigned char *p = in + 1;
	n--;
	text want = whatwg(p, n, fatal, ignore_bom);
	utf8_decoder d = {.fatal = fatal, .ignore_bom = ignore_bom};
	text got = decode(&d, p, n, k);
	check(same(&got, &want), k ? "the chunks' text is not the decoder's" : "the text is not the decoder's");
	utf8_decoder one = {.fatal = fatal, .ignore_bom = ignore_bom};
	text whole = decode(&one, p, n, 0);
	check(same(&whole, &want), "one call's text is not the decoder's");
	if (!got.refused) {
		text again = decode(&d, p, n, 0);
		check(same(&again, &want), "the decoder used again gives another text");
		free(again.s);
	}
	if (print) {
		fputc(got.refused ? 'E' : 'T', stdout);
		if (!got.refused)
			fwrite(got.s, 1, got.len, stdout);
	}
	free(want.s);
	free(got.s);
	free(whole.s);
}

#ifdef KARIO_LIBFUZZER
int LLVMFuzzerTestOneInput(const uint8_t *data, size_t len)
{
	decoder(data, len, 0);
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
	decoder(in, n, 1);
	free(in);
	return 0;
}
#endif
