package main

import (
	"context"

	"github.com/wailsapp/wails/v3/pkg/application"
	"whatsup/internal/desktop"
	"whatsup/internal/diagnostics"
	"whatsup/internal/nativefiles"
)

func (h *nativeHost) configureSecurity() error {
	return desktop.ConfigureSecurity(h.backend, desktop.SecurityRuntimeOptions{
		Life: h.life,
		PickRecovery: func(ctx context.Context) (string, error) {
			if ctx.Err() != nil || h.windowValue() == nil {
				return "", nativefiles.ErrUnavailable
			}
			dialog := h.app.Dialog.OpenFile().SetTitle("Choose encrypted recovery file").AttachToWindow(h.windowValue()).AddFilter("WhatsUp encrypted recovery", "*.whatsup-backup")
			filename, err := dialog.PromptForSingleSelection()
			if ctx.Err() != nil {
				return "", nativefiles.ErrUnavailable
			}
			return filename, err
		},
		SaveRecovery: func(ctx context.Context) (string, error) {
			if ctx.Err() != nil || h.windowValue() == nil {
				return "", nativefiles.ErrUnavailable
			}
			dialog := h.app.Dialog.SaveFile()
			dialog.SetOptions(&application.SaveFileDialogOptions{Title: "Export encrypted recovery file", Message: "Choose a new file name. Existing files are kept.", Filename: "WhatsUp recovery.whatsup-backup", CanCreateDirectories: true, Window: h.windowValue()})
			filename, err := dialog.PromptForSingleSelection()
			if ctx.Err() != nil {
				return "", nativefiles.ErrUnavailable
			}
			return filename, err
		},
		StorageChanged: func(state string) {
			if h.emitStorage != nil {
				h.emitStorage(state)
			}
		},
		PreferencesChanged: func() error {
			if h.life.Err() != nil {
				return nativefiles.ErrUnavailable
			}
			if desktop.ReopenAppPreferences(h.backend) != nil {
				return nativefiles.ErrUnavailable
			}
			values, err := h.backend.GetAppPreferences()
			if err != nil {
				return err
			}
			h.preferencesChanged(values)
			if h.reconcileStartup(values) != nil {
				h.record(diagnostics.RuntimeWarning)
			}
			if h.notifier.Start(h.life) != nil {
				h.record(diagnostics.RuntimeWarning)
			}
			return nil
		},
	})
}
