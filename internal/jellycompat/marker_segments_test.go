package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMediaSegmentsPreservesOccurrencesAndLegacyIDs(t *testing.T) {
	version := &catalog.FileVersion{MarkerSegments: []models.MarkerSegment{
		{Kind: "intro", StartSeconds: 10, EndSeconds: 20},
		{Kind: "intro", StartSeconds: 40, EndSeconds: 50},
		{Kind: "credits", StartSeconds: 100, EndSeconds: 110},
	}}
	segments := buildMediaSegmentDTOs("item", version)
	if len(segments) != 3 || segments[0].EndTicks != secondsToTicks(20) || segments[1].StartTicks != secondsToTicks(40) || segments[2].Type != "Outro" {
		t.Fatalf("segments = %+v", segments)
	}
	if segments[0].Id == segments[1].Id || segments[0].Id != deriveSegmentID("item", "Intro") {
		t.Fatalf("occurrence IDs collide or changed legacy identity: %+v", segments)
	}
	legacy := buildMediaSegmentDTOs("item", &catalog.FileVersion{Intro: &catalog.Marker{Start: 10, End: 20}})
	if len(legacy) != 1 || legacy[0] != segments[0] {
		t.Fatalf("legacy marker fallback = %+v", legacy)
	}
}
