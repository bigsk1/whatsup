package lifecycle

import (
	"errors"
	"testing"
)

func TestSecondLaunchBeforeNativeWindowFocusesExistingWindowOnce(t *testing.T) {
	shown := 0
	c := New(Actions{Show: func() { shown++ }})
	c.Focus()
	c.Focus()
	if shown != 0 {
		t.Fatal("focused before native window existed")
	}
	c.NativeReady()
	if shown != 1 {
		t.Fatal("lost or duplicated startup focus request")
	}
	c.Focus()
	if shown != 2 {
		t.Fatal("existing window was not focused")
	}
}

func TestHiddenStartupCanBeRevealedBeforeRendererReady(t *testing.T) {
	shown := 0
	c := New(Actions{StartHidden: true, Show: func() { shown++ }, TrayAvailable: func() bool { return true }})
	c.NativeReady()
	c.Focus()
	if shown != 1 {
		t.Fatal("renderer readiness stranded second-launch/tray focus")
	}
	c.StartupFallback()
	if shown != 1 {
		t.Fatal("fallback repeated an explicit reveal")
	}
}

func TestHiddenStartupFallbackRevealsMissingRendererOrTray(t *testing.T) {
	for _, ready := range []bool{false, true} {
		for _, tray := range []bool{false, true} {
			shown := 0
			c := New(Actions{StartHidden: true, Show: func() { shown++ }, TrayAvailable: func() bool { return tray }})
			c.NativeReady()
			if ready {
				c.Ready()
			}
			c.StartupFallback()
			c.StartupFallback()
			want := 1
			if ready && tray {
				want = 0
			}
			if shown != want {
				t.Fatalf("ready=%v tray=%v shown=%d", ready, tray, shown)
			}
		}
	}
}

func TestAutostartRemainsHiddenOnlyWithReachableTrayAndNoFocusRequest(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		for _, tray := range []bool{false, true} {
			for _, focus := range []bool{false, true} {
				shown := 0
				c := New(Actions{StartHidden: hidden, Show: func() { shown++ }, TrayAvailable: func() bool { return tray }})
				if focus {
					c.Focus()
				}
				c.NativeReady()
				c.Ready()
				c.Ready()
				want := 0
				if focus || hidden && !tray {
					want = 1
				}
				if shown != want {
					t.Fatalf("hidden=%v tray=%v focus=%v: shown=%d want=%d", hidden, tray, focus, shown, want)
				}
				c.Focus()
				if shown != want+1 {
					t.Fatal("tray/second-launch focus did not restore the ready window")
				}
			}
		}
	}
}

func TestAutostartCannotShowAfterQuit(t *testing.T) {
	c := New(Actions{StartHidden: true, Show: func() { t.Fatal("showed after quit") }, Quit: func() {}, TrayAvailable: func() bool { return false }})
	c.Focus()
	c.Quit()
	c.NativeReady()
	c.StartupFallback()
	c.Ready()
	c.Focus()
}

func TestCloseKeepsRunningWithOneNoticeAndExplicitQuit(t *testing.T) {
	seen := false
	hidden, notices, quits := 0, 0, 0
	c := New(Actions{Show: func() {}, Hide: func() { hidden++ }, Quit: func() { quits++ }, Background: func() bool { return true }, TrayAvailable: func() bool { return true }, NoticeSeen: func() (bool, error) { return seen, nil }, Notice: func(bool) { notices++ }, MarkNotice: func() error { seen = true; return nil }})
	c.CloseWindow()
	c.CloseWindow()
	if hidden != 2 || notices != 1 || quits != 0 {
		t.Fatal("close did not preserve tray process and one-time notice")
	}
	c.Quit()
	c.Quit()
	c.CloseWindow()
	if quits != 1 || hidden != 2 || !c.Quitting() {
		t.Fatal("Quit was not terminal/idempotent")
	}
}

func TestNoTrayOrDisabledBackgroundCannotStrandHiddenWindow(t *testing.T) {
	for _, background := range []bool{false, true} {
		hidden, quits := 0, 0
		c := New(Actions{Show: func() {}, Hide: func() { hidden++ }, Quit: func() { quits++ }, Background: func() bool { return background }, TrayAvailable: func() bool { return false }, Notice: func(bool) {}})
		c.CloseWindow()
		if hidden != 0 || quits != 1 {
			t.Fatal("window was hidden without a recovery tray")
		}
	}
}

func TestUncertainNoticePersistenceKeepsWindowVisible(t *testing.T) {
	hidden := false
	c := New(Actions{Show: func() {}, Hide: func() { hidden = true }, Quit: func() { t.Fatal("unexpected quit") }, Background: func() bool { return true }, TrayAvailable: func() bool { return true }, NoticeSeen: func() (bool, error) { return false, nil }, Notice: func(bool) {}, MarkNotice: func() error { return errors.New("synthetic failure") }})
	c.CloseWindow()
	if hidden {
		t.Fatal("hid before first notice was durably recorded")
	}
}
