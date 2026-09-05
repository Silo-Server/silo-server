# Library diagnostics API

The native v2 library administration operations are acting-admin operations. Their
complete request and response schemas are generated in `contracts/api/v2/openapi.json`.
The frozen v1 bridge keeps its existing responses.

`GET /api/v2/libraries/roots`, `GET /api/v2/libraries/skipped-roots`, and
`GET /api/v2/libraries/stale-ids` return `{items, page}` collections. Roots additionally
return `total`, counting the matches across every page. `limit` defaults to 50 and is
at most 200. Continue with `page.next_cursor` while `page.has_more` is true. A cursor
is bound to the acting administrator and query filters; changing a filter starts a
new listing without a cursor.

All three operations accept a trimmed, case-insensitive substring query `q`. Search
runs before database pagination, so a match can be found without loading preceding
pages. Percent signs and underscores are literal characters.

| Operation | Search fields | Additional filters |
| --- | --- | --- |
| Roots | Root path, title, sample file path | Required `library_id`; optional `state` |
| Skipped roots | Root path, library name, reason | None |
| Stale IDs | Title, provider, provider ID, library name | Actionable provider IDs only |

The web administration page loads diagnostics when their section opens. It requests
more results through **Load more** and starts a new first page when the search changes.
Sorting controls on diagnostic tables sort the loaded results.

Library poster uploads allow a file of up to 10 MiB plus 1 MiB of multipart framing.
The file limit is checked separately from the total request size. Accepted library
deletion and metadata-refresh jobs return their canonical job URI in `Location`.
