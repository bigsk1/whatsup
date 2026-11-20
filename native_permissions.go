package main

import "github.com/wailsapp/wails/v3/pkg/application"

// webviewPermissions denies every capability the renderer can request. The
// bundled interface uses none of them: clipboard and notifications go through
// narrow native Go paths, and nothing captures audio, video or location.
//
// An explicit policy is required. With no entry, the pinned Wails grants every
// WebView2 request silently on Windows and allows media capture on Linux,
// where WebKitGTK has no prompt of its own. Only bundled script can ask, so
// this is defence in depth, not the only barrier.
func webviewPermissions() map[application.PermissionType]application.Permission {
	return map[application.PermissionType]application.Permission{
		application.PermissionMicrophone:    application.PermissionDeny,
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
