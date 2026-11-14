# Security posture

WhatsUp has a development desktop shell and tested backend components. Its live
messenger protocol and security integration are incomplete. Do not use it for
sensitive communication or describe it as an audited messenger. Tailcat's
production networking components do not establish the safety of this application.

The current bridge exposes only status and local storage lock/unlock. It starts no
network listener and exposes no pairing, message or file API. Strict direct-only
remains unavailable. See [the local storage design](docs/storage.md) and
[the transport source review](docs/transport-review.md) for implemented boundaries
and evidence limits. Component tests do not establish safe live communication.

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
