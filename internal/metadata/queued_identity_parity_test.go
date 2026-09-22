package metadata

import (
	"context"
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
