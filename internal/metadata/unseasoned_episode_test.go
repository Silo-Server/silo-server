package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestUnseasonedEpisodeResolution(t *testing.T) {
	first := &models.Episode{ContentID: "first", SeasonNumber: 1, EpisodeNumber: 2, Title: "The First Arrival", MetadataSource: "provider"}
	second := &models.Episode{ContentID: "second", SeasonNumber: 2, EpisodeNumber: 2, Title: "The Second Arrival", MetadataSource: "provider"}
	third := &models.Episode{ContentID: "third", SeasonNumber: 1, EpisodeNumber: 3, Title: "The Third Arrival", MetadataSource: "provider"}
	duplicateTitle := &models.Episode{ContentID: "duplicate", SeasonNumber: 3, EpisodeNumber: 2, Title: "The First Arrival", MetadataSource: "provider"}
	special := &models.Episode{ContentID: "special", SeasonNumber: 0, EpisodeNumber: 2, Title: "The Second Arrival", MetadataSource: "provider"}
	fallback := &models.Episode{ContentID: "fallback", SeasonNumber: 1, EpisodeNumber: 2, Title: "The First Arrival", MetadataSource: "scanner_fallback"}
	laterOnly := &models.Episode{ContentID: "later", SeasonNumber: 2, EpisodeNumber: 13, Title: "The Later Arrival", MetadataSource: "provider"}
	for _, tt := range []struct {
		name     string
		episodes []*models.Episode
		number   int
		title    string
		wantID   string
	}{
		{name: "unique provider number", episodes: []*models.Episode{first}, number: 2, wantID: "first"},
		{name: "first season number agrees with absolute order", episodes: []*models.Episode{first, laterOnly}, number: 2, wantID: "first"},
		{name: "later season number can be absolute", episodes: []*models.Episode{first, laterOnly}, number: 13},
		{name: "later season title stays exact", episodes: []*models.Episode{first, laterOnly}, number: 13, title: "The Later Arrival", wantID: "later"},
		{name: "missing first season defers number links", episodes: []*models.Episode{second}, number: 2},
		{name: "repeated number is ambiguous", episodes: []*models.Episode{first, second}, number: 2},
		{name: "title disambiguates season", episodes: []*models.Episode{first, second}, number: 2, title: "The Second Arrival", wantID: "second"},
		{name: "absolute number with exact title", episodes: []*models.Episode{first, second}, number: 136, title: "The Second Arrival", wantID: "second"},
		{name: "absolute number has no inferred order", episodes: []*models.Episode{first, second}, number: 136},
		{name: "specials are excluded", episodes: []*models.Episode{special}, number: 2},
		{name: "fallback cannot establish season", episodes: []*models.Episode{fallback}, number: 2},
		{name: "conflicting title defeats unique number", episodes: []*models.Episode{first}, number: 2, title: "The Second Arrival"},
		{name: "existing number conflicts with another title", episodes: []*models.Episode{first, third}, number: 2, title: "The Third Arrival"},
		{name: "duplicate distinctive title remains ambiguous", episodes: []*models.Episode{first, duplicateTitle}, number: 2, title: "The First Arrival"},
		{name: "short title must agree", episodes: []*models.Episode{first}, number: 2, title: "Reborn"},
		{name: "zero number cannot match title", episodes: []*models.Episode{first}, title: "The First Arrival"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := newUnseasonedEpisodeIndex(tt.episodes).resolve(tt.number, tt.title)
			if tt.wantID == "" {
				if ok || got != nil {
					t.Fatalf("resolved ambiguous input to %+v", got)
				}
				return
			}
			if !ok || got == nil || got.ContentID != tt.wantID {
				t.Fatalf("resolved episode = %+v, %v; want %s", got, ok, tt.wantID)
			}
		})
	}
}

func TestUnseasonedEpisodeLinksUseProviderEvidence(t *testing.T) {
	for _, tt := range []struct {
		name         string
		filename     string
		secondSeason bool
		specialOnly  bool
		fallbackOnly bool
		wantID       string
	}{
		{name: "unique number", filename: "E02.mkv", wantID: "first"},
		{name: "ambiguous number", filename: "E02.mkv", secondSeason: true},
		{name: "exact episode title", filename: "E02 - The Second Arrival.mkv", secondSeason: true, wantID: "second"},
		{name: "absolute title", filename: "Example Show - 136 - The Second Arrival.mkv", secondSeason: true, wantID: "second"},
		{name: "no absolute mapping", filename: "Example Show - 136.mkv", secondSeason: true},
		{name: "not specials", filename: "E02.mkv", specialOnly: true},
		{name: "not fallback", filename: "E02.mkv", fallbackOnly: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newFallbackTestHarness()
			const seriesID = "unseasoned-series"
			if err := h.itemRepo.Upsert(t.Context(), &models.MediaItem{ContentID: seriesID, Title: "Example Show", Type: "series", Status: "matched"}); err != nil {
				t.Fatal(err)
			}
			first := &models.Episode{ContentID: "first", SeriesID: seriesID, SeasonNumber: 1, EpisodeNumber: 2, Title: "The First Arrival", MetadataSource: "provider"}
			if tt.specialOnly {
				first.SeasonNumber = 0
			}
			if tt.fallbackOnly {
				first.MetadataSource = "scanner_fallback"
			}
			if err := h.episodeRepo.Upsert(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			if tt.secondSeason {
				if err := h.episodeRepo.Upsert(t.Context(), &models.Episode{ContentID: "second", SeriesID: seriesID, SeasonNumber: 2, EpisodeNumber: 2, Title: "The Second Arrival", MetadataSource: "provider"}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := h.episodeRepo.ListBySeries(t.Context(), seriesID)
			if err != nil {
				t.Fatal(err)
			}
			file := &models.MediaFile{ID: 1, MediaFolderID: 1, FilePath: "/tv/Example Show/" + tt.filename}
			h.fileRepo.addFile(file)
			h.fileRepo.contentIDs[file.ID] = seriesID
			if err := h.service.ensureSeriesEpisodeLinks(t.Context(), seriesID); err != nil {
				t.Fatal(err)
			}
			if got := h.fileRepo.episodeLinks[file.ID]; got != tt.wantID {
				t.Fatalf("episode link = %q, want %q", got, tt.wantID)
			}
			after, err := h.episodeRepo.ListBySeries(t.Context(), seriesID)
			if err != nil || len(after) != len(before) {
				t.Fatalf("unknown season synthesized episode rows: before=%d after=%d err=%v", len(before), len(after), err)
			}
			if hints := selectLocalEpisodeCoordinateHints([]string{file.FilePath}); len(hints) != 0 {
				t.Fatalf("unknown season supplied coordinate validation evidence: %+v", hints)
			}
			localCtx := buildSeriesChildLocalContext(nil, []string{file.FilePath})
			if len(localCtx.episodeFilePaths) != 0 || len(localCtx.seasonDirectoryPaths) != 0 {
				t.Fatalf("unknown season supplied specials sidecar context: %+v", localCtx)
			}
		})
	}
}

func TestUnseasonedEpisodeRetriesWhenProviderMetadataArrives(t *testing.T) {
	h := newFallbackTestHarness()
	const seriesID = "unseasoned-retry-series"
	if err := h.itemRepo.Upsert(t.Context(), &models.MediaItem{ContentID: seriesID, Title: "Example Show", Type: "series", Status: "matched"}); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 1, MediaFolderID: 1, FilePath: "/tv/Example Show/E02.mkv"}
	h.fileRepo.addFile(file)
	h.fileRepo.contentIDs[file.ID] = seriesID
	if err := h.service.ensureSeriesEpisodeLinks(t.Context(), seriesID); err != nil {
		t.Fatal(err)
	}
	if got := h.fileRepo.episodeLinks[file.ID]; got != "" {
		t.Fatalf("linked without provider evidence: %q", got)
	}
	if err := h.episodeRepo.Upsert(t.Context(), &models.Episode{ContentID: "provider-episode", SeriesID: seriesID, SeasonNumber: 1, EpisodeNumber: 2, Title: "The Arrival", MetadataSource: "provider"}); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ensureSeriesEpisodeLinks(t.Context(), seriesID); err != nil {
		t.Fatal(err)
	}
	if got := h.fileRepo.episodeLinks[file.ID]; got != "provider-episode" {
		t.Fatalf("episode link after metadata arrival = %q", got)
	}
}
