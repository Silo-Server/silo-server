package downloads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// stubQuotaPreparer satisfies EncodePreparer without running ffmpeg; the quota
// test never drains the queue, so it is never invoked.
type stubQuotaPreparer struct{}

func (stubQuotaPreparer) PrepareFile(_ context.Context, _ playback.TranscodeOpts, _ string) error {
	return nil
}

// TestArtifactQuotaCheckedBeforeEnqueue pins two quota invariants for
// artifact-backed downloads: 'preparing' rows consume the concurrent quota,
// and a quota-rejected request must not leave an encode job behind.
func TestArtifactQuotaCheckedBeforeEnqueue(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)

	var present *string
	if err := f.pool.QueryRow(ctx, `SELECT to_regclass('public.download_artifacts')::text`).Scan(&present); err != nil {
		t.Fatalf("check download_artifacts: %v", err)
	}
	if present == nil {
		t.Skip("download_artifacts migration has not been applied")
	}

	// Second media file so the rejected request would need a distinct artifact.
	suffix := time.Now().UnixNano()
	var folderID int
	if err := f.pool.QueryRow(ctx, `SELECT media_folder_id FROM media_files WHERE id = $1`, f.fileID).Scan(&folderID); err != nil {
		t.Fatalf("resolve folder: %v", err)
	}
	contentID2 := fmt.Sprintf("dl-quota-content-%d", suffix)
	var fileID2 int
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
		 VALUES ($1, $2, $3, 2048) RETURNING id`,
		contentID2, folderID, fmt.Sprintf("/tmp/downloads-quota-test-%d.mp4", suffix),
	).Scan(&fileID2); err != nil {
		t.Fatalf("seed second media file: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id IN ($1, $2)`, f.fileID, fileID2)
		_, _ = f.pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID2)
	})

	limiter := NewQuantityLimiter(f.repo, 1, 0, 0)
	svc := NewService(f.repo, nil, limiter, nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	svc.SetArtifactManager(NewArtifactManager(
		NewArtifactRepository(f.pool), f.repo, nil, stubQuotaPreparer{}, "quota-test",
		func() *config.Config { return nil }, nil,
	))

	decision := QualityDecision{
		RequestedQuality:  Quality5Mbps,
		EffectiveQuality:  Quality5Mbps,
		DeliveryFormat:    FormatTranscode,
		TargetBitrateKbps: 5000,
		RequiresArtifact:  true,
		PrepareTarget:     playback.PrepareTarget{Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", TargetBitrateKbps: 5000},
	}

	first, err := svc.createArtifactDownload(ctx, f.userID, CreateRequest{Quality: Quality5Mbps},
		&models.MediaFile{ID: f.fileID, ContentID: f.contentID, FileSize: 1024}, decision)
	if err != nil {
		t.Fatalf("first artifact download: %v", err)
	}
	if first.Status != StatusPreparing {
		t.Fatalf("first download status = %q, want %q", first.Status, StatusPreparing)
	}

	// Over the concurrent cap of 1: the 'preparing' row above must count.
	_, err = svc.createArtifactDownload(ctx, f.userID, CreateRequest{Quality: Quality5Mbps},
		&models.MediaFile{ID: fileID2, ContentID: contentID2, FileSize: 2048}, decision)
	if !errors.Is(err, ErrConcurrentLimitReached) {
		t.Fatalf("second artifact download error = %v, want ErrConcurrentLimitReached", err)
	}

	// The rejected request must not have enqueued encode work.
	var enqueued int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM download_artifacts WHERE media_file_id = $1`, fileID2,
	).Scan(&enqueued); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if enqueued != 0 {
		t.Fatalf("rejected request enqueued %d artifact job(s), want 0", enqueued)
	}
}
