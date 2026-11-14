# Development, updating, and packaging

## One entry point

Use the root npm commands on both Windows and Linux. The runner itself has no
third-party dependencies, so `npm run doctor` and `npm test` work before app setup.
The commands invoke programs with argument arrays, not concatenated shell commands.

| Task | Command | Behavior |
| --- | --- | --- |
| Inspect environment | `npm run doctor -- --network` | Version checks and bounded HTTPS diagnostics; local report in `.local/doctor-<platform>-<arch>.json`. |
| Install a checkout | `npm run setup` | Frozen npm installs, Go verification, exact pinned Wails/scanner tools. |
| Develop | `npm run dev` | Native Wails reload loop using the local pinned CLI. |
| Validate implemented scope | `npm run test:app` | Frozen installs, workflow/Go/frontend tests, browser E2E and current advisory scans. Does not claim live messaging or release readiness. |
| Verify OS credentials | `npm run test:credentials` | Actual Credential Manager/Secret Service persistence and missing-key rejection with disposable validation credentials. |
| Run the Linux workflow from Windows | `npm run wsl -- test:app` | Synchronize a derived WSL snapshot, run the same Node command runner, and verify source equality before/after. |
| Build development candidate | `npm run build` | Frozen native Windows/Linux binary under `build/bin`; unsigned and not approved for distribution. |
| Preserve a private test build | `npm run build:review` | Frozen native build plus an immutable versioned development snapshot, manifest and checksums. |
| Validate | `npm run check` | Frozen installs, formatting, types, Go tests/race tests, integration/E2E, build, known-vulnerability scans. |
| Update dependencies | `npm run update` | Require clean git, discover stable versions, update exact pins/locks, install, and run all checks. |
| Repackage | `npm run package` | All checks, native packaging, versioned output, SHA-256 sums, toolchain/commit manifest. |
| Approve a distributable | `npm run verify:release` | Currently blocked until real signature/SBOM/update-verification gates exist. |

The desktop shell and backend components exist. Full `check` and `package` remain
blocked by incomplete product/security gates. `npm test` tests workflow safety
only. `test:app` is a separate implemented-scope check and still fails for a known
unpatched build dependency advisory; see docs/status.md. Browser tests do not
replace native webview testing. Windows component tests explicitly report when
they run without the race detector; full checks cannot pass without it.

## Develop the current implementation

Use the existing platform's nvm and the exact `.nvmrc` (26.13.1 LTS). In a noninteractive
Ubuntu shell, set `NVM_DIR="$HOME/.nvm"`, source `$NVM_DIR/nvm.sh` with `--no-use`,
then run `nvm use` from the checkout. Keep Windows and Linux Node installations,
node_modules and tool binaries separate. The Windows workspace is authoritative.
Use npm run wsl for Linux checks rather than editing or manually refreshing a
second working tree. It selects the existing Ubuntu distribution and its NVM,
creates a private derived snapshot under
~/.cache/whatsup-development/<workspace-id>, and synchronizes source before each
command. Native dependencies, generated output and private state are excluded.
Checksum comparison verifies source equality before and after the command; an
edit during validation makes the command fail and requires a rerun. A workspace
lock prevents concurrent synchronization/install/build operations. The old
~/Projects/whatsup-linux-dev copy is retained but is no longer a validation source.

Go 1.27.4 was verified from the official download feed/release history. This host's
Windows distribution is installed per user under
`%LOCALAPPDATA%\Programs\Go\1.27.4\go`; its bin directory is on user PATH. New
terminals inherit it; an already running app can have an older inherited PATH.
Ubuntu selects `/usr/local/lib/go/1.27.4/go/bin` from ~/.profile and ~/.bashrc. Its
old `/usr/local/go` installation was preserved. Do not replace an existing Go tree
by extracting a new release over it.

Run `npm run doctor -- --network`, then `npm run setup`, then `npm run dev` or
`npm run build`. Setup installs frozen npm/Go graphs, exact Wails/scanner tools in
`.local/tools/<platform>-<arch>`, and Chromium through the pinned Playwright CLI.
Go tool directives lock the CLIs and their transitive dependencies in go.mod/go.sum.
Setup builds from that graph, rather than installing package@version with an
independent potentially stale graph. The runner checks embedded compiler/module/
dependency versions and scans both CLI binaries as well as the application.
Npm lifecycle scripts stay disabled. Wails builds call the same Node runner from
reviewed Windows/Linux Taskfiles; ordinary builds do not run go mod tidy, float
dependencies, fetch remote scripts or expose a server-mode application.

Linux Wails 3.1 uses GTK4/WebKitGTK 6 with CGO and GLib 2.72 or newer. Ubuntu needs libgtk-4-dev,
libwebkitgtk-6.0-dev, build-essential and pkg-config. Go race tests also require
CGO and a C compiler. Browser libraries are needed for Chromium tests; native
desktop validation needs a display session. WSL builds and backend/race/profile
checks do not require WSLg. Native UI testing belongs on an Ubuntu Desktop
machine or VM. A usable Linux Secret Service session is separately required for
local identity unlock; headless WSL must fail closed if it is absent. See
docs/storage.md for paths and persistence limitations.

Actual dependency versions are in go.mod/go.sum and frontend/package-lock.json.
The Wails Go module and npm runtime both use 3.1.0. TypeScript stays at the newest
reviewed compatible 6.x because svelte-check does not yet support 7.x. The update
runner respects these explicit major constraints, resolves peers, and fails on
incompatibility. Tailcat and its reviewed transport modules stay pinned as
documented in docs/transport-review.md; their movement needs source/API review.

The owner recorded the first local Git commit on main. No remote is configured.
Follow-up development changes are left uncommitted for review. Setup, development,
component tests and ordinary native builds work without a commit. Update and packaging require an existing
clean checkout and commit. They do not initialize Git,
commit, reset, create a remote, or discard work. `applicationReady` and
`transportIntegrationVerified` must stay false until actual product/security and
live direct/relay failure-path integration gates exist and pass.

Windows/Linux component CI and Dependabot are configured for the manifests that
now exist. CI has not run remotely, and currently the npm advisory gate would fail.
The application has no selected public license, publisher or signing credentials.

## Run and review this workspace

The owner created the first local baseline commit; see [baseline notes](first-commit.md).
No push or publication occurred. The update command requires a clean Git checkout.
Recording a source
baseline does not open the application's release or transport gates.

In a fresh Windows PowerShell terminal:

```powershell
Set-Location "$HOME\Documents\say-whatsup"
$whatsupNvm = [Environment]::GetEnvironmentVariable('NVM_HOME', 'User')
$whatsupGo = Join-Path $env:LOCALAPPDATA 'Programs\Go\1.27.4\go\bin'
$env:Path = (Join-Path $whatsupNvm '.nodejs') + ';' + $whatsupGo + ';' + $env:Path
node --version
npm run doctor -- --network
npm run dev
```

The NVM shim selects the exact project version from .nvmrc. This host also has a
standalone Node in Program Files that otherwise takes precedence; the PATH prefix
above affects only this terminal and its children. No global NVM default or
persistent PATH was changed, and older installations/global tools were preserved.
The Go prefix also supports terminals whose parent has an older inherited PATH;
Go's user PATH entry is already saved persistently.
The expected node version is v26.13.1. Setup has already run. For a fresh checkout
or changed lockfile, run npm run setup
before dev. If an existing terminal cannot resolve go, reopen it to inherit user
PATH. The current Windows Go executable is under
%LOCALAPPDATA%\Programs\Go\1.27.4\go\bin. The native development loop opens the app;
Ctrl+C stops the loop. Close an existing WhatsUp instance before starting another.

To run a local binary without the watcher:

```powershell
npm run build
.\build\bin\whatsup.exe
```

From that same Windows project terminal, run the Linux workflow through WSL:

```powershell
npm run wsl -- doctor --network
npm run wsl -- setup
npm run wsl -- test:app
npm run wsl -- build
```

The bridge supports doctor, setup, tooling-test, test:app, test:credentials,
build and build:review (the internal test-app/test-credentials names also work). It delegates to
scripts/whatsup.mjs; the shell helper only synchronizes
source and selects Linux NVM/npm. It does not introduce a separate build recipe.
Run setup once for the derived workspace or after dependency/tool changes. Linux
reports and build/bin/whatsup remain inside that snapshot. Do not edit its source;
the next bridge command replaces derived source from Windows. A failed source
equality check invalidates that run's evidence even if the child tests passed.

The bridge also copies doctor-linux-x64.json and validation-linux-x64.json back
to the Windows workspace's .local directory for review; check recordedAt before
relying on a report.

For native Ubuntu Desktop review, transfer the reviewed Linux bundle and its
SHA-256 checksums through the VM's configured shared folder. Use the scoped
development integration and launcher described in [native testing](native-testing.md);
a raw per-user executable can fail the host's WebKit sandbox policy. Use a
development candidate only for its documented scope. Runtime libraries include libgtk-4-1 and libwebkitgtk-6.0-4;
local unlock also requires the user's working, unlocked Secret Service. An
unavailable store remains locked without creating plaintext state. A host-side
SSH connection is test infrastructure, not a service exposed by WhatsUp.

The configured Ubuntu Desktop VM is the intended second app machine. Native
checks should cover settings, appearance, About/privacy text, local unlock/lock,
quit/reopen identity continuity and private WebKit directories. Later, once live
pairing is implemented, use independent Windows/Ubuntu processes and identities
for the real direct/relay/restart tests below. No current binary can exchange chat.

The native startup correction is covered by four browser regressions that use the
actual application, generated bindings and Wails HTTP transport with synthetic
status responses. In a current native binary the footer should change to
Desktop development build and Local storage locked, and Settings should enable
Unlock storage. Plain browser previews keep it disabled. Test real unlock/lock
and quit/reopen identity continuity separately; synthetic bridge tests cannot
prove OS credential access. Recopy a refreshed Linux binary from the shared
folder into the private review directory before retesting an older VM install.

Run npm run test:credentials in the desktop user's session to test the real OS
provider. An SSH session must use that user's actual session bus; do not invent a
plaintext fallback or treat an absent Secret Service as a successful test. The
test uses a separate random disposable credential and removes it afterward.
It is a mandatory step in the eventual full check/package gate, separate from
component tests that use synthetic providers.

Use npm test for the dependency-free workflow tests. Use npm run test:app for Go
components, types/formatting, frontend unit/browser tests and all advisory scans.
It currently exits nonzero on the documented unpatched LOW deepmerge advisory,
after reporting the functional test results. The reports are
.local/validation-win32-x64.json and .local/validation-linux-x64.json in their
respective workspaces. npm run check/package remain blocked by broader readiness
gates. Do not interpret a red readiness/advisory result as a successful release.

Start with docs/status.md to see implemented behavior, evidence and the remaining
integration sequence. The installed 0.0.2 apps still provide appearance/settings
and local identity storage; their executables are unchanged by the newer pairing
source work. Backend tests do not make those installed apps usable messengers.

## Pairing source and test scope

The current source exposes six typed desktop methods: GetStatus, UnlockStorage,
LockStorage, GetPairingStatus, ApprovePairing and DenyPairing. Pairing snapshots
contain public request state, an opaque request ID, fingerprints and expiry.
Approve/Deny act only on the current request. No invitation bearer, private key,
PSK or generic transport operation crosses those methods.

The confirmation popup requires an authenticated, backend-reserved request;
fingerprints are not presented merely because a TLS handshake succeeded. There
is no mock/sample trigger in the app. The native initiate-pairing controls remain
disabled because Tailcat's privacy/start gate is still closed. Source-level popup
support does not claim a complete two-device pairing route.

Mutual consent saves encrypted pending setup only. Pending is not an active
contact and cannot open chat or a tunnel. Reopening storage can show an inert
incomplete-setup notice with Discard setup; it does not restore an invitation or
repeat approval automatically. This UI is separate from eventual durable
activation and transport integration.

Use the existing `npm run test:app` and `npm run wsl -- test:app` commands. The
browser suite now has 19 tests, including pairing interactions through actual
generated bindings and synthetic Wails HTTP responses. Keep that evidence
separate from real-TLS/coordinator backend tests and owner-driven native checks.
The native owner checks recorded for 0.0.2 do not verify the newer confirmation
popup, credential exchange or two-device behavior.

No app-version bump or installed-binary refresh follows from these source tests.
The new source does migrate schema 1/2 vaults to schema 3 when opened. Earlier
0.0.2 binaries cannot read that newer schema, so an executable-only rollback is
insufficient. Start migration QA with a disposable OS account/profile; establish
a reviewed recovery plan for existing encrypted state and its matching OS
credential before a future binary opens it. No automatic downgrade or
backup/restore UI exists. See [the native migration handoff](native-testing.md#state-migration-before-a-newer-binary).

## Update discipline

**Normal builds are frozen; update runs are deliberate.** `npm run update` checks
current stable LTS Node and stable Go, discovers current compatible npm versions
and the Wails release, aligns .nvmrc/engines/CI inputs, writes exact locks, updates
Go modules and runs setup/full validation. It requires the current runtimes to
already be installed and selected through their normal managers; it reports the
required version before changing files if a runtime is behind.
The Wails npm runtime remains matched to the selected Go release.
The command does not commit, push, publish, merge, or deploy.

If an update fails, leave and inspect the diff; fix compatibility/security issues
and run `npm run check`. Do not automatically reset unrelated work or paper over
failures with older insecure dependencies. No known-vulnerability exception is
silently accepted; document any requested exception and its scope explicitly.

Tailcat's reviewed post-release revision needs a source/API/security review before
it moves. Go updates do not automatically select arbitrary latest-main code.
shadcn-svelte component sources are checked-in application code: bumping its CLI
does not update copied components. Review/regenerate and test those deliberately.
Review scanner/toolchain and transitive updates too. Refresh minimum patched
versions and CI pins as the supported toolchain changes. No nightlies by default.

## Meaningful application tests

Unit tests must cover atomic single-use invitation redemption, server-side expiry,
revocation, third-peer rejection, approved key binding, frame limits, durable queue
acknowledgements/deduplication, attachment naming/quotas, and safe preview handling.

Integration tests run two independently instantiated applications with disposable
identities/state. Cover real direct and forced-relay paths; one app offline; both
restarting; NAT/network changes; lost acknowledgements; concurrent pairing; an
unknown third peer; and file interruption/resumption/cancellation. Test direct-only
under an abrupt path loss while TCP is retransmitting, and assert at the relay
that zero application data packets pass. Discovery packets alone are allowed.
Do not replace this evidence with a mocked 'direct' label.

Frontend checks cover keyboard navigation, empty/offline/queued/error states,
theme settings, invite approval/expiry, attachment acceptance, and real bridge
integration. Browser tests do not replace Windows/Linux native webview, tray,
notifications, installer, and close-to-tray QA.

## Packaging and release

`npm run package` produces a local candidate under
`artifacts/<app-version>/<platform>-<architecture>/`, with a manifest and hashes.
Existing versioned output is preserved rather than overwritten. Native package
commands come from the checked-in Wails Taskfile. Different OS/compiler/signing
stacks require native CI jobs; one Windows executable is not a Linux package.

A local package is not automatically distributable. A release pipeline must:

1. Build from a clean immutable commit and frozen graph on Windows and Linux.
2. Run full tests and current advisory scans, preserving failure evidence.
3. Generate an SBOM/license report, dependency/Go build information, and checksums.
4. Sign Windows packages and the Linux distribution/update artifacts with owner-
   selected credentials; sign the updater manifest with a dedicated update key.
5. Verify signatures, platform/architecture binding, hashes, rollback policy, and
   installation/update behavior; exercise tampered artifacts and manifests.
6. Publish only after all gates pass, to an owner-selected destination.

Current `verify:release` fails deliberately because those signature/SBOM/update
checks have not been implemented. Signing keys, certificates, private repo names,
relay addresses, identities, and history must never be put in the public source.
The update feature must never load or install an unsigned fallback.

## Reproducibility expectations

Record exact app/CLI/tool versions, lockfiles, compiler/platform details, and commit.
Use deterministic frontend builds and Go `-trimpath`/controlled build metadata in
the generated packaging tasks. Timestamped native signatures and installers may
prevent byte-for-byte equality: distinguish a repeatable source/dependency build
from a verified bit-reproducible binary. Claim either only after testing it.
