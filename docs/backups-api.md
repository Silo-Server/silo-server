# Backups API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. Backups have no `/api/v1` routes. See
> [the native API contract](architecture/api-contract.md).

The Backups page's operations under `/api/v2/admin/backups`. Each requires an
acting administrator: an account with the server-wide `admin` role. A
household's primary profile on a member account is not enough. The one
exception is [profile export and import](#profile-export-and-import), which a
signed-in profile uses for its own data. The admin
[wire conventions](admin-api.md#wire-conventions) apply.
[architecture/backups.md](architecture/backups.md) describes what a backup
holds and how it is taken, rotated, and restored. Full restore is the offline
`silo backup restore` command, not an API operation; selective restore of
settings, plugins, libraries, accounts and their profiles' data, collections
and uploaded artwork runs as an admin job, from a backup of this server or
from a restore set [uploaded](#uploads) from another server.

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
| `GET /admin/backups/uploads` | Restore sets uploaded to this server, finished or not |
| `POST /admin/backups/uploads` | Start uploading a restore set |
| `PUT /admin/backups/uploads/{upload_id}/parts/{part}` | Store one part of an upload |
| `POST /admin/backups/uploads/{upload_id}/complete` | Check a fully received upload and freeze its digest |
| `DELETE /admin/backups/uploads/{upload_id}` | Delete an upload |
| `GET /admin/backups/uploads/{upload_id}/restore-set` | Preview an upload's restore set against this server |
| `POST /admin/backups/uploads/{upload_id}/restore` | Restore chosen items of an upload as a `backup_restore` admin job |
| `GET /capabilities/profile-export` | Profile export and import support, for any verified profile |
| `GET /profile/export` | Download the acting profile's own data |
| `POST /profile/import` | Merge a profile export into the acting profile |

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
when it has a local operational store. `selective_restore` and `download` are `true` when available.
`upload` is `true` when the server has an operational store (uploads always
go there) and the user store is PostgreSQL (a restore's own backup needs it).
`max_upload_bytes` is the largest restore set an upload takes (2 GiB).

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
  `operation` (`backup`, `delete`, `restore`, or `upload`, the check of a
  completed upload), `backup_id` (for a restore, its source backup),
  `upload_id` (for a restore from an upload, its source; for `upload` or
  the delete of an upload, that upload), `started_at`, `progress` (0–100),
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
  `missing_blobs` (`store`, `key`, `source`), `restore_set_omitted` (items
  the restore set could not carry: `section`, `id`, `label`, `reason` of
  `too_large` or `export_failed`, `message`, and for `export_failed` in
  restore sets of format 3 `cause`, what failed (the part being read, such
  as a profile data type, and the step its error names:
  `notifications: reading notification preferences`), stable across backups
  so a client can group items without parsing `message`, and `error`, the
  underlying error without ids; the dump still holds them),
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
  missing when copied (`missing_blobs` lists them), or the restore set left
  items out (`restore_set_omitted`).
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
downloads is one `silo backup restore --from` reads, and `restore-set.tar` is
the file an [upload](#uploads) on another server takes. Each `url`
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

The answer carries `backup_id` (or `upload_id` for an
[upload](#uploads)), `digest` (the SHA-256 of `restore-set.tar`; a restore
request must send it back), the source (`server_id`, `server_name`,
`silo_version`, `created_at`, `same_server`), `key_matches` (whether the
backup was taken with this server's `SECRET_KEY`), `missing_blobs`,
`omitted` (items the restore set left out, as in the list; they are in no
section),
`never_restored_settings` (the setting keys a restore never writes because
they describe this server; an entry ending in `.` or `_` is a prefix),
`library_map` (each backup library with `local_id` when a library here has
the identical folder set), and `sections` in restore order: `libraries`,
`plugins`, `settings`, `collections`, `home_sections` (format 3 and later),
`users` (format 2 and later). Each section has `count` and `items`.

Every item has `id` (what a restore request names), `kind`
(`library`, `plugin`, `settings_page`, `account`, `collection`,
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
| `ambiguous` | More than one collection here has its name, or an account or profile has more than one candidate; not selectable |
| `needs_another_key` | Encrypted with another `SECRET_KEY`; not selectable |

`reason` codes: `same`, `path_in_use` (a folder belongs to another library
here), `folder_missing`, `library_not_restorable`, `library_not_mapped`,
`ambiguous_name`, `needs_another_key`, `config_needs_another_key` (a plugin
that installs without its configuration and stays disabled),
`other_version` (another version is installed: settings only),
`item_not_on_this_server`, `file_missing` (its image was missing when the
backup was taken), `not_in_backup`, `changed_since_preview`, and for accounts
and profiles `ambiguous_account`, `ambiguous_profile`, `policy_wider` (this
server would grant a new account more than the backup did),
`group_not_on_this_server` (no access group of the backup group's name),
`login_taken` (another account here signs in with its username or email),
`profile_limit` (the account has no room for another profile),
`user_store_unsupported` (this server uses the SQLite user store),
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
- `account`: `account` with `username`, `email`, `role`, `admin` (restoring
  creates an administrator), `match` (`account_id`, `restored`,
  `username_email`, or `new`), `target` (what the request sends back: the
  local account id, or `new`), `local_id` and `local_username` of a matched
  account, `group` (the backup's group name, which a new account joins here),
  `policy_differences` (`{field, backup, here}` for each policy field this
  server would grant a new account more of), `libraries_left_out` (library
  entries with no library here, dropped), `profile_room` (how many new
  profiles the account can take), and `profiles`. Each profile has `id`,
  `name`, `status` (`not_on_this_server`, `differs` for a matched profile
  a restore would change, `on_this_server` with reason `same` for one it
  would not change, never selectable, or `ambiguous`), `selectable`, `reason`,
  `message`, `match` (`profile_id`, `restored`, `name`, or `new`), `target`,
  `local_id`, `is_primary`, `restricted`, `libraries_left_out`,
  `no_library_access` (restricted with none of its libraries here),
  `has_pin`, and `data_types` (`{id, count, pending}` for `watch_history`,
  `ratings`, `favorites`, `collections`, `dismissals`, `preferences`,
  `notifications`): `count` is the entries the backup holds, and `pending`,
  for a matched profile only, how many of them a restore would add or
  change by its merge rules. Items are matched by content id only for the
  count, so an item a restore would match by provider ids counts as not
  here.
  A matched account stays as it is, gains the profiles it lacks, and merges
  the chosen data. It is `differs` and selectable when a restore would
  change something (a profile with pending changes, or one it can add), and
  otherwise `on_this_server` with reason `same`, not selectable. The counts
  reflect this server when the preview runs.

### Restore

`POST /admin/backups/{id}/restore` with `{digest, selection}` queues a
`backup_restore` admin job and answers `202` with the job (`AdminTaskJob`),
a `Location` of `/api/v2/admin/jobs/{job_id}`, and `Retry-After: 5`.
`selection` has `libraries` (backup library ids), `plugins` (plugin ids),
`settings` (settings page ids), `collections` (`collection:<id>`,
`template:<id>`, `artwork:<content_id>:<image type>`, `branding:<setting
key>`), and `home_sections` (`section:<id>`), each the `id`s the preview
returned, and `users`: `[{account, target, profiles:
[{profile, target, data_types}]}]` with the account and profile ids and the
targets the preview showed. An account with no profiles is restored alone;
a profile with no data types is created or kept without data. The restore
matches each account and profile again and leaves out one whose target is
no longer the mapping (`changed_since_preview`). `409 restore_set_changed` when the backup changed
since the preview (the digest differs): preview it again. `409 conflict` when
another backup or restore is running (a running restore's job is in
`Location`) or the backup has no restore set. `422` for an empty or malformed
selection. The request holds the cluster-wide lock while it checks the source
again and queues the job, so a backup or upload deleted or rotated away
meanwhile answers `404`, never a job without its source; once queued, the
source is kept until the restore ends.

Follow the job through the [admin jobs endpoints](admin-tasks-api.md):
`GET /admin/jobs/{id}` (progress counts items; the before-restore backup is
the first), and `POST /admin/jobs/{id}/cancel`, which stops the restore
between items. A restore job carries `backup_restore_source` (`backup_id`, or
`upload_id` for an upload) from the moment it is queued. Its `backup_restore_result` is the report,
kept with the job when it completes, fails, is canceled, or is interrupted:

- `backup_id` or `upload_id`: the source.
- `before_restore_backup_id`: the recovery point taken before anything was
  restored. Restoring it in full with `silo backup restore` goes back to how
  the server was; a selective restore of it cannot remove what a restore
  added.
- `library_map`: each backup library and the library it is on this server.
- `items`: `{section, id, kind, label, outcome, reason, message, error,
  restart_required, restart_keys, notes, missing_blobs, account}` for each
  item that ran. `account` (users items) has `local_id`, `created`,
  `unmatched` (titles of items not in this catalog, left out), `hidden`
  (titles a restored profile's libraries, content rating, or advisory limits
  hide here, left out), and
  `profiles`, each `{id, label, local_id, created, outcome, reason, message,
  notes, data}` with `data` as `{type, added, updated, unchanged, left_out,
  notes}` per data type merged. `outcome` is `restored`, `unchanged` (already as in the backup),
  `left_out` (with a `reason`), or `failed` (with an `error`).
  A restore writes files (artwork, branding, posters, avatars) only where
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

## Uploads

A restore set from another server is uploaded in parts and then restored
from as a backup is. The file is the bare `restore-set.tar` that
[a backup's download](#downloads) gives; its name does not matter. Parts are written straight to this server's
backup storage, so any API server takes any part, and the session lives in
the database. [architecture/backups.md](architecture/backups.md#uploads)
describes the storage and the rules.

Each upload is `{id, status, size_bytes, part_size, part_count,
received_parts, received_bytes, created_at, updated_at, location}`, plus
`completed_at`, `digest`, and `source` (`server_id`, `server_name`,
`silo_version`, `created_at`, `format_version`) once complete,
`expires_at`, and `rejection` (`reason`,
`message`) when rejected. `status` is `uploading`, `checking` (completion is
reading it), `complete`, or `rejected`.

- `POST /admin/backups/uploads` with `{size_bytes, sha256?, part_size?}`
  answers `201` with the upload. `part_size` is 1–16 MiB and defaults to
  4 MiB. With `sha256` set, completion rejects any other bytes. `413
  payload_too_large` above 2 GiB; `409 upload_limit_reached` when three
  uploads are already uploading or checking (unfinished uploads are deleted
  24 hours after their last part); `409 conflict` when a local destination
  lacks room for what every open upload, this one included, has yet to write
  (four parts more each), plus 1 GB.
- `PUT /admin/backups/uploads/{upload_id}/parts/{part}` with an
  `application/octet-stream` body stores part `part` (counting from 0) and
  answers the upload. Every part but the last is exactly `part_size` bytes.
  Parts may arrive in any order, on any API server, and a part sent again
  replaces the earlier copy, so a failed part is simply sent again. `422` for
  a part outside the upload or shorter than its position, `413` for a longer
  one, `409` once the upload is checking or finished, while the same part or
  four parts of the upload are being received, or when a local destination
  lacks the part plus 1 GB; `408` when the part does not arrive within 2
  minutes.
- `POST /admin/backups/uploads/{upload_id}/complete` reads every part in
  order through the [restore set checks](architecture/backups.md#restore-set)
  under the cluster-wide lock, for at most 10 minutes, freezes the SHA-256 of
  the bytes it read as `digest` (the file as uploaded, the bytes the client
  sent), and answers `complete` with `source`, or `rejected` with
  `rejection.reason`:
  - `not_a_restore_set`: the reader refused it; `message` says why.
  - `newer_silo`: "This backup is from a newer Silo. Upgrade this server
    first."
  - `checksum_mismatch`: the bytes are not the `sha256` the upload declared.

  A rejected upload's parts are deleted at once. A complete or rejected
  upload answers as it is, so completing again is safe. `409 conflict` while
  parts are missing, or while another backup or restore holds the lock:
  the parts stay, and completing it again later checks it. The check runs to
  its end when the client disconnects; `GET /admin/backups/uploads` shows the
  upload `checking` until it does. A check whose server lost the lock before
  writing its outcome answers `409 conflict`, and the upload stays `checking`
  for the next completion to check again.
- `GET /admin/backups/uploads` answers `{items}`, newest first, after
  deleting uploads unfinished or rejected for 24 hours and complete ones
  unused for 7 days. A complete upload's `expires_at` moves on each
  completion, preview, and restore request; one a queued or running restore
  names is never deleted.
- `DELETE /admin/backups/uploads/{upload_id}` answers `204`. `409 conflict`
  while a restore from it is queued or running, or, for a complete upload,
  while another backup or restore holds the lock.
- `GET /admin/backups/uploads/{upload_id}/restore-set` and `POST
  /admin/backups/uploads/{upload_id}/restore` are the
  [preview](#preview-a-restore-set) and [restore](#restore) of a backup, for
  a complete upload: the same answers, bound to the digest completion
  froze. `409` for an upload that is not complete. An upload from another
  server matches accounts by username and email and libraries by folder
  paths, and with another `SECRET_KEY` (`key_matches` false) offers no
  encrypted settings or plugin configuration. The before-restore backup and
  every restore rule apply unchanged.

## The backup task

The `backup` task runs daily at `backup.time` in server local time, skipping
scheduled runs while `backup.enabled` is off. Every server fires the trigger;
the lock lets one back up and the others record a completed run whose result
is `{"skipped": "skipped: another backup or restore is running"}`. A run's
result data carries `id`, `kind`, `status`, `size_bytes`, `duration_ms`,
`missing_blobs`, `rotated`, `swept`, and `warnings`.

## Profile export and import

A signed-in profile exports and imports its own data. These operations are
profile scoped (`X-Profile-Id`, verified), not admin operations, and the
target is always the acting profile: a document never chooses it.
[architecture/backups.md](architecture/backups.md#profile-export) describes
the format and rules.

`GET /capabilities/profile-export` answers `state` (`available`, or
`not_configured` on the SQLite user store), `format`
(`silo-profile-export`), `version`, `max_import_bytes`, and `data_types`.

`GET /profile/export` answers the `silo-profile-export` document as an
attachment (`silo-profile.json`): watch history and progress, ratings,
favorites and watchlist, personal collections, dismissals, preferences, and
notification preferences. It holds no account fields, password or PIN
hashes, or access fields.

`POST /profile/import` takes that document (at most 64 MiB) and merges it
into the acting profile in one transaction: newer progress wins, plays,
ratings, favorites, and dismissals are added where missing, collections gain
missing items, and preferences are overwritten. Nothing is deleted;
importing the same document again changes nothing. The answer has
`same_server`, `data` (`{type, added, updated, unchanged, left_out, notes}`
per data type), `unmatched` (titles not in this catalog), and `hidden`
(titles the profile may not see: outside its libraries or above its
content-rating or advisory limits); both were left out. `422
validation_failed` for a document carrying account or access fields, from a
newer version, or otherwise not a valid export; `403
profile_verification_required` for a PIN-protected profile reached without
its PIN, including through an API key.
