package chapterthumbs

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const previewWidthPoll = time.Minute

var errPreviewWidthChanged = errors.New("preview image width changed during backfill")

// followPreviewWidth queues existing images at other widths on startup and
// after a width change. Extraction stays with the configured workers.
func (s *Service) followPreviewWidth(ctx context.Context) {
	ticker := time.NewTicker(previewWidthPoll)
	defer ticker.Stop()
	var queuedWidth int
	for ctx.Err() == nil {
		width, err := s.previewImageWidth(ctx)
		if err == nil && width != queuedWidth {
			var pending bool
			pending, err = s.queueWidthBackfill(ctx, width)
			if err == nil && !pending {
				queuedWidth = width
			} else if errors.Is(err, errPreviewWidthChanged) {
				continue
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "chapter thumbnail width backfill failed", "component", "chapterthumbs", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// queueWidthBackfill advances by ID so a queued or cooling-down file cannot
// keep later files out of a page. It holds at most one page and only adds
// requests while the normal queue is below the batch limit.
func (s *Service) queueWidthBackfill(ctx context.Context, width int) (bool, error) {
	afterID := 0
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		current, err := s.previewImageWidth(ctx)
		if err != nil {
			return false, err
		}
		if current != width {
			return false, errPreviewWidthChanged
		}
		files, pending, err := s.fileRepo.ListChapterThumbnailsAtOtherWidths(ctx, defaultBatchLimit, chapterThumbnailSuffix(width), afterID)
		if err != nil {
			return false, err
		}
		for _, file := range files {
			if file == nil {
				continue
			}
			if err := s.queueWidthFile(ctx, file.ID, width); err != nil {
				return false, err
			}
			afterID = file.ID
		}
		if len(files) < defaultBatchLimit {
			return pending, nil
		}
	}
}

func (s *Service) queueWidthFile(ctx context.Context, fileID int, width int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := s.previewImageWidth(ctx)
		if err != nil {
			return err
		}
		if current != width {
			return errPreviewWidthChanged
		}
		s.mu.Lock()
		_, normal := s.queuedNormal[fileID]
		_, priority := s.queuedPriority[fileID]
		if normal || priority {
			s.mu.Unlock()
			return nil
		}
		if len(s.queuedNormal) < defaultBatchLimit {
			// A request already running may still be making the previous
			// width. Keep one follow-up, which cannot start until it ends.
			s.queuedNormal[fileID] = ChapterThumbnailRequest{FileID: fileID}
			s.normalQueue = append(s.normalQueue, fileID)
			s.mu.Unlock()
			s.notifyNormalWorker()
			return nil
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.widthQueueSpace:
		case <-time.After(previewWidthPoll):
		}
	}
}
