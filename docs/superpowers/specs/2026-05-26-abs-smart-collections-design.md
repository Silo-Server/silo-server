# ABS Smart Collections — Phase 1 Sub-Project 3

**Status:** Approved 2026-05-26. Ready for implementation plan.
**Scope:** Third sub-project of Phase 1 (per `2026-05-26-abs-implementation-fix-design.md`).
**Predecessor specs:** `2026-05-26-abs-bookmarks-design.md`, `2026-05-26-abs-collections-playlists-design.md`. Conventions established there (anti-enumeration 404, `io.LimitReader(1<<20)` body cap, `errors.Is(err, pgx.ErrNoRows)` store pattern, profile-scoped + cross-user-public read, 200 OK on POST, in-package test fakes + `dispatchABSWithParams`) are reused without re-justification.

Commands in this document assume the repository root is the cwd.

## 1. Goal

Land rule-based "smart" audiobook collections on the silo ABS surface so the official Audiobookshelf clients can define dynamic groupings ("books from 2020-2024 by Brandon Sanderson", "audiobooks I've started but haven't finished", "5-star fantasy under 12 hours"). Rules are stored as a JSON DSL; the items endpoint evaluates the rules against the catalog at request time and returns paginated `LibraryItem` results.

## 2. Non-goals

- **No SQL pushdown.** Evaluation walks the candidate list in Go. silo's audiobook library is small enough (hundreds of items) that linear scan per request is fine. SQL pushdown is a Phase 4 follow-up if the library grows past ~5000 items.
- **No rule-builder UI.** Clients ship their own; silo just stores and evaluates `query_def`.
- **No podcast smart collections.** The DSL catalog is audiobook-domain only (title/author/narrator/series/genre/...). Podcast smart collections, if ever needed, get their own DSL.
- **No socket events.** Matches the manual-collections decision in sub-project 2. Phase 2 covers full event parity.
- **No saved-search aliases.** A smart collection is fully described by its `query_def`; there's no separate "saved query" entity.
- **No background materialisation.** Each `/items` call re-evaluates. If perf matters later, add a 30s in-memory result cache keyed on `(collectionID, userID)`.

## 3. Architecture

Three layers, sharing the established silo ABS structure:

- **DSL layer (new package `internal/audiobooks/smartcoll/`):**
  - `query.go` — `QueryDefinition`, `QueryGroup`, `QueryRule`, `QuerySort` types + field/sort catalogs + `Normalize`/`Validate`/`MarshalJSON`. Ported from continuum's `internal/smartcoll/query.go` verbatim except for the import path.
  - `evaluator.go` — `Candidate`, `EvaluateOptions`, `Evaluate(ctx, qd, candidates, opts) []Candidate`. Pure function, no I/O. Ported from continuum.
  - `query_test.go` + `evaluator_test.go` — port continuum's test suites; remove any references to continuum-specific backend types.
- **HTTP layer (`internal/audiobooks/abs/`):**
  - `smart_collections_handler.go` — six handlers: `handleListSmartCollections`, `handleCreateSmartCollection`, `handleGetSmartCollection`, `handleSmartCollectionItems`, `handleUpdateSmartCollection`, `handleDeleteSmartCollection`.
  - `smart_collections_handler_test.go` — in-memory `memSmartCollectionStore` + handler tests reusing `dispatchABSWithParams`, `stubMediaStore`, `recordingPublisher` from prior sub-projects' test files.
  - `smart_collections_envelope_test.go` — wire-shape marshal test.
- **Storage layer:**
  - `internal/audiobooks/abs/smart_collections.go` — `SmartCollectionStore` interface + `SmartCollection` Go model + `smartCollectionToABS` serialiser.
  - `internal/audiobooks/abs_smart_collection_store.go` — pgx-backed `ABSSmartCollectionStore` (parallel to `abs_collection_store.go`).
- **Migration:** `153_abs_smart_collections.up.sql` + `.down.sql`.
- **Bookmark store extension:**
  - `BookmarkStore.CountByUser(ctx, userID, profileID) (map[string]int, error)` returning `{libraryItemID: count}` for batch hydration of the personalized `bookmark_count` rule. Single SQL: `SELECT library_item_id, COUNT(*) FROM abs_bookmarks WHERE user_id = $1 AND COALESCE(profile_id, sentinel) = COALESCE($2, sentinel) GROUP BY library_item_id`.
- **Service wiring:** `BuildABSHandler` constructs the new store; field lands on `abs.Dependencies` next to `PlaylistStore`.

## 4. Endpoint surface

All routes mounted under both `/abs/api/*` and `/api/*` inside the existing `bearerAuth` group. Success status is **200 OK** unless the `Returns` column says otherwise.

| Verb | Path | Body | Returns |
|---|---|---|---|
| GET | `/me/smart-collections` | — | `{"items": [SmartCollection list-shape]}` |
| POST | `/me/smart-collections` | `{name, description?, color?, isPublic?, isPinned?, query_def}` | SmartCollection full-shape |
| GET | `/me/smart-collections/{id}` | — | SmartCollection full-shape when owner OR `isPublic=true`; otherwise 404 |
| GET | `/me/smart-collections/{id}/items` | (query: `?limit=`, `?page=`) | Paged envelope of `LibraryItem` results from rule evaluation |
| PATCH | `/me/smart-collections/{id}` | `{name?, description?, color?, isPublic?, isPinned?, query_def?}` | SmartCollection full-shape |
| DELETE | `/me/smart-collections/{id}` | — | 204 No Content |

### 4.1 Wire shape — SmartCollection

**List-shape and full-shape are identical** (unlike manual collections / playlists where the list-shape omits child items). Smart collections have no stored items; the items are computed by the separate `/items` route. So one shape:

```json
{
  "id": "01HSC...",
  "userId": "1",
  "name": "Recent Fantasy",
  "description": "fantasy added in the last 6 months",
  "color": "#3b82f6",
  "isPublic": false,
  "isPinned": true,
  "queryDef": {
    "library_ids": [9],
    "match": "all",
    "groups": [{
      "match": "all",
      "rules": [
        {"field": "genre", "op": "contains", "value": "Fantasy"},
        {"field": "added_at", "op": "in_last", "value": "180d"}
      ]
    }],
    "sort": {"field": "added_at", "order": "desc"},
    "limit": null
  },
  "createdAt": 1779786284823,
  "updatedAt": 1779786284823
}
```

All ten top-level keys always present (`description`, `color` default to `""`; `isPublic`, `isPinned` default to `false`; `queryDef` is the parsed JSON, never raw bytes on the wire). Timestamps are JS-epoch milliseconds.

### 4.2 Wire shape — `/items` envelope

Standard ABS paged envelope (`pagedEnvelope` helper already in `handler.go`):

```json
{
  "results":  [ /* hydrated LibraryItem objects */ ],
  "total":    42,
  "limit":    30,
  "page":     0,
  "sortBy":   "added_at",
  "sortDesc": true,
  "filterBy": "",
  "minified": false,
  "include":  ""
}
```

`results` items use the same `siloItemToLibraryItem` shape as `/api/libraries/{id}/items`. Pagination is post-eval slice (eval order is preserved; pages are stable for a given `(collection, user)` as long as the underlying catalog doesn't change).

### 4.3 List-envelope wrap key

Smart-collection LIST uses `{"items": [...]}` — matches continuum's wrap key (which differs from manual collections' `{"collections": [...]}`). Clients pattern-match on the wrap key when distinguishing surfaces; don't override.

## 5. DSL surface (full vocabulary)

### 5.1 Fields (15 total)

**Non-personalized (10):**

| Field | Type | Valid ops | Source |
|---|---|---|---|
| `title` | scalar string | `is`, `is_not`, `contains` | `media_items.title` |
| `author` | array of strings | `is`, `is_not`, `contains` | item people: role=author |
| `narrator` | array of strings | `is`, `is_not`, `contains` | item people: role=narrator |
| `series` | array of strings | `is`, `is_not`, `contains` | `audiobook_series` |
| `genre` | array of strings | `is`, `is_not`, `contains` | `media_items.genres` |
| `year` | int | `is`, `is_not`, `gt`, `gte`, `lt`, `lte`, `between` | `media_items.release_year` |
| `rating` | float | `gt`, `gte`, `lt`, `lte`, `between` | `media_items.rating_imdb` (or whichever rating silo populates) |
| `language` | scalar string | `is`, `is_not` | `media_items.original_language` |
| `publisher` | scalar string | `is`, `is_not`, `contains` | `media_items.publisher` |
| `added_at` | timestamp | `gt`, `lt`, `between`, `in_last` | `media_items.added_at` |
| `duration_seconds` | int | `gt`, `gte`, `lt`, `lte`, `between` | sum of media_files.duration for the item |

**Personalized (5)** — require `AllowPersonalized: true` (only when caller is the collection's owner):

| Field | Valid ops | Source |
|---|---|---|
| `finished` | `is` | `user_watch_progress.is_finished` |
| `in_progress` | `is` | progress exists AND `is_finished == false` AND `current_seconds > 0` |
| `last_played` | `gt`, `gte`, `lt`, `lte`, `between`, `in_last` | `user_watch_progress.updated_at` |
| `abandoned` | `is` | `in_progress == true` AND `last_played < now() - 60d` (configurable via `EvaluateOptions.AbandonedAfter`) |
| `bookmark_count` | `gt`, `gte`, `lt`, `lte`, `between` | count of `abs_bookmarks` rows for (user, profile, item) |

### 5.2 Sort fields

`title` (asc), `added_at` (desc), `year` (desc), `duration_seconds` (desc), `rating` (desc), `random` (deterministic shuffle seeded by `userID + ":" + collectionID`), and personalized: `progress` (desc), `last_played` (desc), `plays` (desc).

### 5.3 Operators

- `is` / `is_not` — equality (case-insensitive for strings; numeric otherwise). For array fields, `is` matches when ANY element equals the value.
- `contains` — substring (case-insensitive) for scalar strings; element-substring for array fields.
- `gt` / `gte` / `lt` / `lte` — numeric or timestamp comparison.
- `between` — `value` is a 2-element array `[low, high]`, inclusive bounds.
- `in_last` — relative window. `value` is a duration string (`"7d"`, `"180d"`, `"24h"`, `"4w"`). Evaluator parses to `time.Duration`; field must be ≥ `opts.Now - duration`.

### 5.4 Aliases

`authors` → `author`, `narrators` → `narrator`, `genres` → `genre` for fields. `sort_title` → `title`, `recently_added` → `added_at`, `duration` → `duration_seconds` for sorts. Applied in `Normalize()`; older clients with plural / verbose names keep working.

## 6. Data model

### 6.1 Migration 153

`migrations/153_abs_smart_collections.up.sql`:

```sql
CREATE TABLE IF NOT EXISTS public.abs_smart_collections (
    id          text PRIMARY KEY,
    user_id     integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id  uuid,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    color       text NOT NULL DEFAULT '',
    is_public   boolean NOT NULL DEFAULT false,
    is_pinned   boolean NOT NULL DEFAULT false,
    query_def   jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS abs_smart_collections_user_profile_idx
    ON public.abs_smart_collections (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
    );
```

`migrations/153_abs_smart_collections.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_smart_collections_user_profile_idx;
DROP TABLE IF EXISTS public.abs_smart_collections;
```

### 6.2 Schema rationale

- **`query_def jsonb`** — JSONB so future GIN-indexed JSON path probes are an option; default `'{}'::jsonb` so rows with no rules are queryable without NULL guards.
- **No items table** — smart collections have no stored children; items are computed at eval time.
- **`color`, `is_pinned`** — UI decorations. silo doesn't validate the color format.
- **No `library_ids` column** — that list is part of the DSL, lives inside `query_def`.

### 6.3 Go model

```go
type SmartCollection struct {
    ID          string
    UserID      string
    ProfileID   string
    Name        string
    Description string
    Color       string
    IsPublic    bool
    IsPinned    bool
    QueryDef    []byte    // raw JSONB bytes; decoded on the items path
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

## 7. Storage contract

```go
type SmartCollectionStore interface {
    ListUserSmartCollections(ctx context.Context, userID, profileID string) ([]SmartCollection, error)
    GetSmartCollection(ctx context.Context, id string) (SmartCollection, error)
    CreateSmartCollection(ctx context.Context, c SmartCollection) error
    UpdateSmartCollection(ctx context.Context, c SmartCollection) error
    DeleteSmartCollection(ctx context.Context, id string) error
}
```

Behavior follows the established silo pattern: ordered by `created_at DESC`, empty slice never nil, `GetSmartCollection` returns `ErrNotFound` without owner check (handler authorizes), `Update` sets `updated_at = now()`, `Delete` returns nil even on no-match.

**Extension to existing `BookmarkStore`:**

```go
// CountByUser returns a map of library_item_id → bookmark count for
// the given (user, profile). Empty map (never nil) when none.
// Used by the smart-collection items evaluator to hydrate the
// personalized `bookmark_count` rule in one SQL pass.
CountByUser(ctx context.Context, userID, profileID string) (map[string]int, error)
```

SQL: `SELECT library_item_id, COUNT(*) FROM abs_bookmarks WHERE user_id = $1 AND COALESCE(profile_id, sentinel) = COALESCE($2::uuid, sentinel) GROUP BY library_item_id`.

## 8. Items evaluation flow

`handleSmartCollectionItems` runs:

1. Auth + store-nil + URL `{id}` checks (same boilerplate as other handlers).
2. `GetSmartCollection(ctx, id)` → 404 on ErrNotFound OR `(non-owner AND !isPublic)`.
3. Decode `c.QueryDef` → `smartcoll.QueryDefinition`. 500 on malformed (shouldn't happen — `Validate` is called on persist; defensive guard).
4. Resolve target libraries: `qd.LibraryIDs` if non-empty, else every audiobook library from `MediaStore.ListAudiobookLibraries`.
5. For each target library, fetch the audiobook list via `MediaStore.ListAudiobooks(ctx, libID, 5000, 0)`. (5000 cap matches continuum's; silo's libraries are smaller.)
6. **Build Candidates.** For each item:
   - `Candidate.Item` = the `*models.MediaItem`.
   - If `c.UserID == a.UserID` (owner): hydrate per-user state.
     - Fetch all progress rows: `ProgressStore.ListProgressForAudiobooks(ctx, a.UserID, a.ProfileID, 10000)`. Build `map[contentID]ProgressRow`.
     - Fetch bookmark counts: `BookmarkStore.CountByUser(ctx, a.UserID, a.ProfileID)`. Map `contentID → count`.
     - Populate `IsFinished`, `ProgressPct`, `CurrentSeconds`, `LastPlayedAt`, `BookmarkCount`.
   - Non-owner viewing public collection: skip hydration; personalized rules eval against zero-values.
7. `smartcoll.Evaluate(ctx, qd, candidates, EvaluateOptions{AllowPersonalized: c.UserID == a.UserID, UserSeed: a.UserID + ":" + c.ID, Now: time.Now(), AbandonedAfter: 60*24*time.Hour})` → matched + sorted.
8. Paginate: `limit, page := readPagedQuery(r, 30)`. Override default limit with `qd.Limit` when `?limit=` query param is absent. Slice `matched[page*limit : (page+1)*limit]`.
9. Hydrate each result via the existing `siloItemToLibraryItem` helper (same as `/api/libraries/{id}/items`).
10. Wrap in `pagedEnvelope`. Write 200.

### 8.1 Performance budget

- Library fetch: one SQL query per library (~hundreds of rows).
- Progress hydration: one SQL query for the user's full progress list.
- Bookmark hydration: one SQL query for the aggregated counts.
- Total: 2 + N (libraries) queries, all paginated/limited at the store layer. silo's current 1-library config means 3 total queries per `/items` request. Fast enough.

## 9. Error model

| Condition | Status | Body |
|---|---|---|
| Missing/invalid bearer | 401 | handled by `bearerAuth` middleware |
| Body decode failure (POST/PATCH) | 400 | `invalid body` |
| `name` missing on POST | 400 | `name required` |
| `query_def` fails `Validate(allowPersonalized=true)` | 400 | `invalid query_def: <reason>` (reason from validator — safe to surface) |
| Unknown collection on GET (owner or not) | 404 | `smart collection not found` |
| Non-owner GET on private | 404 | same body — no leak |
| Non-owner PATCH/DELETE/items | 404 | same body |
| Store mutate fails | 500 | `smart collection persist failed` (delete: `smart collection delete failed`); err logged via `slog.Error` |
| `/items` eval — `MediaStore.ListAudiobooks` fails for a library | log `slog.Warn` and skip that library | partial results beat 500 |
| Personalized rule on non-owner-public eval | silently dropped at `smartcoll.Evaluate` via `AllowPersonalized: false` | no error to the client |

**Cross-cutting:** Body size limit `io.LimitReader(r.Body, 1<<20)` on all POST/PATCH. `validate(allowPersonalized=true)` runs on every persist call; on read, the items handler passes `AllowPersonalized = (c.UserID == a.UserID)` so saved personalized rules are non-fatally dropped when viewed by non-owners.

## 10. Testing

### 10.1 DSL unit tests (`internal/audiobooks/smartcoll/`)

Port continuum's `query_test.go` and `evaluator_test.go` verbatim. The continuum reference covers:

- `Normalize`: aliasing, lowercase, dedupe library_ids, default-match `"all"`, default-sort `"added_at"`.
- `Validate`: unknown field, invalid op for field, personalized rule without scope, invalid sort field, invalid sort order, negative limit.
- `Evaluate`: each operator for each field type, AND/OR combinator for groups and definition, personalized rules drop when `AllowPersonalized: false`, `random` sort stability per `UserSeed`, `Limit` honored, empty rules match-everything.

### 10.2 Handler tests (`smart_collections_handler_test.go`)

In-memory `memSmartCollectionStore` (parallel to `memCollectionStore`); reuses `dispatchABSWithParams`, `stubMediaStore`, `recordingPublisher`. The bookmark store extension (`CountByUser`) needs a matching method on `memBookmarkStore` from sub-project 1's test file — extend that fake.

| Test | Asserts |
|---|---|
| `SmartCollection_Create_ReturnsFullShape` | POST → 200, 10 top-level keys present, ULID, queryDef round-tripped as a nested object (not raw bytes) |
| `SmartCollection_Create_NameRequired_400` | empty name → 400 |
| `SmartCollection_Create_InvalidBody_400` | malformed JSON → 400 |
| `SmartCollection_Create_InvalidQueryDef_400` | `{"groups":[{"rules":[{"field":"nonsense","op":"is","value":1}]}]}` → 400 with reason in body |
| `SmartCollection_Create_PersonalizedRuleAllowed` | rule with `field:"finished"` accepted (persist always runs with `allowPersonalized=true`) |
| `SmartCollection_List_WrappedAsItems` | GET → `{"items": [...]}` envelope key (NOT `"collections"`) |
| `SmartCollection_List_DoesNotLeakOtherUsers` | user 1 creates; user 2 lists → empty |
| `SmartCollection_List_ProfileIsolation` | profile A creates; profile B lists same user → empty |
| `SmartCollection_Get_Owner_ReturnsFullShape` | owner GET → 200 + full-shape |
| `SmartCollection_Get_NonOwner_Public_OK` | public GET by other user → 200 |
| `SmartCollection_Get_NonOwner_Private_404` | private GET by other user → 404 (anti-enumeration) |
| `SmartCollection_Get_Unknown_404` | unknown ID → 404 |
| `SmartCollection_Patch_Owner_UpdatesFields` | partial PATCH updates only present fields |
| `SmartCollection_Patch_NonOwner_404` | non-owner PATCH → 404, original untouched |
| `SmartCollection_Patch_InvalidQueryDef_400` | PATCH with bad query_def → 400 |
| `SmartCollection_Delete_Owner_204` | DELETE → 204; subsequent GET → 404 |
| `SmartCollection_Delete_NonOwner_404` | non-owner DELETE → 404, original still present |
| `SmartCollection_Items_Owner_EvaluatesRules` | seed 3 audiobooks (two match the rule, one doesn't); GET /items returns 2 results |
| `SmartCollection_Items_PersonalizedDroppedForNonOwner` | public collection with `field:"finished"` rule — non-owner sees ALL matching books (personalized rule silently dropped); owner sees only finished |
| `SmartCollection_Items_PaginatedEnvelope` | GET /items → standard pagedEnvelope with 9 fields |
| `SmartCollection_Items_RespectsQueryDefLimit` | qd.Limit=5; GET without ?limit returns 5; GET with ?limit=10 returns 10 (request overrides) |
| `SmartCollection_Items_NonOwner_Private_404` | private collection /items by non-owner → 404 |
| `SmartCollection_Envelope_HasRequiredKeys` | marshal-test for 10 top-level keys with empty defaults |

### 10.3 Live integration smoke (operator)

```bash
TOKEN=$(curl ... /login | jq -r .accessToken)

# Create a smart collection: "fantasy added in the last 6 months"
SC=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"recent fantasy","query_def":{"match":"all","groups":[{"match":"all","rules":[{"field":"genre","op":"contains","value":"Fantasy"},{"field":"added_at","op":"in_last","value":"180d"}]}],"sort":{"field":"added_at","order":"desc"}}}' \
  http://127.0.0.1:13378/api/me/smart-collections | jq -r .id)

# Evaluate
curl ... /api/me/smart-collections/$SC/items | jq '.results | length'

# Patch name
curl -X PATCH ... -d '{"name":"renamed"}' /api/me/smart-collections/$SC | jq .name

# Delete
curl -X DELETE ... /api/me/smart-collections/$SC   # 204
```

### 10.4 Out of scope

- No real-Postgres SQL test (same posture as prior sub-projects).
- No GIN-index on `query_def`. Phase 4 follow-up if rule-querying becomes a feature.
- No performance test. Library is small; linear scan is fine.

## 11. Risks & open questions

- **5000-item per-library cap.** If a library exceeds this, the over-fetch silently truncates. silo's libraries are well below this today; if it changes, switch to paged ListAudiobooks + accumulate.
- **Personalized rule eval against non-owners is silent.** A non-owner viewing a public collection sees results that don't honor the owner's personalized rules. This is the privacy-correct behavior, but a confused user might wonder why "books I haven't finished" shows everything when viewed by their friend. Document in the parent spec's UX notes.
- **`random` sort + pagination.** Two requests with the same `(user, collection)` produce the same shuffle (seeded). Browser-back / page-2 stays consistent. If the catalog mutates between requests, the seed still works but new items land in unpredictable positions — acceptable.
- **`query_def` migration on field-catalog changes.** If silo renames a field or removes an op in the future, existing rows persist the old vocab. `Validate` would reject them, but Normalize won't drop them — we'd surface stale rules as a runtime warning in the items handler. Acceptable for v1.
- **`description` length.** No cap; relies on the 1 MiB body limit. Same posture as other surfaces.

## 12. Out-of-scope follow-ups

- SQL-pushdown evaluator for large libraries.
- GIN-indexed JSON path queries on `query_def`.
- Background materialisation cache for hot smart collections.
- Cross-collection composition ("collection A + collection B").
- Result count badges on `/me/smart-collections` (would require running eval on every list call — expensive).
- Audiobook recommender hooks (the `relevance` sort placeholder in continuum).

## 13. References

- Spec parent: `docs/superpowers/specs/2026-05-26-abs-implementation-fix-design.md`.
- Sibling sub-project specs: `2026-05-26-abs-bookmarks-design.md`, `2026-05-26-abs-collections-playlists-design.md`.
- DSL reference: `continuum-plugin-audiobooks/internal/smartcoll/{query,evaluator}.go` + matching tests (port verbatim).
- HTTP reference: `continuum-plugin-audiobooks/internal/abs/smart_collection_handler.go`.
