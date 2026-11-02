package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type memSmartCollectionStore struct {
	mu   sync.Mutex
	rows map[string]SmartCollection
}

func newMemSmartCollectionStore() *memSmartCollectionStore {
	return &memSmartCollectionStore{rows: map[string]SmartCollection{}}
}

func (m *memSmartCollectionStore) ListUserSmartCollections(_ context.Context, userID, profileID string) ([]SmartCollection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SmartCollection, 0)
	for _, c := range m.rows {
		if c.UserID == userID && c.ProfileID == profileID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memSmartCollectionStore) GetSmartCollection(_ context.Context, id string) (SmartCollection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[id]
	if !ok {
		return SmartCollection{}, ErrNotFound
	}
	return c, nil
}

func (m *memSmartCollectionStore) CreateSmartCollection(_ context.Context, c SmartCollection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[c.ID] = c
	return nil
}

func (m *memSmartCollectionStore) UpdateSmartCollection(_ context.Context, c SmartCollection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.rows[c.ID]
	if !ok {
		return ErrNotFound
	}
	existing.Name = c.Name
	existing.Description = c.Description
	existing.Color = c.Color
	existing.IsPublic = c.IsPublic
	existing.IsPinned = c.IsPinned
	existing.QueryDef = c.QueryDef
	existing.UpdatedAt = time.Now()
	m.rows[c.ID] = existing
	return nil
}

func (m *memSmartCollectionStore) DeleteSmartCollection(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, id)
	return nil
}

type smartCollectionsHarness struct {
	H    *Handler
	SC   *memSmartCollectionStore
	Prog *fakeProgressStore
	Book *memBookmarkStore
}

func newSmartCollectionsHarness(t *testing.T, knownItems ...string) *smartCollectionsHarness {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = &models.MediaItem{ContentID: id, Title: "Title-" + id}
	}
	store := newMemSmartCollectionStore()
	prog := &fakeProgressStore{}
	book := newMemBookmarkStore()
	h := New(Dependencies{
		MediaStore:           &itemListStubMediaStore{stubMediaStore: stubMediaStore{known: known}, items: itemListFromKnown(known)},
		SmartCollectionStore: store,
		ProgressStore:        prog,
		BookmarkStore:        book,
	})
	return &smartCollectionsHarness{H: h, SC: store, Prog: prog, Book: book}
}

// itemListStubMediaStore extends stubMediaStore with ListAudiobooks +
// ListAudiobookLibraries so the smart-collection items handler can
// build candidates and resolve libraries.
type itemListStubMediaStore struct {
	stubMediaStore
	items []*models.MediaItem
}

func (s *itemListStubMediaStore) ListAudiobooks(_ context.Context, _ int64, _, _ int, _ catalog.AccessFilter, _ Filter) ([]*models.MediaItem, int, error) {
	return s.items, len(s.items), nil
}

func (s *itemListStubMediaStore) ListAudiobookLibraries(_ context.Context, _ catalog.AccessFilter) ([]AudiobookLibrary, error) {
	return []AudiobookLibrary{{ID: 9, Name: "Audiobooks", Type: "audiobooks"}}, nil
}

func itemListFromKnown(known map[string]*models.MediaItem) []*models.MediaItem {
	out := make([]*models.MediaItem, 0, len(known))
	for _, it := range known {
		if it != nil {
			out = append(out, it)
		}
	}
	return out
}

func TestSmartCollection_NonOwner_404(t *testing.T) {
	runNonOwner404(t, []nonOwnerCase[*smartCollectionsHarness]{
		{name: "get private", seed: `{"name":"private"}`, method: http.MethodGet, handler: (*Handler).handleGetSmartCollection},
		{name: "patch", method: http.MethodPatch, body: `{"name":"hijack"}`, handler: (*Handler).handleUpdateSmartCollection,
			check: func(t *testing.T, hb *smartCollectionsHarness, id string) {
				if c, _ := hb.SC.GetSmartCollection(context.Background(), id); c.Name != "mine" {
					t.Errorf("non-owner leak: %q", c.Name)
				}
			}},
		{name: "delete", method: http.MethodDelete, handler: (*Handler).handleDeleteSmartCollection,
			check: func(t *testing.T, hb *smartCollectionsHarness, id string) {
				if _, err := hb.SC.GetSmartCollection(context.Background(), id); err != nil {
					t.Errorf("non-owner DELETE leaked: %v", err)
				}
			}},
		{name: "items private", seed: `{"name":"private"}`, known: "book-a", method: http.MethodGet, suffix: "/items", handler: (*Handler).handleSmartCollectionItems},
	}, newSmartCollectionsHarness, func(hb *smartCollectionsHarness) *Handler { return hb.H }, "/api/me/smart-collections",
		func(h *Handler) http.HandlerFunc { return h.handleCreateSmartCollection })
}

func TestSmartCollection_List_DoesNotLeakOtherUsers(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	_ = createForUser(t, "/api/me/smart-collections", "1", "", `{"name":"mine"}`, hb.H.handleCreateSmartCollection)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections", nil, nil, "2", "", hb.H.handleListSmartCollections)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, _ := env["items"].([]any)
	if len(list) != 0 {
		t.Errorf("user 2 sees %d, want 0", len(list))
	}
}
