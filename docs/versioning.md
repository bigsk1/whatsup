# Versions and compatibility

WhatsUp's current preview series uses versions such as **0.0.6**. A version
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

Use compatible versions on both devices. The 0.0.6 preview requires 0.0.6 on both
sides for its updated messaging and shared-name connection.

An application version is separate from the format of local encrypted data and
the network protocol. A newer version may upgrade local data. An older executable
may then be unable to open it, and replacing the executable alone does not
downgrade the stored data.

Reconnecting or changing a display name does not change a device's identity.
Forgetting a contact preserves that device identity but removes its saved
association and conversation. Creating a new identity produces a new fingerprint.

## Preview status

Signed installers and automatic updates are not available yet. Preview builds
are unsigned and are not release-approved merely because they have a version
number or installer metadata.
