// Package workerwire contains the versioned HTTP-only worker messages.
package workerwire

import "encoding/json"

type SourceRef struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Revision string `json:"revision"`
}

type Source struct {
	Ref        SourceRef `json:"ref"`
	Origin     string    `json:"origin"`
	URL        string    `json:"url"`
	Grant      string    `json:"grant"`
	ETag       string    `json:"etag"`
	Size       int64     `json:"size"`
	NodeID     int       `json:"node_id"`
	WorkID     string    `json:"work_id"`
	Generation uint64    `json:"generation"`
}

type Assignment struct {
	Version    int               `json:"version"`
	NodeID     int               `json:"node_id"`
	WorkID     string            `json:"work_id"`
	Generation uint64            `json:"generation"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Body       json.RawMessage   `json:"body"`
	Sources    map[string]Source `json:"sources"`
}

type Snapshot struct {
	Version         int               `json:"version"`
	NodeID          int               `json:"node_id"`
	BootID          string            `json:"boot_id"`
	Nonce           string            `json:"nonce"`
	Sequence        uint64            `json:"sequence"`
	Active          map[string]uint64 `json:"active"`
	Pending         map[string]uint64 `json:"pending,omitempty"`
	Outputs         map[string]bool   `json:"outputs,omitempty"`
	ValidForMillis  int               `json:"valid_for_millis"`
	ExecutionConfig *ExecutionConfig  `json:"execution_config,omitempty"`
}

// ExecutionConfig is the complete core-controlled runtime settings allowlist.
// Binary/device paths, scratch ownership, provider ceilings and credentials
// stay local and are deliberately absent from this wire representation.
type ExecutionConfig struct {
	Revision                 string `json:"revision"`
	SegmentRetentionSeconds  int    `json:"segment_retention_seconds"`
	SubtitleSyncCapacity     int    `json:"subtitle_sync_capacity"`
	Drain                    bool   `json:"drain"`
	OutputAuthorizationEpoch string `json:"output_authorization_epoch"`
}
