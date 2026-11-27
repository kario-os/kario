/* TextDecoder's UTF-8 decoding (web.c), the WHATWG decoder's: a byte
 * order mark dropped at the start, an invalid sequence's maximal subpart
 * replaced by one U+FFFD, a sequence cut at the end of a streamed chunk
 * kept for the next. No engine here, so the fuzz harness (fuzz_utf8.c)
 * links it as it is. */
#ifndef KARIO_UTF8DEC_H
#define KARIO_UTF8DEC_H

#include <stddef.h>

typedef struct {
	int fatal, ignore_bom, bom_seen;
	unsigned char pending[4]; /* a cut sequence from the last streamed chunk */
	int npending;
} utf8_decoder;

/* utf8_decode decodes n bytes at in after what d kept of the last streamed
 * call, as TextDecoder.decode(in, {stream}) does: the text in *out (*len
 * bytes of UTF-8 and a NUL, freed by the caller). The answer is 0, 1 when
 * something was replaced (with fatal, the TypeError: *out is NULL and d
 * starts over), or -1 when out of memory. */
int utf8_decode(utf8_decoder *d, const unsigned char *in, size_t n, int stream, char **out, size_t *len);

#endif
