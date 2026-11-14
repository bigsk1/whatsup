package transport_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/messaging"
	"whatsup/internal/transport"
)

func TestSuccessfulPairingAutomaticallyConnectsItsExactRuntimeGeneration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	profile := transport.ExportLoopbackRelayProfileForTest(t)
	owner, joiner := newPersistentMessagingPeer(t), newPersistentMessagingPeer(t)
	var calls atomic.Int32
	factories := messaging.Factories{Profile: func(context.Context, string) (*transport.RelayProfile, error) { calls.Add(1); return profile, nil }}
	var err error
	owner.manager, err = messaging.New(owner.local, owner.app, factories)
	if err != nil {
		t.Fatal("could not prepare owner runtime")
	}
	joiner.manager, err = messaging.New(joiner.local, joiner.app, factories)
	if err != nil {
		t.Fatal("could not prepare joiner runtime")
	}
	oldOwner := owner.manager
	oldJoiner := joiner.manager
	ownerCompleted := func(id string) { _, _ = oldOwner.Connect(id) }
	joinerCompleted := func(id string) { _, _ = oldJoiner.Connect(id) }
	if owner.manager.Snapshot().State != "none" || joiner.manager.Snapshot().State != "none" || calls.Load() != 0 {
		t.Fatal("empty runtime started networking")
	}
	pairPersistentMessagingPeers(t, ctx, profile, owner, joiner, ownerCompleted, joinerCompleted)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for owner.manager.Snapshot().State != "connected" || joiner.manager.Snapshot().State != "connected" {
		select {
		case <-ctx.Done():
			t.Fatal("approved pairing did not automatically connect")
		case <-tick.C:
		}
	}
	left, right := owner.manager.Snapshot(), joiner.manager.Snapshot()
	if !left.CanSend || !right.CanSend || left.ContactID != right.ContactID || calls.Load() != 2 {
		t.Fatal("automatic connection did not use both completed saved contacts")
	}
	if _, err := owner.manager.Send(left.ContactID, "synthetic automatic connection message"); err != nil {
		t.Fatal("automatic session could not queue text")
	}
	for {
		left, right = owner.manager.Snapshot(), joiner.manager.Snapshot()
		if len(left.Messages) == 1 && len(right.Messages) == 1 && left.Messages[0].Delivered && left.Messages[0].ID == right.Messages[0].ID {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("automatic connection did not deliver authenticated text")
		case <-tick.C:
		}
	}
	oldOwner.Close()
	var replacementCalls atomic.Int32
	replacement, err := messaging.New(owner.local, owner.app, messaging.Factories{Profile: func(context.Context, string) (*transport.RelayProfile, error) {
		replacementCalls.Add(1)
		return profile, nil
	}})
	if err != nil {
		t.Fatal("could not create replacement runtime")
	}
	defer replacement.Close()
	// A delayed old completion must address its closed manager, never select
	// whichever replacement happens to be installed in the service now.
	ownerCompleted(left.ContactID)
	if replacementCalls.Load() != 0 || replacement.Snapshot().State != "offline" {
		t.Fatal("old pairing completion activated a replacement runtime")
	}
}
