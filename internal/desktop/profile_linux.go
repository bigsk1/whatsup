//go:build linux && cgo && !purego && !gtk3 && !android && !server

package desktop

/*
#cgo pkg-config: webkitgtk-6.0
#include <stdlib.h>
#include <webkit/webkit.h>

static int whatsup_glib_program_name_supported(void) {
    // Wails sets the same program name again when creating its GtkApplication.
    // GLib documents repeat calls as supported since 2.72.
    return glib_check_version(2, 72, 0) == NULL;
}

static WebKitWebsiteDataManager* whatsup_default_website_data_manager(void) {
    WebKitNetworkSession *session = webkit_network_session_get_default();
    if (session == NULL) return NULL;
    return webkit_network_session_get_website_data_manager(session);
}
*/
import "C"

import (
	"errors"
	"path/filepath"
	"unsafe"
)

// PrepareLinuxWebviewProfile must run before application.New or any WebView
// creation. WebKitGTK's persistent default session uses GLib's user data/cache
// bases plus g_get_prgname(). We fix that name, prepare both private roots, then
// query the real default session and reject a stale or different cached path.
// No XDG variables are changed, and no network request or GUI is started here.
//
// https://webkitgtk.org/reference/webkitgtk/stable/type_func.NetworkSession.get_default.html
// https://github.com/WebKit/WebKit/blob/main/Source/WebKit/UIProcess/WebsiteData/glib/WebsiteDataStoreGLib.cpp
// https://docs.gtk.org/glib/func.set_prgname.html
func PrepareLinuxWebviewProfile(dataDirectory string) (LinuxWebviewProfile, error) {
	if C.whatsup_glib_program_name_supported() == 0 {
		return LinuxWebviewProfile{}, errors.New("private Linux WebView profile requires GLib 2.72 or newer")
	}
	dataBase := C.g_get_user_data_dir()
	cacheBase := C.g_get_user_cache_dir()
	if dataBase == nil || cacheBase == nil {
		return LinuxWebviewProfile{}, errors.New("Linux WebView profile bases are unavailable")
	}
	profile, err := prepareLinuxProfileDirectories(dataDirectory, C.GoString(dataBase), C.GoString(cacheBase))
	if err != nil {
		return LinuxWebviewProfile{}, err
	}
	name := C.CString(LinuxProgramName)
	C.g_set_prgname(name)
	C.free(unsafe.Pointer(name))
	actual, err := linuxRuntimeProfile()
	if err != nil {
		return LinuxWebviewProfile{}, err
	}
	if actual.DataDirectory != profile.DataDirectory || actual.CacheDirectory != profile.CacheDirectory {
		return LinuxWebviewProfile{}, errors.New("Linux WebView refused the private application profile")
	}
	return profile, nil
}

func linuxRuntimeProfile() (LinuxWebviewProfile, error) {
	manager := C.whatsup_default_website_data_manager()
	if manager == nil {
		return LinuxWebviewProfile{}, errors.New("Linux WebView profile could not be inspected")
	}
	actualData := C.webkit_website_data_manager_get_base_data_directory(manager)
	actualCache := C.webkit_website_data_manager_get_base_cache_directory(manager)
	if actualData == nil || actualCache == nil {
		return LinuxWebviewProfile{}, errors.New("Linux WebView profile paths are unavailable")
	}
	return LinuxWebviewProfile{
		DataDirectory:  filepath.Clean(C.GoString(actualData)),
		CacheDirectory: filepath.Clean(C.GoString(actualCache)),
	}, nil
}
