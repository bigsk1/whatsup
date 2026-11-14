package messaging

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"whatsup/internal/identity"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type registryPeer struct {
	local     *identity.Identity
	app       *storage.ApplicationState
	registry  *Registry
	revisions *Revisions
	directory string
	keys      *lifecycleKeys
}

func newRegistryPeer(t *testing.T) *registryPeer {
	t.Helper()
	directory, keys := filepath.Join(t.TempDir(), "private"), &lifecycleKeys{}
	vault, err := storage.Open(directory, keys)
	if err != nil {
		t.Fatal(err)
	}
	app, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	local, err := app.Identity()
	if err != nil {
		t.Fatal(err)
	}
	p := &registryPeer{local: local, app: app, revisions: &Revisions{}, directory: directory, keys: keys}
	t.Cleanup(func() {
		if p.registry != nil {
			p.registry.Close()
		}
		if err := p.app.Close(); err != nil {
			t.Error(err)
		}
	})
	return p
}

// This fixture exercises actual pinned TLS, message persistence and ACKs over
// net.Pipe. It deliberately does not claim Tailcat/DERP path coverage.
func seedRegistryAssociation(t *testing.T, owner, joiner *registryPeer) string {
	t.Helper()
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	server := tailcat.NewPrivateKey()
	server.Public.RegionID = 900
	client := key.NewNode()
	serverPrivate, _ := server.Private.MarshalText()
	clientPrivate, _ := client.MarshalText()
	defer clear(serverPrivate)
	defer clear(clientPrivate)
	left := storage.PendingRecord{Version: 2, ID: base64.RawURLEncoding.EncodeToString(token[:]), Role: "inviter", Local: owner.local.PublicKey(), Peer: joiner.local.PublicKey(), LocalNodePrivate: string(serverPrivate), LocalNode: server.Private.Public().String(), PeerNode: client.Public().String(), PresharedKey: base64.RawURLEncoding.EncodeToString(server.Public.PresharedKey[:]), RelayServiceURL: "https://relay.example/map.json", RelayFallbackConsent: true, ServerEndpoint: string(server.Public.Addr())}
	right := left
	right.Role = "joiner"
	right.Local, right.Peer = left.Peer, left.Local
	right.LocalNodePrivate, right.LocalNode, right.PeerNode = string(clientPrivate), left.PeerNode, left.LocalNode
	for _, item := range []struct {
		peer   *registryPeer
		record storage.PendingRecord
	}{{owner, left}, {joiner, right}} {
		if err := item.peer.app.SavePendingContact(item.record); err != nil {
			t.Fatal(err)
		}
		if _, err := item.peer.app.CommitPendingContact(item.record); err != nil {
			t.Fatal(err)
		}
	}
	return left.ID
}

type registryPipeListener struct {
	incoming chan net.Conn
	closed   chan struct{}
	once     sync.Once
}

func (l *registryPipeListener) Accept(ctx context.Context) (net.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.closed:
		return nil, net.ErrClosed
	case raw := <-l.incoming:
		return raw, nil
	}
}
func (l *registryPipeListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }

type registryNetwork struct {
	mu        sync.Mutex
	listeners map[string]*registryPipeListener
	calls     map[string]int
}

func (n *registryNetwork) factories() Factories {
	return Factories{
		Profile: func(context.Context, string) (*transport.RelayProfile, error) { return nil, nil },
		Listen: func(ctx context.Context, record storage.PendingRecord, _ *transport.RelayProfile) (Listener, error) {
			l := &registryPipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
			n.mu.Lock()
			if n.listeners == nil {
				n.listeners = map[string]*registryPipeListener{}
			}
			if n.calls == nil {
				n.calls = map[string]int{}
			}
			n.listeners[record.ID] = l
			n.calls[record.ID]++
			n.mu.Unlock()
			return l, nil
		},
		Dial: func(ctx context.Context, record storage.PendingRecord, _ *transport.RelayProfile) (net.Conn, error) {
			n.mu.Lock()
			l := n.listeners[record.ID]
			n.mu.Unlock()
			if l == nil {
				return nil, ErrUnavailable
			}
			left, right := net.Pipe()
			select {
			case <-ctx.Done():
				left.Close()
				right.Close()
				return nil, ctx.Err()
			case <-l.closed:
				left.Close()
				right.Close()
				return nil, net.ErrClosed
			case l.incoming <- left:
				return right, nil
			}
		},
	}
}

func openRegistry(t *testing.T, p *registryPeer, n *registryNetwork, sink func(Hint)) {
	t.Helper()
	r, err := NewRegistry(p.local, p.app, RegistryOptions{Revisions: p.revisions, Factories: n.factories(), OnChanged: sink})
	if err != nil {
		t.Fatal(err)
	}
	p.registry = r
}

func waitRegistry(t *testing.T, p *registryPeer, id string, predicate func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(3 * time.Millisecond)
	defer tick.Stop()
	for {
		snapshot := p.registry.Snapshot(id)
		if predicate(snapshot) {
			return snapshot
		}
		select {
		case <-deadline.C:
			t.Fatalf("contact state did not satisfy condition: %s", snapshot.State)
		case <-tick.C:
		}
	}
}

func TestRegistryThreeIdentitiesConverseIndependentlyAndForgetOne(t *testing.T) {
	a, b, c := newRegistryPeer(t), newRegistryPeer(t), newRegistryPeer(t)
	ab, ac, bc := seedRegistryAssociation(t, a, b), seedRegistryAssociation(t, a, c), seedRegistryAssociation(t, b, c)
	n := &registryNetwork{}
	for _, p := range []*registryPeer{a, b, c} {
		openRegistry(t, p, n, nil)
		summaries, err := p.registry.Contacts()
		if err != nil || len(summaries) != 2 {
			t.Fatal("registry did not retain both exact contacts")
		}
	}
	for _, item := range []struct {
		p  *registryPeer
		id string
	}{{a, ab}, {a, ac}, {b, ab}, {b, bc}, {c, ac}, {c, bc}} {
		if _, err := item.p.registry.Connect(item.id); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		p  *registryPeer
		id string
	}{{a, ab}, {a, ac}, {b, ab}, {b, bc}, {c, ac}, {c, bc}} {
		waitRegistry(t, item.p, item.id, func(s Snapshot) bool { return s.State == "connected" })
	}
	for _, item := range []struct {
		from, to *registryPeer
		id, text string
	}{{a, b, ab, "synthetic A-B"}, {c, a, ac, "synthetic C-A"}, {b, c, bc, "synthetic B-C"}} {
		if _, err := item.from.registry.Send(item.id, item.text); err != nil {
			t.Fatal(err)
		}
		waitRegistry(t, item.to, item.id, func(s Snapshot) bool { return len(s.Messages) == 1 && s.Messages[0].Text == item.text })
		waitRegistry(t, item.from, item.id, func(s Snapshot) bool { return len(s.Messages) == 1 && s.Messages[0].Delivered })
	}
	contacts, err := a.registry.Contacts()
	if err != nil {
		t.Fatal(err)
	}
	for _, summary := range contacts {
		if summary.ContactID == ac && summary.Unread != 1 {
			t.Fatal("incoming unread count missing")
		}
	}
	page, err := a.registry.Page(ac, "", 100)
	if err != nil || len(page.Messages) != 1 || page.HasMore {
		t.Fatal("scoped page unavailable")
	}
	if _, err := a.registry.MarkRead(ac, page.Messages[0].ID); err != nil {
		t.Fatal(err)
	}
	contacts, _ = a.registry.Contacts()
	for _, summary := range contacts {
		if summary.ContactID == ac && summary.Unread != 0 {
			t.Fatal("MarkRead did not clear accepted page")
		}
	}
	records, _ := a.app.CommittedContacts()
	var forgotten storage.PendingRecord
	for _, record := range records {
		if record.ID == ab {
			forgotten = record
		}
	}
	stale, err := a.registry.manager(ab)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.app.ForgetCommittedContact(forgotten, func() error { return a.registry.Retire(ab) }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.registry.Connect(ab); !errors.Is(err, ErrUnavailable) {
		t.Fatal("late completion revived retired association")
	}
	if _, err := stale.Send(ab, "stale forbidden"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("stale target retained queue authority")
	}
	contacts, err = a.registry.Contacts()
	if err != nil || len(contacts) != 1 || contacts[0].ContactID != ac {
		t.Fatal("Forget removed unrelated contact")
	}
	if _, err := a.registry.Send(ac, "synthetic still connected"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, c, ac, func(s Snapshot) bool { return len(s.Messages) == 2 })
	if b.registry.Snapshot(bc).State != "connected" {
		t.Fatal("Forget changed unrelated third-party session")
	}
}

func TestRegistryRevisionClockSurvivesReplacementAndHintsPermitReentry(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	n := &registryNetwork{}
	hints := make(chan Hint, 64)
	openRegistry(t, a, n, func(h Hint) {
		select {
		case hints <- h:
		default:
		}
	})
	old := a.registry
	oldManager, err := old.manager(id)
	if err != nil {
		t.Fatal(err)
	}
	before := old.Snapshot(id).Revision
	for range 8 {
		old.MetadataChanged(id)
	}
	old.Close()
	final := oldManager.Snapshot().Revision
	if final < before {
		t.Fatal("revision regressed before replacement")
	}
	openRegistry(t, a, n, nil)
	after := a.registry.Snapshot(id).Revision
	if after <= final {
		t.Fatal("new unlock reused an old revision range")
	}
	if _, err := old.Connect(id); !errors.Is(err, ErrUnavailable) {
		t.Fatal("old registry callback reconnected new generation")
	}
	a.registry.Close()
	var current atomic.Pointer[Registry]
	entered := make(chan struct{}, 1)
	r, err := NewRegistry(a.local, a.app, RegistryOptions{Revisions: a.revisions, Factories: n.factories(), OnChanged: func(Hint) {
		if target := current.Load(); target != nil {
			_, _ = target.Contacts()
			select {
			case entered <- struct{}{}:
			default:
			}
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	current.Store(r)
	a.registry = r
	r.MetadataChanged(id)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hint callback ran under runtime guard")
	}
	for _, cursor := range []string{"0", "01", "-1", "9223372036854775808", "1x"} {
		if _, err := r.Page(id, cursor, 100); err == nil {
			t.Fatal("noncanonical cursor accepted")
		}
	}
}
