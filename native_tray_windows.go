//go:build windows

package main

import (
	"context"
	"golang.org/x/sys/windows"
)

var nativeGetShellWindow = windows.NewLazySystemDLL("user32.dll").NewProc("GetShellWindow")

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
