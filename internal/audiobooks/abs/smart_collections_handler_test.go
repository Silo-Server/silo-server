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

func createSmartCollectionForUser(t *testing.T, hb *smartCollectionsHarness, userID, profileID, body string) string {
	t.Helper()
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/smart-collections", nil, []byte(body), userID, profileID, hb.H.handleCreateSmartCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed POST status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("seed POST returned no id; body=%s", rec.Body.String())
	}
	return id
}

func TestSmartCollection_Create_ReturnsFullShape(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	body := []byte(`{"name":"x","description":"d","color":"#fff","isPublic":true,"isPinned":true,"query_def":{"match":"all","groups":[{"match":"all","rules":[{"field":"title","op":"is","value":"test"}]}]}}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/smart-collections", nil, body, "1", "", hb.H.handleCreateSmartCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "x" {
		t.Errorf("name = %v, want x", got["name"])
	}
	if got["isPublic"] != true {
		t.Errorf("isPublic = %v, want true", got["isPublic"])
	}
	if got["isPinned"] != true {
		t.Errorf("isPinned = %v, want true", got["isPinned"])
	}
	qd, ok := got["queryDef"].(map[string]any)
	if !ok {
		t.Fatalf("queryDef not nested object: %T %v", got["queryDef"], got["queryDef"])
	}
	if qd["match"] != "all" {
		t.Errorf("queryDef.match = %v, want all", qd["match"])
	}
}

func TestSmartCollection_Create_NameRequired_400(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/smart-collections", nil, []byte(`{}`), "1", "", hb.H.handleCreateSmartCollection)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSmartCollection_Create_InvalidQueryDef_400(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	body := []byte(`{"name":"x","query_def":{"match":"all","groups":[{"match":"all","rules":[{"field":"nonsense","op":"is","value":1}]}]}}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/smart-collections", nil, body, "1", "", hb.H.handleCreateSmartCollection)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSmartCollection_Create_InvalidBody_400(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/smart-collections", nil, []byte(`{not json`), "1", "", hb.H.handleCreateSmartCollection)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSmartCollection_List_WrappedAsItems(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	_ = createSmartCollectionForUser(t, hb, "1", "", `{"name":"a"}`)
	_ = createSmartCollectionForUser(t, hb, "1", "", `{"name":"b"}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections", nil, nil, "1", "", hb.H.handleListSmartCollections)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, ok := env["items"].([]any)
	if !ok {
		t.Fatalf("response missing 'items' key (wrapped envelope); body=%s", rec.Body.String())
	}
	if len(list) != 2 {
		t.Errorf("list len = %d, want 2", len(list))
	}
}

func TestSmartCollection_List_DoesNotLeakOtherUsers(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	_ = createSmartCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections", nil, nil, "2", "", hb.H.handleListSmartCollections)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, _ := env["items"].([]any)
	if len(list) != 0 {
		t.Errorf("user 2 sees %d, want 0", len(list))
	}
}

func TestSmartCollection_Get_Owner_ReturnsFullShape(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleGetSmartCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "mine" {
		t.Errorf("name = %v", got["name"])
	}
}

func TestSmartCollection_Get_NonOwner_Private_404(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"private"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleGetSmartCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSmartCollection_Get_NonOwner_Public_OK(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"public","isPublic":true}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleGetSmartCollection)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestSmartCollection_Get_Unknown_404(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/01HZZZ", map[string]string{"id": "01HZZZ"}, nil, "1", "", hb.H.handleGetSmartCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSmartCollection_Patch_OwnerUpdates(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"old"}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/me/smart-collections/"+id, map[string]string{"id": id}, []byte(`{"name":"new","isPinned":true}`), "1", "", hb.H.handleUpdateSmartCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "new" || got["isPinned"] != true {
		t.Errorf("PATCH didn't apply: %v", got)
	}
}

func TestSmartCollection_Patch_NonOwner_404(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/me/smart-collections/"+id, map[string]string{"id": id}, []byte(`{"name":"hijack"}`), "2", "", hb.H.handleUpdateSmartCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	c, _ := hb.SC.GetSmartCollection(context.Background(), id)
	if c.Name != "mine" {
		t.Errorf("non-owner leak: %q", c.Name)
	}
}

func TestSmartCollection_Patch_InvalidQueryDef_400(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"x"}`)
	body := []byte(`{"query_def":{"match":"all","groups":[{"match":"all","rules":[{"field":"nonsense","op":"is","value":1}]}]}}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/me/smart-collections/"+id, map[string]string{"id": id}, body, "1", "", hb.H.handleUpdateSmartCollection)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSmartCollection_Delete_Owner_204(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"x"}`)
	rec := dispatchABSWithParams(http.MethodDelete, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleDeleteSmartCollection)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	rec2 := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleGetSmartCollection)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("post-delete GET status = %d, want 404", rec2.Code)
	}
}

func TestSmartCollection_Delete_NonOwner_404(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)
	rec := dispatchABSWithParams(http.MethodDelete, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleDeleteSmartCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if _, err := hb.SC.GetSmartCollection(context.Background(), id); err != nil {
		t.Errorf("non-owner DELETE leaked: %v", err)
	}
}

func TestSmartCollection_Items_OwnerEvaluatesRules(t *testing.T) {
	hb := newSmartCollectionsHarness(t, "book-a", "book-b", "book-c")
	id := createSmartCollectionForUser(t, hb, "1", "",
		`{"name":"a-only","query_def":{"match":"all","groups":[{"match":"all","rules":[{"field":"title","op":"contains","value":"book-a"}]}]}}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id+"/items",
		map[string]string{"id": id}, nil, "1", "", hb.H.handleSmartCollectionItems)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	results, _ := env["results"].([]any)
	if len(results) != 1 {
		t.Errorf("results len = %d, want 1; body=%s", len(results), rec.Body.String())
	}
}

func TestSmartCollection_Items_PaginatedEnvelope(t *testing.T) {
	hb := newSmartCollectionsHarness(t, "book-a", "book-b")
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"all"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id+"/items",
		map[string]string{"id": id}, nil, "1", "", hb.H.handleSmartCollectionItems)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	for _, k := range []string{"results", "total", "limit", "page", "sortBy", "sortDesc", "filterBy", "minified", "include"} {
		if _, has := env[k]; !has {
			t.Errorf("envelope missing %q", k)
		}
	}
}

func TestSmartCollection_Items_NonOwnerPrivate_404(t *testing.T) {
	hb := newSmartCollectionsHarness(t, "book-a")
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"private"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id+"/items",
		map[string]string{"id": id}, nil, "2", "", hb.H.handleSmartCollectionItems)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSmartCollection_Items_NonOwnerPublic_OK(t *testing.T) {
	hb := newSmartCollectionsHarness(t, "book-a")
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"public","isPublic":true}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id+"/items",
		map[string]string{"id": id}, nil, "2", "", hb.H.handleSmartCollectionItems)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
