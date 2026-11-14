package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"whatsup/internal/identity"
	"whatsup/internal/profile"
)

func TestProfileAndContactNamesPersistEncryptedWithoutChangingIdentityOrChatWriters(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, a := openApplicationTest(t, directory, keys)
	local, _ := a.Identity()
	seed, wrapping := local.Seed(), keys.value
	initial, err := a.Profile()
	if err != nil || initial != (profile.Local{Revision: 1}) {
		t.Fatal("new profile did not start unnamed with sharing off")
	}
	r, _, _, _, store, payload := prepareForgetStorage(t, a, 211)
	if got, err := a.ContactNames(r.ID); err != nil || got != (profile.Contact{}) {
		t.Fatal("new contact inherited display labels")
	}
	first, err := a.SetProfile("Synthetic owner", false)
	if err != nil || first.Revision != 2 || first.ShareDisplayName {
		t.Fatal("local profile save changed the explicit sharing choice")
	}
	if err := a.SetContactName(r.ID, "Synthetic local nickname"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetPeerDisplayName(r.ID, r.Peer, "Synthetic shared name"); err != nil {
		t.Fatal(err)
	}
	// Metadata saves must preserve the generation of an existing scoped writer.
	if err := writeChatFixtureTest(store, payload); err != nil {
		t.Fatal("name changes invalidated an unrelated chat writer")
	}
	if _, err := a.SetProfile("Synthetic owner", true); err != nil {
		t.Fatal(err)
	}
	last, err := a.SetProfile("Synthetic owner", false)
	if err != nil || last.Revision != 4 || last.DisplayName != "Synthetic owner" || last.ShareDisplayName {
		t.Fatal("turning sharing off lost the local name or revision")
	}
	// Idempotent reads and saves must neither write ciphertext nor bump revisions.
	writes := 0
	save := a.save
	a.save = func(payload []byte) error { writes++; return save(payload) }
	if same, err := a.SetProfile(last.DisplayName, last.ShareDisplayName); err != nil || same != last {
		t.Fatal("unchanged profile was not idempotent")
	}
	if err := a.SetContactName(r.ID, "Synthetic local nickname"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetPeerDisplayName(r.ID, r.Peer, "Synthetic shared name"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Profile(); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatal("unchanged metadata caused a disk write")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openApplicationTest(t, directory, keys)
	got, err := reopened.Profile()
	names, namesErr := reopened.ContactNames(r.ID)
	restored, _ := reopened.Identity()
	if err != nil || namesErr != nil || got != last || names != (profile.Contact{LocalName: "Synthetic local nickname", PeerDisplayName: "Synthetic shared name"}) || restored.Seed() != seed || keys.value != wrapping || keys.writes != 1 {
		t.Fatal("profile/name reopen changed identity, credential or metadata")
	}
	ciphertext, err := os.ReadFile(filepath.Join(directory, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"Synthetic owner", "Synthetic local nickname", "Synthetic shared name"} {
		if bytes.Contains(ciphertext, []byte(private)) {
			t.Fatal("profile/name reached disk in plaintext")
		}
	}
}

func TestContactNamesRequireExactApprovedIdentityAndCannotOverrideLocalName(t *testing.T) {
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	r, pin := commitOngoing(t, a, 212)
	if _, err := a.ContactNames(r.ID); err == nil || a.SetContactName(r.ID, "Local") == nil || a.SetPeerDisplayName(r.ID, r.Peer, "Peer") == nil {
		t.Fatal("unapproved committed record obtained profile authority")
	}
	if err := a.ApproveCommittedConversation(r, pin); err != nil {
		t.Fatal(err)
	}
	other, _ := identity.Generate()
	if a.SetPeerDisplayName(r.ID, other.PublicKey(), "Impersonator") == nil || a.SetPeerDisplayName("", r.Peer, "Impersonator") == nil || a.SetContactName(strings.Repeat("A", 22), "Stale") == nil {
		t.Fatal("wrong identity or contact ID changed a label")
	}
	if err := a.SetContactName(r.ID, "My private label"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Peer's old name", "Peer's new name", ""} {
		if err := a.SetPeerDisplayName(r.ID, r.Peer, name); err != nil {
			t.Fatal(err)
		}
		got, err := a.ContactNames(r.ID)
		if err != nil || got.LocalName != "My private label" || got.PeerDisplayName != name {
			t.Fatal("received metadata overwrote a local label")
		}
	}
	for _, invalid := range []string{" leading", "trailing ", "spoof\u202e", "hidden\u3164", strings.Repeat("x", 65)} {
		if !errors.Is(a.SetContactName(r.ID, invalid), profile.ErrName) || !errors.Is(a.SetPeerDisplayName(r.ID, r.Peer, invalid), profile.ErrName) {
			t.Fatal("invalid name crossed a storage boundary")
		}
		if _, err := a.SetProfile(invalid, false); !errors.Is(err, profile.ErrName) {
			t.Fatal("invalid own display name persisted")
		}
	}
}

func TestForgetRemovesNamesButKeepsOwnProfileAndRejectsLateMetadata(t *testing.T) {
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	owner, err := a.SetProfile("Synthetic owner", true)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, _, _, _ := prepareForgetStorage(t, a, 213)
	if a.SetContactName(r.ID, "Old private label") != nil || a.SetPeerDisplayName(r.ID, r.Peer, "Old shared name") != nil {
		t.Fatal("could not prepare names")
	}
	if err := a.ForgetCommittedContact(r, func() error {
		if _, err := a.ContactNames(r.ID); err == nil || a.SetContactName(r.ID, "Late local") == nil || a.SetPeerDisplayName(r.ID, r.Peer, "Late remote") == nil {
			t.Error("cancellation claim retained metadata authority")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fresh := ongoingRecord(t, a, 214)
	fresh.Peer = r.Peer
	if err := a.SavePendingContact(fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CommitPendingContact(fresh); err != nil {
		t.Fatal(err)
	}
	pin := &identifiedPin{approvedPin: &approvedPin{local: fresh.Local, peer: fresh.Peer}, id: fresh.ID}
	if err := a.ApproveCommittedConversation(fresh, pin); err != nil {
		t.Fatal(err)
	}
	got, err := a.ContactNames(fresh.ID)
	retained, ownErr := a.Profile()
	if err != nil || ownErr != nil || got != (profile.Contact{}) || retained != owner {
		t.Fatal("fresh pairing inherited forgotten labels or lost the own profile")
	}
	if a.SetContactName(r.ID, "Stale local") == nil || a.SetPeerDisplayName(r.ID, r.Peer, "Stale remote") == nil {
		t.Fatal("late old association metadata changed a replacement contact")
	}
}

func TestProfileStorageFailuresCloseAuthorityAndBoundRevisions(t *testing.T) {
	for _, field := range []string{"own", "local", "peer"} {
		t.Run(field, func(t *testing.T) {
			_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
			r, _, _, _, _, _ := prepareForgetStorage(t, a, 215)
			a.save = func([]byte) error { return errors.New("synthetic failed metadata save") }
			var err error
			switch field {
			case "peer":
				err = a.SetPeerDisplayName(r.ID, r.Peer, "Synthetic new name")
			case "local":
				err = a.SetContactName(r.ID, "Synthetic local name")
			default:
				_, err = a.SetProfile("Synthetic owner", true)
			}
			if !errors.Is(err, ErrStateFailed) {
				t.Fatal("failed persistence reported success")
			}
			if _, err := a.Profile(); err == nil || a.SetContactName(r.ID, "After failure") == nil {
				t.Fatal("uncertain metadata persistence retained authority")
			}
		})
	}
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	a.state.Profile.Revision = math.MaxUint64
	if _, err := a.SetProfile("Overflow", true); !errors.Is(err, ErrCapacity) {
		t.Fatal("profile revision wrapped")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Profile(); !errors.Is(err, ErrLocked) || a.SetContactName("", "x") == nil {
		t.Fatal("closed metadata state remained available")
	}
	var missing *ApplicationState
	if _, err := missing.Profile(); !errors.Is(err, ErrLocked) {
		t.Fatal("missing application exposed a profile")
	}
	if _, err := missing.SetProfile("Missing", true); !errors.Is(err, ErrLocked) || missing.SetPeerDisplayName("", identity.PublicKey{}, "Missing") == nil {
		t.Fatal("missing application accepted metadata")
	}
}

func TestUncertainProfileSaveRequiresReopenToReconcileAuthenticatedState(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, a := openApplicationTest(t, directory, keys)
	save := a.save
	a.save = func(payload []byte) error {
		if err := save(payload); err != nil {
			return err
		}
		return errors.New("synthetic failure after ciphertext replacement")
	}
	if _, err := a.SetProfile("Synthetic requested name", true); !errors.Is(err, ErrStateFailed) {
		t.Fatal("uncertain replaced profile was reported as saved")
	}
	if _, err := a.Profile(); !errors.Is(err, ErrStateFailed) {
		t.Fatal("poisoned profile remained readable")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openApplicationTest(t, directory, keys)
	got, err := reopened.Profile()
	if err != nil || got != (profile.Local{DisplayName: "Synthetic requested name", ShareDisplayName: true, Revision: 2}) {
		t.Fatal("reopen failed to reconcile authenticated replacement")
	}
}

func TestConcurrentNameAndChatSavesDoNotLoseEitherSection(t *testing.T) {
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	r, _, _, _, store, payload := prepareForgetStorage(t, a, 216)
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for i := range 25 {
			if err := a.SetContactName(r.ID, "Synthetic-label-"+strings.Repeat("x", i)); err != nil {
				errors <- err
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		for range 25 {
			if err := writeChatFixtureTest(store, payload); err != nil {
				errors <- err
				return
			}
		}
	}()
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	got, err := a.ContactNames(r.ID)
	if err != nil || got.LocalName != "Synthetic-label-"+strings.Repeat("x", 24) {
		t.Fatal("concurrent metadata/chat saves lost a section")
	}
	if record, err := store.Get("AAAAAAAAAAAAAAAAAAAAAA"); err != nil || record.Text != "synthetic message waiting offline" {
		t.Fatal("concurrent metadata saves changed a committed message record")
	}
}

func TestSchemaFiveMigrationPreservesExactIdentityContactsAndHistory(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, a := openApplicationTest(t, directory, keys)
	r, _, _, _, _, payload := prepareForgetStorage(t, a, 217)
	pending := ongoingRecord(t, a, 218)
	if err := a.SavePendingContact(pending); err != nil {
		t.Fatal(err)
	}
	local, _ := a.Identity()
	seed, wrapping := local.Seed(), keys.value
	old := applicationEnvelopeV5{Version: 5, Identity: a.state.Identity, Contacts: []contactSectionV5{}, PendingContacts: a.state.PendingContacts, CommittedContacts: a.state.CommittedContacts}
	for _, c := range a.state.Contacts {
		old.Contacts = append(old.Contacts, contactSectionV5{Version: c.Version, Local: c.Local, Peer: c.Peer, ContactID: c.ContactID, Chat: bytes.Clone(payload)})
	}
	encoded, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	vault := legacyVaultForTest(t, directory, keys)
	if err := vault.Save(encoded); err != nil {
		t.Fatal(err)
	}
	migrated, err := OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	gotIdentity, _ := migrated.Identity()
	gotProfile, _ := migrated.Profile()
	gotNames, err := migrated.ContactNames(r.ID)
	committed, _ := migrated.CommittedContacts()
	gotPending, _ := migrated.PendingContacts()
	scope, scopeErr := migrated.ChatStore(&identifiedPin{approvedPin: &approvedPin{local: r.Local, peer: r.Peer}, id: r.ID})
	var history []byte
	if scopeErr == nil {
		history, scopeErr = loadChatFixtureTest(scope)
	}
	if err != nil || scopeErr != nil || migrated.state.Version != ApplicationSchemaVersion || gotIdentity.Seed() != seed || keys.value != wrapping || keys.writes != 1 || gotProfile != (profile.Local{Revision: 1}) || gotNames != (profile.Contact{}) || len(committed) != 1 || committed[0] != r || len(gotPending) != 1 || gotPending[0] != pending || !bytes.Equal(history, payload) {
		t.Fatal("schema five migration changed old state or enabled sharing")
	}
	// New fields under the old version must be rejected rather than silently
	// reinterpreted. The same applies to schema 6 with missing/invalid profile.
	for _, invalid := range [][]byte{
		bytes.Replace(encoded, []byte(`"contacts":`), []byte(`"profile":{"displayName":"Injected","shareDisplayName":true,"revision":1},"contacts":`), 1),
		bytes.Replace(encoded, []byte(`"contactID":`), []byte(`"localName":"Injected","contactID":`), 1),
		bytes.Replace(encoded, []byte(`"version":5`), []byte(`"version":6`), 1),
	} {
		v := openTest(t, privateDirectory(t), &memoryKeys{})
		if err := v.Save(invalid); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenApplication(v); !errors.Is(err, ErrState) {
			t.Fatal("malformed migration snapshot accepted")
		}
	}
}
