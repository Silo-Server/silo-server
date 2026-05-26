# Jellyfin Autoscan Scan Compatibility Design

## Goal

Make Silo work with Autoscan's stock Jellyfin target. Autoscan should be able to
point at Silo's Jellyfin compatibility URL, use a Silo admin API key as the
Jellyfin token, discover Silo library roots, and notify Silo about changed media
paths without a custom script.

This design is intentionally scoped to Jellyfin compatibility only. Emby routes
and aliases are out of scope.

## Current State

Silo already has a native admin scan API at `POST /api/v1/scan`. It accepts
either `library_id`, `path`, or both, resolves the target to a full-library,
subtree, or single-file scan, and dispatches through the existing scan queue or
scanner path.

Silo's Jellyfin compatibility server currently supports enough read/playback
routes for Jellyfin clients, including `GET /System/Info` and
`GET /Library/VirtualFolders`, but it does not expose Jellyfin's scan notification
endpoint. `GET /Library/VirtualFolders` also returns empty `Locations`, which
prevents Autoscan from matching incoming paths to Jellyfin libraries.

Autoscan's Jellyfin target uses this flow:

1. `GET /System/Info` with `X-Emby-Token`.
2. `GET /Library/VirtualFolders` with `X-Emby-Token`.
3. `POST /Library/Media/Updated` with `X-Emby-Token` and a body shaped like:

   ```json
   {
     "Updates": [
       {
         "path": "/media/tv/Show/Season 01/Episode.mkv",
         "updateType": "Modified"
       }
     ]
   }
   ```

## Compatibility Surface

Add a small Jellyfin scan compatibility adapter under `internal/jellycompat`.
The adapter should own Jellyfin scan/discovery semantics and translate them into
Silo's existing scan behavior.

The first supported route set is:

- `GET /System/Info`: already present, but should accept Silo admin API keys on
  the Autoscan path.
- `GET /Library/VirtualFolders`: return enabled Silo libraries with real
  configured root paths in `Locations`.
- `POST /Library/Media/Updated`: accept Autoscan update payloads and enqueue
  equivalent Silo scans.

Do not add Emby-specific routes such as `/emby/Library/SelectableMediaFolders`
or `/emby/Library/Media/Updated` in this pass.

## Authentication

For the Jellyfin scan/discovery routes needed by Autoscan, allow a Silo admin API
key (`sa_...`) in the token locations Autoscan uses:

- `X-Emby-Token`
- `X-Mediabrowser-Token`
- `Authorization: Bearer`
- `api_key` query parameter

The API key must resolve to an enabled Silo admin user. Non-admin API keys must
receive a non-2xx authorization error. Existing Jellyfin compatibility session
tokens should continue to work for normal Jellyfin client routes; this change
should not broadly weaken playback or browse authorization.

## Library Discovery

`GET /Library/VirtualFolders` should include `Locations` using the exact
server-side paths configured on each enabled Silo library. Autoscan appends a
trailing slash internally and compares incoming paths against these roots, so the
paths must be real filesystem paths as Silo sees them.

Disabled libraries should be omitted from the Autoscan discovery response because
they are not valid scan targets.

## Scan Notification Behavior

`POST /Library/Media/Updated` should parse every `Updates[]` entry with a
non-empty `path`. The first pass ignores `updateType`; Autoscan sends
`Modified`, and Silo's existing path resolver determines the correct scan mode.

Each update path should use the same effective target resolution as
`POST /api/v1/scan`:

- A path equal to a configured library root becomes a full-library scan.
- A directory under a configured root becomes a subtree scan.
- A supported media file under a configured root becomes a file scan.
- Paths outside all libraries, missing paths, permission failures, special files,
  disabled libraries, and unsupported file extensions are rejected.

For requests containing multiple updates, resolution should be all-or-fail:
validate every update first, enqueue nothing if any update is invalid, and return
a non-2xx error. This avoids Autoscan seeing success while Silo silently drops
part of the request.

When all updates are valid, enqueue each resolved scan independently and let the
existing scan queue deduplicate or serialize overlapping work. The compatibility
adapter should not implement a separate deduplication policy.

The successful response can be `204 No Content`; Autoscan only requires a 2xx.

## Component Boundaries

Keep the compatibility layer small and explicit:

- Add a Jellyfin scan handler in `internal/jellycompat` for
  `Library/Media/Updated` and Autoscan-facing `VirtualFolders`.
- Share scan target resolution with the native scan API by extracting the
  resolver/enqueue logic behind a small interface or helper. Avoid duplicating
  path classification rules in two packages.
- Reuse the existing API key repository and user lookup logic for admin API key
  validation rather than creating a Jellyfin-specific API key store.
- Continue routing normal playback, browse, and user-data Jellyfin endpoints
  through the existing compat session authenticator.

## Error Handling

Return non-2xx responses for invalid scan notifications so Autoscan can treat the
target as failed:

- `401 Unauthorized` for missing or invalid tokens.
- `403 Forbidden` for valid non-admin keys.
- `400 Bad Request` for malformed JSON, empty update lists, empty paths, paths
  outside libraries, missing paths, unsupported files, and other validation
  failures.
- `409 Conflict` for paths that map only to a disabled library.
- `503 Service Unavailable` if the scanner or scan queue is unavailable.
- `500 Internal Server Error` for unexpected repository or enqueue failures.

The response body may use Silo's existing JSON error shape where practical.

## Testing

Add focused backend tests for this compatibility surface:

- Admin API key auth is accepted by Autoscan routes.
- Non-admin or invalid keys are rejected.
- `GET /Library/VirtualFolders` includes enabled library `Locations`.
- `POST /Library/Media/Updated` maps a valid file or directory path into an
  enqueued Silo scan.
- Multi-update requests are all-or-fail and do not enqueue partial scans when
  one path is invalid.

No frontend tests are needed.

## Documentation

Update `docs/scan-api.md` to explain that Autoscan can use its stock Jellyfin
target:

- URL: Silo's Jellyfin compatibility URL, usually `http://host:8096`.
- Token: a Silo admin API key beginning with `sa_`.
- Paths: server-side paths as seen by Silo.

Keep the custom script/webhook example as an alternative for users who do not
want to expose the Jellyfin compatibility endpoint.
