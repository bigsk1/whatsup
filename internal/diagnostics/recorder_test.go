package diagnostics

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func openTest(t *testing.T, directory string) *Recorder {
	t.Helper()
	recorder, err := Open(directory, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	return recorder
}

func privateDirectory(t *testing.T) string {
	t.Helper()
	// testing.TempDir children can use ordinary 0777/umask permissions. Create
	// the actual diagnostics directory with the same private mode as the app.
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func readRecords(t *testing.T, directory string) []record {
	t.Helper()
	var records []record
	for _, filename := range []string{previousFilename, currentFilename} {
		data, err := os.ReadFile(filepath.Join(directory, filename))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > MaxFileBytes {
			t.Fatal("local diagnostics exceeded per-file retention limit")
		}
		if len(data) > 0 && data[len(data)-1] != '\n' {
			t.Fatal("retained incomplete diagnostic line")
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			var entry record
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				t.Fatal("invalid diagnostic JSONL")
			}
			var fields map[string]any
			_ = json.Unmarshal(scanner.Bytes(), &fields)
			if len(fields) != 6 {
				t.Fatal("diagnostics retained fields outside the finite schema")
			}
			for _, key := range []string{"utc", "appVersion", "goVersion", "platform", "arch", "event"} {
				if _, exists := fields[key]; !exists {
					t.Fatal("diagnostic schema field missing")
				}
			}
			_, offset := entry.UTC.Zone()
			if entry.UTC.IsZero() || offset != 0 || entry.Platform != runtime.GOOS || entry.Arch != runtime.GOARCH || entry.AppVersion != "0.1.0" {
				t.Fatal("diagnostic metadata did not match its public runtime/build")
			}
			records = append(records, entry)
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return records
}

func TestFiniteEventsAndIdempotentClose(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "diagnostics")
	recorder := openTest(t, directory)
	for event := Startup; event <= RuntimePanic; event++ {
		if err := recorder.Record(event); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []Event{0, TransferFailed + 1, 255} {
		if err := recorder.Record(invalid); !errors.Is(err, ErrEvent) {
			t.Fatal("unknown event accepted")
		}
	}
	if records := readRecords(t, directory); len(records) != int(RuntimePanic) {
		t.Fatal("unknown event wrote a record or finite event was lost")
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal("Close is not idempotent")
	}
	if err := recorder.Record(Startup); !errors.Is(err, ErrClosed) {
		t.Fatal("closed recorder accepted an event")
	}
	if strings.Contains(fmt.Sprintf("%#v", recorder), directory) {
		t.Fatal("recorder formatting exposed local path")
	}
	reopened := openTest(t, directory)
	if err := reopened.Record(Startup); err != nil {
		t.Fatal("closed recorder retained writer lock")
	}
}

type secretLogValuer struct{ resolved *atomic.Int32 }

func (secret secretLogValuer) LogValue() slog.Value {
	secret.resolved.Add(1)
	panic("diagnostics must never evaluate a LogValuer")
}

type secretStringer struct{}

func (secretStringer) String() string { panic("diagnostics must never format caller values") }

func TestWailsSlogDropsAllMessagesAttributesAndGroups(t *testing.T) {
	directory := privateDirectory(t)
	recorder := openTest(t, directory)
	logger := recorder.WailsLogger()
	secrets := []string{
		"synthetic-private-key-ABC123", "whatsup://pair/v1?secret=synthetic-bearer", "peer.synthetic.invalid:1234",
		"synthetic private message contents", "C:\\Users\\synthetic-person\\private-file.txt", "synthetic-user@host.invalid",
	}
	var resolved atomic.Int32
	bound := logger.With("privateKey", secrets[0], "deferred", secretLogValuer{&resolved}).WithGroup(secrets[4])
	bound.Warn(secrets[1], slog.String("endpoint", secrets[2]), slog.Group("nested", "text", secrets[3]), slog.Any("error", errors.New(secrets[5])), slog.Any("stringer", secretStringer{}))
	bound.Error(secrets[3], "payload", map[string]any{"identityFingerprint": secrets[0], "filename": secrets[4]})
	logger.Debug(secrets[0])
	logger.Info(secrets[1])
	handler := recorder.Handler().WithGroup(secrets[0]).WithAttrs([]slog.Attr{slog.String("path", secrets[4]), slog.Any("deferred", secretLogValuer{&resolved})})
	entry := slog.NewRecord(time.Now(), slog.LevelWarn, secrets[5], 0)
	entry.AddAttrs(slog.String("secret", secrets[0]), slog.Any("value", secretStringer{}))
	if err := handler.Handle(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	info := slog.NewRecord(time.Now(), slog.LevelInfo, secrets[0], 0)
	if err := handler.Handle(context.Background(), info); err != nil {
		t.Fatal(err)
	}
	if handler.Enabled(context.Background(), slog.LevelInfo) || !handler.Enabled(context.Background(), slog.LevelWarn) {
		t.Fatal("unexpected diagnostics log level policy")
	}
	if resolved.Load() != 0 {
		t.Fatal("diagnostics evaluated deferred values")
	}
	data, _ := os.ReadFile(filepath.Join(directory, currentFilename))
	for _, secret := range secrets {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("raw log content leaked into local diagnostics")
		}
	}
	if bytes.Contains(data, []byte("privateKey")) || bytes.Contains(data, []byte("nested")) || bytes.Contains(data, []byte(directory)) {
		t.Fatal("attribute names, groups, or local path leaked")
	}
	records := readRecords(t, directory)
	if len(records) != 3 || records[0].Event != "runtime_warning" || records[1].Event != "runtime_error" || records[2].Event != "runtime_warning" {
		t.Fatal("slog retained something beyond generic warning/error events")
	}
}

func TestRotationKeepsOnlyTwoBoundedFiles(t *testing.T) {
	directory := privateDirectory(t)
	line := func(event Event) []byte {
		name, _ := eventName(event)
		data, err := json.Marshal(record{UTC: time.Date(2026, 11, 13, 0, 0, 0, 123456789, time.UTC), AppVersion: "0.1.0", GoVersion: runtime.Version(), Platform: runtime.GOOS, Arch: runtime.GOARCH, Event: name})
		if err != nil {
			t.Fatal(err)
		}
		return append(data, '\n')
	}
	current, previous := line(Startup), line(Shutdown)
	// Seed valid bounded records without doing thousands of filesystem syncs;
	// actual rotations and every subsequent event use the production writer.
	if err := os.WriteFile(filepath.Join(directory, currentFilename), bytes.Repeat(current, MaxFileBytes/len(current)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, previousFilename), bytes.Repeat(previous, MaxFileBytes/len(previous)), 0600); err != nil {
		t.Fatal(err)
	}
	recorder := openTest(t, directory)
	for i := 0; i < 4; i++ {
		if err := recorder.Record(RuntimeError); err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if entry.Name() != lockFilename && entry.Name() != currentFilename && entry.Name() != previousFilename {
			t.Fatal("diagnostics created an unbounded archive filename")
		}
		if info.Size() > MaxFileBytes {
			t.Fatal("per-file diagnostic limit exceeded")
		}
		total += info.Size()
	}
	if total > MaxRetainedBytes || len(entries) != 3 {
		t.Fatal("diagnostics retention was not bounded to two files")
	}
	old, _ := os.ReadFile(filepath.Join(directory, previousFilename))
	if bytes.Contains(old, []byte(`"event":"shutdown"`)) || !bytes.Contains(old, []byte(`"event":"startup"`)) {
		t.Fatal("rotation failed to replace old previous file")
	}
	_ = readRecords(t, directory)
}

func TestPartialFinalAppendIsRecoveredAfterRestart(t *testing.T) {
	directory := privateDirectory(t)
	recorder := openTest(t, directory)
	if err := recorder.Record(Startup); err != nil {
		t.Fatal(err)
	}
	_ = recorder.Close()
	file, err := os.OpenFile(filepath.Join(directory, currentFilename), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"utc":"synthetic incomplete append`)
	_ = file.Close()
	recorder = openTest(t, directory)
	if err := recorder.Record(Shutdown); err != nil {
		t.Fatal(err)
	}
	if records := readRecords(t, directory); len(records) != 2 {
		t.Fatal("crash recovery lost complete records or retained incomplete append")
	}
}

func TestConcurrentRecordingAndWriterExclusion(t *testing.T) {
	directory := privateDirectory(t)
	recorder := openTest(t, directory)
	if _, err := Open(directory, "0.1.0"); !errors.Is(err, ErrBusy) {
		t.Fatal("second writer opened diagnostics")
	}
	const workers, perWorker = 16, 16
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if err := recorder.Record(RuntimeWarning); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if records := readRecords(t, directory); len(records) != workers*perWorker {
		t.Fatal("concurrent records were interleaved, lost, or duplicated")
	}
}

func TestConcurrentCloseAndRecordPreserveCompleteLines(t *testing.T) {
	directory := privateDirectory(t)
	recorder := openTest(t, directory)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := recorder.Record(RuntimeWarning); err != nil && !errors.Is(err, ErrClosed) {
				t.Error("record/close race returned an unexpected failure")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if err := recorder.Close(); err != nil {
			t.Error("concurrent close failed")
		}
	}()
	close(start)
	wg.Wait()
	_ = readRecords(t, directory)
	_ = openTest(t, directory)
}

func TestUnsafePathsModesBoundsAndVersionsFailClosed(t *testing.T) {
	for _, version := range []string{"", "synthetic-user", "0.1.0-synthetic-private-key", "1.0.0-/private/user", "1.0.0+machine-hostname"} {
		if _, err := Open(filepath.Join(t.TempDir(), "unused"), version); !errors.Is(err, ErrVersion) {
			t.Fatal("private/freeform version text accepted")
		}
	}
	if _, err := Open("relative-diagnostics", "0.1.0"); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("relative diagnostics path accepted")
	}
	for _, filename := range []string{currentFilename, previousFilename, lockFilename} {
		t.Run("directory-"+filename, func(t *testing.T) {
			directory := privateDirectory(t)
			if err := os.Mkdir(filepath.Join(directory, filename), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(directory, "0.1.0"); !errors.Is(err, ErrUnsafePath) {
				t.Fatal("nonregular diagnostic file accepted")
			}
		})
		t.Run("oversized-"+filename, func(t *testing.T) {
			directory := privateDirectory(t)
			length := int64(MaxFileBytes + 1)
			if filename == lockFilename {
				length = 1
			}
			path := filepath.Join(directory, filename)
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_ = file.Truncate(length)
			_ = file.Close()
			if _, err := Open(directory, "0.1.0"); !errors.Is(err, ErrUnsafePath) {
				t.Fatal("oversized retained file accepted")
			}
			info, _ := os.Stat(path)
			if info.Size() != length {
				t.Fatal("unsafe existing file was silently overwritten")
			}
		})
	}
	if runtime.GOOS == "windows" {
		return // Unix modes and unprivileged symlinks are verified on Linux.
	}
	for _, filename := range []string{currentFilename, previousFilename, lockFilename} {
		t.Run("symlink-"+filename, func(t *testing.T) {
			directory := privateDirectory(t)
			target := filepath.Join(t.TempDir(), "preserve")
			_ = os.WriteFile(target, []byte("synthetic preserved file"), 0600)
			if err := os.Symlink(target, filepath.Join(directory, filename)); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(directory, "0.1.0"); !errors.Is(err, ErrUnsafePath) {
				t.Fatal("symlink diagnostic file accepted")
			}
			data, _ := os.ReadFile(target)
			if string(data) != "synthetic preserved file" {
				t.Fatal("symlink target changed")
			}
		})
		t.Run("mode-"+filename, func(t *testing.T) {
			directory := privateDirectory(t)
			_ = os.WriteFile(filepath.Join(directory, filename), nil, 0644)
			if _, err := Open(directory, "0.1.0"); !errors.Is(err, ErrUnsafePath) {
				t.Fatal("shared-readable diagnostic file accepted")
			}
		})
	}
	directory := filepath.Join(t.TempDir(), "unsafe-mode")
	_ = os.Mkdir(directory, 0755)
	if _, err := Open(directory, "0.1.0"); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("shared directory accepted")
	}
	link := filepath.Join(t.TempDir(), "directory-link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link, "0.1.0"); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("symlink directory accepted")
	}
}

func TestCreatedUnixFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows inherits user application-data ACL; Unix mode bits do not establish ACLs")
	}
	directory := filepath.Join(t.TempDir(), "private-diagnostics")
	recorder := openTest(t, directory)
	_ = recorder.Record(Startup)
	for _, path := range []string{directory, filepath.Join(directory, currentFilename), filepath.Join(directory, lockFilename)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		wanted := os.FileMode(0600)
		if path == directory {
			wanted = 0700
		}
		if info.Mode().Perm() != wanted {
			t.Fatal("diagnostics created a nonprivate mode")
		}
	}
}

func TestWriteFailureStopsRecorderWithoutFallback(t *testing.T) {
	directory := privateDirectory(t)
	recorder := openTest(t, directory)
	if err := recorder.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record(RuntimeError); !errors.Is(err, ErrWrite) {
		t.Fatal("closed underlying file was reported as a persisted event")
	}
	if err := recorder.Record(RuntimeWarning); !errors.Is(err, ErrWrite) {
		t.Fatal("failed recorder silently resumed writing")
	}
	_ = recorder.Close()
	_ = openTest(t, directory)
	entries, _ := os.ReadDir(directory)
	if len(entries) != 2 {
		t.Fatal("write failure created a fallback file")
	}
}

func TestDiagnosticsLockChild(t *testing.T) {
	if os.Getenv("WHATSUP_DIAGNOSTICS_TEST_CHILD") != "1" {
		return
	}
	var directory string
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 8192)).Decode(&directory); err != nil {
		t.Fatal("invalid synthetic diagnostic subprocess input")
	}
	recorder, err := Open(directory, "0.1.0")
	if recorder != nil {
		_ = recorder.Close()
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatal("independent process acquired occupied diagnostic writer")
	}
}

func TestIndependentProcessWriterExclusion(t *testing.T) {
	directory := privateDirectory(t)
	_ = openTest(t, directory)
	input, _ := json.Marshal(directory)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDiagnosticsLockChild$", "-test.count=1")
	command.Env = append(os.Environ(), "WHATSUP_DIAGNOSTICS_TEST_CHILD=1")
	command.Stdin = bytes.NewReader(input)
	if _, err := command.CombinedOutput(); err != nil {
		t.Fatal("independent process diagnostic exclusion failed")
	}
}
