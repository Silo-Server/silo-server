# Media Request System Implementation Plan

Spec: [Media Request System Spec](../specs/request-system.md)

## Objective

Implement the request system in small phases so Silo can first persist and
display requests safely, then add external fulfillment and reconciliation.

## Phase 1: Backend Foundation

Goal: persist requests, enforce requestability rules, and expose enough API for
the web UI to search/discover/request without Radarr or Sonarr side effects.

### Database

Add migration `139_media_requests.{up,down}.sql` unless a newer migration number
exists at implementation time.

Tables:

- `media_requests`
- `media_request_events`
- `request_user_limits`
- `request_settings`
- `request_integrations`

Important constraints and indexes:

- Partial unique index for active requests by `(media_type, provider, tmdb_id)`.
- Index request lookups by `requested_by_user_id`, `requested_by_profile_id`,
  `status`, `outcome`, and `created_at`.
- Index quota checks by `(requested_by_user_id, created_at)`.
- Foreign keys to `users` where possible.
- Profile IDs should remain text because user profile storage is per-user.

### Backend Package

Create `internal/requests` with:

- domain types for media type, status, outcome, settings, quota, discovery item
- repository for request CRUD, request events, settings, integrations, and quota
  checks
- service for request creation, requestability, approval, decline, retry, and
  user/admin listing
- TMDB discovery/search client wrapper or extension over the existing TMDB
  client
- catalog presence resolver using existing `catalog.ItemRepository` provider ID
  lookups where possible

Core service methods:

- `Search(ctx, viewer, query, mediaType, page)`
- `Discover(ctx, viewer, section, page)`
- `CreateRequest(ctx, viewer, input)`
- `ListMine(ctx, viewer, filters)`
- `ListAdmin(ctx, filters)`
- `Approve(ctx, admin, requestID)`
- `Decline(ctx, admin, requestID, reason)`
- `Retry(ctx, admin, requestID)`
- `ResolveRequestability(ctx, viewer, mediaType, tmdbID)`

### API Handlers

Add `internal/api/handlers/requests.go`.

Profile routes:

- `GET /api/v1/requests/search`
- `GET /api/v1/requests/discover`
- `GET /api/v1/requests/discover/{section}`
- `POST /api/v1/requests`
- `GET /api/v1/requests/mine`
- `GET /api/v1/requests/{id}`

Admin routes:

- `GET /api/v1/admin/requests`
- `POST /api/v1/admin/requests/{id}/approve`
- `POST /api/v1/admin/requests/{id}/decline`
- `POST /api/v1/admin/requests/{id}/retry`
- `GET /api/v1/admin/request-settings`
- `PUT /api/v1/admin/request-settings`
- `GET /api/v1/admin/request-integrations`
- `PUT /api/v1/admin/request-integrations`

Router wiring should follow existing profile/admin grouping in
`internal/api/router.go`.

### Phase 1 Behavior

- Search and discovery call TMDB directly.
- Results are enriched with local availability, existing request status, and
  requestability.
- Creating a request enforces:
  - requests enabled
  - per-user quota
  - not already locally available
  - no active request for the same `(media_type, tmdb_id)`
  - user not blocked from requesting
- Auto approval may set status to `approved`, but Phase 1 does not submit to
  Radarr or Sonarr yet.
- Admin approval moves `pending` to `approved`.
- Admin decline sets `outcome = declined`.
- Retry can be accepted only for failed requests, but can be a no-op until
  fulfillment exists.

### Phase 1 Verification

Minimal backend tests are warranted because the requestability and quota logic
is critical:

- active duplicate request blocks creation
- declined/cancelled/failed/completed requests do not block a new request
- quota counts every created request in the rolling window
- quota is per user, not per profile
- search/discovery enrichment hides requester identity

No frontend tests unless specifically requested.

## Phase 2: Radarr And Sonarr Fulfillment

Goal: approved requests are submitted to Radarr/Sonarr and move to `queued`.

Add adapter interfaces in `internal/requests`:

- `MovieFulfillmentAdapter`
- `SeriesFulfillmentAdapter`

Built-in implementations:

- `internal/requests/radarr`
- `internal/requests/sonarr`

Radarr support:

- connection check
- root folders
- quality profiles
- tags
- existing movie lookup by TMDB ID
- movie lookup/hydration by TMDB ID
- add movie

Sonarr support:

- connection check
- root folders
- quality profiles
- tags
- existing series lookup by TVDB ID
- series lookup/hydration by search term or TVDB-bearing result
- add series

Submission should be idempotent:

- If the item already exists in Radarr/Sonarr, store the external ID and mark
  `queued` rather than failing.
- If the external API accepts the item, store the external ID and mark `queued`.
- If submission fails, set `outcome = failed` and persist `last_error`.

## Phase 3: Reconciliation Worker

Goal: keep request status current after submission.

Add a periodic worker that:

- polls active `queued` and `downloading` requests
- checks Radarr/Sonarr queue/history/activity to detect active downloads
- checks Silo catalog provider IDs to mark completed
- records status transitions in `media_request_events`

Completion must be based on Silo catalog presence, not Radarr/Sonarr status
alone.

## Phase 4: Web UI

Goal: expose the feature in the admin web UI and user web UI.

User pages:

- request discovery page with direct TMDB sections
- TMDB search mode
- own requests page
- request status badges and disabled-state reasons

Admin pages:

- request queue with status/outcome filters
- approve/decline/retry actions
- request settings
- Radarr/Sonarr connection settings and connection checks
- per-user overrides

Frontend API additions should live in `web/src/api/types.ts` and the existing
query hook structure.

## Phase 5: Polish And Follow-Up

Potential follow-ups after V1:

- notifications for approval/completion/failure
- season-specific requests
- multiple Radarr/Sonarr routing profiles
- quality-tier request choices
- request notes visible to admins
- plugin SDK fulfillment adapter boundary
- client app surfaces for Android and Apple

## Open Implementation Notes

- TMDB image URLs should use the same image handling convention as existing
  metadata/search UI where possible.
- API keys for Radarr/Sonarr must not be returned by API responses.
- Request status should be evented later if the UI needs realtime updates, but
  polling is enough for V1.
- The request discovery service may cache raw TMDB responses, but request and
  availability enrichment must happen per API request.
