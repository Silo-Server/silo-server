package metadata

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeImageCacheJobs struct {
	claimed       []*models.MetadataImageCacheJob
	succeededID   int64
	failedID      int64
	failedText    string
	deletedCount  int
	requeuedIDs   []int64
	currentSource *string // when set, overrides CurrentTargetSourcePath
	targetFound   *bool
}

type targetImageCacheJobs struct {
	fakeImageCacheJobs
	retriedTarget  string
	retryCalls     int
	claimedTarget  string
	targetClaims   [][]*models.MetadataImageCacheJob
	targetCalls    int
	runningResults []bool
	runningCalls   int
	alwaysRunning  bool
}

func (f *targetImageCacheJobs) retryTargetNow(_ context.Context, targetContentID string) error {
	f.retriedTarget = targetContentID
	f.retryCalls++
	return nil
}

func (f *targetImageCacheJobs) claimDueForTarget(_ context.Context, _ string, targetContentID string, _ int) ([]*models.MetadataImageCacheJob, error) {
	f.claimedTarget = targetContentID
	var jobs []*models.MetadataImageCacheJob
	if f.targetCalls < len(f.targetClaims) {
		jobs = f.targetClaims[f.targetCalls]
	}
	f.targetCalls++
	return jobs, nil
}

func (f *targetImageCacheJobs) targetHasRunningJobs(_ context.Context, _ string) (bool, error) {
	running := f.alwaysRunning
	if f.runningCalls < len(f.runningResults) {
		running = f.runningResults[f.runningCalls]
	}
	f.runningCalls++
	return running, nil
}

func (f *fakeImageCacheJobs) ClaimDue(context.Context, string, int) ([]*models.MetadataImageCacheJob, error) {
	return f.claimed, nil
}

func (f *fakeImageCacheJobs) MarkSucceeded(_ context.Context, id int64, _ string) error {
	f.succeededID = id
	return nil
}

func (f *fakeImageCacheJobs) MarkFailed(_ context.Context, id int64, _ int, _ string, errText string) error {
	f.failedID = id
	f.failedText = errText
	return nil
}

func (f *fakeImageCacheJobs) RequeueClaimed(_ context.Context, ids []int64, _ string) error {
	f.requeuedIDs = append(f.requeuedIDs, ids...)
	return nil
}

func (f *fakeImageCacheJobs) CurrentTargetSourcePath(_ context.Context, job *models.MetadataImageCacheJob) (string, bool, error) {
	if f.targetFound != nil && !*f.targetFound {
		return "", false, nil
	}
	if f.currentSource != nil {
		return *f.currentSource, true, nil
	}
	return job.SourcePath, true, nil
}

func (f *fakeImageCacheJobs) EnqueueExistingProviderArtwork(context.Context, int) (int, error) {
	return 0, nil
}

func (f *fakeImageCacheJobs) DeleteSucceededBefore(context.Context, time.Time, int) (int, error) {
	return f.deletedCount, nil
}

type loopingImageCacheJobs struct {
	enqueueResults []int
	claimedResults [][]*models.MetadataImageCacheJob
	succeededIDs   []int64
	enqueueCalls   int
	enqueueLimits  []int
	claimCalls     int
	backlog        ImageCacheBacklog
	backlogCalls   int
}

type serializingImageCacheJobs struct {
	fakeImageCacheJobs
	mu            sync.Mutex
	claimCalls    int
	firstEntered  chan struct{}
	secondEntered chan struct{}
	releaseFirst  chan struct{}
}

func (f *serializingImageCacheJobs) ClaimDue(context.Context, string, int) ([]*models.MetadataImageCacheJob, error) {
	f.mu.Lock()
	f.claimCalls++
	call := f.claimCalls
	f.mu.Unlock()
	switch call {
	case 1:
		close(f.firstEntered)
		<-f.releaseFirst
	case 2:
		close(f.secondEntered)
	}
	return nil, nil
}

func (f *loopingImageCacheJobs) GetBacklog(context.Context) (ImageCacheBacklog, error) {
	f.backlogCalls++
	return f.backlog, nil
}

func (f *loopingImageCacheJobs) EnqueueExistingProviderArtwork(_ context.Context, limit int) (int, error) {
	result := 0
	if f.enqueueCalls < len(f.enqueueResults) {
		result = f.enqueueResults[f.enqueueCalls]
	}
	f.enqueueLimits = append(f.enqueueLimits, limit)
	f.enqueueCalls++
	return result, nil
}

func (f *loopingImageCacheJobs) ClaimDue(context.Context, string, int) ([]*models.MetadataImageCacheJob, error) {
	var result []*models.MetadataImageCacheJob
	if f.claimCalls < len(f.claimedResults) {
		result = f.claimedResults[f.claimCalls]
	}
	f.claimCalls++
	return result, nil
}

func (f *loopingImageCacheJobs) MarkSucceeded(_ context.Context, id int64, _ string) error {
	f.succeededIDs = append(f.succeededIDs, id)
	return nil
}

func (f *loopingImageCacheJobs) MarkFailed(context.Context, int64, int, string, string) error {
	return nil
}

func (f *loopingImageCacheJobs) RequeueClaimed(context.Context, []int64, string) error {
	return nil
}

func (f *loopingImageCacheJobs) CurrentTargetSourcePath(_ context.Context, job *models.MetadataImageCacheJob) (string, bool, error) {
	return job.SourcePath, true, nil
}

func (f *loopingImageCacheJobs) DeleteSucceededBefore(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

// repairOnlyImageCacheJobs is the passthrough seam: a store that can claim
// repair-requested jobs on their own, and that records whether the ordinary
// materialization claim was attempted at all.
type repairOnlyImageCacheJobs struct {
	fakeImageCacheJobs
	repairBatches  [][]*models.MetadataImageCacheJob
	repairCalls    int
	ordinaryClaims int
}

func (f *repairOnlyImageCacheJobs) ClaimDue(context.Context, string, int) ([]*models.MetadataImageCacheJob, error) {
	f.ordinaryClaims++
	return f.claimed, nil
}

func (f *repairOnlyImageCacheJobs) ClaimDueRepairs(context.Context, string, int) ([]*models.MetadataImageCacheJob, error) {
	var batch []*models.MetadataImageCacheJob
	if f.repairCalls < len(f.repairBatches) {
		batch = f.repairBatches[f.repairCalls]
	}
	f.repairCalls++
	return batch, nil
}

type fakeImageCacher struct {
	result *CacheImageResult
	err    error
	reqs   []CacheImageRequest
	after  func()
}

func (f *fakeImageCacher) CacheImage(_ context.Context, req CacheImageRequest) (*CacheImageResult, error) {
	f.reqs = append(f.reqs, req)
	if f.after != nil {
		f.after()
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

type fakeImageResolver struct {
	url string
}

func (f *fakeImageResolver) ResolveImageURL(context.Context, string, string) string {
	return f.url
}

type fakeArtworkPublicationObserver struct {
	jobs                  *fakeImageCacheJobs
	called                int
	publishedPath         string
	succeededIDAtCallback int64
}

func (f *fakeArtworkPublicationObserver) ArtworkPublished(_ context.Context, _ *models.MetadataImageCacheJob, _, publishedPath string) error {
	f.called++
	f.publishedPath = publishedPath
	f.succeededIDAtCallback = f.jobs.succeededID
	return nil
}

type fakeEpisodeStillUpdater struct {
	updated    bool
	seriesID   string
	season     int
	episode    int
	sourcePath string
	cachedPath string
	thumbhash  string
}

func (f *fakeEpisodeStillUpdater) UpdateStillIfSourceMatches(_ context.Context, seriesID string, seasonNumber, episodeNumber int, sourcePath, cachedPath, thumbhash string) (bool, error) {
	f.seriesID = seriesID
	f.season = seasonNumber
	f.episode = episodeNumber
	f.sourcePath = sourcePath
	f.cachedPath = cachedPath
	f.thumbhash = thumbhash
	return f.updated, nil
}

type fakeItemArtworkUpdater struct {
	updated    bool
	contentID  string
	imageType  string
	sourcePath string
	cachedPath string
	thumbhash  string
}

func (f *fakeItemArtworkUpdater) UpdateArtworkIfSourceMatches(_ context.Context, contentID, imageType, sourcePath, cachedPath, thumbhash string) (bool, error) {
	f.contentID = contentID
	f.imageType = imageType
	f.sourcePath = sourcePath
	f.cachedPath = cachedPath
	f.thumbhash = thumbhash
	return f.updated, nil
}

type fakeItemLocalizationArtworkUpdater struct {
	updated    bool
	contentID  string
	language   string
	imageType  string
	sourcePath string
	cachedPath string
	thumbhash  string
}

func (f *fakeItemLocalizationArtworkUpdater) UpdateArtworkIfSourceMatches(_ context.Context, contentID, language, imageType, sourcePath, cachedPath, thumbhash string) (bool, error) {
	f.contentID = contentID
	f.language = language
	f.imageType = imageType
	f.sourcePath = sourcePath
	f.cachedPath = cachedPath
	f.thumbhash = thumbhash
	return f.updated, nil
}

type fakePersonPhotoUpdater struct {
	updated    bool
	personID   int64
	sourcePath string
	cachedPath string
	thumbhash  string
}

func (f *fakePersonPhotoUpdater) UpdatePhotoIfSourceMatches(_ context.Context, personID int64, sourcePath, cachedPath, thumbhash string) (bool, error) {
	f.personID = personID
	f.sourcePath = sourcePath
	f.cachedPath = cachedPath
	f.thumbhash = thumbhash
	return f.updated, nil
}

func TestImageCacheProcessorUpdatesEpisodeOnSuccess(t *testing.T) {
	jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{{
		ID:                1,
		TargetType:        ImageCacheTargetEpisode,
		TargetContentID:   "series-tvdb-1",
		SourcePath:        "tvdb://banners/episode.jpg",
		ProviderID:        "tvdb",
		ProviderContentID: "1",
		ContentType:       "series",
		ImageType:         ImageCacheImageStill,
		SeasonNumber:      intPointer(1),
		EpisodeNumber:     intPointer(1),
	}}}
	cacher := &fakeImageCacher{result: &CacheImageResult{
		BasePath:  "tvdb/series/1/seasons/1/episodes/1/still",
		Ext:       ".webp",
		Thumbhash: "thumb",
	}}
	resolver := &fakeImageResolver{url: "https://artworks.thetvdb.com/banners/episode.jpg"}
	episodes := &fakeEpisodeStillUpdater{updated: true}

	processor := NewImageCacheProcessor(jobs, cacher, resolver, nil, episodes)
	stats, err := processor.RunOnce(context.Background(), "test-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Succeeded != 1 {
		t.Fatalf("Succeeded = %d, want 1", stats.Succeeded)
	}
	if episodes.cachedPath != "tvdb/series/1/seasons/1/episodes/1/still/original.webp" {
		t.Fatalf("cachedPath = %q", episodes.cachedPath)
	}
	if episodes.sourcePath != "tvdb://banners/episode.jpg" {
		t.Fatalf("sourcePath = %q", episodes.sourcePath)
	}
	if episodes.seriesID != "series-tvdb-1" || episodes.season != 1 || episodes.episode != 1 {
		t.Fatalf("episode target = (%q, %d, %d), want (series-tvdb-1, 1, 1)", episodes.seriesID, episodes.season, episodes.episode)
	}
	if jobs.succeededID != 1 {
		t.Fatalf("succeededID = %d", jobs.succeededID)
	}
}

func TestImageCacheProcessorFailsRepairWhenTargetLookupMatchesNoRow(t *testing.T) {
	missing := false
	jobs := &fakeImageCacheJobs{
		targetFound: &missing,
		claimed: []*models.MetadataImageCacheJob{{
			ID:              19,
			TargetType:      ImageCacheTargetSeason,
			TargetContentID: "series-tvdb-293088",
			SourcePath:      "tvdb://banners/season.jpg",
			ImageType:       ImageCacheImagePoster,
			SeasonNumber:    intPointer(0),
			RepairRequested: true,
		}},
	}
	cacher := &fakeImageCacher{}
	processor := NewImageCacheProcessorWithTargets(jobs, cacher, &fakeImageResolver{}, ImageCacheProcessorTargets{})

	stats, err := processor.RunOnce(context.Background(), "repair-worker", 1, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Failed != 1 || stats.Succeeded != 0 || stats.Skipped != 0 {
		t.Fatalf("stats = %+v, want one failed repair", stats)
	}
	if jobs.failedID != 19 || jobs.succeededID != 0 {
		t.Fatalf("failed=%d succeeded=%d, want failed=19 succeeded=0", jobs.failedID, jobs.succeededID)
	}
	if !strings.Contains(jobs.failedText, "repair target not found") ||
		!strings.Contains(jobs.failedText, "series-tvdb-293088") ||
		!strings.Contains(jobs.failedText, "season=0") {
		t.Fatalf("failure text = %q", jobs.failedText)
	}
	if len(cacher.reqs) != 0 {
		t.Fatalf("cache requests = %d, want none for a missing repair target", len(cacher.reqs))
	}
}

func TestImageCacheProcessorSkipsOrdinaryJobWhenTargetVanished(t *testing.T) {
	missing := false
	jobs := &fakeImageCacheJobs{
		targetFound: &missing,
		claimed: []*models.MetadataImageCacheJob{{
			ID:              20,
			TargetType:      ImageCacheTargetItem,
			TargetContentID: "movie-gone",
			SourcePath:      "tmdb://posters/gone.jpg",
			ImageType:       ImageCacheImagePoster,
		}},
	}
	processor := NewImageCacheProcessorWithTargets(jobs, &fakeImageCacher{}, &fakeImageResolver{}, ImageCacheProcessorTargets{})

	stats, err := processor.RunOnce(context.Background(), "ordinary-worker", 1, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Skipped != 1 || jobs.succeededID != 20 || jobs.failedID != 0 {
		t.Fatalf("stats=%+v succeeded=%d failed=%d, want benign skip", stats, jobs.succeededID, jobs.failedID)
	}
}

func TestImageCacheProcessorUpdatesItemArtworkOnSuccess(t *testing.T) {
	jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{{
		ID:                20,
		TargetType:        ImageCacheTargetItem,
		TargetContentID:   "series-1",
		SourcePath:        "tmdb://poster/series.jpg",
		ProviderID:        "tmdb",
		ProviderContentID: "1396",
		ContentType:       "series",
		ImageType:         ImageCacheImageBackdrop,
	}}}
	cacher := &fakeImageCacher{result: &CacheImageResult{
		BasePath:  "tmdb/series/1396/backdrop",
		Ext:       ".webp",
		Thumbhash: "thumb",
	}}
	resolver := &fakeImageResolver{url: "https://image.tmdb.org/t/p/original/backdrop.jpg"}
	items := &fakeItemArtworkUpdater{updated: true}

	processor := NewImageCacheProcessorWithTargets(jobs, cacher, resolver, ImageCacheProcessorTargets{
		Items: items,
	})
	observer := &fakeArtworkPublicationObserver{jobs: jobs}
	processor.SetArtworkPublicationObserver(observer)
	stats, err := processor.RunOnce(context.Background(), "test-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Succeeded != 1 {
		t.Fatalf("Succeeded = %d, want 1", stats.Succeeded)
	}
	if len(cacher.reqs) != 1 || cacher.reqs[0].SourceReference != jobs.claimed[0].SourcePath {
		t.Fatalf("stable source reference = %#v, want original plugin path", cacher.reqs)
	}
	if items.imageType != ImageCacheImageBackdrop {
		t.Fatalf("imageType = %q, want backdrop", items.imageType)
	}
	if items.cachedPath != "tmdb/series/1396/backdrop/original.webp" {
		t.Fatalf("cachedPath = %q", items.cachedPath)
	}
	if observer.called != 1 || observer.publishedPath != items.cachedPath {
		t.Fatalf("publication observer = %d/%q, want 1/%q", observer.called, observer.publishedPath, items.cachedPath)
	}
	if observer.succeededIDAtCallback != 0 || jobs.succeededID != 20 {
		t.Fatalf("publication/completion order = %d then %d, want 0 then 20", observer.succeededIDAtCallback, jobs.succeededID)
	}
}

func TestImageCacheProcessorCachesOnlyManuallyRefreshedTargetImmediately(t *testing.T) {
	job := &models.MetadataImageCacheJob{
		ID:                23,
		TargetType:        ImageCacheTargetItem,
		TargetContentID:   "series-1",
		SourcePath:        "tmdb://poster/series.jpg",
		ProviderID:        "tmdb",
		ProviderContentID: "1396",
		ContentType:       "series",
		ImageType:         ImageCacheImagePoster,
	}
	jobs := &targetImageCacheJobs{
		targetClaims: [][]*models.MetadataImageCacheJob{{job}, nil},
	}
	cacher := &fakeImageCacher{result: &CacheImageResult{
		BasePath:  "tmdb/series/1396/poster",
		Ext:       ".webp",
		Thumbhash: "thumb",
	}}
	resolver := &fakeImageResolver{url: "https://image.tmdb.org/t/p/original/poster.jpg"}
	items := &fakeItemArtworkUpdater{updated: true}

	processor := NewImageCacheProcessorWithTargets(jobs, cacher, resolver, ImageCacheProcessorTargets{
		Items: items,
	})
	if err := processor.CacheTargetArtwork(context.Background(), "series-1"); err != nil {
		t.Fatalf("CacheTargetArtwork() error = %v", err)
	}
	if jobs.retriedTarget != "series-1" || jobs.claimedTarget != "series-1" {
		t.Fatalf("target retry/claim = %q/%q, want series-1", jobs.retriedTarget, jobs.claimedTarget)
	}
	if jobs.targetCalls != 2 {
		t.Fatalf("target claim calls = %d, want 2", jobs.targetCalls)
	}
	// The retry-now reset runs once per refresh, not once per poll.
	if jobs.retryCalls != 1 {
		t.Fatalf("target retry calls = %d, want 1", jobs.retryCalls)
	}
	if jobs.succeededID != 23 {
		t.Fatalf("succeededID = %d, want 23", jobs.succeededID)
	}
	if items.cachedPath != "tmdb/series/1396/poster/original.webp" {
		t.Fatalf("cachedPath = %q", items.cachedPath)
	}
}

func TestImageCacheProcessorReportsImmediateTargetFailure(t *testing.T) {
	job := &models.MetadataImageCacheJob{
		ID:                24,
		TargetType:        ImageCacheTargetItem,
		TargetContentID:   "series-1",
		SourcePath:        "tmdb://poster/series.jpg",
		ProviderID:        "tmdb",
		ProviderContentID: "1396",
		ContentType:       "series",
		ImageType:         ImageCacheImagePoster,
	}
	jobs := &targetImageCacheJobs{
		targetClaims: [][]*models.MetadataImageCacheJob{{job}, nil},
	}
	processor := NewImageCacheProcessorWithTargets(
		jobs,
		&fakeImageCacher{err: errors.New("cache failed")},
		&fakeImageResolver{url: "https://image.tmdb.org/t/p/original/poster.jpg"},
		ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}},
	)

	err := processor.CacheTargetArtwork(context.Background(), "series-1")
	if err == nil || !strings.Contains(err.Error(), "failed to cache") {
		t.Fatalf("CacheTargetArtwork() error = %v, want cache failure", err)
	}
	if jobs.failedID != 24 {
		t.Fatalf("failedID = %d, want 24", jobs.failedID)
	}
}

func TestImageCacheProcessorRetriesTargetAfterConcurrentWorkerFinishes(t *testing.T) {
	job := &models.MetadataImageCacheJob{
		ID:                25,
		TargetType:        ImageCacheTargetItem,
		TargetContentID:   "series-1",
		SourcePath:        "tmdb://poster/series.jpg",
		ProviderID:        "tmdb",
		ProviderContentID: "1396",
		ContentType:       "series",
		ImageType:         ImageCacheImagePoster,
	}
	jobs := &targetImageCacheJobs{
		targetClaims:   [][]*models.MetadataImageCacheJob{nil, {job}, nil},
		runningResults: []bool{true, false},
	}
	processor := NewImageCacheProcessorWithTargets(
		jobs,
		&fakeImageCacher{result: &CacheImageResult{BasePath: "tmdb/series/1396/poster", Ext: ".webp"}},
		&fakeImageResolver{url: "https://image.tmdb.org/t/p/original/poster.jpg"},
		ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}},
	)

	if err := processor.CacheTargetArtwork(context.Background(), "series-1"); err != nil {
		t.Fatalf("CacheTargetArtwork() error = %v", err)
	}
	if jobs.retryCalls != 1 {
		t.Fatalf("target retry calls = %d, want 1", jobs.retryCalls)
	}
	if jobs.succeededID != 25 {
		t.Fatalf("succeededID = %d, want 25", jobs.succeededID)
	}
}

func TestImageCacheProcessorStopsWaitingOnStuckBackgroundWorker(t *testing.T) {
	// A worker that claimed the job and died holds its lease for
	// ImageCacheLeaseDuration. The interactive refresh must not block that
	// long: it gives up and reports the artwork as still pending.
	jobs := &targetImageCacheJobs{alwaysRunning: true}
	processor := NewImageCacheProcessorWithTargets(
		jobs,
		&fakeImageCacher{},
		&fakeImageResolver{},
		ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{}},
	)
	processor.idleWaitTimeout = 20 * time.Millisecond

	err := processor.CacheTargetArtwork(context.Background(), "series-1")
	if !errors.Is(err, ErrTargetArtworkPending) {
		t.Fatalf("CacheTargetArtwork() error = %v, want ErrTargetArtworkPending", err)
	}
	if jobs.retryCalls != 1 {
		t.Fatalf("target retry calls = %d, want 1", jobs.retryCalls)
	}
	// A hot 10 Hz poll would issue far more probes than this in 20ms.
	if jobs.runningCalls > 4 {
		t.Fatalf("running probes = %d, want a backed-off poll", jobs.runningCalls)
	}
}

func TestImageCacheProcessorPassesLanguageToLocalizedItemArtwork(t *testing.T) {
	jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{{
		ID:                21,
		TargetType:        ImageCacheTargetItemLocalization,
		TargetContentID:   "series-1",
		TargetLanguage:    "fr",
		SourcePath:        "tmdb://logo/fr.png",
		ProviderID:        "tmdb",
		ProviderContentID: "1396",
		ContentType:       "series",
		ImageType:         ImageCacheImageLogo,
	}}}
	cacher := &fakeImageCacher{result: &CacheImageResult{
		BasePath: "tmdb/series/1396/localizations/fr/logo",
		Ext:      ".webp",
	}}
	resolver := &fakeImageResolver{url: "https://image.tmdb.org/t/p/original/logo.png"}
	localizations := &fakeItemLocalizationArtworkUpdater{updated: true}

	processor := NewImageCacheProcessorWithTargets(jobs, cacher, resolver, ImageCacheProcessorTargets{
		ItemLocalizations: localizations,
	})
	stats, err := processor.RunOnce(context.Background(), "test-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Succeeded != 1 {
		t.Fatalf("Succeeded = %d, want 1", stats.Succeeded)
	}
	if len(cacher.reqs) != 1 || cacher.reqs[0].Language != "fr" {
		t.Fatalf("cached request language = %#v, want fr", cacher.reqs)
	}
	if localizations.language != "fr" || localizations.imageType != ImageCacheImageLogo {
		t.Fatalf("localization update = language %q image %q", localizations.language, localizations.imageType)
	}
}

func TestImageCacheProcessorUpdatesPersonProfileOnSuccess(t *testing.T) {
	jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{{
		ID:                22,
		TargetType:        ImageCacheTargetPerson,
		TargetContentID:   "287",
		SourcePath:        "tmdb://profile/287.jpg",
		ProviderID:        "tmdb",
		ProviderContentID: "287",
		ContentType:       "people",
		ImageType:         ImageCacheImageProfile,
	}}}
	cacher := &fakeImageCacher{result: &CacheImageResult{
		BasePath:  "tmdb/people/287/profile",
		Ext:       ".webp",
		Thumbhash: "person-thumb",
	}}
	resolver := &fakeImageResolver{url: "https://image.tmdb.org/t/p/original/person.jpg"}
	people := &fakePersonPhotoUpdater{updated: true}

	processor := NewImageCacheProcessorWithTargets(jobs, cacher, resolver, ImageCacheProcessorTargets{
		People: people,
	})
	stats, err := processor.RunOnce(context.Background(), "test-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Succeeded != 1 {
		t.Fatalf("Succeeded = %d, want 1", stats.Succeeded)
	}
	if people.personID != 287 {
		t.Fatalf("personID = %d, want 287", people.personID)
	}
	if people.cachedPath != "tmdb/people/287/profile/original.webp" {
		t.Fatalf("cachedPath = %q", people.cachedPath)
	}
	if people.thumbhash != "person-thumb" {
		t.Fatalf("thumbhash = %q", people.thumbhash)
	}
}

func TestImageCacheProcessorMarksSkippedWhenSourceNoLongerMatches(t *testing.T) {
	jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{{
		ID:                2,
		TargetType:        ImageCacheTargetEpisode,
		TargetContentID:   "episode-tvdb-1-1-1",
		SourcePath:        "tvdb://banners/episode.jpg",
		ProviderID:        "tvdb",
		ProviderContentID: "1",
		ContentType:       "series",
		ImageType:         ImageCacheImageStill,
		SeasonNumber:      intPointer(1),
		EpisodeNumber:     intPointer(1),
	}}}
	cacher := &fakeImageCacher{result: &CacheImageResult{BasePath: "tvdb/series/1/seasons/1/episodes/1/still", Ext: ".webp"}}
	resolver := &fakeImageResolver{url: "https://artworks.thetvdb.com/banners/episode.jpg"}
	episodes := &fakeEpisodeStillUpdater{updated: false}

	processor := NewImageCacheProcessor(jobs, cacher, resolver, nil, episodes)
	stats, err := processor.RunOnce(context.Background(), "test-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", stats.Skipped)
	}
	if jobs.succeededID != 2 {
		t.Fatalf("succeededID = %d, want 2", jobs.succeededID)
	}
}

func TestImageCacheProcessorMarksFailureOnCacheError(t *testing.T) {
	jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{{
		ID:                3,
		TargetType:        ImageCacheTargetEpisode,
		TargetContentID:   "episode-tvdb-1-1-1",
		SourcePath:        "tvdb://banners/episode.jpg",
		ProviderID:        "tvdb",
		ProviderContentID: "1",
		ContentType:       "series",
		ImageType:         ImageCacheImageStill,
		SeasonNumber:      intPointer(1),
		EpisodeNumber:     intPointer(1),
	}}}
	cacher := &fakeImageCacher{err: errors.New("cache failed")}
	resolver := &fakeImageResolver{url: "https://artworks.thetvdb.com/banners/episode.jpg"}
	episodes := &fakeEpisodeStillUpdater{updated: false}

	processor := NewImageCacheProcessor(jobs, cacher, resolver, nil, episodes)
	stats, err := processor.RunOnce(context.Background(), "test-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", stats.Failed)
	}
	if jobs.failedID != 3 {
		t.Fatalf("failedID = %d, want 3", jobs.failedID)
	}
}

func TestImageCacheProcessorMarksFailureOnEmptyCacheResult(t *testing.T) {
	jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{{
		ID:                4,
		TargetType:        ImageCacheTargetEpisode,
		TargetContentID:   "episode-tvdb-1-1-1",
		SourcePath:        "tvdb://banners/episode.jpg",
		ProviderID:        "tvdb",
		ProviderContentID: "1",
		ContentType:       "series",
		ImageType:         ImageCacheImageStill,
		SeasonNumber:      intPointer(1),
		EpisodeNumber:     intPointer(1),
	}}}
	cacher := &fakeImageCacher{result: &CacheImageResult{}}
	resolver := &fakeImageResolver{url: "https://artworks.thetvdb.com/banners/episode.jpg"}
	episodes := &fakeEpisodeStillUpdater{updated: true}

	processor := NewImageCacheProcessor(jobs, cacher, resolver, nil, episodes)
	stats, err := processor.RunOnce(context.Background(), "test-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", stats.Failed)
	}
	if jobs.failedID != 4 {
		t.Fatalf("failedID = %d, want 4", jobs.failedID)
	}
	if episodes.cachedPath != "" {
		t.Fatalf("episode updater was called with cachedPath = %q", episodes.cachedPath)
	}
}

func TestImageCacheProcessorDeletesOldSucceededJobsWithoutClaimedJobs(t *testing.T) {
	jobs := &fakeImageCacheJobs{deletedCount: 7}
	processor := NewImageCacheProcessor(jobs, &fakeImageCacher{}, nil, nil, nil)

	stats, err := processor.RunOnce(context.Background(), "test-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.DeletedSucceeded != 7 {
		t.Fatalf("DeletedSucceeded = %d, want 7", stats.DeletedSucceeded)
	}
}

func TestImageCacheProcessorDrainUntilIdleNeverDiscoversCatalog(t *testing.T) {
	job := &models.MetadataImageCacheJob{
		ID:                9,
		TargetType:        ImageCacheTargetEpisode,
		TargetContentID:   "episode-tvdb-1-1-1",
		SourcePath:        "tvdb://banners/episode-1.jpg",
		ProviderID:        "tvdb",
		ProviderContentID: "1",
		ContentType:       "series",
		ImageType:         ImageCacheImageStill,
		SeasonNumber:      intPointer(1),
		EpisodeNumber:     intPointer(1),
	}
	jobs := &loopingImageCacheJobs{
		// A discovery call would enqueue more work; drain mode must never ask.
		enqueueResults: []int{1000},
		claimedResults: [][]*models.MetadataImageCacheJob{
			{job},
			{},
		},
		backlog: ImageCacheBacklog{Known: true, Queued: 1},
	}
	cacher := &fakeImageCacher{result: &CacheImageResult{
		BasePath: "tvdb/series/1/seasons/1/episodes/1/still",
		Ext:      ".webp",
	}}
	resolver := &fakeImageResolver{url: "https://artworks.thetvdb.com/banners/episode.jpg"}
	processor := NewImageCacheProcessor(jobs, cacher, resolver, nil, &fakeEpisodeStillUpdater{updated: true})

	stats, err := processor.DrainUntilIdle(context.Background(), "test-worker", 1000, 2, time.Minute, nil)
	if err != nil {
		t.Fatalf("DrainUntilIdle() error = %v", err)
	}
	if stats.Claimed != 1 || stats.Succeeded != 1 || stats.EnqueuedExisting != 0 {
		t.Fatalf("stats = %+v, want one queued job drained and no discovery", stats)
	}
	if jobs.enqueueCalls != 0 || jobs.claimCalls != 2 {
		t.Fatalf("calls enqueue=%d claim=%d, want 0/2", jobs.enqueueCalls, jobs.claimCalls)
	}
}

func TestImageCacheProcessorRunUntilIdleDrainsNewWorkAddedDuringRun(t *testing.T) {
	job1 := &models.MetadataImageCacheJob{
		ID:                10,
		TargetType:        ImageCacheTargetEpisode,
		TargetContentID:   "episode-tvdb-1-1-1",
		SourcePath:        "tvdb://banners/episode-1.jpg",
		ProviderID:        "tvdb",
		ProviderContentID: "1",
		ContentType:       "series",
		ImageType:         ImageCacheImageStill,
		SeasonNumber:      intPointer(1),
		EpisodeNumber:     intPointer(1),
	}
	job2 := &models.MetadataImageCacheJob{
		ID:                11,
		TargetType:        ImageCacheTargetEpisode,
		TargetContentID:   "episode-tvdb-1-1-2",
		SourcePath:        "tvdb://banners/episode-2.jpg",
		ProviderID:        "tvdb",
		ProviderContentID: "1",
		ContentType:       "series",
		ImageType:         ImageCacheImageStill,
		SeasonNumber:      intPointer(1),
		EpisodeNumber:     intPointer(2),
	}
	// Discovery now runs only when the queue drains, not on every batch: drain
	// job1, find the queue empty and sweep (enqueues 1 more), drain job2, find
	// the queue empty and sweep again (enqueues 0 -> idle).
	jobs := &loopingImageCacheJobs{
		enqueueResults: []int{1, 0},
		backlog:        ImageCacheBacklog{Known: true, Queued: 2, Running: 1},
		claimedResults: [][]*models.MetadataImageCacheJob{
			{job1},
			{},
			{job2},
			{},
		},
	}
	cacher := &fakeImageCacher{result: &CacheImageResult{
		BasePath: "tvdb/series/1/seasons/1/episodes/1/still",
		Ext:      ".webp",
	}}
	resolver := &fakeImageResolver{url: "https://artworks.thetvdb.com/banners/episode.jpg"}
	episodes := &fakeEpisodeStillUpdater{updated: true}

	processor := NewImageCacheProcessor(jobs, cacher, resolver, nil, episodes)
	var progressUpdates []ImageCacheRunStats
	stats, err := processor.RunUntilIdle(
		context.Background(),
		"test-worker",
		1000,
		2,
		0,
		func(update ImageCacheRunStats) {
			progressUpdates = append(progressUpdates, update)
		},
	)
	if err != nil {
		t.Fatalf("RunUntilIdle() error = %v", err)
	}
	if stats.Batches != 4 {
		t.Fatalf("Batches = %d, want 4", stats.Batches)
	}
	if stats.RuntimeLimited {
		t.Fatal("manual backfill without a deadline reported RuntimeLimited")
	}
	if stats.EnqueuedExisting != 1 || stats.Claimed != 2 || stats.Succeeded != 2 {
		t.Fatalf("stats = %+v, want enqueued=1 claimed=2 succeeded=2", stats)
	}
	if jobs.enqueueCalls != 2 || jobs.claimCalls != 4 {
		t.Fatalf("calls enqueue=%d claim=%d, want enqueue=2 claim=4", jobs.enqueueCalls, jobs.claimCalls)
	}
	for _, limit := range jobs.enqueueLimits {
		if limit != imageCacheDiscoveryBatchSize {
			t.Fatalf("discovery limit = %d, want %d", limit, imageCacheDiscoveryBatchSize)
		}
	}
	if len(jobs.succeededIDs) != 2 || jobs.succeededIDs[0] != 10 || jobs.succeededIDs[1] != 11 {
		t.Fatalf("succeededIDs = %#v, want [10 11]", jobs.succeededIDs)
	}
	if len(progressUpdates) == 0 {
		t.Fatal("RunUntilIdle() did not report progress")
	}
	if got := progressUpdates[0].Backlog; got != jobs.backlog {
		t.Fatalf("initial backlog = %+v, want %+v", got, jobs.backlog)
	}
	if progressUpdates[0].Processed() != 0 {
		t.Fatalf("initial processed = %d, want 0", progressUpdates[0].Processed())
	}
	if jobs.backlogCalls != 1 {
		t.Fatalf("GetBacklog calls = %d, want exactly 1 per run", jobs.backlogCalls)
	}
	if got := progressUpdates[len(progressUpdates)-1]; got != stats {
		t.Fatalf("last progress update = %+v, want final stats %+v", got, stats)
	}
}

func TestImageCacheProcessorManualBackfillAlwaysDiscovers(t *testing.T) {
	jobs := &loopingImageCacheJobs{
		enqueueResults: []int{0, 0},
		claimedResults: [][]*models.MetadataImageCacheJob{
			{},
			{},
		},
	}
	processor := NewImageCacheProcessor(jobs, &fakeImageCacher{}, &fakeImageResolver{}, nil, nil)
	for i := 0; i < 2; i++ {
		if _, err := processor.RunUntilIdle(context.Background(), "test-worker", 1000, 2, 0, nil); err != nil {
			t.Fatalf("RunUntilIdle() call %d error = %v", i+1, err)
		}
	}
	if jobs.enqueueCalls != 2 {
		t.Fatalf("discovery calls = %d, want one for each explicit backfill", jobs.enqueueCalls)
	}
}

func TestImageCacheProcessorManualBackfillFailsClosedWhenDisabled(t *testing.T) {
	jobs := &loopingImageCacheJobs{}
	processor := NewImageCacheProcessor(jobs, &fakeImageCacher{}, &fakeImageResolver{}, nil, nil)
	processor.SetEnabled(false)
	_, err := processor.RunUntilIdle(context.Background(), "test-worker", 2, 2, 0, nil)
	if !errors.Is(err, ErrImageCachingDisabled) {
		t.Fatalf("RunUntilIdle() error = %v, want ErrImageCachingDisabled", err)
	}
	if jobs.claimCalls != 0 || jobs.enqueueCalls != 0 {
		t.Fatalf("disabled backfill calls claim=%d discovery=%d, want 0/0", jobs.claimCalls, jobs.enqueueCalls)
	}
}

func TestImageCacheProcessorManualBackfillStopsDiscoveryWhenDisabledMidRun(t *testing.T) {
	job := &models.MetadataImageCacheJob{
		ID:                91,
		TargetType:        ImageCacheTargetEpisode,
		TargetContentID:   "episode-tvdb-1-1-1",
		SourcePath:        "tvdb://banners/episode-1.jpg",
		ProviderID:        "tvdb",
		ProviderContentID: "1",
		ContentType:       "series",
		ImageType:         ImageCacheImageStill,
		SeasonNumber:      intPointer(1),
		EpisodeNumber:     intPointer(1),
	}
	jobs := &loopingImageCacheJobs{claimedResults: [][]*models.MetadataImageCacheJob{{job}}}
	cacher := &fakeImageCacher{result: &CacheImageResult{BasePath: "tvdb/series/1/seasons/1/episodes/1/still", Ext: ".webp"}}
	processor := NewImageCacheProcessor(jobs, cacher, &fakeImageResolver{url: "https://artworks.thetvdb.com/episode.jpg"}, nil, &fakeEpisodeStillUpdater{updated: true})
	cacher.after = func() { processor.SetEnabled(false) }
	_, err := processor.RunUntilIdle(context.Background(), "test-worker", 2, 2, 0, nil)
	if !errors.Is(err, ErrImageCachingDisabled) {
		t.Fatalf("RunUntilIdle() error = %v, want ErrImageCachingDisabled", err)
	}
	if jobs.enqueueCalls != 0 {
		t.Fatalf("discovery calls after disabling cache = %d, want 0", jobs.enqueueCalls)
	}
}

func TestImageCacheProcessorRequeuesClaimedTailWhenDisabled(t *testing.T) {
	jobs := &fakeImageCacheJobs{}
	for i := int64(1); i <= 4; i++ {
		jobs.claimed = append(jobs.claimed, &models.MetadataImageCacheJob{
			ID:                i,
			TargetType:        ImageCacheTargetEpisode,
			TargetContentID:   "episode-tvdb-1-1-1",
			SourcePath:        "tvdb://banners/episode-1.jpg",
			ProviderID:        "tvdb",
			ProviderContentID: "1",
			ContentType:       "series",
			ImageType:         ImageCacheImageStill,
			SeasonNumber:      intPointer(1),
			EpisodeNumber:     intPointer(1),
		})
	}
	cacher := &fakeImageCacher{result: &CacheImageResult{BasePath: "tvdb/series/1/seasons/1/episodes/1/still", Ext: ".webp"}}
	processor := NewImageCacheProcessor(jobs, cacher, &fakeImageResolver{url: "https://artworks.thetvdb.com/episode.jpg"}, nil, &fakeEpisodeStillUpdater{updated: true})
	cacher.after = func() { processor.SetEnabled(false) }
	stats, err := processor.RunOnce(context.Background(), "test-worker", 4, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Succeeded != 1 || len(cacher.reqs) != 1 {
		t.Fatalf("stats=%+v requests=%d, want one in-flight job to finish", stats, len(cacher.reqs))
	}
	if got := jobs.requeuedIDs; len(got) != 3 || got[0] != 2 || got[1] != 3 || got[2] != 4 {
		t.Fatalf("requeued IDs = %#v, want [2 3 4]", got)
	}
}

func TestImageCacheProcessorSerializesDrainAndBackfill(t *testing.T) {
	jobs := &serializingImageCacheJobs{
		firstEntered:  make(chan struct{}),
		secondEntered: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	processor := NewImageCacheProcessor(jobs, &fakeImageCacher{}, &fakeImageResolver{}, nil, nil)
	firstDone := make(chan error, 1)
	go func() {
		_, err := processor.DrainUntilIdle(context.Background(), "drain-worker", 1000, 2, time.Minute, nil)
		firstDone <- err
	}()
	<-jobs.firstEntered

	secondDone := make(chan error, 1)
	go func() {
		_, err := processor.RunUntilIdle(context.Background(), "backfill-worker", 1000, 2, 0, nil)
		secondDone <- err
	}()

	enteredBeforeRelease := false
	select {
	case <-jobs.secondEntered:
		enteredBeforeRelease = true
	case <-time.After(50 * time.Millisecond):
	}
	close(jobs.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("DrainUntilIdle() error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("RunUntilIdle() error = %v", err)
	}
	if enteredBeforeRelease {
		t.Fatal("manual backfill entered the queue while scheduled drain still held the processor run lock")
	}
}

func TestImageCacheProcessorCancelsWhileWaitingForRunGate(t *testing.T) {
	jobs := &serializingImageCacheJobs{
		firstEntered:  make(chan struct{}),
		secondEntered: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
	}
	processor := NewImageCacheProcessor(jobs, &fakeImageCacher{}, &fakeImageResolver{}, nil, nil)
	firstDone := make(chan error, 1)
	go func() {
		_, err := processor.DrainUntilIdle(context.Background(), "drain-worker", 2, 2, time.Minute, nil)
		firstDone <- err
	}()
	<-jobs.firstEntered

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := processor.RunUntilIdle(ctx, "backfill-worker", 2, 2, 0, nil)
		secondDone <- err
	}()
	cancel()
	if err := <-secondDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunUntilIdle() waiting error = %v, want context.Canceled", err)
	}
	close(jobs.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("DrainUntilIdle() error = %v", err)
	}
}

func TestImageCacheProcessorSkipsWhenTargetSourceChanged(t *testing.T) {
	// A stale job whose target no longer references its source must not upload.
	changed := "tmdb://poster/new.jpg"
	jobs := &fakeImageCacheJobs{
		claimed: []*models.MetadataImageCacheJob{{
			ID:                40,
			TargetType:        ImageCacheTargetItem,
			TargetContentID:   "series-1",
			SourcePath:        "tmdb://poster/old.jpg",
			ProviderID:        "tmdb",
			ProviderContentID: "1396",
			ContentType:       "series",
			ImageType:         ImageCacheImagePoster,
		}},
		currentSource: &changed,
	}
	cacher := &fakeImageCacher{result: &CacheImageResult{BasePath: "tmdb/series/1396/poster", Ext: ".webp"}}
	resolver := &fakeImageResolver{url: "https://image.tmdb.org/t/p/original/poster.jpg"}
	items := &fakeItemArtworkUpdater{updated: true}

	processor := NewImageCacheProcessorWithTargets(jobs, cacher, resolver, ImageCacheProcessorTargets{Items: items})
	stats, err := processor.RunOnce(context.Background(), "test-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", stats.Skipped)
	}
	if len(cacher.reqs) != 0 {
		t.Fatalf("CacheImage called %d times, want 0 (stale job must not upload)", len(cacher.reqs))
	}
	if items.cachedPath != "" {
		t.Fatalf("item updater called with cachedPath = %q, want none", items.cachedPath)
	}
	if jobs.succeededID != 40 {
		t.Fatalf("succeededID = %d, want 40", jobs.succeededID)
	}
}

func repairJobFixture(id int64) *models.MetadataImageCacheJob {
	return &models.MetadataImageCacheJob{
		ID:                id,
		TargetType:        ImageCacheTargetItem,
		TargetContentID:   "movie-1",
		SourcePath:        "tmdb://poster/movie.jpg",
		ProviderID:        "tmdb",
		ProviderContentID: "603",
		ContentType:       "movie",
		ImageType:         ImageCacheImagePoster,
		RepairRequested:   true,
	}
}

func repairOnlyProcessorFixture(t *testing.T, batches ...[]*models.MetadataImageCacheJob) (*ImageCacheProcessor, *repairOnlyImageCacheJobs, *fakeItemArtworkUpdater) {
	t.Helper()
	jobs := &repairOnlyImageCacheJobs{repairBatches: batches}
	cacher := &fakeImageCacher{result: &CacheImageResult{
		BasePath: "tmdb/movie/603/poster", Ext: ".webp", Thumbhash: "thumb",
	}}
	items := &fakeItemArtworkUpdater{updated: true}
	processor := NewImageCacheProcessorWithTargets(jobs, cacher,
		&fakeImageResolver{url: "https://image.tmdb.org/t/p/original/movie.jpg"},
		ImageCacheProcessorTargets{Items: items})
	// artwork.remote_materialization=passthrough.
	processor.SetEnabled(false)
	return processor, jobs, items
}

// A store switched to passthrough still has to finish an authoritative-empty
// rebuild, and that rebuild blocks until the repair jobs it enqueued drain. A
// disabled processor therefore claims repair work — and only repair work.
func TestDisabledImageCacheProcessorRunsRepairJobsOnly(t *testing.T) {
	processor, jobs, items := repairOnlyProcessorFixture(t, []*models.MetadataImageCacheJob{repairJobFixture(71)})

	stats, err := processor.RunOnce(context.Background(), "repair-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Succeeded != 1 || stats.Claimed != 1 {
		t.Fatalf("stats = %+v, want one claimed and succeeded repair", stats)
	}
	if jobs.ordinaryClaims != 0 {
		t.Fatalf("ordinary claims = %d, want 0 while materialization is off", jobs.ordinaryClaims)
	}
	if jobs.succeededID != 71 {
		t.Fatalf("succeededID = %d, want 71", jobs.succeededID)
	}
	if len(jobs.requeuedIDs) != 0 {
		t.Fatalf("requeued = %v, want the repair job processed rather than handed back", jobs.requeuedIDs)
	}
	if items.cachedPath != "tmdb/movie/603/poster/original.webp" {
		t.Fatalf("cachedPath = %q, want the repaired revision published", items.cachedPath)
	}
}

// Without a repair-scoped claim the disabled processor must stay fully off:
// nothing else in the queue may be materialized behind passthrough's back.
func TestDisabledImageCacheProcessorClaimsNothingWithoutRepairSupport(t *testing.T) {
	jobs := &fakeImageCacheJobs{claimed: []*models.MetadataImageCacheJob{repairJobFixture(72)}}
	cacher := &fakeImageCacher{result: &CacheImageResult{BasePath: "tmdb/movie/603/poster", Ext: ".webp"}}
	processor := NewImageCacheProcessorWithTargets(jobs, cacher, &fakeImageResolver{},
		ImageCacheProcessorTargets{Items: &fakeItemArtworkUpdater{updated: true}})
	processor.SetEnabled(false)

	stats, err := processor.RunOnce(context.Background(), "repair-worker", 10, 1)
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if stats.Claimed != 0 || len(cacher.reqs) != 0 {
		t.Fatalf("stats = %+v, cache requests = %d, want a fully disabled processor", stats, len(cacher.reqs))
	}
}

// The scheduled drain is what actually clears the rebuild's outstanding jobs,
// so it must reach repairs in passthrough; the explicit backfill is
// materialization by definition and must still refuse to run.
func TestPassthroughDrainRunsRepairsButBackfillStaysDisabled(t *testing.T) {
	processor, jobs, _ := repairOnlyProcessorFixture(t, []*models.MetadataImageCacheJob{repairJobFixture(73)})

	stats, err := processor.DrainUntilIdle(context.Background(), "repair-worker", 10, 1, 0, nil)
	if err != nil {
		t.Fatalf("DrainUntilIdle() error = %v", err)
	}
	if stats.Succeeded != 1 {
		t.Fatalf("stats = %+v, want the queued repair drained", stats)
	}
	if jobs.ordinaryClaims != 0 {
		t.Fatalf("ordinary claims = %d, want 0", jobs.ordinaryClaims)
	}

	if _, err := processor.RunUntilIdle(context.Background(), "backfill-worker", 10, 1, 0, nil); !errors.Is(err, ErrImageCachingDisabled) {
		t.Fatalf("RunUntilIdle() error = %v, want ErrImageCachingDisabled", err)
	}
}

func intPointer(v int) *int {
	return &v
}
