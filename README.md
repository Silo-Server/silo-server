<p align="center">
  <img src="assets/icon.png" alt="Silo logo" width="112" height="112">
</p>

<h1 align="center">Silo</h1>

<p align="center">
  <strong>Your media. Your server. Your way.</strong>
</p>

<p align="center">
  A modern, self-hosted media server for movies, shows, music, and books.
</p>

<p align="center">
  <a href="https://github.com/Silo-Server/silo-server/releases"><img alt="Latest GitHub release" src="https://img.shields.io/github/v/release/Silo-Server/silo-server?include_prereleases&amp;sort=semver&amp;display_name=tag&amp;style=flat-square&amp;label=release"></a>
  <a href="https://github.com/Silo-Server/silo-server/actions/workflows/ci.yml"><img alt="Continuous integration" src="https://img.shields.io/github/actions/workflow/status/Silo-Server/silo-server/ci.yml?branch=main&amp;style=flat-square&amp;label=CI"></a>
  <img alt="Go 1.26" src="https://img.shields.io/badge/Go-1.26-00ADD8?style=flat-square&amp;logo=go&amp;logoColor=white">
  <img alt="React 19" src="https://img.shields.io/badge/React-19-149ECA?style=flat-square&amp;logo=react&amp;logoColor=white">
  <a href="LICENSE"><img alt="AGPL-3.0-or-later license" src="https://img.shields.io/badge/license-AGPL--3.0--or--later-555555?style=flat-square"></a>
</p>

<p align="center">
  <a href="#deploy-with-docker-recommended">Installation</a>
  · <a href="docs/wiki/index.md">Documentation</a>
  · <a href="https://github.com/Silo-Server/silo-server/releases">Releases</a>
  · <a href="https://discord.com/invite/4RxuUQAEnW">Discord</a>
  · <a href="https://github.com/sponsors/quick104">Donate</a>
  · <a href="CONTRIBUTING.md">Contributing</a>
</p>

---

## Product overview

Silo keeps your media and its metadata under your control while providing a
modern experience at home or away.

<table>
  <tr>
    <td width="33%" valign="top">
      <strong>Play</strong><br><br>
      Direct play when possible, remux when needed, or use hardware-accelerated
      transcoding—including NVENC—automatically for each device.
    </td>
    <td width="33%" valign="top">
      <strong>Explore</strong><br><br>
      Organize movies, shows, music, and books with plugin-driven matching and
      enrichment from providers such as TMDB and TVDB.
    </td>
    <td width="33%" valign="top">
      <strong>Connect</strong><br><br>
      Use the included web app or optional Jellyfin/Emby-compatible clients such
      as VidHub, Findroid, and Infuse.
    </td>
  </tr>
  <tr>
    <td width="33%" valign="top">
      <strong>Share</strong><br><br>
      Give household members their own profiles, watch state, and parental
      controls without giving up ownership of the server.
    </td>
    <td width="33%" valign="top">
      <strong>Deploy</strong><br><br>
      Start the integrated stack with Docker Compose, then manage libraries,
      users, providers, and playback settings in the admin UI.
    </td>
    <td width="33%" valign="top">
      <strong>Scale</strong><br><br>
      Keep everything on one host or separate API, proxy, and transcode roles
      across a shared PostgreSQL and Redis deployment.
    </td>
  </tr>
</table>

## Releases and updates

Silo uses [Semantic Versioning](https://semver.org/) in the form
`vMAJOR.MINOR.PATCH`, with suffixes such as `-alpha.1` for prereleases and
optional `+build.7` metadata. The
[GitHub Releases](https://github.com/Silo-Server/silo-server/releases) page is
the canonical public history of shipped changes, with categorized notes,
contributors, and a full comparison for every version.

> [!IMPORTANT]
> Until the maintainers select and publish Silo's first release, builds remain
> identified by their commit SHA. No version is implied by this documentation.

For every release, review the notes for configuration, compatibility, and
upgrade information before updating. See
[Release versioning](docs/release-versioning.md) for the version contract and
maintainer release process.

## Deploy with Docker (recommended)

The easiest way to run Silo is with Docker Compose 2.24 or newer. The default stack assumes you do
not already have PostgreSQL and Redis available, so it bundles PostgreSQL, Redis, FFmpeg, and the
application for a one-command start.

<table>
  <tr>
    <td width="33%" valign="top">
      <strong>Default stack</strong><br><br>
      One integrated Silo server with bundled PostgreSQL and Redis. This is the
      recommended starting point.
    </td>
    <td width="33%" valign="top">
      <strong>GPU acceleration</strong><br><br>
      Add <a href="#optional-intelamd-va-api-or-intel-quick-sync">VA-API / Quick Sync</a>
      or <a href="#optional-nvidianvenc">NVIDIA NVENC</a> only when the host supports it.
    </td>
    <td width="33%" valign="top">
      <strong>Distributed deployment</strong><br><br>
      Separate API, proxy, and transcode roles when one integrated host is no
      longer the right fit. See the <a href="#distributed-examples">examples</a>.
    </td>
  </tr>
</table>

1. **Create a `.env` file**

   ```sh
   cp .env.example .env
   printf '\nPOSTGRES_PASSWORD=%s\nSECRET_KEY=%s\n' \
     "$(openssl rand -hex 24)" "$(openssl rand -base64 48)" >> .env
   ```

   This replaces the development database password from `.env.example` and creates the key Silo
   uses to encrypt stored credentials. Back up `.env` separately from PostgreSQL; losing
   `SECRET_KEY` makes those credentials unrecoverable.

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

   ### Optional Intel/AMD VA-API or Intel Quick Sync

   The default stack is CPU-only so it starts on hosts without `/dev/dri`. On a Linux host with
   `/dev/dri`, enable the device overlay:

   ```sh
   docker compose -f docker-compose.yml -f docker-compose.vaapi.yml up -d
   ```

   To make that the default for this installation, set:

   ```dotenv
   COMPOSE_FILE=docker-compose.yml:docker-compose.vaapi.yml
   ```

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
- `/opt/silo/plugins`
- `/opt/silo/compat`
- `/opt/silo/transcode`
- `/opt/silo/catalog-seeds`

The optional `search` profile also stores its index under `/opt/silo/meilisearch`.

Media is mounted into the container at `/mnt/media` from the host path you set in `MEDIA_ROOT`.

### Optional Search Profile

PostgreSQL full-text search works without any optional services. Meilisearch is available when you
want its search provider:

| Profile | Command | Description |
|---|---|---|
| default | `docker compose up -d` | Integrated server plus bundled PostgreSQL and Redis |
| `search` | `docker compose --profile search up -d` | Add the optional Meilisearch service |

Before starting the `search` profile, set `MEILI_MASTER_KEY` in `.env` to the output of
`openssl rand -hex 32`. After Silo starts, choose Meilisearch under **Admin > Settings > Search**,
set the URL to `http://meilisearch:7700`, enter the same key as the API key, test the connection,
and save. Restart Silo, then rebuild the catalog search index from the same page. Silo continues
to use PostgreSQL full-text search until you select Meilisearch.

### Distributed Examples

The main Compose file includes commented proxy and transcode service examples. Most single-host
installs should leave them commented because the integrated service already includes proxying and
transcoding.

Multi-host operators can use those examples as a starting point for a dedicated worker Compose
file connected to the deployment's shared PostgreSQL and Redis services.

Proxy nodes serve source downloads from the same absolute media paths used by direct playback.
Prepared-download work can also run on transcode nodes. Each selected transcode node retains its
result on node-local storage and exposes it only through Silo's authenticated internal artifact API;
the paired proxy relays those bytes, so no shared artifact mount is required. Dedicated transcode
nodes default to retaining prepared downloads in a protected directory inside the transcode volume
captured at process startup. `download.artifact_dir` overrides that location for both dedicated
transcode nodes and the integrated/API-local fallback, so mount the configured path on every process
that prepares downloads. Changing either artifact-path setting requires a restart. Downloads with a
configured server-wide or per-user bandwidth limit remain API-local so those aggregate limits stay exact.
Clients discover distributed delivery through `proxy_delivery` on the download capability response.
When it is true, they may opt into `GET` or `HEAD /api/v1/downloads/{id}/file-proxy` and
`/api/v1/direct-download-proxy`; those routes may return a temporary redirect to a proxy node. The
established `/file` and `/direct-download` routes keep serving bytes directly with their existing
status-code contract; for a node-local prepared artifact, the API itself performs the authenticated
relay on that fallback route.

### Deployment Notes

The default compose stack intentionally bundles PostgreSQL and Redis for ease of setup and assumes a fresh install without those services already available. If you already operate PostgreSQL and Redis, omit those examples from compose and point Silo at your existing infrastructure instead. For serious installs, PostgreSQL is better on a separate VM or a managed service so upgrades, tuning, and backups are isolated from the app host. Redis can stay local for many installs, but externalizing it is also reasonable if you already operate shared infrastructure.

Silo is externally stateful by default rather than fully stateless. Durable application state lives in PostgreSQL. Redis only stores coordination and cache-style data. Silo still writes transient transcode output locally under `/tmp/silo-transcode`. If you switch `userdb.backend=sqlite`, Silo also becomes locally stateful at `/var/lib/silo/userdb`.

Migrating an existing Continuum Docker install should be done with the preflight
helper and cutover guide in [docs/continuum-to-silo-docker-migration.md](docs/continuum-to-silo-docker-migration.md).

## Configuration

Silo requires `DATABASE_URL` and `SECRET_KEY` when running from source or against external
infrastructure. In the default Docker Compose path, the stack wires the database and Redis URLs
for you. All other settings — libraries, metadata providers, transcoding, users — are managed
through the admin UI after first launch.

### Server Modes

| Mode | Description |
|---|---|
| `integrated` | Full server: API + frontend + scanner + transcode (default) |
| `api` | API server only, no local transcoding |
| `proxy` | Stream proxy node that connects to the shared deployment database and Redis |
| `transcode` | HLS and prepared-download worker node that connects to the shared deployment database and Redis |

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

1. **Install prerequisites**: Go 1.26.4+, Node.js 22+, pnpm 10.32.1, PostgreSQL 18 with pgvector, Redis, and FFmpeg.

2. **Configure the source process**

   ```sh
   cp .env.example .env
   printf '\nSECRET_KEY=%s\nDATABASE_URL=%s\nREDIS_URL=%s\n' \
     "$(openssl rand -base64 48)" \
     'postgres://silo:silo@localhost:5432/silo?sslmode=disable' \
     'redis://localhost:6379' >> .env
   ```

   Change the URLs when you use existing services instead of the bundled development defaults.

3. **Start PostgreSQL and Redis** (skip if you already have them running)

   ```sh
   docker compose up -d postgres redis
   ```

4. **Build and run**

   ```sh
   make build
   ./silo
   ```

   The server starts at `http://localhost:8080` by default. All other settings are configured through the admin UI.

## Documentation

| Start with | Use it for |
| --- | --- |
| [Documentation index](docs/wiki/index.md) | Feature guides, administration, deployment, playback, and troubleshooting |
| [Release versioning](docs/release-versioning.md) | Version selection, GitHub release notes, prereleases, and maintainer checks |
| [Development guide](DEVELOPMENT.md) | Local setup, builds, tests, migrations, and repository structure |
| [Canonical Settings API](docs/settings-api.md) | Client contracts, contextual headers, remote scopes, and effective reads |

## Reporting Issues

Client implementers can use the [Canonical Settings API guide](docs/settings-api.md)
for contract discovery, contextual headers, remote scopes, effective reads, and
the admin projection.

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

<p align="center">
  <a href="https://github.com/sponsors/quick104"><strong>Sponsor Silo</strong></a>
  · <a href="https://discord.com/invite/4RxuUQAEnW"><strong>Join the Discord community</strong></a>
</p>

## License & Trademarks

Silo's source code is licensed under the **GNU Affero General Public License
v3.0 or later** (`AGPL-3.0-or-later`) — see [LICENSE](LICENSE).

The **Silo name, logo, and wordmark are trademarks of Silo Media L.L.C.** and
are **not** covered by the AGPL. You're free to fork and redistribute the code,
but forks and redistributions must not use the Silo brand as their identity and
must remove or replace the brand assets. Publishing a Silo-branded app to an app
store requires written permission. See [TRADEMARK.md](TRADEMARK.md) for what's
permitted — including referential use like "compatible with Silo."
