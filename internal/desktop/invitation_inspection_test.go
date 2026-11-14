package desktop

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"whatsup/internal/pairing"
	"whatsup/internal/transport/address"
)

func inspectionInvite(t *testing.T, owner *Service, service string) pairing.Invite {
	t.Helper()
	manager, err := pairing.NewManager(owner.invitations.local.PublicKey(), pairing.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	listener := disposableListener(t)
	invite, err := manager.CreateWithService(string(listener.endpoint.EncodeForTransport()), service, 0)
	if err != nil {
		t.Fatal(err)
	}
	return invite
}

func TestInspectInvitationIsLocalOnlyAndReturnsOnlyApprovedServiceContext(t *testing.T) {
	owner := unlockedInvitationService(t)
	invite := inspectionInvite(t, owner, "https://relay.example.invalid:8443/map.json")
	locked := New(filepath.Join(t.TempDir(), "private"), &testKeys{})
	value, err := locked.InspectInvitation(invite.Link())
	if err != nil || value.ServiceURL != invite.ServiceURL() || value.Host != "relay.example.invalid:8443" || locked.vault != nil || locked.preferenceState != nil || locked.invitations != nil {
		t.Fatal("inspection touched storage/runtime or lost service context")
	}
	encoded, err := json.Marshal(value)
	if err != nil || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "owner") || strings.Contains(string(encoded), "endpoint") || strings.Contains(string(encoded), "whatsup://") {
		t.Fatal("inspection exposed bearer or transport material")
	}
	var fields map[string]any
	if json.Unmarshal(encoded, &fields) != nil || len(fields) != 2 {
		t.Fatal("inspection returned more than serviceURL and host")
	}
	if _, err := locked.InspectInvitation("malformed"); err == nil {
		t.Fatal("inspection accepted malformed invitation")
	}
}

func TestProductionJoinRejectsLegacyChangedServiceAndMissingConsentBeforeNetwork(t *testing.T) {
	owner, joiner := unlockedInvitationService(t), unlockedInvitationService(t)
	joiner.invitations.legacyFixture = false
	invite := inspectionInvite(t, owner, disposableRelayURL)
	var calls atomic.Int32
	joiner.invitations.dial = func(context.Context, address.Endpoint, string, bool) (net.Conn, error) {
		calls.Add(1)
		return nil, net.ErrClosed
	}
	manager, err := pairing.NewManager(owner.invitations.local.PublicKey(), pairing.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	listener := disposableListener(t)
	legacy, err := manager.Create(string(listener.endpoint.EncodeForTransport()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.JoinInvitation(legacy.Link(), disposableRelayURL, true); err == nil {
		t.Fatal("production bridge accepted fixture-only legacy invitation")
	}
	if _, err := joiner.InspectInvitation(legacy.Link()); err == nil {
		t.Fatal("inspection selected legacy fixture mode")
	}
	if _, err := joiner.JoinInvitation(invite.Link(), "https://different.example/map.json", true); err == nil {
		t.Fatal("changed approved service reached the dial path")
	}
	if _, err := joiner.JoinInvitation(invite.Link(), disposableRelayURL, false); err == nil {
		t.Fatal("inspection replaced explicit relay consent")
	}
	if calls.Load() != 0 || joiner.invitations.job != nil {
		t.Fatal("rejected join performed network work")
	}
}

func TestProductionCreateAlwaysSharesV2ServiceInvitation(t *testing.T) {
	s := unlockedInvitationService(t)
	s.invitations.legacyFixture = false
	listener := disposableListener(t)
	s.invitations.listen = func(context.Context, string, bool) (invitationListener, error) { return listener, nil }
	share, err := s.CreateInvitation(disposableRelayURL, true)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := pairing.ParseInvite(share.Link)
	if err != nil || invite.ServiceURL() != disposableRelayURL || invite.ContextBinding() == [32]byte{} || !strings.HasPrefix(share.Link, "whatsup://pair/v2?") {
		t.Fatal("production create emitted an unbound legacy share")
	}
	if _, err := s.CancelInvitation(); err != nil {
		t.Fatal(err)
	}
}
