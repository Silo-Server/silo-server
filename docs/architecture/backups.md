# Backups

`internal/backup` takes whole-server backups: a `pg_dump` of the database plus
the blobs that cannot be fetched again, and a restore set of versioned
sections for selective restore. It lists, rotates, and deletes them; the
`silo backup` command restores one in full, and the admin API restores chosen
parts of one, or of a restore set uploaded from another server (see
[Selective restore](#selective-restore) and [Uploads](#uploads)). A profile can
also export and import its own data (see [Profile export](#profile-export)).
The endpoints are in [backups-api.md](../backups-api.md).

A backup is a recovery point for this server's own data. It does not include
media files (Silo never manages them, see [non-goals](../non-goals.md)),
rebuildable data (provider artwork, chapter images, trickplay, Redis,
Meilisearch, transcode and download folders), `SECRET_KEY`, `.env`, or
Compose files. It is not point-in-time recovery; deployments that need that
archive WAL at the PostgreSQL layer.

Backups refuse to run on the SQLite user store (`userdb.backend=sqlite`): a
backup must never claim to be complete without user data. The message is
"Backups need the PostgreSQL user store. Import your SQLite user store first."

## Layout

Each backup is one folder at the destination:

```
backups/<server-id>/<UTC time>-<kind>/
  db.dump          pg_dump custom format, zstd level 3
  restore-set.tar  sections for selective restore
  blobs.tar        assets/<key> and operational/<key> entries
  manifest.json    written last
```

- `server-id` is `server.identity_id` (`internal/serveridentity`). Every server
  writes and rotates only its own prefix; other prefixes at the destination
  are listed by id and otherwise left alone. `backups/uploads/` holds
  [uploaded restore sets](#uploads) and is never a server id.
- The folder name is the backup id: `20261031T040000.123Z-nightly`. The time
  is the start of the run in UTC, to the millisecond; ids written to the
  second (`20261031T040000Z-nightly`) still parse. Kinds are `nightly`,
  `manual`, and `before-restore`. Names that do not parse,
  such as a full restore's `<time>-overwritten-blobs`, are never treated as
  backups.
- **A folder is never reused.** A backup reserves its id under the subsystem
  lock from the folders at the destination, complete or not and of any kind:
  a time a folder already has (two runs in one millisecond, or a clock that
  went back) moves on a millisecond until it is free (`reserveID`). Every
  folder's time is therefore distinct, which keeps rotation's newest-first
  order total. "Back up now" reserves the id before it answers, under the lock
  the run inherits, so the id it returns is the folder written.
- **A folder without `manifest.json` is incomplete.** The manifest is the last
  object written, so its presence is the commit point. Listing shows an
  incomplete folder as `in_progress` only while the published active
  operation (see [Status](#status)) is that backup, and `incomplete`
  otherwise. The sweep deletes incomplete folders whose name and newest
  object are both older than 24 hours.

### Manifest

`manifest.json` has `format: "silo-backup"` and `version: 1`. Readers accept
every version up to theirs and refuse a newer one. It records the id, kind,
server id, start and completion times, Silo version, goose version (the
highest applied `goose_db_version`), `applied_migrations` (every applied
goose version, ascending, without goose's version 0; it must end at the goose
version), PostgreSQL server version, `pg_dump`
version, the tables whose data was excluded, each object's size and the
SHA-256 of its stored bytes, the `SECRET_KEY` check value, how the table of contents was checked, the blob count and bytes,
`missing_blobs`, the core table inventory, and, when the backup has a restore
set, `restore_set`: its format version and the item count of each section.
The objects are exactly `db.dump` and `blobs.tar`, plus `restore-set.tar`
when `restore_set` is present.

The inventory covers `users`, `user_profiles`, `media_folders`, `media_items`,
`media_files`, `server_settings`, `plugin_installations`,
`library_collections`, `user_watch_history`, and `user_watch_progress`. Each
is "present" with a row count or "absent", checked with `to_regclass` in the
backup's snapshot, so an older schema without a table records "absent"
instead of failing. A full restore compares the restored database with it,
presence and counts both.

Status is `complete` when every referenced blob was copied and
`complete_with_warnings` when some were missing. "Complete" means the manifest
exists, checksums are recorded, and `pg_restore --list` read the table of
contents. It does not claim the backup was restored; `silo backup verify` is
the drill for that.

The check value is HKDF-SHA256 over `SECRET_KEY` with info
`silo/backup-key-check/v1`, 8 bytes, written as 16 hex digits in groups of 4
(`secret.KeyCheckValue`, beside the data key derivation in
`internal/secret/cipher.go`). `silo secret check` reads a key on stdin and
prints the value, so an operator can tell which key a backup needs without
showing either key.

## Taking a backup

1. Preflight: the user store is PostgreSQL, the destination exists, its id
   is reserved, `pg_dump` and `pg_restore` are installed, and the destination
   has free space of at least 1.5 times the previous
   backup plus 1 GB. The default destination shares a disk with the database,
   and filling it would take PostgreSQL down.
2. Open a `REPEATABLE READ`, read-only transaction and call
   `pg_export_snapshot()`. In the same transaction read the server and schema
   versions, the table inventory, and the referenced blob keys.
3. Run `pg_dump --format=custom --compress=zstd:3 --snapshot=<id>` with
   `--exclude-table-data-and-children` for `activity_log`, `operational_logs`,
   `policy_decisions`, `catalog_search_index_events`, `backup_status` (its row
   describes the backup being taken), `backup_uploads` and
   `backup_upload_parts` (their parts are not in the backup), and, unless
   `backup.include_intro_fingerprints`, `media_intro_fingerprints`. The schema
   is always dumped. The password moves from the URL into `PGPASSWORD` so it
   never appears in a process listing.
4. Stream `pg_dump`'s stdout straight into the destination with no local
   spool, hashing the stored bytes. `pg_restore --list` reads a tee of the
   stream. `pg_restore` reads a custom-format archive's table of contents
   from a pipe and exits without reading the data; the tee then drops its copy
   and the upload continues. If the piped check fails, the check reads the
   stored object back. The manifest records which happened (`pipe` or
   `read_back`).
5. Write `restore-set.tar` (see
   [Restore set](#restore-set)): its documents and plugin archives come from
   the same snapshot transaction, then the snapshot closes and the section
   blobs are copied, as blobs.tar's are.
6. Copy the blobs into `blobs.tar`.
7. Write the manifest, then rotate and sweep. A failure in any earlier step
   leaves an incomplete folder and deletes nothing.

Missing fingerprints are safe to leave out: existing markers live elsewhere,
and intro detection recomputes a season's fingerprints only when the season's
input signature changes, for example when a new episode arrives.

### Blobs

Blob keys come from the snapshot's references, never from listing the store:

| Source | Store |
|---|---|
| Item, season, episode, and person artwork whose source is `upload://` (`metadata.UploadedArtworkColumns`) | assets |
| Admin collection posters and backdrops, personal collection posters | assets |
| Library posters (`media_folders.poster_path`) | assets |
| Branding assets (`branding.*_ref` settings, `branding.AssetObjectKey`) | assets |
| Uploaded profile avatars (`user_profiles.avatar` starting `upload:`) | operational |
| Downloaded subtitles (`downloaded_subtitles.s3_key`), unless `backup.include_subtitles` is off | assets |

For an artwork original (`.../original[.<revision>].<ext>`) the copy also
takes the stored variants of the same revision in its directory. Only the
referenced key itself is required.

A backup carries only keys in these namespaces: `local/` (uploaded item
artwork is cached as library artwork), `collection-images/`,
`user-collection-images/`, `library-posters/`, `branding/`, and `subtitles/`
in the assets store, and `profile-avatars/` in the operational store. A
reference outside them (provider artwork a collection borrows, for example)
can be fetched again and is not copied. Nothing under `backups/` is ever a
blob a backup carries.

Blobs are copied after the snapshot closes, so the copy is not frozen with it.
A key replaced in place (library posters, some collection images) may carry
bytes newer than the snapshot. A referenced key can be deleted during the
backup: superseded collection image revisions and replaced downloaded
subtitles are deleted immediately, and only derived media images go through
the delayed `blob_gc_queue`. No delay is guaranteed for the blobs a backup
copies. A key missing at copy time is recorded in `missing_blobs` and the
backup completes with warnings: the database and every other blob are still a
valid recovery point. Any other read error fails the backup.

## Destinations

`backup.Destination` is the seam for every destination: put a stream, open,
list, delete a folder, probe, report local free space, and presign a
download. The one destination, `default`, is the operational store from
`blobstore.Open` on local artwork storage, the local root. On an S3 artwork
backend there is no local operational store, so there is nowhere to write
backups; the Backups page says so and links to Storage settings. Uploads use
the same store.

**Every API server must share the operational store.** Backups are written by
whichever server runs the task and listed, downloaded, and deleted by any
other. On a local artwork backend that is the shared-volume requirement
artwork already has in a multi-server deployment
([blob-storage.md](blob-storage.md)).

Local backups hold the same data as the database, password hashes included,
and sit in the data folder with the database's protection. Downloads are
short-lived and scoped to one object: a presigned URL where the store can
presign, or a link to `GET /api/v2/admin/backups/{id}/objects/{name}` signed
under its own capability domain (`artworkurl.NewBackupObjectSigner`) where it
cannot. A full backup downloads its objects as stored.

## Tools

`pg_dump` and `pg_restore` run on the API servers. The omnibus image carries
them with its PostgreSQL server; the regular image installs
`postgresql-client-18` from the PostgreSQL apt repository. `backup.FindTools`
prefers the newest `/usr/lib/postgresql/<major>/bin` and falls back to `PATH`.
`pg_dump`'s major version must be at least the server's: a backup checks
before dumping, and the Backups list reports a missing or older client.
Restoring needs a `pg_restore` at least as new as the `pg_dump` that wrote the
backup.

## Coordination

One cluster-wide lock covers the subsystem: a PostgreSQL session advisory lock
(`backup.SubsystemLockKey`) held on a connection of its own, opened with the
application pool's settings but outside the pool. Work under the lock takes
pool connections (the snapshot transaction, settings reads), so a lock held
on a pool connection would deadlock a pool of one. Every
operation that writes or deletes backups, or restores from one, holds it: the
nightly and manual backup, rotation, deletion, the sweep, upload completion,
the deletion of a complete upload, and the restore job.

- Rotation and the sweep run inside the backup that holds the lock.
- "Back up now" takes the lock in the request and hands it to the task run it
  starts, so there is no window between the check and the backup. It and
  "Delete" answer 409 when the lock is held.
- A scheduled run that finds the lock held records
  "skipped: another backup or restore is running". Every API server fires the
  same daily trigger; one backs up and the others record the skip.
- A held lock's session is checked every 10 seconds. If it ends, the work
  holding the lock is canceled (`pg_dump`, uploads, a restore), and the
  incomplete folder it leaves is the sweep's.
- Whether the lock is held is read from `pg_locks`, never by taking it: a read
  that took the lock even briefly could make a nightly run skip.

### Status

The lock holder publishes what it is doing in the one-row `backup_status`
table, so any API server can report a run another server started: the active
operation (`backup`, `delete`, `restore`, whose backup or upload id is its source, or `upload`, the check of a completed upload), its backup or upload id, start time, progress and
message, and, when a backup run ends, its id, kind, outcome, and error, and
for a run that succeeded its finish time as `last_success_at`, which a
failed run after it leaves alone. Lock occupancy alone says nothing about
which operation holds it.

- Status writes run on the lock's own session, serialized with its session
  check and release, and each statement requires that session to hold the
  advisory lock (`pg_locks`, `pid = pg_backend_pid()`). A publication cannot
  outlive the lock that justified it, even when its write is delayed.
- Each operation gets a token when it publishes. Its progress and end writes
  apply only while the row still carries that token, and it writes nothing
  once its lock session has ended, so a holder that lost the lock can never
  change what the next holder published.
- The row names the session holding the lock: its backend pid and start
  time. A lock session holds the lock once and is then unlocked and closed,
  so a session names one acquisition. A reader believes
  the active operation only while `pg_locks` shows that session holding the
  lock; otherwise the row describes an interrupted run, even while an
  unrelated operation holds the lock.
- The next holder, when it publishes, first records an active backup left in
  the row as the latest run, `interrupted`, with its id, kind, and start
  time, in the same statement.

A restore holds the subsystem lock for its
whole run and takes its before-restore backup by calling the backup function
directly, never by acquiring the lock again.

## Scheduling and settings

The `backup` task runs daily at `backup.time` (server local time) and on
"Back up now". `ShouldRun` reads `backup.enabled`, so turning backups off stops
only scheduled runs. The setting owns the time: each server points the task's
trigger at it at startup and on every settings change, so an edit to the task
schedule itself does not stick. Task history records each run with its id,
status, size, duration, missing blobs, and what rotation removed.

| Key | Default | Rule |
|---|---|---|
| `backup.enabled` | `true` | boolean |
| `backup.time` | `04:00` | 24-hour `HH:MM` |
| `backup.keep` | `7` | 1–60 |
| `backup.include_intro_fingerprints` | `false` | boolean |
| `backup.include_subtitles` | `true` | boolean |

Every `backup.*` key is server-specific: selective restore never offers one.

An absent or invalid value takes its default, but a setting that cannot be
read fails the backup or the listing, and skips rotation with a warning. It
never falls back to the default: a failed read of `backup.keep` could rotate
away copies the admin meant to keep.

## Rotation

- Rotation runs only after a manifest is written. A failed backup never
  deletes anything, and folders without a manifest are never rotated, only
  swept.
- `nightly` and `manual` copies share `backup.keep`; older ones beyond it are
  deleted, oldest first.
- The newest `nightly` or `manual` copy without warnings is never deleted, even
  when that keeps one more than `backup.keep`, so repeated runs with missing
  blobs cannot rotate away the last copy that has every blob.
- `before-restore` copies are deleted after 7 days and do not count toward
  `backup.keep`.
- Rotation manages only the current server id's prefix, and never deletes a backup that is the source of a queued or
  running restore. A running restore holds the subsystem lock for its whole
  run, so no other rotation, sweep, or delete runs, and the rotation of its
  own before-restore backup is told to keep the source. A queued restore is
  read from the job queue under the lock rotation holds, the lock restores
  are queued under ([The restore job](#the-restore-job)); when the queue
  cannot be read, rotation deletes nothing and warns.
- Deleting a backup takes the same lock and refuses (409) while a queued or
  running restore reads it.
- A delete that fails leaves a warning in the task result.

## Restore set

`restore-set.tar` duplicates a small part of the database as versioned JSON
so that selective restore never loads the dump, works across Silo versions,
and works on another server.

```
restore-set.json                  index; always the first entry
libraries.json
plugins/<plugin-id>/plugin.json
plugins/<plugin-id>/archive.bin
settings.json
collections.json
home-sections.json                version 3 and later
users/<account id>.json           version 2 and later
blobs/<object key>
```

`restore-set.json` has `format: "silo-restore-set"`, `version` (3: version 2
added the users section; version 3 the home sections section, template
collections in `collections.json`, and an omitted item's `cause` and
`error`), the source
server id and name, Silo version, creation time, `SECRET_KEY` check value, and
a section index: for each section its item count, each of its files with
size and SHA-256, and the blob keys its documents reference. Plugin archive
digests are computed in the snapshot (`sha256(archive_bytes)`), so the index
is written before the archives; blob digests are not indexed, because blobs
are copied after the snapshot closes. The manifest's checksum of the whole
`restore-set.tar` is the restore set's digest, which a preview returns and a
restore request must carry.

Sections are registered in one table in `internal/backup/restoreset.go`, in
the order a restore applies them: libraries, plugins, settings, collections,
home sections, users. Collections come before the home sections that name
them, and users last, so a profile's page layout resolves against the
sections the same restore wrote. Each names its document (or per-item
files) and the blob prefixes it may carry, which are disjoint across
sections, and the first format version that may hold it. A reader refuses a
section, and a document field, it does not know, so adding either is a new
format version: an older reader refuses a newer set as "from a newer Silo"
instead of failing on what it does not know, and a set that claims an older
version while holding something added later (`users` in version 1,
`home_sections` or template collections in version 2) is refused. Readers
accept versions 1 to 3; version 3 is always written.

| Section | Holds | Blob prefixes |
|---|---|---|
| `libraries` | `media_folders` rows with their paths and per-library options, and the poster key | `library-posters/` |
| `plugins` | per installation: plugin id, version, enabled state, installation id, the stored archive (`plugin_archives`), and `plugin_runtime_configs` rows as stored | none |
| `settings` | `server_settings` rows the restore table offers, as stored | none |
| `users` | one document per account: the account's fields, its profiles with their fields and data by data type, and the items that data names (see [Users](#users)) | `profile-avatars/` (operational store), `user-collection-images/` |
| `collections` | manually managed admin collections with their library scope, group, and items; template-bundle collections (see below); uploaded item and season artwork (`upload://` sources); branding assets | `collection-images/`, `local/`, `branding/` |
| `home_sections` | every admin `page_sections` row of the home page and each library page as it is now, and the collection a section manages | none |

Encrypted values stay ciphertext under their source AAD: settings under
`server_settings:<key>`, plugin configuration under the source installation
id. Server-specific settings are never written into a restore set.

What a restore set leaves out on purpose:

- A template-bundle collection (`management_mode` `template_bundle`) carries
  no items and no template definition. The template catalog is code
  (`internal/collections/templates`), so the collections are the only record
  of which Starter packs were applied to which library: each carries its
  management key (`<bundle>:<template>:library:<library id>`), bundle and
  template ids, library scope and group slugs, and the fields an admin can
  change afterwards (title, description, visibility, featured, sort order,
  item sort, source, sync schedule, and a poster or backdrop the admin set,
  with an upload's blob; a template poster or a generated collage is not
  carried). Its items come back from the source by syncing.
- A collection a section manages (`management_mode` `section`) travels in
  the home sections section with its section, items included, and without
  uploaded images, which its collage replaces.
- Page section revisions (`page_section_revisions`,
  `page_section_scope_revisions`) are not carried: they are concurrency
  witnesses, and writing a section advances them through their triggers.
- Collection poster variants (`library_collection_poster_variants`) are not
  restored: they are per-viewer collages, regenerated on read, and backups do
  not carry their files.
- A library's metadata provider chain is not restored. A restored library
  gets the default chain, as a library created in the editor does.
- Builtin plugin rows and `plugin_instance_state` are never included.

**Restore sets are untrusted input**, whether read from the destination or
uploaded. The reader (`ReadRestoreSet`) checks the whole archive
before anything is applied: regular files only (links, directories, and
devices are refused); canonical relative paths with no empty, `.`, or `..`
segment, backslash, or NUL; no duplicate paths, sections, or item ids;
`restore-set.json` first; every other file listed in the index with its size
and digest, and every listed file present; blobs only under their section's
prefixes and only keys the section references, or variants stored in a
referenced artwork original's directory; branding entries only for a
branding asset setting (`branding.AssetSettingKeys`) whose reference is one
file name naming the entry's own blob, because a branding entry is written as
a setting; user documents whose file names their own account, with a known
role, bounded fields, unique profile ids, avatars only under
`profile-avatars/` and listed for the section, and data only of registered
data types, each passing its owner's strict checks; documents that decode strictly
(unknown fields and trailing data refused) and match the index counts; and
limits on entries (200,000), document size (64 MiB), plugin archive and blob
size (256 MiB), total bytes (32 GiB), and JSON nesting (32). A plugin archive
also gets the installer's normal checks when it is installed. Readers accept
every format version up to theirs and refuse a newer one with "This backup is
from a newer Silo. Upgrade this server first."

A referenced blob absent from the archive was missing when the backup was
taken; the preview and the report list it with the item.

Previews of a backup share one validated copy of its restore set, keyed by
the archive's SHA-256 from the manifest (at most 4 sets and 256 MiB of
documents and blob keys, least recently used evicted), and concurrent
previews of the same archive read it once. Only the parsed documents and
index are kept, never blobs or archives; comparisons with this server run on
every request. Applying a restore never uses the cache: the job reads and
verifies the whole archive again.

## Selective restore

### Settings

The restore table (`internal/backup/restore_settings.go`) is an allowlist,
grouped by the admin page that edits each key: a key or prefix with its class.

- **portable**: offered.
- **external service**: offered, and flagged in review as connecting to an
  outside service (mail, Discord, request integrations, metadata provider
  credentials, AI keys).
- **server-specific**: never offered: the server's identity and URLs, storage
  identities, locations and credentials (`artwork.*`, `s3.*`, `storage.*`),
  the search provider and its endpoint, key, and index, `database.*`,
  `redis.*`, `userdb.*`, `ratelimit.*`, `auth.jwt_secret`, push relay and web
  push keys, AI provider registry and bindings, local paths and hardware
  (`playback.ffmpeg_path`, `playback.transcode_dir`, `playback.hw_*`), and
  `backup.*`.

A key the table does not match is never offered. `TestEverySettingIsClassified`
fails for any admin setting default, sensitive setting, or branding asset key
the table leaves out. Branding asset references are restored with their blobs
in the collections section, not as settings.

Settings are chosen per page. A restore writes the page's differing values
through the admin settings write path (the same normalization, validation,
and `ServerSettingsMutationLock`) inside the item's transaction, then
publishes `EventSettingsChanged` per key and flags keys where
`config.RestartRequired` holds. An encrypted value is decrypted with this
server's key and written through the same path, which re-encrypts it under
the same key name, only when the backup's key check value matches; otherwise
it is never offered and the preview says so.

### Plugins

A plugin missing here is installed from the backup's archive through the
admin upload path's checks, with the installer's `KeepDisabled` option: the
installation is persisted disabled and is never enabled in between. Its
configuration is decrypted under the source installation id, checked with
the validation an admin save uses, and written re-encrypted under the new
installation id (`plugin_runtime_configs:config_value:<installation id>:<key>`).
Only then is the backup's enabled state applied, and after commit
`EventPluginsChanged` is published and a metadata provider joins the library
chains as an upload's does. Configuration that fails validation or cannot be
decrypted leaves the plugin disabled, and the report says why.

A plugin installed here keeps its installation and enabled state. At the
same version its configuration is restored the same way; at another version
only values the installed version accepts are written ("settings only").
With another `SECRET_KEY` a missing plugin installs without configuration and
stays disabled. `plugin_instance_state` is never restored.

A new installation's lifecycle event and provider-chain sync are owed
durably (see [Follow-ups](#follow-ups)). Reconfiguring an installed plugin
advances its runtime generation in the item's transaction, which replaces
stale processes even when the event after commit is lost.

### Libraries

A backup library carries its settings, the box set settings among them, its
folders, and its poster. It matches a library here only by an identical
folder set. A
library sharing a folder with another library here cannot be restored, nor
can one with a folder this server cannot open (checked as the library
editor's folder browser does). A restored library gets a new id, reserved
before its transaction so its poster is written under
`library-posters/<new id>` first; after commit the library editor's
follow-ups run: sections, provider chain, monitoring, and the first scan
(see [Follow-ups](#follow-ups)).
The job records a library map (backup id to local id), matched or restored,
in its report; later sections remap through it.

### Collections and artwork

A collection maps every library in its scope through the library map; one
whose library is not here is left out. It matches in its mapped library:
the collection with its backup id, else by name. A name match skips a
collection that has another backup collection's id or that another item of
the same restore wrote, and a name two of the backup's collections share in
that library is no identity, so a collection here with it makes both
ambiguous. No match creates it (with its backup id when no collection here
has it, else a new one; uploaded artwork copied under
`collection-images/<id>/` first; a smart collection's library ids remapped
and unmapped ones dropped), one adds the items it lacks, more than one is
ambiguous. A re-run therefore finds each created collection by id, even
when two share a name. In every library of its scope the collection goes back
into the group its membership had there, matched by slug in the mapped
library or created; only an ungrouped membership is changed, so a group an
admin chose since stays, and a re-run repairs a membership left ungrouped. Items not in this catalog
are left out. Uploaded item and season artwork is published for the same
content id, whose blob key is the same on every server, as an upload does,
with the images lock. Branding assets write their blob and then their setting
through the admin settings write path.

### Template collections

A template collection maps its library through the library map and matches
by its remapped management key within it; two of the backup's that would
share one key here (their libraries map to one library) are ambiguous. Its template must be in this
server's catalog and suit the library; otherwise it cannot be chosen
(`template_not_available`). A field counts as the admin's change only where
the backup differs from the template's default as a bundle apply creates it
here (`LibraryCollectionHandler.TemplateCollectionDefaults`, the input the
bundle apply itself builds, so validation and defaults match), and a restore
writes it only where the collection here differs from the backup; a field
the admin changed here since, that the backup left at the default, stays.
Visibility is written only as the backup had it, never wider. An existing
collection takes those changes and goes back into its groups. A missing one
is created in the item's transaction from that same input, under the mapped
library's key and a free slug, with the changes applied and its groups, and
records a `collection_created` follow-up: after commit the template's poster
is stored when the collection still uses it, the collection syncs, and its
next scheduled sync is set. A failed sync keeps the follow-up for the next
restore run. A placeholder template that needs a source (the TMDB franchise
template) is created and noted, and does not sync until one is set.

### Home sections

A page section maps its library page and every library its configuration
names (`library_ids`, `filter_library_ids`, `filter_library_id`,
`generated_library_id`, `library_id`) through the library map; one naming a
library that is neither here nor restorable cannot be chosen. A collection
it names is mapped through the collections section: a manual collection by
name in its mapped library, a template collection by its remapped
management key, and, on the server that took the backup, one the set does
not carry by its id. One that is neither here nor in the backup cannot be
chosen (`collection_not_mapped`); one only in the backup is required with it.
A section matches by id: on the server that took the backup any section of
the page with it, on another server one a restore created from it (same
page and type). Otherwise it matches by scope, library, type, and title with
exactly one candidate, skipping a section that has another backup section's
id or that another item of the same restore wrote; more than one candidate
is ambiguous, and so is any candidate when two of the backup's sections
share that page, type, and title, since the title then identifies neither.
So two such sections restore as two, and a re-run finds each by its id. A matched section that differs lists what a restore replaces
(title, position, featured, item limit, enabled, configuration, and missing
items of its managed collection), and a restore replaces its configuration
as an admin save would. A missing one is created, with its backup id when
no section here has it, so a profile override naming it by id resolves. The
section editor's checks (`SectionHandler.CheckRestoredSection`: type,
scope, configuration, recipe) run first, the write locks the collections it
names as every section writer does (`sections.WriteSectionInTx`), and a
featured section stays the only featured one on its page. A managed
collection is kept, adding the items it lacks, or created with the section,
its management fields renamed to the section's id where they named the old
one.

### The restore job

`POST /admin/backups/{id}/restore` queues a `backup_restore` admin job with
the selection and the preview's digest; one restore can be queued or running
at a time (a partial unique index). The request takes the subsystem lock
(409 while another backup or restore holds it), checks the source and its
digest again, creates the job, and releases the lock. Backup deletion,
rotation, and upload deletion and expiry check the queued restore's source
under the same lock, so either they see the job or the job's check sees the
source gone; a source can never disappear between the check and the queue.
Whichever API server claims it:

1. takes the subsystem lock and holds it for the whole run;
2. reads the restore set and refuses it when its digest differs from the
   request's;
3. takes a `before-restore` backup by calling the backup function under the
   same lock, and records its id; if that fails, nothing is restored;
4. publishes `restore` as the active operation;
5. applies the chosen items section by section. **Each item runs in its own
   transaction that begins with `SELECT … FROM admin_jobs … FOR UPDATE`,
   requiring status running and the worker's claim generation, holds that
   lock through commit, and writes the item's outcome into the job's result
   in the same transaction.** The package write paths take that transaction.
   A failed item rolls back to a savepoint and is recorded as failed; the
   restore continues. Events are published after each commit.
6. honors cancellation between items, never inside one.

Every item is matched again when it is applied, so a mapping that changed
since the preview is left out and reported, and running the same restore
twice reports what already committed as `unchanged`.

**A stale restore is never requeued.** `RequeueStaleRunning` marks a
`backup_restore` job whose worker stopped heartbeating failed with error
`interrupted`, keeping the report of what committed. The stale updater waits
on the row lock an item holds, so it cannot mark the job between an item's
check and its commit, and a slow original worker's next item finds the job no
longer running under its claim and writes nothing. Running the same restore
again is safe.

### Blobs a restore writes

A restore writes a blob only where no object is, with an exclusive create
(`blobstore.CreateExclusive`): on a local store the synced temporary file is
hard-linked into place, which fails when the name exists, and on S3 the
upload carries `If-None-Match: *`. A concurrent upload of the same key on
another server is therefore never overwritten. There is no check-then-write
fallback, which a writer landing between the two could lose to: a bucket
that rejects the conditional header, or a store without an exclusive create,
answers `blobstore.ErrExclusiveUnsupported`, writes nothing, and the item
fails with "this storage can't guarantee existing files are kept; restore
this item manually" (`ErrBlobNotExclusive`); other items go on. Ordinary
writes to such a bucket are unaffected. An object found at the key with the same bytes is left as it is; one
with other bytes fails the item and is never replaced (`ErrBlobConflict`). Uploaded item artwork and branding keys
are content-addressed revisions, and library posters and collection images
are written under ids the restore just took, so a legitimate restore finds
them absent or equal; an archive naming a live asset's key cannot replace
it, whether or not the item's transaction later commits.

### Follow-ups

Some work runs only after an item commits: a restored library's sections,
provider chain, monitoring, and first scan, a restored plugin's lifecycle
event and provider-chain sync, and a restored template collection's
template poster and sync. The item's transaction records it in
`backup_restore_followups`, and the row is deleted only once every step of
the work succeeded: the restore's follow-ups return each failed step
(sections, provider chain, scan queue, provider-chain sync), where the
editor and an admin upload only log them. A provider's chain placement comes
from its manifest metadata; when that lookup fails, no chain is written from
a fallback placement, on any path, and the restore's retry places it from
the manifest. A
crash or failure between the commit and the work leaves the row; every
restore job runs the owed rows before its own items, and after each item.
Only rows a restore recorded run, so a library or plugin that exists for any
other reason is never set up again. A library's follow-ups seed sections only
where none exist and leave a provider chain an admin already set; a target
that no longer exists drops its row.

The before-restore copy is a recovery point, not an undo: selective restore
merges and never deletes, so restoring that copy selectively cannot remove
what a restore added. Going back means restoring it in full with
`silo backup restore`.

### Users

The users section carries each account (`users/<account id>.json`) read from
the backup's snapshot, from the PostgreSQL user store; backups refuse to run
on the SQLite store, and on a server using it the section's accounts cannot
be chosen (`user_store_unsupported`). Each account is read under a savepoint
of the snapshot transaction. An account the restore set cannot carry, because
its document would exceed the 64 MiB document limit or reading it failed, is
left out and recorded in the index's and the manifest's `omitted` list
(`too_large` or `export_failed`); the backup is complete with warnings, and
the backup list and the preview name it. The dump still holds it. A failed
read also records `cause`, the part that failed and the step its error
names (`notifications: reading notification preferences`), stable across
backups so a client can group them, and `error`, the underlying error
without ids.

**Account fields:** username, email, password hash,
`local_password_login_enabled`, `password_change_required`, `enabled`, role,
permissions, and every policy override `internal/access/groups.go` resolves
(`library_ids`, with NULL kept distinct from an empty list,
`max_playback_quality`, `max_streams`, `max_transcodes`, both stream bitrate
caps, `transcode_allowed`, `audio_transcode_allowed`, `max_profiles`,
`download_allowed`, `download_transcode_allowed`, `requests_allowed`); the
access group's name and policy; and the account's effective policy in the
backup. `is_owner` and `break_glass` are never carried.

**Profile fields:** name, avatar, `is_primary`, PIN hash, and every field the
access resolver (`internal/access/resolver.go`) reads to decide access:
`is_child`, `max_content_rating`, `max_advisory_age`, `require_advisory_age`,
`library_restrictions_enabled`, the allowed libraries
(`user_profile_allowed_libraries`), and `max_playback_quality`. The PIN
revision is not carried; a restored profile starts its own.

**Data types**, each owned by the package that owns its tables and
implemented against the small interface in `internal/backup/userdata`
(registered in `internal/backup/users_registry.go`). Every import runs in the
caller's transaction, merges, and never deletes; running it again changes
nothing.

| Data type | Owner | Tables | Merge rule |
|---|---|---|---|
| `watch_history` | `watchstate` | `user_watch_progress`, `user_watch_history`, `user_history_hidden_items` | progress: one upsert takes the backup's row only when it is newer (`updated_at`) than the row as committed, so a playback heartbeat landing meanwhile wins; plays: added unless the profile has a play of the item at the same second, whatever its source; a play's source is a fixed one (`playback`, `import`, …) or a watch-sync plugin's, `plugin:<installation id>:<capability id>` (`userstore.ParsePluginWatchSource`; the capability id a lowercase slug of at most 128 characters), which stays on the server that wrote it, elsewhere names the one installation here of the same plugin (from the restore set's plugins section) with that capability, and otherwise becomes `import`, as does any source the reader would refuse when it is exported; history hides: `hidden_before` and `removed_before` each keep the later cutoff, independently (a legacy NULL `removed_before` reads as its `hidden_before`), and `-infinity`/`infinity` cutoffs round-trip as those strings. Plays and hides merge under the profile's history lock (`userstore.LockImportedHistory`) |
| `ratings` | `catalog` | `user_ratings` | only where the profile has no rating |
| `favorites` | `watchlist` | `user_favorites`, `user_watchlist`, `user_watchlist_titles` | added where missing; restored watchlist entries sort as local additions; a watchlisted title outside the library travels by its TMDB, IMDb, and TVDB ids and display snapshot and is attached as the watchlist adds one (`watchlist.AddSnapshotInTx`): to the title here holding its TMDB id, current or former, or a new title the watchlist's TMDB checks then refresh |
| `collections` | `usercollections` | `user_personal_collections`, `user_personal_collection_items`, `user_personal_collection_profiles`, `user_collection_groups` | by name among the profile's own collections: none creates it (new id, uploaded poster copied under it, in its shelf group, found by slug or label or created), one gains missing items, sharing, and its group when ungrouped here, several leave it out. The smart query, display filter, and sync source are all remapped to this server's libraries (`userdata.RemapQueryLibraries`); a definition that would lose its whole library scope leaves the collection out, never widened to every library |
| `dismissals` | `userstore/pgstore` | `user_home_item_dismissals`, `user_not_interested`, `user_dropped_series` | added where missing |
| `preferences` | `userstore/pgstore` | `user_setting_values` (profile, client-family, library, and series scopes), `user_audio_preferences`, `user_subtitle_preferences`, `user_library_playback_preferences`, `user_series_playback_preferences`, `user_collection_sort_preferences`, the home and library page layout (`user_settings` section overrides), and the preference columns of `user_profiles` | each differing value overwritten, under the account's preference-settings lock; setting keys, scopes, and values checked against this server's settings contract, others left out. A page layout replaces the profile's overrides of that page: the library is mapped, an admin section found by id (on another server only one of the same type, as a restore creates a section with its backup id) or else by type and title with one candidate, and library ids in section configuration remapped, and so are the collections it names (`library_collection_id`, `user_collection_id`, in `config` and `user_config`) through `Target.Collection`: an admin collection as the restore's collections and sections match it (a carried or template collection, a section-managed one through its section, and on the server that wrote the backup any collection still here by its id), a personal one as the account's personal collection import matches it (on the same server the account's collection by its id, else the one native collection with its name of its creator's profile here); an override with no counterpart is left out and counted, never written with a dangling id. Overrides are JSON no schema constrains, so an export leaves out one the reader would refuse, and one naming a section or collection that is gone, rather than make the document unreadable; a comparison leaves the same ones out of the layout here |
| `notifications` | `notifications` | `notification_preferences`, `profile_series_notification_mutes` | preferences overwritten; mutes added |

Never carried: login sessions, API keys, push and device registrations,
device-scoped settings, watch-provider and history-import credentials,
external sign-in links (OAuth, plugin identities, Discord), email delivery
addresses (they must be verified here), webhooks, downloads, logs, derived
state (aggregates, recommendation caches, collages, series interest),
onboarding tours, and beta Books data. **`TestUserTablesCoveredDB` fails for any table with a
`user_id` or `profile_id` column that no data type or account field set
claims and `excludedUserTables` does not list with a reason**, so a new
per-user table must be classified. No user-scoped value carried today is
encrypted.

**Lock order.** Both import entrypoints, a selective restore's account item
and a profile import, take their locks in one order before writing any data
row (`userdata.Preparer`, `internal/backup/users_locks.go`):

1. the account locks (`pgstore.LockAccountForImport`): the account's
   preference-settings lock, which profile creation and setting mutations
   take, then its section-override lock, which section override writes take;
   profile deletion takes both, in this order, right after it begins and
   before it locks or deletes any row;
2. each data type's own locks over every profile of the import: the
   profiles' history locks in profile id order (`watch_history`), then every
   watchlist title the import names, in media type and TMDB id order, created
   when this server lacks it (`watchlist.EnsureTitlesInTx`, `favorites`);
3. the data rows.

Ordinary writers take a subset of these in the same order (checked: profile
creation and setting mutations, device-setting resets, and section override
writes take their account lock first), so an import
never waits on a lock a writer holds while that writer waits on a row the
import wrote. Locking watchlist titles before any watchlist row matters
because watchlist promotion holds a title's lock while another connection
writes the library watchlist entry, a cycle across connections Postgres would
not detect; holding every title, including ones created for the import,
before the first row means a title another writer creates mid-restore waits
for the import rather than the reverse. A restore creates a missing account before taking its locks;
nothing else can hold the locks of an id its transaction has not committed.

**Items** are named by content id; each document lists every item its data
names with its kind, title, year, and IMDb, TMDB, and TVDB ids (an episode
with its series' ids and numbers). A content id this catalog holds is the same
item. Any other goes through the history import matcher
(`internal/historyimport`), whose reads run in the restore's or import's own
transaction (`historyimport.NewMatcherOn`): the transaction already holds a
connection, and a match that waited on the pool for another would wait
forever on a pool of one. An item still unmatched is left out and listed in
the report by title. Items the target profile may not see here are left out
too and listed as hidden: each profile's data is merged through the canonical
catalog visibility filter (`catalog.FilterAccessibleContentIDsInTransaction`)
with its libraries under the account's policy (`access.ProfileLibraries`) and
its content-rating and advisory limits (`access.ProfileMaturity`, with the
server's `access.unrated_content`). File ids and external subtitle paths are
restored only on the server that wrote them.

**Matching.** The preview returns a mapping per account, and a restore
request sends back the target it showed (`new` or the local account id), per
profile too:

- An account an earlier restore of this backup's server created
  (`backup_restored_accounts`) maps to it, so running a restore again never
  creates an account twice.
- Otherwise, on the server that took the backup, the account id.
- Otherwise by username and by email, case-insensitively, each checked
  against both columns because they share one sign-in namespace. The match
  needs exactly one candidate: either lookup finding several accounts, or the
  two finding different ones, makes the account ambiguous and unselectable.
  On the same server, a missing account whose sign-in another account now
  holds is unselectable (`login_taken`).
- Profiles match within the mapped account only: a profile an earlier
  restore created (`backup_restored_profiles`), the profile id on the same
  server, or else the name with exactly one candidate. Two backup profiles
  matching one profile by name are both ambiguous.

The job matches each account and profile again in the item's transaction and
leaves out one whose target changed (`changed_since_preview`); a target of
`new` that an earlier run of the restore has since created counts as
unchanged.

**New accounts.** The preview resolves the account's policy here with
`access.ApplyGroupPolicy` (the backup's overrides, library ids mapped) and
compares it field by field with its effective policy in the backup (library
ids mapped) using `access.WiderThan`, where unrestricted libraries, no quality
ceiling, and 0 caps are the open values. Any field this server would grant
more of makes the account unselectable (`policy_wider`) and is listed. An
administrator resolves against `AdminPolicy` on both sides and is marked
`admin` in the preview. An account with no group in the backup is created
with no group, compared against the no-group policy, and never given the
default group. An account with a group joins the group of that name here, if
there is one and it grants nothing more (`group_not_on_this_server`
otherwise); groups themselves are never restored. A new account keeps its
password hash, so it signs in with its old password.

**Access never loosens.** A library id with no library here is dropped from
an account's library list and from a profile's allowed libraries; a list
never becomes NULL, and restriction flags stay as in the backup. A restricted
profile left with no allowed library is restored with no library access, and
the report says so. Hidden-library and library-order preferences are mapped
the same way; navigation preferences that name libraries, sections, and
collections are restored only on the same server.

**Existing accounts and profiles** are never changed: not the password, role,
permissions, group, login or owner flags, PIN, avatar, name, or access
fields. A restore adds missing profiles and merges the chosen data types. A
profile is created only within the account's profile limit here, counted
under the same account lock ordinary profile creation counts under
(`pgstore.LockProfilesInTx`), so a restore and a concurrent creation cannot
both take the last place.

**What a matched account would change.** A preview counts, for each data
type of each matched profile, what a restore would add or change, by the
same merge rules, without writing or locking: every data type implements
`userdata.Type.Pending` beside `Import` (plays whose item and second are not
here, progress newer than here, hides that would advance a cutoff, missing
ratings, favorites, watchlist entries and titles, dismissals, and mutes,
settings and preferences that differ, page layouts whose overrides differ,
personal collections missing here or lacking items, shares, or their group,
notification preferences that differ). The counts run in one read-only
repeatable-read transaction over the whole users section, on the one
connection the preview already uses. Each data type compares a profile's
document with its rows in set-based queries (watch history's three in one
round trip, a profile's settings read at once), by content id, and only the
entries that would change are resolved through the profile's visibility
(`userdata.SettleChanged`), so a comparison that finds nothing to change
asks the catalog about nothing. Items resolve by content id and visibility
only, without the provider-id fallback an import has, so on another server
an item only that fallback finds counts as not here. A setting here is
compared as the settings contract normalizes it, as the backup's value is
(a stored `"eng"` and a normalized `"en"` are the same value, and the import
does not rewrite it either), and a page layout here is compared without the
overrides a backup cannot carry (an override of a section that is gone, or
one the reader would refuse); an import leaves such a page as it is. A
library id list here (`ui.disabled_library_ids`, `ui.library_order`) is
compared without the ids of libraries deleted since, which the backup's list
no longer names; the import then leaves it as it is too. The comparisons
pass whole documents as arrays, from a few entries to tens of thousands, so
the transaction sets `plan_cache_mode = force_custom_plan`: the connection
caches the statements, and Postgres would otherwise reuse a generic plan
that assumes ten array entries (an index probe and heap fetch per entry)
instead of the hash or merge join a plan for the real arrays picks. Watch
history documents, the large ones, are decoded ahead on a bounded number of
goroutines (`PreparePending`, a split of `Pending` into decoding and
comparing) while the comparison queries the connection, and the per-type
counts come from the restore-set reader's validation rather than a second
decode. On a local seed shaped like shared dev (184 accounts, 245 profiles,
about 445,000 plays with one account at 105,000, 350,000 progress rows, and
29,000 hides) the comparison takes about 1.6 seconds; one INFO line
("compared the backup's accounts with this server") records its time per
phase and data type, and a DEBUG line names the preference columns, setting
keys, and kinds of preference that differ per profile (never values).

A matched profile with nothing pending is `on_this_server` (`same`, not
selectable), and so is a matched account whose profiles all are and that
lacks none the restore could add; anything pending makes it `differs`. The
counts depend on this server's current state, so they are never kept in the
restore-set cache. The restore itself does not use them:
it merges again by the rules and reports what it did.

**Apply.** Each chosen account is one fenced item: the account, its new
profiles, and their chosen data commit together. Avatars and personal
collection posters are written under keys for the new ids
(`profile-avatars/<account>/<profile>/`, `user-collection-images/<id>/`)
with the exclusive create of [Blobs a restore writes](#blobs-a-restore-writes)
before the rows that reference them. The report lists each account with
`created`, its local id, the titles that did not match, and per profile the
outcome and what each data type added, updated, left unchanged, and left out.
A restore publishes no per-item realtime events for user data; clients read
the merged data on their next request.

### Profile export

A signed-in profile downloads and imports its own data as a
`silo-profile-export` document, version 1 (`internal/backup/profile_transfer.go`):
`format`, `version`, `server_id`, `exported_at`, `profile_name`, `data` (each
data type's document, as in a user document), and `items`. It never holds
account fields, password or PIN hashes, access fields, or allowed libraries.
Personal collection posters are not carried.

An import writes only the profile of the session, never one the document
names, in one transaction, with the merge rules above. It refuses a document
that carries an account or access field (at the top level or among the
profile preferences), unknown fields or data types, a newer version, nesting
deeper than 32, or more than 64 MiB; each data type then applies its own
strict checks. Items the profile may not see are left out and listed: outside
its allowed libraries (libraries it hid itself still count as visible), or
above its content-rating or advisory limits, through the same canonical
filter; so are preferences of libraries it cannot see. Library references survive only on the server
that wrote the export. A page layout's reference to a personal collection maps
to the collection the export's collections import matches; one to an admin
collection survives only on the server that wrote the export, while that
collection is there, and is otherwise left out. A PIN-protected profile reached without its PIN (an
API key is exempt from the gate's PIN check) cannot import. The import runs
synchronously: a profile's data is small, the size cap bounds it, and a single
transaction keeps it all-or-nothing without job bookkeeping.

## Uploads

An admin restores from another server's backup by uploading its restore
set: the bare `restore-set.tar` that a backup's download gives. Every
downloaded object saves under its stored name, so a folder of downloads is
a backup folder `silo backup restore --from` reads. The upload's file name
does not matter; the reader decides what the file is. An upload is then previewed and restored as a
backup is (`/admin/backups/uploads/{id}/restore-set` and `.../restore`),
and every selective-restore rule applies unchanged, the before-restore
backup included. A restore request names one source, `backup_id` or
`upload_id`, and the job's report and the published active operation carry
it.

`internal/uploads` keeps chunks on one server's disk, and requests are not
routed back to the server that holds them. A restore-set upload instead
writes every part straight to the destination, and keeps the session in
PostgreSQL, so any API server takes any part and any server completes it:

```
backups/uploads/<upload id>/<part>.<nonce>
```

Uploads go to the operational store, the backup destination. Parts are
stored as sent, so the parts read back in order are exactly the bytes the
client sent.

- `backup_uploads` holds each session: its size, part size, optional
  declared SHA-256, status (`uploading`, `checking`, `complete`,
  `rejected`), and once complete the frozen digest and what the set says
  about itself (source server id and name, Silo version, creation time,
  format version). `backup_upload_parts` names, for each received part, the
  object that counts.
- Before a byte of a part is stored, the send records an attempt
  (`backup_upload_attempts`) under the session row lock: one per part and at
  most four per upload. A second send of a part being written answers 409
  without writing anything, so concurrent sends cannot multiply the objects
  an upload holds. A part's storage write (its body read included) has a
  5-minute deadline, and cancellation stops it: the write's temporary file
  is closed, so its next write fails, and removed. An attempt is abandoned only after 10 minutes, once its writer has
  certainly stopped: another send may then replace it, and it can no longer
  record its object.
- Every part sent is a new object. Its row moves to it in a transaction that
  locks the session row, requires it to be `uploading`, and consumes the
  send's own recent attempt; the replaced object is deleted after commit, and
  a write the transaction refuses deletes its own object. A part sent again
  therefore replaces the first copy, a failed or abandoned attempt can only
  delete what it wrote, and once completion moved the session to `checking`
  no late write changes the parts it reads.
- Parts are 1–16 MiB (4 MiB by default), and every part but the last is
  exactly the part size. Parts may arrive in any order. A part's request sets
  its own connection read deadline of 2 minutes (the listener's 30-second
  read timeout would otherwise cut a slow part off) and answers 408 when the
  part does not arrive in time.

Limits: an upload is at most 2 GiB, and a server keeps three uploads
`uploading` or `checking` at once. On a local destination every open upload
reserves what it has not written yet plus four parts, and creation (under a
transaction advisory lock, so it holds across API servers) requires the
reservations of every open upload, the new one's included, plus the 1 GB
margin backups keep to be free. Every part checks the margin again before
it is written, since other writers share the disk.

**Completion freezes the digest.** It takes the subsystem lock (a busy lock
answers 409, and the parts stay for another try), moves the session to
`checking`, publishes the `upload` operation, and, with a 10-minute deadline
that closes the part being read when it passes (so a stalled read cannot
keep the lock), reads the parts in order
through `ReadRestoreSet`, the same checks a backup's restore set gets. The
SHA-256 of the bytes read becomes the upload's digest. These are the bytes
the client sent (the file as uploaded), so the digest is what a client
computes and what a declared SHA-256 is compared with: a preview returns it,
a restore request must carry it, and the restore job reads the parts again
and refuses bytes with another digest. A set that fails the reader is
`rejected` and its parts are deleted at once:

| Reason | When |
|---|---|
| `newer_silo` | A newer format version |
| `not_a_restore_set` | Anything else the reader refuses, with its error |
| `checksum_mismatch` | Other bytes than the SHA-256 declared at creation |

A failure to read the destination rejects nothing: the session returns to
`uploading`, and completing again checks it again. A session left
`checking` by a server that stopped is checked again by the next
completion, which holds the lock, so no other check can be running. The
validated set enters the preview cache under its digest, so the first
preview does not read it again.

A complete upload is kept 7 days after its last use (completion, a preview,
or a restore request), and never while a queued or running restore names it.
Deleting an unfinished
or rejected upload, which no restore can name, needs no lock; deleting a
complete one takes the subsystem lock (a running restore holds it) and
refuses while the queued restore job names the upload. Restores are queued
under the same lock, so none is queued between that check and the delete.

The sweep deletes, with their parts, sessions `uploading` or `rejected` for
24 hours, sessions left `checking` for 24 hours, and complete uploads unused
for 7 days. It deletes a `checking` or complete session only under the
subsystem lock: in a backup's rotation it already holds it; otherwise, when
such a session is due, it takes the lock without waiting and leaves them
all when it is busy, releasing it before deleting parts. Under the lock no
completion is reading the session, no restore is running from it or being
queued, and the one the queued restore names is kept. Deleting a session row is atomic against
a completion, which moves it to `checking` first, and a part write, which
requires the row.

The full sweep also lists the upload folders and deletes objects older than
an hour that no part row or attempt names: a replaced part a crash kept
from being deleted, a folder whose delete the store refused, or uploads a
full restore's database no longer knows. The objects are listed before the
rows are read, and a send can record its object only within 10 minutes of
its attempt, so an object found unnamed and an hour old can never be named
later. The quick sweep runs on every upload create and list; the full sweep
runs in every backup's rotation and, at most every 10 minutes per server, on
create and list, so it does not depend on nightly backups running.

## Full restore

Full restore is the offline `silo backup restore` command, never an API
operation. `SECRET_KEY` and `DATABASE_URL` come from the environment (or
`-env`); nothing comes from the database being replaced. `--from` is a backup
folder on this computer (the folder at the destination, or a downloaded copy
of its objects), read in place. `pg_restore` reads it, and nothing changes
until every check passed.

The first SIGINT or SIGTERM cancels a `silo backup` command, which stops
and cleans up before exiting, a swap's rollback included. A second signal exits at once. Once a swap's
journal exists, the first interrupt says that a second may leave the
databases closed and prints `silo backup restore --recover <time>` with the
swap's time.

A swap's cleanup (settling, rolling back, or dropping an unused restored
database) ignores the interrupt but has one 5-minute deadline for the whole
cleanup, so a statement that never returns cannot keep the databases closed
forever. When it passes, the command stops with the databases closed and
prints the journal and the `--recover` command.

Every mode first validates: the manifest is present and complete (exactly
`db.dump` and `blobs.tar`, and
`restore-set.tar` when it lists a restore set,
each with a size and SHA-256; exactly the core
inventory tables, each present with a count or absent; a kind matching the
id; a valid server id), `db.dump` and `blobs.tar` match their checksums,
`blobs.tar` holds the manifest's file count, every entry is a regular file
in its store's backup namespaces, and no two entries land on the same object
(stores that share one root count as one place), the backup's goose version
is not newer than this binary's newest migration, the backup applied no
migration this binary does not carry (`database.MigrationVersions`; the
refusal names the versions), and the key check value matches. All of it runs
before anything changes. Migrations run out of order, so a database can hold
a version below the binary's newest that the binary does not have, such as
one from another branch's build; the newest-version check alone would let it
through. A manifest written before `applied_migrations` existed is checked
by its goose version alone, and the restored database's applied versions are
checked against the binary in the verification step instead. A backup taken
with another key needs `--accept-lost-credentials`: its encrypted settings and credentials cannot be
read with this key.

### Separate mode

`silo backup restore --from <backup> --into <database URL> --blobs-into <dir>`
restores into a pre-created empty database and a separate directory laid out
as a local store root, changing nothing in the live deployment. It needs no
`CREATEDB` or rename privilege. After `pg_restore --exit-on-error
--no-owner --no-privileges`, it verifies the schema version, the applied
migrations, and the table inventory against the manifest. The target stays
open for the operator to point `DATABASE_URL` and the artwork storage
settings at. `--database-only` replaces `--blobs-into` and warns that uploaded
artwork, avatars, and branding will be missing. Separate mode clears no
Redis: the live deployment may still be running on it, and which Redis a
server on the restored database will use is not known here. The command
says to run `FLUSHDB` on that Redis database, or use an empty one, before
starting Silo on the restored database.

### Swap mode: never destroy first

`silo backup restore --from <dir>` replaces the live database and restores
blobs into the live stores. Before running it the operator stops Silo on
every server and turns off automatic restarts (Compose `restart`, Kubernetes
replicas set to 0), so nothing can write blobs. The command cannot prove that,
so it refuses while any other client is connected to the live database and
closes the database as an extra guard.

1. Validate (above).
2. Preflight on a control connection to the maintenance database
   (`postgres`): no other client on the live database, `CREATEDB` and
   ownership of it (or superuser), no database already using the two names
   the swap needs, and write access to the live stores, opened from the live
   configuration. Nothing has changed yet.
3. Create `silo_restore_<time>` (the live database's encoding and libc
   locale) and `pg_restore --exit-on-error` into it.
4. Verify it: schema version, applied migrations, and inventory match the
   manifest, no applied migration is one this binary does not carry, and the
   restored settings record the storage location the live stores are. Then
   close it to connections and end the command's sessions on it.
5. Quiesce the live database: `ALLOW_CONNECTIONS false`, then end any session
   that appeared since step 2. The journal starts just before this, in
   `silo-restore-<time>.journal` in the working directory (each record
   synced, and the directory synced once the file exists, so its name
   survives a host crash; if that cannot be done the swap stops before
   closing anything) and at
   `backups/<server-id>/<time>-overwritten-blobs/journal.json`.
   Its header records both databases' OIDs and the live storage
   configuration, credentials sealed with `SECRET_KEY`.
6. Blobs, unless `--database-only`: each key the backup holds is recorded in
   the journal before it changes; one that exists is first copied to
   `backups/<server-id>/<time>-overwritten-blobs/<key>` in its own store.
7. Rename the live database to `silo_before_restore_<time>`, rename the
   restored database to the live name, and reopen it, the only database
   reopened. Each rename is recorded as intended before it runs, and whether
   it took effect is read back from `pg_database` by OID, whatever the
   statement answered. If only the reopen fails, the swap is done; the command retries
   and then prints the one statement to run.
8. The old database stays closed and the overwritten-blobs folder stays. The
   command prints how to drop them once the restored server works, or how to
   go back.
9. Clear Redis, unless `--keep-redis`: `FLUSHDB` on the database the live
   deployment is configured with (`REDIS_URL` when set, otherwise the
   `redis.url` and `redis.db` settings read from the live database before
   step 2), never `FLUSHALL`, so no server serves sessions, caches, or
   coordination state from before the restore. A deployment without Redis
   skips this. The restore has finished by now, so a Redis that cannot be
   reached or cleared does not fail it: the command warns and prints the
   `redis-cli -n <db> FLUSHDB` to run before starting Silo.

A failure in step 6, or before the second rename completes, rolls back with
connections blocked throughout and the live database reopened last:

1. undo the first rename, so the original database has the live name again;
2. put preserved blobs back and delete keys the restore added;
3. drop the restored database;
4. reopen the original database.

If a rollback step fails, the command stops with the databases closed, keeps
the restored database, the old database, the preserved blobs, and the journal,
and prints exactly which steps remain. It never reports the deployment as
unchanged unless every rollback step succeeded.
**The databases, not the journal, say how far the swap got.** A crash can
land between a rename and its journal record, so the swap and its recovery
read both databases' current names by OID: original names (nothing renamed),
the live database under the before name (first rename only), the restored
database under the live name (swapped), or the live name back with the
restored database gone (rolled back). Any other state stops with everything
closed. Every rollback step checks that state first, so running it again
after a crash is safe. A torn last journal record (a crash mid-write) is cut
off before anything is appended; damage before the last record stops
recovery.

`silo backup restore --recover <time>` reads the local journal and finishes
the swap when the databases show the second rename landed, or the rollback
otherwise. It never opens either database: the control connection reads
`pg_database`, and the blob stores are reopened from the storage
configuration in the journal, its credentials unsealed with `SECRET_KEY`.
Because it opens no database it cannot read the Redis settings, so it
clears no Redis and reminds the operator to run `FLUSHDB` if the restore
went ahead.

### Restore drills

`silo backup verify --from <backup> --scratch <database URL>` runs the
validation, restores into an empty scratch database, runs the step 4
verification (not its connection closing). The validation reads
`blobs.tar` through. It does not need the right `SECRET_KEY`; with another
one it warns that encrypted values would be lost.
CI runs the same round trip against PostgreSQL
(`TestBackupRestoreRoundTripDB`): a seeded database with blobs and an
encrypted setting is backed up, restored in separate mode, and compared
table by table, blob by blob, and by the decrypted value.

## Health

There is no server-side dashboard warning registry; the dashboard derives its
notices on the client. `GET /api/v2/admin/backups` therefore returns the
conditions the dashboard shows as `warnings`: backups turned off, no
successful backup in the last 48 hours (`no_recent_backup`): the later of
the status row's `last_success_at` and the newest complete backup at the
destination, which also covers a status row a full restore left empty; the
destination missing or unreachable, the SQLite user store, and `pg_dump`
missing or older than the server. It also carries this server's key check
value. It also returns the active operation and the latest run from
[the status row](#status).
