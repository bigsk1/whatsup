package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"testing"
)

func pngFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{R: byte(x), G: byte(y), B: 71, A: 255})
		}
	}
	var b bytes.Buffer
	if png.Encode(&b, img) != nil {
		t.Fatal("encode")
	}
	return b.Bytes()
}
func pngChunk(name string, body []byte) []byte {
	result := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	result = append(result, name...)
	result = append(result, body...)
	return binary.BigEndian.AppendUint32(result, crc32.ChecksumIEEE(result[4:]))
}

func TestPrepareReencodesAndStripsMetadata(t *testing.T) {
	original := pngFixture(t, 900, 500)
	metadata := []byte("synthetic-location-secret-metadata")
	source := append([]byte{}, original[:33]...)
	source = append(source, pngChunk("tEXt", append([]byte("Comment\x00"), metadata...))...)
	source = append(source, original[33:]...)
	reader := bytes.NewReader(source)
	reader.Seek(8, io.SeekStart)
	prepared, err := Prepare(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	if position, _ := reader.Seek(0, io.SeekCurrent); position != 8 {
		t.Fatal("reader position changed")
	}
	if bytes.Contains(prepared.Data, metadata) || bytes.Contains(prepared.Thumbnail, metadata) {
		t.Fatal("metadata survived reencoding")
	}
	if prepared.MediaType != "image/png" || prepared.Width != 900 || prepared.Height != 500 {
		t.Fatal("dimensions changed")
	}
	thumbnail, err := png.DecodeConfig(bytes.NewReader(prepared.Thumbnail))
	if err != nil || thumbnail.Width != 256 || thumbnail.Height != 142 {
		t.Fatal("thumbnail bounds")
	}
	if _, err := Validate(context.Background(), bytes.NewReader(prepared.Data)); err != nil {
		t.Fatal(err)
	}
}

func TestStaticWebPReencodesPixelsAndRemovesMetadata(t *testing.T) {
	// Our 258-byte fixture contains only synthetic pixels and metadata. It was
	// generated offline with Pillow 10.4.0/libwebp 1.4.0. Reproduction in Python:
	// from PIL import Image
	// im = Image.new('RGB', (384, 192))
	// colors = [(255,0,0), (0,255,0), (0,0,255), (255,255,255)]
	// for y in range(192):
	//     for x in range(384):
	//         im.putpixel((x,y), colors[(x >= 192) + 2 * (y >= 96)])
	// exif = Image.Exif(); exif[270] = 'WhatsUp synthetic EXIF location sentinel'
	// xmp = b'<x:xmpmeta xmlns:x="adobe:ns:meta/">WhatsUp synthetic XMP sentinel</x:xmpmeta>'
	// im.save('testdata/static-metadata.webp', 'WEBP', lossless=True, method=6, exif=exif, xmp=xmp)
	// The committed bytes make the test independent of any installed encoder.
	source, err := os.ReadFile("testdata/static-metadata.webp")
	if err != nil {
		t.Fatal(err)
	}
	markers := [][]byte{[]byte("WhatsUp synthetic EXIF location sentinel"), []byte("WhatsUp synthetic XMP sentinel")}
	for _, marker := range markers {
		if !bytes.Contains(source, marker) {
			t.Fatal("WebP fixture lost its metadata sentinel")
		}
	}
	kind, mime, err := Sniff(bytes.NewReader(source))
	if err != nil || kind != KindImage || mime != "image/webp" {
		t.Fatalf("static WebP sniff: kind=%q MIME=%q error=%v", kind, mime, err)
	}
	info, err := Validate(context.Background(), bytes.NewReader(source))
	if err != nil || info.MediaType != "image/webp" || info.Width != 384 || info.Height != 192 {
		t.Fatalf("static WebP validation: info=%+v error=%v", info, err)
	}
	prepared, err := Prepare(context.Background(), bytes.NewReader(source))
	if err != nil || prepared.MediaType != "image/png" || prepared.Width != 384 || prepared.Height != 192 {
		t.Fatalf("static WebP preparation: MIME=%q dimensions=%dx%d error=%v", prepared.MediaType, prepared.Width, prepared.Height, err)
	}
	for _, output := range []struct {
		name          string
		data          []byte
		width, height int
	}{
		{"full image", prepared.Data, 384, 192},
		{"thumbnail", prepared.Thumbnail, 256, 128},
	} {
		t.Run(output.name, func(t *testing.T) {
			for _, marker := range markers {
				if bytes.Contains(output.data, marker) {
					t.Fatal("WebP metadata survived image reencoding")
				}
			}
			kind, mime, err := Sniff(bytes.NewReader(output.data))
			if err != nil || kind != KindImage || mime != "image/png" {
				t.Fatalf("reencoded output sniff: kind=%q MIME=%q error=%v", kind, mime, err)
			}
			decoded, format, err := image.Decode(bytes.NewReader(output.data))
			if err != nil || format != "png" {
				t.Fatalf("reencoded output format=%q error=%v", format, err)
			}
			if decoded.Bounds() != image.Rect(0, 0, output.width, output.height) {
				t.Fatal("reencoded image dimensions changed")
			}
			for _, sample := range []struct {
				x, y int
				want color.RGBA
			}{
				{0, 0, color.RGBA{R: 255, A: 255}},
				{output.width - 1, 0, color.RGBA{G: 255, A: 255}},
				{0, output.height - 1, color.RGBA{B: 255, A: 255}},
				{output.width - 1, output.height - 1, color.RGBA{R: 255, G: 255, B: 255, A: 255}},
			} {
				if got := color.RGBAModel.Convert(decoded.At(sample.x, sample.y)).(color.RGBA); got != sample.want {
					t.Fatalf("synthetic pixel (%d,%d): got=%v want=%v", sample.x, sample.y, got, sample.want)
				}
			}
		})
	}
}

func TestSupportedStaticImagesAndSniff(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	var jpg bytes.Buffer
	if jpeg.Encode(&jpg, img, nil) != nil {
		t.Fatal("jpeg")
	}
	pal := image.NewPaletted(image.Rect(0, 0, 4, 3), color.Palette{color.Black, color.White})
	var static bytes.Buffer
	if gif.Encode(&static, pal, nil) != nil {
		t.Fatal("gif")
	}
	for _, data := range [][]byte{pngFixture(t, 3, 2), jpg.Bytes(), static.Bytes()} {
		if _, err := Validate(context.Background(), bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		kind, mime, err := Sniff(bytes.NewReader(data))
		if err != nil || kind != KindImage || !AllowedImage(mime) {
			t.Fatal("static image not sniffed")
		}
	}
	for _, data := range [][]byte{[]byte("<html>active</html>"), []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), []byte("%PDF synthetic document")} {
		kind, _, err := Sniff(bytes.NewReader(data))
		if err != nil || kind != KindFile {
			t.Fatal("active/non-image type previewable")
		}
		if _, err := Validate(context.Background(), bytes.NewReader(data)); err == nil {
			t.Fatal("non-image decoded")
		}
	}
}
func TestBoundsAnimationAndTruncationRejectBeforeExposure(t *testing.T) {
	for _, dimensions := range [][2]uint32{{MaxSide + 1, 1}, {1, MaxSide + 1}, {8000, 8000}, {0, 1}} {
		header := append([]byte{}, pngFixture(t, 1, 1)...)
		binary.BigEndian.PutUint32(header[16:20], dimensions[0])
		binary.BigEndian.PutUint32(header[20:24], dimensions[1])
		binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))
		if _, err := Validate(context.Background(), bytes.NewReader(header)); err == nil {
			t.Fatal("oversized or empty image accepted")
		}
	}
	pal := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	var animation bytes.Buffer
	if gif.EncodeAll(&animation, &gif.GIF{Image: []*image.Paletted{pal, pal}, Delay: []int{1, 1}}) != nil {
		t.Fatal("gif")
	}
	if _, err := Validate(context.Background(), bytes.NewReader(animation.Bytes())); err == nil {
		t.Fatal("animated GIF accepted")
	}
	pngData := pngFixture(t, 2, 2)
	if _, err := Validate(context.Background(), bytes.NewReader(pngData[:len(pngData)-8])); err == nil {
		t.Fatal("truncated PNG accepted")
	}
	var webp bytes.Buffer
	webp.WriteString("RIFF")
	binary.Write(&webp, binary.LittleEndian, uint32(22))
	webp.WriteString("WEBPVP8X")
	binary.Write(&webp, binary.LittleEndian, uint32(10))
	webp.Write([]byte{2, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if staticWebP(bytes.NewReader(webp.Bytes()), uint64(webp.Len())) == nil {
		t.Fatal("animated WebP accepted")
	}
}
func TestCancellationAndEncodingBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prepare(ctx, bytes.NewReader(pngFixture(t, 2, 2))); err == nil {
		t.Fatal("cancelled preparation succeeded")
	}
	buffer := boundedBuffer{maximum: 3}
	if _, err := buffer.Write([]byte("four")); err == nil || buffer.Len() != 0 {
		t.Fatal("encode bound bypassed")
	}
	if _, err := Prepare(nil, bytes.NewReader(nil)); err == nil {
		t.Fatal("nil context accepted")
	}
}
