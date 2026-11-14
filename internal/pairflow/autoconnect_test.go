package pairflow

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/pairing"
)

func setCompletionCallback(flow *Coordinator, callback func(string)) {
	flow.mu.Lock()
	flow.onCompleted = callback
	flow.mu.Unlock()
}

func TestCompletedPairingNotifiesOnceAfterInvitationRetirement(t *testing.T) {
	owner, ownerApp, _, _ := encryptedPeer(t)
	joiner, joinerApp, _, _ := encryptedPeer(t)
	p := connectTestPair(t, owner, joiner, ownerApp, joinerApp, 0, false, true)
	var ownerCalls, joinerCalls atomic.Int32
	ownerID, joinerID := make(chan string, 1), make(chan string, 1)
	setCompletionCallback(p.owner, func(id string) {
		ownerCalls.Add(1)
		if p.retired.Load() != 1 || p.manager.Snapshot().State != pairing.Consumed {
			t.Error("completion was notified before invitation retirement")
		}
		records, err := ownerApp.CommittedContacts()
		if err != nil || len(records) != 1 || records[0].ID != id {
			t.Error("completion did not identify the exact durable contact")
		}
		// A callback may read coordinator state. Use a bounded probe so a
		// regression that holds Coordinator.mu fails without hanging cleanup.
		state := make(chan string, 1)
		go func() { state <- p.owner.Snapshot().State }()
		select {
		case got := <-state:
			if got != "completed" {
				t.Error("completion callback observed unfinished state")
			}
		case <-time.After(time.Second):
			t.Error("completion callback ran while the coordinator guard was held")
		}
		select {
		case ownerID <- id:
		default:
		}
	})
	setCompletionCallback(p.joiner, func(id string) {
		joinerCalls.Add(1)
		select {
		case joinerID <- id:
		default:
		}
	})
	approveBoth(t, p)
	if waitRun(t, p.ownerResult) != nil || waitRun(t, p.joinerResult) != nil {
		t.Fatal("mutual durable activation failed")
	}
	if ownerCalls.Load() != 1 || joinerCalls.Load() != 1 {
		t.Fatal("a successful pairing did not notify each side exactly once")
	}
	left, right := <-ownerID, <-joinerID
	if left == "" || left != right {
		t.Fatal("completion callbacks selected different contacts")
	}
	for range 3 {
		_ = p.owner.Snapshot()
		_ = p.joiner.Snapshot()
	}
	if ownerCalls.Load() != 1 || joinerCalls.Load() != 1 {
		t.Fatal("status polling repeated a completion notification")
	}
}

func TestFailedInvitationRetirementDoesNotNotifyCompletion(t *testing.T) {
	owner, ownerApp, _, _ := encryptedPeer(t)
	joiner, joinerApp, _, _ := encryptedPeer(t)
	p := connectTestPair(t, owner, joiner, ownerApp, joinerApp, 0, false, true)
	var calls atomic.Int32
	setCompletionCallback(p.owner, func(string) { calls.Add(1) })
	p.retireFailure.Store(true)
	approveBoth(t, p)
	if waitRun(t, p.ownerResult) == nil {
		t.Fatal("failed invitation retirement completed activation")
	}
	_ = waitRun(t, p.joinerResult)
	if calls.Load() != 0 || p.owner.Snapshot().State != "reconnect-required" {
		t.Fatal("failed retirement notified an active contact")
	}
}

func TestCloseJoinsCompletionCallback(t *testing.T) {
	owner, ownerApp, _, _ := encryptedPeer(t)
	joiner, joinerApp, _, _ := encryptedPeer(t)
	p := connectTestPair(t, owner, joiner, ownerApp, joinerApp, 0, false, true)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	setCompletionCallback(p.owner, func(string) {
		close(entered)
		<-release
	})
	approveBoth(t, p)
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("successful completion did not enter its callback")
	}
	closed := make(chan error, 1)
	go func() { closed <- p.owner.Close() }()
	// Observing unavailable proves Close entered and published closure; it
	// must still wait for the callback owned by the in-progress pairing run.
	waitState(t, p.owner, "unavailable")
	select {
	case <-closed:
		t.Fatal("Close returned before its completion callback finished")
	default:
	}
	unblock()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal("Close failed after the completion callback finished")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Close did not join the released completion callback")
	}
	if waitRun(t, p.ownerResult) != nil || waitRun(t, p.joinerResult) != nil {
		t.Fatal("joined activation did not finish")
	}
}

func TestPendingAndRestoredRecordsDoNotNotifyCompletion(t *testing.T) {
	for _, complete := range []bool{false, true} {
		name, state := "pending", "pending"
		if complete {
			name, state = "committed", "reconnect-required"
		}
		t.Run(name, func(t *testing.T) {
			owner, ownerApp, _, _ := encryptedPeer(t)
			joiner, joinerApp, _, _ := encryptedPeer(t)
			p := connectTestPair(t, owner, joiner, ownerApp, joinerApp, 0, false, complete)
			var calls atomic.Int32
			if !complete {
				setCompletionCallback(p.owner, func(string) { calls.Add(1) })
			}
			approveBoth(t, p)
			if waitRun(t, p.ownerResult) != nil || waitRun(t, p.joinerResult) != nil {
				t.Fatal("durable pairing fixture failed")
			}
			if calls.Load() != 0 {
				t.Fatal("inert pending exchange notified an active contact")
			}
			if err := p.owner.Close(); err != nil {
				t.Fatal(err)
			}
			restored, err := New(owner, ownerApp, Options{CompleteActivation: true, OnCompleted: func(string) { calls.Add(1) }})
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			for range 3 {
				if restored.Snapshot().State != state {
					t.Fatal("restored record did not remain inert")
				}
			}
			if calls.Load() != 0 {
				t.Fatal("restoring or polling durable state notified a new pairing")
			}
		})
	}
}
