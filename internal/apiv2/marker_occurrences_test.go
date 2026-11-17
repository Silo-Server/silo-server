package apiv2

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMarkerOccurrencesCarryTheirOwnPreview(t *testing.T) {
	ranges := []models.MarkerSegment{{Kind: "intro", StartSeconds: 6, EndSeconds: 9}, {Kind: "credits", StartSeconds: 30, EndSeconds: 33}}
	previews := []catalogpkg.VersionMarkerPreview{{MarkerSegment: ranges[0], ThumbnailURL: "https://example.test/yellow.webp", ThumbnailThumbhash: "hash", ThumbnailCaptureSeconds: 6}}
	version := catalogpkg.FileVersion{FileID: 1, MarkerSegments: ranges, MarkerPreviews: previews}
	view := handlers.FileMarkersView{FileID: 1, MarkerSegments: ranges, MarkerPreviews: previews}
	output, err := markerOutput(view, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, occurrences := range [][]MarkerOccurrence{watchVersionOf(version).MarkerSegments, output.Body.MarkerSegments} {
		if occurrences[0].ThumbnailURL != previews[0].ThumbnailURL || occurrences[0].ThumbnailCaptureSeconds == nil || *occurrences[0].ThumbnailCaptureSeconds != 6 {
			t.Fatalf("missing preview: %+v", occurrences)
		}
		if occurrences[1].ThumbnailURL != "" || occurrences[1].ThumbnailCaptureSeconds != nil {
			t.Fatal("missing image borrowed another occurrence")
		}
	}
}
