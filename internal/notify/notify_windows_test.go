//go:build windows

package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

func TestWindowsActivationAcceptsOnlyCanonicalContactForDefaultMessageAction(t *testing.T) {
	contactID := syntheticNotice().ContactID
	valid := notifications.NotificationResult{Response: notifications.NotificationResponse{
		ID: syntheticNotice().ID, ActionIdentifier: notifications.DefaultActionIdentifier,
		UserInfo: map[string]interface{}{"contactID": contactID},
	}}
	if windowsActivation(valid) != contactID {
		t.Fatal("canonical default activation was lost")
	}
	for _, change := range []func(*notifications.NotificationResult){
		func(value *notifications.NotificationResult) { value.Error = errors.New("synthetic error") },
		func(value *notifications.NotificationResult) { value.Response.ActionIdentifier = "untrusted-action" },
		func(value *notifications.NotificationResult) { value.Response.ID = "not a message ID" },
		func(value *notifications.NotificationResult) { value.Response.UserInfo = nil },
		func(value *notifications.NotificationResult) {
			value.Response.UserInfo = map[string]interface{}{"contactID": 1}
		},
		func(value *notifications.NotificationResult) {
			value.Response.UserInfo = map[string]interface{}{"contactID": "AAAAAAAAAAAAAAAAAAAAAA"}
		},
		func(value *notifications.NotificationResult) {
			value.Response.UserInfo = map[string]interface{}{"contactID": contactID, "body": "synthetic content"}
		},
	} {
		value := valid
		change(&value)
		if windowsActivation(value) != "" {
			t.Fatal("malformed or non-default activation requested a conversation")
		}
	}
}

func TestPrivateToastABIRequiresExactReviewedModuleAndChecksum(t *testing.T) {
	reviewed := debug.Module{Path: "git.sr.ht/~jackmordaunt/go-toast/v2", Version: "v2.0.3", Sum: "h1:N3IGoHHp9pb6mj1cbXbuaSXV/UMKwmbKLf53nQmtqMA="}
	if !toastBuildApproved(&debug.BuildInfo{Deps: []*debug.Module{&reviewed}}) {
		t.Fatal("exact reviewed module was not accepted")
	}
	for _, changed := range []debug.Module{
		{Path: reviewed.Path, Version: "v2.0.4", Sum: reviewed.Sum},
		{Path: reviewed.Path, Version: reviewed.Version, Sum: "h1:unreviewed"},
		{Path: reviewed.Path, Version: reviewed.Version, Sum: reviewed.Sum, Replace: &debug.Module{Path: "../elsewhere", Version: reviewed.Version}},
		{Path: "example.invalid/toast", Version: reviewed.Version, Sum: reviewed.Sum},
	} {
		if toastBuildApproved(&debug.BuildInfo{Deps: []*debug.Module{&changed}}) {
			t.Fatal("unreviewed private toast ABI accepted")
		}
	}
	if toastBuildApproved(nil) || toastBuildApproved(&debug.BuildInfo{}) {
		t.Fatal("missing provenance enabled private toast ABI")
	}
}

func TestWindowsNativeNotificationStartupIsScopedAndDisplaysNothing(t *testing.T) {
	if os.Getenv("WHATSUP_NATIVE_NOTIFY_STARTUP") != "1" {
		t.Skip("explicit scoped native startup probe only")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	appID := "WhatsUp-Notify-QA-" + hex.EncodeToString(token[:])
	binary := filepath.Join(t.TempDir(), "native-notification-startup.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-mod=readonly", "-o", binary, "./testdata/startup")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("scoped native probe could not build: %v %s", err, output)
	}
	command := exec.CommandContext(ctx, binary, appID)
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "NATIVE_NOTIFICATION_STARTUP_OK" {
		t.Fatalf("scoped native notification startup failed: %v", err)
	}
}
