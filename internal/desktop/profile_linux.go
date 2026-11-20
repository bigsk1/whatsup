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

// WebKit settles, once per process and the first time anything asks, the
// identity it later gives the desktop's permission portal for its sandboxed
// web process: the default GApplication's ID, else a program name that is a
// valid application ID, else a hash of the executable's path. Inspecting the
// default session asks. Wails has not built its GtkApplication yet, and the
// program name is deliberately not an application ID, so without a default
// application here the web process would be announced under the hash. A
// desktop that only lets the application in front ask for the camera then
// refuses, because no window belongs to that identity.
//
// The application held here is never registered or run: it owns no bus name
// and opens no display. It exists only while the session is inspected.
static GApplication* whatsup_identity_hold(const char *id) {
    if (g_application_get_default() != NULL) return NULL;
    if (!g_application_id_is_valid(id)) return NULL;
    GApplication *held = g_application_new(id, G_APPLICATION_NON_UNIQUE);
    if (held != NULL && g_application_get_default() != held) {
        g_object_unref(held);
        return NULL;
    }
    return held;
}

// Wails's own GtkApplication must become the default when it is built.
static void whatsup_identity_release(GApplication *held) {
    if (held == NULL) return;
    if (g_application_get_default() == held) g_application_set_default(NULL);
    g_object_unref(held);
}

static int whatsup_default_application_present(void) {
    return g_application_get_default() != NULL;
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
// The same query is when WebKit settles the identity it announces to the
// desktop's permission portal, so LinuxApplicationID is in place for it.
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
	actual, err := linuxRuntimeProfileAs(LinuxApplicationID)
	if err != nil {
		return LinuxWebviewProfile{}, err
	}
	if actual.DataDirectory != profile.DataDirectory || actual.CacheDirectory != profile.CacheDirectory {
		return LinuxWebviewProfile{}, errors.New("Linux WebView refused the private application profile")
	}
	return profile, nil
}

// linuxRuntimeProfileAs inspects the default session while id stands as the
// process's application identity. It refuses when the identity cannot be put
// in place: the camera and microphone questions a desktop asks depend on it.
func linuxRuntimeProfileAs(id string) (LinuxWebviewProfile, error) {
	if C.whatsup_default_application_present() == 0 {
		value := C.CString(id)
		held := C.whatsup_identity_hold(value)
		C.free(unsafe.Pointer(value))
		if held == nil {
			return LinuxWebviewProfile{}, errors.New("Linux application identity could not be prepared")
		}
		defer C.whatsup_identity_release(held)
	}
	return linuxRuntimeProfile()
}

// linuxDefaultApplicationPresent reports whether GLib has a default
// application. Tests use it to see that nothing is left behind.
func linuxDefaultApplicationPresent() bool {
	return C.whatsup_default_application_present() != 0
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
