// Package recovery encodes portable, passphrase-protected application snapshots.
// It owns no paths, credentials or installation authority. Import callbacks may
// write only uninstalled staging data until Read returns a valid receipt.
package recovery

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
)

type Kind uint8

const (
	Master Kind = iota + 1
	Application
	Database
	Object
)

const (
	MaxEntries          uint32 = 32*(4<<30)/4096 + 3
	MaxTotalBytes       uint64 = 256 << 30
	MaxObjectBytes      uint64 = 4 << 30
	MaxApplicationBytes uint32 = 4 << 20
)

var (
	ErrSecret         = errors.New("Recovery passphrase must contain 12 to 1024 valid UTF-8 bytes")
	ErrFormat         = errors.New("Recovery archive is invalid or unsupported")
	ErrAuthentication = errors.New("Recovery passphrase is incorrect or the archive was changed")
	ErrLimit          = errors.New("Recovery archive exceeds its safety limits")
	ErrIO             = errors.New("Recovery archive could not be read or written")
)

// Entry identifies a fixed section, never an archive path. ID is empty for the
// three singleton sections and a canonical 16-byte base64url object ID otherwise.
// The sizes and hashes describe original bytes, including ciphertext DB/objects.
type Entry struct {
	Kind   Kind
	ID     string
	Size   uint64
	SHA256 [32]byte
}

func (Entry) String() string   { return "[private WhatsUp recovery section]" }
func (Entry) GoString() string { return "[private WhatsUp recovery section]" }

type OpenFunc func(context.Context, Entry) (io.ReadCloser, error)
type StageFunc func(context.Context, Entry, io.Reader) error

// Zero values select the fixed defaults. Callers may lower, but never raise,
// these limits. Reducing MaxEntries to three forbids attachment-object sections.
type Limits struct {
	MaxEntries          uint32
	MaxTotalBytes       uint64
	MaxObjectBytes      uint64
	MaxApplicationBytes uint32
}

func limitsFor(supplied []Limits) (Limits, error) {
	if len(supplied) > 1 {
		return Limits{}, ErrLimit
	}
	l := Limits{MaxEntries: MaxEntries, MaxTotalBytes: MaxTotalBytes, MaxObjectBytes: MaxObjectBytes, MaxApplicationBytes: MaxApplicationBytes}
	if len(supplied) == 1 {
		v := supplied[0]
		if v.MaxEntries != 0 {
			l.MaxEntries = v.MaxEntries
		}
		if v.MaxTotalBytes != 0 {
			l.MaxTotalBytes = v.MaxTotalBytes
		}
		if v.MaxObjectBytes != 0 {
			l.MaxObjectBytes = v.MaxObjectBytes
		}
		if v.MaxApplicationBytes != 0 {
			l.MaxApplicationBytes = v.MaxApplicationBytes
		}
	}
	if l.MaxEntries < 3 || l.MaxEntries > MaxEntries || l.MaxTotalBytes < 33 || l.MaxTotalBytes > MaxTotalBytes || l.MaxObjectBytes > MaxObjectBytes || l.MaxApplicationBytes > MaxApplicationBytes {
		return Limits{}, ErrLimit
	}
	return l, nil
}

// Verified can be issued only after section integrity, the terminal marker and
// authenticated EOF have all passed. A zero value grants no authority.
type Verified struct{ state *verification }
type verification struct {
	count uint32
	total uint64
}

func (v Verified) Valid() bool { return v.state != nil }
func (v Verified) EntryCount() uint32 {
	if v.state == nil {
		return 0
	}
	return v.state.count
}
func (v Verified) TotalBytes() uint64 {
	if v.state == nil {
		return 0
	}
	return v.state.total
}
func (Verified) String() string               { return "[private WhatsUp recovery verification]" }
func (Verified) GoString() string             { return "[private WhatsUp recovery verification]" }
func (Verified) MarshalJSON() ([]byte, error) { return nil, ErrFormat }

var objectEncoding = base64.RawURLEncoding.Strict()

func objectID(value string) (raw [16]byte, fold [22]byte, ok bool) {
	if len(value) != 22 {
		return raw, fold, false
	}
	n, err := objectEncoding.Decode(raw[:], []byte(value))
	if err != nil || n != 16 {
		return raw, fold, false
	}
	var any byte
	for _, b := range raw {
		any |= b
	}
	if any == 0 {
		return raw, fold, false
	}
	for i, c := range []byte(value) {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		fold[i] = c
	}
	return raw, fold, true
}
