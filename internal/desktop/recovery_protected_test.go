package desktop

import (
	"context"
	"path/filepath"
	"testing"
)

// Exercise the complete typed desktop recovery path, including dialog handles,
// quiescence, protection summary and reopen over a different protected identity.
func TestDesktopRecoveryReplacesDifferentProtectedProfileWithoutOldAppSecret(t *testing.T) {
	for _, protected := range []bool{true, false} {
		t.Run(map[bool]string{true: "protected", false: "os-only-then-protect"}[protected], func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "synthetic.whatsup-backup")
			source := securityTestService(t, SecurityRuntimeOptions{SaveRecovery: func(context.Context) (string, error) { return archive, nil }})
			if _, err := source.UnlockStorage(); err != nil {
				t.Fatal(err)
			}
			record := prepareDesktopForgetContact(t, source)
			identity := source.GetStatus().IdentityFingerprint
			message := source.GetConversation().Messages[0]
			if result, err := source.ExportRecovery(securityTestSecret); err != nil || !result.Saved {
				t.Fatal("source export", err)
			}
			target := securityTestService(t, SecurityRuntimeOptions{PickRecovery: func(context.Context) (string, error) { return archive, nil }})
			if _, err := target.UnlockStorage(); err != nil {
				t.Fatal(err)
			}
			oldIdentity := target.GetStatus().IdentityFingerprint
			if identity == oldIdentity {
				t.Fatal("test identities unexpectedly match")
			}
			const oldSecret = "synthetic retired profile passphrase"
			const newSecret = "synthetic restored profile passphrase"
			if _, err := target.ConfigurePassphrase("", oldSecret, true); err != nil {
				t.Fatal(err)
			}
			credential := target.keys.(*testKeys).key
			target.LockStorage()
			secret := ""
			if protected {
				secret = newSecret
			}
			for range 2 {
				preview, err := target.StageRecoveryImport(securityTestSecret)
				if err != nil || !preview.ReplaceRequired || preview.CredentialResetRequired || preview.Contacts != 1 || preview.Messages != 1 {
					t.Fatal("replacement preview", err)
				}
				got, err := target.CommitRecoveryImport(preview.Handle, secret, protected, true)
				if err != nil || got.StorageState != "unlocked" || got.IdentityFingerprint != identity || got.RequiresPassphrase != protected {
					t.Fatal("protected replacement failed", err)
				}
				if target.keys.(*testKeys).key != credential {
					t.Fatal("replacement changed OS credential")
				}
				conversation := target.GetConversation()
				if conversation.ContactID != record.ID || !conversation.CanSend || len(conversation.Messages) != 1 || conversation.Messages[0].ID != message.ID || conversation.Messages[0].Text != message.Text || conversation.Messages[0].Delivered {
					t.Fatal("replacement changed approved history or stable outbox identity")
				}
				security := target.GetStorageSecurity()
				if security.Busy || security.BackupAttention != "" || security.BackupRepairAvailable {
					t.Fatal("replacement retained pending operation or backup error")
				}
				target.LockStorage()
				if protected {
					if _, err := target.UnlockStorage(); err == nil {
						t.Fatal("replacement lost required app factor")
					}
					if _, err := target.UnlockWithPassphrase(oldSecret); err == nil {
						t.Fatal("retired passphrase unlocked restored profile")
					}
					got, err = target.UnlockWithPassphrase(newSecret)
				} else {
					got, err = target.UnlockStorage()
				}
				if err != nil || got.StorageState != "unlocked" || got.IdentityFingerprint != identity {
					t.Fatal("restored profile did not reopen", err)
				}
			}
			if !protected {
				if _, err := target.ConfigurePassphrase("", newSecret, true); err != nil {
					t.Fatal("retiring root blocked later protection", err)
				}
				target.LockStorage()
				if got, err := target.UnlockWithPassphrase(newSecret); err != nil || got.IdentityFingerprint != identity {
					t.Fatal("later protection lost restored identity", err)
				}
			}
		})
	}
}
