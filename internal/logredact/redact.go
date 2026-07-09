// Package logredact provides secret redaction for slog output sinks (console,
// file, OTLP). The opslog database path does its own redaction when flattening
// records into rows; this package covers the remaining sinks so secrets do not
// leak to stderr, a mounted log file, or an OTLP collector.
//
// Redaction is key-based: an attribute whose key looks secret-bearing (token,
// password, api_key, ...) has its value replaced with the placeholder. Values
// are not scanned, matching the opslog DB-path behavior — a secret embedded in
// a free-text message or a non-secret-keyed value is not caught.
package logredact

import (
	"context"
	"log/slog"
	"strings"
)

// Placeholder replaces the value of a redacted attribute.
const Placeholder = "[REDACTED]"

// secretMarkers are substrings that, when present in a lower-cased attribute
// key, mark the value as secret-bearing. Kept in sync with the opslog DB path
// by being the single shared source (opslog.shouldRedact delegates here).
var secretMarkers = []string{
	"password", "secret", "token", "api_key", "apikey", "authorization", "cookie",
}

// SecretKey reports whether an attribute key names a secret-bearing value.
func SecretKey(key string) bool {
	key = strings.ToLower(key)
	for _, marker := range secretMarkers {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

// Handler wraps an slog.Handler, redacting secret-bearing attributes (including
// those bound via WithAttrs and nested groups) before they reach the inner
// handler. It preserves level gating and best-effort semantics of whatever it
// wraps by delegating Enabled/Handle to the inner handler.
type Handler struct {
	inner slog.Handler
	// redactAll is set once the logger enters a group whose name is
	// secret-bearing (e.g. WithGroup("authorization")). Inside such a subtree
	// every leaf is masked regardless of its own key, matching how a
	// slog.Group("authorization", ...) value is masked as a whole. It stays set
	// for all descendant groups.
	redactAll bool
}

// New returns a Handler wrapping inner.
func New(inner slog.Handler) *Handler {
	return &Handler{inner: inner}
}

// Enabled delegates to the inner handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle redacts the record's attributes and forwards to the inner handler.
// When the record carries no secret-bearing keys, the original record is passed
// through unchanged to avoid rebuilding it on the hot path.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if !h.redactAll && !recordNeedsRedaction(r) {
		return h.inner.Handle(ctx, r)
	}
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redactAttr(a, h.redactAll))
		return true
	})
	return h.inner.Handle(ctx, nr)
}

// WithAttrs redacts the bound attributes so secrets attached via a logger's
// With(...) are also masked.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redactAttr(a, h.redactAll)
	}
	return &Handler{inner: h.inner.WithAttrs(redacted), redactAll: h.redactAll}
}

// WithGroup delegates to the inner handler; grouped attributes are still
// redacted by leaf key at Handle/WithAttrs time. A group whose name is itself
// secret-bearing masks every leaf within the subtree, so nothing logged under
// it (e.g. WithGroup("authorization").Info(..., "value", token)) leaks.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		inner:     h.inner.WithGroup(name),
		redactAll: h.redactAll || SecretKey(name),
	}
}

// recordNeedsRedaction reports whether any record-level attribute (or nested
// group attribute) has a secret-bearing key.
func recordNeedsRedaction(r slog.Record) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if attrHasSecret(a) {
			found = true
			return false
		}
		return true
	})
	return found
}

func attrHasSecret(a slog.Attr) bool {
	// Resolve LogValuer values so a secret hidden behind one (e.g. a type whose
	// LogValue() returns a token) is inspected, not passed through opaque.
	a.Value = a.Value.Resolve()
	// A secret-bearing key masks the whole attribute — including a group whose
	// own key is secret (its members are never reached below).
	if SecretKey(a.Key) {
		return true
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, ga := range a.Value.Group() {
			if attrHasSecret(ga) {
				return true
			}
		}
	}
	return false
}

// redactAttr masks a's value when its key is secret-bearing. When force is set
// (the logger is inside a secret-named group), every leaf is masked regardless
// of its own key, while group structure is preserved so the subtree stays
// well-formed.
func redactAttr(a slog.Attr, force bool) slog.Attr {
	// On the Handle slow path this re-invokes any LogValuer that attrHasSecret
	// already resolved (on its own copy). slog requires LogValue to be cheap and
	// side-effect free, so the double call is acceptable and is what keeps the
	// no-secrets fast path allocation-free.
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		// A group whose own key is secret collapses to a single placeholder,
		// matching the leaf case; otherwise recurse, propagating force.
		if SecretKey(a.Key) {
			return slog.String(a.Key, Placeholder)
		}
		group := a.Value.Group()
		out := make([]slog.Attr, len(group))
		for i, ga := range group {
			out[i] = redactAttr(ga, force)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	// Leaf: mask when forced or when the key itself is secret-bearing.
	if force || SecretKey(a.Key) {
		return slog.String(a.Key, Placeholder)
	}
	return a
}
