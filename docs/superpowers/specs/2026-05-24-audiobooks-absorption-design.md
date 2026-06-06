# Audiobooks Absorption — Design Spec

**Date:** 2026-05-24
**Branch context:** `feat/audiobooks` in `silo-server`
**Status:** Approved — ready for implementation plan

## Goal

Absorb the `silo-plugin-audiobooks` plugin into the primary `silo-server`
Go application. Eliminate it as a plugin. User-visible outcome:

- Silo's existing web UI gains audiobook + podcast browse/playback.
- Audiobookshelf-compatible (ABS) mobile/desktop clients connect to silo
  directly and play audiobooks from libraries silo already scans.

## Hard constraints

- **Minimize changes.** Reuse silo-server's existing tables and infrastructure
  wherever they fit. Add new tables only where there is no existing equivalent.
- **No separate SPA.** Audiobook UI lives inside silo's existing React app at
  `web/src/`. The plugin's standalone SPA is dropped.
- **Audiobook files = scanned by silo.** Silo's existing scanner discovers
  audiobook files in configured library paths the same way it scans movies
  and TV. The `silo-plugin-local-audiobooks` plugin is no longer needed.
- **No external backends.** BookWarehouse / other audiobook backend plugins
  are out of scope. Local filesystem is the only source.
- **No standalone HTTP listener.** Everything runs on silo's main `:8080`
  listener, including ABS Socket.io. The plugin's standalone-listener
  workaround existed only because the host plugin-proxy could not bridge
  WebSocket upgrades; that problem disappears once code runs in-process.

## Scope

### In

- Audiobook entity, library, playback (silo SPA + ABS clients).
- ABS-compatible REST API + Socket.io realtime channel.
- Podcasts: subscribed RSS feeds with episode refresh, plus filesystem podcasts.
- Per-profile listening progress, active play sessions, basic chapter navigation.

### Out

- Audiobook requests flow (`silo-plugin-audiobook-requests` untouched).
- Smart collections, share links, content restrictions, metadata-provider integration.
- Embedding-powered "similar books" recommender.
- External enrichment through the audiobook metadata plugin/provider — deferred;
  scanner extracts local tags and identifiers for v1.
- Group listening / cowatch parity for audiobooks (silo already has cowatch for
  video; revisit later if needed).

## Architecture

### Package layout

New top-level Go package: `internal/audiobooks/`.

```text
internal/audiobooks/
  abs/          ← ported from plugin's internal/abs/         (~6.8k LOC)
                  ABS-compatible REST handlers, mounted under /abs/* and the
                  legacy /api/* paths ABS clients hardcode.
  abssocket/    ← ported from plugin's internal/abssocket/   (~250 LOC)
                  Socket.io protocol on top of gorilla/websocket; optional
                  Redis pub/sub adapter for multi-replica fan-out.
  podcastfeed/  ← ported from plugin's internal/podcastfeed/ (~350 LOC)
                  RSS feed refresher; registered as a scheduled task.
  service.go    ← thin orchestrator wiring the above to silo's existing
                  catalog/playback/session/auth services.
```

### Wiring touchpoints in existing silo code

| Existing file | Change |
|---|---|
| `internal/api/router.go` | Mount silo-native audiobook routes under `/api/v1/audiobooks/*`; mount ABS routes under `/abs/*` plus the small set of legacy `/api/*` paths ABS clients hit; mount the `/abs/socket.io/` endpoint. |
| `internal/scanner/` | Per-library dispatch on `media_libraries.kind`. New parsers for `audiobooks` and `podcasts`. |
| `cmd/silo/main.go` (task wiring) | Register `podcastfeed.Refresher` as a scheduled task. |
| `internal/playback/` | Accept `media_items.type='audiobook'` and `'podcast_episode'` (mostly already generic). |
| `web/src/pages/`, `web/src/player/` | New audiobook/podcast pages and player component. |

### What the plugin contributed that is dropped

The plugin's other packages — `server`, `store`, `enrich`, `recommend`,
`smartcoll`, `event`, `consumer`, `libsync`, `migrate`, `runtime`,
`bookref`, `cdn`, `mediatoken`, `streaming` — are either dropped (out-of-scope
features) or replaced by reuse of silo's existing equivalents (catalog store,
media-token signing, CDN, streaming, event bus, migrations runner).

### Total code budget

- ~7.4k LOC ported from the plugin.
- ~1.5–2k LOC new TypeScript/TSX in silo's SPA.
- ~400 LOC of new Go glue (scanner branches, router wiring, service.go).
- Three new SQL migrations in the current stack (`147_abs_sessions`,
  `157_podcast_feeds`, `160_audiobooks_feature_flag`) plus one documented
  no-op migration for the already-present media-folder type column.

## Data model

### Reused silo tables (no schema changes)

| Audiobook concept | Silo table | Notes |
|---|---|---|
| Audiobook entity | `media_items` | New value: `type='audiobook'`. Existing title/year/overview/poster/sort_title cover the basics. |
| Podcast (show) | `media_items` | New value: `type='podcast'`. |
| Podcast episode | `episodes` | Parallel to TV episodes; FK to parent `media_items` row. |
| Audio file + chapters | `media_files` | The `chapters jsonb` column from migration 066 stores chapters exactly as silo already does for video. |
| Library scope | `media_libraries` + `media_folders` + `media_item_libraries` | Audiobook libraries are libraries with `kind='audiobooks'` (see below). |
| Author / narrator | `people` + `item_people` | Two new role string constants: `'author'`, `'narrator'`. |
| Listening position | `user_watch_progress` | Per-profile position in file. Naming reads as video-only but works fine for audio. |
| Active play session | `user_playback_sessions` | Generic enough to host audiobook sessions. |
| Generic auth | `auth_sessions` | Used by silo's own clients. ABS clients use the new `abs_sessions` table. |
| Series / author shelves | `library_collections` + `library_collection_items` | Reuse for audiobook series, "by author" collections. |

### New tables (two migrations)

1. **`abs_sessions`** (migration 147) — parallel to existing `jellycompat_sessions`. Tracks ABS client device, token hash, last-seen so ABS apps reconnect without re-auth. Columns: `id`, `user_id`, `token_hash`, `device_id`, `device_name`, `abs_client_version`, `created_at`, `last_seen_at`. Same shape as `jellycompat_sessions` plus the `abs_client_version` text.

2. **`podcast_feeds`** (migration 157) — one row per subscribed podcast. Columns: `media_item_id` (FK to the podcast `media_items` row), `feed_url`, `etag`, `last_refreshed_at`, `refresh_interval_seconds`. Episode rows live in the reused `episodes` table.

### Schema-touch column add

- `media_libraries.kind` — `'movies' | 'tv' | 'audiobooks' | 'podcasts'`. If a
  similar column already exists on `media_libraries`, reuse it; otherwise this
  is a third small migration. The scanner reads it to pick the per-library
  parser.

### Plugin migrations NOT ported

Smart collections, share links, embeddings, content restrictions,
metadata-provider integration, request provider, file cache, listening stats aggregates,
reading goals, notification prefs, standalone-mode tables, recommender
embeddings. All represent features dropped from scope.

### Data migration

None. The plugin's `audiobooks.*` Postgres schema is left intact until cutover
is confirmed, then dropped. Silo's scanner repopulates from the filesystem on
first run. ABS clients re-authenticate once (their tokens lived in the plugin
DB, not silo's). This is the one user-visible cost.

## Routing & ABS-compatibility

All routes mount on silo's main `:8080` HTTP listener.

### 1. Silo-native audiobook API

- Prefix: `/api/v1/audiobooks/*`.
- Examples: `GET /api/v1/audiobooks/library/{id}/items`,
  `GET /api/v1/audiobooks/{item_id}`,
  `POST /api/v1/audiobooks/{item_id}/progress`.
- Mounted next to other v1 routes. Standard silo auth middleware. JSON shapes
  follow silo's existing patterns — not the ABS shape.

### 2. ABS-compatible API

- Prefix: `/abs/*` for the bulk of client-facing endpoints, plus the small set
  of `/api/*` paths ABS clients hardcode (e.g. `/api/libraries`, `/api/me`,
  `/api/items/{id}`). To avoid colliding with silo's existing `/api/v1/*`,
  ABS gets its full namespace at `/abs/`; ABS-legacy `/api/*` paths are
  registered explicitly and scoped to ABS auth.
- Auth: a separate middleware `audiobooks.RequireABSSession` validates the ABS
  bearer token against `abs_sessions`.
- Login: `POST /abs/login` accepts ABS-shaped credentials, internally calls
  silo's existing auth backend (same code path as `POST /api/v1/auth/login`),
  then issues an ABS token bound to a row in `abs_sessions`. No password
  handling lives in audiobooks code itself.

### 3. ABS Socket.io endpoint

- Path: `/abs/socket.io/`.
- Uses silo's existing `gorilla/websocket` upgrader. The ported `abssocket/`
  package handles Socket.io handshake, framing, and the long-polling fallback.
- Multi-replica fan-out: if `REDIS_URL` is set, use a Redis pub/sub adapter
  (already implemented in plugin). Unset → in-memory single-replica.

### What goes away

- No standalone listener — main `:8080` only.
- No `SILO_HOST_URL` / `SILO_PLUGIN_TOKEN` env plumbing — those existed because
  the plugin called back into silo over HTTP. Direct Go calls replace this.
- No host plugin-proxy in the request path.

### Streaming reuse

ABS stream URLs internally rewrite to silo's existing
`/api/v1/stream/{session_id}` machinery — direct play, range requests,
transcode fallback if needed, media-token signing. **Zero new transcode or
stream code.**

## Scanner integration

### Per-library dispatch (~30 LOC in `internal/scanner`)

The scanner reads `media_libraries.kind` and picks a parser. Existing
`movies`/`tv` paths are unchanged. New branches: `audiobooks`, `podcasts`.

### Audiobook parser (~200 LOC, `internal/scanner/audiobook.go`)

- Folder convention: one audiobook = one folder. Files = chapters in filename
  order, or one `.m4b` with embedded chapters.
- Recognized extensions: `.m4b`, `.mp3`, `.m4a`, `.flac`, `.opus`.
- Single `.m4b`: one `media_items` row + one `media_files` row. Embedded
  chapters extracted via the existing ffprobe call site → `media_files.chapters`.
- Multi-file folder: one `media_items` row + N `media_files` rows. Chapter
  index = filename ordering; each file's `chapters` carries one synthesized
  chapter for that file.
- Metadata: ID3 / MP4 tags (title, author, narrator, series, year, cover).
  Reuses silo's existing tag-extraction helpers.

### Podcast parser (~150 LOC, `internal/scanner/podcast.go`)

Two paths:

1. **Filesystem podcasts** (downloaded episodes on disk): folder = podcast,
   file = episode. Same `series → episodes` shape silo already handles for TV.
2. **RSS-subscribed podcasts**: no filesystem walk. The `podcastfeed.Refresher`
   scheduled task fetches RSS, upserts `media_items` (podcast) + `episodes`,
   optionally downloads enclosures to a configured cache dir (reusing silo's
   existing download/cache infrastructure). `media_files` rows point at the
   cached file once downloaded.

### Author / narrator extraction

Tag-derived names → upsert into `people` → link via `item_people` with roles
`'author'` / `'narrator'`. Same upsert pattern silo already uses for actors
and directors. No new code, just two more role string constants.

### External enrichment

Deferred. The scanner extracts local tags and identity hints only. Canonical
enrichment remains the responsibility of the audiobook metadata plugin/provider
path and is not ported in this foundation pass.

### Scheduled task

`podcastfeed.Refresher` runs every 10 minutes (matches plugin behavior).
Registered with silo's existing task manager. No other new background jobs.

### Impact on existing scanner behavior

None for movie/TV libraries. Audiobook/podcast libraries were not being
scanned before; now they are when `kind` is set appropriately.

## Web UI surfaces

### Stack

Silo's existing React 19 + Vite SPA at `web/src/`. React Query, radix-ui,
tailwind. The plugin's SPA uses the same stack so component idioms transfer
cleanly, but we are writing fresh silo pages — the plugin's `web/` is dropped.

### New pages under `web/src/pages/audiobooks/`

| Route | Purpose |
|---|---|
| `/audiobooks` | Home: continue-listening shelf, recent additions, library-wide browse. |
| `/audiobooks/library/:id` | Library view: grid of audiobooks, filter/sort, paginated. |
| `/audiobooks/book/:id` | Detail: cover, author, narrator, series, chapter list, play/continue CTA. |
| `/audiobooks/authors`, `/audiobooks/series` | Index pages built on `library_collections`. |
| `/podcasts` | Subscribed podcasts grid. |
| `/podcasts/show/:id` | Podcast detail + episode list. |
| `/podcasts/episode/:id` | Episode detail + play. |

### Player

`web/src/player/AudiobookPlayer.tsx`:

- HTML5 `<audio>` (no HLS / transcoding needed for typical audio). Falls back
  to silo's existing transcode flow only if the file needs it.
- Chapter list panel, sleep timer, playback rate (0.5×–3×), 30-second seek
  buttons, skip-silence toggle.
- Position updates via `POST /api/v1/audiobooks/{id}/progress` at 5–10s
  intervals + on pause/seek. Mirrors silo's existing video progress cadence.

### Navigation

Add "Audiobooks" and "Podcasts" to the existing sidebar/library switcher,
driven by `media_libraries.kind`. Same dispatch point silo uses today for
Movies vs TV.

### Reused silo components (no copies)

Card grids, library shelves, search box, profile/auth chrome, image cache,
virtualized list (`@tanstack/react-virtual` already in `package.json`).

### Not building (out of scope)

Smart collections UI, share-link manager, request submission UI, embeddings
"similar books", admin enrich button, content-restriction admin.

### Frontend volume

Roughly 8 new page components + 1 player + a handful of audiobook-specific
hooks/types under `web/src/hooks/audiobooks/` and `web/src/lib/audiobooks/`.
~1.5–2k LOC of new TS/TSX total. No new top-level dependencies beyond what
silo's `package.json` already has.

## Plugin retirement & rollout

### Sibling repos

| Repo | Action |
|---|---|
| `silo-plugin-audiobooks` | Archive on GitHub; remove from catalog. |
| `silo-plugin-local-audiobooks` | Archive (silo scanner replaces it). |
| `silo-plugin-bookwarehouse-audio` | Archive (out of scope). |
| `silo-plugin-audiobook-requests` | Untouched — request flow out of scope. |

Stance: **archive, do not delete.** Leaves a recoverable home for any feature
dropped from this port that might come back later.

### Catalog update

One-line PR in `silo-plugins`: remove the three audiobook-related plugin
entries from `manifest.json`. The fourth (audiobook-requests) stays.

### Rollout sequence

1. Land the absorbed code in silo-server behind a feature-flag server setting
   (`audiobooks.enabled`, default off).
2. Side-by-side: silo-server has audiobooks compiled in, the plugin is still
   installed and running on this host. Verify silo's flow end-to-end
   (scanner → SPA → ABS clients).
3. Stop the plugin runtime. Flip `audiobooks.enabled` on. ABS clients
   reconnect to silo's `/abs/*` endpoints (config change in the ABS app if
   the base URL differs).
4. Remove plugin entries from catalog; archive repos.
5. After confidence in cutover, `DROP SCHEMA audiobooks CASCADE` in Postgres.

### Rollback

Revert the silo flag. Until step 4 ships, the plugin is still installed and
runnable — single-flag rollback.

### Data migration

None. Scanner repopulates from filesystem on first run. ABS clients
re-authenticate once. This is the one user-visible cost.

## Testing

- **Unit tests** for the ported `abs/`, `abssocket/`, `podcastfeed/` packages
  carry over from the plugin where they apply; rewrite tests that depended on
  the plugin's separate schema.
- **Integration tests** against the real silo Postgres (per project preference
  to avoid mocking the DB):
  - Scanner: audiobook folder → `media_items` + `media_files` + chapters.
  - ABS auth: `POST /abs/login` issues a token rooted in silo's `users`.
  - ABS playback: GET a library → GET an item → stream a chapter → progress upsert.
  - Podcast refresher: feed URL → upserted episodes.
- **Manual E2E**: an ABS mobile client points at silo, browses, plays, scrubs
  through chapters, sleep-timer expires; silo SPA shows the listening
  progress and resumes from the right chapter.

## Risks & open questions

- **Socket.io protocol port quality.** The plugin's `abssocket/` is ~250 LOC of
  Socket.io framing on top of gorilla/websocket. It works today for the
  plugin; once mounted on silo's main listener, verify the long-polling
  fallback path still works under silo's middleware stack (auth, rate-limit,
  request-id).
- **`media_libraries.kind` column.** If the existing schema already has an
  equivalent column under a different name, use that instead. Verify before
  writing migration 134/135.
- **`media_files.chapters` shape compatibility.** Silo's chapter JSONB shape
  was designed for video chapters. Confirm the audiobook ffprobe output maps
  cleanly into the same shape, or extend the JSON tolerantly.
- **Search.** The plugin had its own search index for books/authors. Silo's
  existing FTS (catalog/pg-fts) needs to index audiobooks too; verify that
  setting `type='audiobook'` is enough or whether the FTS index needs a small
  config tweak.
- **Profile-scoped vs user-scoped state.** `user_watch_progress` is keyed —
  confirm it's keyed on profile, not just user (CLAUDE.md notes silo
  separates login accounts from household profiles).

## Next step

Hand off to the `writing-plans` skill to produce a phased implementation plan
covering migrations, package ports, router wiring, scanner branches, SPA
pages, and cutover. The plan's first phase should resolve the open questions
in the **Risks** section before code lands.
