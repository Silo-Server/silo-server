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
	rows  map[string]Collection       // id -> row
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

// createForUser POSTs a create body through fn as (userID, profileID) and
// returns the new row's id. It seeds collections, playlists and smart
// collections.
func createForUser(t *testing.T, path, userID, profileID, body string, fn http.HandlerFunc) string {
	t.Helper()
	rec := dispatchABSWithParams(http.MethodPost, path, nil, []byte(body), userID, profileID, fn)
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

// nonOwnerCase is one request user 2 makes against user 1's row. seed is the
// owner's create body (default {"name":"mine"}); known is a media item the
// harness can resolve; before runs as the owner ahead of the request; check
// asserts the owner's row is unchanged.
type nonOwnerCase[H any] struct {
	name, seed, known string
	method, suffix    string
	params            map[string]string
	body              string
	handler           func(*Handler, http.ResponseWriter, *http.Request)
	before, check     func(t *testing.T, hb H, id string)
}

// runNonOwner404 creates the owner's row in a fresh harness for each case and
// asserts that user 2's request answers 404 without leaking or mutating it.
func runNonOwner404[H any](t *testing.T, cases []nonOwnerCase[H], newHarness func(t *testing.T, known ...string) H, handler func(H) *Handler, base string, create func(*Handler) http.HandlerFunc) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var known []string
			if tc.known != "" {
				known = append(known, tc.known)
			}
			hb := newHarness(t, known...)
			h := handler(hb)
			seed := tc.seed
			if seed == "" {
				seed = `{"name":"mine"}`
			}
			id := createForUser(t, base, "1", "", seed, create(h))
			if tc.before != nil {
				tc.before(t, hb, id)
			}
			params := map[string]string{"id": id}
			for k, v := range tc.params {
				params[k] = v
			}
			var body []byte
			if tc.body != "" {
				body = []byte(tc.body)
			}
			rec := dispatchABSWithParams(tc.method, base+"/"+id+tc.suffix, params, body, "2", "",
				func(w http.ResponseWriter, r *http.Request) { tc.handler(h, w, r) })
			if rec.Code != http.StatusNotFound {
				t.Errorf("non-owner status = %d, want 404 (no leak); body=%s", rec.Code, rec.Body.String())
			}
			if tc.check != nil {
				tc.check(t, hb, id)
			}
		})
	}
}

func TestCollection_NonOwner_404(t *testing.T) {
	book := map[string]string{"bookId": "book-1"}
	runNonOwner404(t, []nonOwnerCase[*collectionsHarness]{
		{name: "get private", seed: `{"name":"private"}`, method: http.MethodGet, handler: (*Handler).handleGetCollection},
		{name: "patch", method: http.MethodPatch, body: `{"name":"hijack"}`, handler: (*Handler).handleUpdateCollection,
			check: func(t *testing.T, hb *collectionsHarness, id string) {
				if c, _ := hb.Coll.GetCollection(context.Background(), id); c.Name != "mine" {
					t.Errorf("collection name = %q, want 'mine'; non-owner mutation leaked", c.Name)
				}
			}},
		{name: "delete", method: http.MethodDelete, handler: (*Handler).handleDeleteCollection,
			check: func(t *testing.T, hb *collectionsHarness, id string) {
				if _, err := hb.Coll.GetCollection(context.Background(), id); err != nil {
					t.Errorf("collection wrongly deleted: %v", err)
				}
			}},
		{name: "add book", known: "book-1", method: http.MethodPost, suffix: "/book/book-1", params: book, handler: (*Handler).handleAddCollectionBook},
		{name: "remove book", known: "book-1", method: http.MethodDelete, suffix: "/book/book-1", params: book, handler: (*Handler).handleRemoveCollectionBook,
			before: func(t *testing.T, hb *collectionsHarness, id string) {
				_ = dispatchABSWithParams(http.MethodPost, "/api/collections/"+id+"/book/book-1",
					map[string]string{"id": id, "bookId": "book-1"}, nil, "1", "", hb.H.handleAddCollectionBook)
			},
			check: func(t *testing.T, hb *collectionsHarness, id string) {
				if items, _ := hb.Coll.ListCollectionItems(context.Background(), id); len(items) != 1 {
					t.Errorf("items len = %d, want 1 (non-owner remove leaked)", len(items))
				}
			}},
	}, newCollectionsHarness, func(hb *collectionsHarness) *Handler { return hb.H }, "/api/collections",
		func(h *Handler) http.HandlerFunc { return h.handleCreateCollection })
}

// TestCollection_List_Isolation creates a collection as user 1 and lists as
// another user, or as another profile of the same account.
func TestCollection_List_Isolation(t *testing.T) {
	pA := "00000000-0000-0000-0000-0000000000aa"
	pB := "00000000-0000-0000-0000-0000000000bb"
	for _, tc := range []struct {
		name                  string
		body, createProfile   string
		listUser, listProfile string
	}{
		{name: "user 2", body: `{"name":"mine"}`, listUser: "2"},
		{name: "profile B", body: `{"name":"A"}`, createProfile: pA, listUser: "1", listProfile: pB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hb := newCollectionsHarness(t)
			_ = dispatchABSWithParams(http.MethodPost, "/api/collections", nil, []byte(tc.body), "1", tc.createProfile, hb.H.handleCreateCollection)
			rec := dispatchABSWithParams(http.MethodGet, "/api/collections", nil, nil, tc.listUser, tc.listProfile, hb.H.handleListCollections)
			var env map[string]any
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
			}
			list, ok := env["collections"].([]any)
			if !ok {
				t.Fatalf("response missing 'collections' key; body=%s", rec.Body.String())
			}
			if len(list) != 0 {
				t.Errorf("%s sees %d collections, want 0", tc.name, len(list))
			}
		})
	}
}
