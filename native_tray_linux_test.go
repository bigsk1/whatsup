//go:build linux

package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type syntheticTrayProbe struct {
	host              bool
	items             []string
	missing, wrongPID bool
	missingItem       bool
}

type syntheticTraySession struct {
	live    atomic.Bool
	samples atomic.Int32
	closed  atomic.Int32
}

func (s *syntheticTraySession) connected() bool                { return s.live.Load() }
func (s *syntheticTraySession) available(context.Context) bool { s.samples.Add(1); return false }
func (s *syntheticTraySession) close() error                   { s.closed.Add(1); s.live.Store(false); return nil }

func TestLinuxTrayProbeReusesConnectionForMissingHostAndReconnectsOnlyAfterLoss(t *testing.T) {
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sessions []*syntheticTraySession
	p := newLinuxTrayProbe(life, func(context.Context) (traySession, error) {
		s := &syntheticTraySession{}
		s.live.Store(true)
		sessions = append(sessions, s)
		return s, nil
	})
	for range 10 {
		if p.available(context.Background()) {
			t.Fatal("synthetic absent host reported available")
		}
	}
	if len(sessions) != 1 || sessions[0].samples.Load() != 10 {
		t.Fatal("periodic missing-host probes recreated bus connection")
	}
	sessions[0].live.Store(false)
	p.available(context.Background())
	if len(sessions) != 2 || sessions[0].closed.Load() != 1 {
		t.Fatal("lost connection did not reconnect independently of host absence")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for sessions[1].closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sessions[1].closed.Load() != 1 || p.available(context.Background()) || len(sessions) != 2 {
		t.Fatal("shutdown did not retire owned connection")
	}
}

func TestLinuxTrayProbeRetriesFailedConnectionWithoutKeepingPartialState(t *testing.T) {
	attempts := 0
	session := &syntheticTraySession{}
	session.live.Store(true)
	p := &linuxTrayProbe{life: context.Background(), connect: func(context.Context) (traySession, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("temporary local bus failure")
		}
		return session, nil
	}}
	p.available(context.Background())
	if p.session != nil {
		t.Fatal("failed connection retained as active")
	}
	p.available(context.Background())
	p.available(context.Background())
	if attempts != 2 || session.samples.Load() != 2 {
		t.Fatal("connection failure became permanent or healthy connection wasn't reused")
	}
	p.close()
}

func TestTraySocketPreservesSupportedLocalAddressForms(t *testing.T) {
	for _, test := range []struct{ address, want string }{
		{"unix:path=/run/user/1000/bus", "/run/user/1000/bus"},
		{"unix:path=%2ftmp%2fuser%20bus,guid=00112233445566778899aabbccddeeff", "/tmp/user bus"},
		{"unix:abstract=/tmp/dbus-example", "@/tmp/dbus-example"},
		{"unix:abstract=dbus%2dexample", "@dbus-example"},
	} {
		got, err := localTraySocket(test.address)
		if err != nil || got != test.want {
			t.Fatalf("supported local address refused: %s", test.address)
		}
	}
	for _, address := range []string{"", "tcp:host=127.0.0.1,port=1234", "unix:path=/tmp/bus;tcp:host=example.invalid", "unix:path=/tmp/a,path=/tmp/b", "unix:path=/tmp/a,abstract=b", "unix:path=relative", "unix:path=%00/tmp/a", "unix:abstract=", "unix:runtime=yes"} {
		if _, err := localTraySocket(address); err == nil {
			t.Fatal("invalid or remote bus address accepted")
		}
	}
}

func TestTrayInitialDialReceivesSampleDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := connectTraySessionWithDial(context.Background(), ctx, "unix:abstract=synthetic", func(ctx context.Context, network, address string) (net.Conn, error) {
		if _, bounded := ctx.Deadline(); !bounded || network != "unix" || address != "@synthetic" {
			t.Error("initial dial lost deadline or local transport")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err == nil || time.Since(started) > time.Second {
		t.Fatal("initial local bus dial ignored sample deadline")
	}
}

func TestTrayAuthenticationStallHonorsSampleDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bus")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			accepted <- connection
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	session, err := connectTraySession(context.Background(), ctx, "unix:path="+path)
	if session != nil {
		session.close()
	}
	select {
	case connection := <-accepted:
		connection.Close()
	case <-time.After(time.Second):
		t.Fatal("synthetic peer not accepted")
	}
	if err == nil || time.Since(started) > time.Second {
		t.Fatal("silent local authentication peer ignored sample bound")
	}
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
