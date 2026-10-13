package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSeriesSeasonAvailabilityDatabase(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprint(time.Now().UnixNano())
	series := "series-season-test-" + suffix
	enabled, disabled := 880000+int(time.Now().UnixNano()%9000), 0
	disabled = enabled + 1
	seed := func(stmt string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO media_folders (id, type, name, enabled) VALUES ($1, 'tvshows', 'Season test', true), ($2, 'tvshows', 'Season test off', false)`, enabled, disabled)
	seed(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'Season test')`, series)
	seed(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, air_date)
		SELECT $1::text || v.suffix, $1::text, v.season, v.episode, v.aired
		FROM (VALUES ('-s0e1', 0, 1, DATE '2020-01-01'), ('-s1e1', 1, 1, DATE '2020-01-01'), ('-s1e2', 1, 2, DATE '2020-01-08'),
		             ('-s2e1', 2, 1, DATE '2021-01-01'), ('-s2e2', 2, 2, DATE '2999-01-01'), ('-s3e1', 3, 1, NULL::date))
		     AS v(suffix, season, episode, aired)`, series)
	seed(`INSERT INTO episode_libraries (episode_id, media_folder_id)
		VALUES ($1::text || '-s0e1', $2), ($1::text || '-s1e1', $2), ($1::text || '-s1e2', $2), ($1::text || '-s2e1', $3), ($1::text || '-s3e1', $2)`,
		series, enabled, disabled)
	// Season 4: one file spans e1-e2 and links to e1 only; e3 aired but is
	// missing, and e4 is present ahead of its air date, so it must not stand
	// in for e3.
	seed(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, air_date)
		SELECT $1::text || v.suffix, $1::text, 4, v.episode, v.aired
		FROM (VALUES ('-s4e1', 1, DATE '2022-01-01'), ('-s4e2', 2, DATE '2022-01-08'), ('-s4e3', 3, DATE '2022-01-15'), ('-s4e4', 4, DATE '2999-01-01'))
		     AS v(suffix, episode, aired)`, series)
	seed(`INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1::text || '-s4e1', $2), ($1::text || '-s4e4', $2)`, series, enabled)
	// Season 5 is dated but has not aired; one episode arrived early.
	seed(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, air_date)
		SELECT $1::text || v.suffix, $1::text, 5, v.episode, v.aired
		FROM (VALUES ('-s5e1', 1, DATE '2999-01-01'), ('-s5e2', 2, DATE '2999-01-08')) AS v(suffix, episode, aired)`, series)
	seed(`INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1::text || '-s5e1', $2)`, series, enabled)
	fileID := time.Now().UnixNano()
	seed(`INSERT INTO media_files (id, media_folder_id, file_path, episode_id, multi_episode_start, multi_episode_end)
		VALUES ($1, $2, $3, $4, 1, 2)`, fileID, enabled, "/season-test/"+suffix+"/S04E01-E02.mkv", series+"-s4e1")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE id = $1`, fileID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM episodes WHERE series_id = $1`, series)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, series)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id IN ($1, $2)`, enabled, disabled)
	})

	bySeries, err := NewItemRepository(pool).SeriesSeasonAvailability(ctx, []string{series, "series-season-test-absent-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	if len(bySeries) != 1 {
		t.Fatalf("series = %d, want only the one with episodes", len(bySeries))
	}
	got := bySeries[series]
	want := map[int]SeasonAvailability{
		1: {Aired: 2, Have: 2, HaveAired: 2},              // complete
		2: {Aired: 1, Upcoming: 1, Have: 0, HaveAired: 0}, // its file is in a disabled library; e2 has not aired
		3: {Aired: 0, Have: 1, HaveAired: 0},              // no air dates yet
		4: {Aired: 3, Upcoming: 1, Have: 3, HaveAired: 2}, // e2 through the multi-episode file; e3 missing
		5: {Aired: 0, Upcoming: 2, Have: 1, HaveAired: 0}, // dated, not aired yet
	}
	if len(got) != len(want) {
		t.Fatalf("seasons = %+v, want %+v (specials excluded)", got, want)
	}
	for season, w := range want {
		if got[season] != w {
			t.Errorf("season %d = %+v, want %+v", season, got[season], w)
		}
	}
}
