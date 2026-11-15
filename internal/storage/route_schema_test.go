package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"whatsup/internal/recovery"
)

func envelopeV7(s applicationEnvelope) applicationEnvelopeV7 {
	return applicationEnvelopeV7{Version: 7, Identity: s.Identity, Profile: s.Profile, Contacts: s.Contacts, PendingContacts: s.PendingContacts, CommittedContacts: s.CommittedContacts, MessageStoreState: s.MessageStoreState, MigrationBackup: s.MigrationBackup, MigrationBackupHash: s.MigrationBackupHash, DeleteScopes: s.DeleteScopes}
}
func TestRouteSchemaSevenUpgradePreservesSQLiteAndProtection(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "os-only", true: "passphrase"}[protected], func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			_, app := openApplicationTest(t, dir, keys)
			record, _, _, _, chat, _ := prepareForgetStorage(t, app, 58)
			identity, _ := app.Identity()
			seed, master := identity.Seed(), bytes.Clone(app.vault.master)
			ids, err := chat.PendingIDs()
			if err != nil {
				t.Fatal(err)
			}
			if protected {
				if err := app.SetPassphrase(context.Background(), nil, testPassphrase); err != nil {
					t.Fatal(err)
				}
			}
			payload, err := json.Marshal(envelopeV7(app.state))
			if err != nil {
				t.Fatal(err)
			}
			app.Close()
			secret := []byte(nil)
			if protected {
				secret = testPassphrase
			}
			raw, err := OpenWithPassphrase(dir, keys, secret)
			if err != nil {
				t.Fatal(err)
			}
			if err := raw.Save(payload); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			original, err := os.ReadFile(filepath.Join(dir, "state.vault"))
			if err != nil {
				t.Fatal(err)
			}
			beforeDB, err := os.ReadFile(filepath.Join(dir, "messages.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			_, app = openProtectedTest(t, dir, keys, secret)
			identity, _ = app.Identity()
			if app.state.Version != ApplicationSchemaVersion || app.state.MessageStoreState != "ready" || identity.Seed() != seed || !bytes.Equal(app.vault.master, master) || len(app.state.Routes) != 0 {
				t.Fatal("metadata migration changed hierarchy/history state")
			}
			backup, err := os.ReadFile(filepath.Join(dir, app.state.MigrationBackup))
			if err != nil || !bytes.Equal(backup, original) {
				t.Fatal("schema7 ciphertext backup changed")
			}
			records, _ := app.CommittedContacts()
			if len(records) != 1 || records[0] != record {
				t.Fatal("base changed in migration")
			}
			scope, err := app.ChatStore(&identifiedPin{approvedPin: &approvedPin{local: record.Local, peer: record.Peer}, id: record.ID})
			if err != nil {
				t.Fatal(err)
			}
			pending, err := scope.PendingIDs()
			if err != nil || len(pending) != len(ids) || pending[0] != ids[0] {
				t.Fatal("outbox changed")
			}
			protection, err := app.Protection()
			if err != nil || protection.RequiresPassphrase != protected {
				t.Fatal("metadata migration changed protection")
			}
			app.Close()
			afterDB, err := os.ReadFile(filepath.Join(dir, "messages.sqlite"))
			if err != nil || !bytes.Equal(beforeDB, afterDB) {
				t.Fatal("schema7 upgrade rewrote message database")
			}
		})
	}
}
func TestRouteRecoveryAcceptsPhaseFiveSchemaSevenArchive(t *testing.T) {
	f := makeRecoveryFixture(t)
	var entries []recovery.Entry
	data := map[recovery.Kind]map[string][]byte{}
	verified, err := recovery.Read(context.Background(), bytes.NewReader(f.archive), testPassphrase, func(ctx context.Context, e recovery.Entry, reader io.Reader) error {
		body, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		if e.Kind == recovery.Application {
			var current portableApplication
			if decodeCanonical(body, &current) != nil {
				return ErrRecovery
			}
			body, err = json.Marshal(portableApplicationV7{Version: 1, RequiresPassphrase: current.RequiresPassphrase, State: envelopeV7(current.State)})
			if err != nil {
				return err
			}
			e.Size = uint64(len(body))
			e.SHA256 = sha256.Sum256(body)
		}
		if data[e.Kind] == nil {
			data[e.Kind] = map[string][]byte{}
		}
		data[e.Kind][e.ID] = body
		entries = append(entries, e)
		return nil
	})
	if err != nil || !verified.Valid() {
		t.Fatal(err)
	}
	var legacy bytes.Buffer
	if err := recovery.Write(context.Background(), &legacy, testPassphrase, entries, func(ctx context.Context, e recovery.Entry) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data[e.Kind][e.ID])), nil
	}); err != nil {
		t.Fatal(err)
	}
	dir, keys := privateDirectory(t), &memoryKeys{}
	staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(legacy.Bytes()), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if staged.portable.State.Version != ApplicationSchemaVersion || staged.portable.State.Routes == nil {
		t.Fatal("old archive was not normalized after authentication")
	}
	if err := staged.Commit(context.Background(), keys, nil, false); err != nil {
		t.Fatal(err)
	}
	_, restored := openApplicationTest(t, dir, keys)
	assertRestoredFixture(t, restored, f)
}

func TestRouteMetadataBackupRejectsNonRegularSourceWithoutPublishing(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	source, saved := filepath.Join(dir, "state.vault"), filepath.Join(dir, "saved-cipher.vault")
	if err := os.Rename(source, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	defer func() { os.Remove(source); os.Rename(saved, source) }()
	app.vault.mu.Lock()
	err := app.backupRootMetadataLocked()
	app.vault.mu.Unlock()
	if err == nil {
		t.Fatal("nonregular source accepted")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if migrationBackupName.MatchString(entry.Name()) || ownedRootTemporaryName.MatchString(entry.Name()) {
			t.Fatal("unsafe source published a backup")
		}
	}
}
