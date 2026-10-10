package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestSeriesAirDatesFollowEpisodesAndCalendarDB pins the media_items
// last_air_date_at / next_air_date_at denorm: episode writes and air-date
// edits compute both from the full episode set, and RefreshDueSeriesAirDates
// advances series whose known next episode has aired without any episode
// write.
func TestSeriesAirDatesFollowEpisodesAndCalendarDB(t *testing.T) {
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

	var hasColumn bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'media_items' AND column_name = 'next_air_date_at'
		)`).Scan(&hasColumn); err != nil {
		t.Fatalf("check next_air_date_at column: %v", err)
	}
	if !hasColumn {
		t.Skip("test database has not applied the next_air_date_at migration")
	}

	var today time.Time
	if err := pool.QueryRow(ctx, `SELECT CURRENT_DATE`).Scan(&today); err != nil {
		t.Fatalf("read CURRENT_DATE: %v", err)
	}
	day := func(offset int) *time.Time {
		d := today.AddDate(0, 0, offset)
		return &d
	}

	repo := NewEpisodeRepository(pool)
	seedSeries := func(t *testing.T, name string) (string, string) {
		t.Helper()
		seriesID := fmt.Sprintf("air-dates-%s-%d", name, time.Now().UnixNano())
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', $2)`,
			seriesID, "Air Dates "+name,
		); err != nil {
			t.Fatalf("seed series: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		})
		season := &models.Season{
			ContentID:      seriesID + "-season-1",
			SeriesID:       seriesID,
			SeasonNumber:   1,
			Title:          "Season 1",
			MetadataSource: "provider",
		}
		if err := NewSeasonRepository(pool).Upsert(ctx, season); err != nil {
			t.Fatalf("seed season: %v", err)
		}
		return seriesID, season.ContentID
	}
	episodes := func(seriesID, seasonID string, airDates ...*time.Time) []*models.Episode {
		out := make([]*models.Episode, 0, len(airDates))
		for i, airDate := range airDates {
			out = append(out, &models.Episode{
				ContentID:      fmt.Sprintf("%s-episode-%d", seriesID, i+1),
				SeriesID:       seriesID,
				SeasonID:       seasonID,
				SeasonNumber:   1,
				EpisodeNumber:  i + 1,
				Title:          fmt.Sprintf("Episode %d", i+1),
				AirDate:        airDate,
				MetadataSource: "provider",
			})
		}
		return out
	}
	format := func(d *time.Time) string {
		if d == nil {
			return "NULL"
		}
		return d.Format("2006-01-02")
	}
	assertAirDates := func(t *testing.T, seriesID string, wantLast, wantNext *time.Time) {
		t.Helper()
		var last, next *time.Time
		if err := pool.QueryRow(ctx,
			`SELECT last_air_date_at, next_air_date_at FROM media_items WHERE content_id = $1`,
			seriesID,
		).Scan(&last, &next); err != nil {
			t.Fatalf("read air dates: %v", err)
		}
		if format(last) != format(wantLast) || format(next) != format(wantNext) {
			t.Fatalf("air dates = (last %s, next %s), want (last %s, next %s)",
				format(last), format(next), format(wantLast), format(wantNext))
		}
	}
	drainDueSeries := func(t *testing.T) {
		t.Helper()
		const limit = 500
		for {
			due, _, err := repo.RefreshDueSeriesAirDates(ctx, limit)
			if err != nil {
				t.Fatalf("RefreshDueSeriesAirDates: %v", err)
			}
			if due < limit {
				return
			}
		}
	}

	t.Run("episode writes set last aired and next airing", func(t *testing.T) {
		seriesID, seasonID := seedSeries(t, "writes")
		eps := episodes(seriesID, seasonID, day(-14), day(-7), day(7), day(14))
		if err := repo.BulkUpsert(ctx, seriesID, eps[:3]); err != nil {
			t.Fatalf("BulkUpsert: %v", err)
		}
		assertAirDates(t, seriesID, day(-7), day(7))

		if err := repo.Upsert(ctx, eps[3]); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		assertAirDates(t, seriesID, day(-7), day(7))
	})

	t.Run("sweep advances series whose next episode aired", func(t *testing.T) {
		due, dueSeason := seedSeries(t, "due")
		if err := repo.BulkUpsert(ctx, due, episodes(due, dueSeason, day(-7), day(0), day(7))); err != nil {
			t.Fatalf("BulkUpsert due series: %v", err)
		}
		notDue, notDueSeason := seedSeries(t, "not-due")
		if err := repo.BulkUpsert(ctx, notDue, episodes(notDue, notDueSeason, day(-7), day(7))); err != nil {
			t.Fatalf("BulkUpsert not-due series: %v", err)
		}
		// Values as computed a day earlier, when today's episode was still
		// in the future. The not-due series carries a deliberately wrong
		// last_air_date_at to prove the sweep only touches due series.
		if _, err := pool.Exec(ctx,
			`UPDATE media_items SET last_air_date_at = $2, next_air_date_at = $3 WHERE content_id = $1`,
			due, day(-7), day(0),
		); err != nil {
			t.Fatalf("stage stale due series: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE media_items SET last_air_date_at = $2 WHERE content_id = $1`,
			notDue, day(-30),
		); err != nil {
			t.Fatalf("stage not-due series: %v", err)
		}

		drainDueSeries(t)

		assertAirDates(t, due, day(0), day(7))
		assertAirDates(t, notDue, day(-30), day(7))
	})

	t.Run("air date edits recompute the series", func(t *testing.T) {
		seriesID, seasonID := seedSeries(t, "edited")
		eps := episodes(seriesID, seasonID, day(-14), day(-7))
		if err := repo.BulkUpsert(ctx, seriesID, eps); err != nil {
			t.Fatalf("BulkUpsert: %v", err)
		}
		assertAirDates(t, seriesID, day(-7), nil)

		// Moving the newest episode into the future must give the series a
		// next air date; otherwise the sweep would never revisit it.
		moved := day(3).Format("2006-01-02")
		if err := repo.UpdateMetadata(ctx, eps[1].ContentID, &MetadataUpdate{AirDate: &moved}); err != nil {
			t.Fatalf("UpdateMetadata: %v", err)
		}
		assertAirDates(t, seriesID, day(-14), day(3))
	})

	t.Run("episode writes clear dates when air dates are removed", func(t *testing.T) {
		seriesID, seasonID := seedSeries(t, "cleared")
		if err := repo.BulkUpsert(ctx, seriesID, episodes(seriesID, seasonID, day(-7), day(7))); err != nil {
			t.Fatalf("BulkUpsert dated episodes: %v", err)
		}
		assertAirDates(t, seriesID, day(-7), day(7))

		if err := repo.BulkUpsert(ctx, seriesID, episodes(seriesID, seasonID, nil, nil)); err != nil {
			t.Fatalf("BulkUpsert undated episodes: %v", err)
		}
		assertAirDates(t, seriesID, nil, nil)
	})

	t.Run("sweep keeps a concurrent episode write's dates", func(t *testing.T) {
		seriesID, seasonID := seedSeries(t, "concurrent")
		eps := episodes(seriesID, seasonID, day(-7), day(0))
		if err := repo.BulkUpsert(ctx, seriesID, eps); err != nil {
			t.Fatalf("BulkUpsert: %v", err)
		}
		// Due: computed a day earlier, when today's episode was in the future.
		if _, err := pool.Exec(ctx,
			`UPDATE media_items SET last_air_date_at = $2, next_air_date_at = $3 WHERE content_id = $1`,
			seriesID, day(-7), day(0),
		); err != nil {
			t.Fatalf("stage due series: %v", err)
		}

		// An episode write moves today's episode to tomorrow and recomputes
		// the series, as UpdateMetadata does, but has not committed yet.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin episode write: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `UPDATE episodes SET air_date = $2 WHERE content_id = $1`, eps[1].ContentID, day(1)); err != nil {
			t.Fatalf("move episode: %v", err)
		}
		if _, err := tx.Exec(ctx, refreshSeriesAirDatesSQL, []string{seriesID}); err != nil {
			t.Fatalf("recompute series in episode write: %v", err)
		}

		swept := make(chan error, 1)
		go func() {
			_, _, err := repo.RefreshDueSeriesAirDates(ctx, 500)
			swept <- err
		}()
		sweepWaiting := func() bool {
			var waiting bool
			if err := pool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM pg_stat_activity
					WHERE wait_event_type = 'Lock' AND query LIKE '%next_airing%' AND pid <> pg_backend_pid()
				)`).Scan(&waiting); err != nil {
				t.Fatalf("check sweep lock wait: %v", err)
			}
			return waiting
		}
		// Let the sweep either finish or block on the series row before the
		// write commits; a sweep that blocks must not apply its older view.
		var sweepErr error
		sweepDone := false
		deadline := time.Now().Add(10 * time.Second)
		for !sweepDone && !sweepWaiting() {
			if time.Now().After(deadline) {
				t.Fatal("sweep neither finished nor blocked on the series row")
			}
			select {
			case sweepErr = <-swept:
				sweepDone = true
			case <-time.After(10 * time.Millisecond):
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit episode write: %v", err)
		}
		if !sweepDone {
			sweepErr = <-swept
		}
		if sweepErr != nil {
			t.Fatalf("RefreshDueSeriesAirDates: %v", sweepErr)
		}
		assertAirDates(t, seriesID, day(-7), day(1))
	})
}
