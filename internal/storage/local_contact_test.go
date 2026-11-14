package storage

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
)

type identifiedPin struct {
	*approvedPin
	id string
}

func (p *identifiedPin) ContactID() string { return p.id }

func ongoingRecord(t *testing.T, a *ApplicationState, fill byte) PendingRecord {
	t.Helper()
	r := pendingFor(t, a, fill)
	r.Version = 2
	r.RelayServiceURL = "https://relay.example/map.json"
	r.RelayFallbackConsent = true
	var private key.NodePrivate
	if err := private.UnmarshalText([]byte(r.LocalNodePrivate)); err != nil {
		t.Fatal(err)
	}
	psk, _ := base64.RawURLEncoding.DecodeString(r.PresharedKey)
	defer clear(psk)
	ci := tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: private.Public()}, ServerDiscoPublic: tailcat.DiscoPublicForNode(private), RegionID: 900}
	copy(ci.PresharedKey[:], psk)
	r.ServerEndpoint = string(ci.Addr())
	if ValidatePendingRecord(r) != nil {
		t.Fatal("invalid disposable ongoing record")
	}
	return r
}

func commitOngoing(t *testing.T, a *ApplicationState, fill byte) (PendingRecord, *identifiedPin) {
	t.Helper()
	r := ongoingRecord(t, a, fill)
	if err := a.SavePendingContact(r); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CommitPendingContact(r); err != nil {
		t.Fatal(err)
	}
	return r, &identifiedPin{approvedPin: &approvedPin{local: r.Local, peer: r.Peer}, id: r.ID}
}

func TestOngoingRecordPinsExactServiceEndpointKeysAndConsent(t *testing.T) {
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	valid := ongoingRecord(t, a, 171)
	changes := map[string]func(*PendingRecord){
		"legacy-with-route":  func(r *PendingRecord) { r.Version = 1 },
		"no-consent":         func(r *PendingRecord) { r.RelayFallbackConsent = false },
		"http":               func(r *PendingRecord) { r.RelayServiceURL = "http://relay.example/map.json" },
		"private-url":        func(r *PendingRecord) { r.RelayServiceURL = "https://127.0.0.1/map" },
		"credential-url":     func(r *PendingRecord) { r.RelayServiceURL = "https://user:secret@relay.example/map" },
		"query-url":          func(r *PendingRecord) { r.RelayServiceURL += "?relay=other" },
		"missing-endpoint":   func(r *PendingRecord) { r.ServerEndpoint = "" },
		"different-endpoint": func(r *PendingRecord) { r.ServerEndpoint = ongoingRecord(t, a, 172).ServerEndpoint },
		"foreign-discovery": func(r *PendingRecord) {
			ci, _ := tailcat.ParseAddr(tailcat.Addr(r.ServerEndpoint))
			ci.ServerDiscoPublic = tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()}
			r.ServerEndpoint = string(ci.Addr())
		},
		"different-psk": func(r *PendingRecord) { r.PresharedKey = ongoingRecord(t, a, 173).PresharedKey },
		"invalid-region": func(r *PendingRecord) {
			ci, _ := tailcat.ParseAddr(tailcat.Addr(r.ServerEndpoint))
			ci.RegionID = -1
			r.ServerEndpoint = string(ci.Addr())
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			r := valid
			change(&r)
			if ValidatePendingRecord(r) == nil {
				t.Fatal("substituted ongoing association accepted")
			}
			if a.SavePendingContact(r) == nil {
				t.Fatal("invalid association persisted")
			}
		})
	}
	for _, raw := range []string{"https://relay.example:0/map", "https://relay.example:0443/map", "https://relay.example/map#fragment", "https://localhost/map", "https://[fd00::1]/map", "https://relay.example/map\n"} {
		if ValidateRelayServiceURL(raw) == nil {
			t.Fatal("unsafe service selection accepted")
		}
	}
	legacy := pendingFor(t, a, 174)
	if _, err := ContactBinding(legacy); err == nil {
		t.Fatal("legacy record gained ongoing binding")
	}
}

func TestContactBindingIsReciprocalAndBindsTheCompletePolicy(t *testing.T) {
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	owner := ongoingRecord(t, a, 175)
	client := key.NewNode()
	encoded, _ := client.MarshalText()
	defer clear(encoded)
	owner.PeerNode = client.Public().String()
	joiner := owner
	joiner.Role = "joiner"
	joiner.Local, joiner.Peer = owner.Peer, owner.Local
	joiner.LocalNodePrivate = string(encoded)
	joiner.LocalNode, joiner.PeerNode = owner.PeerNode, owner.LocalNode
	left, err := ContactBinding(owner)
	right, otherErr := ContactBinding(joiner)
	if err != nil || otherErr != nil || left == [32]byte{} || left != right {
		t.Fatal("reciprocal records disagreed on contact binding")
	}
	for _, mutate := range []func(*PendingRecord){func(r *PendingRecord) { r.RelayServiceURL = "https://another.example/map.json" }, func(r *PendingRecord) {
		ci, _ := tailcat.ParseAddr(tailcat.Addr(r.ServerEndpoint))
		ci.RegionID = 901
		r.ServerEndpoint = string(ci.Addr())
	}, func(r *PendingRecord) { r.ID = pendingFor(t, a, 176).ID }} {
		changed := owner
		mutate(&changed)
		value, err := ContactBinding(changed)
		if err != nil || value == left {
			t.Fatal("changed policy was omitted from binding")
		}
	}
}

func TestLocalConversationRequiresDurableContactPinAndRestoresOffline(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, a := openApplicationTest(t, directory, keys)
	r, pin := commitOngoing(t, a, 177)
	if _, err := a.LocalConversation(r); err == nil {
		t.Fatal("unfinished committed setup granted local history")
	}
	if err := a.ApproveCommittedConversation(r, pin); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
	if err := a.ApproveCommittedConversation(r, pin); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
	if !bytes.Equal(before, after) {
		t.Fatal("idempotent approval rewrote vault")
	}
	if _, err := a.ChatStore(pin.approvedPin); err == nil {
		t.Fatal("legacy identity-only pin read contact-ID history")
	}
	wrong := &identifiedPin{approvedPin: pin.approvedPin, id: pendingFor(t, a, 178).ID}
	if _, err := a.ChatStore(wrong); err == nil {
		t.Fatal("sibling contact ID read another history")
	}
	store, err := a.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	payload := queuedChat(t, pin.approvedPin, "synthetic offline durable queue")
	if err := writeChatFixtureTest(store, payload); err != nil {
		t.Fatal(err)
	}
	receipt, err := a.LocalConversation(r)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Local() != r.Local || receipt.Peer() != r.Peer || receipt.ContactID() != r.ID || receipt.WithLocalApproval(func() error { return nil }) != nil {
		t.Fatal("local receipt lost exact approval")
	}
	for _, v := range []any{receipt, *receipt, LocalContactReceipt{}} {
		if _, err := json.Marshal(v); err == nil {
			t.Fatal("local approval serialized")
		}
		if fmt.Sprintf("%#v", v) != "[private WhatsUp local conversation receipt]" {
			t.Fatal("local approval formatting leaked fields")
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if receipt.WithLocalApproval(func() error { t.Error("closed approval was admitted"); return nil }) == nil {
		t.Fatal("closed vault retained local authority")
	}
	_, a = openApplicationTest(t, directory, keys)
	restored, err := a.LocalConversation(r)
	if err != nil || restored.WithLocalApproval(func() error { return nil }) != nil {
		t.Fatal("durable local approval could not restore offline")
	}
	store, err = a.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadChatFixtureTest(store)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("offline reopen lost durable queue")
	}
	if _, err := a.CommitPendingContact(r); !errors.Is(err, ErrCommittedRestore) {
		t.Fatal("local approval recreated live pairing receipt")
	}
	if err := a.RemoveCommittedContact(r.ID); err != nil {
		t.Fatal(err)
	}
	if restored.WithLocalApproval(func() error { return nil }) == nil {
		t.Fatal("removed contact retained stale local approval")
	}
	if _, err := loadChatFixtureTest(store); err == nil {
		t.Fatal("removed contact retained chat handle")
	}
}

func TestLocalConversationApprovalRejectsWrongIDsLegacyHistoryAndUncertainWrites(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
			r, pin := commitOngoing(t, a, 179)
			wrong := *pin
			wrong.id = pendingFor(t, a, 180).ID
			if a.ApproveCommittedConversation(r, &wrong) == nil || a.ApproveCommittedConversation(r, pin.approvedPin) == nil {
				t.Fatal("unscoped approval created history pin")
			}
			if uncertain {
				a.save = func([]byte) error { return errors.New("synthetic uncertain local approval") }
				if !errors.Is(a.ApproveCommittedConversation(r, pin), ErrStateFailed) {
					t.Fatal("uncertain approval was accepted")
				}
				if _, err := a.LocalConversation(r); err == nil {
					t.Fatal("uncertain approval granted local access")
				}
				return
			}
			if err := a.PinApprovedContact(pin.approvedPin); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(a.ApproveCommittedConversation(r, pin), ErrCommittedConflict) {
				t.Fatal("new contact inherited legacy approval/history")
			}
		})
	}
}

func TestSchemaFourMigrationPreservesLegacyCredentialsWithoutInventingPolicy(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, a := openApplicationTest(t, directory, keys)
	r, _ := commitForTest(t, a, 181)
	local, _ := a.Identity()
	seed := local.Seed()
	old := legacySnapshotForTest(a.state)
	encoded, _ := json.Marshal(old)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	vault := legacyVaultForTest(t, directory, keys)
	if err := vault.Save(encoded); err != nil {
		t.Fatal(err)
	}
	a, err := OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := a.CommittedContacts()
	restored, _ := a.Identity()
	if a.state.Version != ApplicationSchemaVersion || len(got) != 1 || got[0] != r || restored.Seed() != seed || keys.writes != 1 {
		t.Fatal("schema four migration changed identity or credentials")
	}
	if _, err := a.LocalConversation(got[0]); err == nil {
		t.Fatal("legacy committed record acquired local history authority")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	// A version-2 policy cannot be placed under a schema-4 label.
	_, modern := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	fresh := ongoingRecord(t, modern, 182)
	modern.state.CommittedContacts = []pendingRecordDisk{pendingRecordDisk(fresh)}
	malformed := modern.state
	malformed.Version = 4
	bad, _ := json.Marshal(malformed)
	other := openTest(t, privateDirectory(t), &memoryKeys{})
	if err := other.Save(bad); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenApplication(other); !errors.Is(err, ErrState) {
		t.Fatal("schema four accepted new routing fields")
	}
}
