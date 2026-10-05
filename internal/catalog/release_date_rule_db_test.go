package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// The release_date rule reads release_date when set and otherwise a
// well-formed first_air_date; a release_date outside the range is not rescued
// by an in-range first_air_date.
func TestReleaseDateRuleMatchesEffectiveDateDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var lib int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('mixed', $1, true) RETURNING id`,
		fmt.Sprintf("release-rule-%d", suffix)).Scan(&lib); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	type seed struct {
		name, kind   string
		releaseDate  any
		firstAirDate any
		match        bool
	}
	seeds := []seed{
		{"release-in", "movie", "2020-06-01", nil, true},
		{"release-out-air-in", "movie", "2010-06-01", "2020-06-01", false},
		{"release-in-air-out", "movie", "2020-06-01", "2010-06-01", true},
		{"air-in", "series", nil, " 2020-06-01 ", true},
		{"air-out", "series", nil, "2010-06-01", false},
		{"air-malformed", "series", nil, "June 2020", false},
		{"air-blank", "series", nil, "  ", false},
		{"neither", "series", nil, nil, false},
	}
	var ids, want []string
	for _, s := range seeds {
		id := fmt.Sprintf("release-rule-%d-%s", suffix, s.name)
		ids = append(ids, id)
		if s.match {
			want = append(want, id)
		}
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title, release_date, first_air_date) VALUES ($1, $2, $1, $3::date, $4)`, id, s.kind, s.releaseDate, s.firstAirDate)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id, lib)
	}
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = $1`, lib)
	})

	executor := &QueryExecutor{Pool: pool}
	for _, rule := range []QueryRule{
		{Field: "release_date", Op: "between", Value: []any{"2019-01-01", "2021-12-31"}},
		{Field: "release_date", Op: "gt", Value: "2019-01-01"},
	} {
		def := QueryDefinition{LibraryIDs: []int{lib}, Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{rule}}}}.Normalize()
		page, err := executor.PreviewCursorPage(ctx, ApplySmartCollectionItemLimit(def), AccessFilter{}, 50, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, item := range page.Items {
			got = append(got, item.ContentID)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) || page.Total != len(want) {
			t.Fatalf("%s matched %v (total %d), want %v", rule.Op, got, page.Total, want)
		}
	}
}
