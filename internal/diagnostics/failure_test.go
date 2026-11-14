package diagnostics

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFixedFailureEventsAreRateLimitedWithoutPayload(t *testing.T) {
	directory := privateDirectory(t)
	r := openTest(t, directory)
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	r.clock = func() time.Time { return now }
	for range 20 {
		for event := RelayFetchFailed; event <= TransferFailed; event++ {
			if err := r.Record(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries := readRecords(t, directory)
	want := []string{"relay_fetch_failed", "dial_failed", "authentication_failed", "identity_changed", "capacity_reached", "transfer_failed"}
	if len(entries) != len(want) {
		t.Fatal("failure flood exceeded finite rate budget")
	}
	for i, event := range want {
		if entries[i].Event != event {
			t.Fatal("fixed failure code changed")
		}
	}
	now = now.Add(FailureInterval)
	for event := RelayFetchFailed; event <= TransferFailed; event++ {
		if err := r.Record(event); err != nil {
			t.Fatal(err)
		}
	}
	if len(readRecords(t, directory)) != 2*len(want) {
		t.Fatal("failure interval never reopened")
	}
}

func TestSharedFailureLimiterBoundsConcurrentContactReports(t *testing.T) {
	var calls atomic.Int32
	now := time.Now()
	sink := rateLimited(func(Event) { calls.Add(1) }, func() time.Time { return now })
	var writers sync.WaitGroup
	for range 64 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for event := RelayFetchFailed; event <= TransferFailed; event++ {
				sink(event)
			}
			sink(0)
			sink(255)
		}()
	}
	writers.Wait()
	if calls.Load() != int32(TransferFailed-RelayFetchFailed+1) {
		t.Fatal("shared contacts escaped per-code failure budget")
	}
	for range 3 {
		sink(StorageUnlocked)
	}
	if calls.Load() != int32(TransferFailed-RelayFetchFailed+1)+3 {
		t.Fatal("lifecycle events were silently suppressed")
	}
	now = now.Add(FailureInterval)
	sink(RelayFetchFailed)
	if calls.Load() != int32(TransferFailed-RelayFetchFailed+1)+4 {
		t.Fatal("failure code remained blocked after interval")
	}
}

func TestBootstrapLoggerAcceptsFiniteSinkAndDropsEveryCallerValue(t *testing.T) {
	var r *Recorder
	logger := LoggerFor(func(event Event) {
		if r != nil {
			_ = r.Record(event)
		}
	})
	logger.Warn("synthetic bootstrap secret before recorder")
	directory := privateDirectory(t)
	r = openTest(t, directory)
	var resolved atomic.Int32
	secret := "synthetic private bootstrap endpoint and text"
	logger.With("secret", secret, "deferred", secretLogValuer{&resolved}).WithGroup(secret).Warn(secret, slog.Any("stringer", secretStringer{}))
	logger.ErrorContext(context.Background(), secret, "payload", secretLogValuer{&resolved})
	LoggerFor(nil).Warn(secret)
	wire, err := os.ReadFile(filepath.Join(directory, currentFilename))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire, []byte(secret)) || resolved.Load() != 0 {
		t.Fatal("bootstrap logger exposed caller-controlled content")
	}
	entries := readRecords(t, directory)
	if len(entries) != 2 || entries[0].Event != "runtime_warning" || entries[1].Event != "runtime_error" {
		t.Fatal("bootstrap logger did not retain only finite codes")
	}
}
