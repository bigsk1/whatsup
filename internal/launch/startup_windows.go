package launch

import (
	"strings"
	"sync"

	"golang.org/x/sys/windows/registry"
)

const startupKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const startupName = "WhatsUp"

type windowsEntry struct {
	mu      sync.Mutex
	exe     string
	keyPath string
}

func newStartup(exe string, _ bool) (Entry, error) {
	if strings.ContainsRune(exe, '"') || len(`"`+exe+`" --autostart`) > 260 {
		return nil, ErrStartup
	}
	return &windowsEntry{exe: exe, keyPath: startupKey}, nil
}

func (e *windowsEntry) Set(enabled bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !enabled {
		key, err := registry.OpenKey(registry.CURRENT_USER, e.keyPath, registry.SET_VALUE)
		if err == registry.ErrNotExist {
			return nil
		}
		if err != nil {
			return ErrStartup
		}
		defer key.Close()
		if err := key.DeleteValue(startupName); err != nil && err != registry.ErrNotExist {
			return ErrStartup
		}
		return nil
	}
	if !validExecutable(e.exe) {
		return ErrStartup
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, e.keyPath, registry.SET_VALUE)
	if err != nil {
		return ErrStartup
	}
	defer key.Close()
	if key.SetStringValue(startupName, `"`+e.exe+`" --autostart`) != nil {
		return ErrStartup
	}
	return nil
}
