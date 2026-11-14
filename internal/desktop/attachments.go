package desktop

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/diagnostics"
	"whatsup/internal/media"
	"whatsup/internal/messaging"
	"whatsup/internal/storage"
	"whatsup/internal/transfer"
)

var errAttachment = errors.New("The attachment action could not be completed. Check the selected contact and local storage, then try again.")
var errImageAsFile = errors.New("This image cannot be safely prepared for preview. Choose Send as file to send the original without an image preview.")

func attachmentActionError(err error) error {
	if errors.Is(err, transfer.ErrCapacity) {
		return errors.New("Attachment storage or the active transfer limit is full. Clear this contact's history or finish another transfer before trying again.")
	}
	if errors.Is(err, transfer.ErrIntegrity) {
		return errors.New("The file failed its integrity or image checks and was not made available.")
	}
	return errAttachment
}

// AttachmentRuntimeOptions is construction input for Go only. Paths and native
// capabilities are never exposed through the bound Service or renderer events.
type AttachmentRuntimeOptions struct {
	Life      context.Context
	Pick      func(context.Context) (string, error)
	Clipboard func(context.Context) ([]byte, error)
	Save      func(context.Context, string) (string, error)
	Reveal    func(context.Context, string) error
	Serve     func(http.ResponseWriter, *http.Request, io.ReadSeekCloser, int64) error
}

type attachmentControl struct {
	options AttachmentRuntimeOptions
	session *attachmentSession
	work    chan struct{}
}

type attachmentSession struct {
	application *storage.ApplicationState
	messages    *messaging.Registry
	life        context.Context
	cancel      context.CancelFunc
	contacts    map[string]*attachmentContact
	selected    string
	drafts      map[string]attachmentReference
	assets      map[string]attachmentReference
	saved       map[string]savedAttachment
	images      *attachmentImageCache
}

type attachmentContact struct {
	life   context.Context
	cancel context.CancelFunc
}
type attachmentReference struct{ contactID, transferID, kind string }
type savedAttachment struct {
	reference attachmentReference
	path      string
	info      os.FileInfo
}

type attachmentLease struct {
	session *attachmentSession
	contact *attachmentContact
	scope   *attachmentstore.Scope
	id      string
	options AttachmentRuntimeOptions
	work    chan struct{}
}

type AttachmentDraft struct {
	Handle               string `json:"handle"`
	Name                 string `json:"name"`
	MediaType            string `json:"mediaType"`
	Size                 uint64 `json:"size"`
	PreviewURL           string `json:"previewURL"`
	RequiresRelayWarning bool   `json:"requiresRelayWarning"`
}

type AttachmentView struct {
	transfer.Summary
	PreviewURL string `json:"previewURL"`
}

type Attachments struct {
	ContactID        string            `json:"contactID"`
	Revision         uint64            `json:"revision"`
	Items            []AttachmentView  `json:"items"`
	Drafts           []AttachmentDraft `json:"drafts"`
	AutoAcceptImages bool              `json:"autoAcceptImages"`
	NextCursor       string            `json:"nextCursor"`
	HasMore          bool              `json:"hasMore"`
}

type SavedAttachment struct {
	Handle string `json:"handle"`
}

func opaqueAttachmentID() (string, error) {
	var data [24]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errAttachment
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}
func transferID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errAttachment
	}
	id := base64.RawURLEncoding.EncodeToString(data[:])
	if !transfer.ValidID(id) {
		return "", errAttachment
	}
	return id, nil
}

func ConfigureAttachments(s *Service, options AttachmentRuntimeOptions) error {
	if s == nil || options.Pick == nil || options.Clipboard == nil || options.Save == nil || options.Reveal == nil || options.Serve == nil {
		return errAttachment
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "locked" || s.vault != nil || s.attachments != nil {
		return errAttachment
	}
	s.attachments = &attachmentControl{options: options, work: make(chan struct{}, 2)}
	return nil
}
func (s *Service) startAttachmentsLocked() {
	if s.attachments == nil {
		return
	}
	s.stopAttachmentsLocked()
	parent := s.attachments.options.Life
	if parent == nil {
		parent = context.Background()
	}
	life, cancel := context.WithCancel(parent)
	s.attachments.session = &attachmentSession{application: s.application, messages: s.messages, life: life, cancel: cancel, contacts: map[string]*attachmentContact{}, drafts: map[string]attachmentReference{}, assets: map[string]attachmentReference{}, saved: map[string]savedAttachment{}, images: newAttachmentImageCache()}
}
func (s *Service) stopAttachmentsLocked() {
	if s.attachments != nil && s.attachments.session != nil {
		s.attachments.session.cancel()
		s.attachments.session.images.invalidate("", "")
		s.attachments.session = nil
	}
}
func (s *Service) retireAttachmentsLocked(id string) {
	if s.attachments == nil || s.attachments.session == nil {
		return
	}
	g := s.attachments.session
	g.images.invalidate(id, "")
	if contact := g.contacts[id]; contact != nil {
		contact.cancel()
		delete(g.contacts, id)
	}
	if g.selected == id {
		g.selected = ""
	}
	for handle, ref := range g.drafts {
		if ref.contactID == id {
			delete(g.drafts, handle)
		}
	}
	for handle, ref := range g.assets {
		if ref.contactID == id {
			delete(g.assets, handle)
		}
	}
	for handle, saved := range g.saved {
		if saved.reference.contactID == id {
			delete(g.saved, handle)
		}
	}
}
func (s *Service) attachmentLocked(id string) (attachmentLease, error) {
	if s.state != "unlocked" || s.application == nil || s.messages == nil || s.attachments == nil || s.attachments.session == nil || !transfer.ValidID(id) {
		return attachmentLease{}, errAttachment
	}
	g := s.attachments.session
	if g.application != s.application || g.life.Err() != nil {
		return attachmentLease{}, errAttachment
	}
	scope, err := s.application.AttachmentStore(id)
	if err != nil {
		return attachmentLease{}, errAttachment
	}
	contact := g.contacts[id]
	if contact == nil {
		life, cancel := context.WithCancel(g.life)
		contact = &attachmentContact{life, cancel}
		g.contacts[id] = contact
	}
	return attachmentLease{g, contact, scope, id, s.attachments.options, s.attachments.work}, nil
}
func (s *Service) attachmentLease(id string) (attachmentLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attachmentLocked(id)
}
func (s *Service) attachmentCurrentLocked(l attachmentLease) bool {
	return s.attachments != nil && s.attachments.session == l.session && s.state == "unlocked" && s.application == l.session.application && l.contact.life.Err() == nil && l.session.contacts[l.id] == l.contact
}
func (l attachmentLease) begin() error {
	select {
	case l.work <- struct{}{}:
		if l.contact.life.Err() != nil {
			<-l.work
			return errAttachment
		}
		return nil
	default:
		return errors.New("Another file action is in progress. Try again shortly.")
	}
}
func (l attachmentLease) end() { <-l.work }

// SetAttachmentInputContact selects only an existing approved association for
// subsequent OS-native drop input. No renderer-supplied path is accepted.
func (s *Service) SetAttachmentInputContact(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		if s.attachments != nil && s.attachments.session != nil {
			s.attachments.session.selected = ""
		}
		return nil
	}
	l, err := s.attachmentLocked(id)
	if err != nil {
		return err
	}
	l.session.selected = id
	return nil
}

func safeAttachmentName(name string) string {
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`/\<>:"|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(strings.TrimSpace(name), ". ")
	for len(name) > 200 {
		name = name[:len(name)-1]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
	}
	if name == "" {
		name = "attachment"
	}
	return name
}

func (s *Service) ChooseAttachment(id string, sendAsFile bool) (AttachmentDraft, error) {
	l, err := s.attachmentLease(id)
	if err != nil {
		return AttachmentDraft{}, err
	}
	if err = l.begin(); err != nil {
		return AttachmentDraft{}, err
	}
	defer l.end()
	filename, err := l.options.Pick(l.contact.life)
	if err != nil {
		return AttachmentDraft{}, errAttachment
	}
	if filename == "" {
		return AttachmentDraft{}, nil
	}
	return s.stagePath(l, filename, sendAsFile)
}

func (s *Service) PasteAttachment(id string) (AttachmentDraft, error) {
	l, err := s.attachmentLease(id)
	if err != nil {
		return AttachmentDraft{}, err
	}
	if err = l.begin(); err != nil {
		return AttachmentDraft{}, err
	}
	defer l.end()
	data, err := l.options.Clipboard(l.contact.life)
	if err != nil || len(data) == 0 || len(data) > 256<<20 {
		return AttachmentDraft{}, errAttachment
	}
	defer clear(data)
	return s.stageReader(l, bytes.NewReader(data), "pasted-image.png", false, true)
}

// StageNativeDrop is Go-only. The reviewed Wails native callback supplies these
// paths directly; DOM file objects and renderer paths never reach this method.
func StageNativeDrop(s *Service, paths []string) error {
	if s == nil || len(paths) != 1 {
		return errAttachment
	}
	s.mu.Lock()
	id := ""
	if s.attachments != nil && s.attachments.session != nil {
		id = s.attachments.session.selected
	}
	l, err := s.attachmentLocked(id)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err = l.begin(); err != nil {
		return err
	}
	defer l.end()
	_, err = s.stagePath(l, paths[0], false)
	return err
}

func (s *Service) stagePath(l attachmentLease, filename string, sendAsFile bool) (AttachmentDraft, error) {
	if !filepath.IsAbs(filename) || strings.ContainsRune(filename, 0) {
		return AttachmentDraft{}, errAttachment
	}
	before, err := os.Lstat(filename)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || uint64(before.Size()) > transfer.MaxFileBytes {
		return AttachmentDraft{}, errAttachment
	}
	file, err := os.Open(filename)
	if err != nil {
		return AttachmentDraft{}, errAttachment
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return AttachmentDraft{}, errAttachment
	}
	draft, err := s.stageReader(l, file, safeAttachmentName(filename), sendAsFile, false)
	after, statErr := file.Stat()
	if err == nil && (statErr != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || opened.ModTime() != after.ModTime()) {
		_ = s.discardAttachmentDraft(l, draft.Handle)
		return AttachmentDraft{}, errAttachment
	}
	return draft, err
}

func (s *Service) stageReader(l attachmentLease, source io.ReadSeeker, name string, asFile, requireImage bool) (AttachmentDraft, error) {
	if l.contact.life.Err() != nil {
		return AttachmentDraft{}, errAttachment
	}
	kind, mime := media.KindFile, "application/octet-stream"
	var err error
	if !asFile {
		kind, mime, err = media.Sniff(source)
		if err != nil {
			return AttachmentDraft{}, errAttachment
		}
		if requireImage && kind != media.KindImage || kind != media.KindImage && media.LooksLikeImage(source, name) {
			return AttachmentDraft{}, errImageAsFile
		}
	}
	var prepared []byte
	if !asFile && kind == "image" {
		value, err := media.Prepare(l.contact.life, source)
		if err != nil {
			return AttachmentDraft{}, errImageAsFile
		}
		prepared = value.Data
		defer clear(prepared)
		source = bytes.NewReader(prepared)
		mime = value.MediaType
		name = strings.TrimSuffix(name, filepath.Ext(name)) + value.Extension()
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return AttachmentDraft{}, errAttachment
	}
	digest := sha256.New()
	count, err := io.CopyBuffer(digest, io.LimitReader(&contextAttachmentReader{l.contact.life, source}, int64(transfer.MaxFileBytes)+1), make([]byte, 64<<10))
	if err != nil || uint64(count) > transfer.MaxFileBytes {
		return AttachmentDraft{}, errAttachment
	}
	id, err := transferID()
	if err != nil {
		return AttachmentDraft{}, err
	}
	offer := transfer.Offer{ID: id, Name: safeAttachmentName(name), MediaType: mime, Size: uint64(count)}
	copy(offer.SHA256[:], digest.Sum(nil))
	if offer.Validate() != nil {
		return AttachmentDraft{}, errAttachment
	}
	if _, err = source.Seek(0, io.SeekStart); err != nil {
		return AttachmentDraft{}, errAttachment
	}
	if err = l.scope.Stage(l.contact.life, offer, source); err != nil {
		s.record(diagnostics.TransferFailed)
		if errors.Is(err, transfer.ErrCapacity) {
			s.record(diagnostics.CapacityReached)
		}
		return AttachmentDraft{}, attachmentActionError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.attachmentCurrentLocked(l) {
		_ = l.scope.RemoveDraft(id)
		return AttachmentDraft{}, errAttachment
	}
	handle, err := opaqueAttachmentID()
	if err != nil {
		_ = l.scope.RemoveDraft(id)
		return AttachmentDraft{}, err
	}
	l.session.drafts[handle] = attachmentReference{l.id, id, "draft"}
	view := s.draftViewLocked(l, handle, offer)
	_ = l.session.messages.WakeTransfers(l.id)
	return view, nil
}

type contextAttachmentReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextAttachmentReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *Service) assetURLLocked(l attachmentLease, id, kind string) string {
	for token, ref := range l.session.assets {
		if ref.contactID == l.id && ref.transferID == id && ref.kind == kind {
			return "/local-media/" + token
		}
	}
	if len(l.session.assets) >= 4096 {
		return ""
	}
	token, err := opaqueAttachmentID()
	if err != nil {
		return ""
	}
	l.session.assets[token] = attachmentReference{l.id, id, kind}
	return "/local-media/" + token
}
func (s *Service) draftViewLocked(l attachmentLease, handle string, o transfer.Offer) AttachmentDraft {
	preview := ""
	if media.AllowedImage(o.MediaType) {
		preview = s.assetURLLocked(l, o.ID, "thumbnail")
	}
	path := l.session.messages.Snapshot(l.id).Path
	return AttachmentDraft{handle, o.Name, o.MediaType, o.Size, preview, o.Size > transfer.LargeFileBytes && path != "Direct"}
}

func (s *Service) GetAttachments(id string) (Attachments, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Attachments{ContactID: id, Items: []AttachmentView{}, Drafts: []AttachmentDraft{}}
	l, err := s.attachmentLocked(id)
	if err != nil {
		return result, err
	}
	result.Revision = s.messages.Snapshot(id).Revision
	items, err := s.messages.Transfers(id)
	if err != nil {
		return result, errAttachment
	}
	for _, item := range items {
		if item.State == "draft" {
			continue
		}
		view := AttachmentView{Summary: item}
		if item.State == transfer.Complete && media.AllowedImage(item.MediaType) {
			view.PreviewURL = s.assetURLLocked(l, item.ID, "thumbnail")
		}
		result.Items = append(result.Items, view)
	}
	if err = s.attachmentHistoryLocked(l, &result, 0); err != nil {
		return result, err
	}
	// Rebuild opaque handles for retained encrypted drafts after a restart. Drafts
	// remain local and are never queued merely by opening this list.
	records, err := l.scope.List()
	if err != nil {
		return result, errAttachment
	}
	for _, record := range records {
		if record.State != "draft" || record.Hidden {
			continue
		}
		handle := ""
		for candidate, ref := range l.session.drafts {
			if ref.contactID == id && ref.transferID == record.Offer.ID {
				handle = candidate
				break
			}
		}
		if handle == "" {
			handle, err = opaqueAttachmentID()
			if err != nil {
				return result, err
			}
			l.session.drafts[handle] = attachmentReference{id, record.Offer.ID, "draft"}
		}
		result.Drafts = append(result.Drafts, s.draftViewLocked(l, handle, record.Offer))
	}
	result.AutoAcceptImages, err = l.scope.AutoAcceptImages()
	if err != nil {
		return result, errAttachment
	}
	return result, nil
}

func (s *Service) attachmentHistoryLocked(l attachmentLease, result *Attachments, before uint64) error {
	records, next, more, err := l.scope.History(before, 32)
	if err != nil {
		return errAttachment
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		seen[item.ID] = true
	}
	for _, record := range records {
		if record.State == transfer.Draft || record.Hidden || seen[record.Offer.ID] {
			continue
		}
		progress := uint64(record.NextChunk) * transfer.ChunkBytes
		if progress > record.Offer.Size || record.State == transfer.Complete {
			progress = record.Offer.Size
		}
		view := AttachmentView{Summary: transfer.Summary{ID: record.Offer.ID, Name: record.Offer.Name, MediaType: record.Offer.MediaType, Size: record.Offer.Size, Direction: record.Direction, State: record.State, TransferredBytes: progress}}
		if record.State == transfer.Complete {
			if mime, typeErr := l.scope.VerifiedMediaType(record.Offer.ID); typeErr == nil {
				view.MediaType = mime
			}
			if media.AllowedImage(view.MediaType) {
				view.PreviewURL = s.assetURLLocked(l, record.Offer.ID, "thumbnail")
			}
		}
		result.Items = append(result.Items, view)
		seen[record.Offer.ID] = true
	}
	result.HasMore = more
	if more {
		if next == 0 {
			return errAttachment
		}
		result.NextCursor = strconv.FormatUint(next, 10)
	}
	return nil
}

func (s *Service) GetAttachmentHistory(id, cursor string) (Attachments, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := Attachments{ContactID: id, Items: []AttachmentView{}, Drafts: []AttachmentDraft{}}
	before, err := strconv.ParseUint(cursor, 10, 63)
	if err != nil || before == 0 || strconv.FormatUint(before, 10) != cursor {
		return result, errAttachment
	}
	l, err := s.attachmentLocked(id)
	if err != nil {
		return result, err
	}
	result.Revision = s.messages.Snapshot(id).Revision
	if err = s.attachmentHistoryLocked(l, &result, before); err != nil {
		return result, err
	}
	result.AutoAcceptImages, err = l.scope.AutoAcceptImages()
	if err != nil {
		return result, errAttachment
	}
	return result, nil
}

func (s *Service) SendAttachment(id, handle string, allowRelayedLarge bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.attachmentLocked(id)
	if err != nil {
		return err
	}
	if contact := s.messages.Snapshot(id); contact.PeerProtocol == 3 {
		return errors.New("This contact can exchange text, but needs an app update before receiving attachments. The local draft is kept.")
	}
	ref, ok := l.session.drafts[handle]
	if !ok || ref.contactID != id {
		return errAttachment
	}
	if err = s.messages.QueueTransfer(id, ref.transferID, allowRelayedLarge); err != nil {
		return errors.New("The file could not be queued. Large files may need confirmation to use relay bandwidth.")
	}
	delete(l.session.drafts, handle)
	return nil
}
func (s *Service) discardAttachmentDraft(l attachmentLease, handle string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.attachmentCurrentLocked(l) {
		return errAttachment
	}
	ref, ok := l.session.drafts[handle]
	if !ok || ref.contactID != l.id {
		return errAttachment
	}
	if l.scope.RemoveDraft(ref.transferID) != nil {
		return errAttachment
	}
	delete(l.session.drafts, handle)
	l.session.images.invalidate(l.id, ref.transferID)
	for token, asset := range l.session.assets {
		if asset.contactID == l.id && asset.transferID == ref.transferID {
			delete(l.session.assets, token)
		}
	}
	return l.session.messages.WakeTransfers(l.id)
}
func (s *Service) DiscardAttachment(id, handle string) error {
	l, err := s.attachmentLease(id)
	if err != nil {
		return err
	}
	return s.discardAttachmentDraft(l, handle)
}
func (s *Service) SetAutoAcceptImages(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.attachmentLocked(id)
	if err != nil {
		return err
	}
	if l.scope.SetAutoAcceptImages(enabled) != nil {
		return errAttachment
	}
	return s.messages.WakeTransfers(id)
}
func (s *Service) attachmentDecision(id, attachment, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.attachmentLocked(id); err != nil || !transfer.ValidID(attachment) {
		return errAttachment
	}
	var err error
	switch action {
	case "allow-relay":
		err = s.messages.AllowTransferRelay(id, attachment)
	case "accept":
		err = s.messages.AcceptTransfer(id, attachment)
	case "decline":
		err = s.messages.DeclineTransfer(id, attachment)
	case "cancel":
		err = s.messages.CancelTransfer(id, attachment)
	case "withdraw":
		err = s.messages.WithdrawTransfer(id, attachment)
	case "resume":
		err = s.messages.ResumeTransfer(id, attachment)
	default:
		return errAttachment
	}
	if err != nil {
		s.record(diagnostics.TransferFailed)
		if errors.Is(err, transfer.ErrCapacity) {
			s.record(diagnostics.CapacityReached)
		}
		return attachmentActionError(err)
	}
	return nil
}
func (s *Service) AcceptAttachment(id, attachment string) error {
	return s.attachmentDecision(id, attachment, "accept")
}
func (s *Service) DeclineAttachment(id, attachment string) error {
	return s.attachmentDecision(id, attachment, "decline")
}
func (s *Service) CancelAttachment(id, attachment string) error {
	return s.attachmentDecision(id, attachment, "cancel")
}
func (s *Service) WithdrawAttachment(id, attachment string) error {
	return s.attachmentDecision(id, attachment, "withdraw")
}
func (s *Service) ResumeAttachment(id, attachment string) error {
	return s.attachmentDecision(id, attachment, "resume")
}

func (s *Service) AllowAttachmentRelay(id, attachment string) error {
	return s.attachmentDecision(id, attachment, "allow-relay")
}

func (s *Service) PreviewAttachment(id, attachment string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.attachmentLocked(id)
	if err != nil {
		return "", err
	}
	item, err := l.scope.Get(attachment)
	if err != nil || item.Hidden || item.State != transfer.Complete {
		return "", errAttachment
	}
	mime, err := l.scope.VerifiedMediaType(attachment)
	if err != nil || !media.AllowedImage(mime) && mime != "video/mp4" && mime != "video/webm" {
		return "", errAttachment
	}
	kind := "video"
	if media.AllowedImage(mime) {
		kind = "image"
	}
	url := s.assetURLLocked(l, attachment, kind)
	if url == "" {
		return "", errAttachment
	}
	return url, nil
}

func (s *Service) SaveAttachment(id, attachment string) (SavedAttachment, error) {
	l, err := s.attachmentLease(id)
	if err != nil {
		return SavedAttachment{}, err
	}
	if err = l.begin(); err != nil {
		return SavedAttachment{}, err
	}
	defer l.end()
	item, err := l.scope.Get(attachment)
	if err != nil || item.Hidden || item.State != transfer.Complete {
		return SavedAttachment{}, errAttachment
	}
	filename, err := l.options.Save(l.contact.life, item.Offer.Name)
	if err != nil {
		return SavedAttachment{}, errAttachment
	}
	if filename == "" {
		return SavedAttachment{}, nil
	}
	if !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
		return SavedAttachment{}, errAttachment
	}
	input, err := l.scope.Open(l.contact.life, attachment)
	if err != nil {
		return SavedAttachment{}, errAttachment
	}
	defer input.Close()
	// The explicit save destination is the sole plaintext output. Exclusive
	// creation preserves existing files and avoids plaintext temporary files.
	output, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return SavedAttachment{}, errors.New("Choose a new file name. Existing files are kept.")
	}
	created, _ := output.Stat()
	success := false
	defer func() {
		_ = output.Close()
		if !success && created != nil {
			if current, e := os.Lstat(filename); e == nil && os.SameFile(created, current) && current.Mode().IsRegular() {
				_ = os.Remove(filename)
			}
		}
	}()
	if created == nil || !created.Mode().IsRegular() {
		return SavedAttachment{}, errAttachment
	}
	hash := sha256.New()
	count, err := io.CopyBuffer(io.MultiWriter(output, hash), io.LimitReader(input, int64(item.Offer.Size)+1), make([]byte, 64<<10))
	if err != nil || uint64(count) != item.Offer.Size || !bytes.Equal(hash.Sum(nil), item.Offer.SHA256[:]) || l.contact.life.Err() != nil || output.Sync() != nil {
		return SavedAttachment{}, errAttachment
	}
	if output.Close() != nil {
		return SavedAttachment{}, errAttachment
	}
	info, err := os.Lstat(filename)
	if err != nil || created == nil || !os.SameFile(created, info) || !info.Mode().IsRegular() {
		return SavedAttachment{}, errAttachment
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.attachmentCurrentLocked(l) {
		return SavedAttachment{}, errAttachment
	}
	if len(l.session.saved) >= 256 {
		return SavedAttachment{}, errAttachment
	}
	handle, err := opaqueAttachmentID()
	if err != nil {
		return SavedAttachment{}, err
	}
	l.session.saved[handle] = savedAttachment{attachmentReference{id, attachment, "saved"}, filename, info}
	success = true
	return SavedAttachment{handle}, nil
}
func (s *Service) ShowSavedAttachment(id, handle string) error {
	s.mu.Lock()
	l, err := s.attachmentLocked(id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	saved, ok := l.session.saved[handle]
	s.mu.Unlock()
	if !ok || saved.reference.contactID != id {
		return errAttachment
	}
	current, err := os.Lstat(saved.path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(saved.info, current) || l.contact.life.Err() != nil {
		return errAttachment
	}
	if l.options.Reveal(l.contact.life, saved.path) != nil {
		return errAttachment
	}
	return nil
}

// ServeAttachment handles only unguessable, session-scoped local asset IDs.
// Incomplete incoming transfers, locked storage and retired contacts fail closed.
// It runs outside Service.mu while reading or talking to the native webview.
func ServeAttachment(s *Service, w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/local-media/") {
		return false
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	deny := func() { http.NotFound(w, r) }
	if s == nil || r.URL.RawQuery != "" || r.Method != http.MethodGet && r.Method != http.MethodHead {
		deny()
		return true
	}
	token := strings.TrimPrefix(r.URL.Path, "/local-media/")
	if len(token) != 32 || strings.ContainsAny(token, "/\\%.") {
		deny()
		return true
	}
	s.mu.Lock()
	if s.attachments == nil || s.attachments.session == nil {
		s.mu.Unlock()
		deny()
		return true
	}
	ref, ok := s.attachments.session.assets[token]
	if !ok {
		s.mu.Unlock()
		deny()
		return true
	}
	l, err := s.attachmentLocked(ref.contactID)
	s.mu.Unlock()
	if err != nil {
		deny()
		return true
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(l.contact.life, cancel)
	defer stop()
	item, err := l.scope.Get(ref.transferID)
	mime, typeErr := l.scope.VerifiedMediaType(ref.transferID)
	if err != nil || typeErr != nil || item.Hidden {
		deny()
		return true
	}
	input, err := l.scope.Open(ctx, ref.transferID)
	if err != nil {
		deny()
		return true
	}
	if ref.kind == "thumbnail" || ref.kind == "image" {
		defer input.Close()
		if !media.AllowedImage(mime) || ref.kind == "image" && item.State != transfer.Complete {
			deny()
			return true
		}
		image, err := l.session.images.load(ctx, ref, func() (attachmentImage, error) {
			if err := l.begin(); err != nil {
				return attachmentImage{}, err
			}
			defer l.end()
			if ref.kind == "thumbnail" {
				data, err := media.Thumbnail(ctx, input)
				return attachmentImage{data: data, mime: "image/png"}, err
			}
			prepared, err := media.Prepare(ctx, input)
			clear(prepared.Thumbnail)
			return attachmentImage{data: prepared.Data, mime: prepared.MediaType}, err
		})
		defer clear(image.data)
		if err != nil || ctx.Err() != nil {
			deny()
			return true
		}
		s.mu.Lock()
		current := s.attachmentCurrentLocked(l) && l.session.assets[token] == ref
		s.mu.Unlock()
		if !current || !media.AllowedImage(image.mime) {
			deny()
			return true
		}
		w.Header().Set("Content-Type", image.mime)
		http.ServeContent(w, r, "image", zeroAttachmentTime, bytes.NewReader(image.data))
		return true
	}
	if err != nil || typeErr != nil || item.Hidden || item.State != transfer.Complete || ref.kind != "video" || mime != "video/mp4" && mime != "video/webm" {
		input.Close()
		deny()
		return true
	}
	w.Header().Set("Content-Type", mime)
	if l.options.Serve(w, r.WithContext(ctx), input, int64(item.Offer.Size)) != nil {
		s.record(diagnostics.TransferFailed)
	}
	return true
}

// Keep media responses free of filesystem timestamps and name-derived headers.
var zeroAttachmentTime time.Time
