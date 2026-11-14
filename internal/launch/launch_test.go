package launch

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStrictLaunchModesOnlyAcceptFixedArguments(t *testing.T) {
	for _, test := range []struct {
		goos string
		args []string
		want Options
	}{
		{"windows", nil, Options{}},
		{"linux", nil, Options{}},
		{"windows", []string{"--autostart"}, Options{Autostart: true}},
		{"linux", []string{"--autostart", "--software-rendering"}, Options{Autostart: true, SoftwareRendering: true}},
		{"windows", []string{"-Embedding"}, Options{ToastActivation: true}},
		{"windows", []string{"-eMbEdDiNg", "%1"}, Options{ToastActivation: true}},
		{"windows", []string{"%1", "-Embedding"}, Options{ToastActivation: true}},
	} {
		got, err := Parse(test.args, test.goos)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatal("fixed launch mode was not parsed")
		}
	}
	for _, test := range []struct {
		goos string
		args []string
	}{
		{"darwin", nil},
		{"linux", []string{"-Embedding"}},
		{"linux", []string{"%1"}},
		{"windows", []string{"%1"}},
		{"windows", []string{"--software-rendering"}},
		{"linux", []string{"--software-rendering=true"}},
		{"linux", []string{"--autostart=false"}},
		{"windows", []string{"--autostart", "--autostart"}},
		{"windows", []string{"-Embedding", "-Embedding"}},
		{"windows", []string{"-Embedding", "%1", "%1"}},
		{"windows", []string{"-Embedding", "whatsup://pair/v2?private"}},
		{"windows", []string{"-Embedding", "https://example.invalid/toast"}},
		{"linux", []string{"--", "--autostart"}},
		{"linux", []string{"--help"}},
		{"linux", []string{"--unknown"}},
		{"windows", []string{"%TEMP%"}},
		{"linux", []string{"./synthetic-file"}},
	} {
		if got, err := Parse(test.args, test.goos); !errors.Is(err, ErrOptions) || got != (Options{}) {
			t.Fatal("unknown, duplicated or positional launch input was accepted")
		}
	}
}

func temporaryExecutable(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte{0, 1, 2}, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStartupConstructorRequiresExactRegularExecutable(t *testing.T) {
	if _, err := NewStartup("relative", false); !errors.Is(err, ErrStartup) {
		t.Fatal("relative startup executable was accepted")
	}
	if _, err := NewStartup(t.TempDir(), false); !errors.Is(err, ErrStartup) {
		t.Fatal("directory startup executable was accepted")
	}
	if _, err := NewStartup(filepath.Join(t.TempDir(), "missing"), false); !errors.Is(err, ErrStartup) {
		t.Fatal("missing startup executable was accepted")
	}
	for _, control := range []string{"\x00", "\n", "\r", "\t"} {
		if _, err := NewStartup(filepath.Join(t.TempDir(), "bad"+control+"file"), false); !errors.Is(err, ErrStartup) {
			t.Fatal("control character entered startup command")
		}
	}
}
