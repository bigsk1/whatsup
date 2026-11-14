package launch

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

const startupFilename = "whatsup.desktop"
const startupMarker = "X-WhatsUp-Managed=true\n"

type linuxEntry struct {
	mu       sync.Mutex
	exe      string
	software bool
	config   string
}

func newStartup(exe string, software bool) (Entry, error) {
	config, err := os.UserConfigDir()
	if err != nil || !filepath.IsAbs(config) || strings.ContainsRune(exe, '=') {
		return nil, ErrStartup
	}
	return &linuxEntry{exe: exe, software: software, config: config}, nil
}

func startupOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func startupDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !startupOwned(info) || info.Mode().Perm()&0022 != 0 {
		return ErrStartup
	}
	return nil
}

func readStartup(path string) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || !startupOwned(info) || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return nil, nil, ErrStartup
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, ErrStartup
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, nil, ErrStartup
	}
	wire, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(wire) > 16384 || !bytes.HasPrefix(wire, []byte("[Desktop Entry]\n")) || !bytes.Contains(wire, []byte("\n"+startupMarker)) {
		return nil, nil, ErrStartup
	}
	return wire, info, nil
}

// Apply desktop-entry string escaping before the separate Exec quoting pass.
// The resulting command is consumed by the desktop launcher, never a shell.
func desktopExecutable(exe string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for _, r := range exe {
		switch r {
		case '%':
			quoted.WriteString("%%")
		case '\\':
			quoted.WriteString(`\\\\`)
		case '"', '`', '$':
			quoted.WriteString(`\\`)
			quoted.WriteRune(r)
		default:
			quoted.WriteRune(r)
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}

func (e *linuxEntry) payload() []byte {
	exec := desktopExecutable(e.exe) + " --autostart"
	if e.software {
		exec += " --software-rendering"
	}
	return []byte("[Desktop Entry]\nType=Application\nName=WhatsUp\n" + startupMarker + "Exec=" + exec + "\nTerminal=false\nX-GNOME-Autostart-enabled=true\n")
}

func (e *linuxEntry) Set(enabled bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	directory := filepath.Join(e.config, "autostart")
	path := filepath.Join(directory, startupFilename)
	if !enabled {
		if _, err := os.Lstat(directory); os.IsNotExist(err) {
			return nil
		}
	} else {
		if !validExecutable(e.exe) {
			return ErrStartup
		}
		if err := os.MkdirAll(e.config, 0700); err != nil {
			return ErrStartup
		}
		if err := startupDirectory(e.config); err != nil {
			return err
		}
		if err := os.MkdirAll(directory, 0700); err != nil {
			return ErrStartup
		}
	}
	if startupDirectory(e.config) != nil || startupDirectory(directory) != nil {
		return ErrStartup
	}
	_, previous, err := readStartup(path)
	if err != nil {
		return err
	}
	if !enabled {
		if previous == nil {
			return nil
		}
		if os.Remove(path) != nil {
			return ErrStartup
		}
		return syncStartupDirectory(directory)
	}
	f, err := os.CreateTemp(directory, ".whatsup-autostart-*.tmp")
	if err != nil {
		return ErrStartup
	}
	temp := f.Name()
	defer os.Remove(temp)
	if f.Chmod(0600) != nil {
		_ = f.Close()
		return ErrStartup
	}
	if _, err := f.Write(e.payload()); err != nil {
		_ = f.Close()
		return ErrStartup
	}
	if f.Sync() != nil || f.Close() != nil {
		_ = f.Close()
		return ErrStartup
	}
	if startupDirectory(e.config) != nil || startupDirectory(directory) != nil {
		return ErrStartup
	}
	_, current, err := readStartup(path)
	if err != nil || previous == nil && current != nil || previous != nil && (current == nil || !os.SameFile(previous, current)) {
		return ErrStartup
	}
	if os.Rename(temp, path) != nil {
		return ErrStartup
	}
	return syncStartupDirectory(directory)
}

func syncStartupDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return ErrStartup
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrStartup
	}
	return nil
}
