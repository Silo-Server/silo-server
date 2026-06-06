# ABS Phase 1 Close-out — Sub-Project 4

**Status:** Approved 2026-05-26. Ready for implementation plan.
**Scope:** Final sub-project of Phase 1 (per `2026-05-26-abs-implementation-fix-design.md`). Closes out the four remaining feature surfaces in one combined spec/plan to ship in a single push.
**Predecessor specs:** bookmarks, collections+playlists, smart collections. Established conventions (anti-enumeration 404, `io.LimitReader(1<<20)`, `errors.Is(err, pgx.ErrNoRows)`, profile-scoped + cross-user-public, 200-on-POST, in-package test fakes, `dispatchABSWithParams`) carry over without re-justification.

Commands assume the repository root is the cwd.

## 1. Goal

Land the four remaining Phase 1 surfaces:
- **Listening stats** — totalTime / days / dayOfWeek / monthly aggregations + paginated session history + per-session detail.
- **Author detail** — `GET /authors/{id}` and `GET /authors/{id}/image` (the latter currently 404s; wire to people poster image).
- **Series detail** — `GET /series/{id}` with embedded books sorted by series_sequence.
- **Continue-listening toggles** — `/me/progress/{itemId}/remove-from-continue-listening` + `/readd-to-continue-listening`, backed by a new `hide_from_continue` column on `user_watch_progress`.
- **RSS feeds** — minimal viable surface: open a feed for a library item, list caller's feeds, close a feed, and serve the public RSS XML to podcast clients. Series/collection feed variants deferred.

## 2. Non-goals

- **No socket events.** Continue-listening toggles, stats, author/series, and RSS feed mutations fire no events (Phase 2 covers full event parity).
- **No series/collection RSS feeds.** Only single-item feeds in v1. The continuum `/api/feeds/series/{id}/open` and `/api/feeds/collection/{id}/open` variants are explicitly deferred.
- **No RSS cover / per-track endpoints.** `GET /feed/{slug}/cover` and `GET /feed/{slug}/item/{itemId}/{idx}` are deferred — podcast clients can fetch covers from the enclosure URL embedded in the RSS XML.
- **No stable series IDs.** silo stores series denormalised on `audiobook_series.content_id` (one row per book); a "series" is identified by `LOWER(series_name)`. The detail handler accepts URL-decoded series names; URL slugs use the lowercased name. Continuum's stable series-row schema is out of scope.
- **No author write surface.** Author detail is read-only.

## 3. Architecture

Five surfaces, sharing the established structure. Three new HTTP handler files; one new migration; minimal extensions to existing stores; one new pgx store for RSS.

- **HTTP layer (`internal/audiobooks/abs/`):**
  - `listening_stats_handler.go` — three handlers (stats / list / detail).
  - `author_series_handler.go` — three handlers (author / author-image / series).
  - `continue_listening_handler.go` — two handlers.
  - `rss_feeds_handler.go` — four authenticated handlers + one public XML handler.
- **Store extensions:**
  - `ABSPlaybackSessionStore.AggregateStats(ctx, userID, profileID) (Stats, error)` — single SQL with grouped aggregates.
  - `ABSPlaybackSessionStore.ListClosedSessions(ctx, userID, profileID, limit, offset) ([]ABSPlaybackSession, int, error)` — paginated history.
  - `ProgressStore.SetHideFromContinue(ctx, userID, profileID, contentID string, hide bool) error` — toggle column.
  - `MediaStore.GetAuthorByID(ctx, authorID string) (Author, error)` — author detail + books list.
  - `MediaStore.GetSeriesByName(ctx, seriesName string) (Series, error)` — series detail + books list ordered by series_index.
- **New store interface + concrete:**
  - `RSSFeedStore` interface (CRUD + lookup-by-slug) in `internal/audiobooks/abs/rss_feeds.go`.
  - `ABSRSSFeedStore` (pgx) in `internal/audiobooks/abs_rss_feed_store.go`.
- **Migration:**
  - `154_user_watch_progress_hide_from_continue.up.sql` — `ALTER TABLE user_watch_progress ADD COLUMN hide_from_continue boolean NOT NULL DEFAULT false`.
  - `155_abs_rss_feeds.up.sql` — feed rows (id, user_id, profile_id, library_item_id, slug, minified, created_at, closed_at).
- **Service wiring + route registration:** new fields on `abs.Dependencies` + new routes in `mountRoutes` (auth-gated group plus a new unauth public group for `/feed/{slug}.xml`).

## 4. Endpoint surface

All authenticated routes mount under both `/abs/api/*` and `/api/*` inside the existing `bearerAuth` group. Success status is **200 OK** except where the `Returns` column says otherwise.

### 4.1 Listening stats

| Verb | Path | Returns |
|---|---|---|
| GET | `/me/listening-stats` | `{totalTime, items, days, dayOfWeek, monthly}` aggregated from `abs_playback_sessions` |
| GET | `/me/listening-sessions` (?limit=, ?page=) | `pagedEnvelope` of session rows (most-recent-first) |
| GET | `/me/listening-sessions/{sid}` | single session detail (404 when not found or not owned) |

Stats wire shape:

```json
{
  "totalTime": 36000,
  "items": 12,
  "days": [{"date": "2026-05-26", "seconds": 1800}, ...],
  "dayOfWeek": {"0": 1200, "1": 3600, ..., "6": 900},
  "monthly": [{"month": "2026-05", "seconds": 18000}, ...]
}
```

Session row shape:

```json
{
  "id": "01HSESS...",
  "libraryItemId": "126887...",
  "userId": "1",
  "startedAt": 1779786284823,
  "lastUpdate": 1779786884823,
  "duration": 1500,
  "currentTime": 1234.5,
  "timeListening": 600
}
```

### 4.2 Author + Series detail

| Verb | Path | Returns |
|---|---|---|
| GET | `/authors/{id}` | `{id, name, numBooks, books: [LibraryItem]}` |
| GET | `/authors/{id}/image` | redirect to PresignURL for `item_people.poster_path` when set; otherwise 404 |
| GET | `/series/{id}` | `{id, name, numBooks, books: [LibraryItem]}` ordered by series_index ASC |

`{id}` for authors is the stringified `people.id`. `{id}` for series is the URL-decoded series name (case-insensitive lookup). The author-image route was already mounted unauthenticated in handler.go but currently returns 404 — this spec wires the body.

### 4.3 Continue-listening toggles

| Verb | Path | Returns |
|---|---|---|
| GET | `/me/progress/{itemId}/remove-from-continue-listening` | 200 `{ok: true}` after setting `hide_from_continue = true` |
| GET | `/me/progress/{itemId}/readd-to-continue-listening` | 200 `{ok: true}` after setting `hide_from_continue = false` |

Both endpoints are idempotent. Item validation via `MediaStore.GetAudiobookByID` (404 on unknown item). The existing `MediaStore.ListContinueListening` SQL is updated to add `AND uwp.hide_from_continue = false` to the WHERE clause.

### 4.4 RSS feeds

| Verb | Path | Auth | Body | Returns |
|---|---|---|---|---|
| GET | `/api/feeds` | bearer | — | `{feeds: [Feed]}` for caller (owner + profile scope, only open feeds) |
| POST | `/api/feeds/item/{itemId}/open` | bearer | `{slug?, minified?}` | created Feed (full shape) |
| POST | `/api/feeds/{id}/close` | bearer | — | 204 (sets closed_at) |
| GET | `/feed/{slug}.xml` | none | — | RSS 2.0 XML; 404 on unknown / closed |
| GET | `/feed/{slug}` | none | — | same as above (some podcast clients omit the `.xml`) |

Feed wire shape:

```json
{
  "id": "01HFEED...",
  "userId": "1",
  "libraryItemId": "126887...",
  "slug": "abc-randomsluggenerated",
  "minified": false,
  "createdAt": 1779786284823,
  "url": "https://silo.example.com/feed/abc-randomsluggenerated.xml"
}
```

`slug` is a 16-char URL-safe random string when caller omits it from the body. Manual slugs accepted as long as they match `^[a-z0-9-]{4,64}$`. Slugs are globally unique (UNIQUE index).

RSS XML uses standard RSS 2.0 + iTunes namespace; each `<item>` is one media_file (chapter) of the underlying audiobook. URLs are absolute (`absBaseURL` + `/public/session/...` style — but RSS feeds need a stable public path, so v1 uses a new public file route below). For v1 simplicity, the `<enclosure url>` points at the existing `/abs/public/session/{sid}/track/{idx}` style URL — but feeds don't have an associated session. So we use a new public route mounted in tandem:

**Additional public RSS support route:**

| Verb | Path | Auth | Returns |
|---|---|---|---|
| GET | `/feed/{slug}/file/{ino}` | none | streams the media file for the feed's library_item_id (404 if ino doesn't belong to the item) |

This is a feed-scoped public file route — slug is the capability token, so no Bearer needed. Mirrors the bookmarked-session pattern from sub-project 1.

## 5. Data model

### 5.1 Migration 154 — `hide_from_continue` column

`migrations/154_user_watch_progress_hide_from_continue.up.sql`:

```sql
ALTER TABLE public.user_watch_progress
    ADD COLUMN IF NOT EXISTS hide_from_continue boolean NOT NULL DEFAULT false;
```

`migrations/154_user_watch_progress_hide_from_continue.down.sql`:

```sql
ALTER TABLE public.user_watch_progress
    DROP COLUMN IF EXISTS hide_from_continue;
```

### 5.2 Migration 155 — `abs_rss_feeds`

`migrations/155_abs_rss_feeds.up.sql`:

```sql
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

### 5.3 Go models

```go
// Stats aggregates per spec §4.1.
type Stats struct {
    TotalTime int                          // seconds
    Items     int                          // distinct content_ids listened to
    Days      []DayStat                    // last 30 days
    DayOfWeek [7]int                       // index 0=Sunday
    Monthly   []MonthStat                  // last 12 months
}
type DayStat   struct{ Date string; Seconds int }   // "2026-05-26"
type MonthStat struct{ Month string; Seconds int }  // "2026-05"

// Author is the detail-shape row.
type Author struct {
    ID       string
    Name     string
    PosterPath string  // empty when unset; resolved via CoverResolver on emit
    Books    []*models.MediaItem
}

// Series is the detail-shape row.
type Series struct {
    ID    string  // lowercased series_name
    Name  string  // canonical series_name
    Books []*models.MediaItem  // ordered by series_index ASC
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
```

## 6. Storage contracts

```go
// Extensions to existing interfaces:

// On ABSPlaybackSessionStore (existing):
AggregateStats(ctx context.Context, userID, profileID string) (Stats, error)
ListClosedSessions(ctx context.Context, userID, profileID string, limit, offset int) ([]ABSPlaybackSession, int, error)

// On ProgressStore (existing):
SetHideFromContinue(ctx context.Context, userID, profileID, contentID string, hide bool) error

// On MediaStore (existing):
GetAuthorByID(ctx context.Context, authorID string) (Author, error)
GetSeriesByName(ctx context.Context, seriesName string) (Series, error)

// New store interface — RSSFeedStore (defined in internal/audiobooks/abs/rss_feeds.go):
type RSSFeedStore interface {
    ListUserFeeds(ctx context.Context, userID, profileID string) ([]RSSFeed, error)
    GetFeedBySlug(ctx context.Context, slug string) (RSSFeed, error)
    GetFeed(ctx context.Context, id string) (RSSFeed, error)
    CreateFeed(ctx context.Context, f RSSFeed) error
    CloseFeed(ctx context.Context, id string) error
}
```

Behavior:
- `AggregateStats` runs one SQL with three CTE subqueries (days / day_of_week / monthly aggregates) plus a `SUM(time_listening_seconds)` and `COUNT(DISTINCT content_id)`.
- `SetHideFromContinue` writes `UPDATE user_watch_progress SET hide_from_continue = $4 WHERE user_id = $1 AND profile_id = $2 AND media_item_id = $3`. Returns `nil` even when no row matched (idempotent — no-progress means nothing to hide; client can readd later).
- `GetAuthorByID` parses `authorID` as int (`strconv.Atoi`), then `SELECT name FROM people WHERE id = $1` for the author row + `SELECT mi.* FROM item_people ip JOIN media_items mi ON mi.content_id = ip.content_id WHERE ip.person_id = $1 AND ip.kind = 7 AND mi.type = 'audiobook' ORDER BY mi.title`. Returns `ErrNotFound` when the people row is missing.
- `GetSeriesByName` does case-insensitive lookup on `audiobook_series.series_name`: `SELECT DISTINCT series_name FROM audiobook_series WHERE LOWER(series_name) = LOWER($1) LIMIT 1` → name; then `SELECT mi.* FROM audiobook_series s JOIN media_items mi ON mi.content_id = s.content_id WHERE LOWER(s.series_name) = LOWER($1) AND mi.type = 'audiobook' ORDER BY s.series_index NULLS LAST`. Returns `ErrNotFound` when no rows.
- `RSSFeedStore.CreateFeed` inserts with the slug as-is; UNIQUE constraint on slug surfaces as a 409 client-side. `GetFeedBySlug` only returns rows where `closed_at IS NULL` (closed feeds 404 to podcast clients). `CloseFeed` sets `closed_at = now()` — idempotent on already-closed rows.

## 7. Error model

| Condition | Status | Body |
|---|---|---|
| 401 missing/invalid bearer | 401 | bearerAuth |
| Body decode (POST `/feeds/.../open`) | 400 | `invalid body` |
| Slug fails `^[a-z0-9-]{4,64}$` | 400 | `invalid slug` |
| Item not found on author/series/feed POST | 404 | `<type> not found` |
| Author/Series unknown ID | 404 | `<author\|series> not found` |
| RSS slug collision on POST | 409 | `slug taken` |
| Non-owner DELETE/close feed | 404 | `feed not found` (anti-enumeration) |
| Public `/feed/{slug}` unknown or closed slug | 404 | `feed not found` (plain text body — podcast clients ignore it) |
| Store mutate fails | 500 | sanitized; err logged |

Cross-cutting:
- Same `io.LimitReader(1<<20)` on POST/PATCH bodies.
- Continue-listening toggles are idempotent and return 200 even on no-row-matched.
- The public RSS routes (`/feed/{slug}.xml`, `/feed/{slug}`, `/feed/{slug}/file/{ino}`) mount OUTSIDE the bearerAuth group — they're capability-gated by the slug. Mount alongside the existing public-session routes in `mountRoutes`.

## 8. Testing

### 8.1 Listening stats

- `TestStats_TotalAggregation` — seed 3 closed sessions, assert `totalTime == sum(time_listening_seconds)` and `items == 2` (3 sessions across 2 distinct items).
- `TestStats_DayOfWeekBucketing` — sessions on a Monday + a Wednesday + another Monday, assert `dayOfWeek[1] == 2 sessions worth, dayOfWeek[3] == 1`.
- `TestSessions_List_PaginatedAndScoped` — paged response shape, other-user sessions excluded.
- `TestSession_Detail_Owner_OK` + `TestSession_Detail_NonOwner_404`.

### 8.2 Author + Series

- `TestAuthor_Detail_ReturnsBooks` — seed author with 2 books; GET returns both, name matches.
- `TestAuthor_Unknown_404`.
- `TestAuthor_Image_PresignWired` — when CoverResolver returns a URL, image route 302s; when poster_path is empty, 404.
- `TestSeries_Detail_OrderedByIndex` — books returned in series_index ASC.
- `TestSeries_Unknown_404` (name doesn't match any audiobook_series row).

### 8.3 Continue-listening

- `TestContinue_Remove_SetsHideTrue` — seed progress row, GET remove-from-continue, assert column becomes true.
- `TestContinue_Readd_SetsHideFalse` — inverse.
- `TestContinue_UnknownItem_404`.
- `TestContinue_NoProgressRow_OK_Idempotent` — no progress row exists; remove-from-continue still returns 200 (idempotent no-op).

### 8.4 RSS

- `TestFeed_Open_Item_GeneratesSlug` — POST `/api/feeds/item/{id}/open` returns a feed with a random 16-char slug.
- `TestFeed_Open_AcceptsCustomSlug`.
- `TestFeed_Open_RejectsInvalidSlug_400`.
- `TestFeed_Open_RejectsCollision_409`.
- `TestFeed_List_OwnerOnly` — only caller's open feeds, profile-scoped.
- `TestFeed_Close_Owner_204`.
- `TestFeed_Close_NonOwner_404`.
- `TestPublicFeed_UnknownSlug_404`.
- `TestPublicFeed_ClosedSlug_404`.
- `TestPublicFeed_HappyPath_RSSXML` — open a feed with a known item; fetch `/feed/{slug}.xml`; assert Content-Type is `application/rss+xml`, body contains `<rss`, `<channel>`, `<item>`, `<enclosure url=`.

### 8.5 Out of scope

- No real-Postgres SQL test.
- No live smoke for the public RSS endpoints (operator can hit them; v1 doesn't include the curl snippet).
- No RSS validator test against a third-party tool.

## 9. References

- Continuum reference: `continuum-plugin-audiobooks/internal/abs/rss_feed_handler.go` (port routing names + slug generation; adapt store/auth to silo).
- silo's existing public-route precedent: `handlePublicTrack` in `internal/audiobooks/abs/file_handler.go` — same slug-as-capability pattern.
- Item-people kinds: 7 = author per `media_store.go:ListLibraryAuthors`. Narrator kind not used here.

## 10. Out-of-scope follow-ups

- RSS feed variants: series feeds, collection feeds, cover endpoint, per-track public route is included but is the minimum.
- Listening-stats `/all-time` vs `/last-N-days` filters — current spec returns a single aggregate.
- Author edit / merge surface.
- Series metadata edit.
- Stats charts beyond the three buckets (per-genre, per-author, per-narrator) — defer.
