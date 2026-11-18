package messaging

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

func TestPauseJoinsRelayIntentAndBlocksNewTransportAndSend(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	n := &routeTestNetwork{}
	const nextURL = "https://pause-relay.example/map.json"
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	var proposedFetches atomic.Int32
	factories := n.factories("a")
	ordinaryProfile := factories.Profile
	factories.Profile = func(ctx context.Context, url string) (*transport.RelayProfile, error) {
		if url != nextURL {
			return ordinaryProfile(ctx, url)
		}
		if proposedFetches.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
		}
		return nil, ErrUnavailable
	}
	registry, err := NewRegistry(a.local, a.app, RegistryOptions{Factories: factories})
	if err != nil {
		t.Fatal(err)
	}
	a.registry = registry
	openRouteRegistry(t, b, n, "b")
	connectRoutePair(t, a, b, n, id)
	old, err := registry.manager(id)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := registry.transferEngine(id)
	if err != nil {
		t.Fatal(err)
	}
	before := registry.Snapshot(id)
	if _, err := a.app.BeginRelayChange(id, nextURL); err != nil {
		t.Fatal(err)
	}
	if err := registry.RelayChanged(id); err != nil {
		t.Fatal(err)
	}
	awaitRetryGeneration(t, started, "in-flight relay map fetch")
	type result struct {
		contact ContactSummary
		err     error
	}
	done := make(chan result, 1)
	go func() {
		contact, err := registry.SetPaused(id, true)
		done <- result{contact, err}
	}()
	awaitRetryGeneration(t, canceled, "pause canceling the relay fetch")
	select {
	case <-done:
		t.Fatal("pause returned before its relay worker stopped")
	default:
	}
	if _, err := registry.Connect(id); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a late Connect recreated the contact while the old generation was retiring")
	}
	finish()
	select {
	case got := <-done:
		if got.err != nil || got.contact.State != "paused" || !got.contact.Paused || got.contact.Revision <= before.Revision {
			t.Fatal("pause did not publish a newer paused summary", got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pause did not join the released worker")
	}
	if _, err := engine.Summaries(); err == nil {
		t.Fatal("old transfer engine remained usable after pause")
	}
	if _, err := old.Connect(id); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a stale manager retained connection authority")
	}
	if _, err := registry.Send(id, "synthetic rejected while paused"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("paused contact accepted a new message")
	}
	if _, err := registry.transferEngine(id); !errors.Is(err, ErrUnavailable) {
		t.Fatal("paused contact opened a new transfer engine")
	}
	if err := registry.RelayChanged(id); err != nil {
		t.Fatal(err)
	}
	if err := registry.RefreshRoutes(id); err != nil {
		t.Fatal(err)
	}
	current, err := registry.manager(id)
	if err != nil {
		t.Fatal(err)
	}
	current.mu.Lock()
	startedWork := current.running || current.intentRunning
	current.mu.Unlock()
	if startedWork || proposedFetches.Load() != 1 {
		t.Fatal("settings or route changes bypassed the pause")
	}
}

func TestPauseResumePreservesIdentityChangeStop(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	n := &routeTestNetwork{}
	openRouteRegistry(t, a, n, "a")
	openRouteRegistry(t, b, n, "b")
	connectRoutePair(t, a, b, n, id)
	before, err := a.registry.Send(id, "synthetic existing history")
	if err != nil {
		t.Fatal(err)
	}
	a.registry.Close()
	factories := n.factories("a-changed")
	var attempts atomic.Int32
	// Inject the typed outcome of pinned authentication; the changed-key TLS
	// handshake and absence of application bytes are covered independently.
	factories.Listen = func(context.Context, storage.PendingRecord, *transport.RelayProfile) (Listener, error) {
		attempts.Add(1)
		return nil, sessionauth.ErrIdentityChanged
	}
	registry, err := NewRegistry(a.local, a.app, RegistryOptions{Revisions: a.revisions, Factories: factories})
	if err != nil {
		t.Fatal(err)
	}
	a.registry = registry
	if _, err := registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, a, id, func(s Snapshot) bool { return s.State == "identity-changed" })
	if got, err := registry.SetPaused(id, true); err != nil || !got.Paused || got.CanConnect || got.CanSend {
		t.Fatal("pause did not apply over the identity failure", err)
	}
	if got, err := registry.SetPaused(id, false); err != nil || got.Paused || got.State != "identity-changed" || got.CanConnect {
		t.Fatal("resume bypassed the saved identity failure", err)
	}
	if _, err := registry.Connect(id); !errors.Is(err, ErrUnavailable) || attempts.Load() != 1 {
		t.Fatal("toggle rearmed connection attempts after a changed identity")
	}
	after := registry.Snapshot(id)
	if after.PeerFingerprint != before.PeerFingerprint || len(after.Messages) != 1 || after.Messages[0].ID != before.Messages[0].ID {
		t.Fatal("toggle changed the saved pin or history")
	}
}
