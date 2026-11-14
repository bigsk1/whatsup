package launch

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestWindowsStartupOnlyUpdatesAndRemovesFixedOwnedValue(t *testing.T) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	keyPath := `Software\WhatsUp\Tests\Launch-` + hex.EncodeToString(random[:])
	key, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal("disposable registry test namespace unavailable")
	}
	t.Cleanup(func() {
		_ = key.Close()
		if !strings.HasPrefix(keyPath, `Software\WhatsUp\Tests\Launch-`) {
			t.Fatal("test registry cleanup escaped its disposable namespace")
		}
		if err := registry.DeleteKey(registry.CURRENT_USER, keyPath); err != nil {
			t.Error("disposable registry test namespace could not be removed")
		}
	})
	if err := key.SetStringValue("Unrelated", "synthetic unrelated startup value"); err != nil {
		t.Fatal(err)
	}
	first := &windowsEntry{exe: temporaryExecutable(t, "old version.exe"), keyPath: keyPath}
	if err := first.Set(true); err != nil {
		t.Fatal(err)
	}
	command, _, err := key.GetStringValue(startupName)
	if err != nil || command != `"`+first.exe+`" --autostart` {
		t.Fatal("startup command lost executable quoting or fixed argument")
	}
	second := &windowsEntry{exe: temporaryExecutable(t, "new version.exe"), keyPath: keyPath}
	if err := second.Set(true); err != nil {
		t.Fatal(err)
	}
	command, _, err = key.GetStringValue(startupName)
	if err != nil || command != `"`+second.exe+`" --autostart` {
		t.Fatal("version update did not replace the fixed startup value")
	}
	if err := first.Set(false); err != nil {
		t.Fatal("disable depended on matching the previous executable path")
	}
	if _, _, err := key.GetStringValue(startupName); err != registry.ErrNotExist {
		t.Fatal("app-owned startup value was not removed")
	}
	if value, _, err := key.GetStringValue("Unrelated"); err != nil || value != "synthetic unrelated startup value" {
		t.Fatal("startup action changed an unrelated value")
	}
	if err := first.Set(false); err != nil {
		t.Fatal("startup disable was not idempotent")
	}
}

func TestWindowsStartupConstructorNeverWritesTheRealRunKey(t *testing.T) {
	exe := temporaryExecutable(t, "quoted executable.exe")
	entry, err := NewStartup(exe, false)
	if err != nil {
		t.Fatal(err)
	}
	if actual, ok := entry.(*windowsEntry); !ok || actual.keyPath != startupKey {
		t.Fatal("production startup adapter did not use its fixed Run key")
	}
	// Do not call Set on this production entry: all mutation tests above inject
	// an isolated registry namespace and retain unrelated values there.
	if _, err := NewStartup(exe, true); err == nil {
		t.Fatal("Windows accepted a Linux-only startup flag")
	}
}
