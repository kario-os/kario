/* fetch's response headers (net.c): libcurl hands the header callback
 * the lines of every response on the way (a redirect's, a 100 Continue's),
 * one at a time. fetch_head_line checks each and keeps those of the
 * response in progress; fetch_head_split takes the app's response's apart
 * into names and values. No engine and no network here, so the fuzz
 * harness (fuzz_fetch_head.c) links it as it is. */
#ifndef KARIO_FETCHHEAD_H
#define KARIO_FETCHHEAD_H

#include <stddef.h>

/* The most of a response's header lines kept, a line feed each counted;
 * the fuzz harness is built with a smaller one, to get past it. */
#ifndef FETCH_MAX_HEAD
#define FETCH_MAX_HEAD (64 << 10)
#endif

typedef struct {
	char *s; /* the header lines, each ending in a line feed (none in a line) */
	size_t len, cap;
	int location; /* one of them is a Location */
	int open;     /* after a status line, before the blank line that ends its headers */
} fetch_head;

/* What fetch_head_line found. */
enum {
	FETCH_HEAD_KEPT,    /* a header line, kept */
	FETCH_HEAD_STATUS,  /* a status line: a new response, the lines before it dropped */
	FETCH_HEAD_END,     /* the blank line after a response's headers */
	FETCH_HEAD_REFUSED, /* a line no response may have, or past FETCH_MAX_HEAD */
};

/* fetch_head_line takes a line as libcurl gives it (len bytes, its line
 * end included): what it was, and when refused why. A header line is a
 * name (a token), a colon and a value without control characters but tab;
 * Node.js's parser refuses the others too. */
int fetch_head_line(fetch_head *h, const char *p, size_t len, const char **why);
void fetch_head_free(fetch_head *h);

typedef struct {
	char *name;  /* lower case */
	char *value; /* its lines' values in their order, without the spaces and tabs before them, joined with ", " */
} fetch_header;

/* fetch_head_split takes the lines fetch_head_line kept apart into one
 * header a name, in the order of their names: how many, in *out (freed by
 * fetch_headers_free), or -1 when out of memory. Each value is written
 * once, so many lines of one name cost no more than as many of different
 * names. */
long fetch_head_split(const fetch_head *h, fetch_header **out);
void fetch_headers_free(fetch_header *hs, size_t n);

#endif
