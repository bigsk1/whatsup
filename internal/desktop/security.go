package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"whatsup/internal/diagnostics"
	"whatsup/internal/keymaterial"
	"whatsup/internal/storage"
)

var errSecurity = errors.New("The local security operation could not be completed. Local data has not been made available.")

// SecurityRuntimeOptions supplies native dialogs and lifecycle notifications.
// It is trusted Go construction input and never a renderer-selectable provider.
type SecurityRuntimeOptions struct {
	Life               context.Context
	PickRecovery       func(context.Context) (string, error)
	SaveRecovery       func(context.Context) (string, error)
	StorageChanged     func(string)
	PreferencesChanged func() error
}
type securityControl struct {
	options   SecurityRuntimeOptions
	operation *securityOperation
	imports   map[string]*securityImport
}
type securityOperation struct {
	life        context.Context
	cancel      context.CancelFunc
	application *storage.ApplicationState
	vault       *storage.Vault
	quiesced    bool
}
type securityImport struct {
	cap     *storage.RecoveryImport
	expires time.Time
	timer   *time.Timer
}

type StorageSecurity struct {
	RequiresPassphrase bool `json:"requiresPassphrase"`
	RecoveryAvailable  bool `json:"recoveryAvailable"`
	Busy               bool `json:"busy"`
}
type RecoveryExportResult struct {
	Saved bool `json:"saved"`
}
type RecoveryImportPreview struct {
	Handle                   string `json:"handle"`
	Contacts                 int    `json:"contacts"`
	Messages                 uint64 `json:"messages"`
	Attachments              uint64 `json:"attachments"`
	SourceRequiresPassphrase bool   `json:"sourceRequiresPassphrase"`
	ReplaceRequired          bool   `json:"replaceRequired"`
	CredentialResetRequired  bool   `json:"credentialResetRequired"`
}

func ConfigureSecurity(s *Service, options SecurityRuntimeOptions) error {
	if s == nil || options.PickRecovery == nil || options.SaveRecovery == nil {
		return errSecurity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "locked" || s.vault != nil || s.security != nil {
		return errSecurity
	}
	s.security = &securityControl{options: options, imports: map[string]*securityImport{}}
	return nil
}
func (s *Service) securityLocked() *securityControl {
	if s.security == nil {
		s.security = &securityControl{imports: map[string]*securityImport{}}
	}
	return s.security
}
func (s *Service) GetStorageSecurity() StorageSecurity {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := StorageSecurity{RequiresPassphrase: s.status().RequiresPassphrase}
	if s.security != nil {
		result.Busy = s.security.operation != nil
		result.RecoveryAvailable = s.security.options.PickRecovery != nil && s.security.options.SaveRecovery != nil
	}
	return result
}
func (s *Service) UnlockWithPassphrase(secret string) (Status, error) {
	data := []byte(secret)
	defer clear(data)
	if keymaterial.ValidateSecret(data) != nil {
		return s.GetStatus(), errors.New("Use a passphrase of 12 to 1024 UTF-8 bytes")
	}
	return s.unlockStorage(data)
}
func (s *Service) beginSecurityLocked(unlocked bool) (*securityOperation, error) {
	control := s.securityLocked()
	if control.operation != nil || unlocked && (s.state != "unlocked" || s.application == nil || s.vault == nil) {
		return nil, errSecurity
	}
	parent := control.options.Life
	if parent == nil {
		parent = context.Background()
	}
	if parent.Err() != nil {
		return nil, errSecurity
	}
	life, cancel := context.WithCancel(parent)
	op := &securityOperation{life: life, cancel: cancel, application: s.application, vault: s.vault}
	control.operation = op
	return op, nil
}
func (s *Service) currentSecurityLocked(op *securityOperation) bool {
	return s.security != nil && s.security.operation == op && op.life.Err() == nil
}
func (s *Service) finishSecurityLocked(op *securityOperation) {
	if s.security != nil && s.security.operation == op {
		s.security.operation = nil
	}
	op.cancel()
}
func (s *Service) cancelSecurityLocked() {
	if s.security == nil {
		return
	}
	if op := s.security.operation; op != nil {
		op.cancel()
		s.security.operation = nil
	}
	for handle, entry := range s.security.imports {
		entry.timer.Stop()
		_ = entry.cap.Close()
		delete(s.security.imports, handle)
	}
}
func (s *Service) securityHint(op *securityOperation, state string) {
	s.mu.Lock()
	var emit func(string)
	if s.security != nil {
		emit = s.security.options.StorageChanged
	}
	active := s.security != nil && s.security.operation == op || op == nil
	s.mu.Unlock()
	if emit != nil && active {
		emit(state)
	}
}

func (s *Service) ConfigurePassphrase(currentSecret, newSecret string, enabled bool) (Status, error) {
	current, next := []byte(currentSecret), []byte(newSecret)
	defer clear(current)
	defer clear(next)
	if enabled && keymaterial.ValidateSecret(next) != nil || !enabled && len(next) != 0 {
		return s.GetStatus(), errors.New("Use a passphrase of 12 to 1024 UTF-8 bytes")
	}
	s.mu.Lock()
	op, err := s.beginSecurityLocked(true)
	s.mu.Unlock()
	if err != nil {
		return s.GetStatus(), err
	}
	err = op.application.SetPassphrase(op.life, current, next)
	s.mu.Lock()
	if !s.currentSecurityLocked(op) {
		s.finishSecurityLocked(op)
		status := s.status()
		s.mu.Unlock()
		return status, errSecurity
	}
	s.finishSecurityLocked(op)
	if err != nil {
		s.record(diagnostics.StorageUnavailable)
		status := s.lockStorageLocked()
		s.mu.Unlock()
		s.securityHint(nil, status.StorageState)
		return status, errors.New("Passphrase settings could not be changed. Go online and check the current passphrase before trying again.")
	}
	status := s.status()
	s.mu.Unlock()
	return status, nil
}

// quiesceSecurityLocked retires every runtime handle before a stable snapshot
// or backend replacement. It never closes a native dialog while holding mu.
func (s *Service) quiesceSecurityLocked(op *securityOperation, closeVault bool) error {
	if !s.currentSecurityLocked(op) {
		return errSecurity
	}
	op.quiesced = true
	s.stopAttachmentsLocked()
	if s.messages != nil {
		s.messages.Close()
		s.messages = nil
	}
	if s.invitations != nil {
		s.invitations.close()
		s.invitations = nil
	}
	if s.pairing != nil {
		if s.pairing.Close() != nil {
			return errSecurity
		}
		s.pairing = nil
	}
	s.fingerprint = ""
	s.state = "locked"
	if closeVault && s.vault != nil {
		if s.vault.Close() != nil {
			s.state = "unavailable"
			return errSecurity
		}
		s.vault = nil
		s.application = nil
	}
	return nil
}
func (s *Service) resumeSecurityLocked(op *securityOperation) error {
	if !s.currentSecurityLocked(op) || s.application != op.application || s.vault != op.vault {
		return errSecurity
	}
	local, err := s.application.Identity()
	if err != nil {
		return errSecurity
	}
	messages, pairing, invitations, err := makeControllers(local, s.application, s.revisions, s.events.enqueue, s.runtimeOptions, s.record)
	if err != nil {
		return errSecurity
	}
	s.messages, s.pairing, s.invitations = messages, pairing, invitations
	s.fingerprint = local.PublicKey().Fingerprint()
	s.state = "unlocked"
	s.startAttachmentsLocked()
	if s.runtimeOptions.AutoConnect {
		if contacts, e := messages.Contacts(); e == nil {
			for _, contact := range contacts {
				if contact.CanConnect {
					_, _ = messages.Connect(contact.ContactID)
				}
			}
		}
	}
	return nil
}

func (s *Service) ExportRecovery(secret string) (RecoveryExportResult, error) {
	pass := []byte(secret)
	defer clear(pass)
	if keymaterial.ValidateSecret(pass) != nil {
		return RecoveryExportResult{}, errors.New("Use a recovery passphrase of 12 to 1024 UTF-8 bytes")
	}
	s.mu.Lock()
	op, err := s.beginSecurityLocked(true)
	var choose func(context.Context) (string, error)
	if err == nil {
		choose = s.security.options.SaveRecovery
		if choose == nil {
			s.finishSecurityLocked(op)
			err = errSecurity
		}
	}
	s.mu.Unlock()
	if err != nil {
		return RecoveryExportResult{}, err
	}
	defer op.cancel()
	filename, err := choose(op.life)
	if err != nil || filename == "" {
		s.mu.Lock()
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		if err != nil {
			return RecoveryExportResult{}, errSecurity
		}
		return RecoveryExportResult{}, nil
	}
	if op.life.Err() != nil {
		s.mu.Lock()
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		return RecoveryExportResult{}, errSecurity
	}
	if !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
		s.mu.Lock()
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		return RecoveryExportResult{}, errSecurity
	}
	output, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		s.mu.Lock()
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		return RecoveryExportResult{}, errors.New("Choose a new recovery file name. Existing files are kept.")
	}
	created, _ := output.Stat()
	archiveComplete := false
	defer func() {
		_ = output.Close()
		if !archiveComplete && created != nil {
			if current, e := os.Lstat(filename); e == nil && current.Mode().IsRegular() && os.SameFile(created, current) {
				_ = os.Remove(filename)
			}
		}
	}()
	if created == nil || !created.Mode().IsRegular() {
		s.mu.Lock()
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		return RecoveryExportResult{}, errSecurity
	}
	s.mu.Lock()
	err = s.quiesceSecurityLocked(op, false)
	s.mu.Unlock()
	quiesced := err == nil
	if quiesced {
		s.securityHint(op, "locked")
		err = op.application.ExportRecovery(op.life, output, pass)
	}
	if err == nil {
		err = output.Sync()
	}
	if e := output.Close(); err == nil {
		err = e
	}
	archiveComplete = err == nil
	s.mu.Lock()
	if !s.currentSecurityLocked(op) {
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		return RecoveryExportResult{}, errSecurity
	}
	if !quiesced {
		s.finishSecurityLocked(op)
		status := s.lockStorageLocked()
		s.mu.Unlock()
		s.securityHint(nil, status.StorageState)
		return RecoveryExportResult{}, errSecurity
	}
	if s.resumeSecurityLocked(op) != nil {
		s.finishSecurityLocked(op)
		status := s.lockStorageLocked()
		s.mu.Unlock()
		s.securityHint(nil, status.StorageState)
		if archiveComplete {
			return RecoveryExportResult{Saved: true}, errors.New("The encrypted recovery file was saved, but local storage could not reopen. Go online to check local state.")
		}
		return RecoveryExportResult{}, errSecurity
	}
	s.finishSecurityLocked(op)
	s.mu.Unlock()
	s.securityHint(nil, "unlocked")
	if err != nil {
		return RecoveryExportResult{}, errors.New("The encrypted recovery file could not be saved. Existing local data remains on this device.")
	}
	return RecoveryExportResult{Saved: true}, nil
}

func (s *Service) StageRecoveryImport(secret string) (RecoveryImportPreview, error) {
	pass := []byte(secret)
	defer clear(pass)
	if keymaterial.ValidateSecret(pass) != nil {
		return RecoveryImportPreview{}, errors.New("Use a recovery passphrase of 12 to 1024 UTF-8 bytes")
	}
	s.mu.Lock()
	op, err := s.beginSecurityLocked(false)
	var choose func(context.Context) (string, error)
	if err == nil {
		choose = s.security.options.PickRecovery
		if choose == nil {
			s.finishSecurityLocked(op)
			err = errSecurity
		}
	}
	s.mu.Unlock()
	if err != nil {
		return RecoveryImportPreview{}, err
	}
	defer op.cancel()
	finishFailure := func() (RecoveryImportPreview, error) {
		s.mu.Lock()
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		return RecoveryImportPreview{}, errors.New("The recovery file could not be authenticated. Check the recovery passphrase and file.")
	}
	filename, err := choose(op.life)
	if err != nil {
		return finishFailure()
	}
	if filename == "" {
		s.mu.Lock()
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		return RecoveryImportPreview{}, nil
	}
	if op.life.Err() != nil {
		return finishFailure()
	}
	if !filepath.IsAbs(filename) {
		return finishFailure()
	}
	before, err := os.Lstat(filename)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > 256<<30 {
		return finishFailure()
	}
	input, err := os.Open(filename)
	if err != nil {
		return finishFailure()
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return finishFailure()
	}
	stop := context.AfterFunc(op.life, func() { _ = input.Close() })
	defer stop()
	capability, err := storage.StageRecovery(op.life, s.directory, input, pass)
	if err != nil {
		return finishFailure()
	}
	keep := false
	defer func() {
		if !keep {
			_ = capability.Close()
		}
	}()
	after, err := input.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || opened.ModTime() != after.ModTime() {
		return finishFailure()
	}
	summary, err := capability.InspectTarget(s.keys)
	if err != nil {
		return finishFailure()
	}
	handle, err := opaqueAttachmentID()
	if err != nil {
		return finishFailure()
	}
	s.mu.Lock()
	if !s.currentSecurityLocked(op) {
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		return RecoveryImportPreview{}, errSecurity
	}
	// At most one authenticated import can retain master material in memory.
	for old, entry := range s.security.imports {
		entry.timer.Stop()
		_ = entry.cap.Close()
		delete(s.security.imports, old)
	}
	entry := &securityImport{cap: capability, expires: time.Now().Add(10 * time.Minute)}
	entry.timer = time.AfterFunc(10*time.Minute, func() { _ = s.CancelRecoveryImport(handle) })
	s.security.imports[handle] = entry
	s.finishSecurityLocked(op)
	s.mu.Unlock()
	keep = true
	return RecoveryImportPreview{handle, summary.Contacts, summary.Messages, summary.Attachments, summary.SourceRequiresPassphrase, summary.ReplaceRequired, summary.CredentialResetRequired}, nil
}
func (s *Service) CancelRecoveryImport(handle string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.security == nil {
		return nil
	}
	entry := s.security.imports[handle]
	if entry == nil {
		return nil
	}
	entry.timer.Stop()
	delete(s.security.imports, handle)
	if entry.cap.Close() != nil {
		return errSecurity
	}
	return nil
}

func (s *Service) CommitRecoveryImport(handle, newAppSecret string, requirePassphrase, confirmReplace bool) (Status, error) {
	pass := []byte(newAppSecret)
	defer clear(pass)
	if requirePassphrase && keymaterial.ValidateSecret(pass) != nil || !requirePassphrase && len(pass) != 0 {
		return s.GetStatus(), errors.New("Use an app passphrase of 12 to 1024 UTF-8 bytes")
	}
	s.mu.Lock()
	if s.security == nil || s.security.imports[handle] == nil {
		s.mu.Unlock()
		return s.GetStatus(), errSecurity
	}
	entry := s.security.imports[handle]
	if time.Now().After(entry.expires) {
		entry.timer.Stop()
		delete(s.security.imports, handle)
		_ = entry.cap.Close()
		s.mu.Unlock()
		return s.GetStatus(), errSecurity
	}
	op, err := s.beginSecurityLocked(false)
	if err != nil {
		s.mu.Unlock()
		return s.GetStatus(), err
	}
	entry.timer.Stop()
	delete(s.security.imports, handle)
	defer entry.cap.Close()
	s.mu.Unlock()
	summary, err := entry.cap.InspectTarget(s.keys)
	if err != nil || (summary.ReplaceRequired || summary.CredentialResetRequired) && !confirmReplace {
		s.mu.Lock()
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		return s.GetStatus(), errors.New("Confirm replacement of local backend data before restoring")
	}
	s.mu.Lock()
	err = s.quiesceSecurityLocked(op, true)
	s.mu.Unlock()
	if err == nil {
		s.securityHint(op, "locked")
	}
	if err == nil && summary.CredentialResetRequired {
		err = CloseAppPreferences(s)
	}
	if err == nil {
		err = entry.cap.Commit(op.life, s.keys, pass, confirmReplace)
	}
	s.mu.Lock()
	if !s.currentSecurityLocked(op) {
		s.finishSecurityLocked(op)
		s.mu.Unlock()
		return s.GetStatus(), errSecurity
	}
	s.finishSecurityLocked(op)
	if err != nil {
		s.state = "unavailable"
		status := s.status()
		s.mu.Unlock()
		s.securityHint(nil, status.StorageState)
		return status, errors.New("Recovery could not be completed. Go online to check local state before trying again.")
	}
	status, openErr := s.unlockStorageLocked(pass)
	var preferences func() error
	if s.security != nil {
		preferences = s.security.options.PreferencesChanged
	}
	s.mu.Unlock()
	if summary.CredentialResetRequired && preferences != nil {
		if preferences() != nil {
			s.record(diagnostics.RuntimeWarning)
		}
	}
	s.securityHint(nil, status.StorageState)
	if openErr != nil {
		return status, errors.New("Recovery was installed, but local storage could not reopen. Check OS credential protection and the app passphrase.")
	}
	return status, nil
}
