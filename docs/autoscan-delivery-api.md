# Autoscan delivery API

`POST /api/v2/autoscan/webhooks/{token}` accepts the existing provider JSON
envelope. The token is a source delivery capability; an ambient account login
is neither required nor used as proof. Unknown tokens return `404` without
reading provider bytes. Tokens, request URLs and payloads must not be logged.

The shared application method resolves the source token before reading at most
256 KiB and calls the existing provider parser and autoscan ingest service.
The declared request schema is attached after Huma input registration so its
decoder does not read or re-encode provider bytes before token resolution.
HTTP request compression and non-JSON media types are refused. When configured,
the same `autoscan_webhook` per-endpoint rate-limit bucket as the bridge applies;
rate-limit problems retain `Retry-After`.

Accepted requests return `202` with `{"status":"accepted"}`. Test events,
unsupported event types and disabled sources/global autoscan settings return the
same acceptance without enqueuing work. Actionable deliveries are durably
accepted through the existing ingest service before `202`. Pending processing
is retried by that service. A failure to durably accept returns `500`; this
transport does not create another scheduler or claim a new durable job receipt.

The endpoint's `last_received_at` (the admin source view's
`webhook_last_received_at`, shown as "Last delivery") records deliveries the
source took: provider Test events in any state, so the setup wiring check works
before the source is enabled, and any delivery while both the source and
Autoscan are enabled. A real delivery that arrives while the source or Autoscan
is disabled still returns `202` but is dropped and leaves the timestamp unchanged.
A delivery with paths is stamped by the same statement that durably accepts it,
using the database clock, so one answered with `500` does not update it, and an
error recorded while processing it is always newer than its stamp.

The operation is `non_retryable`: a lost response leaves admission uncertain,
and repeated delivery is not guaranteed to reproduce the same result forever.
The service's internal processing retry is separate from caller replay.

`GET /api/v2/autoscan/capabilities` returns revision `1` and state `available`
when the ingress service exists, otherwise `not_configured`. It does not reveal
source configuration, delivery tokens or whether a particular source is enabled.

The bridge remains unchanged. Operations administration owns source-management
UI and callback URL projection; those consumers must emit the v2 URL before the
two legacy registration rows can be ratified. Existing external webhook
configurations require an explicit update; this implementation does not contact
providers or modify their registered destinations. There is no first-party
native or Jellyfin delivery consumer.
