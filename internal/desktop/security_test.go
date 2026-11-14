package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const securityTestSecret = "  synthetic café passphrase  "

func TestDamagedBackupUnlockExplainsTheSpecificLocalFile(t *testing.T) {
	s := securityTestService(t, SecurityRuntimeOptions{})
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigurePassphrase("", securityTestSecret, true); err != nil {
		t.Fatal(err)
	}
	s.LockStorage()
	path := filepath.Join(s.directory, "pre-migration-00000000000000000000000000000000.vault")
	if err := os.WriteFile(path, []byte("WUPVAULT\x02{truncated synthetic root"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.UnlockWithPassphrase(securityTestSecret)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "Preserve this file") || got.StorageState != "unavailable" || got.IdentityFingerprint != "" {
		t.Fatal("managed backup failure is not actionable and closed", err)
	}
	if err := os.Rename(path, filepath.Join(t.TempDir(), "preserved.vault")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.UnlockWithPassphrase(securityTestSecret); err != nil || got.StorageState != "unlocked" {
		t.Fatal("repair did not restore healthy access", err)
	}
}

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

func TestWrongCurrentPassphraseKeepsUnlockedConversationAndProtection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disable", true: "change"}[enabled], func(t *testing.T) {
			s := securityTestService(t, SecurityRuntimeOptions{})
			if _, err := s.UnlockStorage(); err != nil {
				t.Fatal(err)
			}
			prepareDesktopForgetContact(t, s)
			if _, err := s.ConfigurePassphrase("", securityTestSecret, true); err != nil {
				t.Fatal(err)
			}
			before := s.GetStatus()
			conversation := s.GetConversation()
			vault, application, messages := s.vault, s.application, s.messages
			ciphertext, err := os.ReadFile(filepath.Join(s.directory, "state.vault"))
			if err != nil {
				t.Fatal(err)
			}
			next := ""
			if enabled {
				next = "different synthetic passphrase"
			}
			for _, wrong := range []string{"wrong synthetic passphrase", "", "short"} {
				got, err := s.ConfigurePassphrase(wrong, next, enabled)
				if err == nil || got.StorageState != "unlocked" || !got.RequiresPassphrase || got.IdentityFingerprint != before.IdentityFingerprint {
					t.Fatal("wrong current secret changed local access or protection")
				}
				if s.vault != vault || s.application != application || s.messages != messages || s.GetStorageSecurity().Busy {
					t.Fatal("validation error retired runtime handles or retained a busy operation")
				}
				after := s.GetConversation()
				if after.ContactID != conversation.ContactID || len(after.Messages) != len(conversation.Messages) || after.Messages[0] != conversation.Messages[0] {
					t.Fatal("validation error changed the queued conversation")
				}
				stored, err := os.ReadFile(filepath.Join(s.directory, "state.vault"))
				if err != nil || !bytes.Equal(stored, ciphertext) {
					t.Fatal("validation error wrote protected state")
				}
			}
			if got, err := s.ConfigurePassphrase(securityTestSecret, next, enabled); err != nil || got.StorageState != "unlocked" || got.RequiresPassphrase != enabled {
				t.Fatal("correct current secret could not retry normally", err)
			}
		})
	}
}

func TestPassphraseChangeStillClosesStorageOnCorruptRoot(t *testing.T) {
	s := securityTestService(t, SecurityRuntimeOptions{})
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigurePassphrase("", securityTestSecret, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.directory, "state.vault")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the root's syntax and wrapping slots valid, but break authentication
	// of the encrypted application payload after correct factor verification.
	offset := bytes.IndexByte(data, '{')
	var root map[string]json.RawMessage
	if offset < 0 || json.Unmarshal(data[offset:], &root) != nil {
		t.Fatal("invalid synthetic root")
	}
	var payload map[string][]byte
	if json.Unmarshal(root["payload"], &payload) != nil {
		t.Fatal("invalid synthetic payload")
	}
	for _, value := range payload {
		if len(value) > 24 {
			before, _ := json.Marshal(value)
			value[len(value)-1] ^= 1
			after, _ := json.Marshal(value)
			data = bytes.Replace(data, before, after, 1)
		}
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConfigurePassphrase(securityTestSecret, "", false)
	if err == nil || got.StorageState == "unlocked" || s.application != nil || s.vault != nil || s.messages != nil {
		t.Fatal("root authentication failure was treated as an ordinary validation error")
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
