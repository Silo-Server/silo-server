package downloads

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// stubQuotaPreparer satisfies EncodePreparer without running ffmpeg; the quota
// test never drains the queue, so it is never invoked.
type stubQuotaPreparer struct{}

func (stubQuotaPreparer) PrepareFile(_ context.Context, _ string, _ playback.TranscodeOpts, outputPath string) (PreparedArtifact, error) {
	return PreparedArtifact{OutputPath: outputPath}, nil
}

// TestConcurrentArtifactCreatesCannotBypassQuotaDB is the C5 regression test:
// parallel creates against a concurrent cap of 1 must produce exactly one
// download row and one artifact job. Before the per-user quota lock, the
// check-then-insert pair raced — every worker observed free quota before any
// row existed, bypassing the cap and stacking encode jobs.
func TestConcurrentArtifactCreatesCannotBypassQuotaDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)

	var present *string
	if err := f.pool.QueryRow(ctx, `SELECT to_regclass('public.download_artifacts')::text`).Scan(&present); err != nil {
		t.Fatalf("check download_artifacts: %v", err)
	}
	if present == nil {
		t.Skip("download_artifacts migration has not been applied")
	}

	const workers = 8
	suffix := time.Now().UnixNano()
	var folderID int
	if err := f.pool.QueryRow(ctx, `SELECT media_folder_id FROM media_files WHERE id = $1`, f.fileID).Scan(&folderID); err != nil {
		t.Fatalf("resolve folder: %v", err)
	}
	fileIDs := make([]int, workers)
	contentIDs := make([]string, workers)
	for i := range fileIDs {
		contentIDs[i] = fmt.Sprintf("dl-race-content-%d-%d", suffix, i)
		if err := f.pool.QueryRow(ctx,
			`INSERT INTO media_files (content_id, media_folder_id, file_path, file_size)
			 VALUES ($1, $2, $3, 4096) RETURNING id`,
			contentIDs[i], folderID, fmt.Sprintf("/tmp/downloads-race-test-%d-%d.mp4", suffix, i),
		).Scan(&fileIDs[i]); err != nil {
			t.Fatalf("seed media file %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = ANY($1)`, fileIDs)
		_, _ = f.pool.Exec(ctx, `DELETE FROM media_files WHERE id = ANY($1)`, fileIDs)
	})

	limiter := NewQuantityLimiter(f.repo, 1, 0, 0)
	svc := NewService(f.repo, nil, limiter, nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	svc.SetArtifactManager(NewArtifactManager(
		NewArtifactRepository(f.pool), f.repo, nil, stubQuotaPreparer{}, "quota-race-test",
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

	start := make(chan struct{})
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			download, err := svc.createArtifactDownload(ctx, f.userID, CreateRequest{Quality: Quality5Mbps},
				&models.MediaFile{ID: fileIDs[w], ContentID: contentIDs[w], FileSize: 4096}, decision)
			if err == nil && download.Status != StatusPreparing {
				err = fmt.Errorf("download status = %q, want preparing", download.Status)
			}
			errs[w] = err
		}(w)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for w, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConcurrentLimitReached):
		default:
			t.Fatalf("worker %d unexpected error: %v", w, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d concurrent creates succeeded, want exactly 1 (cap is 1)", succeeded)
	}

	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM downloads WHERE user_id = $1 AND media_file_id = ANY($2)`,
		f.userID, fileIDs,
	).Scan(&rows); err != nil {
		t.Fatalf("count downloads: %v", err)
	}
	if rows != 1 {
		t.Fatalf("quota bypass: %d download rows created, want 1", rows)
	}
	var jobs int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM download_artifacts WHERE media_file_id = ANY($1)`, fileIDs,
	).Scan(&jobs); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if jobs != 1 {
		t.Fatalf("rejected requests enqueued artifact jobs: %d, want 1", jobs)
	}
}

// TestPreparedSeasonReplacementCountsTowardConcurrentLimitDB pins that a season
// page replacing an existing entry with a prepared file goes through the
// concurrent limit, like a new prepared entry: the replacement becomes an
// active download.
func TestPreparedSeasonReplacementCountsTowardConcurrentLimitDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = $1`, f.fileID)
	})
	create := func(id, episodeID, status string) {
		now := time.Now()
		if err := f.repo.Create(ctx, &Download{
			ID: id, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, MediaFileID: f.fileID,
			ContentID: f.contentID, EpisodeID: episodeID, Kind: KindQueued, Status: status,
			Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal,
			FileSize: 1024, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	suffix := time.Now().UnixNano()
	existingID := fmt.Sprintf("dl-ep-%d", suffix)
	create(existingID, "ep-1", StatusReady)
	// Another download holds the account's only slot.
	busyID := fmt.Sprintf("dl-busy-%d", suffix)
	create(busyID, "ep-2", StatusDownloading)

	svc := NewService(f.repo, nil, NewQuantityLimiter(f.repo, 1, 0, 0), nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	svc.SetArtifactManager(NewArtifactManager(
		NewArtifactRepository(f.pool), f.repo, nil, stubQuotaPreparer{}, "replacement-quota-test",
		func() *config.Config { return nil }, nil,
	))
	decision := QualityDecision{
		RequestedQuality: Quality5Mbps, EffectiveQuality: Quality5Mbps, DeliveryFormat: FormatTranscode,
		TargetBitrateKbps: 5000, RequiresArtifact: true,
		PrepareTarget: playback.PrepareTarget{Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", TargetBitrateKbps: 5000},
	}
	req := CreateRequest{ContentID: f.contentID, ProfileID: f.profileA, DeviceID: f.deviceA,
		ExpectedEntries: map[string]ManagedCreateExpectation{"ep-1": {ID: existingID, Revision: 1}}}
	item := managedItem{file: &models.MediaFile{ID: f.fileID, ContentID: f.contentID, FileSize: 1024}, contentID: f.contentID, episodeID: "ep-1"}

	if _, err := svc.ensureManagedDecisions(ctx, f.userID, req, []managedItem{item}, []QualityDecision{decision}, "season"); !errors.Is(err, ErrConcurrentLimitReached) {
		t.Fatalf("replacement with no free slot: err = %v, want ErrConcurrentLimitReached", err)
	}
	row, err := f.repo.GetManagedEntry(ctx, f.userID, f.profileA, f.deviceA, f.contentID, "ep-1")
	if err != nil || row.Format != FormatOriginal || row.Revision != 1 {
		t.Fatalf("refused replacement changed the entry: %+v %v", row, err)
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM downloads WHERE id = $1`, busyID); err != nil {
		t.Fatal(err)
	}
	// A stale guard is refused before any encode is queued for it.
	stale := req
	stale.ExpectedEntries = map[string]ManagedCreateExpectation{"ep-1": {ID: existingID, Revision: 7}}
	if _, err := svc.ensureManagedDecisions(ctx, f.userID, stale, []managedItem{item}, []QualityDecision{decision}, "season"); !errors.Is(err, ErrStatusConflict) {
		t.Fatalf("stale guard: err = %v, want ErrStatusConflict", err)
	}
	var jobs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM download_artifacts WHERE media_file_id = $1`, f.fileID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("stale guard queued %d encode jobs (%v)", jobs, err)
	}
	rows, err := svc.ensureManagedDecisions(ctx, f.userID, req, []managedItem{item}, []QualityDecision{decision}, "season")
	if err != nil || len(rows) != 1 || rows[0].Status != StatusPreparing || rows[0].Format != FormatTranscode {
		t.Fatalf("replacement with a free slot: %+v %v", rows, err)
	}
}

// TestPreparedReplacementQuotaAccountingDB pins how a prepared replacement is
// counted: it creates no download, so the period quota ignores it, and a
// target whose prepared file is already ready takes no concurrent slot.
func TestPreparedReplacementQuotaAccountingDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = $1`, f.fileID)
	})
	suffix := time.Now().UnixNano()
	create := func(episodeID, status string) string {
		id := fmt.Sprintf("dl-%s-%d", episodeID, suffix)
		now := time.Now()
		if err := f.repo.Create(ctx, &Download{
			ID: id, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, MediaFileID: f.fileID,
			ContentID: f.contentID, EpisodeID: episodeID, Kind: KindQueued, Status: status,
			Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal,
			FileSize: 1024, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		return id
	}
	artifacts := NewArtifactManager(NewArtifactRepository(f.pool), f.repo, nil, stubQuotaPreparer{}, "replacement-accounting-test",
		func() *config.Config { return nil }, nil)
	decision := QualityDecision{
		RequestedQuality: Quality5Mbps, EffectiveQuality: Quality5Mbps, DeliveryFormat: FormatTranscode,
		TargetBitrateKbps: 5000, RequiresArtifact: true,
		PrepareTarget: playback.PrepareTarget{Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", TargetBitrateKbps: 5000},
	}
	replace := func(svc *Service, episodeID, id string) ([]*Download, error) {
		req := CreateRequest{ContentID: f.contentID, ProfileID: f.profileA, DeviceID: f.deviceA,
			ExpectedEntries: map[string]ManagedCreateExpectation{episodeID: {ID: id, Revision: 1}}}
		item := managedItem{file: &models.MediaFile{ID: f.fileID, ContentID: f.contentID, FileSize: 1024}, contentID: f.contentID, episodeID: episodeID}
		return svc.ensureManagedDecisions(ctx, f.userID, req, []managedItem{item}, []QualityDecision{decision}, "season")
	}

	// At the period limit (this account created two downloads this hour), a
	// replacement still goes through.
	first := create("ep-1", StatusReady)
	create("ep-2", StatusReady)
	periodOnly := NewService(f.repo, nil, NewQuantityLimiter(f.repo, 0, 2, time.Hour), nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	periodOnly.SetArtifactManager(artifacts)
	if rows, err := replace(periodOnly, "ep-1", first); err != nil || rows[0].Status != StatusPreparing {
		t.Fatalf("replacement at the period limit: %+v %v", rows, err)
	}

	// Once the prepared file is ready, another replacement to it takes no
	// slot, even with the only concurrent slot taken.
	if _, err := f.pool.Exec(ctx, `UPDATE download_artifacts SET status = 'ready' WHERE media_file_id = $1`, f.fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM downloads WHERE user_id = $1 AND episode_id = 'ep-1'`, f.userID); err != nil {
		t.Fatal(err)
	}
	create("busy", StatusDownloading)
	oneSlot := NewService(f.repo, nil, NewQuantityLimiter(f.repo, 1, 0, 0), nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	oneSlot.SetArtifactManager(artifacts)
	second := fmt.Sprintf("dl-ep-2-%d", suffix)
	if rows, err := replace(oneSlot, "ep-2", second); err != nil || rows[0].Status != StatusReady {
		t.Fatalf("replacement to a ready file with no free slot: %+v %v", rows, err)
	}
}
