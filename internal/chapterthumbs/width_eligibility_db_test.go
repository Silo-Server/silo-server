package chapterthumbs

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type observedWidthRepository struct {
	*scanner.FileRepository
	scans chan struct{}
}

func (r *observedWidthRepository) ListChapterThumbnailsAtOtherWidths(ctx context.Context, limit int, suffix string, afterID int) ([]*models.MediaFile, bool, error) {
	files, pending, err := r.FileRepository.ListChapterThumbnailsAtOtherWidths(ctx, limit, suffix, afterID)
	select {
	case r.scans <- struct{}{}:
	case <-ctx.Done():
	}
	return files, pending, err
}

func TestCompletedWidthScanRechecksLibraryEligibilityDB(t *testing.T) {
	for _, disabledColumn := range []string{"enabled", "chapter_thumbnails_enabled"} {
		t.Run(disabledColumn, func(t *testing.T) {
			pool := chapterURLTestPool(t, nil)
			fileID, _ := chapterURLTestFile(t, pool)
			var folderID int
			if err := pool.QueryRow(t.Context(), `SELECT media_folder_id FROM media_files WHERE id=$1`, fileID).Scan(&folderID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(t.Context(), `UPDATE media_folders SET `+disabledColumn+`=false WHERE id=$1`, folderID); err != nil {
				t.Fatal(err)
			}
			repo := &observedWidthRepository{FileRepository: scanner.NewFileRepository(pool), scans: make(chan struct{}, 1)}
			service := widthQueueService(repo)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ticks := make(chan time.Time)
			done := make(chan struct{})
			go func() {
				defer close(done)
				service.followPreviewWidthTicks(ctx, ticks)
			}()
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
