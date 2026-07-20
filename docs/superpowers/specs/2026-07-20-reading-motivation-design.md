# Reading Stats: Motivation Layer

Date: 2026-07-20
Status: approved (design review)

Fourth reader sub-project; branch stacks on `feat/reader-stats`. Pure
aggregation over `reading_sessions` and the existing ebook progress data —
no new tracking, no reader-side changes. Everything lands as new sections on
the `/reading-stats` page.

## Problem

The stats foundation records time but offers no motivation loop: no streaks,
goals, challenges, badges, or taste profile.

## Design

### Streaks

- A UTC day counts toward a streak with **≥ 300 seconds** of reading (sum of
  session `duration_seconds` attributed to `started_at`'s UTC day).
- Computed on read from sessions: current streak (ending today or yesterday —
  a streak is "alive" until a full UTC day is missed) and longest streak.
  No storage.

### Goals (per profile, editable)

- `reading_goals` table: `user_id, profile_id, books_per_year int NULL,
  hours_per_year int NULL, updated_at` (PK user+profile). NULL = unset.
- `PUT /api/v1/ebooks/reading-goals` `{books_per_year?, hours_per_year?}`
  (null clears; positive ints ≤ 100000 validated).
- Progress computed on read for the current UTC year:
  - hours: YTD session seconds.
  - books: ebooks whose progress crossed the existing finished threshold
    (`models.EbookFinishedProgressThreshold`) with the progress row's
    `updated_at` in the current year — an approximation of "finished this
    year" (documented; the progress row's last write when finished is the
    best available signal without new tracking).
- Pace projection: linear from year elapsed → "on track for N".

### Monthly challenge (auto-generated)

- Target for the current month = `max(last calendar month's total seconds,
  10800)` (3-hour floor so an empty month doesn't trivialize it).
- Response includes target, current month seconds, and percent. No storage;
  no curated content.

### Achievements (persistent unlocks)

- `reading_achievements` table: `user_id, profile_id, achievement_id text,
  achieved_at timestamptz` (PK user+profile+achievement_id).
- Definitions live in Go as a fixed table (id, category, name, description,
  criteria over the computed aggregates). Evaluated **idempotently** during
  the motivation endpoint call: any newly satisfied badge is inserted with
  `ON CONFLICT DO NOTHING`; response returns all definitions with
  achieved_at (null = locked). Unlocks never revoke.
- Starter set (18), categories in parentheses:
  1. `first-hour` (time) — 1 hour total
  2. `ten-hours` (time) — 10 hours
  3. `fifty-hours` (time) — 50 hours
  4. `hundred-hours` (time) — 100 hours
  5. `marathon-session` (time) — a single session ≥ 2 hours
  6. `streak-3` (streak) — 3-day streak
  7. `streak-7` (streak) — 7-day streak
  8. `streak-30` (streak) — 30-day streak
  9. `streak-100` (streak) — 100-day streak
  10. `first-book` (books) — 1 book finished
  11. `ten-books` (books) — 10 books finished
  12. `fifty-books` (books) — 50 books finished
  13. `night-owl` (habits) — ≥ 10 hours read between 00:00–05:00*
  14. `early-bird` (habits) — ≥ 10 hours read between 05:00–08:00
  15. `weekender` (habits) — ≥ 20 hours on weekends
  16. `genre-hopper` (exploration) — sessions in ≥ 5 distinct genres
  17. `deep-diver` (exploration) — ≥ 10 hours on a single book
  18. `finisher` (exploration) — finished a book with ≥ 95% read
  *Habit hours use the session's `started_at` hour; UTC (consistent with all
  other day math — documented as such in the UI copy).

### Reading DNA (computed, no storage)

- Genre breakdown: session seconds joined through the book's `media_items`
  genres (seconds split evenly across a book's genres), top 8 + "other".
- Top authors by reading time (via the ebook author credits, kind 7).
- Diversity score 0–100: `(1 - Σ share²)` over genre shares (complement of
  the Herfindahl index), scaled ×100, rounded.
- Average session length, most-read hour-of-day buckets (morning/afternoon/
  evening/night), and a year-end projection of hours from YTD pace.

### API

- `GET /api/v1/ebooks/reading-motivation` → one payload:
  `{streak: {current_days, longest_days, today_seconds, today_qualified},
    goals: {books_per_year, hours_per_year, books_finished_ytd,
            hours_ytd, books_on_track_for, hours_on_track_for},
    challenge: {target_seconds, month_seconds, percent},
    achievements: [{id, category, name, description, achieved_at}],
    dna: {genres: [{name, seconds}], authors: [{name, seconds}],
          diversity_score, avg_session_seconds,
          hours_by_bucket: {morning, afternoon, evening, night},
          projected_year_hours}}`
  Evaluating achievements happens inside this call (insert-new-unlocks).
- `PUT /api/v1/ebooks/reading-goals` as above; both routes in the `/ebooks`
  group (RequireProfile), additive.

### UI (new sections on `/reading-stats`, top to bottom after totals)

- Streak + challenge row: current/longest streak with a flame count, the
  month challenge as a progress bar.
- Goals card: two editable number inputs (books/year, hours/year) saving on
  blur via the PUT; progress bars with "on track for N" captions; empty
  state invites setting a goal.
- Badge grid: all 18, locked ones dimmed with description; achieved show
  date. Category grouping.
- Reading DNA card: genre bar list, top authors, diversity score dial (plain
  number styling — no chart dependency), hour-bucket row, projection line.

## Error handling

- Motivation endpoint degrades per-section: a failed genre join yields empty
  DNA genres, never a 500 for the whole payload (partial data over hard
  failure; log the section error).
- Goals PUT validates and 400s with field-specific messages.
- Achievement evaluation failures skip persisting that badge silently (next
  load retries) — never block the response.

## Testing

- Go: streak math (alive-until-missed-day, longest), goal progress incl.
  year boundaries and the finished-book approximation, challenge floor,
  each achievement criterion satisfied/unsatisfied at its boundary,
  idempotent unlock persistence, DNA math (even genre split, diversity
  score, buckets, projection); endpoint shape.
- Client: goals editor save/validation, badge grid locked/unlocked render,
  streak/challenge/DNA sections from fixtures; graceful partial-payload
  rendering.

## Out of scope

- Notifications on unlock (future hook; achieved_at persistence enables it).
- Cross-profile/household leaderboards; sharing.
- Audiobook time (joins when listening data merges into sessions).
- Curated/rotating challenge content.
