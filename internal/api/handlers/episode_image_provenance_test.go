package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

func TestEpisodeImageProvenanceBeforeSigning(t *testing.T) {
	for _, tc := range []struct {
		name, poster, backdrop, still string
		posterStill, backdropStill    bool
	}{
		{"still and season poster", "season/poster", "episode/still", "episode/still", false, true},
		{"series fallback", "series/poster", "series/backdrop", "", false, false},
		{"still in poster fallback", "episode/still", "series/backdrop", "episode/still", true, false},
		{"external provider still", "series/poster", "https://example.invalid/image", "https://example.invalid/image", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The section episode query marks provenance on the item it selects
			// artwork for (sections.markEpisodeStillProvenance).
			item := &models.MediaItem{ContentID: "episode-1", Type: "episode", PosterPath: tc.poster, BackdropPath: tc.backdrop,
				PosterIsEpisodeStill: new(tc.posterStill), BackdropIsEpisodeStill: new(tc.backdropStill)}
			h := &SectionHandler{}
			card := h.toSectionItemResponse(sections.SectionNextUp, item, &sections.SectionItemMeta{}, nil, nil,
				sectionItemImageURLs{posterURL: "https://example.invalid/signed-poster", backdropURL: "https://example.invalid/signed-backdrop"}, "")
			if card.PosterIsEpisodeStill == nil || *card.PosterIsEpisodeStill != tc.posterStill || card.BackdropIsEpisodeStill == nil || *card.BackdropIsEpisodeStill != tc.backdropStill {
				t.Fatalf("section provenance = %v / %v", card.PosterIsEpisodeStill, card.BackdropIsEpisodeStill)
			}
			// Items from other queries carry no provenance; the section card
			// compares their paths with the episode's still instead.
			unmarked := &models.MediaItem{ContentID: "episode-1", Type: "episode", PosterPath: tc.poster, BackdropPath: tc.backdrop}
			fallback := h.toSectionItemResponse(sections.SectionCustomFilter, unmarked, &sections.SectionItemMeta{EpisodeStillPath: new(tc.still)}, nil, nil,
				sectionItemImageURLs{posterURL: "https://example.invalid/signed-poster", backdropURL: "https://example.invalid/signed-backdrop"}, "")
			if fallback.PosterIsEpisodeStill == nil || *fallback.PosterIsEpisodeStill != tc.posterStill || fallback.BackdropIsEpisodeStill == nil || *fallback.BackdropIsEpisodeStill != tc.backdropStill {
				t.Fatalf("fallback provenance = %v / %v", fallback.PosterIsEpisodeStill, fallback.BackdropIsEpisodeStill)
			}
			wt := (&RecommendationsHandler{}).buildSectionItem(context.Background(), item, nil, nil)
			if wt.PosterIsEpisodeStill != item.PosterIsEpisodeStill || wt.BackdropIsEpisodeStill != item.BackdropIsEpisodeStill {
				t.Fatal("Watch Tonight card drops provenance")
			}
			listing := itemListResponseShell(item, nil, nil)
			applyEpisodeBrowseMetadata(&listing, episodeBrowseMetadata{StillPath: tc.still})
			if *listing.PosterIsEpisodeStill != tc.posterStill || *listing.BackdropIsEpisodeStill != tc.backdropStill {
				t.Fatal("listing provenance disagrees with selected paths")
			}
			for _, view := range []any{card, listing} {
				wire, err := json.Marshal(view)
				if err != nil || strings.Contains(string(wire), "is_episode_still") || strings.Contains(string(wire), "episode/still") {
					t.Fatalf("v1 leaks provenance or raw paths: %s (%v)", wire, err)
				}
			}
		})
	}
}

func TestEpisodeRowProvenancePreservesSeriesFallback(t *testing.T) {
	for _, still := range []string{"", "https://example.invalid/still"} {
		row, _ := episodeResponseShell(&models.Episode{StillPath: still}, episodeImageFallback{Path: "series/backdrop"}, imagesize.Unset)
		if row.StillIsEpisodeStill == nil || *row.StillIsEpisodeStill != (still != "") {
			t.Fatal("row provenance loses fallback source")
		}
	}
}

// A cached section item keeps the provenance decided when its artwork was
// selected, even after the image cache rewrites the episode's still path.
func TestSectionCardKeepsProvenanceOfCachedArtwork(t *testing.T) {
	item := &models.MediaItem{ContentID: "episode-1", Type: "episode", PosterPath: "series/poster", BackdropPath: "provider/still.jpg",
		PosterIsEpisodeStill: new(false), BackdropIsEpisodeStill: new(true)}
	// The image cache has since rewritten the still to its cached path.
	meta := &sections.SectionItemMeta{EpisodeStillPath: new("library/cached-still.webp")}
	card := (&SectionHandler{}).toSectionItemResponse(sections.SectionRecentlyAdded, item, meta, nil, nil,
		sectionItemImageURLs{backdropURL: "https://example.invalid/signed-backdrop"}, "")
	if card.BackdropIsEpisodeStill == nil || !*card.BackdropIsEpisodeStill {
		t.Fatalf("cached still served as %v", card.BackdropIsEpisodeStill)
	}
}
