# Scan API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. The frozen alpha `/api/v1` scan routes are summarized in [Bridge note](#bridge-note) and
> are retired after the pre-1.0 bridge window. See
> [the native API contract](architecture/api-contract.md).

Silo's scan API lets external tools trigger media library scans on demand. Use it to integrate
with download managers like Sonarr and Radarr, or with relay tools like Autoscan that notify your
server when new media arrives.

This page is the integration guide. The contract itself — authority, retry safety, and what an
accepted scan does and does not promise — is in
[the scan control API](scan-control-api.md).

## Prerequisites

- Silo must be running in **integrated** or **api** mode. In `proxy` and `transcode` modes the
  scan operations are not registered.
- You need an **admin API key**. Create one in the Silo web UI under **Settings > API Keys**.
  Keys start with the `sa_` prefix.

## Authentication

Every scan operation requires an acting administrator, authenticated with either a **JWT access
token** or an **API key** in the `Authorization` header:

```
Authorization: Bearer sa_your_api_key_here
```

A profile is optional. An API-key caller should omit `X-Profile-Id`. A JWT caller that sends
one must name the account's primary profile, with `X-Profile-Token` when that profile is
PIN-locked; a secondary profile is rejected by the acting-admin gate.

## Finding your library ID

```bash
curl -s http://your-server:8090/api/v2/libraries \
  -H "Authorization: Bearer sa_your_api_key" | jq '.items[] | {id, name}'
```

`listLibraries` returns `{items, page}`. Library IDs are opaque strings; send them back exactly as
received. You can also find them in the Silo web UI under **Settings > Libraries**.

## Operations

| Operation            | Method and path              | Success |
| -------------------- | ---------------------------- | ------- |
| `getScanCapabilities` | `GET /api/v2/scan/capabilities` | `200` capability document |
| `startLibraryScan`   | `POST /api/v2/scan`          | `202 Accepted` |
| `cancelLibraryScans` | `POST /api/v2/scan/cancel`   | `200 OK` |

### Feature detection

```
GET /api/v2/scan/capabilities
```

Returns the common capability document. `state` is `available` when a scan queue or local
ingester exists, and `not_configured` otherwise. Read it before offering scan controls instead of
sniffing the server version.

### Trigger a scan

```
POST /api/v2/scan
```

Accepts a library ID, a filesystem path, or both. The server picks the scan mode and runs the
scan asynchronously.

| Field        | Type   | Required | Description |
| ------------ | ------ | -------- | ----------- |
| `library_id` | string | no\*     | ID of the library to scan. Must be a canonical positive decimal string. |
| `path`       | string | no\*     | Filesystem path to scan. A library root, a subdirectory, or a single file. |

\* At least one of `library_id` or `path` must be provided.

#### Scan mode resolution

| Input | Path target | Mode | Behavior |
| ----- | ----------- | ---- | -------- |
| `library_id` only | — | `library` | Full scan of all paths in the library. |
| `path` equals a library root | directory | `library` | Full scan of that library. |
| `path` is a subdirectory within a library | directory | `subtree` | Scans only that directory and its descendants. |
| `path` is a video file inside a subdirectory | file | `subtree` | Scans the file's directory and its descendants. |
| `path` is a video file whose directory no longer exists | file | `file` | Reconciles only that single file. |
| `path` is a video file directly under a library root | file | `file` | Scans only that single file. |
| `path` is an audiobook, ebook, or manga file | file | `file` | Scans only that single file. |

When only `path` is provided, the server resolves which library owns that path. When both are
provided, the server verifies the path belongs to the named library. A file path is rejected when
the file's extension is not a media extension for the library's kind; matching is
case-insensitive.

A video file path widens to its directory because a release rarely changes one file in isolation:
an upgrade deletes the replaced version and rewrites sidecars (`.nfo`, posters, subtitles), and
change publishers batch per directory, so not every event reaches the caller. Scanning the
directory reconciles all of it. The widening stops at the library root so a flat library never
turns one file event into a full library scan, and it only happens when the directory still exists:
a video reported from a directory that has already been removed keeps the exact-file scan, since
a scan of a missing directory cannot see anything. A directory that exists but cannot be inspected
is rejected with `400`. The response `mode` reports the scan that actually ran.

A request for a scope that is already being scanned is coalesced into that run. If the run had
already started, one follow-up scan of the same scope is queued when it finishes, so a file that
landed after the running scan walked the directory is still picked up.

#### Response

`202 Accepted`:

```json
{
  "status": "accepted",
  "mode": "subtree",
  "library_id": "1"
}
```

`mode` is `library`, `subtree`, or `file`.

`202` means the request was validated and dispatched to the durable queue, or to the
process-local ingester when no queue is configured. It is not a completion, a durable command
identity, or a replay receipt, and it does not promise that process-local execution survives a
restart. A conflicting scan that is already running for the same library is deduplicated (see
[Deduplication](#deduplication)) and still answers `202`.

This operation is **non-retryable**. Do not replay it automatically after an uncertain response;
observe the library's scan state and make a new explicit decision.

#### Errors

Errors are RFC 9457 problem documents. The trailing segment of `type` is the stable problem code.

| Status | Problem code             | Cause |
| ------ | ------------------------ | ----- |
| 400    | `malformed_request`      | Malformed JSON body, or a target the resolver rejects: neither `library_id` nor `path` supplied, a path outside the library's roots, a missing or uninspectable path, a path that is neither file nor directory, a permission-denied path, an ambiguous path matching several libraries, or an unsupported media extension. |
| 401    | `authentication_required` / `invalid_token` / `session_expired` | Missing, unreadable, or expired credential. |
| 403    | `permission_denied`      | The caller is not an acting administrator. The demo-mode guard also answers here; it exempts acting administrators. |
| 403    | `profile_verification_required` | A PIN-protected profile without `X-Profile-Token`. |
| 404    | `not_found`              | The library ID does not exist. |
| 409    | `conflict`               | The target library is disabled, or the path state conflicts with the requested scan. |
| 422    | `validation_failed`      | `library_id` is not a canonical positive decimal string, or the body fails contract validation. `errors[].location` names the member. |
| 500    | `internal_error`         | Unexpected server error. |
| 503    | `dependency_unavailable` | The scanner is not initialized on this server instance. |

### Cancel a scan

```
POST /api/v2/scan/cancel
```

Cancels the library's currently queued and local scans.

| Field        | Type   | Required | Description |
| ------------ | ------ | -------- | ----------- |
| `library_id` | string | yes      | The library whose scans should be cancelled. Must be a canonical positive decimal string. |

`200 OK`:

```json
{
  "cancelled": 2,
  "library_id": "1"
}
```

`cancelled` preserves the queue and local-ingester result. It does not assert that every cluster
worker has stopped or that cleanup has finished. This operation is **non-retryable**: repeating
it may cancel scans created after the original dispatch, so it is not an idempotent cancellation
of a stable scan identity.

## Examples

### Scan an entire library

```bash
curl -X POST http://your-server:8090/api/v2/scan \
  -H "Authorization: Bearer sa_your_api_key" \
  -H "Content-Type: application/json" \
  -d '{"library_id": "1"}'
```

### Scan a specific show folder (subtree)

This is the most common integration pattern. When Sonarr downloads a new episode, scan the show's
folder:

```bash
curl -X POST http://your-server:8090/api/v2/scan \
  -H "Authorization: Bearer sa_your_api_key" \
  -H "Content-Type: application/json" \
  -d '{"path": "/mnt/media/tv/Breaking Bad"}'
```

### Scan a single newly downloaded file

```bash
curl -X POST http://your-server:8090/api/v2/scan \
  -H "Authorization: Bearer sa_your_api_key" \
  -H "Content-Type: application/json" \
  -d '{"path": "/mnt/media/movies/Oppenheimer (2023)/Oppenheimer.2023.2160p.mkv"}'
```

### Scan a path within a specific library

When a path could belong to more than one library, disambiguate by sending both:

```bash
curl -X POST http://your-server:8090/api/v2/scan \
  -H "Authorization: Bearer sa_your_api_key" \
  -H "Content-Type: application/json" \
  -d '{"library_id": "2", "path": "/mnt/media/movies/Oppenheimer (2023)"}'
```

## Integration with Autoscan

[Autoscan](https://github.com/Cloudbox/autoscan) monitors Sonarr, Radarr, and other sources for
new downloads, then relays scan requests to media servers. Silo supports Autoscan's stock
Jellyfin target through the Jellyfin compatibility server.

Use:

- URL: Silo's Jellyfin compatibility URL, usually `http://your-server:8096`
- Token: a Silo admin API key beginning with `sa_`
- Target type: Autoscan `jellyfin`

Autoscan discovers library roots from `GET /Library/VirtualFolders` and sends changed paths to
`POST /Library/Media/Updated`. The paths must be server-side paths as Silo sees them.

### Alternative: Autoscan custom script target

Create a script (for example `silo-scan.sh`) that Autoscan calls with the changed path:

```bash
#!/bin/bash
# silo-scan.sh — called by Autoscan with the path as $1
SILO_URL="http://your-server:8090"
API_KEY="sa_your_api_key"

BODY=$(jq -n --arg path "$1" '{"path": $path}')

curl -s -S --fail -X POST "${SILO_URL}/api/v2/scan" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d "$BODY" || echo "Silo scan request failed for: $1" >&2
```

## Integration with Sonarr / Radarr

Sonarr and Radarr can trigger external scripts or webhooks when a download completes.

### Built-in autoscan receiver

Silo also ships a first-class receiver. An administrator creates a webhook on an autoscan source
with `POST /api/v2/admin/autoscan/sources/{id}/webhook` (`createAdminAutoscanSourceWebhook`) and
configures the resulting token URL in the external service. Events arrive at
`POST /api/v2/autoscan/webhooks/{token}` (`receiveAutoscanWebhook`), which authenticates on the
token alone and answers `202 {"status":"accepted"}`. `GET /api/v2/autoscan/capabilities` reports
whether the ingress is available. Rotate a token with
`POST /api/v2/admin/autoscan/sources/{id}/webhook/rotate` and remove it with
`DELETE /api/v2/admin/autoscan/sources/{id}/webhook`; Silo does not redirect a retired token URL.

### Sonarr custom script

Where you prefer a script, Sonarr sets environment variables on import:

```bash
#!/bin/bash
# silo-sonarr.sh — Sonarr Connect > Custom Script
SILO_URL="http://your-server:8090"
API_KEY="sa_your_api_key"

# Sonarr sets these environment variables on import:
#   sonarr_series_path        — /mnt/media/tv/Show Name
#   sonarr_episodefile_path   — /mnt/media/tv/Show Name/Season 01/episode.mkv
#   sonarr_eventtype          — Download, Rename, Test, etc.
#   sonarr_isupgrade          — True if this is an upgrade of an existing file

case "$sonarr_eventtype" in
  Download)
    # Fires for both new imports and upgrades (check sonarr_isupgrade if needed)
    if [ -n "$sonarr_episodefile_path" ]; then
      SCAN_PATH="$sonarr_episodefile_path"
    else
      SCAN_PATH="$sonarr_series_path"
    fi
    ;;
  Rename)
    # On rename, rescan the entire series folder
    SCAN_PATH="$sonarr_series_path"
    ;;
  SeriesDelete|EpisodeFileDelete)
    # On deletion, rescan to mark files as missing
    SCAN_PATH="$sonarr_series_path"
    ;;
  Test)
    # Sonarr sends this when you test the connection — exit successfully
    exit 0
    ;;
  *)
    exit 0
    ;;
esac

# If Sonarr and Silo see files at different mount points, remap here:
# SCAN_PATH="${SCAN_PATH/#\/tv//mnt/media/tv}"

BODY=$(jq -n --arg path "$SCAN_PATH" '{"path": $path}')

curl -s -S --fail -X POST "${SILO_URL}/api/v2/scan" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d "$BODY" || echo "Silo scan failed for: $SCAN_PATH" >&2
```

Place this script somewhere accessible (for example `/opt/scripts/silo-sonarr.sh`), make it
executable (`chmod +x`), then in Sonarr go to **Settings > Connect > + > Custom Script** and set
the path. Use the **Test** button to verify connectivity.

### Radarr custom script

Radarr works the same way with different environment variables:

```bash
#!/bin/bash
# silo-radarr.sh — Radarr Connect > Custom Script
SILO_URL="http://your-server:8090"
API_KEY="sa_your_api_key"

# Radarr sets these environment variables on import:
#   radarr_movie_path         — /mnt/media/movies/Movie Name (2024)
#   radarr_moviefile_path     — /mnt/media/movies/Movie Name (2024)/movie.mkv
#   radarr_eventtype          — Download, Rename, Test, etc.
#   radarr_isupgrade          — True if this is an upgrade of an existing file

case "$radarr_eventtype" in
  Download)
    # Fires for both new imports and upgrades (check radarr_isupgrade if needed)
    if [ -n "$radarr_moviefile_path" ]; then
      SCAN_PATH="$radarr_moviefile_path"
    else
      SCAN_PATH="$radarr_movie_path"
    fi
    ;;
  Rename)
    SCAN_PATH="$radarr_movie_path"
    ;;
  MovieDelete|MovieFileDelete)
    # On deletion, rescan to mark files as missing
    SCAN_PATH="$radarr_movie_path"
    ;;
  Test)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac

# If Radarr and Silo see files at different mount points, remap here:
# SCAN_PATH="${SCAN_PATH/#\/movies//mnt/media/movies}"

BODY=$(jq -n --arg path "$SCAN_PATH" '{"path": $path}')

curl -s -S --fail -X POST "${SILO_URL}/api/v2/scan" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d "$BODY" || echo "Silo scan failed for: $SCAN_PATH" >&2
```

## How scanning works

Understanding the scan pipeline helps you choose the right scan mode:

1. **File discovery** — the scanner walks the target path and collects files with recognized
   extensions.
2. **Technical probing** — each new or changed file is analyzed with ffprobe to extract codec,
   resolution, duration, HDR status, and track information.
3. **Metadata matching** — newly discovered files are matched to library items (movies, series,
   episodes) using filename parsing and configured metadata providers.
4. **Reconciliation** — files that were previously in the database but no longer exist on disk
   are marked as missing.

Subtree and file scans only reconcile within their scope: they will not mark files outside the
scanned path as missing. That makes them safe and efficient for targeted updates.

### Deduplication

Scans are deduplicated per library. If a conflicting scan is already running, the new request is
dropped rather than queued, and the operation still answers `202`. The conflict rules:

- Two full library scans on the same library conflict with each other.
- Two subtree/file scans conflict only if their paths overlap.
- A subtree or file scan does **not** conflict with a full library scan.

So if a full library scan is running and Sonarr fires a subtree scan, the subtree scan still
runs. If two full library scans are triggered back to back, the second is dropped.

## Tips

- **Prefer subtree scans** for automation. Scanning a show or movie folder is fast and precise —
  it picks up new files and marks removed ones without touching the rest of the library.
- **Sending a file path is fine.** For video libraries the server scans the file's directory
  when that directory exists, which catches the replaced version, renames, and new sidecar files
  alongside the file you named. Sending the season or movie folder directly is equivalent.
- **Full library scans are expensive.** Reserve these for periodic maintenance (Silo runs one
  daily at 02:00 server-local time by default). Do not trigger full scans from download
  automation.
- **Paths must be server-side paths.** The path you send must match the filesystem as the Silo
  server sees it. If Sonarr and Silo see files at different mount points (common with Docker),
  uncomment and adjust the path remapping line in the scripts above.
- **Scripts require `jq`.** The integration scripts use `jq` to build JSON safely, which handles
  paths containing quotes or backslashes. Install it with your package manager (`apt install jq`,
  `brew install jq`).

## Bridge note

The frozen alpha surface serves the same two commands at `POST /api/v1/scan` and
`POST /api/v1/scan/cancel`, with integer `library_id` values, a flat `{error, message}` error
envelope, and no problem-document `type`. Those routes are frozen: no feature work lands on them, and Silo 1.0 answers the whole `/api/v1` namespace with
`410 Gone` and the `client_upgrade_required` problem code. Point new integrations at `/api/v2`.
