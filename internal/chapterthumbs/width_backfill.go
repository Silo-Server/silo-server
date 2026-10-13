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
// after a width or library eligibility change. Extraction stays with the
// configured workers, and each scan keeps at most one page in memory.
func (s *Service) followPreviewWidth(ctx context.Context) {
	s.followPreviewWidthTicks(ctx, time.Tick(previewWidthPoll))
}

func (s *Service) followPreviewWidthTicks(ctx context.Context, ticks <-chan time.Time) {
	scannedWidth := 0
	scannedLibraries := ""
	scannedPolicy := ""
	var nextRetry time.Time
	now := time.Now()
	for ctx.Err() == nil {
		width, err := s.previewImageWidth(ctx)
		if err == nil {
			var libraries string
			libraries, err = s.fileRepo.ChapterThumbnailLibraryKey(ctx)
			// Turning the HDR policy back on makes HDR files eligible again.
			policy := s.chapterThumbnailHDRPolicy(ctx)
			if err == nil && (width != scannedWidth || libraries != scannedLibraries || policy != scannedPolicy || (!nextRetry.IsZero() && !now.Before(nextRetry))) {
				var retry time.Time
				retry, err = s.queueWidthBackfill(ctx, width, policy == chapterThumbnailHDRPolicyDisabled)
				if errors.Is(err, errPreviewWidthChanged) {
					continue
				}
				if err == nil {
					// The key was read before scanning. A library enabled during
					// the scan changes it and triggers another pass next time.
					scannedWidth, scannedLibraries, scannedPolicy = width, libraries, policy
					nextRetry = retry
					// Work that is eligible now may still be queued or running.
					// Check its completion on the normal minute cadence.
					if !nextRetry.IsZero() && nextRetry.Before(now.Add(previewWidthPoll)) {
						nextRetry = now.Add(previewWidthPoll)
					}
				}
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "chapter thumbnail width backfill failed", "component", "chapterthumbs", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case now = <-ticks:
		}
	}
}

// queueWidthBackfill advances by ID so a queued or cooling-down file cannot
// keep later files out of a page. It holds at most one page and only adds
// requests while the normal queue is below the batch limit. skipHDR leaves
// out files extraction would skip under a disabled HDR policy, which would
// otherwise be requeued on every pass.
func (s *Service) queueWidthBackfill(ctx context.Context, width int, skipHDR bool) (time.Time, error) {
	afterID := 0
	for {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		current, err := s.previewImageWidth(ctx)
		if err != nil {
			return time.Time{}, err
		}
		if current != width {
			return time.Time{}, errPreviewWidthChanged
		}
		files, nextRetry, err := s.fileRepo.ListChapterThumbnailsAtOtherWidths(ctx, defaultBatchLimit, chapterThumbnailSuffix(width), afterID, skipHDR)
		if err != nil {
			return time.Time{}, err
		}
		for _, file := range files {
			if file == nil {
				continue
			}
			if err := s.queueWidthFile(ctx, file.ID, width); err != nil {
				return time.Time{}, err
			}
			afterID = file.ID
		}
		if len(files) < defaultBatchLimit {
			return nextRetry, nil
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
