package storage

import "testing"

func TestPeerRemovalUsesExactApprovedScopeAndPreservesPinsAndHistory(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	record, _, _, _, _, before := prepareForgetStorage(t, app, 211)
	other, _, _, _, _, _ := prepareForgetStorage(t, app, 212)
	wrong := record
	wrong.Peer = other.Peer
	if err := app.MarkPeerRemoved(wrong, record.ID); err == nil {
		t.Fatal("changed exact association accepted")
	}
	if err := app.MarkPeerRemoved(record, record.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := app.PeerRemoval(record); err != nil || got != record.ID {
		t.Fatal("approved marker not durable", err)
	}
	if got, err := app.PeerRemoval(other); err != nil || got != "" {
		t.Fatal("unrelated contact paused", err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	_, app = openApplicationTest(t, dir, keys)
	if got, err := app.PeerRemoval(record); err != nil || got != record.ID {
		t.Fatal("restart lost marker", err)
	}
	records, err := app.CommittedContacts()
	if err != nil || len(records) != 2 {
		t.Fatal("peer notice deleted identity pins", err)
	}
	receipt, err := app.LocalConversation(record)
	if err != nil {
		t.Fatal("peer notice blocked local history", err)
	}
	if err := receipt.WithLocalApproval(func() error { return nil }); err != nil {
		t.Fatal("local approval was removed", err)
	}
	var old storedChat
	if decodeCanonical(before, &old) != nil {
		t.Fatal("invalid synthetic history fixture")
	}
	scope, err := app.messages.Scope(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := scope.Page(0, 10)
	if err != nil || len(page) != 1 || page[0].ID != old.Messages[0].ID || page[0].Text != old.Messages[0].Text {
		t.Fatal("peer notice deleted or changed history", err)
	}
}
