# Privacy and local diagnostics

WhatsUp has no application analytics or remote crash reporting. Troubleshooting
diagnostics stay on the device with no automatic upload.

## Local data and protection

Device identity, contact credentials, display/private names, history, queued
messages and attachment data are encrypted locally. Windows Credential Manager
or Linux Secret Service protects the local wrapping credential. An optional app
passphrase adds a required factor; both modes still require the OS provider.
OS-only launch automatically opens storage and reconnects saved contacts.
Passphrase-protected launch prompts for the passphrase.

**Go offline** stops connections, closes storage and clears decrypted UI state.
Saved data remains encrypted. Lock the OS session when leaving the computer.
The app runs with your account's privileges; it is not an OS sandbox.

**Share my display name** starts off. Shared names travel only to approved peers
inside authenticated connections, after pairing. They do not enter invitations
or diagnostics. A private contact nickname stays local. Disabling sharing clears
the peer's shared label when connected, but cannot erase independent copies.

Invitations are temporary bearer links held in memory. Copying one places it on
the OS clipboard; it can remain there or in clipboard history until removed.
Cancellation, expiry and restart invalidate its authority. Links are not
published through DNS or a public contact directory, and pasting one does not
navigate a browser.

## Connections and relays

Creating an invitation requires a chosen HTTPS relay-map service and separate
unchecked consent. The public Tailcat preset fills an empty field without a fetch
or consent. Joiners review and consent to the service carried in the invitation
before any map fetch. Opening an invitation form alone starts no networking.
Launching in OS-only mode, going online or completing pairing reconnects contacts.

Direct connectivity is preferred, with consented encrypted relay fallback. The
service can observe IP addresses and availability. Setup/reconnection and network
rediscovery depend on that service even if payload traffic later becomes direct.
No anonymity or offline mailbox is provided. Strict direct-only is unavailable.
A sampled Direct/Relayed/Unknown display is not a packet-level policy guarantee.

Saved relay services cannot be changed silently by a peer. A service migration
requires both users' authenticated approval. The operator can update its server
list under the same service URL.

## Files, exports and deletion

Received files require Accept/Decline. Per-contact automatic acceptance of images
up to 25 MB is an explicit receiver opt-in. Incomplete data and completed objects
remain encrypted inside WhatsUp. Save writes a plaintext copy to a destination
you choose through the Go dialog; Show in folder reveals it without opening it.
Those copies are outside WhatsUp's encryption and deletion controls.

Image sends apply EXIF orientation, re-encode and strip metadata by default. JPEG
photos remain JPEG. Full-size previews are sanitized on demand and use a bounded
session cache cleared on retirement. Send as file retains original bytes and
metadata as a save-only object, without image preview or automatic image acceptance. Copy/paste uses the OS clipboard and its retention policy.
Recovery exports contain identity, contacts, history and attachments, encrypted
under a separate recovery passphrase. Keep exports and secrets private.

Clear history keeps the contact and cancels unfinished attachments. Queued text
continues until acknowledged. Forget deletes one contact's local state while
preserving your identity and other contacts. An online removal notice stops the
peer's retries; it does not delete their history. These are logical deletion,
not physical erasure of media, memory, external exports or OS notification history.
Managed migration backups are re-protected on passphrase changes/authenticated
reopen and have bounded root-snapshot retention. Complete recovery rollback data
is retained for safety; external old backups are not retroactively protected.

## Desktop behavior and OS artifacts

Close-to-tray and generic new-message notifications start on. Start at login,
notification contact names and message previews are separate opt-ins. Choose
Quit from the tray to stop the app. Behavior preferences are encrypted in
app-preferences.vault; appearance alone uses the private WebView's storage.

Windows notification support registers per-user app identity and activation data
under `HKCU\Software\Classes\AppUserModelId\WhatsUp` and an app-specific
`HKCU\Software\Classes\CLSID\{GUID}\LocalServer32`. These identify the app and
its executable; they are not analytics. The native runtime writes an app icon
under `%TEMP%` using the app name and GUID. Registration/icon artifacts can outlive
a run. Enabling start at login adds the WhatsUp value under
`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`; disabling it removes
that value. No system-wide startup entry is created.

Linux start at login uses `$XDG_CONFIG_HOME/autostart/whatsup.desktop`, normally
`~/.config/autostart/whatsup.desktop`. Disabling it removes the app-managed entry.
Installation may also create a menu/desktop launcher and optional dock entry.
Notifications use the desktop notification service over the user's session bus.

Notifications say only **New message** by default. Enabling names/previews sends
that content to the OS notification system, including its notification history.
It can appear on the desktop or lock screen according to OS settings. Notification activation data contains only opaque routing identifiers, without
duplicating names or message previews. Clicking a notification can select an
existing saved conversation after unlock; it cannot unlock the app or add a
contact. Turning previews off affects future notifications; it
does not erase existing Action Center entries, OS history or prior copies.

## Local diagnostics and locations

Logs contain fixed event codes, UTC time, app/Go versions, OS and architecture.
They exclude names, text, keys, invitations, fingerprints, peer addresses, user
paths and raw errors/bridge payloads. Network diagnostics use fixed rate-limited
codes. Logs are plaintext operational metadata readable by the local account.
The active and previous logs are limited to 256 KiB each, 512 KiB total.

| Platform | Application data |
| --- | --- |
| Windows | `%LOCALAPPDATA%\WhatsUp` |
| Linux | `$XDG_DATA_HOME/whatsup`, normally `~/.local/share/whatsup` |

The profile includes state.vault, messages.sqlite and sidecars containing encrypted records,
encrypted attachment objects, app-preferences.vault, private WebView data and
recognized migration/recovery copies. Logs are diagnostics.jsonl and
diagnostics.previous.jsonl. Linux cache normally uses `~/.cache/whatsup`.
No diagnostic export or upload runs automatically.

## WebView storage and vendor diagnostics

Windows uses system WebView2 with a private profile at
`%LOCALAPPDATA%\WhatsUp\webview`, separate from Edge cookies/storage. The app does
not change Windows/Edge privacy settings. Optional diagnostic data follows
Windows privacy settings. Microsoft also documents required diagnostics without
an overall application-controlled off switch. See [Microsoft's WebView2 privacy
documentation](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/data-privacy).

Linux uses system WebKitGTK with private data/cache paths checked for location,
ownership and permissions. Unsafe existing paths fail closed. Private profiles
separate storage; they do not guarantee no OS/runtime/network diagnostic traffic.
See [security information](../SECURITY.md) for the security boundary.
