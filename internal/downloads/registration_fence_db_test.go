package downloads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// fenceDownload is a download of contentID on fileID, managed on the
// fixture's device A when managed is set.
func (f managedFixture) fenceDownload(id string, fileID int, contentID string, managed bool) *Download {
	now := time.Now()
	d := &Download{
		ID: id, UserID: f.userID, MediaFileID: fileID, ContentID: contentID, Kind: KindQueued, Status: StatusReady,
		Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal,
		FileSize: 1024, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if managed {
		d.ProfileID, d.DeviceID = f.profileA, f.deviceA
	}
	return d
}

func downloadFileOf(t *testing.T, pool *pgxpool.Pool, id string) (fileID int, found bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `SELECT media_file_id FROM downloads WHERE id = $1`, id).Scan(&fileID)
	if err != nil {
		return 0, false
	}
	return fileID, true
}

// TestRegistrationRefusesAFileOfAnotherTitleDB pins that each statement that
// writes a download's file, a new download, a batch, a managed batch, or a
// managed entry's replacement, writes it only while the file is present and
// linked to the title the download names, and otherwise answers the
// not-found a missing file gets.
func TestRegistrationRefusesAFileOfAnotherTitleDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	oldTitle, newTitle := f.contentID, f.contentID+"-other"
	// The file is relinked to another title after a download of the old
	// one resolved it.
	if _, err := f.pool.Exec(ctx, `UPDATE media_files SET content_id = $2 WHERE id = $1`, f.fileID, newTitle); err != nil {
		t.Fatal(err)
	}
	refused := func(t *testing.T, err error, ids ...string) {
		t.Helper()
		if !errors.Is(err, catalog.ErrItemNotFound) {
			t.Fatalf("err = %v, want the missing-file not-found", err)
		}
		for _, id := range ids {
			if _, found := downloadFileOf(t, f.pool, id); found {
				t.Fatalf("download %s was written", id)
			}
		}
	}

	t.Run("create", func(t *testing.T) {
		refused(t, f.repo.Create(ctx, f.fenceDownload("fence-create-old", f.fileID, oldTitle, false)), "fence-create-old")
		if err := f.repo.Create(ctx, f.fenceDownload("fence-create-new", f.fileID, newTitle, false)); err != nil {
			t.Fatalf("download of the file's own title: %v", err)
		}
	})
	t.Run("batch", func(t *testing.T) {
		err := f.repo.CreateBatch(ctx, []*Download{
			f.fenceDownload("fence-batch-new", f.fileID, newTitle, false),
			f.fenceDownload("fence-batch-old", f.fileID, oldTitle, false),
		})
		refused(t, err, "fence-batch-new", "fence-batch-old")
	})
	t.Run("managed batch", func(t *testing.T) {
		rows, err := f.repo.CreateManagedEntriesBatch(ctx, []*Download{
			f.fenceDownload("fence-managed-old", f.fileID, oldTitle, true),
			f.fenceDownload("fence-managed-new", f.fileID, newTitle, true),
		})
		if err != nil || len(rows) != 1 || rows[0].ID != "fence-managed-new" {
			t.Fatalf("managed batch inserted %+v (%v), want only the file's own title", rows, err)
		}
		if _, found := downloadFileOf(t, f.pool, "fence-managed-old"); found {
			t.Fatal("managed entry of the old title was written")
		}
	})
	t.Run("replace", func(t *testing.T) {
		// The old title's entry is on another file of that title; replacing
		// it with the relinked file must leave it there.
		var keptFile int
		if err := f.pool.QueryRow(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
			VALUES ($1, $2, $3, 1024) RETURNING id`, oldTitle, f.folderID, "/tmp/downloads-fence-"+oldTitle+".mkv").Scan(&keptFile); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, keptFile) })
		existing := f.fenceDownload("fence-replace", keptFile, oldTitle, true)
		if err := f.repo.Create(ctx, existing); err != nil {
			t.Fatal(err)
		}
		replacement := f.fenceDownload("", f.fileID, oldTitle, true)
		_, err := f.repo.ReplaceManagedEntry(ctx, existing, replacement)
		if !errors.Is(err, catalog.ErrItemNotFound) {
			t.Fatalf("replacement onto another title's file: err = %v, want not found", err)
		}
		row, err := f.repo.GetByID(ctx, existing.ID)
		if err != nil || row.MediaFileID != keptFile || row.Revision != 1 {
			t.Fatalf("refused replacement changed the entry: %+v %v", row, err)
		}
		// A missing file of the right title is refused the same way.
		if _, err := f.pool.Exec(ctx, `UPDATE media_files SET missing_since = NOW() WHERE id = $1`, keptFile); err != nil {
			t.Fatal(err)
		}
		refused(t, f.repo.Create(ctx, f.fenceDownload("fence-missing", keptFile, oldTitle, false)), "fence-missing")
	})
}

// retitleFixtureFile renames the fixture's file to a path naming another
// title, through the scanner's retitle, and returns whether it moved.
func retitleFixtureFile(ctx context.Context, f managedFixture) (bool, error) {
	files := scanner.NewFileRepository(f.pool)
	newPath := fmt.Sprintf("/tmp/downloads-fence-retitled-%d.mkv", f.fileID)
	candidates, err := files.MovedFileCandidates(ctx, f.folderID, newPath, 1024, "fence-hash")
	if err != nil {
		return false, err
	}
	for _, moved := range candidates {
		if moved.ID == f.fileID {
			return files.RetitleMovedFile(ctx, moved, models.MediaFile{MediaFolderID: f.folderID, FilePath: newPath})
		}
	}
	return false, errors.New("the fixture's file is not a moved-file candidate")
}

// waitBlockedBy waits until a session waits for a lock pid holds and returns
// that session's pid.
func waitBlockedBy(ctx context.Context, t *testing.T, pool *pgxpool.Pool, pid int) int {
	t.Helper()
	for {
		var blocked int
		err := pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)) LIMIT 1`, pid).Scan(&blocked)
		if err == nil {
			return blocked
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no session waited for session %d: %v", pid, ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// retitleFenceFixture seeds a managed fixture whose file has the OSHash
// retitleFixtureFile looks for, under a deadline for the whole test.
func retitleFenceFixture(t *testing.T) (context.Context, managedFixture) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := seedManagedFixture(t)
	if _, err := f.pool.Exec(ctx, `UPDATE media_files SET file_hash = 'fence-hash' WHERE id = $1`, f.fileID); err != nil {
		t.Fatal(err)
	}
	return ctx, f
}

// retitleStandIn returns the stand-in the retitle left for the fixture's
// file.
func retitleStandIn(ctx context.Context, t *testing.T, f managedFixture) int {
	t.Helper()
	var id int
	if err := f.pool.QueryRow(ctx, `SELECT id FROM media_files WHERE stand_in_for_file_id = $1 AND missing_since IS NOT NULL`, f.fileID).Scan(&id); err != nil {
		t.Fatalf("no stand-in for the retitled file: %v", err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM media_files WHERE id = $1`, id) })
	return id
}

// TestRegistrationAndRetitleSerializeOnTheFileRowDB drives a download
// registration and a retitle of its file from two database sessions, each
// holding the file row while the other waits. A registration that commits
// first is moved off the file with the old title; one that waits for the
// retitle writes nothing, so no download of the old title is left on the
// file once it names another one.
func TestRegistrationAndRetitleSerializeOnTheFileRowDB(t *testing.T) {
	t.Run("registration holds the row", func(t *testing.T) {
		ctx, f := retitleFenceFixture(t)
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		d := f.fenceDownload("fence-first", f.fileID, f.contentID, false)
		if tag, err := tx.Exec(ctx, insertDownloadSQL, downloadInsertArgs(d)...); err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("registration: %v %v", tag, err)
		}
		retitled := make(chan error, 1)
		go func() {
			ok, err := retitleFixtureFile(ctx, f)
			if err == nil && !ok {
				err = errors.New("retitle did not move the row")
			}
			retitled <- err
		}()
		waitBlockedBy(ctx, t, f.pool, pid)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-retitled; err != nil {
			t.Fatal(err)
		}
		if got, _ := downloadFileOf(t, f.pool, d.ID); got != retitleStandIn(ctx, t, f) {
			t.Fatalf("download registered before the retitle is on file %d, want the old title's stand-in", got)
		}
	})

	t.Run("retitle holds the row", func(t *testing.T) {
		ctx, f := retitleFenceFixture(t)
		// A download the retitle parks; holding it stops the retitle after
		// it took the file row and before it commits.
		parked := f.fenceDownload("fence-parked", f.fileID, f.contentID, false)
		if err := f.repo.Create(ctx, parked); err != nil {
			t.Fatal(err)
		}
		hold, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = hold.Rollback(context.Background()) }()
		var holdPID int
		if err := hold.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holdPID); err != nil {
			t.Fatal(err)
		}
		if _, err := hold.Exec(ctx, `SELECT 1 FROM downloads WHERE id = $1 FOR UPDATE`, parked.ID); err != nil {
			t.Fatal(err)
		}
		retitled := make(chan error, 1)
		go func() {
			ok, err := retitleFixtureFile(ctx, f)
			if err == nil && !ok {
				err = errors.New("retitle did not move the row")
			}
			retitled <- err
		}()
		retitlePID := waitBlockedBy(ctx, t, f.pool, holdPID)
		registered := make(chan error, 1)
		late := f.fenceDownload("fence-late", f.fileID, f.contentID, false)
		go func() { registered <- f.repo.Create(ctx, late) }()
		waitBlockedBy(ctx, t, f.pool, retitlePID)
		if err := hold.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-retitled; err != nil {
			t.Fatal(err)
		}
		if err := <-registered; !errors.Is(err, catalog.ErrItemNotFound) {
			t.Fatalf("registration after the retitle: err = %v, want not found", err)
		}
		if got, found := downloadFileOf(t, f.pool, late.ID); found {
			t.Fatalf("download of the old title was written on file %d after the retitle", got)
		}
		if got, _ := downloadFileOf(t, f.pool, parked.ID); got != retitleStandIn(ctx, t, f) {
			t.Fatalf("parked download is on file %d, want the stand-in", got)
		}
	})
}
