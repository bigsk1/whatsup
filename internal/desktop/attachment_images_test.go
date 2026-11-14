package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"whatsup/internal/media"
	"whatsup/internal/transfer"
)

func unsupportedAttachmentImages(t *testing.T) map[string][]byte {
	t.Helper()
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	oversized := bytes.Clone(picture.Bytes())
	binary.BigEndian.PutUint32(oversized[16:20], media.MaxSide+1)
	binary.BigEndian.PutUint32(oversized[29:33], crc32.ChecksumIEEE(oversized[12:29]))
	pal := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	var animation bytes.Buffer
	if err := gif.EncodeAll(&animation, &gif.GIF{Image: []*image.Paletted{pal, pal}, Delay: []int{1, 1}}); err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"oversized.png": oversized,
		"animated.gif":  animation.Bytes(),
		"original.avif": append([]byte{0, 0, 0, 24}, []byte("ftypavif\x00\x00\x00\x00avifmif1")...),
		"active.svg":    []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>synthetic</script></svg>`),
	}
}

func TestUnsupportedImagesRequireExplicitSaveOnlyFileChoice(t *testing.T) {
	for name, original := range unsupportedAttachmentImages(t) {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(filename, original, 0600); err != nil {
				t.Fatal(err)
			}
			s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
				o.Pick = func(context.Context) (string, error) { return filename, nil }
			})
			if draft, err := s.ChooseAttachment(contact.ID, false); !errors.Is(err, errImageAsFile) || draft.Handle != "" || !strings.Contains(err.Error(), "Send as file") {
				t.Fatal("default selection silently retained original image bytes", err)
			}
			draft, err := s.ChooseAttachment(contact.ID, true)
			if err != nil || draft.PreviewURL != "" || draft.MediaType != "application/octet-stream" || draft.Name != name || draft.Size != uint64(len(original)) {
				t.Fatal("explicit original was not save-only", err)
			}
			scope, err := s.application.AttachmentStore(contact.ID)
			if err != nil {
				t.Fatal(err)
			}
			records, err := scope.List()
			if err != nil || len(records) != 1 || records[0].Offer.SHA256 != sha256.Sum256(original) {
				t.Fatal("explicit file source hash changed", err)
			}
			if kind, err := scope.VerifiedMediaType(records[0].Offer.ID); err != nil || kind != "application/octet-stream" {
				t.Fatal("save-only file was reclassified as previewable", err)
			}
			input, err := scope.Open(context.Background(), records[0].Offer.ID)
			if err != nil {
				t.Fatal(err)
			}
			var actual bytes.Buffer
			_, err = actual.ReadFrom(input)
			_ = input.Close()
			if err != nil || !bytes.Equal(actual.Bytes(), original) {
				t.Fatal("Send as file changed original bytes", err)
			}
			if _, err := s.PreviewAttachment(contact.ID, records[0].Offer.ID); err == nil {
				t.Fatal("save-only image draft gained full-preview authority")
			}
			if err := scope.QueueOutgoing(records[0].Offer.ID, false); err != nil {
				t.Fatal(err)
			}
			if err := scope.MarkDelivered(records[0].Offer.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PreviewAttachment(contact.ID, records[0].Offer.ID); err == nil {
				t.Fatal("completed save-only image gained preview authority")
			}
			list, err := s.GetAttachments(contact.ID)
			if err != nil || len(list.Items) != 1 || list.Items[0].MediaType != "application/octet-stream" || list.Items[0].PreviewURL != "" {
				t.Fatal("completed explicit file gained a thumbnail", err)
			}
		})
	}
}

func TestJPEGDefaultUsesJPEGExtensionAndLeavesSourceUntouched(t *testing.T) {
	var original bytes.Buffer
	if err := jpeg.Encode(&original, image.NewRGBA(image.Rect(0, 0, 900, 500)), &jpeg.Options{Quality: 93}); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "synthetic-photo.jpeg")
	if err := os.WriteFile(filename, original.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
		o.Pick = func(context.Context) (string, error) { return filename, nil }
	})
	draft, err := s.ChooseAttachment(contact.ID, false)
	if err != nil || draft.MediaType != "image/jpeg" || draft.Name != "synthetic-photo.jpg" || draft.PreviewURL == "" {
		t.Fatal("JPEG prepare did not keep JPEG encoding/name", err)
	}
	unchanged, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(unchanged, original.Bytes()) {
		t.Fatal("preparing a photo modified its source", err)
	}
}

func TestFullImagePreviewRequiresCompletionAndRetiresItsCacheAndURL(t *testing.T) {
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewNRGBA(image.Rect(0, 0, 900, 500))); err != nil {
		t.Fatal(err)
	}
	s, contact := attachmentService(t, nil)
	scope, err := s.application.AttachmentStore(contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := transferID()
	if err != nil {
		t.Fatal(err)
	}
	o := transfer.Offer{ID: id, Name: "synthetic.png", MediaType: "image/png", Size: uint64(original.Len()), SHA256: sha256.Sum256(original.Bytes())}
	if err := scope.Offer(o); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewAttachment(contact.ID, id); err == nil {
		t.Fatal("unapproved offer gained image authority")
	}
	if _, err := scope.Accept(id); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.Append(id, 0, original.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PreviewAttachment(contact.ID, id); err == nil {
		t.Fatal("incomplete transfer gained image authority")
	}
	if err := scope.Finalize(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	url, err := s.PreviewAttachment(contact.ID, id)
	if err != nil || !strings.HasPrefix(url, "/local-media/") || strings.Contains(url, id) {
		t.Fatal("complete image did not get an opaque local URL", err)
	}
	cache := s.attachments.session.images
	if len(cache.entries) != 0 {
		t.Fatal("issuing a full-image URL eagerly decoded pixels")
	}
	for range 2 {
		response := httptest.NewRecorder()
		if !ServeAttachment(s, response, httptest.NewRequest(http.MethodGet, url, nil)) || response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
			t.Fatal("full image handler omitted its security headers")
		}
		config, err := png.DecodeConfig(bytes.NewReader(response.Body.Bytes()))
		if err != nil || config.Width != 900 || config.Height != 500 {
			t.Fatal("full image handler returned only a thumbnail", err)
		}
	}
	if len(cache.entries) != 1 {
		t.Fatal("full-image responses did not share their bounded cache")
	}
	if _, err := s.ClearHistory(contact.ID); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	ServeAttachment(s, response, httptest.NewRequest(http.MethodGet, url, nil))
	if response.Code != http.StatusNotFound || len(cache.entries) != 0 || cache.bytes != 0 {
		t.Fatal("clear retained full-image URL or cached pixels")
	}
}

func TestThumbnailCacheAndURLsRetireOnDiscardForgetAndLock(t *testing.T) {
	for _, action := range []string{"discard", "forget", "lock"} {
		t.Run(action, func(t *testing.T) {
			var original bytes.Buffer
			if err := png.Encode(&original, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(t.TempDir(), "synthetic.png")
			if err := os.WriteFile(filename, original.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
				o.Pick = func(context.Context) (string, error) { return filename, nil }
			})
			draft, err := s.ChooseAttachment(contact.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			cache := s.attachments.session.images
			response := httptest.NewRecorder()
			ServeAttachment(s, response, httptest.NewRequest(http.MethodGet, draft.PreviewURL, nil))
			if response.Code != http.StatusOK || len(cache.entries) != 1 {
				t.Fatal("thumbnail did not populate the session cache")
			}
			switch action {
			case "discard":
				err = s.DiscardAttachment(contact.ID, draft.Handle)
			case "forget":
				_, err = s.ForgetContact(contact.ID)
			case "lock":
				if status := s.LockStorage(); status.StorageState != "locked" {
					t.Fatal("lock failed")
				}
			}
			if err != nil || len(cache.entries) != 0 || cache.bytes != 0 {
				t.Fatal("retired image pixels remained cached", err)
			}
			response = httptest.NewRecorder()
			ServeAttachment(s, response, httptest.NewRequest(http.MethodGet, draft.PreviewURL, nil))
			if response.Code != http.StatusNotFound {
				t.Fatal("retired thumbnail URL remained readable")
			}
		})
	}
}

type heldAttachmentResponse struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release <-chan struct{}
}

func (w *heldAttachmentResponse) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseRecorder.Write(p)
}

func TestInvalidatingThumbnailCacheCannotZeroAnAlreadyServingResponse(t *testing.T) {
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewNRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "synthetic.png")
	if err := os.WriteFile(filename, original.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	s, contact := attachmentService(t, func(o *AttachmentRuntimeOptions) {
		o.Pick = func(context.Context) (string, error) { return filename, nil }
	})
	draft, err := s.ChooseAttachment(contact.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	first := httptest.NewRecorder()
	ServeAttachment(s, first, httptest.NewRequest(http.MethodGet, draft.PreviewURL, nil))
	release := make(chan struct{})
	response := &heldAttachmentResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: release}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ServeAttachment(s, response, httptest.NewRequest(http.MethodGet, draft.PreviewURL, nil))
	}()
	select {
	case <-response.entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("response never reached its independent output buffer")
	}
	_, clearErr := s.ClearHistory(contact.ID)
	close(release)
	<-finished
	if clearErr != nil || !bytes.Equal(response.Body.Bytes(), first.Body.Bytes()) {
		t.Fatal("cache invalidation changed an already-authorized response's bytes", clearErr)
	}
	if _, err := png.Decode(bytes.NewReader(response.Body.Bytes())); err != nil {
		t.Fatal("cache invalidation corrupted a serving PNG", err)
	}
}
