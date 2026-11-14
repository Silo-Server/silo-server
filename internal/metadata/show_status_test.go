package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMetadataResultToItem_PassesNonSeriesShowStatusVerbatim(t *testing.T) {
	// Manga statuses use their own value domain ("Ongoing", "Completed", ...)
	// normalized by the manga enrichment pipeline; the generic converter must
	// not case-mangle them on a round-trip.
	result := &MetadataResult{
		HasMetadata: true,
		Title:       "Manga",
		ShowStatus:  "Ongoing",
	}

	item := metadataResultToItem(result, "manga")
	if item.ShowStatus != "Ongoing" {
		t.Fatalf("expected manga show_status to pass through verbatim, got %q", item.ShowStatus)
	}
}

// A refresh cycle that fetches no status must not wipe a previously persisted
// one: the existing item's status round-trips through itemToMetadataResult,
// survives the merge (fresh empty values never overwrite), and lands back on
// the item built for the upsert.
func TestShowStatus_SurvivesRefreshWithoutProviderStatus(t *testing.T) {
	existing := itemToMetadataResult(&models.MediaItem{
		ContentID:  "series-1",
		Type:       "series",
		Title:      "Series",
		ShowStatus: "ended",
	})
	fresh := &MetadataResult{HasMetadata: true, Title: "Series"}

	MergeMetadata(fresh, existing, nil, MergeReplaceUnlocked)
	item := metadataResultToItem(existing, "series")

	if item.ShowStatus != "ended" {
		t.Fatalf("expected persisted show_status to survive refresh, got %q", item.ShowStatus)
	}
}
