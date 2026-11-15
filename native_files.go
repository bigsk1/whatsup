package main

import (
	"context"
	"errors"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"whatsup/internal/desktop"
	"whatsup/internal/diagnostics"
	"whatsup/internal/nativefiles"
)

func (h *nativeHost) configureFiles() error {
	options := desktop.AttachmentRuntimeOptions{
		Life: h.life,
		Pick: func(ctx context.Context) (string, error) {
			if ctx.Err() != nil || h.windowValue() == nil {
				return "", nativefiles.ErrUnavailable
			}
			dialog := h.app.Dialog.OpenFile().SetTitle("Choose an attachment").SetMessage("Choose one file, up to 2 GiB.").AttachToWindow(h.windowValue())
			path, err := dialog.PromptForSingleSelection()
			if ctx.Err() != nil {
				return "", nativefiles.ErrUnavailable
			}
			return path, err
		},
		PickAvatar: func(ctx context.Context) (string, error) {
			if ctx.Err() != nil || h.windowValue() == nil {
				return "", nativefiles.ErrUnavailable
			}
			dialog := h.app.Dialog.OpenFile().SetTitle("Choose your profile photo").
				SetMessage("Choose a static PNG, JPEG or WebP image, up to 16 MiB.").
				AttachToWindow(h.windowValue()).AddFilter("Profile photos (PNG, JPEG, WebP)", "*.png;*.jpg;*.jpeg;*.webp")
			path, err := dialog.PromptForSingleSelection()
			if ctx.Err() != nil {
				return "", nativefiles.ErrUnavailable
			}
			return path, err
		},
		Clipboard: func(ctx context.Context) ([]byte, error) {
			if ctx.Err() != nil {
				return nil, nativefiles.ErrUnavailable
			}
			if runtime.GOOS == "windows" {
				return nativefiles.ClipboardImage(ctx)
			}
			return application.InvokeSyncWithResultAndError(func() ([]byte, error) { return nativefiles.ClipboardImage(ctx) })
		},
		Save: func(ctx context.Context, name string) (string, error) {
			if ctx.Err() != nil || h.windowValue() == nil {
				return "", nativefiles.ErrUnavailable
			}
			dialog := h.app.Dialog.SaveFile()
			dialog.SetOptions(&application.SaveFileDialogOptions{Title: "Save attachment", Message: "Choose a new file name. Existing files are kept.", Filename: name, CanCreateDirectories: true, Window: h.windowValue()})
			path, err := dialog.PromptForSingleSelection()
			if ctx.Err() != nil {
				return "", nativefiles.ErrUnavailable
			}
			return path, err
		},
		Reveal: nativefiles.ShowInFolder,
		Serve:  application.ServeCompletedReader,
	}
	if err := desktop.ConfigureAttachments(h.backend, options); err != nil {
		return err
	}
	h.fileDrops = make(chan []string, 4)
	h.actionMu.Lock()
	if h.stopped {
		h.actionMu.Unlock()
		return errors.New("Native file input is unavailable")
	}
	h.actions.Add(1)
	h.actionMu.Unlock()
	go func() {
		defer h.actions.Done()
		for {
			select {
			case <-h.life.Done():
				return
			case paths := <-h.fileDrops:
				if h.life.Err() != nil {
					return
				}
				if desktop.StageNativeDrop(h.backend, paths) != nil {
					h.record(diagnostics.TransferFailed)
				}
			}
		}
	}()
	return nil
}

func (h *nativeHost) nativeFileDrop(paths []string) {
	h.resetNativeDrag()
	if h.life.Err() != nil || len(paths) != 1 || len(paths[0]) == 0 || len(paths[0]) > 32768 {
		return
	}
	copyOfPaths := append([]string(nil), paths...)
	select {
	case h.fileDrops <- copyOfPaths:
	default:
		h.record(diagnostics.TransferFailed)
	}
}
