# Tailcat integration review

Updated 2026-11-14. This records source/API review, a narrowly pinned privacy
patch, wrapper tests and a disposable loopback Tailcat/pairing integration test.
It is not an independent cryptographic audit, native two-device QA, public relay
availability test or proof of strict direct-only behavior.

## Immutable dependency selection

The selected revision is
[`e00cdde6ff787f801361ff85ed085775c775d4c5`](https://github.com/tailscale/tailcat/commit/e00cdde6ff787f801361ff85ed085775c775d4c5),
committed 2026-11-05. Go resolves it as
`github.com/tailscale/tailcat v0.7.1-0.20261105151022-e00cdde6ff78`.
The latest published tag observed was
[`v0.7.0`](https://github.com/tailscale/tailcat/releases/tag/v0.7.0).
Normal builds use that immutable module version and the reviewed local replacement
at third_party/tailcat. config/tailcat-patch.json records its exact upstream
revision, copied file inventory, original/patched SHA-256 values and patch version.
The workflow verifies that inventory and permits only this exact replacement;
Go/module checksums and ordinary frozen dependency checks remain enabled.
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

## Invitation adapter and outbound policy

Explicit Create/Join actions require a user-selected HTTPS relay-map URL and
separate relay consent before any fetch or socket. An empty URL is rejected.
Declining consent does not select a weaker mode: strict direct-only is unavailable
and validation fails first. The default form is empty/unchecked and there is no
network start on app launch, unlock or dialog open.

FetchRelayProfile bounds the HTTPS response, preserves certificate verification,
rejects redirects/credentials/query/fragment additions and private destinations,
and does not use environment proxies. Resolved relay destinations are pinned in
the reviewed profile. Dialing validates an invitation against that profile rather
than trusting peer-supplied embedded relay metadata to add an outbound host.

The dedicated InvitationListener uses separate ephemeral node and PSK credentials,
limits candidate admission, accepts one stream and exposes only tunnel TCP port
48730. It authenticates WireGuard admission independently of the subsequent
Ed25519/TLS identity proof and bearer reservation. Only explicit approval may
provision ongoing contact credentials. Closing or expiry retires the invitation
transport, including its accepted stream; none of its private keys enter the vault.

Successful TLS Write does not prove that a userspace TCP stack transmitted its
last data or FIN. The authenticated terminal protocol therefore precedes
role-ordered retirement using Tailcat's supported DrainTCP operation, bounded by
a context deadline rather than a grace sleep. Failure to complete that handoff
or drain cannot issue a completion capability; records already committed remain
inert for reconciliation. Repeated success/denial teardown is part of the test
requirement, not a native reliability claim from one successful run.

## Ongoing contact adapter and exact promotion

`internal/transport.ContactServer` is backend infrastructure, not a Wails service.
Its construction performs no network activity. Pairing.Contact now records local
identity approval only; it no longer generates a separate session key or PSK.
The transport owns one opaque node-key/PSK bundle per local approval, with atomic
one-time preparation and shared-copy revocation. The same PSK is supplied to
Tailcat rather than generating an unrelated second secret.

Preparing credentials does not activate a contact. PromotePendingCredentials
requires the live reserved TLS completion, the exact durable CommitReceipt and
the finalized Contact. It adopts the two identity-bound node keys and shared PSK
from the agreed pending record; it never calls the legacy random factory to mint
replacement credentials. Restored records, local approval, copied booleans and
forged/stale receipts cannot create that private verified binding. Local approval
alone still fails closed.

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

ContactServer.Start still fails before network IO. The privacy patch is present,
but this ongoing adapter lacks its operator-profile/endpoint and restored-session
reconfirmation integration. Its retained ErrPrivacyUnavailable result is a closed
gate, not evidence that the invitation patch is missing. Invitation transport is
separate and can start after consent. No ongoing messaging tunnel is opened by
contact completion. Ongoing addresses contain a PSK and may never cross a frontend
DTO or public lookup. Bounded chat framing/acknowledgements/deduplication remain
unconnected to this adapter.

Invitation endpoints are parsed into a typed, redacted Tailcat address before
reservation or eventual use. Parsing requires canonical bounded encoding,
nonzero node/discovery/PSK material and valid bounded relay metadata, and rejects
legacy/insecure/malformed forms. It performs no DNS resolution or dialing.
Syntactic validity does not authenticate a peer or grant consent to use its relay.

The ongoing admission wrapper retains synthetic authenticated-key/net.Pipe tests
for revocation, stream limits and closure. Separately,
internal/transport/pairflow_integration_test.go uses a disposable loopback TLS
DERP fixture and actual Tailcat/WireGuard invitation connections, then runs real
pairing TLS, bearer/reservation ACK, two explicit approvals and schema-4 durable
completion through encrypted disposable vaults. It checks reciprocal exact
credentials and lifecycle behavior; its wrapping-key provider is disposable
memory, not the user's OS keyring.

This exercises the composed protocol on one host. It does not prove two native
apps/VMs, arbitrary NAT, all traffic traversing a forced relay, network transitions,
strict direct-only packet exclusion, or behavior against every hostile netstack
input. Those remain integration and release requirements. No public relay was
contacted by this network fixture.

## Services and notices

The copied Tailcat source has a narrow, reviewed privacy patch:
DisableDERPApplicationStats suppresses the optional DERP application-statistics
name on both invitation endpoints, and debugAddr is fixed false to prevent bearer
address logging through an inherited debug switch. It also identifies its patch
version so accidentally selecting unpatched source fails the build. The patch
does not change WireGuard cryptography or implement strict direct-only.

All eight supported omission tags and process controls remain mandatory: logtail,
netlog, client/user/runtime metrics, inherited proxies, router port mapping and
captive-portal probes are omitted. See [privacy controls](privacy.md). Muting Logf
alone would not provide those controls, and allowed network services still observe
IP/availability and ordinary connection metadata.

An empty relay setting is rejected rather than allowing Tailcat's public default
discovery service to be selected silently. A relay map must be an explicit
operator-provided HTTPS setting without URL credentials or fragments. No relay
is deployed by this change. Peer-provided embedded relay metadata is checked
against the chosen profile; it cannot silently change the selected service.
Upstream's documented `https://tailcat.dev/derpmap.json` is a manual operator-choice
example only, not a WhatsUp default or availability promise. It requires the same
metadata/relay consent.

Tailcat's
[stability terms](https://github.com/tailscale/tailcat/blob/e00cdde6ff787f801361ff85ed085775c775d4c5/README.md#stability)
offer no API/wire compatibility or public relay availability guarantee. Hosting
a separate relay would add hosting and operating costs and needs owner approval.

Tailcat is
[BSD-3-Clause](https://github.com/tailscale/tailcat/blob/e00cdde6ff787f801361ff85ed085775c775d4c5/LICENSE).
The reviewed subset in third_party/tailcat is copied upstream source, with the
BSD-3-Clause LICENSE retained and its changes inventoried. The Tailcat WebUI
reference remains separate: none of that project's code/styles/assets were
imported. Any future binary distribution
must reproduce required dependency notices, including Tailcat's full copyright,
conditions, and disclaimer, and inventory transitive licenses in its SBOM/license
report. No public license for WhatsUp itself has been selected.
