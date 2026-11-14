# Tailcat integration review

Reviewed on 2026-11-13 for the initial WhatsUp implementation. This is a source
and API review with local wrapper tests, not an independent cryptographic audit
or evidence that WhatsUp has exchanged data through WireGuard, DERP, or two devices.

## Immutable dependency selection

The selected revision is
[`e00cdde6ff787f801361ff85ed085775c775d4c5`](https://github.com/tailscale/tailcat/commit/e00cdde6ff787f801361ff85ed085775c775d4c5),
committed 2026-11-05. Go resolves it as
`github.com/tailscale/tailcat v0.7.1-0.20261105151022-e00cdde6ff78`.
The latest published tag observed was
[`v0.7.0`](https://github.com/tailscale/tailcat/releases/tag/v0.7.0).
Normal builds must use the immutable module version and generated checksums.
An update must not silently substitute a newer main revision or revert to the tag.

This post-release pin is deliberate. Relevant changes after v0.7.0 include:

- [`aa8950c58f81b4799a57cef06e73a6a3b948f421`](https://github.com/tailscale/tailcat/commit/aa8950c58f81b4799a57cef06e73a6a3b948f421)
  exposes `Server.PeerKey`, allowing the application to identify an accepted
  stream's authenticated WireGuard peer without trusting its display name or IP.
- [`7d80847ede152e5ac17a64865c216be15831d0f2`](https://github.com/tailscale/tailcat/commit/7d80847ede152e5ac17a64865c216be15831d0f2)
  replaces `AllowedClients` with `AllowClient`, and adds `KeySet` and
  `DisconnectClient`. A nil hook allows everyone. Removing admission does not
  evict a connected peer; callers must also disconnect it and close live streams.
- The selected revision updates Tailscale for a netstack shutdown fix. Its
  [module manifest](https://github.com/tailscale/tailcat/blob/e00cdde6ff787f801361ff85ed085775c775d4c5/go.mod)
  requires Go 1.27.2 and uses
  `tailscale.com v1.105.0-pre.0.20261013225543-fb79793d99f5` and
  `github.com/tailscale/wireguard-go v0.0.0-20260928213353-e61b6b78110b`.
  This upstream prerelease dependency is part of the deliberate reviewed pin,
  not a claim that the whole dependency graph is stable or vulnerability-free.

Upstream's
[security policy](https://github.com/tailscale/tailcat/blob/e00cdde6ff787f801361ff85ed085775c775d4c5/SECURITY.md)
describes Tailcat as experimental and discusses past address handling, malformed
discovery, and key-linkability issues. Those fixes are present in the selected
history. Do not expose the CLI's shell, filesystem, SSH, proxy, or exit-node
features through WhatsUp. Current advisory scans remain a separate required gate.

## Direct-only remains unavailable

The inspected public
[`Server` and `Client` API](https://github.com/tailscale/tailcat/blob/e00cdde6ff787f801361ff85ed085775c775d4c5/tailcat.go)
does not expose an application-payload relay gate at both packet boundaries.
WireGuard PSKs are enabled by default, but addresses contain that secret and
must be treated as bearer credentials. Discovery and fallback share the lower
transport. Connection status can describe a path; it cannot prevent later
fallback, buffered transmission, or TCP retransmission through a relay.

`internal/transport.ValidatePolicy` therefore rejects `DirectOnly`, including
when relay fallback was approved or a direct path was observed. `RelayFallback`
requires a separate explicit approval. Neither the UI nor a status poll may
override that guard. Before enabling strict mode, review a supported upstream
hook or maintain a narrowly versioned patch in the Tailscale packet send/receive
paths, then capture failure-path traffic proving zero application packets at
the relay while discovery continues. No such patch or evidence exists yet.

The resolved Tailscale source confirms the relevant lower boundary:
[`endpoint.addrForSendLocked` and `endpoint.send`](https://github.com/tailscale/tailscale/blob/fb79793d99f5ccedabe1ab428f0af852f49d0c62/wgengine/magicsock/endpoint.go)
can choose both UDP and DERP when path trust expires, including peer UDP relays.
[`magicsock.Options.OnDERPRecv`](https://github.com/tailscale/tailscale/blob/fb79793d99f5ccedabe1ab428f0af852f49d0c62/wgengine/magicsock/magicsock.go)
is a receive hook below Tailcat's API; Tailcat uses it for bootstrap. It does
not provide the required matching outgoing gate or a peer-relay policy.

## Narrow adapter implemented

`internal/transport.ContactServer` is backend infrastructure, not a Wails service.
Its construction performs no network activity. Pairing.Contact now records local
identity approval only; it no longer generates a separate session key or PSK.
The transport owns one opaque node-key/PSK bundle per local approval, with atomic
one-time preparation and shared-copy revocation. The same PSK is supplied to
Tailcat rather than generating an unrelated second secret.

Preparing credentials does not activate a contact. Construction requires a
reciprocally authenticated binding of both identity pins and both node keys;
there is no production API to create that binding until the reviewed handshake
exists. Local approval alone fails closed. Synthetic private bindings in tests
exercise the admission wrapper without claiming mutual authentication.

The adapter requires a nonzero approved WireGuard peer distinct from self and
explicit relay approval. It configures the exact-key admission hook, keeps the
PSK enabled, and limits any future listener to TCP port 48731 inside the tunnel. Forwarding,
UDP services, arbitrary subprocesses, and proxy handlers remain absent.

Every accepted stream is checked again using the authenticated peer key, and the
wrapper allows one live application stream. Revoke first rejects admission, then
closes pending accepts and the active stream, removes the connected peer, and
shuts down the transport. Reopening requires a new instance. Credentials and
bearer addresses reject JSON serialization and redact ordinary formatting.
This is not a guarantee of secure memory erasure.

Start currently fails before network I/O for the unresolved upstream privacy
control. There is no Tailcat Start/Listen call in the wrapper. This protects
against an accidental future call as well as the disabled desktop controls.
The application has not connected this adapter to live pairing or messaging.
The Ed25519 application identity and WireGuard peer key need a mutually
authenticated binding; invitation reservation alone cannot provide it. The
contact address must be exchanged inside that approved channel, never through a
frontend DTO or public lookup. Restart restoration needs reviewed encrypted
credential persistence. Bounded framing, acknowledgements and durable
deduplication exist in the chat domain but remain unconnected to this adapter.

Invitation endpoints are parsed into a typed, redacted Tailcat address before
reservation or eventual use. Parsing requires canonical bounded encoding,
nonzero node/discovery/PSK material and valid bounded relay metadata, and rejects
legacy/insecure/malformed forms. It performs no DNS resolution or dialing.
Syntactic validity does not authenticate a peer or grant consent to use its relay.

The adapter's tests use a synthetic authenticated-key lookup and `net.Pipe` to
check admission, concurrent revocation, stream limits, and closure. These are
wrapper tests. They do not prove wire authentication, bounded netstack resource
use against a malicious approved peer, direct connectivity, relay fallback,
network transitions, or independent-process behavior. Those remain integration
and release requirements.

## Services and notices

Application privacy policy additionally uses supported no-log/no-metric upstream
build tags and process controls; see [privacy controls](privacy.md). There is no
active remote uploader on the reviewed startup path, but pinned Tailcat advertises
an optional hardcoded DERP application name for statistics. Its public API cannot
clear that field. Resolve it through a reviewed immutable hook/patch before live
transport is connected to the app. Suppressing Logf is not itself an upload opt-out.

An empty relay setting is rejected rather than allowing Tailcat's public default
discovery service to be selected silently. A relay map must be an explicit
operator-provided HTTPS setting without URL credentials or fragments. No relay
is deployed by this change. Peer-provided embedded relay metadata still requires
an outbound policy review before a dialer is implemented.

Tailcat's
[stability terms](https://github.com/tailscale/tailcat/blob/e00cdde6ff787f801361ff85ed085775c775d4c5/README.md#stability)
offer no API/wire compatibility or public relay availability guarantee. Hosting
a separate relay would add hosting and operating costs and needs owner approval.

Tailcat is
[BSD-3-Clause](https://github.com/tailscale/tailcat/blob/e00cdde6ff787f801361ff85ed085775c775d4c5/LICENSE).
No upstream code was copied into this wrapper. Any future binary distribution
must reproduce required dependency notices, including Tailcat's full copyright,
conditions, and disclaimer, and inventory transitive licenses in its SBOM/license
report. No public license for WhatsUp itself has been selected.
