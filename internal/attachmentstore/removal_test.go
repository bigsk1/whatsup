package attachmentstore_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/transfer"
)

func TestRemoveSingleAttachmentReclaimsObjectAndKeepsTerminalIntegrity(t *testing.T) {
	for _, scenario := range []string{"incoming-complete", "outgoing-complete", "incoming-active", "outgoing-queued", "incoming-paused", "incoming-offered", "draft", "already-withdrawn"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			body := []byte("synthetic per-item removal bytes")
			o := offer(81, body)
			outgoing := scenario == "outgoing-complete" || scenario == "outgoing-queued" || scenario == "draft" || scenario == "already-withdrawn"
			if outgoing {
				if err := f.scope.Stage(t.Context(), o, bytes.NewReader(body)); err != nil {
					t.Fatal(err)
				}
				if scenario != "draft" {
					if err := f.scope.QueueOutgoing(o.ID, false); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "outgoing-complete" {
					if err := f.scope.MarkDelivered(o.ID); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "already-withdrawn" {
					if err := f.scope.Withdraw(o.ID); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if err := f.scope.Offer(o); err != nil {
					t.Fatal(err)
				}
				if scenario != "incoming-offered" {
					if _, err := f.scope.Accept(o.ID); err != nil {
						t.Fatal(err)
					}
					appendBody(t, f.scope, o, body, 0)
				}
				if scenario == "incoming-complete" {
					if err := f.scope.Finalize(t.Context(), o.ID); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "incoming-paused" {
					if err := f.scope.Cancel(o.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, err := f.raw.Attachments().Get(o.ID)
			if err != nil {
				t.Fatal(err)
			}
			var reader io.ReadSeekCloser
			if scenario == "incoming-complete" || scenario == "outgoing-complete" || scenario == "draft" {
				reader, err = f.scope.Open(context.Background(), o.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
			}
			for range 2 {
				if err := f.scope.Remove(o.ID); err != nil {
					t.Fatal(err)
				}
			}
			after, err := f.raw.Attachments().Get(o.ID)
			if err != nil || after.Validate() != nil || !after.Hidden || after.ObjectID != "" || len(after.Key) != 0 || len(after.Units) != 0 || after.CipherBytes != 0 || after.Charge() != 4096 {
				t.Fatal("delete did not reclaim object while retaining a valid bounded tombstone", err)
			}
			if before.Record.State == transfer.Complete {
				if after.Record.State != transfer.Complete || after.Record.PendingWithdrawal || after.Record.NextChunk != o.Chunks() {
					t.Fatal("completed deletion lost receipt authority")
				}
			} else if after.Record.State != transfer.Declined || after.Record.PendingWithdrawal != (scenario != "draft") {
				t.Fatal("unfinished deletion lost durable cancellation")
			}
			if before.ObjectID != "" {
				if _, err := os.Stat(filepath.Join(f.dir, attachmentstore.Filename, before.ObjectID+".bin")); !os.IsNotExist(err) {
					t.Fatal("deleted object still exists", err)
				}
			}
			if reader != nil {
				if _, err := reader.Read(make([]byte, 1)); err == nil {
					t.Fatal("deleted object retained an old reader")
				}
				reader.Close()
			}
			if _, err := f.scope.Open(t.Context(), o.ID); err == nil {
				t.Fatal("deleted object reopened")
			}
			if records, _, _, err := f.scope.History(0, 32); err != nil || len(records) != 0 {
				t.Fatal("deleted card remains visible", err)
			}
			if !outgoing {
				if err := f.scope.Offer(o); err != nil {
					t.Fatal("exact replay should retain the terminal record", err)
				}
				changed := o
				changed.SHA256[0] ^= 1
				if err := f.scope.Offer(changed); !errors.Is(err, transfer.ErrConflict) {
					t.Fatal("ID/content replay conflict lost", err)
				}
			}
			if after.Record.PendingWithdrawal {
				if err := f.scope.AcknowledgeWithdrawal(o.ID, false); err != nil {
					t.Fatal(err)
				}
				if err := f.scope.Remove(o.ID); err != nil {
					t.Fatal(err)
				}
				if current, _ := f.scope.Get(o.ID); current.PendingWithdrawal {
					t.Fatal("idempotent delete recreated an acknowledged withdrawal")
				}
			}
			f.close(t)
			f.open(t)
			if current, err := f.scope.Get(o.ID); err != nil || !current.Hidden || current.State != after.Record.State {
				t.Fatal("delete did not survive reopen", err)
			}
		})
	}
}

func TestRemoveFreesReservationWithoutRemovingAnotherAttachment(t *testing.T) {
	f := newFixture(t)
	first := offer(82, []byte("synthetic quota reservation"))
	first.Size = transfer.MaxFileBytes
	second := first
	second.ID = id(83)
	for _, o := range []transfer.Offer{first, second} {
		if err := f.scope.Offer(o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.scope.Accept(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.scope.Accept(second.ID); !errors.Is(err, transfer.ErrCapacity) {
		t.Fatal("fixture did not consume quota", err)
	}
	if err := f.scope.Remove(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.scope.Accept(second.ID); err != nil {
		t.Fatal("delete retained full-file reservation", err)
	}
	if record, _ := f.scope.Get(second.ID); record.Hidden || record.State != transfer.Accepted {
		t.Fatal("single delete altered another item")
	}
}

func TestRemoveRefusedMetadataCommitPreservesOriginalBytes(t *testing.T) {
	f := newFixture(t)
	body := []byte("synthetic preserved removal failure")
	o := offer(84, body)
	if err := f.scope.Stage(t.Context(), o, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	f.fail = true
	if err := f.scope.Remove(o.ID); err == nil {
		t.Fatal("refused deletion reported success")
	}
	f.fail = false
	f.close(t)
	f.open(t)
	if record, err := f.scope.Get(o.ID); err != nil || record.Hidden {
		t.Fatal("refused delete hid the original", err)
	}
	if !bytes.Equal(readAll(t, f.scope, o), body) {
		t.Fatal("refused delete lost encrypted bytes")
	}
}
