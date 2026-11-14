// Package launch accepts only the fixed application launch modes and manages
// one app-owned startup entry. It never interprets a shell command or URL.
package launch

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrOptions = errors.New("unsupported application launch arguments")
var ErrStartup = errors.New("application startup entry is unavailable")

type Options struct {
	Autostart         bool
	SoftwareRendering bool
	ToastActivation   bool
}

func Parse(args []string, goos string) (Options, error) {
	var result Options
	if goos != "windows" && goos != "linux" {
		return result, ErrOptions
	}
	placeholder := false
	for _, arg := range args {
		switch {
		case arg == "--autostart" && !result.Autostart:
			result.Autostart = true
		case arg == "--software-rendering" && goos == "linux" && !result.SoftwareRendering:
			result.SoftwareRendering = true
		case goos == "windows" && strings.EqualFold(arg, "-Embedding") && !result.ToastActivation:
			result.ToastActivation = true
		case goos == "windows" && arg == "%1" && !placeholder:
			placeholder = true
		default:
			return Options{}, ErrOptions
		}
	}
	if placeholder && !result.ToastActivation {
		return Options{}, ErrOptions
	}
	return result, nil
}

type Entry interface{ Set(bool) error }

func validExecutable(exe string) bool {
	if !filepath.IsAbs(exe) || filepath.Clean(exe) != exe || !utf8.ValidString(exe) || len(exe) > 32768 || strings.IndexFunc(exe, unicode.IsControl) >= 0 {
		return false
	}
	info, err := os.Lstat(exe)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func NewStartup(exe string, softwareRendering bool) (Entry, error) {
	if !validExecutable(exe) || softwareRendering && runtime.GOOS != "linux" {
		return nil, ErrStartup
	}
	return newStartup(exe, softwareRendering)
}
