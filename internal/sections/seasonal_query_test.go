package sections

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// TestSeasonalThemeHasQueryCoversAllOrderedThemes ensures every theme that can
// win multi-theme selection resolves to either a genre query or a keyword
// fallback. A theme in SeasonalThemeOrder without either would black out the
// section during its own window.
func TestSeasonalThemeHasQueryCoversAllOrderedThemes(t *testing.T) {
	for _, theme := range recipes.SeasonalThemeOrder {
		if !seasonalThemeHasQuery(theme) {
			t.Errorf("theme %q is selectable (SeasonalThemeOrder) but has no query or keyword fallback", theme)
		}
	}
}

// TestSeasonalKeywordThemesHaveNoGenreQuery pins the routing: keyword themes
// must not also have a genre QueryDefinition, otherwise the keyword fallback
// is dead code.
func TestSeasonalKeywordThemesHaveNoGenreQuery(t *testing.T) {
	for theme := range seasonalKeywordTitles {
		if _, ok := seasonalQueryDef(theme); ok {
			t.Errorf("theme %q has both a genre query and keyword fallback; keyword path unreachable", theme)
		}
		if _, ok := recipes.SeasonalPredicates[theme]; !ok {
			t.Errorf("keyword theme %q has no seasonal predicate", theme)
		}
	}
}

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
