package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// webviewPermissions denies every capability the renderer can request.
// Clipboard and notifications go through narrow native Go paths, and nothing
// captures video or location.
//
// An explicit policy is required. With no entry, the pinned Wails grants every
// WebView2 request silently on Windows and allows media capture on Linux,
// where WebKitGTK has no prompt of its own. Only bundled script can ask, so
// this is defence in depth, not the only barrier.
//
// The microphone and camera are denied here too. Calls need them, so
// setCallCapture switches this table's microphone entry to allow between an
// answer and the end of that call, and the camera entry while this device's
// camera is on in an answered video call, and back. The table is read at each request on both
// platforms: Wails answers WebKitGTK requests from it directly, and the
// reviewed Wails patch does the same for WebView2, where it also keeps every
// answer out of the profile and erases answers an earlier build saved. So no
// webview prompt is shown and no grant outlives a call.
func webviewPermissions() map[application.PermissionType]application.Permission {
	return map[application.PermissionType]application.Permission{
		application.PermissionMicrophone:    application.PermissionDeny,
		application.PermissionCamera:        application.PermissionDeny,
		application.PermissionGeolocation:   application.PermissionDeny,
		application.PermissionNotifications: application.PermissionDeny,
		application.PermissionClipboardRead: application.PermissionDeny,
	}
}

// blockedCapturePolicy is sent with every response when the webview could not
// confirm that it holds no saved device permission. A document served with it
// cannot capture audio or video whatever the profile has saved.
const blockedCapturePolicy = "microphone=(), camera=()"

// windowsWebviewPermissions covers the WebView2 kind that has no
// cross-platform equivalent. Any kind in neither table is denied by the
// patched request handler.
func windowsWebviewPermissions() map[application.CoreWebView2PermissionKind]application.CoreWebView2PermissionState {
	return map[application.CoreWebView2PermissionKind]application.CoreWebView2PermissionState{
		application.CoreWebView2PermissionKindOtherSensors: application.CoreWebView2PermissionStateDeny,
	}
}

// captureGateUpdate returns the work a capture-gate change queues on the main
// thread. It carries no answer of its own: allowed is asked when the work
// runs. Updates can run late and out of order; each one then writes the same
// present truth, so the last word is never a stale one.
func captureGateUpdate(table map[application.PermissionType]application.Permission, microphone, camera func() bool) func() {
	return func() {
		setCapturePermission(table, microphone != nil && microphone(), camera != nil && camera())
	}
}

// setCapturePermission is the whole of the capture gate: the microphone entry
// is allow while a call is answered, the camera entry while this device's
// camera is on in an answered video call, and both are deny otherwise. Nothing
// else in the table changes. It must run on the main thread, where requests
// are answered.
func setCapturePermission(table map[application.PermissionType]application.Permission, microphone, camera bool) {
	if table == nil {
		return
	}
	state := func(allowed bool) application.Permission {
		if allowed {
			return application.PermissionAllow
		}
		return application.PermissionDeny
	}
	table[application.PermissionMicrophone] = state(microphone)
	table[application.PermissionCamera] = state(camera)
}
