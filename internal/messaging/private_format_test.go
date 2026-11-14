package messaging

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"whatsup/internal/storage"
)

func TestManagerPointerAndValueCannotFormatOrSerializePrivateRecord(t *testing.T) {
	m := &Manager{managerState: &managerState{record: storage.PendingRecord{
		LocalNodePrivate: "SYNTHETIC-PRIVATE-NODE-MATERIAL",
		PresharedKey:     "SYNTHETIC-PRIVATE-PRESHARED-MATERIAL",
		RelayServiceURL:  "https://synthetic-private-service.invalid/map.json",
	}}}
	for _, value := range []any{m, *m, Manager{}, &Manager{}} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if got := fmt.Sprintf(verb, value); got != "[private WhatsUp messaging coordinator]" {
				t.Fatal("messaging coordinator formatting exposed a private record")
			}
		}
		if encoded, err := json.Marshal(value); err == nil || len(encoded) != 0 {
			t.Fatal("messaging coordinator value or pointer was serializable")
		}
	}
}

func TestZeroManagersHaveNoConversationOrMutationAuthority(t *testing.T) {
	for _, m := range []*Manager{nil, {}} {
		if got := m.Snapshot(); got.State != "unavailable" || got.CanConnect || got.CanSend || len(got.Messages) != 0 {
			t.Fatal("zero manager exposed a usable conversation")
		}
		if _, err := m.Connect("synthetic-id"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("zero manager started networking")
		}
		if _, err := m.Send("synthetic-id", "synthetic forbidden text"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("zero manager queued a message")
		}
		if _, err := m.ClearHistory("synthetic-id"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("zero manager changed history")
		}
		if _, err := m.DeleteMessage("synthetic-id", "synthetic-message"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("zero manager deleted a message")
		}
		m.MetadataChanged()
		m.Close()
	}
}

func TestManagerCopiesShareClosureInsteadOfCopyingLocks(t *testing.T) {
	m, _, _ := newBlockedManager(t)
	alias := *m
	alias.Close()
	if got := m.Snapshot(); got.State != "unavailable" || got.CanSend || got.CanConnect || len(got.Messages) != 0 {
		t.Fatal("closing a copied manager left the original active")
	}
}

func TestZeroRegistriesHaveNoContactOrMutationAuthority(t *testing.T) {
	for _, r := range []*Registry{nil, {}} {
		if _, err := r.Contacts(); !errors.Is(err, ErrUnavailable) {
			t.Fatal("zero registry exposed contacts")
		}
		if _, err := r.Connect("synthetic"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("zero registry started networking")
		}
		if _, err := r.Send("synthetic", "forbidden"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("zero registry queued text")
		}
		if _, err := r.Page("synthetic", "", 100); !errors.Is(err, ErrUnavailable) {
			t.Fatal("zero registry read history")
		}
		if _, err := r.MarkRead("synthetic", "message"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("zero registry changed unread state")
		}
		if err := r.Retire("synthetic"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("zero registry retired a contact")
		}
		r.MetadataChanged("")
		r.Close()
	}
	private := &Registry{registryState: &registryState{application: &storage.ApplicationState{}}}
	for _, value := range []any{private, *private, Registry{}, &Registry{}} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if fmt.Sprintf(verb, value) != "[private WhatsUp contact runtimes]" {
				t.Fatal("registry formatting traversed private runtime state")
			}
		}
		if wire, err := json.Marshal(value); err == nil || len(wire) != 0 {
			t.Fatal("registry runtime was serializable")
		}
	}
}
