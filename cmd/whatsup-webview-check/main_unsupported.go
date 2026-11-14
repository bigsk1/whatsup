//go:build !linux || !cgo || gtk3 || purego || android || server

package main

import "os/exec"

func runNativeProbe(bool) int { return probeUnsupported }

func prepareNativeCommand(*exec.Cmd) {}
