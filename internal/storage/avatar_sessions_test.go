package storage

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestFreshAvatarTLSLeaseOverridesDurableFloorAndRevokesOldSession(t *testing.T) {
	f := makeRouteFixture(t)
	planA, err := f.a.ConnectionPlan(f.ra.ID)
	if err != nil {
		t.Fatal(err)
	}
	planB, err := f.b.ConnectionPlan(f.rb.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldTLS, oldPeerTLS := routeSessions(t, f, planA.Candidates[0], planB.Candidates[0])
	oldProof, err := planA.Candidates[0].Authenticated(oldTLS)
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.a.OpenPeerAvatarSession(context.Background(), oldProof)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	data := avatarFixture(t, 57)
	if changed, err := old.Apply(100, data); err != nil || !changed {
		t.Fatal("first authenticated snapshot failed", err)
	}
	newTLS, _ := routeSessions(t, f, planA.Candidates[0], planB.Candidates[0])
	newProof, err := planA.Candidates[0].Authenticated(newTLS)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current, err := f.a.OpenPeerAvatarSession(ctx, newProof)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if changed, err := current.Apply(1, nil); err != nil || !changed {
		t.Fatal("fresh session could not clear stored higher revision", err)
	}
	if oldTLS.WithLivePeer(f.ra.Peer, func() error { return nil }) != nil {
		t.Fatal("test requires old TLS still live to exercise lease supersession")
	}
	// The old authenticated stream can still deliver its previously shared
	// image; storage must reject that worker after the new stream's clear.
	if err := oldTLS.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() { _, err := oldPeerTLS.Write(data); sent <- err }()
	replayed := make([]byte, len(data))
	if _, err := io.ReadFull(oldTLS, replayed); err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if _, err := old.Apply(101, replayed); err == nil {
		t.Fatal("old TLS writer resurrected withdrawn picture")
	}
	if _, err := f.a.OpenPeerAvatarSession(context.Background(), oldProof); err == nil {
		t.Fatal("old TLS reopened authoritative scope after supersession")
	}
	oldCopy := *oldTLS
	copyProof, err := planA.Candidates[0].Authenticated(&oldCopy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.OpenPeerAvatarSession(context.Background(), copyProof); err == nil {
		t.Fatal("copied TLS wrapper reset first-snapshot authority")
	}
	old.Close()
	if changed, err := current.Apply(2, data); err != nil || !changed {
		t.Fatal("closing old writer retired replacement or prevented re-sharing", err)
	}
	if changed, err := current.Apply(1, nil); err != nil || changed {
		t.Fatal("lower revision changed this session", err)
	}
	if _, err := current.Apply(2, nil); err == nil {
		t.Fatal("same revision changed bytes")
	}
	if err := f.a.SetPeerAvatar(context.Background(), f.ra.ID, f.ra.Peer, 999, nil); err == nil {
		t.Fatal("legacy raw writer bypassed live session lease")
	}
	if len(f.a.avatarSessions) != 1 {
		t.Fatal("superseded sessions leaked slots")
	}
	cancel()
	if _, err := current.Apply(3, nil); err == nil {
		t.Fatal("cancelled session retained writer authority")
	}
	current.Close()
	if len(f.a.avatarSessions) != 0 {
		t.Fatal("closed session retained lease")
	}
	if _, err := f.a.OpenPeerAvatarSession(context.Background(), newProof); err == nil {
		t.Fatal("closing lease allowed same TLS to regain first-snapshot authority")
	}
	if _, err := f.a.OpenPeerAvatarSession(context.Background(), RouteSessionProof{}); err == nil {
		t.Fatal("forged route proof granted avatar authority")
	}
}

func TestAvatarTLSLeaseDiesOnForgetAndVaultClose(t *testing.T) {
	for _, forget := range []bool{true, false} {
		t.Run(map[bool]string{true: "forget", false: "close"}[forget], func(t *testing.T) {
			f := makeRouteFixture(t)
			a, _ := f.a.ConnectionPlan(f.ra.ID)
			b, _ := f.b.ConnectionPlan(f.rb.ID)
			tls, _ := routeSessions(t, f, a.Candidates[0], b.Candidates[0])
			proof, err := a.Candidates[0].Authenticated(tls)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := f.a.OpenPeerAvatarSession(context.Background(), proof)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if _, err := lease.Apply(1, avatarFixture(t, 19)); err != nil {
				t.Fatal(err)
			}
			if forget {
				err = f.a.ForgetCommittedContact(f.ra, func() error { return nil })
			} else {
				err = f.a.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(f.a.avatarSessions) != 0 {
				t.Fatal("retired profile/contact kept active avatar lease")
			}
			if _, err := lease.Apply(2, nil); err == nil {
				t.Fatal("retired writer could change contact photo")
			}
		})
	}
}
