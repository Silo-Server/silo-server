package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

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
	H  *Handler
	SC *memSmartCollectionStore
}

func newSmartCollectionsHarness(t *testing.T, knownItems ...string) *smartCollectionsHarness {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = nil
	}
	store := newMemSmartCollectionStore()
	h := New(Dependencies{
		MediaStore:           &stubMediaStore{known: known},
		SmartCollectionStore: store,
	})
	return &smartCollectionsHarness{H: h, SC: store}
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
