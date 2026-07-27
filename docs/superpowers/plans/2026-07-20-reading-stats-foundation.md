# Reading Stats Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Server-side reading sessions from client heartbeats, pace/time-remaining in the reader footer, and a per-profile Reading Stats page with heatmap, totals, top books, and a session timeline.

**Architecture:** A `reading_sessions` table fed by a coalescing heartbeat handler (all logic in one new Go file beside the reader handlers); two read endpoints (per-book pace, profile history rollups) computed by query; a fake-timer-testable `useReadingHeartbeat` hook mounted by the prose reader; the footer's reserved time-left slot fed by a React Query hook; a new `/reading-stats` page of presentational components.

**Tech Stack:** Go + chi + pgx + Goose; React 19 + TypeScript + React Query + vitest.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-20-reading-stats-foundation-design.md` — constants and payload shapes there are binding: 30s heartbeat cadence, 60s activity window, 120s session gap, 90s per-beat credit cap, 14-day pace window, 10-min book / 30-min profile pace minimums, 5-min footer refresh, most-recent-50 sessions, UTC day boundaries.
- Routes additive, inside the `/ebooks` group (`internal/api/router.go` ~2230, `apimw.RequireProfile`); identity via `apimw.GetUserID`/`apimw.GetProfileID`; helpers `writeJSON`/`writeError` (handlers/auth.go:583); date params parsed like `calendar.go:101` (`time.Parse("2006-01-02", …)`).
- Comics never heartbeat; heartbeat responses ignored client-side; failures silent.
- Gates per task: focused tests; before commit — web: `pnpm exec tsc --noEmit -p tsconfig.app.json` 0 errors, `pnpm run lint` 0 errors (159-warning baseline), prettier; Go: `go test ./internal/api/handlers -run TestReadingSession -count=1` (plus new names), `go vet ./internal/api/...`, `gofmt` clean, `make migrate-validate`.
- Conventional Commits, trailer `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`; web commands from `web/`, commits from repo root.

---

### Task 1: Sessions backend — migration, store, heartbeat coalescing

**Files:**
- Create: `migrations/sql/20260720210000_reading_sessions.sql`
- Create: `internal/api/handlers/reading_sessions.go`
- Create: `internal/api/handlers/reading_sessions_test.go`
- Modify: `internal/api/router.go` (route + handler wiring beside `readerFontsHandler`)

**Interfaces:**
- Produces: `POST /ebooks/{content_id}/reading-heartbeat` body `{"fraction": number}` → 204 (400 invalid fraction). Go: `type ReadingSessionsHandler struct { Store ReadingSessionStore; Now func() time.Time }`, `ReadingSessionStore` interface (Task 2 adds its read methods to this same interface and file), `NewPGReadingSessionStore(pool)`. Constants `heartbeatSessionGap = 120 * time.Second`, `heartbeatMaxCredit = 90 * time.Second`.

- [ ] **Step 1: Migration**

```sql
-- migrations/sql/20260720210000_reading_sessions.sql
-- +goose Up
CREATE TABLE IF NOT EXISTS reading_sessions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    content_id TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    last_heartbeat_at TIMESTAMPTZ NOT NULL,
    duration_seconds INTEGER NOT NULL DEFAULT 0,
    start_fraction REAL NOT NULL,
    end_fraction REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_reading_sessions_recency
    ON reading_sessions (user_id, profile_id, last_heartbeat_at);
CREATE INDEX IF NOT EXISTS idx_reading_sessions_book
    ON reading_sessions (user_id, profile_id, content_id);

-- +goose Down
DROP TABLE IF EXISTS reading_sessions;
```

Run: `make migrate-validate` → passes.

- [ ] **Step 2: Failing tests (fake store, injected clock)**

In `reading_sessions_test.go` build `fakeReadingSessionStore` (in-memory slice implementing the store interface with a `latestOpen(user, profile, content)` helper) and drive the handler with `httptest` + chi route params + `apimw` test context (copy the identity-context setup pattern from `reader_fonts_test.go`). `Now` is a swappable `func() time.Time` on the handler. Write fully:

```go
func TestReadingHeartbeatStartsAndExtendsSessions(t *testing.T)
// t0: POST fraction 0.10 → 204; store has one session: duration 0, start=end=0.10.
// t0+30s: fraction 0.12 → same session: duration 30, end_fraction 0.12.
// t0+30s+200s (gap > 120s): fraction 0.13 → NEW session row; first session untouched.

func TestReadingHeartbeatCapsCredit(t *testing.T)
// beats at t0, t0+100s (gap 100 < 120 extends, credit min(100,90)=90): duration 90.

func TestReadingHeartbeatValidatesFraction(t *testing.T)
// fraction -0.1, 1.5, NaN (json "NaN" is invalid → decode error), missing → 400; store untouched.

func TestReadingHeartbeatScopedToProfile(t *testing.T)
// beats from profile A then profile B on same book → two separate sessions.
```

Run: `go test ./internal/api/handlers -run TestReadingHeartbeat -count=1` → FAIL undefined symbols.

- [ ] **Step 3: Implement**

`reading_sessions.go`:

```go
type ReadingSession struct {
    ID              int64
    UserID          int
    ProfileID       string
    ContentID       string
    StartedAt       time.Time
    LastHeartbeatAt time.Time
    DurationSeconds int
    StartFraction   float64
    EndFraction     float64
}

type ReadingSessionStore interface {
    LatestOpen(ctx context.Context, userID int, profileID, contentID string, since time.Time) (*ReadingSession, error)
    Insert(ctx context.Context, s ReadingSession) error
    Extend(ctx context.Context, id int64, lastHeartbeatAt time.Time, addSeconds int, endFraction float64) error
}
```

`HandleHeartbeat`: identity from `apimw`; decode `{Fraction float64}` (reject !(<0..1> finite)); `now := h.Now()`; `open, _ := Store.LatestOpen(..., now.Add(-heartbeatSessionGap))`; if open → `credit := min(now.Sub(open.LastHeartbeatAt), heartbeatMaxCredit)`, `Store.Extend(open.ID, now, int(credit.Seconds()), fraction)`; else `Store.Insert` with duration 0, start=end=fraction, started=lastHeartbeat=now. Respond 204 always on success; store errors → 500 (client ignores). PG store: `LatestOpen` = `SELECT … WHERE user_id=$1 AND profile_id=$2 AND content_id=$3 AND last_heartbeat_at >= $4 ORDER BY last_heartbeat_at DESC LIMIT 1`; `Extend` = `UPDATE … SET last_heartbeat_at=$2, duration_seconds=duration_seconds+$3, end_fraction=$4 WHERE id=$1`.

Route in the `/ebooks` group: `if readingSessionsHandler != nil { r.Post("/{content_id}/reading-heartbeat", readingSessionsHandler.HandleHeartbeat) }`; wire `readingSessionsHandler = &handlers.ReadingSessionsHandler{Store: handlers.NewPGReadingSessionStore(deps.DB), Now: func() time.Time { return time.Now().UTC() }}` beside `readerFontsHandler`.

- [ ] **Step 4: Run + gates**

`go test ./internal/api/handlers -run TestReadingHeartbeat -count=1` PASS; `go vet ./internal/api/...`; `gofmt -l internal/` clean for touched files; `make migrate-validate`.

- [ ] **Step 5: Commit**

```bash
git add migrations/sql/20260720210000_reading_sessions.sql internal/api/handlers/reading_sessions.go internal/api/handlers/reading_sessions_test.go internal/api/router.go
git commit -m "feat(reader): coalesce reading heartbeats into sessions

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Stats read endpoints — pace/time-left + history rollups

**Files:**
- Modify: `internal/api/handlers/reading_sessions.go` (+ store methods, two handlers, route lines)
- Modify: `internal/api/handlers/reading_sessions_test.go`
- Modify: `internal/api/router.go` (two `r.Get` lines in the same block)

**Interfaces:**
- Produces:
  - `GET /ebooks/{content_id}/reading-stats` → `{"pace_fraction_per_hour": number|null, "time_left_seconds": number|null, "book_seconds": number}`
  - `GET /ebooks/reading-stats?from=YYYY-MM-DD&to=YYYY-MM-DD` → the spec's `{totals, days, books, sessions}` shape (field names exactly as spec; `sessions` capped 50, newest first; `books` newest-read first; missing titles → `"Removed book"`).
- Store gains: `PaceWindow(ctx, userID int, profileID, contentID string, since time.Time) (fractions float64, seconds int, err error)` (contentID `""` = all books), `BookSeconds(ctx, …) (int, error)`, `DailyRollup(ctx, userID int, profileID string, from, to time.Time) ([]DayTotal, error)`, `BookTotals(…) ([]BookTotal, error)`, `RecentSessions(…, limit int) ([]SessionRow, error)`, `TotalsSince(ctx, …, since time.Time) (int, error)`.
- Pure, unit-tested: `func paceAndTimeLeft(bookF float64, bookSec int, allF float64, allSec int, progress float64) (paceFPH *float64, timeLeftSec *int64)` implementing the spec thresholds (book ≥600s wins; else all ≥1800s; else nil).

- [ ] **Step 1: Failing tests**

```go
func TestPaceAndTimeLeftThresholds(t *testing.T)
// book 0.2 fractions over 1200s, progress 0.5 → pace 0.6/h, timeLeft (0.5/0.0001667)≈3000s (assert ±1)
// book only 300s of data, all-books 0.3 over 3600s → falls back to all-books pace
// both under minimums → nil, nil
// zero/negative fraction deltas with enough seconds → nil (guard divide + nonsense pace)

func TestReadingStatsBookHandler(t *testing.T)
// fake store returns canned PaceWindow/BookSeconds + progress via a fake progress getter;
// asserts exact JSON field names and null handling.

func TestReadingStatsHistoryHandler(t *testing.T)
// fake store: days/books/sessions fixtures; default range (no params) = last 366 days;
// bad from/to → 400; titles LEFT-JOIN fallback "Removed book" (fake returns empty title);
// sessions capped at 50 (fixture 60 → assert 50).
```

Progress lookup: the per-book handler needs the profile's current fraction — reuse the existing progress store the reader handlers already use (`EbookReaderHandler`'s progress path in `ebook_reader.go`; inject as a small interface `readingProgressGetter { GetProgress(ctx, userID, profileID, contentID) (float64, bool, error) }` implemented by the existing PG progress store — check its exact type and wrap if needed).

Run: FAIL undefined.

- [ ] **Step 2: Implement**

`paceAndTimeLeft` exactly per spec (return values as pointers; `paceFPH = fractions/seconds*3600`). Handlers: per-book — identity, progress lookup (no progress row → progress 0), two `PaceWindow` calls (`since = now-14d`), `BookSeconds`, respond with the trio (nulls when pace nil). History — parse optional from/to (`calendar.go` pattern), default `to=today`, `from=to-365d`, clamp `from<=to`; `TotalsSince` × today/-7d/-30d/all (zero time); assemble spec shape. SQL: `DailyRollup` = `SELECT date_trunc('day', started_at)::date, SUM(duration_seconds) … GROUP BY 1 ORDER BY 1`; `BookTotals` joins `media_items mi ON mi.content_id = reading_sessions.content_id` LEFT for title; `RecentSessions` same join, `ORDER BY started_at DESC LIMIT $n`.

Routes: `r.Get("/reading-stats", h.HandleHistory)` MUST be registered before chi's `/{content_id}/…` wildcard routes in the group so the literal path wins (chi matches literals first regardless of order, but keep it adjacent and add a route test if in doubt — `TestReadingStatsHistoryHandler` covers the handler; add one router-level assertion only if a conflict actually surfaces).

- [ ] **Step 3: Run + gates** — `go test ./internal/api/handlers -run 'TestPaceAndTimeLeft|TestReadingStats' -count=1` PASS; vet/gofmt clean.

- [ ] **Step 4: Commit**

```bash
git add internal/api/handlers/reading_sessions.go internal/api/handlers/reading_sessions_test.go internal/api/router.go
git commit -m "feat(reader): pace, time-left, and history endpoints

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: useReadingHeartbeat hook + reader mount

**Files:**
- Create: `web/src/hooks/useReadingHeartbeat.ts`
- Create: `web/src/hooks/useReadingHeartbeat.test.ts`
- Modify: `web/src/pages/EbookReader.tsx` (mount beside `useTTS()`/`useScreenWakeLock` ~line 299; feed activity from existing relocate/pointer/key paths)

**Interfaces:**
- Produces:

```typescript
export function shouldHeartbeat(input: { visible: boolean; lastActivityAt: number; now: number }): boolean; // activity within 60_000ms and visible
export function useReadingHeartbeat(options: {
  contentId: string | null;      // null disables (comics, no book)
  getFraction: () => number;     // current position
  send?: (contentId: string, fraction: number) => void; // default posts the API
}): { noteActivity: () => void };
```

- Default `send` posts `/ebooks/{id}/reading-heartbeat` via the same api client as `ebookReaderApi.ts`, `.catch(() => {})`.

- [ ] **Step 1: Failing tests (vi.useFakeTimers)**

```typescript
// shouldHeartbeat: visible+fresh → true; hidden → false; stale (>60s) → false.
// hook (renderHook or a tiny harness component like other hook tests — check
// web/src/hooks/*.test.* for the convention; if none exists, mount via a test component):
it("sends immediately on first activity, then every 30s while active", ...)
// noteActivity() → send called once; advance 30s → second call; advance 30s → third.
it("stops when idle or hidden and resumes on activity", ...)
// advance 90s with no activity → no further sends; noteActivity() → immediate send again.
// simulate visibilitychange hidden → no sends even with activity.
it("does nothing when contentId is null", ...)
```

Run: FAIL — module missing.

- [ ] **Step 2: Implement**

Interval started lazily on first `noteActivity`; every tick evaluates `shouldHeartbeat({visible: !document.hidden, lastActivityAt, now: Date.now()})`; sends `send(contentId, clamp01(getFraction()))`. `visibilitychange` listener updates and can trigger an immediate re-check. Cleanup on unmount/contentId change. Refs for callbacks (stable interval).

- [ ] **Step 3: Mount in EbookReader**

```tsx
const { noteActivity: noteReadingActivity } = useReadingHeartbeat({
  contentId: isComicFormat ? null : contentId || null,
  getFraction: () => locationInfoRef.current?.fraction ?? readerProgress ?? 0,
});
```

Call `noteReadingActivity()` from: the existing `handleLocationChange` (relocate), `dispatchSurfaceTap`, and the keydown handler (one line each). Extend the FoliateBookReader mock in `EbookReader.test.tsx` only if tsc requires; add one page-level test: relocate then 30s advance → mocked heartbeat api called (mock `@/hooks/useReadingHeartbeat`'s default send via module mock OR pass-through — prefer mocking the api module `useReadingHeartbeat` imports).

- [ ] **Step 4: Suites + gates** — `pnpm exec vitest run src/hooks/useReadingHeartbeat.test.ts src/pages/EbookReader.test.tsx` PASS; tsc/lint/prettier clean.

- [ ] **Step 5: Commit**

```bash
git add web/src/hooks/useReadingHeartbeat.ts web/src/hooks/useReadingHeartbeat.test.ts web/src/pages/EbookReader.tsx web/src/pages/EbookReader.test.tsx
git commit -m "feat(reader): activity-gated reading heartbeats

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Footer time-left

**Files:**
- Create: `web/src/hooks/queries/readingStats.ts`
- Modify: `web/src/reader/ReaderFooter.tsx` (right slot: `34% · 2h 10m left`)
- Create: `web/src/lib/formatDuration.ts` (+ `formatDuration.test.ts`)
- Modify: `web/src/pages/EbookReader.tsx` (pass `timeLeftSeconds` to footer)
- Test: extend `web/src/reader/ReaderFooter.test.tsx`

**Interfaces:**
- Produces: `formatDuration(seconds: number): string` — `"2h 10m"`, `"45m"`, `"<1m"` (floor minutes; hours+minutes; under 60s → `"<1m"`). `useBookReadingStats(contentId: string | undefined)` React Query hook (queryKey `["reading-stats", contentId]`, `staleTime`/`refetchInterval` 5 minutes, enabled on prose contentId) returning the per-book endpoint shape. `ReaderFooterProps` gains `timeLeftSeconds?: number | null`.

- [ ] **Step 1: Failing tests** — `formatDuration` cases above; ReaderFooter: `timeLeftSeconds: 7800` renders `2h 10m left` beside the percentage, `null`/undefined renders percentage only (assert absence of "left").

- [ ] **Step 2: Implement** — formatter; hook (mirror `useEbookReaderProgress` in `web/src/hooks/queries/ebookReaderProgress.ts`); footer right slot `<span>{percent}%{timeLeftSeconds != null && ` · ${formatDuration(timeLeftSeconds)} left`}</span>`; EbookReader calls the hook (prose only) and passes `data?.time_left_seconds ?? null`.

- [ ] **Step 3: Suites + gates** — footer/lib/page suites + tsc/lint/prettier.

- [ ] **Step 4: Commit**

```bash
git add web/src/lib/formatDuration.ts web/src/lib/formatDuration.test.ts web/src/hooks/queries/readingStats.ts web/src/reader/ReaderFooter.tsx web/src/reader/ReaderFooter.test.tsx web/src/pages/EbookReader.tsx
git commit -m "feat(reader): time-remaining estimate in the footer

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Reading Stats page

**Files:**
- Create: `web/src/pages/ReadingStats.tsx`
- Create: `web/src/pages/ReadingStats.test.tsx`
- Create: `web/src/components/stats/ReadingHeatmap.tsx` (+ pure helper `heatmapBuckets` in the same file, exported for tests)
- Modify: `web/src/hooks/queries/readingStats.ts` (add `useReadingHistory(from?, to?)`)
- Modify: `web/src/App.tsx` (route) and `web/src/components/SideNav.tsx` (nav entry)

**Interfaces:**
- Consumes: history endpoint shape (Task 2), `formatDuration` (Task 4).
- Produces: route `/reading-stats` wrapped `<RequireProfile><Layout><ReadingStats /></Layout></RequireProfile>` (match the exact wrapper nesting used by `/catalog` in App.tsx ~L493); SideNav entry "Reading stats" with the `BookOpen` lucide icon following the file's existing entry structure.

- [ ] **Step 1: Failing tests**

```typescript
// heatmapBuckets(days, max): 0 → bucket 0; quartiles of max → 1..4; returns 366-cell grid
// aligned to weeks (leading blanks for partial first week).
// ReadingStats page (mock useReadingHistory): renders totals row (formatDuration applied),
// top books list with titles incl. "Removed book" fallback rows, sessions timeline entries,
// and a heatmap cell count matching fixture range.
```

- [ ] **Step 2: Implement** — page skeleton per `AccessibilitySettings.tsx` conventions (`space-y-6`, `text-2xl font-semibold tracking-tight` header) but as a full page under Layout: totals as a 4-stat row, `ReadingHeatmap` (CSS grid, 53×7, `bg-primary` opacity steps via bucket classes, title tooltip `date · Xm`), top books (link to `/item/{content_id}` unless removed), sessions list (date, book, duration, fraction range). Hook: `useReadingHistory` with default range (no params → server default).

- [ ] **Step 3: Suites + gates** — new suites + `pnpm exec vitest run src/pages/ReadingStats.test.tsx src/components` and the standard web gates.

- [ ] **Step 4: Commit**

```bash
git add web/src/pages/ReadingStats.tsx web/src/pages/ReadingStats.test.tsx web/src/components/stats/ReadingHeatmap.tsx web/src/hooks/queries/readingStats.ts web/src/App.tsx web/src/components/SideNav.tsx
git commit -m "feat(reader): reading stats page with heatmap and history

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Full verification pass

- [ ] **Step 1:** `cd web && pnpm exec vitest run` — green except the 7 documented pre-existing upstream failures.
- [ ] **Step 2:** `cd web && pnpm run lint && pnpm run format:check && pnpm build` — 0 errors; build ok.
- [ ] **Step 3:** `go test ./internal/api/handlers -count=1 && go build ./...` (after pnpm build) — green.
- [ ] **Step 4:** `make migrate-validate && make verify-local-paths` — pass.
- [ ] **Step 5:** Reviewer note: manual pass — read a book 2+ minutes, confirm a session row, footer estimate appears after enough data, stats page populates; comics produce no heartbeats.
- [ ] **Step 6:** Commit fixes as `test(reader): stabilize stats suites` (skip if clean).
