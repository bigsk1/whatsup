//go:build windows

package notify

import (
	"context"
	"errors"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	_ "unsafe"

	"github.com/go-ole/go-ole"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The exact reviewed go-toast version owns a process-wide initialize flag.
// Initialize it on this dedicated MTA thread before Wails eagerly registers the
// COM activator. Calling CoInitializeEx first would make its first RoInitialize
// return S_FALSE, which that version treats as a failed first Push.
//
//go:linkname initializeWindowsToast git.sr.ht/~jackmordaunt/go-toast/v2/wintoast.initialize
func initializeWindowsToast() error

//go:linkname toastActivationKey git.sr.ht/~jackmordaunt/go-toast/v2/wintoast.activationKey
var toastActivationKey string

//go:linkname toastAppDataMu git.sr.ht/~jackmordaunt/go-toast/v2/wintoast.appDataMu
var toastAppDataMu sync.Mutex

var toastOwner struct {
	sync.Mutex
	claimed bool
}

const defaultToastGUID = "{0F82E845-CB89-4039-BDBF-67CA33254C76}"

func scopedToastActivation(appName string) error {
	if !validToastAppName(appName) {
		return ErrUnavailable
	}
	appKey, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+appName, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return ErrUnavailable
	}
	defer appKey.Close()
	guid, _, err := appKey.GetStringValue("CustomActivator")
	if err == registry.ErrNotExist {
		generated, err := windows.GenerateGUID()
		if err != nil {
			return ErrUnavailable
		}
		guid = generated.String()
		if appKey.SetStringValue("CustomActivator", guid) != nil {
			return ErrUnavailable
		}
	} else if err != nil {
		return ErrUnavailable
	}
	parsed, err := windows.GUIDFromString(guid)
	if err != nil || !strings.EqualFold(parsed.String(), guid) || strings.EqualFold(guid, defaultToastGUID) {
		return ErrUnavailable
	}
	// The pinned upstream stores its default activation path before SetAppData
	// selects an app GUID. Redirect it under that module's own mutex, before
	// Wails creates the matching scoped LocalServer32. The shared default CLSID
	// is neither read for configuration nor written as an activation target.
	toastAppDataMu.Lock()
	toastActivationKey = `Software\Classes\CLSID\` + guid + `\LocalServer32`
	toastAppDataMu.Unlock()
	return nil
}

func validToastAppName(appName string) bool {
	if len(appName) < 1 || len(appName) > 80 {
		return false
	}
	for _, r := range appName {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func toastBuildApproved(info *debug.BuildInfo) bool {
	if info == nil {
		return false
	}
	for _, module := range info.Deps {
		if module.Path == "git.sr.ht/~jackmordaunt/go-toast/v2" {
			return module.Version == "v2.0.3" && module.Sum == "h1:N3IGoHHp9pb6mj1cbXbuaSXV/UMKwmbKLf53nQmtqMA=" && module.Replace == nil
		}
	}
	return false
}

type windowsBackend struct {
	service      *notifications.NotificationService
	activate     func(string)
	initialized  bool
	thread       bool
	receipts     *noticeReceipts
	receiptsMu   sync.Mutex
	appName      string
	openReceipts func() (*noticeReceipts, error)
}

func newBackend(activate func(string)) backend {
	name := ""
	if app := application.Get(); app != nil {
		name = app.Config().Name
	}
	return &windowsBackend{activate: activate, appName: name}
}
func (w *windowsBackend) start(ctx context.Context) error {
	if ctx.Err() != nil {
		return ErrUnavailable
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || !toastBuildApproved(info) {
		return ErrUnavailable
	}
	if application.Get() == nil || !validToastAppName(w.appName) || w.appName != application.Get().Config().Name {
		return ErrUnavailable
	}
	toastOwner.Lock()
	if toastOwner.claimed {
		toastOwner.Unlock()
		return ErrUnavailable
	}
	toastOwner.claimed = true
	toastOwner.Unlock()
	runtime.LockOSThread()
	w.thread = true
	if err := initializeWindowsToast(); err != nil {
		var status *ole.OleError
		if !errors.As(err, &status) || status.Code() != 1 {
			return ErrUnavailable
		}
	}
	w.initialized = true
	if scopedToastActivation(application.Get().Config().Name) != nil {
		return ErrUnavailable
	}
	// A transient registry failure affects notice delivery, not native service
	// lifetime. Later sends retry without requiring a process restart.
	_, _ = w.ensureReceipts()
	w.service = notifications.New()
	w.service.OnNotificationResponse(func(result notifications.NotificationResult) {
		if id := windowsActivation(result, w.receiptValue()); id != "" && w.activate != nil {
			w.activate(id)
		}
	})
	if err := w.service.ServiceStartup(ctx, application.ServiceOptions{}); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (w *windowsBackend) receiptValue() *noticeReceipts {
	w.receiptsMu.Lock()
	defer w.receiptsMu.Unlock()
	return w.receipts
}

func (w *windowsBackend) ensureReceipts() (*noticeReceipts, error) {
	w.receiptsMu.Lock()
	defer w.receiptsMu.Unlock()
	if w.receipts != nil {
		return w.receipts, nil
	}
	var err error
	if w.openReceipts != nil {
		w.receipts, err = w.openReceipts()
	} else {
		w.receipts, err = openWindowsReceipts(w.appName)
	}
	return w.receipts, err
}

func windowsActivation(result notifications.NotificationResult, receipts *noticeReceipts) string {
	if result.Error != nil || result.Response.ActionIdentifier != notifications.DefaultActionIdentifier || !canonicalID(result.Response.ID, false) || len(result.Response.UserInfo) != 2 {
		return ""
	}
	id, ok := result.Response.UserInfo["contactID"].(string)
	if !ok || !canonicalID(id, true) {
		return ""
	}
	proof, ok := result.Response.UserInfo["proof"].(string)
	if !ok || !receipts.consume(result.Response.ID, id, proof) {
		return ""
	}
	return id
}
func (w *windowsBackend) send(ctx context.Context, notice Notice) error {
	if ctx.Err() != nil || w.service == nil {
		return ErrUnavailable
	}
	receipts, err := w.ensureReceipts()
	if err != nil {
		return ErrUnavailable
	}
	proof, err := receipts.issue(notice)
	if err != nil {
		return ErrUnavailable
	}
	// Pinned Wails uses wintoast.Push with no PowerShell fallback. Do not call
	// generic actions, schedules, categories, attachment or file APIs.
	err = w.service.SendNotification(notifications.NotificationOptions{ID: notice.ID, Title: notice.Title, Body: notice.Body, Data: map[string]interface{}{"contactID": notice.ContactID, "proof": proof}})
	if err != nil {
		receipts.discard(notice.ID, notice.ContactID, proof)
	}
	return err
}

func (w *windowsBackend) forget(contactID string) error {
	receipts, err := w.ensureReceipts()
	if err != nil {
		return ErrUnavailable
	}
	return receipts.forget(contactID)
}
func (w *windowsBackend) close() error {
	var err error
	if w.service != nil {
		w.service.OnNotificationResponse(nil)
		err = w.service.ServiceShutdown()
	}
	if w.initialized {
		windows.NewLazySystemDLL("combase.dll").NewProc("RoUninitialize").Call()
		w.initialized = false
	}
	if w.thread {
		runtime.UnlockOSThread()
		w.thread = false
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
