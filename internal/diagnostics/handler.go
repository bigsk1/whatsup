package diagnostics

import (
	"context"
	"log/slog"
)

type handler struct{ record func(Event) error }

func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (h *handler) Handle(_ context.Context, record slog.Record) error {
	if record.Level >= slog.LevelError {
		return h.record(RuntimeError)
	}
	if record.Level >= slog.LevelWarn {
		return h.record(RuntimeWarning)
	}
	return nil
}

// Returning the same handler is intentional: even retaining these caller-owned
// values would unnecessarily keep potential secrets in the diagnostics object.
func (h *handler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *handler) WithGroup(_ string) slog.Handler      { return h }

// LoggerFor can be installed before the on-disk recorder is opened. The sink
// receives only finite codes, never raw log messages, attributes or groups.
func LoggerFor(record func(Event)) *slog.Logger {
	return slog.New(&handler{record: func(event Event) error {
		if record != nil {
			record(event)
		}
		return nil
	}})
}
