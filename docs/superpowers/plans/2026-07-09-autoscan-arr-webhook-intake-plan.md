# Implementation Plan: Autoscan Sonarr/Radarr Webhook Intake

**Spec:** `docs/superpowers/specs/2026-07-09-autoscan-arr-webhook-intake-design.md`
(Option A, decisions locked — read the spec's "Resolved review decisions" before
starting.)
**Date:** 2026-07-09

Commands assume the repository root is the cwd.

## Overview

Add a webhook delivery mode to Autoscan so Sonarr/Radarr can POST import,
upgrade, rename, and delete notifications directly to Silo without an arr API
key. Webhook sources bind to a host-discovered built-in identity
(`silo.autoscan.arr-webhook`), never the installed ARR plugin. The host parses
payloads and feeds the existing rewrite → resolve → suppress → enqueue → event
pipeline.

Non-negotiable design points from the review:

1. **Built-in source identity** — no plugin install required for webhook mode.
2. **Webhook deliveries are never single-flight-dropped.** Only poll cycles may
   be skipped when a running event exists (their marker window is re-read).
3. Event rows record `delivery_mode` and `provider_event_type` from day one.
4. Unknown arr event types → `202` no-op. Secret lookup uses plain SHA-256.
   Webhook URL stays redisplayable on admin endpoints.

Suggested commit sequence is one commit per phase below, all on one branch, PR
subject `feat(autoscan): add Sonarr/Radarr webhook intake`.

## Phase 1 — Migration

Create with `make migrate-create NAME=autoscan_webhook_intake` (single
timestamped Goose migration; do not hand-number, do not create paired
up/down files).

Up:

```sql
ALTER TABLE autoscan_sources
    ADD COLUMN delivery_mode text NOT NULL DEFAULT 'poll'
        CONSTRAINT autoscan_sources_delivery_mode_check
        CHECK (delivery_mode = ANY (ARRAY['poll'::text, 'webhook'::text]));

CREATE TABLE autoscan_webhook_endpoints (
    source_id          uuid PRIMARY KEY
                       REFERENCES autoscan_sources(id) ON DELETE CASCADE,
    secret_hash        text UNIQUE NOT NULL,
    secret_ref         text NOT NULL,
    secret_suffix      text NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    rotated_at         timestamptz,
    last_received_at   timestamptz,
    last_error_at      timestamptz,
    last_error_message text NOT NULL DEFAULT ''
);

ALTER TABLE autoscan_events
    ADD COLUMN delivery_mode text NOT NULL DEFAULT 'poll',
    ADD COLUMN provider_event_type text NOT NULL DEFAULT '';
```

Down reverses all three (drop table, drop columns). Verify with
`make migrate-status` / `make migrate-up` against a scratch DB, and check
rollback.

## Phase 2 — Built-in source identity

**`internal/autoscan/discovery.go`**

- Add exported constants:
  - `BuiltinArrWebhookPluginID = "silo.autoscan.arr-webhook"`
  - `BuiltinArrWebhookCapabilityID = "arr-webhook"`
  - display name `"Sonarr/Radarr Webhook"`.
- Add a composite lister so the built-in identity lives beside the discovery
  types it extends (not in wiring code):

  ```go
  // WithBuiltinSources wraps a ScanSourceLister and appends host-built-in
  // source identities that need no plugin installation.
  func WithBuiltinSources(inner ScanSourceLister, builtins ...DiscoveredSource) ScanSourceLister
  ```

  It must tolerate `inner == nil` (return only builtins) because
  `Service.lister` is already allowed to be nil.

**`internal/api/autoscan_wiring.go`**

- In `BuildAutoscanService`, wrap the existing lister:

  ```go
  autoscan.WithBuiltinSources(
      PluginScanSourceLister{installationStore},
      autoscan.BuiltinArrWebhookSource(), // helper returning the DiscoveredSource
  )
  ```

Tests (`internal/autoscan/discovery_test.go`): builtin appears with and without
an inner lister; plugin entries pass through unchanged.

## Phase 3 — Repository and types

**`internal/autoscan/types.go`**

- `Source`: add `DeliveryMode string` (values `"poll"`/`"webhook"`; constants
  `DeliveryModePoll`, `DeliveryModeWebhook`).
- `Event`: add `DeliveryMode string`, `ProviderEventType string`.
- `EventCreate`: add `DeliveryMode string`, `ProviderEventType string`,
  `SkipRunningCheck bool`.
- New:

  ```go
  type WebhookEndpoint struct {
      SourceID         string
      SecretSuffix     string
      CreatedAt        time.Time
      RotatedAt        *time.Time
      LastReceivedAt   *time.Time
      LastErrorAt      *time.Time
      LastErrorMessage string
  }
  ```

  The plaintext token and hash never live on the struct that flows to
  handlers/responses; creation/rotation return the token separately.

**`internal/autoscan/repository.go`**

- Thread `delivery_mode` through `CreateSource`, `UpdateSource`,
  `ListSources`, `ListEnabledSources`, `GetSource` (default `'poll'` when
  empty on write).
- `CreateEvent`: write the two new columns. When `in.SkipRunningCheck` is
  true, use the simple no-transaction insert path (the same shape as the
  existing `sourceID == ""` branch, but with the source id set) — no advisory
  lock, no running-row check. Poll-mode behavior is unchanged.
- `FinishEvent` / `ListEvents` / `ListRunningEvents` / scan-event joins:
  select and populate the new event fields.
- Webhook endpoint methods (token = 32 bytes from `crypto/rand`,
  `base64.RawURLEncoding`; hash = hex SHA-256 of the token; `secret_ref` =
  `r.cipher.Encrypt(token, secret.RowAAD("autoscan_webhook_endpoints", "secret_ref", sourceID))`;
  suffix = last 6 chars of the token):

  ```go
  // CreateWebhookEndpoint creates (or returns the existing) endpoint; token is
  // "" when the endpoint already existed.
  CreateWebhookEndpoint(ctx, sourceID string) (WebhookEndpoint, token string, err error)
  RotateWebhookEndpoint(ctx, sourceID string) (WebhookEndpoint, token string, err error)
  DeleteWebhookEndpoint(ctx, sourceID string) error
  GetWebhookEndpoint(ctx, sourceID string) (WebhookEndpoint, error)
  // RevealWebhookToken decrypts secret_ref for admin redisplay.
  RevealWebhookToken(ctx, sourceID string) (string, error)
  // ResolveWebhookToken maps a raw delivery token to its source. Constant
  // shape: hash the token, look up by secret_hash, join autoscan_sources.
  // Returns ErrNotFound for unknown hashes.
  ResolveWebhookToken(ctx, token string) (Source, WebhookEndpoint, error)
  TouchWebhookReceived(ctx, sourceID string) error
  RecordWebhookError(ctx, sourceID, msg string) error // bounded, sanitized by caller
  ```

DB-backed tests follow the existing `SILO_TEST_DATABASE_URL` pattern used by
the rest of `internal/autoscan`. Cover: create/rotate invalidates the old
token, resolve-by-token round-trip, cascade delete with the source, reveal
decrypts to the original token.

## Phase 4 — Service: shared consume path + `IngestChanges`

**`internal/autoscan/service.go`**

- Extract everything in `PollOnce` after `PollChanges` returns into:

  ```go
  type consumeOptions struct {
      EventID     int64
      Marker      string // poll only; "" for webhook
      NextMarker  string // poll only
      AdvanceMarker bool // false for webhook
  }

  func (s *Service) consumeSourceChanges(ctx context.Context, src Source, changes []Change, opts consumeOptions) error
  ```

  It owns: `rewriteChanges` → `resolveAndClaim` → target-cap collapse →
  `enqueueScanTargets` (+ `releaseClaims` on failure) → the
  transient-error / unresolved / success status decision → `finishEvent`.
  Marker advancement stays conditional on `opts.AdvanceMarker`; the hold-marker
  comments and semantics in `PollOnce` must survive the refactor intact.
- `PollOnce`: skip sources with `src.DeliveryMode == DeliveryModeWebhook`
  (before the interval check), then delegate to `consumeSourceChanges`.
  Existing poll tests in `service_test.go` must pass unmodified except for
  construction changes.
- Add:

  ```go
  type ChangeIngest struct {
      SourceID          string
      ProviderEventType string
      Changes           []Change
      ReceivedAt        time.Time
  }

  type IngestResult struct {
      Enqueued   int
      Suppressed int
      Unresolved bool
  }

  func (s *Service) IngestChanges(ctx context.Context, in ChangeIngest) (IngestResult, error)
  ```

  Behavior:
  - Load the source; reject non-webhook sources.
  - `CreateEvent` with `DeliveryMode: "webhook"`, `ProviderEventType`,
    `SkipRunningCheck: true` — deliveries are never dropped because another
    event is running (spec: "Security and reliability").
  - Delegate to `consumeSourceChanges` with `AdvanceMarker: false` and empty
    markers.
  - Transient resolve failure → finish event as error and return an error (the
    handler maps it; a duplicate later delivery is safe). All paths outside
    Silo folders → finish as `unresolved`, return success.

Service tests: webhook ingest enqueues the same targets as an equivalent poll;
concurrent ingests for one source both complete (no `ErrPollAlreadyRunning`);
suppressor claims dedupe overlapping targets; `PollOnce` skips webhook sources.

## Phase 5 — Parser: `internal/autoscan/arrwebhook`

New package, no dependencies on api/handlers.

```go
type ParsedWebhook struct {
    Provider  string // "sonarr" | "radarr"
    EventType string
    Test      bool
    Changes   []autoscan.Change
}

func Parse(provider string, body []byte) (ParsedWebhook, error)
```

- `provider` is `sonarr`, `radarr`, or `auto`; `auto` infers from top-level
  `series` vs `movie` keys. Inference failure on a non-test event returns a
  typed error the handler maps to 400 (sanitized message, no payload echo).
- Event handling per the spec's "Payload parsing" section: Test → no changes;
  Download/Import/Upgrade/DownloadComplete → imported file paths; Rename → new
  **and** previous paths; EpisodeFileDelete/MovieFileDelete → deleted path.
  All paths emit `ChangeScopeFile` (`resolveChange` already falls back through
  `ResolveVanishedPath` for vanished files). Series/movie folder paths are a
  subtree fallback (`ChangeScopeSubtree`) only when no file path exists.
- Unknown event types return `ParsedWebhook` with no changes and a marker the
  handler can distinguish (e.g. `EventType` set, `Changes` empty, no error) —
  they must not be an error.
- Dedupe exact paths before returning.

Fixtures in `internal/autoscan/arrwebhook/testdata/`: Sonarr Download, Rename,
EpisodeFileDelete, Test; Radarr Download, Rename, MovieFileDelete, Test; plus
an unknown-event and a malformed body. Build them from the Servarr custom-
scripts docs linked in the spec.

## Phase 6 — HTTP: public delivery route + admin endpoints

**New file `internal/api/handlers/autoscan_webhook.go`**

Public handler `HandleWebhookDelivery` (`POST /api/v1/autoscan/webhooks/{token}`):

1. `http.MaxBytesReader` at 256 KiB → 413 on overflow.
2. Hash token, `ResolveWebhookToken` → 404 on miss (identical body for
   deleted/never-existed; do not distinguish).
3. Global autoscan disabled or source disabled → 202, no enqueue, still
   `TouchWebhookReceived`.
4. `arrwebhook.Parse` with `source_config.webhook_provider` (default `auto`)
   → 400 on parse error; 202 no-op for Test and unknown event types.
5. `IngestChanges`; on success `TouchWebhookReceived` and return `202`. On
   transient ingest failure, `RecordWebhookError` (bounded message) and return
   `500` so arr retries — a duplicate later delivery is safe. Log only source
   id, provider, event type, and path count. **Never log the token, URL, or
   body.**

Admin handlers (same file, wired through `AutoscanHandler` deps):

- `POST /admin/autoscan/sources/{id}/webhook` → create-if-missing, return
  URL + status.
- `POST /admin/autoscan/sources/{id}/webhook/rotate` → rotate, return new URL.
- `DELETE /admin/autoscan/sources/{id}/webhook` → delete.
- URL construction: `deps.PublicURL + "/api/v1/autoscan/webhooks/" + token`
  when `PublicURL` is set; otherwise return the relative path and let the UI
  prepend `window.location.origin`. Response field is `webhook_url` either way.

**`internal/api/handlers/autoscan.go`**

- `sourceResponse`: add `delivery_mode`, `webhook_configured`, `webhook_url`,
  `webhook_secret_suffix`, `webhook_last_received_at`, `webhook_last_error_at`,
  `webhook_last_error_message` (fetch endpoint rows for listed sources; a
  single batched query, not N+1).
- `HandleCreateSource` / `HandleUpdateSource` validation:
  - `delivery_mode` defaults to `poll`; must be `poll` or `webhook`.
  - `webhook` is valid only for the built-in identity; `poll` is invalid for
    the built-in identity.
  - Webhook mode requires no connection; a bound connection is allowed
    (rewrite suggestions only).
  - `source_config.webhook_provider` ∈ {`sonarr`, `radarr`, `auto`} when set.
  - The installed-capability check passes for the built-in identity
    automatically because the composite lister supplies it — verify with a
    handler test rather than special-casing `scanSourceInstalled`.
- Event list responses: add `delivery_mode` and `provider_event_type`.

**`internal/api/router.go`**

- Admin routes: add the three webhook-management routes next to the existing
  `/autoscan/sources` block.
- Public route: register `r.Post("/autoscan/webhooks/{token}", ...)` alongside
  the other public routes (near the auth/discord-callback blocks), gated on
  `autoscanHandler != nil`.
- Rate limiting: reuse `internal/ratelimit` — mirror the
  `Middleware.AuthEndpointHandler(endpoint)` per-IP pattern with a
  `"autoscan_webhook"` endpoint entry (config default on the generous side,
  e.g. 60/min burst 30; arr bursts are legitimate). Per-token limiting beyond
  that is unnecessary for v1 given the suppressor already collapses duplicate
  work; note this in the PR.

Handler tests (`internal/api/handlers`): status-code matrix from the spec's
Testing section — 202 (ok / test / disabled / unknown event), 400, 404, 405
(chi handles), 413, 429; rotation invalidates the old token; no token/body in
logs (assert via a captured slog handler).

## Phase 7 — Frontend

- `web/src/api/types.ts`: extend `AutoscanSource` with the new response
  fields; add webhook endpoint response type.
- `web/src/hooks/queries/useAutoscan.ts` (+ `keys.ts`): mutations for webhook
  create/rotate/delete; invalidate the sources query on success.
- `web/src/pages/admin/autoscan/SourcesPanel.tsx`:
  - Delivery mode segmented control (`Poll` / `Webhook`) in the source
    create/edit form. Only offered when the picked capability is the built-in
    webhook identity (which forces webhook mode) or a plugin capability
    (which forces poll mode) — in practice the mode follows the picker choice;
    render the control as the mode indicator rather than a free toggle.
  - Webhook mode panel: provider select (`Auto`/`Sonarr`/`Radarr` →
    `source_config.webhook_provider`), webhook URL display with copy button
    (prepend `window.location.origin` when the API returns a relative path),
    rotate action with confirm, last-received / last-error status line.
  - Connection picker hidden in webhook mode (or shown as explicitly optional
    for rewrite suggestions); poll-interval input hidden.
  - Path-rewrite editor stays visible in both modes.
- `web/src/pages/admin/autoscan/ActivityPanel.tsx`: show delivery mode and
  provider event type on event rows.
- Follow existing panel patterns; 2-space/double-quote Prettier config.

## Phase 8 — Verification

```bash
GOWORK=off go test ./internal/autoscan/... ./internal/api/handlers
GOWORK=off go test ./...
cd web && pnpm run lint && pnpm run format:check && pnpm run build
make lint
make verify-local-paths
```

Manual smoke (dev): create a webhook source, paste the URL into a Sonarr
Connect → Webhook (On Import + On Rename + On File Delete), use Sonarr's
"Test" button (expect 202 and `last_received_at` updating), import a file,
confirm a webhook event row and a file-scope scan appear in the Activity
panel.

## Risks / notes

- The `PollOnce` refactor is the riskiest step: marker-advancement semantics
  are subtle and documented inline — preserve the decision comments and rely
  on the existing `service_test.go` suite as the regression harness before
  adding webhook paths.
- `secret_ref` uses `internal/secret.Cipher` with row-bound AAD; never rename
  the table/column in SQL later without a `db_loader`-style fallback
  (encryption is AAD-bound).
- Keep `/api/v1` additive: every API change above is a new field, new
  endpoint, or new route; nothing existing is renamed or repurposed.
- Client repos: none required for v1 — this is an admin-web-only surface; the
  public endpoint is consumed by arr, not by Silo clients.
