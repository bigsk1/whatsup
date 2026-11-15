package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"whatsup/internal/media"
)

var errAvatar = errors.New("The profile photo could not be updated. Choose a static PNG, JPEG or WebP image within the supported size, and check that local storage is unlocked.")

type AvatarContact struct {
	ContactID string `json:"contactID"`
	URL       string `json:"url"`
}
type AvatarState struct {
	Revision          uint64          `json:"revision"`
	OwnURL            string          `json:"ownURL"`
	ShareAvatar       bool            `json:"shareAvatar"`
	ShowContactPhotos bool            `json:"showContactPhotos"`
	Contacts          []AvatarContact `json:"contacts"`
}
type avatarReference struct {
	contactID string
	digest    [32]byte
}

// The avatar bridge returns local opaque capabilities only. It never returns
// raw image data, native paths, identity claims or a peer-controlled URL.
func (s *Service) GetAvatarState() (AvatarState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.avatarStateLocked()
}

func (s *Service) avatarURLLocked(id string, data []byte) (string, error) {
	if s.attachments == nil || s.attachments.session == nil {
		return "", errAvatar
	}
	session := s.attachments.session
	if session.avatarTokens == nil {
		session.avatarTokens = map[string]avatarReference{}
	}
	digest := sha256.Sum256(data)
	for token, ref := range session.avatarTokens {
		if ref.contactID == id {
			if ref.digest == digest {
				return "/local-media/" + token, nil
			}
			delete(session.avatarTokens, token)
		}
	}
	if len(session.avatarTokens) >= 33 {
		return "", errAvatar
	}
	token, err := opaqueAttachmentID()
	if err != nil {
		return "", errAvatar
	}
	session.avatarTokens[token] = avatarReference{id, digest}
	return "/local-media/" + token, nil
}

func (s *Service) avatarStateLocked() (AvatarState, error) {
	result := AvatarState{Contacts: []AvatarContact{}}
	if s.state != "unlocked" || s.application == nil {
		return result, errAvatar
	}
	local, err := s.application.Profile()
	if err != nil {
		return result, errAvatar
	}
	result.ShareAvatar, result.ShowContactPhotos = local.ShareAvatar, !local.HideContactPhotos
	valid := map[string]bool{}
	if local.Avatar != "" {
		data, err := base64.StdEncoding.Strict().DecodeString(local.Avatar)
		if err != nil {
			return AvatarState{}, errAvatar
		}
		result.OwnURL, err = s.avatarURLLocked("", data)
		if err != nil {
			return AvatarState{}, errAvatar
		}
		valid[""] = true
	}
	records, err := s.application.CommittedContacts()
	if err != nil {
		return AvatarState{}, errAvatar
	}
	for _, record := range records {
		if record.Version != 2 {
			continue
		}
		photo, err := s.application.ContactAvatar(record.ID)
		if err != nil {
			continue
		}
		entry := AvatarContact{ContactID: record.ID}
		if !local.HideContactPhotos && photo.Data != "" {
			data, err := base64.StdEncoding.Strict().DecodeString(photo.Data)
			if err != nil {
				return AvatarState{}, errAvatar
			}
			entry.URL, err = s.avatarURLLocked(record.ID, data)
			if err != nil {
				return AvatarState{}, errAvatar
			}
			valid[record.ID] = true
		}
		result.Contacts = append(result.Contacts, entry)
	}
	if s.attachments != nil && s.attachments.session != nil {
		session := s.attachments.session
		for token, ref := range session.avatarTokens {
			if !valid[ref.contactID] {
				delete(session.avatarTokens, token)
			}
		}
		wire, _ := json.Marshal(result)
		signature := sha256.Sum256(wire)
		if session.avatarRevision == 0 || signature != session.avatarSignature {
			if session.avatarRevision >= 1<<53-1 {
				return AvatarState{}, errAvatar
			}
			session.avatarRevision++
			session.avatarSignature = signature
		}
		result.Revision = session.avatarRevision
	} else {
		result.Revision = 1
	}
	return result, nil
}

func (s *Service) SetAvatarPreferences(shareAvatar, showContactPhotos bool) (AvatarState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.application == nil {
		return AvatarState{}, errAvatar
	}
	if _, err := s.application.SetAvatarPreferences(shareAvatar, showContactPhotos); err != nil {
		return AvatarState{}, errAvatar
	}
	if s.messages != nil {
		s.messages.MetadataChanged("")
	}
	return s.avatarStateLocked()
}

func (s *Service) RemoveProfileAvatar() (AvatarState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.application == nil {
		return AvatarState{}, errAvatar
	}
	if _, err := s.application.SetAvatar(context.Background(), nil); err != nil {
		return AvatarState{}, errAvatar
	}
	if s.messages != nil {
		s.messages.MetadataChanged("")
	}
	return s.avatarStateLocked()
}

func (s *Service) ChooseProfileAvatar() (AvatarState, error) {
	s.mu.Lock()
	if s.state != "unlocked" || s.application == nil || s.attachments == nil || s.attachments.session == nil {
		s.mu.Unlock()
		return AvatarState{}, errAvatar
	}
	session, options, work := s.attachments.session, s.attachments.options, s.attachments.work
	s.mu.Unlock()
	select {
	case work <- struct{}{}:
		defer func() { <-work }()
	case <-session.life.Done():
		return AvatarState{}, errAvatar
	default:
		return AvatarState{}, errAvatar
	}
	pick := options.PickAvatar
	if pick == nil {
		pick = options.Pick
	}
	filename, err := pick(session.life)
	if err != nil {
		return AvatarState{}, errAvatar
	}
	if filename == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.attachments == nil || s.attachments.session != session {
			return AvatarState{}, errAvatar
		}
		return s.avatarStateLocked()
	}
	if !filepath.IsAbs(filename) || strings.ContainsRune(filename, 0) {
		return AvatarState{}, errAvatar
	}
	before, err := os.Lstat(filename)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > media.MaxAvatarInputBytes {
		return AvatarState{}, errAvatar
	}
	file, err := os.Open(filename)
	if err != nil {
		return AvatarState{}, errAvatar
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return AvatarState{}, errAvatar
	}
	data, err := media.PrepareAvatar(session.life, file)
	after, statErr := file.Stat()
	if err != nil || statErr != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || opened.ModTime() != after.ModTime() {
		return AvatarState{}, errAvatar
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.application != session.application || s.attachments == nil || s.attachments.session != session || session.life.Err() != nil {
		return AvatarState{}, errAvatar
	}
	if _, err := s.application.SetAvatar(session.life, data); err != nil {
		return AvatarState{}, errAvatar
	}
	if s.messages != nil {
		s.messages.MetadataChanged("")
	}
	return s.avatarStateLocked()
}

// Called only with Service.mu held, before the existing attachment capability
// lookup. Live storage and preferences are rechecked on every request, so an old
// URL cannot reveal a removed, forgotten, hidden or replaced photo.
func (s *Service) avatarDataLocked(token string) ([]byte, string, bool) {
	if s.state != "unlocked" || s.application == nil || s.attachments == nil || s.attachments.session == nil {
		return nil, "", false
	}
	ref, ok := s.attachments.session.avatarTokens[token]
	if !ok {
		return nil, "", false
	}
	local, err := s.application.Profile()
	if err != nil {
		return nil, "", true
	}
	encoded := local.Avatar
	if ref.contactID != "" {
		if local.HideContactPhotos {
			return nil, "", true
		}
		photo, err := s.application.ContactAvatar(ref.contactID)
		if err != nil {
			return nil, "", true
		}
		encoded = photo.Data
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || sha256.Sum256(data) != ref.digest {
		return nil, "", true
	}
	mime, err := media.AvatarType(data)
	if err != nil {
		return nil, "", true
	}
	return data, mime, true
}

func serveAvatar(w http.ResponseWriter, r *http.Request, data []byte, mime string) {
	if len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodGet {
		_, _ = bytes.NewReader(data).WriteTo(w)
	}
}

func (AvatarState) String() string     { return "[private WhatsUp avatar state]" }
func (s AvatarState) GoString() string { return fmt.Sprint(s) }
