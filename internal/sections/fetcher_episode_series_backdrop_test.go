package sections

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEpisodeTargetsCarrySeriesBackdropBesideStill(t *testing.T) {
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
	seriesID := fmt.Sprintf("series-backdrop-series-%d", suffix)
	withStill := fmt.Sprintf("series-backdrop-still-%d", suffix)
	withoutStill := fmt.Sprintf("series-backdrop-bare-%d", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, backdrop_path, backdrop_thumbhash) VALUES ($1, 'series', 'Show', 'matched', '{}'::text[], '/series/original.jpg', 'series-hash')`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview, runtime, still_path, still_thumbhash) VALUES ($1, $3, 1, 1, 'One', '', 0, '/still/original.jpg', 'still-hash'), ($2, $3, 1, 2, 'Two', '', 0, '', '')`, withStill, withoutStill, seriesID); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}

	items, meta, err := NewFetcher(pool).fetchEpisodeTargetsByContentIDs(ctx, []string{withStill, withoutStill}, nil, nil, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("fetch episode targets: %v", err)
	}
	backdrops := map[string]string{}
	for _, item := range items {
		backdrops[item.ContentID] = item.BackdropPath
	}
	tests := []struct {
		id, backdrop string
	}{
		{withStill, "/still/original.jpg"},
		{withoutStill, "/series/original.jpg"},
	}
	for _, tt := range tests {
		if got := backdrops[tt.id]; got != tt.backdrop {
			t.Errorf("%s backdrop = %q, want %q", tt.id, got, tt.backdrop)
		}
		if got := meta[tt.id]; got.SeriesBackdropPath != "/series/original.jpg" || got.SeriesBackdropThumbhash != "series-hash" {
			t.Errorf("%s series backdrop = %q %q, want the series'", tt.id, got.SeriesBackdropPath, got.SeriesBackdropThumbhash)
		}
	}
}
