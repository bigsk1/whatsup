// Package nativefiles implements explicit, Go-only desktop file input and
// reveal operations. It never opens received files or exposes local paths to JS.
package nativefiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

const MaxClipboardBytes = 256 << 20

var ErrUnavailable = errors.New("Native file action is unavailable")

// ClipboardImage reads encoded image bytes only after an explicit paste action.
// The caller must validate/decode them and present a preview before sending.
// On GTK this function must run on the GTK main thread.
func ClipboardImage(ctx context.Context) ([]byte, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	return clipboardImage(ctx)
}

// ShowInFolder accepts only a trusted Go-selected, existing regular file. The
// renderer receives no generic path-taking entry point for this operation.
func ShowInFolder(ctx context.Context, filename string) error {
	if ctx == nil || ctx.Err() != nil || !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
		return ErrUnavailable
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnavailable
	}
	return showInFolder(ctx, filename)
}
