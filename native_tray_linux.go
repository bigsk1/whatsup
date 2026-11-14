//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

func nativeTrayAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
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
		if item == owner+"/StatusNotifierItem" || item == name+"/StatusNotifierItem" {
			return true
		}
	}
	return false
}
