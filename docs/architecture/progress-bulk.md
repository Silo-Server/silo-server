# Progress bulk writes

`POST /api/v2/sync/progress` is a synchronous **per-item** operation. It accepts
1–100 items for the authenticated account and active profile. Each item names a
unique `media_item_id` and may include a unique, nonblank `client_ref` of at most
128 characters. Invalid envelopes, duplicate targets, and duplicate references
are rejected before processing any item. Authentication and profile access still
run before the handler.

After preflight, the selected user store applies the existing progress thresholds,
force-overwrite behavior, and clamped client-event ordering. Catalog visibility is
checked using the active viewer's library and rating scope. A hidden or missing
item returns an item-level `404` without a write. A store write failure returns a
safe item-level `500`; it does not expose the database error. Failure to open the
selected store or resolve catalog access rejects the request before item writes.

A completed batch returns HTTP `200`, including a wholly failed batch. The named
`ProgressSyncBatchResult` contains `items` in request order and exact `summary`
counts (`total`, `succeeded`, `failed`). Each item contains its zero-based `index`,
`media_item_id`, optional echoed `client_ref`, and a `status` discriminating named
success and failure variants. Failures carry `BulkItemFailure` with a stable type,
fixed title, status, and safe detail. Success includes writes skipped by existing
threshold or event-order rules. V2 does not use `207`.

The operation remains `non_retryable`: the bulk envelope does not introduce a
request receipt, replay identity, or transaction spanning all items. A lost
response can leave partial progress and repeated events. Clients must not blindly
replay a batch. `client_ref` is correlation only. Progress writes are not protected
editor resources, so this operation does not accept per-item `expected_etag`.

The reusable foundation consists of the item correlation, summary and failure
models, the 100-item limit, and duplicate-identity preflight. It does not provide a
generic execution engine or claim support for atomic bulk operations. Each later
bulk operation still owns validation, authorization, commit boundaries, and retry
semantics.

## Client coordination

The bundled web `useReportMediaProgress` hook uses v2 and turns an item failure
inside HTTP `200` into a failed mutation. Generated types and contract fixtures
carry the discriminated result shape.

Apple's `iosApp/iosApp/Downloads/DownloadAPI.swift` (`syncProgressBatch`) and
`iosApp/iosApp/Networking/SiloAPI.swift` (`syncProgress`) still use v1. Android's
`shared/src/commonMain/kotlin/org/siloserver/silo/network/api/PersonalDataApi.kt`
feeds playback and offline outbox progress through v1. Their v2 adoption must
chunk at 100, avoid duplicate targets within a batch, consume ordered item results,
and preserve failed items without blindly replaying successful ones. These native
migrations remain a coordinated follow-up; this change leaves the frozen v1
request and response unchanged. Jellyfin compatibility does not use this native
batch envelope and needs no transport change.
