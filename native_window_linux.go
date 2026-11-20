//go:build linux && cgo && !gtk3 && !purego && !android && !server

package main

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>

static void whatsup_clear_minimize_request(void *window) {
	if (window != NULL && GTK_IS_WINDOW(window)) {
		gtk_window_unminimize(GTK_WINDOW(window));
	}
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

// prepareReveal makes the next Show bring the window to the front.
//
// A Wayland compositor does not let an application raise or un-minimize a
// window that is already mapped: GNOME ignores the request and shows an
// "is ready" notice instead, so a minimized or covered window stayed where it
// was when the tray, a second launch or an incoming call asked for it. A
// window that is mapped afresh is shown in front. So a window that is mapped
// but not active is hidden first, and the following Show maps it again.
//
// GTK also remembers a minimize request and would minimize the fresh window
// as it appears; the pinned Wails never clears that memory, so it is cleared
// here. Nothing is specific to one desktop: where a compositor honours a
// plain raise, the result is the same window in front.
func prepareReveal(window *application.WebviewWindow) {
	if window == nil {
		return
	}
	if window.IsVisible() && !window.IsFocused() {
		window.Hide()
	}
	native := window.NativeWindow()
	if native == nil {
		return
	}
	application.InvokeSync(func() { C.whatsup_clear_minimize_request(native) })
}
