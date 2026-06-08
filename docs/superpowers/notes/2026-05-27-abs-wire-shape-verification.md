# ABS Wire-Shape Verification (post collections-unify cutover)

Breadcrumbs for the next person debugging an ABS endpoint wire-shape issue
after the canonical-tables cutover (migration 156 + commits `0dc830e`,
`8c7fe1b`, `b64ce17`).

The Go in-memory structs (`abs.Collection`, `abs.Playlist`,
`abs.SmartCollection`, `abs.CollectionItem`, `abs.PlaylistItem`) have **no
`json:"..."` struct tags**. The JSON wire contract is defined entirely by
the `*ToABS()` map-builder helpers in `internal/audiobooks/abs/`. As long as
the store layer populates the struct fields with the same values, the wire
shape is preserved. The rewrites in `0dc830e`, `8c7fe1b`, `b64ce17` did NOT
modify the emitters — only the SQL-backed store implementations.

## Envelope tests — which one guards which endpoint

Run with `go test ./internal/audiobooks/abs/ -run Envelope -v -count=1`.

| Test file | Functions | Guards |
|---|---|---|
| `collections_envelope_test.go` | `TestCollectionEnvelope_HasRequiredKeys`, `TestCollectionListShape_OmitsBooks` | `collectionToABS` keys; list-shape (no `books`) vs detail-shape (with `books`) for `GET /api/collections`, `GET /api/collections/{id}`, `GET /api/libraries/{id}/collections`, and all POST/PATCH/DELETE collection endpoints |
| `playlists_envelope_test.go` | `TestPlaylistEnvelope_HasRequiredKeys`, `TestPlaylistEnvelope_OmitsCoverPathWhenEmpty`, `TestPlaylistListShape_OmitsItems` | `playlistToABS` keys; list-shape (no `items`) vs detail-shape (with `items`); `coverPath` is omitted when `CoverItem == ""` — covers `GET /api/playlists`, `GET /api/playlists/{id}`, `GET /api/libraries/{id}/playlists`, batch and item-add/remove endpoints |
| `smart_collections_envelope_test.go` | `TestSmartCollectionEnvelope_HasRequiredKeys`, `TestSmartCollectionEnvelope_EmptyQueryDef` | `smartCollectionToABS` keys; `queryDef` decoded from raw JSONB bytes into nested object, empty bytes → `{}` — covers `GET /api/me/smart-collections`, `GET /api/me/smart-collections/{id}`, POST/PATCH equivalents |
| `bookmarks_envelope_test.go` | `TestBookmarkEnvelope_HasRequiredKeys` | Bookmarks emitter (separate from this cutover, not affected by migration 156) |
| `login_envelope_test.go` | `TestLoginEnvelope_HasRequiredKeys` and three xReturnTokens / displayName variants | Login envelope (not affected by migration 156) |

In addition, handler-level round-trip tests live in
`playlists_handler_test.go` and `bookmarks_handler_test.go`. There is NO
snapshot/goldenfile harness in the repo today — these envelope tests are
the primary regression guard.

## Manual live-DB diff procedure

For a pre/post-deploy wire-shape verification against a live silo, see the
plan's Task 5 "manual verification" section at
`docs/superpowers/plans/2026-05-27-collections-unify-3-abs-adapters.md`
(§ `Task 5: Wire-shape regression test`). Summary:

1. Pre-cutover, seed one of each (collection, playlist with item,
   smart collection) via the old `abs_*` tables, then capture each list
   endpoint's response to `/tmp/wire_before_<kind>.json` using a curl
   against the running silo with a valid ABS bearer token (HS256 JWT —
   minted by the login flow, NOT the raw `abs_sessions.token` value).
2. Apply migration 156. Seed equivalent rows in `user_personal_collections`
   with the same IDs and content. Capture again to
   `/tmp/wire_after_<kind>.json`.
3. `diff /tmp/wire_before_<kind>.json /tmp/wire_after_<kind>.json` for each
   `kind in {collections,playlists,smart_collections}`. Expected: empty
   diff.

This is an MR-description-level manual step, not a committed test.

## Intentionally-zero fields after the rewrite

These wire keys are still emitted, but the store always populates the
in-memory field with the zero value because the canonical
`user_personal_collections` schema has no analog column (per spec §6 of the
collections-unify plan). They are NOT bugs — do not "fix" them by reaching
for some other column.

| In-memory field | Wire key | Zero value | Spec ref | Disposition |
|---|---|---|---|---|
| `abs.Playlist.CoverItem` | `coverPath` | `""` (key omitted entirely when empty — see `playlistToABS`) | spec §6.1 | Dropped. PATCH `coverPath` body field is silently ignored by the store. Cover regeneration from first-item poster is the chosen long-term path. |
| `abs.SmartCollection.Color` | `color` | `""` (key always emitted as empty string) | spec §6.3 | Deferred. No column on `user_personal_collections`. Wire key stays present for client compatibility. |
| `abs.SmartCollection.IsPinned` | `isPinned` | `false` (key always emitted) | spec §6.2 | Deferred. Same rationale. |

If you're adding a "Pin this smart collection" feature later, the column
needs to land in a new migration on `user_personal_collections` first;
don't try to thread it through some adjacent column.

## Canonical mapping — struct field → source column

The full pre-cutover wire contract was captured in a working note that does
not persist (`/tmp/abs_wire_contract.md`). The essentials are reproduced
here so the next maintainer doesn't have to re-derive them.

All three struct families now read from `user_personal_collections`
(and `user_personal_collection_items` for collections + playlists),
discriminated by `collection_type IN ('manual','playlist','smart')`.

### `abs.Collection` (`collection_type = 'manual'`)

| Field | Source column |
|---|---|
| ID | `user_personal_collections.id` |
| UserID | `user_personal_collections.user_id::text` (column is `integer`) |
| ProfileID | `user_personal_collections.profile_id` |
| Name | `user_personal_collections.name` |
| Description | `user_personal_collections.description` |
| IsPublic | `user_personal_collections.is_shared` |
| CreatedAt | `user_personal_collections.created_at` |
| UpdatedAt | `user_personal_collections.updated_at` |

`abs.CollectionItem` reads `user_personal_collection_items` with
`sub_item_id = ''` filter (the manual-collection sentinel established in
migration 156 step 1). LibraryItemID ← `media_item_id`. ORDER BY
`added_at ASC`.

### `abs.Playlist` (`collection_type = 'playlist'`)

Same column mapping as `abs.Collection` (modulo `collection_type` filter)
EXCEPT `CoverItem` which is always `""` — see "Intentionally-zero fields"
above.

`abs.PlaylistItem` reads `user_personal_collection_items` with NO
`sub_item_id` filter (playlists can carry episode entries). Mapping:
LibraryItemID ← `media_item_id`, EpisodeID ← `sub_item_id`,
Position ← `position`. ORDER BY `position ASC, added_at ASC`.

### `abs.SmartCollection` (`collection_type = 'smart'`)

Same column mapping as `abs.Collection` EXCEPT:

- `Color`, `IsPinned` → always zero (see above).
- `QueryDef` ← `user_personal_collections.query_definition` (JSONB → `[]byte`
  round-trip; column is `NOT NULL DEFAULT '{}'::jsonb` per migration 016).

No items table — smart-collection membership is evaluated at request time
via the `smartcoll` package.

### Wire-shape quirks worth remembering

- Collection/Playlist emit `lastUpdate` (NOT `updatedAt`). SmartCollection
  emits `updatedAt`. Cross-struct inconsistency, carry forward verbatim.
- All timestamps are `UnixMilli()` int64, NOT RFC3339 strings.
- `ProfileID` is carried in memory but NEVER emitted on the wire — it's
  scope/auth only.
- The list vs detail shape distinction is implicit: list responses pass
  `nil` for the items/books slice; the emitter then omits the key
  entirely. Clients differentiate on key presence.

## Verification status (2026-05-27)

- All 13 envelope tests pass (run: `go test ./internal/audiobooks/abs/
  -run Envelope -v -count=1`).
- Full audiobooks test suite passes (`go test ./internal/audiobooks/...
  -short -count=1 -timeout 120s`).
- Live-DB diff was NOT executed in CI — see manual procedure above.
