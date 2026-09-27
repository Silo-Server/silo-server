package metadata

import (
	"context"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSeriesQueueManualIdentityRequiresCompleteGroupCoverage(t *testing.T) {
	for _, tt := range []struct {
		name, source string
		active       bool
		partial      bool
		wantProcess  bool
	}{
		{"manual snapshot", "manual", false, false, true},
		{"active override", "none", true, false, true},
		{"manual representative only", "manual", false, true, false},
		{"active representative only", "none", true, true, false},
		{"automatic linked root", "none", false, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			h.service.folderRepo = &fakeWorkerFolderRepo{folders: map[int]*models.MediaFolder{10: {ID: 10, Type: "series", Enabled: true, Paths: []string{"/tv/Example Show"}}}}
			files := []*models.MediaFile{
				{ID: 1, MediaFolderID: 10, FilePath: "/tv/Example Show/Original.Name.S01E01.mkv", ObservedRootPath: "/tv/Example Show", GroupKeyVersion: 1, ContentGroupKey: "selected-series", ContentID: "existing-series"},
				{ID: 2, MediaFolderID: 10, FilePath: "/tv/Example Show/Alternate.Name.S01E02.mkv", ObservedRootPath: "/tv/Example Show", GroupKeyVersion: 1, ContentGroupKey: "selected-series", ContentID: "existing-series"},
			}
			if tt.partial {
				files[1].ContentGroupKey = "unrelated-series"
			}
			h.fileRepo.setGroupFiles(10, 1, "selected-series", files...)
			h.itemRepo.items["existing-series"] = &models.MediaItem{ContentID: "existing-series", Title: "Example Show", Type: "series", Status: "matched"}
			h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
				MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: "selected-series", InferredType: "series", BaseTitle: "Example Show", OverrideSource: tt.source,
			})
			if tt.active {
				h.service.groupOverrideRepo = queuedIdentityGroupOverrideRepo{override: &models.MediaGroupOverride{ForcedTitle: "Example Show", ForcedType: "series"}}
			}
			ensured := false
			h.service.hooks.ensureSeriesEpisodeLinks = func(context.Context, string) error {
				ensured = true
				return nil
			}
			job := models.SeriesRootMatchJob{MediaFolderID: 10, ObservedRootPath: "/tv/Example Show", SampleFilePath: files[0].FilePath}
			queue := newFakeSeriesQueueRepo(job)
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			worker.SetSeriesRootClaimer(queue, true)
			processed, err := worker.processSeriesRoot(t.Context(), job, &sync.Map{})
			if err != nil {
				t.Fatal(err)
			}
			wantProcessed := 0
			if tt.wantProcess {
				wantProcessed = len(files)
			}
			if ensured != tt.wantProcess || processed != wantProcessed {
				t.Fatalf("processed=%d ensured=%t, want processing=%t; queue errors=%v", processed, ensured, tt.wantProcess, queue.errors)
			}
			if !tt.wantProcess && len(queue.errors) == 0 {
				t.Fatal("uncovered conflicting identities did not require a rescan")
			}
		})
	}
}
