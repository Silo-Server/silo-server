package notifications

import (
	"time"
)

// Domain types for the user-facing release-notification system
// (docs/architecture/notifications.md, "Release types"). These are unrelated
// to the operational catalog/jobs Hub defined in hub.go.

// DeliveryTypeEpisodeAvailable is the v1 primary delivery type. The type
// registry is extensible; clients must render unknown types with a generic
// fallback.
const DeliveryTypeEpisodeAvailable = "episode.available"

// SuppressedReasonStale marks release events that aged past the fanout
// staleness horizon before they could fan out (fanout disabled for a stretch,
// extended downtime); delivering them long after the fact would be noise.
const SuppressedReasonStale = "stale"

// Release event kinds. Episode events carry the series/episode columns and
// fan out to interested profiles; flat item kinds (movie, audiobook, ebook)
// carry ItemID only and exist for the server-channel broadcast feed (no
// per-profile fanout in v1). The flat kinds are described by the registry in
// item_kind.go.
const (
	EventKindEpisode   = "episode"
	EventKindMovie     = "movie"
	EventKindAudiobook = "audiobook"
	EventKindEbook     = "ebook"
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
// event. New episode events use dedupe_key
// "episode:{library_id}:{series_id}:{episode_key}" so episode-id churn from a
// series re-ID or episode-row re-mint does not create another release. Flat
// item kinds use "{kind}:{library_id}:{item_id}".
type ReleaseEvent struct {
	ID        string
	LibraryID int
	Kind      string
	// ItemID is the media_items content id for flat item kind events (movie,
	// audiobook, ebook); empty for episode events.
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

// SeriesInterest is the compact recipient-index row used by fanout. A row
// notifies only through Next Up: the profile finished an episode and waits for
// the next one (docs/architecture/notifications.md, "Eligibility rules").
type SeriesInterest struct {
	UserID    int
	ProfileID string
	LibraryID int
	SeriesID  string
	// NextUpCandidate is set while the series shows in the profile's Next Up
	// on Home and no episode of it is partly watched.
	NextUpCandidate bool
	// LastCompletedEpisodeKey is Home's Next Up anchor: the episode the
	// profile finished most recently.
	LastCompletedEpisodeKey *int
	// NextExpectedEpisodeKey is the episode Next Up waits for. With
	// NextExpectedOpen unset it is that exact episode; with it set the catalog
	// had no unstarted episode after the anchor, and the first episode keyed
	// at or past it counts.
	NextExpectedEpisodeKey *int
	NextExpectedOpen       bool
	LastNotifiedEpisodeKey *int
	UpdatedAt              time.Time
}

func (i SeriesInterest) key() profileKey { return profileKey{i.UserID, i.ProfileID} }

// ReasonFlags records which interest reasons matched for an
// episode.available delivery. Next Up is the only reason an episode notifies
// for.
type ReasonFlags struct {
	NextUp bool `json:"next_up"`
}

// ReasonNextUp is the Next Up reason's name, as ReasonFlags' JSON key spells it.
const ReasonNextUp = "next_up"

// Has reports whether the named reason is set; an unknown name never is.
func (f ReasonFlags) Has(reason string) bool {
	return reason == ReasonNextUp && f.NextUp
}

// Any reports whether at least one reason matched.
func (f ReasonFlags) Any() bool {
	return f.NextUp
}

// Preferences are the per-profile notification controls. Missing rows default
// to all-enabled.
type Preferences struct {
	UserID       int       `json:"-"`
	ProfileID    string    `json:"profile_id"`
	Enabled      bool      `json:"enabled"`
	NotifyNextUp bool      `json:"notify_next_up"`
	UpdatedAt    time.Time `json:"-"`
}

// DefaultPreferences returns the all-enabled defaults for the profile on
// userID's account.
func DefaultPreferences(userID int, profileID string) Preferences {
	return Preferences{
		UserID:       userID,
		ProfileID:    profileID,
		Enabled:      true,
		NotifyNextUp: true,
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
	// PosterSourcePath is the provider-origin artwork path preserved when
	// image caching rewrote PosterPath to a local storage key; embeds fall
	// back to it to build public provider-CDN poster URLs.
	PosterSourcePath string
	// PosterURL is the fetchable poster URL chosen for outbound embeds by
	// the sender layer (System.discordPosterURL). Transient — never read
	// from or written to the database; empty renders the embed without an
	// image.
	PosterURL string
	// Catalog enrichment rendered by outbound channels (Discord embeds).
	// Series-level fields describe the joined media_items row, which for
	// request.fulfilled deliveries is the matched movie or series itself.
	MediaType       string // media_items.type: "movie" | "series"
	Year            int
	SeriesOverview  string
	EpisodeOverview string
	Genres          []string
	ContentRating   string
	RatingIMDB      float64
	RatingTMDB      float64
	IMDBID          string
	TMDBID          string
	TVDBID          string
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

func (d InsertedDelivery) key() profileKey { return profileKey{d.UserID, d.ProfileID} }
