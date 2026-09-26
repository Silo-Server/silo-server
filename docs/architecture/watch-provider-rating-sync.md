# Watch-provider rating sync

A profile's movie and series ratings sync with a connected watch provider in either
direction or both. Each connection has two settings: `import_ratings_enabled` brings
provider ratings into Silo, and `export_ratings_enabled` sends Silo ratings to the
provider and clears ones the profile removes. New connections start with both on;
connections that existed before rating sync start with both off. A provider takes part
only when it advertises the matching `import_ratings` or `export_ratings` capability.
Season and episode ratings are not synced, because Silo rates only movies and series.

## Scale

Silo stores integer stars from 1 to 5. Providers and the plugin contract use integers
from 1 to 10. Import rounds half up (`stars = (rating + 1) / 2`, so 7 and 8 are both
4 stars) and export doubles (`rating = stars × 2`). Every sync decision compares stars,
so a provider change within one star, such as 7 to 8, is not a change, and Silo never
overwrites a provider's 7 with the 8 its 4 stars map to.

## Agreed rating

`watch_provider_rating_items` stores, per connection and item, the last rating both
sides agreed on, in stars. No row means both sides agreed the item is unrated, so the
first sync is a union of both sides. Each item is a three-way merge of the local
rating, the provider rating, and the agreed rating:

- Both sides equal: record the agreement.
- Only one side moved from the agreed rating: that side wins, including removals.
- Both moved to different values: a rating beats a removal, so a conflict never
  deletes; otherwise the newer change wins, and ties or unknown provider times go to
  Silo.

A decision the connection's direction settings do not allow is skipped without
recording agreement, so it is reconsidered when the direction is turned on.

`remote_seen` records that a provider read confirmed the agreed rating. A sent rating
is agreed but not seen until a later read returns it, and it is agreed only if the local
rating still has the value that was sent. A sent removal keeps its agreed row until a
read confirms the title is unrated; if the provider still reports a rating (for example
a second entry for the same title), the removal is sent again instead of the rating
being imported back.

A local rating that changed while its write was in flight may have been sent already by
a newer event, which the older write then overwrote. So the current value is sent again,
up to three times. If the rating is still changing after that, its agreed row is set to
the value last confirmed on the provider, so the next merge reads the newer local value
(rating or removal) as a local change and sends it instead of importing the provider's.

Rows are scoped to the provider account they were agreed with. A connection that moves
to another account ignores the old rows, and so does a sync still running for the old
account, so no stale agreement can read as a removal. Agreed rows and rating cursors are
written only while the connection is still bound to the account the run read, so a run
that outlives a re-bind cannot write for the old account. Agreed rows are written under a
share lock on the connection row, so a re-bind waits for them and then clears them.

The row's `provider_item_key` is the provider's own key for the title once a read has
returned one, and Silo's key (`imdb:`, `tmdb:`, or `tvdb:`) before that. Rating writes
carry the same key, so a provider can name the title in later tombstones and writes.

## Provider reads

A read returns rows plus `SnapshotKinds`, the kinds for which the rows are the
provider's complete set. Rows match the catalog by TMDB, IMDb, or TVDB identifier as
[history-import-execution.md](history-import-execution.md) describes; several rows for
one item resolve to the newest. An explicit tombstone removes the rating whose agreed
row carries the same provider key.

An item missing from a read counts as removed on the provider only when all hold:

- its kind is in `SnapshotKinds`;
- no row in the read shares one of its identifiers within the same kind, so a row the
  matcher could not place, or a row with an out-of-range rating, never looks like a
  removal;
- its agreed row is `remote_seen`.

A missing item whose rating was sent but never seen is sent again instead of removed
locally. A read row with no IMDb, TMDB, or TVDB id could be any title, so its kind is
dropped from `SnapshotKinds`. A complete snapshot that returns nothing for a kind while
Silo holds two or more confirmed ratings of that kind is treated as a failed read:
removals for that kind are skipped and the run records a warning. Any other missing item
keeps its agreed value.

Silo reads the profile's ratings in one query, and a rated item the sync skips (no longer
in the catalog, or without external ids) never counts as a local removal.

## Concurrency

Scheduled syncs run on every API node, and the per-connection sync lock is local to a
process. Rating reconciliation of a connection is therefore serialized across nodes by
a PostgreSQL advisory lock keyed by the connection (`WithRatingSyncLock`), held from the
local read through the provider read to the last agreed-row write. Overlapping runs
would otherwise merge from their own reads, and an older run could leave an agreed
rating older than the provider holds, which a later removal would then lose to. A
scheduled or manual run that finds the lock held skips ratings with a warning; a local
rating event, an account switch, and a disconnect wait for it. The lock lives on a
database session opened outside the pool, so the work it guards keeps the whole pool.
Each node admits one caller per connection and at most four lock sessions, never more
than its pool size. A node that dies releases the lock with its session.

Imports use compare-and-set
writes (`RatingsRepo.SetIfUnchanged` and `DeleteIfUnchanged`) against the local value
the sync read, so a concurrent user edit wins and is reconsidered next run. Imported
ratings keep the provider's rating time. Provider writes are idempotent desired-state
writes. A stale agreed row heals on the next run, because equal values on both sides
simply record agreement again. Read cursors are skip hints only. They are saved in
send-only mode too, so a cursor-gated provider does not re-read everything each run, and
turning import on forces one full read so no change skipped while sending only is lost.

## Local changes

`RatingsHandler.SetRating` and `DeleteRating`, the seam shared by v1 and v2, dispatch
a `LocalRatingEvent` that names the changed items but carries no values. The handler
re-reads the current rating and sends a new or changed rating right away. A removal waits
for the next scheduled merge, which can see whether the provider changed the rating in
the meantime; a rating beats a removal. Imports write through the ratings repository
directly and never dispatch events, so an imported rating is not echoed back. Imports
mark the profile's recommendations stale once per run.

## Provider-specific rules

A provider that rates only some kinds implements `RatingKindFilter`; items of other
kinds are left out of its sync entirely, so they are neither sent nor read as removed.
Plugin providers rate the kinds they list in `supported_media_types`. Removing an
absent rating must answer `APPLIED` or `NO_CHANGE`, so a plugin's `REJECTED` answer to
a rating removal counts as a failure and the removal is retried on the next run. A
complete plugin snapshot with an unreadable rating row covers no kind, because the
row's kind is unknown, and the run records a warning.

A provider that records a rated title as watched implements `RatingExportWatchGate`.
Silo then sends a new rating of that kind only once the profile has a completed play of
the title; until then the rating stays pending and each run records a warning. A title
the provider already holds a rating for is not held back, and neither are removals.

## Identity changes

Re-binding a connection to a different provider account drops its rating read cursors,
whose keys contain `.ratings`, and, once the new binding is saved, clears agreed ratings
of every other account. Whenever ratings move to
another media item (a duplicate merge or a reattribution), the agreed rows move with them;
the destination's row wins a collision. A moved row is unconfirmed and forgets its
provider key: for the same title the next read confirms it again, and for a different
title the rating is sent for that title. A move therefore never reads as a removal.
