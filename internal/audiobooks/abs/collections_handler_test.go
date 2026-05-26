package abs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ---------------------------------------------------------------------------
// In-memory fakes
// ---------------------------------------------------------------------------

// memCollectionStore is an in-memory CollectionStore for handler tests.
// Owner identity is tracked alongside the row (production stores user_id
// and profile_id; we mirror that so List can filter correctly).
type memCollectionStore struct {
	mu    sync.Mutex
	rows  map[string]Collection      // id -> row
	items map[string][]CollectionItem // collection_id -> items
}

func newMemCollectionStore() *memCollectionStore {
	return &memCollectionStore{
		rows:  map[string]Collection{},
		items: map[string][]CollectionItem{},
	}
}

func (m *memCollectionStore) ListUserCollections(_ context.Context, userID, profileID string) ([]Collection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Collection, 0)
	for _, c := range m.rows {
		if c.UserID == userID && c.ProfileID == profileID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memCollectionStore) GetCollection(_ context.Context, id string) (Collection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[id]
	if !ok {
		return Collection{}, ErrNotFound
	}
	return c, nil
}

func (m *memCollectionStore) CreateCollection(_ context.Context, c Collection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[c.ID] = c
	return nil
}

func (m *memCollectionStore) UpdateCollection(_ context.Context, c Collection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.rows[c.ID]
	if !ok {
		return ErrNotFound
	}
	existing.Name = c.Name
	existing.Description = c.Description
	existing.IsPublic = c.IsPublic
	existing.UpdatedAt = time.Now()
	m.rows[c.ID] = existing
	return nil
}

func (m *memCollectionStore) DeleteCollection(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, id)
	delete(m.items, id) // cascade
	return nil
}

func (m *memCollectionStore) ListCollectionItems(_ context.Context, collectionID string) ([]CollectionItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.items[collectionID]
	out := make([]CollectionItem, len(items))
	copy(out, items)
	sort.Slice(out, func(i, j int) bool { return out[i].AddedAt.Before(out[j].AddedAt) })
	return out, nil
}

func (m *memCollectionStore) AddCollectionItem(_ context.Context, collectionID, libraryItemID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.items[collectionID] {
		if it.LibraryItemID == libraryItemID {
			return nil // ON CONFLICT DO NOTHING
		}
	}
	m.items[collectionID] = append(m.items[collectionID], CollectionItem{
		CollectionID:  collectionID,
		LibraryItemID: libraryItemID,
		AddedAt:       time.Now(),
	})
	if c, ok := m.rows[collectionID]; ok {
		c.UpdatedAt = time.Now()
		m.rows[collectionID] = c
	}
	return nil
}

func (m *memCollectionStore) RemoveCollectionItem(_ context.Context, collectionID, libraryItemID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.items[collectionID]
	out := items[:0]
	for _, it := range items {
		if it.LibraryItemID != libraryItemID {
			out = append(out, it)
		}
	}
	m.items[collectionID] = out
	if c, ok := m.rows[collectionID]; ok {
		c.UpdatedAt = time.Now()
		m.rows[collectionID] = c
	}
	return nil
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

type collectionsHarness struct {
	H    *Handler
	Coll *memCollectionStore
	Pub  *recordingPublisher
}

func newCollectionsHarness(t *testing.T, knownItems ...string) *collectionsHarness {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = nil
	}
	pub := &recordingPublisher{}
	store := newMemCollectionStore()
	h := New(Dependencies{
		MediaStore:      &stubMediaStore{known: known},
		CollectionStore: store,
		Publisher:       pub,
	})
	return &collectionsHarness{H: h, Coll: store, Pub: pub}
}

// dispatchABSWithParams drives a handler directly with arbitrary URL
// params + injected ctxAuth, bypassing the bearerAuth middleware.
// Generalised version of dispatchBookmark for surfaces with different
// URL-param shapes (collections use {id}, {bookId}; playlists use
// {id}, {libraryItemId}, {episodeId}).
func dispatchABSWithParams(method, path string, params map[string]string, body []byte, userID, profileID string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, ctxKey{}, ctxAuth{UserID: userID, ProfileID: profileID})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	fn(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCollection_Create_ReturnsFullShape(t *testing.T) {
	hb := newCollectionsHarness(t)
	body := []byte(`{"name":"Favorites","description":"My top picks"}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/collections", nil, body, "1", "", hb.H.handleCreateCollection)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if got["name"] != "Favorites" {
		t.Errorf("name = %v, want Favorites", got["name"])
	}
	if got["description"] != "My top picks" {
		t.Errorf("description = %v, want 'My top picks'", got["description"])
	}
	if got["userId"] != "1" {
		t.Errorf("userId = %v, want 1", got["userId"])
	}
	if got["isPublic"] != false {
		t.Errorf("isPublic = %v, want false", got["isPublic"])
	}
	for _, k := range []string{"id", "lastUpdate", "createdAt"} {
		if _, ok := got[k]; !ok {
			t.Errorf("response missing %q", k)
		}
	}
	books, ok := got["books"].([]any)
	if !ok || len(books) != 0 {
		t.Errorf("books = %v (type %T), want empty array", got["books"], got["books"])
	}
}

func TestCollection_Create_NameRequired_400(t *testing.T) {
	hb := newCollectionsHarness(t)
	body := []byte(`{"description":"only"}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/collections", nil, body, "1", "", hb.H.handleCreateCollection)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCollection_Create_InvalidBody_400(t *testing.T) {
	hb := newCollectionsHarness(t)
	rec := dispatchABSWithParams(http.MethodPost, "/api/collections", nil, []byte(`{not json`), "1", "", hb.H.handleCreateCollection)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCollection_List_ReturnsWrappedEnvelope(t *testing.T) {
	hb := newCollectionsHarness(t)
	// Seed two collections.
	_ = dispatchABSWithParams(http.MethodPost, "/api/collections", nil, []byte(`{"name":"A"}`), "1", "", hb.H.handleCreateCollection)
	_ = dispatchABSWithParams(http.MethodPost, "/api/collections", nil, []byte(`{"name":"B"}`), "1", "", hb.H.handleCreateCollection)

	rec := dispatchABSWithParams(http.MethodGet, "/api/collections", nil, nil, "1", "", hb.H.handleListCollections)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	list, ok := env["collections"].([]any)
	if !ok {
		t.Fatalf("response missing 'collections' key; body=%s", rec.Body.String())
	}
	if len(list) != 2 {
		t.Errorf("list len = %d, want 2", len(list))
	}
	// List-shape must omit books.
	for _, c := range list {
		entry := c.(map[string]any)
		if _, has := entry["books"]; has {
			t.Errorf("list entry has books key (should be detail-only): %v", entry)
		}
	}
}

func TestCollection_List_DoesNotLeakOtherUsers(t *testing.T) {
	hb := newCollectionsHarness(t)
	// User 1 creates.
	_ = dispatchABSWithParams(http.MethodPost, "/api/collections", nil, []byte(`{"name":"mine"}`), "1", "", hb.H.handleCreateCollection)
	// User 2 lists.
	rec := dispatchABSWithParams(http.MethodGet, "/api/collections", nil, nil, "2", "", hb.H.handleListCollections)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, _ := env["collections"].([]any)
	if len(list) != 0 {
		t.Errorf("user 2 sees %d collections, want 0", len(list))
	}
}

func TestCollection_List_ProfileIsolation(t *testing.T) {
	hb := newCollectionsHarness(t)
	pA := "00000000-0000-0000-0000-0000000000aa"
	pB := "00000000-0000-0000-0000-0000000000bb"
	_ = dispatchABSWithParams(http.MethodPost, "/api/collections", nil, []byte(`{"name":"A"}`), "1", pA, hb.H.handleCreateCollection)
	rec := dispatchABSWithParams(http.MethodGet, "/api/collections", nil, nil, "1", pB, hb.H.handleListCollections)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, _ := env["collections"].([]any)
	if len(list) != 0 {
		t.Errorf("profile B sees %d collections, want 0", len(list))
	}
}

// createCollectionForUser is a tiny helper that POSTs a collection and
// returns its id. Used by tests that need to seed a row.
func createCollectionForUser(t *testing.T, hb *collectionsHarness, userID, profileID, body string) string {
	t.Helper()
	rec := dispatchABSWithParams(http.MethodPost, "/api/collections", nil, []byte(body), userID, profileID, hb.H.handleCreateCollection)
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

func TestCollection_Get_Owner_ReturnsFullShape(t *testing.T) {
	hb := newCollectionsHarness(t)
	id := createCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/collections/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleGetCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "mine" {
		t.Errorf("name = %v, want 'mine'", got["name"])
	}
	books, ok := got["books"].([]any)
	if !ok {
		t.Errorf("books missing on full-shape response: %v", got)
	}
	if len(books) != 0 {
		t.Errorf("books len = %d, want 0 for freshly created", len(books))
	}
}

func TestCollection_Get_NonOwner_Private_404(t *testing.T) {
	hb := newCollectionsHarness(t)
	id := createCollectionForUser(t, hb, "1", "", `{"name":"private"}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/collections/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleGetCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("non-owner private GET status = %d, want 404 (anti-enumeration); body=%s", rec.Code, rec.Body.String())
	}
}

func TestCollection_Get_NonOwner_Public_OK(t *testing.T) {
	hb := newCollectionsHarness(t)
	id := createCollectionForUser(t, hb, "1", "", `{"name":"public","isPublic":true}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/collections/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleGetCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-owner public GET status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "public" {
		t.Errorf("name = %v, want 'public'", got["name"])
	}
}

func TestCollection_Get_Unknown_404(t *testing.T) {
	hb := newCollectionsHarness(t)
	rec := dispatchABSWithParams(http.MethodGet, "/api/collections/01HZZZ", map[string]string{"id": "01HZZZ"}, nil, "1", "", hb.H.handleGetCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}
