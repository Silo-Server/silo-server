# Silo

Silo is a self-hosted media streaming server for your movies, shows, music, and books. Point it at your media folders and stream to your devices — at home or away — with direct play, remuxing, and hardware-accelerated transcoding handled automatically.

Join the community on [Discord](https://discord.gg/4RxuUQAEnW). If Silo is useful to you, consider [sponsoring the project](https://github.com/sponsors/quick104) — see [Supporting Silo](#supporting-silo).

## Highlights

- **Plays your media, your way** — direct play when the device supports it, remux or hardware-accelerated transcode (including NVENC) when it doesn't.
- **Web app included** — a full-featured web client and admin interface ship with the server.
- **Works with apps you already use** — optional Jellyfin/Emby-compatible API supports clients such as VidHub, Findroid, and Infuse.
- **Household profiles** — multiple profiles per account, with per-profile watch state and parental controls.
- **Plugin-driven metadata** — match and enrich your libraries with providers like TMDB and TVDB, installed as plugins.
- **Fast setup** — one `docker compose up -d` brings up the whole stack; everything else is configured in the admin UI.

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

   This starts PostgreSQL, Redis, and the integrated Silo server. The app is available at `http://localhost:8090`. Jellyfin-compatible app support is disabled until an administrator enables it in onboarding or admin settings.

   If you already have PostgreSQL and Redis available, omit those bundled service examples from compose and point Silo at your existing `DATABASE_URL` and `REDIS_URL` instead.

   ### Optional NVIDIA/NVENC

   GPU support is kept out of the default compose file so hosts without NVIDIA drivers work unchanged.

   Install the NVIDIA Container Toolkit and use a Docker Compose version with GPU reservation support before enabling this override.

   Use the optional override file when you want NVENC:

   ```sh
   docker compose -f docker-compose.yml -f docker-compose.nvidia.yml up -d
   ```

   If you want this controlled from `.env`, set `COMPOSE_FILE`:

   ```dotenv
   COMPOSE_FILE=docker-compose.yml:docker-compose.nvidia.yml
   NVIDIA_GPU_COUNT=1
   ```

   Windows uses `;` instead of `:` between compose files.

   Then `docker compose up -d` will include the NVIDIA override automatically.

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

## Configuration

Silo requires only a `DATABASE_URL` when running from source or against external infrastructure. In the default Docker Compose path, the stack wires the database and Redis URLs for you. All other settings — libraries, metadata providers, transcoding, users — are managed through the admin UI after first launch.

### Server Modes

| Mode | Description |
|---|---|
| `integrated` | Full server: API + frontend + scanner + transcode (default) |
| `api` | API server only, no local transcoding |
| `proxy` | Stream proxy node that connects to the shared deployment database and Redis |
| `transcode` | HLS transcode worker node that connects to the shared deployment database and Redis |

### PostgreSQL Auto-Tuning

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

## Build from Source

If you prefer running Silo without Docker:

1. **Install prerequisites**: Go 1.24+, Bun 1.0+, PostgreSQL 18+, and FFmpeg.

2. **Start PostgreSQL and Redis** (skip if you already have them running)

   ```sh
   docker compose up -d postgres redis
   ```

   The main compose file still expects `MEDIA_ROOT` to be set even if you only want the bundled PostgreSQL and Redis services, so set that in `.env` first.

3. **Configure the database connection**

   ```sh
   cp .env.example .env
   ```

   Edit `.env` and set `DATABASE_URL` to point to your PostgreSQL instance.

4. **Build and run**

   ```sh
   make build
   ./silo
   ```

   The server starts at `http://localhost:8080` by default. All other settings are configured through the admin UI.

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

## Contributing & Development

Silo is open source and contributions are welcome. See [DEVELOPMENT.md](DEVELOPMENT.md) for building from source in a dev workflow, running tests, database migrations, and project layout, and [CONTRIBUTING.md](CONTRIBUTING.md) for contribution expectations, merge request guidance, and the policy for AI-assisted submissions.

## Supporting Silo

Silo is an open-source hobby project, developed in spare time and funded out of pocket. If you'd like to support development, you can sponsor via [GitHub Sponsors](https://github.com/sponsors/quick104).

Donations go directly toward the costs of building and running the project:

- AI development tooling subscriptions (Claude, Codex) used to build and maintain Silo
- Push notification relay infrastructure
- Future development costs

Sponsoring is entirely optional — Silo is and will remain free and open source. Bug reports, contributions, and feedback are just as valuable.

## License & Trademarks

Silo's source code is licensed under the **GNU Affero General Public License
v3.0 or later** (`AGPL-3.0-or-later`) — see [LICENSE](LICENSE).

The **Silo name, logo, and wordmark are trademarks of Silo Media L.L.C.** and
are **not** covered by the AGPL. You're free to fork and redistribute the code,
but forks and redistributions must not use the Silo brand as their identity and
must remove or replace the brand assets. Publishing a Silo-branded app to an app
store requires written permission. See [TRADEMARK.md](TRADEMARK.md) for what's
permitted — including referential use like "compatible with Silo."
