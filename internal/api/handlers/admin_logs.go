package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/opslog"
)

type AdminLogsHandler struct {
	opsRepo   *opslog.Repo
	auditRepo *activitylog.Repo
	streamHub *logstream.Hub
}

func NewAdminLogsHandler(opsRepo *opslog.Repo, auditRepo *activitylog.Repo, streamHub *logstream.Hub) *AdminLogsHandler {
	return &AdminLogsHandler{opsRepo: opsRepo, auditRepo: auditRepo, streamHub: streamHub}
}

func (h *AdminLogsHandler) HandleListOperationalLogs(w http.ResponseWriter, r *http.Request) {
	opts, err := parseOperationalLogOptionsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	result, err := h.opsRepo.List(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to query operational logs")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *AdminLogsHandler) HandleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	opts, err := parseAuditLogOptionsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	result, err := h.auditRepo.List(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to query audit logs")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func parseOperationalLogOptionsFromRequest(r *http.Request) (opslog.ListOptions, error) {
	opts := opslog.ListOptions{
		Level:             strings.TrimSpace(r.URL.Query().Get("level")),
		Component:         strings.TrimSpace(r.URL.Query().Get("component")),
		NodeID:            strings.TrimSpace(r.URL.Query().Get("node_id")),
		RequestID:         strings.TrimSpace(r.URL.Query().Get("request_id")),
		SessionID:         strings.TrimSpace(r.URL.Query().Get("session_id")),
		PlaybackSessionID: strings.TrimSpace(r.URL.Query().Get("playback_session_id")),
		Query:             strings.TrimSpace(r.URL.Query().Get("q")),
		Cursor:            strings.TrimSpace(r.URL.Query().Get("cursor")),
		Limit:             parseLimit(r, 100),
	}

	userID, err := parseOptionalIntQuery(r, "user_id")
	if err != nil {
		return opslog.ListOptions{}, err
	}
	opts.UserID = userID

	if from, err := parseTimeQuery(r, "from"); err != nil {
		return opslog.ListOptions{}, err
	} else {
		opts.From = from
	}
	if to, err := parseTimeQuery(r, "to"); err != nil {
		return opslog.ListOptions{}, err
	} else {
		opts.To = to
	}

	return opts, nil
}

func parseAuditLogOptionsFromRequest(r *http.Request) (activitylog.ListOptions, error) {
	opts := activitylog.ListOptions{
		Method:            strings.TrimSpace(r.URL.Query().Get("method")),
		PathPrefix:        strings.TrimSpace(r.URL.Query().Get("path_prefix")),
		ClientIP:          strings.TrimSpace(r.URL.Query().Get("client_ip")),
		RequestID:         strings.TrimSpace(r.URL.Query().Get("request_id")),
		SessionID:         strings.TrimSpace(r.URL.Query().Get("session_id")),
		PlaybackSessionID: strings.TrimSpace(r.URL.Query().Get("playback_session_id")),
		Cursor:            strings.TrimSpace(r.URL.Query().Get("cursor")),
		Limit:             parseLimit(r, 100),
	}

	statusCode, err := parseOptionalIntQuery(r, "status_code")
	if err != nil {
		return activitylog.ListOptions{}, err
	}
	opts.StatusCode = statusCode

	userID, err := parseOptionalIntQuery(r, "user_id")
	if err != nil {
		return activitylog.ListOptions{}, err
	}
	opts.UserID = userID

	if from, err := parseTimeQuery(r, "from"); err != nil {
		return activitylog.ListOptions{}, err
	} else {
		opts.From = from
	}
	if to, err := parseTimeQuery(r, "to"); err != nil {
		return activitylog.ListOptions{}, err
	} else {
		opts.To = to
	}

	return opts, nil
}

func parseTimeQuery(r *http.Request, key string) (*time.Time, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, invalidQueryError(key)
	}
	value := ts.UTC()
	return &value, nil
}

func parseOptionalIntQuery(r *http.Request, key string) (*int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil, invalidQueryError(key)
	}
	return &value, nil
}

func invalidQueryError(key string) error {
	return &requestParseError{message: "Invalid " + key}
}

type requestParseError struct {
	message string
}

func (e *requestParseError) Error() string {
	return e.message
}

func parseLimit(r *http.Request, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return fallback
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func (h *AdminLogsHandler) HandleLogStreamWebSocket(w http.ResponseWriter, r *http.Request) {
	if h.streamHub == nil {
		http.Error(w, "log stream unavailable", http.StatusServiceUnavailable)
		return
	}

	stream := logstream.Stream(strings.TrimSpace(r.URL.Query().Get("stream")))
	if stream != logstream.StreamApp && stream != logstream.StreamAudit {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid stream")
		return
	}

	var (
		appOpts   opslog.ListOptions
		auditOpts activitylog.ListOptions
		err       error
	)
	switch stream {
	case logstream.StreamApp:
		appOpts, err = parseOperationalLogOptionsFromRequest(r)
	case logstream.StreamAudit:
		auditOpts, err = parseAuditLogOptionsFromRequest(r)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	events, unsubscribe := h.streamHub.Subscribe(func(msg logstream.Message) bool {
		return msg.Type == logstream.MessageTypeAppend && msg.Stream == stream
	})
	defer unsubscribe()

	conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	buffered := drainBufferedMessages(events)
	seenIDs := make(map[int64]struct{})
	newestSnapshotID := int64(0)

	switch stream {
	case logstream.StreamApp:
		if h.opsRepo == nil {
			h.writeStreamError(conn, stream, "internal_error", "Operational log stream unavailable")
			return
		}
		result, err := h.opsRepo.List(context.Background(), appOpts)
		if err != nil {
			slog.ErrorContext(r.Context(), "admin log stream operational snapshot failed", "component", "api", "error", err)
			h.writeStreamError(conn, stream, "internal_error", "Failed to query operational logs")
			return
		}
		if len(result.Entries) > 0 {
			newestSnapshotID = result.Entries[0].ID
		}
		for _, entry := range result.Entries {
			seenIDs[entry.ID] = struct{}{}
		}
		if err := writeSnapshotMessage(conn, stream, result.Entries, result.NextCursor); err != nil {
			return
		}
		buffered = append(buffered, drainBufferedMessages(events)...)
		for _, msg := range buffered {
			entry, ok := decodeAppEntry(msg)
			if !ok {
				continue
			}
			if newestSnapshotID > 0 && entry.ID <= newestSnapshotID {
				continue
			}
			if !matchesOperationalLog(appOpts, entry) {
				continue
			}
			if _, ok := seenIDs[entry.ID]; ok {
				continue
			}
			seenIDs[entry.ID] = struct{}{}
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	case logstream.StreamAudit:
		if h.auditRepo == nil {
			h.writeStreamError(conn, stream, "internal_error", "Audit log stream unavailable")
			return
		}
		result, err := h.auditRepo.List(context.Background(), auditOpts)
		if err != nil {
			slog.ErrorContext(r.Context(), "admin log stream audit snapshot failed", "component", "api", "error", err)
			h.writeStreamError(conn, stream, "internal_error", "Failed to query audit logs")
			return
		}
		if len(result.Entries) > 0 {
			newestSnapshotID = result.Entries[0].ID
		}
		for _, entry := range result.Entries {
			seenIDs[entry.ID] = struct{}{}
		}
		if err := writeSnapshotMessage(conn, stream, result.Entries, result.NextCursor); err != nil {
			return
		}
		buffered = append(buffered, drainBufferedMessages(events)...)
		for _, msg := range buffered {
			entry, ok := decodeAuditEntry(msg)
			if !ok {
				continue
			}
			if newestSnapshotID > 0 && entry.ID <= newestSnapshotID {
				continue
			}
			if !matchesAuditLog(auditOpts, entry) {
				continue
			}
			if _, ok := seenIDs[entry.ID]; ok {
				continue
			}
			seenIDs[entry.ID] = struct{}{}
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	}

	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-done:
			return
		case msg, ok := <-events:
			if !ok {
				return
			}
			switch stream {
			case logstream.StreamApp:
				entry, ok := decodeAppEntry(msg)
				if !ok || !matchesOperationalLog(appOpts, entry) {
					continue
				}
				if _, ok := seenIDs[entry.ID]; ok {
					continue
				}
				seenIDs[entry.ID] = struct{}{}
			case logstream.StreamAudit:
				entry, ok := decodeAuditEntry(msg)
				if !ok || !matchesAuditLog(auditOpts, entry) {
					continue
				}
				if _, ok := seenIDs[entry.ID]; ok {
					continue
				}
				seenIDs[entry.ID] = struct{}{}
			}
			if err := conn.WriteJSON(msg); err != nil {
				if websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					return
				}
				return
			}
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
}

func writeSnapshotMessage(conn *websocket.Conn, stream logstream.Stream, entries any, nextCursor string) error {
	raw, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return conn.WriteJSON(logstream.Message{
		Type:       logstream.MessageTypeSnapshot,
		Stream:     stream,
		Entries:    raw,
		NextCursor: nextCursor,
	})
}

func (h *AdminLogsHandler) writeStreamError(conn *websocket.Conn, stream logstream.Stream, code, message string) {
	_ = conn.WriteJSON(logstream.Message{
		Type:    logstream.MessageTypeError,
		Stream:  stream,
		Code:    code,
		Message: message,
	})
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, message), time.Now().Add(5*time.Second))
}

func drainBufferedMessages(events <-chan logstream.Message) []logstream.Message {
	buffered := make([]logstream.Message, 0, 8)
	for {
		select {
		case msg, ok := <-events:
			if !ok {
				return buffered
			}
			buffered = append(buffered, msg)
		default:
			return buffered
		}
	}
}

func decodeAppEntry(msg logstream.Message) (opslog.EntryRow, bool) {
	var entry opslog.EntryRow
	if err := json.Unmarshal(msg.Entry, &entry); err != nil {
		return opslog.EntryRow{}, false
	}
	return entry, true
}

func decodeAuditEntry(msg logstream.Message) (activitylog.AuditEntry, bool) {
	var entry activitylog.AuditEntry
	if err := json.Unmarshal(msg.Entry, &entry); err != nil {
		return activitylog.AuditEntry{}, false
	}
	return entry, true
}

func matchesOperationalLog(opts opslog.ListOptions, entry opslog.EntryRow) bool {
	if opts.From != nil && entry.Timestamp.Before(*opts.From) {
		return false
	}
	if opts.To != nil && entry.Timestamp.After(*opts.To) {
		return false
	}
	if opts.Level != "" && entry.Level != strings.ToLower(opts.Level) {
		return false
	}
	if opts.Component != "" && entry.Component != opts.Component {
		return false
	}
	if opts.NodeID != "" && entry.NodeID != opts.NodeID {
		return false
	}
	if opts.RequestID != "" && entry.RequestID != opts.RequestID {
		return false
	}
	if opts.UserID != nil {
		if entry.UserID == nil || *entry.UserID != *opts.UserID {
			return false
		}
	}
	if opts.SessionID != "" && entry.SessionID != opts.SessionID {
		return false
	}
	if opts.PlaybackSessionID != "" && entry.PlaybackSessionID != opts.PlaybackSessionID {
		return false
	}
	if opts.Query != "" && !strings.Contains(strings.ToLower(entry.Message), strings.ToLower(opts.Query)) {
		return false
	}
	return true
}

func matchesAuditLog(opts activitylog.ListOptions, entry activitylog.AuditEntry) bool {
	if opts.From != nil && entry.Timestamp.Before(*opts.From) {
		return false
	}
	if opts.To != nil && entry.Timestamp.After(*opts.To) {
		return false
	}
	if opts.Method != "" && entry.Method != strings.ToUpper(opts.Method) {
		return false
	}
	if opts.StatusCode != nil && entry.StatusCode != *opts.StatusCode {
		return false
	}
	if opts.PathPrefix != "" && !strings.HasPrefix(entry.Path, opts.PathPrefix) {
		return false
	}
	if opts.ClientIP != "" && entry.ClientIP != opts.ClientIP {
		return false
	}
	if opts.RequestID != "" && entry.RequestID != opts.RequestID {
		return false
	}
	if opts.UserID != nil {
		if entry.UserID == nil || *entry.UserID != *opts.UserID {
			return false
		}
	}
	if opts.SessionID != "" && entry.SessionID != opts.SessionID {
		return false
	}
	if opts.PlaybackSessionID != "" && entry.PlaybackSessionID != opts.PlaybackSessionID {
		return false
	}
	return true
}
