# Audiobooks Absorption — Sub-plan 1: Discovery + Schema

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve open data-model questions from the audiobooks spec, then land the foundational schema (two new tables, optional one column add, one feature-flag setting) and a compiling-but-empty `internal/audiobooks/` package scaffold. No user-visible behavior change after this sub-plan; silo continues to operate exactly as it does today.

**Architecture:** Six tasks total. Task 1 audits silo-server's existing schema/code and writes a findings doc that locks the remaining decisions. Tasks 2–5 land additive SQL migrations (idempotent up + symmetric down). Task 6 creates a Go package skeleton with one no-op service constructor wired into the dependency-injection point in `cmd/silo`, which proves the new package compiles inside the rest of the binary without changing any runtime behavior.

**Tech Stack:** Go 1.26 + pgx; PostgreSQL 18 (pgvector image); plain `.up.sql` / `.down.sql` migrations applied at startup by `internal/database/migrate.go`. Frontend out of scope for this sub-plan.

**Source spec:** `docs/superpowers/specs/2026-05-24-audiobooks-absorption-design.md`

---

## File Structure

| Path | Created/Modified | Purpose |
|---|---|---|
| `docs/superpowers/plans/artifacts/2026-05-24-audiobooks-discovery-findings.md` | Create | Findings doc produced by Task 1. Locks the remaining schema/code decisions. |
| `migrations/147_abs_sessions.up.sql` | Create | New table parallel to `jellycompat_sessions`. |
| `migrations/147_abs_sessions.down.sql` | Create | Drop the table. |
| `migrations/157_podcast_feeds.up.sql` | Create | New table for RSS-subscribed podcast metadata. |
| `migrations/157_podcast_feeds.down.sql` | Create | Drop the table. |
| `migrations/159_media_folders_kind_noop.up.sql` | Create (conditional — see Task 4 step 1) | Document `media_folders.type='audiobooks'`; no column change needed. |
| `migrations/159_media_folders_kind_noop.down.sql` | Create (same condition) | No-op rollback. |
| `migrations/160_audiobooks_feature_flag.up.sql` | Create | Insert `audiobooks.enabled='false'` into `server_settings`. |
| `migrations/160_audiobooks_feature_flag.down.sql` | Create | Delete the setting row. |
| `internal/audiobooks/doc.go` | Create | Package comment + intent. |
| `internal/audiobooks/service.go` | Create | Empty `Service` struct, `New` constructor, `Enabled()` reader. |
| `internal/audiobooks/service_test.go` | Create | One unit test proving the package compiles and `Enabled()` returns the setting value. |
| `cmd/silo/main.go` | Modify | Construct `audiobooks.New(...)` once during startup so the scaffold is referenced. No routes are mounted, no tasks registered. |

Migrations and the scaffold compile and run independently from the audiobooks ports that arrive in later sub-plans.

---

## Task 1: Discovery Audit

Audits silo-server's existing schema and code to resolve the spec's open
Risk questions. Produces one findings document and one commit. No code
changes elsewhere.

**Files:**
- Create: `docs/superpowers/plans/artifacts/2026-05-24-audiobooks-discovery-findings.md`

### Step 1.1: Create the findings doc skeleton

- [ ] **Create** `docs/superpowers/plans/artifacts/2026-05-24-audiobooks-discovery-findings.md` with this exact content:

```markdown
# Audiobooks Absorption — Discovery Findings

Produced by sub-plan 1, Task 1. Locks data-model and integration
decisions for migrations 139–142 and downstream sub-plans.

## D1 — Next migration number

(Filled in step 1.2)

## D2 — `media_libraries` kind/type column

(Filled in step 1.3)

## D3 — `media_files.chapters` JSONB shape

(Filled in step 1.4)

## D4 — `user_watch_progress` scoping (profile vs user)

(Filled in step 1.5)

## D5 — `user_playback_sessions` audiobook fit

(Filled in step 1.6)

## D6 — `people` / `item_people` role conventions

(Filled in step 1.7)

## D7 — Catalog FTS handling of `type='audiobook'`

(Filled in step 1.8)

## D8 — First-party scheduled-task registration

(Filled in step 1.9)
```

### Step 1.2: D1 — Migration number

- [ ] **Run:** `ls migrations/ | grep -E '^[0-9]+_' | sort -t_ -k1 -n | tail -3`

  Expected output is three filenames whose numeric prefixes are consecutive.
  Whatever the highest prefix is, the next available number is **(highest + 1)**.

- [ ] **Append to the findings doc** under the D1 heading:

  ```text
  Highest existing migration: <NN>_<name>.up.sql
  Next available number: <NN+1>
  Sub-plan 1 will use migrations <NN+1> through <NN+4>.
  ```

  (Replace the placeholders with the observed values.) This locks the
  numbering used by Tasks 2–5. If the next number is not 139, update the
  filenames in Tasks 2–5 accordingly when you reach them.

### Step 1.3: D2 — `media_libraries` kind/type column

- [ ] **Run:** `sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "\d public.media_libraries"`

  Inspect the column list. Look for any column whose name or comment
  suggests a per-library *kind* / *type* / *category* discriminator
  (candidate names: `kind`, `library_type`, `category`, `media_type`,
  `content_type`).

- [ ] **Append to the findings doc** under the D2 heading exactly one of:

  - `No existing kind/type column on media_libraries. Task 4 adds 'kind' as planned.`
  - `Existing column '<NAME>' (<TYPE>) discriminates library content. Task 4 becomes a no-op migration; the audiobook scanner branch will read '<NAME>' instead of 'kind'.`

### Step 1.4: D3 — `media_files.chapters` JSONB shape

- [ ] **Run:** `sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "SELECT chapters FROM media_files WHERE chapters IS NOT NULL AND jsonb_array_length(chapters) > 0 LIMIT 1;"`

  This returns one sample chapter array used by silo's existing video
  chapter feature (added in migration 066).

- [ ] **Append to the findings doc** under the D3 heading:

  ```text
  Sample chapter JSON (live data):
  <paste the JSON exactly as returned, no edits>

  Sub-plan 2 (scanner) MUST emit objects with the same keys when writing
  audiobook chapters so the existing player and serialization code accept
  them without changes.
  ```

### Step 1.5: D4 — `user_watch_progress` scoping

- [ ] **Run:** `sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "\d public.user_watch_progress"`

- [ ] **Append to the findings doc** under the D4 heading exactly one of:

  - `user_watch_progress is profile-scoped: column '<profile_id>' (FK to user_profiles). Audiobook progress slots in directly.`
  - `user_watch_progress is user-scoped only (no profile column). Audiobook progress will share user state across all household profiles unless Sub-plan 3 adds a profile_id column. RAISE THIS AS A RISK in sub-plan 2 brainstorming.`

### Step 1.6: D5 — `user_playback_sessions` fit

- [ ] **Run:** `sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "\d public.user_playback_sessions"`

- [ ] **Append to the findings doc** under the D5 heading:

  ```text
  Column list:
  <paste the \d output verbatim>

  Columns required by audiobook sessions: media_item_id (or equivalent),
  profile/user FK, started_at, current_position_seconds (or equivalent),
  status. Mark any required column as MISSING and surface in sub-plan 3.
  ```

### Step 1.7: D6 — `people` / `item_people` role conventions

- [ ] **Run:**
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "\d public.item_people"
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "SELECT DISTINCT role FROM item_people ORDER BY role;"
  ```

- [ ] **Append to the findings doc** under the D6 heading:

  ```text
  item_people.role storage: <TEXT | enum named '<name>' | CHECK constraint>
  Existing role values in use: <comma-separated list from the SELECT>

  Sub-plan 2 will UPSERT 'author' and 'narrator' into item_people for
  audiobook items. If the role column is constrained by an enum or CHECK,
  Sub-plan 2 must add 'author' and 'narrator' to the constraint as part of
  its first migration.
  ```

### Step 1.8: D7 — FTS handling of new `type` values

- [ ] **Run:**
  ```bash
  grep -lE "type\s*=\s*'(movie|series|episode)'" migrations/*.up.sql | head -5
  ```

  These migrations build/maintain silo's title search. Open each one and
  scan for whether the type filter is hardcoded (`type IN ('movie','series')`)
  or generic (no type filter, indexes everything).

- [ ] **Append to the findings doc** under the D7 heading:

  ```text
  Indexes / generated columns that filter by media_items.type:
  <bulleted list of (migration_file, what it filters)>

  Verdict: audiobooks WILL / WILL NOT be FTS-searchable out of the box.

  If WILL NOT: Sub-plan 3 must include an extra migration that extends the
  type filter to include 'audiobook' and 'podcast'.
  ```

### Step 1.9: D8 — Scheduled-task registration

- [ ] **Run:**
  ```bash
  grep -rEn "RegisterTask|TaskManager|RegisterScheduled|task\.Register" cmd/silo/main.go internal --include='*.go' | head -10
  ```

- [ ] **Append to the findings doc** under the D8 heading:

  ```text
  First-party scheduled tasks register at: <file:line>
  Registration call shape: <one-line copy of the call as used by an
  existing first-party task such as smart-count refresh>

  Sub-plan 5 (podcasts) will register podcastfeed.Refresher at the same
  call site using the same pattern.
  ```

### Step 1.10: Commit

- [ ] **Run:**
  ```bash
  git add docs/superpowers/plans/artifacts/2026-05-24-audiobooks-discovery-findings.md
  git commit -m "$(cat <<'EOF'
docs(audiobooks): discovery findings for absorption sub-plan 1

Locks schema/code decisions for migrations 139-142 and downstream
sub-plans. Resolves open Risk questions from the absorption design spec.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
  ```

---

## Task 2: Migration 147 — `abs_sessions`

Parallel of `jellycompat_sessions` for Audiobookshelf clients. Lets ABS
mobile/desktop apps reconnect without re-authenticating against silo's
main `auth_sessions`.

**Files:**
- Create: `migrations/147_abs_sessions.up.sql`
- Create: `migrations/147_abs_sessions.down.sql`

> **Note:** If Task 1, Step 1.2 found the next number is not 139, rename
> both filenames to match. All other tasks downstream of 139 shift by the
> same delta.

### Step 2.1: Write the failing assertion

- [ ] **Run** (this should error — the table does not exist yet):

  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "\d public.abs_sessions"
  ```

  Expected: `Did not find any relation named "public.abs_sessions".`

### Step 2.2: Create the up migration

- [ ] **Create** `migrations/147_abs_sessions.up.sql` with:

  ```sql
  -- Audiobookshelf-compatible client sessions. Parallel to
  -- jellycompat_sessions: each row identifies an ABS mobile/desktop
  -- client by its device + token so it can reconnect without
  -- re-authenticating against silo's main auth_sessions.

  CREATE TABLE IF NOT EXISTS public.abs_sessions (
      id           BIGSERIAL PRIMARY KEY,
      user_id      INTEGER NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
      token_hash   TEXT NOT NULL,
      device_id    TEXT NOT NULL,
      device_name  TEXT,
      client_name  TEXT,
      client_version TEXT,
      created_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
      last_seen_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
      revoked_at   TIMESTAMP WITH TIME ZONE
  );

  CREATE UNIQUE INDEX IF NOT EXISTS idx_abs_sessions_token_hash
      ON public.abs_sessions (token_hash);

  CREATE INDEX IF NOT EXISTS idx_abs_sessions_user_device
      ON public.abs_sessions (user_id, device_id);

  CREATE INDEX IF NOT EXISTS idx_abs_sessions_last_seen
      ON public.abs_sessions (last_seen_at);
  ```

### Step 2.3: Create the down migration

- [ ] **Create** `migrations/147_abs_sessions.down.sql` with:

  ```sql
  DROP INDEX IF EXISTS public.idx_abs_sessions_last_seen;
  DROP INDEX IF EXISTS public.idx_abs_sessions_user_device;
  DROP INDEX IF EXISTS public.idx_abs_sessions_token_hash;
  DROP TABLE IF EXISTS public.abs_sessions;
  ```

### Step 2.4: Apply the migration by restarting silo

- [ ] **Run:** `sudo docker compose -p silo-prod restart silo`

- [ ] **Wait until healthy:**
  ```bash
  until [ "$(sudo docker inspect -f '{{.State.Health.Status}}' silo-prod-silo-1 2>/dev/null)" = "healthy" ]; do sleep 2; done; echo healthy
  ```

- [ ] **Verify silo applied the migration:**
  ```bash
  sudo docker logs --since 2m silo-prod-silo-1 2>&1 | grep -i 'database migrations applied'
  ```
  Expected: one line containing `database migrations applied`.

### Step 2.5: Verify the table exists and is usable

- [ ] **Run** (each command should now succeed):
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "\d public.abs_sessions"
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "SELECT version FROM schema_versions WHERE version = 139;"
  ```
  Expected: `\d` prints the full column list; the `SELECT` returns one row with `version=139`.

- [ ] **Run** a sample insert/delete to prove constraints work:
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "
  INSERT INTO abs_sessions (user_id, token, device_id, device_name, client_name, client_version)
  VALUES (1, 'test-token-001', 'test-device-001', 'Test Device', 'AbsTestClient', '1.0.0');
  DELETE FROM abs_sessions WHERE token = 'test-token-001';
  "
  ```
  Expected: `INSERT 0 1` then `DELETE 1`. (User id 1 must exist — silo's first user.)

### Step 2.6: Commit

- [ ] **Run:**
  ```bash
  git add migrations/147_abs_sessions.up.sql migrations/147_abs_sessions.down.sql
  git commit -m "$(cat <<'EOF'
feat(audiobooks): migration 139 add abs_sessions table

Parallel of jellycompat_sessions for Audiobookshelf-compatible clients.
Lets ABS mobile/desktop apps maintain a device-bound session that
silo's audiobooks/abs handlers will validate.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
  ```

---

## Task 3: Migration 140 — `podcast_feeds`

One row per subscribed podcast. The podcast itself lives in `media_items`
(`type='podcast'`); this side table carries the RSS-specific metadata the
feed refresher needs.

**Files:**
- Create: `migrations/140_podcast_feeds.up.sql`
- Create: `migrations/140_podcast_feeds.down.sql`

### Step 3.1: Write the failing assertion

- [ ] **Run:**
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "\d public.podcast_feeds"
  ```
  Expected: `Did not find any relation named "public.podcast_feeds".`

### Step 3.2: Create the up migration

- [ ] **Create** `migrations/140_podcast_feeds.up.sql` with:

  ```sql
  -- RSS-feed metadata for subscribed podcasts. One row per podcast
  -- media_items row; the feed refresher polls feed_url every
  -- refresh_interval_seconds and upserts new episodes into the existing
  -- episodes table.

  CREATE TABLE IF NOT EXISTS public.podcast_feeds (
      media_item_id              TEXT PRIMARY KEY
                                 REFERENCES public.media_items(content_id) ON DELETE CASCADE,
      feed_url                   TEXT NOT NULL,
      etag                       TEXT,
      last_modified              TEXT,
      last_refreshed_at          TIMESTAMP WITH TIME ZONE,
      last_refresh_error         TEXT,
      refresh_interval_seconds   INTEGER NOT NULL DEFAULT 600,
      created_at                 TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
      updated_at                 TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
  );

  CREATE UNIQUE INDEX IF NOT EXISTS idx_podcast_feeds_feed_url
      ON public.podcast_feeds (feed_url);

  CREATE INDEX IF NOT EXISTS idx_podcast_feeds_due_for_refresh
      ON public.podcast_feeds (last_refreshed_at);
  ```

  > **Note:** `media_items.content_id` is TEXT (silo uses snowflake-style
  > string IDs, not BIGSERIAL — confirmed by the FK targets in other
  > migrations). If Task 1 Step 1.4 sampled a numeric `content_id`,
  > change the column type to match. Otherwise leave as TEXT.

### Step 3.3: Create the down migration

- [ ] **Create** `migrations/140_podcast_feeds.down.sql` with:

  ```sql
  DROP INDEX IF EXISTS public.idx_podcast_feeds_due_for_refresh;
  DROP INDEX IF EXISTS public.idx_podcast_feeds_feed_url;
  DROP TABLE IF EXISTS public.podcast_feeds;
  ```

### Step 3.4: Apply the migration

- [ ] **Run:** `sudo docker compose -p silo-prod restart silo`

- [ ] **Wait until healthy:**
  ```bash
  until [ "$(sudo docker inspect -f '{{.State.Health.Status}}' silo-prod-silo-1 2>/dev/null)" = "healthy" ]; do sleep 2; done; echo healthy
  ```

### Step 3.5: Verify

- [ ] **Run:**
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "\d public.podcast_feeds"
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "SELECT version FROM schema_versions WHERE version = 140;"
  ```
  Expected: column list printed; one row with `version=140`.

### Step 3.6: Commit

- [ ] **Run:**
  ```bash
  git add migrations/140_podcast_feeds.up.sql migrations/140_podcast_feeds.down.sql
  git commit -m "$(cat <<'EOF'
feat(audiobooks): migration 140 add podcast_feeds table

Side table on media_items for RSS-subscribed podcasts. Holds feed URL,
ETag/Last-Modified for conditional fetches, last-refresh timestamp, and
the per-feed refresh interval consumed by the upcoming
podcastfeed.Refresher scheduled task.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
  ```

---

## Task 4: Migration 159 — `media_folders.type` audiobook value (no-op)

Adds a discriminator column so the scanner can branch on
`audiobooks`/`podcasts` libraries without filename heuristics.

> **CONDITIONAL:** If Task 1 Step 1.3 (D2) recorded *"Existing column
> '<NAME>' discriminates library content"*, swap this whole task for a
> documented no-op: create both migration files as no-op SQL (`-- intentionally empty: existing column '<NAME>' covers this need`)
> and adjust downstream sub-plans to read `'<NAME>'` instead of `kind`.
> Otherwise, proceed with the steps below.

**Files:**
- Create: `migrations/159_media_folders_kind_noop.up.sql`
- Create: `migrations/159_media_folders_kind_noop.down.sql`

### Step 4.1: Write the failing assertion

- [ ] **Run:**
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "SELECT kind FROM media_libraries LIMIT 1;"
  ```
  Expected: `column "kind" does not exist`.

### Step 4.2: Create the up migration

- [ ] **Create** `migrations/159_media_folders_kind_noop.up.sql` with:

  ```sql
  -- Per-library content discriminator. Lets the scanner choose the right
  -- parser (movie/tv extensions vs audiobook/podcast extensions) without
  -- filename heuristics. Existing rows default to 'movies' to preserve
  -- current behavior; operators set 'audiobooks' or 'podcasts' on the
  -- libraries they want the new flavor for.

  ALTER TABLE public.media_libraries
      ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'movies';

  ALTER TABLE public.media_libraries
      ADD CONSTRAINT media_libraries_kind_check
      CHECK (kind IN ('movies', 'tv', 'audiobooks', 'podcasts'));

  CREATE INDEX IF NOT EXISTS idx_media_libraries_kind
      ON public.media_libraries (kind);
  ```

### Step 4.3: Create the down migration

- [ ] **Create** `migrations/159_media_folders_kind_noop.down.sql` with:

  ```sql
  DROP INDEX IF EXISTS public.idx_media_libraries_kind;
  ALTER TABLE public.media_libraries
      DROP CONSTRAINT IF EXISTS media_libraries_kind_check;
  ALTER TABLE public.media_libraries
      DROP COLUMN IF EXISTS kind;
  ```

### Step 4.4: Apply the migration

- [ ] **Run:** `sudo docker compose -p silo-prod restart silo`
- [ ] **Wait until healthy** (same command as Task 2 Step 2.4).

### Step 4.5: Verify

- [ ] **Run:**
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "
  SELECT id, name, kind FROM media_libraries ORDER BY id;
  "
  ```
  Expected: every existing library row has `kind='movies'`.

- [ ] **Run** to confirm the CHECK rejects bad values:
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "
  INSERT INTO media_libraries (id, name, kind) VALUES (-9999, 'bad-test', 'garbage');
  "
  ```
  Expected: `ERROR: new row for relation "media_libraries" violates check constraint "media_libraries_kind_check"`.

- [ ] **Run** to clean up any test row that snuck through (no-op if the previous step rejected as expected):
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "DELETE FROM media_libraries WHERE id = -9999;"
  ```

### Step 4.6: Commit

- [ ] **Run:**
  ```bash
  git add migrations/159_media_folders_kind_noop.up.sql migrations/159_media_folders_kind_noop.down.sql
  git commit -m "$(cat <<'EOF'
feat(audiobooks): migration 159 document audiobooks media folder type

Per-library discriminator that lets the scanner pick the right parser
('movies', 'tv', 'audiobooks', 'podcasts') instead of inferring from
filename. Existing libraries default to 'movies'; operators set the new
values on the libraries they want indexed with audiobook/podcast logic.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
  ```

---

## Task 5: Migration 160 — `audiobooks.enabled` feature flag

Adds a `server_settings` row so subsequent sub-plans can branch on
"audiobooks compiled in but turned off" vs "fully live". Default is
`false` so this sub-plan's landing is a strict no-op for users.

**Files:**
- Create: `migrations/160_audiobooks_feature_flag.up.sql`
- Create: `migrations/160_audiobooks_feature_flag.down.sql`

### Step 5.1: Write the failing assertion

- [ ] **Run:**
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "
  SELECT value FROM server_settings WHERE key = 'audiobooks.enabled';
  "
  ```
  Expected: zero rows.

### Step 5.2: Create the up migration

- [ ] **Create** `migrations/160_audiobooks_feature_flag.up.sql` with:

  ```sql
  -- Master kill-switch for the absorbed audiobooks feature. Defaults to
  -- 'false' so landing migrations 147/157/159/160 is a no-op for users; operators
  -- flip this to 'true' at cutover.

  INSERT INTO server_settings (key, value) VALUES ('audiobooks.enabled', 'false')
  ON CONFLICT (key) DO NOTHING;
  ```

### Step 5.3: Create the down migration

- [ ] **Create** `migrations/160_audiobooks_feature_flag.down.sql` with:

  ```sql
  DELETE FROM server_settings WHERE key = 'audiobooks.enabled';
  ```

### Step 5.4: Apply and verify

- [ ] **Run:** `sudo docker compose -p silo-prod restart silo`
- [ ] **Wait until healthy** (same command).

- [ ] **Run:**
  ```bash
  sudo docker exec silo-prod-postgres-1 psql -U silo -d silo -c "
  SELECT key, value FROM server_settings WHERE key = 'audiobooks.enabled';
  "
  ```
  Expected: one row, `value='false'`.

### Step 5.5: Commit

- [ ] **Run:**
  ```bash
  git add migrations/160_audiobooks_feature_flag.up.sql migrations/160_audiobooks_feature_flag.down.sql
  git commit -m "$(cat <<'EOF'
feat(audiobooks): migration 160 add audiobooks.enabled flag

Server-settings row that gates the absorbed audiobooks feature.
Defaults to 'false' so sub-plan 1 lands as a strict no-op; subsequent
sub-plans branch on this flag and operators flip it to 'true' at
cutover.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
  ```

---

## Task 6: Scaffold `internal/audiobooks/` package

Creates an empty but compiling Go package, wires its constructor into
`cmd/silo/main.go` so the package is referenced from the binary, and
ships a unit test that proves the `Enabled()` reader returns the flag
value seeded by migration 160.

This task proves the package compiles inside the rest of silo without
changing any runtime behavior. No routes are mounted. No scheduled
tasks are registered. No DB writes happen.

**Files:**
- Create: `internal/audiobooks/doc.go`
- Create: `internal/audiobooks/service.go`
- Create: `internal/audiobooks/service_test.go`
- Modify: `cmd/silo/main.go` (one new constructor call near the other service constructions)

### Step 6.1: Write the failing test

- [ ] **Create** `internal/audiobooks/service_test.go` with:

  ```go
  package audiobooks

  import (
  	"context"
  	"errors"
  	"testing"
  )

  type fakeSettingsReader struct {
  	value string
  	err   error
  }

  func (f *fakeSettingsReader) GetString(_ context.Context, key string) (string, error) {
  	if key != "audiobooks.enabled" {
  		return "", errors.New("unexpected key: " + key)
  	}
  	return f.value, f.err
  }

  func TestServiceEnabledReadsFlag(t *testing.T) {
  	cases := []struct {
  		name    string
  		stored  string
  		want    bool
  	}{
  		{"flag true", "true", true},
  		{"flag false", "false", false},
  		{"flag empty defaults false", "", false},
  		{"flag garbage defaults false", "yes-please", false},
  	}
  	for _, tc := range cases {
  		tc := tc
  		t.Run(tc.name, func(t *testing.T) {
  			svc := New(&fakeSettingsReader{value: tc.stored})
  			got, err := svc.Enabled(context.Background())
  			if err != nil {
  				t.Fatalf("Enabled returned error: %v", err)
  			}
  			if got != tc.want {
  				t.Fatalf("Enabled = %v, want %v", got, tc.want)
  			}
  		})
  	}
  }

  func TestServiceEnabledPropagatesError(t *testing.T) {
  	wantErr := errors.New("db down")
  	svc := New(&fakeSettingsReader{err: wantErr})
  	_, err := svc.Enabled(context.Background())
  	if !errors.Is(err, wantErr) {
  		t.Fatalf("Enabled error = %v, want %v wrapped", err, wantErr)
  	}
  }
  ```

### Step 6.2: Run the test to verify it fails

- [ ] **Run:** `go test ./internal/audiobooks/...`

  Expected: build failure with messages like `undefined: New` and
  `package audiobooks not found`.

### Step 6.3: Write the package doc

- [ ] **Create** `internal/audiobooks/doc.go` with:

  ```go
  // Package audiobooks owns silo's first-party audiobook + podcast feature,
  // absorbed from the historical silo-plugin-audiobooks. Sub-plan 1 lands
  // only the package scaffold and the kill-switch reader; ABS-compat REST,
  // Socket.io, scanner branches, podcast feed refresh, and the silo SPA
  // pages arrive in later sub-plans.
  //
  // See docs/superpowers/specs/2026-05-24-audiobooks-absorption-design.md
  // for the design and docs/superpowers/plans/2026-05-24-audiobooks-*.md
  // for the staged implementation plans.
  package audiobooks
  ```

### Step 6.4: Write the service

- [ ] **Create** `internal/audiobooks/service.go` with:

  ```go
  package audiobooks

  import (
  	"context"
  	"fmt"
  )

  // SettingsReader is the minimal slice of the server-settings store that
  // the audiobooks service needs. The production implementation is
  // internal/serversettings.Store (or whatever silo names that helper at
  // wiring time); tests pass a fake.
  type SettingsReader interface {
  	GetString(ctx context.Context, key string) (string, error)
  }

  // Service is the audiobooks feature's top-level orchestrator. Sub-plan 1
  // exposes only Enabled(); subsequent sub-plans hang additional methods
  // off Service as new capabilities (scanner branches, ABS handlers, etc.)
  // come online.
  type Service struct {
  	settings SettingsReader
  }

  // New constructs a Service. The constructor takes the dependencies it
  // will actually use; current sub-plan needs only the settings reader.
  func New(settings SettingsReader) *Service {
  	return &Service{settings: settings}
  }

  // Enabled reports whether the audiobooks feature flag (set by
  // 160_audiobooks_feature_flag and toggled by operators) is currently true.
  // Any value other than the literal string "true" reads as false; this matches how silo
  // treats other boolean server_settings rows.
  func (s *Service) Enabled(ctx context.Context) (bool, error) {
  	if s == nil || s.settings == nil {
  		return false, nil
  	}
  	value, err := s.settings.GetString(ctx, "audiobooks.enabled")
  	if err != nil {
  		return false, fmt.Errorf("audiobooks: read audiobooks.enabled: %w", err)
  	}
  	return value == "true", nil
  }
  ```

### Step 6.5: Run the test to verify it passes

- [ ] **Run:** `go test ./internal/audiobooks/...`
  Expected: `ok  github.com/Silo-Server/silo-server/internal/audiobooks ...`

### Step 6.6: Wire the constructor into `cmd/silo/main.go`

The audit in Task 1 Step 1.9 located the function where first-party
services are constructed at startup. Open `cmd/silo/main.go`, find the
block where other internal services (e.g. metadata, downloads, catalog)
are constructed and pass a settings store to.

- [ ] **Run** to find the right insertion point:
  ```bash
  grep -nE 'serversettings|server_settings|settingsStore|metadata\.NewService|downloads\.NewService' cmd/silo/main.go | head -10
  ```
  Note the file:line of the existing service construction block.

- [ ] **Modify** `cmd/silo/main.go`: in the service-construction block (immediately after another similar `service := pkg.New(...)` line), add:

  ```go
  audiobooksService := audiobooks.New(settingsStore)
  _ = audiobooksService // referenced by sub-plan 2 onward; no behavior in sub-plan 1
  ```

  Where `settingsStore` is whatever the existing services pass in for
  the server-settings reader. The `_ = audiobooksService` line keeps Go
  from rejecting an unused variable; it gets removed in sub-plan 2 when
  the variable is actually used.

- [ ] **Add** the import to the import block at the top of `cmd/silo/main.go`:

  ```go
  "github.com/Silo-Server/silo-server/internal/audiobooks"
  ```

### Step 6.7: Verify everything still compiles and tests pass

- [ ] **Run:** `go build ./...`
  Expected: exit 0, no output.

- [ ] **Run:** `go test ./internal/audiobooks/... ./cmd/...`
  Expected: all passes.

- [ ] **Run:** `make lint`
  Expected: no new lint findings introduced by this task.

### Step 6.8: Smoke-test the running binary

- [ ] **Run:** `sudo docker compose -p silo-prod up -d --force-recreate silo`
- [ ] **Wait until healthy** (same command as Task 2 Step 2.4).
- [ ] **Run:**
  ```bash
  curl -sS -o /dev/null -w "HTTP %{http_code}\n" http://localhost:8090/api/v1/health
  curl -sS -o /dev/null -w "HTTP %{http_code}\n" http://localhost:8090/
  ```
  Expected: same status codes silo returned before this sub-plan
  started (typically `200` for `/`, `404` for `/api/v1/health` since
  that path was 404 in the baseline logs). No new errors in `docker
  logs silo-prod-silo-1`.

### Step 6.9: Commit

- [ ] **Run:**
  ```bash
  git add internal/audiobooks/doc.go internal/audiobooks/service.go internal/audiobooks/service_test.go cmd/silo/main.go
  git commit -m "$(cat <<'EOF'
feat(audiobooks): scaffold internal/audiobooks package

Empty-but-compiling Service that reads the audiobooks.enabled feature
flag from server_settings. Wired into cmd/silo so the package is
referenced from the binary; no routes mounted, no scheduled tasks
registered, no DB writes. Subsequent sub-plans hang scanner branches,
ABS handlers, Socket.io, podcast refresher, and SPA pages off this
Service.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
  ```

---

## Self-review (done by the planner before handoff)

**Spec coverage:**

| Spec section | Task(s) that cover it |
|---|---|
| Data model: reused tables | Task 1 (verification only) |
| Data model: `abs_sessions` (migration 147) | Task 2 |
| Data model: `podcast_feeds` (migration 157) | Task 3 |
| Data model: `media_folders.type` audiobook value (migration 159 no-op) | Task 4 |
| Data model: data migration = none | n/a — confirmed in spec |
| Feature flag (`audiobooks.enabled`) | Task 5 |
| Architecture: `internal/audiobooks/` package | Task 6 |
| Risks: `media_libraries` column check | Task 1, Step 1.3 |
| Risks: `media_files.chapters` shape | Task 1, Step 1.4 |
| Risks: progress profile-scoping | Task 1, Step 1.5 |
| Risks: FTS handling of `audiobook` | Task 1, Step 1.8 |
| Risks: `people` role conventions | Task 1, Step 1.7 |

Out of sub-plan 1 scope (deferred to later sub-plans):
ABS REST port, Socket.io port, scanner branches, podcast refresher,
silo SPA pages, plugin retirement, cutover. Each is its own sub-plan
(2–6).

**Placeholder scan:** None. Every step has the actual command, the
actual SQL, or the actual Go to write. Conditional language is confined
to Task 4 (which is explicit about its condition and what to do if it
fires).

**Type consistency:** The fake `SettingsReader` interface in Task 6's
test matches the production interface defined in `service.go`. Method
name `GetString`, signature `(ctx context.Context, key string) (string, error)` — consistent across both files.

**Note on migration numbering:** Tasks 2–5 originally used numbers `139..142`
based on a snapshot showing `138_search_number_word_normalization.up.sql` as
the highest existing migration. The landed implementation was renumbered to
`147_abs_sessions`, `157_podcast_feeds`, `159_media_folders_kind_noop`, and
`160_audiobooks_feature_flag`.
