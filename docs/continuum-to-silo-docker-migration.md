# Continuum to Silo Docker Migration

This guide is for admins who already run a Docker Compose based Continuum install
and want to switch that host to the Silo repository without rebuilding their
library from scratch.

## Best Practice

Treat the rename as a controlled cutover, not an in-place edit while the old app
is running:

1. Take a host snapshot or a PostgreSQL dump.
2. Run the migration helper in read-only `check` mode.
3. Fix `.env` until the check output matches the old deployment.
4. Run the cutover during a quiet window.
5. Verify health, readiness, media paths, users, and plugins before deleting any
   old compatibility symlink or backup.

The migration helper is intentionally conservative. Its default mode is
read-only, and mutating modes require `--apply`.

## Prepare `.env`

From a Silo checkout:

```sh
cp .env.example .env
```

Set these values before migrating:

```dotenv
MEDIA_ROOT=/host/path/to/media
MEDIA_CONTAINER_ROOT=/old/container/path/to/media
SILO_DATA_ROOT=/opt/silo
POSTGRES_USER=continuum
POSTGRES_PASSWORD=continuum
POSTGRES_DB=continuum
```

`MEDIA_CONTAINER_ROOT` matters for existing installs. New Silo installs can use
`/mnt/media`, but migrated libraries may already store absolute paths from the
old container. Preserve the old in-container path here if existing library paths
use it.

If your old database used different PostgreSQL credentials, keep those exact
values. Do not switch to the Silo defaults unless you are starting with a fresh
database.

## Run The Preflight

```sh
scripts/migrate-continuum-docker.sh check
```

Common overrides:

```sh
CONTINUUM_DATA_ROOT=/srv/continuum \
SILO_DATA_ROOT=/srv/silo \
scripts/migrate-continuum-docker.sh check
```

If the old stack does not use obvious `continuum-*` container names, point the
script at the old compose file:

```sh
CONTINUUM_COMPOSE_FILE=/path/to/old/docker-compose.yml \
CONTINUUM_ENV_FILE=/path/to/old/.env \
scripts/migrate-continuum-docker.sh check
```

## Cut Over

After reviewing the preflight output:

```sh
sudo scripts/migrate-continuum-docker.sh migrate --apply
```

The migration mode:

- creates a `pg_dump` backup unless `--skip-db-dump` is provided
- stops the old Continuum compose stack or detected `continuum-*` containers
- moves `CONTINUUM_DATA_ROOT` to `SILO_DATA_ROOT`
- leaves a compatibility symlink from the old data root to the new one unless
  `--no-compat-symlink` is provided
- starts the Silo compose stack
- updates plugin cache paths from `/var/lib/continuum/plugins` to
  `/var/lib/silo/plugins`
- disables old `continuum.*` plugin installations and the old plugin catalog

Old Continuum plugin binaries are disabled because they may not be compatible
with the Silo plugin runtime. Install Silo-native plugin packages from the admin
UI after the server is healthy.

## Verify

```sh
docker compose ps
curl -fsS http://localhost:8090/api/v1/health
curl -fsS http://localhost:8090/api/v1/ready
docker compose logs --tail=200 silo
```

`/api/v1/health` and `/api/v1/ready` are retained operational probes. They keep
their paths after the `/api/v1` contract is retired, so existing probe
configuration keeps working.

In the admin UI, verify:

- admin login works
- libraries point at paths visible inside the Silo container
- users and profiles are present
- metadata plugins are installed and provider chains are configured
- playback starts for at least one direct-play item and one transcode/remux item

## DB Fix Only

If you already moved data and started Silo manually, run just the compatibility
SQL against the current Silo compose stack:

```sh
scripts/migrate-continuum-docker.sh db-fix --apply
```

## Rollback

Prefer restoring the host snapshot. If no snapshot is available, stop Silo,
restore the PostgreSQL dump created under the migration backup directory, and
move the data root back to the old location. Keep the backup until you have
verified library scans, metadata refreshes, and playback under Silo.
