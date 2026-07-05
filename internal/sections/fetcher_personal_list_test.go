package sections

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Watchlist and favorites sections are profile-scoped: with no user store (or
// no authenticated profile) they must degrade to an empty rail instead of the
// "unsupported section type" error they returned before being wired up.
func TestFetchSectionPersonalListWithoutStoreReturnsEmpty(t *testing.T) {
	t.Parallel()

	f := &Fetcher{}
	for _, sectionType := range []SectionType{SectionWatchlist, SectionFavorites} {
		s := ResolvedSection{ID: "s1", SectionType: sectionType, ItemLimit: 20}
		items, total, err := f.fetchSection(context.Background(), s, nil, nil, 0, "", catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("fetchSection(%s) error = %v, want nil", sectionType, err)
		}
		if len(items) != 0 || total != 0 {
			t.Fatalf("fetchSection(%s) = %d items, total %d; want empty", sectionType, len(items), total)
		}
	}
}
