package logfilter

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
)

// Handler wraps an slog.Handler and drops records whose message starts
// with any of the configured quiet prefixes (e.g. "metadata:").
//
// The prefix list is shared through an atomic pointer so SetQuiet applies
// to every clone produced by WithAttrs/WithGroup, and is safe to call
// concurrently with logging.
type Handler struct {
	inner    slog.Handler
	prefixes *atomic.Pointer[[]string]
}

// New returns a Handler that delegates to inner but silences messages
// matching any prefix in quietCSV (comma-separated, e.g. "metadata,scanner").
// A colon+space is appended automatically: "metadata" silences "metadata: ...".
// The Handler is always returned (even for an empty quietCSV) so the quiet
// list can be changed later via SetQuiet.
func New(inner slog.Handler, quietCSV string) *Handler {
	prefixes := &atomic.Pointer[[]string]{}
	parsed := parseQuiet(quietCSV)
	prefixes.Store(&parsed)
	return &Handler{inner: inner, prefixes: prefixes}
}

// SetQuiet replaces the quiet prefix list. It applies immediately to this
// handler and every WithAttrs/WithGroup clone derived from it.
func (h *Handler) SetQuiet(quietCSV string) {
	parsed := parseQuiet(quietCSV)
	h.prefixes.Store(&parsed)
}

func parseQuiet(quietCSV string) []string {
	trimmed := strings.TrimSpace(quietCSV)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, ",")
	prefixes := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimSuffix(p, ":")
		if p != "" {
			prefixes = append(prefixes, p+":")
		}
	}
	return prefixes
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	msg := r.Message
	for _, prefix := range *h.prefixes.Load() {
		if strings.HasPrefix(msg, prefix) {
			return nil
		}
	}
	return h.inner.Handle(ctx, r)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(attrs), prefixes: h.prefixes}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), prefixes: h.prefixes}
}
