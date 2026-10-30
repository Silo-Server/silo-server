# Omnibus image

The omnibus image is the regular Silo image plus PostgreSQL, and optional Redis
and Meilisearch, in one container supervised by
[s6-overlay](https://github.com/just-containers/s6-overlay). It serves
installs that want one container instead of the Compose stack: a main server
with its own PostgreSQL, and proxy and transcode nodes that use it (see
[Nodes](#nodes)). Its source is `omnibus/`; `docker-compose.yml` runs a
main server, or a node when `.env` sets `MODE`, `DATABASE_URL`, `REDIS_URL`,
and `NODE_URL`. Hardware transcoding and node access are commented out in it.
`docker-compose.split.yml` is the earlier multi-container file, kept for
installs that stay on that layout; it runs the regular image.

## Build

The `omnibus` job in `.github/workflows/docker.yml` runs after the regular
image's manifest list is pushed. It builds `omnibus/Dockerfile` with
`SILO_IMAGE` set to that commit's regular image and publishes the regular tags
with an `-aio` suffix (`build-N-aio`, `<full SHA>-aio`, `apiv2-aio`). In place
of `latest` it moves `aio`; it takes over `latest` only once validated for
every install, in a later change.

Because the omnibus image starts from the regular one, both run the same Silo
binary against the same libvips, FFmpeg build, GPU runtimes, and Node.js. Keep
it that way: a runtime dependency Silo needs belongs in the root `Dockerfile`,
and `omnibus/Dockerfile` adds only the bundled services and s6. The base changes
on every build, so the omnibus build uses no layer cache and always installs
current PostgreSQL and Redis packages.

Two versions are load-bearing for existing installs:

- `MEILISEARCH_VERSION` pins the bundled Meilisearch. Meilisearch refuses data
  written by another version, so a bump needs `MEILI_UPGRADE_DB=true` for one
  start of each install that enabled it.
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
   are ready. It passes a granted NET_ADMIN to `silo` as an ambient capability
   through `setpriv`, so Silo's startup helper can configure client sockets.
   If `setpriv` fails, including when the ambient raise is refused, `svc-silo`
   starts Silo through `s6-setuidgid` without the grant, so a refusal never
   keeps Silo from starting. Silo then holds only the bounding bit and enables
   `no_new_privs` to contain its child processes.

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
uses `en_US.UTF-8`, the locale of the `pgvector/pgvector` image in the earlier
multi-container Compose stack. Each database records the C library collation version it was built
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
`MODE=transcode`, `init-config` requires `DATABASE_URL`, `REDIS_URL`, and
`NODE_URL` (without it Silo guesses a URL from its listen port, and two nodes
would share session keys), starts none of the bundled services, and ignores `ENABLE_REDIS`, `ENABLE_MEILISEARCH`,
and `ENABLE_NODE_ACCESS` with a warning. Silo already keeps migrations and
`POSTGRES_TUNE` to the primary modes (`integrated`, `api`), so a node never
changes the shared database's schema or settings. `/config` holds the node's
own state. An administrator adds the node on the main server, which then checks
its health over `NODE_URL`.

A main server shares its bundled services with nodes when `.env` sets
`ENABLE_NODE_ACCESS=true` and the two commented port lines in
`docker-compose.yml` are uncommented to publish 5432 and 6379 on
`NODE_ACCESS_HOST`:

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

## Moving from the multi-container stack

Before the omnibus image, `docker-compose.yml` ran PostgreSQL
(`pgvector/pgvector:pg18`), Redis, and Meilisearch as services of their own,
with data in `SILO_DATA_ROOT/postgres`, `redis`, and `meilisearch`, and Silo's
state in `plugins`, `artwork`, and `compat`. That file is now
`docker-compose.split.yml`, and an install of that layout keeps it by
selecting it in `COMPOSE_FILE`.

`docker-compose.yml` is an example of a new install and does not look for an
earlier one. Started on its `SILO_DATA_ROOT`, it replaces the earlier `silo`
container and creates a new, empty bundled database, with first-run setup open
on the published ports. The earlier `postgres`, `redis`, and `meilisearch`
containers keep running as orphans of the Compose project, their data
untouched; selecting `docker-compose.split.yml` and running `docker compose up
-d` again brings the install back.

Moving an install into the single container is a separate step that an
administrator runs once: `/etc/silo-omnibus/migrate-compose-stack`, with the
earlier stack stopped. With `.env` still holding the earlier stack's
settings, and `COMPOSE_FILE` removed from it once the stack is down:

```sh
docker compose -f docker-compose.split.yml down
docker compose run --rm -v <SILO_DATA_ROOT>:/silo-data \
  --entrypoint /etc/silo-omnibus/migrate-compose-stack silo
docker compose up -d
```

`docker compose run` gives the script the `silo` service's `.env` and `PUID`,
and the script works within the one mount so it can hard-link:

- Silo's three directories are hard-linked into `config/silo`, or copied when
  hard links fail across file systems. This happens with an external
  `DATABASE_URL` too. Silo replaces files rather than rewriting them, so the
  earlier stack keeps its own copy of any file Silo changes afterwards;
  ownership is shared between the two, which the root-run earlier image
  ignores.
- The cluster in `postgres/18/docker` is copied to `config/postgres/data`. A
  temporary server on a private socket creates the `silo` superuser role if
  `POSTGRES_USER` named another, and renames `POSTGRES_DB` to `silo`. When that
  was `postgres`, it creates an empty `postgres` database again, which
  `init-postgres-db` connects to.
  `init-postgres` then replaces the copy's `pg_hba.conf`, which trusted every
  local connection, on its first start, and `init-postgres-db` rebuilds the
  collation-dependent indexes, since the copy was built under glibc 2.36.
- Redis and Meilisearch data stay behind. Redis holds nothing a single node
  cannot rebuild, and Silo builds a new index when `ENABLE_MEILISEARCH` is
  turned on. The Compose file still starts the bundled Redis unless `.env`
  sets `ENABLE_REDIS=false`, as the earlier stack always ran one: Silo offers
  the Watch Party room socket only with Redis. The image's own default stays
  off, since a node ignores the setting and warns when it is on. A search
  provider the admin UI pointed at the old `meilisearch` service goes back to
  `postgres` in the copy, unless `ENABLE_MEILISEARCH` is on, in which case
  `init-config` hands Silo the bundled instance.

It reports nothing to move when the install was already moved, or when an
omnibus install has a database of its own and none of the earlier stack is
here. It refuses, before anything is moved into place, when:

- No earlier install is in `SILO_DATA_ROOT`: its data may be in named volumes
  or other paths, which have to move there first.
- A pinned `SILO_IMAGE` (other than `latest` or `aio`) is set
  without `SILO_OMNIBUS_IMAGE`: this file would otherwise follow
  `aio` past the pin.
- The omnibus install has a database of its own, no migration finished, and
  the earlier stack's cluster or Silo directories are here: Silo created that
  database first, most likely when `docker-compose.yml` started before the move.
  Copying over it would drop what it holds, and going on would leave the
  earlier install behind unannounced. The message says to move
  `config/postgres/data` aside and start again if it holds nothing needed, or
  to delete the earlier stack's directories to keep it.
- An earlier install is here and `.env` holds a `DATABASE_URL` or `REDIS_URL`
  naming the old services or `localhost` (the old file overrode them for Silo,
  and this one passes `.env` through), or a `SILO_MEILISEARCH_URL` naming the
  old `meilisearch` service (Silo applies it on every start, and the image keeps
  it even with its own Meilisearch on).
- An earlier install is here, `.env` sets `SILO_SEARCH_PROVIDER=meilisearch`
  without `SILO_MEILISEARCH_URL`, and `ENABLE_MEILISEARCH` is off: the URL
  stored from the admin UI would apply, and it names the old service.
- `plugins`, `artwork`, or `compat` in `SILO_DATA_ROOT` is a symbolic link,
  which the earlier stack's bind mount followed but this mount cannot, or is
  not a directory.
- The earlier install's directories exist without a cluster and `DATABASE_URL`
  is unset, or `.env` sets a `DATABASE_URL` beside the earlier stack's cluster:
  the earlier file set `DATABASE_URL` for Silo itself, so that cluster is the
  database Silo used, and moving on would point Silo at another one.
- The old cluster's `PG_VERSION` is a link or holds no version number, it has
  a `postmaster.pid` (a server may still have it open), was written by another
  PostgreSQL major version, or keeps its WAL or a tablespace outside the data
  directory.
- There is no room for the copy plus 2 GiB, or, once copied, for rebuilding
  its largest collation-dependent index twice over (the index and its WAL) plus
  1 GiB, or for copying a Silo directory that cannot be hard-linked while
  keeping that room for the database's first start.
- The copy's search uses a Meilisearch chosen in the admin UI, `.env` sets no
  `SILO_SEARCH_PROVIDER`, and nothing turns Redis on (`ENABLE_REDIS`,
  `ENABLE_MEILISEARCH`, node access, or a `REDIS_URL`): its paging sessions need
  the Redis the earlier stack always ran.
- The copy has enabled proxy or transcode nodes and node access is off: they
  reached the old services on published ports.

How it stays safe to repeat and to roll back:

- It never changes the old directories' contents, so the earlier Compose file
  still starts on them. Deleting them is the administrator's job.
- The database copy and Silo's directories are one generation, built under
  staging names that no later run promotes. Moving them into place happens
  under `/config/.compose-migration-pending`; a run that finds it without the
  done marker below sets aside whatever was already moved
  (`.<name>.before-<time>`) and starts over, since the old install may have
  changed since. A run that finds both markers, left by a kill just after the
  done marker was written, only removes the pending record. A data directory
  an unfinished `initdb` left is set aside the same way. A failure removes
  everything staged, and the closing log lists every copy set aside. While the
  pending record exists, `init-postgres` refuses to start, whatever the
  environment and even with an external database: Silo writing to a
  half-moved cluster or Silo directory that the next run sets aside would
  leave those writes behind.
- `cp` does not flush, and PostgreSQL never syncs a cleanly stopped cluster it
  starts on, so the script syncs the file system after staging and writing the
  pending record, after the moves, and after the done marker. Otherwise a power
  loss could keep the renames and lose the files.
- The copy's `pg_hba.conf` is removed and rewritten as `postgres`, never written
  through, and its `PG_VERSION` is read without following a link, since files in
  a cluster from another container can be links.
- The done marker, `/config/.compose-cluster-copied`, is written to a
  temporary name and renamed into place once everything else is, so it is
  whole or absent. It records the source cluster's system identifier and latest
  checkpoint location (`pg_controldata`, read in the C locale; a cluster it
  cannot read stops the script), or `none` when Silo's database is
  elsewhere. Any later start of the old cluster, even a clean start and stop,
  changes the checkpoint, so the script refuses to go on once the earlier
  Compose file has been used since the copy: starting on the copy would drop
  what it wrote. It checks the same before moving a copy into place.
- Going back to `docker-compose.split.yml` after a move leaves the copy
  stale. Before using `docker-compose.yml` again, move `config` aside and run
  the move again; a run on the stale copy refuses (the done marker above).
- Silo directories that `init-config` made, empty, before the move are
  removed instead of set aside. A database created beside the old install
  has to be moved aside by hand (above).

A `docker-compose.override.yml` that still changes the old `postgres`,
`redis`, or `meilisearch` services makes Compose reject `docker-compose.yml`,
which no longer has them. Such overrides have to go before the update.

Remove this script once installs of the multi-container stack no longer move
to the single container.
