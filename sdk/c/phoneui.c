/* phoneui: the phone.Ui protocol for C and C++ apps (see phoneui.h). */
#define _POSIX_C_SOURCE 200809L
#include "phoneui.h"

#include <errno.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#define MAX_LINE (4u << 20) /* as the shell's wire framing */

typedef struct {
	char *s;
	size_t len, cap;
} buf;

static int buf_put(buf *b, const char *s, size_t n)
{
	if (b->len + n + 1 > b->cap) {
		size_t cap = b->cap ? b->cap : 256;
		while (b->len + n + 1 > cap)
			cap *= 2;
		char *p = realloc(b->s, cap);
		if (!p)
			return -1;
		b->s = p;
		b->cap = cap;
	}
	memcpy(b->s + b->len, s, n);
	b->len += n;
	b->s[b->len] = 0;
	return 0;
}

static int buf_puts(buf *b, const char *s) { return buf_put(b, s, strlen(s)); }

struct phoneui {
	int fd;
	buf in;       /* bytes read, not yet split into lines */
	char **queue; /* lines that arrived during phoneui_call */
	size_t nqueue;
	char **owned; /* strings handed out with the last message */
	size_t nowned;
	long next_req;
};

/* ---- JSON reading: just enough for the shell's messages ---- */

static const char *ws(const char *p)
{
	while (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r')
		p++;
	return p;
}

static const char *skip_string(const char *p)
{
	for (p++; *p && *p != '"'; p++)
		if (*p == '\\' && !*++p)
			return NULL;
	return *p == '"' ? p + 1 : NULL;
}

/* skip_value returns the end of the JSON value at p, or NULL. */
static const char *skip_value(const char *p)
{
	p = ws(p);
	if (*p == '"')
		return skip_string(p);
	if (*p == '{' || *p == '[') {
		char close = *p == '{' ? '}' : ']';
		p = ws(p + 1);
		if (*p == close)
			return p + 1;
		for (;;) {
			if (close == '}') {
				if (*p != '"' || !(p = skip_string(p)))
					return NULL;
				p = ws(p);
				if (*p++ != ':')
					return NULL;
			}
			if (!(p = skip_value(p)))
				return NULL;
			p = ws(p);
			if (*p == close)
				return p + 1;
			if (*p++ != ',')
				return NULL;
			p = ws(p);
		}
	}
	const char *s = p;
	while (*p && !strchr(",}] \t\r\n", *p))
		p++;
	return p > s ? p : NULL;
}

static void put_utf8(buf *b, uint32_t c)
{
	char u[4];
	size_t n;
	if (c < 0x80) {
		u[0] = (char)c;
		n = 1;
	} else if (c < 0x800) {
		u[0] = (char)(0xc0 | c >> 6);
		u[1] = (char)(0x80 | (c & 0x3f));
		n = 2;
	} else if (c < 0x10000) {
		u[0] = (char)(0xe0 | c >> 12);
		u[1] = (char)(0x80 | (c >> 6 & 0x3f));
		u[2] = (char)(0x80 | (c & 0x3f));
		n = 3;
	} else {
		u[0] = (char)(0xf0 | c >> 18);
		u[1] = (char)(0x80 | (c >> 12 & 0x3f));
		u[2] = (char)(0x80 | (c >> 6 & 0x3f));
		u[3] = (char)(0x80 | (c & 0x3f));
		n = 4;
	}
	buf_put(b, u, n);
}

static int hex4(const char *p, uint32_t *v)
{
	*v = 0;
	for (int i = 0; i < 4; i++) {
		char c = p[i];
		int d = c >= '0' && c <= '9' ? c - '0' : c >= 'a' && c <= 'f' ? c - 'a' + 10 : c >= 'A' && c <= 'F' ? c - 'A' + 10 : -1;
		if (d < 0)
			return -1;
		*v = *v << 4 | (uint32_t)d;
	}
	return 0;
}

/* decode_string decodes the JSON string at p into a malloc'd UTF-8 string. */
static char *decode_string(const char *p)
{
	buf b = {0};
	buf_puts(&b, "");
	for (p++; *p && *p != '"'; p++) {
		if (*p != '\\') {
			buf_put(&b, p, 1);
			continue;
		}
		static const char names[] = "\"\\/bfnrt", chars[] = "\"\\/\b\f\n\r\t";
		char e = *++p;
		const char *simple = e ? strchr(names, e) : NULL;
		if (simple) {
			buf_put(&b, &chars[simple - names], 1);
			continue;
		}
		uint32_t c, lo;
		if (e != 'u' || hex4(p + 1, &c)) {
			free(b.s);
			return NULL;
		}
		p += 4;
		if (c >= 0xd800 && c < 0xdc00 && p[1] == '\\' && p[2] == 'u' && !hex4(p + 3, &lo) && lo >= 0xdc00 && lo < 0xe000) {
			c = 0x10000 + ((c - 0xd800) << 10) + (lo - 0xdc00);
			p += 6;
		}
		put_utf8(&b, c);
	}
	return b.s;
}

/* member finds name in the JSON object obj: the start of its value and
 * (through end) where the value ends; NULL when absent. */
static const char *member(const char *obj, const char *name, const char **end)
{
	const char *p = ws(obj);
	if (*p++ != '{')
		return NULL;
	size_t n = strlen(name);
	for (;;) {
		p = ws(p);
		if (*p != '"')
			return NULL;
		const char *k = p + 1, *ke = skip_string(p);
		if (!ke)
			return NULL;
		p = ws(ke);
		if (*p++ != ':')
			return NULL;
		const char *v = ws(p), *ve = skip_value(v);
		if (!ve)
			return NULL;
		if ((size_t)(ke - 1 - k) == n && !memcmp(k, name, n)) {
			*end = ve;
			return v;
		}
		p = ws(ve);
		if (*p++ != ',')
			return NULL;
	}
}

char *phoneui_get_string(const char *obj, const char *name)
{
	const char *end, *v = obj ? member(obj, name, &end) : NULL;
	return v && *v == '"' ? decode_string(v) : NULL;
}

/* ---- the connection ---- */

static void release(phoneui *ui)
{
	for (size_t i = 0; i < ui->nowned; i++)
		free(ui->owned[i]);
	ui->nowned = 0;
}

static const char *own(phoneui *ui, char *s)
{
	if (!s)
		return NULL;
	char **o = realloc(ui->owned, (ui->nowned + 1) * sizeof *o);
	if (!o) {
		free(s);
		return NULL;
	}
	ui->owned = o;
	ui->owned[ui->nowned++] = s;
	return s;
}

static const char *own_string(phoneui *ui, const char *obj, const char *name)
{
	return own(ui, phoneui_get_string(obj, name));
}

static const char *own_raw(phoneui *ui, const char *obj, const char *name)
{
	const char *end, *v = member(obj, name, &end);
	return v ? own(ui, strndup(v, (size_t)(end - v))) : NULL;
}

int phoneui_send(phoneui *ui, const char *json)
{
	size_t n = strlen(json);
	char *line = malloc(n + 1);
	if (!line)
		return -1;
	memcpy(line, json, n);
	line[n] = '\n';
	size_t off = 0;
	while (off < n + 1) {
		ssize_t w = write(ui->fd, line + off, n + 1 - off);
		if (w < 0 && errno == EINTR)
			continue;
		if (w <= 0) {
			free(line);
			return -1;
		}
		off += (size_t)w;
	}
	free(line);
	return 0;
}

phoneui *phoneui_open(void)
{
	const char *fdv = getenv("PHONE_UI_FD"), *app = getenv("PHONE_APP_ID");
	if (!fdv)
		return NULL;
	phoneui *ui = calloc(1, sizeof *ui);
	if (!ui)
		return NULL;
	ui->fd = atoi(fdv);
	phoneui_json *j = phoneui_json_new();
	phoneui_json_begin_object(j);
	phoneui_json_key(j, "t");
	phoneui_json_string(j, "hello");
	phoneui_json_key(j, "app");
	phoneui_json_string(j, app ? app : "");
	phoneui_json_key(j, "proto");
	phoneui_json_string(j, PHONEUI_PROTO);
	phoneui_json_end_object(j);
	int err = phoneui_send(ui, phoneui_json_text(j));
	phoneui_json_free(j);
	if (err) {
		free(ui);
		return NULL;
	}
	return ui;
}

void phoneui_close(phoneui *ui)
{
	if (!ui)
		return;
	release(ui);
	for (size_t i = 0; i < ui->nqueue; i++)
		free(ui->queue[i]);
	free(ui->queue);
	free(ui->owned);
	free(ui->in.s);
	close(ui->fd);
	free(ui);
}

/* take_line gets the next line from the socket (malloc'd) into *out: 1,
 * or -1 when the connection is gone; without wait, 0 when no whole line
 * has arrived yet (the socket is read without blocking, its flags left
 * alone). */
static int take_line(phoneui *ui, int wait, char **out)
{
	for (;;) {
		char *nl = ui->in.len ? memchr(ui->in.s, '\n', ui->in.len) : NULL;
		if (nl) {
			size_t n = (size_t)(nl - ui->in.s);
			*out = strndup(ui->in.s, n);
			memmove(ui->in.s, nl + 1, ui->in.len - n - 1);
			ui->in.len -= n + 1;
			return *out ? 1 : -1;
		}
		if (ui->in.len > MAX_LINE)
			return -1;
		char chunk[16384];
		ssize_t r = recv(ui->fd, chunk, sizeof chunk, wait ? 0 : MSG_DONTWAIT);
		if (r < 0 && errno == EINTR)
			continue;
		if (r < 0 && !wait && (errno == EAGAIN || errno == EWOULDBLOCK))
			return 0;
		if (r <= 0 || buf_put(&ui->in, chunk, (size_t)r))
			return -1;
	}
}

/* read_line returns the next line from the socket (malloc'd), or NULL. */
static char *read_line(phoneui *ui)
{
	char *line = NULL;
	return take_line(ui, 1, &line) == 1 ? line : NULL;
}

/* answer_ping replies to a ping and reports whether line was one. */
static int answer_ping(phoneui *ui, const char *line)
{
	char *t = phoneui_get_string(line, "t");
	int ping = t && !strcmp(t, "ping");
	free(t);
	if (!ping)
		return 0;
	char *v = phoneui_get_string(line, "value");
	phoneui_json *j = phoneui_json_new();
	phoneui_json_begin_object(j);
	phoneui_json_key(j, "t");
	phoneui_json_string(j, "pong");
	phoneui_json_key(j, "value");
	phoneui_json_string(j, v ? v : "");
	phoneui_json_end_object(j);
	phoneui_send(ui, phoneui_json_text(j));
	phoneui_json_free(j);
	free(v);
	return 1;
}

int phoneui_pending(const phoneui *ui)
{
	return ui->nqueue > 0 || (ui->in.len && memchr(ui->in.s, '\n', ui->in.len));
}

/* next_msg is phoneui_next, and with !wait phoneui_try_next. */
static int next_msg(phoneui *ui, phoneui_msg *msg, int wait)
{
	release(ui);
	memset(msg, 0, sizeof *msg);
	for (;;) {
		char *line = NULL;
		if (ui->nqueue) {
			line = ui->queue[0];
			memmove(ui->queue, ui->queue + 1, --ui->nqueue * sizeof *ui->queue);
		} else {
			int got = take_line(ui, wait, &line);
			if (got <= 0)
				return got;
		}
		if (answer_ping(ui, line)) {
			free(line);
			continue;
		}
		msg->line = own(ui, line);
		msg->t = own_string(ui, line, "t");
		msg->screen = own_string(ui, line, "screen");
		msg->node = own_string(ui, line, "node");
		msg->action = own_string(ui, line, "action");
		msg->value = own_string(ui, line, "value");
		msg->event = own_string(ui, line, "event");
		msg->state = own_string(ui, line, "state");
		msg->fields = own_raw(ui, line, "fields");
		return 1;
	}
}

int phoneui_next(phoneui *ui, phoneui_msg *msg) { return next_msg(ui, msg, 1) == 1 ? 0 : -1; }

int phoneui_try_next(phoneui *ui, phoneui_msg *msg) { return next_msg(ui, msg, 0); }

int phoneui_call(phoneui *ui, const char *cap, const char *args, const char **result, const char **err)
{
	release(ui);
	long id = ++ui->next_req;
	buf req = {0};
	phoneui_json *j = phoneui_json_new();
	phoneui_json_string(j, cap);
	char head[64];
	snprintf(head, sizeof head, "{\"t\":\"req\",\"req\":%ld,\"cap\":", id);
	buf_puts(&req, head);
	buf_puts(&req, phoneui_json_text(j));
	phoneui_json_free(j);
	if (args) {
		buf_puts(&req, ",\"args\":");
		buf_puts(&req, args);
	}
	buf_puts(&req, "}");
	int sent = phoneui_send(ui, req.s);
	free(req.s);
	if (sent) {
		if (err)
			*err = "connection lost";
		return -1;
	}
	for (;;) {
		char *line = read_line(ui);
		if (!line) {
			if (err)
				*err = "connection lost";
			return -1;
		}
		if (answer_ping(ui, line)) {
			free(line);
			continue;
		}
		char *t = phoneui_get_string(line, "t");
		const char *end, *r = member(line, "req", &end);
		int mine = t && !strcmp(t, "res") && r && strtol(r, NULL, 10) == id;
		free(t);
		if (!mine) {
			char **q = realloc(ui->queue, (ui->nqueue + 1) * sizeof *q);
			if (q) {
				ui->queue = q;
				ui->queue[ui->nqueue++] = line;
			} else {
				free(line);
			}
			continue;
		}
		const char *okv = member(line, "ok", &end);
		int ok = okv && !strncmp(okv, "true", 4);
		if (result)
			*result = ok ? own_raw(ui, line, "data") : NULL;
		if (err)
			*err = ok ? NULL : own_string(ui, line, "err");
		own(ui, line);
		return ok ? 0 : -1;
	}
}

/* ---- JSON writing ---- */

struct phoneui_json {
	buf b;
	int fresh; /* nothing written yet at this nesting level */
	int after_key;
};

phoneui_json *phoneui_json_new(void)
{
	phoneui_json *j = calloc(1, sizeof *j);
	if (j) {
		buf_puts(&j->b, "");
		j->fresh = 1;
	}
	return j;
}

void phoneui_json_free(phoneui_json *j)
{
	if (j) {
		free(j->b.s);
		free(j);
	}
}

const char *phoneui_json_text(const phoneui_json *j) { return j->b.s; }

static void sep(phoneui_json *j)
{
	if (!j->fresh && !j->after_key)
		buf_puts(&j->b, ",");
	j->fresh = 0;
	j->after_key = 0;
}

void phoneui_json_begin_object(phoneui_json *j)
{
	sep(j);
	buf_puts(&j->b, "{");
	j->fresh = 1;
}

void phoneui_json_end_object(phoneui_json *j)
{
	buf_puts(&j->b, "}");
	j->fresh = 0;
}

void phoneui_json_begin_array(phoneui_json *j)
{
	sep(j);
	buf_puts(&j->b, "[");
	j->fresh = 1;
}

void phoneui_json_end_array(phoneui_json *j)
{
	buf_puts(&j->b, "]");
	j->fresh = 0;
}

static void quote(buf *b, const char *s)
{
	buf_puts(b, "\"");
	for (const unsigned char *p = (const unsigned char *)s; *p; p++) {
		char esc[8];
		if (*p == '"' || *p == '\\') {
			esc[0] = '\\';
			esc[1] = (char)*p;
			buf_put(b, esc, 2);
		} else if (*p < 0x20) {
			snprintf(esc, sizeof esc, "\\u%04x", *p);
			buf_puts(b, esc);
		} else {
			buf_put(b, (const char *)p, 1);
		}
	}
	buf_puts(b, "\"");
}

void phoneui_json_key(phoneui_json *j, const char *key)
{
	sep(j);
	quote(&j->b, key);
	buf_puts(&j->b, ":");
	j->after_key = 1;
}

void phoneui_json_string(phoneui_json *j, const char *s)
{
	sep(j);
	quote(&j->b, s ? s : "");
}

void phoneui_json_int(phoneui_json *j, long long v)
{
	char n[32];
	sep(j);
	snprintf(n, sizeof n, "%lld", v);
	buf_puts(&j->b, n);
}

void phoneui_json_bool(phoneui_json *j, int v)
{
	sep(j);
	buf_puts(&j->b, v ? "true" : "false");
}
