package downloads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// seedActiveDownloads creates n queued downloads for the fixture's user, the
// state a series batch leaves its episodes in. A device holds one entry per
// media file, so each download gets its own file.
func seedActiveDownloads(t *testing.T, f managedFixture, n int) {
	t.Helper()
	ctx := context.Background()
	var folderID int
	if err := f.pool.QueryRow(ctx, `SELECT media_folder_id FROM media_files WHERE id = $1`, f.fileID).Scan(&folderID); err != nil {
		t.Fatalf("resolve folder: %v", err)
	}
	now := time.Now()
	for i := range n {
		contentID := fmt.Sprintf("dl-limit-content-%d-%d", now.UnixNano(), i)
		var fileID int
		if err := f.pool.QueryRow(ctx,
			`INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
			 VALUES ($1, $2, $3, 1024) RETURNING id`,
			contentID, folderID, fmt.Sprintf("/tmp/downloads-limit-test-%d-%d.mp4", now.UnixNano(), i),
		).Scan(&fileID); err != nil {
			t.Fatalf("seed media file: %v", err)
		}
		t.Cleanup(func() {
			_, _ = f.pool.Exec(ctx, `DELETE FROM downloads WHERE media_file_id = $1`, fileID)
			_, _ = f.pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID)
		})
		if err := f.repo.Create(ctx, &Download{
			ID: fmt.Sprintf("dl-active-%d-%d", now.UnixNano(), i), UserID: f.userID,
			ProfileID: f.profileA, DeviceID: f.deviceA, MediaFileID: fileID, ContentID: contentID,
			Kind: KindQueued, Status: StatusQueued, Format: FormatOriginal, FileSize: 1024,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed active download: %v", err)
		}
	}
}

// A series batch needs one free concurrent slot, not one per episode, so a
// series longer than the limit can still be downloaded (#1703).
func TestQuantityLimiterConcurrentLimitNeedsOneSlotPerBatch(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name      string
		active    int
		batchSize int
		wantErr   error
	}{
		{name: "series longer than the limit, nothing active", active: 0, batchSize: 10},
		{name: "series with one slot free", active: 2, batchSize: 10},
		{name: "series with no slot free", active: 3, batchSize: 10, wantErr: ErrConcurrentLimitReached},
		{name: "single download with a slot free", active: 2, batchSize: 1},
		{name: "single download at the limit", active: 3, batchSize: 1, wantErr: ErrConcurrentLimitReached},
		// A series batch's queued episodes keep counting as active, so they
		// still hold back further downloads until they finish.
		{name: "single download behind a queued series", active: 10, batchSize: 1, wantErr: ErrConcurrentLimitReached},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := seedManagedFixture(t)
			seedActiveDownloads(t, f, tc.active)
			limiter := NewQuantityLimiter(f.repo, 3, 0, 0)
			if err := limiter.Check(ctx, f.userID, tc.batchSize); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Check(active=%d, batch=%d) = %v, want %v", tc.active, tc.batchSize, err, tc.wantErr)
			}
		})
	}
}

// The period quota still counts every record a batch creates.
func TestQuantityLimiterPeriodQuotaCountsWholeBatch(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	limiter := NewQuantityLimiter(f.repo, 0, 5, 24*time.Hour)

	if err := limiter.Check(ctx, f.userID, 5); err != nil {
		t.Fatalf("batch of 5 within a quota of 5: %v", err)
	}
	if err := limiter.Check(ctx, f.userID, 6); !errors.Is(err, ErrPeriodLimitReached) {
		t.Fatalf("batch of 6 over a quota of 5 = %v, want ErrPeriodLimitReached", err)
	}
}
