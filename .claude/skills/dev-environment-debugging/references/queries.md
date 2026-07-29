# Diagnostic queries

Run with `scripts/silo-dev psql "<sql>"`.

## Schema traps

- `activity_log` and `operational_logs` order by `"timestamp"` — quoted, not `created_at`.
- `admin_jobs` uses `job_type`, `requested_at`, and `completed_at` (not `type`,
  `created_at`, `finished_at`).
- Login accounts (`users`) are separate from household profiles (`user_profiles`); a
  playback session carries both a `user_id` and a `profile_id`.

## Errors and requests

```sql
SELECT "timestamp", level, component, message, request_id, playback_session_id, attrs
FROM operational_logs
WHERE level IN ('error', 'warn')
ORDER BY "timestamp" DESC
LIMIT 50;
```

```sql
SELECT "timestamp", client_ip, method, path, status_code, duration_ms, request_id
FROM activity_log
WHERE status_code >= 500
ORDER BY "timestamp" DESC
LIMIT 20;
```

Drop the `WHERE` clause on `activity_log` for a plain recent-traffic view — useful for
confirming a client is reaching the server at all, and with which paths.

## Playback

```sql
SELECT s.session_id, u.username, COALESCE(s.profile_id, '') AS profile_id,
       s.play_method, s.reporting_node, s.transcode_node_url,
       s.position_seconds, s.is_paused, s.target_resolution,
       s.started_at, s.updated_at
FROM playback_sessions_sync s
LEFT JOIN users u ON u.id = s.user_id
ORDER BY s.updated_at DESC NULLS LAST;
```

Stale rows only:

```sql
SELECT session_id, user_id, play_method, updated_at
FROM playback_sessions_sync
WHERE updated_at < now() - interval '5 minutes'
ORDER BY updated_at;
```

## Nodes

```sql
SELECT id, name, type, url, enabled, healthy, active_jobs, last_health_check
FROM stream_nodes
ORDER BY type, name;
```

Read-only node state is better taken from here than from the admin endpoints, which need
a bearer token. To force a re-check, `POST /api/v1/admin/nodes/{id}/check` — reachable as
`scripts/silo-dev api /api/v1/admin/nodes/{id}/check -X POST`.

## Jobs

```sql
SELECT id, job_type, status, message, error_message,
       progress_current, progress_total, requested_at, started_at,
       completed_at, heartbeat_at
FROM admin_jobs
ORDER BY requested_at DESC
LIMIT 20;
```

Filter by `job_type = 'library_refresh'` (and select `request_payload`, `result_payload`)
when chasing a scan or refresh that did not finish.

## Users and settings

```sql
SELECT id, username, email, role, enabled, permissions, max_streams, max_transcodes, created_at
FROM users
ORDER BY id;
```

```sql
SELECT key, value
FROM server_settings
WHERE key NOT ILIKE '%secret%'
  AND key NOT ILIKE '%password%'
  AND key NOT ILIKE '%api_key%'
  AND key NOT ILIKE '%token%'
ORDER BY key;
```

Keep that filter. Encrypted `server_settings` values are GCM-bound to their key name, so
they are unreadable here anyway — and dumping them into a transcript is a leak.

## Raising the log level

```sql
INSERT INTO server_settings (key, value) VALUES ('server.log_level', 'debug')
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
```

Then `scripts/silo-dev compose "up -d --force-recreate silo"`, and set it back to `info`
when done.
