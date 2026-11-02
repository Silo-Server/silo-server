package sections

import (
	"encoding/json"
	"testing"
)

// TestRecommendationConfigAnchorPrecedence verifies because_you_watched honors
// both the legacy source_item_id key and the recipe's anchor_item_id key,
// preferring the legacy key when both are set.
func TestRecommendationConfigAnchorPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		config string
		want   string
	}{
		{"anchor only", `{"anchor_item_id":"abc"}`, "abc"},
		{"source only", `{"source_item_id":"def"}`, "def"},
		{"both set prefers legacy", `{"source_item_id":"def","anchor_item_id":"abc"}`, "def"},
		{"blank strings auto-pick", `{"source_item_id":"","anchor_item_id":"  "}`, ""},
		{"empty config auto-picks", `{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := parseRecommendationSectionConfig(json.RawMessage(tc.config))
			if got := cfg.anchor(); got != tc.want {
				t.Errorf("anchor() = %q, want %q", got, tc.want)
			}
		})
	}
}
