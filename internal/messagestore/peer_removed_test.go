package messagestore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerRemovalIsEncryptedDurableTerminalAndSurvivesClear(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	if err := s.Queue(testMessage(1, "synthetic preserved peer-removed history")); err != nil {
		t.Fatal(err)
	}
	other, err := db.Scope(testID(901))
	if err != nil {
		t.Fatal(err)
	}
	id := testID(17171)
	if err = s.MarkPeerRemoved(id); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkPeerRemoved(testID(17172)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PeerRemoval(); err != nil || got != id {
		t.Fatal("terminal marker changed", err)
	}
	if got, err := other.PeerRemoval(); err != nil || got != "" {
		t.Fatal("marker escaped contact", err)
	}
	if err = s.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	db, s = reopen(t, db, key, dir, Options{})
	if got, err := s.PeerRemoval(); err != nil || got != id {
		t.Fatal("clear or restart lost marker", err)
	}
	if err = s.MarkPeerRemoved("AAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Fatal("zero receipt ID accepted")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "messages.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(id)) || bytes.Contains(data, []byte("peerRemovalID")) {
		t.Fatal("removal metadata was stored as plaintext")
	}
}

func TestPeerRemovalInterruptedTransactionDoesNotPersistMarker(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	db.options.BeforeCommit = func() error { return errors.New("synthetic interrupted removal transaction") }
	if err := s.MarkPeerRemoved(testID(31)); err == nil {
		t.Fatal("interrupted removal claimed durable success")
	}
	// An uncertain writer is retired; reopen authenticates the last committed state.
	db, s = reopen(t, db, key, dir, Options{})
	if id, err := s.PeerRemoval(); err != nil || id != "" {
		t.Fatal("interrupted removal left a terminal marker", err)
	}
}
