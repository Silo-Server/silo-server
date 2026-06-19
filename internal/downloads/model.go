// Package downloads owns the downloads domain: a single device-aware,
// format-aware downloads registry shared by the web app and (in later phases)
// mobile clients for offline playback. It replaces the former internal/download
// package, absorbing its bandwidth/quota/serving logic and reshaping the table
// and /downloads contract. See
// docs/superpowers/specs/2026-06-18-offline-sync-mobile-design.md.
package downloads

import (
	"errors"
	"time"
)

// Format constants — the delivery format recorded on a download row.
//
// Phase 0 only fulfills FormatOriginal; FormatRemux and FormatTranscode are
// resolved/gated here but become servable when the prepare-to-file pipeline
// ships in Phase 3.
const (
	FormatOriginal  = "original"
	FormatRemux     = "remux"
	FormatTranscode = "transcode"
)

// Download status constants.
const (
	// Ephemeral / web lifecycle (unchanged from downloads v1):
	// queued -> downloading -> completed | failed | canceled.
	StatusQueued      = "queued"
	StatusDownloading = "downloading"
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusCancelled   = "cancelled" //nolint:misspell // persisted DB enum value (migration 042)

	// Managed device-entry lifecycle (Phase 1+):
	// registered -> [preparing ->] ready -> completed, plus revoked.
	StatusRegistered = "registered"
	StatusPreparing  = "preparing"
	StatusReady      = "ready"
	StatusRevoked    = "revoked"
)

// Download kind constants.
const (
	KindDirect = "direct"
	KindQueued = "queued"
)

// Sentinel errors.
var (
	ErrNotFound               = errors.New("download not found")
	ErrDownloadNotAllowed     = errors.New("user is not allowed to download")
	ErrFeatureDisabled        = errors.New("downloads are disabled")
	ErrConcurrentLimitReached = errors.New("concurrent download limit reached")
	ErrPeriodLimitReached     = errors.New("download period limit reached")
	ErrDownloadNotActive      = errors.New("download is not in an active state")
	ErrStatusConflict         = errors.New("download status transition conflict")
	ErrTranscodeDisabled      = errors.New("download transcode is disabled")
	ErrInvalidFormat          = errors.New("invalid download format")
	ErrProfileRequired        = errors.New("managed download requires a profile")
	ErrInvalidStatus          = errors.New("invalid download status transition")
	ErrManifestUnavailable    = errors.New("offline manifest is not available")
	ErrInvalidSubtitleRef     = errors.New("invalid subtitle reference")
	ErrAssetNotFound          = errors.New("download asset not found")
	// ErrFormatUnavailable means the requested format is permitted by policy but
	// cannot be fulfilled yet because the prepare-to-file pipeline (remux/
	// transcode) is not wired until Phase 3. Phase 0 serves only `original`.
	ErrFormatUnavailable = errors.New("requested download format is not available")
)

// Download represents a row in the downloads table. It carries both the
// ephemeral/web lifecycle (DeviceID == "") and the managed device-entry
// lifecycle (DeviceID set); ProfileID/DeviceID/ArtifactID map to nullable
// columns and are empty strings when unset.
type Download struct {
	ID           string
	UserID       int
	ProfileID    string // "" for ephemeral/web rows
	DeviceID     string // "" = ephemeral; set = managed device entry
	MediaFileID  int
	ContentID    string
	EpisodeID    string
	BatchID      string
	Kind         string // direct or queued
	Status       string // see Status* constants
	Format       string // see Format* constants
	ArtifactID   string // "" until a remux/transcode artifact is linked (Phase 3)
	FileSize     int64
	BytesSent    int64
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	CompletedAt  *time.Time
}

// IsManaged reports whether this is a managed device-library entry (DeviceID set)
// rather than an ephemeral/account-level row.
func (d *Download) IsManaged() bool {
	return d.DeviceID != ""
}
