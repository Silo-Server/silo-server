package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeCollectionSource is an in-memory collectionSource.
type fakeCollectionSource struct {
	collections []*models.LibraryCollection
	items       map[string][]*models.LibraryCollectionItem
	listAllLib  *int
	// viewerCounts answers ViewerCollectionCounts; nil means the viewer's
	// access narrows nothing.
	viewerCounts map[string]catalog.ViewerCollectionCount
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

func (f *fakeCollectionSource) ViewerCollectionCounts(context.Context, []*models.LibraryCollection, catalog.AccessFilter) (map[string]catalog.ViewerCollectionCount, error) {
	return f.viewerCounts, nil
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
