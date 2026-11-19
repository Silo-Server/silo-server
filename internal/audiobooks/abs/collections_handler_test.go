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
