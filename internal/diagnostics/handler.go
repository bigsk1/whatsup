package diagnostics

import (
	"context"
	"log/slog"
)

type handler struct{ recorder *Recorder }

func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (h *handler) Handle(_ context.Context, record slog.Record) error {
	if record.Level >= slog.LevelError {
		return h.recorder.Record(RuntimeError)
	}
	if record.Level >= slog.LevelWarn {
		return h.recorder.Record(RuntimeWarning)
	}
	return nil
}

// Returning the same handler is intentional: even retaining these caller-owned
// values would unnecessarily keep potential secrets in the diagnostics object.
func (h *handler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *handler) WithGroup(_ string) slog.Handler      { return h }
