# WhatsUp

WhatsUp is a private, one-to-one desktop messenger for Windows and Linux.

The current **0.0.6 preview** supports identity-verified pairing, text messages,
encrypted local history and queued messages, automatic reconnection, optional
shared display names, and private contact names.

WhatsUp is still preview software. Signed installers, automatic updates and a
public download are not available yet. It has not undergone an independent
security audit. See the [security information](SECURITY.md) before relying on it
for sensitive communication.

## Start the app

On Windows, launch the WhatsUp executable. On Linux, use the installed WhatsUp
application-menu entry. A supplied preview build does not need a development
server or a separate browser to run.

Linux requires the native libraries and desktop services described in
[Linux support](docs/linux-support.md). Use the same preview version on both
devices. Version 0.0.6 upgrades existing local data; installing an older executable
does not downgrade that data.

Choose **Unlock and connect** to open local storage and reconnect to a saved
contact. If no contact is saved, the app is ready for a new invitation. Local
storage uses Windows Credential Manager or Linux Secret Service to protect its
encryption key.

## Pair with someone

1. Launch and unlock WhatsUp on both devices.
2. On one device, choose **Start a conversation**, then **Create invitation**.
   Enter a trusted connection service's HTTPS relay-map URL and explicitly allow
   encrypted relay fallback.
3. Choose **Copy invitation** and send the link privately to the intended person.
   Invitations expire after ten minutes and can be used once. Cancel ends the
   invitation immediately.
4. On the other device, choose **Paste invitation**, paste the link, enter the
   same trusted service URL and give relay consent.
5. Compare the full reciprocal fingerprints through a trusted channel and approve
   on both devices. A display name is a label; the fingerprint identifies the
   device's key.
6. After both approvals and completed setup, the apps start connecting
   automatically.

The relay field starts empty and consent starts unchecked. The invitation page
lists Tailcat's public relay map with a **Copy relay URL** button:

    https://tailcat.dev/derpmap.json

Copying it does not fill the field, grant consent or contact the service. You can
use that public service or a compatible service you trust. The public service is
best effort and rate limited; WhatsUp does not guarantee its availability.

Automatic mode prefers a direct connection and permits encrypted relay fallback.
The selected service can observe connection metadata, including IP addresses and
availability. Strict direct-only mode and a live Direct/Relayed indicator are
not available in this preview.

Fresh connection setup also needs the selected relay service for rendezvous,
even when messages subsequently use a direct path. Relay availability matters
for reconnection as well as initial pairing. An established direct connection
does not need the relay to forward each message.

## Messages and reconnection

Send plain text in either direction. **Queued** means a message is stored
encrypted on the sending device. **Delivered** means the authenticated receiving
app has saved it and acknowledged receipt.

Both apps must be running and connected to exchange messages. The relay is not
an offline mailbox. If a contact is offline, outgoing messages remain in the
local encrypted queue and retry after reconnection.

This preview stores at most 256 messages and 256 KiB of message text per
conversation. Saved history is not automatically deleted to make room. At the
limit, new messages cannot be saved; the app explains the capacity problem and
keeps undelivered text queued rather than marking it delivered.

The saved device identities are checked automatically on every connection.
Ordinary disconnects, restarts and reconnections do not require another
invitation or manual fingerprint approval. An identity mismatch stops
reconnection and preserves the saved key.

When WhatsUp can authenticate a saved contact running the previous incompatible
messaging protocol, it stops automatic retries and asks for compatible app
builds. Update both apps, then choose **Connect**. Other authentication failures
show a notice to check compatible builds and device clocks while retries continue.

## Names and settings

Settings opens a workspace with **Profile**, **Contact**, **Appearance**, and
**Privacy & diagnostics** pages. **Back to chats** returns without disconnecting.

In **Profile**, save a display name and enable **Share my display name** if you
want your approved contact to receive it. Sharing starts off. The name travels
through the authenticated connection after pairing approval; it is not included
in the invitation link. Saved changes are sent while connected. Changing profile
settings while offline does not connect the app.

Clearing a shared name or disabling sharing clears the received shared label
when the peer is connected. It does not erase their private nickname or copies
outside WhatsUp.

In **Contact**, assign a private name to the saved contact. This name stays on
your device. Chat displays your private contact name first, then their shared
display name, then the generic contact label. Changing a name does not change
the identity fingerprint.

Appearance supports dark, light and system themes, with teal, blue and rose
accents.

## Disconnect or forget a contact

**Disconnect and lock** stops networking, closes local storage and clears the
displayed conversation and decrypted names. The saved contact, history and queued
messages remain encrypted for the next unlock.

Unlock uses your operating system's credential store. There is currently no
separate WhatsUp password, so someone using your unlocked desktop session can
also unlock WhatsUp. Lock your operating system session when leaving the device.

**Forget contact…** opens a confirmation to delete that contact's local
credentials, saved messages, queued messages and contact names. Your own profile,
device identity, fingerprint and appearance preferences remain. You can then
create or paste a fresh invitation without restarting.

Forget affects only this device. It does not delete the other person's copies or
guarantee physical erasure. To reuse another device with a different pairing,
remove its old contact there too. A new pairing uses fresh contact keys.

## Current limits

- One saved conversation with two approved device identities.
- Plain text only. Files, images, videos and profile pictures are not available.
- No group chat, conversation search, backup/restore interface or recognition
  record surviving full Forget.
- No tray/background mode, notifications or startup preferences. Closing the last
  window quits the app.
- No strict direct-only mode, live path indicator, signed installer or automatic
  update.

## Privacy and security

WhatsUp has no application analytics or remote crash reporting. Local diagnostics
are bounded and redacted, with no automatic upload. Names, messages and contact
state are encrypted locally; appearance is the only preference stored in the
WebView.

Windows uses the system WebView2 runtime with a private profile separate from
Edge. Optional vendor diagnostics follow Windows privacy settings, and required
WebView2 diagnostics may still occur. Linux uses the system WebKitGTK runtime and
private application data/cache directories.

Read the [privacy guide](docs/privacy.md), [security information](SECURITY.md),
[Linux support](docs/linux-support.md), [version guide](docs/versioning.md), and
[third-party notices](THIRD_PARTY_NOTICES.md) for details.
