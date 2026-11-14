package storage

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

func readRootTest(t *testing.T, directory string) rootEnvelope {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "state.vault"))
	if err != nil || !bytes.HasPrefix(data, rootHeader) {
		t.Fatal("root envelope was not saved in version 2")
	}
	var root rootEnvelope
	if json.Unmarshal(data[len(rootHeader):], &root) != nil {
		t.Fatal("root envelope is not valid JSON")
	}
	return root
}

func writeRootTest(t *testing.T, directory string, root rootEnvelope) {
	t.Helper()
	encoded, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.vault"), append(bytes.Clone(rootHeader), encoded...), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMasterIsWrappedAndPurposeKeysSurviveSaveAndReopen(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	v := openTest(t, dir, keys)
	if _, err := v.deriveKeyLocked("messages/v1"); !errors.Is(err, ErrCredential) {
		t.Fatal("key derivation created or used an unpersisted master")
	}
	if _, err := v.sealRootLocked([]byte("synthetic unpersisted state")); err != nil {
		t.Fatal(err)
	}
	if _, err := v.deriveKeyLocked("messages/v1"); !errors.Is(err, ErrCredential) {
		t.Fatal("a sealed but unsaved root made its master available")
	}
	if err := v.Save([]byte("synthetic root payload")); err != nil {
		t.Fatal(err)
	}
	master := bytes.Clone(v.master)
	defer clear(master)
	if len(master) != 32 || bytes.Equal(master, v.key) {
		t.Fatal("OS wrapping secret is being used as the data master")
	}
	first := readRootTest(t, dir)
	if first.Mode != "os-only" || first.Slots.Passphrase != nil || len(first.Slots.OS.Nonce) != chacha20poly1305.NonceSizeX || len(first.Payload.Nonce) != chacha20poly1305.NonceSizeX {
		t.Fatal("unexpected root key-slot mode or nonce size")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
	if bytes.Contains(data, master) || bytes.Contains(data, []byte(base64.StdEncoding.EncodeToString(master))) || bytes.Contains(data, []byte("synthetic root payload")) {
		t.Fatal("master or envelope plaintext reached disk")
	}
	key, err := v.deriveKeyLocked("messages/v1")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	different, err := v.deriveKeyLocked("attachments/v1")
	if err != nil || bytes.Equal(key, different) {
		t.Fatal("vault key purposes are not separated")
	}
	clear(different)
	if err := v.Save([]byte("synthetic newer root payload")); err != nil {
		t.Fatal(err)
	}
	second := readRootTest(t, dir)
	if bytes.Equal(first.Slots.OS.Nonce, second.Slots.OS.Nonce) || bytes.Equal(first.Payload.Nonce, second.Payload.Nonce) {
		t.Fatal("wrapping or payload encryption reused a nonce")
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	v = openTest(t, dir, keys)
	if _, err := v.Load(); err != nil {
		t.Fatal(err)
	}
	reopened, err := v.deriveKeyLocked("messages/v1")
	if err != nil || !bytes.Equal(key, reopened) || !bytes.Equal(master, v.master) || keys.writes != 1 {
		t.Fatal("reopen replaced the master, purpose key or OS credential")
	}
	clear(reopened)
}

func TestRootModeSlotsCiphertextAndCanonicalFormatFailClosed(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	v := openTest(t, dir, keys)
	if err := v.Save([]byte("synthetic authenticated root")); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
	for _, mutate := range []func(*rootEnvelope){
		func(root *rootEnvelope) { root.Mode = "os-plus-passphrase" },
		func(root *rootEnvelope) { root.Version = 3 },
		func(root *rootEnvelope) { root.Slots.Passphrase = &reservedPassphraseSlot{} },
		func(root *rootEnvelope) { root.Slots.OS.Ciphertext[0] ^= 1 },
		func(root *rootEnvelope) { root.Payload.Ciphertext[0] ^= 1 },
		func(root *rootEnvelope) { root.Slots.OS.Nonce = root.Slots.OS.Nonce[:23] },
		func(root *rootEnvelope) { root.Payload.Nonce[0] ^= 1 },
	} {
		if err := os.WriteFile(filepath.Join(dir, "state.vault"), original, 0600); err != nil {
			t.Fatal(err)
		}
		root := readRootTest(t, dir)
		mutate(&root)
		writeRootTest(t, dir, root)
		if _, err := v.Load(); !errors.Is(err, ErrCorrupt) || v.masterPersisted || len(v.master) != 0 {
			t.Fatal("unverified root retained a usable master or accepted changed fields")
		}
	}
	for _, malformed := range [][]byte{
		append(bytes.Clone(rootHeader), []byte(" ")...),
		bytes.Replace(original, []byte(`"mode":"os-only"`), []byte(`"mode":"os-only","mode":"os-only"`), 1),
		append(bytes.Clone(original), '\n'),
		append(bytes.Clone(rootHeader), bytes.Repeat([]byte("x"), maxRootFileBytes)...),
	} {
		if err := os.WriteFile(filepath.Join(dir, "state.vault"), malformed, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := v.Load(); !errors.Is(err, ErrCorrupt) {
			t.Fatal("noncanonical or oversized root accepted")
		}
		if err := v.Save([]byte("must not overwrite corrupt root")); err == nil {
			t.Fatal("saving before authenticating an existing root replaced it")
		}
	}
}

func TestLegacyAESRootReadsAndUpgradesWithoutReplacingIdentityKey(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	v := openTest(t, dir, keys)
	aead, err := v.aead()
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("synthetic schema 6 identity payload")
	legacy := append(append(bytes.Clone(header), nonce...), aead.Seal(nil, nonce, plaintext, header)...)
	if err := os.WriteFile(filepath.Join(dir, "state.vault"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := v.Load()
	if err != nil || !bytes.Equal(loaded, plaintext) || !v.legacy || v.masterPersisted {
		t.Fatal("legacy AES state did not remain readable and explicitly legacy")
	}
	if _, err := v.deriveKeyLocked("messages/v1"); !errors.Is(err, ErrCredential) {
		t.Fatal("legacy root invented a database master before its upgrade")
	}
	// The application migration creates its encrypted backup before this Save.
	if err := v.Save(loaded); err != nil || v.legacy || !v.masterPersisted {
		t.Fatal("legacy payload did not upgrade to an OS-wrapped master")
	}
	readRootTest(t, dir)
	if keys.writes != 1 {
		t.Fatal("legacy upgrade replaced the OS wrapping credential")
	}
	reopened, err := v.Load()
	if err != nil || !bytes.Equal(reopened, plaintext) {
		t.Fatal("legacy payload changed on upgrade")
	}
}

func TestOrphanDatabaseCannotMintNewRootOrCredential(t *testing.T) {
	for _, filename := range []string{"messages.sqlite", "messages.sqlite-wal", "messages.sqlite-shm", "messages.sqlite-journal"} {
		t.Run(filename, func(t *testing.T) {
			dir := privateDirectory(t)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, filename), []byte("synthetic ciphertext"), 0600); err != nil {
				t.Fatal(err)
			}
			keys := &memoryKeys{}
			if _, err := Open(dir, keys); !errors.Is(err, ErrCorrupt) || keys.writes != 0 {
				t.Fatal("orphan message state received a replacement root credential")
			}
		})
	}
}

func TestCloseClearsLiveMasterAndOSWrappingBuffers(t *testing.T) {
	v := openTest(t, privateDirectory(t), &memoryKeys{})
	if err := v.Save([]byte("synthetic live root")); err != nil {
		t.Fatal(err)
	}
	master, osKey := v.master, v.key
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if v.master != nil || v.key != nil || v.masterPersisted || !bytes.Equal(master, make([]byte, 32)) || !bytes.Equal(osKey, make([]byte, 32)) {
		t.Fatal("closed root retained live key buffers")
	}
	if _, err := v.deriveKeyLocked("messages/v1"); !errors.Is(err, ErrLocked) {
		t.Fatal("closed root derived another key")
	}
}
