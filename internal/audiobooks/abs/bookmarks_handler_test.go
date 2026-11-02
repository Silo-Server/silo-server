package abs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ---------------------------------------------------------------------------
// In-memory fakes
// ---------------------------------------------------------------------------

// memBookmarkStore is an in-memory BookmarkStore for handler tests.
// Keyed on (userID, profileID, itemID, time) to mirror the SQL unique
// index. Thread-safe so parallel sub-tests can share an instance.
type memBookmarkStore struct {
	mu   sync.Mutex
	rows map[string]Bookmark // key = userID|profileID|itemID|time
	seq  int                 // monotonic counter for deterministic IDs in tests
}

func newMemBookmarkStore() *memBookmarkStore {
	return &memBookmarkStore{rows: map[string]Bookmark{}}
}

func bkKey(userID, profileID, itemID string, t float64) string {
	return userID + "|" + profileID + "|" + itemID + "|" + formatTime(t)
}

func formatTime(t float64) string {
	// Round-trip-safe encoding for map keys. Postgres compares float8
	// bit-for-bit too, so this matches production semantics.
	b, _ := json.Marshal(t)
	return string(b)
}

// List iterates the keyed map directly so a row only matches when ALL
// of (user, profile, item) line up. Iterating values and reconstructing
// the key would be ambiguous when two users have a bookmark at the
// same (item, time).
func (m *memBookmarkStore) List(_ context.Context, userID, profileID, itemID string) ([]Bookmark, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := userID + "|" + profileID + "|" + itemID + "|"
	out := make([]Bookmark, 0)
	for k, b := range m.rows {
		if strings.HasPrefix(k, prefix) {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out, nil
}

func (m *memBookmarkStore) Upsert(_ context.Context, userID, profileID, itemID string, t float64, title string) (Bookmark, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := bkKey(userID, profileID, itemID, t)
	now := time.Now()
	if existing, ok := m.rows[key]; ok {
		existing.Title = title
		existing.UpdatedAt = now
		m.rows[key] = existing
		return existing, nil
	}
	m.seq++
	b := Bookmark{
		ID:            "01HTEST" + formatSeq(m.seq),
		LibraryItemID: itemID,
		Time:          t,
		Title:         title,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	m.rows[key] = b
	return b, nil
}

func (m *memBookmarkStore) Delete(_ context.Context, userID, profileID, itemID string, t float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, bkKey(userID, profileID, itemID, t))
	return nil
}

func (m *memBookmarkStore) CountByUser(_ context.Context, userID, profileID string) (map[string]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	prefix := userID + "|" + profileID + "|"
	for k := range m.rows {
		if strings.HasPrefix(k, prefix) {
			rest := k[len(prefix):]
			sep := -1
			for i, c := range rest {
				if c == '|' {
					sep = i
					break
				}
			}
			if sep < 0 {
				continue
			}
			itemID := rest[:sep]
			out[itemID]++
		}
	}
	return out, nil
}

func formatSeq(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// recordingPublisher captures publish() calls so tests can assert socket
// event semantics without wiring a real Socket.io server.
type recordingPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

type publishedEvent struct {
	UserID  string
	Event   string
	Payload any
}

func (p *recordingPublisher) Publish(userID, event string, payload any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, publishedEvent{UserID: userID, Event: event, Payload: payload})
}
func (p *recordingPublisher) Broadcast(_ string, _ any) {}

// stubMediaStore satisfies MediaStore with a configurable item lookup so
// handler tests can drive both the 200 and 404 branches.
type stubMediaStore struct {
	noopMediaStore
	known     map[string]*models.MediaItem // itemID → row (nil means "exists but no row needed")
	lookupErr error
}

func (s *stubMediaStore) GetAudiobookByID(_ context.Context, id string, _ catalog.AccessFilter) (*models.MediaItem, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	if it, ok := s.known[id]; ok {
		if it == nil {
			return &models.MediaItem{ContentID: id}, nil
		}
		return it, nil
	}
	return nil, nil
}
func (s *stubMediaStore) GetAudiobooksByIDs(_ context.Context, ids []string, _ catalog.AccessFilter) (map[string]*models.MediaItem, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	out := make(map[string]*models.MediaItem, len(ids))
	for _, id := range ids {
		if it, ok := s.known[id]; ok {
			if it == nil {
				out[id] = &models.MediaItem{ContentID: id}
			} else {
				out[id] = it
			}
		}
	}
	return out, nil
}
func (s *stubMediaStore) GetAuthorByID(_ context.Context, id string, _ catalog.AccessFilter) (Author, error) {
	return Author{}, ErrNotFound
}
func (s *stubMediaStore) GetSeriesByName(_ context.Context, name string, _ catalog.AccessFilter) (Series, error) {
	return Series{}, ErrNotFound
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

type bookmarksHarness struct {
	H    *Handler
	Pub  *recordingPublisher
	Book *memBookmarkStore
}

func newBookmarksHarness(t *testing.T, knownItems ...string) *bookmarksHarness {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = nil // exists, body content not used by handlers
	}
	pub := &recordingPublisher{}
	store := newMemBookmarkStore()
	h := New(Dependencies{
		MediaStore:    &stubMediaStore{known: known},
		BookmarkStore: store,
		Publisher:     pub,
	})
	return &bookmarksHarness{H: h, Pub: pub, Book: store}
}

// dispatchBookmark drives a bookmarks handler directly. Injects ctxAuth
// (the bearerAuth middleware's product) and chi route params so the
// handler can read both via absAuthFrom() and chi.URLParam() without
// running the full middleware chain.
func dispatchBookmark(h *Handler, method, path, itemID, timeParam string, body []byte, userID, profileID string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	var req *http.Request
	if rd != nil {
		req = httptest.NewRequest(method, path, rd)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rctx := chi.NewRouteContext()
	if itemID != "" {
		rctx.URLParams.Add("itemId", itemID)
	}
	if timeParam != "" {
		rctx.URLParams.Add("time", timeParam)
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

func TestProfileIsolation_BookmarksScopedPerProfile(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")

	// Profile A inserts.
	_ = dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{"title":"a","time":1}`), "1", "00000000-0000-0000-0000-0000000000aa", hb.H.handleUpsertBookmark("bookmark_created"))

	// Profile B (same user) reads via POST at a different time so we get the
	// list back. Profile B's POST should return only profile B's bookmarks.
	rec := dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{"title":"b","time":2}`), "1", "00000000-0000-0000-0000-0000000000bb", hb.H.handleUpsertBookmark("bookmark_created"))

	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("profile B list len = %d, want 1 (isolation broken)", len(list))
	}
	if list[0]["title"] != "b" {
		t.Errorf("profile B saw profile A's bookmark: %v", list[0])
	}
}

func TestDelete_OtherUserBookmark_NoOpAndNoExistenceLeak(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")

	// User B seeds a bookmark.
	_ = dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{"title":"B's bookmark","time":42.5}`), "2", "", hb.H.handleUpsertBookmark("bookmark_created"))

	// User A tries to DELETE at the same item+time.
	rec := dispatchBookmark(hb.H, http.MethodDelete, "/api/me/item/book-1/bookmark/42.5", "book-1", "42.5", nil, "1", "", hb.H.handleDeleteBookmark)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no leak); body=%s", rec.Code, rec.Body.String())
	}
	var aList []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &aList)
	if len(aList) != 0 {
		t.Errorf("user A response list = %v, want empty", aList)
	}

	// User B's bookmark must still be there.
	bList, err := hb.Book.List(context.Background(), "2", "", "book-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(bList) != 1 {
		t.Errorf("user B bookmarks = %d, want 1 (was wrongly deleted)", len(bList))
	}
}
