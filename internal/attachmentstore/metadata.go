// Package attachmentstore owns encrypted, resumable local attachment objects.
// Its contact-scoped metadata capability is supplied only after local approval.
package attachmentstore

import (
	"errors"
	"math"
	"mime"
	"time"

	"whatsup/internal/transfer"
)

const (
	QuotaBytes        uint64 = 4 << 30
	UnitBytes                = transfer.ChunkBytes * transfer.UnitChunks
	MaxUnits                 = int(transfer.MaxFileBytes / UnitBytes)
	MaxDrafts                = 4
	Filename                 = "attachments"
	LockFilename             = "attachments.lock"
	MaxAutoImageBytes uint64 = 25_000_000
)

var (
	ErrUnavailable = transfer.ErrUnavailable
	ErrIntegrity   = transfer.ErrIntegrity
	ErrCapacity    = transfer.ErrCapacity
	ErrConflict    = transfer.ErrConflict
	ErrUnsafePath  = errors.New("attachment objects require private regular files")
)

// Unit indexes remain inside an encrypted record. Compact fields keep the full
// index of a maximum-size file below SQLite's existing 128 KiB record bound.
type Unit struct {
	Offset      uint64 `json:"o"`
	CipherBytes uint32 `json:"c"`
	PlainBytes  uint32 `json:"p"`
}

// Entry is backend-only. Storage uses an explicit disk alias to serialize it;
// ordinary formatting/JSON never exposes the per-object encryption key.
type Entry struct {
	Version      int             `json:"version"`
	Revision     uint64          `json:"revision"`
	Record       transfer.Record `json:"record"`
	ObjectID     string          `json:"objectID"`
	Key          []byte          `json:"key"`
	Units        []Unit          `json:"units"`
	CipherBytes  uint64          `json:"cipherBytes"`
	Ready        bool            `json:"ready"`
	Approved     bool            `json:"approved"`
	ImageOnly    bool            `json:"imageOnly"`
	VerifiedType string          `json:"verifiedType"`
	Hidden       bool            `json:"hidden"`
	Garbage      bool            `json:"garbage"`
	CreatedAt    time.Time       `json:"createdAt"`
	Sequence     uint64          `json:"-"`
}

func (Entry) String() string               { return "[private encrypted WhatsUp attachment]" }
func (Entry) GoString() string             { return "[private encrypted WhatsUp attachment]" }
func (Entry) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func Clone(e Entry) Entry {
	e.Key = append([]byte(nil), e.Key...)
	e.Units = append([]Unit{}, e.Units...)
	return e
}

func (e Entry) Active() bool {
	return e.Record.State == transfer.Accepted || e.Record.State == transfer.Paused || e.Record.State == transfer.Queued
}

func (e Entry) Charge() uint64 {
	if e.ObjectID == "" {
		return 4096
	}
	// Reserve the full accepted/staged object, unit authentication/index space,
	// and a fixed filesystem/index allowance. This is a logical quota.
	units := (e.Record.Offer.Size + UnitBytes - 1) / UnitBytes
	return e.Record.Offer.Size + units*128 + 4096
}

func (e Entry) PlainBytes() uint64 {
	var total uint64
	for _, unit := range e.Units {
		total += uint64(unit.PlainBytes)
	}
	return total
}

func (e Entry) Validate() error {
	if e.Version != 1 || e.Revision == 0 || e.Revision == math.MaxUint64 || e.Record.Offer.Validate() != nil || e.Record.Hidden || e.CreatedAt.IsZero() || len(e.Units) > MaxUnits {
		return ErrIntegrity
	}
	if e.Record.Direction != transfer.Incoming && e.Record.Direction != transfer.Outgoing {
		return ErrIntegrity
	}
	if !transfer.ValidRecordedAt(e.Record.RecordedAt) || e.Record.State == transfer.Draft && !e.Record.RecordedAt.IsZero() {
		return ErrIntegrity
	}
	if (e.Record.PendingWithdrawal || e.Record.PeerCompleted) && e.Record.State != transfer.Declined || e.Record.PendingWithdrawal && e.Record.PeerCompleted {
		return ErrIntegrity
	}
	switch e.Record.State {
	case "draft", transfer.Offered, transfer.Queued, transfer.Accepted, transfer.Paused, transfer.Complete, transfer.Declined:
	default:
		return ErrIntegrity
	}
	if e.Record.Direction == transfer.Incoming && (e.Record.State == "draft" || e.Record.State == transfer.Queued) {
		return ErrIntegrity
	}
	if e.Record.Direction == transfer.Outgoing && (e.Record.State == transfer.Offered || e.Record.State == transfer.Accepted) {
		return ErrIntegrity
	}
	if e.Record.State == transfer.Offered && (e.ObjectID != "" || e.Approved || e.ImageOnly) {
		return ErrIntegrity
	}
	if e.Record.State == transfer.Accepted && (e.ObjectID == "" || !e.Approved) {
		return ErrIntegrity
	}
	if e.Record.Direction == transfer.Outgoing && e.Record.State == transfer.Queued && !e.Ready {
		return ErrIntegrity
	}
	if e.Record.Direction == transfer.Outgoing && e.Record.State == transfer.Paused && !e.Ready {
		return ErrIntegrity
	}
	if e.Record.Direction == transfer.Outgoing && e.Record.State == transfer.Complete && (!e.Ready && !e.Hidden || e.Record.NextChunk != e.Record.Offer.Chunks()) {
		return ErrIntegrity
	}
	if e.Record.Direction == transfer.Outgoing && e.Record.State != transfer.Complete && e.Record.NextChunk != 0 {
		return ErrIntegrity
	}
	if e.Record.Direction == transfer.Incoming && e.Record.State == transfer.Paused && (!e.Approved || e.ObjectID == "") {
		return ErrIntegrity
	}
	if e.Record.Direction == transfer.Incoming && e.Ready && e.Record.State != transfer.Complete && e.Record.State != transfer.Declined {
		return ErrIntegrity
	}
	if e.Record.State == transfer.Declined && (e.Approved || e.ImageOnly) {
		return ErrIntegrity
	}
	if e.Record.Direction == transfer.Outgoing && e.Approved {
		return ErrIntegrity
	}
	if e.ObjectID == "" {
		if len(e.Key) != 0 || len(e.Units) != 0 || e.CipherBytes != 0 || e.Ready {
			return ErrIntegrity
		}
	} else if !transfer.ValidID(e.ObjectID) || len(e.Key) != 32 {
		return ErrIntegrity
	}
	var plain, cipher uint64
	for index, unit := range e.Units {
		if unit.Offset != cipher || unit.PlainBytes == 0 && (e.Record.Offer.Size != 0 || index != 0 || len(e.Units) != 1) || unit.PlainBytes > UnitBytes || unit.CipherBytes < unit.PlainBytes+40+16 || unit.CipherBytes > unit.PlainBytes+128 {
			return ErrIntegrity
		}
		if index+1 < len(e.Units) && unit.PlainBytes != UnitBytes {
			return ErrIntegrity
		}
		plain += uint64(unit.PlainBytes)
		cipher += uint64(unit.CipherBytes)
	}
	if plain > e.Record.Offer.Size || cipher != e.CipherBytes {
		return ErrIntegrity
	}
	if len(e.Units) > 0 && plain < e.Record.Offer.Size && e.Units[len(e.Units)-1].PlainBytes != UnitBytes {
		return ErrIntegrity
	}
	if e.Ready && plain != e.Record.Offer.Size {
		return ErrIntegrity
	}
	if e.Record.Direction == transfer.Incoming {
		if e.Record.NextChunk != uint32((plain+transfer.ChunkBytes-1)/transfer.ChunkBytes) && !(e.Hidden && e.Record.State == transfer.Complete && e.ObjectID == "" && e.Record.NextChunk == e.Record.Offer.Chunks()) {
			return ErrIntegrity
		}
		if e.Record.State == transfer.Complete && (!e.Approved || !e.Ready && !e.Hidden) {
			return ErrIntegrity
		}
	}
	if e.ImageOnly && (!e.Approved || e.Record.Offer.Size > MaxAutoImageBytes) {
		return ErrIntegrity
	}
	if e.Garbage && e.ObjectID == "" {
		return ErrIntegrity
	}
	if e.Garbage && e.Record.State != transfer.Complete && e.Record.State != transfer.Declined {
		return ErrIntegrity
	}
	if e.VerifiedType != "" {
		parsed, params, err := mime.ParseMediaType(e.VerifiedType)
		if err != nil || parsed != e.VerifiedType || len(params) > 0 || len(parsed) > 127 {
			return ErrIntegrity
		}
	}
	return nil
}

// Metadata operations execute bounded encrypted SQLite transactions. Change's
// callback performs no filesystem or network I/O; the provider advances revision
// and enforces quota/active limits atomically with the replacement.
type Metadata interface {
	Epoch() string
	Get(string) (Entry, error)
	Active() ([]Entry, error)
	PendingWithdrawals() ([]Entry, error)
	Page(before uint64, limit int) (entries []Entry, next uint64, more bool, err error)
	Create(Entry) error
	Change(string, func(*Entry) error) (Entry, error)
	AutoAcceptImages() (bool, error)
	SetAutoAcceptImages(bool) error
}
