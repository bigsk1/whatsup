package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
	"whatsup/internal/identity"
	"whatsup/internal/storage"
)

type testKeys struct {
	key  string
	fail bool
}

func (k *testKeys) Get() (string, error) {
	if k.fail {
		return "", errors.New("unavailable")
	}
	if k.key == "" {
		return "", keyring.ErrNotFound
	}
	return k.key, nil
}
func (k *testKeys) Set(value string) error { k.key = value; return nil }

func TestIdentityIsDurableAndBridgeNeverReportsMessaging(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	keys := &testKeys{}
	s := New(dir, keys)
	if s.GetStatus().StorageState != "locked" {
		t.Fatal("must start locked")
	}
	first, err := s.UnlockStorage()
	if err != nil || len(first.IdentityFingerprint) != 64 {
		t.Fatalf("identity unavailable: %v", err)
	}
	if first.DirectOnlyAvailable || first.PairingAvailable || first.MessagingAvailable || first.AttachmentsAvailable || first.NetworkState != "unavailable" {
		t.Fatal("unimplemented capabilities enabled")
	}
	if s.LockStorage().IdentityFingerprint != "" {
		t.Fatal("locked public identity retained")
	}
	s = New(dir, keys)
	defer s.ServiceShutdown()
	second, err := s.UnlockStorage()
	if err != nil || first.IdentityFingerprint != second.IdentityFingerprint {
		t.Fatal("identity changed after restart")
	}
	encoded, _ := json.Marshal(second)
	if strings.Contains(string(encoded), keys.key) || strings.Contains(string(encoded), "Seed") {
		t.Fatal("bridge exposed private material")
	}
}

func TestCredentialOutageFailsClosedAndRetryRecovers(t *testing.T) {
	keys := &testKeys{fail: true}
	s := New(filepath.Join(t.TempDir(), "private"), keys)
	status, err := s.UnlockStorage()
	if err == nil || status.StorageState != "unavailable" || status.IdentityFingerprint != "" {
		t.Fatal("credential outage did not fail closed")
	}
	keys.fail = false
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	_ = s.ServiceShutdown()
}

func TestDesktopMigratesLegacyIdentityAndPreservesMalformedState(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "migration", true: "malformed"}[malformed], func(t *testing.T) {
			dir, keys := filepath.Join(t.TempDir(), "private"), &testKeys{}
			vault, err := storage.Open(dir, keys)
			if err != nil {
				t.Fatal(err)
			}
			seed := bytes.Repeat([]byte{11}, 32)
			legacy, _ := json.Marshal(struct {
				Version int    `json:"version"`
				Seed    []byte `json:"identitySeed"`
			}{1, seed})
			if malformed {
				legacy = append(legacy[:len(legacy)-1], []byte(`,"unknown":true}`)...)
			}
			if err := vault.Save(legacy); err != nil {
				t.Fatal(err)
			}
			if err := vault.Close(); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
			service := New(dir, keys)
			defer service.ServiceShutdown()
			status, err := service.UnlockStorage()
			if malformed {
				if err == nil || status.StorageState != "unavailable" || status.IdentityFingerprint != "" {
					t.Fatal("malformed legacy state did not fail closed")
				}
				after, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
				if !bytes.Equal(before, after) {
					t.Fatal("desktop replaced malformed state")
				}
				return
			}
			local, _ := identity.FromSeed(seed)
			if err != nil || status.IdentityFingerprint != local.PublicKey().Fingerprint() {
				t.Fatal("desktop migration changed identity")
			}
			if service.application == nil {
				t.Fatal("desktop retained raw identity schema")
			}
		})
	}
}
