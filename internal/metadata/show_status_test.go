package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNormalizeShowStatus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  ", ""},
		// TMDB spellings
		{"Returning Series", "returning"},
		{"Ended", "ended"},
		{"Canceled", "cancelled"},
		{"In Production", "in_production"},
		{"Pilot", "in_production"},
		{"Planned", "upcoming"},
		// TVDB spellings
		{"Continuing", "returning"},
		{"Upcoming", "upcoming"},
		// Already-canonical values are stable
		{"returning", "returning"},
		{"ended", "ended"},
		{"cancelled", "cancelled"},
		{"in_production", "in_production"},
		{"upcoming", "upcoming"},
		// Unknown values pass through lowercased instead of being dropped
		{"On Hiatus", "on hiatus"},
	}
	for _, tc := range cases {
		if got := NormalizeShowStatus(tc.in); got != tc.want {
			t.Errorf("NormalizeShowStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMetadataResultToItem_NormalizesSeriesShowStatus(t *testing.T) {
	result := &MetadataResult{
		HasMetadata: true,
		Title:       "Series",
		ShowStatus:  "Returning Series",
	}

	item := metadataResultToItem(result, "series")
	if item.ShowStatus != "returning" {
		t.Fatalf("expected item show_status %q, got %q", "returning", item.ShowStatus)
	}
}

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

func TestItemToMetadataResult_CarriesShowStatus(t *testing.T) {
	result := itemToMetadataResult(&models.MediaItem{
		ContentID:  "series-1",
		Type:       "series",
		Title:      "Series",
		ShowStatus: "returning",
	})

	if result.ShowStatus != "returning" {
		t.Fatalf("expected metadata show_status %q, got %q", "returning", result.ShowStatus)
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
