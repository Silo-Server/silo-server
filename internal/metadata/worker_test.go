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
	for _, job := range out {
		key := fmt.Sprintf("%d:%s", job.MediaFolderID, job.ObservedRootPath)
		r.lastAttemptedAt[key] = time.Now().UTC()
	}
	return out, nil
}

func (r *fakeSeriesQueueRepo) Delete(_ context.Context, folderID int, observedRootPath string) error {
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

func (r *fakeSeriesQueueRepo) UpdateError(_ context.Context, folderID int, observedRootPath string, errText string) error {
	r.errors[fmt.Sprintf("%d:%s", folderID, observedRootPath)] = errText
	return nil
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

func (r *fakeMovieQueueRepo) Claim(_ context.Context, limit int) ([]*models.MediaFile, error) {
	r.claimCalls++
	if r.claimErr != nil {
		return nil, r.claimErr
	}
	return r.claim(limit, 0, "", time.Time{})
}

func (r *fakeMovieQueueRepo) ClaimByFolderAndPathPrefix(_ context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]*models.MediaFile, error) {
	r.scopedClaimCalls++
	return r.claim(limit, folderID, pathPrefix, attemptBefore)
}

func (r *fakeMovieQueueRepo) claim(limit int, folderID int, pathPrefix string, attemptBefore time.Time) ([]*models.MediaFile, error) {
	out := make([]*models.MediaFile, 0, len(r.files))
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
		out = append(out, &fileCopy)
		r.lastAttemptedAt[file.ID] = time.Now().UTC()
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *fakeMovieQueueRepo) Delete(_ context.Context, mediaFileID int) error {
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

func (r *fakeMovieQueueRepo) UpdateError(_ context.Context, mediaFileID int, errText string) error {
	r.errors[mediaFileID] = errText
	return nil
}

// TestWorkerProcessFile_SkeletonCreatedForNoFolderIDs verifies that the worker
// processes files under roots without folder IDs and creates skeleton items
// (no longer skipping them).
func TestWorkerProcessFile_SkeletonCreatedForNoFolderIDs(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}

	// Use the process hook to avoid needing a real provider chain.
	// We simulate a failed enrichment (no providers configured).
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		return &ProcessResult{Updated: false}, nil
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.ProcessFile(ctx, file)

	// The skeleton should have been created.
	if cid, ok := h.fileRepo.contentIDs[file.ID]; !ok || cid == "" {
		t.Fatal("expected file to be linked to a skeleton item")
	}

	// The item should exist and be marked "unmatched" (since enrichment returned Updated=false).
	cid := h.fileRepo.contentIDs[file.ID]
	item, err := h.itemRepo.GetByID(ctx, cid)
	if err != nil {
		t.Fatalf("item not found: %v", err)
	}
	if item.Status != "unmatched" {
		t.Errorf("expected status=unmatched after failed enrichment, got %q", item.Status)
	}
}

// TestWorkerProcessFile_EnrichmentFailureTransitionsToUnmatched verifies that
// when Process returns an error, the item status is set to "unmatched".
func TestWorkerProcessFile_EnrichmentFailureTransitionsToUnmatched(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/The Matrix (1999) {tmdb-603}/The.Matrix.mkv",
	}

	// Simulate enrichment error.
	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		return nil, ErrMetadataNotFound
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.ProcessFile(ctx, file)

	cid := h.fileRepo.contentIDs[file.ID]
	if cid == "" {
		t.Fatal("expected file to be linked to a skeleton item")
	}

	item, err := h.itemRepo.GetByID(ctx, cid)
	if err != nil {
		t.Fatalf("item not found: %v", err)
	}
	if item.Status != "unmatched" {
		t.Errorf("expected status=unmatched, got %q", item.Status)
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

func TestProcessUnmatched_ContinuesAfterSeriesProcessingError(t *testing.T) {
	ctx := context.Background()

	fileLister := newFakeFileRepo()
	seriesRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:    10,
		ObservedRootPath: "/media/shows/Example Show",
		SampleFilePath:   "/media/shows/Example Show/Season 01/Episode.mkv",
	})
	movieRepo := newFakeMovieQueueRepo()

	worker := NewMatchWorker(nil, fileLister, 1, 10, 0)
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
	if fileLister.claimMixedCalls != 1 {
		t.Fatalf("mixed background claim calls = %d, want 1", fileLister.claimMixedCalls)
	}
}

func TestWorkerProcessFile_TrustedExplicitIDBypassesAmbiguousSkeleton(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file := &models.MediaFile{
		ID:              1,
		MediaFolderID:   10,
		FilePath:        "/media/movies/Predator (1987)/Predator Ultimate Hunter Edition (1987) {tmdb-106}.mkv",
		GroupKeyVersion: 1,
		ContentGroupKey: "v1|movie|predator|1987",
	}
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID:   10,
		GroupKeyVersion: 1,
		ContentGroupKey: "v1|movie|predator|1987",
		BaseTitle:       "Predator",
		BaseYear:        1987,
		InferredType:    "movie",
		State:           "ambiguous",
	})

	called := false
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		called = true
		if got, want := req.Hints.TmdbID, "106"; got != want {
			t.Fatalf("Hints.TmdbID = %q, want %q", got, want)
		}
		return &ProcessResult{Updated: false}, nil
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.ProcessFile(ctx, file)

	if !called {
		t.Fatal("expected process hook to run for trusted explicit IDs")
	}
}

// TestWorkerProcessFile_LibraryMembershipForPending verifies that when a
// skeleton is created for a file (no folder IDs), a library membership
// exists immediately — before enrichment runs.
func TestWorkerProcessFile_LibraryMembershipForPending(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}

	var capturedContentID string
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		capturedContentID = req.ContentID

		// At this point the skeleton is created; verify membership exists.
		if !h.libraryRepo.hasMembership(req.ContentID, 10) {
			t.Error("expected library membership to exist before enrichment runs")
		}

		return &ProcessResult{Updated: false}, nil
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.ProcessFile(ctx, file)

	// Also verify membership after enrichment.
	if capturedContentID == "" {
		t.Fatal("expected process hook to be called with a content_id")
	}
	if !h.libraryRepo.hasMembership(capturedContentID, 10) {
		t.Error("expected library membership to persist after enrichment")
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

func TestWorkerProcessFile_MoviesLibraryEpisodeShapedMovieUsesMovieHints(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true},
		},
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
	}

	var capturedType string
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		capturedType = req.Hints.Type
		return &ProcessResult{Updated: false}, nil
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.ProcessFile(ctx, file)

	if capturedType != "movie" {
		t.Fatalf("Hints.Type = %q, want movie", capturedType)
	}
}

func TestWorkerProcessFile_MixedLibraryEpisodeShapedMovieUsesMovieHints(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "mixed", Enabled: true},
		},
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/mixed/s01e03 (2020) {imdb-tt12261772} {tmdb-588077}/s01e03 (2020).mkv",
	}

	var capturedType string
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		capturedType = req.Hints.Type
		return &ProcessResult{Updated: false}, nil
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.ProcessFile(ctx, file)

	if capturedType != "movie" {
		t.Fatalf("Hints.Type = %q, want movie", capturedType)
	}
}

func TestWorkerProcessFile_MovieDoesNotExpandScannerGroupPaths(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true},
		},
	}

	representative := &models.MediaFile{
		ID:              1,
		MediaFolderID:   10,
		FilePath:        "/media/movies/Inception (2010)/Inception.1080p.mkv",
		GroupKeyVersion: 1,
		ContentGroupKey: "v1|movie|inception|2010",
	}
	otherVariant := &models.MediaFile{
		ID:              2,
		MediaFolderID:   10,
		FilePath:        "/media/movies/Inception (2010)/Inception.2160p.mkv",
		GroupKeyVersion: 1,
		ContentGroupKey: "v1|movie|inception|2010",
	}
	h.fileRepo.setGroupFiles(10, 1, "v1|movie|inception|2010", representative, otherVariant)

	var capturedPaths []string
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		capturedPaths = append([]string(nil), req.Hints.AllGroupFilePaths...)
		return &ProcessResult{Updated: false}, nil
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.ProcessFile(ctx, representative)

	if len(capturedPaths) != 1 {
		t.Fatalf("AllGroupFilePaths len = %d, want 1", len(capturedPaths))
	}
	if capturedPaths[0] != representative.FilePath {
		t.Fatalf("AllGroupFilePaths[0] = %q, want %q", capturedPaths[0], representative.FilePath)
	}
}

func TestWorkerProcessFiles_SeriesBatchProcessesOneRepresentativePerGroup(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "series", Enabled: true},
		},
	}

	files := []*models.MediaFile{
		{
			ID:              1,
			MediaFolderID:   10,
			FilePath:        "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
			GroupKeyVersion: 1,
			ContentGroupKey: "v1|series|example_show|2024",
		},
		{
			ID:              2,
			MediaFolderID:   10,
			FilePath:        "/media/shows/Example Show/Season 01/Example.Show.S01E02.mkv",
			GroupKeyVersion: 1,
			ContentGroupKey: "v1|series|example_show|2024",
		},
	}

	processCalls := 0
	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		processCalls++
		return &ProcessResult{Updated: true}, nil
	}

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	processed := worker.processFiles(ctx, files)

	if processed != 2 {
		t.Fatalf("processed = %d, want original claimed file count 2", processed)
	}
	if processCalls != 1 {
		t.Fatalf("processCalls = %d, want 1", processCalls)
	}
}

func TestWorkerProcessAllByFolderAndPathPrefix_SeriesFolderUsesRootQueue(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "series", Enabled: true},
		},
	}

	files := []*models.MediaFile{
		{
			ID:               1,
			MediaFolderID:    10,
			FilePath:         "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
			ObservedRootPath: "/media/shows/Example Show",
			GroupKeyVersion:  1,
			ContentGroupKey:  "v1|series|example_show|2024",
		},
		{
			ID:               2,
			MediaFolderID:    10,
			FilePath:         "/media/shows/Example Show/Season 01/Example.Show.S01E02.mkv",
			ObservedRootPath: "/media/shows/Example Show",
			GroupKeyVersion:  1,
			ContentGroupKey:  "v1|series|example_show|2024",
		},
	}
	h.fileRepo.setGroupFiles(10, 1, "v1|series|example_show|2024", files...)

	processCalls := 0
	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		processCalls++
		return &ProcessResult{Updated: true}, nil
	}

	queueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  "/media/shows/Example Show",
		SampleFilePath:    files[0].FilePath,
		ObservedFileCount: 2,
	})

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(queueRepo, true)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/shows/Example Show", time.Time{})
	if err != nil {
		t.Fatalf("ProcessAllByFolderAndPathPrefix error = %v", err)
	}

	if processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}
	if processCalls != 1 {
		t.Fatalf("processCalls = %d, want 1", processCalls)
	}
	if h.fileRepo.claimUnmatchedCalls != 0 {
		t.Fatalf("claimUnmatchedCalls = %d, want 0", h.fileRepo.claimUnmatchedCalls)
	}
	if _, ok := queueRepo.deleted["10:/media/shows/Example Show"]; !ok {
		t.Fatal("expected queue row to be deleted")
	}
}

func TestWorkerProcessAllByFolderAndPathPrefix_MovieFolderUsesMovieQueue(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true},
		},
	}

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/movies/Inception (2010)/Inception.mkv",
	}
	h.fileRepo.setGroupFiles(10, 1, "movie", file)

	processCalls := 0
	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		processCalls++
		return &ProcessResult{Updated: true}, nil
	}

	seriesQueueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  "/media/movies/Inception (2010)",
		SampleFilePath:    file.FilePath,
		ObservedFileCount: 1,
	})
	movieQueueRepo := newFakeMovieQueueRepo(file)

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(seriesQueueRepo, true)
	worker.SetMovieFileClaimer(movieQueueRepo)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/movies/Inception (2010)", time.Time{})
	if err != nil {
		t.Fatalf("ProcessAllByFolderAndPathPrefix error = %v", err)
	}

	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if processCalls != 1 {
		t.Fatalf("processCalls = %d, want 1", processCalls)
	}
	if seriesQueueRepo.scopedClaimCalls != 0 {
		t.Fatalf("scopedClaimCalls = %d, want 0", seriesQueueRepo.scopedClaimCalls)
	}
	if movieQueueRepo.scopedClaimCalls == 0 {
		t.Fatal("expected movie queue to be used")
	}
	if _, ok := movieQueueRepo.deleted[file.ID]; !ok {
		t.Fatal("expected movie queue row to be deleted")
	}
	if h.fileRepo.claimNonSeriesCalls != 0 {
		t.Fatalf("claimNonSeriesCalls = %d, want 0", h.fileRepo.claimNonSeriesCalls)
	}
}

func TestWorkerProcessAllByFolderAndPathPrefix_MixedFolderDrainsSeriesAndMovieQueues(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "mixed", Enabled: true},
		},
	}

	seriesFile := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/mixed/television/Example Show/Season 01/Example.Show.S01E01.mkv",
		ObservedRootPath: "/media/mixed/television/Example Show",
		GroupKeyVersion:  1,
		ContentGroupKey:  "v1|series|example_show|2024",
		BaseTitle:        "Example Show",
		BaseType:         "series",
	}
	movieFile := &models.MediaFile{
		ID:            2,
		MediaFolderID: 10,
		FilePath:      "/media/mixed/movies/Example Movie (2024)/Example.Movie.mkv",
		BaseTitle:     "Example Movie",
		BaseType:      "movie",
	}
	h.fileRepo.setGroupFiles(10, 1, "v1|series|example_show|2024", seriesFile)
	h.fileRepo.setGroupFiles(10, 1, "v1|movie|example_movie|2024", movieFile)

	processedTypes := make([]string, 0, 2)
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		processedTypes = append(processedTypes, req.Hints.Type)
		return &ProcessResult{Updated: true}, nil
	}

	seriesQueueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  "/media/mixed/television/Example Show",
		SampleFilePath:    seriesFile.FilePath,
		ObservedFileCount: 1,
	})
	movieQueueRepo := newFakeMovieQueueRepo(movieFile)

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(seriesQueueRepo, true)
	worker.SetMovieFileClaimer(movieQueueRepo)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/mixed", time.Time{})
	if err != nil {
		t.Fatalf("ProcessAllByFolderAndPathPrefix error = %v", err)
	}

	if processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}
	if got, want := strings.Join(processedTypes, ","), "series,movie"; got != want {
		t.Fatalf("processed types = %q, want %q", got, want)
	}
	if seriesQueueRepo.scopedClaimCalls == 0 {
		t.Fatal("expected series queue to be claimed")
	}
	if movieQueueRepo.scopedClaimCalls == 0 {
		t.Fatal("expected movie queue to be claimed")
	}
	if h.fileRepo.claimMixedCalls != 1 {
		t.Fatalf("claimMixedCalls = %d, want 1", h.fileRepo.claimMixedCalls)
	}
}

func TestWorkerProcessAllByFolderAndPathPrefix_SeriesRootSkeletonErrorKeepsQueueRow(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "series", Enabled: true},
		},
	}

	file := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
		ObservedRootPath: "/media/shows/Example Show",
		GroupKeyVersion:  1,
		ContentGroupKey:  "v1|series|example_show|2024",
	}
	h.fileRepo.setGroupFiles(10, 1, "v1|series|example_show|2024", file)
	h.service.hooks.createOrFindSkeleton = func(_ context.Context, _ *models.MediaFile, _ int) (*skeletonResult, error) {
		return nil, fmt.Errorf("boom")
	}

	queueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  "/media/shows/Example Show",
		SampleFilePath:    file.FilePath,
		ObservedFileCount: 1,
	})

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(queueRepo, true)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/shows/Example Show", time.Time{})
	if err != nil {
		t.Fatalf("ProcessAllByFolderAndPathPrefix error = %v", err)
	}

	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if _, ok := queueRepo.deleted["10:/media/shows/Example Show"]; ok {
		t.Fatal("did not expect queue row to be deleted")
	}
	if queueRepo.errors["10:/media/shows/Example Show"] == "" {
		t.Fatal("expected queue error to be recorded")
	}
}

func TestWorkerProcessAllByFolderAndPathPrefix_SeriesRootEnrichmentFailureKeepsQueueRow(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "series", Enabled: true},
		},
	}

	file := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
		ObservedRootPath: "/media/shows/Example Show",
		GroupKeyVersion:  1,
		ContentGroupKey:  "v1|series|example_show|2024",
		BaseTitle:        "Example Show",
		BaseType:         "series",
	}
	h.fileRepo.setGroupFiles(10, 1, "v1|series|example_show|2024", file)
	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		return nil, ErrMetadataNotFound
	}

	queueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  "/media/shows/Example Show",
		SampleFilePath:    file.FilePath,
		ObservedFileCount: 1,
	})

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(queueRepo, true)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/shows/Example Show", time.Time{})
	if err != nil {
		t.Fatalf("ProcessAllByFolderAndPathPrefix error = %v", err)
	}

	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if _, ok := queueRepo.deleted["10:/media/shows/Example Show"]; ok {
		t.Fatal("did not expect queue row to be deleted")
	}
	if queueRepo.errors["10:/media/shows/Example Show"] == "" {
		t.Fatal("expected queue error to be recorded")
	}

	contentID := h.fileRepo.rootContent["10:/media/shows/Example Show"]
	item, err := h.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatalf("item not found: %v", err)
	}
	if item.Status != "unmatched" {
		t.Fatalf("item.Status = %q, want unmatched", item.Status)
	}
}

func TestWorkerProcessAllByFolderAndPathPrefix_MovieQueueFailureDoesNotStopLaterClaims(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true},
		},
	}

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
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true},
		},
	}

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

func TestWorkerClaimBackgroundFiles_WithMovieQueueAndTVQueueDisabledUsesGenericClaims(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
	}
	h.fileRepo.setGroupFiles(10, 0, "", file)

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetMovieFileClaimer(newFakeMovieQueueRepo())

	files, err := worker.claimBackgroundFiles(ctx)
	if err != nil {
		t.Fatalf("claimBackgroundFiles error = %v", err)
	}
	if len(files) != 1 || files[0].ID != file.ID {
		t.Fatalf("files = %#v, want [%d]", files, file.ID)
	}
	if h.fileRepo.claimMixedCalls != 0 {
		t.Fatalf("claimMixedCalls = %d, want 0", h.fileRepo.claimMixedCalls)
	}
	if h.fileRepo.claimUnmatchedCalls == 0 {
		t.Fatal("expected generic unmatched claims to be used")
	}
}

func TestWorkerClaimBackgroundFiles_WithMovieQueueAndTVQueueEnabledUsesMixedClaims(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/mixed/Movie/Example.mkv",
	}
	h.fileRepo.setGroupFiles(10, 0, "", file)

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetMovieFileClaimer(newFakeMovieQueueRepo())
	worker.SetSeriesRootClaimer(newFakeSeriesQueueRepo(), true)

	files, err := worker.claimBackgroundFiles(ctx)
	if err != nil {
		t.Fatalf("claimBackgroundFiles error = %v", err)
	}
	if len(files) != 1 || files[0].ID != file.ID {
		t.Fatalf("files = %#v, want [%d]", files, file.ID)
	}
	if h.fileRepo.claimMixedCalls == 0 {
		t.Fatal("expected mixed unmatched claims to be used")
	}
	if h.fileRepo.claimNonSeriesCalls != 0 {
		t.Fatalf("claimNonSeriesCalls = %d, want 0", h.fileRepo.claimNonSeriesCalls)
	}
	if h.fileRepo.claimUnmatchedCalls != 0 {
		t.Fatalf("claimUnmatchedCalls = %d, want 0", h.fileRepo.claimUnmatchedCalls)
	}
}

func TestWorkerClaimScopedFiles_WithMovieQueueAndTVQueueDisabledUsesScopedGenericClaims(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	file := &models.MediaFile{
		ID:            1,
		MediaFolderID: 10,
		FilePath:      "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
	}
	h.fileRepo.setGroupFiles(10, 0, "", file)

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetMovieFileClaimer(newFakeMovieQueueRepo())

	files, err := worker.claimScopedFiles(ctx, 10, "/media/shows/Example Show", time.Time{}, scopedFallbackGeneric)
	if err != nil {
		t.Fatalf("claimScopedFiles error = %v", err)
	}
	if len(files) != 1 || files[0].ID != file.ID {
		t.Fatalf("files = %#v, want [%d]", files, file.ID)
	}
	if h.fileRepo.claimMixedCalls != 0 {
		t.Fatalf("claimMixedCalls = %d, want 0", h.fileRepo.claimMixedCalls)
	}
	if h.fileRepo.claimUnmatchedCalls == 0 {
		t.Fatal("expected scoped generic unmatched claims to be used")
	}
}

func TestWorkerProcessAllByFolderAndPathPrefix_MovieQueueRetryReusesLinkedSkeleton(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true},
		},
	}

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

func TestWorkerProcessBatchByFolderAndPathPrefix_MovieQueueClaimsOnlyOncePerScan(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "movies", Enabled: true},
		},
	}

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

	attemptBefore := time.Now().UTC()
	firstProcessed, err := worker.ProcessBatchByFolderAndPathPrefix(ctx, 10, "/media/movies/Inception (2010)", attemptBefore)
	if err != nil {
		t.Fatalf("first ProcessBatchByFolderAndPathPrefix error = %v", err)
	}
	secondProcessed, err := worker.ProcessBatchByFolderAndPathPrefix(ctx, 10, "/media/movies/Inception (2010)", attemptBefore)
	if err != nil {
		t.Fatalf("second ProcessBatchByFolderAndPathPrefix error = %v", err)
	}

	if firstProcessed != 0 {
		t.Fatalf("firstProcessed = %d, want 0", firstProcessed)
	}
	if secondProcessed != 0 {
		t.Fatalf("secondProcessed = %d, want 0", secondProcessed)
	}
	if movieQueueRepo.errors[file.ID] == "" {
		t.Fatal("expected queue error to be recorded")
	}
}

// ---------------------------------------------------------------------------
// Tests for concurrent-merge ErrItemNotFound tolerance (hotfix 2026-05-27)
// ---------------------------------------------------------------------------

// TestProcessSeriesRoot_Site1_ErrItemNotFoundIsToleratedNotFailed verifies that
// when ensureSeriesEpisodeLinks returns catalog.ErrItemNotFound at Site 1
// (the "all files already linked" fast path), the batch succeeds instead of
// failing. This covers the concurrent-merge case where the source series row
// was deleted after its episodes were moved to the survivor.
func TestProcessSeriesRoot_Site1_ErrItemNotFoundIsToleratedNotFailed(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	// Need a series-type folder so queueUsageForFolder enables the series queue.
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "series", Enabled: true},
		},
	}

	// Pre-populate the item as "matched" so reusableQueuedMovieSkeleton
	// returns false (matched is not skeleton-like), bypassing the re-process
	// block and falling through directly to ensureSeriesEpisodeLinks.
	const contentID = "series-gone-after-merge"
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: contentID,
		Status:    "matched",
		Title:     "Example Show",
		Type:      "series",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	// All files are already linked — hasUnlinkedGroupFile returns false.
	file := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
		ObservedRootPath: "/media/shows/Example Show",
		GroupKeyVersion:  1,
		ContentGroupKey:  "v1|series|example_show|2024",
		ContentID:        contentID,
	}
	h.fileRepo.setGroupFiles(10, 1, "v1|series|example_show|2024", file)
	h.fileRepo.contentIDs[file.ID] = contentID

	// Hook ensureSeriesEpisodeLinks to simulate the source row being gone.
	h.service.hooks.ensureSeriesEpisodeLinks = func(_ context.Context, _ string) error {
		return fmt.Errorf("loading series item: %w", catalog.ErrItemNotFound)
	}

	queueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  "/media/shows/Example Show",
		SampleFilePath:    file.FilePath,
		ObservedFileCount: 1,
	})

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(queueRepo, true)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/shows/Example Show", time.Time{})
	if err != nil {
		t.Fatalf("expected no error for ErrItemNotFound (benign concurrent merge), got: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	// Queue row must be deleted on normal completion.
	if _, ok := queueRepo.deleted["10:/media/shows/Example Show"]; !ok {
		t.Fatal("expected queue row to be deleted on normal completion")
	}
	// No error must have been recorded in the queue.
	if queueRepo.errors["10:/media/shows/Example Show"] != "" {
		t.Fatalf("expected no queue error, got %q", queueRepo.errors["10:/media/shows/Example Show"])
	}
}

// TestProcessSeriesRoot_Site1_NonNotFoundErrorStillFails verifies that a
// genuine (non-ErrItemNotFound) error from ensureSeriesEpisodeLinks at Site 1
// still fails the batch — the fix must not swallow real errors.
func TestProcessSeriesRoot_Site1_NonNotFoundErrorStillFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "series", Enabled: true},
		},
	}

	const contentID = "series-link-failure"
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID: contentID,
		Status:    "matched",
		Title:     "Example Show",
		Type:      "series",
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
	}); err != nil {
		t.Fatalf("upsert item: %v", err)
	}

	file := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
		ObservedRootPath: "/media/shows/Example Show",
		GroupKeyVersion:  1,
		ContentGroupKey:  "v1|series|example_show|2024",
		ContentID:        contentID,
	}
	h.fileRepo.setGroupFiles(10, 1, "v1|series|example_show|2024", file)
	h.fileRepo.contentIDs[file.ID] = contentID

	// Return a genuine (non-not-found) error.
	linkErr := errors.New("database connection reset")
	h.service.hooks.ensureSeriesEpisodeLinks = func(_ context.Context, _ string) error {
		return linkErr
	}

	queueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  "/media/shows/Example Show",
		SampleFilePath:    file.FilePath,
		ObservedFileCount: 1,
	})

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(queueRepo, true)
	_, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/shows/Example Show", time.Time{})
	if err == nil {
		t.Fatal("expected error for genuine link failure, got nil")
	}
	if !strings.Contains(err.Error(), "ensuring series episode links") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestProcessSeriesRoot_Site2_ErrItemNotFoundIsToleratedNotFailed verifies that
// when ensureSeriesEpisodeLinks returns catalog.ErrItemNotFound at Site 2
// (the new-skeleton / enrichment path), the batch succeeds.
func TestProcessSeriesRoot_Site2_ErrItemNotFoundIsToleratedNotFailed(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "series", Enabled: true},
		},
	}

	// File has no ContentID yet → hasUnlinkedGroupFile returns true →
	// proceeds through createOrFindSkeleton + enrichment path (Site 2).
	file := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
		ObservedRootPath: "/media/shows/Example Show",
		GroupKeyVersion:  1,
		ContentGroupKey:  "v1|series|example_show|2024",
		BaseTitle:        "Example Show",
		BaseType:         "series",
	}
	h.fileRepo.setGroupFiles(10, 1, "v1|series|example_show|2024", file)

	const skeletonID = "skeleton-series-id"

	h.service.hooks.createOrFindSkeleton = func(_ context.Context, _ *models.MediaFile, _ int) (*skeletonResult, error) {
		return &skeletonResult{
			ContentID:  skeletonID,
			IsNew:      true,
			ItemStatus: "pending",
			Type:       "series",
		}, nil
	}
	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		return &ProcessResult{Updated: true}, nil
	}
	// Simulate the concurrent-merge: ensureSeriesEpisodeLinks finds the source gone.
	h.service.hooks.ensureSeriesEpisodeLinks = func(_ context.Context, _ string) error {
		return fmt.Errorf("loading series item: %w", catalog.ErrItemNotFound)
	}

	queueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  "/media/shows/Example Show",
		SampleFilePath:    file.FilePath,
		ObservedFileCount: 1,
	})

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(queueRepo, true)
	processed, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/shows/Example Show", time.Time{})
	if err != nil {
		t.Fatalf("expected no error for ErrItemNotFound (benign concurrent merge), got: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if _, ok := queueRepo.deleted["10:/media/shows/Example Show"]; !ok {
		t.Fatal("expected queue row to be deleted on normal completion")
	}
	if queueRepo.errors["10:/media/shows/Example Show"] != "" {
		t.Fatalf("expected no queue error, got %q", queueRepo.errors["10:/media/shows/Example Show"])
	}
}

// TestProcessSeriesRoot_Site2_NonNotFoundErrorStillFails verifies that a genuine
// error from ensureSeriesEpisodeLinks at Site 2 still fails the batch.
func TestProcessSeriesRoot_Site2_NonNotFoundErrorStillFails(t *testing.T) {
	h := newTestHarness()
	ctx := context.Background()

	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{
			10: {ID: 10, Type: "series", Enabled: true},
		},
	}

	file := &models.MediaFile{
		ID:               1,
		MediaFolderID:    10,
		FilePath:         "/media/shows/Example Show/Season 01/Example.Show.S01E01.mkv",
		ObservedRootPath: "/media/shows/Example Show",
		GroupKeyVersion:  1,
		ContentGroupKey:  "v1|series|example_show|2024",
		BaseTitle:        "Example Show",
		BaseType:         "series",
	}
	h.fileRepo.setGroupFiles(10, 1, "v1|series|example_show|2024", file)

	const skeletonID = "skeleton-series-id-2"

	h.service.hooks.createOrFindSkeleton = func(_ context.Context, _ *models.MediaFile, _ int) (*skeletonResult, error) {
		return &skeletonResult{
			ContentID:  skeletonID,
			IsNew:      true,
			ItemStatus: "pending",
			Type:       "series",
		}, nil
	}
	h.service.hooks.process = func(_ context.Context, _ ProcessRequest) (*ProcessResult, error) {
		return &ProcessResult{Updated: true}, nil
	}
	linkErr := errors.New("db timeout")
	h.service.hooks.ensureSeriesEpisodeLinks = func(_ context.Context, _ string) error {
		return linkErr
	}

	queueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  "/media/shows/Example Show",
		SampleFilePath:    file.FilePath,
		ObservedFileCount: 1,
	})

	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(queueRepo, true)
	_, err := worker.ProcessAllByFolderAndPathPrefix(ctx, 10, "/media/shows/Example Show", time.Time{})
	if err == nil {
		t.Fatal("expected error for genuine link failure at site 2, got nil")
	}
	if !strings.Contains(err.Error(), "ensuring series episode links") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
