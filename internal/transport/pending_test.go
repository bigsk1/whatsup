package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/identity"
	"whatsup/internal/pairing/channel"
	"whatsup/internal/storage"
)

func pendingProofs(t *testing.T) (*channel.Reservation, *channel.Reservation, *channel.Approval, *channel.Approval) {
	t.Helper()
	owner, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	joiner, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		s   *channel.Session
		err error
	}
	serverDone := make(chan result, 1)
	id := [16]byte{9}
	expires := time.Now().Add(time.Minute)
	go func() { s, e := channel.NewServer(ctx, a, owner, id, expires); serverDone <- result{s, e} }()
	client, err := channel.NewClient(ctx, b, joiner, owner.PublicKey(), id, expires)
	if err != nil {
		t.Fatal(err)
	}
	server := <-serverDone
	if server.err != nil {
		t.Fatal(server.err)
	}
	t.Cleanup(func() { _ = server.s.Close(); _ = client.Close() })
	type reserved struct {
		r   *channel.Reservation
		err error
	}
	reservationDone := make(chan reserved, 1)
	go func() {
		r, e := server.s.AcceptReservation(ctx, func(link string) error {
			if link != "synthetic-bearer" {
				return errors.New("rejected")
			}
			return nil
		})
		reservationDone <- reserved{r, e}
	}()
	cr, err := client.RequestReservation(ctx, "synthetic-bearer")
	if err != nil {
		t.Fatal(err)
	}
	sr := <-reservationDone
	if sr.err != nil {
		t.Fatal(sr.err)
	}
	sd, cd := make(chan channel.Decision, 1), make(chan channel.Decision, 1)
	sd <- channel.Decision{Approve: true, Fingerprint: joiner.PublicKey().Fingerprint()}
	cd <- channel.Decision{Approve: true, Fingerprint: owner.PublicKey().Fingerprint()}
	type approved struct {
		a   *channel.Approval
		err error
	}
	approvedDone := make(chan approved, 1)
	go func() { a, e := sr.r.WaitApproval(ctx, sd); approvedDone <- approved{a, e} }()
	ca, err := cr.WaitApproval(ctx, cd)
	if err != nil {
		t.Fatal(err)
	}
	sa := <-approvedDone
	if sa.err != nil {
		t.Fatal(sa.err)
	}
	return sr.r, cr, sa.a, ca
}

func TestPendingTransportUsesOnePSKAndSeparateLocalNodes(t *testing.T) {
	sr, cr, sa, ca := pendingProofs(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type result struct {
		p   *PendingCredentials
		err error
	}
	done := make(chan result, 1)
	var ownerSaved, joinerSaved storage.PendingRecord
	go func() {
		p, e := ExchangePending(ctx, sr, sa, func(record storage.PendingRecord) error { ownerSaved = record; return nil })
		done <- result{p, e}
	}()
	joined, err := ExchangePending(ctx, cr, ca, func(record storage.PendingRecord) error { joinerSaved = record; return nil })
	if err != nil {
		t.Fatal(err)
	}
	owner := <-done
	if owner.err != nil {
		t.Fatal(owner.err)
	}
	if storage.ValidatePendingRecord(ownerSaved) != nil || storage.ValidatePendingRecord(joinerSaved) != nil || ownerSaved.ID != joinerSaved.ID || ownerSaved.PresharedKey != joinerSaved.PresharedKey || ownerSaved.LocalNode != joinerSaved.PeerNode || ownerSaved.PeerNode != joinerSaved.LocalNode || ownerSaved.LocalNodePrivate == joinerSaved.LocalNodePrivate || ownerSaved.Role != "inviter" || joinerSaved.Role != "joiner" {
		t.Fatal("pending bundle did not preserve the exact shared PSK and two separate node keys")
	}
	if !ownerSaved.Local.Equal(joinerSaved.Peer) || !ownerSaved.Peer.Equal(joinerSaved.Local) {
		t.Fatal("pending credentials crossed approved identities")
	}
	for _, p := range []*PendingCredentials{owner.p, joined} {
		if _, err := p.RecordForEncryptedStorage(); err != nil {
			t.Fatal(err)
		}
		if _, ok := any(p).(storage.ApprovedContact); ok {
			t.Fatal("pending keys granted chat authority")
		}
		if _, err := json.Marshal(p); err == nil {
			t.Fatal("pending credential serialized")
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if got := fmt.Sprintf(format, *p); !strings.HasPrefix(got, "[private pending") {
				t.Fatal("pending formatter exposed credential fields")
			}
		}
		copy := *p
		p.Revoke()
		if _, err := copy.RecordForEncryptedStorage(); err == nil {
			t.Fatal("copied pending bundle survived revocation")
		}
	}
}

func TestPendingTransportRejectsForgedOrCrossSessionConsent(t *testing.T) {
	sr, cr, sa, ca := pendingProofs(t)
	for _, pair := range []struct {
		r *channel.Reservation
		a *channel.Approval
	}{{nil, sa}, {sr, nil}, {&channel.Reservation{}, sa}, {sr, &channel.Approval{}}, {sr, ca}, {cr, sa}} {
		if p, err := ExchangePending(context.Background(), pair.r, pair.a, func(storage.PendingRecord) error { t.Fatal("invalid proof reached persistence"); return nil }); p != nil || !errors.Is(err, ErrPendingCredentials) {
			t.Fatal("forged or mixed proof provisioned credentials")
		}
	}
}

func TestConcurrentPendingFactoriesHaveOnePersistedWinner(t *testing.T) {
	sr, cr, sa, ca := pendingProofs(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var saves atomic.Int32
	type result struct {
		p   *PendingCredentials
		err error
	}
	done := make(chan result, 2)
	for range 2 {
		go func() {
			p, e := ExchangePending(ctx, sr, sa, func(storage.PendingRecord) error { saves.Add(1); return nil })
			done <- result{p, e}
		}()
	}
	joined, err := ExchangePending(ctx, cr, ca, func(storage.PendingRecord) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer joined.Revoke()
	winners := 0
	for range 2 {
		got := <-done
		if got.err == nil {
			winners++
			got.p.Revoke()
		} else if !errors.Is(got.err, ErrPendingCredentials) {
			t.Fatal(got.err)
		}
	}
	if winners != 1 || saves.Load() != 1 {
		t.Fatal("concurrent factories persisted more than one bundle")
	}
}

func TestPendingPersistenceFailureReturnsNoCredentialCapability(t *testing.T) {
	sr, cr, sa, ca := pendingProofs(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type result struct {
		p   *PendingCredentials
		err error
	}
	done := make(chan result, 1)
	var ownerSaves atomic.Int32
	go func() {
		p, e := ExchangePending(ctx, sr, sa, func(storage.PendingRecord) error { ownerSaves.Add(1); return nil })
		done <- result{p, e}
	}()
	p, err := ExchangePending(ctx, cr, ca, func(storage.PendingRecord) error { return errors.New("synthetic uncertain save") })
	owner := <-done
	if p != nil || owner.p != nil || err == nil || owner.err == nil || ownerSaves.Load() != 1 {
		t.Fatal("failed peer save became completed credential exchange")
	}
	if _, err := ExchangePending(ctx, sr, sa, func(storage.PendingRecord) error {
		t.Fatal("uncertain exchange silently minted another bundle")
		return nil
	}); err == nil {
		t.Fatal("failed exchange could restart")
	}
}
