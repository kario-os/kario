/* fetch's response headers (see fetchhead.h). */
#include "fetchhead.h"

#include <stdlib.h>
#include <string.h>
#include <strings.h>

static int tchar(unsigned char c)
{
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c && strchr("!#$%&'*+-.^_`|~", c));
}

/* put keeps the line p[0..n) with a line feed after it. */
static int put(fetch_head *h, const char *p, size_t n)
{
	if (h->len + n + 2 > h->cap) {
		size_t cap = h->cap ? h->cap : 256;
		while (h->len + n + 2 > cap)
			cap *= 2;
		char *s = realloc(h->s, cap);
		if (!s)
			return -1;
		h->s = s;
		h->cap = cap;
	}
	memcpy(h->s + h->len, p, n);
	h->len += n;
	h->s[h->len++] = '\n';
	h->s[h->len] = 0;
	return 0;
}

int fetch_head_line(fetch_head *h, const char *p, size_t len, const char **why)
{
	/* libcurl's own words: since 7.84 it refuses such a line itself. */
	if (memchr(p, 0, len)) {
		*why = "Nul byte in header";
		return FETCH_HEAD_REFUSED;
	}
	if (!h->open && len >= 5 && !memcmp(p, "HTTP/", 5)) {
		h->len = 0;
		h->location = 0;
		h->open = 1;
		return FETCH_HEAD_STATUS;
	}
	size_t n = len;
	if (n && p[n - 1] == '\n')
		n--;
	if (n && p[n - 1] == '\r')
		n--;
	if (n == 0) {
		h->open = 0;
		return FETCH_HEAD_END;
	}
	if (h->len + n + 1 > FETCH_MAX_HEAD) {
		*why = "the response's headers are over 64 KB";
		return FETCH_HEAD_REFUSED;
	}
	const char *colon = memchr(p, ':', n);
	if (!colon) {
		*why = "Header without colon";
		return FETCH_HEAD_REFUSED;
	}
	int token = colon > p;
	for (const char *c = p; c < colon; c++)
		token = token && tchar(*c);
	if (!token) {
		*why = "a header name in the response that is not a token";
		return FETCH_HEAD_REFUSED;
	}
	for (const char *c = colon + 1; c < p + n; c++)
		if (((unsigned char)*c < 0x20 && *c != '\t') || *c == 0x7f) {
			*why = "a control character in a header of the response";
			return FETCH_HEAD_REFUSED;
		}
	if (colon - p == 8 && !strncasecmp(p, "location", 8))
		h->location = 1;
	if (put(h, p, n)) {
		*why = "out of memory";
		return FETCH_HEAD_REFUSED;
	}
	return FETCH_HEAD_KEPT;
}

void fetch_head_free(fetch_head *h)
{
	free(h->s);
	*h = (fetch_head){0};
}

typedef struct {
	const char *name, *value;
	size_t nlen, vlen, at;
} head_line;

static int same_name(const head_line *x, const head_line *y)
{
	size_t n = x->nlen < y->nlen ? x->nlen : y->nlen;
	int d = strncasecmp(x->name, y->name, n);
	return d ? d : x->nlen < y->nlen ? -1 : x->nlen > y->nlen;
}

static int by_name(const void *a, const void *b)
{
	const head_line *x = a, *y = b;
	int d = same_name(x, y);
	return d ? d : x->at < y->at ? -1 : x->at > y->at;
}

long fetch_head_split(const fetch_head *h, fetch_header **out)
{
	*out = NULL;
	size_t cap = 1;
	for (size_t i = 0; i < h->len; i++)
		cap += h->s[i] == '\n';
	head_line *ls = malloc(cap * sizeof *ls);
	if (!ls)
		return -1;
	size_t n = 0;
	for (size_t at = 0; at < h->len;) {
		const char *p = h->s + at, *e = memchr(p, '\n', h->len - at);
		if (!e)
			e = h->s + h->len;
		at = (size_t)(e - h->s) + 1;
		const char *colon = memchr(p, ':', (size_t)(e - p));
		if (colon && colon > p) {
			const char *v = colon + 1;
			while (v < e && (*v == ' ' || *v == '\t'))
				v++;
			ls[n] = (head_line){p, v, (size_t)(colon - p), (size_t)(e - v), n};
			n++;
		}
	}
	qsort(ls, n, sizeof *ls, by_name);
	size_t names = 0;
	for (size_t i = 0; i < n; i++)
		names += i == 0 || same_name(&ls[i - 1], &ls[i]);
	fetch_header *hs = calloc(names ? names : 1, sizeof *hs);
	if (!hs) {
		free(ls);
		return -1;
	}
	for (size_t i = 0, g = 0; i < n; g++) {
		size_t j = i, vlen = 0;
		for (; j < n && !same_name(&ls[i], &ls[j]); j++)
			vlen += ls[j].vlen + (j > i ? 2 : 0);
		char *name = malloc(ls[i].nlen + 1), *value = malloc(vlen + 1);
		if (!name || !value) {
			free(name);
			free(value);
			fetch_headers_free(hs, g);
			free(ls);
			return -1;
		}
		for (size_t k = 0; k < ls[i].nlen; k++) {
			char c = ls[i].name[k];
			name[k] = c >= 'A' && c <= 'Z' ? (char)(c - 'A' + 'a') : c;
		}
		name[ls[i].nlen] = 0;
		char *w = value;
		for (size_t k = i; k < j; k++) {
			if (k > i) {
				*w++ = ',';
				*w++ = ' ';
			}
			memcpy(w, ls[k].value, ls[k].vlen);
			w += ls[k].vlen;
		}
		*w = 0;
		hs[g] = (fetch_header){name, value};
		i = j;
	}
	free(ls);
	*out = hs;
	return (long)names;
}

void fetch_headers_free(fetch_header *hs, size_t n)
{
	for (size_t i = 0; hs && i < n; i++) {
		free(hs[i].name);
		free(hs[i].value);
	}
	free(hs);
}
