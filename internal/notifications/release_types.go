package notifications

import (
	"time"
)

// Domain types for the user-facing release-notification system
// (docs/superpowers/plans/notifications/). These are unrelated to the
// operational catalog/jobs Hub defined in hub.go.

// DeliveryTypeEpisodeAvailable is the v1 primary delivery type. The type
// registry is extensible; clients must render unknown types with a generic
// fallback.
const DeliveryTypeEpisodeAvailable = "episode.available"

// SuppressedReasonSeriesBurst marks release events consumed by the per-series
// burst cap without fanout.
const SuppressedReasonSeriesBurst = "series_burst"

// SuppressedReasonStale marks release events that aged past the fanout
// staleness horizon before they could fan out (fanout disabled for a stretch,
// extended downtime); delivering them long after the fact would be noise.
const SuppressedReasonStale = "stale"

// Release event kinds. Episode events carry the series/episode columns and
// fan out to interested profiles; movie events carry ItemID only and exist
// for the server-channel broadcast feed (no per-profile fanout in v1).
const (
	EventKindEpisode = "episode"
	EventKindMovie   = "movie"
)

// normalizeEventKind treats an unset kind as episode — the single home of
// that rule. The column is NOT NULL DEFAULT 'episode', so only in-memory
// constructed events can carry an empty kind.
func normalizeEventKind(kind string) string {
	if kind == "" {
		return EventKindEpisode
	}
	return kind
}

// ReleaseEvent is one logical "content became newly available in a library"
// event. dedupe_key is "{library_id}:{episode_id}" for episodes and
// "movie:{library_id}:{item_id}" for movies.
type ReleaseEvent struct {
	ID        string
	LibraryID int
	Kind      string
	// ItemID is the media_items content id for movie events; empty for
	// episode events.
	ItemID           string
	SeriesID         string
	EpisodeID        string
	SeasonNumber     int
	EpisodeNumber    int
	EpisodeKey       int
	AvailableAt      time.Time
	DedupeKey        string
	ProcessedAt      *time.Time
	SuppressedReason *string
	CreatedAt        time.Time
}

// SeriesInterest is the compact recipient-index row used by fanout.
type SeriesInterest struct {
	UserID                  int
	ProfileID               string
	LibraryID               int
	SeriesID                string
	Favorite                bool
	Watchlist               bool
	ContinueWatching        bool
	NextUpCandidate         bool
	LastCompletedEpisodeKey *int
	NextExpectedEpisodeKey  *int
	LastNotifiedEpisodeKey  *int
	UpdatedAt               time.Time
}

// HasAnyInterest reports whether at least one interest flag is set.
func (i SeriesInterest) HasAnyInterest() bool {
	return i.Favorite || i.Watchlist || i.ContinueWatching || i.NextUpCandidate
}

// ReasonFlags records which interest reasons matched for an
// episode.available delivery.
type ReasonFlags struct {
	Favorite         bool `json:"favorite"`
	Watchlist        bool `json:"watchlist"`
	ContinueWatching bool `json:"continue_watching"`
	NextUp           bool `json:"next_up"`
}

// Any reports whether at least one reason matched.
func (f ReasonFlags) Any() bool {
	return f.Favorite || f.Watchlist || f.ContinueWatching || f.NextUp
}

// Preferences are the per-profile notification controls. Missing rows default
// to all-enabled.
type Preferences struct {
	ProfileID              string    `json:"profile_id"`
	Enabled                bool      `json:"enabled"`
	NotifyFavorites        bool      `json:"notify_favorites"`
	NotifyWatchlist        bool      `json:"notify_watchlist"`
	NotifyContinueWatching bool      `json:"notify_continue_watching"`
	NotifyNextUp           bool      `json:"notify_next_up"`
	UpdatedAt              time.Time `json:"-"`
}

// DefaultPreferences returns the all-enabled defaults for a profile.
func DefaultPreferences(profileID string) Preferences {
	return Preferences{
		ProfileID:              profileID,
		Enabled:                true,
		NotifyFavorites:        true,
		NotifyWatchlist:        true,
		NotifyContinueWatching: true,
		NotifyNextUp:           true,
	}
}

// Delivery is a durable per-profile inbox row.
type Delivery struct {
	ID             string
	ReleaseEventID *string
	UserID         int
	ProfileID      string
	LibraryID      *int
	SeriesID       *string
	EpisodeID      *string
	Type           string
	ReasonFlags    []byte // raw JSONB payload
	Status         string
	ReadAt         *time.Time
	DeliveredAt    *time.Time
	CreatedAt      time.Time
}

// DeliveryRow is a delivery enriched with the display metadata clients need
// to render a row without an extra lookup. It is the shared shape for the
// inbox API, the websocket snapshot, and realtime dispatch payloads.
type DeliveryRow struct {
	Delivery
	SeriesTitle     string
	EpisodeTitle    string
	SeasonNumber    *int
	EpisodeNumber   *int
	PosterPath      string
	PosterThumbhash string
}

// InsertedDelivery identifies a delivery row actually inserted by a bulk
// insert (as opposed to deduped by ON CONFLICT DO NOTHING). Realtime publish
// and channel dispatch must operate on this set only.
type InsertedDelivery struct {
	ID        string
	UserID    int
	ProfileID string
	CreatedAt time.Time
}
