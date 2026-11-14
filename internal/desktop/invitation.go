package desktop

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"whatsup/internal/identity"
	"whatsup/internal/pairflow"
	"whatsup/internal/pairing"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
	"whatsup/internal/transport/address"
)

// InvitationShare is returned only by the explicit Create action. It contains a
// private bearer link; status polling, diagnostics and preferences never carry it.
type InvitationShare struct {
	Link      string `json:"link"`
	ExpiresAt string `json:"expiresAt"`
}

type invitationListener interface {
	AddrEndpoint() (address.Endpoint, error)
	Accept(context.Context) (net.Conn, error)
	Close() error
}

// The approved profile stays backend-only and follows the exact transport job.
// It is never reconstructed from peer input or put in a public status DTO.
type selectedInvitationListener struct {
	invitationListener
	profile *transport.RelayProfile
}
type selectedInvitationConn struct {
	net.Conn
	profile *transport.RelayProfile
}

func (c *selectedInvitationConn) RetireInvitation(ctx context.Context, passive bool) error {
	if retirer, ok := c.Conn.(interface {
		RetireInvitation(context.Context, bool) error
	}); ok {
		return retirer.RetireInvitation(ctx, passive)
	}
	return transport.ErrInvitationTransport
}
func selectedProfiles(profile *transport.RelayProfile) []*transport.RelayProfile {
	if profile == nil {
		return nil
	}
	return []*transport.RelayProfile{profile}
}

type invitationControl struct {
	mu              sync.Mutex
	local           *identity.Identity
	flow            *pairflow.Coordinator
	job             *invitationJob
	closed          bool
	wg              sync.WaitGroup
	bootstrapFailed bool
	legacyFixture   bool // trusted Go-only domain fixture; production is always v2
	listen          func(context.Context, string, bool) (invitationListener, error)
	dial            func(context.Context, address.Endpoint, string, bool) (net.Conn, error)
}

type invitationJob struct {
	mu       sync.Mutex
	once     sync.Once
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	listener invitationListener
	manager  *pairing.Manager
}

func newInvitationControl(local *identity.Identity, flow *pairflow.Coordinator) *invitationControl {
	return &invitationControl{local: local, flow: flow,
		listen: func(ctx context.Context, url string, consent bool) (invitationListener, error) {
			if err := transport.ValidatePolicy(transport.RelayFallback, consent); err != nil {
				return nil, err
			}
			profile, err := transport.FetchRelayProfile(ctx, url)
			if err != nil {
				return nil, err
			}
			listener, err := transport.NewInvitationListener(ctx, transport.InvitationOptions{Profile: profile, Policy: transport.RelayFallback, AllowRelayFallback: consent})
			if err != nil {
				return nil, err
			}
			return &selectedInvitationListener{invitationListener: listener, profile: profile}, nil
		},
		dial: func(ctx context.Context, endpoint address.Endpoint, url string, consent bool) (net.Conn, error) {
			if err := transport.ValidatePolicy(transport.RelayFallback, consent); err != nil {
				return nil, err
			}
			profile, err := transport.FetchRelayProfile(ctx, url)
			if err != nil {
				return nil, err
			}
			conn, err := transport.DialInvitation(ctx, endpoint, profile, transport.RelayFallback, consent)
			if err != nil {
				return nil, err
			}
			return &selectedInvitationConn{Conn: conn, profile: profile}, nil
		},
	}
}

func (c *invitationControl) begin(consent bool, expires time.Time) (*invitationJob, error) {
	if transport.ValidatePolicy(transport.RelayFallback, consent) != nil || !time.Now().Before(expires) {
		return nil, errors.New("Encrypted relay consent is required; direct-only pairing is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.flow.Snapshot().State
	if c.closed || c.job != nil || state == "pending" || state == "reconnect-required" || state == "awaiting-approval" || state == "waiting-for-peer" || state == "saving" || state == "connecting" {
		return nil, errors.New("Finish or discard the current setup before starting another invitation")
	}
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	job := &invitationJob{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	c.job, c.bootstrapFailed = job, false
	c.wg.Add(1)
	return job, nil
}

func (j *invitationJob) closeOwned() {
	j.cancel()
	j.mu.Lock()
	listener, manager := j.listener, j.manager
	j.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	if manager != nil {
		_ = manager.Close()
	}
}

func (c *invitationControl) finish(job *invitationJob, failed bool) {
	job.once.Do(func() {
		job.closeOwned()
		c.mu.Lock()
		if c.job == job {
			c.job = nil
			c.bootstrapFailed = failed
		}
		c.mu.Unlock()
		close(job.done)
		c.wg.Done()
	})
}

func (c *invitationControl) create(url string, consent bool) (InvitationShare, error) {
	job, err := c.begin(consent, time.Now().Add(pairing.DefaultLifetime))
	if err != nil {
		return InvitationShare{}, err
	}
	fail := func() (InvitationShare, error) {
		c.finish(job, true)
		return InvitationShare{}, errors.New("Invitation could not start. Check the HTTPS connection service and relay consent.")
	}
	listener, err := c.listen(job.ctx, url, consent)
	if err != nil {
		return fail()
	}
	job.mu.Lock()
	job.listener = listener
	job.mu.Unlock()
	if job.ctx.Err() != nil {
		return fail()
	}
	endpoint, err := listener.AddrEndpoint()
	if err != nil {
		return fail()
	}
	manager, err := pairing.NewManager(c.local.PublicKey(), pairing.Options{CloseInvitation: listener.Close})
	if err != nil {
		return fail()
	}
	job.mu.Lock()
	job.manager = manager
	job.mu.Unlock()
	var invite pairing.Invite
	if c.legacyFixture {
		invite, err = manager.Create(string(endpoint.EncodeForTransport()), time.Until(deadline(job.ctx)))
	} else {
		invite, err = manager.CreateWithService(string(endpoint.EncodeForTransport()), url, time.Until(deadline(job.ctx)))
	}
	if err != nil || job.ctx.Err() != nil {
		return fail()
	}
	go func() {
		raw, err := listener.Accept(job.ctx)
		if err == nil {
			var profile *transport.RelayProfile
			if selected, ok := listener.(*selectedInvitationListener); ok {
				profile = selected.profile
			}
			err = c.flow.Accept(job.ctx, raw, invite, manager, selectedProfiles(profile)...)
		}
		c.finish(job, err != nil)
	}()
	c.mu.Lock()
	current := !c.closed && c.job == job && job.ctx.Err() == nil
	c.mu.Unlock()
	if !current {
		return fail()
	}
	return InvitationShare{Link: invite.Link(), ExpiresAt: invite.ExpiresAt().Format(time.RFC3339)}, nil
}

func deadline(ctx context.Context) time.Time {
	value, _ := ctx.Deadline()
	return value
}

func (c *invitationControl) join(link, url string, consent bool) (pairflow.Snapshot, error) {
	invite, err := pairing.ParseInvite(link)
	if err != nil || invite.Owner().Equal(c.local.PublicKey()) || !c.legacyFixture && invite.ServiceURL() == "" {
		return c.snapshot(), errors.New("This private invitation is invalid or belongs to this device")
	}
	if invite.ServiceURL() != "" && invite.ServiceURL() != url {
		return c.snapshot(), errors.New("Approve the connection service carried by this invitation")
	}
	// Only the inviter's manager decides expiry. The joiner's wall clock can
	// differ; its own deadline bounds this attempt without extending the invite.
	job, err := c.begin(consent, time.Now().Add(pairing.DefaultLifetime))
	if err != nil {
		return c.snapshot(), err
	}
	go func() {
		raw, err := c.dial(job.ctx, invite.Endpoint(), url, consent)
		if err == nil {
			var profile *transport.RelayProfile
			if selected, ok := raw.(*selectedInvitationConn); ok {
				profile = selected.profile
			}
			err = c.flow.Join(job.ctx, raw, invite, selectedProfiles(profile)...)
		}
		c.finish(job, err != nil)
	}()
	return pairflow.Snapshot{State: "connecting"}, nil
}

func (c *invitationControl) snapshot() pairflow.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return pairflow.Snapshot{State: "unavailable"}
	}
	status := c.flow.Snapshot()
	if c.job != nil && (status.State == "idle" || status.State == "completed" || status.State == "failed" || status.State == "canceled" || status.State == "expired") {
		return pairflow.Snapshot{State: "connecting"}
	}
	if c.bootstrapFailed && status.State == "idle" {
		return pairflow.Snapshot{State: "failed"}
	}
	return status
}

func (c *invitationControl) cancelInvitation() (pairflow.Snapshot, error) {
	c.mu.Lock()
	job := c.job
	c.mu.Unlock()
	status := c.flow.Snapshot()
	var denyErr error
	if status.State == "awaiting-approval" || status.State == "waiting-for-peer" || status.State == "saving" {
		status, denyErr = c.flow.Deny(status.RequestID)
	}
	if job != nil {
		job.closeOwned()
		<-job.done
	}
	c.mu.Lock()
	c.bootstrapFailed = false
	c.mu.Unlock()
	if denyErr != nil {
		return status, errors.New("Pairing cancellation could not be saved. Lock storage and preserve existing state before retrying.")
	}
	status = c.flow.Snapshot()
	if status.State == "awaiting-approval" || status.State == "waiting-for-peer" || status.State == "saving" || status.State == "pending" || status.State == "reconnect-required" {
		status, denyErr = c.flow.Deny(status.RequestID)
		if denyErr != nil {
			return status, errors.New("Pairing cancellation could not be saved. Lock storage and preserve existing state before retrying.")
		}
	}
	return status, nil
}

func (c *invitationControl) close() {
	c.mu.Lock()
	c.closed = true
	job := c.job
	c.mu.Unlock()
	if job != nil {
		job.closeOwned()
	}
	c.wg.Wait()
}

func (s *Service) CreateInvitation(relayMapURL string, allowRelayFallback bool) (InvitationShare, error) {
	s.mu.Lock()
	control := s.invitations
	available := s.state == "unlocked" && control != nil
	capacityErr := s.invitationCapacityLocked(available)
	s.mu.Unlock()
	if capacityErr != nil {
		return InvitationShare{}, capacityErr
	}
	if !available {
		return InvitationShare{}, errors.New("Unlock local storage before creating an invitation")
	}
	share, err := control.create(relayMapURL, allowRelayFallback)
	if err != nil {
		return InvitationShare{}, err
	}
	s.mu.Lock()
	current := s.state == "unlocked" && s.invitations == control
	s.mu.Unlock()
	if !current {
		return InvitationShare{}, errors.New("Invitation was canceled while local storage changed")
	}
	return share, nil
}

func (s *Service) JoinInvitation(link, relayMapURL string, allowRelayFallback bool) (pairflow.Snapshot, error) {
	s.mu.Lock()
	control := s.invitations
	available := s.state == "unlocked" && control != nil
	capacityErr := s.invitationCapacityLocked(available)
	s.mu.Unlock()
	if capacityErr != nil {
		return pairflow.Snapshot{State: "unavailable"}, capacityErr
	}
	if !available {
		return pairflow.Snapshot{State: "unavailable"}, errors.New("Unlock local storage before joining an invitation")
	}
	return control.join(link, relayMapURL, allowRelayFallback)
}

func (s *Service) invitationCapacityLocked(available bool) error {
	if !available {
		return nil
	}
	if s.application == nil {
		return errors.New("Saved contact capacity could not be verified")
	}
	contacts, err := s.application.CommittedContacts()
	if err != nil {
		return errors.New("Saved contact capacity could not be verified")
	}
	if len(contacts) >= storage.MaxCommittedContacts {
		return errors.New("WhatsUp can save up to 32 contacts. Forget a contact before adding another.")
	}
	return nil
}

func (s *Service) CancelInvitation() (pairflow.Snapshot, error) {
	s.mu.Lock()
	control := s.invitations
	available := s.state == "unlocked" && control != nil
	s.mu.Unlock()
	if !available {
		return pairflow.Snapshot{State: "unavailable"}, errors.New("Unlock local storage before canceling an invitation")
	}
	return control.cancelInvitation()
}
