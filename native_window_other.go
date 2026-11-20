//go:build !(linux && cgo && !gtk3 && !purego && !android && !server)

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// prepareReveal is needed only where a compositor refuses to raise a mapped
// window; see the Linux implementation.
func prepareReveal(*application.WebviewWindow) {}
