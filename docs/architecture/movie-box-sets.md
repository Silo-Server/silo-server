# Movie box sets

A library with `box_sets_enabled` browses the movies TMDB lists in one
collection as one box set card. This note records the invariants; the wire
contract is in [catalog-api.md](../catalog-api.md#movie-box-sets).

## Data

- `media_item_tmdb_collections` holds the result of looking up one movie:
  the `tmdb_id` it was looked up under (`checked_tmdb_id`) and its collection,
  or NULL for none. A row whose `checked_tmdb_id` no longer matches the movie's
  `tmdb_id` is stale: every read ignores it and the sync looks the movie up
  again.
- `tmdb_movie_collections` holds each collection's name, overview, artwork,
  and `member_tmdb_ids` in release order. Artwork columns follow
  `media_items`: `*_source_path` is the TMDB URL and `*_path` is the cached
  copy's original key, or the TMDB URL until the sync caches it. The columns
  are registered in `artworkSweepSurfaces` and carry the artwork revision GC
  triggers, so the storage sweep keeps their objects and a replaced image is
  collected. TMDB returns a collection's
  parts in no reliable order, so the sync sorts them by release date, with
  undated (announced) parts last.
- The stored name is TMDB's. Reads show it through `catalog.BoxSetTitle`
  ("… Collection" becomes "… Box Set"), and search matches the same rule in
  SQL (`boxSetTitleSQL`); change the two together.

## Sync

The `sync_movie_box_sets` task (`internal/boxsets`) is the only code that calls
TMDB for box sets; browsing and search read stored rows only. It looks up
movies of box set libraries that were never looked up, were rematched, or were
last looked up 30 days ago, then fetches collections that are new or were
fetched 7 days ago, then caches collection artwork still served from TMDB
through the item image cacher, under the same `metadata.cache_images` switch
as item artwork. It runs at startup, hourly, when a library turns box sets
on, and 20 seconds after the node that matched a movie publishes its catalog
change. A run stops after 25 failed TMDB calls; the next run resumes.

## Grouping

A box set browse first lists the library's box sets as the viewer sees them
(`CatalogResolver.BoxSetGroups`): collections with at least
`box_set_min_movies` movies in that library that pass the viewer's access
filter. The browse filters do not change which collections qualify. The
qualifying collection IDs then join the work-grouping plan
(`previewPagePlan.groupedByWork`), which keeps the first row of each group in
the requested order before paging, so totals, cursors, seeks, and the letter
index all count the grouped list. The API layer replaces each representative
movie with its box set card.

The qualifying collections are recomputed on every page, not carried in the
browse cursor. A sync that stores or drops a collection while a viewer is
scrolling can group earlier and later pages differently until the browse is
reloaded; the window is the sync's run, which is short after the first one.

## Movie page

`CatalogResolver.MovieBoxSet` picks the box set a movie's page shows: the
movie's current collection in the first of its libraries with box sets on,
using the same viewer filter and minimum as browse. The movie itself must be
one of the members the viewer sees, so a movie outside the viewer's access
never reveals its box set.
