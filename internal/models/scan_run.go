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
	RequestedAt     time.Time
	StartedAt       *time.Time
	CompletedAt     *time.Time
	HeartbeatAt     *time.Time
	UpdatedAt       time.Time
}
