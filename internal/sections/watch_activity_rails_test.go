package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestWatchActivityRailsCountEpisodePlaysTowardSeries pins that Trending on
// Server, Most Watched, and What Others Just Watched roll episode plays up to
// their series. History
// records an episode play against the episode, which has no media_items row,
// so a show watched only through its episodes must still rank. Titles in
// another library stay out even when they have more plays.
func TestWatchActivityRailsCountEpisodePlaysTowardSeries(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	id := func(name string) string { return fmt.Sprintf("trending-%s-%d", name, suffix) }
	seriesID, popularMovie, quietMovie, staleMovie := id("series"), id("popular-movie"), id("quiet-movie"), id("stale-movie")
	episodeOne, episodeTwo := id("episode-1"), id("episode-2")
	otherSeries, otherMovie, otherEpisode := id("other-series"), id("other-movie"), id("other-episode")
	allItems := []string{seriesID, popularMovie, quietMovie, staleMovie, otherSeries, otherMovie}

	var folderID, otherFolderID, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, id("folder")).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, id("other-folder")).Scan(&otherFolderID); err != nil {
		t.Fatalf("seed other folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`, id("user"), id("user")+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, allItems)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{folderID, otherFolderID})
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres) VALUES
			($1, 'series', 'Show', 'matched', '{}'::text[]),
			($2, 'movie', 'Popular Movie', 'matched', '{}'::text[]),
			($3, 'movie', 'Quiet Movie', 'matched', '{}'::text[]),
			($4, 'movie', 'Stale Movie', 'matched', '{}'::text[]),
			($5, 'series', 'Other Show', 'matched', '{}'::text[]),
			($6, 'movie', 'Other Movie', 'matched', '{}'::text[])`,
		seriesID, popularMovie, quietMovie, staleMovie, otherSeries, otherMovie,
	); err != nil {
		t.Fatalf("seed items: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) SELECT unnest($1::text[]), $2`,
		[]string{seriesID, popularMovie, quietMovie, staleMovie}, folderID,
	); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) SELECT unnest($1::text[]), $2`,
		[]string{otherSeries, otherMovie}, otherFolderID,
	); err != nil {
		t.Fatalf("seed other memberships: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $3, 1, 1, 'One'), ($2, $3, 1, 2, 'Two')`,
		episodeOne, episodeTwo, seriesID,
	); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, 1, 'Other')`,
		otherEpisode, otherSeries,
	); err != nil {
		t.Fatalf("seed other episode: %v", err)
	}

	now := time.Now()
	plays := []struct {
		profile, item string
		at            time.Time
	}{
		// Three profiles watch different episodes of the show: three
		// viewers, four plays.
		{"profile-a", episodeOne, now.Add(-time.Hour)},
		{"profile-a", episodeTwo, now.Add(-30 * time.Minute)},
		{"profile-b", episodeOne, now.Add(-2 * time.Hour)},
		{"profile-c", episodeTwo, now.Add(-3 * time.Hour)},
		// Two profiles watch the popular movie: two viewers.
		{"profile-a", popularMovie, now.Add(-time.Hour)},
		{"profile-b", popularMovie, now.Add(-90 * time.Minute)},
		// One profile watches the quiet movie three times: one viewer.
		{"profile-c", quietMovie, now.Add(-time.Hour)},
		{"profile-c", quietMovie, now.Add(-2 * time.Hour)},
		{"profile-c", quietMovie, now.Add(-3 * time.Hour)},
		// Plays outside the window never count.
		{"profile-a", staleMovie, now.Add(-40 * 24 * time.Hour)},
		{"profile-b", staleMovie, now.Add(-40 * 24 * time.Hour)},
		{"profile-c", staleMovie, now.Add(-40 * 24 * time.Hour)},
		{"profile-a", episodeOne, now.Add(-40 * 24 * time.Hour)},
		// The other library's titles outrank everything but are out of scope.
		{"profile-a", otherEpisode, now.Add(-time.Hour)},
		{"profile-b", otherEpisode, now.Add(-time.Hour)},
		{"profile-c", otherEpisode, now.Add(-time.Hour)},
		{"profile-d", otherEpisode, now.Add(-time.Hour)},
		{"profile-a", otherMovie, now.Add(-time.Hour)},
		{"profile-b", otherMovie, now.Add(-time.Hour)},
		{"profile-c", otherMovie, now.Add(-time.Hour)},
		{"profile-d", otherMovie, now.Add(-time.Hour)},
		{"profile-d", otherMovie, now.Add(-2 * time.Hour)},
	}
	for i, p := range plays {
		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed) VALUES ($1, $2, $3, $4, $5, true)`,
			fmt.Sprintf("%s-%d", id("history"), i), userID, p.profile, p.item, p.at,
		); err != nil {
			t.Fatalf("seed history %d: %v", i, err)
		}
	}

	fetcher := NewFetcher(pool)
	activityFeed := func(viewer, config string) func(context.Context, ResolvedSection, *int, []int, catalog.AccessFilter) ([]*models.MediaItem, int, error) {
		return func(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
			s.Config = json.RawMessage(config)
			return fetcher.fetchProfileActivityFeed(ctx, s, libraryID, libraryIDs, viewer, filter)
		}
	}
	tests := []struct {
		name  string
		fetch func(context.Context, ResolvedSection, *int, []int, catalog.AccessFilter) ([]*models.MediaItem, int, error)
		want  []string
	}{
		// Viewers first: show (3), popular movie (2), quiet movie (1).
		{name: "trending on server", fetch: fetcher.fetchTrending, want: []string{seriesID, popularMovie, quietMovie}},
		// Plays first: show (4), quiet movie (3), popular movie (2).
		{name: "most watched", fetch: fetcher.fetchMostWatched, want: []string{seriesID, quietMovie, popularMovie}},
		// Other profiles' plays, latest first: show (profile-a, 30m ago) once
		// for both episodes, then the popular movie (1h). The quiet movie has
		// only profile-c's plays.
		{name: "what others just watched", fetch: activityFeed("profile-c", `{"profile_id":""}`), want: []string{seriesID, popularMovie}},
		// One profile's plays, latest first: quiet movie (1h), show (3h).
		{name: "one profile's activity", fetch: activityFeed("profile-a", `{"profile_id":"profile-c"}`), want: []string{quietMovie, seriesID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, total, err := tt.fetch(ctx, ResolvedSection{ItemLimit: 10}, &folderID, nil, catalog.AccessFilter{})
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			got := make([]string, len(items))
			for i, item := range items {
				got[i] = item.ContentID
			}
			if total != len(tt.want) || fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("items = %v (total %d), want %v", got, total, tt.want)
			}
		})
	}
}
