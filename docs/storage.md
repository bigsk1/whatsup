# Local state implemented in this iteration

The application snapshot uses the Go standard library's AES-256-GCM with a
fresh random 96-bit nonce per save. A versioned header is authenticated as AAD.
The full ciphertext must authenticate before any plaintext is returned. Reads and
writes are bounded to 4 MiB of plaintext; truncated, oversized, modified and
wrong-key snapshots fail. This is an application storage envelope using standard
cryptography, not an independent cryptographic protocol or an audited format.

The 256-bit wrapping key is generated with crypto/rand and stored through
github.com/zalando/go-keyring: Windows Credential Manager or Linux Secret Service.
There is no plaintext file or environment fallback. If a vault exists and its
credential is missing, unlock fails without replacing the key or state. New
credentials are read back before use. Linux needs a working, unlocked desktop
Secret Service session. Installing GTK/WebKit does not supply one in a headless WSL
session. Windows state is under the user's LOCALAPPDATA/WhatsUp; Linux uses
XDG_DATA_HOME/whatsup or ~/.local/share/whatsup. No state is placed beside the binary.

An OS file lock excludes a second writer, including another application process.
Unix directories/files must be private. Symlink state/lock files and the final
symlink directory are rejected. The directory's parent and the user's local OS
account remain trusted; this is not protection against malicious local processes
running as the same user. Windows permissions inherit from the user's local app
data directory; Go's Unix mode bits are not a Windows ACL sandbox.

Only ciphertext reaches a temporary file. Save flushes that file before rename;
Unix additionally syncs the directory. A failed save is not an acknowledgement.
Go does not guarantee rename atomicity on every non-Unix filesystem, and native
power-loss/crash QA remains outstanding. Orphan temporary files contain ciphertext
and are not read as current state. The wrapping key and decoded buffers are cleared
where practical, but Go-managed copies and OS paging prevent guaranteed memory
erasure. Locking does not undo data already exposed to a compromised endpoint.

Schema 4 contains a private identity section and bounded contact sections, each
with an exact local/peer identity pin and its own chat snapshot. A single
ApplicationState coordinator serializes every section's read-modify-durable-write
through the vault mutex. A chat adapter cannot replace the identity or another
contact's data. Raw Vault.Load/Save reject access while the coordinator owns the
vault. Separate handles for the same chat scope reject stale writes; different
contact scopes preserve each other's changes. Any uncertain durable save poisons
the coordinator until close/reopen, including errors after file replacement.

Existing canonical schema 1, 2 and 3 snapshots migrate to schema 4 without
changing their seed, wrapping credential, pins, pending records or chat/outbox data. Unsupported,
malformed or noncanonical schemas are preserved
and rejected. The total plaintext bound remains 4 MiB, with at most 32 contact
sections and bounded per-contact chat/history. Only the identity's public
fingerprint crosses the Wails bridge after explicit unlock; reserved pairing
status additionally exposes the two public fingerprints and an ephemeral request ID.

The separate pendingContacts section holds at most 32 inert records. Each binds
the two identities, distinct node public keys, the local node private key, one
shared random PSK and a contact ID. Canonical validation checks the local
private/public association and vault identity; conflicting IDs or another pending
bundle for the same peer are refused. Exact retries are idempotent. These records
cannot implement ApprovedContact, create a contact pin or obtain a chat scope.
Ordinary JSON serialization/formatting is blocked or redacted; a private storage
alias alone is encoded inside the encrypted envelope.

Cancellation joins an in-flight pending save before explicit removal, preventing
a late save from resurrecting canceled setup. Remove itself is idempotent,
without a persistent revocation tombstone. A lost ACK or shutdown can leave a
valid but inert pending record; reopening presents incomplete setup with an
explicit discard action, never a resumed invitation or consent proof.

The committedContacts section stores at most 32 exact agreed records. Committing
moves the validated pending bundle without minting keys or a replacement PSK.
Conflicting IDs/peers are rejected; an existing legacy contact/chat scope for the
peer cannot be inherited by a new pairing. Pending and committed sections cannot
hold competing entries for one ID or peer. No chat scope is created by commitment.

An in-memory CommitReceipt proves a successful write through this open
ApplicationState. Its ownership, generation, exact record and one-shot consumption
are checked alongside the live TLS completion before runtime authority is issued.
The receipt is not serialized and cannot be recovered from a committed JSON flag.
Restored committed records are inert and appear as reconnect-required, with an
explicit Discard action. Fresh peer reconfirmation and ongoing-session restoration
remain unimplemented; unlock never reconnects them automatically.

CancelAfterRevocation is the cancellation path when a receipt may already have
issued runtime authority. It first claims the receipt under the vault guard,
rejecting later use, then invokes the trusted runtime revoke/drain callback outside
the vault mutex. Only after that callback returns does it delete the exact record,
including when the receipt was consumed. Stale/forged receipts cannot invoke the
callback or remove another record. A failed or uncertain deletion leaves runtime
stopped and state preserved/poisoned for reopen, rather than reporting success.

Migration is forward-only for retained 0.0.2 binaries: they reject schema 4.
Selecting an older executable alone is not a storage rollback. Current tests use
disposable vaults; installed review apps and the owner's existing state remain
unchanged until an explicit reviewed upgrade. The next private pairing candidate
is planned as 0.0.3; its native owner QA is not recorded by these source tests.
See [native migration guidance](native-testing.md).

Contact pins record local approval metadata. They cannot restore live contact
authority, transport credentials or reciprocal authentication. The desktop uses
the coordinator for identity storage; chat adapters are exercised by backend
integration tests and remain unbound to the visible messenger. Pending and
committed credentials can be restored only as inert setup. First-run durable
completion can issue runtime Contact/credential capabilities after reciprocal
evidence and invitation retirement, but it does not start an ongoing tunnel.
Fresh restored-session reconfirmation, recovery after interrupted distributed
completion, attachments, encrypted search, backup/export and rollback protection
remain work. A local rename cannot make two devices durably atomic.

Tests cover ciphertext-only persistence, fresh nonces, tampering/truncation,
wrong/missing credentials, reopen, size limits, writer exclusion and lock closure.
Independent Go test subprocesses check process-level locking and restoration of
the same synthetic identity and scoped chat/outbox state. Schema migration,
cross-scope preservation, concurrency, stale writes and failures before/after
durable replacement have dedicated tests. These tests inject disposable providers
and do not establish availability of a real user's OS credential service.

Run npm run test:credentials in the target desktop user's session to check the
actual provider. It creates one random disposable account in a separate validation
service, reopens an encrypted vault, deletes that credential, and confirms that
the existing vault fails closed without minting a replacement. It never opens or
replaces the production identity credential. This command is mandatory in the
eventual full check/package gate; component-only tests do not silently claim it
passed. Windows Credential Manager and Ubuntu Desktop Secret Service validation
pass; details are recorded in docs/status.md. Headless WSL without Secret Service is
expected to reject unlock.

An encrypted snapshot can be replayed by a local backup/OS owner who controls its
files. This format makes no promise of backup forward secrecy, rollback prevention,
hardware isolation, or security certification. Backup/restore is not exposed.
