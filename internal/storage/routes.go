package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"github.com/tailscale/tailcat"
	"math"
	"net"
	"net/url"
	"strings"
	"tailscale.com/tailcfg"
	"whatsup/internal/identity"
	"whatsup/internal/relaychange"
	"whatsup/internal/transport/address"
)

var ErrRelayChange = errors.New("relay change is invalid or unavailable")

type relayRoute struct {
	ServiceURL string `json:"serviceURL"`
	RegionID   uint32 `json:"regionID"`
}
type routeSigned struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

func diskSigned(s relaychange.Signed) *routeSigned {
	return &routeSigned{bytes.Clone(s.Payload), bytes.Clone(s.Signature)}
}
func (s *routeSigned) signed() relaychange.Signed {
	if s == nil {
		return relaychange.Signed{}
	}
	return relaychange.Signed{Payload: bytes.Clone(s.Payload), Signature: bytes.Clone(s.Signature)}
}

type routeAgreement struct {
	Proposal *routeSigned `json:"proposal"`
	Approval *routeSigned `json:"approval"`
}
type routePending struct {
	RequestID     string       `json:"requestID"`
	ServiceURL    string       `json:"serviceURL"`
	LocalIntent   bool         `json:"localIntent"`
	Proposal      *routeSigned `json:"proposal"`
	Approval      *routeSigned `json:"approval"`
	Authenticated bool         `json:"authenticated"`
	LocalCommit   *routeSigned `json:"localCommit"`
}
type routeCompletion struct {
	Agreement   routeAgreement `json:"agreement"`
	LocalCommit *routeSigned   `json:"localCommit"`
	PeerCommit  *routeSigned   `json:"peerCommit"`
}
type routeStateDisk struct {
	ContactID string           `json:"contactID"`
	Base      [32]byte         `json:"base"`
	Epoch     uint64           `json:"epoch"`
	Active    relayRoute       `json:"active"`
	Agreement *routeAgreement  `json:"agreement"`
	Pending   *routePending    `json:"pending"`
	Last      *routeCompletion `json:"last"`
	Outgoing  *routeSigned     `json:"outgoing"`
	Retired   []string         `json:"retired"`
	Revision  uint64           `json:"revision"`
	LastState string           `json:"lastState"`
}
type RelayChangeSnapshot struct {
	ContactID          string `json:"contactID"`
	Revision           uint64 `json:"revision"`
	RequestID          string `json:"requestID"`
	State              string `json:"state"`
	CurrentServiceURL  string `json:"currentServiceURL"`
	ProposedServiceURL string `json:"proposedServiceURL"`
	ProposedHost       string `json:"proposedHost"`
	LocalApproved      bool   `json:"localApproved"`
	PeerApproved       bool   `json:"peerApproved"`
	CanPropose         bool   `json:"canPropose"`
	CanApprove         bool   `json:"canApprove"`
}
type RelayIntent struct {
	RequestID   string
	ServiceURL  string
	NeedsRegion bool
}

func (RelayIntent) String() string               { return "[private locally approved relay intent]" }
func (RelayIntent) GoString() string             { return "[private locally approved relay intent]" }
func (RelayIntent) MarshalJSON() ([]byte, error) { return nil, ErrRelayChange }
func role(record PendingRecord) uint8 {
	if record.Role == "inviter" {
		return 1
	}
	return 2
}
func keyForRole(record PendingRecord, r uint8) identity.PublicKey {
	if r == role(record) {
		return record.Local
	}
	return record.Peer
}
func routeOf(record PendingRecord) (relayRoute, error) {
	e, err := address.Parse(record.ServerEndpoint)
	if err != nil {
		return relayRoute{}, ErrRelayChange
	}
	info, err := tailcat.ParseAddr(e.EncodeForTransport())
	if err != nil || !info.RegionID.IsValid() || info.RegionID.Int64() > math.MaxUint32 {
		return relayRoute{}, ErrRelayChange
	}
	return relayRoute{record.RelayServiceURL, uint32(info.RegionID.Int64())}, nil
}
func routeRecord(base PendingRecord, r relayRoute) (PendingRecord, error) {
	if ValidatePendingRecord(base) != nil || ValidateRelayServiceURL(r.ServiceURL) != nil || r.RegionID == 0 {
		return PendingRecord{}, ErrRelayChange
	}
	e, err := address.Parse(base.ServerEndpoint)
	if err != nil {
		return PendingRecord{}, ErrRelayChange
	}
	info, err := tailcat.ParseAddr(e.EncodeForTransport())
	if err != nil {
		return PendingRecord{}, ErrRelayChange
	}
	region := tailcfg.DERPRegionID(r.RegionID)
	if !region.IsValid() || region.Int64() != int64(r.RegionID) {
		return PendingRecord{}, ErrRelayChange
	}
	info.Region = nil
	info.RegionID = region
	base.RelayServiceURL = r.ServiceURL
	base.ServerEndpoint = string(info.Addr())
	if ValidatePendingRecord(base) != nil {
		return PendingRecord{}, ErrRelayChange
	}
	return base, nil
}
func routeBinding(base PendingRecord, epoch uint64, r relayRoute) ([32]byte, error) {
	a0, err := ContactBinding(base)
	if err != nil {
		return [32]byte{}, err
	}
	original, err := routeOf(base)
	if err != nil {
		return [32]byte{}, err
	}
	if epoch == 0 {
		if original != r {
			return [32]byte{}, ErrRelayChange
		}
		return a0, nil
	}
	record, err := routeRecord(base, r)
	if err != nil {
		return [32]byte{}, err
	}
	wire := []byte("WhatsUp approved route association v1\x00")
	wire = append(wire, a0[:]...)
	wire = binary.BigEndian.AppendUint64(wire, epoch)
	wire = binary.BigEndian.AppendUint16(wire, uint16(len(r.ServiceURL)))
	wire = append(wire, r.ServiceURL...)
	wire = binary.BigEndian.AppendUint32(wire, r.RegionID)
	wire = binary.BigEndian.AppendUint16(wire, uint16(len(record.ServerEndpoint)))
	wire = append(wire, record.ServerEndpoint...)
	binding := sha256.Sum256(wire)
	clear(wire)
	return binding, nil
}
func cloneRoute(r routeStateDisk) routeStateDisk {
	copySigned := func(s *routeSigned) *routeSigned {
		if s == nil {
			return nil
		}
		return diskSigned(s.signed())
	}
	copyAgreement := func(a *routeAgreement) *routeAgreement {
		if a == nil {
			return nil
		}
		return &routeAgreement{copySigned(a.Proposal), copySigned(a.Approval)}
	}
	r.Agreement = copyAgreement(r.Agreement)
	if r.Pending != nil {
		p := *r.Pending
		p.Proposal = copySigned(p.Proposal)
		p.Approval = copySigned(p.Approval)
		p.LocalCommit = copySigned(p.LocalCommit)
		r.Pending = &p
	}
	if r.Last != nil {
		p := *r.Last
		p.Agreement = *copyAgreement(&p.Agreement)
		p.LocalCommit = copySigned(p.LocalCommit)
		p.PeerCommit = copySigned(p.PeerCommit)
		r.Last = &p
	}
	r.Outgoing = copySigned(r.Outgoing)
	r.Retired = append([]string{}, r.Retired...)
	return r
}
func initialRoute(base PendingRecord) (routeStateDisk, error) {
	r, err := routeOf(base)
	if err != nil {
		return routeStateDisk{}, err
	}
	binding, err := ContactBinding(base)
	return routeStateDisk{ContactID: base.ID, Base: binding, Active: r, Retired: []string{}, Revision: 1}, err
}
func (a *ApplicationState) routeLocked(id string) (PendingRecord, routeStateDisk, int, error) {
	if err := a.usableLocked(); err != nil {
		return PendingRecord{}, routeStateDisk{}, -1, err
	}
	var base PendingRecord
	for _, r := range a.state.CommittedContacts {
		if r.ID == id {
			base = PendingRecord(r)
			break
		}
	}
	if base.Version != 2 || a.localConversationLocked(base) != nil {
		return base, routeStateDisk{}, -1, ErrRelayChange
	}
	if a.localReceipts == nil {
		a.localReceipts = map[string]*localContactReceiptState{}
	}
	if receipt := a.localReceipts[id]; receipt == nil {
		a.localReceipts[id] = &localContactReceiptState{application: a, record: base}
	} else if receipt.record != base {
		return base, routeStateDisk{}, -1, ErrRelayChange
	}
	removed, err := a.peerRemovedLocked(id)
	if err != nil {
		return base, routeStateDisk{}, -1, err
	}
	if removed {
		return base, routeStateDisk{}, -1, ErrRelayChange
	}
	for i, r := range a.state.Routes {
		if r.ContactID == id {
			return base, cloneRoute(r), i, nil
		}
	}
	r, err := initialRoute(base)
	return base, r, -1, err
}
func (a *ApplicationState) saveRouteLocked(r routeStateDisk, index int) error {
	if r.Revision >= math.MaxUint64 {
		return ErrRelayChange
	}
	r.Revision++
	next := a.cloneLocked()
	if index < 0 {
		next.Routes = append(next.Routes, cloneRoute(r))
	} else {
		next.Routes[index] = cloneRoute(r)
	}
	return a.commitLocked(next)
}
func statementContext(base PendingRecord, r routeStateDisk, p *routePending) (relaychange.Statement, error) {
	old, err := routeBinding(base, r.Epoch, r.Active)
	if err != nil || r.Epoch == math.MaxUint64 {
		return relaychange.Statement{}, ErrRelayChange
	}
	return relaychange.Statement{ContactID: base.ID, Base: r.Base, Old: old, Epoch: r.Epoch + 1, Nonce: p.RequestID, Proposer: role(base), Signer: role(base)}, nil
}
func contextEqual(a, b relaychange.Statement) bool {
	return a.ContactID == b.ContactID && a.Base == b.Base && a.Old == b.Old && a.Epoch == b.Epoch && a.Nonce == b.Nonce && a.Proposer == b.Proposer
}
func proposalStatement(base PendingRecord, proposal *routeSigned) (relaychange.Statement, error) {
	if proposal == nil {
		return relaychange.Statement{}, ErrRelayChange
	}
	statement, err := proposal.signed().Statement()
	if err != nil || statement.Kind != relaychange.Proposal {
		return relaychange.Statement{}, ErrRelayChange
	}
	if _, err := proposal.signed().Verify(keyForRole(base, statement.Signer)); err != nil {
		return relaychange.Statement{}, ErrRelayChange
	}
	a0, err := ContactBinding(base)
	if err != nil || statement.ContactID != base.ID || statement.Base != a0 || ValidateRelayServiceURL(statement.ServiceURL) != nil {
		return relaychange.Statement{}, ErrRelayChange
	}
	if _, err := routeRecord(base, relayRoute{statement.ServiceURL, statement.RegionID}); err != nil {
		return relaychange.Statement{}, err
	}
	return statement, nil
}
func approvalValid(base PendingRecord, proposal, approval *routeSigned) bool {
	p, err := proposalStatement(base, proposal)
	if err != nil || approval == nil {
		return false
	}
	s, err := approval.signed().Verify(keyForRole(base, 3-p.Proposer))
	return err == nil && s.Kind == relaychange.Approval && s.Signer == 3-p.Proposer && contextEqual(p, s) && s.ProposalHash == proposal.signed().Digest()
}
func snapshotRoute(base PendingRecord, r routeStateDisk) RelayChangeSnapshot {
	s := RelayChangeSnapshot{ContactID: base.ID, Revision: r.Revision, State: "idle", CurrentServiceURL: r.Active.ServiceURL, CanPropose: r.Pending == nil}
	if r.Last != nil {
		s.State = "complete"
	}
	if r.LastState == "declined" {
		s.State = "declined"
	}
	if p := r.Pending; p != nil {
		s.RequestID = p.RequestID
		s.ProposedServiceURL = p.ServiceURL
		s.ProposedHost = relayDisplayHost(p.ServiceURL)
		s.State = "proposed"
		if p.Proposal == nil {
			s.LocalApproved = p.LocalIntent
			return s
		}
		st, err := proposalStatement(base, p.Proposal)
		if err != nil {
			s.State = "recovering"
			return s
		}
		both := approvalValid(base, p.Proposal, p.Approval)
		s.LocalApproved = st.Proposer == role(base) || both
		s.PeerApproved = st.Proposer != role(base) || both
		s.CanApprove = !s.LocalApproved
		s.State = "awaiting-approval"
		if both {
			s.State = "preparing"
		}
		if p.Authenticated {
			s.State = "switching"
		}
	}
	return s
}
func relayDisplayHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && port != "443" {
		return net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}
func (a *ApplicationState) RelayChange(id string) (RelayChangeSnapshot, error) {
	if a == nil || a.vault == nil {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, _, err := a.routeLocked(id)
	if err != nil {
		return RelayChangeSnapshot{}, err
	}
	return snapshotRoute(base, r), nil
}
func (a *ApplicationState) BeginRelayChange(id, service string) (RelayChangeSnapshot, error) {
	if a == nil || a.vault == nil || ValidateRelayServiceURL(service) != nil {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, index, err := a.routeLocked(id)
	if err != nil {
		return RelayChangeSnapshot{}, err
	}
	if r.Pending != nil {
		if r.Pending.LocalIntent && r.Pending.Proposal == nil && r.Pending.ServiceURL == service {
			return snapshotRoute(base, r), nil
		}
		return snapshotRoute(base, r), ErrRelayChange
	}
	nonce, err := randomStoreID()
	if err != nil {
		return snapshotRoute(base, r), err
	}
	r.Pending = &routePending{RequestID: nonce, ServiceURL: service, LocalIntent: true}
	r.Outgoing = nil
	r.LastState = ""
	if err := a.saveRouteLocked(r, index); err != nil {
		return RelayChangeSnapshot{}, err
	}
	return snapshotRoute(base, a.routeStateLocked(id)), nil
}
func (a *ApplicationState) routeStateLocked(id string) routeStateDisk {
	for _, r := range a.state.Routes {
		if r.ContactID == id {
			return cloneRoute(r)
		}
	}
	return routeStateDisk{}
}
func (a *ApplicationState) PendingRelayIntent(id string) (RelayIntent, error) {
	if a == nil || a.vault == nil {
		return RelayIntent{}, ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	_, r, _, err := a.routeLocked(id)
	if err != nil {
		return RelayIntent{}, err
	}
	p := r.Pending
	if p == nil || !p.LocalIntent || p.Proposal != nil {
		return RelayIntent{}, nil
	}
	return RelayIntent{p.RequestID, p.ServiceURL, true}, nil
}
func (a *ApplicationState) CompleteRelayProposal(id, requestID string, region uint32) (RelayChangeSnapshot, error) {
	if a == nil || a.vault == nil {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, index, err := a.routeLocked(id)
	if err != nil {
		return RelayChangeSnapshot{}, err
	}
	p := r.Pending
	if p == nil || p.RequestID != requestID || !p.LocalIntent {
		return snapshotRoute(base, r), ErrRelayChange
	}
	if p.Proposal != nil {
		return snapshotRoute(base, r), nil
	}
	route := relayRoute{p.ServiceURL, region}
	if route == r.Active {
		r.Pending = nil
		r.Outgoing = nil
		r.LastState = ""
		retireRoute(&r, requestID)
		if err := a.saveRouteLocked(r, index); err != nil {
			return RelayChangeSnapshot{}, err
		}
		return snapshotRoute(base, a.routeStateLocked(id)), nil
	}
	if _, err := routeRecord(base, route); err != nil {
		return snapshotRoute(base, r), err
	}
	statement, err := statementContext(base, r, p)
	if err != nil {
		return snapshotRoute(base, r), err
	}
	statement.Kind, statement.ServiceURL, statement.RegionID = relaychange.Proposal, route.ServiceURL, route.RegionID
	local, err := identity.FromSeed(a.state.Identity.Seed)
	if err != nil {
		return snapshotRoute(base, r), err
	}
	signed, err := relaychange.Sign(local, statement)
	if err != nil {
		return snapshotRoute(base, r), err
	}
	p.Proposal = diskSigned(signed)
	if err := a.saveRouteLocked(r, index); err != nil {
		return RelayChangeSnapshot{}, err
	}
	return snapshotRoute(base, a.routeStateLocked(id)), nil
}
