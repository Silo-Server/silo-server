package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/danielgtaylor/huma/v2"
)

type AdminOperationalLogsService interface {
	List(context.Context, opslog.ListOptions) (opslog.ListResult, error)
}

// OperationalLogAttributes is the existing collector's structured JSON payload.
// Attribute names and nested values are owned by the logging component.
type OperationalLogAttributes map[string]any

func (OperationalLogAttributes) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: true, Extensions: map[string]any{extExtensionBag: "operational-log-attributes"}}
}

type AdminOperationalLog struct {
	ID                string                   `json:"id"`
	Timestamp         Instant                  `json:"timestamp"`
	Level             string                   `json:"level"`
	Component         string                   `json:"component"`
	Message           string                   `json:"message"`
	RequestID         string                   `json:"request_id,omitempty"`
	UserID            *string                  `json:"user_id,omitempty"`
	SessionID         string                   `json:"session_id,omitempty"`
	PlaybackSessionID string                   `json:"playback_session_id,omitempty"`
	ClientIP          string                   `json:"client_ip,omitempty"`
	NodeID            string                   `json:"node_id,omitempty"`
	Attrs             OperationalLogAttributes `json:"attrs,omitempty"`
}
type AdminOperationalLogsInput struct {
	LimitParam
	Cursor            string `query:"cursor" maxLength:"8192"`
	From              string `query:"from" format:"date-time"`
	To                string `query:"to" format:"date-time"`
	Level             string `query:"level" maxLength:"1024"`
	Component         string `query:"component" maxLength:"256"`
	NodeID            string `query:"node_id" maxLength:"1024"`
	RequestID         string `query:"request_id" maxLength:"1024"`
	UserID            string `query:"user_id" pattern:"^[1-9][0-9]*$" maxLength:"20"`
	SessionID         string `query:"session_id" maxLength:"1024"`
	PlaybackSessionID string `query:"playback_session_id" maxLength:"1024"`
	Query             string `query:"q" maxLength:"4096"`
}
type AdminOperationalLogsOutput struct {
	Body Collection[AdminOperationalLog]
}

func registerAdminOperationalLogs(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/logs/app", "listAdminOperationalLogs", "admin-observability", "Read retained application logs with existing filters and descending timestamp/ID pagination. Live traversal does not guarantee snapshot or late-commit coverage."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminOperationalLogsInput) (*AdminOperationalLogsOutput, error) {
		if reg.deps.AdminOperationalLogs == nil {
			return nil, unavailable("operational logs")
		}
		opts := opslog.ListOptions{Levels: opslog.NormalizeLevels(strings.Split(in.Level, ",")), Component: strings.TrimSpace(in.Component), NodeID: strings.TrimSpace(in.NodeID), RequestID: strings.TrimSpace(in.RequestID), SessionID: strings.TrimSpace(in.SessionID), PlaybackSessionID: strings.TrimSpace(in.PlaybackSessionID), Query: strings.TrimSpace(in.Query), Limit: in.Limit}
		slices.Sort(opts.Levels)
		if in.UserID != "" {
			id, err := intOfID(ID(in.UserID))
			if err != nil || id <= 0 {
				return nil, NewProblem(TypeValidationFailed, "Invalid user_id.")
			}
			opts.UserID = &id
		}
		for _, filter := range []struct {
			raw    string
			target **time.Time
		}{{in.From, &opts.From}, {in.To, &opts.To}} {
			if filter.raw != "" {
				instant, err := time.Parse(time.RFC3339Nano, filter.raw)
				if err != nil {
					return nil, NewProblem(TypeValidationFailed, "Invalid time filter.")
				}
				*filter.target = new(instant.UTC())
			}
		}
		raw, _ := json.Marshal(opts)
		scope := CursorScope{OperationID: "listAdminOperationalLogs", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx) + "/" + viewerScopeDigest(ctx), Filter: string(raw), Sort: "timestamp_desc", Tiebreaker: "id_desc"}
		var position struct{ Source string }
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &position); p != nil {
				return nil, p
			}
			opts.Cursor = position.Source
		}
		result, err := reg.deps.AdminOperationalLogs.List(ctx, opts)
		if err != nil {
			return nil, serviceProblem(err)
		}
		if len(result.Entries) > in.Limit {
			return nil, serviceProblem(errors.New("operational log page exceeds limit"))
		}
		rows := make([]AdminOperationalLog, 0, len(result.Entries))
		for _, entry := range result.Entries {
			if entry.ID <= 0 || entry.Timestamp.IsZero() {
				return nil, serviceProblem(errors.New("invalid operational log identity or time"))
			}
			row := AdminOperationalLog{ID: strconv.FormatInt(entry.ID, 10), Timestamp: NewInstant(entry.Timestamp), Level: entry.Level, Component: entry.Component, Message: entry.Message, RequestID: entry.RequestID, SessionID: entry.SessionID, PlaybackSessionID: entry.PlaybackSessionID, ClientIP: entry.ClientIP, NodeID: entry.NodeID, Attrs: OperationalLogAttributes(entry.Attrs)}
			if entry.UserID != nil {
				row.UserID = new(strconv.Itoa(*entry.UserID))
			}
			rows = append(rows, row)
		}
		next := ""
		if result.NextCursor != "" {
			position.Source = result.NextCursor
			next, err = cursors.Encode(scope, position)
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminOperationalLogsOutput{Body: Paginated(rows, next)}, nil
	})
}
