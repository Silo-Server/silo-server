package metadata

import (
	"context"
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
