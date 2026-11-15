package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	mu                     sync.Mutex
	notices                []Notice
	startErr, errorOnClose error
	block                  bool
	entered                chan struct{}
	once                   sync.Once
	forgotten              []string
}

func (f *fakeBackend) start(context.Context) error { return f.startErr }
func (f *fakeBackend) send(ctx context.Context, n Notice) error {
	if f.block {
		f.once.Do(func() { close(f.entered) })
		<-ctx.Done()
		return errors.New("synthetic private native error")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notices = append(f.notices, n)
	return nil
}
func (f *fakeBackend) close() error { return f.errorOnClose }
func (f *fakeBackend) forget(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, id)
	return nil
}
func syntheticNotice() Notice {
	return Notice{ID: "AgICAgICAgICAgICAgICAg", ContactID: "AQEBAQEBAQEBAQEBAQEBAQ", Title: "WhatsUp", Body: "New message"}
}

func TestNativeNotifierDeduplicatesAndHasNoDefaultPersonalContent(t *testing.T) {
	fake := &fakeBackend{}
	n := newNotifier(fake)
	if err := n.Send(context.Background(), syntheticNotice()); err == nil {
		t.Fatal("unstarted notifier sent a notice")
	}
	if err := n.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	var senders sync.WaitGroup
	for range 20 {
		senders.Add(1)
		go func() {
			defer senders.Done()
			if err := n.Send(context.Background(), syntheticNotice()); err != nil {
				t.Error(err)
			}
		}()
	}
	senders.Wait()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.notices) != 1 || fake.notices[0].Title != "WhatsUp" || fake.notices[0].Body != "New message" {
		t.Fatal("duplicate notice or unexpected default content")
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	if err := n.Send(context.Background(), syntheticNotice()); err == nil {
		t.Fatal("closed notifier sent a notice")
	}
}

func TestNotifierBoundsCancellationAndRejectsUnsafeInputBeforeNativeCall(t *testing.T) {
	fake := &fakeBackend{block: true, entered: make(chan struct{})}
	n := newNotifier(fake)
	if err := n.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := n.Send(ctx, syntheticNotice()); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "private") {
		t.Fatal("native failure was exposed or unbounded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("notification ignored caller cancellation")
	}
	for _, notice := range []Notice{
		{ID: "invalid", ContactID: syntheticNotice().ContactID, Title: "WhatsUp", Body: "New message"},
		{ID: syntheticNotice().ID, ContactID: strings.Repeat("A", 22), Title: "WhatsUp", Body: "New message"},
		{ID: syntheticNotice().ID, ContactID: syntheticNotice().ContactID, Title: "x\n", Body: "New message"},
		{ID: syntheticNotice().ID, ContactID: syntheticNotice().ContactID, Title: "WhatsUp", Body: "spoof\u202e"},
		{ID: syntheticNotice().ID, ContactID: syntheticNotice().ContactID, Title: "WhatsUp", Body: strings.Repeat("x", 1025)},
	} {
		if n.Send(context.Background(), notice) == nil {
			t.Fatal("unsafe notice reached native worker")
		}
	}
	for _, value := range []any{syntheticNotice(), n, *n} {
		if strings.Contains(fmt.Sprintf("%#v", value), "New message") {
			t.Fatal("notification formatting exposed content")
		}
		if encoded, err := json.Marshal(value); err == nil || len(encoded) != 0 {
			t.Fatal("native capability became serializable")
		}
	}
}

func TestFailedStartNeverReportsReadinessAndShutdownErrorsAreFinite(t *testing.T) {
	n := newNotifier(&fakeBackend{startErr: errors.New("synthetic private start failure")})
	if n.Start(context.Background()) == nil || n.Start(context.Background()) == nil || n.Send(context.Background(), syntheticNotice()) == nil {
		t.Fatal("failed startup became available")
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	other := newNotifier(&fakeBackend{errorOnClose: errors.New("synthetic private shutdown")})
	if other.Start(context.Background()) != nil {
		t.Fatal("fixture startup failed")
	}
	if err := other.Close(); !errors.Is(err, ErrUnavailable) {
		t.Fatal("shutdown error was hidden or leaked")
	}
	for _, zero := range []*Notifier{nil, {}} {
		if zero.Start(context.Background()) == nil || zero.Send(context.Background(), syntheticNotice()) == nil {
			t.Fatal("zero notifier was active")
		}
		if zero.Close() != nil {
			t.Fatal("zero close failed")
		}
	}
}

func TestForgetSerializesPurgeAndRejectsLateNoticeAndActivation(t *testing.T) {
	fake := &fakeBackend{block: true, entered: make(chan struct{})}
	n := newNotifier(fake)
	if n.Start(context.Background()) != nil {
		t.Fatal("fixture startup failed")
	}
	defer n.Close()
	notice := syntheticNotice()
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	go func() { done <- n.Send(ctx, notice) }()
	<-fake.entered
	if err := n.ForgetContact(context.Background(), notice.ContactID); err != nil {
		t.Fatal(err)
	}
	<-done
	if n.Send(context.Background(), notice) == nil {
		t.Fatal("late notice sent after Forget")
	}
	n.dispatchActivation(notice.ContactID, func(string) { t.Error("forgotten contact activated") })
	fake.mu.Lock()
	if len(fake.forgotten) != 1 || fake.forgotten[0] != notice.ContactID {
		t.Error("platform receipt purge missing")
	}
	fake.mu.Unlock()
	other := "AwMDAwMDAwMDAwMDAwMDAw"
	activated := false
	n.dispatchActivation(other, func(id string) { activated = id == other })
	if !activated {
		t.Fatal("Forget affected another contact")
	}
}

type receiptBackend struct {
	receipts      *noticeReceipts
	proofs        map[string]string
	forgetFailure bool
}

func (*receiptBackend) start(context.Context) error { return nil }
func (*receiptBackend) close() error                { return nil }
func (b *receiptBackend) send(_ context.Context, notice Notice) error {
	proof, err := b.receipts.issue(notice)
	if err == nil {
		b.proofs[notice.ContactID+"/"+notice.ID] = proof
	}
	return err
}
func (b *receiptBackend) forget(id string) error {
	if b.forgetFailure {
		return ErrUnavailable
	}
	return b.receipts.forget(id)
}

func TestReapprovedRecoveredContactGetsNewNoticesWithoutRevivingOldProofs(t *testing.T) {
	ledger, _ := newNoticeReceipts(nil, func([]byte) error { return nil }, nil)
	b := &receiptBackend{receipts: ledger, proofs: map[string]string{}}
	n := newNotifier(b)
	if n.Start(context.Background()) != nil {
		t.Fatal("fixture startup failed")
	}
	defer n.Close()
	first := syntheticNotice()
	other := first
	other.ContactID = "AwMDAwMDAwMDAwMDAwMDAw"
	if n.Send(context.Background(), first) != nil || n.Send(context.Background(), other) != nil {
		t.Fatal("initial notices failed")
	}
	oldProof := b.proofs[first.ContactID+"/"+first.ID]
	otherProof := b.proofs[other.ContactID+"/"+other.ID]
	if n.ForgetContact(context.Background(), first.ContactID) != nil || n.Send(context.Background(), first) == nil {
		t.Fatal("Forget did not retire late notices")
	}
	// Restore-like reauthorization: only the trusted current-message Go path
	// supplies this flag; another ordinary Send cannot clear retirement.
	b.forgetFailure = true
	if n.SendApproved(context.Background(), first) == nil {
		t.Fatal("failed stale-proof purge permitted reauthorization")
	}
	n.dispatchActivation(first.ContactID, func(string) { t.Error("failed reauthorization revived contact") })
	b.forgetFailure = false
	if n.SendApproved(context.Background(), first) != nil {
		t.Fatal("restored approved contact remained muted")
	}
	newProof := b.proofs[first.ContactID+"/"+first.ID]
	if newProof == oldProof || ledger.consume(first.ID, first.ContactID, oldProof) || !ledger.consume(first.ID, first.ContactID, newProof) || !ledger.consume(other.ID, other.ContactID, otherProof) {
		t.Fatal("reauthorization revived old proof or affected another contact")
	}
	activated := false
	n.dispatchActivation(first.ContactID, func(string) { activated = true })
	if !activated {
		t.Fatal("new approved contact activation stayed retired")
	}
}
