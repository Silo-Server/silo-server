package catalog

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildLibraryScopeJoin(t *testing.T) {
	for _, tc := range []struct {
		name              string
		allowed, disabled []int
		start             int
		itemType          string
		wantArgs          int
		args              [][]int // leading args, each a library ID slice
		contains          []string
	}{
		{
			name: "allowed libraries", allowed: []int{3, 7}, start: 4, wantArgs: 1,
			args:     [][]int{{3, 7}},
			contains: []string{"EXISTS (", "FROM media_item_libraries", "media_folder_id = ANY($4)"},
		},
		{
			name: "disabled libraries", disabled: []int{9}, start: 2, wantArgs: 1,
			args:     [][]int{{9}},
			contains: []string{"NOT EXISTS (", "media_folder_id = ANY($2)"},
		},
		{
			name: "allowed and disabled libraries", allowed: []int{1, 2}, disabled: []int{8, 9}, start: 1, wantArgs: 2,
			args:     [][]int{{1, 2}, {8, 9}},
			contains: []string{"EXISTS (", "NOT EXISTS (", "media_folder_id = ANY($1)", "media_folder_id = ANY($2)"},
		},
		{
			// Episode scope also requires parent-series membership: episode and series allowed-library args.
			name: "episode scope uses episode library membership", allowed: []int{4}, start: 2, itemType: "episode", wantArgs: 2,
			args:     [][]int{{4}},
			contains: []string{"FROM episode_libraries", ".episode_id = mi.content_id", episodeParentSeriesIDExpr("mi.content_id")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			whereSQL, args, ok := buildLibraryScopeJoin(tc.allowed, tc.disabled, tc.start, tc.itemType, "mi.content_id")
			if !ok {
				t.Fatal("expected join to be enabled")
			}
			if len(args) != tc.wantArgs {
				t.Fatalf("args = %v, want %d slice args", args, tc.wantArgs)
			}
			for i, want := range tc.args {
				if got, ok := args[i].([]int); !ok || !slices.Equal(got, want) {
					t.Fatalf("args[%d] = %v, want %v", i, args[i], want)
				}
			}
			for _, want := range tc.contains {
				if !strings.Contains(whereSQL, want) {
					t.Fatalf("expected %q in %q", want, whereSQL)
				}
			}
		})
	}
}

func TestEpisodeCatalogBaseRelationForLibraries_UsesEpisodeLibraries(t *testing.T) {
	relation, args, handled := episodeCatalogBaseRelationForLibraries([]int{2, 7}, []int{9}, 3)
	if !handled {
		t.Fatal("expected episode library scope to be handled in the base relation")
	}
	if len(args) != 4 {
		t.Fatalf("args = %v, want 4 slice args (episode allow/deny and series allow/deny)", args)
	}
	if got, ok := args[0].([]int); !ok || len(got) != 2 || got[0] != 2 || got[1] != 7 {
		t.Fatalf("args[0] = %v, want []int{2, 7}", args[0])
	}
	if got, ok := args[1].([]int); !ok || len(got) != 1 || got[0] != 9 {
		t.Fatalf("args[1] = %v, want []int{9}", args[1])
	}
	if !strings.Contains(relation, "el_scope_in.episode_id = e.content_id") {
		t.Fatalf("expected allowed episode membership in relation, got %q", relation)
	}
	if !strings.Contains(relation, "el_scope_in.media_folder_id = ANY($3)") {
		t.Fatalf("expected allowed library = ANY filter, got %q", relation)
	}
	if !strings.Contains(relation, "NOT EXISTS (") || !strings.Contains(relation, "el_scope_out.media_folder_id = ANY($4)") {
		t.Fatalf("expected independent disabled-library NOT EXISTS filter, got %q", relation)
	}
	if !strings.Contains(relation, "mil.content_id = e.series_id") {
		t.Fatalf("episode catalog must also gate on parent-series membership, got %q", relation)
	}
	if !strings.Contains(relation, "mil.media_folder_id = ANY($5)") {
		t.Fatalf("expected series allowed-library placeholder, got %q", relation)
	}
	if !strings.Contains(relation, "mil.media_folder_id = ANY($6)") {
		t.Fatalf("expected series disabled-library placeholder, got %q", relation)
	}
	if strings.Contains(relation, "NOT (el.media_folder_id = ANY(") {
		t.Fatalf("row-local disabled-library predicate leaks dual-membership episodes, got %q", relation)
	}
	if strings.Contains(relation, "media_files mf") {
		t.Fatalf("expected relation to avoid media_files, got %q", relation)
	}
}

func TestEpisodeCatalogBaseRelationForLibraries_DisabledOnlyRequiresMembership(t *testing.T) {
	relation, args, handled := episodeCatalogBaseRelationForLibraries(nil, []int{9}, 1)
	if !handled {
		t.Fatal("expected disabled episode library scope to be handled")
	}
	if !strings.Contains(relation, episodeCatalogActiveLibraryExists) {
		t.Fatalf("disabled-only episode scope must require positive membership, got %q", relation)
	}
	if !strings.Contains(relation, "NOT EXISTS (") || !strings.Contains(relation, "el_scope_out.media_folder_id = ANY($1)") {
		t.Fatalf("disabled-only episode scope must reject any disabled membership, got %q", relation)
	}
	if !strings.Contains(relation, "mil.content_id = e.series_id") {
		t.Fatalf("disabled-only episode scope must also reject disabled parent-series membership, got %q", relation)
	}
	if len(args) != 2 {
		t.Fatalf("args = %v, want episode and series disabled-library arguments", args)
	}
}

func TestRebindSQLPlaceholders(t *testing.T) {
	got := rebindSQLPlaceholders("mi.created_at <= $1 AND mi.year >= $2", 3)
	want := "mi.created_at <= $4 AND mi.year >= $5"
	if got != want {
		t.Fatalf("rebindSQLPlaceholders() = %q, want %q", got, want)
	}
}
