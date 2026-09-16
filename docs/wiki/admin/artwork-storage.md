# Artwork storage

In **Settings → Infrastructure → Artwork storage**, choose Automatic, Local
disk, or S3. Automatic uses the public S3 bucket when one is configured and local
disk otherwise. Changes require a server restart.

Local disk suits a single server. Persist the displayed artwork directory in
Docker; it contains provider caches and uploads. S3 is recommended when multiple
hosts share a catalog, so every host can read the same artwork.

Provider artwork caching is enabled by default on new installations and works
with either backend. The setup wizard can finish without configuring S3.

## The backend is fixed once artwork is stored

Choose the backend during setup, or before the first library scan. The first
artwork write records where artwork lives, and after that the backend selector
is locked in the settings UI and the API rejects a change. Silo does not move
artwork between backends.

If you must move anyway, treat it as a manual migration: stop the server, copy
the artwork tree to the new store keeping the same keys, save the new backend
setting directly, delete the `artwork.storage_identity` row from
`server_settings`, and restart. The server refuses to start if the configured
storage differs from the recorded one, including a different bucket or local
directory. Clearing the record alone does not move files.

Profile avatars remain in private S3 when configured. Otherwise they use local
storage; the public artwork bucket cannot receive avatar uploads.
