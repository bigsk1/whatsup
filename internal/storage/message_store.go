package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"whatsup/internal/message"
	"whatsup/internal/messagestore"
)

var migrationBackupName = regexp.MustCompile(`^pre-migration-[0-9a-f]{32}\.vault$`)

func randomStoreID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(id[:]), nil
}

// The authenticated legacy payload remains in the pending envelope until every
// imported record has been reopened and verified. No application authority is
// returned while this migration is pending.
func (a *ApplicationState) prepareMessageMigrationLocked() error {
	for _, name := range []string{"messages.sqlite", "messages.sqlite-wal", "messages.sqlite-shm", "messages.sqlite-journal"} {
		if _, err := os.Lstat(filepath.Join(a.vault.directory, name)); !os.IsNotExist(err) {
			return ErrState
		}
	}
	a.state.MessageStoreState = "pending"
	a.state.DeleteScopes = []string{}
	for index := range a.state.Contacts {
		contact := &a.state.Contacts[index]
		contact.StoreID = contact.ContactID
		if contact.StoreID == "" {
			id, err := randomStoreID()
			if err != nil {
				return err
			}
			contact.StoreID = id
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := "pre-migration-" + hex.EncodeToString(random[:]) + ".vault"
	source, err := os.Open(filepath.Join(a.vault.directory, "state.vault"))
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(filepath.Join(a.vault.directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	// Only authenticated ciphertext is copied. An incomplete backup is retained
	// for inspection and never selected as a completed migration backup.
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, digest), io.LimitReader(source, 2*MaxStateBytes+8193))
	if copyErr == nil && (written == 0 || written > 2*MaxStateBytes+8192) {
		copyErr = ErrState
	}
	if copyErr == nil {
		copyErr = destination.Sync()
	}
	closeErr := destination.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if err := syncDirectory(a.vault.directory); err != nil {
		return err
	}
	a.state.MigrationBackup = name
	a.state.MigrationBackupHash = hex.EncodeToString(digest.Sum(nil))
	return nil
}

func (a *ApplicationState) openMessageStoreLocked() error {
	if a.state.MigrationBackup != "" && !migrationBackupName.MatchString(a.state.MigrationBackup) {
		return ErrState
	}
	if a.state.MessageStoreState == "pending" && a.state.MigrationBackup != "" {
		filename := filepath.Join(a.vault.directory, a.state.MigrationBackup)
		if err := regular(filename); err != nil {
			return err
		}
		f, err := os.Open(filename)
		if err != nil {
			return ErrState
		}
		digest := sha256.New()
		size, readErr := io.Copy(digest, io.LimitReader(f, 2*MaxStateBytes+8193))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || size == 0 || size > 2*MaxStateBytes+8192 || hex.EncodeToString(digest.Sum(nil)) != a.state.MigrationBackupHash {
			return ErrState
		}
	}
	if a.state.MessageStoreState == "ready" {
		if err := regular(filepath.Join(a.vault.directory, "messages.sqlite")); err != nil {
			return err
		}
		if _, err := os.Lstat(filepath.Join(a.vault.directory, "messages.sqlite")); err != nil {
			return ErrState
		}
	}
	key, err := a.vault.deriveKeyLocked("message-store/v1")
	if err != nil {
		return err
	}
	defer clear(key)
	open := func() error {
		db, err := messagestore.Open(a.vault.directory, key, messagestore.Options{})
		if err == nil {
			a.messages = db
		}
		return err
	}
	if err := open(); err != nil {
		return err
	}
	if a.state.MessageStoreState == "pending" {
		for _, contact := range a.state.Contacts {
			var legacy storedChat
			if len(contact.Chat) != 0 && decodeCanonical(contact.Chat, &legacy) != nil {
				return ErrState
			}
			items := make([]message.Message, len(legacy.Messages))
			for index, item := range legacy.Messages {
				items[index] = message.Message{ID: item.ID, Direction: item.Direction, Text: item.Text, CreatedAt: item.CreatedAt, Delivered: item.Delivered, Sequence: uint64(index + 1)}
			}
			if err := a.messages.ImportLegacy(contact.StoreID, items); err != nil {
				return err
			}
		}
		if err := a.closeMessageStoreLocked(); err != nil {
			return err
		}
		if err := open(); err != nil {
			return err
		}
		for _, contact := range a.state.Contacts {
			var legacy storedChat
			if len(contact.Chat) != 0 && decodeCanonical(contact.Chat, &legacy) != nil {
				return ErrState
			}
			// Import's existing-scope branch verifies exact row count, sequence,
			// pending/hidden state and content without modifying an imported row.
			// Re-run it after reopen so extra valid rows cannot escape verification.
			expected := make([]message.Message, len(legacy.Messages))
			for index, item := range legacy.Messages {
				expected[index] = message.Message{ID: item.ID, Direction: item.Direction, Text: item.Text, CreatedAt: item.CreatedAt, Delivered: item.Delivered, Sequence: uint64(index + 1)}
			}
			if err := a.messages.ImportLegacy(contact.StoreID, expected); err != nil {
				return err
			}
			scope, err := a.messages.Scope(contact.StoreID)
			if err != nil {
				return err
			}
			for index, old := range legacy.Messages {
				got, err := scope.Get(old.ID)
				if err != nil || got.ID != old.ID || got.Text != old.Text || got.Direction != old.Direction || !got.CreatedAt.Equal(old.CreatedAt) || got.Delivered != old.Delivered || got.Sequence != uint64(index+1) {
					return ErrState
				}
			}
			pending, err := scope.PendingIDs()
			if err != nil || len(pending) != len(legacy.Outbox) {
				return ErrState
			}
			ids := make(map[string]bool, len(pending))
			for _, id := range pending {
				ids[id] = true
			}
			for _, attempt := range legacy.Outbox {
				if !ids[attempt.ID] {
					return ErrState
				}
			}
		}
		next := a.cloneLocked()
		for index := range next.Contacts {
			next.Contacts[index].Chat = nil
		}
		next.MessageStoreState = "ready"
		if err := a.commitLocked(next); err != nil {
			return err
		}
	}
	return a.finishMessageDeletesLocked()
}

func (a *ApplicationState) closeMessageStoreLocked() error {
	if a.messages == nil {
		return nil
	}
	db := a.messages
	a.messages = nil
	return db.Close()
}

// A deletion intent is saved with removal of its contact approval. If SQLite
// deletion is interrupted, the orphaned records have no authority and cleanup
// resumes before the next unlocked application is made available.
func (a *ApplicationState) finishMessageDeletesLocked() error {
	if len(a.state.DeleteScopes) == 0 {
		return nil
	}
	for _, id := range a.state.DeleteScopes {
		if err := a.messages.DropContact(id); err != nil {
			a.failed = true
			return err
		}
	}
	next := a.cloneLocked()
	next.DeleteScopes = []string{}
	return a.commitLocked(next)
}

func (s *ChatStore) Claim() error {
	a := s.application
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if _, err := s.scopeLocked(); err != nil {
		return err
	}
	if s.loaded {
		return ErrScopeUsed
	}
	s.loaded = true
	return nil
}

func (s *ChatStore) scopeLocked() (*messagestore.Scope, error) {
	a := s.application
	if err := a.usableLocked(); err != nil {
		return nil, err
	}
	index := a.findContactLocked(s.local, s.peer)
	if index < 0 || a.messages == nil || a.state.MessageStoreState != "ready" || a.state.Contacts[index].ContactID != s.contactID || a.state.Contacts[index].StoreID != s.storeID {
		return nil, ErrContact
	}
	if a.state.Contacts[index].generation != s.generation {
		return nil, ErrStateConflict
	}
	if s.repository == nil {
		return nil, ErrContact
	}
	return s.repository, nil
}

func (s *ChatStore) withScope(operation func(*messagestore.Scope) error) error {
	a := s.application
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	scope, err := s.scopeLocked()
	if err != nil {
		return err
	}
	err = operation(scope)
	if errors.Is(err, message.ErrStore) || errors.Is(err, message.ErrCorrupt) {
		a.failed = true
	}
	return err
}
func (s *ChatStore) Queue(item message.Message) error {
	return s.withScope(func(scope *messagestore.Scope) error { return scope.Queue(item) })
}
func (s *ChatStore) Receive(item message.Message) (duplicate bool, err error) {
	err = s.withScope(func(scope *messagestore.Scope) error { var e error; duplicate, e = scope.Receive(item); return e })
	return
}
func (s *ChatStore) Acknowledge(id string) error {
	return s.withScope(func(scope *messagestore.Scope) error { return scope.Acknowledge(id) })
}
func (s *ChatStore) PendingIDs() (ids []string, err error) {
	err = s.withScope(func(scope *messagestore.Scope) error { var e error; ids, e = scope.PendingIDs(); return e })
	return
}
func (s *ChatStore) Get(id string) (item message.Message, err error) {
	err = s.withScope(func(scope *messagestore.Scope) error { var e error; item, e = scope.Get(id); return e })
	return
}
func (s *ChatStore) Page(before uint64, limit int) (items []message.Message, err error) {
	err = s.withScope(func(scope *messagestore.Scope) error { var e error; items, e = scope.Page(before, limit); return e })
	return
}
func (s *ChatStore) Usage() (usage message.Usage, err error) {
	err = s.withScope(func(scope *messagestore.Scope) error { var e error; usage, e = scope.Usage(); return e })
	return
}
func (s *ChatStore) DeleteLocal(id string) error {
	return s.withScope(func(scope *messagestore.Scope) error { return scope.DeleteLocal(id) })
}
func (s *ChatStore) ClearHistory() error {
	return s.withScope(func(scope *messagestore.Scope) error { return scope.ClearHistory() })
}
