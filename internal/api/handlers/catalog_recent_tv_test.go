package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// A section card plays the target the resolver chose, not the hint the item
// carried.
func TestSectionCardPlaysTheResolvedTarget(t *testing.T) {
	h := &SectionHandler{}
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
