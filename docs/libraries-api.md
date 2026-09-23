# Library diagnostics API

The native v2 library administration operations are acting-admin operations. Their
complete request and response schemas are generated in `contracts/api/v2/openapi.json`.
The frozen v1 bridge keeps its existing responses.

`GET /api/v2/libraries/roots`, `GET /api/v2/libraries/skipped-roots`, and
`GET /api/v2/libraries/stale-ids` return `{items, page, total}` collections, where `total`
counts the matches across every page. `limit` defaults to 50 and is
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


## Accepted library work

`DELETE /api/v2/libraries/{id}` and library metadata refresh return `202`, a polling
`Retry-After: 5`, and the canonical job body with an origin-relative
`Location: /api/v2/library-jobs/{job_id}`. This corrects the previously emitted
unimplemented v2 `/admin/jobs/{id}` monitor URL. The monitor survives deletion of its
library. Failed persistence never returns acceptance. Library deletion disables its
folder and inserts the job in one transaction; repeated acceptance for the same active
delete conflicts, while deletion of different libraries remains independent.

`POST /api/v2/libraries/{id}/refresh-metadata` takes an optional body with `mode`.
`quick`, the default when the body or mode is absent, refreshes only matched items that
need it, such as ones never refreshed, lacking an overview or artwork, with a failed
refresh, or with incomplete episodes. `full` refreshes every matched item and re-scans
items that have no provider IDs.
A refresh holds a per-library PostgreSQL advisory lock, shared with the
`refresh_all_library_metadata` task; a job that starts while another refresh of the
same library holds it fails rather than refreshing the library twice. A job recovered
after its worker stopped heartbeating waits for the lock instead: its earlier attempt may
still hold it until that worker notices the recovery, or until PostgreSQL closes the
session of a server that disappeared. The wait counts toward the job's six-hour limit
and can be cancelled.

The job contains `id`, `kind`, `state`, `terminal`, `cancelable`, `created_at`, optional
`started_at` and `finished_at`, and optional progress measured in items for metadata
refresh. Deletion stages have no honest shared work denominator and omit progress.
Successful work exposes a named `refresh_result` or `deletion_result`. A failed job
exposes safe `JobFailure` data in `failure`; polling itself still returns `200`.
Internal request documents, storage details, operator messages, and raw errors are
never included. Clients use `terminal` rather than an exhaustive state list.

`GET /api/v2/library-jobs/{job_id}` is available to administrator accounts, supports
ETag/`If-None-Match`, and sends `Retry-After` while nonterminal. The validator covers
the entire authorized body and is deterministic across API replicas. Authorization
precedes conditional evaluation. Unknown jobs and jobs hidden from the caller return
`404`. The structured response retains the default `Cache-Control: no-store` policy.

`POST /api/v2/library-jobs/{job_id}/cancel` retains the acting-admin gate, including
the primary-profile requirement when a profile is supplied; gate denials return `403`.
After that gate, hidden or unknown jobs return `404`. It accepts refresh cancellation with `202`
and `state: canceling`. Pending retries coalesce, an already canceled job returns
`200`, and succeeded, failed, or noncancelable deletion jobs return
`409 job_not_cancelable`. Cancellation is best-effort: already refreshed metadata
remains. Intent is persisted in the job row, is observed by remote workers at their
heartbeat interval, and survives worker restart. Queued intent is acknowledged by the
ordinary worker without executing the refresh. The database serializes cancellation
and completion; once a terminal outcome wins, a stale worker cannot overwrite it.

Jobs remain retrievable for at least 24 hours after completion, failure, or cancellation;
the runner normally retains them for seven days. Cleanup may then remove the monitor.
The same canonical job shape is returned when work completes before the acceptance
response is sent. Retrying terminal work submits a new operation under that operation's
retry policy. Jellyfin behavior is unchanged; native Apple and Android clients must use
the canonical v2 monitor during their coordinated v2 migration.

## Scoped library discovery

`libraries:read` allows `GET /api/v2/user/libraries` and its `/capabilities`
endpoint. It reuses the existing account/profile visibility rules and response:
library IDs, names, types, sort order and optional poster URLs. It grants no
access to administrator storage metadata, library management or media playback.
The credential owner supplies the account identity; this is not discovery on
behalf of an arbitrary user.

Existing web, Apple and Android library callers keep the same response and access
rules. The new API-key scope requires no changes to those clients or Jellyfin.

## Certification country (fork test)

The v2 library create and update bodies accept `certification_country`: `US`
(default) or `AU`. Administrator and user library responses report this value.
`GET /api/v2/user/libraries/capabilities` exposes `certification_countries` for
feature detection. Metadata language remains independent of certification country.
Changing country queues a full metadata refresh; until it completes, existing
recognised US metadata supplies the fallback. Provider failures fail the refresh
job and preserve the previous certification snapshot.

Australian libraries prefer actual Australian TMDB certifications. The server's
existing TMDB certification client fetches country data during metadata refresh,
without requiring a new plugin binary. This test supports TMDB country data and
explicit country-prefixed NFO/manual values; providers returning only an
unqualified rating cannot supply an Australian certification. Locked ratings win.
Country snapshots are bound to the item's TMDB identity and cannot follow a
reidentified title. Shared items retain one snapshot with country-specific values,
not whichever library happened to refresh last.

For missing AU certifications the access-control equivalents are:

| US source | Australian equivalent |
| --- | --- |
| G, TV-G, TV-Y | G |
| PG, TV-PG, TV-Y7, TV-Y7-FV | PG |
| PG-13, TV-14 | M |
| R, NC-17, TV-MA | R18+ |

These are conservative Silo estimates, not official classifications. Unknown and
unrated values remain unknown and are denied under a rating ceiling. AU M and
MA15+ are distinct levels. X18+ is above R18+ and is not inferred from US ratings.

V2 catalogue responses may include `certification` with `country`, `rating`,
`source_country`, `source_rating` and `equivalent`. `content_rating` remains the
machine-readable token (for example `AU-M`). Clients should label equivalents
and retain their source, as the web item-detail badge does. The profile editor
includes the schemes of available libraries and always retains its saved limit.
Existing US ceilings keep their previous US-to-US comparisons.

A library-scoped detail displays that library's certification. Unscoped details
and access checks use the strictest result across the viewer's accessible enabled
libraries. A presentation-library query cannot loosen a rating restriction.
Lists, search, recommendations and direct item access share the resolver; episodes
inherit their series rating. Jellyfin uses the same server-side access checks.
Apple and Android continue to receive rating strings and server-enforced limits;
their native configuration and equivalent-rating presentation require client
follow-up before this fork feature is proposed upstream. The web interface is
the configuration and testing surface for this version.
