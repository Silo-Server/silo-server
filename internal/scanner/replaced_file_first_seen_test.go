package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// An arr upgrade deletes the old release, then imports the new one under a
// new name. The two arrive as separate changes, in either order. The swapped
// title must keep its added date (Recently Added) and, for a movie, the item
// itself with everything attached to it.

const replacedFileGrace = 24 * time.Hour

func (fx presentStateFixture) membershipFirstSeen(ctx context.Context, t *testing.T, table, idColumn, id string) *time.Time {
	t.Helper()
	var firstSeen *time.Time
	if err := fx.pool.QueryRow(ctx,
		`SELECT MAX(first_seen_at) FROM `+table+` WHERE `+idColumn+` = $1 AND media_folder_id = $2`,
		id, fx.folderID).Scan(&firstSeen); err != nil {
		t.Fatalf("read %s first_seen_at: %v", table, err)
	}
	return firstSeen
}

func (fx presentStateFixture) count(ctx context.Context, t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func (fx presentStateFixture) exec(ctx context.Context, t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := fx.pool.Exec(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func assertTime(t *testing.T, label string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil || !got.Equal(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func TestReplacedEpisodeKeepsFirstSeen(t *testing.T) {
	for _, order := range []string{"delete then import", "import then delete"} {
		t.Run(order, func(t *testing.T) {
			ctx := t.Context()
			fx := seedPresentStateFixture(ctx, t, "replaced-episode")
			old := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
			fx.exec(ctx, t, `UPDATE media_files SET created_at = $1 WHERE file_path = $2`, old, fx.targetPath)
			// A second episode keeps the series in the library throughout, as
			// in any real show.
			fx.exec(ctx, t, `
				INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, still_path)
				VALUES ($1, $2, 1, 2, 'Episode 2', '')
			`, fx.episodeID+"-2", fx.seriesID)
			fx.exec(ctx, t, `
				INSERT INTO media_files (content_id, episode_id, media_folder_id, file_path, file_size, season_number, episode_number, created_at)
				VALUES ($1, $2, $3, $4, 1024, 1, 2, $5)
			`, fx.seriesID, fx.episodeID+"-2", fx.folderID, fx.targetPath+".e2.mkv", old.Add(-time.Hour))

			s := NewScanner(NewFileRepository(fx.pool), "", nil, 1, true, replacedFileGrace)
			if err := s.syncPresentLibraryState(ctx, fx.folderID); err != nil {
				t.Fatalf("seed memberships: %v", err)
			}
			assertTime(t, "seeded episode first_seen_at", fx.membershipFirstSeen(ctx, t, "episode_libraries", "episode_id", fx.episodeID), old)

			folder := &models.MediaFolder{ID: fx.folderID, Type: "series"}
			newPath := fx.targetPath + ".upgraded.mkv"
			importNew := func() {
				fx.exec(ctx, t, `
					INSERT INTO media_files (content_id, episode_id, media_folder_id, file_path, file_size, season_number, episode_number, created_at)
					VALUES ($1, $2, $3, $4, 2048, 1, 1, NOW())
				`, fx.seriesID, fx.episodeID, fx.folderID, newPath)
				if err := s.syncPresentFileState(ctx, fx.folderID, newPath); err != nil {
					t.Fatalf("import replacement: %v", err)
				}
			}
			deleteOld := func() {
				if _, err := s.reconcileVanishedFileIfNeeded(ctx, folder, fx.targetPath); err != nil {
					t.Fatalf("reconcile deleted release: %v", err)
				}
			}
			if order == "delete then import" {
				deleteOld()
				if got := fx.membershipFirstSeen(ctx, t, "episode_libraries", "episode_id", fx.episodeID); got != nil {
					t.Fatalf("episode stayed in the library with no present file: %v", got)
				}
				importNew()
			} else {
				importNew()
				deleteOld()
			}

			assertTime(t, "episode first_seen_at", fx.membershipFirstSeen(ctx, t, "episode_libraries", "episode_id", fx.episodeID), old)
			var latest *time.Time
			if err := fx.pool.QueryRow(ctx, `SELECT latest_episode_added_at FROM media_items WHERE content_id = $1`, fx.seriesID).Scan(&latest); err != nil {
				t.Fatalf("read latest_episode_added_at: %v", err)
			}
			assertTime(t, "series latest_episode_added_at", latest, old)
			if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_files WHERE media_folder_id = $1 AND held_episode_first_seen_at IS NOT NULL`, fx.folderID); n != 0 {
				t.Fatalf("held episode dates left after restore: %d", n)
			}
		})
	}
}

// seedMovie turns the fixture's unrelated file into a movie with an old
// membership and a collection entry, in a movies library.
func seedMovie(ctx context.Context, t *testing.T, fx presentStateFixture, old time.Time) {
	t.Helper()
	fx.exec(ctx, t, `UPDATE media_folders SET type = 'movies' WHERE id = $1`, fx.folderID)
	fx.exec(ctx, t, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`,
		fx.unrelatedID, fx.folderID, old)
	collectionID := fx.unrelatedID + "-collection"
	fx.exec(ctx, t, `
		INSERT INTO library_collections (id, library_id, slug, title, collection_type)
		VALUES ($1, $2, $1, 'Favourites', 'manual')
	`, collectionID, fx.folderID)
	fx.exec(ctx, t, `
		INSERT INTO library_collection_items (collection_id, media_item_id, position, source_rank, created_at, updated_at)
		VALUES ($1, $2, 0, 0, NOW(), NOW())
	`, collectionID, fx.unrelatedID)
}

func TestReplacedMovieKeepsItemAndFirstSeen(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "replaced-movie")
	old := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	seedMovie(ctx, t, fx, old)

	s := NewScanner(NewFileRepository(fx.pool), "", nil, 1, true, replacedFileGrace)
	folder := &models.MediaFolder{ID: fx.folderID, Type: "movies"}
	if _, err := s.reconcileVanishedFileIfNeeded(ctx, folder, fx.unrelatedPath); err != nil {
		t.Fatalf("reconcile deleted release: %v", err)
	}
	if fx.hasItemMembership(ctx, t, fx.unrelatedID) {
		t.Fatal("movie stayed in the library with no present file")
	}
	if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_items WHERE content_id = $1`, fx.unrelatedID); n != 1 {
		t.Fatal("movie item was deleted inside the removal grace")
	}
	if n := fx.count(ctx, t, `SELECT COUNT(*) FROM library_collection_items WHERE media_item_id = $1`, fx.unrelatedID); n != 1 {
		t.Fatal("movie lost its collection entry inside the removal grace")
	}

	newPath := fx.unrelatedPath + ".upgraded.mkv"
	fx.exec(ctx, t, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, created_at)
		VALUES ($1, $2, $3, 2048, NOW())
	`, fx.unrelatedID, fx.folderID, newPath)
	if err := s.syncPresentFileState(ctx, fx.folderID, newPath); err != nil {
		t.Fatalf("import replacement: %v", err)
	}
	assertTime(t, "movie first_seen_at", fx.membershipFirstSeen(ctx, t, "media_item_libraries", "content_id", fx.unrelatedID), old)
}

func TestReturningMovieKeepsFirstSeen(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "returning-movie")
	old := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	seedMovie(ctx, t, fx, old)

	s := NewScanner(NewFileRepository(fx.pool), "", nil, 1, true, replacedFileGrace)
	folder := &models.MediaFolder{ID: fx.folderID, Type: "movies"}
	if _, err := s.reconcileVanishedFileIfNeeded(ctx, folder, fx.unrelatedPath); err != nil {
		t.Fatalf("reconcile vanished file: %v", err)
	}
	// The same file comes back at the same path (a remounted drive).
	fx.exec(ctx, t, `UPDATE media_files SET missing_since = NULL WHERE file_path = $1`, fx.unrelatedPath)
	if err := s.syncPresentFileState(ctx, fx.folderID, fx.unrelatedPath); err != nil {
		t.Fatalf("restore returning file: %v", err)
	}
	assertTime(t, "movie first_seen_at", fx.membershipFirstSeen(ctx, t, "media_item_libraries", "content_id", fx.unrelatedID), old)
}

func TestHeldMovieIsDeletedOnceGraceEnds(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "held-movie-expiry")
	old := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	seedMovie(ctx, t, fx, old)

	s := NewScanner(NewFileRepository(fx.pool), "", nil, 1, true, replacedFileGrace)
	folder := &models.MediaFolder{ID: fx.folderID, Type: "movies"}
	if _, err := s.reconcileVanishedFileIfNeeded(ctx, folder, fx.unrelatedPath); err != nil {
		t.Fatalf("reconcile deleted release: %v", err)
	}
	// No replacement arrives; the grace runs out.
	fx.exec(ctx, t, `UPDATE media_files SET missing_since = $1 WHERE file_path = $2`,
		time.Now().Add(-replacedFileGrace-time.Hour), fx.unrelatedPath)
	if _, _, _, err := s.sweepMissingAndReconcile(ctx, folder, false); err != nil {
		t.Fatalf("sweep after grace: %v", err)
	}
	if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_items WHERE content_id = $1`, fx.unrelatedID); n != 0 {
		t.Fatal("held movie survived past the removal grace")
	}
	if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_files WHERE file_path = $1`, fx.unrelatedPath); n != 0 {
		t.Fatal("trash kept the expired movie's file row")
	}
}

func TestZeroGraceDeletesOrphanedMovieAtOnce(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "zero-grace-movie")
	seedMovie(ctx, t, fx, time.Now().Add(-72*time.Hour).UTC().Truncate(time.Second))

	s := NewScanner(NewFileRepository(fx.pool), "", nil, 1, true, 0)
	folder := &models.MediaFolder{ID: fx.folderID, Type: "movies"}
	if _, err := s.reconcileVanishedFileIfNeeded(ctx, folder, fx.unrelatedPath); err != nil {
		t.Fatalf("reconcile deleted release: %v", err)
	}
	if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_items WHERE content_id = $1`, fx.unrelatedID); n != 0 {
		t.Fatal("zero grace kept the orphaned movie")
	}
}

// The trash sweep and the hold each read the clock, so a row can fall past
// the trash cutoff while its item is still held. The sweep must leave the
// rows of an item without membership for its orphan check.
func TestTrashKeepsRowsOfItemWithoutMembership(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "trash-held-item")
	expired := time.Now().Add(-replacedFileGrace - time.Hour)
	fx.exec(ctx, t, `UPDATE media_files SET missing_since = $1 WHERE file_path = $2`, expired, fx.unrelatedPath)

	repo := NewFileRepository(fx.pool)
	if _, err := repo.DeleteMissingByFolder(ctx, fx.folderID, replacedFileGrace, nil); err != nil {
		t.Fatalf("empty trash: %v", err)
	}
	if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_files WHERE file_path = $1`, fx.unrelatedPath); n != 1 {
		t.Fatal("trash deleted the last row of an item without membership")
	}
	fx.exec(ctx, t, `DELETE FROM media_items WHERE content_id = $1`, fx.unrelatedID)
	if _, err := repo.DeleteMissingByFolder(ctx, fx.folderID, replacedFileGrace, nil); err != nil {
		t.Fatalf("empty trash after item delete: %v", err)
	}
	if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_files WHERE file_path = $1`, fx.unrelatedPath); n != 0 {
		t.Fatal("trash kept the row after its item was deleted")
	}
}

func TestHeldFirstSeenIsDroppedOnRelinkAndItemDelete(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "held-drop")
	old := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	seedMovie(ctx, t, fx, old)
	fx.exec(ctx, t, `UPDATE media_files SET missing_since = NOW() WHERE file_path = $1`, fx.unrelatedPath)

	hold := func() {
		fx.exec(ctx, t, `DELETE FROM media_item_libraries WHERE content_id = $1`, fx.unrelatedID)
		if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_files WHERE file_path = $1 AND held_item_first_seen_at = $2`, fx.unrelatedPath, old); n != 1 {
			t.Fatal("membership removal did not hold first_seen_at on the missing row")
		}
	}

	hold()
	// The row is relinked to another title: its held date no longer applies.
	fx.exec(ctx, t, `UPDATE media_files SET content_id = $1 WHERE file_path = $2`, fx.seriesID, fx.unrelatedPath)
	fx.exec(ctx, t, `UPDATE media_files SET content_id = $1 WHERE file_path = $2`, fx.unrelatedID, fx.unrelatedPath)
	fx.exec(ctx, t, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, fx.unrelatedID, fx.folderID)
	if got := fx.membershipFirstSeen(ctx, t, "media_item_libraries", "content_id", fx.unrelatedID); got == nil || got.Equal(old) {
		t.Fatalf("relinked row restored a stale first_seen_at: %v", got)
	}

	fx.exec(ctx, t, `UPDATE media_item_libraries SET first_seen_at = $1 WHERE content_id = $2`, old, fx.unrelatedID)
	hold()
	fx.exec(ctx, t, `DELETE FROM media_items WHERE content_id = $1`, fx.unrelatedID)
	if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_files WHERE file_path = $1 AND held_item_first_seen_at IS NOT NULL`, fx.unrelatedPath); n != 0 {
		t.Fatal("deleting the item kept its held first_seen_at")
	}
}

// Deleting a series cascades to its episodes, whose file rows get episode_id
// set to NULL only after the item-delete trigger runs. Held values on those
// rows must not make the delete fail its foreign key check.
func TestSeriesWithHeldFirstSeenCanBeDeleted(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "held-series-delete")
	s := NewScanner(NewFileRepository(fx.pool), "", nil, 1, true, replacedFileGrace)
	if err := s.syncPresentFileState(ctx, fx.folderID, fx.targetPath); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	fx.exec(ctx, t, `UPDATE media_files SET missing_since = NOW() WHERE file_path = $1`, fx.targetPath)
	fx.exec(ctx, t, `DELETE FROM episode_libraries WHERE episode_id = $1`, fx.episodeID)
	fx.exec(ctx, t, `DELETE FROM media_item_libraries WHERE content_id = $1`, fx.seriesID)
	if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_files WHERE file_path = $1 AND held_item_first_seen_at IS NOT NULL AND held_episode_first_seen_at IS NOT NULL`, fx.targetPath); n != 1 {
		t.Fatal("membership removal did not hold both first_seen_at values")
	}

	fx.exec(ctx, t, `DELETE FROM media_items WHERE content_id = $1`, fx.seriesID)
	if n := fx.count(ctx, t, `
		SELECT COUNT(*) FROM media_files
		WHERE file_path = $1
		  AND (episode_id IS NOT NULL OR held_item_first_seen_at IS NOT NULL OR held_episode_first_seen_at IS NOT NULL)
	`, fx.targetPath); n != 0 {
		t.Fatal("deleting the series left its file row linked or holding dates")
	}
}

// Book items are never held: their chapter and series listings read child
// tables that only an item delete clears.
func TestOrphanedBookItemIsNotHeld(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "book-not-held")
	seedMovie(ctx, t, fx, time.Now().Add(-72*time.Hour).UTC().Truncate(time.Second))
	fx.exec(ctx, t, `UPDATE media_items SET type = 'ebook' WHERE content_id = $1`, fx.unrelatedID)

	s := NewScanner(NewFileRepository(fx.pool), "", nil, 1, true, replacedFileGrace)
	fx.exec(ctx, t, `UPDATE media_files SET missing_since = NOW() WHERE file_path = $1`, fx.unrelatedPath)
	if _, _, _, err := s.sweepMissingAndReconcile(ctx, &models.MediaFolder{ID: fx.folderID, Type: "movies"}, false); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n := fx.count(ctx, t, `SELECT COUNT(*) FROM media_items WHERE content_id = $1`, fx.unrelatedID); n != 0 {
		t.Fatal("orphaned book item was held")
	}
}

// A replacement can be imported while the old release's removal is still
// committing. The insert's BEFORE lookup cannot see the uncommitted held
// date; the insert then waits on the row being deleted and is written once
// the removal commits. The held date must still win.
func TestConcurrentRemovalAndInsertKeepFirstSeen(t *testing.T) {
	ctx := t.Context()
	fx := seedPresentStateFixture(ctx, t, "concurrent-hold")
	old := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	seedMovie(ctx, t, fx, old)
	fx.exec(ctx, t, `UPDATE media_files SET missing_since = NOW() WHERE file_path = $1`, fx.unrelatedPath)

	removal, err := fx.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin removal: %v", err)
	}
	defer removal.Rollback(ctx) //nolint:errcheck
	if _, err := removal.Exec(ctx, `DELETE FROM media_item_libraries WHERE content_id = $1`, fx.unrelatedID); err != nil {
		t.Fatalf("remove membership: %v", err)
	}

	inserted := make(chan error, 1)
	go func() {
		_, err := fx.pool.Exec(ctx, `
			INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (content_id, media_folder_id) DO NOTHING
		`, fx.unrelatedID, fx.folderID)
		inserted <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := fx.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%INSERT INTO media_item_libraries%'
		`).Scan(&waiting); err != nil {
			t.Fatalf("read lock waits: %v", err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("insert never waited on the removal")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := removal.Commit(ctx); err != nil {
		t.Fatalf("commit removal: %v", err)
	}
	if err := <-inserted; err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	assertTime(t, "movie first_seen_at", fx.membershipFirstSeen(ctx, t, "media_item_libraries", "content_id", fx.unrelatedID), old)
}
