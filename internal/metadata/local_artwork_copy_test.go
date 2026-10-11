package metadata

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// localClaimingImageCacheJobs records which claim a drain used.
type localClaimingImageCacheJobs struct {
	fakeLocalImageCacheJobs
	claimCalls      int
	localClaimCalls int
}

func (f *localClaimingImageCacheJobs) ClaimDue(ctx context.Context, workerID string, limit int) ([]*models.MetadataImageCacheJob, error) {
	f.claimCalls++
	return f.fakeLocalImageCacheJobs.ClaimDue(ctx, workerID, limit)
}

func (f *localClaimingImageCacheJobs) ClaimDueLocal(ctx context.Context, workerID string, limit int) ([]*models.MetadataImageCacheJob, error) {
	f.localClaimCalls++
	claimed := f.claimed
	f.claimed = nil
	return claimed, nil
}

// With metadata.cache_images off, draining still copies local artwork: it
// claims local jobs only, and the provider-artwork backfill stays refused.
func TestImageCacheProcessorDrainsLocalArtworkWithCachingOff(t *testing.T) {
	root := t.TempDir()
	job := localArtworkJob("file://" + writeLocalPoster(t, root))
	jobs := &localClaimingImageCacheJobs{fakeLocalImageCacheJobs: fakeLocalImageCacheJobs{
		fakeImageCacheJobs: fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{job}},
	}}
	items := &fakeItemArtworkUpdater{updated: true}
	p := NewImageCacheProcessorWithTargets(jobs, &fakeByteImageCacher{}, nil, ImageCacheProcessorTargets{Items: items})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{root}})
	p.SetEnabled(false)

	stats, err := p.DrainUntilIdle(context.Background(), "w1", 10, 1, 0, nil)
	if err != nil {
		t.Fatalf("DrainUntilIdle: %v", err)
	}
	if jobs.claimCalls != 0 || jobs.localClaimCalls == 0 {
		t.Fatalf("claims all=%d local=%d, want local claims only", jobs.claimCalls, jobs.localClaimCalls)
	}
	if stats.Succeeded != 1 || jobs.succeededID != job.ID || items.cachedPath == "" {
		t.Fatalf("stats=%+v succeeded=%d cached=%q, want the local poster copied", stats, jobs.succeededID, items.cachedPath)
	}
	if !p.ArtworkCachingEnabled() {
		t.Fatal("ArtworkCachingEnabled = false, want true: local artwork is cached with caching off")
	}
	if _, err := p.RunUntilIdle(context.Background(), "w1", 10, 1, 0, nil); !errors.Is(err, ErrImageCachingDisabled) {
		t.Fatalf("backfill error = %v, want ErrImageCachingDisabled", err)
	}
}

// Disabling caching while a claimed page is in flight stops remote jobs from
// starting but lets local jobs finish.
func TestImageCacheProcessorKeepsLocalJobsWhenCachingTurnsOff(t *testing.T) {
	root := t.TempDir()
	local := localArtworkJob("file://" + writeLocalPoster(t, root))
	remote := &models.MetadataImageCacheJob{
		ID: 8, TargetType: ImageCacheTargetItem, TargetContentID: "movie-2", SourcePath: "https://img.example/p.jpg",
		ProviderID: "tmdb", ProviderContentID: "2", ContentType: "movies", ImageType: ImageCacheImagePoster,
	}
	jobs := &fakeLocalImageCacheJobs{fakeImageCacheJobs: fakeImageCacheJobs{}}
	p := NewImageCacheProcessorWithTargets(jobs, &fakeByteImageCacher{}, nil, ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{root}})
	p.SetEnabled(false)

	stats := p.processClaimedJobs(context.Background(), "w1", []*models.MetadataImageCacheJob{remote, local}, 1)
	if stats.Succeeded != 1 || jobs.succeededID != local.ID {
		t.Fatalf("stats=%+v succeeded=%d, want the local job processed", stats, jobs.succeededID)
	}
	if len(jobs.requeuedIDs) != 1 || jobs.requeuedIDs[0] != remote.ID {
		t.Fatalf("requeued = %v, want the remote job %d", jobs.requeuedIDs, remote.ID)
	}
}

// A manual refresh with caching off still waits for its local artwork, and
// asks the queue for local jobs only.
func TestCacheTargetArtworkHandlesLocalOnlyWithCachingOff(t *testing.T) {
	jobs := &targetImageCacheJobs{localOnly: true}
	p := NewImageCacheProcessor(jobs, &fakeImageCacher{}, &fakeImageResolver{}, nil, nil)
	p.SetEnabled(false)
	if err := p.CacheTargetArtwork(context.Background(), "movie-1"); err != nil {
		t.Fatalf("CacheTargetArtwork: %v", err)
	}
	if jobs.retryCalls != 1 || jobs.targetCalls == 0 || !jobs.localOnly {
		t.Fatalf("retry=%d claims=%d localOnly=%v, want one local-only retry and claim", jobs.retryCalls, jobs.targetCalls, jobs.localOnly)
	}
}

// deferringImageCacheJobs records deferrals.
type deferringImageCacheJobs struct {
	targetImageCacheJobs
	deferredID   int64
	deferredText string
}

func (f *deferringImageCacheJobs) DeferClaimed(_ context.Context, id int64, _ string, errText string, _, _ time.Duration) error {
	f.deferredID = id
	f.deferredText = errText
	return nil
}

// A manual refresh on a server without the library mount reports the artwork
// as still pending rather than complete: the job waits for a server with the
// mount.
func TestCacheTargetArtworkReportsDeferredLocalArtworkPending(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "unmounted")
	job := localArtworkJob("file://" + filepath.Join(missingRoot, "Film", "poster.jpg"))
	jobs := &deferringImageCacheJobs{targetImageCacheJobs: targetImageCacheJobs{
		targetClaims: [][]*models.MetadataImageCacheJob{{job}},
	}}
	p := NewImageCacheProcessorWithTargets(jobs, &fakeByteImageCacher{}, nil, ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{missingRoot}})

	err := p.CacheTargetArtwork(context.Background(), "movie-1")
	if !errors.Is(err, ErrTargetArtworkPending) {
		t.Fatalf("CacheTargetArtwork error = %v, want ErrTargetArtworkPending", err)
	}
	if jobs.deferredID != job.ID || jobs.failedID != 0 {
		t.Fatalf("deferred=%d failed=%d (%q), want the job deferred, not failed", jobs.deferredID, jobs.failedID, jobs.failedText)
	}
}

// A local poster outranks remote ones wherever it lands in chain order; the
// first remote poster it displaces or outranks is kept as the stand-in.
func TestSeasonAndEpisodeResultsKeepDisplacedRemoteArtwork(t *testing.T) {
	const local = "file:///media/Show/Season 01/poster.jpg?v=1-1"
	for _, tc := range []struct {
		name  string
		order []string
	}{
		{name: "local first", order: []string{local, "tmdb://season-1.jpg", "tvdb://season-1.jpg"}},
		{name: "local after remote", order: []string{"tmdb://season-1.jpg", local, "tvdb://season-1.jpg"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seasons := map[int]*SeasonResult{}
			episodes := map[episodeResultKey]*EpisodeResult{}
			for _, path := range tc.order {
				accumulateSeasonResults(seasons, []SeasonResult{{SeasonNumber: 1, PosterPath: path}})
				accumulateEpisodeResults(episodes, []EpisodeResult{{SeasonNumber: 1, EpisodeNumber: 1, StillPath: path}})
			}
			season := seasons[1]
			if season.PosterPath != local || season.remotePosterPath != "tmdb://season-1.jpg" {
				t.Fatalf("season poster=%q remote=%q, want local with the tmdb stand-in", season.PosterPath, season.remotePosterPath)
			}
			episode := episodes[episodeResultKey{seasonNumber: 1, episodeNumber: 1}]
			if episode.StillPath != local || episode.stillFallback != "tmdb://season-1.jpg" {
				t.Fatalf("episode still=%q fallback=%q, want local with the tmdb stand-in", episode.StillPath, episode.stillFallback)
			}
		})
	}
}

func TestApplyLocalArtworkFallback(t *testing.T) {
	const localPoster = "file:///media/Film/poster.jpg?v=1-1"
	images := []RemoteImage{
		{URL: localPoster, Type: ImagePoster},
		{ProviderID: "tmdb", URL: "tmdb://poster.jpg", Type: ImagePoster, Language: "en", Rating: 7},
		{ProviderID: "tmdb", URL: "tmdb://backdrop.jpg", Type: ImageBackdrop, Rating: 7},
	}

	t.Run("empty slot waiting on its local copy", func(t *testing.T) {
		item := &models.MediaItem{PosterSourcePath: localPoster, BackdropPath: "tmdb/movie/1/backdrop.webp", BackdropSourcePath: "tmdb://backdrop.jpg"}
		applyLocalArtworkFallback(item, images, "en")
		if item.PosterPath != "tmdb://poster.jpg" || item.PosterSourcePath != localPoster {
			t.Fatalf("poster = %q from %q, want the remote stand-in with the local source kept", item.PosterPath, item.PosterSourcePath)
		}
		if item.BackdropPath != "tmdb/movie/1/backdrop.webp" {
			t.Fatalf("backdrop = %q, want the remote slot untouched", item.BackdropPath)
		}
	})

	t.Run("cached copy already showing", func(t *testing.T) {
		item := &models.MediaItem{PosterPath: "local/movie/1/abcd1234/poster/w500.webp", PosterSourcePath: localPoster}
		applyLocalArtworkFallback(item, images, "en")
		if item.PosterPath != "local/movie/1/abcd1234/poster/w500.webp" {
			t.Fatalf("poster = %q, want the cached copy kept", item.PosterPath)
		}
	})

	t.Run("locked artwork", func(t *testing.T) {
		item := &models.MediaItem{PosterSourcePath: localPoster, LockedFields: []int{int(FieldImages)}}
		applyLocalArtworkFallback(item, images, "en")
		if item.PosterPath != "" {
			t.Fatalf("poster = %q, want no stand-in under an artwork lock", item.PosterPath)
		}
	})
}

// failingLocalArtworkCopier fails every hand-off.
type failingLocalArtworkCopier struct{}

func (failingLocalArtworkCopier) CopyLocalArtwork(context.Context, []EnqueueImageCacheJobInput) error {
	return errNoLocalArtworkQueue
}

// When the copier cannot take the artwork, the refresh queues it instead, so
// any server's drain still copies it; with caching off nothing remote is
// queued alongside it.
func TestLocalArtworkFallsBackToQueueWhenCopierFails(t *testing.T) {
	jobs := &capturingImageJobEnqueuer{}
	s := &MetadataService{imageCacheJobs: jobs}
	s.SetLocalArtworkCopier(failingLocalArtworkCopier{})
	s.enqueueImageCacheJobs(context.Background(), "item", "movie-1", []EnqueueImageCacheJobInput{
		{TargetType: ImageCacheTargetItem, TargetContentID: "movie-1", ImageType: ImageCacheImagePoster, SourcePath: "file:///media/Film/poster.jpg"},
		{TargetType: ImageCacheTargetItem, TargetContentID: "movie-1", ImageType: ImageCacheImageBackdrop, SourcePath: "https://img.example/b.jpg"},
	})
	if len(jobs.inputs) != 1 || jobs.inputs[0].ImageType != ImageCacheImagePoster {
		t.Fatalf("queued = %#v, want only the local poster", jobs.inputs)
	}
}

// A manual refresh does not report its artwork done while another worker has
// left one of the target's local jobs for a server with the storage mounted.
func TestCacheTargetArtworkReportsLocalArtworkDeferredElsewhere(t *testing.T) {
	jobs := &targetImageCacheJobs{deferredElsewhere: true}
	p := NewImageCacheProcessor(jobs, &fakeImageCacher{}, &fakeImageResolver{}, nil, nil)
	if err := p.CacheTargetArtwork(context.Background(), "movie-1"); !errors.Is(err, ErrTargetArtworkPending) {
		t.Fatalf("CacheTargetArtwork error = %v, want ErrTargetArtworkPending", err)
	}
}

// recordingLocalArtworkStore records the lease token of every discovery claim.
type recordingLocalArtworkStore struct {
	tokens []string
}

func (s *recordingLocalArtworkStore) enqueueHeldLocalJobs(_ context.Context, inputs []EnqueueImageCacheJobInput) ([]EnqueueImageCacheJobInput, error) {
	return inputs, nil
}

func (s *recordingLocalArtworkStore) claimQueuedLocalJob(_ context.Context, _ EnqueueImageCacheJobInput, workerID string) (*models.MetadataImageCacheJob, error) {
	s.tokens = append(s.tokens, workerID)
	return nil, nil
}

// Each discovery claim gets its own lease token: when a refresh replaces a
// slot's source while an older copy still runs, the older copy's terminal
// write must not match the newer claim of the same job row.
func TestDiscoveryClaimsUseDistinctLeaseTokens(t *testing.T) {
	store := &recordingLocalArtworkStore{}
	p := NewImageCacheProcessor(&fakeImageCacheJobs{}, &fakeImageCacher{}, nil, nil, nil)
	in := EnqueueImageCacheJobInput{TargetType: ImageCacheTargetItem, TargetContentID: "movie-1", ImageType: ImageCacheImagePoster, SourcePath: "file:///media/Film/poster.jpg"}
	p.copyDiscoveredLocalArtwork(store, in)
	p.copyDiscoveredLocalArtwork(store, in)
	if len(store.tokens) != 2 || store.tokens[0] == store.tokens[1] {
		t.Fatalf("claim tokens = %q, want two distinct tokens", store.tokens)
	}
}

// The artwork budget bounds jobs across callers: with one slot, a second job
// waits until the first releases it.
func TestImageWorkBudgetBoundsConcurrentJobs(t *testing.T) {
	var budget imageWorkBudget
	release, err := budget.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire without a limit: %v", err)
	}
	release()

	budget.set(1)
	release, err = budget.acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := budget.acquire(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire with the slot taken = %v, want it to wait", err)
	}
	release()
	second, err := budget.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	second()
}

// countingByteImageCacher records how many byte caches run at once.
type countingByteImageCacher struct {
	fakeImageCacher
	mu         sync.Mutex
	running    int
	maxRunning int
}

func (c *countingByteImageCacher) CacheImageBytes(_ context.Context, _ []byte, req CacheImageRequest) (*CacheImageResult, error) {
	c.mu.Lock()
	c.running++
	c.maxRunning = max(c.maxRunning, c.running)
	c.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	c.mu.Lock()
	c.running--
	c.mu.Unlock()
	return &CacheImageResult{BasePath: "local/movies/" + req.ContentID + "/poster", Ext: ".webp"}, nil
}

// A drain asking for more concurrency than the process budget still runs
// within the budget.
func TestImageCacheProcessorHonorsWorkerBudget(t *testing.T) {
	root := t.TempDir()
	source := "file://" + writeLocalPoster(t, root)
	var claimed []*models.MetadataImageCacheJob
	for i := int64(1); i <= 4; i++ {
		job := localArtworkJob(source)
		job.ID = i
		claimed = append(claimed, job)
	}
	cacher := &countingByteImageCacher{}
	p := NewImageCacheProcessorWithTargets(&fakeLocalImageCacheJobs{}, cacher, nil, ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{root}})
	p.SetWorkerBudget(1)
	p.processClaimedJobs(context.Background(), "w1", claimed, 4)
	if cacher.maxRunning != 1 {
		t.Fatalf("concurrent jobs = %d, want 1 within the budget", cacher.maxRunning)
	}
}
