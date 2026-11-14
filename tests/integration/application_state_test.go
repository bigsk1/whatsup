// This tests local encrypted identity/contact/chat coordination across real
// processes. No socket, reciprocal handshake or message transport is exercised.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"

	"whatsup/internal/chat"
	"whatsup/internal/desktop"
	"whatsup/internal/identity"
	"whatsup/internal/pairing"
	"whatsup/internal/storage"
)

func stateContact(t *testing.T, local, peer identity.PublicKey) *pairing.Contact {
	t.Helper()
	manager, err := pairing.NewManager(local, pairing.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	endpoint := tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()}, ServerDiscoPublic: tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()}, PresharedKey: tailcat.NewPresharedKey(), RegionID: 1}
	invite, err := manager.Create(string(endpoint.Addr()), 0)
	if err != nil {
		t.Fatal("synthetic invitation failed")
	}
	if _, err := manager.Reserve(invite.Link(), peer); err != nil {
		t.Fatal("synthetic reservation failed")
	}
	contact, err := manager.Approve(peer.Fingerprint())
	if err != nil {
		t.Fatal("synthetic approval failed")
	}
	t.Cleanup(contact.Revoke)
	return contact
}

type applicationChildInput struct {
	Directory   string
	Credential  string
	Fingerprint string
	Peer        identity.PublicKey
	Busy        bool
}

func TestApplicationStateChild(t *testing.T) {
	if os.Getenv("WHATSUP_APPLICATION_STATE_CHILD") != "1" {
		return
	}
	var input applicationChildInput
	if json.NewDecoder(os.Stdin).Decode(&input) != nil {
		t.Fatal("invalid synthetic child input")
	}
	vault, err := storage.Open(input.Directory, &processKeys{value: input.Credential})
	if input.Busy {
		if vault != nil {
			_ = vault.Close()
		}
		if !errors.Is(err, storage.ErrBusy) {
			t.Fatal("child acquired occupied application envelope")
		}
		return
	}
	if err != nil {
		t.Fatal("child could not open envelope")
	}
	defer vault.Close()
	application, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal("child could not authenticate envelope")
	}
	local, err := application.Identity()
	if err != nil || local.PublicKey().Fingerprint() != input.Fingerprint {
		t.Fatal("child identity changed")
	}
	// Reapproval is synthetic and explicit: stored metadata cannot restore live
	// authorization or a Contact after an application restart.
	contact := stateContact(t, local.PublicKey(), input.Peer)
	store, err := application.ChatStore(contact)
	if err != nil {
		t.Fatal("child contact pin lost")
	}
	conversation, err := chat.Open(store, contact, chat.Options{})
	if err != nil || len(conversation.Messages()) != 1 || len(conversation.Outbox()) != 1 || conversation.Messages()[0].Text != "synthetic envelope integration message" {
		t.Fatal("child chat/outbox lost")
	}
}

func TestEncryptedApplicationEnvelopeChatIdentityAndIndependentRestart(t *testing.T) {
	dir, keys := filepath.Join(t.TempDir(), "private"), &processKeys{}
	vault, err := storage.Open(dir, keys)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	application, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	local, _ := application.Identity()
	peer, _ := identity.Generate()
	contact := stateContact(t, local.PublicKey(), peer.PublicKey())
	if err := application.PinApprovedContact(contact); err != nil {
		t.Fatal(err)
	}
	store, err := application.ChatStore(contact)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := chat.Open(store, contact, chat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.Queue("synthetic envelope integration message"); err != nil {
		t.Fatal(err)
	}
	if len(conversation.Outbox()) != 1 {
		t.Fatal("queue was not persisted")
	}
	fingerprint := local.PublicKey().Fingerprint()
	run := func(busy bool) {
		t.Helper()
		payload, err := json.Marshal(applicationChildInput{Directory: dir, Credential: keys.value, Fingerprint: fingerprint, Peer: peer.PublicKey(), Busy: busy})
		if err != nil {
			t.Fatal("synthetic child input failed")
		}
		defer clear(payload)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestApplicationStateChild$", "-test.count=1")
		command.Env = append(os.Environ(), "WHATSUP_APPLICATION_STATE_CHILD=1")
		command.Stdin = bytes.NewReader(payload)
		output, err := command.CombinedOutput()
		clear(output)
		if err != nil {
			t.Fatal("independent application state assertion failed")
		}
	}
	run(true)
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	run(false)
	// Desktop unlock uses the same envelope and cannot replace chat while
	// loading identity. The existing bridge still advertises no messenger.
	service := desktop.New(dir, keys)
	defer service.ServiceShutdown()
	status, err := service.UnlockStorage()
	if err != nil || status.IdentityFingerprint != fingerprint || status.MessagingAvailable || !status.PairingAvailable || status.NetworkState != "unavailable" {
		t.Fatal("desktop did not preserve coordinated state boundary")
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	run(false)
	ciphertext, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil || bytes.Contains(ciphertext, []byte("synthetic envelope integration message")) {
		t.Fatal("plaintext message persisted")
	}
}

func TestApplicationScopedChatReceiveAcknowledgementAndRestart(t *testing.T) {
	dirA, dirB := filepath.Join(t.TempDir(), "private"), filepath.Join(t.TempDir(), "private")
	keysA, keysB := &processKeys{}, &processKeys{}
	open := func(dir string, keys *processKeys) (*storage.ApplicationState, *identity.Identity) {
		t.Helper()
		vault, err := storage.Open(dir, keys)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = vault.Close() })
		app, err := storage.OpenApplication(vault)
		if err != nil {
			t.Fatal(err)
		}
		local, err := app.Identity()
		if err != nil {
			t.Fatal(err)
		}
		return app, local
	}
	appA, identityA := open(dirA, keysA)
	appB, identityB := open(dirB, keysB)
	keyA, keyB := identityA.PublicKey(), identityB.PublicKey()
	contactA, contactB := stateContact(t, keyA, keyB), stateContact(t, keyB, keyA)
	if err := appA.PinApprovedContact(contactA); err != nil {
		t.Fatal(err)
	}
	if err := appB.PinApprovedContact(contactB); err != nil {
		t.Fatal(err)
	}
	conversation := func(app *storage.ApplicationState, contact *pairing.Contact) *chat.Conversation {
		t.Helper()
		store, err := app.ChatStore(contact)
		if err != nil {
			t.Fatal(err)
		}
		c, err := chat.Open(store, contact, chat.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := conversation(appA, contactA), conversation(appB, contactB)
	if _, err := a.Queue("synthetic acknowledged envelope message"); err != nil {
		t.Fatal(err)
	}
	frame, due, err := a.NextAttempt(keyB)
	if err != nil || !due {
		t.Fatal("durable queued frame unavailable")
	}
	ack, err := b.Receive(keyA, frame)
	if err != nil {
		t.Fatal("receiver did not durably accept scoped frame")
	}
	// Drop this acknowledgement and restart both actual encrypted stores.
	if err := appA.Close(); err != nil {
		t.Fatal(err)
	}
	if err := appB.Close(); err != nil {
		t.Fatal(err)
	}
	appA, identityA = open(dirA, keysA)
	appB, identityB = open(dirB, keysB)
	if !identityA.PublicKey().Equal(keyA) || !identityB.PublicKey().Equal(keyB) {
		t.Fatal("chat persistence replaced identity")
	}
	contactA, contactB = stateContact(t, keyA, keyB), stateContact(t, keyB, keyA)
	a, b = conversation(appA, contactA), conversation(appB, contactB)
	if len(a.Outbox()) != 1 || len(b.Messages()) != 1 {
		t.Fatal("restart lost durable queue/history")
	}
	repeatedAck, err := b.Receive(keyA, frame)
	if err != nil || repeatedAck.ID != ack.ID || len(b.Messages()) != 1 {
		t.Fatal("lost acknowledgement duplicated history")
	}
	if err := a.Acknowledge(keyB, repeatedAck); err != nil {
		t.Fatal("authenticated acknowledgement failed")
	}
	if len(a.Outbox()) != 0 || len(a.Messages()) != 1 || !a.Messages()[0].Delivered {
		t.Fatal("acknowledgement did not update scoped durable state")
	}
	if err := appA.Close(); err != nil {
		t.Fatal(err)
	}
	appA, identityA = open(dirA, keysA)
	contactA = stateContact(t, identityA.PublicKey(), keyB)
	a = conversation(appA, contactA)
	if len(a.Outbox()) != 0 || len(a.Messages()) != 1 || !a.Messages()[0].Delivered {
		t.Fatal("acknowledgement did not survive restart")
	}
}

func TestApplicationScopedChatRejectsSharedHandleAndSerializesRecordStores(t *testing.T) {
	dir, keys := filepath.Join(t.TempDir(), "private"), &processKeys{}
	vault, err := storage.Open(dir, keys)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	app, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	local, _ := app.Identity()
	peer, _ := identity.Generate()
	contact := stateContact(t, local.PublicKey(), peer.PublicKey())
	if err := app.PinApprovedContact(contact); err != nil {
		t.Fatal(err)
	}
	store, _ := app.ChatStore(contact)
	a, err := chat.Open(store, contact, chat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Open(store, contact, chat.Options{}); !errors.Is(err, storage.ErrScopeUsed) {
		t.Fatal("shared store opened two independent conversation snapshots")
	}
	staleStore, _ := app.ChatStore(contact)
	stale, err := chat.Open(staleStore, contact, chat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Queue("synthetic newer queue item"); err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Queue("synthetic concurrent queue item"); err != nil {
		t.Fatal("independent per-record transaction was rejected")
	}
	freshStore, _ := app.ChatStore(contact)
	fresh, err := chat.Open(freshStore, contact, chat.Options{})
	if err != nil || len(fresh.Messages()) != 2 || fresh.Messages()[0].Text != "synthetic newer queue item" || fresh.Messages()[1].Text != "synthetic concurrent queue item" || len(fresh.Outbox()) != 2 {
		t.Fatal("conversation conflict lost newest durable content")
	}
}
