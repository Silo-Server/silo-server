# Audiobooks Absorption — Sub-plan 4: ABS-compat REST + Socket.io

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Port the audiobookshelf-compatible REST + Socket.io surface from `silo-plugin-audiobooks/internal/abs/` and `silo-plugin-audiobooks/internal/abssocket/` into `internal/audiobooks/abs/` and `internal/audiobooks/abssocket/`. **Preserve the plugin's file layout** but **rewrite every SQL query** against silo's existing tables (`media_items`, `media_files`, `episodes`, `user_watch_progress`, `user_playback_sessions`, `abs_sessions`, `people`, `item_people`). No new tables.

**Architecture:** Stage-based port. Support files first, then auth, then file serving, then progress, then author/series, then Socket.io, then router wiring. Out-of-scope features (bookmarks, smart collections, share links, playlists, podcasts/RSS in this sub-plan) are dropped — files containing them are not copied, or are copied with the unsupported handlers stubbed to 501 Not Implemented.

**Source plugin:** `/opt/silo_plugins/silo-plugin-audiobooks/internal/{abs,abssocket}/`

---

## Stage 1: Support files

**Files to copy and adapt:**
- `mount.go` — package mount + route registration shape
- `handler.go` — top-level handler type + dependency wiring
- `filter.go` — generic helpers
- `access_log.go` — request-log helper
- `minified.go` — generic helpers (likely small)

**Adapt steps:**
1. Replace import paths: `github.com/RXWatcher/silo-plugin-audiobooks/...` → `github.com/Silo-Server/silo-server/internal/audiobooks/...`
2. Replace plugin SDK references with direct silo equivalents:
   - Plugin's `*pgx.Pool` field on Handler → silo's `*pgxpool.Pool` (same type via different import alias)
   - Plugin's `Logger` → silo's `log/slog` package-level functions
   - Plugin's config types → struct fields on `audiobooks.Service` (extended)
3. Remove any code that talks to the plugin SDK runtime (plugin proxy callbacks etc.)
4. Get `go build ./internal/audiobooks/abs/...` to compile cleanly even if no routes are wired yet.

Commit: `feat(audiobooks): port ABS handler scaffolding (mount/handler/filter)`.

---

## Stage 2: Auth + JWT

**Files:**
- `jwt.go` — token generation + validation
- `login_internal_test.go` (keep as test) + `login_ratelimit.go`
- Whatever the actual `login.go` file is called (look for the login handler — likely inside `handler.go` or a dedicated file)

**Adapt:**
- The plugin's login handler posts credentials to silo's `/api/v1/auth/login` over HTTP. **In silo's in-process port, replace this HTTP round-trip with a direct call to silo's auth service** (find via `grep -nE 'auth.*Authenticate|auth.*Login' /opt/silo-server/internal/auth/*.go`). The plugin's network call is no longer needed because the code is now in-process.
- ABS sessions are stored in `abs_sessions` (migration 139 from sub-plan 1).
- JWT signing secret: store in `server_settings` under key `audiobooks.abs.jwt_secret` (auto-generated on first request if missing).

Commit: `feat(audiobooks): port ABS auth (JWT + login bridge)`.

---

## Stage 3: File serving + play session

**Files:**
- `file_handler.go` — handles ABS GET requests for audio file streams
- `play_response.go` — issues a play session to the client (URL + token)

**Adapt:**
- Plugin's `bookref` lookup table (linking ABS-style IDs to backend file IDs) → use silo's `media_files.id` directly. ABS-side item ID = silo's `content_id`; ABS-side file ID = silo's `media_files.id`.
- The stream URL ABS clients expect must point back at silo. Issue them a `/api/v1/direct-download?file_id={id}` URL signed with silo's existing media-token signer (grep `mediatoken` in `internal/`).

Commit: `feat(audiobooks): port ABS file handler + play session`.

---

## Stage 4: Progress tracking

**Files:**
- `progress_internal_test.go` (test, port)
- Whatever file owns the `/api/me/progress` ABS endpoint family — look in `handler.go` or grep ABS progress route paths
- `continue_listening.go`

**Adapt:**
- All progress reads/writes hit silo's `user_watch_progress` keyed on `(user_id, profile_id, content_id)`. Sub-plan 3's progress handler did this already — copy the patterns.

Commit: `feat(audiobooks): port ABS progress + continue listening`.

---

## Stage 5: Author / Series / Browse

**Files:**
- `author_series_handler.go`
- `collapse.go`

**Adapt:**
- Author rows come from silo's `people` + `item_people` (kind=7).
- Series grouping: if silo doesn't have an explicit "audiobook series" concept, use `media_items.title` prefix matching (e.g., books named "Foundation #1", "Foundation #2") for MVP. Document the heuristic.

Commit: `feat(audiobooks): port ABS author/series browse`.

---

## Stage 6: Socket.io

**Files:**
- `abssocket/server.go` + `abssocket/server_test.go`

**Adapt:**
- Reuse silo's `gorilla/websocket` upgrader. The plugin's standalone-listener escape hatch is no longer needed — mount under `/abs/socket.io/` on silo's main listener.
- Redis adapter behavior preserved if `REDIS_URL` is set (silo always has one in the docker setup).

Commit: `feat(audiobooks): port ABS Socket.io endpoint`.

---

## Stage 7: Router wiring

**Files:**
- Modify `internal/api/router.go` — mount `audiobookABSHandler` under `/abs/*` and the small set of `/api/*` legacy paths ABS clients hardcode
- Modify `internal/audiobooks/service.go` — add `ABSHandler` field, wire it from `audiobooks.New(...)`
- Modify `cmd/silo/main.go` — construct the ABS handler and pass it into the audiobooks service

**Routes to expose** (the minimum ABS-required set):
- `POST /abs/login` (login flow)
- `GET /abs/api/me` (profile)
- `GET /abs/api/libraries` (library list)
- `GET /abs/api/libraries/{id}/items` (browse)
- `GET /abs/api/items/{id}` (item detail)
- `GET /abs/api/items/{id}/file/{ino}` (stream)
- `POST /abs/api/me/progress/{libraryItemId}` (progress)
- `GET /abs/socket.io/` (Socket.io)

For ABS-legacy hard-coded paths that don't fit under `/abs/`: register them explicitly with the ABS auth middleware so they don't collide with silo's existing `/api/v1/*`.

Build + smoke:
```bash
sudo docker build -t silo:latest /opt/silo-server
sudo docker compose -p silo-prod up -d --force-recreate silo
curl -sS -o /dev/null -w "POST /abs/login -> %{http_code}\n" -X POST -H 'Content-Type: application/json' -d '{}' http://localhost:8090/abs/login
```

Expected: 400 (bad request body) or 401 (validation failed), NOT 404.

Commit: `feat(audiobooks): wire ABS routes into main router`.

---

## Stage 8: Build + smoke + lint sweep

Single final task. Same shape as sub-plan 2 Task 10.

---

## Risks

- The plugin's code expects a specific database schema. Some plugin queries may have no clean silo-table equivalent (e.g., the plugin's `bookref` mapping table). In that case, stub the handler to return 501 + log + flag in PR notes — don't add new tables.
- ABS clients have undocumented expectations (CORS headers, specific error shapes, etc.). Smoke testing against a real ABS client is the only way to find these. Defer to sub-plan 6's manual verification.
- Each stage commits independently and produces partial functionality (e.g., after stage 2 you can log in but can't list libraries). That's intentional — clients won't connect end-to-end until stage 7.
