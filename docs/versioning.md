# Application versions and development builds

WhatsUp starts at **0.0.1**. Patch numbers provide room for useful test revisions
without quickly advancing the minor or major version. The initial foundation's
provisional metadata is corrected to 0.0.1 before this review build is delivered;
that correction does not rewrite the existing Git baseline or publish a release.

Keep these three identifiers separate:

| Identifier | Purpose | Example |
| --- | --- | --- |
| Application version | A delivered application/test revision | `0.0.1` |
| Development build ID | The exact rebuild, source revision and dirty state | `20261114T060000000Z-g27e4c4898dbc-dirty` |
| Storage/protocol schema | Compatibility of persisted state or wire messages | Unchanged by an app rebuild |

A timestamp or Git revision belongs in the development build ID and manifest;
it does not increment the application version. Rebuilding the same test revision,
rerunning QA, updating documentation, or making an intermediate edit can retain
the same app version and receive a new build ID. Build IDs never imply release
approval, signatures or a clean source tree.

Use the next patch version, such as **0.0.2**, when delivering a distinct test
revision that needs to be identified separately from the earlier app revision.
Do not bump the app version automatically for every feature edit, review pass,
dependency update, commit or build. Record the reason for a patch increment and
what changed in the delivered revision. Retain older review builds and their
checksums instead of overwriting them.

Minor and major changes are deliberate owner-approved milestones. Do not jump
to 0.1.0 or 1.0.0 because a development session adds several features. A version
number does not certify security or completeness. The current 0.0.2 app still
has live pairing, messaging and release gates closed.

The frontend package version is the application-version source of truth. Keep
the root workspace/lock metadata, Go desktop version, browser fallback version
and native metadata aligned with it. Windows MSIX uses the corresponding fourth
numeric component (`0.0.2.0` for 0.0.2); this is packaging metadata, not another app release.
The dependency-free workflow tests check that alignment. The retained installer
metadata does not mean installers, signing or release packaging are implemented.

Application version changes do not change dependency/tool pins, the encrypted
vault format, the OS credential account, or schema numbers. Those changes need
their own compatibility review. Use the normal frozen build workflow for test
builds; keep release packaging and publishing behind their existing gates.
