# Dropped shows

A profile can drop a series to take it off its Next Up and Continue Watching rows
without touching its watch history. Drops sync both ways with watch providers that
support them (Trakt and Simkl).

## Local drops

`user_dropped_series` holds one row per profile and dropped series, with the time it was
dropped. A drop is **active** while no episode of the series has watch progress, or a
history-hide stamp (mark unwatched, remove from history), newer than `dropped_at`. So
watching any episode again undrops the series, whichever path recorded the watch, and no
writer needs to know about drops. Imported history keeps its original watch time, so
re-importing old plays never undrops a series. An inactive row means the same as no row;
watch sync removes it.

The rule reads `user_watch_progress` in PostgreSQL, so on deployments that keep watch
state in the SQLite user store a drop never ends by watching; it ends only when undone.

A v2 Home dismissal (`PUT /api/v2/home/dismissals/{surface}/{item_id}`) of an episode or
a series drops the series, from either surface. The frozen v1 route keeps per-card
dismissals.
Undoing the dismissal (`DELETE` on the same path) undrops it. Movies, audiobooks, and
ebooks keep per-item dismissals. Dismissing again refreshes `dropped_at`, which re-drops a
series the profile watched since.

## Where drops apply

- Next Up: global lookups (`ListNextUp` without a series) exclude active drops inside the
  anchor walk, so a dropped series neither shows nor spends the walk's series budget. This
  covers Home, Watch Tonight, section recipes, and jellycompat `/Shows/NextUp`. A lookup
  for one series (the series page, `/Shows/NextUp?SeriesId=`, Watch Together) still
  answers for it.
- Continue Watching: in-progress episodes of a dropped series are removed in the sections
  fetcher, the Continue Watching half of Watch Tonight, and the jellycompat Resume
  fallback.
- Raw progress and history reads are unaffected.

## Sync with providers

Each connection has one setting, `sync_dropped_enabled`. It is on for new connections,
and for existing connections that sync anything, so the first sync after the feature
ships imports drops already on the provider; a connection with every sync setting off
stays paused. A provider takes part when it advertises the `sync_dropped` capability.
Only v2 exposes the setting and capability; the frozen v1 responses omit them.

`watch_provider_dropped_items` records, per connection, the series Silo and the provider
last agreed are dropped. Each series is a three-way merge of the local drop, the remote
drop, and that agreed value; the side that moved wins. When the provider dropped a show
before the profile's latest watch of it in Silo, the show is undropped on the provider
instead of imported, because watching undrops. A drop that watching ended locally is restored when the provider's drop is newer than
that watch, since the user dropped the show again there. `remote_seen`, account scoping, the empty
read guard, and the rule that a series missing from a complete read counts as undropped
only when no row shares one of its ids all follow
[watch-provider-rating-sync.md](watch-provider-rating-sync.md). Local imports are
compare-and-set on the row the run read, so a concurrent dismissal or undo wins.

Provider reads can lag or omit Silo's writes. Trakt serves its GET responses from a cache
for up to an hour, and its dropped list leaves out drops that apps make, so a drop Silo
sends is usually never confirmed by a read. Such a drop stays agreed and is not sent
again; resending it would drop the show again each time the user undropped it on Trakt,
which Silo cannot see. An undrop of a drop a read had confirmed keeps its agreed row
until a read shows the show is gone, so a stale read resends the undrop instead of
importing the old drop back; an undrop of a drop no read confirmed forgets the row at
once. Both writes are idempotent.

Merges share the rating sync lock, so a connection reconciles ratings and drops one at a
time across nodes. Dismissing or undoing sends the change right away through a local
event, which waits for the lock and merges only the named series without reading the
provider. The scheduled sync runs the dropped phase first. It costs a request or two, so a large
history read that exhausts the provider's rate limit cannot starve it, and the order does
not change the merge: imported history keeps its original watch time, and providers
undrop a show that is watched.

## Providers

- Trakt: `GET /users/hidden/dropped` (every page, verified by a second read), and
  `POST /users/hidden/dropped` and `/users/hidden/dropped/remove`. Trakt's older hidden
  sections (`progress_watched`, `calendar`) are not drops and are not read. Trakt
  undrops a show itself when the user watches it.
- Simkl: `GET /sync/all-items/{shows,anime}/dropped`, skipped while the lists' `all`
  activity stamps are unchanged. Dropping moves a show to the `dropped` list with
  `POST /sync/add-to-list`; undropping moves it to `watching`. Simkl records no drop
  time, so an imported Simkl drop is stamped with the sync time.

Maintenance keeps both tables with their series: orphan cleanup treats them as
references, and catalog merges and splits move them. When a profile dropped both series
of a merge, the later drop wins, and a moved agreed row is reset to unconfirmed. Deleting
a profile deletes its drops on either user store backend.
