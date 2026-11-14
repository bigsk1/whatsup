//go:build linux

package storage

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRouteBackupFileSizeFaultHelper(t *testing.T) {
	dir := os.Getenv("WHATSUP_ROUTE_BACKUP_FSIZE_PATH")
	if dir == "" {
		t.Skip("isolated filesystem fault helper")
	}
	signal.Ignore(syscall.SIGXFSZ)
	var old syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_FSIZE, &old) != nil {
		t.Fatal("limit read")
	}
	limit := old
	limit.Cur = 128
	if syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit) != nil {
		t.Fatal("limit set")
	}
	defer syscall.Setrlimit(syscall.RLIMIT_FSIZE, &old)
	app := &ApplicationState{vault: &Vault{&vaultState{directory: dir}}}
	if err := app.backupRootMetadataLocked(); err == nil {
		t.Fatal("filesystem write limit did not fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if migrationBackupName.MatchString(entry.Name()) || ownedRootTemporaryName.MatchString(entry.Name()) {
			t.Fatal("partial write published a recognized backup")
		}
	}
}
func TestRouteBackupPartialWriteNeverPublishesRecognizedBackup(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	app.Close()
	before, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil || len(before) <= 128 {
		t.Fatal("cipher fixture too short")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRouteBackupFileSizeFaultHelper$")
	cmd.Env = append(os.Environ(), "WHATSUP_ROUTE_BACKUP_FSIZE_PATH="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("filesystem fault helper: %v %s", err, output)
	}
	after, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("partial backup changed authoritative root")
	}
}
