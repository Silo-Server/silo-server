package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEpisodeLinkMaintainsLatestEpisodeAdded covers the maintenance half of the
// "Latest Episodes" sort (issue #202): linking a new episode file must bump
// the parent series' media_items.latest_episode_added_at denorm for both the
// single-file and bulk link paths, while re-linking away from an episode must
// fully recompute the old and new parent series.
func TestEpisodeLinkMaintainsLatestEpisodeAdded(t *testing.T) {
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
	otherSeriesID := fmt.Sprintf("lea-other-series-%d", suffix)
	ep1 := fmt.Sprintf("lea-ep1-%d", suffix)
	ep2 := fmt.Sprintf("lea-ep2-%d", suffix)
	ep3 := fmt.Sprintf("lea-ep3-%d", suffix)

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('series', 'LEA Test', true) RETURNING id
	`).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, otherSeriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'LEA Series', 'matched', '{}'::text[])
	`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'LEA Other Series', 'matched', '{}'::text[])
	`, otherSeriesID); err != nil {
		t.Fatalf("seed other series: %v", err)
	}
	for i, epID := range []string{ep1, ep2} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, 1, $3, 'Ep')
		`, epID, seriesID, i+1); err != nil {
			t.Fatalf("seed episode %s: %v", epID, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $2, 1, 1, 'Other Ep')
	`, ep3, otherSeriesID); err != nil {
		t.Fatalf("seed other episode: %v", err)
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
	latest := func(contentID string) *time.Time {
		var v *time.Time
		if err := pool.QueryRow(ctx, `
			SELECT latest_episode_added_at FROM media_items WHERE content_id = $1
		`, contentID).Scan(&v); err != nil {
			t.Fatalf("read latest_episode_added_at: %v", err)
		}
		return v
	}

	repo := NewFileRepository(pool)
	firstAdded := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	secondAdded := time.Now().Add(-1 * time.Hour).UTC().Truncate(time.Second)

	// Path 1: UpdateEpisodeLink on a single file.
	pathPrefix := fmt.Sprintf("/tmp/lea-%d", suffix)
	file1 := seedFile(fmt.Sprintf("%s/e1.mkv", pathPrefix), firstAdded, 1, 1)
	if err := repo.UpdateEpisodeLink(ctx, file1, ep1, 1, 1); err != nil {
		t.Fatalf("UpdateEpisodeLink: %v", err)
	}
	got := latest(seriesID)
	if got == nil || !got.Equal(firstAdded) {
		t.Fatalf("latest_episode_added_at after first link = %v, want %v", got, firstAdded)
	}

	// Path 2: BulkLinkEpisodesBySeries picks up the newer file and bumps.
	file2 := seedFile(fmt.Sprintf("%s/e2.mkv", pathPrefix), secondAdded, 1, 2)
	if _, err := repo.BulkLinkEpisodesBySeries(ctx, seriesID); err != nil {
		t.Fatalf("BulkLinkEpisodesBySeries: %v", err)
	}
	got = latest(seriesID)
	if got == nil || !got.Equal(secondAdded) {
		t.Fatalf("latest_episode_added_at after bulk link = %v, want %v", got, secondAdded)
	}

	// Re-linking the same episode is an ON CONFLICT no-op: no re-bump, the
	// value stays at the newest arrival.
	if err := repo.UpdateEpisodeLink(ctx, file1, ep1, 1, 1); err != nil {
		t.Fatalf("re-link UpdateEpisodeLink: %v", err)
	}
	got = latest(seriesID)
	if got == nil || !got.Equal(secondAdded) {
		t.Fatalf("latest_episode_added_at after no-op re-link = %v, want unchanged %v", got, secondAdded)
	}

	if err := repo.UpdateEpisodeLink(ctx, file2, ep3, 1, 1); err != nil {
		t.Fatalf("re-link newer file to other series: %v", err)
	}
	got = latest(seriesID)
	if got == nil || !got.Equal(firstAdded) {
		t.Fatalf("latest_episode_added_at after moving newest episode = %v, want %v", got, firstAdded)
	}
	got = latest(otherSeriesID)
	if got == nil || !got.Equal(secondAdded) {
		t.Fatalf("other latest_episode_added_at after re-link = %v, want %v", got, secondAdded)
	}

	if err := repo.UpdateEpisodeLink(ctx, file1, ep3, 1, 1); err != nil {
		t.Fatalf("re-link remaining file to other series: %v", err)
	}
	if got = latest(seriesID); got != nil {
		t.Fatalf("latest_episode_added_at after moving all episodes = %v, want nil", got)
	}

	cleared, err := repo.ClearContentLinksByPathPrefix(ctx, folderID, pathPrefix)
	if err != nil {
		t.Fatalf("ClearContentLinksByPathPrefix: %v", err)
	}
	if cleared != 2 {
		t.Fatalf("cleared links = %d, want 2", cleared)
	}
	if got = latest(otherSeriesID); got != nil {
		t.Fatalf("other latest_episode_added_at after clearing links = %v, want nil", got)
	}
}
