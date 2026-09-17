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
