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

Profile pictures follow separate controls. **Share my profile photo** starts
off; your own picture still shows locally. **Show contact photos** controls local
display and starts on. Your picture is decoded, cropped, scaled and re-encoded
in Go with bounded size and no source metadata, then stored encrypted with
profile data. Only authenticated, approved peers with compatible photo support
receive it. A contact's photo is validated in Go (exactly 512×512 pixels, at
most 64 KiB), stored encrypted and displayed as the bytes they sent, without
re-encoding.
Disabling sharing sends an explicit clear on the next connection. Forget
removes the contact's stored picture; neither action erases independent copies.
Pictures reach the WebView only through opaque local-media URLs, never paths or
remote URLs.

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
To try a direct path, each device sends the other its public IP address and
port and its local network interface addresses. A saved contact receives them on
every connection. During setup both devices exchange them before fingerprints
are approved, so anyone redeeming a live invitation learns them. A malicious
relay operator can also obtain the local addresses.
No anonymity or offline mailbox is provided. Strict direct-only is unavailable.
A sampled Direct/Relayed/Unknown display is not a packet-level policy guarantee.

Saved relay services cannot be changed silently by a peer. A service migration
requires both users' authenticated approval. The operator can update its server
list under the same service URL.

## Voice and video calls

Voice calls start off. **Allow voice calls** under **Settings → App behavior**
turns them on for this device. WhatsUp uses your default microphone only during
a call you placed or answered. A ringing call does not turn it on, and it is
released when the call ends, when you switch voice calls off or when you go
offline. Your operating system may show its own microphone indicator while a
call is in progress.

Video calls are a separate switch, **Allow video calls**, and also start off.
WhatsUp uses your default camera only while your video is on in a call you
placed or answered. Answering a video call does not have to turn it on: you can
accept without video. The camera is released when you turn it off, when the
call ends, when you switch video calls off or when you go offline. Your system
may ask once whether WhatsUp may use the camera, and may show its own camera
indicator. While a call is going through a relay, your video is held back until
you choose to send it.

Calls are not recorded. Audio and video are encrypted with fresh keys for each
call, sent to the approved contact inside the encrypted connection, and played;
nothing is saved to disk and no call history is kept beyond a notice on screen. The
**Details** readout during a call shows counters and levels only. It stays on
the device and is gone when the call ends.

A relay service, if one carries the call, cannot hear it. Like any network
observer it can see that encrypted packets are flowing between two addresses
and for how long.

## Files, exports and deletion

Received files require Accept/Decline. The exception is per-contact automatic
acceptance of PNG, JPEG, WebP and GIF images up to 25 MB, an explicit receiver
opt-in that starts off. Incomplete data and completed objects
remain encrypted inside WhatsUp. Save writes a plaintext copy to a destination
you choose through the Go dialog; Show in folder reveals it without opening it.
Those copies are outside WhatsUp's encryption and deletion controls.

Image sends apply EXIF orientation, re-encode and strip metadata by default. JPEG
photos remain JPEG. Full-size previews are sanitized on demand and use a bounded
session cache cleared on retirement. Send as file retains original bytes and
metadata as a save-only object, without image preview or automatic image acceptance. Copy/paste uses the OS clipboard and its retention policy.
Received MP4 and WebM videos are content-checked and played from the sender's
original bytes by the WebView's media decoder, only when you choose to play them.
Recovery exports contain identity, profile, contacts, history and attachments,
encrypted under a separate recovery passphrase. Import restores the saved
profile's sharing choices as well as its pictures. Keep exports and secrets
private.

Clear history keeps the contact and cancels unfinished attachments. Queued text
continues until acknowledged. Forget deletes one contact's local state while
preserving your identity and other contacts. An online removal notice stops the
peer's retries; it does not delete their history. These are logical deletion,
not physical erasure of media, memory, external exports or OS notification history.
Deleting an attachment, or clearing history, removes its stored file data but
keeps the encrypted offer record, including the file name, type, size and hash,
until Forget. These actions change only the live profile: migration snapshots
and recovery rollback data described below can still hold encrypted copies of
removed contacts, messages and files.
Managed migration backups are re-protected on passphrase changes/authenticated
reopen. Retention keeps three authenticated top-level snapshots under the current
master plus any live migration reference, in either protection mode. A separate
encrypted `managed-backups.vault` catalog authenticates creation order and exact
ciphertext digests for newly published backups. Mutable file dates do not control
pruning. Unlisted older backups and unverifiable catalog data are retained.
Unreadable
or different-key material does not count toward that limit. Complete recovery
rollback data, including nested snapshots, is retained for safety; it can consume
additional space. External old backups are not retroactively protected.
Confirmed replacement and explicit **Set aside and retry** can retain opaque or
damaged historical roots inside another master-encrypted envelope. Original
bytes remain preserved; no raw rename or plaintext recovery copy is created.
Repair messages use recognized relative file or folder names, without the OS
username or full profile path. Automatic set-aside is offered only for eligible
backup files; unsafe paths, permissions and unavailable files require local
attention without changing their contents.

## Desktop behavior and OS artifacts

Close-to-tray, when a tray is available, and generic new-message notifications
start on. Without a tray, closing the window quits. Start at login,
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

The same app-identity key contains `WhatsUpIssuedNoticesV1`, a Windows DPAPI
current-user encrypted receipt ledger. It holds at most 1,024 opaque notification
and contact identifiers, random activation proofs and issue times for 30 days,
without names or message text. A receipt is consumed before selecting a chat.
This permits checking notification clicks after restart. Invalid receipt formats
and definite corruption are reset to an empty encrypted ledger. Prior proofs
become invalid; new notifications can issue fresh proofs. Temporary registry or
ambiguous decryption failures leave the saved ledger untouched and are retried
on later notices. Missing or expired proofs
cannot select a conversation. While the app is running, older notices without a
valid proof have no conversation action. They may still cause an ordinary cold
app launch, which follows the usual OS-only/passphrase launch behavior.
Forget retires the contact's issued IDs and removes its receipt entries, including
when the notification service has not started or could not start. OS
notification history is separate and can remain visible. If OS storage prevents
cleanup, inaccessible receipt data can remain until cleanup or expiry, while
forgotten contacts cannot be selected. Restoring an approved contact permits new
notices after stale receipts are purged; old proofs are not revived.

Linux start at login uses `$XDG_CONFIG_HOME/autostart/whatsup.desktop`, normally
`~/.config/autostart/whatsup.desktop`. Disabling it removes the app-managed entry.
Installation may also create a menu/desktop launcher and optional dock entry.
Notifications use the desktop notification service over the user's session bus.

The Windows installer adds files under `%LOCALAPPDATA%\Programs\WhatsUp`,
a Start Menu shortcut and its own per-user uninstall registration. Uninstall
removes those files and the matching start-at-login value, preserving local data
and OS credentials. Notification registration/history artifacts can remain under
the separate OS retention rules described above. The Ubuntu package retains the
established executable path and AppArmor name for compatibility while displaying
one WhatsUp menu entry. Managed menu, desktop and autostart entries are removed
on uninstall. Neither installer moves the application data or WebView profile.
Action and dismissal signals must come from its current unique bus owner; IDs
from a replaced service are not reused as conversation authority.

Notifications say only **New message** by default. Enabling names/previews sends
that content to the OS notification system, including its notification history.
It can appear on the desktop or lock screen according to OS settings. Notification
activation data contains opaque identifiers and an activation proof, without
duplicating names or message previews. The message notification identifier is
per message; its contact routing identifier is stable for that saved contact.
Clicking a notification can select an existing saved conversation once storage
is available. A cold launch auto-unlocks in OS-only mode; notification clicks
never bypass an enabled app passphrase or add a contact. Turning previews off
affects future notifications; it
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
