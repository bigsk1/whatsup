package media

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestVideoSniffUsesBoundedContentAndPreservesPosition(t *testing.T) {
	mp4 := binary.BigEndian.AppendUint32(nil, 24)
	mp4 = append(mp4, "ftypisom\x00\x00\x00\x00isommp42"...)
	truncated := bytes.Clone(mp4)
	binary.BigEndian.PutUint32(truncated[:4], 28)
	for _, test := range []struct {
		name, kind, mime string
		data             []byte
	}{
		{"mp4-compatible-brand", KindVideo, "video/mp4", mp4},
		{"webm-header", KindVideo, "video/webm", []byte("\x1a\x45\xdf\xa3\x87\x42\x82\x84webm")},
		{"truncated-mp4-header", KindFile, "application/octet-stream", truncated},
		{"html", KindFile, "text/html", []byte("<html>synthetic document</html>")},
		{"unknown", KindFile, "application/octet-stream", []byte{0, 1, 2, 3, 4, 5}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := &countedImageReader{Reader: bytes.NewReader(test.data)}
			_, _ = input.Seek(5, io.SeekStart)
			kind, mime, err := Sniff(input)
			if err != nil || kind != test.kind || mime != test.mime {
				t.Fatalf("kind=%q mime=%q error=%v; want kind=%q mime=%q", kind, mime, err, test.kind, test.mime)
			}
			if position, _ := input.Seek(0, io.SeekCurrent); position != 5 || input.read > 512 {
				t.Fatalf("position=%d bytes read=%d", position, input.read)
			}
		})
	}
}
