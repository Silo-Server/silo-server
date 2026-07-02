package recommendations

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

func TestBuildItemsNeedingEmbeddingSQLIsCheap(t *testing.T) {
	query := buildItemsNeedingEmbeddingSQL()

	// The whole point of the cheap pass is that it never pays the per-row
	// item_people LATERAL joins that the text-staleness CTE needs. If either of
	// these ever reappears here, Pass 1 has stopped being cheap.
	if strings.Contains(query, "item_people") {
		t.Fatalf("cheap embedding query must not reference item_people: %s", query)
	}
	if strings.Contains(query, "LATERAL") {
		t.Fatalf("cheap embedding query must not use LATERAL joins: %s", query)
	}

	normalized := strings.Join(strings.Fields(query), " ")
	assertQueryTermsInOrder(t, normalized,
		"LEFT JOIN media_item_embeddings e ON e.media_item_id = mi.content_id",
		"e.media_item_id IS NULL OR e.model != $1",
		"$2 = '' OR mi.content_id > $2",
		"ORDER BY mi.content_id",
		"LIMIT $3",
	)
}

func TestItemsNeedingEmbeddingMissingAndModelStaleOnly(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := context.Background()

	const (
		prefix       = "t7cheap-"
		currentModel = "model-current"
		oldModel     = "model-old"
	)
	cleanupRecoMediaItems(t, pool, prefix)

	missingID := prefix + "1-missing"
	modelStaleID := prefix + "2-modelstale"
	textStaleID := prefix + "3-textstale"

	// (i) matched movie, no embedding row at all -> cheap candidate.
	seedRecoMediaItem(t, pool, missingID, "movie", "matched")
	// (ii) embedding exists but under an OLD model -> cheap candidate.
	seedRecoMediaItem(t, pool, modelStaleID, "movie", "matched")
	seedRecoEmbedding(t, pool, modelStaleID, oldModel, modelStaleID)
	// (iii) embedding under the CURRENT model. The cheap query only looks at
	// model identity, so this row must NOT appear even though its cast changed
	// (text-staleness is Pass 2's job, detected by the expensive CTE).
	seedRecoMediaItem(t, pool, textStaleID, "movie", "matched")
	seedRecoEmbedding(t, pool, textStaleID, currentModel, "stale canonical text")

	repo := NewRepo(pool)

	got, err := repo.ItemsNeedingEmbedding(ctx, currentModel, "", 100)
	if err != nil {
		t.Fatalf("ItemsNeedingEmbedding: %v", err)
	}
	gotSet := make(map[string]bool, len(got))
	for _, id := range got {
		gotSet[id] = true
	}
	if !gotSet[missingID] {
		t.Errorf("missing-embedding item %q should be a cheap candidate; got %v", missingID, got)
	}
	if !gotSet[modelStaleID] {
		t.Errorf("model-stale item %q should be a cheap candidate; got %v", modelStaleID, got)
	}
	if gotSet[textStaleID] {
		t.Errorf("text-stale-only item %q must NOT be a cheap candidate; got %v", textStaleID, got)
	}

	// Results are ordered by content_id, so paging past the first id must skip
	// it and still surface the rest. This is what lets EmbedAll advance its
	// cursor over a failed/skipped item without re-fetching it forever.
	if len(got) < 2 {
		t.Fatalf("expected at least 2 cheap candidates to exercise the cursor, got %v", got)
	}
	first := got[0]
	after, err := repo.ItemsNeedingEmbedding(ctx, currentModel, first, 100)
	if err != nil {
		t.Fatalf("ItemsNeedingEmbedding(afterID): %v", err)
	}
	for _, id := range after {
		if id == first {
			t.Fatalf("cursor afterID=%q still returned %q", first, first)
		}
		if id <= first {
			t.Fatalf("cursor returned id %q <= afterID %q; ordering broken", id, first)
		}
	}
}

// seedRecoMediaItem inserts a minimal embed-eligible media item whose title is
// its content_id. Delegates to seedRecoMediaItemTitled so both the cheap-query
// and EmbedAll tests seed rows that survive ItemRepository.GetByIDs.
func seedRecoMediaItem(t *testing.T, pool *pgxpool.Pool, contentID, mediaType, status string) {
	t.Helper()
	seedRecoMediaItemTitled(t, pool, contentID, mediaType, status, contentID)
}

// seedRecoEmbedding inserts a zero-vector embedding row with an explicit model
// and canonical_text. The vector width matches the migrated column (3072).
func seedRecoEmbedding(t *testing.T, pool *pgxpool.Pool, contentID, model, canonicalText string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
		VALUES (
			$1,
			(SELECT array_agg(0.0::real) FROM generate_series(1, 3072))::vector,
			$2,
			$3
		)
	`, contentID, model, canonicalText); err != nil {
		t.Fatalf("seed embedding %s: %v", contentID, err)
	}
}

func cleanupRecoMediaItems(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		// media_item_embeddings and item_people rows cascade on media_items delete.
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
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
