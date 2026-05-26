# Scan API

Silo exposes a scan API that lets external tools trigger media library scans on demand. This is useful for integrating with download managers like Sonarr, Radarr, or relay tools like Autoscan that notify your server when new media arrives.

## Prerequisites

- Silo must be running in **integrated** or **api** mode. In `proxy` and `transcode` modes, the scan endpoints are not registered and will return 404.
- You need an **admin API key**. Create one in the Silo web UI under **Settings > API Keys**. Keys start with the `sa_` prefix.

## Authentication

All scan endpoints require admin-level authentication via the `Authorization` header using either a **JWT access token** or an **API key**.

```
Authorization: Bearer sa_your_api_key_here
```

## Finding Your Library ID

To list libraries and their IDs:

```bash
curl -s http://your-server:8090/api/v1/libraries \
  -H "Authorization: Bearer sa_your_api_key" | jq
```

You can also find library IDs in the Silo web UI under **Settings > Libraries**.

## Endpoints

All endpoints are under `/api/v1` and require `Content-Type: application/json`.

---

### Trigger a Scan

```
POST /api/v1/scan
```

Accepts a library ID, a filesystem path, or both. The server determines the appropriate scan mode automatically and runs the scan asynchronously in the background.

#### Request Body

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `library_id` | integer | no* | ID of the library to scan. |
| `path` | string | no* | Filesystem path to scan. Can be a library root, subdirectory, or single file. |

\* At least one of `library_id` or `path` must be provided.

#### Scan Mode Resolution

The server automatically selects the scan mode based on the input:

| Input | Path Target | Mode | Behavior |
|-------|-------------|------|----------|
| `library_id` only | — | `library` | Full scan of all paths in the library. |
| `path` equals a library root | directory | `library` | Full scan of that library. |
| `path` is a subdirectory within a library | directory | `subtree` | Scans only that directory and its descendants. |
| `path` is a media file | file | `file` | Scans only that single file. |

When only `path` is provided, the server automatically resolves which library owns that path. When both are provided, the server verifies the path belongs to the specified library.

#### Supported Media Extensions

`.mkv`, `.mp4`, `.avi`, `.m4v`, `.ts`, `.wmv`

Extension matching is case-insensitive. Single-file scans will be rejected if the file does not have a recognized extension.

#### Response

**HTTP 202 Accepted**

```json
{
  "status": "accepted",
  "mode": "subtree",
  "library_id": 1
}
```

The `mode` field will be one of `library`, `subtree`, or `file`.

> **Note:** A 202 response means the request was validated and accepted, not that a scan goroutine was necessarily started. If a conflicting scan is already running for the same library (see [Deduplication](#deduplication) below), the request is silently deduplicated and no additional scan runs.

#### Error Responses

| Status | Code | Message | Cause |
|--------|------|---------|-------|
| 400 | `bad_request` | Either library_id or path is required | Missing both fields. |
| 400 | `bad_request` | Path does not belong to the specified library | Path is outside the library's configured roots. |
| 400 | `bad_request` | Path does not exist | Filesystem path not found. |
| 400 | `bad_request` | Permission denied for path | Server lacks read permission. |
| 400 | `bad_request` | Path could not be inspected | Stat failed for another reason. |
| 400 | `bad_request` | Path must be a file or directory | Path is a socket, FIFO, or other special file. |
| 400 | `bad_request` | Path matches multiple libraries | Ambiguous path — provide `library_id` to disambiguate. |
| 400 | `bad_request` | No library matches the given path | Path is not within any configured library. |
| 400 | `bad_request` | Unsupported media file extension | File mode only — extension not recognized. |
| 401 | `unauthorized` | (varies) | Missing, invalid, or expired credentials. |
| 403 | `forbidden` | (varies) | Authenticated user is not an admin. |
| 404 | `not_found` | Library not found | Library ID does not exist. |

---

### Cancel a Scan

```
POST /api/v1/scan/cancel
```

Cancels all running scans for a given library.

#### Request Body

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `library_id` | integer | yes | ID of the library whose scans should be cancelled. Must be > 0. |

#### Response

**HTTP 200 OK**

```json
{
  "cancelled": 2,
  "library_id": 1
}
```

The `cancelled` field indicates how many in-progress scans were stopped.

#### Error Responses

| Status | Code | Message | Cause |
|--------|------|---------|-------|
| 400 | `bad_request` | library_id is required | Missing or zero/negative `library_id`. |
| 503 | `unavailable` | Scanner not available | Scanner is not initialized on this server instance. |

---

## Examples

### Scan an entire library

```bash
curl -X POST http://your-server:8090/api/v1/scan \
  -H "Authorization: Bearer sa_your_api_key" \
  -H "Content-Type: application/json" \
  -d '{"library_id": 1}'
```

### Scan a specific show folder (subtree)

This is the most common integration pattern. When Sonarr downloads a new episode, you scan the show's folder:

```bash
curl -X POST http://your-server:8090/api/v1/scan \
  -H "Authorization: Bearer sa_your_api_key" \
  -H "Content-Type: application/json" \
  -d '{"path": "/mnt/media/tv/Breaking Bad"}'
```

### Scan a single newly downloaded file

```bash
curl -X POST http://your-server:8090/api/v1/scan \
  -H "Authorization: Bearer sa_your_api_key" \
  -H "Content-Type: application/json" \
  -d '{"path": "/mnt/media/movies/Oppenheimer (2023)/Oppenheimer.2023.2160p.mkv"}'
```

### Scan a path within a specific library

When a path could theoretically belong to multiple libraries, you can disambiguate by providing both:

```bash
curl -X POST http://your-server:8090/api/v1/scan \
  -H "Authorization: Bearer sa_your_api_key" \
  -H "Content-Type: application/json" \
  -d '{"library_id": 2, "path": "/mnt/media/movies/Oppenheimer (2023)"}'
```

---

## Integration with Autoscan

[Autoscan](https://github.com/Cloudbox/autoscan) monitors Sonarr, Radarr, and
other sources for new downloads, then relays scan requests to media servers.
Silo supports Autoscan's stock Jellyfin target through the Jellyfin compatibility
server.

Use:

- URL: Silo's Jellyfin compatibility URL, usually `http://your-server:8096`
- Token: a Silo admin API key beginning with `sa_`
- Target type: Autoscan `jellyfin`

Autoscan discovers library roots from `GET /Library/VirtualFolders` and sends
changed paths to `POST /Library/Media/Updated`. The paths must be server-side
paths as Silo sees them.

### Alternative: Autoscan Custom Script Target

Create a script (e.g., `silo-scan.sh`) that Autoscan calls with the changed path:

```bash
#!/bin/bash
# silo-scan.sh — called by Autoscan with the path as $1
SILO_URL="http://your-server:8090"
API_KEY="sa_your_api_key"

BODY=$(jq -n --arg path "$1" '{"path": $path}')

curl -s -S --fail -X POST "${SILO_URL}/api/v1/scan" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d "$BODY" || echo "Silo scan request failed for: $1" >&2
```

---

## Integration with Sonarr / Radarr

Sonarr and Radarr can trigger external scripts or webhooks when a download completes. The simplest approach is to use a **Connect > Custom Script** in Sonarr/Radarr that calls the Silo scan API.

### Sonarr Custom Script

Create a script that Sonarr calls on import. Sonarr sets environment variables with episode details:

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

curl -s -S --fail -X POST "${SILO_URL}/api/v1/scan" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d "$BODY" || echo "Silo scan failed for: $SCAN_PATH" >&2
```

Place this script somewhere accessible (e.g., `/opt/scripts/silo-sonarr.sh`), make it executable (`chmod +x`), then in Sonarr go to **Settings > Connect > + > Custom Script** and set the path. Use the **Test** button to verify connectivity.

### Radarr Custom Script

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

curl -s -S --fail -X POST "${SILO_URL}/api/v1/scan" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d "$BODY" || echo "Silo scan failed for: $SCAN_PATH" >&2
```

---

## How Scanning Works

Understanding the scan pipeline helps you choose the right scan mode:

1. **File discovery** — The scanner walks the target path and collects files with recognized extensions.
2. **Technical probing** — Each new or changed file is analyzed with ffprobe to extract codec, resolution, duration, HDR status, and track information.
3. **Metadata matching** — Newly discovered files are matched to library items (movies, series, episodes) using filename parsing and configured metadata providers.
4. **Reconciliation** — Files that were previously in the database but no longer exist on disk are marked as missing.

Subtree and file scans only reconcile within their scope — they will not mark files outside the scanned path as missing. This makes them safe and efficient for targeted updates.

### Deduplication

Scans are deduplicated per library. If a conflicting scan is already running, the new request is silently dropped (not queued). The API still returns 202 in this case. The conflict rules are:

- Two full library scans on the same library conflict with each other.
- Two subtree/file scans conflict only if their paths overlap.
- A subtree or file scan does **not** conflict with a full library scan.

In practice this means: if a full library scan is running and Sonarr fires a subtree scan, the subtree scan will still run. But if two full library scans are triggered back-to-back, the second is dropped.

---

## Tips

- **Prefer subtree scans** for automation. Scanning a show or movie folder is fast and precise — it picks up new files and marks removed ones without touching the rest of the library.
- **Use file scans sparingly.** They're useful when you know the exact file, but a subtree scan of the parent folder is usually just as fast and also catches renames, deletions, and new subtitle files.
- **Full library scans are expensive.** Reserve these for periodic maintenance (Silo runs one daily at 02:00 server-local time by default). Don't trigger full scans from download automation.
- **Paths must be server-side paths.** The path you send must match the filesystem as the Silo server sees it. If Sonarr and Silo see files at different mount points (common with Docker), uncomment and adjust the path remapping line in the scripts above.
- **Scripts require `jq`.** The integration scripts use `jq` to safely construct JSON, which correctly handles paths containing special characters like quotes or backslashes. Install it via your package manager (`apt install jq`, `brew install jq`, etc.).
