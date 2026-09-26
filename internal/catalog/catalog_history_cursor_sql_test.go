package catalog

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// orderByClause returns everything from the last ORDER BY to the trailing LIMIT.
func orderByClause(t *testing.T, sql string) string {
	t.Helper()
	idx := strings.LastIndex(sql, "ORDER BY")
	if idx < 0 {
		t.Fatalf("no ORDER BY in %s", sql)
	}
	clause := sql[idx:]
	if limit := strings.LastIndex(clause, " LIMIT "); limit >= 0 {
		clause = clause[:limit]
	}
	return clause
}

func historyCursorExecutor(t *testing.T, req CatalogRequest, access AccessFilter) *QueryExecutor {
	t.Helper()
	snapshot := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	executor := &QueryExecutor{Scope: req.Query.MediaScope, SnapshotAt: &snapshot}
	applyHistoryCursorSource(executor, req, access, snapshot)
	return executor
}

// The history keyset order must read its viewed-at key from the joined history
// projection. A correlated per-row subquery re-runs the whole watch-history
// aggregate for every candidate row, which is what made
// /api/v2/catalog?source=history time out on large profiles.
func TestHistoryCursorOrdersFromJoinNotCorrelatedSubquery(t *testing.T) {
	access := AccessFilter{UserID: 7, ProfileID: "profile-1"}
	for _, tc := range []struct {
		name       string
		req        CatalogRequest
		descending bool
	}{
		{name: "source order", req: CatalogRequest{Source: CatalogSourceHistory, UseSourceOrder: true}, descending: true},
		{name: "date_viewed desc", req: CatalogRequest{Source: CatalogSourceHistory, Query: QueryDefinition{Sort: QuerySort{Field: historyDateViewedSort}}}, descending: true},
		{name: "date_viewed asc", req: CatalogRequest{Source: CatalogSourceHistory, Query: QueryDefinition{Sort: QuerySort{Field: historyDateViewedSort, Order: historyAscendingOrder}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := historyCursorExecutor(t, tc.req, access)
			plan, err := executor.buildPreviewPagePlan(tc.req.Query, access, 20, 0)
			if err != nil {
				t.Fatalf("buildPreviewPagePlan: %v", err)
			}
			sql, args, err := plan.cursorSQL(nil)
			if err != nil {
				t.Fatalf("cursorSQL: %v", err)
			}
			order := orderByClause(t, sql)
			if strings.Contains(strings.ToUpper(order), "SELECT") {
				t.Fatalf("ORDER BY still contains a subquery: %s", order)
			}
			if strings.Contains(order, "user_watch_history") {
				t.Fatalf("ORDER BY still re-reads the history table: %s", order)
			}
			wantKey := historyCursorAlias + ".watched_at"
			if !strings.HasPrefix(order, "ORDER BY "+wantKey+" ") {
				t.Fatalf("ORDER BY does not lead with the joined history key: %s", order)
			}
			direction := "ASC"
			if tc.descending {
				direction = "DESC"
			}
			if !strings.Contains(order, wantKey+" "+direction) {
				t.Fatalf("ORDER BY %s is not %s: %s", wantKey, direction, order)
			}
			if !strings.Contains(order, cursorContentIDExpression+" ASC") {
				t.Fatalf("ORDER BY lost the content-id tie breaker: %s", order)
			}
			if !strings.Contains(sql, "JOIN "+historyCursorCTE+" "+historyCursorAlias+" ON "+historyCursorAlias+".display_id = mi.content_id") {
				t.Fatalf("paged query does not join the history projection: %s", sql)
			}
			if !strings.Contains(sql, "WITH "+historyCursorCTE+" AS (") {
				t.Fatalf("paged query does not project history once: %s", sql)
			}
			if strings.Count(sql, "FROM user_watch_history") != 1 {
				t.Fatalf("watch history is read more than once: %s", sql)
			}
			assertPlaceholdersBound(t, sql, len(args))
		})
	}
}

// Membership still filters on the same relation, and non-history sorts keep
// the join (it is the source predicate) without borrowing its ordering key.
func TestHistoryCursorTitleSortKeepsJoinWithoutHistoryOrder(t *testing.T) {
	req := CatalogRequest{Source: CatalogSourceHistory, Query: QueryDefinition{Sort: QuerySort{Field: querySortTitle}}}
	access := AccessFilter{UserID: 7, ProfileID: "profile-1"}
	executor := historyCursorExecutor(t, req, access)
	if len(executor.SourceOrder) != 0 {
		t.Fatalf("title sort must not use the history order: %+v", executor.SourceOrder)
	}
	plan, err := executor.buildPreviewPagePlan(req.Query, access, 20, 0)
	if err != nil {
		t.Fatalf("buildPreviewPagePlan: %v", err)
	}
	sql, args, err := plan.cursorSQL(nil)
	if err != nil {
		t.Fatalf("cursorSQL: %v", err)
	}
	if !strings.Contains(sql, "JOIN "+historyCursorCTE+" "+historyCursorAlias) {
		t.Fatalf("history membership lost its join: %s", sql)
	}
	if strings.Contains(orderByClause(t, sql), historyCursorAlias) {
		t.Fatalf("title sort borrowed the history order: %s", sql)
	}
	countSQL, countArgs := plan.countSQL()
	if !strings.Contains(countSQL, "JOIN "+historyCursorCTE+" "+historyCursorAlias) {
		t.Fatalf("count query lost history membership: %s", countSQL)
	}
	assertPlaceholdersBound(t, sql, len(args))
	assertPlaceholdersBound(t, countSQL, len(countArgs))
}

// A cursor continuation must seek on the same joined key it ordered by, so the
// round trip stays on one stable (viewed_at, content_id) tuple.
func TestHistoryCursorSeekUsesJoinedKey(t *testing.T) {
	req := CatalogRequest{Source: CatalogSourceHistory, UseSourceOrder: true}
	access := AccessFilter{UserID: 7, ProfileID: "profile-1"}
	executor := historyCursorExecutor(t, req, access)
	plan, err := executor.buildPreviewPagePlan(req.Query, access, 20, 0)
	if err != nil {
		t.Fatalf("buildPreviewPagePlan: %v", err)
	}
	watched := "2026-09-01T10:00:00Z"
	contentID := "movie-tmdb-1"
	after := &QueryCursor{Keys: []QueryCursorValue{
		{Kind: cursorKindTimestamp, Value: &watched},
		{Kind: cursorKindText, Value: &contentID},
	}}
	sql, args, err := plan.cursorSQL(after)
	if err != nil {
		t.Fatalf("cursorSQL: %v", err)
	}
	seek := fmt.Sprintf("%s.watched_at < $%d::timestamptz", historyCursorAlias, len(args)-2)
	if !strings.Contains(sql, seek) {
		t.Fatalf("seek predicate %q missing from %s", seek, sql)
	}
	if strings.Count(sql, "FROM user_watch_history") != 1 {
		t.Fatalf("seek re-reads the history table: %s", sql)
	}
	assertPlaceholdersBound(t, sql, len(args))
}

func assertPlaceholdersBound(t *testing.T, sql string, argCount int) {
	t.Helper()
	seen := map[int]bool{}
	for _, match := range sqlPlaceholderPattern.FindAllStringSubmatch(sql, -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("bad placeholder %s", match[0])
		}
		if n < 1 || n > argCount {
			t.Fatalf("placeholder $%d has no argument (%d bound): %s", n, argCount, sql)
		}
		seen[n] = true
	}
	for n := 1; n <= argCount; n++ {
		if !seen[n] {
			t.Fatalf("argument $%d is bound but unused: %s", n, sql)
		}
	}
}
