package metadata

import (
	"context"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

func TestSeriesQueueUsesConfiguredRootForIdentity(t *testing.T) {
	for _, tt := range []struct {
		name, path, observedRoot string
	}{
		{"flat filename", "/library/All Shows/The.X-Files.S01E01.mkv", "/library/All Shows/The.X-Files.S01E01.mkv"},
		{"episode title in show folder", "/library/All Shows/The X-Files/Pilot - S01E01.mkv", "/library/All Shows/The X-Files"},
	} {
		for _, linked := range []bool{false, true} {
			t.Run(tt.name+map[bool]string{false: "/unlinked", true: "/linked"}[linked], func(t *testing.T) {
				h := newTestHarness()
				h.service.folderRepo = &fakeWorkerFolderRepo{folders: map[int]*models.MediaFolder{10: {
					ID: 10, Type: "series", Enabled: true, Paths: []string{"/library/All Shows"},
				}}}
				file := &models.MediaFile{ID: 1, MediaFolderID: 10, FilePath: tt.path, ObservedRootPath: tt.observedRoot, CanonicalRootPath: tt.observedRoot, BaseType: "series", BaseTitle: "The X-Files"}
				if linked {
					file.ContentID = "existing-series"
				}
				h.itemRepo.items["existing-series"] = &models.MediaItem{ContentID: "existing-series", Title: "The X-Files", Type: "series", Status: "unmatched"}
				h.fileRepo.setGroupFiles(10, 1, "series", file)
				h.service.hooks.createOrFindSkeleton = func(_ context.Context, parsed *models.MediaFile, _ int) (*skeletonResult, error) {
					if parsed.BaseTitle != "The X-Files" {
						t.Errorf("parsed series title = %q", parsed.BaseTitle)
					}
					return &skeletonResult{ContentID: "existing-series", Title: parsed.BaseTitle, Type: "series", RootPath: tt.observedRoot, ObservedRootPath: tt.observedRoot, ItemStatus: "pending", IsNew: true}, nil
				}
				processedMetadata := false
				h.service.hooks.process = func(_ context.Context, request ProcessRequest) (*ProcessResult, error) {
					processedMetadata = true
					if request.Hints.Title != "The X-Files" || len(request.Hints.AlternateIdentities) != 0 {
						t.Errorf("provider identity = %q; alternatives = %+v", request.Hints.Title, request.Hints.AlternateIdentities)
					}
					return &ProcessResult{ContentID: "existing-series", Updated: true}, nil
				}
				h.service.hooks.ensureSeriesEpisodeLinks = func(context.Context, string) error { return nil }
				job := models.SeriesRootMatchJob{MediaFolderID: 10, ObservedRootPath: tt.observedRoot, SampleFilePath: tt.path}
				queue := newFakeSeriesQueueRepo(job)
				worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
				worker.SetSeriesRootClaimer(queue, true)
				count, err := worker.processSeriesRoot(t.Context(), job, &sync.Map{})
				if err != nil || count != 1 || !processedMetadata {
					t.Fatalf("count=%d metadata=%t err=%v queue errors=%v", count, processedMetadata, err, queue.errors)
				}
			})
		}
	}
}

type countedNamingFolderRepo struct {
	folder *models.MediaFolder
	calls  int
}

func (r *countedNamingFolderRepo) GetByID(context.Context, int) (*models.MediaFolder, error) {
	r.calls++
	return r.folder, nil
}

func TestMatchFolderConfigCachesNamingRootsWithEnabledState(t *testing.T) {
	h := newTestHarness()
	repo := &countedNamingFolderRepo{folder: &models.MediaFolder{ID: 10, Enabled: true, Paths: []string{"/library/All Shows"}}}
	h.service.folderRepo = repo
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	cache := &sync.Map{}
	if !worker.matchFolderConfig(t.Context(), 10, cache).enabled {
		t.Fatal("library unexpectedly disabled")
	}
	repo.folder.Paths[0] = "/different"
	config := worker.matchFolderConfig(t.Context(), 10, cache)
	if repo.calls != 1 || len(config.paths) != 1 || config.paths[0] != "/library/All Shows" {
		t.Fatalf("folder calls=%d cached roots=%v", repo.calls, config.paths)
	}
}

func TestConfiguredLibraryRootCannotSupplyEpisodeSeason(t *testing.T) {
	h := newFallbackTestHarness()
	const root = "/library/Season 1"
	const seriesID = "root-aware-series"
	h.service.folderRepo = &fakeWorkerFolderRepo{folders: map[int]*models.MediaFolder{1: {ID: 1, Enabled: true, Type: "series", Paths: []string{root}}}}
	if err := h.itemRepo.Upsert(t.Context(), &models.MediaItem{ContentID: seriesID, Type: "series", Title: "Example Show", Status: "matched"}); err != nil {
		t.Fatal(err)
	}
	for _, episode := range []*models.Episode{
		{ContentID: "season-one", SeriesID: seriesID, SeasonNumber: 1, EpisodeNumber: 2, Title: "First Arrival", MetadataSource: "provider"},
		{ContentID: "season-two", SeriesID: seriesID, SeasonNumber: 2, EpisodeNumber: 2, Title: "Second Arrival", MetadataSource: "provider"},
	} {
		if err := h.episodeRepo.Upsert(t.Context(), episode); err != nil {
			t.Fatal(err)
		}
	}
	path := root + "/Example Show E02.mkv"
	parsed := naming.ParseFilename(path, "series", root)
	if parsed.SeasonKnown || parsed.EpisodeNum != 2 {
		t.Fatalf("scanner episode hints = %+v", parsed)
	}
	file := &models.MediaFile{ID: 1, MediaFolderID: 1, FilePath: path, SeasonNumber: parsed.SeasonNum, EpisodeNumber: parsed.EpisodeNum}
	h.fileRepo.addFile(file)
	h.fileRepo.contentIDs[file.ID] = seriesID
	if err := h.service.ensureSeriesEpisodeLinks(t.Context(), seriesID); err != nil {
		t.Fatal(err)
	}
	if got := h.fileRepo.episodeLinks[file.ID]; got != "" {
		t.Fatalf("configured container supplied a season and linked %q", got)
	}
	if hints := selectLocalEpisodeCoordinateHints([]string{path}, root); len(hints) != 0 {
		t.Fatalf("configured container supplied series validation coordinates: %+v", hints)
	}
	childCtx := buildSeriesChildLocalContext(nil, []string{path}, root)
	if len(childCtx.episodeFilePaths) != 0 || len(childCtx.seasonDirectoryPaths) != 0 {
		t.Fatalf("configured container supplied season sidecar context: %+v", childCtx)
	}
}

func TestEpisodeLinkingStopsWhenLibraryContextIsUnavailable(t *testing.T) {
	h := newFallbackTestHarness()
	const seriesID = "unavailable-context-series"
	h.service.folderRepo = &fakeWorkerFolderRepo{folders: map[int]*models.MediaFolder{}}
	if err := h.itemRepo.Upsert(t.Context(), &models.MediaItem{ContentID: seriesID, Type: "series", Title: "Example Show"}); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 1, MediaFolderID: 1, FilePath: "/library/Season 1/Example Show E02.mkv"}
	h.fileRepo.addFile(file)
	h.fileRepo.contentIDs[file.ID] = seriesID
	if err := h.service.ensureSeriesEpisodeLinks(t.Context(), seriesID); err == nil {
		t.Fatal("missing library context silently fell back to path-derived season")
	}
	episodes, err := h.episodeRepo.ListBySeries(t.Context(), seriesID)
	if err != nil || len(episodes) != 0 || h.fileRepo.episodeLinks[file.ID] != "" {
		t.Fatalf("unavailable context mutated episode identity: episodes=%+v err=%v", episodes, err)
	}
}

func TestSeriesQueueRetainsJobWhenLibraryContextIsUnavailable(t *testing.T) {
	h := newTestHarness()
	h.service.folderRepo = &fakeWorkerFolderRepo{folders: map[int]*models.MediaFolder{}}
	h.service.hooks.createOrFindSkeleton = func(context.Context, *models.MediaFile, int) (*skeletonResult, error) {
		t.Fatal("missing library roots reached skeleton creation")
		return nil, nil
	}
	job := models.SeriesRootMatchJob{MediaFolderID: 10, ObservedRootPath: "/library/All Shows", SampleFilePath: "/library/All Shows/Example.Show.S01E01.mkv"}
	queue := newFakeSeriesQueueRepo(job)
	worker := NewMatchWorker(h.service, h.fileRepo, 1, 1, 0)
	worker.SetSeriesRootClaimer(queue, true)
	processed, err := worker.processSeriesRoot(t.Context(), job, &sync.Map{})
	if err != nil || processed != 0 || len(queue.deleted) != 0 {
		t.Fatalf("unavailable context consumed queue job: processed=%d err=%v deleted=%v", processed, err, queue.deleted)
	}
}

func TestEpisodeEvidenceUsesConfiguredRootForTitleSuffix(t *testing.T) {
	const root = "/library/Season 21"
	hints := selectLocalEpisodeCoordinateHints([]string{root + "/Example.Show.301-E05 - Arrival.mkv"}, root)
	if len(hints) != 1 || hints[0].SeasonNumber != 3 || hints[0].EpisodeNumber != 1 || hints[0].Title != "Arrival" {
		t.Fatalf("episode evidence used containing library as season: %+v", hints)
	}
}
