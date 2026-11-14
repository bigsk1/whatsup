package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"io"
)

const (
	maxEXIFBytes       = 1 << 20
	maxEXIFEntries     = 4096
	maxContainerChunks = 16_384
)

// Orientation is the only metadata interpreted. Offsets into other IFDs,
// thumbnails, GPS records and arbitrary tag values are never followed.
func exifOrientation(data []byte) (uint16, error) {
	if len(data) < 8 || len(data) > maxEXIFBytes {
		return 0, ErrImage
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, ErrImage
	}
	if order.Uint16(data[2:4]) != 42 {
		return 0, ErrImage
	}
	offset := uint64(order.Uint32(data[4:8]))
	if offset < 8 || offset > uint64(len(data))-2 {
		return 0, ErrImage
	}
	count := uint64(order.Uint16(data[offset : offset+2]))
	if count > maxEXIFEntries || offset+2+count*12+4 > uint64(len(data)) {
		return 0, ErrImage
	}
	orientation := uint16(1)
	found := false
	for n := range count {
		entry := data[offset+2+n*12 : offset+2+(n+1)*12]
		if order.Uint16(entry[:2]) != 0x0112 {
			continue
		}
		if found || order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			return 0, ErrImage
		}
		orientation = order.Uint16(entry[8:10])
		if orientation < 1 || orientation > 8 {
			return 0, ErrImage
		}
		found = true
	}
	return orientation, nil
}

func seekImage(ctx context.Context, src io.ReadSeeker, offset int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if actual, err := src.Seek(offset, io.SeekStart); err != nil || actual != offset {
		return ErrImage
	}
	return nil
}

func readEXIF(ctx context.Context, src io.Reader, size int64, prefix bool) (uint16, error) {
	if size < 1 || size > maxEXIFBytes {
		return 0, ErrImage
	}
	data := make([]byte, int(size))
	defer clear(data)
	if _, err := io.ReadFull(contextReader{ctx, src}, data); err != nil {
		return 0, ErrImage
	}
	if prefix && bytes.HasPrefix(data, []byte("Exif\x00\x00")) {
		data = data[6:]
	}
	return exifOrientation(data)
}

func imageOrientation(ctx context.Context, src io.ReadSeeker, format string, length int64) (uint16, error) {
	if err := seekImage(ctx, src, 0); err != nil {
		return 0, err
	}
	switch format {
	case "jpeg":
		return jpegOrientation(ctx, src, length)
	case "png":
		return pngOrientation(ctx, src, length)
	case "webp":
		return webPOrientation(ctx, src, length)
	default:
		return 1, nil
	}
}

func jpegOrientation(ctx context.Context, src io.ReadSeeker, length int64) (uint16, error) {
	var signature [2]byte
	if _, err := io.ReadFull(contextReader{ctx, src}, signature[:]); err != nil || signature != [2]byte{0xff, 0xd8} {
		return 0, ErrImage
	}
	orientation, position, found := uint16(1), int64(2), false
	for range maxContainerChunks {
		if length-position < 2 {
			return 0, ErrImage
		}
		var marker [2]byte
		if _, err := io.ReadFull(contextReader{ctx, src}, marker[:]); err != nil || marker[0] != 0xff {
			return 0, ErrImage
		}
		position += 2
		// JPEG permits fill bytes before a marker. Count each against the same
		// header budget rather than permitting an unbounded metadata walk.
		if marker[1] == 0xff {
			if err := seekImage(ctx, src, position-1); err != nil {
				return 0, err
			}
			position--
			continue
		}
		if marker[1] == 0xda || marker[1] == 0xd9 {
			return orientation, nil
		}
		if marker[1] == 0x00 || marker[1] == 0xd8 || marker[1] >= 0xd0 && marker[1] <= 0xd7 {
			return 0, ErrImage
		}
		if marker[1] == 0x01 {
			continue
		}
		var encodedSize [2]byte
		if length-position < 2 {
			return 0, ErrImage
		}
		if _, err := io.ReadFull(contextReader{ctx, src}, encodedSize[:]); err != nil {
			return 0, ErrImage
		}
		position += 2
		size := int64(binary.BigEndian.Uint16(encodedSize[:])) - 2
		if size < 0 || size > length-position {
			return 0, ErrImage
		}
		if marker[1] == 0xe1 && size >= 6 {
			var prefix [6]byte
			if _, err := io.ReadFull(contextReader{ctx, src}, prefix[:]); err != nil {
				return 0, ErrImage
			}
			if string(prefix[:]) == "Exif\x00\x00" {
				if found {
					return 0, ErrImage
				}
				var err error
				orientation, err = readEXIF(ctx, src, size-6, false)
				if err != nil {
					return 0, err
				}
				found = true
			}
		}
		position += size
		if err := seekImage(ctx, src, position); err != nil {
			return 0, err
		}
	}
	return 0, ErrImage
}

func pngOrientation(ctx context.Context, src io.ReadSeeker, length int64) (uint16, error) {
	var signature [8]byte
	if _, err := io.ReadFull(contextReader{ctx, src}, signature[:]); err != nil || string(signature[:]) != "\x89PNG\r\n\x1a\n" {
		return 0, ErrImage
	}
	orientation, position, found := uint16(1), int64(8), false
	for range maxContainerChunks {
		if length-position < 12 {
			return 0, ErrImage
		}
		var chunk [8]byte
		if _, err := io.ReadFull(contextReader{ctx, src}, chunk[:]); err != nil {
			return 0, ErrImage
		}
		position += 8
		size := int64(binary.BigEndian.Uint32(chunk[:4]))
		if size > length-position-4 {
			return 0, ErrImage
		}
		switch string(chunk[4:]) {
		case "acTL", "fcTL", "fdAT":
			return 0, ErrImage
		case "eXIf":
			if found {
				return 0, ErrImage
			}
			var err error
			orientation, err = readEXIF(ctx, src, size, false)
			if err != nil {
				return 0, err
			}
			found = true
		case "IEND":
			if size != 0 || position+4 != length {
				return 0, ErrImage
			}
			return orientation, nil
		}
		position += size + 4 // The normal decoder validates chunk CRCs.
		if err := seekImage(ctx, src, position); err != nil {
			return 0, err
		}
	}
	return 0, ErrImage
}

func webPOrientation(ctx context.Context, src io.ReadSeeker, length int64) (uint16, error) {
	var header [12]byte
	if _, err := io.ReadFull(contextReader{ctx, src}, header[:]); err != nil || string(header[:4]) != "RIFF" || string(header[8:]) != "WEBP" || int64(binary.LittleEndian.Uint32(header[4:8]))+8 != length {
		return 0, ErrImage
	}
	orientation, position, found := uint16(1), int64(12), false
	for chunks := 0; position < length && chunks < maxContainerChunks; chunks++ {
		if length-position < 8 {
			return 0, ErrImage
		}
		var chunk [8]byte
		if _, err := io.ReadFull(contextReader{ctx, src}, chunk[:]); err != nil {
			return 0, ErrImage
		}
		position += 8
		size := int64(binary.LittleEndian.Uint32(chunk[4:]))
		padded := size + size&1
		if padded > length-position {
			return 0, ErrImage
		}
		switch string(chunk[:4]) {
		case "ANIM", "ANMF":
			return 0, ErrImage
		case "VP8X":
			var extended [10]byte
			if size != 10 {
				return 0, ErrImage
			}
			if _, err := io.ReadFull(contextReader{ctx, src}, extended[:]); err != nil || extended[0]&2 != 0 {
				return 0, ErrImage
			}
		case "EXIF":
			if found {
				return 0, ErrImage
			}
			var err error
			orientation, err = readEXIF(ctx, src, size, true)
			if err != nil {
				return 0, err
			}
			found = true
		}
		position += padded
		if err := seekImage(ctx, src, position); err != nil {
			return 0, err
		}
	}
	if position != length {
		return 0, ErrImage
	}
	return orientation, nil
}

type orientedImage struct {
	image.Image
	orientation uint16
	bounds      image.Rectangle
}

func orient(source image.Image, orientation uint16) image.Image {
	if orientation == 1 {
		return source
	}
	w, h := source.Bounds().Dx(), source.Bounds().Dy()
	if orientation >= 5 {
		w, h = h, w
	}
	return orientedImage{source, orientation, image.Rect(0, 0, w, h)}
}

func (o orientedImage) Bounds() image.Rectangle { return o.bounds }

func (o orientedImage) RGBA64At(x, y int) color.RGBA64 {
	r, g, b, a := o.At(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}

func (o orientedImage) At(x, y int) color.Color {
	if !image.Pt(x, y).In(o.bounds) {
		return color.NRGBA{}
	}
	b := o.Image.Bounds()
	w, h := b.Dx(), b.Dy()
	switch o.orientation {
	case 2:
		x = w - 1 - x
	case 3:
		x, y = w-1-x, h-1-y
	case 4:
		y = h - 1 - y
	case 5:
		x, y = y, x
	case 6:
		x, y = y, h-1-x
	case 7:
		x, y = w-1-y, h-1-x
	case 8:
		x, y = w-1-y, x
	}
	return o.Image.At(x+b.Min.X, y+b.Min.Y)
}
