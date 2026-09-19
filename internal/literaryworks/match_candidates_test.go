package literaryworks

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The candidate lookup fans out across three independent match signals (title,
// provider IDs, series position). These tests pin the result semantics so the
// query shape can change underneath them without altering what comes back.

func seedCandidateFixture(t *testing.T, pool *pgxpool.Pool) (folderID int, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	folderID = seedLiteraryFolder(t, pool)
	cleanup = func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE 'cand-%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	}
	t.Cleanup(cleanup)
	return folderID, cleanup
}

func seedSeries(t *testing.T, pool *pgxpool.Pool, table, contentID, seriesName string, index float64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), fmt.Sprintf(`
		INSERT INTO %s (content_id, series_name, series_index)
		VALUES ($1, $2, $3)
	`, table), contentID, seriesName, index); err != nil {
		t.Fatalf("seed %s for %s: %v", table, contentID, err)
	}
}

func TestListMatchCandidateIDsMatchesOppositeFormatByTitle(t *testing.T) {
	pool := newLiteraryWorksTestPool(t)
	folderID, _ := seedCandidateFixture(t, pool)
	repo := NewRepository(pool)

	seedLiteraryMediaItem(t, pool, "cand-src", FormatEbook, "Golden Margins", folderID)
	seedLiteraryMediaItem(t, pool, "cand-hit", FormatAudiobook, "golden margins", folderID)
	seedLiteraryMediaItem(t, pool, "cand-miss", FormatAudiobook, "Different Book", folderID)

	ids, err := repo.listMatchCandidateIDs(context.Background(), MatchItem{
		ContentID: "cand-src",
		Type:      FormatEbook,
		Title:     "Golden Margins",
	}, 20)
	if err != nil {
		t.Fatalf("listMatchCandidateIDs: %v", err)
	}

	if len(ids) != 1 || ids[0] != "cand-hit" {
		t.Fatalf("ids = %v, want [cand-hit] (case-insensitive title match, opposite format only)", ids)
	}
}

func TestListMatchCandidateIDsMatchesBySeriesPosition(t *testing.T) {
	pool := newLiteraryWorksTestPool(t)
	folderID, _ := seedCandidateFixture(t, pool)
	repo := NewRepository(pool)

	seedLiteraryMediaItem(t, pool, "cand-src", FormatEbook, "Book One Title", folderID)
	seedLiteraryMediaItem(t, pool, "cand-hit", FormatAudiobook, "Totally Other Title", folderID)
	seedSeries(t, pool, "audiobook_series", "cand-hit", "Vaz", 1)

	index := 1.0
	ids, err := repo.listMatchCandidateIDs(context.Background(), MatchItem{
		ContentID:   "cand-src",
		Type:        FormatEbook,
		Title:       "Book One Title",
		SeriesName:  "vaz",
		SeriesIndex: &index,
	}, 20)
	if err != nil {
		t.Fatalf("listMatchCandidateIDs: %v", err)
	}

	if len(ids) != 1 || ids[0] != "cand-hit" {
		t.Fatalf("ids = %v, want [cand-hit] matched on series despite differing titles", ids)
	}
}

func seedProviderID(t *testing.T, pool *pgxpool.Pool, contentID, provider, providerID, itemType string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type)
		VALUES ($1, $2, $3, $4)
	`, contentID, provider, providerID, itemType); err != nil {
		t.Fatalf("seed provider id for %s: %v", contentID, err)
	}
}

func TestListMatchCandidateIDsMatchesBySharedProviderID(t *testing.T) {
	pool := newLiteraryWorksTestPool(t)
	folderID, _ := seedCandidateFixture(t, pool)
	repo := NewRepository(pool)

	// Titles differ and neither side has series data, so a shared provider ID
	// is the only signal that can produce a match.
	seedLiteraryMediaItem(t, pool, "cand-src", FormatEbook, "Golden Margins", folderID)
	seedLiteraryMediaItem(t, pool, "cand-hit", FormatAudiobook, "Totally Unrelated Title", folderID)
	seedLiteraryMediaItem(t, pool, "cand-miss", FormatAudiobook, "Another Unrelated Title", folderID)
	seedProviderID(t, pool, "cand-hit", "openlibrary", "OL1618W", FormatAudiobook)

	ids, err := repo.listMatchCandidateIDs(context.Background(), MatchItem{
		ContentID:   "cand-src",
		Type:        FormatEbook,
		Title:       "Golden Margins",
		ExternalIDs: map[string]string{"openlibrary": "OL1618W"},
	}, 20)
	if err != nil {
		t.Fatalf("listMatchCandidateIDs: %v", err)
	}

	want := []string{"cand-hit"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v (provider-ID branch is the only possible match)", ids, want)
	}
}

func TestListMatchCandidateIDsIgnoresASINProviderID(t *testing.T) {
	pool := newLiteraryWorksTestPool(t)
	folderID, _ := seedCandidateFixture(t, pool)
	repo := NewRepository(pool)

	// ASIN identifies an edition rather than a work, so it must never be
	// promoted into a candidate branch.
	seedLiteraryMediaItem(t, pool, "cand-src", FormatEbook, "Golden Margins", folderID)
	seedLiteraryMediaItem(t, pool, "cand-hit", FormatAudiobook, "Totally Unrelated Title", folderID)
	seedProviderID(t, pool, "cand-hit", "asin", "B0FIXTURE1", FormatAudiobook)

	ids, err := repo.listMatchCandidateIDs(context.Background(), MatchItem{
		ContentID:   "cand-src",
		Type:        FormatEbook,
		Title:       "Golden Margins",
		ExternalIDs: map[string]string{"asin": "B0FIXTURE1"},
	}, 20)
	if err != nil {
		t.Fatalf("listMatchCandidateIDs: %v", err)
	}

	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty: a shared ASIN must not match on its own", ids)
	}
}

func TestListMatchCandidateIDsExcludesIgnoredDecisions(t *testing.T) {
	pool := newLiteraryWorksTestPool(t)
	folderID, _ := seedCandidateFixture(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()

	seedLiteraryMediaItem(t, pool, "cand-src", FormatEbook, "Golden Margins", folderID)
	seedLiteraryMediaItem(t, pool, "cand-hit", FormatAudiobook, "Golden Margins", folderID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO literary_work_match_decisions (source_content_id, target_content_id, decision)
		VALUES ('cand-src', 'cand-hit', 'ignored')
	`); err != nil {
		t.Fatalf("seed ignored decision: %v", err)
	}

	ids, err := repo.listMatchCandidateIDs(ctx, MatchItem{
		ContentID: "cand-src",
		Type:      FormatEbook,
		Title:     "Golden Margins",
	}, 20)
	if err != nil {
		t.Fatalf("listMatchCandidateIDs: %v", err)
	}

	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty: an ignored decision must suppress the candidate", ids)
	}
}

// TestMatchCandidateQueryUsesTitleIndex is the performance contract.
//
// The match signals are OR-ed together, but they live on different tables, so
// PostgreSQL cannot combine them into a bitmap OR: it falls back to walking
// every opposite-format book and applying the OR as a post-filter. On a real
// library that is ~434k rows per call. The query must instead be shaped so each
// signal rides its own index -- idx_media_items_books_title_lower for the title
// branch -- which the plan proves by naming it.
func TestMatchCandidateQueryUsesTitleIndex(t *testing.T) {
	pool := newLiteraryWorksTestPool(t)
	folderID, _ := seedCandidateFixture(t, pool)
	ctx := context.Background()

	// Enough rows that a sequential scan is never the planner's cheapest option.
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres)
		SELECT 'cand-bulk-' || g, 'audiobook', 'Bulk Title ' || g, '{}'::text[]
		FROM generate_series(1, 30000) g
	`); err != nil {
		t.Fatalf("seed bulk audiobooks: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_item_libraries (content_id, media_folder_id)
		SELECT 'cand-bulk-' || g, $1 FROM generate_series(1, 30000) g
	`, folderID); err != nil {
		t.Fatalf("seed bulk libraries: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE media_items`); err != nil {
		t.Fatalf("analyze: %v", err)
	}

	index := 1.0
	sql, args := buildMatchCandidateQuery(MatchItem{
		ContentID:   "cand-src",
		Type:        FormatEbook,
		Title:       "Bulk Title 17",
		SeriesName:  "Some Series",
		SeriesIndex: &index,
	}, 20)

	rows, err := pool.Query(ctx, "EXPLAIN "+sql, args...)
	if err != nil {
		t.Fatalf("explain candidate query: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan line: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}

	if !strings.Contains(plan.String(), "idx_media_items_books_title_lower") {
		t.Fatalf("title branch does not use idx_media_items_books_title_lower; plan was:\n%s", plan.String())
	}
}

func TestListMatchCandidateIDsOrdersByTitleAndHonoursLimit(t *testing.T) {
	pool := newLiteraryWorksTestPool(t)
	folderID, _ := seedCandidateFixture(t, pool)
	repo := NewRepository(pool)

	seedLiteraryMediaItem(t, pool, "cand-src", FormatEbook, "Shared Title", folderID)
	for _, id := range []string{"cand-c", "cand-a", "cand-b"} {
		seedLiteraryMediaItem(t, pool, id, FormatAudiobook, "Shared Title", folderID)
	}
	// Distinct series rows must not multiply a candidate into duplicate IDs.
	seedSeries(t, pool, "audiobook_series", "cand-a", "Shared Series", 1)

	index := 1.0
	ids, err := repo.listMatchCandidateIDs(context.Background(), MatchItem{
		ContentID:   "cand-src",
		Type:        FormatEbook,
		Title:       "Shared Title",
		SeriesName:  "Shared Series",
		SeriesIndex: &index,
	}, 2)
	if err != nil {
		t.Fatalf("listMatchCandidateIDs: %v", err)
	}

	// Titles tie, so content_id breaks the tie: cand-a, cand-b, cand-c. The
	// limit keeps the first two, and cand-a appears once despite also matching
	// the series branch.
	want := []string{"cand-a", "cand-b"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}
