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
	// Season 6 has one 1080p episode and one 2160p-only episode. Season 7 is a
	// 2160p file spanning both episodes, linked to the first.
	seed(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, air_date)
		SELECT $1::text || v.suffix, $1::text, v.season, v.episode, DATE '2023-01-01'
		FROM (VALUES ('-s6e1', 6, 1), ('-s6e2', 6, 2), ('-s7e1', 7, 1), ('-s7e2', 7, 2))
		     AS v(suffix, season, episode)`, series)
	seed(`INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES
		($1::text || '-s6e1', $2), ($1::text || '-s6e2', $2), ($1::text || '-s7e1', $2)`, series, enabled)
	fileID := time.Now().UnixNano()
	seed(`INSERT INTO media_files (id, media_folder_id, file_path, episode_id, resolution, multi_episode_start, multi_episode_end)
		VALUES ($1, $2, $3, $4, '1080p', 1, 2)`, fileID, enabled, "/season-test/"+suffix+"/S04E01-E02.mkv", series+"-s4e1")
	seed(`INSERT INTO media_files (media_folder_id, file_path, episode_id, resolution)
		SELECT $2, '/season-test/' || $1 || v.path, $3::text || v.episode, v.resolution
		FROM (VALUES
			('/S01E01.mkv', '-s1e1', '1080p'),
			('/S01E02.mkv', '-s1e2', '1080p'),
			('/S03E01.mkv', '-s3e1', '1080p'),
			('/S04E04.mkv', '-s4e4', '1080p'),
			('/S05E01.mkv', '-s5e1', '1080p'),
			('/S06E01.mkv', '-s6e1', '1080p'),
			('/S06E02.mkv', '-s6e2', '2160p')) AS v(path, episode, resolution)`,
		suffix, enabled, series)
	seed(`INSERT INTO media_files (media_folder_id, file_path, episode_id, resolution, multi_episode_start, multi_episode_end)
		VALUES ($1, $2, $3, '2160p', 1, 2)`, enabled, "/season-test/"+suffix+"/S07E01-E02.mkv", series+"-s7e1")
	seed(`INSERT INTO media_files (media_folder_id, file_path, episode_id, resolution)
		VALUES ($1, $2, $3, '1080p')`, disabled, "/season-test/"+suffix+"/S02E01.mkv", series+"-s2e1")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE file_path LIKE $1 OR id = $2`, "/season-test/"+suffix+"/%", fileID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM episodes WHERE series_id = $1`, series)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, series)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id IN ($1, $2)`, enabled, disabled)
	})

	repo := NewItemRepository(pool)
	bySeries, err := repo.SeriesSeasonAvailability(ctx, []string{series, "series-season-test-absent-" + suffix}, AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bySeries) != 1 {
		t.Fatalf("series = %d, want only the one with episodes", len(bySeries))
	}
	got := bySeries[series]
	want := map[int]SeasonAvailability{
		1: {Aired: 2, Total: 2, Have: 2, HaveAired: 2},              // complete
		2: {Aired: 1, Upcoming: 1, Total: 2, Have: 0, HaveAired: 0}, // its file is in a disabled library; e2 has not aired
		3: {Aired: 0, Total: 1, Have: 1, HaveAired: 0},              // no air dates yet
		4: {Aired: 3, Upcoming: 1, Total: 4, Have: 3, HaveAired: 2}, // e2 through the multi-episode file; e3 missing
		5: {Aired: 0, Upcoming: 2, Total: 2, Have: 1, HaveAired: 0}, // dated, not aired yet
		6: {Aired: 2, Total: 2, Have: 2, HaveAired: 2},              // 1080p and 2160p, both present
		7: {Aired: 2, Total: 2, Have: 2, HaveAired: 2},              // 2160p file spans both episodes
	}
	if len(got) != len(want) {
		t.Fatalf("seasons = %+v, want %+v (specials excluded)", got, want)
	}
	for season, w := range want {
		if got[season] != w {
			t.Errorf("season %d = %+v, want %+v", season, got[season], w)
		}
	}

	// A viewer allowed only the disabled library must not count the enabled
	// holdings that made season 1 complete for an unrestricted query.
	scoped, err := repo.SeriesSeasonAvailability(ctx, []string{series}, AccessFilter{AllowedLibraryIDs: []int{disabled}})
	if err != nil {
		t.Fatal(err)
	}
	for season := range want {
		if _, ok := scoped[series][season]; !ok {
			t.Fatalf("allowlist missing season %d", season)
		}
	}
	if scoped[series][1].Total != 2 {
		t.Fatalf("allowlist: season 1 total = %d, want 2", scoped[series][1].Total)
	}
	if scoped[series][1].Have != 0 {
		t.Fatalf("allowlist excluding enabled library: season 1 have = %d, want 0", scoped[series][1].Have)
	}
	if scoped[series][2].Have != 0 {
		// Season 2's only file is in the disabled (and allowed) folder, but
		// that folder is not enabled, so presence stays empty.
		t.Fatalf("disabled folder in allowlist: season 2 have = %d, want 0", scoped[series][2].Have)
	}

	// A 1080p ceiling keeps the 1080p episode and drops the 2160p-only episode
	// and the 2160p multi-episode file. A 2160p ceiling still counts both.
	cappedWant := map[int]SeasonAvailability{}
	for season, availability := range want {
		cappedWant[season] = availability
	}
	cappedWant[6] = SeasonAvailability{Aired: 2, Total: 2, Have: 1, HaveAired: 1}
	cappedWant[7] = SeasonAvailability{Aired: 2, Total: 2, Have: 0, HaveAired: 0}
	for _, tc := range []struct {
		ceiling string
		want    map[int]SeasonAvailability
	}{
		{ceiling: "1080p", want: cappedWant},
		{ceiling: "2160p", want: want},
	} {
		got, err := repo.SeriesSeasonAvailability(ctx, []string{series}, AccessFilter{MaxPlaybackQuality: tc.ceiling})
		if err != nil {
			t.Fatal(err)
		}
		if len(got[series]) != len(tc.want) {
			t.Fatalf("%s seasons = %+v, want %+v", tc.ceiling, got[series], tc.want)
		}
		for season, w := range tc.want {
			if got[series][season] != w {
				t.Errorf("%s season %d = %+v, want %+v", tc.ceiling, season, got[series][season], w)
			}
		}
	}
}

func TestEpisodeMetadataIncompleteSeasonsWithoutEpisodes(t *testing.T) {
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
	complete := "series-ep-list-complete-" + suffix
	gapped := "series-ep-list-gap-" + suffix
	seed := func(stmt string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'complete'), ($2, 'series', 'gapped')`, complete, gapped)
	seed(`INSERT INTO seasons (content_id, series_id, season_number) VALUES
		($1 || '-s1', $1, 1),
		($2 || '-s1', $2, 1),
		($2 || '-s2', $2, 2)`, complete, gapped)
	seed(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, air_date) VALUES
		($1 || '-s1e1', $1, 1, 1, DATE '2020-01-01'),
		($2 || '-s1e1', $2, 1, 1, DATE '2020-01-01')`, complete, gapped)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM episodes WHERE series_id IN ($1, $2)`, complete, gapped)
		_, _ = pool.Exec(context.Background(), `DELETE FROM seasons WHERE series_id IN ($1, $2)`, complete, gapped)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id IN ($1, $2)`, complete, gapped)
	})

	got, err := NewItemRepository(pool).EpisodeMetadataIncomplete(ctx, []string{complete, gapped})
	if err != nil {
		t.Fatal(err)
	}
	if got[complete] {
		t.Fatalf("series with every season listed = incomplete, want ready")
	}
	if !got[gapped] {
		t.Fatalf("series with season 2 and no episodes = ready, want incomplete")
	}
}
