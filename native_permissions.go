package main

import (
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// webviewPermissions denies every capability the renderer can request except
// the microphone, which voice calls need. Clipboard and notifications go
// through narrow native Go paths, and nothing captures video or location.
//
// An explicit policy is required. With no entry, the pinned Wails grants every
// WebView2 request silently on Windows and allows media capture on Linux,
// where WebKitGTK has no prompt of its own. Only bundled script can ask, so
// this is defence in depth, not the only barrier.
//
// The microphone is never allowed outright here. See microphoneIdlePermission.
func webviewPermissions() map[application.PermissionType]application.Permission {
	return map[application.PermissionType]application.Permission{
		application.PermissionMicrophone:    microphoneIdlePermission(runtime.GOOS),
		application.PermissionCamera:        application.PermissionDeny,
		application.PermissionGeolocation:   application.PermissionDeny,
		application.PermissionNotifications: application.PermissionDeny,
		application.PermissionClipboardRead: application.PermissionDeny,
	}
}

// windowsWebviewPermissions covers the WebView2 kind that has no
// cross-platform equivalent. Kinds this Wails release cannot name fall back to
// WebView2's own default handling, which asks the person instead of granting.
func windowsWebviewPermissions() map[application.CoreWebView2PermissionKind]application.CoreWebView2PermissionState {
	return map[application.CoreWebView2PermissionKind]application.CoreWebView2PermissionState{
		application.CoreWebView2PermissionKindOtherSensors: application.CoreWebView2PermissionStateDeny,
	}
}

// microphoneIdlePermission is the microphone policy while no call is answered.
//
// On Linux it is deny; setCallCapture switches the same table entry to allow
// only between an answer and the end of that call.
//
// On Windows it is "default": WebView2 shows its own permission prompt on
// first use and remembers the answer in the app's private profile. After that
// first grant the webview no longer distinguishes an answered call from any
// other moment, so on Windows the remaining barriers are the Go-side call
// state, the voice-call setting and the bundled-script-only policy. Closing
// that gap needs a per-request native decision, which the pinned Wails does
// not expose yet.
func microphoneIdlePermission(goos string) application.Permission {
	if goos == "windows" {
		return application.PermissionDefault
	}
	return application.PermissionDeny
}
