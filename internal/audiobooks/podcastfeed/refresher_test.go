package podcastfeed_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/audiobooks/podcastfeed"
)

// fakeStore implements podcastfeed.Store for unit tests. It records every
// UpsertPodcastEpisode call so assertions can inspect what the refresher
// would have written without standing up Postgres.
type fakeStore struct {
	mu sync.Mutex

	feeds           []podcastfeed.PodcastFeed
	existingByGUID  map[string]string
	upsertedEpisodes []podcastfeed.PodcastEpisode
	refreshed        map[string]string // media_item_id → last_error
}

func newFakeStore(feeds ...podcastfeed.PodcastFeed) *fakeStore {
	return &fakeStore{
		feeds:          feeds,
		existingByGUID: map[string]string{},
		refreshed:      map[string]string{},
	}
}

func (f *fakeStore) ListPodcastFeeds(_ context.Context) ([]podcastfeed.PodcastFeed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]podcastfeed.PodcastFeed(nil), f.feeds...), nil
}

func (f *fakeStore) GetEpisodeIDsByGUID(_ context.Context, _ string, guids []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, g := range guids {
		if id, ok := f.existingByGUID[g]; ok {
			out[g] = id
		}
	}
	return out, nil
}

func (f *fakeStore) UpsertPodcastEpisode(_ context.Context, e podcastfeed.PodcastEpisode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertedEpisodes = append(f.upsertedEpisodes, e)
	return nil
}

func (f *fakeStore) MarkFeedRefreshed(_ context.Context, mediaItemID string, lastError string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed[mediaItemID] = lastError
	return nil
}

// rssFixture is a small but realistic RSS 2.0 + iTunes-namespaced feed
// covering the fields the refresher actually reads.
const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
<channel>
  <title>Test Show</title>
  <itunes:author>Host McHostface</itunes:author>
  <item>
    <title>Episode 1</title>
    <description>First one.</description>
    <pubDate>Mon, 01 Apr 2026 12:00:00 GMT</pubDate>
    <guid>episode-guid-1</guid>
    <enclosure url="https://cdn.example.com/ep1.mp3" length="12345" type="audio/mpeg"/>
    <itunes:duration>00:30:00</itunes:duration>
    <itunes:episode>1</itunes:episode>
    <itunes:season>1</itunes:season>
  </item>
  <item>
    <title>Episode 2</title>
    <description>Second one.</description>
    <pubDate>Mon, 08 Apr 2026 12:00:00 GMT</pubDate>
    <guid>episode-guid-2</guid>
    <enclosure url="https://cdn.example.com/ep2.mp3" length="23456" type="audio/mpeg"/>
    <itunes:duration>45:30</itunes:duration>
    <itunes:episode>2</itunes:episode>
  </item>
</channel>
</rss>`

// TestRefreshOne_InsertsNewEpisodes drives the refresher against a fake
// feed and confirms it upserts every item with the expected fields.
func TestRefreshOne_InsertsNewEpisodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()

	feed := podcastfeed.PodcastFeed{MediaItemID: "mi-1", FeedURL: srv.URL}
	fs := newFakeStore(feed)
	r := podcastfeed.New().WithHTTPClient(srv.Client())

	if err := r.RefreshOne(context.Background(), fs, feed); err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}

	if len(fs.upsertedEpisodes) != 2 {
		t.Fatalf("upserts = %d, want 2", len(fs.upsertedEpisodes))
	}
	first := fs.upsertedEpisodes[0]
	if first.Title != "Episode 1" {
		t.Errorf("first.Title = %q", first.Title)
	}
	if first.AudioURL != "https://cdn.example.com/ep1.mp3" {
		t.Errorf("first.AudioURL = %q", first.AudioURL)
	}
	if first.GUID != "episode-guid-1" {
		t.Errorf("first.GUID = %q", first.GUID)
	}
	if first.DurationSeconds != 1800 {
		t.Errorf("first.DurationSeconds = %d, want 1800 (00:30:00)", first.DurationSeconds)
	}
	if first.EpisodeNumber != 1 {
		t.Errorf("first.EpisodeNumber = %d, want 1", first.EpisodeNumber)
	}
	if first.SeasonNumber != 1 {
		t.Errorf("first.SeasonNumber = %d, want 1", first.SeasonNumber)
	}
	if first.PublishedAt == nil {
		t.Errorf("first.PublishedAt must be parsed")
	}

	// 45:30 mm:ss must parse to 2730s on the second item.
	if fs.upsertedEpisodes[1].DurationSeconds != 2730 {
		t.Errorf("second.DurationSeconds = %d, want 2730 (45:30)", fs.upsertedEpisodes[1].DurationSeconds)
	}

	// Mark-refreshed bookkeeping must record success (empty last_error).
	if got := fs.refreshed["mi-1"]; got != "" {
		t.Errorf("refreshed[mi-1] = %q, want empty (success)", got)
	}
}

// TestRefreshOne_ReusesExistingEpisodeID confirms the idempotent-upsert
// contract: when a feed re-emits an item we've already stored, we keep
// the existing content_id so per-user progress rows don't lose their FK.
func TestRefreshOne_ReusesExistingEpisodeID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()

	feed := podcastfeed.PodcastFeed{MediaItemID: "mi-1", FeedURL: srv.URL}
	fs := newFakeStore(feed)
	// Pretend episode 1 already exists with a stored id.
	fs.existingByGUID["episode-guid-1"] = "stored-ulid-for-ep1"

	r := podcastfeed.New().WithHTTPClient(srv.Client())
	if err := r.RefreshOne(context.Background(), fs, feed); err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}

	if len(fs.upsertedEpisodes) != 2 {
		t.Fatalf("upserts = %d, want 2", len(fs.upsertedEpisodes))
	}
	if fs.upsertedEpisodes[0].ContentID != "stored-ulid-for-ep1" {
		t.Errorf("existing episode id was rotated: got %q, want stored-ulid-for-ep1",
			fs.upsertedEpisodes[0].ContentID)
	}
	// Episode 2 is new — must mint a non-empty id, but not the stored one.
	if fs.upsertedEpisodes[1].ContentID == "" || fs.upsertedEpisodes[1].ContentID == "stored-ulid-for-ep1" {
		t.Errorf("new episode id = %q (must be a fresh ULID)", fs.upsertedEpisodes[1].ContentID)
	}
}

// TestRefreshOne_UpstreamFailure records the error in the feed's
// last_refresh_error column so operators see the cause without reading logs.
func TestRefreshOne_UpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "feed gone", http.StatusNotFound)
	}))
	defer srv.Close()

	feed := podcastfeed.PodcastFeed{MediaItemID: "mi-1", FeedURL: srv.URL}
	fs := newFakeStore(feed)
	r := podcastfeed.New().WithHTTPClient(srv.Client())

	err := r.RefreshOne(context.Background(), fs, feed)
	if err == nil {
		t.Fatal("RefreshOne must surface upstream 404 as an error")
	}
	if fs.refreshed["mi-1"] == "" {
		t.Errorf("MarkFeedRefreshed not called with the error message")
	}
}

// TestRefreshDue_OnlyWalksDuePodcasts confirms the refresh-interval gate:
// a feed refreshed two minutes ago with a 6-hour interval must not be
// re-fetched.
func TestRefreshDue_OnlyWalksDuePodcasts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()

	twoMinAgo := time.Now().Add(-2 * time.Minute)
	hoursAgo := time.Now().Add(-12 * time.Hour)
	fs := newFakeStore(
		podcastfeed.PodcastFeed{
			MediaItemID:            "due",
			FeedURL:                srv.URL,
			RefreshIntervalSeconds: 21600, // 6 hours
			LastRefreshedAt:        &hoursAgo,
		},
		podcastfeed.PodcastFeed{
			MediaItemID:            "fresh",
			FeedURL:                srv.URL,
			RefreshIntervalSeconds: 21600,
			LastRefreshedAt:        &twoMinAgo,
		},
		podcastfeed.PodcastFeed{
			MediaItemID: "never",
			FeedURL:     srv.URL,
			// LastRefreshedAt is nil → always due
		},
		podcastfeed.PodcastFeed{
			MediaItemID: "noFeed",
			// FeedURL is empty → skipped without attempting
		},
	)

	r := podcastfeed.New().WithHTTPClient(srv.Client())
	attempted, err := r.RefreshDue(context.Background(), fs)
	if err != nil {
		t.Fatalf("RefreshDue: %v", err)
	}
	// "due" + "never" → 2 attempts. "fresh" + "noFeed" → skipped.
	if attempted != 2 {
		t.Errorf("attempted = %d, want 2", attempted)
	}
	if _, ok := fs.refreshed["fresh"]; ok {
		t.Errorf("fresh feed was refreshed when it should not have been")
	}
	if _, ok := fs.refreshed["due"]; !ok {
		t.Errorf("due feed was not refreshed")
	}
	if _, ok := fs.refreshed["never"]; !ok {
		t.Errorf("never-refreshed feed was not refreshed")
	}
}

// TestRefreshOne_SkipsItemsWithoutAudio drops feed items that have no
// audio enclosure (text-only posts that some podcasts mix in).
func TestRefreshOne_SkipsItemsWithoutAudio(t *testing.T) {
	const mixedFixture = `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Mixed</title>
<item><title>Text-only</title><guid>t-1</guid></item>
<item><title>Has audio</title><guid>a-1</guid>
  <enclosure url="https://cdn.example.com/a.mp3" type="audio/mpeg"/>
</item></channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mixedFixture))
	}))
	defer srv.Close()

	feed := podcastfeed.PodcastFeed{MediaItemID: "mi-p", FeedURL: srv.URL}
	fs := newFakeStore(feed)
	r := podcastfeed.New().WithHTTPClient(srv.Client())
	if err := r.RefreshOne(context.Background(), fs, feed); err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}
	if len(fs.upsertedEpisodes) != 1 {
		t.Fatalf("upserts = %d, want 1 (text-only item skipped)", len(fs.upsertedEpisodes))
	}
	if fs.upsertedEpisodes[0].Title != "Has audio" {
		t.Errorf("wrong item kept: %q", fs.upsertedEpisodes[0].Title)
	}
}
