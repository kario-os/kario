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
#include <time.h>
#include <unistd.h>

#define MAX_LINE (4u << 20) /* as the shell's wire framing */
#define MAX_DEPTH 10000      /* nesting, as the shell's JSON decoder takes */
/* Files received that no message has claimed yet: a Shell that sends more
 * than its messages count cannot fill the descriptor table through them. */
#define MAX_UNCLAIMED 32

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

/* A line and the files its "fds" member claimed. */
typedef struct {
	char *s;
	int nfds;
	int fds[PHONEUI_MAX_FDS];
} msgline;

/* Another major's form (ui.ProtoForm, 22-developer-api.md §2.1): rules
 * renaming a message type t to as (kept when NULL) and, in messages of that
 * type (every one without t), fields from this library's name to the
 * major's. */
typedef struct {
	char *t, *as;
	int nfields;
	char **own, **theirs;
} form_rule;

typedef struct {
	int major;
	int nrules;
	form_rule *rules;
} form;

struct phoneui {
	int fd;
	buf in;       /* bytes read, not yet split into lines */
	msgline *queue; /* lines that arrived during phoneui_call */
	size_t nqueue;
	char **owned; /* strings handed out with the last message */
	size_t nowned;
	long next_req;
	int unclaimed[MAX_UNCLAIMED]; /* files received, in arrival order */
	int nunclaimed;
	int nfds; /* the last message's files, -1 for one taken */
	int fds[PHONEUI_MAX_FDS];
	/* The other majors spoken (PHONE_UI_TEST_FORMS); with any, lines
	 * sent wait in held until start names the major (form: NULL for this
	 * library's own). */
	form *forms;
	int nforms;
	const form *form;
	int started;
	char **held;
	size_t nheld;
};

static int write_line(int fd, const char *json);
static void read_forms(phoneui *ui, const char *json);
static void free_forms(phoneui *ui);
static int oldest_major(const phoneui *ui);
static char *turn(const form *f, const char *line, int to_major);
static char *arrived(phoneui *ui, char *line);
static void quote(buf *b, const char *s);

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

/* skip_nested returns the end of the JSON value at p, depth levels down,
 * or NULL. */
static const char *skip_nested(const char *p, int depth)
{
	p = ws(p);
	if (*p == '"')
		return skip_string(p);
	if (*p == '{' || *p == '[') {
		if (depth == MAX_DEPTH)
			return NULL;
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
			if (!(p = skip_nested(p, depth + 1)))
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

static const char *skip_value(const char *p) { return skip_nested(p, 0); }

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

static void close_fds(int *fds, int n)
{
	for (int i = 0; i < n; i++)
		if (fds[i] >= 0)
			close(fds[i]);
}

static void release(phoneui *ui)
{
	for (size_t i = 0; i < ui->nowned; i++)
		free(ui->owned[i]);
	ui->nowned = 0;
	close_fds(ui->fds, ui->nfds);
	ui->nfds = 0;
}

/* current makes l's files the last message's. */
static void current(phoneui *ui, msgline *l)
{
	memcpy(ui->fds, l->fds, sizeof l->fds);
	ui->nfds = l->nfds;
	l->nfds = 0;
}

int phoneui_fds(const phoneui *ui) { return ui->nfds; }

int phoneui_take_fd(phoneui *ui, int i)
{
	if (i < 0 || i >= ui->nfds)
		return -1;
	int fd = ui->fds[i];
	ui->fds[i] = -1;
	return fd;
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

static int write_line(int fd, const char *json)
{
	size_t n = strlen(json);
	char *line = malloc(n + 1);
	if (!line)
		return -1;
	memcpy(line, json, n);
	line[n] = '\n';
	size_t off = 0;
	while (off < n + 1) {
		ssize_t w = write(fd, line + off, n + 1 - off);
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

int phoneui_send(phoneui *ui, const char *json)
{
	if (ui->nforms && !ui->started) {
		char **h = realloc(ui->held, (ui->nheld + 1) * sizeof *h);
		char *copy = strdup(json);
		if (!h || !copy) {
			free(copy);
			return -1;
		}
		ui->held = h;
		ui->held[ui->nheld++] = copy;
		return 0;
	}
	char *turned = ui->form ? turn(ui->form, json, 1) : NULL;
	int err = write_line(ui->fd, turned ? turned : json);
	free(turned);
	return err;
}

#define PROTO_BASE "phone.ui/"

static int own_major(void) { return atoi(PHONEUI_PROTO + strlen(PROTO_BASE)); }

static void proto_name(char *out, size_t n, int major) { snprintf(out, n, PROTO_BASE "%d", major); }

phoneui *phoneui_open(void)
{
	const char *fdv = getenv("PHONE_UI_FD"), *app = getenv("PHONE_APP_ID");
	if (!fdv)
		return NULL;
	phoneui *ui = calloc(1, sizeof *ui);
	if (!ui)
		return NULL;
	ui->fd = atoi(fdv);
	read_forms(ui, getenv("PHONE_UI_TEST_FORMS"));
	char name[32];
	phoneui_json *j = phoneui_json_new();
	phoneui_json_begin_object(j);
	phoneui_json_key(j, "t");
	phoneui_json_string(j, "hello");
	phoneui_json_key(j, "app");
	phoneui_json_string(j, app ? app : "");
	phoneui_json_key(j, "proto");
	proto_name(name, sizeof name, oldest_major(ui));
	phoneui_json_string(j, ui->nforms ? name : PHONEUI_PROTO);
	if (ui->nforms) {
		/* Newest first; the forms are kept in no order. */
		int last = -1;
		phoneui_json_key(j, "protos");
		phoneui_json_begin_array(j);
		for (;;) {
			int next = -1;
			if (own_major() > next && (last < 0 || own_major() < last))
				next = own_major();
			for (int i = 0; i < ui->nforms; i++)
				if (ui->forms[i].major > next && (last < 0 || ui->forms[i].major < last))
					next = ui->forms[i].major;
			if (next < 0)
				break;
			proto_name(name, sizeof name, next);
			phoneui_json_string(j, name);
			last = next;
		}
		phoneui_json_end_array(j);
	}
	phoneui_json_end_object(j);
	int err = write_line(ui->fd, phoneui_json_text(j));
	phoneui_json_free(j);
	if (err) {
		free_forms(ui);
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
	for (size_t i = 0; i < ui->nqueue; i++) {
		free(ui->queue[i].s);
		close_fds(ui->queue[i].fds, ui->queue[i].nfds);
	}
	close_fds(ui->unclaimed, ui->nunclaimed);
	for (size_t i = 0; i < ui->nheld; i++)
		free(ui->held[i]);
	free(ui->held);
	free_forms(ui);
	free(ui->queue);
	free(ui->owned);
	free(ui->in.s);
	close(ui->fd);
	free(ui);
}

/* claim gives l the files its "fds" member counts, the oldest received
 * first: they travel with their message's first bytes (SCM_RIGHTS), so
 * they have all arrived once the whole line has. */
static void claim(phoneui *ui, msgline *l)
{
	l->nfds = 0;
	if (!ui->nunclaimed)
		return;
	const char *end, *v = member(l->s, "fds", &end);
	long n = v ? strtol(v, NULL, 10) : 0;
	if (n <= 0)
		return;
	if (n > PHONEUI_MAX_FDS)
		n = PHONEUI_MAX_FDS;
	if (n > ui->nunclaimed)
		n = ui->nunclaimed;
	memcpy(l->fds, ui->unclaimed, (size_t)n * sizeof(int));
	l->nfds = (int)n;
	ui->nunclaimed -= (int)n;
	memmove(ui->unclaimed, ui->unclaimed + n, (size_t)ui->nunclaimed * sizeof(int));
}

/* receive reads what the socket has, with the files that come along. */
static ssize_t receive(phoneui *ui, char *p, size_t n, int wait)
{
	union {
		struct cmsghdr h;
		char space[CMSG_SPACE(PHONEUI_MAX_FDS * sizeof(int))];
	} ctl;
	struct iovec iov = {.iov_base = p, .iov_len = n};
	struct msghdr mh = {.msg_iov = &iov, .msg_iovlen = 1, .msg_control = ctl.space, .msg_controllen = sizeof ctl.space};
	int flags = wait ? 0 : MSG_DONTWAIT;
#ifdef MSG_CMSG_CLOEXEC
	flags |= MSG_CMSG_CLOEXEC;
#endif
	ssize_t r = recvmsg(ui->fd, &mh, flags);
	if (r < 0)
		return r;
	for (struct cmsghdr *c = CMSG_FIRSTHDR(&mh); c; c = CMSG_NXTHDR(&mh, c)) {
		if (c->cmsg_level != SOL_SOCKET || c->cmsg_type != SCM_RIGHTS)
			continue;
		int nf = (int)((c->cmsg_len - CMSG_LEN(0)) / sizeof(int));
		for (int i = 0; i < nf; i++) {
			int fd;
			memcpy(&fd, CMSG_DATA(c) + i * sizeof(int), sizeof fd);
			if (ui->nunclaimed < MAX_UNCLAIMED)
				ui->unclaimed[ui->nunclaimed++] = fd;
			else
				close(fd);
		}
	}
	return r;
}

/* take_line gets the next line from the socket (malloc'd) into *out: 1,
 * or -1 when the connection is gone; without wait, 0 when no whole line
 * has arrived yet (the socket is read without blocking, its flags left
 * alone). */
static int take_line(phoneui *ui, int wait, msgline *out)
{
	for (;;) {
		char *nl = ui->in.len ? memchr(ui->in.s, '\n', ui->in.len) : NULL;
		if (nl) {
			size_t n = (size_t)(nl - ui->in.s);
			out->s = strndup(ui->in.s, n);
			memmove(ui->in.s, nl + 1, ui->in.len - n - 1);
			ui->in.len -= n + 1;
			if (!out->s)
				return -1;
			claim(ui, out);
			if (ui->nforms && !(out->s = arrived(ui, out->s))) {
				close_fds(out->fds, out->nfds);
				continue;
			}
			return 1;
		}
		if (ui->in.len > MAX_LINE)
			return -1;
		char chunk[16384];
		ssize_t r = receive(ui, chunk, sizeof chunk, wait);
		if (r < 0 && errno == EINTR)
			continue;
		if (r < 0 && !wait && (errno == EAGAIN || errno == EWOULDBLOCK))
			return 0;
		if (r <= 0 || buf_put(&ui->in, chunk, (size_t)r))
			return -1;
	}
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
		msgline l;
		if (ui->nqueue) {
			l = ui->queue[0];
			memmove(ui->queue, ui->queue + 1, --ui->nqueue * sizeof *ui->queue);
		} else {
			int got = take_line(ui, wait, &l);
			if (got <= 0)
				return got;
		}
		char *line = l.s;
		if (answer_ping(ui, line)) {
			close_fds(l.fds, l.nfds);
			free(line);
			continue;
		}
		current(ui, &l);
		msg->line = own(ui, line);
		msg->t = own_string(ui, line, "t");
		msg->screen = own_string(ui, line, "screen_id");
		msg->node = own_string(ui, line, "node");
		msg->action = own_string(ui, line, "action");
		msg->value = own_string(ui, line, "value");
		msg->event = own_string(ui, line, "event");
		msg->state = own_string(ui, line, "state");
		// A new time zone is in force before the app hears of it (as TZ at
		// its start): localtime() and QuickJS's Date follow.
		if (msg->t && msg->event && msg->value && !strcmp(msg->t, "event") && !strcmp(msg->event, "time.zone")) {
			setenv("TZ", msg->value, 1);
			tzset();
		}
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
		msgline l;
		if (take_line(ui, 1, &l) != 1) {
			if (err)
				*err = "connection lost";
			return -1;
		}
		char *line = l.s;
		if (answer_ping(ui, line)) {
			close_fds(l.fds, l.nfds);
			free(line);
			continue;
		}
		char *t = phoneui_get_string(line, "t");
		const char *end, *r = member(line, "req", &end);
		int mine = t && !strcmp(t, "res") && r && strtol(r, NULL, 10) == id;
		free(t);
		if (!mine) {
			msgline *q = realloc(ui->queue, (ui->nqueue + 1) * sizeof *q);
			if (q) {
				ui->queue = q;
				ui->queue[ui->nqueue++] = l;
			} else {
				close_fds(l.fds, l.nfds);
				free(line);
			}
			continue;
		}
		current(ui, &l);
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

/* ---- other majors (22-developer-api.md §2.1) ---- */

/* An iterator over the members of a JSON object or the elements of an
 * array, its values raw. */
typedef struct {
	const char *p;
	char close;
} json_iter;

static int iter_begin(json_iter *it, const char *v, char open)
{
	const char *p = v ? ws(v) : NULL;
	if (!p || *p != open)
		return -1;
	it->p = ws(p + 1);
	it->close = open == '{' ? '}' : ']';
	return 0;
}

/* iter_next steps to the next value (its member's key, quoted, in *k with
 * an object): 1, or 0 at the end, -1 on bad JSON. */
static int iter_next(json_iter *it, const char **k, const char **v, const char **ve)
{
	const char *p = it->p;
	if (*p == it->close)
		return 0;
	if (it->close == '}') {
		const char *ke;
		if (*p != '"' || !(ke = skip_string(p)))
			return -1;
		*k = p;
		p = ws(ke);
		if (*p++ != ':')
			return -1;
	}
	*v = ws(p);
	if (!(*ve = skip_value(*v)))
		return -1;
	p = ws(*ve);
	if (*p == ',')
		p = ws(p + 1);
	else if (*p != it->close)
		return -1;
	it->p = p;
	return 1;
}

static void read_rule(form *f, const char *v)
{
	form_rule r = {.t = phoneui_get_string(v, "t"), .as = phoneui_get_string(v, "as")};
	const char *end, *fields = member(v, "fields", &end), *k = NULL, *fv, *fve;
	json_iter it;
	if (fields && iter_begin(&it, fields, '{') == 0) {
		while (iter_next(&it, &k, &fv, &fve) == 1) {
			char **own = realloc(r.own, (r.nfields + 1) * sizeof *own);
			if (own)
				r.own = own;
			char **theirs = own ? realloc(r.theirs, (r.nfields + 1) * sizeof *theirs) : NULL;
			if (theirs)
				r.theirs = theirs;
			if (!own || !theirs || *fv != '"')
				continue;
			r.own[r.nfields] = decode_string(k);
			r.theirs[r.nfields] = decode_string(fv);
			r.nfields++;
		}
	}
	form_rule *rules = realloc(f->rules, (f->nrules + 1) * sizeof *rules);
	if (!rules) {
		free(r.t);
		free(r.as);
		return;
	}
	f->rules = rules;
	f->rules[f->nrules++] = r;
}

/* read_forms takes the forms of the other majors the app speaks (a JSON
 * array of ui.ProtoForm) from json. */
static void read_forms(phoneui *ui, const char *json)
{
	json_iter forms, rules;
	const char *k, *v, *ve, *rv, *rve, *end;
	if (iter_begin(&forms, json, '[') < 0)
		return;
	while (iter_next(&forms, &k, &v, &ve) == 1) {
		const char *major = member(v, "major", &end);
		form f = {.major = major ? (int)strtol(major, NULL, 10) : -1};
		if (f.major < 0 || f.major == own_major())
			continue;
		if (iter_begin(&rules, member(v, "rules", &end), '[') == 0)
			while (iter_next(&rules, &k, &rv, &rve) == 1)
				read_rule(&f, rv);
		form *grown = realloc(ui->forms, (ui->nforms + 1) * sizeof *grown);
		if (grown) {
			ui->forms = grown;
			ui->forms[ui->nforms++] = f;
		}
	}
}

static void free_forms(phoneui *ui)
{
	for (int i = 0; i < ui->nforms; i++) {
		for (int r = 0; r < ui->forms[i].nrules; r++) {
			form_rule *x = &ui->forms[i].rules[r];
			for (int n = 0; n < x->nfields; n++) {
				free(x->own[n]);
				free(x->theirs[n]);
			}
			free(x->own);
			free(x->theirs);
			free(x->t);
			free(x->as);
		}
		free(ui->forms[i].rules);
	}
	free(ui->forms);
	ui->forms = NULL;
	ui->nforms = 0;
}

static int oldest_major(const phoneui *ui)
{
	int m = own_major();
	for (int i = 0; i < ui->nforms; i++)
		if (ui->forms[i].major < m)
			m = ui->forms[i].major;
	return m;
}

static const char *theirs_type(const form_rule *r) { return r->as ? r->as : r->t; }

/* turn rewrites line from this library's form into f's major (to_major) or
 * back, as ui.ProtoForm's ToMajor and FromMajor; NULL for a line of the
 * major's of a type it does not have, or one that is not a JSON object. */
static char *turn(const form *f, const char *line, int to_major)
{
	char *t = phoneui_get_string(line, "t");
	const char *type = t ? t : "", *newtype = NULL;
	int has = 0;
	for (int i = 0; i < f->nrules; i++) {
		const form_rule *r = &f->rules[i];
		if (r->t && !strcmp(to_major ? r->t : theirs_type(r), type)) {
			has = 1;
			if (r->as)
				newtype = to_major ? r->as : r->t;
		}
	}
	for (int i = 0; !to_major && !has && i < f->nrules; i++) {
		if (f->rules[i].t && f->rules[i].as && !strcmp(f->rules[i].t, type)) {
			free(t);
			return NULL;
		}
	}
	json_iter it;
	const char *k, *v, *ve;
	buf out = {0};
	int n = 0, got;
	if (iter_begin(&it, line, '{') < 0) {
		free(t);
		return NULL;
	}
	buf_puts(&out, "{");
	while ((got = iter_next(&it, &k, &v, &ve)) == 1) {
		char *key = decode_string(k);
		if (!key)
			break;
		const char *name = key;
		for (int i = 0; i < f->nrules && strcmp(key, "t"); i++) {
			const form_rule *r = &f->rules[i];
			if (r->t && strcmp(to_major ? r->t : theirs_type(r), type))
				continue;
			for (int x = 0; x < r->nfields; x++)
				if (!strcmp(key, to_major ? r->own[x] : r->theirs[x]))
					name = to_major ? r->theirs[x] : r->own[x];
		}
		buf_puts(&out, n++ ? "," : "");
		quote(&out, name);
		buf_puts(&out, ":");
		if (!strcmp(key, "t") && newtype)
			quote(&out, newtype);
		else
			buf_put(&out, v, (size_t)(ve - v));
		free(key);
	}
	free(t);
	if (got != 0) {
		free(out.s);
		return NULL;
	}
	buf_puts(&out, "}");
	return out.s;
}

/* arrived turns a line from the Shell into this library's form, the
 * major chosen when start comes (with none named, hello's proto: a system
 * that does not negotiate), what waited for it then sent; NULL (line
 * freed) for one to drop. */
static char *arrived(phoneui *ui, char *line)
{
	if (!ui->started) {
		char *t = phoneui_get_string(line, "t");
		if (t && !strcmp(t, "start")) {
			char *proto = phoneui_get_string(line, "proto");
			int major = proto && !strncmp(proto, PROTO_BASE, strlen(PROTO_BASE)) ? atoi(proto + strlen(PROTO_BASE)) : oldest_major(ui);
			free(proto);
			ui->form = NULL;
			for (int i = 0; i < ui->nforms; i++)
				if (ui->forms[i].major == major)
					ui->form = &ui->forms[i];
			ui->started = 1;
			for (size_t i = 0; i < ui->nheld; i++) {
				phoneui_send(ui, ui->held[i]);
				free(ui->held[i]);
			}
			ui->nheld = 0;
		}
		free(t);
	}
	if (!ui->form)
		return line;
	char *turned = turn(ui->form, line, 0);
	free(line);
	return turned;
}
