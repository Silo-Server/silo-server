package intromarkers

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/migrations"
)

// TestSeasonStateIsKeyedByAnalysisHashPostgres stores two kinds' season state
// for one group side by side.
func TestSeasonStateIsKeyedByAnalysisHashPostgres(t *testing.T) {
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
	if err := database.RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	fileID := seedSilenceBackfillFixture(t, pool)[0]
	state := SeasonState{AnalysisGroupKey: "default|default|und", InputSignature: "signature", Status: seasonStatusPartial, LastError: "1 fingerprint extraction(s) failed"}
	if err := pool.QueryRow(ctx, `
		SELECT e.season_id, mf.media_folder_id
		FROM media_files mf JOIN episodes e ON e.content_id = mf.episode_id
		WHERE mf.id = $1`, fileID).Scan(&state.SeasonID, &state.MediaFolderID); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(pool)
	introHash := DefaultConfig("ffmpeg").AnalysisConfigHash()
	otherHash := mediaartifact.ConfigHash("credits_test", "season")
	before := time.Now().Add(-time.Minute)
	if err := repo.UpsertSeasonState(ctx, state, introHash); err != nil {
		t.Fatal(err)
	}
	if other, err := repo.LoadSeasonState(ctx, state, otherHash); err != nil || other != nil {
		t.Fatalf("state under another kind's hash = %+v, %v; want none", other, err)
	}
	other := state
	other.Status = seasonStatusNotFound
	if err := repo.UpsertSeasonState(ctx, other, otherHash); err != nil {
		t.Fatal(err)
	}
	for hash, want := range map[string]string{introHash: seasonStatusPartial, otherHash: seasonStatusNotFound} {
		loaded, err := repo.LoadSeasonState(ctx, state, hash)
		if err != nil {
			t.Fatal(err)
		}
		if loaded == nil || loaded.Status != want {
			t.Fatalf("state under %s = %+v, want status %s", hash, loaded, want)
		}
		if hash == introHash {
			if loaded.LastError != state.LastError || loaded.AnalyzedAt.Before(before) {
				t.Fatalf("loaded state = %+v, want the saved error and a fresh analyzed_at", loaded)
			}
			now := time.Now()
			if !loaded.settled(now) || loaded.settled(now.Add(partialSeasonRetryInterval)) {
				t.Fatalf("partial state should hold for the retry interval and lapse after it: %+v", loaded)
			}
		}
	}
}

// TestEligibleCandidatePagesHoldWholeSeasonsPostgres pages the library-wide
// episode candidates one season at a time: a season's files in two libraries
// share a page, and a season with no eligible file still advances the cursor.
func TestEligibleCandidatePagesHoldWholeSeasonsPostgres(t *testing.T) {
	pool := movieTestPool(t)
	ctx := t.Context()
	prefix := fmt.Sprintf("episode-page-%d-", time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	seriesID := prefix + "series"
	var folderIDs []int
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, folderID := range folderIDs {
			for _, stmt := range []string{
				`DELETE FROM media_files WHERE media_folder_id = $1`,
				`DELETE FROM media_folders WHERE id = $1`,
			} {
				if _, err := pool.Exec(cleanup, stmt, folderID); err != nil {
					t.Errorf("clean episode page fixture: %v", err)
				}
			}
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID); err != nil {
			t.Errorf("clean episode page fixture: %v", err)
		}
	})
	folder := func(name string, detection bool) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled, intro_detection_enabled) VALUES ('series', $1, true, $2) RETURNING id`, prefix+name, detection).Scan(&id); err != nil {
			t.Fatal(err)
		}
		folderIDs = append(folderIDs, id)
		return id
	}
	first, second, off := folder("first", true), folder("second", true), folder("off", false)

	exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'Episode pages')`, seriesID)
	file := func(folderID int, season, episode int) int {
		t.Helper()
		seasonID := fmt.Sprintf("%ss%d", prefix, season)
		episodeID := fmt.Sprintf("%ss%de%d", prefix, season, episode)
		exec(`INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, seasonID, seriesID, season)
		exec(`INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number) VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`, episodeID, seriesID, seasonID, season, episode)
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, episode_id, season_number, episode_number, file_size, duration)
			VALUES ($1, $2, $3, $4, $5, 1000000, 1500)
			RETURNING id`,
			folderID, fmt.Sprintf("/%s/%d/%s.mkv", prefix, folderID, episodeID), episodeID, season, episode,
		).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Season 1 has an episode in both libraries; season 2 only in a library
	// without marker detection.
	seasonOne := []int{file(first, 1, 1), file(second, 1, 1), file(first, 1, 2)}
	file(off, 2, 1)
	seasonThree := []int{file(first, 3, 1)}

	repo := NewRepository(pool)
	after := &episodeCandidateCursor{seasonID: prefix}
	for i, want := range [][]int{seasonOne, nil, seasonThree} {
		candidates, next, err := repo.ListEligibleCandidatePage(ctx, after, 1)
		if err != nil {
			t.Fatalf("page %d: %v", i+1, err)
		}
		got := candidateIDs(candidates)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("page %d files = %v, want %v", i+1, got, want)
		}
		if next == nil {
			t.Fatalf("page %d: cursor ended with seasons left", i+1)
		}
		after = next
	}
}
