package recovery

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"hash"
	"io"

	"whatsup/internal/keymaterial"
)

const (
	headerSize       = 28
	prefixSize       = 20
	entrySize        = 57
	segmentSize      = 1 << 20
	streamHeaderSize = 40
	streamTagSize    = 16
	archiveMagic     = "WUPRECOV"
	bodyMagic        = "WUPDATA1"
	terminal         = "WUPDONE1"
)

// Write leaves dst ownership with its caller. Entries and sources must remain
// immutable until it returns. The first sections are Master/Application/Database;
// object IDs then increase strictly by ASCII case-folded order. No filename or
// path from this format can reach a filesystem operation inside this package.
func Write(ctx context.Context, dst io.Writer, passphrase []byte, entries []Entry, open OpenFunc, supplied ...Limits) error {
	if ctx == nil || dst == nil || open == nil {
		return ErrIO
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if keymaterial.ValidateSecret(passphrase) != nil {
		return ErrSecret
	}
	limits, err := limitsFor(supplied)
	if err != nil {
		return err
	}
	count, total, err := validateEntries(ctx, entries, limits)
	if err != nil {
		return err
	}
	var header [headerSize]byte
	copy(header[:], archiveMagic)
	binary.BigEndian.PutUint16(header[8:10], 1)
	header[10] = 1
	header[11] = 1
	if _, err := rand.Read(header[12:]); err != nil {
		return ErrIO
	}
	key, err := derive(ctx, passphrase, header[12:])
	if err != nil {
		return err
	}
	defer clear(key)
	aead, err := primitive(key)
	if err != nil {
		return err
	}
	output := guardedWriter{ctx: ctx, w: dst}
	if writeBytes(output, header[:]) != nil {
		return resultError(ctx, ErrIO)
	}
	writer, err := aead.NewEncryptingWriter(output, associatedData(header[:]))
	if err != nil {
		return resultError(ctx, ErrIO)
	}
	closed := false
	defer func() {
		if !closed {
			_ = writer.Close()
		}
	}()
	var prefix [prefixSize]byte
	copy(prefix[:], bodyMagic)
	binary.BigEndian.PutUint32(prefix[8:12], count)
	binary.BigEndian.PutUint64(prefix[12:20], total)
	if writeBytes(writer, prefix[:]) != nil {
		return resultError(ctx, ErrIO)
	}
	buffer := make([]byte, 64<<10)
	defer clear(buffer)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		descriptor := encodeEntry(entry)
		if writeBytes(writer, descriptor[:]) != nil {
			return resultError(ctx, ErrIO)
		}
		reader, err := open(ctx, entry)
		if err != nil || reader == nil {
			if reader != nil {
				_ = reader.Close()
			}
			return resultError(ctx, ErrIO)
		}
		err = writeSection(ctx, writer, reader, entry, buffer)
		closeErr := reader.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return resultError(ctx, ErrIO)
		}
	}
	if writeBytes(writer, []byte(terminal)) != nil {
		return resultError(ctx, ErrIO)
	}
	if writer.Close() != nil {
		return resultError(ctx, ErrIO)
	}
	closed = true
	return ctx.Err()
}

// Read passes only bounded streams to stage. That callback must consume every
// byte and may retain Master/Application only in RAM; other sections are already
// ciphertext and may be written to uninstalled private staging. It is called
// before final verification, never as authorization to install live state.
// Any error returns the zero receipt, even if earlier sections staged correctly.
func Read(ctx context.Context, src io.Reader, passphrase []byte, stage StageFunc, supplied ...Limits) (Verified, error) {
	if ctx == nil || src == nil || stage == nil {
		return Verified{}, ErrIO
	}
	if err := ctx.Err(); err != nil {
		return Verified{}, err
	}
	if keymaterial.ValidateSecret(passphrase) != nil {
		return Verified{}, ErrSecret
	}
	limits, err := limitsFor(supplied)
	if err != nil {
		return Verified{}, err
	}
	raw := &ciphertextReader{r: &guardedReader{ctx: ctx, r: src}, max: cipherBytes(limits.MaxEntries, limits.MaxTotalBytes)}
	var header [headerSize]byte
	if _, err := io.ReadFull(raw, header[:]); err != nil {
		return Verified{}, resultError(ctx, ErrFormat)
	}
	if string(header[:8]) != archiveMagic || binary.BigEndian.Uint16(header[8:10]) != 1 || header[10] != 1 || header[11] != 1 {
		return Verified{}, ErrFormat
	}
	key, err := derive(ctx, passphrase, header[12:])
	if err != nil {
		return Verified{}, err
	}
	defer clear(key)
	aead, err := primitive(key)
	if err != nil {
		return Verified{}, err
	}
	reader, err := aead.NewDecryptingReader(raw, associatedData(header[:]))
	if err != nil {
		return Verified{}, readError(ctx, err)
	}
	plain := &guardedReader{ctx: ctx, r: reader}
	var prefix [prefixSize]byte
	if _, err := io.ReadFull(plain, prefix[:]); err != nil {
		return Verified{}, readError(ctx, err)
	}
	if string(prefix[:8]) != bodyMagic {
		return Verified{}, ErrFormat
	}
	count, total := binary.BigEndian.Uint32(prefix[8:12]), binary.BigEndian.Uint64(prefix[12:20])
	if count < 3 || count > limits.MaxEntries || total < 33 || total > limits.MaxTotalBytes {
		return Verified{}, ErrLimit
	}
	// The fixed Tink profile has a deterministic length for a given plaintext
	// length. Tighten the raw bound after the encrypted count/total is known.
	expected := cipherBytes(count, total)
	if raw.used > expected {
		return Verified{}, ErrFormat
	}
	raw.max = expected
	var consumed uint64
	var previous [22]byte
	for index := uint32(0); index < count; index++ {
		var descriptor [entrySize]byte
		if _, err := io.ReadFull(plain, descriptor[:]); err != nil {
			return Verified{}, readError(ctx, err)
		}
		entry, err := decodeEntry(descriptor)
		if err != nil {
			return Verified{}, err
		}
		previous, err = validateEntry(index, entry, limits, previous)
		if err != nil {
			return Verified{}, err
		}
		if entry.Size > total-consumed {
			return Verified{}, ErrFormat
		}
		consumed += entry.Size
		body := &sectionReader{ctx: ctx, r: plain, remaining: entry.Size, digest: sha256.New()}
		stageErr := stage(ctx, entry, body)
		if err := ctx.Err(); err != nil {
			return Verified{}, err
		}
		if body.failure != nil {
			return Verified{}, readError(ctx, body.failure)
		}
		if stageErr != nil {
			return Verified{}, ErrIO
		}
		if body.remaining != 0 {
			return Verified{}, ErrFormat
		}
		if subtle.ConstantTimeCompare(body.digest.Sum(nil), entry.SHA256[:]) != 1 {
			return Verified{}, ErrAuthentication
		}
	}
	if consumed != total {
		return Verified{}, ErrFormat
	}
	var end [len(terminal)]byte
	if _, err := io.ReadFull(plain, end[:]); err != nil {
		return Verified{}, readError(ctx, err)
	}
	if string(end[:]) != terminal {
		return Verified{}, ErrFormat
	}
	var extra [1]byte
	n, err := io.ReadFull(plain, extra[:])
	if n != 0 || err != io.EOF {
		if err != nil {
			return Verified{}, readError(ctx, err)
		}
		return Verified{}, ErrFormat
	}
	if raw.used != expected {
		return Verified{}, ErrFormat
	}
	// Explicitly require physical EOF too; never accept a second archive or an
	// appended payload merely because an inner reader stopped producing bytes.
	n, err = io.ReadFull(raw, extra[:])
	if n != 0 || err != io.EOF {
		if raw.used > expected {
			return Verified{}, ErrFormat
		}
		return Verified{}, readError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return Verified{}, err
	}
	return Verified{state: &verification{count: count, total: total}}, nil
}

func associatedData(header []byte) []byte {
	return append([]byte("WhatsUp portable recovery archive v1\x00"), header...)
}
func cipherBytes(count uint32, total uint64) uint64 {
	plain := uint64(prefixSize+len(terminal)) + uint64(count)*entrySize + total
	segments := (plain + streamHeaderSize + (segmentSize - streamTagSize) - 1) / (segmentSize - streamTagSize)
	return headerSize + plain + streamHeaderSize + segments*streamTagSize
}
func validateEntries(ctx context.Context, entries []Entry, limits Limits) (uint32, uint64, error) {
	if len(entries) < 3 || uint64(len(entries)) > uint64(limits.MaxEntries) {
		return 0, 0, ErrLimit
	}
	var total uint64
	var previous [22]byte
	for index, entry := range entries {
		if index&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, 0, err
			}
		}
		var err error
		previous, err = validateEntry(uint32(index), entry, limits, previous)
		if err != nil {
			return 0, 0, err
		}
		if entry.Size > limits.MaxTotalBytes-total {
			return 0, 0, ErrLimit
		}
		total += entry.Size
	}
	return uint32(len(entries)), total, nil
}
func validateEntry(index uint32, entry Entry, limits Limits, previous [22]byte) ([22]byte, error) {
	if index < 3 {
		if entry.Kind != Kind(index+1) || entry.ID != "" {
			return previous, ErrFormat
		}
		switch entry.Kind {
		case Master:
			if entry.Size != 32 {
				return previous, ErrFormat
			}
		case Application:
			if entry.Size == 0 || entry.Size > uint64(limits.MaxApplicationBytes) {
				return previous, ErrLimit
			}
		case Database:
			if entry.Size > limits.MaxTotalBytes {
				return previous, ErrLimit
			}
		}
		return previous, nil
	}
	if entry.Kind != Object || entry.Size > limits.MaxObjectBytes {
		if entry.Size > limits.MaxObjectBytes {
			return previous, ErrLimit
		}
		return previous, ErrFormat
	}
	_, fold, ok := objectID(entry.ID)
	if !ok || bytes.Compare(previous[:], fold[:]) >= 0 {
		return previous, ErrFormat
	}
	return fold, nil
}
func encodeEntry(entry Entry) (wire [entrySize]byte) {
	wire[0] = byte(entry.Kind)
	if entry.Kind == Object {
		raw, _, _ := objectID(entry.ID)
		copy(wire[1:17], raw[:])
	}
	binary.BigEndian.PutUint64(wire[17:25], entry.Size)
	copy(wire[25:57], entry.SHA256[:])
	return wire
}
func decodeEntry(wire [entrySize]byte) (Entry, error) {
	entry := Entry{Kind: Kind(wire[0]), Size: binary.BigEndian.Uint64(wire[17:25])}
	copy(entry.SHA256[:], wire[25:57])
	if entry.Kind == Object {
		entry.ID = objectEncoding.EncodeToString(wire[1:17])
	} else {
		var any byte
		for _, b := range wire[1:17] {
			any |= b
		}
		if any != 0 {
			return Entry{}, ErrFormat
		}
	}
	return entry, nil
}

func writeSection(ctx context.Context, w io.Writer, r io.Reader, entry Entry, buffer []byte) error {
	reader := &guardedReader{ctx: ctx, r: r}
	digest := sha256.New()
	remaining := entry.Size
	for remaining > 0 {
		amount := uint64(len(buffer))
		if amount > remaining {
			amount = remaining
		}
		chunk := buffer[:int(amount)]
		if _, err := io.ReadFull(reader, chunk); err != nil {
			return resultError(ctx, ErrIO)
		}
		if writeBytes(w, chunk) != nil {
			return resultError(ctx, ErrIO)
		}
		_, _ = digest.Write(chunk)
		remaining -= amount
	}
	var extra [1]byte
	n, err := io.ReadFull(reader, extra[:])
	if n != 0 || err != io.EOF {
		return resultError(ctx, ErrFormat)
	}
	if subtle.ConstantTimeCompare(digest.Sum(nil), entry.SHA256[:]) != 1 {
		return ErrAuthentication
	}
	return nil
}

type sectionReader struct {
	ctx       context.Context
	r         io.Reader
	remaining uint64
	digest    hash.Hash
	failure   error
}

func (r *sectionReader) Read(p []byte) (int, error) {
	if r.failure != nil {
		return 0, r.failure
	}
	if err := r.ctx.Err(); err != nil {
		r.failure = err
		return 0, err
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if uint64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		_, _ = r.digest.Write(p[:n])
		r.remaining -= uint64(n)
	}
	if err != nil && err != io.EOF {
		r.failure = err
	} else if err == io.EOF && r.remaining != 0 {
		r.failure = io.ErrUnexpectedEOF
		err = r.failure
	}
	return n, err
}
