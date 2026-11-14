# Linux runtime and installation support

The Linux app uses GTK4, WebKitGTK 6, GLib 2.72 or newer, the distribution's
supported native libraries and a working desktop Secret Service. A desktop display
session is needed for GUI operation. WSL builds and backend tests remain headless.
An arbitrary Linux desktop or a successful `ldd` run is not a supported-runtime
claim. Native startup also depends on sandbox policy, graphics and session services.

## Current evidence and support targets

| Environment | Evidence |
| --- | --- |
| Ubuntu 24.04 x86-64 WSL development | Builds, component/race tests, private WebKit profile tests and a local renderer/JavaScript smoke check. This is not VM desktop QA. |
| Ubuntu 24.04 x86-64 VMware Desktop | Scoped root installation and real renderer/local-JavaScript preflight pass with global restrictions enabled. Secret Service and profile tests pass separately. The owner confirms 0.0.2 opens, unlocks and preserves identity across lock/quit/reopen/unlock in software-rendering mode. |
| Other distributions, architectures and desktop sessions | Not yet verified. Add native dependency/policy integration and X11/Wayland/graphics/storage tests before claiming support. |

Broader distribution requires repeatable builds against a chosen compatibility
baseline, declared runtime dependencies and distribution-appropriate installation.
Flatpak is a possible future runtime strategy, with separate review of permissions,
Secret Service and nested sandboxing. No universal package or Flatpak release is
implemented here.

## Restricted user namespaces

Ubuntu 24.04 restricts capabilities inside user namespaces created by otherwise
unconfined programs. WebKit uses Bubblewrap and a D-Bus proxy to construct its
sandbox. A raw user-directory executable without the appropriate profile can fail
with `bwrap: setting up uid map: Permission denied`, followed by a native abort.

The supported compatibility approach is an application-specific AppArmor profile
with `flags=(unconfined)` and the `userns,` permission. The development installer
attaches it only to root-owned, stable executable paths:

```text
/opt/whatsup-development/whatsup
/opt/whatsup-development/whatsup-webview-check
```

This grants the namespace permission needed by WebKit's own sandbox. It is not a
complete AppArmor confinement policy for the Go application. Global namespace
restrictions stay enabled. There is no profile for the general `/usr/bin/bwrap`
tool, no user-writable wildcard, no setuid/capability grant and no dangerous
WebKit sandbox-disable environment flag.

The installer requires administrator authentication, validates the selected
development bundle and hashes, retains immutable version copies and loads the
specific compatibility profile when required. It never launches an app as root or
touches a user's vault, keyring, cache or history. User launchers are updated by
the subsequent unprivileged handoff. This integration does not open release gates.
Before sudo, the desktop-user wrapper stages the selected files in a private
directory on local `/var/tmp` and validates them again. User-mounted VMware/FUSE
shares can deny root access even when the desktop user can read them; staging
avoids changing mount permissions. A retry can reuse an existing immutable
archive only when all declared files and hashes match. Different bytes are refused.

The unprivileged launcher helper installs `whatsup.desktop` to match the existing
GTK application name, while retaining the previous development ID as a hidden
alias. Optional desktop and GNOME dock shortcuts are user preferences, not global
system changes. The standard menu launcher uses the installed executable directly
with `Terminal=false`; running a review build needs no development server. The
setup helpers require Python 3, desktop-file-utils and xdg-utils; GNOME shortcut
integration additionally uses the session's GIO/GSettings tools. Other desktops
can use the standard menu entry without GNOME dock integration.

See [Ubuntu's release notes](https://discourse.ubuntu.com/t/ubuntu-24-04-lts-noble-numbat-release-notes/39890),
[the namespace restriction explanation](https://discourse.ubuntu.com/t/understanding-apparmor-user-namespace-restriction/58007)
and [Chromium's developer guidance](https://chromium.googlesource.com/chromium/src/+/main/docs/security/apparmor-userns-restrictions.md).

## Native preflight and graphics

The original headless data-directory check never created a WebProcess or D-Bus
proxy. It therefore missed the actual launch failure. The separate bounded
`whatsup-webview-check` developer tool now creates an ephemeral WebKit session,
loads only compiled-in local HTML and verifies a JavaScript result. It uses a
hidden, unpresented window, has a timeout and contains native aborts in its own
child. It rejects the dangerous sandbox-disable override and touches no app state.

Run the installed probe from the desktop user's graphical session, never as root.
`npm run test:native` is the corresponding Linux developer command; headless
development hosts can use Xvfb. Test results from the installed policy context
must be distinguished from an unprofiled source-build path. A standalone Bubblewrap
test launched from SSH has a different policy context and cannot prove app readiness.

The probe's default mode uses WebKit's supported software policy; `--accelerated`
checks the accelerated path. A successful local-page/JavaScript probe establishes
renderer/process startup, not visible pixel quality, the real Wails bridge or
native storage QA. The owner still tests the actual app and restart continuity.

For VMs or drivers that cannot render reliably, the app accepts
`--software-rendering`, using Wails' `WebviewGpuPolicyNever` and WebKit's documented
hardware-acceleration API. This changes rendering only; sandbox and encryption
behavior remain intact. CPU rendering can cost more CPU and is a separately
identified launch mode. Graphics warnings do not explain or fix UID-map denial.
See [WebKit's acceleration policy](https://webkitgtk.org/reference/webkitgtk/stable/property.Settings.hardware-acceleration-policy.html).
