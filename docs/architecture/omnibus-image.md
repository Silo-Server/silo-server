# Omnibus image

The omnibus image is the regular Silo image plus PostgreSQL, and optional Redis
and Meilisearch, in one container supervised by
[s6-overlay](https://github.com/just-containers/s6-overlay). It serves
installs that want one container instead of the Compose stack: a main server
with its own PostgreSQL, and proxy and transcode nodes that use it (see
[Nodes](#nodes)). Its source is `omnibus/`; `docker-compose.omnibus.yml` runs a
main server and `docker-compose.node.yml` a node.

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

1. `init-envfile` turns each `FILE__NAME=/path` variable into `NAME`. The path
   must resolve, outside `/config`, to a regular file that group and others
   cannot write and that no service account owns, in directories only root can
   change (sticky directories count); otherwise a service account could point
   it at `SECRET_KEY` and have root export that under another name. The service
   accounts are the four in the table below, with `silo` at the `PUID` that
   `init-adduser` applies next. Docker and Kubernetes secrets, which are
   root's, qualify, and so does a Compose file secret, which keeps its host
   owner, such as uid 1000, unless that uid is `PUID`.
2. `init-adduser` maps the `silo` account onto `PUID` and `PGID` and adds it to
   the groups of any passed-in GPU devices. A device owned by group 0 is
   skipped, so Silo, which never runs as root here, cannot open it. Unprivileged
   LXC containers present passed-through devices that way unless the host gives
   them a group (on Proxmox, `gid=` on the `devN` entry).
3. `init-config` lays out `/config`, decides which bundled services run, and
   exports the connection settings Silo reads for them: `DATABASE_URL`,
   `REDIS_URL`, and the search variables below. An explicit `DATABASE_URL` or
   `REDIS_URL` turns the matching bundled service off. With the bundled
   PostgreSQL it defaults `POSTGRES_TUNE` to `auto` with a 50% memory budget;
   a deployment's own database keeps Silo's default, tuning off. A proxy or
   transcode `MODE` turns them all off and requires both URLs.
4. `init-postgres` creates the cluster on first start and writes the image's
   settings and `pg_hba.conf` on every start; `svc-postgres` runs it;
   `init-postgres-db` creates the `silo` database and rebuilds
   collation-dependent indexes after a C library change. `svc-redis` and
   `svc-meilisearch` start alongside. A service that is off reports ready and
   sleeps, so the dependency graph stays fixed.
5. `svc-silo` runs Silo once the database exists and the enabled services
   are ready.

s6 stops services in reverse order, so Silo drains before PostgreSQL's fast
shutdown. `svc-silo` and `svc-postgres` have no stop timeout of their own; the
container's stop timeout bounds them.

`init-config` empties `/run/silo-omnibus` on every start, so no flag from a
previous start of the same container outlives its setting. The PostgreSQL
server holds a `flock(2)` lock on `/config/.postgres.lock` for its life; the
file is `root:postgres` 0660 in root's `/config`, so the `postgres` account
opens it read-write but cannot replace it. NFS emulates `flock(2)` with a POSIX
lock, and an exclusive one needs a writable descriptor. `postmaster.pid`
only guards servers in one PID namespace, so `svc-postgres` deletes a left-over
pid file only once it holds that lock, and a second container started on the
same `/config` refuses to start PostgreSQL. Bundled Redis keeps an append-only
file (`appendfsync everysec`), forced on the command line even with a
`redis.conf`: a snapshot alone can restore a playback session older than its
revocation. A host crash can still lose about a second of writes.

## Accounts and data

| Service | Account | uid:gid | Data |
| --- | --- | --- | --- |
| Silo | `silo` | `PUID`:`PGID` (911 by default) | `/config/silo`, linked from `/var/lib/silo` |
| PostgreSQL | `postgres` | 999:999 | `/config/postgres` |
| Redis | `redis` | 998:998 | `/config/redis` |
| Meilisearch | `meilisearch` | 997:997 | `/config/meilisearch` |

The service ids are fixed in `omnibus/Dockerfile` so the data directories keep
a valid owner across image updates. The init scripts run as root, and a service
account can plant a symlink anywhere in its own directory, so they never write,
chown, or chmod inside one as root: `/config` itself stays root's and holds the
image's markers, and files in a service directory are written as its account
(`write_as` in `lib.sh`), and read as its account too. Readiness checks run as
the service's account, since s6 starts them as root with `HOME` in the service
directory, where a `.curlrc` or similar file could direct a root process. The
`silo` role is the cluster superuser and can run programs as the `postgres`
account, so this matters for `/config/postgres` as much as for `/config/silo`. `init-config` hands `/config/silo` to the
`silo` account with a full walk only when its ids differ from the ones recorded
in `/config/.silo-owner` by the last walk, or when an entry in the top
two levels is owned wrong; local artwork can hold millions of files. Deleting
the marker forces a walk. The `silo` account's home is `/nonexistent`, because
`usermod -u` re-owns everything in the home and a `PUID` change would otherwise
walk `/config/silo` twice; the scripts set `HOME` for each service.

Silo reaches its state through `/var/lib/silo`, a link to `/config/silo`, so
paths stored in the database keep working. Local artwork storage is recorded by
its resolved root, so a catalog that stored artwork under another image's
`/var/lib/silo/artwork` resolves to `/config/silo/artwork` here; Silo
recognizes that as the same directory and keeps the recorded identity (see
[Storage identity](blob-storage.md#storage-identity)). `init-adduser` refuses a `PUID` that
another account holds: the kernel would report that account's name for Silo's
processes, and PostgreSQL's peer authentication maps the process's user name to
the database role.

PostgreSQL listens only on `/run/postgresql`. The `silo` role is the cluster's
superuser and authenticates by peer, so no database password exists. Silo needs
the superuser role for `POSTGRES_TUNE`, which uses `ALTER SYSTEM`, and its
migrations create the `vector`, `pg_trgm`, and `citext` extensions. The cluster
uses `en_US.UTF-8`, the locale of the `pgvector/pgvector` image in the Compose
stack. Each database records the C library collation version it was built
under, and an index on text sorted under another version can miss rows. On
every start `init-postgres-db` compares the recorded version with the running
one, and for each database that differs it rebuilds the indexes whose keys use
a libc collation and every partial or expression index (`pg_index` records no
collation for a predicate or expression), except vector indexes, then runs
`ALTER DATABASE ... REFRESH COLLATION VERSION`. That covers
a data directory from the Compose stack's image, which is Debian 12 (glibc
2.36) where this one has 2.41, and a later move of this image to a newer Debian
release. The rebuild runs before Silo starts, so a large catalog's first start
takes longer. A database that does not accept connections cannot be rebuilt,
so it keeps its recorded version and logs a warning on each start. `init-postgres` rewrites `conf.d/00-omnibus.conf` and
`pg_hba.conf` on every start and makes sure `postgresql.conf` includes
`conf.d`; `POSTGRES_TUNE` writes `postgresql.auto.conf`, which loads after it.
Rewriting `pg_hba.conf` matters for a data directory from the official
PostgreSQL image, whose file trusts every local connection.

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
`/config/.silo-search-provider`. While Meilisearch is off and the marker
exists, `init-config` exports `SILO_SEARCH_PROVIDER=postgres` on each start.
`svc-search-marker` deletes the marker once Silo answers `/api/v1/health`,
which it does only after storing that provider, so a start that fails earlier
keeps the marker and the next start tries again. Search returns to PostgreSQL,
and from the start after the marker is gone the administrator can choose a
provider again. Without a marker the stored provider
is left alone, so an external Meilisearch configured in the admin UI keeps
working.

## Nodes

The same image runs a proxy or transcode node. With `MODE=proxy` or
`MODE=transcode`, `init-config` requires `DATABASE_URL` and `REDIS_URL`, starts
none of the bundled services, and ignores `ENABLE_REDIS`, `ENABLE_MEILISEARCH`,
and `ENABLE_NODE_ACCESS` with a warning. Silo already keeps migrations and
`POSTGRES_TUNE` to the primary modes (`integrated`, `api`), so a node never
changes the shared database's schema or settings. `/config` holds the node's
own state. An administrator adds the node on the main server, which then checks
its health over `NODE_URL`.

A main server shares its bundled services with nodes when
`ENABLE_NODE_ACCESS=true`, which `docker-compose.node-access.yml` sets
alongside publishing ports 5432 and 6379 on `NODE_ACCESS_HOST`:

- PostgreSQL also listens on every interface (`conf.d/01-node-access.conf`),
  and `pg_hba.conf` gains `host silo silo all scram-sha-256`.
  `init-postgres-db` sets the `silo` role's password to `POSTGRES_PASSWORD` on
  every start, passing it to `psql` through the environment.
- Redis starts even without `ENABLE_REDIS`, since nodes need the Redis the main
  server uses. It binds every interface and requires `REDIS_PASSWORD`, which
  `init-config` writes to a file only the `redis` account and root read; the
  readiness check signs in from it. The password is limited to characters that
  need no escaping in a URL, because Silo's own `REDIS_URL` carries it.
- Silo on the main server still uses the socket and loopback.

Turning node access off removes the listener and the network rule, clears the
role's password, and puts Redis back on loopback. Nodes sign in as `silo`, the
cluster's superuser, as they did with the multi-container stack, and neither
connection is encrypted, so the ports belong on a trusted network only.
Reusing `POSTGRES_PASSWORD` means a main server moved from that stack keeps the
`DATABASE_URL` its nodes already have, as long as that stack ran with the
default `POSTGRES_USER=silo`; nodes that sign in as another user are refused
until their `DATABASE_URL` names `silo`. Their `REDIS_URL` gains the password.
