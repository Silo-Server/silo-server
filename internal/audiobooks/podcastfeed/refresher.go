// Package podcastfeed fetches and parses RSS / Atom podcast feeds and
// upserts the resulting episodes into silo's episodes table. The Refresher
// is invoked from two places: the task scheduler (periodic background
// refresh for every podcast whose refresh window has elapsed) and an admin
// POST endpoint (force-refresh for troubleshooting / manual triggers after
// seeding a feed URL).
//
// Episode identity is keyed by the feed's <guid>, stored in
// episodes.podcast_guid. Upserts via Store.UpsertPodcastEpisode are
// idempotent: re-emitted feed items update existing rows without producing
// duplicates, so listener progress rows survive feed re-emits.
package podcastfeed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/oklog/ulid/v2"
)

// PodcastFeed is one row from podcast_feeds joined to its media_items title.
// Only the fields the refresher needs are populated.
type PodcastFeed struct {
	// MediaItemID is podcast_feeds.media_item_id — the FK to media_items.content_id.
	// Used as the series_id when upserting episode rows.
	MediaItemID string
	// FeedURL is the RSS / Atom subscription URL.
	FeedURL string
	// RefreshIntervalSeconds controls how often the feed should be polled.
	// Zero or negative defaults to 6 hours.
	RefreshIntervalSeconds int
	// LastRefreshedAt is nil for feeds that have never been refreshed.
	LastRefreshedAt *time.Time
}

// PodcastEpisode is the data written to the episodes table for one RSS item.
type PodcastEpisode struct {
	// ContentID is the episodes.content_id (ULID). New episodes get a
	// fresh ULID; re-emitted episodes reuse the stored ID so foreign keys
	// from progress tables survive the refresh.
	ContentID       string
	SeriesID        string // = PodcastFeed.MediaItemID
	GUID            string // stored in episodes.podcast_guid
	Title           string
	Overview        string
	AudioURL        string // stored in episodes.podcast_audio_url
	DurationSeconds int    // stored in episodes.runtime (minutes are fine; we store seconds via adapter)
	EpisodeNumber   int    // 0 when not present in the feed
	SeasonNumber    int    // 0 when not present in the feed
	PublishedAt     *time.Time
	StillPath       string // episode cover image URL (remote)
}

// Store is the narrow database surface the Refresher needs. Implemented by
// *DBStore (this package); surfaced as an interface so tests inject a stub
// without Postgres.
type Store interface {
	// ListPodcastFeeds returns all rows from podcast_feeds. A feed with
	// an empty FeedURL must still be returned — RefreshDue will skip it.
	ListPodcastFeeds(ctx context.Context) ([]PodcastFeed, error)

	// GetEpisodeIDsByGUID returns a map of guid → content_id for every
	// episode in the given series whose podcast_guid matches any element
	// of guids. Used to reuse stored IDs across feed refreshes.
	GetEpisodeIDsByGUID(ctx context.Context, seriesID string, guids []string) (map[string]string, error)

	// UpsertPodcastEpisode inserts or updates an episode keyed by
	// (series_id, podcast_guid). On conflict the mutable fields are
	// updated; the content_id is preserved.
	UpsertPodcastEpisode(ctx context.Context, e PodcastEpisode) error

	// MarkFeedRefreshed records a refresh attempt on podcast_feeds,
	// setting last_refreshed_at = now() and last_refresh_error to the
	// supplied string (empty string clears the error).
	MarkFeedRefreshed(ctx context.Context, mediaItemID string, lastError string) error
}

// Refresher is the long-lived feed-refresh worker. One instance is created
// by the task scheduler and another can be created on demand by the admin
// force-refresh endpoint — both share the same HTTP client + parser.
type Refresher struct {
	hc     *http.Client
	parser *gofeed.Parser
}

// New builds a Refresher with a 30-second HTTP timeout. Podcast feeds are
// typically <1 MB, but the parser reads the whole document so a hard
// timeout is the safety net.
func New() *Refresher {
	return &Refresher{
		hc:     &http.Client{Timeout: 30 * time.Second},
		parser: gofeed.NewParser(),
	}
}

// WithHTTPClient overrides the default HTTP client. Used in tests to point
// at httptest.NewServer fixtures.
func (r *Refresher) WithHTTPClient(hc *http.Client) *Refresher {
	r.hc = hc
	return r
}

// RefreshDue walks every podcast_feeds row and refreshes those whose
// last_refreshed_at + refresh_interval has elapsed. Per-feed failures are
// logged at Warn and do not abort the walk. Returns the count of feeds
// that were attempted.
func (r *Refresher) RefreshDue(ctx context.Context, s Store) (int, error) {
	feeds, err := s.ListPodcastFeeds(ctx)
	if err != nil {
		return 0, fmt.Errorf("list podcast feeds: %w", err)
	}
	now := time.Now()
	attempted := 0
	for _, f := range feeds {
		if f.FeedURL == "" {
			continue
		}
		if !isDue(f, now) {
			continue
		}
		attempted++
		if err := r.RefreshOne(ctx, s, f); err != nil {
			slog.Warn("podcast feed refresh failed",
				"media_item_id", f.MediaItemID,
				"feed_url", f.FeedURL,
				"err", err.Error(),
			)
		}
	}
	return attempted, nil
}

// RefreshOne refreshes a single podcast feed. Public so the admin
// force-refresh endpoint can call it directly. Records success or failure
// in podcast_feeds.last_refresh_error regardless of outcome.
func (r *Refresher) RefreshOne(ctx context.Context, s Store, f PodcastFeed) error {
	if f.FeedURL == "" {
		_ = s.MarkFeedRefreshed(ctx, f.MediaItemID, "no feed_url configured")
		return errors.New("no feed_url configured")
	}
	feed, err := r.fetchAndParse(ctx, f.FeedURL)
	if err != nil {
		_ = s.MarkFeedRefreshed(ctx, f.MediaItemID, err.Error())
		return err
	}
	count, err := r.upsertItems(ctx, s, f, feed)
	if err != nil {
		_ = s.MarkFeedRefreshed(ctx, f.MediaItemID, err.Error())
		return err
	}
	_ = s.MarkFeedRefreshed(ctx, f.MediaItemID, "")
	slog.Debug("podcast feed refreshed",
		"media_item_id", f.MediaItemID,
		"episodes", count,
	)
	return nil
}

func (r *Refresher) fetchAndParse(ctx context.Context, feedURL string) (*gofeed.Feed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	// Some feed hosts gate on a recognisable User-Agent.
	req.Header.Set("User-Agent", "silo/podcast-refresher (+https://siloapp.com)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.5")
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %q: %w", feedURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %q: status %d", feedURL, resp.StatusCode)
	}
	feed, err := r.parser.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", feedURL, err)
	}
	return feed, nil
}

func (r *Refresher) upsertItems(ctx context.Context, s Store, f PodcastFeed, feed *gofeed.Feed) (int, error) {
	if feed == nil || len(feed.Items) == 0 {
		return 0, nil
	}

	// Collect GUIDs so we can look up existing content IDs in bulk and
	// reuse them — preserving FK targets for progress rows.
	guids := make([]string, 0, len(feed.Items))
	for _, item := range feed.Items {
		g := strings.TrimSpace(item.GUID)
		if g == "" {
			g = strings.TrimSpace(item.Link)
		}
		if g == "" {
			continue
		}
		guids = append(guids, g)
	}
	existing, err := s.GetEpisodeIDsByGUID(ctx, f.MediaItemID, guids)
	if err != nil {
		return 0, fmt.Errorf("guid lookup: %w", err)
	}

	count := 0
	for _, item := range feed.Items {
		guid := strings.TrimSpace(item.GUID)
		if guid == "" {
			guid = strings.TrimSpace(item.Link)
		}
		if guid == "" || item.Title == "" {
			continue
		}
		audioURL, _, _ := pickEnclosure(item)
		if audioURL == "" {
			// Text-only post or video — skip.
			continue
		}
		contentID, ok := existing[guid]
		if !ok {
			contentID = ulid.Make().String()
		}
		ep := PodcastEpisode{
			ContentID:       contentID,
			SeriesID:        f.MediaItemID,
			GUID:            guid,
			Title:           item.Title,
			Overview:        item.Description,
			AudioURL:        audioURL,
			DurationSeconds: durationFromItem(item),
			EpisodeNumber:   intFromItem(item, "episode"),
			SeasonNumber:    intFromItem(item, "season"),
			PublishedAt:     item.PublishedParsed,
			StillPath:       coverFromItem(item),
		}
		if err := s.UpsertPodcastEpisode(ctx, ep); err != nil {
			slog.Warn("podcast episode upsert failed",
				"media_item_id", f.MediaItemID,
				"guid", guid,
				"err", err.Error(),
			)
			continue
		}
		count++
	}
	return count, nil
}

// isDue reports whether a feed's refresh window has elapsed.
func isDue(f PodcastFeed, now time.Time) bool {
	if f.LastRefreshedAt == nil {
		return true
	}
	interval := time.Duration(f.RefreshIntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	return now.Sub(*f.LastRefreshedAt) >= interval
}

// pickEnclosure returns the first audio enclosure URL + MIME type for the
// given feed item. Returns empty strings when no audio enclosure is found.
func pickEnclosure(item *gofeed.Item) (audioURL, mimeType string, audioBytes int64) {
	for _, enc := range item.Enclosures {
		mt := strings.ToLower(enc.Type)
		if mt != "" && !strings.HasPrefix(mt, "audio/") {
			continue
		}
		audioBytes = 0
		if enc.Length != "" {
			var n int64
			_, _ = fmt.Sscan(enc.Length, &n)
			audioBytes = n
		}
		return enc.URL, enc.Type, audioBytes
	}
	return "", "", 0
}

// durationFromItem reads the iTunes-namespaced duration field from a gofeed
// item, parsing "HH:MM:SS" / "MM:SS" / "SSS" forms. Returns 0 on missing or
// unparseable input.
func durationFromItem(item *gofeed.Item) int {
	if item.ITunesExt == nil {
		return 0
	}
	raw := strings.TrimSpace(item.ITunesExt.Duration)
	if raw == "" {
		return 0
	}
	return parseDuration(raw)
}

func parseDuration(raw string) int {
	parts := strings.Split(raw, ":")
	var h, m, s int
	switch len(parts) {
	case 1:
		_, _ = fmt.Sscan(parts[0], &s)
	case 2:
		_, _ = fmt.Sscan(parts[0], &m)
		_, _ = fmt.Sscan(parts[1], &s)
	case 3:
		_, _ = fmt.Sscan(parts[0], &h)
		_, _ = fmt.Sscan(parts[1], &m)
		_, _ = fmt.Sscan(parts[2], &s)
	default:
		return 0
	}
	return h*3600 + m*60 + s
}

// intFromItem returns the iTunes-namespaced season or episode number as an
// int, or 0 if absent or unparseable.
func intFromItem(item *gofeed.Item, field string) int {
	if item.ITunesExt == nil {
		return 0
	}
	var raw string
	switch field {
	case "episode":
		raw = item.ITunesExt.Episode
	case "season":
		raw = item.ITunesExt.Season
	default:
		return 0
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var n int
	if _, err := fmt.Sscan(raw, &n); err != nil {
		return 0
	}
	return n
}

// coverFromItem extracts an episode-level cover image URL, falling back to
// the iTunes image extension when present.
func coverFromItem(item *gofeed.Item) string {
	if item.Image != nil && item.Image.URL != "" {
		return item.Image.URL
	}
	if item.ITunesExt != nil && item.ITunesExt.Image != "" {
		return item.ITunesExt.Image
	}
	return ""
}
