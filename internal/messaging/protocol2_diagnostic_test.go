package messaging

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/identity"
	"whatsup/internal/pairing"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type protocol2Signer struct{ local *identity.Identity }

func (s protocol2Signer) Public() crypto.PublicKey {
	key := s.local.PublicKey()
	return ed25519.PublicKey(key[:])
}

func (s protocol2Signer) Sign(_ io.Reader, data []byte, options crypto.SignerOpts) ([]byte, error) {
	if options == nil || options.HashFunc() != crypto.Hash(0) {
		return nil, sessionauth.ErrAuthentication
	}
	return s.local.Sign(data), nil
}

func protocol2Certificate(t *testing.T, local *identity.Identity) tls.Certificate {
	t.Helper()
	signer := protocol2Signer{local}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "WhatsUp contact v1"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(sessionauth.MaxLifetime),
		KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: signer}
}

func TestRealProtocol2TLSStopsManagerWithUpdateNoticeAndRetainsOutbox(t *testing.T) {
	for _, currentServer := range []bool{true, false} {
		t.Run(fmt.Sprintf("current-server=%v", currentServer), func(t *testing.T) {
			owner, joiner := newRegistryPeer(t), newRegistryPeer(t)
			id := seedRegistryAssociation(t, owner, joiner)
			current, previous := owner, joiner
			if !currentServer {
				current, previous = joiner, owner
			}
			records, err := current.app.CommittedContacts()
			if err != nil || len(records) != 1 {
				t.Fatal("synthetic association missing", err)
			}
			record := records[0]
			// Prepare durable local approval, not a fresh invitation or network
			// session. The test then drives the real reconnect authenticator.
			approvals, err := pairing.NewManager(record.Local, pairing.Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer approvals.Close()
			invite, err := approvals.Create(record.ServerEndpoint, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = approvals.Reserve(invite.Link(), record.Peer); err != nil {
				t.Fatal(err)
			}
			contact, err := approvals.Approve(record.Peer.Fingerprint())
			if err != nil {
				t.Fatal(err)
			}
			if err := current.app.ApproveCommittedConversation(record, identifiedFixtureContact{contact, id}); err != nil {
				t.Fatal(err)
			}
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			var profiles, dials atomic.Int32
			listener := &changedPeerListener{raw: left}
			m, err := NewContact(current.local, current.app, id, ContactOptions{Factories: Factories{
				Profile: func(context.Context, string) (*transport.RelayProfile, error) { profiles.Add(1); return nil, nil },
				Listen: func(context.Context, storage.PendingRecord, *transport.RelayProfile) (Listener, error) {
					return listener, nil
				},
				Dial: func(context.Context, storage.PendingRecord, *transport.RelayProfile) (net.Conn, error) {
					dials.Add(1)
					return left, nil
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			queued, err := m.Send(id, "synthetic retained text for authenticated protocol-2 diagnostic")
			if err != nil {
				t.Fatal(err)
			}
			binding, err := storage.ContactBinding(record)
			if err != nil {
				t.Fatal(err)
			}
			alpn := "whatsup-contact/2." + id + "." + base64.RawURLEncoding.EncodeToString(binding[:])
			config := &tls.Config{
				MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
				InsecureSkipVerify: true, Certificates: []tls.Certificate{protocol2Certificate(t, previous.local)},
				ClientAuth: tls.RequireAnyClientCert, SessionTicketsDisabled: true, NextProtos: []string{alpn},
				VerifyConnection: func(state tls.ConnectionState) error {
					if state.Version != tls.VersionTLS13 || state.DidResume || state.NegotiatedProtocol != alpn || len(state.PeerCertificates) != 1 {
						return sessionauth.ErrAuthentication
					}
					key, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
					expected := current.local.PublicKey()
					if !ok || !ed25519.PublicKey(expected[:]).Equal(key) {
						return sessionauth.ErrAuthentication
					}
					return nil
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			peerDone := make(chan error, 1)
			go func() {
				var peer *tls.Conn
				if currentServer {
					peer = tls.Client(right, config)
				} else {
					peer = tls.Server(right, config)
				}
				if err := peer.HandshakeContext(ctx); err != nil {
					peerDone <- err
					return
				}
				_ = peer.SetReadDeadline(time.Now().Add(time.Second))
				var timeout net.Error
				if n, err := peer.Read(make([]byte, 1)); n != 0 || err == nil || errors.As(err, &timeout) && timeout.Timeout() {
					peerDone <- errors.New("protocol-2 diagnostic carried application bytes or retained stream")
					return
				}
				peerDone <- nil
			}()
			if _, err := m.Connect(id); err != nil {
				t.Fatal(err)
			}
			joined := make(chan struct{})
			go func() { m.workers.Wait(); close(joined) }()
			select {
			case <-joined:
			case <-ctx.Done():
				t.Fatal("authenticated protocol-2 contact kept retrying")
			}
			if err := <-peerDone; err != nil {
				t.Fatal("real protocol-2 TLS fixture failed", err)
			}
			status := m.Snapshot()
			if status.State != "update-required" || !status.CanConnect || !status.CanSend || status.PeerProtocol != 0 || status.AttachmentsSupported || status.ContactControlsSupported || status.Path != "Unknown" {
				t.Fatal("real diagnostic did not pause with update notice and no live capabilities")
			}
			if len(status.Messages) != 1 || len(queued.Messages) != 1 || status.Messages[0].ID != queued.Messages[0].ID || status.Messages[0].Delivered || len(m.conversation.Outbox()) != 1 || m.record != record || status.PeerFingerprint != record.Peer.Fingerprint() {
				t.Fatal("diagnostic changed the pin, queue, stable ID or delivery state")
			}
			if profiles.Load() != 1 || currentServer && listener.calls.Load() != 1 || !currentServer && dials.Load() != 1 {
				t.Fatal("diagnostic retried or selected another service")
			}
		})
	}
}
