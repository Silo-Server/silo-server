package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

func TestQueuedMovieIdentityRequiresRescanBeforeUsingStaleGroup(t *testing.T) {
	for _, tt := range []struct {
		name, path, oldTitle string
		oldYear              int
	}{
		{"numeric title", "/movies/Blade Runner 2049/Blade Runner 2049.mkv", "Blade Runner", 2049},
		{"new release suffix", "/movies/loose/Example.Movie.UHD.mkv", "Example Movie UHD", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			file := &models.MediaFile{
				ID:              1,
				MediaFolderID:   10,
				FilePath:        tt.path,
				GroupKeyVersion: 1,
				ContentGroupKey: "old-scanner-group",
				BaseType:        "movie",
				BaseTitle:       tt.oldTitle,
				BaseYear:        tt.oldYear,
			}
			h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
				MediaFolderID:   10,
				GroupKeyVersion: 1,
				ContentGroupKey: file.ContentGroupKey,
				BaseTitle:       tt.oldTitle,
				BaseYear:        tt.oldYear,
				InferredType:    "movie",
				State:           "resolved",
				OverrideSource:  "none",
			})
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			skeleton, _, err := worker.queuedMovieSkeleton(t.Context(), file, false)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "rescan") {
				t.Fatalf("stale scan identity needs a rescan before matching: skeleton=%+v, error=%v", skeleton, err)
			}
			if len(h.itemRepo.items) != 0 || len(h.fileRepo.contentIDs) != 0 {
				t.Fatal("stale identity created or linked a catalog item before rescan")
			}
		})
	}
}

func TestQueuedMovieIdentityPreservesManualGroupOverride(t *testing.T) {
	h := newTestHarness()
	file := &models.MediaFile{
		ID:              1,
		MediaFolderID:   10,
		FilePath:        "/movies/Blade Runner 2049/Blade Runner 2049.mkv",
		GroupKeyVersion: 1,
		ContentGroupKey: "manual-group",
		BaseType:        "movie",
		BaseTitle:       "Manual selection",
		BaseYear:        1987,
	}
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID:   10,
		GroupKeyVersion: 1,
		ContentGroupKey: file.ContentGroupKey,
		BaseTitle:       file.BaseTitle,
		BaseYear:        file.BaseYear,
		InferredType:    "movie",
		State:           "resolved",
		OverrideSource:  "manual",
	})
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	skeleton, _, err := worker.queuedMovieSkeleton(t.Context(), file, false)
	if err != nil || skeleton == nil || skeleton.Title != file.BaseTitle || skeleton.Year != file.BaseYear {
		t.Fatalf("manual group identity was replaced or blocked: skeleton=%+v, error=%v", skeleton, err)
	}
}

type queuedIdentityGroupOverrideRepo struct {
	override *models.MediaGroupOverride
}

func (r queuedIdentityGroupOverrideRepo) Get(context.Context, int, int, string) (*models.MediaGroupOverride, error) {
	return r.override, nil
}

// keyedGroupOverrideRepo holds one override and returns it only for its own
// folder, group key version and content group key.
type keyedGroupOverrideRepo struct {
	folderID, version int
	key               string
	override          *models.MediaGroupOverride
}

func (r keyedGroupOverrideRepo) Get(_ context.Context, folderID, version int, key string) (*models.MediaGroupOverride, error) {
	if folderID != r.folderID || version != r.version || key != r.key {
		return nil, nil
	}
	return r.override, nil
}

// failFirstGroupOverrideRepo fails its first lookup and answers the later
// ones, so a caller that drops the first error is caught by what it does next.
type failFirstGroupOverrideRepo struct {
	keyedGroupOverrideRepo
	calls *int
}

func (r failFirstGroupOverrideRepo) Get(ctx context.Context, folderID, version int, key string) (*models.MediaGroupOverride, error) {
	*r.calls++
	if *r.calls == 1 {
		return nil, errors.New("lookup failed")
	}
	return r.keyedGroupOverrideRepo.Get(ctx, folderID, version, key)
}

func TestQueuedMovieIdentityPreservesActiveGroupOverride(t *testing.T) {
	h := newTestHarness()
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: "/movies/Blade Runner 2049/Blade Runner 2049.mkv",
		GroupKeyVersion: 1, ContentGroupKey: "old-group", BaseType: "movie", BaseTitle: "Blade Runner", BaseYear: 2049,
	}
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: file.ContentGroupKey,
		BaseTitle: file.BaseTitle, BaseYear: file.BaseYear, InferredType: "movie", State: "resolved", OverrideSource: "none",
	})
	h.service.groupOverrideRepo = queuedIdentityGroupOverrideRepo{override: &models.MediaGroupOverride{
		ForcedTitle: "Manual selection", ForcedYear: 1987, ForcedType: "movie",
	}}
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	skeleton, _, err := worker.queuedMovieSkeleton(t.Context(), file, false)
	if err != nil || skeleton == nil || skeleton.Title != "Manual selection" || skeleton.Year != 1987 {
		t.Fatalf("active group override was replaced or blocked: skeleton=%+v error=%v", skeleton, err)
	}
}

func TestQueuedMovieIdentityPreservesCurrentProviderAnchor(t *testing.T) {
	h := newTestHarness()
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: "/movies/Blade Runner 2049/Blade Runner 2049 {tmdb-335984}.mkv",
		GroupKeyVersion: 1, ContentGroupKey: "provider-group", BaseType: "movie", BaseTitle: "Blade Runner", BaseYear: 2049,
	}
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: file.ContentGroupKey,
		BaseTitle: file.BaseTitle, BaseYear: file.BaseYear, InferredType: "movie", State: "resolved", OverrideSource: "none",
		TmdbID: "335984",
	})
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	skeleton, _, err := worker.queuedMovieSkeleton(t.Context(), file, false)
	if err != nil || skeleton == nil || skeleton.TmdbID != "335984" {
		t.Fatalf("explicit provider anchor was blocked: skeleton=%+v error=%v", skeleton, err)
	}
}

func TestScannedGroupIdentityComparisonIgnoresPresentationVariants(t *testing.T) {
	group := &models.ScannedMediaGroup{BaseTitle: "Example Movie", BaseYear: 2020, InferredType: "movie"}
	for _, title := range []string{"Example.Movie", "Example Movie Extended Edition", "Example Movie Director's Cut"} {
		file := &models.MediaFile{
			BaseTitle: title, BaseYear: 2020, BaseType: "movie",
			FilePath: "/movies/Example Movie (2020)/1080p/" + title + " (2020).mkv", CanonicalRootPath: "/movies/Example Movie (2020)",
		}
		if scannedGroupIdentityChanged(group, file, nil) {
			t.Errorf("presentation variant %q requires a rescan", title)
		}
	}
	if !scannedGroupIdentityChanged(group, &models.MediaFile{BaseTitle: group.BaseTitle, BaseYear: 2020, BaseType: "series"}, nil) {
		t.Fatal("changed content type did not require a rescan")
	}
	if !scannedGroupIdentityChanged(group, &models.MediaFile{BaseTitle: group.BaseTitle, BaseType: "series"}, &naming.FolderIDHints{TmdbID: "123"}) {
		t.Fatal("provider ID hid changed content type")
	}
}

func TestScannedGroupIdentityComparisonPreservesEquivalentGroupKey(t *testing.T) {
	group := &models.ScannedMediaGroup{
		BaseTitle: "The Example Movie", BaseYear: 2020, InferredType: "movie", ContentGroupKey: "v1|movie|example movie|2020",
	}
	file := &models.MediaFile{
		BaseTitle: "Example Movie", BaseYear: 2020, BaseType: "movie",
		FilePath: "/movies/Example Movie (2020)/Example.Movie.2020.mkv", CanonicalRootPath: "/movies/Example Movie (2020)",
	}
	if scannedGroupIdentityChanged(group, file, nil) {
		t.Fatal("equivalent title within the same scanner group requires a rescan")
	}
}

func TestQueuedMovieIdentityRequiresRescanForChangedProviderAnchor(t *testing.T) {
	h := newTestHarness()
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: "/movies/Example Movie (2020)/Example Movie (2020) {tmdb-222}.mkv",
		GroupKeyVersion: 1, ContentGroupKey: "v1|movie|anchor|tmdb-111", BaseType: "movie", BaseTitle: "Example Movie", BaseYear: 2020,
	}
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: file.ContentGroupKey,
		BaseTitle: file.BaseTitle, BaseYear: file.BaseYear, InferredType: "movie", TmdbID: "111", State: "resolved", OverrideSource: "none",
	})
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	_, _, err := worker.queuedMovieSkeleton(t.Context(), file, false)
	if err == nil || !strings.Contains(err.Error(), "rescan") {
		t.Fatalf("changed provider anchor did not require a rescan: %v", err)
	}
	if len(h.itemRepo.items) != 0 || len(h.fileRepo.contentIDs) != 0 {
		t.Fatal("changed provider anchor mutated the catalog")
	}
}

func TestLinkedMovieQueueRequiresRescanBeforeMatchingStaleGroup(t *testing.T) {
	for _, status := range []string{"pending", "unmatched", "ambiguous"} {
		t.Run(status, func(t *testing.T) {
			h := newTestHarness()
			file := &models.MediaFile{
				ID: 1, MediaFolderID: 10, FilePath: "/movies/Blade Runner 2049/Blade Runner 2049.mkv",
				ContentID: "provisional-item", GroupKeyVersion: 1, ContentGroupKey: "old-scanner-group",
				BaseType: "movie", BaseTitle: "Blade Runner", BaseYear: 2049,
			}
			h.itemRepo.items[file.ContentID] = &models.MediaItem{ContentID: file.ContentID, Title: file.BaseTitle, Year: file.BaseYear, Type: "movie", Status: status}
			h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
				MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: file.ContentGroupKey,
				BaseTitle: file.BaseTitle, BaseYear: file.BaseYear, InferredType: "movie", State: "resolved",
			})
			h.service.hooks.process = func(context.Context, ProcessRequest) (*ProcessResult, error) {
				t.Fatal("stale linked group reached provider matching")
				return nil, nil
			}
			queue := newFakeMovieQueueRepo(file)
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			worker.SetMovieFileClaimer(queue)
			if worker.processQueuedMovieFile(t.Context(), models.MovieMatchJob{File: file}, &sync.Map{}) {
				t.Fatal("stale linked group was processed")
			}
			if !strings.Contains(queue.errors[file.ID], "rescan") || len(queue.deleted) != 0 {
				t.Fatalf("expected retained queue failure requiring rescan: %+v", queue)
			}
			if h.itemRepo.items[file.ContentID].Status != status || len(h.fileRepo.contentIDs) != 0 {
				t.Fatal("stale linked group changed its catalog identity")
			}
		})
	}
}

func TestReusedGroupIdentityRetainsAcceptedAndEquivalentIdentities(t *testing.T) {
	for _, mode := range []string{"matched", "manual snapshot", "active override", "same title", "provider anchor"} {
		t.Run(mode, func(t *testing.T) {
			h := newTestHarness()
			file := &models.MediaFile{
				ID: 1, MediaFolderID: 10, FilePath: "/movies/Blade Runner 2049/Blade Runner 2049.mkv",
				ContentID: "existing-item", GroupKeyVersion: 1, ContentGroupKey: "old-scanner-group",
				BaseType: "movie", BaseTitle: "Blade Runner", BaseYear: 2049,
			}
			item := &models.MediaItem{ContentID: file.ContentID, Title: file.BaseTitle, Year: file.BaseYear, Type: "movie", Status: "unmatched"}
			group := &models.ScannedMediaGroup{
				MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: file.ContentGroupKey,
				BaseTitle: file.BaseTitle, BaseYear: file.BaseYear, InferredType: "movie", State: "resolved",
			}
			switch mode {
			case "matched":
				item.Status = "matched"
			case "manual snapshot":
				group.OverrideSource = "manual"
			case "active override":
				h.service.groupOverrideRepo = queuedIdentityGroupOverrideRepo{override: &models.MediaGroupOverride{ForcedTitle: "Manual selection", ForcedType: "movie"}}
			case "same title":
				group.BaseTitle, group.BaseYear = "Blade Runner 2049", 0
			case "provider anchor":
				file.FilePath = "/movies/Blade Runner 2049/Blade Runner 2049 {tmdb-335984}.mkv"
				group.TmdbID = "335984"
			}
			h.itemRepo.items[file.ContentID] = item
			h.scannedGroupRepo.setGroup(group)
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			skeleton, reused, err := worker.queuedMovieSkeleton(t.Context(), file, true)
			if err != nil || !reused || skeleton.ContentID != file.ContentID {
				t.Fatalf("accepted identity was blocked: skeleton=%+v reused=%v err=%v", skeleton, reused, err)
			}
		})
	}
}

func TestLinkedSeriesQueueRequiresRescanBeforeMatchingStaleGroup(t *testing.T) {
	h := newTestHarness()
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: "/tv/Example Show/Season 1/Example.Show.S01E01.mkv",
		ObservedRootPath: "/tv/Example Show", ContentID: "provisional-series", GroupKeyVersion: 1, ContentGroupKey: "old-series-group",
		BaseType: "series", BaseTitle: "Old Show",
	}
	h.itemRepo.items[file.ContentID] = &models.MediaItem{ContentID: file.ContentID, Title: file.BaseTitle, Type: "series", Status: "unmatched"}
	h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
		MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: file.ContentGroupKey,
		BaseTitle: file.BaseTitle, InferredType: "series", State: "resolved",
	})
	h.fileRepo.setGroupFiles(10, 1, file.ContentGroupKey, file)
	h.service.hooks.process = func(context.Context, ProcessRequest) (*ProcessResult, error) {
		t.Fatal("stale linked series reached provider matching")
		return nil, nil
	}
	job := models.SeriesRootMatchJob{MediaFolderID: 10, ObservedRootPath: file.ObservedRootPath}
	queue := newFakeSeriesQueueRepo(job)
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	worker.SetSeriesRootClaimer(queue, true)
	// The row records the rescan requirement without failing the batch: a
	// returned error would cancel sibling jobs and the scan itself.
	processed, err := worker.processSeriesRoot(t.Context(), job, &sync.Map{})
	queueErr := queue.errors[fmt.Sprintf("%d:%s", job.MediaFolderID, job.ObservedRootPath)]
	if err != nil || !strings.Contains(queueErr, "rescan") || processed != 0 || len(queue.deleted) != 0 {
		t.Fatalf("stale series did not retain rescan error: processed=%d err=%v queue=%+v", processed, err, queue)
	}
}

// A file already linked to a provisional item is re-evaluated from that item,
// not through createOrFindSkeleton, and must get the same override. An item
// that is already matched keeps its own identity.
func TestQueuedIdentityAppliesGroupOverrideToLinkedProvisionalItem(t *testing.T) {
	for _, tt := range []struct {
		status, wantStatus string
		wantOverride       bool
	}{
		{"unmatched", "unmatched", true},
		{"pending", "pending", true},
		{"ambiguous", "pending", true},
		{"matched", "matched", false},
	} {
		t.Run(tt.status, func(t *testing.T) {
			h := newTestHarness()
			file := &models.MediaFile{
				ID: 1, MediaFolderID: 10, FilePath: "/movies/Old Name (1999)/Old Name (1999).mkv",
				GroupKeyVersion: 1, ContentGroupKey: "linked-group", ContentID: "local-linked",
				BaseType: "movie", BaseTitle: "Old Name", BaseYear: 1999,
			}
			h.itemRepo.items[file.ContentID] = &models.MediaItem{
				ContentID: file.ContentID, Type: "movie", Title: "Old Name", Year: 1999, Status: tt.status,
			}
			h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
				MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: file.ContentGroupKey,
				BaseTitle: file.BaseTitle, BaseYear: file.BaseYear, InferredType: "movie", State: "resolved", OverrideSource: "none",
			})
			h.service.groupOverrideRepo = keyedGroupOverrideRepo{folderID: 10, version: 1, key: file.ContentGroupKey, override: &models.MediaGroupOverride{
				ForcedTitle: "Manual selection", ForcedYear: 1987, ForcedType: "movie", ForcedTmdbID: "335984",
			}}
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			// A rerun also reuses a matched item.
			skeleton, reused, err := worker.queuedMovieSkeleton(t.Context(), file, true)
			if err != nil || !reused || skeleton == nil {
				t.Fatalf("linked item was not reused: skeleton=%+v reused=%t error=%v", skeleton, reused, err)
			}
			wantTitle, wantYear, wantTmdbID := "Old Name", 1999, ""
			if tt.wantOverride {
				wantTitle, wantYear, wantTmdbID = "Manual selection", 1987, "335984"
			}
			if skeleton.ContentID != file.ContentID || skeleton.Title != wantTitle || skeleton.Year != wantYear ||
				skeleton.TmdbID != wantTmdbID || skeleton.ItemStatus != tt.wantStatus {
				t.Fatalf("skeleton = %+v, want title %q year %d tmdb %q status %q", skeleton, wantTitle, wantYear, wantTmdbID, tt.wantStatus)
			}
			// The match merges into the stored item, so a settled status is stored too.
			if got := h.itemRepo.items[file.ContentID].Status; got != tt.wantStatus {
				t.Fatalf("stored item status = %q, want %q", got, tt.wantStatus)
			}
		})
	}
}

// A failed override lookup must fail the queued file, not match it without
// the override.
func TestQueuedIdentityReportsGroupOverrideLookupFailure(t *testing.T) {
	h := newTestHarness()
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: "/movies/Old Name (1999)/Old Name (1999).mkv",
		GroupKeyVersion: 1, ContentGroupKey: "linked-group", ContentID: "local-linked",
		BaseType: "movie", BaseTitle: "Old Name", BaseYear: 1999,
	}
	h.itemRepo.items[file.ContentID] = &models.MediaItem{
		ContentID: file.ContentID, Type: "movie", Title: "Old Name", Year: 1999, Status: "unmatched",
	}
	h.service.groupOverrideRepo = failFirstGroupOverrideRepo{calls: new(int), keyedGroupOverrideRepo: keyedGroupOverrideRepo{
		folderID: 10, version: 1, key: file.ContentGroupKey, override: &models.MediaGroupOverride{ForcedType: "movie", ForcedTmdbID: "335984"},
	}}
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	if skeleton, _, err := worker.queuedMovieSkeleton(t.Context(), file, false); err == nil {
		t.Fatalf("queuedMovieSkeleton() = %+v, want the lookup error", skeleton)
	}
}

func TestQueuedIdentityPathTagBeatsGroupOverrideOnLinkedProvisionalItem(t *testing.T) {
	h := newTestHarness()
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: "/movies/Old Name (1999)/Old Name (1999) {tmdb-111}.mkv",
		GroupKeyVersion: 1, ContentGroupKey: "linked-group", ContentID: "local-linked",
		BaseType: "movie", BaseTitle: "Old Name", BaseYear: 1999,
	}
	h.itemRepo.items[file.ContentID] = &models.MediaItem{
		ContentID: file.ContentID, Type: "movie", Title: "Old Name", Year: 1999, Status: "unmatched",
	}
	h.service.groupOverrideRepo = keyedGroupOverrideRepo{folderID: 10, version: 1, key: file.ContentGroupKey, override: &models.MediaGroupOverride{
		ForcedType: "movie", ForcedTmdbID: "222", ForcedImdbID: "tt0000222",
	}}
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	skeleton, reused, err := worker.queuedMovieSkeleton(t.Context(), file, false)
	if err != nil || !reused || skeleton == nil {
		t.Fatalf("linked provisional item was not reused: skeleton=%+v reused=%t error=%v", skeleton, reused, err)
	}
	if skeleton.TmdbID != "111" || skeleton.ImdbID != "tt0000222" {
		t.Fatalf("want the path tag's tmdb ID and the override's imdb ID, got skeleton=%+v", skeleton)
	}
}

// Another writer can match or delete the ambiguous item between the worker's
// read and its status write. The worker must then fail the queued file rather
// than match it with the override.
func TestQueuedIdentityOverrideFailsWhenItemChangesBeforeSettling(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(items map[string]*models.MediaItem, contentID string)
	}{
		{"matched meanwhile", func(items map[string]*models.MediaItem, contentID string) { items[contentID].Status = "matched" }},
		{"deleted meanwhile", func(items map[string]*models.MediaItem, contentID string) { delete(items, contentID) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			file := &models.MediaFile{
				ID: 1, MediaFolderID: 10, FilePath: "/movies/Old Name/Old Name.mkv",
				GroupKeyVersion: 1, ContentGroupKey: "linked-group", ContentID: "local-linked",
				BaseType: "movie", BaseTitle: "Old Name",
			}
			h.itemRepo.items[file.ContentID] = &models.MediaItem{ContentID: file.ContentID, Type: "movie", Title: "Old Name", Status: "ambiguous"}
			h.service.groupOverrideRepo = keyedGroupOverrideRepo{folderID: 10, version: 1, key: file.ContentGroupKey, override: &models.MediaGroupOverride{
				ForcedType: "movie", ForcedTmdbID: "335984",
			}}
			h.service.hooks.updateItemStatus = func(ctx context.Context, contentID, status string) (bool, error) {
				h.itemRepo.mu.Lock()
				tt.change(h.itemRepo.items, contentID)
				h.itemRepo.mu.Unlock()
				return h.itemRepo.SetStatusUnlessMatched(ctx, contentID, status)
			}
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			if skeleton, _, err := worker.queuedMovieSkeleton(t.Context(), file, false); err == nil {
				t.Fatalf("queuedMovieSkeleton() = %+v, want an error", skeleton)
			}
			if item := h.itemRepo.items[file.ContentID]; item != nil && item.Status != "matched" {
				t.Fatalf("stored item status = %q, want the concurrent match kept", item.Status)
			}
		})
	}
}

// A stored status that differs from "ambiguous" only in case or spacing is
// still ambiguous, and the override must settle it.
func TestQueuedIdentityOverrideSettlesUnnormalizedAmbiguousStatus(t *testing.T) {
	h := newTestHarness()
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: "/movies/Old Name/Old Name.mkv",
		GroupKeyVersion: 1, ContentGroupKey: "linked-group", ContentID: "local-linked",
		BaseType: "movie", BaseTitle: "Old Name",
	}
	h.itemRepo.items[file.ContentID] = &models.MediaItem{ContentID: file.ContentID, Type: "movie", Title: "Old Name", Status: " Ambiguous"}
	h.service.groupOverrideRepo = keyedGroupOverrideRepo{folderID: 10, version: 1, key: file.ContentGroupKey, override: &models.MediaGroupOverride{
		ForcedType: "movie", ForcedTmdbID: "335984",
	}}
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	skeleton, reused, err := worker.queuedMovieSkeleton(t.Context(), file, false)
	if err != nil || !reused || skeleton.ItemStatus != "pending" || skeleton.TmdbID != "335984" {
		t.Fatalf("skeleton=%+v reused=%t err=%v, want the override applied and status pending", skeleton, reused, err)
	}
	if got := h.itemRepo.items[file.ContentID].Status; got != "pending" {
		t.Fatalf("stored item status = %q, want pending", got)
	}
}

// A movie group key can also be shared by items that are different films, and
// the override does not name a root, so a movie item gets it only when the item
// and the group hold the same files.
func TestQueuedMovieOverrideRequiresItemToOwnGroup(t *testing.T) {
	const groupKey = "v1|movie|halloween|0000"
	for _, tt := range []struct {
		name  string
		other *models.MediaFile
		want  bool
	}{
		{name: "item holds the whole group", want: true},
		{name: "group shared with another item", other: &models.MediaFile{
			ID: 2, MediaFolderID: 10, FilePath: "/movies/Halloween (copy)/Halloween.mkv", GroupKeyVersion: 1, ContentGroupKey: groupKey, ContentID: "local-other",
		}},
		{name: "item also holds another group", other: &models.MediaFile{
			ID: 2, MediaFolderID: 10, FilePath: "/movies/Other/Other.mkv", GroupKeyVersion: 1, ContentGroupKey: "v1|movie|other|0000", ContentID: "local-linked",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			file := &models.MediaFile{
				ID: 1, MediaFolderID: 10, FilePath: "/movies/Halloween/Halloween.mkv",
				GroupKeyVersion: 1, ContentGroupKey: groupKey, ContentID: "local-linked", BaseType: "movie", BaseTitle: "Halloween",
			}
			byGroup := map[string][]*models.MediaFile{groupKey: {file}}
			for _, f := range []*models.MediaFile{file, tt.other} {
				if f == nil {
					continue
				}
				if f != file {
					byGroup[f.ContentGroupKey] = append(byGroup[f.ContentGroupKey], f)
				}
				if err := h.fileRepo.UpdateContentID(t.Context(), f.ID, f.ContentID); err != nil {
					t.Fatal(err)
				}
			}
			for key, files := range byGroup {
				h.fileRepo.setGroupFiles(10, 1, key, files...)
			}
			h.itemRepo.items[file.ContentID] = &models.MediaItem{ContentID: file.ContentID, Type: "movie", Title: "Halloween", Status: "unmatched"}
			h.service.groupOverrideRepo = keyedGroupOverrideRepo{folderID: 10, version: 1, key: groupKey, override: &models.MediaGroupOverride{
				ForcedType: "movie", ForcedTitle: "Halloween", ForcedYear: 1978, ForcedTmdbID: "948",
			}}
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			skeleton, reused, err := worker.queuedMovieSkeleton(t.Context(), file, false)
			if err != nil || !reused {
				t.Fatalf("linked item was not reused: skeleton=%+v reused=%t err=%v", skeleton, reused, err)
			}
			if got := skeleton.TmdbID == "948"; got != tt.want {
				t.Fatalf("override applied = %t, want %t: skeleton=%+v", got, tt.want, skeleton)
			}
		})
	}
}

// fileRepoWithoutContentLister hides GetByContentID, so the worker cannot tell
// which files an item holds.
type fileRepoWithoutContentLister struct{ FileContentUpdater }

func TestQueuedOverrideFailsWithoutContentFileLister(t *testing.T) {
	h := newTestHarness()
	h.service.fileRepo = fileRepoWithoutContentLister{h.fileRepo}
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: "/movies/Old Name/Old Name.mkv",
		GroupKeyVersion: 1, ContentGroupKey: "linked-group", ContentID: "local-linked", BaseType: "movie", BaseTitle: "Old Name",
	}
	h.itemRepo.items[file.ContentID] = &models.MediaItem{ContentID: file.ContentID, Type: "movie", Title: "Old Name", Status: "unmatched"}
	h.service.groupOverrideRepo = keyedGroupOverrideRepo{folderID: 10, version: 1, key: file.ContentGroupKey, override: &models.MediaGroupOverride{
		ForcedType: "movie", ForcedTmdbID: "335984",
	}}
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	if skeleton, _, err := worker.queuedMovieSkeleton(t.Context(), file, false); err == nil {
		t.Fatalf("queuedMovieSkeleton() = %+v, want an error", skeleton)
	}
}

// A heuristic folder ID is a guess, so on first link the override's forced ID
// for the same provider wins, as it does when the worker reuses a linked item.
func TestCreateOrFindSkeletonForcedIDBeatsHeuristicFolderID(t *testing.T) {
	h := newTestHarness()
	file := &models.MediaFile{
		ID: 1, MediaFolderID: 10, FilePath: "/movies/Old Name tt0000111/Old Name.mkv",
		GroupKeyVersion: 1, ContentGroupKey: "new-group", BaseType: "movie", BaseTitle: "Old Name",
	}
	if hints := naming.ParseFolderIDs("Old Name tt0000111"); hints == nil || hints.ImdbID != "tt0000111" {
		t.Fatalf("fixture folder name does not carry a heuristic IMDb ID: %+v", hints)
	}
	h.service.groupOverrideRepo = keyedGroupOverrideRepo{folderID: 10, version: 1, key: file.ContentGroupKey, override: &models.MediaGroupOverride{
		ForcedType: "movie", ForcedImdbID: "tt0000222",
	}}
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	skeleton, reused, err := worker.queuedMovieSkeleton(t.Context(), file, false)
	if err != nil || reused || skeleton == nil {
		t.Fatalf("first link failed: skeleton=%+v reused=%t err=%v", skeleton, reused, err)
	}
	if skeleton.ImdbID != "tt0000222" {
		t.Fatalf("skeleton IMDb ID = %q, want the forced tt0000222", skeleton.ImdbID)
	}
}

// An override establishes identity regardless of how the filename parses now,
// but only when the worker applies it. When it declines because the group is
// shared with another item, a filename that changed since the scan still
// requires a rescan.
func TestQueuedMovieDeclinedOverrideStillRequiresRescan(t *testing.T) {
	const groupKey = "v1|movie|old name|1999"
	for _, tt := range []struct {
		name       string
		shared     bool
		wantRescan bool
		wantTmdbID string
	}{
		{name: "override applied", wantTmdbID: "948"},
		{name: "override declined", shared: true, wantRescan: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness()
			file := &models.MediaFile{
				ID: 1, MediaFolderID: 10, FilePath: "/movies/New Name (2001)/New Name (2001).mkv",
				GroupKeyVersion: 1, ContentGroupKey: groupKey, ContentID: "local-linked", BaseType: "movie", BaseTitle: "Old Name", BaseYear: 1999,
			}
			groupFiles := []*models.MediaFile{file}
			if tt.shared {
				groupFiles = append(groupFiles, &models.MediaFile{
					ID: 2, MediaFolderID: 10, FilePath: "/movies/Old Name (1999)/Old Name (1999).mkv", GroupKeyVersion: 1, ContentGroupKey: groupKey, ContentID: "local-other",
				})
			}
			for _, f := range groupFiles {
				if err := h.fileRepo.UpdateContentID(t.Context(), f.ID, f.ContentID); err != nil {
					t.Fatal(err)
				}
			}
			h.fileRepo.setGroupFiles(10, 1, groupKey, groupFiles...)
			h.itemRepo.items[file.ContentID] = &models.MediaItem{ContentID: file.ContentID, Type: "movie", Title: "Old Name", Year: 1999, Status: "unmatched"}
			h.scannedGroupRepo.setGroup(&models.ScannedMediaGroup{
				MediaFolderID: 10, GroupKeyVersion: 1, ContentGroupKey: groupKey,
				BaseTitle: "Old Name", BaseYear: 1999, InferredType: "movie", State: "resolved", OverrideSource: "none",
			})
			h.service.groupOverrideRepo = keyedGroupOverrideRepo{folderID: 10, version: 1, key: groupKey, override: &models.MediaGroupOverride{
				ForcedType: "movie", ForcedTmdbID: "948",
			}}
			worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
			skeleton, _, err := worker.queuedMovieSkeleton(t.Context(), file, false)
			if tt.wantRescan {
				if err == nil || !strings.Contains(err.Error(), "rescan") {
					t.Fatalf("queuedMovieSkeleton() = %+v, %v; want the rescan error", skeleton, err)
				}
				return
			}
			if err != nil || skeleton.TmdbID != tt.wantTmdbID {
				t.Fatalf("queuedMovieSkeleton() = %+v, %v; want the override applied", skeleton, err)
			}
		})
	}
}
