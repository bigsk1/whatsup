package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/identity"
)

func commitForTest(t *testing.T, app *ApplicationState, fill byte) (PendingRecord, *CommitReceipt) {
	t.Helper()
	record := pendingFor(t, app, fill)
	if err := app.SavePendingContact(record); err != nil {
		t.Fatal(err)
	}
	receipt, err := app.CommitPendingContact(record)
	if err != nil {
		t.Fatal(err)
	}
	return record, receipt
}

func TestCommittedRecordsAreEncryptedExactAndCannotRestoreReceipts(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, directory, keys)
	record, receipt := commitForTest(t, app, 151)
	if records, _ := app.PendingContacts(); len(records) != 0 {
		t.Fatal("committed bundle remained pending")
	}
	committed, err := app.CommittedContacts()
	if err != nil || len(committed) != 1 || committed[0] != record {
		t.Fatal("exact bundle was not durably committed")
	}
	if len(app.state.Contacts) != 0 {
		t.Fatal("durable record alone created a history pin")
	}
	if _, err := app.ChatStore(&approvedPin{local: record.Local, peer: record.Peer}); !errors.Is(err, ErrContact) {
		t.Fatal("committed record granted history authority")
	}
	before, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
	for _, secret := range []string{record.PresharedKey, record.LocalNodePrivate, "committedContacts"} {
		if bytes.Contains(before, []byte(secret)) {
			t.Fatal("committed credential persisted as plaintext")
		}
	}
	again, err := app.CommitPendingContact(record)
	if err != nil || again.state != receipt.state {
		t.Fatal("identical retry did not share one-shot receipt")
	}
	after, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
	if !bytes.Equal(before, after) {
		t.Fatal("idempotent commit retry rewrote state")
	}
	got, err := receipt.RecordForActivation()
	if err != nil || got != record {
		t.Fatal("receipt facts did not match exact committed bundle")
	}
	changed := record
	changed.PresharedKey = pendingFor(t, app, 152).PresharedKey
	if receipt.ValidateRecord(changed) == nil || receipt.ConsumeForActivation(changed) == nil {
		t.Fatal("receipt accepted caller substituted credentials")
	}
	if err := receipt.ValidateRecord(record); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{*receipt, receipt, CommitReceipt{}} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if fmt.Sprintf(format, value) != "[private WhatsUp durable contact receipt]" {
				t.Fatal("receipt formatting leaked fields")
			}
		}
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("durable receipt serialized")
		}
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(receipt.ValidateRecord(record), ErrLocked) {
		t.Fatal("closed coordinator retained durable authority")
	}
	_, app = openApplicationTest(t, directory, keys)
	loaded, err := app.CommittedContacts()
	if err != nil || len(loaded) != 1 || loaded[0] != record {
		t.Fatal("encrypted committed record did not reopen")
	}
	if renewed, err := app.CommitPendingContact(record); renewed != nil || !errors.Is(err, ErrCommittedRestore) {
		t.Fatal("restored record minted a live receipt without fresh authentication")
	}
	if err := app.RemoveCommittedContact(record.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.RemoveCommittedContact(record.ID); err != nil {
		t.Fatal("inert discard was not idempotent")
	}
	if loaded, _ := app.CommittedContacts(); len(loaded) != 0 {
		t.Fatal("explicit inert discard failed")
	}
}

func TestCommitReceiptCopiesConsumeOnceAndRemovalInvalidatesGenerations(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record, first := commitForTest(t, app, 153)
	stale := *first
	if err := app.RemoveCommittedContact(record.ID); err != nil {
		t.Fatal(err)
	}
	if err := stale.ValidateRecord(record); err == nil {
		t.Fatal("discarded receipt stayed valid")
	}
	if err := app.SavePendingContact(record); err != nil {
		t.Fatal(err)
	}
	second, err := app.CommitPendingContact(record)
	if err != nil || second.state == first.state || second.state.generation == first.state.generation {
		t.Fatal("recommit reused old receipt generation")
	}
	if err := stale.ConsumeForActivation(record); err == nil {
		t.Fatal("old receipt claimed replacement record")
	}
	copyReceipt := *second
	if err := second.ConsumeForActivation(record); err != nil {
		t.Fatal(err)
	}
	if err := copyReceipt.ConsumeForActivation(record); !errors.Is(err, ErrCommittedUsed) {
		t.Fatal("copied receipt consumed twice")
	}
	if _, err := app.CommitPendingContact(record); !errors.Is(err, ErrCommittedUsed) {
		t.Fatal("commit retry reminted a consumed receipt")
	}
	if err := app.RemoveCommittedContact(record.ID); !errors.Is(err, ErrCommittedUsed) {
		t.Fatal("inert discard deleted a record with issued live authority")
	}
}

func TestNewActivationCannotInheritLegacyHistory(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record := pendingFor(t, app, 154)
	pin := &approvedPin{local: record.Local, peer: record.Peer}
	if err := app.PinApprovedContact(pin); err != nil {
		t.Fatal(err)
	}
	chat, err := app.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	payload := queuedChat(t, pin, "synthetic history belonging to prior approval")
	if err := chat.Save(payload); err != nil {
		t.Fatal(err)
	}
	if err := app.SavePendingContact(record); err != nil {
		t.Fatal(err)
	}
	if receipt, err := app.CommitPendingContact(record); receipt != nil || !errors.Is(err, ErrCommittedConflict) {
		t.Fatal("new activation inherited an existing peer's chat scope")
	}
	if !bytes.Equal(readChat(t, app, pin), payload) {
		t.Fatal("rejected activation changed old history")
	}
	if records, _ := app.PendingContacts(); len(records) != 1 || records[0] != record {
		t.Fatal("conflicting activation discarded recoverable pending state")
	}
}

func TestCommitRejectsMissingSubstitutedAndDuplicateRecords(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record := pendingFor(t, app, 155)
	if proof, err := app.CommitPendingContact(record); proof != nil || !errors.Is(err, ErrCommitted) {
		t.Fatal("unprepared record was committed")
	}
	if err := app.SavePendingContact(record); err != nil {
		t.Fatal(err)
	}
	changed := record
	changed.LocalNodePrivate = pendingFor(t, app, 156).LocalNodePrivate
	if proof, err := app.CommitPendingContact(changed); proof != nil || err == nil {
		t.Fatal("substituted private key was committed")
	}
	changed = record
	changed.PresharedKey = pendingFor(t, app, 156).PresharedKey
	if proof, err := app.CommitPendingContact(changed); proof != nil || !errors.Is(err, ErrCommittedConflict) {
		t.Fatal("different valid bundle replaced pending record")
	}
	if _, err := app.CommitPendingContact(record); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(app.SavePendingContact(record), ErrPendingConflict) {
		t.Fatal("committed ID was also accepted as pending")
	}
	changed = pendingFor(t, app, 157)
	changed.Peer = record.Peer
	if !errors.Is(app.SavePendingContact(changed), ErrPendingConflict) {
		t.Fatal("another pending bundle replaced committed peer")
	}
	if (&CommitReceipt{}).ValidateRecord(record) == nil || (&CommitReceipt{}).ConsumeForActivation(record) == nil {
		t.Fatal("zero receipt manufactured authority")
	}
}

func TestSchemaThreeMigrationPreservesSeedKeyPinsChatAndPending(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, directory, keys)
	local, _ := app.Identity()
	seed := local.Seed()
	pin := pinFor(t, app, 158)
	if err := app.PinApprovedContact(pin); err != nil {
		t.Fatal(err)
	}
	chat, _ := app.ChatStore(pin)
	payload := queuedChat(t, pin, "synthetic schema three history")
	if err := chat.Save(payload); err != nil {
		t.Fatal(err)
	}
	record := pendingFor(t, app, 159)
	if err := app.SavePendingContact(record); err != nil {
		t.Fatal(err)
	}
	old := legacySnapshotForTest(app.state)
	legacy, err := json.Marshal(applicationEnvelopeV3{Version: 3, Identity: old.Identity, Contacts: old.Contacts, PendingContacts: old.PendingContacts})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	vault := openTest(t, directory, keys)
	if err := vault.Save(legacy); err != nil {
		t.Fatal(err)
	}
	app, err = OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := app.Identity()
	pending, _ := app.PendingContacts()
	committed, _ := app.CommittedContacts()
	if app.state.Version != ApplicationSchemaVersion || restored.Seed() != seed || keys.writes != 1 || len(pending) != 1 || pending[0] != record || committed == nil || len(committed) != 0 || !bytes.Equal(readChat(t, app, pin), payload) {
		t.Fatal("schema three migration replaced existing state")
	}
}

func TestUncertainCommitPoisonsAllReceiptsAndRestoreStaysInert(t *testing.T) {
	for _, afterReplace := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[afterReplace], func(t *testing.T) {
			directory, keys := privateDirectory(t), &memoryKeys{}
			_, app := openApplicationTest(t, directory, keys)
			old, oldReceipt := commitForTest(t, app, 160)
			record := pendingFor(t, app, 161)
			if err := app.SavePendingContact(record); err != nil {
				t.Fatal(err)
			}
			original := app.save
			app.save = func(payload []byte) error {
				if afterReplace {
					if err := original(payload); err != nil {
						return err
					}
				}
				return errors.New("synthetic uncertain durable replacement")
			}
			if receipt, err := app.CommitPendingContact(record); receipt != nil || !errors.Is(err, ErrStateFailed) {
				t.Fatal("uncertain commit returned receipt")
			}
			if !errors.Is(oldReceipt.ValidateRecord(old), ErrStateFailed) {
				t.Fatal("uncertain write left another live receipt usable")
			}
			if _, err := app.CommitPendingContact(record); !errors.Is(err, ErrStateFailed) {
				t.Fatal("poisoned coordinator allowed retry")
			}
			_ = app.Close()
			_, app = openApplicationTest(t, directory, keys)
			committed, _ := app.CommittedContacts()
			wanted := 1
			if afterReplace {
				wanted = 2
			}
			if len(committed) != wanted {
				t.Fatal("reopen did not reflect actual durable replacement")
			}
			if receipt, err := app.CommitPendingContact(old); receipt != nil || !errors.Is(err, ErrCommittedRestore) {
				t.Fatal("restore revived old live receipt")
			}
			if afterReplace {
				if receipt, err := app.CommitPendingContact(record); receipt != nil || !errors.Is(err, ErrCommittedRestore) {
					t.Fatal("uncertain restored record minted authority")
				}
			}
		})
	}
}

func TestMalformedCommittedSectionsArePreservedAndRejected(t *testing.T) {
	for _, kind := range []string{"nil", "duplicate", "wrong-key", "cross-pending", "foreign-local"} {
		t.Run(kind, func(t *testing.T) {
			directory, keys := privateDirectory(t), &memoryKeys{}
			_, app := openApplicationTest(t, directory, keys)
			record, _ := commitForTest(t, app, 162)
			state := app.cloneLocked()
			switch kind {
			case "nil":
				state.CommittedContacts = nil
			case "duplicate":
				state.CommittedContacts = append(state.CommittedContacts, pendingRecordDisk(record))
			case "wrong-key":
				state.CommittedContacts[0].LocalNode = pendingFor(t, app, 163).LocalNode
			case "cross-pending":
				state.PendingContacts = append(state.PendingContacts, pendingRecordDisk(record))
			case "foreign-local":
				other, _ := identity.Generate()
				state.CommittedContacts[0].Local = other.PublicKey()
			}
			payload, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			_ = app.Close()
			vault := openTest(t, directory, keys)
			if err := vault.Save(payload); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
			if _, err := OpenApplication(vault); !errors.Is(err, ErrState) {
				t.Fatal("malformed committed envelope accepted")
			}
			after, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
			if !bytes.Equal(before, after) {
				t.Fatal("malformed committed state was replaced")
			}
		})
	}
}

func TestCancelAfterRevocationStopsConsumedAuthorityBeforeDeleting(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record, receipt := commitForTest(t, app, 171)
	copyReceipt := *receipt
	if err := receipt.ConsumeForActivation(record); err != nil {
		t.Fatal(err)
	}
	var stopped atomic.Bool
	if err := receipt.CancelAfterRevocation(func() {
		// Callback can inspect storage because no vault lock is held.
		stored, err := app.CommittedContacts()
		if err != nil || len(stored) != 1 {
			t.Fatal("record was deleted before runtime revocation")
		}
		if !errors.Is(copyReceipt.ValidateRecord(record), ErrCommittedUsed) {
			t.Fatal("canceling receipt still granted authority")
		}
		stopped.Store(true)
	}); err != nil {
		t.Fatal(err)
	}
	if !stopped.Load() {
		t.Fatal("runtime revocation callback did not run")
	}
	if records, _ := app.CommittedContacts(); len(records) != 0 {
		t.Fatal("revoked record was not durably removed")
	}
	if receipt.ValidateRecord(record) == nil || copyReceipt.ConsumeForActivation(record) == nil {
		t.Fatal("revoked receipt remained usable")
	}
	if err := copyReceipt.CancelAfterRevocation(func() { t.Fatal("stale receipt invoked runtime callback") }); err == nil {
		t.Fatal("revocation repeated with stale receipt")
	}
}

func TestCancellationClaimBlocksConcurrentConsumeAndRevoke(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record, receipt := commitForTest(t, app, 172)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() { done <- receipt.CancelAfterRevocation(func() { close(entered); <-release }) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("revocation callback did not start")
	}
	if !errors.Is(receipt.ConsumeForActivation(record), ErrCommittedUsed) || !errors.Is(receipt.ValidateRecord(record), ErrCommittedUsed) {
		t.Fatal("authority was issued after cancellation began")
	}
	if _, err := app.CommitPendingContact(record); !errors.Is(err, ErrCommittedUsed) {
		t.Fatal("retry returned a canceling receipt")
	}
	if !errors.Is(app.RemoveCommittedContact(record.ID), ErrCommittedUsed) {
		t.Fatal("generic removal raced explicit revocation")
	}
	if err := receipt.CancelAfterRevocation(func() { t.Fatal("second revocation callback ran") }); !errors.Is(err, ErrCommittedUsed) {
		t.Fatal("concurrent cancellation was admitted")
	}
	// Another independent persistence operation can finish while revoke blocks.
	other := pendingFor(t, app, 173)
	if err := app.SavePendingContact(other); err != nil {
		t.Fatal("vault mutex was held during runtime drain")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not finish")
	}
	if records, _ := app.CommittedContacts(); len(records) != 0 {
		t.Fatal("canceled record survived")
	}
	if pending, _ := app.PendingContacts(); len(pending) != 1 || pending[0] != other {
		t.Fatal("revocation removed another pending scope")
	}
}

func TestRevocationRefusesInvalidReceiptsBeforeCallback(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record, receipt := commitForTest(t, app, 174)
	if err := receipt.CancelAfterRevocation(nil); !errors.Is(err, ErrCommitted) {
		t.Fatal("nil revocation callback accepted")
	}
	if err := receipt.ValidateRecord(record); err != nil {
		t.Fatal("invalid cancellation consumed receipt")
	}
	if err := (&CommitReceipt{}).CancelAfterRevocation(func() { t.Fatal("forged receipt invoked callback") }); err == nil {
		t.Fatal("forged receipt canceled")
	}
	if err := app.RemoveCommittedContact(record.ID); err != nil {
		t.Fatal(err)
	}
	if err := receipt.CancelAfterRevocation(func() { t.Fatal("removed receipt invoked callback") }); err == nil {
		t.Fatal("removed receipt canceled again")
	}
}

func TestUncertainRevocationSavePoisonsStateAfterRuntimeStops(t *testing.T) {
	for _, afterReplace := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[afterReplace], func(t *testing.T) {
			directory, keys := privateDirectory(t), &memoryKeys{}
			_, app := openApplicationTest(t, directory, keys)
			record, receipt := commitForTest(t, app, 175)
			if err := receipt.ConsumeForActivation(record); err != nil {
				t.Fatal(err)
			}
			original := app.save
			app.save = func(payload []byte) error {
				if afterReplace {
					if err := original(payload); err != nil {
						return err
					}
				}
				return errors.New("synthetic uncertain deletion")
			}
			stopped := false
			if err := receipt.CancelAfterRevocation(func() { stopped = true }); !errors.Is(err, ErrStateFailed) || !stopped {
				t.Fatal("uncertain deletion did not stop runtime and poison state")
			}
			if !errors.Is(receipt.ValidateRecord(record), ErrStateFailed) {
				t.Fatal("uncertain revocation left receipt usable")
			}
			_ = app.Close()
			_, app = openApplicationTest(t, directory, keys)
			records, _ := app.CommittedContacts()
			wanted := 1
			if afterReplace {
				wanted = 0
			}
			if len(records) != wanted {
				t.Fatal("reopen did not reflect actual deletion")
			}
			if !afterReplace {
				if proof, err := app.CommitPendingContact(record); proof != nil || !errors.Is(err, ErrCommittedRestore) {
					t.Fatal("restored canceled record recreated runtime authority")
				}
			}
		})
	}
}
