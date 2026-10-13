# Collection posters

A collection's poster is either assigned or generated. This page covers the rules for generated
posters, which differ by viewer, and what code that returns a collection poster must do. Server
collections and personal collections share the machinery (`internal/catalog/collection_collages.go`);
[Personal collections](#personal-collections) lists where they differ.

## Assigned posters

An uploaded poster, or the poster of the template a collection came from, is stored in
`library_collections.poster_url` and shown to every viewer. `poster_url` holds nothing else.
A row with `poster_auto_generated` set is a shared collage stored by a server older than this
design; it is never served.

## Generated posters

A collection without an assigned poster shows a collage of its members'
posters. Each viewer sees the first four members it can access that have a poster, in
collection order. Access uses the same predicates as the collection's member list
(`itemAccessConditions`: library allow and deny lists, maturity limits, excluded media types),
so a collage never shows a title the viewer's member list leaves out. A smart collection lists
its members from its query and has no collage. Only `collection_type` makes a collection live: a
query definition on any other type is ignored, and its members are its stored items
(`catalog.ResolveLibraryCollectionMembership`).

Each distinct set of source posters is one stored collage, shared by every viewer that selects
the same posters:

- `library_collection_poster_variants` holds one row per collection and collage key. The key
  (`catalog.CollectionCollageKey`) hashes the source poster paths and a layout version, so a
  change to the members, their order, or their artwork selects a new collage instead of
  serving a stale one. Bump `collectionCollageLayout` when the composition changes.
- Objects live at `collection-images/{collection}/collage/{variant}.{key}.webp`. Every node
  that builds the same collage writes the same keys, so concurrent builds are harmless.
- A read that finds no collage returns no poster (clients show their placeholder) and queues a
  background build. The build is deduplicated per node, bounded, and backs off after a failure.
  A build lost with its node is retried by the next read. A source poster that doesn't resolve or
  download fails the build, so a collage is never stored without one of the posters its key names.
- A sync, template bundle apply, or poster removal builds the unrestricted viewer's collage up
  front, so the common case is ready before anyone asks.
- Listing a collection or serving its collage through a Jellyfin image tag touches the
  collage's `last_used_at` at most daily. Building any collage of a collection deletes that
  collection's collages unused for a week. Rows also go with their collection
  (`ON DELETE CASCADE`).
- Collages are never deleted from storage directly. The table's delete trigger queues a deleted
  row's objects in `artwork_revision_gc_candidates` in the same transaction
  (`queue_collection_poster_objects`), and the artwork revision collector deletes them after its
  grace period while no row names the path. Collage paths are deterministic, so a build first
  reserves its path in that queue, past the build, and saving the row releases it in the same
  transaction. A build that fails or dies anywhere in between leaves its objects to the
  collector. A build waits for a later read while a collector worker holds its path. Deleting a
  collection also deletes its whole `collection-images/` prefix right away.
- The artwork reconcile sweeps the table like other artwork surfaces. A cleared row reads as a
  missing collage and is rebuilt on the next read.

## Returning a poster

`catalog.LibraryCollectionService.CollectionPosters` is the only way to answer "which poster
does this viewer see". Never build a viewer-facing response from `poster_url` alone:

- The v1 and v2 handlers build responses from `withViewerPosters` copies
  (`internal/api/handlers/library_collections.go`). That covers the library Collections tab,
  server collections, and admin responses. `/api/v2` collections and collection cards mark a
  collage with `poster_is_collage`, so a client can tell it from an uploaded poster.
- Jellyfin BoxSets resolve posters for the session's viewer. A collage's image tag carries its
  key and a signature (32 hex digits), so the image route can serve the tagged collage to a
  request without a session. Collage URLs are never seeded into the shared compat image cache.

Collection list responses differ by viewer. None of them may be cached across profiles.

## Personal collections

A personal collection's assigned poster is the one its creator uploaded, linked or imported,
stored in `user_personal_collections.poster_url`. Without one it shows a collage, by the rules
above with these differences:

- The viewer's filter is `catalog.PersonalCollectionFilter`: for another profile's shared
  collection, the viewer's access intersected with the owner's, resolved on every read. Sources
  are the first members with a poster in stored order (`position`, then item ID), with the same
  predicates as the collection's item count and pages, manga chapters excluded. A smart
  collection's sources are the first matches of its query, read like its item pages, so a smart
  personal collection has a collage. The display filter is not applied.
- A list read never runs a smart collection's query. A background refresh reads its matches,
  builds their collage, and records it in `user_personal_collection_smart_collages`, keyed by
  collection and a hash of the viewer's access filter, with a hash of the query definition it was
  made for (no collage when the viewer can see no match with a poster). A read serves that
  record with one statement for all of a list's smart collections. It queues a refresh when the
  record is missing or names another definition (serving nothing until then), or is older than
  `SmartRefreshInterval` (15 minutes; serving the recorded collage meanwhile), so a new match
  reaches the collage within that time. A record goes with its collection, and with its collage
  when that collage is retired as unused.
- `user_personal_collection_poster_variants` holds the collages, keyed by account, collection
  and collage key, and goes with its collection or account (`ON DELETE CASCADE`). Its delete
  trigger is the same `queue_deleted_collection_collage`.
- Objects live at `user-collection-images/{collection}/collage/{variant}.{key}.webp`, beside
  uploaded posters.
- `catalog.PersonalCollectionCollages.Posters` is the read path. Callers group collections by
  owner, resolve each owner once, and pass only collections without an assigned poster
  (`ownerScopedCollectionReads` in `internal/api/handlers`).
- A create, edit, membership change, poster removal or sync calls `Refresh`, which waits about
  two seconds so a burst of changes to one collection shares one build, then builds the acting
  profile's collage (the owner's filter for a scheduled sync) through the build queue.
- Only `/api/v2` reads show collages. The `/api/v1` bridge is frozen and keeps returning
  assigned posters only. Accounts on the per-user SQLite store keep their collections outside
  Postgres and have no collage.
- Jellyfin clients never see personal collections, so jellycompat has no personal collage path.
