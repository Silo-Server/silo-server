# Scan control API

This is the contract reference. For integration recipes — Sonarr, Radarr, Autoscan,
and the webhook receiver — see [the scan API guide](scan-api.md).

The v2 scan commands use the same acting-administrator and profile-verification
gates as library administration. The existing demo policy applies, including its
administrator exemption. They share target resolution, dispatch and cancellation
with the bridge; this transport does not implement another scheduler.

`GET /api/v2/scan/capabilities` returns revision `1` and state `available` when a
scan queue or local ingester exists, otherwise `not_configured`.

`POST /api/v2/scan` accepts optional string `library_id` and optional `path`.
The existing resolver requires enough information to identify a configured
library and a permitted library, subtree or file target. It retains disabled
library, root containment, missing-path and file-kind rules. Invalid body IDs
return a validation problem. An accepted request returns `202`:

```json
{"status":"accepted","mode":"library","library_id":"42"}
```

Acceptance dispatches to the durable queue when configured, otherwise to the
existing process-local ingester. The response does not identify a durable
command or promise that process-local execution survives restart. It is not a
completion or a replay receipt. An uncertain reply must not be retried
automatically.

`POST /api/v2/scan/cancel` requires a string `library_id` and returns `200` with
`cancelled` and `library_id`. The count preserves the existing queue and local
ingester result; it does not assert that every cluster worker has stopped or
that cleanup has finished. Current local scan events are updated through the
existing cancellation path.

Both POST operations are `non_retryable`. Repeating library-wide cancellation
may cancel scans created after the original dispatch; it is not an idempotent
cancellation of a stable scan identity. Neither operation adds scheduling,
leases, durable cancellation receipts or cross-replica replay guarantees.

The bridge retains its numeric IDs and response shapes. Operations owns the web
scan controls and their v2 adoption, including disabling automatic mutation and
authentication replay, retaining captured administrator/profile authority, and
converting IDs at the boundary. The two ordinary migration rows stay proposed
until independent review and that consumer adoption are accepted. There is no
native or Jellyfin scan-control consumer in the migration inventory.

The web library page, dashboard widget and autoscan activity cancellation now use
these v2 commands through shared hooks. Submission captures the selected library
and administrator/profile context before an offline pause. Both mutation and
authentication retries are disabled; a lost reply requires operator observation
and a new explicit decision. Late results from a replaced authority do not publish
toasts or invalidate the active view. Existing UI selection and cancellation
notifications are preserved. The all-libraries task remains its separate task API.
