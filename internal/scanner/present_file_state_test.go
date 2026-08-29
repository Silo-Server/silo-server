package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// presentStateFixture seeds one folder holding a series episode file and an
// unrelated movie file, both present, with no memberships yet.
type presentStateFixture struct {
	pool          *pgxpool.Pool
	folderID      int
	seriesID      string
	episodeID     string
	unrelatedID   string
	targetPath    string
	unrelatedPath string
	firstSeen     time.Time
}

func seedPresentStateFixture(ctx context.Context, t *testing.T, label string) presentStateFixture {
	t.Helper()

	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	fx := presentStateFixture{
		pool:          pool,
		seriesID:      fmt.Sprintf("present-%s-series-%d", label, suffix),
		episodeID:     fmt.Sprintf("present-%s-episode-%d", label, suffix),
		unrelatedID:   fmt.Sprintf("present-%s-unrelated-%d", label, suffix),
		targetPath:    fmt.Sprintf("/tmp/present-%s-%d/episode.mkv", label, suffix),
		unrelatedPath: fmt.Sprintf("/tmp/present-%s-%d/unrelated.mkv", label, suffix),
		firstSeen:     time.Now().Add(-time.Hour).UTC().Truncate(time.Second),
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('series', 'Present State Test', true)
		RETURNING id
	`).Scan(&fx.folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, fx.folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{fx.seriesID, fx.unrelatedID})
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES
			($1, 'series', 'Target Series', 'matched', '{}'::text[]),
			($2, 'movie', 'Unrelated Movie', 'matched', '{}'::text[])
	`, fx.seriesID, fx.unrelatedID); err != nil {
		t.Fatalf("seed media items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $2, 1, 1, 'Episode')
	`, fx.episodeID, fx.seriesID); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_files (
			content_id, episode_id, media_folder_id, file_path, file_size,
			season_number, episode_number, created_at
		)
		VALUES
			($1, $2, $3, $4, 1024, 1, 1, $5),
			($6, NULL, $3, $7, 1024, NULL, NULL, NOW())
	`, fx.seriesID, fx.episodeID, fx.folderID, fx.targetPath, fx.firstSeen, fx.unrelatedID, fx.unrelatedPath); err != nil {
		t.Fatalf("seed media files: %v", err)
	}

	return fx
}

func (fx presentStateFixture) hasItemMembership(ctx context.Context, t *testing.T, contentID string) bool {
	t.Helper()
	var exists bool
	if err := fx.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM media_item_libraries
			WHERE content_id = $1 AND media_folder_id = $2
		)
	`, contentID, fx.folderID).Scan(&exists); err != nil {
		t.Fatalf("read membership for %s: %v", contentID, err)
	}
	return exists
}

// assertEpisodeRepaired checks the parts both scopes must agree on: the
// episode membership exists with first_seen_at aggregated over all of the
// episode's active files, and the series denorm was bumped to match.
func (fx presentStateFixture) assertEpisodeRepaired(ctx context.Context, t *testing.T) {
	t.Helper()

	var episodeFirstSeen time.Time
	if err := fx.pool.QueryRow(ctx, `
		SELECT first_seen_at
		FROM episode_libraries
		WHERE episode_id = $1 AND media_folder_id = $2
	`, fx.episodeID, fx.folderID).Scan(&episodeFirstSeen); err != nil {
		t.Fatalf("read episode membership: %v", err)
	}
	if !episodeFirstSeen.Equal(fx.firstSeen) {
		t.Fatalf("episode first_seen_at = %v, want %v", episodeFirstSeen, fx.firstSeen)
	}

	var latestEpisodeAdded *time.Time
	if err := fx.pool.QueryRow(ctx, `
		SELECT latest_episode_added_at FROM media_items WHERE content_id = $1
	`, fx.seriesID).Scan(&latestEpisodeAdded); err != nil {
		t.Fatalf("read latest episode timestamp: %v", err)
	}
	if latestEpisodeAdded == nil || !latestEpisodeAdded.Equal(fx.firstSeen) {
		t.Fatalf("latest_episode_added_at = %v, want %v", latestEpisodeAdded, fx.firstSeen)
	}

	if !fx.hasItemMembership(ctx, t, fx.seriesID) {
		t.Fatal("target media item membership was not restored")
	}
}

func TestSyncPresentFileStateScopesMembershipRepairToFile(t *testing.T) {
	ctx := context.Background()
	fx := seedPresentStateFixture(ctx, t, "file")

	scanner := &Scanner{fileRepo: NewFileRepository(fx.pool)}
	if err := scanner.syncPresentFileState(ctx, fx.folderID, fx.targetPath); err != nil {
		t.Fatalf("syncPresentFileState: %v", err)
	}

	fx.assertEpisodeRepaired(ctx, t)
	if fx.hasItemMembership(ctx, t, fx.unrelatedID) {
		t.Fatal("unrelated media item membership was repaired by a single-file sync")
	}
}

// TestSyncPresentLibraryStateRepairsWholeFolder drives the same unified body
// through the folder-wide scope: the file-scoped assertions must still hold,
// and the unrelated file in the folder must be repaired too.
func TestSyncPresentLibraryStateRepairsWholeFolder(t *testing.T) {
	ctx := context.Background()
	fx := seedPresentStateFixture(ctx, t, "folder")

	scanner := &Scanner{fileRepo: NewFileRepository(fx.pool)}
	if err := scanner.syncPresentLibraryState(ctx, fx.folderID); err != nil {
		t.Fatalf("syncPresentLibraryState: %v", err)
	}

	fx.assertEpisodeRepaired(ctx, t)
	if !fx.hasItemMembership(ctx, t, fx.unrelatedID) {
		t.Fatal("unrelated media item membership was not repaired by a folder-wide sync")
	}
}

// TestSyncPresentStateClearsDanglingLinks covers the dangling-link statement
// through both scopes: a file pointing at a deleted item must have its
// content_id nulled. Only content_id can dangle — media_files.episode_id has
// an ON DELETE SET NULL foreign key, so the episode variant of the repair is
// defensive and cannot be seeded here.
func TestSyncPresentStateClearsDanglingLinks(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		label string
		scope func(s *Scanner, fx presentStateFixture) error
	}{
		{
			name:  "file scope",
			label: "dangling-file",
			scope: func(s *Scanner, fx presentStateFixture) error {
				return s.syncPresentFileState(ctx, fx.folderID, fx.targetPath)
			},
		},
		{
			name:  "folder scope",
			label: "dangling-folder",
			scope: func(s *Scanner, fx presentStateFixture) error {
				return s.syncPresentLibraryState(ctx, fx.folderID)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := seedPresentStateFixture(ctx, t, tc.label)

			// Point the target file at content that does not exist, leaving
			// the row's content link dangling.
			if _, err := fx.pool.Exec(ctx, `
				UPDATE media_files
				SET content_id = $1, episode_id = NULL
				WHERE media_folder_id = $2 AND file_path = $3
			`, fx.seriesID+"-missing", fx.folderID, fx.targetPath); err != nil {
				t.Fatalf("dangle target link: %v", err)
			}

			scanner := &Scanner{fileRepo: NewFileRepository(fx.pool)}
			if err := tc.scope(scanner, fx); err != nil {
				t.Fatalf("sync present state: %v", err)
			}

			var contentID, episodeID *string
			if err := fx.pool.QueryRow(ctx, `
				SELECT content_id, episode_id
				FROM media_files
				WHERE media_folder_id = $1 AND file_path = $2
			`, fx.folderID, fx.targetPath).Scan(&contentID, &episodeID); err != nil {
				t.Fatalf("read repaired file: %v", err)
			}
			if contentID != nil {
				t.Fatalf("dangling content_id was not cleared: %v", *contentID)
			}
			if episodeID != nil {
				t.Fatalf("episode_id unexpectedly set: %v", *episodeID)
			}
		})
	}
}
