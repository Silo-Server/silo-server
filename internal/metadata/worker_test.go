package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestQueueClaimSizeNeverExceedsAvailableWorkers(t *testing.T) {
	worker := NewMatchWorker(nil, nil, 8, 500, time.Second)
	if got := worker.queueClaimSize(); got != 8 {
		t.Fatalf("queue claim size = %d, want 8 workers", got)
	}

	worker.SetConcurrency(32, 4)
	if got := worker.queueClaimSize(); got != 4 {
		t.Fatalf("queue claim size = %d, want batch cap 4", got)
	}
}

type fakeWorkerFolderRepo struct {
	folders map[int]*models.MediaFolder
}

func (r *fakeWorkerFolderRepo) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	if folder, ok := r.folders[id]; ok {
		cp := *folder
		return &cp, nil
	}
	return nil, fmt.Errorf("folder not found: %d", id)
}

type fakeSeriesQueueRepo struct {
	jobs             []models.SeriesRootMatchJob
	errors           map[string]string
	deleted          map[string]struct{}
	lastAttemptedAt  map[string]time.Time
	claimCalls       int
	scopedClaimCalls int
	claimErr         error
	releasedLeases   []string
}

func newFakeSeriesQueueRepo(jobs ...models.SeriesRootMatchJob) *fakeSeriesQueueRepo {
	return &fakeSeriesQueueRepo{
		jobs:            append([]models.SeriesRootMatchJob(nil), jobs...),
		errors:          make(map[string]string),
		deleted:         make(map[string]struct{}),
		lastAttemptedAt: make(map[string]time.Time),
	}
}

func (r *fakeSeriesQueueRepo) Claim(_ context.Context, limit int) ([]models.SeriesRootMatchJob, error) {
	r.claimCalls++
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	return r.claim(limit, time.Time{})
}

func (r *fakeSeriesQueueRepo) ClaimByFolderAndPathPrefix(_ context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]models.SeriesRootMatchJob, error) {
	r.scopedClaimCalls++
	out := make([]models.SeriesRootMatchJob, 0, len(r.jobs))
	for _, job := range r.jobs {
		if job.MediaFolderID != folderID {
			continue
		}
		if pathPrefix != "" && job.SampleFilePath != pathPrefix && !strings.HasPrefix(job.SampleFilePath, pathPrefix+"/") &&
			job.ObservedRootPath != pathPrefix && !strings.HasPrefix(job.ObservedRootPath, pathPrefix+"/") {
			continue
		}
		key := fmt.Sprintf("%d:%s", job.MediaFolderID, job.ObservedRootPath)
		if claimedAt := r.lastAttemptedAt[key]; !attemptBefore.IsZero() && !claimedAt.IsZero() && !claimedAt.Before(attemptBefore) {
			continue
		}
		job.LeaseToken = "fake-series-lease"
		out = append(out, job)
		r.lastAttemptedAt[key] = time.Now().UTC()
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *fakeSeriesQueueRepo) claim(limit int, _ time.Time) ([]models.SeriesRootMatchJob, error) {
	if limit <= 0 || limit > len(r.jobs) {
		limit = len(r.jobs)
	}
	out := append([]models.SeriesRootMatchJob(nil), r.jobs[:limit]...)
	for i, job := range out {
		out[i].LeaseToken = "fake-series-lease"
		key := fmt.Sprintf("%d:%s", job.MediaFolderID, job.ObservedRootPath)
		r.lastAttemptedAt[key] = time.Now().UTC()
	}
	return out, nil
}

func (r *fakeSeriesQueueRepo) Delete(_ context.Context, folderID int, observedRootPath, _ string) error {
	r.deleted[fmt.Sprintf("%d:%s", folderID, observedRootPath)] = struct{}{}
	filtered := r.jobs[:0]
	for _, job := range r.jobs {
		if job.MediaFolderID == folderID && job.ObservedRootPath == observedRootPath {
			continue
		}
		filtered = append(filtered, job)
	}
	r.jobs = filtered
	return nil
}

func (r *fakeSeriesQueueRepo) UpdateError(_ context.Context, folderID int, observedRootPath, _ string, errText string) error {
	r.errors[fmt.Sprintf("%d:%s", folderID, observedRootPath)] = errText
	return nil
}

func (r *fakeSeriesQueueRepo) UpdateFailure(ctx context.Context, folderID int, observedRootPath, leaseToken string, failure MatchFailure) error {
	return r.UpdateError(ctx, folderID, observedRootPath, leaseToken, failure.Message)
}

func (r *fakeSeriesQueueRepo) ReleaseLease(_ context.Context, leaseToken string) (int, error) {
	r.releasedLeases = append(r.releasedLeases, leaseToken)
	return 1, nil
}

func (r *fakeSeriesQueueRepo) ListByFolder(_ context.Context, folderID int, limit int, offset int) ([]models.SeriesRootMatchQueueEntry, int, error) {
	out := make([]models.SeriesRootMatchQueueEntry, 0)
	for _, job := range r.jobs {
		if job.MediaFolderID != folderID {
			continue
		}
		out = append(out, models.SeriesRootMatchQueueEntry{
			MediaFolderID:    job.MediaFolderID,
			ObservedRootPath: job.ObservedRootPath,
		})
	}
	total := len(out)
	if offset > len(out) {
		return nil, total, nil
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, total, nil
}

func (r *fakeSeriesQueueRepo) CountByFolder(_ context.Context, folderID int) (int, error) {
	count := 0
	for _, job := range r.jobs {
		if job.MediaFolderID == folderID {
			count++
		}
	}
	return count, nil
}

type fakeMovieQueueRepo struct {
	files            []*models.MediaFile
	errors           map[int]string
	deleted          map[int]struct{}
	lastAttemptedAt  map[int]time.Time
	claimCalls       int
	scopedClaimCalls int
	claimErr         error
	releasedLeases   []string
}

func newFakeMovieQueueRepo(files ...*models.MediaFile) *fakeMovieQueueRepo {
	cp := make([]*models.MediaFile, 0, len(files))
	for _, file := range files {
		if file == nil {
			continue
		}
		fileCopy := *file
		cp = append(cp, &fileCopy)
	}
	return &fakeMovieQueueRepo{
		files:           cp,
		errors:          make(map[int]string),
		deleted:         make(map[int]struct{}),
		lastAttemptedAt: make(map[int]time.Time),
	}
}

func (r *fakeMovieQueueRepo) Claim(_ context.Context, limit int) ([]models.MovieMatchJob, error) {
	r.claimCalls++
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	return r.claim(limit, 0, "", time.Time{})
}

func (r *fakeMovieQueueRepo) ClaimByFolderAndPathPrefix(_ context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]models.MovieMatchJob, error) {
	r.scopedClaimCalls++
	return r.claim(limit, folderID, pathPrefix, attemptBefore)
}

func (r *fakeMovieQueueRepo) claim(limit int, folderID int, pathPrefix string, attemptBefore time.Time) ([]models.MovieMatchJob, error) {
	out := make([]models.MovieMatchJob, 0, len(r.files))
	for _, file := range r.files {
		if file == nil {
			continue
		}
		if folderID > 0 && file.MediaFolderID != folderID {
			continue
		}
		if pathPrefix != "" && file.FilePath != pathPrefix && !strings.HasPrefix(file.FilePath, pathPrefix+"/") {
			continue
		}
		if claimedAt := r.lastAttemptedAt[file.ID]; !attemptBefore.IsZero() && !claimedAt.IsZero() && !claimedAt.Before(attemptBefore) {
			continue
		}
		fileCopy := *file
		out = append(out, models.MovieMatchJob{File: &fileCopy, LeaseToken: "fake-movie-lease"})
		r.lastAttemptedAt[file.ID] = time.Now().UTC()
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *fakeMovieQueueRepo) Delete(_ context.Context, mediaFileID int, _ string) error {
	r.deleted[mediaFileID] = struct{}{}
	filtered := r.files[:0]
	for _, file := range r.files {
		if file != nil && file.ID == mediaFileID {
			continue
		}
		filtered = append(filtered, file)
	}
	r.files = filtered
	return nil
}

func (r *fakeMovieQueueRepo) UpdateError(_ context.Context, mediaFileID int, _ string, errText string) error {
	r.errors[mediaFileID] = errText
	return nil
}

func (r *fakeMovieQueueRepo) UpdateFailure(ctx context.Context, mediaFileID int, leaseToken string, failure MatchFailure) error {
	return r.UpdateError(ctx, mediaFileID, leaseToken, failure.Message)
}

func (r *fakeMovieQueueRepo) ReleaseLease(_ context.Context, leaseToken string) (int, error) {
	r.releasedLeases = append(r.releasedLeases, leaseToken)
	return 1, nil
}

func TestProcessQueuedMovieFiles_ReleasesUnfinishedBatchOnCancellation(t *testing.T) {
	const leaseToken = "movie-batch"
	repo := newFakeMovieQueueRepo()
	worker := NewMatchWorker(nil, nil, 2, 10, 0)
	worker.movieClaimer = repo
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	processed := worker.processQueuedMovieFiles(ctx, []models.MovieMatchJob{
		{File: &models.MediaFile{ID: 1}, LeaseToken: leaseToken},
		{File: &models.MediaFile{ID: 2}, LeaseToken: leaseToken},
	})

	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if got := repo.releasedLeases; len(got) != 1 || got[0] != leaseToken {
		t.Fatalf("released leases = %v, want [%s]", got, leaseToken)
	}
}

func TestProcessSeriesRoots_ReleasesUnfinishedBatchOnCancellation(t *testing.T) {
	const leaseToken = "series-batch"

	repo := newFakeSeriesQueueRepo()
	worker := NewMatchWorker(nil, nil, 2, 10, 0)
	worker.seriesClaimer = repo
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	processed, err := worker.processSeriesRoots(ctx, []models.SeriesRootMatchJob{
		{MediaFolderID: 1, ObservedRootPath: "/shows/One", LeaseToken: leaseToken},
		{MediaFolderID: 1, ObservedRootPath: "/shows/Two", LeaseToken: leaseToken},
	})

	if err != nil {
		t.Fatalf("process series roots: %v", err)
	}
	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if got := repo.releasedLeases; len(got) != 1 || got[0] != leaseToken {
		t.Fatalf("released leases = %v, want [%s]", got, leaseToken)
	}
}

func TestProcessUnmatched_ContinuesAfterSeriesClaimError(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	seriesRepo := newFakeSeriesQueueRepo()
	seriesRepo.claimErr = errors.New("series queue unavailable")
	movieRepo := newFakeMovieQueueRepo()

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.enableTVSeriesRootQueue = true
	worker.seriesClaimer = seriesRepo
	worker.movieClaimer = movieRepo

	worker.processUnmatched(ctx)

	if seriesRepo.claimCalls != 1 {
		t.Fatalf("series claim calls = %d, want 1", seriesRepo.claimCalls)
	}
	if movieRepo.claimCalls != 1 {
		t.Fatalf("movie claim calls = %d, want 1", movieRepo.claimCalls)
	}
	if h.fileRepo.claimMixedCalls != 1 {
		t.Fatalf("mixed background claim calls = %d, want 1", h.fileRepo.claimMixedCalls)
	}
}

func TestWorkerProcessFile_SkipsDisabledLibrary(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Enabled: false},
		},
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}

	called := false
	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		called = true
		return &ProcessResult{Updated: false}, nil
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.ProcessFile(ctx, file)

	if called {
		t.Fatal("expected matcher to skip files in disabled libraries")
	}
	if cid := h.fileRepo.contentIDs[file.ID]; cid != "" {
		t.Fatalf("expected file to remain unmatched, got content_id %q", cid)
	}
}

func TestWorkerProcessAllByFolderAndPathPrefix_MovieQueueFailureDoesNotStopLaterClaims(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	setWorkerFolder(h, "movies")

	first := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}
	second := &models.MediaFile{
		ID:            2,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Interstellar (2014)/Interstellar.mkv",
	}
	movieQueueRepo := newFakeMovieQueueRepo(first, second)

	processCalls := 0
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		processCalls++
		if req.ContentID == h.fileRepo.contentIDs[first.ID] {
			return nil, ErrMetadataNotFound
		}
		return &ProcessResult{Updated: true}, nil
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	worker.SetMovieFileClaimer(movieQueueRepo)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/movies", time.Time{})
	if err != nil {
		t.Fatalf("ProcessAllByFolderAndPathPrefix error = %v", err)
	}

	if processed != 1 {
		t.Fatalf("processed = %d, want 1 successful item", processed)
	}
	if processCalls != 2 {
		t.Fatalf("processCalls = %d, want 2", processCalls)
	}
	if _, ok := movieQueueRepo.deleted[first.ID]; ok {
		t.Fatal("did not expect failed movie queue row to be deleted")
	}
	if _, ok := movieQueueRepo.deleted[second.ID]; !ok {
		t.Fatal("expected successful movie queue row to be deleted")
	}
	if movieQueueRepo.errors[first.ID] == "" {
		t.Fatal("expected failed movie queue row to record an error")
	}
}

func TestWorkerProcessAllByFolderAndPathPrefix_MovieQueueFailureKeepsQueueRow(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	setWorkerFolder(h, "movies")

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}
	movieQueueRepo := newFakeMovieQueueRepo(file)

	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		return nil, ErrMetadataNotFound
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	worker.SetMovieFileClaimer(movieQueueRepo)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/movies/Inception (2010)", time.Time{})
	if err != nil {
		t.Fatalf("ProcessAllByFolderAndPathPrefix error = %v", err)
	}

	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if _, ok := movieQueueRepo.deleted[file.ID]; ok {
		t.Fatal("did not expect movie queue row to be deleted")
	}
	if movieQueueRepo.errors[file.ID] == "" {
		t.Fatal("expected queue error to be recorded")
	}

	contentID := h.fileRepo.contentIDs[file.ID]
	item, err := h.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatalf("item not found: %v", err)
	}
	if item.Status != "unmatched" {
		t.Fatalf("item.Status = %q, want unmatched", item.Status)
	}
}

func TestWorkerProcessAllByFolderAndPathPrefix_MovieQueueRetryReusesLinkedSkeleton(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	setWorkerFolder(h, "movies")

	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: "pending-movie",
		Status:    "pending",
		Title:     "Inception",
		Year:      2010,
		Type:      "movie",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert pending movie: %v", err)
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
		ContentID:     "pending-movie",
		BaseTitle:     "Inception",
		BaseYear:      2010,
		BaseType:      "movie",
	}
	h.fileRepo.contentIDs[file.ID] = "pending-movie"
	movieQueueRepo := newFakeMovieQueueRepo(file)

	createOrFindCalls := 0
	h.service.hooks.createOrFindSkeleton = func(_ context.Context, _ *models.MediaFile, _ int) (*skeletonResult, error) {
		createOrFindCalls++
		return nil, fmt.Errorf("unexpected skeleton creation")
	}

	processCalls := 0
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		processCalls++
		if req.ContentID != "pending-movie" {
			t.Fatalf("req.ContentID = %q, want pending-movie", req.ContentID)
		}
		return nil, ErrMetadataNotFound
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	worker.SetMovieFileClaimer(movieQueueRepo)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/movies/Inception (2010)", time.Time{})
	if err != nil {
		t.Fatalf("ProcessAllByFolderAndPathPrefix error = %v", err)
	}

	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if createOrFindCalls != 0 {
		t.Fatalf("createOrFindCalls = %d, want 0", createOrFindCalls)
	}
	if processCalls != 1 {
		t.Fatalf("processCalls = %d, want 1", processCalls)
	}
	if got := h.fileRepo.contentIDs[file.ID]; got != "pending-movie" {
		t.Fatalf("contentIDs[%d] = %q, want pending-movie", file.ID, got)
	}
	if _, ok := movieQueueRepo.deleted[file.ID]; ok {
		t.Fatal("did not expect movie queue row to be deleted")
	}
}

func TestSeriesRootRetryFailurePreservesMatchedItem(t *testing.T) {
	for _, providerError := range []bool{false, true} {
		t.Run(fmt.Sprint(providerError), func(t *testing.T) {
			h := newTestHarness()
			ctx := t.Context()
			const contentID = "series-existing-match"
			const root = "/shows/Example Show"
			setWorkerFolder(h, "series")
			if err := h.itemRepo.Upsert(ctx, &models.MediaItem{ContentID: contentID, Status: "matched", Title: "Example Show", Type: "series", TvdbID: "123"}); err != nil {
				t.Fatal(err)
			}
			file := &models.MediaFile{ID: 1, MediaFolderID: 10, FilePath: root + "/Season 01/Example.Show.S01E01.mkv", ObservedRootPath: root, GroupKeyVersion: 1, ContentGroupKey: "v1|series|example_show|0", ContentID: contentID, BaseTitle: "Example Show", BaseType: "series"}
			h.fileRepo.setGroupFiles(10, 1, file.ContentGroupKey, file)
			h.fileRepo.contentIDs[file.ID] = contentID
			called := false
			h.service.hooks.process = func(context.Context, ProcessRequest) (*ProcessResult, error) {
				called = true
				if providerError {
					return nil, ErrMetadataNotFound
				}
				return &ProcessResult{Updated: false}, nil
			}
			job := models.SeriesRootMatchJob{MediaFolderID: 10, ObservedRootPath: root, SampleFilePath: file.FilePath, ObservedFileCount: 1, RerunRequested: true}
			queue := newFakeSeriesQueueRepo(job)
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
			worker.SetSeriesRootClaimer(queue, true)
			if _, err := worker.processSeriesRoot(ctx, job, nil); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("expected provider retry")
			}
			item, err := h.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				t.Fatal(err)
			}
			if item.Status != "matched" || item.TvdbID != "123" {
				t.Fatalf("retry discarded existing match: status=%s provider=%s", item.Status, item.TvdbID)
			}
			if _, deleted := queue.deleted["10:"+root]; deleted {
				t.Fatal("failed retry must retain its queue entry")
			}
		})
	}
}

// TestRetryUnmatchedItems_SkipsItemsRekeyedSinceListing covers the scan retry
// racing the series-root matcher: an item listed as unmatched is matched and
// rekeyed (local-… to series-tvdb-…) before the retry loads it, or while the
// retry waits on providers. That item is resolved and must not be counted as
// retried or still unmatched, while genuine failures still count.
func TestRetryUnmatchedItems_SkipsItemsRekeyedSinceListing(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	const (
		rekeyed    = "local-rekeyed"
		midProcess = "local-rekeyed-mid-process"
		matched    = "local-matched"
		noMatch    = "local-placeholder"
		failing    = "local-provider-error"
		danglingNF = "local-dangling-not-found"
	)
	for _, id := range []string{midProcess, matched, noMatch, failing, danglingNF} {
		h.itemRepo.items[id] = &models.MediaItem{ContentID: id, Type: "series", Status: "unmatched"}
	}
	// rekeyed is listed but already gone from media_items, as after a
	// concurrent series-root match replaced it with series-tvdb-296762.
	h.itemRepo.items["series-tvdb-296762"] = &models.MediaItem{ContentID: "series-tvdb-296762", Type: "series", Status: "matched"}

	var processed []string
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		processed = append(processed, req.ContentID)
		switch req.ContentID {
		case rekeyed:
			return nil, fmt.Errorf("loading existing item: %w", catalog.ErrItemNotFound)
		case midProcess:
			// The matcher renames the item while this retry waits on
			// providers, and the retry then finds no match.
			h.itemRepo.mu.Lock()
			delete(h.itemRepo.items, midProcess)
			h.itemRepo.mu.Unlock()
			return &ProcessResult{Updated: false}, nil
		case matched:
			return &ProcessResult{Updated: true}, nil
		case noMatch:
			return &ProcessResult{Updated: false}, nil
		case failing:
			return nil, errors.New("provider unavailable")
		case danglingNF:
			// A not-found for some other row while the listed item still
			// exists is a real failure, not a resolved item.
			return nil, fmt.Errorf("loading parent: %w", catalog.ErrItemNotFound)
		}
		t.Fatalf("unexpected content id %q", req.ContentID)
		return nil, nil
	}

	h.itemRepo.unmatchedIDs = []string{rekeyed, midProcess, matched, noMatch, failing, danglingNF}
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)

	retried, stillUnmatched, err := worker.RetryUnmatchedItemsByFolderAndPathPrefix(ctx, 1, "/media/tv")
	if err != nil {
		t.Fatalf("RetryUnmatchedItemsByFolderAndPathPrefix: %v", err)
	}
	if len(processed) != 6 {
		t.Fatalf("processed = %v, want every listed item attempted", processed)
	}
	if retried != 4 {
		t.Errorf("retried = %d, want 4 (rekeyed items excluded)", retried)
	}
	if stillUnmatched != 3 {
		t.Errorf("stillUnmatched = %d, want 3 (placeholder, provider error, dangling not-found)", stillUnmatched)
	}
}

// setWorkerFolder registers folder 10 as an enabled library of folderType.
func setWorkerFolder(h *testHarness, folderType string) {
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{10: {ID: 10, Type: folderType, Enabled: true}},
	}
}

const exampleShowRoot = "/media/shows/Example Show"

// exampleShowRootFile returns Example Show S01E01 in folder 10, grouped under
// its observed series root.
func exampleShowRootFile() *models.MediaFile {
	return &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         exampleShowRoot + "/Season 01/Example.Show.S01E01.mkv",
		ObservedRootPath: exampleShowRoot,
		GroupKeyVersion:  1,
		ContentGroupKey:  "v1|series|example_show|2024",
	}
}

// processExampleShowRoot queues file's series root and drains it through a
// match worker scoped to that root.
func processExampleShowRoot(h *testHarness, file *models.MediaFile) (*fakeSeriesQueueRepo, int, error) {
	queueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  exampleShowRoot,
		SampleFilePath:    file.FilePath,
		ObservedFileCount: 1,
	})
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(queueRepo, true)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(context.Background(), 10, exampleShowRoot, time.Time{})
	return queueRepo, processed, err
}

// TestWorkerProcessAllByFolderAndPathPrefix_SeriesRootFailureKeepsQueueRow
// covers a series root whose skeleton creation or enrichment fails: the batch
// carries on, and the queue row stays with its error recorded.
func TestWorkerProcessAllByFolderAndPathPrefix_SeriesRootFailureKeepsQueueRow(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(h *testHarness, file *models.MediaFile)
		wantUnmatched bool
	}{
		{
			name: "skeleton error",
			setup: func(h *testHarness, _ *models.MediaFile) {
				h.service.hooks.createOrFindSkeleton = func(_ context.Context, _ *models.MediaFile, _ int) (*skeletonResult, error) {
					return nil, errors.New("boom")
				}
			},
		},
		{
			name: "enrichment failure",
			setup: func(h *testHarness, file *models.MediaFile) {
				file.BaseTitle, file.BaseType = "Example Show", "series"
				h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
					return nil, ErrMetadataNotFound
				}
			},
			wantUnmatched: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			ctx := context.Background()
			setWorkerFolder(h, "series")
			file := exampleShowRootFile()
			tt.setup(h, file)
			h.fileRepo.setGroupFiles(10, 1, "v1|series|example_show|2024", file)

			queueRepo, processed, err := processExampleShowRoot(h, file)
			if err != nil {
				t.Fatalf("ProcessAllByFolderAndPathPrefix error = %v", err)
			}
			if processed != 0 {
				t.Fatalf("processed = %d, want 0", processed)
			}
			if _, ok := queueRepo.deleted["10:"+exampleShowRoot]; ok {
				t.Fatal("did not expect queue row to be deleted")
			}
			if queueRepo.errors["10:"+exampleShowRoot] == "" {
				t.Fatal("expected queue error to be recorded")
			}
			if !tt.wantUnmatched {
				return
			}
			contentID := h.fileRepo.rootContent["10:"+exampleShowRoot]
			item, err := h.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				t.Fatalf("item not found: %v", err)
			}
			if item.Status != "unmatched" {
				t.Fatalf("item.Status = %q, want unmatched", item.Status)
			}
		})
	}
}
