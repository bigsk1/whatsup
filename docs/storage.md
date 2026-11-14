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

Schema 3 contains a private identity section and bounded contact sections, each
with an exact local/peer identity pin and its own chat snapshot. A single
ApplicationState coordinator serializes every section's read-modify-durable-write
through the vault mutex. A chat adapter cannot replace the identity or another
contact's data. Raw Vault.Load/Save reject access while the coordinator owns the
vault. Separate handles for the same chat scope reject stale writes; different
contact scopes preserve each other's changes. Any uncertain durable save poisons
the coordinator until close/reopen, including errors after file replacement.

Existing canonical schema 1 and schema 2 snapshots migrate to schema 3 without
changing their seed, wrapping credential, pins or chat/outbox data. Unsupported,
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

Cancellation joins an in-flight save before explicit removal, preventing a late
save from resurrecting canceled setup. Remove itself is idempotent, without a
persistent revocation tombstone. A lost ACK or shutdown can leave a valid but
inert record; reopening presents incomplete setup with an explicit discard action,
never a resumed invitation, consent proof or active transport.

Migration is forward-only for retained 0.0.2 binaries: they reject schema 3.
Selecting an older executable alone is not a storage rollback. Current tests use
disposable vaults; installed review apps and the owner's existing state remain
unchanged. See [native migration guidance](native-testing.md).

Contact pins record local approval metadata. They cannot restore live contact
authority, transport credentials or reciprocal authentication. The desktop uses
the coordinator for identity storage; chat adapters are exercised by backend
integration tests and remain unbound to the visible messenger. Pending credentials
can be restored only as inert setup. Durable active completion, credential
reconciliation/revocation, attachments, encrypted search, backup/export
and rollback protection remain work.

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
