# Silo

A self-hosted media streaming server with a React frontend and Go backend. Supports direct play, remuxing, and hardware-accelerated transcoding. Compatible with Jellyfin/Emby clients (VidHub, Findroid) via a built-in compatibility layer.

Join the community on [Discord](https://discord.gg/4RxuUQAEnW).

## Deploy with Docker (recommended)

The easiest way to run Silo is with Docker Compose. The default stack assumes you do not already have PostgreSQL and Redis available, so it bundles PostgreSQL, Redis, FFmpeg, and the application for a one-command start.

1. **Create a `.env` file**

   ```sh
   cp .env.example .env
   ```

2. **Set your media path**

   Edit `.env` and set:

   ```dotenv
   MEDIA_ROOT=/path/to/your/media
   ```

   `MEDIA_ROOT` is the one value most users need to change. You can also override `SILO_DATA_ROOT` if you do not want bind mounts under `/opt/silo`, and change ports if the defaults conflict with something else on the host.

3. **Start the default integrated stack**

   ```sh
   docker compose up -d
   ```

   This starts PostgreSQL, Redis, and the integrated Silo server. The app is available at `http://localhost:8090` and the Jellyfin-compatible endpoint at `http://localhost:8096`.

   If you already have PostgreSQL and Redis available, omit those bundled service examples from compose and point Silo at your existing `DATABASE_URL` and `REDIS_URL` instead.

4. **Configure through the admin UI**

   Add libraries, users, metadata providers, and playback settings from the web interface.

### Bind Mount Layout

The deploy-oriented compose files use host folder mappings rather than Docker-managed volumes.

By default, data is stored under `/opt/silo`:

- `/opt/silo/postgres`
- `/opt/silo/redis`
- `/opt/silo/transcode`
- `/opt/silo/catalog-seeds`

Media is mounted into the container at `/mnt/media` from the host path you set in `MEDIA_ROOT`.

### Optional Profiles

The main compose file is integrated-first. These profiles exist for operators testing distributed mode or mirroring a split deployment shape. Most single-host installs should stay on the default integrated service, because it already includes proxying and transcoding.

| Profile | Command | Description |
|---|---|---|
| default | `docker compose up -d` | Integrated server plus bundled PostgreSQL and Redis |
| `proxy` | `docker compose --profile proxy up -d` | Start a standalone proxy service for distributed-mode testing |
| `transcode` | `docker compose --profile transcode up -d` | Start a standalone transcode service for distributed-mode testing |

You can enable both optional examples together:

```sh
docker compose --profile proxy --profile transcode up -d
```

If you are splitting workers across multiple hosts, use the separate remote worker example instead of trying to stretch the main compose file across machines.

### Advanced Remote Node Example

For a dedicated remote transcode worker, use [docker-compose.remote-transcode.yml](docker-compose.remote-transcode.yml). That file is intended for a separate worker host that connects back to an existing Silo deployment using shared PostgreSQL and Redis.

### Deployment Notes

The default compose stack intentionally bundles PostgreSQL and Redis for ease of setup and assumes a fresh install without those services already available. If you already operate PostgreSQL and Redis, omit those examples from compose and point Silo at your existing infrastructure instead. For serious installs, PostgreSQL is better on a separate VM or a managed service so upgrades, tuning, and backups are isolated from the app host. Redis can stay local for many installs, but externalizing it is also reasonable if you already operate shared infrastructure.

Silo is externally stateful by default rather than fully stateless. Durable application state lives in PostgreSQL. Redis only stores coordination and cache-style data. Silo still writes transient transcode output locally under `/tmp/silo-transcode`. If you switch `userdb.backend=sqlite`, Silo also becomes locally stateful at `/var/lib/silo/userdb`.

Migrating an existing Continuum Docker install should be done with the preflight
helper and cutover guide in [docs/continuum-to-silo-docker-migration.md](docs/continuum-to-silo-docker-migration.md).

## Build from Source

### Prerequisites

- **Go** 1.24+
- **Bun** 1.0+
- **PostgreSQL** 18+
- **FFmpeg** (for transcoding support)

### Quick Start

1. **Start PostgreSQL** (skip if you already have one running)

   ```sh
   docker compose up -d postgres redis
   ```

   The main compose file still expects `MEDIA_ROOT` to be set even if you only want the bundled PostgreSQL and Redis services, so set that in `.env` first.

2. **Configure the database connection**

   ```sh
   cp .env.example .env
   ```

   Edit `.env` and set `DATABASE_URL` to point to your PostgreSQL instance.

3. **Build and run**

   ```sh
   make build
   ./silo
   ```

   The server starts at `http://localhost:8080` by default. All other settings are configured through the admin UI.

## Development

Local development remains intentionally separate from the deploy-oriented compose setup. Use [docker-compose.yml](docker-compose.yml) and the existing source-build workflow for local development.

```sh
# Run the frontend dev server (hot reload, proxies API to :8090)
make dev-frontend

# Run the Go backend
make dev-backend
```

If you are developing `Silo` and `silo-plugin-sdk` together, keep using the local [`go.work`](go.work) workspace. That workspace is a developer convenience only. CI and release builds run with `GOWORK=off`, so any new SDK helper used here must be pushed and tagged in `silo-plugin-sdk` before this repo can merge or release the change.

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution expectations, merge request guidance, and the policy for AI-assisted submissions.

Plugin authors should start with [docs/architecture/plugin-development.md](docs/architecture/plugin-development.md), which covers the RPC plugin package format, generated proto workflow, SDK import paths, route and asset exposure, and auth or user-config integration points.

## Reporting Issues

If you are reporting a bug, install problem, or performance issue, start with the admin workflow and reproduction steps, not Claude/Codex analysis.

Please include:

- What you were trying to do
- Exact steps you took
- What you expected to happen
- What actually happened
- What exact action is slow or broken (`save`, `scan`, `browse`, `import`, `playback`, etc.)
- Whether it happens every time or only sometimes
- The library, media type, filter, setting, or value involved
- Version, branch, commit, and deployment details if you know them
- Screenshots, recordings, or log snippets if relevant

If you used Claude/Codex for debugging, put that under `Technical notes` at the end. Suspected files, SQL output, stack traces, and root-cause theories can be helpful, but only after the workflow and repro steps are clear.

Use this template:

```text
Goal:
Steps:
Expected:
Actual:
What is slow/broken:
Scope:
Version/branch:
Deployment:
Technical notes:
```

### Make Targets

| Target | Description |
|---|---|
| `make build` | Build frontend + Go binary |
| `make frontend` | Build frontend only |
| `make dev-frontend` | Vite dev server with HMR |
| `make dev-backend` | Run Go backend (integrated mode) |
| `make dev-proxy` | Run a standalone proxy node |
| `make dev-transcode` | Run a standalone transcode node |
| `make migrate-create NAME=add_thing` | Create a timestamped Goose SQL migration |
| `make migrate-validate` | Validate Goose migration files without touching a database |
| `make migrate-status` | Show Goose migration status using Silo's bootstrapping runner |
| `make migrate-up` | Apply pending Goose migrations using Silo's bootstrapping runner |
| `make clean` | Remove build artifacts |

### Database Migrations

PostgreSQL schema migrations are managed by Goose. Migration SQL files live in
`migrations/sql/` and use Goose annotations. Converted legacy migrations keep
their original numeric versions so existing `schema_versions` rows can bootstrap
cleanly into Goose without replaying old SQL. New migrations should be created
with timestamped filenames:

```sh
make migrate-create NAME=add_thing
make migrate-validate
```

Do not run `goose fix`; timestamped migrations are the repository policy because
they avoid version collisions across parallel PRs. The existing `001`-style
files are historical compatibility records, not the naming pattern for new work.
Runtime migrations are applied by the integrated/API server only. Proxy and
transcode modes never mutate schema.
For existing installs, use `make migrate-status` and `make migrate-up` rather
than invoking the Goose CLI directly; those targets copy legacy
`schema_versions` rows into `public.goose_db_version` under the migration lock
before reading or applying migrations. Set `ENV_FILE=path/to/.env` when the
database URL should be read from a non-default env file.

### Running Tests

```sh
# Go tests (uses testcontainers — Docker must be running)
go test ./...

# Frontend tests
cd web && bun test
```

### Linting

```sh
# Go
golangci-lint run

# Frontend
cd web && bun run lint
cd web && bun run format:check
```

## License

Silo is licensed under `AGPL-3.0-or-later`. See [LICENSE](LICENSE).

## Configuration

Silo requires only a `DATABASE_URL` when running from source or against external infrastructure. In the default Docker Compose path, the stack wires the database and Redis URLs for you. All other settings — libraries, metadata providers, transcoding, users — are managed through the admin UI after first launch.

### Server Modes

| Mode | Description |
|---|---|
| `integrated` | Full server: API + frontend + scanner + transcode (default) |
| `api` | API server only, no local transcoding |
| `proxy` | Stream proxy node that connects to the shared deployment database and Redis |
| `transcode` | HLS transcode worker node that connects to the shared deployment database and Redis |

## PostgreSQL Auto-Tuning

The default Docker Compose stack does not require a checked-in `postgresql.conf`.
It enables Silo's [pgtune](https://github.com/le0pard/pgtune)-style OLTP tuning
by default:

```yaml
POSTGRES_TUNE: auto
```

When enabled, Silo connects with `DATABASE_URL` and applies recommendations with
`ALTER SYSTEM`, which writes to PostgreSQL's `postgresql.auto.conf` inside the
database data directory. Reloadable settings are applied immediately with
`pg_reload_conf()`. Settings that PostgreSQL marks as restart-only are written
too, and Silo logs the setting names so you can restart PostgreSQL once:

```sh
docker compose restart postgres
```

The default Compose database user has the required PostgreSQL permissions. If
you use an external PostgreSQL server, make sure the configured `DATABASE_URL`
user can run `ALTER SYSTEM`, or set `POSTGRES_TUNE=off` and manage
PostgreSQL yourself.

For `POSTGRES_TUNE_MEMORY=auto`, Silo uses the first trustworthy memory source:
a finite Docker cgroup limit, the read-only `/host/proc/meminfo` mount supplied
by the bundled Compose file, then `/proc/meminfo` with container safety guards.
Auto-detected memory is treated as a PostgreSQL budget, defaulting to 75% of
detected RAM so Silo, Redis, plugins, transcodes, and the OS retain headroom.
`POSTGRES_TUNE_DB_SIZE=auto` queries `pg_database_size(current_database())` and
classifies the workload by comparing the database size to that memory budget.

Optional tuning overrides:

| Variable | Default | Description |
|---|---:|---|
| `POSTGRES_TUNE_PROFILE` | `oltp` | Tuning profile. Only `oltp` is currently supported. |
| `POSTGRES_TUNE_MEMORY` | `auto` | Server/container RAM, such as `8GB` or `32GB`; explicit values are used as-is. |
| `POSTGRES_TUNE_MEMORY_BUDGET_PERCENT` | `75` | Percent of auto-detected RAM used for PostgreSQL recommendations. |
| `POSTGRES_TUNE_CPUS` | `auto` | CPU count used for worker recommendations. |
| `POSTGRES_TUNE_STORAGE` | `ssd` | One of `hdd`, `ssd`, `san`, or `nvme`. |
| `POSTGRES_TUNE_DB_SIZE` | `auto` | Use `less_ram` when the database comfortably fits in RAM, `mid_ram`, or `greater_ram` for very large databases. |
| `POSTGRES_TUNE_CONNECTIONS` | `100` | PostgreSQL `max_connections`; automatically raised if Silo's app pool is configured higher. |
| `POSTGRES_SHM_SIZE` | `8gb` | Docker `/dev/shm` size for the bundled PostgreSQL container. |

Advanced operators can still supply their own PostgreSQL configuration or
override these env vars. Set `POSTGRES_TUNE=off` when you do not want Silo to
change PostgreSQL server settings. Settings already written with `ALTER SYSTEM`
remain in `postgresql.auto.conf`; reset those PostgreSQL parameters if you later
move fully to a custom `postgresql.conf`.

## Project Structure

```
cmd/silo/       Entry point
internal/
  api/               HTTP router, handlers, middleware
  auth/              JWT authentication and sessions
  catalog/           Media item, episode, season repositories
  config/            YAML + env var configuration
  jellycompat/       Jellyfin/Emby protocol compatibility
  metadata/          Plugin-driven metadata matching and enrichment
  playback/          Direct play, remux, transcode session management
  scanner/           Media file discovery and FFProbe
  worker/            Background jobs (scan, match, reconcile)
web/                 React + TypeScript frontend (Vite, Tailwind, shadcn/ui)
migrations/sql/      Goose-managed PostgreSQL schema migrations
```
