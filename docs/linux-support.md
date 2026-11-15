# Linux support

The Linux preview currently targets Ubuntu 24.04 on x86-64. Other distributions,
architectures, desktop sessions and graphics stacks are not yet validated.

## Requirements

WhatsUp needs:

- A graphical desktop session.
- GTK4, WebKitGTK 6 and GLib 2.72 or newer.
- A working, unlocked desktop Secret Service for the local encryption key.
- Host policy that permits WebKit to create its sandboxed processes.

On Ubuntu, the runtime library packages include libgtk-4-1 and
libwebkitgtk-6.0-4. Installing WebKit libraries alone does not provide an unlocked
Secret Service session.

Running a supplied preview executable does not require Go, Node.js, a development
server or a separate browser.

## Install a supplied preview bundle

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

Launch **WhatsUp (Development)** from the menu or installed shortcut. This preview
label identifies the unsigned preview build. Closing the window keeps the app in
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

No universal Linux package, signed DEB/RPM or Flatpak release is available yet.

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
changing global sandbox policy to make an individual preview executable start.

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
