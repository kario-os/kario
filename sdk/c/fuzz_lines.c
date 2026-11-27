/* Reads what a fuzzed Shell sends through libphoneui, as an app would
 * (tools/cfuzz): every message's members, a field of its inputs, and a
 * capability call whose answer comes from the same lines. Built with
 * KARIO_LIBFUZZER defined it is a libFuzzer target instead, the input
 * coming through a socket pair as the Shell's lines would (make
 * fuzz-libfuzzer). */
#define _POSIX_C_SOURCE 200809L

#include "phoneui.h"

#include <stdlib.h>
#include <string.h>

static int read_shell(void)
{
	phoneui *ui = phoneui_open();
	if (!ui)
		return 2;
	phoneui_msg m;
	size_t n = 0;
	while (phoneui_next(ui, &m) == 0) {
		const char *s[] = {m.t, m.screen, m.node, m.action, m.value, m.event, m.state, m.fields, m.line};
		for (size_t i = 0; i < sizeof s / sizeof *s; i++)
			n += s[i] ? strlen(s[i]) : 0;
		if (m.fields)
			free(phoneui_get_string(m.fields, "q"));
		if (m.t && !strcmp(m.t, "action")) {
			const char *res = NULL, *err = NULL;
			phoneui_call(ui, "clipboard.read", "{\"x\":[1,\"\\u00e9\"]}", &res, &err);
			n += res ? strlen(res) : 0;
		}
	}
	phoneui_close(ui);
	return n == (size_t)-1;
}

#ifdef KARIO_LIBFUZZER
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <sys/socket.h>
#include <unistd.h>

struct shell {
	int fd;
	const uint8_t *data;
	size_t len;
};

/* The Shell's end writes the input and, apart, reads what the app sends,
 * so neither direction fills up while the other waits. */
static void *shell_write(void *arg)
{
	struct shell *s = arg;
	for (size_t off = 0; off < s->len;) {
		ssize_t w = send(s->fd, s->data + off, s->len - off, MSG_NOSIGNAL);
		if (w <= 0)
			break;
		off += (size_t)w;
	}
	shutdown(s->fd, SHUT_WR);
	return NULL;
}

static void *shell_read(void *arg)
{
	struct shell *s = arg;
	char buf[4096];
	while (read(s->fd, buf, sizeof buf) > 0) {
	}
	return NULL;
}

int LLVMFuzzerInitialize(int *argc, char ***argv)
{
	(void)argc;
	(void)argv;
	setenv("PHONE_APP_ID", "com.example.c", 1);
	return 0;
}

int LLVMFuzzerTestOneInput(const uint8_t *data, size_t len)
{
	int fds[2];
	if (socketpair(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0, fds))
		abort();
	char fd[16];
	snprintf(fd, sizeof fd, "%d", fds[0]);
	setenv("PHONE_UI_FD", fd, 1);
	struct shell s = {fds[1], data, len};
	pthread_t w, r;
	if (pthread_create(&w, NULL, shell_write, &s) || pthread_create(&r, NULL, shell_read, &s))
		abort();
	if (read_shell() == 2)
		close(fds[0]);
	pthread_join(w, NULL);
	pthread_join(r, NULL);
	close(fds[1]);
	return 0;
}
#else
int main(void) { return read_shell(); }
#endif
