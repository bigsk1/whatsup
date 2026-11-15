package desktop

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"whatsup/internal/transfer"
)

// These synthetic container headers exercise classification and authority, not
// codec playback. No user media or external file is needed for these fixtures.
func attachmentMP4Header() []byte {
	data := binary.BigEndian.AppendUint32(nil, 24)
	data = append(data, "ftypisom"...)
	data = append(data, 0, 0, 0, 0)
	return append(data, "isommp42"...)
}

func TestCompletedAttachmentVideoUsesVerifiedTypeAcrossHistoryAndReopen(t *testing.T) {
	for _, test := range []struct {
		name, advertised, verified string
		body                       []byte
		playable                   bool
	}{
		{"mp4", "video/mp4", "video/mp4", attachmentMP4Header(), true},
		{"webm", "video/webm", "video/webm", []byte("\x1a\x45\xdf\xa3\x87\x42\x82\x84webm"), true},
		{"mp4-advertised-as-document", "application/pdf", "video/mp4", attachmentMP4Header(), true},
		{"document-advertised-as-mp4", "video/mp4", "application/pdf", []byte("%PDF-1.4 synthetic document"), false},
		{"html-advertised-as-mp4", "video/mp4", "text/html", []byte("<html>synthetic active content</html>"), false},
		{"explicit-file-mp4", "application/octet-stream", "application/octet-stream", attachmentMP4Header(), false},
	} {
		for _, direction := range []string{transfer.Incoming, transfer.Outgoing} {
			t.Run(test.name+"/"+direction, func(t *testing.T) {
				s, contact := attachmentService(t, nil)
				scope, err := s.application.AttachmentStore(contact.ID)
				if err != nil {
					t.Fatal(err)
				}
				id, err := transferID()
				if err != nil {
					t.Fatal(err)
				}
				offer := transfer.Offer{ID: id, Name: "synthetic.mp4", MediaType: test.advertised, Size: uint64(len(test.body)), SHA256: sha256.Sum256(test.body)}
				if direction == transfer.Incoming {
					if err := scope.Offer(offer); err != nil {
						t.Fatal(err)
					}
					if _, err := s.PreviewAttachment(contact.ID, id); err == nil {
						t.Fatal("unapproved offer gained preview authority")
					}
					if _, err := scope.Accept(id); err != nil {
						t.Fatal(err)
					}
					if _, err := scope.Append(id, 0, test.body); err != nil {
						t.Fatal(err)
					}
					if _, err := s.PreviewAttachment(contact.ID, id); err == nil {
						t.Fatal("incomplete video gained preview authority")
					}
					if err := scope.Finalize(t.Context(), id); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := scope.Stage(t.Context(), offer, bytes.NewReader(test.body)); err != nil {
						t.Fatal(err)
					}
					if _, err := s.PreviewAttachment(contact.ID, id); err == nil {
						t.Fatal("unsent video draft gained preview authority")
					}
					if err := scope.QueueOutgoing(id, false); err != nil {
						t.Fatal(err)
					}
					if err := scope.MarkDelivered(id); err != nil {
						t.Fatal(err)
					}
				}
				if mime, err := scope.VerifiedMediaType(id); err != nil || mime != test.verified {
					t.Fatalf("verified MIME=%q error=%v, want %q", mime, err, test.verified)
				}
				for _, reopened := range []bool{false, true} {
					if reopened {
						if status := s.LockStorage(); status.StorageState != "locked" {
							t.Fatal("could not lock fixture")
						}
						if _, err := s.UnlockStorage(); err != nil {
							t.Fatal(err)
						}
					}
					list, listErr := s.GetAttachments(contact.ID)
					history, historyErr := s.GetAttachmentHistory(contact.ID, "9223372036854775807")
					for name, result := range map[string]Attachments{"list": list, "history": history} {
						if listErr != nil || historyErr != nil || len(result.Items) != 1 {
							t.Fatalf("%s reopened=%v: list=%v history=%v items=%d", name, reopened, listErr, historyErr, len(result.Items))
						}
						view := result.Items[0]
						if view.ID != id || view.State != transfer.Complete || view.MediaType != test.verified || view.PreviewURL != "" {
							t.Fatalf("%s reopened=%v exposed unverified type or eager preview", name, reopened)
						}
					}
					url, err := s.PreviewAttachment(contact.ID, id)
					if test.playable {
						if err != nil || url == "" {
							t.Fatal("completed verified video lost explicit preview", err)
						}
						response := httptest.NewRecorder()
						if !ServeAttachment(s, response, httptest.NewRequest(http.MethodGet, url, nil)) || response.Code != http.StatusOK || response.Header().Get("Content-Type") != test.verified || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" || !bytes.Equal(response.Body.Bytes(), test.body) {
							t.Fatal("verified video handler changed bytes or protection headers")
						}
					} else if err == nil || url != "" {
						t.Fatal("file-mode or mislabeled non-video gained preview authority")
					}
				}
			})
		}
	}
}
