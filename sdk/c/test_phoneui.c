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
	shell_write(sv[1], "{\"t\":\"action\",\"screen\":\"main\",\"node\":\"inc\",\"action\":\"inc\","
	                   "\"value\":\"\\u4f60\\u597d \\ud83d\\ude00 \\\"q\\\"\",\"fields\":{\"name\":\"\\u5f20\\u4e09\",\"n\":3}}");
	phoneui_msg m;
	CHECK(phoneui_next(ui, &m) == 0);
	CHECK(!strcmp(shell_read(sv[1]), "{\"t\":\"pong\",\"value\":\"7\"}"));
	CHECK(m.t && !strcmp(m.t, "action"));
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
	if (failures)
		return 1;
	puts("phoneui tests passed");
	return 0;
}
