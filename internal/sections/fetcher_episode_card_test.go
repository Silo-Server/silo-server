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

func TestEpisodeTargetsCarryAirDateAndRatings(t *testing.T) {
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
	seriesID := fmt.Sprintf("episode-card-series-%d", suffix)
	aired := fmt.Sprintf("episode-card-aired-%d", suffix)
	unaired := fmt.Sprintf("episode-card-unaired-%d", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'series', 'Show', 'matched', '{}'::text[])`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview, runtime, air_date, rating_imdb, rating_tmdb) VALUES ($1, $3, 1, 1, 'One', '', 0, '2021-06-11', 7.9, 8.39), ($2, $3, 1, 2, 'Two', '', 0, NULL, NULL, NULL)`, aired, unaired, seriesID); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}

	items, _, err := NewFetcher(pool).fetchEpisodeTargetsByContentIDs(ctx, []string{aired, unaired}, nil, nil, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("fetch episode targets: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	for _, item := range items {
		switch item.ContentID {
		case aired:
			if item.ReleaseDate == nil || *item.ReleaseDate != "2021-06-11" {
				t.Errorf("release date = %v, want 2021-06-11", item.ReleaseDate)
			}
			if item.RatingIMDB == nil || *item.RatingIMDB != 7.9 || item.RatingTMDB == nil || *item.RatingTMDB != 8.39 {
				t.Errorf("ratings = %v, %v; want 7.9, 8.39", item.RatingIMDB, item.RatingTMDB)
			}
		case unaired:
			if item.ReleaseDate != nil || item.RatingIMDB != nil || item.RatingTMDB != nil {
				t.Errorf("unaired episode = %v, %v, %v; want none", item.ReleaseDate, item.RatingIMDB, item.RatingTMDB)
			}
		}
	}
}
