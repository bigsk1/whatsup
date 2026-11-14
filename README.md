# WhatsUp

A private, one-to-one desktop messenger in development for Windows and Linux, using
Go, Wails, Svelte, TypeScript, Tailwind, and Tailcat.

**Current state:** private test revision **0.0.3** adds explicit invitation creation
and joining, mutual identity confirmation and durable encrypted contact setup to
the native desktop shell. Live invitation setup uses a separately selected,
trusted connection service and requires relay consent. Messaging, files and
ongoing contact connections remain unavailable. This is development software,
not a usable or audited private messenger yet.
See [current status](docs/status.md) and the [architecture](docs/architecture.md).

The backend reserves the one-use invitation before showing either fingerprint,
authenticates both Ed25519 identities through TLS 1.3, and requires both people to
approve. It commits the exact agreed contact keys before retiring the separate
invitation transport. Saved setup cannot resume a live session after restart.
See the [pairing protocol boundary](docs/pairing-protocol.md).

## Consistent development commands

Run these from the project root, in PowerShell or a Linux shell:

| Command | Purpose |
| --- | --- |
| `npm run doctor` | Check Node, Go, project readiness, and pinned tool policy. |
| `npm run doctor -- --network` | Also check HTTPS access to dependency services. |
| `npm test` | Test the dependency-free workflow tools. |
| `npm run test:app` | Check implemented components, frontend behavior and vulnerability gates. |
| `npm run test:credentials` | Test the real OS credential provider using disposable validation credentials. |
| `npm run test:native` | On Linux, start a real WebKit process, load local HTML and verify JavaScript with the sandbox enabled. |
| `npm run wsl -- test:app` | From Windows, synchronize current sources and run the same Linux component workflow. |
| `npm run build` | Build a native development binary from the frozen graph. |
| `npm run build:review` | Build and retain a versioned private test executable with checksums and source provenance. |
| `npm run setup` | Install frozen app dependencies and the exact Wails CLI. |
| `npm run dev` | Run the native desktop development loop. |
| `npm run check` | Run the complete application validation gates. |
| `npm run update` | Refresh dependencies, test, and leave a reviewable diff. |
| `npm run package` | Validate, build native packages, and write checksums. |

`setup`, `dev` and `build` use the pinned Wails CLI. `test:app` validates the current
implementation without claiming messenger completion. `check` and `package` stay
blocked until real transport/security integration gates exist. Known dependency
advisories also fail validation; see [current status](docs/status.md).
There is no placeholder connection, sample conversation or fake delivery indicator.

The first application version is **0.0.1**. New delivered test revisions can use
patch increments such as 0.0.2; rebuilds use a separate timestamp/source build ID.
Minor and major versions are deliberate milestones. See [versioning](docs/versioning.md).
Review candidates stay under `artifacts/development/<version>/<build-id>/`;
they are unsigned executables, not release installers. See [native testing](docs/native-testing.md).
Test revision **0.0.2** established Linux sandbox integration and an explicit
software rendering mode. The new **0.0.3** pairing flow still needs native owner
QA on both machines. Linux runtime requirements and tested scope are documented in
[Linux support](docs/linux-support.md).

Windows remains the authoritative source workspace. The WSL command creates a
derived Linux snapshot with separate dependencies, checks source equality before
and after the command, and prevents concurrent use of that snapshot. Use WSL for
builds and backend/race checks without WSLg; use an Ubuntu Desktop machine or VM
for native Linux UI and later two-machine chat tests. See the
[run and review instructions](docs/development.md#run-and-review-this-workspace).

The interface can unlock local identity through Windows Credential Manager or
Linux Secret Service. An unavailable credential store leaves it locked. Appearance
preferences are the only data stored in frontend local storage. Unlocking does not
start a listener or enable messaging. Native windows close normally; tray behavior
is not implemented.

The current source uses encrypted schema 4 for identity, contact pins, chat,
pending setup and committed contact credentials. It migrates schemas 1/2/3
without changing identity or the wrapping credential. Older executables reject
the newer schema; preserve existing state and its OS credential, and follow the
[native migration test guidance](docs/native-testing.md) before trying this build
against existing data. Restored pending or committed records remain inert;
reconnection requires fresh authenticated peer confirmation, which is unfinished.

## Try the private pairing flow

Run `npm run dev` for the native development loop, or build with `npm run build`
and launch `build/bin/whatsup.exe` on Windows or `build/bin/whatsup` on Linux.
`npm run build:review` retains a versioned executable for repeatable private QA.
Linux installation and desktop integration use the
[native review instructions](docs/native-testing.md), including the supported
sandbox setup. Browser preview cannot unlock storage or create invitations.

Pairing needs a trusted operator's **HTTPS Tailcat relay-map URL**. This checkout
does not supply, choose or deploy a service. An example placeholder is not a
working endpoint. The service learns your IP address and availability and can
carry encrypted pairing traffic; accepting that disclosure is a separate choice
on each device. Strict direct-only pairing is unavailable.

1. Launch WhatsUp on both devices and unlock local storage in Settings.
2. On one device, open **Start a conversation**, choose **Create invitation**,
   enter the trusted connection-service URL and explicitly allow encrypted relay
   fallback. Continue to create the ten-minute, one-use invitation.
3. Use **Copy invitation** and send it privately to the intended person. The link
   is a bearer secret; keep it out of screenshots, issue reports and logs.
4. On the other device, choose **Paste invitation**, paste the link, independently
   enter the same trusted service URL and give relay consent.
5. Both devices show an Approve/Deny dialog only after authenticated reservation.
   Compare the full reciprocal fingerprints through a trusted channel, then
   approve on both devices. Either person can deny, including after the other
   person has approved.
6. Completion means the exact contact credentials were committed and the
   invitation transport was retired. Sending messages is still disabled. After
   restarting, saved setup shows that reconnection is required; it does not
   automatically contact a peer. Discard saved setup before starting over.

Launch, unlock and opening the invitation dialog do not contact a relay. The
service URL, unchecked consent and private link stay in transient UI state; only
an explicit Copy action writes the invitation to the clipboard. Automated tests
have exercised this protocol over a disposable local TLS relay. Two-machine
native pairing, public-service operation and forced direct/relay paths remain
unverified.

Normal builds consume exact versions recorded in the manifests and lockfiles. Updating dependencies is a
separate, explicit operation. See the [development and release workflow](docs/development.md).

## Privacy and local diagnostics

WhatsUp has no application analytics or remote crash reporting. Local troubleshooting
logs contain only fixed operational event codes, UTC time and public app/tool/platform
versions. They exclude message text, identity fingerprints, keys, invitations, peer
addresses, filesystem paths and raw bridge/error payloads. The active and previous
log are limited to 256 KiB each. They stay under the user's private application-data
directory and are never uploaded automatically. See [privacy controls](docs/privacy.md).

On Windows this app uses the system WebView2 with an app-owned profile under
%LOCALAPPDATA%\WhatsUp\webview, separate from Edge's cookies and storage. Optional
WebView2 diagnostic data follows Windows privacy settings; Microsoft documents that
required diagnostics may still occur. The owner accepts that runtime limitation.
The app preserves Wails' disabled SmartScreen setting for its bundled-content
renderer. It does not change system-wide Windows privacy or browser policies.

Linux uses the system WebKitGTK. Startup prepares private per-app data and cache
directories and verifies the paths selected by WebKit before opening a window.
Its diagnostic profile differs from WebView2. This is not a guarantee that the host
OS, runtime or network infrastructure sends no data.

Go telemetry is set to off on Windows and Ubuntu, including local tool counters,
and the workflow verifies that setting. Automatic npm version checks and install-
time audit submissions are disabled. Explicit vulnerability scans, dependency
downloads and requested environment/network checks still contact their services.
The reviewed local Tailcat patch disables its optional DERP application statistics
and bearer-address debug logging. Supported build tags omit inherited proxies,
router port mapping and captive-portal probes; invitation startup fails closed
when those controls are absent. See the
[transport review](docs/transport-review.md) and
[third-party notices](THIRD_PARTY_NOTICES.md).

## Intended experience

- Private invitation links/QR codes: one use, 10-minute default expiry, and
  approval of a specific contact identity.
- Direct-only message delivery with local queuing, or direct-preferred delivery
  with encrypted relay fallback. Strict direct-only is a transport implementation
  gate, not a UI toggle over stock fallback behavior.
- Text, Markdown/text attachments, approved image transfers, and image paste.
- Contact sidebar, clear connection/delivery indicators, local search, themes,
  tray unread count, background listener, and native notifications.
- Encrypted local state, explicit encrypted backups, and verified application updates.

Read [SECURITY.md](SECURITY.md) and [AGENTS.md](AGENTS.md) before implementation.
The owner recorded the local source baseline; see [baseline notes](docs/first-commit.md).
Retained upstream build assets are covered by [third-party notices](THIRD_PARTY_NOTICES.md).
No remote repository, relay deployment, signing key, or publishing target is set.
