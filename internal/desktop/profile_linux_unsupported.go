//go:build !linux || !cgo || purego || gtk3 || android || server

package desktop

import "errors"

// Unsupported backends fail closed instead of silently using an unverified
// WebView profile. The supported Linux build uses GTK4/WebKitGTK 6 with cgo.
func PrepareLinuxWebviewProfile(string) (LinuxWebviewProfile, error) {
	return LinuxWebviewProfile{}, errors.New("private Linux WebView profile requires the supported GTK4 cgo build")
}
