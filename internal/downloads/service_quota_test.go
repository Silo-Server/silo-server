package downloads

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
	ep1, ep2 := f.contentID+"-ep-1", f.contentID+"-ep-2"
	ep1File, ep2File := f.episodeFile(t, ep1), f.episodeFile(t, ep2)
	create := func(id, episodeID string, fileID int, status string) {
		now := time.Now()
		if err := f.repo.Create(ctx, &Download{
			ID: id, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, MediaFileID: fileID,
			ContentID: f.contentID, EpisodeID: episodeID, Kind: KindQueued, Status: status,
			Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal,
			FileSize: 1024, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	suffix := time.Now().UnixNano()
	existingID := fmt.Sprintf("dl-ep-%d", suffix)
	create(existingID, ep1, ep1File, StatusReady)
	// Another download, being prepared, holds the account's only slot.
	busyID := fmt.Sprintf("dl-busy-%d", suffix)
	create(busyID, ep2, ep2File, StatusPreparing)

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
		ExpectedEntries: map[string]ManagedCreateExpectation{ep1: {ID: existingID, Revision: 1}}}
	item := managedItem{file: &models.MediaFile{ID: ep1File, ContentID: f.contentID, EpisodeID: ep1, FileSize: 1024}, contentID: f.contentID, episodeID: ep1}

	if _, err := svc.ensureManagedDecisions(ctx, f.userID, req, []managedItem{item}, []QualityDecision{decision}, "season"); !errors.Is(err, ErrConcurrentLimitReached) {
		t.Fatalf("replacement with no free slot: err = %v, want ErrConcurrentLimitReached", err)
	}
	row, err := f.repo.GetManagedEntry(ctx, f.userID, f.profileA, f.deviceA, f.contentID, ep1)
	if err != nil || row.Format != FormatOriginal || row.Revision != 1 {
		t.Fatalf("refused replacement changed the entry: %+v %v", row, err)
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM downloads WHERE id = $1`, busyID); err != nil {
		t.Fatal(err)
	}
	// A stale guard is refused before any encode is queued for it.
	stale := req
	stale.ExpectedEntries = map[string]ManagedCreateExpectation{ep1: {ID: existingID, Revision: 7}}
	if _, err := svc.ensureManagedDecisions(ctx, f.userID, stale, []managedItem{item}, []QualityDecision{decision}, "season"); !errors.Is(err, ErrStatusConflict) {
		t.Fatalf("stale guard: err = %v, want ErrStatusConflict", err)
	}
	var jobs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM download_artifacts WHERE media_file_id = $1`, ep1File).Scan(&jobs); err != nil || jobs != 0 {
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
	files := make(map[string]int)
	fileOf := func(episodeID string) int {
		if _, ok := files[episodeID]; !ok {
			files[episodeID] = f.episodeFile(t, f.contentID+"-"+episodeID)
		}
		return files[episodeID]
	}
	create := func(episodeID, status string) string {
		id := fmt.Sprintf("dl-%s-%d", episodeID, suffix)
		now := time.Now()
		if err := f.repo.Create(ctx, &Download{
			ID: id, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, MediaFileID: fileOf(episodeID),
			ContentID: f.contentID, EpisodeID: f.contentID + "-" + episodeID, Kind: KindQueued, Status: status,
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
		linked := f.contentID + "-" + episodeID
		req := CreateRequest{ContentID: f.contentID, ProfileID: f.profileA, DeviceID: f.deviceA,
			ExpectedEntries: map[string]ManagedCreateExpectation{linked: {ID: id, Revision: 1}}}
		item := managedItem{file: &models.MediaFile{ID: fileOf(episodeID), ContentID: f.contentID, EpisodeID: linked, FileSize: 1024}, contentID: f.contentID, episodeID: linked}
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
	// slot, even with the only concurrent slot taken. Episode 2's file is
	// prepared to the same target.
	if _, err := artifacts.Ensure(ctx, &models.MediaFile{ID: fileOf("ep-2"), ContentID: f.contentID, EpisodeID: f.contentID + "-ep-2", FileSize: 1024}, decision.DeliveryFormat, decision.PrepareTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE download_artifacts SET status = 'ready' WHERE media_file_id = ANY($1)`, []int{fileOf("ep-1"), fileOf("ep-2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM downloads WHERE user_id = $1 AND episode_id = $2`, f.userID, f.contentID+"-ep-1"); err != nil {
		t.Fatal(err)
	}
	create("busy", StatusPreparing)
	oneSlot := NewService(f.repo, nil, NewQuantityLimiter(f.repo, 1, 0, 0), nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	oneSlot.SetArtifactManager(artifacts)
	second := fmt.Sprintf("dl-ep-2-%d", suffix)
	if rows, err := replace(oneSlot, "ep-2", second); err != nil || rows[0].Status != StatusReady {
		t.Fatalf("replacement to a ready file with no free slot: %+v %v", rows, err)
	}
}

// replaceWhileTheLockIsHeld registers episode 1 of a fixture on device A with
// status, then runs a season request that replaces it with a 5 Mbps version
// still to prepare, at a concurrent limit of 1. The test holds the account's
// quota lock until the request has read the entry and is trying for the lock,
// runs change, then lets the request go and returns its error.
func replaceWhileTheLockIsHeld(t *testing.T, status string, change func(ctx context.Context, f managedFixture, existingID, ep2 string, ep2File int)) (managedFixture, string, int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := seedManagedFixture(t)
	ep1, ep2 := f.contentID+"-ep-1", f.contentID+"-ep-2"
	ep1File, ep2File := f.episodeFile(t, ep1), f.episodeFile(t, ep2)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM download_artifacts WHERE media_file_id = ANY($1)`, []int{ep1File, ep2File})
	})
	existingID := fmt.Sprintf("dl-ep-%d", time.Now().UnixNano())
	now := time.Now()
	if err := f.repo.Create(ctx, &Download{
		ID: existingID, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, MediaFileID: ep1File,
		ContentID: f.contentID, EpisodeID: ep1, Kind: KindQueued, Status: status,
		Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal,
		FileSize: 1024, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// The request runs on its own pool, named so the test can see it poll
	// for the lock.
	appName := "dl-replace-race-" + existingID
	cfg := f.pool.Config()
	cfg.ConnConfig.RuntimeParams["application_name"] = appName
	reqPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reqPool.Close)
	reqRepo := NewRepository(reqPool)
	svc := NewService(reqRepo, nil, NewQuantityLimiter(reqRepo, 1, 0, 0), nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	svc.SetArtifactManager(NewArtifactManager(
		NewArtifactRepository(reqPool), reqRepo, nil, stubQuotaPreparer{}, "replacement-race-test",
		func() *config.Config { return nil }, nil,
	))
	decision := QualityDecision{
		RequestedQuality: Quality5Mbps, EffectiveQuality: Quality5Mbps, DeliveryFormat: FormatTranscode,
		TargetBitrateKbps: 5000, RequiresArtifact: true,
		PrepareTarget: playback.PrepareTarget{Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", TargetBitrateKbps: 5000},
	}
	req := CreateRequest{ContentID: f.contentID, ProfileID: f.profileA, DeviceID: f.deviceA,
		ExpectedEntries: map[string]ManagedCreateExpectation{ep1: {ID: existingID, Revision: 1}}}
	item := managedItem{file: &models.MediaFile{ID: ep1File, ContentID: f.contentID, EpisodeID: ep1, FileSize: 1024}, contentID: f.contentID, episodeID: ep1}

	lock, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(context.Background()) }()
	if _, err := lock.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, downloadQuotaLockClassID, f.userID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := svc.ensureManagedDecisions(ctx, f.userID, req, []managedItem{item}, []QualityDecision{decision}, "season")
		done <- err
	}()
	// The request has read the entry once it tries for the lock.
	for {
		var polling bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE application_name = $1 AND query LIKE '%pg_try_advisory_xact_lock%')`, appName).Scan(&polling); err != nil {
			t.Fatal(err)
		}
		if polling {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("request finished without waiting for the lock: %v", err)
		case <-ctx.Done():
			t.Fatal("request never tried for the quota lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
	change(ctx, f, existingID, ep2, ep2File)
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	return f, existingID, ep1File, <-done
}

// TestPreparedReplacementCountsTheEntryStatusUnderTheLockDB pins that a season
// replacement's concurrent slot does not depend on the entry's status when
// the request read it. The entry is preparing then; while the request waits
// for the lock, its conversion finishes (which leaves the revision alone) and
// another download takes the freed slot. The replacement would start a second
// conversion at a limit of 1, so it is refused.
func TestPreparedReplacementCountsTheEntryStatusUnderTheLockDB(t *testing.T) {
	f, existingID, _, err := replaceWhileTheLockIsHeld(t, StatusPreparing, func(ctx context.Context, f managedFixture, existingID, ep2 string, ep2File int) {
		if _, err := f.pool.Exec(ctx, `UPDATE downloads SET status = $2 WHERE id = $1`, existingID, StatusReady); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if err := f.repo.Create(ctx, &Download{
			ID: existingID + "-other", UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, MediaFileID: ep2File,
			ContentID: f.contentID, EpisodeID: ep2, Kind: KindQueued, Status: StatusPreparing,
			Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal,
			FileSize: 1024, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, ErrConcurrentLimitReached) {
		var preparing int
		_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM downloads WHERE user_id = $1 AND status = 'preparing'`, f.userID).Scan(&preparing)
		t.Fatalf("replacement after its entry's conversion finished: err = %v, want ErrConcurrentLimitReached (%d preparing at a limit of 1)", err, preparing)
	}
	row, err := f.repo.GetByID(context.Background(), existingID)
	if err != nil || row.Status != StatusReady || row.Revision != 1 {
		t.Fatalf("refused replacement changed the entry: %+v %v", row, err)
	}
}

// TestPreparedReplacementRefusesAStaleGuardUnderTheLockDB pins that a season
// replacement whose entry another request replaced while it waited for the
// lock is refused before it queues an encode.
func TestPreparedReplacementRefusesAStaleGuardUnderTheLockDB(t *testing.T) {
	f, _, ep1File, err := replaceWhileTheLockIsHeld(t, StatusReady, func(ctx context.Context, f managedFixture, existingID, _ string, _ int) {
		// The other request's replacement: a new revision, now preparing.
		if _, err := f.pool.Exec(ctx, `UPDATE downloads SET status = $2, revision = 2 WHERE id = $1`, existingID, StatusPreparing); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, ErrStatusConflict) {
		t.Fatalf("stale guard: err = %v, want ErrStatusConflict", err)
	}
	var jobs int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM download_artifacts WHERE media_file_id = $1`, ep1File).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("stale guard queued %d encode jobs (%v)", jobs, err)
	}
}

// TestReadyArtifactSingleCreateTakesNoConcurrentSlotDB pins that a new
// single-item entry whose prepared file is already ready registers with the
// account's only concurrent slot taken: it starts no preparation, so it counts
// only toward the period quota. One that would start preparing is still
// refused without queueing an encode.
func TestReadyArtifactSingleCreateTakesNoConcurrentSlotDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	ep1, ep2, busyEp := f.contentID+"-ep-1", f.contentID+"-ep-2", f.contentID+"-busy"
	ep1File, ep2File, busyFile := f.episodeFile(t, ep1), f.episodeFile(t, ep2), f.episodeFile(t, busyEp)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = ANY($1)`, []int{ep1File, ep2File})
	})
	now := time.Now()
	if err := f.repo.Create(ctx, &Download{
		ID: fmt.Sprintf("dl-busy-%d", now.UnixNano()), UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA,
		MediaFileID: busyFile, ContentID: f.contentID, EpisodeID: busyEp, Kind: KindQueued, Status: StatusPreparing,
		Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal,
		FileSize: 1024, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed busy download: %v", err)
	}

	artifacts := NewArtifactManager(NewArtifactRepository(f.pool), f.repo, nil, stubQuotaPreparer{}, "ready-single-create-test",
		func() *config.Config { return nil }, nil)
	svc := NewService(f.repo, nil, NewQuantityLimiter(f.repo, 1, 0, 0), nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	svc.SetArtifactManager(artifacts)
	decision := QualityDecision{
		RequestedQuality: Quality5Mbps, EffectiveQuality: Quality5Mbps, DeliveryFormat: FormatTranscode,
		TargetBitrateKbps: 5000, RequiresArtifact: true,
		PrepareTarget: playback.PrepareTarget{Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", TargetBitrateKbps: 5000},
	}
	file := func(id int, episodeID string) *models.MediaFile {
		return &models.MediaFile{ID: id, ContentID: f.contentID, EpisodeID: episodeID, FileSize: 1024}
	}
	req := CreateRequest{ContentID: f.contentID, ProfileID: f.profileA, DeviceID: f.deviceA, Quality: Quality5Mbps}

	if _, err := artifacts.Ensure(ctx, file(ep1File, ep1), decision.DeliveryFormat, decision.PrepareTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE download_artifacts SET status = 'ready' WHERE media_file_id = $1`, ep1File); err != nil {
		t.Fatal(err)
	}
	d, err := svc.createArtifactDownload(ctx, f.userID, req, file(ep1File, ep1), decision)
	if err != nil || d.Status != StatusReady {
		t.Fatalf("ready prepared file with no free slot: %+v %v", d, err)
	}

	if _, err := svc.createArtifactDownload(ctx, f.userID, req, file(ep2File, ep2), decision); !errors.Is(err, ErrConcurrentLimitReached) {
		t.Fatalf("unprepared file with no free slot: err = %v, want ErrConcurrentLimitReached", err)
	}
	var jobs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM download_artifacts WHERE media_file_id = $1`, ep2File).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("refused create queued %d encode jobs (%v)", jobs, err)
	}
}

// TestPreparedSingleReplacementCountsTowardConcurrentLimitDB pins that a
// single-item replacement that starts preparing takes a concurrent slot, as
// a new download and a season replacement do. It creates no download, so the
// period quota ignores it, and an entry already preparing is not counted
// twice when it is replaced again.
func TestPreparedSingleReplacementCountsTowardConcurrentLimitDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	ep1, busyEp := f.contentID+"-ep-1", f.contentID+"-busy"
	ep1File, busyFile := f.episodeFile(t, ep1), f.episodeFile(t, busyEp)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = $1`, ep1File)
	})
	suffix := time.Now().UnixNano()
	existingID, busyID := fmt.Sprintf("dl-ep-%d", suffix), fmt.Sprintf("dl-busy-%d", suffix)
	for _, seed := range []struct {
		id, episodeID, status string
		fileID                int
	}{{existingID, ep1, StatusReady, ep1File}, {busyID, busyEp, StatusPreparing, busyFile}} {
		now := time.Now()
		if err := f.repo.Create(ctx, &Download{
			ID: seed.id, UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA, MediaFileID: seed.fileID,
			ContentID: f.contentID, EpisodeID: seed.episodeID, Kind: KindQueued, Status: seed.status,
			Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal,
			FileSize: 1024, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create %s: %v", seed.id, err)
		}
	}

	// One slot, and the account has created as many downloads this hour as
	// the period quota allows.
	svc := NewService(f.repo, nil, NewQuantityLimiter(f.repo, 1, 2, time.Hour), nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	svc.SetArtifactManager(NewArtifactManager(
		NewArtifactRepository(f.pool), f.repo, nil, stubQuotaPreparer{}, "single-replacement-quota-test",
		func() *config.Config { return nil }, nil,
	))
	decision := func(quality string, kbps int) QualityDecision {
		return QualityDecision{
			RequestedQuality: quality, EffectiveQuality: quality, DeliveryFormat: FormatTranscode,
			TargetBitrateKbps: kbps, RequiresArtifact: true,
			PrepareTarget: playback.PrepareTarget{Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", TargetBitrateKbps: kbps},
		}
	}
	file := &models.MediaFile{ID: ep1File, ContentID: f.contentID, EpisodeID: ep1, FileSize: 1024}
	replace := func(revision int, d QualityDecision) (*Download, error) {
		req := CreateRequest{ContentID: f.contentID, ProfileID: f.profileA, DeviceID: f.deviceA, Quality: d.RequestedQuality,
			ExpectedRevision: new(revision), ExpectedDownloadID: existingID}
		return svc.createArtifactDownload(ctx, f.userID, req, file, d)
	}

	if _, err := replace(1, decision(Quality5Mbps, 5000)); !errors.Is(err, ErrConcurrentLimitReached) {
		t.Fatalf("replacement with no free slot: err = %v, want ErrConcurrentLimitReached", err)
	}
	row, err := f.repo.GetByID(ctx, existingID)
	if err != nil || row.Format != FormatOriginal || row.Revision != 1 {
		t.Fatalf("refused replacement changed the entry: %+v %v", row, err)
	}
	var jobs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM download_artifacts WHERE media_file_id = $1`, ep1File).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("refused replacement queued %d encode jobs (%v)", jobs, err)
	}

	if _, err := f.pool.Exec(ctx, `UPDATE downloads SET status = $2 WHERE id = $1`, busyID, StatusReady); err != nil {
		t.Fatal(err)
	}
	d, err := replace(1, decision(Quality5Mbps, 5000))
	if err != nil || d.Status != StatusPreparing || d.Revision != 2 {
		t.Fatalf("replacement with a free slot at the period limit: %+v %v", d, err)
	}
	// The entry now holds the only slot; replacing it again moves that slot.
	d, err = replace(2, decision(Quality2Mbps, 2000))
	if err != nil || d.Status != StatusPreparing || d.Revision != 3 {
		t.Fatalf("replacing the entry that holds the slot: %+v %v", d, err)
	}
}

// TestManagedBatchOverConcurrentCapDB pins how a new managed batch is counted
// against the concurrent cap. Original entries register ready and the app
// queues their transfers, so a season larger than the cap registers even when
// the account is over the cap. New entries that start preparing do count, so
// a transcoded season with no free slot is refused without inserting a row or
// queueing an encode. The period quota still covers every new entry.
func TestManagedBatchOverConcurrentCapDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = $1`, f.fileID)
	})
	countRows := func() int {
		var n int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM downloads WHERE user_id = $1`, f.userID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Three downloads being prepared put the account over the cap of 2, as
	// after an administrator lowers it.
	suffix := time.Now().UnixNano()
	for i := range 3 {
		now := time.Now()
		episodeID := fmt.Sprintf("%s-busy-%d", f.contentID, i)
		if err := f.repo.Create(ctx, &Download{
			ID: fmt.Sprintf("dl-busy-%d-%d", i, suffix), UserID: f.userID, ProfileID: f.profileA, DeviceID: f.deviceA,
			MediaFileID: f.episodeFile(t, episodeID), ContentID: f.contentID, EpisodeID: episodeID, Kind: KindQueued,
			Status: StatusPreparing, Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal,
			FileSize: 1024, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed busy download: %v", err)
		}
	}

	limiter := NewQuantityLimiter(f.repo, 2, 0, 0)
	svc := NewService(f.repo, nil, limiter, nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	svc.SetArtifactManager(NewArtifactManager(
		NewArtifactRepository(f.pool), f.repo, nil, stubQuotaPreparer{}, "managed-batch-cap-test",
		func() *config.Config { return nil }, nil,
	))
	req := CreateRequest{ContentID: f.contentID, ProfileID: f.profileA, DeviceID: f.deviceA}
	episodes := func(prefix string, n int) []managedItem {
		items := make([]managedItem, n)
		for i := range items {
			episodeID := fmt.Sprintf("%s-%s-%d", f.contentID, prefix, i+1)
			items[i] = managedItem{
				file:      &models.MediaFile{ID: f.episodeFile(t, episodeID), ContentID: f.contentID, EpisodeID: episodeID, FileSize: 1024},
				contentID: f.contentID, episodeID: episodeID,
			}
		}
		return items
	}

	original := episodes("original", 4)
	rows, err := svc.ensureManaged(ctx, f.userID, req, original, originalDecision(), "season-original")
	if err != nil {
		t.Fatalf("original season over the cap: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("registered %d rows, want 4", len(rows))
	}
	for _, row := range rows {
		if row.Status != StatusReady {
			t.Fatalf("original entry %s status = %q, want ready", row.EpisodeID, row.Status)
		}
	}

	transcoded := episodes("transcoded", 2)
	decision := QualityDecision{
		RequestedQuality: Quality5Mbps, EffectiveQuality: Quality5Mbps, DeliveryFormat: FormatTranscode,
		TargetBitrateKbps: 5000, RequiresArtifact: true,
		PrepareTarget: playback.PrepareTarget{Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", TargetBitrateKbps: 5000},
	}
	if _, err := svc.ensureManagedDecisions(ctx, f.userID, req, transcoded, uniformDecisions(decision, len(transcoded)), "season-transcoded"); !errors.Is(err, ErrConcurrentLimitReached) {
		t.Fatalf("transcoded season with no free slot: err = %v, want ErrConcurrentLimitReached", err)
	}
	if got := countRows(); got != 7 {
		t.Fatalf("refused transcoded season left %d rows, want 7", got)
	}
	var jobs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM download_artifacts a JOIN media_files mf ON mf.id = a.media_file_id WHERE mf.content_id = $1`, f.contentID).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("refused transcoded season queued %d encode jobs (%v)", jobs, err)
	}

	// Seven downloads were created this hour; a period quota of 7 refuses an
	// eighth, even an original one.
	limiter.Reload(2, 7, time.Hour)
	if _, err := svc.ensureManaged(ctx, f.userID, req, episodes("late", 1), originalDecision(), "season-late"); !errors.Is(err, ErrPeriodLimitReached) {
		t.Fatalf("batch over the period quota: err = %v, want ErrPeriodLimitReached", err)
	}
	if got := countRows(); got != 7 {
		t.Fatalf("refused batch left %d rows, want 7", got)
	}
}

// TestDeviceReportedTransfersHoldNoConcurrentSlotDB pins that an entry its
// device reported as downloading does not count toward the concurrent cap.
// The server cannot end that report when the transfer is abandoned, so it
// would block the account's new downloads for good. Work the server drives
// still counts, and a reported entry takes a slot when it returns to
// preparing.
func TestDeviceReportedTransfersHoldNoConcurrentSlotDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM download_artifacts a USING media_files mf WHERE mf.id = a.media_file_id AND mf.content_id = $1`, f.contentID)
	})
	suffix := time.Now().UnixNano()
	episode := func(name string) managedItem {
		episodeID := f.contentID + "-" + name
		return managedItem{
			file:      &models.MediaFile{ID: f.episodeFile(t, episodeID), ContentID: f.contentID, EpisodeID: episodeID, FileSize: 1024},
			contentID: f.contentID, episodeID: episodeID,
		}
	}
	seed := func(name, profileID, deviceID, status string) *Download {
		it, now := episode(name), time.Now()
		d := &Download{
			ID: fmt.Sprintf("dl-%s-%d", name, suffix), UserID: f.userID, ProfileID: profileID, DeviceID: deviceID,
			MediaFileID: it.file.ID, ContentID: f.contentID, EpisodeID: it.episodeID, Kind: KindQueued,
			Status: status, Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal,
			FileSize: 1024, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := f.repo.Create(ctx, d); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		return d
	}
	limiter := NewQuantityLimiter(f.repo, 2, 0, 0)
	svc := NewService(f.repo, nil, limiter, nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	artifacts := NewArtifactManager(NewArtifactRepository(f.pool), f.repo, nil, stubQuotaPreparer{}, "reported-transfer-test",
		func() *config.Config { return nil }, nil)
	svc.SetArtifactManager(artifacts)
	decision := QualityDecision{
		RequestedQuality: Quality5Mbps, EffectiveQuality: Quality5Mbps, DeliveryFormat: FormatTranscode,
		TargetBitrateKbps: 5000, RequiresArtifact: true,
		PrepareTarget: playback.PrepareTarget{Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", TargetBitrateKbps: 5000},
	}

	// Three transfers the device started and never finished: more than the
	// cap of 2.
	var reported *Download
	for i := range 3 {
		reported = seed(fmt.Sprintf("reported-%d", i), f.profileA, f.deviceA, StatusDownloading)
	}
	if err := limiter.Check(ctx, f.userID, 1); err != nil {
		t.Fatalf("account-level download beside three reported transfers: %v", err)
	}
	req := CreateRequest{ContentID: f.contentID, ProfileID: f.profileA, DeviceID: f.deviceA}
	rows, err := svc.ensureManagedDecisions(ctx, f.userID, req, []managedItem{episode("prepared")}, []QualityDecision{decision}, "")
	if err != nil || len(rows) != 1 || rows[0].Status != StatusPreparing {
		t.Fatalf("prepared entry beside three reported transfers: %+v %v", rows, err)
	}

	// That entry and an account-level download in flight fill the cap.
	seed("account", "", "", StatusQueued)
	if err := limiter.Check(ctx, f.userID, 1); !errors.Is(err, ErrConcurrentLimitReached) {
		t.Fatalf("third download the server drives: err = %v, want ErrConcurrentLimitReached", err)
	}

	// A reported entry whose prepared file must be made again starts
	// preparing, which takes a slot.
	artifact, err := artifacts.Ensure(ctx, episode("again").file, decision.DeliveryFormat, decision.PrepareTarget)
	if err != nil {
		t.Fatal(err)
	}
	reported.ArtifactID = artifact.ID
	if activates, err := artifacts.prepareAgainActivates(ctx, reported); err != nil || !activates {
		t.Fatalf("reported entry returning to preparing takes a slot = %v (%v), want true", activates, err)
	}

	// Preparing it again creates no download, so an account already over the
	// period quota (an administrator lowered it) is not refused for it.
	overPeriod := NewQuantityLimiter(f.repo, 0, 1, time.Hour)
	if err := overPeriod.CheckCounts(ctx, f.userID, 1, 0); err != nil {
		t.Fatalf("prepare again over the period quota: %v", err)
	}
	if err := overPeriod.Check(ctx, f.userID, 1); !errors.Is(err, ErrPeriodLimitReached) {
		t.Fatalf("new download over the period quota: err = %v, want ErrPeriodLimitReached", err)
	}
}
