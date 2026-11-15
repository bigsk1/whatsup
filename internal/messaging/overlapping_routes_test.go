package messaging

import (
	"context"
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

func approvedMigrationManager(t *testing.T) (*Manager, *registryPeer, *registryPeer, []approvedRoute, *routeTestNetwork) {
	t.Helper()
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	n := &routeTestNetwork{}
	openRouteRegistry(t, a, n, "a")
	openRouteRegistry(t, b, n, "b")
	connectRoutePair(t, a, b, n, id)
	a.registry.Close()
	b.registry.Close()
	approveSyntheticMigration(t, a, b, id, "https://overlapping.example/map")
	m, err := NewContact(a.local, a.app, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	routes, err := m.approvedRoutes()
	if err != nil || len(routes) != 2 {
		t.Fatal("missing approved route pair", err)
	}
	m.factories = n.factories("overlap-test")
	m.mu.Lock()
	m.planKey = routePlanKey(routes)
	m.mu.Unlock()
	return m, a, b, routes, n
}

func TestDisjointFallbackStageKeepsAuthenticatedChatThenVisitsPreferredAfterClose(t *testing.T) {
	m, a, b, routes, n := approvedMigrationManager(t)
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	const window = 150 * time.Millisecond
	listen := m.factories.Listen
	var preferredAttempts atomic.Int32
	m.factories.Listen = func(ctx context.Context, record storage.PendingRecord, profile *transport.RelayProfile) (Listener, error) {
		if record.RelayServiceURL == routes[0].record.RelayServiceURL && preferredAttempts.Add(1) == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return listen(ctx, record, profile)
	}
	done := make(chan error, 1)
	go func() { done <- m.visitOverlappingListeners(ctx, routes, window) }()
	defer func() { cancel(); <-done }()
	until := time.Now().Add(time.Second)
	for {
		n.mu.Lock()
		ready := n.listeners[routes[1].record.ID+routes[1].record.RelayServiceURL] != nil
		n.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(until) {
			t.Fatal("old fallback listener not ready")
		}
		time.Sleep(time.Millisecond)
	}
	raw, err := m.factories.Dial(ctx, peerRouteRecord(t, b, routes[1]), nil)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := sessionauth.Authenticate(ctx, raw, sessionauth.Options{Local: b.local, Peer: a.local.PublicKey(), ContactID: routes[1].record.ID, Association: routes[1].association})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	consumeInitialProfile(t, peer)
	textAcked := make(chan struct{}, 1)
	go func() {
		for {
			frame, err := readPacket(peer)
			if err != nil {
				return
			}
			if frame.kind == dataPacket && frame.frame.Type == chat.MessageFrame {
				if writePacket(peer, packet{kind: dataPacket, frame: chat.Frame{Version: 1, Type: chat.AckFrame, ID: frame.frame.ID}}) != nil {
					return
				}
				textAcked <- struct{}{}
			}
		}
	}()
	time.Sleep(3 * window)
	m.mu.Lock()
	live := m.sessionActive && m.claim != nil && m.claim.route.association == routes[1].association
	m.mu.Unlock()
	if !live {
		t.Fatal("disjoint-map stage timer disconnected a working fallback")
	}
	if _, err := m.Send(routes[1].record.ID, "synthetic text on retained disjoint fallback"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-textAcked:
	case <-time.After(time.Second):
		t.Fatal("retained fallback could not deliver text")
	}
	if preferredAttempts.Load() != 1 {
		t.Fatal("live fallback was retired to keep trying preferred")
	}
	_ = peer.Close()
	until = time.Now().Add(3 * time.Second)
	for preferredAttempts.Load() < 2 {
		if time.Now().After(until) {
			t.Fatal("natural fallback close prevented future preferred attempts")
		}
		time.Sleep(time.Millisecond)
	}
	n.mu.Lock()
	peak := n.peak[routes[0].record.ID]
	n.mu.Unlock()
	if peak != 1 {
		t.Fatal("fallback retention created duplicate physical engines")
	}
}

func TestEquivalentListenerProofUsesExactlyNegotiatedRoute(t *testing.T) {
	for _, index := range []int{0, 1} {
		t.Run([]string{"new", "old"}[index], func(t *testing.T) {
			m, a, b, routes, _ := approvedMigrationManager(t)
			ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
			defer cancel()
			left, right := net.Pipe()
			done := make(chan error, 1)
			go func() { done <- m.sessionRouteCandidates(ctx, left, routes[0], routes[1]) }()
			peer, err := sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: b.local, Peer: a.local.PublicKey(), ContactID: routes[0].record.ID, Association: routes[index].association})
			if err != nil {
				t.Fatal("approved multiplexed route did not authenticate", err)
			}
			defer func() { cancel(); _ = peer.Close(); <-done }()
			consumeInitialProfile(t, peer)
			m.mu.Lock()
			selected := m.controls.route
			proof := *m.controls.proof
			m.mu.Unlock()
			if selected.association != routes[index].association || selected.epoch != routes[index].epoch {
				t.Fatal("physical equivalence substituted the preferred route for negotiated TLS authority")
			}
			if index == 1 {
				if _, err := a.app.CreateRelayCommit(proof); err == nil {
					t.Fatal("old-route TLS authorized a new-route commitment")
				}
				state, err := a.app.RelayChange(routes[0].record.ID)
				if err != nil || state.State != "preparing" {
					t.Fatal("old negotiated route advanced migration", err)
				}
			}
		})
	}
}

func TestOverlappingStagesUseOldFallbackThenRetryNewWithoutDuplicateEngines(t *testing.T) {
	m, a, b, routes, n := approvedMigrationManager(t)
	ctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
	defer cancel()
	var callsMu sync.Mutex
	preferredCalls := 0
	listen := m.factories.Listen
	m.factories.Listen = func(ctx context.Context, r storage.PendingRecord, p *transport.RelayProfile) (Listener, error) {
		if r.RelayServiceURL == routes[0].record.RelayServiceURL {
			callsMu.Lock()
			preferredCalls++
			first := preferredCalls == 1
			callsMu.Unlock()
			if first {
				return nil, ErrUnavailable
			}
		}
		return listen(ctx, r, p)
	}
	done := make(chan error, 1)
	go func() { done <- m.visitOverlappingListeners(ctx, routes, 400*time.Millisecond) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("serial listener stages did not join")
		}
	}()
	dial := func(route approvedRoute) *sessionauth.Session {
		t.Helper()
		var raw net.Conn
		for ctx.Err() == nil {
			var err error
			raw, err = m.factories.Dial(ctx, peerRouteRecord(t, b, route), nil)
			if err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		peer, err := sessionauth.Authenticate(ctx, raw, sessionauth.Options{Local: b.local, Peer: a.local.PublicKey(), ContactID: route.record.ID, Association: route.association})
		if err != nil {
			t.Fatal("serial fallback route failed pinned TLS", err)
		}
		return peer
	}
	old := dial(routes[1])
	defer old.Close()
	consumeInitialProfile(t, old)
	read := make(chan packet, 8)
	go func() {
		defer close(read)
		for {
			p, err := readPacket(old)
			if err != nil {
				return
			}
			select {
			case read <- p:
			case <-ctx.Done():
				return
			}
		}
	}()
	id := capacityID(117)
	if err := writePacket(old, packet{kind: dataPacket, frame: chat.Frame{Version: chat.ProtocolVersion, Type: chat.MessageFrame, ID: id, Text: "synthetic bounded fallback text"}}); err != nil {
		t.Fatal(err)
	}
	acknowledged := false
	for !acknowledged {
		select {
		case p, ok := <-read:
			if !ok {
				t.Fatal("working fallback closed before acknowledging text")
			}
			acknowledged = p.kind == dataPacket && p.frame.Type == chat.AckFrame && p.frame.ID == id
		case <-time.After(time.Second):
			t.Fatal("old fallback did not durably receive and ACK text")
		}
	}
	// A live fallback is not retired for migration. Closing it naturally makes
	// the next serial preferred stage eligible without registering two engines.
	_ = old.Close()
	for range read {
	}
	next := dial(routes[0])
	defer next.Close()
	consumeInitialProfile(t, next)
	m.mu.Lock()
	selected := m.controls.route
	m.mu.Unlock()
	if selected.association != routes[0].association {
		t.Fatal("preferred retry accepted the old association")
	}
	n.mu.Lock()
	peak := n.peak[routes[0].record.ID]
	n.mu.Unlock()
	if peak != 1 {
		t.Fatal("serial overlap registered simultaneous engines")
	}
}

func peerRouteRecord(t *testing.T, peer *registryPeer, route approvedRoute) storage.PendingRecord {
	t.Helper()
	plan, err := peer.app.ConnectionPlan(route.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range plan.Candidates {
		if candidate.Association() == route.association {
			record, err := candidate.Record()
			if err != nil {
				t.Fatal(err)
			}
			return record
		}
	}
	t.Fatal("peer route not approved")
	return storage.PendingRecord{}
}

func TestOverlappingStagesCancellationJoinsPendingAccept(t *testing.T) {
	m, _, _, routes, n := approvedMigrationManager(t)
	ctx, cancel := context.WithCancel(m.ctx)
	done := make(chan error, 1)
	go func() { done <- m.visitOverlappingListeners(ctx, routes, time.Second) }()
	limit := time.Now().Add(time.Second)
	for {
		n.mu.Lock()
		ready := n.active[routes[0].record.ID] == 1
		n.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(limit) {
			cancel()
			t.Fatal("preferred listener did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancel did not terminate the route generation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not join the pending listener")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.active[routes[0].record.ID] != 0 || n.listeners[routes[1].record.ID+routes[1].record.RelayServiceURL] != nil {
		t.Fatal("cancellation started or retained the fallback listener")
	}
}

func TestOverlappingStagesBoundBlockedSetupAndSpareAuthenticatedPreferred(t *testing.T) {
	for _, blocked := range []bool{true, false} {
		t.Run(map[bool]string{true: "blocked setup", false: "authenticated preferred"}[blocked], func(t *testing.T) {
			m, a, b, routes, n := approvedMigrationManager(t)
			ctx, cancel := context.WithTimeout(m.ctx, 4*time.Second)
			defer cancel()
			const window = 200 * time.Millisecond
			if blocked {
				listen := m.factories.Listen
				m.factories.Listen = func(ctx context.Context, r storage.PendingRecord, p *transport.RelayProfile) (Listener, error) {
					if r.RelayServiceURL == routes[0].record.RelayServiceURL {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return listen(ctx, r, p)
				}
			}
			done := make(chan error, 1)
			go func() { done <- m.visitOverlappingListeners(ctx, routes, window) }()
			defer func() { cancel(); <-done }()
			index := 0
			if blocked {
				index = 1
			}
			limit := time.Now().Add(time.Second)
			for {
				n.mu.Lock()
				ready := n.listeners[routes[index].record.ID+routes[index].record.RelayServiceURL] != nil
				n.mu.Unlock()
				if ready {
					break
				}
				if time.Now().After(limit) {
					t.Fatal("bounded setup did not expose the next eligible listener")
				}
				time.Sleep(time.Millisecond)
			}
			if !blocked {
				raw, err := m.factories.Dial(ctx, peerRouteRecord(t, b, routes[0]), nil)
				if err != nil {
					t.Fatal(err)
				}
				peer, err := sessionauth.Authenticate(ctx, raw, sessionauth.Options{Local: b.local, Peer: a.local.PublicKey(), ContactID: routes[0].record.ID, Association: routes[0].association})
				if err != nil {
					t.Fatal(err)
				}
				defer peer.Close()
				consumeInitialProfile(t, peer)
				go func() {
					for {
						if _, err := readPacket(peer); err != nil {
							return
						}
					}
				}()
				timer := time.NewTimer(3 * window)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					t.Fatal("preferred session unexpectedly stopped")
				}
				m.mu.Lock()
				live := m.sessionActive && m.claim != nil && m.claim.route.association == routes[0].association
				m.mu.Unlock()
				if !live {
					t.Fatal("stage timer killed an authenticated preferred session")
				}
			}
			n.mu.Lock()
			peak := n.peak[routes[0].record.ID]
			n.mu.Unlock()
			if peak != 1 {
				t.Fatal("setup stages registered duplicate engines")
			}
		})
	}
}

func TestListenerFreshnessDistinguishesInMemoryFactoryFromInvalidRealProfile(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "in-memory factory", true: "invalid nonnil profile"}[invalid], func(t *testing.T) {
			m, a, b, routes, _ := approvedMigrationManager(t)
			ctx, cancel := context.WithTimeout(m.ctx, 4*time.Second)
			defer cancel()
			var profile *transport.RelayProfile
			if invalid {
				profile = &transport.RelayProfile{}
			}
			done := make(chan error, 1)
			go func() { done <- m.serveRoute(ctx, routes[0], profile) }()
			joined := false
			defer func() {
				cancel()
				if !joined {
					<-done
				}
			}()
			connect := func() *sessionauth.Session {
				t.Helper()
				var raw net.Conn
				for ctx.Err() == nil {
					var err error
					raw, err = m.factories.Dial(ctx, peerRouteRecord(t, b, routes[0]), nil)
					if err == nil {
						break
					}
					time.Sleep(time.Millisecond)
				}
				peer, err := sessionauth.Authenticate(ctx, raw, sessionauth.Options{Local: b.local, Peer: a.local.PublicKey(), ContactID: routes[0].record.ID, Association: routes[0].association})
				if err != nil {
					t.Fatal(err)
				}
				consumeInitialProfile(t, peer)
				return peer
			}
			peer := connect()
			_ = peer.Close()
			if invalid {
				select {
				case err := <-done:
					joined = true
					if !errors.Is(err, ErrUnavailable) {
						t.Fatal("invalid real profile did not retire the listener", err)
					}
				case <-time.After(time.Second):
					t.Fatal("invalid nonnil profile kept the listener available")
				}
			} else {
				// The intentional nil-profile transport should keep its existing
				// listener after a lost stream, rather than fabricate map expiry.
				second := connect()
				_ = second.Close()
			}
		})
	}
}
