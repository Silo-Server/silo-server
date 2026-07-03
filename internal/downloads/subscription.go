package downloads

import (
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Subscription monitor modes — which episodes of a series a download
// subscription keeps on the device.
const (
	// SubModeAll keeps every episode of the series (existing + future).
	SubModeAll = "all"
	// SubModeFuture keeps only episodes that become available after subscribing
	// (no backfill of the existing catalog).
	SubModeFuture = "future"
	// SubModeLatestSeason keeps the latest season at subscribe time and any newer
	// season going forward (episodes whose season_number >= TargetSeason).
	SubModeLatestSeason = "latest_season"
	// SubModeSpecificSeasons keeps only the seasons listed in SeasonNumbers.
	SubModeSpecificSeasons = "specific_seasons"
)

// ValidSubMode reports whether mode is a recognized subscription mode.
func ValidSubMode(mode string) bool {
	switch mode {
	case SubModeAll, SubModeFuture, SubModeLatestSeason, SubModeSpecificSeasons:
		return true
	default:
		return false
	}
}

// Subscription is a device-scoped opt-in to keep a series downloaded on one
// device ("monitor a series"). The client triggers a sync (on open / background
// refresh); the server then registers the in-scope, not-yet-downloaded episodes.
// It is keyed on (UserID, ProfileID, DeviceID, SeriesID) and is deliberately
// separate from the device-less, derived profile_series_interest the
// notifications system uses.
type Subscription struct {
	ID              string
	UserID          int
	ProfileID       string
	DeviceID        string
	SeriesID        string
	Mode            string // see SubMode* constants
	SeasonNumbers   []int  // monitored seasons when Mode == SubModeSpecificSeasons
	TargetSeason    *int   // latest season at subscribe time when Mode == SubModeLatestSeason
	DeleteWatched   bool   // client-enforced: delete episodes once watched
	MaxStorageBytes int64  // 0 = unlimited; client-enforced, server soft-gates auto-registration
	Active          bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CoversSeason reports whether an episode in the given season number falls
// within this subscription's monitored scope. SubModeAll and SubModeFuture cover
// every season — they differ only in SubModeFuture's air-date cutoff (see
// coversEpisode). SubModeLatestSeason covers the subscribe-time latest season and
// every newer one (season_number >= TargetSeason), so a new season is picked up
// on the next sync.
func (s *Subscription) CoversSeason(seasonNumber int) bool {
	switch s.Mode {
	case SubModeAll, SubModeFuture:
		return true
	case SubModeLatestSeason:
		return s.TargetSeason != nil && seasonNumber >= *s.TargetSeason
	case SubModeSpecificSeasons:
		for _, n := range s.SeasonNumbers {
			if n == seasonNumber {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// coversEpisode reports whether a specific available episode is in scope for a
// sync. It layers SubModeFuture's "only episodes that aired after I subscribed"
// cutoff onto the season scope, so future-only never registers the back
// catalog. An episode with no air date falls back to its ingest time.
func (s *Subscription) coversEpisode(ep *models.Episode) bool {
	if !s.CoversSeason(ep.SeasonNumber) {
		return false
	}
	if s.Mode == SubModeFuture {
		// air_date is a date-only column (midnight), so a strict instant
		// comparison against the subscribe timestamp would permanently exclude
		// an episode airing the same day the user subscribed. Compare calendar
		// days (UTC) instead; with no air date, a genuinely new episode still
		// counts as future via its ingest time.
		sc := s.CreatedAt.UTC()
		cutoff := time.Date(sc.Year(), sc.Month(), sc.Day(), 0, 0, 0, 0, time.UTC)
		if ep.AirDate != nil {
			return !ep.AirDate.Before(cutoff)
		}
		return !ep.CreatedAt.Before(s.CreatedAt)
	}
	return true
}

// Admits reports whether adding an item of addBytes keeps the device within this
// subscription's storage budget. MaxStorageBytes <= 0 means unlimited. It is the
// single home of the storage-cap rule shared by backfill and the live worker;
// the caller owns reading the current usage (best-effort, client-enforced).
func (s *Subscription) Admits(usedBytes, addBytes int64) bool {
	return s.MaxStorageBytes <= 0 || usedBytes+addBytes <= s.MaxStorageBytes
}

// SubscriptionRequest is the input to create (or re-create) a subscription. The
// identity fields are taken from the X-Silo-Device-Id header and the
// viewer-access profile, never the body.
type SubscriptionRequest struct {
	SeriesID        string
	Mode            string
	SeasonNumbers   []int
	DeleteWatched   bool
	MaxStorageBytes int64
	ProfileID       string
	DeviceID        string
	DeviceName      string
	DevicePlatform  string
}

// SubscriptionPatch is a partial update to a subscription; nil fields are left
// unchanged.
type SubscriptionPatch struct {
	Mode            *string
	SeasonNumbers   *[]int
	DeleteWatched   *bool
	MaxStorageBytes *int64
	Active          *bool
}
