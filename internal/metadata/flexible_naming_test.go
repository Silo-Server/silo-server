package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFlexibleEpisodeNamingValidatesAndLinksEpisodes(t *testing.T) {
	for _, filePath := range []string{
		"/tv/Example Show/Example.Show.1x02.The.Arrival.1080p.WEB-DL.mkv",
		"/tv/Example Show/Example Show Season 1 Episode 2 - The Arrival.mkv",
		"/tv/Example Show/Season 1/Episode 02 - The Arrival.mkv",
		"/tv/Example Show/Season 1/02 - The Arrival.mkv",
		"/tv/Example Show/S01/E02 - The Arrival.mkv",
		"/tv/Example Show/Season 1/Example.Show.s01.e02.The.Arrival.mkv",
	} {
		t.Run(filePath, func(t *testing.T) {
			ctx := t.Context()
			h := newFallbackTestHarness()
			const seriesID = "flexible-series"
			const episodeID = "flexible-episode"
			if err := h.itemRepo.Upsert(ctx, &models.MediaItem{ContentID: seriesID, Title: "Example Show", Type: "series", Status: "matched"}); err != nil {
				t.Fatal(err)
			}
			if err := h.episodeRepo.Upsert(ctx, &models.Episode{ContentID: episodeID, SeriesID: seriesID, SeasonNumber: 1, EpisodeNumber: 2, Title: "The Arrival", MetadataSource: "provider"}); err != nil {
				t.Fatal(err)
			}
			file := &models.MediaFile{ID: 1, MediaFolderID: 1, FilePath: filePath}
			h.fileRepo.addFile(file)
			h.fileRepo.contentIDs[file.ID] = seriesID
			if err := h.service.ensureSeriesEpisodeLinks(ctx, seriesID); err != nil {
				t.Fatal(err)
			}
			if got := h.fileRepo.episodeLinks[file.ID]; got != episodeID {
				t.Fatalf("episode link = %q, want %q", got, episodeID)
			}
			if hints := selectLocalEpisodeCoordinateHints([]string{filePath}); len(hints) != 1 || hints[0].SeasonNumber != 1 || hints[0].EpisodeNumber != 2 || hints[0].Title != "The Arrival" {
				t.Fatalf("series validation evidence = %+v", hints)
			}
		})
	}
}

func TestReparseQueuedReleaseFilename(t *testing.T) {
	for _, path := range []string{
		"/movies/Example.Movie.2020.1080p.BluRay.x264-GROUP.mkv",
		"/movies/Example.Movie.2020.1080p.BluRay/title00.mkv",
	} {
		file := &models.MediaFile{FilePath: path, BaseTitle: "old parser title", BaseType: "movie"}
		current := reparseQueuedFileIdentity(file, "movies")
		if current.BaseTitle != "Example Movie" || current.BaseYear != 2020 {
			t.Errorf("queued identity = %q (%d)", current.BaseTitle, current.BaseYear)
		}
		if file.BaseTitle != "old parser title" {
			t.Fatal("persisted model changed")
		}
	}
}

func TestFlexibleEpisodeNamingLinksRangeStart(t *testing.T) {
	ctx := t.Context()
	h := newFallbackTestHarness()
	const seriesID = "unsupported-range-series"
	if err := h.itemRepo.Upsert(ctx, &models.MediaItem{ContentID: seriesID, Title: "Example Show", Type: "series", Status: "matched"}); err != nil {
		t.Fatal(err)
	}
	if err := h.episodeRepo.Upsert(ctx, &models.Episode{ContentID: "wrong-ending-episode", SeriesID: seriesID, SeasonNumber: 2009, EpisodeNumber: 15, Title: "The Arrival", MetadataSource: "provider"}); err != nil {
		t.Fatal(err)
	}
	if err := h.episodeRepo.Upsert(ctx, &models.Episode{ContentID: "range-start-episode", SeriesID: seriesID, SeasonNumber: 2009, EpisodeNumber: 3, Title: "The Arrival", MetadataSource: "provider"}); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{ID: 1, MediaFolderID: 1, FilePath: "/tv/Example Show/Season 2009/2009x03-E15 - The Arrival.mkv"}
	h.fileRepo.addFile(file)
	h.fileRepo.contentIDs[file.ID] = seriesID
	if err := h.service.ensureSeriesEpisodeLinks(ctx, seriesID); err != nil {
		t.Fatal(err)
	}
	if got := h.fileRepo.episodeLinks[file.ID]; got != "range-start-episode" {
		t.Fatalf("range linked to episode %q, want starting episode", got)
	}
	if hints := selectLocalEpisodeCoordinateHints([]string{file.FilePath}); len(hints) != 1 || hints[0].EpisodeNumber != 3 {
		t.Fatalf("range supplied incorrect series validation evidence: %+v", hints)
	}
}

func TestFlexibleEpisodeNamingDisambiguatesSameTitleSeries(t *testing.T) {
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"2020": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Arrival"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Departure"},
		},
		"2015": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Day One"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Crime and Punishment"},
		},
	}}
	hints := &MatchHints{
		Title: "Crims", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Crims/Season 1/Crims - 1x01 - Day One.mkv",
			"/tv/Crims/Season 1/Episode 02 - Crime and Punishment.mkv",
		},
	}
	candidates := []MatchCandidate{
		{Title: "Crims", Year: 2020, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "2020"}},
		{Title: "Crims", Year: 2015, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "2015"}},
	}
	winner, errs := validateSeriesMatchByEpisodes(t.Context(), hints, candidates, []Provider{provider}, "en")
	if len(errs) != 0 {
		t.Fatalf("validation errors = %v", errs)
	}
	if winner == nil || winner.ProviderIDs["tmdb"] != "2015" {
		t.Fatalf("winner = %+v, want series corroborated by episode titles", winner)
	}
}
