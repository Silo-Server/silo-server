# Administrator diagnostic downloads

`GET /api/v2/admin/diagnostics/reports/{id}/download` (`downloadAdminDiagnosticReport`)
streams a ready report as `application/gzip`. It requires an authenticated acting
administrator and the selected profile's verification headers. Demo accounts
cannot download reports. The operation returns `Cache-Control: no-store`, an
attachment filename, and a content length when the stored size is known.

The API host opens the stored bundle and streams it directly. It does not return
a presigned storage URL. Range requests receive the entire archive with status
200 and `Accept-Ranges: none`. This preserves the existing web proxy download
behavior. Failures before streaming use v2 Problems: 404 for a missing report or
object, 409 for a report that is not ready, and 503 for unavailable storage or
report services. Interrupted transfers after headers cannot become JSON errors.

The web administrator download helper uses the captured session and profile for
the request and checks that authority again after reading the body. A profile
change prevents creating a browser download from the completed response. The
native Apple and Android clients do not consume this administrator endpoint;
Jellyfin compatibility has no diagnostic administration counterpart.

The frozen v1 download operation retains its existing presigned-URL and proxy
modes during the bridge release. Other diagnostic administration operations
continue migrating separately.

## Report list and detail

`GET /api/v2/admin/diagnostics/reports` (`listAdminDiagnosticReports`) returns an
`items` collection and `page` metadata. It retains the bridge filters `user_id`,
`platform`, `report_type`, `from`, `to`, and `short_id`. Time filters bound
`received_at` inclusively; `from` must not follow `to`. Page size defaults to 50
and is bounded to 1–200. Ordering remains descending `(received_at, id)`.
The opaque cursor is signed and bound to the administrator, selected profile,
normalized filters, and page size. Changing that scope requires restarting the
list. It is ordinary keyset paging over stored reports, not a snapshot or sync
watermark.

`GET /api/v2/admin/diagnostics/reports/{id}` (`getAdminDiagnosticReport`) returns
the same metadata plus the original validated manifest. Account IDs are opaque
strings; metadata timestamps use UTC with millisecond precision. The embedded
manifest retains its own schema version, timestamps, and extension fields.
Summaries omit the manifest, and neither operation returns object-store bucket
or key locations. These reads use the same acting-administrator and demo gates
as downloads. An unavailable report service returns 503 and a missing detail
returns 404.

The web queries preserve opaque IDs and reject decoded responses if profile
authority changed while reading the body. Existing report deletion and upload
settings remain separate migration scopes.

## Report deletion

`DELETE /api/v2/admin/diagnostics/reports/{id}` (`deleteAdminDiagnosticReport`)
returns 204 when the report row is deleted or already absent. It uses the same
administrator and demo gates as report reads. Database errors fail the request;
the service deletes the row first, then attempts object cleanup. A cleanup
failure is logged for reconciliation and does not resurrect the report or fail
an otherwise successful deletion. The response confirms metadata removal and
does not guarantee immediate removal of every stored object. No durable job is
created or advertised.

The web captures the report ID and profile authority when the administrator
submits the deletion, including when the request is paused offline. It disables
automatic retries and authentication replay, and only changes the active view
or its cache if the captured authority is still active when the request finishes.
