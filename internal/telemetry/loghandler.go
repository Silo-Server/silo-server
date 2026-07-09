package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	logapi "go.opentelemetry.io/otel/log"
)

// NewOTelHandler builds an slog.Handler that bridges records into the OTel logs
// pipeline via otelslog, wrapped so that Handle/WithAttrs/WithGroup never
// propagate an export error. This mirrors the best-effort contract of the
// existing internal/logsink/opslog handlers: a failure to export must not break
// the handler chain (e.g. the console or opslog DB branch must still run).
func NewOTelHandler(lp logapi.LoggerProvider) slog.Handler {
	return &bestEffortHandler{
		inner: otelslog.NewHandler("silo-server", otelslog.WithLoggerProvider(lp)),
	}
}

// bestEffortHandler wraps a handler so its Handle never returns an error.
type bestEffortHandler struct {
	inner slog.Handler
}

func (h *bestEffortHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *bestEffortHandler) Handle(ctx context.Context, r slog.Record) error {
	// Swallow export errors: telemetry is best-effort and must never break the
	// fan-out chain.
	_ = h.inner.Handle(ctx, r)
	return nil
}

func (h *bestEffortHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &bestEffortHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *bestEffortHandler) WithGroup(name string) slog.Handler {
	return &bestEffortHandler{inner: h.inner.WithGroup(name)}
}

// LevelGated wraps a handler so both Enabled and Handle are gated by the shared
// level. This is REQUIRED for the OTel branch: slog.MultiHandler.Enabled ORs its
// children, so without gating, the otel branch (whose Enabled follows the
// unfiltered LoggerProvider) would make the logger emit and export Debug records
// even when the shared level is Info — a per-call allocation cost and a silent
// stderr/OTLP divergence.
func LevelGated(h slog.Handler, level slog.Leveler) slog.Handler {
	return &LevelGatedHandler{inner: h, level: level}
}

type LevelGatedHandler struct {
	inner slog.Handler
	level slog.Leveler
}

func (h *LevelGatedHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if level < h.level.Level() {
		return false
	}
	return h.inner.Enabled(ctx, level)
}

func (h *LevelGatedHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < h.level.Level() {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

func (h *LevelGatedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LevelGatedHandler{inner: h.inner.WithAttrs(attrs), level: h.level}
}

func (h *LevelGatedHandler) WithGroup(name string) slog.Handler {
	return &LevelGatedHandler{inner: h.inner.WithGroup(name), level: h.level}
}

// FanOut returns a handler that dispatches each record to both the console and
// otel handlers using the stdlib slog.MultiHandler (Go 1.26).
func FanOut(console, otel slog.Handler) slog.Handler {
	return slog.NewMultiHandler(console, otel)
}
