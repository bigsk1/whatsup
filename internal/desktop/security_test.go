package desktop

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const securityTestSecret = "  synthetic café passphrase  "

func securityTestService(t *testing.T, options SecurityRuntimeOptions) *Service {
	t.Helper()
	if options.PickRecovery == nil {
		options.PickRecovery = func(context.Context) (string, error) { return "", nil }
	}
	if options.SaveRecovery == nil {
		options.SaveRecovery = func(context.Context) (string, error) { return "", nil }
	}
	s := New(filepath.Join(t.TempDir(), "private"), &testKeys{})
	if err := ConfigureSecurity(s, options); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ServiceShutdown() })
	return s
}

func TestDesktopPassphraseKeepsIdentityAndRequiresBothFactorsAfterRestart(t *testing.T) {
	s := securityTestService(t, SecurityRuntimeOptions{})
	first, err := s.UnlockStorage()
	if err != nil {
		t.Fatal(err)
	}
	wrapping := s.keys.(*testKeys).key
	if got, err := s.ConfigurePassphrase("", securityTestSecret, true); err != nil || !got.RequiresPassphrase || got.StorageState != "unlocked" {
		t.Fatal("passphrase protection was not enabled", err)
	}
	if _, err := s.application.Identity(); err != nil {
		t.Fatal("normal protected state operation failed", err)
	}
	s.LockStorage()
	if got, err := s.UnlockStorage(); err == nil || !got.RequiresPassphrase || got.StorageState != "locked" || got.IdentityFingerprint != "" {
		t.Fatalf("OS-only unlock did not stay protected: error=%v requires=%t state=%s fingerprintPresent=%t", err, got.RequiresPassphrase, got.StorageState, got.IdentityFingerprint != "")
	}
	if got, err := s.UnlockWithPassphrase("wrong synthetic passphrase"); err == nil || got.StorageState != "locked" || got.IdentityFingerprint != "" {
		t.Fatal("wrong passphrase exposed local state")
	}
	if got, err := s.UnlockWithPassphrase(securityTestSecret); err != nil || got.IdentityFingerprint != first.IdentityFingerprint {
		t.Fatal("exact passphrase did not preserve identity", err)
	}
	if got, err := s.ConfigurePassphrase(securityTestSecret, "", false); err != nil || got.RequiresPassphrase {
		t.Fatal("passphrase protection was not disabled", err)
	}
	s.LockStorage()
	if got, err := s.UnlockStorage(); err != nil || got.IdentityFingerprint != first.IdentityFingerprint || s.keys.(*testKeys).key != wrapping {
		t.Fatal("OS-only restart changed identity or OS wrapping credential", err)
	}
}

func TestDesktopRecoveryRoundTripRestoresApprovedContactHistoryAndIdentity(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "synthetic.whatsup-backup")
	source := securityTestService(t, SecurityRuntimeOptions{SaveRecovery: func(context.Context) (string, error) { return archive, nil }})
	if _, err := source.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	record := prepareDesktopForgetContact(t, source)
	before := source.GetStatus().IdentityFingerprint
	message := source.GetConversation().Messages[0]
	if got, err := source.ExportRecovery(securityTestSecret); err != nil || !got.Saved {
		t.Fatal("recovery export failed", err)
	}
	if got := source.GetConversation(); source.GetStatus().StorageState != "unlocked" || len(got.Messages) != 1 || got.Messages[0].ID != message.ID {
		t.Fatal("snapshot export did not reopen the original conversation")
	}
	target := securityTestService(t, SecurityRuntimeOptions{PickRecovery: func(context.Context) (string, error) { return archive, nil }})
	if got, err := target.StageRecoveryImport("incorrect recovery secret"); err == nil || got.Handle != "" || target.keys.(*testKeys).key != "" {
		t.Fatal("wrong recovery secret created a target credential or capability")
	}
	preview, err := target.StageRecoveryImport(securityTestSecret)
	if err != nil || preview.Handle == "" || preview.Contacts != 1 || preview.Messages != 1 || preview.ReplaceRequired || preview.CredentialResetRequired {
		t.Fatalf("authenticated import preview is incorrect: error=%v handlePresent=%t contacts=%d messages=%d replace=%t reset=%t", err, preview.Handle != "", preview.Contacts, preview.Messages, preview.ReplaceRequired, preview.CredentialResetRequired)
	}
	if got, err := target.CommitRecoveryImport(preview.Handle, securityTestSecret, true, false); err != nil || got.StorageState != "unlocked" || !got.RequiresPassphrase || got.IdentityFingerprint != before {
		t.Fatal("recovery did not install the same protected identity", err)
	}
	conversation := target.GetConversation()
	if conversation.ContactID != record.ID || !conversation.CanSend || len(conversation.Messages) != 1 || conversation.Messages[0].ID != message.ID || conversation.Messages[0].Text != message.Text || conversation.Messages[0].Delivered {
		t.Fatal("recovery changed contact approval, queued message or stable ID")
	}
	if _, err := target.CommitRecoveryImport(preview.Handle, "", false, true); err == nil {
		t.Fatal("consumed import capability was reusable")
	}
	target.LockStorage()
	if _, err := target.UnlockStorage(); err == nil {
		t.Fatal("restored protection did not survive restart")
	}
	if got, err := target.UnlockWithPassphrase(securityTestSecret); err != nil || got.IdentityFingerprint != before {
		t.Fatal("restored protected state could not reopen", err)
	}
}

func TestDesktopSecurityOfflineRetiresDelayedNativeDialogs(t *testing.T) {
	for _, exporting := range []bool{false, true} {
		t.Run(map[bool]string{false: "import", true: "export"}[exporting], func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "must-not-be-created.whatsup-backup")
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			choose := func(context.Context) (string, error) { close(entered); <-release; return filename, nil }
			s := securityTestService(t, SecurityRuntimeOptions{PickRecovery: choose, SaveRecovery: choose})
			if _, err := s.UnlockStorage(); err != nil {
				t.Fatal(err)
			}
			go func() {
				defer close(done)
				if exporting {
					_, _ = s.ExportRecovery(securityTestSecret)
				} else {
					_, _ = s.StageRecoveryImport(securityTestSecret)
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("native dialog was not requested")
			}
			if !s.GetStorageSecurity().Busy {
				t.Fatal("active native operation was not reflected")
			}
			s.LockStorage()
			close(release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled native operation did not finish")
			}
			if s.GetStatus().StorageState != "locked" || s.GetStorageSecurity().Busy {
				t.Fatal("late native dialog revived retired storage")
			}
			if _, err := os.Stat(filename); !os.IsNotExist(err) {
				t.Fatal("late native selection created an output")
			}
		})
	}
}

func TestDesktopRecoveryReplacementIsExplicitAndOfflineRetiresPreview(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "synthetic.whatsup-backup")
	source := securityTestService(t, SecurityRuntimeOptions{SaveRecovery: func(context.Context) (string, error) { return archive, nil }})
	if _, err := source.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	sourceIdentity := source.GetStatus().IdentityFingerprint
	if got, err := source.ExportRecovery(securityTestSecret); err != nil || !got.Saved {
		t.Fatal("source export failed", err)
	}
	target := securityTestService(t, SecurityRuntimeOptions{PickRecovery: func(context.Context) (string, error) { return archive, nil }})
	if _, err := target.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	oldIdentity := target.GetStatus().IdentityFingerprint
	preview, err := target.StageRecoveryImport(securityTestSecret)
	if err != nil || !preview.ReplaceRequired || preview.CredentialResetRequired {
		t.Fatal("existing readable target replacement was not identified", err)
	}
	if _, err := target.CommitRecoveryImport(preview.Handle, "", false, false); err == nil || target.GetStatus().IdentityFingerprint != oldIdentity {
		t.Fatal("unconfirmed replacement changed local identity")
	}
	preview, err = target.StageRecoveryImport(securityTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	target.LockStorage()
	if _, err := target.CommitRecoveryImport(preview.Handle, "", false, true); err == nil {
		t.Fatal("Go offline retained a decrypted recovery capability")
	}
	preview, err = target.StageRecoveryImport(securityTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := target.CommitRecoveryImport(preview.Handle, "", false, true); err != nil || got.IdentityFingerprint != sourceIdentity {
		t.Fatal("confirmed replacement failed", err)
	}
}

func TestDesktopRecoveryExportNeverOverwritesAnExistingFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "kept.whatsup-backup")
	original := []byte("synthetic existing file must remain byte-identical")
	if err := os.WriteFile(filename, original, 0600); err != nil {
		t.Fatal(err)
	}
	s := securityTestService(t, SecurityRuntimeOptions{SaveRecovery: func(context.Context) (string, error) { return filename, nil }})
	before, err := s.UnlockStorage()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.ExportRecovery(securityTestSecret); err == nil || got.Saved {
		t.Fatal("existing export path was accepted")
	}
	after, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(original, after) || s.GetStatus().IdentityFingerprint != before.IdentityFingerprint || s.GetStorageSecurity().Busy {
		t.Fatal("failed export changed file or local session")
	}
}
