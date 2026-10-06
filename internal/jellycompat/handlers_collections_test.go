package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeCollectionSource is an in-memory collectionSource.
type fakeCollectionSource struct {
	collections []*models.LibraryCollection
	items       map[string][]*models.LibraryCollectionItem
	listAllLib  *int
}

func (f *fakeCollectionSource) ListAll(_ context.Context, libraryID *int, _ catalog.ListLibraryCollectionsOptions) ([]*models.LibraryCollection, error) {
	f.listAllLib = libraryID
	out := make([]*models.LibraryCollection, 0, len(f.collections))
	for _, c := range f.collections {
		if libraryID != nil && !collectionInLibrary(c, *libraryID) {
			continue
		}
		if c.Visibility != "visible" {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func collectionInLibrary(c *models.LibraryCollection, libraryID int) bool {
	if len(c.LibraryIDs) == 0 {
		return c.LibraryID == libraryID
	}
	for _, id := range c.LibraryIDs {
		if id == libraryID {
			return true
		}
	}
	return false
}

func (f *fakeCollectionSource) GetByID(_ context.Context, id string) (*models.LibraryCollection, error) {
	for _, c := range f.collections {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, nil
}

func (f *fakeCollectionSource) ListItems(_ context.Context, collectionID string) ([]*models.LibraryCollectionItem, error) {
	return f.items[collectionID], nil
}

func (f *fakeCollectionSource) ListContainingItem(_ context.Context, mediaItemID string) ([]*models.LibraryCollection, error) {
	out := []*models.LibraryCollection{}
	for _, c := range f.collections {
		if c.Visibility != "visible" {
			continue
		}
		for _, item := range f.items[c.ID] {
			if item.MediaItemID == mediaItemID {
				out = append(out, c)
				break
			}
		}
	}
	return out, nil
}

func (f *fakeCollectionSource) AnyVisibleInLibraries(_ context.Context, libraryIDs []int) (bool, error) {
	set := make(map[int]struct{}, len(libraryIDs))
	for _, id := range libraryIDs {
		set[id] = struct{}{}
	}
	for _, c := range f.collections {
		if c.Visibility != "visible" {
			continue
		}
		if len(c.LibraryIDs) == 0 {
			if _, ok := set[c.LibraryID]; ok {
				return true, nil
			}
			continue
		}
		for _, id := range c.LibraryIDs {
			if _, ok := set[id]; ok {
				return true, nil
			}
		}
	}
	return false, nil
}

// librariesContentService serves a fixed library list; other methods panic.
type librariesContentService struct {
	countingContentService
	libraries []upstreamUserLibrary
}

func (s *librariesContentService) ListUserLibraries(context.Context, *Session) ([]upstreamUserLibrary, error) {
	out := make([]upstreamUserLibrary, len(s.libraries))
	copy(out, s.libraries)
	return out, nil
}

// fakeBatchItemRepo backs the compatPool()==nil hydration fallback.
type fakeBatchItemRepo struct {
	items map[string]*models.MediaItem
}

func (f *fakeBatchItemRepo) GetByIDs(_ context.Context, contentIDs []string) ([]*models.MediaItem, error) {
	return f.byIDs(contentIDs), nil
}

func (f *fakeBatchItemRepo) GetByIDsWithAccess(_ context.Context, contentIDs []string, _ catalog.AccessFilter) ([]*models.MediaItem, error) {
	return f.byIDs(contentIDs), nil
}

func (f *fakeBatchItemRepo) GetItemsInLibrary(_ context.Context, contentIDs []string, _ int) (map[string]bool, error) {
	out := make(map[string]bool, len(contentIDs))
	for _, id := range contentIDs {
		out[id] = true
	}
	return out, nil
}

func (f *fakeBatchItemRepo) byIDs(contentIDs []string) []*models.MediaItem {
	out := make([]*models.MediaItem, 0, len(contentIDs))
	for _, id := range contentIDs {
		if item, ok := f.items[id]; ok {
			out = append(out, item)
		}
	}
	return out
}

func newCollectionsTestHandler(collections *fakeCollectionSource, libraries []upstreamUserLibrary, itemRepo itemRepoForBatchLoader) *ItemsHandler {
	codec := NewResourceIDCodec()
	return &ItemsHandler{
		content:     &librariesContentService{libraries: libraries},
		userData:    &mockUserDataService{},
		codec:       codec,
		mapper:      newMapper(codec, &config.Config{}),
		images:      NewImageCache(time.Hour, time.Now),
		collections: collections,
		itemRepo:    itemRepo,
	}
}

func collectionsTestSession() *Session {
	return &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
}

func performItemsRequest(t *testing.T, h *ItemsHandler, target string) queryResultDTO {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
	rec := httptest.NewRecorder()
	h.HandleItems(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result queryResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return result
}

func TestHandleItems_BoxSetListingFiltersHiddenLibraries(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible", ItemCount: 3},
			{ID: "102", LibraryID: 2, Title: "Audiobook Picks", Visibility: "visible", ItemCount: 5},
			{ID: "103", LibraryID: 1, Title: "Hidden", Visibility: "hidden", ItemCount: 2},
		},
	}
	// Library 2 is not visible (e.g. audiobook library, excluded upstream).
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	result := performItemsRequest(t, h, "/Items?IncludeItemTypes=BoxSet&Recursive=true")
	if result.TotalRecordCount != 1 || len(result.Items) != 1 {
		t.Fatalf("expected exactly the Marvel collection, got %+v", result.Items)
	}
	item := result.Items[0]
	if item.Type != "BoxSet" || item.Name != "Marvel" || !item.IsFolder || item.ChildCount != 3 {
		t.Fatalf("unexpected BoxSet DTO: %+v", item)
	}
}

func TestHandleItems_BoxSetListingScopedToParentLibrary(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible"},
			{ID: "104", LibraryID: 3, Title: "Shows Sets", Visibility: "visible"},
		},
	}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{
		{ID: 1, Name: "Movies", Type: "movies"},
		{ID: 3, Name: "Shows", Type: "series"},
	}, nil)

	parentID := h.codec.EncodeIntID(EncodedIDLibrary, 3)
	result := performItemsRequest(t, h, "/Items?IncludeItemTypes=BoxSet&ParentId="+parentID)
	if len(result.Items) != 1 || result.Items[0].Name != "Shows Sets" {
		t.Fatalf("expected library-scoped collection list, got %+v", result.Items)
	}
	if collections.listAllLib == nil || *collections.listAllLib != 3 {
		t.Fatalf("expected ListAll scoped to library 3, got %v", collections.listAllLib)
	}
}

func TestHandleItems_BoxSetChildrenPreservePositionOrder(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible"},
		},
		items: map[string][]*models.LibraryCollectionItem{
			"101": {
				{CollectionID: "101", MediaItemID: "m-2", Position: 0},
				{CollectionID: "101", MediaItemID: "m-1", Position: 1},
				{CollectionID: "101", MediaItemID: "m-3", Position: 2},
			},
		},
	}
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{
		"m-1": {ContentID: "m-1", Type: "movie", Title: "Iron Man"},
		"m-2": {ContentID: "m-2", Type: "movie", Title: "Captain America"},
		"m-3": {ContentID: "m-3", Type: "movie", Title: "Thor"},
	}}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, itemRepo)

	parentID := h.codec.EncodeStringID(EncodedIDCollection, "101")
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID)
	if len(result.Items) != 3 {
		t.Fatalf("expected 3 children, got %d", len(result.Items))
	}
	gotNames := []string{result.Items[0].Name, result.Items[1].Name, result.Items[2].Name}
	wantNames := []string{"Captain America", "Iron Man", "Thor"}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Fatalf("expected curated order %v, got %v", wantNames, gotNames)
		}
	}
	if result.Items[0].ParentID != parentID {
		t.Fatalf("expected ParentId %s, got %s", parentID, result.Items[0].ParentID)
	}
}

func TestHandleItems_BoxSetChildrenPagination(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible"},
		},
		items: map[string][]*models.LibraryCollectionItem{
			"101": {
				{CollectionID: "101", MediaItemID: "m-1", Position: 0},
				{CollectionID: "101", MediaItemID: "m-2", Position: 1},
				{CollectionID: "101", MediaItemID: "m-3", Position: 2},
			},
		},
	}
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{
		"m-1": {ContentID: "m-1", Type: "movie", Title: "One"},
		"m-2": {ContentID: "m-2", Type: "movie", Title: "Two"},
		"m-3": {ContentID: "m-3", Type: "movie", Title: "Three"},
	}}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, itemRepo)

	parentID := h.codec.EncodeStringID(EncodedIDCollection, "101")
	result := performItemsRequest(t, h, "/Items?ParentId="+parentID+"&StartIndex=1&Limit=1")
	if result.TotalRecordCount != 3 {
		t.Fatalf("expected total 3, got %d", result.TotalRecordCount)
	}
	if len(result.Items) != 1 || result.Items[0].Name != "Two" {
		t.Fatalf("expected page [Two], got %+v", result.Items)
	}
}

func TestHandleItem_BoxSetDetailAndHiddenCollection(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible", ItemCount: 4, Description: "Heroes"},
			{ID: "103", LibraryID: 1, Title: "Hidden", Visibility: "hidden"},
		},
	}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	router := chi.NewRouter()
	router.Get("/Items/{id}", h.HandleItem)

	routeID := h.codec.EncodeStringID(EncodedIDCollection, "101")
	req := httptest.NewRequest("GET", "/Items/"+routeID, nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200 for visible collection, got %d", rec.Code)
	}
	var dto baseItemDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dto.Type != "BoxSet" || dto.Name != "Marvel" || dto.Overview != "Heroes" || dto.ChildCount != 4 {
		t.Fatalf("unexpected detail DTO: %+v", dto)
	}

	hiddenID := h.codec.EncodeStringID(EncodedIDCollection, "103")
	req = httptest.NewRequest("GET", "/Items/"+hiddenID, nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("expected 404 for hidden collection, got %d", rec.Code)
	}
}

func TestHandleItems_UnmappedTypeFilterReturnsEmpty(t *testing.T) {
	h := newCollectionsTestHandler(&fakeCollectionSource{}, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	result := performItemsRequest(t, h, "/Items?IncludeItemTypes=Playlist")
	if result.TotalRecordCount != 0 || len(result.Items) != 0 {
		t.Fatalf("expected empty result for unmapped type filter, got %+v", result)
	}
}

func TestHandleItems_BoxSetWithUserStateFilterReturnsEmpty(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible"},
		},
	}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	// Collections carry no favorite/played state; user-state-filtered BoxSet
	// queries must stay empty (jellyfin-web Favorites tab relies on this).
	for _, target := range []string{
		"/Items?IncludeItemTypes=BoxSet&Filters=IsFavorite",
		"/Items?IncludeItemTypes=BoxSet&Filters=IsResumable",
		"/Items?IncludeItemTypes=BoxSet&IsPlayed=true",
	} {
		result := performItemsRequest(t, h, target)
		if result.TotalRecordCount != 0 || len(result.Items) != 0 {
			t.Fatalf("%s: expected empty result, got %+v", target, result.Items)
		}
	}
}

func TestHandleItems_CollectionFolderTypeReturnsViews(t *testing.T) {
	h := newCollectionsTestHandler(&fakeCollectionSource{}, []upstreamUserLibrary{
		{ID: 1, Name: "Movies", Type: "movies"},
		{ID: 3, Name: "Shows", Type: "series"},
	}, nil)

	result := performItemsRequest(t, h, "/Items?IncludeItemTypes=CollectionFolder")
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 library views, got %+v", result.Items)
	}
	for _, item := range result.Items {
		if item.Type != "CollectionFolder" {
			t.Fatalf("expected CollectionFolder items, got %+v", item)
		}
	}
}

func TestHandleItems_SpecificIdsIncludeBoxSets(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible", ItemCount: 2},
		},
	}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	boxSetID := h.codec.EncodeStringID(EncodedIDCollection, "101")
	result := performItemsRequest(t, h, "/Items?Ids="+url.QueryEscape(boxSetID))
	if len(result.Items) != 1 || result.Items[0].Type != "BoxSet" || result.Items[0].Name != "Marvel" {
		t.Fatalf("expected the BoxSet DTO for Ids= re-hydration, got %+v", result.Items)
	}
}

func TestParseItemsQuery_BoxSetFlags(t *testing.T) {
	codec := NewResourceIDCodec()

	req := httptest.NewRequest("GET", "/Items?IncludeItemTypes=BoxSet", nil)
	query := parseItemsQuery(req, codec)
	if !query.wantsBoxSets || len(query.itemTypes) != 0 || !query.hasItemTypeFilter {
		t.Fatalf("expected BoxSet flags, got %+v", query)
	}

	collectionID := codec.EncodeStringID(EncodedIDCollection, "555")
	req = httptest.NewRequest("GET", "/Items?ParentId="+url.QueryEscape(collectionID), nil)
	query = parseItemsQuery(req, codec)
	if query.parentCollectionID != "555" {
		t.Fatalf("expected parentCollectionID 555, got %q", query.parentCollectionID)
	}
	if query.sortExplicit {
		t.Fatalf("expected sortExplicit=false without SortBy")
	}

	req = httptest.NewRequest("GET", "/Items?SortBy=SortName", nil)
	query = parseItemsQuery(req, codec)
	if !query.sortExplicit {
		t.Fatalf("expected sortExplicit=true with SortBy")
	}
}

func TestUserViews_PrependsCollectionsViewWhenVisible(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible", ItemCount: 3},
		},
	}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{
		{ID: 1, Name: "Movies", Type: "movies"},
	}, nil)

	result := performItemsRequest(t, h, "/Items")
	if len(result.Items) != 2 {
		t.Fatalf("expected Collections view + 1 library, got %+v", result.Items)
	}
	view := result.Items[0]
	if view.ID != collectionsViewID {
		t.Fatalf("expected Collections view first with ID %s, got %+v", collectionsViewID, view)
	}
	if view.Type != "CollectionFolder" || view.CollectionType != "boxsets" || !view.IsFolder || view.Name != "Collections" {
		t.Fatalf("unexpected Collections view DTO: %+v", view)
	}
	if view.ChildCount != 0 {
		t.Fatalf("expected ChildCount omitted (0), got %d", view.ChildCount)
	}
	if result.Items[1].Name != "Movies" {
		t.Fatalf("expected real library after the Collections view, got %+v", result.Items[1])
	}
	// Jellyfin for Android TV crashes reopening a view without DisplayPreferencesId.
	for _, item := range result.Items {
		if item.DisplayPreferencesID == "" || strings.Contains(item.DisplayPreferencesID, "-") {
			t.Fatalf("view %q DisplayPreferencesId = %q, want hyphenless view id", item.Name, item.DisplayPreferencesID)
		}
	}
}

func TestUserViews_OmitsCollectionsViewWhenNoVisibleCollections(t *testing.T) {
	// Only collection lives in a library the session cannot see (library 2).
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "102", LibraryID: 2, Title: "Audiobook Picks", Visibility: "visible"},
			{ID: "103", LibraryID: 1, Title: "Hidden", Visibility: "hidden"},
		},
	}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{
		{ID: 1, Name: "Movies", Type: "movies"},
	}, nil)

	result := performItemsRequest(t, h, "/Items")
	if len(result.Items) != 1 || result.Items[0].Name != "Movies" {
		t.Fatalf("expected only the Movies library, got %+v", result.Items)
	}
}

func TestUserViews_OmitsCollectionsViewWhenSourceNil(t *testing.T) {
	h := newCollectionsTestHandler(&fakeCollectionSource{}, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)
	h.collections = nil // exercise the no-collection-source path (e.g. a DB-less deployment)

	result := performItemsRequest(t, h, "/Items")
	for _, item := range result.Items {
		if item.ID == collectionsViewID {
			t.Fatalf("did not expect Collections view without a collection source: %+v", result.Items)
		}
	}
}

func TestHandleItems_CollectionsViewParentHonorsTypeFilter(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible", ItemCount: 3},
		},
	}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	// A non-BoxSet type filter has no direct children under the view.
	empty := performItemsRequest(t, h, "/Items?ParentId="+collectionsViewID+"&IncludeItemTypes=Movie")
	if empty.TotalRecordCount != 0 || len(empty.Items) != 0 {
		t.Fatalf("expected empty result for IncludeItemTypes=Movie, got %+v", empty.Items)
	}

	// An explicit BoxSet filter still lists the collections.
	boxsets := performItemsRequest(t, h, "/Items?ParentId="+collectionsViewID+"&IncludeItemTypes=BoxSet")
	if len(boxsets.Items) != 1 || boxsets.Items[0].Type != "BoxSet" {
		t.Fatalf("expected the Marvel BoxSet, got %+v", boxsets.Items)
	}
}

func TestHandleItems_CollectionsViewRehydratedByIds(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible"},
		},
	}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, nil)

	result := performItemsRequest(t, h, "/Items?Ids="+collectionsViewID)
	if len(result.Items) != 1 {
		t.Fatalf("expected the Collections view, got %+v", result.Items)
	}
	if result.Items[0].ID != collectionsViewID || result.Items[0].Type != "CollectionFolder" {
		t.Fatalf("unexpected re-hydrated view: %+v", result.Items[0])
	}
}

func TestHandleItems_CollectionsViewParentListsBoxSets(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "101", LibraryID: 1, Title: "Marvel", Visibility: "visible", ItemCount: 3},
			{ID: "104", LibraryID: 3, Title: "Shows Sets", Visibility: "visible", ItemCount: 2},
			{ID: "102", LibraryID: 2, Title: "Hidden Library Set", Visibility: "visible"},
		},
	}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{
		{ID: 1, Name: "Movies", Type: "movies"},
		{ID: 3, Name: "Shows", Type: "series"},
	}, nil)

	// Compact and dashed forms both resolve to the Collections view.
	for _, parentID := range []string{collectionsViewID, "9d7ad6af-e9af-a2da-b1a2-f6e00ad28fa6"} {
		result := performItemsRequest(t, h, "/Items?ParentId="+parentID)
		if result.TotalRecordCount != 2 || len(result.Items) != 2 {
			t.Fatalf("ParentId %s: expected the 2 visible collections, got %+v", parentID, result.Items)
		}
		for _, item := range result.Items {
			if item.Type != "BoxSet" || !item.IsFolder {
				t.Fatalf("ParentId %s: expected BoxSet children, got %+v", parentID, item)
			}
		}
		if collections.listAllLib != nil {
			t.Fatalf("ParentId %s: expected unscoped ListAll, got library %v", parentID, *collections.listAllLib)
		}
	}
}

func TestHandleItem_CollectionsViewReturnsCollectionFolder(t *testing.T) {
	h := newCollectionsTestHandler(&fakeCollectionSource{}, nil, nil)

	req := httptest.NewRequest("GET", "/Items/"+collectionsViewID, nil)
	ctx := context.WithValue(req.Context(), compatSessionKey, collectionsTestSession())
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", collectionsViewID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	h.HandleItem(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var view baseItemDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if view.ID != collectionsViewID || view.Type != "CollectionFolder" || view.CollectionType != "boxsets" {
		t.Fatalf("unexpected Collections view: %+v", view)
	}
}

func performItemCollectionsRequest(t *testing.T, h *ItemsHandler, target string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.Get("/Items/{id}/Collections", h.HandleItemCollections)
	req := httptest.NewRequest("GET", target, nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, collectionsTestSession()))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestHandleItemCollections_ListsVisibleContainingCollectionsByName(t *testing.T) {
	member := func(collectionID string) []*models.LibraryCollectionItem {
		return []*models.LibraryCollectionItem{{CollectionID: collectionID, MediaItemID: "m-1"}}
	}
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{
			{ID: "201", LibraryID: 1, Title: "zombie classics", Visibility: "visible", ItemCount: 4},
			{ID: "202", LibraryID: 1, Title: "Action Picks", Visibility: "visible", ItemCount: 2},
			{ID: "203", LibraryID: 2, Title: "Hidden Library Set", Visibility: "visible"},
			{ID: "204", LibraryID: 1, Title: "Hidden Set", Visibility: "hidden"},
			{ID: "205", LibraryID: 1, Title: "Unrelated", Visibility: "visible"},
		},
		items: map[string][]*models.LibraryCollectionItem{
			"201": member("201"),
			"202": member("202"),
			"203": member("203"),
			"204": member("204"),
			"205": {{CollectionID: "205", MediaItemID: "m-2"}},
		},
	}
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{"m-1": {ContentID: "m-1", Type: "movie", Title: "Night"}}}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, itemRepo)

	itemID := h.codec.EncodeStringID(EncodedIDItem, "m-1")
	rec := performItemCollectionsRequest(t, h, "/Items/"+itemID+"/Collections?Fields=PrimaryImageAspectRatio")
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result queryResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.TotalRecordCount != 2 || len(result.Items) != 2 {
		t.Fatalf("expected the two visible containing collections, got %+v", result)
	}
	if result.Items[0].Name != "Action Picks" || result.Items[1].Name != "zombie classics" {
		t.Fatalf("expected case-insensitive name order, got %q, %q", result.Items[0].Name, result.Items[1].Name)
	}
	if result.Items[0].Type != "BoxSet" || result.Items[0].PrimaryImageAspectRatio == nil {
		t.Fatalf("expected BoxSet DTO with aspect ratio, got %+v", result.Items[0])
	}

	rec = performItemCollectionsRequest(t, h, "/Items/"+itemID+"/Collections?StartIndex=1&Limit=1")
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal page: %v", err)
	}
	if result.TotalRecordCount != 2 || result.StartIndex != 1 || len(result.Items) != 1 || result.Items[0].Name != "zombie classics" {
		t.Fatalf("expected second page [zombie classics] of 2, got %+v", result)
	}

	// Standard item response controls apply as on every other item list.
	if result.Items[0].UserData == nil {
		t.Fatal("fixture should carry user data before it is disabled")
	}
	rec = performItemCollectionsRequest(t, h, "/Items/"+itemID+"/Collections?EnableUserData=false&EnableImages=false")
	result = queryResultDTO{}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal controls: %v", err)
	}
	for _, item := range result.Items {
		if item.UserData != nil || len(item.ImageTags) != 0 {
			t.Fatalf("response controls ignored for %q: user data %v, image tags %v", item.Name, item.UserData, item.ImageTags)
		}
	}
}

func TestHandleItemCollections_EmptyAndNotFound(t *testing.T) {
	collections := &fakeCollectionSource{
		collections: []*models.LibraryCollection{{ID: "201", LibraryID: 1, Title: "Set", Visibility: "visible"}},
		items:       map[string][]*models.LibraryCollectionItem{"201": {{CollectionID: "201", MediaItemID: "m-hidden"}}},
	}
	// m-hidden is a member but the viewer's access filter does not return it;
	// m-ghost is equally invisible and belongs to no collection.
	itemRepo := &fakeBatchItemRepo{items: map[string]*models.MediaItem{
		"m-none": {ContentID: "m-none", Type: "movie", Title: "Loner"},
		"s-show": {ContentID: "s-show", Type: "series", Title: "Show"},
	}}
	h := newCollectionsTestHandler(collections, []upstreamUserLibrary{{ID: 1, Name: "Movies", Type: "movies"}}, itemRepo)
	h.episodeRepo = &countingEpisodeRepo{episodesByID: map[string]*models.Episode{"e-1": {ContentID: "e-1", SeriesID: "s-show", SeasonNumber: 1, EpisodeNumber: 1}}}

	cases := []struct {
		name   string
		target string
		code   int
	}{
		{"visible item in no collection", "/Items/" + h.codec.EncodeStringID(EncodedIDItem, "m-none") + "/Collections", 200},
		{"visible episode", "/Items/" + h.codec.EncodeStringID(EncodedIDItem, "e-1") + "/Collections", 200},
		{"season", "/Items/" + h.codec.EncodeStringID(EncodedIDSeason, "s-1") + "/Collections", 200},
		{"inaccessible member", "/Items/" + h.codec.EncodeStringID(EncodedIDItem, "m-hidden") + "/Collections", 404},
		{"inaccessible non-member", "/Items/" + h.codec.EncodeStringID(EncodedIDItem, "m-ghost") + "/Collections", 404},
		{"undecodable id", "/Items/not-an-id/Collections", 404},
		{"other user", "/Items/" + h.codec.EncodeStringID(EncodedIDItem, "m-none") + "/Collections?userId=someone-else", 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := performItemCollectionsRequest(t, h, tc.target)
			if rec.Code != tc.code {
				t.Fatalf("expected %d, got %d: %s", tc.code, rec.Code, rec.Body.String())
			}
			if tc.code != 200 {
				return
			}
			var result queryResultDTO
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if result.TotalRecordCount != 0 || len(result.Items) != 0 {
				t.Fatalf("expected empty result, got %+v", result)
			}
		})
	}
}
