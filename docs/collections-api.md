# Personal collections API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. The frozen alpha `/api/v1` surface answers the same features through the pre-1.0 bridge
> window and is then retired. See [the native API contract](architecture/api-contract.md).

A personal collection belongs to the profile that created it (`creator_profile_id`). Collections
for the whole server are admin collections and are not covered here. Paths below omit the
`/api/v2` prefix.

## Who sees a collection

- A private collection (`is_shared: false`) is visible only to its creator.
- A shared collection (`is_shared: true`) is visible, read-only, to every profile on the same
  login, including profiles created later. The server decides this when it reads the
  collection; there is no per-profile list.
- Nobody on another login ever sees a personal collection.

A collection the profile cannot see answers `404 not_found` on every operation, reads and writes
alike.

## Who changes a collection

Only the creator changes a collection: its definition, members, artwork, sharing, sync, order
and deletion. Another profile's shared collection answers `403 permission_denied` to every
mutation, including `syncCollection`. The web client shows no management actions on collections
the profile does not own.

## Sharing

`is_shared` is the one sharing switch, on `createCollection`, `updateCollection` and the import
operations (`importMDBListCollection`, `importTMDBCollection`, `importTMDBListCollection`,
`importTraktCollection`). Clients label it **Show to other profiles**.

`include_in_server_collections` is independent: it places the collection in the library
Collections tab (`listLibraryUserCollections`, `getLibraryCollections`) of every profile that can
see the collection. Cards there carry `creator_profile_id`, so clients can label another
profile's collection "by *Name*".

## What a viewer sees in a shared collection

The titles a profile sees are the collection's members limited to what the creator can access,
then limited again to what the viewer can access. Access means allowed libraries and rating
limits. A library the creator only hides from its own browsing does not count as a limit. The
creator's access is checked on every read. When it shrinks, titles disappear for every viewer
at once; stored manual members are hidden, not deleted, and return with the access. This applies
to `item_count`, item pages, the catalog `user_collection` source, home rows and the library
Collections tab. It never changes what a viewer can open elsewhere.

Smart rules on watch state, favorites and the watchlist, and display filters, are evaluated for
the viewer. A shared collection never reveals its creator's activity.

## Posters

A collection's poster is an image its creator uploaded (`uploadCollectionPoster`), linked
(`poster_source_url`) or imported with a list, or otherwise a collage of its first titles'
posters, composed by the server. The collage follows the viewer: it shows the first titles
(up to four) that the viewer sees in the collection by the rules above, so another profile's
collage of a shared collection never shows a title that profile can't open. Members keep their
stored order; a smart collection uses its first matches in its query order. The display filter
does not narrow the collage.

The server fetches a linked poster only from a public internet address. A URL on the server's
own network (loopback, a private or link-local range, or a name that resolves to one), including
one reached through a redirect, fails the poster with one fixed error. See
[Outbound address guard](architecture/outbound-address-guard.md#collection-artwork).

- `listCollections`, `getLibraryCollections` and `listLibraryUserCollections` return the
  collage in `poster_url` and mark it with `poster_is_collage: true`.
- `getCollection` and `updateCollection` carry no `poster_url` for any poster, uploaded,
  imported or collage, and `poster_is_collage` is false there: their body is the editor state
  behind a strong `ETag`, and a presigned URL expires. Clients take the poster from the list.
  `createCollection`, the imports and `uploadCollectionPoster` return an uploaded or imported
  poster only; a new collection's collage is built after they answer.
- The server builds collages in the background, after a collection is created, edited or
  synced, or its poster removed, and when a read finds one missing. Until then, and when no
  title the viewer sees has a poster, `poster_url` is empty and clients show their
  placeholder.
- `getCollectionCapabilities` reports `poster_collages: true` when the server shows collages.
  It is false when the server has no artwork storage (and then `artwork` is false too), and for
  an account on the SQLite user store.
- `deleteCollectionImage` removes an uploaded poster; the collection then shows its collage.

The `/api/v1` bridge does not show collages: its personal collection and library tab reads keep
returning only uploaded and imported posters.

## Listing and order

`listCollections` returns the acting profile's own collections in its order, then the shared
collections of other profiles on the login, grouped by `creator_profile_id`, each in its
creator's order. Clients split the list with `creator_profile_id` and label shared collections
with the creator's name from the login's profile list. The web client orders the **Shared with
me** owners by the login's profile-list order.

Each profile has one flat order of its own collections (`sort_order`). `getCollectionOrder`
returns it with a strong ETag, and `reorderCollections` replaces it under `If-Match`.
`ordered_ids` must name each of the acting profile's own collections exactly once. Any other ID,
including another profile's shared collection, is a `422 validation_failed` at
`body.ordered_ids`. The server stores no per-viewer order of shared collections.

## Capabilities

`getCollectionCapabilities` reports:

- `login_sharing: true`: the model above. A client shows **Shared with me** and the single
  sharing switch only when it is present and true.
- `poster_collages`: collections without an uploaded or imported poster show a collage (see
  [Posters](#posters)).
- `groups: false`: personal collection groups are no longer supported.

## Groups (being removed)

Personal collection groups were removed by #1615. Until native clients stop decoding them, the
contract keeps their members with fixed values:

- `listCollections` returns `groups: []`, and every collection has `group_id: null`.
- `createCollectionGroup`, `getCollectionGroup`, `updateCollectionGroup`,
  `deleteCollectionGroup`, `getCollectionGroupsOrder` and `reorderCollectionGroups` answer
  `501 capability_unsupported`, as they already did for an account on the SQLite user store.
- Setting `group_id` to a group in `updateCollection` answers `501 capability_unsupported`; a
  null or blank `group_id` is accepted and changes nothing, since no collection is in a group.
- `group_id` on `reorderCollections` and `getCollectionOrder` must be omitted (or null in the
  body); a value is a `422 validation_failed`.

A later change removes these operations and members from `/api/v2` before the 1.0 lock.

## `/api/v1` bridge

The frozen v1 routes share the same rules. `GET /api/v1/collections` returns own and shared
collections; `groups` is always `[]` and `group_id` always `null`. `allowed_profile_ids` reports
the effective audience (the creator alone, or every profile on the login) and is ignored on
input. `PUT /api/v1/collections/order` accepts only a null or absent `group_id`, and its
`ordered_ids` names only the acting profile's own collections. The v1 group routes answer `501`. See the [v1 removals table](architecture/v1-scope.md#breaking-removals-taken-before-lock).
