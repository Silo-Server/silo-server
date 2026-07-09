package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// captureHandler records handled messages; it is gated by an optional level.
type captureHandler struct {
	level slog.Leveler
	msgs  *[]string
}

func newCapture(level slog.Leveler) (*captureHandler, *[]string) {
	msgs := &[]string{}
	return &captureHandler{level: level, msgs: msgs}, msgs
}

func (h *captureHandler) Enabled(_ context.Context, level slog.Level) bool {
	if h.level == nil {
		return true
	}
	return level >= h.level.Level()
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	*h.msgs = append(*h.msgs, r.Message)
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// erroringHandler always fails on Handle.
type erroringHandler struct{}

func (erroringHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (erroringHandler) Handle(context.Context, slog.Record) error { return errors.New("boom") }
func (erroringHandler) WithAttrs([]slog.Attr) slog.Handler        { return erroringHandler{} }
func (erroringHandler) WithGroup(string) slog.Handler             { return erroringHandler{} }

func TestFanOutForwardsToBoth(t *testing.T) {
	console, consoleMsgs := newCapture(nil)
	otel, otelMsgs := newCapture(nil)

	h := FanOut(console, otel)
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "hello", 0)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	if len(*consoleMsgs) != 1 || (*consoleMsgs)[0] != "hello" {
		t.Errorf("console msgs = %v, want [hello]", *consoleMsgs)
	}
	if len(*otelMsgs) != 1 || (*otelMsgs)[0] != "hello" {
		t.Errorf("otel msgs = %v, want [hello]", *otelMsgs)
	}
}

func TestFanOutSwallowsOTelError(t *testing.T) {
	console, consoleMsgs := newCapture(nil)
	// Wrap the erroring handler like NewOTelHandler does.
	otel := &bestEffortHandler{inner: erroringHandler{}}

	h := FanOut(console, otel)
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "hi", 0)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatalf("fan-out Handle returned error despite best-effort otel: %v", err)
	}
	if len(*consoleMsgs) != 1 {
		t.Errorf("console did not receive record: %v", *consoleMsgs)
	}
}

func TestLevelGateDropsDebugOnBothBranches(t *testing.T) {
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)

	console, consoleMsgs := newCapture(level)
	otelInner, otelMsgs := newCapture(nil) // otel provider is unfiltered by default
	otel := LevelGated(otelInner, level)

	logger := slog.New(FanOut(console, otel))
	logger.DebugContext(context.Background(), "debug-dropped")
	logger.InfoContext(context.Background(), "info-kept")

	if len(*consoleMsgs) != 1 || (*consoleMsgs)[0] != "info-kept" {
		t.Errorf("console msgs = %v, want [info-kept]", *consoleMsgs)
	}
	if len(*otelMsgs) != 1 || (*otelMsgs)[0] != "info-kept" {
		t.Errorf("otel msgs = %v, want [info-kept] (debug must be dropped by level gate)", *otelMsgs)
	}
}
