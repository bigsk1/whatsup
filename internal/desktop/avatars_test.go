package desktop

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"whatsup/internal/media"
)

func avatarSource(t *testing.T) ([]byte, []byte) {
	t.Helper()
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewNRGBA(image.Rect(0, 0, 800, 600))); err != nil {
		t.Fatal(err)
	}
	data, err := media.PrepareAvatar(context.Background(), bytes.NewReader(input.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return input.Bytes(), data
}

func avatarResponse(s *Service, url string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	ServeAttachment(s, response, httptest.NewRequest("GET", url, nil))
	return response
}

func TestAvatarBridgeOwnPhotoPrivateAndOpaqueMediaCapabilitiesRetire(t *testing.T) {
	input, data := avatarSource(t)
	selected := filepath.Join(t.TempDir(), "synthetic-avatar.png")
	if err := os.WriteFile(selected, input, 0600); err != nil {
		t.Fatal(err)
	}
	s, record := attachmentService(t, func(o *AttachmentRuntimeOptions) {
		o.Pick = func(context.Context) (string, error) { return selected, nil }
	})
	initial, err := s.GetAvatarState()
	if err != nil || initial.ShareAvatar || !initial.ShowContactPhotos || initial.OwnURL != "" {
		t.Fatal("avatar defaults not private", err)
	}
	own, err := s.ChooseProfileAvatar()
	if err != nil || own.ShareAvatar || !strings.HasPrefix(own.OwnURL, "/local-media/") || own.Revision <= initial.Revision {
		t.Fatal("own photo was not private local opaque capability", err)
	}
	response := avatarResponse(s, own.OwnURL)
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), data) || response.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("avatar did not use sandboxed inert handler")
	}
	if same, err := s.GetAvatarState(); err != nil || same.OwnURL != own.OwnURL || same.Revision != own.Revision {
		t.Fatal("unchanged avatar minted new capability or revision")
	}
	if err := s.application.SetPeerAvatar(context.Background(), record.ID, record.Peer, 1, data); err != nil {
		t.Fatal(err)
	}
	shared, err := s.GetAvatarState()
	if err != nil || len(shared.Contacts) != 1 || shared.Contacts[0].URL == "" {
		t.Fatal("contact photo not visible", err)
	}
	peerURL := shared.Contacts[0].URL
	if response := avatarResponse(s, peerURL); response.Code != 200 {
		t.Fatal("approved peer image not served")
	}
	if _, err := s.SetAvatarPreferences(false, false); err != nil {
		t.Fatal(err)
	}
	if response := avatarResponse(s, peerURL); response.Code != 404 {
		t.Fatal("hidden contact image capability survived preference")
	}
	if response := avatarResponse(s, own.OwnURL); response.Code != 200 {
		t.Fatal("hiding contacts hid own image")
	}
	if _, err := s.SetAvatarPreferences(false, true); err != nil {
		t.Fatal(err)
	}
	shared, _ = s.GetAvatarState()
	peerURL = shared.Contacts[0].URL
	if err := s.application.SetPeerAvatar(context.Background(), record.ID, record.Peer, 2, nil); err != nil {
		t.Fatal(err)
	}
	if response := avatarResponse(s, peerURL); response.Code != 404 {
		t.Fatal("remote unshare left cached capability live")
	}
	if _, err := s.RemoveProfileAvatar(); err != nil {
		t.Fatal(err)
	}
	if response := avatarResponse(s, own.OwnURL); response.Code != 404 {
		t.Fatal("own remove left capability live")
	}
	if _, err := s.ChooseProfileAvatar(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForgetContact(record.ID); err != nil {
		t.Fatal(err)
	}
	if response := avatarResponse(s, peerURL); response.Code != 404 {
		t.Fatal("forget left contact photo capability live")
	}
	if s.LockStorage().StorageState != "locked" {
		t.Fatal("lock")
	}
	if _, err := s.GetAvatarState(); err == nil {
		t.Fatal("locked avatar state exposed")
	}
}

func TestAvatarPickerCannotCrossStorageGeneration(t *testing.T) {
	input, _ := avatarSource(t)
	selected := filepath.Join(t.TempDir(), "synthetic-avatar.png")
	if err := os.WriteFile(selected, input, 0600); err != nil {
		t.Fatal(err)
	}
	entered, released := make(chan struct{}), make(chan struct{})
	s, _ := attachmentService(t, func(o *AttachmentRuntimeOptions) {
		o.Pick = func(context.Context) (string, error) { close(entered); <-released; return selected, nil }
	})
	done := make(chan error, 1)
	go func() { _, err := s.ChooseProfileAvatar(); done <- err }()
	<-entered
	s.LockStorage()
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	close(released)
	if err := <-done; err == nil {
		t.Fatal("retired picker changed new profile")
	}
	if state, err := s.GetAvatarState(); err != nil || state.OwnURL != "" {
		t.Fatal("old picker resurrected avatar")
	}
}
