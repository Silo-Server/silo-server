package sections

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

func TestOverlaySummaryCacheScopeSeparatesAccess(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		filter catalog.AccessFilter
	}{
		{"unrestricted", catalog.AccessFilter{}},
		{"empty allow list", catalog.AccessFilter{AllowedLibraryIDs: []int{}}},
		{"one library", catalog.AccessFilter{AllowedLibraryIDs: []int{1}}},
		{"two libraries", catalog.AccessFilter{AllowedLibraryIDs: []int{1, 2}}},
		{"disabled library", catalog.AccessFilter{DisabledLibraryIDs: []int{1}}},
		{"quality ceiling", catalog.AccessFilter{MaxPlaybackQuality: "1080p"}},
		{"4k ceiling", catalog.AccessFilter{MaxPlaybackQuality: "2160p"}},
	}

	seen := make(map[string]string, len(cases))
	for _, tc := range cases {
		scope := overlaySummaryCacheScope(tc.filter)
		if other, clash := seen[scope]; clash {
			t.Errorf("%q and %q share cache scope %q; one profile would read the other's summaries", tc.name, other, scope)
		}
		seen[scope] = tc.name
	}
}

func TestOverlaySummaryCacheScopeIgnoresOrderAndContentRating(t *testing.T) {
	t.Parallel()

	a := overlaySummaryCacheScope(catalog.AccessFilter{
		AllowedLibraryIDs:  []int{3, 1, 2},
		DisabledLibraryIDs: []int{9, 7},
		MaxContentRating:   "PG",
	})
	b := overlaySummaryCacheScope(catalog.AccessFilter{
		AllowedLibraryIDs:  []int{1, 2, 3},
		DisabledLibraryIDs: []int{7, 9},
		MaxContentRating:   "R",
	})
	if a != b {
		t.Errorf("equivalent access filters produced different scopes:\n  %q\n  %q", a, b)
	}
}

// A nil allow-list means "every library"; an empty one means "none". Collapsing
// them would let an unrestricted profile's summaries serve a profile with no
// library access at all.
func TestOverlaySummaryCacheScopeNilAllowListIsNotEmpty(t *testing.T) {
	t.Parallel()

	if overlaySummaryCacheScope(catalog.AccessFilter{}) == overlaySummaryCacheScope(catalog.AccessFilter{AllowedLibraryIDs: []int{}}) {
		t.Error("nil and empty AllowedLibraryIDs share a cache scope")
	}
}
