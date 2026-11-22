package catalog

import (
	"reflect"
	"strings"
	"testing"
)

func TestQueryCursorTitleSeekIsIndexBounded(t *testing.T) {
	const key = "LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title), ''), mi.title))"
	after := &QueryCursor{Keys: []QueryCursorValue{{Kind: cursorKindText, Value: new("m")}, {Kind: cursorKindText, Value: new("movie-1")}}}
	for _, tc := range []struct{ order, bound string }{{"asc", key + " >= $3::text AND "}, {"desc", key + " <= $3::text AND "}} {
		t.Run(tc.order, func(t *testing.T) {
			plan, err := (&QueryExecutor{}).buildPreviewPagePlan(QueryDefinition{Sort: QuerySort{Field: "title", Order: tc.order}}, AccessFilter{}, 60, 0)
			if err != nil {
				t.Fatal(err)
			}
			seek, args, err := cursorSeekSQL(plan.cursorTerms, after, 3)
			if err != nil {
				t.Fatal(err)
			}
			// The bound lets PostgreSQL start idx_media_items_sort_key at the
			// cursor; the arms keep the exact tuple order. Neither title nor
			// content_id can be NULL on media_items, so no NULL arms remain.
			if !strings.HasPrefix(seek, "("+tc.bound) || strings.Contains(seek, "IS NULL") {
				t.Fatalf("seek = %s", seek)
			}
			if !reflect.DeepEqual(args, []any{"m", "movie-1"}) {
				t.Fatalf("args = %#v", args)
			}
		})
	}

	// Episode titles may be NULL, so the derived relation keeps the NULL arms
	// and gets no bound.
	plan, err := (&QueryExecutor{Scope: "episode"}).buildPreviewPagePlan(QueryDefinition{MediaScope: "episode", Sort: QuerySort{Field: "title", Order: "asc"}}, AccessFilter{}, 60, 0)
	if err != nil {
		t.Fatal(err)
	}
	seek, _, err := cursorSeekSQL(plan.cursorTerms, after, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(seek, "(((mi.sort_key > $1::text OR mi.sort_key IS NULL))") {
		t.Fatalf("episode seek = %s", seek)
	}
}
