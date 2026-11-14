package storage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func prepareForgetStorage(t *testing.T, a *ApplicationState, fill byte) (PendingRecord, *identifiedPin, *CommitReceipt, *LocalContactReceipt, *ChatStore, []byte) {
	t.Helper()
	r, pin := commitOngoing(t, a, fill)
	receipt, err := a.CommitPendingContact(r)
	if err != nil || receipt.ConsumeForActivation(r) != nil {
		t.Fatal("could not prepare consumed pairing receipt")
	}
	if err := a.ApproveCommittedConversation(r, pin); err != nil {
		t.Fatal(err)
	}
	local, err := a.LocalConversation(r)
	if err != nil {
		t.Fatal(err)
	}
	store, err := a.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expected an empty synthetic conversation")
	}
	payload := queuedChat(t, pin.approvedPin, "synthetic message waiting offline")
	if err := store.Save(payload); err != nil {
		t.Fatal(err)
	}
	return r, pin, receipt, local, store, payload
}

func TestForgetCommittedContactAcceptsConsumedReceiptAndPreservesOtherSections(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, a := openApplicationTest(t, directory, keys)
	local, _ := a.Identity()
	seed, wrapping := local.Seed(), keys.value
	r, pin, receipt, approval, store, payload := prepareForgetStorage(t, a, 191)
	other, otherPin, _, _, _, otherPayload := prepareForgetStorage(t, a, 192)
	legacy := pinFor(t, a, 193)
	if err := a.PinApprovedContact(legacy); err != nil {
		t.Fatal(err)
	}
	legacyStore, _ := a.ChatStore(legacy)
	if _, err := legacyStore.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expected empty legacy section")
	}
	legacyPayload := queuedChat(t, legacy, "synthetic unrelated legacy history")
	if err := legacyStore.Save(legacyPayload); err != nil {
		t.Fatal(err)
	}
	pending := ongoingRecord(t, a, 194)
	if err := a.SavePendingContact(pending); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(a.RemoveCommittedContact(r.ID), ErrCommittedUsed) {
		t.Fatal("fixture no longer exercises consumed pairing receipt")
	}
	stopped := false
	if err := a.ForgetCommittedContact(r, func() error {
		// These calls need the same vault mutex, proving the callback is outside
		// that lock. Claimed authority cannot be reused before deletion.
		if _, err := a.CommittedContacts(); err != nil {
			return err
		}
		if approval.WithLocalApproval(func() error { t.Error("retiring approval admitted"); return nil }) == nil {
			t.Error("old local receipt remained usable while stopping")
		}
		if _, err := a.LocalConversation(r); err == nil || a.ApproveCommittedConversation(r, pin) == nil {
			t.Error("retiring contact minted new local authority")
		}
		if receipt.ConsumeForActivation(r) == nil {
			t.Error("retiring pairing receipt became reusable")
		}
		stopped = true
		return nil
	}); err != nil || !stopped {
		t.Fatal("consumed contact was not forgotten after stopping")
	}
	got, err := a.CommittedContacts()
	if err != nil || len(got) != 1 || got[0] != other || a.findContactLocked(r.Local, r.Peer) >= 0 {
		t.Fatal("forget did not remove only the exact contact scope")
	}
	if store.Save(payload) == nil || approval.WithLocalApproval(func() error { return nil }) == nil || receipt.ValidateRecord(r) == nil {
		t.Fatal("removed contact retained a stale writer or receipt")
	}
	readOther, _ := a.ChatStore(otherPin)
	if got, err := readOther.Load(); err != nil || !bytes.Equal(got, otherPayload) {
		t.Fatal("forget changed unrelated current conversation")
	}
	if got := readChat(t, a, legacy); !bytes.Equal(got, legacyPayload) {
		t.Fatal("forget changed unrelated legacy history")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openApplicationTest(t, directory, keys)
	identity, _ := reopened.Identity()
	if identity.Seed() != seed || keys.value != wrapping || keys.writes != 1 {
		t.Fatal("forget changed device identity or wrapping credential")
	}
	got, err = reopened.CommittedContacts()
	remaining, pendingErr := reopened.PendingContacts()
	if err != nil || len(got) != 1 || got[0] != other || pendingErr != nil || len(remaining) != 1 || remaining[0] != pending {
		t.Fatal("forget changed another record or was not durable")
	}
	if _, err := reopened.LocalConversation(r); err == nil {
		t.Fatal("removed contact restored local authority")
	}
	if encrypted, err := os.ReadFile(filepath.Join(directory, "state.vault")); err != nil || bytes.Contains(encrypted, payload) || bytes.Contains(encrypted, []byte(r.LocalNodePrivate)) {
		t.Fatal("temporary application state exposed plaintext")
	}
}

func TestForgetRestoredContactAndNewKeysRejectStaleConfirmation(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, a := openApplicationTest(t, directory, keys)
	r, _, _, _, _, _ := prepareForgetStorage(t, a, 195)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	_, a = openApplicationTest(t, directory, keys)
	if err := a.ForgetCommittedContact(r, func() error { return nil }); err != nil {
		t.Fatal("restored contact could not be removed")
	}
	replacement, _, _, _, _, _ := prepareForgetStorage(t, a, 196)
	called := false
	if err := a.ForgetCommittedContact(r, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("stale confirmation stopped a replacement contact")
	}
	if replacement.LocalNodePrivate == r.LocalNodePrivate || replacement.PresharedKey == r.PresharedKey {
		t.Fatal("replacement contact reused credentials")
	}
	got, _ := a.CommittedContacts()
	if len(got) != 1 || got[0] != replacement {
		t.Fatal("stale confirmation removed replacement contact")
	}
}

func TestForgetRequiresExactRecordAndSamePeerRepairingCannotInheritHistory(t *testing.T) {
	_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	r, _, _, _, oldStore, oldPayload := prepareForgetStorage(t, a, 200)
	changed := r
	changed.RelayServiceURL = "https://other-relay.example/map.json"
	called := false
	if err := a.ForgetCommittedContact(changed, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("changed exact association reached runtime retirement")
	}
	if err := a.ForgetCommittedContact(r, nil); err == nil {
		t.Fatal("contact removal accepted a missing retirement callback")
	}
	if err := a.ForgetCommittedContact(r, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	fresh := ongoingRecord(t, a, 201)
	fresh.Peer = r.Peer
	if err := a.SavePendingContact(fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CommitPendingContact(fresh); err != nil {
		t.Fatal(err)
	}
	pin := &identifiedPin{approvedPin: &approvedPin{local: fresh.Local, peer: fresh.Peer}, id: fresh.ID}
	if err := a.ApproveCommittedConversation(fresh, pin); err != nil {
		t.Fatal(err)
	}
	store, err := a.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("same peer's new pairing inherited forgotten history/outbox")
	}
	if fresh.LocalNodePrivate == r.LocalNodePrivate || fresh.PresharedKey == r.PresharedKey || oldStore.Save(oldPayload) == nil {
		t.Fatal("same peer's new association reused removed keys or old history writer")
	}
}

func TestForgetDoesNotDeleteWhenStopOrSaveFails(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "save"}[failSave], func(t *testing.T) {
			directory, keys := privateDirectory(t), &memoryKeys{}
			_, a := openApplicationTest(t, directory, keys)
			r, _, _, approval, store, payload := prepareForgetStorage(t, a, 197)
			before, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
			saves := 0
			save := a.save
			a.save = func(payload []byte) error {
				saves++
				if failSave {
					return errors.New("synthetic refused durable delete")
				}
				return save(payload)
			}
			stopped := false
			err := a.ForgetCommittedContact(r, func() error {
				stopped = true
				if !failSave {
					return errors.New("synthetic failed runtime retirement")
				}
				return nil
			})
			if err == nil || !stopped || (!failSave && saves != 0) || (failSave && saves != 1) {
				t.Fatal("failed stop/save was reported as completed deletion")
			}
			if approval.WithLocalApproval(func() error { return nil }) == nil {
				t.Fatal("failed deletion retained usable local authority")
			}
			if store.Save(payload) == nil {
				t.Fatal("failed stop/save retained a writable chat handle")
			}
			after, _ := os.ReadFile(filepath.Join(directory, "state.vault"))
			if !bytes.Equal(before, after) {
				t.Fatal("failed stop/refused save changed durable state")
			}
			_ = a.Close()
			_, restored := openApplicationTest(t, directory, keys)
			got, err := restored.CommittedContacts()
			if err != nil || len(got) != 1 || got[0] != r {
				t.Fatal("failed deletion removed the old durable association")
			}
		})
	}
}

func TestForgetUncertainWriteAfterReplacementRequiresReconciliation(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, a := openApplicationTest(t, directory, keys)
	r, _, _, approval, store, payload := prepareForgetStorage(t, a, 199)
	save := a.save
	a.save = func(data []byte) error {
		if err := save(data); err != nil {
			return err
		}
		return errors.New("synthetic failure after ciphertext replacement")
	}
	if err := a.ForgetCommittedContact(r, func() error { return nil }); err == nil {
		t.Fatal("uncertain replacement was reported as completed deletion")
	}
	if store.Save(payload) == nil || approval.WithLocalApproval(func() error { return nil }) == nil {
		t.Fatal("uncertain delete retained usable open-state authority")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openApplicationTest(t, directory, keys)
	if records, err := reopened.CommittedContacts(); err != nil || len(records) != 0 {
		t.Fatal("reopen did not reconcile a completed ciphertext replacement")
	}
	if len(reopened.state.Contacts) != 0 {
		t.Fatal("uncertain replacement retained the removed local outbox/history")
	}
}

func TestForgetCancellationClaimHasOneWinnerAndRechecksAfterStop(t *testing.T) {
	for _, closeDuringStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "concurrent", true: "closed"}[closeDuringStop], func(t *testing.T) {
			_, a := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
			r, _, _, _, _, _ := prepareForgetStorage(t, a, 198)
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var callbacks atomic.Int32
			go func() {
				done <- a.ForgetCommittedContact(r, func() error {
					callbacks.Add(1)
					close(entered)
					<-release
					return nil
				})
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("forget did not reach outside-lock stop callback")
			}
			if closeDuringStop {
				_ = a.Close()
			} else if err := a.ForgetCommittedContact(r, func() error { callbacks.Add(1); return nil }); err == nil {
				t.Error("concurrent forget was admitted")
			}
			close(release)
			select {
			case err := <-done:
				if (err != nil) != closeDuringStop || callbacks.Load() != 1 {
					t.Fatal("forget failed to revalidate ownership or admitted duplicate stop")
				}
			case <-time.After(time.Second):
				t.Fatal("forget did not finish after stop returned")
			}
		})
	}
}
