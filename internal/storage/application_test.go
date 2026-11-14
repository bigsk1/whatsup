package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"whatsup/internal/identity"
	"whatsup/internal/message"
)

func TestApplicationScopeFormattingNeverRevealsStateCopies(t *testing.T) {
	state := ApplicationState{state: applicationEnvelope{Identity: identitySection{Seed: []byte("synthetic private seed")}}}
	store := ChatStore{application: &state}
	for _, value := range []any{state, &state, store, &store} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			got := fmt.Sprintf(format, value)
			if got != "[private WhatsUp application state]" && got != "[private WhatsUp scoped chat store]" {
				t.Fatal("application scope formatting exposed private fields")
			}
		}
	}
}

// Unit tests deliberately inject local approval only. Actual pairing.Contact +
// chat.Store interoperability is checked by application_state_test integration.
type approvedPin struct {
	mu          sync.RWMutex
	local, peer identity.PublicKey
	revoked     bool
}

func (p *approvedPin) Local() identity.PublicKey { return p.local }
func (p *approvedPin) Peer() identity.PublicKey  { return p.peer }
func (p *approvedPin) AuthorizeAuthenticatedPeer(peer identity.PublicKey) error {
	return p.WithAuthenticatedPeer(peer, func() error { return nil })
}
func (p *approvedPin) WithAuthenticatedPeer(peer identity.PublicKey, operation func() error) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.revoked || !p.peer.Equal(peer) {
		return ErrContact
	}
	return operation()
}

func openApplicationTest(t *testing.T, dir string, keys *memoryKeys) (*Vault, *ApplicationState) {
	t.Helper()
	v := openTest(t, dir, keys)
	a, err := OpenApplication(v)
	if err != nil {
		t.Fatal(err)
	}
	return v, a
}
func pinFor(t *testing.T, a *ApplicationState, fill byte) *approvedPin {
	t.Helper()
	local, err := a.Identity()
	if err != nil {
		t.Fatal(err)
	}
	peer, _ := identity.FromSeed(bytes.Repeat([]byte{fill}, 32))
	return &approvedPin{local: local.PublicKey(), peer: peer.PublicKey()}
}
func emptyChat(t *testing.T, pin *approvedPin) []byte {
	t.Helper()
	payload, err := json.Marshal(storedChat{Version: 1, Local: pin.local, Peer: pin.peer})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
func queuedChat(t *testing.T, pin *approvedPin, text string) []byte {
	t.Helper()
	id := "AAAAAAAAAAAAAAAAAAAAAA"
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	payload, err := json.Marshal(storedChat{Version: 1, Local: pin.local, Peer: pin.peer, Messages: []storedMessage{{ID: id, Direction: "outgoing", Text: text, CreatedAt: now}}, Outbox: []storedAttempt{{ID: id, NextAttempt: now}}})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func readChat(t *testing.T, a *ApplicationState, pin *approvedPin) []byte {
	t.Helper()
	store, err := a.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := loadChatFixtureTest(store)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// These fixture codecs preserve exact legacy snapshots for migration assertions
// while exercising the new typed record API. They do not restore snapshot
// writes to production or replace any existing record/history wholesale.
func writeChatFixtureTest(store *ChatStore, payload []byte) error {
	if validateChat(payload, store.local, store.peer) != nil {
		return ErrState
	}
	var state storedChat
	if decodeCanonical(payload, &state) != nil {
		return ErrState
	}
	if _, err := store.PendingIDs(); err != nil {
		return err
	}
	for _, old := range state.Messages {
		item := message.Message{ID: old.ID, Direction: old.Direction, Text: old.Text, CreatedAt: old.CreatedAt, Delivered: old.Delivered}
		if old.Direction == message.Incoming {
			if _, err := store.Receive(item); err != nil {
				return err
			}
		} else {
			item.Delivered = false
			if err := store.Queue(item); err != nil {
				return err
			}
			if old.Delivered {
				if err := store.Acknowledge(old.ID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func loadChatFixtureTest(store *ChatStore) ([]byte, error) {
	if err := store.Claim(); err != nil {
		return nil, err
	}
	return encodeChatRecordsTest(store)
}

func encodeChatRecordsTest(store *ChatStore) ([]byte, error) {
	state := storedChat{Version: 1, Local: store.local, Peer: store.peer}
	var records []message.Message
	var cursor uint64
	for {
		page, err := store.Page(cursor, 100)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		records = append(page, records...)
		cursor = page[0].Sequence
	}
	if len(records) == 0 {
		return nil, os.ErrNotExist
	}
	pending, err := store.PendingIDs()
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(pending))
	for _, id := range pending {
		ids[id] = true
	}
	for _, item := range records {
		state.Messages = append(state.Messages, storedMessage{ID: item.ID, Direction: item.Direction, Text: item.Text, CreatedAt: item.CreatedAt, Delivered: item.Delivered})
		if ids[item.ID] {
			state.Outbox = append(state.Outbox, storedAttempt{ID: item.ID, NextAttempt: item.CreatedAt})
		}
	}
	return json.Marshal(state)
}

func queuedMessageTest(id, text string) message.Message {
	return message.Message{ID: id, Direction: message.Outgoing, Text: text, CreatedAt: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
}

// A historical snapshot fixture must not inherit the new fixture application's
// SQLite database. Only synthetic TempDir files are removed, after its close.
func legacyVaultForTest(t *testing.T, directory string, keys *memoryKeys) *Vault {
	t.Helper()
	for _, name := range []string{"messages.sqlite", "messages.sqlite-wal", "messages.sqlite-shm", "messages.sqlite-journal", "messages.lock"} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	return openTest(t, directory, keys)
}

func TestApplicationScopesCannotOverwriteIdentityOrAnotherContact(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	v, a := openApplicationTest(t, dir, keys)
	local, _ := a.Identity()
	fingerprint := local.PublicKey().Fingerprint()
	if same, err := OpenApplication(v); err != nil || same != a {
		t.Fatal("multiple coordinators for one vault")
	}
	pin := pinFor(t, a, 12)
	if _, err := a.ChatStore(pin); !errors.Is(err, ErrContact) {
		t.Fatal("unpinned contact opened history")
	}
	if err := a.PinApprovedContact(pin); err != nil {
		t.Fatal(err)
	}
	store, err := a.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadChatFixtureTest(store); !os.IsNotExist(err) {
		t.Fatal("new contact obtained existing history")
	}
	payload := queuedChat(t, pin, "synthetic scoped content")
	if err := writeChatFixtureTest(store, payload); err != nil {
		t.Fatal(err)
	}
	clear(payload)
	loaded := readChat(t, a, pin)
	if !bytes.Contains(loaded, []byte("synthetic scoped content")) {
		t.Fatal("caller mutated saved state")
	}
	clear(loaded)
	loaded = readChat(t, a, pin)
	if !bytes.Contains(loaded, []byte("synthetic scoped content")) {
		t.Fatal("caller mutated retained state")
	}
	other := pinFor(t, a, 13)
	if err := a.PinApprovedContact(other); err != nil {
		t.Fatal(err)
	}
	otherStore, err := a.ChatStore(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherStore.Get("AAAAAAAAAAAAAAAAAAAAAA"); !errors.Is(err, message.ErrNotFound) {
		t.Fatal("another approved contact obtained this contact's record")
	}
	if err := v.Save([]byte(`{}`)); !errors.Is(err, ErrScope) {
		t.Fatal("raw writer bypassed envelope")
	}
	if _, err := v.Load(); !errors.Is(err, ErrScope) {
		t.Fatal("raw reader bypassed envelope")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadChatFixtureTest(store); !errors.Is(err, ErrLocked) {
		t.Fatal("closed scope readable")
	}
	_, a = openApplicationTest(t, dir, keys)
	local, _ = a.Identity()
	if local.PublicKey().Fingerprint() != fingerprint {
		t.Fatal("chat replaced identity")
	}
	store, err = a.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ = loadChatFixtureTest(store)
	if !bytes.Contains(loaded, []byte("synthetic scoped content")) {
		t.Fatal("chat did not survive reopen")
	}
	ciphertext, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
	if bytes.Contains(ciphertext, []byte("synthetic scoped content")) || bytes.Contains(ciphertext, []byte(`"identity"`)) {
		t.Fatal("plaintext envelope persisted")
	}
}

func TestApplicationMigratesIdentityV1WithoutReplacingIt(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	v := openTest(t, dir, keys)
	seed := bytes.Repeat([]byte{17}, 32)
	legacy, _ := json.Marshal(struct {
		Version int    `json:"version"`
		Seed    []byte `json:"identitySeed"`
	}{1, seed})
	if err := v.Save(legacy); err != nil {
		t.Fatal(err)
	}
	a, err := OpenApplication(v)
	if err != nil {
		t.Fatal(err)
	}
	local, _ := a.Identity()
	got := local.Seed()
	if !bytes.Equal(got[:], seed) || a.state.Version != ApplicationSchemaVersion {
		t.Fatal("migration changed identity")
	}
	_ = a.Close()
	_, a = openApplicationTest(t, dir, keys)
	local, _ = a.Identity()
	got = local.Seed()
	if !bytes.Equal(got[:], seed) || keys.writes != 1 {
		t.Fatal("migration replaced identity or OS credential")
	}
}

func TestApplicationRejectsUnsupportedOrAmbiguousStateWithoutWriting(t *testing.T) {
	seed := bytes.Repeat([]byte{18}, 32)
	legacy, _ := json.Marshal(struct {
		Version int    `json:"version"`
		Seed    []byte `json:"identitySeed"`
	}{1, seed})
	cases := [][]byte{
		[]byte(`{"version":99}`),
		append(bytes.Clone(legacy[:len(legacy)-1]), []byte(`,"extra":true}`)...),
		append([]byte(`{"version":1,`), legacy[1:]...),
		append(bytes.Clone(legacy), []byte(` {}`)...),
		[]byte(`{"version":1,"identitySeed":"AAAA"}`),
		[]byte(`{"version":2,"identity":{"version":1,"seed":null},"contacts":[]}`),
	}
	for index, payload := range cases {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			v := openTest(t, dir, keys)
			if err := v.Save(payload); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
			if _, err := OpenApplication(v); !errors.Is(err, ErrState) {
				t.Fatal("unsupported state accepted")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
			if !bytes.Equal(before, after) {
				t.Fatal("invalid state replaced")
			}
		})
	}
}

func TestApplicationFailedCommitPoisonsAllScopesUntilReopen(t *testing.T) {
	for _, writeFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[writeFirst], func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			v, a := openApplicationTest(t, dir, keys)
			pin := pinFor(t, a, 19)
			if err := a.PinApprovedContact(pin); err != nil {
				t.Fatal(err)
			}
			store, _ := a.ChatStore(pin)
			before := queuedChat(t, pin, "synthetic durable queue before root failure")
			if err := writeChatFixtureTest(store, before); err != nil {
				t.Fatal(err)
			}
			a.save = func(payload []byte) error {
				if writeFirst {
					if err := v.saveLocked(payload); err != nil {
						return err
					}
				}
				return errors.New("synthetic uncertain commit")
			}
			if _, err := a.SetProfile("synthetic uncertain root metadata", false); !errors.Is(err, ErrStateFailed) {
				t.Fatal("root save failure hidden")
			}
			if a.state.Profile.DisplayName != "" {
				t.Fatal("failed root save published speculative memory")
			}
			if _, err := a.Identity(); !errors.Is(err, ErrStateFailed) {
				t.Fatal("failed envelope still usable")
			}
			if err := writeChatFixtureTest(store, before); !errors.Is(err, ErrStateFailed) {
				t.Fatal("scope wrote after uncertain commit")
			}
			if _, err := loadChatFixtureTest(store); !errors.Is(err, ErrStateFailed) {
				t.Fatal("scope read after uncertain commit")
			}
			if _, err := OpenApplication(v); !errors.Is(err, ErrStateFailed) {
				t.Fatal("reopened without closing uncertain vault")
			}
			_ = a.Close()
			_, a = openApplicationTest(t, dir, keys)
			store, _ = a.ChatStore(pin)
			got, _ := loadChatFixtureTest(store)
			if !bytes.Equal(got, before) {
				t.Fatal("root failure changed the independent committed message records")
			}
			expectedName := ""
			if writeFirst {
				expectedName = "synthetic uncertain root metadata"
			}
			if a.state.Profile.DisplayName != expectedName {
				t.Fatal("reopen did not reconcile the uncertain root replacement")
			}
		})
	}
}

func TestApplicationConcurrentScopesPreserveEverySection(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, a := openApplicationTest(t, dir, keys)
	const workers = 12
	pins := make([]*approvedPin, workers)
	for index := range pins {
		pins[index] = pinFor(t, a, byte(index+30))
	}
	var group sync.WaitGroup
	errorsFound := make(chan error, workers)
	for _, pin := range pins {
		group.Go(func() {
			if err := a.PinApprovedContact(pin); err != nil {
				errorsFound <- err
				return
			}
			store, err := a.ChatStore(pin)
			if err != nil {
				errorsFound <- err
				return
			}
			for index := range 4 {
				id := fmt.Sprintf("%021dA", index)
				if err := store.Queue(queuedMessageTest(id, fmt.Sprintf("synthetic transaction %d", index))); err != nil {
					errorsFound <- err
					return
				}
			}
		})
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	_ = a.Close()
	_, a = openApplicationTest(t, dir, keys)
	for _, pin := range pins {
		store, err := a.ChatStore(pin)
		if err != nil {
			t.Fatal("concurrent pin lost")
		}
		if got, err := store.Page(0, 100); err != nil || len(got) != 4 {
			t.Fatal("concurrent contact transactions lost a record")
		}
	}
}

func TestApplicationRejectsUnapprovedWrongLocalAndOversizedSections(t *testing.T) {
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	var nilPin *approvedPin
	if err := a.PinApprovedContact(nilPin); !errors.Is(err, ErrContact) {
		t.Fatal("typed nil approval accepted")
	}
	pin := pinFor(t, a, 20)
	pin.revoked = true
	if err := a.PinApprovedContact(pin); !errors.Is(err, ErrContact) {
		t.Fatal("revoked approval accepted")
	}
	pin.revoked = false
	wrong := approvedPin{local: pin.peer, peer: pin.peer}
	if err := a.PinApprovedContact(&wrong); !errors.Is(err, ErrContact) {
		t.Fatal("different local identity accepted")
	}
	if err := a.PinApprovedContact(pin); err != nil {
		t.Fatal(err)
	}
	store, _ := a.ChatStore(pin)
	pin.revoked = true
	if _, err := a.ChatStore(pin); !errors.Is(err, ErrContact) {
		t.Fatal("revoked pin reopened history")
	}
	invalid := queuedMessageTest("AAAAAAAAAAAAAAAAAAAAAA", string(make([]byte, 16*1024+1)))
	if err := store.Queue(invalid); !errors.Is(err, message.ErrCorrupt) {
		t.Fatal("oversized message record accepted")
	}
}

func TestApplicationScopedTransactionsPreserveOtherHandlesAndClaimOnce(t *testing.T) {
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	pin := pinFor(t, a, 21)
	if err := a.PinApprovedContact(pin); err != nil {
		t.Fatal(err)
	}
	first, _ := a.ChatStore(pin)
	stale, _ := a.ChatStore(pin)
	if _, err := loadChatFixtureTest(stale); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	newer := queuedMessageTest("AAAAAAAAAAAAAAAAAAAAAA", "synthetic first transaction")
	if err := first.Queue(newer); err != nil {
		t.Fatal(err)
	}
	second := queuedMessageTest("AQEBAQEBAQEBAQEBAQEBAQ", "synthetic second transaction")
	if err := stale.Queue(second); err != nil {
		t.Fatal("independent record writer could not serialize after the first")
	}
	if _, err := loadChatFixtureTest(stale); !errors.Is(err, ErrScopeUsed) {
		t.Fatal("same store checked out twice")
	}
	fresh, _ := a.ChatStore(pin)
	got, err := fresh.Page(0, 100)
	if err != nil || len(got) != 2 || got[0].Text != newer.Text || got[1].Text != second.Text {
		t.Fatal("transactional writers lost another handle's record")
	}
	if _, err := a.Identity(); err != nil {
		t.Fatal("known conflict poisoned unaffected identity")
	}
}

func TestApplicationBoundsContactsAndRejectsUnknownEnvelopeSections(t *testing.T) {
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	for index := range MaxContacts {
		if err := a.PinApprovedContact(pinFor(t, a, byte(index+50))); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.PinApprovedContact(pinFor(t, a, 90)); !errors.Is(err, ErrCapacity) {
		t.Fatal("unbounded contact metadata")
	}
	if len(a.state.Contacts) != MaxContacts {
		t.Fatal("failed pin changed in-memory state")
	}
	valid, err := json.Marshal(a.state)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(bytes.Clone(valid[:len(valid)-1]), []byte(`,"preferences":{}}`)...)
	wrongIdentityVersion := bytes.Replace(valid, []byte(`"identity":{"version":1`), []byte(`"identity":{"version":99`), 1)
	wrongContactVersion := bytes.Replace(valid, []byte(`"contacts":[{"version":1`), []byte(`"contacts":[{"version":99`), 1)
	for index, payload := range [][]byte{unknown, wrongIdentityVersion, wrongContactVersion} {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			dir := privateDirectory(t)
			vault := openTest(t, dir, &memoryKeys{})
			if err := vault.Save(payload); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
			if _, err := OpenApplication(vault); !errors.Is(err, ErrState) {
				t.Fatal("unknown section/version accepted")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
			if !bytes.Equal(before, after) {
				t.Fatal("unsupported envelope rewritten")
			}
		})
	}
}
