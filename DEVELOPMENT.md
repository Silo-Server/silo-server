# Developing Silo

How to build, run, and test the Silo server from source. To run Silo without
building it, see the [README](README.md). Contribution rules and the
pre-submission gate are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Prerequisites

- Git, Make, and OpenSSL
- Docker Engine or Docker Desktop with Docker Compose 2.24+ (local services and testcontainers)
- Go 1.26.4+
- Node.js 22+ with pnpm 10.32.1
- PostgreSQL 18 with pgvector
- Redis
- FFmpeg (transcoding). On macOS, install Homebrew's keg-only full build so
  text-subtitle burn-in and the complete filter set are available alongside
  VideoToolbox: `brew install ffmpeg-full`. Silo discovers the Apple Silicon
  and Intel keg paths automatically when the FFmpeg Path setting is blank; a
  custom build can be selected explicitly in Admin Settings.
- A C compiler and build toolchain (CGO dependencies)
- pkg-config and the libvips development headers (image processing through bimg)

## Local development

Source builds use [docker-compose.yml](docker-compose.yml) only for PostgreSQL
and Redis; the deploy-oriented stack in the README is separate.

```sh
# Create the local bootstrap configuration
cp .env.example .env
chmod 600 .env
printf '\nSECRET_KEY=%s\nDATABASE_URL=%s\nREDIS_URL=%s\n' \
  "$(openssl rand -base64 48)" \
  'postgres://silo:silo@localhost:5432/silo?sslmode=disable' \
  'redis://localhost:6379' >> .env

# Start local PostgreSQL and Redis
docker compose up -d postgres redis

# Install frontend dependencies and create the embedded-frontend test stub
cd web
pnpm install --frozen-lockfile
cd ..
make embed-stub
```

Run the backend and frontend in separate terminals, backend first:

```sh
make dev-backend
```

The source-built backend listens on `:8080`, while the Vite proxy defaults to
the Compose port `8090`. Point it at the source backend in `web/.env.local`:

```dotenv
VITE_API_PROXY_TARGET=http://localhost:8080
```

Then:

```sh
make dev-frontend
```

`.env.example` ships a non-empty `MEDIA_ROOT` because Compose validates the
whole file even when you only start PostgreSQL and Redis. Change it before
testing libraries against real media.

### Working on the plugin SDK at the same time

If a change spans Silo and `silo-plugin-sdk`, use an untracked local `go.work`
workspace. `go.work` and `go.work.sum` are gitignored developer conveniences: CI
runs from a clean checkout without them, and release builds set `GOWORK=off`.
Any SDK package or symbol this repository uses must therefore be pushed and
tagged in `silo-plugin-sdk` before the change here can merge.

Plugin authors should start in the `silo-plugin-sdk` repository, usually checked
out beside this one. It owns the plugin package format, protobuf contracts,
generated plugin API, import paths, and manifest helpers.

## Build and run from source

With `.env` created and PostgreSQL and Redis running:

```sh
make build
./silo
```

The server listens at <http://localhost:8080>. Complete onboarding and manage
the remaining settings in the web interface.

## Make targets

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

## Database migrations

Goose manages the PostgreSQL schema. Migration SQL lives in `migrations/sql/`
with Goose annotations. Create new migrations with timestamped filenames:

```sh
make migrate-create NAME=add_thing
make migrate-validate
```

Never run `goose fix`. Timestamped names avoid version collisions across
parallel PRs; the `001`-style files are converted legacy migrations that keep
their original numbers so existing `schema_versions` rows bootstrap into Goose
without replaying old SQL. Do not renumber them.

Only the integrated/API server applies migrations at runtime. Proxy and
transcode modes never touch the schema.

For existing installs, use `make migrate-status` and `make migrate-up` rather
than the Goose CLI: those targets copy legacy `schema_versions` rows into
`public.goose_db_version` under the migration lock before reading or applying
anything. Set `ENV_FILE=path/to/.env` to read the database URL from a different
env file.

## Tests and lint

While iterating:

```sh
go test ./internal/<package>/...                 # needs Docker for testcontainers
cd web && pnpm exec vitest run path/to/test.tsx
```

The full pre-submission gate (build, format, vet, lint, both test suites, and
the verify targets) is listed once, in
[CONTRIBUTING.md](CONTRIBUTING.md#validate-your-change).

## Project structure

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
