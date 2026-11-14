//go:build linux && cgo && !purego && !gtk3 && !android && !server

package nativefiles

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestClipboardCancellationJoinsBeforeReleasingBorrowedNativeObject(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered, release, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var freed atomic.Bool
	join := clipboardCancellation(ctx, func() {
		close(entered)
		<-release
		if freed.Load() {
			t.Error("native cancellation raced freed object")
		}
	})
	cancel()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("native cancellation was not dispatched")
	}
	go func() { join(); freed.Store(true); close(joined) }()
	select {
	case <-joined:
		t.Fatal("cleanup did not join active cancellation")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("native cancellation join blocked")
	}
	if !freed.Load() {
		t.Fatal("native object was not released")
	}
	join() // Cleanup remains safe if a second path tries to join.
}

func TestClipboardCompletionStopsLateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	called := make(chan struct{}, 1)
	join := clipboardCancellation(ctx, func() { called <- struct{}{} })
	join()
	cancel()
	select {
	case <-called:
		t.Fatal("cancel callback accessed completed native operation")
	case <-time.After(20 * time.Millisecond):
	}
}
