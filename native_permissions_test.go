package main

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestWebviewPermissionsDenyEveryNamedCapability(t *testing.T) {
	policy := webviewPermissions()
	// An empty map is the dangerous case: the pinned Wails then grants every
	// WebView2 request and allows Linux media capture without asking.
	if len(policy) == 0 {
		t.Fatal("webview permission policy is empty, which selects silent allow")
	}
	for name, kind := range map[string]application.PermissionType{
		"microphone":     application.PermissionMicrophone,
		"camera":         application.PermissionCamera,
		"geolocation":    application.PermissionGeolocation,
		"notifications":  application.PermissionNotifications,
		"clipboard read": application.PermissionClipboardRead,
	} {
		state, listed := policy[kind]
		if !listed || state != application.PermissionDeny {
			t.Errorf("%s permission is not explicitly denied", name)
		}
	}
	for kind, state := range policy {
		if state != application.PermissionDeny {
			t.Errorf("permission kind %d is %d, want deny", kind, state)
		}
	}
}

func TestWindowsWebviewPermissionsNeverAllow(t *testing.T) {
	policy := windowsWebviewPermissions()
	if policy[application.CoreWebView2PermissionKindOtherSensors] != application.CoreWebView2PermissionStateDeny {
		t.Error("sensor permission is not explicitly denied")
	}
	for kind, state := range policy {
		if state != application.CoreWebView2PermissionStateDeny {
			t.Errorf("WebView2 permission kind %d is %d, want deny", kind, state)
		}
	}
}
