# Media Request System Spec

## Goal

Build a Silo-native request system that lets authenticated users discover and
search TMDB movies and series, request missing media, and have approved
requests fulfilled through Radarr and Sonarr.

Silo owns request state, request limits, approval policy, visibility, and
catalog availability detection. Radarr and Sonarr are external fulfillment
adapters.

## V1 Scope

- TMDB movie and series search.
- Direct TMDB-backed discovery sections for requestable media.
- One canonical request per media item.
- Radarr fulfillment for movies.
- Sonarr fulfillment for whole series.
- Manual approval and configurable auto approval.
- Global rolling request limits with per-user overrides.
- User-facing request status badges on search and discovery results.
- Admin queue for all requests.
- Background reconciliation with Radarr, Sonarr, and the Silo catalog.

## Non-Goals For V1

- Season-specific or episode-specific requests.
- Multiple Radarr or Sonarr instance routing.
- User-selected quality tiers per request.
- Public/global request lists for non-admin users.
- Request comments or conversation threads.
- Client app updates outside the server web UI.
- Collection-backed request discovery.

## Request Visibility

Requests are not treated as highly private, but Silo should avoid exposing a
global request list to normal users.

- Non-admin users can list only their own submitted requests.
- Admin users can list all requests and see requester, profile, and audit
  details.
- Search and discovery results include lightweight request state for the item.
- Search and discovery results must not expose requester identity, notes, or
  user counts to non-admin users.

If an item has already been requested, other users cannot request it. The item
should show the existing request status instead.

Example search/discovery item state:

```json
{
  "request": {
    "status": "queued",
    "requestable": false
  }
}
```

## Status Model

The primary request status tracks the normal fulfillment path:

- `pending`: submitted and waiting for approval.
- `approved`: approved in Silo, but not yet handed to Radarr or Sonarr.
- `queued`: accepted by Radarr or Sonarr and waiting/searching.
- `downloading`: Radarr or Sonarr reports active download/import activity.
- `completed`: Silo catalog contains the requested media after a scan and
  provider-ID match.

Exceptional outcomes are separate from the main status ladder:

- `declined`
- `cancelled`
- `failed`

V1 assumption: declined, cancelled, and failed requests no longer block a new
request for the same item, but they still count against historical request
limits.

## Request Limits

Admins configure a global rolling quota:

- `enabled`
- `max_requests`
- `window_days`

Admins can define per-user override modes:

- inherit global
- custom `max_requests` and `window_days`
- unlimited
- blocked from requesting

Quota rules:

- A new request counts when a request row is created.
- Every created request counts regardless of final status or outcome.
- Pending, approved, queued, downloading, completed, declined, cancelled, and
  failed requests all count during the rolling window.
- Duplicate attempts for an already-requested item do not count because no new
  request is created.
- Limits are enforced per Silo user account, not per household profile.
- The active profile is still recorded for audit and display.

## Auto Approval

Auto approval supports a global default plus per-user override:

- inherit global
- always manual
- auto approve
- blocked

Auto approval only applies when:

- Requests are enabled.
- The user is within quota.
- The item is not already available in Silo.
- The item does not already have an active request.
- The relevant Radarr or Sonarr integration is configured.

## Discovery

Request discovery is separate from Silo collections. It should call TMDB
directly and enrich the results with Silo availability and request state. It
must not create, sync, or depend on `library_collections`.

V1 discovery sections:

- `trending_movies`: `/trending/movie/week`
- `trending_series`: `/trending/tv/week`
- `popular_movies`: `/movie/popular`
- `popular_series`: `/tv/popular`
- `upcoming_movies`: `/movie/upcoming`
- `on_air_series`: `/tv/on_the_air`

Discovery responses should include:

- media type
- TMDB ID
- title
- year or first air year
- overview
- poster/backdrop URLs or paths
- local availability
- lightweight request state

Example:

```json
{
  "media_type": "movie",
  "tmdb_id": 550,
  "title": "Fight Club",
  "year": 1999,
  "poster_path": "/pB8BM7pdSp6B6Ih7QZ4DrQ3PmJK.jpg",
  "availability": "missing",
  "request": {
    "status": null,
    "requestable": true
  }
}
```

Discovery implementation notes:

- Add a dedicated request discovery service under the request system.
- It may extend or reuse the low-level TMDB HTTP client.
- It must not use collection templates, collection sync, or collection
  persistence.
- Cache raw TMDB section responses briefly, for example 15-60 minutes.
- Enrich request and availability state at request time so status remains
  current.

## Search

Search should call TMDB directly for movie and TV results, then enrich each
result with:

- local Silo availability
- existing request status
- requestability under current settings and quota

Search should not require the item to exist in the Silo catalog.

## Fulfillment

### Movies

1. User selects a TMDB movie search/discovery result.
2. Silo validates quota, availability, duplicate request state, and policy.
3. Silo creates a request.
4. If auto approved, or after manual admin approval, Silo looks up/adds the
   movie in Radarr.
5. Silo stores the Radarr movie ID and moves the request to `queued`.
6. Background reconciliation updates the request to `downloading` from Radarr
   activity/history.
7. Silo marks the request `completed` only after the Silo catalog sees the
   matching TMDB ID.

### Series

1. User selects a TMDB TV search/discovery result.
2. Silo resolves the TVDB ID needed by Sonarr.
3. Silo validates quota, availability, duplicate request state, and policy.
4. Silo creates a request.
5. If auto approved, or after manual admin approval, Silo adds the whole series
   in Sonarr using the default add behavior.
6. Silo stores the Sonarr series ID and moves the request to `queued`.
7. Background reconciliation updates the request to `downloading` from Sonarr
   activity/history.
8. Silo marks the request `completed` only after the Silo catalog sees the
   matching TMDB or TVDB ID.

## Radarr Integration

The Radarr adapter should use Radarr v3 API endpoints:

- `GET /api/v3/system/status` for connection checks.
- `GET /api/v3/rootfolder` for root folder choices.
- `GET /api/v3/qualityprofile` for quality profiles.
- `GET /api/v3/tag` for tag choices.
- `GET /api/v3/movie?tmdbId={id}` to detect existing Radarr items.
- `GET /api/v3/movie/lookup/tmdb?tmdbId={id}` to hydrate add payloads.
- `POST /api/v3/movie` to add approved movies.

Configured movie options:

- root folder
- quality profile
- tags
- minimum availability
- search on add

## Sonarr Integration

The Sonarr adapter should use Sonarr v3 API endpoints:

- `GET /api/v3/system/status` for connection checks.
- `GET /api/v3/rootfolder` for root folder choices.
- `GET /api/v3/qualityprofile` for quality profiles.
- `GET /api/v3/tag` for tag choices.
- `GET /api/v3/series?tvdbId={id}` to detect existing Sonarr items.
- `GET /api/v3/series/lookup?term={term}` to hydrate add payloads.
- `POST /api/v3/series` to add approved series.

Configured series options:

- root folder
- quality profile
- tags
- series type
- season folder
- search for missing episodes on add

## API Surface

Profile-scoped routes:

- `GET /api/v1/requests/search?q=&media_type=movie|series`
- `GET /api/v1/requests/discover`
- `GET /api/v1/requests/discover/{section}?page=1`
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
- `GET /api/v1/admin/request-users/{user_id}/limit`
- `PUT /api/v1/admin/request-users/{user_id}/limit`
- `GET /api/v1/admin/request-integrations`
- `PUT /api/v1/admin/request-integrations`

## Data Model

### `media_requests`

Canonical media request records.

Suggested fields:

- `id`
- `media_type`
- `provider`
- `tmdb_id`
- `tvdb_id`
- `imdb_id`
- `title`
- `year`
- `poster_path`
- `overview`
- `status`
- `outcome`
- `requested_by_user_id`
- `requested_by_profile_id`
- `integration_kind`
- `external_id`
- `external_status`
- `last_error`
- `created_at`
- `updated_at`
- `approved_at`
- `completed_at`

Add a uniqueness constraint for active requests by `(media_type, provider,
tmdb_id)`. Declined, cancelled, failed, and completed requests should not block
a future request.

### `media_request_events`

Audit trail for request lifecycle changes.

Suggested fields:

- `id`
- `request_id`
- `event_type`
- `actor_user_id`
- `actor_profile_id`
- `message`
- `metadata`
- `created_at`

### `request_user_limits`

Per-user quota and approval overrides.

Suggested fields:

- `user_id`
- `limit_mode`
- `max_requests`
- `window_days`
- `approval_mode`
- `updated_at`

### `request_settings`

Global request settings.

Suggested fields:

- `requests_enabled`
- `global_max_requests`
- `global_window_days`
- `global_auto_approval_enabled`
- `updated_at`

### `request_integrations`

Radarr and Sonarr configuration. API keys should use the existing sensitive
settings pattern rather than being returned from API responses. The expected
sensitive setting references are `requests.radarr.api_key` and
`requests.sonarr.api_key`.

Suggested fields:

- `kind`
- `enabled`
- `base_url`
- `api_key_ref`
- `root_folder`
- `quality_profile_id`
- `tags`
- `options`
- `last_check_at`
- `last_check_status`
- `last_check_error`
- `updated_at`

## UI Requirements

User UI:

- Request discovery page with TMDB sections.
- Search page or search mode that includes TMDB results.
- Clear badges for available, requested, and requestable states.
- Own requests page.
- Request button disabled when unavailable due to existing request, local
  availability, quota, or disabled system settings.

Admin UI:

- Request queue with filters by status and outcome.
- Approve, decline, and retry actions.
- Request settings page.
- Radarr and Sonarr configuration with connection checks.
- Per-user limit and approval override controls.

## Acceptance Criteria

- A user can discover trending/popular/upcoming TMDB movies and series without
  touching Silo collections.
- A user can search TMDB and request a missing movie or whole series.
- Search and discovery items show whether they are available, already
  requested, or requestable.
- A user cannot request an item that already has an active request.
- Request limits count every created request within the rolling window,
  regardless of final status.
- Admins can configure global limits and per-user overrides.
- Admins can configure Radarr and Sonarr, approve/decline/retry requests, and
  see request audit history.
- Auto-approved requests are submitted to Radarr/Sonarr without admin action.
- Requests progress through `pending`, `approved`, `queued`, `downloading`, and
  `completed`.
- `completed` means Silo has scanned and matched the media in its catalog, not
  merely that Radarr or Sonarr reports completion.
