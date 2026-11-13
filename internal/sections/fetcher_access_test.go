package sections

import (
	"slices"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestEffectiveFetchLibraryIDs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		explicit []int
		allowed  []int
		want     []int
	}{
		{name: "uses allowed libraries", allowed: []int{7, 8}, want: []int{7, 8}},
		{name: "keeps explicit scope", explicit: []int{3}, allowed: []int{7, 8}, want: []int{3}},
		// An empty allowed scope stays empty and non-nil: nil would read as every library.
		{name: "preserves empty allowed scope", allowed: []int{}, want: []int{}},
	} {
		got := effectiveFetchLibraryIDs(tc.explicit, catalog.AccessFilter{AllowedLibraryIDs: tc.allowed})
		if got == nil || !slices.Equal(got, tc.want) {
			t.Fatalf("%s: effective library IDs = %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

func TestApplyEpisodeTargetLibraryAccess(t *testing.T) {
	for _, tc := range []struct {
		name      string
		filter    catalog.AccessFilter
		argIdx    int
		wantWhere []string
		wantArgs  int
		firstArg  []int
	}{
		{
			// Episode hydration requires positive series membership and rejects
			// any disabled series membership.
			name:   "rejects any disabled series membership",
			filter: catalog.AccessFilter{DisabledLibraryIDs: []int{9}},
			argIdx: 2,
			wantWhere: []string{
				"EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = si.content_id)",
				"NOT EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = si.content_id AND mil.media_folder_id = ANY($2))",
			},
			wantArgs: 1,
			firstArg: []int{9},
		},
		{
			// Allowed series membership is required and disabled membership is
			// rejected independently.
			name:      "composes allowed and disabled membership",
			filter:    catalog.AccessFilter{AllowedLibraryIDs: []int{7}, DisabledLibraryIDs: []int{9}},
			argIdx:    1,
			wantWhere: []string{"mil.media_folder_id = ANY($1)", "NOT EXISTS", "mil.media_folder_id = ANY($2)"},
			wantArgs:  2,
		},
	} {
		var conditions []string
		var args []any
		argIdx := tc.argIdx
		if !applyEpisodeTargetLibraryAccess(tc.filter, nil, nil, &conditions, &args, &argIdx) {
			t.Fatalf("%s: unexpectedly returned an empty scope", tc.name)
		}
		where := strings.Join(conditions, " AND ")
		for _, want := range tc.wantWhere {
			if !strings.Contains(where, want) {
				t.Fatalf("%s: episode hydration where %s, want it to contain %s", tc.name, where, want)
			}
		}
		if len(args) != tc.wantArgs || argIdx != 3 || (tc.firstArg != nil && !slices.Equal(args[0].([]int), tc.firstArg)) {
			t.Fatalf("%s: args = %v, argIdx = %d; want %d args and next index 3", tc.name, args, argIdx, tc.wantArgs)
		}
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
