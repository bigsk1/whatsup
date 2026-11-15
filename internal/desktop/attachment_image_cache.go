package desktop

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"whatsup/internal/media"
)

const (
	attachmentImageCacheEntries = 64
	attachmentImageCacheBytes   = 16 << 20
)

var errAttachmentImageRetired = errors.New("attachment image cache generation retired")

type attachmentImage struct {
	data []byte
	mime string
}

type cachedAttachmentImage struct {
	image attachmentImage
	err   error
	used  uint64
}

// Each unlocked session owns a bounded cache of sanitized images, including
// invalid-image results. Never share response bytes with cache invalidation.
type attachmentImageCache struct {
	mu       sync.Mutex
	entries  map[attachmentReference]cachedAttachmentImage
	building map[attachmentReference]chan struct{}
	bytes    int
	clock    uint64
	epoch    atomic.Uint64
}

func newAttachmentImageCache() *attachmentImageCache {
	return &attachmentImageCache{entries: map[attachmentReference]cachedAttachmentImage{}, building: map[attachmentReference]chan struct{}{}}
}

func (c *attachmentImageCache) invalidate(contactID, transferID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch.Add(1) // In-flight results from the retired generation cannot repopulate.
	for key, value := range c.entries {
		if contactID == "" || key.contactID == contactID && (transferID == "" || key.transferID == transferID) {
			clear(value.image.data)
			c.bytes -= len(value.image.data)
			delete(c.entries, key)
		}
	}
}

func (c *attachmentImageCache) load(ctx context.Context, key attachmentReference, build func() (attachmentImage, error)) (attachmentImage, error) {
	// Keep the generation of this invocation across mutex and coalesced-build
	// waits. A waiter must not acquire a newer generation by looping after the
	// build it joined was invalidated. The scalar never retains retired IDs.
	epoch := c.epoch.Load()
	for {
		if err := ctx.Err(); err != nil {
			return attachmentImage{}, err
		}
		c.mu.Lock()
		// Cancellation can occur after the fast check above while this call is
		// waiting for the guard. Recheck before serving cached pixels or admitting
		// another decode, rather than relying only on the post-build cache gate.
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return attachmentImage{}, err
		}
		if epoch != c.epoch.Load() {
			c.mu.Unlock()
			return attachmentImage{}, errAttachmentImageRetired
		}
		if value, ok := c.entries[key]; ok {
			c.clock++
			value.used = c.clock
			c.entries[key] = value
			result := attachmentImage{bytes.Clone(value.image.data), value.image.mime}
			c.mu.Unlock()
			return result, value.err
		}
		if pending := c.building[key]; pending != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return attachmentImage{}, ctx.Err()
			case <-pending:
				continue
			}
		}
		done := make(chan struct{})
		c.building[key] = done
		c.mu.Unlock()
		image, err := build()
		c.mu.Lock()
		delete(c.building, key)
		if epoch == c.epoch.Load() && ctx.Err() == nil && (err == nil || errors.Is(err, media.ErrImage)) && len(image.data) <= attachmentImageCacheBytes {
			for len(c.entries) >= attachmentImageCacheEntries || c.bytes+len(image.data) > attachmentImageCacheBytes {
				var oldest attachmentReference
				var used uint64
				first := true
				for candidate, value := range c.entries {
					if first || value.used < used {
						oldest, used, first = candidate, value.used, false
					}
				}
				value := c.entries[oldest]
				clear(value.image.data)
				c.bytes -= len(value.image.data)
				delete(c.entries, oldest)
			}
			c.clock++
			c.entries[key] = cachedAttachmentImage{image: attachmentImage{bytes.Clone(image.data), image.mime}, err: err, used: c.clock}
			c.bytes += len(image.data)
		}
		close(done)
		c.mu.Unlock()
		return image, err
	}
}
