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
