package nodesource

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// RedactRelayURLs replaces every relay URL in s with "source <etag>" while
// its source is open, or "source (released)" after: a relay URL is a bearer
// capability on the node host and must never reach a log, which a transcode
// node ships to the core. Apply it to anything that can carry FFmpeg's
// input: argv, FFmpeg's own output, error messages.
func RedactRelayURLs(s string) string {
	if !strings.Contains(s, relayURLPrefix) {
		return s
	}
	return relayURLPattern.ReplaceAllStringFunc(s, func(url string) string {
		if e := lookupRelay(url); e != nil {
			return "source " + e.etag
		}
		return "source (released)"
	})
}

// RedactingHandler wraps a node's log handler so no record carries a relay
// URL: it redacts the message and every attribute that can hold text (a
// string, an error, a string slice, a Stringer, and groups of them). It
// catches what explicit redaction misses, such as an error that names
// FFmpeg's input. Install it outermost, so the operational log the node ships
// to the core sees redacted records only.
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
	redacted := slog.NewRecord(r.Time, r.Level, RedactRelayURLs(r.Message), r.PC)
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
		return slog.String(a.Key, RedactRelayURLs(value.String()))
	case slog.KindGroup:
		group := value.Group()
		redacted := make([]any, len(group))
		for i, member := range group {
			redacted[i] = redactAttr(member)
		}
		return slog.Group(a.Key, redacted...)
	case slog.KindAny:
		switch v := value.Any().(type) {
		case error:
			if text := v.Error(); strings.Contains(text, relayURLPrefix) {
				return slog.String(a.Key, RedactRelayURLs(text))
			}
		case []string:
			if strings.Contains(strings.Join(v, "\x00"), relayURLPrefix) {
				out := make([]string, len(v))
				for i, s := range v {
					out[i] = RedactRelayURLs(s)
				}
				return slog.Any(a.Key, out)
			}
		case fmt.Stringer:
			if text := v.String(); strings.Contains(text, relayURLPrefix) {
				return slog.String(a.Key, RedactRelayURLs(text))
			}
		}
	}
	return slog.Attr{Key: a.Key, Value: value}
}
