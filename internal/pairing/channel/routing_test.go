package channel

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRoutedProvisionAgreesOnExactLocallySelectedService(t *testing.T) {
	for _, matching := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching", false: "substituted-service"}[matching], func(t *testing.T) {
			server, client := pair(t, time.Now().Add(time.Minute))
			sr, cr := reservePair(t, server, client)
			sp, cp := consentPair(t, server, client, sr, cr)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ownerService := "https://relay.example/map.json"
			joinerService := ownerService
			if !matching {
				joinerService = "https://other.example/map.json"
			}
			var ownerSaved, joinerSaved atomic.Bool
			done := make(chan error, 1)
			go func() {
				_, err := sp.ExchangePending(ctx, sr, ContactOffer{Node: [32]byte{1}, ID: [16]byte{3}, PresharedKey: [32]byte{4}, RelayServiceURL: ownerService, ServerEndpoint: "tc-synthetic-stable-endpoint"}, func(a ContactAgreement) error {
					ownerSaved.Store(true)
					if a.RelayServiceURL != ownerService || a.ServerEndpoint != "tc-synthetic-stable-endpoint" {
						return ErrProtocol
					}
					return nil
				})
				done <- err
			}()
			agreement, err := cp.ExchangePending(ctx, cr, ContactOffer{Node: [32]byte{2}, RelayServiceURL: joinerService}, func(a ContactAgreement) error { joinerSaved.Store(true); return nil })
			other := <-done
			if !matching {
				if err == nil || other == nil || ownerSaved.Load() || joinerSaved.Load() {
					t.Fatal("peer selected a different service before persistence")
				}
				return
			}
			if err != nil || other != nil || !ownerSaved.Load() || !joinerSaved.Load() || agreement.RelayServiceURL != ownerService || agreement.ServerEndpoint != "tc-synthetic-stable-endpoint" {
				t.Fatal("routed pending agreement failed")
			}
			server.state.mu.Lock()
			left := server.state.activation
			server.state.mu.Unlock()
			client.state.mu.Lock()
			right := client.state.activation
			client.state.mu.Unlock()
			if left.RoutingCommitment == [32]byte{} || !left.Equal(right) {
				t.Fatal("routing facts were omitted from activation association")
			}
			changed := left
			changed.RoutingCommitment[0] ^= 1
			var persisted bool
			if proof, err := sp.CompleteActivation(ctx, sr, changed, func() error { persisted = true; return nil }); proof != nil || !errors.Is(err, ErrCompletion) || persisted {
				t.Fatal("substituted routing reached committed persistence")
			}
		})
	}
}

func TestRoutedProvisionRejectsOversizedServiceBeforeAllocationOrPersistence(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, cr := reservePair(t, server, client)
	sp, _ := consentPair(t, server, client, sr, cr)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sp.ExchangePending(ctx, sr, ContactOffer{Node: [32]byte{1}, ID: [16]byte{3}, PresharedKey: [32]byte{4}, RelayServiceURL: "https://relay.example/map.json", ServerEndpoint: "tc-synthetic"}, func(ContactAgreement) error { t.Error("oversized service reached persistence"); return nil })
		done <- err
	}()
	packet := makePacket("WUPRE002", 1, Joiner, client.state.binding, 34)
	packet[frameSize] = 2
	binary.BigEndian.PutUint16(packet[frameSize+32:], 65535)
	if err := writePacket(client.state.tls, packet); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("oversized routing frame accepted")
	}
}

func TestRoutingCommitmentHasUnambiguousVersionedLayout(t *testing.T) {
	base, err := RoutingCommitment("https://relay.example/map", "tc-synthetic")
	if err != nil || base == [32]byte{} {
		t.Fatal("valid routing commitment rejected")
	}
	for _, values := range [][2]string{{"https://other.example/map", "tc-synthetic"}, {"https://relay.example/map", "tc-different"}} {
		got, err := RoutingCommitment(values[0], values[1])
		if err != nil || got == base {
			t.Fatal("routing substitution did not change association")
		}
	}
	for _, values := range [][2]string{{"", "tc-synthetic"}, {"http://relay.example/map", "tc-synthetic"}, {"https://relay.example/map", ""}, {"https://user:secret@relay.example/map", "tc-synthetic"}, {"https://relay.example/map?other=1", "tc-synthetic"}, {"https://relay.example/map", strings.Repeat("x", 513)}} {
		if _, err := RoutingCommitment(values[0], values[1]); err == nil {
			t.Fatal("malformed routing facts accepted")
		}
	}
	legacy, err := RoutingCommitment("", "")
	if err != nil || legacy != [32]byte{} {
		t.Fatal("legacy agreement acquired routing authority")
	}
	// Routing lengths are authenticated bounded fields, never delimited by an
	// attacker-selected separator or decoded by an unbounded JSON allocation.
	if _, _, err := readProvision(strings.NewReader(""), 1, Joiner, [32]byte{}, 32, "https://relay.example/map", false); !errors.Is(err, ErrProtocol) && !errors.Is(err, io.EOF) {
		t.Fatal("truncated routing frame accepted")
	}
}
