package catalog

import (
	"reflect"
	"testing"
	"time"
)

func TestDeferredEpisodeHydrationMatchesOriginalPage(t *testing.T) {
	repo, seriesID, _ := seedCompletionEpisodes(t, 9)
	_, otherSeriesID, _ := seedCompletionEpisodes(t, 5)
	ctx := t.Context()
	var folderID int
	if err := repo.pool.QueryRow(ctx, `SELECT media_folder_id FROM episode_libraries el JOIN episodes e ON e.content_id=el.episode_id WHERE e.series_id=$1 ORDER BY media_folder_id LIMIT 1`, seriesID).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, []any{seriesID, folderID}},
		{`UPDATE media_items SET content_rating=CASE WHEN content_id=$1 THEN 'PG' ELSE 'R' END WHERE content_id=ANY($2)`, []any{seriesID, []string{seriesID, otherSeriesID}}},
		{`UPDATE episodes SET title=CASE WHEN episode_number%3=0 THEN '' WHEN episode_number%2=0 THEN 'Same Title' ELSE title END,
    created_at='2026-01-01'::timestamptz + episode_number*interval '1 hour' WHERE series_id=ANY($1)`, []any{[]string{seriesID, otherSeriesID}}},
	} {
		if _, err := repo.pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := repo.ListIDsBySeriesIDs(ctx, []string{seriesID, otherSeriesID})
	if err != nil {
		t.Fatal(err)
	}
	ids := append(rows[seriesID], rows[otherSeriesID]...)
	cutoff := time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		access   AccessFilter
		snapshot *time.Time
		cap      *int
	}{
		{name: "unrestricted", access: AccessFilter{AllowedContentIDs: ids}},
		{name: "rating", access: AccessFilter{AllowedContentIDs: ids, MaxContentRating: "PG"}},
		{name: "library", access: AccessFilter{AllowedContentIDs: ids, AllowedLibraryIDs: []int{folderID}}},
		{name: "denied-library", access: AccessFilter{AllowedContentIDs: ids, DisabledLibraryIDs: []int{folderID}}},
		{name: "no-libraries", access: AccessFilter{AllowedContentIDs: ids, AllowedLibraryIDs: []int{}}},
		{name: "no-items", access: AccessFilter{AllowedContentIDs: []string{}}},
		{name: "title-prefix", access: AccessFilter{AllowedContentIDs: ids, NamePrefix: "Same"}},
		{name: "snapshot", access: AccessFilter{AllowedContentIDs: ids}, snapshot: &cutoff},
		{name: "capped", access: AccessFilter{AllowedContentIDs: ids}, cap: new(4)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, order := range []string{"asc", "desc"} {
				for _, offset := range []int{1, 4, 20} {
					def := QueryDefinition{MediaScope: "episode", Sort: QuerySort{Field: "title", Order: order}, Limit: tc.cap}
					executor := &QueryExecutor{Pool: repo.pool, Scope: "episode", SnapshotAt: tc.snapshot}
					plan, err := executor.buildPreviewPagePlan(def, tc.access, 3, offset)
					if err != nil {
						t.Fatal(err)
					}
					if !plan.deferEpisodeHydration {
						t.Fatal("expected deferred hydration")
					}
					original := plan
					original.deferEpisodeHydration = false
					for _, includeTotal := range []bool{false, true} {
						got, total, more, err := executor.executePreviewPagePlan(t.Context(), plan, includeTotal)
						if err != nil {
							t.Fatal(err)
						}
						want, wantTotal, wantMore, err := executor.executePreviewPagePlan(t.Context(), original, includeTotal)
						if err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(got, want) || total != wantTotal || more != wantMore {
							t.Fatalf("page changed: order=%s offset=%d includeTotal=%v; got total=%d more=%v, want total=%d more=%v", order, offset, includeTotal, total, more, wantTotal, wantMore)
						}
					}
				}
			}
		})
	}
	// A metadata edit between requests must be visible; there is no added cache.
	if _, err := repo.pool.Exec(ctx, `UPDATE episodes SET overview='Updated overview' WHERE series_id=$1`, seriesID); err != nil {
		t.Fatal(err)
	}
	items, _, _, err := (&QueryExecutor{Pool: repo.pool, Scope: "episode"}).PreviewPage(t.Context(), QueryDefinition{MediaScope: "episode", Sort: QuerySort{Field: "title", Order: "asc"}}, AccessFilter{AllowedContentIDs: rows[seriesID]}, 3, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected an updated page")
	}
	for _, item := range items {
		if item.Overview != "Updated overview" {
			t.Fatal("stale metadata")
		}
	}
}

func TestDeferredEpisodeHydrationScope(t *testing.T) {
	for _, tc := range []struct {
		scope, sort string
		offset      int
		want        bool
	}{
		{"episode", "title", 20, true}, {"episode", "title", 0, false},
		{"movie", "title", 20, false}, {"manga", "title", 20, false},
		{"episode", "year", 20, false}, {"episode", "added_at", 20, false},
	} {
		plan, err := (&QueryExecutor{Scope: tc.scope}).buildPreviewPagePlan(QueryDefinition{MediaScope: tc.scope, Sort: QuerySort{Field: tc.sort}}, AccessFilter{}, 20, tc.offset)
		if err != nil {
			t.Fatal(err)
		}
		if plan.deferEpisodeHydration != tc.want {
			t.Fatalf("scope=%s sort=%s offset=%d: deferred=%v, want %v", tc.scope, tc.sort, tc.offset, plan.deferEpisodeHydration, tc.want)
		}
	}
}
