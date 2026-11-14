#define _GNU_SOURCE
#include "view.h"

#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

#include <wayland-server.h>
#include <wpe/unstable/fdo-shm.h>

#include "scripts.h"

#define HEADER 32

/* The surface: memfd, a 32-byte header ("PHSURF1\0", width, height, the
 * newest slot, the two slots' frame numbers), then two RGB slots (the
 * layout of fw/media/surface.go). Its size is sealed, so the renderer's
 * mapping stays valid. */
static void make_surface(struct view *v) {
    v->surface_len = HEADER + 2 * (size_t)v->w * v->h * 3;
    v->surface_fd = memfd_create("phone-webview", MFD_CLOEXEC | MFD_ALLOW_SEALING);
    if (v->surface_fd < 0 || ftruncate(v->surface_fd, v->surface_len) < 0) {
        perror("phone-webview: surface");
        exit(1);
    }
    fcntl(v->surface_fd, F_ADD_SEALS, F_SEAL_SHRINK | F_SEAL_GROW | F_SEAL_SEAL);
    v->surface = mmap(NULL, v->surface_len, PROT_READ | PROT_WRITE, MAP_SHARED, v->surface_fd, 0);
    if (v->surface == MAP_FAILED) {
        perror("phone-webview: mmap");
        exit(1);
    }
    memcpy(v->surface, "PHSURF1", 8);
    uint32_t wh[2] = {(uint32_t)v->w, (uint32_t)v->h};
    memcpy(v->surface + 8, wh, 8);
    char msg[128];
    snprintf(msg, sizeof msg, "{\"t\":\"surface\",\"view\":%d,\"w\":%d,\"h\":%d}", v->id, v->w, v->h);
    send_with_fd(msg, v->surface_fd);
}

/* A frame from WebKit: XRGB/ARGB little endian into the slot not shown,
 * flip, then the frame number to the shell. WebKit starts the next frame
 * only after frame_complete, which comes after the message. */
static void on_shm_frame(void *data, struct wpe_fdo_shm_exported_buffer *buf) {
    struct view *v = data;
    struct wl_shm_buffer *shm = wpe_fdo_shm_exported_buffer_get_shm_buffer(buf);
    wl_shm_buffer_begin_access(shm);
    int bw = wl_shm_buffer_get_width(shm), bh = wl_shm_buffer_get_height(shm), stride = wl_shm_buffer_get_stride(shm);
    const uint8_t *px = wl_shm_buffer_get_data(shm);
    uint32_t latest = __atomic_load_n((uint32_t *)(v->surface + 16), __ATOMIC_ACQUIRE);
    uint32_t next = 1 - (latest & 1);
    uint8_t *slot = v->surface + HEADER + (size_t)next * v->w * v->h * 3;
    for (int y = 0; y < v->h; y++) {
        uint8_t *out = slot + (size_t)y * v->w * 3;
        if (y >= bh) {
            memset(out, 255, (size_t)v->w * 3);
            continue;
        }
        const uint8_t *in = px + (size_t)y * stride;
        for (int x = 0; x < v->w; x++, out += 3) {
            if (x < bw)
                out[0] = in[x * 4 + 2], out[1] = in[x * 4 + 1], out[2] = in[x * 4];
            else
                out[0] = out[1] = out[2] = 255;
        }
    }
    wl_shm_buffer_end_access(shm);
    wpe_view_backend_exportable_fdo_dispatch_release_shm_exported_buffer(v->exportable, buf);
    v->seq++;
    __atomic_store_n((uint32_t *)(v->surface + 20 + 4 * next), v->seq, __ATOMIC_RELEASE);
    __atomic_store_n((uint32_t *)(v->surface + 16), next, __ATOMIC_RELEASE);
    char msg[96];
    snprintf(msg, sizeof msg, "{\"t\":\"frame\",\"view\":%d,\"seq\":%u}", v->id, v->seq);
    send_json(msg);
    wpe_view_backend_exportable_fdo_dispatch_frame_complete(v->exportable);
}

static void send_state(struct view *v) {
    char *url = json_quote(webkit_web_view_get_uri(v->web));
    char *title = json_quote(webkit_web_view_get_title(v->web));
    char *focus = json_quote(v->focus);
    char *edit = json_quote(v->edit);
    char *value = json_quote(v->value);
    char *msg = g_strdup_printf("{\"t\":\"state\",\"view\":%d,\"url\":%s,\"title\":%s,\"loading\":%s,\"focus\":%s,"
                                "\"can_back\":%s,\"can_forward\":%s,\"zoom\":%.2f,\"edit\":%s,\"value\":%s}",
                                v->id, url, title, webkit_web_view_is_loading(v->web) ? "true" : "false", focus,
                                webkit_web_view_can_go_back(v->web) ? "true" : "false",
                                webkit_web_view_can_go_forward(v->web) ? "true" : "false",
                                webkit_web_view_get_zoom_level(v->web), edit, value);
    send_json(msg);
    g_free(msg), g_free(url), g_free(title), g_free(focus), g_free(edit), g_free(value);
}

static void clear_focus(struct view *v) {
    v->focus[0] = v->edit[0] = 0;
    g_free(v->value);
    v->value = NULL;
}

static void run_js(struct view *v, const char *js) {
    webkit_web_view_evaluate_javascript(v->web, js, -1, NULL, NULL, NULL, NULL, NULL);
}

static char *string_prop(JSCValue *o, const char *name) {
    JSCValue *p = jsc_value_object_get_property(o, name);
    char *s = jsc_value_is_undefined(p) || jsc_value_is_null(p) ? NULL : jsc_value_to_string(p);
    g_object_unref(p);
    return s;
}

static void on_nav(WebKitUserContentManager *m, JSCValue *value, gpointer data) {
    (void)m;
    struct view *v = data;
    char *s = jsc_value_to_string(value);
    JSCValue *o = jsc_value_new_from_json(json_context(), s);
    g_free(s);
    if (!o)
        return;
    if (jsc_value_is_object(o)) {
        char *focus = string_prop(o, "focus"), *edit = string_prop(o, "edit");
        g_strlcpy(v->focus, focus ? focus : "", sizeof v->focus);
        g_strlcpy(v->edit, edit ? edit : "", sizeof v->edit);
        g_free(v->value);
        v->value = string_prop(o, "value");
        g_free(focus), g_free(edit);
        send_state(v);
    }
    g_object_unref(o);
}

/* phone.call(cap, args) from the page, to the shell's broker. */
static void on_call(WebKitUserContentManager *m, JSCValue *value, gpointer data) {
    (void)m;
    struct view *v = data;
    char *s = jsc_value_to_string(value);
    JSCValue *o = jsc_value_new_from_json(json_context(), s);
    g_free(s);
    if (!o)
        return;
    if (jsc_value_is_object(o)) {
        JSCValue *id = jsc_value_object_get_property(o, "id");
        JSCValue *args = jsc_value_object_get_property(o, "args");
        char *cap = string_prop(o, "cap");
        char *capq = json_quote(cap);
        char *argj = jsc_value_to_json(args, 0);
        char *msg = g_strdup_printf("{\"t\":\"call\",\"view\":%d,\"id\":%d,\"cap\":%s,\"args\":%s}", v->id,
                                    jsc_value_to_int32(id), capq, argj ? argj : "{}");
        send_json(msg);
        g_free(msg), g_free(capq), g_free(cap), g_free(argj);
        g_object_unref(id), g_object_unref(args);
    }
    g_object_unref(o);
}

static void on_change(GObject *o, GParamSpec *p, gpointer data) {
    (void)o, (void)p;
    send_state(data);
}

/* alert, confirm, prompt and leave-page questions go to the shell, which
 * shows them as system dialogs naming the page; the page waits. */
static gboolean on_script_dialog(WebKitWebView *web, WebKitScriptDialog *d, gpointer data) {
    (void)web;
    struct view *v = data;
    static const char *const kinds[] = {"alert", "confirm", "prompt", "beforeunload"};
    WebKitScriptDialogType type = webkit_script_dialog_get_dialog_type(d);
    int id = ++v->next_ask;
    g_hash_table_insert(v->dialogs, GINT_TO_POINTER(id), webkit_script_dialog_ref(d));
    char *message = json_quote(webkit_script_dialog_get_message(d));
    char *def = json_quote(type == WEBKIT_SCRIPT_DIALOG_PROMPT ? webkit_script_dialog_prompt_get_default_text(d) : "");
    char *msg = g_strdup_printf("{\"t\":\"dialog\",\"view\":%d,\"id\":%d,\"kind\":\"%s\",\"message\":%s,\"default\":%s}", v->id, id,
                                (unsigned)type < G_N_ELEMENTS(kinds) ? kinds[type] : "alert", message, def);
    send_json(msg);
    g_free(msg), g_free(message), g_free(def);
    return TRUE;
}

/* A list box (<select>): its items go to the shell's menu. */
static gboolean on_option_menu(WebKitWebView *web, WebKitOptionMenu *menu, WebKitRectangle *r, gpointer data) {
    (void)web, (void)r;
    struct view *v = data;
    int id = ++v->next_ask;
    g_hash_table_insert(v->menus, GINT_TO_POINTER(id), g_object_ref(menu));
    GString *items = g_string_new("[");
    for (guint i = 0; i < webkit_option_menu_get_n_items(menu); i++) {
        WebKitOptionMenuItem *it = webkit_option_menu_get_item(menu, i);
        char *label = json_quote(webkit_option_menu_item_get_label(it));
        g_string_append_printf(items, "%s{\"label\":%s,\"enabled\":%s,\"selected\":%s,\"group\":%s}", i ? "," : "", label,
                               webkit_option_menu_item_is_enabled(it) ? "true" : "false",
                               webkit_option_menu_item_is_selected(it) ? "true" : "false",
                               webkit_option_menu_item_is_group_label(it) ? "true" : "false");
        g_free(label);
    }
    g_string_append(items, "]");
    char *msg = g_strdup_printf("{\"t\":\"menu\",\"view\":%d,\"id\":%d,\"items\":%s}", v->id, id, items->str);
    send_json(msg);
    g_free(msg);
    g_string_free(items, TRUE);
    return TRUE;
}

static gboolean allowed_scheme(const char *uri) {
    static const char *const ok[] = {"http:", "https:", "about:", "data:", "blob:"};
    for (size_t i = 0; i < G_N_ELEMENTS(ok); i++)
        if (!g_ascii_strncasecmp(uri, ok[i], strlen(ok[i])))
            return TRUE;
    return FALSE;
}

/* Pages stay on the web: no file:, no other apps' schemes (they are
 * reported, for the shell to log); new windows open in this view; what
 * the engine cannot show is downloaded, for the shell to put in Files. */
static gboolean on_policy(WebKitWebView *web, WebKitPolicyDecision *d, WebKitPolicyDecisionType type, gpointer data) {
    struct view *v = data;
    if (type == WEBKIT_POLICY_DECISION_TYPE_RESPONSE) {
        if (webkit_response_policy_decision_is_mime_type_supported(WEBKIT_RESPONSE_POLICY_DECISION(d)))
            return FALSE;
        webkit_policy_decision_download(d);
        return TRUE;
    }
    WebKitNavigationAction *a = webkit_navigation_policy_decision_get_navigation_action(WEBKIT_NAVIGATION_POLICY_DECISION(d));
    const char *uri = webkit_uri_request_get_uri(webkit_navigation_action_get_request(a));
    if (!allowed_scheme(uri)) {
        char *q = json_quote(uri);
        char *msg = g_strdup_printf("{\"t\":\"blocked\",\"view\":%d,\"uri\":%s}", v->id, q);
        send_json(msg);
        g_free(msg), g_free(q);
        webkit_policy_decision_ignore(d);
        return TRUE;
    }
    if (type == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) {
        webkit_web_view_load_uri(web, uri);
        webkit_policy_decision_ignore(d);
        return TRUE;
    }
    return FALSE;
}

static WebKitWebView *on_create(WebKitWebView *web, WebKitNavigationAction *a, gpointer data) {
    (void)data;
    webkit_web_view_load_uri(web, webkit_uri_request_get_uri(webkit_navigation_action_get_request(a)));
    return NULL;
}

/* A file field: the shell lists the user's files (Files) in a system menu and
 * copies the one picked to $HOME/uploads, where this process can read it. */
static gboolean on_file_chooser(WebKitWebView *web, WebKitFileChooserRequest *r, gpointer data) {
    (void)web;
    struct view *v = data;
    int id = ++v->next_ask;
    g_hash_table_insert(v->choosers, GINT_TO_POINTER(id), g_object_ref(r));
    GString *types = g_string_new("[");
    const gchar *const *mimes = webkit_file_chooser_request_get_mime_types(r);
    for (int i = 0; mimes && mimes[i]; i++) {
        char *q = json_quote(mimes[i]);
        g_string_append_printf(types, "%s%s", i ? "," : "", q);
        g_free(q);
    }
    g_string_append(types, "]");
    char *msg = g_strdup_printf("{\"t\":\"choose-file\",\"view\":%d,\"id\":%d,\"types\":%s,\"multiple\":%s}", v->id, id, types->str,
                                webkit_file_chooser_request_get_select_multiple(r) ? "true" : "false");
    send_json(msg);
    g_free(msg);
    g_string_free(types, TRUE);
    return TRUE;
}

static void on_found(WebKitFindController *fc, guint count, gpointer data) {
    (void)fc;
    struct view *v = data;
    char msg[96];
    snprintf(msg, sizeof msg, "{\"t\":\"found\",\"view\":%d,\"count\":%u}", v->id, count);
    send_json(msg);
}

static void on_not_found(WebKitFindController *fc, gpointer data) { on_found(fc, 0, data); }

/* Location, notifications, camera and microphone are not the page's to
 * ask for here: pages reach system capabilities through phone.call. */
static gboolean on_permission(WebKitWebView *web, WebKitPermissionRequest *r, gpointer data) {
    (void)web, (void)data;
    fprintf(stderr, "phone-webview: denied %s\n", G_OBJECT_TYPE_NAME(r));
    webkit_permission_request_deny(r);
    return TRUE;
}

static const char *catalog_json(const char *lang) {
    for (size_t i = 0; catalogs[i].lang; i++)
        if (!strcmp(catalogs[i].lang, lang))
            return catalogs[i].json;
    return NULL;
}

/* The translations into the device's language (PHONE_LANG, zh-Hans when
 * unset) as one JSON object of the English messages: the language's own
 * catalog over that of the language it falls back to before English, the
 * source (zh-Hant falls back to zh-Hans); "{}" when neither has one. */
static const char *catalog_text(void) {
    static char *text;
    if (text)
        return text;
    const char *lang = getenv("PHONE_LANG");
    if (!lang || !*lang)
        lang = "zh-Hans";
    const char *fallback = !strcmp(lang, "zh-Hant") ? "zh-Hans" : NULL;
    const char *chain[] = {fallback, lang};
    JSCValue *all = jsc_value_new_object(json_context(), NULL, NULL);
    for (size_t i = 0; i < G_N_ELEMENTS(chain); i++) {
        const char *json = chain[i] ? catalog_json(chain[i]) : NULL;
        JSCValue *one = json ? jsc_value_new_from_json(json_context(), json) : NULL;
        char **keys = one && jsc_value_is_object(one) ? jsc_value_object_enumerate_properties(one) : NULL;
        for (char **k = keys; k && *k; k++) {
            JSCValue *v = jsc_value_object_get_property(one, *k);
            jsc_value_object_set_property(all, *k, v);
            g_object_unref(v);
        }
        g_strfreev(keys);
        if (one)
            g_object_unref(one);
    }
    text = jsc_value_to_json(all, 0);
    g_object_unref(all);
    if (!text)
        text = g_strdup("{}");
    return text;
}

/* msg in the device's language; the string lives as long as the process. */
static const char *tr(const char *msg) {
    static JSCValue *catalog;
    if (!catalog)
        catalog = jsc_value_new_from_json(json_context(), catalog_text());
    JSCValue *t = catalog && jsc_value_is_object(catalog) ? jsc_value_object_get_property(catalog, msg) : NULL;
    const char *out = msg;
    if (t && jsc_value_is_string(t)) {
        char *s = jsc_value_to_string(t);
        out = g_intern_string(s);
        g_free(s);
    }
    if (t)
        g_object_unref(t);
    return out;
}

/* nav.js with the device's translations in place of its empty TEXT. */
static char *nav_source(void) {
    char **parts = g_strsplit(nav_js, "/*TEXT*/{}", 2);
    char *s = g_strjoinv(catalog_text(), parts);
    g_strfreev(parts);
    return s;
}

static void error_page(struct view *v, const char *uri, const char *title, const char *detail) {
    char *t = g_markup_escape_text(title, -1);
    char *d = g_markup_escape_text(detail ? detail : "", -1);
    char *u = g_markup_escape_text(uri ? uri : "", -1);
    char *html = g_strdup_printf("<!doctype html><meta charset=utf-8><meta name=viewport content=\"width=device-width\">"
                                 "<title>%s</title><body style=\"font-family:sans-serif;margin:16px;color:#333\">"
                                 "<h2>%s</h2><p>%s</p><p style=\"color:#888;word-break:break-all\">%s</p>",
                                 t, t, d, u);
    webkit_web_view_load_alternate_html(v->web, html, uri, NULL);
    g_free(html), g_free(t), g_free(d), g_free(u);
}

static gboolean on_load_failed(WebKitWebView *web, WebKitLoadEvent e, gchar *uri, GError *err, gpointer data) {
    (void)web, (void)e;
    if (g_error_matches(err, WEBKIT_NETWORK_ERROR, WEBKIT_NETWORK_ERROR_CANCELLED) ||
        g_error_matches(err, WEBKIT_POLICY_ERROR, WEBKIT_POLICY_ERROR_FRAME_LOAD_INTERRUPTED_BY_POLICY_CHANGE))
        return FALSE;
    fprintf(stderr, "phone-webview: %s: %s\n", uri, err ? err->message : "load failed");
    error_page(data, uri, tr("This page cannot be opened"), err ? err->message : NULL);
    return TRUE;
}

static gboolean on_tls_errors(WebKitWebView *web, gchar *uri, GTlsCertificate *cert, GTlsCertificateFlags errors, gpointer data) {
    (void)web, (void)cert;
    fprintf(stderr, "phone-webview: %s: certificate errors 0x%x\n", uri, errors);
    error_page(data, uri, tr("Insecure connection"), tr("This site's certificate is not trusted, so it was not opened."));
    return TRUE;
}

/* A crashed or oversized web process: reload the page, but not in a loop. */
static void on_terminated(WebKitWebView *web, WebKitWebProcessTerminationReason r, gpointer data) {
    struct view *v = data;
    gint64 now = g_get_monotonic_time();
    fprintf(stderr, "phone-webview: view %d: the web process ended (%d)\n", v->id, r);
    memmove(v->crashes + 1, v->crashes, sizeof v->crashes - sizeof v->crashes[0]);
    v->crashes[0] = now;
    clear_focus(v);
    if (v->crashes[2] && now - v->crashes[2] < 60 * G_USEC_PER_SEC)
        error_page(v, webkit_web_view_get_uri(web), tr("This page crashed repeatedly"), tr("Reloading stopped."));
    else
        webkit_web_view_reload(web);
}

struct view *view_new(int id, int w, int h, double scale, WebKitWebContext *ctx, WebKitSettings *settings,
                      WebKitUserContentFilter *filter) {
    struct view *v = g_new0(struct view, 1);
    v->id = id, v->w = w, v->h = h;
    v->dialogs = g_hash_table_new(NULL, NULL);
    v->menus = g_hash_table_new(NULL, NULL);
    v->choosers = g_hash_table_new(NULL, NULL);
    make_surface(v);
    static const struct wpe_view_backend_exportable_fdo_client client = {.export_shm_buffer = on_shm_frame};
    /* Sized in CSS pixels; the scale factor makes the buffers the surface's size. */
    v->exportable = wpe_view_backend_exportable_fdo_create(&client, v, (uint32_t)(w / scale), (uint32_t)(h / scale));
    v->backend = wpe_view_backend_exportable_fdo_get_view_backend(v->exportable);
    WebKitWebViewBackend *vb = webkit_web_view_backend_new(v->backend, (GDestroyNotify)wpe_view_backend_exportable_fdo_destroy, v->exportable);

    WebKitUserContentManager *ucm = webkit_user_content_manager_new();
    WebKitUserScript *bridge = webkit_user_script_new(bridge_js, WEBKIT_USER_CONTENT_INJECT_TOP_FRAME, WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_START, NULL, NULL);
    char *nav_src = nav_source();
    WebKitUserScript *nav = webkit_user_script_new(nav_src, WEBKIT_USER_CONTENT_INJECT_TOP_FRAME, WEBKIT_USER_SCRIPT_INJECT_AT_DOCUMENT_END, NULL, NULL);
    g_free(nav_src);
    webkit_user_content_manager_add_script(ucm, bridge);
    webkit_user_content_manager_add_script(ucm, nav);
    webkit_user_script_unref(bridge), webkit_user_script_unref(nav);
    if (filter)
        webkit_user_content_manager_add_filter(ucm, filter);
    webkit_user_content_manager_register_script_message_handler(ucm, "phoneNav", NULL);
    webkit_user_content_manager_register_script_message_handler(ucm, "phone", NULL);
    g_signal_connect(ucm, "script-message-received::phoneNav", G_CALLBACK(on_nav), v);
    g_signal_connect(ucm, "script-message-received::phone", G_CALLBACK(on_call), v);

    v->web = WEBKIT_WEB_VIEW(g_object_new(WEBKIT_TYPE_WEB_VIEW, "backend", vb, "web-context", ctx, "user-content-manager", ucm,
                                          "settings", settings, NULL));
    g_object_unref(ucm);
    wpe_view_backend_dispatch_set_device_scale_factor(v->backend, (float)scale);
    /* Views open when their node comes on screen. */
    wpe_view_backend_add_activity_state(v->backend, wpe_view_activity_state_visible | wpe_view_activity_state_focused |
                                                        wpe_view_activity_state_in_window);
    g_signal_connect(v->web, "notify::title", G_CALLBACK(on_change), v);
    g_signal_connect(v->web, "notify::uri", G_CALLBACK(on_change), v);
    g_signal_connect(v->web, "notify::is-loading", G_CALLBACK(on_change), v);
    g_signal_connect(v->web, "create", G_CALLBACK(on_create), v);
    g_signal_connect(v->web, "decide-policy", G_CALLBACK(on_policy), v);
    g_signal_connect(v->web, "permission-request", G_CALLBACK(on_permission), v);
    g_signal_connect(v->web, "script-dialog", G_CALLBACK(on_script_dialog), v);
    g_signal_connect(v->web, "show-option-menu", G_CALLBACK(on_option_menu), v);
    g_signal_connect(v->web, "load-failed", G_CALLBACK(on_load_failed), v);
    g_signal_connect(v->web, "load-failed-with-tls-errors", G_CALLBACK(on_tls_errors), v);
    g_signal_connect(v->web, "web-process-terminated", G_CALLBACK(on_terminated), v);
    g_signal_connect(v->web, "run-file-chooser", G_CALLBACK(on_file_chooser), v);
    WebKitFindController *fc = webkit_web_view_get_find_controller(v->web);
    g_signal_connect(fc, "found-text", G_CALLBACK(on_found), v);
    g_signal_connect(fc, "failed-to-find-text", G_CALLBACK(on_not_found), v);
    return v;
}

void view_free(struct view *v) {
    GHashTableIter it;
    gpointer d;
    g_hash_table_iter_init(&it, v->dialogs);
    while (g_hash_table_iter_next(&it, NULL, &d)) {
        webkit_script_dialog_close(d);
        webkit_script_dialog_unref(d);
    }
    g_hash_table_iter_init(&it, v->menus);
    while (g_hash_table_iter_next(&it, NULL, &d)) {
        webkit_option_menu_close(d);
        g_object_unref(d);
    }
    g_hash_table_iter_init(&it, v->choosers);
    while (g_hash_table_iter_next(&it, NULL, &d)) {
        webkit_file_chooser_request_cancel(d);
        g_object_unref(d);
    }
    g_hash_table_destroy(v->dialogs), g_hash_table_destroy(v->menus), g_hash_table_destroy(v->choosers);
    g_signal_handlers_disconnect_by_data(webkit_web_view_get_find_controller(v->web), v);
    g_signal_handlers_disconnect_by_data(v->web, v);
    g_object_unref(v->web); /* and with it the backend and its exportable */
    munmap(v->surface, v->surface_len);
    close(v->surface_fd);
    g_free(v->value);
    g_free(v);
}

void view_open(struct view *v, const char *url) {
    if (!url || !(g_str_has_prefix(url, "http://") || g_str_has_prefix(url, "https://")))
        return;
    clear_focus(v);
    webkit_web_view_load_uri(v->web, url);
}

/* A list box opens on a real key press, as a keyboard user opens it. */
static void on_activated(GObject *o, GAsyncResult *res, gpointer data) {
    struct view *v = data;
    JSCValue *r = webkit_web_view_evaluate_javascript_finish(WEBKIT_WEB_VIEW(o), res, NULL);
    if (!r)
        return;
    char *s = jsc_value_is_string(r) ? jsc_value_to_string(r) : NULL;
    if (s && !strcmp(s, "native")) {
        struct wpe_input_keyboard_event e = {.time = (uint32_t)(g_get_monotonic_time() / 1000), .key_code = WPE_KEY_space, .pressed = true};
        wpe_view_backend_dispatch_keyboard_event(v->backend, &e);
        e.pressed = false;
        wpe_view_backend_dispatch_keyboard_event(v->backend, &e);
    }
    g_free(s);
    g_object_unref(r);
}

void view_key(struct view *v, const char *key) {
    if (!strcmp(key, "enter")) {
        webkit_web_view_evaluate_javascript(v->web, "window.__phoneNav?window.__phoneNav.activate():''", -1, NULL, NULL, NULL,
                                            on_activated, v);
    } else if (!strcmp(key, "up") || !strcmp(key, "down") || !strcmp(key, "left") || !strcmp(key, "right")) {
        char *js = g_strdup_printf("window.__phoneNav&&window.__phoneNav.move('%s')", key);
        run_js(v, js);
        g_free(js);
    }
}

void view_back(struct view *v) {
    if (webkit_web_view_can_go_back(v->web)) {
        clear_focus(v);
        webkit_web_view_go_back(v->web);
    }
}

/* A view off screen is hidden from the engine, which then throttles its
 * timers and stops painting. */
void view_show(struct view *v, gboolean visible) {
    uint32_t s = wpe_view_activity_state_visible | wpe_view_activity_state_focused;
    if (visible)
        wpe_view_backend_add_activity_state(v->backend, s);
    else
        wpe_view_backend_remove_activity_state(v->backend, s);
}

void view_set_value(struct view *v, const char *value) {
    g_free(v->value);
    v->value = g_strdup(value ? value : "");
    char *q = json_quote(v->value);
    char *js = g_strdup_printf("window.__phoneNav&&window.__phoneNav.setValue(%s)", q);
    run_js(v, js);
    g_free(js), g_free(q);
}

void view_submit(struct view *v) {
    run_js(v, "window.__phoneNav&&window.__phoneNav.submit()");
}

void view_reply(struct view *v, int id, gboolean ok, const char *json) {
    char *js = g_strdup_printf("window.__phoneReply&&window.__phoneReply(%d,%s,%s)", id, ok ? "true" : "false", json ? json : "null");
    run_js(v, js);
    g_free(js);
}

void view_answer(struct view *v, int id, gboolean ok, const char *text) {
    WebKitScriptDialog *d = g_hash_table_lookup(v->dialogs, GINT_TO_POINTER(id));
    if (!d)
        return;
    g_hash_table_remove(v->dialogs, GINT_TO_POINTER(id));
    switch (webkit_script_dialog_get_dialog_type(d)) {
    case WEBKIT_SCRIPT_DIALOG_CONFIRM:
    case WEBKIT_SCRIPT_DIALOG_BEFORE_UNLOAD_CONFIRM:
        webkit_script_dialog_confirm_set_confirmed(d, ok);
        break;
    case WEBKIT_SCRIPT_DIALOG_PROMPT:
        if (ok)
            webkit_script_dialog_prompt_set_text(d, text ? text : "");
        break;
    default:
        break;
    }
    webkit_script_dialog_close(d);
    webkit_script_dialog_unref(d);
}

void view_choose(struct view *v, int id, int index) {
    WebKitOptionMenu *m = g_hash_table_lookup(v->menus, GINT_TO_POINTER(id));
    if (!m)
        return;
    g_hash_table_remove(v->menus, GINT_TO_POINTER(id));
    if (index >= 0 && (guint)index < webkit_option_menu_get_n_items(m))
        webkit_option_menu_activate_item(m, (guint)index);
    webkit_option_menu_close(m);
    g_object_unref(m);
}

static void on_cleared(GObject *o, GAsyncResult *res, gpointer data) {
    struct view *v = data;
    GError *err = NULL;
    gboolean ok = webkit_website_data_manager_clear_finish(WEBKIT_WEBSITE_DATA_MANAGER(o), res, &err);
    if (!ok)
        fprintf(stderr, "phone-webview: clearing website data: %s\n", err ? err->message : "failed");
    g_clear_error(&err);
    char msg[80];
    snprintf(msg, sizeof msg, "{\"t\":\"cleared\",\"view\":%d,\"ok\":%s}", v->id, ok ? "true" : "false");
    send_json(msg);
}

/* The kinds of data a site keeps, as the shell names them. */
static void append_site_types(GString *s, WebKitWebsiteDataTypes t) {
    const WebKitWebsiteDataTypes cache = WEBKIT_WEBSITE_DATA_MEMORY_CACHE | WEBKIT_WEBSITE_DATA_DISK_CACHE;
    const char *sep = "";
    g_string_append_c(s, '[');
    if (t & WEBKIT_WEBSITE_DATA_COOKIES)
        g_string_append(s, "\"cookies\""), sep = ",";
    if (t & ~(WEBKIT_WEBSITE_DATA_COOKIES | cache))
        g_string_append_printf(s, "%s\"storage\"", sep), sep = ",";
    if (t & cache)
        g_string_append_printf(s, "%s\"cache\"", sep);
    g_string_append_c(s, ']');
}

static void on_sites(GObject *o, GAsyncResult *res, gpointer data) {
    int id = GPOINTER_TO_INT(data);
    GList *list = webkit_website_data_manager_fetch_finish(WEBKIT_WEBSITE_DATA_MANAGER(o), res, NULL);
    GString *s = g_string_new(NULL);
    g_string_append_printf(s, "{\"t\":\"sites\",\"view\":%d,\"sites\":[", id);
    for (GList *l = list; l; l = l->next) {
        char *name = json_quote(webkit_website_data_get_name(l->data));
        g_string_append_printf(s, "%s{\"name\":%s,\"types\":", l == list ? "" : ",", name);
        append_site_types(s, webkit_website_data_get_types(l->data));
        g_string_append_c(s, '}');
        g_free(name);
    }
    g_string_append(s, "]}");
    send_json(s->str);
    g_string_free(s, TRUE);
    g_list_free_full(list, (GDestroyNotify)webkit_website_data_unref);
}

/* Deleting one site's data: fetch the records, remove the site's. */
struct forget {
    int id;
    char *name;
};

static void forgot(struct forget *f, gboolean ok) {
    char *name = json_quote(f->name);
    char *msg = g_strdup_printf("{\"t\":\"forgot\",\"view\":%d,\"name\":%s,\"ok\":%s}", f->id, name, ok ? "true" : "false");
    send_json(msg);
    g_free(msg), g_free(name), g_free(f->name), g_free(f);
}

static void on_removed(GObject *o, GAsyncResult *res, gpointer data) {
    GError *err = NULL;
    gboolean ok = webkit_website_data_manager_remove_finish(WEBKIT_WEBSITE_DATA_MANAGER(o), res, &err);
    if (!ok)
        fprintf(stderr, "phone-webview: deleting website data: %s\n", err ? err->message : "failed");
    g_clear_error(&err);
    forgot(data, ok);
}

static void on_fetched_for_forget(GObject *o, GAsyncResult *res, gpointer data) {
    struct forget *f = data;
    GList *list = webkit_website_data_manager_fetch_finish(WEBKIT_WEBSITE_DATA_MANAGER(o), res, NULL);
    GList *pick = NULL;
    for (GList *l = list; l; l = l->next)
        if (!g_strcmp0(webkit_website_data_get_name(l->data), f->name))
            pick = g_list_prepend(pick, l->data);
    if (pick)
        webkit_website_data_manager_remove(WEBKIT_WEBSITE_DATA_MANAGER(o), WEBKIT_WEBSITE_DATA_ALL, pick, NULL, on_removed, f);
    else
        forgot(f, FALSE);
    g_list_free(pick);
    g_list_free_full(list, (GDestroyNotify)webkit_website_data_unref);
}

void view_control(struct view *v, const char *op, const char *text, double value) {
    WebKitFindController *fc = webkit_web_view_get_find_controller(v->web);
    if (!strcmp(op, "reload")) {
        clear_focus(v);
        webkit_web_view_reload(v->web);
    } else if (!strcmp(op, "forward")) {
        if (webkit_web_view_can_go_forward(v->web)) {
            clear_focus(v);
            webkit_web_view_go_forward(v->web);
        }
    } else if (!strcmp(op, "stop")) {
        webkit_web_view_stop_loading(v->web);
    } else if (!strcmp(op, "zoom")) {
        webkit_web_view_set_zoom_level(v->web, value < 0.5 ? 0.5 : value > 3 ? 3 : value);
        send_state(v);
    } else if (!strcmp(op, "find") && text && *text) {
        webkit_find_controller_search(fc, text, WEBKIT_FIND_OPTIONS_CASE_INSENSITIVE | WEBKIT_FIND_OPTIONS_WRAP_AROUND, G_MAXUINT);
    } else if (!strcmp(op, "find_next")) {
        webkit_find_controller_search_next(fc);
    } else if (!strcmp(op, "find_prev")) {
        webkit_find_controller_search_previous(fc);
    } else if (!strcmp(op, "find_end")) {
        webkit_find_controller_search_finish(fc);
    } else if (!strcmp(op, "clear_data")) {
        WebKitWebsiteDataManager *dm = webkit_network_session_get_website_data_manager(webkit_web_view_get_network_session(v->web));
        webkit_website_data_manager_clear(dm, WEBKIT_WEBSITE_DATA_ALL, 0, NULL, on_cleared, v);
    } else if (!strcmp(op, "sites")) {
        WebKitWebsiteDataManager *dm = webkit_network_session_get_website_data_manager(webkit_web_view_get_network_session(v->web));
        webkit_website_data_manager_fetch(dm, WEBKIT_WEBSITE_DATA_ALL, NULL, on_sites, GINT_TO_POINTER(v->id));
    } else if (!strcmp(op, "forget_site") && text && *text) {
        WebKitWebsiteDataManager *dm = webkit_network_session_get_website_data_manager(webkit_web_view_get_network_session(v->web));
        struct forget *f = g_new0(struct forget, 1);
        f->id = v->id, f->name = g_strdup(text);
        webkit_website_data_manager_fetch(dm, WEBKIT_WEBSITE_DATA_ALL, NULL, on_fetched_for_forget, f);
    }
}

void view_files(struct view *v, int id, char **names) {
    WebKitFileChooserRequest *r = g_hash_table_lookup(v->choosers, GINT_TO_POINTER(id));
    if (!r)
        return;
    g_hash_table_remove(v->choosers, GINT_TO_POINTER(id));
    GPtrArray *paths = g_ptr_array_new_with_free_func(g_free);
    for (int i = 0; names && names[i]; i++) {
        char *base = g_path_get_basename(names[i]);
        if (*base && *base != '.')
            g_ptr_array_add(paths, g_build_filename(g_get_home_dir(), "uploads", base, NULL));
        g_free(base);
    }
    g_ptr_array_add(paths, NULL);
    if (paths->len > 1)
        webkit_file_chooser_request_select_files(r, (const gchar *const *)paths->pdata);
    else
        webkit_file_chooser_request_cancel(r);
    g_ptr_array_free(paths, TRUE);
    g_object_unref(r);
}

/* Where a download goes and how it ended, for the shell. The name is the
 * server's, without directories; the shell checks it again. */
static gboolean on_destination(WebKitDownload *d, gchar *suggested, gpointer data) {
    (void)data;
    char *dir = g_build_filename(g_get_home_dir(), "downloads", NULL);
    g_mkdir_with_parents(dir, 0700);
    char *base = g_path_get_basename(suggested && *suggested ? suggested : "download");
    if (*base == '.' || !strcmp(base, G_DIR_SEPARATOR_S)) {
        g_free(base);
        base = g_strdup("download");
    }
    char *path = g_build_filename(dir, base, NULL);
    webkit_download_set_allow_overwrite(d, TRUE);
    webkit_download_set_destination(d, path);
    g_free(path), g_free(base), g_free(dir);
    return TRUE;
}

static void download_done(WebKitDownload *d, struct view *v, gboolean ok) {
    const char *dest = webkit_download_get_destination(d);
    char *base = dest ? g_path_get_basename(dest) : g_strdup("");
    char *name = json_quote(base);
    WebKitURIResponse *res = webkit_download_get_response(d);
    char *mime = json_quote(res ? webkit_uri_response_get_mime_type(res) : "");
    char *msg = g_strdup_printf("{\"t\":\"download\",\"view\":%d,\"name\":%s,\"mime\":%s,\"ok\":%s}", v ? v->id : 0, name, mime,
                                ok ? "true" : "false");
    send_json(msg);
    g_free(msg), g_free(mime), g_free(name), g_free(base);
}

static void on_download_finished(WebKitDownload *d, gpointer data) { download_done(d, data, TRUE); }

static void on_download_failed(WebKitDownload *d, GError *err, gpointer data) {
    fprintf(stderr, "phone-webview: download failed: %s\n", err ? err->message : "?");
    g_signal_handlers_disconnect_by_func(d, on_download_finished, data);
    download_done(d, data, FALSE);
}

void view_download(WebKitDownload *d, struct view *v) {
    g_signal_connect(d, "decide-destination", G_CALLBACK(on_destination), v);
    g_signal_connect(d, "finished", G_CALLBACK(on_download_finished), v);
    g_signal_connect(d, "failed", G_CALLBACK(on_download_failed), v);
}
