package metadata

import (
	"fmt"
	"strings"
	"time"
)

const (
	ArtworkPurgeModeEdgeOnly         = "edge_only"
	ArtworkPurgeModeSafeMaterialized = "safe_materialized"
)

type ArtworkPurgeScope struct {
	LibraryID *int `json:"library_id,omitempty"`
	Server    bool `json:"server,omitempty"`
}

type ArtworkPurgeRequest struct {
	Scope  ArtworkPurgeScope `json:"scope"`
	Mode   string            `json:"mode"`
	DryRun bool              `json:"dry_run"`
}

func (r *ArtworkPurgeRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("artwork purge request is required")
	}
	r.Mode = strings.ToLower(strings.TrimSpace(r.Mode))
	if r.Mode != ArtworkPurgeModeEdgeOnly && r.Mode != ArtworkPurgeModeSafeMaterialized {
		return fmt.Errorf("mode must be %q or %q", ArtworkPurgeModeEdgeOnly, ArtworkPurgeModeSafeMaterialized)
	}
	if (r.Scope.LibraryID == nil) == !r.Scope.Server {
		return fmt.Errorf("scope must contain exactly one of library_id or server")
	}
	if r.Scope.LibraryID != nil && *r.Scope.LibraryID <= 0 {
		return fmt.Errorf("library_id must be positive")
	}
	return nil
}

type ArtworkPurgeCheckpoint struct {
	Version           int                            `json:"version"`
	PlanFingerprint   string                         `json:"plan_fingerprint,omitempty"`
	Phase             string                         `json:"phase,omitempty"`
	BatchIndex        int                            `json:"batch_index"`
	Targets           []ArtworkPurgeCheckpointTarget `json:"targets,omitempty"`
	QueuedPaths       []string                       `json:"queued_paths,omitempty"`
	Transitioned      int64                          `json:"transitioned"`
	QueuedRevisions   int64                          `json:"queued_revisions"`
	SharedRetained    int64                          `json:"shared_retained"`
	ProtectedSkipped  int64                          `json:"protected_skipped"`
	Failures          int64                          `json:"failures"`
	DriftedReferences int64                          `json:"drifted_references"`
	PendingBytes      int64                          `json:"pending_bytes"`
	ReclaimableBytes  int64                          `json:"reclaimable_bytes"`
	GraceDeadline     *time.Time                     `json:"grace_deadline,omitempty"`
	Finished          bool                           `json:"finished"`
}

type ArtworkPurgeCheckpointTarget struct {
	SurfaceName string   `json:"surface_name"`
	Keys        []string `json:"keys"`
	Path        string   `json:"path"`
	Source      string   `json:"source"`
	Fallback    string   `json:"fallback"`
	Shared      bool     `json:"shared"`
	Protected   bool     `json:"protected"`
	Bytes       int64    `json:"bytes"`
}

type ArtworkPurgeResult struct {
	DryRun                        bool       `json:"dry_run"`
	Transitioned                  int64      `json:"transitioned"`
	QueuedRevisions               int64      `json:"queued_revisions"`
	PendingBytes                  int64      `json:"pending_bytes"`
	BytesDeleted                  int64      `json:"bytes_deleted"`
	ReclaimableBytes              int64      `json:"reclaimable_bytes"`
	SharedRetained                int64      `json:"shared_retained"`
	ProtectedSkipped              int64      `json:"protected_skipped"`
	DriftedReferences             int64      `json:"drifted_references"`
	GraceDeadline                 *time.Time `json:"grace_deadline,omitempty"`
	Failures                      []string   `json:"failures"`
	AccountingRefreshQueued       bool       `json:"accounting_refresh_queued"`
	UntrackedUserArtworkProtected bool       `json:"untracked_user_artwork_protected"`
}
