package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/activitylog"
)

type AdminAuditLogsService interface {
	List(context.Context, activitylog.ListOptions) (activitylog.ListResult, error)
}

type AdminAuditLog struct {
	ID                 string  `json:"id"`
	Timestamp          Instant `json:"timestamp"`
	ClientIP           string  `json:"client_ip"`
	UserID             *string `json:"user_id,omitempty"`
	ImpersonatorUserID *string `json:"impersonator_user_id,omitempty"`
	SessionID          string  `json:"session_id,omitempty"`
	PlaybackSessionID  string  `json:"playback_session_id,omitempty"`
	RequestID          string  `json:"request_id,omitempty"`
	NodeID             string  `json:"node_id,omitempty"`
	Method             string  `json:"method"`
	Path               string  `json:"path"`
	PathPattern        string  `json:"path_pattern,omitempty"`
	StatusCode         int     `json:"status_code"`
	UserAgent          string  `json:"user_agent,omitempty"`
	DurationMs         int     `json:"duration_ms"`
}
type AdminAuditLogsInput struct {
	LimitParam
	Cursor            string `query:"cursor" maxLength:"8192"`
	From              string `query:"from" format:"date-time"`
	To                string `query:"to" format:"date-time"`
	Method            string `query:"method" maxLength:"64"`
	StatusCode        string `query:"status_code" pattern:"^-?[0-9]+$" maxLength:"20"`
	PathPrefix        string `query:"path_prefix" maxLength:"4096"`
	ClientIP          string `query:"client_ip" maxLength:"128"`
	RequestID         string `query:"request_id" maxLength:"1024"`
	UserID            string `query:"user_id" pattern:"^[1-9][0-9]*$" maxLength:"20"`
	SessionID         string `query:"session_id" maxLength:"1024"`
	PlaybackSessionID string `query:"playback_session_id" maxLength:"1024"`
}
type AdminAuditLogsOutput struct {
	Body Collection[AdminAuditLog]
}

func registerAdminAuditLogs(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/logs/audit", "listAdminAuditLogs", "admin-observability", "Read retained audit logs with existing filters and descending timestamp/ID pagination. Live traversal does not guarantee snapshot or late-commit coverage."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminAuditLogsInput) (*AdminAuditLogsOutput, error) {
		if reg.deps.AdminAuditLogs == nil {
			return nil, unavailable("audit logs")
		}
		opts := activitylog.ListOptions{Method: strings.ToUpper(strings.TrimSpace(in.Method)), PathPrefix: strings.TrimSpace(in.PathPrefix), ClientIP: strings.TrimSpace(in.ClientIP), RequestID: strings.TrimSpace(in.RequestID), SessionID: strings.TrimSpace(in.SessionID), PlaybackSessionID: strings.TrimSpace(in.PlaybackSessionID), Limit: in.Limit}
		if in.StatusCode != "" {
			code, err := strconv.Atoi(in.StatusCode)
			if err != nil {
				return nil, NewProblem(TypeValidationFailed, "Invalid status_code.")
			}
			opts.StatusCode = new(code)
		}
		if opts.ClientIP != "" {
			if addr, err := netip.ParseAddr(opts.ClientIP); err == nil && addr.Zone() == "" {
				opts.ClientIP = addr.String()
			} else if prefix, err := netip.ParsePrefix(opts.ClientIP); err == nil {
				// Keep host bits: PostgreSQL inet equality includes the address and mask.
				opts.ClientIP = prefix.String()
			} else {
				return nil, NewProblem(TypeValidationFailed, "Invalid client_ip.")
			}
		}
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
		scope := CursorScope{OperationID: "listAdminAuditLogs", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx) + "/" + viewerScopeDigest(ctx), Filter: string(raw), Sort: "timestamp_desc", Tiebreaker: "id_desc"}
		var position struct{ Source string }
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &position); p != nil {
				return nil, p
			}
			opts.Cursor = position.Source
		}
		result, err := reg.deps.AdminAuditLogs.List(ctx, opts)
		if err != nil {
			return nil, serviceProblem(err)
		}
		if len(result.Entries) > in.Limit {
			return nil, serviceProblem(errors.New("audit log page exceeds limit"))
		}
		rows := make([]AdminAuditLog, 0, len(result.Entries))
		for _, entry := range result.Entries {
			if entry.ID <= 0 || entry.Timestamp.IsZero() {
				return nil, serviceProblem(errors.New("invalid audit log identity or time"))
			}
			row := AdminAuditLog{ID: strconv.FormatInt(entry.ID, 10), Timestamp: NewInstant(entry.Timestamp), ClientIP: entry.ClientIP, SessionID: entry.SessionID, PlaybackSessionID: entry.PlaybackSessionID, RequestID: entry.RequestID, NodeID: entry.NodeID, Method: entry.Method, Path: entry.Path, PathPattern: entry.PathPattern, StatusCode: entry.StatusCode, UserAgent: entry.UserAgent, DurationMs: entry.DurationMs}
			if entry.ImpersonatorUserID != nil {
				row.ImpersonatorUserID = new(strconv.Itoa(*entry.ImpersonatorUserID))
			}
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
		return &AdminAuditLogsOutput{Body: Paginated(rows, next)}, nil
	})
}
