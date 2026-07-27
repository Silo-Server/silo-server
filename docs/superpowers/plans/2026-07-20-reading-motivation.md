# Reading Motivation Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Streaks, yearly goals, auto monthly challenges, 18 persistent achievements, and Reading DNA — computed over existing sessions in the requester's timezone, rendered as new sections on `/reading-stats`.

**Architecture:** One new Go file (`reading_motivation.go`) beside the sessions handlers holds pure aggregation math (streaks/challenge/goals/DNA/badge criteria over fetched session rows), two small tables (`reading_goals`, `reading_achievements`), and two routes; a `tz` request param resolved via `time.LoadLocation` governs all day/hour boundaries and is also threaded (additively) into the existing history endpoint. Client: one motivation query hook + four presentational sections appended to `ReadingStats.tsx`.

**Tech Stack:** Go + chi + pgx + Goose; React 19 + TypeScript + React Query + vitest.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-20-reading-motivation-design.md` — binding values: 300s streak-day minimum; streak alive until a full local day missed; challenge target `max(prev calendar month seconds, 10800)`; goals ints 1..100000 or null; finished book = progress ≥ `models.EbookFinishedProgressThreshold` (0.9, `internal/models/ebook_progress.go`) with progress-row `updated_at` in the current local year; the 18 badge ids/criteria exactly as listed; diversity = round((1 − Σ share²) × 100); genre seconds split evenly across a book's genres, top 8 + "other"; authors via item_people kind 7; hour buckets morning 05–12 / afternoon 12–17 / evening 17–22 / night 22–05.
- Timezone: `tz` query param (IANA) on motivation + history endpoints; `time.LoadLocation`, invalid/absent → UTC. Sessions stay stored UTC. Achievement unlocks evaluated with the request's tz, never revoked.
- Motivation endpoint degrades per-section (partial payload, log the failed section) — never a whole-endpoint 500 for a section failure; goals PUT 400s with field-specific messages; badge persistence failures are silent skips.
- Routes additive in the `/ebooks` group; conventions identical to `reading_sessions.go` (identity via `apimw`, `writeJSON`/`writeError`).
- Gates per task: web — vitest focused suites, `pnpm exec tsc --noEmit -p tsconfig.app.json` 0 errors, `pnpm run lint` 0 errors (warnings baseline), prettier; Go — named test runs, `go vet ./internal/api/...`, `gofmt` clean, `make migrate-validate`.
- Conventional Commits + trailer `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`; web commands from `web/`, commits from repo root.

---

### Task 1: Migration + timezone plumbing + goals store/PUT

**Files:**
- Create: `migrations/sql/20260720230000_reading_motivation.sql`
- Create: `internal/api/handlers/reading_motivation.go`
- Create: `internal/api/handlers/reading_motivation_test.go`
- Modify: `internal/api/handlers/reading_sessions.go` (history handler gains tz-aware day math via the shared helper)
- Modify: `internal/api/router.go` (PUT route; motivation handler wiring — GET route lands in Task 3)

**Interfaces:**
- Produces:
  - `func requestLocation(r *http.Request) *time.Location` — parses `tz` query param, `time.LoadLocation`, fallback `time.UTC` (empty/invalid/`Local`-rejected).
  - `reading_goals` + `reading_achievements` tables per spec.
  - `type ReadingGoals struct { BooksPerYear *int; HoursPerYear *int }`, store methods on a new `ReadingMotivationStore` interface:
    - `GetGoals(ctx context.Context, userID int, profileID string) (*ReadingGoals, error)`
    - `PutGoals(ctx context.Context, userID int, profileID string, g ReadingGoals) error`
    - `AchievedAt(ctx context.Context, userID int, profileID string) (map[string]time.Time, error)`
    - `PersistAchievement(ctx context.Context, userID int, profileID, achievementID string, at time.Time) error`
    - `SessionsSince(ctx context.Context, userID int, profileID string, since time.Time) ([]ReadingSession, error)` (raw rows for the pure math)
    - `FinishedBooksInRange(ctx context.Context, userID int, profileID string, from, to time.Time) (int, error)`
    - `HasBookReadAbove(ctx context.Context, userID int, profileID string, threshold float64) (bool, error)` (Task-3-era addition for finisher badge)
    - `GenreSeconds(ctx context.Context, userID int, profileID string) ([]GenreSeconds, error)`
    - `AuthorSeconds(ctx context.Context, userID int, profileID string) ([]AuthorSeconds, error)`
    — implemented on `PGReadingMotivationStore` (pool).
  - `PUT /ebooks/reading-goals` handler (`HandlePutGoals` on `ReadingMotivationHandler{Store; Now func() time.Time}`).
  - History endpoint behavior change (additive): totals/days computed in `requestLocation(r)`.

- [ ] **Step 1: Migration**

```sql
-- migrations/sql/20260720230000_reading_motivation.sql
-- +goose Up
CREATE TABLE IF NOT EXISTS reading_goals (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    books_per_year INTEGER,
    hours_per_year INTEGER,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, profile_id)
);
CREATE TABLE IF NOT EXISTS reading_achievements (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    achievement_id TEXT NOT NULL,
    achieved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, profile_id, achievement_id)
);

-- +goose Down
DROP TABLE IF EXISTS reading_achievements;
DROP TABLE IF EXISTS reading_goals;
```

`make migrate-validate` → passes.

- [ ] **Step 2: Failing tests**

In `reading_motivation_test.go` (fake store maps; identity-context helpers as in `reading_sessions_test.go`):

```go
func TestRequestLocation(t *testing.T)
// ?tz=Europe/Amsterdam → that location; absent → UTC; ?tz=Nonsense → UTC; ?tz=Local → UTC.

func TestPutGoalsValidatesAndPersists(t *testing.T)
// {"books_per_year":30,"hours_per_year":200} → 204, stored;
// {"books_per_year":null,"hours_per_year":200} → books cleared, hours 200 (PUT replaces
//   BOTH fields; absent field = null = cleared — assert a second PUT omitting hours clears it);
// {"books_per_year":0} and {"hours_per_year":100001} → 400 with field-named message;
// non-JSON → 400.

func TestHistoryUsesRequestTimezone(t *testing.T)
// fake sessions store already exists in reading_sessions_test.go — a session at
// 2026-07-19T23:30:00Z with ?tz=Europe/Amsterdam (UTC+2) lands on local day
// 2026-07-20; without tz on day 2026-07-19. Assert the days rollup date strings differ.
// (Drive the EXISTING history handler; this pins the additive tz behavior.)
```

Run: `go test ./internal/api/handlers -run 'TestRequestLocation|TestPutGoals|TestHistoryUsesRequestTimezone' -count=1` → FAIL undefined.

- [ ] **Step 3: Implement**

`requestLocation` in `reading_motivation.go`:

```go
func requestLocation(r *http.Request) *time.Location {
    name := strings.TrimSpace(r.URL.Query().Get("tz"))
    if name == "" || name == "Local" {
        return time.UTC
    }
    loc, err := time.LoadLocation(name)
    if err != nil {
        return time.UTC
    }
    return loc
}
```

Goals: `PUT` decodes the following struct, validates each non-nil value 1..100000 (400 `invalid_goal` "books_per_year must be between 1 and 100000" / same for hours), calls `Store.PutGoals` to upsert, 204:

```go
type putGoalsRequest struct {
	BooksPerYear *int `json:"books_per_year"`
	HoursPerYear *int `json:"hours_per_year"`
}
```

History tz: in the existing history handler (`reading_sessions.go`), replace the UTC-fixed day computations (today/week/month boundaries + `DailyRollup` day grouping) to use `loc := requestLocation(r)`. For SQL day grouping pass the zone name: `date_trunc('day', started_at AT TIME ZONE $n)` (pass `loc.String()`; `"UTC"` for UTC) — adjust `DailyRollup`/`TotalsSince` signatures to accept `loc *time.Location` and compute range boundaries in that zone. Keep response field names unchanged.

Wire `readingMotivationHandler` in router.go beside `readingSessionsHandler`; route `r.Put("/reading-goals", readingMotivationHandler.HandlePutGoals)` (nil-guarded).

- [ ] **Step 4: Run + gates** — the three named tests PASS; existing `TestReadingStats*`/`TestReadingHeartbeat*` still green; vet/gofmt/migrate-validate clean.

- [ ] **Step 5: Commit**

```bash
git add migrations/sql/20260720230000_reading_motivation.sql internal/api/handlers/reading_motivation.go internal/api/handlers/reading_motivation_test.go internal/api/handlers/reading_sessions.go internal/api/router.go
git commit -m "feat(reader): reading goals and requester-timezone day math

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Pure aggregation math — streaks, challenge, goals progress, DNA, badge criteria

**Files:**
- Modify: `internal/api/handlers/reading_motivation.go`
- Modify: `internal/api/handlers/reading_motivation_test.go`

**Interfaces:**
- Consumes: `ReadingSession` rows (Task 1's `SessionsSince`).
- Produces (Task 3 calls these; all pure, no I/O):

```go
type DaySeconds map[string]int // "YYYY-MM-DD" in loc → seconds
func sessionDaySeconds(sessions []ReadingSession, loc *time.Location) DaySeconds
func streakFrom(days DaySeconds, today string) (current, longest int) // 300s minimum per spec
func monthChallenge(days DaySeconds, now time.Time, loc *time.Location) (targetSec, monthSec int)
func goalProjection(ytd float64, yearElapsedFraction float64) float64 // linear on-track-for
type dnaAggregates struct { Genres []GenreSeconds; Authors []AuthorSeconds; AvgSessionSeconds int; HoursByBucket map[string]int; ProjectedYearHours float64; DiversityScore int }
func computeDNA(sessions []ReadingSession, genres []GenreSeconds, authors []AuthorSeconds, now time.Time, loc *time.Location) dnaAggregates
type achievementInput struct { TotalSeconds int; LongestSessionSeconds int; CurrentStreak, LongestStreak int; BooksFinished int; NightSeconds, EarlyBirdSeconds, WeekendSeconds int; DistinctGenres int; MaxBookSeconds int; FinishedWithHighRead bool }
func evaluateAchievements(in achievementInput) []string // ids of satisfied badges
var achievementDefinitions []AchievementDefinition // 18 entries: {ID, Category, Name, Description}
```

- [ ] **Step 1: Failing tests** (table-driven; write fully — key cases):

```go
func TestSessionDaySecondsUsesLocation(t *testing.T)
// 23:30Z session, Europe/Amsterdam → next local day.

func TestStreakMath(t *testing.T)
// days {d-2:400, d-1:400, d:100} today=d → current 2 (today unqualified but alive), longest 2
// days {d-1:400, d:400} → current 2; gap breaks: {d-3:400, d-1:400, d:400} → current 2, longest 2
// sub-300s days never count; empty → 0,0.

func TestMonthChallenge(t *testing.T)
// prev month 20000s → target 20000; prev month 3600s → target 10800 (floor);
// month boundaries computed in loc (a session at prev-month-end 23:30Z with +02:00 counts current month).

func TestComputeDNA(t *testing.T)
// diversity: single genre → 0; two equal genres → 50; bucket assignment at 05/12/17/22 boundaries;
// avg session length; projection = ytdHours / elapsedFraction.

func TestEvaluateAchievements(t *testing.T)
// each of the 18 at its boundary: satisfied at exactly the threshold, unsatisfied just below
// (3600s→first-hour, 7200s single session→marathon, streak 3/7/30/100, books 1/10/50,
//  36000s night→night-owl, 36000s early→early-bird, 72000s weekend→weekender,
//  5 genres→genre-hopper, 36000s one book→deep-diver, FinishedWithHighRead→finisher).
```

Run → FAIL undefined.

- [ ] **Step 2: Implement** the pure functions per spec. Notes: `streakFrom` walks back from `today`; today counts if qualified but does not break the streak while unqualified; `monthChallenge` uses `time.Date(y, m, 1, 0,0,0,0, loc)` boundaries; `computeDNA` buckets by `started_at.In(loc).Hour()` (morning 5–11, afternoon 12–16, evening 17–21, night 22–4), diversity over genre-seconds shares, projection guards `elapsedFraction < 0.01` → use ytd as projection. 18 definitions in a package-level slice, ids exactly as spec.

- [ ] **Step 3: Run + gates** — named tests PASS; vet/gofmt.

- [ ] **Step 4: Commit**

```bash
git add internal/api/handlers/reading_motivation.go internal/api/handlers/reading_motivation_test.go
git commit -m "feat(reader): motivation aggregation math

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Motivation endpoint — assembly, achievement persistence, DNA queries

**Files:**
- Modify: `internal/api/handlers/reading_motivation.go` (+`HandleGetMotivation`, PG store queries for `FinishedBooksInRange`/`GenreSeconds`/`AuthorSeconds`)
- Modify: `internal/api/handlers/reading_motivation_test.go`
- Modify: `internal/api/router.go` (`r.Get("/reading-motivation", …)`)

**Interfaces:**
- Produces: `GET /ebooks/reading-motivation?tz=…` → the spec's exact payload (field names verbatim from the spec's API section). Achievements evaluated + newly-satisfied persisted via `PersistAchievement` (`ON CONFLICT DO NOTHING`), response's `achieved_at` from the merged map (existing ∪ new). Per-section degradation: each section computed in its own step; on error, log via `slog` and emit that section's zero value (empty arrays / zeroed struct), never abort.

- [ ] **Step 1: Failing tests**

```go
func TestMotivationEndpointShape(t *testing.T)
// fake store with sessions/goals/achievements fixtures → 200; assert exact JSON keys:
// streak{current_days,longest_days,today_seconds,today_qualified}, goals{...6 fields},
// challenge{target_seconds,month_seconds,percent}, achievements[18]{id,category,name,description,achieved_at},
// dna{genres,authors,diversity_score,avg_session_seconds,hours_by_bucket,projected_year_hours}.

func TestMotivationPersistsNewUnlocks(t *testing.T)
// fixtures satisfying first-hour + streak-3, store already has first-hour →
// PersistAchievement called ONLY for streak-3; response shows both achieved.

func TestMotivationDegradesPerSection(t *testing.T)
// GenreSeconds returns error → 200 with dna.genres = [] and other sections intact.

func TestMotivationUsesTimezone(t *testing.T)
// same 23:30Z-session trick: ?tz shifts streak/today attribution.
```

Run → FAIL.

- [ ] **Step 2: Implement.** SQL: `FinishedBooksInRange` = `SELECT COUNT(*) FROM ebook_reader_progress WHERE user_id=$1 AND profile_id=$2 AND progress >= $3 AND updated_at >= $4 AND updated_at < $5` (`$3 = models.EbookFinishedProgressThreshold`); `GenreSeconds` = per-genre even split:

```sql
SELECT g.genre, SUM(rs.duration_seconds::float / GREATEST(cardinality(mi.genres), 1))::bigint
FROM reading_sessions rs
JOIN media_items mi ON mi.content_id = rs.content_id
CROSS JOIN LATERAL unnest(mi.genres) AS g(genre)
WHERE rs.user_id=$1 AND rs.profile_id=$2
GROUP BY g.genre ORDER BY 2 DESC
```

`AuthorSeconds` joins `item_people ip ON ip.content_id = rs.content_id AND ip.kind = 7` + `people p ON p.id = ip.person_id`, grouped by `p.name`, top 8. Handler assembles: sessions (since epoch for totals; the pure funcs slice as needed), goals row, finished counts (local-year range), achievements merge+persist, DNA. Percent = `min(100, round(month/target*100))`.

- [ ] **Step 3: Run + gates** — all motivation tests + full handlers package PASS; vet/gofmt.

- [ ] **Step 4: Commit**

```bash
git add internal/api/handlers/reading_motivation.go internal/api/handlers/reading_motivation_test.go internal/api/router.go
git commit -m "feat(reader): reading motivation endpoint with persistent achievements

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Client — hooks, tz param, four page sections

**Files:**
- Modify: `web/src/hooks/queries/readingStats.ts` (+`useReadingMotivation`, `putReadingGoals`; existing hooks gain the `tz` param via a shared `clientTimezone()` helper)
- Create: `web/src/components/stats/MotivationSections.tsx` (streak+challenge row, goals card, badge grid, DNA card — four exported components)
- Create: `web/src/components/stats/MotivationSections.test.tsx`
- Modify: `web/src/pages/ReadingStats.tsx` (render the sections after totals)
- Modify: `web/src/pages/ReadingStats.test.tsx`

**Interfaces:**
- Consumes: motivation endpoint payload (Task 3 shape), `formatDuration`.
- Produces: `clientTimezone(): string` (`Intl.DateTimeFormat().resolvedOptions().timeZone ?? "UTC"`) exported from `readingStats.ts` and appended as `tz` to motivation AND history fetches; `useReadingMotivation()` (staleTime 5 min); `putReadingGoals({books_per_year, hours_per_year})`.

- [ ] **Step 1: Failing tests**

```typescript
// MotivationSections.test.tsx (fixture payload):
// streak row shows current/longest and challenge percent bar width;
// goals card: inputs prefilled, blur with changed value calls putReadingGoals (mocked),
//   invalid (0/negative) shows inline error and does not call; empty state when both null;
// badge grid renders 18, locked dimmed (aria-disabled or data-locked), achieved show date;
// DNA card renders top genres bars, authors, diversity number, buckets, projection.
// ReadingStats.test.tsx: sections render from mocked useReadingMotivation; history fetch
//   URL includes tz=<mocked zone> (mock Intl.DateTimeFormat resolvedOptions).
```

Run → FAIL (module missing).

- [ ] **Step 2: Implement.** Hooks per Task 4 interface (key `ebookKeys.readingMotivation()`); sections as presentational components taking the payload slices as props (page passes `data?.streak` etc., each section null-tolerant rendering its empty state). Styling: existing card conventions from the page (same wrappers as totals/heatmap sections); no new dependencies; badge grid = CSS grid of bordered tiles grouped by category headings.

- [ ] **Step 3: Run + gates** — new suites + ReadingStats + full `src/components/stats src/hooks src/pages/ReadingStats.test.tsx` green; tsc 0; lint 0; prettier.

- [ ] **Step 4: Commit**

```bash
git add web/src/hooks/queries/readingStats.ts web/src/components/stats/MotivationSections.tsx web/src/components/stats/MotivationSections.test.tsx web/src/pages/ReadingStats.tsx web/src/pages/ReadingStats.test.tsx
git commit -m "feat(reader): streaks, goals, badges, and Reading DNA on the stats page

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Full verification pass

- [ ] **Step 1:** `cd web && pnpm exec vitest run` — green except the 7 documented pre-existing upstream failures.
- [ ] **Step 2:** `cd web && pnpm run lint && pnpm run format:check && pnpm build`.
- [ ] **Step 3:** `go test ./internal/api/handlers -count=1 && go build ./...` (after pnpm build).
- [ ] **Step 4:** `make migrate-validate && make verify-local-paths`.
- [ ] **Step 5:** Reviewer note: manual pass — set goals, read past midnight local, confirm streak day attribution in CEST, unlock first-hour badge, DNA populates.
- [ ] **Step 6:** Commit fixes as `test(reader): stabilize motivation suites` (skip if clean).
