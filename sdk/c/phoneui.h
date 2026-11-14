/*
 * phoneui: the phone.Ui protocol for C and C++ apps (22-developer-api.md §2).
 *
 * The shell starts an app with a Unix socket on fd 3 (PHONE_UI_FD); every
 * message is one line of JSON. This library opens the socket, says hello,
 * answers the shell's responsiveness pings, hands the app the rest of the
 * messages, sends screens, and calls capabilities synchronously. It is
 * plain C11 with no dependencies, so a vendor's existing C++ code links it
 * as is.
 *
 * Strings in a phoneui_msg are decoded (UTF-8) and stay valid until the next
 * phoneui_next() or phoneui_call() on the same connection. Not thread-safe:
 * use one connection from one thread.
 */
#ifndef PHONEUI_H
#define PHONEUI_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

#define PHONEUI_PROTO "phone.ui/0"

typedef struct phoneui phoneui;

/* A message from the shell. Members absent from it are NULL (or 0). */
typedef struct {
	/* start, action, popped, lifecycle, event, notif_action, res; resync:
	 * a new Shell took over the connection after the previous one crashed
	 * or restarted, so send the pages again (reset, then push the rest).
	 * Calls the previous Shell had not answered get no answer. */
	const char *t;
	const char *screen; /* action, popped: the screen id */
	const char *node;   /* action: the node id */
	const char *action; /* action, notif_action: the action id */
	const char *value;  /* start: the deep link; action: the input's value; event: its value */
	const char *event;  /* event: its name */
	const char *state;  /* lifecycle: foreground | background */
	const char *fields; /* action: the screen's inputs, a JSON object (raw) */
	const char *line;   /* the whole message, raw JSON */
} phoneui_msg;

/* Opens the connection the shell handed over and says hello; NULL when the
 * process was not started by the shell. */
phoneui *phoneui_open(void);
void phoneui_close(phoneui *ui);

/* Waits for the next message. Returns 0, or -1 when the shell closed the
 * connection. Pings are answered here and never returned. */
int phoneui_next(phoneui *ui, phoneui_msg *msg);

/* For an event loop: the next message if one has arrived, without
 * blocking. Returns 1 with msg filled in, 0 when none is there yet (pings
 * are answered all the same), -1 when the shell closed the connection. */
int phoneui_try_next(phoneui *ui, phoneui_msg *msg);

/* Whether a whole message already waits inside the library (read along
 * with an earlier one, or kept during phoneui_call), which poll() on the
 * connection cannot see: the loop then takes it without polling. */
int phoneui_pending(const phoneui *ui);

/* Sends one message: a JSON object, without the newline. 0 or -1. */
int phoneui_send(phoneui *ui, const char *json);

/* Calls a capability (22-developer-api.md §4) and waits for the result.
 * args is a JSON value or NULL. On success returns 0 and, if result is not
 * NULL, the result's raw JSON; on failure -1 and, if err is not NULL, the
 * reason. Both strings are valid until the next call. Messages arriving
 * meanwhile are kept for phoneui_next(). */
int phoneui_call(phoneui *ui, const char *cap, const char *args, const char **result, const char **err);

/* The decoded string member name of a JSON object (e.g. msg->fields), or
 * NULL. The returned string is malloc'd: free() it. */
char *phoneui_get_string(const char *object_json, const char *name);

/* A JSON writer for screens and capability arguments. Calls nest the way
 * the JSON does; commas and escaping are taken care of. */
typedef struct phoneui_json phoneui_json;
phoneui_json *phoneui_json_new(void);
void phoneui_json_free(phoneui_json *j);
void phoneui_json_begin_object(phoneui_json *j);
void phoneui_json_end_object(phoneui_json *j);
void phoneui_json_begin_array(phoneui_json *j);
void phoneui_json_end_array(phoneui_json *j);
void phoneui_json_key(phoneui_json *j, const char *key);
void phoneui_json_string(phoneui_json *j, const char *s);
void phoneui_json_int(phoneui_json *j, long long v);
void phoneui_json_bool(phoneui_json *j, int v);
/* The text written so far; owned by the writer. */
const char *phoneui_json_text(const phoneui_json *j);

#ifdef __cplusplus
}
#endif

#endif
