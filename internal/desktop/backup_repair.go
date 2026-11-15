package desktop

import (
	"errors"

	"whatsup/internal/storage"
)

// SetAsideBackup can act only on the exact backend-captured backup failure.
// There is no filename/path argument or filesystem capability in this bridge.
func (s *Service) SetAsideBackup(secret string) (Status, error) {
	data := []byte(secret)
	defer clear(data)
	s.mu.Lock()
	attention := s.backupAttention
	if attention == nil || attention.Digest == [32]byte{} || s.vault != nil || s.state == "unlocked" {
		status := s.status()
		s.mu.Unlock()
		return status, errors.New("There is no reported backup available to set aside. Retry going online to check local storage.")
	}
	op, err := s.beginSecurityLocked(false)
	s.mu.Unlock()
	if err != nil {
		return s.GetStatus(), err
	}
	err = storage.SetAsideManagedCopy(op.life, s.directory, s.keys, data, attention.RelativePath, attention.Digest)
	s.mu.Lock()
	if !s.currentSecurityLocked(op) {
		s.finishSecurityLocked(op)
		status := s.status()
		s.mu.Unlock()
		return status, errSecurity
	}
	s.finishSecurityLocked(op)
	if err != nil {
		status := s.status()
		s.mu.Unlock()
		return status, errors.New("The backup could not be set aside. Check the current app passphrase and OS credential, or retry going online if the backup changed.")
	}
	s.backupAttention = nil
	status, err := s.unlockStorageLocked(data)
	s.mu.Unlock()
	return status, err
}
