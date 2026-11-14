# WhatsUp architecture and product contract

## Stack and layout

Use a Go backend embedded in a Wails v3 desktop app, with a Svelte 5/TypeScript
frontend, Tailwind, and selected shadcn-svelte components. A webview UI does not
make the application an OS sandbox. Package and test Windows and Linux separately.

Component layout (see docs/status.md for which parts are connected to the app):

- `frontend/src/lib/components/`: contact sidebar, chat pane, composer, dialogs.
- `frontend/src/lib/themes/`: dark/light themes and user-selected accent tokens.
- `internal/pairing/`: invitations, approval, and single-use state transitions.
- `internal/identity/`: long-lived identity and approved contact key bindings.
- `internal/chat/`: framing, acknowledgements, deduplication, and local outbox.
- `internal/transfer/`: bounded, approved attachment transfers.
- `internal/transport/`: isolated Tailcat integration and enforced delivery policy.
- `internal/storage/`: encrypted local persistence, search, and backups.
- `scripts/`: shared developer and release commands.

The root `AGENTS.md`, this contract, and `docs/status.md` are the handover points.

The current desktop bridge exposes only GetStatus, UnlockStorage and LockStorage.
Only public status DTOs cross it. Identity state can be encrypted locally; invitation,
contact, transport and chat domain components are deliberately not bound until
reciprocal transport authentication and coordinated persistence are integrated.
No runtime listener starts in this implementation. A locally approved Contact
object alone does not establish a mutual, authenticated live session.

## One-to-one pairing

An invitation has a cryptographically random secret, expiry, protocol version,
ephemeral transport address, and identity binding. Encode it in a private app
deep link and/or QR code, without public lookup or DNS publication. Do not log it.
Default lifetime: 10 minutes; single redemption; revoke button; invalid on restart.

Backend state transitions:

`created -> pending approval -> consumed`, with terminal `expired` and `revoked`.

Validate expiry and compare the secret in constant time, then reserve the invite
atomically for exactly one candidate identity. Bound pending state by the invite's
expiry. A denied/aborted attempt burns that invite; a fresh invite is needed.
Concurrent attempts cannot both succeed. No history or file access before approval.
Both people verify a fingerprint/QR identity match over a trusted channel.

After mutual approval, persist the approved identity and provision separate
contact/session credentials. Tear down the invite listener and erase its secrets.
Restarting the app, reopening a deep link, or restoring a history backup must not
revive an invite. A contact session admits exactly one pinned peer and provides
no group membership or implicit fan-out. An invite alone does not prove a person's
real-world identity; the verification/approval step provides that binding.

## Delivery policy

| Setting | Payload path | If a direct path is unavailable |
| --- | --- | --- |
| Direct only (planned default) | Direct device-to-device only | Queue locally; keep discovery/retry active. |
| Allow encrypted relay fallback | Prefer direct; DERP may carry encrypted payload | Deliver through a live relay if both apps are reachable. |

DERP is not an offline mailbox. If either app is stopped, live relay fallback
does not make it receive messages. The sender keeps an encrypted outbox and
retries with jittered, bounded backoff, wakes on network changes, and requires
authenticated acknowledgements. Stable IDs and durable deduplication prevent
duplicates if an acknowledgement is lost. Queue limits and cancellation are visible.

Stock Tailcat uses DERP for initial discovery and transparent fallback. Its public
API inspected on 2026-11-13 does not provide an established strict payload-only
direct gate. Polling status then writing is racy: lower-level fallback can relay
buffered/retransmitted packets after the check. Never advertise strict direct-only
until enforcement is implemented in both directions at packet send/receive.

First investigate a supported upstream hook. Otherwise prepare a minimal reviewed
adapter/patch with immutable versioning, upstream notice compliance, and tests that
block relayed WireGuard application traffic while retaining required discovery.
Treat other peer relays as relayed too. Disabling all DERP breaks the normal
bootstrap/re-discovery design and is not a substitute. The stricter endpoint's
policy wins; there is no silent downgrade during reconnect or a settings change.

## Files and images

Initial allowlist: `.txt`, `.md`, PNG, JPEG, WebP. Proposed initial maximum:
25 MiB per attachment, configurable only within backend safety limits. Receive
approval precedes transfer; no automatic opening or OS execution. Validate actual
content and bounded decoded image dimensions, not the supplied extension alone.
Reject active SVG/HTML. Escape text/Markdown and disable raw HTML and remote assets.

Offer metadata, accept/reject, chunk transfer, authenticate/hash, finalize, then
acknowledge. Use receiver-selected destinations, generated temporary names, atomic
finalization, filename sanitization, disk quotas, and cancellation. The peer never
supplies a trusted local filesystem path. Memory usage must not scale with file size.
Image paste creates a local preview and requires Send. Clear clipboard retention
and metadata handling must be documented; do not silently claim EXIF stripping.

## Local experience

Use the provided dark teal reference for spacing, sidebar, chat pane, composer,
and network footer. Ship light/dark themes and accent tokens; no design-time
sample people/messages mixed with real runtime state. Display actual connection
state, verified identity, queue state, and delivery acknowledgements. Never hardcode
crypto suite names from the screenshot: show verified library capabilities only.

Tray close keeps the listener alive only when enabled; Quit terminates it.
Background listening, startup behavior, local notification previews (off by default),
and privacy lock are explicit settings. Search indexes stay inside encrypted local
state. No cloud analytics, remote avatars/fonts, link previews, or content fetching.

## Self-hosting and updates

A configured self-hosted DERP can handle bootstrap and fallback. Hosting your own
relay provides operational control, but latency depends on location and privacy
still depends on logging, TLS, and host security. Use direct TCP/TLS exposure and
STUN ports as documented upstream; do not assume a generic HTTP proxy works.

Update verification uses an application signing identity distinct from contact
identities. Create native packages, SBOMs, hashes, and signed update metadata in
CI. Store no signing secrets in source. Configure signing and remote publishing
only after the owner selects destinations and credentials. No unsigned fallback.

## Primary references inspected

- https://github.com/tailscale/tailcat/blob/main/README.md
- https://github.com/tailscale/tailcat/blob/main/SECURITY.md
- https://github.com/tailscale/tailcat/blob/main/tailcat.go
- https://github.com/tailscale/tailcat/blob/main/wire.go
- https://github.com/tailscale/tailscale/blob/main/wgengine/magicsock/endpoint.go
- https://github.com/tailscale/tailscale/blob/main/wgengine/magicsock/magicsock.go
- https://github.com/tailscale/tailscale/blob/main/cmd/derper/README.md
- https://github.com/wailsapp/wails/releases/tag/v3.1.0
- https://wails.io/faq/
