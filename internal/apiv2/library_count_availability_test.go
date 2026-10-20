package apiv2

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

func TestLibraryCountAvailabilityAdapters(t *testing.T) {
	for _, status := range []string{"unavailable", "available", "future-status", ""} {
		want := status
		if want == "" {
			want = "available"
		}
		v := handlers.LibraryCollectionView{ID: "synthetic", ItemCountStatus: status}
		curated := curatedCollectionOf(v)
		admin := adminCollectionOf(v)
		cards := collectionCardsOf([]handlers.LibraryCollectionTabEntryView{{ID: v.ID, ItemCountStatus: status}})
		if curated.ItemCountStatus != want || admin.ItemCountStatus != want || cards[0].ItemCountStatus != want {
			t.Fatalf("status %q: curated=%q admin=%q card=%q", status, curated.ItemCountStatus, admin.ItemCountStatus, cards[0].ItemCountStatus)
		}
		if curated.ItemCount != 0 || admin.ItemCount != 0 || cards[0].ItemCount != 0 {
			t.Fatal("adapter changed zero")
		}
	}
}
