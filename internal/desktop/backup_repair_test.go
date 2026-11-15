package desktop

import (
	"os"
	"path/filepath"
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
	if attention.BackupAttention != filepath.Base(path) || !attention.RequiresPassphrase {
		t.Fatal("repair summary lost file or authoritative protection")
	}
	if got, err := s.SetAsideBackup(securityTestSecret); err != nil || got.StorageState != "unlocked" {
		t.Fatal("repair could not retry committed original factors", err)
	}
	if s.GetStorageSecurity().BackupAttention != "" {
		t.Fatal("completed repair retained stale file authority")
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
