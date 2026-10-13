package sections

import (
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestEffectiveFetchLibraryIDsUsesAllowedLibraries(t *testing.T) {
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{7, 8}}

	got := effectiveFetchLibraryIDs(nil, filter)

	if !slices.Equal(got, []int{7, 8}) {
		t.Fatalf("effective library IDs = %#v, want [7 8]", got)
	}
}

func TestEffectiveFetchLibraryIDsKeepsExplicitScope(t *testing.T) {
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{7, 8}}

	got := effectiveFetchLibraryIDs([]int{3}, filter)

	if !slices.Equal(got, []int{3}) {
		t.Fatalf("effective library IDs = %#v, want [3]", got)
	}
}

func TestEffectiveFetchLibraryIDsPreservesEmptyAllowedScope(t *testing.T) {
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{}}

	got := effectiveFetchLibraryIDs(nil, filter)

	if got == nil {
		t.Fatalf("effective library IDs = nil, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("effective library IDs = %#v, want empty slice", got)
	}
}

func TestApplyEpisodeTargetLibraryAccessRejectsAnyDisabledSeriesMembership(t *testing.T) {
	var conditions []string
	var args []any
	argIdx := 2

	ok := applyEpisodeTargetLibraryAccess(
		catalog.AccessFilter{DisabledLibraryIDs: []int{9}},
		nil,
		nil,
		&conditions,
		&args,
		&argIdx,
	)
	if !ok {
		t.Fatal("disabled-only access unexpectedly returned an empty scope")
	}

	where := strings.Join(conditions, " AND ")
	if !strings.Contains(where, "EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = si.content_id)") {
		t.Fatalf("episode hydration must require positive series membership, got %s", where)
	}
	if !strings.Contains(where, "NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = si.content_id AND mil.media_folder_id = ANY($2))") {
		t.Fatalf("episode hydration must reject any disabled series membership, got %s", where)
	}
	if len(args) != 1 || !slices.Equal(args[0].([]int), []int{9}) || argIdx != 3 {
		t.Fatalf("args = %v, argIdx = %d; want disabled list at $2 and next index 3", args, argIdx)
	}
}

func TestApplyEpisodeTargetLibraryAccessComposesAllowedAndDisabledMembership(t *testing.T) {
	var conditions []string
	var args []any
	argIdx := 1

	ok := applyEpisodeTargetLibraryAccess(
		catalog.AccessFilter{AllowedLibraryIDs: []int{7}, DisabledLibraryIDs: []int{9}},
		nil,
		nil,
		&conditions,
		&args,
		&argIdx,
	)
	if !ok {
		t.Fatal("allowed library unexpectedly returned an empty scope")
	}

	where := strings.Join(conditions, " AND ")
	if !strings.Contains(where, "mil.media_folder_id = ANY($1)") {
		t.Fatalf("episode hydration must require allowed series membership, got %s", where)
	}
	if !strings.Contains(where, "NOT EXISTS") || !strings.Contains(where, "mil.media_folder_id = ANY($2)") {
		t.Fatalf("episode hydration must independently reject disabled series membership, got %s", where)
	}
	if len(args) != 2 || argIdx != 3 {
		t.Fatalf("args = %v, argIdx = %d; want allowed and disabled lists with next index 3", args, argIdx)
	}
}

func TestApplySectionLibraryScopeToQuery(t *testing.T) {
	seven := 7
	cases := []struct {
		name       string
		def        []int
		libraryID  *int
		libraryIDs []int
		wantIDs    []int
		wantOK     bool
	}{
		{name: "unrestricted keeps the query's libraries", def: []int{1, 2}, wantIDs: []int{1, 2}, wantOK: true},
		{name: "pinned library wins", def: []int{1}, libraryID: &seven, wantIDs: []int{7}, wantOK: true},
		{name: "no query libraries take the viewer's", libraryIDs: []int{3, 4}, wantIDs: []int{3, 4}, wantOK: true},
		{name: "overlap keeps the shared libraries", def: []int{1, 3}, libraryIDs: []int{3, 4}, wantIDs: []int{3}, wantOK: true},
		// The query asked for library 1 and the viewer cannot reach it: the
		// section must match nothing rather than fall back to every library
		// the viewer can see.
		{name: "disjoint libraries match nothing", def: []int{1}, libraryIDs: []int{3}, wantOK: false},
		{name: "nonexistent query library matches nothing", def: []int{999}, libraryIDs: []int{3}, wantOK: false},
		// An empty, non-nil scope means the viewer can reach no library; it
		// must not read as "every library".
		{name: "empty scope matches nothing", libraryIDs: []int{}, wantOK: false},
		{name: "empty scope with query libraries matches nothing", def: []int{1}, libraryIDs: []int{}, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := applySectionLibraryScopeToQuery(catalog.QueryDefinition{LibraryIDs: tc.def}, tc.libraryID, tc.libraryIDs)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (libraries %v)", ok, tc.wantOK, got.LibraryIDs)
			}
			if ok && !slices.Equal(got.LibraryIDs, tc.wantIDs) {
				t.Fatalf("libraries = %v, want %v", got.LibraryIDs, tc.wantIDs)
			}
		})
	}
}
