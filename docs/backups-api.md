# Backups API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. Backups have no `/api/v1` routes. See
> [the native API contract](architecture/api-contract.md).

The Backups page's operations under `/api/v2/admin/backups`. Each requires an
acting administrator: an account with the server-wide `admin` role. A
household's primary profile on a member account is not enough. The admin
[wire conventions](admin-api.md#wire-conventions) apply.
[architecture/backups.md](architecture/backups.md) describes what a backup
holds and how it is taken, rotated, and restored. Full restore is the offline
`silo backup restore` command, not an API operation; selective restore of
settings, plugins, libraries, collections and uploaded artwork, and home and
library page sections runs as an admin job, from a backup of this server.

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
| `POST /admin/backups/{id}/download-links` | Short-lived download URLs for every object of a backup, or for its restore set |
| `GET /admin/backups/{id}/objects/{name}` | Stream one object; authorized by the signed link, not a session |
| `GET /admin/backups/{id}/restore-set` | Preview a backup's restore set against this server |
| `POST /admin/backups/{id}/restore` | Restore chosen items as a `backup_restore` admin job |

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
when it has a local operational store. `selective_restore` and `download`
are `true` when available.

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
- `busy`: whether a backup, delete, or selective restore holds the
  cluster-wide backup lock now, on any server.
- `active`: the operation holding the lock, on whichever server runs it:
  `operation` (`backup`, `delete`, or `restore`), `backup_id` (for a restore,
  its source backup), `started_at`, `progress` (0–100),
  and `message`. Absent when nothing runs. A restore first shows as the
  `backup` of its before-restore copy.
- `last_run`: the latest backup run: `id`, `kind`, `status` (`complete`,
  `complete_with_warnings`, `failed`, or `interrupted` when the server running
  it stopped), `started_at`, `finished_at`, and `error` for a failed or
  interrupted run.
- `keep`: `backup.keep`, how many nightly and manual backups rotation keeps.
- `last_success_at`: when the latest successful backup finished (the newest
  manifest when that is later).
- `items`: this server's backups, newest first. Each has `id`, `kind`
  (`nightly`, `manual`, `before-restore`), `status`,
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
result. It answers `409 conflict` when another backup or restore is running
on any server, `409` with the SQLite user store message on that backend, and
`409 capability_not_configured` with no destination. It is not retried
automatically.

### Delete

`DELETE /admin/backups/{id}` deletes one backup of this server, complete or
incomplete, and answers `204`. `404` when the id names no backup, `409
conflict` while another backup or restore is running, or while a queued or
running restore reads the backup.

### Downloads

`POST /admin/backups/{id}/download-links` with `{"scope": "full"}` answers
`{items: [{name, size_bytes, url, expires_at}]}` for every stored object
(`db.dump`, `blobs.tar`, `restore-set.tar` when the backup has one) and
`manifest.json` of a backup with a manifest (`404` otherwise). With
`{"scope": "restore-set"}` it answers `restore-set.tar` alone, and `409` for
a backup without one. Every object downloads under its stored name
(`db.dump`, `blobs.tar`, `restore-set.tar`, `manifest.json`), so a folder of
downloads is one `silo backup restore --from` reads. Each `url`
is a presigned storage URL where the destination can presign, or a link to
`GET /admin/backups/{id}/objects/{name}?exp=…&sig=…`, valid for 15 minutes and
scoped to that one object. A browser opens it without an `Authorization`
header. The streaming route answers `404` for any invalid, expired, or
mismatched signature without revealing whether the backup exists, and `503`
when storage is unavailable.

### Preview a restore set

`GET /admin/backups/{id}/restore-set` reads the backup's `restore-set.tar`
and compares each item with this server. It writes nothing and takes no
lock. `409` when the backup has no restore set, when it is from a newer Silo ("This backup is from a newer Silo. Upgrade this
server first."), or when the restore set cannot be read.

The answer carries `backup_id`, `digest` (the SHA-256 of `restore-set.tar`; a restore
request must send it back), the source (`server_id`, `server_name`,
`silo_version`, `created_at`, `same_server`), `key_matches` (whether the
backup was taken with this server's `SECRET_KEY`), `missing_blobs`,
`never_restored_settings` (the setting keys a restore never writes because
they describe this server; an entry ending in `.` or `_` is a prefix),
`library_map` (each backup library with `local_id` when a library here has
the identical folder set), and `sections` in restore order: `libraries`,
`plugins`, `settings`, `collections`, and `home_sections` (format 3 and
later). Each section has `count` and `items`.

Every item has `id` (what a restore request names), `kind`
(`library`, `plugin`, `settings_page`, `collection`,
`template_collection`, `item_artwork`, `branding`, `home_section`), `label`, `status`, `selectable`, and, when it applies,
`reason`, `message`, `requires`, `restart_required`, `external_service`,
and `missing_blobs`. A selectable item that writes files is
restored only on storage that can create a file without replacing one: an
S3-compatible bucket that rejects conditional writes fails it at restore
time (see the report's `failed` outcome), which a preview cannot detect.

| `status` | Meaning |
|---|---|
| `on_this_server` | Already the same here; nothing to restore, never selectable |
| `not_on_this_server` | Restoring creates it |
| `differs` | Restoring changes or adds to what is here |
| `folder_missing` | A library folder this server cannot open; not selectable |
| `ambiguous` | More than one collection or section here matches; not selectable |
| `needs_another_key` | Encrypted with another `SECRET_KEY`; not selectable |

`reason` codes: `same`, `path_in_use` (a folder belongs to another library
here), `folder_missing`, `library_not_restorable`, `library_not_mapped`,
`ambiguous_name`, `needs_another_key`, `config_needs_another_key` (a plugin
that installs without its configuration and stays disabled),
`other_version` (another version is installed: settings only),
`item_not_on_this_server`, `file_missing` (its image was missing when the
backup was taken), `not_in_backup`, `changed_since_preview`,
`template_not_available` (a template collection whose template this
server's catalog does not have, or that does not suit the library), and
`collection_not_mapped` (a page section showing a collection that is
neither here nor in the backup).

`requires` lists items (`libraries:<id>`, `collections:<item id>`) that must
be restored with this one: a collection or template collection whose library
this server does not have yet is selectable only together with its library,
and a page section only together with the libraries and the collection it
shows.

Kind-specific details:

- `settings_page`: `settings`, each `{key, class, status, masked,
  backup_value, current_value, restart_required}`. `class` is `portable` or
  `external_service` (connects to an outside service). Encrypted and
  sensitive settings are `masked` with both values empty.
- `plugin`: `plugin` with `version`, `installed_version`, `enabled`,
  `config_keys`, and `config_restorable`.
- `library`: `library` with `type`, `paths`, `local_id`, and
  `missing_paths`.
- `collection`: `collection` with `library_ids` (mapped here), `items`,
  `missing_items`, `unknown_items` (not in this catalog, left out), and
  `local_id` (the collection here with the backup's id, else the one in its
  library with its name; `ambiguous` when two of the backup's collections
  share that name there). A created collection keeps the backup's id when
  it is free.
- `template_collection` (id `template:<backup collection id>`, labeled with
  the template's title in this server's catalog and the backup library's
  name): a collection a template bundle created. Its items are not carried:
  a restore creates a missing one from this server's template the way a
  bundle apply does, applies the admin's changes, and syncs it after the
  restore; an existing one (matched by the template, bundle, and mapped
  library) takes only the changes that differ. `collection` with
  `library_ids`, `local_id`, `template_id`, `bundle_id`, and `changes`, the
  fields the admin changed from the template's defaults that a restore
  writes here (`title`, `description`, `visibility`, `featured`,
  `sort order`, `item sort`, `source`, `sync schedule`, `poster`,
  `backdrop`).
- `home_section` (id `section:<backup section id>`, labeled with its title
  and its page): an admin section of the home page or a library page.
  `home_section` with `scope` (`home` or `library`), `library_id` (the
  library here), `section_type`, `local_id` (the matching section here: the
  one with the backup's id, else the one section of the page with the same
  type and title, which is `ambiguous` when two of the backup's sections
  share that page, type, and title), and `changes`, what a restore replaces
  in it (`title`, `position`, `featured`, `item limit`, `enabled`,
  `configuration`, and `N collection items` of the collection it manages). A
  chosen section replaces the configuration of the one here or is created; a
  section that manages a collection brings it.

### Restore

`POST /admin/backups/{id}/restore` with `{digest, selection}` queues a
`backup_restore` admin job and answers `202` with the job (`AdminTaskJob`),
a `Location` of `/api/v2/admin/jobs/{job_id}`, and `Retry-After: 5`.
`selection` has `libraries` (backup library ids), `plugins` (plugin ids),
`settings` (settings page ids), `collections` (`collection:<id>`,
`template:<id>`, `artwork:<content_id>:<image type>`, `branding:<setting
key>`), and `home_sections` (`section:<id>`), each the `id`s the preview
returned. The restore matches each item again and leaves out one whose
mapping changed since the preview (`changed_since_preview`).
`409 restore_set_changed` when the backup changed
since the preview (the digest differs): preview it again. `409 conflict` when
another backup or restore is running (a running restore's job is in
`Location`) or the backup has no restore set. `422` for an empty or malformed
selection. The request holds the cluster-wide lock while it checks the source
again and queues the job, so a backup deleted or rotated away
meanwhile answers `404`, never a job without its source; once queued, the
source is kept until the restore ends.

Follow the job through the [admin jobs endpoints](admin-tasks-api.md):
`GET /admin/jobs/{id}` (progress counts items; the before-restore backup is
the first), and `POST /admin/jobs/{id}/cancel`, which stops the restore
between items. A restore job carries `backup_restore_source` (`backup_id`)
from the moment it is queued. Its `backup_restore_result` is the report,
kept with the job when it completes, fails, is canceled, or is interrupted:

- `backup_id`: the source.
- `before_restore_backup_id`: the recovery point taken before anything was
  restored. Restoring it in full with `silo backup restore` goes back to how
  the server was; a selective restore of it cannot remove what a restore
  added.
- `library_map`: each backup library and the library it is on this server.
- `items`: `{section, id, kind, label, outcome, reason, message, error,
  restart_required, restart_keys, notes, missing_blobs}` for each item that
  ran. `outcome` is `restored`, `unchanged` (already as in the backup),
  `left_out` (with a `reason`), or `failed` (with an `error`).
  A restore writes files (artwork, branding, posters) only where
  none is, with a create the storage makes exclusive. Storage that cannot
  (an S3-compatible bucket that rejects conditional writes) fails each item
  that writes a file, with the error "this storage can't guarantee existing
  files are kept; restore this item manually", and writes nothing for it;
  the preview cannot tell in advance.
- `counts`: `restored`, `unchanged`, `left_out`, `failed`.
- `interrupted`: the server running the restore stopped. Items listed
  committed; running the same restore again is safe and reports them
  unchanged.
- `error`: why the restore stopped, when it did, and `error_code`:
  `restore_set_changed` (the backup changed since the request; nothing was
  restored), `busy` (another backup or restore held the lock),
  `before_restore_backup_failed` (nothing was restored), `interrupted`, or
  `failed`.

## The backup task

The `backup` task runs daily at `backup.time` in server local time, skipping
scheduled runs while `backup.enabled` is off. Every server fires the trigger;
the lock lets one back up and the others record a completed run whose result
is `{"skipped": "skipped: another backup or restore is running"}`. A run's
result data carries `id`, `kind`, `status`, `size_bytes`, `duration_ms`,
`missing_blobs`, `rotated`, `swept`, and `warnings`.
