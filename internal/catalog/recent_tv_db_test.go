package catalog

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecentTVRepositoryGroupsScanBatchesAndPaginates(t *testing.T) {
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
	series := func(name string) string { return fmt.Sprintf("recent-tv-%s-%d", name, suffix) }
	episode := func(name string) string { return fmt.Sprintf("recent-tv-episode-%s-%d", name, suffix) }
	run := func(name string) string { return fmt.Sprintf("recent-tv-run-%s-%d", name, suffix) }
	seriesA, seriesB, seriesC, seriesD := series("a"), series("b"), series("c"), series("d")
	epA1, epA2 := episode("a1"), episode("a2")
	epB1, epB2, epB3, epB4 := episode("b1"), episode("b2"), episode("b3"), episode("b4")
	epC1 := episode("c1")
	runA1, runA2, runB1, runB2 := run("a1"), run("a2"), run("b1"), run("b2")

	var tvFolderID, movieFolderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, series("tv-folder")).Scan(&tvFolderID); err != nil {
		t.Fatalf("seed TV folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, series("movie-folder")).Scan(&movieFolderID); err != nil {
		t.Fatalf("seed movie folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{seriesA, seriesB, seriesC, seriesD})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{tvFolderID, movieFolderID})
	})

	base := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, created_at)
		VALUES ($1, 'series', 'Alpha Show', 'matched', '{}'::text[], $5),
		       ($2, 'series', 'Beta Show', 'matched', '{}'::text[], $5),
		       ($3, 'series', 'Classic Show', 'matched', '{}'::text[], $5),
		       ($4, 'series', 'Dormant Show', 'matched', '{}'::text[], $5)
	`, seriesA, seriesB, seriesC, seriesD, base); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $5, $6), ($2, $5, $7), ($3, $5, $8), ($4, $5, $9)
	`, seriesA, seriesB, seriesC, seriesD, tvFolderID,
		base.Add(time.Minute), base.Add(4*time.Minute), base.Add(2*time.Minute), base.Add(3*time.Minute)); err != nil {
		t.Fatalf("seed series memberships: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO scan_runs (id, media_folder_id, mode, status, requested_at, completed_at)
		VALUES ($1, $5, 'library', 'completed', $6, $6),
		       ($2, $5, 'library', 'completed', $7, $7),
		       ($3, $5, 'library', 'completed', $8, $8),
		       ($4, $5, 'library', 'completed', $9, $9)
	`, runA1, runA2, runB1, runB2, tvFolderID,
		base.Add(5*time.Minute), base.Add(10*time.Minute), base.Add(7*time.Minute), base.Add(9*time.Minute)); err != nil {
		t.Fatalf("seed scan runs: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $8, 1, 1, 'Alpha One'), ($2, $8, 2, 1, 'Alpha Two'),
		       ($3, $9, 1, 1, 'Beta One'), ($4, $9, 1, 2, 'Beta Two'),
		       ($5, $9, 2, 1, 'Beta Three'), ($6, $9, 2, 2, 'Beta Four'),
		       ($7, $10, 1, 1, 'Classic One')
	`, epA1, epA2, epB1, epB2, epB3, epB4, epC1, seriesA, seriesB, seriesC); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id)
		VALUES ($1, $8, $9, $10), ($2, $8, $11, $12),
		       ($3, $8, $13, $14), ($4, $8, $13, $14),
		       ($5, $8, $15, $16), ($6, $8, $15, $16),
		       ($7, $8, $17, NULL)
	`, epA1, epA2, epB1, epB2, epB3, epB4, epC1, tvFolderID,
		base.Add(5*time.Minute), runA1, base.Add(10*time.Minute), runA2,
		base.Add(7*time.Minute), runB1, base.Add(9*time.Minute), runB2, base.Add(2*time.Minute)); err != nil {
		t.Fatalf("seed episode memberships: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (episode_id, media_folder_id, file_path)
		SELECT episode_id, $1, '/recent-tv/' || episode_id || '.mkv'
		FROM unnest($2::text[]) AS episode_id
	`, tvFolderID, []string{epA1, epA2, epB1, epB2, epB3, epB4, epC1}); err != nil {
		t.Fatalf("seed available episode files: %v", err)
	}

	repo := NewRecentTVRepository(pool)
	targets, total, hasMore, err := repo.List(ctx, RecentTVQuery{LibraryIDs: []int{tvFolderID}, Limit: 20})
	if err != nil {
		t.Fatalf("list recent TV: %v", err)
	}
	want := []RecentTVTarget{
		{ContentID: epA2, Type: "episode", AddedAt: base.Add(10 * time.Minute), PlayContentID: epA2},
		{ContentID: seriesB, Type: "series", AddedAt: base.Add(9 * time.Minute), PlayContentID: epB3},
		{ContentID: seriesB, Type: "series", AddedAt: base.Add(7 * time.Minute), PlayContentID: epB1},
		{ContentID: epA1, Type: "episode", AddedAt: base.Add(5 * time.Minute), PlayContentID: epA1},
		{ContentID: seriesD, Type: "series", AddedAt: base.Add(3 * time.Minute)},
		{ContentID: seriesC, Type: "series", AddedAt: base.Add(2 * time.Minute), PlayContentID: epC1},
	}
	if total != len(want) || hasMore || !equalRecentTVTargets(targets, want) {
		t.Fatalf("targets = %#v, total %d, hasMore %v; want %#v", targets, total, hasMore, want)
	}

	page, pageTotal, pageHasMore, err := repo.List(ctx, RecentTVQuery{LibraryIDs: []int{tvFolderID}, Limit: 2, Offset: 1})
	if err != nil || pageTotal != len(want) || !pageHasMore || !equalRecentTVTargets(page, want[1:3]) {
		t.Fatalf("page = %#v, total %d, hasMore %v, err %v", page, pageTotal, pageHasMore, err)
	}
	preview, previewTotal, previewHasMore, err := repo.List(ctx, RecentTVQuery{
		LibraryIDs: []int{tvFolderID},
		Limit:      2,
		Offset:     1,
		SkipTotal:  true,
	})
	if err != nil || previewTotal != 0 || !previewHasMore || !equalRecentTVTargets(preview, want[1:3]) {
		t.Fatalf("count-free page = %#v, total %d, hasMore %v, err %v", preview, previewTotal, previewHasMore, err)
	}

	uniqueWant := []RecentTVTarget{
		{ContentID: epA2, Type: "episode", AddedAt: base.Add(10 * time.Minute), PlayContentID: epA2},
		{ContentID: seriesB, Type: "series", AddedAt: base.Add(9 * time.Minute), PlayContentID: epB3},
		{ContentID: epA1, Type: "episode", AddedAt: base.Add(5 * time.Minute), PlayContentID: epA1},
		{ContentID: seriesD, Type: "series", AddedAt: base.Add(3 * time.Minute)},
		{ContentID: seriesC, Type: "series", AddedAt: base.Add(2 * time.Minute), PlayContentID: epC1},
	}
	unique, uniqueTotal, uniqueHasMore, err := repo.List(ctx, RecentTVQuery{
		LibraryIDs:    []int{tvFolderID},
		Limit:         20,
		UniqueTargets: true,
	})
	if err != nil || uniqueTotal != len(uniqueWant) || uniqueHasMore || !equalRecentTVTargets(unique, uniqueWant) {
		t.Fatalf("unique targets = %#v, total %d, hasMore %v, err %v; want %#v", unique, uniqueTotal, uniqueHasMore, err, uniqueWant)
	}

	uniquePage, uniquePageTotal, uniquePageHasMore, err := repo.List(ctx, RecentTVQuery{
		LibraryIDs:    []int{tvFolderID},
		Limit:         2,
		Offset:        1,
		UniqueTargets: true,
	})
	if err != nil || uniquePageTotal != len(uniqueWant) || !uniquePageHasMore || !equalRecentTVTargets(uniquePage, uniqueWant[1:3]) {
		t.Fatalf("unique page = %#v, total %d, hasMore %v, err %v; want %#v", uniquePage, uniquePageTotal, uniquePageHasMore, err, uniqueWant[1:3])
	}

	uniquePreview, uniquePreviewTotal, uniquePreviewHasMore, err := repo.List(ctx, RecentTVQuery{
		LibraryIDs:    []int{tvFolderID},
		Limit:         2,
		Offset:        1,
		SkipTotal:     true,
		UniqueTargets: true,
	})
	if err != nil || uniquePreviewTotal != 0 || !uniquePreviewHasMore || !equalRecentTVTargets(uniquePreview, uniqueWant[1:3]) {
		t.Fatalf("count-free unique page = %#v, total %d, hasMore %v, err %v; want %#v", uniquePreview, uniquePreviewTotal, uniquePreviewHasMore, err, uniqueWant[1:3])
	}

	uniqueNamed, uniqueNamedTotal, uniqueNamedHasMore, err := repo.List(ctx, RecentTVQuery{
		LibraryIDs:    []int{tvFolderID},
		NamePrefix:    "Beta",
		Limit:         20,
		UniqueTargets: true,
	})
	if err != nil || uniqueNamedTotal != 1 || uniqueNamedHasMore || !equalRecentTVTargets(uniqueNamed, []RecentTVTarget{uniqueWant[1]}) {
		t.Fatalf("named unique targets = %#v, total %d, hasMore %v, err %v; want newest Beta event", uniqueNamed, uniqueNamedTotal, uniqueNamedHasMore, err)
	}

	empty, emptyTotal, emptyHasMore, err := repo.List(ctx, RecentTVQuery{LibraryIDs: []int{tvFolderID}, Limit: 2, Offset: 99})
	if err != nil || emptyTotal != len(want) || emptyHasMore || len(empty) != 0 {
		t.Fatalf("past-end page = %#v, total %d, hasMore %v, err %v", empty, emptyTotal, emptyHasMore, err)
	}

	restricted, _, _, err := repo.List(ctx, RecentTVQuery{
		LibraryIDs: []int{tvFolderID},
		Access:     AccessFilter{AllowedContentIDs: []string{seriesA}},
		Limit:      20,
	})
	if err != nil || !reflect.DeepEqual(targetIDs(restricted), []string{epA2, epA1}) {
		t.Fatalf("restricted targets = %#v, err %v", restricted, err)
	}

	if ids, ok, err := ResolveRecentTVLibraryIDs(ctx, pool, []int{tvFolderID}, "", AccessFilter{}); err != nil || !ok || !reflect.DeepEqual(ids, []int{tvFolderID}) {
		t.Fatalf("TV scope = %v, %v, %v", ids, ok, err)
	}
	if _, ok, err := ResolveRecentTVLibraryIDs(ctx, pool, []int{tvFolderID, movieFolderID}, "", AccessFilter{}); err != nil || ok {
		t.Fatalf("mixed implicit scope unexpectedly eligible: ok %v, err %v", ok, err)
	}
	if _, ok, err := ResolveRecentTVLibraryIDs(ctx, pool, []int{tvFolderID}, recentTVTypeEpisode, AccessFilter{}); err != nil || ok {
		t.Fatalf("episode-filtered series library unexpectedly TV scoped: ok %v, err %v", ok, err)
	}
}

func TestRecentTVRepositorySelectsFirstAvailableEpisodeFromLatestBatchSeason(t *testing.T) {
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
	id := func(name string) string { return fmt.Sprintf("recent-tv-play-%s-%d", name, suffix) }
	seriesCross, seriesMissing := id("cross"), id("missing")
	crossS3E9, crossS4E0, crossS4E1, crossS4E4 := id("cross-s3e9"), id("cross-s4e0"), id("cross-s4e1"), id("cross-s4e4")
	missingS2E4, missingS2E7 := id("missing-s2e4"), id("missing-s2e7")
	runCross, runMissing := id("run-cross"), id("run-missing")

	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, id("folder")).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{seriesCross, seriesMissing})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	base := time.Date(2026, 8, 7, 13, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, created_at)
		VALUES ($1, 'series', 'Cross Season', 'matched', '{}'::text[], $3),
		       ($2, 'series', 'Missing Episode One', 'matched', '{}'::text[], $3)
	`, seriesCross, seriesMissing, base); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
		VALUES ($1, $3, $4), ($2, $3, $4)
	`, seriesCross, seriesMissing, folderID, base); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO scan_runs (id, media_folder_id, mode, status, requested_at, completed_at)
		VALUES ($1, $3, 'library', 'completed', $4, $4),
		       ($2, $3, 'library', 'completed', $5, $5)
	`, runCross, runMissing, folderID, base.Add(10*time.Minute), base.Add(8*time.Minute)); err != nil {
		t.Fatalf("seed scan runs: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $7, 3, 9, 'Season Three Nine'),
		       ($2, $7, 4, 0, 'Unavailable Episode Zero'),
		       ($3, $7, 4, 1, 'Season Four One'),
		       ($4, $7, 4, 4, 'Season Four Four'),
		       ($5, $8, 2, 4, 'First Available'),
		       ($6, $8, 2, 7, 'Later Available')
	`, crossS3E9, crossS4E0, crossS4E1, crossS4E4, missingS2E4, missingS2E7, seriesCross, seriesMissing); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id)
		VALUES ($1, $6, $7, $8),
		       ($2, $6, $9, $8),
		       ($3, $6, $7, $8),
		       ($4, $6, $10, $11),
		       ($5, $6, $12, $11)
	`, crossS3E9, crossS4E1, crossS4E4, missingS2E4, missingS2E7, folderID,
		base.Add(10*time.Minute), runCross, base.Add(9*time.Minute),
		base.Add(7*time.Minute), runMissing, base.Add(8*time.Minute)); err != nil {
		t.Fatalf("seed episode memberships: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (episode_id, media_folder_id, file_path)
		SELECT episode_id, $1, '/recent-tv-play/' || episode_id || '.mkv'
		FROM unnest($2::text[]) AS episode_id
	`, folderID, []string{crossS3E9, crossS4E1, crossS4E4, missingS2E4, missingS2E7}); err != nil {
		t.Fatalf("seed available episode files: %v", err)
	}

	targets, total, hasMore, err := NewRecentTVRepository(pool).List(ctx, RecentTVQuery{LibraryIDs: []int{folderID}, Limit: 10})
	if err != nil {
		t.Fatalf("list recent TV: %v", err)
	}
	want := []RecentTVTarget{
		{ContentID: seriesCross, Type: "series", AddedAt: base.Add(10 * time.Minute), PlayContentID: crossS4E1},
		{ContentID: seriesMissing, Type: "series", AddedAt: base.Add(8 * time.Minute), PlayContentID: missingS2E4},
	}
	if total != len(want) || hasMore || !equalRecentTVTargets(targets, want) {
		t.Fatalf("targets = %#v, total %d, hasMore %v; want %#v", targets, total, hasMore, want)
	}
}

func TestRecentTVRepositorySnapshotKeepsSeriesWithoutEpisodesAtSnapshot(t *testing.T) {
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
	seriesID := fmt.Sprintf("recent-tv-snapshot-series-%d", suffix)
	episodeID := fmt.Sprintf("recent-tv-snapshot-episode-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, seriesID).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	base := time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC)
	snapshot := base.Add(time.Minute)
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres, created_at) VALUES ($1, 'series', 'Snapshot Show', 'matched', '{}'::text[], $2)`, seriesID, base); err != nil {
		t.Fatalf("seed snapshot series: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, seriesID, folderID, base); err != nil {
		t.Fatalf("seed snapshot series membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, 1, 'Later Episode')`, episodeID, seriesID); err != nil {
		t.Fatalf("seed later episode: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, episodeID, folderID, base.Add(2*time.Minute)); err != nil {
		t.Fatalf("seed later episode membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_files (episode_id, media_folder_id, file_path) VALUES ($1, $2, $3)`, episodeID, folderID, "/recent-tv-snapshot/"+episodeID+".mkv"); err != nil {
		t.Fatalf("seed later episode file: %v", err)
	}

	targets, total, hasMore, err := NewRecentTVRepository(pool).List(ctx, RecentTVQuery{
		LibraryIDs: []int{folderID},
		SnapshotAt: &snapshot,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("list at snapshot: %v", err)
	}
	want := []RecentTVTarget{{ContentID: seriesID, Type: "series", AddedAt: base}}
	if total != 1 || hasMore || !equalRecentTVTargets(targets, want) {
		t.Fatalf("targets = %#v, total %d, hasMore %v; want %#v", targets, total, hasMore, want)
	}
}

func targetIDs(targets []RecentTVTarget) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.ContentID)
	}
	return ids
}

func equalRecentTVTargets(got, want []RecentTVTarget) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].ContentID != want[i].ContentID || got[i].Type != want[i].Type || got[i].PlayContentID != want[i].PlayContentID || !got[i].AddedAt.Equal(want[i].AddedAt) {
			return false
		}
	}
	return true
}
