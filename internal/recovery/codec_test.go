package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
)

var testSecret = []byte("  synthetic recovery secret 🗝️  ")

type part struct {
	entry Entry
	data  []byte
}

func fixture(objects ...[]byte) []part {
	result := []part{{entry: Entry{Kind: Master}, data: bytes.Repeat([]byte{0x71}, 32)}, {entry: Entry{Kind: Application}, data: []byte(`{"synthetic":"portable identity and contact payload"}`)}, {entry: Entry{Kind: Database}, data: []byte("synthetic encrypted database")}}
	for index, data := range objects {
		id := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(index + 1)}, 16))
		result = append(result, part{entry: Entry{Kind: Object, ID: id}, data: data})
	}
	sort.Slice(result[3:], func(i, j int) bool {
		return strings.ToLower(result[3+i].entry.ID) < strings.ToLower(result[3+j].entry.ID)
	})
	for index := range result {
		result[index].entry.Size = uint64(len(result[index].data))
		result[index].entry.SHA256 = sha256.Sum256(result[index].data)
	}
	return result
}
func descriptors(parts []part) []Entry {
	result := make([]Entry, len(parts))
	for index, p := range parts {
		result[index] = p.entry
	}
	return result
}
func opener(parts []part) OpenFunc {
	return func(_ context.Context, e Entry) (io.ReadCloser, error) {
		for _, p := range parts {
			if p.entry.Kind == e.Kind && p.entry.ID == e.ID {
				return io.NopCloser(bytes.NewReader(p.data)), nil
			}
		}
		return nil, errors.New("synthetic missing source")
	}
}
func archive(t testing.TB, parts []part) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := Write(context.Background(), &output, testSecret, descriptors(parts), opener(parts)); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
func discard(ctx context.Context, e Entry, r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

func TestRoundTripUsesFreshEncryptionAndExactSecrets(t *testing.T) {
	parts := fixture([]byte("synthetic file contents"), nil)
	first := archive(t, parts)
	second := archive(t, parts)
	if bytes.Equal(first, second) || !bytes.Equal(first[:8], []byte(archiveMagic)) {
		t.Fatal("encryption salt was reused or format missing")
	}
	for _, p := range parts {
		if len(p.data) >= 16 && bytes.Contains(first, p.data) {
			t.Fatal("archive exposed section plaintext")
		}
	}
	var staged []part
	verified, err := Read(context.Background(), bytes.NewReader(first), testSecret, func(_ context.Context, e Entry, r io.Reader) error {
		data, err := io.ReadAll(r)
		staged = append(staged, part{e, data})
		return err
	})
	if err != nil || !verified.Valid() || verified.EntryCount() != uint32(len(parts)) || len(staged) != len(parts) {
		t.Fatalf("roundtrip failed: %v", err)
	}
	var total uint64
	for index, p := range parts {
		if staged[index].entry != p.entry || !bytes.Equal(staged[index].data, p.data) {
			t.Fatal("restored section changed")
		}
		total += p.entry.Size
	}
	if verified.TotalBytes() != total {
		t.Fatal("wrong receipt total")
	}
	if uint64(len(first)) != cipherBytes(uint32(len(parts)), total) {
		t.Fatal("fixed streaming profile ciphertext length changed")
	}
	if _, err := json.Marshal(verified); err == nil {
		t.Fatal("private verification receipt serialized")
	}
	if strings.Contains(fmt.Sprintf("%#v", verified), "verification{") {
		t.Fatal("private receipt formatting leaked state")
	}
	for _, secret := range [][]byte{[]byte(strings.TrimSpace(string(testSecret))), []byte("different long secret")} {
		v, err := Read(context.Background(), bytes.NewReader(first), secret, discard)
		if !errors.Is(err, ErrAuthentication) || v.Valid() {
			t.Fatal("secret bytes were normalized or wrong secret accepted")
		}
	}
}

func TestExactSegmentBoundaries(t *testing.T) {
	base := fixture(nil)
	base[1].data = []byte("x")
	base[2].data = nil
	for i := range base {
		base[i].entry.Size = uint64(len(base[i].data))
		base[i].entry.SHA256 = sha256.Sum256(base[i].data)
	}
	fixed := prefixSize + len(terminal) + len(base)*entrySize + 33
	firstCapacity := segmentSize - streamHeaderSize - streamTagSize
	for _, size := range []int{0, 1, firstCapacity - fixed - 1, firstCapacity - fixed, firstCapacity - fixed + 1, 2*(segmentSize-streamTagSize) - streamHeaderSize - fixed} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			parts := append([]part(nil), base...)
			parts[3].data = bytes.Repeat([]byte{0x36}, size)
			parts[3].entry.Size = uint64(size)
			parts[3].entry.SHA256 = sha256.Sum256(parts[3].data)
			encoded := archive(t, parts)
			v, err := Read(context.Background(), bytes.NewReader(encoded), testSecret, discard)
			if err != nil || !v.Valid() {
				t.Fatalf("segment boundary rejected: %v", err)
			}
			if uint64(len(encoded)) != cipherBytes(uint32(len(parts)), uint64(fixed-prefixSize-len(terminal)-len(parts)*entrySize+size)) {
				t.Fatal("unexpected segment length")
			}
		})
	}
}

type observedReader struct {
	remaining uint64
	position  uint64
	largest   int
}

func (r *observedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if uint64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	if len(p) > r.largest {
		r.largest = len(p)
	}
	for i := range p {
		p[i] = byte((r.position + uint64(i)) % 251)
	}
	r.remaining -= uint64(len(p))
	r.position += uint64(len(p))
	return len(p), nil
}
func (*observedReader) Close() error { return nil }
func TestLargeArchiveStreamsThroughCiphertextFile(t *testing.T) {
	const size = 32 << 20
	digest := sha256.New()
	source := &observedReader{remaining: size}
	if _, err := io.CopyBuffer(digest, source, make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	parts := fixture(nil)
	parts[3].entry.Size = size
	copy(parts[3].entry.SHA256[:], digest.Sum(nil))
	file, err := os.CreateTemp(t.TempDir(), "synthetic-encrypted-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	source = &observedReader{remaining: size}
	baseOpen := opener(parts)
	open := func(ctx context.Context, e Entry) (io.ReadCloser, error) {
		if e.Kind == Object {
			return source, nil
		}
		return baseOpen(ctx, e)
	}
	if err := Write(context.Background(), file, testSecret, descriptors(parts), open); err != nil {
		t.Fatal(err)
	}
	if source.largest > 64<<10 {
		t.Fatal("source reads were not bounded")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	var objectBytes int64
	v, err := Read(context.Background(), file, testSecret, func(_ context.Context, e Entry, r io.Reader) error {
		n, err := io.CopyBuffer(io.Discard, r, make([]byte, 64<<10))
		if e.Kind == Object {
			objectBytes = n
		}
		return err
	})
	if err != nil || !v.Valid() || objectBytes != size {
		t.Fatalf("streamed archive failed: %v", err)
	}
}

func TestLimitsSecretsAndSourceOrderFailBeforeOpeningData(t *testing.T) {
	parts := fixture([]byte("a"), []byte("b"))
	entries := descriptors(parts)
	for _, secret := range [][]byte{nil, []byte("elevenbytes"), bytes.Repeat([]byte("x"), 1025), append(bytes.Repeat([]byte("x"), 12), 0xff)} {
		var output bytes.Buffer
		called := false
		err := Write(context.Background(), &output, secret, entries, func(context.Context, Entry) (io.ReadCloser, error) { called = true; return nil, nil })
		if !errors.Is(err, ErrSecret) || called || output.Len() != 0 {
			t.Fatal("invalid secret reached source or output")
		}
		v, err := Read(context.Background(), panicReader{}, secret, discard)
		if !errors.Is(err, ErrSecret) || v.Valid() {
			t.Fatal("invalid secret reached archive input")
		}
	}
	for _, limits := range []Limits{{MaxEntries: 2}, {MaxEntries: MaxEntries + 1}, {MaxTotalBytes: MaxTotalBytes + 1}, {MaxObjectBytes: MaxObjectBytes + 1}, {MaxApplicationBytes: MaxApplicationBytes + 1}, {MaxEntries: 3}, {MaxTotalBytes: 33}} {
		var output bytes.Buffer
		called := false
		err := Write(context.Background(), &output, testSecret, entries, func(context.Context, Entry) (io.ReadCloser, error) { called = true; return nil, nil }, limits)
		if err == nil || called || output.Len() != 0 {
			t.Fatal("limit failure reached source")
		}
	}
	for _, mutate := range []func([]Entry){func(e []Entry) { e[3], e[4] = e[4], e[3] }, func(e []Entry) { e[4].ID = e[3].ID }, func(e []Entry) { e[4].ID = "a" + e[3].ID[1:] }, func(e []Entry) { e[3].ID = "../not-an-object-id" }, func(e []Entry) { e[0].ID = "forbidden" }, func(e []Entry) { e[0].Size = 33 }, func(e []Entry) { e[1].Size = 0 }} {
		copyOfEntries := append([]Entry(nil), entries...)
		mutate(copyOfEntries)
		var output bytes.Buffer
		err := Write(context.Background(), &output, testSecret, copyOfEntries, func(context.Context, Entry) (io.ReadCloser, error) {
			t.Fatal("malformed descriptors opened a source")
			return nil, nil
		})
		if err == nil || output.Len() != 0 {
			t.Fatal("malformed source descriptors accepted")
		}
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("invalid secret must not read input") }

func TestCallbackUnderconsumptionAndErrorsNeverIssueReceipt(t *testing.T) {
	encoded := archive(t, fixture([]byte("synthetic file")))
	for _, stage := range []StageFunc{func(context.Context, Entry, io.Reader) error { return nil }, func(context.Context, Entry, io.Reader) error { return errors.New("synthetic secret local path") }} {
		v, err := Read(context.Background(), bytes.NewReader(encoded), testSecret, stage)
		if err == nil || v.Valid() || strings.Contains(err.Error(), "synthetic secret local path") {
			t.Fatal("callback failure escaped or yielded authority")
		}
	}
}
