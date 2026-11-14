//go:build linux && cgo && !purego && !gtk3 && !android && !server

package nativefiles

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>
#include <stdlib.h>

typedef struct {
    GMainLoop *loop;
    GCancellable *cancel;
    GInputStream *stream;
    GByteArray *bytes;
    gboolean failed;
} WhatsUpClipboardRead;

static void whatsup_clipboard_next(WhatsUpClipboardRead *state);

static void whatsup_clipboard_bytes(GObject *source, GAsyncResult *result, gpointer opaque) {
    WhatsUpClipboardRead *state = opaque;
    GError *error = NULL;
    GBytes *bytes = g_input_stream_read_bytes_finish(G_INPUT_STREAM(source), result, &error);
    if (error != NULL || bytes == NULL) {
        if (error != NULL) g_error_free(error);
        if (bytes != NULL) g_bytes_unref(bytes);
        state->failed = TRUE;
        g_main_loop_quit(state->loop);
        return;
    }
    gsize size = 0;
    const guint8 *data = g_bytes_get_data(bytes, &size);
    // Bound encoded bytes before allocation or image decoding. No GdkTexture
    // conversion is requested, so the clipboard producer cannot trigger an
    // unbounded image decode in GTK before Go validates its dimensions.
    if (size > 268435456U - state->bytes->len) {
        state->failed = TRUE;
        g_bytes_unref(bytes);
        g_main_loop_quit(state->loop);
        return;
    }
    if (size != 0) g_byte_array_append(state->bytes, data, (guint)size);
    g_bytes_unref(bytes);
    if (size == 0) {
        g_main_loop_quit(state->loop);
        return;
    }
    whatsup_clipboard_next(state);
}

static void whatsup_clipboard_next(WhatsUpClipboardRead *state) {
    g_input_stream_read_bytes_async(state->stream, 65536, G_PRIORITY_DEFAULT,
        state->cancel, whatsup_clipboard_bytes, state);
}

static void whatsup_clipboard_ready(GObject *source, GAsyncResult *result, gpointer opaque) {
    WhatsUpClipboardRead *state = opaque;
    GError *error = NULL;
    const char *mime = NULL;
    state->stream = gdk_clipboard_read_finish(GDK_CLIPBOARD(source), result, &mime, &error);
    if (error != NULL || state->stream == NULL || mime == NULL ||
        (g_strcmp0(mime, "image/png") != 0 && g_strcmp0(mime, "image/jpeg") != 0 &&
         g_strcmp0(mime, "image/webp") != 0 && g_strcmp0(mime, "image/gif") != 0)) {
        if (error != NULL) g_error_free(error);
        state->failed = TRUE;
        g_main_loop_quit(state->loop);
        return;
    }
    whatsup_clipboard_next(state);
}

static gboolean whatsup_clipboard_timeout(gpointer opaque) {
    WhatsUpClipboardRead *state = opaque;
    state->failed = TRUE;
    // Keep the stack alive until the outstanding callback observes cancellation.
    // Quitting immediately here would leave an async callback with freed state.
    g_cancellable_cancel(state->cancel);
    return G_SOURCE_REMOVE;
}

static guint8 *whatsup_clipboard_image(gsize *length, guint timeout_ms, GCancellable *cancel) {
    *length = 0;
    if (cancel == NULL || g_cancellable_is_cancelled(cancel)) return NULL;
    GdkDisplay *display = gdk_display_get_default();
    if (display == NULL) return NULL;
    GdkClipboard *clipboard = gdk_display_get_clipboard(display);
    if (clipboard == NULL) return NULL;
    WhatsUpClipboardRead state = {0};
    state.loop = g_main_loop_new(NULL, FALSE);
    // Go owns this reference through native completion AND cancellation join.
    state.cancel = cancel;
    state.bytes = g_byte_array_new();
    const char *mimes[] = {"image/png", "image/jpeg", "image/webp", "image/gif", NULL};
    GSource *timer = g_timeout_source_new(timeout_ms);
    g_source_set_callback(timer, whatsup_clipboard_timeout, &state, NULL);
    g_source_attach(timer, g_main_context_default());
    gdk_clipboard_read_async(clipboard, mimes, G_PRIORITY_DEFAULT, state.cancel, whatsup_clipboard_ready, &state);
    g_main_loop_run(state.loop);
    g_source_destroy(timer);
    g_source_unref(timer);
    if (state.stream != NULL) {
        // The close task owns its source reference. No completion callback keeps
        // a pointer to this stack state after the final read has retired.
        g_input_stream_close_async(state.stream, G_PRIORITY_DEFAULT, NULL, NULL, NULL);
        g_object_unref(state.stream);
    }
    g_main_loop_unref(state.loop);
    if (state.failed || state.bytes->len == 0) {
        g_byte_array_unref(state.bytes);
        return NULL;
    }
    *length = state.bytes->len;
    return g_byte_array_free(state.bytes, FALSE);
}
*/
import "C"

import (
	"context"
	"sync"
	"time"
	"unsafe"
)

func clipboardImage(ctx context.Context) ([]byte, error) {
	// The native integration dispatches this call on GTK's main thread. Nested
	// GTK iteration allows the asynchronous owner/stream to answer without an
	// unbounded blocking read. Context cancellation also reaches the native read.
	timeout := 8 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if timeout < time.Millisecond {
		return nil, ErrUnavailable
	}
	cancel := C.g_cancellable_new()
	if cancel == nil {
		return nil, ErrUnavailable
	}
	defer C.g_object_unref(C.gpointer(unsafe.Pointer(cancel)))
	join := clipboardCancellation(ctx, func() { C.g_cancellable_cancel(cancel) })
	defer join() // Join before releasing the original native reference.
	var size C.gsize
	data := C.whatsup_clipboard_image(&size, C.guint(timeout.Milliseconds()), cancel)
	if data == nil {
		return nil, ErrUnavailable
	}
	defer C.g_free(C.gpointer(unsafe.Pointer(data)))
	if ctx.Err() != nil || size == 0 || uint64(size) > MaxClipboardBytes {
		return nil, ErrUnavailable
	}
	return C.GoBytes(unsafe.Pointer(data), C.int(size)), nil
}

// A context callback can still be executing when native completion returns.
// Stop it or join it before freeing the borrowed GCancellable; cleanup is safe
// when both paths finish at once and repeated joins remain harmless.
func clipboardCancellation(ctx context.Context, cancel func()) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); cancel() })
	var once sync.Once
	return func() {
		once.Do(func() {
			if !stop() {
				<-done
			}
		})
	}
}
