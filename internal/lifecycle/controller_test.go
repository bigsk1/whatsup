package lifecycle

import (
	"errors"
	"testing"
)

func TestSecondLaunchBeforeReadyFocusesExistingWindowOnce(t *testing.T) {
	shown := 0
	c := New(Actions{Show: func() { shown++ }})
	c.Focus()
	c.Focus()
	if shown != 0 {
		t.Fatal("focused before native window existed")
	}
	c.Ready()
	if shown != 1 {
		t.Fatal("lost or duplicated startup focus request")
	}
	c.Focus()
	if shown != 2 {
		t.Fatal("existing window was not focused")
	}
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
