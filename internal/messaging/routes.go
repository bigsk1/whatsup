package messaging

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"whatsup/internal/diagnostics"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

var (
	errRoutesChanged = errors.New("approved contact routes changed")
	errRouteBusy     = errors.New("another authenticated contact stream is active")
	// Only a typed local refresh may retire a generation without consuming the
	// automatic unchanged-plan restart budget. Peer frames cannot mint this.
	errLocalRouteRefresh = fmt.Errorf("%w: explicit local refresh", errRoutesChanged)
	// Only this result represents a peer asking to rebuild an unchanged plan.
	// Local map failures and strategy changes follow their own normal retry path.
	errPeerRouteReady = fmt.Errorf("%w: peer route ready", errRoutesChanged)
)

const overlappingRouteWindow = 25 * time.Second

func routeFailure(err error) error {
	if err == nil {
		return ErrUnavailable
	}
	return err
}

func (m *Manager) routePlanChanged() (bool, error) {
	m.mu.Lock()
	approved, previous := m.conversation != nil, m.planKey
	m.mu.Unlock()
	if !approved {
		return false, nil
	}
	key, err := m.currentPlanKey()
	return key != previous, err
}

type approvedRoute struct {
	candidate   *storage.RouteCandidate
	record      storage.PendingRecord
	association [32]byte
	epoch       uint64
}

func (r approvedRoute) validate() error {
	if r.candidate != nil {
		return r.candidate.Validate()
	}
	return storage.ValidatePendingRecord(r.record)
}

type routeAttempt struct{ raw net.Conn }
type routeListener struct {
	listener Listener
	route    approvedRoute
}
type routeClaim struct {
	attempt *routeAttempt
	route   approvedRoute
}
type routeRestartRequest struct {
	ctx  context.Context
	done chan struct{}
	once sync.Once
}

func (r *routeRestartRequest) finish() {
	if r != nil && r.done != nil {
		r.once.Do(func() { close(r.done) })
	}
}

func sameBaseRoute(base, route storage.PendingRecord) bool {
	route.RelayServiceURL, route.ServerEndpoint = base.RelayServiceURL, base.ServerEndpoint
	return route == base
}

func (m *Manager) approvedRoutes() ([]approvedRoute, error) {
	m.mu.Lock()
	if m.closed || m.refreshLocked() != nil || m.peerRemoved || m.removing {
		m.mu.Unlock()
		return nil, ErrUnavailable
	}
	base, localApproval := m.record, m.conversation != nil
	m.mu.Unlock()
	if !localApproval {
		// Only unfinished A0 activation uses pairing reconfirmation. A failed
		// migrated plan must never silently fall back to the historical route.
		binding, err := storage.ContactBinding(base)
		if err != nil {
			return nil, err
		}
		return []approvedRoute{{record: base, association: binding}}, nil
	}
	plan, err := m.application.ConnectionPlan(base.ID)
	if err != nil {
		return nil, err
	}
	if len(plan.Candidates) < 1 || len(plan.Candidates) > 2 {
		return nil, ErrUnavailable
	}
	change, err := m.application.RelayChange(base.ID)
	if err != nil {
		return nil, err
	}
	routes := make([]approvedRoute, 0, len(plan.Candidates))
	seen := map[[32]byte]bool{}
	for i := range plan.Candidates {
		candidate := plan.Candidates[i]
		record, err := candidate.Record()
		association := candidate.Association()
		if err != nil {
			return nil, routeFailure(err)
		}
		if err = candidate.Validate(); err != nil {
			return nil, routeFailure(err)
		}
		if !sameBaseRoute(base, record) || association == [32]byte{} || seen[association] {
			return nil, ErrUnavailable
		}
		seen[association] = true
		routes = append(routes, approvedRoute{candidate: &candidate, record: record, association: association, epoch: candidate.Epoch()})
	}
	m.mu.Lock()
	// Cache entries are immutable and remain eligible only for an exact live
	// URL/TTL. A route transition cannot grow a third approved profile slot.
	for url := range m.profiles {
		keep := change.LocalApproved && change.ProposedServiceURL == url
		for _, r := range routes {
			keep = keep || r.record.RelayServiceURL == url
		}
		if !keep {
			delete(m.profiles, url)
		}
	}
	m.mu.Unlock()
	return routes, nil
}

func (m *Manager) relayProfile(ctx context.Context, url string) (*transport.RelayProfile, error) {
	m.mu.Lock()
	cached := m.profiles[url]
	m.mu.Unlock()
	if cached.ReadyFor(url) {
		return cached, nil
	}
	profile, err := m.factories.Profile(ctx, url)
	if err != nil {
		if ctx.Err() == nil {
			m.report(diagnostics.RelayFetchFailed)
		}
		return nil, err
	}
	m.mu.Lock()
	if !m.closed && profile.ReadyFor(url) {
		m.rememberRelayProfileLocked(url, profile)
	}
	m.mu.Unlock()
	return profile, nil
}

// A fresh locally consented service can be prepared before the next route-plan
// rebuild prunes retired entries. Retain that validated fetch immediately rather
// than repeatedly fetching it while both older slots remain occupied. FIFO is
// bounded to two entries; callers still require exact URL and original TTL.
func (m *Manager) rememberRelayProfileLocked(url string, profile *transport.RelayProfile) {
	if m.profiles == nil {
		m.profiles = map[string]*transport.RelayProfile{}
	}
	order := m.profileOrder[:0]
	for _, key := range m.profileOrder {
		if _, present := m.profiles[key]; present && key != url {
			order = append(order, key)
		}
	}
	// Accommodate entries retained by older state/test construction without
	// relying on map iteration order for ordinary FIFO-managed entries.
	for key := range m.profiles {
		if key == url {
			continue
		}
		present := false
		for _, old := range order {
			present = present || old == key
		}
		if !present {
			order = append(order, key)
		}
	}
	if _, exists := m.profiles[url]; !exists {
		for len(m.profiles) >= 2 {
			delete(m.profiles, order[0])
			order = order[1:]
		}
	}
	m.profiles[url] = profile
	m.profileOrder = append(order, url)
}

func (m *Manager) requestRouteRefresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !m.running {
		return
	}
	select {
	case m.routeRestart <- &routeRestartRequest{ctx: m.ctx}:
	default:
	}
}

// RefreshRoutes retires only this contact's route generation. Local approval,
// history, drafts and the Manager generation remain intact. The old workers are
// joined before run starts any replacement listener or client.
func (r *Registry) RefreshRoutes(id string) error {
	if r == nil || r.registryState == nil {
		return ErrUnavailable
	}
	r.mu.Lock()
	m := r.managers[id]
	invalid := r.closed || r.retired[id]
	r.mu.Unlock()
	if invalid || m == nil {
		return ErrUnavailable
	}
	return m.refreshRoutes()
}
func (m *Manager) refreshRoutes() error {
	m.routeRefresh.Lock()
	defer m.routeRefresh.Unlock()
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	defer cancel()
	m.mu.Lock()
	if m.closed || m.peerRemoved || m.removing {
		m.mu.Unlock()
		return ErrUnavailable
	}
	if !m.running {
		m.bumpLocked()
		m.mu.Unlock()
		return nil
	}
	runDone := m.runDone
	request := &routeRestartRequest{ctx: ctx, done: make(chan struct{})}
	select {
	case m.routeRestart <- request:
	default:
	}
	cycleDone := m.routeDone
	m.mu.Unlock()
	select {
	case <-request.done:
		return nil
	case <-cycleDone:
		return nil
	case <-runDone:
		return nil
	case <-ctx.Done():
		return ErrUnavailable
	}
}

func (m *Manager) runRouteCycle() error {
	for {
		select {
		case request := <-m.routeRestart:
			request.finish()
		default:
			goto drained
		}
	}
drained:
	routes, err := m.approvedRoutes()
	if err != nil {
		return err
	}
	life, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})
	m.mu.Lock()
	m.planKey = routePlanKey(routes)
	m.preferredRoute = routes[0].association
	m.routeCancel = cancel
	m.routeDone = done
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		if m.routeDone == done {
			m.routeCancel = nil
			m.routeDone = nil
		}
		m.mu.Unlock()
		close(done)
	}()
	result := make(chan error, 1)
	go func() {
		if m.record.Role == "inviter" {
			result <- m.runListeners(life, routes)
		} else {
			result <- m.runClient(life, routes)
		}
	}()
	select {
	case err := <-result:
		return err
	case request := <-m.routeRestart:
		if request.ctx.Err() != nil {
			request.finish()
			cancel()
			<-result
			return errLocalRouteRefresh
		}
		cancel()
		<-result
		request.finish()
		return errLocalRouteRefresh
	case <-m.ctx.Done():
		cancel()
		<-result
		return m.ctx.Err()
	}
}

func (m *Manager) runClient(ctx context.Context, routes []approvedRoute) error {
	var last error = ErrUnavailable
	for _, route := range routes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := route.validate(); err != nil {
			return routeFailure(err)
		}
		profile, err := m.relayProfile(ctx, route.record.RelayServiceURL)
		if err != nil {
			last = err
			continue
		}
		raw, err := m.factories.Dial(ctx, route.record, profile)
		if err != nil {
			if ctx.Err() == nil {
				m.report(diagnostics.DialFailed)
			}
			last = err
			continue
		}
		before := m.authenticated
		err = m.sessionRoute(ctx, raw, route)
		if terminalSession(err) || m.authenticated != before {
			return err
		}
		last = err
	}
	return last
}

func (m *Manager) runListeners(ctx context.Context, routes []approvedRoute) error {
	if len(routes) == 2 {
		first, firstErr := m.relayProfile(ctx, routes[0].record.RelayServiceURL)
		second, secondErr := m.relayProfile(ctx, routes[1].record.RelayServiceURL)
		if firstErr == nil && secondErr == nil && first != nil && second != nil {
			equivalent, _, err := transport.OngoingListenerRelation(listenerOptions(routes[0], first), listenerOptions(routes[1], second))
			if err != nil {
				return err
			}
			if equivalent {
				return m.listenRoute(ctx, routes[0], routes[1])
			}
		}
		// Disjoint endpoint maps can still name meshed DERP servers belonging
		// to one operator. Only exact equivalence is safe to coalesce; all
		// other or unavailable profiles use one physical engine at a time.
		return m.runOverlappingListeners(ctx, routes)
	}
	if len(routes) != 1 {
		return ErrUnavailable
	}
	return m.listenRoute(ctx, routes[0])
}

func listenerOptions(route approvedRoute, profile *transport.RelayProfile) transport.OngoingOptions {
	return transport.OngoingOptions{Record: route.record, Profile: profile, Policy: transport.RelayFallback, AllowRelayFallback: true}
}

// Two different approved maps can contain the same physical or meshed DERP
// service. Duplicate WireGuard node registrations make packet delivery ambiguous.
// Different maps cannot share TLS route authority, so visit them serially. Keep
// any authenticated stream. Migration must wait for a natural disconnect when
// visiting the other map would otherwise interrupt a working fallback.
func (m *Manager) runOverlappingListeners(ctx context.Context, routes []approvedRoute) error {
	return m.visitOverlappingListeners(ctx, routes, overlappingRouteWindow)
}

func (m *Manager) visitOverlappingListeners(ctx context.Context, routes []approvedRoute, window time.Duration) error {
	if len(routes) != 2 || window <= 0 {
		return ErrUnavailable
	}
	for ctx.Err() == nil {
		for _, route := range routes {
			if err := route.validate(); err != nil {
				return routeFailure(err)
			}
			profile, err := m.relayProfile(ctx, route.record.RelayServiceURL)
			if err != nil {
				continue
			}
			life, cancel := context.WithCancel(ctx)
			// Bound idle setup/accept stages, while sparing an authenticated claim
			// on either route. Availability takes priority over trying a preferred
			// map that cannot safely run a second physical listener concurrently.
			timer := time.AfterFunc(window, func() {
				m.mu.Lock()
				activeClaim := m.claim != nil && m.claim.route.association == route.association
				m.mu.Unlock()
				if !activeClaim {
					cancel()
				}
			})
			err = m.serveRouteWithin(life, route, profile, window)
			timer.Stop()
			cancel()
			if terminalSession(err) || errors.Is(err, errRoutesChanged) {
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		// A profile that was unavailable on initial classification may now be
		// cached. Rebuild into the equivalent strategy when possible.
		m.mu.Lock()
		first, second := m.profiles[routes[0].record.RelayServiceURL], m.profiles[routes[1].record.RelayServiceURL]
		m.mu.Unlock()
		if first.ReadyFor(routes[0].record.RelayServiceURL) && second.ReadyFor(routes[1].record.RelayServiceURL) {
			equivalent, _, err := transport.OngoingListenerRelation(listenerOptions(routes[0], first), listenerOptions(routes[1], second))
			if err != nil {
				return err
			}
			if equivalent {
				return errLocalRouteRefresh
			}
		}
		timer := time.NewTimer(m.reconnectDelay(time.Now()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (m *Manager) listenRoute(ctx context.Context, route approvedRoute, alternatives ...approvedRoute) error {
	for ctx.Err() == nil {
		if err := route.validate(); err != nil {
			return routeFailure(err)
		}
		profile, err := m.relayProfile(ctx, route.record.RelayServiceURL)
		if err == nil {
			for _, alternative := range alternatives {
				if err = alternative.validate(); err != nil {
					return routeFailure(err)
				}
				other, fetchErr := m.relayProfile(ctx, alternative.record.RelayServiceURL)
				if fetchErr != nil {
					return fetchErr
				}
				equivalent, _, compareErr := transport.OngoingListenerRelation(listenerOptions(route, profile), listenerOptions(alternative, other))
				if compareErr != nil {
					return compareErr
				}
				if !equivalent {
					return errLocalRouteRefresh
				}
			}
			err = m.serveRoute(ctx, route, profile, alternatives...)
		}
		if terminalSession(err) || errors.Is(err, errRoutesChanged) {
			return err
		}
		m.mu.Lock()
		if !m.closed && !m.sessionActive && m.state != "failed" {
			m.state = retryState(err)
			m.bumpLocked()
		}
		m.mu.Unlock()
		timer := time.NewTimer(m.reconnectDelay(time.Now()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (m *Manager) serveRoute(ctx context.Context, route approvedRoute, profile *transport.RelayProfile, alternatives ...approvedRoute) error {
	return m.serveRouteWithin(ctx, route, profile, 0, alternatives...)
}

func (m *Manager) serveRouteWithin(ctx context.Context, route approvedRoute, profile *transport.RelayProfile, window time.Duration, alternatives ...approvedRoute) error {
	if err := route.validate(); err != nil {
		return routeFailure(err)
	}
	listener, err := m.factories.Listen(ctx, route.record, profile)
	if err != nil {
		if ctx.Err() == nil {
			m.report(diagnostics.DialFailed)
		}
		return err
	}
	owned := &routeListener{listener: listener, route: route}
	m.mu.Lock()
	if m.closed || ctx.Err() != nil {
		m.mu.Unlock()
		_ = listener.Close()
		return ErrUnavailable
	}
	if m.listeners == nil {
		m.listeners = map[*routeListener]struct{}{}
	}
	m.listeners[owned] = struct{}{}
	select {
	case m.wake <- struct{}{}:
	default:
	}
	m.mu.Unlock()
	defer func() { _ = listener.Close(); m.mu.Lock(); delete(m.listeners, owned); m.mu.Unlock() }()
	for ctx.Err() == nil {
		changed, err := m.routePlanChanged()
		if err != nil {
			return routeFailure(err)
		}
		if changed {
			return errRoutesChanged
		}
		accept, cancel := ctx, func() {}
		if window > 0 {
			accept, cancel = context.WithTimeout(ctx, window)
		}
		raw, err := listener.Accept(accept)
		cancel()
		if err != nil {
			return err
		}
		err = m.sessionRouteCandidates(ctx, raw, route, alternatives...)
		if terminalSession(err) || errors.Is(err, errRoutesChanged) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A local change can race an exchange's final wake. Check before the
		// next blocking Accept so a healthy old listener cannot strand it.
		changed, err = m.routePlanChanged()
		if err != nil {
			return routeFailure(err)
		}
		if changed {
			return errRoutesChanged
		}
		m.mu.Lock()
		if !m.sessionActive && !m.closed {
			m.state = retryState(err)
			m.bumpLocked()
		}
		m.mu.Unlock()
		// Production NewOngoingListener rejects nil profiles before any IO.
		// Custom in-memory Go transport factories use nil to model a listener
		// without an operator map; do not invent an expiry for that fixture.
		if profile != nil && !profile.Ready() {
			return ErrUnavailable
		}
	}
	return ctx.Err()
}
