package messaging

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

// This peer substitutes only the physical dial. Every successful connection
// completes real pinned TLS and a framed ping/pong after the initial profile,
// so a connected snapshot alone cannot satisfy the test's readiness barrier.
type retryGenerationPeer struct {
	manager *Manager
	hold    atomic.Bool
	fail    atomic.Bool
	dialed  chan struct{}
	failed  chan struct{}
	ready   chan struct{}
	workers sync.WaitGroup
}

func newRetryGenerationPeer(t *testing.T, hold bool) *retryGenerationPeer {
	t.Helper()
	owner, joiner := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, owner, joiner)
	records, err := joiner.app.CommittedContacts()
	if err != nil || len(records) != 1 {
		t.Fatal("synthetic retry contact unavailable", err)
	}
	binding, err := storage.ContactBinding(records[0])
	if err != nil {
		t.Fatal(err)
	}
	p := &retryGenerationPeer{dialed: make(chan struct{}, 32), failed: make(chan struct{}, 1), ready: make(chan struct{}, 32)}
	p.hold.Store(hold)
	factories := Factories{
		Profile: func(context.Context, string) (*transport.RelayProfile, error) { return nil, nil },
		Dial: func(ctx context.Context, _ storage.PendingRecord, _ *transport.RelayProfile) (net.Conn, error) {
			select {
			case p.dialed <- struct{}{}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if p.hold.Load() {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			if p.fail.Swap(false) {
				p.failed <- struct{}{}
				return nil, ErrUnavailable
			}
			left, right := net.Pipe()
			p.workers.Add(1)
			go func() {
				defer p.workers.Done()
				session, err := sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: owner.local, Peer: joiner.local.PublicKey(), ContactID: id, Association: binding, Server: true})
				if err != nil {
					return
				}
				defer session.Close()
				var nonce = [16]byte{117, 41}
				barrierSent, announced := false, false
				for {
					frame, err := readPacket(session)
					if err != nil {
						return
					}
					switch frame.kind {
					case profilePacket:
						if !barrierSent {
							barrierSent = true
							if writePacket(session, packet{kind: pingPacket, nonce: nonce}) != nil {
								return
							}
						}
					case pongPacket:
						if barrierSent && !announced && frame.nonce == nonce {
							announced = true
							select {
							case p.ready <- struct{}{}:
							case <-ctx.Done():
								return
							}
						}
					case pingPacket:
						if writePacket(session, packet{kind: pongPacket, nonce: frame.nonce}) != nil {
							return
						}
					}
				}
			}()
			return left, nil
		},
	}
	p.manager, err = NewContact(joiner.local, joiner.app, id, ContactOptions{Factories: factories})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.manager.Close(); p.workers.Wait() })
	if _, err := p.manager.Connect(id); err != nil {
		t.Fatal(err)
	}
	return p
}

func awaitRetryGeneration(t *testing.T, event <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not complete within the prompt retry budget", description)
	}
}

func TestPlannedRouteRebuildsDoNotIncreaseTransportRetryDelay(t *testing.T) {
	p := newRetryGenerationPeer(t, true)
	for range 6 {
		awaitRetryGeneration(t, p.dialed, "planned route dial")
		if err := p.manager.refreshRoutes(); err != nil {
			t.Fatal("planned route retirement did not join", err)
		}
	}
	// The next stage has not reached TLS. A failed physical dial should use the
	// initial bounded retry, rather than inherit six deliberate cancellations.
	p.fail.Store(true)
	p.hold.Store(false)
	if err := p.manager.refreshRoutes(); err != nil {
		t.Fatal(err)
	}
	awaitRetryGeneration(t, p.failed, "injected transient dial failure")
	awaitRetryGeneration(t, p.ready, "authenticated retry after planned rebuilds")
}

func TestFreshAuthenticationResetsRetryAcrossPlannedRouteRebuilds(t *testing.T) {
	p := newRetryGenerationPeer(t, false)
	awaitRetryGeneration(t, p.ready, "initial authenticated stream")
	for range 6 {
		if err := p.manager.refreshRoutes(); err != nil {
			t.Fatal("authenticated route retirement did not join", err)
		}
		awaitRetryGeneration(t, p.ready, "replacement authenticated stream")
	}
	p.fail.Store(true)
	if err := p.manager.refreshRoutes(); err != nil {
		t.Fatal(err)
	}
	awaitRetryGeneration(t, p.failed, "injected transient dial failure")
	awaitRetryGeneration(t, p.ready, "prompt retry following fresh authentication")
}
