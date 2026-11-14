# Authenticated pairing protocol work

This records authenticated reservation, confirmation and encrypted pending setup.
The six desktop methods expose status, storage lock/unlock and Approve/Deny for
an already reserved request. No invitation creation/listener, dialer, relay
connection, active contact or live chat is enabled by this source.

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

The returned opaque `Approval` represents live reciprocal consent only. It has
no activation or network-start API, exposes no raw connection or generic peer
messaging operation, and is invalidated by session closure or expiry. Only the
narrow typed pending-credential exchange can run after reserved mutual consent.
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
contact or grant access. The future coordinator owns both lifecycles and must
close the session on denial, expiry, cancellation and shutdown.

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
can trigger its prompt.

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
on one side. This is not atomic distributed commitment or completed activation.
Records use the coordinated schema 3 envelope; see [storage](storage.md).

The coordinator closes TLS and retires the invitation on denial, expiry, error,
shutdown and after this pending-only exchange. A rejected concurrent candidate
cannot close the manager already reserved by another attempt. Cancellation waits
for its persistence callback before removing the local pending record. Storage
lock joins all runs/actions before closing the vault. Known bearer copies are
dropped from retained pending state; Go-managed copies cannot be guaranteed erased.

Restored pending records cannot revive an invitation, consent proof, Contact or
tunnel. They display a nonmodal incomplete-setup notice with a fresh ephemeral
request ID and a Discard action. They block a replacement attempt until explicitly
discarded. The coordinator is deliberately pending-only: the future completion
step belongs before final invitation retirement in a fully integrated run, with
separate reviewed recovery for an already interrupted pending exchange.

## Confirmation UI

The backend publishes fingerprints only after reservation. The modal shows both
full fingerprints, asks for comparison over a trusted channel, and offers Approve
or Deny. Deny has initial focus, and Escape denies. Approval records the exact
current request ID and fingerprint, returns promptly, and waits for the other
person; stale responses cannot approve or restore another request. Browser storage
contains no pairing state. Polling is bounded/serialized and stops on storage lock
or unmount. Pending setup is always labeled inactive.

No sample trigger or unauthenticated fingerprint preview is added. Initiate-pairing
controls remain disabled until the actual invitation transport is implemented and
its privacy gate is resolved. Browser tests use synthetic bridge responses; they
do not demonstrate a native peer reaching the popup.

## Required next integration

The coordinator now covers reservation, reciprocal consent and pending writes.
The remaining completion integration must run without network IO under invitation,
contact or vault mutexes:

1. Resolve invitation transport privacy/outbound relay controls before enabling
   native creation, link handling or dialing; keep strict direct-only unavailable.
2. Add authenticated durable-completion evidence and crash reconciliation before
   granting an active capability. Two devices cannot be made durably atomic with
   a local file rename, and a saved pending record alone is insufficient.
3. Persist completed activation, retire the invitation listener/credentials, and
   permit only the exact approved node for that conversation. Restore must reject
   malformed, incomplete, mismatched or revoked records. Multiple separate contact
   pins never mean multiple members of one conversation.

Promote the exact agreed pending key/PSK bundle when that finalization is added.
Do not call the older local-only credential generator to mint unrelated replacement
keys during activation.

No production constructor for the adapter's private `verifiedBinding` is added
by this slice. Tailcat's statistics-label control, outbound relay policy,
packet-boundary direct-only enforcement, and real two-device delivery tests remain
separate requirements. The adapter still fails before network startup.

## Evidence limits

Tests exercise real TLS handshakes, bearer acknowledgements, reciprocal consent,
pending credential exchange and actual encrypted vaults over loopback TCP. They
use synthetic identities and invitations. The UI tests exercise the real Svelte
app and generated bridge with synthetic status responses. These are component
tests, not Tailcat, WireGuard, relay, native pairing or two-machine messenger
verification. The
normal `npm run test:app` command includes these packages; its known advisory gate
remains independent. The installed 0.0.2 review executables are unchanged by this
backend work, and no app-version increment is needed until a new test revision
is delivered.
