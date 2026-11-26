/* An app of the C SDK for TestSDKProtoMajors (shell/sdkproto_test.go): the
 * title of its page says what it saw. */
#include <stdio.h>
#include <string.h>

#include "phoneui.h"

static void page(phoneui *ui, const char *t, const char *title)
{
	char line[256];
	snprintf(line, sizeof line, "{\"t\":\"%s\",\"screen\":{\"id\":\"main\",\"title\":\"%s\",\"body\":[]}}", t, title);
	phoneui_send(ui, line);
}

int main(void)
{
	phoneui *ui = phoneui_open();
	if (!ui)
		return 1;
	phoneui_msg m;
	while (phoneui_next(ui, &m) == 0) {
		if (!strcmp(m.t, "start")) {
			page(ui, "reset", "started");
			phoneui_send(ui, "{\"t\":\"toast\",\"text\":\"hello from c\"}");
		} else if (!strcmp(m.t, "action")) {
			char title[128];
			snprintf(title, sizeof title, "action on %s", m.screen ? m.screen : "?");
			page(ui, "update", title);
		} else if (!strcmp(m.t, "resync")) {
			page(ui, "reset", "resynced");
		}
	}
	phoneui_close(ui);
	return 0;
}
