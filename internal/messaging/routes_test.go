package messaging

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type routeTestListener struct {
	base    *registryPipeListener
	network *routeTestNetwork
	key     string
	id      string
	once    sync.Once
}

func (l *routeTestListener) Accept(ctx context.Context) (net.Conn, error) { return l.base.Accept(ctx) }
func (l *routeTestListener) Close() error {
	l.once.Do(func() {
		_ = l.base.Close()
		l.network.mu.Lock()
		if l.network.listeners[l.key] == l {
			delete(l.network.listeners, l.key)
		}
		l.network.active[l.id]--
		l.network.mu.Unlock()
	})
	return nil
}

type routeTestNetwork struct {
	mu           sync.Mutex
	listeners    map[string]*routeTestListener
	active, peak map[string]int
	fetches      map[string]int
}

func (n *routeTestNetwork) factories(label string) Factories {
	return Factories{
		Profile: func(ctx context.Context, url string) (*transport.RelayProfile, error) {
			n.mu.Lock()
			if n.fetches == nil {
				n.fetches = map[string]int{}
			}
			n.fetches[label+url]++
			n.mu.Unlock()
			return nil, nil
		},
		// The Go-only fixture substitutes map selection and transports. It still
		// runs actual pinned TLS, signed decisions and encrypted durable stores.
		PreferredRegion: func(*transport.RelayProfile) (uint16, error) { return 900, nil },
		Listen: func(ctx context.Context, record storage.PendingRecord, _ *transport.RelayProfile) (Listener, error) {
			n.mu.Lock()
			defer n.mu.Unlock()
			if n.listeners == nil {
				n.listeners = map[string]*routeTestListener{}
				n.active = map[string]int{}
				n.peak = map[string]int{}
			}
			key := record.ID + record.RelayServiceURL
			if n.listeners[key] != nil {
				return nil, errors.New("duplicate synthetic route listener")
			}
			l := &routeTestListener{base: &registryPipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}, network: n, key: key, id: record.ID}
			n.listeners[key] = l
			n.active[record.ID]++
			if n.active[record.ID] > n.peak[record.ID] {
				n.peak[record.ID] = n.active[record.ID]
			}
			return l, nil
		},
		Dial: func(ctx context.Context, record storage.PendingRecord, _ *transport.RelayProfile) (net.Conn, error) {
			n.mu.Lock()
			l := n.listeners[record.ID+record.RelayServiceURL]
			n.mu.Unlock()
			if l == nil {
				return nil, ErrUnavailable
			}
			left, right := net.Pipe()
			select {
			case l.base.incoming <- left:
				return right, nil
			case <-ctx.Done():
				_ = left.Close()
				_ = right.Close()
				return nil, ctx.Err()
			case <-l.base.closed:
				_ = left.Close()
				_ = right.Close()
				return nil, ErrUnavailable
			}
		},
	}
}
func openRouteRegistry(t *testing.T, p *registryPeer, n *routeTestNetwork, label string) {
	t.Helper()
	r, err := NewRegistry(p.local, p.app, RegistryOptions{Revisions: p.revisions, Factories: n.factories(label)})
	if err != nil {
		t.Fatal(err)
	}
	p.registry = r
}
func waitRelayState(t *testing.T, p *registryPeer, id, state string) storage.RelayChangeSnapshot {
	t.Helper()
	limit := time.Now().Add(15 * time.Second)
	for {
		snapshot, err := p.app.RelayChange(id)
		if err == nil && snapshot.State == state {
			return snapshot
		}
		if time.Now().After(limit) {
			t.Fatalf("relay state did not become %s (last state %s, error %v)", state, snapshot.State, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func connectRoutePair(t *testing.T, a, b *registryPeer, n *routeTestNetwork, id string) {
	t.Helper()
	if _, err := a.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	limit := time.Now().Add(time.Second)
	for {
		n.mu.Lock()
		ready := n.active[id] > 0
		n.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("route listener unavailable")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := b.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*registryPeer{a, b} {
		waitRegistry(t, p, id, func(s Snapshot) bool { return s.State == "connected" && s.ContactControlsSupported })
	}
}

func TestRelayMigrationUsesBothConsentActualNewTLSAndIndependentContact(t *testing.T) {
	a, b, c := newRegistryPeer(t), newRegistryPeer(t), newRegistryPeer(t)
	ab, ac := seedRegistryAssociation(t, a, b), seedRegistryAssociation(t, a, c)
	n := &routeTestNetwork{}
	openRouteRegistry(t, a, n, "a")
	openRouteRegistry(t, b, n, "b")
	openRouteRegistry(t, c, n, "c")
	connectRoutePair(t, a, b, n, ab)
	connectRoutePair(t, a, c, n, ac)
	initialA, _ := a.app.CommittedContacts()
	initialB, _ := b.app.CommittedContacts()
	if _, err := a.registry.Send(ab, "synthetic pre-migration durable history"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, b, ab, func(s Snapshot) bool { return len(s.Messages) == 1 })
	const nextURL = "https://next-relay.example/map.json"
	if _, err := a.app.BeginRelayChange(ab, nextURL); err != nil {
		t.Fatal(err)
	}
	if err := a.registry.RelayChanged(ab); err != nil {
		t.Fatal(err)
	}
	proposal := waitRelayState(t, b, ab, "awaiting-approval")
	n.mu.Lock()
	fetchedWithoutConsent := n.fetches["b"+nextURL]
	n.mu.Unlock()
	if proposal.LocalApproved || fetchedWithoutConsent != 0 {
		t.Fatal("imported peer proposal fetched or approved a service")
	}
	plan, err := b.app.ConnectionPlan(ab)
	if err != nil || len(plan.Candidates) != 1 {
		t.Fatal("unapproved route became eligible", err)
	}
	if _, err := b.app.ApproveRelayChange(ab, proposal.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := b.registry.RelayChanged(ab); err != nil {
		t.Fatal(err)
	}
	waitRelayState(t, a, ab, "complete")
	waitRelayState(t, b, ab, "complete")
	for _, p := range []*registryPeer{a, b} {
		plan, err := p.app.ConnectionPlan(ab)
		if err != nil || len(plan.Candidates) != 1 || plan.Candidates[0].Epoch() != 1 {
			t.Fatal("old candidate was not retired after durable mutual commit", err)
		}
		record, err := plan.Candidates[0].Record()
		if err != nil || record.RelayServiceURL != nextURL {
			t.Fatal("new service was not active", err)
		}
		waitRegistry(t, p, ab, func(s Snapshot) bool { return s.State == "connected" && s.CanSend })
	}
	afterA, _ := a.app.CommittedContacts()
	afterB, _ := b.app.CommittedContacts()
	if len(afterA) != len(initialA) || len(afterB) != len(initialB) {
		t.Fatal("migration replaced contacts")
	}
	for i := range initialA {
		if afterA[i] != initialA[i] {
			t.Fatal("base contact/credentials changed")
		}
	}
	if afterB[0] != initialB[0] {
		t.Fatal("joiner base contact/credentials changed")
	}
	if _, err := b.registry.Send(ab, "synthetic post-migration reply"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, a, ab, func(s Snapshot) bool { return len(s.Messages) == 2 })
	if _, err := a.registry.Send(ac, "synthetic unaffected contact"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, c, ac, func(s Snapshot) bool { return len(s.Messages) == 1 })
	n.mu.Lock()
	peak := n.peak[ab]
	n.mu.Unlock()
	if peak > 2 {
		t.Fatal("more than two route listeners were live")
	}
}

func TestLocalRelayIntentCompletesAfterRegistryRestartWithoutPeerConsentFetch(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	n := &routeTestNetwork{}
	openRouteRegistry(t, a, n, "a")
	openRouteRegistry(t, b, n, "b")
	connectRoutePair(t, a, b, n, id)
	const nextURL = "https://restart-relay.example/map.json"
	if _, err := a.app.BeginRelayChange(id, nextURL); err != nil {
		t.Fatal(err)
	}
	// No RelayChanged/worker ran before closing: intent alone is durable.
	a.registry.Close()
	openRouteRegistry(t, a, n, "a-restart")
	if _, err := a.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	waitRelayState(t, a, id, "awaiting-approval")
	intent, err := a.app.PendingRelayIntent(id)
	if err != nil || intent.NeedsRegion {
		t.Fatal("restart left locally approved intent unfinished", err)
	}
	n.mu.Lock()
	recipientFetches := n.fetches["b"+nextURL]
	n.mu.Unlock()
	if recipientFetches != 0 {
		t.Fatal("unapproved recipient fetched proposed service")
	}
}

func approveSyntheticMigration(t *testing.T, a, b *registryPeer, id, url string) {
	t.Helper()
	local, err := a.app.BeginRelayChange(id, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.app.CompleteRelayProposal(id, local.RequestID, 900); err != nil {
		t.Fatal(err)
	}
	proposal, err := a.app.ExportRelayChange(id)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := b.app.ImportRelayChange(id, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.app.ApproveRelayChange(id, remote.RequestID); err != nil {
		t.Fatal(err)
	}
	approval, err := b.app.ExportRelayChange(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.app.ImportRelayChange(id, approval); err != nil {
		t.Fatal(err)
	}
}

func TestParallelApprovedRouteHandshakesHaveOneApplicationClaim(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	network := &routeTestNetwork{}
	openRouteRegistry(t, a, network, "a")
	openRouteRegistry(t, b, network, "b")
	connectRoutePair(t, a, b, network, id)
	a.registry.Close()
	b.registry.Close()
	approveSyntheticMigration(t, a, b, id, "https://parallel-relay.example/map.json")
	m, err := NewContact(a.local, a.app, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	routes, err := m.approvedRoutes()
	if err != nil || len(routes) != 2 {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.planKey = routePlanKey(routes)
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	type serverResult struct {
		index int
		err   error
	}
	results := make(chan serverResult, 2)
	peers := make([]*sessionauth.Session, 2)
	var joined sync.WaitGroup
	for i, route := range routes {
		left, right := net.Pipe()
		joined.Add(1)
		go func(index int, route approvedRoute) {
			defer joined.Done()
			results <- serverResult{index, m.sessionRoute(ctx, left, route)}
		}(i, route)
		peer, err := sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: b.local, Peer: a.local.PublicKey(), ContactID: id, Association: route.association})
		if err == nil {
			peers[i] = peer
		} else {
			_ = right.Close()
		}
	}
	t.Cleanup(func() {
		cancel()
		for _, peer := range peers {
			if peer != nil {
				_ = peer.Close()
			}
		}
		joined.Wait()
	})
	type readResult struct {
		index int
		p     packet
		err   error
	}
	reads := make(chan readResult, 2)
	for i, peer := range peers {
		go func(i int, peer *sessionauth.Session) {
			if peer == nil {
				reads <- readResult{index: i, err: ErrUnavailable}
				return
			}
			_ = peer.SetReadDeadline(time.Now().Add(time.Second))
			p, err := readPacket(peer)
			reads <- readResult{i, p, err}
		}(i, peer)
	}
	winners := 0
	for range 2 {
		select {
		case got := <-reads:
			if got.err == nil {
				if got.p.kind != profilePacket {
					t.Fatal("unexpected first application frame")
				}
				winners++
			}
		case <-ctx.Done():
			t.Fatal("parallel handshake reader did not finish")
		}
	}
	if winners != 1 {
		t.Fatal("parallel routes admitted zero or multiple application sessions")
	}
	select {
	case got := <-results:
		if !errors.Is(got.err, errRouteBusy) {
			t.Fatal("losing route was not rejected after authentication", got.err)
		}
	case <-ctx.Done():
		t.Fatal("losing route did not finish")
	}
	m.mu.Lock()
	live, claim, raw, pending := m.sessionActive, m.claim, m.raw, len(m.attempts)
	m.mu.Unlock()
	if !live || claim == nil || raw == nil || pending != 1 {
		t.Fatal("losing teardown cleared the winning session")
	}
}

func TestDelayedNewListenerDoesNotRegisterParallelOldEngine(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	network := &routeTestNetwork{}
	const nextURL = "https://delayed-relay.example/map.json"
	entered, release := make(chan struct{}, 1), make(chan struct{})
	factories := network.factories("a")
	baseListen := factories.Listen
	factories.Listen = func(ctx context.Context, r storage.PendingRecord, p *transport.RelayProfile) (Listener, error) {
		if r.RelayServiceURL == nextURL {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return baseListen(ctx, r, p)
	}
	var err error
	a.registry, err = NewRegistry(a.local, a.app, RegistryOptions{Factories: factories})
	if err != nil {
		t.Fatal(err)
	}
	openRouteRegistry(t, b, network, "b")
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, peer := range []*registryPeer{a, b} {
			manager, err := peer.registry.manager(id)
			if err != nil {
				continue
			}
			manager.mu.Lock()
			epoch := uint64(0)
			if manager.controls != nil {
				epoch = manager.controls.route.epoch
			}
			t.Logf("delayed listener runtime role=%s state=%s running=%t active=%t epoch=%d authentications=%d listeners=%d attempts=%d", manager.record.Role, manager.state, manager.running, manager.sessionActive, epoch, manager.authenticated, len(manager.listeners), len(manager.attempts))
			manager.mu.Unlock()
		}
		network.mu.Lock()
		t.Logf("remaining listeners new=%t old=%t active=%d", network.listeners[id+nextURL] != nil, network.listeners[id+"https://relay.example/map.json"] != nil, network.active[id])
		network.mu.Unlock()
	})
	connectRoutePair(t, a, b, network, id)
	if _, err := a.app.BeginRelayChange(id, nextURL); err != nil {
		t.Fatal(err)
	}
	if err := a.registry.RelayChanged(id); err != nil {
		t.Fatal(err)
	}
	proposal := waitRelayState(t, b, id, "awaiting-approval")
	if _, err := b.app.ApproveRelayChange(id, proposal.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := b.registry.RelayChanged(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("new listener did not start")
	}
	network.mu.Lock()
	oldStillRunning := network.active[id] != 0
	network.mu.Unlock()
	if oldStillRunning {
		t.Fatal("new listener setup raced an old engine using the same node key")
	}
	close(release)
	// This transport fixture supplies nil profiles and therefore cannot model
	// an unexpired profile surviving a lost final receipt. Completion/replay is
	// covered by the actual Tailcat test; this check owns delayed setup only.
	limit := time.Now().Add(3 * time.Second)
	for {
		network.mu.Lock()
		ready := network.listeners[id+nextURL] != nil
		peak := network.peak[id]
		network.mu.Unlock()
		if ready {
			if peak != 1 {
				t.Fatal("released setup registered more than one physical engine")
			}
			break
		}
		if time.Now().After(limit) {
			t.Fatal("released preferred listener did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestForgetJoinsPendingLocalIntentAndCannotCreateLateProposal(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	network := &routeTestNetwork{}
	const nextURL = "https://blocked-relay.example/map.json"
	entered := make(chan struct{}, 1)
	factories := network.factories("a")
	original := factories.Profile
	factories.Profile = func(ctx context.Context, url string) (*transport.RelayProfile, error) {
		if url == nextURL {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return original(ctx, url)
	}
	var err error
	a.registry, err = NewRegistry(a.local, a.app, RegistryOptions{Factories: factories})
	if err != nil {
		t.Fatal(err)
	}
	openRouteRegistry(t, b, network, "b")
	connectRoutePair(t, a, b, network, id)
	if _, err := a.app.BeginRelayChange(id, nextURL); err != nil {
		t.Fatal(err)
	}
	if err := a.registry.RelayChanged(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("locally approved map fetch did not start")
	}
	records, err := a.app.CommittedContacts()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- a.app.ForgetCommittedContact(records[0], func() error { return a.registry.Retire(id) })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Forget did not cancel/join local intent")
	}
	if _, err := a.app.RelayChange(id); err == nil {
		t.Fatal("late intent recreated deleted route state")
	}
	if _, err := a.registry.Connect(id); err == nil {
		t.Fatal("late local worker recreated forgotten runtime")
	}
}

func TestRelayIntentChangedInWorkerExitWindowIsCoalesced(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	network := &routeTestNetwork{}
	openRouteRegistry(t, a, network, "a")
	openRouteRegistry(t, b, network, "b")
	connectRoutePair(t, a, b, network, id)
	m, err := a.registry.manager(id)
	if err != nil {
		t.Fatal(err)
	}
	limit := time.Now().Add(time.Second)
	for {
		m.mu.Lock()
		idle := !m.intentRunning
		if idle {
			m.intentRunning = true
		}
		m.mu.Unlock()
		if idle {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("initial intent check did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	// Model the old worker having observed no durable intent, just before its
	// actual deferred completion executes. The next explicit action must win.
	const nextURL = "https://exit-window-relay.example/map.json"
	if _, err := a.app.BeginRelayChange(id, nextURL); err != nil {
		t.Fatal(err)
	}
	if err := a.registry.RelayChanged(id); err != nil {
		t.Fatal(err)
	}
	m.finishRelayIntent()
	got := waitRelayState(t, a, id, "awaiting-approval")
	if got.ProposedServiceURL != nextURL {
		t.Fatal("exit window lost or replaced the newer local intent")
	}
}

func TestOfflineApprovalAndCancelRefreshAlreadyAcceptingInviter(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	network := &routeTestNetwork{}
	openRouteRegistry(t, a, network, "a")
	openRouteRegistry(t, b, network, "b")
	connectRoutePair(t, a, b, network, id)
	a.registry.Close()
	b.registry.Close()
	openRouteRegistry(t, a, network, "a-offline")
	if _, err := a.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	oldURL := "https://relay.example/map.json"
	limit := time.Now().Add(time.Second)
	for {
		network.mu.Lock()
		ready := network.listeners[id+oldURL] != nil
		network.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("old inviter was not accepting")
		}
		time.Sleep(time.Millisecond)
	}
	const nextURL = "https://offline-approved-relay.example/map.json"
	approveSyntheticMigration(t, a, b, id, nextURL)
	if err := a.registry.RelayChanged(id); err != nil {
		t.Fatal(err)
	}
	limit = time.Now().Add(3 * time.Second)
	for {
		network.mu.Lock()
		ready := network.listeners[id+nextURL] != nil
		network.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("already-running inviter did not install manually approved route without a peer")
		}
		time.Sleep(time.Millisecond)
	}
	change, err := a.app.RelayChange(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.app.CancelRelayChange(id, change.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := a.registry.RelayChanged(id); err != nil {
		t.Fatal(err)
	}
	limit = time.Now().Add(3 * time.Second)
	for {
		network.mu.Lock()
		newGone := network.listeners[id+nextURL] == nil
		oldReady := network.listeners[id+oldURL] != nil
		network.mu.Unlock()
		if newGone && oldReady {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("cancelled pending route remained listening without a peer")
		}
		time.Sleep(time.Millisecond)
	}
}
