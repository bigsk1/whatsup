package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"whatsup/internal/identity"
	"whatsup/internal/media"
)

func avatarFixture(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			img.Set(x, y, color.NRGBA{R: shade, G: 43, B: 91, A: 255})
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	data, err := media.PrepareAvatar(context.Background(), bytes.NewReader(source.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAvatarEncryptedProfileAndExactPeerReplayProtection(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, directory, keys)
	record, _, _, _, _, _ := prepareForgetStorage(t, app, 177)
	data, changed := avatarFixture(t, 17), avatarFixture(t, 91)
	local, err := app.SetAvatar(context.Background(), data)
	if err != nil || local.ShareAvatar || local.HideContactPhotos || local.Avatar == "" {
		t.Fatal("owner picture implicitly shared", err)
	}
	if _, err := app.SetProfile("Synthetic owner", true); err != nil {
		t.Fatal(err)
	}
	if current, _ := app.Profile(); current.Avatar != local.Avatar {
		t.Fatal("name update lost local picture")
	}
	other, _ := identity.Generate()
	if err := app.SetPeerAvatar(context.Background(), record.ID, other.PublicKey(), 1, data); err == nil {
		t.Fatal("wrong pin installed picture")
	}
	if err := app.SetPeerAvatar(context.Background(), record.ID, record.Peer, 3, data); err != nil {
		t.Fatal(err)
	}
	writes := 0
	save := app.save
	app.save = func(data []byte) error { writes++; return save(data) }
	for range 10 {
		if err := app.SetPeerAvatar(context.Background(), record.ID, record.Peer, 3, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.SetPeerAvatar(context.Background(), record.ID, record.Peer, 2, changed); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatal("replay changed ciphertext")
	}
	if err := app.SetPeerAvatar(context.Background(), record.ID, record.Peer, 3, changed); err == nil {
		t.Fatal("same revision changed picture")
	}
	if err := app.SetPeerAvatar(context.Background(), record.ID, record.Peer, 4, nil); err != nil {
		t.Fatal(err)
	}
	if err := app.SetPeerAvatar(context.Background(), record.ID, record.Peer, 3, data); err != nil {
		t.Fatal(err)
	}
	if peer, _ := app.ContactAvatar(record.ID); peer.Data != "" || peer.Revision != 4 {
		t.Fatal("replay resurrected withdrawn photo")
	}
	if _, err := app.SetAvatarPreferences(true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := app.SetAvatarPreferences(false, true); err != nil {
		t.Fatal(err)
	}
	if current, _ := app.Profile(); current.Avatar != local.Avatar || current.ShareAvatar || current.HideContactPhotos {
		t.Fatal("unsharing lost local picture")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := os.ReadFile(filepath.Join(directory, "state.vault"))
	if err != nil || bytes.Contains(ciphertext, data) || bytes.Contains(ciphertext, []byte(base64.StdEncoding.EncodeToString(data))) {
		t.Fatal("picture is readable on disk")
	}
	_, reopened := openApplicationTest(t, directory, keys)
	if got, _ := reopened.Profile(); got.Avatar != local.Avatar {
		t.Fatal("encrypted picture did not survive reopen")
	}
	if peer, _ := reopened.ContactAvatar(record.ID); peer.Data != "" || peer.Revision != 4 {
		t.Fatal("withdrawal tombstone lost on reopen")
	}
}

func TestSchemaEightAvatarMigrationPreservesIdentityAndMakesEncryptedBackup(t *testing.T) {
	directory, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, directory, keys)
	local, _ := app.Identity()
	seed := local.Seed()
	old := app.cloneLocked()
	old.Version = 8
	payload, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	vault, err := Open(directory, keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Save(payload); err != nil {
		t.Fatal(err)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openApplicationTest(t, directory, keys)
	got, _ := reopened.Identity()
	if got.Seed() != seed || reopened.state.Version != 9 {
		t.Fatal("migration changed identity")
	}
	copies, err := filepath.Glob(filepath.Join(directory, "pre-migration-*.vault"))
	if err != nil || len(copies) != 1 {
		t.Fatal("migration did not preserve encrypted root backup")
	}
	backup, err := os.ReadFile(copies[0])
	if err != nil || bytes.Contains(backup, payload) || bytes.Contains(backup, seed[:]) {
		t.Fatal("migration backup is not encrypted")
	}
}

func TestAvatarRecoveryRoundtripKeepsOwnerAndContactPhotosEncrypted(t *testing.T) {
	_, source := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record, _, _, _, _, _ := prepareForgetStorage(t, source, 169)
	data := avatarFixture(t, 73)
	if _, err := source.SetAvatar(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetAvatarPreferences(true, false); err != nil {
		t.Fatal(err)
	}
	if err := source.SetPeerAvatar(context.Background(), record.ID, record.Peer, 7, data); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := source.ExportRecovery(context.Background(), &archive, testPassphrase); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive.Bytes(), data) || bytes.Contains(archive.Bytes(), []byte(base64.StdEncoding.EncodeToString(data))) {
		t.Fatal("recovery archive exposed photo")
	}
	dir, keys := privateDirectory(t), &memoryKeys{}
	staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(archive.Bytes()), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if err := staged.Commit(context.Background(), keys, nil, false); err != nil {
		t.Fatal(err)
	}
	_, restored := openApplicationTest(t, dir, keys)
	local, err := restored.Profile()
	if err != nil || local.Avatar != base64.StdEncoding.EncodeToString(data) || !local.ShareAvatar || !local.HideContactPhotos {
		t.Fatal("recovery changed owner avatar/preferences", err)
	}
	peer, err := restored.ContactAvatar(record.ID)
	if err != nil || peer.Data != local.Avatar || peer.Revision != 7 {
		t.Fatal("recovery lost authenticated contact avatar/replay revision", err)
	}
}

func TestSchemaEightPortableBackupUpgradesWithoutInventingPhotos(t *testing.T) {
	_, source := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	state := source.cloneLocked()
	state.Version = 8
	wire, err := json.Marshal(portableApplication{Version: 1, State: state})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePortableApplication(wire)
	if err != nil || decoded.State.Version != ApplicationSchemaVersion || !legacyAvatarEnvelope(decoded.State) {
		t.Fatal("old portable backup cannot be reopened", err)
	}
	state.Profile.Avatar = base64.StdEncoding.EncodeToString(avatarFixture(t, 7))
	state.Profile.AvatarRevision = 1
	wire, err = json.Marshal(portableApplication{Version: 1, State: state})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePortableApplication(wire); err == nil {
		t.Fatal("old schema smuggled avatar fields")
	}
}
