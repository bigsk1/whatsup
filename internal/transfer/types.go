// Package transfer owns bounded attachment records and authenticated stream
// coordination. It has no public listener, path selection or renderer binding.
package transfer

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ChunkBytes            = 64 << 10
	UnitChunks            = 16
	MaxFileBytes   uint64 = 2 << 30
	MaxActive             = 8
	MaxOffers             = 128
	LargeFileBytes uint64 = 100_000_000
	Draft                 = "draft"
	StateDraft            = Draft
	Incoming              = "incoming"
	Outgoing              = "outgoing"
	Offered               = "offered"
	Queued                = "queued"
	Accepted              = "accepted"
	Paused                = "paused"
	Complete              = "complete"
	Declined              = "declined"
)

var (
	ErrProtocol      = errors.New("invalid attachment protocol")
	ErrUnavailable   = errors.New("attachment is unavailable")
	ErrConflict      = errors.New("attachment identifier refers to different content")
	ErrCapacity      = errors.New("attachment storage is full")
	ErrIntegrity     = errors.New("attachment integrity could not be verified")
	ErrRelayApproval = errors.New("this transfer may use relay bandwidth; approval is required")
)

type Offer struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	MediaType string   `json:"mediaType"`
	Size      uint64   `json:"size"`
	SHA256    [32]byte `json:"sha256"`
}

func (Offer) String() string   { return "[private WhatsUp attachment offer]" }
func (Offer) GoString() string { return "[private WhatsUp attachment offer]" }

func ValidID(value string) bool {
	if len(value) != 22 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != 16 || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return false
	}
	var any byte
	for _, b := range decoded {
		any |= b
	}
	return any != 0
}

func (o Offer) Validate() error {
	if !ValidID(o.ID) || o.Size > MaxFileBytes || o.SHA256 == [32]byte{} || len(o.Name) == 0 || len(o.Name) > 255 ||
		!utf8.ValidString(o.Name) || strings.TrimSpace(o.Name) != o.Name || o.Name == "." || o.Name == ".." ||
		strings.HasSuffix(o.Name, ".") || strings.ContainsAny(o.Name, `/\<>:"|?*`) || len(o.MediaType) > 127 {
		return ErrProtocol
	}
	for _, r := range o.Name {
		if unicode.IsControl(r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) ||
			unicode.Is(unicode.Cf, r) && r != '\u200c' && r != '\u200d' {
			return ErrProtocol
		}
	}
	media, params, err := mime.ParseMediaType(o.MediaType)
	if err != nil || media != o.MediaType || len(params) != 0 || !strings.Contains(media, "/") {
		return ErrProtocol
	}
	return nil
}

func (o Offer) Chunks() uint32 { return uint32((o.Size + ChunkBytes - 1) / ChunkBytes) }

func (o Offer) ChunkSize(index uint32) (int, error) {
	if o.Validate() != nil || index >= o.Chunks() {
		return 0, ErrProtocol
	}
	remaining := o.Size - uint64(index)*ChunkBytes
	if remaining > ChunkBytes {
		return ChunkBytes, nil
	}
	return int(remaining), nil
}

type Progress struct {
	NextChunk uint32
}

type Record struct {
	Offer             Offer
	Direction         string
	State             string
	NextChunk         uint32
	AllowRelayedLarge bool
	Hidden            bool
}

func (Record) String() string   { return "[private WhatsUp attachment record]" }
func (Record) GoString() string { return "[private WhatsUp attachment record]" }

func validRecord(r Record) bool {
	if r.Offer.Validate() != nil || !checkpoint(r.Offer, r.NextChunk) {
		return false
	}
	if r.Direction == Incoming {
		return r.State == Offered || r.State == Accepted || r.State == Paused || r.State == Complete || r.State == Declined
	}
	return r.Direction == Outgoing && (r.State == Draft || r.State == Queued || r.State == Paused || r.State == Complete || r.State == Declined)
}

// Repository is one exact approved contact's capability. All metadata is
// encrypted. Append returns only a durable checkpoint, never a buffered index.
// Immutable encrypted units contain sixteen wire chunks; Resume discards an
// unfinished unit and reactivates only an explicit local pause. Complete incoming
// content is readable only after Finalize verifies the offered whole-file hash.
type Repository interface {
	List() ([]Record, error)
	Get(id string) (Record, error)
	QueueOutgoing(id string, allowRelayedLarge bool) error
	Offer(Offer) error
	Accept(id string) (Progress, error)
	Append(id string, index uint32, chunk []byte) (Progress, error)
	Resume(id string) (Progress, error)
	Finalize(context.Context, string) error
	Cancel(id string) error
	Decline(id string) error
	MarkDelivered(id string) error
	Open(context.Context, string) (io.ReadSeekCloser, error)
}

// OutgoingRepository separates transport reads from user-readable media. Clear
// history withdraws unfinished attachments and revokes both forms of authority.
type OutgoingRepository interface {
	OpenOutgoing(context.Context, string) (io.ReadSeekCloser, error)
}

type Summary struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	MediaType        string `json:"mediaType"`
	Size             uint64 `json:"size"`
	Direction        string `json:"direction"`
	State            string `json:"state"`
	TransferredBytes uint64 `json:"transferredBytes"`
	Warning          string `json:"warning"`
}
