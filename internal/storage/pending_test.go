package storage

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tailscale.com/types/key"

	"whatsup/internal/identity"
)

func pendingFor(t *testing.T, a *ApplicationState, fill byte) PendingRecord {
	t.Helper()
	local, err := a.Identity()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := identity.FromSeed(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	private := key.NewNode()
	encoded, _ := private.MarshalText()
	record := PendingRecord{
		Version: 1, ID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 16)), Role: "inviter",
		Local: local.PublicKey(), Peer: peer.PublicKey(), LocalNodePrivate: string(encoded),
		LocalNode: private.Public().String(), PeerNode: key.NewNode().Public().String(),
		PresharedKey: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32)),
	}
	clear(encoded)
	if err := ValidatePendingRecord(record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestPendingRecordRejectsInvalidCredentialAssociationsAndExports(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	valid := pendingFor(t, app, 101)
	if _, grantsAuthority := any(valid).(ApprovedContact); grantsAuthority {
		t.Fatal("pending persistence record implements approved-contact authority")
	}
	for _, value := range []any{valid, &valid} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if fmt.Sprintf(format, value) != "[private WhatsUp pending contact]" {
				t.Fatal("pending record formatting exposed credentials")
			}
		}
		if encoded, err := json.Marshal(value); err == nil || len(encoded) != 0 {
			t.Fatal("pending credentials serialized outside the encrypted envelope")
		}
	}
	changes := map[string]func(*PendingRecord){
		"version":           func(r *PendingRecord) { r.Version = 2 },
		"zero-id":           func(r *PendingRecord) { r.ID = base64.RawURLEncoding.EncodeToString(make([]byte, 16)) },
		"padded-id":         func(r *PendingRecord) { r.ID += "==" },
		"bad-role":          func(r *PendingRecord) { r.Role = "active" },
		"zero-local":        func(r *PendingRecord) { r.Local = identity.PublicKey{} },
		"same-peer":         func(r *PendingRecord) { r.Peer = r.Local },
		"zero-private":      func(r *PendingRecord) { r.LocalNodePrivate = "privkey:" + strings.Repeat("0", 64) },
		"public-as-private": func(r *PendingRecord) { r.LocalNodePrivate = r.LocalNode },
		"wrong-local-node":  func(r *PendingRecord) { r.LocalNode = key.NewNode().Public().String() },
		"same-nodes":        func(r *PendingRecord) { r.PeerNode = r.LocalNode },
		"zero-peer-node":    func(r *PendingRecord) { r.PeerNode = "nodekey:" + strings.Repeat("0", 64) },
		"uppercase-node":    func(r *PendingRecord) { r.PeerNode = strings.ToUpper(r.PeerNode) },
		"zero-psk":          func(r *PendingRecord) { r.PresharedKey = base64.RawURLEncoding.EncodeToString(make([]byte, 32)) },
		"padded-psk":        func(r *PendingRecord) { r.PresharedKey += "=" },
		"oversized":         func(r *PendingRecord) { r.LocalNodePrivate = strings.Repeat("a", 4096) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			bad := valid
			change(&bad)
			if !errors.Is(ValidatePendingRecord(bad), ErrPending) {
				t.Fatal("malformed pending credentials accepted")
			}
			if !errors.Is(app.SavePendingContact(bad), ErrPending) {
				t.Fatal("malformed pending credentials reached storage")
			}
		})
	}
}

func TestPendingContactEncryptedReopenAndScopeIsolation(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, directory, keys)
	local, _ := app.Identity()
	seed := local.Seed()
	pin := pinFor(t, app, 102)
	if err := app.PinApprovedContact(pin); err != nil {
		t.Fatal(err)
	}
	chat, _ := app.ChatStore(pin)
	payload := queuedChat(t, pin, "synthetic existing history")
	if err := chat.Save(payload); err != nil {
		t.Fatal(err)
	}
	record := pendingFor(t, app, 103)
	if err := app.SavePendingContact(record); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
	if err := app.SavePendingContact(record); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
	if !bytes.Equal(before, after) {
		t.Fatal("identical pending retry rewrote encrypted state")
	}
	for _, secret := range []string{record.LocalNodePrivate, record.PresharedKey, record.Peer.String(), "synthetic existing history", "pendingContacts"} {
		if bytes.Contains(after, []byte(secret)) {
			t.Fatal("private pending or chat data persisted as plaintext")
		}
	}
	pendingPin := &approvedPin{local: record.Local, peer: record.Peer}
	if _, err := app.ChatStore(pendingPin); !errors.Is(err, ErrContact) {
		t.Fatal("pending contact gained access to a chat scope")
	}
	if len(app.state.Contacts) != 1 {
		t.Fatal("pending credentials created an approval pin")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	_, app = openApplicationTest(t, directory, keys)
	loaded, err := app.PendingContacts()
	if err != nil || len(loaded) != 1 || loaded[0] != record {
		t.Fatal("pending credential bundle did not survive encrypted reopen")
	}
	loaded[0].PresharedKey = "caller changed copy"
	retained, _ := app.PendingContacts()
	if retained[0] != record {
		t.Fatal("caller modified the retained pending record")
	}
	restored, _ := app.Identity()
	if restored.Seed() != seed || !bytes.Equal(readChat(t, app, pin), payload) || keys.writes != 1 {
		t.Fatal("pending write replaced identity, wrapping key or existing history")
	}
	if err := app.RemovePendingContact(record.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.RemovePendingContact(record.ID); err != nil {
		t.Fatal("pending removal was not idempotent")
	}
	if got, _ := app.PendingContacts(); len(got) != 0 || !bytes.Equal(readChat(t, app, pin), payload) {
		t.Fatal("pending removal changed another scope")
	}
}

func TestSchemaTwoMigrationPreservesIdentityPinsAndChat(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	vault := openTest(t, directory, keys)
	seed := bytes.Repeat([]byte{104}, 32)
	local, _ := identity.FromSeed(seed)
	peer, _ := identity.FromSeed(bytes.Repeat([]byte{105}, 32))
	pin := &approvedPin{local: local.PublicKey(), peer: peer.PublicKey()}
	chat := queuedChat(t, pin, "synthetic schema two queue")
	legacy, err := json.Marshal(applicationEnvelopeV2{
		Version: 2, Identity: identitySection{Version: 1, Seed: seed},
		Contacts: []contactSection{{Version: 1, Local: pin.local, Peer: pin.peer, Chat: chat}},
	})
	if err != nil || vault.Save(legacy) != nil {
		t.Fatal("could not prepare synthetic schema two snapshot")
	}
	app, err := OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := app.Identity()
	restoredSeed := restored.Seed()
	if app.state.Version != ApplicationSchemaVersion || app.state.PendingContacts == nil || len(app.state.PendingContacts) != 0 || !bytes.Equal(restoredSeed[:], seed) || !bytes.Equal(readChat(t, app, pin), chat) {
		t.Fatal("schema two migration changed existing state")
	}
	_ = app.Close()
	_, app = openApplicationTest(t, directory, keys)
	if keys.writes != 1 || !bytes.Equal(readChat(t, app, pin), chat) {
		t.Fatal("migration changed wrapping credential or durable queue")
	}
}

func TestPendingContactConflictCapacityAndConcurrentChatPreservation(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record := pendingFor(t, app, 106)
	if err := app.SavePendingContact(record); err != nil {
		t.Fatal(err)
	}
	changed := record
	changed.PresharedKey = pendingFor(t, app, 107).PresharedKey
	if !errors.Is(app.SavePendingContact(changed), ErrPendingConflict) {
		t.Fatal("same ID replaced its credential set")
	}
	changed = record
	changed.ID = pendingFor(t, app, 108).ID
	if !errors.Is(app.SavePendingContact(changed), ErrPendingConflict) {
		t.Fatal("same peer acquired another concurrent pending attempt")
	}
	changed = record
	changed.Local, changed.Peer = changed.Peer, changed.Local
	if !errors.Is(app.SavePendingContact(changed), ErrPending) {
		t.Fatal("another local identity wrote pending state")
	}
	pin := pinFor(t, app, 109)
	if err := app.PinApprovedContact(pin); err != nil {
		t.Fatal(err)
	}
	first, _ := app.ChatStore(pin)
	stale, _ := app.ChatStore(pin)
	_, _ = stale.Load()
	newer := queuedChat(t, pin, "synthetic concurrent queue")
	var group sync.WaitGroup
	errorsFound := make(chan error, MaxPendingContacts)
	group.Go(func() { errorsFound <- first.Save(newer) })
	for index := 1; index < MaxPendingContacts; index++ {
		next := pendingFor(t, app, byte(110+index))
		group.Go(func() { errorsFound <- app.SavePendingContact(next) })
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := app.SavePendingContact(pendingFor(t, app, 160)); !errors.Is(err, ErrCapacity) {
		t.Fatal("pending credential count was unbounded")
	}
	if err := app.RemovePendingContact(record.ID); err != nil {
		t.Fatal(err)
	}
	if err := stale.Save(emptyChat(t, pin)); !errors.Is(err, ErrStateConflict) {
		t.Fatal("pending writes/removal reset the stale-chat guard")
	}
	if !bytes.Equal(readChat(t, app, pin), newer) {
		t.Fatal("pending writes or stale chat replaced newer content")
	}
}

func TestPendingContactUncertainPersistencePoisonsAllScopes(t *testing.T) {
	for _, writeFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-replacement", true: "after-replacement"}[writeFirst], func(t *testing.T) {
			directory, keys := privateDirectory(t), &memoryKeys{}
			vault, app := openApplicationTest(t, directory, keys)
			record := pendingFor(t, app, 161)
			app.save = func(payload []byte) error {
				if writeFirst {
					if err := vault.saveLocked(payload); err != nil {
						return err
					}
				}
				return errors.New("synthetic uncertain pending save")
			}
			if !errors.Is(app.SavePendingContact(record), ErrStateFailed) || len(app.state.PendingContacts) != 0 {
				t.Fatal("uncertain save published memory or hid failure")
			}
			if _, err := app.Identity(); !errors.Is(err, ErrStateFailed) {
				t.Fatal("pending failure left identity scope usable")
			}
			if _, err := app.PendingContacts(); !errors.Is(err, ErrStateFailed) {
				t.Fatal("pending failure left pending reads usable")
			}
			if !errors.Is(app.RemovePendingContact(record.ID), ErrStateFailed) {
				t.Fatal("pending removal overwrote uncertain durable state")
			}
			_ = app.Close()
			_, app = openApplicationTest(t, directory, keys)
			restored, err := app.PendingContacts()
			if err != nil || len(restored) != map[bool]int{false: 0, true: 1}[writeFirst] {
				t.Fatal("reopen failed to reconcile the actual durable state")
			}
		})
	}
}

func TestMalformedPendingEnvelopeIsPreservedAndRejected(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record := pendingFor(t, app, 162)
	if err := app.SavePendingContact(record); err != nil {
		t.Fatal(err)
	}
	valid, err := json.Marshal(app.state)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := app.cloneLocked()
	duplicate.PendingContacts = append(duplicate.PendingContacts, duplicate.PendingContacts[0])
	duplicateBytes, _ := json.Marshal(duplicate)
	wrongNode := bytes.Replace(valid, []byte(record.LocalNode), []byte(record.PeerNode), 1)
	unknownField := bytes.Replace(valid, []byte(`"role":"inviter"`), []byte(`"role":"inviter","active":true`), 1)
	nullPending := bytes.Replace(valid, []byte(`"pendingContacts":[`), []byte(`"pendingContacts":null,"extra":[`), 1)
	for index, payload := range [][]byte{duplicateBytes, wrongNode, unknownField, nullPending} {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			directory := privateDirectory(t)
			vault := openTest(t, directory, &memoryKeys{})
			if err := vault.Save(payload); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
			if _, err := OpenApplication(vault); !errors.Is(err, ErrState) {
				t.Fatal("malformed pending envelope accepted")
			}
			after, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
			if !bytes.Equal(before, after) {
				t.Fatal("invalid encrypted pending state was replaced")
			}
		})
	}
}
