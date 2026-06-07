# ABS Collections + Playlists Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land manual user collections (named groupings of audiobooks with name + description) and ordered playlists (named queues with cover image + episode-id-aware items) on the silo audiobook surface, so ABS Android/iOS/Plappa clients can create, browse, mutate, and share them.

**Architecture:** Four migrations (149-152), two REST surfaces sharing a uniform CRUD shape, two store interfaces with pgx-backed concrete implementations parallel to `abs_bookmark_store.go`. Both surfaces are profile-scoped with cross-user-public read semantics; both use the same anti-enumeration 404 pattern landed in sub-project 1 (bookmarks). Collections fire no socket events; playlists fire `playlist_added`/`_updated`/`_removed` (continuum-canonical event names).

**Tech Stack:** Go 1.x, `chi/v5` router, `pgx/v5`, `oklog/ulid/v2`, internal `package abs` tests with in-memory fakes.

**Commands assume the repository root (`/opt/silo-server`) is the cwd.**

**Source spec:** `docs/superpowers/specs/2026-05-26-abs-collections-playlists-design.md`. Re-read sections 4 (endpoint table), 5 (data model + Go structs), 6 (storage contract), 7 (error model), and 8 (tests) before each task — the plan implements the spec, it does not re-justify decisions already made there.

**Predecessor plan:** `docs/superpowers/plans/2026-05-26-abs-bookmarks-implementation.md`. The conventions used here (TDD ordering, in-memory fakes shape, dispatch helpers, error-handling phrasing, commit message style) all mirror the bookmark plan that just shipped.

---

## File map

**Create:**
- `migrations/149_abs_user_collections.up.sql` + `.down.sql`
- `migrations/150_abs_collection_items.up.sql` + `.down.sql`
- `migrations/151_abs_playlists.up.sql` + `.down.sql`
- `migrations/152_abs_playlist_items.up.sql` + `.down.sql`
- `internal/audiobooks/abs/collections.go` — `Collection`, `CollectionItem`, `CollectionStore` interface, `collectionToABS`/`collectionItemToABS` serialisers.
- `internal/audiobooks/abs/collections_handler.go` — 7 handlers (list/create/get/update/delete + add-book/remove-book).
- `internal/audiobooks/abs/collections_handler_test.go` — `memCollectionStore` in-memory fake + handler tests.
- `internal/audiobooks/abs/collections_envelope_test.go` — wire-shape test.
- `internal/audiobooks/abs/playlists.go` — `Playlist`, `PlaylistItem`, `PlaylistStore` interface, `playlistToABS`/`playlistItemToABS` serialisers.
- `internal/audiobooks/abs/playlists_handler.go` — 10 handlers (list/create/get/update/delete + add-item single + batch-add + batch-remove + remove-item + remove-episode).
- `internal/audiobooks/abs/playlists_handler_test.go` — `memPlaylistStore` in-memory fake + handler tests.
- `internal/audiobooks/abs/playlists_envelope_test.go` — wire-shape test.
- `internal/audiobooks/abs_collection_store.go` — pgx-backed `CollectionStore`.
- `internal/audiobooks/abs_playlist_store.go` — pgx-backed `PlaylistStore`.

**Modify:**
- `internal/audiobooks/abs/handler.go` — add `CollectionStore` and `PlaylistStore` fields to `Dependencies`; register the 17 new routes in `mountRoutes`.
- `internal/audiobooks/service.go` — construct both stores in `BuildABSHandler` and pass them through.

---

## Task 1: Collections migrations (149 + 150)

**Files:**
- Create: `migrations/149_abs_user_collections.up.sql`
- Create: `migrations/149_abs_user_collections.down.sql`
- Create: `migrations/150_abs_collection_items.up.sql`
- Create: `migrations/150_abs_collection_items.down.sql`

- [ ] **Step 1: Write migration 149 up**

`migrations/149_abs_user_collections.up.sql`:

```sql
-- Manual user collections (named groupings of audiobooks).
-- Profile-scoped: NULL profile_id encodes the primary profile, and the
-- COALESCE-to-sentinel-UUID trick in the lookup index collapses NULL
-- to a single bucket per user (raw NULL is treated as distinct for
-- index purposes otherwise).
--
-- is_public allows other users on the same silo instance to GET-by-id
-- (the list endpoint never exposes other users' collections; only the
-- detail route honors is_public).

CREATE TABLE IF NOT EXISTS public.abs_user_collections (
    id          text PRIMARY KEY,
    user_id     integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id  uuid,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    is_public   boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS abs_user_collections_user_profile_idx
    ON public.abs_user_collections (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
    );
```

- [ ] **Step 2: Write migration 149 down**

`migrations/149_abs_user_collections.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_user_collections_user_profile_idx;
DROP TABLE IF EXISTS public.abs_user_collections;
```

- [ ] **Step 3: Write migration 150 up**

`migrations/150_abs_collection_items.up.sql`:

```sql
-- Items inside an abs_user_collections row. Composite PK rules out
-- duplicates. Both FKs cascade so a deleted collection or a deleted
-- media item silently drops the membership row.

CREATE TABLE IF NOT EXISTS public.abs_collection_items (
    collection_id   text NOT NULL REFERENCES public.abs_user_collections(id) ON DELETE CASCADE,
    library_item_id text NOT NULL REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    added_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collection_id, library_item_id)
);

CREATE INDEX IF NOT EXISTS abs_collection_items_library_item_idx
    ON public.abs_collection_items (library_item_id);
```

- [ ] **Step 4: Write migration 150 down**

`migrations/150_abs_collection_items.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_collection_items_library_item_idx;
DROP TABLE IF EXISTS public.abs_collection_items;
```

- [ ] **Step 5: Apply locally and verify**

Postgres must be running (`docker compose ps postgres` should show it up). Apply both up migrations:

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/149_abs_user_collections.up.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/150_abs_collection_items.up.sql
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_user_collections"
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_collection_items"
```

Expected: both `\d` outputs show the column lists and FKs as in the up migrations. No errors.

Verify down migrations parse and re-up cleanly:

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/150_abs_collection_items.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/149_abs_user_collections.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/149_abs_user_collections.up.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/150_abs_collection_items.up.sql
```

Expected: clean output throughout.

- [ ] **Step 6: Commit**

IMPORTANT: There are pre-existing unrelated modifications in the working tree (`Dockerfile`, `cmd/silo/main.go`, `docker-compose.yml`, `internal/api/router.go`, `internal/audiobooks/abs/me_handler.go`, `internal/audiobooks/abs/progress.go`, `internal/audiobooks/media_store.go`, `internal/auth/session.go`, `internal/config/config.go`, `internal/config/db_loader.go`) and untracked files. DO NOT stage them. Stage only the four new migration files.

```bash
git add migrations/149_abs_user_collections.up.sql migrations/149_abs_user_collections.down.sql \
        migrations/150_abs_collection_items.up.sql migrations/150_abs_collection_items.down.sql
git commit -m "$(cat <<'EOF'
feat(audiobooks): add abs_user_collections + abs_collection_items migrations

Migrations 149 + 150 back the upcoming ABS collection endpoints.
Schema rationale documented in
docs/superpowers/specs/2026-05-26-abs-collections-playlists-design.md
§5.1, §5.2, §5.5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Collection types + `CollectionStore` interface + envelope test

**Files:**
- Create: `internal/audiobooks/abs/collections.go`
- Create: `internal/audiobooks/abs/collections_envelope_test.go`

- [ ] **Step 1: Write the failing envelope test**

Create `internal/audiobooks/abs/collections_envelope_test.go`:

```go
package abs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestCollectionEnvelope_HasRequiredKeys asserts the seven top-level
// keys ABS Android pattern-matches on are present even when description
// is empty and books[] is empty. Fixes the continuum-reference bug where
// description always emitted as "" regardless of stored value.
func TestCollectionEnvelope_HasRequiredKeys(t *testing.T) {
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	out := collectionToABS(Collection{
		ID:          "01HCOLL",
		UserID:      "1",
		Name:        "Favorites",
		Description: "",
		IsPublic:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, []map[string]any{})
	body, _ := json.Marshal(out)
	js := string(body)
	for _, key := range []string{
		`"id":`, `"userId":`, `"name":`, `"description":`,
		`"isPublic":`, `"lastUpdate":`, `"createdAt":`, `"books":`,
	} {
		if !strings.Contains(js, key) {
			t.Errorf("envelope missing %s; got %s", key, js)
		}
	}
	if out["description"] != "" {
		t.Errorf("description = %v, want empty string", out["description"])
	}
	wantMs := now.UnixMilli()
	if out["createdAt"] != wantMs {
		t.Errorf("createdAt = %v, want %d", out["createdAt"], wantMs)
	}
}

// TestCollectionListShape_OmitsBooks asserts the list shape (passed
// nil books) emits no "books" key — clients distinguish list vs detail
// by presence/absence of this field.
func TestCollectionListShape_OmitsBooks(t *testing.T) {
	out := collectionToABS(Collection{
		ID: "01HCOLL", UserID: "1", Name: "x",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, nil)
	if _, ok := out["books"]; ok {
		t.Errorf("list-shape must not include books key; got %v", out)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/audiobooks/abs/ -run TestCollectionEnvelope -v
```

Expected: compile failure with `undefined: collectionToABS` / `undefined: Collection`.

- [ ] **Step 3: Create the type, interface, and envelope helper**

Create `internal/audiobooks/abs/collections.go`:

```go
package abs

import (
	"context"
	"time"
)

// CollectionStore is the narrow slice of the abs_user_collections +
// abs_collection_items tables the collections handlers need.
// Implemented by ABSCollectionStore in
// internal/audiobooks/abs_collection_store.go.
type CollectionStore interface {
	// ListUserCollections returns collections owned by (userID, profileID),
	// ordered by created_at DESC. Empty slice (never nil) when none.
	ListUserCollections(ctx context.Context, userID, profileID string) ([]Collection, error)
	// GetCollection fetches by ID without owner check (caller authorizes).
	// Returns ErrNotFound when absent.
	GetCollection(ctx context.Context, id string) (Collection, error)
	// CreateCollection inserts. ID must be set by caller (ULID).
	CreateCollection(ctx context.Context, c Collection) error
	// UpdateCollection writes name, description, is_public; bumps
	// updated_at = now(). Owner check is the caller's responsibility.
	UpdateCollection(ctx context.Context, c Collection) error
	// DeleteCollection removes the collection and (via FK CASCADE) all
	// its abs_collection_items. Returns nil even if no row matched.
	DeleteCollection(ctx context.Context, id string) error
	// ListCollectionItems returns items ordered by added_at ASC.
	// Empty slice (never nil) when none.
	ListCollectionItems(ctx context.Context, collectionID string) ([]CollectionItem, error)
	// AddCollectionItem inserts (collectionID, libraryItemID) and bumps
	// the parent's updated_at. ON CONFLICT DO NOTHING — re-adding is a
	// silent no-op.
	AddCollectionItem(ctx context.Context, collectionID, libraryItemID string) error
	// RemoveCollectionItem deletes one row and bumps the parent's
	// updated_at. Returns nil when not present (idempotent).
	RemoveCollectionItem(ctx context.Context, collectionID, libraryItemID string) error
}

// Collection is the in-memory representation of an
// abs_user_collections row.
type Collection struct {
	ID          string
	UserID      string
	ProfileID   string
	Name        string
	Description string
	IsPublic    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CollectionItem is the in-memory representation of an
// abs_collection_items row.
type CollectionItem struct {
	CollectionID  string
	LibraryItemID string
	AddedAt       time.Time
}

// collectionToABS shapes a Collection in the ABS wire format. When
// books is nil the list-shape is emitted (no "books" key); when books
// is non-nil (possibly empty) the full-shape is emitted.
//
// All seven non-books keys are always present (no omitempty),
// camelCase, with timestamps as JS-epoch milliseconds.
func collectionToABS(c Collection, books []map[string]any) map[string]any {
	out := map[string]any{
		"id":          c.ID,
		"userId":      c.UserID,
		"name":        c.Name,
		"description": c.Description,
		"isPublic":    c.IsPublic,
		"lastUpdate":  c.UpdatedAt.UnixMilli(),
		"createdAt":   c.CreatedAt.UnixMilli(),
	}
	if books != nil {
		out["books"] = books
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run TestCollectionEnvelope -v
go test ./internal/audiobooks/abs/ -run TestCollectionListShape -v
```

Expected: both PASS.

- [ ] **Step 5: Build whole package**

```bash
go build ./...
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/audiobooks/abs/collections.go internal/audiobooks/abs/collections_envelope_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): add CollectionStore interface + ABS envelope helper

Defines the storage contract and wire-shape serialiser the collections
handlers will consume. Envelope test asserts the seven required keys
including description (which the continuum reference always emits as
empty regardless of stored value — this round-trips it correctly).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Test harness + `handleCreateCollection` (TDD)

**Files:**
- Modify: `internal/audiobooks/abs/handler.go` (add `CollectionStore` field to `Dependencies`)
- Create: `internal/audiobooks/abs/collections_handler.go`
- Create: `internal/audiobooks/abs/collections_handler_test.go`

- [ ] **Step 1: Add `CollectionStore` field to `Dependencies`**

In `internal/audiobooks/abs/handler.go`, locate the `Dependencies` struct. Add after `BookmarkStore` (which was added by the bookmarks sub-project):

```go
	// CollectionStore persists ABS user-collection rows (migrations 149 + 150).
	// May be nil; handlers respond 503 when unset.
	CollectionStore CollectionStore
```

Verify the build:

```bash
go build ./...
```

Expected: clean.

- [ ] **Step 2: Create the failing test + in-memory fake**

Create `internal/audiobooks/abs/collections_handler_test.go`:

```go
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
	rows  map[string]Collection // id -> row
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
```

- [ ] **Step 3: Run the tests to verify they fail (compile error)**

```bash
go test ./internal/audiobooks/abs/ -run TestCollection_Create -v
```

Expected: compile failure with `h.handleCreateCollection undefined`.

- [ ] **Step 4: Implement `handleCreateCollection`**

Create `internal/audiobooks/abs/collections_handler.go`:

```go
package abs

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

// collectionBody is the JSON body for POST and PATCH /collections[/{id}].
// All fields are optional on PATCH; name is required on POST (checked
// in the handler, not via tag-driven validation).
type collectionBody struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	IsPublic    *bool   `json:"isPublic"`
}

// handleCreateCollection — POST /collections.
// Body: {name, description?, isPublic?}. Returns the created collection
// in full-shape (with an empty books[] array).
func (h *Handler) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection store unavailable", http.StatusServiceUnavailable)
		return
	}

	var body collectionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == nil || *body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	c := Collection{
		ID:        ulid.Make().String(),
		UserID:    a.UserID,
		ProfileID: a.ProfileID,
		Name:      *body.Name,
	}
	if body.Description != nil {
		c.Description = *body.Description
	}
	if body.IsPublic != nil {
		c.IsPublic = *body.IsPublic
	}
	if err := h.deps.CollectionStore.CreateCollection(r.Context(), c); err != nil {
		slog.Error("abs collection create failed", "err", err, "user", a.UserID)
		http.Error(w, "collection persist failed", http.StatusInternalServerError)
		return
	}

	// Re-fetch to pick up server-set timestamps.
	persisted, err := h.deps.CollectionStore.GetCollection(r.Context(), c.ID)
	if errors.Is(err, ErrNotFound) {
		persisted = c
	} else if err != nil {
		slog.Warn("abs collection get-after-create failed", "err", err, "id", c.ID)
		persisted = c
	}
	writeJSON(w, http.StatusOK, h.collectionFullShape(r, persisted))
}

// collectionFullShape renders a Collection in full-shape, hydrating
// books[] via MediaStore. Errors during hydration degrade to bare
// {id, libraryId} entries so the response always reflects DB truth.
func (h *Handler) collectionFullShape(r *http.Request, c Collection) map[string]any {
	books := h.collectionBooks(r, c.ID)
	return collectionToABS(c, books)
}

// collectionBooks resolves the items in a collection to wire-shape book
// entries, hydrating titles/authors via MediaStore. Returns a non-nil
// slice (possibly empty) so collectionToABS emits the books key.
func (h *Handler) collectionBooks(r *http.Request, collectionID string) []map[string]any {
	if h.deps.CollectionStore == nil {
		return []map[string]any{}
	}
	rows, err := h.deps.CollectionStore.ListCollectionItems(r.Context(), collectionID)
	if err != nil {
		slog.Warn("abs collection list-items failed", "err", err, "collection", collectionID)
		return []map[string]any{}
	}
	lib := h.resolveDefaultLibrary(r.Context())
	libID := audiobookLibraryID(lib)
	out := make([]map[string]any, 0, len(rows))
	for _, it := range rows {
		entry := map[string]any{
			"id":        it.LibraryItemID,
			"libraryId": libID,
		}
		if item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), it.LibraryItemID); err == nil && item != nil {
			entry["media"] = map[string]any{
				"metadata": map[string]any{
					"title": item.Title,
				},
			}
		}
		out = append(out, entry)
	}
	return out
}

// chiURLID is a tiny shim around chi.URLParam(r, "id") so handler call
// sites read uniformly. Inlined where unambiguous.
func chiURLID(r *http.Request) string { return chi.URLParam(r, "id") }
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run TestCollection_Create -v
```

Expected: all three PASS.

- [ ] **Step 6: Run the full package**

```bash
go test ./internal/audiobooks/abs/ -v -count=1 | tail -20
```

Expected: all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/audiobooks/abs/handler.go internal/audiobooks/abs/collections_handler.go internal/audiobooks/abs/collections_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): POST /collections — ABS collection create

First handler of the collections surface. Body {name, description?,
isPublic?} returns the created collection in full-shape (empty
books[]). Backed by the new CollectionStore dependency (nil-safe:
handler returns 503 when unwired). Adds the in-memory test harness
(memCollectionStore + dispatchABSWithParams) that the rest of the
collections suite will reuse.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: `handleListCollections` + list tests

**Files:**
- Modify: `internal/audiobooks/abs/collections_handler.go` (add handler)
- Modify: `internal/audiobooks/abs/collections_handler_test.go` (add tests)

- [ ] **Step 1: Append the failing tests**

Append to `internal/audiobooks/abs/collections_handler_test.go`:

```go

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
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run 'TestCollection_List' -v
```

Expected: compile failure with `h.handleListCollections undefined`.

- [ ] **Step 3: Implement `handleListCollections`**

Append to `internal/audiobooks/abs/collections_handler.go`:

```go

// handleListCollections — GET /collections.
// Returns the caller's collections wrapped in {"collections": [...]}.
// List-shape (no books[]).
func (h *Handler) handleListCollections(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"collections": []any{}})
		return
	}
	rows, err := h.deps.CollectionStore.ListUserCollections(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs collection list failed", "err", err, "user", a.UserID)
		http.Error(w, "collection list failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, collectionToABS(c, nil)) // list-shape: nil books
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": out})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestCollection_List' -v
```

Expected: all three PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/collections_handler.go internal/audiobooks/abs/collections_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): GET /collections — list caller's collections

Wraps the result in {"collections": [...]} matching continuum/real-ABS
clients. Owner-scope only (other users' collections never leaked).
Profile-scoped (collections under a different profile excluded).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: `handleGetCollection` + visibility tests

**Files:**
- Modify: `internal/audiobooks/abs/collections_handler.go`
- Modify: `internal/audiobooks/abs/collections_handler_test.go`

- [ ] **Step 1: Append failing tests**

Append to `collections_handler_test.go`:

```go

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
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run 'TestCollection_Get_' -v
```

Expected: compile failure with `h.handleGetCollection undefined`.

- [ ] **Step 3: Implement `handleGetCollection`**

Append to `collections_handler.go`:

```go

// handleGetCollection — GET /collections/{id}.
// Owner gets full-shape; non-owner gets full-shape only when isPublic.
// Otherwise 404 (no existence leak — indistinguishable from real
// not-found).
func (h *Handler) handleGetCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	c, err := h.deps.CollectionStore.GetCollection(r.Context(), chiURLID(r))
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID && !c.IsPublic) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs collection get failed", "err", err)
		http.Error(w, "collection get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.collectionFullShape(r, c))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestCollection_Get_' -v
```

Expected: all four PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/collections_handler.go internal/audiobooks/abs/collections_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): GET /collections/{id} — owner and public access

Owner sees their own collection in full-shape (with books[]).
Non-owner sees it only when isPublic=true; otherwise 404 with the same
body as a genuine not-found (anti-enumeration pattern from the
bookmarks sub-project).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: `handleUpdateCollection` + `handleDeleteCollection` + tests

**Files:**
- Modify: `internal/audiobooks/abs/collections_handler.go`
- Modify: `internal/audiobooks/abs/collections_handler_test.go`

- [ ] **Step 1: Append failing tests**

Append to `collections_handler_test.go`:

```go

func TestCollection_Patch_OwnerUpdatesNameAndDescription(t *testing.T) {
	hb := newCollectionsHarness(t)
	id := createCollectionForUser(t, hb, "1", "", `{"name":"old","description":"d1"}`)

	body := []byte(`{"name":"new","description":"d2","isPublic":true}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/collections/"+id, map[string]string{"id": id}, body, "1", "", hb.H.handleUpdateCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "new" {
		t.Errorf("name = %v, want 'new'", got["name"])
	}
	if got["description"] != "d2" {
		t.Errorf("description = %v, want 'd2'", got["description"])
	}
	if got["isPublic"] != true {
		t.Errorf("isPublic = %v, want true", got["isPublic"])
	}
}

func TestCollection_Patch_PartialOnlyChangesPresentFields(t *testing.T) {
	hb := newCollectionsHarness(t)
	id := createCollectionForUser(t, hb, "1", "", `{"name":"keep","description":"d1"}`)

	// PATCH only name; description and isPublic must stay.
	body := []byte(`{"name":"renamed"}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/collections/"+id, map[string]string{"id": id}, body, "1", "", hb.H.handleUpdateCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "renamed" {
		t.Errorf("name = %v, want 'renamed'", got["name"])
	}
	if got["description"] != "d1" {
		t.Errorf("description = %v, want 'd1' (unchanged)", got["description"])
	}
}

func TestCollection_Patch_NonOwner_404(t *testing.T) {
	hb := newCollectionsHarness(t)
	id := createCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)

	body := []byte(`{"name":"hijack"}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/collections/"+id, map[string]string{"id": id}, body, "2", "", hb.H.handleUpdateCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no leak); body=%s", rec.Code, rec.Body.String())
	}
	// User 1's collection must be untouched.
	c, _ := hb.Coll.GetCollection(context.Background(), id)
	if c.Name != "mine" {
		t.Errorf("collection name = %q, want 'mine'; non-owner mutation leaked", c.Name)
	}
}

func TestCollection_Delete_Owner_204(t *testing.T) {
	hb := newCollectionsHarness(t)
	id := createCollectionForUser(t, hb, "1", "", `{"name":"x"}`)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/collections/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleDeleteCollection)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	// Subsequent GET must 404.
	rec2 := dispatchABSWithParams(http.MethodGet, "/api/collections/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleGetCollection)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("post-delete GET status = %d, want 404", rec2.Code)
	}
}

func TestCollection_Delete_NonOwner_404(t *testing.T) {
	hb := newCollectionsHarness(t)
	id := createCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/collections/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleDeleteCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	// User 1's collection still exists.
	if _, err := hb.Coll.GetCollection(context.Background(), id); err != nil {
		t.Errorf("collection wrongly deleted: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run 'TestCollection_Patch|TestCollection_Delete' -v
```

Expected: compile failure with `h.handleUpdateCollection` / `h.handleDeleteCollection` undefined.

- [ ] **Step 3: Implement both handlers**

Append to `collections_handler.go`:

```go

// handleUpdateCollection — PATCH /collections/{id}.
// Owner-only. Partial body: only fields explicitly present are
// modified. Non-owner gets 404 (no leak).
func (h *Handler) handleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs collection get-for-update failed", "err", err, "id", id)
		http.Error(w, "collection get failed", http.StatusInternalServerError)
		return
	}

	var body collectionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name != nil {
		c.Name = *body.Name
	}
	if body.Description != nil {
		c.Description = *body.Description
	}
	if body.IsPublic != nil {
		c.IsPublic = *body.IsPublic
	}
	if err := h.deps.CollectionStore.UpdateCollection(r.Context(), c); err != nil {
		slog.Error("abs collection update failed", "err", err, "id", id)
		http.Error(w, "collection persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if err != nil {
		slog.Warn("abs collection get-after-update failed", "err", err, "id", id)
		persisted = c
	}
	writeJSON(w, http.StatusOK, h.collectionFullShape(r, persisted))
}

// handleDeleteCollection — DELETE /collections/{id}.
// Owner-only. Cascade drops abs_collection_items via FK CASCADE.
// 204 on success; 404 for unknown or non-owned.
func (h *Handler) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs collection get-for-delete failed", "err", err, "id", id)
		http.Error(w, "collection get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.CollectionStore.DeleteCollection(r.Context(), id); err != nil {
		slog.Error("abs collection delete failed", "err", err, "id", id)
		http.Error(w, "collection delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestCollection_Patch|TestCollection_Delete' -v
```

Expected: all five PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/collections_handler.go internal/audiobooks/abs/collections_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): PATCH + DELETE /collections/{id}

Owner-gated mutation with partial-body PATCH semantics (only fields
present in the body are updated). Non-owner attempts return 404
matching the bookmarks anti-enumeration pattern. DELETE cascades to
abs_collection_items via FK.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: `handleAddCollectionBook` + `handleRemoveCollectionBook` + tests

**Files:**
- Modify: `internal/audiobooks/abs/collections_handler.go`
- Modify: `internal/audiobooks/abs/collections_handler_test.go`

- [ ] **Step 1: Append failing tests**

Append to `collections_handler_test.go`:

```go

func TestCollection_AddBook_Owner_HydratesInResponse(t *testing.T) {
	hb := newCollectionsHarness(t, "book-1")
	id := createCollectionForUser(t, hb, "1", "", `{"name":"x"}`)

	rec := dispatchABSWithParams(http.MethodPost, "/api/collections/"+id+"/book/book-1",
		map[string]string{"id": id, "bookId": "book-1"}, nil, "1", "", hb.H.handleAddCollectionBook)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	books, _ := got["books"].([]any)
	if len(books) != 1 {
		t.Fatalf("books len = %d, want 1", len(books))
	}
	entry := books[0].(map[string]any)
	if entry["id"] != "book-1" {
		t.Errorf("book entry id = %v, want book-1", entry["id"])
	}
	if _, has := entry["media"]; !has {
		t.Errorf("book entry missing media hydration: %v", entry)
	}
}

func TestCollection_AddBook_Idempotent(t *testing.T) {
	hb := newCollectionsHarness(t, "book-1")
	id := createCollectionForUser(t, hb, "1", "", `{"name":"x"}`)

	_ = dispatchABSWithParams(http.MethodPost, "/api/collections/"+id+"/book/book-1",
		map[string]string{"id": id, "bookId": "book-1"}, nil, "1", "", hb.H.handleAddCollectionBook)
	rec := dispatchABSWithParams(http.MethodPost, "/api/collections/"+id+"/book/book-1",
		map[string]string{"id": id, "bookId": "book-1"}, nil, "1", "", hb.H.handleAddCollectionBook)
	if rec.Code != http.StatusOK {
		t.Fatalf("second add status = %d, want 200 (idempotent); body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	books, _ := got["books"].([]any)
	if len(books) != 1 {
		t.Errorf("books len after double-add = %d, want 1", len(books))
	}
}

func TestCollection_AddBook_UnknownItem_404(t *testing.T) {
	hb := newCollectionsHarness(t /* no known items */)
	id := createCollectionForUser(t, hb, "1", "", `{"name":"x"}`)

	rec := dispatchABSWithParams(http.MethodPost, "/api/collections/"+id+"/book/ghost",
		map[string]string{"id": id, "bookId": "ghost"}, nil, "1", "", hb.H.handleAddCollectionBook)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (item not found); body=%s", rec.Code, rec.Body.String())
	}
}

func TestCollection_AddBook_NonOwner_404(t *testing.T) {
	hb := newCollectionsHarness(t, "book-1")
	id := createCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)

	rec := dispatchABSWithParams(http.MethodPost, "/api/collections/"+id+"/book/book-1",
		map[string]string{"id": id, "bookId": "book-1"}, nil, "2", "", hb.H.handleAddCollectionBook)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no leak); body=%s", rec.Code, rec.Body.String())
	}
}

func TestCollection_RemoveBook_Idempotent(t *testing.T) {
	hb := newCollectionsHarness(t, "book-1")
	id := createCollectionForUser(t, hb, "1", "", `{"name":"x"}`)

	// Remove book that was never added — should be 200 with empty books.
	rec := dispatchABSWithParams(http.MethodDelete, "/api/collections/"+id+"/book/book-1",
		map[string]string{"id": id, "bookId": "book-1"}, nil, "1", "", hb.H.handleRemoveCollectionBook)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	books, _ := got["books"].([]any)
	if len(books) != 0 {
		t.Errorf("books len = %d, want 0", len(books))
	}
}

func TestCollection_RemoveBook_NonOwner_404(t *testing.T) {
	hb := newCollectionsHarness(t, "book-1")
	id := createCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/collections/"+id+"/book/book-1",
		map[string]string{"id": id, "bookId": "book-1"}, nil, "1", "", hb.H.handleAddCollectionBook)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/collections/"+id+"/book/book-1",
		map[string]string{"id": id, "bookId": "book-1"}, nil, "2", "", hb.H.handleRemoveCollectionBook)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	// User 1's items must be intact.
	items, _ := hb.Coll.ListCollectionItems(context.Background(), id)
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1 (non-owner remove leaked)", len(items))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run 'TestCollection_AddBook|TestCollection_RemoveBook' -v
```

Expected: compile failure.

- [ ] **Step 3: Implement both handlers**

Append to `collections_handler.go`:

```go

// handleAddCollectionBook — POST /collections/{id}/book/{bookId}.
// Owner-gated. Validates the item exists via MediaStore (returns 404
// for unknown items). Idempotent: re-adding is a silent no-op.
// Returns the parent collection's full-shape with updated books[].
func (h *Handler) handleAddCollectionBook(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	bookID := chi.URLParam(r, "bookId")
	if bookID == "" {
		http.Error(w, "bookId required", http.StatusBadRequest)
		return
	}

	c, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs collection get-for-add failed", "err", err, "id", id)
		http.Error(w, "collection get failed", http.StatusInternalServerError)
		return
	}

	// Item validation — avoid orphan refs.
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), bookID)
	if err != nil || item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	if err := h.deps.CollectionStore.AddCollectionItem(r.Context(), id, bookID); err != nil {
		slog.Error("abs collection add-item failed", "err", err, "id", id, "book", bookID)
		http.Error(w, "collection persist failed", http.StatusInternalServerError)
		return
	}

	// Re-fetch to surface updated_at bump.
	persisted, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if err != nil {
		persisted = c
	}
	writeJSON(w, http.StatusOK, h.collectionFullShape(r, persisted))
}

// handleRemoveCollectionBook — DELETE /collections/{id}/book/{bookId}.
// Owner-gated. Idempotent: removing a non-member is a no-op.
// Returns the parent collection's full-shape with updated books[].
func (h *Handler) handleRemoveCollectionBook(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.CollectionStore == nil {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	bookID := chi.URLParam(r, "bookId")
	if bookID == "" {
		http.Error(w, "bookId required", http.StatusBadRequest)
		return
	}

	c, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs collection get-for-remove failed", "err", err, "id", id)
		http.Error(w, "collection get failed", http.StatusInternalServerError)
		return
	}

	if err := h.deps.CollectionStore.RemoveCollectionItem(r.Context(), id, bookID); err != nil {
		slog.Error("abs collection remove-item failed", "err", err, "id", id, "book", bookID)
		http.Error(w, "collection delete failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.CollectionStore.GetCollection(r.Context(), id)
	if err != nil {
		persisted = c
	}
	writeJSON(w, http.StatusOK, h.collectionFullShape(r, persisted))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestCollection_' -v -count=1 | tail -30
```

Expected: all collection tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/collections_handler.go internal/audiobooks/abs/collections_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): add/remove collection items

POST /collections/{id}/book/{bookId} validates the item against
MediaStore (404 on unknown) and is idempotent on the store side. The
DELETE variant is unconditional idempotent (returns the current
membership state regardless of whether the row existed). Both 404
when non-owner.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: `ABSCollectionStore` (pgx-backed) + wiring + route registration

**Files:**
- Create: `internal/audiobooks/abs_collection_store.go`
- Modify: `internal/audiobooks/service.go` (`BuildABSHandler`)
- Modify: `internal/audiobooks/abs/handler.go` (`mountRoutes`)

- [ ] **Step 1: Implement the concrete store**

Create `internal/audiobooks/abs_collection_store.go`:

```go
package audiobooks

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSCollectionStore implements abs.CollectionStore against the
// abs_user_collections + abs_collection_items tables (migrations
// 149 + 150). One row per collection in the parent table; one row
// per (collection_id, library_item_id) in the items table.
type ABSCollectionStore struct {
	Pool *pgxpool.Pool
}

// Compile-time assertion.
var _ abs.CollectionStore = (*ABSCollectionStore)(nil)

func (s *ABSCollectionStore) ListUserCollections(ctx context.Context, userID, profileID string) ([]abs.Collection, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_collection_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, description, is_public, created_at, updated_at
		FROM abs_user_collections
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY created_at DESC`,
		uid, profileArg(profileID),
	)
	if err != nil {
		return nil, fmt.Errorf("abs_collection_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.Collection, 0)
	for rows.Next() {
		var c abs.Collection
		var uidScan int
		var profileScan *string
		if err := rows.Scan(&c.ID, &uidScan, &profileScan, &c.Name, &c.Description, &c.IsPublic, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_collection_store: list scan: %w", err)
		}
		c.UserID = strconv.Itoa(uidScan)
		if profileScan != nil {
			c.ProfileID = *profileScan
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *ABSCollectionStore) GetCollection(ctx context.Context, id string) (abs.Collection, error) {
	var c abs.Collection
	var uidScan int
	var profileScan *string
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, description, is_public, created_at, updated_at
		FROM abs_user_collections WHERE id = $1`, id)
	if err := row.Scan(&c.ID, &uidScan, &profileScan, &c.Name, &c.Description, &c.IsPublic, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if err.Error() == "no rows in result set" {
			return abs.Collection{}, abs.ErrNotFound
		}
		return abs.Collection{}, fmt.Errorf("abs_collection_store: get: %w", err)
	}
	c.UserID = strconv.Itoa(uidScan)
	if profileScan != nil {
		c.ProfileID = *profileScan
	}
	return c, nil
}

func (s *ABSCollectionStore) CreateCollection(ctx context.Context, c abs.Collection) error {
	uid, err := strconv.Atoi(c.UserID)
	if err != nil {
		return fmt.Errorf("abs_collection_store: invalid user id %q: %w", c.UserID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO abs_user_collections (id, user_id, profile_id, name, description, is_public)
		VALUES ($1, $2, $3::uuid, $4, $5, $6)`,
		c.ID, uid, profileArg(c.ProfileID), c.Name, c.Description, c.IsPublic,
	); err != nil {
		return fmt.Errorf("abs_collection_store: create: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) UpdateCollection(ctx context.Context, c abs.Collection) error {
	if _, err := s.Pool.Exec(ctx, `
		UPDATE abs_user_collections
		   SET name = $2, description = $3, is_public = $4, updated_at = now()
		 WHERE id = $1`,
		c.ID, c.Name, c.Description, c.IsPublic,
	); err != nil {
		return fmt.Errorf("abs_collection_store: update: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) DeleteCollection(ctx context.Context, id string) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM abs_user_collections WHERE id = $1`, id); err != nil {
		return fmt.Errorf("abs_collection_store: delete: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) ListCollectionItems(ctx context.Context, collectionID string) ([]abs.CollectionItem, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT collection_id, library_item_id, added_at
		FROM abs_collection_items
		WHERE collection_id = $1
		ORDER BY added_at ASC`, collectionID)
	if err != nil {
		return nil, fmt.Errorf("abs_collection_store: list-items: %w", err)
	}
	defer rows.Close()
	out := make([]abs.CollectionItem, 0)
	for rows.Next() {
		var it abs.CollectionItem
		if err := rows.Scan(&it.CollectionID, &it.LibraryItemID, &it.AddedAt); err != nil {
			return nil, fmt.Errorf("abs_collection_store: list-items scan: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *ABSCollectionStore) AddCollectionItem(ctx context.Context, collectionID, libraryItemID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_collection_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO abs_collection_items (collection_id, library_item_id)
		VALUES ($1, $2)
		ON CONFLICT (collection_id, library_item_id) DO NOTHING`,
		collectionID, libraryItemID,
	); err != nil {
		return fmt.Errorf("abs_collection_store: add-item: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE abs_user_collections SET updated_at = now() WHERE id = $1`, collectionID); err != nil {
		return fmt.Errorf("abs_collection_store: bump-parent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_collection_store: commit: %w", err)
	}
	return nil
}

func (s *ABSCollectionStore) RemoveCollectionItem(ctx context.Context, collectionID, libraryItemID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_collection_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM abs_collection_items WHERE collection_id = $1 AND library_item_id = $2`,
		collectionID, libraryItemID,
	); err != nil {
		return fmt.Errorf("abs_collection_store: remove-item: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE abs_user_collections SET updated_at = now() WHERE id = $1`, collectionID); err != nil {
		return fmt.Errorf("abs_collection_store: bump-parent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_collection_store: commit: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Wire in `BuildABSHandler`**

In `internal/audiobooks/service.go`, locate the `bookmarkStore` construction (added by the previous sub-project). Add a similar block after it:

```go
	var collectionStore abs.CollectionStore
	if deps.Pool != nil {
		collectionStore = &ABSCollectionStore{Pool: deps.Pool}
	}
```

In the `abs.New(abs.Dependencies{...})` call, add after `BookmarkStore`:

```go
		BookmarkStore:    bookmarkStore,
		CollectionStore:  collectionStore,
```

- [ ] **Step 3: Register routes in `mountRoutes`**

In `internal/audiobooks/abs/handler.go`, locate the Stage 4 group (the `bearerAuth` block where bookmark routes were added). At the end of the `for _, prefix := range ...` loop (after the bookmark `r.Delete` line), append:

```go
			// Collections — owner-gated CRUD with cross-user public reads.
			r.Get(prefix+"/collections", h.handleListCollections)
			r.Post(prefix+"/collections", h.handleCreateCollection)
			r.Get(prefix+"/collections/{id}", h.handleGetCollection)
			r.Patch(prefix+"/collections/{id}", h.handleUpdateCollection)
			r.Delete(prefix+"/collections/{id}", h.handleDeleteCollection)
			r.Post(prefix+"/collections/{id}/book/{bookId}", h.handleAddCollectionBook)
			r.Delete(prefix+"/collections/{id}/book/{bookId}", h.handleRemoveCollectionBook)
```

- [ ] **Step 4: Build and test**

```bash
go build ./...
go test ./internal/audiobooks/... -count=1 | tail -10
```

Expected: clean build, all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs_collection_store.go internal/audiobooks/service.go internal/audiobooks/abs/handler.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): wire ABSCollectionStore + mount collections routes

Adds the pgx-backed CollectionStore impl (parallel to
abs_bookmark_store.go), wires it into BuildABSHandler when a Pool is
present, and registers the seven collections routes under both
/abs/api and /api prefixes inside the existing bearerAuth group.
AddCollectionItem/RemoveCollectionItem run in a transaction so the
parent's updated_at bump is atomic with the item mutation.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Playlists migrations (151 + 152)

**Files:**
- Create: `migrations/151_abs_playlists.up.sql` + `.down.sql`
- Create: `migrations/152_abs_playlist_items.up.sql` + `.down.sql`

- [ ] **Step 1: Write migration 151 up**

`migrations/151_abs_playlists.up.sql`:

```sql
-- Ordered audiobook playlists. Profile-scoped (NULL profile_id =
-- primary profile, collapsed to a single bucket per user via the
-- COALESCE-to-sentinel index trick).
--
-- cover_item references media_items(content_id) with ON DELETE SET NULL
-- so the playlist survives cover-item deletion gracefully.

CREATE TABLE IF NOT EXISTS public.abs_playlists (
    id          text PRIMARY KEY,
    user_id     integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id  uuid,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    cover_item  text REFERENCES public.media_items(content_id) ON DELETE SET NULL,
    is_public   boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS abs_playlists_user_profile_idx
    ON public.abs_playlists (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
    );
```

- [ ] **Step 2: Write migration 151 down**

`migrations/151_abs_playlists.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_playlists_user_profile_idx;
DROP TABLE IF EXISTS public.abs_playlists;
```

- [ ] **Step 3: Write migration 152 up**

`migrations/152_abs_playlist_items.up.sql`:

```sql
-- Items inside a playlist. library_item_id is NOT FK'd (decoupled to
-- allow future episode support); episode_id defaults to '' (empty) so
-- the unique constraint works without COALESCE.
--
-- position is a sort hint; gaps are allowed (no compaction on remove).

CREATE TABLE IF NOT EXISTS public.abs_playlist_items (
    playlist_id     text NOT NULL REFERENCES public.abs_playlists(id) ON DELETE CASCADE,
    library_item_id text NOT NULL,
    episode_id      text NOT NULL DEFAULT '',
    position        integer NOT NULL,
    added_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (playlist_id, library_item_id, episode_id)
);

CREATE INDEX IF NOT EXISTS abs_playlist_items_playlist_position_idx
    ON public.abs_playlist_items (playlist_id, position);
```

- [ ] **Step 4: Write migration 152 down**

`migrations/152_abs_playlist_items.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_playlist_items_playlist_position_idx;
DROP TABLE IF EXISTS public.abs_playlist_items;
```

- [ ] **Step 5: Apply locally and verify**

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/151_abs_playlists.up.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/152_abs_playlist_items.up.sql
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_playlists"
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_playlist_items"
```

Expected: both tables created with FKs and indexes as in the up migrations.

Verify down + re-up:

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/152_abs_playlist_items.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/151_abs_playlists.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/151_abs_playlists.up.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/152_abs_playlist_items.up.sql
```

Expected: clean throughout.

- [ ] **Step 6: Commit**

```bash
git add migrations/151_abs_playlists.up.sql migrations/151_abs_playlists.down.sql \
        migrations/152_abs_playlist_items.up.sql migrations/152_abs_playlist_items.down.sql
git commit -m "$(cat <<'EOF'
feat(audiobooks): add abs_playlists + abs_playlist_items migrations

Migrations 151 + 152 back the upcoming ABS playlist endpoints.
Schema rationale documented in
docs/superpowers/specs/2026-05-26-abs-collections-playlists-design.md
§5.3, §5.4, §5.5.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Playlist types + `PlaylistStore` interface + envelope test

**Files:**
- Create: `internal/audiobooks/abs/playlists.go`
- Create: `internal/audiobooks/abs/playlists_envelope_test.go`

- [ ] **Step 1: Write the failing envelope test**

Create `internal/audiobooks/abs/playlists_envelope_test.go`:

```go
package abs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestPlaylistEnvelope_HasRequiredKeys asserts the eight (or nine with
// coverPath) top-level keys are present when populated. coverPath is
// emitted only when non-empty.
func TestPlaylistEnvelope_HasRequiredKeys(t *testing.T) {
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	out := playlistToABS(Playlist{
		ID:          "01HPL",
		UserID:      "1",
		Name:        "queue",
		Description: "",
		CoverItem:   "01HCOVER",
		IsPublic:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, []map[string]any{})
	body, _ := json.Marshal(out)
	js := string(body)
	for _, key := range []string{
		`"id":`, `"userId":`, `"name":`, `"description":`,
		`"isPublic":`, `"coverPath":`, `"createdAt":`, `"lastUpdate":`, `"items":`,
	} {
		if !strings.Contains(js, key) {
			t.Errorf("envelope missing %s; got %s", key, js)
		}
	}
	if out["coverPath"] != "01HCOVER" {
		t.Errorf("coverPath = %v, want 01HCOVER", out["coverPath"])
	}
}

// TestPlaylistEnvelope_OmitsCoverPathWhenEmpty asserts cover_item=""
// produces no coverPath key (matches continuum).
func TestPlaylistEnvelope_OmitsCoverPathWhenEmpty(t *testing.T) {
	out := playlistToABS(Playlist{
		ID: "01HPL", UserID: "1", Name: "x",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, []map[string]any{})
	if _, has := out["coverPath"]; has {
		t.Errorf("coverPath emitted when empty: %v", out)
	}
}

// TestPlaylistListShape_OmitsItems asserts nil items produces no items key.
func TestPlaylistListShape_OmitsItems(t *testing.T) {
	out := playlistToABS(Playlist{
		ID: "01HPL", UserID: "1", Name: "x",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, nil)
	if _, has := out["items"]; has {
		t.Errorf("list-shape includes items key (should be detail-only): %v", out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run TestPlaylistEnvelope -v
go test ./internal/audiobooks/abs/ -run TestPlaylistListShape -v
```

Expected: compile failure (`undefined: playlistToABS` / `undefined: Playlist`).

- [ ] **Step 3: Create the type, interface, and envelope helper**

Create `internal/audiobooks/abs/playlists.go`:

```go
package abs

import (
	"context"
	"time"
)

// PlaylistStore is the narrow slice of abs_playlists + abs_playlist_items
// the playlists handlers need. Implemented by ABSPlaylistStore in
// internal/audiobooks/abs_playlist_store.go.
type PlaylistStore interface {
	ListUserPlaylists(ctx context.Context, userID, profileID string) ([]Playlist, error)
	GetPlaylist(ctx context.Context, id string) (Playlist, error)
	CreatePlaylist(ctx context.Context, p Playlist) error
	UpdatePlaylist(ctx context.Context, p Playlist) error
	DeletePlaylist(ctx context.Context, id string) error
	ListPlaylistItems(ctx context.Context, playlistID string) ([]PlaylistItem, error)
	AddPlaylistItem(ctx context.Context, playlistID, libraryItemID, episodeID string) error
	RemovePlaylistItem(ctx context.Context, playlistID, libraryItemID, episodeID string) error
}

// Playlist is the in-memory representation of an abs_playlists row.
type Playlist struct {
	ID          string
	UserID      string
	ProfileID   string
	Name        string
	Description string
	CoverItem   string // empty when unset
	IsPublic    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PlaylistItem is the in-memory representation of an abs_playlist_items row.
type PlaylistItem struct {
	PlaylistID    string
	LibraryItemID string
	EpisodeID     string // empty for audiobook items
	Position      int
	AddedAt       time.Time
}

// playlistToABS shapes a Playlist in the ABS wire format. When items
// is nil the list-shape is emitted (no "items" key); when items is
// non-nil (possibly empty) the full-shape is emitted.
//
// coverPath is omitted when CoverItem is empty (matches continuum).
// Description is always present (round-tripped from storage).
func playlistToABS(p Playlist, items []map[string]any) map[string]any {
	out := map[string]any{
		"id":          p.ID,
		"userId":      p.UserID,
		"name":        p.Name,
		"description": p.Description,
		"isPublic":    p.IsPublic,
		"createdAt":   p.CreatedAt.UnixMilli(),
		"lastUpdate":  p.UpdatedAt.UnixMilli(),
	}
	if p.CoverItem != "" {
		out["coverPath"] = p.CoverItem
	}
	if items != nil {
		out["items"] = items
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run TestPlaylist -v
```

Expected: all three PASS.

- [ ] **Step 5: Build**

```bash
go build ./...
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/audiobooks/abs/playlists.go internal/audiobooks/abs/playlists_envelope_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): add PlaylistStore interface + ABS envelope helper

Defines the storage contract and wire-shape serialiser the playlists
handlers will consume. Envelope test asserts the eight (or nine with
coverPath) top-level keys including description always round-tripped
correctly and coverPath omitted when empty.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Test harness + `handleCreatePlaylist` + `playlist_added` event

**Files:**
- Modify: `internal/audiobooks/abs/handler.go` (add `PlaylistStore` field)
- Create: `internal/audiobooks/abs/playlists_handler.go`
- Create: `internal/audiobooks/abs/playlists_handler_test.go`

- [ ] **Step 1: Add `PlaylistStore` field to `Dependencies`**

In `internal/audiobooks/abs/handler.go`, in the `Dependencies` struct, add after `CollectionStore`:

```go
	// PlaylistStore persists ABS playlist rows (migrations 151 + 152).
	// May be nil; handlers respond 503 when unset.
	PlaylistStore PlaylistStore
```

Build:

```bash
go build ./...
```

Expected: clean.

- [ ] **Step 2: Create failing test + in-memory fake**

Create `internal/audiobooks/abs/playlists_handler_test.go`:

```go
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

// memPlaylistStore is an in-memory PlaylistStore for handler tests.
type memPlaylistStore struct {
	mu    sync.Mutex
	rows  map[string]Playlist        // id -> row
	items map[string][]PlaylistItem  // playlist_id -> items
}

func newMemPlaylistStore() *memPlaylistStore {
	return &memPlaylistStore{
		rows:  map[string]Playlist{},
		items: map[string][]PlaylistItem{},
	}
}

func (m *memPlaylistStore) ListUserPlaylists(_ context.Context, userID, profileID string) ([]Playlist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Playlist, 0)
	for _, p := range m.rows {
		if p.UserID == userID && p.ProfileID == profileID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memPlaylistStore) GetPlaylist(_ context.Context, id string) (Playlist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[id]
	if !ok {
		return Playlist{}, ErrNotFound
	}
	return p, nil
}

func (m *memPlaylistStore) CreatePlaylist(_ context.Context, p Playlist) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[p.ID] = p
	return nil
}

func (m *memPlaylistStore) UpdatePlaylist(_ context.Context, p Playlist) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.rows[p.ID]
	if !ok {
		return ErrNotFound
	}
	existing.Name = p.Name
	existing.Description = p.Description
	existing.CoverItem = p.CoverItem
	existing.IsPublic = p.IsPublic
	existing.UpdatedAt = time.Now()
	m.rows[p.ID] = existing
	return nil
}

func (m *memPlaylistStore) DeletePlaylist(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, id)
	delete(m.items, id)
	return nil
}

func (m *memPlaylistStore) ListPlaylistItems(_ context.Context, playlistID string) ([]PlaylistItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.items[playlistID]
	out := make([]PlaylistItem, len(items))
	copy(out, items)
	sort.Slice(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out, nil
}

func (m *memPlaylistStore) AddPlaylistItem(_ context.Context, playlistID, libraryItemID, episodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.items[playlistID] {
		if it.LibraryItemID == libraryItemID && it.EpisodeID == episodeID {
			return nil // ON CONFLICT DO NOTHING
		}
	}
	maxPos := 0
	for _, it := range m.items[playlistID] {
		if it.Position > maxPos {
			maxPos = it.Position
		}
	}
	m.items[playlistID] = append(m.items[playlistID], PlaylistItem{
		PlaylistID:    playlistID,
		LibraryItemID: libraryItemID,
		EpisodeID:     episodeID,
		Position:      maxPos + 1,
		AddedAt:       time.Now(),
	})
	if p, ok := m.rows[playlistID]; ok {
		p.UpdatedAt = time.Now()
		m.rows[playlistID] = p
	}
	return nil
}

func (m *memPlaylistStore) RemovePlaylistItem(_ context.Context, playlistID, libraryItemID, episodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := m.items[playlistID]
	out := items[:0]
	for _, it := range items {
		if it.LibraryItemID != libraryItemID || it.EpisodeID != episodeID {
			out = append(out, it)
		}
	}
	m.items[playlistID] = out
	if p, ok := m.rows[playlistID]; ok {
		p.UpdatedAt = time.Now()
		m.rows[playlistID] = p
	}
	return nil
}

type playlistsHarness struct {
	H    *Handler
	Play *memPlaylistStore
	Pub  *recordingPublisher
}

func newPlaylistsHarness(t *testing.T, knownItems ...string) *playlistsHarness {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = nil
	}
	pub := &recordingPublisher{}
	store := newMemPlaylistStore()
	h := New(Dependencies{
		MediaStore:    &stubMediaStore{known: known},
		PlaylistStore: store,
		Publisher:     pub,
	})
	return &playlistsHarness{H: h, Play: store, Pub: pub}
}

func TestPlaylist_Create_ReturnsFullShape(t *testing.T) {
	hb := newPlaylistsHarness(t)
	body := []byte(`{"name":"queue","description":"d","isPublic":true}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists", nil, body, "1", "", hb.H.handleCreatePlaylist)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "queue" {
		t.Errorf("name = %v, want queue", got["name"])
	}
	if got["isPublic"] != true {
		t.Errorf("isPublic = %v, want true", got["isPublic"])
	}
	items, _ := got["items"].([]any)
	if items == nil {
		t.Errorf("items missing on full-shape: %v", got)
	}
}

func TestPlaylist_Create_NameRequired_400(t *testing.T) {
	hb := newPlaylistsHarness(t)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists", nil, []byte(`{}`), "1", "", hb.H.handleCreatePlaylist)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPlaylist_Create_FiresPlaylistAddedEvent(t *testing.T) {
	hb := newPlaylistsHarness(t)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists", nil, []byte(`{"name":"queue"}`), "7", "", hb.H.handleCreatePlaylist)
	evts := hb.Pub.snapshot()
	if len(evts) != 1 {
		t.Fatalf("events = %d, want 1", len(evts))
	}
	if evts[0].Event != "playlist_added" {
		t.Errorf("event = %q, want playlist_added", evts[0].Event)
	}
	if evts[0].UserID != "7" {
		t.Errorf("event userID = %q, want 7", evts[0].UserID)
	}
	payload, _ := evts[0].Payload.(map[string]any)
	if payload["name"] != "queue" {
		t.Errorf("payload name = %v, want queue", payload["name"])
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_Create' -v
```

Expected: compile failure (`h.handleCreatePlaylist undefined`).

- [ ] **Step 4: Implement `handleCreatePlaylist`**

Create `internal/audiobooks/abs/playlists_handler.go`:

```go
package abs

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

// playlistBody is the JSON body for POST and PATCH /playlists[/{id}].
// Fields are pointers so PATCH can distinguish "field absent" from
// "field set to empty/false".
type playlistBody struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	CoverItem   *string `json:"cover_item"`
	IsPublic    *bool   `json:"isPublic"`
}

// playlistItemRef is the JSON body for adding/removing a single
// playlist item (and an element of the batch arrays).
type playlistItemRef struct {
	LibraryItemID string `json:"libraryItemId"`
	EpisodeID     string `json:"episodeId"`
}

// handleCreatePlaylist — POST /playlists.
// Body: {name, description?, cover_item?, isPublic?}.
// Returns the created playlist in full-shape (empty items[]).
// Fires playlist_added on success.
func (h *Handler) handleCreatePlaylist(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist store unavailable", http.StatusServiceUnavailable)
		return
	}

	var body playlistBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == nil || *body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	p := Playlist{
		ID:        ulid.Make().String(),
		UserID:    a.UserID,
		ProfileID: a.ProfileID,
		Name:      *body.Name,
	}
	if body.Description != nil {
		p.Description = *body.Description
	}
	if body.CoverItem != nil {
		p.CoverItem = *body.CoverItem
	}
	if body.IsPublic != nil {
		p.IsPublic = *body.IsPublic
	}
	if err := h.deps.PlaylistStore.CreatePlaylist(r.Context(), p); err != nil {
		slog.Error("abs playlist create failed", "err", err, "user", a.UserID)
		http.Error(w, "playlist persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), p.ID)
	if errors.Is(err, ErrNotFound) {
		persisted = p
	} else if err != nil {
		persisted = p
	}

	h.publish(a.UserID, "playlist_added", map[string]any{"id": p.ID, "name": p.Name})
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, persisted))
}

// playlistFullShape renders a Playlist in full-shape, hydrating items[]
// via MediaStore for audiobook items (episode items echo bare refs).
func (h *Handler) playlistFullShape(r *http.Request, p Playlist) map[string]any {
	items := h.playlistItems(r, p.ID)
	return playlistToABS(p, items)
}

// playlistItems resolves items in a playlist to wire-shape entries.
// Audiobook items (empty episodeId) hydrate title via MediaStore.
// Episode items are emitted as bare {libraryItemId, episodeId, position}.
func (h *Handler) playlistItems(r *http.Request, playlistID string) []map[string]any {
	if h.deps.PlaylistStore == nil {
		return []map[string]any{}
	}
	rows, err := h.deps.PlaylistStore.ListPlaylistItems(r.Context(), playlistID)
	if err != nil {
		slog.Warn("abs playlist list-items failed", "err", err, "playlist", playlistID)
		return []map[string]any{}
	}
	lib := h.resolveDefaultLibrary(r.Context())
	libID := audiobookLibraryID(lib)
	out := make([]map[string]any, 0, len(rows))
	for _, it := range rows {
		entry := map[string]any{
			"libraryItemId": it.LibraryItemID,
			"position":      it.Position,
		}
		if it.EpisodeID != "" {
			entry["episodeId"] = it.EpisodeID
		} else {
			// Audiobook hydration.
			if item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), it.LibraryItemID); err == nil && item != nil {
				entry["libraryId"] = libID
				entry["title"] = item.Title
			}
		}
		out = append(out, entry)
	}
	return out
}

// playlistURLID is a tiny shim around chi.URLParam(r, "id") to read
// uniformly with the collections handler's chiURLID.
func playlistURLID(r *http.Request) string { return chi.URLParam(r, "id") }
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_Create' -v
```

Expected: all three PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/audiobooks/abs/handler.go internal/audiobooks/abs/playlists_handler.go internal/audiobooks/abs/playlists_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): POST /playlists — ABS playlist create + event

First handler of the playlists surface. Body {name, description?,
cover_item?, isPublic?} returns the created playlist in full-shape
(empty items[]). Fires playlist_added realtime event. Adds the
in-memory test harness (memPlaylistStore) parallel to
memCollectionStore.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: `handleListPlaylists` + `handleGetPlaylist` + visibility tests

**Files:**
- Modify: `internal/audiobooks/abs/playlists_handler.go`
- Modify: `internal/audiobooks/abs/playlists_handler_test.go`

- [ ] **Step 1: Append failing tests**

Append to `playlists_handler_test.go`:

```go

func createPlaylistForUser(t *testing.T, hb *playlistsHarness, userID, profileID, body string) string {
	t.Helper()
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists", nil, []byte(body), userID, profileID, hb.H.handleCreatePlaylist)
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

func TestPlaylist_List_WrappedEnvelope(t *testing.T) {
	hb := newPlaylistsHarness(t)
	_ = createPlaylistForUser(t, hb, "1", "", `{"name":"a"}`)
	_ = createPlaylistForUser(t, hb, "1", "", `{"name":"b"}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists", nil, nil, "1", "", hb.H.handleListPlaylists)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, ok := env["playlists"].([]any)
	if !ok {
		t.Fatalf("response missing 'playlists' key; body=%s", rec.Body.String())
	}
	if len(list) != 2 {
		t.Errorf("list len = %d, want 2", len(list))
	}
	for _, p := range list {
		entry := p.(map[string]any)
		if _, has := entry["items"]; has {
			t.Errorf("list entry has items key (should be detail-only): %v", entry)
		}
	}
}

func TestPlaylist_List_ProfileIsolation(t *testing.T) {
	hb := newPlaylistsHarness(t)
	pA := "00000000-0000-0000-0000-0000000000aa"
	pB := "00000000-0000-0000-0000-0000000000bb"
	_ = createPlaylistForUser(t, hb, "1", pA, `{"name":"A"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists", nil, nil, "1", pB, hb.H.handleListPlaylists)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, _ := env["playlists"].([]any)
	if len(list) != 0 {
		t.Errorf("profile B sees %d playlists, want 0", len(list))
	}
}

func TestPlaylist_Get_Owner_ReturnsFullShape(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleGetPlaylist)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "mine" {
		t.Errorf("name = %v, want 'mine'", got["name"])
	}
	if _, has := got["items"]; !has {
		t.Errorf("items missing on full-shape: %v", got)
	}
}

func TestPlaylist_Get_NonOwner_Private_404(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"private"}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleGetPlaylist)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlaylist_Get_NonOwner_Public_OK(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"public","isPublic":true}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleGetPlaylist)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlaylist_Get_Unknown_404(t *testing.T) {
	hb := newPlaylistsHarness(t)
	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists/01HZZZ", map[string]string{"id": "01HZZZ"}, nil, "1", "", hb.H.handleGetPlaylist)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_List|TestPlaylist_Get' -v
```

Expected: compile failure.

- [ ] **Step 3: Implement both handlers**

Append to `playlists_handler.go`:

```go

// handleListPlaylists — GET /playlists.
// Returns the caller's playlists wrapped in {"playlists": [...]}.
// List-shape (no items[]).
func (h *Handler) handleListPlaylists(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"playlists": []any{}})
		return
	}
	rows, err := h.deps.PlaylistStore.ListUserPlaylists(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs playlist list failed", "err", err, "user", a.UserID)
		http.Error(w, "playlist list failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, playlistToABS(p, nil))
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlists": out})
}

// handleGetPlaylist — GET /playlists/{id}.
// Owner gets full-shape; non-owner gets full-shape only when isPublic.
// Otherwise 404 (no existence leak).
func (h *Handler) handleGetPlaylist(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), playlistURLID(r))
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID && !p.IsPublic) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get failed", "err", err)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, p))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_List|TestPlaylist_Get' -v
```

Expected: all six PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/playlists_handler.go internal/audiobooks/abs/playlists_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): GET /playlists + GET /playlists/{id}

List wraps the result in {"playlists": [...]} and emits list-shape
(no items[]). Detail handler returns full-shape for owner or for any
caller when isPublic=true; otherwise 404 matching the bookmarks
anti-enumeration pattern.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: `handleUpdatePlaylist` + `handleDeletePlaylist` + tests + events

**Files:**
- Modify: `internal/audiobooks/abs/playlists_handler.go`
- Modify: `internal/audiobooks/abs/playlists_handler_test.go`

- [ ] **Step 1: Append failing tests**

Append to `playlists_handler_test.go`:

```go

func TestPlaylist_Patch_UpdatesCover(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"x"}`)

	body := []byte(`{"cover_item":"01HCOVER"}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/playlists/"+id, map[string]string{"id": id}, body, "1", "", hb.H.handleUpdatePlaylist)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["coverPath"] != "01HCOVER" {
		t.Errorf("coverPath = %v, want 01HCOVER", got["coverPath"])
	}
}

func TestPlaylist_Patch_FiresUpdatedEvent(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "7", "", `{"name":"x"}`)
	// snapshot count after create
	before := len(hb.Pub.snapshot())

	_ = dispatchABSWithParams(http.MethodPatch, "/api/playlists/"+id, map[string]string{"id": id}, []byte(`{"name":"renamed"}`), "7", "", hb.H.handleUpdatePlaylist)

	evts := hb.Pub.snapshot()
	if len(evts) != before+1 {
		t.Fatalf("events = %d (delta %d), want exactly 1 new event", len(evts), len(evts)-before)
	}
	if evts[len(evts)-1].Event != "playlist_updated" {
		t.Errorf("event = %q, want playlist_updated", evts[len(evts)-1].Event)
	}
}

func TestPlaylist_Patch_NonOwner_404(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)

	rec := dispatchABSWithParams(http.MethodPatch, "/api/playlists/"+id, map[string]string{"id": id}, []byte(`{"name":"hijack"}`), "2", "", hb.H.handleUpdatePlaylist)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	p, _ := hb.Play.GetPlaylist(context.Background(), id)
	if p.Name != "mine" {
		t.Errorf("non-owner mutation leaked: name = %q", p.Name)
	}
}

func TestPlaylist_Delete_Owner_FiresRemovedEvent(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "7", "", `{"name":"x"}`)
	before := len(hb.Pub.snapshot())

	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id, map[string]string{"id": id}, nil, "7", "", hb.H.handleDeletePlaylist)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	evts := hb.Pub.snapshot()
	if len(evts) != before+1 {
		t.Fatalf("events = %d, want exactly 1 new event", len(evts)-before)
	}
	if evts[len(evts)-1].Event != "playlist_removed" {
		t.Errorf("event = %q, want playlist_removed", evts[len(evts)-1].Event)
	}
}

func TestPlaylist_Delete_NonOwner_404(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleDeletePlaylist)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if _, err := hb.Play.GetPlaylist(context.Background(), id); err != nil {
		t.Errorf("playlist wrongly deleted: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_Patch|TestPlaylist_Delete' -v
```

Expected: compile failure.

- [ ] **Step 3: Implement both handlers**

Append to `playlists_handler.go`:

```go

// handleUpdatePlaylist — PATCH /playlists/{id}.
// Owner-only. Partial body. Fires playlist_updated.
func (h *Handler) handleUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	id := playlistURLID(r)
	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get-for-update failed", "err", err, "id", id)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}

	var body playlistBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name != nil {
		p.Name = *body.Name
	}
	if body.Description != nil {
		p.Description = *body.Description
	}
	if body.CoverItem != nil {
		p.CoverItem = *body.CoverItem
	}
	if body.IsPublic != nil {
		p.IsPublic = *body.IsPublic
	}
	if err := h.deps.PlaylistStore.UpdatePlaylist(r.Context(), p); err != nil {
		slog.Error("abs playlist update failed", "err", err, "id", id)
		http.Error(w, "playlist persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if err != nil {
		persisted = p
	}
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, persisted))
}

// handleDeletePlaylist — DELETE /playlists/{id}.
// Owner-only. Cascade drops abs_playlist_items via FK.
// Fires playlist_removed.
func (h *Handler) handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	id := playlistURLID(r)
	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get-for-delete failed", "err", err, "id", id)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.PlaylistStore.DeletePlaylist(r.Context(), id); err != nil {
		slog.Error("abs playlist delete failed", "err", err, "id", id)
		http.Error(w, "playlist delete failed", http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "playlist_removed", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_Patch|TestPlaylist_Delete' -v
```

Expected: all five PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/playlists_handler.go internal/audiobooks/abs/playlists_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): PATCH + DELETE /playlists/{id}

Owner-gated mutation with partial-body PATCH semantics. Non-owner gets
404 (anti-enumeration). Both handlers fire realtime events
(playlist_updated, playlist_removed) — clients re-render from the
response (the event payloads carry only the id).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 14: `handleAddPlaylistItem` (single) + position/hydration/episode tests

**Files:**
- Modify: `internal/audiobooks/abs/playlists_handler.go`
- Modify: `internal/audiobooks/abs/playlists_handler_test.go`

- [ ] **Step 1: Append failing tests**

Append to `playlists_handler_test.go`:

```go

func TestPlaylist_AddItem_AudiobookHydrates(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"libraryItemId":"book-1"}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	entry := items[0].(map[string]any)
	if entry["libraryItemId"] != "book-1" {
		t.Errorf("libraryItemId = %v, want book-1", entry["libraryItemId"])
	}
	if _, has := entry["title"]; !has {
		t.Errorf("audiobook item missing 'title' hydration: %v", entry)
	}
	if pos, _ := entry["position"].(float64); pos != 1 {
		t.Errorf("first item position = %v, want 1", entry["position"])
	}
}

func TestPlaylist_AddItem_AppendsAtNextPosition(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1", "book-2", "book-3")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	for _, b := range []string{"book-1", "book-2", "book-3"} {
		body := []byte(`{"libraryItemId":"` + b + `"}`)
		_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
			map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	}
	rec := dispatchABSWithParams(http.MethodGet, "/api/playlists/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleGetPlaylist)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items len = %d, want 3", len(items))
	}
	for i, raw := range items {
		entry := raw.(map[string]any)
		wantPos := float64(i + 1)
		if entry["position"] != wantPos {
			t.Errorf("items[%d] position = %v, want %v", i, entry["position"], wantPos)
		}
	}
}

func TestPlaylist_AddItem_Idempotent(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"libraryItemId":"book-1"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item", map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item", map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusOK {
		t.Fatalf("second add status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1 (idempotent)", len(items))
	}
}

func TestPlaylist_AddItem_Episode_AcceptsAndEchoes(t *testing.T) {
	hb := newPlaylistsHarness(t /* no known items - episode skips validation */)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"libraryItemId":"podcast-x","episodeId":"ep-1"}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	entry := items[0].(map[string]any)
	if entry["episodeId"] != "ep-1" {
		t.Errorf("episodeId = %v, want ep-1", entry["episodeId"])
	}
	if _, has := entry["title"]; has {
		t.Errorf("episode item must NOT be hydrated: %v", entry)
	}
}

func TestPlaylist_AddItem_UnknownAudiobook_404(t *testing.T) {
	hb := newPlaylistsHarness(t /* no known items */)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"libraryItemId":"ghost"}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (item not found); body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlaylist_AddItem_LibraryItemIdRequired_400(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"libraryItemId":""}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "1", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPlaylist_AddItem_NonOwner_404(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)

	body := []byte(`{"libraryItemId":"book-1"}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "2", "", hb.H.handleAddPlaylistItem)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPlaylist_AddItem_FiresUpdatedEvent(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "7", "", `{"name":"q"}`)
	before := len(hb.Pub.snapshot())

	body := []byte(`{"libraryItemId":"book-1"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, body, "7", "", hb.H.handleAddPlaylistItem)

	evts := hb.Pub.snapshot()
	if len(evts) != before+1 {
		t.Fatalf("events = %d, want exactly 1 new event", len(evts)-before)
	}
	if evts[len(evts)-1].Event != "playlist_updated" {
		t.Errorf("event = %q, want playlist_updated", evts[len(evts)-1].Event)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_AddItem' -v
```

Expected: compile failure (`h.handleAddPlaylistItem undefined`).

- [ ] **Step 3: Implement the handler**

Append to `playlists_handler.go`:

```go

// handleAddPlaylistItem — POST /playlists/{id}/item.
// Body: {libraryItemId, episodeId?}.
// Owner-only. Item validation: audiobooks validated via MediaStore
// (404 on unknown); episode items skip validation per spec §7.1 (the
// audiobook-only-hydration policy doesn't reject opaque episode IDs).
// Idempotent on (libraryItemId, episodeId) tuple. Fires playlist_updated.
func (h *Handler) handleAddPlaylistItem(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	id := playlistURLID(r)
	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get-for-add failed", "err", err, "id", id)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}

	var body playlistItemRef
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.LibraryItemID == "" {
		http.Error(w, "libraryItemId required", http.StatusBadRequest)
		return
	}

	// Audiobook items validated; episodes skip validation.
	if body.EpisodeID == "" {
		item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), body.LibraryItemID)
		if err != nil || item == nil {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}
	}

	if err := h.deps.PlaylistStore.AddPlaylistItem(r.Context(), id, body.LibraryItemID, body.EpisodeID); err != nil {
		slog.Error("abs playlist add-item failed", "err", err, "id", id)
		http.Error(w, "playlist persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if err != nil {
		persisted = p
	}
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, persisted))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_AddItem' -v
```

Expected: all eight PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/playlists_handler.go internal/audiobooks/abs/playlists_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): POST /playlists/{id}/item — add single item

Append a new (libraryItemId, episodeId) tuple to the end of the
playlist (positions start at 1 and increment). Audiobook items
validated against MediaStore (404 on unknown); episode items
accept-and-echo per spec §7.1 (podcast hydration is a future
sub-project). Idempotent on the tuple. Fires playlist_updated.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 15: `handleRemovePlaylistItem` + `handleRemovePlaylistEpisode` + tests

**Files:**
- Modify: `internal/audiobooks/abs/playlists_handler.go`
- Modify: `internal/audiobooks/abs/playlists_handler_test.go`

- [ ] **Step 1: Append failing tests**

Append to `playlists_handler_test.go`:

```go

func TestPlaylist_RemoveItem_Single(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1", "book-2")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, []byte(`{"libraryItemId":"book-1"}`), "1", "", hb.H.handleAddPlaylistItem)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, []byte(`{"libraryItemId":"book-2"}`), "1", "", hb.H.handleAddPlaylistItem)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id+"/item/book-1",
		map[string]string{"id": id, "libraryItemId": "book-1"}, nil, "1", "", hb.H.handleRemovePlaylistItem)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].(map[string]any)["libraryItemId"] != "book-2" {
		t.Errorf("remaining item = %v, want book-2", items[0])
	}
}

func TestPlaylist_RemoveItem_Idempotent(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id+"/item/book-99",
		map[string]string{"id": id, "libraryItemId": "book-99"}, nil, "1", "", hb.H.handleRemovePlaylistItem)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (idempotent)", rec.Code)
	}
}

func TestPlaylist_RemoveItem_WithEpisode(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	// Seed two items at the same libraryItemId — one with episode, one without.
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, []byte(`{"libraryItemId":"podcast-x","episodeId":"ep-1"}`), "1", "", hb.H.handleAddPlaylistItem)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, []byte(`{"libraryItemId":"podcast-x","episodeId":"ep-2"}`), "1", "", hb.H.handleAddPlaylistItem)

	// Remove ep-1 specifically.
	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id+"/item/podcast-x/ep-1",
		map[string]string{"id": id, "libraryItemId": "podcast-x", "episodeId": "ep-1"}, nil, "1", "", hb.H.handleRemovePlaylistEpisode)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1 (ep-2 should remain)", len(items))
	}
	if items[0].(map[string]any)["episodeId"] != "ep-2" {
		t.Errorf("remaining item episodeId = %v, want ep-2", items[0])
	}
}

func TestPlaylist_RemoveItem_NonOwner_404(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
		map[string]string{"id": id}, []byte(`{"libraryItemId":"book-1"}`), "1", "", hb.H.handleAddPlaylistItem)

	rec := dispatchABSWithParams(http.MethodDelete, "/api/playlists/"+id+"/item/book-1",
		map[string]string{"id": id, "libraryItemId": "book-1"}, nil, "2", "", hb.H.handleRemovePlaylistItem)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	items, _ := hb.Play.ListPlaylistItems(context.Background(), id)
	if len(items) != 1 {
		t.Errorf("items len = %d, want 1 (non-owner remove leaked)", len(items))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_RemoveItem' -v
```

Expected: compile failure.

- [ ] **Step 3: Implement both handlers**

Append to `playlists_handler.go`:

```go

// handleRemovePlaylistItem — DELETE /playlists/{id}/item/{libraryItemId}.
// Owner-only. Removes the item with empty episode_id. Idempotent.
// Fires playlist_updated.
func (h *Handler) handleRemovePlaylistItem(w http.ResponseWriter, r *http.Request) {
	h.removePlaylistItemImpl(w, r, "")
}

// handleRemovePlaylistEpisode — DELETE /playlists/{id}/item/{libraryItemId}/{episodeId}.
// Owner-only. Removes the item keyed on (libraryItemId, episodeId).
// Idempotent. Fires playlist_updated.
func (h *Handler) handleRemovePlaylistEpisode(w http.ResponseWriter, r *http.Request) {
	h.removePlaylistItemImpl(w, r, chi.URLParam(r, "episodeId"))
}

// removePlaylistItemImpl is the shared body for both remove variants.
// episodeIDFromURL is "" for the libraryItemId-only DELETE and the
// {episodeId} URL param for the episode-aware DELETE.
func (h *Handler) removePlaylistItemImpl(w http.ResponseWriter, r *http.Request, episodeIDFromURL string) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	id := playlistURLID(r)
	libItem := chi.URLParam(r, "libraryItemId")
	if libItem == "" {
		http.Error(w, "libraryItemId required", http.StatusBadRequest)
		return
	}

	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get-for-remove failed", "err", err, "id", id)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}

	if err := h.deps.PlaylistStore.RemovePlaylistItem(r.Context(), id, libItem, episodeIDFromURL); err != nil {
		slog.Error("abs playlist remove-item failed", "err", err, "id", id, "item", libItem, "episode", episodeIDFromURL)
		http.Error(w, "playlist delete failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if err != nil {
		persisted = p
	}
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, persisted))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_RemoveItem' -v
```

Expected: all four PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/playlists_handler.go internal/audiobooks/abs/playlists_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): DELETE /playlists/{id}/item/{libraryItemId}[/{episodeId}]

Two route variants share the same body via removePlaylistItemImpl:
the bare libraryItemId form removes the item with empty episode_id;
the libraryItemId+episodeId form removes only that episode-keyed
entry, leaving other entries with the same libraryItemId intact.
Idempotent; fires playlist_updated.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 16: Batch add + batch remove + tests

**Files:**
- Modify: `internal/audiobooks/abs/playlists_handler.go`
- Modify: `internal/audiobooks/abs/playlists_handler_test.go`

- [ ] **Step 1: Append failing tests**

Append to `playlists_handler_test.go`:

```go

func TestPlaylist_BatchAdd_TolerantOfPartialFailures(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1", "book-2") // book-3 unknown
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"items":[{"libraryItemId":"book-1"},{"libraryItemId":"book-3"},{"libraryItemId":"book-2"}]}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/batch/add",
		map[string]string{"id": id}, body, "1", "", hb.H.handleBatchAddPlaylistItems)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 2 {
		t.Errorf("items len = %d, want 2 (book-3 skipped)", len(items))
	}
}

func TestPlaylist_BatchAdd_FiresOneUpdatedEvent(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1", "book-2")
	id := createPlaylistForUser(t, hb, "7", "", `{"name":"q"}`)
	before := len(hb.Pub.snapshot())

	body := []byte(`{"items":[{"libraryItemId":"book-1"},{"libraryItemId":"book-2"}]}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/batch/add",
		map[string]string{"id": id}, body, "7", "", hb.H.handleBatchAddPlaylistItems)

	evts := hb.Pub.snapshot()
	if len(evts)-before != 1 {
		t.Errorf("event delta = %d, want exactly 1", len(evts)-before)
	}
}

func TestPlaylist_BatchAdd_EmptyItems_OKNoOp(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	body := []byte(`{"items":[]}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/batch/add",
		map[string]string{"id": id}, body, "1", "", hb.H.handleBatchAddPlaylistItems)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestPlaylist_BatchAdd_InvalidBody_400(t *testing.T) {
	hb := newPlaylistsHarness(t)
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)

	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/batch/add",
		map[string]string{"id": id}, []byte(`{not json`), "1", "", hb.H.handleBatchAddPlaylistItems)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPlaylist_BatchAdd_NonOwner_404(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"mine"}`)

	body := []byte(`{"items":[{"libraryItemId":"book-1"}]}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/batch/add",
		map[string]string{"id": id}, body, "2", "", hb.H.handleBatchAddPlaylistItems)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPlaylist_BatchRemove(t *testing.T) {
	hb := newPlaylistsHarness(t, "book-1", "book-2", "book-3")
	id := createPlaylistForUser(t, hb, "1", "", `{"name":"q"}`)
	for _, b := range []string{"book-1", "book-2", "book-3"} {
		_ = dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/item",
			map[string]string{"id": id}, []byte(`{"libraryItemId":"`+b+`"}`), "1", "", hb.H.handleAddPlaylistItem)
	}

	body := []byte(`{"items":[{"libraryItemId":"book-1"},{"libraryItemId":"book-3"}]}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/playlists/"+id+"/batch/remove",
		map[string]string{"id": id}, body, "1", "", hb.H.handleBatchRemovePlaylistItems)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1 (book-2 should remain)", len(items))
	}
	if items[0].(map[string]any)["libraryItemId"] != "book-2" {
		t.Errorf("remaining item = %v, want book-2", items[0])
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_BatchAdd|TestPlaylist_BatchRemove' -v
```

Expected: compile failure.

- [ ] **Step 3: Implement both handlers**

Append to `playlists_handler.go`:

```go

// batchItemsBody is the shared body shape for batch add/remove.
type batchItemsBody struct {
	Items []playlistItemRef `json:"items"`
}

// handleBatchAddPlaylistItems — POST /playlists/{id}/batch/add.
// Body: {items: [{libraryItemId, episodeId?}]}. Per-item failures are
// tolerated silently (matches continuum). Only the whole-body decode
// failure surfaces as 400. Audiobook items validated per-entry; failed
// validations skipped with slog.Debug (the entry never reaches the
// store). One playlist_updated event fires for the whole batch.
func (h *Handler) handleBatchAddPlaylistItems(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	id := playlistURLID(r)
	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get-for-batch-add failed", "err", err, "id", id)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}

	var body batchItemsBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	for _, it := range body.Items {
		if it.LibraryItemID == "" {
			slog.Debug("abs playlist batch-add: skipping empty libraryItemId")
			continue
		}
		// Audiobook validation; episode items skip.
		if it.EpisodeID == "" {
			item, lookupErr := h.deps.MediaStore.GetAudiobookByID(r.Context(), it.LibraryItemID)
			if lookupErr != nil || item == nil {
				slog.Debug("abs playlist batch-add: skipping unknown audiobook", "id", it.LibraryItemID)
				continue
			}
		}
		if addErr := h.deps.PlaylistStore.AddPlaylistItem(r.Context(), id, it.LibraryItemID, it.EpisodeID); addErr != nil {
			slog.Debug("abs playlist batch-add: store error", "err", addErr, "id", it.LibraryItemID)
		}
	}

	persisted, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if err != nil {
		persisted = p
	}
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, persisted))
}

// handleBatchRemovePlaylistItems — POST /playlists/{id}/batch/remove.
// Body: {items: [{libraryItemId, episodeId?}]}. Per-item failures
// tolerated; one playlist_updated event for the whole batch.
func (h *Handler) handleBatchRemovePlaylistItems(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaylistStore == nil {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	id := playlistURLID(r)
	p, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && p.UserID != a.UserID) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs playlist get-for-batch-remove failed", "err", err, "id", id)
		http.Error(w, "playlist get failed", http.StatusInternalServerError)
		return
	}

	var body batchItemsBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	for _, it := range body.Items {
		if rmErr := h.deps.PlaylistStore.RemovePlaylistItem(r.Context(), id, it.LibraryItemID, it.EpisodeID); rmErr != nil {
			slog.Debug("abs playlist batch-remove: store error", "err", rmErr, "id", it.LibraryItemID)
		}
	}

	persisted, err := h.deps.PlaylistStore.GetPlaylist(r.Context(), id)
	if err != nil {
		persisted = p
	}
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": id})
	writeJSON(w, http.StatusOK, h.playlistFullShape(r, persisted))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run 'TestPlaylist_BatchAdd|TestPlaylist_BatchRemove' -v
```

Expected: all six PASS.

- [ ] **Step 5: Run the full package**

```bash
go test ./internal/audiobooks/abs/ -count=1 | tail -5
```

Expected: ends in `PASS`.

- [ ] **Step 6: Commit**

```bash
git add internal/audiobooks/abs/playlists_handler.go internal/audiobooks/abs/playlists_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): batch add/remove playlist items

POST /playlists/{id}/batch/add and /batch/remove accept arrays of
{libraryItemId, episodeId?} tuples. Per-item failures are tolerated
silently (matching continuum); only a whole-body decode error
surfaces as 400. One playlist_updated event fires for the whole
batch regardless of per-item outcomes.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 17: `ABSPlaylistStore` (pgx) + wiring + route registration

**Files:**
- Create: `internal/audiobooks/abs_playlist_store.go`
- Modify: `internal/audiobooks/service.go` (`BuildABSHandler`)
- Modify: `internal/audiobooks/abs/handler.go` (`mountRoutes`)

- [ ] **Step 1: Implement the concrete store**

Create `internal/audiobooks/abs_playlist_store.go`:

```go
package audiobooks

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSPlaylistStore implements abs.PlaylistStore against the abs_playlists
// + abs_playlist_items tables (migrations 151 + 152).
type ABSPlaylistStore struct {
	Pool *pgxpool.Pool
}

var _ abs.PlaylistStore = (*ABSPlaylistStore)(nil)

func (s *ABSPlaylistStore) ListUserPlaylists(ctx context.Context, userID, profileID string) ([]abs.Playlist, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_playlist_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, description, cover_item, is_public, created_at, updated_at
		FROM abs_playlists
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY created_at DESC`,
		uid, profileArg(profileID),
	)
	if err != nil {
		return nil, fmt.Errorf("abs_playlist_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.Playlist, 0)
	for rows.Next() {
		var p abs.Playlist
		var uidScan int
		var profileScan, coverScan *string
		if err := rows.Scan(&p.ID, &uidScan, &profileScan, &p.Name, &p.Description, &coverScan, &p.IsPublic, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_playlist_store: list scan: %w", err)
		}
		p.UserID = strconv.Itoa(uidScan)
		if profileScan != nil {
			p.ProfileID = *profileScan
		}
		if coverScan != nil {
			p.CoverItem = *coverScan
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *ABSPlaylistStore) GetPlaylist(ctx context.Context, id string) (abs.Playlist, error) {
	var p abs.Playlist
	var uidScan int
	var profileScan, coverScan *string
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, description, cover_item, is_public, created_at, updated_at
		FROM abs_playlists WHERE id = $1`, id)
	if err := row.Scan(&p.ID, &uidScan, &profileScan, &p.Name, &p.Description, &coverScan, &p.IsPublic, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if err.Error() == "no rows in result set" {
			return abs.Playlist{}, abs.ErrNotFound
		}
		return abs.Playlist{}, fmt.Errorf("abs_playlist_store: get: %w", err)
	}
	p.UserID = strconv.Itoa(uidScan)
	if profileScan != nil {
		p.ProfileID = *profileScan
	}
	if coverScan != nil {
		p.CoverItem = *coverScan
	}
	return p, nil
}

// coverArg returns the value to bind for cover_item; empty string maps
// to NULL so the FK doesn't reject empty.
func coverArg(cover string) any {
	if cover == "" {
		return nil
	}
	return cover
}

func (s *ABSPlaylistStore) CreatePlaylist(ctx context.Context, p abs.Playlist) error {
	uid, err := strconv.Atoi(p.UserID)
	if err != nil {
		return fmt.Errorf("abs_playlist_store: invalid user id %q: %w", p.UserID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO abs_playlists (id, user_id, profile_id, name, description, cover_item, is_public)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, $7)`,
		p.ID, uid, profileArg(p.ProfileID), p.Name, p.Description, coverArg(p.CoverItem), p.IsPublic,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: create: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) UpdatePlaylist(ctx context.Context, p abs.Playlist) error {
	if _, err := s.Pool.Exec(ctx, `
		UPDATE abs_playlists
		   SET name = $2, description = $3, cover_item = $4, is_public = $5, updated_at = now()
		 WHERE id = $1`,
		p.ID, p.Name, p.Description, coverArg(p.CoverItem), p.IsPublic,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: update: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) DeletePlaylist(ctx context.Context, id string) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM abs_playlists WHERE id = $1`, id); err != nil {
		return fmt.Errorf("abs_playlist_store: delete: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) ListPlaylistItems(ctx context.Context, playlistID string) ([]abs.PlaylistItem, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT playlist_id, library_item_id, episode_id, position, added_at
		FROM abs_playlist_items
		WHERE playlist_id = $1
		ORDER BY position ASC`, playlistID)
	if err != nil {
		return nil, fmt.Errorf("abs_playlist_store: list-items: %w", err)
	}
	defer rows.Close()
	out := make([]abs.PlaylistItem, 0)
	for rows.Next() {
		var it abs.PlaylistItem
		if err := rows.Scan(&it.PlaylistID, &it.LibraryItemID, &it.EpisodeID, &it.Position, &it.AddedAt); err != nil {
			return nil, fmt.Errorf("abs_playlist_store: list-items scan: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *ABSPlaylistStore) AddPlaylistItem(ctx context.Context, playlistID, libraryItemID, episodeID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_playlist_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	// Position assignment: MAX(position)+1 inside the INSERT, one round-trip,
	// no read-before-write race.
	if _, err := tx.Exec(ctx, `
		INSERT INTO abs_playlist_items (playlist_id, library_item_id, episode_id, position)
		SELECT $1, $2, $3, COALESCE(MAX(position), 0) + 1
		  FROM abs_playlist_items WHERE playlist_id = $1
		ON CONFLICT (playlist_id, library_item_id, episode_id) DO NOTHING`,
		playlistID, libraryItemID, episodeID,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: add-item: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE abs_playlists SET updated_at = now() WHERE id = $1`, playlistID); err != nil {
		return fmt.Errorf("abs_playlist_store: bump-parent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_playlist_store: commit: %w", err)
	}
	return nil
}

func (s *ABSPlaylistStore) RemovePlaylistItem(ctx context.Context, playlistID, libraryItemID, episodeID string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abs_playlist_store: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		DELETE FROM abs_playlist_items
		WHERE playlist_id = $1 AND library_item_id = $2 AND episode_id = $3`,
		playlistID, libraryItemID, episodeID,
	); err != nil {
		return fmt.Errorf("abs_playlist_store: remove-item: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE abs_playlists SET updated_at = now() WHERE id = $1`, playlistID); err != nil {
		return fmt.Errorf("abs_playlist_store: bump-parent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("abs_playlist_store: commit: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Wire in `BuildABSHandler`**

In `internal/audiobooks/service.go`, after the `collectionStore` block, add:

```go
	var playlistStore abs.PlaylistStore
	if deps.Pool != nil {
		playlistStore = &ABSPlaylistStore{Pool: deps.Pool}
	}
```

In the `abs.New(abs.Dependencies{...})` call, after `CollectionStore`:

```go
		CollectionStore: collectionStore,
		PlaylistStore:   playlistStore,
```

- [ ] **Step 3: Register routes in `mountRoutes`**

In `internal/audiobooks/abs/handler.go`, after the collections routes (added in Task 8), append:

```go
			// Playlists — owner-gated CRUD with cross-user public reads,
			// realtime events on every mutation, batch endpoints.
			r.Get(prefix+"/playlists", h.handleListPlaylists)
			r.Post(prefix+"/playlists", h.handleCreatePlaylist)
			r.Get(prefix+"/playlists/{id}", h.handleGetPlaylist)
			r.Patch(prefix+"/playlists/{id}", h.handleUpdatePlaylist)
			r.Delete(prefix+"/playlists/{id}", h.handleDeletePlaylist)
			r.Post(prefix+"/playlists/{id}/item", h.handleAddPlaylistItem)
			r.Post(prefix+"/playlists/{id}/batch/add", h.handleBatchAddPlaylistItems)
			r.Post(prefix+"/playlists/{id}/batch/remove", h.handleBatchRemovePlaylistItems)
			r.Delete(prefix+"/playlists/{id}/item/{libraryItemId}", h.handleRemovePlaylistItem)
			r.Delete(prefix+"/playlists/{id}/item/{libraryItemId}/{episodeId}", h.handleRemovePlaylistEpisode)
```

- [ ] **Step 4: Build and test**

```bash
go build ./...
go test ./internal/audiobooks/... -count=1 | tail -10
```

Expected: clean build, all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs_playlist_store.go internal/audiobooks/service.go internal/audiobooks/abs/handler.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): wire ABSPlaylistStore + mount playlists routes

Adds the pgx-backed PlaylistStore impl, wires it into BuildABSHandler
when a Pool is present, and registers all ten playlists routes under
both /abs/api and /api prefixes inside the existing bearerAuth group.
AddPlaylistItem computes position = MAX+1 inside the INSERT (one
round-trip, no read-before-write race); both add and remove run in
transactions so the parent's updated_at bump is atomic.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 18: Full verification

This is a single-task, multi-step check the engineer runs before considering the work done. No code changes unless something fails — and only fix issues clearly attributable to this sub-project's files (everything in `internal/audiobooks/abs/`, `internal/audiobooks/abs_collection_store.go`, `internal/audiobooks/abs_playlist_store.go`, `internal/audiobooks/service.go` collections/playlists wiring, and the four migrations).

The live integration smoke (step 7 in the bookmark plan's verification task) is the operator's responsibility — flag it in the report and skip it.

- [ ] **Step 1: Full Go test suite**

```bash
go test ./... 2>&1 | grep -E '^(FAIL|ok|---)' | grep -v '^ok ' | head -20
```

Expected: empty output (no FAIL lines).

- [ ] **Step 2: Lint**

```bash
make lint 2>&1 | tail -30
```

Expected: clean. If `golangci-lint` is not on PATH, document that and continue (operator's setup concern, not a code concern). Findings in pre-existing unrelated files are out of scope — document them and move on.

- [ ] **Step 3: Frontend format check**

```bash
cd web && pnpm run format:check 2>&1 | tail -10 ; cd ..
```

Expected: clean (no bookmark plan files exist in the web tree, and this sub-project doesn't touch the frontend).

- [ ] **Step 4: Verify local paths**

```bash
make verify-local-paths 2>&1 | tail -10
```

Expected: clean.

- [ ] **Step 5: Frontend build (per project memory, catches what `tsc --noEmit` misses)**

```bash
cd web && pnpm run build 2>&1 | tail -10 ; cd ..
```

Expected: clean build (no bookmark frontend changes, but the merge gate requires this).

- [ ] **Step 6: Migration roundtrip on the local DB**

```bash
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_user_collections"
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_collection_items"
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_playlists"
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_playlist_items"
```

Expected: all four tables present with the schemas defined in Tasks 1 and 9.

Verify down + up cycle:

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/152_abs_playlist_items.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/151_abs_playlists.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/150_abs_collection_items.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/149_abs_user_collections.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/149_abs_user_collections.up.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/150_abs_collection_items.up.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/151_abs_playlists.up.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/152_abs_playlist_items.up.sql
```

Expected: clean throughout.

- [ ] **Step 7: Live smoke — operator step**

Per the spec §8.4, the operator runs the live curl smoke against a deployed server. Not run by the implementer. Note this in the final report.

- [ ] **Step 8: No commit unless something needed fixing**

If steps 1-6 all passed cleanly, the branch is ready for the operator's live smoke. If a fix was needed in a file owned by this sub-project, commit it with a descriptive `fix(audiobooks): ...` message. If the issue is in pre-existing unrelated files, document it and move on without fixing.

---

## Out of scope (deferred per spec §10)

- **Smart collections** — sub-project 3.
- **RSS feeds + listening stats** — sub-project 4.
- **Author / series detail endpoints** — separate small sub-project.
- **Continue-listening toggles** — separate small sub-project.
- **Reorder API for playlists** — future follow-up.
- **Cover-image hydration** — currently `coverPath` emits the bare `content_id`; a future enhancement resolves it through `DetailService.PresignURL`.
- **Episode hydration in playlists** — when the podcast-playlist sub-project lands.
- **Collection socket events** — Phase 2 of the parent spec.

