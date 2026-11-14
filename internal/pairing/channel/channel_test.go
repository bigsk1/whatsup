package channel

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"whatsup/internal/identity"
)

func device(t *testing.T) *identity.Identity {
	t.Helper()
	local, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return local
}

type sessionResult struct {
	session *Session
	err     error
}

func pair(t *testing.T, expires time.Time) (*Session, *Session) {
	t.Helper()
	owner, candidate := device(t), device(t)
	id := [16]byte{1}
	serverRaw, clientRaw := net.Pipe()
	t.Cleanup(func() { _ = serverRaw.Close(); _ = clientRaw.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	serverDone := make(chan sessionResult, 1)
	go func() {
		session, err := NewServer(ctx, serverRaw, owner, id, expires)
		serverDone <- sessionResult{session, err}
	}()
	client, err := NewClient(ctx, clientRaw, candidate, owner.PublicKey(), id, expires)
	if err != nil {
		t.Fatal(err)
	}
	server := <-serverDone
	if server.err != nil {
		t.Fatal(server.err)
	}
	t.Cleanup(func() { _ = server.session.Close(); _ = client.Close() })
	return server.session, client
}

type approvalResult struct {
	proof *Approval
	err   error
}

func approvePair(t *testing.T, server, client *Session) (*Approval, *Approval) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	serverDone := make(chan approvalResult, 1)
	go func() {
		proof, err := server.Approve(ctx, server.Peer().Fingerprint())
		serverDone <- approvalResult{proof, err}
	}()
	clientProof, err := client.Approve(ctx, client.Peer().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	result := <-serverDone
	if result.err != nil {
		t.Fatal(result.err)
	}
	return result.proof, clientProof
}

func TestRealTLSMutualIdentityAndOneShotApproval(t *testing.T) {
	expires := time.Now().Add(time.Minute)
	server, client := pair(t, expires)
	if server.Role() != Inviter || client.Role() != Joiner || !server.Local().Equal(client.Peer()) || !server.Peer().Equal(client.Local()) || server.InvitationID() != client.InvitationID() || !server.ExpiresAt().Equal(expires) {
		t.Fatal("TLS identity, role or invitation association was lost")
	}
	for _, session := range []*Session{server, client} {
		called := false
		if err := session.WithPendingPeer(func() error { called = true; return nil }); err != nil || !called {
			t.Fatal("authenticated candidate was not available for bounded reservation")
		}
		config := session.state.tls.ConnectionState()
		if config.Version != tls.VersionTLS13 || config.DidResume {
			t.Fatal("TLS protocol policy was weakened")
		}
	}
	serverProof, clientProof := approvePair(t, server, client)
	for index, proof := range []*Approval{serverProof, clientProof} {
		session := []*Session{server, client}[index]
		if !proof.MatchesSession(session) || proof.MatchesSession(&Session{}) || !proof.Local().Equal(session.Local()) || !proof.Peer().Equal(session.Peer()) || proof.Role() != session.Role() || proof.InvitationID() != session.InvitationID() || !proof.ExpiresAt().Equal(expires) {
			t.Fatal("approval was not bound to the exact authenticated session")
		}
		if err := proof.WithLiveApproval(func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := session.Approve(context.Background(), session.Peer().Fingerprint()); !errors.Is(err, ErrUsed) {
			t.Fatal("repeated approval succeeded")
		}
		if err := session.WithPendingPeer(func() error { t.Fatal("approved channel became pending"); return nil }); !errors.Is(err, ErrUsed) {
			t.Fatal("approved channel was reused for reservation")
		}
	}
	copyOfProof := *serverProof
	copyOfSession := *server
	if err := copyOfSession.Close(); err != nil {
		t.Fatal(err)
	}
	if err := copyOfProof.WithLiveApproval(func() error { t.Fatal("closed proof retained authority"); return nil }); !errors.Is(err, ErrClosed) {
		t.Fatal("closing a copy did not invalidate the shared approval")
	}
}

func TestWrongOwnerOrInvitationFailsTheRealTLSHandshake(t *testing.T) {
	for _, mismatch := range []string{"owner", "invitation"} {
		t.Run(mismatch, func(t *testing.T) {
			owner, candidate := device(t), device(t)
			expected := owner.PublicKey()
			serverID, clientID := [16]byte{1}, [16]byte{1}
			if mismatch == "owner" {
				expected = device(t).PublicKey()
			} else {
				clientID[0] = 2
			}
			serverRaw, clientRaw := net.Pipe()
			t.Cleanup(func() { _ = serverRaw.Close(); _ = clientRaw.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			expires := time.Now().Add(time.Minute)
			go func() { _, err := NewServer(ctx, serverRaw, owner, serverID, expires); done <- err }()
			if _, err := NewClient(ctx, clientRaw, candidate, expected, clientID, expires); !errors.Is(err, ErrHandshake) {
				t.Fatalf("mismatched %s was accepted: %v", mismatch, err)
			}
			if err := <-done; err == nil {
				t.Fatal("failed handshake produced a pending server session")
			}
		})
	}
}

func alteredCertificate(t *testing.T, local *identity.Identity, change func(*x509.Certificate)) tls.Certificate {
	t.Helper()
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: certificateName},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Minute),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	change(template)
	signer := identitySigner{local}
	der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: signer}
}

func TestUntrustedCandidateCertificatesCannotEnterPending(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *identity.Identity) []tls.Certificate
	}{
		{"missing", func(*testing.T, *identity.Identity) []tls.Certificate { return nil }},
		{"expired", func(t *testing.T, local *identity.Identity) []tls.Certificate {
			return []tls.Certificate{alteredCertificate(t, local, func(c *x509.Certificate) {
				c.NotBefore = time.Now().Add(-2 * time.Minute)
				c.NotAfter = time.Now().Add(-time.Minute)
			})}
		}},
		{"future", func(t *testing.T, local *identity.Identity) []tls.Certificate {
			return []tls.Certificate{alteredCertificate(t, local, func(c *x509.Certificate) {
				c.NotBefore = time.Now().Add(time.Minute)
				c.NotAfter = time.Now().Add(2 * time.Minute)
			})}
		}},
		{"wrong-subject", func(t *testing.T, local *identity.Identity) []tls.Certificate {
			return []tls.Certificate{alteredCertificate(t, local, func(c *x509.Certificate) { c.Subject.CommonName = "another protocol" })}
		}},
		{"ca", func(t *testing.T, local *identity.Identity) []tls.Certificate {
			return []tls.Certificate{alteredCertificate(t, local, func(c *x509.Certificate) { c.IsCA = true })}
		}},
		{"extra-name", func(t *testing.T, local *identity.Identity) []tls.Certificate {
			return []tls.Certificate{alteredCertificate(t, local, func(c *x509.Certificate) { c.DNSNames = []string{"example.invalid"} })}
		}},
		{"wrong-usage", func(t *testing.T, local *identity.Identity) []tls.Certificate {
			return []tls.Certificate{alteredCertificate(t, local, func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} })}
		}},
		{"bad-self-signature", func(t *testing.T, local *identity.Identity) []tls.Certificate {
			certificate := alteredCertificate(t, local, func(*x509.Certificate) {})
			der := certificate.Certificate[0]
			der[len(der)-1] ^= 1
			return []tls.Certificate{certificate}
		}},
		{"extra-certificate", func(t *testing.T, local *identity.Identity) []tls.Certificate {
			certificate := alteredCertificate(t, local, func(*x509.Certificate) {})
			certificate.Certificate = append(certificate.Certificate, certificate.Certificate[0])
			return []tls.Certificate{certificate}
		}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			owner, candidate := device(t), device(t)
			id := [16]byte{1}
			serverRaw, clientRaw := net.Pipe()
			t.Cleanup(func() { _ = serverRaw.Close(); _ = clientRaw.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			serverDone := make(chan error, 1)
			go func() { _, err := NewServer(ctx, serverRaw, owner, id, time.Now().Add(time.Minute)); serverDone <- err }()
			config := tlsConfig(tls.Certificate{}, candidate.PublicKey(), owner.PublicKey(), id)
			config.Certificates = item.change(t, candidate)
			client := tls.Client(clientRaw, config)
			_ = client.HandshakeContext(ctx)
			_ = clientRaw.Close()
			if err := <-serverDone; err == nil {
				t.Fatal("unacceptable certificate produced an authenticated pending candidate")
			}
		})
	}
}

func TestTLS12DowngradeCannotEnterPending(t *testing.T) {
	owner, candidate := device(t), device(t)
	id := [16]byte{1}
	expires := time.Now().Add(time.Minute)
	left, right := net.Pipe()
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := NewServer(ctx, left, owner, id, expires); done <- err }()
	certificate, err := newCertificate(candidate, time.Now(), expires)
	if err != nil {
		t.Fatal(err)
	}
	config := tlsConfig(certificate, candidate.PublicKey(), owner.PublicKey(), id)
	config.MinVersion, config.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	_ = tls.Client(right, config).HandshakeContext(ctx)
	_ = right.Close()
	if err := <-done; err == nil {
		t.Fatal("TLS 1.2 produced a pending authenticated channel")
	}
}

func TestHandshakeTimeoutAndConfigurationCloseTheSuppliedTransport(t *testing.T) {
	local, owner := device(t), device(t)
	id := [16]byte{1}
	for _, kind := range []string{"canceled", "timeout", "expired", "too-long", "zero-id", "nil-identity"} {
		t.Run(kind, func(t *testing.T) {
			left, right := net.Pipe()
			defer right.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			expires, inputID, inputIdentity := time.Now().Add(time.Minute), id, local
			expectedErr := ErrCanceled
			switch kind {
			case "canceled":
				cancel()
			case "expired":
				expires, expectedErr = time.Now().Add(-time.Second), ErrExpired
			case "too-long":
				expires, expectedErr = time.Now().Add(2*MaxLifetime), ErrInvalid
			case "zero-id":
				inputID, expectedErr = [16]byte{}, ErrInvalid
			case "nil-identity":
				inputIdentity, expectedErr = nil, ErrInvalid
			}
			if _, err := NewClient(ctx, left, inputIdentity, owner.PublicKey(), inputID, expires); !errors.Is(err, expectedErr) {
				t.Fatalf("unexpected failure category: %v", err)
			}
			_ = right.SetWriteDeadline(time.Now().Add(time.Second))
			if _, err := right.Write([]byte{1}); err == nil {
				t.Fatal("failed constructor left its supplied transport open")
			}
		})
	}
}

func TestDenialOnEitherSideNeverProducesApproval(t *testing.T) {
	for _, denyServer := range []bool{true, false} {
		t.Run(fmt.Sprint(denyServer), func(t *testing.T) {
			server, client := pair(t, time.Now().Add(time.Minute))
			denier, approver := client, server
			if denyServer {
				denier, approver = server, client
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- denier.Deny(ctx) }()
			if proof, err := approver.Approve(ctx, approver.Peer().Fingerprint()); proof != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("denial yielded approval: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnilateralApprovalCancellationBurnsChannel(t *testing.T) {
	_, client := pair(t, time.Now().Add(time.Minute))
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if proof, err := client.Approve(ctx, client.Peer().Fingerprint()); proof != nil || (!errors.Is(err, ErrCanceled) && !errors.Is(err, ErrProtocol)) {
		t.Fatal("single-sided approval produced authority")
	}
	if err := client.WithPendingPeer(func() error { t.Fatal("canceled exchange was reusable"); return nil }); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestWrongFingerprintBurnsChannelWithoutConsent(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	if proof, err := client.Approve(context.Background(), server.Peer().Fingerprint()); proof != nil || !errors.Is(err, ErrFingerprint) {
		t.Fatal("wrong complete fingerprint reached the approval exchange")
	}
	if err := client.WithPendingPeer(func() error { t.Fatal("fingerprint refusal was reusable"); return nil }); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestConcurrentApprovalHasExactlyOneWinner(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	serverDone := make(chan approvalResult, 1)
	go func() {
		proof, err := server.Approve(ctx, server.Peer().Fingerprint())
		serverDone <- approvalResult{proof, err}
	}()
	results := make(chan approvalResult, 16)
	var ready sync.WaitGroup
	for range 16 {
		ready.Add(1)
		go func() {
			defer ready.Done()
			proof, err := client.Approve(ctx, client.Peer().Fingerprint())
			results <- approvalResult{proof, err}
		}()
	}
	ready.Wait()
	close(results)
	winners := 0
	for result := range results {
		if result.err == nil && result.proof != nil {
			winners++
		} else if !errors.Is(result.err, ErrUsed) {
			t.Fatal(result.err)
		}
	}
	if result := <-serverDone; result.err != nil || winners != 1 {
		t.Fatal("concurrent exchange did not select exactly one winner")
	}
}

func TestMalformedOrCrossSessionConsentIsRejected(t *testing.T) {
	for _, change := range []string{"binding", "role", "version", "decision", "truncated"} {
		t.Run(change, func(t *testing.T) {
			server, client := pair(t, time.Now().Add(time.Minute))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan approvalResult, 1)
			go func() {
				proof, err := server.Approve(ctx, server.Peer().Fingerprint())
				done <- approvalResult{proof, err}
			}()
			var frame [frameSize]byte
			copy(frame[:8], "WUPAIR01")
			frame[8], frame[9] = 1, byte(Joiner)
			copy(frame[10:], client.state.binding[:])
			switch change {
			case "binding":
				otherServer, _ := pair(t, time.Now().Add(time.Minute))
				copy(frame[10:], otherServer.state.binding[:])
			case "role":
				frame[9] = byte(Inviter)
			case "version":
				frame[7] = '2'
			case "decision":
				frame[8] = 0
			}
			payload := frame[:]
			if change == "truncated" {
				payload = payload[:1]
			}
			_, _ = client.state.tls.Write(payload)
			_ = client.Close()
			result := <-done
			if result.proof != nil || !errors.Is(result.err, ErrProtocol) {
				t.Fatalf("malformed consent was accepted: %v", result.err)
			}
		})
	}
}

func TestExpiryInvalidatesPreviouslyApprovedCopies(t *testing.T) {
	expires := time.Now().Add(2 * time.Second)
	server, client := pair(t, expires)
	proof, _ := approvePair(t, server, client)
	time.Sleep(time.Until(expires) + 20*time.Millisecond)
	if err := proof.WithLiveApproval(func() error { t.Fatal("expired proof granted authority"); return nil }); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}

func TestCloseWaitsForAdmittedBoundedOperationAndRejectsTheNext(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	proof, _ := approvePair(t, server, client)
	entered, release, operationDone, closeDone := make(chan struct{}), make(chan struct{}), make(chan error, 1), make(chan error, 1)
	go func() {
		operationDone <- proof.WithLiveApproval(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	go func() { closeDone <- server.Close() }()
	select {
	case <-closeDone:
		t.Fatal("Close passed an admitted authorization guard")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := proof.WithLiveApproval(func() error { t.Fatal("closed approval admitted another operation"); return nil }); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestZeroValuesAndFormattingCannotExposeOrManufactureAuthority(t *testing.T) {
	var zeroSession *Session
	var zeroApproval *Approval
	if zeroSession.Local().Valid() || zeroSession.Peer().Valid() || zeroSession.Role() != 0 || !zeroSession.ExpiresAt().IsZero() || zeroApproval.Local().Valid() || zeroApproval.MatchesSession(nil) {
		t.Fatal("zero value acquired identity or session authority")
	}
	if err := zeroApproval.WithLiveApproval(func() error { return nil }); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	server, client := pair(t, time.Now().Add(time.Minute))
	proof, _ := approvePair(t, server, client)
	for _, value := range []any{server, *server, proof, *proof} {
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("sensitive channel state was serialized")
		}
		for _, verb := range []string{"%v", "%+v", "%#v"} {
			text := fmt.Sprintf(verb, value)
			if text != "[private WhatsUp pairing channel]" && text != "[private WhatsUp pairing approval]" {
				t.Fatal("formatting exposed identity, certificate or exporter state")
			}
		}
	}
}
