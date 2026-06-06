# Collections Unification — Sub-project 3: ABS Store Adapter Rewrites

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the three ABS store adapters (`abs_collection_store.go`, `abs_playlist_store.go`, `abs_smart_collection_store.go`) to query the canonical `user_personal_collections` tables instead of the dropped `abs_*` tables. The wire shape returned by ABS HTTP endpoints must remain byte-identical so the ABS Android/iOS apps don't notice anything changed.

**Architecture:** The three adapter files expose a struct + ~6 methods each (List, Get, Create, Update, Delete, ListItems, AddItem, RemoveItem) consumed by `internal/audiobooks/abs/` HTTP handlers. We keep every public method signature unchanged and replace each SQL body with one that maps to `user_personal_collections` filtered by `collection_type`. Granular podcast-episode entries map to the new `sub_item_id` column. Library scoping (which library this list "belongs to" in ABS UI) is stored in `query_definition.library_ids`.

**Tech Stack:** Go, `pgx/v5`, `database/sql`-style query patterns from the existing adapters.

**Commands assume the repository root is the cwd.**

**Source spec:** `docs/superpowers/specs/2026-05-27-unified-audiobook-collections-design.md` §4.3, §4.5.

**Predecessor sub-projects:**
- Sub-project 1 (migration 156) **must** land before this. The new tables/columns are required.
- Sub-project 2 (smartcoll lift) needed only for the smart-collection adapter — if 2 hasn't landed, Task 3 below can either wait for it or temporarily keep the old import path.

---

## File map

**Modify (rewrite bodies, keep method signatures):**
- `internal/audiobooks/abs_collection_store.go` (193 lines today; ~8 methods)
- `internal/audiobooks/abs_playlist_store.go` (210 lines today; ~9 methods including `coverArg`)
- `internal/audiobooks/abs_smart_collection_store.go` (~140 lines today; ~6 methods)

**Add (snapshot tests for wire-shape preservation):**
- `internal/audiobooks/abs_collection_store_test.go` (if not already present — check first)
- `internal/audiobooks/abs_playlist_store_test.go`
- `internal/audiobooks/abs_smart_collection_store_test.go`

**No new files** beyond those three test files. No new exported types.

---

## Task 1: Capture pre-cutover wire shapes (snapshots)

This task runs against the **pre-migration** ABS endpoints. Capture JSON now so we can diff after.

**Files:** none modified; output captured to `/tmp/abs_wire_*.json`.

- [ ] **Step 1: Identify the wire-shape contract**

The ABS HTTP handlers live in `internal/audiobooks/abs/`. They call the store adapters, which return Go structs. The structs (`abs.Collection`, `abs.Playlist`, `abs.SmartCollection`, etc.) marshal to JSON via standard struct tags. The wire shape is fixed by those struct tag declarations.

Read them:

```bash
grep -n "type Collection struct\|type Playlist struct\|type SmartCollection struct\|type CollectionItem struct\|type PlaylistItem struct" internal/audiobooks/abs/*.go
```

For each struct, note every field's JSON tag. **This is your reference contract.** The rewritten store must populate every field with the same semantic content as today.

- [ ] **Step 2: Capture sample JSON from existing endpoints (if any data exists)**

The DB today has 1 abs_playlists row, 0 collections, 0 smart collections. Even one sample helps. Hit each endpoint via `curl` against the running silo and save the output:

```bash
# Adapt to your env's auth / port
PORT=8090   # silo's API port from .env
TOKEN=...   # admin or user JWT

curl -sH "Authorization: Bearer $TOKEN" http://localhost:$PORT/api/libraries/9/collections \
    > /tmp/abs_wire_collections.json
curl -sH "Authorization: Bearer $TOKEN" http://localhost:$PORT/api/libraries/9/playlists \
    > /tmp/abs_wire_playlists.json
curl -sH "Authorization: Bearer $TOKEN" http://localhost:$PORT/api/libraries/9/smart-collections \
    > /tmp/abs_wire_smart_collections.json
```

If you don't have a token or the data is uninteresting (empty arrays), skip this step — the field-by-field reference from Step 1 is enough to write correct code.

- [ ] **Step 3: Note any non-obvious mappings**

For each adapter, write out the mapping between abs.* struct fields and `user_personal_collections` columns. Sample for Collection:

| `abs.Collection` field | Source column |
|---|---|
| `Id` | `user_personal_collections.id` |
| `Name` | `user_personal_collections.name` |
| `Description` | `user_personal_collections.description` |
| `UserId` | `user_personal_collections.user_id` |
| `LibraryId` | derived from `query_definition.library_ids[0]` (single library scope per collection) |
| `IsPublic` | `user_personal_collections.is_shared` |
| `CreatedAt` / `UpdatedAt` | timestamps as-is |

Similar table for Playlist and SmartCollection. **Write this out in a working note** — you'll reference it in Task 2.

No commit for this task.

---

## Task 2: Rewrite `ABSCollectionStore`

**Files:**
- Modify: `internal/audiobooks/abs_collection_store.go`
- Test: `internal/audiobooks/abs_collection_store_test.go` (create if missing)

The current 8 methods are: `ListUserCollections`, `GetCollection`, `CreateCollection`, `UpdateCollection`, `DeleteCollection`, `ListCollectionItems`, `AddCollectionItem`, `RemoveCollectionItem`.

For each, the rewrite swaps `FROM abs_user_collections` / `FROM abs_collection_items` for the canonical equivalents.

- [ ] **Step 1: Write a failing test for `ListUserCollections`**

Append to `internal/audiobooks/abs_collection_store_test.go` (create the file if absent, declaring `package audiobooks`):

```go
package audiobooks

import (
	"context"
	"testing"
	// ...add the test DB harness import discovered by the next step
)

func TestABSCollectionStoreListUserCollections(t *testing.T) {
	if testing.Short() {
		t.Skip("requires test DB")
	}
	ctx := context.Background()
	// Set up a test pool against migration head (>= 156).
	pool := newTestPool(t)
	defer pool.Close()

	// Insert one user_personal_collections row with collection_type='manual',
	// is_shared=true, library_ids=[9] in query_definition.
	_, err := pool.Exec(ctx, `
		INSERT INTO user_personal_collections
			(id, user_id, profile_id, name, description, collection_type,
			 is_shared, created_at, updated_at, creator_profile_id, query_definition)
		VALUES
			('test-c-1', 1, '', 'TestColl', 'desc', 'manual',
			 true, NOW(), NOW(), '', '{"library_ids":[9]}'::jsonb)
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	store := &ABSCollectionStore{pool: pool}
	got, err := store.ListUserCollections(ctx, "1", "")
	if err != nil {
		t.Fatalf("ListUserCollections: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d collections, want 1", len(got))
	}
	if got[0].Id != "test-c-1" || got[0].Name != "TestColl" || !got[0].IsPublic {
		t.Errorf("unexpected fields: %+v", got[0])
	}
}
```

**Verify the test-pool harness:** `grep -rln "func newTestPool\|func testPool" --include="*_test.go" .` finds the project's convention. If none exists, this test stays gated by `testing.Short()` and is best-effort — flag in the PR.

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/audiobooks/ -run TestABSCollectionStoreListUserCollections -v
```

Expected: FAIL because `ListUserCollections` still queries the dropped `abs_user_collections` table.

- [ ] **Step 3: Rewrite `ListUserCollections`**

Replace the existing function body with:

```go
func (s *ABSCollectionStore) ListUserCollections(ctx context.Context, userID, profileID string) ([]abs.Collection, error) {
	const q = `
		SELECT
			id, name, description, COALESCE(query_definition->'library_ids'->>0, '0')::int AS library_id,
			user_id, is_shared, created_at, updated_at
		FROM user_personal_collections
		WHERE collection_type = 'manual'
		  AND user_id = $1::int
		  AND (profile_id = $2 OR ($2 = '' AND profile_id = ''))
		ORDER BY created_at DESC
	`
	rows, err := s.pool.Query(ctx, q, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("abs collection list: %w", err)
	}
	defer rows.Close()

	var out []abs.Collection
	for rows.Next() {
		var c abs.Collection
		if err := rows.Scan(&c.Id, &c.Name, &c.Description, &c.LibraryId, &c.UserId, &c.IsPublic, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan abs collection: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
```

Adjust the `Scan` order to match `abs.Collection`'s actual field types — read the struct from `internal/audiobooks/abs/` to confirm. If `Id` is named `ID` per Go convention, fix accordingly.

- [ ] **Step 4: Run the test, verify it passes**

```bash
go test ./internal/audiobooks/ -run TestABSCollectionStoreListUserCollections -v
```

Expected: PASS.

- [ ] **Step 5: Rewrite the remaining 7 methods using the same pattern**

For each method, apply this template:

| Method | Canonical operation |
|---|---|
| `GetCollection(id)` | `SELECT … WHERE id=$1 AND collection_type='manual'` |
| `CreateCollection(c)` | `INSERT INTO user_personal_collections (...) VALUES (..., 'manual', ..., jsonb_build_object('library_ids', jsonb_build_array(c.LibraryId)))` |
| `UpdateCollection(c)` | `UPDATE user_personal_collections SET name=$2, description=$3, is_shared=$4, query_definition=$5, updated_at=NOW() WHERE id=$1 AND collection_type='manual'` |
| `DeleteCollection(id)` | `DELETE FROM user_personal_collections WHERE id=$1 AND collection_type='manual'` (cascades to items via the existing FK) |
| `ListCollectionItems(collectionID)` | `SELECT media_item_id, sub_item_id, position FROM user_personal_collection_items WHERE collection_id=$1 ORDER BY position, added_at` — map `media_item_id`→`abs.CollectionItem.LibraryItemId`, `sub_item_id`→`abs.CollectionItem.EpisodeId` |
| `AddCollectionItem(collectionID, libraryItemID)` | `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, sub_item_id, position, added_at) SELECT user_id, $1, $2, '', COALESCE(MAX(position)+1, 0), NOW() FROM user_personal_collections c LEFT JOIN user_personal_collection_items i ON i.collection_id = c.id WHERE c.id=$1 GROUP BY user_id` |
| `RemoveCollectionItem(collectionID, libraryItemID)` | `DELETE FROM user_personal_collection_items WHERE collection_id=$1 AND media_item_id=$2 AND sub_item_id=''` |

Each gets its own targeted test mirroring Step 1's structure: insert seed data, call the method, assert the canonical-table side-effect (`SELECT … FROM user_personal_collection_items …`).

For brevity, the plan shows the SQL above as a table; the actual implementation has each method as a complete function. Reuse the connection-error-wrapping style and the `fmt.Errorf("...: %w", err)` pattern already in the file.

- [ ] **Step 6: Run all tests for the file**

```bash
go test ./internal/audiobooks/ -run TestABSCollectionStore -v
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/audiobooks/abs_collection_store.go internal/audiobooks/abs_collection_store_test.go
git commit -m "refactor(audiobooks): ABS collection store queries canonical tables

Rewrites ABSCollectionStore's 8 methods to read/write
user_personal_collections (collection_type='manual') instead of the
dropped abs_user_collections / abs_collection_items tables. Library
scope is now encoded in query_definition.library_ids.

ABS HTTP handlers consume the same method signatures; wire shape
is unchanged."
```

---

## Task 3: Rewrite `ABSPlaylistStore`

**Files:**
- Modify: `internal/audiobooks/abs_playlist_store.go`
- Test: `internal/audiobooks/abs_playlist_store_test.go` (create if missing)

Same pattern as Task 2. Methods: `ListUserPlaylists`, `GetPlaylist`, `coverArg`, `CreatePlaylist`, `UpdatePlaylist`, `DeletePlaylist`, `ListPlaylistItems`, `AddPlaylistItem`, `RemovePlaylistItem`.

Filter on `collection_type='playlist'` instead of `'manual'`.

**Special handling for `coverArg`:** `abs_playlists` has a `cover_item` foreign key to `media_items`; the canonical `user_personal_collections` has only `poster_url` (text). Per spec §6 open question, the design recommends dropping `cover_item` and regenerating poster URLs from the first item. For this rewrite:

- `coverArg` becomes a noop / removed: the new CreatePlaylist doesn't accept a cover_item field on insert.
- The wire-shape struct can keep its `CoverItem` field; the rewrite always serializes it as `""` (empty). If the ABS app surfaces a cover, it falls back to the first item's poster — same as the audiobook UI does today.

- [ ] **Step 1: Write a failing test for `ListUserPlaylists`**

```go
func TestABSPlaylistStoreListUserPlaylists(t *testing.T) {
	if testing.Short() {
		t.Skip("requires test DB")
	}
	ctx := context.Background()
	pool := newTestPool(t)
	defer pool.Close()

	_, err := pool.Exec(ctx, `
		INSERT INTO user_personal_collections
			(id, user_id, profile_id, name, description, collection_type,
			 is_shared, created_at, updated_at, creator_profile_id)
		VALUES
			('test-pl-1', 1, '', 'TestPlaylist', 'desc', 'playlist',
			 false, NOW(), NOW(), '')
	`)
	if err != nil { t.Fatalf("seed: %v", err) }

	store := &ABSPlaylistStore{pool: pool}
	got, err := store.ListUserPlaylists(ctx, "1", "")
	if err != nil { t.Fatalf("ListUserPlaylists: %v", err) }
	if len(got) != 1 || got[0].Id != "test-pl-1" || got[0].Name != "TestPlaylist" {
		t.Errorf("unexpected: %+v", got)
	}
}
```

- [ ] **Step 2: Run the test, fail, then rewrite**

```bash
go test ./internal/audiobooks/ -run TestABSPlaylistStoreListUserPlaylists -v
```

Expected: FAIL until rewrite lands.

- [ ] **Step 3: Rewrite `ListUserPlaylists`**

```go
func (s *ABSPlaylistStore) ListUserPlaylists(ctx context.Context, userID, profileID string) ([]abs.Playlist, error) {
	const q = `
		SELECT id, name, description, user_id, is_shared, created_at, updated_at
		FROM user_personal_collections
		WHERE collection_type = 'playlist'
		  AND user_id = $1::int
		  AND (profile_id = $2 OR ($2 = '' AND profile_id = ''))
		ORDER BY created_at DESC
	`
	rows, err := s.pool.Query(ctx, q, userID, profileID)
	if err != nil { return nil, fmt.Errorf("abs playlist list: %w", err) }
	defer rows.Close()

	var out []abs.Playlist
	for rows.Next() {
		var p abs.Playlist
		if err := rows.Scan(&p.Id, &p.Name, &p.Description, &p.UserId, &p.IsPublic, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan abs playlist: %w", err)
		}
		// CoverItem deliberately left as zero value; see plan + spec §6.
		out = append(out, p)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Rewrite the remaining methods**

| Method | Canonical operation |
|---|---|
| `GetPlaylist(id)` | `SELECT … WHERE id=$1 AND collection_type='playlist'` |
| `CreatePlaylist(p)` | `INSERT … VALUES (..., 'playlist', ...)` — drop cover_item handling |
| `UpdatePlaylist(p)` | `UPDATE … SET name=$2, description=$3, is_shared=$4, updated_at=NOW() WHERE id=$1 AND collection_type='playlist'` |
| `DeletePlaylist(id)` | `DELETE FROM user_personal_collections WHERE id=$1 AND collection_type='playlist'` |
| `ListPlaylistItems(playlistID)` | `SELECT media_item_id, sub_item_id, position FROM user_personal_collection_items WHERE collection_id=$1 ORDER BY position, added_at` — map `sub_item_id`→`abs.PlaylistItem.EpisodeId` (empty string when no episode) |
| `AddPlaylistItem(playlistID, libraryItemID, episodeID)` | `INSERT INTO user_personal_collection_items (user_id, collection_id, media_item_id, sub_item_id, position, added_at) SELECT user_id, $1, $2, $3, COALESCE(MAX(i.position)+1, 0), NOW() FROM user_personal_collections c LEFT JOIN user_personal_collection_items i ON i.collection_id = c.id WHERE c.id=$1 GROUP BY user_id` |
| `RemovePlaylistItem(playlistID, libraryItemID, episodeID)` | `DELETE FROM user_personal_collection_items WHERE collection_id=$1 AND media_item_id=$2 AND sub_item_id=$3` |
| `coverArg(cover string)` | Delete entirely — no longer used. |

- [ ] **Step 5: Run all playlist tests**

```bash
go test ./internal/audiobooks/ -run TestABSPlaylistStore -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/audiobooks/abs_playlist_store.go internal/audiobooks/abs_playlist_store_test.go
git commit -m "refactor(audiobooks): ABS playlist store queries canonical tables

Rewrites ABSPlaylistStore methods to read/write user_personal_collections
(collection_type='playlist') and user_personal_collection_items
(sub_item_id for episode-level entries). Drops the cover_item field
(no longer modeled in the canonical store; clients fall back to
first-item poster, same as audiobook UI today).

Wire shape preserved otherwise."
```

---

## Task 4: Rewrite `ABSSmartCollectionStore`

**Files:**
- Modify: `internal/audiobooks/abs_smart_collection_store.go`
- Test: `internal/audiobooks/abs_smart_collection_store_test.go` (create if missing)

Filter on `collection_type='smart'`. The rule DSL stored in `query_definition` (jsonb) — same column the silo-native query_definition uses. Note: the source column on `abs_smart_collections` was named `query_def` (not `query_definition`); the canonical column is `query_definition`. **Read+write target the canonical name.**

`color` and `is_pinned` from `abs_smart_collections` have no canonical equivalent (spec §6 defers them). The rewrite emits empty/false for those fields in the JSON output. If the ABS app silently relies on either, ship a follow-up to add the columns.

- [ ] **Step 1: Write a failing test**

```go
func TestABSSmartCollectionStoreListUserSmart(t *testing.T) {
	if testing.Short() { t.Skip("requires test DB") }
	ctx := context.Background()
	pool := newTestPool(t)
	defer pool.Close()

	_, err := pool.Exec(ctx, `
		INSERT INTO user_personal_collections
			(id, user_id, profile_id, name, description, collection_type,
			 is_shared, created_at, updated_at, creator_profile_id, query_definition)
		VALUES
			('test-sc-1', 1, '', 'TestSmart', '', 'smart',
			 false, NOW(), NOW(), '', '{"rules":[]}'::jsonb)
	`)
	if err != nil { t.Fatalf("seed: %v", err) }

	store := &ABSSmartCollectionStore{pool: pool}
	got, err := store.ListUserSmartCollections(ctx, "1", "")
	if err != nil { t.Fatalf("ListUserSmartCollections: %v", err) }
	if len(got) != 1 || got[0].Id != "test-sc-1" {
		t.Errorf("unexpected: %+v", got)
	}
}
```

- [ ] **Step 2: Rewrite the 6 methods**

| Method | Canonical operation |
|---|---|
| `ListUserSmartCollections` | `SELECT id, name, description, query_definition, user_id, is_shared FROM user_personal_collections WHERE collection_type='smart' AND user_id=$1::int AND (profile_id=$2 OR ...) ORDER BY created_at DESC` |
| `GetSmartCollection(id)` | same with `WHERE id=$1 AND collection_type='smart'` |
| `CreateSmartCollection(c)` | `INSERT … 'smart' … query_definition=$N::jsonb` |
| `UpdateSmartCollection(c)` | `UPDATE … SET name=$2, description=$3, query_definition=$4, is_shared=$5, updated_at=NOW() WHERE id=$1 AND collection_type='smart'` |
| `DeleteSmartCollection(id)` | `DELETE FROM user_personal_collections WHERE id=$1 AND collection_type='smart'` |
| Materializer (if present) | Calls `internal/smartcoll.Evaluate(...)` against `media_items` filtered by `query_definition.library_ids` |

For `color` and `is_pinned`: read from `abs.SmartCollection` zero-value on read paths; ignore on write paths. If the wire shape unconditionally includes them, leave the zero values in place — clients receive `"color":"", "is_pinned":false`.

- [ ] **Step 3: Tests + commit**

Same shape as Tasks 2 and 3. Commit message:

```
refactor(audiobooks): ABS smart collection store queries canonical tables

Rewrites ABSSmartCollectionStore methods to read/write user_personal_collections
(collection_type='smart'). Rule DSL goes into query_definition (formerly
the abs_smart_collections.query_def column).

color and is_pinned have no canonical analog; emitted as zero values.
See spec §6 for the deferred decision on those columns.
```

---

## Task 5: Wire-shape regression test (snapshot diff)

**Files:**
- Add or extend: an integration test that drives the ABS handlers and asserts the JSON output for each endpoint hasn't changed.

If a snapshot/golden-file test framework already exists in the repo, use it (`grep -rln 'goldenfile\|snapshot' --include="*_test.go" internal/audiobooks/`). Otherwise this task is **manual verification** captured in the MR description:

- [ ] **Step 1: Seed identical fixtures pre- and post-rewrite**

Before the rewrite commits, seed one of each:
- An `abs_user_collections` row (pre-migration) with name "FixtureColl", 1 item.
- An `abs_playlists` row (pre-migration) with name "FixturePL", 1 item with sub_item.
- An `abs_smart_collections` row (pre-migration) with name "FixtureSmart", rules `{"any":[…]}`.

Capture the JSON response from each list endpoint. Save to `/tmp/wire_before_*.json`.

- [ ] **Step 2: Run migration 156, then seed equivalent rows in canonical tables**

Insert one of each `user_personal_collections` row with the same Id, Name, etc.

Capture the JSON response. Save to `/tmp/wire_after_*.json`.

- [ ] **Step 3: Diff**

```bash
for kind in collections playlists smart_collections; do
    diff /tmp/wire_before_${kind}.json /tmp/wire_after_${kind}.json
done
```

Expected: empty diff for each. If anything differs, note the field and either patch the rewrite to match or flag it as a wire-shape regression in the PR description so the ABS team is aware.

- [ ] **Step 4: Commit the test (if framework supports it) or paste the diff into the PR description**

```bash
git add internal/audiobooks/abs_wireshape_test.go  # if applicable
git commit -m "test(audiobooks): wire-shape snapshot for ABS adapters

Locks in JSON parity for /api/libraries/{id}/collections|playlists|
smart-collections after the canonical-table cutover."
```

---

## Verification (after merge)

1. Silo binary starts cleanly against a migration-156 DB.
2. ABS endpoints respond:
   - `GET /api/libraries/9/collections` — returns the 0 manual collections, no errors.
   - `GET /api/libraries/9/playlists` — returns the 1 promoted playlist with `Id` preserved.
   - `GET /api/libraries/9/smart-collections` — returns 0 smart collections, no errors.
3. ABS Android app (or whatever client is in use) opens the library, sees the same content it saw pre-migration.
4. Creating a new collection from the ABS app inserts into `user_personal_collections`, not anywhere else:

   ```sql
   SELECT collection_type, COUNT(*) FROM user_personal_collections GROUP BY 1;
   ```

   Expected: counts increase for the relevant `collection_type` as the user creates content.

---

## Self-Review

**Spec coverage:**
- Collection store rewrite ✓ (Task 2)
- Playlist store rewrite ✓ (Task 3)
- Smart collection store rewrite ✓ (Task 4)
- Wire-shape preservation ✓ (Tasks 1, 5)
- Drop cover_item ✓ (Task 3, per spec §6)
- color / is_pinned deferred ✓ (Task 4, per spec §6)

**Placeholder scan:** Task 2 Step 5 lists 7 remaining methods as a table rather than spelling out the full Go body of each. The table gives the exact SQL semantics, the column mappings, and a pointer at the existing file's error-wrap style — sufficient for a competent implementer to write the bodies. The full code for each method would balloon this plan from ~700 to ~2000 lines; the table form is the right granularity. Same applies to Task 3 Step 4 and Task 4 Step 2. Each table entry could equally be a TDD task on its own; the implementer is free to expand any row into a write-test-fail-implement-pass-commit cycle.

**Type consistency:** `abs.Collection`, `abs.Playlist`, `abs.SmartCollection`, `abs.CollectionItem`, `abs.PlaylistItem` referenced consistently. `collection_type` enum values `'manual'`, `'playlist'`, `'smart'` consistent. `sub_item_id` column name consistent.

**Risk:** Largest sub-project of the four. Three files rewritten end-to-end. The biggest risk is wire-shape regressions where a struct field's serialized name differs subtly between adapters — Task 5 mitigates with the diff approach, but it's manual unless a snapshot harness exists. Sub-project 1 must land first; sub-project 2 must land first if Task 4 imports the new `internal/smartcoll` path.
