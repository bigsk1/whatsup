package transport_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/chat"
	"whatsup/internal/identity"
	"whatsup/internal/messaging"
	"whatsup/internal/pairflow"
	"whatsup/internal/pairing"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type persistentMessagingPeer struct {
	keys      *disposableIntegrationKeys
	directory string
	app       *storage.ApplicationState
	local     *identity.Identity
	manager   *messaging.Manager
}

func newPersistentMessagingPeer(t *testing.T) *persistentMessagingPeer {
	t.Helper()
	peer := &persistentMessagingPeer{keys: &disposableIntegrationKeys{}, directory: filepath.Join(t.TempDir(), "private")}
	peer.open(t)
	t.Cleanup(func() { peer.close(t) })
	return peer
}

func (p *persistentMessagingPeer) open(t *testing.T) {
	t.Helper()
	vault, err := storage.Open(p.directory, p.keys)
	if err != nil {
		t.Fatal("could not reopen disposable encrypted vault")
	}
	app, err := storage.OpenApplication(vault)
	if err != nil {
		_ = vault.Close()
		t.Fatal("could not reopen disposable application state")
	}
	local, err := app.Identity()
	if err != nil {
		_ = app.Close()
		t.Fatal("could not load disposable identity")
	}
	if p.local != nil && !p.local.PublicKey().Equal(local.PublicKey()) {
		t.Fatal("restart replaced the established local identity")
	}
	p.app, p.local = app, local
}

func (p *persistentMessagingPeer) close(t *testing.T) {
	t.Helper()
	if p.manager != nil {
		p.manager.Close()
		p.manager = nil
	}
	if p.app != nil {
		if err := p.app.Close(); err != nil {
			t.Error("disposable vault could not close after messaging joined")
		}
		p.app = nil
	}
}

func pairPersistentMessagingPeers(t *testing.T, ctx context.Context, profile *transport.RelayProfile, owner, joiner *persistentMessagingPeer, completed ...func(string)) {
	t.Helper()
	var ownerCompleted, joinerCompleted func(string)
	if len(completed) == 2 {
		ownerCompleted, joinerCompleted = completed[0], completed[1]
	}
	ownerFlow, err := pairflow.New(owner.local, owner.app, pairflow.Options{CompleteActivation: true, OnCompleted: ownerCompleted})
	if err != nil {
		t.Fatal("could not construct disposable owner pairing")
	}
	joinerFlow, err := pairflow.New(joiner.local, joiner.app, pairflow.Options{CompleteActivation: true, OnCompleted: joinerCompleted})
	if err != nil {
		t.Fatal("could not construct disposable joiner pairing")
	}
	defer ownerFlow.Close()
	defer joinerFlow.Close()
	listener, err := transport.NewInvitationListener(ctx, transport.InvitationOptions{Profile: profile, Policy: transport.RelayFallback, AllowRelayFallback: true})
	if err != nil {
		t.Fatal("could not construct disposable invitation transport")
	}
	defer listener.Close()
	manager, err := pairing.NewManager(owner.local.PublicKey(), pairing.Options{CloseInvitation: listener.Close})
	if err != nil {
		t.Fatal("could not construct disposable invitation manager")
	}
	defer manager.Close()
	endpoint, err := listener.AddrEndpoint()
	if err != nil {
		t.Fatal("could not obtain disposable invitation endpoint")
	}
	invite, err := manager.Create(string(endpoint.EncodeForTransport()), 0)
	if err != nil {
		t.Fatal("could not construct disposable invitation")
	}
	results := make(chan error, 2)
	go func() {
		raw, err := listener.Accept(ctx)
		if err == nil {
			err = ownerFlow.Accept(ctx, raw, invite, manager, profile)
		}
		results <- err
	}()
	raw, err := transport.DialInvitation(ctx, invite.Endpoint(), profile, transport.RelayFallback, true)
	if err != nil {
		t.Fatal("could not dial disposable invitation")
	}
	go func() { results <- joinerFlow.Join(ctx, raw, invite, profile) }()
	fixture := &liveIntegrationPair{ctx: ctx}
	left := waitIntegrationApproval(t, fixture, ownerFlow)
	right := waitIntegrationApproval(t, fixture, joinerFlow)
	if _, err := ownerFlow.Approve(left.RequestID, left.PeerFingerprint); err != nil {
		t.Fatal("owner approval was rejected")
	}
	if _, err := joinerFlow.Approve(right.RequestID, right.PeerFingerprint); err != nil {
		t.Fatal("joiner approval was rejected")
	}
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal("real reciprocal pairing did not complete")
			}
		case <-ctx.Done():
			t.Fatal("real reciprocal pairing exceeded its deadline")
		}
	}
	for _, peer := range []*persistentMessagingPeer{owner, joiner} {
		records, err := peer.app.CommittedContacts()
		if err != nil || len(records) != 1 || records[0].Version != 2 {
			t.Fatal("pairing did not commit one routed contact")
		}
		if _, err := peer.app.LocalConversation(records[0]); err != nil {
			t.Fatal("successful pairing did not commit local conversation approval")
		}
	}
}

// writeGate holds the receiver's authenticated outgoing records before they
// reach the network. It is enabled only after fresh TLS has completed. This
// proves that a sender cannot mark delivery merely because its Write succeeded.
type writeGate struct {
	enabled atomic.Bool
	writes  atomic.Int32
	entered chan struct{}
	abort   chan struct{}
	once    sync.Once
	stop    sync.Once
}

func newWriteGate() *writeGate {
	return &writeGate{entered: make(chan struct{}), abort: make(chan struct{})}
}

type gatedMessagingConn struct {
	net.Conn
	gate *writeGate
}

func (c *gatedMessagingConn) Write(p []byte) (int, error) {
	if c.gate.enabled.Load() {
		c.gate.once.Do(func() { close(c.gate.entered) })
		<-c.gate.abort
		return 0, net.ErrClosed
	}
	n, err := c.Conn.Write(p)
	if err == nil && n == len(p) {
		c.gate.writes.Add(1)
	}
	return n, err
}

func (c *gatedMessagingConn) Close() error {
	c.gate.stop.Do(func() { close(c.gate.abort) })
	return c.Conn.Close()
}

type messagingNetworkFixture struct {
	profile    *transport.RelayProfile
	url        string
	profiles   atomic.Int32
	listens    atomic.Int32
	dials      atomic.Int32
	gate       atomic.Pointer[writeGate]
	serverGate atomic.Pointer[writeGate]
}

type observedMessagingListener struct {
	messaging.Listener
	fixture *messagingNetworkFixture
}

func (l *observedMessagingListener) Accept(ctx context.Context) (net.Conn, error) {
	raw, err := l.Listener.Accept(ctx)
	if err != nil {
		return nil, err
	}
	gate := newWriteGate()
	l.fixture.serverGate.Store(gate)
	return &gatedMessagingConn{Conn: raw, gate: gate}, nil
}

func (f *messagingNetworkFixture) factories() messaging.Factories {
	return messaging.Factories{
		Profile: func(ctx context.Context, url string) (*transport.RelayProfile, error) {
			f.profiles.Add(1)
			if ctx.Err() != nil || url != f.url {
				return nil, transport.ErrRelayProfile
			}
			return f.profile, nil
		},
		Listen: func(ctx context.Context, record storage.PendingRecord, profile *transport.RelayProfile) (messaging.Listener, error) {
			f.listens.Add(1)
			listener, err := transport.NewOngoingListener(ctx, transport.OngoingOptions{Record: record, Profile: profile, Policy: transport.RelayFallback, AllowRelayFallback: record.RelayFallbackConsent})
			if err != nil {
				return nil, err
			}
			return &observedMessagingListener{Listener: listener, fixture: f}, nil
		},
		Dial: func(ctx context.Context, record storage.PendingRecord, profile *transport.RelayProfile) (net.Conn, error) {
			f.dials.Add(1)
			raw, err := transport.DialOngoing(ctx, record, profile, transport.RelayFallback, record.RelayFallbackConsent)
			if err != nil {
				return nil, err
			}
			gate := newWriteGate()
			f.gate.Store(gate)
			return &gatedMessagingConn{Conn: raw, gate: gate}, nil
		},
	}
}

func requireIdleMessagingHeartbeat(t *testing.T, ctx context.Context, fixture *messagingNetworkFixture, owner, joiner *messaging.Manager) {
	t.Helper()
	client, server := fixture.gate.Load(), fixture.serverGate.Load()
	if client == nil || server == nil {
		t.Fatal("authenticated transport observations were missing")
	}
	clientBefore, serverBefore := client.writes.Load(), server.writes.Load()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if owner.Snapshot().State != "connected" || joiner.Snapshot().State != "connected" {
			t.Fatal("idle heartbeat lost the authenticated connection")
		}
		// There is no queued data after the acknowledged first message. A
		// ping and a pong each write two TLS application records per side.
		// Observe the actual heartbeat traffic instead of waiting a grace sleep.
		if client.writes.Load()-clientBefore >= 4 && server.writes.Load()-serverBefore >= 4 {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("idle authenticated connection produced no bidirectional heartbeat")
		}
	}
}

func waitMessaging(t *testing.T, ctx context.Context, manager *messaging.Manager, condition func(messaging.Snapshot) bool) messaging.Snapshot {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		snapshot := manager.Snapshot()
		if condition(snapshot) {
			return snapshot
		}
		if snapshot.State == "failed" || snapshot.State == "unavailable" {
			t.Fatalf("messaging entered an unexpected finite state: %s", snapshot.State)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("messaging did not progress before deadline; state=%s count=%d", snapshot.State, len(snapshot.Messages))
		}
	}
}

func messagingMessage(snapshot messaging.Snapshot, text string) (chat.Message, bool) {
	for _, message := range snapshot.Messages {
		if message.Text == text {
			return message, true
		}
	}
	return chat.Message{}, false
}

func requireEncryptedMessagingHistory(t *testing.T, peer *persistentMessagingPeer, texts ...string) {
	t.Helper()
	ciphertext, err := os.ReadFile(filepath.Join(peer.directory, "state.vault"))
	if err != nil {
		t.Fatal("encrypted messaging snapshot was missing")
	}
	for _, text := range texts {
		if bytes.Contains(ciphertext, []byte(text)) {
			t.Fatal("message history appeared in plaintext vault storage")
		}
	}
}

func TestFullMessagingOverRealTailcatReauthenticatesQueuesAcknowledgesAndRestarts(t *testing.T) {
	profile := transport.ExportLoopbackRelayProfileForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	owner, joiner := newPersistentMessagingPeer(t), newPersistentMessagingPeer(t)
	pairPersistentMessagingPeers(t, ctx, profile, owner, joiner)
	ownerRecords, err := owner.app.CommittedContacts()
	if err != nil || len(ownerRecords) != 1 {
		t.Fatal("approved contact was unavailable")
	}
	network := &messagingNetworkFixture{profile: profile, url: ownerRecords[0].RelayServiceURL}
	owner.manager, err = messaging.New(owner.local, owner.app, network.factories())
	if err != nil {
		t.Fatal("could not restore owner's approved offline conversation")
	}
	cold := owner.manager.Snapshot()
	if cold.State != "offline" || !cold.CanConnect || !cold.CanSend || network.profiles.Load() != 0 || network.listens.Load() != 0 || network.dials.Load() != 0 {
		t.Fatal("cold restore contacted the network or lost local queue approval")
	}
	queuedBeforeConnect := "synthetic queued before either ongoing connection"
	queued, err := owner.manager.Send(cold.ContactID, queuedBeforeConnect)
	first, exists := messagingMessage(queued, queuedBeforeConnect)
	if err != nil || !exists || first.Delivered {
		t.Fatal("offline queue failed or claimed unauthenticated delivery")
	}
	if _, err := owner.manager.Connect(cold.ContactID); err != nil {
		t.Fatal("explicit owner connect was rejected")
	}
	waitMessaging(t, ctx, owner.manager, func(s messaging.Snapshot) bool { return network.listens.Load() > 0 && s.State == "connecting" })
	stillQueued, _ := messagingMessage(owner.manager.Snapshot(), queuedBeforeConnect)
	if stillQueued.Delivered {
		t.Fatal("waiting for the offline peer acknowledged local outbox data")
	}
	joiner.manager, err = messaging.New(joiner.local, joiner.app, network.factories())
	if err != nil {
		t.Fatal("could not restore joiner's approved offline conversation")
	}
	joinerCold := joiner.manager.Snapshot()
	if joinerCold.State != "offline" || !joinerCold.CanSend {
		t.Fatal("joiner lost durable local conversation approval")
	}
	if _, err := joiner.manager.Connect(joinerCold.ContactID); err != nil {
		t.Fatal("explicit joiner connect was rejected")
	}
	waitMessaging(t, ctx, owner.manager, func(s messaging.Snapshot) bool { return s.State == "connected" })
	waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool { return s.State == "connected" })
	waitMessaging(t, ctx, owner.manager, func(s messaging.Snapshot) bool {
		message, ok := messagingMessage(s, queuedBeforeConnect)
		return ok && message.Delivered
	})
	requireIdleMessagingHeartbeat(t, ctx, network, owner.manager, joiner.manager)
	gate := network.gate.Load()
	if gate == nil {
		t.Fatal("actual client connection was not captured")
	}
	gate.enabled.Store(true)
	acknowledgedText := "synthetic delivery waits for authenticated acknowledgement"
	if _, err := owner.manager.Send(cold.ContactID, acknowledgedText); err != nil {
		t.Fatal("connected send was rejected")
	}
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("receiver did not attempt an authenticated response")
	}
	waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool { _, ok := messagingMessage(s, acknowledgedText); return ok })
	beforeAck, ok := messagingMessage(owner.manager.Snapshot(), acknowledgedText)
	if !ok || beforeAck.Delivered {
		t.Fatal("sender marked delivery before any receiver response could leave")
	}
	// Lose the ACK after the receiver's durable save. Reconnection must retry
	// the same ID and receive a new authenticated ACK without duplicating text.
	joiner.close(t)
	queuedAcrossRestart := "synthetic durable queue while the receiver is stopped"
	queued, err = owner.manager.Send(cold.ContactID, queuedAcrossRestart)
	queuedMessage, exists := messagingMessage(queued, queuedAcrossRestart)
	if err != nil || !exists || queuedMessage.Delivered {
		t.Fatal("stopped receiver caused false delivery or rejected durable queue")
	}
	joiner.open(t)
	joiner.manager, err = messaging.New(joiner.local, joiner.app, network.factories())
	if err != nil {
		t.Fatal("receiver restart did not restore approved conversation")
	}
	restored := joiner.manager.Snapshot()
	if restored.State != "offline" || !restored.CanSend || len(restored.Messages) != 2 {
		t.Fatal("receiver restart lost history or initiated a connection")
	}
	if _, err := joiner.manager.Connect(restored.ContactID); err != nil {
		t.Fatal("saved contact could not reconnect without a new invitation")
	}
	waitMessaging(t, ctx, owner.manager, func(s messaging.Snapshot) bool {
		message, ok := messagingMessage(s, acknowledgedText)
		return ok && message.Delivered && message.ID == beforeAck.ID
	})
	waitMessaging(t, ctx, owner.manager, func(s messaging.Snapshot) bool {
		message, ok := messagingMessage(s, queuedAcrossRestart)
		return ok && message.Delivered
	})
	received := waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool { _, ok := messagingMessage(s, queuedAcrossRestart); return ok })
	copies := 0
	for _, message := range received.Messages {
		if message.Text == queuedAcrossRestart {
			copies++
			if message.ID != queuedMessage.ID || message.Direction != chat.Incoming {
				t.Fatal("reconnected delivery replaced the stable queued ID or direction")
			}
		}
	}
	ackCopies := 0
	for _, message := range received.Messages {
		if message.Text == acknowledgedText {
			ackCopies++
			if message.ID != beforeAck.ID {
				t.Fatal("lost-ACK retry replaced the original durable message ID")
			}
		}
	}
	if copies != 1 || ackCopies != 1 || len(received.Messages) != 3 {
		t.Fatal("reconnect duplicated or lost durable messages")
	}
	replyText := "synthetic reply from independently authenticated contact"
	if _, err := joiner.manager.Send(restored.ContactID, replyText); err != nil {
		t.Fatal("bidirectional reply was rejected")
	}
	waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool {
		message, ok := messagingMessage(s, replyText)
		return ok && message.Delivered
	})
	waitMessaging(t, ctx, owner.manager, func(s messaging.Snapshot) bool { _, ok := messagingMessage(s, replyText); return ok })
	for _, peer := range []*persistentMessagingPeer{owner, joiner} {
		requireEncryptedMessagingHistory(t, peer, queuedBeforeConnect, acknowledgedText, replyText, queuedAcrossRestart)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("messaging exhausted its overall integration deadline")
	}
}
