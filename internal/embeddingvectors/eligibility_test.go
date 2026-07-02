package embeddingvectors

import (
	"fmt"
	"strings"
	"testing"
)

// legacyRecommendationItemEligibilityWhereClause reproduces the predicate that
// recommendations.recommendationItemEligibilityWhereClause emitted before it was
// refactored to delegate here. The shared helper must remain byte-identical so
// existing recommendation queries continue to filter the same population.
func legacyRecommendationItemEligibilityWhereClause(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		alias = "media_items"
	}
	return fmt.Sprintf("(%s.status = 'matched' OR %s.type = 'audiobook' OR %s.type = 'ebook')", alias, alias, alias)
}

func TestItemEligibilityWhereClauseMatchesLegacy(t *testing.T) {
	for _, alias := range []string{"mi", "media_items", "  e  ", "", "   "} {
		want := legacyRecommendationItemEligibilityWhereClause(alias)
		got := ItemEligibilityWhereClause(alias)
		if got != want {
			t.Fatalf("ItemEligibilityWhereClause(%q) = %q, want %q", alias, got, want)
		}
	}
}

func TestItemEligibilityWhereClauseDefaultsAlias(t *testing.T) {
	got := ItemEligibilityWhereClause("")
	want := "(media_items.status = 'matched' OR media_items.type = 'audiobook' OR media_items.type = 'ebook')"
	if got != want {
		t.Fatalf("ItemEligibilityWhereClause(\"\") = %q, want %q", got, want)
	}
}
