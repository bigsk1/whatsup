# Security information

WhatsUp 0.2.1 is distributed unsigned. It has not undergone an independent
security audit or certification; weigh that before relying on it for sensitive
communication.

## Identity, pairing and connections

Each device has an Ed25519 identity. A conversation has exactly two approved
identities; up to 32 separate contacts can be saved. Invitations expire after
ten minutes, admit one candidate atomically and require both approvals. Compare
the full reciprocal fingerprints through a trusted channel. Names and avatars
do not verify identity. Pending pairing has no access to existing history.
Invitation credentials are retired after setup. Saved contacts have distinct
transport credentials and random WireGuard pre-shared keys, always enabled.

Connections use fresh mutually authenticated TLS 1.3 against the saved identity
and exact contact/policy association, inside the encrypted Tailcat transport.
Changed identities stop reconnection. Reconnect requires no repeated manual
fingerprint approval. Current and previous messaging protocols exchange text;
older incompatible authenticated peers get an update notice. This is not an
unlimited compatibility promise.

Automatic mode prefers direct connectivity and permits explicitly accepted
encrypted relay fallback. A relay sees IP addresses and availability and may
carry encrypted traffic. Setup and rediscovery require the selected service.
To try a direct path, each device sends the other its public IP address and
port and its local network interface addresses. A saved contact receives them
on every connection. During setup both devices exchange them before fingerprints
are approved, so anyone redeeming a live invitation learns them. A malicious
relay operator can also obtain the local addresses.
The path display is sampled and is not packet-boundary enforcement. Large-file
relay-bandwidth consent is a precaution, not strict direct-only protection.
Strict direct-only is unavailable. WhatsUp does not promise anonymity.

The approved service is saved with each contact. Relay migration requires
explicit authenticated consent on both sides, retaining identity pins. An
operator may update its map; this is not permanent operator certificate pinning.

You can operate the service yourself with the
[DERP relay Docker template](https://github.com/bigsk1/derp-relay).

## Voice and video calls

Voice calls are off until enabled and exist only between two approved, connected
contacts whose builds both support them. Video is a second switch, also off
until enabled, and requires voice calls. A device with video switched off rings
for a video offer as a voice call, and the person answering always chooses
whether to answer with video. Ring, accept, decline, hang-up, mute and
camera-state signals travel as frames on the same mutually authenticated TLS 1.3
session as messages. A device rings only for the pinned identity, takes one call
at a time, and limits how fast call frames may arrive.

Call audio and video are sealed with SRTP using AES-256-GCM, with replay
protection on.
Each call's keys come from the session's TLS 1.3 exporter, bound to a random
call identifier chosen by the caller and a random value chosen by the answering
device. Both devices contribute fresh randomness to every call, each direction
has its own key, and a session refuses to derive keys for the same call twice.
The sealed packets travel on a UDP flow inside the same WireGuard tunnel as
everything else. That flow is opened only for an answered call with the approved
peer and closed when the call ends. WhatsUp asks the audio encoder for a
constant bitrate with silence suppression off, which keeps packet sizes from
following speech where the system's encoder supports it. Where it does not, the
encoder falls back to a variable bitrate. Either way the content is encrypted,
but packet sizes and timing are visible on the network and can still reveal
patterns such as when someone is speaking.

Video is VP8 at 640 by 360, up to 20 pictures a second, sealed in the same SRTP
context as the audio and sent on the same flow. A camera starts off on both
sides of a video call and is on only after the person at that device chooses
it. Video frames sent before the flow is open, or while the camera is recorded
as off, are dropped. While the connection's sampled path is **Relayed**, the
device does not start sending video until the person chooses to, once per
call. If a call that began direct falls back to a relay, the device stops its
video when the reading changes and offers the choice again. Received video is
shown either way.

The webview's microphone permission is denied except between an answer and the
end of that call. Its camera permission is denied except while an answered
video call's camera is switched on at this device. A ringing call cannot open
the microphone or the camera, or attach a media stream. On Windows each request is answered
at the moment it is made, the answer is not saved in the WebView2 profile, and
answers saved by earlier preview builds are erased at start. The page is not
loaded until the runtime confirms that erasure; if it cannot, or the runtime is
too old to leave an answer unsaved (before version 111), voice calls are
unavailable for that run and the page is served with capture refused. On
Linux, WebKitGTK requests are answered from the same table, and the desktop's
own camera permission applies on top of it. Hanging up, switching calls off and
going offline each end the call and close capture on this device first, without
waiting for the other device or the network; turning the camera off stops the
camera before the other device is told. Mute works the same way: from the
moment Mute is pressed this device hands no new audio to the network, before
the other device is told, and it stays muted even if that message is delayed
or fails. A packet already on its way at that instant can still arrive.
Unmuting takes effect only once this device has recorded it for that call.
Received audio and video that arrive faster than the device can decode them
are dropped instead of being queued. Calls are not recorded, and audio and
video are never written to disk. The call details readout shows counters only
and stays on the device.

Limits to keep in mind:

- Capture, Opus and VP8 encoding and decoding, and playback run in the system
  webview (WebView2 or WebKitGTK). Audio and video packets from an approved
  contact are parsed by that engine's decoders, as their video files already
  are.
- A relay, or anyone watching the network, cannot read a call but can see that
  packets are flowing, their sizes and timing, and for how long. That is enough
  to infer that a call is taking place, its duration and possibly its rhythm.
- The **Direct** / **Relayed** label on a call is the same sampled reading as
  elsewhere, not packet-boundary enforcement. Holding video back on a relayed
  connection relies on that reading. It is checked when the camera starts and
  each time the reading changes, not for every packet, so video sent between a
  path change and the next reading can travel through the relay. It is
  encrypted like everything else there.
- Video has one fixed quality and does not adapt to a slow connection.
- A hidden or minimized window does not stop the camera. The system may slow
  the page while its window is out of sight, which slows or stalls the picture
  being sent, but that is a side effect and not a privacy control. The camera
  runs until it is switched off or the call ends.
- On Linux the camera is reached through the desktop's camera portal, which
  identifies WhatsUp by its application ID and may ask once for permission.
  Sending live video needs WebKitGTK 2.54.3 or newer where GStreamer is 1.28 or
  newer: earlier versions repeat the camera's first picture for a time that
  grows with how long the computer has been on. WhatsUp detects a camera whose
  picture is not updating, switches that video off and says so, rather than
  sending a frozen picture as if it were live.
- WebKitGTK has no echo cancellation, noise suppression or automatic gain.
  Headphones avoid echo; microphone level is set in the system's sound settings.
- Bringing the window forward for an incoming call depends on the desktop. Some
  only flash or notify.
- On Linux WhatsUp sets WebKitGTK's autoplay policy so that its own page may
  start sound without a click first. That is what lets an incoming call ring
  after WhatsUp started at login. The page is the bundled interface and loads
  no remote content; the microphone and camera rules above are unaffected.

## Local encryption and access

A random master key is wrapped using the OS-protected credential. An optional
app passphrase adds a required wrapping layer, using Argon2id with RFC 9106's
second recommended parameter set: t=3, 64 MiB, p=4. Purpose keys use HKDF-SHA256.
There is no passphrase-only fallback for a missing OS credential provider.

The small authenticated encrypted envelope holds identity, contact credentials
and key slots. SQLite history and outbox records use XChaCha20-Poly1305 with
random 192-bit nonces and record/context binding. Readable database metadata is
limited to random IDs, per-contact sequence numbers, ciphertext lengths and
row counts. Retry scheduling stays in memory. Attachment objects and recovery
archives use Tink streaming AEAD. Temporary attachment files are encrypted too.

Messages are queued durably before sending. Receive and duplicate-check state
commit before ACK; delivery and outbox removal commit together. Stable IDs
prevent duplicate delivery after retries. Message and attachment quotas are
logical limits; they do not guarantee enough free disk space or bound all OS
storage overhead. Missing credentials, corrupt state and uncertain writes fail
closed without plaintext fallback or replacement keys for existing data.

In OS-only mode launch auto-unlocks and reconnects contacts. Anyone using the
unlocked OS account can access that credential. Requiring an app passphrase adds
an application access barrier. **Go offline** closes connections and local
storage; it does not erase keys from OS memory or substitute for locking the OS.
A wrong current passphrase is a validation error and does not disconnect a
healthy app. Storage corruption and uncertain protection writes still close it.

Passphrase changes cover recognized migration and recovery root copies and owned
crash temporaries. Authenticated reopen repairs copies missed by older builds.
An encrypted, master-authenticated catalog records creation sequences and exact
ciphertext digests for newly published backups. Retention keeps the newest three
eligible top-level roots plus a required live migration reference in either mode;
filesystem timestamps do not authorize deletion. Unlisted historical copies,
interrupted publication and unverifiable catalogs are preserved without pruning.
Full recovery rollback trees, including their migration roots, and unknown
recovery material are preserved for safety and can consume extra space.
Damaged managed backups produce a specific local-file repair error; they are not
silently skipped when passphrase protection requires reconciliation. Pruning is
best-effort after a verified settings change and cannot turn success into a
misleading passphrase-change failure.
Confirmed recovery securely preserves unreadable retiring roots behind an
additional envelope derived from the restored master. It retains the original
bytes without depending on the old app passphrase or exempting raw key slots
from protection. Eligible damaged-copy errors offer an explicit **Set aside and
retry** action, requiring current OS/app factors and the exact backend-captured
file digest. The renderer supplies no path. Corrupt preservation envelopes still
fail authentication. These envelopes cannot act as a live application root.
Unsafe directories, ownership, permissions and unavailable files remain visible
as attention conditions without granting automatic repair authority.
An app passphrase cannot revoke an earlier external OS-only backup or data
already obtained by someone else. Master keys are not rotated on mode changes.

Recovery export/import uses a separate recovery passphrase and authenticated
archive. Import may replace local data and restore the same identity/contact
credentials. Lost OS credentials or secrets can make data unrecoverable without
a usable export. No backup forward-secrecy guarantee is made.

## Files and deletion

Transfers require receiver approval. The one exception is automatic image
acceptance, which starts off and is enabled per contact: PNG, JPEG, WebP and GIF
offers up to 25 MB from that contact are then accepted without a prompt. All
transfers use bounded offers/chunks, encrypted temporary storage and whole-file
SHA-256 verification before completion. A peer cannot
choose a local save path. The Go save dialog chooses the export destination.
Files are never auto-opened. Preview handlers serve opaque IDs with forced MIME,
nosniff and restrictive CSP; HTML, SVG and active Markdown are not previewed.
Images are decoded with dimension/pixel limits, oriented and re-encoded for
full-size views and thumbnails. MP4 and WebM videos are content-checked and
served as the sender's original bytes, so the WebView's media decoder parses
them; they load only when you choose to play them. Contact photos are validated
in Go (exactly 512×512 pixels, at most 64 KiB) and shown as the sender's bytes
without re-encoding. Normal image sends strip metadata; explicit
send-as-file preserves original bytes as save-only data, skipping image decoding,
preview and image auto-accept eligibility. Session caches are bounded and retired
with their contact/storage authority.

Clear history cancels unfinished attachments while keeping the contact; queued
text retains its delivery obligation. Forget removes one contact's local state
and sends an authenticated removal notice when connected. Neither action erases
a peer's copies, earlier exports, notification history, physical media or memory.
Single-attachment deletion removes the stored file data, revokes its local media
and saved-file handles and retires queued file writes. Clear history does the
same for every attachment. The encrypted offer record, including the file name,
type, size and hash, remains until Forget for replay rejection, cancellation
retries and completion receipts. Deletion never removes a previously exported
file. Clear history, Forget and attachment deletion change only the live
profile: earlier migration snapshots and recovery rollback trees can still hold
encrypted copies of removed data. Retention can prune older migration snapshots;
recovery rollback trees are never removed automatically. Per-contact storage
meters read authenticated logical
quota counters; they do not report physical allocation or OS free space.
Attachment timeline times are generated locally and stored inside encrypted
records, not supplied by a peer or added to readable SQLite columns. Native
drag highlighting sends only a boolean and revision; staging and file paths
remain on the Go side and an actual drop does not send a file automatically.

## Desktop and disclosure boundaries

The Go application runs with the desktop account's privileges. A private WebView
is not an OS sandbox for that process. WhatsUp runs no shell/SSH service, LAN
proxy, port forwarding or public content server, and its own bridge methods
offer no command execution or generic file access. The Wails runtime's built-in
calls also remain available to the app page, including opening links with the
OS default handler, clipboard access, native file dialogs and window control.
The page loads only bundled scripts under a restrictive content policy and never
renders peer text as HTML; script injected into the page could still use those
calls. A compromised OS/account or approved peer can access its own data.

No application analytics, automatic diagnostic upload or remote crash reporting
is enabled. Local diagnostics are bounded and redact content and endpoints.
Notification names/previews are opt-in and may persist in OS notification history.
WebView2 has vendor diagnostics outside this policy. See [privacy](docs/privacy.md)
for data paths, registry/autostart artifacts and notification behavior.

Release packages include checksums and an SBOM. Pushing a version tag runs GitHub
release CI, which repeats the checks, generates build-provenance attestations and
publishes the release. It never replaces an existing release.
An attestation identifies the source/workflow; it is not Authenticode signing,
a security audit or a guarantee that a package is safe. Windows packages are
not Authenticode-signed, and in-app updates stay disabled.
In-app updates remain disabled. Version numbers, passing tests and successful
connections do not certify security.
Older executables cannot necessarily read upgraded storage.

## Dependency advisories

The component checks and release packaging run known-vulnerability scans over
the Go modules and both npm dependency trees. No advisory is excepted: any
finding fails those checks, and a failed or unreachable scan counts as a
failure, not a clean result. An ordinary development build does not run them.

Earlier releases were built under a temporary exception for one advisory in
the frontend build and test tooling:

- 0.1.0 and 0.1.1: deepmerge,
  [GHSA-gcqx-w48m-w2h4](https://github.com/advisories/GHSA-gcqx-w48m-w2h4).
- 0.1.2 and 0.2.0: picomatch,
  [GHSA-fc5q-39xc-vjpf](https://github.com/advisories/GHSA-fc5q-39xc-vjpf).
  GitHub withdrew this advisory on 2026-11-18 and npm no longer reports it.

In both cases the affected tools handled project files during the build, were
not part of the shipped app and did not receive peer input. From 0.2.1 on, no
exception applies.

Build and test tooling stays out of the application: the build fails if any of
it is bundled into the app.

## Reporting a security issue

Use this repository's **Security → Advisories → Report a vulnerability** to
send a private report through GitHub. See GitHub's [private reporting instructions](https://docs.github.com/en/code-security/security-advisories/working-with-repository-security-advisories/privately-reporting-a-security-vulnerability).
The repository administrator must enable private vulnerability reporting before
sharing downloads. If the private button is unavailable, do not post details in
a public issue; wait for the private route to be enabled.

Only the current release series receives fixes; there is no support or response
time guarantee. Describe the version, platform, affected action and a synthetic
or redacted reproduction. Never attach live invitations, keys, fingerprints,
message history, vaults, recovery exports, contact photos, credentials or personal
endpoints to a public report. See [third-party notices](THIRD_PARTY_NOTICES.md).
