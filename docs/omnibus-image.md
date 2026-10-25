# Omnibus image

The omnibus image runs Silo and PostgreSQL in one container, with Redis and
Meilisearch bundled but off until asked for. It is published beside the regular
image under the same tags with an `-omnibus` suffix:
`ghcr.io/silo-server/silo-server:latest-omnibus`, `build-N-omnibus`, and
`<commit>-omnibus`. The regular image and `docker-compose.yml` are unchanged;
pick whichever fits the deployment.

The image is Ubuntu 24.04 with [s6-overlay](https://github.com/just-containers/s6-overlay)
as init. The Silo binary is copied from the regular image of the same build, so
both images run the same Silo. PostgreSQL 18 with pgvector comes from the
PostgreSQL apt repository, Redis from Ubuntu, and Meilisearch from its GitHub
release, pinned to the same version as `docker-compose.yml`.

## Quick start

```sh
mkdir -p /opt/silo/config
docker run -d --name silo \
  --stop-timeout 120 \
  -e PUID=1000 -e PGID=1000 \
  -e SECRET_KEY="$(openssl rand -base64 48)" \
  -p 8090:8080 -p 8096:8096 -p 13378:13378 \
  -v /opt/silo/config:/config \
  -v /srv/media:/mnt/media:ro \
  ghcr.io/silo-server/silo-server:latest-omnibus
```

Keep the `SECRET_KEY` you generated. It encrypts stored credentials, and a lost
key makes them unrecoverable. `docker-compose.omnibus.yml` is the same
deployment as a Compose file.

## Processes and users

Nothing in the container serves traffic as root. s6 starts each service under
its own account and the init steps run as root only long enough to prepare
directories and permissions.

| Service | User | Data | Listens on |
| --- | --- | --- | --- |
| Silo | `silo` (PUID/PGID) | `/config/silo` | 8080, 8096, 13378 |
| PostgreSQL 18 | `postgres` | `/config/postgres` | Unix socket only |
| Redis (optional) | `redis` | `/config/redis` | 127.0.0.1:6379 |
| Meilisearch (optional) | `meilisearch` | `/config/meilisearch` | 127.0.0.1:7700 |

`PUID` and `PGID` set the uid and gid of the `silo` user, so give them the
owner of your media. They default to 911. The `silo` user is also added to the
`video` and `render` groups and to the group owning any `/dev/dri` or
`/dev/nvidia*` device passed into the container, so GPU transcoding works the
same way as with the regular image.

Silo's own defaults are unchanged. `/var/lib/silo` is a link to `/config/silo`,
so artwork, plugins, and the Jellyfin Web compatibility files land there. The
transcode directory and prepared downloads stay under `/tmp` by default; mount
`/tmp/silo-transcode` and `/tmp/silo-download-artifacts` to keep them across
container recreation, or change them in Settings.

## Environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `SECRET_KEY` | required | Master key for at-rest credential encryption, 32 characters or more. |
| `PUID`, `PGID` | `911` | uid and gid of the `silo` user. |
| `TZ` | unset | Time zone for logs and schedules. |
| `ENABLE_REDIS` | `false` | Start the bundled Redis and point Silo at it. |
| `ENABLE_MEILISEARCH` | `false` | Start the bundled Meilisearch and make it the catalog search provider. |
| `MEILI_MASTER_KEY` | generated | Meilisearch master key. Without it one is generated on first start and kept in `/config/meilisearch/master-key`. |
| `DATABASE_URL` | bundled | Set to use an external PostgreSQL. The bundled server does not start. |
| `REDIS_URL` | bundled or unset | Set to use an external Redis. The bundled server does not start even with `ENABLE_REDIS=true`. |
| `POSTGRES_TUNE` | `auto` | Silo tunes the bundled PostgreSQL through `ALTER SYSTEM` as it does for the Compose stack. |
| `POSTGRES_TUNE_MEMORY_BUDGET_PERCENT` | `50` | Share of the container's memory handed to PostgreSQL. Lower than the regular default because Silo shares the container. |
| `FILE__<NAME>` | unset | Read `<NAME>` from the file at the given path, for example `FILE__SECRET_KEY=/run/secrets/silo_secret_key`. |

Every other Silo variable from the regular image applies, and any `MEILI_*`
variable reaches Meilisearch unchanged. `MODE` must stay `integrated`; the
omnibus image runs one node.

Silo also reads an optional `/config/silo/.env` at start, the same file the
regular image reads from its working directory.

## Redis

Redis is optional for a single node. Without it Silo keeps caches and rate
limits in memory and the event bus stays in-process. Set `ENABLE_REDIS=true`
to start the bundled server. It binds to the loopback interface and snapshots
to `/config/redis`. A `/config/redis/redis.conf`, when present, replaces the
built-in command line entirely.

## Meilisearch

Set `ENABLE_MEILISEARCH=true`. On the next start the image starts Meilisearch,
then writes three of Silo's settings before Silo itself starts:
`catalog.search.provider` becomes `meilisearch`, and the Meilisearch URL and
API key point at the bundled instance. Silo picks the change up at start,
encrypts the stored key, and its search index task builds the index on its own.
Search works on Meilisearch from the first request after startup, with no
admin steps.

Setting `ENABLE_MEILISEARCH=false` again switches the provider back to
`postgres` on the next start, but only if it still points at the bundled
instance. A provider pointed at an external Meilisearch is left alone.

Meilisearch refuses to open data written by another version. When this image
moves to a new Meilisearch version, set `MEILI_UPGRADE_DB=true` for one start
so it upgrades `/config/meilisearch/data` in place, then remove the variable.

## PostgreSQL

The cluster is created on first start in `/config/postgres/data` with
`en_US.UTF-8`, the same locale as the `pgvector/pgvector` image used by
`docker-compose.yml`. The superuser is `silo`, authenticated by Unix peer
authentication, which is why no database password exists or is needed. Nothing
listens on TCP.

To reach the database from the host:

```sh
docker exec -u silo silo psql -h /run/postgresql silo
docker exec -u silo silo pg_dump -h /run/postgresql -Fc silo > silo.dump
```

The image writes `/config/postgres/data/conf.d/00-omnibus.conf` on every
start. Put your own settings in another file in that directory, for example
`10-local.conf`; files load in name order. Silo's `POSTGRES_TUNE` writes to
`postgresql.auto.conf`, which loads after all of them. Settings that need a
restart, such as `shared_buffers`, take effect the next time the container
starts.

### Major version upgrades

The image refuses to start when `/config/postgres/data` was written by a
different PostgreSQL major version, and says so in the log. Upgrade by dump and
restore:

1. Start the previous image and dump the database with the `pg_dump` command
   above.
2. Stop the container and move `/config/postgres/data` aside.
3. Start the new image. It creates an empty cluster and Silo runs its
   migrations against it.
4. Restore the dump into it:

   ```sh
   docker exec -i -u silo silo pg_restore -h /run/postgresql -d silo --clean --if-exists < silo.dump
   ```

5. Restart the container so Silo reloads its settings from the restored data.

## Moving from the Compose stack

Dump the database from the `postgres` service, start the omnibus container
once so it creates its cluster, restore with the `pg_restore` command above,
and copy the contents of the old `artwork`, `plugins`, and `compat` bind
mounts into `/config/silo/`. Use the same `SECRET_KEY`. Fix ownership with
`chown -R <PUID>:<PGID> /config/silo` on the host if the files came from a
different uid.

## Stopping

Silo stops first and drains in-flight work, then PostgreSQL shuts down in fast
mode, then the rest. Give the container enough time: `--stop-timeout 120` for
`docker run`, or `stop_grace_period: 2m` in Compose, as the example file sets.
