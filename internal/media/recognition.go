package media

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

// LooksLikeImage is a conservative default-send privacy guard, not validation
// or permission to preview. It never decodes pixels, reads at most 512 bytes,
// and restores the caller's position. Explicit file sends need not call it.
func LooksLikeImage(src io.ReadSeeker, name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".apng", ".jpg", ".jpeg", ".jpe", ".jfif", ".gif", ".webp",
		".bmp", ".dib", ".ico", ".cur", ".tif", ".tiff", ".heic", ".heif",
		".avif", ".avifs", ".jxl", ".jp2", ".j2k", ".jpf", ".jpx", ".svg",
		".svgz", ".raw", ".dng", ".cr2", ".cr3", ".nef", ".arw", ".orf", ".rw2",
		".heics", ".heifs", ".psd", ".xcf", ".exr", ".hdr", ".pbm", ".pgm", ".ppm",
		".pnm", ".pam", ".tga", ".pcx", ".dds":
		return true
	}
	result := false
	err := preserve(src, func() error {
		var prefix [512]byte
		n, err := io.ReadFull(src, prefix[:])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return ErrImage
		}
		data := prefix[:n]
		if strings.HasPrefix(http.DetectContentType(data), "image/") ||
			bytes.Contains(bytes.ToLower(data), []byte("<svg")) ||
			bytes.HasPrefix(data, []byte("II\x2a\x00")) || bytes.HasPrefix(data, []byte("MM\x00\x2a")) ||
			bytes.HasPrefix(data, []byte("\xff\x0a")) || bytes.HasPrefix(data, []byte("\x00\x00\x00\x0cJXL \r\n\x87\n")) ||
			bytes.HasPrefix(data, []byte("\x00\x00\x00\x0cjP  \r\n\x87\n")) {
			result = true
			return nil
		}
		if len(data) >= 12 && string(data[4:8]) == "ftyp" {
			size := uint64(binary.BigEndian.Uint32(data[:4]))
			if size >= 16 {
				end := min(uint64(len(data)), size)
				for at := uint64(8); at+4 <= end; at += 4 {
					if at == 12 { // The minor-version field is not a brand.
						continue
					}
					switch string(data[at : at+4]) {
					case "avif", "avis", "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
						result = true
						return nil
					}
				}
			}
		}
		return nil
	})
	// A failed bounded inspection must not silently authorize original bytes.
	return result || err != nil
}
