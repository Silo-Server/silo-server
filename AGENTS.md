# Repository Guidelines

## Project Structure & Module Organization
`cmd/silo` contains the main server entrypoint. Backend code lives in `internal/`, organized by domain (`api`, `catalog`, `metadata`, `playback`, `scanner`, `jellycompat`, etc.); keep new code in the package that owns the behavior instead of creating catch-all helpers. Database changes belong in `migrations/` as paired numbered `.up.sql` and `.down.sql` files. The React frontend lives in `web/src/`, with feature code split across `components/`, `pages/`, `hooks/`, `player/`, and `lib/`. Reference material belongs in `docs/architecture/` or `docs/superpowers/{specs,plans}/`; ad hoc SQL helpers live in `scripts/`.

When creating or editing `docs/superpowers/specs/` or `docs/superpowers/plans/`, never include local absolute filesystem paths or transient worktree IDs. Use repository-relative paths and wording like "Commands assume the repository root is the cwd."

This repository is a VERY EARLY WIP. Proposing sweeping changes that improve long-term maintainability is encouraged.


## Core Priorities

1. Performance first.
2. Reliability first.
3. Keep behavior predictable under load and during failures (session restarts, reconnects, partial streams).

If a tradeoff is required, choose correctness and robustness over short-term convenience.

## Maintainability

Long term maintainability is a core priority. If you add new functionality, first check if there is shared logic that can be extracted to a separate module. Duplicate logic across multiple files is a code smell and should be avoided. Don't be afraid to change existing code. Don't take shortcuts by just adding local logic to solve a problem.

This repository is part of a broader multi-repo Silo workspace. The sibling
repositories are usually checked out side-by-side in the same parent directory.

- `silo-server` owns the Go backend, web admin UI, API contracts, auth/session
  behavior, catalog/scanner/playback services, database migrations, Jellyfin
  compatibility, and host-side plugin runtime.
- `silo-android` owns the Android phone and TV clients. Client-visible API,
  auth, playback, session, library, and metadata changes may require Android
  follow-up.
- `silo-apple` owns the iOS, tvOS, and macOS clients. Client-visible API, auth,
  playback, session, library, and metadata changes may require Apple follow-up.

When changing server behavior consumed by clients, check whether both client
repos need model, routing, playback, or UX updates. Prefer coordinated
multi-repo changes over leaving one platform behind.

Do not assume all plugin-related code lives in this repo.

- `silo-plugin-sdk` owns the public plugin SDK, protobuf contracts, generated plugin API code, manifest helpers, and runtime bootstrap.
- `silo-plugins` owns the central plugin catalog / repository manifest.
- First-party plugins such as  `silo-plugin-tmdb`, and `silo-plugin-tvdb` live in their own repositories.
- `Silo` owns host-side plugin installation, runtime management, API handlers, and integration logic.

When a task mentions plugins, first determine whether the change belongs in this repo, the SDK repo, the catalog repo, or a specific plugin repo. Prefer coordinated multi-repo changes over forcing plugin work into `Silo`.

## Build, and Development Commands
Use the checked-in `Makefile` for the common paths:

- `make build`: install frontend deps, build `web/dist`, compile `./silo`
- `make dev-backend`: run the Go server in integrated mode
- `make dev-frontend`: start the Vite dev server with HMR
- `make dev-proxy` / `make dev-transcode`: run standalone worker modes
- `make lint`: run `golangci-lint` and frontend ESLint

Run before opening a merge request:

- `cd web && pnpm run lint`
- `cd web && pnpm run format:check`
- `make verify-local-paths`

For local services, start PostgreSQL and Redis with `docker compose up -d postgres redis`.

## Coding Style & Naming Conventions
Go code must stay `gofmt`/`goimports` clean and pass `golangci-lint`. Keep package names lowercase and focused; Frontend code is TypeScript with 2-space indentation, semicolons, double quotes, trailing commas, and a 100-character line width (`web/.prettierrc`). Use `PascalCase.tsx` for components/pages, `useThing.ts` for hooks, and keep shared utilities in `web/src/lib` or `web/src/utils`.


## Deployment Debugging

When troubleshooting a Silo deployment (container health, playback failures,
database state, log analysis, deploys), follow the runbook at
`.claude/skills/deployment-debugging/SKILL.md`. It includes step-by-step
procedures and a first-run setup that configures SSH, database, and Redis
connection details for your environment.

## Commit & Merge Request Guidelines
Recent history follows Conventional Commit-style subjects such as `feat(playback): add realtime session hub`, `fix(playback): ...`, and `docs: ...`. Keep commits scoped to one concern. For non-trivial work, open an issue or discussion first; this codebase moves quickly. Merge requests should explain the problem, why this approach was chosen, linked issue/spec/plan, risks or follow-up work, and AI-use disclosure. Include screenshots or recordings for UI changes.

## Learned User Preferences

- When implementing from an attached plan, do not edit the plan file itself.
- After feature work, user may invoke the `/simplify` skill to improve readability without changing behavior.

## Learned Workspace Facts

- Silo separates login accounts (`users`) from household profiles; multiple profiles on one account share the same `user_id`.
- Profile `is_primary` marks the household parent for that account; it is not the same as server-wide `admin` role on the user account.
- Point the Vite dev frontend at a remote backend with `VITE_API_PROXY_TARGET` in `web/.env.local` (or inline) when running `make dev-frontend`; the frontend calls relative `/api` URLs proxied by Vite.
