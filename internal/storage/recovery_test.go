package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"whatsup/internal/keymaterial"
	"whatsup/internal/messagestore"
)

type recoveryFixture struct {
	archive    []byte
	record     PendingRecord
	seed       [32]byte
	master     []byte
	messageIDs []string
	offerID    string
	body       []byte
	keys       *memoryKeys
	source     *ApplicationState
}

func makeRecoveryFixture(t *testing.T) recoveryFixture {
	return makeRecoveryFixtureWithKeys(t, &memoryKeys{})
}
func makeRecoveryFixtureWithKeys(t *testing.T, keys *memoryKeys) recoveryFixture {
	t.Helper()
	_, app := openApplicationTest(t, privateDirectory(t), keys)
	record, _, _, _, chat, _ := prepareForgetStorage(t, app, 143)
	identity, _ := app.Identity()
	scope, err := app.AttachmentStore(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("synthetic recovery private file"), 8000)
	offer := storageAttachmentOffer(42, body)
	if err := scope.Stage(context.Background(), offer, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if scope.QueueOutgoing(offer.ID, true) != nil {
		t.Fatal("queue")
	}
	if _, err := app.SetProfile("Synthetic recovery profile", true); err != nil {
		t.Fatal(err)
	}
	if err := app.SetContactName(record.ID, "Synthetic recovery contact"); err != nil {
		t.Fatal(err)
	}
	ids, err := chat.PendingIDs()
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := app.ExportRecovery(context.Background(), &archive, testPassphrase); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive.Bytes(), body[:80]) || bytes.Contains(archive.Bytes(), []byte("Synthetic recovery profile")) {
		t.Fatal("archive plaintext")
	}
	return recoveryFixture{archive: archive.Bytes(), record: record, seed: identity.Seed(), master: bytes.Clone(app.vault.master), messageIDs: ids, offerID: offer.ID, body: body, source: app}
}
func assertRestoredFixture(t *testing.T, a *ApplicationState, f recoveryFixture) {
	t.Helper()
	identity, err := a.Identity()
	if err != nil || identity.Seed() != f.seed || !bytes.Equal(a.vault.master, f.master) {
		t.Fatal("restore changed seed/master")
	}
	records, err := a.CommittedContacts()
	if err != nil || len(records) != 1 || records[0] != f.record {
		t.Fatal("restore changed saved credentials")
	}
	pin := &identifiedPin{approvedPin: &approvedPin{local: f.record.Local, peer: f.record.Peer}, id: f.record.ID}
	chat, err := a.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := chat.PendingIDs()
	if err != nil || len(ids) != len(f.messageIDs) || ids[0] != f.messageIDs[0] {
		t.Fatal("encrypted outbox did not restore")
	}
	scope, err := a.AttachmentStore(f.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := scope.OpenOutgoing(context.Background(), f.offerID)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(actual, f.body) {
		t.Fatal("attachment did not restore")
	}
	profile, err := a.Profile()
	if err != nil || profile.DisplayName != "Synthetic recovery profile" {
		t.Fatal("profile lost")
	}
}

func TestPassphraseAfterOwnRecoveryProtectsNestedMigrationBackup(t *testing.T) {
	keys := &memoryKeys{}
	f := makeRecoveryFixtureWithKeys(t, keys)
	dir := f.source.vault.directory
	backupName := "pre-migration-0123456789abcdef0123456789abcdef.vault"
	original, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil || os.WriteFile(filepath.Join(dir, backupName), original, 0600) != nil {
		t.Fatal("prepare migration backup", err)
	}
	f.source.Close()
	staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(f.archive), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if err := staged.Commit(context.Background(), keys, nil, true); err != nil {
		t.Fatal(err)
	}
	_, app := openApplicationTest(t, dir, keys)
	assertRestoredFixture(t, app, f)
	entries, _ := os.ReadDir(dir)
	var backup string
	for _, entry := range entries {
		if recoveryRollbackName.MatchString(entry.Name()) {
			path := filepath.Join(dir, entry.Name(), backupName)
			if _, err := os.Stat(path); err == nil {
				backup = path
			}
		}
	}
	if backup == "" {
		t.Fatal("recovery did not retain the migration backup")
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	scratch := scratchVault(app.vault)
	plain, err := decodeRootWithSecret(scratch, data, nil)
	clear(plain)
	same := bytes.Equal(scratch.master, app.vault.master)
	clearScratch(scratch)
	if err != nil || !same {
		t.Fatal("fixture does not reproduce an OS-only path to the current master")
	}
	if err := app.SetPassphrase(context.Background(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	scratch = scratchVault(app.vault)
	plain, err = decodeRootWithSecret(scratch, data, nil)
	clear(plain)
	clearScratch(scratch)
	if !errors.Is(err, ErrPassphraseRequired) {
		t.Fatal("nested migration backup still exposes the current master with only the OS credential")
	}
	scratch = scratchVault(app.vault)
	plain, err = decodeRootWithSecret(scratch, data, testPassphrase)
	clear(plain)
	same = bytes.Equal(scratch.master, app.vault.master)
	clearScratch(scratch)
	if err != nil || !same {
		t.Fatal("backup did not preserve its master under the new protection")
	}
	assertRestoredFixture(t, app, f)
	app.Close()
	_, app = openProtectedTest(t, dir, keys, testPassphrase)
	assertRestoredFixture(t, app, f)
}

func TestRecoveryExportRestorePreservesHierarchyAndPendingDelivery(t *testing.T) {
	f := makeRecoveryFixture(t)
	target, keys := privateDirectory(t), &memoryKeys{}
	staged, err := StageRecovery(context.Background(), target, bytes.NewReader(f.archive), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	summary, err := staged.InspectTarget(keys)
	if err != nil || summary.Contacts != 1 || summary.Attachments != 1 || summary.CredentialResetRequired || summary.ReplaceRequired || keys.writes != 0 {
		t.Fatal("staging changed target credential or summary")
	}
	if err := staged.Commit(context.Background(), keys, testPassphrase, false); err != nil {
		t.Fatal(err)
	}
	if keys.writes != 1 {
		t.Fatal("new target credential not established exactly once")
	}
	_, restored := openProtectedTest(t, target, keys, testPassphrase)
	assertRestoredFixture(t, restored, f)
	if _, err := os.Stat(filepath.Join(target, recoveryJournalFilename)); !os.IsNotExist(err) {
		t.Fatal("completed restore retained journal")
	}
	if err := staged.Commit(context.Background(), keys, nil, true); err == nil {
		t.Fatal("import capability reused")
	}
}

func TestRecoveryWrongSecretAndTamperingLeaveTargetUnchanged(t *testing.T) {
	f := makeRecoveryFixture(t)
	for _, kind := range []string{"wrong-secret", "tamper", "truncate"} {
		t.Run(kind, func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			_, app := openApplicationTest(t, dir, keys)
			before, err := os.ReadFile(filepath.Join(dir, "state.vault"))
			if err != nil {
				t.Fatal(err)
			}
			data := bytes.Clone(f.archive)
			secret := testPassphrase
			switch kind {
			case "wrong-secret":
				secret = []byte("synthetic wrong recovery secret")
			case "tamper":
				data[len(data)/2] ^= 1
			case "truncate":
				data = data[:len(data)-9]
			}
			if staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(data), secret); err == nil {
				staged.Close()
				t.Fatal("invalid archive authenticated")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
			if !bytes.Equal(before, after) || keys.writes != 1 {
				t.Fatal("invalid archive changed target")
			}
			entries, _ := os.ReadDir(dir)
			for _, entry := range entries {
				if len(entry.Name()) > 16 && entry.Name()[:16] == ".recovery-stage-" {
					t.Fatal("failed staging retained secret-bearing state")
				}
			}
			app.Close()
		})
	}
}

func TestRecoveryExplicitLostCredentialPreservesOldCiphertext(t *testing.T) {
	f := makeRecoveryFixture(t)
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, old := openApplicationTest(t, dir, keys)
	oldRoot, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
	old.Close()
	prefs := []byte("synthetic unreadable encrypted preference fixture")
	if os.WriteFile(filepath.Join(dir, "app-preferences.vault"), prefs, 0600) != nil {
		t.Fatal("prefs")
	}
	keys.value = ""
	writes := keys.writes
	staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(f.archive), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	summary, err := staged.InspectTarget(keys)
	if err != nil || !summary.ReplaceRequired || !summary.CredentialResetRequired {
		t.Fatal("lost credential not explicit")
	}
	if err := staged.Commit(context.Background(), keys, nil, false); !errors.Is(err, ErrRecoveryReplace) || keys.writes != writes {
		t.Fatal("unconfirmed recovery replaced credential")
	}
	if err := staged.Commit(context.Background(), keys, testPassphrase, true); err != nil {
		t.Fatal(err)
	}
	_, restored := openProtectedTest(t, dir, keys, testPassphrase)
	assertRestoredFixture(t, restored, f)
	entries, _ := os.ReadDir(dir)
	found := false
	for _, entry := range entries {
		if recoveryRollbackName.MatchString(entry.Name()) {
			root, _ := os.ReadFile(filepath.Join(dir, entry.Name(), "state.vault"))
			savedPrefs, _ := os.ReadFile(filepath.Join(dir, entry.Name(), "app-preferences.vault"))
			if bytes.Equal(root, oldRoot) && bytes.Equal(savedPrefs, prefs) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("lost-key rollback ciphertext not preserved")
	}
}

func TestRecoveryInterruptedInstallReconcilesBeforeIdentityAccess(t *testing.T) {
	f := makeRecoveryFixture(t)
	for _, step := range []string{"journal", "credential", "protected", "messages.sqlite", "attachments", "state.vault"} {
		t.Run(step, func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			_, old := openApplicationTest(t, dir, keys)
			old.Close()
			staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(f.archive), testPassphrase)
			if err != nil {
				t.Fatal(err)
			}
			staged.testStep = func(at string) error {
				if at == step {
					return errors.New("synthetic interrupted restore")
				}
				return nil
			}
			if err := staged.Commit(context.Background(), keys, testPassphrase, true); err == nil {
				t.Fatal("fault did not interrupt install")
			}
			hint, err := InspectProtectionWithKeys(dir, keys)
			if err != nil || !hint.RequiresPassphrase {
				t.Fatal("pending journal did not advertise intended app protection")
			}
			staged.Close()
			_, restored := openProtectedTest(t, dir, keys, testPassphrase)
			assertRestoredFixture(t, restored, f)
			if _, err := os.Stat(filepath.Join(dir, recoveryJournalFilename)); !os.IsNotExist(err) {
				t.Fatal("interrupted journal was not finished")
			}
		})
	}
}

func TestRecoveryCrossOSFixture(t *testing.T) {
	filename, mode := os.Getenv("WHATSUP_RECOVERY_FIXTURE"), os.Getenv("WHATSUP_RECOVERY_FIXTURE_MODE")
	if filename == "" || mode == "" {
		t.Skip("explicit synthetic cross-OS exchange")
	}
	if !filepath.IsAbs(filename) {
		t.Fatal("absolute fixture path required")
	}
	if mode == "write" {
		f := makeRecoveryFixture(t)
		if err := os.WriteFile(filename, f.archive, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if mode != "read" {
		t.Fatal("unsupported fixture mode")
	}
	archive, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	dir, keys := privateDirectory(t), &memoryKeys{}
	staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(archive), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	f := recoveryFixture{archive: archive, record: PendingRecord(staged.portable.State.CommittedContacts[0]), master: bytes.Clone(staged.master), body: bytes.Repeat([]byte("synthetic recovery private file"), 8000)}
	copy(f.seed[:], staged.portable.State.Identity.Seed)
	derived, err := keymaterial.Derive(staged.master, "message-store/v1")
	if err != nil {
		t.Fatal(err)
	}
	db, err := messagestore.Open(staged.stage, derived, messagestore.Options{})
	clear(derived)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := db.Scope(staged.portable.State.Contacts[0].StoreID)
	if err != nil {
		t.Fatal(err)
	}
	f.messageIDs, err = scope.PendingIDs()
	if err != nil {
		t.Fatal(err)
	}
	entries, _, _, err := scope.Attachments().Page(0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatal("cross-platform attachment index missing")
	}
	f.offerID = entries[0].Record.Offer.ID
	db.Close()
	if err := staged.Commit(context.Background(), keys, testPassphrase, false); err != nil {
		t.Fatal(err)
	}
	_, app := openProtectedTest(t, dir, keys, testPassphrase)
	assertRestoredFixture(t, app, f)
}

func TestRecoveryCrashHelper(t *testing.T) {
	step := os.Getenv("WHATSUP_RECOVERY_CRASH_STEP")
	if step == "" {
		t.Skip("child-process crash boundary")
	}
	dir, archivePath := os.Getenv("WHATSUP_RECOVERY_CRASH_TARGET"), os.Getenv("WHATSUP_RECOVERY_CRASH_ARCHIVE")
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	keys := &memoryKeys{value: base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{189}, 32))}
	staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(archive), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	staged.testStep = func(at string) error {
		if at == step {
			os.Exit(87)
		}
		return nil
	}
	secret := testPassphrase
	if os.Getenv("WHATSUP_RECOVERY_CRASH_PROTECT") == "0" {
		secret = nil
	}
	if err := staged.Commit(context.Background(), keys, secret, true); err != nil {
		t.Fatal(err)
	}
	t.Fatal("child did not reach crash boundary")
}

func TestRecoveryActualProcessCrashKeepsCoherentInstalledIdentity(t *testing.T) {
	f := makeRecoveryFixture(t)
	archivePath := filepath.Join(t.TempDir(), "synthetic-recovery.wup")
	if os.WriteFile(archivePath, f.archive, 0600) != nil {
		t.Fatal("archive")
	}
	for _, step := range []string{"journal", "messages.sqlite", "state.vault"} {
		t.Run(step, func(t *testing.T) {
			dir := privateDirectory(t)
			keys := &memoryKeys{value: base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{189}, 32))}
			_, old := openApplicationTest(t, dir, keys)
			old.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryCrashHelper$")
			cmd.Env = append(os.Environ(), "WHATSUP_RECOVERY_CRASH_STEP="+step, "WHATSUP_RECOVERY_CRASH_TARGET="+dir, "WHATSUP_RECOVERY_CRASH_ARCHIVE="+archivePath)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 87 {
				t.Fatalf("crash helper did not stop at boundary: %v %s", err, output)
			}
			_, restored := openProtectedTest(t, dir, keys, testPassphrase)
			assertRestoredFixture(t, restored, f)
		})
	}
}

func TestRecoverySameMasterRewrapsOldProtectedRollback(t *testing.T) {
	f := makeRecoveryFixture(t)
	app := f.source
	dir, keys := app.vault.directory, &memoryKeys{value: base64.RawStdEncoding.EncodeToString(app.vault.key)}
	if err := app.SetPassphrase(context.Background(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	app.Close()
	staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(f.archive), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	secretB := []byte("synthetic recovery replacement secret B")
	if err := staged.Commit(context.Background(), keys, secretB, true); err != nil {
		t.Fatal(err)
	}
	_, restored := openProtectedTest(t, dir, keys, secretB)
	assertRestoredFixture(t, restored, f)
	entries, _ := os.ReadDir(dir)
	found := false
	for _, e := range entries {
		if !recoveryRollbackName.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "state.vault"))
		if err != nil {
			t.Fatal(err)
		}
		scratch := scratchVault(restored.vault)
		if _, err := decodeRootWithSecret(scratch, data, testPassphrase); !errors.Is(err, ErrPassphrase) {
			t.Fatal("rollback retained historical secret A")
		}
		clearScratch(scratch)
		scratch = scratchVault(restored.vault)
		plain, err := decodeRootWithSecret(scratch, data, secretB)
		clear(plain)
		clearScratch(scratch)
		if err != nil {
			t.Fatal("rollback did not use replacement secret B")
		}
		found = true
	}
	if !found {
		t.Fatal("no preserved rollback root")
	}
}

func TestRecoveryUnjournaledCandidateCrashCannotBypassLaterProtection(t *testing.T) {
	f := makeRecoveryFixture(t)
	dir, keys := f.source.vault.directory, &memoryKeys{value: base64.RawStdEncoding.EncodeToString(f.source.vault.key)}
	f.source.Close()
	staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(f.archive), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	staged.testStep = func(at string) error {
		if at == "candidate" {
			return errors.New("synthetic crash before journal")
		}
		return nil
	}
	if err := staged.Commit(context.Background(), keys, nil, true); err == nil {
		t.Fatal("candidate fault not reached")
	}
	// A process death skips Close, retaining the prepared root. Keep this exact
	// directory alive to exercise the same protection walk after reopen.
	stagePath := staged.stage
	staged.committed = true
	staged.Close()
	_, app := openApplicationTest(t, dir, keys)
	if err := app.SetPassphrase(context.Background(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(stagePath, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	scratch := scratchVault(app.vault)
	if _, err := scratch.loadRootLocked(data[len(rootHeader):]); !errors.Is(err, ErrPassphraseRequired) {
		t.Fatal("unjournaled staged root retained OS-only master path")
	}
	clearScratch(scratch)
}

func TestRecoveryActualCandidateCrashThenEnableRemovesOSOnlyPath(t *testing.T) {
	keys := &memoryKeys{value: base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{189}, 32))}
	f := makeRecoveryFixtureWithKeys(t, keys)
	dir := f.source.vault.directory
	f.source.Close()
	archivePath := filepath.Join(t.TempDir(), "synthetic-candidate.wup")
	if os.WriteFile(archivePath, f.archive, 0600) != nil {
		t.Fatal("archive")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryCrashHelper$")
	cmd.Env = append(os.Environ(), "WHATSUP_RECOVERY_CRASH_STEP=candidate", "WHATSUP_RECOVERY_CRASH_PROTECT=0", "WHATSUP_RECOVERY_CRASH_TARGET="+dir, "WHATSUP_RECOVERY_CRASH_ARCHIVE="+archivePath)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 87 {
		t.Fatalf("candidate helper did not exit: %v %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(dir, recoveryJournalFilename)); !os.IsNotExist(err) {
		t.Fatal("candidate boundary already had journal")
	}
	_, app := openApplicationTest(t, dir, keys)
	secretB := []byte("synthetic post-crash protection B")
	before, _ := os.ReadDir(dir)
	osOnlyCandidate := false
	for _, entry := range before {
		if !recoveryStageName.MatchString(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name(), "state.vault"))
		if err != nil {
			t.Fatal(err)
		}
		root, err := decodeRoot(data[len(rootHeader):])
		if err != nil || root.Mode != "os-only" {
			t.Fatal("crash fixture was not an OS-only candidate")
		}
		osOnlyCandidate = true
	}
	if !osOnlyCandidate {
		t.Fatal("crashed OS-only candidate absent")
	}
	if err := app.SetPassphrase(context.Background(), nil, secretB); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	found := false
	for _, entry := range entries {
		if !recoveryStageName.MatchString(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name(), "state.vault"))
		if err != nil {
			t.Fatal(err)
		}
		scratch := scratchVault(app.vault)
		if _, err := scratch.loadRootLocked(data[len(rootHeader):]); !errors.Is(err, ErrPassphraseRequired) {
			t.Fatal("crashed candidate retained OS-only route")
		}
		clearScratch(scratch)
		scratch = scratchVault(app.vault)
		plain, err := decodeRootWithSecret(scratch, data, secretB)
		clear(plain)
		clearScratch(scratch)
		if err != nil {
			t.Fatal("crashed candidate did not rotate to B")
		}
		found = true
	}
	if !found {
		t.Fatal("crashed candidate missing")
	}
}

func TestRecoveryCancelledInstallKeepsAuthenticatedResumableJournal(t *testing.T) {
	f := makeRecoveryFixture(t)
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, old := openApplicationTest(t, dir, keys)
	old.Close()
	staged, err := StageRecovery(context.Background(), dir, bytes.NewReader(f.archive), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	staged.testStep = func(step string) error {
		if step == "credential" {
			cancel()
		}
		return nil
	}
	if err := staged.Commit(ctx, keys, testPassphrase, true); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation was not retained")
	}
	staged.Close()
	if _, err := os.Stat(filepath.Join(dir, recoveryJournalFilename)); err != nil {
		t.Fatal("cancelled install lost reconciliation state")
	}
	_, restored := openProtectedTest(t, dir, keys, testPassphrase)
	assertRestoredFixture(t, restored, f)
}
