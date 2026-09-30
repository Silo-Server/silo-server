# Collection posters

A server collection's poster is either assigned or generated. This page covers the rules for
generated posters, which differ by viewer, and what code that returns a collection poster must
do.

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
so a collage never shows a title the viewer's member list leaves out. A collection the catalog
lists from a query (a smart collection, or any collection with a query definition) has no
collage.

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
  server collections, and admin responses.
- Jellyfin BoxSets resolve posters for the session's viewer. A collage's image tag carries its
  key and a signature (32 hex digits), so the image route can serve the tagged collage to a
  request without a session. Collage URLs are never seeded into the shared compat image cache.

Collection list responses differ by viewer. None of them may be cached across profiles.
