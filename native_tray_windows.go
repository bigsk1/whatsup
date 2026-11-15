//go:build windows

package main

import (
	"context"
	"golang.org/x/sys/windows"
)

var nativeGetShellWindow = windows.NewLazySystemDLL("user32.dll").NewProc("GetShellWindow")

func newNativeTrayProbe(life context.Context) func(context.Context) bool {
	return func(ctx context.Context) bool { return life.Err() == nil && nativeTrayAvailable(ctx) }
}

func nativeTrayAvailable(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if nativeGetShellWindow.Find() != nil {
		return false
	}
	window, _, _ := nativeGetShellWindow.Call()
	return window != 0
}
