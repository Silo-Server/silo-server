package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// An import's sync count is what the next list read reports unless a display
// filter narrows it, so clients that ignore item_count_status don't show 0.
func TestImportViewReportsSyncCountWithoutDisplayFilter(t *testing.T) {
	h := &UserCollectionImportHandler{}
	ctx := WithNativeAPIV2(t.Context())
	v := h.collectionView(ctx, userstore.Collection{ID: "c", ItemCount: 7})
	if v.ItemCount != 7 || v.ItemCountStatus != CollectionCountAvailable {
		t.Fatalf("no display filter: count %d status %q", v.ItemCount, v.ItemCountStatus)
	}
	v = h.collectionView(ctx, userstore.Collection{ID: "c", ItemCount: 7, DisplayQueryDefinition: `{"groups":[{"rules":[{"field":"year","op":"gte","value":2000}]}]}`})
	if v.ItemCount != 0 || v.ItemCountStatus != CollectionCountUnavailable {
		t.Fatalf("display filter: count %d status %q", v.ItemCount, v.ItemCountStatus)
	}
}
