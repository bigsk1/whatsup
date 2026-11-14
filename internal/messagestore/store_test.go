package messagestore

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"whatsup/internal/message"
)

func testID(index uint64) string {
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[8:], index)
	return base64.RawURLEncoding.EncodeToString(raw[:])
}
func testMessage(index uint64, text string) message.Message {
	return message.Message{ID: testID(index), Direction: message.Outgoing, Text: text, CreatedAt: time.Date(2026, 11, 14, 1, 2, 3, 0, time.UTC)}
}
func fixture(t *testing.T, options Options) (*DB, *Scope, []byte, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	key := bytes.Repeat([]byte{41}, 32)
	db, err := Open(dir, key, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	scope, err := db.Scope(testID(900))
	if err != nil {
		t.Fatal(err)
	}
	return db, scope, key, dir
}

func reopen(t *testing.T, db *DB, key []byte, dir string, options Options) (*DB, *Scope) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(dir, key, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	scope, err := next.Scope(testID(900))
	if err != nil {
		t.Fatal(err)
	}
	return next, scope
}

func TestQueueReceiveACKAndEncryptedRestart(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	out := testMessage(1, "synthetic outgoing private body")
	if err := s.Queue(out); err != nil {
		t.Fatal(err)
	}
	ids, err := s.PendingIDs()
	if err != nil || len(ids) != 1 || ids[0] != out.ID {
		t.Fatal("queue/outbox was not atomic")
	}
	in := testMessage(2, "synthetic received private body")
	dup, err := s.Receive(in)
	if err != nil || dup {
		t.Fatal("new receive failed")
	}
	in.CreatedAt = in.CreatedAt.Add(time.Hour)
	dup, err = s.Receive(in)
	if err != nil || !dup {
		t.Fatal("lost ACK retry inserted duplicate or compared arrival time")
	}
	in.Text = "changed authenticated content"
	if _, err = s.Receive(in); !errors.Is(err, ErrConflict) {
		t.Fatal("message ID substituted different content")
	}
	db, s = reopen(t, db, key, dir, Options{})
	page, err := s.Page(0, 10)
	if err != nil || len(page) != 2 || page[0].Direction != message.Outgoing || page[1].Direction != message.Incoming || !page[1].Delivered {
		t.Fatal("encrypted reopen lost delivery shape")
	}
	if err = s.Acknowledge(out.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Acknowledge(out.ID); err != nil {
		t.Fatal("ACK not idempotent")
	}
	db, s = reopen(t, db, key, dir, Options{})
	ids, err = s.PendingIDs()
	if err != nil || len(ids) != 0 {
		t.Fatal("ACK did not atomically remove pending state")
	}
	got, err := s.Get(out.ID)
	if err != nil || !got.Delivered {
		t.Fatal("delivered history lost")
	}
	if err = s.Acknowledge(testID(1234)); !errors.Is(err, ErrAck) {
		t.Fatal("unknown ACK accepted")
	}
	if err = s.Acknowledge(testID(2)); !errors.Is(err, ErrAck) {
		t.Fatal("incoming text acknowledged as outgoing")
	}
}

func TestQuotaIsPerContactAndDoesNotEvictOrLoseOutbox(t *testing.T) {
	_, s, _, _ := fixture(t, Options{QuotaBytes: 1500})
	queued := []message.Message{}
	for i := uint64(1); i < 20; i++ {
		m := testMessage(i, strings.Repeat("x", 200))
		err := s.Queue(m)
		if errors.Is(err, ErrCapacity) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, m)
	}
	if len(queued) == 0 || len(queued) >= 19 {
		t.Fatal("fixture did not enforce quota")
	}
	usage, err := s.Usage()
	if err != nil || usage.UsedBytes > usage.QuotaBytes {
		t.Fatal("quota grew beyond allowed ciphertext budget")
	}
	ids, err := s.PendingIDs()
	if err != nil || len(ids) != len(queued) {
		t.Fatal("quota failure lost pending text")
	}
	other, err := s.db.Scope(testID(901))
	if err != nil {
		t.Fatal(err)
	}
	if err = other.Queue(testMessage(500, "different contact budget")); err != nil {
		t.Fatal("one full contact poisoned another")
	}
	if err = s.DeleteLocal(queued[0].ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(queued[0].ID); err != nil || got.Text != queued[0].Text {
		t.Fatal("local delete removed unacknowledged payload")
	}
	if err = s.Acknowledge(queued[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(queued[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted acknowledged text remained readable")
	}
}

func TestQuotaFullMeansEvenMinimumRecordCannotFit(t *testing.T) {
	_, s, _, _ := fixture(t, Options{QuotaBytes: minimumRecordCharge - 1})
	usage, err := s.Usage()
	if err != nil || !usage.Full {
		t.Fatal("tiny remaining budget was not reported full")
	}
	if err = s.Queue(testMessage(1, "x")); !errors.Is(err, ErrCapacity) {
		t.Fatal("minimum message exceeded declared quota")
	}
}

func TestClearHistoryPreservesPendingAndIncomingDeduplication(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	in := testMessage(1, "synthetic incoming tombstone")
	if _, err := s.Receive(in); err != nil {
		t.Fatal(err)
	}
	out := testMessage(2, "synthetic pending retained")
	if err := s.Queue(out); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	page, err := s.Page(0, 10)
	if err != nil || len(page) != 0 {
		t.Fatal("clear retained visible history")
	}
	db, s = reopen(t, db, key, dir, Options{})
	ids, err := s.PendingIDs()
	if err != nil || len(ids) != 1 || ids[0] != out.ID {
		t.Fatal("clear discarded pending queue")
	}
	if got, err := s.Get(out.ID); err != nil || got.Text != out.Text {
		t.Fatal("pending local delete lost text needed for retry")
	}
	in.CreatedAt = in.CreatedAt.Add(2 * time.Hour)
	if duplicate, err := s.Receive(in); err != nil || !duplicate {
		t.Fatal("deleted incoming retry resurrected message")
	}
	in.Text = "different text under tombstoned ID"
	if _, err := s.Receive(in); !errors.Is(err, ErrConflict) {
		t.Fatal("tombstone failed ID/content binding")
	}
	if err = s.Acknowledge(out.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Acknowledge(out.ID); err != nil {
		t.Fatal("repeated hidden ACK was not idempotent")
	}
	if _, err = s.Get(out.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("acknowledged hidden text remained readable")
	}
}

func TestLegacyImportIsExactAtomicAndKeepsCanonicalZeroMessageID(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	input := []message.Message{testMessage(0, "synthetic zero legacy ID"), testMessage(1, "synthetic delivered legacy body")}
	input[1].Delivered = true
	if err := db.ImportLegacy(testID(900), input); err != nil {
		t.Fatal(err)
	}
	if err := db.ImportLegacy(testID(900), input); err != nil {
		t.Fatal("exact repeated migration was not idempotent")
	}
	db, s = reopen(t, db, key, dir, Options{})
	page, err := s.Page(0, 10)
	if err != nil || len(page) != 2 || page[0].ID != input[0].ID {
		t.Fatal("legacy sequence/content changed")
	}
	input[0].Text = "changed after migration"
	if err = db.ImportLegacy(testID(900), input); !errors.Is(err, ErrCorrupt) {
		t.Fatal("migration retry replaced committed content")
	}
	if err = db.DropContact(testID(900)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Page(0, 10); !errors.Is(err, ErrClosed) {
		t.Fatal("forgotten scope retained authority")
	}
	fresh, err := db.Scope(testID(900))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PendingIDs(); !errors.Is(err, ErrClosed) {
		t.Fatal("stale scope revived after same-ID recreation")
	}
	page, err = fresh.Page(0, 10)
	if err != nil || len(page) != 0 {
		t.Fatal("fresh contact retained forgotten messages")
	}
}

func TestInterruptedTransactionKeepsQueueAndHistoryUnchanged(t *testing.T) {
	armed := false
	db, s, key, dir := fixture(t, Options{BeforeCommit: func() error {
		if armed {
			return errors.New("synthetic interrupted commit")
		}
		return nil
	}})
	if err := s.Queue(testMessage(1, "synthetic committed before fault")); err != nil {
		t.Fatal(err)
	}
	armed = true
	if err := s.Queue(testMessage(2, "synthetic rolled back candidate")); !errors.Is(err, ErrStore) {
		t.Fatal("transaction interruption was not surfaced")
	}
	db, s = reopen(t, db, key, dir, Options{})
	page, err := s.Page(0, 10)
	if err != nil || len(page) != 1 || page[0].ID != testID(1) {
		t.Fatal("interrupted transaction partly committed")
	}
	ids, err := s.PendingIDs()
	if err != nil || len(ids) != 1 {
		t.Fatal("message and outbox did not roll back together")
	}
}

func TestSQLiteFullRollsBackWithoutFalseDelivery(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	if err := s.Queue(testMessage(1, "synthetic prior durable text")); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := db.conn.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(t.Context(), fmt.Sprintf("PRAGMA max_page_count=%d", pages)); err != nil {
		t.Fatal(err)
	}
	err := s.Queue(testMessage(2, strings.Repeat("synthetic-large-body-", 750)))
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("SQLite disk/page-full was not surfaced as capacity: %v", err)
	}
	db, s = reopen(t, db, key, dir, Options{})
	page, err := s.Page(0, 10)
	if err != nil || len(page) != 1 || page[0].Delivered {
		t.Fatal("full disk partially committed or acknowledged text")
	}
}

func external(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, Filename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestTamperedSwappedAndDeletedRowsFailAuthentication(t *testing.T) {
	for _, kind := range []string{"body", "ID", "sequence", "contact", "cipher-swap", "cross-purpose", "oversize", "missing-message", "missing-contact", "missing-root"} {
		t.Run(kind, func(t *testing.T) {
			db, s, key, dir := fixture(t, Options{})
			if err := s.Queue(testMessage(1, "synthetic authenticated message body")); err != nil {
				t.Fatal(err)
			}
			if err := s.Queue(testMessage(2, "synthetic second authenticated message")); err != nil {
				t.Fatal(err)
			}
			x := external(t, dir)
			var err error
			switch kind {
			case "body":
				_, err = x.Exec("UPDATE records SET cipher=zeroblob(length(cipher)) WHERE contact_id=? AND sequence=1", testID(900))
			case "ID":
				_, err = x.Exec("UPDATE records SET message_id=? WHERE contact_id=? AND sequence=1", testID(44), testID(900))
			case "sequence":
				_, err = x.Exec("UPDATE records SET sequence=55 WHERE contact_id=? AND sequence=1", testID(900))
			case "contact":
				_, err = x.Exec("UPDATE records SET contact_id=? WHERE contact_id=? AND sequence=1", testID(901), testID(900))
			case "cipher-swap":
				var first, second []byte
				if err = x.QueryRow("SELECT cipher FROM records WHERE contact_id=? AND sequence=1", testID(900)).Scan(&first); err != nil {
					t.Fatal(err)
				}
				if err = x.QueryRow("SELECT cipher FROM records WHERE contact_id=? AND sequence=2", testID(900)).Scan(&second); err != nil {
					t.Fatal(err)
				}
				if _, err = x.Exec("UPDATE records SET cipher=? WHERE contact_id=? AND sequence=1", second, testID(900)); err == nil {
					_, err = x.Exec("UPDATE records SET cipher=? WHERE contact_id=? AND sequence=2", first, testID(900))
				}
			case "cross-purpose":
				_, err = x.Exec("UPDATE store SET cipher=(SELECT cipher FROM records WHERE contact_id=? AND sequence=1)", testID(900))
			case "oversize":
				_, err = x.Exec("UPDATE records SET cipher=zeroblob(1048576) WHERE contact_id=? AND sequence=1", testID(900))
			case "missing-message":
				_, err = x.Exec("DELETE FROM records WHERE contact_id=? AND sequence=1", testID(900))
			case "missing-contact":
				_, err = x.Exec("DELETE FROM records WHERE contact_id=?", testID(900))
			case "missing-root":
				_, err = x.Exec("DELETE FROM store")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Page(0, 10); !errors.Is(err, ErrCorrupt) && !(kind == "oversize" && errors.Is(err, ErrStore)) {
				t.Fatalf("outside tamper was not detected before exposing history: %v", err)
			}
			_ = x.Close()
			_ = db.Close()
			bad, err := Open(dir, key, Options{})
			if bad != nil {
				_ = bad.Close()
			}
			if !errors.Is(err, ErrCorrupt) && !(kind == "oversize" && errors.Is(err, ErrStore)) {
				t.Fatal("reopen accepted altered authenticated database")
			}
		})
	}
}

func TestCiphertextsUseRandomNoncesAndPurposeSeparation(t *testing.T) {
	db, s, _, _ := fixture(t, Options{})
	r := record{Version: 1, Message: testMessage(1, "synthetic key purpose binding"), Pending: true, Digest: digest("synthetic key purpose binding")}
	a, err := db.seal("message", s.id, r.Message.ID, 1, r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.seal("message", s.id, r.Message.ID, 1, r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a[:24], b[:24]) {
		t.Fatal("record rewrites reused an XChaCha nonce")
	}
	for _, purpose := range []string{"metadata", "manifest", "attachments"} {
		var target record
		if db.open(purpose, s.id, r.Message.ID, 1, a, &target) == nil {
			t.Fatal("record accepted under a different purpose key")
		}
	}
	var target record
	if db.open("message", testID(901), r.Message.ID, 1, a, &target) == nil {
		t.Fatal("record accepted under another contact")
	}
}

func TestCrashChildProcess(t *testing.T) {
	dir := os.Getenv("WHATSUP_TEST_MESSAGESTORE_CRASH")
	if dir == "" {
		return
	}
	armed := false
	db, err := Open(dir, bytes.Repeat([]byte{41}, 32), Options{BeforeCommit: func() error {
		if armed {
			os.Exit(19)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := db.Scope(testID(900))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.conn.ExecContext(t.Context(), "PRAGMA cache_size=1"); err != nil {
		t.Fatal(err)
	}
	armed = true
	if err = s.Queue(testMessage(2, strings.Repeat("SYNTHETIC-CRASH-PRIVATE-BODY", 400))); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash boundary was not reached")
}

func TestProcessCrashBeforeCommitDoesNotExposePartialMessage(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	if err := s.Queue(testMessage(1, "synthetic prior committed message")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestCrashChildProcess$", "-test.v")
	child.Env = append(os.Environ(), "WHATSUP_TEST_MESSAGESTORE_CRASH="+dir)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 19 {
		t.Fatalf("crash helper did not terminate at precommit boundary: %v %s", err, output)
	}
	for _, name := range []string{Filename, Filename + "-wal", Filename + "-shm"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("SYNTHETIC-CRASH-PRIVATE-BODY")) {
			t.Fatal("crash staging left plaintext")
		}
	}
	next, err := Open(dir, key, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	s, err = next.Scope(testID(900))
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.Page(0, 10)
	if err != nil || len(page) != 1 || page[0].ID != testID(1) {
		t.Fatal("process crash partly published a message")
	}
	ids, err := s.PendingIDs()
	if err != nil || len(ids) != 1 || ids[0] != testID(1) {
		t.Fatal("process crash separated message and pending transaction")
	}
}

func TestWrongKeyLockPrivatePathsAndOrphanSidesFailClosed(t *testing.T) {
	db, _, key, dir := fixture(t, Options{})
	if _, err := Open(dir, key, Options{}); !errors.Is(err, ErrBusy) {
		t.Fatal("second process/handle acquired store")
	}
	_ = db.Close()
	if bad, err := Open(dir, bytes.Repeat([]byte{9}, 32), Options{}); err == nil || bad != nil {
		t.Fatal("wrong key opened empty store")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(filepath.Join(dir, Filename), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, key, Options{}); !errors.Is(err, ErrUnsafePath) {
			t.Fatal("public database permissions accepted")
		}
		_ = os.Chmod(filepath.Join(dir, Filename), 0600)
	}
	for _, name := range []string{Filename, Filename + "-wal", Filename + "-shm", Filename + "-journal", LockFilename} {
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "target")
			_ = os.WriteFile(target, []byte("untouched"), 0600)
			parent := filepath.Join(t.TempDir(), "private")
			_ = os.Mkdir(parent, 0700)
			if err := os.Symlink(target, filepath.Join(parent, name)); err != nil {
				t.Skip("host denies synthetic symlinks")
			}
			if _, err := Open(parent, key, Options{}); !errors.Is(err, ErrUnsafePath) {
				t.Fatal("symlinked store member accepted")
			}
		})
	}
	orphan := filepath.Join(t.TempDir(), "orphan")
	_ = os.Mkdir(orphan, 0700)
	_ = os.WriteFile(filepath.Join(orphan, Filename+"-wal"), []byte("synthetic orphan"), 0600)
	if _, err := Open(orphan, key, Options{}); !errors.Is(err, ErrCorrupt) {
		t.Fatal("orphan sidecar silently created replacement database")
	}
}

func TestScopesAndPagesRemainBoundedWithoutAcceptingZeroContactID(t *testing.T) {
	db, s, _, _ := fixture(t, Options{})
	if _, err := db.Scope(testID(0)); !errors.Is(err, ErrCorrupt) {
		t.Fatal("zero contact identity accepted")
	}
	for index := uint64(1); index < MaxContacts; index++ {
		if _, err := db.Scope(testID(900 + index)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Scope(testID(9999)); !errors.Is(err, ErrCapacity) {
		t.Fatal("contact scope limit exceeded")
	}
	for _, limit := range []int{-1, 0, MaxPageMessages + 1} {
		if _, err := s.Page(0, limit); err == nil {
			t.Fatal("unbounded page accepted")
		}
	}
	for index := uint64(1); index <= 80; index++ {
		if err := s.Queue(testMessage(index, strings.Repeat("x", MaxTextBytes))); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Page(0, MaxPageMessages)
	if err != nil || len(page) != MaxPageTextBytes/MaxTextBytes {
		t.Fatal("page exceeded text-byte budget")
	}
}

func TestFormattingAndZeroValuesDoNotExposeKeysOrManufactureAuthority(t *testing.T) {
	db, s, _, _ := fixture(t, Options{})
	for _, value := range []any{db, *db, s, *s} {
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("private repository serialized")
		}
		for _, verb := range []string{"%v", "%+v", "%#v"} {
			text := fmt.Sprintf(verb, value)
			if text != "[private encrypted WhatsUp message store]" && text != "[private approved message scope]" {
				t.Fatal("formatting exposed private repository fields")
			}
		}
	}
	var zero DB
	if _, err := zero.Scope(testID(3)); !errors.Is(err, ErrClosed) {
		t.Fatal("zero-value store manufactured a scope")
	}
	if err := zero.Close(); err != nil {
		t.Fatal(err)
	}
	var scope Scope
	if _, err := scope.Page(0, 10); !errors.Is(err, ErrClosed) {
		t.Fatal("zero scope exposed messages")
	}
}

func TestDetachedOrReplacedFilesCannotReceiveAcknowledgement(t *testing.T) {
	for _, name := range []string{Filename, LockFilename} {
		t.Run(name, func(t *testing.T) {
			_, s, _, dir := fixture(t, Options{})
			if err := s.Queue(testMessage(1, "synthetic pre-replacement durable text")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name)
			if err := os.Rename(path, path+".detached"); err != nil {
				if runtime.GOOS == "windows" {
					t.Skip("Windows prevents renaming active repository file")
				}
				t.Fatal(err)
			}
			if _, err := s.Receive(testMessage(2, "must never ACK detached storage")); !errors.Is(err, ErrUnsafePath) {
				t.Fatal("missing live database/lock pathname remained usable")
			}
			if err := os.WriteFile(path, []byte{}, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PendingIDs(); !errors.Is(err, ErrUnsafePath) {
				t.Fatal("replacement file retained old writer authority")
			}
		})
	}
}

func TestSQLiteFilesNeverContainPlaintextAndPragmasAreVerified(t *testing.T) {
	db, s, _, dir := fixture(t, Options{})
	secret := "SYNTHETIC-PRIVATE-MESSAGE-NEVER-IN-SQLITE-FILES"
	if err := s.Queue(testMessage(1, secret)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{Filename, Filename + "-wal", Filename + "-shm", Filename + "-journal"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{secret, "createdAt", "direction", "delivered", "pending", "2026-11-14"} {
			if bytes.Contains(data, []byte(needle)) {
				t.Fatalf("plaintext %q in %s", needle, name)
			}
		}
	}
	for name, want := range map[string]int{"synchronous": 2, "secure_delete": 1, "temp_store": 2, "trusted_schema": 0} {
		var value int
		if err := db.conn.QueryRowContext(t.Context(), "PRAGMA "+name).Scan(&value); err != nil || value != want {
			t.Fatalf("pragma %s not active", name)
		}
	}
	if _, err := db.conn.ExecContext(t.Context(), "CREATE TEMP TABLE scratch(cipher BLOB); INSERT INTO scratch(cipher) SELECT cipher FROM records;"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "temp") {
			t.Fatal("temporary SQLite storage unexpectedly used disk")
		}
	}
}

func TestTenThousandMessagesHaveNoLifetimeCapAndRebuildPendingOnReopen(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	started := time.Now()
	for index := uint64(1); index <= 10001; index++ {
		if err := s.Queue(testMessage(index, fmt.Sprintf("Synthetic scalable durable message %d", index))); err != nil {
			t.Fatalf("message %d did not fit normal history: %v", index, err)
		}
	}
	t.Logf("10001 individual durable queues: %s", time.Since(started))
	db, s = reopen(t, db, key, dir, Options{})
	pending, err := s.PendingIDs()
	if err != nil || len(pending) != 10001 || pending[0] != testID(1) || pending[10000] != testID(10001) {
		t.Fatal("encrypted outbox did not reconstruct in sequence order")
	}
	var cursor uint64
	seen := 0
	for {
		page, err := s.Page(cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for index := 1; index < len(page); index++ {
			if page[index].Sequence != page[index-1].Sequence+1 {
				t.Fatal("page sequence gaps or order changed")
			}
		}
		seen += len(page)
		cursor = page[0].Sequence
	}
	if seen != 10001 {
		t.Fatalf("paged history lost messages: got %d", seen)
	}
	usage, err := s.Usage()
	if err != nil || usage.Full || usage.UsedBytes >= DefaultQuotaBytes {
		t.Fatal("small history hit a lifetime cap")
	}
	if err = s.Acknowledge(testID(1)); err != nil {
		t.Fatal(err)
	}
	if err = s.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	db, s = reopen(t, db, key, dir, Options{})
	page, err := s.Page(0, 100)
	if err != nil || len(page) != 0 {
		t.Fatal("cleared scalable history remained visible")
	}
	pending, err = s.PendingIDs()
	if err != nil || len(pending) != 10000 {
		t.Fatal("clear incorrectly discarded scalable outbox")
	}
}
