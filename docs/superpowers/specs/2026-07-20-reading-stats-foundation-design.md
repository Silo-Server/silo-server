# Reading Stats: Foundation + History

Date: 2026-07-20
Status: approved (approach review)

Third reader sub-project (after navigation and appearance; branch stacks on
`feat/reader-appearance`). This spec covers the session engine and history
visuals. The motivation layer (streaks, goals, achievements, Reading DNA) is
a separate follow-up spec that aggregates this data and adds no tracking.

## Problem

Silo records only reading position. There is no notion of time spent
reading, so it cannot show reading history, estimate time remaining (the
navigation footer reserved a slot for exactly this), or support any future
goals/achievements layer.

## Design

### Session tracking (heartbeats)

- While the prose reader is genuinely active — document visible AND user
  interaction (relocate, pointer, key) within the last 60 seconds — the
  client posts a heartbeat every 30 seconds:
  `POST /api/v1/ebooks/{content_id}/reading-heartbeat` with
  `{ "fraction": <current 0..1> }`. Fire-and-forget; a lost heartbeat loses
  at most 30 seconds of credit. First heartbeat fires immediately on
  activity, not after the first interval. Comics excluded (this is the
  prose reader; the comic reader has no session tracking in this spec).
- Server coalesces heartbeats into sessions (`reading_sessions` table). A
  heartbeat within 120 seconds of the profile+book's open session extends
  it: `duration_seconds += min(elapsed_since_last, 90)`, `end_fraction`
  and `last_heartbeat_at` updated. A larger gap (or no open session)
  starts a new row with `duration_seconds = 0` and
  `start_fraction = end_fraction = fraction`. Server clock only; client
  timestamps are ignored.
- Table: `reading_sessions(id identity PK, user_id int FK cascade,
  profile_id text, content_id text, started_at timestamptz,
  last_heartbeat_at timestamptz, duration_seconds int, start_fraction
  real, end_fraction real)`, indexes on `(user_id, profile_id,
  last_heartbeat_at)` and `(user_id, profile_id, content_id)`.

### Pace and time remaining

- Pace = fractions-per-second over recent sessions:
  `sum(end_fraction - start_fraction) / sum(duration_seconds)` over the
  profile's sessions for THIS book in the last 14 days (min 10 minutes of
  data); fallback: same formula across all the profile's books (last 14
  days, min 30 minutes); else no estimate.
- `GET /api/v1/ebooks/{content_id}/reading-stats` →
  `{ "pace_fraction_per_hour": number|null, "time_left_seconds": number|null,
     "book_seconds": number }` where time-left uses the profile's current
  progress fraction for that book. The navigation footer shows the
  "· 2h 10m left" segment when `time_left_seconds` is non-null; absent
  otherwise (no layout shift — the slot was designed for this).
- Footer fetches once on book open and refreshes at most every 5 minutes;
  no per-heartbeat recomputation.

### History API and stats page

- `GET /api/v1/ebooks/reading-stats?from=YYYY-MM-DD&to=YYYY-MM-DD` →
  `{ "totals": { "today_seconds", "week_seconds", "month_seconds",
     "all_time_seconds" },
     "days": [ { "date", "seconds" } … ],           // rollup for range
     "books": [ { "content_id", "title", "seconds", "last_read_at" } … ],
     "sessions": [ { "content_id", "title", "started_at",
                     "duration_seconds", "start_fraction",
                     "end_fraction" } … ] }          // most recent 50
  Rollups computed by query (`date_trunc('day', started_at)` grouped) —
  no aggregate tables. Day boundaries use UTC (the server has no timezone
  setting; all handler timestamps are already `time.Now().UTC()`).
- New per-profile page "Reading stats" at `/reading-stats` (RequireProfile +
  Layout route like other top-level pages) with a sidebar nav entry: a 12-month
  contribution-style heatmap (calendar grid of `days`), totals row,
  top-books-by-time list, and a recent-sessions timeline. Reuses the
  app's existing card/list components; no new charting dependency — the
  heatmap is a CSS grid of tinted cells (5 intensity buckets scaled to
  the profile's max day).

### Wiring

- Routes live in the existing `/ebooks` group (RequireProfile); the
  heartbeat and per-book stats are content-scoped, the history endpoint is
  profile-scoped. All additive.
- Client: a `useReadingHeartbeat` hook owns the activity gate and interval
  (fake-timer testable, pure decision helper for "is active"); the reader
  page mounts it for prose books only.

## Error handling

- Heartbeat failures are silent (console-debug at most); no retry queue.
- Sessions never block reading: handler errors return 4xx/5xx but the
  client ignores heartbeat responses entirely.
- Books deleted from the library keep their sessions (content_id has no
  FK; titles resolve via LEFT JOIN and fall back to "Removed book").
- Zero-progress sessions (opened, never turned a page) still count time;
  `start_fraction = end_fraction` is valid.

## Testing

- Go: coalescing rules (extend vs new session, 90s cap, 120s gap),
  pace/time-left math incl. both fallbacks and the null case, rollup
  query correctness over fixture sessions, profile scoping.
- Client: activity gate + interval scheduling with fake timers (visible /
  hidden / idle transitions), heartbeat payload, footer time-left
  rendering (present/absent), stats page rendering from fixture data.

## Out of scope

- Streaks, goals, achievements, Reading DNA (follow-up spec).
- Audiobook/podcast listening time (merges later from the playback
  system's data).
- Offline heartbeat queueing; comic reader sessions; cross-device session
  dedup (two devices reading simultaneously double-count — acceptable).
