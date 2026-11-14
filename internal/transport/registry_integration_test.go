package transport_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"whatsup/internal/messaging"
	"whatsup/internal/pairflow"
	"whatsup/internal/pairing"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type multiMessagingPeer struct {
	*persistentMessagingPeer
	registry *messaging.Registry
	flow     *pairflow.Coordinator
}

func newMultiMessagingPeer(t *testing.T, profile *transport.RelayProfile) *multiMessagingPeer {
	t.Helper()
	p := &multiMessagingPeer{persistentMessagingPeer: newPersistentMessagingPeer(t)}
	var err error
	p.registry, err = messaging.NewRegistry(p.local, p.app, messaging.RegistryOptions{Factories: messaging.Factories{Profile: func(context.Context, string) (*transport.RelayProfile, error) { return profile, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	p.flow, err = pairflow.New(p.local, p.app, pairflow.Options{CompleteActivation: true, OnCompleted: func(id string) { _, _ = p.registry.Connect(id) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.registry.Close(); _ = p.flow.Close() })
	return p
}

// The same pairing coordinators are reused for multiple peers. All transport
// engines and TLS sessions are real; the DERP service is disposable loopback.
func pairMultiMessagingPeers(t *testing.T, ctx context.Context, profile *transport.RelayProfile, owner, joiner *multiMessagingPeer) string {
	t.Helper()
	listener, err := transport.NewInvitationListener(ctx, transport.InvitationOptions{Profile: profile, Policy: transport.RelayFallback, AllowRelayFallback: true})
	if err != nil {
		t.Fatal("could not construct multi-peer invitation transport")
	}
	defer listener.Close()
	manager, err := pairing.NewManager(owner.local.PublicKey(), pairing.Options{CloseInvitation: listener.Close})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	endpoint, err := listener.AddrEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	invite, err := manager.Create(string(endpoint.EncodeForTransport()), 0)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		raw, err := listener.Accept(ctx)
		if err == nil {
			err = owner.flow.Accept(ctx, raw, invite, manager, profile)
		}
		results <- err
	}()
	raw, err := transport.DialInvitation(ctx, invite.Endpoint(), profile, transport.RelayFallback, true)
	if err != nil {
		t.Fatal("could not dial multi-peer invitation")
	}
	go func() { results <- joiner.flow.Join(ctx, raw, invite, profile) }()
	fixture := &liveIntegrationPair{ctx: ctx}
	left := waitIntegrationApproval(t, fixture, owner.flow)
	right := waitIntegrationApproval(t, fixture, joiner.flow)
	if _, err := owner.flow.Approve(left.RequestID, left.PeerFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.flow.Approve(right.RequestID, right.PeerFingerprint); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal("multi-peer reciprocal pairing failed")
			}
		case <-ctx.Done():
			t.Fatal("multi-peer pairing timed out")
		}
	}
	records, err := owner.app.CommittedContacts()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Peer.Equal(joiner.local.PublicKey()) {
			if _, err := owner.app.LocalConversation(record); err != nil {
				t.Fatal("pairing omitted local approval")
			}
			return record.ID
		}
	}
	t.Fatal("pairing did not retain exact peer association")
	return ""
}

func waitMultiMessaging(t *testing.T, ctx context.Context, p *multiMessagingPeer, id string, predicate func(messaging.Snapshot) bool) messaging.Snapshot {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		snapshot := p.registry.Snapshot(id)
		if predicate(snapshot) {
			return snapshot
		}
		select {
		case <-ctx.Done():
			t.Fatalf("real multi-contact session stalled in %s", snapshot.State)
		case <-tick.C:
		}
	}
}

func TestThreeIdentitiesOverRealTailcatMaintainIndependentPairsAndForget(t *testing.T) {
	profile := transport.ExportLoopbackRelayProfileForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	a, b, c := newMultiMessagingPeer(t, profile), newMultiMessagingPeer(t, profile), newMultiMessagingPeer(t, profile)
	ab := pairMultiMessagingPeers(t, ctx, profile, a, b)
	ac := pairMultiMessagingPeers(t, ctx, profile, a, c)
	bc := pairMultiMessagingPeers(t, ctx, profile, b, c)
	for _, item := range []struct {
		p  *multiMessagingPeer
		id string
	}{{a, ab}, {b, ab}, {a, ac}, {c, ac}, {b, bc}, {c, bc}} {
		waitMultiMessaging(t, ctx, item.p, item.id, func(s messaging.Snapshot) bool { return s.State == "connected" })
	}
	for _, p := range []*multiMessagingPeer{a, b, c} {
		contacts, err := p.registry.Contacts()
		if err != nil || len(contacts) != 2 {
			t.Fatal("three-peer graph lost a contact")
		}
		records, _ := p.app.CommittedContacts()
		if records[0].PresharedKey == records[1].PresharedKey || records[0].LocalNode == records[1].LocalNode {
			t.Fatal("independent contacts shared transport credentials")
		}
	}
	for _, item := range []struct {
		from, to *multiMessagingPeer
		id, text string
	}{{a, b, ab, "synthetic real A-B"}, {b, a, ab, "synthetic real B-A"}, {a, c, ac, "synthetic real A-C"}, {c, a, ac, "synthetic real C-A"}, {b, c, bc, "synthetic real B-C"}, {c, b, bc, "synthetic real C-B"}} {
		if _, err := item.from.registry.Send(item.id, item.text); err != nil {
			t.Fatal("multi-contact send failed")
		}
		waitMultiMessaging(t, ctx, item.to, item.id, func(s messaging.Snapshot) bool { _, found := messagingMessage(s, item.text); return found })
		waitMultiMessaging(t, ctx, item.from, item.id, func(s messaging.Snapshot) bool {
			message, found := messagingMessage(s, item.text)
			return found && message.Delivered
		})
	}
	// Restart the pairing coordinator while contacts remain active. Completed
	// contacts must not become a blocking discardable pairing request.
	if err := a.flow.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	a.flow, err = pairflow.New(a.local, a.app, pairflow.Options{CompleteActivation: true, OnCompleted: func(id string) { _, _ = a.registry.Connect(id) }})
	if err != nil || a.flow.Snapshot().State != "idle" {
		t.Fatal("approved contacts blocked restored pairing coordinator")
	}
	records, _ := a.app.CommittedContacts()
	var record storage.PendingRecord
	for _, candidate := range records {
		if candidate.ID == ab {
			record = candidate
		}
	}
	if err := a.app.ForgetCommittedContact(record, func() error { return a.registry.Retire(ab) }); err != nil {
		t.Fatal("exact contact could not be forgotten")
	}
	if _, err := a.registry.Connect(ab); !errors.Is(err, messaging.ErrUnavailable) {
		t.Fatal("late completion revived forgotten runtime")
	}
	contacts, err := a.registry.Contacts()
	if err != nil || len(contacts) != 1 || contacts[0].ContactID != ac {
		t.Fatal("Forget removed independent contact")
	}
	if _, err := a.registry.Send(ac, "synthetic surviving A-C"); err != nil {
		t.Fatal("Forget stopped surviving connection")
	}
	waitMultiMessaging(t, ctx, c, ac, func(s messaging.Snapshot) bool {
		_, found := messagingMessage(s, "synthetic surviving A-C")
		return found
	})
	if b.registry.Snapshot(bc).State != "connected" || c.registry.Snapshot(bc).State != "connected" {
		t.Fatal("Forget disturbed unrelated B-C session")
	}
}
