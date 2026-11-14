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
	payload, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return payload
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
	if _, err := store.Load(); !os.IsNotExist(err) {
		t.Fatal("new contact obtained existing history")
	}
	payload := queuedChat(t, pin, "synthetic scoped content")
	if err := store.Save(payload); err != nil {
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
	if err := store.Save([]byte(`{"version":1,"identitySeed":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)); !errors.Is(err, ErrState) {
		t.Fatal("chat accepted identity schema")
	}
	other := pinFor(t, a, 13)
	if err := store.Save(emptyChat(t, other)); !errors.Is(err, ErrState) {
		t.Fatal("chat accepted another peer pin")
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
	if _, err := store.Load(); !errors.Is(err, ErrLocked) {
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
	loaded, _ = store.Load()
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
			before := emptyChat(t, pin)
			if err := store.Save(before); err != nil {
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
			after := queuedChat(t, pin, "synthetic uncertain content")
			if err := store.Save(after); !errors.Is(err, ErrStateFailed) {
				t.Fatal("save failure hidden")
			}
			if !bytes.Equal(a.state.Contacts[0].Chat, before) {
				t.Fatal("failed save published speculative memory")
			}
			if _, err := a.Identity(); !errors.Is(err, ErrStateFailed) {
				t.Fatal("failed envelope still usable")
			}
			if err := store.Save(before); !errors.Is(err, ErrStateFailed) {
				t.Fatal("scope wrote after uncertain commit")
			}
			if _, err := store.Load(); !errors.Is(err, ErrStateFailed) {
				t.Fatal("scope read after uncertain commit")
			}
			if _, err := OpenApplication(v); !errors.Is(err, ErrStateFailed) {
				t.Fatal("reopened without closing uncertain vault")
			}
			_ = a.Close()
			_, a = openApplicationTest(t, dir, keys)
			store, _ = a.ChatStore(pin)
			got, _ := store.Load()
			expected := before
			if writeFirst {
				expected = after
			}
			if !bytes.Equal(got, expected) {
				t.Fatal("reopen did not reconcile durable state")
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
			payload, _ := json.Marshal(storedChat{Version: 1, Local: pin.local, Peer: pin.peer})
			for range 4 {
				if err := store.Save(payload); err != nil {
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
		if got, err := store.Load(); err != nil || !bytes.Equal(got, emptyChat(t, pin)) {
			t.Fatal("concurrent section lost")
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
	if err := store.Save(make([]byte, maxChatBytes+1)); !errors.Is(err, ErrState) {
		t.Fatal("oversized chat accepted")
	}
	unknown := emptyChat(t, pin)
	unknown = append(unknown[:len(unknown)-1], []byte(`,"unknown":true}`)...)
	if err := store.Save(unknown); !errors.Is(err, ErrState) {
		t.Fatal("unknown chat schema accepted")
	}
	pin.revoked = true
	if _, err := a.ChatStore(pin); !errors.Is(err, ErrContact) {
		t.Fatal("revoked pin reopened history")
	}
}

func TestApplicationRejectsStaleScopedWriterWithoutLosingNewChat(t *testing.T) {
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	pin := pinFor(t, a, 21)
	if err := a.PinApprovedContact(pin); err != nil {
		t.Fatal(err)
	}
	first, _ := a.ChatStore(pin)
	stale, _ := a.ChatStore(pin)
	if _, err := stale.Load(); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	newer := queuedChat(t, pin, "synthetic newer content")
	if err := first.Save(newer); err != nil {
		t.Fatal(err)
	}
	if err := stale.Save(emptyChat(t, pin)); !errors.Is(err, ErrStateConflict) {
		t.Fatal("stale writer erased newer chat")
	}
	if _, err := stale.Load(); !errors.Is(err, ErrScopeUsed) {
		t.Fatal("same store checked out twice")
	}
	fresh, _ := a.ChatStore(pin)
	got, err := fresh.Load()
	if err != nil || !bytes.Equal(got, newer) {
		t.Fatal("conflict changed durable chat")
	}
	if err := fresh.Save(got); err != nil {
		t.Fatal("freshly loaded writer could not reconcile")
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
