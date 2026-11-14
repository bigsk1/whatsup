//go:build linux && cgo && !gtk3 && !purego && !android && !server

package main

/*
#cgo pkg-config: webkitgtk-6.0
#include <gtk/gtk.h>
#include <webkit/webkit.h>
#include <string.h>

typedef struct {
    GMainLoop *loop;
    WebKitWebView *view;
    GCancellable *cancellable;
    guint timeout;
    int result;
    gboolean finished;
    gboolean evaluating;
    gint references;
} ProbeState;

static void probe_state_unref(ProbeState *state) {
    if (!g_atomic_int_dec_and_test(&state->references)) return;
    g_object_unref(state->cancellable);
    g_main_loop_unref(state->loop);
    g_free(state);
}

static void probe_finish(ProbeState *state, int result) {
    if (state->finished) return;
    state->finished = TRUE;
    state->result = result;
    g_main_loop_quit(state->loop);
}

static gboolean probe_timeout(gpointer data) {
    ProbeState *state = data;
    state->timeout = 0;
    probe_finish(state, 6);
    return G_SOURCE_REMOVE;
}

static void probe_javascript_done(GObject *source, GAsyncResult *result, gpointer data) {
    ProbeState *state = data;
    GError *error = NULL;
    JSCValue *value = webkit_web_view_evaluate_javascript_finish(WEBKIT_WEB_VIEW(source), result, &error);
    gboolean matches = FALSE;
    if (error == NULL && value != NULL && jsc_value_is_string(value)) {
        char *text = jsc_value_to_string(value);
        matches = text != NULL && strcmp(text, "whatsup-native-runtime-ok") == 0;
        g_free(text);
    }
    if (value != NULL) g_object_unref(value);
    if (error != NULL) g_error_free(error);
    probe_finish(state, matches ? 0 : 5);
    probe_state_unref(state);
}

static void probe_load_changed(WebKitWebView *view, WebKitLoadEvent event, gpointer data) {
    ProbeState *state = data;
    if (event != WEBKIT_LOAD_FINISHED || state->finished || state->evaluating) return;
    state->evaluating = TRUE;
    // A cancellation callback can run after the native loop has stopped. It
    // retains the heap state instead of referring to an expired C stack frame.
    g_atomic_int_inc(&state->references);
    webkit_web_view_evaluate_javascript(view,
        "document.getElementById('probe').textContent", -1,
        NULL, NULL, state->cancellable, probe_javascript_done, state);
}

static gboolean probe_load_failed(WebKitWebView *view, WebKitLoadEvent event,
        const char *uri, GError *error, gpointer data) {
    probe_finish(data, 3);
    return TRUE;
}

static void probe_process_terminated(WebKitWebView *view,
        WebKitWebProcessTerminationReason reason, gpointer data) {
    probe_finish(data, 4);
}

static int probe_webkit_runtime(int accelerated) {
    if (!gtk_init_check()) return 2;

    WebKitNetworkSession *session = webkit_network_session_new_ephemeral();
    if (session == NULL || !webkit_network_session_is_ephemeral(session)) return 8;
    GtkWidget *widget = g_object_new(WEBKIT_TYPE_WEB_VIEW, "network-session", session, NULL);
    g_object_unref(session);
    if (widget == NULL) return 8;
    WebKitWebView *view = WEBKIT_WEB_VIEW(widget);
    WebKitSettings *settings = webkit_web_view_get_settings(view);
    webkit_settings_set_hardware_acceleration_policy(settings, accelerated
        ? WEBKIT_HARDWARE_ACCELERATION_POLICY_ALWAYS
        : WEBKIT_HARDWARE_ACCELERATION_POLICY_NEVER);

    // The window owns the widget but is never presented: no desktop control,
    // browser navigation or owner interaction is part of this probe.
    GtkWidget *window = gtk_window_new();
    g_object_ref_sink(window);
    gtk_window_set_default_size(GTK_WINDOW(window), 64, 64);
    gtk_window_set_child(GTK_WINDOW(window), widget);

    ProbeState *state = g_new0(ProbeState, 1);
    state->references = 1;
    state->loop = g_main_loop_new(NULL, FALSE);
    state->view = view;
    state->cancellable = g_cancellable_new();
    state->result = 6;
    g_signal_connect(view, "load-changed", G_CALLBACK(probe_load_changed), state);
    g_signal_connect(view, "load-failed", G_CALLBACK(probe_load_failed), state);
    g_signal_connect(view, "web-process-terminated", G_CALLBACK(probe_process_terminated), state);
    state->timeout = g_timeout_add_seconds(20, probe_timeout, state);
    webkit_web_view_load_html(view,
        "<!doctype html><meta charset='utf-8'>"
        "<meta http-equiv='Content-Security-Policy' content=\"default-src 'none'; base-uri 'none'; form-action 'none'\">"
        "<p id='probe'>whatsup-native-runtime-ok</p>", "about:blank");
    if (!state->finished) g_main_loop_run(state->loop);

    if (state->timeout != 0) g_source_remove(state->timeout);
    g_signal_handlers_disconnect_by_data(view, state);
    g_cancellable_cancel(state->cancellable);
    gtk_window_destroy(GTK_WINDOW(window));
    g_object_unref(window);
    int result = state->result;
    probe_state_unref(state);
    return result;
}
*/
import "C"

import "runtime"

func init() { runtime.LockOSThread() }

func runNativeProbe(accelerated bool) int {
	mode := C.int(0)
	if accelerated {
		mode = 1
	}
	return int(C.probe_webkit_runtime(mode))
}
