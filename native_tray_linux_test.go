//go:build linux

package main

import (
	"errors"
	"testing"
)

type syntheticTrayProbe struct {
	host              bool
	items             []string
	missing, wrongPID bool
	missingItem       bool
}

func (p syntheticTrayProbe) owner(name string) (string, error) {
	if p.missing {
		return "", errors.New("absent")
	}
	if name == "org.kde.StatusNotifierWatcher" {
		return ":1.2", nil
	}
	return ":1.8", nil
}
func (p syntheticTrayProbe) pid(string) (uint32, error) {
	if p.wrongPID {
		return 99, nil
	}
	return 42, nil
}
func (p syntheticTrayProbe) watcher(string) (bool, []string, error) { return p.host, p.items, nil }
func (p syntheticTrayProbe) item(string) (bool, error)              { return !p.missingItem, nil }
func TestTrayRequiresRegisteredItemFromThisProcess(t *testing.T) {
	for _, item := range []string{":1.8", "org.kde.StatusNotifierItem-42-1", ":1.8/StatusNotifierItem", "org.kde.StatusNotifierItem-42-1/StatusNotifierItem", ":1.8@/StatusNotifierItem", "org.kde.StatusNotifierItem-42-1@/StatusNotifierItem"} {
		if !registeredNativeTray(syntheticTrayProbe{host: true, items: []string{item}}, 42) {
			t.Fatal("registered own item unavailable")
		}
	}
	for _, probe := range []syntheticTrayProbe{
		{missing: true}, {host: true}, {host: false, items: []string{":1.8/StatusNotifierItem"}},
		{host: true, items: []string{":1.9/StatusNotifierItem"}},
		{host: true, items: []string{":1.9@/StatusNotifierItem"}},
		{host: true, items: []string{":1.8@/AnotherItem"}},
		{host: true, items: []string{":1.8@@/StatusNotifierItem"}},
		{host: true, wrongPID: true, items: []string{":1.8/StatusNotifierItem"}},
		{host: true, missingItem: true, items: []string{":1.8"}},
	} {
		if registeredNativeTray(probe, 42) {
			t.Fatal("unproven tray stranded hidden app")
		}
	}
}
