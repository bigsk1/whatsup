package messaging

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/png"
	"path/filepath"
	"testing"
	"time"

	"whatsup/internal/media"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
)

func avatarFixture(t *testing.T) []byte {
	t.Helper()
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	data, err := media.PrepareAvatar(context.Background(), bytes.NewReader(input.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAvatarPacketBoundedAndRequiresAuthenticatedCapability(t *testing.T) {
	for _, data := range [][]byte{nil, avatarFixture(t)} {
		frame := avatarFrame{7, data}
		wire, err := encodeAvatar(frame)
		if err != nil {
			t.Fatal(err)
		}
		wire = append(wire, pingPacket)
		wire = append(wire, make([]byte, 16)...)
		reader := bytes.NewReader(wire)
		got, err := readPacket(reader)
		if err != nil || got.kind != avatarPacket || got.avatar.revision != 7 || !bytes.Equal(got.avatar.data, data) {
			t.Fatal("avatar framing changed bytes", err)
		}
		if got, err := readPacket(reader); err != nil || got.kind != pingPacket || reader.Len() != 0 {
			t.Fatal("avatar framing consumed heartbeat")
		}
		if _, err := readPacketFor(bytes.NewReader(wire), sessionauth.Capabilities{WireVersion: 4, Attachments: true, Control: true, Withdrawals: true}); err == nil {
			t.Fatal("old capability accepted avatar")
		}
	}
	for _, length := range []uint32{media.MaxAvatarBytes + 1, ^uint32(0)} {
		header := []byte{avatarPacket, 1}
		header = binary.BigEndian.AppendUint64(header, 1)
		header = binary.BigEndian.AppendUint32(header, length)
		reader := &headerOnlyReader{header: bytes.NewReader(header)}
		if _, err := readPacket(reader); err == nil || reader.bodyRead {
			t.Fatal("oversized avatar read body")
		}
	}
	for _, frame := range []avatarFrame{{0, nil}, {maxAvatarRevision + 1, nil}, {1, []byte("<svg/>")}} {
		if _, err := encodeAvatar(frame); err == nil {
			t.Fatal("invalid avatar encoded")
		}
	}
}

func TestAvatarBudgetRejectsSameRevisionMutationAndLimitsNewImages(t *testing.T) {
	var b avatarBudget
	now := time.Now()
	for i := uint64(1); i <= 5; i++ {
		if !b.accept(avatarFrame{i, nil}, now) {
			t.Fatal("ordinary avatar revision refused")
		}
	}
	for range 100 {
		if !b.accept(avatarFrame{5, nil}, now) {
			t.Fatal("same image replay consumed budget")
		}
	}
	if b.accept(avatarFrame{5, []byte("changed")}, now) || b.accept(avatarFrame{6, nil}, now) || !b.accept(avatarFrame{6, nil}, now.Add(10*time.Second)) {
		t.Fatal("avatar budget or replay check failed")
	}
}

func TestAvatarSharingIsSeparateAndWithdrawalReachesExistingSession(t *testing.T) {
	m, server, client, conversation, _ := exchangeFixture(t)
	if _, err := m.application.SetAvatar(context.Background(), avatarFixture(t)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- m.exchange(ctx, server, conversation) }()
	consumeInitialProfile(t, client)
	var got packet
	var err error
	for _, share := range []bool{true, false} {
		if _, err := m.application.SetAvatarPreferences(share, true); err != nil {
			t.Fatal(err)
		}
		m.MetadataChanged()
		if got, err := readPacket(client); err != nil || got.kind != profilePacket {
			t.Fatal("profile wake missing")
		}
		got, err = readPacket(client)
		if err != nil || got.kind != avatarPacket || (len(got.avatar.data) > 0) != share {
			t.Fatal("photo share/withdraw not reflected", err)
		}
	}
	cancel()
	waitExchange(t, result)
}

func TestAvatarTwoPinnedTLSPeersShareAndWithdrawAfterOfflineReconnect(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	data := avatarFixture(t)
	if _, err := a.app.SetAvatar(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	network := &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	for _, p := range []*registryPeer{a, b} {
		if _, err := p.registry.Connect(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []*registryPeer{a, b} {
		waitRegistry(t, p, id, func(s Snapshot) bool { return s.State == "connected" })
	}
	if got, err := b.app.ContactAvatar(id); err != nil || got.Data != "" {
		t.Fatal("private own photo sent on initial connection", err)
	}
	if _, err := a.app.SetAvatarPreferences(true, true); err != nil {
		t.Fatal(err)
	}
	a.registry.MetadataChanged("")
	want := base64.StdEncoding.EncodeToString(data)
	waitRegistry(t, b, id, func(s Snapshot) bool { got, err := b.app.ContactAvatar(id); return err == nil && got.Data == want })
	b.registry.Close()
	if _, err := a.app.SetAvatarPreferences(false, true); err != nil {
		t.Fatal(err)
	}
	a.registry.MetadataChanged("")
	openRegistry(t, b, network, nil)
	if _, err := b.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, b, id, func(s Snapshot) bool {
		got, err := b.app.ContactAvatar(id)
		return err == nil && got.Data == "" && got.Revision >= 1
	})
	if _, err := b.registry.Send(id, "synthetic avatar withdrawal keeps text working"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, a, id, func(s Snapshot) bool { return len(s.Messages) == 1 })
	if local, _ := a.app.Profile(); local.Avatar != want || local.ShareAvatar {
		t.Fatal("withdrawal removed own local photo")
	}
}

func TestRestoredOwnerBackupCanWithdrawOlderRevisionOnFreshPinnedTLS(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	data := avatarFixture(t)
	if _, err := a.app.SetAvatar(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if _, err := a.app.SetAvatarPreferences(true, true); err != nil {
		t.Fatal(err)
	}
	network := &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	for _, p := range []*registryPeer{a, b} {
		if _, err := p.registry.Connect(id); err != nil {
			t.Fatal(err)
		}
	}
	waitRegistry(t, b, id, func(s Snapshot) bool {
		photo, err := b.app.ContactAvatar(id)
		return s.State == "connected" && err == nil && photo.Data != "" && photo.Revision == 1
	})
	secret := []byte("synthetic-avatar-recovery-passphrase")
	var archive bytes.Buffer
	a.registry.Close()
	if err := a.app.ExportRecovery(context.Background(), &archive, secret); err != nil {
		t.Fatal(err)
	}
	openRegistry(t, a, network, nil)
	if _, err := a.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, a, id, func(s Snapshot) bool { return s.State == "connected" })
	// Connected is published before the exchange's initial profile write. A
	// delivered chat frame is a real stream barrier: the initial snapshot was
	// handled first, so later local edits cannot coalesce into wire revision 1.
	if _, err := a.registry.Send(id, "synthetic initial avatar stream barrier"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, b, id, func(s Snapshot) bool {
		for _, message := range s.Messages {
			if message.Text == "synthetic initial avatar stream barrier" {
				return true
			}
		}
		return false
	})
	for _, show := range []bool{false, true, false, true} {
		before, _ := b.app.ContactAvatar(id)
		if _, err := a.app.SetAvatarPreferences(true, show); err != nil {
			t.Fatal(err)
		}
		a.registry.MetadataChanged("")
		waitRegistry(t, b, id, func(s Snapshot) bool {
			photo, err := b.app.ContactAvatar(id)
			return err == nil && photo.Revision > before.Revision
		})
	}
	waitRegistry(t, b, id, func(s Snapshot) bool {
		photo, err := b.app.ContactAvatar(id)
		return err == nil && photo.Revision == 5 && photo.Data != ""
	})
	a.registry.Close()
	directory, keys := filepath.Join(t.TempDir(), "restored-private"), &lifecycleKeys{}
	staged, err := storage.StageRecovery(context.Background(), directory, bytes.NewReader(archive.Bytes()), secret)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if err := staged.Commit(context.Background(), keys, nil, false); err != nil {
		t.Fatal(err)
	}
	vault, err := storage.Open(directory, keys)
	if err != nil {
		t.Fatal(err)
	}
	app, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	local, err := app.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if !local.PublicKey().Equal(a.local.PublicKey()) {
		t.Fatal("recovery changed owner identity")
	}
	restored := &registryPeer{local: local, app: app, revisions: &Revisions{}, directory: directory, keys: keys}
	t.Cleanup(func() {
		if restored.registry != nil {
			restored.registry.Close()
		}
		_ = app.Close()
	})
	profile, err := app.SetAvatarPreferences(false, true)
	if err != nil || profile.AvatarRevision != 3 {
		t.Fatal("did not restore older sharing revision", err)
	}
	openRegistry(t, restored, network, nil)
	if _, err := restored.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, restored, id, func(s Snapshot) bool { return s.State == "connected" })
	if _, err := restored.registry.Send(id, "synthetic restored profile text"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, b, id, func(s Snapshot) bool {
		for _, message := range s.Messages {
			if message.Text == "synthetic restored profile text" {
				return true
			}
		}
		return false
	})
	photo, err := b.app.ContactAvatar(id)
	if err != nil || photo.Data != "" || photo.Revision != 1 {
		t.Fatal("fresh authenticated restored profile could not withdraw an older avatar revision", err)
	}
	if _, err := app.SetAvatarPreferences(true, true); err != nil {
		t.Fatal(err)
	}
	restored.registry.MetadataChanged("")
	waitRegistry(t, b, id, func(s Snapshot) bool {
		photo, err := b.app.ContactAvatar(id)
		return err == nil && photo.Data != "" && photo.Revision == 2
	})
}
