package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDamagedBackupPassphraseChangeReportsRepairInsteadOfWrongSecret(t *testing.T) {
	s := securityTestService(t, SecurityRuntimeOptions{})
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigurePassphrase("", securityTestSecret, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.directory, "pre-migration-00000000000000000000000000000000.vault")
	if err := os.WriteFile(path, []byte("WUPVAULT\x02{synthetic damaged root"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConfigurePassphrase(securityTestSecret, "synthetic desired replacement passphrase", true)
	if err == nil || got.StorageState == "unlocked" || !strings.Contains(err.Error(), "requires attention") || strings.Contains(err.Error(), s.directory) || strings.Contains(err.Error(), "current passphrase is incorrect") {
		t.Fatal("damaged backup misreported as incorrect secret", err)
	}
	attention := s.GetStorageSecurity()
	if attention.BackupAttention != filepath.Base(path) || !attention.RequiresPassphrase || !attention.BackupRepairAvailable {
		t.Fatal("repair summary lost file or authoritative protection")
	}
	if got, err := s.SetAsideBackup(securityTestSecret); err != nil || got.StorageState != "unlocked" {
		t.Fatal("repair could not retry committed original factors", err)
	}
	if s.GetStorageSecurity().BackupAttention != "" {
		t.Fatal("completed repair retained stale file authority")
	}
}

func TestUnsafeRecoveryDirectoryReportsAttentionWithoutRepairAuthority(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX private-directory permissions")
	}
	s := securityTestService(t, SecurityRuntimeOptions{})
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigurePassphrase("", securityTestSecret, true); err != nil {
		t.Fatal(err)
	}
	s.LockStorage()
	name := ".recovery-rollback-AQEBAQEBAQEBAQEBAQEBAQ"
	directory := filepath.Join(s.directory, name)
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0700) })
	rootPath := filepath.Join(s.directory, "state.vault")
	before, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.UnlockWithPassphrase(securityTestSecret)
	if err == nil || got.StorageState != "unavailable" || got.IdentityFingerprint != "" || strings.Contains(err.Error(), s.directory) || strings.Contains(err.Error(), "securely set it aside") {
		t.Fatal("unsafe directory did not fail closed with accurate relative guidance", err)
	}
	summary := s.GetStorageSecurity()
	if summary.BackupAttention != name || summary.BackupRepairAvailable {
		t.Fatal("unsafe directory granted set-aside authority")
	}
	if _, err := s.SetAsideBackup(securityTestSecret); err == nil {
		t.Fatal("unsafe directory accepted repair")
	}
	after, err := os.ReadFile(rootPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("refused repair changed live storage")
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := s.UnlockWithPassphrase(securityTestSecret); err != nil || got.StorageState != "unlocked" {
		t.Fatal("private directory did not restore authenticated access", err)
	}
}

func TestSetAsideRejectsChangedBackupAndNoAttention(t *testing.T) {
	s := securityTestService(t, SecurityRuntimeOptions{})
	if _, err := s.SetAsideBackup(""); err == nil {
		t.Fatal("repair allowed without a captured failure")
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigurePassphrase("", securityTestSecret, true); err != nil {
		t.Fatal(err)
	}
	s.LockStorage()
	path := filepath.Join(s.directory, "pre-migration-00000000000000000000000000000000.vault")
	if err := os.WriteFile(path, []byte("WUPVAULT\x02{synthetic initial corruption"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlockWithPassphrase(securityTestSecret); err == nil {
		t.Fatal("bad backup opened")
	}
	changed := []byte("WUPVAULT\x02{synthetic different corruption")
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SetAsideBackup(securityTestSecret); err == nil || got.StorageState == "unlocked" {
		t.Fatal("stale repair authority accepted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(changed) {
		t.Fatal("stale repair modified changed file")
	}
}

func TestForgetCallbackRunsAfterDurableRemovalOutsideServiceGuard(t *testing.T) {
	s := unlockedInvitationService(t)
	r := prepareDesktopForgetContact(t, s)
	called := false
	s.runtimeOptions.OnContactForgotten = func(id string) {
		if id != r.ID || s.GetStatus().StorageState != "unlocked" {
			t.Error("forget callback lost independent unlocked authority")
		}
		if snapshot := s.GetConversation(); snapshot.ContactID == r.ID {
			t.Error("callback ran before durable deletion")
		}
		called = true
	}
	if _, err := s.ForgetContact("invalid"); err == nil || called {
		t.Fatal("invalid forget invoked callback")
	}
	if _, err := s.ForgetContact(r.ID); err != nil || !called {
		t.Fatal("successful forget did not notify native host", err)
	}
}
