package launch

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func linuxStartupTest(t *testing.T, software bool) (*linuxEntry, string) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", config)
	entry, err := NewStartup(temporaryExecutable(t, "WhatsUp versioned executable"), software)
	if err != nil {
		t.Fatal(err)
	}
	return entry.(*linuxEntry), filepath.Join(config, "autostart", startupFilename)
}

func TestLinuxStartupVersionUpdateAndDisableKeepUnrelatedEntries(t *testing.T) {
	first, path := linuxStartupTest(t, true)
	if err := first.Set(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.config); !os.IsNotExist(err) {
		t.Fatal("disabling absent startup created configuration")
	}
	if err := first.Set(true); err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(wire, []byte("Exec="+desktopExecutable(first.exe)+" --autostart --software-rendering\n")) || !bytes.Contains(wire, []byte(startupMarker)) {
		t.Fatal("startup entry omitted current executable or fixed rendering flag")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("startup entry was not private")
	}
	unrelated := filepath.Join(filepath.Dir(path), "unrelated.desktop")
	if err := os.WriteFile(unrelated, []byte("synthetic unrelated entry"), 0600); err != nil {
		t.Fatal(err)
	}
	second := &linuxEntry{exe: temporaryExecutable(t, "new version"), config: first.config}
	if err := second.Set(true); err != nil {
		t.Fatal(err)
	}
	wire, _ = os.ReadFile(path)
	if !bytes.Contains(wire, []byte("Exec="+desktopExecutable(second.exe)+" --autostart\n")) || bytes.Contains(wire, []byte(first.exe)) || bytes.Contains(wire, []byte("--software-rendering")) {
		t.Fatal("startup entry kept old version path or rendering state")
	}
	if err := first.Set(false); err != nil {
		t.Fatal("startup disable depended on current executable matching saved version")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("fixed startup entry remained after disable")
	}
	if wire, err := os.ReadFile(unrelated); err != nil || string(wire) != "synthetic unrelated entry" {
		t.Fatal("startup action altered an unrelated entry")
	}
}

func TestDesktopExecEscapingUsesTwoPassDesktopRules(t *testing.T) {
	exe := "/home/test/50% spaced\"quote$dollar`tick\\backslash"
	want := `"/home/test/50%% spaced\\"quote\\$dollar\\` + "`" + `tick\\\\backslash"`
	if got := desktopExecutable(exe); got != want {
		t.Fatal("Exec escaping did not preserve literal reserved characters")
	}
	entry := &linuxEntry{exe: exe}
	if bytes.Contains(entry.payload(), []byte("%U")) || bytes.Contains(entry.payload(), []byte("%F")) || strings.Count(string(entry.payload()), "Exec=") != 1 {
		t.Fatal("startup entry introduced file/URL field expansion")
	}
}

func TestLinuxStartupRejectsSymlinksUnsafePathsAndUnownedMarker(t *testing.T) {
	for _, mode := range []string{"symlink-entry", "symlink-directory", "wide-entry", "writable-directory", "unknown-entry"} {
		t.Run(mode, func(t *testing.T) {
			entry, path := linuxStartupTest(t, false)
			if err := entry.Set(true); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "symlink-entry":
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, entry.payload(), 0600); err != nil {
					t.Fatal(err)
				}
				if os.Remove(path) != nil || os.Symlink(outside, path) != nil {
					t.Fatal("could not construct symlink fixture")
				}
			case "symlink-directory":
				original := filepath.Dir(path)
				if os.Rename(original, original+"-old") != nil || os.Symlink(original+"-old", original) != nil {
					t.Fatal("could not construct directory symlink fixture")
				}
			case "wide-entry":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "writable-directory":
				if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
					t.Fatal(err)
				}
			case "unknown-entry":
				if err := os.WriteFile(path, []byte("[Desktop Entry]\nName=Unrelated\nExec=/bin/false\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := entry.Set(true); !errors.Is(err, ErrStartup) {
				t.Fatal("unsafe startup entry was replaced")
			}
			if err := entry.Set(false); !errors.Is(err, ErrStartup) {
				t.Fatal("unsafe startup entry was deleted")
			}
		})
	}
}

func TestLinuxStartupRejectsRelativeConfigAndUnsupportedExecEquals(t *testing.T) {
	exe := temporaryExecutable(t, "executable")
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := NewStartup(exe, false); !errors.Is(err, ErrStartup) {
		t.Fatal("relative XDG config path was accepted")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := NewStartup(temporaryExecutable(t, "unsupported=path"), false); !errors.Is(err, ErrStartup) {
		t.Fatal("Desktop Entry unsupported executable equals sign was accepted")
	}
}
