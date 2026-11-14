package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"
)

func plaintext(parts []part) []byte {
	var total uint64
	for _, p := range parts {
		total += uint64(len(p.data))
	}
	prefix := make([]byte, prefixSize)
	copy(prefix, bodyMagic)
	binary.BigEndian.PutUint32(prefix[8:12], uint32(len(parts)))
	binary.BigEndian.PutUint64(prefix[12:20], total)
	result := prefix
	for _, p := range parts {
		descriptor := encodeEntry(p.entry)
		result = append(result, descriptor[:]...)
		result = append(result, p.data...)
	}
	return append(result, terminal...)
}
func sealPlain(t testing.TB, data []byte) []byte {
	t.Helper()
	var header [headerSize]byte
	copy(header[:], archiveMagic)
	binary.BigEndian.PutUint16(header[8:10], 1)
	header[10], header[11] = 1, 1
	copy(header[12:], "synthetic-salt16")
	key, err := derive(context.Background(), testSecret, header[12:])
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	aead, err := primitive(key)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	output.Write(header[:])
	writer, err := aead.NewEncryptingWriter(&output, associatedData(header[:]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if writer.Close() != nil {
		t.Fatal("fixture encryption failed")
	}
	return output.Bytes()
}

func TestAuthenticatedMalformedBodiesNeverGrantInstallAuthority(t *testing.T) {
	parts := fixture([]byte("first object"), []byte("second object"))
	base := plaintext(parts)
	firstObject := prefixSize
	for _, p := range parts[:3] {
		firstObject += entrySize + len(p.data)
	}
	secondObject := firstObject + entrySize + len(parts[3].data)
	cases := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"body marker", func(p []byte) []byte { p[0] ^= 1; return p }},
		{"too few sections", func(p []byte) []byte { binary.BigEndian.PutUint32(p[8:12], 2); return p }},
		{"too many sections", func(p []byte) []byte { binary.BigEndian.PutUint32(p[8:12], MaxEntries+1); return p }},
		{"count overflow", func(p []byte) []byte { binary.BigEndian.PutUint32(p[8:12], math.MaxUint32); return p }},
		{"total overflow", func(p []byte) []byte { binary.BigEndian.PutUint64(p[12:20], math.MaxUint64); return p }},
		{"total below content", func(p []byte) []byte {
			binary.BigEndian.PutUint64(p[12:20], binary.BigEndian.Uint64(p[12:20])-1)
			return p
		}},
		{"total above content", func(p []byte) []byte {
			binary.BigEndian.PutUint64(p[12:20], binary.BigEndian.Uint64(p[12:20])+1)
			return p
		}},
		{"unknown kind", func(p []byte) []byte { p[prefixSize] = 255; return p }},
		{"singleton object ID", func(p []byte) []byte { p[prefixSize+1] = 1; return p }},
		{"master length", func(p []byte) []byte { binary.BigEndian.PutUint64(p[prefixSize+17:prefixSize+25], 33); return p }},
		{"object length overflow", func(p []byte) []byte {
			binary.BigEndian.PutUint64(p[firstObject+17:firstObject+25], math.MaxUint64)
			return p
		}},
		{"object zero ID", func(p []byte) []byte { clear(p[firstObject+1 : firstObject+17]); return p }},
		{"duplicate object ID", func(p []byte) []byte {
			copy(p[secondObject+1:secondObject+17], p[firstObject+1:firstObject+17])
			return p
		}},
		{"case folded object alias", func(p []byte) []byte {
			alias := "a" + parts[3].entry.ID[1:]
			raw, _, ok := objectID(alias)
			if !ok {
				t.Fatal("invalid alias fixture")
			}
			copy(p[secondObject+1:secondObject+17], raw[:])
			return p
		}},
		{"object order", func(p []byte) []byte {
			a := append([]byte(nil), p[firstObject+1:firstObject+17]...)
			copy(p[firstObject+1:firstObject+17], p[secondObject+1:secondObject+17])
			copy(p[secondObject+1:secondObject+17], a)
			return p
		}},
		{"swapped body", func(p []byte) []byte { p[firstObject+entrySize] ^= 1; return p }},
		{"wrong section hash", func(p []byte) []byte { p[prefixSize+25] ^= 1; return p }},
		{"terminal marker", func(p []byte) []byte { p[len(p)-1] ^= 1; return p }},
		{"missing terminal", func(p []byte) []byte { return p[:len(p)-len(terminal)] }},
		{"extra plaintext", func(p []byte) []byte { return append(p, 1) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			encoded := sealPlain(t, c.mutate(append([]byte(nil), base...)))
			v, err := Read(context.Background(), bytes.NewReader(encoded), testSecret, discard)
			if err == nil || v.Valid() {
				t.Fatal("malformed authenticated archive yielded authority")
			}
		})
	}
}

func TestTamperingTruncationAndAppendedCiphertextAreRejected(t *testing.T) {
	encoded := archive(t, fixture(bytes.Repeat([]byte("synthetic object"), 160000)))
	for _, offset := range []int{12, headerSize, headerSize + streamHeaderSize + 100, headerSize + segmentSize + 100, len(encoded) - 17, len(encoded) - 1} {
		changed := append([]byte(nil), encoded...)
		changed[offset] ^= 1
		v, err := Read(context.Background(), bytes.NewReader(changed), testSecret, discard)
		if err == nil || v.Valid() {
			t.Fatalf("ciphertext mutation at %d accepted", offset)
		}
	}
	for _, size := range []int{0, 1, headerSize - 1, headerSize, headerSize + streamHeaderSize - 1, headerSize + segmentSize - 1, len(encoded) - 17, len(encoded) - 1} {
		v, err := Read(context.Background(), bytes.NewReader(encoded[:size]), testSecret, discard)
		if err == nil || v.Valid() {
			t.Fatalf("truncation at %d accepted", size)
		}
	}
	for _, suffix := range [][]byte{{0}, encoded} {
		joined := append(append([]byte(nil), encoded...), suffix...)
		v, err := Read(context.Background(), bytes.NewReader(joined), testSecret, discard)
		if err == nil || v.Valid() {
			t.Fatal("appended ciphertext or a second archive accepted")
		}
	}
}

func TestLargeDeclaredCountDoesNotAllocateAnEntryArray(t *testing.T) {
	data := plaintext(fixture())
	binary.BigEndian.PutUint32(data[8:12], MaxEntries)
	encoded := sealPlain(t, data)
	calls := 0
	v, err := Read(context.Background(), bytes.NewReader(encoded), testSecret, func(ctx context.Context, e Entry, r io.Reader) error { calls++; return discard(ctx, e, r) })
	if err == nil || v.Valid() || calls != 3 {
		t.Fatal("large declared count did not fail incrementally")
	}
}

type emptyReader struct{ calls int }

func (r *emptyReader) Read([]byte) (int, error) { r.calls++; return 0, nil }

type shortWriter struct{}

func (shortWriter) Write([]byte) (int, error) { return 0, nil }

type failingCloser struct{ io.Reader }

func (failingCloser) Close() error { return errors.New("synthetic secret close path") }
func TestNoProgressShortWritesAndSourceFailures(t *testing.T) {
	input := &emptyReader{}
	v, err := Read(context.Background(), input, testSecret, discard)
	if err == nil || v.Valid() || input.calls != 100 {
		t.Fatal("no-progress input was not bounded")
	}
	parts := fixture([]byte("expected"))
	entries := descriptors(parts)
	if err := Write(context.Background(), shortWriter{}, testSecret, entries, opener(parts)); !errors.Is(err, ErrIO) {
		t.Fatal("short writer accepted")
	}
	for _, open := range []OpenFunc{
		func(context.Context, Entry) (io.ReadCloser, error) {
			return nil, errors.New("synthetic secret open path")
		},
		func(context.Context, Entry) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte("short"))), nil
		},
		func(context.Context, Entry) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), 33))), nil
		},
		func(context.Context, Entry) (io.ReadCloser, error) {
			return failingCloser{bytes.NewReader(parts[0].data)}, nil
		},
	} {
		var output bytes.Buffer
		err := Write(context.Background(), &output, testSecret, entries, open)
		if err == nil || strings.Contains(err.Error(), "synthetic secret") {
			t.Fatal("source failure leaked or was accepted")
		}
		v, readErr := Read(context.Background(), bytes.NewReader(output.Bytes()), testSecret, discard)
		if readErr == nil || v.Valid() {
			t.Fatal("failed export became a valid archive")
		}
	}
	bad := append([]part(nil), parts...)
	bad[0].entry.SHA256 = sha256.Sum256([]byte("wrong"))
	var output bytes.Buffer
	if err := Write(context.Background(), &output, testSecret, descriptors(bad), opener(bad)); !errors.Is(err, ErrAuthentication) {
		t.Fatal("source hash change accepted")
	}
}

func TestCancellationAndKDFQueueAreBounded(t *testing.T) {
	encoded := archive(t, fixture([]byte("object")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v, err := Read(ctx, bytes.NewReader(encoded), testSecret, discard)
	if !errors.Is(err, context.Canceled) || v.Valid() {
		t.Fatal("cancelled input accepted")
	}
	ctx, cancel = context.WithCancel(context.Background())
	v, err = Read(ctx, bytes.NewReader(encoded), testSecret, func(context.Context, Entry, io.Reader) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || v.Valid() {
		t.Fatal("cancelled staging yielded authority")
	}
	derivation <- struct{}{}
	defer func() { <-derivation }()
	// Unsupported clear profiles reject before waiting for expensive derivation.
	for _, offset := range []int{8, 10, 11} {
		changed := append([]byte(nil), encoded...)
		changed[offset] ^= 1
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		v, err := Read(ctx, bytes.NewReader(changed), testSecret, discard)
		cancel()
		if !errors.Is(err, ErrFormat) || v.Valid() {
			t.Fatal("unsupported header reached KDF")
		}
	}
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		v, err := Read(ctx, bytes.NewReader(encoded), testSecret, discard)
		if v.Valid() {
			done <- errors.New("cancelled KDF yielded authority")
			return
		}
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled derivation wait blocked")
	}
}

type diskFullWriter struct {
	data      bytes.Buffer
	remaining int
}

func (w *diskFullWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		n, _ := w.data.Write(p[:w.remaining])
		w.remaining = 0
		return n, errors.New("synthetic disk full")
	}
	n, err := w.data.Write(p)
	w.remaining -= n
	return n, err
}
func TestDiskFullAndIgnoredAuthenticationErrorCannotComplete(t *testing.T) {
	parts := fixture(bytes.Repeat([]byte("large synthetic object"), 100000))
	writer := &diskFullWriter{remaining: headerSize + segmentSize/2}
	err := Write(context.Background(), writer, testSecret, descriptors(parts), opener(parts))
	if !errors.Is(err, ErrIO) {
		t.Fatal("full disk was reported as successful export")
	}
	v, err := Read(context.Background(), bytes.NewReader(writer.data.Bytes()), testSecret, discard)
	if err == nil || v.Valid() {
		t.Fatal("partial disk-full output granted authority")
	}
	encoded := archive(t, parts)
	encoded[len(encoded)-1] ^= 1
	v, err = Read(context.Background(), bytes.NewReader(encoded), testSecret, func(_ context.Context, _ Entry, r io.Reader) error { _, _ = io.Copy(io.Discard, r); return nil })
	if !errors.Is(err, ErrAuthentication) || v.Valid() {
		t.Fatal("callback swallowed final authentication failure")
	}
}

func TestReducedImportLimitsAndZeroReceiptFailClosed(t *testing.T) {
	encoded := archive(t, fixture([]byte("object")))
	for _, limits := range []Limits{{MaxEntries: 3}, {MaxTotalBytes: 33}, {MaxObjectBytes: 1}, {MaxApplicationBytes: 1}} {
		v, err := Read(context.Background(), bytes.NewReader(encoded), testSecret, discard, limits)
		if err == nil || v.Valid() {
			t.Fatal("reduced import limit was bypassed")
		}
	}
	var zero Verified
	if zero.Valid() || zero.EntryCount() != 0 || zero.TotalBytes() != 0 {
		t.Fatal("zero receipt granted authority")
	}
}

func FuzzReadNeverIssuesAuthorityAfterFailure(f *testing.F) {
	parts := fixture([]byte("synthetic fuzz object"))
	f.Add(archive(f, parts))
	f.Add([]byte("not an archive"))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 2<<20 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		v, err := Read(ctx, bytes.NewReader(input), testSecret, discard, Limits{MaxEntries: 8, MaxTotalBytes: 1 << 20, MaxObjectBytes: 1 << 20})
		if (err == nil) != v.Valid() {
			t.Fatal("error/verification authority mismatch")
		}
	})
}

func FuzzDescriptorValidation(f *testing.F) {
	for _, p := range fixture([]byte("synthetic object")) {
		wire := encodeEntry(p.entry)
		f.Add(uint8(p.entry.Kind-1), wire[:])
	}
	f.Fuzz(func(t *testing.T, index uint8, data []byte) {
		if len(data) != entrySize {
			return
		}
		var wire [entrySize]byte
		copy(wire[:], data)
		entry, err := decodeEntry(wire)
		if err != nil {
			return
		}
		limits, _ := limitsFor(nil)
		_, err = validateEntry(uint32(index), entry, limits, [22]byte{})
		if err != nil {
			return
		}
		if index < 3 {
			if entry.Kind != Kind(index+1) || entry.ID != "" {
				t.Fatal("invalid singleton accepted")
			}
			if index == 0 && entry.Size != 32 {
				t.Fatal("invalid master size accepted")
			}
			if index == 1 && (entry.Size == 0 || entry.Size > uint64(MaxApplicationBytes)) {
				t.Fatal("invalid application size accepted")
			}
		} else {
			_, _, valid := objectID(entry.ID)
			if entry.Kind != Object || !valid || entry.Size > MaxObjectBytes {
				t.Fatal("invalid object accepted")
			}
		}
	})
}
