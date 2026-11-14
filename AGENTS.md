# WhatsUp — agent guide

## Read first

Read this file, `README.md`, `SECURITY.md`, `docs/architecture.md`, and
`docs/development.md` before changing code. `docs/status.md` distinguishes
implemented behavior from planned behavior. Update that status when work changes.

WhatsUp is a private, one-to-one desktop messenger for Windows and Linux.
The intended stack is Go + Wails v3 + Svelte 5 + TypeScript + Tailwind CSS.
Use selected shadcn-svelte components where useful. Follow the supplied dark,
teal chat reference while supporting light, dark, and alternate accent themes.

## Current state

The repository began with workflow and architecture only. It now has a desktop
shell, encrypted local identity and tested backend domain components. Live mutual
pairing, message delivery, attachments and installers remain incomplete. Read
docs/status.md for the exact implemented and verified scope.
Do not describe tooling checks as application, encryption, or network verification.
Go/package installation and WSL access were blocked in the initial agent session.
Run `npm run doctor -- --network` to check the current environment; do not assume
those restrictions still apply or mistake sandbox failures for host defects.

## Project layout

- `frontend/`: Svelte interface, public generated bindings and frontend tests.
- `internal/`: Go identity, pairing, chat, transport, storage, desktop and diagnostics
  components; see docs/status.md for integration boundaries and planned features.
- `scripts/`: dependency-free, cross-platform Node development commands and tests.
- `config/`: reviewed tool versions and release policy.
- `docs/`: requirements, decisions, current status, and operating instructions.
- `.github/`: dependency update and CI configuration.

## Mandatory product and security invariants

1. One conversation has exactly two approved identities. There is no group chat.
2. Invitations expire (default 10 minutes) and can be redeemed only once. Enforce
   both on the Go side atomically, not in UI state. Restart invalidates outstanding
   invitations. Reject replay, duplicate/concurrent redemption, and malformed links.
3. A pending pairing has no access to chat history, messages, or attachments.
   Pin the approved peer's identity. A display name is not identity verification.
4. Pairing credentials are separate from ongoing contact/session credentials.
   After pairing, tear down the invitation listener and reject its credentials.
   Never admit arbitrary clients to an established contact's listener.
5. WireGuard pre-shared keys are random, per contact, and enabled. Do not add a
   switch that silently weakens default encryption. Never invent cryptography.
6. Direct-only mode permits discovery/bootstrap metadata through DERP but forbids
   application data through DERP or another peer relay. Enforce at the transport
   packet boundary in both directions. Polling a status flag before writing is
   insufficient: fallback and TCP retransmission can race that check. Until a
   supported adapter or reviewed patch passes failure-path tests, mark this mode
   unavailable and fail closed. Never relabel 'direct preferred' as 'direct only'.
7. Queued messages remain local and encrypted at rest, retry with bounded backoff,
   and are removed only on authenticated acknowledgement. Stable IDs deduplicate
   after reconnect. Both devices must run to exchange data without a mailbox.
8. Files require receiver approval, bounded size, safe names, bounded chunking,
   authenticated integrity, cancellation, and crash-safe temporary files.
   Never auto-open files or accept peer-controlled save paths. No executable HTML,
   SVG, or active Markdown preview. Disable remote images and HTML in Markdown.
9. No shell/SSH service, command execution, generic filesystem API, LAN proxy,
   port-forwarding service, telemetry, or public content server in the app.
   Bind the minimum typed Wails bridge methods. Keep secrets out of the frontend.
10. A Wails process is not an OS sandbox. Document runtime privileges accurately.
    Do not promise isolation, anonymity, forward secrecy of local backups, or
    security certification without implementation and evidence.
11. Private keys, pre-shared keys, live invitations, message/history databases,
    attachments, backups, signing credentials, and personal endpoints never belong
    in git, CI logs, fixtures, screenshots, or release bundles. Use synthetic data.
12. DNS TXT publication of a bearer invite is disabled. Public discovery requires
    a separately reviewed, authenticated design and contains no invite secret.
13. Application/tool telemetry stays off by default. Go must report telemetry
    `off` on each development host; use its supported persistent control, not an
    invented environment opt-out. Keep local troubleshooting diagnostics bounded
    and redacted, with no automatic upload. The owner explicitly accepts Windows
    WebView2's documented vendor diagnostics limitation; do not expand that
    exception to WhatsUp analytics, remote crash reports, or other services.

## One workflow

- `npm run doctor -- --network`: environment and registry diagnostics.
- `npm test`: dependency-free workflow tests; NOT app tests.
- `npm run test:app`: implemented component/frontend tests and advisory scans;
  NOT proof of a complete messenger or release readiness.
- `npm run build`: frozen native development binary; NOT a distributable release.
- `npm run check`: full formatting, type, test, build, and known-vulnerability gates;
  deliberately fails until the application and its checks exist.
- `npm run setup`: install the committed, exact dependency graph and pinned CLI.
- `npm run dev`: run the native Wails development loop.
- `npm run update`: update dependency candidates in a clean git checkout and run
  the complete check. Leave a reviewable diff; do not commit, push, or publish.
- `npm run package`: run full checks, package locally, and write SHA-256 checksums.

The command runner is `scripts/whatsup.mjs`; it must work on Windows and Linux.
Do not add parallel shell-specific build recipes that drift from it.
Keep normal builds frozen. Resolve current stable versions only during an explicit
update. Commit npm lockfiles and `go.sum` once dependencies are resolved. Do not
make up lockfiles, hashes, vulnerability scan results, or passing checks.

## Dependency maintenance

Use current supported stable tools and current patched dependencies. Recheck
upstream at implementation/update time, including transitive dependencies and
GitHub Actions. Source release observations in `config/toolchain.json` are a
dated research snapshot, not a resolved application lockfile. Tailcat's main
branch contains post-release API/security changes: choose and document a reviewed
immutable revision; do not assume the latest tag contains the current API.

Use full commit SHAs for third-party CI actions. Dependabot proposals need tests;
security-sensitive changes and major upgrades need review. Do not auto-merge.
Never run a remote install script or unpinned `npx` invocation in a release build.
Do not disable TLS, module checksum verification, audits, or tests to make updates
pass. Network failures are failed checks, not clean vulnerability results.

## Validation and release

Follow `docs/development.md`. Security behavior requires meaningful adversarial
tests and real network integration tests. Test two independent app processes,
direct and forced-relay paths, path changes during transfer, and restarts.
Packaging must fail if app tests, frontend tests, frozen installs, or security
scans are absent or fail. Signing/updater configuration is not a release signature.
Ship an SBOM, signed platform artifacts, and a signed update manifest before
enabling in-app updates. Never fall back to an unsigned update.

Do not create a remote repository, publish a release, deploy a relay, or choose a
public license unless instructed. Future public release is intended; keep all
committed material suitable for public inspection now.
