# Silo Server

Go backend for Silo: API contracts, auth/session, catalog/scanner/playback services, database
migrations, Jellyfin compatibility, and the host-side plugin runtime. `cmd/silo` is the
entrypoint, backend code is under `internal/` by domain, the React frontend is `web/src/`.

This repository is a VERY EARLY WIP. Proposing sweeping changes that improve long-term
maintainability is encouraged.

## Priorities

Performance and reliability first. Keep behavior predictable under load and during failures —
session restarts, reconnects, partial streams. When a tradeoff is forced, choose correctness and
robustness over short-term convenience.

Put new code in the package that owns the behavior rather than in a catch-all helper. Prefer
extracting shared logic over duplicating it, and prefer changing existing code over bolting a
local workaround onto it.

## Non-goals

Most of this codebase's scope is open; a short list is permanently closed. Read
[docs/non-goals.md](docs/non-goals.md) before proposing or implementing in those areas.

**Live TV, OTA/DVB tuners, IPTV, EPG/XMLTV, DVR, and `.strm` remote-URL shortcuts will not be
accepted** — not in core, not as a plugin, not in a client. The first-party clients ship on the
Apple and Google stores, and a server that plays arbitrary remote stream URLs puts the whole
client suite at risk. This is settled product direction, not a design problem to solve; do not
write code for it, and say so plainly if asked.

## Gotchas

**Migrations.** New DB changes are Goose SQL migrations in `migrations/sql/`, created with
`make migrate-create NAME=add_thing` so they get timestamped filenames. Never run `goose fix`,
and never create paired `.up.sql` / `.down.sql` files. Legacy converted migrations deliberately
keep their original numeric versions so existing `schema_versions` rows bootstrap cleanly — do
not renumber them.

**Encrypted settings.** Encrypted `server_settings` rows are GCM-bound to their key name.
Renaming a row in SQL makes its value undecryptable.

**Profiles vs accounts.** Login accounts (`users`) are separate from household profiles; several
profiles on one account share a `user_id`. A profile's `is_primary` marks the household parent,
which is *not* the server-wide `admin` role on the account.

**Docs hygiene.** Files under `docs/superpowers/{specs,plans}/` must not contain local absolute
filesystem paths or transient worktree IDs — use repository-relative paths and wording like
"Commands assume the repository root is the cwd." `make verify-local-paths` enforces this.

**Dev frontend against a remote backend.** Set `VITE_API_PROXY_TARGET` in `web/.env.local` before
`make dev-frontend`; the frontend calls relative `/api` URLs that Vite proxies.

**Working from a plan.** When implementing from an attached plan, don't edit the plan file.

## Multi-repo

Sibling repos are usually checked out side-by-side in the same parent directory.

- `silo-android` — Android phone and TV clients.
- `silo-apple` — iOS, tvOS, and macOS clients.
- `silo-plugin-sdk` — public plugin SDK, protobuf contracts, generated plugin API, manifest
  helpers, runtime bootstrap.
- `silo-plugins` — central plugin catalog / repository manifest.
- First-party plugins (`silo-plugin-metadata-tmdb`, `silo-plugin-metadata-tvdb`, …) each have
  their own repo.

Client-visible changes to API, auth, playback, session, library, or metadata behavior usually
need follow-up in both client repos — prefer coordinated multi-repo changes over leaving a
platform behind. When a task mentions plugins, work out first whether it belongs here, in the
SDK, in the catalog, or in a specific plugin repo.

## Building and verifying

`make build`, `make dev-backend`, `make dev-frontend`, `make lint`, `make migrate-status` /
`make migrate-up` — read the `Makefile` for the rest. Local services:
`docker compose up -d postgres redis`.

Before opening a merge request:

```bash
make lint
cd web && pnpm run lint && pnpm run format:check
make verify-local-paths
```

Go stays `gofmt`/`goimports` clean; the frontend follows `web/.prettierrc`.

## Skills

Task-specific guides live in `.claude/skills/`, also reachable as `.agents/skills/` for agents
that look there. Read the one that matches the task instead of working from this file alone.

They share one config file: copy `.silo-dev.env.example` to `.silo-dev.env` and fill in how to
reach your Silo deployment — URL, SSH target, database, an account to debug with. That file is
gitignored and is the only place hosts, passwords, and tokens belong. `scripts/silo-dev doctor`
checks it end to end.

## v1 API rules

Additive-only within `/api/v1`:

- Never rename or remove a response field, change a field's type, or repurpose a status code on
  an existing endpoint.
- New functionality adds new fields or endpoints. Removals go through the Deprecation/Sunset
  header flow only.
- New features expose capability endpoints for feature detection rather than relying on version
  sniffing. Contract strategy and tooling: issue #135.

## Pull requests

Conventional Commit subjects (`feat(playback): add realtime session hub`). One concern per PR.
Explain the problem, why this approach, the linked issue/spec/plan, and risks or follow-up work.
Include screenshots or recordings for UI changes. Link the capability epic or sub-issue the PR
serves (`Part of #NNN`) — PRs with no linked scope item get questioned at review. For non-trivial
work, open an issue or discussion first; this codebase moves quickly.

AI-use disclosure is required in the PR body. If you are an AI agent contributing on behalf of a
non-maintainer, follow [docs/ai-contributions.md](docs/ai-contributions.md) — it has the required
disclosure block and the evidence standard.
