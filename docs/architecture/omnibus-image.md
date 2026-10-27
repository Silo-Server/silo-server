# Omnibus image

The omnibus image is the regular Silo image plus PostgreSQL, and optional Redis
and Meilisearch, in one container supervised by
[s6-overlay](https://github.com/just-containers/s6-overlay). It serves
single-node installs that want one container instead of the Compose stack, and
it always runs one integrated node. Deployments with proxy or transcode nodes
use the regular image. Its source is `omnibus/`, and `docker-compose.omnibus.yml`
runs it.

## Build

The `omnibus` job in `.github/workflows/docker.yml` runs after the regular
image's manifest list is pushed. It builds `omnibus/Dockerfile` with
`SILO_IMAGE` set to that commit's regular image and publishes the regular tags
with an `-omnibus` suffix.

Because the omnibus image starts from the regular one, both run the same Silo
binary against the same libvips, FFmpeg build, GPU runtimes, and Node.js. Keep
it that way: a runtime dependency Silo needs belongs in the root `Dockerfile`,
and `omnibus/Dockerfile` adds only the bundled services and s6. The base changes
on every build, so the omnibus build uses no layer cache and always installs
current PostgreSQL and Redis packages.

Two versions are load-bearing for existing installs:

- `MEILISEARCH_VERSION` matches the `meilisearch` service in
  `docker-compose.yml`. Meilisearch refuses data written by another version, so
  a bump needs `MEILI_UPGRADE_DB=true` for one start in both deployments.
- `PG_MAJOR` is the PostgreSQL major version. `init-postgres` refuses to open a
  data directory written by another major version, so a bump makes every
  existing omnibus install dump and restore. Treat it as a breaking change and
  call it out in the release notes and the user manual.

## Start and stop

The s6-rc services under `omnibus/rootfs/etc/s6-overlay/s6-rc.d` start in this
order:

1. `init-envfile` turns each `FILE__NAME=/path` variable into `NAME`.
2. `init-adduser` maps the `silo` account onto `PUID` and `PGID` and adds it to
   the groups of any passed-in GPU devices.
3. `init-config` lays out `/config`, decides which bundled services run, and
   exports the connection settings Silo reads for them: `DATABASE_URL`,
   `REDIS_URL`, and the search variables below. An explicit `DATABASE_URL` or
   `REDIS_URL` turns the matching bundled service off.
4. `init-postgres` creates the cluster on first start; `svc-postgres` runs it;
   `init-postgres-db` creates the `silo` database. `svc-redis` and
   `svc-meilisearch` start alongside. A service that is off reports ready and
   sleeps, so the dependency graph stays fixed.
5. `svc-silo` runs Silo once the database exists and the enabled services
   are ready.

s6 stops services in reverse order, so Silo drains before PostgreSQL's fast
shutdown. `svc-silo` and `svc-postgres` have no stop timeout of their own; the
container's stop timeout bounds them.

## Accounts and data

| Service | Account | uid:gid | Data |
| --- | --- | --- | --- |
| Silo | `silo` | `PUID`:`PGID` (911 by default) | `/config/silo`, linked from `/var/lib/silo` |
| PostgreSQL | `postgres` | 999:999 | `/config/postgres` |
| Redis | `redis` | 998:998 | `/config/redis` |
| Meilisearch | `meilisearch` | 997:997 | `/config/meilisearch` |

The service ids are fixed in `omnibus/Dockerfile` so the data directories keep
a valid owner across image updates. `init-adduser` refuses a `PUID` that
another account holds: the kernel would report that account's name for Silo's
processes, and PostgreSQL's peer authentication maps the process's user name to
the database role.

PostgreSQL listens only on `/run/postgresql`. The `silo` role is the cluster's
superuser and authenticates by peer, so no database password exists. Silo needs
the superuser role for `POSTGRES_TUNE`, which uses `ALTER SYSTEM`, and its
migrations create the `vector`, `pg_trgm`, and `citext` extensions. The cluster
uses `en_US.UTF-8`, the locale of the `pgvector/pgvector` image in the Compose
stack. That image's glibc can differ from this one's, so a data directory moved
between them reports a collation version mismatch and needs its text indexes
rebuilt; moving an install between the two goes through `pg_dump` and
`pg_restore` instead. `init-postgres` rewrites `conf.d/00-omnibus.conf` on
every start and makes sure `postgresql.conf` includes `conf.d`;
`POSTGRES_TUNE` writes `postgresql.auto.conf`, which loads after it.

## Search settings

Meilisearch search keeps its paging sessions in Redis, and without Redis the
v2 search capabilities and paged searches answer 503
`search_continuation_unavailable`. So `ENABLE_MEILISEARCH=true`, or a
deployment's own `SILO_SEARCH_PROVIDER=meilisearch`, also starts the bundled
Redis unless `REDIS_URL` points elsewhere.

With `ENABLE_MEILISEARCH=true`, `init-config` exports `SILO_SEARCH_PROVIDER`,
`SILO_MEILISEARCH_URL`, and `SILO_MEILISEARCH_API_KEY` for the bundled instance,
leaving any of the three the deployment already sets. Silo validates them,
stores them on every start, with the API key encrypted, and the admin UI shows
them as set by the environment
([admin settings API](../admin-settings-api.md)).

When the image chose the provider, it leaves a marker,
`/config/meilisearch/silo-search-provider`. On the first start with Meilisearch
off again, a marker makes `init-config` export `SILO_SEARCH_PROVIDER=postgres`
and delete it. Search returns to PostgreSQL, and from the next start the
administrator can choose a provider again. Without a marker the stored provider
is left alone, so an external Meilisearch configured in the admin UI keeps
working.
