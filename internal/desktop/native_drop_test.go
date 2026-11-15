package desktop

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"whatsup/internal/transfer"
)

func nativeDropFixture(t *testing.T, kind string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "synthetic-native-drop."+kind)
	data := []byte("%PDF-1.4\nSynthetic native file drop fixture.\n%%EOF\n")
	if kind == "png" {
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 8, 5))); err != nil {
			t.Fatal(err)
		}
		data = encoded.Bytes()
	} else if kind == "mp4" {
		// A bounded ISO-BMFF type box exercises content sniffing/staging only;
		// it is not a playable movie or evidence of native codec support.
		data = binary.BigEndian.AppendUint32(nil, 24)
		data = append(data, "ftypisom\x00\x00\x00\x00isommp42"...)
	}
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func assertNativeDraftOnly(t *testing.T, s *Service, id string, count int) Attachments {
	t.Helper()
	list, err := s.GetAttachments(id)
	if err != nil || list.ContactID != id || len(list.Drafts) != count || len(list.Items) != 0 {
		t.Fatal("native input did not remain local draft work for exact contact", err)
	}
	scope, err := s.application.AttachmentStore(id)
	if err != nil {
		t.Fatal(err)
	}
	records, err := scope.List()
	if err != nil || len(records) != count {
		t.Fatal("draft records were lost or duplicated", err)
	}
	for _, record := range records {
		if record.State != transfer.Draft || record.Direction != transfer.Outgoing || record.NextChunk != 0 || record.Hidden {
			t.Fatal("native drop queued or sent a file without explicit Send")
		}
	}
	if current := s.messages.Snapshot(id); current.State != "offline" {
		t.Fatal("native input started network work")
	}
	return list
}

func TestNativeDropNeedsExactApprovedInputContactAndPreparesOnlyDraft(t *testing.T) {
	for _, kind := range []string{"png", "pdf", "mp4"} {
		t.Run(kind, func(t *testing.T) {
			file := nativeDropFixture(t, kind)
			s, first := attachmentService(t, nil)
			second := prepareDesktopForgetContact(t, s)
			if err := StageNativeDrop(s, []string{file}); err == nil {
				t.Fatal("unbound native input chose a contact implicitly")
			}
			assertNativeDraftOnly(t, s, first.ID, 0)
			assertNativeDraftOnly(t, s, second.ID, 0)
			if err := s.SetAttachmentInputContact(second.ID); err != nil {
				t.Fatal(err)
			}
			before := s.messages.Snapshot(second.ID).Revision
			if err := StageNativeDrop(s, []string{file}); err != nil {
				t.Fatal("native physical-file path did not stage", err)
			}
			list := assertNativeDraftOnly(t, s, second.ID, 1)
			assertNativeDraftOnly(t, s, first.ID, 0)
			if list.Revision <= before || len(list.Drafts[0].Handle) != 32 {
				t.Fatal("native draft did not publish contact revision and opaque draft handle")
			}
			wantType := map[string]string{"png": "image/png", "pdf": "application/pdf", "mp4": "video/mp4"}[kind]
			if list.Drafts[0].MediaType != wantType {
				t.Fatal("native input did not sniff the expected image/document/video type")
			}
			if kind == "png" && !strings.HasPrefix(list.Drafts[0].PreviewURL, "/local-media/") || kind != "png" && list.Drafts[0].PreviewURL != "" {
				t.Fatal("native preview did not follow inert image/file policy")
			}
			encoded, err := json.Marshal(list)
			if err != nil || bytes.Contains(encoded, []byte(file)) || bytes.Contains(encoded, []byte(s.directory)) {
				t.Fatal("native drop exposed private source/storage paths")
			}
		})
	}
}

func TestNativeDropAfterClearHistoryNeedsSameContactRebindWhilePickerWorks(t *testing.T) {
	imageFile, pdfFile := nativeDropFixture(t, "png"), nativeDropFixture(t, "pdf")
	s, contact := attachmentService(t, func(options *AttachmentRuntimeOptions) {
		options.Pick = func(context.Context) (string, error) { return pdfFile, nil }
	})
	if err := s.SetAttachmentInputContact(contact.ID); err != nil {
		t.Fatal(err)
	}
	if err := StageNativeDrop(s, []string{imageFile}); err != nil {
		t.Fatal(err)
	}
	before := assertNativeDraftOnly(t, s, contact.ID, 1)
	old, err := s.attachmentLease(contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := s.ClearHistory(contact.ID)
	if err != nil || cleared.ContactID != contact.ID || cleared.Revision <= before.Revision {
		t.Fatal("history clear failed to retire exact contact revision", err)
	}
	if err := old.begin(); err == nil {
		old.end()
		t.Fatal("cleared native-drop lease retained authority")
	}
	if err := StageNativeDrop(s, []string{pdfFile}); err == nil {
		t.Fatal("cleared input selection revived implicitly")
	}
	assertNativeDraftOnly(t, s, contact.ID, 0)
	if err := s.SendAttachment(contact.ID, before.Drafts[0].Handle, false); err == nil {
		t.Fatal("clear kept old draft send authority")
	}
	picked, err := s.ChooseAttachment(contact.ID, false)
	if err != nil || picked.Handle == "" {
		t.Fatal("explicit paperclip action failed while native input was unbound", err)
	}
	assertNativeDraftOnly(t, s, contact.ID, 1)
	if err := StageNativeDrop(s, []string{imageFile}); err == nil {
		t.Fatal("paperclip silently rebound native input")
	}
	if err := s.SetAttachmentInputContact(contact.ID); err != nil {
		t.Fatal(err)
	}
	if err := StageNativeDrop(s, []string{imageFile}); err != nil {
		t.Fatal("same-contact typed rebind did not restore native drop", err)
	}
	after := assertNativeDraftOnly(t, s, contact.ID, 2)
	if after.Revision <= cleared.Revision {
		t.Fatal("rebound drop did not publish a newer exact-contact revision")
	}
	t.Log("Clear history retires native selection; GetAttachments and paperclip do not rebind it; SetAttachmentInputContact(same ID) restores the drop path.")
}

func TestNativeDropRetiresOnSessionReplacementLockAndForget(t *testing.T) {
	for _, action := range []string{"session-replacement", "lock", "forget"} {
		t.Run(action, func(t *testing.T) {
			file := nativeDropFixture(t, "png")
			s, contact := attachmentService(t, nil)
			if err := s.SetAttachmentInputContact(contact.ID); err != nil {
				t.Fatal(err)
			}
			if err := StageNativeDrop(s, []string{file}); err != nil {
				t.Fatal(err)
			}
			old, err := s.attachmentLease(contact.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "session-replacement":
				// Security resume uses this same stop/start boundary without moving
				// the contact or encrypted drafts to another profile.
				s.mu.Lock()
				s.startAttachmentsLocked()
				s.mu.Unlock()
			case "lock":
				if status := s.LockStorage(); status.StorageState != "locked" {
					t.Fatal("fixture lock failed")
				}
			case "forget":
				if _, err := s.ForgetContact(contact.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := old.begin(); err == nil {
				old.end()
				t.Fatal("retired native input lease remained usable")
			}
			if err := StageNativeDrop(s, []string{file}); err == nil {
				t.Fatal("native drop retained retired input selection")
			}
			if action == "forget" {
				if err := s.SetAttachmentInputContact(contact.ID); err == nil {
					t.Fatal("forgotten ID rebound native input")
				}
				if _, err := s.ChooseAttachment(contact.ID, false); err == nil {
					t.Fatal("forgotten ID retained picker authority")
				}
				return
			}
			if action == "lock" {
				if err := s.SetAttachmentInputContact(contact.ID); err == nil {
					t.Fatal("locked contact rebound native input")
				}
				if _, err := s.UnlockStorage(); err != nil {
					t.Fatal(err)
				}
			}
			assertNativeDraftOnly(t, s, contact.ID, 1)
			if err := StageNativeDrop(s, []string{file}); err == nil {
				t.Fatal("draft reload restored native selection implicitly")
			}
			if err := s.SetAttachmentInputContact(contact.ID); err != nil {
				t.Fatal(err)
			}
			if err := StageNativeDrop(s, []string{file}); err != nil {
				t.Fatal("explicit rebind failed after runtime replacement", err)
			}
			assertNativeDraftOnly(t, s, contact.ID, 2)
		})
	}
}
