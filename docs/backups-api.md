# Backups API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. Backups have no `/api/v1` routes. See
> [the native API contract](architecture/api-contract.md).

The Backups page's operations under `/api/v2/admin/backups`. Each requires an
acting administrator: an account with the server-wide `admin` role. A
household's primary profile on a member account is not enough. The admin
[wire conventions](admin-api.md#wire-conventions) apply.
[architecture/backups.md](architecture/backups.md) describes what a backup
holds and how it is taken, rotated, and restored. Restore is the offline
`silo backup restore` command, not an API operation.

Settings are written through the [admin settings API](admin-settings-api.md):
`backup.enabled`, `backup.time`, `backup.keep` (1–60),
`backup.include_intro_fingerprints`, and `backup.include_subtitles`.

## Operations

| Operation | Purpose |
|---|---|
| `GET /admin/backups/capabilities` | What backups on this server support |
| `GET /admin/backups` | This server's backups, other server ids at the destination, and warnings |
| `POST /admin/backups` | Back up now |
| `DELETE /admin/backups/{id}` | Delete one backup |
| `POST /admin/backups/{id}/download-links` | Short-lived download URLs for every object of a backup |
| `GET /admin/backups/{id}/objects/{name}` | Stream one object; authorized by the signed link, not a session |

A backup `id` is its folder name: the UTC start time, to the millisecond, and
the kind, for example `20261031T040000.123Z-nightly`. Ids written to the
second (`20261031T040000Z-nightly`) are still accepted. No two backups of a
server share an id or a time.

### Capabilities

`GET /admin/backups/capabilities` is a capability document: `state` is
`available` when the server has somewhere to write backups and
`not_configured` when it does not: backups are written to the operational
store on local storage, and an S3 artwork backend has none.
`destinations` lists the destinations this server can write to: `default`
when it has a local operational store. `download` is `true` when available.

### List

`GET /admin/backups` answers:

- `server_id`: this server's identity; its backups live under
  `backups/<server_id>/`.
- `destination` (`default`) and `destination_available`, false when the
  destination is missing or cannot be reached.
- `key_check`: this server's `SECRET_KEY` check value, 16 hex digits in
  groups of 4 (`1a2b-3c4d-5e6f-7a8b`). It names the key without revealing it;
  `silo secret check` prints the same value for a key.
- `enabled`: whether nightly backups run.
- `busy`: whether a backup or delete holds the cluster-wide backup lock now,
  on any server.
- `active`: the operation holding the lock, on whichever server runs it:
  `operation` (`backup` or `delete`), `backup_id`, `started_at`, `progress`
  (0–100), and `message`. Absent when nothing runs.
- `last_run`: the latest backup run: `id`, `kind`, `status` (`complete`,
  `complete_with_warnings`, `failed`, or `interrupted` when the server running
  it stopped), `started_at`, `finished_at`, and `error` for a failed or
  interrupted run.
- `keep`: `backup.keep`, how many nightly and manual backups rotation keeps.
- `last_success_at`: when the latest successful backup finished (the newest
  manifest when that is later).
- `items`: this server's backups, newest first. Each has `id`, `kind`
  (`nightly`, `manual`), `status`,
  `created_at`, `completed_at`, `size_bytes`, `silo_version`, `location`
  (a filesystem path on this server's storage),
  `blob_count`,
  `missing_blobs` (`store`, `key`, `source`),
  `objects` (`name`, `size_bytes`, and `sha256` of the stored bytes),
  `key_check` (the check value of the key the backup was taken with), and
  `key_matches` (whether that is this server's; settings and credentials the
  backup holds encrypted cannot be read with another key).
- `other_server_ids`: other server identities with backups at the destination.
- `warnings`: `{code, message}` for the dashboard. Codes are `disabled`,
  `no_recent_backup` (none completed in 48 hours), `destination_unavailable`,
  `sqlite_user_store`, `pg_dump_missing`, and `pg_dump_too_old`.

`status` is one of:

- `complete`: manifest written, checksums recorded, and the dump's table of
  contents readable. It does not claim the backup was restored.
- `complete_with_warnings`: as complete, but some referenced files were
  missing when copied (`missing_blobs` lists them).
- `in_progress`: no manifest yet, and `active` is this backup.
- `incomplete`: no manifest and nothing running. Deleted automatically after
  24 hours.

### Back up now

`POST /admin/backups` takes the cluster-wide lock and starts the backup task on
the responding server, answering `202` with `{id, kind: "manual", status:
"in_progress"}`. The id is the folder the backup writes. Poll the list, or the
`backup` task through the [admin tasks API](admin-tasks-api.md), for the
result. It answers `409 conflict` when another backup is running on any
server, `409` with the SQLite user store message on that backend, and
`409 capability_not_configured` with no destination. It is not retried
automatically.

### Delete

`DELETE /admin/backups/{id}` deletes one backup of this server, complete or
incomplete, and answers `204`. `404` when the id names no backup, `409
conflict` while another backup is running.

### Downloads

`POST /admin/backups/{id}/download-links` with `{"scope": "full"}` answers
`{items: [{name, size_bytes, url, expires_at}]}` for every stored object
(`db.dump`, `blobs.tar`) and `manifest.json` of a backup with a manifest
(`404` otherwise). Every object downloads under its stored name (`db.dump`,
`blobs.tar`, `manifest.json`), so a folder of
downloads is one `silo backup restore --from` reads. Each `url`
is a presigned storage URL where the destination can presign, or a link to
`GET /admin/backups/{id}/objects/{name}?exp=…&sig=…`, valid for 15 minutes and
scoped to that one object. A browser opens it without an `Authorization`
header. The streaming route answers `404` for any invalid, expired, or
mismatched signature without revealing whether the backup exists, and `503`
when storage is unavailable.

## The backup task

The `backup` task runs daily at `backup.time` in server local time, skipping
scheduled runs while `backup.enabled` is off. Every server fires the trigger;
the lock lets one back up and the others record a completed run whose result
is `{"skipped": "skipped: another backup or restore is running"}`. A run's
result data carries `id`, `kind`, `status`, `size_bytes`, `duration_ms`,
`missing_blobs`, `rotated`, `swept`, and `warnings`.
