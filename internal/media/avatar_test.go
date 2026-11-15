package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"testing"
	"time"
)

func TestAvatarSanitizesMetadataCropsSquareAndBoundsBytes(t *testing.T) {
	original := pngFixture(t, 1000, 600)
	secret := []byte("synthetic-location-avatar-metadata")
	input := append([]byte{}, original[:33]...)
	input = append(input, pngChunk("tEXt", append([]byte("Comment\x00"), secret...))...)
	input = append(input, original[33:]...)
	data, err := PrepareAvatar(context.Background(), bytes.NewReader(input))
	if err != nil || len(data) > MaxAvatarBytes || bytes.Contains(data, secret) {
		t.Fatal("avatar kept metadata or exceeded cap", err)
	}
	if err := ValidateAvatar(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != 512 || config.Height != 512 {
		t.Fatal("avatar is not bounded square")
	}
	if _, err := AvatarType(input); err == nil {
		t.Fatal("wire accepted metadata or wrong dimensions")
	}
}

func TestAvatarHighEntropyImageStillFitsStoredCap(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	r := rand.New(rand.NewSource(1234))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = byte(r.Intn(256)), byte(r.Intn(256)), byte(r.Intn(256)), 255
	}
	var input bytes.Buffer
	if err := png.Encode(&input, img); err != nil {
		t.Fatal(err)
	}
	data, err := PrepareAvatar(context.Background(), bytes.NewReader(input.Bytes()))
	if err != nil || len(data) > MaxAvatarBytes {
		t.Fatal("high entropy avatar did not honor cap", err)
	}
	if err := ValidateAvatar(context.Background(), data); err != nil {
		t.Fatal(err)
	}
}

func TestAvatarRejectsAnimationUnsupportedMalformedAndOversize(t *testing.T) {
	pal := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	var animated bytes.Buffer
	if err := gif.EncodeAll(&animated, &gif.GIF{Image: []*image.Paletted{pal, pal}, Delay: []int{1, 1}}); err != nil {
		t.Fatal(err)
	}
	png := pngFixture(t, 2, 2)
	apng := append([]byte{}, png[:33]...)
	apng = append(apng, pngChunk("acTL", []byte{0, 0, 0, 2, 0, 0, 0, 0})...)
	apng = append(apng, png[33:]...)
	for _, input := range [][]byte{[]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), animated.Bytes(), apng, []byte("broken"), make([]byte, MaxAvatarInputBytes+1)} {
		if _, err := PrepareAvatar(context.Background(), bytes.NewReader(input)); err == nil {
			t.Fatal("accepted nonstatic/unsupported/malformed/oversize avatar")
		}
	}
	if _, err := PrepareAvatar(context.Background(), nil); err == nil {
		t.Fatal("nil source")
	}
	if _, err := AvatarType(make([]byte, MaxAvatarBytes+1)); err == nil {
		t.Fatal("oversize wire avatar")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PrepareAvatar(ctx, bytes.NewReader(png)); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestAvatarWireRejectsMetadataInsideAndAfterJPEGScan(t *testing.T) {
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 512, 512)), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	if _, err := AvatarType(data); err != nil {
		t.Fatal("baseline encoder JPEG rejected", err)
	}
	comment := []byte{0xff, 0xfe, 0x00, 0x09, 'p', 'r', 'i', 'v', 'a', 't', 'e'}
	beforeEOI := append([]byte{}, data[:len(data)-2]...)
	beforeEOI = append(beforeEOI, comment...)
	beforeEOI = append(beforeEOI, 0xff, 0xd9)
	afterEOI := append([]byte{}, data...)
	afterEOI = append(afterEOI, comment...)
	afterEOI = append(afterEOI, 0xff, 0xd9)
	beforeScan := append([]byte{}, data[:2]...)
	beforeScan = append(beforeScan, comment...)
	beforeScan = append(beforeScan, data[2:]...)
	for _, bad := range [][]byte{beforeEOI, afterEOI, beforeScan} {
		if _, err := AvatarType(bad); err == nil {
			t.Fatal("avatar wire retained JPEG metadata or trailing payload")
		}
	}
}

func TestAvatarWireRejectsPNGMetadataAndBadPixelCRC(t *testing.T) {
	data, err := PrepareAvatar(context.Background(), bytes.NewReader(pngFixture(t, 2, 2)))
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte{}, data[:33]...)
	bad = append(bad, pngChunk("tEXt", []byte("Label\x00private"))...)
	bad = append(bad, data[33:]...)
	if _, err := AvatarType(bad); err == nil {
		t.Fatal("metadata PNG accepted on wire")
	}
	broken := append([]byte{}, data...)
	broken[len(broken)-13] ^= 1
	if err := ValidateAvatar(context.Background(), broken); err == nil {
		t.Fatal("corrupt PNG accepted")
	}
}

func TestAvatarReceiveDoesNotWaitForLargeAttachmentImageGate(t *testing.T) {
	data, err := PrepareAvatar(context.Background(), bytes.NewReader(pngFixture(t, 2, 2)))
	if err != nil {
		t.Fatal(err)
	}
	imageGate <- struct{}{}
	defer func() { <-imageGate }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := ValidateAvatar(ctx, data); err != nil {
		t.Fatal("bounded wire avatar waited for local large-image operation", err)
	}
}

func TestAvatarAcceptsStaticWebPAndRejectsAnimationChunks(t *testing.T) {
	source, err := os.ReadFile("testdata/static-metadata.webp")
	if err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("synthetic-private-avatar-location")
	exif := append(exifFixture(binary.LittleEndian, 6), sentinel...)
	withMetadata := withEXIF(t, source, "webp", exif)
	data, err := PrepareAvatar(context.Background(), bytes.NewReader(withMetadata))
	if err != nil || len(data) > MaxAvatarBytes || bytes.Contains(data, sentinel) {
		t.Fatal("static WebP did not sanitize into bounded avatar", err)
	}
	if err := ValidateAvatar(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"ANIM", "ANMF"} {
		animated := append([]byte{}, source...)
		animated = append(animated, kind...)
		animated = binary.LittleEndian.AppendUint32(animated, 0)
		binary.LittleEndian.PutUint32(animated[4:8], uint32(len(animated)-8))
		if _, err := PrepareAvatar(context.Background(), bytes.NewReader(animated)); err == nil {
			t.Fatal("animated WebP accepted")
		}
	}
}
