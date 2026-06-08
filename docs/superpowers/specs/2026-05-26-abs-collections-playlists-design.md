# ABS Collections + Playlists — Phase 1 Sub-Project 2

**Status:** Approved 2026-05-26. Ready for implementation plan.
**Scope:** Second sub-project of Phase 1 (per `2026-05-26-abs-implementation-fix-design.md`).
**Predecessor spec:** `docs/superpowers/specs/2026-05-26-abs-implementation-fix-design.md` §"Phase 1 — Feature surface completion" → Manual Collections + Playlists bullets.
**Sibling sub-project:** `docs/superpowers/specs/2026-05-26-abs-bookmarks-design.md` (sub-project 1; the bookmarks data model, in-memory test fakes, store wiring, and error-handling conventions established there are reused here without re-justification).

Commands in this document assume the repository root is the cwd.

## 1. Goal

Land manual user collections and ordered playlists on the silo audiobook surface so the official Audiobookshelf Android, iOS, and Plappa clients can create, browse, mutate, and share named groupings of audiobooks. "Manual collection" means a user-curated unordered set of audiobooks with a name + description (think: "Favorites", "To Read"). "Playlist" means an ordered queue with a cover image (think: "Wind-down listening"). Both are owned by a profile, optionally public to other users on the same silo instance.

## 2. Non-goals

- **No podcast episode hydration.** Playlist items accept and echo `episodeId`, but only audiobook items are looked up in `MediaStore`. A future sub-project will wire `internal/audiobooks/podcastfeed` hydration.
- **No reorder API.** Clients simulate reorder via remove+add (the re-added item lands at the end).
- **No smart collections.** Those are sub-project 3 (separate `query_def` storage + DSL evaluator).
- **No silo-web UI.** The web admin doesn't ship a collections/playlists view yet; ABS mobile clients render these.
- **No socket-server overhaul.** Playlists publish three existing-pattern events; collections publish none. Full socket parity is Phase 2 of the parent spec.

## 3. Architecture

Two REST surfaces sharing a uniform shape, mounted under `/abs/api/*` and `/api/*` inside the existing `bearerAuth` group.

- **HTTP layer:**
  - `internal/audiobooks/abs/collections_handler.go` — 7 routes (list, create, get, update, delete, add item, remove item).
  - `internal/audiobooks/abs/playlists_handler.go` — 9 routes (list, create, get, update, delete, add single item, batch add, batch remove, remove single item; remove-episode variant uses two URL params).
- **Storage layer:**
  - New interfaces in `internal/audiobooks/abs/collections.go` and `playlists.go` (one file each, parallel to `bookmarks.go`):
    - `CollectionStore` — `ListUserCollections / GetCollection / CreateCollection / UpdateCollection / DeleteCollection / ListCollectionItems / AddCollectionItem / RemoveCollectionItem`.
    - `PlaylistStore` — `ListUserPlaylists / GetPlaylist / CreatePlaylist / UpdatePlaylist / DeletePlaylist / ListPlaylistItems / AddPlaylistItem / RemovePlaylistItem`.
  - Concrete pgx impls in `internal/audiobooks/abs_collection_store.go` and `abs_playlist_store.go` (parallel to `abs_bookmark_store.go`).
- **Migrations** (paired up/down per `CLAUDE.md`):
  - `149_abs_user_collections`, `150_abs_collection_items`, `151_abs_playlists`, `152_abs_playlist_items`.
- **Socket events:** Playlists publish `playlist_added` / `playlist_updated` / `playlist_removed` (continuum-canonical event names). Collections publish nothing. All publishes go through the existing nil-safe `Handler.publish(...)` wrapper.
- **Service wiring:** Both new stores wired in `BuildABSHandler` (mirrors the BookmarkStore wiring landed in sub-project 1).

## 4. Endpoint surface

All routes mounted under both `/abs/api/*` and `/api/*` inside `bearerAuth`. Success status code is **200 OK** in every row of the tables below, except where the `Returns` column explicitly says **204 No Content** (DELETE collection / DELETE playlist themselves; the item-mutation DELETEs return 200 with the parent's full-shape body, matching continuum).

### 4.1 Collections (7 routes)

| Verb | Path | Body | Returns |
|---|---|---|---|
| GET | `/collections` | — | `{"collections": [Collection list-shape]}` |
| POST | `/collections` | `{name, description, isPublic?}` | Collection full-shape |
| GET | `/collections/{id}` | — | Collection full-shape when caller is owner, OR when `is_public=true`; otherwise 404 |
| PATCH | `/collections/{id}` | `{name?, description?, isPublic?}` | Collection full-shape |
| DELETE | `/collections/{id}` | — | 204 No Content |
| POST | `/collections/{id}/book/{bookId}` | — | Collection full-shape (with updated `books[]`) |
| DELETE | `/collections/{id}/book/{bookId}` | — | Collection full-shape |

### 4.2 Playlists (9 routes)

| Verb | Path | Body | Returns |
|---|---|---|---|
| GET | `/playlists` | — | `{"playlists": [Playlist list-shape]}` |
| POST | `/playlists` | `{name, description, cover_item?, isPublic?}` | Playlist full-shape |
| GET | `/playlists/{id}` | — | Playlist full-shape when caller is owner, OR when `is_public=true`; otherwise 404 |
| PATCH | `/playlists/{id}` | `{name?, description?, cover_item?, isPublic?}` | Playlist full-shape |
| DELETE | `/playlists/{id}` | — | 204 No Content |
| POST | `/playlists/{id}/item` | `{libraryItemId, episodeId?}` | Playlist full-shape |
| POST | `/playlists/{id}/batch/add` | `{items: [{libraryItemId, episodeId?}]}` | Playlist full-shape |
| POST | `/playlists/{id}/batch/remove` | `{items: [{libraryItemId, episodeId?}]}` | Playlist full-shape |
| DELETE | `/playlists/{id}/item/{libraryItemId}` | — | Playlist full-shape |
| DELETE | `/playlists/{id}/item/{libraryItemId}/{episodeId}` | — | Playlist full-shape |

### 4.3 Wire shape — Collection

**List shape (no `books[]`):**

```json
{
  "id": "01HXXX",
  "userId": "1",
  "name": "Favorites",
  "description": "My top picks",
  "isPublic": false,
  "lastUpdate": 1779786284823,
  "createdAt": 1779786284823
}
```

**Full shape (with `books[]`):**

```json
{
  "id": "01HXXX",
  "userId": "1",
  "name": "Favorites",
  "description": "My top picks",
  "isPublic": false,
  "lastUpdate": 1779786284823,
  "createdAt": 1779786284823,
  "books": [
    {
      "id": "126887...",
      "libraryId": "9",
      "media": { "metadata": { "title": "Book Title", "authors": [...] } }
    }
  ]
}
```

All seven top-level keys always present (`description` is `""` when unset, never omitted — fixes the continuum-reference bug where description was always emitted as empty regardless of stored value). `books[]` is always an array (possibly empty) on the full shape; omitted on the list shape.

### 4.4 Wire shape — Playlist

**List shape (no `items[]`):**

```json
{
  "id": "01HXXX",
  "userId": "1",
  "name": "My Queue",
  "description": "",
  "isPublic": false,
  "coverPath": "126887...",
  "createdAt": 1779786284823,
  "lastUpdate": 1779786284823
}
```

**Full shape (with `items[]`):**

```json
{
  "id": "01HXXX",
  "userId": "1",
  "name": "My Queue",
  "description": "",
  "isPublic": false,
  "coverPath": "126887...",
  "createdAt": 1779786284823,
  "lastUpdate": 1779786284823,
  "items": [
    { "libraryItemId": "126887...", "position": 0, "title": "Book Title", "libraryId": "9" },
    { "libraryItemId": "podcast-x",  "episodeId": "ep-1", "position": 1 }
  ]
}
```

`coverPath` is emitted when set; omitted when unset. `description` always present. Items always sorted by `position` ASC. Entries with `episodeId` set are emitted with the field; audiobook entries omit it. Audiobook items hydrate `title` + `libraryId` via `MediaStore.GetAudiobookByID`; episode items emit the bare reference.

### 4.5 Hydration semantics

- `MediaStore.GetAudiobookByID(libraryItemId)` is called per book on the **full** shape only (the list shape skips it to keep response sizes small).
- On miss (item deleted between collection/playlist mutation and list-fetch), the entry degrades to `{id, libraryId}` only — clients render a placeholder.
- `libraryId` resolution: same `resolveDefaultLibrary` helper from `items_handler.go` (returns numeric library ID as decimal string).

### 4.6 Socket events

| Event | When | Payload |
|---|---|---|
| `playlist_added` | After POST `/playlists` | `{id, name}` |
| `playlist_updated` | After PATCH or any item mutation (single or batch) | `{id}` |
| `playlist_removed` | After DELETE `/playlists/{id}` | `{id}` |

Collections fire no socket events (matches continuum; Phase 2 of the parent spec covers full event parity).

## 5. Data model

### 5.1 Migration 149 — `abs_user_collections`

`migrations/149_abs_user_collections.up.sql`:

```sql
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

`migrations/149_abs_user_collections.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_user_collections_user_profile_idx;
DROP TABLE IF EXISTS public.abs_user_collections;
```

### 5.2 Migration 150 — `abs_collection_items`

`migrations/150_abs_collection_items.up.sql`:

```sql
CREATE TABLE IF NOT EXISTS public.abs_collection_items (
    collection_id   text NOT NULL REFERENCES public.abs_user_collections(id) ON DELETE CASCADE,
    library_item_id text NOT NULL REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    added_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collection_id, library_item_id)
);

CREATE INDEX IF NOT EXISTS abs_collection_items_library_item_idx
    ON public.abs_collection_items (library_item_id);
```

`migrations/150_abs_collection_items.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_collection_items_library_item_idx;
DROP TABLE IF EXISTS public.abs_collection_items;
```

### 5.3 Migration 151 — `abs_playlists`

`migrations/151_abs_playlists.up.sql`:

```sql
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

`migrations/151_abs_playlists.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_playlists_user_profile_idx;
DROP TABLE IF EXISTS public.abs_playlists;
```

### 5.4 Migration 152 — `abs_playlist_items`

`migrations/152_abs_playlist_items.up.sql`:

```sql
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

`migrations/152_abs_playlist_items.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_playlist_items_playlist_position_idx;
DROP TABLE IF EXISTS public.abs_playlist_items;
```

### 5.5 Schema rationale

- **`is_public boolean`** — cross-user-public read semantics. `false` default. List endpoints never expose other users' rows; GET-by-id allows non-owner reads only when `is_public = true`.
- **Collection items: FK + CASCADE on `library_item_id`** — when a book is deleted from the library, drop it from all collections (clean orphan-free state). Same pattern as migration 143's `abs_playback_sessions.content_id`.
- **Collection items: composite PK on `(collection_id, library_item_id)`** — enforces "a book appears at most once in a collection" without a synthetic ID.
- **Playlist items: `library_item_id` NOT FK'd** — collections enforce one-shot membership; playlists are ordered queues that may legitimately reference items the user hasn't bookmarked. Decoupling lets a future migration FK it when episode support lands properly. Today: handler validates via MediaStore (same pattern as bookmarks).
- **Playlist items: `episode_id text NOT NULL DEFAULT ''`** — empty string for audiobook items, populated for podcast episodes. Empty-string-default lets `(playlist_id, library_item_id, episode_id)` be a clean unique key without COALESCE.
- **Playlist items: no synthetic ID** — `(playlist_id, library_item_id, episode_id)` is naturally unique; `position` is sort hint (gaps allowed when items are removed).
- **`cover_item REFERENCES media_items(content_id) ON DELETE SET NULL`** — playlist survives cover-item deletion; cover gracefully becomes nothing.

### 5.6 Go models

```go
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

type CollectionItem struct {
    CollectionID  string
    LibraryItemID string
    AddedAt       time.Time
}

type Playlist struct {
    ID          string
    UserID      string
    ProfileID   string
    Name        string
    Description string
    CoverItem   string  // empty when unset
    IsPublic    bool
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type PlaylistItem struct {
    PlaylistID    string
    LibraryItemID string
    EpisodeID     string  // empty for audiobook items
    Position      int
    AddedAt       time.Time
}
```

Handlers convert these to the wire shape (camelCase JSON keys, timestamps as `UnixMilli()`).

## 6. Storage contract

### 6.1 `CollectionStore` (in `internal/audiobooks/abs/collections.go`)

```go
type CollectionStore interface {
    ListUserCollections(ctx context.Context, userID, profileID string) ([]Collection, error)
    GetCollection(ctx context.Context, id string) (Collection, error)
    CreateCollection(ctx context.Context, c Collection) error
    UpdateCollection(ctx context.Context, c Collection) error
    DeleteCollection(ctx context.Context, id string) error
    ListCollectionItems(ctx context.Context, collectionID string) ([]CollectionItem, error)
    AddCollectionItem(ctx context.Context, collectionID, libraryItemID string) error
    RemoveCollectionItem(ctx context.Context, collectionID, libraryItemID string) error
}
```

### 6.2 `PlaylistStore` (in `internal/audiobooks/abs/playlists.go`)

```go
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
```

### 6.3 Behavior

- **List ordering:** Collections list ordered by `created_at DESC`. Playlists list ordered by `created_at DESC`. Playlist items ordered by `position ASC`. Collection items ordered by `added_at ASC`. All sorting happens in SQL, not in Go.
- **Empty results:** All List methods return an empty slice (never nil) when no rows match.
- **`GetCollection` / `GetPlaylist`:** Return `ErrNotFound` when absent. NO owner check — caller authorizes via the response's `UserID` and `IsPublic`.
- **`AddPlaylistItem` position assignment:** Single SQL statement computing `position = COALESCE(MAX(position), 0) + 1 WHERE playlist_id = $1` inside the INSERT. No read-before-write race.
- **`AddCollectionItem` / `AddPlaylistItem` idempotency:** `INSERT ... ON CONFLICT (...) DO NOTHING`. Re-adding an existing tuple is a silent no-op (no error, no row mutation, no `updated_at` bump beyond what the handler does separately).
- **`RemoveCollectionItem` / `RemovePlaylistItem` idempotency:** Return `nil` on no-match.
- **Profile mapping:** Handlers pass `a.ProfileID` straight through (empty = primary). Store uses `profileArg` helper (returns `nil` for empty, the string otherwise). `COALESCE(profile_id, '00000000-...'::uuid)` in WHERE clauses on both column and bind value, mirroring the BookmarkStore convention.
- **`updated_at` bump on item mutations:** Both `Add*Item` and `Remove*Item` execute the item mutation and an `UPDATE abs_user_collections SET updated_at = now() WHERE id = $1` (or `abs_playlists`) in the same transaction. Wire field `lastUpdate` reflects this.
- **Cascade cleanup:** Deleting a collection drops all its `abs_collection_items` via FK CASCADE. Deleting a playlist drops all its `abs_playlist_items`. Deleting a `media_items` row drops it from all `abs_collection_items` (via FK CASCADE); playlists surface stale references on the next List, where the handler degrades to bare-id entries.

### 6.4 Cross-user public read

Handler pattern after `Get*`:

```go
c, err := store.GetCollection(ctx, id)
if errors.Is(err, ErrNotFound) || (c.UserID != a.UserID && !c.IsPublic) {
    http.Error(w, "collection not found", http.StatusNotFound)
    return
}
```

Same pattern for playlists. The condition collapses unknown-id and not-authorized into one branch so existence-leak vectors don't open up.

## 7. Error model

| Condition | Status | Body |
|---|---|---|
| Missing/invalid bearer | 401 | handled by `bearerAuth` middleware |
| Body decode failure (POST/PATCH) | 400 | `invalid body` |
| Required field missing (POST: `name`; POST item: `libraryItemId`) | 400 | `<field> required` |
| Unknown collection/playlist on GET (owner or not) | 404 | `collection not found` / `playlist not found` |
| Non-owner GET on private collection/playlist | 404 | same body — no existence leak |
| Non-owner PATCH/DELETE/item-mutation | 404 | same body |
| `library_item_id` not in MediaStore (audiobook add-item) | 404 | `item not found` |
| Store insert/update/delete fails | 500 | sanitized `<collection\|playlist> persist failed`; err logged via `slog.Error` |
| List fetch fails after a successful mutation | 200 + best-effort response (in-memory state) + `slog.Warn` | mutation already committed; falling back beats 500 |

### 7.1 Cross-cutting

- **Anti-enumeration.** All non-owner-or-private paths funnel to the same `<type> not found` 404 (never 403, never "private collection"). Indistinguishable from real not-found.
- **Item validation on add.** `handleAddCollectionBook` and `handleAddPlaylistItem` call `MediaStore.GetAudiobookByID(libraryItemID)` before touching the store. Catches typos and orphan refs early. **Exception:** `handleAddPlaylistItem` with non-empty `episodeId` SKIPS item validation (audiobook-only hydration scope; podcast episodes are stored opaque-id-style).
- **Body size limit.** All POST/PATCH wrap `r.Body` in `io.LimitReader(r.Body, 1<<20)` before `json.NewDecoder`. Same as bookmarks.
- **Batch endpoints.** `POST /playlists/{id}/batch/add` and `/batch/remove` tolerate per-item failures silently (matching continuum's `_ = h.store.Add...`). Total failure (e.g., body decode error) is still a 400. Item validation on the batch path: each item validated individually; failed validations skipped with `slog.Debug` (no 404 — the operation as a whole succeeds with the remaining items).
- **Socket event on batch mutation.** A batch add/remove fires exactly one `playlist_updated` event, regardless of how many items succeeded or failed. Clients re-render from the response.
- **Socket publish never fails the request.** `h.publish(...)` is nil-safe and fire-and-forget; if the Publisher is unwired the response still completes.

## 8. Testing

### 8.1 Unit tests — `collections_handler_test.go`

In-memory fake `memCollectionStore` (parallel to `memBookmarkStore`). Reuses `stubMediaStore` and `recordingPublisher` from `bookmarks_handler_test.go`. The `dispatchBookmark` helper is generalized in-place (or a sibling `dispatchABS` is added) so the same wiring (URL params + `ctxAuth` injection) drives both new surfaces.

| Test | Asserts |
|---|---|
| `Collection_Create_ReturnsFullShape` | POST `{name:"x"}` → 200, response carries all 7 top-level keys + empty `books[]`, ID is a ULID |
| `Collection_Create_NameRequired_400` | POST `{description:"only"}` → 400 |
| `Collection_List_ReturnsWrappedEnvelope` | GET → 200, body is `{"collections":[…]}`, list-shape omits `books` |
| `Collection_List_DoesNotLeakOtherUsers` | User 1 creates; User 2 GETs → empty list |
| `Collection_Get_Owner_ReturnsFullShape` | POST → owner GET → 200 + `books[]` |
| `Collection_Get_NonOwner_Public_OK` | User 1 creates with `isPublic:true`; User 2 GET → 200 |
| `Collection_Get_NonOwner_Private_404` | User 1 creates (default private); User 2 GET → 404 |
| `Collection_Patch_OwnerUpdatesNameAndDescription` | POST → PATCH `{name:"y", description:"d"}` → 200, fields updated, `lastUpdate` advanced |
| `Collection_Patch_NonOwner_404` | User 2 PATCH on User 1's collection → 404 (no existence leak) |
| `Collection_Delete_OwnerRemovesItAndItems` | POST + add book → DELETE → 204; subsequent GET → 404; items table empty |
| `Collection_Delete_NonOwner_404` | User 2 DELETE → 404, User 1's collection still present |
| `Collection_AddBook_Owner_HydratesInResponse` | POST + add book → 200 + `books[]` contains the entry with `media.metadata.title` |
| `Collection_AddBook_Idempotent` | Add same book twice → 200 both times, `books[]` length stays at 1 |
| `Collection_AddBook_UnknownItem_404` | Add a non-existent item → 404 `item not found` |
| `Collection_RemoveBook_Idempotent` | Remove book not in collection → 200, `books[]` unchanged |
| `Collection_ProfileIsolation` | Profile A creates; Profile B's list returns empty |
| `Collection_Envelope_HasRequiredKeys` | Marshal-test: 7 top-level keys present even when description and books are empty |

### 8.2 Unit tests — `playlists_handler_test.go`

In-memory fake `memPlaylistStore`.

| Test | Asserts |
|---|---|
| `Playlist_Create_ReturnsFullShape` | POST `{name:"x", description:"d", isPublic:true}` → 200, all 8 keys present, ID is ULID |
| `Playlist_Create_NameRequired_400` | POST `{description:"only"}` → 400 |
| `Playlist_Create_FiresPlaylistAddedEvent` | POST → `recordingPublisher` captures `playlist_added` with `{id, name}` |
| `Playlist_List_WrappedEnvelope` | GET → 200, body `{"playlists":[…]}`, list-shape omits `items` |
| `Playlist_AddItem_AppendsAtNextPosition` | POST + add 3 items → response `items[]` positions are 1,2,3 in insertion order |
| `Playlist_AddItem_Idempotent` | Add same `(libraryItemId, episodeId)` twice → list length 1 |
| `Playlist_AddItem_AudiobookHydrates` | Add audiobook item → response item has `title` |
| `Playlist_AddItem_Episode_AcceptsAndEchoes` | Add `{libraryItemId, episodeId:"ep-1"}` → response item has `episodeId`, no `title` (un-hydrated by design) |
| `Playlist_AddItem_UnknownAudiobook_404` | Add non-existent audiobook → 404; episode adds skip validation, so episode-only adds succeed |
| `Playlist_BatchAdd_TolerantOfPartialFailures` | Batch of `[valid, invalid, valid]` → 200, response has 2 items, no 404 |
| `Playlist_BatchAdd_FiresOneUpdatedEvent` | Batch add → single `playlist_updated` event (not one per item) |
| `Playlist_BatchRemove` | Seed 3 items, batch-remove 2 → response items[] length 1 |
| `Playlist_RemoveItem_Single` | DELETE /item/{libraryItemId} → 200, item gone |
| `Playlist_RemoveItem_WithEpisode` | DELETE /item/{libraryItemId}/{episodeId} → 200, episode-keyed item removed; audiobook-keyed item with same libraryItemId still present |
| `Playlist_Patch_UpdatesCover` | PATCH `{cover_item:"id"}` → 200, `coverPath` updated; `playlist_updated` event fires |
| `Playlist_Delete_FiresPlaylistRemovedEvent` | DELETE → 204 + `playlist_removed` event |
| `Playlist_Get_NonOwner_Public_OK` | User 1 creates `isPublic:true`; User 2 GET → 200 |
| `Playlist_Get_NonOwner_Private_404` | Private playlist; non-owner GET → 404 |
| `Playlist_NonOwner_Mutation_404` | User 2 add-item / remove-item / PATCH / DELETE → 404 each, User 1's playlist intact |
| `Playlist_ProfileIsolation` | Profile A creates; Profile B's list returns empty |
| `Playlist_Envelope_HasRequiredKeys` | Marshal-test: 8 top-level keys present; `coverPath` omitted when empty |

### 8.3 Shared envelope tests

One marshal test per surface (`collections_envelope_test.go`, `playlists_envelope_test.go`) asserting the wire shape end-to-end. Parallel to `bookmarks_envelope_test.go`.

### 8.4 Live integration smoke (post-deploy, operator)

```bash
TOKEN=$(curl ... /login | jq -r .accessToken)
ITEM=$(curl ... /api/libraries/9/items?limit=1 | jq -r .results[0].id)

# Collections
COLL=$(curl -X POST -d '{"name":"smoke"}' ... /api/collections | jq -r .id)
curl -X POST ... /api/collections/$COLL/book/$ITEM
curl ... /api/collections/$COLL                     # books[] contains $ITEM
curl -X DELETE ... /api/collections/$COLL/book/$ITEM
curl -X DELETE ... /api/collections/$COLL           # 204

# Playlists
PL=$(curl -X POST -d '{"name":"queue"}' ... /api/playlists | jq -r .id)
curl -X POST -d "{\"libraryItemId\":\"$ITEM\"}" ... /api/playlists/$PL/item
curl ... /api/playlists/$PL                         # items[] contains $ITEM at position 1
curl -X DELETE ... /api/playlists/$PL/item/$ITEM
curl -X DELETE ... /api/playlists/$PL               # 204
```

Expected: each call returns the wire shape from §4; `playlist_added` / `_updated` / `_removed` events appear on the user's Socket.io channel.

### 8.5 Explicitly out of scope

- **No real-Postgres SQL test.** Same posture as bookmarks (§8.4 of that spec): in-memory fakes cover the semantics; CI does not run Postgres for the ABS subdomain.
- **No episode hydration.** Episode IDs accepted and echoed but not resolved to podcast metadata. A future podcast-playlist sub-project will plug in `podcastfeed` hydration.
- **No reorder API.** Future follow-up.
- **No two-user smoke.** Cross-user-public visibility is unit-tested but the operator smoke uses one user.

## 9. Risks & open questions

- **Position gaps after remove.** Removing the middle item from a playlist leaves a `position` gap (1, 3, 4 instead of 1, 2, 3). Clients sort by position; gaps are harmless. If a future reorder API lands, it'll compact positions in a single statement.
- **Cross-user public + profile scoping interaction.** A public collection owned by `(user A, profile primary)` is visible to user B regardless of B's active profile. The collection's `userId` field discloses A's user identity. Acceptable for v1; if A wanted to hide who owns the collection, that's a separate feature.
- **No migration rollback test.** Down migrations are `DROP INDEX + DROP TABLE` — low risk, same posture as bookmarks. Not a blocker.
- **`cover_item` references a `media_items.content_id` but the playlist owner may not have access to that library** — silo's library visibility isn't enforced at the schema level. The cover image hydrator (when added) will need to check visibility. For now, the field round-trips opaquely.
- **Concurrent `AddPlaylistItem` position collision.** Two parallel appends compute `MAX(position)+1` concurrently and may both produce the same value. The UNIQUE constraint on `(playlist_id, library_item_id, episode_id)` prevents collisions for the SAME item, but distinct items could land at the same position. Acceptable — clients tolerate equal positions and break ties by insertion order. A SERIALIZABLE transaction would close the race; not warranted for a low-traffic UX surface.

## 10. Out-of-scope follow-ups

These are intentionally deferred — they belong to later Phase 1 sub-projects or Phase 2:

- **Smart collections** — sub-project 3 (`abs_smart_collections` + `query_def` JSONB + DSL evaluator).
- **RSS feeds** — sub-project 4 (`abs_rss_feeds`).
- **Listening stats** — sub-project 4 (aggregations on `abs_playback_sessions`).
- **Author / series detail endpoints** — small sub-project, separate.
- **Continue-listening toggles** — small sub-project, separate.
- **Reorder API for playlists** — compaction + drag-reorder UX. Probably warrants its own design pass.
- **Cover-image hydration** — currently the wire emits `coverPath: <content_id>` opaquely. A future enhancement resolves it through `DetailService.PresignURL` to a fully-qualified cover URL.
- **Episode hydration in playlists** — when the podcast-playlist sub-project lands.
- **Collection socket events** — Phase 2 of the parent spec.

## 11. References

- Spec parent: `docs/superpowers/specs/2026-05-26-abs-implementation-fix-design.md`.
- Sibling sub-project spec: `docs/superpowers/specs/2026-05-26-abs-bookmarks-design.md`.
- Wire-shape reference: `continuum-plugin-audiobooks/internal/abs/collections_handler.go` and `playlists_handler.go` (canonical, diff against silo's adaptation before flagging response-shape concerns).
- Client wire usage: `audiobookshelf-app/components/...` Collections and Playlists modal/page components.
