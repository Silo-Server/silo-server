# Silo Server

Go backend for Silo: API contracts, auth/session, catalog/scanner/playback services, database
migrations, Jellyfin compatibility, and the host-side plugin runtime. `cmd/silo` is the
entrypoint, backend code is under `internal/` by domain, the React frontend is `web/src/`.

This repository is a VERY EARLY WIP. Proposing sweeping changes that improve long-term
maintainability is encouraged.

## What Silo is

A modern, open-source media server built from the ground up on current infrastructure —
Postgres, S3, Redis — rather than SQLite and local disk. The foundational bet is horizontal
scale: Silo deploys as a cluster (Kubernetes, remote transcode nodes) and stays fast on large
libraries, whether it's one node serving a household or a deployment streaming to thousands of
users. Weigh every design against that full spectrum; treat a node dying mid-stream as a normal
event, not an edge case.

It is an open platform, not a walled garden: third-party clients are encouraged, and other
people's clients will depend on the v1 API once it locks — see "v1 API rules" below for the
current pre-1.0 posture. Jellyfin-protocol compatibility is a long-term commitment as an
on-ramp for the existing ecosystem.

The core/plugin line is about implementation multiplicity: library types (movies, TV,
audiobooks, ebooks, podcasts) are core; plugins are for interfaces where many implementations
will plausibly exist (metadata, subtitle, and watch providers). Plugins are never a loophole
for the non-goals below.

Taste: KISS and YAGNI win — the simple design beats the clever one, provided it survives both
the single-node and the multi-node deployment. Current posture: the 1.0 feature set is
essentially complete; the present era is QA, UX polish, and verifying everything does what it
says. Prefer correctness and polish over new feature sprawl.

## How it fits together

Media enters through the scanner (`internal/scanner`, fed by `scanqueue`/`autoscan`), is
classified by library kind (`librarykind`), ingested (`libraryingest`), and enriched by
metadata plugins into the catalog: `media_items` keyed by deterministic content IDs
(`contentid`), with `media_files` for the actual on-disk files. The catalog serves the v1 API
and the home-screen sections (`sections`); `jellycompat` is a separate Jellyfin-protocol view
over the same catalog. Playback resolves a play method (`internal/playback`) — direct play,
direct stream, or transcode on a node from `nodepool` — and stream URLs are authorized by
short-lived `streamtoken` JWTs. Per-user state (watch progress, settings) is stored
server-side (`watchstate`, `userdb`, `settingsresolve`).

## Glossary

- **Account vs profile** — an account is a `users` row (login); a profile is a household
  member on an account. Several profiles share one `user_id`. See the gotcha below.
- **Library** — a media folder with a kind (movies, TV, audiobooks, ebooks, podcasts).
- **Item vs file** — a `MediaItem` is a catalog entry (movie/series, `content_id` PK); a
  `MediaFile` is one real file. One item can own many files (versions, extras, episodes).
- **Section** — a home-screen row (Continue Watching, Recently Added…), not a library.
- **Node** — a remote transcode/streaming worker in `nodepool`, not the API server.
- **Session** — ambiguous; always say which: playback session (`internal/playback`) or login
  session (`internal/auth`).
- **jellycompat vs v1** — jellycompat is the Jellyfin-protocol surface for ecosystem clients;
  "the API" or "v1" means Silo's native `/api/v1`.

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

The first two are irreversible — data loss, not inconvenience. Treat them as absolute.

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

**Docs hygiene.** Implementation plans and specs are ephemeral working artifacts, not
documentation. `docs/superpowers/` is gitignored: write plans there (or in any scratch dir)
while working, but never commit them — put the plan in the PR description instead. Before a
branch merges, distill anything durable (invariants, protocols, security rules) into
`docs/architecture/` and let the plan die. The code is the source of truth; a doc that
disagrees with the code is wrong. Any committed doc must not contain local absolute
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

When a task mentions plugins, work out first whether it belongs here, in the SDK, in the
catalog, or in a specific plugin repo.

A client-visible change (API, auth, playback, session, library, or metadata behavior) is not
done until each of these has been handled or ruled out:

- The API change fits the current v1 posture (see "v1 API rules" below); new features still
  expose a capability endpoint.
- Follow-up work is done or filed for both `silo-apple` and `silo-android` — prefer
  coordinated multi-repo changes over leaving a platform behind.
- jellycompat parity was considered (does the Jellyfin surface need the same behavior?).
- The relevant `docs/*-api.md` is updated, and `docs/feature-changelog.md` gets an entry if
  the change is user-facing.

## Building and verifying

`make build`, `make dev-backend`, `make dev-frontend`, `make lint`, `make test`, `make migrate-status`
/ `make migrate-up` — read the `Makefile` for the rest. Local services:
`docker compose up -d postgres redis`.

While iterating, run the focused tests for the packages you touched (`go test ./internal/<pkg>/...`)
rather than the whole suite; the full gate below is for pre-PR. In tests, wait on observable
state — job status, health endpoints, channel receipts — not fixed sleeps.

`make test-go` runs the whole Go suite. A Go test that cannot pass yet carries a `t.Skip` and the
reason in its own source, not an entry in a Makefile variable. `make test-web` still skips the
files in `WEBTEST_KNOWN_FAILURES`, which predate the CI gate; that list may only shrink — delete an
entry together with its fix, and never add to it to make a new change pass.

Before opening a merge request:

```bash
make lint
make test
cd web && pnpm run lint && pnpm run format:check
make verify-local-paths
```

`.github/workflows/ci.yml` runs these on every pull request, with one difference worth knowing:
`make lint` runs `golangci-lint` over the whole tree, while CI runs it with `--new-from-merge-base`
so only the lines a branch touched have to be clean. The repo does not pass a full run today, so
expect local output to include findings that are not yours and that CI will not fail on. Do not add
to them.

Go stays `gofmt`/`goimports` clean; the frontend follows `web/.prettierrc`.

## Skills

Task-specific guides live in `.claude/skills/`, also reachable as `.agents/skills/` for agents
that look there. Read the one that matches the task instead of working from this file alone.

They share one config file: copy `.silo-dev.env.example` to `.silo-dev.env` and fill in how to
reach your Silo deployment — URL, SSH target, database, an account to debug with. That file is
gitignored and is the only place hosts, passwords, and tokens belong. `scripts/silo-dev doctor`
checks it end to end.

## v1 API rules

Silo is alpha and `/api/v1` is not locked yet. Until it locks, restructuring the API is in
scope — if a shape is wrong, fix it now rather than carry it into 1.0. Prefer larger
coordinated sweeps over a drip of small breaks, and don't build backwards-compatibility shims
for pre-lock clients. A breaking change still needs coordination with `silo-apple` and
`silo-android`, and removals get recorded in the pre-lock removals table in
[docs/architecture/v1-scope.md](docs/architecture/v1-scope.md) so client authors can track
them.

At v1 lock (1.0), the contract becomes additive-only and binding:

- Never rename or remove a response field, change a field's type, or repurpose a status code on
  an existing endpoint.
- New functionality adds new fields or endpoints. Removals go through the Deprecation/Sunset
  header flow only.
- New features expose capability endpoints for feature detection rather than relying on version
  sniffing. Contract strategy and tooling: issue #135.

Design new endpoints today so they can live under that regime tomorrow.

## Pull requests

Conventional Commit subjects (`feat(playback): add realtime session hub`). One concern per PR.
Explain the problem, why this approach, the linked issue/spec/plan, and risks or follow-up work.
Include screenshots or recordings for UI changes. Link the capability epic or sub-issue the PR
serves (`Part of #NNN`) — PRs with no linked scope item get questioned at review. For non-trivial
work, open an issue or discussion first; this codebase moves quickly.

AI-use disclosure is required in the PR body. If you are an AI agent contributing on behalf of a
non-maintainer, follow [docs/ai-contributions.md](docs/ai-contributions.md) — it has the required
disclosure block and the evidence standard.
