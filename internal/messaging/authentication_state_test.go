package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

func TestAuthenticatedIncompatibleProtocolStopsUntilExplicitRetryAndPreservesQueue(t *testing.T) {
	m, store, profiles := newBlockedManager(t)
	store.armed = false
	if _, err := m.conversation.Queue("synthetic retained text awaiting compatible build"); err != nil {
		t.Fatal(err)
	}
	before := m.record
	m.factories.Profile = func(context.Context, string) (*transport.RelayProfile, error) { profiles.Add(1); return nil, nil }
	// This trusted factory fixture models the typed failure from sessionauth.
	// Real old/new TLS, wrong-key and unsupported-protocol cases are exercised
	// independently in sessionauth; arbitrary TLS alerts never get this type.
	m.factories.Listen = func(context.Context, storage.PendingRecord, *transport.RelayProfile) (Listener, error) {
		return nil, sessionauth.ErrIncompatible
	}
	for attempt := int32(1); attempt <= 2; attempt++ {
		if _, err := m.Connect(before.ID); err != nil {
			t.Fatal("explicit retry was rejected after compatibility change")
		}
		joined := make(chan struct{})
		go func() { m.workers.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Fatal("incompatible protocol kept retrying automatically")
		}
		status := m.Snapshot()
		if status.State != "update-required" || !status.CanConnect || !status.CanSend || m.ctx.Err() != nil || profiles.Load() != attempt {
			t.Fatal("compatibility failure did not pause retries while permitting explicit recovery")
		}
		if len(status.Messages) != 1 || status.Messages[0].Delivered || len(m.conversation.Outbox()) != 1 || m.record != before || status.PeerFingerprint != before.Peer.Fingerprint() {
			t.Fatal("compatibility failure changed identity, queue or delivery state")
		}
	}
}

func TestOtherAuthenticationFailuresExplainRetriesWithoutClaimingVerifiedVersion(t *testing.T) {
	for _, failure := range []error{sessionauth.ErrAuthentication, errors.New("remote error: tls: no application protocol")} {
		t.Run(failure.Error(), func(t *testing.T) {
			m, store, profiles := newBlockedManager(t)
			store.armed = false
			before := m.record
			m.factories.Profile = func(context.Context, string) (*transport.RelayProfile, error) { profiles.Add(1); return nil, nil }
			m.factories.Listen = func(context.Context, storage.PendingRecord, *transport.RelayProfile) (Listener, error) {
				return nil, failure
			}
			if _, err := m.Connect(before.ID); err != nil {
				t.Fatal(err)
			}
			expected := "connecting"
			if errors.Is(failure, sessionauth.ErrAuthentication) {
				expected = "authentication-failed"
			}
			deadline := time.Now().Add(time.Second)
			var status Snapshot
			for time.Now().Before(deadline) {
				status = m.Snapshot()
				if profiles.Load() > 0 && status.State == expected {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if status.State != expected || status.CanConnect || m.ctx.Err() != nil || m.record != before {
				t.Fatal("retryable failure was hidden or treated as verified incompatibility")
			}
			m.Close()
		})
	}
}
