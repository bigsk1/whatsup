package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"whatsup/internal/messaging"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type resourceWorkerConfig struct {
	Directory string
	Profile   []byte
	Control   string
	Contacts  int
	Measure   bool
}

type resourceSample struct {
	RSSBytes   uint64 `json:"rssBytes"`
	HeapBytes  uint64 `json:"heapBytes"`
	Goroutines int    `json:"goroutines"`
}

type resourceResult struct {
	Platform          string         `json:"platform"`
	Contacts          int            `json:"contacts"`
	Baseline          resourceSample `json:"baseline"`
	Connected         resourceSample `json:"connected"`
	CPUSeconds        float64        `json:"cpuSeconds"`
	ElapsedSeconds    float64        `json:"elapsedSeconds"`
	CPUPercentOneCore float64        `json:"cpuPercentOneCore"`
}

func resourceWrite(path string, value any) error {
	wire, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", wire, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func resourceWait(ctx context.Context, path string) error {
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func resourceSnapshot() (resourceSample, time.Duration, error) {
	rss, cpu, err := resourceProcessStats()
	if err != nil {
		return resourceSample{}, 0, err
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return resourceSample{RSSBytes: rss, HeapBytes: memory.HeapAlloc, Goroutines: runtime.NumGoroutine()}, cpu, nil
}

// TestRegistryResourceWorker is a child-process entry point. It is inert during
// normal tests and is not linked into the application.
func TestRegistryResourceWorker(t *testing.T) {
	configPath := os.Getenv("WHATSUP_RESOURCE_WORKER")
	if configPath == "" {
		t.Skip("isolated resource worker only")
	}
	wire, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal("worker configuration unavailable")
	}
	var config resourceWorkerConfig
	if json.Unmarshal(wire, &config) != nil || config.Contacts < 1 || config.Contacts > 10 {
		t.Fatal("worker configuration invalid")
	}
	clear(wire)
	profile, err := transport.ExportImportResourceProfileForTest(t, config.Profile)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := io.ReadAll(io.LimitReader(os.Stdin, 257))
	if err != nil || len(credential) == 0 || len(credential) > 256 {
		t.Fatal("worker credential handoff unavailable")
	}
	vault, err := storage.Open(config.Directory, &disposableIntegrationKeys{value: string(credential)})
	clear(credential)
	if err != nil {
		t.Fatal("worker vault unavailable")
	}
	app, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal("worker state unavailable")
	}
	defer func() {
		if err := app.Close(); err != nil {
			t.Error("resource worker could not close storage")
		}
	}()
	local, err := app.Identity()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := messaging.NewRegistry(local, app, messaging.RegistryOptions{Factories: messaging.Factories{Profile: func(context.Context, string) (*transport.RelayProfile, error) { return profile, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	contacts, err := registry.Contacts()
	if err != nil || len(contacts) != config.Contacts {
		t.Fatal("worker contact count mismatch")
	}
	runtime.GC()
	baseline, _, err := resourceSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := resourceWrite(filepath.Join(config.Control, "ready.json"), true); err != nil {
		t.Fatal(err)
	}
	if err := resourceWait(ctx, filepath.Join(config.Control, "start.json")); err != nil {
		t.Fatal(err)
	}
	for _, contact := range contacts {
		if _, err := registry.Connect(contact.ContactID); err != nil {
			t.Fatal("worker could not connect")
		}
	}
	for {
		contacts, err = registry.Contacts()
		if err != nil {
			t.Fatal(err)
		}
		connected := true
		for _, contact := range contacts {
			connected = connected && contact.State == "connected"
		}
		if connected {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("worker connections timed out")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if err := resourceWrite(filepath.Join(config.Control, "connected.json"), true); err != nil {
		t.Fatal(err)
	}
	if config.Measure {
		if err := resourceWait(ctx, filepath.Join(config.Control, "measure.json")); err != nil {
			t.Fatal(err)
		}
		// Let handshake allocations settle before measuring the target only.
		time.Sleep(1500 * time.Millisecond)
		runtime.GC()
		_, beforeCPU, err := resourceSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		time.Sleep(12 * time.Second) // Includes three authenticated heartbeat cycles.
		connected, afterCPU, err := resourceSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(started)
		contacts, err = registry.Contacts()
		if err != nil {
			t.Fatal(err)
		}
		for _, contact := range contacts {
			if contact.State != "connected" {
				t.Fatal("worker lost session during measurement")
			}
		}
		result := resourceResult{Platform: runtime.GOOS + "/" + runtime.GOARCH, Contacts: config.Contacts, Baseline: baseline, Connected: connected, CPUSeconds: (afterCPU - beforeCPU).Seconds(), ElapsedSeconds: elapsed.Seconds(), CPUPercentOneCore: 100 * (afterCPU - beforeCPU).Seconds() / elapsed.Seconds()}
		if err := resourceWrite(filepath.Join(config.Control, "result.json"), result); err != nil {
			t.Fatal(err)
		}
	}
	if err := resourceWait(ctx, filepath.Join(config.Control, "stop.json")); err != nil {
		t.Fatal(err)
	}
}

type resourceProcess struct {
	command *exec.Cmd
	control string
	log     *os.File
	done    chan struct{}
	err     error
}

func launchResourceProcess(t *testing.T, ctx context.Context, p *multiMessagingPeer, profile []byte, contacts int, measure bool) *resourceProcess {
	t.Helper()
	control := t.TempDir()
	config := resourceWorkerConfig{Directory: p.directory, Profile: profile, Control: control, Contacts: contacts, Measure: measure}
	configPath := filepath.Join(control, "worker.json")
	if err := resourceWrite(configPath, config); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(control, "worker.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRegistryResourceWorker$", "-test.timeout=200s")
	command.Env = append(os.Environ(), "WHATSUP_RESOURCE_WORKER="+configPath)
	// Synthetic OS wrapping credentials travel only through the child's stdin,
	// never command arguments, environment variables, configuration or logs.
	command.Stdin = bytes.NewReader([]byte(p.keys.value))
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	process := &resourceProcess{command: command, control: control, log: log, done: make(chan struct{})}
	go func() { process.err = command.Wait(); close(process.done) }()
	t.Cleanup(func() {
		_ = resourceWrite(filepath.Join(control, "stop.json"), true)
		select {
		case <-process.done:
			if process.err != nil {
				t.Errorf("isolated resource worker failed: %v", process.err)
			}
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
			<-process.done
			t.Error("isolated resource worker did not stop within its shutdown bound")
		}
		_ = log.Close()
	})
	return process
}

func resourceWaitProcess(ctx context.Context, process *resourceProcess, name string) error {
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(process.control, name)); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-process.done:
			return fmt.Errorf("isolated worker exited before numeric result: %v", process.err)
		case <-tick.C:
		}
	}
}

// Run explicitly with WHATSUP_RESOURCE_PROBE=1. The target, every peer and the
// DERP driver are distinct processes. Reported CPU/RSS exclude peers and relay.
func TestRegistryConnectedResourceProbe(t *testing.T) {
	if os.Getenv("WHATSUP_RESOURCE_PROBE") != "1" {
		t.Skip("explicit isolated resource probe only")
	}
	for _, count := range []int{1, 5, 10} {
		t.Run(fmt.Sprintf("contacts-%d", count), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			t.Cleanup(cancel)
			profile := transport.ExportLoopbackRelayProfileForTest(t)
			profileWire := transport.ExportResourceProfileForTest(t, profile)
			target := newMultiMessagingPeer(t, profile)
			peers := make([]*multiMessagingPeer, 0, count)
			for range count {
				peer := newMultiMessagingPeer(t, profile)
				pairMultiMessagingPeers(t, ctx, profile, target, peer)
				peers = append(peers, peer)
			}
			// All driver handles close before child processes open the same stores.
			all := append([]*multiMessagingPeer{target}, peers...)
			for _, p := range all {
				p.registry.Close()
				if err := p.flow.Close(); err != nil {
					t.Fatal(err)
				}
				p.close(t)
			}
			processes := make([]*resourceProcess, 0, count+1)
			for index, p := range all {
				expected := 1
				if index == 0 {
					expected = count
				}
				processes = append(processes, launchResourceProcess(t, ctx, p, profileWire, expected, index == 0))
			}
			for _, process := range processes {
				if err := resourceWaitProcess(ctx, process, "ready.json"); err != nil {
					t.Fatal(err)
				}
			}
			for _, process := range processes {
				if err := resourceWrite(filepath.Join(process.control, "start.json"), true); err != nil {
					t.Fatal(err)
				}
			}
			for _, process := range processes {
				if err := resourceWaitProcess(ctx, process, "connected.json"); err != nil {
					t.Fatal(err)
				}
			}
			if err := resourceWrite(filepath.Join(processes[0].control, "measure.json"), true); err != nil {
				t.Fatal(err)
			}
			resultPath := filepath.Join(processes[0].control, "result.json")
			if err := resourceWaitProcess(ctx, processes[0], "result.json"); err != nil {
				t.Fatal(err)
			}
			wire, err := os.ReadFile(resultPath)
			if err != nil {
				t.Fatal(err)
			}
			var result resourceResult
			if json.Unmarshal(wire, &result) != nil || result.Contacts != count || result.ElapsedSeconds < 12 {
				t.Fatal("resource measurement incomplete")
			}
			t.Logf("ISOLATED_RESOURCE %s", wire)
		})
	}
}
