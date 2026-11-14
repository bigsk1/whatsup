package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"testing"
)

var quadrantColors = []color.NRGBA{
	{R: 235, G: 15, B: 15, A: 255}, {R: 15, G: 235, B: 15, A: 255},
	{R: 15, G: 15, B: 235, A: 255}, {R: 235, G: 235, B: 235, A: 255},
}

func exifFixture(order binary.ByteOrder, orientation uint16) []byte {
	data := make([]byte, 26)
	if order == binary.LittleEndian {
		copy(data, "II")
	} else {
		copy(data, "MM")
	}
	order.PutUint16(data[2:], 42)
	order.PutUint32(data[4:], 8)
	order.PutUint16(data[8:], 1)
	order.PutUint16(data[10:], 0x0112)
	order.PutUint16(data[12:], 3)
	order.PutUint32(data[14:], 1)
	order.PutUint16(data[18:], orientation)
	return data
}

func quadrantFixture(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 80, 48))
	for y := range 48 {
		for x := range 80 {
			n := 0
			if x >= 40 {
				n++
			}
			if y >= 24 {
				n += 2
			}
			img.SetNRGBA(x, y, quadrantColors[n])
		}
	}
	var out bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 100})
	} else {
		err = png.Encode(&out, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func withEXIF(t *testing.T, source []byte, format string, exif []byte) []byte {
	t.Helper()
	switch format {
	case "jpeg":
		result := append([]byte{}, source[:2]...)
		result = append(result, 0xff, 0xe1)
		result = binary.BigEndian.AppendUint16(result, uint16(len(exif)+8))
		result = append(result, "Exif\x00\x00"...)
		result = append(result, exif...)
		return append(result, source[2:]...)
	case "png":
		result := append([]byte{}, source[:33]...)
		result = append(result, pngChunk("eXIf", exif)...)
		return append(result, source[33:]...)
	case "webp":
		result := append([]byte{}, source[:12]...)
		for at := 12; at < len(source); {
			size := int(binary.LittleEndian.Uint32(source[at+4 : at+8]))
			end := at + 8 + size + size&1
			if string(source[at:at+4]) != "EXIF" {
				result = append(result, source[at:end]...)
			}
			at = end
		}
		result = append(result, "EXIF"...)
		result = binary.LittleEndian.AppendUint32(result, uint32(len(exif)))
		result = append(result, exif...)
		if len(exif)&1 != 0 {
			result = append(result, 0)
		}
		binary.LittleEndian.PutUint32(result[4:8], uint32(len(result)-8))
		return result
	default:
		t.Fatal("unsupported test format")
		return nil
	}
}

func assertCorners(t *testing.T, data []byte, want []int, palette []color.NRGBA, tolerance int) {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	for n, point := range []image.Point{{0, 0}, {b.Dx() - 1, 0}, {0, b.Dy() - 1}, {b.Dx() - 1, b.Dy() - 1}} {
		got := color.NRGBAModel.Convert(img.At(point.X, point.Y)).(color.NRGBA)
		expected := palette[want[n]]
		for _, delta := range []int{int(got.R) - int(expected.R), int(got.G) - int(expected.G), int(got.B) - int(expected.B), int(got.A) - int(expected.A)} {
			if delta < -tolerance || delta > tolerance {
				t.Fatalf("corner %d: got %v want %v", n, got, expected)
			}
		}
	}
}

func TestEXIFOrientationAllTransformsAndByteOrders(t *testing.T) {
	wants := [][]int{{0, 1, 2, 3}, {1, 0, 3, 2}, {3, 2, 1, 0}, {2, 3, 0, 1}, {0, 2, 1, 3}, {2, 0, 3, 1}, {3, 1, 2, 0}, {1, 3, 0, 2}}
	webp, err := os.ReadFile("testdata/static-metadata.webp")
	if err != nil {
		t.Fatal(err)
	}
	webpPalette := []color.NRGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}, {R: 255, G: 255, B: 255, A: 255}}
	for _, format := range []string{"jpeg", "png", "webp"} {
		source, palette, width, height := webp, webpPalette, 384, 192
		if format != "webp" {
			source, palette, width, height = quadrantFixture(t, format), quadrantColors, 80, 48
		}
		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			for orientation := uint16(1); orientation <= 8; orientation++ {
				t.Run(fmt.Sprintf("%s/%s/%d", format, order, orientation), func(t *testing.T) {
					metadata := append(exifFixture(order, orientation), []byte("synthetic-private-EXIF-sentinel")...)
					input := withEXIF(t, source, format, metadata)
					reader := bytes.NewReader(input)
					_, _ = reader.Seek(7, io.SeekStart)
					prepared, err := Prepare(context.Background(), reader)
					if err != nil {
						t.Fatal(err)
					}
					if position, _ := reader.Seek(0, io.SeekCurrent); position != 7 {
						t.Fatal("preparation did not preserve source position")
					}
					w, h := width, height
					if orientation >= 5 {
						w, h = h, w
					}
					if prepared.Width != w || prepared.Height != h {
						t.Fatalf("oriented dimensions got %dx%d want %dx%d", prepared.Width, prepared.Height, w, h)
					}
					if bytes.Contains(prepared.Data, []byte("synthetic-private-EXIF-sentinel")) || bytes.Contains(prepared.Thumbnail, []byte("synthetic-private-EXIF-sentinel")) {
						t.Fatal("EXIF metadata survived")
					}
					tolerance := 0
					if format == "jpeg" {
						tolerance = 24
						if prepared.MediaType != "image/jpeg" || prepared.Extension() != ".jpg" {
							t.Fatal("JPEG was not kept efficient")
						}
					} else if prepared.MediaType != "image/png" || prepared.Extension() != ".png" {
						t.Fatal("lossless image was not PNG")
					}
					assertCorners(t, prepared.Data, wants[orientation-1], palette, tolerance)
					assertCorners(t, prepared.Thumbnail, wants[orientation-1], palette, tolerance)
					thumbnail, err := Thumbnail(context.Background(), bytes.NewReader(input))
					if err != nil {
						t.Fatal(err)
					}
					assertCorners(t, thumbnail, wants[orientation-1], palette, tolerance)
					info, err := Validate(context.Background(), bytes.NewReader(input))
					if err != nil || info.Width != w || info.Height != h {
						t.Fatal("validation did not report oriented bounds")
					}
				})
			}
		}
	}
}

func TestMalformedEXIFRejectedWithoutFollowingOtherIFDs(t *testing.T) {
	valid := exifFixture(binary.LittleEndian, 6)
	for name, mutate := range map[string]func([]byte) []byte{
		"truncated":         func(b []byte) []byte { return b[:7] },
		"endian":            func(b []byte) []byte { b[0] = 'X'; return b },
		"magic":             func(b []byte) []byte { b[2] = 0; return b },
		"overflow-offset":   func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:], ^uint32(0)); return b },
		"header-offset":     func(b []byte) []byte { binary.LittleEndian.PutUint32(b[4:], 2); return b },
		"entry-count":       func(b []byte) []byte { binary.LittleEndian.PutUint16(b[8:], maxEXIFEntries+1); return b },
		"orientation-type":  func(b []byte) []byte { b[12] = 4; return b },
		"orientation-count": func(b []byte) []byte { b[14] = 2; return b },
		"orientation-zero":  func(b []byte) []byte { b[18] = 0; return b },
		"orientation-nine":  func(b []byte) []byte { b[18] = 9; return b },
		"duplicate": func(b []byte) []byte {
			binary.LittleEndian.PutUint16(b[8:], 2)
			return append(append(b[:22:22], b[10:22]...), make([]byte, 4)...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := mutate(append([]byte{}, valid...))
			if _, err := exifOrientation(bad); err == nil {
				t.Fatal("malformed EXIF accepted")
			}
			for _, format := range []string{"jpeg", "png"} {
				input := withEXIF(t, quadrantFixture(t, format), format, bad)
				if _, err := Prepare(context.Background(), bytes.NewReader(input)); err == nil {
					t.Fatal("image with malformed EXIF accepted")
				}
			}
		})
	}
	// Unrelated metadata offsets and the next-IFD pointer are inert. Only IFD0's
	// inline orientation is read; metadata is discarded by the output encoder.
	valid[22], valid[23], valid[24], valid[25] = 0xff, 0xff, 0xff, 0xff
	if orientation, err := exifOrientation(valid); err != nil || orientation != 6 {
		t.Fatal("parser followed unrelated IFD pointer")
	}
	if _, err := exifOrientation(make([]byte, maxEXIFBytes+1)); err == nil {
		t.Fatal("oversized metadata accepted")
	}
}

func TestPNGAlphaAndAnimatedPNGPolicy(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 90, G: 45, B: 180, A: 120})
	img.SetNRGBA(1, 0, color.NRGBA{R: 15, G: 200, B: 20, A: 255})
	var source bytes.Buffer
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(context.Background(), bytes.NewReader(source.Bytes()))
	if err != nil || prepared.MediaType != "image/png" {
		t.Fatal("alpha image preparation failed")
	}
	decoded, err := png.Decode(bytes.NewReader(prepared.Data))
	if err != nil || color.NRGBAModel.Convert(decoded.At(0, 0)) != img.NRGBAAt(0, 0) {
		t.Fatal("alpha pixels changed")
	}
	for _, chunk := range []string{"acTL", "fcTL", "fdAT"} {
		animated := append([]byte{}, source.Bytes()[:33]...)
		animated = append(animated, pngChunk(chunk, make([]byte, 8))...)
		animated = append(animated, source.Bytes()[33:]...)
		if _, err := Prepare(context.Background(), bytes.NewReader(animated)); err == nil {
			t.Fatalf("animated PNG %s accepted", chunk)
		}
		if _, err := Thumbnail(context.Background(), bytes.NewReader(animated)); err == nil {
			t.Fatalf("animated PNG %s thumbnail accepted", chunk)
		}
	}
}

func TestJPEGPhotoRemainsSmallerThanLosslessPNG(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 512, 384))
	state := uint32(7)
	for y := range 384 {
		for x := range 512 {
			state = state*1664525 + 1013904223
			noise := int(state>>28) - 8
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(60 + x/4 + noise), G: uint8(50 + y/3 + noise), B: uint8(70 + (x+y)/8 + noise), A: 255})
		}
	}
	var source bytes.Buffer
	if err := jpeg.Encode(&source, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatal(err)
	}
	input := withEXIF(t, source.Bytes(), "jpeg", append(exifFixture(binary.LittleEndian, 1), "synthetic-photo-location"...))
	prepared, err := Prepare(context.Background(), bytes.NewReader(input))
	if err != nil || prepared.MediaType != "image/jpeg" {
		t.Fatal("JPEG preparation failed")
	}
	decoded, err := jpeg.Decode(bytes.NewReader(source.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var lossless bytes.Buffer
	if err := png.Encode(&lossless, decoded); err != nil {
		t.Fatal(err)
	}
	if len(prepared.Data)*2 >= lossless.Len() {
		t.Fatalf("photo JPEG=%d bytes PNG=%d bytes; expected JPEG below half", len(prepared.Data), lossless.Len())
	}
	if bytes.Contains(prepared.Data, []byte("synthetic-photo-location")) {
		t.Fatal("JPEG metadata retained")
	}
	t.Logf("synthetic photo: sanitized JPEG %d bytes; prior lossless PNG %d bytes", len(prepared.Data), lossless.Len())
}

func TestContainerMetadataBoundsAndDuplicates(t *testing.T) {
	valid := exifFixture(binary.LittleEndian, 1)
	for _, format := range []string{"jpeg", "png"} {
		source := quadrantFixture(t, format)
		duplicate := withEXIF(t, withEXIF(t, source, format, valid), format, valid)
		if _, err := Prepare(context.Background(), bytes.NewReader(duplicate)); err == nil {
			t.Fatalf("%s accepted duplicate EXIF", format)
		}
	}
	pngSource := quadrantFixture(t, "png")
	oversized := withEXIF(t, pngSource, "png", make([]byte, maxEXIFBytes+1))
	if _, err := Prepare(context.Background(), bytes.NewReader(oversized)); err == nil {
		t.Fatal("oversized PNG EXIF accepted")
	}
	invalidLength := withEXIF(t, pngSource, "png", valid)
	binary.BigEndian.PutUint32(invalidLength[33:37], ^uint32(0))
	if _, err := Prepare(context.Background(), bytes.NewReader(invalidLength)); err == nil {
		t.Fatal("out-of-file PNG metadata length accepted")
	}
	jpegSource := quadrantFixture(t, "jpeg")
	segments := append([]byte{}, jpegSource[:2]...)
	for range maxContainerChunks {
		segments = append(segments, 0xff, 0xe0, 0, 2)
	}
	segments = append(segments, jpegSource[2:]...)
	if _, err := Prepare(context.Background(), bytes.NewReader(segments)); err == nil {
		t.Fatal("unbounded JPEG metadata walk accepted")
	}
	webp, err := os.ReadFile("testdata/static-metadata.webp")
	if err != nil {
		t.Fatal(err)
	}
	withPrefix := withEXIF(t, webp, "webp", append([]byte("Exif\x00\x00"), valid...))
	if _, err := Prepare(context.Background(), bytes.NewReader(withPrefix)); err != nil {
		t.Fatal("WebP optional EXIF prefix rejected", err)
	}
	badRIFF := append([]byte{}, webp...)
	binary.LittleEndian.PutUint32(badRIFF[4:8], uint32(len(badRIFF)))
	if _, err := Prepare(context.Background(), bytes.NewReader(badRIFF)); err == nil {
		t.Fatal("inconsistent WebP container length accepted")
	}
	oversizedWebP := withEXIF(t, webp, "webp", make([]byte, maxEXIFBytes+1))
	if _, err := Prepare(context.Background(), bytes.NewReader(oversizedWebP)); err == nil {
		t.Fatal("oversized WebP EXIF accepted")
	}
}

func TestOrientationPreservesAlphaAndNonzeroSourceBounds(t *testing.T) {
	img := image.NewNRGBA(image.Rect(4, 7, 6, 8))
	img.SetNRGBA(4, 7, color.NRGBA{R: 90, G: 45, B: 180, A: 120})
	img.SetNRGBA(5, 7, color.NRGBA{R: 15, G: 200, B: 20, A: 255})
	rotated := orient(img, 6)
	if rotated.Bounds() != image.Rect(0, 0, 1, 2) || rotated.At(0, 0) != img.At(4, 7) || rotated.At(0, 1) != img.At(5, 7) {
		t.Fatal("rotation lost alpha pixels or source bounds")
	}
	if got := color.NRGBAModel.Convert(rotated.At(-1, -1)).(color.NRGBA); got.A != 0 {
		t.Fatal("out-of-bounds orientation access exposed a pixel")
	}
	var out bytes.Buffer
	if err := png.Encode(&out, rotated); err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(out.Bytes()))
	if err != nil || color.NRGBAModel.Convert(decoded.At(0, 0)) != img.NRGBAAt(4, 7) {
		t.Fatal("oriented PNG changed alpha pixels")
	}
}
