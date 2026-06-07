# ABS Implementation Fix — Design

**Status:** Approved (brainstorming complete, awaiting writing-plans handoff)
**Date:** 2026-05-26
**Scope:** Bring silo-server's Audiobookshelf-compatible API to full parity with the canonical `continuum-plugin-audiobooks` implementation so that official ABS iOS, Android, and 3rd-party (Plappa, AudioBookShelfFully) clients work end-to-end against silo.
**Out of scope:** silo's native audiobook surface (`/api/v1/audiobooks/*`), silo-android/silo-apple/silo-plugin-sdk repos, Continuum's plugin-host RPC layer, the "standalone listener" port (silo has no separate process), cover transcoding pipelines, transcribed-audio search.

Commands assume the repository root is the cwd.

---

## 1. Problem

silo's ABS-compat layer at `internal/audiobooks/abs/` exposes ~20% of the surface that the canonical Continuum plugin implements (`continuum-plugin-audiobooks` reference). Real ABS mobile clients cannot complete the basic flow today — login is reported broken, and even when it succeeds many subsequent calls (filterdata, resume position, author/series IDs) return data shapes that break the client. Bookmarks, collections, playlists, smart collections, RSS feeds, author/series detail, and listening stats are entirely absent. Socket.io publishes only 3 of the ~30 events real clients subscribe to.

The user's directive: every feature of ABS clients must function. Source of truth for behavior is the Continuum plugin; the booklore-ng reference docs (`BOOKLORE_ABS_IMPLEMENTATION_ISSUES.md`, `src/lib/socket/events.ts`) document specific response-shape bugs and the canonical socket event list.

## 2. Goals & Non-goals

### Goals
- Official ABS iOS app, ABS Android app, and 3rd-party clients (Plappa, AudioBookShelfFully) work end-to-end: add server → login → browse libraries → play → progress sync → bookmark → collection → playlist.
- Token lifecycle is complete: login, refresh, logout, revocation, multi-device tracking.
- Socket.io publishes the full event surface real clients subscribe to.
- New data lives in ABS-scoped tables that don't entangle silo's existing collection system.

### Non-goals
- silo's native audiobook UI changes.
- silo-android / silo-apple client changes.
- Importing real audiobook content via ABS-protocol requests (silo has its own `internal/requests/` system that is not being bridged here).
- Watch-together, podcast download queues, server-side backup event streams.

## 3. Architecture & Strategy

**Source of truth:** `continuum-plugin-audiobooks` (canonical). Port file-by-file, adapt to silo's data model (catalog repos, `auth.Service`, profile model).

**Topology:** silo is monolithic; the ABS layer reads/writes the local DB directly. The Continuum plugin's `HostClient → backend plugin RPC` layer has no analog and is dropped.

**Package layout:** keep silo's existing `internal/audiobooks/{abs,abssocket,podcastfeed}` — it already mirrors the Continuum plugin's structure.

**Listener:** continue using the dedicated `:13378` ABS-compat HTTP server (configured in `internal/config/db_loader.go` as `audiobookshelf_compat.listen`). Routes mounted on a fresh chi router; no SPA fallback collisions.

**Data isolation:** new ABS surface uses dedicated tables (`abs_bookmarks`, `abs_user_collections`, `abs_collection_items`, `abs_playlists`, `abs_playlist_items`, `abs_smart_collections`, `abs_rss_feeds`). silo's existing `library_collections` / `user_personal_collections` / `user_smart_collections` / collection groups are untouched. Future bridging is possible but out of scope here.

## 4. Phasing

Four phases, each independently mergeable and independently verifiable against a real mobile client.

### Phase 0 — Login + critical bug fixes

**Goal:** iOS/Android app can `add server → login → browse library → tap book → play` end-to-end. No bookmarks/collections/etc. yet.

**Concrete changes:**

| File:line | Change |
|---|---|
| `internal/audiobooks/abs/login.go:195-234` | Add to login envelope: `user.itemTagsAccessible: []`, `user.itemTagsSelected: []`, `user.lastSeen: <epoch ms>`, `user.createdAt: <epoch ms>`. Enrich `serverSettings` with `coverAspectRatio`, `dateFormat`, `timeFormat`, `storeCoverWithItem`, `scannerDisableWatcher`, `metadataFileFormat`, `chromecastEnabled`. Mirror Continuum's `completeLogin` (`continuum-plugin-audiobooks/internal/abs/handler.go:591-697`) verbatim. |
| `internal/audiobooks/abs/login.go:274-315` (`handleABSAuthorize`) | Return the identical envelope as `/login`, including `accessToken` and `refreshToken`. Currently omits them, which breaks resume-on-launch. |
| `internal/audiobooks/abs/handler.go:353-391` (bearerAuth) | Audit JTI lookup against `abs_session_store.go:GetTokenByJTI` to confirm the freshly-minted JTI is found. Add `slog.Debug` lines at each rejection branch so the next 401 is traceable. Confirm `?token=` query-param fallback parses correctly (iOS AVPlayer requirement). |
| `internal/audiobooks/abs/libraries_handler.go:499-549` (`siloItemToMetadata`) | Include `id` on every `authors[]` entry (use `item_people.id` UUID). Include `id` on every `series[]` entry (slugified name until a series table lands). Populate `genres` and `tags` from real catalog data instead of empty arrays. Cross-reference `booklore-ng/BOOKLORE_ABS_IMPLEMENTATION_ISSUES.md` lines 9-100. |
| `internal/audiobooks/abs/play_response.go:119` | Replace hardcoded `currentTime: 0` with `ProgressStore.GetItemProgress(userID, profileID, libraryItemID)` lookup; emit the persisted `currentTime` and `progress` fields. |
| `internal/audiobooks/abs/libraries_handler.go:56-66` | When `?include=filterdata`, hydrate `authors`, `series`, `narrators`, `genres`, `languages`, `tags` aggregations so iOS filter UI populates. |
| `internal/audiobooks/abs/login.go` (new handler) | Add `POST /auth/refresh`. Validate refresh token type, check JTI not revoked, mint new access+refresh pair, persist new JTIs. Port from `continuum-plugin-audiobooks/internal/abs/handler.go:handleRefresh`. |
| `internal/audiobooks/abs/login.go` (new handler) | Add `POST /logout`. Marks caller's JTIs revoked in `abs_sessions`. Port from Continuum's `handleLogout`. |
| `internal/audiobooks/abs/handler.go:225` (`mountRoutes`) | Mount the new `/auth/refresh` and `/logout` routes (the latter inside `bearerAuth`). |

**Deliverable:** silo build where official ABS iOS app smoke test passes the core flow.

**Size:** ~600-800 lines across ~6 files + 2 new endpoints.

### Phase 1 — Feature surface completion

**Goal:** Bookmarks, manual collections, playlists, smart collections, RSS feeds, author/series detail, and listening stats functional in mobile clients.

**Endpoints to add** (all under `bearerAuth` unless noted; mounted at both `/abs/api/*` and `/api/*`):

**Bookmarks** (port `continuum-plugin-audiobooks/internal/abs/bookmarks_handler.go`)
- `POST /api/me/item/{itemId}/bookmark` — body `{title, time}` → array of all bookmarks for the item. Fires socket `user_updated` (`reason: "bookmark_created"`).
- `PATCH /api/me/item/{itemId}/bookmark` — upsert at time position. Fires `user_updated` (`reason: "bookmark_updated"`).
- `DELETE /api/me/item/{itemId}/bookmark/{time}` — fires `user_updated` (`reason: "bookmark_deleted"`).

**Manual Collections** (port `collections_handler.go`)
- `GET /api/collections` — list with embedded `libraryItems[]`.
- `POST /api/collections` — `{name, description}` → single collection.
- `GET/PATCH/DELETE /api/collections/{id}`.
- `POST /api/collections/{id}/book/{bookId}` — add item.
- `DELETE /api/collections/{id}/book/{bookId}` — remove item.

**Playlists** (port `playlists_handler.go`)
- `GET /api/playlists` → `{playlists: [...]}`.
- `POST /api/playlists` — `{name, description, cover_item, is_public}`.
- `GET/PATCH/DELETE /api/playlists/{id}`.
- `POST /api/playlists/{id}/item` — `{libraryItemId, episodeId?}`.
- `POST /api/playlists/{id}/batch/add` — `{items: [...]}`.
- `POST /api/playlists/{id}/batch/remove`.
- `DELETE /api/playlists/{id}/item/{libraryItemId}[/{episodeId}]`.

**Smart Collections** (port `smart_collection_handler.go` + the entire `internal/smartcoll/` DSL package; adapt SQL to silo's `media_items` schema)
- `GET /api/me/smart-collections` → `{items: [...]}`.
- `POST /api/me/smart-collections` — `{name, description, color, is_public, is_pinned, query_def}`.
- `GET /api/me/smart-collections/{id}`.
- `GET /api/me/smart-collections/{id}/items` — evaluates `query_def` against the catalog.
- `PATCH/DELETE /api/me/smart-collections/{id}`.

**Author detail**
- `GET /api/authors/{id}` — single author with `books[]`.
- `GET /api/authors/{id}/image` — wire to `item_people.poster_path` via `DetailService.PresignURL` (currently 404s).

**Series detail**
- `GET /api/series/{id}` — single series with `books[]` sorted by `series_sequence`.

**RSS Feeds** (port `rss_feed_handler.go` + `internal/podcastfeed/`)
- `POST /api/feeds` — `{itemId, slug, minified}` → `{success, feed: {id, slug, url, ...}}`.
- `GET /api/feeds` — caller's feeds.
- `DELETE /api/feeds/{id}`.
- `GET /abs/public/feed/{slug}` — public RSS XML (no auth, slug is capability token).
- `GET /abs/public/feed/{slug}/cover`.
- `GET /abs/public/feed/{slug}/item/{itemId}/{fileIdx}`.

**Listening stats** (port `internal/abs/handler.go:handleListeningStats` + `handleListeningSessions`)
- `GET /api/me/listening-stats` → `{totalTime, items, days, dayOfWeek, monthly}` from `abs_playback_sessions` aggregation.
- `GET /api/me/listening-sessions?limit=...` — paginated session history.
- `GET /api/me/listening-sessions/{sid}` — single session detail.

**Continue-listening toggles** (port `continue_listening.go`)
- `GET /api/me/progress/{itemId}/remove-from-continue-listening`.
- `GET /api/me/progress/{itemId}/readd-to-continue-listening`.
- Adds `hide_from_continue boolean` column to `user_watch_progress` (the table the ABS layer writes via `abs_progress_store.go`).

**Migrations** (numbered after silo's current max, paired up/down per `CLAUDE.md`):
- `148_abs_bookmarks` — `(id ULID PK, user_id, profile_id, library_item_id, time_seconds, title, created_at, updated_at)` + unique `(user_id, profile_id, library_item_id, time_seconds)`.
- `149_abs_user_collections` + `150_abs_collection_items`.
- `151_abs_playlists` + `152_abs_playlist_items`.
- `153_abs_smart_collections` — includes `query_def jsonb`.
- `154_abs_rss_feeds` — slug is unique capability token.
- `155_abs_progress_hide_from_continue` — adds `hide_from_continue boolean default false`.

**Deliverable:** ABS iOS/Android Library, Collections, Playlists, Bookmarks, RSS, Stats tabs all functional.

**Size:** ~3000-4000 lines across ~15 new files + 7 migration pairs. Splittable into 4 sub-commits (bookmarks, collections+playlists, smart collections, RSS+stats).

### Phase 2 — Socket.io full event parity

**Goal:** A logged-in mobile client sees real-time updates when another device on the same account changes progress, when a server scan adds items, when collections/playlists are mutated remotely, and when an admin posts notifications.

**Event surface** (union of Continuum + booklore-ng — what real ABS iOS source subscribes to):

**Server → Client** (~30 events):
- Lifecycle: `init`, `auth_failed`, `user_online`, `user_offline`, `listener_count`.
- Progress/session: `user_item_progress_updated`, `user_session_open`, `user_session_updated`, `user_session_closed`, `user_stream_update`, `user_stream_end`.
- User-scoped: `user_updated` (bookmark reasons).
- Library: `library_added`, `library_updated`, `library_removed`.
- Items: `item_added`, `item_updated`, `item_removed`, `items_added`.
- People/series: `author_added`, `author_updated`, `author_removed`, `series_added`, `series_updated`, `series_removed`.
- Collections/playlists: `collection_added/updated/removed`, `playlist_added/updated/removed`.
- RSS: `rss_feed_open`, `rss_feed_closed`.
- Tasks: `scan_start`, `scan_progress`, `scan_complete`, `task_started`, `task_progress`, `task_finished`.
- Misc: `notification`.

**Client → Server** (10 events):
- `auth` (exists), `ping`, `join_library`, `leave_library`, `playback_start`, `playback_sync`, `playback_end`, `stream_open`, `stream_close`, `sync_progress`.

**Architecture changes to `internal/audiobooks/abssocket/`:**
- Add room types: existing `user:<userID>`, new `lib:<libraryID>` (joined via `join_library`), new `admin:*`.
- New file `abssocket/publisher.go` exposing `Publisher` interface (`PublishUser`, `PublishLibrary`, `Broadcast`). Replaces ad-hoc `h.publish/h.broadcast` helpers on the Handler struct (currently silent no-ops when `SocketIO == nil`). Tests pass a recording Publisher.

**Cross-package event-source hooks** (via a new `audiobooks.EventBroadcaster` interface in `api.Dependencies` so the consumer packages don't import `abssocket` directly):
- `internal/scanner/` — emit `scan_*`, `item_*`, `items_added` on audiobook-library scan operations.
- `internal/taskmanager/` — emit `task_*` for audiobook-scoped tasks.
- `internal/api/handlers/libraries.go` (or wherever library CRUD lives) — emit `library_*`.

**Auth/lifecycle improvements:**
- Richer `init` payload: `{userId, connectedAt, libraries[], itemTagsAccessible[]}`.
- Heartbeat: respond to `ping` with `pong`.
- On disconnect, decrement listener count and broadcast `user_offline` if this was the user's last device.
- Reconnect replay (last 30s of user-scoped events) is out of scope for v1; leave a hook.

**Migrations:** none — sockets are stateless except for in-memory connection registry.

**Size:** ~1500-2000 lines: ~600 in `abssocket/`, ~400 publisher wiring across audiobook handlers, ~500 cross-package hooks.

### Phase 3 — Hardening

**Media token signing layer** (port `continuum-plugin-audiobooks/internal/mediatoken/`):
- Separate HS256 secret (`audiobooks.abs.media_signing_secret`), 15-min TTL, claims bound to `(user_id, profile_id, book_id, file_idx)`.
- `file_handler.go` mints when building stream URLs; validates on file request.
- Defense-in-depth: a leaked stream URL grants access to ONE file for 15 minutes instead of full account.
- Secret stored in `server_settings`, auto-generated on first read.

**Device tracking** (currently `abs_sessions.device_id = JTI`, useless for UI):
- Parse `User-Agent` on `/login` → derive `device_name`, `client_name`, `client_version`, store on `abs_sessions` insert.
- `GET /api/me/sessions` — list user's sessions (so they see "iPhone 15 Pro — Sep 1").
- `DELETE /api/me/sessions/{jti}` — selective revocation.

**Audit log:**
- New table `abs_audit_log`: `(id, user_id, action, ip, user_agent, metadata jsonb, created_at)`.
- Logged events: login success/failure, logout, token refresh, session revoke, smart-collection-rule changes.
- Optional `GET /api/admin/audit-log` for ops visibility.

**Rate limiting beyond login:**
- Per-user limit on `POST /me/progress` (1/sec — clients sometimes spam this).
- Per-user limit on `POST /auth/refresh` (5/hour — prevent JTI accumulation).

**Size:** ~800-1200 lines + 2 migration pairs.

## 5. Data Model

### New tables (all in Phase 1 unless noted)

```
abs_bookmarks (
  id              text PRIMARY KEY,         -- ULID
  user_id         integer NOT NULL,
  profile_id      uuid,                     -- nullable: primary profile
  library_item_id text NOT NULL,
  time_seconds    double precision NOT NULL,
  title           text,
  created_at      timestamptz DEFAULT now(),
  updated_at      timestamptz DEFAULT now(),
  UNIQUE (user_id, profile_id, library_item_id, time_seconds)
)

abs_user_collections (
  id          text PRIMARY KEY,            -- ULID
  user_id     integer NOT NULL,
  profile_id  uuid,
  name        text NOT NULL,
  description text,
  is_public   boolean DEFAULT false,
  created_at  timestamptz DEFAULT now(),
  updated_at  timestamptz DEFAULT now()
)

abs_collection_items (
  collection_id text NOT NULL REFERENCES abs_user_collections(id) ON DELETE CASCADE,
  library_item_id text NOT NULL,
  added_at      timestamptz DEFAULT now(),
  PRIMARY KEY (collection_id, library_item_id)
)

abs_playlists (
  id            text PRIMARY KEY,
  user_id       integer NOT NULL,
  profile_id    uuid,
  name          text NOT NULL,
  description   text,
  cover_item    text,                       -- library_item_id for cover
  is_public     boolean DEFAULT false,
  created_at    timestamptz DEFAULT now(),
  updated_at    timestamptz DEFAULT now()
)

abs_playlist_items (
  playlist_id     text NOT NULL REFERENCES abs_playlists(id) ON DELETE CASCADE,
  position        integer NOT NULL,
  library_item_id text NOT NULL,
  episode_id      text,                     -- nullable: book item vs podcast episode
  added_at        timestamptz DEFAULT now(),
  PRIMARY KEY (playlist_id, position)
)

abs_smart_collections (
  id          text PRIMARY KEY,
  user_id     integer NOT NULL,
  profile_id  uuid,
  name        text NOT NULL,
  description text,
  color       text,
  is_public   boolean DEFAULT false,
  is_pinned   boolean DEFAULT false,
  query_def   jsonb NOT NULL,
  created_at  timestamptz DEFAULT now(),
  updated_at  timestamptz DEFAULT now()
)

abs_rss_feeds (
  id           text PRIMARY KEY,
  user_id      integer NOT NULL,
  slug         text UNIQUE NOT NULL,        -- capability token in URL
  item_id      text NOT NULL,
  minified     boolean DEFAULT false,
  created_at   timestamptz DEFAULT now()
)

-- Phase 3
abs_audit_log (
  id          bigserial PRIMARY KEY,
  user_id     integer,
  action      text NOT NULL,
  ip          inet,
  user_agent  text,
  metadata    jsonb,
  created_at  timestamptz DEFAULT now()
)
```

### Modifications to existing tables

```
-- Phase 1: hide-from-continue toggle
ALTER TABLE user_watch_progress ADD COLUMN hide_from_continue boolean DEFAULT false;

-- Phase 3: real device tracking
ALTER TABLE abs_sessions
  ALTER COLUMN device_id DROP DEFAULT,
  ADD COLUMN parsed_user_agent text;
```

## 6. Testing Strategy

**Unit tests** (Go, in-package):
- Table-driven per handler in `internal/audiobooks/abs/`: happy path, missing field, wrong field type, IDOR (other user's session/bookmark/collection).
- `abssocket/` `Publisher` recording mock — verify events fire on the right operations with the right payload shape.
- Smart collection DSL evaluator — port Continuum's `smartcoll_test.go`.

**Integration tests** (against real Postgres + Redis via silo's `internal/audiobooks/testutil` harness):
- Full login → browse → play → progress → close-session flow.
- Token refresh — old token rejected, new token accepted.
- Bookmark CRUD round-trip with socket event capture.
- Smart collection rule evaluation against seeded catalog.

**End-to-end (manual)**:
- iOS official ABS app: add server → login → browse → play offline downloads → progress sync across devices.
- Plappa: same flow. Strictest client for response shape; if Plappa works, everything works.
- Android official: same.

**Migration safety:**
- Each migration includes `.down.sql`.
- New helper `scripts/test-abs-migrations.sh` runs up → down → up against a fresh DB.

## 7. Rollout

Single-shot per phase, no human review gate. **Each phase gets its own implementation plan** (separate `writing-plans` cycle) since a combined plan would be unwieldy at this size. Start with Phase 0; subsequent phases planned only after the prior phase merges and verifies.

| Phase | PR | Verify |
|---|---|---|
| Phase 0 | One PR | `make build` → restart silo → curl `/ping` → iOS app smoke test |
| Phase 1 | One PR (or 4 sub-PRs if reviewable size matters) | iOS app: Bookmarks, Collections, Playlists, Stats tabs functional |
| Phase 2 | One PR | Two devices on same account: progress sync visible in real time |
| Phase 3 | One PR | `/api/me/sessions` shows real device names; leaked stream URL expires |

**Cross-repo coordination:** silo-android and silo-apple are NOT touched. The ABS-compat surface targets OFFICIAL ABS clients; silo's own clients use silo's native API.

**Rollback:** every migration has a working `.down.sql`. New tables don't break existing functionality; silo's native audiobook API (`/api/v1/audiobooks/*`) is untouched.

## 8. Open Follow-ups (Explicitly Out of Scope)

- ABS server-side import requests (`/api/me/request`) — Continuum had this; silo has its own `internal/requests/` system. Bridging is a future decision.
- Podcast episode-download queue events (`episode_download_*`) — only relevant if silo gains podcast download capability beyond RSS refresh.
- Backup events — silo's backup story is separate.
- Watch-Together over the ABS socket — silo has `internal/watchtogether/`, deferred.
- Reconnect replay buffer for socket events — Phase 2 leaves a hook but does not implement.

## 9. References

- Source-of-truth implementation: `continuum-plugin-audiobooks` (sibling worktree). Key files: `internal/abs/handler.go`, `bookmarks_handler.go`, `collections_handler.go`, `playlists_handler.go`, `smart_collection_handler.go`, `rss_feed_handler.go`, `continue_listening.go`, `abssocket/server.go`, `mediatoken/`.
- Bug catalog for response shape: `booklore-ng/BOOKLORE_ABS_IMPLEMENTATION_ISSUES.md`.
- Canonical socket event list: `booklore-ng/src/lib/socket/events.ts`.
- ABS API documentation: `booklore-ng/AUDIOBOOKSHELF_API_DOCUMENTATION.md`.

## 10. Phase 0 — Status

**Implemented:** 2026-05-26. Plan: `docs/superpowers/plans/2026-05-26-abs-phase-0-login-and-critical-fixes.md`. All 11 plan tasks committed on `feat/audiobooks` and deployed to the production silo container. Endpoint smoke tests confirm correct mount + status codes for `/ping`, `/login`, `/auth/refresh`, `/logout`, `/me`, `/authorize` at both root and `/api`/`/abs/api` prefixes. End-to-end iOS/Plappa app validation pending operator hand-off.

**Phase 0 commits (13 total — implementation + review-feedback fixes):**

| # | SHA | Subject |
|---|-----|---------|
| 1 | `6c9c721` | feat(audiobooks): diagnostic logging to ABS bearer auth and login |
| 1+ | `d5edb24` | chore(audiobooks): normalize slog "err" key + add path to secret-fetch log |
| 2 | `877120f` | fix(audiobooks): enrich ABS login envelope |
| 2+ | `47c04f1` | chore(audiobooks): reuse existing now var in login envelope |
| 3 | `045984c` | fix(audiobooks): /authorize returns identical envelope to /login |
| 3+ | `0306cfe` | docs(audiobooks): restore x-return-tokens and displayName fallback comments |
| 4 | `e163858` | fix(audiobooks): emit IDs on authors/series and stable genres/tags arrays |
| 4+ | `470a483` | fix(audiobooks): proper pagination total + tags key in play session |
| 5 | `7f600b2` | fix(audiobooks): hydrate filterdata authors and series in library detail |
| 5+ | `2e2b411` | chore(audiobooks): rename const cap to fetchCap in buildFilterData |
| 6 | `7e956f1` | fix(audiobooks): seed currentTime from ProgressStore so resume works |
| 7 | `93e321f` | feat(audiobooks): POST /auth/refresh for ABS token rotation |
| 8 | `278f815` | feat(audiobooks): POST /logout for ABS sign-out |

**Tests added:** 15 (6 metadata + 4 resume + 5 refresh + 4 logout — note 4 logout overlaps with refresh's memTokenStore). `go test ./internal/audiobooks/... -count=1` all pass.

**Next:** Phase 1 (bookmarks, collections, playlists, smart collections, RSS, author/series detail, listening stats) will start with its own brainstorming → writing-plans cycle after operator confirms Phase 0 works against real clients.
