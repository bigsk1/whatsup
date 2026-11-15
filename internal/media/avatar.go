package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"

	"golang.org/x/image/draw"
)

const (
	AvatarSide                = 512
	MaxAvatarBytes            = 64 << 10
	MaxAvatarInputBytes int64 = 16 << 20
)

// PrepareAvatar uses the attachment decoder's dimension, static-image and EXIF
// checks, then center-crops and re-encodes a bounded square. No original metadata
// or format-specific bytes survive. GIF is deliberately excluded for profiles.
func PrepareAvatar(ctx context.Context, src io.ReadSeeker) ([]byte, error) {
	if src == nil {
		return nil, ErrImage
	}
	if err := acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-imageGate }()
	length, err := src.Seek(0, io.SeekEnd)
	if err != nil || length < 1 || length > MaxAvatarInputBytes {
		return nil, ErrImage
	}
	decoded, info, err := decode(ctx, src)
	if err != nil || info.MediaType != "image/png" && info.MediaType != "image/jpeg" && info.MediaType != "image/webp" {
		return nil, ErrImage
	}
	bounds := decoded.Bounds()
	side := min(bounds.Dx(), bounds.Dy())
	x, y := bounds.Min.X+(bounds.Dx()-side)/2, bounds.Min.Y+(bounds.Dy()-side)/2
	target := image.NewNRGBA(image.Rect(0, 0, AvatarSide, AvatarSide))
	draw.ApproxBiLinear.Scale(target, target.Bounds(), decoded, image.Rect(x, y, x+side, y+side), draw.Src, nil)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var encoded boundedBuffer
	encoded.maximum = MaxAvatarBytes
	if png.Encode(contextWriter{ctx, &encoded}, target) == nil {
		return bytes.Clone(encoded.Bytes()), ctx.Err()
	}
	// Preserve a strict stored-size cap even for high-entropy photographs. Flatten
	// alpha before JPEG rather than retaining source metadata or huge PNG data.
	flat := image.NewRGBA(target.Bounds())
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), target, image.Point{}, draw.Over)
	for _, quality := range []int{85, 70, 50, 30, 15} {
		encoded.Reset()
		if jpeg.Encode(contextWriter{ctx, &encoded}, flat, &jpeg.Options{Quality: quality}) == nil {
			return bytes.Clone(encoded.Bytes()), ctx.Err()
		}
	}
	return nil, ErrImage
}

// AvatarType accepts only bounded inert wire/storage images without ancillary
// metadata. Full decoding occurs once on receipt; ordinary envelope validation
// only calls this structural check, avoiding 33 image decodes on every save.
func AvatarType(data []byte) (string, error) {
	if len(data) < 1 || len(data) > MaxAvatarBytes {
		return "", ErrImage
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != AvatarSide || config.Height != AvatarSide {
		return "", ErrImage
	}
	if format == "png" {
		position := 8
		for chunks := 0; chunks < maxContainerChunks && position < len(data); chunks++ {
			if len(data)-position < 12 {
				return "", ErrImage
			}
			size := uint64(binary.BigEndian.Uint32(data[position:]))
			if size > uint64(len(data)-position-12) {
				return "", ErrImage
			}
			kind := string(data[position+4 : position+8])
			if kind != "IHDR" && kind != "IDAT" && kind != "IEND" {
				return "", ErrImage
			}
			position += int(size) + 12
			if kind == "IEND" {
				if size != 0 || position != len(data) {
					return "", ErrImage
				}
				return "image/png", nil
			}
		}
	}
	if format == "jpeg" {
		position := 2
		for chunks := 0; chunks < maxContainerChunks && position < len(data); chunks++ {
			if len(data)-position < 4 || data[position] != 0xff {
				return "", ErrImage
			}
			marker := data[position+1]
			if marker == 0xda {
				size := int(binary.BigEndian.Uint16(data[position+2:]))
				if size < 2 || size > len(data)-position-2 {
					return "", ErrImage
				}
				position += 2 + size
				for position < len(data) {
					if data[position] != 0xff {
						position++
						continue
					}
					if len(data)-position < 2 {
						return "", ErrImage
					}
					next := data[position+1]
					if next == 0x00 || next >= 0xd0 && next <= 0xd7 {
						position += 2
						continue
					}
					if next == 0xd9 && position+2 == len(data) {
						return "image/jpeg", nil
					}
					return "", ErrImage
				}
				return "", ErrImage
			}
			if marker != 0xdb && marker != 0xc0 && marker != 0xc4 {
				return "", ErrImage
			}
			size := int(binary.BigEndian.Uint16(data[position+2:]))
			if size < 2 || size > len(data)-position-2 {
				return "", ErrImage
			}
			position += 2 + size
		}
	}
	return "", ErrImage
}

func ValidateAvatar(ctx context.Context, data []byte) error {
	if _, err := AvatarType(data); err != nil {
		return err
	}
	if ctx == nil {
		return ErrImage
	}
	// Structural validation fixes this decode at 512x512 and 64 KiB. It does
	// not queue behind a potentially large local attachment-image operation,
	// which would otherwise stall authenticated chat/heartbeat processing.
	_, _, err := decode(ctx, bytes.NewReader(data))
	return err
}
