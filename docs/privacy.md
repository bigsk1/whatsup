# Privacy controls and local troubleshooting

Policy updated 2026-11-14. Application and development-tool telemetry are off by
default. The owner explicitly accepts the documented Windows WebView2 diagnostic
limitation. WhatsUp's own diagnostics stay local; no automatic upload or remote
crash-reporting service is configured.

## Application diagnostics

The backend records a small fixed event vocabulary: startup/shutdown, storage
locked/unlocked/unavailable, and generic runtime warning/error/panic. A record
contains UTC time, app version, Go version, OS and architecture. It cannot accept
arbitrary strings or extra fields. Wails logging discards raw message text and all
attributes without evaluating them; panic handling records a generic event rather
than uploading or serializing panic details.

Logs are plaintext operational metadata, separate from the encrypted identity
snapshot. They deliberately exclude messages, attachments, keys, invitations,
fingerprints, contact/peer endpoints, user paths, machine names and bridge payloads.
They show operational timing and software versions to someone who can access the
local account. They are not a promise of protection against that account or OS.

Files live under %LOCALAPPDATA%\WhatsUp on Windows and XDG_DATA_HOME/whatsup (or
~/.local/share/whatsup) on Linux: diagnostics.jsonl plus diagnostics.previous.jsonl.
Each is limited to 256 KiB, for 512 KiB total retained event data. Unix directories
and files use 0700/0600; Windows inherits the user's application-data ACL. A file
lock excludes competing writers. Unsafe paths fail without a remote fallback.
No frontend filesystem bridge or automatic diagnostic export is exposed.

## WebView runtime boundary

Windows uses the system WebView2 runtime. WhatsUp explicitly creates its profile
under %LOCALAPPDATA%\WhatsUp\webview and supplies that path through Wails'
WebviewUserDataPath option. It neither imports nor shares an Edge browser profile.
This separation is storage isolation, not an OS sandbox. Wails already disables
the msSmartScreenProtection feature; the app preserves this for its bundled-content
renderer. It does not alter global Edge or Windows settings.

Optional WebView2 diagnostics follow Windows privacy settings. Microsoft also
documents required diagnostics that applications cannot disable through an overall
collection switch. Its supported custom-crash-reporting opt-out is not exposed by
the pinned Wails loader. The owner permits these documented WebView2 behaviors;
this is not permission to add WhatsUp analytics or a remote crash reporter. See
[Microsoft's data/privacy documentation](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/data-privacy)
and [the crash-reporting API](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2environmentoptions3).
Unstable Chromium flags are not treated as proof of zero vendor diagnostics.

Linux uses system GTK4/WebKitGTK 6 with CGO and GLib 2.72 or newer. Before Wails
creates a window, WhatsUp fixes the program name to whatsup, prepares
XDG_DATA_HOME/whatsup and XDG_CACHE_HOME/whatsup (normally ~/.local/share/whatsup
and ~/.cache/whatsup), and checks WebKit's actual default network-session data and
cache paths. The app roots and WebKitCache directory must be owned by the current
user and have 0700 permissions. Symlinks, shared modes and an already cached
session pointing elsewhere fail startup. Existing shared directories are not
silently accepted or automatically chmodded. No XDG environment rewrite or Wails
fork is used. This isolates app storage; it does not sandbox the OS account.

Headless native tests verify both the real selected paths and rejection of a
pre-cached foreign program profile. These checks do not require WSLg. See the
[WebKit default session API](https://webkitgtk.org/reference/webkitgtk/stable/type_func.NetworkSession.get_default.html),
[WebKit's directory construction](https://github.com/WebKit/WebKit/blob/main/Source/WebKit/UIProcess/WebsiteData/glib/WebsiteDataStoreGLib.cpp)
and [GLib's program-name API](https://docs.gtk.org/glib/func.set_prgname.html).
WebKit diagnostics differ from WebView2. Source inspection found no WhatsUp-owned
remote collector, but neither the app's CSP nor these tests establish a
system-wide zero-outbound-network guarantee.

## Go and development tools

Go's supported persistent control is go telemetry off, executed separately for
the Windows and Ubuntu development users. Both go telemetry and go env GOTELEMETRY
report off. This disables local Go counters as well as uploads. The runner applies
and verifies it before other Go/tool commands. GOTELEMETRY is a non-settable report;
assigning an environment variable is not used as a pretend opt-out. Existing old
counter files are not deleted by this command. See [Go telemetry documentation](https://go.dev/doc/telemetry).

Root and frontend .npmrc files disable update-notifier, automatic install-time
audit, and funding notices. The runner also normalizes inherited npm configuration
casing so a parent environment cannot accidentally re-enable those defaults.
Funding suppression is a console preference. The deliberate npm audit command
still submits dependency names/versions to the registry and fails on advisories.
The security gate remains enabled. See [npm configuration](https://docs.npmjs.com/cli/v12/using-npm/config)
and [explicit audit behavior](https://docs.npmjs.com/cli/v12/commands/npm-audit).

Windows NVM's optional project/release announcements are also disabled through
its documented disable_announcements configuration. TLS, signature/revocation
verification and package-manager trust policies were preserved. Shared runtime
defaults and older Node/global-tool installations were not replaced.

The pinned Wails CLI disables task-version checks. Its sponsor/docs footer prints
links; explicit docs/sponsor commands open them. No Wails-owned analytics uploader
was found in the inspected CLI or application startup source. Generated runtime
fetches use the local application's origin. No invented WAILS_NO_TELEMETRY flag
is used. The same source review found no collector in the pinned Svelte, Vite or
Tailwind packages. Vitest OpenTelemetry instrumentation is explicitly disabled;
there is no exporter. Playwright has no configured telemetry reporter; its isolated
Chromium tests disable background/crash/reliability reporting and hyperlink pings.
These test-browser flags do not control Windows WebView2.

Dependency and browser downloads, Go checksum verification, requested doctor
network checks, explicit advisory queries and deliberate update discovery remain
functional network operations. The service operators observe normal request
metadata such as source IP/User-Agent. Explicitly requested live pairing uses
peer discovery and approved encrypted setup traffic. Privacy controls do not
claim anonymity.

## Explicit pairing connections and reviewed transport controls

The shared workflow uses the reviewed upstream tags ts_omit_logtail,
ts_omit_netlog, ts_omit_clientmetrics, ts_omit_usermetrics and
ts_omit_runtimemetrics, plus ts_omit_useproxy, ts_omit_portmapper and
ts_omit_captiveportal. The latter omit inherited proxy routing, automatic router
mapping and captive-portal probing. Transport additionally sets the supported
no-logs/no-support process control and disables normal logtail uploads. These controls do not disable
WhatsUp's independent local diagnostic recorder.

The reviewed copy under third_party/tailcat is based on immutable upstream
revision e00cdde6ff787f801361ff85ed085775c775d4c5. Its narrow patch adds
DisableDERPApplicationStats, set true on both invitation endpoints, and fixes
debugAddr to false so an inherited debug switch cannot log bearer addresses.
config/tailcat-patch.json records upstream and patched file hashes, and the
workflow verifies the exact allowed replacement. The BSD-3-Clause source and
notice are retained. See [the transport review](transport-review.md).

The invitation URL field is empty by default. Create/Join require an explicit
HTTPS operator URL and a separate unchecked-by-default relay-consent checkbox.
Go validates consent before any map request or socket. The operator can observe
the device's IP address, availability and ordinary connection metadata; DERP can
relay encrypted pairing setup. Strict direct-only is unavailable and is never
silently weakened. Merely opening or unlocking WhatsUp makes no pairing connection.

The supported map fetch rejects redirects, credentials, query/fragment additions
and private destinations, bypasses environment proxies and bounds its response.
Relay destinations are resolved and pinned to the selected profile. A peer's
embedded relay metadata cannot introduce another outbound service. No public
service is silently selected. Upstream documents `https://tailcat.dev/derpmap.json`
as its default in the
[pinned source](https://github.com/tailscale/tailcat/blob/e00cdde6ff787f801361ff85ed085775c775d4c5/tailcat.go#L106-L109).
It is a manual operator-choice example only, with the same
metadata/relay consent. WhatsUp does not promise that service's availability.

The private bearer link is returned only for explicit Create/share, stays in
session memory, is copied only by a Copy action, and is cleared on handoff,
cancel, expiry, lock and unmount. A copied link remains in the OS clipboard until
the user replaces it; cancellation/expiry invalidates its server-side authority.
Paste does not navigate a browser or register an operating-system URL handler.
Ordinary status responses, local preferences and diagnostics never contain it.

The ongoing ContactServer.Start gate remains closed for endpoint/reconfirmation
integration, independently of invitation networking. No chat or file traffic is
enabled. The real Tailcat integration fixture is disposable and loopback-only;
it does not contact a public relay. Source checks, browser request lists and this
fixture do not establish a complete process-wide traffic audit, anonymity,
forced-relay delivery or strict direct-only enforcement.
