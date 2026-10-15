package nodesource

import (
	"context"
	"log/slog"
)

// RedactingHandler wraps a node's log handler so no record carries a relay
// URL: it applies RedactLoopback to the message and every string attribute.
// It catches what explicit redaction misses, such as an os.Open error that
// names FFmpeg's input.
func RedactingHandler(next slog.Handler) slog.Handler {
	return redactingHandler{next: next}
}

type redactingHandler struct {
	next slog.Handler
}

func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	redacted := slog.NewRecord(r.Time, r.Level, RedactLoopback(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		redacted.AddAttrs(redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, redacted)
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redactAttr(a)
	}
	return redactingHandler{next: h.next.WithAttrs(redacted)}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{next: h.next.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	value := a.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, RedactLoopback(value.String()))
	case slog.KindGroup:
		group := value.Group()
		redacted := make([]any, len(group))
		for i, member := range group {
			redacted[i] = redactAttr(member)
		}
		return slog.Group(a.Key, redacted...)
	case slog.KindAny:
		if err, ok := value.Any().(error); ok {
			return slog.String(a.Key, RedactLoopback(err.Error()))
		}
	}
	return slog.Attr{Key: a.Key, Value: value}
}
