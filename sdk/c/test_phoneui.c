/* Unit test: a fake shell on the other end of a socketpair. */
#define _POSIX_C_SOURCE 200809L
#include "phoneui.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

static int failures;
#define CHECK(c) do { if (!(c)) { fprintf(stderr, "%s:%d: %s\n", __FILE__, __LINE__, #c); failures++; } } while (0)

static char *shell_read(int fd)
{
	static char line[65536];
	size_t n = 0;
	while (n < sizeof line - 1 && read(fd, line + n, 1) == 1 && line[n] != '\n')
		n++;
	line[n] = 0;
	return line;
}

static void shell_write(int fd, const char *s)
{
	if (write(fd, s, strlen(s)) < 0 || write(fd, "\n", 1) < 0)
		perror("write");
}

/* shell_write_fds sends a line with files attached (SCM_RIGHTS). */
static void shell_write_fds(int fd, const char *s, const int *fds, int n)
{
	union {
		struct cmsghdr h;
		char space[CMSG_SPACE(8 * sizeof(int))];
	} ctl;
	char line[4096];
	snprintf(line, sizeof line, "%s\n", s);
	struct iovec iov = {.iov_base = line, .iov_len = strlen(line)};
	struct msghdr mh = {.msg_iov = &iov, .msg_iovlen = 1, .msg_control = ctl.space, .msg_controllen = CMSG_SPACE(n * sizeof(int))};
	struct cmsghdr *c = CMSG_FIRSTHDR(&mh);
	c->cmsg_level = SOL_SOCKET;
	c->cmsg_type = SCM_RIGHTS;
	c->cmsg_len = CMSG_LEN(n * sizeof(int));
	memcpy(CMSG_DATA(c), fds, n * sizeof(int));
	if (sendmsg(fd, &mh, 0) < 0)
		perror("sendmsg");
}

int main(void)
{
	int sv[2];
	if (socketpair(AF_UNIX, SOCK_STREAM, 0, sv))
		return 1;
	char fd[16];
	snprintf(fd, sizeof fd, "%d", sv[0]);
	setenv("PHONE_UI_FD", fd, 1);
	setenv("PHONE_APP_ID", "com.example.test", 1);
	phoneui *ui = phoneui_open();
	CHECK(ui != NULL);
	CHECK(!strcmp(shell_read(sv[1]), "{\"t\":\"hello\",\"app\":\"com.example.test\",\"proto\":\"phone.ui/0\"}"));

	/* A ping is answered and not handed to the app; escapes are decoded. */
	shell_write(sv[1], "{\"t\":\"ping\",\"value\":\"7\"}");
	shell_write(sv[1], "{\"t\":\"action\",\"screen_id\":\"main\",\"node\":\"inc\",\"action\":\"inc\","
	                   "\"value\":\"\\u4f60\\u597d \\ud83d\\ude00 \\\"q\\\"\",\"fields\":{\"name\":\"\\u5f20\\u4e09\",\"n\":3}}");
	phoneui_msg m;
	CHECK(phoneui_next(ui, &m) == 0);
	CHECK(!strcmp(shell_read(sv[1]), "{\"t\":\"pong\",\"value\":\"7\"}"));
	CHECK(m.t && !strcmp(m.t, "action"));
	CHECK(m.screen && !strcmp(m.screen, "main"));
	CHECK(m.action && !strcmp(m.action, "inc"));
	CHECK(m.value && !strcmp(m.value, "\u4f60\u597d \U0001F600 \"q\""));
	char *name = phoneui_get_string(m.fields, "name");
	CHECK(name && !strcmp(name, "\u5f20\u4e09"));
	free(name);
	CHECK(m.event == NULL);

	/* A call waits for its own result; what arrives meanwhile is kept. */
	shell_write(sv[1], "{\"t\":\"event\",\"event\":\"job\",\"value\":\"j1\"}");
	shell_write(sv[1], "{\"t\":\"res\",\"req\":1,\"ok\":true,\"data\":{\"ok\":true}}");
	const char *res = NULL, *err = NULL;
	CHECK(phoneui_call(ui, "clipboard.set", "{\"text\":\"x\"}", &res, &err) == 0);
	CHECK(!strcmp(shell_read(sv[1]), "{\"t\":\"req\",\"req\":1,\"cap\":\"clipboard.set\",\"args\":{\"text\":\"x\"}}"));
	CHECK(res && !strcmp(res, "{\"ok\":true}"));
	shell_write(sv[1], "{\"t\":\"res\",\"req\":2,\"ok\":false,\"err\":\"permission denied by user\"}");
	CHECK(phoneui_call(ui, "camera.capture", NULL, &res, &err) == -1);
	shell_read(sv[1]);
	CHECK(err && !strcmp(err, "permission denied by user"));
	CHECK(phoneui_pending(ui));
	CHECK(phoneui_next(ui, &m) == 0);
	CHECK(m.t && !strcmp(m.t, "event") && !strcmp(m.event, "job") && !strcmp(m.value, "j1"));
	CHECK(!phoneui_pending(ui));

	/* A new time zone is in force before the app sees the event. */
	shell_write(sv[1], "{\"t\":\"event\",\"event\":\"time.zone\",\"value\":\"America/Mexico_City\"}");
	CHECK(phoneui_next(ui, &m) == 0 && !strcmp(m.event, "time.zone"));
	CHECK(getenv("TZ") && !strcmp(getenv("TZ"), "America/Mexico_City"));

	/* Two messages in one read: the second waits inside, unseen by poll(). */
	shell_write(sv[1], "{\"t\":\"event\",\"event\":\"a\"}\n{\"t\":\"event\",\"event\":\"b\"}");
	CHECK(phoneui_next(ui, &m) == 0 && !strcmp(m.event, "a"));
	CHECK(phoneui_pending(ui));
	CHECK(phoneui_next(ui, &m) == 0 && !strcmp(m.event, "b"));
	CHECK(!phoneui_pending(ui));

	/* Without blocking: nothing yet, then a ping alone (answered, nothing
	 * to hand over), then a message. */
	CHECK(phoneui_try_next(ui, &m) == 0);
	shell_write(sv[1], "{\"t\":\"ping\",\"value\":\"8\"}");
	CHECK(phoneui_try_next(ui, &m) == 0);
	CHECK(!strcmp(shell_read(sv[1]), "{\"t\":\"pong\",\"value\":\"8\"}"));
	shell_write(sv[1], "{\"t\":\"event\",\"event\":\"c\"}");
	CHECK(phoneui_try_next(ui, &m) == 1 && !strcmp(m.event, "c"));
	CHECK(phoneui_fds(ui) == 0 && phoneui_take_fd(ui, 0) == -1);

	/* Files travel with the message that counts them: a call's result has
	 * its own, one kept meanwhile keeps its own; what is not taken is
	 * closed by the next call. */
	int a[2], b[2];
	CHECK(pipe(a) == 0 && pipe(b) == 0);
	shell_write_fds(sv[1], "{\"t\":\"event\",\"event\":\"d\",\"fds\":1}", &b[0], 1);
	int both[2] = {a[0], b[1]};
	shell_write_fds(sv[1], "{\"t\":\"res\",\"req\":3,\"ok\":true,\"data\":{\"id\":\"media:p1\"},\"fds\":2}", both, 2);
	close(a[0]);
	close(b[0]);
	close(b[1]);
	CHECK(phoneui_call(ui, "media.pick", "{\"kind\":\"image\"}", &res, &err) == 0);
	shell_read(sv[1]);
	CHECK(phoneui_fds(ui) == 2);
	int got = phoneui_take_fd(ui, 0);
	CHECK(got >= 0 && phoneui_take_fd(ui, 0) == -1);
	CHECK(write(a[1], "x", 1) == 1);
	char c = 0;
	CHECK(read(got, &c, 1) == 1 && c == 'x');
	close(got);
	CHECK(phoneui_next(ui, &m) == 0 && !strcmp(m.event, "d"));
	CHECK(phoneui_fds(ui) == 1);
	shell_write(sv[1], "{\"t\":\"event\",\"event\":\"e\"}");
	CHECK(phoneui_next(ui, &m) == 0 && !strcmp(m.event, "e") && phoneui_fds(ui) == 0);
	close(a[1]);

	/* The writer nests and escapes. */
	phoneui_json *j = phoneui_json_new();
	phoneui_json_begin_object(j);
	phoneui_json_key(j, "a");
	phoneui_json_begin_array(j);
	phoneui_json_string(j, "x\"\n");
	phoneui_json_int(j, -3);
	phoneui_json_bool(j, 1);
	phoneui_json_begin_object(j);
	phoneui_json_end_object(j);
	phoneui_json_end_array(j);
	phoneui_json_key(j, "b");
	phoneui_json_string(j, "\u4e2d");
	phoneui_json_end_object(j);
	CHECK(!strcmp(phoneui_json_text(j), "{\"a\":[\"x\\\"\\u000a\",-3,true,{}],\"b\":\"\u4e2d\"}"));
	phoneui_json_free(j);

	close(sv[1]);
	CHECK(phoneui_try_next(ui, &m) == -1);
	CHECK(phoneui_next(ui, &m) == -1);
	phoneui_close(ui);

	/* Speaking a second major (a made-up phone.ui/1, as the Shell's tests
	 * have it): hello offers both, what is sent waits for start, and a
	 * system that names phone.ui/1 gets its form both ways (a toast is a
	 * notice, the page of an action is "page", resync is resend); one
	 * that names phone.ui/0, or none (it does not negotiate), the
	 * library's own. */
	setenv("PHONE_UI_TEST_FORMS",
	       "[{\"major\":1,\"rules\":[{\"t\":\"toast\",\"as\":\"notice\",\"fields\":{\"text\":\"body\"}},"
	       "{\"t\":\"resync\",\"as\":\"resend\"},{\"fields\":{\"screen_id\":\"page\"}}]}]",
	       1);
	const char *starts[] = {"{\"t\":\"start\",\"proto\":\"phone.ui/1\"}", "{\"t\":\"start\",\"proto\":\"phone.ui/0\"}", "{\"t\":\"start\"}"};
	for (int v = 0; v < 3; v++) {
		int v1 = v == 0;
		if (socketpair(AF_UNIX, SOCK_STREAM, 0, sv))
			return 1;
		snprintf(fd, sizeof fd, "%d", sv[0]);
		setenv("PHONE_UI_FD", fd, 1);
		ui = phoneui_open();
		CHECK(ui != NULL);
		CHECK(!strcmp(shell_read(sv[1]), "{\"t\":\"hello\",\"app\":\"com.example.test\",\"proto\":\"phone.ui/0\",\"protos\":[\"phone.ui/1\",\"phone.ui/0\"]}"));
		CHECK(phoneui_send(ui, "{\"t\":\"toast\",\"text\":\"early\"}") == 0);
		CHECK(phoneui_try_next(ui, &m) == 0);
		shell_write(sv[1], starts[v]);
		CHECK(phoneui_next(ui, &m) == 0 && !strcmp(m.t, "start"));
		CHECK(!strcmp(shell_read(sv[1]), v1 ? "{\"t\":\"notice\",\"body\":\"early\"}" : "{\"t\":\"toast\",\"text\":\"early\"}"));
		shell_write(sv[1], v1 ? "{\"t\":\"action\",\"page\":\"main\",\"action\":\"go\"}" : "{\"t\":\"action\",\"screen_id\":\"main\",\"action\":\"go\"}");
		CHECK(phoneui_next(ui, &m) == 0 && !strcmp(m.t, "action") && m.screen && !strcmp(m.screen, "main"));
		shell_write(sv[1], v1 ? "{\"t\":\"resend\"}" : "{\"t\":\"resync\"}");
		CHECK(phoneui_next(ui, &m) == 0 && !strcmp(m.t, "resync"));
		/* A message of the library's form phone.ui/1 lacks is dropped. */
		if (v1) {
			shell_write(sv[1], "{\"t\":\"toast\",\"text\":\"not phone.ui/1\"}\n{\"t\":\"event\",\"event\":\"after\"}");
			CHECK(phoneui_next(ui, &m) == 0 && !strcmp(m.t, "event") && !strcmp(m.event, "after"));
		}
		shell_write(sv[1], "{\"t\":\"ping\",\"value\":\"9\"}");
		CHECK(phoneui_try_next(ui, &m) == 0);
		CHECK(!strcmp(shell_read(sv[1]), "{\"t\":\"pong\",\"value\":\"9\"}"));
		close(sv[1]);
		phoneui_close(ui);
	}
	unsetenv("PHONE_UI_TEST_FORMS");

	if (failures)
		return 1;
	puts("phoneui tests passed");
	return 0;
}
