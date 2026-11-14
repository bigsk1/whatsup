package attachmentstore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/png"
	"sync"
	"testing"
	"time"

	"whatsup/internal/media"
	"whatsup/internal/transfer"
)

type occupiedImageDecoder struct {
	*bytes.Reader
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (r *occupiedImageDecoder) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.entered); <-r.release })
	return r.Reader.Read(p)
}

func TestExplicitFileModeSkipsImageDecodeButAuthenticatesSourceAndReceive(t *testing.T) {
	sender, receiver := newFixture(t), newFixture(t)
	var valid bytes.Buffer
	if err := png.Encode(&valid, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	// Occupy the package's one-image decoder permit. File mode must finish
	// while it is occupied, proving neither Stage nor Finalize enters decoding.
	release := make(chan struct{})
	gate := &occupiedImageDecoder{Reader: bytes.NewReader(valid.Bytes()), entered: make(chan struct{}), release: release}
	decoded := make(chan error, 1)
	go func() { _, err := media.Validate(context.Background(), gate); decoded <- err }()
	<-gate.entered
	t.Cleanup(func() {
		close(release)
		if err := <-decoded; err != nil {
			t.Error(err)
		}
	})
	body := append(bytes.Clone(valid.Bytes()), []byte("synthetic original metadata")...)
	o := offer(94, body)
	o.MediaType = "application/octet-stream"
	finished := make(chan error, 1)
	go func() {
		if err := sender.scope.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
			finished <- err
			return
		}
		if err := receiver.scope.Offer(o); err != nil {
			finished <- err
			return
		}
		if _, err := receiver.scope.Accept(o.ID); err != nil {
			finished <- err
			return
		}
		if _, err := receiver.scope.Append(o.ID, 0, body); err != nil {
			finished <- err
			return
		}
		finished <- receiver.scope.Finalize(context.Background(), o.ID)
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("explicit original file waited for an image decoder")
	}
	for _, f := range []*fixture{sender, receiver} {
		mime, err := f.scope.VerifiedMediaType(o.ID)
		if err != nil || mime != "application/octet-stream" || !bytes.Equal(readAll(t, f.scope, o), body) {
			t.Fatal("file mode changed bytes or became previewable", err)
		}
	}
	bad := offer(95, body)
	bad.MediaType = "application/octet-stream"
	bad.SHA256 = sha256.Sum256([]byte("different expected source"))
	if err := sender.scope.Stage(context.Background(), bad, bytes.NewReader(body)); !errors.Is(err, transfer.ErrIntegrity) {
		t.Fatal("file mode bypassed the source hash", err)
	}
	if err := receiver.scope.Offer(bad); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.scope.Accept(bad.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.scope.Append(bad.ID, 0, body); err != nil {
		t.Fatal(err)
	}
	if err := receiver.scope.Finalize(context.Background(), bad.ID); !errors.Is(err, transfer.ErrIntegrity) {
		t.Fatal("file mode bypassed the received whole-file hash", err)
	}
}
