# ABS Phase 1 Close-out Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Close out Phase 1 of ABS by landing the four remaining surfaces in one combined push: listening stats, author/series detail, continue-listening toggles, RSS feeds.

**Tech Stack:** Go, `chi/v5`, `pgx/v5`, `oklog/ulid/v2`, in-package test fakes.

**Commands assume `/opt/silo-server` as cwd. Pre-existing unrelated working-tree modifications (Dockerfile, cmd/silo/main.go, docker-compose.yml, internal/api/router.go, internal/audiobooks/abs/me_handler.go, internal/audiobooks/abs/progress.go, internal/audiobooks/media_store.go, internal/auth/session.go, internal/config/config.go, internal/config/db_loader.go) and untracked files must NOT be staged in any commit.**

**Source spec:** `docs/superpowers/specs/2026-05-26-abs-phase1-closeout-design.md`. Re-read the relevant §4-§8 section before each task.

**Predecessor plans:** bookmarks / collections-playlists / smart-collections. Conventions (TDD ordering, dispatch helper, anti-enumeration 404, profile-scoped + cross-user-public) carry over.

---

## File map

**Create:**
- `migrations/154_user_watch_progress_hide_from_continue.up.sql` + `.down.sql`
- `migrations/155_abs_rss_feeds.up.sql` + `.down.sql`
- `internal/audiobooks/abs/listening_stats_handler.go` + `_test.go`
- `internal/audiobooks/abs/author_series_handler.go` + `_test.go`
- `internal/audiobooks/abs/continue_listening_handler.go` + `_test.go`
- `internal/audiobooks/abs/rss_feeds.go` — RSSFeedStore interface + RSSFeed model + serialiser
- `internal/audiobooks/abs/rss_feeds_handler.go` + `_test.go`
- `internal/audiobooks/abs_rss_feed_store.go` — pgx-backed RSSFeedStore

**Modify:**
- `internal/audiobooks/abs/handler.go` — `Stats` / `Author` / `Series` types + `RSSFeedStore` field + route registration (auth group + public group)
- `internal/audiobooks/abs/progress.go` — `ProgressStore.SetHideFromContinue` + `AggregateStats` + `ListClosedSessions` methods on interfaces
- `internal/audiobooks/abs_progress_store.go` — implement `SetHideFromContinue`
- `internal/audiobooks/abs_playback_session_store.go` — implement `AggregateStats` + `ListClosedSessions`
- `internal/audiobooks/media_store.go` — implement `GetAuthorByID` + `GetSeriesByName`; update `ListContinueListening` SQL with `AND uwp.hide_from_continue = false`
- `internal/audiobooks/service.go` — construct `&ABSRSSFeedStore{Pool: ...}` and pass through

---

## Task 1: Migrations 154 + 155

**Files:**
- Create: `migrations/154_user_watch_progress_hide_from_continue.up.sql` + `.down.sql`
- Create: `migrations/155_abs_rss_feeds.up.sql` + `.down.sql`

- [ ] **Step 1: Write migration 154**

`migrations/154_user_watch_progress_hide_from_continue.up.sql`:

```sql
-- Toggle for hiding an in-progress book from the Continue Listening
-- shelf without affecting the progress row itself. Used by the
-- ABS-compat /me/progress/{itemId}/remove-from-continue-listening +
-- /readd-to-continue-listening endpoints.

ALTER TABLE public.user_watch_progress
    ADD COLUMN IF NOT EXISTS hide_from_continue boolean NOT NULL DEFAULT false;
```

`migrations/154_user_watch_progress_hide_from_continue.down.sql`:

```sql
ALTER TABLE public.user_watch_progress
    DROP COLUMN IF EXISTS hide_from_continue;
```

- [ ] **Step 2: Write migration 155**

`migrations/155_abs_rss_feeds.up.sql`:

```sql
-- Audiobookshelf-style RSS podcast feeds. Each row exposes one
-- audiobook (library_item_id) as a public RSS XML feed reachable at
-- /feed/{slug}.xml — slug is the unguessable capability token.
-- closed_at is NULL while the feed is active; closing soft-deletes
-- so re-opening creates a new row with a new slug.

CREATE TABLE IF NOT EXISTS public.abs_rss_feeds (
    id              text PRIMARY KEY,
    user_id         integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id      uuid,
    library_item_id text NOT NULL REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    slug            text NOT NULL,
    minified        boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    closed_at       timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS abs_rss_feeds_slug_uniq
    ON public.abs_rss_feeds (slug);

CREATE INDEX IF NOT EXISTS abs_rss_feeds_user_profile_idx
    ON public.abs_rss_feeds (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
    );
```

`migrations/155_abs_rss_feeds.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_rss_feeds_user_profile_idx;
DROP INDEX IF EXISTS public.abs_rss_feeds_slug_uniq;
DROP TABLE IF EXISTS public.abs_rss_feeds;
```

- [ ] **Step 3: Apply locally + verify**

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/154_user_watch_progress_hide_from_continue.up.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/155_abs_rss_feeds.up.sql
docker compose exec -T postgres psql -U silo -d silo -c "\d user_watch_progress" | grep hide_from_continue
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_rss_feeds"
```

Roll-back + re-up to verify the down migrations parse:

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/155_abs_rss_feeds.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/154_user_watch_progress_hide_from_continue.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/154_user_watch_progress_hide_from_continue.up.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/155_abs_rss_feeds.up.sql
```

- [ ] **Step 4: Commit**

```bash
git add migrations/154_user_watch_progress_hide_from_continue.up.sql migrations/154_user_watch_progress_hide_from_continue.down.sql \
        migrations/155_abs_rss_feeds.up.sql migrations/155_abs_rss_feeds.down.sql
git commit -m "$(cat <<'EOF'
feat(audiobooks): migrations 154 + 155 for Phase 1 close-out

154 adds hide_from_continue to user_watch_progress (backs the
ABS remove/readd-to-continue-listening endpoints). 155 creates
abs_rss_feeds for the upcoming RSS surface.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Listening stats — store methods + handlers + tests

**Files:**
- Modify: `internal/audiobooks/abs/progress.go` (extend `ABSPlaybackSessionStore` interface)
- Modify: `internal/audiobooks/abs_playback_session_store.go`
- Create: `internal/audiobooks/abs/listening_stats_handler.go`
- Create: `internal/audiobooks/abs/listening_stats_handler_test.go`

- [ ] **Step 1: Extend interface**

In `internal/audiobooks/abs/progress.go`, add `Stats` + `DayStat` + `MonthStat` types AND add the two new methods to the `ABSPlaybackSessionStore` interface (between `ClosePlaybackSession` and the closing brace):

```go
// Stats is the aggregated /me/listening-stats response shape.
type Stats struct {
	TotalTime int        // seconds
	Items     int        // distinct content_ids listened to
	Days      []DayStat  // recent days (most-recent first)
	DayOfWeek [7]int     // index 0 = Sunday
	Monthly   []MonthStat
}

type DayStat   struct{ Date    string; Seconds int }  // "2026-05-26"
type MonthStat struct{ Month   string; Seconds int }  // "2026-05"
```

Add to the `ABSPlaybackSessionStore` interface:

```go
	// AggregateStats returns aggregated listening stats for (user, profile).
	AggregateStats(ctx context.Context, userID, profileID string) (Stats, error)
	// ListClosedSessions returns paginated closed sessions for (user, profile)
	// ordered by started_at DESC. Returns (rows, totalRowCount, error).
	ListClosedSessions(ctx context.Context, userID, profileID string, limit, offset int) ([]ABSPlaybackSession, int, error)
```

- [ ] **Step 2: Implement on the concrete store**

Append to `internal/audiobooks/abs_playback_session_store.go`:

```go
// AggregateStats returns the aggregated /me/listening-stats payload.
// Closed AND open sessions both contribute; we sum the entire user's
// historical listening time.
func (s *ABSPlaybackSessionStore) AggregateStats(ctx context.Context, userID, profileID string) (abs.Stats, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: invalid user id %q: %w", userID, err)
	}
	out := abs.Stats{Days: []abs.DayStat{}, Monthly: []abs.MonthStat{}}

	// Totals: sum(time_listening_seconds) + distinct items.
	row := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(time_listening_seconds), 0), COUNT(DISTINCT content_id)
		FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2`,
		uid, profileID,
	)
	if err := row.Scan(&out.TotalTime, &out.Items); err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats totals: %w", err)
	}

	// Per-day (last 30 days).
	rows, err := s.Pool.Query(ctx, `
		SELECT TO_CHAR(date_trunc('day', started_at), 'YYYY-MM-DD'),
		       COALESCE(SUM(time_listening_seconds), 0)
		FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2
		  AND started_at >= now() - INTERVAL '30 days'
		GROUP BY 1
		ORDER BY 1 DESC`,
		uid, profileID,
	)
	if err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats days: %w", err)
	}
	for rows.Next() {
		var d abs.DayStat
		if scanErr := rows.Scan(&d.Date, &d.Seconds); scanErr != nil {
			rows.Close()
			return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats days scan: %w", scanErr)
		}
		out.Days = append(out.Days, d)
	}
	rows.Close()

	// Day-of-week (postgres EXTRACT(DOW): 0=Sunday).
	dowRows, err := s.Pool.Query(ctx, `
		SELECT EXTRACT(DOW FROM started_at)::int,
		       COALESCE(SUM(time_listening_seconds), 0)
		FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2
		GROUP BY 1`,
		uid, profileID,
	)
	if err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats dow: %w", err)
	}
	for dowRows.Next() {
		var dow, secs int
		if scanErr := dowRows.Scan(&dow, &secs); scanErr != nil {
			dowRows.Close()
			return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats dow scan: %w", scanErr)
		}
		if dow >= 0 && dow < 7 {
			out.DayOfWeek[dow] = secs
		}
	}
	dowRows.Close()

	// Per-month (last 12 months).
	mRows, err := s.Pool.Query(ctx, `
		SELECT TO_CHAR(date_trunc('month', started_at), 'YYYY-MM'),
		       COALESCE(SUM(time_listening_seconds), 0)
		FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2
		  AND started_at >= now() - INTERVAL '12 months'
		GROUP BY 1
		ORDER BY 1 DESC`,
		uid, profileID,
	)
	if err != nil {
		return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats months: %w", err)
	}
	for mRows.Next() {
		var m abs.MonthStat
		if scanErr := mRows.Scan(&m.Month, &m.Seconds); scanErr != nil {
			mRows.Close()
			return abs.Stats{}, fmt.Errorf("abs_playback_session_store: stats months scan: %w", scanErr)
		}
		out.Monthly = append(out.Monthly, m)
	}
	mRows.Close()
	return out, nil
}

// ListClosedSessions returns paginated closed sessions for (user, profile).
func (s *ABSPlaybackSessionStore) ListClosedSessions(ctx context.Context, userID, profileID string, limit, offset int) ([]abs.ABSPlaybackSession, int, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_playback_session_store: invalid user id %q: %w", userID, err)
	}
	if limit <= 0 || limit > 200 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2 AND closed_at IS NOT NULL`,
		uid, profileID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("abs_playback_session_store: list closed count: %w", err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, content_id,
		       time_listening_seconds, current_position_seconds, closed_at
		FROM abs_playback_sessions
		WHERE user_id = $1 AND profile_id = $2 AND closed_at IS NOT NULL
		ORDER BY started_at DESC
		LIMIT $3 OFFSET $4`,
		uid, profileID, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_playback_session_store: list closed: %w", err)
	}
	defer rows.Close()
	out := make([]abs.ABSPlaybackSession, 0, limit)
	for rows.Next() {
		var sess abs.ABSPlaybackSession
		var scanUID int
		var scanProfile string
		var closedAt *time.Time
		if err := rows.Scan(&sess.ID, &scanUID, &scanProfile, &sess.ContentID, &sess.TimeListeningSeconds, &sess.CurrentPositionSeconds, &closedAt); err != nil {
			return nil, 0, fmt.Errorf("abs_playback_session_store: list closed scan: %w", err)
		}
		sess.UserID = strconv.Itoa(scanUID)
		sess.ProfileID = scanProfile
		sess.ClosedAt = closedAt
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("abs_playback_session_store: list closed rows: %w", err)
	}
	return out, total, nil
}
```

- [ ] **Step 3: Extend the existing `fakePlaybackSessionStore` test fake**

In `internal/audiobooks/abs/file_handler_public_track_test.go` (where `fakePlaybackSessionStore` is defined), append two new methods to the existing fake type:

```go
func (f *fakePlaybackSessionStore) AggregateStats(_ context.Context, userID, profileID string) (Stats, error) {
	return Stats{Days: []DayStat{}, Monthly: []MonthStat{}}, nil
}

func (f *fakePlaybackSessionStore) ListClosedSessions(_ context.Context, userID, profileID string, limit, offset int) ([]ABSPlaybackSession, int, error) {
	return nil, 0, nil
}
```

This keeps the existing tests passing while we add stats-specific tests against a richer fake in step 4.

- [ ] **Step 4: Write failing tests + handler**

Create `internal/audiobooks/abs/listening_stats_handler_test.go`:

```go
package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// statsFakeStore is a richer in-memory fake than fakePlaybackSessionStore;
// supports seeded stats + closed-session lists.
type statsFakeStore struct {
	fakePlaybackSessionStore
	stats   Stats
	closed  []ABSPlaybackSession
}

func (f *statsFakeStore) AggregateStats(_ context.Context, _, _ string) (Stats, error) {
	return f.stats, nil
}

func (f *statsFakeStore) ListClosedSessions(_ context.Context, _, _ string, limit, offset int) ([]ABSPlaybackSession, int, error) {
	total := len(f.closed)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return f.closed[offset:end], total, nil
}

func TestStats_Aggregate_Ok(t *testing.T) {
	fake := &statsFakeStore{
		stats: Stats{TotalTime: 3600, Items: 4, DayOfWeek: [7]int{0, 1800, 0, 1800, 0, 0, 0}},
	}
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-stats", nil, nil, "1", "", h.handleListeningStats)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["totalTime"] != float64(3600) {
		t.Errorf("totalTime = %v, want 3600", got["totalTime"])
	}
	if got["items"] != float64(4) {
		t.Errorf("items = %v, want 4", got["items"])
	}
	dow, _ := got["dayOfWeek"].(map[string]any)
	if dow["1"] != float64(1800) {
		t.Errorf("dayOfWeek[1] = %v, want 1800", dow["1"])
	}
}

func TestStats_Sessions_List_Paginated(t *testing.T) {
	fake := &statsFakeStore{closed: []ABSPlaybackSession{
		{ID: "s1", UserID: "1", ContentID: "book-1"},
		{ID: "s2", UserID: "1", ContentID: "book-2"},
		{ID: "s3", UserID: "1", ContentID: "book-3"},
	}}
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-sessions", nil, nil, "1", "", h.handleListeningSessions)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env["total"] != float64(3) {
		t.Errorf("total = %v, want 3", env["total"])
	}
	results, _ := env["results"].([]any)
	if len(results) != 3 {
		t.Errorf("results len = %d, want 3", len(results))
	}
}

func TestStats_Session_Detail_Owner(t *testing.T) {
	fake := &statsFakeStore{}
	// seed via the fake's embedded fake's session map.
	_ = fake.InsertPlaybackSession(context.Background(), ABSPlaybackSession{ID: "s1", UserID: "1", ContentID: "book-1"})
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-sessions/s1", map[string]string{"sid": "s1"}, nil, "1", "", h.handleListeningSessionDetail)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["id"] != "s1" {
		t.Errorf("id = %v", got["id"])
	}
}

func TestStats_Session_Detail_NonOwner_404(t *testing.T) {
	fake := &statsFakeStore{}
	_ = fake.InsertPlaybackSession(context.Background(), ABSPlaybackSession{ID: "s1", UserID: "1", ContentID: "book-1"})
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-sessions/s1", map[string]string{"sid": "s1"}, nil, "2", "", h.handleListeningSessionDetail)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
```

Create `internal/audiobooks/abs/listening_stats_handler.go`:

```go
package abs

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// handleListeningStats — GET /me/listening-stats.
func (h *Handler) handleListeningStats(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaybackSessionStore == nil {
		writeJSON(w, http.StatusOK, statsToABS(Stats{}))
		return
	}
	stats, err := h.deps.PlaybackSessionStore.AggregateStats(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs listening stats failed", "err", err, "user", a.UserID)
		http.Error(w, "stats unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, statsToABS(stats))
}

// handleListeningSessions — GET /me/listening-sessions?limit=&page=.
func (h *Handler) handleListeningSessions(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaybackSessionStore == nil {
		writeJSON(w, http.StatusOK, pagedEnvelope([]any{}, 0, 30, 0, "started_at", true, "", false, ""))
		return
	}
	limit, page := readPagedQuery(r, 30)
	sessions, total, err := h.deps.PlaybackSessionStore.ListClosedSessions(r.Context(), a.UserID, a.ProfileID, limit, page*limit)
	if err != nil {
		slog.Error("abs listening sessions failed", "err", err, "user", a.UserID)
		http.Error(w, "sessions unavailable", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, sessionToABS(s))
	}
	writeJSON(w, http.StatusOK, pagedEnvelope(out, total, limit, page, "started_at", true, "", false, ""))
}

// handleListeningSessionDetail — GET /me/listening-sessions/{sid}.
func (h *Handler) handleListeningSessionDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.PlaybackSessionStore == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	sid := chi.URLParam(r, "sid")
	sess, err := h.deps.PlaybackSessionStore.GetPlaybackSession(r.Context(), sid)
	if err != nil || sess.UserID != a.UserID {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, sessionToABS(sess))
}

// statsToABS shapes a Stats aggregate for the wire.
func statsToABS(s Stats) map[string]any {
	dow := map[string]int{}
	for i, sec := range s.DayOfWeek {
		dow[strconv.Itoa(i)] = sec
	}
	days := make([]map[string]any, 0, len(s.Days))
	for _, d := range s.Days {
		days = append(days, map[string]any{"date": d.Date, "seconds": d.Seconds})
	}
	monthly := make([]map[string]any, 0, len(s.Monthly))
	for _, m := range s.Monthly {
		monthly = append(monthly, map[string]any{"month": m.Month, "seconds": m.Seconds})
	}
	return map[string]any{
		"totalTime": s.TotalTime,
		"items":     s.Items,
		"days":      days,
		"dayOfWeek": dow,
		"monthly":   monthly,
	}
}

func sessionToABS(s ABSPlaybackSession) map[string]any {
	out := map[string]any{
		"id":            s.ID,
		"libraryItemId": s.ContentID,
		"userId":        s.UserID,
		"timeListening": s.TimeListeningSeconds,
		"currentTime":   s.CurrentPositionSeconds,
	}
	if s.ClosedAt != nil {
		out["closedAt"] = s.ClosedAt.UnixMilli()
	}
	return out
}
```

- [ ] **Step 5: Run + commit**

```bash
go build ./...
go test ./internal/audiobooks/abs/ -count=1 -run 'TestStats_' -v
go test ./internal/audiobooks/abs/ -count=1 | tail -5
git add internal/audiobooks/abs/progress.go internal/audiobooks/abs_playback_session_store.go \
        internal/audiobooks/abs/file_handler_public_track_test.go \
        internal/audiobooks/abs/listening_stats_handler.go internal/audiobooks/abs/listening_stats_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): listening stats endpoints + store aggregates

Three handlers (/me/listening-stats, /me/listening-sessions,
/me/listening-sessions/{sid}). ABSPlaybackSessionStore gains
AggregateStats (totals + day/dayOfWeek/monthly buckets) and
ListClosedSessions (paginated history). Detail handler enforces
owner-scope via 404 on mismatch (anti-enumeration).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Author + Series detail — store extensions + handlers + tests

**Files:**
- Modify: `internal/audiobooks/abs/handler.go` (add `Author` + `Series` types + extend `MediaStore` interface)
- Modify: `internal/audiobooks/media_store.go` (implement the two new methods)
- Create: `internal/audiobooks/abs/author_series_handler.go`
- Create: `internal/audiobooks/abs/author_series_handler_test.go`

- [ ] **Step 1: Extend MediaStore interface + add types**

In `internal/audiobooks/abs/handler.go`, near the existing `AuthorSummary` / `SeriesSummary` types, ADD:

```go
// Author is the detail-shape author with embedded books list.
type Author struct {
	ID         string
	Name       string
	PosterPath string  // resolved via CoverResolver on emit
	Books      []*models.MediaItem
}

// Series is the detail-shape series with books ordered by series_index.
type Series struct {
	ID    string  // lowercased series_name
	Name  string  // canonical series_name (from first matching row)
	Books []*models.MediaItem
}
```

In the `MediaStore` interface, ADD (between `ListLibrarySeries` and the closing brace):

```go
	// GetAuthorByID returns the author with the given people.id plus
	// their audiobook list, sorted by title. Returns ErrNotFound when
	// no people row matches.
	GetAuthorByID(ctx context.Context, authorID string) (Author, error)
	// GetSeriesByName returns the canonical series (case-insensitive
	// match on audiobook_series.series_name) with its books ordered
	// by series_index ASC (NULLS LAST), title fallback. Returns
	// ErrNotFound when no rows match.
	GetSeriesByName(ctx context.Context, seriesName string) (Series, error)
```

- [ ] **Step 2: Implement on `ABSMediaStore`**

Append to `internal/audiobooks/media_store.go`:

```go
// GetAuthorByID looks up the author by people.id and returns the row
// plus their audiobooks.
func (s *ABSMediaStore) GetAuthorByID(ctx context.Context, authorID string) (abs.Author, error) {
	if s.Pool == nil {
		return abs.Author{}, abs.ErrNotFound
	}
	id, err := strconv.Atoi(authorID)
	if err != nil {
		return abs.Author{}, abs.ErrNotFound
	}
	var name string
	var poster *string
	row := s.Pool.QueryRow(ctx, `SELECT name, poster_path FROM people WHERE id = $1`, id)
	if err := row.Scan(&name, &poster); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.Author{}, abs.ErrNotFound
		}
		return abs.Author{}, fmt.Errorf("abs_media_store: get author: %w", err)
	}
	author := abs.Author{ID: authorID, Name: name}
	if poster != nil {
		author.PosterPath = *poster
	}
	// Hydrate books.
	rows, err := s.Pool.Query(ctx, `
		SELECT mi.content_id, mi.title
		FROM item_people ip
		JOIN media_items mi ON mi.content_id = ip.content_id
		WHERE ip.person_id = $1 AND ip.kind = 7 AND mi.type = 'audiobook'
		ORDER BY LOWER(mi.title)`,
		id,
	)
	if err != nil {
		return abs.Author{}, fmt.Errorf("abs_media_store: get author books: %w", err)
	}
	defer rows.Close()
	author.Books = make([]*models.MediaItem, 0)
	for rows.Next() {
		mi := &models.MediaItem{}
		if err := rows.Scan(&mi.ContentID, &mi.Title); err != nil {
			return abs.Author{}, fmt.Errorf("abs_media_store: get author books scan: %w", err)
		}
		author.Books = append(author.Books, mi)
	}
	return author, nil
}

// GetSeriesByName looks up a series case-insensitively, plus its books.
func (s *ABSMediaStore) GetSeriesByName(ctx context.Context, seriesName string) (abs.Series, error) {
	if s.Pool == nil {
		return abs.Series{}, abs.ErrNotFound
	}
	var canonicalName string
	row := s.Pool.QueryRow(ctx, `
		SELECT series_name FROM audiobook_series
		WHERE LOWER(series_name) = LOWER($1)
		LIMIT 1`, seriesName,
	)
	if err := row.Scan(&canonicalName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.Series{}, abs.ErrNotFound
		}
		return abs.Series{}, fmt.Errorf("abs_media_store: get series: %w", err)
	}
	series := abs.Series{ID: strings.ToLower(canonicalName), Name: canonicalName}
	rows, err := s.Pool.Query(ctx, `
		SELECT mi.content_id, mi.title
		FROM audiobook_series asx
		JOIN media_items mi ON mi.content_id = asx.content_id
		WHERE LOWER(asx.series_name) = LOWER($1) AND mi.type = 'audiobook'
		ORDER BY asx.series_index NULLS LAST, LOWER(mi.title)`,
		seriesName,
	)
	if err != nil {
		return abs.Series{}, fmt.Errorf("abs_media_store: get series books: %w", err)
	}
	defer rows.Close()
	series.Books = make([]*models.MediaItem, 0)
	for rows.Next() {
		mi := &models.MediaItem{}
		if err := rows.Scan(&mi.ContentID, &mi.Title); err != nil {
			return abs.Series{}, fmt.Errorf("abs_media_store: get series books scan: %w", err)
		}
		series.Books = append(series.Books, mi)
	}
	return series, nil
}
```

If the imports at the top of `media_store.go` don't already include `errors`, `strings`, and `github.com/jackc/pgx/v5`, add them.

- [ ] **Step 3: Implement the two GetAuthor/Series methods on `noopMediaStore` + `stubMediaStore`**

In `internal/audiobooks/abs/login_refresh_test.go`, append to `noopMediaStore`:

```go
func (noopMediaStore) GetAuthorByID(context.Context, string) (Author, error)        { return Author{}, ErrNotFound }
func (noopMediaStore) GetSeriesByName(context.Context, string) (Series, error)      { return Series{}, ErrNotFound }
```

In `internal/audiobooks/abs/bookmarks_handler_test.go`, append to `stubMediaStore`:

```go
func (s *stubMediaStore) GetAuthorByID(_ context.Context, id string) (Author, error) {
	return Author{}, ErrNotFound
}
func (s *stubMediaStore) GetSeriesByName(_ context.Context, name string) (Series, error) {
	return Series{}, ErrNotFound
}
```

And in `internal/audiobooks/abs/smart_collections_handler_test.go` `itemListStubMediaStore` does not need overrides — embedded `stubMediaStore.GetAuthorByID` already covers it.

Verify build:

```bash
go build ./...
```

- [ ] **Step 4: Failing tests + handler**

Create `internal/audiobooks/abs/author_series_handler_test.go`:

```go
package abs

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type authorSeriesStubMediaStore struct {
	noopMediaStore
	author Author
	series Series
}

func (s *authorSeriesStubMediaStore) GetAuthorByID(_ context.Context, id string) (Author, error) {
	if id != s.author.ID {
		return Author{}, ErrNotFound
	}
	return s.author, nil
}

func (s *authorSeriesStubMediaStore) GetSeriesByName(_ context.Context, name string) (Series, error) {
	if name != s.series.ID && name != s.series.Name {
		return Series{}, ErrNotFound
	}
	return s.series, nil
}

func TestAuthor_Detail_ReturnsBooks(t *testing.T) {
	media := &authorSeriesStubMediaStore{
		author: Author{ID: "42", Name: "Brandon Sanderson", Books: []*models.MediaItem{
			{ContentID: "book-1", Title: "Mistborn"},
			{ContentID: "book-2", Title: "Stormlight"},
		}},
	}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/authors/42", map[string]string{"id": "42"}, nil, "1", "", h.handleAuthorDetail)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "Brandon Sanderson" {
		t.Errorf("name = %v", got["name"])
	}
	books, _ := got["books"].([]any)
	if len(books) != 2 {
		t.Errorf("books len = %d, want 2", len(books))
	}
}

func TestAuthor_Detail_Unknown_404(t *testing.T) {
	media := &authorSeriesStubMediaStore{author: Author{ID: "42"}}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/authors/99", map[string]string{"id": "99"}, nil, "1", "", h.handleAuthorDetail)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSeries_Detail_ReturnsBooks(t *testing.T) {
	media := &authorSeriesStubMediaStore{
		series: Series{ID: "mistborn", Name: "Mistborn", Books: []*models.MediaItem{
			{ContentID: "b1", Title: "Final Empire"},
			{ContentID: "b2", Title: "Well of Ascension"},
		}},
	}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/series/mistborn", map[string]string{"id": "mistborn"}, nil, "1", "", h.handleSeriesDetail)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "Mistborn" {
		t.Errorf("name = %v", got["name"])
	}
	books, _ := got["books"].([]any)
	if len(books) != 2 {
		t.Errorf("books len = %d, want 2", len(books))
	}
}

func TestSeries_Detail_Unknown_404(t *testing.T) {
	media := &authorSeriesStubMediaStore{series: Series{ID: "mistborn", Name: "Mistborn"}}
	h := New(Dependencies{MediaStore: media})

	rec := dispatchABSWithParams(http.MethodGet, "/api/series/unknown", map[string]string{"id": "unknown"}, nil, "1", "", h.handleSeriesDetail)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
```

Create `internal/audiobooks/abs/author_series_handler.go`:

```go
package abs

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
)

// handleAuthorDetail — GET /authors/{id}.
func (h *Handler) handleAuthorDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "id")
	author, err := h.deps.MediaStore.GetAuthorByID(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "author not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs author detail failed", "err", err, "id", id)
		http.Error(w, "author get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, authorToABS(author))
}

// handleSeriesDetail — GET /series/{id}.
// {id} is the URL-encoded series name. Matching is case-insensitive.
func (h *Handler) handleSeriesDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	idRaw := chi.URLParam(r, "id")
	id, err := url.PathUnescape(idRaw)
	if err != nil {
		id = idRaw
	}
	series, err := h.deps.MediaStore.GetSeriesByName(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "series not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs series detail failed", "err", err, "id", id)
		http.Error(w, "series get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, seriesToABS(series))
}

func authorToABS(a Author) map[string]any {
	books := make([]map[string]any, 0, len(a.Books))
	for _, b := range a.Books {
		books = append(books, map[string]any{"id": b.ContentID, "media": map[string]any{"metadata": map[string]any{"title": b.Title}}})
	}
	return map[string]any{
		"id":         a.ID,
		"name":       a.Name,
		"numBooks":   len(a.Books),
		"books":      books,
	}
}

func seriesToABS(s Series) map[string]any {
	books := make([]map[string]any, 0, len(s.Books))
	for _, b := range s.Books {
		books = append(books, map[string]any{"id": b.ContentID, "media": map[string]any{"metadata": map[string]any{"title": b.Title}}})
	}
	return map[string]any{
		"id":       s.ID,
		"name":     s.Name,
		"numBooks": len(s.Books),
		"books":    books,
	}
}
```

NOTE: `authorSeriesStubMediaStore` references `context` — add `"context"` to that test file's import block.

- [ ] **Step 5: Run + commit**

```bash
go build ./...
go test ./internal/audiobooks/abs/ -count=1 -run 'TestAuthor_|TestSeries_' -v
go test ./internal/audiobooks/abs/ -count=1 | tail -5
git add internal/audiobooks/abs/handler.go internal/audiobooks/media_store.go \
        internal/audiobooks/abs/login_refresh_test.go internal/audiobooks/abs/bookmarks_handler_test.go \
        internal/audiobooks/abs/author_series_handler.go internal/audiobooks/abs/author_series_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): author + series detail endpoints

GET /authors/{id} (people.id, kind=7 join over item_people) and
GET /series/{id} (case-insensitive series_name match, ordered by
series_index NULLS LAST). Both return entity + embedded books[].
Existing /authors/{id}/image route (unauth) wired in Task 7's
route-registration step.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Continue-listening toggles

**Files:**
- Modify: `internal/audiobooks/abs/progress.go` (add `SetHideFromContinue` to `ProgressStore` interface)
- Modify: `internal/audiobooks/abs_progress_store.go` (implement)
- Modify: `internal/audiobooks/media_store.go` (`ListContinueListening` SQL gains `AND uwp.hide_from_continue = false`)
- Create: `internal/audiobooks/abs/continue_listening_handler.go`
- Create: `internal/audiobooks/abs/continue_listening_handler_test.go`

- [ ] **Step 1: Extend `ProgressStore` interface**

In `internal/audiobooks/abs/progress.go`, add to the `ProgressStore` interface:

```go
	// SetHideFromContinue toggles the hide_from_continue flag on a
	// progress row. Idempotent — succeeds even when no row matches
	// (no progress yet means nothing to hide; readd-to-continue
	// becomes a no-op on rows that don't exist).
	SetHideFromContinue(ctx context.Context, userID, profileID, contentID string, hide bool) error
```

- [ ] **Step 2: Implement on `ABSProgressStore`**

Append to `internal/audiobooks/abs_progress_store.go`:

```go
// SetHideFromContinue sets the hide_from_continue flag for the given
// progress row. Idempotent on missing-row.
func (s *ABSProgressStore) SetHideFromContinue(ctx context.Context, userID, profileID, contentID string, hide bool) error {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return fmt.Errorf("abs_progress_store: invalid user id %q: %w", userID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		UPDATE user_watch_progress
		SET hide_from_continue = $4
		WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`,
		uid, profileID, contentID, hide,
	); err != nil {
		return fmt.Errorf("abs_progress_store: set hide_from_continue: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Update `MediaStore.ListContinueListening` SQL to filter**

In `internal/audiobooks/media_store.go`, locate the `ListContinueListening` function. Find its SQL — it should be a JOIN with `user_watch_progress` (aliased `uwp`). Add this clause to the WHERE:

```sql
AND uwp.hide_from_continue = false
```

The existing SQL probably has a filter like `WHERE uwp.user_id = $1 AND uwp.is_finished = false ...`. Append the new AND condition. The exact text varies; the implementer must locate the existing query and add the filter without disturbing other clauses.

- [ ] **Step 4: Extend `fakeProgressStore` test fake**

In `internal/audiobooks/abs/play_resume_test.go`, append to `fakeProgressStore`:

```go
func (f *fakeProgressStore) SetHideFromContinue(_ context.Context, _, _, _ string, _ bool) error {
	return nil
}
```

- [ ] **Step 5: Write tests + handler**

Create `internal/audiobooks/abs/continue_listening_handler_test.go`:

```go
package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// recordingProgressFake captures SetHideFromContinue calls.
type recordingProgressFake struct {
	fakeProgressStore
	mu     sync.Mutex
	last   string  // "hide:<contentID>" or "show:<contentID>"
}

func (f *recordingProgressFake) SetHideFromContinue(_ context.Context, userID, profileID, contentID string, hide bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if hide {
		f.last = "hide:" + contentID
	} else {
		f.last = "show:" + contentID
	}
	return nil
}

func TestContinue_Remove_SetsHide(t *testing.T) {
	prog := &recordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{"book-1": nil}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/progress/book-1/remove-from-continue-listening",
		map[string]string{"itemId": "book-1"}, nil, "1", "", h.handleRemoveFromContinueListening)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["ok"] != true {
		t.Errorf("ok = %v", got["ok"])
	}
	if prog.last != "hide:book-1" {
		t.Errorf("last = %q, want hide:book-1", prog.last)
	}
}

func TestContinue_Readd_SetsShow(t *testing.T) {
	prog := &recordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{"book-1": nil}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/progress/book-1/readd-to-continue-listening",
		map[string]string{"itemId": "book-1"}, nil, "1", "", h.handleReaddToContinueListening)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if prog.last != "show:book-1" {
		t.Errorf("last = %q, want show:book-1", prog.last)
	}
}

func TestContinue_UnknownItem_404(t *testing.T) {
	prog := &recordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/progress/ghost/remove-from-continue-listening",
		map[string]string{"itemId": "ghost"}, nil, "1", "", h.handleRemoveFromContinueListening)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
```

Create `internal/audiobooks/abs/continue_listening_handler.go`:

```go
package abs

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// handleRemoveFromContinueListening — GET /me/progress/{itemId}/remove-from-continue-listening.
func (h *Handler) handleRemoveFromContinueListening(w http.ResponseWriter, r *http.Request) {
	h.setHideFromContinue(w, r, true)
}

// handleReaddToContinueListening — GET /me/progress/{itemId}/readd-to-continue-listening.
func (h *Handler) handleReaddToContinueListening(w http.ResponseWriter, r *http.Request) {
	h.setHideFromContinue(w, r, false)
}

func (h *Handler) setHideFromContinue(w http.ResponseWriter, r *http.Request, hide bool) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	itemID := chi.URLParam(r, "itemId")
	if itemID == "" {
		http.Error(w, "itemId required", http.StatusBadRequest)
		return
	}
	// Validate the item exists.
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), itemID)
	if err != nil || item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}
	if h.deps.ProgressStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if err := h.deps.ProgressStore.SetHideFromContinue(r.Context(), a.UserID, a.ProfileID, itemID, hide); err != nil {
		slog.Error("abs continue toggle failed", "err", err, "user", a.UserID, "item", itemID, "hide", hide)
		http.Error(w, "continue toggle failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

- [ ] **Step 6: Run + commit**

```bash
go build ./...
go test ./internal/audiobooks/abs/ -count=1 -run 'TestContinue_' -v
go test ./internal/audiobooks/abs/ -count=1 | tail -5
git add internal/audiobooks/abs/progress.go internal/audiobooks/abs_progress_store.go internal/audiobooks/media_store.go \
        internal/audiobooks/abs/play_resume_test.go \
        internal/audiobooks/abs/continue_listening_handler.go internal/audiobooks/abs/continue_listening_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): continue-listening toggles

Two GET endpoints (remove-from / readd-to-continue-listening) backed
by ProgressStore.SetHideFromContinue. ListContinueListening SQL gains
an AND uwp.hide_from_continue = false filter so hidden items drop off
the Continue Listening shelf immediately. Item validation via
MediaStore (404 on unknown).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: RSS feeds — store + auth handlers

**Files:**
- Create: `internal/audiobooks/abs/rss_feeds.go` — interface + model + serialiser
- Create: `internal/audiobooks/abs/rss_feeds_handler.go` — auth handlers
- Create: `internal/audiobooks/abs/rss_feeds_handler_test.go`
- Create: `internal/audiobooks/abs_rss_feed_store.go` — pgx-backed store
- Modify: `internal/audiobooks/abs/handler.go` — add `RSSFeedStore` field on `Dependencies`

- [ ] **Step 1: Define interface + types**

Create `internal/audiobooks/abs/rss_feeds.go`:

```go
package abs

import (
	"context"
	"time"
)

// RSSFeedStore is the storage contract for the abs_rss_feeds table.
type RSSFeedStore interface {
	ListUserFeeds(ctx context.Context, userID, profileID string) ([]RSSFeed, error)
	GetFeed(ctx context.Context, id string) (RSSFeed, error)
	GetFeedBySlug(ctx context.Context, slug string) (RSSFeed, error)
	CreateFeed(ctx context.Context, f RSSFeed) error
	CloseFeed(ctx context.Context, id string) error
}

// RSSFeed mirrors an abs_rss_feeds row.
type RSSFeed struct {
	ID            string
	UserID        string
	ProfileID     string
	LibraryItemID string
	Slug          string
	Minified      bool
	CreatedAt     time.Time
	ClosedAt      *time.Time
}

// rssFeedToABS shapes a feed in the ABS wire format. `url` is built
// from the supplied base URL + slug.
func rssFeedToABS(f RSSFeed, baseURL string) map[string]any {
	url := baseURL + "/feed/" + f.Slug + ".xml"
	return map[string]any{
		"id":            f.ID,
		"userId":        f.UserID,
		"libraryItemId": f.LibraryItemID,
		"slug":          f.Slug,
		"minified":      f.Minified,
		"createdAt":     f.CreatedAt.UnixMilli(),
		"url":           url,
	}
}
```

Add to `Dependencies` struct in `handler.go`, after `SmartCollectionStore`:

```go
	// RSSFeedStore persists abs_rss_feeds rows (migration 155).
	// May be nil; handlers respond 503 when unset.
	RSSFeedStore RSSFeedStore
```

- [ ] **Step 2: Implement pgx-backed store**

Create `internal/audiobooks/abs_rss_feed_store.go`:

```go
package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

type ABSRSSFeedStore struct {
	Pool *pgxpool.Pool
}

var _ abs.RSSFeedStore = (*ABSRSSFeedStore)(nil)

func (s *ABSRSSFeedStore) ListUserFeeds(ctx context.Context, userID, profileID string) ([]abs.RSSFeed, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_rss_feed_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, library_item_id, slug, minified, created_at, closed_at
		FROM abs_rss_feeds
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		  AND closed_at IS NULL
		ORDER BY created_at DESC`,
		uid, profileArg(profileID),
	)
	if err != nil {
		return nil, fmt.Errorf("abs_rss_feed_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.RSSFeed, 0)
	for rows.Next() {
		var f abs.RSSFeed
		var uidScan int
		var profileScan *string
		if err := rows.Scan(&f.ID, &uidScan, &profileScan, &f.LibraryItemID, &f.Slug, &f.Minified, &f.CreatedAt, &f.ClosedAt); err != nil {
			return nil, fmt.Errorf("abs_rss_feed_store: list scan: %w", err)
		}
		f.UserID = strconv.Itoa(uidScan)
		if profileScan != nil {
			f.ProfileID = *profileScan
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_rss_feed_store: list rows: %w", err)
	}
	return out, nil
}

func (s *ABSRSSFeedStore) GetFeed(ctx context.Context, id string) (abs.RSSFeed, error) {
	return s.getFeed(ctx, "id = $1", id)
}

func (s *ABSRSSFeedStore) GetFeedBySlug(ctx context.Context, slug string) (abs.RSSFeed, error) {
	return s.getFeed(ctx, "slug = $1 AND closed_at IS NULL", slug)
}

func (s *ABSRSSFeedStore) getFeed(ctx context.Context, where string, arg string) (abs.RSSFeed, error) {
	var f abs.RSSFeed
	var uidScan int
	var profileScan *string
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, library_item_id, slug, minified, created_at, closed_at
		FROM abs_rss_feeds WHERE `+where, arg)
	if err := row.Scan(&f.ID, &uidScan, &profileScan, &f.LibraryItemID, &f.Slug, &f.Minified, &f.CreatedAt, &f.ClosedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.RSSFeed{}, abs.ErrNotFound
		}
		return abs.RSSFeed{}, fmt.Errorf("abs_rss_feed_store: get: %w", err)
	}
	f.UserID = strconv.Itoa(uidScan)
	if profileScan != nil {
		f.ProfileID = *profileScan
	}
	return f, nil
}

func (s *ABSRSSFeedStore) CreateFeed(ctx context.Context, f abs.RSSFeed) error {
	uid, err := strconv.Atoi(f.UserID)
	if err != nil {
		return fmt.Errorf("abs_rss_feed_store: invalid user id %q: %w", f.UserID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO abs_rss_feeds (id, user_id, profile_id, library_item_id, slug, minified)
		VALUES ($1, $2, $3::uuid, $4, $5, $6)`,
		f.ID, uid, profileArg(f.ProfileID), f.LibraryItemID, f.Slug, f.Minified,
	); err != nil {
		return fmt.Errorf("abs_rss_feed_store: create: %w", err)
	}
	return nil
}

func (s *ABSRSSFeedStore) CloseFeed(ctx context.Context, id string) error {
	if _, err := s.Pool.Exec(ctx, `UPDATE abs_rss_feeds SET closed_at = now() WHERE id = $1 AND closed_at IS NULL`, id); err != nil {
		return fmt.Errorf("abs_rss_feed_store: close: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Write the auth handlers + tests**

Create `internal/audiobooks/abs/rss_feeds_handler.go`:

```go
package abs

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

var slugRe = regexp.MustCompile(`^[a-z0-9-]{4,64}$`)

type feedOpenBody struct {
	Slug     string `json:"slug"`
	Minified bool   `json:"minified"`
}

// handleListRSSFeeds — GET /api/feeds.
func (h *Handler) handleListRSSFeeds(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.RSSFeedStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"feeds": []any{}})
		return
	}
	rows, err := h.deps.RSSFeedStore.ListUserFeeds(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs feed list failed", "err", err, "user", a.UserID)
		http.Error(w, "feed list failed", http.StatusInternalServerError)
		return
	}
	base := h.absBaseURL(r)
	out := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		out = append(out, rssFeedToABS(f, base))
	}
	writeJSON(w, http.StatusOK, map[string]any{"feeds": out})
}

// handleOpenItemFeed — POST /api/feeds/item/{itemId}/open.
func (h *Handler) handleOpenItemFeed(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.RSSFeedStore == nil {
		http.Error(w, "feed store unavailable", http.StatusServiceUnavailable)
		return
	}
	itemID := chi.URLParam(r, "itemId")
	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), itemID)
	if err != nil || item == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	var body feedOpenBody
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) // body is optional

	slug := strings.ToLower(strings.TrimSpace(body.Slug))
	if slug == "" {
		slug = randomSlug()
	} else if !slugRe.MatchString(slug) {
		http.Error(w, "invalid slug", http.StatusBadRequest)
		return
	}

	f := RSSFeed{
		ID:            ulid.Make().String(),
		UserID:        a.UserID,
		ProfileID:     a.ProfileID,
		LibraryItemID: itemID,
		Slug:          slug,
		Minified:      body.Minified,
	}
	if err := h.deps.RSSFeedStore.CreateFeed(r.Context(), f); err != nil {
		// Slug collision: pg's UNIQUE constraint surfaces as a 23505
		// duplicate-key error. Crude detection via substring.
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique") {
			http.Error(w, "slug taken", http.StatusConflict)
			return
		}
		slog.Error("abs feed create failed", "err", err, "user", a.UserID)
		http.Error(w, "feed persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.RSSFeedStore.GetFeed(r.Context(), f.ID)
	if errors.Is(err, ErrNotFound) || err != nil {
		f.CreatedAt = time.Now()
		persisted = f
	}
	writeJSON(w, http.StatusOK, rssFeedToABS(persisted, h.absBaseURL(r)))
}

// handleCloseFeed — POST /api/feeds/{id}/close.
func (h *Handler) handleCloseFeed(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.RSSFeedStore == nil {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	id := chi.URLParam(r, "id")
	f, err := h.deps.RSSFeedStore.GetFeed(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && f.UserID != a.UserID) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs feed get-for-close failed", "err", err, "id", id)
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.RSSFeedStore.CloseFeed(r.Context(), id); err != nil {
		slog.Error("abs feed close failed", "err", err, "id", id)
		http.Error(w, "feed close failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// randomSlug returns a 16-character URL-safe slug.
func randomSlug() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf)
}
```

Create `internal/audiobooks/abs/rss_feeds_handler_test.go`:

```go
package abs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type memRSSFeedStore struct {
	mu   sync.Mutex
	rows map[string]RSSFeed
}

func newMemRSSFeedStore() *memRSSFeedStore { return &memRSSFeedStore{rows: map[string]RSSFeed{}} }

func (m *memRSSFeedStore) ListUserFeeds(_ context.Context, userID, profileID string) ([]RSSFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RSSFeed, 0)
	for _, f := range m.rows {
		if f.UserID == userID && f.ProfileID == profileID && f.ClosedAt == nil {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memRSSFeedStore) GetFeed(_ context.Context, id string) (RSSFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.rows[id]
	if !ok {
		return RSSFeed{}, ErrNotFound
	}
	return f, nil
}

func (m *memRSSFeedStore) GetFeedBySlug(_ context.Context, slug string) (RSSFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.rows {
		if f.Slug == slug && f.ClosedAt == nil {
			return f, nil
		}
	}
	return RSSFeed{}, ErrNotFound
}

func (m *memRSSFeedStore) CreateFeed(_ context.Context, f RSSFeed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.rows {
		if existing.Slug == f.Slug && existing.ClosedAt == nil {
			return errors.New("unique violation duplicate key")
		}
	}
	f.CreatedAt = time.Now()
	m.rows[f.ID] = f
	return nil
}

func (m *memRSSFeedStore) CloseFeed(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.rows[id]
	if !ok {
		return nil
	}
	now := time.Now()
	f.ClosedAt = &now
	m.rows[id] = f
	return nil
}

func newFeedsHarness(t *testing.T, knownItems ...string) (*Handler, *memRSSFeedStore) {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = nil
	}
	store := newMemRSSFeedStore()
	h := New(Dependencies{MediaStore: &stubMediaStore{known: known}, RSSFeedStore: store})
	return h, store
}

func TestFeed_Open_GeneratesSlug(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	slug, _ := got["slug"].(string)
	if len(slug) != 16 {
		t.Errorf("slug = %q (len %d), want 16-char auto-generated", slug, len(slug))
	}
}

func TestFeed_Open_CustomSlug(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{"slug":"my-cool-feed"}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["slug"] != "my-cool-feed" {
		t.Errorf("slug = %v, want my-cool-feed", got["slug"])
	}
}

func TestFeed_Open_InvalidSlug_400(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{"slug":"BAD!"}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestFeed_Open_Collision_409(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	body := []byte(`{"slug":"taken-slug"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, body, "1", "", h.handleOpenItemFeed)
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, body, "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestFeed_Open_UnknownItem_404(t *testing.T) {
	h, _ := newFeedsHarness(t)
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/ghost/open",
		map[string]string{"itemId": "ghost"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestFeed_List_OwnerOnly(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	_ = dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	rec := dispatchABSWithParams(http.MethodGet, "/api/feeds", nil, nil, "2", "", h.handleListRSSFeeds)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	feeds, _ := env["feeds"].([]any)
	if len(feeds) != 0 {
		t.Errorf("user 2 sees %d feeds, want 0", len(feeds))
	}
}

func TestFeed_Close_Owner(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	openRec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	var open map[string]any
	_ = json.Unmarshal(openRec.Body.Bytes(), &open)
	id, _ := open["id"].(string)

	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/"+id+"/close", map[string]string{"id": id}, nil, "1", "", h.handleCloseFeed)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestFeed_Close_NonOwner_404(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	openRec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	var open map[string]any
	_ = json.Unmarshal(openRec.Body.Bytes(), &open)
	id, _ := open["id"].(string)

	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/"+id+"/close", map[string]string{"id": id}, nil, "2", "", h.handleCloseFeed)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 4: Run + commit**

```bash
go build ./...
go test ./internal/audiobooks/abs/ -count=1 -run 'TestFeed_' -v
go test ./internal/audiobooks/abs/ -count=1 | tail -5
git add internal/audiobooks/abs/handler.go internal/audiobooks/abs/rss_feeds.go internal/audiobooks/abs/rss_feeds_handler.go internal/audiobooks/abs/rss_feeds_handler_test.go internal/audiobooks/abs_rss_feed_store.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): RSS feeds — auth handlers + pgx store

POST /api/feeds/item/{itemId}/open (auto-gen or custom slug),
GET /api/feeds (owner+profile scope, only open feeds),
POST /api/feeds/{id}/close (idempotent, owner-gated).
Slug validation via ^[a-z0-9-]{4,64}$. 409 on collision via pgx
unique-violation substring detection (crude but adequate).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: RSS feeds — public XML route + file route

**Files:**
- Modify: `internal/audiobooks/abs/rss_feeds_handler.go` (add public handlers)
- Modify: `internal/audiobooks/abs/rss_feeds_handler_test.go` (add tests)

- [ ] **Step 1: Append public handlers**

Append to `internal/audiobooks/abs/rss_feeds_handler.go`:

```go

// handlePublicFeed — GET /feed/{slug}.xml and GET /feed/{slug}.
// Public, no auth. The slug is the capability token.
func (h *Handler) handlePublicFeed(w http.ResponseWriter, r *http.Request) {
	if h.deps.RSSFeedStore == nil {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	slug := strings.TrimSuffix(chi.URLParam(r, "slug"), ".xml")
	f, err := h.deps.RSSFeedStore.GetFeedBySlug(r.Context(), slug)
	if errors.Is(err, ErrNotFound) || (err == nil && f.ClosedAt != nil) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs public feed get failed", "err", err, "slug", slug)
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return
	}

	item, err := h.deps.MediaStore.GetAudiobookByID(r.Context(), f.LibraryItemID)
	if err != nil || item == nil {
		http.Error(w, "feed item not found", http.StatusNotFound)
		return
	}
	files, _ := h.deps.MediaStore.GetMediaFiles(r.Context(), f.LibraryItemID)

	base := h.absBaseURL(r)
	xml := renderFeedXML(f, item, files, base)
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml))
}

// renderFeedXML builds a minimal RSS 2.0 + iTunes document for the
// given feed. One <item> per media file with an absolute enclosure
// URL pointing at /feed/{slug}/file/{ino}.
func renderFeedXML(f RSSFeed, item *models.MediaItem, files []*models.MediaFile, baseURL string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">` + "\n")
	b.WriteString("<channel>\n")
	b.WriteString("<title>" + xmlEscape(item.Title) + "</title>\n")
	b.WriteString("<link>" + xmlEscape(baseURL+"/feed/"+f.Slug+".xml") + "</link>\n")
	b.WriteString("<description>silo audiobook feed</description>\n")
	for i, mf := range files {
		_ = i
		enc := baseURL + "/feed/" + f.Slug + "/file/" + strconv.Itoa(mf.ID)
		b.WriteString("<item>\n")
		b.WriteString("<title>" + xmlEscape(item.Title) + "</title>\n")
		b.WriteString(`<enclosure url="` + xmlEscape(enc) + `" type="audio/mpeg" length="0"/>` + "\n")
		b.WriteString("<guid>" + xmlEscape(f.Slug+"-"+strconv.Itoa(mf.ID)) + "</guid>\n")
		b.WriteString("</item>\n")
	}
	b.WriteString("</channel>\n")
	b.WriteString("</rss>\n")
	return b.String()
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// handlePublicFeedFile — GET /feed/{slug}/file/{ino}. Streams the
// media file when the slug is valid + open and the ino belongs to the
// underlying library item.
func (h *Handler) handlePublicFeedFile(w http.ResponseWriter, r *http.Request) {
	if h.deps.RSSFeedStore == nil {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	slug := chi.URLParam(r, "slug")
	f, err := h.deps.RSSFeedStore.GetFeedBySlug(r.Context(), slug)
	if errors.Is(err, ErrNotFound) || (err == nil && f.ClosedAt != nil) {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "feed get failed", http.StatusInternalServerError)
		return
	}
	// Re-use the existing /public/session-style file handler. We can't
	// directly call handlePublicTrack (it requires a session) — for
	// the RSS file route we serve the file by ino lookup.
	inoStr := chi.URLParam(r, "ino")
	ino, parseErr := strconv.Atoi(inoStr)
	if parseErr != nil {
		http.Error(w, "invalid ino", http.StatusBadRequest)
		return
	}
	mf, mfErr := h.deps.MediaStore.GetMediaFileByID(r.Context(), ino)
	if mfErr != nil || mf == nil || mf.ContentID != f.LibraryItemID {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, mf.FilePath)
}
```

Need to add `"strconv"` to the imports of `rss_feeds_handler.go` (likely already present from the `strings.TrimSuffix` and earlier code — verify). Also add `"github.com/Silo-Server/silo-server/internal/models"`.

- [ ] **Step 2: Append public tests**

Append to `internal/audiobooks/abs/rss_feeds_handler_test.go`:

```go

func TestPublicFeed_UnknownSlug_404(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodGet, "/feed/missing.xml", map[string]string{"slug": "missing.xml"}, nil, "", "", h.handlePublicFeed)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPublicFeed_HappyPath_XML(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	openRec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{"slug":"happy-feed"}`), "1", "", h.handleOpenItemFeed)
	if openRec.Code != http.StatusOK {
		t.Fatalf("seed open failed: %s", openRec.Body.String())
	}

	rec := dispatchABSWithParams(http.MethodGet, "/feed/happy-feed.xml",
		map[string]string{"slug": "happy-feed.xml"}, nil, "", "", h.handlePublicFeed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/rss+xml") {
		t.Errorf("Content-Type = %q, want application/rss+xml", ct)
	}
	body := rec.Body.String()
	for _, needle := range []string{"<rss", "<channel>", "<title>"} {
		if !strings.Contains(body, needle) {
			t.Errorf("body missing %q; got %s", needle, body)
		}
	}
}
```

Add `"strings"` to this test file's imports.

- [ ] **Step 3: Run + commit**

```bash
go build ./...
go test ./internal/audiobooks/abs/ -count=1 -run 'TestFeed_|TestPublicFeed_' -v
go test ./internal/audiobooks/abs/ -count=1 | tail -5
git add internal/audiobooks/abs/rss_feeds_handler.go internal/audiobooks/abs/rss_feeds_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): public RSS XML route + per-file stream

GET /feed/{slug}.xml (and /feed/{slug}) generates a minimal RSS 2.0
document with one <item> per media_file. GET /feed/{slug}/file/{ino}
streams the underlying file when ino belongs to the feed's
library_item_id. Both routes are unauthenticated — slug is the
capability token; closed feeds 404.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Wire stores + register routes + verify

**Files:**
- Modify: `internal/audiobooks/service.go` (construct `&ABSRSSFeedStore{...}` and pass through)
- Modify: `internal/audiobooks/abs/handler.go` (register 10 new routes)

- [ ] **Step 1: Wire RSSFeedStore in `BuildABSHandler`**

In `internal/audiobooks/service.go`, after the smartCollectionStore block, add:

```go
	var rssFeedStore abs.RSSFeedStore
	if deps.Pool != nil {
		rssFeedStore = &ABSRSSFeedStore{Pool: deps.Pool}
	}
```

In the `abs.New(abs.Dependencies{...})` call, after `SmartCollectionStore: smartCollectionStore,`, add:

```go
		RSSFeedStore: rssFeedStore,
```

- [ ] **Step 2: Wire the existing author-image route**

The `handleAuthorImage` route already exists (mounted unauthenticated in handler.go) and currently 404s. Wire it: open `internal/audiobooks/abs/me_handler.go` or wherever `handleAuthorImage` lives (it's pre-existing) and confirm it now uses `MediaStore.GetAuthorByID` + `CoverResolver` to redirect. If it doesn't exist as a stub, write it. Specifically:

```go
// In items_handler.go or me_handler.go, find handleAuthorImage:
func (h *Handler) handleAuthorImage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	author, err := h.deps.MediaStore.GetAuthorByID(r.Context(), id)
	if err != nil || author.PosterPath == "" {
		http.Error(w, "author image not found", http.StatusNotFound)
		return
	}
	if h.deps.CoverResolver == nil {
		http.Error(w, "image resolver not configured", http.StatusServiceUnavailable)
		return
	}
	url := h.deps.CoverResolver(r.Context(), author.PosterPath, "")
	if url == "" {
		http.Error(w, "image resolution failed", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}
```

If `handleAuthorImage` was a placeholder, replace its body with the above. If it doesn't exist at all, append this to `author_series_handler.go`.

- [ ] **Step 3: Register all the new routes in `mountRoutes`**

In `internal/audiobooks/abs/handler.go`, inside the Stage 4 bearerAuth `for _, prefix := range []string{"/abs/api", "/api"} {` loop, after the smart-collections routes (added in sub-project 3), append:

```go
			// Phase 1 close-out: listening stats / author+series detail / continue-listening / RSS feeds (auth).
			r.Get(prefix+"/me/listening-stats", h.handleListeningStats)
			r.Get(prefix+"/me/listening-sessions", h.handleListeningSessions)
			r.Get(prefix+"/me/listening-sessions/{sid}", h.handleListeningSessionDetail)
			r.Get(prefix+"/authors/{id}", h.handleAuthorDetail)
			r.Get(prefix+"/series/{id}", h.handleSeriesDetail)
			r.Get(prefix+"/me/progress/{itemId}/remove-from-continue-listening", h.handleRemoveFromContinueListening)
			r.Get(prefix+"/me/progress/{itemId}/readd-to-continue-listening", h.handleReaddToContinueListening)
			r.Get(prefix+"/feeds", h.handleListRSSFeeds)
			r.Post(prefix+"/feeds/item/{itemId}/open", h.handleOpenItemFeed)
			r.Post(prefix+"/feeds/{id}/close", h.handleCloseFeed)
```

OUTSIDE the bearerAuth group (in the unauth block where `/public/session/.../track/{idx}` is mounted), register the public RSS routes:

```go
	// Public RSS feed routes — slug is the capability token, no auth.
	r.Get("/feed/{slug}.xml", h.handlePublicFeed)
	r.Get("/feed/{slug}", h.handlePublicFeed)
	r.Get("/feed/{slug}/file/{ino}", h.handlePublicFeedFile)
```

- [ ] **Step 4: Build + full test**

```bash
go build ./...
go test ./... 2>&1 | grep -E '^FAIL' | head -5
go test ./internal/audiobooks/abs/ -count=1 | tail -5
```

If any test failures attributable to this sub-project (e.g., the new interface methods being missing on a fake), fix them.

- [ ] **Step 5: Migration roundtrip**

```bash
docker compose exec -T postgres psql -U silo -d silo -c "\d user_watch_progress" | grep hide_from_continue
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_rss_feeds"
```

- [ ] **Step 6: Frontend build + verify-local-paths**

```bash
cd /opt/silo-server/web && pnpm run build 2>&1 | tail -5 ; cd ..
make verify-local-paths 2>&1 | tail -5
```

- [ ] **Step 7: Commit**

```bash
git add internal/audiobooks/abs/handler.go internal/audiobooks/service.go
# only stage any other modified files that were touched in steps 2/3 above
git commit -m "$(cat <<'EOF'
feat(audiobooks): wire Phase 1 close-out stores + mount routes

Wires RSSFeedStore into BuildABSHandler. Mounts the ten new
authenticated routes (stats x3 + author x1 + series x1 + continue x2
+ feeds-auth x3) under both /abs/api and /api inside bearerAuth.
Mounts the three public RSS routes (.xml + slug + /file/{ino}) in
the unauth public block. handleAuthorImage wired to CoverResolver.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Out of scope (per spec §10)

- Series + collection RSS feeds, RSS cover route.
- Listening stats time-range filters.
- Author/series edit surface.
- Stats charts beyond the three buckets.
