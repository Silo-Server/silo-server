# Split Versions / Per-Folder Reassign with History Reattribution — Design Spec

**Date:** 2026-07-06
**Status:** Proposed
**Scope:** scanner / catalog / metadata / admin API / web admin
**Related:** `docs/architecture/deterministic-content-id.md`,
`migrations/sql/078_content_groups_and_locations.sql`,
`migrations/sql/20260614120000_content_id_online_reid.sql`

Commands and paths assume the repository root is the cwd.

## Problem

The scanner groups files into logical items by `ContentGroupKey`
(normalized title + year, `internal/naming/group_identity.go`) unless a
structured provider tag (`{tmdb-…}`, `[tvdbid-…]`, `tt…`) anchors the
identity. When two different titles parse to the same key (remakes,
sequels with sloppy names, "Movie (2019)" vs "Movie (2019) Directors Cut"
folders that are actually different films), their files are merged into a
single item as "versions".

Today there is **no way to fix a wrong merge in-app**:

- Item-level rematch (`POST /admin/items/{id}/match/search|apply`,
  `internal/api/handlers/admin_match.go`) re-identifies the *whole* item —
  every file follows.
- Group/root overrides (`internal/scanner/group_override_repo.go`,
  `HandleUpsertRootOverride` in `internal/api/handlers/libraries.go`) force
  an identity for an *entire* group, and the root-override endpoints
  explicitly refuse ambiguous roots with 409 `ambiguous_root` — telling the
  admin to "override the group after splitting", an operation that does not
  exist.
- The only real fix is renaming folders on disk with provider tags and
  rescanning — and even then, watch state accrued under the merged item is
  not reattributed.

Symmetrically, when a **wrong split** is merged back (two items that are
one title), watch state on the losing item is silently orphaned.

## Goal

An admin operation that:

1. **Splits** a selected subset of an item's files (typically one folder)
   into a different logical item — an existing item, a newly identified
   one, or an unmatched `local-` placeholder.
2. Makes the assignment **durable across rescans** (path-scoped identity
   overrides, not just a one-time row edit).
3. **Reattributes user watch state** (progress, history, downloads,
   session records) to the item that actually owns the plays, using
   per-file evidence where it exists.
4. Provides the same reattribution when items are **merged**, so the
   existing `rebindItemToExistingItem` path stops orphaning state.

Non-goals: automatic detection of wrong merges (ambiguity flagging already
exists and stays), cross-server state sync, file-level dedup.

## Existing machinery this builds on

| Asset | Where | Reused for |
| ----- | ----- | ---------- |
| Deterministic `content_id` (`movie-tmdb-…`) | `internal/contentid` | The split target's id is *derived*, never minted — splitting to TMDB 603 lands on `movie-tmdb-603` whether or not it already exists. |
| `silo_rename_content_id` + `ON UPDATE CASCADE` on the content-id FK family | `migrations/sql/20260614120000_content_id_online_reid.sql` | Whole-item renames during merge; the FK cascade also means per-row `UPDATE … SET media_item_id` is safe everywhere. |
| `rebindItemToExistingItem` / `canonicalizeLocalContentID` | `internal/metadata/canonicalize.go` | Merge path; gains reattribution. |
| Skeleton creation + match apply | `internal/metadata/service.go`, `internal/api/handlers/admin_match.go` | Creating/identifying the split-off item; candidate search UI already exists (`web/src/components/MatchItemDialog.tsx`). |
| `playback_history_admin` (per-session `media_file_id`, `media_item_id`, user/profile, timestamps, `completed`) | `migrations/sql/001_schema.sql` | The evidence table for attributing item-level history rows to files. |
| `user_watch_progress.last_file_id` | `migrations/sql/001_schema.sql` | Direct per-file attribution of resume state. |
| Group override application during inference | `internal/scanner/group_inference.go` (`applyGroupOverrides`) | Extended to path-scoped overrides so splits survive rescans. |

## Design

### 1. Unit of operation: files, addressed by folder

The API operates on explicit `media_files.id` sets. The UI presents the
item's files grouped by `observed_root_path` with the parsed identity
evidence (`base_title`, `base_year`, `identity_confidence`,
`identity_json`), because "this folder is a different movie" is the
overwhelmingly common case. Selecting a folder selects its files.

### 2. Durability: path-scoped identity overrides

Wrong merges are produced by inference, so a one-time row edit would be
undone by the next scan. The split persists **identity overrides scoped to
paths**, generalizing the existing group override:

New table `media_identity_overrides` (new timestamped migration via
`make migrate-create NAME=media_identity_overrides`):

```sql
media_identity_overrides (
  id             bigserial PRIMARY KEY,
  media_folder_id int  NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
  scope          text NOT NULL CHECK (scope IN ('root', 'file')),
  root_path      text NOT NULL DEFAULT '',   -- scope='root'
  file_path      text NOT NULL DEFAULT '',   -- scope='file'
  forced_type    text NOT NULL DEFAULT '',
  forced_title   text NOT NULL DEFAULT '',
  forced_year    int  NOT NULL DEFAULT 0,
  forced_tmdb_id text NOT NULL DEFAULT '',
  forced_imdb_id text NOT NULL DEFAULT '',
  forced_tvdb_id text NOT NULL DEFAULT '',
  note           text NOT NULL DEFAULT '',
  created_by_user_id int NULL,
  updated_by_user_id int NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (media_folder_id, scope, root_path, file_path)
)
```

Application point: `internal/naming/group_identity.go` /
`internal/scanner/group_inference.go`. Before bucketing, each file checks
for an override — `file` scope wins over `root` scope, which wins over the
existing group-key override. A forced provider ID acts exactly like a
structured tag in the name (`hasStructuredIDAnchor` semantics): it anchors
identity, resolves title conflicts, and — because `makeContentGroupKey`
must incorporate the anchor for overridden files — guarantees the file
lands in its own group even when its parsed title+year collides with the
neighbor it was wrongly merged with.

The existing `media_group_overrides` table stays for whole-group forcing;
`HandleUpsertRootOverride`'s 409 `ambiguous_root` branch is retired in
favor of pointing at the split flow. (Optionally, a follow-up migrates
group overrides into this table with `scope='group'`; not required for
v1 of this feature.)

Overrides are visible and deletable in the admin UI (extend the existing
library "roots" listing, `HandleListLibraryRoots`, which already joins
overrides).

### 3. The split operation

`POST /admin/items/{id}/split` (admin-gated, additive API):

```jsonc
{
  "file_ids": [123, 124],            // required, non-empty, all owned by {id}
  "target": {                         // one of:
    "provider_ids": {"tmdb": "603"}, //   identify (from match-candidate search)
    "content_id": "movie-tmdb-603",  //   attach to an existing item
    "unmatched": true                //   detach to a local- placeholder
  },
  "history_mode": "evidence",        // evidence | keep | move_all (default: evidence)
  "persist_override": true,          // default true; scope inferred (root if the
                                     // selection covers whole roots, else file)
  "dry_run": false
}
```

Validation guards:

- Selecting **all** of the item's files is rejected with a hint to use
  `match/apply` — that is a rematch, not a split.
- Target identity equal to the source item's identity is a no-op error.
- For series items, the selection must be root-aligned per series (you
  split a show's folder, not half a season); episode-level moves between
  two shows use the same call with `file` scope.

Execution (single transaction, then async follow-ups):

1. **Resolve target item.** Derive the deterministic `content_id` from the
   target provider IDs (`internal/contentid`). If the item exists, use it;
   otherwise create a skeleton via the metadata service (same path as
   scanner skeleton creation). `unmatched` targets get a `local-` id
   derived from the primary root path.
2. **Persist overrides** (unless `persist_override=false`): one `root`
   override per fully-selected root, `file` overrides for partial roots.
3. **Re-point files.** `UPDATE media_files SET content_id = <target>` for
   the selection. For series: re-derive `episode_id` for each moved file
   from the target series anchor + parsed S/E numbers
   (`contentid.ForEpisode`), creating season/episode skeleton rows as
   needed — the same shape the scanner produces.
4. **Reattribute user state** (§4).
5. **Reconcile scanner state.** Recompute `scanned_media_groups`,
   `media_group_locations`, `observed_media_locations`, and
   `media_item_groups` for the affected folder/roots by re-running group
   inference on the affected paths (not a full library scan). Locations
   that were `ambiguous` because of the merge resolve to their overridden
   groups.
6. **Post-commit:** queue a metadata refresh for the target item (and the
   source item, whose aggregate fields — runtime, editions, trailers —
   may have been polluted by the foreign files); invalidate jellycompat
   caches for both ids; emit an audit log entry (actor, item ids, file
   ids, history_mode, counts).

`POST /admin/items/{id}/split` with `"dry_run": true` (or a sibling
`/split/preview`) returns the full plan without writing: target
content_id (and whether it already exists), files moved, overrides to be
written, and the per-table reattribution counts including the list of
history rows classified `ambiguous` (§4.2) — this is what the
confirmation UI renders.

### 4. History reattribution

All user-state moves live in one shared helper so split and merge use the
same logic — proposed package `internal/catalog/reattribute`:

```go
// Moves user state tied to fromItem onto toItem, for the given moved files.
// movedFiles empty + wholeItem=true means "everything" (the merge case).
Reattribute(ctx, tx, fromItem, toItem string, movedFileIDs []int64,
            mode HistoryMode) (Report, error)
```

#### 4.1 Rows with per-file evidence — exact moves

| Table | Key | Rule |
| ----- | --- | ---- |
| `playback_history_admin` | `media_file_id` | `SET media_item_id = to` where `media_file_id` moved. Exact. |
| `user_downloads` | `media_file_id` | Same. |
| `user_playback_sessions`, `playback_sessions_sync` | `media_file_id` | Same; live sessions keep playing (file path unchanged), their item association is simply corrected. |
| `user_watch_progress` | `last_file_id` | Move rows whose `last_file_id` moved. PK is `(user_id, profile_id, media_item_id)`: on conflict with an existing progress row for the target item, keep the row with the newer `updated_at` and drop the other. Rows with NULL/unmoved `last_file_id` stay. |

For **series splits** the mapping is even stronger: moved files carry
parsed S/E numbers, and episode content ids are
`episode-<provider>-<seriesId>-<s>-<e>` — so every episode-level state row
maps old→new episode id deterministically. Progress/history/favorites
keyed on the *episode* ids of moved episodes move wholesale; ambiguity
only exists for movie-level rows.

#### 4.2 `user_watch_history` — evidence-based attribution

History rows carry no file reference; attribution uses
`playback_history_admin` as the evidence source. For each history row of
the source item (per user+profile):

- **moved**: that profile's play sessions for the source item exist and
  *all* of them are on moved files → row's `media_item_id` updated to the
  target.
- **stays**: sessions exist and none are on moved files → row untouched.
- **ambiguous**: mixed sessions, or no session evidence (rows predating
  `playback_history_admin`, imported history, retention gaps).

`history_mode` controls ambiguous rows: `evidence` (default) leaves them
on the source item and reports the count; `keep` leaves everything
(evidence moves still apply to the exact tables in §4.1); `move_all`
moves every history row — for the "this item was 100% the other movie all
along" case where the admin knows better than the evidence. The dry-run
report lists ambiguous rows (user, watched_at) so the admin can decide.

Watched/completed flags derived from history (continue-watching, watchlist
auto-removal via `watchlist.Maintainer`) recompute from the moved rows on
their normal paths; the helper fires the same completion-observer
notifications a rematch does, if any.

#### 4.3 Item-level rows with no file dimension — deliberate defaults

`user_favorites`, `user_watchlist`, `user_ratings`,
`user_personal_collection_items`, `library_collection_items`,
`user_home_item_dismissals`, `user_history_hidden_items`: a wrong merge
gives no signal about which title the user favorited. Default: **stay on
the source item**, counts surfaced in the report. `move_all` mode moves
these too (dedup on conflict). No copy-to-both — duplicating user intent
is worse than asking the user to re-favorite.

Non-user state that references the item (`media_item_embeddings`, cached
artwork, localizations, trailers, credits) is *not* migrated — it is
metadata, and the post-split refresh rebuilds it for both items.

#### 4.4 Merge reattribution

`rebindItemToExistingItem` (and the admin-facing merge this spec enables:
`POST /admin/items/{id}/merge {"into": "<content_id>"}`) calls the same
helper with `wholeItem=true`: every state row moves, with the same
PK-conflict rule (newer `updated_at` wins for progress; history rows
simply re-key; favorites/watchlist dedup on conflict). This closes the
existing orphaning bug where merged items strand their watch state.

### 5. Admin UI

- **Item page:** alongside "Fix match" (existing `MatchItemDialog`), a
  "Split versions…" action, enabled when the item has >1 file. Dialog:
  file list grouped by folder with identity evidence and per-file
  checkboxes → identity picker (reuses the match-candidate search) →
  dry-run confirmation showing the reattribution report → execute.
- **Library roots page:** ambiguous locations
  (`observed_media_locations.content_group_count > 1` or group state
  `ambiguous`) get a "Resolve…" action that opens the same dialog scoped
  to that root.
- Overrides listed and revocable on the roots page (revoking does not
  undo a past split; it only frees future scans to re-infer).

### 6. Client / compat impact

Split-off files become a *new* item id; jellycompat packs `content_id`
into the client-visible UUID, so clients simply see one item's version
list shrink and a new item appear on next library sync — no client-side
changes required in `silo-android` / `silo-apple`. Resume state moves
server-side, so "Continue Watching" follows the correct title
automatically. Active playback sessions are unaffected (keyed by file).

### 7. Failure & idempotency

- The split transaction is atomic: overrides + file re-point +
  reattribution + scanner-state reconcile commit together; metadata
  refresh and cache invalidation are post-commit and self-healing (refresh
  debt already retries).
- Re-running the same split is a no-op (files already on target; override
  upsert idempotent).
- A rescan after a crash converges: overrides are already persisted, and
  group inference reproduces the same assignment. If the crash landed
  *before* the transaction committed, nothing changed.
- `playback_history_admin` is best-effort evidence (subject to its
  retention/cleanup, `internal/catalog/orphan_cleanup.go`); absence of
  evidence degrades to `ambiguous`, never to a wrong move.

### 8. Risks / open questions

- **Group-key incorporation of overrides** must be deterministic and
  stable (`makeContentGroupKey` gains an anchor component for overridden
  files). Bump `group_key_version` handling is *not* needed — overridden
  files just occupy distinct keys within v1.
- **Series episode re-derivation** assumes parsed S/E numbers are correct
  for moved files; files with unparsable numbering are rejected from the
  selection with a per-file reason (fix naming first).
- **`local-` targets** key on path; a later rename of the split-off folder
  re-IDs the local item (accepted limitation, same as scanner behavior).
- **Retention window** of `playback_history_admin` bounds history
  attribution quality; if it proves too short in practice, a follow-up
  adds `media_file_id` (nullable) to `user_watch_history` written at
  playback-stop time, making future attribution exact.

## Addendum (2026-07-06): structured tags now anchor group keys

A field report validated during implementation showed the highest-frequency
wrong-merge cause is worse than assumed: two *correctly tagged* folders —
"Passenger (2026) `[tmdb-1368314]`" and "The Passenger (2026)
`[tmdb-1285959]`" — merged into one item with state `resolved`, because
`makeContentGroupKey` is title+year only and title normalization strips the
leading article. The explicit provider tags were parsed but never entered the
group key.

Fix shipped with this feature: `InferGroupIdentity` now derives the same
anchored key shape used for identity overrides (`v1|movie|anchor|tmdb-…`)
whenever structured provider IDs are present. Files tagged with the same
provider ID always share a group (cross-folder versions keep working); files
tagged apart can never merge. Untagged files keep title+year keys, so
untagged libraries see no key churn. Tagged libraries re-key on next scan;
content ids are unaffected (they are deterministic from the same tags), so
this is scanner-internal state churn only. Items merged *before* this fix
still need the split flow to repair.

## Implementation order

1. Migration: `media_identity_overrides`; inference application in
   `internal/naming` / `internal/scanner` (+ tests: forced-ID splits a
   colliding key, file scope beats root scope, rescan convergence).
2. `internal/catalog/reattribute` helper + tests (per-table rules,
   PK-conflict handling, evidence classification) — wire into
   `rebindItemToExistingItem` first (pure win, no new API).
3. Split endpoint (+ dry run) and merge endpoint in
   `internal/api/handlers`; scanner-state reconcile.
4. Web admin dialog + roots-page "Resolve…" action; retire the
   `ambiguous_root` 409 dead-end.
5. Audit log + docs; follow-up issue for `user_watch_history.media_file_id`.
