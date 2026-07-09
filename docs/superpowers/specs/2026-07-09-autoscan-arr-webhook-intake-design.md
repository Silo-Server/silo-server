# Autoscan Sonarr/Radarr Webhook Intake

**Status:** Reviewed 2026-07-09 — decisions locked (see "Resolved review decisions")
**Date:** 2026-07-09
**Area:** `internal/autoscan`, `internal/api`, `web/src/pages/AdminAutoscan.tsx`, `web/src/pages/admin/autoscan/`, `migrations/sql/`

Commands assume the repository root is the cwd.

## Summary

Silo's current Sonarr/Radarr Autoscan integration is pull-based. The installed
`silo.autoscan.arr` plugin implements `scan_source.v1`; the host polls it on an
interval, passes a resolved `{base_url, api_key}` connection, the plugin reads
arr `/api/v3/history`, and the host applies path rewrites, resolves scan
targets, suppresses duplicates, enqueues scans, and records poll events.

That design works, but it means a Sonarr/Radarr Autoscan source needs an arr API
key. This spec adds a **webhook delivery mode** so Sonarr/Radarr can POST import,
upgrade, rename, and delete notifications directly to Silo. Webhook mode should
not require an arr connection or API key. The host still owns all security,
path-rewrite, scan-resolution, scan-queue, suppression, and event-bookkeeping
behavior.

The recommended v1 is **host-native webhook intake attached to Autoscan sources**,
using a **host-discovered built-in source identity** (`silo.autoscan.arr-webhook`)
so webhook sources require neither an arr API key nor an installed plugin.
No SDK change is required for v1. The existing pull plugin remains available for
polling mode and for operators who prefer API-based history polling.

## Current state

- `autoscan_sources` binds a source to `plugin_id`, `capability_id`, an optional
  `connection_id`, `enabled`, optional `poll_interval_seconds`, `path_rewrites`,
  `source_config`, label, marker, last run time, and last error.
- `autoscan_connections` can store own credentials or a soft link to a Requests
  integration. Keys are encrypted by `internal/secret`.
- `autoscan.Service.PollOnce` loads enabled sources, optionally resolves their
  connection, calls the plugin `PollChanges`, applies rewrites, resolves scan
  targets, suppresses duplicate work, enqueues scans, advances the marker on
  success, and writes `autoscan_events`.
- `scan_source.v1` is pull only. The SDK has no "plugin received a webhook;
  submit these paths to the host" RPC.
- Plugin HTTP routes exist and can be public, but they route by installation id
  and are not currently connected to Autoscan enqueueing.
- The current ARR plugin has no config. It reads connection data only from
  `PollChangesRequest.connection` and returns raw arr-side paths.

## Goals

- Let operators configure Sonarr/Radarr webhooks without storing an arr API key
  in Silo.
- Keep polling mode intact and compatible with existing Autoscan sources.
- Reuse the existing host path rewrite, resolution, suppression, enqueue, and
  event-history behavior for both poll and webhook deliveries.
- Give each webhook source a stable public callback URL with a rotatable secret.
- Support Sonarr/Radarr import, upgrade, rename, file-delete, and test events.
- Keep `/api/v1` additive: new fields and endpoints only.
- Avoid leaking webhook secrets or raw payloads in logs, events, or frontend
  responses beyond admin-only setup surfaces.

## Non-goals

- No automatic creation of Sonarr/Radarr webhook connections inside arr. That
  would require the arr API key this feature is trying to avoid.
- No SDK change for v1.
- No plugin-to-host push RPC for v1.
- No broad webhook framework for every future provider. This spec only defines
  Sonarr/Radarr webhook intake.
- No webhook signing requirement from Sonarr/Radarr beyond the generated Silo
  URL secret. If a future arr version supports request signing, add it
  additively.
- No replacement of the poll plugin. Polling remains useful for fallback,
  history recovery, and operators who prefer not to expose a public callback.

## Approach comparison

### Option A: Host-native webhook mode on Autoscan sources (recommended)

Silo adds a public secret-bearing endpoint such as
`POST /api/v1/autoscan/webhooks/{token}`. The endpoint resolves the token to an
Autoscan source, parses the Sonarr/Radarr payload in the host, turns it into
`autoscan.Change` values, and calls a new host-side `IngestChanges` path that
shares the same rewrite/resolve/suppress/enqueue/event logic as `PollOnce`.

**Pros**

- No SDK release or plugin release needed for v1.
- No arr API key needed for webhook-only sources.
- Stable callback URL is source-owned, not plugin-installation-id-owned.
- Host has direct access to scan queue, resolver, suppressor, DB, event logs,
  secret encryption, and rate limiting.
- Existing Autoscan activity UI and source rows remain the operator surface.
- The security boundary is simpler: one host-owned bearer secret, one host-owned
  parser, one host-owned enqueue path.

**Cons**

- Sonarr/Radarr payload parsing becomes host code, so provider-specific logic is
  no longer purely in plugins.
- Future non-arr webhook providers would need host work unless a later generic
  push contract is added.

A variant of Option A would reuse the `silo.autoscan.arr` plugin identity for
webhook sources. That was considered and rejected: `HandleCreateSource` hard
rejects any source whose `(plugin_id, capability_id)` is not a
currently-installed capability, so plugin-bound identity would force operators
to install a plugin the webhook path never calls — quietly defeating the
feature's own pitch. The built-in identity below avoids that at near-zero cost
(see "Recommended v1").

### Option B: SDK push RPC plus plugin HTTP route

Add an SDK/runtime-host RPC such as `SubmitScanSourceChanges`, update
`silo-server` to implement it, update the ARR plugin to declare `http_routes.v1`
and receive public webhook requests, then have the plugin parse payloads and
push changes to the host.

**Pros**

- Provider-specific webhook parsing lives in the provider plugin.
- The host can stay more generic once the push RPC exists.
- Future push-style providers can reuse the same plugin-to-host contract.

**Cons**

- Requires a coordinated SDK release, server update, plugin update, and catalog
  update before the feature exists.
- Plugin public URLs include installation id, so reinstalling or replacing the
  plugin can change callback URLs unless the host adds another stable alias.
- The plugin still needs durable webhook secret/config state or host-provided
  secret state.
- More moving parts in the most security-sensitive path.
- The feature cannot ship as a narrow server/API/UI change.

### Option C: Separate `arr-webhook` plugin with internal buffer

Create a new plugin that exposes a public webhook route, stores received paths
in an internal buffer, and returns them when the host later polls
`scan_source.v1`.

**Pros**

- No SDK push RPC required.
- Provider-specific parsing stays outside the host.
- Reuses the existing pull contract.

**Cons**

- Still has poll latency even though arr sent a real-time webhook.
- The plugin needs durable queue/buffer storage, idempotency, and cleanup. The
  current plugin model is not designed around plugin-owned durable event queues.
- Reinstalling the plugin risks losing buffered events or changing callback URLs.
- More operationally fragile than letting the host enqueue immediately.
- Creates a second ARR Autoscan plugin with overlapping purpose.

### Option D: Keep polling only

Improve docs or setup around current polling, but still require an arr API key.

**Pros**

- No implementation work.
- Pulling `/history` remains robust for recovery after Silo downtime.

**Cons**

- Does not satisfy the request.
- Still requires API keys.
- Adds latency compared to direct arr webhook delivery.

## Recommended v1

Implement Option A with a built-in source identity.

Add webhook delivery as a mode on Autoscan source rows. Existing poll sources
remain `delivery_mode = "poll"`. Webhook-only sources use
`delivery_mode = "webhook"` and do not require `connection_id`.

Webhook sources bind to a **host-discovered built-in source identity**,
`silo.autoscan.arr-webhook`, rather than the installed ARR plugin's capability.
`ScanSourceLister` is a narrow interface with a single adapter
(`PluginScanSourceLister` in `internal/api/autoscan_wiring.go`); wrap it in a
composite lister that appends one synthetic `DiscoveredSource` entry for the
built-in identity. Because the Add-source picker, create-time validation,
labels, and events all consume only `(plugin_id, capability_id, display_name)`,
they work unchanged. The provider is never invoked for webhook sources —
`PollOnce` skips them — so no `PollChanges` implementation exists for the
built-in identity, and none is needed.

For v1, `delivery_mode = "webhook"` is accepted only for the built-in
`silo.autoscan.arr-webhook` identity, and `delivery_mode = "poll"` is rejected
for it (it has no plugin to poll). Existing ARR plugin sources stay poll-mode.

## Data model

Add a Goose migration.

### `autoscan_sources`

Add:

| column | type / default | purpose |
|---|---|---|
| `delivery_mode` | text NOT NULL DEFAULT `'poll'` | `poll` or `webhook`; future `hybrid` can be added later |

Check constraint:

```sql
delivery_mode = ANY (ARRAY['poll'::text, 'webhook'::text])
```

Rules:

- Poll mode keeps today's behavior.
- Webhook mode skips `PollChanges` and does not require a connection.
- Webhook mode is valid only for the built-in `silo.autoscan.arr-webhook`
  identity; poll mode is invalid for it.
- Existing rows default to poll.
- `source_config.webhook_provider` may be `sonarr`, `radarr`, or `auto`.
  Use `auto` by default and infer from payload shape when possible.

### `autoscan_webhook_endpoints`

One active webhook endpoint per source for v1.

| column | type / default | purpose |
|---|---|---|
| `source_id` | uuid PK REFERENCES `autoscan_sources(id)` ON DELETE CASCADE | webhook owner |
| `secret_hash` | text UNIQUE NOT NULL | lookup hash of the bearer URL token |
| `secret_ref` | text NOT NULL | encrypted token, admin-display only |
| `secret_suffix` | text NOT NULL | last 6-8 chars for UI/debug |
| `created_at` | timestamptz NOT NULL DEFAULT now() | creation time |
| `rotated_at` | timestamptz | last token rotation |
| `last_received_at` | timestamptz | latest valid delivery |
| `last_error_at` | timestamptz | latest parse/ingest error |
| `last_error_message` | text NOT NULL DEFAULT '' | bounded sanitized error |

Secret handling:

- Generate at least 32 random bytes and encode URL-safe.
- Store a plain SHA-256 hash for lookup. For a 32-byte random token this is
  standard and sufficient (preimage resistance is what matters); an HMAC-keyed
  variant adds key-management complexity for no practical gain here.
- Store encrypted `secret_ref` using `internal/secret.Cipher` and AAD bound to
  `autoscan_webhook_endpoints.source_id`.
- Never log the token or full URL.

### `autoscan_webhook_deliveries`

Persist each actionable parsed delivery before returning `202`. Rows store the
source, provider event type, normalized changes, original receive time, retry
attempt count/backoff, and a bounded lease owner/time. The request attempts the
delivery immediately; transient resolver/database/queue failures release it for
an internal retry task. Workers claim due rows with `FOR UPDATE SKIP LOCKED`, so
multiple API nodes can drain the queue without double-owning a delivery. A
successful consume deletes the row; deleting the source cascades its pending
deliveries.

## API

All admin endpoints are additive under `/api/v1/admin/autoscan`.

### Source response additions

Add fields to `AutoscanSource` responses:

```json
{
  "delivery_mode": "poll",
  "webhook_configured": true,
  "webhook_url": "https://example/api/v1/autoscan/webhooks/...",
  "webhook_secret_suffix": "abc123",
  "webhook_last_received_at": "2026-07-09T12:34:56Z",
  "webhook_last_error_at": null,
  "webhook_last_error_message": ""
}
```

`webhook_url` is admin-only and stays redisplayable in admin source responses
(the encrypted `secret_ref` exists for exactly this). The token's blast radius
is small — the worst an attacker can do with it is trigger scans of paths that
already resolve inside Silo libraries — so one-time display would buy little
security while costing real operator pain (re-pasting into arr after a config
wipe would force a rotation).

### Source write additions

Extend create/update payloads:

```json
{
  "delivery_mode": "webhook",
  "source_config": {
    "webhook_provider": "sonarr"
  }
}
```

Validation:

- `delivery_mode = "poll"`: current validation stays. If enabled, a connection
  should be bound for ARR poll sources.
- `delivery_mode = "webhook"`: only valid with the built-in
  `silo.autoscan.arr-webhook` identity. No connection is required. A bound
  connection is allowed only for rewrite suggestions.
- The installed-capability check treats the built-in identity as always
  available (it comes from the composite lister, not plugin installations).
- `source_config.webhook_provider`: optional; when present must be `sonarr`,
  `radarr`, or `auto`.

### Webhook endpoint management

Add:

- `POST /admin/autoscan/sources/{id}/webhook`
  - Creates a webhook endpoint if missing and returns URL/status.
- `POST /admin/autoscan/sources/{id}/webhook/rotate`
  - Replaces the token, invalidates the old URL, returns the new URL.
- `DELETE /admin/autoscan/sources/{id}/webhook`
  - Deletes the endpoint and makes future deliveries return not found.

### Public delivery endpoint

Add:

```http
POST /api/v1/autoscan/webhooks/{token}
Content-Type: application/json
```

Responses:

- `202 Accepted`: valid token; event accepted, ignored, or enqueued. Use 202 for
  valid test events and valid disabled sources so arr setup tests do not fail
  noisily.
- `400 Bad Request`: valid token but malformed/unsupported payload.
- `404 Not Found`: token not found. Do not reveal whether a source exists.
- `405 Method Not Allowed`: non-POST.
- `413 Payload Too Large`: body exceeds the configured cap.
- `429 Too Many Requests`: per-token or per-IP rate limit exceeded.
- `500 Internal Server Error`: the delivery could not be durably persisted.

Sonarr/Radarr do not replay failed notification events. Silo must therefore
persist actionable deliveries before `202` and retry ingest failures internally;
HTTP error responses are not a delivery retry mechanism.

The body should be small; cap at 256 KiB unless real payload fixtures prove that
is too low.

## Host service design

Factor the existing `PollOnce` consume path so polling and webhooks share it.

Proposed shape:

```go
type ChangeIngest struct {
    SourceID          string
    DeliveryMode      string // "poll" or "webhook"
    ProviderEventType string
    Changes           []autoscan.Change
    ReceivedAt        time.Time
}

func (s *Service) IngestChanges(ctx context.Context, input ChangeIngest) (IngestResult, error)
```

Internally, introduce an unexported helper that both `PollOnce` and
`IngestChanges` call after changes are known:

```go
func (s *Service) consumeSourceChanges(ctx context.Context, src Source, changes []Change, opts consumeOptions) (consumeResult, error)
```

It should reuse:

- `rewriteChanges`
- `resolveAndClaim`
- `collapseTargetsToLibraryScans`
- `enqueueScanTargets`
- `releaseClaims`
- event creation/finish logic

Differences from polling:

- No marker is advanced for webhook deliveries.
- Webhook events use marker fields as empty/null.
- Webhook event creation skips the running-event single-flight exclusion (see
  "Security and reliability") — concurrent deliveries each ingest; the
  suppressor claim is the duplicate guard.
- `changes_returned`, `changes_resolved`, `targets_claimed`,
  `scans_created`, `scans_reused`, and `scans_suppressed` should be recorded
  exactly like poll events.
- If any resolve attempt fails transiently, return an error and record the
  event as error. Already-enqueued targets are allowed; a duplicate later
  delivery is safe.
- If all paths are outside Silo folders, finish as `unresolved`, same as poll.

`PollOnce` changes:

- Skip sources with `delivery_mode = "webhook"`.
- Continue current behavior for `delivery_mode = "poll"`.
- If a future `hybrid` mode is added, poll only when a connection is bound.

## Payload parsing

Implement parsing in a small ARR-specific host package, for example
`internal/autoscan/arrwebhook`.

Input:

```go
func Parse(provider string, body []byte) (ParsedWebhook, error)
```

Output:

```go
type ParsedWebhook struct {
    Provider  string
    EventType string
    Test      bool
    Changes   []autoscan.Change
}
```

Provider inference:

- If `source_config.webhook_provider` is `sonarr` or `radarr`, use it.
- If `auto`, infer from top-level `series` vs `movie`, or from top-level field
  prefixes if supporting flattened payloads.
- If inference fails on a non-test event, return a 400 with a sanitized message.

Events:

- `Test`: no-op success. Update `last_received_at`; do not enqueue.
- `Download`, `Import`, `Upgrade`, `DownloadComplete`: enqueue imported file
  paths as `ChangeScopeFile`.
- `Rename`: enqueue new and previous file paths as `ChangeScopeFile`. Previous
  paths may no longer exist; `resolveChange` already falls back through
  `ResolveVanishedPath` for file changes.
- `EpisodeFileDelete`, `MovieFileDelete`: enqueue deleted file path as
  `ChangeScopeFile` when present.
- Unknown event types: valid token, unsupported event. Return 202 no-op and
  record a low-noise status rather than making arr mark the webhook unhealthy.

Path extraction should support the common webhook JSON shapes and should be
covered by fixtures:

- Sonarr:
  - `episodeFile.path`
  - `episodeFiles[].path`
  - `episodeFile.previousPath`, `episodeFiles[].previousPath`
  - `series.path` as a subtree fallback only when no file path exists
- Radarr:
  - `movieFile.path`
  - `movieFiles[].path`
  - `movieFile.previousPath`, `movieFiles[].previousPath`
  - `movie.path` or `movie.folderPath` as a subtree fallback only when no file
    path exists

The parser should dedupe exact paths before returning changes.

## Security and reliability

- Public endpoint is unauthenticated but token-bearing.
- Token lookup should be constant-shape: invalid tokens return 404 without
  distinguishing disabled/deleted sources.
- Valid token but disabled source/global Autoscan disabled should return 202
  and perform no enqueue. This prevents arr retries or setup failures while
  respecting Silo's disabled state.
- Apply a per-token rate limit and a coarse per-IP limit. Build on the existing
  `internal/ratelimit` middleware (it already supports per-endpoint and per-IP
  limits) rather than introducing a new mechanism.
- Use `http.MaxBytesReader` or equivalent for body cap.
- Do not log request bodies. If an error needs context, log only source id,
  event type, provider, path count, and sanitized bounded error text.
- Never include token in event logs or regular app logs.
- Preserve current duplicate suppression. Duplicate webhook deliveries should
  at worst create suppressed Autoscan events, not duplicate scan storms.
- Honor configured burst capacity in both in-memory and Redis rate-limit
  backends; season-pack imports commonly deliver more than one event per second.
- Keep transient ingest failures in the durable delivery queue with bounded
  exponential backoff. A short-interval hidden task reclaims due or abandoned
  leases and retries them independently of the poll cadence.
- **Webhook deliveries must NOT be single-flighted and dropped.** The
  running-event exclusion in `CreateEvent` (advisory lock + running-row check)
  exists to stop overlapping polls of the same marker window. Poll cycles can
  safely be skipped because the marker window is re-read next cycle; a webhook
  delivery has no marker, so a dropped delivery is lost forever. Sonarr fires
  one webhook per import in rapid succession (a season pack is many imports in
  seconds), so "another event is running" is the normal case during a burst.
  `CreateEvent` takes a flag (or a separate path) that skips the running-event
  exclusion for webhook deliveries; the duplicate-scan guard for concurrent
  ingests is the suppressor's atomic `ShouldScan` claim, which webhook
  ingestion reuses. Poll-mode behavior is unchanged. If a `hybrid` mode ships
  later, the rule is "poll defers to webhook", never "webhook drops".

## Admin UI

Extend `AdminAutoscan` / `SourcesPanel`.

Source row/edit controls:

- Delivery mode segmented control: `Poll` / `Webhook`.
- Poll mode:
  - Existing connection picker, poll interval, rewrites, sync suggestions.
- Webhook mode:
  - Provider selector: `Auto`, `Sonarr`, `Radarr`.
  - Webhook URL display with copy button.
  - Rotate URL action.
  - Last received / last error status.
  - Connection picker hidden or clearly optional. If a connection is bound, it
    is used only for existing rewrite suggestions, not for ingestion.
- Keep path rewrites visible in both modes; webhook payload paths are arr-side
  paths and still need host-side rewrites in Docker/mount-mismatch setups.

Connections panel:

- No required changes, except copy should not imply a connection is mandatory
  for webhook-only sources.

Add-source picker:

- The built-in `silo.autoscan.arr-webhook` entry appears alongside installed
  plugin capabilities (supplied by the composite lister) with a display name
  like "Sonarr/Radarr Webhook". Picking it creates a webhook-mode source.

Activity panel:

- Show delivery mode and provider event type on event rows (the schema adds
  both fields in v1).

## Event metadata (required in v1)

Add fields to `autoscan_events`:

| column | type / default | purpose |
|---|---|---|
| `delivery_mode` | text NOT NULL DEFAULT `'poll'` | poll/webhook event source |
| `provider_event_type` | text NOT NULL DEFAULT '' | arr event type |

These ship in v1, in the same migration as the source/endpoint changes. They
are not required for the enqueue path, but debugging webhook intake without
knowing which delivery produced an event is exactly the pain this feature hits
in week one, and the cost is two columns in a migration being written anyway.

## Rollout

1. Add migrations for source delivery mode, webhook endpoint table, and
   `autoscan_events` delivery-mode/provider-event-type columns.
2. Add the composite `ScanSourceLister` exposing the built-in
   `silo.autoscan.arr-webhook` identity.
3. Add repository methods for webhook endpoint create/get-by-hash/rotate/delete
   and source delivery-mode fields, plus the `CreateEvent` no-exclusion path
   for webhook deliveries.
4. Refactor `autoscan.Service` to share the consume path and add
   `IngestChanges`.
5. Add ARR webhook parser fixtures.
6. Add public route and handler.
7. Add admin endpoint fields and endpoint-management routes.
8. Update frontend types/hooks and `SourcesPanel`.
9. Keep all existing source rows as poll mode.
10. Update docs or setup text after review.

## Testing

Backend:

- Migration applies and rolls back.
- Existing poll-mode service tests still pass.
- `PollOnce` skips webhook-mode sources.
- The composite lister exposes the built-in identity; source creation accepts
  it for webhook mode and rejects poll mode for it.
- `IngestChanges` applies rewrites and enqueues the same targets as polling for
  equivalent changes.
- Actionable deliveries are persisted before processing; transient failures
  return `202`, remain queued, and are consumed by the retry task.
- Delivery leases are ownership-guarded and reclaimable after worker failure.
- Concurrent/burst webhook deliveries for one source all ingest — none are
  dropped by the running-event exclusion; duplicate targets are suppressed by
  the suppressor claim.
- Duplicate webhook deliveries are suppressed by existing debounce logic.
- Webhook events record `delivery_mode` and `provider_event_type`.
- Disabled source and disabled global setting return accepted/no-op behavior.
- Invalid token returns 404; malformed payload for valid token returns 400.
- Body cap returns 413.
- Rate limit returns 429.
- Redis rate limiting honors the configured webhook burst allowance.
- Upgrade payloads reconcile both the new file and arr's `deletedFiles` paths.
- Token rotation invalidates old token and accepts new token.
- Parser fixture tests for Sonarr Download, Rename, EpisodeFileDelete, Test.
- Parser fixture tests for Radarr Download, Rename, MovieFileDelete, Test.
- Unknown event type is accepted/no-op.
- No logs include the token or raw request body.

Frontend:

- Source can switch from poll to webhook mode without selecting a connection.
- Webhook URL renders only for configured endpoint.
- Rotate action replaces the URL.
- Provider selector writes `source_config.webhook_provider`.
- Poll mode behavior remains unchanged.
- Rewrite editor remains available in webhook mode.

Validation commands:

```bash
GOWORK=off go test ./internal/autoscan ./internal/api/handlers
cd web && pnpm run lint
cd web && pnpm run format:check
make verify-local-paths
```

Broader validation after implementation:

```bash
GOWORK=off go test ./...
cd web && pnpm run build
```

## Resolved review decisions (2026-07-09)

1. **Built-in source identity.** Webhook mode uses a host-discovered
   `silo.autoscan.arr-webhook` identity, not the installed ARR plugin's
   capability. Create-time validation requires an installed capability, so
   plugin-bound identity would force installing a plugin the webhook path never
   calls; the composite-lister alternative is ~20 lines and everything
   downstream consumes only `(plugin_id, capability_id, display_name)`.
2. **Webhook URL stays redisplayable** in admin-only responses. Low token blast
   radius (worst case: spurious scans of paths already inside Silo libraries);
   one-time display would force rotations after any config loss.
3. **No `hybrid` in v1.** Purely additive later; shipping it now forces
   poll/webhook overlap semantics to be solved before any field experience.
4. **Unknown event types return 202 no-op.** Arr adds event types over time; a
   400 makes arr mark the webhook unhealthy and nag the operator about
   something Silo intentionally ignores.
5. **`autoscan_events.delivery_mode` and `provider_event_type` ship in v1.**
   Two columns in a migration being written anyway; needed for week-one
   debugging of webhook intake.
6. **Parser lives in `internal/autoscan/arrwebhook`.** Fixture-testable, keeps
   handlers thin, matches the repo's domain-package organization.
7. **Webhook deliveries are never single-flight-dropped** (raised during
   review, not an original question). Poll skips are safe because the marker
   window is re-read; webhooks have no marker, and Sonarr bursts (season packs)
   make a running event the normal case. See "Security and reliability".

## References

- Current Autoscan plugin architecture:
  `docs/superpowers/specs/2026-06-02-autoscan-plugin-architecture-design.md`
- Current ARR polling design:
  `docs/superpowers/specs/2026-06-02-autoscan-arr-polling-design.md`
- Current rewrite sync design:
  `docs/superpowers/specs/2026-06-02-autoscan-rewrite-sync-design.md`
- Current SDK pull contract: sibling `silo-plugin-sdk` repo,
  `proto/silo/plugin/v1/scan_source.proto`
- Current ARR plugin: sibling `silo-plugin-autoscan-arr` repo
- Servarr path/event references:
  - Sonarr custom scripts: <https://wiki.servarr.com/sonarr/custom-scripts>
  - Radarr custom scripts: <https://wiki.servarr.com/radarr/custom-scripts>
