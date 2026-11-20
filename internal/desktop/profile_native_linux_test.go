//go:build linux && cgo && !purego && !gtk3 && !android && !server

package desktop

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// GLib and WebKit cache directory selection once per process. Fresh child
// processes prove the real system APIs, with independent XDG homes and no display.
func TestNativeLinuxProfileHeadless(t *testing.T) {
	if scenario := os.Getenv("WHATSUP_TEST_NATIVE_PROFILE"); scenario != "" {
		dataDirectory := filepath.Join(os.Getenv("XDG_DATA_HOME"), LinuxProgramName)
		if scenario == "cached-other-program" {
			if _, err := linuxRuntimeProfile(); err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareLinuxWebviewProfile(dataDirectory); err == nil {
				t.Fatal("accepted a session cached before the fixed program name")
			}
			return
		}
		if scenario == "invalid-identity" {
			if _, err := linuxRuntimeProfileAs("whatsup"); err == nil {
				t.Fatal("inspected the session under a name that is not an application ID")
			}
			if linuxDefaultApplicationPresent() {
				t.Fatal("a refused identity left a default application behind")
			}
			return
		}
		profile, err := PrepareLinuxWebviewProfile(dataDirectory)
		if err != nil {
			t.Fatal(err)
		}
		// The identity stood only for the inspection. Wails's GtkApplication
		// has to become the default when it is built.
		if linuxDefaultApplicationPresent() {
			t.Fatal("profile preparation left a default application behind")
		}
		actual, err := linuxRuntimeProfile()
		if err != nil || actual != profile {
			t.Fatal("default runtime session did not select private profile")
		}
		if profile.DataDirectory != dataDirectory || profile.CacheDirectory != filepath.Join(os.Getenv("XDG_CACHE_HOME"), LinuxProgramName) {
			t.Fatal("runtime profile differs from isolated test homes")
		}
		for _, directory := range []string{profile.DataDirectory, profile.CacheDirectory, filepath.Join(profile.CacheDirectory, "WebKitCache")} {
			info, err := os.Stat(directory)
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatal("runtime profile directory is not 0700")
			}
		}
		return
	}
	for _, scenario := range []string{"isolated", "cached-other-program", "invalid-identity"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			command := exec.Command(os.Args[0], "-test.run=^TestNativeLinuxProfileHeadless$")
			command.Env = append(os.Environ(),
				"WHATSUP_TEST_NATIVE_PROFILE="+scenario,
				"XDG_DATA_HOME="+filepath.Join(root, "data"),
				"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
				"DISPLAY=", "WAYLAND_DISPLAY=",
			)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("native profile subprocess failed: %v\n%s", err, output)
			}
		})
	}
}
