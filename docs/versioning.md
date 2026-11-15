# Versions and compatibility

WhatsUp's current friend-testing preview is **0.1.0**. A version
identifies an application revision; it does not certify security, completeness
or release approval.

## Application version and build identifier

The application version appears in Settings. A separate build identifier
distinguishes an exact rebuild of the same version. Preview bundles may also
include checksums and a build manifest.

Rebuilding an application or updating documentation does not require a new
application version. Distinct delivered preview revisions can advance the patch
number, such as 0.0.6 to 0.0.7. Minor and major versions mark deliberate product
milestones rather than individual edits.

A build identifier or checksum helps identify a file. Neither replaces a trusted
release signature.

## Device and data compatibility

Use compatible builds on both devices. The current preview supports text with
the previous messaging protocol; attachment and contact-control features need
compatible current builds. Older incompatible peers receive an authenticated
update notice instead of silently changing identity pins. Refresh both builds
for permanent attachment cancellation: an older build can retain its own
unfinished transfer until cleared locally. Acknowledged cancellation requires
0.0.13 or later on both devices; 0.0.11 and 0.0.12 have no durable cancellation
receipt, so a pending cancellation can remain across reconnects with those peers.

An application version is separate from the format of local encrypted data and
the network protocol. A newer version may upgrade local data. An older executable
may then be unable to open it, and replacing the executable alone does not
downgrade the stored data.

Reconnecting or changing a display name does not change a device's identity.
Forgetting a contact preserves that device identity but removes its saved
association and conversation. Creating a new identity produces a new fingerprint.

## Preview status

Windows NSIS and Ubuntu DEB packages are unsigned previews. Release titles use
**vX.Y.Z (unsigned preview)**. SHA-256 checksums identify file bytes, while GitHub
build attestations identify the source and workflow. Neither is Authenticode
signing or a security certification. Signing is the next Windows release gate;
SignPath Foundation is a candidate once the repository is public. Automatic
updates remain disabled.
