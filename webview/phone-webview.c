/*
 * phone-webview: the pixel side of web nodes (14 §5). One process per host
 * app, started by the shell inside the sandbox (the app's web uid, the app's
 * network permission); every web node of the app is a view in it, so the
 * app's pages share one network process, one cookie jar and one cache.
 * Each view renders off screen into a media surface of its own, which the
 * shell hands to the renderer the way it hands over video.
 *
 * fd 3 is a SOCK_SEQPACKET socket to the shell, one JSON object a packet;
 * every message but "ready" names its view:
 *   shell -> view  open {url,w,h,scale}  close  show {visible}
 *                  key {key: up|down|left|right|enter}  back
 *                  value {value}  submit            (the focused text field)
 *                  reply {id,ok,result|error}       (phone.call)
 *                  answer {id,ok,text}  choose {id,index}  (dialogs, list boxes)
 *                  control {op,text,value}          (the host app's page controls)
 *                  files {id,names}                 (a file field's choice)
 *   view -> shell  ready  surface {w,h} with the memfd (SCM_RIGHTS)  frame {seq}
 *                  state {url,title,loading,focus,can_back,can_forward,zoom,edit,value}
 *                  call {id,cap,args}  dialog {id,kind,message,default}
 *                  menu {id,items}  choose-file {id,types,multiple}  blocked {uri}
 *                  found {count}  download {name,mime,ok}  cleared {ok}
 *
 *   phone-webview [--gpu] [--memory-limit MB] [--cache-model viewer|browser|web] [--block RULES.json]
 *
 * --block: WebKit content blocker rules (share/phone/web/blocklist.json),
 * compiled once at start and applied to every view.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#include <glib-unix.h>
#include <wpe/unstable/fdo-shm.h>

#include "view.h"

#define CTL 3

static JSCContext *json;
static GHashTable *views; /* id -> struct view */
static WebKitWebContext *context;
static WebKitSettings *settings;
static WebKitUserContentFilter *filter;

void send_json(const char *msg) {
    if (send(CTL, msg, strlen(msg), MSG_NOSIGNAL) < 0 && errno == EPIPE)
        exit(0);
}

void send_with_fd(const char *msg, int fd) {
    char ctl[CMSG_SPACE(sizeof(int))] = {0};
    struct iovec iov = {(void *)msg, strlen(msg)};
    struct msghdr m = {.msg_iov = &iov, .msg_iovlen = 1, .msg_control = ctl, .msg_controllen = sizeof ctl};
    struct cmsghdr *c = CMSG_FIRSTHDR(&m);
    c->cmsg_level = SOL_SOCKET, c->cmsg_type = SCM_RIGHTS, c->cmsg_len = CMSG_LEN(sizeof(int));
    memcpy(CMSG_DATA(c), &fd, sizeof(int));
    if (sendmsg(CTL, &m, MSG_NOSIGNAL) < 0) {
        perror("phone-webview: handing over a surface");
        exit(1);
    }
}

/* A JSON string literal for s, escaped by JavaScriptCore. */
char *json_quote(const char *s) {
    JSCValue *v = jsc_value_new_string(json, s ? s : "");
    char *q = jsc_value_to_json(v, 0);
    g_object_unref(v);
    return q;
}

JSCContext *json_context(void) { return json; }

static JSCValue *prop(JSCValue *o, const char *name) { return jsc_value_object_get_property(o, name); }

static char *str(JSCValue *o, const char *name) {
    JSCValue *v = prop(o, name);
    char *s = jsc_value_is_undefined(v) || jsc_value_is_null(v) ? NULL : jsc_value_to_string(v);
    g_object_unref(v);
    return s;
}

static int num(JSCValue *o, const char *name) {
    JSCValue *v = prop(o, name);
    int n = jsc_value_is_number(v) ? jsc_value_to_int32(v) : 0;
    g_object_unref(v);
    return n;
}

static double real(JSCValue *o, const char *name) {
    JSCValue *v = prop(o, name);
    double n = jsc_value_is_number(v) ? jsc_value_to_double(v) : 0;
    g_object_unref(v);
    return n;
}

static gboolean flag(JSCValue *o, const char *name) {
    JSCValue *v = prop(o, name);
    gboolean b = jsc_value_to_boolean(v);
    g_object_unref(v);
    return b;
}

static void open_view(JSCValue *o, int id) {
    struct view *v = g_hash_table_lookup(views, GINT_TO_POINTER(id));
    if (!v) {
        int w = num(o, "w"), h = num(o, "h");
        double scale = real(o, "scale");
        if (id <= 0 || w < 64 || h < 64 || w > 1920 || h > 1920 || scale < 1 || scale > 4) {
            fprintf(stderr, "phone-webview: bad view %d (%dx%d at %.2f)\n", id, w, h, scale);
            return;
        }
        v = view_new(id, w, h, scale, context, settings, filter);
        g_hash_table_insert(views, GINT_TO_POINTER(id), v);
    }
    char *url = str(o, "url");
    view_open(v, url);
    g_free(url);
}

static void dispatch(JSCValue *o) {
    char *t = str(o, "t");
    int id = num(o, "view");
    struct view *v = g_hash_table_lookup(views, GINT_TO_POINTER(id));
    if (!t) {
    } else if (!strcmp(t, "open")) {
        open_view(o, id);
    } else if (!v) {
    } else if (!strcmp(t, "close")) {
        g_hash_table_remove(views, GINT_TO_POINTER(id));
        view_free(v);
    } else if (!strcmp(t, "show")) {
        view_show(v, flag(o, "visible"));
    } else if (!strcmp(t, "key")) {
        char *k = str(o, "key");
        if (k)
            view_key(v, k);
        g_free(k);
    } else if (!strcmp(t, "back")) {
        view_back(v);
    } else if (!strcmp(t, "value")) {
        char *s = str(o, "value");
        view_set_value(v, s);
        g_free(s);
    } else if (!strcmp(t, "submit")) {
        view_submit(v);
    } else if (!strcmp(t, "reply")) {
        gboolean ok = flag(o, "ok");
        JSCValue *res = prop(o, ok ? "result" : "error");
        char *rj = jsc_value_is_undefined(res) ? NULL : jsc_value_to_json(res, 0);
        view_reply(v, num(o, "id"), ok, rj);
        g_free(rj);
        g_object_unref(res);
    } else if (!strcmp(t, "answer")) {
        char *text = str(o, "text");
        view_answer(v, num(o, "id"), flag(o, "ok"), text);
        g_free(text);
    } else if (!strcmp(t, "choose")) {
        view_choose(v, num(o, "id"), num(o, "index"));
    } else if (!strcmp(t, "control")) {
        char *op = str(o, "op"), *text = str(o, "text");
        if (op)
            view_control(v, op, text, real(o, "value"));
        g_free(op), g_free(text);
    } else if (!strcmp(t, "files")) {
        JSCValue *names = prop(o, "names");
        GPtrArray *list = g_ptr_array_new_with_free_func(g_free);
        int n = jsc_value_is_array(names) ? num(names, "length") : 0;
        for (int i = 0; i < n && i < 16; i++) {
            JSCValue *e = jsc_value_object_get_property_at_index(names, (guint)i);
            if (jsc_value_is_string(e))
                g_ptr_array_add(list, jsc_value_to_string(e));
            g_object_unref(e);
        }
        g_ptr_array_add(list, NULL);
        view_files(v, num(o, "id"), (char **)list->pdata);
        g_ptr_array_free(list, TRUE);
        g_object_unref(names);
    }
    g_free(t);
}

static void on_download(WebKitNetworkSession *s, WebKitDownload *d, gpointer data) {
    (void)s, (void)data;
    WebKitWebView *web = webkit_download_get_web_view(d);
    GHashTableIter it;
    gpointer v;
    g_hash_table_iter_init(&it, views);
    while (g_hash_table_iter_next(&it, NULL, &v))
        if (((struct view *)v)->web == web) {
            view_download(d, v);
            return;
        }
    webkit_download_cancel(d);
}

static void on_filter(GObject *o, GAsyncResult *res, gpointer loop) {
    GError *err = NULL;
    filter = webkit_user_content_filter_store_save_finish(WEBKIT_USER_CONTENT_FILTER_STORE(o), res, &err);
    if (!filter)
        fprintf(stderr, "phone-webview: content blocker rules: %s\n", err ? err->message : "not compiled");
    g_clear_error(&err);
    g_main_loop_quit(loop);
}

/* The blocker rules, compiled into the process's cache before any view. */
static void load_filter(const char *path) {
    gchar *rules = NULL;
    gsize len = 0;
    if (!g_file_get_contents(path, &rules, &len, NULL)) {
        fprintf(stderr, "phone-webview: cannot read %s\n", path);
        return;
    }
    char *dir = g_build_filename(g_get_user_cache_dir(), "content-filters", NULL);
    WebKitUserContentFilterStore *st = webkit_user_content_filter_store_new(dir);
    GBytes *b = g_bytes_new_take(rules, len);
    GMainLoop *loop = g_main_loop_new(NULL, FALSE);
    webkit_user_content_filter_store_save(st, "phone", b, NULL, on_filter, loop);
    g_main_loop_run(loop);
    g_main_loop_unref(loop);
    g_bytes_unref(b);
    g_object_unref(st);
    g_free(dir);
}

static gboolean on_control(gint fd, GIOCondition cond, gpointer d) {
    (void)d;
    static char buf[1 << 16];
    ssize_t n = (cond & G_IO_IN) ? recv(fd, buf, sizeof buf - 1, 0) : 0;
    if (n <= 0)
        exit(0);
    buf[n] = 0;
    JSCValue *o = jsc_value_new_from_json(json, buf);
    if (o && jsc_value_is_object(o))
        dispatch(o);
    if (o)
        g_object_unref(o);
    return G_SOURCE_CONTINUE;
}

int main(int argc, char **argv) {
    gboolean gpu = FALSE;
    int memory_limit = 0;
    const char *block = NULL;
    const char *cache = "web";
    for (int i = 1; i < argc; i++) {
        if (!strcmp(argv[i], "--gpu"))
            gpu = TRUE;
        else if (!strcmp(argv[i], "--memory-limit") && i + 1 < argc)
            memory_limit = atoi(argv[++i]);
        else if (!strcmp(argv[i], "--block") && i + 1 < argc)
            block = argv[++i];
        else if (!strcmp(argv[i], "--cache-model") && i + 1 < argc)
            cache = argv[++i];
    }
    if (fcntl(CTL, F_GETFD) < 0) {
        fprintf(stderr, "usage: phone-webview [--gpu] [--memory-limit MB] [--cache-model viewer|browser|web] [--block RULES.json], with the shell's socket as fd 3\n");
        return 2;
    }
    /* Without a GPU device in its sandbox WebKit paints on the CPU (its web
     * processes inherit the setting); frames leave through shared memory
     * either way. */
    if (!gpu)
        g_setenv("WEBKIT_SKIA_ENABLE_CPU_RENDERING", "1", TRUE);
    if (!wpe_loader_init("libWPEBackend-fdo-1.0.so.1") || !wpe_fdo_initialize_shm()) {
        fprintf(stderr, "phone-webview: WPEBackend-fdo (shared memory) unavailable\n");
        return 1;
    }
    if (memory_limit > 0) {
        /* Above the limit WebKit first frees caches, then ends the web
         * process, which the view reloads (view.c). */
        WebKitMemoryPressureSettings *mp = webkit_memory_pressure_settings_new();
        webkit_memory_pressure_settings_set_memory_limit(mp, (guint)memory_limit);
        webkit_network_session_set_memory_pressure_settings(mp);
        context = WEBKIT_WEB_CONTEXT(g_object_new(WEBKIT_TYPE_WEB_CONTEXT, "memory-pressure-settings", mp, NULL));
        webkit_memory_pressure_settings_free(mp);
    } else {
        context = g_object_ref(webkit_web_context_get_default());
    }
    /* The memory tier's cache (the shell picks it): on the smallest phones
     * no page cache and no memory cache ("viewer"), so going back reloads
     * a page instead of keeping the last ones in memory, and pages stay
     * within the web process tree's cgroup instead of thrashing in it. */
    if (!strcmp(cache, "viewer"))
        webkit_web_context_set_cache_model(context, WEBKIT_CACHE_MODEL_DOCUMENT_VIEWER);
    else if (!strcmp(cache, "browser"))
        webkit_web_context_set_cache_model(context, WEBKIT_CACHE_MODEL_DOCUMENT_BROWSER);
    else
        webkit_web_context_set_cache_model(context, WEBKIT_CACHE_MODEL_WEB_BROWSER);
    settings = webkit_settings_new();
    webkit_settings_set_enable_developer_extras(settings, FALSE);
    webkit_settings_set_javascript_can_open_windows_automatically(settings, FALSE);
    webkit_settings_set_default_font_size(settings, 15);
    webkit_settings_set_user_agent_with_application_details(settings, "Kario", "0.6");

    /* JSON goes through JavaScriptCore, already linked in. Its context must
     * come after WebKit's own setup: one made first stops page loads. */
    json = jsc_context_new();
    views = g_hash_table_new(NULL, NULL);
    if (block)
        load_filter(block);
    g_signal_connect(webkit_network_session_get_default(), "download-started", G_CALLBACK(on_download), NULL);
    g_unix_fd_add(CTL, G_IO_IN | G_IO_HUP | G_IO_ERR, on_control, NULL);
    send_json("{\"t\":\"ready\"}");
    GMainLoop *loop = g_main_loop_new(NULL, FALSE);
    g_main_loop_run(loop);
    return 0;
}
