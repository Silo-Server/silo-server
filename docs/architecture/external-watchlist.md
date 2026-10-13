# Watchlist titles outside the library

A profile can put a movie or series on its watchlist before the library has it,
usually from Discover. The entry is kept by TMDB ID, shows on the watchlist
page's "Not in your library yet" tab, and moves onto the normal library
watchlist when the title arrives, keeping its original `added_at`. Where the
server and the profile allow it, adding such a title also requests it.

The code lives in `internal/watchlist` (`titles.go`, `titles_repo.go`,
`titles_repair.go`), the catalog match in `internal/catalog/provider_aliases.go`,
watchlist requests in `internal/requests/watchlist.go`, the library-watchlist
side in `internal/api/handlers/watchlist_titles.go`, and the v2 operations in
`internal/apiv2/watchlist_titles.go`.

Scope: movies and series only, native `/api/v2` only. jellycompat has nowhere
to show a title outside the library, and `/api/v1` is frozen. Catalog queries,
smart filters, the calendar, recommendations, Watch Together and the home
Watchlist row read catalog items only and never see these entries.

## Tables

- `watchlist_titles`: one row per external title, shared by every profile that
  added it, so its IDs are checked once for all of them. It holds the current
  TMDB, IMDb and TVDB IDs, a display snapshot from the last TMDB read (title,
  year, release date, poster, US certification, `vote_average`), the TMDB
  `state` (`active`, `needs_review`, `removed`), and the check bookkeeping
  (`not_found_count`, `last_not_found_at`, `checked_at`, `next_check_at`). The
  `id` comes from idgen and is never exposed.
- `watchlist_title_aliases`: every provider ID (`tmdb`, `imdb`, `tvdb`) a title
  has held, keyed by `(media_type, provider, provider_id)`. Two titles can never
  hold the same ID, which is how a duplicate is recognized. A title's former
  TMDB ID stays here after a repoint, so the API still finds the title by it.
- `user_watchlist_titles`: one row per profile entry, keyed by
  `(user_id, profile_id, title_id)`, with `added_at`. `user_id` cascades from
  `users`; there is no profile foreign key because profiles may live in the
  SQLite user store.

`stale_media_ids` gained an index on `(provider, provider_id)`, built
concurrently in its own `NO TRANSACTION` migration, so a lookup by a rejected
value no longer scans the table.

## Invariants

1. **No catalog IDs are stored.** None of the three tables stores a
   `content_id`, and no column uses a name from the `silo_rename_content_ids`
   sweep list (`media_item_id`, `content_id`, `item_id`, `series_id`, … in
   `migrations/sql/20261004211822_add_bulk_content_id_rename.sql`). Re-anchoring,
   merging and deleting catalog items never touch these tables. Keep it that way
   when adding columns.
2. **The title row is locked first.** Every write that can delete a
   `watchlist_titles` row, or attach an entry to one, locks that row before it
   touches entries:
   - Add: `SELECT … FOR UPDATE` on the title holding the TMDB alias, or create
     the title. Creation inserts the TMDB alias with `ON CONFLICT DO NOTHING`;
     losing that race restarts the add against the winner's title (at most 3
     attempts).
   - Lookup by TMDB alias (add and remove): a merge can delete the title the
     lock waited on and move its aliases to the survivor, and the row lock
     then comes back empty. An empty result counts as "no title" only when a
     fresh statement finds no alias either; otherwise the lock is retried
     against the alias's new title (at most 3 attempts).
   - Remove: lock the title, delete the entry, then delete the title if no
     entry references it. The orphan check is a separate statement after the
     lock, so it sees any add it waited on.
   - Promote: lock the title and, only while the profile's entry exists, add
     the item to the library watchlist, then delete the entry and the orphaned
     title before the lock is released. A remove of the same title waits, so it
     either runs first (promotion then adds nothing) or runs after the library
     entry exists and can take it off.
   - Profile purge: lock the profile's titles in id order, delete its entries,
     delete the titles left without entries.
   - Merge: lock both titles in id order.

   So an add never attaches to a title that a concurrent remove is deleting.
3. **Promotion side effects fire once, and only for a new library entry.**
   Only the caller whose `DELETE` removed the entry runs the side effects, and
   only when the library watchlist entry is the promotion's own: its
   `AddToWatchlistAt` inserted the row, or the existing row carries the entry's
   `added_at` (to the second), which is the row a node left behind when it died
   between the add and the delete. An item the profile already had on its
   library watchlist absorbs the entry silently.
4. **Titles merge only through 404 recovery.** A live TMDB detail never merges
   two titles. When a check or detail view reports an IMDb or TVDB ID that is
   already another title's alias, that alias is skipped.
5. **Entries are never deleted automatically.** Only the user's remove,
   promotion, a merge (which moves the entry to the surviving title), and
   profile or user deletion remove an entry. A title TMDB no longer lists keeps
   its entries and shows as needing attention.

### Orphan titles

Two paths delete entries without the title lock: the Postgres user store's
profile delete (`user_watchlist_titles` is in its cascade list) and user
deletion through the `users` foreign key. Both can leave titles with no
entries. After every profile delete, the profile handler calls
`Titles.PurgeProfile`, which removes that profile's entries under the lock and
then sweeps up to 1000 orphaned titles (`FOR UPDATE SKIP LOCKED`, checked again
after locking). After every account delete (v1 `DELETE /admin/users/{id}` and
the v2 admin account delete), the admin handler runs the same sweep through
`Titles.SweepOrphanTitles`. A sweep failure is logged and the next delete
continues it.

A profile delete also withdraws the requests the profile's watchlist made that
nothing has been sent for yet (`WithdrawProfileWatchlistRequests`), as removing
each title would. It needs no entries: a request names the profile that made
it and its `source`. Requests already sent stay in the pipeline.

Until the sweep runs, an orphan's snapshot is not kept current, so lookups by
TMDB ID (`Titles.Find`, which the v2 add uses for its stored snapshot and
rating check, and the TMDB detail observers) skip titles with no entries. An
add that attaches to an orphan stores the caller's snapshot on it and makes its
next check due.

## Resolution

`catalog.ItemRepository.ResolveProviderAliases` decides whether the library has
a title. It is computed when needed and never stored. A catalog item matches an
alias when any of these holds:

- `media_items.tmdb_id`, `imdb_id` or `tvdb_id` equals the alias;
- a `media_item_provider_ids` row equals the alias (same item type);
- a `stale_media_ids` row equals the alias, which catches an item that carries,
  or once carried, a TMDB duplicate ID since deleted.

Only items of the title's media type with a `media_item_libraries` row in an
enabled folder count. Unlike `LookupExternalIDs`, it returns every distinct
match:

- **One `content_id`:** the library has the title.
- **Several:** the library holds the title twice. Nothing is promoted and a
  warning is logged; the entry resolves once the catalog duplicate is merged.
  The add path likewise keeps such a title as an entry.
- **None:** the library does not have it.

`ItemProviderAliases` is the reverse: every current and former TMDB, IMDb and
TVDB ID of one catalog item, used by `PromoteItem`.

## Promotion

`Titles.PromoteProfile(viewer)` loads the profile's entries with their aliases
(one index probe when there are none) and resolves them in one query.
`Titles.PromoteItem(viewer, contentID)` does the same for the entries whose
aliases match one catalog item's IDs. Every matched copy is checked against
the viewer's access filter in one `EnsureAccessibleIDs` query, and only the
copies the viewer may see count: a duplicate in a library the viewer can't
open doesn't make the title ambiguous for them. A title with no visible copy
stays external (library or rating limits); one with several waits until the
catalog merges them. For each title with exactly one visible copy:

1. Lock the title and check the entry still exists (invariant 2).
2. `AddToWatchlistAt(profileID, contentID, entry.added_at)` through the
   notification-wrapped user store, so a promoted series queues an interest
   recompute. When the row already exists, `GetWatchlistEntry` tells whether
   it is a half-done promotion (invariant 3).
3. Delete the entry, and the title if it is now orphaned, then commit.
4. If this call removed the entry and the library entry is new (invariant 3),
   run `Effects.WatchlistPromoted`, implemented by
   `handlers.PersonalDataHandler`: the provider export event
   (`dispatchLocalListEvent`), the recommendations refresh
   (`triggerProfileRefresh`) and a realtime `user_state.changed` event with
   `change: "watchlist"`. These are the effects of a manual add.

The library add in step 3 uses a second pool connection while the promotion's
transaction holds one. Each node runs at most 4 such promotions at once, and at
most half the pool's connections, so they cannot exhaust the pool. A pool of
one connection (`database.max_connections: 1`) can't lend that second
connection, so there the library add runs first, after a check that the entry
still exists, and the title is locked afterwards. A remove landing between that
check and the lock can still leave the item on the library watchlist.

Promotion runs only in request-scoped reads, so a viewer is always available.
A failure is logged and the read continues; the next read retries.

| Read | Call |
|---|---|
| `GET /api/v2/watchlist/titles` | `PromoteProfile` on the first page only, then the list |
| `GET /api/v2/watchlist` and v1 `GET /watchlist` (`PersonalDataHandler.ListWatchlistPage` / `ListWatchlist`) | `PromoteProfile` on the first page only (no cursor, offset 0) |
| Catalog query with `source: "watchlist"` (`CatalogResolver.resolvePersonalSource`) | `PromoteProfile` |
| Watchlist section in `internal/sections/fetcher.go` (home rows and the recommendations fetcher) | `PromoteProfile` |
| Item detail `user_state.in_watchlist` (`CatalogResourceHandler.enrichViewerState`, movies and series) and `GET /api/v2/watchlist/{item_id}` | `PromoteItem` |

Other catalog reads (item lists through `ItemsHandler`, people, jellycompat) do
not promote. Calling promotion from v1 changes no v1 contract: the arrived item
simply appears in the list.

## ID repair without a scheduled task

There is no task, lease table or queue for watchlist titles. Checks happen only
when someone lists or views a title, and each title is checked once for every
profile that has it. Do not add a scheduled task for this.

**Cadence.** A title is due when `next_check_at <= now()`. After a successful
check the next one is 1 day away while the release date is unknown, upcoming or
within the last 180 days, and 30 days away otherwise. A `removed` or
`needs_review` title is checked again every 30 days. A non-404 error (TMDB
unreachable) backs off 6 hours and leaves the state alone.

**Triggers.**

1. **A TMDB detail is read for another reason.** `requests.Service.GetDetail`
   (the Discover title page) and the add path call the injected
   `TitleObserver`: `ObservedDetail` after a successful fetch, `ObservedNotFound`
   on `tmdb.ErrNotFound`. Each is one alias lookup and writes only when the ID
   is a tracked title's current TMDB ID and something changed. Errors are
   logged, never returned.
2. **A watchlist titles page is listed.** `ScheduleChecks` takes up to 3
   overdue titles from the page, reserves a per-node slot for each (at most 4
   concurrent checks per node), and claims them with a conditional update that
   pushes `next_check_at` out by 1 hour. Only one node wins a title; if it dies,
   the claim expires and a later read retries. The checks run detached from the
   request with a 30 second timeout. The page renders from the stored snapshot
   and never waits for TMDB.

**A check** fetches the movie or TV detail with `external_ids` (`GetMediaDetail`,
cached briefly by the TMDB client), stores the current IMDb and TVDB IDs, adds
new aliases, refreshes the snapshot, resets the 404 count and sets the next
check time.

| Case | Detection | Action |
|---|---|---|
| TMDB deleted the ID (a merged duplicate) | Two 404s at least 24 hours apart. The first only schedules the confirming check. | Recover through the stored IMDb aliases, then TVDB, with `tmdb.Client.FindByExternalID` (`/find/{id}?external_source=…`). One candidate of the right media type repoints `tmdb_id`, keeping the old ID as an alias, and refreshes the snapshot. |
| The recovered ID is another title's alias | Alias key conflict | Merge: lock both, move entries keeping the earliest `added_at`, move aliases, delete the losing title. A profile that had both ends with one entry. |
| Several candidates | More than one result | `state = 'needs_review'` |
| No candidates | No results | `state = 'removed'`; the entry stays. |
| IMDb or TVDB ID added or corrected on TMDB | Check or detail observation | Update the current IDs and add aliases, skipping any held by another title (invariant 4). |
| The library imports the title under the dead ID | Resolution matches the old alias | Promoted as usual. |
| TMDB outage | Non-404 error | Back off 6 hours; state unchanged. |

A title-and-year search is never applied automatically. The web shows
`needs_review` and `removed` titles with a "Find it" link to a Discover search
and a remove button.

## Watchlist requests

Adding a title the library doesn't have can also request it, so the existing
`request.fulfilled` notification tells the profile when it arrives. See
[Media requests](media-requests.md#watchlist-requests) for the request side.

- Server switch: `request_settings.watchlist_requests` (default on), edited in
  admin Settings › Requests and carried by the v2 admin request settings.
- Profile opt-out: the settings-contract key `requests.watchlist_auto_request`
  (profile scope, default `true`), shown in Settings › Requests.

An add requests only when requests are on, both switches are on, and the
account may request. `GET /api/v2/requests/status` reports that effective value
as `watchlist_requests`. If the profile's setting cannot be read, the add does
not request.

The entry is saved first and the request step runs after it, so a refused or
failed request never loses the entry, and a repeated add is a no-op once either
step has succeeded. Removing the title withdraws a request the watchlist made
while it is still withdrawable and drops the profile's follows.

A request keeps the TMDB ID it was made under; repair never rewrites
`media_requests`. So every request lookup for a title (the add, the card's
request state, the withdrawal) checks the title's current TMDB ID and each
former one, and a follow goes under the request's own ID. Matching a title to
the library also uses every TMDB ID it has had, so a library copy known only by
a replaced ID still counts.

## API

The three operations are on `/api/v2` and live on the requests surface: they
answer `409 capability_disabled` while requests are off, and the entries are
kept. Promotion on library reads does not depend on that gate.

- `GET /api/v2/watchlist/titles` (`listWatchlistTitles`): promotes, then pages
  the remaining entries by `(added_at DESC, title id DESC)` with a signed cursor
  bound to the account, profile and viewer policy. Titles whose stored US
  certification is above the viewer's rating ceiling are omitted (a title with
  no certification fails closed under a ceiling). Each item carries its request
  state for the viewer, with `download` while it downloads.
- `PUT /api/v2/watchlist/titles/{media_type}/{tmdb_id}` (`addWatchlistTitle`):
  a title already tracked under that ID (current or former) uses its stored
  snapshot; otherwise the server reads the TMDB detail and answers 404 when TMDB
  has no such title or it is above the ceiling. If exactly one library item the
  viewer can see has the title, it goes onto the library watchlist through the
  normal add and the response carries `item_id`. Otherwise the entry is saved
  and watchlist requests apply.
- `DELETE /api/v2/watchlist/titles/{media_type}/{tmdb_id}`
  (`deleteWatchlistTitle`): finds the title by a current or former TMDB ID and
  first withdraws the watchlist's request under every TMDB ID the title has
  had, since a request keeps the ID it was made under. Only then does it remove
  the library watchlist entry of the one visible library item that has the
  title (matched by every ID while the entry still stands), then the entry, and
  that library watchlist entry once more for a promotion that moved the entry
  onto it in between. Last, it withdraws once more if the title is still off
  the watchlist. The order matters: removing the last entry deletes the title and
  its former IDs, so a withdrawal that fails part way leaves the entry and a
  retry still knows every ID. The second withdrawal, together with the add
  rechecking after it requests and withdrawing when the title is off the
  watchlist, means an overlapping add and delete of the same title don't leave
  a request without an entry. "Off the watchlist" (`WatchlistTitleOff`) means
  both forms: no entry outside the library and no library watchlist entry for
  its library copy. A title promotion moved while an add was requesting, or
  one a re-add put back, is on the watchlist and keeps its request. An absent
  entry succeeds with 204.

Both mutations are `non_retryable`, as the library watchlist mutations are.
Discovery results and the title detail carry `in_watchlist`, hydrated with two
reads per page: title entries by TMDB ID, and the library watchlist by
`library_content_id`. `GET /api/v2/requests/status` advertises
`watchlist_titles_supported`, which is false while requests are off: the
operations then answer `409 capability_disabled` and the entries are kept. See [api-contract.md](api-contract.md#watchlist-titles).

There is no new realtime event. `user_state.changed` is keyed by `content_id`,
so other devices pick up entry changes on their next load; promotion emits the
normal event.

## Status badge on the poster

The web shows a title's request status as the `request_status` card overlay in
the `ribbons` group (default on, top-left, with an icon), so it follows the
viewer's overlay preset, corner, accent and `ui.card_overlays_enabled`. It has a
value only for titles outside the library. Attention states (`needs_review`,
`removed`) use an amber accent through the overlay's `getAccent`, unless the
viewer picked an accent for the badge. While a title downloads, the card draws
a progress bar that belongs to this badge: it shows only when overlays and the
badge are both on. The card caption always states the status in words, so the
tab reads the same with overlays off. TMDB rating and content rating badges use
the snapshot's `vote_average` and certification; file-based badges have no
value.

The settings contract added `request_status` to the card-overlays `overlayId`
enum at manifest revision 15. A server below revision 15 rejects a stored value
that contains it, so clients offer the badge only when `manifest_revision` is at
least 15.

## Multi-node and failure behavior

- Repair claims are conditional updates; promotion adds to the library
  watchlist and deletes the entry while it holds the title lock; adds, removes,
  purges and merges follow the lock order.
- A node that dies mid-check leaves a claim that expires after 1 hour.
- A node that dies between the promotion add and the entry delete rolls the
  delete back and leaves both rows. The next read finishes the move, and that
  caller fires the side effects because the library row carries the entry's
  `added_at`.

## Not built

Watch-provider (Trakt, Simkl, MDBList) and Plex imports still drop rows the
library doesn't have, and export only library items. Carrying unowned titles
through them needs its own design; imports would never auto-request. A profile
that cannot request, or has opted out, gets no arrival notification: only the
request's `request.fulfilled` notification tells anyone.
