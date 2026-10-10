# Catalog API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. The frozen alpha `/api/v1` surface answers the same features through the pre-1.0 bridge
> window and is then retired. See [the native API contract](architecture/api-contract.md).

## Section browsing

Catalog reads with `source=section` resolve the row on the acting profile's
`home` or `library` page. This includes personal rows and the profile's overrides
to server rows. Paging uses that effective row's configuration. A row hidden or
removed by the profile, a row belonging to another profile, or a row in an
inaccessible library answers `404`, as an unknown section does.

## People search

`GET /api/v2/catalog/people` (`listPeople`) accepts a name query in `q` and
`limit` from 1 to 100 (default 20). Each whitespace-separated word of `q` must
start a word of the person's name, case-insensitively and in any order. A word
starts the name or follows a character that is not a letter or digit, so `hacks`
matches "Lark Hackshaw" but not "Chad Thackston", and `luc` matches "Jean-Luc".
A partial last word still matches, which keeps typeahead working. A name
written without separators, as many Chinese, Japanese, and Korean names are,
matches only from its start. Only the first eight distinct words count, and a
repeated word counts once. `%` and `_` are literal. Case-insensitive exact name
matches come first;
other matches sort by name, with person ID breaking ties. Ranking happens before
applying the limit.

The optional `media_scope` parameter limits results to people credited on items
in that scope. It accepts `video` (movies and series), `video_with_episodes` (movies,
series, and episodes), `movie`, `series`, `episode`, `audiobook`, `ebook`, or `manga`.
Omit it to search credits across all media scopes.
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

Person detail and `POST /api/v2/catalog/people/{id}/refresh` (`refreshPerson`)
apply the same visibility rule as a v2 people search without `media_scope`: the
viewer must be able to see at least one of the person's credits. Otherwise both
answer `404`, exactly as for an unknown ID, so these routes do not return the
name, biography, or photo of someone who appears only in titles the viewer cannot
see. The v1 bridge routes `GET /api/v1/people/{id}` and
`POST /api/v1/people/{id}/refresh`, and the Jellyfin-compatible `GET /Items/{id}`
for a person, follow the same rule. The v1 bridge search `GET /api/v1/people?q=`
lists only people the viewer can see this way and keeps its alphabetical order
without exact-name ranking. The admin person routes are not filtered.

Clients that warm a cache speculatively, such as web prefetching the cast of an
open item, pass `prefetch=true`. A prefetch returns the same person but does not
queue a refresh; missing metadata is left to the server's background sweep. Read
the person without `prefetch` when the user actually opens them.

`GET /api/v2/catalog/search/capabilities` advertises `person_prefetch: true` when
the server accepts the parameter. Check it first: `/api/v2` rejects unknown query
parameters, so an older server answers a prefetch read with `422`.

## Facet typeahead

`GET /api/v2/catalog/filters/search` (`searchCatalogFacet`) searches the values of
one facet in a scope. The scope parameters are the ones `getCatalogFilters` takes:
`source`, `library_id`, `type` (media scope: `movie`, `series`, `episode`, and so
on), and the rest. `library_ids` names several libraries, one `library_ids`
parameter per id, and combines with `library_id`. Requested libraries are
intersected with the viewer's allowed libraries and stripped of disabled ones,
so the parameter can narrow a scope but never widen it; a scope left with no
library answers no values. `library_ids` is refused with `source=section`.
`getCatalogFilters` accepts it too.

The answer carries two lists, each at most `limit` long.

`matches` holds the values that start with `q`, ignoring case, in A to Z order,
and is empty for an empty `q`. `has_more` is true when more values than `limit`
start with `q`. This is the answer `matches` has always given, so a client that
reads only `matches` sees no change.

`values` is the ranked answer, each entry with `count`, the number of titles in
the scope that carry the value. `values_has_more` is true when more values
matched than `limit`. For `genre`, `studio`, `network`, `country`,
`original_language`, and `content_rating`, a value matches when it starts with
`q` or when any word in it does; words are split on any character that is not a
letter or digit, so `bros` finds `Warner Bros. Pictures`. Values that start with
`q` come first, then word matches. Within each group, values with more titles
come first, then A to Z. An empty `q` returns the most common values. For
`author`, `narrator`, and `series`, `values` holds the names in `matches`, and an
empty `q` returns nothing.

Matching ignores case, A to Z compares lowercased code points, and `%` and `_`
in `q` match literally.

The server answers the first six facets from an in-memory list of the scope's
values, built with one query and kept for two minutes. The list is keyed by the
scope's full SQL and arguments, so viewers with different library access,
disabled libraries, or rating limits never share one. Each node keeps its own
lists, so `count` and newly added values can lag catalog changes by up to two
minutes. A node holds at most 500,000 values across all lists and drops the
least recently used list first, and builds at most four lists at once. A scope
with more distinct values than that is searched in SQL on every request, with
the same matching and order.

`GET /api/v2/catalog/search/capabilities` advertises `facet_value_search: true`
when the server accepts `library_ids`, answers `values` and `values_has_more`,
matches word starts in `values`, and returns common values for an empty `q`.

`GET /api/v1/catalog/filters/search` is frozen and answers as it always has:
names that start with `q`, read live from the database in `LOWER(name)` order
under the database collation, with `%` and `_` in `q` acting as wildcards. It
returns nothing for an empty `q`.

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

`login_sharing: true` reports that a personal collection is either private to its
creator or shared with every profile on the login, that `listCollections` includes other
profiles' shared collections, and that only the creator changes or orders a collection. Show
**Shared with me** and the single **Show to other profiles** switch only when it is true; see
[the personal collections API](collections-api.md). `groups` is always false.

The document's `import_sources` lists the sources a new imported collection can come
from (`mdblist`, `tmdb`, `tmdb_list`); it is empty when `imports` is false. Check for
`tmdb_list` before calling `importTMDBListCollection` (`POST /api/v2/collections/import/tmdb-list`),
which follows a public TMDB list. The administrator capability document
(`getAdminCollectionCapabilities`) carries the same field for `importAdminTMDBList`.

Both collection capability documents, `getCollectionCapabilities` and
`getAdminCollectionCapabilities`, also report:

- `mdblist_search`: `true` when `searchMDBListLists` and `listTopMDBListLists` return
  lists. It is `false` when the server has no MDBList API key; those operations then answer
  `configured: false` with no lists. Importing a pasted MDBList link does not need the key.
- `schedule_time_zone`: the time zone the answering node runs cron collection schedules in.
  `utc_offset` is the current offset from UTC as `±hh:mm` (for example `-05:00`), daylight
  saving time included. `abbreviation` is the current abbreviation the node's time zone
  database reports (for example `CDT`; some zones report a numeric form such as `-03`).
  `name` is the IANA zone name (for example `America/Chicago`): the zone the node's `TZ`
  environment variable names, or `UTC` when `TZ` is empty or names no known zone. It is
  omitted when the node uses its system default zone or a `TZ` file path. Each node reports its own zone, and the
  offset changes with daylight saving time, so read it with the schedule rather than
  storing it. A cron `sync_schedule` with a `TZ=` or `CRON_TZ=` prefix runs in the zone
  the prefix names instead.

`getCollectionCapabilities` also reports `sync_schedule_editable`, `true` when `updateCollection`
accepts `sync_schedule`; see [Personal sync schedules](#personal-sync-schedules).

`getCollectionCapabilities` also reports `contains_item`, `true` when `listCollections` accepts
`contains_item`; see [Collections that hold a title](#collections-that-hold-a-title).

`getAdminCollectionCapabilities` also reports `section_references`, `true` when
`listAdminCollectionSections` is available and `listAdminCollections` items carry row counts; see
[Rows that show a server collection](#rows-that-show-a-server-collection).

## Personal collection descriptions

`createCollection` (`POST /api/v2/collections`) accepts an optional `description`, stored
with the new collection and returned as `description` on collection reads. Omitting it stores
an empty description; `null` is a validation failure. Check `create_description` in the
`getCollectionCapabilities` document before sending it: a server without that flag rejects the
member as unknown, and an account whose user store does not keep descriptions (the SQLite
store) reports `false` and answers a non-empty `description` with `501 capability_unsupported`.
`updateCollection` changes the description of an existing collection.

The frozen `/api/v1/collections` create ignores a `description` member, in a JSON body and in
the multipart `data` field alike; the collection is created with an empty description.

## Personal smart previews

`previewCollection` (`POST /api/v2/collections/preview`) returns the first titles a smart query
matches within the acting profile's access. Each item carries `poster_url`, a card-size poster URL,
when the title has a poster, and omits the member when it has none. A missing `poster_url` alone
does not show whether the server returns posters: check `preview_posters` in the
`getCollectionCapabilities` document. The URL can be signed and expire, like other artwork URLs,
so read a fresh preview rather than storing it.
`previewAdminCollection` items carry the same field.

The frozen `/api/v1/collections/preview` response is unchanged and carries no poster.

## Personal collection imports

A personal collection imported with `importMDBListCollection`, `importTMDBCollection`,
`importTMDBListCollection`, or `importTraktCollection` holds only titles its owner profile
can access: titles in the owner's allowed libraries that pass its rating limits. Those are
the `max_content_rating` ceiling, with the server's `access.unrated_content` setting for
unrated titles, and the `max_advisory_age` limit, with `require_advisory_age` (see
[Advisory-age limit](#advisory-age-limit)). The owner's hidden-library preference does not count;
it changes what the owner browses, not what the owner may access. Every sync applies this,
whether it runs on import, through `syncCollection`, or on the collection's schedule.

- The item limit fills with titles the owner can access. The sync checks every source entry
  it reads before it applies the limit, so titles outside the owner's access never take a
  slot. With an item limit set, the sync reads at most four times the limit from the source
  (100 to 500 entries), so an owner with tight limits can get fewer titles than the limit
  even when the full source list holds enough the owner can access.
- `library_ids` keeps only titles in one of the listed libraries, and each title must still
  be one the owner can access. Omit it to match against everything the owner can access.
- The sync summary counts only titles the owner can access. `items_matched` counts the
  titles kept, and so does the `item_count` the sync stores, which the import response
  returns. An entry the owner cannot access, or one outside `library_ids`, counts in
  `items_unmatched`, like an entry the catalog lacks, so the summary never reveals titles
  outside the owner's access. Collection reads such as `getCollection` and `listCollections`
  report `item_count` as the members the reading profile can see, so the owner's count there
  leaves out titles in libraries the owner hides.
- When the server cannot resolve the owner's access, the sync fails and the collection keeps
  its existing members. The collection records `last_sync_status` `failed` and a
  `last_sync_message` saying it was not updated, so a failed scheduled sync is visible too;
  `syncCollection` answers with an error. An import in this state still creates the
  collection, empty and with a failed first sync the caller can retry.

Each viewer of a shared collection still sees only the titles that viewer can access.

## Personal sync schedules

`updateCollection` (`PATCH /api/v2/collections/{id}`) accepts `sync_schedule` on a synced list
(a collection imported from MDBList, TMDB, or Trakt). The value is a cadence name, `daily`,
`weekly`, or `monthly`, or `""` to stop scheduled syncs; `syncCollection` still syncs on demand.
Cron expressions are a validation failure for every account, administrators included, and so is
`sync_schedule` on a manual or smart collection or `null`. A cadence sets `next_sync_at` to the
schedule's next run; `""` sets `sync_schedule` to `""` and `next_sync_at` to `null`. Only the
collection's creator can change it, as with every other member. Check `sync_schedule_editable`
in the `getCollectionCapabilities` document before sending it: a server without that flag rejects
the member as unknown, and the flag is `false` when `imports` is.

Collection reads carry `sync_cadence`, the cadence `sync_schedule` names: `daily`, `weekly`,
`monthly`, `""` when the collection is not synced, or `custom` for a stored schedule no cadence
name produces. Read it instead of matching cron expressions.

A sync that is running when `sync_schedule` is saved, on any node, records its result but leaves
the `next_sync_at` the save set, even when the save kept the same cadence, and the collection a
sync or import returns shows what was stored. A scheduled sync runs on one node: it first moves
`next_sync_at` past the minimum interval, which is when a failed sync retries; a successful sync
replaces that with the schedule's next run unless the schedule was saved while it ran.

The frozen `/api/v1/collections/{id}` update ignores a `sync_schedule` member, and `/api/v1`
collection responses carry no `sync_cadence`.

## Collections that hold a title

`listCollections` (`GET /api/v2/collections`) accepts an optional `contains_item`, a title's
content id. Each of the acting profile's own manual collections in the response then carries
`contains`: `true` when the collection holds the title, `false` when it does not. No other
collection carries the member: not a smart collection or synced list, and not a collection
another profile shares with the acting profile. Without `contains_item`, no collection carries
it. A title the acting profile cannot access, or one that does not exist, reports `false` on
every collection, even a collection that still stores it. Check `contains_item` in the
`getCollectionCapabilities` document before sending it: a server without that flag rejects the
parameter as unknown.

The frozen `/api/v1/collections` list ignores a `contains_item` parameter, and its collections
carry no `contains`.

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

## Device-scoped playback answers

`getCatalogItem` and `listCatalogItemVersions` accept the optional
`X-Silo-Device-Id` header, as `getWatchState` does. The effective playback
fields (`effective_audio_track_index`, `effective_audio_language`, and the item's
`effective_subtitle_*` fields) resolve the acting profile's preferences for that
device, so a device-scoped override wins over the profile value. Without the
header they resolve the profile's preferences alone. Clients that set
device overrides should send the header so the detail page matches what playback
picks.

## Episode files

Each episode in `listSeasonEpisodes` and `listCatalogItemEpisodes` lists its
accessible files with their quality facts. `unreadable` is present and `true`
on a file the server could not read: ffprobe rejected it as empty, corrupt, or
truncated, and no successful probe exists. Starting playback of that file
falls back to another version of the episode the viewer may play; when there
is none, it answers the terminal reason `source_unreadable` (see
[Playback API](playback-api.md#start)). The field is cleared once the file is
replaced and a scan or playback attempt probes it successfully. The web client
marks an episode only when every one of its files is unreadable, since any
readable version still plays. `/api/v1` episode listings do not carry the
field.

## Section quality badges

Home and library section cards derive `overlay_summary` from the best accessible,
non-missing media file: resolution first, then dynamic range. A series includes
all its eligible episode files even when an episode also appears on the page.
Library restrictions and the playback quality ceiling apply before selecting the
file, so a restricted profile's badge describes a file that profile can access.

Each section response reads current committed file metadata. Badge summaries have
no result cache: a subsequent request sees file updates, removals, and library
moves. Clients must fetch again to update their existing cards.

Recently added TV groups a show's new episodes by arrival time. An episode
first seen within 2 hours of the show's previous arrival joins that arrival's
group, so a chain of imports stays one group even when it spans longer than 2
hours. A group with one episode returns an `episode` card; a larger group
returns the `series` card, ordered by its newest arrival. Grouping ignores
how the files were scanned: one scan per imported episode, as arr webhooks
produce, groups the same way as one library scan. On the home row, the
section's `total_count` is a lower bound: it exceeds `item_limit` when more
cards exist. The catalog view reports the exact count.

Replacing a title's file keeps its added date. A quality upgrade deletes the
old release before importing the new one under a new name, so the title is
briefly absent; if the replacement arrives within the server's file removal
grace (24 hours by default), the title returns with its original added date
and does not reappear at the top of recently added. See
[missing files](architecture/missing-files.md).

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

## Watchlist titles outside the library

The watchlist can also hold movies and series the library doesn't have, keyed by
TMDB ID. They are not catalog items: `GET /api/v2/watchlist`, `GET /api/v2/catalog`
with `source=watchlist`, smart filters and the home Watchlist row never return them.
Read them with `GET /api/v2/watchlist/titles`, which pages by the same kind of opaque
cursor over descending `added_at`, then descending title ID; add and remove them with
`PUT` and `DELETE /api/v2/watchlist/titles/{media_type}/{tmdb_id}`. Check
`watchlist_titles_supported` on `GET /api/v2/requests/status` first. It is false
while requests are off, and the operations then answer `409 capability_disabled`.

When such a title reaches the library, the next watchlist read moves it onto the
library watchlist with its original `added_at`: the watchlist list and entry reads,
a catalog query with `source=watchlist`, the home Watchlist row and an item's
`user_state.in_watchlist`, as well as `GET /api/v2/watchlist/titles` itself. See
[api-contract.md](architecture/api-contract.md#watchlist-titles) and
[External watchlist titles](architecture/external-watchlist.md).

## Catalog query windows

`POST /api/v2/catalog/query` is the structured-body form of `GET /api/v2/catalog`.
It accepts the browse source identifiers, `q`, `name_prefix`, `type`, rule
`groups` and `match`, `sort`/`order`, a page `limit` up to 100 (GET allows 200), and an optional
`query_limit` for the complete result traversal. GET accepts the same rule groups
as a JSON array in `groups` and expresses descending sort as `sort=-field`.
Unknown rule fields and unsupported operators return `422`.

`name_prefix` matches the start of the key title sorting uses: the sort title,
or the title when no sort title is set. "The Hobbit" with sort title
"Hobbit, The" matches `h`, not `t` or `the`. Jellyfin's `NameStartsWith`
follows the same rule. The one exception is recently added TV, which also
matches an episode's own title so episode cards can be found by name.

Both operations return shared catalog cards, `page.next_cursor`, `page.has_more`,
`total`, `total_exact`, and `window_cursor`. Send `next_cursor` unchanged as
`cursor`, without `seek`, for an adjacent page. This reuses the ordering boundary
already returned by the previous page. A virtualized client can retain
`window_cursor` and send it with `seek`, a zero-based result position, to request
a distant window or return to position zero. Use this path when the preceding
page's continuation is unavailable; independent distant windows can load in
parallel without fetching intermediate pages. A seek locates an additional SQL
ordering boundary; it can scan the sorted prefix and does not have constant cost.
The complete browse request has a
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

### Rule fields and operators

Catalog query rule groups, `custom_filter` sections, and Smart collections share
one rule vocabulary (`queryFieldDefs` in `internal/catalog/query_definition.go`).
Each rule is `{field, op, value}`; an operator a field does not list returns `422`,
and so does a value of the wrong shape: a span not like `30d` or reaching back before
PostgreSQL's earliest date (4714 BC), a non-numeric bound,
a decade that is not a year from 10 on (decade 0 would catch every title with no
year), a `title` value that is empty or not a string, or a
`latest_episode_added` or `last_air_date` bound that is not a date (`2024-01-31`)
or an RFC 3339 time.

| Operators | Fields | Value |
| --- | --- | --- |
| `contains`, `not_contains`, `is`, `is_not`, `begins_with`, `ends_with` | `title` | string, compared ignoring case; `%` and `_` match literally |
| `is`, `is_not` | `decade` | the decade's first year (`1990` matches 1990 to 1999) |
| `gt`, `gte`, `lt`, `lte`, `between` | `runtime` (minutes), `rating_imdb`, `rating_tmdb`, `rating_rt_critic`, `rating_rt_audience` | number, or `[min, max]` for `between` |
| `gt`, `lt`, `between`, `in_last`, `not_in_last` | `added_at`, `release_date`, `latest_episode_added`, `last_air_date` | ISO date, `[from, to]`, or a span such as `30d` (`h`, `d`, `w`, `m` for months, `y`) |

`not_in_last` keeps titles whose date falls before the span; a title without the
date matches neither `in_last` nor `not_in_last`, and a title with no runtime
matches no `runtime` bound. `latest_episode_added` and `last_air_date` describe a
show's newest episode, so movies never match them. An episode row never matches
`latest_episode_added`, and in the `episode` scope `last_air_date` is the
episode's own air date. Rules on `rating_rt_critic` and `rating_rt_audience`
still apply when an administrator hides that rating source, so a saved collection
keeps its members; the web editor only stops offering those fields.

The personalized `last_watched` field also takes `not_in_last`. It counts only
finished plays: a title the profile never finished counts as finished long ago,
and a show's last watched date is its most recently finished episode.

`GET /api/v2/catalog/search/capabilities` advertises `extended_query_rules: true`
when the server accepts `title`, `decade`, `runtime`, the TMDB and Rotten
Tomatoes ratings, `latest_episode_added`, `last_air_date`, the partial title
operators, and `not_in_last`. Older servers answer those rules with `422`, so the
web rule editor offers them only while the flag is true. `/api/v1` keeps its frozen
rule vocabulary: its catalog requests (including `POST /api/v1/catalog/query`) and
its section and collection saves and previews refuse those fields and
`not_in_last` with the errors they gave before.

### Search continuation

Text searches with a nonempty `q` and the default `query` source accept explicit
`relevance` sorting, including structured requests with rule groups. Other
sources and saved collection definitions reject `relevance`; it describes a
text query's ranking rather than a persistent collection order.

### Search media scopes

The `type` parameter of `GET /api/v2/catalog` and `POST /api/v2/catalog/query`
names one media scope: `movie`, `series`, `episode`, `audiobook`, `ebook`,
`manga`, or `video` (movies and series). `video` means the same thing in
search, browse, filters, and smart collections.

`video_with_episodes` is a search scope for clients that want everything
watchable without books. With a nonempty `q` on the `query` source, it returns
movies, series, and episodes, ranked together; audiobooks, ebooks, and manga are
excluded. Without `q`, and on `getCatalogFilters` and `searchCatalogFacet`, it
covers the same rows as `video`, because a browse lists catalog items and never
mixes in episode rows (an unscoped browse has no episodes either). Other
sources refuse it with `422` at `query.type` (`body.type` on the structured
form), and saved collection definitions do not accept it. People search accepts
the same value as `media_scope`, so a client can send one scope to both.

`GET /api/v2/catalog/search/capabilities` advertises
`video_with_episodes_scope: true` when the server accepts the scope. Older
servers ignore an unrecognized `type`, which searches every media type, and
reject it as a people `media_scope`. A client that hides books must check the
flag and send `video` when it is absent.

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

## Play target season

A v2 item detail whose `play_content_id` names an episode also carries
`play_season_number`, that episode's season (`0` for specials). A client that
opens a series or season on its play target can request that season's episode
list without fetching the episode first. The season comes from the same query
that chose the target. The field is absent when there is no play target or the
target is not an episode; a client that finds it absent fetches the episode as
before. Cards do not carry it, and the frozen v1 detail does not expose it.

## Collection membership titles

`GET /api/v2/collections/{id}/items` and
`GET /api/v2/admin/collections/{id}/items` include an optional `title` on each
membership row when its catalog title is available. Editors can display that
title while retaining `media_item_id` for mutations and ordering. Clients should
fall back to the ID when the title is absent. Personal membership pages hydrate
titles through the existing viewer access filter; admin pages require acting
administrator access. Membership identity, ordering and cursor revision checks
are unchanged. Frozen v1 membership responses do not expose this field.

## Admin template list

`listAdminCollectionTemplates` (`GET /api/v2/admin/collections/templates`) lists only the
templates with an `mdblist`, `tmdb` or `tmdb_list` source, the set a single collection can be
created from and the same set the personal template list (`listCollectionTemplates`) offers. `tmdb_discover` and
`tmdb_collection` templates are left out because only a template bundle can apply them; read
their summaries from the bundle list below. A category left with no templates is dropped; the
rest keep their order. The response schema is unchanged.

The route requires acting administrator access. The frozen `/api/v1/admin/collections/templates`
response is unchanged and still lists every built-in template.

## Template bundle summaries

`listAdminCollectionTemplateBundles` (`GET /api/v2/admin/collections/template-bundles`) returns
each bundle with `templates`, a summary of every template in `template_ids` order: `id`, `title`,
`source`, `media_kind`, `featured`, `poster_path` (omitted when the template has no poster) and
`needs_setup`. The list covers every source a bundle uses, including `tmdb_discover` and
`tmdb_collection` templates, so a client can describe a bundle without the template catalog.
Check `template_summaries` in the `getAdminCollectionCapabilities` document before relying on
`templates`; a server without it returns bundles without summaries.

`featured` is the pinned-first flag a collection created from the template starts with.
`needs_setup` is true for a template whose collection is created empty and cannot sync until an
administrator sets its source; today that is the `tmdb_franchise_placeholder` template, which has
no TMDB collection ID. Applying the bundle still creates that collection.

The route requires acting administrator access. The frozen
`/api/v1/admin/collections/template-bundles` response is unchanged and carries no `templates`.

## Rows that show a server collection

`listAdminCollectionSections` (`GET /api/v2/admin/collections/{id}/sections`) lists the rows on
the administrator Home and library pages that show a server collection. Each item carries the
row's `id`, `scope` (`home` or `library`), `library_id` (the library whose page holds the row;
`null` for Home), `section_type`, `title`, `featured` (the row is its page's hero banner),
`enabled`, `position`, and `page_row_count`, the number of rows on that page. Home rows come
first, then library pages by library, each in page order. Rows a template bundle generated as a
hero are listed like any other row. An unknown collection answers `404`; a collection no row
shows answers an empty `items` list.

Each `listAdminCollections` item carries `home_row_count`, the number of turned-on Home rows that
show the collection, and `row_count`, the number of Home and library page rows that show it,
turned-off rows included. One count covers the whole list. Both are absent from
`getAdminCollection` and every other response that returns one collection.

The rows list includes turned-off rows. Both routes read the administrator page layouts only: rows a
profile added to its own Home are not listed or counted. A collection that `row_count` reports as
used cannot be deleted (`409`) until those rows stop showing it.

Both routes require acting administrator access. The frozen `/api/v1/admin/collections` list is
unchanged and carries no counts.

## Advisory age

Movies and series may carry `advisory_age`, a recommended minimum viewer age from
an advisory service such as Common Sense Media, and `advisory_source`, which names
who recommended it (`commonsense` or `mdblist`). Both are optional and appear
only together; an item with no advisory omits both.

The advisory is not a certification. `content_rating` remains the certification a
rating body issued, and it alone drives the content-rating ceiling
(`max_content_rating`). Do not present the advisory as a rating a viewer has to
satisfy.

Coverage is partial by design. The providers that supply advisory ages are rate
limited per day, so on a large library some titles carry one and others do not,
and the set grows over time. Absence means "not fetched yet", never "suitable
for everyone".

Whether to show the badge is a per-profile choice, `catalog.show_advisory_age`
in the settings contract, default off. The field is served regardless; the
setting decides whether a client renders it and never changes what a profile may
watch. Detect support by reading the setting from the settings contract
capabilities rather than sniffing versions.

### Advisory-age limit

A household manager can also limit a profile by advisory age with the profile's
`max_advisory_age` (an integer from 1 to 21, or `null` for no limit) on the v2
profile operations. The server hides every title whose `advisory_age` is above
the limit, everywhere the content-rating ceiling applies: browse, search, detail,
episodes, sections, progress, recommendations and the Jellyfin-compatible API.
Episodes use their series' advisory age. Other item types, including the beta
book libraries, never carry an advisory age, so the limit never hides them.

- The limit only ever tightens. It is ANDed with `max_content_rating`, and a
  title must pass both.
- By default a title with no advisory age is **not** hidden by the limit; the
  content-rating ceiling alone decides it. `access.unrated_content` does not
  apply to the advisory limit.
- A profile can instead require an advisory age with `require_advisory_age`
  (boolean, default `false`). With it set, a title with no advisory age is
  hidden too, so the profile sees only titles an advisory service rated at or
  under the limit. It has no effect without `max_advisory_age`. On a large
  library that has not been looked up yet, such a profile starts nearly empty
  and fills in as ages arrive: the opposite of the default, where titles
  disappear as ages arrive.
- Because coverage grows as the provider enriches the library, the set of titles
  a limited profile sees can shrink over time, for example when a title a child
  could see gains an advisory age above the limit. `advisory_titles` on
  `getAdminDashboardStats` reports how many movies and series carry an advisory
  age, next to `total_movies` and `total_shows`.
- Media-request discovery cannot apply the limit, because titles outside the
  library carry no advisory age.
- Only a household manager (the account's primary profile, with its PIN verified
  when it has one; on an admin account too) can set or clear either field; a restricted profile cannot change its own limit.
  Changing either bumps the account's access policy revision, the same as
  changing `max_content_rating`.
- Detect support with `max_advisory_age_supported` and
  `require_advisory_age_supported` on the `listProfiles` response. They are
  separate because `require_advisory_age` arrived later, so a server can report
  the first without the second. The profile operations reject unknown members,
  so do not send either field to a server that does not report it.

Frozen v1 responses do not expose these fields.

## Ratings on title pages

Every client shows a title's external ratings the same way: the v2 item detail
carries `ratings`, the list to render, already chosen and formatted by the
server. It is present on every v2 item detail, as an empty array when there is
nothing to show. Each entry has:

- `source`: `imdb`, `tmdb`, or a name a metadata plugin declared (see "Rating
  sources" below).
- `name`: the source's plain-text mark: `IMDb`, `TMDB`, or the name the plugin
  declared, such as `RT`.
- `score`: the rating on a 0-100 scale.
- `display`: the score on the source's own scale, formatted: `8.5`, `93%`,
  `4.2`.

Clients render each entry as its mark followed by `display`, in list order.
The list holds at most three entries, the first in display order, so every
client shows the same three ratings on one line.
Marks are plain text, never a source's logo artwork, with one exception: TMDB's
approved logo may stand in for the `TMDB` mark, as TMDB's terms allow. Clients
do not recompute the list from the `rating_*` members.

IMDb and TMDB are always in the list when the title has them. Every other
source is one an enabled metadata plugin declares, and appears only after an
administrator turns it on in the `catalog.extra_rating_sources` server
setting, a comma-separated list of source names that is empty by default,
because the owners of those scores restrict how others may display them.
IMDb, TMDB and Rotten Tomatoes come from the `rating_*` members, the same
numbers poster badges and browse sorting use; the other sources come from
`rating_sources`.

Cards follow the same choice: every v2 card leaves out `rating_rt_critic` and
`rating_rt_audience` unless a plugin declares that source and the
administrator turned it on, so poster
badges show a Rotten Tomatoes score only where title pages do. Item detail
does the same, and its `rating_sources` lists only the sources clients show.
The exception is a viewer who may curate the item's metadata (an admin, or an
account with the metadata curation permission): their item detail, and the
detail `updateAdminItemMetadata` returns, keep every stored value for the
metadata editor. Frozen v1 responses are unchanged.

Browse follows the choice too. A v2 catalog browse sorted by
`rating_rt_critic` or `rating_rt_audience` while that source is hidden orders
as if no sort was given: the saved or default order, reported as
`effective_sort`.

`GET /api/v2/capabilities/ratings` (`getRatingsCapability`) is the feature
check. It answers `state: available` on a server whose item detail carries
`ratings`, and `sources` lists the sources title pages and cards show, in
display order, each with its `source` and `name`. Clients use it to decide
whether to render `ratings` and which rating sorts and badges to offer. A
server without the operation predates `ratings`.

A card-sized summary such as a home hero shows one rating, IMDb or, without an
IMDb score, TMDB, as its mark and score. Cards carry raw `rating_*` numbers,
so a client that formats one itself rounds halves away from zero, as the
server's `display` does: a stored IMDb 7.35 reads `7.4`.

## Rating sources

The v2 item detail of a movie or series may carry `rating_sources`, a list of
per-source ratings metadata providers reported, limited to the sources title
pages show. Each entry has:

- `source`: `imdb`, `tmdb`, or a name a metadata plugin declared (below).
  Ignore a name you do not recognize.
- `score`: the rating on a 0-100 scale, whatever scale the source uses itself.
- `votes`: how many votes produced the score, omitted when the source does not
  report it.

Entries follow the order of `ratings`, at most one per source. A stored score
of a source no enabled plugin declares, or one the administrator has not
turned on, is left out, so a score a plugin reported before it stopped
declaring its source is never served; a viewer who curates the item's
metadata still gets every stored source (see "Ratings on title pages"). The
member is absent when no entry is left. It is detail-only: list and section
cards do not carry it. A title page renders `ratings`, not this list.

The four `rating_imdb`, `rating_tmdb`, `rating_rt_critic` and
`rating_rt_audience` members are unchanged, keep their own scales, and remain
the only ratings browse can sort or filter by.

Rating sources follow the lock rules of those four members, but not their
scheduled-refresh rule. A manual or scheduled refresh overwrites the sources
the providers report, so scores and vote counts stay current; the chain's
provider order picks one entry per source. The bulk enrichment pass only adds
sources the item lacks. Locking the rating field freezes all of them. A
refresh never removes a source a provider stopped reporting. Identify is the
exception: it matches the item to a different title, so the sources the new
match reports replace the stored set, and a source it does not report is
removed.

Plugins send them under `ratings.sources` in a metadata item, as
`{"<source>": {"score": 0-100, "votes": n}}`. The server keeps `imdb`, `tmdb`
and the sources the sending plugin declared, and drops any other name, a score
outside 0-100, and a vote count that is not a whole, non-negative number while
keeping its score.

Silo itself names only IMDb and TMDB. Every other rating comes from a metadata
provider that declares it. The built-in NFO provider declares `rt_critic` and
`rt_audience`, the Rotten Tomatoes scores it reads from local `.nfo` files. A
plugin declares its ratings in its capability's manifest metadata, at the top
level or inside the SDK's `metadata` envelope:

```json
"rating_sources": [
  {"id": "rt_critic", "name": "RT", "label": "Rotten Tomatoes critics", "scale": 100, "percent": true},
  {"id": "kinopoisk", "name": "Kinopoisk", "scale": 10}
]
```

- `id`: the source name, matching `^[a-z][a-z0-9_]{0,31}$`, other than `imdb`
  and `tmdb`. `rt_critic` and `rt_audience` name the Rotten Tomatoes scores the
  plugin sends as the flat `rt_critic` and `rt_audience` ratings, which fill the
  `rating_rt_*` members. The server keeps those flat scores only from a plugin
  that declares them, and declaring them lets title pages and poster badges
  show them. They are always percentages: `scale` and `percent` are ignored.
- `name`: the plain-text mark clients show next to the score, at most 24
  characters.
- `label`: optional; the source's full name in the administrator's list, at
  most 60 characters. `name` stands in when it is absent.
- `scale`: the top of the source's own scale, above 0 and at most 100. The
  plugin still sends a 0-100 `score`; a `scale` of 10 shows 72 as `7.2`.
- `percent`: optional; `true` shows the 0-100 score as a percentage, and the
  scale is 100 whatever `scale` says, so `scale` may be left out.

An entry that breaks a rule is dropped on its own, a repeated `id` keeps the
first, and a capability keeps at most eight. When two enabled plugins declare
the same `id`, the first by installation order names and scales it. A declared
source is stored like the others and is shown only after an administrator adds
its `id` to `catalog.extra_rating_sources`; title pages list it after Silo's
own sources. `GET /api/v2/admin/rating-sources` lists every source an
administrator can show, with the plugin that declared it;
`GET /api/v2/admin/rating-sources/capabilities` reports
`plugin_declared_sources: true` on a server that has that list.

Frozen v1 responses do not expose this member.

## Local theme songs, V2

Movies, series, and seasons can own local theme audio. Place `theme.mp3`
(or `.m4a`, `.m4b`, `.flac`, `.ogg`, `.opus`, `.wav`, `.aac`) in the item's directory,
or put audio files in its `theme-music/` directory. Scans honor the library's
ignore rules. Theme audio is separate from media files, extras, metadata
matching, and watch progress. Files must contain audio without video tracks.
Symlinked audio files and symlinked `theme-music` directories are excluded.

Ownership follows current video-file associations. A movie can use its
canonical directory or the directory containing its video. A series uses its
canonical root; flat episode files can use their containing directory when every
video there belongs to that series. A season uses directories below that root whose
videos belong to that season alone, including season directories above disc subdirectories.
Ambiguous directories do not grant a theme to multiple movies, series, or seasons.
Metadata rematching changes ownership without copying theme rows.
Theme-file update events reconcile the owner's audio without importing the library
again. Failed audio probes preserve only that file's cached record, and deleted
videos retain their themes until the scanner removes the missing video rows.

The V2 item detail document includes `themes` with `owner_id` and an ordered
`items` array. Each theme has `id`, `title`, `duration_seconds`, and `container`.
Paths are never exposed. A season without themes inherits its series' set;
an episode uses its season's set, then its series' set. Resolution selects one
owner's set and applies the viewer's library, rating, and quality restrictions.
An empty set retains the requested item's ID and an empty array.
Seasons derived from episode groups use the same ownership and inheritance rules.
If the optional theme lookup fails, item detail still succeeds and omits `themes`.

| Method and path | Result |
| --- | --- |
| `GET /api/v2/catalog/themes/capabilities` | Shared capability document with `delivery: routed`, `transcode`, `cluster_routing`, and `grant_lifetime_seconds` |
| `POST /api/v2/catalog/items/{id}/themes/{theme_id}/playback` | `url`, `expires_at`, `delivery`, and `content_type` for an authenticated login session and verified profile |
| `GET\|HEAD /api/v2/catalog/items/{id}/themes/{theme_id}/audio?token=...` | Theme audio this API node serves, authorized by the playback grant |

The optional playback request body lists what the client decodes as
`accepted_formats`, pairs of `container` and `audio_codec` (for example
`{"container": "ogg", "audio_codec": "vorbis"}`; an empty codec accepts any
codec in the container). The server sends the original when it matches.
Otherwise it sends a conversion to AAC in progressive audio-only MP4
(`delivery: converted`, `content_type: audio/mp4`) when an `mp4` or `m4a`
entry accepts `aac`. A client that decodes neither gets `406 not_acceptable`.
A request without a body receives the original, as before conversion existed.
`transcode` reports whether any conversion route exists on this deployment.

Themes follow the playback routing policy, like video. Original audio follows
`playback.routing.direct_play_egress`: with the default `prefer_proxy`, a proxy
node serves it and the API serves it only when no proxy can. A conversion
follows `playback.routing.remux_execution` and `playback.routing.remux_egress`:
by default a transcode node converts it and a proxy relays it, falling back to
a proxy, then to the API, as far as the policy allows. `proxy_only` and
`worker_only` are never crossed; when no route satisfies the policy, the
playback request answers `503 dependency_unavailable`. Only workers that
advertise `theme_audio_egress_v1` (proxies) or `theme_audio_execution_v1`
(transcode nodes) are chosen, so a mixed-version cluster never hands a theme to
a worker that cannot serve it.

`url` is either this server's audio route or an absolute URL on a proxy's
origin. Both expire after at most five minutes, bounded by the login token's
remaining lifetime. The API grant binds the account, profile, login session,
policy revision, owner, theme file ID, size, and modification time, and each
audio request rechecks the current account, login session, profile,
permissions, ownership, and file. A proxy URL is checked against the same
authority when it is issued and is then authorized by its signed token alone
for its lifetime, as video stream tokens are; it names the theme file, its
size and modification time, the serving proxy, and, for a conversion, the
transcode node. Grants use a separate signing key derived from the server
secret and cannot be used as an account or ordinary playback token. Clients
must not log or persist signed URLs. Grant responses and audio use
`Cache-Control: no-store`.

Original audio supports byte ranges, HEAD, ETags, and HTTP read preconditions.
Authorization happens before a conditional response. A converted stream has no
length and no byte ranges; replay it with a new grant. The node serving a
theme must read the theme directory at the path the scanner recorded, as it
must for library media. Missing local files or changed bytes require a rescan.

The web preferences `ui.theme_music_enabled` and `ui.theme_music_loop` default
to `false` and support profile and profile-device scope on the web platform.
The web player fades theme audio, preserves its position across details with
the same owner, suspends while a navigation destination is unresolved, and
stops on normal playback, logout, or profile change. It handles browser autoplay
rejection and retries a failed audio URL once with a fresh grant. Playback
progress resets that retry budget for a later expiry.

The web player reports its formats with `canPlayType`. It loops original audio
with the element's `loop`, and replays a converted theme with a fresh grant when
it ends.

Apple and Android do not advertise this feature initially, as specified in
issue #937. They need V2 discovery and fixtures, settings, playback, and lifecycle
support before enabling it. Provider downloads, theme videos, uploads, remote
URLs, and HLS theme transcoding are outside this local-file capability. No V1
route or V1 item-detail shape changes.

## Description translation

Silo can translate descriptions with an AI model into a profile's metadata
language (`metadata_ai` settings). Localized text reaches clients through the
ordinary localized fields; these members describe it:

- `machine_translated_fields` on the item detail, season rows and episode rows
  lists the localized fields whose text an AI translation wrote: `overview`
  and, on items, `tagline`. It is absent when no shown field was
  machine-translated, and it is never set for provider or manually entered
  text. Clients label exactly the fields it names; an AI `tagline` alone says
  nothing about the overview. The frozen v1 JSON does not carry it.
- `pending_translation_language` on the item detail is this profile's
  metadata language when the item's description is not available in it yet.
  `POST /api/v2/catalog/items/{id}/translate-description` with that value as
  `target_language` queues the translation; `GET
  /api/v2/capabilities/metadata-ai` reports whether the server translates on
  view automatically (`auto`), offers it as an action (`button`), or not at
  all (`off`).
