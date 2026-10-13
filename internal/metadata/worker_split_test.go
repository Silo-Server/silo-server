package metadata

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type relinkRecordingLibraryRepo struct {
	*fakeLibraryRepo
	folderID   int
	contentIDs []string
}

func (r *relinkRecordingLibraryRepo) ReconcileRelinkedItems(_ context.Context, folderID int, contentIDs []string) (int, int, []string, error) {
	r.folderID = folderID
	r.contentIDs = append([]string(nil), contentIDs...)
	return len(contentIDs), 0, nil, nil
}

func newSplitWorkerHarness(t *testing.T, files ...*models.MediaFile) (*testHarness, *fakeSeriesQueueRepo, *MatchWorker) {
	t.Helper()
	h := newTestHarness()
	h.service.folderRepo = &fakeWorkerFolderRepo{
		folders: map[int]*models.MediaFolder{10: {ID: 10, Type: "series", Enabled: true}},
	}
	h.fileRepo.setGroupFiles(10, 1, "v1|series|example_show|2024", files...)
	h.service.hooks.ensureSeriesEpisodeLinks = func(context.Context, string) error { return nil }
	queueRepo := newFakeSeriesQueueRepo(models.SeriesRootMatchJob{
		MediaFolderID:     10,
		ObservedRootPath:  "/media/shows/Example Show",
		SampleFilePath:    files[0].FilePath,
		ObservedFileCount: len(files),
	})
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 10, 0)
	worker.SetSeriesRootClaimer(queueRepo, true)
	return h, queueRepo, worker
}

func splitWorkerFile(id int, name, contentID string) *models.MediaFile {
	return &models.MediaFile{
		ID:               id,
		MediaFolderID:    10,
		ContentID:        contentID,
		FilePath:         "/media/shows/Example Show/Season 01/" + name,
		ObservedRootPath: "/media/shows/Example Show",
		GroupKeyVersion:  1,
		ContentGroupKey:  "v1|series|example_show|2024",
		BaseTitle:        "Example Show",
		BaseType:         "series",
	}
}

// A pinned result is an unmatched split target that automatic matching must
// leave alone. The queue row completes instead of recording a match failure.
func TestProcessSeriesRootCompletesPinnedSplitTarget(t *testing.T) {
	h, queueRepo, worker := newSplitWorkerHarness(t, splitWorkerFile(1, "Example.Show.S01E01.mkv", ""))
	h.service.hooks.createOrFindSkeleton = func(context.Context, *models.MediaFile, int) (*skeletonResult, error) {
		return &skeletonResult{ContentID: "local-split-target", IsNew: true, ItemStatus: "unmatched", Type: "series"}, nil
	}
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		return &ProcessResult{ContentID: req.ContentID, Pinned: true}, nil
	}

	processed, err := worker.ProcessAllByFolderAndPathPrefix(context.Background(), 10, "/media/shows/Example Show", time.Time{})
	if err != nil {
		t.Fatalf("ProcessAllByFolderAndPathPrefix: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
	if _, ok := queueRepo.deleted["10:/media/shows/Example Show"]; !ok {
		t.Fatal("pinned split target left its queue row behind")
	}
	if msg := queueRepo.errors["10:/media/shows/Example Show"]; msg != "" {
		t.Fatalf("pinned split target recorded a match failure: %q", msg)
	}
}

// Relinking a root to a new item hands the items its files used to belong to
// to the reconciler, so their stale memberships do not wait for a full scan.
func TestProcessSeriesRootReconcilesItemsReplacedByRelink(t *testing.T) {
	h, _, worker := newSplitWorkerHarness(t,
		splitWorkerFile(1, "Example.Show.S01E01.mkv", "old-series"),
		splitWorkerFile(2, "Example.Show.S01E02.mkv", ""),
	)
	recorder := &relinkRecordingLibraryRepo{fakeLibraryRepo: newFakeLibraryRepo()}
	h.service.libraryRepo = recorder
	h.service.hooks.createOrFindSkeleton = func(context.Context, *models.MediaFile, int) (*skeletonResult, error) {
		return &skeletonResult{ContentID: "new-series", IsNew: true, ItemStatus: "pending", Type: "series"}, nil
	}
	h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
		return &ProcessResult{ContentID: req.ContentID, Updated: true}, nil
	}

	if _, err := worker.ProcessAllByFolderAndPathPrefix(context.Background(), 10, "/media/shows/Example Show", time.Time{}); err != nil {
		t.Fatalf("ProcessAllByFolderAndPathPrefix: %v", err)
	}
	if recorder.folderID != 10 || !slices.Equal(recorder.contentIDs, []string{"old-series"}) {
		t.Fatalf("reconciled folder %d items %q, want folder 10 items [old-series]", recorder.folderID, recorder.contentIDs)
	}
}
