package models

import (
	"encoding/json"
	"time"
)

// ScanRun represents a persisted library scan orchestration row.
type ScanRun struct {
	ID              string
	MediaFolderID   int
	Mode            string
	Path            string
	Trigger         string
	Status          string
	ResultPayload   json.RawMessage
	ErrorMessage    string
	AutoscanEventID *int64
	// FollowupTrigger is set when a request for this scope arrived while the
	// run was already running. The run enqueues one follow-up scan of the same
	// scope with this trigger when it finishes. Empty means none is owed.
	FollowupTrigger string
	RequestedAt     time.Time
	StartedAt       *time.Time
	CompletedAt     *time.Time
	HeartbeatAt     *time.Time
	UpdatedAt       time.Time
}
