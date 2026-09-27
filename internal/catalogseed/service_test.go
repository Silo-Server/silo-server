package catalogseed

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestToVideoTrackRecordsPreservesVideoMetadata(t *testing.T) {
	got := toVideoTrackRecords([]models.VideoTrack{
		{ColorRange: "tv", DVLevel: 6},
		{ColorRange: "pc"},
		{ColorRange: "unknown"},
	})

	if len(got) != 3 {
		t.Fatalf("records length = %d, want 3", len(got))
	}
	if got[0].ColorRange != "tv" || got[1].ColorRange != "pc" || got[2].ColorRange != "unknown" {
		t.Fatalf(
			"ColorRange values = [%q, %q, %q], want [tv, pc, unknown]",
			got[0].ColorRange,
			got[1].ColorRange,
			got[2].ColorRange,
		)
	}
	if got[0].DVLevel != 6 {
		t.Fatalf("DVLevel = %d, want 6", got[0].DVLevel)
	}
}

func TestCatalogSeedSearchUpsertIDsIncludesChangedItemsAndEmbeddings(t *testing.T) {
	itemStates := map[string]bool{
		"movie-1":  true,
		"movie-2":  false,
		"series-1": true,
	}
	embeddings := []EmbeddingRecord{
		{MediaItemID: " movie-3 "},
		{MediaItemID: "movie-1"},
		{MediaItemID: ""},
	}
	files := []FileRecord{
		{ContentID: " movie-4 "},
		{ContentID: ""},
	}
	links := []LibraryLinkRecord{
		{ContentID: "movie-5"},
		{ContentID: "movie-2"},
	}

	got := catalogSeedSearchUpsertIDs(itemStates, embeddings, files, links)
	want := []string{"movie-1", "movie-2", "movie-3", "movie-4", "movie-5", "series-1"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalogSeedSearchUpsertIDs = %#v, want %#v", got, want)
	}
}

// TestItemToRecordCarriesTheAdvisory proves the display-only advisory survives
// an export. Unlike content_rating_age it cannot be re-derived from anything
// else in the bundle, and the providers that supply it are rate limited, so an
// export that dropped it would force a re-enrichment pass on restore.
func TestItemToRecordCarriesTheAdvisory(t *testing.T) {
	age := 13
	record := itemToRecord(&models.MediaItem{
		ContentID:      "advisory-1",
		Type:           "movie",
		Title:          "Jaws",
		ContentRating:  "PG",
		AdvisoryAge:    &age,
		AdvisorySource: "commonsense",
	})
	if record.AdvisoryAge == nil || *record.AdvisoryAge != 13 {
		t.Fatalf("AdvisoryAge = %v, want 13", record.AdvisoryAge)
	}
	if record.AdvisorySource != "commonsense" {
		t.Fatalf("AdvisorySource = %q, want \"commonsense\"", record.AdvisorySource)
	}
	// The certification is a separate column and must not be disturbed.
	if record.ContentRating != "PG" {
		t.Fatalf("ContentRating = %q, want \"PG\"", record.ContentRating)
	}
}

// TestItemToRecordOmitsAnAbsentAdvisory keeps a bundle free of empty advisory
// keys, so an older bundle and a new one with no advisory decode identically.
func TestItemToRecordOmitsAnAbsentAdvisory(t *testing.T) {
	record := itemToRecord(&models.MediaItem{ContentID: "advisory-2", Type: "movie", Title: "Jaws"})
	if record.AdvisoryAge != nil {
		t.Fatalf("AdvisoryAge = %v, want nil", *record.AdvisoryAge)
	}
	if record.AdvisorySource != "" {
		t.Fatalf("AdvisorySource = %q, want empty", record.AdvisorySource)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshaling record: %v", err)
	}
	for _, key := range []string{"advisory_age", "advisory_source"} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Fatalf("bundle carries %q for an item with no advisory: %s", key, encoded)
		}
	}
}
