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

func (p *recordingPublisher) snapshot() []publishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]publishedEvent, len(p.events))
	copy(out, p.events)
	return out
}

// stubMediaStore satisfies MediaStore with a configurable item lookup so
// handler tests can drive both the 200 and 404 branches.
type stubMediaStore struct {
	noopMediaStore
	known map[string]*models.MediaItem // itemID → row (nil means "exists but no row needed")
}

func (s *stubMediaStore) GetAudiobookByID(_ context.Context, id string) (*models.MediaItem, error) {
	if it, ok := s.known[id]; ok {
		if it == nil {
			return &models.MediaItem{ContentID: id}, nil
		}
		return it, nil
	}
	return nil, nil
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

func TestCreate_NewBookmark_ReturnsListContainingIt(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")
	body := []byte(`{"title":"Chapter cliffhanger","time":42.5}`)
	rec := dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", body, "1", "", hb.H.handleUpsertBookmark("bookmark_created"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1; body=%s", len(list), rec.Body.String())
	}
	got := list[0]
	if got["libraryItemId"] != "book-1" {
		t.Errorf("libraryItemId = %v, want book-1", got["libraryItemId"])
	}
	if got["time"] != 42.5 {
		t.Errorf("time = %v, want 42.5", got["time"])
	}
	if got["title"] != "Chapter cliffhanger" {
		t.Errorf("title = %v, want Chapter cliffhanger", got["title"])
	}
	for _, k := range []string{"id", "createdAt", "updatedAt"} {
		if _, ok := got[k]; !ok {
			t.Errorf("response missing %q; body=%s", k, rec.Body.String())
		}
	}
}

func TestUpsert_SameTime_UpdatesTitleNoDuplicate(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")

	// POST first.
	postBody := []byte(`{"title":"first","time":10}`)
	rec := dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", postBody, "1", "", hb.H.handleUpsertBookmark("bookmark_created"))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var postList []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &postList)
	if len(postList) != 1 {
		t.Fatalf("after POST list len = %d, want 1", len(postList))
	}
	firstID := postList[0]["id"]

	// PATCH at the same time with a new title.
	patchBody := []byte(`{"title":"renamed","time":10}`)
	rec2 := dispatchBookmark(hb.H, http.MethodPatch, "/api/me/item/book-1/bookmark", "book-1", "", patchBody, "1", "", hb.H.handleUpsertBookmark("bookmark_updated"))
	if rec2.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	var patchList []map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &patchList); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec2.Body.String())
	}
	if len(patchList) != 1 {
		t.Fatalf("after PATCH list len = %d, want 1 (upsert, not insert)", len(patchList))
	}
	if patchList[0]["title"] != "renamed" {
		t.Errorf("title = %v, want renamed", patchList[0]["title"])
	}
	if patchList[0]["id"] != firstID {
		t.Errorf("id changed across upsert: was %v, now %v (id must be preserved)", firstID, patchList[0]["id"])
	}
}

func TestDelete_ExistingBookmark_RemovedFromList(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")

	// Seed a bookmark via POST.
	postBody := []byte(`{"title":"to delete","time":99}`)
	_ = dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", postBody, "1", "", hb.H.handleUpsertBookmark("bookmark_created"))

	// DELETE it.
	rec := dispatchBookmark(hb.H, http.MethodDelete, "/api/me/item/book-1/bookmark/99", "book-1", "99", nil, "1", "", hb.H.handleDeleteBookmark)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(list) != 0 {
		t.Errorf("list len = %d, want 0; body=%s", len(list), rec.Body.String())
	}
}

func TestDelete_NonExistentTime_IdempotentReturnsEmptyList(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")
	rec := dispatchBookmark(hb.H, http.MethodDelete, "/api/me/item/book-1/bookmark/123", "book-1", "123", nil, "1", "", hb.H.handleDeleteBookmark)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (idempotent); body=%s", rec.Code, rec.Body.String())
	}
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("list len = %d, want 0", len(list))
	}
}

func TestCreate_TwoAtDifferentTimes_ListOrderedByTime(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")

	_ = dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{"title":"later","time":100}`), "1", "", hb.H.handleUpsertBookmark("bookmark_created"))
	rec := dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{"title":"earlier","time":50}`), "1", "", hb.H.handleUpsertBookmark("bookmark_created"))

	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if list[0]["time"] != float64(50) || list[1]["time"] != float64(100) {
		t.Errorf("list times = [%v, %v], want [50, 100]", list[0]["time"], list[1]["time"])
	}
}

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

func TestMissingItem_404(t *testing.T) {
	hb := newBookmarksHarness(t /* no known items */)
	rec := dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/unknown/bookmark", "unknown", "", []byte(`{"title":"x","time":1}`), "1", "", hb.H.handleUpsertBookmark("bookmark_created"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestInvalidBody_400(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")
	rec := dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{not json`), "1", "", hb.H.handleUpsertBookmark("bookmark_created"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestMissingTime_400(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")
	rec := dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{"title":"no time"}`), "1", "", hb.H.handleUpsertBookmark("bookmark_created"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func assertOneEvent(t *testing.T, pub *recordingPublisher, wantUser, wantReason string) {
	t.Helper()
	evts := pub.snapshot()
	if len(evts) != 1 {
		t.Fatalf("publisher events = %d, want 1: %+v", len(evts), evts)
	}
	e := evts[0]
	if e.UserID != wantUser {
		t.Errorf("event userID = %q, want %q", e.UserID, wantUser)
	}
	if e.Event != "user_updated" {
		t.Errorf("event name = %q, want user_updated", e.Event)
	}
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", e.Payload)
	}
	if payload["reason"] != wantReason {
		t.Errorf("reason = %v, want %q", payload["reason"], wantReason)
	}
	if _, ok := payload["bookmark"].(map[string]any); !ok {
		t.Errorf("bookmark payload missing or wrong type: %T", payload["bookmark"])
	}
}

func TestSocketEvent_FiredOnCreate(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")
	_ = dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{"title":"x","time":1}`), "7", "", hb.H.handleUpsertBookmark("bookmark_created"))
	assertOneEvent(t, hb.Pub, "7", "bookmark_created")
}

func TestSocketEvent_FiredOnUpdate(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")
	// Seed (publishes a create event); then PATCH and only assert the
	// second event.
	_ = dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{"title":"x","time":1}`), "7", "", hb.H.handleUpsertBookmark("bookmark_created"))
	_ = dispatchBookmark(hb.H, http.MethodPatch, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{"title":"y","time":1}`), "7", "", hb.H.handleUpsertBookmark("bookmark_updated"))
	evts := hb.Pub.snapshot()
	if len(evts) != 2 {
		t.Fatalf("publisher events = %d, want 2", len(evts))
	}
	payload := evts[1].Payload.(map[string]any)
	if payload["reason"] != "bookmark_updated" {
		t.Errorf("second event reason = %v, want bookmark_updated", payload["reason"])
	}
}

func TestSocketEvent_FiredOnDelete(t *testing.T) {
	hb := newBookmarksHarness(t, "book-1")
	_ = dispatchBookmark(hb.H, http.MethodPost, "/api/me/item/book-1/bookmark", "book-1", "", []byte(`{"title":"x","time":1}`), "7", "", hb.H.handleUpsertBookmark("bookmark_created"))
	_ = dispatchBookmark(hb.H, http.MethodDelete, "/api/me/item/book-1/bookmark/1", "book-1", "1", nil, "7", "", hb.H.handleDeleteBookmark)
	evts := hb.Pub.snapshot()
	if len(evts) != 2 {
		t.Fatalf("publisher events = %d, want 2 (create + delete)", len(evts))
	}
	payload := evts[1].Payload.(map[string]any)
	if payload["reason"] != "bookmark_deleted" {
		t.Errorf("delete event reason = %v, want bookmark_deleted", payload["reason"])
	}
	bm, _ := payload["bookmark"].(map[string]any)
	if bm["title"] != "x" {
		t.Errorf("delete payload title = %v, want 'x' (pre-delete snapshot)", bm["title"])
	}
}
