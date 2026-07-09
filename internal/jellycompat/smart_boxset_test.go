package jellycompat

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeSmartExecutor is a smartCollectionQueryExecutor double returning a fixed
// member list. Preview mimics catalog.QueryExecutor.Preview's two-call contract
// (limit=1 probe, then limit=total) for the explicit-sort allowlist path;
// PreviewPage mimics OFFSET/LIMIT SQL paging for the default browse path.
type fakeSmartExecutor struct {
	items            []*models.MediaItem
	previewCalls     int
	previewPageCalls int
	gotPageOffset    int
	gotPageLimit     int
	gotDef           catalog.QueryDefinition
}

func (f *fakeSmartExecutor) Preview(_ context.Context, def catalog.QueryDefinition, _ catalog.AccessFilter, limit int) ([]*models.MediaItem, int, error) {
	f.previewCalls++
	f.gotDef = def
	total := len(f.items)
	if limit < total {
		return f.items[:limit], total, nil
	}
	return f.items, total, nil
}

func (f *fakeSmartExecutor) PreviewPage(_ context.Context, def catalog.QueryDefinition, _ catalog.AccessFilter, limit, offset int, _ bool) ([]*models.MediaItem, int, bool, error) {
	f.previewPageCalls++
	f.gotDef = def
	f.gotPageOffset = offset
	f.gotPageLimit = limit
	total := len(f.items)
	if offset >= total {
		return nil, total, true, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return f.items[offset:end], total, true, nil
}

// TestHandleItems_SmartBoxSetChildrenResolveViaQuery pins the Wholphin fix: a
// smart (live-query) collection's BoxSet children resolve through the query
// executor in the collection's own order, instead of returning empty because
// nothing is materialized in library_collection_items.
func TestHandleItems_SmartBoxSetChildrenResolveViaQuery(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "201", LibraryID: 1, Title: "Directed by X", Visibility: "visible", CollectionType: "smart", ItemCount: 2, QueryDefinition: json.RawMessage(`{}`)},
		},
		// No materialized items: smart collections store none.
	}
	exec := &fakeSmartExecutor{items: []*models.MediaItem{
		{ContentID: "m-1", Type: "movie", Title: "Kill Bill"},
		{ContentID: "m-2", Type: "movie", Title: "Pulp Fiction"},
	}}
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{
		"m-1": {ContentID: "m-1", Type: "movie", Title: "Kill Bill"},
		"m-2": {ContentID: "m-2", Type: "movie", Title: "Pulp Fiction"},
	}}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, itemRepo)
	h.queryExecutor = exec

	parentID := h.codec.EncodeStringID(EncodedIDCollection, "201")
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID)
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 smart-collection children, got %d: %+v", len(result.Items), result.Items)
	}
	if result.Items[0].Name != "Kill Bill" || result.Items[1].Name != "Pulp Fiction" {
		t.Fatalf("expected smart query order, got %q,%q", result.Items[0].Name, result.Items[1].Name)
	}
	if result.Items[0].ParentID != parentID {
		t.Fatalf("expected ParentId %s, got %s", parentID, result.Items[0].ParentID)
	}
	if exec.previewPageCalls == 0 {
		t.Fatal("expected the query executor to be paged for a smart collection")
	}
}

// TestHandleItems_SmartBoxSetChildrenWithoutExecutorEmpty ensures a smart
// collection degrades to an empty page (never a 500) when no executor is wired.
func TestHandleItems_SmartBoxSetChildrenWithoutExecutorEmpty(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "201", LibraryID: 1, Title: "Directed by X", Visibility: "visible", CollectionType: "smart", ItemCount: 5, QueryDefinition: json.RawMessage(`{}`)},
		},
	}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)
	// queryExecutor intentionally left nil.

	parentID := h.codec.EncodeStringID(EncodedIDCollection, "201")
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID)
	if result.TotalRecordCount != 0 || len(result.Items) != 0 {
		t.Fatalf("expected graceful empty result without executor, got %+v", result.Items)
	}
}

// TestHandleItems_SmartBoxSetChildrenIntersectLibraryScope asserts the query's
// own library_ids are intersected with the collection's bound libraries before
// the executor runs, so a restricted collection cannot widen its scope.
func TestHandleItems_SmartBoxSetChildrenIntersectLibraryScope(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "202", LibraryIDs: []int{1, 2}, Title: "Scoped", Visibility: "visible", CollectionType: "smart", ItemCount: 1, QueryDefinition: json.RawMessage(`{"library_ids":[2,3]}`)},
		},
	}
	exec := &fakeSmartExecutor{items: []*models.MediaItem{{ContentID: "m-1", Type: "movie", Title: "Only Two"}}}
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{"m-1": {ContentID: "m-1", Type: "movie", Title: "Only Two"}}}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{
		{ID: 1, Name: "Movies", Type: "movies"},
		{ID: 2, Name: "Foreign", Type: "movies"},
	}, itemRepo)
	h.queryExecutor = exec

	parentID := h.codec.EncodeStringID(EncodedIDCollection, "202")
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID)
	if len(result.Items) != 1 || result.Items[0].Name != "Only Two" {
		t.Fatalf("expected the single resolved child, got %+v", result.Items)
	}
	// query library_ids [2,3] ∩ collection [1,2] = [2].
	if len(exec.gotDef.LibraryIDs) != 1 || exec.gotDef.LibraryIDs[0] != 2 {
		t.Fatalf("expected executor to receive intersected library scope [2], got %v", exec.gotDef.LibraryIDs)
	}
}

// TestHandleItems_SmartBoxSetMalformedQueryEmpty asserts a malformed query
// definition degrades to an empty page and never consults the executor (so it
// can never 500 a browse).
func TestHandleItems_SmartBoxSetMalformedQueryEmpty(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "203", LibraryID: 1, Title: "Broken", Visibility: "visible", CollectionType: "smart", ItemCount: 9, QueryDefinition: json.RawMessage(`{bad`)},
		},
	}
	exec := &fakeSmartExecutor{items: []*models.MediaItem{{ContentID: "m-1", Type: "movie", Title: "Nope"}}}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)
	h.queryExecutor = exec

	parentID := h.codec.EncodeStringID(EncodedIDCollection, "203")
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID)
	if result.TotalRecordCount != 0 || len(result.Items) != 0 {
		t.Fatalf("expected empty result for malformed query definition, got %+v", result.Items)
	}
	if exec.previewCalls != 0 || exec.previewPageCalls != 0 {
		t.Fatalf("executor must not run for a malformed query definition; got %d Preview + %d PreviewPage calls",
			exec.previewCalls, exec.previewPageCalls)
	}
}

// TestHandleItems_SmartBoxSetChildrenPageInSQL asserts the default (no explicit
// sort) browse path pages a smart collection directly in SQL — passing the
// request's StartIndex/Limit through to the executor as OFFSET/LIMIT and
// reporting the full membership as TotalRecordCount — instead of materializing
// the entire (uncapped) membership and slicing one page locally.
func TestHandleItems_SmartBoxSetChildrenPageInSQL(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "210", LibraryID: 1, Title: "Big Smart", Visibility: "visible", CollectionType: "smart", ItemCount: 5, QueryDefinition: json.RawMessage(`{}`)},
		},
	}
	members := []*models.MediaItem{
		{ContentID: "m-1", Type: "movie", Title: "One"},
		{ContentID: "m-2", Type: "movie", Title: "Two"},
		{ContentID: "m-3", Type: "movie", Title: "Three"},
		{ContentID: "m-4", Type: "movie", Title: "Four"},
		{ContentID: "m-5", Type: "movie", Title: "Five"},
	}
	exec := &fakeSmartExecutor{items: members}
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{
		"m-3": {ContentID: "m-3", Type: "movie", Title: "Three"},
		"m-4": {ContentID: "m-4", Type: "movie", Title: "Four"},
	}}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, itemRepo)
	h.queryExecutor = exec

	parentID := h.codec.EncodeStringID(EncodedIDCollection, "210")
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&StartIndex=2&Limit=2")

	if exec.previewPageCalls != 1 {
		t.Fatalf("expected exactly one paged executor call, got %d", exec.previewPageCalls)
	}
	if exec.previewCalls != 0 {
		t.Fatalf("default browse must not use the full-membership Preview path; got %d calls", exec.previewCalls)
	}
	if exec.gotPageOffset != 2 || exec.gotPageLimit != 2 {
		t.Fatalf("expected the request paging to reach the executor as offset=2 limit=2, got offset=%d limit=%d",
			exec.gotPageOffset, exec.gotPageLimit)
	}
	if result.TotalRecordCount != 5 {
		t.Fatalf("expected TotalRecordCount to reflect full membership (5), got %d", result.TotalRecordCount)
	}
	if len(result.Items) != 2 || result.Items[0].Name != "Three" || result.Items[1].Name != "Four" {
		t.Fatalf("expected page [Three, Four], got %+v", result.Items)
	}
	if result.StartIndex != 2 {
		t.Fatalf("expected StartIndex 2, got %d", result.StartIndex)
	}
}
