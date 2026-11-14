# Security information

WhatsUp 0.0.13 is unsigned preview software, not a supported public release.
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
Retention keeps the newest three authenticated top-level migration root backups
under the current master, plus a required live migration reference, in either
protection mode. Unreadable or different-key copies do not displace those backups.
Full recovery rollback trees, including their migration roots, and unknown
recovery material are preserved for safety and can consume extra space.
Damaged managed backups produce a specific local-file repair error; they are not
silently skipped when passphrase protection requires reconciliation. Pruning is
best-effort after a verified settings change and cannot turn success into a
misleading passphrase-change failure.
An inaccessible historical protected copy under the current OS credential may
also require explicit repair or secure preservation outside the app directory
when its relationship to the current master cannot be authenticated.
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

Signed installers, signed update verification, SBOM/distribution checks and a
private reporting route are incomplete. There is no automatic updater. Version
numbers, passing tests and successful connections do not certify security.
Older executables cannot necessarily read upgraded storage.

## Reporting a security issue

No private security-reporting address is published yet. A reporting route and
supported-version policy are required before public distribution. Do not post
live invitations, private keys, fingerprints, history, vaults, credentials or
personal endpoints publicly. Describe the version, platform, affected action and
a redacted reproduction. See [third-party notices](THIRD_PARTY_NOTICES.md).
