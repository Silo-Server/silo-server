package abs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type memRSSFeedStore struct {
	mu   sync.Mutex
	rows map[string]RSSFeed
}

func newMemRSSFeedStore() *memRSSFeedStore { return &memRSSFeedStore{rows: map[string]RSSFeed{}} }

func (m *memRSSFeedStore) ListUserFeeds(_ context.Context, userID, profileID string) ([]RSSFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RSSFeed, 0)
	for _, f := range m.rows {
		if f.UserID == userID && f.ProfileID == profileID && f.ClosedAt == nil {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memRSSFeedStore) GetFeed(_ context.Context, id string) (RSSFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.rows[id]
	if !ok {
		return RSSFeed{}, ErrNotFound
	}
	return f, nil
}

func (m *memRSSFeedStore) GetFeedBySlug(_ context.Context, slug string) (RSSFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.rows {
		if f.Slug == slug && f.ClosedAt == nil {
			return f, nil
		}
	}
	return RSSFeed{}, ErrNotFound
}

func (m *memRSSFeedStore) CreateFeed(_ context.Context, f RSSFeed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.rows {
		if existing.Slug == f.Slug && existing.ClosedAt == nil {
			return errors.New("unique violation duplicate key")
		}
	}
	f.CreatedAt = time.Now()
	m.rows[f.ID] = f
	return nil
}

func (m *memRSSFeedStore) CloseFeed(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.rows[id]
	if !ok {
		return nil
	}
	now := time.Now()
	f.ClosedAt = &now
	m.rows[id] = f
	return nil
}

func newFeedsHarness(t *testing.T, knownItems ...string) (*Handler, *memRSSFeedStore) {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = nil
	}
	store := newMemRSSFeedStore()
	h := New(Dependencies{MediaStore: &stubMediaStore{known: known}, RSSFeedStore: store})
	return h, store
}

func TestFeed_Open_GeneratesSlug(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	slug, _ := got["slug"].(string)
	if len(slug) != 16 {
		t.Errorf("slug = %q (len %d), want 16-char auto-generated", slug, len(slug))
	}
}

func TestFeed_Open_CustomSlug(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{"slug":"my-cool-feed"}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["slug"] != "my-cool-feed" {
		t.Errorf("slug = %v, want my-cool-feed", got["slug"])
	}
}

func TestFeed_Open_InvalidSlug_400(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{"slug":"BAD!"}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestFeed_Open_Collision_409(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	body := []byte(`{"slug":"taken-slug"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, body, "1", "", h.handleOpenItemFeed)
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, body, "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestFeed_Open_UnknownItem_404(t *testing.T) {
	h, _ := newFeedsHarness(t)
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/ghost/open",
		map[string]string{"itemId": "ghost"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestFeed_List_OwnerOnly(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	_ = dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	rec := dispatchABSWithParams(http.MethodGet, "/api/feeds", nil, nil, "2", "", h.handleListRSSFeeds)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	feeds, _ := env["feeds"].([]any)
	if len(feeds) != 0 {
		t.Errorf("user 2 sees %d feeds, want 0", len(feeds))
	}
}

func TestFeed_Close_Owner(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	openRec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	var open map[string]any
	_ = json.Unmarshal(openRec.Body.Bytes(), &open)
	id, _ := open["id"].(string)

	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/"+id+"/close", map[string]string{"id": id}, nil, "1", "", h.handleCloseFeed)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestFeed_Close_NonOwner_404(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	openRec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	var open map[string]any
	_ = json.Unmarshal(openRec.Body.Bytes(), &open)
	id, _ := open["id"].(string)

	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/"+id+"/close", map[string]string{"id": id}, nil, "2", "", h.handleCloseFeed)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
