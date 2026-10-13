package catalog

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestResolveVideoWithEpisodesSearch runs the search-only scope end to end on
// the PostgreSQL provider: a text search returns movies, series, and episodes
// but no audiobooks, on the relevance, sorted, and rule-filtered paths, while
// video keeps its meaning and a browse without q lists only movies and series.
func TestResolveVideoWithEpisodesSearch(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	// A letters-only token keeps full-text search from splitting the term.
	token := "vwe" + strings.Map(func(r rune) rune { return 'a' + (r - '0') }, fmt.Sprint(suffix%1_000_000_000))
	movieID := "vwe-movie-" + token
	seriesID := "vwe-series-" + token
	episodeID := "vwe-episode-" + token
	audiobookID := "vwe-audiobook-" + token

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, seriesID,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		// episodes and episode_libraries cascade from the series row.
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movieID, seriesID, audiobookID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, year)
		VALUES ($1, 'movie', $4 || ' Arrival', 'matched', '{}'::text[], 2001),
		       ($2, 'series', $4 || ' Chronicles', 'matched', '{}'::text[], 2001),
		       ($3, 'audiobook', $4 || ' Narrated', 'matched', '{}'::text[], 2001)
	`, movieID, seriesID, audiobookID, token); err != nil {
		t.Fatalf("seed media items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $2, 1, 1, $3 || ' Begins')
	`, episodeID, seriesID, token); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at) VALUES ($1, $2, NOW())
	`, episodeID, folderID); err != nil {
		t.Fatalf("seed episode library: %v", err)
	}

	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	resolve := func(t *testing.T, values url.Values) []string {
		t.Helper()
		req, err := ParseCatalogRequestWithOptions(values, CatalogRequestOptions{SearchMediaScopes: true})
		if err != nil {
			t.Fatalf("parse %v: %v", values, err)
		}
		req.CursorPaging = true
		req.Limit = 50
		result, err := resolver.Resolve(ctx, req, AccessFilter{})
		if err != nil {
			t.Fatalf("resolve %v: %v", values, err)
		}
		ids := make([]string, 0, len(result.Items))
		for _, item := range result.Items {
			ids = append(ids, item.ContentID)
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...string) []string {
		slices.Sort(ids)
		return ids
	}

	videoWithEpisodes := sorted(movieID, seriesID, episodeID)
	for _, tc := range []struct {
		name   string
		values url.Values
		want   []string
	}{
		{"relevance search", url.Values{"q": {token}, "type": {MediaScopeVideoWithEpisodes}}, videoWithEpisodes},
		{"sorted search", url.Values{"q": {token}, "type": {MediaScopeVideoWithEpisodes}, "sort": {"title"}, "order": {"asc"}}, videoWithEpisodes},
		{"rule-filtered search", url.Values{"q": {token}, "type": {MediaScopeVideoWithEpisodes}, "year_min": {"2000"}}, videoWithEpisodes},
		{"video search keeps its meaning", url.Values{"q": {token}, "type": {MediaScopeVideo}}, sorted(movieID, seriesID)},
		{"unscoped search", url.Values{"q": {token}}, sorted(movieID, seriesID, episodeID, audiobookID)},
		{"browse without q lists video", url.Values{"name_prefix": {token}, "type": {MediaScopeVideoWithEpisodes}}, sorted(movieID, seriesID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolve(t, tc.values); !slices.Equal(got, tc.want) {
				t.Fatalf("items = %v, want %v", got, tc.want)
			}
		})
	}
}
