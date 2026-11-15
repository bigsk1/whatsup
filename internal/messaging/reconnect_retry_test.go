package messaging

import (
	"context"
	"net"
	"testing"
	"time"

	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

func TestReconnectBackoffSlowsOnlyAfterFiveContinuousFailureMinutes(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var schedule reconnectBackoff
	want := [][2]time.Duration{{time.Second, time.Second}, {time.Second, 2 * time.Second}, {2 * time.Second, 4 * time.Second}, {4 * time.Second, 8 * time.Second}, {8 * time.Second, 16 * time.Second}, {15 * time.Second, 30 * time.Second}}
	for index, bounds := range want {
		lower, upper := schedule.nextBounds(base.Add(time.Duration(index) * time.Second))
		if lower != bounds[0] || upper != bounds[1] {
			t.Fatalf("early retry%d got%v..%v", index, lower, upper)
		}
	}
	if lower, upper := schedule.nextBounds(base.Add(5*time.Minute - time.Nanosecond)); lower != 15*time.Second || upper != 30*time.Second {
		t.Fatal("retry slowed before the continuous failure threshold")
	}
	for _, elapsed := range []time.Duration{5 * time.Minute, 2 * time.Hour, 24 * time.Hour} {
		if lower, upper := schedule.nextBounds(base.Add(elapsed)); lower != 90*time.Second || upper != 150*time.Second {
			t.Fatal("long offline retries escaped their bounded slow interval", lower, upper)
		}
	}
	if schedule.attempt != 5 || schedule.failedAt != base {
		t.Fatal("retry age or attempt state grew without bounds")
	}
	schedule = reconnectBackoff{}
	if lower, upper := schedule.nextBounds(base.Add(24 * time.Hour)); lower != time.Second || upper != time.Second {
		t.Fatal("reset inherited old offline retry age")
	}
}

func TestReconnectTimingIsIndependentOfPeerReadyBudgetAndOtherContacts(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	a := &Manager{managerState: &managerState{reconnect: reconnectBackoff{failedAt: base, attempt: 5}, peerRouteBudget: routeReadyRetry{started: true, attempt: maxAutomaticRouteRestarts, plan: [32]byte{1}}}}
	b := &Manager{managerState: &managerState{}}
	for range 200 {
		if delay := a.reconnectDelay(base.Add(5 * time.Minute)); delay < 90*time.Second || delay > 150*time.Second {
			t.Fatal("long retry jitter exceeded its exact bounds", delay)
		}
	}
	if delay := b.reconnectDelay(base.Add(5 * time.Minute)); delay != time.Second {
		t.Fatal("another contact inherited the offline backoff", delay)
	}
	if a.peerRouteBudget.attempt != maxAutomaticRouteRestarts || a.peerRouteBudget.plan != [32]byte{1} {
		t.Fatal("ordinary backoff replenished the exhausted peer-ready budget")
	}
	for range 200 {
		if delay := retryDelay(0); delay != time.Second {
			t.Fatal("ordinary slow retry changed the prompt peer-ready delay")
		}
	}
}

func TestFreshAuthenticatedSessionResetsLongOfflineRetryAge(t *testing.T) {
	p := newRetryGenerationPeer(t, true)
	awaitRetryGeneration(t, p.dialed, "held connection before synthetic long outage")
	p.manager.mu.Lock()
	p.manager.reconnect = reconnectBackoff{failedAt: time.Now().Add(-time.Hour), attempt: 5}
	p.manager.mu.Unlock()
	p.hold.Store(false)
	if err := p.manager.refreshRoutes(); err != nil {
		t.Fatal(err)
	}
	awaitRetryGeneration(t, p.ready, "authenticated reconnect after synthetic long outage")
	p.manager.mu.Lock()
	reset := p.manager.reconnect.failedAt.IsZero() && p.manager.reconnect.attempt == 0
	p.manager.mu.Unlock()
	if !reset {
		t.Fatal("exact-peer TLS did not reset the ordinary reconnect schedule")
	}
	p.fail.Store(true)
	if err := p.manager.refreshRoutes(); err != nil {
		t.Fatal(err)
	}
	awaitRetryGeneration(t, p.failed, "first transient failure after authentication")
	awaitRetryGeneration(t, p.ready, "prompt reconnect after authentication reset")
}

func TestFreshConnectWorkerResetsLongOfflineSchedule(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	observed := make(chan reconnectBackoff, 1)
	var m *Manager
	m, err := NewContact(b.local, b.app, id, ContactOptions{Factories: Factories{
		Profile: func(context.Context, string) (*transport.RelayProfile, error) { return nil, nil },
		Dial: func(ctx context.Context, _ storage.PendingRecord, _ *transport.RelayProfile) (net.Conn, error) {
			m.mu.Lock()
			schedule := m.reconnect
			m.mu.Unlock()
			observed <- schedule
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	m.mu.Lock()
	m.reconnect = reconnectBackoff{failedAt: time.Now().Add(-time.Hour), attempt: 5}
	m.mu.Unlock()
	if _, err := m.Connect(id); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-observed:
		if !got.failedAt.IsZero() || got.attempt != 0 {
			t.Fatal("fresh manual connection retained the long failure schedule")
		}
	case <-time.After(time.Second):
		t.Fatal("fresh Connect did not start its first dial")
	}
}

func TestSlowReconnectWaitIsImmediatelyCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{managerState: &managerState{ctx: ctx, routeRestart: make(chan *routeRestartRequest, 1)}}
	done := make(chan bool, 1)
	go func() { done <- m.waitRetry(slowReconnectMaximum) }()
	cancel()
	select {
	case retried := <-done:
		if retried {
			t.Fatal("shutdown restarted a contact during long offline backoff")
		}
	case <-time.After(time.Second):
		t.Fatal("slow reconnect delay blocked shutdown")
	}
}
