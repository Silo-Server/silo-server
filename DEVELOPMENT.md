# Developing Silo

This document covers building, running, and contributing to the Silo server. If you just want to run Silo, see the [README](README.md).

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution expectations, merge request guidance, and the policy for AI-assisted submissions.

## Prerequisites

- **Go** 1.26.4+
- **Node.js** 22+ with **pnpm** 10.32.1
- **PostgreSQL** 18 with pgvector
- **Redis**
- **FFmpeg** (for transcoding support)

## Local Development

Local development remains intentionally separate from the deploy-oriented compose setup. Use [docker-compose.yml](docker-compose.yml) for local services and the source-build workflow below.

```sh
# Create the local bootstrap configuration
cp .env.example .env
printf '\nSECRET_KEY=%s\nDATABASE_URL=%s\nREDIS_URL=%s\n' \
  "$(openssl rand -base64 48)" \
  'postgres://silo:silo@localhost:5432/silo?sslmode=disable' \
  'redis://localhost:6379' >> .env

# Start local PostgreSQL and Redis
docker compose up -d postgres redis

# Run the frontend dev server (hot reload, proxies API to :8090)
make dev-frontend

# Run the Go backend
make dev-backend
```

The template supplies a non-empty `MEDIA_ROOT` because Compose validates the whole file even when
you start only PostgreSQL and Redis. Change it before testing libraries against real media.

If you are developing `Silo` and `silo-plugin-sdk` together, keep using the local [`go.work`](go.work) workspace. That workspace is a developer convenience only. CI and release builds run with `GOWORK=off`, so any new SDK helper used here must be pushed and tagged in `silo-plugin-sdk` before this repo can merge or release the change.

Plugin authors should start with [docs/architecture/plugin-development.md](docs/architecture/plugin-development.md), which covers the RPC plugin package format, generated proto workflow, SDK import paths, route and asset exposure, and auth or user-config integration points.

## Make Targets

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

## Database Migrations

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

## Running Tests

```sh
# Go tests
go test ./...

# Frontend tests
cd web && pnpm test
```

### Database-backed Go tests

Tests that need PostgreSQL read `SILO_TEST_DATABASE_URL` and **skip themselves when
it is unset**. `go test` prints nothing for a skip, so a run without it is green
while several hundred tests never execute — point it at a migrated database to
actually run them:

```sh
docker compose up -d postgres
go run ./cmd/silo/ --migrate-only    # reads DATABASE_URL from .env

# .env is read by the server, not by your shell, so name the database again
# here. This is the URL the Local Development step above wrote into .env.
export SILO_TEST_DATABASE_URL='postgres://silo:silo@localhost:5432/silo?sslmode=disable'
go test ./...

# Confirm they really ran rather than skipped — this should print nothing:
go test ./... -count=1 -v 2>&1 | grep 'SILO_TEST_DATABASE_URL is not set'
```

The database must be `pgvector/pgvector:pg18` or equivalent — the migrations
`CREATE EXTENSION vector`, `citext` and `pg_trgm`, and stock `postgres` images
ship only the last two. A few tests provision a throwaway database of their own
on the same server (see `internal/dbtest`); they drop it on exit, but a test
killed with SIGKILL can leave one behind.

## Linting

```sh
# Go
golangci-lint run

# Frontend
cd web && pnpm run lint
cd web && pnpm run format:check
```

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
