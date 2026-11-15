package attachmentstore_test

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestLocalAttachmentEventTimeStaysStableThroughProgressAndRestart(t *testing.T) {
	f := newFixture(t)
	body := []byte("synthetic local timestamp contents")
	out := offer(81, body)
	if err := f.scope.Stage(context.Background(), out, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	draft, err := f.scope.Get(out.ID)
	if err != nil || !draft.RecordedAt.IsZero() {
		t.Fatal("unsent draft invented a send timestamp")
	}
	before := time.Now().UTC()
	if err := f.scope.QueueOutgoing(out.ID, false); err != nil {
		t.Fatal(err)
	}
	queued, err := f.scope.Get(out.ID)
	if err != nil || queued.RecordedAt.IsZero() || queued.RecordedAt.Before(before) || queued.RecordedAt.After(time.Now().UTC()) {
		t.Fatal("first queue did not record local Go time")
	}
	if err := f.scope.Cancel(out.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.scope.Resume(out.ID); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.scope.Get(out.ID)
	if err != nil || !resumed.RecordedAt.Equal(queued.RecordedAt) {
		t.Fatal("pause/retry changed original local send time")
	}
	in := offer(82, body)
	before = time.Now().UTC()
	if err := f.scope.Offer(in); err != nil {
		t.Fatal(err)
	}
	first, err := f.scope.Get(in.ID)
	if err != nil || first.RecordedAt.IsZero() || first.RecordedAt.Before(before) || first.RecordedAt.After(time.Now().UTC()) {
		t.Fatal("first offer did not record local receipt time")
	}
	if err := f.scope.Offer(in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.scope.Accept(in.ID); err != nil {
		t.Fatal(err)
	}
	appendBody(t, f.scope, in, body, 0)
	if err := f.scope.Finalize(context.Background(), in.ID); err != nil {
		t.Fatal(err)
	}
	complete, err := f.scope.Get(in.ID)
	if err != nil || !complete.RecordedAt.Equal(first.RecordedAt) {
		t.Fatal("duplicate offer/progress/finalization changed original receipt time")
	}
	f.close(t)
	f.open(t)
	for id, want := range map[string]time.Time{out.ID: queued.RecordedAt, in.ID: first.RecordedAt} {
		got, err := f.scope.Get(id)
		if err != nil || !got.RecordedAt.Equal(want) {
			t.Fatal("timestamp did not survive encrypted reopen")
		}
	}
}
