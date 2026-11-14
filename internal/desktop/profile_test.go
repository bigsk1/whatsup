package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWebviewProfileIsAppOwnedAndRejectsUnsafePaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	profile, err := PrepareWebviewProfile(root)
	if err != nil || profile != filepath.Join(root, "webview") {
		t.Fatal("profile is not isolated under application data")
	}
	if _, err := PrepareWebviewProfile("relative"); err == nil {
		t.Fatal("accepted relative profile root")
	}
	if runtime.GOOS != "windows" {
		other := filepath.Join(t.TempDir(), "symlink-root")
		if err := os.Mkdir(other, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(profile, filepath.Join(other, "webview")); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareWebviewProfile(other); err == nil {
			t.Fatal("accepted shared symlink profile")
		}
	}
}

func TestProfileRejectsSymlinkAncestorBeforeCreatingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating Windows symlinks needs host privileges")
	}
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	if err := os.Mkdir(shared, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareWebviewProfile(filepath.Join(link, "state")); err == nil {
		t.Fatal("accepted a symlink ancestor")
	}
	if _, err := os.Stat(filepath.Join(shared, "state")); !os.IsNotExist(err) {
		t.Fatal("created directories through rejected ancestor")
	}
}

func TestLinuxProfilePreparesSeparatePrivateDataAndCache(t *testing.T) {
	root := t.TempDir()
	dataBase, cacheBase := filepath.Join(root, "data"), filepath.Join(root, "cache")
	profile, err := prepareLinuxProfileDirectories(filepath.Join(dataBase, LinuxProgramName), dataBase, cacheBase)
	if err != nil {
		t.Fatal(err)
	}
	if profile.DataDirectory != filepath.Join(dataBase, LinuxProgramName) || profile.CacheDirectory != filepath.Join(cacheBase, LinuxProgramName) {
		t.Fatal("profile escaped fixed per-program roots")
	}
	for _, directory := range []string{profile.DataDirectory, profile.CacheDirectory, filepath.Join(profile.CacheDirectory, "WebKitCache")} {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm() != 0700) {
			t.Fatal("profile directory is not private")
		}
	}
	if fmt.Sprint(profile) != "[private Linux WebView profile]" || fmt.Sprintf("%#v", profile) != "[private Linux WebView profile]" {
		t.Fatal("profile formatting exposes local paths")
	}
	if _, err := prepareLinuxProfileDirectories(filepath.Join(root, "other"), dataBase, cacheBase); err == nil {
		t.Fatal("accepted inconsistent application-data root")
	}
	if _, err := prepareLinuxProfileDirectories(filepath.Join(dataBase, LinuxProgramName), "relative", cacheBase); err == nil {
		t.Fatal("accepted a relative XDG base")
	}
}

func TestLinuxProfileRejectsExistingSharedCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode enforcement")
	}
	for _, unsafeChild := range []string{"", "WebKitCache"} {
		t.Run(unsafeChild, func(t *testing.T) {
			root := t.TempDir()
			dataBase, cacheBase := filepath.Join(root, "data"), filepath.Join(root, "cache")
			cache := filepath.Join(cacheBase, LinuxProgramName)
			if err := os.MkdirAll(cache, 0700); err != nil {
				t.Fatal(err)
			}
			unsafeDirectory := filepath.Join(cache, unsafeChild)
			if err := os.MkdirAll(unsafeDirectory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(unsafeDirectory, 0755); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareLinuxProfileDirectories(filepath.Join(dataBase, LinuxProgramName), dataBase, cacheBase); err == nil {
				t.Fatal("accepted preexisting shared cache")
			}
			info, err := os.Stat(unsafeDirectory)
			if err != nil || info.Mode().Perm() != 0755 {
				t.Fatal("silently modified preexisting cache permissions")
			}
		})
	}
}
