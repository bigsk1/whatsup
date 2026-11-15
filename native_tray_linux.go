//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
)

func nativeTrayAvailable(ctx context.Context) bool {
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if !strings.HasPrefix(address, "unix:") || strings.Contains(address, ";") {
		return false
	}
	bus, err := dbus.Connect(address, dbus.WithContext(ctx))
	if err != nil {
		return false
	}
	defer bus.Close()
	return registeredNativeTray(trayBusProbe{ctx: ctx, bus: bus}, uint32(os.Getpid()))
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
