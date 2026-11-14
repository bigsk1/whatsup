package desktop

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopProfileIsExplicitEncryptedAndDoesNotStartNetworking(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "private"), &testKeys{})
	defer s.ServiceShutdown()
	if got, err := s.GetProfile(); err == nil || got != (ProfileSnapshot{}) {
		t.Fatal("locked profile exposed editable metadata")
	}
	if _, err := s.SetProfile("Synthetic Alice", true); err == nil {
		t.Fatal("locked profile could be changed")
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	initial, err := s.GetProfile()
	if err != nil || initial != (ProfileSnapshot{}) {
		t.Fatal("fresh profile did not default to empty and private")
	}
	if got, err := s.SetProfile("Synthetic Alice", false); err != nil || got.DisplayName != "Synthetic Alice" || got.ShareDisplayName {
		t.Fatal("private local profile could not be saved")
	}
	if s.GetConversation().State != "none" || s.GetStatus().NetworkState != "unavailable" {
		t.Fatal("saving a profile started a connection")
	}
	for _, name := range []string{" secret-invalid\n", "spoof\u202e", strings.Repeat("x", 65)} {
		if _, err := s.SetProfile(name, true); err == nil || strings.Contains(err.Error(), name) {
			t.Fatal("invalid profile was accepted or reflected in an error")
		}
	}
	if got, err := s.GetProfile(); err != nil || got.DisplayName != "Synthetic Alice" || got.ShareDisplayName {
		t.Fatal("refused input changed the saved profile")
	}
	if s.LockStorage().StorageState != "locked" {
		t.Fatal("could not close disposable storage")
	}
	if got, err := s.GetProfile(); err == nil || got != (ProfileSnapshot{}) {
		t.Fatal("locking retained profile in bridge response")
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProfile()
	if err != nil || got.DisplayName != "Synthetic Alice" || got.ShareDisplayName {
		t.Fatal("encrypted profile did not survive reopen")
	}
	wire, _ := json.Marshal(got)
	var fields map[string]any
	if json.Unmarshal(wire, &fields) != nil || len(fields) != 2 || fields["displayName"] != "Synthetic Alice" || fields["shareDisplayName"] != false {
		t.Fatal("profile bridge returned fields beyond its narrow DTO")
	}
}

func TestDesktopLocalContactNameIsBoundToExactContactAndRemovedByForget(t *testing.T) {
	s := unlockedInvitationService(t)
	r := prepareDesktopForgetContact(t, s)
	if _, err := s.SetProfile("Synthetic Owner", true); err != nil {
		t.Fatal(err)
	}
	if err := s.application.SetPeerDisplayName(r.ID, r.Peer, "Synthetic Peer"); err != nil {
		t.Fatal(err)
	}
	before := s.GetConversation()
	if got, err := s.SetContactName(r.ID, "My local label"); err != nil || got.LocalName != "My local label" || got.PeerDisplayName != "Synthetic Peer" || got.Revision <= before.Revision || got.State != "offline" {
		t.Fatal("local label did not preserve shared name/offline state")
	}
	if _, err := s.SetContactName("stale", "Wrong person"); err == nil {
		t.Fatal("stale confirmation renamed a current contact")
	}
	if got, err := s.SetContactName(r.ID, "unsafe\nname"); err == nil || got.LocalName != "My local label" {
		t.Fatal("invalid name modified the contact")
	}
	if got, err := s.SetContactName(r.ID, ""); err != nil || got.LocalName != "" || got.PeerDisplayName != "Synthetic Peer" {
		t.Fatal("clearing a local label removed the peer's shared name")
	}
	if _, err := s.ForgetContact(r.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.GetConversation(); got.LocalName != "" || got.PeerDisplayName != "" || got.ContactID != "" {
		t.Fatal("forget retained contact name data")
	}
	if profile, err := s.GetProfile(); err != nil || profile.DisplayName != "Synthetic Owner" || !profile.ShareDisplayName {
		t.Fatal("forget deleted the device's own profile")
	}
	if _, err := s.SetContactName(r.ID, "retired"); err == nil {
		t.Fatal("retired contact could be renamed")
	}
}
