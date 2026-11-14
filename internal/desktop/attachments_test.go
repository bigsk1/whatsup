package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"whatsup/internal/storage"
	"whatsup/internal/transfer"
)

func attachmentService(t *testing.T, configure func(*AttachmentRuntimeOptions)) (*Service, storage.PendingRecord) {
	t.Helper()
	s := unlockedInvitationService(t)
	record := prepareDesktopForgetContact(t, s)
	if s.LockStorage().StorageState != "locked" {
		t.Fatal("close fixture")
	}
	options := AttachmentRuntimeOptions{Pick: func(context.Context) (string, error) { return "", nil }, Clipboard: func(context.Context) ([]byte, error) { return nil, nil }, Save: func(context.Context, string) (string, error) { return "", nil }, Reveal: func(context.Context, string) error { return nil }, Serve: func(w http.ResponseWriter, r *http.Request, reader io.ReadSeekCloser, length int64) error {
		defer reader.Close()
		_, err := io.Copy(w, reader)
		return err
	}}
	if configure != nil {
		configure(&options)
	}
	if ConfigureAttachments(s, options) != nil {
		t.Fatal("configure native file actions")
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ServiceShutdown() })
	return s, record
}

func TestNativeFilePickerRetiresOldGenerationWithoutHoldingServiceLock(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "synthetic.pdf")
	if os.WriteFile(selected, []byte("%PDF-1.4 synthetic"), 0600) != nil {
		t.Fatal("write fixture")
	}
	entered, released := make(chan struct{}), make(chan struct{})
	s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
		o.Pick = func(context.Context) (string, error) { close(entered); <-released; return selected, nil }
	})
	finished := make(chan error, 1)
	go func() { _, err := s.ChooseAttachment(contact.ID, true); finished <- err }()
	<-entered
	locked := make(chan Status, 1)
	go func() { locked <- s.LockStorage() }()
	select {
	case status := <-locked:
		if status.StorageState != "locked" {
			t.Fatal("failed to lock")
		}
	case <-time.After(2 * time.Second):
		close(released)
		t.Fatal("picker held the service mutex")
	}
	if _, err := s.UnlockStorage(); err != nil {
		close(released)
		t.Fatal(err)
	}
	close(released)
	if err := <-finished; err == nil {
		t.Fatal("old selection entered a new unlocked generation")
	}
	list, err := s.GetAttachments(contact.ID)
	if err != nil || len(list.Drafts) != 0 || len(list.Items) != 0 {
		t.Fatal("old picker restored local file authority")
	}
}

func TestAttachmentDraftsAreEncryptedLocalAndAssetAuthorityDiesOnLock(t *testing.T) {
	var picture bytes.Buffer
	if png.Encode(&picture, image.NewNRGBA(image.Rect(0, 0, 2, 2))) != nil {
		t.Fatal("encode image")
	}
	selected := filepath.Join(t.TempDir(), "synthetic.png")
	if os.WriteFile(selected, picture.Bytes(), 0600) != nil {
		t.Fatal("write image")
	}
	s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
		o.Pick = func(context.Context) (string, error) { return selected, nil }
	})
	draft, err := s.ChooseAttachment(contact.ID, false)
	if err != nil || draft.Handle == "" || draft.PreviewURL == "" {
		t.Fatalf("stage image: %v", err)
	}
	encoded, err := json.Marshal(draft)
	if err != nil || strings.Contains(string(encoded), selected) || strings.Contains(string(encoded), s.directory) {
		t.Fatal("local path leaked")
	}
	list, err := s.GetAttachments(contact.ID)
	if err != nil || len(list.Drafts) != 1 || len(list.Items) != 0 {
		t.Fatal("draft became a network transfer")
	}
	if err = s.SendAttachment(contact.ID, strings.Repeat("A", 32), true); err == nil {
		t.Fatal("guessed handle queued a file")
	}
	response := httptest.NewRecorder()
	if !ServeAttachment(s, response, httptest.NewRequest("GET", draft.PreviewURL, nil)) || response.Code != 200 || response.Header().Get("Content-Type") != "image/png" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("safe local image unavailable")
	}
	if _, err = s.ClearHistory(contact.ID); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	ServeAttachment(s, response, httptest.NewRequest("GET", draft.PreviewURL, nil))
	if response.Code != 404 || s.SendAttachment(contact.ID, draft.Handle, true) == nil {
		t.Fatal("cleared history retained attachment authority")
	}
	list, err = s.GetAttachments(contact.ID)
	if err != nil || len(list.Drafts) != 0 || len(list.Items) != 0 {
		t.Fatal("cleared image restored")
	}
	s.LockStorage()
	response = httptest.NewRecorder()
	ServeAttachment(s, response, httptest.NewRequest("GET", draft.PreviewURL, nil))
	if response.Code != 404 {
		t.Fatal("locked asset still readable")
	}
}

func TestImagePickerDefaultStripsMetadataAndSendAsFilePreservesOriginal(t *testing.T) {
	// The shared fixture is our synthetic four-quadrant WebP with EXIF/XMP
	// sentinels; its offline generation recipe lives in media/media_test.go.
	original, err := os.ReadFile(filepath.Join("..", "media", "testdata", "static-metadata.webp"))
	if err != nil {
		t.Fatal(err)
	}
	markers := [][]byte{[]byte("WhatsUp synthetic EXIF location sentinel"), []byte("WhatsUp synthetic XMP sentinel")}
	for _, marker := range markers {
		if !bytes.Contains(original, marker) {
			t.Fatal("original WebP fixture lacks its metadata sentinel")
		}
	}
	for _, asFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "default-image", true: "send-as-file"}[asFile], func(t *testing.T) {
			selected := filepath.Join(t.TempDir(), "synthetic-metadata.webp")
			if err := os.WriteFile(selected, original, 0600); err != nil {
				t.Fatal(err)
			}
			s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
				o.Pick = func(context.Context) (string, error) { return selected, nil }
			})
			draft, err := s.ChooseAttachment(contact.ID, asFile)
			if err != nil || len(draft.Handle) != 32 || !strings.HasPrefix(draft.PreviewURL, "/local-media/") {
				t.Fatalf("stage native selection: %v", err)
			}
			encoded, err := json.Marshal(draft)
			if err != nil || bytes.Contains(encoded, []byte(selected)) || bytes.Contains(encoded, []byte(s.directory)) {
				t.Fatal("file selection exposed a private path")
			}
			scope, err := s.application.AttachmentStore(contact.ID)
			if err != nil {
				t.Fatal(err)
			}
			records, err := scope.List()
			if err != nil || len(records) != 1 || records[0].State != transfer.Draft || records[0].Direction != transfer.Outgoing {
				t.Fatal("file selection did not remain a single local draft")
			}
			offer := records[0].Offer
			if draft.Handle == offer.ID {
				t.Fatal("renderer handle exposed the underlying transfer identifier")
			}
			reader, err := scope.Open(context.Background(), offer.ID)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil {
				t.Fatalf("read staged draft: %v %v", readErr, closeErr)
			}
			if offer.Size != uint64(len(body)) || draft.Size != offer.Size || offer.SHA256 != sha256.Sum256(body) || draft.MediaType != offer.MediaType {
				t.Fatal("draft metadata or hash did not describe its actual staged bytes")
			}
			if asFile {
				if !bytes.Equal(body, original) || offer.Size != uint64(len(original)) || offer.SHA256 != sha256.Sum256(original) || offer.MediaType != "image/webp" || draft.Name != "synthetic-metadata.webp" {
					t.Fatal("Send as file changed the original image bytes or metadata")
				}
			} else {
				if bytes.Equal(body, original) || offer.MediaType != "image/png" || http.DetectContentType(body) != "image/png" || draft.Name != "synthetic-metadata.png" {
					t.Fatal("default image selection did not reencode to PNG")
				}
				config, err := png.DecodeConfig(bytes.NewReader(body))
				if err != nil || config.Width != 384 || config.Height != 192 {
					t.Fatal("default image selection changed decoded dimensions")
				}
			}
			for _, marker := range markers {
				if bytes.Contains(body, marker) != asFile {
					t.Fatal("staged metadata did not follow the explicit Send as file choice")
				}
			}
			// Even an original-byte draft gets only a sanitized PNG thumbnail
			// through its opaque app handler, never the original metadata.
			preview := httptest.NewRecorder()
			if !ServeAttachment(s, preview, httptest.NewRequest("GET", draft.PreviewURL, nil)) || preview.Code != http.StatusOK || preview.Header().Get("Content-Type") != "image/png" || preview.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("draft preview did not use the safe image handler")
			}
			for _, marker := range markers {
				if bytes.Contains(preview.Body.Bytes(), marker) {
					t.Fatal("draft thumbnail exposed original image metadata")
				}
			}
			list, err := s.GetAttachments(contact.ID)
			if err != nil || len(list.Drafts) != 1 || list.Drafts[0].Handle != draft.Handle || len(list.Items) != 0 {
				t.Fatal("selection or preview implicitly queued an attachment")
			}
			retained, err := scope.Get(offer.ID)
			if err != nil || retained.State != transfer.Draft {
				t.Fatal("preview changed the draft's local-only state")
			}
		})
	}
}

func TestReceivedFileIsSaveOnlyAndExistingDestinationsArePreserved(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "received.pdf")
	revealed := false
	s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
		o.Save = func(context.Context, string) (string, error) { return destination, nil }
		o.Reveal = func(_ context.Context, path string) error { revealed = path == destination; return nil }
	})
	scope, err := s.application.AttachmentStore(contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("%PDF-1.4 synthetic received file")
	id, err := transferID()
	if err != nil {
		t.Fatal(err)
	}
	offer := transfer.Offer{ID: id, Name: "synthetic.pdf", Size: uint64(len(payload)), MediaType: "application/pdf", SHA256: sha256.Sum256(payload)}
	if err = scope.Offer(offer); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveAttachment(contact.ID, id); err == nil {
		t.Fatal("unapproved file was saveable")
	}
	if _, err = scope.Accept(id); err != nil {
		t.Fatal(err)
	}
	if _, err = scope.Append(id, 0, payload); err != nil {
		t.Fatal(err)
	}
	if err = scope.Finalize(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveAttachment(contact.ID, id)
	if err != nil || saved.Handle == "" || revealed {
		t.Fatal("save did not remain explicit and save-only")
	}
	actual, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(actual, payload) {
		t.Fatal("saved bytes changed")
	}
	if _, err = s.SaveAttachment(contact.ID, id); err == nil {
		t.Fatal("existing destination overwritten")
	}
	if s.ShowSavedAttachment(contact.ID, saved.Handle) != nil || !revealed {
		t.Fatal("explicit show in folder unavailable")
	}
	if _, err = s.ForgetContact(contact.ID); err != nil {
		t.Fatal(err)
	}
	if s.ShowSavedAttachment(contact.ID, saved.Handle) == nil {
		t.Fatal("forgotten contact retained reveal authority")
	}
}
