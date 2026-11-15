package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
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

// The first Err observes nil and signals before returning its captured result.
// With the cache mutex held, this deterministically places cancellation between
// the optimistic context check and admission to a cache read or a new build.
type cacheAdmissionContext struct {
	context.Context
	checked chan struct{}
	once    sync.Once
}

func (c *cacheAdmissionContext) Err() error {
	err := c.Context.Err()
	c.once.Do(func() { close(c.checked) })
	return err
}

func TestAttachmentImageCacheCancellationWhileWaitingForMutexCannotReadOrBuild(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprint("cached=", cached), func(t *testing.T) {
			c := newAttachmentImageCache()
			key := attachmentReference{"synthetic-contact", "synthetic-file", "thumbnail"}
			if cached {
				if _, err := c.load(t.Context(), key, func() (attachmentImage, error) {
					return attachmentImage{[]byte("sanitized"), "image/png"}, nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			life, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := &cacheAdmissionContext{Context: life, checked: make(chan struct{})}
			var builds atomic.Int32
			result := make(chan error, 1)
			c.mu.Lock()
			go func() {
				image, err := c.load(ctx, key, func() (attachmentImage, error) {
					builds.Add(1)
					return attachmentImage{[]byte("unexpected pixels"), "image/png"}, nil
				})
				if len(image.data) != 0 {
					result <- errors.New("cancelled cache admission returned pixels")
					return
				}
				result <- err
			}()
			<-ctx.checked
			cancel()
			c.mu.Unlock()
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation was not rechecked under the cache guard", err)
			}
			if builds.Load() != 0 {
				t.Fatal("cancelled mutex waiter started a decode")
			}
			if len(c.entries) != map[bool]int{false: 0, true: 1}[cached] {
				t.Fatal("cancelled admission changed cache contents")
			}
		})
	}
}

// Done is consulted only after this load has joined an existing build and
// released the cache mutex. The signal makes invalidation/wakeup ordering exact
// without sleeps or depending on whether the scheduler happened to run a waiter.
type cachePendingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *cachePendingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestAttachmentImageCacheInvalidatedPendingLoadCannotClaimNewGeneration(t *testing.T) {
	c := newAttachmentImageCache()
	key := attachmentReference{"synthetic-contact", "synthetic-file", "thumbnail"}
	pending := make(chan struct{})
	c.building[key] = pending
	ctx := &cachePendingContext{Context: t.Context(), waiting: make(chan struct{})}
	var builds atomic.Int32
	build := func() (attachmentImage, error) {
		builds.Add(1)
		return attachmentImage{[]byte("sanitized"), "image/png"}, nil
	}
	result := make(chan error, 1)
	go func() {
		image, err := c.load(ctx, key, build)
		if len(image.data) != 0 {
			result <- errors.New("retired waiter returned pixels")
			return
		}
		result <- err
	}()
	<-ctx.waiting
	c.invalidate(key.contactID, key.transferID)
	// Simulate the already-running builder completing after invalidation: its
	// stale result cannot enter the cache, and its coalesced waiters wake now.
	c.mu.Lock()
	delete(c.building, key)
	close(pending)
	c.mu.Unlock()
	if err := <-result; !errors.Is(err, errAttachmentImageRetired) {
		t.Fatal("retired waiter acquired a new cache generation", err)
	}
	if builds.Load() != 0 || len(c.entries) != 0 || c.bytes != 0 {
		t.Fatal("retired waiter decoded again or repopulated pixels")
	}
	// A newly authorized invocation after retirement can still populate the
	// bounded cache; invalidation does not leave a permanent retired-ID set.
	if image, err := c.load(t.Context(), key, build); err != nil || len(image.data) == 0 || builds.Load() != 1 || len(c.entries) != 1 {
		t.Fatal("fresh invocation could not use the current generation", err)
	}
}
