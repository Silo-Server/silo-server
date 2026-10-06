package chapterthumbs

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Catalog-wide completion must not depend on another package's concurrent
// chapter fixtures. Keep the real repository queries in an isolated catalog.
func chapterWidthTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	admin := chapterURLTestPool(t, nil)
	schema := pgx.Identifier{fmt.Sprintf("chapter_width_%d", time.Now().UnixNano())}.Sanitize()
	if _, err := admin.Exec(t.Context(), `CREATE SCHEMA `+schema+`;
		CREATE TABLE `+schema+`.media_folders (LIKE public.media_folders INCLUDING ALL);
		CREATE TABLE `+schema+`.media_files (LIKE public.media_files INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) })
	cfg := admin.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ", public"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type observedWidthRepository struct {
	*scanner.FileRepository
	scans     chan struct{}
	checks    chan struct{}
	scanCount atomic.Int32
}

func (r *observedWidthRepository) ListChapterThumbnailsAtOtherWidths(ctx context.Context, limit int, suffix string, afterID int, skipHDR bool) ([]*models.MediaFile, time.Time, error) {
	files, pending, err := r.FileRepository.ListChapterThumbnailsAtOtherWidths(ctx, limit, suffix, afterID, skipHDR)
	r.scanCount.Add(1)
	select {
	case r.scans <- struct{}{}:
	case <-ctx.Done():
	}
	return files, pending, err
}

func (r *observedWidthRepository) ChapterThumbnailLibraryKey(ctx context.Context) (string, error) {
	key, err := r.FileRepository.ChapterThumbnailLibraryKey(ctx)
	select {
	case r.checks <- struct{}{}:
	case <-ctx.Done():
	}
	return key, err
}

func TestCompletedWidthScanRechecksLibraryEligibilityDB(t *testing.T) {
	for _, disabledColumn := range []string{"enabled", "chapter_thumbnails_enabled", "completed_current_width"} {
		t.Run(disabledColumn, func(t *testing.T) {
			pool := chapterWidthTestPool(t)
			fileID, _ := chapterURLTestFile(t, pool)
			var folderID int
			if err := pool.QueryRow(t.Context(), `SELECT media_folder_id FROM media_files WHERE id=$1`, fileID).Scan(&folderID); err != nil {
				t.Fatal(err)
			}
			if disabledColumn == "completed_current_width" {
				setChapterURLPath(t, pool, fileID, chapterThumbnailKey(fileID, 0, 320))
			} else {
				if _, err := pool.Exec(t.Context(), `UPDATE media_folders SET `+disabledColumn+`=false WHERE id=$1`, folderID); err != nil {
					t.Fatal(err)
				}
			}
			repo := &observedWidthRepository{FileRepository: scanner.NewFileRepository(pool), scans: make(chan struct{}, 16), checks: make(chan struct{}, 16)}
			service := widthQueueService(repo)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ticks := make(chan time.Time)
			done := make(chan struct{})
			go func() {
				defer close(done)
				service.followPreviewWidthTicks(ctx, ticks)
			}()
			t.Cleanup(func() { cancel(); <-done })
			select {
			case <-repo.scans:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			service.mu.Lock()
			_, queuedBefore := service.queuedNormal[fileID]
			service.mu.Unlock()
			if queuedBefore {
				t.Fatal("disabled library was queued")
			}
			// Each unbuffered tick waits until the preceding poll has finished.
			// Stable library eligibility must only read media_folders again.
			for range 3 {
				select {
				case <-repo.checks:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case ticks <- time.Now():
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if scans := repo.scanCount.Load(); scans != 1 {
				t.Fatalf("unchanged libraries caused %d catalog scans, want only startup", scans)
			}
			if disabledColumn == "completed_current_width" {
				cancel()
				<-done
				return
			}
			// Re-enable without changing the preview width. An ordinary poll must
			// discover the old-width image even though startup had no pending work.
			if _, err := pool.Exec(ctx, `UPDATE media_folders SET `+disabledColumn+`=true WHERE id=$1`, folderID); err != nil {
				t.Fatal(err)
			}
			select {
			case ticks <- time.Now():
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-service.notifyNormal:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			service.mu.Lock()
			_, queuedAfter := service.queuedNormal[fileID]
			service.mu.Unlock()
			if !queuedAfter {
				t.Fatal("eligible library was not queued at the unchanged width")
			}
			cancel()
			select {
			case <-done:
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
		})
	}
}

func TestWidthBackfillDefersCatalogScansUntilRetryDB(t *testing.T) {
	for _, cooldown := range []string{"chapter", "file", "both"} {
		t.Run(cooldown, func(t *testing.T) {
			pool := chapterWidthTestPool(t)
			fileID, _ := chapterURLTestFile(t, pool)
			chapterDeadline := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			deadline := chapterDeadline
			if cooldown == "both" {
				deadline = deadline.Add(time.Hour)
			}
			if cooldown != "file" {
				if _, err := pool.Exec(t.Context(), `UPDATE media_files SET chapters=jsonb_set(chapters,
					'{0,thumbnail_retry_after}',to_jsonb($2::text)) WHERE id=$1`, fileID, chapterDeadline.Format(time.RFC3339)); err != nil {
					t.Fatal(err)
				}
			}
			if cooldown != "chapter" {
				if _, err := pool.Exec(t.Context(), `UPDATE media_files SET chapter_thumbnail_retry_after=$2 WHERE id=$1`, fileID, deadline); err != nil {
					t.Fatal(err)
				}
			}
			repo := &observedWidthRepository{FileRepository: scanner.NewFileRepository(pool), scans: make(chan struct{}, 16), checks: make(chan struct{}, 16)}
			service := widthQueueService(repo)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			ticks := make(chan time.Time)
			done := make(chan struct{})
			go func() { defer close(done); service.followPreviewWidthTicks(ctx, ticks) }()
			t.Cleanup(func() { cancel(); <-done })
			select {
			case <-repo.scans:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			for range 3 {
				select {
				case <-repo.checks:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case ticks <- deadline.Add(-time.Minute):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if scans := repo.scanCount.Load(); scans != 1 {
				t.Fatalf("cooling-down image caused %d catalog scans before retry, want startup only", scans)
			}
			if _, err := pool.Exec(ctx, `UPDATE media_files SET chapter_thumbnail_retry_after=NULL,
				chapters=chapters #- '{0,thumbnail_retry_after}' WHERE id=$1`, fileID); err != nil {
				t.Fatal(err)
			}
			select {
			case ticks <- deadline:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-service.notifyNormal:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			service.mu.Lock()
			_, queued := service.queuedNormal[fileID]
			service.mu.Unlock()
			if !queued {
				t.Fatal("retry deadline did not resume the width backfill")
			}
		})
	}
}
