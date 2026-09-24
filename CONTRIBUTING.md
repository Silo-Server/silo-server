# Contributing to Silo

Contributions are welcome from any workflow, including AI-assisted ones. Most of
Silo was written with AI assistance. Whoever submits the work is responsible for
understanding it, testing it, and explaining it; that applies to maintainers and
external contributors alike.

## Before you start

> [!IMPORTANT]
> An open issue is not required before a pull request. State the problem in the
> pull request itself: what breaks or is missing, who it affects, and why this
> change is the right answer. Link an issue when one already covers the work.

Silo is pre-1.0 and moves quickly. For features, API or behavior changes, schema
migrations, large refactors, or anything else that changes product scope, opening
an issue or discussion first is still the cheapest way to learn that the work is
already in flight or outside scope. That is a judgment call, not a gate; the risk
of a rejected pull request is yours. Read [Project non-goals](docs/non-goals.md)
and the relevant `docs/architecture/` material before proposing a capability.

An open issue is not an unclaimed one. Before implementing someone else's issue,
read its comments and linked pull requests to see whether the author is already
working on it, and say you are picking it up.

Durable architecture and contracts live under `docs/architecture/`.
Implementation plans and working notes belong in the issue or pull request, not
in the repository.

Choose the repository that owns the behavior before implementation begins.
This repository owns the backend, web app, native API, Jellyfin compatibility,
and plugin host. Client-only work belongs in `silo-apple` or `silo-android`;
plugin contracts belong in `silo-plugin-sdk`; provider behavior belongs in the
individual plugin repository. Cross-repository changes should identify all
affected repositories in the issue and pull request.

## Reporting a problem

Use the [GitHub issue forms](https://github.com/Silo-Server/silo-server/issues/new/choose);
they ask for everything a maintainer needs. Two rules: describe what you observed
before any root-cause theory, and paste raw logs rather than a summary. Redact
credentials, tokens, personal data, and private media details, mark each
redaction, and leave the rest untouched.

## Prepare a focused change

1. Read the existing implementation and tests in the area you are changing.
2. One concern per pull request. No unrelated cleanup or refactors.
3. Follow existing patterns; comment only where behavior is not obvious from the
   code.
4. Add tests that fail before the fix and pass after it.
5. Exercise user-facing behavior in a running application when you can.
6. Review the whole diff for unintended behavior, generated-file drift, local
   paths, credentials, and stray edits.
7. For non-trivial changes, get an independent or adversarial review and
   resolve its findings before submitting.

Tests are evidence, not proof. Think about effects beyond the files you touched,
and be ready to explain the implementation, alternatives, and tradeoffs in
review.

## Development setup

[DEVELOPMENT.md](DEVELOPMENT.md) covers prerequisites, local services, builds,
migrations, and repository layout, including how to iterate against
`silo-plugin-sdk`.

## Validate your change

While iterating, run the focused tests for what you touched:

```sh
go test ./internal/<package>/...
cd web && pnpm exec vitest run path/to/changed.test.tsx
```

Before opening a pull request, run the full gate. This is the one list; the
[CI workflow](.github/workflows/ci.yml) is authoritative if they ever disagree.

```sh
# Go
make embed-stub
go build ./...
gofmt -l .                      # must print nothing
go vet ./...
make lint-changed                   # BASE_REF=origin/<pr-base> when not main
make test-go

# Web
cd web
pnpm install --frozen-lockfile
pnpm run lint
pnpm run format:check
pnpm run build
pnpm run budget:check           # launch bundle size against perf-budget.json
cd ..
make test-web

# Generated contracts, fixtures, and docs hygiene
make verify-settings-bindings-all
make verify-playback-fixtures
make verify-route-inventory
make verify-migration-ledger
make verify-scenario-catalogs
make verify-offline-routes
make verify-apiv2-openapi
make verify-apiv2-contract          # BASE_REF=origin/<pr-base> when not main
make verify-apiv2-fixtures
go test -count=1 -run '^TestCommittedArtifactMatchesRouter$' ./internal/apiv2/
make verify-local-paths
```

Touching `internal/apiv2` registrations? Run `make apiv2-openapi` and
`make apiv2-fixtures` and commit what they write; the gates above fail on a
stale artifact or fixture tree.

`make test-go` has no database, so every DB-backed test in it skips. The
`Go DB pins` CI job covers the query-budget pins listed in
[scripts/ci/db-pins.txt](scripts/ci/db-pins.txt): it migrates a fresh database
and runs `make test-db-pins`, which fails when a listed test is missing,
skipped or failing. A test that pins a statement count or query plan belongs in
that list, added in the same change. Run it yourself when you change database
or query code or add a pin. It needs a disposable, migrated database; with the
PostgreSQL service from [DEVELOPMENT.md](DEVELOPMENT.md#local-development)
running under the Compose defaults:

```sh
docker compose exec postgres createdb -U silo silo_pins
export SILO_TEST_DATABASE_URL='postgres://silo:silo@localhost:5432/silo_pins?sslmode=disable'
DATABASE_URL="$SILO_TEST_DATABASE_URL" SECRET_KEY="$(openssl rand -base64 48)" \
  go run ./cmd/silo/ --migrate-only
make test-db-pins
```

`make lint` runs `golangci-lint` over the whole tree and reports inherited
findings the repository does not pass yet; CI only gates the lines your branch
changed. `make lint-changed` checks exactly those lines, and it analyzes only
the packages your branch touched, so it takes seconds where a cold run over
`./...` takes minutes of every core. Do not add to the inherited findings.
Never pass `--allow-parallel-runners`: concurrent runs queue behind one
another on purpose.

Summarize the relevant commands and results in the pull request. Name required
checks that were skipped or failed, and include short output excerpts only when
they help explain a failure. Describe the test environment without identifying
private infrastructure. Never claim a check passed or ran on a target it did not.

## AI-assisted contributions

Disclose AI use in every issue and pull request, or state "No AI used" when true.
The [AI-assisted contribution policy](docs/ai-contributions.md) covers contributor
responsibility, evidence, and enforcement. Use the disclosure fields in the PR
template or issue form.

## Open the pull request

Use a [Conventional Commit](https://www.conventionalcommits.org/) title and fill
in the pull request template. Link an issue or scope item when one covers the
work; write `Related issue: N/A` when none does. Either way, the Problem section
has to stand on its own. Keep the commit history intentional and the diff
limited to the stated problem. Keep the description proportional to the change;
omit session history, full logs, and private report links. Follow the
[public-content and media rules](AGENTS.md#pull-requests). Screenshots and recordings
are not routine PR requirements; attach them only when explicitly requested.

## Review expectations

Maintainers may ask for a smaller change, a different implementation, decline
work that no longer fits, or take the idea and implement it separately. Opening
a pull request does not guarantee a merge. If scope is uncertain, ask before
building.

## Instructions for coding agents

Coding agents must read [AGENTS.md](AGENTS.md) before changing the repository
(`CLAUDE.md` points to the same file). This guide and the
[AI-assisted contribution policy](docs/ai-contributions.md) apply to agent and
human authors equally. Before creating or updating an issue or pull request,
agents must apply the checked-in [unslop skill](.agents/skills/unslop/SKILL.md) to
the title and body, as required by the [Writing policy](AGENTS.md#writing).
