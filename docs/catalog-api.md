# Catalog API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. The frozen alpha `/api/v1` surface answers the same features through the pre-1.0 bridge
> window and is then retired. See [the native API contract](architecture/api-contract.md).

## People search

`GET /api/v2/catalog/people` (`listPeople`) accepts a name fragment in `q` and
`limit` from 1 to 100 (default 20). Case-insensitive exact name matches come first;
other matches sort by name, with person ID breaking ties. Ranking happens before
applying the limit.

The optional `media_scope` parameter limits results to people credited on items
in that scope. It accepts `video` (movies and series), `movie`, `series`, `episode`,
`audiobook`, `ebook`, or `manga`. Omit it to search credits across all media scopes.
Results require at least one credit visible to the viewer. Library restrictions, disabled libraries, rating
limits, and excluded media types apply before the limit, including when the media
scope is omitted. Every credit role participates, so directors match video searches
and authors and narrators match audiobook searches. A person with several matching
credits appears once.

Episode credits inherit library visibility and rating limits from their parent
series. Media scope and excluded media types still apply to the credited episode.

`GET /api/v2/catalog/search/capabilities` advertises `people_media_scope: true`
when people search supports media scopes and viewer access filtering. Clients
must check this signal before offering people results, including unscoped
searches. Web omits the People row on older servers that do not advertise support.

Web search applies its selected media scope to both titles and people. People
responses remain an `{items}` collection with string IDs. The v1 bridge retains
its existing alphabetical, unscoped search.

## Person detail

`GET /api/v2/catalog/people/{id}` (`getPerson`) returns one person. A read counts
as a view: when the person's metadata is incomplete or stale and no provider lookup
ran recently, the server queues a background refresh.

Clients that warm a cache speculatively, such as web prefetching the cast of an
open item, pass `prefetch=true`. A prefetch returns the same person but does not
queue a refresh; missing metadata is left to the server's background sweep. Read
the person without `prefetch` when the user actually opens them.

`GET /api/v2/catalog/search/capabilities` advertises `person_prefetch: true` when
the server accepts the parameter. Check it first: `/api/v2` rejects unknown query
parameters, so an older server answers a prefetch read with `422`.

## Saved browse sort

`PUT /api/v2/collections/sort-preference` (`setCollectionSortPreference`) saves the
acting profile's sort for a library collection, user collection, Watchlist, or
Favorites. The profile is identified by the `X-Profile-Id` header. The request body
is a `CollectionSortPreference`:

```json
{
  "collection_kind": "watchlist",
  "field": "added_at",
  "order": "desc"
}
```

A successful save returns `200` with the stored `CollectionSortPreference`.

`collection_kind` accepts `library`, `user`, `watchlist`, or `favorites`.
`collection_id` is a string and is required for collection kinds; it is omitted or
ignored for Watchlist and Favorites. Saved personal-list preferences accept
non-personalized sort fields; `added_at` means the date the item was added to the
list. Personalized sorts (`progress`, `date_viewed`, and `plays`) are rejected for
both saved preferences and Favorites/Watchlist browse. History accepts
`date_viewed` with an active profile, but rejects mutable `progress` and `plays`
sorts. An empty `field` pins the profile to list source order.

`DELETE /api/v2/collections/sort-preference?collection_kind=watchlist`
(`clearCollectionSortPreference`) removes the saved preference and returns `204`.
Collection kinds also require `collection_id` on DELETE.

When a catalog request has no explicit sort, its saved preference is applied
before the source default. `GET /api/v2/catalog` reports an applied saved/default
sort as `effective_sort`; source order omits that field. `effective_sort` is
reported the same way for `group=work` requests, and `sort_metrics` on each item
describes the effective sort rather than the (possibly empty) requested one.

## Feature detection

`GET /api/v2/collections/capabilities` (`getCollectionCapabilities`) returns a
`CollectionCapabilities` document whose `sort_preference_kinds` lists the
`collection_kind` values this server accepts:

```json
{ "sort_preference_kinds": ["library", "user", "watchlist", "favorites"] }
```

Check it before saving a Watchlist or Favorites preference. The
`collection_sort_preferences` boolean reports only that saved preferences exist at
all, so it cannot be used to detect the personal-list kinds. The document supports
`If-None-Match` and returns `304` when the caller's copy is current.

## Library-scoped version lists

`library_id` on `getCatalogItem`, `listCatalogItemVersions`, `listCatalogItemEpisodes`,
`listSeriesSeasons`, `getSeriesSeason`, and `listSeasonEpisodes` names the library the
viewer opened the item from. It always validates that the item is a member of that
library and picks the library's metadata language as the presentation fallback. It
does not, by default, change which files come back: an item stored in several
libraries lists every version the viewer may access, so a "Movies" and "Movies 4K"
split still shows both files from either library.

The administrator setting `catalog.scope_versions_to_library` (default `false`)
makes those reads answer only the versions stored in the named library. A read
without `library_id` is unaffected, and so is `getWatchDetail`, watch-together
selection, and the Jellyfin compatibility surface: an item always plays from its
full accessible version list. No client change is needed: the setting only
changes what an existing `library_id` request returns.

## Section quality badges

Home and library section cards derive `overlay_summary` from the best accessible,
non-missing media file: resolution first, then dynamic range. A series includes
all its eligible episode files even when an episode also appears on the page.
Library restrictions and the playback quality ceiling apply before selecting the
file, so a restricted profile's badge describes a file that profile can access.

Each section response reads current committed file metadata. Badge summaries have
no result cache: a subsequent request sees file updates, removals, and library
moves. Clients must fetch again to update their existing cards.

Recently-added section membership is shared only within the same library and
access scope. Scan-complete events are coalesced into invalidations at most once
per 30 seconds; invalidation requests a refresh on the next read. While
one background rebuild runs, readers may use the previous membership for at most
30 seconds from the first read after invalidation, capped by its original expiry.
An idle scope retains that original expiry until a reader requests the refresh. Repeated scans and failed refreshes
cannot extend that deadline. Cold or expired membership requires a fresh build;
an older in-flight build cannot replace the current generation. Badge summaries
and per-profile playability are recomputed during this grace period.

## Bridge note

The alpha `/api/v1` surface exposes the same saved-sort and capability features at
`/api/v1/collections/sort-preference`, `/api/v1/collections/capabilities`, and
`/api/v1/catalog`, with numeric `collection_id` values instead of strings. Those
paths are frozen: no feature work lands on them, and Silo 1.0 answers the whole
`/api/v1` namespace with `410 Gone` and the `client_upgrade_required` problem code.
Build against `/api/v2`.

## Personal-list pagination

`GET /api/v2/favorites` and `GET /api/v2/watchlist` use opaque cursors over descending
`added_at`, then descending item ID. The cursor retains the database timestamp's full precision;
clients must send it unchanged rather than construct it from visible timestamps. PostgreSQL orders
by the stored timestamp column so the existing profile/time indexes can serve the page. Visible
`added_at` fields remain UTC timestamps with millisecond precision. The frozen v1 list queries and their timestamp
formatting are unchanged.

## Catalog query windows

`POST /api/v2/catalog/query` is the structured-body form of `GET /api/v2/catalog`.
It accepts the browse source identifiers, `q`, `name_prefix`, `type`, rule
`groups` and `match`, `sort`/`order`, a page `limit` up to 100 (GET allows 200), and an optional
`query_limit` for the complete result traversal. GET accepts the same rule groups
as a JSON array in `groups` and expresses descending sort as `sort=-field`.
Unknown rule fields and unsupported operators return `422`.

Both operations return shared catalog cards, `page.next_cursor`, `page.has_more`,
`total`, `total_exact`, and `window_cursor`. Send `next_cursor` unchanged for the
next page. A virtualized client can retain `window_cursor` and send it with
`seek`, a zero-based result position, to request a distant window or return to
position zero. A seek locates one SQL ordering boundary; it can scan the sorted
prefix and does not have constant cost. The complete browse request has a
10-second deadline and honors client cancellation. Keep only visible and
overscan pages active, and cancel requests when the query changes.

Cursors are bound to the operation, viewer/access policy, filters, page size,
query cap, and requested sort. Changing these inputs starts a new traversal.
`skip_total` may change between windows without invalidating the cursor. The
cursor retains a resolved saved sort so later pages do not reread a changed
preference. A nonexact total is an estimate or lower bound, not a verified final result count.

SQL query continuation retains the complete typed ordering tuple, including the
unique item identity and explicit null ordering. Page rows and an optional count
share one PostgreSQL snapshot. Later pages read live data: an insertion cutoff
excludes newer catalog arrivals where supported, but does not freeze titles,
ratings, progress, visibility, or other mutable sort/filter values. Clients must
not treat a cursor as a frozen catalog export.

Collection-source cursors additionally retain the selected collection's durable
revision. Authoritative revision reads bracket parent/access resolution and page
construction; a committed definition, membership, or order change invalidates the
result. A changed collection returns `400` `invalid_cursor`; restart the query.
These checks do not invalidate a collection when unrelated catalog data changes.

PostgreSQL-dependent viewer predicates require the selected user-store provider
to expose its authoritative SQL state. Unsupported SQLite query combinations
return `501` `capability_unsupported`, rather than silently reading unrelated
PostgreSQL viewer rows. SQLite manual collection source-order paging remains
supported; arbitrary manual sorting, nonzero manual seeks, and personalized SQL
filters are unsupported during storage consolidation.

Recent-TV continuation compares the final event timestamp, target type, target
identity, and event identity after event grouping. Recently-added, released, and
random sections retain their source ordering; random sections retain a seed in
the cursor. Audiobook author/narrator/series groups compare their normalized group
identity after any count or duration sort. Work grouping chooses the first
accessible ebook/audiobook edition under the complete source order before applying
the group cursor. A query cap limits source editions before grouping.

### Search continuation

Text searches with a nonempty `q` and the default `query` source accept explicit
`relevance` sorting, including structured requests with rule groups. Other
sources and saved collection definitions reject `relevance`; it describes a
text query's ranking rather than a persistent collection order.

`GET /api/v2/catalog/search/capabilities` reports the selected provider and, for
Meilisearch, `result_window_limit`, `session_ttl_seconds`, and
`max_sessions_per_account`. Catalog query bodies default to 50 results per page. Search
responses also expose the applicable window limit and fixed session expiry in
`search_diagnostics`.

PostgreSQL search retains the complete relevance tuple or requested SQL sort
rather than a numeric page. It selects the FTS or bounded fuzzy retrieval family
on the first page and retains that choice. The existing fuzzy candidate cap and
reranking remain in force. A fuzzy-family query that becomes a richer FTS query
returns `invalid_cursor` so the client can restart. PostgreSQL search retains its
three-second deadline inside the overall browse deadline.

Meilisearch captures the configured ranked result window in one provider response,
then stores its filtered, ordered candidate IDs in shared Redis for 15 minutes.
The configured window must be between 1 and 1,000 candidates; a larger runtime
index setting returns `capability_unsupported` before serving a partial ranking.
This is the provider's reachable window, not an exact global match count.
At most 16 ranking sessions are retained per account; starting another discards
the oldest retained session. Session requests do not extend expiry.

The retained ranking binds the query, provider configuration, account/profile,
and access scope. Pages reauthorize each candidate and advance past deleted or
inaccessible IDs. Explicit window seeks count visible rows within this bounded
ranking. Metadata and access remain live; the retained IDs and their order are
immutable. Fallback may select PostgreSQL before the first page, but a continuation
never switches providers. Expiry or Redis eviction returns `invalid_cursor`;
Redis failure returns `dependency_unavailable`. Restarting performs a new search.

## History ordering

History defaults to chronological watch-event order. Explicit `date_viewed`
sorting uses each displayed item's latest visible history event, including
episode events collapsed into their parent series; it does not require a
completed watch. `order=asc` puts the oldest latest watch first, and `desc`
puts the newest first. Library/media-scope/search overlays retain this order
before pagination. History does not currently support saved sort preferences.

## Season-list artwork

`GET /api/v2/images/capabilities` advertises
`"season_list_artwork_param": "include_artwork"`. On
`GET /api/v2/catalog/series/{id}/seasons`, this optional boolean defaults to
`true`: omitted and `true` retain the usual artwork. `false` skips poster
preparation and omits `poster_url` and `poster_thumbhash` from each season,
while preserving metadata, viewer rollups, play targets and the `items` envelope.
Invalid booleans return `422 validation_failed`. The parameter does not apply
to single-season or episode operations. Clients can use the capability to
select text-only season lists; callers that omit it keep their existing behavior.

## Collection membership titles

`GET /api/v2/collections/{id}/items` and
`GET /api/v2/admin/collections/{id}/items` include an optional `title` on each
membership row when its catalog title is available. Editors can display that
title while retaining `media_item_id` for mutations and ordering. Clients should
fall back to the ID when the title is absent. Personal membership pages hydrate
titles through the existing viewer access filter; admin pages require acting
administrator access. Membership identity, ordering and cursor revision checks
are unchanged. Frozen v1 membership responses do not expose this field.

## Advisory age

Catalog items may carry `advisory_age`, a recommended minimum viewer age from an
advisory service such as Common Sense Media, and `advisory_source`, which names
who recommended it (`commonsense` or `mdblist`). Both are optional and appear
only together; an item with no advisory omits both.

**The advisory is display only and clients must treat it that way.** It is not a
certification and it never restricts anything. `content_rating` remains the
certification a rating body issued, and it alone drives the server's
content-rating ceiling, so a profile's access is identical whether or not an
advisory is present. Do not filter, gate, or hide content on `advisory_age`, and
do not present it as a rating a viewer has to satisfy.

Coverage is partial by design. The providers that supply advisory ages are rate
limited per day, so on a large library some titles carry one and others do not,
and the set grows over time. Absence means "not fetched yet", never "suitable
for everyone". That unpredictability is exactly why the value is kept out of
parental controls: a ceiling that tightened or loosened based on which titles a
provider happened to reach that day would be impossible for an administrator to
predict or reproduce.

Whether to show the badge is a per-profile choice, `catalog.show_advisory_age`
in the settings contract, default off. The field is served regardless; the
setting decides whether a client renders it. Detect support by reading the
setting from the settings contract capabilities rather than sniffing versions.

Frozen v1 responses do not expose these fields.
