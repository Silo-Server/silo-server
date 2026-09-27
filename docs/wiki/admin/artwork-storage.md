# Artwork storage

In **Settings → Infrastructure → Artwork storage**, choose Automatic, Local
disk, or S3. Automatic uses the public S3 bucket when one is configured and local
disk otherwise. Changes require a server restart.

Local disk suits a single server. Persist the displayed artwork directory in
Docker; it contains provider caches and uploads. S3 is recommended when multiple
hosts share a catalog, so every host can read the same artwork.

Provider artwork caching is enabled by default on new installations and works
with either backend. The setup wizard can finish without configuring S3.

## Changing a locked storage location

The first write to assets storage records its location. From then on the backend,
the local artwork path, and, for S3, the public endpoint, bucket, and key prefix
no longer save directly. Silo records any configured private bucket at startup,
even if it is empty, and locks its endpoint, bucket, and key prefix. The settings
page opens a managed storage transition for changes to a locked location.

A transition verifies the new location, copies what the chosen policy covers,
switches the settings, and asks for a restart:

- **Start fresh** reads nothing from the old storage. If the assets location
  changes, provider artwork returns to its saved source; uploads and downloaded
  subtitles are not copied. Avatars are not copied when private storage changes.
- **Preserve personal uploads** copies branding, collection and library posters,
  and downloaded subtitles when the assets location changes, plus profile
  avatars when their location changes. Provider artwork returns to its saved
  source when the assets location changes.
- **Migrate everything** copies all files from each location that changes,
  including avatars, diagnostic bundles, and catalog export files when private
  storage changes.

Switching from S3 to local disk makes local disk the destination for assets and
private files. The selected policy determines which source files are copied. A
local install can add or remove a private bucket the same way. Silo never
deletes the old storage; remove it yourself once the transition and its restart
have finished.

If startup reports a private storage mismatch, stop every API writer, including
nodes on older releases. The `storage.operational_identity` row in
`server_settings` names the recorded endpoint, bucket, and key prefix. Restore
the effective `s3.private_endpoint`, `s3.private_bucket`, and
`s3.private_key_prefix` settings to that location before restarting. Once a
current server starts, use a managed transition for the intended move. Keep both
buckets until you have checked for objects written during the mismatch and
reconciled them. Do not clear or replace the recorded identity to bypass startup.

## Profile avatars and private files

Profile avatars, diagnostic bundles, and catalog export files use the private S3
bucket when one is configured. Otherwise a local install keeps them on local
disk. The public artwork bucket never receives them, so an S3 install needs a
private bucket for avatar uploads.
