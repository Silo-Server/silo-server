# Calendar Presets & Personalized Default — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the calendar's `All / Favorites / Watchlist` toggles with a Simkl-style preset switcher — **Following** (default), **Popular here**, **Trending**, **Everything** — powered by existing watch data and caches, plus a per-profile "watched" overlay on event cards.

**Architecture:** Every preset resolves to *the windowed airing candidate list ∩ a small id-set*. The heavy windowed SQL is unchanged; presets add a single `itemIDExpr = ANY($ids)` intersection. Popular/Trending id-sets are read from already-cached global sources (no per-request aggregation); the Following id-set is resolved per request from `user_favorites` ∪ `user_watchlist` ∪ watched-series. A best-effort per-profile lookup decorates events with watched status.

**Tech Stack:** Go (pgx/v5, chi router), PostgreSQL, React + TypeScript (TanStack Query, react-router, Tailwind, vitest).

**Spec:** `docs/superpowers/specs/2026-05-29-calendar-presets-design.md`

**Commit convention:** end every commit message body with
`Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`

**Environment note:** if executing inside a worktree under `.claude/worktrees/`, prefix every `go` command with `GOWORK=off` (the parent `go.work` otherwise pins the module to main and `go build`/`test` fail). Commands assume the repository root is the cwd.

---

## File Structure

Backend (`internal/`):
- `catalog/calendar_repo.go` — *modify.* Swap the favorites/watchlist `EXISTS` filter for a generic id-set restriction on `CalendarFilter`.
- `catalog/calendar_personal.go` — *create.* Per-profile resolvers: followed / favorites / watchlist id-sets + watched lookup. Methods on `*CalendarRepository`, kept out of the base-query file.
- `catalog/calendar_repo_test.go` — *modify.* Add restriction-SQL assertions.
- `catalog/calendar_personal_test.go` — *create.* Assert resolver SQL shape (DB-free, mirrors the existing query-fragment test style).
- `api/handlers/calendar.go` — *modify.* Validate the expanded preset set, resolve the id-set per preset, short-circuit empty, apply the watched overlay, add the `watched` response field.
- `api/handlers/calendar_test.go` — *modify.* Stub the new sources; cover each preset + watched + validation.
- `api/router.go` — *modify.* Wire the popular + trending sources into `NewCalendarHandler`.

Frontend (`web/src/`):
- `hooks/queries/calendar.ts` — *modify.* Add `watched?: boolean` to `CalendarEvent`.
- `pages/Calendar.tsx` — *modify.* Preset model, responsive pills↔dropdown, persistence, empty-state nudge.
- `pages/Calendar.test.tsx` — *modify.* Update default-filter assertion.
- `components/calendar/CalendarEventCard.tsx` — *modify.* Watched overlay.

---

## Task 1: Generic id-set restriction on the calendar query

**Files:**
- Modify: `internal/catalog/calendar_repo.go`
- Test: `internal/catalog/calendar_repo_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/catalog/calendar_repo_test.go`:

```go
func TestBuildListEventsQuery_AppliesIDRestriction(t *testing.T) {
	t.Parallel()

	repo := &CalendarRepository{}
	query, args := repo.buildListEventsQuery(CalendarFilter{
		Start:         time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC),
		End:           time.Date(2026, time.April, 12, 0, 0, 0, 0, time.UTC),
		RestrictByIDs: true,
		RestrictToIDs: []string{"series-1", "movie-2"},
	})

	for _, fragment := range []string{
		"mi.content_id = ANY($3)",
		"e.series_id = ANY($3)",
		"s.series_id = ANY($3)",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("expected query to contain %q, got:\n%s", fragment, query)
		}
	}
	if len(args) != 3 {
		t.Fatalf("expected start/end/ids args, got %d", len(args))
	}
}

func TestBuildListEventsQuery_EmptyRestrictionMatchesNothing(t *testing.T) {
	t.Parallel()

	repo := &CalendarRepository{}
	query, args := repo.buildListEventsQuery(CalendarFilter{
		Start:         time.Date(2026, time.April, 6, 0, 0, 0, 0, time.UTC),
		End:           time.Date(2026, time.April, 12, 0, 0, 0, 0, time.UTC),
		RestrictByIDs: true,
		RestrictToIDs: nil,
	})

	if strings.Count(query, "1 = 0") != 3 {
		t.Fatalf("expected each branch to short-circuit with 1 = 0, got:\n%s", query)
	}
	if len(args) != 2 {
		t.Fatalf("expected only start/end args, got %d", len(args))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/catalog/ -run TestBuildListEventsQuery_ -v`
Expected: FAIL — `CalendarFilter` has no field `RestrictByIDs` / `RestrictToIDs` (compile error).

- [ ] **Step 3: Update `CalendarFilter`**

In `internal/catalog/calendar_repo.go`, replace the struct (drop `Filter`, `UserID`, `ProfileID`; add the restriction fields):

```go
// CalendarFilter holds the parameters for a calendar query.
type CalendarFilter struct {
	Start              time.Time
	End                time.Time
	LibraryID          *int
	AllowedLibraryIDs  []int
	DisabledLibraryIDs []int
	MaxContentRating   string

	// RestrictByIDs limits results to items whose movie content_id (movies) or
	// series_id (episodes / season premieres) is in RestrictToIDs. When
	// RestrictByIDs is true and RestrictToIDs is empty, no rows match. Callers
	// resolve the id-set (Following / Popular / Trending) before querying.
	RestrictByIDs bool
	RestrictToIDs []string
}
```

- [ ] **Step 4: Thread a shared restriction arg through `buildListEventsQuery`**

Replace the arg-setup and branch-builder calls in `buildListEventsQuery`:

```go
func (r *CalendarRepository) buildListEventsQuery(f CalendarFilter) (string, []any) {
	var args []any
	argIdx := 1

	// Shared date range args.
	startArg := argIdx
	args = append(args, f.Start)
	argIdx++
	endArg := argIdx
	args = append(args, f.End)
	argIdx++

	// Optional id-set restriction, appended once and shared by all branches.
	// restrictArg stays 0 when the set is empty so branches short-circuit.
	restrictArg := 0
	if f.RestrictByIDs && len(f.RestrictToIDs) > 0 {
		restrictArg = argIdx
		args = append(args, f.RestrictToIDs)
		argIdx++
	}

	movieBranch := r.buildMovieBranch(startArg, endArg, restrictArg, f, &args, &argIdx)
	filteredEpisodes := r.buildFilteredEpisodesCTE(startArg, endArg, restrictArg, f, &args, &argIdx)
	filteredSeasons := r.buildFilteredSeasonsCTE(startArg, endArg, restrictArg, f, &args, &argIdx)
	episodeBranch := r.buildEpisodeBranch()
	seasonBranch := r.buildSeasonBranch()

	query := fmt.Sprintf(`WITH filtered_episodes AS (%s),
     episode_seasons AS (
       SELECT DISTINCT series_id, season_number
       FROM filtered_episodes
     ),
     season_finales AS (
       SELECT e.series_id, e.season_number, MAX(e.episode_number) AS max_episode_number
       FROM episodes e
       JOIN episode_seasons es ON es.series_id = e.series_id AND es.season_number = e.season_number
       GROUP BY e.series_id, e.season_number
     ),
     filtered_seasons AS (%s),
     episode_one_with_air_date AS (
       SELECT DISTINCT e.series_id, e.season_number
       FROM episodes e
       JOIN filtered_seasons fs ON fs.series_id = e.series_id AND fs.season_number = e.season_number
       WHERE e.episode_number = 1 AND e.air_date IS NOT NULL
     )
SELECT content_id, type, title, episode_title, series_id,
       season_number, episode_number, air_date, air_time, air_timezone,
       poster_path, poster_thumbhash,
       is_premiere, is_finale
FROM (
%s UNION ALL %s UNION ALL %s
) combined
%s`,
		filteredEpisodes, filteredSeasons, movieBranch, episodeBranch, seasonBranch, calendarEventsOrderByClause)

	return query, args
}
```

- [ ] **Step 5: Add the restriction clause and call it from each branch**

Replace `appendPersonalFilterClause` (delete it entirely) with:

```go
// appendRestrictClause limits a branch to the id-set in CalendarFilter. restrictArg
// is the positional parameter holding the id array (0 when the set is empty). An
// empty restriction matches nothing.
func (r *CalendarRepository) appendRestrictClause(itemIDExpr string, restrictArg int, f CalendarFilter, conditions *[]string) {
	if !f.RestrictByIDs {
		return
	}
	if restrictArg == 0 {
		*conditions = append(*conditions, "1 = 0")
		return
	}
	*conditions = append(*conditions, fmt.Sprintf("%s = ANY($%d)", itemIDExpr, restrictArg))
}
```

Update the three branch builders' signatures and add the restrict call (the rest of each function body is unchanged):

```go
func (r *CalendarRepository) buildMovieBranch(startArg, endArg, restrictArg int, f CalendarFilter, args *[]any, argIdx *int) string {
	conditions := []string{
		"mi.type = 'movie'",
		fmt.Sprintf("mi.release_date BETWEEN $%d::date AND $%d::date", startArg, endArg),
	}
	r.appendLibraryExistsClauses("mi.content_id", f, &conditions, args, argIdx)
	r.appendContentRatingClause("mi", f, &conditions, args, argIdx)
	r.appendRestrictClause("mi.content_id", restrictArg, f, &conditions)

	return fmt.Sprintf(`SELECT mi.content_id, 'movie'::text AS type,
       mi.title, NULL::text AS episode_title, NULL::text AS series_id,
       NULL::int AS season_number, NULL::int AS episode_number,
       mi.release_date AS air_date, NULL::text AS air_time, NULL::text AS air_timezone,
       mi.poster_path, mi.poster_thumbhash,
       FALSE AS is_premiere, FALSE AS is_finale
FROM media_items mi
WHERE %s`, strings.Join(conditions, " AND "))
}

func (r *CalendarRepository) buildFilteredEpisodesCTE(startArg, endArg, restrictArg int, f CalendarFilter, args *[]any, argIdx *int) string {
	conditions := []string{
		fmt.Sprintf("e.air_date BETWEEN $%d::date AND $%d::date", startArg, endArg),
		"e.season_number > 0", // exclude specials
	}
	r.appendLibraryExistsClauses("e.series_id", f, &conditions, args, argIdx)
	r.appendContentRatingClause("mi", f, &conditions, args, argIdx)
	r.appendRestrictClause("e.series_id", restrictArg, f, &conditions)

	return fmt.Sprintf(`SELECT e.content_id, e.series_id, e.season_number,
       e.episode_number, e.title AS episode_title, e.air_date,
       mi.title AS title, mi.air_time, mi.air_timezone,
       mi.poster_path, mi.poster_thumbhash
FROM episodes e
JOIN media_items mi ON mi.content_id = e.series_id
WHERE %s`, strings.Join(conditions, " AND "))
}

func (r *CalendarRepository) buildFilteredSeasonsCTE(startArg, endArg, restrictArg int, f CalendarFilter, args *[]any, argIdx *int) string {
	conditions := []string{
		fmt.Sprintf("s.air_date BETWEEN $%d::date AND $%d::date", startArg, endArg),
		"s.season_number > 0", // exclude specials
	}
	r.appendLibraryExistsClauses("s.series_id", f, &conditions, args, argIdx)
	r.appendContentRatingClause("mi", f, &conditions, args, argIdx)
	r.appendRestrictClause("s.series_id", restrictArg, f, &conditions)

	return fmt.Sprintf(`SELECT s.content_id, s.series_id, s.season_number,
       s.title AS episode_title, s.air_date, mi.title AS title, mi.air_time, mi.air_timezone,
       COALESCE(NULLIF(s.poster_path, ''), mi.poster_path) AS poster_path,
       COALESCE(NULLIF(s.poster_thumbhash, ''), mi.poster_thumbhash) AS poster_thumbhash
FROM seasons s
JOIN media_items mi ON mi.content_id = s.series_id
WHERE %s`, strings.Join(conditions, " AND "))
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/catalog/ -run TestBuildListEventsQuery -v`
Expected: PASS (including the pre-existing `UsesCTEs`, `UsesNotExistsForDisabledLibraries`, `RejectsExplicitLibraryOutsideAllowedScope` — arg indices are unchanged because the restriction arg is only appended when present).

- [ ] **Step 7: Commit**

```bash
git add internal/catalog/calendar_repo.go internal/catalog/calendar_repo_test.go
git commit -m "feat(calendar): generalize personal filter to an id-set restriction"
```

---

## Task 2: Per-profile resolvers (followed / favorites / watchlist / watched)

**Files:**
- Create: `internal/catalog/calendar_personal.go`
- Test: `internal/catalog/calendar_personal_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/catalog/calendar_personal_test.go`:

```go
package catalog

import (
	"strings"
	"testing"
)

func TestFollowedItemIDsQuery_UnionsAllSignals(t *testing.T) {
	for _, fragment := range []string{
		"FROM user_favorites",
		"FROM user_watchlist",
		"FROM user_watch_progress wp",
		"LEFT JOIN episodes e ON e.content_id = wp.media_item_id",
		"COALESCE(e.series_id, wp.media_item_id)",
		"UNION",
	} {
		if !strings.Contains(followedItemIDsQuery, fragment) {
			t.Fatalf("followedItemIDsQuery missing %q:\n%s", fragment, followedItemIDsQuery)
		}
	}
}

func TestWatchedItemIDsQuery_FiltersCompletedWithinSet(t *testing.T) {
	for _, fragment := range []string{
		"FROM user_watch_progress",
		"completed = true",
		"media_item_id = ANY($3)",
	} {
		if !strings.Contains(watchedItemIDsQuery, fragment) {
			t.Fatalf("watchedItemIDsQuery missing %q:\n%s", fragment, watchedItemIDsQuery)
		}
	}
}

func TestFavoriteAndWatchlistQueries_ScopeToProfile(t *testing.T) {
	if !strings.Contains(favoriteItemIDsQuery, "FROM user_favorites") ||
		!strings.Contains(favoriteItemIDsQuery, "profile_id = $2") {
		t.Fatalf("favoriteItemIDsQuery wrong:\n%s", favoriteItemIDsQuery)
	}
	if !strings.Contains(watchlistItemIDsQuery, "FROM user_watchlist") ||
		!strings.Contains(watchlistItemIDsQuery, "profile_id = $2") {
		t.Fatalf("watchlistItemIDsQuery wrong:\n%s", watchlistItemIDsQuery)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/catalog/ -run "TestFollowedItemIDsQuery|TestWatchedItemIDsQuery|TestFavoriteAndWatchlistQueries" -v`
Expected: FAIL — `followedItemIDsQuery` etc. undefined (compile error).

- [ ] **Step 3: Create the resolvers**

Create `internal/catalog/calendar_personal.go`:

```go
package catalog

import (
	"context"
	"fmt"
)

// Per-profile id-set queries backing the calendar presets. All run on the same
// pool as the base calendar query. The watched-series rollup intentionally
// mirrors recommendations.GetPopularItems (COALESCE(e.series_id, wp.media_item_id))
// so "engaged with a series" means the same thing everywhere; it is a one-line
// SQL expression, not worth a cross-package extraction.
const (
	followedItemIDsQuery = `
SELECT media_item_id FROM user_favorites WHERE user_id = $1 AND profile_id = $2
UNION
SELECT media_item_id FROM user_watchlist WHERE user_id = $1 AND profile_id = $2
UNION
SELECT DISTINCT COALESCE(e.series_id, wp.media_item_id)
FROM   user_watch_progress wp
LEFT JOIN episodes e ON e.content_id = wp.media_item_id
WHERE  wp.user_id = $1 AND wp.profile_id = $2`

	favoriteItemIDsQuery  = `SELECT media_item_id FROM user_favorites WHERE user_id = $1 AND profile_id = $2`
	watchlistItemIDsQuery = `SELECT media_item_id FROM user_watchlist WHERE user_id = $1 AND profile_id = $2`

	watchedItemIDsQuery = `
SELECT media_item_id
FROM   user_watch_progress
WHERE  user_id = $1 AND profile_id = $2 AND completed = true AND media_item_id = ANY($3)`
)

// ListFollowedItemIDs returns the profile's followed set: favorited ∪ watchlisted ∪
// any series/movie they have watch progress on.
func (r *CalendarRepository) ListFollowedItemIDs(ctx context.Context, userID int, profileID string) ([]string, error) {
	return r.queryIDs(ctx, followedItemIDsQuery, userID, profileID)
}

// ListFavoriteItemIDs returns the profile's favorited content ids.
func (r *CalendarRepository) ListFavoriteItemIDs(ctx context.Context, userID int, profileID string) ([]string, error) {
	return r.queryIDs(ctx, favoriteItemIDsQuery, userID, profileID)
}

// ListWatchlistItemIDs returns the profile's watchlisted content ids.
func (r *CalendarRepository) ListWatchlistItemIDs(ctx context.Context, userID int, profileID string) ([]string, error) {
	return r.queryIDs(ctx, watchlistItemIDsQuery, userID, profileID)
}

func (r *CalendarRepository) queryIDs(ctx context.Context, query string, userID int, profileID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, query, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("calendar personal query: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning personal id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListWatchedItemIDs returns the subset of contentIDs the profile has completed,
// as a set. An empty input returns an empty set without querying.
func (r *CalendarRepository) ListWatchedItemIDs(ctx context.Context, userID int, profileID string, contentIDs []string) (map[string]bool, error) {
	watched := make(map[string]bool, len(contentIDs))
	if len(contentIDs) == 0 {
		return watched, nil
	}
	rows, err := r.pool.Query(ctx, watchedItemIDsQuery, userID, profileID, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("calendar watched query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning watched id: %w", err)
		}
		watched[id] = true
	}
	return watched, rows.Err()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/catalog/ -run "TestFollowedItemIDsQuery|TestWatchedItemIDsQuery|TestFavoriteAndWatchlistQueries" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/catalog/calendar_personal.go internal/catalog/calendar_personal_test.go
git commit -m "feat(calendar): add per-profile followed/favorites/watchlist/watched resolvers"
```

---

## Task 3: Handler preset orchestration + watched overlay

**Files:**
- Modify: `internal/api/handlers/calendar.go`
- Test: `internal/api/handlers/calendar_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/api/handlers/calendar_test.go`. (Add the imports `"github.com/Silo-Server/silo-server/internal/recommendations"` and `"github.com/Silo-Server/silo-server/internal/sections"` to the existing import block.)

```go
type stubCalendarPersonal struct {
	followed       []string
	favorites      []string
	watchlist      []string
	watched        map[string]bool
	lastWatchedIDs []string
}

func (s *stubCalendarPersonal) ListFollowedItemIDs(_ context.Context, _ int, _ string) ([]string, error) {
	return s.followed, nil
}
func (s *stubCalendarPersonal) ListFavoriteItemIDs(_ context.Context, _ int, _ string) ([]string, error) {
	return s.favorites, nil
}
func (s *stubCalendarPersonal) ListWatchlistItemIDs(_ context.Context, _ int, _ string) ([]string, error) {
	return s.watchlist, nil
}
func (s *stubCalendarPersonal) ListWatchedItemIDs(_ context.Context, _ int, _ string, ids []string) (map[string]bool, error) {
	s.lastWatchedIDs = ids
	return s.watched, nil
}

type stubPopularSource struct{ items []recommendations.ScoredItem }

func (s *stubPopularSource) GetRecommendationCache(_ context.Context, _ int, _, _, _ string) ([]recommendations.ScoredItem, error) {
	return s.items, nil
}

type stubTrendingSource struct {
	snap sections.TrendingSnapshot
	ok   bool
}

func (s *stubTrendingSource) Get(_ context.Context, _, _ string) (sections.TrendingSnapshot, bool, error) {
	return s.snap, s.ok, nil
}

func TestHandleGetCalendar_FollowingPassesFollowedIDs(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo, personal: &stubCalendarPersonal{followed: []string{"s1", "s2"}}}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=following", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !repo.last.RestrictByIDs {
		t.Fatalf("expected RestrictByIDs true")
	}
	if got := repo.last.RestrictToIDs; len(got) != 2 || got[0] != "s1" || got[1] != "s2" {
		t.Fatalf("RestrictToIDs = %v, want [s1 s2]", got)
	}
}

func TestHandleGetCalendar_PopularReadsCache(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{
		repo:    repo,
		popular: &stubPopularSource{items: []recommendations.ScoredItem{{MediaItemID: "p1"}, {MediaItemID: "p2"}}},
	}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=popular", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if !repo.last.RestrictByIDs || len(repo.last.RestrictToIDs) != 2 || repo.last.RestrictToIDs[0] != "p1" {
		t.Fatalf("RestrictToIDs = %v, want [p1 p2]", repo.last.RestrictToIDs)
	}
}

func TestHandleGetCalendar_TrendingReadsSnapshot(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{
		repo:     repo,
		trending: &stubTrendingSource{ok: true, snap: sections.TrendingSnapshot{ContentIDs: []string{"t1"}}},
	}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=trending", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if !repo.last.RestrictByIDs || len(repo.last.RestrictToIDs) != 1 || repo.last.RestrictToIDs[0] != "t1" {
		t.Fatalf("RestrictToIDs = %v, want [t1]", repo.last.RestrictToIDs)
	}
}

func TestHandleGetCalendar_EverythingHasNoRestriction(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=everything", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if repo.last.RestrictByIDs {
		t.Fatalf("expected no restriction for everything")
	}
}

func TestHandleGetCalendar_RejectsUnknownFilter(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=bogus", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if repo.calls != 0 {
		t.Fatalf("expected repo not called, got %d", repo.calls)
	}
}

func TestHandleGetCalendar_EmptyFollowedShortCircuits(t *testing.T) {
	repo := &stubCalendarRepo{}
	handler := &CalendarHandler{repo: repo, personal: &stubCalendarPersonal{followed: nil}}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=following", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if repo.calls != 0 {
		t.Fatalf("expected ListEvents skipped for empty followed set, got %d calls", repo.calls)
	}
}

func TestHandleGetCalendar_MarksWatchedItems(t *testing.T) {
	repo := &stubCalendarRepo{events: []catalog.CalendarEvent{
		{ContentID: "ep-1", Type: "episode", Title: "Show", AirDate: time.Date(2026, time.April, 8, 0, 0, 0, 0, time.UTC)},
		{ContentID: "ep-2", Type: "episode", Title: "Show", AirDate: time.Date(2026, time.April, 8, 0, 0, 0, 0, time.UTC)},
	}}
	handler := &CalendarHandler{repo: repo, personal: &stubCalendarPersonal{watched: map[string]bool{"ep-1": true}}}
	req := httptest.NewRequest(http.MethodGet, "/calendar?start=2026-04-06&end=2026-04-12&filter=everything", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetCalendar(rec, req)

	var resp calendarResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	watched := map[string]bool{}
	for _, day := range resp.Events {
		for _, item := range day.Items {
			watched[item.ContentID] = item.Watched
		}
	}
	if !watched["ep-1"] || watched["ep-2"] {
		t.Fatalf("watched flags = %v, want ep-1 true / ep-2 false", watched)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/api/handlers/ -run TestHandleGetCalendar -v`
Expected: FAIL — `CalendarHandler` has no field `personal`/`popular`/`trending`; `calendarEventResponse` has no `Watched` (compile errors).

- [ ] **Step 3: Add interfaces, handler fields, and constructor**

In `internal/api/handlers/calendar.go`, add to the import block:

```go
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
```

Replace the `calendarRepository` interface / handler struct / constructor region with:

```go
type calendarRepository interface {
	ListEvents(ctx context.Context, f catalog.CalendarFilter) ([]catalog.CalendarEvent, error)
}

// calendarPersonalRepo resolves per-profile id-sets and watched status.
type calendarPersonalRepo interface {
	ListFollowedItemIDs(ctx context.Context, userID int, profileID string) ([]string, error)
	ListFavoriteItemIDs(ctx context.Context, userID int, profileID string) ([]string, error)
	ListWatchlistItemIDs(ctx context.Context, userID int, profileID string) ([]string, error)
	ListWatchedItemIDs(ctx context.Context, userID int, profileID string, contentIDs []string) (map[string]bool, error)
}

// calendarPopularSource reads the cached server-wide popular id-set.
type calendarPopularSource interface {
	GetRecommendationCache(ctx context.Context, userID int, profileID, recType, sourceItemID string) ([]recommendations.ScoredItem, error)
}

// calendarTrendingSource reads the external-trending snapshot.
type calendarTrendingSource interface {
	Get(ctx context.Context, source, window string) (sections.TrendingSnapshot, bool, error)
}

const (
	calendarFilterAll        = "all"
	calendarFilterEverything = "everything"
	calendarFilterFollowing  = "following"
	calendarFilterFavorites  = "favorites"
	calendarFilterWatchlist  = "watchlist"
	calendarFilterPopular    = "popular"
	calendarFilterTrending   = "trending"
)

// CalendarHandler handles the calendar endpoint.
type CalendarHandler struct {
	repo      calendarRepository
	detailSvc *catalog.DetailService
	personal  calendarPersonalRepo   // nil-tolerant (per-profile presets degrade to empty)
	popular   calendarPopularSource  // nil when recommendations disabled
	trending  calendarTrendingSource // nil when trending disabled
}

// NewCalendarHandler creates a new CalendarHandler. The repo doubles as the
// per-profile resolver since *catalog.CalendarRepository implements both.
func NewCalendarHandler(repo *catalog.CalendarRepository, detailSvc *catalog.DetailService, popular calendarPopularSource, trending calendarTrendingSource) *CalendarHandler {
	return &CalendarHandler{repo: repo, detailSvc: detailSvc, personal: repo, popular: popular, trending: trending}
}
```

- [ ] **Step 4: Add the `Watched` response field**

In the same file, add to `calendarEventResponse`:

```go
	PosterThumbhash string   `json:"poster_thumbhash,omitempty"`
	Watched         bool     `json:"watched"`
	Badges          []string `json:"badges"`
```

- [ ] **Step 5: Rewrite the filter handling in `HandleGetCalendar`**

Replace the block from `filter := q.Get("filter")` through the `events, err := h.repo.ListEvents(...)` / `groupEventsByDate(...)` call with:

```go
	filter := q.Get("filter")
	if filter == "" {
		filter = calendarFilterAll
	}
	switch filter {
	case calendarFilterAll, calendarFilterEverything, calendarFilterFollowing,
		calendarFilterFavorites, calendarFilterWatchlist, calendarFilterPopular, calendarFilterTrending:
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "invalid filter")
		return
	}

	af := requestAccessFilter(r)

	if (filter == calendarFilterFollowing || filter == calendarFilterFavorites || filter == calendarFilterWatchlist) && af.ProfileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "profile required for this filter")
		return
	}

	viewerLocation := catalog.CalendarLocation(q.Get("timezone"))

	restrict, ids, err := h.resolveCalendarRestriction(r.Context(), filter, af)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to resolve calendar filter")
		return
	}
	// A restricting preset with no ids can never match — skip the windowed query.
	if restrict && len(ids) == 0 {
		writeJSON(w, http.StatusOK, calendarResponse{Events: []calendarDayResponse{}})
		return
	}

	cf := catalog.CalendarFilter{
		Start:              start.AddDate(0, 0, -2),
		End:                end.AddDate(0, 0, 2),
		AllowedLibraryIDs:  af.AllowedLibraryIDs,
		DisabledLibraryIDs: af.DisabledLibraryIDs,
		MaxContentRating:   af.MaxContentRating,
		RestrictByIDs:      restrict,
		RestrictToIDs:      ids,
	}

	if v := q.Get("library_id"); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "library_id must be a positive integer")
			return
		}
		cf.LibraryID = &id
	}

	events, err := h.repo.ListEvents(r.Context(), cf)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to fetch calendar events")
		return
	}

	watched := h.resolveWatched(r.Context(), af, events)

	// Group events by date and build response.
	days := groupEventsByDate(events, r, h.detailSvc, start, end, viewerLocation, watched)
	writeJSON(w, http.StatusOK, calendarResponse{Events: days})
}

// resolveCalendarRestriction maps a preset to an id-set restriction. restrict=false
// means no restriction (everything/all). A nil/missing source degrades that preset
// to an empty id-set (which the caller renders as an empty calendar).
func (h *CalendarHandler) resolveCalendarRestriction(ctx context.Context, filter string, af catalog.AccessFilter) (restrict bool, ids []string, err error) {
	switch filter {
	case calendarFilterAll, calendarFilterEverything:
		return false, nil, nil
	case calendarFilterFollowing:
		if h.personal == nil {
			return true, nil, nil
		}
		ids, err = h.personal.ListFollowedItemIDs(ctx, af.UserID, af.ProfileID)
		return true, ids, err
	case calendarFilterFavorites:
		if h.personal == nil {
			return true, nil, nil
		}
		ids, err = h.personal.ListFavoriteItemIDs(ctx, af.UserID, af.ProfileID)
		return true, ids, err
	case calendarFilterWatchlist:
		if h.personal == nil {
			return true, nil, nil
		}
		ids, err = h.personal.ListWatchlistItemIDs(ctx, af.UserID, af.ProfileID)
		return true, ids, err
	case calendarFilterPopular:
		if h.popular == nil {
			return true, nil, nil
		}
		items, err := h.popular.GetRecommendationCache(ctx, recommendations.GlobalCacheUserID, recommendations.GlobalCacheProfileID, recommendations.RecTypePopular, "")
		if err != nil {
			return true, nil, err
		}
		ids = make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.MediaItemID)
		}
		return true, ids, nil
	case calendarFilterTrending:
		if h.trending == nil {
			return true, nil, nil
		}
		snap, ok, err := h.trending.Get(ctx, "tmdb", "week")
		if err != nil {
			return true, nil, err
		}
		if !ok {
			return true, nil, nil
		}
		return true, snap.ContentIDs, nil
	default:
		return false, nil, nil
	}
}

// resolveWatched decorates events with the profile's completed status. Best-effort:
// a lookup failure logs and returns no watched marks rather than failing the page.
func (h *CalendarHandler) resolveWatched(ctx context.Context, af catalog.AccessFilter, events []catalog.CalendarEvent) map[string]bool {
	if h.personal == nil || af.ProfileID == "" || len(events) == 0 {
		return map[string]bool{}
	}
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.ContentID)
	}
	watched, err := h.personal.ListWatchedItemIDs(ctx, af.UserID, af.ProfileID, ids)
	if err != nil {
		slog.WarnContext(ctx, "calendar watched overlay failed", "error", err)
		return map[string]bool{}
	}
	return watched
}
```

- [ ] **Step 6: Thread `watched` into `groupEventsByDate`**

Change the signature and the single response-construction line:

```go
func groupEventsByDate(events []catalog.CalendarEvent, r *http.Request, detailSvc *catalog.DetailService, start, end time.Time, viewerLocation *time.Location, watched map[string]bool) []calendarDayResponse {
```

In the `currentDay.Items = append(...)` literal, add the field:

```go
			PosterThumbhash: ev.PosterThumbhash,
			Watched:         watched[ev.ContentID],
			Badges:          badges,
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/api/handlers/ -run TestHandleGetCalendar -v`
Expected: PASS. The pre-existing `RejectsEndBeforeStart`, `RejectsRangesLongerThan31Days`, `ReturnsEmptyEvents` still pass (they use `&CalendarHandler{repo: repo}`; `personal`/`popular`/`trending` default nil; `all` needs none).

- [ ] **Step 8: Run the full handler package to catch additive-field assertions**

Run: `go test ./internal/api/handlers/`
Expected: PASS. If any pre-existing test compares full calendar JSON literally, update it to include the additive `"watched":false` on each item. If any test calls `groupEventsByDate` directly, pass a final `map[string]bool{}` argument.

- [ ] **Step 9: Commit**

```bash
git add internal/api/handlers/calendar.go internal/api/handlers/calendar_test.go
git commit -m "feat(calendar): resolve presets to id-sets and overlay watched status"
```

---

## Task 4: Wire popular + trending sources into the handler

**Files:**
- Modify: `internal/api/router.go`

- [ ] **Step 1: Update the calendar wiring block**

Replace the calendar registration (currently at `internal/api/router.go:1270`):

```go
				if calendarRepo != nil {
					calendarPopular := recommendations.NewRepo(deps.DB)
					calendarTrending := sections.NewTrendingSnapshotRepository(deps.DB)
					calendarHandler := handlers.NewCalendarHandler(calendarRepo, detailSvc, calendarPopular, calendarTrending)
					r.With(apimw.RequireProfile).Get("/calendar", calendarHandler.HandleGetCalendar)
				}
```

(`recommendations` and `sections` are already imported in this file.)

- [ ] **Step 2: Build the server**

Run: `go build ./...`
Expected: success, no errors.

- [ ] **Step 3: Run the affected packages**

Run: `go test ./internal/api/... ./internal/catalog/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/api/router.go
git commit -m "feat(calendar): wire popular and trending sources into calendar handler"
```

---

## Task 5: Add `watched` to the frontend event type

**Files:**
- Modify: `web/src/hooks/queries/calendar.ts`

- [ ] **Step 1: Add the field**

In the `CalendarEvent` interface, add after `poster_thumbhash`:

```ts
  poster_url?: string;
  poster_thumbhash?: string;
  watched?: boolean;
  badges: string[];
```

- [ ] **Step 2: Type-check**

Run: `cd web && pnpm run build`
Expected: success (build is the source of truth — `tsc --noEmit` misses errors `tsc -b` catches).

- [ ] **Step 3: Commit**

```bash
git add web/src/hooks/queries/calendar.ts
git commit -m "feat(calendar): add watched field to CalendarEvent type"
```

---

## Task 6: Preset selector, persistence, and empty-state nudge

**Files:**
- Modify: `web/src/pages/Calendar.tsx`
- Test: `web/src/pages/Calendar.test.tsx`

- [ ] **Step 1: Update the failing test first**

In `web/src/pages/Calendar.test.tsx`, change the "passes through the selected library" expectation (the no-`filter` case) from `"all"` to `"following"`:

```ts
  it("passes through the selected library", () => {
    renderCalendar("/calendar?week=2026-04-06&library=7");

    expect(mockUseCalendarWeek).toHaveBeenCalledWith("2026-04-06", {
      filter: "following",
      libraryId: 7,
    });
  });
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && pnpm exec vitest run src/pages/Calendar.test.tsx`
Expected: FAIL — currently resolves to `"all"`.

- [ ] **Step 3: Replace the preset model and helpers**

In `web/src/pages/Calendar.tsx`, replace the `CalendarFilter` type + `FILTER_OPTIONS` + `parseCalendarParams` region with:

```tsx
type CalendarFilter = "following" | "popular" | "trending" | "everything";

const PRESET_OPTIONS: { value: CalendarFilter; label: string }[] = [
  { value: "following", label: "Following" },
  { value: "popular", label: "Popular" },
  { value: "trending", label: "Trending" },
  { value: "everything", label: "All" },
];

const DEFAULT_PRESET: CalendarFilter = "following";
const PRESET_STORAGE_KEY = "calendar:preset";

// Accept the four presets plus legacy server values so old shared links keep working.
const KNOWN_FILTERS = new Set<string>([
  "following",
  "popular",
  "trending",
  "everything",
  "all",
  "favorites",
  "watchlist",
]);

/** Skeleton rows mirror a full week; enough slides per row to fill wide viewports. */
const CALENDAR_SKELETON_DAY_ROWS = 7;
const CALENDAR_SKELETON_ITEMS_PER_ROW = 18;

function readStoredPreset(): CalendarFilter {
  if (typeof window === "undefined") return DEFAULT_PRESET;
  const stored = window.localStorage.getItem(PRESET_STORAGE_KEY);
  return stored && PRESET_OPTIONS.some((o) => o.value === stored)
    ? (stored as CalendarFilter)
    : DEFAULT_PRESET;
}

function writeStoredPreset(value: string) {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(PRESET_STORAGE_KEY, value);
}

function parseCalendarParams(searchParams: URLSearchParams) {
  const weekRaw = searchParams.get("week");
  const weekStart =
    weekRaw && /^\d{4}-\d{2}-\d{2}$/.test(weekRaw) ? weekRaw : getWeekStart(new Date());
  const rawFilter = searchParams.get("filter");
  const filter = rawFilter && KNOWN_FILTERS.has(rawFilter) ? rawFilter : readStoredPreset();
  const libraryIdRaw = searchParams.get("library");
  const libraryId = libraryIdRaw ? Number(libraryIdRaw) : undefined;
  return { weekStart, filter, libraryId };
}
```

- [ ] **Step 4: Update `setFilter` to persist**

Replace the `setFilter` definition:

```tsx
  const setFilter = (f: string) => {
    writeStoredPreset(f);
    setParams({ filter: f === DEFAULT_PRESET ? undefined : f });
  };
```

- [ ] **Step 5: Replace the selector markup**

Swap the "Filter toggle" `<div role="group">…</div>` block for the responsive pills + dropdown (keep the existing library `<Select>` block immediately after it unchanged):

```tsx
            {/* Preset pills (desktop) */}
            <div
              role="group"
              aria-label="Calendar preset"
              className="surface-panel-subtle hidden items-center gap-0.5 rounded-full p-1 lg:flex"
            >
              {PRESET_OPTIONS.map((opt) => (
                <button
                  key={opt.value}
                  type="button"
                  aria-pressed={filter === opt.value}
                  onClick={() => setFilter(opt.value)}
                  className={`rounded-full px-3 py-1 text-[12px] font-semibold transition-all duration-150 sm:px-4 sm:py-1.5 sm:text-[13px] ${
                    filter === opt.value
                      ? "bg-primary text-primary-foreground shadow-sm"
                      : "text-muted-foreground hover:bg-surface-hover hover:text-foreground"
                  }`}
                >
                  {opt.label}
                </button>
              ))}
            </div>

            {/* Preset dropdown (smaller displays) */}
            <div className="lg:hidden">
              <Select value={filter} onValueChange={setFilter}>
                <SelectTrigger className="border-border/50 bg-surface/60 h-9 w-auto min-w-[130px] rounded-full text-[12px] font-semibold backdrop-blur-sm sm:text-[13px]">
                  <SelectValue placeholder="Following" />
                </SelectTrigger>
                <SelectContent>
                  {PRESET_OPTIONS.map((opt) => (
                    <SelectItem key={opt.value} value={opt.value}>
                      {opt.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
```

- [ ] **Step 6: Update the empty state to nudge between presets**

Replace the render-site usage:

```tsx
        <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
          <CalendarEmpty filter={filter} onSelectPreset={setFilter} />
        </div>
```

And replace the `CalendarEmpty` component:

```tsx
function CalendarEmpty({
  filter,
  onSelectPreset,
}: {
  filter: string;
  onSelectPreset: (f: string) => void;
}) {
  const isEverything = filter === "everything" || filter === "all";
  return (
    <div className="surface-panel flex min-h-[300px] flex-col items-center justify-center gap-3 rounded-[1.8rem] border-0 px-6 py-16 text-center">
      <CalendarDays className="text-muted-foreground h-10 w-10" strokeWidth={1.5} />
      <p className="text-muted-foreground text-sm">
        {filter === "following"
          ? "Nothing upcoming from shows you follow this week."
          : isEverything
            ? "Nothing scheduled this week."
            : "No events this week for this view."}
      </p>
      {!isEverything && (
        <div className="flex flex-wrap items-center justify-center gap-2">
          <Button
            variant="link"
            size="sm"
            className="text-primary text-sm"
            onClick={() => onSelectPreset("popular")}
          >
            Popular
          </Button>
          <Button
            variant="link"
            size="sm"
            className="text-primary text-sm"
            onClick={() => onSelectPreset("trending")}
          >
            Trending
          </Button>
          <Button
            variant="link"
            size="sm"
            className="text-primary text-sm"
            onClick={() => onSelectPreset("everything")}
          >
            Show everything
          </Button>
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 7: Run the test to verify it passes**

Run: `cd web && pnpm exec vitest run src/pages/Calendar.test.tsx`
Expected: PASS (both cases — `filter=watchlist` still passes through; no-filter now resolves to `following`).

- [ ] **Step 8: Build + lint**

Run: `cd web && pnpm run build && pnpm run lint`
Expected: build succeeds; lint passes for `Calendar.tsx` (ignore unrelated pre-existing `format:check` failures).

- [ ] **Step 9: Commit**

```bash
git add web/src/pages/Calendar.tsx web/src/pages/Calendar.test.tsx
git commit -m "feat(calendar): preset selector with responsive pills, persistence, empty-state nudge"
```

---

## Task 7: Watched overlay on the event card

**Files:**
- Modify: `web/src/components/calendar/CalendarEventCard.tsx`

- [ ] **Step 1: Add the watched treatment**

In `CalendarEventCard`, compute `watched` and apply it to the image wrapper + title, and add a check overlay. Replace the component body's relevant parts:

After `const thumbhashUrl = ...;` add:

```tsx
  const watched = event.watched === true;
```

Change the image wrapper `className` to dim when watched:

```tsx
        <div
          className={`media-card-image relative aspect-[2/3] ${watched ? "opacity-60 grayscale" : ""}`}
          style={
```

Add the check overlay immediately before the closing `</div>` of the `media-card-image` block (right after the badges block):

```tsx
          {watched && (
            <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
              <span
                className="bg-background/70 text-foreground flex h-8 w-8 items-center justify-center rounded-full text-base backdrop-blur-sm"
                aria-label="Watched"
              >
                ✓
              </span>
            </div>
          )}
```

Dim the title when watched:

```tsx
        <div
          className={`truncate text-[14px] font-semibold tracking-tight ${watched ? "text-muted-foreground" : ""}`}
        >
          {event.title}
        </div>
```

- [ ] **Step 2: Build + lint**

Run: `cd web && pnpm run build && pnpm run lint`
Expected: build succeeds; lint passes for `CalendarEventCard.tsx`.

- [ ] **Step 3: Commit**

```bash
git add web/src/components/calendar/CalendarEventCard.tsx
git commit -m "feat(calendar): dim and check-mark already-watched event cards"
```

---

## Final verification

- [ ] **Backend:** `go build ./... && go test ./internal/catalog/ ./internal/api/...` → PASS
- [ ] **Frontend:** `cd web && pnpm run build && pnpm exec vitest run src/pages/Calendar.test.tsx && pnpm run lint` → PASS
- [ ] **Manual smoke (optional, via dev server):** load `/calendar` → defaults to **Following**; switch presets (pills on desktop, dropdown when narrow); reload → remembers last preset; an empty **Following** week shows the Popular/Trending/Everything nudge; watched episodes render dimmed with a ✓.

---

## Notes & deviations from spec

- **Preset persistence is browser-level** (`localStorage` key `calendar:preset`), not keyed by profile id, to avoid depending on a profile-id hook. Per-profile keying (or a server-side preference) is a clean follow-up once the current-profile hook is confirmed.
- **Favorites/watchlist** remain valid `filter` values (resolved via the same id-set path) for backward-compatible client links, even though the web UI no longer surfaces them as separate presets — **Following** subsumes them.
- **Watched overlay** is best-effort: a lookup error logs (`slog.WarnContext`) and renders no watched marks rather than failing the calendar.
- **Trending** reads the canonical `tmdb` / `week` snapshot. Unioning sources/windows is deferred (YAGNI).
