//go:build linux

package notify

import (
	"context"
	"html"
	"os"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

const daemon = "org.freedesktop.Notifications"
const daemonPath = dbus.ObjectPath("/org/freedesktop/Notifications")

type linuxBackend struct {
	mu          sync.Mutex
	conn        *dbus.Conn
	signals     chan *dbus.Signal
	done        chan struct{}
	activate    func(string)
	ids         map[uint32]string
	order       []uint32
	closed      bool
	owner       string
	lookupOwner func() (string, error)
}

func newBackend(activate func(string)) backend {
	return &linuxBackend{activate: activate, ids: map[uint32]string{}}
}
func (l *linuxBackend) start(ctx context.Context) error {
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	// Use the existing local desktop bus only. Never autolaunch a service,
	// execute a helper, or connect to a TCP/remote bus from inherited settings.
	if !strings.HasPrefix(address, "unix:") || strings.Contains(address, ";") {
		return ErrUnavailable
	}
	conn, err := dbus.Dial(address, dbus.WithContext(ctx))
	if err != nil {
		return ErrUnavailable
	}
	l.conn = conn
	l.lookupOwner = func() (string, error) {
		bounded, cancel := context.WithTimeout(ctx, CallTimeout)
		defer cancel()
		var owner string
		err := conn.BusObject().CallWithContext(bounded, "org.freedesktop.DBus.GetNameOwner", 0, daemon).Store(&owner)
		return owner, err
	}
	setup, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	stop := context.AfterFunc(setup, func() { _ = conn.Close() })
	defer stop()
	if conn.Auth(nil) != nil || conn.Hello() != nil {
		return ErrUnavailable
	}
	l.signals = make(chan *dbus.Signal, 16)
	conn.Signal(l.signals)
	for _, member := range []string{"ActionInvoked", "NotificationClosed"} {
		if conn.AddMatchSignalContext(setup, dbus.WithMatchSender(daemon), dbus.WithMatchInterface(daemon), dbus.WithMatchObjectPath(daemonPath), dbus.WithMatchMember(member)) != nil {
			return ErrUnavailable
		}
	}
	l.done = make(chan struct{})
	go func() {
		defer close(l.done)
		for signal := range l.signals {
			l.handle(signal)
		}
	}()
	return nil
}
func notificationArgs(notice Notice) []interface{} {
	return []interface{}{"WhatsUp", uint32(0), "", notice.Title, html.EscapeString(notice.Body), []string{"default", "Show WhatsUp"}, map[string]dbus.Variant{}, int32(-1)}
}
func (l *linuxBackend) send(ctx context.Context, notice Notice) error {
	if l.conn == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	owner, err := l.notificationOwner()
	if err != nil {
		return ErrUnavailable
	}
	var id uint32
	if err := l.conn.Object(owner, daemonPath).CallWithContext(ctx, daemon+".Notify", 0, notificationArgs(notice)...).Store(&id); err != nil || id == 0 {
		return ErrUnavailable
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrUnavailable
	}
	l.setOwnerLocked(owner)
	l.ids[id] = notice.ContactID
	l.order = append(l.order, id)
	if len(l.order) > 1024 {
		delete(l.ids, l.order[0])
		l.order = l.order[1:]
	}
	return nil
}

func (l *linuxBackend) notificationOwner() (string, error) {
	if l.lookupOwner == nil {
		return "", ErrUnavailable
	}
	owner, err := l.lookupOwner()
	if err != nil || !strings.HasPrefix(owner, ":") {
		return "", ErrUnavailable
	}
	return owner, nil
}

func (l *linuxBackend) setOwnerLocked(owner string) {
	if l.owner == owner {
		return
	}
	l.owner = owner
	l.ids = map[uint32]string{}
	l.order = nil
}

func (l *linuxBackend) handle(signal *dbus.Signal) {
	if signal == nil || signal.Path != daemonPath || len(signal.Body) != 2 || signal.Name != daemon+".ActionInvoked" && signal.Name != daemon+".NotificationClosed" {
		return
	}
	id, ok := signal.Body[0].(uint32)
	if !ok {
		return
	}
	owner, err := l.notificationOwner()
	if err != nil || signal.Sender != owner {
		return
	}
	l.mu.Lock()
	l.setOwnerLocked(owner)
	contactID := l.ids[id]
	if l.closed {
		l.mu.Unlock()
		return
	}
	if signal.Name == daemon+".NotificationClosed" {
		delete(l.ids, id)
		l.mu.Unlock()
		return
	}
	action, ok := signal.Body[1].(string)
	activate := l.activate
	l.mu.Unlock()
	if signal.Name == daemon+".ActionInvoked" && ok && action == "default" && canonicalID(contactID, true) && activate != nil {
		activate(contactID)
	}
}
func (l *linuxBackend) close() error {
	l.mu.Lock()
	l.closed = true
	l.ids = map[uint32]string{}
	l.mu.Unlock()
	if l.conn != nil {
		_ = l.conn.Close()
	}
	if l.done != nil {
		<-l.done
	}
	return nil
}
