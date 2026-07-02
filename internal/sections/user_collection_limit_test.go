package sections

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestLimitUserCollectionSectionItemsKeepsFilteredTotal(t *testing.T) {
	filtered := []*models.MediaItem{
		{ContentID: "visible-a"},
		{ContentID: "visible-b"},
		{ContentID: "visible-c"},
	}

	got, total := limitUserCollectionSectionItems(filtered, 2)

	if total != 3 {
		t.Fatalf("total = %d, want full filtered count", total)
	}
	if ids := sectionMediaItemIDs(got); !reflect.DeepEqual(ids, []string{"visible-a", "visible-b"}) {
		t.Fatalf("limited IDs = %#v, want first visible items", ids)
	}
}

func sectionMediaItemIDs(items []*models.MediaItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			ids = append(ids, item.ContentID)
		}
	}
	return ids
}
