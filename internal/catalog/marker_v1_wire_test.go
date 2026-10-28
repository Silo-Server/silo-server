package catalog

import (
	"encoding/json"
	"testing"
)

// GET /api/v1/watch/{id} writes catalog.FileVersion directly, so this struct is the
// wire shape of a frozen response. Provenance reaches clients on the v2 marker, which
// maps from these Go fields rather than from their JSON, so a tag here would quietly
// add fields to a contract that is closed.
func TestMarkerJSONCarriesOnlyTheFrozenRange(t *testing.T) {
	rated := 0.55
	marker := Marker{
		Start:      0,
		End:        90,
		Source:     "plugin",
		Provider:   "introdb",
		Confidence: &rated,
		Algorithm:  "introdb:v4",
	}

	encoded, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}
	if string(encoded) != `{"start":0,"end":90}` {
		t.Fatalf("v1 marker wire = %s, want {\"start\":0,\"end\":90}", encoded)
	}

	// The same fields must still be readable in Go, because the v2 projection maps
	// from them.
	if marker.Source != "plugin" || marker.Provider != "introdb" || marker.Algorithm != "introdb:v4" {
		t.Fatalf("provenance lost from the struct: %+v", marker)
	}
	if marker.Confidence == nil || *marker.Confidence != rated {
		t.Fatalf("confidence lost from the struct: %+v", marker.Confidence)
	}
}
