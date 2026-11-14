package chat

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"whatsup/internal/message"
	"whatsup/internal/messagestore"
)

func TestPagedConversationUnreadUsesApprovedRepositoryAuthority(t *testing.T) {
	db, err := messagestore.Open(filepath.Join(t.TempDir(), "private"), bytes.Repeat([]byte{71}, 32), messagestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	scope, err := db.Scope(recordID(900))
	if err != nil {
		t.Fatal(err)
	}
	contact := testContact(t, 1, 2)
	c := openTest(t, scope, contact, Options{})
	if _, err := c.Receive(contact.Peer(), Frame{Version: ProtocolVersion, Type: MessageFrame, ID: recordID(1), Text: "synthetic paged conversation"}); err != nil {
		t.Fatal(err)
	}
	if count, err := c.UnreadCount(); err != nil || count != 1 {
		t.Fatal("committed receive did not become unread")
	}
	page, err := c.ReadPage(0, 1)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != recordID(1) || page.HasMore || page.NextCursor != 0 {
		t.Fatal("approved paged conversation did not load its message")
	}
	if err := c.MarkRead(recordID(1)); err != nil {
		t.Fatal(err)
	}
	if count, err := c.UnreadCount(); err != nil || count != 0 {
		t.Fatal("read marker did not commit")
	}
	if err := c.MarkRead(""); !errors.Is(err, ErrFrame) {
		t.Fatal("empty message ID was accepted")
	}
	contact.Revoke()
	if _, err := c.ReadPage(0, 1); err == nil {
		t.Fatal("revoked contact read a page")
	}
	if _, err := c.UnreadCount(); err == nil {
		t.Fatal("revoked contact read unread metadata")
	}
	if err := c.MarkRead(recordID(1)); err == nil {
		t.Fatal("revoked contact changed read metadata")
	}
}

type pagedRepositoryFixture struct {
	*memoryStore
	page message.Page
}

func (s *pagedRepositoryFixture) ReadPage(uint64, int) (message.Page, error) {
	return s.page, nil
}
func (*pagedRepositoryFixture) UnreadCount() (uint64, error) { return 0, nil }
func (*pagedRepositoryFixture) MarkRead(string) error        { return nil }

func TestPagedConversationRejectsNonAdvancingAndInvalidRepositoryPages(t *testing.T) {
	valid := Message{ID: recordID(1), Direction: Incoming, Text: "synthetic page validation", CreatedAt: time.Now().UTC(), Delivered: true, Sequence: 7}
	for _, name := range []string{"non-advancing", "missing-cursor", "terminal-cursor", "before-bound", "below-cursor", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			page := message.Page{Messages: []Message{valid}}
			switch name {
			case "non-advancing":
				page.HasMore, page.NextCursor = true, 10
			case "missing-cursor":
				page.HasMore = true
			case "terminal-cursor":
				page.NextCursor = 7
			case "before-bound":
				page.Messages[0].Sequence = 10
			case "below-cursor":
				page.HasMore, page.NextCursor = true, 8
			case "duplicate":
				page.Messages = append(page.Messages, valid)
			}
			repository := &pagedRepositoryFixture{memoryStore: &memoryStore{}, page: page}
			c := openTest(t, repository, testContact(t, 1, 2), Options{})
			if _, err := c.ReadPage(10, 5); !errors.Is(err, ErrCorrupt) {
				t.Fatal("invalid repository page crossed the conversation boundary")
			}
			if _, err := c.UnreadCount(); !errors.Is(err, ErrStore) {
				t.Fatal("corrupt page did not retire the conversation")
			}
		})
	}
}

func TestPagedConversationPreservesHiddenOnlyCursor(t *testing.T) {
	repository := &pagedRepositoryFixture{memoryStore: &memoryStore{}, page: message.Page{Messages: []Message{}, HasMore: true, NextCursor: 6}}
	c := openTest(t, repository, testContact(t, 1, 2), Options{})
	page, err := c.ReadPage(10, 5)
	if err != nil || len(page.Messages) != 0 || !page.HasMore || page.NextCursor != 6 {
		t.Fatal("conversation discarded a hidden-only advancing cursor")
	}
}

func TestPagedConversationDoesNotInventOptionalRepositoryState(t *testing.T) {
	c := openTest(t, &memoryStore{}, testContact(t, 1, 2), Options{})
	if _, err := c.ReadPage(0, 10); !errors.Is(err, ErrPage) {
		t.Fatal("legacy repository was assumed to support paged reads")
	}
	if _, err := c.UnreadCount(); !errors.Is(err, ErrPage) {
		t.Fatal("legacy repository was assumed to have zero unread")
	}
	if err := c.MarkRead(recordID(1)); !errors.Is(err, ErrPage) {
		t.Fatal("legacy repository accepted a read marker")
	}
	if _, err := c.Queue("existing repository remains usable"); err != nil {
		t.Fatal("optional paging failure retired the compatible repository")
	}
}
