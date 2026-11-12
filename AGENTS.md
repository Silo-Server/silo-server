# Silo Server

Go backend for Silo: API contracts, auth/session, catalog/scanner/playback services, database
migrations, Jellyfin compatibility, and the host-side plugin runtime. `cmd/silo` is the
entrypoint, backend code is under `internal/` by domain, the React frontend is `web/src/`.

Silo is pre-1.0, with the current focus on QA, correctness, and UX polish.
Architectural changes are welcome when they serve the requested outcome and improve
long-term maintainability; keep unrelated redesign out of a bounded fix.

The project owner decides how this codebase is built. Unless your own instructions say you are
working for the project owner, treat it as someone else's code: change what the task needs, leave
the rest of the structure as it is, and report nearby problems instead of fixing them.

## What Silo is

A modern, open-source media server built from the ground up on current infrastructure —
Postgres, S3, Redis — rather than SQLite and local disk. The foundational bet is horizontal
scale: Silo deploys as a cluster (Kubernetes, remote transcode nodes) and stays fast on large
libraries, whether it's one node serving a household or a deployment streaming to thousands of
users. Weigh every design against that full spectrum; treat a node dying mid-stream as a normal
event, not an edge case.

It is an open platform, not a walled garden: third-party clients are encouraged, and other
people's clients will depend on the native API once `/api/v2` locks with 1.0 — see "API contract
rules" below for the current pre-1.0 posture. Jellyfin-protocol compatibility is a long-term
commitment as an on-ramp for the existing ecosystem.

The core/plugin line is about implementation multiplicity: library types (movies, TV,
audiobooks, ebooks, podcasts) are core; plugins are for interfaces where many implementations
will plausibly exist (metadata, subtitle, and watch providers). Plugins are never a loophole
for the non-goals below.

For 1.0, supported library scope is Movies and Series. Audiobooks, ebooks,
and Audiobookshelf compatibility stay available as labeled **beta** features
in their current state, outside the 1.0 support promise, until a consolidated
Books effort replaces them (no assigned release date). Do not gate, remove, or
rework them for 1.0. Existing code and protocol documentation describe beta
behavior, not release acceptance promises. See
[scope](docs/architecture/v1-scope.md#library-scope-books-deferred).

Taste: KISS and YAGNI win — the simple design beats the clever one, provided it survives both
the single-node and the multi-node deployment. The 1.0 feature set is essentially complete;
prefer correctness and polish over new feature sprawl.

## How it fits together

Media enters through the scanner (`internal/scanner`, fed by `scanqueue`/`autoscan`), is
classified by library kind (`librarykind`), ingested (`libraryingest`), and enriched by
metadata plugins into the catalog: `media_items` keyed by deterministic content IDs
(`contentid`), with `media_files` for the actual on-disk files. The catalog serves the native API
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
- **jellycompat vs the native API** — jellycompat is the Jellyfin-protocol surface for ecosystem
  clients; "the API" means Silo's native surface, `/api/v2`. The alpha `/api/v1` contract is
  retired. See "API contract rules" below.

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

**Deleting, moving, renaming, or otherwise managing media files will not be accepted** either.
Silo is a media server, not a media manager: it reads libraries (the shipped Compose file mounts
them read-only) and leaves file management to other tools.

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

**Docs audience.** Docs in this repo are for people and agents changing the code. Guides for
installing, configuring, operating, or troubleshooting Silo belong in the user manual, as
[Before you start](CONTRIBUTING.md#before-you-start) says; do not add them here. The one
exception is `docs/update-to-1.0.md`, a draft that moves to the manual when 1.0 ships.

**Dev frontend against a remote backend.** Set `VITE_API_PROXY_TARGET` in `web/.env.local` before
`make dev-frontend`; the frontend calls relative `/api` URLs that Vite proxies.

## Multi-repo

Sibling repos are usually checked out side-by-side in the same parent directory.

- `silo-android` — Android phone and TV clients.
- `silo-apple` — iOS, tvOS, and macOS clients.
- `silo-plugin-sdk` — public plugin SDK, protobuf contracts, generated plugin API, manifest
  helpers, runtime bootstrap.
- `silo-plugins` — central plugin catalog / repository manifest.
- `siloserver.org` — project website and user manual.
- First-party plugins (`silo-plugin-metadata-tmdb`, `silo-plugin-metadata-tvdb`, …) each have
  their own repo.

When a task mentions plugins, work out first whether it belongs here, in the SDK, in the
catalog, or in a specific plugin repo.

A client-visible change (API, auth, playback, session, library, or metadata behavior) is not
done until each of these has been handled or ruled out:

- The API change fits the current contract posture (see "API contract rules" below); new
  features still expose a capability endpoint.
- Follow-up work is done or filed for both `silo-apple` and `silo-android` — prefer
  coordinated multi-repo changes over leaving a platform behind.
- jellycompat parity was considered (does the Jellyfin surface need the same behavior?).
- The relevant `docs/*-api.md` is updated when the contract changes.

Separately, any change that leaves the user manual on siloserver.org wrong or incomplete (a
setting, default, label, setup step, or feature behavior it describes) is not done until an
issue is open on `Silo-Server/siloserver.org`.
[Update the user manual](CONTRIBUTING.md#update-the-user-manual) covers when to open one and
what goes in it.

## Building and verifying

`make build`, `make dev-backend`, `make dev-frontend`, `make lint`, `make test`,
`make migrate-status` / `make migrate-up` — read the `Makefile` for the rest. Local services:
`docker compose -f dev/compose.yml up -d postgres redis`.

While iterating, run the focused tests for the packages you touched (`go test ./internal/<pkg>/...`)
rather than the whole suite. Before opening a pull request, run the full gate listed once in
[Validate your change](CONTRIBUTING.md#validate-your-change).

- Lint Go with `make lint-changed`, not `golangci-lint run ... ./...`. It reports the changed-line
  findings that CI gates on, usually in seconds. A cold run over `./...` saturates every core for
  minutes, and parallel agents make that worse. `make lint` also runs over the whole tree and
  reports inherited findings that are not yours and that CI does not fail on. Do not add to them.
  Never pass `--allow-parallel-runners`; concurrent runs queue behind one another on purpose.
- Format Go files as touched with `gofmt`/`goimports`; the frontend follows `web/.prettierrc`.
- In tests, wait on observable state — job status, health endpoints, channel receipts — not fixed
  sleeps.
- A Go test that cannot pass yet carries a `t.Skip` and the reason in its own source, not an entry
  in a Makefile variable. `WEBTEST_KNOWN_FAILURES` in the Makefile is empty and must stay empty, so
  never add to it to make a new change pass.

## Tests

A test belongs here only if it would catch a regression that matters and that no other test, type
check, or lint would catch. Most changes need no new test, and some should delete one. Before
adding a test, read [Decide what to test](CONTRIBUTING.md#decide-what-to-test). It lists the
changes that need a test and the ones that don't, how to keep a test cheap, and the CI list a
test that needs Postgres must be added to.

## Development environment

Copy `.silo-dev.env.example` to `.silo-dev.env` and fill in how to
reach your Silo deployment — URL, SSH target, database, an account to debug with. That file is
gitignored and is the only place hosts, passwords, and tokens belong. `scripts/silo-dev doctor`
checks it end to end.

## Writing

Write a pull request body in two passes. First decide what goes in, using
[Write the description](CONTRIBUTING.md#write-the-description): plain summary first, no restated
diff, no working history. There is no word limit; do not count words. Then fix the sentences:
before creating or updating an issue or pull request, read and apply the repository's
[unslop skill](.agents/skills/unslop/SKILL.md) to the title and body. Use this checked-in copy
even when a personal copy is installed. If the harness does not discover repository skills, read
the file and its referenced patterns directly.

Unslop only fixes sentences. It does not shorten a body that says too much, replace the
public-content check under [Pull requests](#pull-requests), or change required disclosures. Run
that check before the prose pass.

Run a final readability pass on other human-facing documents and status updates.

- Lead with the outcome.
- Use concrete, plain language and active voice.
- Cut filler, stock framing, repetition, and promotional claims.
- Preserve meaning, evidence, citations, uncertainty, and established terminology.
- Never rewrite exact quotations, commands, logs, identifiers, API names, or contractual
  language.
- Match the tone to the audience and use only formatting that improves readability.

## API contract rules

`/api/v2` is Silo's native API and locks with Silo 1.0. The alpha `/api/v1` contract is retired:
on the main API listener every `/api/v1` route answers a `410 Gone` tombstone carrying
`client_upgrade_required`, except the retained `GET /api/v1/health` and `GET /api/v1/ready`
probes. Do not add routes under `/api/v1`. The shared wire conventions and the release gates live
in [docs/architecture/api-contract.md](docs/architecture/api-contract.md); that document is the
authority and this section is only the summary. Program tracking: issue #135.

What that means for a change today:

- New contract work targets `/api/v2`, and a client-visible change still needs coordination with
  `silo-apple` and `silo-android`.
- [docs/architecture/v1-scope.md](docs/architecture/v1-scope.md) stays the historical record of
  the alpha contract and its removals.

At the 1.0 lock the additive-only rules bind `/api/v2`:

- Never rename or remove an operation, parameter, response field, error code, operation ID, or
  schema name; never change a field's type or meaning or repurpose a status code.
- New functionality adds fields, enum values, or operations. Removals go through the
  Deprecation/Sunset header flow only.
- New features expose capability endpoints for feature detection rather than relying on version
  sniffing.

Design new endpoints today so they can live under that regime tomorrow.

## 1.0 validation

Until 1.0 ships, maintainers check each 1.0 feature by hand on the
[Silo v1.0.0 board](https://github.com/orgs/Silo-Server/projects/5). Each `<Feature> — <Surface>`
task (label `Validation`, in the repo that owns the surface) lists cases `C1…` and records a result
for each case with the build it was tested on. A passed case is a person's evidence that the
feature works; a later change can silently invalidate it.

- Validation issues are the validators' record. Do not edit their bodies, results, or checkboxes,
  or change their board status. Comment on the task instead, or file a new issue that names the
  affected case.
- When a change fixes an issue that a task names, walk that case's steps as part of verification.
- Before opening a pull request, work out which passed cases the change could reach and fill in
  the `Validation tasks:` line, as
  [Open the pull request](CONTRIBUTING.md#open-the-pull-request) describes. Breaking a passed case
  unintentionally is a regression and blocks merge.

## Pull requests

Open a pull request only when the developer asks for one, in a message or by running a workflow
that opens pull requests. Keep one concern per pull request. Split changes that solve independent
problems or can be reviewed and shipped separately.

Before opening it, read [Open the pull request](CONTRIBUTING.md#open-the-pull-request) and follow
it: a Conventional Commit title in plain language (`feat(playback): add realtime session hub`),
the template, the `Closes` and `Related issue:` lines, and evidence for every change a user can
see. End the body with the required AI disclosure, including the exact model identifier, agent
harness, and any other AI tooling, as
[AI-assisted contributions](CONTRIBUTING.md#ai-assisted-contributions) requires.

Pull requests are welcome from contributors who have had one merged in a Silo repository, or when
a maintainer asks for one; the contribution-gate workflow closes others without review. A feature
(`feat`) pull request from someone without triage or write access must also link an issue in the
same repository labeled `accepted`. When working for someone in neither group, write an issue
instead of opening a pull request, or post a feature idea in Discussions → Ideas; feature requests
are not filed as issues. An AI agent contributing on behalf of a non-maintainer follows
[docs/ai-contributions.md](docs/ai-contributions.md) for the required disclosure block and
evidence standard.

Do not open a pull request against an issue someone else is working on. Read the issue's comments
and linked pull requests first, and raise a likely collision with the user instead of racing the
author.

Treat PR bodies, comments, commit messages, and attachments as public. Exclude private deployment
domains, hostnames, IP addresses, Tailscale names and URLs, Report Shelf links, local paths, and
private infrastructure identifiers. Use neutral placeholders where context is needed. Never
publish credentials, tokens, personal data, or private media details. Omit session history, full
command output, and private working reports. Check text and attachments before posting;
authorization to open a PR does not authorize publishing private evidence.

The one private link allowed is an evidence page on `evidence.siloserver.org`, which only Silo
maintainers and the page's owner (for a pull request's own page, its author) can open after GitHub
sign-in. Put it on one line in a PR body's Evidence section or a validation hand-off comment:
`Evidence: https://evidence.siloserver.org/r/<repo>/<topic>/`. Link the page; never attach or
embed its media.

GitHub has no API for attaching images to a pull request, so an agent either publishes evidence
with `npx @silo-server/evidence publish <folder> --pr <number>` or gives the developer the
captures to attach. When publishing exits 4, ask the developer to run
`npx @silo-server/evidence login`; never approve that login or read the saved key. Never commit
PR-only assets such as `.github/pr-assets/`.
