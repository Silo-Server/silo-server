# Audiobooks Absorption — Sub-plan 3: Silo-native API + Frontend (MVP)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Land the minimum viable silo-native audiobooks UI: backend REST endpoints + React pages so a user can browse audiobook libraries scanned by sub-plan 2, see book details with chapters, play with progress saved. Author/series index pages, smart collections, share links, and other features from the spec's "nice-to-haves" list are deferred to a future sub-plan if the demand surfaces.

**Architecture:** Three new Go handler files under `internal/api/handlers/audiobooks_*.go` for list/detail/progress. New `web/src/pages/audiobooks/` directory with three pages: Library, Detail, and an inline player. Hooks under `web/src/hooks/audiobooks/`. The streaming path reuses silo's existing `/api/v1/stream/{session_id}` endpoint — no new transcode code.

**Tech stack:** Go + chi router + pgx for backend; React 19 + TanStack Query + radix-ui + tailwind for frontend.

**Source spec:** `docs/superpowers/specs/2026-05-24-audiobooks-absorption-design.md`
**Predecessor:** `docs/superpowers/plans/2026-05-24-audiobooks-absorption-2-scanner.md` (scanner produces `media_items.type='audiobook'`)

---

## File Structure

| Path | C/M | Purpose |
|---|---|---|
| `internal/api/handlers/audiobooks_list.go` | C | `HandleListAudiobooks` — GET /api/v1/audiobooks (paginated, filterable by library) |
| `internal/api/handlers/audiobooks_detail.go` | C | `HandleGetAudiobook` — GET /api/v1/audiobooks/{id} (returns item + chapters + author/narrator + listening progress) |
| `internal/api/handlers/audiobooks_progress.go` | C | `HandleReportAudiobookProgress` — POST /api/v1/audiobooks/{id}/progress |
| `internal/api/router.go` | M | Mount three new routes under `/api/v1/audiobooks/*` |
| `web/src/hooks/audiobooks/useAudiobookLibrary.ts` | C | TanStack Query hook for list |
| `web/src/hooks/audiobooks/useAudiobook.ts` | C | Detail + progress hooks |
| `web/src/pages/audiobooks/AudiobookLibrary.tsx` | C | Grid page (poster + title + author) |
| `web/src/pages/audiobooks/AudiobookDetail.tsx` | C | Detail page (cover, metadata, chapter list, play button) |
| `web/src/pages/audiobooks/AudiobookPlayer.tsx` | C | HTML5 `<audio>` player with chapter nav + progress reporting |
| `web/src/App.tsx` or routing entry | M | Add the three new routes |
| `web/src/components/Sidebar.tsx` (or equivalent) | M | Add "Audiobooks" navigation link when at least one audiobook library exists |

---

## Task 1: Backend — list audiobooks

GET /api/v1/audiobooks?library_id=N&limit=...&offset=... → paginated audiobook list scoped to libraries the user has access to.

**Files:** Create `internal/api/handlers/audiobooks_list.go`, modify `internal/api/router.go`.

### 1.1 Test

Add `internal/api/handlers/audiobooks_list_test.go` with a test that:
- Inserts two `media_items` rows (one `type='audiobook'`, one `type='movie'`)
- Calls the handler with a fake request
- Asserts only the audiobook is returned

If no test harness exists for handler-level tests in `internal/api/handlers/`, search neighbors (`grep -lE 'func.*Handler.*Test' internal/api/handlers/*_test.go`) for the established pattern. If integration testing requires a real DB and no harness exists, write the handler logic + a simple compile-time test only; defer the integration test (DONE_WITH_CONCERNS).

### 1.2 Handler

```go
package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

type AudiobookHandler struct {
	Items *catalog.ItemRepository
}

func (h *AudiobookHandler) HandleListAudiobooks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	filter := apimw.AccessFilterFor(r)
	items, total, err := h.Items.Search(r.Context(), "", []string{"audiobook"}, limit, offset, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "list audiobooks failed")
		return
	}
	resp := struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}{
		Items:  itemSummariesAsMaps(items),
		Total:  total,
		Limit:  limit,
		Offset: offset,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func itemSummariesAsMaps(items []*models.MediaItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"content_id": it.ContentID,
			"title":      it.Title,
			"year":       it.Year,
			"poster_url": it.PosterPath, // silo's poster column; UI prefixes with media-token base URL
		})
	}
	return out
}
```

Verify `catalog.ItemRepository.Search` matches the signature above by reading `internal/catalog/item_repo.go:739`. Adjust if needed.

### 1.3 Route + commit

In `internal/api/router.go`, find where other v1 routes are mounted and add:

```go
r.Get("/audiobooks", audiobookHandler.HandleListAudiobooks)
```

Wire the handler construction near other handler constructors. The handler needs `catalog.ItemRepository` — already available in the router's deps.

Commit: `feat(audiobooks): list endpoint at GET /api/v1/audiobooks`.

---

## Task 2: Backend — audiobook detail

GET /api/v1/audiobooks/{id} → media_item + media_files (with chapters) + author/narrator from item_people + per-profile listening progress.

### 2.1 Handler

Create `internal/api/handlers/audiobooks_detail.go`. The handler queries:
1. `media_items WHERE content_id = $1 AND type = 'audiobook'`
2. `media_files WHERE content_id = $1` (returns paths + chapters JSONB)
3. `item_people JOIN people ON ... WHERE content_id = $1 AND kind IN (7, 8)`
4. `user_watch_progress WHERE content_id = $1 AND profile_id = $current`

Reuse existing repo methods where they exist. If not, add inline SQL.

Response shape:

```json
{
  "audiobook": { "content_id": "...", "title": "...", "year": 2024, "overview": "...", "poster_url": "..." },
  "author": "Test Author",
  "narrator": "Test Narrator",
  "files": [
    { "id": 123, "path": "/...", "duration_seconds": 3600, "chapters": [ {"index": 0, "title": "Intro", "start_seconds": 0, "end_seconds": 27.9} ] }
  ],
  "progress": { "position_seconds": 1842.5, "updated_at": "2026-05-24T..." }
}
```

### 2.2 Route + commit

In `router.go`: `r.Get("/audiobooks/{id}", audiobookHandler.HandleGetAudiobook)`.

Commit: `feat(audiobooks): detail endpoint at GET /api/v1/audiobooks/{id}`.

---

## Task 3: Backend — progress endpoint

POST /api/v1/audiobooks/{id}/progress with body `{"position_seconds": 1842.5, "media_file_id": 123}`. UPSERTs `user_watch_progress`.

### 3.1 Handler

Create `internal/api/handlers/audiobooks_progress.go`. Body is JSON; handler parses, validates `position_seconds >= 0`, then upserts into `user_watch_progress` keyed on `(user_id, profile_id, media_item_id)`.

Reuse silo's existing progress-write helper if one exists (grep `INSERT INTO user_watch_progress` or `UpsertWatchProgress`). If not, write inline SQL using the existing DB pool from handler deps.

### 3.2 Route + commit

`r.Post("/audiobooks/{id}/progress", audiobookHandler.HandleReportAudiobookProgress)`.

Commit: `feat(audiobooks): progress endpoint at POST /api/v1/audiobooks/{id}/progress`.

---

## Task 4: Frontend — TanStack Query hooks + types

**Files:**
- Create: `web/src/lib/audiobooks/types.ts`
- Create: `web/src/hooks/audiobooks/useAudiobookLibrary.ts`
- Create: `web/src/hooks/audiobooks/useAudiobook.ts`
- Create: `web/src/hooks/audiobooks/useReportAudiobookProgress.ts`

### 4.1 Types

```ts
// web/src/lib/audiobooks/types.ts
export interface AudiobookSummary {
  content_id: string;
  title: string;
  year: number;
  poster_url: string | null;
}

export interface AudiobookListResponse {
  items: AudiobookSummary[];
  total: number;
  limit: number;
  offset: number;
}

export interface AudiobookChapter {
  index: number;
  title: string;
  start_seconds: number;
  end_seconds: number;
}

export interface AudiobookFile {
  id: number;
  path: string;
  duration_seconds: number;
  chapters: AudiobookChapter[];
}

export interface AudiobookProgress {
  position_seconds: number;
  updated_at: string;
}

export interface AudiobookDetail {
  audiobook: { content_id: string; title: string; year: number; overview: string; poster_url: string | null };
  author: string;
  narrator: string;
  files: AudiobookFile[];
  progress: AudiobookProgress | null;
}
```

### 4.2 Hooks

Use TanStack Query patterns silo already uses (grep `useQuery` for examples). Cache keys: `["audiobooks", "library", libraryId]` and `["audiobook", id]`. Progress mutation invalidates the detail query on success.

### 4.3 Commit

Commit: `feat(audiobooks): frontend types and TanStack Query hooks`.

---

## Task 5: Frontend — Audiobook Library page

**Files:**
- Create: `web/src/pages/audiobooks/AudiobookLibrary.tsx`

Grid of audiobook cards (poster + title + author). Use silo's existing card / grid components — grep `web/src/components/` for `MediaCard` or similar. Infinite scroll via `useInfiniteQuery` would be ideal but a simple paginated load-more is fine for MVP.

Route URL: `/audiobooks` (or `/audiobooks/library/:id` if there are multiple libraries). For MVP, single library route `/audiobooks` is fine.

Commit: `feat(audiobooks): library grid page`.

---

## Task 6: Frontend — Audiobook Detail page

**Files:**
- Create: `web/src/pages/audiobooks/AudiobookDetail.tsx`

Layout: cover image (poster) + metadata (title, author, year, overview) + chapter list + play button. Chapter list is collapsible; clicking a chapter starts playback at that chapter's start_seconds.

Route URL: `/audiobooks/book/:id`.

Commit: `feat(audiobooks): detail page with chapter list`.

---

## Task 7: Frontend — Audiobook Player

**Files:**
- Create: `web/src/pages/audiobooks/AudiobookPlayer.tsx` (or `web/src/player/AudiobookPlayer.tsx`)

HTML5 `<audio>` element. The stream URL for an audiobook file is silo's existing `/api/v1/stream/{session_id}` — call POST /api/v1/playback/session to start a session (existing silo endpoint), then point the `<audio src>` at the stream URL. Position updates throttled to every 5-10 seconds + on pause/seek.

Controls: play/pause, skip-30s-forward, skip-30s-back, playback rate select (0.5×, 0.75×, 1×, 1.25×, 1.5×, 2×, 3×), chapter list panel.

If silo's existing stream-session-start endpoint shape isn't obvious, grep for `playback/session` and copy the pattern from the video player.

Commit: `feat(audiobooks): HTML5 audio player with chapter navigation`.

---

## Task 8: Navigation + routing integration

**Files:**
- Modify: `web/src/App.tsx` (or wherever routes are registered) — add three new routes
- Modify: `web/src/components/Sidebar.tsx` (or equivalent) — add "Audiobooks" nav link

The sidebar link should only show when at least one audiobook library exists. Use a `useAudiobookLibrary` query with a short staleTime to gate visibility, OR a separate `useLibrariesOfKind("audiobooks")` hook.

Commit: `feat(audiobooks): wire navigation and routes`.

---

## Task 9: Build, lint, smoke

```bash
cd /opt/silo-server
go build ./...
go test ./internal/api/handlers/audiobooks_*_test.go ./internal/scanner/... ./internal/models/... ./internal/audiobooks/...
go vet ./...
cd web && pnpm run lint && pnpm run format:check && cd ..
make build
```

Rebuild silo image + force-recreate container:

```bash
sudo docker build -t silo:latest /opt/silo-server
sudo docker compose -p silo-prod up -d --force-recreate silo
until [ "$(sudo docker inspect -f '{{.State.Health.Status}}' silo-prod-silo-1 2>/dev/null)" = "healthy" ]; do sleep 2; done; echo healthy
curl -sS -o /dev/null -w "GET /api/v1/audiobooks -> %{http_code}\n" http://localhost:8090/api/v1/audiobooks
```

If any sweep changes (gofmt, prettier) were made, commit them with `chore(audiobooks): sweep formatting`.

---

## Risks

- Frontend tasks depend on silo's existing component shapes. Subagents will need to grep `web/src/components/` for the right patterns. If they invent ad-hoc components, ask them to use existing ones.
- The progress endpoint UPSERT shape depends on `user_watch_progress` PK shape — which discovery D4 confirmed is `(user_id, profile_id, content_id)`. Confirm and use exactly that ON CONFLICT target.
- Audiobook libraries may not yet exist on the test stack. To smoke-test, an operator must manually update a `media_folders.type` to `'audiobooks'` and trigger a rescan after the sub-plan 2 scanner branch lands.
