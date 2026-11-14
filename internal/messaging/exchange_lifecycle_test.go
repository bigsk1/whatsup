package messaging

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"whatsup/internal/chat"
	"whatsup/internal/identity"
	"whatsup/internal/message"
	"whatsup/internal/pairing"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transfer"
)

type readDeadlineFault struct {
	net.Conn
	fail         atomic.Bool
	blockWrite   atomic.Bool
	writeStarted chan struct{}
	writeClosed  chan struct{}
	writeOnce    sync.Once
	closeOnce    sync.Once
}

// These heartbeat tests intentionally have no durable contact record. An empty
// scoped engine isolates transport lifecycle without granting file authority.
type emptyTransferRepository struct{ transfer.Repository }

func (emptyTransferRepository) List() ([]transfer.Record, error) { return []transfer.Record{}, nil }

func (c *readDeadlineFault) Write(data []byte) (int, error) {
	if c.blockWrite.Load() {
		c.writeOnce.Do(func() { close(c.writeStarted) })
		<-c.writeClosed
		return 0, net.ErrClosed
	}
	return c.Conn.Write(data)
}

func (c *readDeadlineFault) Close() error {
	c.closeOnce.Do(func() { close(c.writeClosed) })
	return c.Conn.Close()
}

func (c *readDeadlineFault) SetReadDeadline(limit time.Time) error {
	if c.fail.Load() {
		return errors.New("synthetic unavailable deadline")
	}
	return c.Conn.SetReadDeadline(limit)
}

func exchangeFixture(t *testing.T, repositories ...message.Repository) (*Manager, *sessionauth.Session, *sessionauth.Session, *chat.Conversation, *readDeadlineFault) {
	t.Helper()
	local, _ := identity.Generate()
	peer, _ := identity.Generate()
	left, right := net.Pipe()
	fault := &readDeadlineFault{Conn: left, writeStarted: make(chan struct{}), writeClosed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	type result struct {
		session *sessionauth.Session
		err     error
	}
	done := make(chan result, 1)
	options := sessionauth.Options{Local: local, Peer: peer.PublicKey(), ContactID: "AQEBAQEBAQEBAQEBAQEBAQ", Association: [32]byte{1}, Server: true}
	go func() { s, err := sessionauth.Authenticate(ctx, fault, options); done <- result{s, err} }()
	client, err := sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: peer, Peer: local.PublicKey(), ContactID: options.ContactID, Association: options.Association})
	server := <-done
	if err != nil || server.err != nil {
		t.Fatal("disposable exact-key TLS fixture failed")
	}
	t.Cleanup(func() { _ = server.session.Close(); _ = client.Close() })
	// Isolate exchange lifecycle with a domain-local approved conversation;
	// production record approval and network factories are checked separately.
	keys := tailcat.NewPrivateKey()
	keys.Public.RegionID = 900
	owner, err := pairing.NewManager(local.PublicKey(), pairing.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	invite, err := owner.Create(string(keys.Public.Addr()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Reserve(invite.Link(), peer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	contact, err := owner.Approve(peer.PublicKey().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(contact.Revoke)
	var store message.Repository
	if len(repositories) == 0 {
		store = newMessageRepository(t)
	} else if len(repositories) == 1 {
		store = repositories[0]
	} else {
		t.Fatal("one repository per test conversation is required")
	}
	conversation, err := chat.Open(store, contact, chat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	vault, err := storage.Open(filepath.Join(t.TempDir(), "private"), &lifecycleKeys{})
	if err != nil {
		t.Fatal(err)
	}
	app, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	engine, err := transfer.New(emptyTransferRepository{}, transfer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Close)
	m := &Manager{managerState: &managerState{application: app, record: storage.PendingRecord{Peer: peer.PublicKey()}, transfers: engine, state: "connected", wake: make(chan struct{}, 1)}}
	return m, server.session, client, conversation, fault
}

func consumeInitialProfile(t *testing.T, session *sessionauth.Session) {
	t.Helper()
	got, err := readPacket(session)
	if err != nil || got.kind != profilePacket || got.name != "" {
		t.Fatal("default profile sharing emitted a name or invalid packet")
	}
}

func waitExchange(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("terminated exchange reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("exchange did not close and join its reader")
	}
}

func TestExchangeRejectsUnsolicitedPongAndJoinsTLSReader(t *testing.T) {
	m, server, client, conversation, _ := exchangeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- m.exchange(ctx, server, conversation) }()
	consumeInitialProfile(t, client)
	if err := writePacket(client, packet{kind: pongPacket, nonce: [16]byte{1}}); err != nil {
		t.Fatal(err)
	}
	waitExchange(t, result)
	if server.WithLivePeer(m.record.Peer, func() error { return nil }) == nil {
		t.Fatal("rejected heartbeat retained live proof")
	}
}

func TestExchangeReadDeadlineFailureTerminatesWithoutHeartbeatWait(t *testing.T) {
	m, server, _, conversation, fault := exchangeFixture(t)
	fault.fail.Store(true)
	result := make(chan error, 1)
	go func() { result <- m.exchange(context.Background(), server, conversation) }()
	waitExchange(t, result)
}

func TestExchangeEchoesHeartbeatAndCancellationJoinsBlockedReader(t *testing.T) {
	m, server, client, conversation, _ := exchangeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- m.exchange(ctx, server, conversation) }()
	consumeInitialProfile(t, client)
	nonce := [16]byte{1, 3, 5, 7}
	if err := writePacket(client, packet{kind: pingPacket, nonce: nonce}); err != nil {
		t.Fatal(err)
	}
	got, err := readPacket(client)
	if err != nil || got.kind != pongPacket || got.nonce != nonce {
		t.Fatal("heartbeat response was not bound to exact challenge")
	}
	cancel()
	waitExchange(t, result)
	if _, err := client.Read(make([]byte, 1)); err == nil || errors.Is(err, io.ErrNoProgress) {
		t.Fatal("canceled exchange left peer stream readable")
	}
}
