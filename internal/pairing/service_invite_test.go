package pairing

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"whatsup/internal/transport/address"
)

func TestServiceInvitationCanonicalContextBindsEveryPublicField(t *testing.T) {
	manager, err := NewManager(syntheticKey(t, 1), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	invite, err := manager.CreateWithService(syntheticEndpoint(), "https://relay.example:8443/map.json", 0)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseInvite(invite.Link())
	if err != nil || parsed.Link() != invite.Link() || parsed.ContextBinding() == [32]byte{} || parsed.ContextBinding() != invite.ContextBinding() || parsed.ServiceURL() != invite.ServiceURL() || !strings.HasPrefix(invite.Link(), "whatsup://pair/v2?") {
		t.Fatal("v2 invitation lost its exact service/context")
	}
	for _, change := range []func(*Invite){
		func(i *Invite) { i.id[0] ^= 1 },
		func(i *Invite) { i.owner = syntheticKey(t, 3) },
		func(i *Invite) { i.endpoint, _ = address.Parse(syntheticEndpoint()) },
		func(i *Invite) { i.expires = i.expires.Add(time.Second) },
		func(i *Invite) { i.service += "/changed" },
	} {
		modified := parsed
		change(&modified)
		if modified.ContextBinding() == parsed.ContextBinding() {
			t.Fatal("public invitation field was absent from authentication context")
		}
	}
	modified := parsed
	modified.service = "https://different.example/map.json"
	if _, err := manager.Reserve(modified.Link(), syntheticKey(t, 2)); !errors.Is(err, ErrInvalidInvite) || manager.Snapshot().State != Created {
		t.Fatal("changed service redeemed or burned the original invitation")
	}
	u, _ := url.Parse(invite.Link())
	q := u.Query()
	q.Del("service")
	u.RawQuery = q.Encode()
	if _, err := ParseInvite(u.String()); err == nil {
		t.Fatal("v2 invitation without service was accepted")
	}
	u.Path = "/v1"
	legacy, err := ParseInvite(u.String())
	if err != nil || legacy.ContextBinding() != [32]byte{} {
		t.Fatal("legacy domain invitation did not use explicit legacy context")
	}
}

func TestServiceInvitationRejectsUnsafeURLShapes(t *testing.T) {
	for _, service := range []string{"", "http://relay.example/map", "https://name:secret@relay.example/map", "https://relay.example/map?secret=x", "https://relay.example/map#fragment", "https://relay.example:0/map", "https://relay.example:65536/map", "https://relay.example:0443/map", "https://relay.example/\nmap", "https://rélay.example/map"} {
		manager, err := NewManager(syntheticKey(t, 1), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.CreateWithService(syntheticEndpoint(), service, 0); err == nil {
			t.Fatal("unsafe service URL created an invitation")
		}
		_ = manager.Close()
	}
}

func TestV2InvitationCannotReserveAnOmittedContextLegacySession(t *testing.T) {
	owner, candidate := authenticatedIdentities(t)
	manager, err := NewManager(owner.PublicKey(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	invite, err := manager.CreateWithService(syntheticEndpoint(), "https://relay.example/map.json", 0)
	if err != nil {
		t.Fatal(err)
	}
	legacy := invite
	legacy.service = ""
	server, _ := authenticatedPair(t, owner, candidate, legacy)
	if _, err := manager.ReserveAuthenticated(invite.Link(), server); !errors.Is(err, ErrChannel) || manager.Snapshot().State != Created {
		t.Fatal("v2 invitation fell back to legacy session authentication")
	}
}

func TestV2AuthenticatedReservationRequiresExactCarriedService(t *testing.T) {
	owner, candidate := authenticatedIdentities(t)
	manager, err := NewManager(owner.PublicKey(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	invite, err := manager.CreateWithService(syntheticEndpoint(), "https://relay.example/map.json", 0)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := authenticatedPair(t, owner, candidate, invite)
	changed := invite
	changed.service = "https://different.example/map.json"
	if _, err := manager.ReserveAuthenticated(changed.Link(), server); !errors.Is(err, ErrChannel) {
		t.Fatal("changed service reused an authenticated v2 session")
	}
	if _, err := manager.ReserveAuthenticated(invite.Link(), server); err != nil || manager.Snapshot().State != Pending {
		t.Fatal("exact authenticated service invitation could not reserve")
	}
}
