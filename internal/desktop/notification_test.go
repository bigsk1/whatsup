package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tailscale/tailcat"
	"net"
	"strings"
	"tailscale.com/types/key"
	"testing"
	"time"
	"unicode/utf8"

	"whatsup/internal/messaging"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type notificationListener struct{ net.Listener }

func (l notificationListener) Accept(ctx context.Context) (net.Conn, error) {
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	return l.Listener.Accept()
}

func notificationFixture(t *testing.T) (*Service, messaging.Incoming) {
	t.Helper()
	local, _, incoming := notificationPairFixture(t)
	return local, incoming
}

func notificationPairFixture(t *testing.T) (*Service, *Service, messaging.Incoming) {
	t.Helper()
	s, peer := unlockedInvitationService(t), unlockedInvitationService(t)
	local, _ := s.application.Identity()
	remote, _ := peer.application.Identity()
	node := tailcat.NewPrivateKey()
	node.Public.RegionID = 900
	client := key.NewNode()
	nodePrivate, _ := node.Private.MarshalText()
	clientPrivate, _ := client.MarshalText()
	defer clear(nodePrivate)
	defer clear(clientPrivate)
	record := storage.PendingRecord{Version: 2, ID: base64.RawURLEncoding.EncodeToString([]byte("synthetic-id-001")), Role: "inviter", Local: local.PublicKey(), Peer: remote.PublicKey(), LocalNodePrivate: string(nodePrivate), LocalNode: node.Private.Public().String(), PeerNode: client.Public().String(), PresharedKey: base64.RawURLEncoding.EncodeToString(node.Public.PresharedKey[:]), RelayServiceURL: disposableRelayURL, RelayFallbackConsent: true, ServerEndpoint: string(node.Public.Addr())}
	other := record
	other.Role = "joiner"
	other.Local, other.Peer = record.Peer, record.Local
	other.LocalNodePrivate, other.LocalNode, other.PeerNode = string(clientPrivate), record.PeerNode, record.LocalNode
	for _, item := range []struct {
		service *Service
		record  storage.PendingRecord
	}{{s, record}, {peer, other}} {
		if item.service.application.SavePendingContact(item.record) != nil {
			t.Fatal("fixture pending save failed")
		}
		if _, err := item.service.application.CommitPendingContact(item.record); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	factories := messaging.Factories{Profile: func(context.Context, string) (*transport.RelayProfile, error) { return nil, nil }, Listen: func(context.Context, storage.PendingRecord, *transport.RelayProfile) (messaging.Listener, error) {
		return notificationListener{listener}, nil
	}, Dial: func(ctx context.Context, _ storage.PendingRecord, _ *transport.RelayProfile) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}}
	notices := make(chan messaging.Incoming, 2)
	s.messages.Close()
	s.messages, err = messaging.NewRegistry(local, s.application, messaging.RegistryOptions{Factories: factories, OnIncoming: func(incoming messaging.Incoming) { notices <- incoming }})
	if err != nil {
		t.Fatal(err)
	}
	peer.messages.Close()
	peer.messages, err = messaging.NewRegistry(remote, peer.application, messaging.RegistryOptions{Factories: factories})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.messages.Connect(record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.messages.Connect(record.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for s.messages.Snapshot(record.ID).State != "connected" || peer.messages.Snapshot(record.ID).State != "connected" {
		select {
		case <-deadline:
			t.Fatal("fixture mutual authentication timed out")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := s.SetContactName(record.ID, "Synthetic private contact"); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.messages.Send(record.ID, "Synthetic private message\nwith joining 👩‍💻"); err != nil {
		t.Fatal(err)
	}
	select {
	case notice := <-notices:
		return s, peer, notice
	case <-time.After(time.Second):
		t.Fatal("new durable receive did not yield a native notice receipt")
		return nil, nil, messaging.Incoming{}
	}
}

func TestNotificationDefaultsAndIndependentEncryptedOptIns(t *testing.T) {
	s, incoming := notificationFixture(t)
	for _, test := range []struct{ name, preview bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		prefs, err := s.GetAppPreferences()
		if err != nil {
			t.Fatal(err)
		}
		prefs.Notifications, prefs.NotificationName, prefs.NotificationPreview = true, test.name, test.preview
		if _, err := s.SetAppPreferences(prefs); err != nil {
			t.Fatal(err)
		}
		var shown Notification
		if err := WithIncomingNotification(s, incoming, func(n Notification) error { shown = n; return nil }); err != nil {
			t.Fatal(err)
		}
		if !test.name && shown.Name != "WhatsUp" || test.name && shown.Name != "Synthetic private contact" {
			t.Fatal("name privacy opt-in changed")
		}
		if !test.preview && shown.Body != "New message" || test.preview && !strings.Contains(shown.Body, "Synthetic private message with joining") {
			t.Fatal("preview privacy opt-in changed")
		}
		if strings.Contains(shown.Body, "\n") {
			t.Fatal("notification retained raw controls")
		}
	}
	prefs, _ := s.GetAppPreferences()
	prefs.Notifications = false
	if _, err := s.SetAppPreferences(prefs); err != nil {
		t.Fatal(err)
	}
	if err := WithIncomingNotification(s, incoming, func(Notification) error { t.Fatal("disabled notifications sent content"); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{Notification{Name: "SYNTHETIC-SECRET", Body: "SYNTHETIC-SECRET"}, incoming} {
		if strings.Contains(fmt.Sprintf("%#v", value), "SYNTHETIC-SECRET") {
			t.Fatal("notification formatting leaked content")
		}
		if wire, err := json.Marshal(value); err == nil || len(wire) != 0 {
			t.Fatal("backend notification became serializable")
		}
	}
}

func TestNotificationRejectsMissingDeletedForgottenAndLockedAuthority(t *testing.T) {
	s, incoming := notificationFixture(t)
	noSend := func(Notification) error { t.Fatal("stale authority sent notification"); return nil }
	missing := incoming
	missing.MessageID = "AQEBAQEBAQEBAQEBAQEBAQ"
	if WithIncomingNotification(s, missing, noSend) == nil {
		t.Fatal("missing record became notice")
	}
	if _, err := s.DeleteMessage(incoming.ContactID, incoming.MessageID); err != nil {
		t.Fatal(err)
	}
	if WithIncomingNotification(s, incoming, noSend) == nil {
		t.Fatal("deleted record became notice")
	}
	if _, err := s.ForgetContact(incoming.ContactID); err != nil {
		t.Fatal(err)
	}
	if WithIncomingNotification(s, incoming, noSend) == nil {
		t.Fatal("forgotten contact became notice")
	}
	s.LockStorage()
	if WithIncomingNotification(s, incoming, noSend) == nil {
		t.Fatal("locked storage became notice")
	}
}

func TestNotificationSendSerializesOfflineAndReturnsOnlyFiniteErrors(t *testing.T) {
	s, incoming := notificationFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- WithIncomingNotification(s, incoming, func(Notification) error {
			close(entered)
			<-release
			return errors.New("SYNTHETIC PRIVATE NATIVE ERROR")
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("notification never entered bounded send")
	}
	locked := make(chan struct{})
	go func() { s.LockStorage(); close(locked) }()
	select {
	case <-locked:
		t.Fatal("Go offline overtook current bounded send")
	default:
	}
	close(release)
	if err := <-result; !errors.Is(err, ErrNotification) || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("raw native error leaked")
	}
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("Go offline did not resume after notification")
	}
	text := notificationText(strings.Repeat("界", 400)+"\n\u202e", 1024)
	if !utf8.ValidString(text) || len(text) > 1024 || strings.ContainsAny(text, "\n\u202e") {
		t.Fatal("preview bound split Unicode or retained controls")
	}
}
