# ABS Bookmarks — Phase 1 Sub-Project 1

**Status:** Approved 2026-05-26. Ready for implementation plan.
**Scope:** First sub-project of Phase 1 (per `2026-05-26-abs-implementation-fix-design.md`).
**Predecessor spec:** `docs/superpowers/specs/2026-05-26-abs-implementation-fix-design.md` §"Phase 1 — Feature surface completion" → Bookmarks bullet.

Commands in this document assume the repository root is the cwd.

## 1. Goal

Let an Audiobookshelf mobile client (official Android, official iOS, and well-behaved third parties such as Plappa) create, edit, and delete in-book bookmarks against a silo audiobook library, and see those bookmarks sync in real time to the user's other connected devices.

## 2. Non-goals

- No ebook bookmarks (audiobook only — matches ABS's own surface).
- No bulk bookmark import / export, no merge-by-title, no fuzzy time matching.
- No `/api/me/bookmarks` aggregate-list endpoint. The Android client only calls per-item endpoints; adding the aggregate would be dead code today.
- No client UI work — silo's web admin doesn't ship a bookmarks view yet, and the mobile clients already render bookmarks from these endpoints.
- No socket-server overhaul; Phase 1 just publishes events through the existing `Handler.publish` wrapper. Phase 2 covers the full socket event surface.

## 3. Architecture

Three ABS-compatible REST handlers + one socket event family + one migration.

- **HTTP layer:** new `internal/audiobooks/abs/bookmarks_handler.go` alongside existing `items_handler.go` / `progress.go`. Handler-method pattern mirrors `handleSetItemProgress`. Routes registered under both `/abs/api` and `/api` prefixes inside the existing bearerAuth group.
- **Storage layer:** new `BookmarkStore` interface in `internal/audiobooks/abs/bookmarks.go` (split out rather than crowd `progress.go`) plus a concrete `ABSBookmarkStore` Postgres implementation in `internal/audiobooks/abs_bookmark_store.go` — naming mirrors `abs_playback_session_store.go`. Wiring lives in `internal/audiobooks/service.go`, alongside the other store constructions in `BuildABSHandler`.
- **Migration:** `migrations/148_abs_bookmarks.up.sql` + `.down.sql`.
- **Socket events:** the existing nil-safe `Handler.publish(userID, event, payload)` wrapper. Each handler fires `user_updated` with a `reason` discriminator on success — three lines per handler. No new event publisher code.

## 4. Endpoint surface

All mounted at both `/abs/api/*` and `/api/*` inside the bearerAuth group.

| Verb | Path | Body | Returns |
|---|---|---|---|
| POST | `/me/item/{itemId}/bookmark` | `{title, time}` | full item bookmark list |
| PATCH | `/me/item/{itemId}/bookmark` | `{title, time}` | full item bookmark list (upserts at `time`) |
| DELETE | `/me/item/{itemId}/bookmark/{time}` | — | full item bookmark list (idempotent) |

All three handlers fire `user_updated` on success with the relevant `reason`:

| Handler | `reason` value |
|---|---|
| Create | `bookmark_created` |
| Update | `bookmark_updated` |
| Delete | `bookmark_deleted` |

Socket event payload shape:

```json
{
  "reason": "bookmark_created",
  "bookmark": { "id": "01K…", "libraryItemId": "126887…", "time": 1234.5, "title": "Cliffhanger", "createdAt": 1779786284823, "updatedAt": 1779786284823 }
}
```

For `bookmark_deleted`, `bookmark.title` is the pre-delete title; for `bookmark_created` / `bookmark_updated` it is the freshly-persisted row.

### 4.1 Wire shape

```json
{
  "id": "01KSHR…",
  "libraryItemId": "126887440911695876",
  "time": 1234.5,
  "title": "Good cliffhanger",
  "createdAt": 1779786284823,
  "updatedAt": 1779786284823
}
```

- All six fields always present (never `omitempty`).
- `time` is fractional seconds (`double`).
- `createdAt` / `updatedAt` are JS-epoch milliseconds (`int64`), matching every other ABS handler's timestamp emission.
- Empty title serializes as `"title": ""`.

### 4.2 Why no GET-list endpoint

The canonical continuum plugin handler, the Android `BookmarksModal.vue`, booklore-ng's per-item route, and the official ABS server all return the full item bookmark list from the mutating endpoints. A separate `GET /me/item/{id}/bookmark` would be dead code.

## 5. Data model

### 5.1 Migration 148_abs_bookmarks

`migrations/148_abs_bookmarks.up.sql`:

```sql
CREATE TABLE abs_bookmarks (
    id              text PRIMARY KEY,
    user_id         integer NOT NULL,
    profile_id      uuid,
    library_item_id text NOT NULL,
    time_seconds    double precision NOT NULL,
    title           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX abs_bookmarks_user_profile_item_time_uniq
    ON abs_bookmarks (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid),
        library_item_id,
        time_seconds
    );

CREATE INDEX abs_bookmarks_user_item_idx
    ON abs_bookmarks (user_id, library_item_id);
```

`migrations/148_abs_bookmarks.down.sql`:

```sql
DROP TABLE abs_bookmarks;
```

### 5.2 Schema rationale

- **`id` text (ULID), not UUID** — matches every other ABS surface ID emitted by silo (`abs_tokens.id`, `abs_playback_sessions.id`). Clients get a consistent shape; ULIDs are also lexicographically sortable by creation time, which simplifies debug output.
- **`profile_id` nullable, with COALESCE sentinel in the unique index** — silo's primary-profile convention is `NULL` profile. Postgres treats raw `NULL` as distinct for uniqueness, so the COALESCE-to-fixed-UUID trick collapses NULL to a single bucket per user, which is what makes "one bookmark per timestamp per profile" work.
- **Unique on `(user, profile, item, time)`** — encodes the "one bookmark per timestamp" invariant directly, which is what makes `Upsert(...,time,title)` the natural primitive: PATCH at an existing time updates the title; PATCH at a new time creates.
- **Secondary index on `(user_id, library_item_id)`** — covers the hot-path `List` query that runs after every mutation. Avoids a sort or full scan when a user has many bookmarks across many items.
- **`title` NOT NULL DEFAULT ''** — Android lets users create quick bookmarks without a title; empty string stores cleanly. Avoids null-guard branches in client iteration code.

### 5.3 Go model

```go
type Bookmark struct {
    ID            string    // ULID
    LibraryItemID string
    Time          float64   // fractional seconds
    Title         string
    CreatedAt     time.Time
    UpdatedAt     time.Time
}
```

Handlers convert to the wire shape (camelCase JSON keys, timestamps as `UnixMilli()`).

## 6. Storage contract

```go
type BookmarkStore interface {
    // List returns all bookmarks for (user, profile, item) ordered by time
    // ASC. Empty slice (never nil) when none exist.
    List(ctx context.Context, userID, profileID, itemID string) ([]Bookmark, error)

    // Upsert inserts a bookmark or updates the title at the exact
    // (user, profile, item, time) tuple. ID is generated on insert and
    // preserved on update. Returns the resulting row.
    Upsert(ctx context.Context, userID, profileID, itemID string, time float64, title string) (Bookmark, error)

    // Delete removes the bookmark at (user, profile, item, time). Returns
    // nil when no row matched — DELETE is idempotent (a UX convenience,
    // not a 404 surface).
    Delete(ctx context.Context, userID, profileID, itemID string, time float64) error
}
```

**Behavior:**

- `Upsert` uses a single `INSERT ... ON CONFLICT (user_id, COALESCE(profile_id,'…'), library_item_id, time_seconds) DO UPDATE SET title = EXCLUDED.title, updated_at = now() RETURNING *` — one round-trip, no read-then-write race.
- `List` sort happens in SQL (`ORDER BY time_seconds ASC`), not in Go.
- Profile mapping: handlers pass `a.ProfileID` (string, empty = primary) straight through to the store; the store maps empty → NULL on writes and uses the same COALESCE-to-sentinel-UUID trick on reads. Mirrors the pattern already used by `abs_session_store.go` (`*string` round-trip) — pick whichever pgx approach reads cleanest in the impl, just keep the empty-as-primary convention consistent with the rest of `internal/audiobooks/`.
- Time precision: exact float64 equality on PATCH/DELETE-at-time. Plappa and the official client always echo back the exact value they received, so epsilon comparison would be complexity nothing exercises.
- `Delete` on a non-existent row returns `nil`. The caller still re-fetches the list and returns it, so double-tapped deletes produce consistent state.

## 7. Error model

| Condition | Status | Body |
|---|---|---|
| Missing/invalid bearer | 401 | handled by `bearerAuth` middleware |
| Body decode failure (POST/PATCH) | 400 | `invalid body` |
| `time` missing or NaN | 400 | `time required` |
| `itemId` not in `MediaStore` | 404 | `item not found` |
| Store upsert / mutate fails | 500 | sanitized `bookmark persist failed`; err logged via `slog.Error` |
| Store delete fails (DB error, not missing row) | 500 | sanitized `bookmark delete failed` |
| List fetch fails after a successful mutation | 200 + empty `[]` + `slog.Warn` | mutation already committed; returning empty list beats 500 |

**Cross-cutting:**

- **No leaked-existence side channel.** DELETE for a bookmark belonging to another user/profile returns 200 with the caller's list (which doesn't contain the target) — indistinguishable from DELETE for a non-existent bookmark. No 403, no enumeration vector.
- **Item validation.** Validate `itemId` via `MediaStore.GetAudiobookByID` before touching the store (same pattern as `handleItem`). Catches typos and deleted items early; avoids orphan bookmark rows whose item no longer exists.
- **Body size limit.** Request body wrapped in `io.LimitReader(r.Body, 1<<20)` — same 1 MiB cap as `handleStandaloneLogin`.
- **Socket publish never fails the request.** `h.publish(...)` is nil-safe and fire-and-forget; if the Publisher is unwired the response still completes.

## 8. Testing

### 8.1 Unit tests — `bookmarks_handler_test.go`

In-memory fake `BookmarkStore` (mirrors `memTokenStore` / `fakePlaybackSessionStore` pattern already used in this package).

| Test | Asserts |
|---|---|
| `Create_NewBookmark_ReturnsListContainingIt` | POST → 200, response is `[{id, time, title, …}]`, list length 1, fields populated |
| `Create_TwoAtDifferentTimes_ListOrderedByTime` | POST × 2 (times 100 and 50), list returned `[{time:50},{time:100}]` |
| `Upsert_SameTime_UpdatesTitleNoDuplicate` | POST then PATCH at same time with new title → list length 1, title updated, id preserved |
| `Delete_ExistingBookmark_RemovedFromList` | POST then DELETE same time → 200, list empty |
| `Delete_NonExistentTime_IdempotentReturnsEmptyList` | DELETE without prior POST → 200, empty list |
| `Delete_OtherUserBookmark_NoOpAndNoExistenceLeak` | seed bookmark for user B, user A DELETEs same item+time → 200, user B's bookmark untouched |
| `ProfileIsolation_BookmarksScopedPerProfile` | (user, profile A) POST → (user, profile B) List returns empty |
| `MissingItem_404` | POST against unknown itemId → 404 |
| `InvalidBody_400` | POST with malformed JSON → 400, no DB write |
| `MissingTime_400` | POST with body `{title:"x"}` (no time) → 400 |
| `SocketEvent_FiredOnCreate` | mock `EventPublisher` captures the publish; assert userID + `user_updated` + `reason:"bookmark_created"` |
| `SocketEvent_FiredOnUpdate` | same for `bookmark_updated` |
| `SocketEvent_FiredOnDelete` | same for `bookmark_deleted` |

### 8.2 Wire-shape test — `bookmarks_envelope_test.go`

One marshal test that takes a `Bookmark` (including the empty-title case) and asserts every required JSON key is present and uses the camelCase spelling: `id`, `libraryItemId`, `time`, `title`, `createdAt`, `updatedAt`. Mirrors the existing `TestSiloItemToMetadata_JSONKeysAlwaysPresent` and `TestLoginEnvelope_HasRequiredKeys` patterns.

### 8.3 Live integration smoke (post-deploy)

```
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' -H 'x-return-tokens: true' \
  -d '{"username":"<u>","password":"<p>"}' http://127.0.0.1:13378/login \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['accessToken'])")

ITEM=$(curl -s -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:13378/api/libraries/<lid>/items?limit=1 \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['results'][0]['id'])")

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"smoke","time":42.5}' \
  http://127.0.0.1:13378/api/me/item/$ITEM/bookmark | python3 -m json.tool

curl -s -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"updated","time":42.5}' \
  http://127.0.0.1:13378/api/me/item/$ITEM/bookmark | python3 -m json.tool

curl -s -X DELETE -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:13378/api/me/item/$ITEM/bookmark/42.5 | python3 -m json.tool
```

Expected: each call returns the updated list; the final DELETE returns `[]`.

### 8.4 Explicitly out-of-scope

- No real-Postgres integration test for the SQL. The `ON CONFLICT` clause and unique-index COALESCE behavior are small enough that the in-memory fake covers the semantics; CI does not currently run a Postgres for the ABS subdomain.
- No load test. Bookmark mutations are user-driven and rare (handful per session).

## 9. Risks & open questions

- **Time-precision collision risk.** If a future client rounds `time` differently between POST and DELETE (e.g. POST sends `42.500001`, DELETE sends `42.5`), the DELETE would silently no-op. Acceptable for the current client set (Android and official iOS echo exactly), but worth a comment on the `Delete` method so future-us doesn't burn time chasing a ghost.
- **No migration rollback test.** The `.down.sql` is one `DROP TABLE` — low risk — but the migration suite doesn't currently exercise rolls. Same posture as recent migrations; not a blocker.
- **Bookmark fan-out on socket events.** `Handler.publish(userID, ...)` targets the user room. If the user has many connected sockets, the publish fans out per-socket. Volume here is trivial (manual user action). No backpressure concern at Phase 1 scale.

## 10. Out-of-scope follow-ups

These are intentionally deferred — they belong to later Phase 1 sub-projects or Phase 2:

- Aggregate `GET /api/me/bookmarks` (caller's full bookmark inventory). Adds value only when a "Bookmarks" tab lands in a client that needs it.
- Hydrating `user.bookmarks` in the `/login` envelope. Currently emits `[]` placeholder; populating costs a join on every login and only matters to clients that read from there at startup.
- Embedding `bookmarks` on item-detail (`GET /api/items/{id}`). The Android player fetches its bookmarks via the per-item bookmark endpoint on modal-open, not from item-detail.

## 11. References

- Spec parent: `docs/superpowers/specs/2026-05-26-abs-implementation-fix-design.md`.
- Wire-shape reference (canonical, working against real ABS Android): the continuum-plugin-audiobooks `bookmarks_handler.go` + the `r.Post/Patch/Delete` mounts in its `handler.go`. Diff against silo before flagging response-shape concerns.
- Client wire usage: `audiobookshelf-app/components/modals/BookmarksModal.vue` (the only place the official client builds bookmark requests).
- Booklore-ng has its own `me/bookmarks` aggregate endpoint that the official client does not use; ignored here.
