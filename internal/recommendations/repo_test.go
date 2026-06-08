package recommendations

import (
	"strings"
	"testing"
)

func TestTasteSeedCandidateQueryOrdersByReliableColdStartSignals(t *testing.T) {
	query := strings.Join(strings.Fields(tasteSeedCandidateQuery), " ")

	assertQueryTermsInOrder(t, query,
		"ORDER BY COALESCE(wc.watch_count, 0) DESC",
		"WHEN mi.rating_imdb IS NOT NULL THEN 2",
		"WHEN mi.rating_tmdb IS NOT NULL AND mi.rating_tmdb < 9.5 THEN 1",
		"ELSE 0 END DESC",
		"mi.rating_imdb DESC NULLS LAST",
		"CASE WHEN mi.rating_tmdb < 9.5 THEN mi.rating_tmdb END DESC NULLS LAST",
		"mi.year DESC NULLS LAST",
		"mi.content_id ASC",
	)

	if strings.Contains(query, "ORDER BY COALESCE(wc.watch_count, 0) DESC, mi.rating_tmdb DESC NULLS LAST") {
		t.Fatal("taste seed query must not rank cold-start candidates by raw TMDB rating directly")
	}
}

func TestEmbeddingEligibilityWhereClauseIncludesBookTypes(t *testing.T) {
	clause := embeddingEligibilityWhereClause()

	for _, term := range []string{"mi.status = 'matched'", "mi.type = 'audiobook'", "mi.type = 'ebook'"} {
		if !strings.Contains(clause, term) {
			t.Fatalf("embedding eligibility clause missing %q: %s", term, clause)
		}
	}
}

func TestEmbeddingEligibilityWhereClauseUsesMediaItemsAlias(t *testing.T) {
	clause := embeddingEligibilityWhereClause()

	if strings.Contains(clause, " type =") || strings.Contains(clause, " status =") {
		t.Fatalf("embedding eligibility clause should qualify media_items columns: %s", clause)
	}
}

func TestRecommendationItemEligibilityWhereClauseIncludesBookTypes(t *testing.T) {
	clause := recommendationItemEligibilityWhereClause("mi")

	for _, term := range []string{"mi.status = 'matched'", "mi.type = 'audiobook'", "mi.type = 'ebook'"} {
		if !strings.Contains(clause, term) {
			t.Fatalf("recommendation item eligibility clause missing %q: %s", term, clause)
		}
	}
}

func TestRecommendationItemEligibilityWhereClauseDefaultsAlias(t *testing.T) {
	clause := recommendationItemEligibilityWhereClause("")

	if !strings.Contains(clause, "media_items.status = 'matched'") {
		t.Fatalf("default eligibility alias missing media_items qualification: %s", clause)
	}
}

func assertQueryTermsInOrder(t *testing.T, query string, terms ...string) {
	t.Helper()

	searchFrom := 0
	for _, term := range terms {
		idx := strings.Index(query[searchFrom:], term)
		if idx < 0 {
			t.Fatalf("query term %q missing or out of order in query:\n%s", term, query)
		}
		searchFrom += idx + len(term)
	}
}
