package desktop

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestPairingBridgeIsLockedAndCannotInitiateNetworking(t *testing.T) {
	service := New(filepath.Join(t.TempDir(), "private"), &testKeys{})
	defer service.ServiceShutdown()
	if status := service.GetPairingStatus(); status.State != "unavailable" || status.PeerFingerprint != "" || status.RequestID != "" {
		t.Fatal("locked storage exposed a pairing request")
	}
	if _, err := service.ApprovePairing("stale", "unverified"); err == nil {
		t.Fatal("locked bridge accepted approval")
	}
	if _, err := service.DenyPairing("stale"); err == nil {
		t.Fatal("locked bridge accepted a decision")
	}
	if _, err := service.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	if status := service.GetPairingStatus(); status.State != "idle" || status.PeerFingerprint != "" {
		t.Fatal("unlock fabricated a peer request")
	}
	if _, err := service.ApprovePairing("stale", service.GetStatus().IdentityFingerprint); err == nil {
		t.Fatal("missing reservation accepted a decision")
	}
	public, err := json.Marshal(service.GetPairingStatus())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if json.Unmarshal(public, &fields) != nil || len(fields) != 5 {
		t.Fatal("pairing bridge exported more than the fixed public DTO")
	}
	if status := service.GetStatus(); status.PairingAvailable || status.MessagingAvailable || status.AttachmentsAvailable || status.NetworkState != "unavailable" {
		t.Fatal("decision bridge enabled an initiating or messaging capability")
	}
	controller := service.pairing
	if service.LockStorage().StorageState != "locked" || controller.Snapshot().State != "unavailable" || service.GetPairingStatus().PeerFingerprint != "" {
		t.Fatal("storage lock did not shut down pairing and clear its public state")
	}
}
