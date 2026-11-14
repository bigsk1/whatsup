# Security information

WhatsUp is preview software and has not undergone an independent security audit.
It is not yet a supported, signed public release. Do not rely on the preview for
sensitive communication.

## Identity and pairing

Each device has its own Ed25519 identity. Invitations expire after ten minutes,
accept one candidate and require approval on both devices. Compare the reciprocal
fingerprints through a trusted channel before approving. A name or invitation
alone does not verify the person behind a device.

A pending pairing cannot read an existing conversation. After approval, the
invitation connection and its credentials are retired. The saved contact uses
separate credentials, including a random per-contact WireGuard pre-shared key.

Each ongoing connection uses fresh TLS 1.3 mutual authentication against the
saved device identities and contact association. Ordinary reconnection does not
ask for another manual approval. A changed identity stops reconnection; WhatsUp
does not silently replace the saved key.

Display names and private contact names are labels associated with the approved
identity. They do not grant trust or change a key pin. Shared names arrive only
through the authenticated connection after pairing approval. Private contact
names are never transmitted.

## Messages and local storage

Messages are saved to an encrypted local queue before sending. A message is
marked delivered only after the authenticated receiving app has saved it and
returned an acknowledgement. Stable message identifiers prevent duplicate
display after retries.

Local identity, contact credentials, names, history and queued messages use
authenticated AES-256-GCM encryption. The random local encryption key is
protected by Windows Credential Manager or Linux Secret Service. Missing
credentials, corrupt state and uncertain saves fail closed; there is no plaintext
fallback or automatic replacement key for an existing vault.

**Disconnect and lock** stops connections before closing storage. It preserves
encrypted saved state and clears displayed messages, names and drafts.
It does not require a separate application password to unlock again. It is not
an access-control barrier against someone using the same unlocked OS session.
**Forget contact and delete conversation** removes the selected contact's local
credentials, history, outbox and names after stopping its work. It preserves the
device identity and local profile.

Forget is logical local deletion. It does not delete another person's copies or
guarantee erasure from storage media, backups or operating-system memory.

## Network and runtime boundaries

Automatic mode prefers a direct connection and permits explicitly accepted
encrypted relay fallback. Strict direct-only mode is unavailable. A relay can
observe IP addresses and availability and can carry encrypted packets. WhatsUp
does not promise anonymity.

The selected connection service is saved with the contact. A peer cannot replace
it during reconnection. The service operator may update its own relay map; this
is not permanent certificate or public-key pinning of that operator.

The app exposes no shell, SSH service, port forwarding, LAN proxy or public
content server. Its desktop renderer is not an operating-system sandbox for the
Go application. A compromised local account, operating system or approved peer
can access information available to it.

WhatsUp has no application analytics, automatic diagnostic upload or remote crash
reporting. Windows WebView2 has its own documented vendor diagnostics behavior.
See the [privacy guide](docs/privacy.md).

## Current limitations

Files, images, videos, profile pictures, backup/restore and automatic updates are
not available. Signed installers and release verification are incomplete. A
version number or successful connection does not certify security.

Use compatible versions on both devices. Newer versions may upgrade local data,
and an older executable cannot necessarily read that data. Replacing the
executable alone is not a storage downgrade.

See [third-party notices](THIRD_PARTY_NOTICES.md) for included software notices.

## Reporting a security issue

There is no published private security-reporting address yet. A private reporting
route and supported-version policy must be documented before public distribution.

Do not include live invitations, private keys, fingerprints, message history,
vault files, credentials or personal endpoints in a public report. A useful
report can describe the app version, platform, affected action and a redacted
reproduction without exposing private data.
