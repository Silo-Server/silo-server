package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

func TestCatalogRecentTVResponseIncludesPlayAndParentSeriesTargets(t *testing.T) {
	seasonNumber := 4
	episodeNumber := 9
	resp := itemListResponseShell(&models.MediaItem{
		ContentID:     "episode-9",
		PlayContentID: "episode-9",
		Type:          "episode",
		Title:         "The Arrival",
		Status:        "matched",
	}, nil, nil)
	applyEpisodeBrowseMetadata(&resp, episodeBrowseMetadata{
		SeriesID:      "series-1",
		SeriesTitle:   "Running Show",
		SeasonNumber:  &seasonNumber,
		EpisodeNumber: &episodeNumber,
	})

	if resp.PlayContentID != "episode-9" || resp.SeriesID != "series-1" || resp.SeriesTitle != "Running Show" {
		t.Fatalf("catalog response targets = %#v", resp)
	}
	if resp.SeasonNumber == nil || *resp.SeasonNumber != 4 || resp.EpisodeNumber == nil || *resp.EpisodeNumber != 9 {
		t.Fatalf("catalog response episode metadata = %#v", resp)
	}

	unrelated := itemListResponseShell(&models.MediaItem{ContentID: "movie-1", Type: "movie", Title: "Movie", Status: "matched"}, nil, nil)
	if unrelated.PlayContentID != "" || unrelated.SeriesID != "" {
		t.Fatalf("unrelated response leaked TV targets: %#v", unrelated)
	}
}

func TestUniversalPlayableTargetsAreAdditiveAcrossResponseShapes(t *testing.T) {
	detailJSON, err := json.Marshal(catalog.ItemDetail{
		ContentID: "series-1", PlayContentID: "episode-2", Type: "series", Title: "Show",
	})
	if err != nil || !strings.Contains(string(detailJSON), `"play_content_id":"episode-2"`) {
		t.Fatalf("detail JSON = %s, err %v", detailJSON, err)
	}
	seasonJSON, err := json.Marshal(seasonResponse{
		ContentID: "season-1", PlayContentID: "episode-2", SeasonNumber: 1, Title: "Season 1",
	})
	if err != nil || !strings.Contains(string(seasonJSON), `"play_content_id":"episode-2"`) {
		t.Fatalf("season JSON = %s, err %v", seasonJSON, err)
	}

	// A card's own hint reaches the resolver as PreferredContentID, so the
	// resolved value already carries the event-specific target when this
	// profile can play it. The response takes the resolver's answer.
	h := &SectionHandler{}
	recent := h.toSectionItemResponse(
		sections.SectionRecentlyAdded,
		&models.MediaItem{ContentID: "series-1", PlayContentID: "event-episode", Type: "series", Title: "Show"},
		nil, nil, nil, sectionItemImageURLs{}, "event-episode",
	)
	if recent.PlayContentID != "event-episode" {
		t.Fatalf("recent target = %q, want event-specific target", recent.PlayContentID)
	}

	// When the hint does not survive profile-aware validation, the resolver's
	// fallback wins instead of the unvalidated hint carried by the item.
	rejected := h.toSectionItemResponse(
		sections.SectionRecentlyAdded,
		&models.MediaItem{ContentID: "series-1", PlayContentID: "unplayable-episode", Type: "series", Title: "Show"},
		nil, nil, nil, sectionItemImageURLs{}, "playable-episode",
	)
	if rejected.PlayContentID != "playable-episode" {
		t.Fatalf("rejected hint target = %q, want playable-episode", rejected.PlayContentID)
	}
}
