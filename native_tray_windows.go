//go:build windows

package main

import "golang.org/x/sys/windows"

var nativeGetShellWindow = windows.NewLazySystemDLL("user32.dll").NewProc("GetShellWindow")

func nativeTrayAvailable() bool {
	if nativeGetShellWindow.Find() != nil {
		return false
	}
	window, _, _ := nativeGetShellWindow.Call()
	return window != 0
}
