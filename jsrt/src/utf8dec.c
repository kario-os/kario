/* TextDecoder's UTF-8 decoding (see utf8dec.h). */
#include "utf8dec.h"

#include <stdlib.h>
#include <string.h>

/* utf8_next decodes the sequence at p (n bytes there): its length, or 0
 * when it is cut short (more bytes may follow), or -k for an invalid
 * sequence whose first k bytes are replaced by one U+FFFD (the maximal
 * subpart, as the WHATWG decoder does). */
static int utf8_next(const unsigned char *p, size_t n)
{
	unsigned char c = p[0];
	if (c < 0x80)
		return 1;
	int need;
	unsigned char lo = 0x80, hi = 0xbf;
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
		return -1;
	}
	for (int i = 1; i <= need; i++) {
		if ((size_t)i >= n)
			return 0;
		if (p[i] < lo || p[i] > hi)
			return -i;
		lo = 0x80;
		hi = 0xbf;
	}
	return need + 1;
}

int utf8_decode(utf8_decoder *d, const unsigned char *in, size_t n, int stream, char **out, size_t *len)
{
	*out = NULL;
	*len = 0;
	size_t all = (size_t)d->npending + n;
	unsigned char *p = malloc(all ? all : 1);
	/* An input byte is at most one U+FFFD, three bytes. */
	char *w = all < ((size_t)-1 - 1) / 3 ? malloc(3 * all + 1) : NULL;
	if (!p || !w) {
		free(p);
		free(w);
		return -1;
	}
	memcpy(p, d->pending, (size_t)d->npending);
	if (n)
		memcpy(p + d->npending, in, n);
	d->npending = 0;
	size_t i = 0, end = all;
	if (!d->bom_seen && stream && all < 3 && !d->ignore_bom && !memcmp(p, "\xef\xbb\xbf", all)) {
		/* What came may be the start of a byte order mark. */
		end = 0;
	} else if (!d->bom_seen && (all || !stream)) {
		if (!d->ignore_bom && all >= 3 && !memcmp(p, "\xef\xbb\xbf", 3))
			i = 3;
		d->bom_seen = 1;
	}
	if (stream && end) {
		/* A sequence cut at the end waits for the next chunk. */
		for (size_t j = i; j < all;) {
			int k = utf8_next(p + j, all - j);
			if (k == 0) {
				end = j;
				break;
			}
			j += k > 0 ? (size_t)k : (size_t)-k;
		}
	}
	if (stream) {
		memcpy(d->pending, p + end, all - end);
		d->npending = (int)(all - end);
	}
	int bad = 0;
	size_t o = 0;
	while (i < end) {
		int k = utf8_next(p + i, end - i);
		if (k > 0) {
			memcpy(w + o, p + i, (size_t)k);
			o += (size_t)k;
			i += (size_t)k;
		} else {
			bad = 1;
			memcpy(w + o, "\xef\xbf\xbd", 3);
			o += 3;
			i += k == 0 ? end - i : (size_t)-k;
		}
	}
	free(p);
	if (!stream)
		d->bom_seen = 0;
	if (bad && d->fatal) {
		free(w);
		d->npending = 0;
		return 1;
	}
	w[o] = 0;
	*out = w;
	*len = o;
	return bad;
}
