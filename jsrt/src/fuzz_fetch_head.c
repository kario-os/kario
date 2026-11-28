/* Fuzz harness of kario-jsrt's reading of fetch's response headers
 * (fetchhead.c, tools/cfuzz): the input is what libcurl hands net.c's
 * header callback, cut after each line feed. Each line taken, ending a
 * response or refused must be so by the harness's own reading, and at
 * every blank line and at the end of the input the headers split from the
 * lines taken since the last status line have:
 *   - one header a name, the names in lower case and in order;
 *   - each line's value, without the spaces and tabs before it, in its
 *     name's header in the order of the lines, joined with ", ", and
 *     nothing else;
 *   - the Location flag set when, and only when, a line is named so.
 * Built with the sanitizers; a report, a crash or a broken invariant fails
 * the input. Built with KARIO_LIBFUZZER defined it is a libFuzzer target
 * instead (make fuzz-libfuzzer). */
#include "fetchhead.h"

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void check(int ok, const char *what)
{
	if (!ok) {
		fprintf(stderr, "fuzz_fetch_head: %s\n", what);
		abort();
	}
}

static void *grow(void *p, size_t n)
{
	p = realloc(p, n ? n : 1);
	check(p != NULL, "out of memory");
	return p;
}

/* RFC 9110's tchar. */
static const char tokens[] = "!#$%&'*+-.^_`|~0123456789"
			     "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";

/* RFC 9110's field-vchar, and the spaces and tabs between. */
static int field_byte(unsigned char c)
{
	return c == '\t' || (c >= ' ' && c <= '~') || c >= 0x80;
}

typedef struct {
	char *text; /* the line as given */
	char *name; /* lower case */
	const char *value;
	size_t vlen;
} line;

/* The lines taken since the last status line. */
typedef struct {
	line *ls;
	size_t n, cap, kept;
	int open, location;
} model;

/* reading is what the line p[0..len) is to the harness; a header line's
 * length without its line end goes in *body, its first colon's place in
 * *colon. */
static int reading(const model *md, const char *p, size_t len, size_t *body, size_t *colon)
{
	if (memchr(p, 0, len))
		return FETCH_HEAD_REFUSED;
	if (!md->open && len >= 5 && !memcmp(p, "HTTP/", 5))
		return FETCH_HEAD_STATUS;
	size_t n = len;
	if (n && p[n - 1] == '\n')
		n--;
	if (n && p[n - 1] == '\r')
		n--;
	*body = n;
	if (!n)
		return FETCH_HEAD_END;
	if (md->kept + n + 1 > FETCH_MAX_HEAD)
		return FETCH_HEAD_REFUSED;
	size_t c = 0;
	while (c < n && p[c] != ':')
		c++;
	*colon = c;
	if (c == 0 || c == n)
		return FETCH_HEAD_REFUSED;
	for (size_t i = 0; i < c; i++)
		if (!memchr(tokens, p[i], sizeof tokens - 1))
			return FETCH_HEAD_REFUSED;
	for (size_t i = c + 1; i < n; i++)
		if (!field_byte((unsigned char)p[i]))
			return FETCH_HEAD_REFUSED;
	return FETCH_HEAD_KEPT;
}

static void forget(model *md)
{
	for (size_t i = 0; i < md->n; i++) {
		free(md->ls[i].text);
		free(md->ls[i].name);
	}
	md->n = md->kept = 0;
	md->location = 0;
}

static void take(model *md, char *p, size_t n, size_t colon)
{
	if (md->n == md->cap) {
		md->cap = md->cap ? md->cap * 2 : 16;
		md->ls = grow(md->ls, md->cap * sizeof *md->ls);
	}
	char *name = grow(NULL, colon + 1);
	for (size_t i = 0; i < colon; i++)
		name[i] = p[i] >= 'A' && p[i] <= 'Z' ? (char)(p[i] | 0x20) : p[i];
	name[colon] = 0;
	size_t v = colon + 1;
	while (v < n && (p[v] == ' ' || p[v] == '\t'))
		v++;
	md->ls[md->n++] = (line){p, name, p + v, n - v};
	md->kept += n + 1;
	md->location |= !strcmp(name, "location");
}

static void verify(const fetch_head *h, const model *md)
{
	check(h->location == md->location, "the Location flag is wrong");
	check(h->len == md->kept && h->len <= FETCH_MAX_HEAD, "kept other lines than those taken");
	fetch_header *hs;
	long k = fetch_head_split(h, &hs);
	check(k >= 0, "out of memory");
	size_t *vlen = grow(NULL, (size_t)k * sizeof *vlen), *at = grow(NULL, (size_t)k * sizeof *at);
	char *seen = grow(NULL, (size_t)k);
	for (long g = 0; g < k; g++) {
		for (const char *c = hs[g].name; *c; c++)
			check(*c < 'A' || *c > 'Z', "a name not in lower case");
		check(g == 0 || strcmp(hs[g - 1].name, hs[g].name) < 0, "names out of order, or one in two headers");
		vlen[g] = strlen(hs[g].value);
		at[g] = 0;
		seen[g] = 0;
	}
	for (size_t i = 0; i < md->n; i++) {
		const line *l = &md->ls[i];
		long lo = 0, hi = k;
		while (lo < hi) {
			long mid = lo + (hi - lo) / 2;
			if (strcmp(hs[mid].name, l->name) < 0)
				lo = mid + 1;
			else
				hi = mid;
		}
		check(lo < k && !strcmp(hs[lo].name, l->name), "a line's name is lost");
		size_t a = at[lo];
		if (seen[lo]) {
			check(a + 2 <= vlen[lo] && !memcmp(hs[lo].value + a, ", ", 2), "values not joined with \", \"");
			a += 2;
		}
		check(a + l->vlen <= vlen[lo] && !memcmp(hs[lo].value + a, l->value, l->vlen),
		      "a line's value is lost or out of order");
		at[lo] = a + l->vlen;
		seen[lo] = 1;
	}
	for (long g = 0; g < k; g++)
		check(seen[g] && at[g] == vlen[g], "a header no line has, or more in it than its lines");
	fetch_headers_free(hs, (size_t)k);
	free(vlen);
	free(at);
	free(seen);
}

static void heads(const uint8_t *in, size_t n)
{
	fetch_head h = {0};
	model md = {0};
	for (size_t at = 0; at < n;) {
		const uint8_t *e = memchr(in + at, '\n', n - at);
		size_t len = e ? (size_t)(e - in - at) + 1 : n - at;
		char *p = grow(NULL, len);
		memcpy(p, in + at, len);
		at += len;
		size_t body = 0, colon = 0;
		int want = reading(&md, p, len, &body, &colon);
		const char *why = NULL;
		int got = fetch_head_line(&h, p, len, &why);
		check(got == want, got == FETCH_HEAD_REFUSED ? "a line refused that may be taken"
			   : want == FETCH_HEAD_REFUSED	     ? "a line taken that may not be"
							     : "a line taken for what it is not");
		if (got == FETCH_HEAD_REFUSED) {
			check(why && *why, "refused without a reason");
			free(p);
			break;
		}
		if (got == FETCH_HEAD_KEPT) {
			take(&md, p, body, colon);
			continue;
		}
		free(p);
		if (got == FETCH_HEAD_STATUS) {
			forget(&md);
			md.open = 1;
		} else {
			md.open = 0;
			verify(&h, &md);
		}
		check(h.open == md.open, "the response's headers open when they are not, or not when they are");
	}
	verify(&h, &md);
	forget(&md);
	free(md.ls);
	fetch_head_free(&h);
}

#ifdef KARIO_LIBFUZZER
int LLVMFuzzerTestOneInput(const uint8_t *data, size_t len)
{
	heads(data, len);
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
	heads(in, n);
	free(in);
	return 0;
}
#endif
