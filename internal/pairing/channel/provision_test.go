package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type reservationResult struct {
	proof *Reservation
	err   error
}

func reservePair(t *testing.T, server, client *Session) (*Reservation, *Reservation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan reservationResult, 1)
	go func() {
		r, err := server.AcceptReservation(ctx, func(link string) error {
			if link != "synthetic-bearer" {
				return ErrReservation
			}
			return nil
		})
		done <- reservationResult{r, err}
	}()
	clientReceipt, err := client.RequestReservation(ctx, "synthetic-bearer")
	if err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !got.proof.MatchesSession(server) || !clientReceipt.MatchesSession(client) || got.proof.MatchesSession(client) {
		t.Fatal("reservation was not session-specific")
	}
	return got.proof, clientReceipt
}

func consentPair(t *testing.T, server, client *Session, sr, cr *Reservation) (*Approval, *Approval) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sd, cd := make(chan Decision, 1), make(chan Decision, 1)
	sd <- Decision{true, server.Peer().Fingerprint()}
	cd <- Decision{true, client.Peer().Fingerprint()}
	done := make(chan approvalResult, 1)
	go func() { proof, err := sr.WaitApproval(ctx, sd); done <- approvalResult{proof, err} }()
	cp, err := cr.WaitApproval(ctx, cd)
	if err != nil {
		t.Fatal(err)
	}
	sp := <-done
	if sp.err != nil {
		t.Fatal(sp.err)
	}
	return sp.proof, cp
}

func TestBearerReservationIsAcknowledgedBeforeConsentAndNeverReused(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, cr := reservePair(t, server, client)
	if _, err := client.RequestReservation(context.Background(), "synthetic-bearer"); !errors.Is(err, ErrUsed) {
		t.Fatal("reservation was repeated")
	}
	sp, cp := consentPair(t, server, client, sr, cr)
	if err := sr.WithLiveApproval(sp, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := sr.WithLiveApproval(cp, func() error { t.Fatal("cross-session proof admitted"); return nil }); err == nil {
		t.Fatal("proof crossed TLS sessions")
	}
}

func TestRejectedBearerAndOversizedInputBurnChannel(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := server.AcceptReservation(ctx, func(string) error { return errors.New("synthetic rejection") })
		done <- err
	}()
	if receipt, err := client.RequestReservation(ctx, "synthetic-bearer"); receipt != nil || err == nil {
		t.Fatal("rejected bearer got receipt")
	}
	if err := <-done; !errors.Is(err, ErrReservation) {
		t.Fatal("reservation failure was not redacted")
	}
	if err := server.WithPendingPeer(func() error { t.Fatal("closed channel admitted candidate"); return nil }); err == nil {
		t.Fatal("rejected channel remained pending")
	}
	_, other := pair(t, time.Now().Add(time.Minute))
	if _, err := other.RequestReservation(ctx, strings.Repeat("x", maxInvitationBytes+1)); !errors.Is(err, ErrReservation) {
		t.Fatal("oversized link accepted")
	}
}

func TestWaitApprovalDenialDismissesUnclickedPeer(t *testing.T) {
	for _, denier := range []Role{Inviter, Joiner} {
		t.Run(fmt.Sprint(denier), func(t *testing.T) {
			server, client := pair(t, time.Now().Add(time.Minute))
			sr, cr := reservePair(t, server, client)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			sd, cd := make(chan Decision, 1), make(chan Decision, 1)
			if denier == Inviter {
				sd <- Decision{Approve: false}
			} else {
				cd <- Decision{Approve: false}
			}
			done := make(chan approvalResult, 1)
			go func() { proof, err := sr.WaitApproval(ctx, sd); done <- approvalResult{proof, err} }()
			proof, err := cr.WaitApproval(ctx, cd)
			other := <-done
			if proof != nil || other.proof != nil || !errors.Is(err, ErrDenied) || !errors.Is(other.err, ErrDenied) {
				t.Fatalf("denial did not terminate both pending prompts: %v / %v", err, other.err)
			}
		})
	}
}

func TestPeerCloseAfterOneVoteDismissesOtherUnclickedPrompt(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, cr := reservePair(t, server, client)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sd, cd := make(chan Decision), make(chan Decision, 1)
	cd <- Decision{true, client.Peer().Fingerprint()}
	done := make(chan approvalResult, 2)
	go func() { p, e := sr.WaitApproval(ctx, sd); done <- approvalResult{p, e} }()
	go func() { p, e := cr.WaitApproval(ctx, cd); done <- approvalResult{p, e} }()
	time.Sleep(20 * time.Millisecond)
	_ = client.Close()
	for range 2 {
		select {
		case got := <-done:
			if got.proof != nil || got.err == nil {
				t.Fatal("disconnect granted approval")
			}
		case <-time.After(time.Second):
			t.Fatal("unanswered prompt ignored peer disconnect")
		}
	}
}

func TestPrematureReadyCannotReplaceTheLocalChoice(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, _ := reservePair(t, server, client)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan approvalResult, 1)
	go func() { proof, err := sr.WaitApproval(ctx, make(chan Decision)); done <- approvalResult{proof, err} }()
	if err := writePacket(client.state.tls, makePacket("WUPAIR01", 1, Joiner, client.state.binding, 0)); err != nil {
		t.Fatal(err)
	}
	if err := writePacket(client.state.tls, makePacket("WUREADY1", 3, Joiner, client.state.binding, 0)); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.proof != nil || !errors.Is(got.err, ErrProtocol) {
		t.Fatal("premature peer handoff replaced local consent")
	}
}

func TestProvisionRejectsReflectedNodeAndForeignSessionFrameBeforePersistence(t *testing.T) {
	for _, mutation := range []string{"same-node", "foreign-session"} {
		t.Run(mutation, func(t *testing.T) {
			server, client := pair(t, time.Now().Add(time.Minute))
			sr, cr := reservePair(t, server, client)
			sp, _ := consentPair(t, server, client, sr, cr)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := sp.ExchangePending(ctx, sr, ContactOffer{Node: [32]byte{1}, ID: [16]byte{2}, PresharedKey: [32]byte{3}}, func(ContactAgreement) error { t.Error("invalid frame reached pending persistence"); return nil })
				done <- err
			}()
			binding := client.state.binding
			if mutation == "foreign-session" {
				binding[0] ^= 1
			}
			packet := makePacket("WUPRE001", 1, Joiner, binding, 32)
			packet[frameSize] = 1
			if err := writePacket(client.state.tls, packet); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("reflected node or unrelated session frame was accepted")
			}
		})
	}
}

func TestPendingAgreementPersistsBothSidesAndRejectsReuse(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, cr := reservePair(t, server, client)
	sp, cp := consentPair(t, server, client, sr, cr)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ownerOffer := ContactOffer{Node: [32]byte{1}, ID: [16]byte{3}, PresharedKey: [32]byte{4}}
	joinerOffer := ContactOffer{Node: [32]byte{2}}
	type result struct {
		agreement ContactAgreement
		err       error
	}
	done := make(chan result, 1)
	var ownerSaved, joinerSaved ContactAgreement
	go func() {
		a, e := sp.ExchangePending(ctx, sr, ownerOffer, func(a ContactAgreement) error { ownerSaved = a; return nil })
		done <- result{a, e}
	}()
	joined, err := cp.ExchangePending(ctx, cr, joinerOffer, func(a ContactAgreement) error { joinerSaved = a; return nil })
	if err != nil {
		t.Fatal(err)
	}
	owner := <-done
	if owner.err != nil {
		t.Fatal(owner.err)
	}
	if ownerSaved.ID != joined.ID || joinerSaved.ID != owner.agreement.ID || ownerSaved.PresharedKey != joinerSaved.PresharedKey || ownerSaved.LocalNode != joinerSaved.PeerNode || ownerSaved.PeerNode != joinerSaved.LocalNode {
		t.Fatal("persisted records did not bind one exact credential set")
	}
	if _, err := sp.ExchangePending(ctx, sr, ownerOffer, func(ContactAgreement) error { t.Fatal("reused pending exchange persisted"); return nil }); !errors.Is(err, ErrUsed) {
		t.Fatal("exchange was reused")
	}
}

func TestPersistenceFailureNeverAcknowledgesPreparedPeer(t *testing.T) {
	for _, failingRole := range []Role{Inviter, Joiner} {
		t.Run(fmt.Sprint(failingRole), func(t *testing.T) {
			server, client := pair(t, time.Now().Add(time.Minute))
			sr, cr := reservePair(t, server, client)
			sp, cp := consentPair(t, server, client, sr, cr)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := sp.ExchangePending(ctx, sr, ContactOffer{Node: [32]byte{1}, ID: [16]byte{3}, PresharedKey: [32]byte{4}}, func(ContactAgreement) error {
					if failingRole == Inviter {
						return errors.New("synthetic write failure")
					}
					return nil
				})
				done <- err
			}()
			_, err := cp.ExchangePending(ctx, cr, ContactOffer{Node: [32]byte{2}}, func(ContactAgreement) error {
				if failingRole == Joiner {
					return errors.New("synthetic write failure")
				}
				return nil
			})
			if err == nil || <-done == nil {
				t.Fatal("failed persistence received a prepared acknowledgement")
			}
		})
	}
}

func TestReservationAndOffersCannotSerializeSecretsOrForgeConsent(t *testing.T) {
	for _, value := range []any{Reservation{}, ContactOffer{PresharedKey: [32]byte{7}}, ContactAgreement{PresharedKey: [32]byte{8}}} {
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("sensitive protocol state serialized")
		}
		if got := fmt.Sprintf("%#v", value); !strings.HasPrefix(got, "[private WhatsUp") {
			t.Fatal("protocol formatter exposed fields")
		}
	}
	if err := (&Reservation{}).WithLiveApproval(&Approval{}, func() error { t.Fatal("zero proof callback ran"); return nil }); err == nil {
		t.Fatal("zero proof manufactured consent")
	}
}
