package transport_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"whatsup/internal/messaging"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type migrationListeners struct {
	mu           sync.Mutex
	active, peak int
	fetches      map[string]int
}
type countedMigrationListener struct {
	messaging.Listener
	once   sync.Once
	counts *migrationListeners
}

func (l *countedMigrationListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { l.counts.mu.Lock(); l.counts.active--; l.counts.mu.Unlock() })
	return err
}

func migrationFactories(old, next *transport.RelayProfile, oldURL, nextURL string, counts *migrationListeners) messaging.Factories {
	return messaging.Factories{
		Profile: func(ctx context.Context, url string) (*transport.RelayProfile, error) {
			counts.mu.Lock()
			counts.fetches[url]++
			counts.mu.Unlock()
			switch url {
			case oldURL:
				return old, nil
			case nextURL:
				return next, nil
			}
			return nil, transport.ErrRelayProfile
		},
		Listen: func(ctx context.Context, record storage.PendingRecord, profile *transport.RelayProfile) (messaging.Listener, error) {
			counts.mu.Lock()
			counts.active++
			if counts.active > counts.peak {
				counts.peak = counts.active
			}
			counts.mu.Unlock()
			listener, err := transport.NewOngoingListener(ctx, transport.OngoingOptions{Record: record, Profile: profile, Policy: transport.RelayFallback, AllowRelayFallback: true})
			if err != nil {
				counts.mu.Lock()
				counts.active--
				counts.mu.Unlock()
				return nil, err
			}
			return &countedMigrationListener{Listener: listener, counts: counts}, nil
		},
	}
}

func waitRealRelayState(t *testing.T, ctx context.Context, p *persistentMessagingPeer, id, state string) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		value, err := p.app.RelayChange(id)
		if err == nil && value.State == state {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("real relay migration did not reach %s (last state %s)", state, value.State)
		case <-tick.C:
		}
	}
}

func TestRealTailcatRelayMigrationKeepsKeysAndRestartsOnNewRoute(t *testing.T) {
	for _, sameOperator := range []bool{false, true} {
		t.Run(map[bool]string{false: "distinct-services", true: "same-operator-url-aliases"}[sameOperator], func(t *testing.T) {
			oldPhysical := transport.ExportLoopbackRelayProfileForTest(t)
			nextPhysical := oldPhysical
			if !sameOperator {
				nextPhysical = transport.ExportLoopbackRelayProfileForTest(t)
			}
			const oldURL = "https://operator.example/migration-old"
			const nextURL = "https://operator.example/migration-next"
			old := transport.ExportRelayMigrationProfileForTest(t, oldPhysical, oldURL)
			next := transport.ExportRelayMigrationProfileForTest(t, nextPhysical, nextURL)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			a, b := newPersistentMessagingPeer(t), newPersistentMessagingPeer(t)
			pairPersistentMessagingPeers(t, ctx, old, a, b)
			recordsA, err := a.app.CommittedContacts()
			if err != nil || len(recordsA) != 1 {
				t.Fatal("missing base record")
			}
			recordsB, err := b.app.CommittedContacts()
			if err != nil || len(recordsB) != 1 {
				t.Fatal("missing peer record")
			}
			id := recordsA[0].ID
			counts := &migrationListeners{fetches: map[string]int{}}
			factories := migrationFactories(old, next, oldURL, nextURL, counts)
			var left, right *messaging.Registry
			open := func() {
				var err error
				left, err = messaging.NewRegistry(a.local, a.app, messaging.RegistryOptions{Factories: factories})
				if err != nil {
					t.Fatal(err)
				}
				right, err = messaging.NewRegistry(b.local, b.app, messaging.RegistryOptions{Factories: factories})
				if err != nil {
					t.Fatal(err)
				}
			}
			open()
			t.Cleanup(func() { left.Close(); right.Close() })
			t.Cleanup(func() {
				if t.Failed() {
					leftChange, _ := a.app.RelayChange(id)
					rightChange, _ := b.app.RelayChange(id)
					counts.mu.Lock()
					defer counts.mu.Unlock()
					t.Logf("migration states=%s/%s runtime=%s/%s listeners=%d peak=%d map-fetches=%d/%d", leftChange.State, rightChange.State, left.Snapshot(id).State, right.Snapshot(id).State, counts.active, counts.peak, counts.fetches[oldURL], counts.fetches[nextURL])
				}
			})
			connect := func() {
				if _, err := left.Connect(id); err != nil {
					t.Fatal(err)
				}
				if _, err := right.Connect(id); err != nil {
					t.Fatal(err)
				}
				for _, registry := range []*messaging.Registry{left, right} {
					for {
						if s := registry.Snapshot(id); s.State == "connected" && s.ContactControlsSupported {
							break
						}
						select {
						case <-ctx.Done():
							t.Fatal("real route did not connect")
						case <-time.After(10 * time.Millisecond):
						}
					}
				}
			}
			connect()
			if _, err := left.Send(id, "synthetic real pre-migration message"); err != nil {
				t.Fatal(err)
			}
			for {
				if len(right.Snapshot(id).Messages) == 1 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("real pre-migration text missing")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if _, err := a.app.BeginRelayChange(id, nextURL); err != nil {
				t.Fatal(err)
			}
			if err := left.RelayChanged(id); err != nil {
				t.Fatal(err)
			}
			waitRealRelayState(t, ctx, b, id, "awaiting-approval")
			request, err := b.app.RelayChange(id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.app.ApproveRelayChange(id, request.RequestID); err != nil {
				t.Fatal(err)
			}
			if err := right.RelayChanged(id); err != nil {
				t.Fatal(err)
			}
			waitRealRelayState(t, ctx, a, id, "complete")
			waitRealRelayState(t, ctx, b, id, "complete")
			for _, p := range []*persistentMessagingPeer{a, b} {
				plan, err := p.app.ConnectionPlan(id)
				if err != nil || len(plan.Candidates) != 1 || plan.Candidates[0].Epoch() != 1 {
					t.Fatal("real migration retained old route", err)
				}
			}
			afterA, _ := a.app.CommittedContacts()
			afterB, _ := b.app.CommittedContacts()
			if afterA[0] != recordsA[0] || afterB[0] != recordsB[0] {
				t.Fatal("real migration replaced node key, PSK, identity or base route")
			}
			left.Close()
			right.Close()
			a.close(t)
			b.close(t)
			a.open(t)
			b.open(t)
			counts.mu.Lock()
			oldFetches := counts.fetches[oldURL]
			counts.mu.Unlock()
			open()
			connect()
			if _, err := right.Send(id, "synthetic real new-route restart reply"); err != nil {
				t.Fatal(err)
			}
			for {
				if s := left.Snapshot(id); len(s.Messages) == 2 {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("real new-route restart text missing")
				case <-time.After(10 * time.Millisecond):
				}
			}
			counts.mu.Lock()
			peak, afterOldFetches := counts.peak, counts.fetches[oldURL]
			counts.mu.Unlock()
			if peak > 2 || afterOldFetches != oldFetches {
				t.Fatal("new-only restart opened old service or exceeded two live listeners")
			}
			if sameOperator && peak != 1 {
				t.Fatal("equivalent relay aliases registered duplicate listener engines")
			}
		})
	}
}
