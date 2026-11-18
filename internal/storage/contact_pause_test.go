package storage

import (
	"errors"
	"testing"
)

func TestContactPauseIsScopedToAnApprovedAssociationAndPersists(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, directory, keys)
	first, pin := commitOngoing(t, app, 221)
	if _, err := app.ContactPaused(first.ID); err == nil || app.SetContactPaused(first.ID, true) == nil {
		t.Fatal("unfinished pairing acquired pause authority")
	}
	if err := app.ApproveCommittedConversation(first, pin); err != nil {
		t.Fatal(err)
	}
	second, _, _, _, _, _ := prepareForgetStorage(t, app, 222)
	if paused, err := app.ContactPaused(first.ID); err != nil || paused {
		t.Fatal("new contact did not default to unpaused")
	}
	if err := app.SetContactPaused(first.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openApplicationTest(t, directory, keys)
	if paused, err := reopened.ContactPaused(first.ID); err != nil || !paused {
		t.Fatal("pause did not survive encrypted reopen")
	}
	if paused, err := reopened.ContactPaused(second.ID); err != nil || paused {
		t.Fatal("pause spilled into another association")
	}
	if err := reopened.ForgetCommittedContact(first, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := reopened.SetContactPaused(first.ID, false); !errors.Is(err, ErrContact) {
		t.Fatal("stale resume could change a forgotten contact", err)
	}
}
