package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"whatsup/internal/profile"
)

// Preserve the exact previous canonical envelope; metadata migration must not
// reinterpret schema7 as a pre-SQLite snapshot or regenerate any key/scope.
type applicationEnvelopeV7 struct {
	Version             int                 `json:"version"`
	Identity            identitySection     `json:"identity"`
	Profile             profile.Local       `json:"profile"`
	Contacts            []contactSection    `json:"contacts"`
	PendingContacts     []pendingRecordDisk `json:"pendingContacts"`
	CommittedContacts   []pendingRecordDisk `json:"committedContacts"`
	MessageStoreState   string              `json:"messageStoreState"`
	MigrationBackup     string              `json:"migrationBackup,omitempty"`
	MigrationBackupHash string              `json:"migrationBackupSHA256,omitempty"`
	DeleteScopes        []string            `json:"deleteScopes"`
}

func upgradeEnvelopeV7(old applicationEnvelopeV7) applicationEnvelope {
	return applicationEnvelope{Version: ApplicationSchemaVersion, Identity: old.Identity, Profile: old.Profile, Contacts: old.Contacts, PendingContacts: old.PendingContacts, CommittedContacts: old.CommittedContacts, MessageStoreState: old.MessageStoreState, MigrationBackup: old.MigrationBackup, MigrationBackupHash: old.MigrationBackupHash, DeleteScopes: old.DeleteScopes, Routes: []routeStateDisk{}}
}
func (a *ApplicationState) backupRootMetadataLocked() error {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	name := "pre-migration-" + hex.EncodeToString(random[:]) + ".vault"
	data, err := readCipherFile(filepath.Join(a.vault.directory, "state.vault"), maxRootFileBytes)
	if err != nil {
		return err
	}
	destination, err := os.CreateTemp(a.vault.directory, ".state-*.tmp")
	if err != nil {
		return err
	}
	temp := destination.Name()
	defer os.Remove(temp)
	if destination.Chmod(0600) != nil {
		destination.Close()
		return ErrUnsafePath
	}
	if n, err := destination.Write(data); err != nil || n != len(data) {
		destination.Close()
		return ErrState
	}
	if destination.Sync() != nil {
		destination.Close()
		return ErrState
	}
	if destination.Close() != nil {
		return ErrState
	}
	// A same-directory hard link publishes only the already-synced complete
	// ciphertext and never replaces an existing backup name. A crash can leave
	// an owned temp link, which normal protection cleanup safely retires.
	if err := os.Link(temp, filepath.Join(a.vault.directory, name)); err != nil {
		return err
	}
	if err := syncDirectory(a.vault.directory); err != nil {
		return err
	}
	a.state.MigrationBackup = name
	hash := sha256.Sum256(data)
	a.state.MigrationBackupHash = hex.EncodeToString(hash[:])
	return nil
}

type portableApplicationV7 struct {
	Version            int                   `json:"version"`
	RequiresPassphrase bool                  `json:"requiresPassphrase"`
	State              applicationEnvelopeV7 `json:"state"`
}

func decodePortableApplication(payload []byte) (portableApplication, error) {
	var version struct {
		State struct {
			Version int `json:"version"`
		} `json:"state"`
	}
	if json.Unmarshal(payload, &version) != nil {
		return portableApplication{}, ErrRecovery
	}
	if version.State.Version == 7 {
		var old portableApplicationV7
		if decodeCanonical(payload, &old) != nil {
			return portableApplication{}, ErrRecovery
		}
		return portableApplication{Version: old.Version, RequiresPassphrase: old.RequiresPassphrase, State: upgradeEnvelopeV7(old.State)}, nil
	}
	var current portableApplication
	if decodeCanonical(payload, &current) != nil {
		return portableApplication{}, ErrRecovery
	}
	return current, nil
}
