package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type completionResult struct {
	proof *Completion
	err   error
}

func provisionedCompletionPair(t *testing.T) (*Session, *Session, *Reservation, *Reservation, *Approval, *Approval, ActivationAssociation) {
	t.Helper()
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, cr := reservePair(t, server, client)
	sp, cp := consentPair(t, server, client, sr, cr)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sp.ExchangePending(ctx, sr, ContactOffer{Node: [32]byte{1}, ID: [16]byte{3}, PresharedKey: [32]byte{4}}, func(ContactAgreement) error { return nil })
		done <- err
	}()
	if _, err := cp.ExchangePending(ctx, cr, ContactOffer{Node: [32]byte{2}}, func(ContactAgreement) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	server.state.mu.Lock()
	association := server.state.activation
	server.state.mu.Unlock()
	client.state.mu.Lock()
	other := client.state.activation
	client.state.mu.Unlock()
	if !association.Valid() || !association.Equal(other) || !association.InviterIdentity.Equal(server.Local()) || !association.JoinerIdentity.Equal(client.Local()) {
		t.Fatal("pending exchange did not pin one normalized association")
	}
	return server, client, sr, cr, sp, cp, association
}

func completedPair(t *testing.T) (*Session, *Session, *Completion, *Completion, ActivationAssociation) {
	t.Helper()
	server, client, sr, cr, sp, cp, association := provisionedCompletionPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var localSaved, remoteSaved atomic.Bool
	done := make(chan completionResult, 1)
	go func() {
		p, e := sp.CompleteActivation(ctx, sr, association, func() error { localSaved.Store(true); return nil })
		done <- completionResult{p, e}
	}()
	other, err := cp.CompleteActivation(ctx, cr, association, func() error { remoteSaved.Store(true); return nil })
	if err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.proof == nil || !localSaved.Load() || !remoteSaved.Load() {
		t.Fatal("completion escaped before both durable callbacks")
	}
	return server, client, got.proof, other, association
}

func TestActivationCompletionPinsActualProvisionedAssociationAndConsumesOnce(t *testing.T) {
	server, client, proof, other, association := completedPair(t)
	if !proof.MatchesSession(server) || proof.MatchesSession(client) || !other.MatchesSession(client) || !proof.MatchesAssociation(association) || !proof.Association().Equal(other.Association()) {
		t.Fatal("completion did not bind the exact session and credential association")
	}
	copyProof := *proof
	var admitted atomic.Int32
	if err := copyProof.ConsumeLiveOperation(func() error { admitted.Add(1); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := proof.ConsumeLiveOperation(func() error { t.Fatal("reused completion callback ran"); return nil }); !errors.Is(err, ErrUsed) || admitted.Load() != 1 {
		t.Fatal("copied completion was consumed twice")
	}
	if err := other.WithLiveCompletion(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if err := other.WithLiveCompletion(func() error { t.Fatal("closed proof callback ran"); return nil }); err == nil {
		t.Fatal("closed completion retained authority")
	}
	if !other.MatchesAssociation(association) {
		t.Fatal("immutable association facts were lost after closure")
	}
}

func TestActivationRejectsUnprovisionedForgedAndMismatchedFacts(t *testing.T) {
	changes := map[string]func(*ActivationAssociation){
		"id": func(a *ActivationAssociation) { a.ID[0] ^= 1 },
		"identity": func(a *ActivationAssociation) {
			a.InviterIdentity, a.JoinerIdentity = a.JoinerIdentity, a.InviterIdentity
		},
		"node": func(a *ActivationAssociation) { a.InviterNode, a.JoinerNode = a.JoinerNode, a.InviterNode },
		"psk":  func(a *ActivationAssociation) { a.PSKCommitment[0] ^= 1 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			_, _, sr, _, sp, _, association := provisionedCompletionPair(t)
			change(&association)
			if proof, err := sp.CompleteActivation(context.Background(), sr, association, func() error { t.Fatal("mismatched record persisted"); return nil }); proof != nil || err == nil {
				t.Fatal("caller replaced actual provisioned association")
			}
		})
	}
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, cr := reservePair(t, server, client)
	sp, _ := consentPair(t, server, client, sr, cr)
	valid := ActivationAssociation{ID: [16]byte{1}, InviterIdentity: server.Local(), JoinerIdentity: client.Local(), InviterNode: [32]byte{1}, JoinerNode: [32]byte{2}, PSKCommitment: [32]byte{3}}
	if proof, err := sp.CompleteActivation(context.Background(), sr, valid, func() error { t.Fatal("unprovisioned record persisted"); return nil }); proof != nil || !errors.Is(err, ErrUsed) {
		t.Fatal("unprovisioned approval completed activation")
	}
	if _, err := (&Approval{}).CompleteActivation(context.Background(), sr, valid, func() error { return nil }); err == nil {
		t.Fatal("zero approval completed")
	}
	if _, err := sp.CompleteActivation(context.Background(), cr, valid, func() error { return nil }); err == nil {
		t.Fatal("foreign reservation completed")
	}
}

func TestActivationFailedWriteCancelsBothSidesWithoutProof(t *testing.T) {
	for _, failing := range []Role{Inviter, Joiner} {
		t.Run(fmt.Sprint(failing), func(t *testing.T) {
			_, _, sr, cr, sp, cp, association := provisionedCompletionPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan completionResult, 1)
			go func() {
				p, e := sp.CompleteActivation(ctx, sr, association, func() error {
					if failing == Inviter {
						return errors.New("synthetic private write failure")
					}
					return nil
				})
				done <- completionResult{p, e}
			}()
			p, e := cp.CompleteActivation(ctx, cr, association, func() error {
				if failing == Joiner {
					return errors.New("synthetic private write failure")
				}
				return nil
			})
			got := <-done
			if p != nil || got.proof != nil || e == nil || got.err == nil {
				t.Fatal("failed commit granted activation")
			}
			if strings.Contains(e.Error(), "synthetic") || strings.Contains(got.err.Error(), "synthetic") {
				t.Fatal("raw persistence error leaked")
			}
		})
	}
}

// sendManualCommit exercises the final handoff independently of the joiner's
// CompleteActivation implementation, so a missing receipt cannot be hidden by
// two peers sharing the same sequencing mistake.
func sendManualCommit(t *testing.T, client *Session, association ActivationAssociation) {
	t.Helper()
	if err := client.state.raw.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	wire := association.encode()
	packet := makePacket("WUCOM001", 1, Joiner, client.state.binding, associationSize)
	copy(packet[frameSize:], wire[:])
	if err := writePacket(client.state.tls, packet); err != nil {
		t.Fatal(err)
	}
	clear(packet)
	got, err := readPacket(client.state.tls, "WUCOM001", 1, Inviter, client.state.binding, associationSize)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(got)
	if string(got[frameSize:]) != string(wire[:]) {
		t.Fatal("inviter committed a different association")
	}
}

func TestActivationWaitsForTerminalReceiptAndCleanHandoff(t *testing.T) {
	_, client, sr, _, sp, _, association := provisionedCompletionPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan completionResult, 1)
	go func() {
		proof, err := sp.CompleteActivation(ctx, sr, association, func() error { return nil })
		done <- completionResult{proof, err}
	}()
	sendManualCommit(t, client, association)
	select {
	case <-done:
		t.Fatal("inviter completed before the joiner acknowledged its COMMITTED frame")
	case <-time.After(20 * time.Millisecond):
	}
	wire := association.encode()
	packet := makePacket("WUCOM001", 2, Joiner, client.state.binding, associationSize)
	copy(packet[frameSize:], wire[:])
	if err := writePacket(client.state.tls, packet); err != nil {
		t.Fatal(err)
	}
	clear(packet)
	select {
	case <-done:
		t.Fatal("inviter completed before an authenticated stream end")
	case <-time.After(20 * time.Millisecond):
	}
	if err := client.state.tls.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	var extra [1]byte
	if n, err := client.state.tls.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatal("inviter did not acknowledge the authenticated stream end")
	}
	if err := client.state.raw.Close(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.proof == nil || got.err != nil {
		t.Fatalf("clean terminal handoff failed: %v", got.err)
	}
	if err := got.proof.ConsumeLiveOperation(func() error { return nil }); err != nil {
		t.Fatal("successful transport retirement invalidated immediate activation proof")
	}
}

func TestActivationRejectsMissingOrMalformedTerminalReceipts(t *testing.T) {
	for _, corrupt := range []string{"magic", "kind", "binding", "role", "association", "version", "truncated", "missing"} {
		t.Run(corrupt, func(t *testing.T) {
			_, client, sr, _, sp, _, association := provisionedCompletionPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan completionResult, 1)
			go func() {
				proof, err := sp.CompleteActivation(ctx, sr, association, func() error { return nil })
				done <- completionResult{proof, err}
			}()
			sendManualCommit(t, client, association)
			wire := association.encode()
			packet := makePacket("WUCOM001", 2, Joiner, client.state.binding, associationSize)
			copy(packet[frameSize:], wire[:])
			switch corrupt {
			case "magic":
				packet[0] ^= 1
			case "kind":
				packet[8] = 1 // replaying COMMITTED cannot acknowledge receipt.
			case "binding":
				packet[10] ^= 1
			case "role":
				packet[9] = byte(Inviter)
			case "association":
				packet[frameSize+1] ^= 1
			case "version":
				packet[frameSize] = 2
			case "truncated":
				packet = packet[:len(packet)-1]
			case "missing":
				packet = nil
			}
			if len(packet) != 0 {
				_ = writePacket(client.state.tls, packet)
			}
			clear(packet)
			if corrupt == "missing" || corrupt == "truncated" {
				_ = client.Close()
			}
			got := <-done
			if got.proof != nil || got.err == nil {
				t.Fatal("unacknowledged or mismatched final commit granted activation")
			}
		})
	}
}

func TestActivationRejectsUncleanOrAdditionalTerminalData(t *testing.T) {
	for _, ending := range []string{"unclean", "extra"} {
		t.Run(ending, func(t *testing.T) {
			_, client, sr, _, sp, _, association := provisionedCompletionPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan completionResult, 1)
			go func() {
				proof, err := sp.CompleteActivation(ctx, sr, association, func() error { return nil })
				done <- completionResult{proof, err}
			}()
			sendManualCommit(t, client, association)
			wire := association.encode()
			packet := makePacket("WUCOM001", 2, Joiner, client.state.binding, associationSize)
			copy(packet[frameSize:], wire[:])
			if err := writePacket(client.state.tls, packet); err != nil {
				t.Fatal(err)
			}
			clear(packet)
			if ending == "extra" {
				_, _ = client.state.tls.Write([]byte{1})
			} else {
				_ = client.Close()
			}
			got := <-done
			if got.proof != nil || got.err == nil {
				t.Fatal("unverified invitation stream end granted activation")
			}
		})
	}
}

func TestActivationRejectsMalformedAndCrossSessionCommitFrames(t *testing.T) {
	for _, corrupt := range []string{"binding", "role", "association", "version", "truncated"} {
		t.Run(corrupt, func(t *testing.T) {
			_, client, sr, _, sp, _, association := provisionedCompletionPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan completionResult, 1)
			go func() {
				p, e := sp.CompleteActivation(ctx, sr, association, func() error { return nil })
				done <- completionResult{p, e}
			}()
			wire := association.encode()
			packet := makePacket("WUCOM001", 1, Joiner, client.state.binding, associationSize)
			copy(packet[frameSize:], wire[:])
			switch corrupt {
			case "binding":
				packet[10] ^= 1
			case "role":
				packet[9] = byte(Inviter)
			case "association":
				packet[frameSize+1] ^= 1
			case "version":
				packet[frameSize] = 2
			case "truncated":
				packet = packet[:len(packet)-1]
			}
			_ = writePacket(client.state.tls, packet)
			if corrupt == "truncated" {
				_ = client.Close()
			}
			got := <-done
			if got.proof != nil || got.err == nil {
				t.Fatal("malformed commit frame granted activation")
			}
		})
	}
}

func TestActivationCancellationExpiryAndConcurrentSingleWinner(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		_, _, sr, _, sp, _, association := provisionedCompletionPair(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if p, e := sp.CompleteActivation(ctx, sr, association, func() error { t.Fatal("canceled callback ran"); return nil }); p != nil || !errors.Is(e, ErrCanceled) {
			t.Fatal("canceled completion escaped")
		}
	})
	t.Run("expired", func(t *testing.T) {
		server, _, sr, _, sp, _, association := provisionedCompletionPair(t)
		server.state.mu.Lock()
		server.state.expires = time.Now().Add(-time.Second)
		server.state.mu.Unlock()
		if p, e := sp.CompleteActivation(context.Background(), sr, association, func() error { t.Fatal("expired callback ran"); return nil }); p != nil || !errors.Is(e, ErrExpired) {
			t.Fatal("expired completion escaped")
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		_, _, sr, cr, sp, cp, association := provisionedCompletionPair(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var saved, winners atomic.Int32
		var group sync.WaitGroup
		for range 16 {
			group.Add(1)
			go func() {
				defer group.Done()
				p, e := sp.CompleteActivation(ctx, sr, association, func() error { saved.Add(1); return nil })
				if e == nil && p != nil {
					winners.Add(1)
				} else if !errors.Is(e, ErrUsed) {
					t.Errorf("unexpected completion failure: %v", e)
				}
			}()
		}
		if p, e := cp.CompleteActivation(ctx, cr, association, func() error { return nil }); p == nil || e != nil {
			t.Fatal("peer completion failed")
		}
		group.Wait()
		if saved.Load() != 1 || winners.Load() != 1 {
			t.Fatal("concurrent completion persisted or activated twice")
		}
	})
}

func TestActivationCapabilitiesDoNotSerializeOrManufacturePermission(t *testing.T) {
	for _, value := range []any{ActivationAssociation{}, Completion{}, &Completion{}} {
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("private activation state serialized")
		}
		if got := fmt.Sprintf("%#v", value); !strings.HasPrefix(got, "[private WhatsUp") {
			t.Fatal("private activation formatter leaked")
		}
	}
	if err := (&Completion{}).ConsumeLiveOperation(func() error { t.Fatal("forged completion callback ran"); return nil }); err == nil {
		t.Fatal("zero completion manufactured authority")
	}
}
