package handlers

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestAdvisoryStaysOffTheV1Wire guards the frozen /api/v1 contract.
//
// The v2 card renderer reads the advisory off these Go structs, which are
// aliased as SectionItemView and CollectionItemView. V1 feature development is
// frozen, so the fields carry json:"-" and must never appear in a v1 body —
// including when an item actually has an advisory.
func TestAdvisoryStaysOffTheV1Wire(t *testing.T) {
	age := 13

	section := sectionItemResponse{
		ContentID:      "advisory-1",
		Type:           "movie",
		Title:          "Jaws",
		ContentRating:  "PG",
		AdvisoryAge:    &age,
		AdvisorySource: "commonsense",
	}
	list := itemListResponse{
		ContentID:      "advisory-1",
		Type:           "movie",
		Title:          "Jaws",
		ContentRating:  "PG",
		AdvisoryAge:    &age,
		AdvisorySource: "commonsense",
	}

	for name, payload := range map[string]any{
		"sectionItemResponse": section,
		"itemListResponse":    list,
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshaling %s: %v", name, err)
			}
			for _, key := range []string{"advisory_age", "advisory_source", "AdvisoryAge", "AdvisorySource"} {
				if bytes.Contains(encoded, []byte(key)) {
					t.Fatalf("%s leaked %q onto the frozen v1 contract: %s", name, key, encoded)
				}
			}
			// The certification is part of v1 and must still be there.
			if !bytes.Contains(encoded, []byte(`"content_rating":"PG"`)) {
				t.Fatalf("%s lost content_rating: %s", name, encoded)
			}
		})
	}
}
