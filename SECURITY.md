# Security posture

WhatsUp has a development desktop shell and tested backend components. Its live
messenger protocol and security integration are incomplete. Do not use it for
sensitive communication or describe it as an audited messenger. Tailcat's
production networking components do not establish the safety of this application.

The bridge exposes nine typed methods for status, local storage lock/unlock,
reserved pairing decisions, and explicit invitation creation/joining/cancellation.
Only Create returns a private bearer link for transient sharing; ordinary status
polling contains no link or transport credentials. Launch and unlock start no
listener. Create/Join require a separately selected trusted HTTPS relay-map URL
and explicit consent to encrypted relay fallback before network I/O. No message,
file or generic transport API is bound. Strict direct-only remains unavailable.
See [the local storage design](docs/storage.md) and
[the transport source review](docs/transport-review.md) for implemented boundaries
and evidence limits. Component tests do not establish safe live communication.

The live invitation adapter uses separate ephemeral WireGuard credentials and
only its fixed pairing TCP service. The TLS 1.3 channel proves both Ed25519
identities, pins the exact invitation owner/context, and requires bearer
reservation before either fingerprint prompt. Reciprocal consent provisions one
shared contact PSK and two distinct node keys. Encrypted pending writes precede
prepared receipts; matching durable committed receipts precede promotion of that
exact bundle. Successful completion requires authenticated terminal receipts,
TLS close-notify and bounded supported TCP draining before invitation teardown.
Immediate abort remains available for cancellation, malformed traffic or shutdown.
Denial is acknowledged before ordinary teardown, including after the other side
has approved. Persistence callbacks are joined before removal or vault lock.
This is not an atomic transaction spanning two machines: a lost connection or
crash can leave an inert record requiring reconciliation or explicit discard.

Encrypted schema 4 migrates schemas 1/2/3 while preserving identity, wrapping
credential, pins and chat sections. Restored pending and committed records grant
no live transport or history authority. Fresh authenticated reconfirmation of a
restored contact and ongoing messaging are unfinished. Older executables reject
the newer schema; preserve the vault and its OS credential before migration.
Authenticated reservations reject the legacy immediate-consumption approval
path. See the
[pairing protocol boundary](docs/pairing-protocol.md).

Application/tool telemetry is disabled by default. Bounded, redacted operational
diagnostics remain local for troubleshooting. The owner explicitly permits the
documented system WebView2 diagnostics on Windows; no broader analytics or remote
crash-reporting exception applies. See [privacy controls](docs/privacy.md).

## Boundaries

Trusted components are the reviewed application build, the approved contact's
pinned identity, and the local operating system. Current local development
candidates are unsigned; signed, verified artifacts are required for release.
Peers, invitation payloads,
attachments, relay operators, update mirrors, and restored backups are untrusted
inputs. A compromised endpoint can read messages available to that endpoint.

The selected operator map is a trust decision about outbound service destinations,
not permission for an invitation to choose arbitrary hosts. Fetches require normal
HTTPS certificate validation, bounded JSON, public resolved addresses and no
redirects or inherited proxy. The adapter pins vetted addresses, replaces peer
metadata with the selected profile, and rejects unknown regions, private targets
and insecure test overrides. No default public map is fetched. The operator can
observe IP addresses/availability and carry encrypted pairing packets; the
application does not promise anonymity.

Tailcat is pinned to a reviewed upstream revision with a minimal local BSD-3-Clause
privacy patch. Its file/license inventory is checked against
`config/tailcat-patch.json`. The patch disables optional DERP application-statistics
labels and bearer-address debug logging. Supported build tags omit inherited
proxies, router port mapping and captive-portal probes. Custom builds missing
these networking controls cannot start invitation transport. This does not prove
strict direct-only packet enforcement or eliminate the selected relay's necessary
connection metadata. See [third-party notices](THIRD_PARTY_NOTICES.md).

The Wails frontend is a renderer, not a secret store or general local API. The Go
backend owns pairing, policy enforcement, identity material, and storage. A webview
does not automatically sandbox the Go process. No inherited shell/SSH, forwarding,
exit-node, or directory-serving features from the Tailcat CLI are exposed.

## Required protections

- Atomically consume expiring invites; bind approval to one identity and invalidate
  the invitation's transport/credentials after use. Never authorize on a name.
- Restrict established sessions to the approved peer. Enforce revocation on live
  sessions as well as subsequent attempts. New pairing cannot reveal old history.
- Keep the WireGuard per-contact pre-shared key enabled. Pin a reviewed Tailcat
  revision and track its upstream security fixes.
- Implement strict direct-only policy at transport boundaries. Do not trust a
  status snapshot to control fallback. Discovery traffic remains distinct.
- Encrypt local identity, queue, history, and attachment state with a reviewed
  storage design and OS credential protection. Fail closed if the credential
  store is unavailable; no plaintext fallback. Do not store data beside the binary.
- Limit message/frame sizes, attachment bytes, decoded image dimensions, receive
  time, disk consumption, and concurrent pending requests. No automatic file open.
- Render untrusted text safely. No arbitrary HTML, SVG, remote image loading,
  executable links, or plaintext content in native notification defaults.
- Use restrictive content security policy, bundled frontend assets, and a minimal
  typed bridge. No public HTTP content/management listener or generic file bridge.
- Accept only platform-appropriate, verified signed updates. Reject unsigned,
  modified, expired, wrong-platform, or disallowed rollback update manifests.
- Encrypt exports with a reviewed authenticated format and password KDF; never
  export plaintext keys/history by default. Restores require explicit identity
  and history merge decisions and protection against duplicate active identities.

## Evidence required before a release

The test plan in `docs/development.md` is a release gate. At minimum, independent
clients must exercise expired/replayed/concurrent invitations, an unknown third
peer, identity substitution, revoked sessions, malformed data, local restart,
attachment traversal/size attacks, path changes during retransmission, and
tampered updates/backups. Record real direct and relayed traffic evidence.

Audit both direct and transitive dependencies with current advisory data. Keep
full scan results private where needed and never interpret an unavailable scanner
or advisory service as a clean scan. Signing secrets and live identities stay
outside the repository. CI uses least privilege and full-SHA-pinned actions.

## Reporting

No public reporting address or repository has been selected. Do not invent one.
Before publishing, configure a private vulnerability reporting route and replace
this paragraph with the actual supported-version and reporting policy.
