# Audiobooks Absorption — Discovery Findings

Produced by sub-plan 1, Task 1. Locks data-model and integration
decisions for migrations 139–142 and downstream sub-plans.

## D1 — Next migration number

Highest existing migration: 138_search_number_word_normalization.up.sql
Next available number: 139
Sub-plan 1 will use migrations 139 through 142.

## D2 — `media_libraries` kind/type column

The spec refers to `media_libraries` but the actual table is `media_folders`.
`media_folders` has an existing column `type` (text, NOT NULL) that discriminates
library content. Current values in production: `movies`, `series`, `mixed`.

Existing column 'type' (text) discriminates library content on `media_folders`.
Task 4 (migration 141) should ADD the value `audiobooks` to the type vocabulary
rather than add a new column. The audiobook scanner branch will set
`media_folders.type = 'audiobooks'` for audiobook libraries. No schema change
needed for the column itself; migration 141 becomes a no-op DDL migration that
documents the new allowed value and adds any supporting indexes if needed.

No CHECK constraint or enum enforces the `type` column values, so adding
`audiobooks` as a value requires no DDL constraint change.

## D3 — `media_files.chapters` JSONB shape

Sample chapter JSON (live data):
[{"index": 0, "title": "Intro start", "source": "embedded", "end_seconds": 27.944, "start_seconds": 0}, {"index": 1, "title": "Intro end", "source": "embedded", "end_seconds": 1343, "start_seconds": 27.944}]

Sub-plan 2 (scanner) MUST emit objects with the same keys when writing
audiobook chapters so the existing player and serialization code accept
them without changes.

Required keys: `index` (integer), `title` (text), `source` (text),
`start_seconds` (float), `end_seconds` (float).

## D4 — `user_watch_progress` scoping (profile vs user)

user_watch_progress is profile-scoped: column 'profile_id' (text, NOT NULL, FK via
composite PK on user_id + profile_id + media_item_id). Audiobook progress slots in directly.

Additional context: the table also stores `last_file_id`, `last_resolution`,
`last_hdr`, `last_codec_video`, and `last_edition_key`. For audiobooks, only
`position_seconds`, `duration_seconds`, `completed`, and `last_file_id` are
semantically relevant; the video-specific columns (`last_resolution`,
`last_hdr`, `last_codec_video`) will be NULL for audiobook progress rows,
which is acceptable.

## D5 — `user_playback_sessions` audiobook fit

Column list:
                    Table "public.user_playback_sessions"
      Column      |           Type           | Collation | Nullable | Default
------------------+--------------------------+-----------+----------+---------
 session_id       | text                     |           | not null |
 user_id          | integer                  |           | not null |
 profile_id       | text                     |           | not null |
 media_file_id    | integer                  |           | not null |
 play_method      | text                     |           | not null |
 position_seconds | double precision         |           | not null | 0
 is_paused        | boolean                  |           | not null | false
 started_at       | timestamp with time zone |           | not null | now()
 updated_at       | timestamp with time zone |           | not null | now()

Columns required by audiobook sessions: media_item_id (or equivalent),
profile/user FK, started_at, current_position_seconds (or equivalent),
status. Mark any required column as MISSING and surface in sub-plan 3.

Assessment:
- media_item_id: MISSING — table stores `media_file_id` (FK to media_files) rather
  than `media_item_id`. For audiobooks, a file maps to one audiobook item, so the
  item can be looked up via the file join. No schema change strictly required, but
  sub-plan 3 should note this indirect join cost.
- profile_id: PRESENT (text, not null)
- user_id: PRESENT (integer, not null)
- started_at: PRESENT
- position_seconds: PRESENT (as `position_seconds`)
- status / is_paused: PRESENT (as `is_paused`); no explicit `status` enum, but
  paused/playing state is representable.
- `play_method`: required for existing sessions; audiobook sessions must supply a
  value (e.g. `'direct'`).

No blocking gaps. Audiobook sessions can be written to `user_playback_sessions`
without migration using `media_file_id` as the join key.

## D6 — `people` / `item_people` role conventions

item_people.role storage: `kind` smallint (NOT NULL) — NOT a text `role` column.
The column is named `kind` with type `smallint`. No CHECK constraint or enum.

Existing kind values in use (mapped from models/media.go):
  1 = Actor, 2 = Director, 3 = Writer, 4 = Producer, 5 = GuestStar, 6 = Composer
  (6 = Composer defined in code but 0 rows in production data)

Sub-plan 2 will UPSERT `author` and `narrator` into item_people for
audiobook items. Since the role column is an unconstrained smallint (not
a text role column and not an enum or CHECK constraint), Sub-plan 2 must:
1. Add new PersonKind constants to `internal/models/media.go`:
   `PersonKindAuthor PersonKind = 7` and `PersonKindNarrator PersonKind = 8`
2. Add corresponding cases to `PersonKind.String()` returning `"Author"` and
   `"Narrator"` respectively.
No migration is needed to extend a constraint — the smallint column accepts
any integer value.

## D7 — Catalog FTS handling of `type='audiobook'`

Indexes / generated columns that filter by media_items.type:
- `001_schema.up.sql`: `idx_media_items_search` — GIN on `to_tsvector('english', title || ' ' || overview)` — NO type filter, indexes ALL rows
- `001_schema.up.sql`: `idx_media_items_search_exact_title` — btree on `lower(title)` — NO type filter
- `001_schema.up.sql`: `idx_media_items_search_overview` — GIN on overview tsvector — NO type filter
- `001_schema.up.sql`: `idx_media_items_search_title_fields` — GIN on weighted title/original_title/sort_title tsvector — NO type filter (rebuilt by migrations 127 and 138)
- `001_schema.up.sql`: `idx_media_items_type_created` — btree on `(type, created_at DESC)` — indexes ALL types, used for filtering by type
- `057_calendar_indexes.up.sql`: `idx_media_items_movie_release_date` — btree WHERE `type = 'movie'` — movie-only, not FTS
- `103_media_items_last_air_date_denorm.up.sql`: `idx_media_items_last_air_date_at` — btree WHERE `type = 'series'` — series-only, not FTS
- `105_media_items_title_normalized.up.sql`: `idx_media_items_title_normalized_trgm` — gin trigram on `title_normalized` — NO type filter (rebuilt by 127 and 138)
- `138_search_number_word_normalization.up.sql`: `idx_media_items_search_title_fields` (current) — GIN on weighted tsvector — NO type filter
- `138_search_number_word_normalization.up.sql`: `idx_media_items_title_normalized_trgm` (current) — gin trigram — NO type filter

Verdict: audiobooks WILL be FTS-searchable out of the box.

All FTS indexes on `media_items` operate on the full table with no type
restriction. A row with `type = 'audiobook'` will be indexed automatically
by `idx_media_items_search_title_fields` and `idx_media_items_title_normalized_trgm`
as soon as it is inserted. No extra migration is needed for Sub-plan 3 to
extend type filters.

## D8 — First-party scheduled-task registration

First-party scheduled tasks register at: `cmd/silo/main.go:1239–1285`

Registration call shape (from existing first-party tasks):
```go
taskMgr.Register(tasks.NewSyncCollectionsTask(collectionSyncScheduler))
```

Full interface required (from `internal/taskmanager/tasks/sync_collections.go`):
- `Key() string` — unique string key e.g. `"sync_podcast_feeds"`
- `Name() string` — human-readable name
- `Description() string` — human-readable description
- `Category() taskmanager.TaskCategory` — e.g. `taskmanager.TaskCategoryLibrary`
- `IsHidden() bool`
- `DefaultTriggers() []taskmanager.TriggerConfig` — e.g. interval trigger
- `Execute(ctx context.Context, progress taskmanager.ProgressReporter) error`

Sub-plan 5 (podcasts) will register `podcastfeed.Refresher` at `cmd/silo/main.go`
in the task registration block (around line 1259–1265) using the same pattern:
```go
taskMgr.Register(tasks.NewSyncPodcastFeedsTask(podcastFeedRefresher))
```
