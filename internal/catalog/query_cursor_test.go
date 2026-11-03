package catalog

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestQueryCursorRejectsChangedEpisodeDateOrder(t *testing.T) {
	for _, field := range []string{"release_date", "last_air_date"} {
		for _, order := range []string{"asc", "desc"} {
			t.Run(field+"/"+order, func(t *testing.T) {
				plan, err := NewQueryBuilder("mi").WithMediaScope("episode").BuildSortPlan(QuerySort{Field: field, Order: order})
				if err != nil {
					t.Fatal(err)
				}
				// A cursor issued under the old (date, title, id) order.
				previous := &QueryCursor{Keys: []QueryCursorValue{{Kind: cursorKindDate}, {Kind: cursorKindText}, {Kind: cursorKindText}}}
				if _, _, err := cursorSeekSQL(plan.terms, previous, 1); !errors.Is(err, ErrCatalogCursorChanged) {
					t.Fatalf("old cursor error = %v, want ErrCatalogCursorChanged", err)
				}
				// Right length, but the episode numbers were stored as text.
				previous.Keys = append(previous.Keys, QueryCursorValue{Kind: cursorKindText}, QueryCursorValue{Kind: cursorKindText}, QueryCursorValue{Kind: cursorKindText})
				if _, _, err := cursorSeekSQL(plan.terms, previous, 1); !errors.Is(err, ErrCatalogCursorChanged) {
					t.Fatalf("wrong key type error = %v, want ErrCatalogCursorChanged", err)
				}
			})
		}
	}
}

func TestQueryCursorSortTerms(t *testing.T) {
	for field := range querySortDefs {
		for _, order := range []string{"asc", "desc"} {
			t.Run(field+order, func(t *testing.T) {
				plan, err := NewQueryBuilder("mi").WithUserScope(1, "p").BuildSortPlan(QuerySort{Field: field, Order: order})
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.terms) < 2 || plan.terms[len(plan.terms)-1].expression != "mi.content_id" {
					t.Fatalf("missing identity tuple: %#v", plan.terms)
				}
				parts := []string{}
				for _, term := range plan.terms {
					dir := "ASC"
					if term.descending {
						dir = "DESC"
					}
					p := term.expression + " " + dir
					if term.nullsLast && term.descending {
						p += " NULLS LAST"
					}
					parts = append(parts, p)
				}
				// Explicit ASC NULLS LAST is equivalent to PostgreSQL's ASC default.
				actual := strings.ReplaceAll(plan.OrderBy, " ASC NULLS LAST", " ASC")
				if actual != "ORDER BY "+strings.Join(parts, ", ") {
					t.Fatalf("tuple differs: %s vs %s", actual, strings.Join(parts, ", "))
				}
			})
		}
	}
}

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
