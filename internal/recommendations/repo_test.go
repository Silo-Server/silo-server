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

func TestTasteSeedCandidateQueryIncludesEbooks(t *testing.T) {
	query := strings.Join(strings.Fields(tasteSeedCandidateQuery), " ")

	for _, term := range []string{
		"(mi.status = 'matched' OR mi.type = 'audiobook' OR mi.type = 'ebook')",
		"mi.type IN ('movie', 'series', 'audiobook', 'ebook')",
	} {
		if !strings.Contains(query, term) {
			t.Fatalf("taste seed candidate query missing %q: %s", term, query)
		}
	}
}

func TestItemWatchersQueryCountsDistinctWatchersPerItem(t *testing.T) {
	query := strings.Join(strings.Fields(itemWatchersQuery), " ")

	// watched_activity rolls episodes up to their parent series, so one
	// watcher can emit many rows per item. The pairs must be deduplicated
	// before ROW_NUMBER ranking and the HAVING watcher count, otherwise a
	// single binge-watcher inflates both.
	assertQueryTermsInOrder(t, query,
		"ROW_NUMBER() OVER (PARTITION BY user_id, profile_id ORDER BY MAX(updated_at) DESC) AS rn",
		"GROUP BY user_id, profile_id, watcher_id, item_id",
		"WHERE rn <= $1",
		"GROUP BY media_item_id",
		"HAVING COUNT(DISTINCT user_id) >= $2",
	)
}

func TestItemWatchersQueryMinWatchersCountsDistinctAccounts(t *testing.T) {
	query := strings.Join(strings.Fields(itemWatchersQuery), " ")

	// The minimum-watchers floor is a sparsity/privacy threshold: one login
	// account with N household profiles must not satisfy it by itself, so the
	// HAVING clause counts distinct user_id (accounts), not watcher rows. The
	// per-profile watcher_id array is still aggregated for Jaccard math.
	assertQueryTermsInOrder(t, query,
		"SELECT user_id, watcher_id, item_id AS media_item_id",
		"ARRAY_AGG(watcher_id) AS watchers",
		"HAVING COUNT(DISTINCT user_id) >= $2",
	)
	if strings.Contains(query, "HAVING COUNT(*) >= $2") {
		t.Fatal("minWatchers floor must not count per-profile watcher rows")
	}
}

func TestEbookReaderProgressForUserQueryGatesHiddenHistory(t *testing.T) {
	query := strings.Join(strings.Fields(ebookReaderProgressForUserQuery), " ")

	// Mirrors GetWatchProgressForUser: rows the user hid stay hidden while
	// updated_at <= hidden_before; reading activity after hidden_before
	// surfaces again.
	assertQueryTermsInOrder(t, query,
		"FROM ebook_reader_progress",
		"NOT EXISTS",
		"FROM user_history_hidden_items hhi",
		"hhi.media_item_id = ebook_reader_progress.content_id",
		"ebook_reader_progress.updated_at <= hhi.hidden_before",
	)
}

func TestWatchedActivityCTEIncludesEbookReaderProgress(t *testing.T) {
	query := strings.Join(strings.Fields(watchedActivityCTE), " ")

	for _, term := range []string{
		"FROM ebook_reader_progress erp",
		"erp.progress >= 0.5",
		"erp.progress >= 0.9 AS completed",
		"hhi.media_item_id = erp.content_id",
	} {
		if !strings.Contains(query, term) {
			t.Fatalf("watched activity CTE missing %q: %s", term, query)
		}
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

func TestTasteProfileRefreshSubjectsQueryIncludesEbookReaderProgress(t *testing.T) {
	query := strings.Join(strings.Fields(tasteProfileRefreshSubjectsQuery), " ")

	assertQueryTermsInOrder(t, query,
		"SELECT DISTINCT user_id, profile_id FROM user_watch_progress",
		"UNION",
		"SELECT DISTINCT user_id, profile_id FROM ebook_reader_progress",
	)
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
