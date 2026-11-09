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
  db.dump[.age]          pg_dump custom format, zstd level 3
  restore-set.tar[.age]  sections for selective restore; not in before-upgrade backups
  blobs.tar[.age]        assets/<key> and operational/<key> entries; not in before-upgrade backups
  manifest.json          written last; never encrypted
```

Objects end in `.age` when they are encrypted, which every backup in an
S3 bucket is: the `s3` destination, and the `default` destination when it is
the private bucket ([Destinations](#destinations), [Encryption](#encryption)).

- `server-id` is `server.identity_id` (`internal/serveridentity`). Every server
  writes and rotates only its own prefix; other prefixes at the destination
  are listed by id and otherwise left alone. `backups/uploads/` holds
  [uploaded restore sets](#uploads) and is never a server id.
- The folder name is the backup id: `20261031T040000.123Z-nightly`. The time
  is the start of the run in UTC, to the millisecond; ids written to the
  second (`20261031T040000Z-nightly`) still parse. Kinds are `nightly`,
  `manual`, `before-restore`, and `before-upgrade`. Names that do not parse,
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
  otherwise. So is a folder whose manifest lists an object that is not
  there, or not at its recorded size, in a complete listing of the folder:
  it keeps its manifest, so the sweep leaves it for the admin. The sweep
  deletes incomplete folders whose name and newest object are both older
  than 24 hours, and only a folder a complete, error-free listing showed
  without a manifest object (`folder.noManifest`): deleting a complete
  backup is the one mistake it must not make. A folder whose manifest is
  listed but could not be read (a timeout, a destination error), or that
  holds more objects than a listing reads of it (below), is `unreadable`:
  listed with that status and the `backup_unreadable` warning, never
  rotated, not counted toward `backup.keep`, and never swept. A failed or
  capped listing skips rotation and the sweep for that run.
- **Listings are bounded.** Anyone who can write a bucket, or answer for its
  endpoint, could otherwise hold enough keys, or page without end, to
  exhaust the server's memory on every scheduled backup. Every listing pages
  through `Destination.ListPage` (at most 1,000 objects a request) and stops
  at a cap (`listing_bounded.go`): one server's backup folders are read in
  key order, at most 2,000 folders and 1,000 requests (past either the
  listing fails, so nothing is rotated or swept), and a child that is not a
  backup folder (a full restore's overwritten files) is skipped by listing
  after it, as a delimiter would. A folder with more than 32 objects, which
  no backup writes, is read only that far and is `unreadable`: its contents
  are unknown. One listing reads at most 256 MiB of manifests
  (`maxListedManifestBytes`, judged by their listed sizes before any is
  read); past it the remaining folders are `unreadable` and kept. Other
  servers' folders are named one request each, at most 100, and the
  upload sweep reads at most 100,000 objects.
- **The listing reads a manifest once.** A manifest is not rewritten after
  its backup completes, so the Backups listing (which the page polls every
  few seconds while a backup runs) keeps each manifest it read and checked,
  keyed by destination and location, and reads it again only when the
  listing reports another size, modification time, or ETag. A failed read,
  including a manifest that names this server's key and does not verify, is
  not kept, entries of folders no longer listed
  are dropped, and the cache holds at most 32 MiB of manifests. Rotation, the
  sweep, and a backup's own checks read every manifest.

### Manifest

`manifest.json` has `format: "silo-backup"` and `version: 1`. Readers accept
every version up to theirs and refuse a newer one. It records the id, kind,
server id, start and completion times, Silo version, goose version (the
highest applied `goose_db_version`), `applied_migrations` (every applied
goose version, ascending, without goose's version 0; it must end at the goose
version), PostgreSQL server version, `pg_dump`
version, the tables whose data was excluded, each object's size and the
SHA-256 of its stored bytes, whether objects are encrypted, the `SECRET_KEY`
check value, how the table of contents was checked, the blob count and bytes,
`missing_blobs`, the core table inventory, and, when the backup has a restore
set, `restore_set`: its format version and the item count of each section.
The objects are exactly `db.dump` and `blobs.tar`, plus `restore-set.tar`
when `restore_set` is present, each named with `.age` when `encrypted` is
true; a manifest whose names disagree with `encrypted` is refused. A
`before-upgrade` manifest lists `db.dump` alone, with no restore set and no
blob counts, and records the upgrade it precedes in `upgrade.pending`, the
migrations pending when it was taken (see [Before upgrades](#before-upgrades)).
A manifest is at most 16 MiB, the most a reader reads: `missing_blobs` lists
at most 10,000 entries, with `missing_blobs_total` counting them all when
there are more, and a manifest that would still be larger is not written.

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
checksum, and every other field. Because the MAC is over the encoding, a
field added to the manifest after a build first wrote it must encode to
nothing at its zero value (`omitempty`, as `encrypted` and `restore_set`
do), or every backup an earlier build took would read as tampered;
`TestManifestsOfEarlierBuildsStillVerify` checks manifests signed by
earlier builds, byte for byte. Builds that wrote `encrypted` without
`omitempty` signed `"encrypted":false` into an unencrypted manifest; for
one that does not verify as encoded, the check also tries that encoding.
Anyone who can write the destination can
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
  operator also passes `--accept-superuser` (`checkLoadRole`). A selective
  restore needs the admin's confirmation (`accept_unauthenticated`) before
  it reads anything inside the restore set, its preview included, and the
  listing shows it as `unverified`.
- A manifest a destination read must also name the folder and server it was
  read from. A folder whose manifest names this key and does not verify is
  listed as `unverified` with no details, cannot be restored or downloaded,
  and the sweep leaves it.

## Taking a backup

1. Preflight: the user store is PostgreSQL, the destination `backup.destination`
   names exists (and, for a bucket, where backups are encrypted, the confirmed
   key check value is this server's; see [Encryption](#encryption)), its id
   is reserved, `pg_dump` and `pg_restore` are installed,
   and a local destination has free space of at least 1.5 times the previous
   backup plus 1 GB. The default destination shares a disk with the database,
   and filling it would take PostgreSQL down.
2. Open a `REPEATABLE READ`, read-only transaction and call
   `pg_export_snapshot()`. In the same transaction read the server and schema
   versions, the table inventory, and, except for `before-upgrade`, the
   referenced blob keys.
3. Run `pg_dump --format=custom --compress=zstd:3 --snapshot=<id>` with
   `--exclude-table-data-and-children` for `activity_log`, `operational_logs`,
   `policy_decisions`, `catalog_search_index_events`, the short-lived
   secrets of [Credentials](#credentials) (login sessions and refresh
   tokens, Jellyfin and Audiobookshelf sessions, device-code, OIDC,
   account-link, password-reset, email-verification, Discord-link,
   watch-provider, and history-import sign-in flows, Watch Together rooms),
   `backup_status` (its row describes the backup being taken),
   `backup_uploads`, `backup_upload_parts`, and `backup_upload_attempts`
   (their parts are not in the backup), and, unless
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
   spool, encrypting it for a bucket and hashing the stored
   (encrypted) bytes. `pg_restore --list` reads a tee of the plaintext
   stream, before encryption. `pg_restore` reads a custom-format archive's table of contents
   from a pipe and exits without reading the data; the tee then drops its copy
   and the upload continues. If the piped check fails, the check reads the
   stored object back. The manifest records which happened (`pipe` or
   `read_back`).
5. Every kind except `before-upgrade` writes `restore-set.tar` (see
   [Restore set](#restore-set)): its documents and plugin archives come from
   the same snapshot transaction, then the snapshot closes and the section
   blobs are copied, as blobs.tar's are.
6. Except for `before-upgrade`, copy the blobs into `blobs.tar`.
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
download. `backup.destination` picks one, read when each operation starts, so
every API server follows a change without a restart:

- `default`: the operational store from `blobstore.Open` (the local root, or
  the private bucket when one is configured). Backups there are encrypted when
  it is the private bucket, exactly as at `s3`, and plaintext on the local
  filesystem (`Destination.Encrypted`). On an S3 artwork backend with no
  private bucket there is no operational store; the Backups page says to set
  up an S3 bucket for backups.
- `s3`: a bucket used only for backups (`backup.s3.*`), reached with its own
  credentials through `s3client`, even when artwork storage is local. Every
  object written there is encrypted ([Encryption](#encryption)).

One destination is current at a time. Switching leaves the copies at the
previous destination where they are: listing, rotation, deletion, previews,
and downloads see only the current one, and the Backups page says so. Uploads
always use the operational store, whatever the destination.

An upload to either bucket is a multipart upload holding one part in memory.
A backup sizes its parts from the previous complete backup at the
destination: 8 MiB, or larger when that backup's largest object could need
more than 9,000 of them at twice its size (S3 allows 10,000). With no
complete earlier backup there (the first one, or the first after a
destination change) parts are 64 MiB, about 640 GiB per object, which is
then the most an upload holds in memory. Locations read
`s3://<bucket>/<key prefix>/backups/<server-id>/<id>/`, the address
`silo backup restore --from` takes.

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
is written. Backups in any bucket are encrypted. Downloads are short-lived and scoped to one object: a presigned URL
where the store can presign, or a link to
`GET /api/v2/admin/backups/{id}/objects/{name}` signed under its own
capability domain (`artworkurl.NewBackupObjectSigner`) where it cannot. A full
backup downloads its objects as stored, encrypted for an S3 backup. The
restore set of an encrypted backup is never presigned: it is served through
that signed route, decrypted by the server, so it can be uploaded to another
server. The server first decrypts it into an unlinked temporary file while
hashing the stored bytes, and releases it only when they match the
manifest's size and SHA-256, so an object replaced under the same key is
refused, not downloaded.

### Encryption

Every object of a backup in an S3 bucket (the `s3` destination, or the
`default` one on the private bucket) but `manifest.json` is an
[age](https://age-encryption.org) file with a single scrypt recipient. The
passphrase is the standard base64 of HKDF-SHA256 over `SECRET_KEY` with info
`silo/backup-encryption/v1`, 32 bytes (`secret.BackupPassphrase`, beside the
data key and check value derivations in `internal/secret/cipher.go`). There
is no separate backup key, and the UI never shows or offers `SECRET_KEY`.

- The passphrase already has full entropy, so scrypt's stretching adds
  nothing: backups use work factor 2^10 (about 1 MiB and 1 ms per object;
  age's default 2^18 needs 256 MiB). Decrypting accepts at most 2^15 (about
  32 MiB), because an uploaded file names its own work factor.
- The manifest records `encrypted: true` and the key check value. Object
  sizes and SHA-256 are of the stored, encrypted bytes; every reader hashes
  the ciphertext while it decrypts and compares once it has read through.
- `silo backup decrypt` writes the plaintext of one object; `age -d` with
  the passphrase does the same.

**Confirmation.** Encrypted backups start only after an admin confirms keeping a
copy of `SECRET_KEY` off the server: `POST /admin/backups/s3/acknowledge-key`
records this server's check value in `backup.s3.key_acknowledged`, a row only
that endpoint writes. A settings save that switches `backup.destination` to
`s3` is refused (409, `backup_key_not_confirmed`) unless that row holds the
current check value, and needs an endpoint and a bucket. The `default`
destination on the private bucket needs no setting change: its backups wait,
paused, until the key is confirmed. When `SECRET_KEY` changes, or was never
confirmed, the recorded value does not match: encrypted backups pause (a run
fails with the reason, and the backup before migrations is skipped with a
warning) and the Backups page warns (`key_not_confirmed`, with `encrypted`
true) until the admin confirms the key. The
destination settings stay editable meanwhile.

**Reading.** A preview, a restore, and a restore-set download decrypt on the
fly with this server's key. An encrypted backup whose check value differs is
refused before anything is read, with both check values in the message:
selective restore cannot read it at all, unlike an unencrypted backup taken
with another key, where only encrypted values are lost. An uploaded restore
set encrypted with this server's key is decrypted and read; its digest is of
the uploaded bytes.

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

Lock order, for every caller:

1. the before-upgrade wrapper lock, taken only around startup migrations;
2. the subsystem lock;
3. goose's own migration lock.

Ordinary operations never take the wrapper, and nothing takes the wrapper
while holding the subsystem lock. A restore holds the subsystem lock for its
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
| `backup.destination` | `default` | `default` or `s3`; `s3` needs the confirmed key ([Encryption](#encryption)) |
| `backup.s3.endpoint` | empty | `http` or `https` URL |
| `backup.s3.region`, `.bucket`, `.key_prefix`, `.access_key` | empty | the key prefix is the bucket folder; backups go under `<key_prefix>/backups/` |
| `backup.s3.path_style` | `false` | boolean |
| `backup.s3.secret_key` | empty | sensitive: encrypted at rest, never returned |
| `backup.s3.key_acknowledged` | empty | the confirmed check value; written only by the acknowledge-key endpoint |

Every `backup.*` key is server-specific: selective restore never offers one.

An absent or invalid value takes its default, but a setting that cannot be
read fails the backup or the listing, and skips rotation with a warning. It
never falls back to the default: a failed read of `backup.destination` would write an
unencrypted copy to this server instead of the S3 bucket, and one of
`backup.s3.key_acknowledged` would read as unconfirmed.

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
- `before-restore` copies are deleted after 7 days; `before-upgrade` copies
  keep the 2 newest, and always the 2 newest whose manifests this server
  authenticated, so unverified copies at the destination cannot rotate its
  own away. A `before-upgrade` copy whose upgrade has not completed (some of
  its `upgrade.pending` migrations are not applied in the database rotation
  runs against) is never deleted: it is the way back from what failed
  migrations, or a restore that undid them, left. When the applied
  migrations cannot be read, every copy that records its upgrade is kept.
  Neither kind counts toward `backup.keep`.
- Rotation manages only the current destination and the current server id's
  prefix, and never deletes a backup that is the source of a queued or
  running restore. A running restore holds the subsystem lock for its whole
  run, so no other rotation, sweep, or delete runs, and the rotation of its
  own before-restore backup is told to keep the source. A queued restore is
  read from the job queue under the lock rotation holds, the lock restores
  are queued under ([The restore job](#the-restore-job)); the service reads
  it from `admin_jobs` itself, wired when it is built, so nothing that
  starts before the API router can rotate without it. When the queue cannot
  be read, rotation deletes nothing and warns.
- Deleting a backup takes the same lock and refuses (409) while a queued or
  running restore reads it.
- A destination that refuses a delete (object lock, or a policy that denies
  deletes) leaves a warning in the task result, and retention to the bucket's
  lifecycle rules. A folder is deleted manifest first at every destination
  (`deleteBackupFolder`), so a refused delete leaves the backup whole, and
  one that stops partway leaves an incomplete folder for the sweep, never a
  manifest listing objects that are gone.

## Before upgrades

Every API server runs migrations at startup, and so does `silo --migrate-only`.
Both run `database.MigrateAtStartup`, which takes a database backup first
whenever migrations are pending:

1. Take the wrapper lock (`SILOMIGR`, a session advisory lock on a connection
   of its own, outside the pool), waiting while another server holds it.
2. Run the legacy `schema_versions` bootstrap under goose's lock, so the
   pending check sees the versions a legacy install applied. goose's
   `HasPending` takes no lock and would not run it.
3. Ask goose whether migrations are pending, with the provider that runs them.
   Nothing pending ends the sequence.
4. On a database with a schema (any applied version), take the subsystem lock,
   waiting for a running backup or restore, and take a `before-upgrade` backup.
   A new database has nothing to back up.
5. Run goose `Up` with the same provider, still holding the subsystem lock, so
   a nightly run or a restore on another server never reads a schema being
   migrated. goose's own lock is then uncontended.
6. Release the subsystem lock, then the wrapper, then build the audit-detail
   indexes.

Other servers block on the wrapper and, once they get it, find nothing
pending. If the session of the wrapper or of the subsystem lock ends at any
point, the backup or migration in flight is canceled and startup fails.
goose runs each migration in a transaction unless it is marked
`-- +goose NO TRANSACTION`, so a canceled transactional migration rolls
back. A `NO TRANSACTION` migration (a concurrent index build, a backfill
that commits in batches) can stop with its earlier statements committed:
the next start runs it again, and the `before-upgrade` backup is the way
back to the database as it was before any of them ran.

A start after failed migrations does not back up the database those
migrations left. It keeps the earlier attempt's backup, logging its id, when
that backup is this server's newest at the destination, complete and
verified, taken less than 6 hours ago, and records the same pending
migrations over the same applied ones as the database has now. Otherwise it
takes another backup, and rotation keeps both, since neither upgrade has
completed (see rotation above). The 6-hour bound covers the one way the
database can change between attempts: a server running the previous release
on it, whose writes a kept backup would lack. A failed migration retried any
number of times within the window leaves one backup; once all its migrations
are applied, the next rotation treats it like any other `before-upgrade`
copy. A destination that cannot be listed takes a backup, which then fails
with the reason.

The backup runs before this release's schema exists, against whatever the
previous release left:

- It is `db.dump` and the manifest only: no restore set, no `blobs.tar`, and
  no blob queries. Migrations do not touch blobs, so a restore from it leaves
  the files in storage as they are. It cannot bring back a file changed or
  deleted after it was taken: artwork, avatars, or branding replaced or
  removed since stay as they are now, and a restored row may name a file
  that is gone.
- It reads only `server_settings` rows, with defaults for keys the old
  release never wrote: the first upgrade to a release with backups finds no
  `backup.*` rows and backs up to the default destination. Blob storage comes
  from the stored settings, as at startup, opened without recording anything.
- The core inventory checks each table with `to_regclass`, so tables the old
  schema lacks are recorded as absent. `pg_dump` excludes the same table data
  as every backup; an exclusion that matches no table is ignored.
- It publishes nothing to `backup_status` and skips the upload sweep: neither
  table may exist yet. Rotation of backup folders runs as after any backup.
  It takes the subsystem lock itself, not through the nightly task, so the
  nightly slot (`backup_status.last_nightly_slot`) neither skips it nor
  changes with it, and it writes no restore journal.
- Its manifest is signed with `SECRET_KEY` like every backup's, so restoring
  it needs no `--accept-lost-credentials`.

It fails closed: if the backup fails, startup stops with the reason and
nothing is migrated. It is skipped, with the reason logged and the subsystem
lock still held through `Up`, when:

- `SILO_SKIP_BACKUP_BEFORE_MIGRATE=true` is set. It applies to the process it
  is set on; remove it after that start. A value that is not a boolean does
  not skip;
- `backup.enabled` is `false`;
- the user store is SQLite (backups refuse there, and the upgrade that imports
  it must not be blocked; the warning tells the admin to back up the database
  and the SQLite file themselves);
- there is no destination: no operational store, or `backup.destination=s3`
  without a bucket and endpoint;
- the destination is a bucket, where backups are encrypted, and this
  server's `SECRET_KEY` is not confirmed.

Only settings that were read can skip it. Anything else, including a missing
or too old `pg_dump`, an unreachable destination, or a setting that cannot be
read, fails startup. The error names
`SILO_SKIP_BACKUP_BEFORE_MIGRATE` as the way to start once without the backup.

### Timing and readiness

| Variable | Default | Bounds |
|---|---|---|
| `SILO_MIGRATION_WAIT` | `60m` | the wait for the wrapper while another server runs the sequence, and the wait for the subsystem lock while a backup or restore holds it; `0` waits indefinitely |
| `SILO_MIGRATE_TIMEOUT` | `20m` | goose `Up` and the index step ([schema integrity](schema-integrity.md)); `0` disables it |

The backup itself has no deadline; it ends when it finishes, fails, or loses
its lock. A server waiting for the wrapper logs that it is waiting, then a
heartbeat every 20 seconds; one waiting for the subsystem lock behind a running
backup or restore does the same every 30 seconds; and the backup logs each step
and a heartbeat every 30 seconds.

The API listener opens only after migrations, so while a server waits, backs
up, or migrates, its readiness and health endpoints do not answer, which every
probe reads as not ready: a deployment's start budget has to cover the wait,
the backup, and the migrations. The image's `HEALTHCHECK` allows a 20-minute
start period. How to size probes for a deployment belongs in the user manual.

### Restoring one

A `before-upgrade` backup is a full-restore recovery point; the Backups page
offers no selective restore for it. `silo backup restore` and `silo backup
verify` read it as a database-only backup: they fetch and verify `db.dump`
alone and leave files in storage untouched. Swap mode needs no
`--database-only`. In separate mode the command still requires
`--database-only` or `--blobs-into`, since it checks its flags before it
reads the backup, and writes nothing to the latter; `RestoreSeparate`
itself requires a files target only for a backup that has files. The
backup holds the previous release's schema, and every later release
carries that release's migrations, so the new release's binary can restore
it; a release started on the restored database migrates it again and takes
another `before-upgrade` backup.

The steps after the load (new signing keys, clearing unreadable settings,
the credential and setting carries, ending unfinished work, the account
rollback report) run against the backup's schema, not the binary's. When
the backup's applied migrations are fewer than the binary's, a step that
meets a table, column, or type that schema lacks (SQLSTATE 42P01, 42703, or
42704) is skipped with a printed line naming it, and the restore goes on.
Each step changes the restored database in one transaction or statement, so
a skipped step changed nothing. A backup of the binary's own schema skips
nothing: there the same error fails the restore. The credential carry is
narrower, so an older schema does not leave the backup's credentials in
place: it skips only the carried tables the schema does not have (printing
them; the live rows of those tables are not carried) and writes only the
columns it has.

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

`restore-set.json` has `format: "silo-restore-set"`, `version` (4: version 2
added the users section; version 3 the home sections section, template
collections in `collections.json`, and an omitted item's `cause` and
`error`; version 4 a home section's `web_only`), the source
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
`home_sections` or template collections in version 2, a `web_only` section
in version 3) is refused. A set without `web_only` reads it as false, the
column's default. Readers accept versions 1 to 4; version 4 is always
written.

| Section | Holds | Blob prefixes |
|---|---|---|
| `libraries` | `media_folders` rows with their paths and per-library options, and the poster key | `library-posters/` |
| `plugins` | per installation: plugin id, version, enabled state, installation id, the stored archive (`plugin_archives`, a record only: never installed), and `plugin_runtime_configs` rows as stored | none |
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
prefixes and only keys the section references, or variants stored beside a
referenced artwork original (in its directory and of its revision, or, for
the legacy names the writer used, `original.<ext>` and `w<width>.<ext>`,
both without one; at most 16 per original); avatars a user document names
listed for the section; documents that decode strictly
(unknown fields and trailing data refused) and match the index counts; and
limits on entries (200,000), document size (64 MiB for the collections and
account documents, 32 MiB for the index, 8 MiB for `libraries.json` and
`home-sections.json`, 4 MiB for `settings.json` and each `plugin.json`), the
documents one read holds together (256 MiB, checked from the index's listed
sizes before any document is read; account documents are not held, see
below), plugin archive and blob size (256 MiB),
total bytes (32 GiB), and JSON nesting (32). What a document decodes to is
bounded too, before it is decoded: `internal/backup/jsonsize` walks the
document beside its Go type and adds up what `encoding/json` would allocate
(each array element at its type's size, doubled for slice growth; map
entries, pointers, strings, raw values). A document is refused when that is
over 8 times its size plus 64 KiB, when the set's other documents together
would decode to over 256 MiB, or when one array or object holds over
1,000,000 elements. A short document of empty elements (`[{},{},…]`) would otherwise
decode to a whole struct per three bytes; documents Silo writes decode to
about their size.

Account documents grow with a server's households and watch history: a large
server's add up to gigabytes, far past what one read may hold. The read
writes each to the set's temporary directory (`TMPDIR`, a 0600 file checked
against its digest as it is written), after checking from the index that
their listed sizes plus 1 GB fit there (`ErrNotEnoughTempSpace` otherwise).
Once the archive is verified it decodes and validates them one at a time,
keeping only each account's entry and file (`UserRef`). The preview and a
restore load one account's document when they plan or apply it
(`RestoreSet.loadUser`), checking its digest and validating it again, so
memory follows the largest account rather than their sum. `Cleanup` removes
the directory.

Every entry a restore writes (a setting, a library, a
plugin configuration, a collection, a template collection, an item artwork,
a branding asset, a page section, and each account's profiles) is left out
as unusable when it encodes to more than 1 MiB, the v2 API's request body
cap (`maxRestoredEntryBytes`), and a stored thumbhash is the one computed
from the restored image (item artwork) or one of thumbhash form, never
anything else. A selective restore also writes at most 16 MiB of collection metadata
(every field but the item list) in all and 8 MiB into one library, counted
over the collections, template collections, and section-managed collections
it commits; the rest are left out with `over_restore_budget`, since a
library's collections are listed whole. The collection editors have no
per-field limits besides the request body cap, so a restore applies none of
its own to single fields. A set holds at most 5,000 settings, 1,000 libraries, 500 plugins, 20,000
collections and as many template collections, 50,000 item artwork entries,
64 branding assets, and 10,000 page sections, valid or not; a set beyond
one of them is refused when read, so a preview lists a bounded number of
items. A set's collections list at most 1,000,000 items together. The preview
cache charges each set what it holds once read: the bytes read, jsonsize's
estimate of its decoded documents, and its blob index; and it keeps at most
one set of a backup this server cannot verify, dropping the older one.
Restored rate limits are written through the rate-limit configuration's
own checks (`RateLimitHandler.ApplyRateLimitSettingsInTx`), every other
setting through the admin settings batch, in the item's transaction. A
restore job claimed while the request that queued it still holds the
subsystem lock waits up to 10 seconds for it (`lockWithin`) before it
gives up. A set lists at most the first 100 entries of a section it
leaves out, and one more entry counting the rest, so a preview of a set of
malformed entries stays small. A plugin archive is checked against its
digest and discarded, never installed ([Plugins](#plugins)). Readers accept
every format version up to theirs and refuse a newer one with "This backup is
from a newer Silo. Upgrade this server first."

**Nothing inside a restore set is decoded before it is verified,** with
one exception: an uploaded restore set sent without a SHA-256. One sent
with a SHA-256 is hashed whole when it completes and decoded only when the
digest matches; one sent without is decoded unverified, under the reader's
bounds alone (the tar guard, the entry and size caps, `jsonsize` before
each document, `DecodeStrict` with `CheckBounds`), and is restorable only
with the admin's confirmation that it is unauthenticated. The
manifest records the restore set's digest and the digest of its index
(`restore_set.index_sha256`), both covered by the manifest's `mac`. For an
authenticated manifest the reader checks the index against its digest
before decoding it, and the whole archive against its digest
(`ReadOptions.Verify`) before decoding any document; until then documents
are held as bytes, each checked against its index digest. A backup whose
manifest cannot be authenticated, or that records no index digest, is read
only after the admin confirms (`accept_unauthenticated`), its preview
included. Previews of one archive share a read (`restoreSetCache`); the read
stops when every preview waiting on it has gone.

A restored library, admin collection, template collection, or section
meets the editor's own checks, run through restore targets so they are the
editor's code: the library create command's (`CheckRestoredLibrary`), the
admin collection create command's (`CheckRestoredCollection`), and the
section editor's (`CheckRestoredSection`). One the editor would refuse is not
offered, with its message. No entry may encode to more than the v2 API's
request body cap (1 MiB; a collection's item list is not counted), as no
editor request could write more for one.

Any of those failures refuses the whole set. An entry whose own values no
restore could write is left out instead, and the rest stays readable
(`RestoreSet.Unusable`): a setting key that is not one, a library without an
id, name, type, or clean absolute folders, a collection, template
collection, or section with no valid id, title, scope, type, or JSON, a
section carrying a collection its configuration does not name, item artwork
that does not say what it is, a plugin whose configuration keys repeat or
whose values are not JSON, and a branding entry that is not for a branding
asset setting (`branding.AssetSettingKeys`) or whose reference is not one
file name naming its own blob (a branding entry is written as a setting), and
a user document that names another account than its file, or whose account
or profiles fail their checks (a known role, bounded fields, unique profile
ids, avatars only under `profile-avatars/`, and data only of registered data
types, each passing its owner's strict checks); that account is left out.
The preview lists each as an unchoosable item with reason `unreadable`; an
id listed twice still refuses the set.

A referenced blob absent from the archive was missing when the backup was
taken; the preview and the report list it with the item.

Previews of a backup share one validated copy of its restore set, keyed by
the archive's SHA-256 from the manifest (at most 4 sets and 256 MiB of
documents and blob keys, least recently used evicted), and concurrent
previews of the same archive read it once. Only the parsed documents, the
index, and the account documents on disk are kept, never blobs or archives;
comparisons with this server run on every request. Each preview holds a
reference to the set it reads, and an evicted set's directory is removed
when its last reference is released. Applying a restore never uses the cache: the job reads and
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
  (`playback.ffmpeg_path`, `playback.transcode_dir`, `playback.hw_*`),
  `backup.*`, and state the server writes for itself under an admin prefix:
  sweep and cleanup checkpoints, `recommendations.embedding_lock` (the
  stored vectors' model and dimensions), and
  `recommendations.policy_marked_generation`. The network-security and
  sign-in trust toggles are server-specific too (`trustSettings`): whether
  webhooks and media-server connections may reach private networks
  (`*.allow_private_destinations`), `clientip.trusted_proxies`, sign-up,
  self-service password reset, local password sign-in, and matching
  external sign-ins by email. A backup is only as trustworthy as its
  destination, and one of these turned on would let a profile reach this
  server's private network or let someone sign in, so they stay as the live
  server has them. `TestTrustSettingsAreServerSpecific` fails on a setting
  named like one that is not in the list.
- A restore never widens access. The access settings
  (`accessSettings`, shared with the full restore: `access.unrated_content`, the Jellyfin and
  Audiobookshelf sign-in switches, access and refresh token and Jellyfin
  session lifetimes, and the identity provider's re-check interval and
  outage policy) are offered, but a value that grants more than the live one
  (`AccessSettingWider`; a value that does not parse counts as wider)
  is never written: the preview marks it `kept_stricter` and the report says
  the stricter current value was kept. The write compares again under the
  settings lock (`accessUnchanged`, the admin settings write path's guard),
  so a stricter value an admin saves after the plan is kept: the item starts
  again from its read. `TestAccessSettingsAreComparedOnRestore` requires
  every access setting to be offered, so none is compared in vain. Mail's
  transport security (`email.smtp_security`) is compared the same way: a
  restore never weakens it to `none`.
- A service's address and its secrets restore together (`settingRoutes`:
  the SMTP host and password, the AI, speech, subtitle-AI, and embedding
  base URLs and their keys). When a restore changes the address, each secret
  must come from the backup and decrypt here; otherwise the restore clears
  it and turns the service off (`email.enabled`) instead of sending this
  server's secret to the backup's address. The preview marks those settings
  `cleared`, and the secrets are expected unchanged under the settings lock.
- A backup this server cannot authenticate restores no setting that
  connects to an outside service (class `external_service`) and no plugin
  configuration, whatever the admin confirmed: the preview marks them
  `unverified_backup`. Nor does it turn on work that sends library data to
  outside services or runs for hours: a new library is created with AI
  translation of metadata, trickplay previews, intro detection, chapter
  thumbnails, and box sets off (`costlyLibraryOptions`); every collection
  a restore creates (an admin collection, or one a section carries) keeps
  its source but gets no sync schedule (`applier.createCollection`, which
  every creation goes through); and a template collection keeps this
  server's source and sync schedule; every home section comes back
  disabled (each home page request fetches every enabled row, and the
  seasonal task fetches the MDBList lists of every enabled seasonal one).
  A section's carried collection, like an admin collection, may not list
  an item twice. `unverifiedWorkFields` lists every such item field, with where the
  restore leaves it inactive, and `TestUnverifiedWorkIsClassified` walks
  it. The preview says what is left off; for a verified backup it names the
  library options the restore turns on and the lists a seasonal section
  fetches. One restore writes at most 50 seasonal sections, and a seasonal
  task run fetches at most 200 lists, taking the rest in turn on later runs.
- Every collection a restore or import writes from a document stores its
  source as the editors build one (`collectionutil.CanonicalCollectionSource`):
  each list URL the sync fetches (an MDBList collection's, a `tmdb_list`
  source's) goes through its canonicalizer and is stored canonical; a
  collection with any other URL is refused (`planner.checkCollection`).

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

A restore never installs or runs a plugin from a backup. Its archive is
vouched for only by the backup's own checksums, and anyone who can write the
backup destination could replace it with code that would run as the Silo
user. A plugin missing here is not selectable (`needs_install`): the preview
and the report tell the admin to install that plugin id and version from a
plugin catalog, through the normal install path and its checks, and to
restore it again, which then applies its settings. The archive stays in the
restore set as a record of what was installed; the reader checks its digest
and keeps no copy.

A plugin installed here keeps its installation and enabled state. At the
same version its configuration is decrypted under the source installation
id, checked with the validation an admin save uses, and written re-encrypted
under this installation's id
(`plugin_runtime_configs:config_value:<installation id>:<key>`); at another
version only values the installed version accepts are written ("settings
only"). Configuration that cannot be decrypted, as with another
`SECRET_KEY`, or that fails validation is left out, and the report says why.
`plugin_instance_state` is never restored. Reconfiguring a plugin advances
its runtime generation in the item's transaction, which replaces stale
processes even when the event after commit is lost.

### Libraries

A backup library carries its settings, the box set settings among them, its
folders, and its poster. It matches a library here only by an identical
folder set. A
library sharing a folder with another library here cannot be restored, nor
can one with a folder this server cannot open (an absolute directory it
can read, checked by opening it and reading at most one entry). A library
entry lists at most 64 folders, none twice. A restored library gets a new id, reserved
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
through the admin settings write path. Item artwork or a branding asset this
server already shows as the backup has it is the same only when its files
(the artwork's stored family, the branding blob) are in the assets store,
checked with `Stat`, eight at a time. When any is missing it differs
(`files_missing_here`), and restoring it writes the missing files back with
the exclusive create every restore uses and changes nothing else; the next
preview finds it the same. Library posters and collection images of a library
or collection matched here are not restored onto it, so they are not checked.

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
(title, position, featured, item limit, enabled, web only, configuration,
and missing items of its managed collection), and a restore replaces its
configuration as an admin save would. A restore writes `web_only` as the
backup has it on a featured row, whether it creates or replaces the
section, so a web-only hero row never comes back on the apps. Only a collection the section manages stands for the
carried one: a managed section pointed at an ordinary collection since the
backup lists "collection replaced", whatever that collection holds, and the
restore points it back at its managed collection (the one still here with
its management key, or a new one). The key's section id and each
`library:<id>` it names are this server's (`remapManagementKey`), and a
collection here with that key is reused only when its libraries are the
mapped ones (`collectionScopeIs`); otherwise a new one is created under a
key of its own. A missing one is created, with its backup id when no
section here has it, so a profile override naming it by id resolves. The
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
   lock through commit, and records the item's outcome in the same
   transaction.** The outcome is a `backup_restore_items` row; the job's
   result holds the counts, the library map, and the latest 20 outcomes, so
   each item's write is the same size however many came before it. The full
   list goes into the result once: when the restore returns, however it
   ends, or when stale recovery marks it interrupted. The rows are removed
   in the statement that records that final report (`Complete`, `Fail`,
   `CancelWithResult`, or the recovery's own update), never before, so a
   crash between reading them and recording the report loses no outcome.
   The package write paths take that transaction.
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
longer running under its claim and writes nothing. An item's commit writes
its heartbeat at `clock_timestamp()`, the commit's time: `NOW()` is the
transaction's start, which an item longer than the stale window would leave
already stale for the updater waiting behind it. Every heartbeat only moves
forward (`GREATEST(heartbeat_at, clock_timestamp())`), so an ordinary
heartbeat that waited on the row behind an item cannot write its older
start over the item's commit. Running the same restore
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

A restored blob meets the checks an upload of its kind meets, before it
reaches storage (`checkRestoredBlob`): a branding asset its kind's size limit,
the type its extension serves, and the kind's processing
(`branding.CheckStoredAsset`); any other image at most 10 MB, a JPEG, PNG, or
WebP by its bytes, and a source `imageutil.CheckImage` accepts. A blob that
fails fails its item with `ErrRestoredFileRefused`; other items go on.

The job extracts only the blobs its selected items write (`selectedBlobs`):
the exact keys the apply step reads (a library's poster, a branding asset,
an item artwork's family, a collection's uploaded poster and backdrop with
their families), never a prefix. It sizes them from the preview's read of the
same restore set, and refuses to start (`ErrNotEnoughTempSpace`) for more
than 50,000 files, or unless the temporary directory has their size and the
account documents' plus 1 GB free and a free inode for each plus 10,000. The rest of the archive is
read, checked, and discarded. Reading the set and extracting stop when the
job is canceled; once the first item runs, a cancel stops between items.

Before its first item, a restore records each store's identity
(`blobstore.RecordIdentity`), which a store opened with settings would do on
its first write through a pool connection; items write files inside their
transactions, and on a one-connection pool that second connection never
comes.

### Lock order

An item takes the locks the editor or API path for the same rows takes, in
the same order, so the two wait for each other instead of deadlocking:

- Admin collections: the collection parent (`FOR NO KEY UPDATE`), then the
  library's collection order revision, then membership rows. The library
  editor's move and reorder takes every parent of the library first, in id
  order, so a restore holding one parent never holds what the editor waits
  on. Restoring onto an existing collection (admin, template, or a page
  section's managed one) takes its parent first in the item's transaction
  (`catalog.LockLibraryCollectionInTx`), before it creates a group, adds
  items, or regroups a membership, any of which takes the order revision
  (`TestGroupingWaitsForTheEditorDB`). The catalog functions take the parent
  again themselves, which is free in the same transaction, so no caller can
  skip it.
- Page sections and featured sections: the section editor's order (see
  `selective_sections.go`): collection parents, then section rows in id
  order, then surface revisions. A section whose collection references
  changed between its read and its row lock is `sections.ErrReferencesChanged`:
  its new parents are never locked under the section lock.
- Any item that fails with that, a deadlock (`40P01`), or a serialization
  failure (`40001`) rolls back to its savepoint and starts again from its
  read, up to three times, and is then left out as changed during the
  restore (`changed_since_preview`).
- Accounts and profiles: `userdata.Preparer`'s order, the account locks, then
  each data type's own, then data rows.
- Libraries, plugins, and settings write through the editor's own functions
  (`catalog.CreateFolderInTx`, the plugin configuration store, the admin settings write
  path) and take their locks.

### Follow-ups

Some work runs only after an item commits: a restored library's sections,
provider chain, monitoring, and first scan, and a restored template collection's
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
A restored library's default and home sections are their own follow-up
(`library_sections`), which runs only at the end of the restore, once the
saved sections it carries are written, and seeds only where none exist: a
default renamed before the backup comes back once, not beside a fresh
default. Template and section-managed collections seed no sections.
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
   when this server lacks it (`watchlist.EnsureTitlesInTx`, `favorites`). A
   profile's own export (`userdata.Target.Untrusted`) creates no title and
   writes none of its snapshot to one other accounts read: it only locks and
   links titles this server already has (`watchlist.LockTitlesInTx`,
   `LinkExistingTitleInTx`), and reports the rest as left out;
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
item. Every other one the document lists is matched up front, before any
data type reads it, by the history import matcher's rules
(`historyimport.MatchAllOn`): its provider-id lookups are collected for all
items and read in batches, one query per lookup and provider column for up
to 20,000 values, and a lookup that finds nothing is remembered, so a
document's unknown items cost a few reads, not one each. The reads run in
the restore's or import's own transaction: the transaction already holds a
connection, and a match that waited on the pool for another would wait
forever on a pool of one. The matched items' visibility is checked in the
same one read as the present ones'. A profile export names at most 100,000
items, its items list and every id its data names together, once each,
checked before the import opens its transaction and mirrored by export. An item still unmatched is left out and listed in the report by
title. Items the target profile may not see here are left out
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
password hash, so it signs in with its old password. A password or PIN hash
in a format or cost this server does not accept is replaced, and the report
notes that a new one must be set.

After an account's item or a profile import commits, the restore tells the
notification system which items each profile's data now names
(`ItemsChanged`, the interest recompute the user store's own writes
trigger), so new-episode notifications follow the restored favorites,
watchlist, and progress without waiting for the nightly repair.

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
both take the last place. A restored profile is primary only when the
account here has no primary profile, and a restore never makes another
profile primary; so an account with none here (a new one, always) that
restores any profile must restore the backup's primary profile with it,
or it is left out (`needs_primary_profile`): without one, nothing could
manage the household. The primary profile is created first, so it takes
the first free place under the account's profile limit; an account here
with no primary and no free place is left out in the preview. The admin page selects the primary profile, locked,
with any other profile of a new account.

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
Personal collection posters are not carried. The export holds what the
profile may see now, as every other read of its data does: rows about titles
outside its libraries or above its content-rating or advisory limits are left
out, and so are those titles' item references (`userdata.Keep`, checked once
in the export's snapshot). Watchlisted titles outside the library are kept
by the watchlist's own rule: their stored certification under the profile's
rating ceiling (`access.RatingAllowed`), so a title without one is left out
under a ceiling. A title the catalog no longer holds stays, since
no read can show it. A server backup's user documents are not filtered.
The export reads the data once to learn which items it names, keeping no
row, and once to write it, and refuses (413) a document larger than an
import accepts.

Exports and imports share one admission (`ProfileTransfer.admit`): one per
account and two per server at a time, refused (409) rather than queued, and
taken before either opens a transaction. An import's slot is taken before
its body is read (an operation middleware on `importProfile`), so a busy
server buffers nothing; the import parses the document once into its
top-level members, once strictly, and runs each data type's checks once.

Every imported document, and every account document of a restore set,
passes one bounds check after it decodes (`userdata.CheckBounds`): a
string is at most 1 KiB, a map 64 entries, a list `userdata.MaxEntries`,
and raw JSON 64 KiB and valid, unless a field's `bound` tag sets its
editor's limit (a collection's description 8 KiB, source URL 2 KiB, and
rules 1 MiB; a subtitle path 4 KiB; a section override's configuration
64 KiB). Track signatures are decoded into their types and stored as the
playback writer encodes them. `TestEveryImportedFieldIsBounded` makes
each field of every importer's document, and of an account document,
too large in turn and requires the check to refuse it. A carried
collection reference maps to the importing profile's own copy (the one
its import created or merged into), and on the same server to an
uncarried collection only one that profile can see.

A collection sort preference is stored under the identity, field, and
order the sort editor stores it under
(`catalog.NormalizeCollectionSortPreference`): a watchlist or favorites
sort under the one `personal` id whatever id the document names, so a
document lists at most one per personal list; a library-kind sort under
an admin collection the restore's collection mapping finds here, mapped
for the whole list in one read. A profile's own import maps an admin
collection only when the profile may see it
(`catalog.CanAccessLibraryCollection`, the check the sort editor's save
makes); one it may not see is left out exactly as one that is gone, so the
import tells nothing about hidden collections. A document holds at most 10,000. Every
list of every data type is written, and compared for a preview, in one
set-based statement whatever its length (settings in one per scope);
personal collections, at most 1,000 an import, are created or merged one
at a time. An import or restore never leaves a profile with more than
1,000 personal collections, or 50 with a sync schedule, or 16 MB of
collection rules, display filters, sorts, and sync sources together,
counting the ones it has (read under the account's import lock): past
these, a collection is left out or created without its schedule, and the
result says so. An imported collection's sort and sync source are decoded
into their types, refusing members they lack, and stored as the editors
encode them; each list URL its sync fetches goes through its editor's
canonicalizer (`collectionutil.CanonicalCollectionSource`), and a document
with any other is refused. The
collection editor has no such limit. From a backup this server cannot
verify, personal collections get no sync schedule either
(`userdata.Target.Unverified`). Once an import or restore commits, a
profile's notification interest is rebuilt once
(`InterestUpdater.QueueProfileRebuild`, coalesced, at most 1,000 queued)
rather than recomputed per item, and its recommendations refreshed once. `TestImportsReadInBatchesDB` drives each list and each sort
kind with 100,000 and 1,000 distinct entries, every reference mapping,
and requires a bounded number of statements.

An import writes only the profile of the session, never one the document
names, in one transaction, with the merge rules above. It refuses a document
that carries an account or access field (at the top level or among the
profile preferences), unknown fields or data types, a newer version, nesting
deeper than 32, more than 64 MiB, or one that would decode to more than 8
times its size plus 64 KiB (`jsonsize`, checked before it is decoded); each
data type then applies its own strict checks, and `userdata.DecodeStrict`
bounds each type's document the same way, in restore sets too. Items the profile may not see are left out and listed: outside
its allowed libraries (libraries it hid itself still count as visible), or
above its content-rating or advisory limits, through the same canonical
filter; so are preferences of libraries it cannot see. They are listed with
the items this catalog does not hold, in one list (`unmatched`, with `hidden`
always empty), so an import cannot tell a restricted profile which titles its
hidden libraries hold. Library references survive only on the server
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
backups/uploads/<upload id>/<part>.<nonce>[.age]
```

**Parts are encrypted on a bucket.** Uploads go to the operational store,
which is the private bucket when one is configured. There every part is
stored encrypted with the archive key ([Encryption](#encryption)) and named
`.age`, since an uploaded restore set holds password hashes, and creating an
upload or sending a part needs the confirmed `SECRET_KEY`, as a backup there
does (409, the same `key_not_confirmed` problem otherwise). On the local
filesystem parts are stored as sent. Each part is its own age file, so the
parts read back in order, decrypted, are exactly the bytes the client sent.
A part encrypted with a `SECRET_KEY` the server no longer has cannot be read:
completing or restoring from that upload fails, and the upload is deleted
and sent again.

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
  5-minute deadline, and cancellation stops it: an S3 request is canceled,
  and a local write's temporary file is closed, so its next write fails, and
  removed. An attempt is abandoned only after 10 minutes, once its writer has
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
`checking`, publishes the `upload` operation, and, with a 20-minute deadline
that closes the part being read when it passes (so a stalled read cannot
keep the lock), reads the parts in order
through `ReadRestoreSet`, the same checks a backup's restore set gets. The
SHA-256 of the bytes read becomes the upload's digest. These are the bytes
the client sent (the file as uploaded, still encrypted if the admin
uploaded a `.age` file), never the parts as stored, so the digest is what a
client computes, what a declared SHA-256 is compared with, and the same
whether the parts are encrypted at rest: a preview returns it,
a restore request must carry it, and the restore job reads the parts again
and refuses bytes with another digest. A set that fails the reader is
`rejected` and its parts are deleted at once:

| Reason | When |
|---|---|
| `encrypted` | The file is an age file (binary or armored header) this server's key cannot open: "This file is encrypted with another server's SECRET_KEY. On a computer with that server's SECRET_KEY in the environment, run `silo backup decrypt restore-set.tar.age -o restore-set.tar`, then upload restore-set.tar." One encrypted with this server's key is decrypted and read. |
| `newer_silo` | A newer format version |
| `not_a_restore_set` | Anything else the reader refuses, with its error |
| `checksum_mismatch` | Other bytes than the SHA-256 declared at creation |

The web admin reads a chosen file's first 512 bytes before it creates an
upload and refuses only a file that is neither a tar starting with
`restore-set.json` nor an age file; its picker offers `.tar` and `.age`. An
age file is uploaded for the server to decide, with the line "Encrypted:
this server can read it only if it was made with this server's SECRET_KEY."
shown while it uploads: an age header names no key (a scrypt stanza carries
a salt and a work factor), so only the server, trying its key, can tell.

A failure to read the destination rejects nothing: the session returns to
`uploading`, and completing again checks it again. A check does not stop
when its request's client disconnects. Every outcome (`complete`, `rejected`,
back to `uploading`) is written on the lock's own session, so a check whose
lock was lost writes nothing: another server may hold the lock and be
checking the same session. A session left `checking` by a server that
stopped, or by a check that lost the lock, is checked again by the next
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
operation. `SECRET_KEY`, `DATABASE_URL`, and bucket credentials come from the
environment (or `-env`); nothing comes from the database being replaced.
`--from` is a local backup folder (the folder at a local destination, or a
downloaded copy), or a bucket folder,
`s3://<bucket>/<key prefix>/backups/<server-id>/<id>`, reached with
`SILO_BACKUP_S3_ENDPOINT`, `SILO_BACKUP_S3_REGION`,
`SILO_BACKUP_S3_ACCESS_KEY`, `SILO_BACKUP_S3_SECRET_KEY`, and
`SILO_BACKUP_S3_PATH_STYLE`.

A local unencrypted folder is read in place. A bucket folder or an encrypted
one is fetched first: the manifest is read and an encrypted backup with
another key check value is refused, then `db.dump` and `blobs.tar` (only
`db.dump` with `--database-only` or from a `before-upgrade` backup) are copied into a working folder in the
current directory as plaintext, decrypted as they stream, each checked
against the manifest's size and SHA-256 of the stored bytes. The manifest's
MAC is checked before anything downloads (validation checks it again), so
a changed manifest fetches nothing. Before anything downloads, the folder's filesystem must have room for the objects' stored
sizes (a plaintext is never larger) plus 256 MiB, and an object that writes
more than its stored size is stopped. The folder is mode 0700, its files
0600, and it is removed when the command ends. `pg_restore` then reads the
local copy, and nothing changes until every check passed.

The first SIGINT or SIGTERM cancels a `silo backup` command, which stops
and cleans up before exiting: the working folder, a decrypt's `.partial`
file, and a swap's rollback. A second signal exits at once. Once a swap's
journal exists, the first interrupt says that a second may leave the
databases closed and prints `silo backup restore --recover <time>` with the
swap's time.

A swap's cleanup (settling, rolling back, or dropping an unused restored
database) ignores the interrupt but has one 5-minute deadline for the whole
cleanup, so a statement that never returns cannot keep the databases closed
forever. When it passes, the command stops with the databases closed and
prints the journal and the `--recover` command.

`silo backup decrypt <file | s3://bucket/key> [-o <file | ->]` writes the
plaintext of one encrypted object (to `<name>` without `.age` beside a local
file, or in the current directory for an object). With `manifest.json` in
the same folder it refuses a key with another check value, and a manifest
whose MAC does not verify under this key, before reading, and checks the
stored bytes against the manifest. The plaintext lands in
`<output>.partial` and is renamed only after every check passed.

Every mode first validates: the manifest is present and complete (exactly
`db.dump` and `blobs.tar`, `db.dump` alone for `before-upgrade`, and
`restore-set.tar` when it lists a restore set,
each with a size and SHA-256; exactly the core
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
checked against the binary in the verification step instead. An encrypted
backup needs the key it was taken with. An unencrypted backup taken with
another key needs `--accept-lost-credentials`: its encrypted settings and credentials cannot be
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
  since the backup would receive them again. Notification retention then
  removes a carried mobile registration when its account has no login left
  and no API key; the phone registers again at sign-in. A browser subscription
  bound to a login session is carried too, but it names a session the restore
  does not have, so it receives nothing and retention removes it; the web app
  registers it again at the next sign-in. A backup taken before subscriptions
  recorded a login drops the binding, and retention removes such an unbound
  subscription once its account has no login left and no API key. What this
  server holds for
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
  semantic-search providers that receive library data, what spends on an AI
  provider (metadata translation when a viewer opens a title,
  `metadata_ai.on_view`; the service tier; the per-user transcription quota
  and its window), marker sharing and online marker lookups, and LAN
  discovery. Every other switch setting is in `restoredFlagSettings` with
  its reason (`TestEverySwitchSettingIsDecided`), which covers only
  `true`/`false` settings: a mode or limit that enables spend or sends data
  out has to be added to `accessSettings` by hand.
  The
  live row is copied as stored, so an encrypted value stays bound to its
  key name; values are compared decrypted, and one that does not decrypt
  counts as wider. The command lists each setting it kept.
- Separate mode (`--into`) restores a copy, not the live server: it deletes
  the backup's carried credentials and carries none.

### Separate mode

`silo backup restore --from <backup> --into <database URL> --blobs-into <dir | s3://bucket/prefix>`
restores into a pre-created empty database and a separate directory laid out
as a local store root, or a bucket under a key prefix (with the
`SILO_BACKUP_S3_*` credentials), changing nothing in the live deployment. Before
anything is written it refuses a target that would overwrite something,
since a copy keeps nothing it replaces: `--into` must not be the live
database (`DATABASE_URL`, compared by host, port, and database as pgx
resolves them), and `--blobs-into` must be missing or an empty directory, or a key prefix
that holds nothing, and not the live server's local file storage (read from its settings when
`DATABASE_URL` is set; symbolic links resolved). It needs no
`CREATEDB` or rename privilege. After `pg_restore --exit-on-error
--no-owner --no-privileges`, it verifies the schema version, the applied
migrations, and the table inventory against the manifest. It then deletes
the recorded storage identities (`artwork.storage_identity`,
`storage.operational_identity`) from the target: they name the backed-up
server's stores, so a server started on the copy would refuse the stores it
is configured with. That server records its own on its first start or write.
It deletes the backup destination settings too (`backup.destination` and
`backup.s3.*`): the copy keeps the backed-up server's id, so backing up to
that bucket would rotate away the original's backups. The copy backs up to
its own storage until an admin names a destination. The copy
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
it (`TestBackupRestoreRoundTripDB` verifies as one). An encrypted backup
needs its key.
CI runs the same round trip against PostgreSQL
(`TestBackupRestoreRoundTripDB`): a seeded database with blobs and an
encrypted setting is backed up, restored in separate mode, and compared
table by table, blob by blob, and by the decrypted value.
`TestEncryptedS3RoundTripDB` does it through an encrypted S3 destination (a
fake bucket), and `TestBackupCommandsReadS3DB` runs `verify`, `restore`, and
`decrypt` from `s3://` addresses, with the right key and another.

## Health

There is no server-side dashboard warning registry; the dashboard derives its
notices on the client. `GET /api/v2/admin/backups` therefore returns the
conditions the dashboard shows as `warnings`: backups turned off, no
successful backup in the last 48 hours (`no_recent_backup`), at any
destination: the later of the status row's `last_success_at` and the newest
complete backup at the current destination whose manifest this server
authenticated (anyone who can write the destination could set an unverified
one's completion time in the future), which also covers a status row a
full restore left empty; no authenticated backup at the current destination although one
completed before (`no_backups_at_destination`, after switching
destinations), the destination missing or unreachable, the SQLite user store, `pg_dump` missing or older than the
server, and encrypted backups (any bucket) paused for an unconfirmed
`SECRET_KEY` (`key_not_confirmed`). It also carries this server's key check value, whether
it is confirmed, and whether new backups are encrypted. It also returns the active operation and the latest run from
[the status row](#status).
