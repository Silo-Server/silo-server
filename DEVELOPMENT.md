# Developing Silo

This document covers building, running, and contributing to the Silo server. If you just want to run Silo, see the [README](README.md).

See [CONTRIBUTING.md](CONTRIBUTING.md) for contribution expectations, merge request guidance, and the policy for AI-assisted submissions.

## Prerequisites

- **Go** 1.24+
- **Bun** 1.0+
- **PostgreSQL** 18+
- **FFmpeg** (for transcoding support)

## Local Development

Local development remains intentionally separate from the deploy-oriented compose setup. Use [docker-compose.yml](docker-compose.yml) for local services and the source-build workflow below.

```sh
# Start local PostgreSQL and Redis
docker compose up -d postgres redis

# Run the frontend dev server (hot reload, proxies API to :8090)
make dev-frontend

# Run the Go backend
make dev-backend
```

The main compose file expects `MEDIA_ROOT` to be set even if you only want the bundled PostgreSQL and Redis services, so set that in `.env` first (`cp .env.example .env`).

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
# Go tests (uses testcontainers — Docker must be running)
go test ./...

# Frontend tests
cd web && bun test
```

## Linting

```sh
# Go
golangci-lint run

# Frontend
cd web && bun run lint
cd web && bun run format:check
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
