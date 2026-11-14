package desktop

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/messaging"
	"whatsup/internal/transport"
)

func TestNativeUnlockConnectsEverySavedContactAndOfflineJoinsThem(t *testing.T) {
	s := unlockedInvitationService(t)
	left := prepareDesktopForgetContact(t, s)
	right := prepareDesktopForgetContact(t, s)
	if s.LockStorage().StorageState != "locked" {
		t.Fatal("could not close seeded state")
	}
	var started, retired atomic.Int32
	options := RuntimeOptions{AutoConnect: true, Factories: messaging.Factories{
		Profile: func(ctx context.Context, _ string) (*transport.RelayProfile, error) {
			started.Add(1)
			<-ctx.Done()
			retired.Add(1)
			return nil, ctx.Err()
		},
	}}
	if err := ConfigureRuntime(s, options); err != nil {
		t.Fatal(err)
	}
	if status, err := s.UnlockStorage(); err != nil || status.StorageState != "unlocked" {
		t.Fatal("native unlock failed")
	}
	deadline := time.Now().Add(2 * time.Second)
	for started.Load() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("unlock did not start both saved contact runtimes")
		}
		time.Sleep(time.Millisecond)
	}
	for _, id := range []string{left.ID, right.ID} {
		snapshot := s.messages.Snapshot(id)
		if snapshot.State != "connecting" || snapshot.CanConnect || !snapshot.CanSend || len(snapshot.Messages) != 1 {
			t.Fatal("auto-connect lost contact or local history")
		}
	}
	if err := ConfigureRuntime(s, RuntimeOptions{}); err == nil {
		t.Fatal("active runtime construction policy could be replaced")
	}
	if s.LockStorage().StorageState != "locked" || retired.Load() != 2 {
		t.Fatal("Go offline did not join every connection worker")
	}
	if s.GetStatus().MessagingAvailable {
		t.Fatal("offline retained messaging authority")
	}
}

func TestFailedAutoConnectKeepsEachLocalConversationAvailable(t *testing.T) {
	s := unlockedInvitationService(t)
	first, second := prepareDesktopForgetContact(t, s), prepareDesktopForgetContact(t, s)
	s.LockStorage()
	var calls atomic.Int32
	if ConfigureRuntime(s, RuntimeOptions{AutoConnect: true, Factories: messaging.Factories{Profile: func(context.Context, string) (*transport.RelayProfile, error) {
		calls.Add(1)
		return nil, errors.New("synthetic relay unavailable")
	}}}) != nil {
		t.Fatal("could not configure native policy")
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("one failed contact prevented the other attempt")
		}
		time.Sleep(time.Millisecond)
	}
	for _, id := range []string{first.ID, second.ID} {
		if _, err := s.SendMessage(id, "synthetic queued despite unavailable relay"); err != nil {
			t.Fatal("relay failure blocked durable offline queue")
		}
	}
}
