package messaging

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"
)

const (
	routeReadyControl        byte = 16
	relayContributionControl byte = 17
	relayCommitControl       byte = 18
	relayReceiptControl      byte = 19
)

// RelayChanged follows a typed, durable local storage mutation. It never
// approves a URL: pending intent and every contribution are reloaded from Go.
func (r *Registry) RelayChanged(id string) error {
	m, err := r.manager(id)
	if err != nil {
		return err
	}
	m.startRelayIntent()
	m.MetadataChanged()
	m.mu.Lock()
	needsRestart := m.running && (!m.sessionActive || !m.capabilities.Control)
	previous := m.planKey
	m.mu.Unlock()
	if needsRestart {
		if current, err := m.currentPlanKey(); err == nil && current != previous {
			m.requestRouteRefresh()
		}
	}
	return nil
}

func (m *Manager) finishRelayIntent() {
	m.mu.Lock()
	m.intentRunning = false
	again := m.intentAgain && !m.closed && !m.peerRemoved && !m.removing
	m.intentAgain = false
	m.mu.Unlock()
	if again {
		m.startRelayIntent()
	}
}

func (m *Manager) startRelayIntent() {
	m.mu.Lock()
	if m.closed || m.peerRemoved || m.removing || m.ctx == nil || m.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	if m.intentRunning {
		m.intentAgain = true
		m.mu.Unlock()
		return
	}
	m.intentRunning = true
	m.workers.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.workers.Done()
		defer m.finishRelayIntent()
		for attempt := 0; m.ctx.Err() == nil; attempt++ {
			intent, err := m.application.PendingRelayIntent(m.record.ID)
			if terminalPersistence(err) {
				m.mu.Lock()
				m.failLocked()
				m.mu.Unlock()
				return
			}
			if err != nil || !intent.NeedsRegion || intent.RequestID == "" {
				return
			}
			profile, err := m.relayProfile(m.ctx, intent.ServiceURL)
			if err == nil {
				region, selectionErr := m.factories.PreferredRegion(profile)
				if selectionErr == nil && m.ctx.Err() == nil {
					if _, err = m.application.CompleteRelayProposal(m.record.ID, intent.RequestID, uint32(region)); err == nil {
						m.MetadataChanged()
						return
					}
					if terminalPersistence(err) {
						m.mu.Lock()
						m.failLocked()
						m.mu.Unlock()
						return
					}
				}
			}
			timer := time.NewTimer(retryDelay(attempt))
			select {
			case <-m.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

func (m *Manager) currentPlanKey() ([32]byte, error) {
	plan, err := m.application.ConnectionPlan(m.record.ID)
	if err != nil {
		return [32]byte{}, err
	}
	if len(plan.Candidates) < 1 || len(plan.Candidates) > 2 {
		return [32]byte{}, ErrUnavailable
	}
	var wire []byte
	for _, candidate := range plan.Candidates {
		if err := candidate.Validate(); err != nil {
			return [32]byte{}, err
		}
		a := candidate.Association()
		wire = append(wire, a[:]...)
		wire = append(wire, candidate.RequestID()...)
		wire = append(wire, 0)
	}
	return sha256.Sum256(wire), nil
}

func routePlanKey(routes []approvedRoute) [32]byte {
	var wire []byte
	for _, route := range routes {
		wire = append(wire, route.association[:]...)
		if route.candidate != nil {
			wire = append(wire, route.candidate.RequestID()...)
		}
		wire = append(wire, 0)
	}
	return sha256.Sum256(wire)
}

func relayFrame(kind byte, body []byte) (controlFrame, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil || token == [16]byte{} {
		return controlFrame{}, ErrUnavailable
	}
	return controlFrame{kind: kind, id: base64.RawURLEncoding.EncodeToString(token[:]), body: bytes.Clone(body)}, nil
}

type relayExchange struct {
	manager  *Manager
	control  *controlSession
	sent     map[[32]byte]bool
	replies  map[[32]byte]bool
	ready    [32]byte
	budgetAt time.Time
	budget   int
}

func (x *relayExchange) live() bool {
	m := x.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.closed && !m.peerRemoved && !m.removing && m.sessionActive && m.controls == x.control && x.control.session.Capabilities().Control
}

func (x *relayExchange) write(kind byte, body []byte) error {
	if !x.live() {
		return ErrUnavailable
	}
	f, err := relayFrame(kind, body)
	if err != nil {
		return err
	}
	return writePacket(x.control.session, packet{kind: controlPacket, control: f})
}

func (x *relayExchange) removing() bool {
	x.manager.mu.Lock()
	defer x.manager.mu.Unlock()
	return x.manager.removing
}

func (x *relayExchange) sync() (result error) {
	// Local Forget's priority request supersedes optional relay synchronization.
	// Yield to the writer loop instead of closing its newly authenticated stream.
	defer func() {
		if errors.Is(result, ErrUnavailable) && x.removing() {
			result = nil
		}
	}()
	if x.removing() {
		return nil
	}
	if !x.control.session.Capabilities().Control || x.manager.record.ID == "" || x.control.proof == nil {
		return nil
	}
	if !x.live() {
		return ErrUnavailable
	}
	m := x.manager
	contributions, err := m.application.RelayContributions(m.record.ID)
	if err != nil || len(contributions) > 4 {
		if err != nil {
			return err
		}
		return ErrUnavailable
	}
	for _, body := range contributions {
		if len(body) == 0 || len(body) > maxControlBody {
			return errProtocol
		}
		hash := sha256.Sum256(body)
		if !x.sent[hash] {
			if err := x.write(relayContributionControl, body); err != nil {
				return err
			}
			x.sent[hash] = true
		}
	}
	if x.control.route.epoch > 0 && x.control.proof != nil {
		body, err := m.application.CreateRelayCommit(*x.control.proof)
		if terminalPersistence(err) {
			return err
		}
		if err == nil && len(body) > 0 {
			if len(body) > 512 {
				return errProtocol
			}
			hash := sha256.Sum256(body)
			if !x.sent[hash] {
				if err := x.write(relayCommitControl, body); err != nil {
					return err
				}
				x.sent[hash] = true
				m.MetadataChanged()
			}
		}
	}
	if m.record.Role == "inviter" {
		var ready *approvedRoute
		m.mu.Lock()
		for owned := range m.listeners {
			route := owned.route
			if route.epoch > x.control.route.epoch && (ready == nil || route.epoch > ready.epoch) {
				copy := route
				ready = &copy
			}
		}
		m.mu.Unlock()
		if ready != nil && ready.validate() == nil && ready.association != x.ready {
			body := binary.BigEndian.AppendUint64(nil, ready.epoch)
			body = append(body, ready.association[:]...)
			if ready.candidate == nil {
				return errProtocol
			}
			nonce, err := base64.RawURLEncoding.Strict().DecodeString(ready.candidate.RequestID())
			if err != nil || len(nonce) != 16 {
				return errProtocol
			}
			body = append(body, nonce...)
			if err := x.write(routeReadyControl, body); err != nil {
				return err
			}
			x.ready = ready.association
		}
	}
	key, err := m.currentPlanKey()
	if err != nil {
		return routeFailure(err)
	}
	m.mu.Lock()
	changed := key != m.planKey
	m.mu.Unlock()
	if changed {
		return errRoutesChanged
	}
	return nil
}

func (x *relayExchange) receive(f controlFrame) error {
	if !x.live() {
		return ErrUnavailable
	}
	now := time.Now()
	if x.budgetAt.IsZero() || now.Sub(x.budgetAt) >= 10*time.Second {
		x.budgetAt = now
		x.budget = 0
	}
	if x.budget >= 32 {
		return errProtocol
	}
	x.budget++
	m := x.manager
	switch f.kind {
	case routeReadyControl:
		if m.record.Role != "joiner" || len(f.body) != 56 {
			return errProtocol
		}
		epoch := binary.BigEndian.Uint64(f.body[:8])
		var association [32]byte
		copy(association[:], f.body[8:40])
		requestID := base64.RawURLEncoding.EncodeToString(f.body[40:])
		plan, err := m.application.ConnectionPlan(m.record.ID)
		if err != nil {
			return routeFailure(err)
		}
		for _, candidate := range plan.Candidates {
			if candidate.Epoch() == epoch && candidate.Association() == association && candidate.RequestID() == requestID && candidate.Validate() == nil {
				if epoch > x.control.route.epoch {
					return errPeerRouteReady
				}
				return nil
			}
		}
		return errProtocol
	case relayContributionControl:
		// Signed contributions also support manual offline carriage. A queued
		// valid contribution remains verifiable after a peer's orderly FIN.
		if _, err := m.application.ImportRelayContribution(m.record.ID, f.body); err != nil {
			if terminalPersistence(err) {
				return err
			}
			return errProtocol
		}
		m.MetadataChanged()
		return x.sync()
	case relayCommitControl, relayReceiptControl:
		if x.control.proof == nil || x.control.route.epoch == 0 || x.control.proof.Validate() != nil {
			return errProtocol
		}
		if _, err := m.application.AcceptRelayCommit(*x.control.proof, f.body); err != nil {
			if terminalPersistence(err) {
				return err
			}
			return errProtocol
		}
		m.MetadataChanged()
		if f.kind == relayCommitControl {
			hash := sha256.Sum256(f.body)
			if !x.replies[hash] {
				body, err := m.application.CreateRelayCommit(*x.control.proof)
				if err != nil || len(body) == 0 {
					return errProtocol
				}
				if err := x.write(relayReceiptControl, body); err != nil {
					return err
				}
				x.replies[hash] = true
			}
		}
		return x.sync()
	}
	return errProtocol
}
