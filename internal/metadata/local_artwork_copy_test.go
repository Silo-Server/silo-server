package metadata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
)

// claimCountingImageCacheJobs counts drain claims.
type claimCountingImageCacheJobs struct {
	fakeLocalImageCacheJobs
	claimCalls int
}

func (f *claimCountingImageCacheJobs) ClaimDue(ctx context.Context, workerID string, limit int) ([]*models.MetadataImageCacheJob, error) {
	f.claimCalls++
	return f.fakeLocalImageCacheJobs.ClaimDue(ctx, workerID, limit)
}

// With metadata.cache_images off, draining caches nothing, local artwork
// included: it claims no jobs, and the provider-artwork backfill stays
// refused.
func TestImageCacheProcessorDrainsNothingWithCachingOff(t *testing.T) {
	root := t.TempDir()
	job := localArtworkJob("file://" + writeLocalPoster(t, root))
	jobs := &claimCountingImageCacheJobs{fakeLocalImageCacheJobs: fakeLocalImageCacheJobs{
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
	if jobs.claimCalls != 0 || stats.Claimed != 0 || items.cachedPath != "" {
		t.Fatalf("claims=%d stats=%+v cached=%q, want nothing claimed or copied", jobs.claimCalls, stats, items.cachedPath)
	}
	if p.ArtworkCachingEnabled() {
		t.Fatal("ArtworkCachingEnabled = true, want false with caching off")
	}
	if _, err := p.RunUntilIdle(context.Background(), "w1", 10, 1, 0, nil); !errors.Is(err, ErrImageCachingDisabled) {
		t.Fatalf("backfill error = %v, want ErrImageCachingDisabled", err)
	}
}

// Disabling caching while a claimed page is in flight stops every job from
// starting, local ones too; they go back to the queue.
func TestImageCacheProcessorRequeuesClaimedJobsWhenCachingTurnsOff(t *testing.T) {
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

	stats := p.processClaimedJobs(context.Background(), "w1", []*models.MetadataImageCacheJob{remote, local}, 1, time.Now())
	if stats.Succeeded != 0 || jobs.succeededID != 0 {
		t.Fatalf("stats=%+v succeeded=%d, want nothing processed", stats, jobs.succeededID)
	}
	if !slices.Equal(jobs.requeuedIDs, []int64{remote.ID, local.ID}) {
		t.Fatalf("requeued = %v, want both jobs %d and %d", jobs.requeuedIDs, remote.ID, local.ID)
	}
}

// A manual refresh with caching off caches nothing and does not wait.
func TestCacheTargetArtworkDoesNothingWithCachingOff(t *testing.T) {
	jobs := &targetImageCacheJobs{}
	p := NewImageCacheProcessor(jobs, &fakeImageCacher{}, &fakeImageResolver{}, nil, nil)
	p.SetEnabled(false)
	if err := p.CacheTargetArtwork(context.Background(), "movie-1"); err != nil {
		t.Fatalf("CacheTargetArtwork: %v", err)
	}
	if jobs.retryCalls != 0 || jobs.targetCalls != 0 {
		t.Fatalf("retry=%d claims=%d, want no queue access", jobs.retryCalls, jobs.targetCalls)
	}
}

// deferringImageCacheJobs records deferrals. A deferred job waits in the
// queue, as targetHasDeferredLocalJobs then reports, unless copiedElsewhere
// says a server with the storage mounted has copied it since.
type deferringImageCacheJobs struct {
	targetImageCacheJobs
	deferredID      int64
	deferredText    string
	copiedElsewhere bool
}

func (f *deferringImageCacheJobs) DeferClaimed(_ context.Context, id int64, _ string, errText string, _, _ time.Duration) error {
	f.deferredID = id
	f.deferredText = errText
	f.deferredElsewhere = !f.copiedElsewhere
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

// A job this refresh deferred for missing storage, then copied by a server
// with the mount before the refresh ended, leaves nothing pending.
func TestCacheTargetArtworkNotPendingOnceADeferredJobIsCopied(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "unmounted")
	job := localArtworkJob("file://" + filepath.Join(missingRoot, "Film", "poster.jpg"))
	jobs := &deferringImageCacheJobs{copiedElsewhere: true, targetImageCacheJobs: targetImageCacheJobs{
		targetClaims: [][]*models.MetadataImageCacheJob{{job}},
	}}
	p := NewImageCacheProcessorWithTargets(jobs, &fakeByteImageCacher{}, nil, ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{missingRoot}})

	if err := p.CacheTargetArtwork(context.Background(), "movie-1"); err != nil {
		t.Fatalf("CacheTargetArtwork error = %v, want done once the deferred job was copied", err)
	}
	if jobs.deferredID != job.ID {
		t.Fatalf("deferred = %d, want the job deferred by this refresh", jobs.deferredID)
	}
}

// A pending image URL an NFO names is not kept with caching off: only the
// image cache could fetch it, so the slot takes the provider poster. With
// caching on it is kept while the NFO is unreadable.
func TestPendingNFONamedURLNotKeptWithCachingOff(t *testing.T) {
	const (
		contentID    = "movie:tmdb:7"
		nfoPoster    = "nfo+https://img.example.org/poster.jpg"
		remotePoster = "tmdb://remote-poster.jpg"
	)
	for _, tc := range []struct {
		name    string
		caching bool
		want    string
	}{
		{name: "caching off", want: remotePoster},
		{name: "caching on", caching: true, want: nfoPoster},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHarness()
			h.service.SetImageCacheJobEnqueuer(&recordingImageCacheJobEnqueuer{})
			h.service.SetAutoCacheImages(tc.caching)
			// A refresh searches local artwork only while it is usable.
			ctx := context.Background()
			if tc.caching {
				ctx = withLocalMetadataUnreadable(withLocalArtworkSearched(ctx, localArtworkItem), localArtworkItem)
			}
			if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
				ContentID: contentID, Type: "movie", Title: "Film", Status: "matched", TmdbID: "7",
				PosterSourcePath: nfoPoster,
				Studios:          []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
			}); err != nil {
				t.Fatalf("upsert existing item: %v", err)
			}
			providerRepo := newFakeProviderIDRepo()
			providerRepo.set(contentID, &models.MediaItemProviderID{ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "7"})
			h.service.providerIDRepo = providerRepo

			if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: contentID, Mode: ModeScheduledRefresh, Language: "en"},
				&MetadataResult{HasMetadata: true, Title: "Film", ProviderIDs: map[string]string{"tmdb": "7"}},
				[]RemoteImage{{Type: ImagePoster, URL: remotePoster, ProviderID: "tmdb", Rating: 7.5, Language: "en"}},
				nil, nil, "movie"); err != nil {
				t.Fatalf("mergeAndPersist: %v", err)
			}
			got, err := h.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if got.PosterSourcePath != tc.want {
				t.Fatalf("poster = path %q source %q, want source %q", got.PosterPath, got.PosterSourcePath, tc.want)
			}
		})
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
// any server's drain still copies it.
func TestLocalArtworkFallsBackToQueueWhenCopierFails(t *testing.T) {
	jobs := &capturingImageJobEnqueuer{}
	s := &MetadataService{imageCacheJobs: jobs}
	s.SetAutoCacheImages(true)
	s.SetLocalArtworkCopier(failingLocalArtworkCopier{})
	if err := s.enqueueImageCacheJobs(context.Background(), "item", "movie-1", []EnqueueImageCacheJobInput{
		{TargetType: ImageCacheTargetItem, TargetContentID: "movie-1", ImageType: ImageCacheImagePoster, SourcePath: "file:///media/Film/poster.jpg"},
		{TargetType: ImageCacheTargetItem, TargetContentID: "movie-1", ImageType: ImageCacheImageBackdrop, SourcePath: "https://img.example/b.jpg"},
	}); err != nil {
		t.Fatalf("enqueueImageCacheJobs: %v", err)
	}
	if len(jobs.inputs) != 2 || jobs.inputs[0].ImageType != ImageCacheImagePoster {
		t.Fatalf("queued = %#v, want the local poster and the remote backdrop", jobs.inputs)
	}
}

// Only sidecars this server can see go to its copier. A local source a
// refresh kept without seeing it (no mount here) is queued the usual way, so
// it neither takes a discovery hold nor gets claimed by a copy that can only
// defer it.
func TestOnlyVisibleLocalArtworkGoesToTheCopier(t *testing.T) {
	dir := t.TempDir()
	writeTestBytes(t, filepath.Join(dir, "poster.jpg"))
	seen := "file://" + filepath.Join(dir, "poster.jpg") + "?v=5-1"
	unseen := "file://" + filepath.Join(dir, "missing-mount", "fanart.jpg") + "?v=5-1"
	jobs := &capturingImageJobEnqueuer{}
	copier := &recordingLocalArtworkCopier{}
	s := &MetadataService{imageCacheJobs: jobs}
	s.SetAutoCacheImages(true)
	s.SetLocalArtworkCopier(copier)
	if err := s.enqueueImageCacheJobs(context.Background(), "item", "movie-1", []EnqueueImageCacheJobInput{
		{TargetType: ImageCacheTargetItem, TargetContentID: "movie-1", ImageType: ImageCacheImagePoster, SourcePath: seen},
		{TargetType: ImageCacheTargetItem, TargetContentID: "movie-1", ImageType: ImageCacheImageBackdrop, SourcePath: unseen},
	}); err != nil {
		t.Fatalf("enqueueImageCacheJobs: %v", err)
	}
	if len(copier.inputs) != 1 || copier.inputs[0].SourcePath != seen {
		t.Fatalf("copier got %#v, want only the poster this server can see", copier.inputs)
	}
	if len(jobs.inputs) != 1 || jobs.inputs[0].SourcePath != unseen {
		t.Fatalf("queued %#v, want only the unseen backdrop", jobs.inputs)
	}
}

// A folder that does not answer in time makes its sidecars unseen, without
// looking the files up; one that denies listing still has its files looked
// up. Each folder is probed once, under one deadline for the whole batch.
func TestVisibleLocalSidecarsProbesEachFolderOnce(t *testing.T) {
	answering, unlisted := t.TempDir(), t.TempDir()
	writeTestBytes(t, filepath.Join(answering, "poster.jpg"))
	writeTestBytes(t, filepath.Join(unlisted, "poster.jpg"))
	stalled := filepath.Join(t.TempDir(), "stalled-share")
	src := func(dir, name string) EnqueueImageCacheJobInput {
		return EnqueueImageCacheJobInput{SourcePath: "file://" + filepath.Join(dir, name) + "?v=1-1"}
	}
	var probed []string
	probe := func(ctx context.Context, paths []string, _ time.Duration) []rootcheck.Result {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > rootcheck.DefaultProbeTimeout {
			t.Errorf("probe deadline = %v, %v; want one batch deadline within %s", deadline, ok, rootcheck.DefaultProbeTimeout)
		}
		probed = append(probed, paths...)
		results := make([]rootcheck.Result, len(paths))
		for i, p := range paths {
			switch p {
			case answering:
				results[i] = rootcheck.Result{Reachable: true}
			case unlisted:
				results[i] = rootcheck.Result{ErrorCode: rootcheck.ErrCodePermissionDenied}
			default:
				results[i] = rootcheck.Result{ErrorCode: rootcheck.ErrCodeTimeout}
			}
		}
		return results
	}
	got := visibleLocalSidecars(context.Background(), []EnqueueImageCacheJobInput{
		src(answering, "poster.jpg"), src(answering, "fanart.jpg"), src(stalled, "poster.jpg"), src(stalled, "fanart.jpg"),
		src(unlisted, "poster.jpg"),
	}, probe)
	if want := []bool{true, false, false, false, true}; !slices.Equal(got, want) {
		t.Fatalf("visible = %v, want %v", got, want)
	}
	if len(probed) != 3 {
		t.Fatalf("probed %v, want each folder once", probed)
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
	// With the slot taken, an acquire on a done context fails at once.
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := budget.acquire(done); !errors.Is(err, context.Canceled) {
		t.Fatalf("second acquire with the slot taken = %v, want it to wait", err)
	}
	release()
	second, err := budget.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	second()
}

// A drain whose budget is taken starts no job, and hands its whole claimed
// page back to the queue when it is canceled while waiting.
func TestImageCacheProcessorWaitsForWorkerBudget(t *testing.T) {
	root := t.TempDir()
	source := "file://" + writeLocalPoster(t, root)
	var claimed []*models.MetadataImageCacheJob
	for i := int64(1); i <= 3; i++ {
		job := localArtworkJob(source)
		job.ID = i
		claimed = append(claimed, job)
	}
	jobs := &fakeLocalImageCacheJobs{}
	cacher := &fakeByteImageCacher{}
	p := NewImageCacheProcessorWithTargets(jobs, cacher, nil, ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{root}})
	p.SetWorkerBudget(1)
	release, err := p.workBudget.acquire(context.Background())
	if err != nil {
		t.Fatalf("take the only slot: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stats := p.processClaimedJobs(ctx, "w1", claimed, 3, time.Now())
	if len(cacher.byteReqs) != 0 || stats.Succeeded != 0 {
		t.Fatalf("copies = %d, stats = %+v; want none while the budget is taken", len(cacher.byteReqs), stats)
	}
	if len(jobs.requeuedIDs) != 3 {
		t.Fatalf("requeued = %v, want the whole page back in the queue", jobs.requeuedIDs)
	}
}

// A manual refresh's jobs hold slots of the artwork budget the drain and
// discovery copies share, so metadata.image_workers bounds them too.
func TestCacheTargetArtworkHoldsWorkerBudget(t *testing.T) {
	root := t.TempDir()
	job := localArtworkJob("file://" + writeLocalPoster(t, root))
	jobs := &targetImageCacheJobs{targetClaims: [][]*models.MetadataImageCacheJob{{job}}}
	cacher := &gatedByteImageCacher{entered: make(chan struct{}), proceed: make(chan struct{})}
	p := NewImageCacheProcessorWithTargets(jobs, cacher, nil, ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{root}})
	p.SetWorkerBudget(1)

	done := make(chan error)
	go func() { done <- p.CacheTargetArtwork(context.Background(), "movie-1") }()
	<-cacher.entered
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.workBudget.acquire(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("budget while the refresh's job runs: acquire = %v, want the only slot held", err)
	}
	close(cacher.proceed)
	if err := <-done; err != nil {
		t.Fatalf("CacheTargetArtwork: %v", err)
	}
}

// Claimed jobs that can no longer start in time to finish inside their lease
// go back to the queue instead of waiting for a worker or the budget, so no
// other server recovers them as abandoned while they wait.
func TestClaimedJobsPastTheirStartDeadlineGoBackToTheQueue(t *testing.T) {
	claimStarted := time.Now()
	if got, want := latestImageCacheJobStart(claimStarted).Sub(claimStarted), ImageCacheLeaseDuration-ImageCacheJobTimeout; got != want {
		t.Fatalf("start window = %s, want %s from the claim's start", got, want)
	}

	root := t.TempDir()
	source := "file://" + writeLocalPoster(t, root)
	var claimed []*models.MetadataImageCacheJob
	for i := int64(1); i <= 3; i++ {
		job := localArtworkJob(source)
		job.ID = i
		claimed = append(claimed, job)
	}
	jobs := &fakeLocalImageCacheJobs{}
	cacher := &fakeByteImageCacher{}
	p := NewImageCacheProcessorWithTargets(jobs, cacher, nil, ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{root}})

	// A claim that began a full start window ago: the window ran out while
	// the claim and the page waited.
	stats := p.processClaimedJobs(context.Background(), "w1", claimed, 3,
		time.Now().Add(-(ImageCacheLeaseDuration-ImageCacheJobTimeout)-time.Second))
	if len(cacher.byteReqs) != 0 || stats.Succeeded != 0 {
		t.Fatalf("copies = %d, stats = %+v; want none past the start deadline", len(cacher.byteReqs), stats)
	}
	if len(jobs.requeuedIDs) != 3 {
		t.Fatalf("requeued = %v, want the whole page back in the queue", jobs.requeuedIDs)
	}
}

// gatedByteImageCacher signals each copy as it starts and holds it until the
// test lets it finish.
type gatedByteImageCacher struct {
	fakeImageCacher
	entered chan struct{}
	proceed chan struct{}
}

func (c *gatedByteImageCacher) CacheImageBytes(_ context.Context, _ []byte, req CacheImageRequest) (*CacheImageResult, error) {
	c.entered <- struct{}{}
	<-c.proceed
	return &CacheImageResult{BasePath: "local/movies/" + req.ContentID + "/poster", Ext: ".webp"}, nil
}

// A drain job holds a slot of the process's artwork budget while it runs.
func TestImageCacheProcessorJobsHoldWorkerBudget(t *testing.T) {
	root := t.TempDir()
	source := "file://" + writeLocalPoster(t, root)
	first, second := localArtworkJob(source), localArtworkJob(source)
	second.ID = first.ID + 1
	cacher := &gatedByteImageCacher{entered: make(chan struct{}), proceed: make(chan struct{})}
	p := NewImageCacheProcessorWithTargets(&fakeLocalImageCacheJobs{}, cacher, nil, ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})
	p.SetLibraryRootResolver(&fakeLibraryRootResolver{roots: []string{root}})
	p.SetWorkerBudget(1)

	done := make(chan ImageCacheRunStats)
	go func() {
		done <- p.processClaimedJobs(context.Background(), "w1", []*models.MetadataImageCacheJob{first, second}, 2, time.Now())
	}()
	<-cacher.entered
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.workBudget.acquire(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("budget while a job runs: acquire = %v, want the only slot held", err)
	}
	close(cacher.proceed)
	<-cacher.entered
	if stats := <-done; stats.Succeeded != 2 {
		t.Fatalf("stats = %+v, want both jobs copied", stats)
	}
}

// Lowering the budget holds back callers already waiting: none starts until
// the jobs running before the change have dropped below the new limit.
func TestImageWorkBudgetLowersForWaitingCallers(t *testing.T) {
	var budget imageWorkBudget
	budget.set(2)
	first, _ := budget.acquire(context.Background())
	second, _ := budget.acquire(context.Background())
	waiting := make(chan func())
	go func() {
		release, err := budget.acquire(context.Background())
		if err != nil {
			t.Errorf("waiting acquire: %v", err)
		}
		waiting <- release
	}()
	budget.set(1)
	full := func(what string) {
		t.Helper()
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := budget.acquire(canceled); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: acquire = %v, want the budget full", what, err)
		}
	}
	first()
	full("one job still running under a limit of one")
	second()
	release := <-waiting
	full("the waiting caller took the only slot")
	release()
}

// A folder that exists but denies listing shows the storage is mounted, so a
// file under it is the file's failure, not a missing mount.
func TestUnreadableFolderCountsAsMountedStorage(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "Film")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	if !nearestExistingFolderListsEntries(context.Background(), locked, []string{root}) {
		t.Fatal("unreadable folder read as missing storage, want mounted")
	}
	if nearestExistingFolderListsEntries(context.Background(), filepath.Join(root, "Gone"), []string{filepath.Join(root, "Gone")}) {
		t.Fatal("missing root read as mounted storage")
	}
}

// A freed slot goes to the caller that was already waiting, not back to the
// one that released it, so a busy copy worker cannot starve a refresh.
func TestImageWorkBudgetHandsFreedSlotsToWaitingCallers(t *testing.T) {
	var budget imageWorkBudget
	budget.set(1)
	worker, _ := budget.acquire(context.Background())
	waiting := make(chan func())
	go func() {
		release, err := budget.acquire(context.Background())
		if err != nil {
			t.Errorf("waiting acquire: %v", err)
		}
		waiting <- release
	}()
	waitForBudgetWaiters(&budget, 1)
	worker()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := budget.acquire(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("releasing caller retook the slot: acquire = %v", err)
	}
	(<-waiting)()
}

// waitForBudgetWaiters returns once n callers are queued for budget slots.
func waitForBudgetWaiters(budget *imageWorkBudget, n int) {
	for {
		budget.mu.Lock()
		queued := len(budget.waiting)
		budget.mu.Unlock()
		if queued == n {
			return
		}
		runtime.Gosched()
	}
}

// A manual refresh waits for a budget slot only behind other manual
// refreshes, not behind queued background work.
func TestImageWorkBudgetServesInteractiveCallersFirst(t *testing.T) {
	var budget imageWorkBudget
	budget.set(1)
	held, _ := budget.acquire(context.Background())
	started := make(chan string, 2)
	wait := func(name string, interactive bool) {
		go func() {
			release, err := budget.acquireFor(context.Background(), interactive)
			if err != nil {
				t.Errorf("%s acquire: %v", name, err)
				started <- ""
				return
			}
			started <- name
			release()
		}()
	}
	wait("background", false)
	waitForBudgetWaiters(&budget, 1)
	wait("refresh", true)
	waitForBudgetWaiters(&budget, 2)
	held()
	if first := <-started; first != "refresh" {
		t.Fatalf("first served = %q, want the refresh", first)
	}
	<-started
}

// A canceled manual refresh reports the cancellation, not pending artwork.
func TestCacheTargetArtworkReturnsCancellation(t *testing.T) {
	page := []*models.MetadataImageCacheJob{localArtworkJob("file:///media/Film/poster.jpg?v=1-1")}
	jobs := &targetImageCacheJobs{targetClaims: [][]*models.MetadataImageCacheJob{page}}
	p := NewImageCacheProcessor(jobs, &fakeImageCacher{}, &fakeImageResolver{}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.CacheTargetArtwork(ctx, "movie-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CacheTargetArtwork error = %v, want context.Canceled", err)
	}
}

// disablingTargetImageCacheJobs runs afterClaim once a target page is claimed.
type disablingTargetImageCacheJobs struct {
	targetImageCacheJobs
	afterClaim func()
}

func (f *disablingTargetImageCacheJobs) claimDueForTarget(ctx context.Context, workerID, targetContentID string, limit int) ([]*models.MetadataImageCacheJob, error) {
	jobs, err := f.targetImageCacheJobs.claimDueForTarget(ctx, workerID, targetContentID, limit)
	f.afterClaim()
	return jobs, err
}

// A manual refresh whose claimed page makes no progress leaves it to the
// background drain and reports the artwork as pending, rather than claiming
// the same jobs again.
func TestCacheTargetArtworkStopsWhenAPageMakesNoProgress(t *testing.T) {
	remote := &models.MetadataImageCacheJob{
		ID: 9, TargetType: ImageCacheTargetItem, TargetContentID: "movie-1", SourcePath: "https://img.example/p.jpg",
		ProviderID: "tmdb", ProviderContentID: "1", ContentType: "movies", ImageType: ImageCacheImagePoster,
	}
	page := []*models.MetadataImageCacheJob{remote}
	jobs := &disablingTargetImageCacheJobs{targetImageCacheJobs: targetImageCacheJobs{targetClaims: [][]*models.MetadataImageCacheJob{page, page, page}}}
	p := NewImageCacheProcessor(jobs, &fakeImageCacher{}, &fakeImageResolver{}, nil, nil)
	// Caching goes off once the page is claimed, so no job starts and the
	// page goes back.
	jobs.afterClaim = func() { p.SetEnabled(false) }
	if err := p.CacheTargetArtwork(context.Background(), "movie-1"); !errors.Is(err, ErrTargetArtworkPending) {
		t.Fatalf("CacheTargetArtwork error = %v, want ErrTargetArtworkPending", err)
	}
	if jobs.targetCalls != 1 {
		t.Fatalf("claims = %d, want one", jobs.targetCalls)
	}
}

// A refresh on a server without the library mount keeps a local source whose
// copy is still pending, and the remote stand-in it serves meanwhile, rather
// than letting the remote pick take the slot and its queued job. A slot that
// serves nothing yet keeps its source when nothing is offered, too.
func TestRefreshWithoutTheMountKeepsPendingLocalArtwork(t *testing.T) {
	const (
		contentID    = "movie:tmdb:7"
		standIn      = "https://img.example/stand-in.jpg"
		remotePoster = "tmdb://remote-poster.jpg"
	)
	offered := []RemoteImage{{Type: ImagePoster, URL: remotePoster, ProviderID: "tmdb", Rating: 7.5, Language: "en"}}
	for _, tc := range []struct {
		name, path, wantPath string
		images               []RemoteImage
	}{
		{name: "serving a stand-in", path: standIn, wantPath: standIn, images: offered},
		{name: "serving nothing yet", path: "", wantPath: remotePoster, images: offered},
		{name: "serving nothing, nothing offered", path: "", wantPath: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			localSource := "file://" + filepath.Join(t.TempDir(), "missing-mount", "Film", "poster.jpg") + "?v=12-1"
			h := newTestHarness()
			enqueuer := &recordingImageCacheJobEnqueuer{}
			h.service.SetImageCacheJobEnqueuer(enqueuer)
			h.service.SetAutoCacheImages(true)
			ctx := withLocalArtworkSearched(context.Background(), localArtworkItem)
			if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
				ContentID: contentID, Type: "movie", Title: "Film", Status: "matched", TmdbID: "7",
				PosterPath: tc.path, PosterSourcePath: localSource,
				Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
			}); err != nil {
				t.Fatalf("upsert existing item: %v", err)
			}
			providerRepo := newFakeProviderIDRepo()
			providerRepo.set(contentID, &models.MediaItemProviderID{ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "7"})
			h.service.providerIDRepo = providerRepo

			if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: contentID, Mode: ModeManualRefresh, Language: "en"},
				&MetadataResult{HasMetadata: true, Title: "Film", ProviderIDs: map[string]string{"tmdb": "7"}},
				tc.images, nil, nil, "movie"); err != nil {
				t.Fatalf("mergeAndPersist: %v", err)
			}
			got, err := h.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if got.PosterSourcePath != localSource || got.PosterPath != tc.wantPath {
				t.Fatalf("poster = path %q source %q, want path %q and the local source kept", got.PosterPath, got.PosterSourcePath, tc.wantPath)
			}
			for _, job := range enqueuer.inputs {
				if job.ImageType == ImageCacheImagePoster && job.SourcePath != localSource {
					t.Fatalf("queued %q over the pending local poster", job.SourcePath)
				}
			}
		})
	}
}

// An item left on its stand-in by a refresh that cannot judge the sidecar
// keeps its local source: its artwork is locked, or the local provider did
// not answer and no other artwork was offered.
func TestItemLeftOnItsStandInKeepsPendingLocalSource(t *testing.T) {
	const (
		contentID    = "movie:tmdb:7"
		standIn      = "https://img.example/stand-in.jpg"
		remotePoster = "tmdb://remote-poster.jpg"
	)
	for _, tc := range []struct {
		name     string
		locked   bool
		searched bool
		images   []RemoteImage
	}{
		{name: "artwork locked", locked: true, searched: true,
			images: []RemoteImage{{Type: ImagePoster, URL: remotePoster, ProviderID: "tmdb", Rating: 7.5, Language: "en"}}},
		{name: "local art not searched, nothing offered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The sidecar is gone from a mounted folder, so only the lock or
			// the missing search keeps it.
			dir := filepath.Join(t.TempDir(), "Film")
			mkdirAll(t, dir)
			writeTestBytes(t, filepath.Join(dir, "Film.mkv"))
			localSource := "file://" + filepath.Join(dir, "poster.jpg") + "?v=12-1"
			h := newTestHarness()
			enqueuer := &recordingImageCacheJobEnqueuer{}
			h.service.SetImageCacheJobEnqueuer(enqueuer)
			h.service.SetAutoCacheImages(true)
			ctx := context.Background()
			if tc.searched {
				ctx = withLocalArtworkSearched(ctx, localArtworkItem)
			}
			existing := &models.MediaItem{
				ContentID: contentID, Type: "movie", Title: "Film", Status: "matched", TmdbID: "7",
				PosterPath: standIn, PosterSourcePath: localSource,
				Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
			}
			if tc.locked {
				existing.LockedFields = []int{int(FieldImages)}
			}
			if err := h.itemRepo.Upsert(ctx, existing); err != nil {
				t.Fatalf("upsert existing item: %v", err)
			}
			providerRepo := newFakeProviderIDRepo()
			providerRepo.set(contentID, &models.MediaItemProviderID{ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "7"})
			h.service.providerIDRepo = providerRepo

			if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: contentID, Mode: ModeManualRefresh, Language: "en"},
				&MetadataResult{HasMetadata: true, Title: "Film", ProviderIDs: map[string]string{"tmdb": "7"}},
				tc.images, nil, nil, "movie"); err != nil {
				t.Fatalf("mergeAndPersist: %v", err)
			}
			got, err := h.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				t.Fatalf("load item: %v", err)
			}
			if got.PosterPath != standIn || got.PosterSourcePath != localSource {
				t.Fatalf("poster = path %q source %q, want the stand-in and the local source kept", got.PosterPath, got.PosterSourcePath)
			}
			for _, job := range enqueuer.inputs {
				if job.ImageType == ImageCacheImagePoster && job.SourcePath != localSource {
					t.Fatalf("queued %q over the pending local poster", job.SourcePath)
				}
			}
		})
	}
}

// Seasons and episodes keep a pending local source and its stand-in the same
// way, also when the refresh offers no artwork at all. A slot with no stand-in
// yet takes the remote artwork this refresh chose as one, and keeps its local
// source when nothing is offered.
func TestPersistSeasonsAndEpisodesWithoutTheMountKeepsPendingLocalArtwork(t *testing.T) {
	const (
		seriesID      = "series:tvdb:123"
		seasonStandIn = "https://img.example/season-stand-in.jpg"
		stillStandIn  = "https://img.example/still-stand-in.jpg"
		remoteSeason  = "tvdb://remote-season.jpg"
		remoteStill   = "tvdb://remote-still.jpg"
	)
	type slot struct{ path, thumbhash string }
	for _, tc := range []struct {
		name                      string
		storedSeason, storedStill slot
		remoteSeason, remoteStill string
		wantSeason, wantStill     slot
	}{
		{
			name:         "serving stand-ins, remote artwork offered",
			storedSeason: slot{seasonStandIn, "season-thumb"}, storedStill: slot{stillStandIn, "still-thumb"},
			remoteSeason: remoteSeason, remoteStill: remoteStill,
			wantSeason: slot{seasonStandIn, "season-thumb"}, wantStill: slot{stillStandIn, "still-thumb"},
		},
		{
			name:         "serving stand-ins, no artwork offered",
			storedSeason: slot{seasonStandIn, "season-thumb"}, storedStill: slot{stillStandIn, "still-thumb"},
			wantSeason: slot{seasonStandIn, "season-thumb"}, wantStill: slot{stillStandIn, "still-thumb"},
		},
		{
			name:         "serving nothing yet",
			remoteSeason: remoteSeason, remoteStill: remoteStill,
			wantSeason: slot{remoteSeason, "remote-season-thumb"}, wantStill: slot{remoteStill, ""},
		},
		{name: "serving nothing, nothing offered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folder := filepath.Join(t.TempDir(), "missing-mount", "Show", "Season 01")
			seasonSource := "file://" + filepath.Join(folder, "poster.jpg") + "?v=12-1"
			stillSource := "file://" + filepath.Join(folder, "Show S01E01-thumb.jpg") + "?v=9-1"
			service, _, seasonRepo, episodeRepo := newSeasonEpisodeServiceForTest(seriesID)
			enqueuer := &recordingImageCacheJobEnqueuer{}
			service.SetImageCacheJobEnqueuer(enqueuer)
			seasonRepo.seasons[seasonKey(seriesID, 1)] = &models.Season{
				ContentID: "season-1", SeriesID: seriesID, SeasonNumber: 1, Title: "Season 1",
				PosterPath: tc.storedSeason.path, PosterSourcePath: seasonSource, PosterThumbhash: tc.storedSeason.thumbhash,
			}
			episodeRepo.episodes[episodeKey(seriesID, 1, 1)] = &models.Episode{
				ContentID: "episode-1", SeriesID: seriesID, SeasonID: "season-1", SeasonNumber: 1, EpisodeNumber: 1,
				Title: "Pilot", StillPath: tc.storedStill.path, StillSourcePath: stillSource, StillThumbhash: tc.storedStill.thumbhash,
			}
			remoteSeasonThumbhash := ""
			if tc.remoteSeason != "" {
				remoteSeasonThumbhash = "remote-season-thumb"
			}

			service.persistSeasonsAndEpisodes(withLocalArtworkSearched(context.Background(), localArtworkSeason|localArtworkEpisode),
				&models.MediaItem{ContentID: seriesID, Type: "series", TvdbID: "123"},
				map[string]string{"tvdb": "123"}, "en", "en",
				[]SeasonResult{{SeasonNumber: 1, Title: "Season 1", PosterPath: tc.remoteSeason, PosterThumbhash: remoteSeasonThumbhash}},
				[]EpisodeResult{{
					ProviderIDs: map[string]string{"tvdb": "ep-1"}, SeasonNumber: 1, EpisodeNumber: 1,
					Title: "Pilot", StillPath: tc.remoteStill,
				}},
				MergeReplaceUnlocked,
			)

			season := seasonRepo.seasons[seasonKey(seriesID, 1)]
			if got := (slot{season.PosterPath, season.PosterThumbhash}); got != tc.wantSeason || season.PosterSourcePath != seasonSource {
				t.Fatalf("season poster = %+v source %q, want %+v and the local source kept", got, season.PosterSourcePath, tc.wantSeason)
			}
			episode := episodeRepo.episodes[episodeKey(seriesID, 1, 1)]
			if got := (slot{episode.StillPath, episode.StillThumbhash}); got != tc.wantStill || episode.StillSourcePath != stillSource {
				t.Fatalf("episode still = %+v source %q, want %+v and the local source kept", got, episode.StillSourcePath, tc.wantStill)
			}
			for _, job := range enqueuer.inputs {
				if job.SourcePath == remoteSeason || job.SourcePath == remoteStill {
					t.Fatalf("queued remote artwork over pending local artwork: %+v", job)
				}
			}
		})
	}
}

// With caching off a fill-empty refresh replaces a stored copy only with a
// provider image; a slot nothing fills keeps its copy, a stored clear-art
// logo included, rather than being emptied.
func TestCachingOffKeepsStoredArtworkNoProviderFills(t *testing.T) {
	const (
		contentID    = "movie:tmdb:7"
		storedPoster = "tmdb/movies/7/poster/original.r1.webp"
		storedLogo   = "tmdb/movies/7/logo/original.r1.webp"
		remotePoster = "tmdb://remote-poster.jpg"
	)
	h := newTestHarness()
	h.service.SetImageCacheJobEnqueuer(&recordingImageCacheJobEnqueuer{})
	ctx := context.Background()
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: contentID, Type: "movie", Title: "Film", Status: "matched", TmdbID: "7",
		PosterPath: storedPoster, PosterSourcePath: "file:///m/Film/poster.jpg?v=1-1",
		LogoPath: storedLogo, LogoSourcePath: "https://img.example/clearart/film.png",
		Studios: []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatalf("upsert existing item: %v", err)
	}
	providerRepo := newFakeProviderIDRepo()
	providerRepo.set(contentID, &models.MediaItemProviderID{ContentID: contentID, ItemType: "movie", Provider: "tmdb", ProviderID: "7"})
	h.service.providerIDRepo = providerRepo

	if _, err := h.service.mergeAndPersist(ctx, ProcessRequest{ContentID: contentID, Mode: ModeScheduledRefresh, Language: "en"},
		&MetadataResult{HasMetadata: true, Title: "Film", ProviderIDs: map[string]string{"tmdb": "7"}},
		[]RemoteImage{{Type: ImagePoster, URL: remotePoster, ProviderID: "tmdb", Language: "en"}},
		nil, nil, "movie"); err != nil {
		t.Fatalf("mergeAndPersist: %v", err)
	}
	got, err := h.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatalf("load item: %v", err)
	}
	if got.PosterPath != remotePoster || got.PosterSourcePath != remotePoster {
		t.Errorf("poster = %q/%q, want the unrated provider poster for both", got.PosterPath, got.PosterSourcePath)
	}
	if got.LogoPath != storedLogo {
		t.Errorf("logo = %q, want the stored copy kept", got.LogoPath)
	}
}
