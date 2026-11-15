//go:build linux

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
)

type traySession interface {
	connected() bool
	available(context.Context) bool
	close() error
}

type linuxTraySession struct{ bus *dbus.Conn }

func (s linuxTraySession) connected() bool { return s.bus.Connected() }
func (s linuxTraySession) available(ctx context.Context) bool {
	return registeredNativeTray(trayBusProbe{ctx: ctx, bus: s.bus}, uint32(os.Getpid()))
}
func (s linuxTraySession) close() error { return s.bus.Close() }

type linuxTrayProbe struct {
	mu      sync.Mutex
	life    context.Context
	connect func(context.Context) (traySession, error)
	session traySession
}

func newNativeTrayProbe(life context.Context) func(context.Context) bool {
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	probe := newLinuxTrayProbe(life, func(ctx context.Context) (traySession, error) { return connectTraySession(life, ctx, address) })
	return probe.available
}

func newLinuxTrayProbe(life context.Context, connect func(context.Context) (traySession, error)) *linuxTrayProbe {
	probe := &linuxTrayProbe{life: life, connect: connect}
	context.AfterFunc(life, probe.close)
	return probe
}

// The owned connection lives with the host; individual calls keep their short
// deadlines. A missing watcher/host does not create a new bus client every poll.
func connectTraySession(life, ctx context.Context, address string) (traySession, error) {
	dialer := &net.Dialer{}
	return connectTraySessionWithDial(life, ctx, address, dialer.DialContext)
}

func localTraySocket(address string) (string, error) {
	if !strings.HasPrefix(address, "unix:") || strings.Contains(address, ";") {
		return "", fmt.Errorf("tray requires one local session bus")
	}
	values := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(address, "unix:"), ",") {
		key, encoded, ok := strings.Cut(part, "=")
		if !ok || key != "path" && key != "abstract" && key != "guid" {
			return "", fmt.Errorf("unsupported local bus address")
		}
		if _, duplicate := values[key]; duplicate {
			return "", fmt.Errorf("duplicate local bus address field")
		}
		value, err := dbus.UnescapeBusAddressValue(encoded)
		if err != nil || value == "" || strings.ContainsRune(value, 0) {
			return "", fmt.Errorf("invalid local bus address")
		}
		values[key] = value
	}
	path, abstract := values["path"], values["abstract"]
	if path != "" && abstract == "" && strings.HasPrefix(path, "/") {
		return path, nil
	}
	if path == "" && abstract != "" {
		return "@" + abstract, nil
	}
	return "", fmt.Errorf("local bus requires one path or abstract name")
}

func connectTraySessionWithDial(life, ctx context.Context, address string, dial func(context.Context, string, string) (net.Conn, error)) (traySession, error) {
	if life.Err() != nil || ctx.Err() != nil {
		return nil, context.Canceled
	}
	setup, cancelSetup := context.WithCancel(ctx)
	stopLife := context.AfterFunc(life, cancelSetup)
	defer cancelSetup()
	defer stopLife()
	ctx = setup
	socket, err := localTraySocket(address)
	if err != nil {
		return nil, err
	}
	// godbus.Dial uses net.DialUnix before applying its connection context.
	// DialContext bounds that initial socket operation as well as authentication.
	connection, err := dial(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	unix, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, fmt.Errorf("local bus did not use Unix transport")
	}
	bus, err := dbus.DialUnix(unix, dbus.WithContext(life))
	if err != nil {
		_ = unix.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = bus.Close() })
	defer stop()
	if err := bus.Auth(nil); err != nil {
		_ = bus.Close()
		return nil, err
	}
	if err := bus.Hello(); err != nil {
		_ = bus.Close()
		return nil, err
	}
	if ctx.Err() != nil || life.Err() != nil {
		_ = bus.Close()
		return nil, context.Canceled
	}
	return linuxTraySession{bus: bus}, nil
}

func (p *linuxTrayProbe) available(ctx context.Context) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.life.Err() != nil || ctx.Err() != nil {
		return false
	}
	if p.session != nil && !p.session.connected() {
		_ = p.session.close()
		p.session = nil
	}
	if p.session == nil {
		var err error
		p.session, err = p.connect(ctx)
		if err != nil || p.session == nil {
			p.session = nil
			return false
		}
	}
	return p.session.available(ctx)
}

func (p *linuxTrayProbe) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session != nil {
		_ = p.session.close()
		p.session = nil
	}
}

type nativeTrayProbe interface {
	owner(string) (string, error)
	pid(string) (uint32, error)
	watcher(string) (bool, []string, error)
	item(string) (bool, error)
}

type trayBusProbe struct {
	ctx context.Context
	bus *dbus.Conn
}

func (p trayBusProbe) owner(name string) (string, error) {
	var owner string
	err := p.bus.BusObject().CallWithContext(p.ctx, "org.freedesktop.DBus.GetNameOwner", 0, name).Store(&owner)
	return owner, err
}
func (p trayBusProbe) pid(name string) (uint32, error) {
	var pid uint32
	err := p.bus.BusObject().CallWithContext(p.ctx, "org.freedesktop.DBus.GetConnectionUnixProcessID", 0, name).Store(&pid)
	return pid, err
}
func (p trayBusProbe) watcher(owner string) (bool, []string, error) {
	var props map[string]dbus.Variant
	err := p.bus.Object(owner, "/StatusNotifierWatcher").CallWithContext(p.ctx, "org.freedesktop.DBus.Properties.GetAll", 0, "org.kde.StatusNotifierWatcher").Store(&props)
	registered, _ := props["IsStatusNotifierHostRegistered"].Value().(bool)
	items, _ := props["RegisteredStatusNotifierItems"].Value().([]string)
	return registered, items, err
}

func (p trayBusProbe) item(owner string) (bool, error) {
	var status dbus.Variant
	err := p.bus.Object(owner, "/StatusNotifierItem").CallWithContext(p.ctx, "org.freedesktop.DBus.Properties.Get", 0, "org.kde.StatusNotifierItem", "Status").Store(&status)
	return status.Value() == "Active", err
}

// A host alone is insufficient. The pinned Wails implementation exports one
// item at this exact path/name; require that the watcher registered this process.
func registeredNativeTray(probe nativeTrayProbe, pid uint32) bool {
	watcher, err := probe.owner("org.kde.StatusNotifierWatcher")
	if err != nil || !strings.HasPrefix(watcher, ":") {
		return false
	}
	name := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", pid)
	owner, err := probe.owner(name)
	if err != nil || !strings.HasPrefix(owner, ":") {
		return false
	}
	actualPID, err := probe.pid(owner)
	if err != nil || actualPID != pid {
		return false
	}
	host, items, err := probe.watcher(watcher)
	if err != nil || !host {
		return false
	}
	for _, item := range items {
		if registeredOwnItem(item, owner, name) {
			active, err := probe.item(owner)
			return err == nil && active
		}
	}
	return false
}

// Watchers expose either a service name, service/path, or service@/path.
// Normalize only exact forms for our pinned default object, never prefixes.
func registeredOwnItem(item, owner, name string) bool {
	for _, service := range []string{owner, name} {
		if item == service || item == service+"/StatusNotifierItem" || item == service+"@/StatusNotifierItem" {
			return true
		}
	}
	return false
}
