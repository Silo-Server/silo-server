package auditmutation

import "time"

type Entry struct {
	Action             string    `json:"action,omitempty"`
	TargetType         string    `json:"target_type,omitempty"`
	TargetID           string    `json:"target_id,omitempty"`
	Changes            []Change  `json:"changes,omitempty"`
	ID                 int64     `json:"id"`
	Timestamp          time.Time `json:"timestamp"`
	ClientIP           string    `json:"client_ip"`
	UserID             *int      `json:"user_id,omitempty"`
	ImpersonatorUserID *int      `json:"impersonator_user_id,omitempty"`
	SessionID          string    `json:"session_id,omitempty"`
	PlaybackSessionID  string    `json:"playback_session_id,omitempty"`
	RequestID          string    `json:"request_id,omitempty"`
	NodeID             string    `json:"node_id,omitempty"`
	Method             string    `json:"method"`
	Path               string    `json:"path"`
	PathPattern        string    `json:"path_pattern,omitempty"`
	StatusCode         int       `json:"status_code"`
	UserAgent          string    `json:"user_agent,omitempty"`
	DurationMs         int       `json:"duration_ms"`
}
