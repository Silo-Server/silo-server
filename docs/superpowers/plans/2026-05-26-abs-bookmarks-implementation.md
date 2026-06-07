# ABS Bookmarks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the three Audiobookshelf-compatible bookmark endpoints (POST/PATCH/DELETE) plus realtime socket events, so the official ABS Android, ABS iOS, and Plappa clients can create, edit, and delete bookmarks against a silo audiobook library.

**Architecture:** New migration `148_abs_bookmarks` adds a postgres-backed `abs_bookmarks` table keyed on (user, profile, item, time). New `BookmarkStore` interface in `internal/audiobooks/abs/` with an in-memory fake for tests and a concrete `ABSBookmarkStore` (pgx) for production. New `bookmarks_handler.go` with one upsert handler (shared by POST/PATCH via a `reason`-parameterised closure) and one delete handler. Routes mount under both `/abs/api/*` and `/api/*` inside the existing `bearerAuth` group. Socket events ride on the existing nil-safe `Handler.publish` wrapper.

**Tech Stack:** Go 1.x, `chi/v5` router, `pgx/v5`, `oklog/ulid/v2`, internal `package abs` tests with in-memory fakes.

**Commands assume the repository root (`/opt/silo-server`) is the cwd.**

**Source spec:** `docs/superpowers/specs/2026-05-26-abs-bookmarks-design.md`. Re-read sections 4 (endpoint table), 5 (data model), 6 (storage contract), and 7 (error model) before each task — the plan implements the spec, it does not re-justify decisions already made there.

---

## File map

**Create:**
- `migrations/148_abs_bookmarks.up.sql`
- `migrations/148_abs_bookmarks.down.sql`
- `internal/audiobooks/abs/bookmarks.go` — `Bookmark` struct, `BookmarkStore` interface, `bookmarkToABS` serialiser.
- `internal/audiobooks/abs/bookmarks_handler.go` — `handleUpsertBookmark(reason) http.HandlerFunc`, `handleDeleteBookmark`.
- `internal/audiobooks/abs/bookmarks_handler_test.go` — in-memory fake `memBookmarkStore`, recording `recordingPublisher`, dispatch helper, all 13 spec tests.
- `internal/audiobooks/abs/bookmarks_envelope_test.go` — wire-shape test for `bookmarkToABS`.
- `internal/audiobooks/abs_bookmark_store.go` — `ABSBookmarkStore` (pgx-backed concrete impl).

**Modify:**
- `internal/audiobooks/abs/handler.go` — add `BookmarkStore` field on `Dependencies`; register the three routes inside the existing `bearerAuth` group at the bottom of `mountRoutes`.
- `internal/audiobooks/service.go` — construct `&ABSBookmarkStore{Pool: deps.Pool}` and pass it into `abs.Dependencies` inside `BuildABSHandler`.

---

## Task 1: Migration 148 — `abs_bookmarks`

**Files:**
- Create: `migrations/148_abs_bookmarks.up.sql`
- Create: `migrations/148_abs_bookmarks.down.sql`

- [ ] **Step 1: Write the up-migration**

Create `migrations/148_abs_bookmarks.up.sql`:

```sql
-- ABS bookmark rows. One row per (user, profile, item, time). Backs the
-- POST/PATCH/DELETE /me/item/{itemId}/bookmark endpoints in
-- internal/audiobooks/abs/bookmarks_handler.go.
--
-- profile_id is nullable because silo's "primary profile" is encoded as
-- NULL profile. The COALESCE-to-sentinel-UUID in the unique index
-- collapses NULL to a single bucket per user (raw NULL would be treated
-- as distinct for uniqueness purposes).

CREATE TABLE IF NOT EXISTS public.abs_bookmarks (
    id              text PRIMARY KEY,
    user_id         integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id      uuid,
    library_item_id text NOT NULL,
    time_seconds    double precision NOT NULL,
    title           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS abs_bookmarks_user_profile_item_time_uniq
    ON public.abs_bookmarks (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid),
        library_item_id,
        time_seconds
    );

CREATE INDEX IF NOT EXISTS abs_bookmarks_user_item_idx
    ON public.abs_bookmarks (user_id, library_item_id);
```

- [ ] **Step 2: Write the down-migration**

Create `migrations/148_abs_bookmarks.down.sql`:

```sql
DROP TABLE IF EXISTS public.abs_bookmarks;
```

- [ ] **Step 3: Apply locally to verify it parses**

Make sure local postgres is running:

```bash
docker compose up -d postgres redis
```

Then apply the migration by booting silo (the server runs migrations on startup). The Makefile target installs deps and compiles `./silo`:

```bash
make build
./silo --mode integrated 2>&1 | head -50
```

Expected: a startup log line acknowledging migration 148, no parse errors. `Ctrl+C` once you see the server settle.

If the project uses a separate migrate tool, the equivalent is `docker compose exec postgres psql -U silo -d silo -f /migrations/148_abs_bookmarks.up.sql` — but the startup path is the canonical one.

Verify the rollback also parses by applying it manually:

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/148_abs_bookmarks.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/148_abs_bookmarks.up.sql
```

Both should return `DROP TABLE` / `CREATE TABLE` etc. with no errors.

- [ ] **Step 4: Commit**

```bash
git add migrations/148_abs_bookmarks.up.sql migrations/148_abs_bookmarks.down.sql
git commit -m "$(cat <<'EOF'
feat(audiobooks): add abs_bookmarks migration (148)

Backs the upcoming ABS-compatible bookmark endpoints. Schema and
rationale documented in
docs/superpowers/specs/2026-05-26-abs-bookmarks-design.md §5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `Bookmark` type, `BookmarkStore` interface, envelope helper + test

**Files:**
- Create: `internal/audiobooks/abs/bookmarks.go`
- Create: `internal/audiobooks/abs/bookmarks_envelope_test.go`

- [ ] **Step 1: Write the failing envelope test**

Create `internal/audiobooks/abs/bookmarks_envelope_test.go`:

```go
package abs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBookmarkEnvelope_HasRequiredKeys asserts the wire shape ABS Android
// builds against: id, libraryItemId, time, title, createdAt, updatedAt,
// all camelCase and all present (no omitempty), including when title is
// empty — Android shows an "Untitled" placeholder client-side rather
// than treating missing-title differently from empty-title.
func TestBookmarkEnvelope_HasRequiredKeys(t *testing.T) {
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	out := bookmarkToABS(Bookmark{
		ID:            "01HXX",
		LibraryItemID: "126887",
		Time:          1234.5,
		Title:         "",
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(body)
	for _, key := range []string{
		`"id":`, `"libraryItemId":`, `"time":`, `"title":`,
		`"createdAt":`, `"updatedAt":`,
	} {
		if !strings.Contains(js, key) {
			t.Errorf("envelope missing %s; got %s", key, js)
		}
	}
	if out["title"] != "" {
		t.Errorf("title = %v, want empty string", out["title"])
	}
	wantMs := now.UnixMilli()
	if out["createdAt"] != wantMs {
		t.Errorf("createdAt = %v, want %d (UnixMilli)", out["createdAt"], wantMs)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/audiobooks/abs/ -run TestBookmarkEnvelope_HasRequiredKeys -v
```

Expected: build failure with "undefined: bookmarkToABS" / "undefined: Bookmark".

- [ ] **Step 3: Create the type, interface, and envelope helper**

Create `internal/audiobooks/abs/bookmarks.go`:

```go
package abs

import (
	"context"
	"time"
)

// BookmarkStore is the narrow slice of the abs_bookmarks table the
// bookmarks handlers need. Implemented by ABSBookmarkStore in
// internal/audiobooks/abs_bookmark_store.go.
type BookmarkStore interface {
	// List returns all bookmarks for (user, profile, item) ordered by
	// time ASC. Returns an empty slice (never nil) when none exist.
	List(ctx context.Context, userID, profileID, itemID string) ([]Bookmark, error)
	// Upsert inserts a bookmark or updates the title at the exact
	// (user, profile, item, time) tuple. ID is generated on insert and
	// preserved on update. Returns the resulting row.
	Upsert(ctx context.Context, userID, profileID, itemID string, timeSeconds float64, title string) (Bookmark, error)
	// Delete removes the bookmark at (user, profile, item, time).
	// Returns nil when no row matched — DELETE is idempotent (a UX
	// convenience, not a 404 surface). See spec §6.
	Delete(ctx context.Context, userID, profileID, itemID string, timeSeconds float64) error
}

// Bookmark is the in-memory representation of an abs_bookmarks row as
// the handlers use it. Intentionally narrow — only the fields the wire
// format cares about.
type Bookmark struct {
	ID            string  // ULID
	LibraryItemID string
	Time          float64 // fractional seconds
	Title         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// bookmarkToABS shapes a Bookmark into the ABS wire format the Android
// and iOS clients expect. All six keys are always present (no
// omitempty), camelCase, with timestamps as JS-epoch milliseconds.
func bookmarkToABS(b Bookmark) map[string]any {
	return map[string]any{
		"id":            b.ID,
		"libraryItemId": b.LibraryItemID,
		"time":          b.Time,
		"title":         b.Title,
		"createdAt":     b.CreatedAt.UnixMilli(),
		"updatedAt":     b.UpdatedAt.UnixMilli(),
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./internal/audiobooks/abs/ -run TestBookmarkEnvelope_HasRequiredKeys -v
```

Expected: `--- PASS: TestBookmarkEnvelope_HasRequiredKeys`.

- [ ] **Step 5: Build the whole package to ensure no symbol drift**

```bash
go build ./...
```

Expected: no output (clean build).

- [ ] **Step 6: Commit**

```bash
git add internal/audiobooks/abs/bookmarks.go internal/audiobooks/abs/bookmarks_envelope_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): add BookmarkStore interface + ABS envelope helper

Defines the storage contract and wire-shape serialiser the bookmarks
handlers will consume. Envelope test asserts the six required keys
(id, libraryItemId, time, title, createdAt, updatedAt) and the
JS-epoch-millis timestamp shape ABS Android pattern-matches on.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: In-memory fake `memBookmarkStore`, dispatch helper, `handleUpsertBookmark` (create path)

**Files:**
- Modify: `internal/audiobooks/abs/handler.go:163` (add `BookmarkStore` field to `Dependencies`)
- Create: `internal/audiobooks/abs/bookmarks_handler.go`
- Create: `internal/audiobooks/abs/bookmarks_handler_test.go`

- [ ] **Step 1: Add the field to `Dependencies` so the type compiles**

Edit `internal/audiobooks/abs/handler.go` in the `Dependencies` struct (around line 163). Add this field after `PlaybackSessionStore`:

```go
	// BookmarkStore persists ABS bookmark rows (migration 148) for the
	// POST/PATCH/DELETE /me/item/{itemId}/bookmark endpoints. May be
	// nil; handlers respond 503 when unset.
	BookmarkStore BookmarkStore
```

Verify the package still builds:

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 2: Write the failing test (and supporting fakes)**

Create `internal/audiobooks/abs/bookmarks_handler_test.go`:

```go
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
```

- [ ] **Step 3: Run the test to verify it fails (compile error)**

```bash
go test ./internal/audiobooks/abs/ -run TestCreate_NewBookmark_ReturnsListContainingIt -v
```

Expected: build failure with "h.handleUpsertBookmark undefined".

- [ ] **Step 4: Implement `handleUpsertBookmark`**

Create `internal/audiobooks/abs/bookmarks_handler.go`:

```go
package abs

import (
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// bookmarkBody is the JSON body for POST and PATCH
// /me/item/{itemId}/bookmark. Time is a pointer so we can distinguish
// missing (→ 400) from the literal 0.0.
type bookmarkBody struct {
	Title string   `json:"title"`
	Time  *float64 `json:"time"`
}

// handleUpsertBookmark backs both POST (reason="bookmark_created") and
// PATCH (reason="bookmark_updated") /me/item/{itemId}/bookmark. Both
// share the exact same upsert semantics — only the realtime event
// reason differs.
func (h *Handler) handleUpsertBookmark(reason string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ok := absAuthFrom(r)
		if !ok || a.UserID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if h.deps.BookmarkStore == nil {
			http.Error(w, "bookmark store unavailable", http.StatusServiceUnavailable)
			return
		}

		itemID := chi.URLParam(r, "itemId")
		if itemID == "" {
			http.Error(w, "itemId required", http.StatusBadRequest)
			return
		}

		// 1 MiB body cap — matches handleStandaloneLogin.
		var body bookmarkBody
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		if err := dec.Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if body.Time == nil || math.IsNaN(*body.Time) {
			http.Error(w, "time required", http.StatusBadRequest)
			return
		}

		// Item validation: avoid orphan bookmark rows whose item no
		// longer exists. Skipped on DELETE (see handleDeleteBookmark).
		item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), itemID)
		if err != nil || item == nil {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}

		bm, err := h.deps.BookmarkStore.Upsert(r.Context(), a.UserID, a.ProfileID, itemID, *body.Time, body.Title)
		if err != nil {
			slog.Error("abs bookmark upsert failed", "err", err, "user", a.UserID, "item", itemID)
			http.Error(w, "bookmark persist failed", http.StatusInternalServerError)
			return
		}

		h.publish(a.UserID, "user_updated", map[string]any{
			"reason":   reason,
			"bookmark": bookmarkToABS(bm),
		})

		writeBookmarkList(w, r, h, a.UserID, a.ProfileID, itemID)
	}
}

// writeBookmarkList re-fetches the item's bookmarks and writes them as
// the JSON response. On list-fetch failure after a successful mutation,
// degrade to 200 + empty list + slog.Warn (the mutation already
// committed; failing the response would mis-report the state).
func writeBookmarkList(w http.ResponseWriter, r *http.Request, h *Handler, userID, profileID, itemID string) {
	rows, err := h.deps.BookmarkStore.List(r.Context(), userID, profileID, itemID)
	if err != nil {
		slog.Warn("abs bookmark list after mutation failed", "err", err, "user", userID, "item", itemID)
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, b := range rows {
		out = append(out, bookmarkToABS(b))
	}
	writeJSON(w, http.StatusOK, out)
}

// parseBookmarkTime parses the {time} URL parameter on DELETE
// /me/item/{itemId}/bookmark/{time}. Returns (0, false) on parse
// failure.
func parseBookmarkTime(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) {
		return 0, false
	}
	return v, true
}
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./internal/audiobooks/abs/ -run TestCreate_NewBookmark_ReturnsListContainingIt -v
```

Expected: `--- PASS: TestCreate_NewBookmark_ReturnsListContainingIt`.

- [ ] **Step 6: Run the whole package to confirm no other tests regressed**

```bash
go test ./internal/audiobooks/abs/ -v
```

Expected: all existing tests still pass.

- [ ] **Step 7: Commit**

```bash
git add internal/audiobooks/abs/handler.go internal/audiobooks/abs/bookmarks_handler.go internal/audiobooks/abs/bookmarks_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): POST /me/item/{id}/bookmark — ABS bookmark create

First of three ABS bookmark endpoints. Body { title, time } upserts on
(user, profile, item, time); response is the item's full bookmark
list. Backed by a new BookmarkStore dependency (nil-safe: handler
returns 503 when unwired). Item validation via MediaStore;
realtime user_updated event with reason=bookmark_created on success.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: PATCH path — `handleUpsertBookmark("bookmark_updated")`

**Files:**
- Modify: `internal/audiobooks/abs/bookmarks_handler_test.go` (add test)

The PATCH handler is the same function as POST, just constructed with a different `reason`. This task is "add the test that drives both methods" so the contract is documented.

- [ ] **Step 1: Write the failing test**

Append to `bookmarks_handler_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it passes**

```bash
go test ./internal/audiobooks/abs/ -run TestUpsert_SameTime_UpdatesTitleNoDuplicate -v
```

Expected: PASS (the create-path implementation already supports update).

- [ ] **Step 3: Commit**

```bash
git add internal/audiobooks/abs/bookmarks_handler_test.go
git commit -m "$(cat <<'EOF'
test(audiobooks): cover PATCH /me/item/{id}/bookmark upsert semantics

Drives the same handleUpsertBookmark with reason="bookmark_updated",
asserts the (user, profile, item, time) tuple is unique (PATCH updates
title in place, never duplicates) and that the ULID is preserved
across upsert.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: DELETE handler — `handleDeleteBookmark`

**Files:**
- Modify: `internal/audiobooks/abs/bookmarks_handler.go` (add handler)
- Modify: `internal/audiobooks/abs/bookmarks_handler_test.go` (add tests)

- [ ] **Step 1: Write the failing tests**

Append to `bookmarks_handler_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail (compile error)**

```bash
go test ./internal/audiobooks/abs/ -run 'TestDelete_' -v
```

Expected: build failure with "h.handleDeleteBookmark undefined".

- [ ] **Step 3: Implement `handleDeleteBookmark`**

Append to `internal/audiobooks/abs/bookmarks_handler.go`:

```go
// handleDeleteBookmark — DELETE /me/item/{itemId}/bookmark/{time}.
//
// Idempotent: returns 200 with the caller's current bookmark list,
// whether or not the (item, time) row existed. Crucially, this means
// a DELETE against another user's bookmark returns the caller's own
// (empty-or-other) list — no enumeration vector.
//
// Item validation is intentionally skipped: a bookmark whose item was
// just deleted should still be removable. (Upsert keeps validation
// because it would create a new orphan row.)
func (h *Handler) handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.BookmarkStore == nil {
		http.Error(w, "bookmark store unavailable", http.StatusServiceUnavailable)
		return
	}

	itemID := chi.URLParam(r, "itemId")
	if itemID == "" {
		http.Error(w, "itemId required", http.StatusBadRequest)
		return
	}
	t, ok := parseBookmarkTime(chi.URLParam(r, "time"))
	if !ok {
		http.Error(w, "time required", http.StatusBadRequest)
		return
	}

	// Snapshot the pre-delete row so the realtime payload carries the
	// title that just got removed (clients prefer this over a bare ID).
	var pre Bookmark
	if rows, err := h.deps.BookmarkStore.List(r.Context(), a.UserID, a.ProfileID, itemID); err == nil {
		for _, b := range rows {
			if b.Time == t {
				pre = b
				break
			}
		}
	}

	if err := h.deps.BookmarkStore.Delete(r.Context(), a.UserID, a.ProfileID, itemID, t); err != nil {
		slog.Error("abs bookmark delete failed", "err", err, "user", a.UserID, "item", itemID)
		http.Error(w, "bookmark delete failed", http.StatusInternalServerError)
		return
	}

	// Only publish when the row actually existed (pre.ID is empty
	// otherwise). Avoids notifying other devices about a phantom delete.
	if pre.ID != "" {
		h.publish(a.UserID, "user_updated", map[string]any{
			"reason":   "bookmark_deleted",
			"bookmark": bookmarkToABS(pre),
		})
	}

	writeBookmarkList(w, r, h, a.UserID, a.ProfileID, itemID)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestDelete_' -v
```

Expected: both `TestDelete_ExistingBookmark_RemovedFromList` and `TestDelete_NonExistentTime_IdempotentReturnsEmptyList` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/bookmarks_handler.go internal/audiobooks/abs/bookmarks_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): DELETE /me/item/{id}/bookmark/{time} — ABS delete

Idempotent: returns 200 with the caller's current bookmark list
regardless of whether the row existed. Skips item validation so
bookmarks remain removable even after the underlying item is deleted.
Realtime user_updated event with reason=bookmark_deleted fires only
when a row actually existed (carries the pre-delete title).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Remaining unit-test coverage

**Files:**
- Modify: `internal/audiobooks/abs/bookmarks_handler_test.go` (add tests)

Adds the remaining spec §8.1 cases: ordering, profile isolation, cross-user no-op, 404 on missing item, 400 on bad bodies.

- [ ] **Step 1: Write the failing tests**

Append to `bookmarks_handler_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests**

```bash
go test ./internal/audiobooks/abs/ -run 'TestCreate_Two|TestProfileIsolation|TestDelete_OtherUser|TestMissingItem|TestInvalidBody|TestMissingTime' -v
```

Expected: all six PASS. If `TestProfileIsolation_BookmarksScopedPerProfile` fails, the `memBookmarkStore.List` prefix check is wrong — verify the prefix includes `userID|profileID|itemID|`.

- [ ] **Step 3: Run the full package suite to catch any drift**

```bash
go test ./internal/audiobooks/abs/ -v
```

Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/audiobooks/abs/bookmarks_handler_test.go
git commit -m "$(cat <<'EOF'
test(audiobooks): cover ABS bookmark edge cases

Adds ordering, per-profile isolation, cross-user no-op (existence
leak guard), missing-item 404, malformed-body 400, and missing-time
400 to the bookmark handler suite.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Socket-event tests

**Files:**
- Modify: `internal/audiobooks/abs/bookmarks_handler_test.go` (add tests)

Asserts the realtime `user_updated` events documented in spec §4: one per
mutation, scoped to the acting user, with the right `reason` and a
populated bookmark payload.

- [ ] **Step 1: Write the failing tests**

Append to `bookmarks_handler_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestSocketEvent_' -v
```

Expected: all three PASS (the handlers already call `h.publish` per Task 3 and Task 5).

- [ ] **Step 3: Commit**

```bash
git add internal/audiobooks/abs/bookmarks_handler_test.go
git commit -m "$(cat <<'EOF'
test(audiobooks): assert realtime user_updated events on bookmark ops

Covers the three reason discriminators (bookmark_created /
bookmark_updated / bookmark_deleted) documented in
docs/superpowers/specs/2026-05-26-abs-bookmarks-design.md §4. Asserts
event count, scope (event userID), event name, and payload shape.
DELETE event uses the pre-delete snapshot so clients keep the title.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Register routes in `mountRoutes`

**Files:**
- Modify: `internal/audiobooks/abs/handler.go` (the Stage 4 `bearerAuth` group around line 298)

- [ ] **Step 1: Add the three routes inside the existing bearerAuth group**

In `internal/audiobooks/abs/handler.go`, locate the Stage 4 group (currently registering `/me/progress*` and `/session/{sid}*`). At the end of the `for _, prefix := range []string{"/abs/api", "/api"} {` loop, before the closing brace, append:

```go
			// Bookmarks — POST/PATCH both upsert; DELETE is idempotent.
			r.Post(prefix+"/me/item/{itemId}/bookmark", h.handleUpsertBookmark("bookmark_created"))
			r.Patch(prefix+"/me/item/{itemId}/bookmark", h.handleUpsertBookmark("bookmark_updated"))
			r.Delete(prefix+"/me/item/{itemId}/bookmark/{time}", h.handleDeleteBookmark)
```

The result should look like (showing only the bottom of the Stage 4 group):

```go
			// POST  /session/{sid}/close     — finalise the play session
			r.Post(prefix+"/session/{sid}/close", h.handleSessionClose)
			// Bookmarks — POST/PATCH both upsert; DELETE is idempotent.
			r.Post(prefix+"/me/item/{itemId}/bookmark", h.handleUpsertBookmark("bookmark_created"))
			r.Patch(prefix+"/me/item/{itemId}/bookmark", h.handleUpsertBookmark("bookmark_updated"))
			r.Delete(prefix+"/me/item/{itemId}/bookmark/{time}", h.handleDeleteBookmark)
		}
	})
```

- [ ] **Step 2: Build the package**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 3: Run the whole audiobooks suite**

```bash
go test ./internal/audiobooks/...
```

Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/audiobooks/abs/handler.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): mount ABS bookmark routes at /abs/api and /api

Registers POST/PATCH/DELETE /me/item/{itemId}/bookmark inside the
existing bearerAuth group so real ABS clients hitting either prefix
resolve to the same handlers.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Concrete `ABSBookmarkStore` (pgx-backed)

**Files:**
- Create: `internal/audiobooks/abs_bookmark_store.go`

- [ ] **Step 1: Implement the store**

Create `internal/audiobooks/abs_bookmark_store.go`:

```go
package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSBookmarkStore implements abs.BookmarkStore against the
// abs_bookmarks table (migration 148). One row per
// (user, profile, item, time) — uniqueness is enforced by the
// abs_bookmarks_user_profile_item_time_uniq index, with the
// COALESCE-to-sentinel-UUID trick collapsing NULL profile_id into a
// single bucket per user.
type ABSBookmarkStore struct {
	Pool *pgxpool.Pool
}

// Compile-time assertion that ABSBookmarkStore satisfies the
// abs.BookmarkStore contract. Catches signature drift at build time.
var _ abs.BookmarkStore = (*ABSBookmarkStore)(nil)

// profileArg returns the value to bind for the profile_id column.
// pgx interprets a (*string)(nil) as SQL NULL, which is exactly what
// the schema wants for primary-profile rows.
func profileArg(profileID string) any {
	if profileID == "" {
		return nil
	}
	return profileID
}

// List returns all bookmarks for (user, profile, item) ordered by
// time_seconds ASC. Empty slice (never nil) when none exist.
func (s *ABSBookmarkStore) List(ctx context.Context, userID, profileID, itemID string) ([]abs.Bookmark, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, library_item_id, time_seconds, title, created_at, updated_at
		FROM abs_bookmarks
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		  AND library_item_id = $3
		ORDER BY time_seconds ASC`,
		uid, profileArg(profileID), itemID,
	)
	if err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.Bookmark, 0)
	for rows.Next() {
		var b abs.Bookmark
		if err := rows.Scan(&b.ID, &b.LibraryItemID, &b.Time, &b.Title, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_bookmark_store: list scan: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: list rows: %w", err)
	}
	return out, nil
}

// Upsert inserts a new bookmark or updates the title at the exact
// (user, profile, item, time) tuple. ID is generated on insert and
// preserved on update.
func (s *ABSBookmarkStore) Upsert(ctx context.Context, userID, profileID, itemID string, timeSeconds float64, title string) (abs.Bookmark, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return abs.Bookmark{}, fmt.Errorf("abs_bookmark_store: invalid user id %q: %w", userID, err)
	}
	id := ulid.Make().String()
	var out abs.Bookmark
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO abs_bookmarks
		  (id, user_id, profile_id, library_item_id, time_seconds, title)
		VALUES ($1, $2, $3::uuid, $4, $5, $6)
		ON CONFLICT (
		    user_id,
		    COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid),
		    library_item_id,
		    time_seconds
		) DO UPDATE
		   SET title = EXCLUDED.title,
		       updated_at = now()
		RETURNING id, library_item_id, time_seconds, title, created_at, updated_at`,
		id, uid, profileArg(profileID), itemID, timeSeconds, title,
	)
	if err := row.Scan(&out.ID, &out.LibraryItemID, &out.Time, &out.Title, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return abs.Bookmark{}, fmt.Errorf("abs_bookmark_store: upsert: %w", err)
	}
	return out, nil
}

// Delete removes the bookmark at (user, profile, item, time).
// Returns nil when no row matched — DELETE is idempotent per
// the BookmarkStore contract.
func (s *ABSBookmarkStore) Delete(ctx context.Context, userID, profileID, itemID string, timeSeconds float64) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("abs_bookmark_store: invalid user id %q: %w", userID, err)
	}
	_, err = s.Pool.Exec(ctx, `
		DELETE FROM abs_bookmarks
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		  AND library_item_id = $3
		  AND time_seconds = $4`,
		uid, profileArg(profileID), itemID, timeSeconds,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("abs_bookmark_store: delete: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: clean build. If the compile-time assertion fires, the interface and concrete signatures have drifted — re-check `internal/audiobooks/abs/bookmarks.go` and the methods on `ABSBookmarkStore`.

- [ ] **Step 3: Commit**

```bash
git add internal/audiobooks/abs_bookmark_store.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): ABSBookmarkStore — pgx-backed concrete store

Implements abs.BookmarkStore against abs_bookmarks (migration 148).
COALESCE-to-sentinel-UUID matches the table's unique index for
profile NULL collapsing. Upsert is one round-trip via INSERT ... ON
CONFLICT ... DO UPDATE RETURNING.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Wire `ABSBookmarkStore` into `BuildABSHandler`

**Files:**
- Modify: `internal/audiobooks/service.go:90-130` (the `BuildABSHandler` function)

- [ ] **Step 1: Construct the store and pass it through**

In `internal/audiobooks/service.go`, locate the block that constructs `playbackSessionStore` (around line 90). Below that block, before the `configProvider` block, insert:

```go
	var bookmarkStore abs.BookmarkStore
	if deps.Pool != nil {
		bookmarkStore = &ABSBookmarkStore{Pool: deps.Pool}
	}
```

Then in the `abs.New(abs.Dependencies{...})` call (around line 121), add the field after `PlaybackSessionStore`:

```go
		PlaybackSessionStore: playbackSessionStore,
		BookmarkStore:        bookmarkStore,
```

The diff is just two added lines for construction and one added field on the Dependencies literal.

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 3: Run the audiobooks suite one more time**

```bash
go test ./internal/audiobooks/...
```

Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/audiobooks/service.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): wire ABSBookmarkStore into BuildABSHandler

When deps.Pool is non-nil, construct the pgx-backed store and pass
it through to the ABS handler. Mirrors the other store wirings in
BuildABSHandler; when no pool is available (tests, minimal fixtures),
BookmarkStore stays nil and the handlers respond 503.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Full verification

This is a single-task, multi-step check the engineer runs before considering the work done. No code changes unless something fails.

- [ ] **Step 1: Run the full Go test suite**

```bash
go test ./...
```

Expected: all packages PASS. If anything outside `internal/audiobooks/` regresses, fix it before continuing — bookmark code should be independent.

- [ ] **Step 2: Lint**

```bash
make lint
```

Expected: clean.

- [ ] **Step 3: Frontend lint + format (no frontend changes, but the merge gate requires both)**

```bash
cd web && pnpm run lint && pnpm run format:check
cd ..
```

Expected: clean.

- [ ] **Step 4: Local-paths guard**

```bash
make verify-local-paths
```

Expected: clean.

- [ ] **Step 5: Frontend build (catches what `tsc --noEmit` misses; see CLAUDE.md memory)**

```bash
cd web && pnpm run build
cd ..
```

Expected: clean build.

- [ ] **Step 6: Apply the migration on a clean local DB and roll back**

```bash
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_bookmarks"
```

Expected: the table schema printed, matching the migration. Roll back to confirm the down works:

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/148_abs_bookmarks.down.sql
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_bookmarks"
```

Expected: "Did not find any relation named ..." or equivalent. Then re-apply:

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/148_abs_bookmarks.up.sql
```

- [ ] **Step 7: Live integration smoke (spec §8.3)**

Boot the server in integrated mode, then run the smoke from the spec. Substitute your local `<u>`, `<p>`, and `<lid>` values.

```bash
make build && ./silo --mode integrated &
SILO_PID=$!
sleep 3

TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' -H 'x-return-tokens: true' \
  -d '{"username":"<u>","password":"<p>"}' http://127.0.0.1:13378/login \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['accessToken'])")

ITEM=$(curl -s -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:13378/api/libraries/<lid>/items?limit=1 \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['results'][0]['id'])")

echo "--- POST create ---"
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"smoke","time":42.5}' \
  http://127.0.0.1:13378/api/me/item/$ITEM/bookmark | python3 -m json.tool

echo "--- PATCH update ---"
curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"updated","time":42.5}' \
  http://127.0.0.1:13378/api/me/item/$ITEM/bookmark | python3 -m json.tool

echo "--- DELETE ---"
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:13378/api/me/item/$ITEM/bookmark/42.5 | python3 -m json.tool

kill $SILO_PID
```

Expected:
- POST → `[{"id":"...","libraryItemId":"<ITEM>","time":42.5,"title":"smoke",...}]`
- PATCH → `[{...,"title":"updated",...}]` with the SAME `id` as POST.
- DELETE → `[]`.

If the PATCH returns a different `id`, the unique-index `COALESCE` clause is wrong — re-check migration 148.

- [ ] **Step 8: No commit unless something needed fixing**

If steps 1-7 all passed cleanly, the branch is ready. If a fix was needed, commit it with a descriptive `fix(audiobooks): ...` message.

---

## Out of scope (deferred per spec §10)

- Aggregate `GET /api/me/bookmarks` endpoint.
- Hydrating `user.bookmarks` in the `/login` envelope.
- Embedding `bookmarks` on `GET /api/items/{id}`.

These belong to later Phase 1 sub-projects.
