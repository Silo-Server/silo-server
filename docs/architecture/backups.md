# Backups

`internal/backup` takes whole-server backups: a `pg_dump` of the database plus
the blobs that cannot be fetched again. It lists, rotates, and deletes them,
and the `silo backup` command restores one in full. The endpoints are in
[backups-api.md](../backups-api.md).

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
  blobs.tar        assets/<key> and operational/<key> entries
  manifest.json    written last
```

- `server-id` is `server.identity_id` (`internal/serveridentity`). Every server
  writes and rotates only its own prefix; other prefixes at the destination
  are listed by id and otherwise left alone.
- The folder name is the backup id: `20261031T040000.123Z-nightly`. The time
  is the start of the run in UTC, to the millisecond; ids written to the
  second (`20261031T040000Z-nightly`) still parse. Kinds are `nightly`,
  and `manual`. Names that do not parse,
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
  object are both older than 24 hours, and only a folder a complete,
  error-free listing showed without a manifest object (`folder.noManifest`):
  deleting a complete backup is the one mistake it must not make. A folder
  whose manifest is listed but could not be read (a timeout, a destination
  error) is `unreadable`: listed with that status and the
  `backup_unreadable` warning, never rotated, not counted toward
  `backup.keep`, and never swept. A failed listing skips rotation and the
  sweep for that run.

### Manifest

`manifest.json` has `format: "silo-backup"` and `version: 1`. Readers accept
every version up to theirs and refuse a newer one. It records the id, kind,
server id, start and completion times, Silo version, goose version (the
highest applied `goose_db_version`), `applied_migrations` (every applied
goose version, ascending, without goose's version 0; it must end at the goose
version), PostgreSQL server version, `pg_dump`
version, the tables whose data was excluded, each object's size and the
SHA-256 of its stored bytes, the `SECRET_KEY` check value, how the table of contents was checked, the blob count and bytes,
`missing_blobs`, and the core table inventory. The objects are exactly
`db.dump` and `blobs.tar`. A manifest is at most 16 MiB, the most a reader
reads: `missing_blobs` lists at most 10,000 entries, with
`missing_blobs_total` counting them all when there are more, and a manifest
that would still be larger is not written.

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

`mac` authenticates the rest of the manifest: HMAC-SHA256 under a key
derived from `SECRET_KEY` (HKDF-SHA256, info `silo/backup-manifest/v1`,
`secret.BackupManifestKey`) of the manifest encoded with `mac` empty, in hex.
It covers the id, kind, server id, times, every object's name, size, and
checksum, and every other field. Anyone who can write the destination can
change an object and its checksum together; only a server with the key can
write a `mac` that verifies. Every read checks it before trusting anything
else in the manifest: the listing, downloads and download links, a
selective restore's preview and restore, `silo backup restore`, and
`silo backup verify`. The check is a strict state machine, with no legacy
path (every Silo that writes manifests signs them):

- The manifest names this server's check value: its `mac` must verify, or
  it is refused (`ErrManifestTampered`), a missing `mac` included, even with
  `--accept-lost-credentials`.
- It names another check value, none, or `SECRET_KEY` is not set here: it
  cannot be authenticated. `silo backup restore` and `silo backup verify`
  refuse it unless the operator passes `--accept-lost-credentials`, the flag
  that already accepts a backup taken with another key (whose encrypted
  settings are lost), and say plainly that the backup cannot be verified.
  Its `db.dump` could have been changed by anyone who could write to the
  backup's storage, and `pg_restore` runs what a crafted archive says with
  the connecting role's rights (`--no-owner --no-privileges` drop ownership
  and grants, not arbitrary SQL in other entries). So before loading such a
  dump, every mode (verify's scratch database, `--into`, and a replace's
  `DATABASE_URL` role) checks the role and refuses a superuser unless the
  operator also passes `--accept-superuser` (`checkLoadRole`). A selective restore needs the admin's confirmation
  (`accept_unauthenticated`), and the listing shows it as `unverified`.
- A manifest a destination read must also name the folder and server it was
  read from. A folder whose manifest names this key and does not verify is
  listed as `unverified` with no details, cannot be restored or downloaded,
  and the sweep leaves it.

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
   `policy_decisions`, `catalog_search_index_events`, the short-lived
   secrets of [Credentials](#credentials) (login sessions and refresh
   tokens, Jellyfin and Audiobookshelf sessions, device-code, OIDC,
   account-link, password-reset, email-verification, Discord-link,
   watch-provider, and history-import sign-in flows, Watch Together rooms),
   `backup_status` (its row describes the backup being taken), and, unless
   `backup.include_intro_fingerprints`, `media_intro_fingerprints`. The schema
   is always dumped. Leaving sign-in state out means a restore never brings
   back a session or token revoked after the backup: after a full restore
   every client, Jellyfin-protocol ones included, signs in again. Long-lived
   credentials stay in the dump, and a restore replaces them (see
   [Credentials](#credentials)). No secret from the URL appears in a
   process listing: the password moves into `PGPASSWORD`, and an
   `sslpassword` (the client key's passphrase, which libpq reads from no
   environment variable) into a service file readable only by this user,
   named by `PGSERVICEFILE` and removed when the command exits. pgx's
   `sslinline`, which puts key material in the URL, is refused.
4. Stream `pg_dump`'s stdout straight into the destination with no local
   spool, hashing the stored bytes. `pg_restore --list` reads a tee of the
   stream. `pg_restore` reads a custom-format archive's table of contents
   from a pipe and exits without reading the data; the tee then drops its copy
   and the upload continues. If the piped check fails, the check reads the
   stored object back. The manifest records which happened (`pipe` or
   `read_back`).
5. Close the snapshot transaction and copy the blobs into `blobs.tar`.
6. Write the manifest, then rotate and sweep. A failure in any earlier step
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
| Card overlay images (`overlay_images.ref` under `overlays.ImageKeyPrefix`) | assets |
| Uploaded profile avatars (`user_profiles.avatar` starting `upload:`) | operational |
| Uploaded subtitles (`downloaded_subtitles.s3_key` with provider `upload`), always: they cannot be fetched again | assets |
| Downloaded subtitles (`downloaded_subtitles.s3_key` from a provider), unless `backup.include_subtitles` is off | assets |

A backup taken with `backup.include_subtitles` off still dumps every
`downloaded_subtitles` row. A full restore deletes the provider rows whose
object is not in the storage the restored server uses (in separate mode,
all of them), so players do not list tracks with no content; Silo downloads
them again when they are chosen. Storing a subtitle whose row exists but
whose object is gone writes the object back under the row.

For an artwork original (`.../original[.<revision>].<ext>`) the copy also
takes the stored variants of the same revision in its directory. Only the
referenced key itself is required.

A backup carries only keys in these namespaces: `local/` (uploaded item
artwork is cached as library artwork), `collection-images/`,
`user-collection-images/`, `library-posters/`, `branding/`, `subtitles/`,
and `overlay-images/` in the assets store, and `profile-avatars/` in the operational store. A
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
backups; the Backups page says so and links to Storage settings.

A storage transition never copies `backups/` (see
[blob-storage.md](blob-storage.md#managed-transitions)). Local backups stay
on this server's disk when the operational store moves to a bucket; the
Backups page then lists the bucket's backups only, and an earlier local one
is still restored from its folder with `silo backup restore --from <dir>`.

**Every API server must share the operational store.** Backups are written by
whichever server runs the task and listed, downloaded, and deleted by any
other. On a local artwork backend that is the shared-volume requirement
artwork already has in a multi-server deployment
([blob-storage.md](blob-storage.md)).

Local backups hold the same data as the database, password hashes included.
The local destination writes them readable by the server's account only:
`backups/` and every folder under it are 0700, each object 0600, where the
store's own modes are 0755 and 0644. `backups/` is made private before an
object is written, so a file is never readable by another account while it
is written. Downloads are
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
on a pool connection would deadlock a pool of one. Every operation that
writes or deletes backups holds it: the nightly and manual backup, rotation,
deletion, and the sweep.

- Rotation and the sweep run inside the backup that holds the lock.
- "Back up now" takes the lock in the request and hands it to the task run it
  starts, so there is no window between the check and the backup. It and
  "Delete" answer 409 when the lock is held.
- A scheduled run that finds the lock held records
  "skipped: another backup or restore is running". Every API server fires the
  same daily trigger; one backs up and the others record the skip. A
  scheduled run that takes the lock after another server already backed up
  its slot skips too ("skipped: tonight's backup already ran on another
  server"): a successful nightly records its slot, the latest `backup.time`
  at or before it started in its server's local time zone, in
  `backup_status.last_nightly_slot`, and a scheduled run skips when the
  recorded slot is later than its own previous slot (the same wall-clock
  time a calendar day earlier, so each night across a daylight saving
  change backs up once). Servers in different time zones, or a
  `backup.time` moved earlier, can cost one night's run, never a second
  copy. A failed nightly records no slot; manual and
  before-upgrade backups neither record nor check one.
- A held lock's session is checked every 10 seconds. If it ends, the work
  holding the lock is canceled (`pg_dump`), and the
  incomplete folder it leaves is the sweep's.
- Whether the lock is held is read from `pg_locks`, never by taking it: a read
  that took the lock even briefly could make a nightly run skip.

### Status

The lock holder publishes what it is doing in the one-row `backup_status`
table, so any API server can report a run another server started: the active
operation (`backup` or `delete`), its backup id, start time, progress and
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
- The newest clean `nightly` or `manual` copy is never deleted, even when
  that keeps one more than `backup.keep`, so repeated runs with missing blobs
  cannot rotate away the last copy that has every blob. A clean copy is
  complete, has every blob, and verifies under this server's `SECRET_KEY`:
  one this server can restore without extra flags. An unverified copy never
  stands in for it. The listing sends `clean` per backup, and the page
  predicts rotation from that instead of deciding it again.
- Rotation manages only the current server id's prefix.
- Deleting a backup takes the same lock.
- A delete that fails leaves a warning in the task result.

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
`db.dump` and `blobs.tar`, each with a size and SHA-256; exactly the core
inventory tables, each present with a count or absent; a kind matching the
id; a valid server id), `db.dump` and `blobs.tar` match their checksums,
`blobs.tar` holds the manifest's file count (each header checked before
`tar.Reader` expands it, `tarGuard`: only regular files and PAX records of at
most 8 KiB holding only the keys the writer emits (`path`, `size`, `mtime`),
each once, so no sparse map or long-name record is expanded, and every entry
`tar.Reader` returns must have the size the guard let its data run for), every entry is a regular file
in its store's backup namespaces, and no two entries land on the same object
(stores that share one root count as one place), the backup's goose version
is not newer than this binary's newest migration, the backup applied no
migration this binary does not carry (`database.MigrationVersions`; the
refusal names the versions), and the key check value matches. All of it runs
before anything changes. The checksums are taken of private copies: after
the manifest's MAC, `db.dump` and `blobs.tar` are copied into a new 0700
directory (`TMPDIR`, as 0600 files) and hashed as they are written, after a
check that their size plus 1 GB is free there. `pg_restore`, the archive
checks, and the blob writes read only those copies, so a backup folder
changed after the check changes nothing. The command removes them when it
ends, failed or not. Migrations run out of order, so a database can hold
a version below the binary's newest that the binary does not have, such as
one from another branch's build; the newest-version check alone would let it
through. A manifest written before `applied_migrations` existed is checked
by its goose version alone, and the restored database's applied versions are
checked against the binary in the verification step instead. A backup taken
with another key needs `--accept-lost-credentials`: its encrypted settings and credentials cannot be
read with this key. Silo refuses to start on a setting it cannot decrypt, so
after verification such a restore deletes, in the restored database, every
encrypted `server_settings` row this key cannot open and prints their key
names (never values) for the admin to enter again. Rows are only deleted,
never renamed: a value is bound to its key name.

### Connections to outside services

Where this server connects outside and how it signs in there never comes
from the backup on its own. `outboundBundles` (`internal/backup/settings_outbound.go`)
groups those settings into units: each endpoint and secret route of
`settingRoutes` (SMTP, AI providers, speech, subtitle AI, embeddings), the
AI provider list, the ChatGPT credentials, the push relay, the search
engine's address and key, the Discord application, and the API keys and
client secrets of metadata, request, and watch-sync services. On a full
restore of this deployment's backup each unit the live server has is taken
from it whole, so an SMTP host and password changed since the backup stay
changed. A unit the live server lacks, and every unit of another
deployment's backup or a separate restore, keeps the backup's other values
with its secrets cleared and its switch off, and the command lists it for
re-entry. `TestEveryCredentialSettingIsDecided` fails on an encrypted
setting that is neither in a unit nor this server's own (signing keys,
storage, Redis, the web push key pair).

A full restore also keeps every server-specific setting (`SettingKindServerSpecific`:
the server's identity and public URL, storage, Redis, search, AI
providers, the push relay, and the rest of `serverSpecificSettings`) as the
live server has it, deleting one the live server lacks so its default
applies, whichever deployment the backup came from: they describe this
server, as a selective restore never writes them
(`CarryServerSpecificSettings`). The signing keys a restore rotates keep
their new values. `TestServerSpecificSettingsComeFromTheLiveServerDB`
checks every classified one.

### Interrupted work

Nothing the backup had queued or running runs by itself after a full
restore (either mode). In the restored database, before Silo starts on it,
`InterruptWork` cancels admin jobs of every kind (backups, restores,
imports, storage transitions, scans, exports), history import runs, and scan
runs that were queued or running; fails running collection syncs and pending
or retrying deliveries to notification destinations (push, webhooks, browser
push); and clears storage transition checkpoints and cursors, all with the
reason "interrupted by restore". Finished rows stay as history. The storage
transition target (`storage.transition.target`) is the live server's, or
none for separate mode or when the live server has none, so a transition
canceled since the backup stays canceled and a staged target is never
revived. Leases and claims in other queues (match queues, enrichment and
trickplay state) are local work a server reclaims when they expire.

Stores kept outside Postgres whose bookkeeping is in it are rebuilt from the
restored data: the search index state is deleted, so the indexer rebuilds
the search engine's index from the restored catalog when Silo starts (its
change events are not in the dump), and every taste profile is marked
stale, so recommendations are rebuilt with the live server's embedding
settings. Files the catalog points at (artwork, trickplay, chapter images)
are restored with it. A selective restore needs neither: catalog writes
queue search index events through triggers, as every other write does.

### Accounts roll back

A full restore is a rollback of the accounts, by maintainer decision: every
account, role, and password goes back to the backup's, an admin demoted or a
password changed since included. Keeping today's accounts beside the backup's
data would restore neither state, and the backup is the state the operator
chose. Only what `secretTables` carries and the stricter switches
(`stricterFlags`) stay the live server's. Before the swap the restore says
so: "Accounts, roles, and passwords go back to <backup time>", with advice to
review the admin accounts and reset the passwords that changed since. On the
deployment the backup came from it prints this only when an account changed
since the backup, and lists by name (up to 20 per line, then a count) the
accounts whose role, owner, or break-glass flag changes, whose password
changes, that were created since and are removed, and that were deleted since
and come back (`AccountRollback`). Another deployment's backup always gets
the notice, without the lists: its account ids are other people.

### Credentials

`secretTables` in `internal/backup/credentials.go` classifies every table
that holds a bearer secret or a public capability, each one way:

- Left out: short-lived secrets and capabilities (sessions, sign-in flows,
  reset and verification tokens, Watch Together join codes, Audiobookshelf
  playback sessions, whose id alone opens the public track route). The dump leaves their
  rows out, so a restore never brings back a session or token revoked
  after the backup, and after a full restore every client signs in again.
- Carried from the live server: long-lived credentials that clients and
  other services present to Silo (API keys, pending invitations, invite
  codes, autoscan webhook tokens, audiobook RSS feed links, Plex and
  Jellyfin webhook sync secrets with their profile mappings, external
  sign-in links and sign-in providers), and the
  destinations that receive notifications without a session (browser and
  mobile push registrations, notification webhooks, notification email
  addresses, Discord links, server notification channels): one removed
  since the backup would receive them again. What this server holds for
  an outside account is carried the same way: watch-provider connections
  with their tokens (one removed since would sync again), and plugins'
  configuration and stored state, which hold the keys and tokens a plugin
  presents to its service and which the server cannot tell apart from a
  plugin's other settings. A restore
  never brings back the backup's rows: one revoked after the backup would
  work again, and one created since would stop working. They stay in the
  dump (`marker_edit_audit` refers to API keys), and the restore replaces
  them as below.
- What the server runs or routes clients through is the live server's:
  stream nodes and the plugin catalogs it trusts (`plugin_repositories`)
  are carried like the credentials above (a node or catalog removed since
  the backup stays removed), and each plugin installation the live
  server has, by id and plugin, takes the live release (version, install
  path, repository, stored archive, and capabilities;
  `CarryPluginReleases`), so a release replaced since the backup is never
  launched again; a catalog it names that the restored data lacks is
  dropped from the reference rather than failing the restore. An
  installation whose id is another plugin here, and
  every installation of another deployment's backup or a separate
  restore, comes back off and is listed; one only the backup has is off
  by its stricter switch. The trusted proxies are a stricter-of setting.
- Custom access policies only tighten decisions, so a newer one lost to
  the backup would widen access: their documents and versions
  (`liveTables`) are taken from the live server as one unit, with the
  version each runs (`CarryPolicies`); when the live server has none, and
  for another deployment's backup or a separate restore, the backup's come
  back turned off and are listed. The rule across these: what governs
  access decisions, what the server runs, and what it routes clients
  through is the live server's or the stricter of the two, never the
  backup's alone. Access group definitions are the one exception, by the
  same decision as accounts (see Accounts roll back): their switches are
  kept at the stricter value (`stricterFlags`) and grants made from them
  are compared, but their limits roll back with the accounts.
- Restored with live credentials (`secretScrubbed`): connections to outside
  services that other restored data refers to (history import sources,
  subtitle provider accounts, autoscan targets, request integrations). The
  backup's rows come back, but each takes its address and credentials from
  the live row with its key, so neither the backup's credentials reach a
  service again nor the live ones the backup's address; a row the live
  server does not have (deleted since, or any row of another deployment's
  backup) comes back with its credentials cleared and turned off, and the
  command lists it.
- Restored with the data: what looks like a secret admits no one to this
  server or belongs to the record it comes with (URL slugs, worker leases,
  delivery histories, account passwords and profile PINs with their
  accounts). No table that holds what this server presents to an outside
  service is restored as the backup has it.

Every other table is classified too (`internal/backup/tables.go`): the
logs and queues the dump leaves out (`loggedTables`), the server's data
(`restoredDataTables`), and the running server's own state, restored as it
is and rebuilt or reconciled by the server (`serverStateTables`: caches,
queues, job, lease, and lock state, revisions, samples).
`TestEveryTableIsClassifiedDB` reads every table after the migrations and
fails on one in no list or in two, and on a listed name that is not a table,
so a new table needs a decision before it ships; a layer that adds one
classifies it.

Sensitive settings (`catalog.SensitiveSettingKeys`) are classified the same
way in `internal/backup/settings_secrets.go`. The signing keys Silo
generates for the tokens it issues (`auth.jwt_secret`, which signs sessions,
download and artwork links, backup object links, OAuth state, and cursors,
and `audiobooks.abs.jwt_secret`) are replaced on every restore, swap or
separate, before the lost-settings pass: a fresh random value written
encrypted with this server's `SECRET_KEY`, bound to its key name as the
settings store writes it, or, without a key, deleted so the server generates
one when it starts. A key leaked and rotated after the backup therefore
never signs again; every session ends with a restore anyway. The other
sensitive settings are credentials for other services and are restored.
`TestSensitiveSettingsAreClassified` fails on a sensitive setting in
neither list.

- A full restore (swap mode) keeps the live server's carried credentials as
  they are right before the swap. After verification, in the same step
  that clears unreadable settings, it reads them from the live database
  and writes them into the restored database in one transaction,
  before it becomes live. A restored credential identical to a live one
  (same id, owner, and secret) is updated in place, so what refers to it
  stays linked; the others are deleted. Accepted and revoked invitations
  are the restored history and stay. A live credential whose account,
  profile, access group, item, autoscan source, or connection is not in the
  restored data is dropped, and so is one whose account (owner, inviter, or
  creator) has more authority in the restored data than on the live server:
  a higher role (the Owner over admin over user), enabled where it is
  disabled now, break-glass access, an effective account policy wider in
  any field (`access.WiderThan`: libraries, quality, stream and transcode
  caps, profiles, downloads, requests, permissions, with access groups
  applied), a household manager (a primary profile) without a PIN where
  every live one has a PIN, a profile only the backup has (deleted
  since, which the credential's holder could select), or, for a profile
  both sides have, a
  protection or restriction lifted or loosened (its PIN, becoming the
  primary profile, child profile, rating or advisory-age ceiling, library
  restriction, playback-quality ceiling), or a looser request policy
  (requests at all, without limit, more titles or over a shorter window,
  or with automatic approval, as `requests.LayeredPolicy` resolves it from
  the server's, the access group's, and the account's limits). The PIN
  itself rolls back with the account, as passwords do; a live credential
  never attaches to the weaker protection. Changing an account's role
  deletes its keys for the same reason.
  `TestAuthorityComparisonCoversEveryField` fails on a field of these
  policies, or of a stored profile, that the comparison does not cover
  or name as no authority. An invitation or invite code is also dropped when the account it
  would create gets a wider effective policy under the restored data than
  live: its role and libraries applied to its access group, or the default
  group when it names none, as account creation resolves them, and the
  request policy that group gives a new account. A sign-in
  provider that creates accounts on first sign-in (`auto_provision`) is
  compared the same way, for the user it would create in the default group;
  where that is wider it is carried with account creation off, so people
  already linked still sign in, and listed. These are every way a new
  account appears without an admin: an invite-code signup also needs
  `signup.enabled`, kept at the stricter of the two servers' values. The
  command counts what it kept per kind and lists what it dropped or kept
  with less by id and label, never key material. Identity sequences move past the ids written. Live rows are
  encrypted with this server's key, so they stay readable after
  `--accept-lost-credentials`.
- Carrying from the live server is for the deployment the backup came from:
  credentials, destinations, sign-in links, and switches are tied to its
  account, profile, and installation ids, which on another deployment are
  other people. When the authenticated manifest's server id is not the live
  server's (or the manifest cannot be verified), the restore says so before
  anything changes and needs `--accept-lost-credentials`; it then carries
  none of them, deleting the backup's own, so everyone signs in again and
  admins create keys, invitations, and links again
  (`ErrOtherDeployment`, `checkDeployment`). Stricter-of server settings,
  which are not tied to ids, still apply.
- In the same transaction, a full restore keeps each security, access,
  sharing, or outbound switch inside an otherwise restored table at the
  stricter of the backup's and the live server's value (`stricterFlags`),
  so it never turns back on what an admin or a user turned off since the
  backup: a library's `enabled`; autoscan's and each autoscan source's;
  history-import sources, request integrations, routes, and the request
  settings (requests, auto-approval, watchlist requests); subtitle
  providers; marker sharing; per-account and per-group download,
  transcode, and request permissions; a profile's child flag, library
  restriction, and advisory-age requirement (on wins); a personal
  collection's sharing; a profile's notification preferences; a
  watch-provider connection's sync switches; an account's `enabled`,
  `break_glass`, and `local_password_login_enabled` (off wins) and
  `password_change_required` (on wins), and the `enabled` of every plugin
  installation, plugin task binding, and plugin catalog (off wins): plugins run as the server, so one
  turned off during an incident stays off, and a sign-in provider's carried
  links then sign no one in. A plugin installation or catalog the backup
  has and the live server no longer does comes back off, never on, and so
  does every other row with an off-wins switch that was deleted since the
  backup: a library (disabled, for an admin to turn on deliberately: an
  account with every library would otherwise see it at once), a personal
  collection (unshared), an autoscan source, a history import source, a
  request integration or route, a subtitle or marker provider's sharing, a
  plugin task binding, an access group's permissions, a profile's
  notifications. Only account switches roll back as they were
  (`rollsBack`), since accounts roll back whole. Nothing
  a full restore does turns a plugin on; the admin does. These switches are
  set before the credential carry compares authority, so it compares the
  restored server as it will start; the carry also counts an account with
  every library as seeing each enabled library of its server, and drops a
  credential whose account would see a library it cannot see now. A row is the live
  one when its id and username (an account), plugin id, catalog URL, or
  installation and capability (a task binding) match. The command
  lists each switch it kept. Grants through policy rather than a switch
  (`users.role`, `is_owner`, access group and per-account permissions) are
  restored as the backup has them; the credential carry above refuses
  credentials whose account gains authority that way. Every other boolean
  column of a restored table is in `restoredFlagColumns` with why the
  backup's value is safe, and `TestEveryTableIsClassifiedDB` fails on one in
  neither list.
- Every server setting has exactly one kind (`SettingKindOf`,
  `settings_classes.go`), which both restores read: restored as the backup
  has it, server-specific (never written by a selective restore), stricter
  of backup and live (`accessSettings`), or part of a service route
  (`settingRoutes`: an address and the secrets sent there, restored
  together). `TestEverySettingIsClassified` enumerates the registries (admin
  defaults, sensitive settings, branding assets, rate limits, and the keys
  the server writes for itself) and fails on a setting with no kind or an
  entry that names none.
- It also keeps the live value of each stricter setting the backup would
  widen (`accessSettings` in `settings_access.go`): who may sign in and how (local password
  sign-in, email auto-match, sign-up, self-service password reset, the
  Jellyfin and Audiobookshelf sign-in surfaces), session, token, and
  provider re-check lifetimes and the provider outage policy, what a
  rating-capped profile sees (`access.unrated_content`), private-network
  destinations, trusted proxies (a list that trusts any network the live
  one does not is wider), mail transport security, and the sign-in rate
  limits (`ratelimit.enabled`, `ratelimit.auth.*`, `ratelimit.ip.*`), and
  the delivery switches (`email.enabled` and the email, Web Push, webhook,
  Apple and Android push, Discord, server-channel, and fan-out notification
  switches), so a channel an admin turned off stays off while its live
  destinations are carried, the release-event pause and in-app
  notifications, downloads, the AI, transcription, recommendation, and
  semantic-search providers that receive library data, marker sharing and
  online marker lookups, and LAN discovery. Every other switch setting is in
  `restoredFlagSettings` with its reason (`TestEverySwitchSettingIsDecided`).
  The
  live row is copied as stored, so an encrypted value stays bound to its
  key name; values are compared decrypted, and one that does not decrypt
  counts as wider. The command lists each setting it kept.
- Separate mode (`--into`) restores a copy, not the live server: it deletes
  the backup's carried credentials and carries none.

### Separate mode

`silo backup restore --from <backup> --into <database URL> --blobs-into <dir>`
restores into a pre-created empty database and a separate directory laid out
as a local store root, changing nothing in the live deployment. Before
anything is written it refuses a target that would overwrite something,
since a copy keeps nothing it replaces: `--into` must not be the live
database (`DATABASE_URL`, compared by host, port, and database as pgx
resolves them), and `--blobs-into` must be missing or an empty directory
and not the live server's local file storage (read from its settings when
`DATABASE_URL` is set; symbolic links resolved). It needs no
`CREATEDB` or rename privilege. After `pg_restore --exit-on-error
--no-owner --no-privileges`, it verifies the schema version, the applied
migrations, and the table inventory against the manifest. It then deletes
the recorded storage identities (`artwork.storage_identity`,
`storage.operational_identity`) from the target: they name the backed-up
server's stores, so a server started on the copy would refuse the stores it
is configured with. That server records its own on its first start or write. The copy
gets none of the backup's long-lived credentials
([Credentials](#credentials)).
The target stays open for the operator to point `DATABASE_URL` and the
artwork storage settings at. `--database-only` replaces `--blobs-into` and warns that uploaded
artwork, avatars, and branding will be missing. Separate mode clears no
Redis: the live deployment may still be running on it, and which Redis a
server on the restored database will use is not known here. The command
says to run `FLUSHDB` on that Redis database, or use an empty one, before
starting Silo on the restored database.

A database URL given on the command line (`--into`, `silo backup verify
--scratch`) must not carry a password, an `sslpassword`, or inline key
material: any account on the host can read a running command's arguments.
The command refuses one and points to `PGPASSWORD` or a password file
(`~/.pgpass`, `PGPASSFILE`), and to a service file (`PGSERVICEFILE`) for an
`sslpassword`, which both the command and `pg_restore` read.

A swap creates the restored database from `template0`, so `pg_restore`
creates the backup's extensions in it as the `DATABASE_URL` role. Preflight
reads them from the dump's table of contents and refuses, before anything
changes, one this PostgreSQL server does not have, or one only a superuser
may create (`vector` is not trusted) when the role is not a superuser
(`ErrExtensionNeedsSuperuser`), pointing to a superuser role or separate
mode into a database where a superuser created them.

The control connection uses the first of `postgres` and `template1` that is
not the live database and that the role can connect to (a live database
named `postgres` gets `template1`); a swap renames the live database, which
PostgreSQL refuses while a session is on it. With neither, the command
refuses before anything changes.

A swap reaches the maintenance database and the databases it creates through
URLs derived from `DATABASE_URL` with only the database changed. The
database is replaced wherever the URL can name it: the path and a `dbname`
query parameter (which overrides the path in libpq and pgx), or every
`dbname` of a key=value string. The live database's name is read the same
way. A URL using a connection service (`service`, `servicefile`) is refused,
since the service file can name the database; host and port overrides are
kept, because they pick the server, which stays the same.

### Swap mode: never destroy first

`silo backup restore --from <dir>` replaces the live database and restores
blobs into the live stores. Before running it the operator stops Silo on
every server and turns off automatic restarts (Compose `restart`, Kubernetes
replicas set to 0), so nothing can write blobs. The command cannot prove that,
so it refuses while any other client is connected to the live database and
closes the database as an extra guard.

Before anything else, the restore takes the full-restore lock: a session
advisory lock on its control connection (`PGControl.LockRestore`, keys
`"SRST"` and the live database's name hashed, apart from the subsystem lock),
held until the command returns or its connection drops. A second restore or
`--recover` of the same database fails at once with `ErrRestoreRunning`,
naming the holder's process: each control connection is on the maintenance
database, so neither preflight would see the other.

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
   replace its credentials with the live ones ([Credentials](#credentials)),
   close it to connections, and end the command's sessions on it.
5. Quiesce the live database: `ALLOW_CONNECTIONS false`. The carried
   credentials are the live ones only while no other client is on the live
   database, so a client that connected since step 2 stops the restore
   (`ErrLiveInUse`), both just before the credentials are read and here,
   once the database is closed; its session is never ended, which would lose
   what it changed. The live database is reopened and left unchanged. The journal starts just before this, in
   `silo-restore-<time>.journal` in the working directory (each record
   synced, and the directory synced once the file exists, so its name
   survives a host crash; if that cannot be done the swap stops before
   closing anything) and at
   `backups/<server-id>/<time>-overwritten-blobs/journal.json`.
   Its header records both databases' OIDs and the live storage
   configuration, credentials sealed with `SECRET_KEY`. Every line carries
   an HMAC-SHA256, under a key derived from `SECRET_KEY`
   (`secret.RestoreJournalKey`, `silo/restore-journal/v1`), of the previous
   line's MAC and its record, so the chain covers the header, every step,
   and every blob, in order. `--recover` reads only a regular file of the
   user running it, opened without following links, in a directory no other
   user can write to unless it is sticky; refuses any line that does not
   verify (only a torn last line, from a crash mid-write, is cut off); and
   refuses a journal whose live database is not the one `DATABASE_URL`
   names, before it acts on either database. A journal from a build that
   wrote no MACs is refused with how to recover by hand.
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
9. Clear Redis, unless `--keep-redis`: `FLUSHDB` on the database the
   restored server will use (`REDIS_URL` when set, otherwise the
   `redis.url` and `redis.db` settings read from the restored database now
   live), never `FLUSHALL`, so no server serves sessions, caches, or
   coordination state from before the restore. A deployment without Redis
   skips this. The restore has finished by now, so a Redis that cannot be
   reached or cleared does not fail it: the command warns and prints the
   `redis-cli -n <db> FLUSHDB` to run before starting Silo. When the
   restored settings cannot be read, it clears nothing, since the Redis the
   database named before the swap may not be the restored server's, and
   warns the operator to clear the one Silo uses.

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
`blobs.tar` through. It does not need the right `SECRET_KEY`, but a backup
this key cannot authenticate needs `--accept-lost-credentials`, as for a
restore. Connect `--scratch` and `--into` as a role that is not a superuser
and owns only that database (`CREATE ROLE silo_drill LOGIN; CREATE DATABASE
silo_drill OWNER silo_drill`, then, as a superuser in that database, `CREATE
EXTENSION vector`, which only a superuser may create): a dump loaded there
can then reach nothing else on the server. `pg_restore` loads the dump's
table of contents less the comments on extensions (`--use-list`), which only
an extension's owner may write and nothing reads, so such a role can load
it (`TestBackupRestoreRoundTripDB` verifies as one).
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
destination whose manifest this server authenticated (anyone who can write
the destination could set an unverified one's completion time in the
future), which also covers a status row a full restore left empty; the
destination missing or unreachable, the SQLite user store, and `pg_dump`
missing or older than the server. It also carries this server's key check
value. It also returns the active operation and the latest run from
[the status row](#status).
