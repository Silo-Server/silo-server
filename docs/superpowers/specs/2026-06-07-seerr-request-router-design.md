# silo-plugin-requests-seerr Design

**Date:** 2026-06-07
**Status:** Approved (brainstorming)
**Related:** `2026-06-07-requests-pluginization-design.md` (the `request_router.v1` contract), `2026-06-07-schema-driven-config-form-design.md` (the `AdminFormDescriptor`/`SchemaForm` this plugin renders into). Sibling implementation: `silo-plugin-requests-arr` (the template).

## Goal

A second `request_router.v1` fulfillment backend that satisfies Silo content requests by submitting them to a **Seerr** instance (`https://github.com/seerr-team/seerr` — an Overseerr/Jellyseerr-compatible request manager that internally manages its own Sonarr/Radarr). Seerr owns its arr routing; this plugin is a thin adapter that translates a Silo request into Seerr API calls and maps Seerr's status back.

## Why this exists (the agnosticism validator)

The `request_router.v1` contract and the schema-driven config form were built to be backend-agnostic. The arr plugin (rich 14-field config, Sonarr/Radarr-specific) is the only consumer so far, so "agnostic" is unproven. Seerr is deliberately *differently shaped*:

- Connection config is nearly **empty** (one boolean) vs. arr's fourteen fields and two sections.
- `mediaType` vocabulary is `movie`/`tv`, not Silo's `movie`/`series`.
- Media is identified by **TMDB id only**; there are no per-request root folders, quality profiles, or tags (Seerr owns those internally).
- 4K is a per-request boolean, not a separate instance.

If this plugin works against the existing contract and the existing `SchemaForm` **with zero changes to silo-server or the SDK**, the contract is proven agnostic. That zero-host-change constraint is a hard success criterion of this design.

## Scope

**In scope:** a new repository `/opt/silo-plugin-requests-seerr` (module `github.com/Silo-Server/silo-plugin-requests-seerr`) containing the plugin binary, its Seerr HTTP client, its manifest, and unit tests.

**Out of scope (hard constraint):** any change to `silo-server` (`/opt/silo`) or the SDK (`/opt/silo-plugin-sdk`). If implementation reveals a genuine contract gap that *requires* a host/SDK change, that is a finding to escalate — not something to patch around — because it would mean the contract is not actually agnostic.

**Local-only:** commits only; no push/tag/PR. `go.mod` keeps the machine-local `replace github.com/Silo-Server/silo-plugin-sdk => /opt/silo-plugin-sdk` (swapped for a published version only at publish time, per the requests-pluginization pre-publish checklist).

## Architecture

Structurally a clone of the arr plugin:

```
/opt/silo-plugin-requests-seerr/
  main.go                    # entrypoint: embed+checksum manifest, serve Runtime + RequestRouter
  manifest.json              # plugin_id silo.requests.seerr, capability id "seerr"
  go.mod / go.sum            # module + local SDK replace
  internal/
    router/
      server.go              # the 5 RequestRouter RPCs (Fulfill/CheckStatus/ListConfigOptions/TestConnection/Validate)
      server_test.go
      config.go              # parse RouterConnection.config -> connection settings (supports_4k)
    seerr/
      client.go              # Overseerr-compatible HTTP client (X-Api-Key)
      types.go               # request/response DTOs + status enums
      client_test.go
```

### main.go

Identical pattern to the arr plugin: `//go:embed manifest.json`, `publicmanifest.Load`, self-checksum via `sha256(os.Executable())` replacing the `"__CHECKSUM__"` placeholder, optional ldflags `version` override, then `runtime.Serve(runtime.ServeConfig{Servers: runtime.CapabilityServers{Runtime: &runtimeServer{...}, RequestRouter: router.New()}})`. `runtimeServer` implements `GetManifest` + a no-op `Configure`. Only `router.New()` differs from the arr plugin.

### The Seerr HTTP client (`internal/seerr`)

A small stateless client constructed per call from `(baseURL, apiKey)` (carried on `RouterConnection`, not config). All requests send the `X-Api-Key` header. Base path `/api/v1`. Methods:

- `CreateRequest(ctx, CreateRequestBody) (*MediaRequest, error)` → `POST /api/v1/request`.
- `GetRequest(ctx, id) (*MediaRequest, error)` → `GET /api/v1/request/{id}`.
- `FindExistingRequest(ctx, tmdbID int, is4k bool) (*MediaRequest, error)` → `GET /api/v1/request?take=100&sort=added` (the request list returns `MediaRequest` objects that carry the request `id`, per-request `is4k`, and nested `media.tmdbId`; `sort=added` pins deterministic newest-first ordering so a just-created duplicate is reliably in the scanned page), scanned for the entry matching `(tmdbId, is4k)`. Overseerr's request list has no per-TMDB filter, so the scan is bounded to the 100 most-recent requests; a duplicate older than that window returns the not-found sentinel (the caller then fails the target honestly rather than emitting a phantom). This is preferred over `GET /api/v1/media` because the media list does not directly carry the request id that 409 recovery needs. Used **only** on the `409` duplicate path to recover the existing request id. Bounded scan; returns a not-found sentinel if no match in the page.
- `Me(ctx) error` → `GET /api/v1/auth/me` (connection test; 200 = ok).

Non-2xx responses are wrapped as a typed `*APIError{StatusCode int, Message string}` (Message parsed from Seerr's `{ "message": ... }` error body when present). `409 Conflict` (duplicate request) is surfaced distinctly (e.g. `errors.Is(err, ErrDuplicate)` or a `StatusCode==409` check) so the router can recover the existing request id (see Fulfill step 4).

`CreateRequestBody` fields (JSON tags per the Overseerr API): `mediaType string` (`"movie"|"tv"`), `mediaId int`, `is4k bool`, `seasons any` (the string `"all"` for TV; omitted for movies). `serverId`/`profileId`/`rootFolder`/`userId` are intentionally **not** sent (Seerr default-routes; attribution defaults to the API-key owner).

`MediaRequest` DTO: `ID int`, `Status int` (MediaRequestStatus), nested `Media struct { Status int }` (MediaStatus). Enums (from Overseerr `server/constants/media.ts`):

- `MediaRequestStatus`: `1 PENDING, 2 APPROVED, 3 DECLINED, 4 FAILED, 5 COMPLETED`.
- `MediaStatus`: `1 UNKNOWN, 2 PENDING, 3 PROCESSING, 4 PARTIALLY_AVAILABLE, 5 AVAILABLE, 6 DELETED`.

### The router server (`internal/router/server.go`)

`Server` embeds `pluginv1.UnimplementedRequestRouterServer`, holds no state (`New() *Server`).

**`Fulfill(ctx, *FulfillRequest) (*FulfillResponse, error)`**

1. Read the descriptor: `mediaType` = `series → "tv"` else `"movie"`; `tmdbID` = `external_ids["tmdb"]` parsed to int. If `tmdb` is missing/unparseable the request cannot be routed to Seerr at all, so `Fulfill` short-circuits **before** the connection loop and returns a single request-level `FulfillResponse{Message: "request has no TMDB id; Seerr requires a TMDB id"}` (zero targets) — rather than emitting one identical failed target per connection×quality.
2. For each `connection` in `req.connections` (already media-type-filtered + single-installation-contained by the host): parse `config` → `supports4k bool`.
3. For each `quality` in `req.qualities`, plan a target:
   - If `quality` is the **4K tier** (the host's 2160p/4K quality constant) and `supports4k` is false → **skip** (emit no target; the connection does not fulfill 4K).
   - Otherwise build `CreateRequestBody{mediaType, mediaId: tmdbID, is4k: <quality is the 4K tier>, seasons: "all" if tv}` and `POST`.
4. Per target, map the outcome to a `FulfillmentTarget{quality, connection_id, external_id, external_status, status, message}`:
   - success with a usable id (`resp.ID > 0`) → `external_id = fmt.Sprint(resp.ID)`, `status = "queued"`, `external_status = fmt.Sprint(resp.Media.Status)` (the **media** status, the same enum CheckStatus reports — not the request status, so `external_status` means the same thing across Fulfill and CheckStatus).
   - **id-less success OR `409` duplicate → recover the id.** Two cases share one recovery path: (a) a 2xx create whose body is empty/has no id (`resp.ID <= 0` — the client tolerates an empty 2xx body as success), and (b) a `409` (media already requested). Both call `FindExistingRequest(ctx, tmdbID, is4k)`: if found → `status = "queued"`, `external_id = fmt.Sprint(found.ID)`, `external_status = fmt.Sprint(found.Media.Status)`, `message = "already requested in Seerr"` (duplicate) / `"created in Seerr"` (empty-body) — this target reconciles normally. If the lookup finds nothing → `status = "failed"` with a clear message (an honest visible state, never a phantom queued target with no id). **A blank/zero `external_id` is never emitted for a `queued` target.**
   - any other error → `status = "failed"`, `message = <APIError.Message or err>`. Per-target containment: a failure on one quality/connection never aborts the others.
5. If zero targets were planned across all connections (e.g. only a 4K request to a non-4K-capable connection), return `FulfillResponse{Message: "no Seerr connection fulfills the requested quality"}` (mirrors the arr plugin's empty-plan message).

**`CheckStatus(ctx, *CheckStatusRequest) (*CheckStatusResponse, error)`**

Index `req.connections` by id, building one Seerr client per connection (reused across that connection's targets). For each `TargetRef{quality, connection_id, external_id}`: if the connection is missing or the `external_id` isn't a positive int → skip; else `GET /api/v1/request/{external_id}` and map to `TargetStatus{quality, connection_id, status, external_status, message}`. Error handling: a **`404`** (the request was deleted/purged in Seerr) maps to a terminal `status = "failed"` (`message = "request no longer present in Seerr"`) so the target doesn't poll forever; any **other** error (network, 5xx) → skip that target and retry next cycle (don't blank the whole response). Status mapping (evaluated in this order):

| Silo status   | Condition |
|---------------|-----------|
| `failed`      | `request.Status == 3 (DECLINED)` or `== 4 (FAILED)` |
| `completed`   | `request.Status == 5 (COMPLETED)` or `media.Status == 5 (AVAILABLE)` |
| `downloading` | `media.Status == 3 (PROCESSING)` or `== 4 (PARTIALLY_AVAILABLE)` |
| `queued`      | otherwise (`media.Status` `1 UNKNOWN`/`2 PENDING`, request approved/pending) |

`external_status` = `fmt.Sprint(media.Status)` (raw, for display).

**`ListConfigOptions(ctx, *ListConfigOptionsRequest) (*ListConfigOptionsResponse, error)`** → `&ListConfigOptionsResponse{}` (empty `options_by_field`). Seerr's config has no dynamic-options fields, so the host never needs runtime options. (Implemented as an explicit empty response rather than `Unimplemented` so the host's options probe gets a clean answer.)

**`TestConnection(ctx, *TestConnectionRequest) (*TestConnectionResponse, error)`** → parse the connection, `GET /api/v1/auth/me`. 200 → `{Ok: true}`. Non-200 / error → `{Ok: false, Message: <status/error>}`. Never returns a gRPC error (failure is `Ok:false` + message), matching the arr plugin.

**`Validate(ctx, *ValidateRequest) (*ValidateResponse, error)`** → `&ValidateResponse{}` (no field errors, no form error). The single `supports_4k` boolean has no cross-field rules. (Explicit empty response so the host's save-time `Validate` call succeeds cleanly.)

### config.go

`type Connection struct { Supports4K bool }`. `connectionFromRouter(c *pluginv1.RouterConnection) Connection` reads `c.GetConfig()` (a `structpb.Struct`) and extracts `supports_4k` as a bool (absent → false). `baseURL` and `apiKey` come from `c.GetBaseUrl()` / `c.GetApiKey()`.

## The manifest

`manifest.json`:

```jsonc
{
  "plugin_id": "silo.requests.seerr",
  "version": "0.1.0",
  "checksum": "__CHECKSUM__",
  "silo_api_version": "v1",
  "supported_platforms": [
    {"os": "linux", "arch": "amd64"}, {"os": "linux", "arch": "arm64"}, {"os": "darwin", "arch": "arm64"}
  ],
  "global_config_schema": [],
  "capabilities": [
    {
      "type": "request_router.v1",
      "id": "seerr",
      "display_name": "Seerr",
      "description": "Fulfill content requests through a Seerr (Overseerr/Jellyseerr) instance.",
      "config_schema": [
        {
          "key": "connection",
          "title": "Seerr",
          "json_schema": "{\"type\":\"object\",\"properties\":{\"supports_4k\":{\"type\":\"boolean\"}}}",
          "admin_form": {
            "submit_label": "Save connection",
            "fields": [
              {
                "key": "supports_4k",
                "label": "This Seerr handles 4K requests",
                "control": "ADMIN_FORM_CONTROL_SWITCH",
                "default_value": false,
                "description": "Enable only if the Seerr instance has a 4K Sonarr/Radarr configured. When off, 2160p requests are not sent to this connection."
              }
            ]
          }
        }
      ]
    }
  ]
}
```

This is intentionally the minimal valid rich-form manifest: one config_schema entry, one SWITCH field, no sections, no `dynamic_options`. It exercises the SDK's capability-config-schema validation (added in the schema-form work) and renders through the host's `adminFormToJSON` + `SchemaForm` unchanged.

## Quality-tier mapping

The host passes `qualities` as tier strings (e.g. `["1080p", "2160p"]`). The plugin treats the **4K tier** (the host's 2160p constant) as `is4k:true` and every other tier as `is4k:false`. The implementation plan must pin the exact 4K-tier string by reference to the host's quality constants / how the arr plugin distinguishes its 4K instances (the arr plugin's `routing` already encodes this tier distinction); the Seerr plugin reuses that same notion of "is this the 4K tier." If the host passes a tier the plugin doesn't recognise as 4K, it is treated as HD (`is4k:false`) — the safe default (an HD request never fails for lack of a 4K server).

## Error handling

- **Per-target containment:** every Fulfill/CheckStatus target is independent; one connection or quality failing produces a `failed`/skipped target, never an aborted response. (Same invariant the host already relies on from the arr plugin.)
- **409 duplicate → recover the id, then already-queued:** Seerr dedupes by `(tmdbId, is4k)`. A duplicate is not an error for Silo (the media is already being fulfilled), but the target still needs a trackable Seerr request id, so the plugin recovers it via `FindExistingRequest` and emits a `queued` target with that `external_id`. A `queued` target is never emitted without an `external_id` (which would be un-pollable by CheckStatus and stick forever). If the id genuinely cannot be recovered, the target is `failed` with a clear message rather than a phantom.
- **Missing TMDB id → failed target with a clear message** (Seerr cannot be called without it).
- **4K to a non-4K-capable connection:** prevented by config (`supports_4k` off → skipped). If `supports_4k` is on but Seerr actually lacks a 4K server, the `is4k` request errors upstream → recorded as a `failed` target with Seerr's message (visible to the operator), not a silent drop.
- **Auth/admin requirement:** the connection's API key must be a Seerr admin / auto-approve key so requests auto-fulfill (no second approval gate in Seerr). This is documented in the field description and the repo README; `TestConnection` (`/auth/me`) surfaces an invalid key, and a non-auto-approving key would show requests stuck `queued` (visible via CheckStatus) rather than failing silently.

## Testing

Unit tests with `net/http/httptest` (no live Seerr), mirroring the arr plugin's test style:

- **Fulfill:** HD-only (`qualities:["1080p"]` → one `is4k:false` request); HD+4K with `supports_4k:true` (two requests, `is4k` false then true, two targets); HD+4K with `supports_4k:false` (only the HD target; 4K skipped); `series → "tv"` + `seasons:"all"`; movie body has no `seasons`; missing-TMDB → zero targets + a request-level `Message`; empty-body create → `FindExistingRequest` recovers the id → `queued` target carrying it; `409` → `FindExistingRequest` recovers the id → `queued` target ("already requested"); `409`/empty-body with no recoverable match → `failed` target (no phantom); upstream 500 → `failed` target with message; zero-target plan → `FulfillResponse.Message` set.
- **CheckStatus:** a table test driving each `(request.Status, media.Status)` combination through the mapping table to the expected Silo status; a `404` from `GET /request/{id}` → terminal `failed`; a transient (5xx/network) error → target skipped; missing-connection target skipped.
- **TestConnection:** 200 → `Ok:true`; 401 → `Ok:false` + message.
- **Manifest:** `TestEmbeddedManifestLoads` — `publicmanifest.Load` accepts the manifest and yields one `request_router.v1` capability `seerr` (the same guard the arr plugin has; also confirms the SDK's capability-config-schema validation passes the one-field SWITCH form).

## Open questions / future enhancements (explicitly out of scope)

- **Silo↔Seerr user attribution:** `userId` is left unset (requests attributed to the API-key owner). A future enhancement could maintain a Silo-user → Seerr-user mapping and pass `userId`.
- **Pinning a specific Seerr Sonarr/Radarr server/profile:** deliberately omitted (Seerr default-routes). Could be added later as optional config with `dynamic_options` populated from Seerr's `/service/*` settings endpoints — the "richer" option considered and rejected during brainstorming for YAGNI.
- **4K media status field (`status4k`):** the public Overseerr spec documents a single `media.status`; some builds also expose `status4k`. The CheckStatus mapping reads `media.status`. If, in live testing, a 4K target's status tracks `status4k` separately, the mapping should consult the 4K field for `is4k` targets — flagged for verification during the live e2e (part of the requests-pluginization pre-publish checklist), not a blocker for the plugin's unit-tested logic.
