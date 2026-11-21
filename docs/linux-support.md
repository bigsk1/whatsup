# Linux support

The Linux release provides an Ubuntu 24.04 package for x86-64. For Arch Linux
and Omarchy, follow the [manual installation guide](arch-installation.md).

## Requirements

WhatsUp needs:

- A graphical desktop session.
- GTK4, WebKitGTK 6 and GLib 2.72 or newer.
- A working, unlocked desktop Secret Service for the local encryption key.
- Host policy that permits WebKit to create its sandboxed processes.

On Ubuntu, the runtime library packages include libgtk-4-1 and
libwebkitgtk-6.0-4. Installing WebKit libraries alone does not provide an unlocked
Secret Service session.

Running a supplied executable does not require Go, Node.js, a development
server or a separate browser.

## Calls

Voice and video calls use your system's WebKitGTK and GStreamer for the
microphone, camera and codecs:

- **Voice** needs the GStreamer Opus plugin. Ubuntu installs it with WebKitGTK.
  On Arch it is in `gst-plugins-base`.
- **Video** needs the VP8 and JPEG plugins (`gst-plugins-good` on Arch;
  installed with WebKitGTK on Ubuntu) and the PipeWire plugin for the camera
  (`gstreamer1.0-pipewire` on Ubuntu, which the package installs;
  `gst-plugin-pipewire` on Arch), plus a running desktop portal.
- **Sending video** needs WebKitGTK 2.54.3 or newer on systems with GStreamer
  1.28 or newer. Earlier WebKitGTK versions keep repeating the camera's first
  picture for a while after the camera starts, longer the longer the computer
  has been on. WhatsUp notices, switches your video off and tells you, and the
  call carries on with voice. Receiving video works on the earlier versions.
  You can check your version with `apt policy libwebkitgtk-6.0-4` or
  `pacman -Q webkitgtk-6.0`.

The first time your camera is used, your desktop may ask whether WhatsUp may
use it. On GNOME that question appears only for the app in front, so answer the
call with the WhatsUp window in front. An incoming call rings even if WhatsUp
started at login and its window has not been touched since.

WebKitGTK has no echo cancellation. Headphones avoid echo, and the microphone
level is set in your system's sound settings.

## Install on Ubuntu

Install the downloaded package with the system package manager:

    sudo apt install ./whatsup_0.2.1_amd64.deb

The package installs runtime dependencies, one application-menu entry and the
scoped AppArmor compatibility profile. Quit WhatsUp from its tray before an
upgrade or uninstall. Upgrades preserve identity, contacts, history and WebView
data. Removing the package keeps personal data and Secret Service credentials:

    sudo apt remove whatsup

The existing /opt/whatsup-development executable path and AppArmor attachment
name are retained for compatibility; neither is the displayed application name.
Known older review launchers are migrated to the current **WhatsUp** menu entry,
`io.github.bigsk1.whatsup.desktop`, keeping an existing software-rendering choice. The
entry is named after the application so that your desktop can show WhatsUp by
name when it asks whether the app may use the camera. If you pinned WhatsUp to
a dock before this change, pin it again once after upgrading. The app's data/profile
directories do not move. Uninstall removes managed launchers and start-at-login
integration without deleting those directories.

## Older supplied preview bundles

From the supplied preview bundle, run its setup command in your normal graphical
desktop terminal. For a bundle containing the Linux integration directory:

    bash linux-integration/scripts/enable-linux-review.sh

Setup asks for administrator authentication when installing the executable and
its scoped sandbox compatibility policy. It runs the renderer check as the
desktop user, then creates the application-menu entry. It does not launch WhatsUp
as root or delete local identity, credentials or conversation data.

The optional setup flags **--desktop-shortcut** and **--pin-to-dock** create a
desktop shortcut and a GNOME dock entry. Other dock entries are preserved. Other
desktop environments can use the standard application-menu entry.

Launch **WhatsUp** from the menu or installed shortcut. The About page shows
the version. Closing the window keeps the app in
the tray by default when a tray is available; without one, closing quits the app.
Choose Quit from its tray menu to exit; no terminal needs to remain open. Start
at login is optional under Settings → App behavior. If a tray icon cannot be
confirmed, the window is shown. A second launch can also reveal the window while
its interface is still loading.

Tray behavior depends on a compatible desktop status-icon service. WhatsUp uses
the native backend's StatusNotifierItem integration, including common KDE and
GNOME AppIndicator hosts. It detects registration and host changes while running;
late tray startup is allowed a grace period. Startup without a compatible host
shows a window rather than relying on an unreachable tray. Support for a
graphical desktop does not guarantee that every tray implementation is available.

The Ubuntu DEB is unsigned. No universal Linux package, RPM or
Flatpak release is provided.

## WebKit sandbox compatibility

Ubuntu 24.04 restricts unprivileged user namespaces. WebKit uses Bubblewrap and
a D-Bus proxy for its sandbox. Without the required application policy, startup
can fail with:

    bwrap: setting up uid map: Permission denied

The supplied setup installs an application-specific AppArmor compatibility
profile attached to root-owned executables under /opt/whatsup-development.
It keeps the global namespace restriction enabled and does not disable WebKit's
sandbox.

This compatibility profile permits the namespace setup WebKit needs. It is not
a complete confinement policy for the Go application or a guarantee of isolation
from the desktop account.

If setup or its renderer check fails, keep the output for troubleshooting. Avoid
changing global sandbox policy to make an individual executable start.

## Graphics

If accelerated rendering is unreliable, use the app's **--software-rendering**
option. The setup command can select it explicitly:

    bash linux-integration/scripts/enable-linux-review.sh --software-rendering

This changes rendering only. WebKit's sandbox and WhatsUp's encryption remain
enabled. Software rendering may use more CPU.

The setup renderer check verifies that a sandboxed WebKit process can load a
local page and run JavaScript. It does not certify graphics quality on every
desktop or replace testing the actual app.

## Local data

Application data and cache use private directories under $XDG_DATA_HOME/whatsup
and $XDG_CACHE_HOME/whatsup, normally ~/.local/share/whatsup and ~/.cache/whatsup.
Startup rejects unexpected ownership, shared permissions or unsafe paths.

An unavailable or locked Secret Service leaves storage unavailable; WhatsUp does
not substitute a plaintext key file. See the [privacy guide](privacy.md) and
[security information](../SECURITY.md).
