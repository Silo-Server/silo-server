package catalog

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBuildListNextUpQuery_GlobalBoundsAnchorScan(t *testing.T) {
	t.Parallel()

	q := NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
	}
	query, args := buildListNextUpQuery(q, 20, nil)

	expectedFragments := []string{
		"WITH RECURSIVE",
		"(uwp.updated_at, uwp.media_item_id) < (w.updated_at, w.media_item_id)",
		"NOT (e.series_id = ANY(w.seen))",
		fmt.Sprintf("w.n < %d", nextUpAnchorMaxSeries),
		"frontier AS (",
		"LEFT JOIN (",
	}
	for _, fragment := range expectedFragments {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected global anchor walk to contain %q, got:\n%s", fragment, query)
		}
	}
	// Seed step, recursive step, the anchor lateral attached to each, and the
	// regular-episode lookup that moves a special anchor.
	if got := strings.Count(query, "FROM user_history_hidden_items hhi"); got != 5 {
		t.Fatalf("expected hidden-items filter in every walk step and anchor, got %d occurrences:\n%s", got, query)
	}
	seed := strings.SplitN(query, "UNION ALL", 2)[0]
	if strings.Contains(seed, "(uwp.updated_at, uwp.media_item_id) < ($") {
		t.Fatalf("first batch seed must not contain a continuation cursor, got:\n%s", seed)
	}
	if strings.Contains(seed, "e.series_id = ANY($") {
		t.Fatalf("first batch seed must not contain a seen-series exclusion, got:\n%s", seed)
	}
	if len(args) != 3 {
		t.Fatalf("expected first batch args only, got %v", args)
	}

	cursorAt := time.Now().UTC()
	cursor := &nextUpWalkCursor{
		updatedAt:   cursorAt,
		mediaItemID: "episode-96",
		seen:        []string{"series-1", "series-2"},
	}
	continuedQuery, continuedArgs := buildListNextUpQuery(q, 7, cursor)
	continuedSeed := strings.SplitN(continuedQuery, "UNION ALL", 2)[0]
	continuedFragments := []string{
		"(uwp.updated_at, uwp.media_item_id) < ($4, $5)",
		"NOT (e.series_id = ANY($6))",
		"$6::text[] || pick.series_id AS seen",
		"frontier AS (",
		"LEFT JOIN (",
	}
	for _, fragment := range continuedFragments {
		if !strings.Contains(continuedQuery, fragment) {
			t.Fatalf("expected continued query to contain %q, got:\n%s", fragment, continuedQuery)
		}
	}
	if !strings.Contains(continuedSeed, "(uwp.updated_at, uwp.media_item_id) < ($4, $5)") ||
		!strings.Contains(continuedSeed, "NOT (e.series_id = ANY($6))") {
		t.Fatalf("continued batch seed must contain cursor and seen-series predicates, got:\n%s", continuedSeed)
	}
	if len(continuedArgs) != 6 || continuedArgs[3] != cursorAt || continuedArgs[4] != cursor.mediaItemID {
		t.Fatalf("unexpected continuation args: %v", continuedArgs)
	}
	seenArg, ok := continuedArgs[5].([]string)
	if !ok || len(seenArg) != len(cursor.seen) || seenArg[0] != cursor.seen[0] || seenArg[1] != cursor.seen[1] {
		t.Fatalf("expected cursor seen array verbatim, got %#v", continuedArgs[5])
	}
}

func TestBuildListNextUpQuery_SeriesScopedKeepsUnboundedAnchor(t *testing.T) {
	t.Parallel()

	q := NextUpQuery{
		UserID:    7,
		ProfileID: "profile-1",
		SeriesID:  "series-42",
	}
	query, args := buildListNextUpQuery(q, 20, nil)

	// The show-detail tile must anchor on the series' last completed episode
	// no matter how long ago it was watched: the recency bound would make a
	// long-idle series' tile disappear.
	if strings.Contains(query, "recent_completed AS (") {
		t.Fatalf("series-scoped query must not bound the anchor scan, got:\n%s", query)
	}
	if strings.Contains(query, "WITH RECURSIVE") {
		t.Fatalf("series-scoped query must keep its non-recursive SQL shape, got:\n%s", query)
	}
	if !strings.Contains(query, "AND e.series_id = $4") {
		t.Fatalf("series-scoped query must filter by series_id, got:\n%s", query)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args with SeriesID, got %d (%v)", len(args), args)
	}

	queryWithCursor, argsWithCursor := buildListNextUpQuery(q, 20, &nextUpWalkCursor{
		updatedAt:   time.Now().UTC(),
		mediaItemID: "ignored-episode",
		seen:        []string{"ignored-series"},
	})
	if queryWithCursor != query || fmt.Sprint(argsWithCursor) != fmt.Sprint(args) {
		t.Fatalf("series-scoped SQL and args must be byte-identical when a walk cursor is supplied")
	}
}

func TestBuildListNextUpQuery_DroppedSeriesExcludedFromEveryWalkStep(t *testing.T) {
	t.Parallel()

	dropped := []string{"series-dropped"}
	query, args := buildListNextUpQuery(NextUpQuery{
		UserID:           7,
		ProfileID:        "profile-1",
		droppedSeriesIDs: dropped,
	}, 20, &nextUpWalkCursor{mediaItemID: "m", seen: []string{"s"}})
	if got := strings.Count(query, "AND NOT (e.series_id = ANY($7::text[]))"); got != 2 {
		t.Fatalf("expected dropped exclusion in seed and recursive step, got %d:\n%s", got, query)
	}
	if len(args) != 7 {
		t.Fatalf("expected dropped IDs as the seventh arg, got %v", args)
	}

	withoutDrops, _ := buildListNextUpQuery(NextUpQuery{UserID: 7, ProfileID: "profile-1"}, 20, nil)
	if strings.Contains(withoutDrops, "::text[]))") {
		t.Fatalf("query without drops must not carry the exclusion:\n%s", withoutDrops)
	}

	scoped, _ := buildListNextUpQuery(NextUpQuery{UserID: 7, ProfileID: "profile-1", SeriesID: "series-dropped", droppedSeriesIDs: dropped}, 20, nil)
	if strings.Contains(scoped, "ANY($") {
		t.Fatalf("series-scoped query must not exclude drops:\n%s", scoped)
	}

	resumable, resumableArgs := buildListResumableFirstEpisodesQuery(NextUpQuery{UserID: 7, ProfileID: "profile-1", droppedSeriesIDs: dropped}, []string{"ep"})
	if !strings.Contains(resumable, "AND NOT (e.series_id = ANY($4::text[]))") || len(resumableArgs) != 4 {
		t.Fatalf("global resumable query must exclude drops, got args %v:\n%s", resumableArgs, resumable)
	}
}
