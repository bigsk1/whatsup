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
		// Deny, never "default": default would hand the decision to a webview
		// prompt whose answer WebView2 saves.
		if state != application.PermissionDeny {
			t.Errorf("permission kind %d is %d, want deny", kind, state)
		}
	}
	if _, listed := policy[application.PermissionMicrophone]; !listed {
		t.Error("microphone has no explicit policy entry")
	}
}

// The microphone is the one capability calls need. It is allowed only by the
// capture gate, only for the microphone, and deny is restored afterwards.
func TestTheCaptureGateChangesOnlyTheMicrophoneAndRestoresDeny(t *testing.T) {
	table := webviewPermissions()
	setCapturePermission(table, true)
	for kind, state := range table {
		want := application.PermissionDeny
		if kind == application.PermissionMicrophone {
			want = application.PermissionAllow
		}
		if state != want {
			t.Errorf("during a call permission kind %d is %d, want %d", kind, state, want)
		}
	}
	setCapturePermission(table, false)
	for kind, state := range table {
		if state != application.PermissionDeny {
			t.Errorf("after the call permission kind %d is %d, want deny", kind, state)
		}
	}
	if len(table) != len(webviewPermissions()) {
		t.Error("the capture gate added or removed table entries")
	}
	setCapturePermission(nil, true) // a host without a table must not panic
}

// A gate update that was queued when a call was answered may run after the
// update queued when it ended, or after its caller stopped waiting. It must
// not reopen the gate.
func TestALateCaptureGateUpdateAppliesThePresentTruth(t *testing.T) {
	table := webviewPermissions()
	answered := false
	truth := func() bool { return answered }

	answered = true
	queuedWhenAnswered := captureGateUpdate(table, truth)
	answered = false
	queuedWhenEnded := captureGateUpdate(table, truth)

	// The later update runs first; the earlier one arrives afterwards.
	queuedWhenEnded()
	queuedWhenAnswered()
	if table[application.PermissionMicrophone] != application.PermissionDeny {
		t.Fatal("an update queued for an earlier call state reopened the capture gate")
	}
	// Run again after any number of state changes: always the present truth.
	answered = true
	queuedWhenEnded()
	if table[application.PermissionMicrophone] != application.PermissionAllow {
		t.Fatal("an update did not apply the present truth")
	}
	answered = false
	queuedWhenAnswered()
	queuedWhenAnswered()
	if table[application.PermissionMicrophone] != application.PermissionDeny {
		t.Fatal("the gate stayed open after the call ended")
	}
	// Without a source of truth the gate is closed.
	table[application.PermissionMicrophone] = application.PermissionAllow
	captureGateUpdate(table, nil)()
	if table[application.PermissionMicrophone] != application.PermissionDeny {
		t.Fatal("a gate update with no source of truth left capture allowed")
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
