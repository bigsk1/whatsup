package transport_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"whatsup/internal/messaging"
	"whatsup/internal/transport"
)

func TestProfilesOverRealAuthenticatedTailcatAreOptionalPrivateAndDurable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	relay := transport.ExportLoopbackRelayProfileForTest(t)
	owner, joiner := newPersistentMessagingPeer(t), newPersistentMessagingPeer(t)
	if _, err := owner.app.SetProfile("Synthetic Alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.app.SetProfile("Synthetic Bob", false); err != nil {
		t.Fatal(err)
	}
	pairPersistentMessagingPeers(t, ctx, relay, owner, joiner)
	records, err := owner.app.CommittedContacts()
	if err != nil || len(records) != 1 {
		t.Fatal("profile fixture did not establish one approved association")
	}
	id := records[0].ID
	network := &messagingNetworkFixture{profile: relay, url: records[0].RelayServiceURL}
	owner.manager, err = messaging.New(owner.local, owner.app, network.factories())
	if err != nil {
		t.Fatal(err)
	}
	joiner.manager, err = messaging.New(joiner.local, joiner.app, network.factories())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.app.SetContactName(id, "Only Alice sees this nickname"); err != nil {
		t.Fatal(err)
	}
	owner.manager.MetadataChanged()
	if _, err := owner.manager.Connect(id); err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.manager.Connect(id); err != nil {
		t.Fatal(err)
	}
	left := waitMessaging(t, ctx, owner.manager, func(s messaging.Snapshot) bool { return s.State == "connected" })
	right := waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool { return s.State == "connected" })
	if left.PeerDisplayName != "" || right.PeerDisplayName != "" || left.LocalName != "Only Alice sees this nickname" || right.LocalName != "" {
		t.Fatal("private own names or local nickname leaked to the other side")
	}
	initialProfiles, initialDials := network.profiles.Load(), network.dials.Load()
	if _, err := owner.app.SetProfile("Synthetic Alice", true); err != nil {
		t.Fatal(err)
	}
	owner.manager.MetadataChanged()
	waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool { return s.PeerDisplayName == "Synthetic Alice" })
	if err := joiner.app.SetContactName(id, "My private Alice nickname"); err != nil {
		t.Fatal(err)
	}
	joiner.manager.MetadataChanged()
	if _, err := owner.app.SetProfile("Synthetic Alice Updated", true); err != nil {
		t.Fatal(err)
	}
	owner.manager.MetadataChanged()
	right = waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool { return s.PeerDisplayName == "Synthetic Alice Updated" })
	if right.LocalName != "My private Alice nickname" || owner.manager.Snapshot().PeerDisplayName != "" || network.profiles.Load() != initialProfiles || network.dials.Load() != initialDials {
		t.Fatal("name update replaced a local label, leaked private profile or reopened transport")
	}
	text := "synthetic text after authenticated metadata writes"
	if _, err := owner.manager.Send(id, text); err != nil {
		t.Fatal("metadata writes invalidated an existing scoped conversation writer")
	}
	waitMessaging(t, ctx, owner.manager, func(s messaging.Snapshot) bool {
		message, ok := messagingMessage(s, text)
		return ok && message.Delivered
	})
	waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool { _, ok := messagingMessage(s, text); return ok })
	for _, p := range []*persistentMessagingPeer{owner, joiner} {
		requireEncryptedMessagingHistory(t, p, "Synthetic Alice", "Synthetic Bob", "Synthetic Alice Updated", "Only Alice sees this nickname", "My private Alice nickname", text)
	}
	joiner.close(t)
	joiner.open(t)
	joiner.manager, err = messaging.New(joiner.local, joiner.app, network.factories())
	if err != nil {
		t.Fatal(err)
	}
	restored := joiner.manager.Snapshot()
	if restored.State != "offline" || restored.PeerDisplayName != "Synthetic Alice Updated" || restored.LocalName != "My private Alice nickname" || len(restored.Messages) != 1 {
		t.Fatal("encrypted peer label, local nickname or history did not restore offline")
	}
	if _, err := owner.app.SetProfile("Synthetic Alice Updated", false); err != nil {
		t.Fatal(err)
	}
	owner.manager.MetadataChanged()
	if _, err := joiner.manager.Connect(id); err != nil {
		t.Fatal(err)
	}
	right = waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool { return s.State == "connected" && s.PeerDisplayName == "" })
	if right.LocalName != "My private Alice nickname" || len(right.Messages) != 1 {
		t.Fatal("withdrawing shared name deleted local nickname or conversation")
	}
	// A fresh explicit share is an update on the authenticated stream too.
	if _, err := joiner.app.SetProfile("Synthetic Bob", true); err != nil {
		t.Fatal(err)
	}
	joiner.manager.MetadataChanged()
	waitMessaging(t, ctx, owner.manager, func(s messaging.Snapshot) bool {
		return s.PeerDisplayName == "Synthetic Bob" && s.LocalName == "Only Alice sees this nickname"
	})
}

func TestAuthenticatedProfilePersistenceFailureStopsTheAffectedRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	relay := transport.ExportLoopbackRelayProfileForTest(t)
	owner, joiner := newPersistentMessagingPeer(t), newPersistentMessagingPeer(t)
	pairPersistentMessagingPeers(t, ctx, relay, owner, joiner)
	records, err := owner.app.CommittedContacts()
	if err != nil || len(records) != 1 {
		t.Fatal("missing approved profile fixture")
	}
	network := &messagingNetworkFixture{profile: relay, url: records[0].RelayServiceURL}
	owner.manager, err = messaging.New(owner.local, owner.app, network.factories())
	if err != nil {
		t.Fatal(err)
	}
	joiner.manager, err = messaging.New(joiner.local, joiner.app, network.factories())
	if err != nil {
		t.Fatal(err)
	}
	id := records[0].ID
	if _, err := owner.manager.Connect(id); err != nil {
		t.Fatal(err)
	}
	if _, err := joiner.manager.Connect(id); err != nil {
		t.Fatal(err)
	}
	waitMessaging(t, ctx, owner.manager, func(s messaging.Snapshot) bool { return s.State == "connected" })
	waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool { return s.State == "connected" })
	// Fault only this disposable test vault's replacement target. The next
	// received profile save must fail and poison its open application state.
	target := filepath.Join(joiner.directory, "state.vault")
	if err := os.Rename(target, filepath.Join(joiner.directory, "previous-ciphertext")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.app.SetProfile("Synthetic Profile Save Failure", true); err != nil {
		t.Fatal(err)
	}
	owner.manager.MetadataChanged()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for joiner.manager.Snapshot().State != "failed" {
		select {
		case <-ctx.Done():
			t.Fatal("profile persistence failure did not terminate the runtime")
		case <-tick.C:
		}
	}
	if got := joiner.manager.Snapshot(); got.CanConnect || got.CanSend || got.PeerDisplayName != "" {
		t.Fatal("failed metadata save retained send, retry or speculative name")
	}
	if _, err := joiner.manager.Send(id, "must not queue after failure"); err == nil {
		t.Fatal("profile persistence failure continued writing chat")
	}
	if _, err := joiner.app.Profile(); err == nil {
		t.Fatal("failed metadata save did not poison shared application persistence")
	}
}
