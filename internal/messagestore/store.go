// Package messagestore stores ciphertext-only message records in SQLite. Its
// callers must obtain contact-scoped capabilities only after local approval.
package messagestore

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"golang.org/x/crypto/chacha20poly1305"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
	"whatsup/internal/message"
)

const (
	DefaultQuotaBytes uint64 = 1 << 30
	// Accounting charges ciphertext plus a fixed per-row index/allocation reserve.
	// SQLite file/page allocation is shared; this is a per-contact logical disk
	// budget, not a claim of exact filesystem allocation or physical erasure.
	RowOverhead      uint64 = 128
	MaxTextBytes            = 16 << 10
	MaxPageMessages         = 1000
	MaxPageTextBytes        = 1 << 20
	MaxPageScanRows         = 256
	MaxPageScanBytes        = 2 << 20
	MaxContacts             = 32
	Filename                = "messages.sqlite"
	LockFilename            = "messages.lock"
)

var (
	ErrCapacity   = message.ErrCapacity
	ErrCorrupt    = message.ErrCorrupt
	ErrStore      = message.ErrStore
	ErrNotFound   = message.ErrNotFound
	ErrConflict   = message.ErrConflict
	ErrAck        = message.ErrAck
	ErrClosed     = message.ErrClosed
	ErrUnsafePath = errors.New("message storage requires private regular files")
	ErrBusy       = errors.New("message storage is open in another process")
)

type Usage = message.Usage

type Options struct {
	QuotaBytes uint64
	// BeforeCommit is a Go-only fault-injection boundary. Production leaves it
	// nil. It cannot execute SQL and is never bound to the desktop renderer.
	BeforeCommit func() error
}

type manifest struct {
	Version  int      `json:"version"`
	Marker   string   `json:"marker"`
	Contacts []string `json:"contacts"`
}

type metadata struct {
	Version              int    `json:"version"`
	Epoch                string `json:"epoch"`
	NextSequence         uint64 `json:"nextSequence"`
	Rows                 uint64 `json:"rows"`
	UsedBytes            uint64 `json:"usedBytes"`
	Pending              uint64 `json:"pending"`
	ClearThroughSequence uint64 `json:"clearThroughSequence"`
	ReadThroughSequence  uint64 `json:"readThroughSequence"`
	Unread               uint64 `json:"unread"`
	AttachmentNext       uint64 `json:"attachmentNext,omitempty"`
	AttachmentRows       uint64 `json:"attachmentRows,omitempty"`
	AttachmentUsed       uint64 `json:"attachmentUsed,omitempty"`
	AutoImages           bool   `json:"autoImages,omitempty"`
	PeerRemovalID        string `json:"peerRemovalID,omitempty"`
}

// Keep the exact Phase 1 canonical shape for authenticated on-open migration.
type metadataV1 struct {
	Version      int    `json:"version"`
	Epoch        string `json:"epoch"`
	NextSequence uint64 `json:"nextSequence"`
	Rows         uint64 `json:"rows"`
	UsedBytes    uint64 `json:"usedBytes"`
	Pending      uint64 `json:"pending"`
}

type record struct {
	Version int             `json:"version"`
	Message message.Message `json:"message"`
	Hidden  bool            `json:"hidden"`
	Pending bool            `json:"pending"`
	Digest  string          `json:"digest"`
}

type contactCache struct {
	epoch       string
	pending     map[string]uint64
	attachments map[string]bool
}

type DB struct{ *dbState }

type dbState struct {
	mu           sync.Mutex
	directory    string
	root         []byte
	sql          *sql.DB
	conn         *sql.Conn
	lock         *flock.Flock
	options      Options
	manifest     manifest
	version      int64
	contacts     map[string]*contactCache
	closed       bool
	failed       bool
	afterCommit  []func()
	databaseInfo os.FileInfo
	lockInfo     os.FileInfo
}

type Scope struct {
	db        *DB
	id, epoch string
}

func (DB) String() string                  { return "[private encrypted WhatsUp message store]" }
func (d DB) GoString() string              { return d.String() }
func (DB) MarshalJSON() ([]byte, error)    { return nil, ErrStore }
func (Scope) String() string               { return "[private approved message scope]" }
func (s Scope) GoString() string           { return s.String() }
func (Scope) MarshalJSON() ([]byte, error) { return nil, ErrStore }

func privateFile(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 || !owned(info) {
		return ErrUnsafePath
	}
	return nil
}

func (d *DB) paths() error {
	info, err := os.Lstat(d.directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 || !owned(info) {
		return ErrUnsafePath
	}
	for _, name := range []string{Filename, Filename + "-wal", Filename + "-shm", Filename + "-journal", LockFilename} {
		if err := privateFile(filepath.Join(d.directory, name)); err != nil {
			return err
		}
	}
	for name, expected := range map[string]os.FileInfo{Filename: d.databaseInfo, LockFilename: d.lockInfo} {
		if expected == nil {
			continue
		}
		current, err := os.Lstat(filepath.Join(d.directory, name))
		if err != nil || !os.SameFile(expected, current) {
			return ErrUnsafePath
		}
	}
	return nil
}

func Open(directory string, rootKey []byte, options Options) (_ *DB, result error) {
	if !filepath.IsAbs(directory) || len(rootKey) != 32 {
		return nil, ErrUnsafePath
	}
	if options.QuotaBytes == 0 {
		options.QuotaBytes = DefaultQuotaBytes
	}
	if options.QuotaBytes > math.MaxInt64 {
		return nil, ErrCapacity
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, ErrUnsafePath
	}
	d := &DB{dbState: &dbState{directory: directory, root: bytes.Clone(rootKey), options: options, contacts: make(map[string]*contactCache)}}
	defer func() {
		if result != nil {
			_ = d.Close()
		}
	}()
	if err := d.paths(); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, Filename)
	_, statErr := os.Lstat(path)
	created := os.IsNotExist(statErr)
	if statErr != nil && !created {
		return nil, ErrUnsafePath
	}
	if created {
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
				return nil, ErrCorrupt
			}
		}
	}
	d.lock = flock.New(filepath.Join(directory, LockFilename), flock.SetPermissions(0600))
	locked, err := d.lock.TryLock()
	if err != nil {
		return nil, ErrStore
	}
	if !locked {
		return nil, ErrBusy
	}
	d.lockInfo, err = os.Lstat(filepath.Join(directory, LockFilename))
	if err != nil {
		return nil, ErrUnsafePath
	}
	if err := d.paths(); err != nil {
		return nil, err
	}
	if created {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return nil, ErrStore
		}
		if err = file.Close(); err != nil {
			return nil, ErrStore
		}
	}
	d.databaseInfo, err = os.Lstat(path)
	if err != nil {
		return nil, ErrUnsafePath
	}
	params := url.Values{"_pragma": {"journal_mode(WAL)", "synchronous(FULL)", "secure_delete(ON)", "temp_store(MEMORY)", "trusted_schema(OFF)", "busy_timeout(2500)"}, "_txlock": {"immediate"}, "_defensive": {"1"}}
	dsn := "file:" + filepath.ToSlash(path) + "?" + params.Encode()
	d.sql, err = sql.Open("sqlite", dsn)
	if err != nil {
		return nil, ErrStore
	}
	d.sql.SetMaxOpenConns(1)
	d.sql.SetMaxIdleConns(1)
	d.conn, err = d.sql.Conn(context.Background())
	if err != nil {
		return nil, ErrStore
	}
	// Bound SQLite's own row/blob allocation before selecting peer-controlled
	// ciphertext from a damaged file; the AEAD decoder's bound alone is too late.
	if _, err = sqlite.Limit(d.conn, sqlite3.SQLITE_LIMIT_LENGTH, 128<<10); err != nil {
		return nil, ErrStore
	}
	for name, want := range map[string]int{"synchronous": 2, "secure_delete": 1, "temp_store": 2, "trusted_schema": 0} {
		var value int
		if err = d.conn.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&value); err != nil || value != want {
			return nil, ErrStore
		}
	}
	var mode string
	if err = d.conn.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		return nil, ErrStore
	}
	if created {
		tx, beginErr := d.conn.BeginTx(context.Background(), nil)
		if beginErr != nil {
			return nil, ErrStore
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(context.Background(), `CREATE TABLE store(cipher BLOB NOT NULL); CREATE TABLE records(contact_id TEXT NOT NULL, message_id TEXT NOT NULL, sequence INTEGER NOT NULL CHECK(sequence>=0), cipher BLOB NOT NULL, PRIMARY KEY(contact_id,message_id), UNIQUE(contact_id,sequence)) WITHOUT ROWID;`); err != nil {
			return nil, ErrStore
		}
		root := manifest{Version: 1, Marker: "WhatsUp message storage", Contacts: []string{}}
		blob, err := d.seal("manifest", "", "", 0, root)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(context.Background(), "INSERT INTO store(cipher) VALUES(?)", blob); err != nil {
			return nil, ErrStore
		}
		if tx.Commit() != nil {
			return nil, ErrStore
		}
	}
	if _, err = d.conn.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS attachments(contact_id TEXT NOT NULL,message_id TEXT NOT NULL,sequence INTEGER NOT NULL CHECK(sequence>0),cipher BLOB NOT NULL,PRIMARY KEY(contact_id,message_id),UNIQUE(contact_id,sequence)) WITHOUT ROWID;`); err != nil {
		return nil, ErrStore
	}
	if err = d.upgradeMetadata(context.Background()); err != nil {
		return nil, err
	}
	if err = d.conn.QueryRowContext(context.Background(), "PRAGMA data_version").Scan(&d.version); err != nil {
		return nil, ErrStore
	}
	if err = d.paths(); err != nil {
		return nil, err
	}
	return d, nil
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (d *DB) aead(purpose, contact string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, d.root, nil, "WhatsUp/message-store/"+purpose+"/v1/"+contact, 32)
	if err != nil {
		return nil, ErrStore
	}
	defer clear(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, ErrStore
	}
	return aead, nil
}

func aad(purpose, contact, id string, sequence uint64) []byte {
	return []byte("WhatsUp/message-store/" + purpose + "/v1\x00" + contact + "\x00" + id + "\x00" + strconv.FormatUint(sequence, 10))
}

func (d *DB) seal(purpose, contact, id string, sequence uint64, value any) ([]byte, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return nil, ErrStore
	}
	defer clear(plain)
	aead, err := d.aead(purpose, contact)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err = rand.Read(nonce); err != nil {
		return nil, ErrStore
	}
	return aead.Seal(nonce, nonce, plain, aad(purpose, contact, id, sequence)), nil
}

func (d *DB) open(purpose, contact, id string, sequence uint64, blob []byte, value any) error {
	if len(blob) < chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead || len(blob) > 128<<10 {
		return ErrCorrupt
	}
	aead, err := d.aead(purpose, contact)
	if err != nil {
		return err
	}
	plain, err := aead.Open(nil, blob[:24], blob[24:], aad(purpose, contact, id, sequence))
	if err != nil {
		return ErrCorrupt
	}
	defer clear(plain)
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.DisallowUnknownFields()
	if dec.Decode(value) != nil || dec.Decode(new(any)) != io.EOF {
		return ErrCorrupt
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(plain, canonical) {
		clear(canonical)
		return ErrCorrupt
	}
	clear(canonical)
	return nil
}

func validID(id string) bool {
	if len(id) != 22 {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(id)
	return err == nil && len(raw) == 16 && base64.RawURLEncoding.EncodeToString(raw) == id
}

func validContactID(id string) bool { return validID(id) && id != "AAAAAAAAAAAAAAAAAAAAAA" }
func randomID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", ErrStore
	}
	return base64.RawURLEncoding.EncodeToString(id[:]), nil
}
func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

func validateRecord(id string, r record) error {
	if r.Version != 1 || r.Message.ID != id || !validID(id) || r.Message.CreatedAt.IsZero() || r.Message.Direction != message.Incoming && r.Message.Direction != message.Outgoing || r.Message.Direction == message.Incoming && !r.Message.Delivered || r.Pending != (r.Message.Direction == message.Outgoing && !r.Message.Delivered) {
		return ErrCorrupt
	}
	hash, err := base64.RawStdEncoding.Strict().DecodeString(r.Digest)
	if err != nil || len(hash) != 32 {
		return ErrCorrupt
	}
	if r.Hidden && !r.Pending {
		if r.Message.Text != "" {
			return ErrCorrupt
		}
		return nil
	}
	if len(r.Message.Text) == 0 || len(r.Message.Text) > MaxTextBytes || !utf8.ValidString(r.Message.Text) || digest(r.Message.Text) != r.Digest {
		return ErrCorrupt
	}
	return nil
}

func (d *DB) loadManifest(ctx context.Context, q queryer) (manifest, error) {
	rows, err := q.QueryContext(ctx, "SELECT cipher FROM store")
	if err != nil {
		return manifest{}, ErrCorrupt
	}
	defer rows.Close()
	var blob []byte
	if !rows.Next() || rows.Scan(&blob) != nil {
		return manifest{}, ErrCorrupt
	}
	var m manifest
	if err := d.open("manifest", "", "", 0, blob, &m); err != nil {
		return manifest{}, err
	}
	if rows.Next() || rows.Err() != nil || m.Version != 1 || m.Marker != "WhatsUp message storage" || len(m.Contacts) > MaxContacts {
		return manifest{}, ErrCorrupt
	}
	for i, id := range m.Contacts {
		if !validContactID(id) || i > 0 && m.Contacts[i-1] >= id {
			return manifest{}, ErrCorrupt
		}
	}
	return m, nil
}

func (d *DB) loadMeta(ctx context.Context, q queryer, id string) (metadata, string, error) {
	var key string
	var blob []byte
	if err := q.QueryRowContext(ctx, "SELECT message_id,cipher FROM records WHERE contact_id=? AND sequence=0", id).Scan(&key, &blob); err != nil {
		return metadata{}, "", ErrCorrupt
	}
	var m metadata
	if !validID(key) {
		return metadata{}, "", ErrCorrupt
	}
	if d.open("metadata", id, key, 0, blob, &m) != nil {
		var old metadataV1
		if d.open("metadata", id, key, 0, blob, &old) != nil || old.Version != 1 {
			return metadata{}, "", ErrCorrupt
		}
		m = metadata{Version: 1, Epoch: old.Epoch, NextSequence: old.NextSequence, Rows: old.Rows, UsedBytes: old.UsedBytes, Pending: old.Pending}
		// Phase 1 had no unread state. Existing history starts already read.
		if m.NextSequence != 0 {
			m.ReadThroughSequence = m.NextSequence - 1
		}
	} else if m.Version != 2 {
		return metadata{}, "", ErrCorrupt
	}
	if m.Version != 1 && m.Version != 2 || !validID(m.Epoch) || m.NextSequence == 0 || m.NextSequence > math.MaxInt64 || m.Rows != m.NextSequence-1 || m.Pending > m.Rows || m.Unread > m.Rows || m.ClearThroughSequence > m.ReadThroughSequence || m.ReadThroughSequence >= m.NextSequence || !validAttachmentMetadata(m) || m.PeerRemovalID != "" && !validContactID(m.PeerRemovalID) {
		return metadata{}, "", ErrCorrupt
	}
	return m, key, nil
}

func (d *DB) saveMeta(ctx context.Context, q queryer, id, key string, m metadata) error {
	m.Version = 2
	blob, err := d.seal("metadata", id, key, 0, m)
	if err != nil {
		return err
	}
	result, err := q.ExecContext(ctx, "UPDATE records SET cipher=? WHERE contact_id=? AND message_id=? AND sequence=0", blob, id, key)
	if err != nil {
		return sqlError(err)
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrCorrupt
	}
	return nil
}

func (d *DB) verifyAll(ctx context.Context, q queryer) error {
	root, err := d.loadManifest(ctx, q)
	if err != nil {
		return err
	}
	var distinct int
	if err = q.QueryRowContext(ctx, "SELECT count(DISTINCT contact_id) FROM records").Scan(&distinct); err != nil || distinct != len(root.Contacts) {
		return ErrCorrupt
	}
	contacts := make(map[string]*contactCache, len(root.Contacts))
	var attachmentContacts int
	if err = q.QueryRowContext(ctx, "SELECT count(DISTINCT contact_id) FROM attachments").Scan(&attachmentContacts); err != nil {
		return ErrCorrupt
	}
	var expectedAttachmentContacts int
	for _, contact := range root.Contacts {
		m, _, err := d.loadMeta(ctx, q, contact)
		if err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, "SELECT message_id,sequence,cipher FROM records WHERE contact_id=? AND sequence>0 ORDER BY sequence", contact)
		if err != nil {
			return ErrStore
		}
		cache := &contactCache{epoch: m.Epoch, pending: make(map[string]uint64)}
		var count, used, last, unread uint64
		for rows.Next() {
			var id string
			var seq uint64
			var blob []byte
			if rows.Scan(&id, &seq, &blob) != nil || seq != last+1 {
				rows.Close()
				return ErrCorrupt
			}
			var r record
			if d.open("message", contact, id, seq, blob, &r) != nil || validateRecord(id, r) != nil {
				rows.Close()
				return ErrCorrupt
			}
			count++
			used += uint64(len(blob)) + RowOverhead
			last = seq
			if r.Pending {
				cache.pending[id] = seq
			}
			if seq <= m.ClearThroughSequence && !r.Hidden {
				rows.Close()
				return ErrCorrupt
			}
			if !r.Hidden && r.Message.Direction == message.Incoming && seq > m.ReadThroughSequence {
				unread++
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return ErrStore
		}
		rows.Close()
		if count != m.Rows || used != m.UsedBytes || last+1 != m.NextSequence || uint64(len(cache.pending)) != m.Pending || unread != m.Unread {
			return ErrCorrupt
		}
		if err := d.verifyAttachments(ctx, q, contact, m, cache); err != nil {
			return err
		}
		if m.AttachmentRows != 0 {
			expectedAttachmentContacts++
		}
		contacts[contact] = cache
	}
	if attachmentContacts != expectedAttachmentContacts {
		return ErrCorrupt
	}
	d.manifest, d.contacts = root, contacts
	return nil
}

func sqlError(err error) error {
	if err == nil {
		return nil
	}
	var sqliteError *sqlite.Error
	if errors.As(err, &sqliteError) && sqliteError.Code()&255 == 13 {
		return ErrCapacity
	}
	return ErrStore
}

func (d *DB) transaction(operation func(context.Context, *sql.Tx) error) error {
	if d == nil || d.dbState == nil {
		return ErrClosed
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrClosed
	}
	if d.failed {
		return ErrStore
	}
	if err := d.paths(); err != nil {
		return err
	}
	d.afterCommit = nil
	defer func() { d.afterCommit = nil }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return sqlError(err)
	}
	defer tx.Rollback()
	var version int64
	if err = tx.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
		return ErrStore
	}
	if version != d.version {
		if err = d.verifyAll(ctx, tx); err != nil {
			d.failed = true
			return err
		}
		d.version = version
	}
	if err = operation(ctx, tx); err != nil {
		if errors.Is(err, ErrStore) {
			d.failed = true
		}
		return err
	}
	if d.options.BeforeCommit != nil {
		if err = d.options.BeforeCommit(); err != nil {
			d.failed = true
			return ErrStore
		}
	}
	if err = tx.Commit(); err != nil {
		d.failed = true
		return errors.Join(ErrStore, sqlError(err))
	}
	for _, apply := range d.afterCommit {
		apply()
	}
	// Pending maps are updated by full verification only after outside writes or
	// by operations after a confirmed commit, never ahead of durable storage.
	return nil
}

func (d *DB) Scope(id string) (scope *Scope, result error) {
	if !validContactID(id) {
		return nil, ErrCorrupt
	}
	var epoch string
	result = d.transaction(func(ctx context.Context, tx *sql.Tx) error {
		if cache := d.contacts[id]; cache != nil {
			epoch = cache.epoch
			return nil
		}
		if len(d.manifest.Contacts) >= MaxContacts {
			return ErrCapacity
		}
		var err error
		epoch, err = randomID()
		if err != nil {
			return err
		}
		key, err := randomID()
		if err != nil {
			return err
		}
		meta := metadata{Version: 2, Epoch: epoch, NextSequence: 1}
		blob, err := d.seal("metadata", id, key, 0, meta)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO records(contact_id,message_id,sequence,cipher) VALUES(?,?,0,?)", id, key, blob); err != nil {
			return sqlError(err)
		}
		next := d.manifest
		next.Contacts = append(append([]string{}, next.Contacts...), id)
		sort.Strings(next.Contacts)
		blob, err = d.seal("manifest", "", "", 0, next)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE store SET cipher=?", blob); err != nil {
			return sqlError(err)
		}
		d.afterCommit = append(d.afterCommit, func() {
			d.manifest = next
			d.contacts[id] = &contactCache{epoch: epoch, pending: make(map[string]uint64)}
		})
		return nil
	})
	if result != nil {
		return nil, result
	}
	return &Scope{db: d, id: id, epoch: epoch}, nil
}

func (s *Scope) meta(ctx context.Context, q queryer) (metadata, string, error) {
	cache := s.db.contacts[s.id]
	if cache == nil || cache.epoch != s.epoch {
		return metadata{}, "", ErrClosed
	}
	m, key, err := s.db.loadMeta(ctx, q, s.id)
	if err != nil {
		return metadata{}, "", err
	}
	if m.Epoch != s.epoch {
		return metadata{}, "", ErrClosed
	}
	return m, key, nil
}

func (s *Scope) load(ctx context.Context, q queryer, id string) (record, []byte, error) {
	var seq uint64
	var blob []byte
	if !validID(id) {
		return record{}, nil, ErrNotFound
	}
	err := q.QueryRowContext(ctx, "SELECT sequence,cipher FROM records WHERE contact_id=? AND message_id=? AND sequence>0", s.id, id).Scan(&seq, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return record{}, nil, ErrNotFound
	}
	if err != nil {
		return record{}, nil, ErrStore
	}
	var r record
	if s.db.open("message", s.id, id, seq, blob, &r) != nil || validateRecord(id, r) != nil {
		return record{}, nil, ErrCorrupt
	}
	r.Message.Sequence = seq
	return r, blob, nil
}

func (d *DB) Close() error {
	if d == nil || d.dbState == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	var result error
	if d.conn != nil {
		if _, err := d.conn.ExecContext(context.Background(), "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			result = ErrStore
		}
		if d.conn.Close() != nil {
			result = ErrStore
		}
	}
	if d.sql != nil && d.sql.Close() != nil {
		result = ErrStore
	}
	if d.lock != nil && d.lock.Close() != nil {
		result = ErrStore
	}
	clear(d.root)
	d.root = nil
	d.contacts = nil
	return result
}
