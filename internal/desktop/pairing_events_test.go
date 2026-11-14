package desktop

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestPairingHintsCoalesceAndCallbacksCanReloadOutsideServiceGuard(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "private"), &testKeys{})
	seen := make(chan PairingHint, 8)
	if err := ConfigureRuntime(s, RuntimeOptions{OnPairingChanged: func(h PairingHint) {
		_ = s.GetPairingStatus()
		seen <- h
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	_, _ = s.ApprovePairing("missing", "unverified")
	select {
	case h := <-seen:
		wire, err := json.Marshal(h)
		if err != nil || h.Revision == 0 || string(wire) != "{\"revision\":1}" {
			t.Fatal("hint contains more than a revision")
		}
	case <-time.After(time.Second):
		t.Fatal("state invalidation was lost or callback deadlocked")
	}
	done := make(chan struct{})
	go func() { _ = s.ServiceShutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown joined a callback under the service guard")
	}
}

func TestPairingEventQueueIsBoundedAndRetainsNewestRevision(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	seen := make(chan PairingHint, 2)
	p := newPairingEvents(func(h PairingHint) {
		if h.Revision == 1 {
			close(entered)
			<-release
		}
		seen <- h
	})
	p.enqueue(PairingHint{})
	<-entered
	for range 1000 {
		p.enqueue(PairingHint{})
	}
	if len(p.wake) != 1 {
		t.Fatal("pairing queue grew instead of coalescing")
	}
	close(release)
	<-seen
	select {
	case h := <-seen:
		if h.Revision != 1001 {
			t.Fatal("latest revision lost")
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced hint missing")
	}
	p.close()
}
