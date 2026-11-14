package messaging

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/identity"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type changedPeerListener struct {
	raw   net.Conn
	calls atomic.Int32
}

func (l *changedPeerListener) Accept(ctx context.Context) (net.Conn, error) {
	if l.calls.Add(1) == 1 {
		return l.raw, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (l *changedPeerListener) Close() error { return l.raw.Close() }

func TestChangedPeerIdentityStopsRetriesAndPreservesSavedServiceAndPin(t *testing.T) {
	m, _, profiles := newBlockedManager(t)
	before := m.record
	left, right := net.Pipe()
	listener := &changedPeerListener{raw: left}
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	m.factories.Profile = func(ctx context.Context, service string) (*transport.RelayProfile, error) {
		profiles.Add(1)
		if service != before.RelayServiceURL {
			t.Error("reconnect substituted the saved service")
		}
		return nil, nil
	}
	m.factories.Listen = func(ctx context.Context, record storage.PendingRecord, _ *transport.RelayProfile) (Listener, error) {
		if record != before {
			t.Error("reconnect changed approved credentials")
		}
		return listener, nil
	}
	unknown, _ := identity.Generate()
	binding, err := storage.ContactBinding(before)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	peerResult := make(chan error, 1)
	go func() {
		peer, err := sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: unknown, Peer: m.local.PublicKey(), ContactID: before.ID, Association: binding})
		if peer != nil {
			// TLS 1.3 can complete this client's local handshake before the
			// server rejects its certificate. Require the rejected client to
			// receive no application data; server admission is checked below
			// through the coordinator's terminal identity-changed state.
			_ = peer.SetReadDeadline(time.Now().Add(time.Second))
			var applicationData [1]byte
			n, readErr := peer.Read(applicationData[:])
			if n != 0 || readErr == nil {
				err = nil
			} else {
				err = readErr
			}
			_ = peer.Close()
		}
		peerResult <- err
	}()
	if _, err := m.Connect(before.ID); err != nil {
		t.Fatal("explicit reconnect could not start")
	}
	joined := make(chan struct{})
	go func() { m.workers.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-ctx.Done():
		t.Fatal("changed identity kept automatic retries alive")
	}
	select {
	case err := <-peerResult:
		if err == nil {
			t.Fatal("rejected identity received application data")
		}
	case <-ctx.Done():
		t.Fatal("changed peer authentication did not terminate")
	}
	status := m.Snapshot()
	if status.State != "identity-changed" || status.CanConnect || status.PeerFingerprint != before.Peer.Fingerprint() {
		t.Fatal("identity mismatch did not retain the original approved pin")
	}
	if profiles.Load() != 1 || listener.calls.Load() != 1 {
		t.Fatal("identity mismatch retried or selected another service")
	}
	if _, err := m.Connect(before.ID); err == nil {
		t.Fatal("stopped identity mismatch silently retried")
	}
	stored, err := m.application.CommittedContacts()
	if err != nil || len(stored) != 1 || stored[0] != before || m.record != before {
		t.Fatal("identity mismatch replaced approved credentials or service policy")
	}
}
