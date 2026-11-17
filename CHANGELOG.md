# Changelog

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
