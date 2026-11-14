//go:build windows

// Disposable native lifecycle probe. This starts no app window and displays no
// notification. Only a random test app ID and its scoped COM registration exist.
package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows/registry"
	"whatsup/internal/diagnostics"
	"whatsup/internal/notify"
)

const shared = `Software\Classes\CLSID\{0F82E845-CB89-4039-BDBF-67CA33254C76}\LocalServer32`

func sharedValue() (bool, string) {
	key, err := registry.OpenKey(registry.CURRENT_USER, shared, registry.QUERY_VALUE)
	if err != nil {
		return false, ""
	}
	defer key.Close()
	value, _, _ := key.GetStringValue("")
	return true, value
}

func cleanup(appID string) bool {
	path := `Software\Classes\AppUserModelId\` + appID
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	guid := ""
	if err == nil {
		guid, _, _ = key.GetStringValue("CustomActivator")
		key.Close()
	}
	if guid != "" && !strings.EqualFold(guid, "{0F82E845-CB89-4039-BDBF-67CA33254C76}") && regexp.MustCompile(`^\{[0-9A-Fa-f-]{36}\}$`).MatchString(guid) {
		target := `Software\Classes\CLSID\` + guid + `\LocalServer32`
		registered, openErr := registry.OpenKey(registry.CURRENT_USER, target, registry.QUERY_VALUE)
		if openErr == nil {
			value, _, valueErr := registered.GetStringValue("")
			registered.Close()
			exe, exeErr := os.Executable()
			if valueErr != nil || exeErr != nil || (value != `"`+exe+`" %1` && value != exe) {
				return false
			}
		} else if openErr != registry.ErrNotExist {
			return false
		}
		for _, suffix := range []string{`\LocalServer32`, ""} {
			_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Classes\CLSID\`+guid+suffix)
		}
	} else if guid != "" {
		return false
	}
	for _, owned := range []string{`Software\` + appID + `\NotificationCategories`, `Software\` + appID, path} {
		if err := registry.DeleteKey(registry.CURRENT_USER, owned); err != nil && err != registry.ErrNotExist {
			return false
		}
	}
	return true
}

func run() int {
	if len(os.Args) != 2 || !regexp.MustCompile(`^WhatsUp-Notify-QA-[0-9a-f]{32}$`).MatchString(os.Args[1]) {
		return 2
	}
	appID := os.Args[1]
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\AppUserModelId\`+appID, registry.QUERY_VALUE)
	if err == nil {
		key.Close()
		return 3
	}
	if err != registry.ErrNotExist {
		return 4
	}
	beforeExists, beforeValue := sharedValue()
	application.New(application.Options{Name: appID, Logger: diagnostics.LoggerFor(nil)})
	native := notify.New(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startErr := native.Start(ctx)
	closeErr := native.Close()
	afterExists, afterValue := sharedValue()
	cleaned := cleanup(appID)
	if !cleaned {
		return 5
	}
	if beforeExists != afterExists || beforeValue != afterValue {
		return 6
	}
	if startErr != nil {
		return 7
	}
	if closeErr != nil {
		return 8
	}
	fmt.Println("NATIVE_NOTIFICATION_STARTUP_OK")
	return 0
}
func main() { os.Exit(run()) }
