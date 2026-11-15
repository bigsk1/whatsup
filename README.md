# WhatsUp

WhatsUp is a private desktop messenger for Windows and Linux. The current
**0.0.17 preview** supports up to 32 saved contacts, with a separate one-to-one
conversation and approved device identities for each contact.

It includes encrypted local message history and offline queues, resumable file
transfers, image previews, optional shared display names, private contact names,
a tray, notifications and optional app passphrase protection. Both apps must be
running to exchange data; there is no account service or offline mailbox.

This is unsigned preview software. Public downloads, signed installers and
automatic updates are not available. It has not undergone an independent
security audit. Read the [security information](SECURITY.md) before relying on it
for sensitive communication.

## Start and stop

On Windows, run the WhatsUp executable or its installed shortcut. On Linux, use
the installed application-menu entry. Supplied builds need no Go, Node.js,
development server or separate browser. See [Linux support](docs/linux-support.md)
for native dependencies and sandbox integration.

With OS-only protection, launch opens local storage and reconnects saved contacts
automatically. If an app passphrase is enabled, enter it to go online. Windows
Credential Manager or Linux Secret Service is required in both modes.

**Go offline** disconnects all contacts and closes local storage. Encrypted data
stays on the device. **Keep running when the window closes** is enabled initially;
closing the window keeps connections running when a tray is available. Without
a tray, closing the window quits. Choose **Quit** from the tray to exit. A second
launch shows the existing window. Start at login is
opt-in under **Settings → App behavior** and starts in the tray when one is
available. Clicking a notification opens its saved conversation after local
storage is available; it never bypasses the app passphrase.

## Pair with someone

1. Open WhatsUp on both devices and go online.
2. Choose **Start a conversation**, then **Create invitation**. Choose a trusted
   HTTPS relay-map URL and separately allow encrypted relay fallback.
3. **Use Tailcat public relay** fills an empty field with
   `https://tailcat.dev/derpmap.json`; it does not grant consent or fetch the map.
   The public service is best effort and rate limited, with no WhatsUp uptime
   guarantee. A compatible service you trust can be used instead.
4. Copy the invitation and send it privately to the intended person. It expires
   after ten minutes and can be used once. Cancel invalidates it immediately.
5. The other person pastes the invitation, reviews the included service host and
   explicitly allows it before WhatsUp contacts that service.
6. Compare the full reciprocal fingerprints, shown in groups of four characters,
   through a trusted channel. Approve on both devices. Names are labels, not
   identity verification. Completed setup connects automatically.

Saved identities are checked on every connection. Ordinary restarts do not need
another invitation. A changed identity stops reconnection and preserves the pin.
An authenticated incompatible old peer gets an update notice. Current and
previous messaging protocols can exchange text; some newer operations require
both devices to use the current protocol. Prefer the same preview on both.

## Messages, history and files

**Queued** means saved encrypted on your device. **Delivered** means the
receiving app durably saved and acknowledged the message, not that it was read.
Messages retry after the other app reconnects. Stable IDs prevent duplicates.
While a contact is unavailable, **Waiting for their device** means automatic
retries are still enabled. After five minutes of failures, ordinary retries slow
to roughly two minutes with jitter. Go offline stops them; successful
authentication restores the normal retry schedule.
There is no lifetime message-count cap. The default per-contact message quota is
1 GiB of logical record storage; a full state is shown when it is reached. This
is not an exact cap on database, journal or filesystem space.

Select a contact to view its conversation. Local message deletion removes the
visible message. **Clear history** keeps the contact, clears local history and
cancels unfinished attachments; already queued text continues until acknowledged.
Neither action deletes another person's copies or guarantees physical erasure.

Use the Go file picker, file drop or **Paste image** to prepare an attachment,
then explicitly send it. The receiver chooses **Accept** or **Decline**. A contact
may separately enable automatic acceptance of images up to 25 MB. Files are
limited to 2 GiB each and logical attachment storage to 4 GiB per contact.
Transfers can be paused/resumed or cancelled. Completed files remain encrypted
inside WhatsUp until **Save…** writes a copy to a destination you choose.
**Show in folder** reveals a saved copy; the app never opens arbitrary files.
Cancellation is saved locally and retried until a compatible peer acknowledges
it. An older peer may need an update before its unfinished copy can be cancelled;
text messaging remains available. A file completed before cancellation may stay
on the other device.
Use **0.0.13 or later on both devices** for acknowledged cancellation. The older
0.0.11 and 0.0.12 protocols cannot confirm remote cancellation, so their pending
notices can remain until both devices are updated.

PNG, JPEG, WebP and static GIF previews use bounded Go decoding and sanitized
thumbnails. **View image** opens a full-size preview with fit and actual-size
controls. Image sends apply EXIF orientation, re-encode and strip metadata. JPEG
photos remain JPEG; other supported static images use PNG. If an image is
unsupported, animated or too large to preview, explicitly choose **Send as file**
to preserve its original bytes and metadata. File mode is save-only, without
image auto-accept or previews. MP4/WebM playback starts only on click; Linux
playback depends on installed GStreamer codecs. Other file types are save-only.
No HTML, SVG or active Markdown preview is provided. The attachment panel can be
collapsed without losing transfer state or saved items.

Completed photos and supported videos also appear in the conversation. New
attachments use the local time when they were first offered or queued; older
attachments without a recorded time are labelled as saved attachments. Video
still requires an explicit click. Original-file mode remains save-only.
Dragging files over an eligible chat highlights the drop area; dropping prepares
a preview, and sending still requires your action. File paths stay in Go.

**Delete attachment…** removes one attachment from WhatsUp on this device and
cancels unfinished work. It preserves files saved elsewhere and the peer's copy.
Small encrypted records remain to reject replays and finish cancellation or
completion acknowledgements. **Settings → Contact** shows separate message and
attachment budgets, including queued or accepted file reservations and retained
records. These are logical quota counters, not physical allocation or free disk
space. Deleting files can reclaim their reservations without forgetting the contact.

## Settings and privacy

Settings has Profile, Contact, Appearance, App behavior, Security & recovery,
and Privacy & diagnostics pages. Shared display names start off; private contact
names stay local. Display names never replace fingerprint verification.

Appearance combines a look (Bubbly, Arcade, Neon City or Terminal) with any of
eighteen color schemes, including Dracula, Nord, One Dark, Tokyo Night, Catppuccin,
Solarized, Gruvbox, GitHub and retro console palettes. You can match the system's
light and dark mode, choose a text size and turn animations and effects off.
Animations also stop when the system asks for reduced motion. All fonts and icons
ship inside the app; none load from the network.

Notifications start on and say only **New message**. Contact names and message
previews are separate opt-ins. Enabling them exposes that content to the desktop
notification system and its history. Turning them off does not erase notices
already retained by the OS. Appearance is stored in the private WebView;
behavior preferences, names and conversation data are encrypted locally.

In Security & recovery, optionally require an app passphrase in addition to the
OS credential. Keep it safe. Recovery export uses a separate recovery passphrase
and includes identity, saved contacts, history and attachment data. Import is an
explicit replacement operation. Losing required secrets can make data
unrecoverable. Exports and files saved outside WhatsUp are your responsibility.
If a managed backup prevents going online or changing protection, Security &
recovery identifies the relative file or folder name. For eligible backup files,
**Set aside and retry…** authenticates the current protection and preserves the
backup's exact bytes inside an additional encrypted envelope. Unsafe folders,
permissions or unavailable files require local attention before retrying; the
app does not offer an automatic repair it cannot safely perform.

**Forget contact…** deletes that contact's local credentials, names, history and
queue while preserving your identity and other contacts. When the peer is online,
an authenticated removal notice stops its retries; it keeps its own history.
A relay-service change requires approval on both devices and preserves identity
pins. A peer cannot silently change providers during reconnection.

Automatic connections prefer a direct path and permit consented encrypted relay
fallback. The live indicator shows **Direct**, **Relayed** or **Unknown**; stale
samples become Unknown. Setup and rediscovery still depend on the selected relay
service. Transfers over 100 MB need additional relay-bandwidth approval when a
fresh direct sample is unavailable. This is a bandwidth precaution, not a strict
packet-level direct-only guarantee. Strict direct-only remains unavailable.

WhatsUp has no application analytics, automatic diagnostic upload or remote crash
reporting. Windows uses the system WebView2 with a private profile separate from
Edge. Diagnostic data follows Windows privacy settings; required vendor
WebView2 diagnostics may still occur. Linux uses WebKitGTK and private data/cache
paths. Read the [privacy guide](docs/privacy.md) for OS artifacts and boundaries.

## Current limits

There is no group chat, offline mailbox, conversation search, profile-picture
sharing or public contact directory. Backward protocol support is limited; an old
executable may also be unable to read upgraded local data. Native video playback,
all desktop environments and all network conditions are not universally verified.
There is no signed public distribution or in-app updater.

See [Linux support](docs/linux-support.md), [versioning](docs/versioning.md),
[security information](SECURITY.md) and [third-party notices](THIRD_PARTY_NOTICES.md).
