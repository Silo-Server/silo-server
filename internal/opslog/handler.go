package opslog

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
)

type Handler struct {
	inner      slog.Handler
	writer     Writer
	capture    slog.Level
	nodeID     string
	static     map[string]any
	groupNames []string
}

func NewHandler(inner slog.Handler, writer Writer, capture slog.Level, nodeID string) slog.Handler {
	return &Handler{
		inner:   inner,
		writer:  writer,
		capture: capture,
		nodeID:  nodeID,
		static:  map[string]any{},
	}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level) || level >= h.capture
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if err := h.inner.Handle(ctx, r); err != nil {
		return err
	}
	if h.writer == nil || r.Level < h.capture {
		return nil
	}

	attrs := make(map[string]any, len(h.static)+8)
	for k, v := range h.static {
		attrs[k] = v
	}
	r.Attrs(func(attr slog.Attr) bool {
		h.addAttr(attrs, attr, "")
		return true
	})
	attrs = redactAttrs(attrs)

	component, _ := attrs["component"].(string)
	if component == "" {
		component = inferComponent(r.Message)
	}
	requestID, _ := attrs["request_id"].(string)
	sessionID, _ := attrs["session_id"].(string)
	playbackSessionID, _ := attrs["playback_session_id"].(string)
	clientIP, _ := attrs["client_ip"].(string)
	if clientIP == "" {
		clientIP, _ = attrs["remote_addr"].(string)
	}
	nodeID := h.nodeID
	if attrNodeID, _ := attrs["node_id"].(string); attrNodeID != "" {
		nodeID = attrNodeID
	}
	var userID *int
	switch v := attrs["user_id"].(type) {
	case int:
		value := v
		userID = &value
	case int64:
		value := int(v)
		userID = &value
	case float64:
		value := int(v)
		userID = &value
	}

	h.writer.Write(Entry{
		Timestamp:         time.Now().UTC(),
		Level:             strings.ToLower(r.Level.String()),
		Component:         component,
		Message:           r.Message,
		RequestID:         requestID,
		UserID:            userID,
		SessionID:         sessionID,
		PlaybackSessionID: playbackSessionID,
		ClientIP:          clientIP,
		NodeID:            nodeID,
		Attrs:             attrs,
	})
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &Handler{
		inner:      h.inner.WithAttrs(attrs),
		writer:     h.writer,
		capture:    h.capture,
		nodeID:     h.nodeID,
		static:     map[string]any{},
		groupNames: append([]string(nil), h.groupNames...),
	}
	for k, v := range h.static {
		next.static[k] = v
	}
	for _, attr := range attrs {
		next.addAttr(next.static, attr, "")
	}
	return next
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{
		inner:      h.inner.WithGroup(name),
		writer:     h.writer,
		capture:    h.capture,
		nodeID:     h.nodeID,
		static:     cloneMap(h.static),
		groupNames: append(append([]string(nil), h.groupNames...), name),
	}
}

func (h *Handler) addAttr(dst map[string]any, attr slog.Attr, prefix string) {
	attr.Value = attr.Value.Resolve()
	key := attr.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	if len(h.groupNames) > 0 && prefix == "" {
		key = strings.Join(append(append([]string(nil), h.groupNames...), attr.Key), ".")
	}
	if attr.Value.Kind() == slog.KindGroup {
		nextPrefix := key
		for _, child := range attr.Value.Group() {
			h.addAttr(dst, child, nextPrefix)
		}
		return
	}
	dst[key] = attrValue(attr.Value)
}

func attrValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindAny:
		return v.Any()
	default:
		return v.String()
	}
}

func inferComponent(message string) string {
	if prefix, _, ok := strings.Cut(message, ":"); ok {
		prefix = strings.TrimSpace(prefix)
		if prefix != "" {
			return prefix
		}
	}
	return "app"
}

func redactAttrs(attrs map[string]any) map[string]any {
	redacted := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if shouldRedact(k) {
			redacted[k] = "[REDACTED]"
			continue
		}
		redacted[k] = v
	}
	return redacted
}

// shouldRedact delegates to the shared logredact detection so the DB path and
// the console/file/OTLP sinks stay in lock-step on what counts as a secret key.
func shouldRedact(key string) bool {
	return logredact.SecretKey(key)
}

func cloneMap(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
