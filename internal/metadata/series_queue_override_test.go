package metadata

import (
	"context"
	"slices"
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

// A root whose files already link to a provisional item is matched again from
// that item. The operator's override must reach the match request, or forcing
// a provider ID on the root can never move its files to that ID's item. The
// override is keyed by content group and does not name a root, so it reaches
// the item only when the item holds the whole group and nothing else: roots
// that share a group key on separate items may be different shows.
func TestSeriesQueuePassesGroupOverrideForLinkedProvisionalRoot(t *testing.T) {
	const (
		root      = "/tv/Example Show (2013)/Season"
		otherRoot = "/tv/Other Show (2015)/Season"
		groupKey  = "v1|series|season|0000"
	)
	file := func(id int, root, name, contentID, key string) *models.MediaFile {
		return &models.MediaFile{
			ID: id, MediaFolderID: 10, FilePath: root + "/" + name, ObservedRootPath: root,
			GroupKeyVersion: 1, ContentGroupKey: key, ContentID: contentID, BaseType: "series", BaseTitle: "Season",
		}
	}
	for _, tt := range []struct {
		name, status string
		// other is a file outside the queued root.
		other        *models.MediaFile
		lookupFails  bool
		wantOverride bool
		wantRequests int
		wantStatus   string
	}{
		{name: "unmatched item holds the whole group", status: "unmatched", wantOverride: true, wantRequests: 1, wantStatus: "unmatched"},
		{name: "ambiguous item holds the whole group", status: "ambiguous", wantOverride: true, wantRequests: 1, wantStatus: "pending"},
		{
			name: "item spans two roots of one group", status: "unmatched",
			other:        file(3, otherRoot, "Example Show - S01E03.mkv", "local-stray", groupKey),
			wantOverride: true, wantRequests: 1, wantStatus: "unmatched",
		},
		{
			name: "group shared with another item", status: "unmatched",
			other:        file(3, otherRoot, "Other Show - S01E01.mkv", "local-other", groupKey),
			wantRequests: 1, wantStatus: "unmatched",
		},
		{
			// An ambiguous root is not matched until something settles it.
			name: "ambiguous item in a shared group", status: "ambiguous",
			other:      file(3, otherRoot, "Other Show - S01E01.mkv", "local-other", groupKey),
			wantStatus: "ambiguous",
		},
		{
			name: "item also holds another group", status: "unmatched",
			other:        file(3, otherRoot, "Example Show - S01E03.mkv", "local-stray", "v1|series|example show|2013"),
			wantRequests: 1, wantStatus: "unmatched",
		},
		{name: "override lookup fails", status: "unmatched", lookupFails: true, wantStatus: "unmatched"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			h.service.folderRepo = &fakeWorkerFolderRepo{folders: map[int]*models.MediaFolder{10: {ID: 10, Type: "series", Enabled: true, Paths: []string{"/tv"}}}}
			files := []*models.MediaFile{
				file(1, root, "Example Show - S01E01.mkv", "local-stray", groupKey),
				file(2, root, "Example Show - S01E02.mkv", "local-stray", groupKey),
			}
			all := files
			if tt.other != nil {
				all = append(slices.Clone(files), tt.other)
			}
			byGroup := map[string][]*models.MediaFile{}
			for _, f := range all {
				byGroup[f.ContentGroupKey] = append(byGroup[f.ContentGroupKey], f)
				if err := h.fileRepo.UpdateContentID(t.Context(), f.ID, f.ContentID); err != nil {
					t.Fatal(err)
				}
			}
			for key, groupFiles := range byGroup {
				h.fileRepo.setGroupFiles(10, 1, key, groupFiles...)
			}
			h.itemRepo.items["local-stray"] = &models.MediaItem{ContentID: "local-stray", Title: "Season", Type: "series", Status: tt.status}
			h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
				MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: groupKey, InferredType: "series", BaseTitle: "Season", State: "resolved", OverrideSource: "none",
			})
			overrides := keyedGroupOverrideRepo{folderID: 10, version: 1, key: groupKey, override: &models.MediaGroupOverride{
				ForcedType: "series", ForcedTitle: "Example Show", ForcedYear: 2013, ForcedTvdbID: "123456",
			}}
			h.service.groupOverrideRepo = overrides
			if tt.lookupFails {
				h.service.groupOverrideRepo = failFirstGroupOverrideRepo{keyedGroupOverrideRepo: overrides, calls: new(int)}
			}
			var requests []ProcessRequest
			h.service.hooks.process = func(_ context.Context, req ProcessRequest) (*ProcessResult, error) {
				requests = append(requests, req)
				return &ProcessResult{ContentID: "series-tvdb-123456", Updated: true}, nil
			}
			h.service.hooks.ensureSeriesEpisodeLinks = func(context.Context, string) error { return nil }
			job := models.SeriesRootMatchJob{MediaFolderID: 10, ObservedRootPath: root, SampleFilePath: files[0].FilePath}
			queue := newFakeSeriesQueueRepo(job)
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			worker.SetSeriesRootClaimer(queue, true)
			processed, err := worker.processSeriesRoot(t.Context(), job, &sync.Map{})
			if err != nil {
				t.Fatalf("processSeriesRoot(): %v", err)
			}
			if tt.lookupFails {
				// The failure stays on the queue row; nothing is matched without the override.
				if processed != 0 || len(queue.errors) != 1 || len(queue.deleted) != 0 || len(requests) != 0 {
					t.Fatalf("processed=%d queue errors=%v deleted=%v requests=%d, want the lookup failure recorded and no match", processed, queue.errors, queue.deleted, len(requests))
				}
				return
			}
			if processed != len(files) || len(queue.errors) != 0 {
				t.Fatalf("processSeriesRoot() = %d; queue errors=%v", processed, queue.errors)
			}
			if got := h.itemRepo.items["local-stray"].Status; got != tt.wantStatus {
				t.Fatalf("stored item status = %q, want %q", got, tt.wantStatus)
			}
			if len(requests) != tt.wantRequests {
				t.Fatalf("got %d match requests, want %d: %+v", len(requests), tt.wantRequests, requests)
			}
			if tt.wantRequests == 0 {
				return
			}
			req := requests[0]
			if req.ContentID != "local-stray" || req.Hints == nil {
				t.Fatalf("match request = %+v, want hints for local-stray", req)
			}
			overridden := req.Hints.Type == "series" && req.Hints.Title == "Example Show" && req.Hints.Year == 2013 && req.Hints.TvdbID == "123456"
			untouched := req.Hints.Title == "Season" && req.Hints.Year == 0 && req.Hints.TvdbID == ""
			if tt.wantOverride && !overridden {
				t.Fatalf("group override did not reach the match request: hints=%+v", req.Hints)
			}
			if !tt.wantOverride && !untouched {
				t.Fatalf("group override reached an item that does not hold exactly its group: hints=%+v", req.Hints)
			}
		})
	}
}
