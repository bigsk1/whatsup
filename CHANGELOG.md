# Changelog

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
