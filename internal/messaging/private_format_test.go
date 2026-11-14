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
