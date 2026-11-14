// Package media validates and re-encodes inert local images. No path, network,
// renderer, clipboard or OS opener is exposed by this package.
package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	MaxSide                = 16 << 10
	MaxPixels       uint64 = 50_000_000
	MaxInputBytes   int64  = 2 << 30
	MaxEncodedBytes        = 256 << 20
	ThumbnailSide          = 256
	KindImage              = "image"
	KindVideo              = "video"
	KindFile               = "file"
)

var ErrImage = errors.New("image is unsupported, malformed or exceeds its bounds")
var imageGate = make(chan struct{}, 1)

type Info struct {
	MediaType     string
	Width, Height int
}
type Prepared struct {
	Data, Thumbnail []byte
	MediaType       string
	Width, Height   int
}

func (Prepared) String() string               { return "[private validated WhatsUp image]" }
func (Prepared) GoString() string             { return "[private validated WhatsUp image]" }
func (Prepared) MarshalJSON() ([]byte, error) { return nil, ErrImage }

func (p Prepared) Extension() string {
	if p.MediaType == "image/jpeg" {
		return ".jpg"
	}
	return ".png"
}

func AllowedImage(mime string) bool {
	return mime == "image/png" || mime == "image/jpeg" || mime == "image/gif" || mime == "image/webp"
}

func preserve(src io.ReadSeeker, run func() error) error {
	if src == nil {
		return ErrImage
	}
	position, err := src.Seek(0, io.SeekCurrent)
	if err != nil {
		return ErrImage
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return ErrImage
	}
	defer src.Seek(position, io.SeekStart)
	return run()
}

func Sniff(src io.ReadSeeker) (kind, mediaType string, err error) {
	err = preserve(src, func() error {
		var prefix [512]byte
		n, readErr := io.ReadFull(src, prefix[:])
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return ErrImage
		}
		mediaType, _, readErr = mime.ParseMediaType(http.DetectContentType(prefix[:n]))
		if readErr != nil {
			return ErrImage
		}
		kind = KindFile
		if AllowedImage(mediaType) {
			kind = KindImage
		}
		if mediaType == "video/mp4" || mediaType == "video/webm" {
			kind = KindVideo
		}
		return nil
	})
	return
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

type contextWriter struct {
	ctx context.Context
	io.Writer
}

func (w contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.Writer.Write(p)
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func acquire(ctx context.Context) error {
	if ctx == nil {
		return ErrImage
	}
	select {
	case imageGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func decode(ctx context.Context, src io.ReadSeeker) (image.Image, Info, error) {
	var decoded image.Image
	var info Info
	err := preserve(src, func() error {
		length, err := src.Seek(0, io.SeekEnd)
		if err != nil || length < 1 || length > MaxInputBytes {
			return ErrImage
		}
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return ErrImage
		}
		config, format, err := image.DecodeConfig(contextReader{ctx, io.LimitReader(src, MaxInputBytes+1)})
		if err != nil || config.Width < 1 || config.Height < 1 || config.Width > MaxSide || config.Height > MaxSide || uint64(config.Width)*uint64(config.Height) > MaxPixels {
			return ErrImage
		}
		switch format {
		case "png":
			info.MediaType = "image/png"
		case "jpeg":
			info.MediaType = "image/jpeg"
		case "gif":
			info.MediaType = "image/gif"
		case "webp":
			info.MediaType = "image/webp"
		default:
			return ErrImage
		}
		info.Width, info.Height = config.Width, config.Height
		orientation, err := imageOrientation(ctx, src, format, length)
		if err != nil {
			return err
		}
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return ErrImage
		}
		if format == "gif" && staticGIF(contextReader{ctx, io.LimitReader(src, MaxInputBytes+1)}) != nil {
			return ErrImage
		}
		if _, err := src.Seek(0, io.SeekStart); err != nil {
			return ErrImage
		}
		decoded, format, err = image.Decode(contextReader{ctx, io.LimitReader(src, MaxInputBytes+1)})
		if err != nil || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
			return ErrImage
		}
		decoded = orient(decoded, orientation)
		info.Width, info.Height = decoded.Bounds().Dx(), decoded.Bounds().Dy()
		return ctx.Err()
	})
	if err != nil {
		return nil, Info{}, err
	}
	return decoded, info, nil
}

func Validate(ctx context.Context, src io.ReadSeeker) (Info, error) {
	if err := acquire(ctx); err != nil {
		return Info{}, err
	}
	defer func() { <-imageGate }()
	_, info, err := decode(ctx, src)
	return info, err
}

type boundedBuffer struct {
	bytes.Buffer
	maximum int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.maximum-b.Len() {
		return 0, ErrImage
	}
	return b.Buffer.Write(p)
}

func thumbnail(ctx context.Context, source image.Image) ([]byte, error) {
	width, height := source.Bounds().Dx(), source.Bounds().Dy()
	if width > ThumbnailSide || height > ThumbnailSide {
		if width >= height {
			height = max(1, height*ThumbnailSide/width)
			width = ThumbnailSide
		} else {
			width = max(1, width*ThumbnailSide/height)
			height = ThumbnailSide
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	preview := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.ApproxBiLinear.Scale(preview, preview.Bounds(), source, source.Bounds(), draw.Src, nil)
	output := boundedBuffer{maximum: 1 << 20}
	if png.Encode(contextWriter{ctx, &output}, preview) != nil {
		return nil, ErrImage
	}
	return output.Bytes(), ctx.Err()
}

func Prepare(ctx context.Context, src io.ReadSeeker) (Prepared, error) {
	if err := acquire(ctx); err != nil {
		return Prepared{}, err
	}
	defer func() { <-imageGate }()
	decoded, info, err := decode(ctx, src)
	if err != nil {
		return Prepared{}, err
	}
	output := boundedBuffer{maximum: MaxEncodedBytes}
	outputType := "image/png"
	if info.MediaType == "image/jpeg" {
		outputType = "image/jpeg"
		err = jpeg.Encode(contextWriter{ctx, &output}, decoded, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(contextWriter{ctx, &output}, decoded)
	}
	if err != nil {
		return Prepared{}, ErrImage
	}
	preview, err := thumbnail(ctx, decoded)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Data: output.Bytes(), Thumbnail: preview, MediaType: outputType, Width: info.Width, Height: info.Height}, nil
}

func Thumbnail(ctx context.Context, src io.ReadSeeker) ([]byte, error) {
	if err := acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { <-imageGate }()
	decoded, _, err := decode(ctx, src)
	if err != nil {
		return nil, err
	}
	return thumbnail(ctx, decoded)
}

func skip(r io.Reader, n int64) error { _, err := io.CopyN(io.Discard, r, n); return err }
func subBlocks(r io.Reader) error {
	var size [1]byte
	for {
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return err
		}
		if size[0] == 0 {
			return nil
		}
		if err := skip(r, int64(size[0])); err != nil {
			return err
		}
	}
}

// Count descriptors without allocating frame pixels. Decode alone accepts an
// animated GIF's first frame; DecodeAll would retain an unbounded frame list.
func staticGIF(r io.Reader) error {
	var header [13]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return ErrImage
	}
	if string(header[:6]) != "GIF87a" && string(header[:6]) != "GIF89a" {
		return ErrImage
	}
	if header[10]&0x80 != 0 {
		if skip(r, int64(3*(1<<((header[10]&7)+1)))) != nil {
			return ErrImage
		}
	}
	var marker [1]byte
	frames := 0
	for {
		if _, err := io.ReadFull(r, marker[:]); err != nil {
			return ErrImage
		}
		switch marker[0] {
		case 0x3b:
			if frames != 1 {
				return ErrImage
			}
			return nil
		case 0x21:
			if skip(r, 1) != nil || subBlocks(r) != nil {
				return ErrImage
			}
		case 0x2c:
			frames++
			if frames > 1 {
				return ErrImage
			}
			var descriptor [9]byte
			if _, err := io.ReadFull(r, descriptor[:]); err != nil {
				return ErrImage
			}
			if descriptor[8]&0x80 != 0 && skip(r, int64(3*(1<<((descriptor[8]&7)+1)))) != nil {
				return ErrImage
			}
			if skip(r, 1) != nil || subBlocks(r) != nil {
				return ErrImage
			}
		default:
			return ErrImage
		}
	}
}

func staticWebP(r io.ReadSeeker, length uint64) error {
	if length > uint64(MaxInputBytes) {
		return ErrImage
	}
	_, err := webPOrientation(context.Background(), r, int64(length))
	return err
}
