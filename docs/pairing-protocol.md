# Authenticated pairing protocol work

This records explicit invitation transport, authenticated reservation, confirmation
and durable contact completion. Nine typed desktop methods expose status/storage,
CreateInvitation, JoinInvitation, CancelInvitation and current-request Approve/Deny.
Invitation networking requires a chosen HTTPS operator URL and separate explicit
relay consent. Ongoing contact transport, chat and files remain unavailable.

## Implemented channel

`internal/pairing/channel` owns an already supplied backend `net.Conn`. It never
listens or dials. Go's standard TLS 1.3 performs ephemeral key agreement,
encryption and mutual proof of the existing Ed25519 identity keys. No new
cryptography dependency or Ed25519-to-WireGuard key conversion is introduced.

The joiner pins the invitation owner's complete public identity. The inviter
authenticates the candidate's certificate/key possession before pending approval,
but that proof does not establish who the person is or grant consent. Both users
must verify the full peer fingerprint through a trusted channel.

Certificates are short-lived, self-signed Ed25519 leaves with a fixed profile.
Both endpoints run mandatory custom verification of that profile, signature,
validity, key usage, and identities. On the client, Go's `InsecureSkipVerify`
replaces CA/hostname trust with that mandatory verification: a successful
connection never bypasses the exact owner-key check. A bare flag without this
callback would be unsafe. This follows Go's documented custom-verification API.
See [Go TLS configuration](https://pkg.go.dev/crypto/tls#Config).

Only TLS 1.3 is accepted. Session tickets and client caching are disabled;
resumed sessions are rejected. The protocol/version and exact invitation ID are
bound into ALPN. Session lifetime is limited to the invitation's expiry, at most
10 minutes, with a separate maximum 15-second handshake and caller cancellation.
Rejected certificates have a 2 KiB application profile limit; Go's own handshake
parsing limits apply before that profile check. Raw handshake errors and peer
certificates are not logged or returned to the frontend.

Reciprocal consent uses fixed 42-byte decision frames, with a version
marker, explicit decision, opposite role, and the standard 32-byte TLS exporter
channel binding. This binds a decision to the actual authenticated TLS session;
an approval from another connection cannot substitute for it. The exporter uses
the label and empty context specified by
[RFC 9266](https://www.rfc-editor.org/rfc/rfc9266.html#section-2).
Duplicate decisions, invalid framing, denial, timeout, cancellation and closure
produce no approval capability. Session/proof value copies share their lifecycle,
and formatting/JSON handling cannot expose private state.

The reserved workflow starts its peer reader before the local user clicks. It
requires both explicit votes and a session-bound READY handoff before provisioning
can read the connection. Denial or disconnect therefore dismisses a peer's
unanswered prompt, including after one person has already approved. A premature
READY cannot substitute for a local vote. The older direct Session.Approve API
remains a lower-level component path, not the coordinator's user-facing workflow.

The returned opaque `Approval` represents live reciprocal consent only. It
exposes no raw connection or generic peer messaging operation and is invalidated
by session closure or expiry. Narrow typed credential/completion operations still
need the exact reserved session and successful durable callbacks; approval alone
cannot create a runtime Contact or start networking.
Its validity is not an online/presence indicator or evidence that the other
device has durably activated a contact.

## Invitation integration

`Manager.ReserveAuthenticated` accepts the channel's authenticated candidate and
requires the inviter role, local owner, exact invitation ID and expiry. It still
parses and compares the actual invitation bearer secret and atomically admits
only one candidate. A channel for another invitation with the same owner and
expiry is rejected. TLS authentication does not replace bearer validation.

The channel's pending check completes before the manager lock is taken, so an
invitation retirement hook can close its transport without a lock cycle. A close
between those checks can only burn a pending invitation; it cannot activate a
contact or grant access. The coordinator owns both lifecycles and closes the
session on denial, expiry, cancellation and shutdown.

The older `Reserve`/`Approve` domain path remains for existing component tests and
independently authenticated adapter experiments. It must not be used as native
network pairing. `Approve` explicitly rejects an authenticated reservation with
the unfinished activation gate, including after reciprocal consent. This avoids
its legacy immediate invitation teardown and prevents either approval path from
silently substituting for durable activation.

`RequestReservation` transmits the bounded bearer inside TLS. `AcceptReservation`
calls that exact manager validation before returning its authenticated acceptance
ACK. The joiner waits for this ACK before showing a fingerprint too. The
`pairflow.Coordinator` uses this path exclusively; no successful handshake alone
can trigger its prompt. Authenticated completion uses FinalizeAuthenticated,
never the legacy immediate-consumption approval path.

## Explicit creation, joining and privacy

The native dialog starts with an empty service URL and unchecked relay consent.
Only an explicit Create/Join action after both choices can fetch the selected
HTTPS relay map or open invitation transport. False consent is rejected before
network IO; it never silently changes strict direct-only into relay fallback.
The operator observes IP/availability and can relay encrypted pairing setup.

The reviewed Tailcat subset suppresses optional DERP application statistics and
bearer debug logging. Eight supported omission tags also remove log/metric upload,
inherited proxies, router mapping and captive-portal probes. The map fetch is
bounded, retains TLS verification, refuses redirects/private destinations and
pins allowed relay destinations. Peer-supplied metadata cannot add a different
service. See [transport review](transport-review.md) and [privacy](privacy.md).

Upstream's documented `https://tailcat.dev/derpmap.json` is a manual example only;
it is not preselected or contacted automatically and has no availability promise.
The invitation service uses dedicated ephemeral WireGuard node/PSK credentials
and tunnel TCP port 48730. These are separate from ongoing contact credentials.

Create returns the bearer link solely for explicit session-only sharing. Copy is
manual, paste does not navigate a browser, and no OS URI registration or QR flow
is implemented. Handoff, cancellation, expiry, storage lock and unmount clear the
UI link. Clipboard contents remain until the user replaces them. Ordinary status,
preferences and diagnostics never contain a bearer or ongoing transport secret.

## Pending credentials and lifecycle

After mutual consent, transport creates one local node key per side. Only the
inviter generates the ongoing contact ID and PSK. The typed exchange binds both
node public keys, the two authenticated identities and the shared PSK to the
reserved session. It never reuses invitation transport keys or generates an
independent joiner PSK.

The inviter persists its encrypted pending record before sending the agreed
proposal. The joiner persists before sending its prepared ACK; the inviter sends
the final prepared ACK after receiving that confirmation. A successful exchange
means both pending writes were acknowledged. A lost ACK can leave an inert record
on one side. These prepared ACKs alone are not completed activation.
Records use the coordinated schema 4 envelope; see [storage](storage.md).

Durable completion moves each exact bundle from pendingContacts to
committedContacts before announcing local COMMITTED on that same reserved TLS
channel. Each side verifies the peer's matching identity/node/PSK association.
The live Completion plus an exact one-shot CommitReceipt is required to finalize
Contact authority and promote the existing pending credentials. No replacement
keys or PSK are minted. A role-ordered authenticated terminal receipt confirms
the final commit before invitation teardown. Tailcat's supported DrainTCP
operation then bounds retirement of queued TCP data/FIN/ACKs with a deadline;
successful TLS Write alone is insufficient. Failure leaves no completion
capability. Invitation retirement and runtime publication run outside the
manager/channel/vault locks.
This remains a distributed protocol: a dropped final frame or interrupted
retirement can leave one side committed but inert, and no local rename can make
two devices durably atomic. Terminal handoff and bounded transport teardown must
pass their failure-path tests before native reliability is claimed.

The coordinator closes TLS and retires the invitation on denial, expiry, error
and shutdown as well. A rejected concurrent candidate
cannot close the manager already reserved by another attempt. Cancellation waits
for its persistence callback before removing the local pending record. Storage
lock joins all runs/actions before closing the vault. Known bearer copies are
dropped from retained pending state; Go-managed copies cannot be guaranteed erased.

If a durable receipt has already issued runtime authority, CancelAfterRevocation
claims it against further use, invokes runtime revoke/drain outside the vault
mutex, and only then removes the exact committed record. This includes consumed
receipts. Uncertain deletion leaves runtime stopped and state preserved/poisoned
for reopen; it is never reported as a successful rollback.

Restored pending records cannot revive an invitation, consent proof, Contact or
tunnel. They display a nonmodal incomplete-setup notice with a fresh ephemeral
request ID and a Discard action. Restored committed records similarly remain
inert, labeled reconnect-required, and cannot reconstruct live completion or a
CommitReceipt. Both block replacement until explicitly discarded. A future
ongoing-session reconfirmation/recovery protocol is required to reuse them safely.

## Confirmation UI

The backend publishes fingerprints only after reservation. The modal shows both
full fingerprints, asks for comparison over a trusted channel, and offers Approve
or Deny. Deny has initial focus, and Escape denies. Approval records the exact
current request ID and fingerprint, returns promptly, and waits for the other
person; stale responses cannot approve or restore another request. Browser storage
contains no pairing state. Polling is bounded/serialized and stops on storage lock
or unmount. Pending setup is labeled incomplete; completed first setup says Contact
approved and messaging not ready. Restored committed setup offers inert Discard,
not automatic approval or a connection indicator.

No sample trigger or unauthenticated fingerprint preview is added. Native
Create/Join require unlocked storage and the explicit operator/consent choices
above. Browser tests use synthetic bridge responses; they do not demonstrate a
native peer reaching the popup.

## Required next integration

1. Complete owner-driven two-device native pairing, cancellation, expiry and
   restart QA for the new private candidate, including migration/recovery planning.
2. Add ongoing endpoint/profile exchange and fresh peer reconfirmation for restored
   committed contacts before allowing ContactServer.Start. A disk record or a
   successful initial pairing must never silently resume a tunnel.
3. Implement reviewed packet-boundary strict direct-only enforcement and prove
   zero relayed payload under path changes/retransmission; it remains unavailable.
4. Connect the encrypted chat queue/ACK adapters to authenticated ongoing sessions,
   then implement files and the remaining release gates.

Runtime Contact/credentials now have a production constructor tied to live
completion and exact durable evidence. The ongoing server still fails before
network startup because its endpoint/reconfirmation integration is unfinished.
Invitation networking is a separate, explicit-consent service. Multiple separate
contacts never mean more than two identities in one conversation.

## Evidence limits

Tests exercise real TLS, reservation ACKs, consent, encrypted pending writes,
durable completion, exact credential promotion and cancellation. The external
transport integration fixture additionally runs real Tailcat/WireGuard over a
disposable loopback TLS DERP service, with two independent encrypted vaults and
explicit decisions. Its wrapping keys are disposable test providers, and it makes
no public service connection. No forced payload path or two-native-device result
is inferred from it.

The 32 browser tests exercise the real Svelte app/generated bridge with synthetic
HTTP responses. They are separate from protocol tests and native owner QA. The
normal `npm run test:app` command includes these packages; its known advisory gate
remains independent. The planned private native candidate is 0.0.3, with no
owner/native pairing pass recorded here. Installed 0.0.2 executables remain
unchanged until an explicit reviewed launch/upgrade. Schema-4 migration prevents
an executable-only rollback to those older binaries.
