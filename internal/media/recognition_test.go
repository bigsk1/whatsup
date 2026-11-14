package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type countedImageReader struct {
	*bytes.Reader
	read int
}

func (r *countedImageReader) Read(data []byte) (int, error) {
	n, err := r.Reader.Read(data)
	r.read += n
	return n, err
}

type failedImageReader struct{}

func (failedImageReader) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }
func (failedImageReader) Seek(int64, int) (int64, error) {
	return 0, errors.New("synthetic seek failure")
}

func TestLooksLikeImageIsBoundedConservativeAndPreservesPosition(t *testing.T) {
	brand := func(major, compatible string) []byte {
		result := binary.BigEndian.AppendUint32(nil, 20)
		result = append(result, "ftyp"...)
		result = append(result, major...)
		result = append(result, 0, 0, 0, 0)
		return append(result, compatible...)
	}
	for _, test := range []struct {
		name string
		data []byte
		want bool
	}{
		{"renamed.bin", pngFixture(t, 2, 2), true},
		{"renamed.bin", []byte("BM\x00\x00\x00\x00"), true},
		{"renamed.bin", []byte("\x00\x00\x01\x00\x01\x00"), true},
		{"renamed.bin", []byte("II\x2a\x00"), true},
		{"renamed.bin", []byte("MM\x00\x2a"), true},
		{"renamed.bin", []byte("\xff\x0a"), true},
		{"renamed.bin", []byte("\x00\x00\x00\x0cJXL \r\n\x87\n"), true},
		{"renamed.bin", brand("avif", "mif1"), true},
		{"renamed.bin", brand("mif1", "heic"), true},
		{"renamed.bin", brand("isom", "avif"), true},
		{"renamed.bin", []byte("<?xml version='1.0'?><svg xmlns='http://www.w3.org/2000/svg'/>"), true},
		{"renamed.bin", brand("isom", "mp41"), false},
		{"ordinary.pdf", []byte("%PDF synthetic document"), false},
		{"ordinary.txt", []byte("synthetic text"), false},
		{"ordinary.html", []byte("<html>synthetic document</html>"), false},
		{"PHOTO.HEIC", []byte("not valid image pixels"), true},
		{"animation.APNG", nil, true},
		{"raw.DNG", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := &countedImageReader{Reader: bytes.NewReader(append(test.data, make([]byte, 4096)...))}
			_, _ = input.Seek(19, io.SeekStart)
			if got := LooksLikeImage(input, test.name); got != test.want {
				t.Fatalf("recognition got %v want %v", got, test.want)
			}
			if position, _ := input.Seek(0, io.SeekCurrent); position != 19 || input.read > 512 {
				t.Fatalf("source position=%d bytesread=%d", position, input.read)
			}
		})
	}
	if !LooksLikeImage(failedImageReader{}, "unknown.bin") || !LooksLikeImage(nil, "unknown.bin") {
		t.Fatal("failed inspection silently authorized original bytes")
	}
}
