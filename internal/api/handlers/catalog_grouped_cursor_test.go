package handlers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type groupedCursorSummaries struct{}

func (groupedCursorSummaries) GetSummaryForContentID(_ context.Context, id string, _ catalog.AccessFilter) (*catalog.WorkSummary, error) {
	work := id
	if strings.HasPrefix(id, "edition-") {
		work = "shared-work"
	}
	return &catalog.WorkSummary{WorkID: work}, nil
}

func TestGroupedCatalogAdvancesRawKeysetsAndRetainsCollectionScope(t *testing.T) {
	h := &CatalogHandler{workSummary: groupedCursorSummaries{}}
	items := []*models.MediaItem{}
	for _, id := range []string{"edition-a", "edition-b", "edition-c", "edition-d", "second-work", "third-work"} {
		items = append(items, &models.MediaItem{ContentID: id, Type: "ebook"})
	}
	seed := &catalog.QueryCursor{Collection: &catalog.CollectionCursor{ID: "collection", Revision: 42}}
	calls := 0
	resolve := func(_ context.Context, req catalog.CatalogRequest, _ catalog.AccessFilter) (*catalog.CatalogResult, error) {
		calls++
		if calls > 8 {
			return nil, fmt.Errorf("raw keyset did not advance")
		}
		if req.Seek != nil {
			t.Fatal("group seek forwarded to raw source")
		}
		start := 0
		if req.After != nil {
			if req.After.Collection == nil || req.After.Collection.Revision != 42 {
				t.Fatal("lost collection fence")
			}
			start = req.After.Consumed
		}
		// Deliberately ignore Offset, as a real tuple executor does.
		end := min(start+2, len(items))
		result := &catalog.CatalogResult{Items: items[start:end], HasMore: end < len(items), CursorScope: seed}
		if result.HasMore {
			result.Next = &catalog.QueryCursor{Consumed: end, Collection: seed.Collection}
		}
		return result, nil
	}
	req := catalog.CatalogRequest{CursorPaging: true, Limit: 2, CollectionID: "collection", After: seed}
	first, _, err := h.resolveGroupedCatalogByWorkUsing(t.Context(), req, catalog.AccessFilter{}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(first.Items) != 2 || first.Items[0].ContentID != "edition-a" || first.Items[1].ContentID != "second-work" || !first.HasMore {
		t.Fatalf("first grouped page: %+v (%d calls)", first, calls)
	}
	if first.Next != seed || first.CursorScope != seed {
		t.Fatal("group continuation must preserve scope-only seed")
	}
	req.After = first.Next
	req.Seek = new(2)
	second, _, err := h.resolveGroupedCatalogByWorkUsing(t.Context(), req, catalog.AccessFilter{}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 6 || len(second.Items) != 1 || second.Items[0].ContentID != "third-work" || second.HasMore || second.TotalExact {
		t.Fatalf("second grouped page: %+v (%d calls)", second, calls)
	}
	if second.CursorScope != seed {
		t.Fatal("final page lost source fence")
	}
}
