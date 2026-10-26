package metadata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// localArtworkTestStore is the real queue with the target-row reads answered
// as if every job's target still names its source and holds no cached copy,
// so the tests need no catalog rows.
type localArtworkTestStore struct {
	*ImageCacheJobRepository
}

func (s localArtworkTestStore) CurrentTargetSourcePath(_ context.Context, job *models.MetadataImageCacheJob) (string, error) {
	return job.SourcePath, nil
}

func (s localArtworkTestStore) CurrentTargetCachedPath(context.Context, *models.MetadataImageCacheJob) (string, error) {
	return "", nil
}

// recordingArtworkTargets records every source-matched artwork update, from
// any of the processor's target types. Copies run on pool goroutines.
type recordingArtworkTargets struct {
	mu      sync.Mutex
	updates map[string]string // "<target type>:<id>:<image type>" -> cached path
}

func newRecordingArtworkTargets() *recordingArtworkTargets {
	return &recordingArtworkTargets{updates: make(map[string]string)}
}

func (r *recordingArtworkTargets) record(key, cachedPath string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates[key] = cachedPath
	return true, nil
}

func (r *recordingArtworkTargets) get(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updates[key]
}

type recordingItemTargets struct{ *recordingArtworkTargets }

func (r recordingItemTargets) UpdateArtworkIfSourceMatches(_ context.Context, contentID, imageType, _, cachedPath, _ string) (bool, error) {
	return r.record(ImageCacheTargetItem+":"+contentID+":"+imageType, cachedPath)
}

type recordingSeasonTargets struct{ *recordingArtworkTargets }

func (r recordingSeasonTargets) UpdateArtworkIfSourceMatches(_ context.Context, contentID, _, cachedPath, _ string) (bool, error) {
	return r.record(ImageCacheTargetSeason+":"+contentID+":"+ImageCacheImagePoster, cachedPath)
}

type recordingEpisodeTargets struct{ *recordingArtworkTargets }

func (r recordingEpisodeTargets) UpdateStillIfSourceMatches(_ context.Context, contentID, _, cachedPath, _ string) (bool, error) {
	return r.record(ImageCacheTargetEpisode+":"+contentID+":"+ImageCacheImageStill, cachedPath)
}

type recordingItemBackdropTargets struct{ *recordingArtworkTargets }

func (r recordingItemBackdropTargets) UpdateArtworkIfSourceMatches(_ context.Context, id int64, _, cachedPath, _ string) (bool, error) {
	return r.record(ImageCacheTargetItemBackdrop+":"+strconv.FormatInt(id, 10)+":"+ImageCacheImageBackdrop, cachedPath)
}

type recordingPersonTargets struct{ *recordingArtworkTargets }

func (r recordingPersonTargets) UpdatePhotoIfSourceMatches(_ context.Context, personID int64, _, cachedPath, _ string) (bool, error) {
	return r.record(ImageCacheTargetPerson+":"+strconv.FormatInt(personID, 10)+":"+ImageCacheImageProfile, cachedPath)
}

// syncByteImageCacher is a goroutine-safe byte cacher that builds keys the way
// the real cacher does.
type syncByteImageCacher struct {
	fakeImageCacher
	mu    sync.Mutex
	calls int
}

func (c *syncByteImageCacher) CacheImageBytes(_ context.Context, _ []byte, req CacheImageRequest) (*CacheImageResult, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return &CacheImageResult{
		BasePath: fmt.Sprintf("%s/%s/%s/%s/%s", req.ProviderID, req.ContentType, req.ContentID, req.KeyDiscriminator, ImageTypeToString(req.ImageType)),
		Ext:      ".webp",
	}, nil
}

func (c *syncByteImageCacher) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func newLocalArtworkDBProcessor(pool *pgxpool.Pool, roots []string) (*ImageCacheProcessor, *recordingArtworkTargets, *syncByteImageCacher) {
	targets := newRecordingArtworkTargets()
	cacher := &syncByteImageCacher{}
	p := NewImageCacheProcessorWithTargets(localArtworkTestStore{NewImageCacheJobRepository(pool)}, cacher, nil, ImageCacheProcessorTargets{
		Items:         recordingItemTargets{targets},
		ItemBackdrops: recordingItemBackdropTargets{targets},
		Seasons:       recordingSeasonTargets{targets},
		Episodes:      recordingEpisodeTargets{targets},
		People:        recordingPersonTargets{targets},
	})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: roots})
	return p, targets, cacher
}

type localArtworkJobRow struct {
	status        string
	attempts      int
	lastError     string
	nextAttemptIn time.Duration
}

func readLocalArtworkJob(t *testing.T, pool *pgxpool.Pool, targetType, targetID, imageType string) localArtworkJobRow {
	t.Helper()
	var row localArtworkJobRow
	var seconds float64
	if err := pool.QueryRow(context.Background(), `
		SELECT status, attempt_count, last_error, EXTRACT(EPOCH FROM next_attempt_at - NOW())
		FROM metadata_image_cache_jobs
		WHERE target_type = $1 AND target_content_id = $2 AND image_type = $3 AND target_language = ''
	`, targetType, targetID, imageType).Scan(&row.status, &row.attempts, &row.lastError, &seconds); err != nil {
		t.Fatalf("read %s %s %s job: %v", targetType, targetID, imageType, err)
	}
	row.nextAttemptIn = time.Duration(seconds * float64(time.Second))
	return row
}

func cleanupLocalArtworkJobs(t *testing.T, pool *pgxpool.Pool, scope string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM metadata_image_cache_jobs WHERE target_content_id LIKE $1 || '%' OR series_id = $1
		`, scope)
	})
}

func writeSidecar(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("sidecar "+path), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sidecar: %v", err)
	}
	return LocalImageSourceURL(path, info.Size(), info.ModTime())
}

// TestLocalArtworkCopiedOnDiscoveryDB pins copy on discovery: the process
// that discovered item (poster and banner), extra backdrop, season, episode
// and person artwork copies it, other drains cannot take the jobs during the
// discovery hold, and every job ends succeeded with the cached copy written to
// its slot.
func TestLocalArtworkCopiedOnDiscoveryDB(t *testing.T) {
	pool := imageCacheQueueTestPool(t)
	ctx := context.Background()
	seriesID := fmt.Sprintf("series:local-copy-%d", time.Now().UnixNano())
	cleanupLocalArtworkJobs(t, pool, seriesID)
	const personID, backdropID = "987654321", "876543219"
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM metadata_image_cache_jobs
			WHERE (target_type = 'person' AND target_content_id = $1) OR (target_type = 'item_backdrop' AND target_content_id = $2)
		`, personID, backdropID)
	})

	root := t.TempDir()
	show := filepath.Join(root, "Show")
	season, episode := 1, 1
	seasonID, episodeID := seriesID+"-s1", seriesID+"-s1e1"
	inputs := []EnqueueImageCacheJobInput{
		{TargetType: ImageCacheTargetItem, TargetContentID: seriesID, SeriesID: seriesID, ContentType: "series",
			ImageType: ImageCacheImagePoster, SourcePath: writeSidecar(t, filepath.Join(show, "poster.jpg"))},
		{TargetType: ImageCacheTargetSeason, TargetContentID: seasonID, SeriesID: seriesID, ContentType: "series",
			ImageType: ImageCacheImagePoster, SeasonNumber: &season, SourcePath: writeSidecar(t, filepath.Join(show, "Season 01", "poster.jpg"))},
		{TargetType: ImageCacheTargetEpisode, TargetContentID: episodeID, SeriesID: seriesID, ContentType: "series",
			ImageType: ImageCacheImageStill, SeasonNumber: &season, EpisodeNumber: &episode,
			SourcePath: writeSidecar(t, filepath.Join(show, "Season 01", "Show S01E01-thumb.jpg"))},
		// A person photo found in the series folder: SeriesID names the
		// content whose library roots confine the read.
		{TargetType: ImageCacheTargetPerson, TargetContentID: personID, SeriesID: seriesID, ContentType: "person",
			ImageType: ImageCacheImageProfile, SourcePath: writeSidecar(t, filepath.Join(show, ".actors", "Jane_Doe.jpg"))},
		{TargetType: ImageCacheTargetItem, TargetContentID: seriesID, SeriesID: seriesID, ContentType: "series",
			ImageType: ImageCacheImageBanner, SourcePath: writeSidecar(t, filepath.Join(show, "banner.jpg"))},
		// A backdrop after the primary targets its media_item_backdrops row.
		{TargetType: ImageCacheTargetItemBackdrop, TargetContentID: backdropID, SeriesID: seriesID, ContentType: "series",
			ImageType: ImageCacheImageBackdrop, SourcePath: writeSidecar(t, filepath.Join(show, "extrafanart", "one.jpg"))},
	}
	for i := range inputs {
		inputs[i].ProviderID = imageCacheLocalProviderID
		inputs[i].ProviderContentID = seriesID
	}
	// A person's copy is keyed by the person, so two people credited on one
	// title never share a local/ prefix.
	inputs[3].ProviderContentID = personID

	t.Run("held from other drains", func(t *testing.T) {
		repo := NewImageCacheJobRepository(pool)
		// Drains claim due jobs only (claimDueQuery), so a job that is not
		// due is out of their reach.
		assertHeld := func(t *testing.T, what string) localArtworkJobRow {
			t.Helper()
			got := readLocalArtworkJob(t, pool, ImageCacheTargetItem, seriesID, ImageCacheImagePoster)
			if got.status != ImageCacheStatusQueued || got.nextAttemptIn < localArtworkDiscoveryHold-time.Minute {
				t.Fatalf("%s: job = %+v, want queued behind the %s discovery hold", what, got, localArtworkDiscoveryHold)
			}
			return got
		}
		queued, err := repo.enqueueHeldLocalJobs(ctx, inputs[:1])
		if err != nil || len(queued) != 1 {
			t.Fatalf("enqueueHeldLocalJobs = %d queued, %v; want 1", len(queued), err)
		}
		held := assertHeld(t, "discovered")

		// Rediscovered during its hold, the hold is not extended.
		if _, err := repo.enqueueHeldLocalJobs(ctx, inputs[:1]); err != nil {
			t.Fatalf("re-enqueue: %v", err)
		}
		if got := assertHeld(t, "rediscovered while held"); got.nextAttemptIn > held.nextAttemptIn {
			t.Fatalf("rediscovery moved the hold from %s to %s", held.nextAttemptIn, got.nextAttemptIn)
		}

		// A manual refresh of the target does not make it due for other
		// drains, but claims it itself.
		if err := repo.retryTargetNow(ctx, seriesID); err != nil {
			t.Fatalf("retryTargetNow: %v", err)
		}
		assertHeld(t, "after a manual refresh's retry")
		jobs, err := repo.claimDueForTarget(ctx, "refreshing-server", seriesID, 10)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("refresh claim = %d jobs, %v; want the held job", len(jobs), err)
		}
		if err := repo.RequeueClaimed(ctx, []int64{jobs[0].ID}, "refreshing-server"); err != nil {
			t.Fatalf("requeue: %v", err)
		}
		assertHeld(t, "handed back unstarted by the refresh")

		// A job already waiting and due (another server deferred it for
		// missing storage) is held again when this server rediscovers it.
		if _, err := pool.Exec(ctx, `
			UPDATE metadata_image_cache_jobs SET next_attempt_at = NOW() WHERE target_content_id = $1
		`, seriesID); err != nil {
			t.Fatalf("make job due: %v", err)
		}
		if queued, err := repo.enqueueHeldLocalJobs(ctx, inputs[:1]); err != nil || len(queued) != 1 {
			t.Fatalf("re-enqueue = %d queued, %v; want 1", len(queued), err)
		}
		assertHeld(t, "rediscovered while due")
		job, err := repo.claimQueuedLocalJob(ctx, inputs[0], "discovering-server")
		if err != nil || job == nil || job.LockedBy != "discovering-server" {
			t.Fatalf("discovering claim = %+v, %v; want the held job", job, err)
		}
		if err := repo.RequeueClaimed(ctx, []int64{job.ID}, "discovering-server"); err != nil {
			t.Fatalf("requeue: %v", err)
		}
	})

	t.Run("discovery does not skip a failure's backoff", func(t *testing.T) {
		repo := NewImageCacheJobRepository(pool)
		if _, err := pool.Exec(ctx, `
			UPDATE metadata_image_cache_jobs
			SET attempt_count = 1, next_attempt_at = NOW() + interval '1 hour', last_error = 'local image read failed: io'
			WHERE target_type = 'item' AND target_content_id = $1 AND image_type = 'poster'
		`, seriesID); err != nil {
			t.Fatalf("back off job: %v", err)
		}
		queued, err := repo.enqueueHeldLocalJobs(ctx, inputs[:1])
		if err != nil || len(queued) != 1 {
			t.Fatalf("enqueueHeldLocalJobs = %d queued, %v; want the backed-off job reported", len(queued), err)
		}
		if job, err := repo.claimQueuedLocalJob(ctx, inputs[0], "discovering-server"); err != nil || job != nil {
			t.Fatalf("discovering claim = %+v, %v; want none while the failure backs off", job, err)
		}

		// A deferral for missing storage that followed the failure does not
		// keep the discovering server out.
		if _, err := pool.Exec(ctx, `
			UPDATE metadata_image_cache_jobs SET last_error = $2
			WHERE target_type = 'item' AND target_content_id = $1 AND image_type = 'poster'
		`, seriesID, localImageStorageUnavailable+": /media/Show"); err != nil {
			t.Fatalf("defer job: %v", err)
		}
		job, err := repo.claimQueuedLocalJob(ctx, inputs[0], "discovering-server")
		if err != nil || job == nil {
			t.Fatalf("discovering claim = %+v, %v; want the storage-deferred job", job, err)
		}
		if err := repo.RequeueClaimed(ctx, []int64{job.ID}, "discovering-server"); err != nil {
			t.Fatalf("requeue: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE metadata_image_cache_jobs SET attempt_count = 0, next_attempt_at = NOW(), last_error = ''
			WHERE target_type = 'item' AND target_content_id = $1 AND image_type = 'poster'
		`, seriesID); err != nil {
			t.Fatalf("reset job: %v", err)
		}
	})

	t.Run("a copy of a replaced source cannot finish the newer claim", func(t *testing.T) {
		repo := NewImageCacheJobRepository(pool)
		slot := inputs[1]
		if _, err := repo.enqueueHeldLocalJobs(ctx, []EnqueueImageCacheJobInput{slot}); err != nil {
			t.Fatalf("enqueue v1: %v", err)
		}
		older, err := repo.claimQueuedLocalJob(ctx, slot, "local-artwork:older")
		if err != nil || older == nil {
			t.Fatalf("claim v1 = %+v, %v", older, err)
		}
		replaced := slot
		replaced.SourcePath = writeSidecar(t, filepath.Join(show, "Season 01", "folder.jpg"))
		if _, err := repo.enqueueHeldLocalJobs(ctx, []EnqueueImageCacheJobInput{replaced}); err != nil {
			t.Fatalf("enqueue v2: %v", err)
		}
		newer, err := repo.claimQueuedLocalJob(ctx, replaced, "local-artwork:newer")
		if err != nil || newer == nil || newer.ID != older.ID {
			t.Fatalf("claim v2 = %+v, %v; want the same job row", newer, err)
		}
		if err := repo.MarkSucceeded(ctx, older.ID, older.LockedBy); err != nil {
			t.Fatalf("mark older succeeded: %v", err)
		}
		if got := readLocalArtworkJob(t, pool, slot.TargetType, slot.TargetContentID, slot.ImageType); got.status != ImageCacheStatusRunning {
			t.Fatalf("job after the older copy finished = %+v, want the newer claim still running", got)
		}
		if err := repo.RequeueClaimed(ctx, []int64{newer.ID}, newer.LockedBy); err != nil {
			t.Fatalf("release newer claim: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE metadata_image_cache_jobs SET source_path = $1 WHERE id = $2`, slot.SourcePath, newer.ID); err != nil {
			t.Fatalf("restore source: %v", err)
		}
	})

	t.Run("copied by the discovering process", func(t *testing.T) {
		p, targets, cacher := newLocalArtworkDBProcessor(pool, []string{root})
		if err := p.CopyLocalArtwork(ctx, inputs); err != nil {
			t.Fatalf("CopyLocalArtwork: %v", err)
		}
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		poll := time.NewTicker(10 * time.Millisecond)
		defer poll.Stop()
		for _, in := range inputs {
			got := readLocalArtworkJob(t, pool, in.TargetType, in.TargetContentID, in.ImageType)
			for got.status != ImageCacheStatusSucceeded {
				select {
				case <-deadline.C:
					t.Fatalf("%s copy did not finish: %+v", in.TargetType, got)
				case <-poll.C:
				}
				got = readLocalArtworkJob(t, pool, in.TargetType, in.TargetContentID, in.ImageType)
			}
			if got.attempts != 0 {
				t.Errorf("%s job = %+v, want succeeded on the first attempt", in.TargetType, got)
			}
			key := in.TargetType + ":" + in.TargetContentID + ":" + in.ImageType
			if cached := targets.get(key); !strings.HasPrefix(cached, "local/") {
				t.Errorf("%s slot cached path = %q, want a local/ copy", key, cached)
			}
		}
		if copies := cacher.callCount(); copies != len(inputs) {
			t.Errorf("copies = %d, want %d", copies, len(inputs))
		}

		// Rediscovering the unchanged files queues nothing to copy again.
		if err := p.CopyLocalArtwork(ctx, inputs); err != nil {
			t.Fatalf("CopyLocalArtwork again: %v", err)
		}
		for _, in := range inputs {
			if got := readLocalArtworkJob(t, pool, in.TargetType, in.TargetContentID, in.ImageType); got.status != ImageCacheStatusSucceeded {
				t.Errorf("%s rediscovered job = %+v, want it left succeeded", in.TargetType, got)
			}
		}
		if copies := cacher.callCount(); copies != len(inputs) {
			t.Errorf("copies after rediscovery = %d, want still %d", copies, len(inputs))
		}
	})
}

// TestLocalArtworkRetriesWithoutLibraryRootDB pins how a claimer that cannot
// serve a local job records it. A server without the file's storage mounted
// (the library root absent, an empty mount point, or a share nested in a
// present root) releases the job without counting an attempt and without the
// week-long stable-failure delay, and waits longer when that repeats. A file
// missing from a folder the server does see stays a stable failure, and
// confinement still rejects a symlink escaping the root.
func TestLocalArtworkRetriesWithoutLibraryRootDB(t *testing.T) {
	pool := imageCacheQueueTestPool(t)
	ctx := context.Background()

	repo := NewImageCacheJobRepository(pool)
	claimAndProcess := func(t *testing.T, contentID string, roots []string) localArtworkJobRow {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			UPDATE metadata_image_cache_jobs SET next_attempt_at = NOW() WHERE target_content_id = $1
		`, contentID); err != nil {
			t.Fatalf("make job due: %v", err)
		}
		jobs, err := repo.claimDueForTarget(ctx, "mountless-server", contentID, 1)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("claim = %d jobs, %v; want 1", len(jobs), err)
		}
		p, _, _ := newLocalArtworkDBProcessor(pool, roots)
		p.processClaimedJobs(ctx, "mountless-server", jobs, 1, time.Now())
		return readLocalArtworkJob(t, pool, ImageCacheTargetItem, contentID, ImageCacheImagePoster)
	}
	enqueue := func(t *testing.T, source string) string {
		t.Helper()
		contentID := fmt.Sprintf("movie:local-root-%d", time.Now().UnixNano())
		cleanupLocalArtworkJobs(t, pool, contentID)
		if err := repo.Enqueue(ctx, EnqueueImageCacheJobInput{
			TargetType: ImageCacheTargetItem, TargetContentID: contentID, SeriesID: contentID,
			ContentType: "movie", ImageType: ImageCacheImagePoster, SourcePath: source,
		}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		return contentID
	}
	run := func(t *testing.T, roots []string, source string) localArtworkJobRow {
		t.Helper()
		return claimAndProcess(t, enqueue(t, source), roots)
	}
	assertDeferred := func(t *testing.T, got localArtworkJobRow) {
		t.Helper()
		if got.status != ImageCacheStatusQueued || got.attempts != 0 ||
			!strings.HasPrefix(got.lastError, localImageStorageUnavailable) ||
			got.nextAttemptIn < imageCacheRootUnavailableRetry-time.Minute || got.nextAttemptIn > imageCacheRootUnavailableRetry+time.Minute {
			t.Fatalf("job = %+v, want queued again in %s with no attempt counted", got, imageCacheRootUnavailableRetry)
		}
	}
	assertStableFailure := func(t *testing.T, got localArtworkJobRow, wantError string) {
		t.Helper()
		if got.status != ImageCacheStatusQueued || got.attempts != 1 || !strings.Contains(got.lastError, wantError) ||
			got.nextAttemptIn < imageCacheDeferredRetry-time.Hour {
			t.Fatalf("job = %+v, want one attempt counted, %q, and the %s stable-failure delay", got, wantError, imageCacheDeferredRetry)
		}
	}

	t.Run("root absent on this server", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "unmounted")
		source := "file://" + filepath.Join(root, "Film", "poster.jpg") + "?v=12-1700000000"
		contentID := enqueue(t, source)
		assertDeferred(t, claimAndProcess(t, contentID, []string{root}))
		if waiting, err := repo.targetHasDeferredLocalJobs(ctx, contentID); err != nil || !waiting {
			t.Fatalf("targetHasDeferredLocalJobs = %v, %v; want the deferred job reported", waiting, err)
		}

		// Deferred again for the same reason, it waits longer, still without
		// counting an attempt.
		got := claimAndProcess(t, contentID, []string{root})
		if got.status != ImageCacheStatusQueued || got.attempts != 0 ||
			got.nextAttemptIn < imageCacheRootUnavailableRepeatRetry-time.Minute {
			t.Fatalf("repeat deferral = %+v, want queued again in %s with no attempt counted", got, imageCacheRootUnavailableRepeatRetry)
		}

		// This server's discovery copy, which ignores the deferral, cannot
		// push the job further out either.
		if _, err := pool.Exec(ctx, `
			UPDATE metadata_image_cache_jobs SET next_attempt_at = NOW() + interval '20 minutes' WHERE target_content_id = $1
		`, contentID); err != nil {
			t.Fatalf("shorten the wait: %v", err)
		}
		before := 20 * time.Minute
		job, err := repo.claimQueuedLocalJob(ctx, EnqueueImageCacheJobInput{
			TargetType: ImageCacheTargetItem, TargetContentID: contentID, ImageType: ImageCacheImagePoster, SourcePath: source,
		}, "mountless-copy")
		if err != nil || job == nil {
			t.Fatalf("discovery claim of the deferred job = %+v, %v", job, err)
		}
		p, _, _ := newLocalArtworkDBProcessor(pool, []string{root})
		p.processClaimedJobs(ctx, "mountless-copy", []*models.MetadataImageCacheJob{job}, 1, time.Now())
		if got := readLocalArtworkJob(t, pool, ImageCacheTargetItem, contentID, ImageCacheImagePoster); got.status != ImageCacheStatusQueued ||
			got.attempts != 0 || got.nextAttemptIn > before+time.Minute {
			t.Fatalf("after a discovery copy = %+v, want queued with its wait (%s) kept", got, before)
		}

		// A manual refresh starts the deferral over, so refreshing on this
		// server does not push the job to the long delay; also when the
		// deferral had already run out.
		if err := repo.retryTargetNow(ctx, contentID); err != nil {
			t.Fatalf("retryTargetNow: %v", err)
		}
		assertDeferred(t, claimAndProcess(t, contentID, []string{root}))
		if _, err := pool.Exec(ctx, `
			UPDATE metadata_image_cache_jobs SET next_attempt_at = NOW() - interval '1 second' WHERE target_content_id = $1
		`, contentID); err != nil {
			t.Fatalf("let the deferral run out: %v", err)
		}
		if err := repo.retryTargetNow(ctx, contentID); err != nil {
			t.Fatalf("retryTargetNow: %v", err)
		}
		assertDeferred(t, claimAndProcess(t, contentID, []string{root}))
	})

	t.Run("folder removed from mounted storage", func(t *testing.T) {
		root := t.TempDir()
		writeSidecar(t, filepath.Join(root, "Show", "Season 02", "poster.jpg"))
		assertStableFailure(t, run(t, []string{root}, "file://"+filepath.Join(root, "Show", "Season 01", "poster.jpg")+"?v=12-1700000000"), "local image missing")
	})

	t.Run("share nested in a present root is not mounted", func(t *testing.T) {
		root := t.TempDir()
		writeSidecar(t, filepath.Join(root, "local-disk", "Other", "poster.jpg"))
		if err := os.Mkdir(filepath.Join(root, "nas"), 0o755); err != nil {
			t.Fatalf("mkdir mount point: %v", err)
		}
		assertDeferred(t, run(t, []string{root}, "file://"+filepath.Join(root, "nas", "Film", "poster.jpg")+"?v=12-1700000000"))
	})

	t.Run("root is an empty mount point", func(t *testing.T) {
		root := t.TempDir()
		assertDeferred(t, run(t, []string{root}, "file://"+filepath.Join(root, "Film", "poster.jpg")+"?v=12-1700000000"))
	})

	t.Run("file missing from a mounted folder", func(t *testing.T) {
		root := t.TempDir()
		writeSidecar(t, filepath.Join(root, "Film", "Film.mkv"))
		assertStableFailure(t, run(t, []string{root}, "file://"+filepath.Join(root, "Film", "poster.jpg")+"?v=12-1700000000"), "local image missing")
	})

	t.Run("symlink escaping a present root", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		writeSidecar(t, filepath.Join(outside, "poster.jpg"))
		if err := os.Symlink(outside, filepath.Join(root, "Film")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		assertStableFailure(t, run(t, []string{root}, "file://"+filepath.Join(root, "Film", "poster.jpg")+"?v=12-1700000000"), "outside library roots")

		// A file missing behind a link to an empty folder outside the root
		// fails too: that folder says nothing about the library's storage.
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "Art")); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		assertStableFailure(t, run(t, []string{root}, "file://"+filepath.Join(root, "Art", "poster.jpg")+"?v=12-1700000000"), "local image missing")
	})
}
