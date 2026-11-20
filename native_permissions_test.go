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
		if state == application.PermissionAllow {
			t.Errorf("permission kind %d is allowed without asking", kind)
		}
		if kind != application.PermissionMicrophone && state != application.PermissionDeny {
			t.Errorf("permission kind %d is %d, want deny", kind, state)
		}
	}
	if _, listed := policy[application.PermissionMicrophone]; !listed {
		t.Error("microphone has no explicit policy entry")
	}
}

// The microphone is the one capability calls need. It must never be granted
// silently by the static policy on either platform.
func TestMicrophoneIsNeverAllowedWhileNoCallIsAnswered(t *testing.T) {
	if got := microphoneIdlePermission("linux"); got != application.PermissionDeny {
		t.Errorf("Linux microphone policy outside a call is %d, want deny", got)
	}
	if got := microphoneIdlePermission("windows"); got != application.PermissionDefault {
		t.Errorf("Windows microphone policy is %d, want the WebView2 prompt", got)
	}
	for _, goos := range []string{"linux", "windows", "darwin", ""} {
		if microphoneIdlePermission(goos) == application.PermissionAllow {
			t.Errorf("%q grants the microphone without asking", goos)
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
