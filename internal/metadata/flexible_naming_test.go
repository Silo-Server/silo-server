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
