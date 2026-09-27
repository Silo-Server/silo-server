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
- Only a household manager (a server admin, or the primary profile) can set or
  clear either field; a restricted profile cannot change its own limit.
  Changing either bumps the account's access policy revision, the same as
  changing `max_content_rating`.
- Detect support with `max_advisory_age_supported` and
  `require_advisory_age_supported` on the `listProfiles` response. They are
  separate because `require_advisory_age` arrived later, so a server can report
  the first without the second. The profile operations reject unknown members,
  so do not send either field to a server that does not report it.

Frozen v1 responses do not expose these fields.

## Rating sources

The v2 item detail of a movie or series may carry `rating_sources`, a list of
per-source ratings a metadata provider reported, such as the MDBList plugin's
IMDb, Metacritic, Letterboxd and Roger Ebert scores. Each entry has:

- `source`: one of `imdb`, `tmdb`, `rt_critic`, `rt_audience`, `metacritic`,
  `metacritic_user`, `letterboxd`, `trakt`, `rogerebert`, `myanimelist` or
  `mdblist` (MDBList's own aggregate). The list of sources can grow; ignore a
  name you do not recognize.
- `score`: the rating on a 0-100 scale, whatever scale the source uses itself.
- `votes`: how many votes produced the score, omitted when the source does not
  report it.

Entries come in that fixed source order, at most one per source. The member is
absent when no provider reported a source. It is detail-only: list and section
cards do not carry it.

The four `rating_imdb`, `rating_tmdb`, `rating_rt_critic` and
`rating_rt_audience` members are unchanged, keep their own scales, and remain
the only ratings browse can sort or filter by.

Rating sources follow the same refresh and lock rules as those four members. A
scheduled refresh only adds sources the item lacks, a manual refresh overwrites
the sources the providers report, and locking the rating field freezes all of
them. A refresh never removes a source a provider stopped reporting. Identify
is the exception: it matches the item to a different title, so the sources the
new match reports replace the stored set, and a source it does not report is
removed.

Plugins send them under `ratings.sources` in a metadata item, as
`{"<source>": {"score": 0-100, "votes": n}}`. The server drops an unknown
source name or a score outside 0-100, and drops a vote count that is not a
whole, non-negative number while keeping its score.

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
