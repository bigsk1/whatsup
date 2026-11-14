package storage

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"whatsup/internal/message"
	"whatsup/internal/profile"
)

const migrationTextSentinel = "SCHEMA6_MIGRATION_PRIVATE_TEXT_SENTINEL/"

type phase1Fixture struct {
	directory        string
	keys             *memoryKeys
	vault            *Vault
	legacy           []byte
	original         []byte
	seed             [32]byte
	credential       string
	contact, pending PendingRecord
	profile          profile.Local
	names            profile.Contact
	chat             storedChat
}

func migrationMessageID(index uint64) string {
	var id [16]byte
	binary.BigEndian.PutUint64(id[8:], index)
	return base64.RawURLEncoding.EncodeToString(id[:])
}

func newPhase1Fixture(t *testing.T, aesLegacy bool) *phase1Fixture {
	t.Helper()
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, directory, keys)
	record, pin := commitOngoing(t, app, 229)
	if err := app.ApproveCommittedConversation(record, pin); err != nil {
		t.Fatal(err)
	}
	pending := ongoingRecord(t, app, 230)
	identity, err := app.Identity()
	if err != nil {
		t.Fatal(err)
	}
	f := &phase1Fixture{directory: directory, keys: keys, seed: identity.Seed(), credential: keys.value, contact: record, pending: pending,
		profile: profile.Local{DisplayName: "MIGRATION_PRIVATE_DISPLAY", ShareDisplayName: true, Revision: 4},
		names:   profile.Contact{LocalName: "MIGRATION_PRIVATE_NICKNAME", PeerDisplayName: "MIGRATION_PEER_DISPLAY"},
		chat:    storedChat{Version: 1, Local: record.Local, Peer: record.Peer},
	}
	for index := uint64(1); index <= 256; index++ {
		item := storedMessage{ID: migrationMessageID(index), Direction: message.Outgoing, Text: migrationTextSentinel + migrationMessageID(index), CreatedAt: time.Date(2026, 11, 13, 0, 0, int(index), 0, time.UTC), Delivered: index%3 == 0}
		if index%3 == 2 {
			item.Direction, item.Delivered = message.Incoming, true
		}
		f.chat.Messages = append(f.chat.Messages, item)
		if item.Direction == message.Outgoing && !item.Delivered {
			f.chat.Outbox = append(f.chat.Outbox, storedAttempt{ID: item.ID, Count: 7, NextAttempt: item.CreatedAt.Add(5 * time.Minute)})
		}
	}
	chat, err := json.Marshal(f.chat)
	if err != nil {
		t.Fatal(err)
	}
	legacy := applicationEnvelopeV6{Version: 6, Identity: identitySection{Version: 1, Seed: bytes.Clone(f.seed[:])}, Profile: f.profile,
		Contacts:        []contactSectionV6{{Version: 2, Local: record.Local, Peer: record.Peer, ContactID: record.ID, Chat: chat, LocalName: f.names.LocalName, PeerName: f.names.PeerDisplayName}},
		PendingContacts: []pendingRecordDisk{pendingRecordDisk(pending)}, CommittedContacts: []pendingRecordDisk{pendingRecordDisk(record)}}
	f.legacy, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	f.vault = legacyVaultForTest(t, directory, keys)
	if aesLegacy {
		aead, err := f.vault.aead()
		if err != nil {
			t.Fatal(err)
		}
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			t.Fatal(err)
		}
		sealed := append(append(bytes.Clone(header), nonce...), aead.Seal(nil, nonce, f.legacy, header)...)
		if err := os.WriteFile(filepath.Join(directory, "state.vault"), sealed, 0600); err != nil {
			t.Fatal(err)
		}
	} else if err := f.vault.Save(f.legacy); err != nil {
		t.Fatal(err)
	}
	f.original, err = os.ReadFile(filepath.Join(directory, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clear(f.legacy); clear(f.seed[:]) })
	return f
}

func assertPhase1Migration(t *testing.T, f *phase1Fixture, app *ApplicationState) *ChatStore {
	t.Helper()
	local, err := app.Identity()
	if err != nil || local.Seed() != f.seed || f.keys.value != f.credential || f.keys.writes != 1 {
		t.Fatal("migration changed device identity or OS credential")
	}
	gotProfile, profileErr := app.Profile()
	gotNames, namesErr := app.ContactNames(f.contact.ID)
	committed, committedErr := app.CommittedContacts()
	pending, pendingErr := app.PendingContacts()
	if profileErr != nil || namesErr != nil || gotProfile != f.profile || gotNames != f.names || committedErr != nil || len(committed) != 1 || committed[0] != f.contact || pendingErr != nil || len(pending) != 1 || pending[0] != f.pending {
		t.Fatal("migration changed names, profile or contact credentials")
	}
	if app.state.Version != ApplicationSchemaVersion || app.state.MessageStoreState != "ready" || len(app.state.Contacts) != 1 || len(app.state.Contacts[0].Chat) != 0 || !migrationBackupName.MatchString(app.state.MigrationBackup) {
		t.Fatal("migration did not finish with a small envelope and verified message store")
	}
	backupPath := filepath.Join(f.directory, app.state.MigrationBackup)
	backup, err := os.ReadFile(backupPath)
	if err != nil || !bytes.Equal(backup, f.original) {
		t.Fatal("pre-migration backup is not the exact original ciphertext")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(backupPath); err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("migration backup is not private")
		}
	}
	pin := &identifiedPin{approvedPin: &approvedPin{local: f.contact.Local, peer: f.contact.Peer}, id: f.contact.ID}
	scope, err := app.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	for index, old := range f.chat.Messages {
		got, err := scope.Get(old.ID)
		if err != nil || got.ID != old.ID || got.Direction != old.Direction || got.Text != old.Text || !got.CreatedAt.Equal(old.CreatedAt) || got.Delivered != old.Delivered || got.Sequence != uint64(index+1) {
			t.Fatal("migration lost or changed a message record")
		}
	}
	ids, err := scope.PendingIDs()
	if err != nil || len(ids) != len(f.chat.Outbox) {
		t.Fatal("migration lost or invented outbox records")
	}
	for index, id := range ids {
		if id != f.chat.Outbox[index].ID {
			t.Fatal("migration changed pending message IDs or order")
		}
	}
	assertMigrationCiphertextFiles(t, f)
	return scope
}

func assertMigrationCiphertextFiles(t *testing.T, f *phase1Fixture) {
	t.Helper()
	entries, err := os.ReadDir(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		// These zero-content OS lock files are deliberately unreadable while
		// held on Windows; they are not persistence payloads or SQLite sidecars.
		if entry.Name() == "state.lock" || entry.Name() == "messages.lock" || entry.Name() == "attachments.lock" {
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(f.directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, sentinel := range []string{migrationTextSentinel, f.profile.DisplayName, f.names.LocalName, f.names.PeerDisplayName, f.contact.LocalNodePrivate} {
			if bytes.Contains(data, []byte(sentinel)) {
				t.Fatal("plaintext reached a database, WAL, temporary, root or backup file")
			}
		}
	}
}

func TestPhase1SchemaSixMigrationBacksUpBothRootFormatsAndRetainsExactHistory(t *testing.T) {
	for _, aesLegacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrapped-root", true: "AES-v1-root"}[aesLegacy], func(t *testing.T) {
			f := newPhase1Fixture(t, aesLegacy)
			app, err := OpenApplication(f.vault)
			if err != nil {
				t.Fatal(err)
			}
			scope := assertPhase1Migration(t, f, app)
			// The legacy lifetime count quota must not leak into typed writes.
			next := message.Message{ID: migrationMessageID(257), Direction: message.Outgoing, Text: "synthetic 257th record after migration", CreatedAt: time.Now().UTC()}
			if err := scope.Queue(next); err != nil {
				t.Fatal("migrated scope retained the 256-message lifetime cap")
			}
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			_, reopened := openApplicationTest(t, f.directory, f.keys)
			scope = assertPhase1MigrationWithExtra(t, f, reopened, next)
			if usage, err := scope.Usage(); err != nil || usage.QuotaBytes != 1<<30 || usage.Full {
				t.Fatal("migrated scope did not use the default per-contact disk quota")
			}
		})
	}
}

func assertPhase1MigrationWithExtra(t *testing.T, f *phase1Fixture, app *ApplicationState, extra message.Message) *ChatStore {
	t.Helper()
	copy := *f
	copy.chat = f.chat
	copy.chat.Messages = append(append([]storedMessage(nil), f.chat.Messages...), storedMessage{ID: extra.ID, Direction: extra.Direction, Text: extra.Text, CreatedAt: extra.CreatedAt, Delivered: extra.Delivered})
	copy.chat.Outbox = append(append([]storedAttempt(nil), f.chat.Outbox...), storedAttempt{ID: extra.ID, NextAttempt: extra.CreatedAt})
	return assertPhase1Migration(t, &copy, app)
}

// Build the same durable pending state that OpenApplication writes, then stop
// at chosen persistence boundaries without production-global failure hooks.
func pendingPhase1Migration(t *testing.T, f *phase1Fixture) *ApplicationState {
	t.Helper()
	v := f.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	loaded, err := v.loadLocked()
	defer clear(loaded)
	if err != nil {
		t.Fatal(err)
	}
	var old applicationEnvelopeV6
	if decodeCanonical(loaded, &old) != nil {
		t.Fatal("invalid synthetic schema-6 fixture")
	}
	a := &ApplicationState{vault: v, save: v.saveLocked, commitReceipts: make(map[string]*commitReceiptState)}
	a.state = applicationEnvelope{Version: ApplicationSchemaVersion, Identity: old.Identity, Profile: old.Profile, PendingContacts: old.PendingContacts, CommittedContacts: old.CommittedContacts, Routes: []routeStateDisk{}}
	for _, c := range old.Contacts {
		a.state.Contacts = append(a.state.Contacts, contactSection{Version: c.Version, Local: c.Local, Peer: c.Peer, ContactID: c.ContactID, Chat: c.Chat, LocalName: c.LocalName, PeerName: c.PeerName})
	}
	if err := a.prepareMessageMigrationLocked(); err != nil {
		t.Fatal(err)
	}
	v.application = a
	if err := a.commitLocked(a.state); err != nil {
		t.Fatal(err)
	}
	return a
}

func interruptBeforeReady(t *testing.T, a *ApplicationState, writeReady bool) {
	t.Helper()
	write := a.save
	a.save = func(payload []byte) error {
		var next applicationEnvelope
		if decodeCanonical(payload, &next) != nil {
			return ErrState
		}
		if next.MessageStoreState == "ready" {
			if writeReady {
				if err := write(payload); err != nil {
					return err
				}
			}
			return errors.New("synthetic interrupted migration completion")
		}
		return write(payload)
	}
	a.vault.mu.Lock()
	err := a.openMessageStoreLocked()
	a.vault.mu.Unlock()
	if !errors.Is(err, ErrStateFailed) || a.state.MessageStoreState != "pending" {
		t.Fatal("interrupted completion was reported as ready")
	}
}

func TestPhase1PendingMigrationResumesExactImportWithoutDuplicateMessages(t *testing.T) {
	for _, boundary := range []string{"before-import", "before-ready", "after-ready-write"} {
		t.Run(boundary, func(t *testing.T) {
			f := newPhase1Fixture(t, true)
			a := pendingPhase1Migration(t, f)
			backup := a.state.MigrationBackup
			if boundary != "before-import" {
				interruptBeforeReady(t, a, boundary == "after-ready-write")
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			_, resumed := openApplicationTest(t, f.directory, f.keys)
			scope := assertPhase1Migration(t, f, resumed)
			if resumed.state.MigrationBackup != backup {
				t.Fatal("resuming migration replaced the original encrypted backup")
			}
			var count int
			var cursor uint64
			for {
				page, err := scope.Page(cursor, 100)
				if err != nil {
					t.Fatal(err)
				}
				if len(page) == 0 {
					break
				}
				count += len(page)
				cursor = page[0].Sequence
			}
			if count != len(f.chat.Messages) {
				t.Fatal("resuming import duplicated or dropped messages")
			}
		})
	}
}

func TestPhase1ChangedDatabaseCannotCompletePendingMigration(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "authenticated-extra-record", true: "changed-ciphertext"}[tamper], func(t *testing.T) {
			f := newPhase1Fixture(t, true)
			a := pendingPhase1Migration(t, f)
			interruptBeforeReady(t, a, false)
			if !tamper {
				scope, err := a.messages.Scope(a.state.Contacts[0].StoreID)
				if err != nil {
					t.Fatal(err)
				}
				if err := scope.Queue(message.Message{ID: migrationMessageID(1000), Direction: message.Outgoing, Text: "synthetic authenticated unexpected row", CreatedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			if tamper {
				db, err := sql.Open("sqlite", filepath.Join(f.directory, "messages.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("UPDATE records SET cipher=zeroblob(length(cipher)) WHERE contact_id=? AND message_id=?", f.contact.ID, f.chat.Messages[0].ID)
				closeErr := db.Close()
				if err != nil || closeErr != nil {
					t.Fatal("could not prepare ciphertext tamper fixture")
				}
			}
			before, err := os.ReadFile(filepath.Join(f.directory, "state.vault"))
			if err != nil {
				t.Fatal(err)
			}
			v := openTest(t, f.directory, f.keys)
			if _, err := OpenApplication(v); err == nil {
				t.Fatal("changed import was marked ready")
			}
			after, err := os.ReadFile(filepath.Join(f.directory, "state.vault"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed import rewrote the pending root")
			}
		})
	}
}

func TestPhase1EmptyLegacyScopeCannotInheritUnexpectedImportedRecords(t *testing.T) {
	f := newPhase1Fixture(t, true)
	var old applicationEnvelopeV6
	if decodeCanonical(f.legacy, &old) != nil {
		t.Fatal("invalid synthetic legacy fixture")
	}
	old.Contacts[0].Chat = nil
	clear(f.legacy)
	var err error
	f.legacy, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := f.vault.aead()
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	f.original = append(append(bytes.Clone(header), nonce...), aead.Seal(nil, nonce, f.legacy, header)...)
	if err := os.WriteFile(filepath.Join(f.directory, "state.vault"), f.original, 0600); err != nil {
		t.Fatal(err)
	}
	a := pendingPhase1Migration(t, f)
	interruptBeforeReady(t, a, false)
	scope, err := a.messages.Scope(a.state.Contacts[0].StoreID)
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.Queue(message.Message{ID: migrationMessageID(1000), Direction: message.Outgoing, Text: "synthetic record absent from an empty legacy history", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(f.directory, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	v := openTest(t, f.directory, f.keys)
	if _, err := OpenApplication(v); err == nil {
		t.Fatal("empty legacy chat inherited unexpected pending database records")
	}
	after, err := os.ReadFile(filepath.Join(f.directory, "state.vault"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unexpected empty-scope import was marked ready")
	}
}

func TestPhase1ExactRecommitCannotReviveOldScopedHandles(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record, pin, _, _, original, _ := prepareForgetStorage(t, app, 227)
	unclaimed, err := app.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := app.ChatStore(pin)
	if err != nil || claimed.Claim() != nil {
		t.Fatal("could not claim a synthetic original scope")
	}
	if err := app.ForgetCommittedContact(record, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// This intentionally exercises the stronger internal API case. Normal fresh
	// pairing generates new IDs/keys; exact recommit must still use a fresh epoch.
	if err := app.SavePendingContact(record); err != nil {
		t.Fatal(err)
	}
	receipt, err := app.CommitPendingContact(record)
	if err != nil || receipt.ConsumeForActivation(record) != nil || app.ApproveCommittedConversation(record, pin) != nil {
		t.Fatal("could not prepare synthetic exact recommit")
	}
	fresh, err := app.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	item := queuedMessageTest(migrationMessageID(2000), "synthetic replacement epoch history")
	if err := fresh.Queue(item); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []*ChatStore{original, unclaimed, claimed} {
		if _, err := stale.Get(item.ID); err == nil {
			t.Fatal("old scope read a recommitted association's history")
		}
		if _, err := stale.PendingIDs(); err == nil {
			t.Fatal("old scope accessed a recommitted association's outbox")
		}
		if err := stale.Queue(queuedMessageTest(migrationMessageID(2001), "synthetic forbidden stale write")); err == nil {
			t.Fatal("old scope wrote after the exact contact record was recommitted")
		}
	}
	page, err := fresh.Page(0, 100)
	if err != nil || len(page) != 1 || page[0].ID != item.ID {
		t.Fatal("stale handles changed the replacement epoch's data")
	}
}

func TestPhase1MissingOrTamperedBackupCannotFinishPendingMigration(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "tampered"}[tamper], func(t *testing.T) {
			f := newPhase1Fixture(t, true)
			a := pendingPhase1Migration(t, f)
			backup := filepath.Join(f.directory, a.state.MigrationBackup)
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			if tamper {
				data, err := os.ReadFile(backup)
				if err != nil {
					t.Fatal(err)
				}
				data[len(data)-1] ^= 1
				if err := os.WriteFile(backup, data, 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(backup); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(f.directory, "state.vault"))
			if err != nil {
				t.Fatal(err)
			}
			v := openTest(t, f.directory, f.keys)
			if _, err := OpenApplication(v); err == nil {
				t.Fatal("pending migration completed without its intact required backup")
			}
			after, err := os.ReadFile(filepath.Join(f.directory, "state.vault"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed backup verification rewrote the pending root")
			}
		})
	}
}

func TestPhase1LostRootOrDatabaseDoesNotMintReplacementState(t *testing.T) {
	for _, missingRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "database-missing", true: "root-missing"}[missingRoot], func(t *testing.T) {
			f := newPhase1Fixture(t, true)
			a, err := OpenApplication(f.vault)
			if err != nil {
				t.Fatal(err)
			}
			assertPhase1Migration(t, f, a)
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			if missingRoot {
				if err := os.Remove(filepath.Join(f.directory, "state.vault")); err != nil {
					t.Fatal(err)
				}
				if _, err := Open(f.directory, f.keys); !errors.Is(err, ErrCorrupt) {
					t.Fatal("orphan database acquired a replacement root")
				}
				absent := &memoryKeys{}
				if _, err := Open(f.directory, absent); !errors.Is(err, ErrCorrupt) || absent.writes != 0 {
					t.Fatal("orphan database acquired a replacement credential")
				}
			} else {
				for _, name := range []string{"messages.sqlite", "messages.sqlite-wal", "messages.sqlite-shm", "messages.sqlite-journal"} {
					if err := os.Remove(filepath.Join(f.directory, name)); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
				rootBefore, _ := os.ReadFile(filepath.Join(f.directory, "state.vault"))
				v := openTest(t, f.directory, f.keys)
				if _, err := OpenApplication(v); err == nil {
					t.Fatal("completed root silently created an empty replacement database")
				}
				rootAfter, _ := os.ReadFile(filepath.Join(f.directory, "state.vault"))
				if !bytes.Equal(rootBefore, rootAfter) {
					t.Fatal("missing database changed its authenticated root")
				}
				if _, err := os.Stat(filepath.Join(f.directory, "messages.sqlite")); !os.IsNotExist(err) {
					t.Fatal("missing database was recreated")
				}
			}
			if f.keys.value != f.credential || f.keys.writes != 1 {
				t.Fatal("state loss replaced an existing OS credential")
			}
		})
	}
}
