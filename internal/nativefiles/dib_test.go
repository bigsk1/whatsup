package nativefiles

import (
	"bytes"
	"context"
	"encoding/binary"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func testDIB(width, height int32, depth uint16) []byte {
	stride := (uint64(width)*uint64(depth) + 31) / 32 * 4
	absHeight := int64(height)
	if absHeight < 0 {
		absHeight = -absHeight
	}
	data := make([]byte, 40+stride*uint64(absHeight))
	binary.LittleEndian.PutUint32(data, 40)
	binary.LittleEndian.PutUint32(data[4:], uint32(width))
	binary.LittleEndian.PutUint32(data[8:], uint32(height))
	binary.LittleEndian.PutUint16(data[12:], 1)
	binary.LittleEndian.PutUint16(data[14:], depth)
	return data
}

func TestClipboardDIBStrideOrientationAndNoMetadata(t *testing.T) {
	for _, height := range []int32{2, -2} {
		for _, depth := range []uint16{24, 32} {
			data := testDIB(1, height, depth)
			// Both forms use a four-byte row, including 24-bit padding.
			copy(data[40:], []byte{0, 0, 255, 0, 255, 0, 0, 0})
			encoded, err := clipboardDIBPNG(data)
			if err != nil {
				t.Fatal(err)
			}
			image, err := png.Decode(bytes.NewReader(encoded))
			if err != nil || image.Bounds().Dx() != 1 || image.Bounds().Dy() != 2 {
				t.Fatal("invalid result")
			}
			r, _, b, a := image.At(0, 0).RGBA()
			if a != 65535 || height > 0 && b != 65535 || height < 0 && r != 65535 {
				t.Fatal("wrong orientation or alpha")
			}
		}
	}
}

func TestClipboardDIBRejectsMalformedBeforeAllocation(t *testing.T) {
	valid := testDIB(1, 1, 32)
	for _, mutate := range []func([]byte){
		func(d []byte) { binary.LittleEndian.PutUint32(d, 0xffffffff) },
		func(d []byte) { binary.LittleEndian.PutUint32(d[4:], 0x80000000) },
		func(d []byte) { binary.LittleEndian.PutUint32(d[8:], 0x80000000) },
		func(d []byte) { binary.LittleEndian.PutUint32(d[4:], 16001) },
		func(d []byte) { binary.LittleEndian.PutUint32(d[16:], 1) },
		func(d []byte) { binary.LittleEndian.PutUint32(d[32:], 256) },
		func(d []byte) { binary.LittleEndian.PutUint16(d[14:], 16) },
	} {
		data := append([]byte(nil), valid...)
		mutate(data)
		if _, err := clipboardDIBPNG(data); err == nil {
			t.Fatal("accepted malformed DIB")
		}
	}
	if _, err := clipboardDIBPNG(valid[:len(valid)-1]); err == nil {
		t.Fatal("accepted truncated pixels")
	}
	oversized := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(oversized[4:], 16000)
	binary.LittleEndian.PutUint32(oversized[8:], 16000)
	if _, err := clipboardDIBPNG(oversized); err == nil {
		t.Fatal("accepted too many pixels")
	}
}

func TestRevealRejectsUntrustedOrMissingPathsBeforeDesktopCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	regular := filepath.Join(root, "saved.pdf")
	if err := os.WriteFile(regular, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "relative.pdf", root, filepath.Join(root, "missing.pdf"), regular} {
		if ShowInFolder(ctx, path) == nil {
			t.Fatal("cancelled action reached desktop")
		}
	}
	for _, path := range []string{"relative.pdf", root, filepath.Join(root, "missing.pdf")} {
		if ShowInFolder(context.Background(), path) == nil {
			t.Fatal("invalid path reached desktop")
		}
	}
}
