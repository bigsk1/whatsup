package messaging

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/chat"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

func TestRouteReadyRetryHasOneImmediateAttemptAndBoundedSchedulePerPlan(t *testing.T) {
	var retry routeReadyRetry
	first, next := [32]byte{1}, [32]byte{2}
	if delay, ok := retry.next(first); !ok || delay != 0 {
		t.Fatal("first approved handover was not prompt")
	}
	for range maxAutomaticRouteRestarts - 1 {
		if delay, ok := retry.next(first); !ok || delay < time.Second || delay > 30*time.Second {
			t.Fatal("repeated route changes escaped bounded retry delays")
		}
	}
	for range 100 {
		if _, ok := retry.next(first); ok || retry.attempt != maxAutomaticRouteRestarts {
			t.Fatal("unchanged route plan escaped the hard restart limit")
		}
	}
	if delay, ok := retry.next(next); !ok || delay != 0 || retry.attempt != 1 {
		t.Fatal("a genuinely changed approved plan inherited old-route backoff")
	}
}

func TestPeerReadyRetryUsesCurrentApprovalInsteadOfRetiringGenerationKey(t *testing.T) {
	m, _, _, _, _ := approvedMigrationManager(t)
	old := [32]byte{207}
	current, err := m.currentPlanKey()
	if err != nil || current == old {
		t.Fatal("missing current approved plan", err)
	}
	m.mu.Lock()
	m.planKey = old
	m.mu.Unlock()
	retry := routeReadyRetry{plan: old, started: true, attempt: maxAutomaticRouteRestarts}
	delay, allowed, err := m.peerRouteRetry(&retry)
	if err != nil || !allowed || delay != 0 || retry.plan != current || retry.attempt != 1 {
		t.Fatal("fresh approval inherited the exhausted retiring plan", err)
	}
}

func TestLocalRouteErrorsCannotConsumePeerReadyBudget(t *testing.T) {
	for _, err := range []error{ErrUnavailable, context.DeadlineExceeded, errors.New("synthetic transient map lookup"), storage.ErrStateFailed} {
		got := routeFailure(err)
		if errors.Is(got, errRoutesChanged) || errors.Is(got, errPeerRouteReady) || !errors.Is(got, err) {
			t.Fatal("local route failure was relabeled as peer rebuild", got)
		}
	}
	if !errors.Is(errPeerRouteReady, errRoutesChanged) || errors.Is(errLocalRouteRefresh, errPeerRouteReady) {
		t.Fatal("local refresh obtained peer-only budget classification")
	}
}

// The newer transport initially cannot be reached, while the old transport
// repeatedly authenticates the exact saved identity and announces the same
// genuinely approved newer route. Each peer-induced restart must remain bounded
// even though those fallback handshakes reset the ordinary dial retry schedule.
func TestRepeatedRouteReadyUsesBoundedBackoffAndCanRecoverOrCancel(t *testing.T) {
	for _, mode := range []string{"cancel during backoff", "preferred route recovers", "limit then explicit retry"} {
		t.Run(mode, func(t *testing.T) {
			initial, owner, joiner, _, _ := approvedMigrationManager(t)
			initial.Close()
			id := initial.record.ID
			var allowPreferred atomic.Bool
			var authenticatedFallback atomic.Int32
			preferredDials := make(chan time.Time, 32)
			var workers sync.WaitGroup
			var routes []approvedRoute
			factories := Factories{
				Profile: func(context.Context, string) (*transport.RelayProfile, error) { return nil, nil },
				Dial: func(ctx context.Context, record storage.PendingRecord, _ *transport.RelayProfile) (net.Conn, error) {
					preferred := record.RelayServiceURL == routes[0].record.RelayServiceURL
					if preferred {
						select {
						case preferredDials <- time.Now():
						default:
						}
						if !allowPreferred.Load() {
							return nil, ErrUnavailable
						}
					}
					route := routes[1]
					if preferred {
						route = routes[0]
					}
					left, right := net.Pipe()
					workers.Add(1)
					go func() {
						defer workers.Done()
						peer, err := sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: owner.local, Peer: joiner.local.PublicKey(), ContactID: id, Association: route.association, Server: true})
						if err != nil {
							_ = right.Close()
							return
						}
						defer peer.Close()
						if !preferred {
							authenticatedFallback.Add(1)
						}
						profileSeen, readDone := make(chan struct{}), make(chan struct{})
						go func() {
							defer close(readDone)
							announced := false
							for {
								frame, err := readPacket(peer)
								if err != nil {
									return
								}
								if frame.kind == profilePacket && !announced {
									announced = true
									close(profileSeen)
								}
								if preferred && frame.kind == dataPacket && frame.frame.Type == chat.MessageFrame {
									if writePacket(peer, packet{kind: dataPacket, frame: chat.Frame{Version: chat.ProtocolVersion, Type: chat.AckFrame, ID: frame.frame.ID}}) != nil {
										return
									}
								}
							}
						}()
						select {
						case <-profileSeen:
							if !preferred {
								body := binary.BigEndian.AppendUint64(nil, routes[0].epoch)
								body = append(body, routes[0].association[:]...)
								nonce, err := base64.RawURLEncoding.Strict().DecodeString(routes[0].candidate.RequestID())
								if err == nil {
									body = append(body, nonce...)
									frame, err := relayFrame(routeReadyControl, body)
									if err == nil {
										_ = writePacket(peer, packet{kind: controlPacket, control: frame})
									}
								}
							}
						case <-readDone:
						case <-ctx.Done():
						}
						<-readDone
					}()
					return left, nil
				},
			}
			m, err := NewContact(joiner.local, joiner.app, id, ContactOptions{Factories: factories})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { m.Close(); workers.Wait() })
			routes, err = m.approvedRoutes()
			if err != nil || len(routes) != 2 || routes[0].epoch <= routes[1].epoch {
				t.Fatal("expected an approved newer route and retained fallback", err)
			}
			queued, err := m.Send(id, "synthetic message retained while approved route is unreachable")
			if err != nil || len(queued.Messages) != 1 {
				t.Fatal("could not queue synthetic message", err)
			}
			queuedID := queued.Messages[0].ID
			if _, err := m.Connect(id); err != nil {
				t.Fatal(err)
			}
			var dialed [3]time.Time
			for index := range dialed {
				select {
				case dialed[index] = <-preferredDials:
				case <-time.After(5 * time.Second):
					t.Fatal("approved new route was not retried")
				}
			}
			if dialed[2].Sub(dialed[1]) < 900*time.Millisecond {
				t.Fatal("repeated authenticated ready notices bypassed the minimum retry delay")
			}
			if authenticatedFallback.Load() < 2 {
				t.Fatal("regression did not authenticate repeated old-route streams")
			}
			pending := m.Snapshot()
			if pending.State == "failed" || len(pending.Messages) != 1 || pending.Messages[0].ID != queuedID || pending.Messages[0].Delivered {
				t.Fatal("unreachable migration changed local approval or acknowledged queued text")
			}
			if mode == "cancel during backoff" {
				start := time.Now()
				m.Close()
				if time.Since(start) > time.Second {
					t.Fatal("cancellation did not interrupt route-ready backoff")
				}
				return
			}
			if mode == "limit then explicit retry" {
				limit := time.Now().Add(5 * time.Second)
				for {
					s := m.Snapshot()
					if s.State == "route-unavailable" && s.CanConnect {
						if !s.CanSend || len(s.Messages) != 1 || s.Messages[0].ID != queuedID || s.Messages[0].Delivered {
							t.Fatal("route exhaustion lost local queuing or acknowledged retained text")
						}
						break
					}
					if time.Now().After(limit) {
						t.Fatal("repeated same-plan handovers did not stop at the hard limit")
					}
					time.Sleep(5 * time.Millisecond)
				}
				// Exactly one initial dial plus the bounded three automatic
				// rebuilds are permitted; no disconnected worker keeps retrying.
				if authenticatedFallback.Load() != maxAutomaticRouteRestarts+1 {
					t.Fatal("route handover count exceeded its hard limit")
				}
				for len(preferredDials) > 0 {
					<-preferredDials
				}
				select {
				case <-preferredDials:
					t.Fatal("route exhaustion kept retrying without an explicit action")
				case <-time.After(100 * time.Millisecond):
				}
			}
			allowPreferred.Store(true)
			if mode == "limit then explicit retry" {
				if _, err := m.Connect(id); err != nil {
					t.Fatal("explicit Connect could not retry a restored route", err)
				}
			}
			limit := time.Now().Add(5 * time.Second)
			for {
				s := m.Snapshot()
				if s.State == "connected" && len(s.Messages) == 1 && s.Messages[0].ID == queuedID && s.Messages[0].Delivered {
					m.mu.Lock()
					progress := m.preferredAuthenticated
					m.mu.Unlock()
					if progress == 0 {
						t.Fatal("preferred-route authentication did not record retry-budget progress")
					}
					break
				}
				if time.Now().After(limit) {
					t.Fatal("preferred route recovery did not deliver the retained stable message")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
