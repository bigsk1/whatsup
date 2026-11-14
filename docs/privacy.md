# Privacy and local diagnostics

WhatsUp has no application analytics or remote crash reporting. Troubleshooting
diagnostics stay on the device and are never uploaded automatically.

## Names, messages and invitations

Device identity, contact credentials, display names, private contact names,
message history and queued messages are stored encrypted locally. Windows
Credential Manager or Linux Secret Service protects the local encryption key.

**Share my display name** starts off. Enabling it and saving sends the display
name only to the approved contact over the authenticated connection, after
identity approval. It is not added to invitation links or diagnostic logs.
A private contact name never leaves your device.

Turning sharing off clears the received shared label when the peer is connected.
It cannot erase information already copied elsewhere or remove the peer's own
private nickname.

An invitation is a private, temporary link. WhatsUp keeps it in session memory
and copies it to the clipboard only when you choose **Copy invitation**. The link
is cleared from the interface on handoff, cancellation, expiry or lock. A copied
link remains in the operating-system clipboard until it is replaced.
Cancellation and expiry invalidate its pairing authority.

Invitations are not published through DNS or a public contact directory. Pasting
one into WhatsUp does not navigate a browser.

## Connections and relays

The connection-service field starts empty, and relay consent starts unchecked.
You explicitly select a trusted HTTPS relay-map service before creating or
joining an invitation.

Opening WhatsUp or viewing Settings does not start a new contact connection.
**Unlock and connect**, an explicit **Connect** action, or successful pairing
completion starts the saved connection and its automatic retry loop.

Automatic mode prefers direct connectivity and allows consented encrypted relay
fallback. The service can observe connection metadata, including IP addresses
and availability. Encryption does not hide that metadata or guarantee anonymity.
Strict direct-only mode is not available.

The selected service is saved encrypted with the contact. A peer cannot substitute
another provider during reconnection, although the selected operator may update
its own server list.

## Local diagnostics

Diagnostic files contain fixed operational event codes, UTC time, app and Go
versions, operating system and architecture. They exclude names, message text,
keys, invitations, fingerprints, peer addresses, user paths and raw error or
bridge payloads.

The active and previous logs are limited to 256 KiB each, for 512 KiB total
retained diagnostic data. These logs are plaintext operational metadata,
separate from the encrypted conversation. Anyone with access to the local
account may be able to read them.

The files are named diagnostics.jsonl and diagnostics.previous.jsonl:

| Platform | Application data |
| --- | --- |
| Windows | %LOCALAPPDATA%\\WhatsUp |
| Linux | $XDG_DATA_HOME/whatsup, or ~/.local/share/whatsup |

There is no automatic diagnostic export or upload. Share troubleshooting
information only through a channel you choose, after checking it for private
content.

## WebView storage and vendor diagnostics

On Windows, WhatsUp uses the system WebView2 runtime with a private profile under
%LOCALAPPDATA%\\WhatsUp\\webview. It does not share Edge's cookies or storage.
The app does not change system-wide Windows or Edge privacy settings.

Optional WebView2 diagnostic data follows Windows privacy settings. Microsoft
also documents required diagnostics that applications cannot disable through
an overall collection switch. See [Microsoft's WebView2 privacy documentation](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/data-privacy).
WhatsUp's local diagnostics policy does not control those runtime diagnostics.

Linux uses the system WebKitGTK runtime with private application data and cache
directories, normally ~/.local/share/whatsup and ~/.cache/whatsup. Startup checks
their location, ownership and permissions. Unsafe existing directories are
rejected rather than silently accepted.

A private WebView profile separates storage. It does not sandbox the local
operating-system account or guarantee that the operating system and network
infrastructure send no data.

## Locking and deletion

**Disconnect and lock** stops networking, closes local storage and clears
displayed messages, names, drafts and the contact search. Saved data remains
encrypted for the next unlock.

**Forget contact and delete conversation** removes that contact's credentials,
history, queue and contact names from this device. Your own profile, identity,
fingerprint and appearance preferences remain. It does not delete remote copies
or guarantee physical erasure from media, backups or memory.

Appearance is the only preference persisted in the WebView. Profile pictures,
remote avatars and attachment sharing are not available.

See the [security information](../SECURITY.md) for the current security boundary.
