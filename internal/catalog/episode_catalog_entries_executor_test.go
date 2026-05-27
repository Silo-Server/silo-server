package catalog

import (
	"strings"
	"testing"
)

func TestEpisodeCatalogEntryOrderByUsesReadModelColumns(t *testing.T) {
	tests := []struct {
		name string
		sort QuerySort
		want []string
	}{
		{
			name: "resolution",
			sort: QuerySort{Field: "resolution", Order: "desc"},
			want: []string{"ece.max_resolution_rank DESC NULLS LAST", "ece.sort_key ASC", "ece.episode_id ASC"},
		},
		{
			name: "bitrate",
			sort: QuerySort{Field: "bitrate", Order: "desc"},
			want: []string{"ece.max_bitrate DESC NULLS LAST", "ece.sort_key ASC", "ece.episode_id ASC"},
		},
		{
			name: "date added",
			sort: QuerySort{Field: "added_at", Order: "desc"},
			want: []string{"ece.added_at DESC", "ece.sort_key ASC", "ece.episode_id ASC"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orderBy, ok := episodeCatalogEntryOrderBy(tt.sort)
			if !ok {
				t.Fatalf("episodeCatalogEntryOrderBy(%+v) did not produce a plan", tt.sort)
			}
			for _, want := range tt.want {
				if !strings.Contains(orderBy, want) {
					t.Fatalf("ORDER BY %q does not contain %q", orderBy, want)
				}
			}
		})
	}
}

func TestBuildEpisodeCatalogEntryQueryWhereUsesReadModelFilters(t *testing.T) {
	def := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{
			Match: "all",
			Rules: []QueryRule{
				{Field: "genre", Op: "is", Value: "Comedy"},
				{Field: "content_rating", Op: "is", Value: "TV-14"},
				{Field: "year", Op: "between", Value: []any{2015, 2016}},
			},
		}},
	}.Normalize()

	where, args, nextArgIdx, ok, err := buildEpisodeCatalogEntryQueryWhere(def, 2)
	if err != nil {
		t.Fatalf("buildEpisodeCatalogEntryQueryWhere returned error: %v", err)
	}
	if !ok {
		t.Fatal("buildEpisodeCatalogEntryQueryWhere unexpectedly fell back")
	}
	for _, want := range []string{
		"ece.genres @> ARRAY[$2]::text[]",
		"ece.content_rating = $3",
		"ece.year >= $4 AND ece.year <= $5",
	} {
		if !strings.Contains(where, want) {
			t.Fatalf("WHERE %q does not contain %q", where, want)
		}
	}
	if nextArgIdx != 6 {
		t.Fatalf("nextArgIdx = %d, want 6", nextArgIdx)
	}
	if len(args) != 4 {
		t.Fatalf("len(args) = %d, want 4", len(args))
	}
}

func TestEpisodeCatalogEntryQueryFallsBackForSameFileTechnicalAnd(t *testing.T) {
	group := QueryGroup{
		Match: "all",
		Rules: []QueryRule{
			{Field: "resolution", Op: "is", Value: "2160p"},
			{Field: "audio_language", Op: "is", Value: "en"},
		},
	}

	_, _, _, ok, err := buildEpisodeCatalogEntryGroupWhere(group, 1)
	if err != nil {
		t.Fatalf("buildEpisodeCatalogEntryGroupWhere returned error: %v", err)
	}
	if ok {
		t.Fatal("same-file technical AND should fall back to the generic media_files EXISTS path")
	}
}

func TestExtractEpisodeCatalogUserStatePlanForLastWatched(t *testing.T) {
	def := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{
			Match: "all",
			Rules: []QueryRule{
				{Field: "last_watched", Op: "in_last", Value: "30d"},
				{Field: "genre", Op: "is", Value: "Comedy"},
			},
		}},
		Sort: QuerySort{Field: "title", Order: "asc"},
	}.Normalize()

	plan, ok, err := extractEpisodeCatalogUserStatePlan(def)
	if err != nil {
		t.Fatalf("extractEpisodeCatalogUserStatePlan returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected last_watched filter to use a user-state plan")
	}
	if plan.source != "viewed" || plan.alias != "uv" || !plan.positiveSource {
		t.Fatalf("unexpected user-state plan: %+v", plan)
	}
	if len(plan.userClauses) != 1 || !strings.Contains(plan.userClauses[0], "uv.last_watched") {
		t.Fatalf("unexpected user clauses: %#v", plan.userClauses)
	}
	if len(plan.entryDef.Groups) != 1 || len(plan.entryDef.Groups[0].Rules) != 1 {
		t.Fatalf("expected genre rule to remain in entry def, got %+v", plan.entryDef.Groups)
	}
}
