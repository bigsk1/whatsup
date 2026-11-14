# Private native test handoff

The retained 0.0.2 apps test startup, appearance and encrypted local identity
storage. New source adds explicit invitation pairing, but messaging and attachments
remain disabled. Opening two apps does not establish a chat connection. GUI
testing belongs to the owner; the development workflow does not automate their
browser or desktop.

## Versions and retained builds

The first test version is 0.0.1. Use patch increments for later delivered test
revisions as needed; see [versioning](versioning.md). Do not increment minor/major
versions for every edit or build.
The current Linux startup-fix test revision is 0.0.2.

The next private pairing candidate is planned as 0.0.3, with retained builds,
source provenance and checksums. No owner-driven native pairing QA is recorded
for it yet. Installed 0.0.2 executables remain selected until an explicit reviewed
launch/upgrade. A new handoff must identify its source/build ID and repeat the
relevant checks; the older 0.0.2 results do not transfer to it.

The owner has verified native Windows unlock/restart identity continuity and the
same Ubuntu Desktop 0.0.2 workflow after the scoped Linux integration setup, using
software rendering on VMware. The installed Ubuntu app runs under its expected
AppArmor profile with the global namespace restriction still enabled. This
records the tested configuration; other Linux desktops and live messaging remain
unverified. The owner also confirms Ubuntu dock/desktop icon launch, close and
relaunch work without a terminal. Repeat the checklist for later app changes
that affect these paths.

`npm run build` writes the current executable to `build/bin/whatsup.exe` on Windows
or `build/bin/whatsup` on Linux. `npm run build:review` performs that same frozen
build and preserves a copy under:

```text
artifacts/development/<app-version>/<UTC-time>-g<base-commit>[-dirty]/
  win32-x64/ or linux-x64/
    whatsup.exe or whatsup
    build-manifest.json
    SHA256SUMS
```

No earlier platform candidate is overwritten or automatically deleted. The
manifest records the actual source digest, base commit and whether uncommitted
changes were present. Generated bindings and ignored outputs are excluded from
the source-input digest. Checksums cover the executable and manifest. They are
integrity metadata, not release signatures.

These private snapshots do not run or bypass full release packaging. Existing
advisory/product gates remain visible. A snapshot only claims component evidence
when its source digest matches the recorded component run; otherwise its manifest
says that checks were not run for that snapshot. Native UI review is a separate
owner check. No publication, signed installer, automatic update or relay is added.

From Windows, the Linux command is `npm run wsl -- build:review`. An optional
`--review-id <ID>` lets both platform builds use the same review folder. WSL
verifies its source against the authoritative Windows inventory and copies a
small latest-build pointer back to `.local/review-build-linux-x64.json`.

## Launching the apps

On Windows, the current native executable can be launched from the project root:

```powershell
.\build\bin\whatsup.exe
```

For a specific retained build, run its executable inside the versioned artifacts
directory. The installed Windows Start-menu shortcut is named WhatsUp (Development)
and selects the current review candidate. Installing a later candidate updates
that selection separately from building it. Running an executable requires no Node, Go, development server or
browser. Close another instance using the same local profile first.

The initial per-user raw-copy install failed Ubuntu's AppArmor namespace policy
when WebKit started its actual sandbox. On restricted hosts, the development
integration installer places reviewed executables under root-owned
`/opt/whatsup-development` and installs a narrowly scoped AppArmor compatibility
profile. The user's launcher opens that exact installed path. App data, cache,
identity and credentials remain per user. This is private development integration,
not a signed DEB/RPM or approved release installer. See [Linux support](linux-support.md).
The VM needs GTK4, WebKitGTK6 and the desktop user's unlocked Secret Service;
it does not need Go, Node or WSLg to run the installed app or probe.
The launcher is named WhatsUp (Development), with desktop ID `whatsup.desktop` to
match GTK's application name for dock/window grouping. The integration setup can
also create a desktop shortcut and pin the app to the GNOME/Ubuntu dock with
`--desktop-shortcut --pin-to-dock`. Those are opt-in user preferences; other dock
entries are preserved. A desktop environment that asks to trust the shortcut may
require its normal Allow Launching action.

Launch it from the application menu, dock or desktop without keeping a terminal
open. For terminal use, `gtk-launch whatsup` opens the same installed app;
`gtk-launch whatsup-development` remains a hidden compatibility alias. The
installed review executable has the `production` build tag and no development
watcher. Closing its last window quits the app. A separate `npm run dev` session
does keep its watcher running until stopped with Ctrl+C.

## Owner checklist

1. Launch the selected build. Confirm its expected version and retained build ID,
   Desktop development build, Local storage locked and enabled Unlock storage.
2. Open Settings and unlock storage. If the OS asks for credential access, handle
   that prompt yourself. Confirm a fingerprint appears; compare it locally rather
   than adding a real identifier to source, screenshots or fixtures.
3. Lock storage, quit the actual app window and launch it again. It must start
   locked. Unlock again and confirm the same identity returns.
4. Confirm appearance remains as selected and check the local privacy/About text.
5. On Ubuntu, check that the app data/cache roots and WebKitCache are private and
   owned by the desktop user. Existing unsafe directories are rejected; the app
   does not silently change their permissions.

Send pass/fail observations and the build ID. If something fails, leave the state
and credential intact for diagnosis. Do not delete a vault or credential to make
a restart check pass.

## Pairing source and planned 0.0.3 handoff

The source has nine typed methods: GetStatus, UnlockStorage, LockStorage,
GetPairingStatus, ApprovePairing, DenyPairing, CreateInvitation, JoinInvitation and
CancelInvitation. Ordinary status never contains a bearer. Create returns a
private share link only after the explicit user action; paste/Join does not
navigate a browser. No OS URI handler or QR flow is registered yet.

Use the following only with the new candidate after its migration/recovery
preparation. This is a proposed Windows/Ubuntu owner check, not a recorded pass:

1. Unlock each app and open Start a conversation. Confirm the connection-service
   HTTPS URL is empty and relay consent unchecked. Opening the dialog must not
   start public networking. The operator learns IP/availability and can relay
   encrypted pairing setup; strict direct-only is unavailable.
2. Enter a trusted operator's relay-map URL explicitly on both devices and give
   separate consent. Upstream's documented `https://tailcat.dev/derpmap.json` is
   a manual example only, not an app default or a service-availability guarantee.
   No public relay has been contacted by the disposable network integration tests.
3. Choose Create on one device, then explicitly Copy and privately share the
   one-use link. Choose Paste on the other device and Open. Do not put a live
   invitation in logs, screenshots, fixtures, Git or a public/shared artifact.
   A copied invitation remains in the clipboard until replaced.
4. Confirm the identity popup appears only after backend reservation on both
   devices. Compare full fingerprints through a trusted channel. Deny has initial
   focus; Escape denies. Test denial before trying a fresh invitation for approval.
5. Approve both sides. Waiting or saving is not completion. On success the UI says
   Contact approved and messaging not ready. It must not enable the composer,
   open an ongoing conversation tunnel or claim strict direct-only delivery.
6. Test cancel, expiry, storage lock and quit during setup. Share/paste text must
   clear and stale responses must not restore it. Reservation hides the share
   dialog without canceling the request being confirmed.

After restart, pending setup remains incomplete; committed setup is inert and
requires reconnection. Both have an explicit Discard action and neither revives
an invitation, consent proof or live connection. Fresh ongoing-session
reconfirmation is not implemented. Discard removes the selected inert setup;
runtime cancellation must revoke authority before durable deletion.

The 32 browser tests include real generated-binding calls with synthetic Wails
HTTP responses. Backend tests separately compose actual Tailcat/WireGuard over a
disposable loopback TLS DERP fixture with real pairing TLS, reservation, two
decisions and encrypted schema-4 completion. These do not prove two native
devices/VMs, forced-relay payload paths or direct-only packet exclusion. The native
owner results above remain limited to the retained 0.0.2 shell/storage binaries.

## State migration before a newer binary

The new source migrates canonical schema 1/2/3 application state to schema 4,
preserving identity, the wrapping credential, pending records and existing
contact/chat sections while adding committed contacts.
The earlier installed 0.0.2 executables understand schema 2 and cannot read schema
4. Selecting an older executable alone is therefore not a rollback after migration.

Use a disposable OS account/profile for initial migration QA. Before a later
binary opens existing state, establish a reviewed recovery plan for the compatible
encrypted snapshot and its matching OS credential. Preserve the credential; do
not delete or replace it to make an old binary start. There is no automatic schema
downgrade or implemented backup/restore UI. Private recovery material stays out of
Git and shared build artifacts.

## Troubleshooting evidence

Local diagnostic files are `diagnostics.jsonl` and
`diagnostics.previous.jsonl`, at most 256 KiB each. Windows uses
`%LOCALAPPDATA%\WhatsUp`; Linux uses `$XDG_DATA_HOME/whatsup` or
`~/.local/share/whatsup`. They contain only six fixed fields and finite operational
events; see [privacy controls](privacy.md).

After the owner tests, the agent can inspect the existing VM over authorized SSH,
summarize these local events and verify file ownership/modes. SSH is test
infrastructure, not a service in WhatsUp. No diagnostic upload service is enabled,
and there is no need to send the identity vault, WebView storage or OS credential.
