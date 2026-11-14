# Implementation status

Recorded 2026-11-14 (America/Los_Angeles).

## Implemented

- Native Wails v3 entry point and nine generated typed bridge methods:
  GetStatus, UnlockStorage, LockStorage, GetPairingStatus, ApprovePairing,
  DenyPairing, CreateInvitation, JoinInvitation and CancelInvitation. Status and
  decision DTOs contain no bearer or transport secrets. Only explicit Create
  returns a private invitation link for transient sharing. Identity/contact/vault
  objects, general filesystem access, messages and files remain unbound.
- Svelte 5/TypeScript/Tailwind interface with an empty conversation layout,
  search, keyboard-accessible settings/About dialogs, dark/light/system appearance
  and teal/blue/rose accents. No sample people/messages or fake delivery indicators.
  Appearance is the only state persisted by the frontend.
- Desktop startup listens for Wails' runtime-configuration readiness event and
  checks immediately after subscribing. Late Windows/Linux injection now reaches
  GetStatus instead of leaving the native window in browser-preview mode.
  Duplicate or premature events cannot create duplicate startup calls; an initial
  status failure preserves the desktop retry path. The listener is removed on
  component teardown. Plain browsers still have no desktop storage actions.
- A backend-only TLS 1.3 pairing channel authenticates both Ed25519 identities
  over a supplied connection, pins the invitation owner and exact invitation ID,
  and requires reciprocal fingerprint confirmation. Its opaque live consent
  capability alone grants no chat or transport activation. Authenticated invitation
  reservation derives the candidate from that channel, retains bearer/expiry/
  single-use checks and rejects legacy immediate approval/teardown. A typed
  reservation ACK precedes fingerprint presentation on both sides. The coordinator
  owns joint TLS/invitation cancellation; the desktop invitation controller owns
  explicit transport creation and dialing.
  See [pairing protocol](pairing-protocol.md).
- Reserved requests drive a simple Approve/Deny confirmation dialog with both
  full fingerprints. Deny has default focus, Escape denies, and stale request IDs
  or async responses cannot approve/restore another request. Polling stops on
  storage lock/unmount. No sample prompt or browser-persisted pairing state exists.
  Restored pending or committed setup has a nonmodal Discard/reconnection notice,
  never a renewed approval prompt or automatic connection.
- After reserved mutual consent, the transport creates distinct node keys and
  one inviter-generated shared PSK/contact ID. Typed session-bound exchange saves
  encrypted pending records before prepared ACKs. Cancel joins in-flight saves
  before removal; shutdown joins runs/actions before vault closure. Pending records
  remain inert across reopen and grant no Contact, chat or transport authority.
  Durable completion commits the exact identity/node/PSK association on both sides
  before one-shot runtime promotion. Matching terminal receipts, TLS close-notify
  and bounded supported TCP drain preserve queued final frames before invitation
  teardown. Denial is acknowledged in either direction, including after one prior
  approval. Malformed traffic, shutdown and cancellation retain immediate abort.
  A lost connection or crash can leave inert pending/committed state; this is not
  an atomic transaction across machines. Restored-session reconfirmation remains
  unfinished.
- Native Create/Paste invitation controls require unlocked storage, a separately
  selected trusted HTTPS relay-map URL and unchecked-by-default relay consent.
  Launch, unlock and opening the dialog do not fetch a map or start Tailcat. The
  form has no default service; this checkout deploys none. The private one-use
  link and URL are session-only; only an explicit Copy action writes the link to
  the clipboard. Cancel, expiry and storage lock retire owned work. Completion is
  contact setup only: messaging, files and ongoing contact networking remain off.
- Bounded local diagnostic recorder, warning/error/panic redaction, and private
  per-app WebView profiles on Windows and Linux. Linux startup prepares private
  data/cache roots and verifies the paths selected by the actual WebKit session;
  mismatched cached paths, shared modes and foreign ownership fail closed.
  Go telemetry is off on the Windows/WSL development hosts; automatic
  npm version/install-audit requests are disabled. Supported upstream transport
  collector controls are configured. The reviewed local Tailcat privacy patch
  disables optional DERP application statistics and bearer-address debug logging.
  Supported build tags omit inherited proxies, router port mapping and captive-
  portal probes; a custom build missing these controls cannot start invitations.
  WebView2's documented vendor diagnostics are an explicitly accepted exception.
  See [privacy controls and local log locations](privacy.md).
- OS-credential-protected encrypted application snapshot: explicit unlock, AES-256-GCM,
  random nonces, bounded authenticated reads, ciphertext-only temporary writes,
  file flush, process writer exclusion and fail-closed credential/corruption behavior.
  Schema 4 coordinates identity, locally approved contact pins, per-contact chat,
  pending setup and committed contact credentials under one vault mutex. Scoped
  adapters prevent cross-section replacement; raw access and stale writes are rejected; uncertain saves poison
  the coordinator until reopen. Exact schema 1/2/3 state migrates without changing
  its seed, wrapping credential, pins or chat. Bounded pending/committed sections
  validate credential associations but cannot restore live transport authority.
  Older binaries reject migrated schema 4. The historical 0.0.2 owner QA does not
  validate this migration; native 0.0.3 QA is pending. See
  [storage design and limits](storage.md).
- Backend identity/invitation domain: Ed25519 fingerprints; canonical bounded private
  links; random expiring one-use invitations; atomic single-candidate reservation;
  full fingerprint approval; denial/replay/restart invalidation; synchronous listener
  teardown and one pinned peer. Invitation endpoints are typed canonical bounded
  Tailcat addresses with validated keys/PSK/relay metadata. Parsing performs no
  resolution or network I/O. Local approval neither establishes reciprocal network
  authentication nor generates duplicate ongoing credentials.
- Backend chat domain: bounded canonical text frames/history, stable random IDs,
  encrypted snapshot integration, durable outbox/acknowledgement/deduplication,
  pinned-peer authorization, bounded jittered retries, quotas and safe save failures.
  Display controls, bidi overrides, format characters and invisible fillers are
  rejected before encoding/acceptance; ordinary RTL letters and combining accents
  remain valid. Join controls are currently rejected too. Actual chat-domain tests
  exercise the coordinated encrypted adapter. It is not connected to the desktop
  messenger controls or a network session.
- Dedicated live Tailcat invitation adapter with separate ephemeral node/PSK,
  fixed TCP port 48730, bounded bootstrap peer admission, one accepted stream and
  joint revocation. No SSH, forwarding, UDP proxy or public content service is
  enabled. Explicit HTTPS operator profiles use normal TLS, bounded maps, vetted
  public IPs pinned before startup, no redirects and no inherited proxy. Unknown
  regions, private targets and mismatched peer-embedded relay metadata are
  rejected before dialing; no default public map is fetched. Runtime profiles
  are opaque and immutable. Strict direct-only remains unavailable and fails closed.
- Ongoing-contact transport remains unbound and unavailable. Its existing narrow
  adapter requires an enabled per-contact PSK, exact peer admission/recheck and
  one accepted stream; promotion receives the exact mutually committed bundle,
  rather than generating another PSK. Real invitation integration now exercises
  Tailcat/WireGuard against a disposable in-process TLS relay; this does not prove
  ongoing contact delivery, two-device operation or forced relay/direct paths.
- One command runner for frozen setup, dev, workflow/component checks, native
  development builds, deliberate updates and blocked full/package/release gates.
  Exact CLI versions are verified from build metadata and isolated per platform.
  Builds use frozen npm/readonly Go; no floating or remote install scripts.
  The Windows WSL bridge automatically derives a Linux snapshot, excludes native
  dependencies/private data, locks concurrent use, and verifies source equality
  before and after commands. One source workspace remains authoritative.
- Real manifests/locks, current scanner pin, reviewed immutable transport revisions,
  full-SHA-pinned component CI and Dependabot for the manifests that now exist.
  No remote repository exists and no GitHub CI has run.
- The owner recorded the first local main-branch commit, 27e4c48. No remote is
  configured. Repository text uses explicit LF endings; copied Wails build assets
  retain their upstream MIT notice. The library-only Tailcat source replacement
  retains its BSD-3-Clause license and an upstream/local file-hash manifest; the
  workflow rejects unexpected source/replacement changes. See [baseline notes](first-commit.md).
  The owner authorized the subsequent local native integration checkpoint,
  ef3e142. The next pairing workflow checkpoint is 7a0132f. Current integration
  work remains a separate reviewable change; no remote or publication was added.
- The first application/test version is 0.0.1. Patch increments identify new
  delivered test revisions; rebuilds use separate timestamp/source build IDs.
  Private 0.0.3 is the next delivered pairing-test revision; the previously
  installed 0.0.2 apps establish historical native evidence only. Minor and major
  changes are deliberate milestones. Native metadata and runtime
  versions are checked together. See [version policy](versioning.md).
- A build:review command preserves immutable Windows/Linux development candidates
  under artifacts/development with checksums, manifests, base commit, dirty-source
  state and a digest of actual source inputs. Derived WSL source is verified
  against the Windows inventory. Release/package gates remain independent.
- The initial paired 0.0.1 review builds have matching source digests and verified
  executable/manifest checksums. The first Linux candidate was installed per user
  in the VMware Ubuntu Desktop VM; it was superseded by the scoped 0.0.2
  installation described below. Windows has a Start-menu launcher selecting its
  retained review executable. Both are named WhatsUp (Development).
  The VM's real Secret Service roundtrip/missing-key rejection and actual headless
  WebKit profile tests passed on the 0.0.1 test binaries. Actual UI checks are
  recorded separately below. See [native testing](native-testing.md).
- The owner's Ubuntu GUI attempt exposed a missing installation requirement:
  Ubuntu's restricted unprivileged-user-namespace policy denied Bubblewrap's UID
  map during real WebKit process startup. Earlier profile-only checks did not
  spawn that process and therefore missed this failure. The per-user raw-copy
  installation is not sufficient evidence of native launch readiness.
- Test revision 0.0.2 adds an explicit Linux software-rendering option and a bounded
  native renderer probe that loads synthetic local HTML and verifies JavaScript.
  A narrowly scoped root-installed development profile permits WebKit's required
  namespaces on restricted AppArmor hosts. The profile does not confine WhatsUp's
  Go process or disable WebKit's sandbox. Installation and current native results
  remain separate from release packaging; see [Linux support](linux-support.md).
- The owner completed the 0.0.2 Ubuntu integration setup. SSH verification confirms
  root-owned 0755 stable executables, matching installed hashes, a loaded
  application-specific profile and a launcher selecting software rendering.
  The global AppArmor user-namespace restriction remains 1. The installed real
  WebKit renderer/local-JavaScript probe passes in the desktop user's session.
  The owner then confirmed that the 0.0.2 app opens, unlocks and passes the
  lock/quit/reopen/unlock identity-continuity check. SSH inspection confirms the
  running app uses the expected application-specific AppArmor profile; its
  per-user data and cache roots are owned by the desktop user with mode 0700.
  WebKitCache is also 0700. The local 0.0.2 log records startup, unlock, lock,
  shutdown and another startup/unlock using only the six allowed fields and
  finite event codes; no identity fingerprint was retrieved or recorded.
  VMware shared files are staged on local ext4 before sudo;
  the root installer revalidates them and reuses only byte-identical archives.
- The Linux menu launcher now uses desktop ID whatsup.desktop to match GTK's
  existing program name, with a hidden alias for the earlier development ID.
  The unprivileged setup supports optional desktop/GNOME dock shortcuts while
  preserving other favorites. Both were installed on the VM and validated through
  GIO, including executable/trusted desktop metadata and Terminal=false. The owner
  confirms icon launch, window close and icon relaunch work without a terminal.
  The installed 0.0.2 ELF has the production build tag and no development watcher;
  no native code change or version increment was needed for this launcher fix.

## Environment established

- Official Go download JSON and release history both identify Go 1.27.4 as latest
  stable, published 2026-11-10. Official Windows AMD64 ZIP/Linux AMD64 tarball were
  downloaded and checked against the published SHA-256 hashes.
- Windows Go 1.27.4 installed under
  %LOCALAPPDATA%\Programs\Go\1.27.4\go with persistent user PATH. A fresh child
  PowerShell using saved machine/user PATH resolves the installed executable.
- Ubuntu Go 1.27.4 installed under /usr/local/lib/go/1.27.4/go. Both ~/.profile and
  ~/.bashrc select its bin directory; login/interactive shells verify it.
  Old Go 1.23.3 under /usr/local/go remains intact.
- Node 26.13.1 LTS is selected through the existing Windows/Ubuntu NVM installations
  and pinned in .nvmrc, engines, Node types and both CI jobs. The official feed
  identifies it as the current patched LTS. Older runtimes/global tools were
  preserved; Windows NVM's shared default was not changed. This host's standalone
  Node otherwise precedes NVM, so Windows project shells need the documented
  process-local shim PATH prefix. Separate platform node_modules/caches are used.
- WSL Ubuntu GTK4/WebKitGTK 6 development libraries, compiler/pkg-config and pinned
  Wails/govulncheck are installed. The maintained Linux path is the automatic
  ~/.cache/whatsup-development/<workspace-id> snapshot selected by npm run wsl.
  The old ~/Projects/whatsup-linux-dev copy is retained but no longer used as a
  validation source. WSL builds/backend/race/profile checks need no display or WSLg.
- SSH key access to the configured VMware Ubuntu Desktop VM is verified, along
  with its active desktop session, shared-folder mount and GTK/WebKit runtime.
  It is the intended second machine for native Linux UI and eventual Windows/
  Ubuntu chat tests. Connectivity checks do not establish native app behavior.
- Doctor passes Node/Go and npm registry, Go proxy and GitHub HTTPS checks on both
  platforms. These are environment diagnostics, not app network verification.
- Native Windows Wails doctor recognizes WebView2 and reports ready for development.
  Native installer/signing tools and signing identities remain unconfigured.

## Validation and advisory gates

### Current 0.0.3 integration evidence

- Full-stack Windows integration passed five repetitions: 25 real pairings in
  81.570 seconds. Each uses actual Tailcat/WireGuard TCP, a disposable in-process
  TLS DERP server, identity TLS and separate encrypted temporary vaults. Five
  mutual completions produce reciprocal committed records with the exact shared
  PSK and two identity-bound nodes, with no pending records. Twenty denials cover
  both roles, before approval and after the other person approved; both stores
  remain empty, remote prompts clear and stale approvals fail. Invitation
  publication/admission retires exactly once. The wrapping-key providers are
  disposable test doubles; this does not revalidate the OS keyring.
- The real adapter regression suite passed in 16.610 seconds, including bidirectional
  streams, context cancellation, one-stream admission, concurrent close, pending
  Accept cancellation, graceful FIN/ACK drain and immediate abort during drain.
  The loopback profile exists only in test builds and pins the exact TLS server
  certificate; production rejects private targets and insecure test overrides.
  The plain-build test proves networking stays closed without supported omission
  tags. Targeted Windows vet and formatting checks pass.
- Repeated integration exposed dropped terminal data when an in-process Tailcat
  engine was destroyed immediately after Close. The regression now exercises
  authenticated completion/denial receipts, TLS close-notify and role-ordered,
  deadline-bounded supported TCP draining. No grace sleeps or relaxed assertions
  were used. Immediate abort remains available when graceful retirement fails.
- The complete 0.0.3 checks run through `npm run test:app` on Windows and
  `npm run wsl -- test:app` for synchronized Linux race validation. Each records
  a private, source-digest-bound `.local/validation-<platform>-x64.json` only
  after all functional gates pass, with separate results for all five advisory
  scans. A failed scan still makes the command fail. Earlier platform passes
  below are historical evidence; use the current matching reports for this
  snapshot. Native 0.0.3 owner QA and two-machine pairing remain unrecorded.
  No public relay was contacted or deployed by the integration tests.
- The completed Windows and synchronized WSL component runs passed 32 workflow
  tests, seven frontend unit tests, 32 browser tests, frontend formatting/types,
  production frontend builds, vet, all twelve application Go test packages and
  the nested Tailcat privacy tests. Linux used the race detector; Windows did not.
  Both source-bound reports identify digest
  `4c95ff1352e5199b84d600294b2f7d714b5b6337064dc0e327bd53a0a6119f59`.
  Each command returned nonzero only for the unsuppressed frontend deepmerge
  advisory; the other four scanner gates passed. Both frozen native 0.0.3 builds
  and retained artifact checksums passed. Synthetic bridge/browser results remain
  separate from native Wails, OS credentials and real two-machine pairing evidence.
- Local runtime inspection observed 0.0.3 startup and storage unlock on both the
  Windows host and Ubuntu Desktop VM. The deliberately redacted logs do not record
  pairing decisions, so this does not establish native mutual approval or messaging.
- Full product/release readiness remains blocked. applicationReady and
  transportIntegrationVerified remain false; local loopback success does not
  establish forced direct/relay, NAT/path-change or ongoing message delivery.

### Previous foundation validation

The native integration and earlier pending-pairing batches completed the Windows
and Linux component workflows, including twelve Go test packages and Linux race
testing. Those commands returned nonzero for the documented frontend advisory.
The Linux run verified source equality before and after validation. The following
results describe that earlier source unless a current result is recorded above.

- Twenty-five workflow safety tests cover runtime/dependency/tool-graph selection, review
  snapshot integrity/provenance, application-version alignment and workflow
  safety. Node syntax checks pass.
- Backend identity, invitation, chat, transport admission, desktop and storage tests
  pass on Windows. Windows has no configured C compiler and runs without -race.
- The earlier pairing-channel suite passed on Windows and with the Linux race
  detector. Real TLS tests reject wrong identities, invitation context, missing/
  malformed/expired certificates, TLS downgrade, unilateral consent, denial,
  duplicate decisions, malformed/replayed frames and cancellation. Expiry and
  guarded-close tests cover capability invalidation. Real loopback TCP tests
  prove authenticated reservation still has one winner, validates the bearer,
  blocks premature activation and closes the session through its retirement hook.
  An independent source review found a timer-publication race; the guarded fix
  and other targeted corrections were verified before these component runs.
  These tests do not exercise Tailcat, two devices or native pairing UI.
- All implemented Go component/process tests and vet pass on Ubuntu with the race
  detector, including storage, desktop and local diagnostics. This is separate
  from actual peer transport QA.
- Earlier bounded fuzzing passed: 388,069 invitation parser executions and
  120,174 chat frame executions. These exercise the updated typed endpoint/display
  control paths; they are not exhaustive protocol review or live-network evidence.
- Storage checks include nonplaintext persistence, nonce uniqueness, wrong/missing
  keys, tampering/truncation, limits, reopen, permissions and lock closure.
  Independent Go subprocesses verify actual process-level locking and restoration
  of the same synthetic identity. Synthetic providers do not prove OS keyring access.
- New envelope tests pass for migration, exact schema rejection without rewrite,
  malicious cross-section/pin substitution, encrypted content, concurrent contact
  sections, stale writers and pre/post-replacement failure poisoning. External
  integration tests use actual chat adapters and independent-process identity/
  chat/outbox restoration. Storage/desktop/process targeted tests pass on Windows
  and with the WSL race detector.
- npm run test:credentials passes against real Windows Credential Manager and the
  VMware Ubuntu Desktop Secret Service through its authenticated desktop session
  bus. Both use disposable validation credentials, encrypted reopen, deletion,
  then missing-key rejection without replacement. The Linux integration-test
  binary was transferred to the VM with its hash verified. No production identity was opened.
  This command is mandatory in the eventual full check/package gate. The WSL
  credential check fails because no Secret Service is available, as expected;
  it has no plaintext fallback and is not reported as a provider pass.
- Headless Linux native profile tests pass against actual WebKitGTK, including
  rejection of an already cached foreign profile. The runtime probe selected the
  app's XDG data/cache roots. This is profile-selection evidence, separate from
  native window QA.
- Local diagnostics tests pass on Windows and Ubuntu race testing: rotation bounds,
  process exclusion, private files, interrupted-write recovery, concurrency and
  adversarial slog data that must neither be retained nor evaluated.
- Frontend formatting/types, seven unit tests, nineteen Chromium E2E tests and production
  Vite builds are covered by the component workflow. Agent Browser previously checked empty
  and disabled states, settings, appearance changes and Escape handling.
- The reported native startup bug was reproduced by a failing browser regression
  before the correction. Four new browser tests mount the actual Svelte app and
  exercise generated bindings plus the real Wails HTTP runtime against synthetic
  status responses: late Windows/Linux injection with unlock/lock, pre-mount
  injection, and failed initial status followed by retry. Late-injection cases also
  reject premature events, deduplicate repeated readiness events and verify no
  identity status is persisted in browser storage. These are lifecycle/bridge
  transport tests; real native credential and restart behavior is verified
  separately from these synthetic responses.
- Eight new browser regressions exercise the confirmation dialog through the
  actual generated bridge and Svelte app with synthetic responses: reserved prompt,
  Approve/waiting, Deny/Escape, stale responses, lock/poll cancellation, malformed
  DTOs, expiry and inert pending discard. The full component runs pass all 19
  browser tests on Windows and Ubuntu, with zero frontend check warnings and
  successful production frontend builds. Real TLS/loopback coordinator tests
  pass on both, including the Linux race detector, and separately cover bearer ACK before
  fingerprints, joint closure, one PSK/two nodes, encrypted save/reopen, stale
  actions, rejected concurrent candidates, cancellation during save and shutdown
  waiting for persistence. These are not native/two-device pairing evidence.
- Pending schema tests pass for exact schema 2 migration preserving seed/key/
  pins/chat, canonical credential associations, encrypted save/reopen, conflicts,
  scope preservation, concurrency and uncertain pre/post-replacement failure.
  An independent review caught the node-key binary prefix issue and a competing
  candidate's manager-close hazard; both fixes have targeted regression evidence.
  The native 0.0.2 executables and real owner state were not replaced or migrated.
- Frozen npm run setup passes on both platforms, including exact Wails/scanner and
  Playwright browser installation. Npm lifecycle scripts remain disabled.
- Initial Go scanning found an affected compression dependency and graph advisories.
  Updated compress to 1.20.4, x/net to 0.62.0, x/crypto to 0.59.0 and compatible
  transitives. Final application govulncheck including tests reports no affected
  vulnerabilities.
- A binary scan also caught GO-2026-6865 in the latest Wails CLI's independently
  installed dependency graph. Both CLI tools are now Go tool directives in the
  resolved application module graph, rebuilt with its patched dependencies. The runner checks
  their compiler/module/transitive versions and rejects replacements/stale binaries.
  CLI binary scans report no affected symbols. The Wails CLI scan additionally
  reports package-only GO-2026-6905 in blakesmith/ar Reader, with no upstream fix.
  No affected symbols were found in that binary. This is recorded without a waiver.
- Frontend npm audit fails for LOW
  [GHSA-gcqx-w48m-w2h4](https://github.com/advisories/GHSA-gcqx-w48m-w2h4),
  deepmerge 4.3.1 through @sveltejs/vite-plugin-svelte 7.3.1. Npm reports two affected
  package entries for one advisory. No patched release is available. No suppression,
  exception, alias or unreviewed fork was added. test:app runs all five scans
  and fails if any fails. A functional component result is not a green full check.
- The component command runs five separate scans: root/frontend npm audit,
  application govulncheck, Wails CLI binary and govulncheck's own binary.
- Full check/package/native packaging and verify:release correctly fail closed.
  applicationReady and transportIntegrationVerified remain false.
- The previous Windows/Linux development binaries built from frozen dependencies. The
  Linux binary, checksum file, run instructions and disposable credential test
  were copied to the owner's VM shared folder for private review. Checksums and
  all native runtime-library resolutions pass on the VMware Ubuntu Desktop VM.
  The owner then reviewed the pre-fix Windows UI and found that late runtime
  injection left it in browser-preview mode with Unlock disabled. The readiness
  correction and its regression tests are included in both rebuilt binaries.
  The owner reports successful Windows native GetStatus, unlock, lock, real
  window close, fresh launch and unlock with the same identity fingerprint.
  Startup returns locked; Dark/Teal appearance was preserved. The real fingerprint
  is deliberately omitted from source and fixtures. The owner also reports
  successful Ubuntu Desktop 0.0.2 native unlock and restart identity continuity
  after installing the scoped sandbox integration and selecting software rendering.
  Earlier native process startup checks did not catch this frontend lifecycle
  bug. These historical native results do not validate 0.0.3, schema 4 or the new
  invitation controls. Visual redesign remains deferred.
- The Windows native process was additionally verified to use the dedicated
  WhatsUp webview directory; local logs contained only the six allowed fields.
  The Linux npm dev command started Vite on loopback and the native app under
  Xvfb, then stopped on the bounded test timeout. The review observed the WSL app
  data root at 0700 and separate WebKit cache root/WebKitCache at 0755. The new
  preparation checks reject shared cache modes. The two development cache
  directories were verified as owned by the WSL user, nonsymlink directories and
  tightened to 0700. The app does not silently migrate unsafe existing permissions.

## Not implemented or verified

- Native owner QA of private 0.0.3 on Windows and the Ubuntu Desktop VM, including
  schema 4 migration and actual two-machine Create/Join/Approve/Deny/restart.
  Local automated transport success does not establish native or public-service
  pairing reliability.
- Fresh authenticated reconfirmation and crash reconciliation of restored contact
  credentials, ongoing contact transport and revocation across reconnects. Pending
  and committed records restore only inert notices, without live authority.
- Binding exactly one active conversation and connecting chat queue/history/
  acknowledgement adapters to authenticated ongoing sessions. Durable setup
  completion is not message-delivery availability.
- Real two-process/device direct and forced-relay delivery, network reconnect,
  lost acknowledgements, NAT/path changes or retransmission tests.
- Strict direct-only packet enforcement. Never label direct-preferred as strict.
- Receiver-approved bounded attachments/images, safe previews, cancellation,
  crash/resume handling, encrypted attachment storage and search.
- Tray/background listening, native notifications, startup settings, backup/restore,
  signed application updates, and privacy lock beyond the local snapshot lock.
- Native installers, SBOM/notices inventory, signatures, signed update manifests,
  power-loss durability tests and full Windows/Linux native behavior QA.
- Public repository/remote settings/alerts, public license, relay deployment,
  signing credentials, release destination or published artifacts.

The owner ran the optional scaffold cleanup. All twelve listed unused asset/file
or platform-directory targets are confirmed absent, including the unused font
and its accompanying license. Active Windows/Linux build assets, build/windows
and frontend/src/app.css remain. No further cleanup action is required.

## Next integration sequence

1. Review matching Windows/WSL validation reports and complete native 0.0.3 migration/UI QA.
   Then exercise pairing between the Windows host and Ubuntu Desktop VM using a
   separately approved trusted service, without publishing or deploying a relay.
2. Add fresh authenticated reconfirmation and crash reconciliation before restored
   history access; bind one conversation, then connect the queue/deduplication/
   acknowledgement adapters to ongoing authenticated sessions. Saved records alone
   must never activate.
3. Investigate supported upstream packet hooks or a reviewed minimal direct-only
   patch, retain discovery and prove zero relayed payload during failure-path tests.
4. Add files/images, native background behavior, backup/update verification and QA.
   Resolve the upstream npm advisory before claiming green validation.
5. Enable full readiness only with passing actual transport/security integration,
   then implement/review installers, SBOM/signatures and updater gates. Nothing is
   published by the current commands.
