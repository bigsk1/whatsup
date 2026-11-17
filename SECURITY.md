# Security information

WhatsUp 0.1.1 is unsigned preview software, not a stable release.
It has not undergone an independent security audit or certification. Do not
rely on the preview for sensitive communication.

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
The path display is sampled and is not packet-boundary enforcement. Large-file
relay-bandwidth consent is a precaution, not strict direct-only protection.
Strict direct-only is unavailable. WhatsUp does not promise anonymity.

The approved service is saved with each contact. Relay migration requires
explicit authenticated consent on both sides, retaining identity pins. An
operator may update its map; this is not permanent operator certificate pinning.

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

Transfers require receiver approval, bounded offers/chunks, encrypted temporary
storage and whole-file SHA-256 verification before completion. A peer cannot
choose a local save path. The Go save dialog chooses the export destination.
Files are never auto-opened. Preview handlers serve opaque IDs with forced MIME,
nosniff and restrictive CSP; HTML, SVG and active Markdown are not previewed.
Images are decoded with dimension/pixel limits, oriented and re-encoded for
full-size views and thumbnails. Normal image sends strip metadata; explicit
send-as-file preserves original bytes as save-only data, skipping image decoding,
preview and image auto-accept eligibility. Session caches are bounded and retired
with their contact/storage authority.

Clear history cancels unfinished attachments while keeping the contact; queued
text retains its delivery obligation. Forget removes one contact's local state
and sends an authenticated removal notice when connected. Neither action erases
a peer's copies, earlier exports, notification history, physical media or memory.
Single-attachment deletion revokes its local media and saved-file handles and
retires queued file writes. Encrypted terminal IDs/hashes remain for replay
rejection, cancellation retries and completion receipts. It never deletes a
previously exported file. Per-contact storage meters read authenticated logical
quota counters; they do not report physical allocation or OS free space.
Attachment timeline times are generated locally and stored inside encrypted
records, not supplied by a peer or added to readable SQLite columns. Native
drag highlighting sends only a boolean and revision; staging and file paths
remain on the Go side and an actual drop does not send a file automatically.

## Desktop and disclosure boundaries

The Go application runs with the desktop account's privileges. A private WebView
is not an OS sandbox for that process. No shell/SSH service, command execution,
generic filesystem bridge, LAN proxy, port forwarding or public content server
is exposed. A compromised OS/account or approved peer can access its own data.

No application analytics, automatic diagnostic upload or remote crash reporting
is enabled. Local diagnostics are bounded and redact content and endpoints.
Notification names/previews are opt-in and may persist in OS notification history.
WebView2 has vendor diagnostics outside this policy. See [privacy](docs/privacy.md)
for data paths, registry/autostart artifacts and notification behavior.

Preview packages include checksums and an SBOM. Pushing a version tag runs GitHub
release CI, which repeats the checks, generates build-provenance attestations and
publishes the release. It never replaces an existing release.
An attestation identifies the source/workflow; it is not Authenticode signing,
a security audit or a guarantee that a package is safe. Windows previews remain
unsigned. Authenticode is the next Windows release gate; SignPath Foundation is
a candidate after this repository becomes public, subject to its acceptance.
In-app updates remain disabled. Version numbers, passing tests and successful
connections do not certify security.
Older executables cannot necessarily read upgraded storage.

## Build dependency exception

The LOW [deepmerge advisory GHSA-gcqx-w48m-w2h4](https://github.com/advisories/GHSA-gcqx-w48m-w2h4)
has no patched upstream release at the time of review. An explicit exception
covers only deepmerge 4.3.1 through @sveltejs/vite-plugin-svelte 7.3.1 in the
frontend development/build graph. It expires on **2026-12-15** and must be removed
when a compatible patch is available. The plugin processes trusted local build
configuration; deepmerge is absent from the shipped frontend bundle and receives
no peer input. Do not build untrusted configuration under this exception.

Audit results remain visible. Expiration, a changed dependency chain, production
use, another advisory or a higher severity fails the gate. The exact policy is
in config/advisory-exception.json. This exception does not suppress other scans.

## Reporting a security issue

Use this repository's **Security → Advisories → Report a vulnerability** to
send a private report through GitHub. See GitHub's [private reporting instructions](https://docs.github.com/en/code-security/security-advisories/working-with-repository-security-advisories/privately-reporting-a-security-vulnerability).
The repository administrator must enable private vulnerability reporting before
sharing downloads. If the private button is unavailable, do not post details in
a public issue; wait for the private route to be enabled.

Only the current preview series receives fixes; there is no support or response
time guarantee. Describe the version, platform, affected action and a synthetic
or redacted reproduction. Never attach live invitations, keys, fingerprints,
message history, vaults, recovery exports, contact photos, credentials or personal
endpoints to a public report. See [third-party notices](THIRD_PARTY_NOTICES.md).
