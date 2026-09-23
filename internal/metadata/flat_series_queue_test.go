package metadata

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestStaleFlatSeriesQueueDoesNotRelinkDifferentShows(t *testing.T) {
	for _, existingContentID := range []string{"", "old-series"} {
		t.Run(existingContentID, func(t *testing.T) {
			h := newTestHarness()
			h.service.folderRepo = &fakeWorkerFolderRepo{folders: map[int]*models.MediaFolder{10: {ID: 10, Type: "series", Enabled: true, Paths: []string{"/tv/Incoming"}}}}
			files := []*models.MediaFile{
				{ID: 1, MediaFolderID: 10, FilePath: "/tv/Incoming/Show.One.S01E01.mkv", ObservedRootPath: "/tv/Incoming", ContentID: existingContentID},
				{ID: 2, MediaFolderID: 10, FilePath: "/tv/Incoming/Show.Two.S01E01.mkv", ObservedRootPath: "/tv/Incoming", ContentID: existingContentID},
			}
			h.fileRepo.setGroupFiles(10, 1, "old-flat-series", files...)
			h.service.hooks.process = func(context.Context, ProcessRequest) (*ProcessResult, error) {
				t.Fatal("stale mixed-show root reached provider matching")
				return nil, nil
			}
			job := models.SeriesRootMatchJob{MediaFolderID: 10, ObservedRootPath: "/tv/Incoming", SampleFilePath: files[0].FilePath}
			queue := newFakeSeriesQueueRepo(job)
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			worker.SetSeriesRootClaimer(queue, true)
			processed, err := worker.processSeriesRoot(t.Context(), job, &sync.Map{})
			if err != nil || processed != 0 {
				t.Fatalf("processSeriesRoot() = %d, %v", processed, err)
			}
			if queue.errors["10:/tv/Incoming"] == "" || len(queue.deleted) != 0 {
				t.Fatalf("expected retained queue failure requiring rescan: %+v", queue)
			}
			for _, file := range files {
				if got := h.fileRepo.contentIDs[file.ID]; got != "" && got != existingContentID {
					t.Fatalf("file %d was relinked to %q", file.ID, got)
				}
			}
		})
	}
}

func TestConsistentSeriesQueueDoesNotRequireRescan(t *testing.T) {
	files := []*models.MediaFile{
		{FilePath: "/tv/Show One/Season 1/Show.One.S01E01.mkv"},
		{FilePath: "/tv/Show One/Season 1/Episode.Title.S01E02.mkv"},
	}
	if seriesRootNeedsIdentityRescan(files) {
		t.Fatal("established show folder was treated as a mixed-show flat root")
	}
}

func TestStaleFlatSeriesQueueWithAnonymousSiblingRequiresRescan(t *testing.T) {
	for _, files := range [][]*models.MediaFile{
		{{FilePath: "/tv/Show.One.S01E01.mkv"}, {FilePath: "/tv/E02.mkv"}},
		{{FilePath: "/tv/E02.mkv"}, {FilePath: "/tv/Show.One.S01E01.mkv"}},
	} {
		if !seriesRootNeedsIdentityRescan(files, "/tv") {
			t.Fatalf("anonymous sibling could be relinked with a file-rooted show: %s, %s", files[0].FilePath, files[1].FilePath)
		}
	}
}

func TestFlatSeriesEpisodesShareOneProvisionalItem(t *testing.T) {
	for _, tt := range []struct {
		name       string
		groupState string
		wantShared bool
	}{
		{"resolved group", "resolved", true},
		{"ambiguous group", "ambiguous", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			const groupKey = "v1|series|example show|0000"
			files := []*models.MediaFile{
				{ID: 1, MediaFolderID: 10, FilePath: "/tv/Example.Show.S01E01.mkv", BaseTitle: "Example Show", BaseType: "series", GroupKeyVersion: 1, ContentGroupKey: groupKey},
				{ID: 2, MediaFolderID: 10, FilePath: "/tv/Example.Show.S01E02.mkv", BaseTitle: "Example Show", BaseType: "series", GroupKeyVersion: 1, ContentGroupKey: groupKey},
			}
			for _, file := range files {
				file.ObservedRootPath, file.CanonicalRootPath = file.FilePath, file.FilePath
			}
			h.fileRepo.setGroupFiles(10, 1, groupKey, files...)
			h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
				MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: groupKey,
				BaseTitle: "Example Show", InferredType: "series", State: tt.groupState,
			})
			first, err := h.service.createOrFindSkeleton(t.Context(), files[0], 10, "/tv")
			if err != nil {
				t.Fatal(err)
			}
			second, err := h.service.createOrFindSkeleton(t.Context(), files[1], 10, "/tv")
			if err != nil {
				t.Fatal(err)
			}
			if shared := first.ContentID == second.ContentID; shared != tt.wantShared {
				t.Fatalf("episodes share item = %v (%q, %q), want %v", shared, first.ContentID, second.ContentID, tt.wantShared)
			}
		})
	}
}

func TestFlatSeriesProvisionalItemConvergesAcrossNodes(t *testing.T) {
	h := newTestHarness()
	const groupKey = "v1|series|example show|0000"
	files := []*models.MediaFile{
		{ID: 1, MediaFolderID: 10, FilePath: "/tv/Example.Show.S01E01.mkv", BaseTitle: "Example Show", BaseType: "series", GroupKeyVersion: 1, ContentGroupKey: groupKey},
		{ID: 2, MediaFolderID: 10, FilePath: "/tv/Example.Show.S01E02.mkv", BaseTitle: "Example Show", BaseType: "series", GroupKeyVersion: 1, ContentGroupKey: groupKey},
	}
	for _, file := range files {
		file.ObservedRootPath, file.CanonicalRootPath = file.FilePath, file.FilePath
	}
	h.fileRepo.setGroupFiles(10, 1, groupKey, files...)
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: groupKey,
		BaseTitle: "Example Show", InferredType: "series", State: "resolved",
	})
	first, err := h.service.createOrFindSkeleton(t.Context(), files[0], 10, "/tv")
	if err != nil {
		t.Fatal(err)
	}
	// Another node matching episode 2 concurrently has not seen episode 1's
	// link yet, so it cannot reuse that item and creates its own skeleton.
	h.fileRepo.mu.Lock()
	delete(h.fileRepo.contentIDs, files[0].ID)
	h.fileRepo.mu.Unlock()
	// The first node has meanwhile matched the item.
	h.itemRepo.mu.Lock()
	h.itemRepo.items[first.ContentID].Title = "Matched Show"
	h.itemRepo.items[first.ContentID].Status = "matched"
	h.itemRepo.mu.Unlock()
	second, err := h.service.createOrFindSkeleton(t.Context(), files[1], 10, "/tv")
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentID != second.ContentID {
		t.Fatalf("concurrent episodes minted separate items %q and %q", first.ContentID, second.ContentID)
	}
	item, err := h.itemRepo.GetByID(t.Context(), first.ContentID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Matched Show" || item.Status != "matched" {
		t.Fatalf("second node reset the matched item: %+v", item)
	}
}

func TestFlatSeriesLinkFailureKeepsItemAnotherNodeLinked(t *testing.T) {
	h := newTestHarness()
	const groupKey = "v1|series|example show|0000"
	file := &models.MediaFile{ID: 1, MediaFolderID: 10, FilePath: "/tv/Example.Show.S01E01.mkv", BaseTitle: "Example Show", BaseType: "series", GroupKeyVersion: 1, ContentGroupKey: groupKey}
	file.ObservedRootPath, file.CanonicalRootPath = file.FilePath, file.FilePath
	h.fileRepo.setGroupFiles(10, 1, groupKey, file)
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: groupKey,
		BaseTitle: "Example Show", InferredType: "series", State: "resolved",
	})
	// This node wins the insert, but its link fails after another node has
	// linked its own episode to the shared item.
	h.fileRepo.updateErrors[file.ID] = errors.New("link failed")
	h.itemRepo.referenced = func(string) bool { return true }

	if _, err := h.service.createOrFindSkeleton(t.Context(), file, 10, "/tv"); err == nil {
		t.Fatal("expected the link failure to surface")
	}
	h.itemRepo.mu.Lock()
	defer h.itemRepo.mu.Unlock()
	if len(h.itemRepo.items) != 1 {
		t.Fatalf("cleanup removed the shared item: %d items left", len(h.itemRepo.items))
	}
}
