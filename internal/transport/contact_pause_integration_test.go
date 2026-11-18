package transport_test

import (
	"context"
	"testing"
	"time"

	"whatsup/internal/messaging"
	"whatsup/internal/transport"
)

func TestRealTailcatPausesOnlyOneContactAndResumesSavedQueue(t *testing.T) {
	profile := transport.ExportLoopbackRelayProfileForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	a, b, c := newMultiMessagingPeer(t, profile), newMultiMessagingPeer(t, profile), newMultiMessagingPeer(t, profile)
	// A exercises both saved transport roles: listening with B, dialing C.
	ab := pairMultiMessagingPeers(t, ctx, profile, a, b)
	ac := pairMultiMessagingPeers(t, ctx, profile, c, a)
	isConnected := func(s messaging.Snapshot) bool { return s.State == "connected" }
	for _, item := range []struct {
		p  *multiMessagingPeer
		id string
	}{{a, ab}, {b, ab}, {a, ac}, {c, ac}} {
		waitMultiMessaging(t, ctx, item.p, item.id, isConnected)
	}

	// Stop the other end temporarily, so A has a real saved outgoing message.
	if _, err := b.registry.SetPaused(ab, true); err != nil {
		t.Fatal("could not pause the dialing endpoint", err)
	}
	waitMultiMessaging(t, ctx, a, ab, func(s messaging.Snapshot) bool { return s.State == "connecting" })
	const queued = "synthetic message held during contact pause"
	if _, err := a.registry.Send(ab, queued); err != nil {
		t.Fatal("could not save a message while the other device was unavailable", err)
	}
	if got, err := a.registry.SetPaused(ab, true); err != nil || !got.Paused || got.State != "paused" {
		t.Fatal("could not pause the listening endpoint", err)
	}
	if _, err := b.registry.SetPaused(ab, false); err != nil {
		t.Fatal("could not resume the dialing endpoint", err)
	}
	if got := b.registry.Snapshot(ab); got.State != "connecting" {
		t.Fatal("remote endpoint did not see an ordinary connection wait", got.State)
	}
	if _, err := a.registry.Send(ab, "synthetic send blocked by pause"); err == nil {
		t.Fatal("paused endpoint accepted a new send")
	}
	if _, err := a.registry.Send(ac, "synthetic message to the other contact"); err != nil {
		t.Fatal("pausing one contact stopped another send", err)
	}
	waitMultiMessaging(t, ctx, c, ac, func(s messaging.Snapshot) bool {
		_, found := messagingMessage(s, "synthetic message to the other contact")
		return found
	})
	if a.registry.Snapshot(ac).State != "connected" || c.registry.Snapshot(ac).State != "connected" {
		t.Fatal("the other contact disconnected during the pause")
	}

	// Reopen A's encrypted state and build a fresh runtime, just like app startup.
	if err := a.flow.Close(); err != nil {
		t.Fatal(err)
	}
	a.registry.Close()
	a.close(t)
	a.open(t)
	reopened, err := messaging.NewRegistry(a.local, a.app, messaging.RegistryOptions{Factories: messaging.Factories{Profile: func(context.Context, string) (*transport.RelayProfile, error) { return profile, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	a.registry = reopened
	contacts, err := reopened.Contacts()
	if err != nil || len(contacts) != 2 {
		t.Fatal("reopen lost saved contacts", err)
	}
	for _, contact := range contacts {
		if contact.CanConnect {
			if _, err := reopened.Connect(contact.ContactID); err != nil {
				t.Fatal("startup could not reconnect an eligible contact", err)
			}
		}
	}
	waitMultiMessaging(t, ctx, a, ac, isConnected)
	if state := reopened.Snapshot(ab); state.State != "paused" || state.CanConnect || state.CanSend {
		t.Fatal("startup made the paused contact eligible to reconnect", state.State)
	}
	page, err := reopened.Page(ab, "", 10)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != queued || page.Messages[0].Delivered {
		t.Fatal("pause lost its saved local history or queue", err)
	}
	if _, found := messagingMessage(b.registry.Snapshot(ab), queued); found {
		t.Fatal("queued message was delivered before resume")
	}
	if got, err := reopened.SetPaused(ab, false); err != nil || got.Paused {
		t.Fatal("could not resume the listening endpoint", err)
	}
	waitMultiMessaging(t, ctx, b, ab, func(s messaging.Snapshot) bool {
		_, found := messagingMessage(s, queued)
		return found
	})
	waitMultiMessaging(t, ctx, a, ab, func(s messaging.Snapshot) bool {
		message, found := messagingMessage(s, queued)
		return found && message.Delivered
	})
}
