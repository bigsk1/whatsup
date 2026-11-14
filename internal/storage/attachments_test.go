package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"whatsup/internal/transfer"
)

func storageAttachmentOffer(n byte, body []byte) transfer.Offer {
	return transfer.Offer{ID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n}, 16)), Name: "synthetic-test-file.pdf", MediaType: "application/pdf", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
}
func TestAttachmentHistoryClearWithdrawsPendingAndRevokesPreview(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record, _, _, _, chat, _ := prepareForgetStorage(t, app, 181)
	other, _, _, _, _, _ := prepareForgetStorage(t, app, 182)
	scope, err := app.AttachmentStore(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherScope, err := app.AttachmentStore(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("synthetic-private-object"), 10000)
	o := storageAttachmentOffer(1, body)
	otherOffer := storageAttachmentOffer(2, body)
	if err := scope.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := scope.QueueOutgoing(o.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := otherScope.Stage(context.Background(), otherOffer, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	preview, err := scope.Open(context.Background(), o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := chat.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	if _, err := preview.Read(make([]byte, 1)); err == nil {
		t.Fatal("clear left old plaintext reader usable")
	}
	preview.Close()
	if _, err := scope.Open(context.Background(), o.ID); err == nil {
		t.Fatal("hidden queued source exposed through public Open")
	}
	if reader, err := scope.OpenOutgoing(context.Background(), o.ID); err == nil {
		reader.Close()
		t.Fatal("clear retained queued attachment transport authority")
	}
	if err := scope.MarkDelivered(o.ID); err == nil {
		t.Fatal("late completion revived a withdrawn attachment")
	}
	if rows, _, _, err := scope.History(0, 32); err != nil || len(rows) != 0 {
		t.Fatal("hidden delivered card returned")
	}
	if _, err := otherScope.Open(context.Background(), otherOffer.ID); err != nil {
		t.Fatal("clear affected another contact")
	}
	if err := app.ForgetCommittedContact(record, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.List(); err == nil {
		t.Fatal("forgotten file capability survived")
	}
	if _, err := app.AttachmentStore(record.ID); err == nil {
		t.Fatal("removed contact reacquired file store")
	}
	if rows, _, _, err := otherScope.History(0, 32); err != nil || len(rows) != 1 {
		t.Fatal("forget affected another contact")
	}
}

type blockedAttachmentSource struct {
	entered, closed chan struct{}
	once            sync.Once
}

func (b *blockedAttachmentSource) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.closed
	return 0, io.EOF
}
func (b *blockedAttachmentSource) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}
func TestForgetJoinsStagingBeforeDeletingObjectMetadata(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record, _, _, _, _, _ := prepareForgetStorage(t, app, 183)
	scope, err := app.AttachmentStore(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := &blockedAttachmentSource{entered: make(chan struct{}), closed: make(chan struct{})}
	done := make(chan error, 1)
	o := storageAttachmentOffer(3, []byte("expected bytes"))
	go func() { done <- scope.Stage(context.Background(), o, source) }()
	select {
	case <-source.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("stage never began")
	}
	forgot := make(chan error, 1)
	go func() {
		forgot <- app.ForgetCommittedContact(record, func() error {
			if _, err := scope.Get(o.ID); err == nil {
				t.Error("file capability remained live in stop callback")
			}
			return nil
		})
	}()
	select {
	case err := <-forgot:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forget deadlocked while joining stage")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked source staged after forget")
		}
	case <-time.After(time.Second):
		t.Fatal("stage was not joined")
	}
	entries, err := os.ReadDir(filepath.Join(app.vault.directory, "attachments"))
	if err != nil || len(entries) != 0 {
		t.Fatal("forgotten partial object remained")
	}
}
func TestOrphanAttachmentObjectsNeverMintReplacementCredential(t *testing.T) {
	directory := privateDirectory(t)
	objects := filepath.Join(directory, "attachments")
	if os.MkdirAll(objects, 0700) != nil {
		t.Fatal("mkdir")
	}
	if os.WriteFile(filepath.Join(objects, "synthetic-ciphertext.bin"), bytes.Repeat([]byte{149}, 300), 0600) != nil {
		t.Fatal("object")
	}
	keys := &memoryKeys{}
	if vault, err := Open(directory, keys); err == nil {
		vault.Close()
		t.Fatal("orphan objects minted a new root")
	}
	if keys.writes != 0 {
		t.Fatal("orphan objects replaced missing credential")
	}
}
