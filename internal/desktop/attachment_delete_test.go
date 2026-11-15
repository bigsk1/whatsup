package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/transfer"
)

func TestDeleteCompletedAttachmentRevokesMediaAndSavedHandleButKeepsExport(t *testing.T) {
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(t.TempDir(), "synthetic.png")
	exported := filepath.Join(t.TempDir(), "saved-copy.png")
	if err := os.WriteFile(selected, picture.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	revealed := false
	s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
		o.Pick = func(context.Context) (string, error) { return selected, nil }
		o.Save = func(context.Context, string) (string, error) { return exported, nil }
		o.Reveal = func(context.Context, string) error { revealed = true; return nil }
	})
	draft, err := s.ChooseAttachment(contact.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SendAttachment(contact.ID, draft.Handle, false); err != nil {
		t.Fatal(err)
	}
	scope, err := s.application.AttachmentStore(contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	records, err := scope.List()
	if err != nil || len(records) != 1 {
		t.Fatal("missing staged attachment", err)
	}
	id := records[0].Offer.ID
	if err := scope.MarkDelivered(id); err != nil {
		t.Fatal(err)
	}
	url, err := s.PreviewAttachment(contact.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", url, nil)
	before := httptest.NewRecorder()
	if !ServeAttachment(s, before, request) || before.Code != 200 {
		t.Fatal("fixture image unavailable")
	}
	saved, err := s.SaveAttachment(contact.ID, id)
	if err != nil || saved.Handle == "" {
		t.Fatal("fixture export unavailable", err)
	}
	for range 2 {
		if err := s.DeleteAttachment(contact.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := s.GetAttachments(contact.ID); err != nil || len(list.Items) != 0 || list.ContactID != contact.ID {
		t.Fatal("delete did not remove the one card", err)
	}
	after := httptest.NewRecorder()
	if !ServeAttachment(s, after, request) || after.Code != 404 {
		t.Fatal("old image URL retained cache or read authority")
	}
	if _, err := s.PreviewAttachment(contact.ID, id); err == nil {
		t.Fatal("deleted preview was recreated")
	}
	if err := s.ShowSavedAttachment(contact.ID, saved.Handle); err == nil || revealed {
		t.Fatal("saved handle survived delete")
	}
	if data, err := os.ReadFile(exported); err != nil || !bytes.Equal(data, picture.Bytes()) {
		t.Fatal("delete altered the user's exported copy", err)
	}
	if s.GetStatus().StorageState != "unlocked" || s.GetConversation().ContactID != contact.ID {
		t.Fatal("single delete retired contact or storage")
	}
}

func TestDeletePendingAttachmentIsScopedOfflineDurableAndReclaimsReservation(t *testing.T) {
	s, contact := attachmentService(t, nil)
	scope, err := s.application.AttachmentStore(contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("synthetic incoming pending deletion")
	id := "CAgICAgICAgICAgICAgICA"
	o := transfer.Offer{ID: id, Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
	if err := scope.Offer(o); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.Accept(id); err != nil {
		t.Fatal(err)
	}
	other := prepareDesktopForgetContact(t, s)
	if err := s.DeleteAttachment(other.ID, id); err == nil {
		t.Fatal("another contact deleted scoped attachment")
	}
	if record, _ := scope.Get(id); record.Hidden || record.State != transfer.Accepted {
		t.Fatal("wrong-contact delete changed target")
	}
	if err := s.DeleteAttachment(contact.ID, "../state.vault"); err == nil {
		t.Fatal("path accepted as attachment ID")
	}
	if err := s.DeleteAttachment(contact.ID, id); err != nil {
		t.Fatal(err)
	}
	if record, err := scope.Get(id); err != nil || !record.Hidden || record.State != transfer.Declined || !record.PendingWithdrawal {
		t.Fatal("offline delete lost cancellation obligation", err)
	}
	s.LockStorage()
	if err := s.DeleteAttachment(contact.ID, id); err == nil {
		t.Fatal("locked deletion accepted")
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	scope, err = s.application.AttachmentStore(contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record, err := scope.Get(id); err != nil || !record.Hidden || !record.PendingWithdrawal {
		t.Fatal("reopen resurrected a deleted item", err)
	}
	if err := s.DeleteAttachment(contact.ID, id); err != nil {
		t.Fatal("offline delete is not idempotent", err)
	}
	if list, err := s.GetAttachments(contact.ID); err != nil || len(list.Items) != 0 {
		t.Fatal("hidden cancellation resurfaced", err)
	}
}

func TestLateSaveDialogCannotRecreateAuthorityAfterAttachmentDelete(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	destination := filepath.Join(t.TempDir(), "must-not-be-saved.bin")
	s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
		o.Save = func(context.Context, string) (string, error) { close(entered); <-release; return destination, nil }
	})
	scope, _ := s.application.AttachmentStore(contact.ID)
	body := []byte("synthetic completed late-save bytes")
	id := "CQkJCQkJCQkJCQkJCQkJCQ"
	o := transfer.Offer{ID: id, Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
	if err := scope.Stage(t.Context(), o, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := scope.QueueOutgoing(id, false); err != nil {
		t.Fatal(err)
	}
	if err := scope.MarkDelivered(id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.SaveAttachment(contact.ID, id); done <- err }()
	<-entered
	if err := s.DeleteAttachment(contact.ID, id); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("late save recreated deleted authority")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("late dialog saved deleted bytes", err)
	}
}

func TestDeleteWaitsForStartedRevealWithoutHoldingServiceGuard(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	destination := filepath.Join(t.TempDir(), "kept-user-export.bin")
	var calls atomic.Int32
	s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
		o.Save = func(context.Context, string) (string, error) { return destination, nil }
		o.Reveal = func(ctx context.Context, filename string) error {
			calls.Add(1)
			if _, ok := ctx.Deadline(); !ok || filename != destination {
				t.Error("reveal lost its bounded saved-file authority")
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	scope, _ := s.application.AttachmentStore(contact.ID)
	body := []byte("synthetic user-owned saved copy")
	id := "CgoKCgoKCgoKCgoKCgoKCg"
	o := transfer.Offer{ID: id, Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
	if err := scope.Stage(t.Context(), o, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := scope.QueueOutgoing(id, false); err != nil {
		t.Fatal(err)
	}
	if err := scope.MarkDelivered(id); err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveAttachment(contact.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	permit := s.attachments.session.saved[saved.Handle].reveal
	s.mu.Unlock()
	revealed := make(chan error, 1)
	go func() { revealed <- s.ShowSavedAttachment(contact.ID, saved.Handle) }()
	<-entered
	deleted := make(chan error, 1)
	go func() { deleted <- s.DeleteAttachment(contact.ID, id) }()
	// Revocation is published before joining the per-handle reveal; unrelated
	// service reads must remain available while that OS callback is held.
	detached := make(chan struct{})
	go func() {
		for {
			s.mu.Lock()
			revoked := permit.retired.Load()
			s.mu.Unlock()
			if revoked {
				close(detached)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case <-detached:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("delete waited for OS reveal under Service.mu")
	}
	if err := s.ShowSavedAttachment(contact.ID, saved.Handle); err == nil {
		close(release)
		t.Fatal("detached reveal handle was still accepted")
	}
	secondDelete := make(chan error, 1)
	go func() { secondDelete <- s.DeleteAttachment(contact.ID, id) }()
	select {
	case err := <-deleted:
		close(release)
		t.Fatal("delete returned before started reveal finished", err)
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case err := <-secondDelete:
		close(release)
		t.Fatal("idempotent concurrent delete bypassed reveal barrier", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-revealed; err != nil {
		t.Fatal(err)
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDelete; err != nil {
		t.Fatal(err)
	}
	if err := permit.run(t.Context(), func(context.Context) error { t.Fatal("previously copied handle started a late reveal"); return nil }); err == nil {
		t.Fatal("copied reveal permit survived delete")
	}
	if err := s.ShowSavedAttachment(contact.ID, saved.Handle); err == nil || calls.Load() != 1 {
		t.Fatal("post-delete reveal was accepted")
	}
	if data, err := os.ReadFile(destination); err != nil || !bytes.Equal(data, body) {
		t.Fatal("delete changed saved file bytes", err)
	}
}
