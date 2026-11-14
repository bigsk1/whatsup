package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/media"
)

func TestAttachmentImageCacheCopiesBoundsNegativeResultsAndRetires(t *testing.T) {
	c := newAttachmentImageCache()
	key := attachmentReference{"synthetic-contact", "synthetic-file", "thumbnail"}
	builds := 0
	build := func() (attachmentImage, error) {
		builds++
		return attachmentImage{data: []byte("synthetic sanitized image"), mime: "image/png"}, nil
	}
	first, err := c.load(t.Context(), key, build)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.load(t.Context(), key, build)
	if err != nil || builds != 1 || !bytes.Equal(first.data, second.data) {
		t.Fatal("cache decoded the same image twice", err)
	}
	first.data[0] ^= 1
	if bytes.Equal(first.data, second.data) {
		t.Fatal("response bytes alias each other")
	}
	c.invalidate(key.contactID, key.transferID)
	if len(c.entries) != 0 || c.bytes != 0 || !bytes.Equal(second.data, []byte("synthetic sanitized image")) {
		t.Fatal("retirement changed response-owned bytes or retained cache")
	}
	negative := attachmentReference{key.contactID, "bad-image", "thumbnail"}
	failures := 0
	for range 3 {
		if _, err := c.load(t.Context(), negative, func() (attachmentImage, error) {
			failures++
			return attachmentImage{}, media.ErrImage
		}); !errors.Is(err, media.ErrImage) {
			t.Fatal(err)
		}
	}
	if failures != 1 {
		t.Fatal("negative result was repeatedly decoded")
	}
	for i := range attachmentImageCacheEntries + 3 {
		_, err := c.load(t.Context(), attachmentReference{key.contactID, fmt.Sprint(i), "thumbnail"}, build)
		if err != nil || len(c.entries) > attachmentImageCacheEntries || c.bytes > attachmentImageCacheBytes {
			t.Fatal("cache exceeded entry bound", err)
		}
	}
	large := bytes.Repeat([]byte{71}, attachmentImageCacheBytes/2+1)
	for i := range 3 {
		_, err := c.load(t.Context(), attachmentReference{key.contactID, fmt.Sprintf("large%d", i), "image"}, func() (attachmentImage, error) {
			return attachmentImage{data: bytes.Clone(large), mime: "image/png"}, nil
		})
		if err != nil || c.bytes > attachmentImageCacheBytes || len(c.entries) > attachmentImageCacheEntries {
			t.Fatal("cache exceeded byte bound", err)
		}
	}
	c.invalidate("", "")
	if len(c.entries) != 0 || c.bytes != 0 {
		t.Fatal("session shutdown retained cached pixels")
	}
}

func TestAttachmentImageCacheCoalescesAndCannotRepopulateAfterRetirement(t *testing.T) {
	c := newAttachmentImageCache()
	key := attachmentReference{"synthetic-contact", "synthetic-file", "thumbnail"}
	entered, release := make(chan struct{}), make(chan struct{})
	var builds atomic.Int32
	build := func() (attachmentImage, error) {
		if builds.Add(1) == 1 {
			close(entered)
		}
		<-release
		return attachmentImage{[]byte("sanitized"), "image/png"}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 2)
	go func() { _, err := c.load(ctx, key, build); finished <- err }()
	<-entered
	go func() { _, err := c.load(ctx, key, build); finished <- err }()
	c.invalidate(key.contactID, "")
	cancel()
	close(release)
	for range 2 {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("cache waiter did not observe cancellation")
		}
	}
	if builds.Load() != 1 || len(c.entries) != 0 {
		t.Fatal("retired generation decoded again or repopulated cache")
	}
}
