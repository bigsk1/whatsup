package pairing

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCredentialPreparationOnlyRunsOncePerLocalApproval(t *testing.T) {
	contact, err := newContact(syntheticKey(t, 1), syntheticKey(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	defer contact.Revoke()
	var prepared atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			err := contact.PrepareTransportCredentials(func() error { prepared.Add(1); return nil })
			if err != nil && !errors.Is(err, ErrPeer) {
				t.Error("unexpected credential preparation result")
			}
		})
	}
	wg.Wait()
	if prepared.Load() != 1 {
		t.Fatal("one local approval provisioned multiple ongoing credential bundles")
	}
}

func TestCredentialPreparationFailureAndInvalidContactsFailClosed(t *testing.T) {
	contact, _ := newContact(syntheticKey(t, 1), syntheticKey(t, 2))
	defer contact.Revoke()
	failure := errors.New("synthetic generation failure")
	if err := contact.PrepareTransportCredentials(func() error { return failure }); !errors.Is(err, failure) {
		t.Fatal("credential preparation failure was lost")
	}
	if err := contact.PrepareTransportCredentials(func() error { return nil }); err != nil {
		t.Fatal("failed preparation burned the local approval")
	}
	for _, invalid := range []*Contact{nil, {}, {state: &contactState{local: syntheticKey(t, 1), peer: syntheticKey(t, 1)}}} {
		called := false
		if err := invalid.PrepareTransportCredentials(func() error { called = true; return nil }); !errors.Is(err, ErrPeer) || called {
			t.Fatal("invalid contact prepared credentials")
		}
		if err := invalid.AuthorizeAuthenticatedPeer(invalid.Peer()); !errors.Is(err, ErrPeer) {
			t.Fatal("zero or self contact authorized a peer")
		}
	}
}

func TestApprovalCopiesSharePreparationAndRevocation(t *testing.T) {
	contact, _ := newContact(syntheticKey(t, 1), syntheticKey(t, 2))
	copyOfContact := *contact
	if err := contact.PrepareTransportCredentials(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := copyOfContact.PrepareTransportCredentials(func() error { return nil }); !errors.Is(err, ErrPeer) {
		t.Fatal("copied local approval provisioned unrelated credentials")
	}
	copyOfContact.Revoke()
	if err := contact.AuthorizeAuthenticatedPeer(contact.Peer()); !errors.Is(err, ErrPeer) {
		t.Fatal("contact copy bypassed revocation")
	}
	for _, value := range []any{contact, *contact} {
		if fmt.Sprintf("%#v", value) != "[private WhatsUp contact credentials]" {
			t.Fatal("contact value formatting exposed private pins")
		}
	}
}
