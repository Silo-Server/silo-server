package apiv2

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMarkerOccurrenceProjectionPreservesGapsAndLegacyWire(t *testing.T) {
	ranges := []models.MarkerSegment{{Kind: "intro", StartSeconds: 10, EndSeconds: 20}, {Kind: "intro", StartSeconds: 40, EndSeconds: 50}}
	legacy := handlers.FileMarkersView{FileID: 1, Intro: handlers.MarkerSegmentView{Start: new(10.0), End: new(20.0)}, MarkerSegments: ranges}
	output, err := markerOutput(legacy, nil)
	if err != nil {
		t.Fatal(err)
	}
	watch := watchVersionOf(catalogpkg.FileVersion{FileID: 1, MarkerSegments: ranges})
	manifest := syntheticDownloadManifest()
	manifest.MarkerSegments = ranges
	offline, err := downloadManifestOf(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, occurrences := range [][]MarkerOccurrence{output.Body.MarkerSegments, watch.MarkerSegments, offline.MarkerSegments} {
		if len(occurrences) != 2 || occurrences[0].EndSeconds != 20 || occurrences[1].StartSeconds != 40 {
			t.Fatalf("marker gap lost in projection: %+v", occurrences)
		}
	}
	for _, value := range []any{legacy, catalogpkg.FileVersion{FileID: 1, MarkerSegments: ranges}, downloads.OfflineManifest{MarkerSegments: ranges}} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(data, &wire); err != nil {
			t.Fatal(err)
		}
		if _, present := wire["marker_segments"]; present {
			t.Fatalf("legacy wire includes additive v2 field: %s", data)
		}
	}
	if empty := watchVersionOf(catalogpkg.FileVersion{}); empty.MarkerSegments == nil {
		t.Fatal("marker collection must be empty rather than null")
	}
}
