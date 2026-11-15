//go:build linux

package notify

import (
	"context"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestLinuxNotificationEscapesMarkupAndRoutesOnlyKnownExplicitAction(t *testing.T) {
	notice := syntheticNotice()
	notice.Title = "Synthetic label"
	notice.Body = "<b>synthetic</b> & <img src='https://invalid.example'>"
	args := notificationArgs(notice)
	if len(args) != 8 || args[4] != "&lt;b&gt;synthetic&lt;/b&gt; &amp; &lt;img src=&#39;https://invalid.example&#39;&gt;" {
		t.Fatal("DBus notice retained executable markup")
	}
	if hints, ok := args[6].(map[string]dbus.Variant); !ok || len(hints) != 0 {
		t.Fatal("notice added file/category/preference hints")
	}
	var activations []string
	l := &linuxBackend{owner: ":1.4", lookupOwner: func() (string, error) { return ":1.4", nil }, ids: map[uint32]string{7: notice.ContactID}, activate: func(id string) { activations = append(activations, id) }}
	l.handle(&dbus.Signal{Sender: ":1.4", Path: daemonPath, Name: daemon + ".NotificationClosed", Body: []interface{}{uint32(99), uint32(2)}})
	l.handle(&dbus.Signal{Sender: ":1.4", Path: daemonPath, Name: daemon + ".ActionInvoked", Body: []interface{}{uint32(99), "default"}})
	l.handle(&dbus.Signal{Sender: ":1.4", Path: daemonPath, Name: daemon + ".ActionInvoked", Body: []interface{}{uint32(7), "untrusted-action"}})
	if len(activations) != 0 {
		t.Fatal("dismissal or unknown action activated app")
	}
	l.handle(&dbus.Signal{Sender: ":1.4", Path: daemonPath, Name: daemon + ".ActionInvoked", Body: []interface{}{uint32(7), "default"}})
	if len(activations) != 1 || activations[0] != notice.ContactID {
		t.Fatal("known default action did not select canonical contact")
	}
	l.handle(&dbus.Signal{Sender: ":1.4", Path: daemonPath, Name: daemon + ".NotificationClosed", Body: []interface{}{uint32(7), uint32(2)}})
	l.handle(&dbus.Signal{Sender: ":1.4", Path: daemonPath, Name: daemon + ".ActionInvoked", Body: []interface{}{uint32(7), "default"}})
	if len(activations) != 1 {
		t.Fatal("closed notification retained activation routing")
	}
}

func TestLinuxNeverAutolaunchesOrUsesRemoteSessionBus(t *testing.T) {
	for _, address := range []string{"", "tcp:host=invalid.example,port=123", "unix:path=/tmp/example;tcp:host=invalid.example"} {
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
		l := newBackend(nil)
		if err := l.start(context.Background()); err == nil {
			t.Fatal("invalid desktop bus accepted")
		}
		_ = l.close()
	}
}

func TestLinuxNotificationRejectsForgedAndPreviousDaemonSenders(t *testing.T) {
	owner := ":1.4"
	activations := 0
	l := &linuxBackend{owner: owner, lookupOwner: func() (string, error) { return owner, nil }, ids: map[uint32]string{7: syntheticNotice().ContactID}, activate: func(string) { activations++ }}
	for _, sender := range []string{"", daemon, ":1.99"} {
		l.handle(&dbus.Signal{Sender: sender, Path: daemonPath, Name: daemon + ".ActionInvoked", Body: []interface{}{uint32(7), "default"}})
		l.handle(&dbus.Signal{Sender: sender, Path: daemonPath, Name: daemon + ".NotificationClosed", Body: []interface{}{uint32(7), uint32(2)}})
	}
	if activations != 0 || len(l.ids) != 1 {
		t.Fatal("forged sender activated or retired issued notice")
	}
	l.handle(&dbus.Signal{Sender: owner, Path: daemonPath, Name: daemon + ".ActionInvoked", Body: []interface{}{uint32(7), "default"}})
	if activations != 1 {
		t.Fatal("legitimate daemon action failed")
	}
	previous := owner
	owner = ":1.5"
	l.handle(&dbus.Signal{Sender: previous, Path: daemonPath, Name: daemon + ".ActionInvoked", Body: []interface{}{uint32(7), "default"}})
	l.handle(&dbus.Signal{Sender: owner, Path: daemonPath, Name: daemon + ".ActionInvoked", Body: []interface{}{uint32(7), "default"}})
	if activations != 1 || len(l.ids) != 0 {
		t.Fatal("daemon replacement reused stale issued IDs")
	}
	l.ids[7] = syntheticNotice().ContactID
	l.handle(&dbus.Signal{Sender: owner, Path: daemonPath, Name: daemon + ".ActionInvoked", Body: []interface{}{uint32(7), "default"}})
	if activations != 2 {
		t.Fatal("new daemon could not activate newly issued notice")
	}
}

func TestLinuxForgetPurgesOnlyMatchingIssuedIDs(t *testing.T) {
	first, second := syntheticNotice().ContactID, "AwMDAwMDAwMDAwMDAwMDAw"
	l := &linuxBackend{ids: map[uint32]string{1: first, 2: second, 3: first}, order: []uint32{1, 2, 3}}
	if l.forget(first) != nil || len(l.ids) != 1 || l.ids[2] != second || len(l.order) != 1 || l.order[0] != 2 {
		t.Fatal("Forget retained IDs or affected another contact")
	}
}
