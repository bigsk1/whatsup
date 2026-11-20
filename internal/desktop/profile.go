package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// LinuxProgramName fixes the WebKit default profile independently of the binary's
// filename. It must also be supplied as Wails LinuxOptions.ProgramName.
const LinuxProgramName = "whatsup"

// LinuxApplicationID is the GTK application ID. With GTK 4 it is also the
// Wayland app ID of the window, which is how a desktop decides which launcher
// a running window belongs to, and it is the identity the web engine gives
// the desktop's permission portal when it asks for the camera.
//
// The application-menu entry WhatsUp installs or documents is therefore named
// exactly this ID plus ".desktop", and carries it as StartupWMClass. A desktop
// looks the entry up by that name to say who is asking, and GNOME refuses to
// ask at all unless the application in front is the entry so named. With any
// other name the window also shows as a second, unnamed application.
//
// It sits under the project's own namespace, the reverse of its repository
// address, rather than the framework's default "org.wails.<name>", and is set
// explicitly so that it cannot drift from the entries. Changing it needs every
// entry renamed and changed with it, and desktops ask for camera permission
// again under the new name.
const LinuxApplicationID = "io.github.bigsk1.whatsup"

// LinuxWebviewProfile describes backend-only paths. It is never a bridge DTO.
type LinuxWebviewProfile struct {
	DataDirectory  string
	CacheDirectory string
}

func (LinuxWebviewProfile) String() string     { return "[private Linux WebView profile]" }
func (p LinuxWebviewProfile) GoString() string { return p.String() }

// PrepareWebviewProfile creates an app-owned WebView2 profile under the private
// application-data root. No Edge browser profile is imported or shared.
func PrepareWebviewProfile(dataDirectory string) (string, error) {
	if !filepath.IsAbs(dataDirectory) {
		return "", errors.New("private WebView profile requires an absolute application data path")
	}
	profile := filepath.Join(dataDirectory, "webview")
	if err := preparePrivateProfileDirectory(profile); err != nil {
		return "", err
	}
	return profile, nil
}

func preparePrivateProfileDirectory(directory string) error {
	if !filepath.IsAbs(directory) {
		return errors.New("WebView profile requires an absolute directory")
	}
	// Check ancestors before MkdirAll, which otherwise follows an existing
	// symlink/junction and can create a profile beneath a shared directory.
	for ancestor := filepath.Clean(directory); ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("WebView profile cannot use a symlink or non-directory")
		}
		if err != nil && !os.IsNotExist(err) {
			return errors.New("WebView profile directory could not be inspected")
		}
		if parent := filepath.Dir(ancestor); parent == ancestor {
			break
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return errors.New("private WebView profile could not be created")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !profileDirectoryOwnedByCurrentUser(info) || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("WebView profile must be a private directory")
	}
	return nil
}

func prepareLinuxProfileDirectories(dataDirectory, dataBase, cacheBase string) (LinuxWebviewProfile, error) {
	if !filepath.IsAbs(dataDirectory) || !filepath.IsAbs(dataBase) || !filepath.IsAbs(cacheBase) {
		return LinuxWebviewProfile{}, errors.New("Linux WebView profile requires absolute XDG directories")
	}
	profile := LinuxWebviewProfile{
		DataDirectory:  filepath.Join(dataBase, LinuxProgramName),
		CacheDirectory: filepath.Join(cacheBase, LinuxProgramName),
	}
	if filepath.Clean(dataDirectory) != profile.DataDirectory {
		return LinuxWebviewProfile{}, errors.New("Linux WebView data directory does not match private application state")
	}
	for _, directory := range []string{profile.DataDirectory, profile.CacheDirectory, filepath.Join(profile.CacheDirectory, "WebKitCache")} {
		if err := preparePrivateProfileDirectory(directory); err != nil {
			return LinuxWebviewProfile{}, err
		}
	}
	return profile, nil
}
