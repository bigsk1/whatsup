//go:build linux && (!cgo || purego || gtk3 || android || server)

package nativefiles

import "context"

func clipboardImage(context.Context) ([]byte, error) { return nil, ErrUnavailable }
