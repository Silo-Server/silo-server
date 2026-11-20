# Request Discover rows

The Discover tab of the Requests page shows rows of titles the server does not
have yet, for people to request. An admin decides which rows it shows and in
what order, on Admin → Requests → Discover Page. The row types and their checks
live in `internal/requests/discoverrows`; the layout's storage and each
viewer's overlay live in `internal/requests` (`discover_layout.go`,
`discover_rows.go`).

## Rows

A row has an ID, a type, a title, a switch, a limit (how many titles the page
shows, 1–100, default 20) and `hide_available` (leave out titles the library
has). Its type decides where its titles come from:

| Type | Titles | Spec |
| --- | --- | --- |
| `tmdb_chart` | A TMDB chart: trending (day or week; movies, series or both), popular, top rated, now playing, upcoming, airing today, on the air | `chart` |
| `tmdb_discover` | A TMDB `/discover` query: genres (all of), excluded genres, keywords, companies, networks (series), countries of origin, streaming services in a region (each any of), original language, year range, lowest score and vote count, runtime, highest US rating, sort | `discover` |
| `tmdb_list` | A public themoviedb.org list | `list` |
| `mdblist` | A public mdblist.com list, in list order or newest added or released first | `list` |
| `recommended` | The viewer's own recommendations of titles the server lacks (see [Recommended rows](#recommended-rows)) | `recommended` (`media_type`: `movie` or `series`) |
| `studios`, `networks`, `genres` | The fixed brand cards; each opens a browse page | none |

Each type is a `discoverrows.Kind`: it checks and canonicalizes its spec,
names its source, and, for a list type, names the stored copy it reads. A
`recommended` row's source is personal (`discoverrows.PersonalSource`): it is
read per viewer from stored data, not page by page from TMDB. A row
of a type this server does not know (stored by a newer server) is skipped on
read and dropped by the next save.

Checks (`discoverrows.NormalizeLayout`):

- At most 30 rows; titles of 1–80 characters; exactly the spec its type uses.
- Chart and discover rules are the collection templates' (`ValidateTMDBSpec`,
  `ValidateTMDBDiscoverSpec`, see [Collection templates](collection-templates.md#source-rules)),
  plus what only rows have: a series sort for a series row, a rating on the
  media type's US ladder, a region with streaming services, two-letter codes,
  at most 20 IDs per list.
- A list URL passes the same allowlists as collection sync
  (`collectionutil.CanonicalMDBListURL`, `ParseTMDBListURL`) and is stored in
  canonical form. The fetch checks it again and follows only redirects that
  stay on mdblist.com, so a row cannot point the server at another host.
- `list.order` is for `mdblist` rows only: `list` (stored absent),
  `newest_added` or `newest_released`. MDBList's `/json` feed returns rank
  order unless asked for a sort, whatever sort the owner saved for the list
  page, and it does not report that saved sort, so the admin picks it. The
  refresher sends `sort=added` or `sort=released` with `sortorder=asc`,
  because MDBList inverts `sortorder` for date sorts.
- A `recommended` row always has `hide_available` on: it never shows a title
  the library has.
- Stored layouts are not checked again on read: tightening a rule changes what
  the next save accepts.

### IDs

- **Built-in rows.** The six rows the page had before it could be changed keep
  their IDs: `trending_movies`, `trending_series`, `popular_movies`,
  `popular_series`, `upcoming_movies`, `on_air_series`. They are the keys
  `getDiscoverSection` accepts and the keys shipped Apple and Android apps merge
  into their two carousels (trending, popular), so a built-in row keeps its type and
  chart; its title, limit, switch and `hide_available` can change, and it can be
  removed.
- **Recommended rows.** The default `recommended_movies` and `recommended_series`
  rows keep their media types. A saved custom row that already used either ID
  can still be edited and saved. Clients read these rows through `row_order` and
  `getDiscoverRow`.
- **Brand rows.** A brand row's ID is its type, so each appears at most once.
- **Custom rows.** A new row gets an opaque `r_` ID. `rows` and `browse` are
  reserved.

## Storage

The layout is one row of `request_discover_layout`: the rows as a JSON array
and a revision from `request_editor_revision_seq`, like the other request
editors. With no row stored the default layout applies: the two recommended
rows, the six built-in rows, then studios, networks, genres. That default is code, so a server whose admin
never changed the page follows future defaults; it reads as revision zero.
Saving replaces the whole layout (`If-Match` on the revision); resetting deletes
the row. A first save over the default races safely: the upsert's revision
predicate turns the loser into a stale write.

## Each viewer's page

`DiscoverRows` renders the page; `DiscoverRow` serves one title row's see all
page. Both read the layout, skip turned-off rows, and fill each title row other
than a recommended one from its source page by page (`fillRowPage`):

- The viewer's rating ceiling filters every title, failing closed on unrated
  ones, whatever the row's source. For a discover row the stricter of the
  viewer's ceiling and the row's own rating is also sent to TMDB to fetch
  fewer titles that would be dropped; the row's own rating is curation, not a
  safety filter.
- Availability, request state and follows are looked up per viewer. With
  `hide_available` titles the library has are dropped.
- When filtering thins a page, later source pages are read until enough titles
  survive or the page budget is spent: `ceil(limit/20)` pages, plus one when
  the row filters, on the Discover page; five on a see all page. Totals stay
  the source's unfiltered counts. `next_page` is the resume cursor; after a
  read of several pages reached the end it points past `total_pages`, so a
  client that never saw a cursor does not read those pages again. See the
  backfill comment in `internal/requests/service.go`.
- The Discover page shows at most a row's limit. A row it trims loses its
  cursor; its see all page starts again at page 1.
- A title row that fails (TMDB refused a filter, a list is unreadable) is left
  out and logged; the page fails only when every title row that reads TMDB or a
  stored list failed and no recommended row has titles to show. A page with only
  recommended rows fails when every one of them failed to read, and otherwise
  can succeed with an empty result. A title row
  with no titles is left out of `items` and stays in `row_order`, since shipped
  clients cannot draw an empty row.

Cost: every title row but a recommended one costs TMDB and database work on each Discover load
(rows run four at a time). A rating-restricted viewer on a cold certification
cache costs up to 20 certification lookups per source page read; certifications
are cached for seven days and shared by every profile.

## Recommended rows

A `recommended` row shows the viewer's own ranked titles of one media type that
the server does not have, as the recommendations engine stored them for the
viewer's profile (see [Recommendations](recommendations.md)). Each profile reads
its own row, so two profiles on one account see different titles. The request
service reads it through `discoverrows.Recommendations`, which the
recommendations reader implements; without one, the row has no titles.

A read makes no outbound call, on a cold cache too: it calls neither TMDB nor
the embedding provider. `internal/requests/discover_personal.go` reads the whole
row (at most 60 titles), then:

- applies the viewer's rating ceiling to each title's stored US certification
  with the rule the rest of Requests uses, `access.RatingAllowed`: a title with
  no US certification, or one it does not recognize, is left out;
- leaves out titles the library has, matched in the catalog by the TMDB, TVDB
  and IMDb IDs stored with each title, without the TMDB external-ID lookups
  other rows make;
- lays the viewer's request state and follows over the titles shown, as on other
  rows, and the API adds `in_watchlist` the same way. A title stays `missing`.

Each title carries up to two `reasons`: the library titles that led to it, best
first, and only ones the profile watched, rated or favorited. A reason the viewer cannot see is left out; the title stays.

Filtering comes before paging, so totals are exact: `total_results` counts the
titles the viewer gets and `total_pages` is that over 20. Titles whose request
by the viewer's profile was declined (with no active request) are dropped, and
titles that already have an active request are stably moved behind fresh
unrequested titles so leading slots stay actionable. Daily tail rotation
(pinning the top 10 and rotating positions 11–20 by UTC date) keeps rows fresh
between rebuilds. The Discover page
shows up to the row's limit. Its see all page serves 20 titles a page with no
`next_page`; page+1 follows. A row the Discover page trims has no cursor, as on
other rows; a row it shows whole over several pages points past its last page.

The row has no titles, so it is left out of `items` and kept in `row_order`,
when the profile has no stored row yet, when the server has Discover
recommendations off (`recommendations.external.enabled`) or recommendations off.
A profile blocked from requests gets no
Discover page at all. `GET /requests/status` reports
`personal_discover_rows_supported`. A server whose admin saved a layout before
these rows existed shows them once the admin adds them.

## List rows

TMDB and MDBList lists are read from a stored copy, never fetched while a
viewer waits. `request_discover_list_snapshots` holds one copy per list source
(`tmdb_list:<id>`, `mdblist:<canonical /json URL>`, with `?order=<order>`
appended for an MDBList row in another order), so two rows showing one list in
one order share it and a title or limit change keeps it. A copy holds up to 200
cards in list order (TMDB ID, media type, title, overview, artwork, release
date, votes); availability and request state are still looked up per viewer.
MDBList entries carry no artwork, so each costs a TMDB lookup by TMDB ID (or
IMDb ID when it has none); a TMDB list page carries full items.

`discoverrows.Refresher` keeps the copies:

- The `refresh_request_discover_lists` task runs at startup and hourly and
  fetches every enabled list row's list older than 12 hours. A layout save also
  fetches the saved rows' lists that are missing, stale or failing, in a
  goroutine detached from the request; the scheduled run is the recovery path
  when that node stops.
- A fetch claims its list for a ten-minute lease, as seasonal and trending
  snapshots do, so any node can run it and a crashed worker's claim expires. A
  save needs the claim it started with.
- A failed fetch, a failed lookup other than "TMDB does not have this title",
  or an empty answer over a copy that had titles keeps the last good copy and
  records the error; a scheduled run retries a failed list after 30 minutes.
- A list no row names is deleted once nothing has touched it for seven days.
  A save that names it again refreshes it first, which moves its last attempt
  past the cutoff.

The admin editor shows each list row's copy: pending, ready (titles and when),
empty, or the last error. A turned-off row's list is not fetched until the row
is on.

## Editor

The editor is the Sections page's row list and row dialog
(`web/src/components/homeRows`) over the layout, with the Collections source
pickers for charts and lists. `useDiscoverRows` adapts the layout to the row
list (`HomeRowsListAdapter`). Every change saves at once, as on Sections: it
writes the whole layout with `If-Match` over the newest version the page holds,
one write at a time, so a switch flipped while another write runs lands on top
of it. A 412 means another admin changed the page: the page shows its conflict
banner, and writes keep failing that way until the admin reloads.

## API

The contract is in [api-contract.md](api-contract.md#personal-requests):
`listDiscoverSections` follows the layout and adds `row_order`, `getDiscoverRow`
pages any title row, `getDiscoverSection` answers 404 for a built-in row the
admin turned off, and `GET /requests/status` advertises
`discover_rows_supported` and `personal_discover_rows_supported`. Results on a
recommended row carry `reasons`. The admin operations are under
`/admin/request-discover` ([Request administration](api-contract.md#request-administration)).
They work while requests are turned off, so an admin can set the page up first.
