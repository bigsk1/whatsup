//go:build windows

package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func TestWindowsActivationAcceptsOnlyCanonicalContactForDefaultMessageAction(t *testing.T) {
	contactID := syntheticNotice().ContactID
	receipts, _ := newNoticeReceipts(nil, func([]byte) error { return nil }, nil)
	proof, err := receipts.issue(syntheticNotice())
	if err != nil {
		t.Fatal(err)
	}
	valid := notifications.NotificationResult{Response: notifications.NotificationResponse{
		ID: syntheticNotice().ID, ActionIdentifier: notifications.DefaultActionIdentifier,
		UserInfo: map[string]interface{}{"contactID": contactID, "proof": proof},
	}}
	for _, change := range []func(*notifications.NotificationResult){
		func(value *notifications.NotificationResult) { value.Error = errors.New("synthetic error") },
		func(value *notifications.NotificationResult) { value.Response.ActionIdentifier = "untrusted-action" },
		func(value *notifications.NotificationResult) { value.Response.ID = "not a message ID" },
		func(value *notifications.NotificationResult) { value.Response.UserInfo = nil },
		func(value *notifications.NotificationResult) {
			value.Response.UserInfo = map[string]interface{}{"contactID": 1}
		},
		func(value *notifications.NotificationResult) {
			value.Response.UserInfo = map[string]interface{}{"contactID": "AAAAAAAAAAAAAAAAAAAAAA"}
		},
		func(value *notifications.NotificationResult) {
			value.Response.UserInfo = map[string]interface{}{"contactID": contactID, "proof": proof, "body": "synthetic content"}
		},
		func(value *notifications.NotificationResult) {
			value.Response.UserInfo = map[string]interface{}{"contactID": contactID, "proof": strings.Repeat("A", 43)}
		},
		func(value *notifications.NotificationResult) {
			value.Response.UserInfo = map[string]interface{}{"contactID": contactID}
		},
	} {
		value := valid
		change(&value)
		if windowsActivation(value, receipts) != "" {
			t.Fatal("malformed or non-default activation requested a conversation")
		}
	}
	if windowsActivation(valid, receipts) != contactID {
		t.Fatal("issued canonical default activation was lost")
	}
	if windowsActivation(valid, receipts) != "" {
		t.Fatal("issued activation replayed")
	}
}

func TestWindowsReceiptsSurviveRestartAndRecoverFromDPAPIDamage(t *testing.T) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	appName := "WhatsUp-Receipt-QA-" + hex.EncodeToString(token[:])
	path := `Software\Classes\AppUserModelId\` + appName
	t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, path) })
	first, err := openWindowsReceipts(appName)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := first.issue(syntheticNotice())
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := openWindowsReceipts(appName)
	if err != nil || !reopened.consume(syntheticNotice().ID, syntheticNotice().ContactID, proof) {
		t.Fatal("cold launch lost issued proof")
	}
	third, err := openWindowsReceipts(appName)
	if err != nil || third.consume(syntheticNotice().ID, syntheticNotice().ContactID, proof) {
		t.Fatal("consumption not durable")
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	for _, damaged := range [][]byte{{1, 2, 3}, make([]byte, maxProtectedNoticeReceipts+1)} {
		if key.SetBinaryValue(noticeReceiptRegistryValue, damaged) != nil {
			t.Fatal("fixture write failed")
		}
		assertFreshWorkingReceipts(t, appName, proof)
	}
	if key.SetStringValue(noticeReceiptRegistryValue, "wrong type") != nil {
		t.Fatal("fixture write failed")
	}
	assertFreshWorkingReceipts(t, appName, proof)
	malformed, err := receiptDPAPI([]byte("invalid ledger syntax"), appName, true)
	if err != nil || key.SetBinaryValue(noticeReceiptRegistryValue, malformed) != nil {
		t.Fatal("fixture write failed")
	}
	assertFreshWorkingReceipts(t, appName, proof)
	plain := encodeNoticeReceipts(nil)
	ciphertext, err := receiptDPAPI(plain, appName, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := receiptDPAPI(ciphertext, appName+"-other", false); err == nil {
		t.Fatal("app binding lost")
	}
	if key.SetBinaryValue(noticeReceiptRegistryValue, ciphertext) != nil {
		t.Fatal("fixture write failed")
	}
	if _, err := openWindowsReceipts(appName); err != nil {
		t.Fatal("valid empty registry ledger rejected")
	}
	if key.DeleteValue(noticeReceiptRegistryValue) != nil {
		t.Fatal("fixture removal failed")
	}
	lost, err := openWindowsReceipts(appName)
	if err != nil || lost.consume(syntheticNotice().ID, syntheticNotice().ContactID, proof) {
		t.Fatal("lost ledger invented proof for old notice")
	}
}

func assertFreshWorkingReceipts(t *testing.T, appName, oldProof string) {
	t.Helper()
	fresh, err := openWindowsReceipts(appName)
	if err != nil || fresh.consume(syntheticNotice().ID, syntheticNotice().ContactID, oldProof) {
		t.Fatal("damaged ledger did not reset old proofs")
	}
	proof, err := fresh.issue(syntheticNotice())
	if err != nil {
		t.Fatal("reset ledger cannot issue new notice")
	}
	reopened, err := openWindowsReceipts(appName)
	if err != nil || !reopened.consume(syntheticNotice().ID, syntheticNotice().ContactID, proof) {
		t.Fatal("new notice does not survive reset/restart")
	}
}

func TestWindowsReceiptInitializationRetriesTemporaryRegistryFailure(t *testing.T) {
	attempts := 0
	receipts, _ := newNoticeReceipts(nil, func([]byte) error { return nil }, nil)
	w := &windowsBackend{openReceipts: func() (*noticeReceipts, error) {
		attempts++
		if attempts == 1 {
			return nil, ErrUnavailable
		}
		return receipts, nil
	}}
	if _, err := w.ensureReceipts(); err == nil || w.receiptValue() != nil {
		t.Fatal("unavailable ledger reported usable")
	}
	if got, err := w.ensureReceipts(); err != nil || got != receipts {
		t.Fatal("temporary ledger failure became permanent")
	}
	if _, err := w.ensureReceipts(); err != nil || attempts != 2 {
		t.Fatal("healthy ledger needlessly reopened")
	}
}

func TestReceiptTransientReadAndDecryptFailuresPreserveExistingProofs(t *testing.T) {
	var plain []byte
	ledger, _ := newNoticeReceipts(nil, func(data []byte) error { plain = append([]byte(nil), data...); return nil }, nil)
	proof, err := ledger.issue(syntheticNotice())
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"first-read", "second-read", "size-changed", "decrypt"} {
		t.Run(failure, func(t *testing.T) {
			writes := 0
			read := func(buf []byte) (int, uint32, error) {
				if failure == "first-read" && buf == nil {
					return 0, 0, windows.ERROR_ACCESS_DENIED
				}
				if buf != nil {
					if failure == "second-read" {
						return 0, registry.BINARY, windows.ERROR_MORE_DATA
					}
					if failure == "size-changed" {
						return len(plain) + 1, registry.BINARY, nil
					}
					copy(buf, plain)
				}
				return len(plain), registry.BINARY, nil
			}
			decrypt := func(data []byte) ([]byte, error) {
				if failure == "decrypt" {
					return nil, classifyReceiptDPAPIError(windows.ERROR_NOT_ENOUGH_MEMORY)
				}
				return append([]byte(nil), data...), nil
			}
			if _, err := loadWindowsReceipts(read, decrypt, func([]byte) error { writes++; return nil }); err == nil || writes != 0 {
				t.Fatal("temporary fault reset saved proof evidence")
			}
			// Retrying once the transient fault clears keeps the original proof.
			recovered, err := newNoticeReceipts(plain, func([]byte) error { return nil }, nil)
			if err != nil || !recovered.consume(syntheticNotice().ID, syntheticNotice().ContactID, proof) {
				t.Fatal("temporary read/decrypt fault lost existing notice")
			}
		})
	}
	for _, err := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_NOT_ENOUGH_MEMORY, windows.Errno(0x8009000B)} {
		if errors.Is(classifyReceiptDPAPIError(err), errReceiptCorrupt) {
			t.Fatal("ambiguous DPAPI fault classified as corrupt")
		}
	}
	for _, err := range []error{windows.ERROR_INVALID_DATA, windows.ERROR_INVALID_PARAMETER} {
		if !errors.Is(classifyReceiptDPAPIError(err), errReceiptCorrupt) {
			t.Fatal("documented invalid input not recognized")
		}
	}
}

func TestForgetPurgesPersistedWindowsProofsBeforeStartAndAfterStartupFailure(t *testing.T) {
	for _, startFailed := range []bool{false, true} {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			t.Fatal(err)
		}
		name := "WhatsUp-Purge-QA-" + hex.EncodeToString(token[:])
		path := `Software\Classes\AppUserModelId\` + name
		t.Cleanup(func() { _ = registry.DeleteKey(registry.CURRENT_USER, path) })
		ledger, err := openWindowsReceipts(name)
		if err != nil {
			t.Fatal(err)
		}
		first := syntheticNotice()
		other := first
		other.ContactID = "AwMDAwMDAwMDAwMDAwMDAw"
		proof, err := ledger.issue(first)
		if err != nil {
			t.Fatal(err)
		}
		otherProof, err := ledger.issue(other)
		if err != nil {
			t.Fatal(err)
		}
		backend := &windowsBackend{appName: name}
		n := newNotifier(backend)
		if startFailed && n.Start(context.Background()) == nil {
			t.Fatal("native fixture unexpectedly started")
		}
		if err := n.ForgetContact(context.Background(), first.ContactID); err != nil {
			t.Fatal("persistent purge required native startup")
		}
		if n.Close() != nil {
			t.Fatal("fixture close failed")
		}
		reopened, err := openWindowsReceipts(name)
		if err != nil || reopened.consume(first.ID, first.ContactID, proof) || !reopened.consume(other.ID, other.ContactID, otherProof) {
			t.Fatal("Forget failed persistent scoped purge when service wasn't running")
		}
	}
}

func TestPrivateToastABIRequiresExactReviewedModuleAndChecksum(t *testing.T) {
	reviewed := debug.Module{Path: "git.sr.ht/~jackmordaunt/go-toast/v2", Version: "v2.0.3", Sum: "h1:N3IGoHHp9pb6mj1cbXbuaSXV/UMKwmbKLf53nQmtqMA="}
	if !toastBuildApproved(&debug.BuildInfo{Deps: []*debug.Module{&reviewed}}) {
		t.Fatal("exact reviewed module was not accepted")
	}
	for _, changed := range []debug.Module{
		{Path: reviewed.Path, Version: "v2.0.4", Sum: reviewed.Sum},
		{Path: reviewed.Path, Version: reviewed.Version, Sum: "h1:unreviewed"},
		{Path: reviewed.Path, Version: reviewed.Version, Sum: reviewed.Sum, Replace: &debug.Module{Path: "../elsewhere", Version: reviewed.Version}},
		{Path: "example.invalid/toast", Version: reviewed.Version, Sum: reviewed.Sum},
	} {
		if toastBuildApproved(&debug.BuildInfo{Deps: []*debug.Module{&changed}}) {
			t.Fatal("unreviewed private toast ABI accepted")
		}
	}
	if toastBuildApproved(nil) || toastBuildApproved(&debug.BuildInfo{}) {
		t.Fatal("missing provenance enabled private toast ABI")
	}
}

func TestWindowsNativeNotificationStartupIsScopedAndDisplaysNothing(t *testing.T) {
	if os.Getenv("WHATSUP_NATIVE_NOTIFY_STARTUP") != "1" {
		t.Skip("explicit scoped native startup probe only")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	appID := "WhatsUp-Notify-QA-" + hex.EncodeToString(token[:])
	binary := filepath.Join(t.TempDir(), "native-notification-startup.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-mod=readonly", "-o", binary, "./testdata/startup")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("scoped native probe could not build: %v %s", err, output)
	}
	command := exec.CommandContext(ctx, binary, appID)
	output, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "NATIVE_NOTIFICATION_STARTUP_OK" {
		t.Fatalf("scoped native notification startup failed: %v", err)
	}
}
