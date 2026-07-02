// Package downloads owns the downloads domain: a single device-aware,
// quality-aware registry shared by the web app and mobile clients for offline
// playback. It replaces the former internal/download package, absorbing its
// bandwidth/quota/serving logic and reshaping the table and /downloads contract. See
// docs/superpowers/specs/2026-06-18-offline-sync-mobile-design.md.
package downloads

import (
	"errors"
	"time"
)

// Format constants are internal delivery formats recorded on a download row.
// They are not user-facing quality presets: clients choose a Quality*, then the
// service records the concrete delivery format it can fulfill.
const (
	FormatOriginal  = "original"
	FormatRemux     = "remux"
	FormatTranscode = "transcode"
)

// Quality constants are the public download choices clients may request.
const (
	QualityOriginal = "original"
	Quality20Mbps   = "20mbps"
	Quality10Mbps   = "10mbps"
	Quality5Mbps    = "5mbps"
	Quality2Mbps    = "2mbps"
	Quality1Mbps    = "1mbps"
)

// QualityPresets is the ordered UI/API ladder for download quality.
var QualityPresets = []string{
	QualityOriginal,
	Quality20Mbps,
	Quality10Mbps,
	Quality5Mbps,
	Quality2Mbps,
	Quality1Mbps,
}

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
	// [preparing ->] ready -> completed, plus revoked (reserved for the
	// admin revoke flow; see the download limits & restrictions design).
	StatusPreparing = "preparing"
	StatusReady     = "ready"
	StatusRevoked   = "revoked"
)

// Download kind constants.
const (
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
	ErrInvalidQuality         = errors.New("invalid download quality")
	ErrProfileRequired        = errors.New("managed download requires a profile")
	ErrInvalidStatus          = errors.New("invalid download status transition")
	ErrManifestUnavailable    = errors.New("offline manifest is not available")
	ErrInvalidSubtitleRef     = errors.New("invalid subtitle reference")
	ErrAssetNotFound          = errors.New("download asset not found")
	ErrFormatUnavailable      = errors.New("requested download format is not available")
	// ErrQualityUnavailable means the requested quality is valid and permitted in
	// principle but cannot be fulfilled by the current server wiring.
	ErrQualityUnavailable = errors.New("requested download quality is not available")
	// ErrBulkQualityUnavailable keeps season/series batches original-only until
	// batch artifact UX and storage reporting are explicit.
	ErrBulkQualityUnavailable = errors.New("bulk quality downloads are not available")
	// ErrNoDownloadableEpisodes means a series/season download matched a valid
	// series but none of its episodes have a downloadable file.
	ErrNoDownloadableEpisodes = errors.New("no downloadable episodes found")

	// Series-monitoring (download subscription) errors.
	ErrSubscriptionNotFound     = errors.New("download subscription not found")
	ErrSubscriptionsUnavailable = errors.New("download subscriptions are not available")
	ErrInvalidSubscriptionMode  = errors.New("invalid subscription mode")
	ErrSeasonsRequired          = errors.New("season_numbers is required for specific_seasons mode")
	ErrInvalidSeasonNumbers     = errors.New("season_numbers contains an out-of-range season number")
	ErrNotSeries                = errors.New("content_id is not a series")
)

// Download represents a row in the downloads table. It carries both the
// ephemeral/web lifecycle (DeviceID == "") and the managed device-entry
// lifecycle (DeviceID set); ProfileID/DeviceID/ArtifactID map to nullable
// columns and are empty strings when unset.
type Download struct {
	ID                string
	UserID            int
	ProfileID         string // "" for ephemeral/web rows
	DeviceID          string // "" = ephemeral; set = managed device entry
	MediaFileID       int
	ContentID         string
	EpisodeID         string
	BatchID           string
	Kind              string // direct or queued
	Status            string // see Status* constants
	Format            string // internal delivery format; see Format* constants
	Quality           string // user-requested quality; see Quality* constants
	EffectiveQuality  string // actual quality after original compatibility fallback
	TargetBitrateKbps int    // 0 for original/remux; bitrate cap for transcode
	Revision          int    // increments when a managed row is replaced in place
	ArtifactID        string // "" until a remux/transcode artifact is linked (Phase 3)
	FileSize          int64
	BytesSent         int64
	ErrorMessage      string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
}

// SkippedDownload explains why a bulk series/season request did not create a
// row for an episode.
type SkippedDownload struct {
	EpisodeID string `json:"episode_id"`
	Reason    string `json:"reason"`
}

// IsManaged reports whether this is a managed device-library entry (DeviceID set)
// rather than an ephemeral/account-level row.
func (d *Download) IsManaged() bool {
	return d.DeviceID != ""
}
