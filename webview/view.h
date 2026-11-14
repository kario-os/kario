/* One page of a host app: a WebKit view rendering off screen into its own
 * media surface. All views of an app live in one phone-webview process and
 * share its network process and storage. */
#pragma once

#include <stdint.h>
#include <wpe/fdo.h>
#include <wpe/webkit.h>

struct view {
    int id;
    int w, h; /* the surface, physical pixels */
    struct wpe_view_backend_exportable_fdo *exportable;
    struct wpe_view_backend *backend;
    WebKitWebView *web;
    uint8_t *surface;
    size_t surface_len;
    int surface_fd;
    uint32_t seq;
    char focus[256];
    char edit[16]; /* the focused text field's kind, "" when none */
    char *value;   /* its text */
    GHashTable *dialogs; /* id -> WebKitScriptDialog, waiting for the shell */
    GHashTable *menus;   /* id -> WebKitOptionMenu */
    GHashTable *choosers; /* id -> WebKitFileChooserRequest */
    int next_ask;
    gint64 crashes[3]; /* when the web process last ended, newest first */
};

struct view *view_new(int id, int w, int h, double scale, WebKitWebContext *ctx, WebKitSettings *settings,
                      WebKitUserContentFilter *filter);
void view_free(struct view *v);
void view_open(struct view *v, const char *url);
void view_key(struct view *v, const char *key);
void view_back(struct view *v);
void view_show(struct view *v, gboolean visible);
void view_set_value(struct view *v, const char *value);
void view_submit(struct view *v);
void view_reply(struct view *v, int id, gboolean ok, const char *json);
void view_answer(struct view *v, int id, gboolean ok, const char *text);
void view_choose(struct view *v, int id, int index);
/* The host app's page controls: reload, forward, stop, zoom {value},
 * find {text}, find_next, find_prev, find_end, clear_data, sites (the
 * sites keeping data, for the shell's list); and the shell's own
 * forget_site {text: the site}. */
void view_control(struct view *v, const char *op, const char *text, double value);
/* The files the user picked for the page's file field (names under
 * $HOME/uploads), or none: the field stays empty. */
void view_files(struct view *v, int id, char **names);
/* Downloads go to $HOME/downloads; the shell takes them into Files. */
void view_download(WebKitDownload *d, struct view *v);

/* phone-webview.c: the control socket and JSON. */
void send_json(const char *json);
void send_with_fd(const char *json, int fd);
char *json_quote(const char *s);
JSCContext *json_context(void);
