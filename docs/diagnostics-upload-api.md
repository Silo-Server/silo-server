# Diagnostics upload API

The native v2 diagnostics ingress uses the existing diagnostics validator and
storage service. The bridge endpoints retain their wire contract.

`GET /api/v2/diagnostics/capabilities` requires a signed-in account access token;
API keys are refused. The capability document returns the account-specific status,
server instance identity, accepted manifest schema versions, bundle and manifest
limits, retention days and consent notice version. Capability state is `available`,
`disabled`, or `not_configured` according to diagnostics availability. A missing
service returns a dependency-unavailable problem. Treat `revision` as an opaque
string; cache the document privately with its ETag and revalidate with
`If-None-Match`. A bodyless `304` reuses the matching account-scoped document.

`upload_chunk_bytes` advertises the fixed chunk size when the v2 chunk service
is configured; zero means unsupported. Clients must use the capability from the
same API namespace as their upload transport.

`POST /api/v2/diagnostics/reports` requires the same account access token and is
refused in demo mode. Send `multipart/form-data` with exactly two file parts in
this order:

1. `manifest`, `application/json`, at most 65,536 bytes.
2. `bundle`, `application/gzip`, bounded by the advertised `max_bundle_bytes`.

The optional `X-Profile-Id` attributes the captured report. It must match the
manifest and belong to the authenticated account; child profiles are forbidden.
It is independent of the current viewer and does not require an active viewer
PIN. The existing service validates attribution, schema, destination, consent,
archive metadata and quota before accepting a report.

Ingress streams the gzip part through the existing validator without buffering
the complete multipart body in Huma. It applies the live bundle limit plus the
existing 128 KiB framing allowance, finite ten-minute upload deadlines and the
same process-local admission limiter used by bridge uploads and chunk completion.
The OpenAPI multipart schema is attached after Huma input registration so Huma's
compiled decoder does not pre-read the stream; the media gate still enforces the
declared request content type.

Success is `201` with `report_id` and `short_id`. Failures use native Problem
Details with the existing validation messages and corresponding HTTP statuses.
Disabled and unconfigured destinations return `409 capability_disabled` and
`409 capability_not_configured`, respectively, including chunk init and completion.
These availability failures are distinct from a transient dependency failure.
Explicit quota and busy rejections include `Retry-After`. Uploads are
`non_retryable`: an uncertain response may follow a completed report, so clients
must not replay automatically. `Retry-After` does not make an uncertain upload
safe to repeat.

The bridge and v2 chunk routes share sessions in process memory and temporary spool files,
with a fifteen-minute lifetime, sixteen-session cap and one session per account.
They require affinity to the creating process. Another replica or a process
restart may return `404`, requiring a fresh session; this is not durable
cross-replica resumability. Both transports retain those constraints.

## Chunk transport

All four chunk operations require a user access token and are refused in demo
mode. API keys cannot own or complete sessions.

| Operation | Request and result | Retry behavior |
| --- | --- | --- |
| `POST /api/v2/diagnostics/reports/uploads` | JSON `manifest` and positive `bundle_bytes`; `201` with `upload_id`, `chunk_bytes`, `total_chunks`, canonical UTC `expires_at` | Non-retryable; a new init replaces this account's previous session |
| `PUT /api/v2/diagnostics/reports/uploads/{upload_id}/chunks/{chunk_index}` | `application/octet-stream`, exactly the expected chunk size; `200` with `received_chunks` and `total_chunks` | The accepted index is immutable; retry only the same bytes with the same session/index |
| `POST /api/v2/diagnostics/reports/uploads/{upload_id}/complete` | No body; optional captured `X-Profile-Id`; `201` report receipt | Non-retryable; the session is consumed before ingest and there is no durable receipt replay |
| `DELETE /api/v2/diagnostics/reports/uploads/{upload_id}` | No body; `204` for an owned, absent or foreign session | Idempotent best-effort abort; foreign sessions are unaffected |

PUT checks account/session ownership before reading bytes. Its declared hard
body cap applies to both known and unknown content lengths, and compressed HTTP
request encodings are refused. Already received chunks cannot be replaced by a
retry. Init validates size and availability before reserving capacity; full
manifest/content validation remains at completion.

An incomplete session returns `409`. A busy completion retains the session and
returns `503` with `Retry-After`; a transient availability lookup failure also
retains it. A definitive disabled/unavailable status aborts the session. After
completion consumes the session, any ingest result leaves it spent. Missing,
expired, foreign, restarted or other-replica sessions return `404` for PUT and
completion. That absence does not prove that an earlier uncertain completion
failed to create a report. Clients must not automatically repeat an uncertain
completion or start a replacement report based on that `404`.

Apple and Android upload transports and the web diagnostics-status consumer must
adopt the v2 discovery and ingress together before migration ratification. The
Jellyfin compatibility surface has no corresponding diagnostics upload contract.
