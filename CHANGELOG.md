# Changelog

## 0.2.1 (unsigned)

- Mute now takes effect the moment you press it. Before, WhatsUp kept sending
  your audio until the other device had been told, so a slow or failed
  connection could leave you audible for a moment after pressing Mute. Your
  microphone now stays muted until you unmute it, whatever happens to that
  message.
- Incoming call audio and video that arrive faster than your device can decode
  them are dropped instead of building up, which keeps a call from falling
  behind or using more and more memory.
- The temporary build-tooling exception for the picomatch advisory is removed.
  GitHub withdrew that advisory; dependency scans now accept no exceptions.

## 0.2.0 (unsigned)

- Voice calls between two connected contacts. Calls are off until you turn on
  **Allow voice calls** under **Settings → App behavior**. Place a call from the
  phone button; the other person accepts or declines. The call bar shows the call
  time, **Direct** or **Relayed**, and a mute control. Call audio is encrypted
  with fresh keys for every call. The microphone is used only during an answered
  call and is released as soon as it ends. Both devices need a version with
  voice calls.
- Video calls between two connected contacts, off until you turn on **Allow
  video calls** under **Settings → App behavior**. Place one from the video
  button; the other person can accept with video, accept without video or
  decline. The camera button turns your camera off and on during the call, and
  while someone's camera is off you see their profile picture. Video is
  encrypted like call audio. On a relayed connection your video waits until the
  connection is direct or you choose **Send video over the relay**. The camera
  is used only while your video is on in an answered call.
- On Linux, sending video needs WebKitGTK 2.54.3 or newer. With an older
  version WhatsUp tells you the camera picture is not updating and continues
  with voice; received video is shown either way. See
  [Linux support](docs/linux-support.md#calls).
- **Silence the ring sound** in App behavior turns off the ring for incoming and
  outgoing calls.
- On Linux an incoming call now rings even when the WhatsUp window has not been
  clicked since it started, for example after starting at login.
- On Linux desktops that don't let an app raise its own window, the tray's
  **Show WhatsUp**, launching WhatsUp again and an incoming call now bring a
  minimized or covered window to the front.
- On GNOME and other Wayland desktops the running WhatsUp window now belongs
  to its launcher: one dock icon instead of two, and the desktop names the app
  **WhatsUp**. WhatsUp's application ID is now `io.github.bigsk1.whatsup`, and
  the menu entry is named `io.github.bigsk1.whatsup.desktop` to match, so that
  the desktop can ask for camera permission in WhatsUp's name. Package upgrades
  rename the entry for you; a dock pin made before the upgrade needs pinning
  once more. If you created the menu entry by hand from the Arch guide, rename
  it to `io.github.bigsk1.whatsup.desktop` and set
  `StartupWMClass=io.github.bigsk1.whatsup`.
- The sidebar tagline is now **Private & encrypted**.

## 0.1.2 (unsigned preview)

- Pause and resume one contact’s connection while other conversations stay
  online. The pause survives restarts, and saved history and queued messages
  stay on your device. Resume all paused contacts before reopening the same
  profile in WhatsUp 0.1.1 or earlier.
- Renamed the sidebar field to **Filter contacts** to clarify that it matches
  contacts by name or fingerprint.
- Conversation controls use compact icons to leave more room for contact names.

## 0.1.1 (unsigned preview)

- Copy text from either side of a conversation, including URLs and line breaks,
  with a brief confirmation that screen readers can announce. Desktop copy
  actions use the native clipboard for messages, invitations and signed
  connection-service text. Right-click editing supports copying selected text
  and pasting into the message box. URLs stay as text, without automatic previews
  or remote requests.
- Chat follows incoming messages while you are at the bottom. Reading earlier
  messages keeps your place, with a New message button to return to the latest.
  Sending reveals your own message, and loading older history keeps your place.
- The compact sidebar appears when you narrow the desktop window. The new
  conversation button is smaller, and attachment deletion fits narrow windows
  and long filenames.
- Invitation setup explains the one-use, ten-minute lifetime before creation.
  A countdown shows how long a created invitation has left.
- The version in the bottom corner opens the WhatsUp GitHub repository.
- Added installation instructions for Arch Linux and Omarchy.

## 0.1.0 (unsigned preview)

- Profile pictures. Pick a photo for yourself and choose whether contacts can see
  it, and turn contacts' photos off if you prefer letters. Photos are stripped of
  metadata and stored encrypted. They are only labels; fingerprints still verify
  who someone is.
- Installers for Windows and Ubuntu 24.04. Upgrading keeps your identity,
  contacts and history.
- The app no longer calls itself a development build, and the README now covers
  installing, pairing, verifying downloads and exporting a recovery copy.
- Releases are built, tested and published by GitHub Actions, with checksums,
  SBOMs and build attestations.

## 0.0.17

- Four interface looks and eighteen color schemes, with bundled fonts and icons.
- Native file-drag highlighting and inline image/video presentation.
- Individual local attachment deletion and per-contact storage meters.
- Slower reconnection attempts after a device has been unavailable for several
  minutes, with a clearer waiting state.

Earlier previews introduced encrypted history, one-to-one pairing and messaging,
multiple contacts, attachments, optional passphrase protection, recovery exports,
tray behavior and private notifications.
