package nativefiles

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
)

// clipboardDIBPNG accepts the uncompressed 24/32-bit DIB forms used by Windows
// screenshot producers. Validate dimensions, masks, stride and length before
// allocating or indexing. Unsupported encodings fail instead of invoking tools.
func clipboardDIBPNG(data []byte) ([]byte, error) {
	if len(data) < 40 || len(data) > MaxClipboardBytes {
		return nil, ErrUnavailable
	}
	u32 := func(at int) uint32 { return binary.LittleEndian.Uint32(data[at:]) }
	header := uint64(u32(0))
	width, height := int64(int32(u32(4))), int64(int32(u32(8)))
	depth := binary.LittleEndian.Uint16(data[14:])
	compression := u32(16)
	if header != 40 && header != 108 && header != 124 || header > uint64(len(data)) ||
		width <= 0 || width > 16000 || height == 0 || height < -16000 || height > 16000 ||
		binary.LittleEndian.Uint16(data[12:]) != 1 || depth != 24 && depth != 32 ||
		compression != 0 && (compression != 3 || depth != 32) || u32(32) != 0 {
		return nil, ErrUnavailable
	}
	topDown := height < 0
	if topDown {
		height = -height
	}
	if width*height > 50000000 {
		return nil, ErrUnavailable
	}
	offset := header
	alpha := false
	if compression == 3 {
		masks := uint64(40)
		if header == 40 {
			offset += 12
		}
		if uint64(len(data)) < masks+12 || u32(int(masks)) != 0x00ff0000 || u32(int(masks+4)) != 0x0000ff00 || u32(int(masks+8)) != 0x000000ff {
			return nil, ErrUnavailable
		}
		if header >= 108 {
			mask := u32(52)
			if mask != 0 && mask != 0xff000000 {
				return nil, ErrUnavailable
			}
			alpha = mask != 0
		}
	}
	stride := (uint64(width)*uint64(depth) + 31) / 32 * 4
	if offset > uint64(len(data)) || uint64(height)*stride > uint64(len(data))-offset {
		return nil, ErrUnavailable
	}
	result := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	for y := int64(0); y < height; y++ {
		sourceY := height - 1 - y
		if topDown {
			sourceY = y
		}
		row := offset + uint64(sourceY)*stride
		for x := int64(0); x < width; x++ {
			at := int(row + uint64(x)*uint64(depth/8))
			out := int(y)*result.Stride + int(x)*4
			result.Pix[out], result.Pix[out+1], result.Pix[out+2], result.Pix[out+3] = data[at+2], data[at+1], data[at], 255
			if alpha {
				result.Pix[out+3] = data[at+3]
			}
		}
	}
	var encoded bytes.Buffer
	if png.Encode(&encoded, result) != nil || encoded.Len() > MaxClipboardBytes {
		return nil, ErrUnavailable
	}
	return encoded.Bytes(), nil
}
