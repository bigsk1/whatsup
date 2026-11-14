//go:build linux && cgo && !gtk3 && !purego && !android && !server

package main

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Opt in under an actual display or Xvfb. This test creates a WebKit renderer;
// profile-only APIs cannot prove that its Bubblewrap/DBus sandbox can launch.
func TestNativeWebKitSmoke(t *testing.T) {
	if os.Getenv("WHATSUP_TEST_NATIVE_WEBKIT") != "1" {
		t.Skip("requires an explicitly selected display and native sandbox integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeWebKitSmokeChild$")
	command.Env = append(os.Environ(), "WHATSUP_NATIVE_WEBKIT_CHILD=1")
	if err := command.Run(); err != nil {
		t.Fatal("native WebKit renderer/local JavaScript probe failed")
	}
}

func TestNativeWebKitSmokeChild(t *testing.T) {
	if os.Getenv("WHATSUP_NATIVE_WEBKIT_CHILD") != "1" {
		t.Skip("native probe subprocess only")
	}
	if unsafeSandboxOverride() {
		os.Exit(probeUnsafeEnv)
	}
	os.Exit(runNativeProbe(false))
}
