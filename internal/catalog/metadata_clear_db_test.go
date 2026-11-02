package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestMetadataUpdateClearsNullableFieldsDB pins what an admin's clear does on
// each table: the cleared columns become NULL and the rest keep their values.
// A cleared rating also loses its per-source row, which the title page would
// otherwise show in its place, along with the TMDB vote pair copied from that
// row. Clearing an episode's air date recomputes the series air dates, as an
// air-date edit does.
func TestMetadataUpdateClearsNullableFieldsDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	stamp := time.Now().UnixNano()

	t.Run("item", func(t *testing.T) {
		movie := fmt.Sprintf("clear-metadata-movie-%d", stamp)
		t.Cleanup(func() {
			batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, movie)
		})
		batchEquivExec(t, pool, `
			INSERT INTO media_items (content_id, type, title, genres, rating_imdb, rating_tmdb,
				rating_rt_critic, rating_rt_audience, release_date, first_air_date, last_air_date,
				air_time, air_timezone)
			VALUES ($1, 'movie', 'Clear Metadata', '{}'::text[], 7.2, 7.5, 90, 85, '2016-03-10',
				'2016-03-08', '2016-04-01', '20:00', 'America/New_York')`, movie)
		// The tmdb row's votes reach media_items through the vote-pair trigger.
		batchEquivExec(t, pool, `
			INSERT INTO media_item_rating_sources (content_id, source, score, votes, provider)
			VALUES ($1, 'imdb', 72, NULL, 'mdblist'), ($1, 'tmdb', 75, 20000, 'mdblist'),
				($1, 'letterboxd', 76, NULL, 'mdblist')`, movie)
		repo := NewItemRepository(pool)

		if err := repo.UpdateMetadata(ctx, movie, &MetadataUpdate{Clear: []string{"rating_imdb", "release_date"}}); err != nil {
			t.Fatalf("UpdateMetadata(clear imdb, release date): %v", err)
		}
		assertColumns(t, pool, "media_items", movie, map[string]string{
			"title": "Clear Metadata", "rating_imdb": "NULL", "rating_tmdb": "7.5",
			"rating_rt_critic": "90", "rating_rt_audience": "85", "release_date": "NULL",
			"first_air_date": "2016-03-08", "last_air_date": "2016-04-01", "air_time": "20:00",
			"air_timezone": "America/New_York", "tmdb_vote_count": "20000",
		})
		assertRatingSources(t, pool, movie, "letterboxd", "tmdb")

		// A clear combines with set fields. air_date is not an item column, so
		// the item ignores it rather than failing the write.
		title := "Cleared"
		if err := repo.UpdateMetadata(ctx, movie, &MetadataUpdate{Title: &title, Clear: []string{
			"rating_tmdb", "rating_rt_critic", "rating_rt_audience", "first_air_date",
			"last_air_date", "air_time", "air_timezone", "air_date",
		}}); err != nil {
			t.Fatalf("UpdateMetadata(clear the rest): %v", err)
		}
		assertColumns(t, pool, "media_items", movie, map[string]string{
			"title": "Cleared", "rating_imdb": "NULL", "rating_tmdb": "NULL",
			"rating_rt_critic": "NULL", "rating_rt_audience": "NULL", "release_date": "NULL",
			"first_air_date": "NULL", "last_air_date": "NULL", "air_time": "NULL",
			"air_timezone": "NULL", "tmdb_vote_count": "NULL",
		})
		// A plugin's own source has no column for an admin to clear.
		assertRatingSources(t, pool, movie, "letterboxd")
	})

	t.Run("season and episode", func(t *testing.T) {
		var today time.Time
		if err := pool.QueryRow(ctx, `SELECT CURRENT_DATE`).Scan(&today); err != nil {
			t.Fatalf("read CURRENT_DATE: %v", err)
		}
		day := func(offset int) *time.Time {
			d := today.AddDate(0, 0, offset)
			return &d
		}
		seriesID := fmt.Sprintf("clear-metadata-series-%d", stamp)
		t.Cleanup(func() {
			batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		})
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'Clear Metadata')`, seriesID)
		season := &models.Season{
			ContentID: seriesID + "-season-1", SeriesID: seriesID, SeasonNumber: 1,
			Title: "Season 1", AirDate: day(-14), MetadataSource: "provider",
		}
		seasons := NewSeasonRepository(pool)
		if err := seasons.Upsert(ctx, season); err != nil {
			t.Fatalf("seed season: %v", err)
		}
		rating := 8.1
		var eps []*models.Episode
		for i, airDate := range []*time.Time{day(-14), day(-7)} {
			eps = append(eps, &models.Episode{
				ContentID: fmt.Sprintf("%s-episode-%d", seriesID, i+1), SeriesID: seriesID,
				SeasonID: season.ContentID, SeasonNumber: 1, EpisodeNumber: i + 1,
				Title: fmt.Sprintf("Episode %d", i+1), AirDate: airDate,
				RatingIMDB: &rating, RatingTMDB: &rating, MetadataSource: "provider",
			})
		}
		episodes := NewEpisodeRepository(pool)
		if err := episodes.BulkUpsert(ctx, seriesID, eps); err != nil {
			t.Fatalf("seed episodes: %v", err)
		}
		assertColumns(t, pool, "media_items", seriesID, map[string]string{"last_air_date_at": day(-7).Format(time.DateOnly)})

		if err := seasons.UpdateMetadata(ctx, season.ContentID, &MetadataUpdate{Clear: []string{"air_date", "rating_imdb"}}); err != nil {
			t.Fatalf("season UpdateMetadata: %v", err)
		}
		assertColumns(t, pool, "seasons", season.ContentID, map[string]string{"air_date": "NULL", "title": "Season 1"})

		if err := episodes.UpdateMetadata(ctx, eps[1].ContentID, &MetadataUpdate{Clear: []string{"air_date", "rating_imdb"}}); err != nil {
			t.Fatalf("episode UpdateMetadata: %v", err)
		}
		assertColumns(t, pool, "episodes", eps[1].ContentID, map[string]string{
			"air_date": "NULL", "rating_imdb": "NULL", "rating_tmdb": "8.1",
		})
		// The newest aired episode no longer has a date, so the series falls
		// back to the one before it.
		assertColumns(t, pool, "media_items", seriesID, map[string]string{"last_air_date_at": day(-14).Format(time.DateOnly)})
	})
}

// assertColumns compares a row's columns, read as text with NULL as "NULL".
func assertColumns(t *testing.T, pool *pgxpool.Pool, table, contentID string, want map[string]string) {
	t.Helper()
	cols := make([]string, 0, len(want))
	for col := range want {
		cols = append(cols, col)
	}
	slices.Sort(cols)
	exprs := make([]string, len(cols))
	for i, col := range cols {
		exprs[i] = fmt.Sprintf("COALESCE(%s::text, 'NULL')", col)
	}
	got := make([]string, len(cols))
	dests := make([]any, len(cols))
	for i := range got {
		dests[i] = &got[i]
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE content_id = $1", strings.Join(exprs, ", "), table)
	if err := pool.QueryRow(context.Background(), query, contentID).Scan(dests...); err != nil {
		t.Fatalf("read %s %s: %v", table, contentID, err)
	}
	for i, col := range cols {
		if got[i] != want[col] {
			t.Errorf("%s.%s = %s, want %s", table, col, got[i], want[col])
		}
	}
}

// assertRatingSources compares the item's per-source rating rows.
func assertRatingSources(t *testing.T, pool *pgxpool.Pool, contentID string, want ...string) {
	t.Helper()
	var got []string
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(array_agg(source ORDER BY source), '{}') FROM media_item_rating_sources WHERE content_id = $1`,
		contentID).Scan(&got); err != nil {
		t.Fatalf("read rating sources: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("rating sources = %v, want %v", got, want)
	}
}
