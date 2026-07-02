package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEpisodeLinkBumpsLatestEpisodeAdded covers the maintenance half of the
// "Latest Episodes" sort (issue #202): linking a new episode file must bump
// the parent series' media_items.latest_episode_added_at denorm in the same
// statement, for both the single-file and the bulk link paths. Re-linking an
// already-linked episode (ON CONFLICT DO NOTHING) must not re-bump.
func TestEpisodeLinkBumpsLatestEpisodeAdded(t *testing.T) {
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
	seriesID := fmt.Sprintf("lea-series-%d", suffix)
	ep1 := fmt.Sprintf("lea-ep1-%d", suffix)
	ep2 := fmt.Sprintf("lea-ep2-%d", suffix)

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('series', 'LEA Test', true) RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'LEA Series', 'matched', '{}'::text[])
	`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	for i, epID := range []string{ep1, ep2} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, 1, $3, 'Ep')
		`, epID, seriesID, i+1); err != nil {
			t.Fatalf("seed episode %s: %v", epID, err)
		}
	}

	seedFile := func(path string, createdAt time.Time, season, episode int) int {
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, season_number, episode_number, created_at)
			VALUES ($1, $2, $3, 1024, $4, $5, $6) RETURNING id
		`, seriesID, folderID, path, season, episode, createdAt).Scan(&id); err != nil {
			t.Fatalf("seed media file %s: %v", path, err)
		}
		return id
	}
	latest := func() *time.Time {
		var v *time.Time
		if err := pool.QueryRow(ctx, `
			SELECT latest_episode_added_at FROM media_items WHERE content_id = $1
		`, seriesID).Scan(&v); err != nil {
			t.Fatalf("read latest_episode_added_at: %v", err)
		}
		return v
	}

	repo := NewFileRepository(pool)
	firstAdded := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	secondAdded := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Second)

	// Path 1: UpdateEpisodeLink on a single file.
	file1 := seedFile(fmt.Sprintf("/tmp/lea-%d-e1.mkv", suffix), firstAdded, 1, 1)
	if err := repo.UpdateEpisodeLink(ctx, file1, ep1, 1, 1); err != nil {
		t.Fatalf("UpdateEpisodeLink: %v", err)
	}
	got := latest()
	if got == nil || !got.Equal(firstAdded) {
		t.Fatalf("latest_episode_added_at after first link = %v, want %v", got, firstAdded)
	}

	// Path 2: BulkLinkEpisodesBySeries picks up the newer file and bumps.
	seedFile(fmt.Sprintf("/tmp/lea-%d-e2.mkv", suffix), secondAdded, 1, 2)
	if _, err := repo.BulkLinkEpisodesBySeries(ctx, seriesID); err != nil {
		t.Fatalf("BulkLinkEpisodesBySeries: %v", err)
	}
	got = latest()
	if got == nil || !got.Equal(secondAdded) {
		t.Fatalf("latest_episode_added_at after bulk link = %v, want %v", got, secondAdded)
	}

	// Re-linking the same episode is an ON CONFLICT no-op: no re-bump, the
	// value stays at the newest arrival.
	if err := repo.UpdateEpisodeLink(ctx, file1, ep1, 1, 1); err != nil {
		t.Fatalf("re-link UpdateEpisodeLink: %v", err)
	}
	got = latest()
	if got == nil || !got.Equal(secondAdded) {
		t.Fatalf("latest_episode_added_at after no-op re-link = %v, want unchanged %v", got, secondAdded)
	}
}
