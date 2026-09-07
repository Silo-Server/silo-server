# Contributing to Silo

Contributions are welcome from any workflow, including AI-assisted ones. Most of
Silo was written with AI assistance. Whoever submits the work is responsible for
understanding it, testing it, and explaining it; that applies to maintainers and
external contributors alike.

## Before you start

> [!IMPORTANT]
> Open an issue or discussion before implementing features, API or behavior
> changes, schema migrations, large refactors, or anything else that changes
> product scope. Documentation, typo fixes, and narrow bug fixes can go straight
> to a pull request.

Silo is pre-1.0 and moves quickly. Coordinating first avoids duplicate work,
conflicts with changes already in flight, and proposals outside scope. Read
[Project non-goals](docs/non-goals.md) and the relevant
`docs/architecture/` material before proposing a capability.

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
golangci-lint run --new-from-merge-base="origin/main" ./...
make test-go

# Web
cd web
pnpm install --frozen-lockfile
pnpm run lint
pnpm run format:check
pnpm run build
cd ..
make test-web

# Generated contracts, fixtures, and docs hygiene
make verify-settings-bindings-all
make verify-playback-fixtures
make verify-local-paths
```

`make lint` runs `golangci-lint` over the whole tree and reports inherited
findings the repository does not pass yet; CI only gates the lines your branch
changed, which is what the `--new-from-merge-base` form checks. Do not add to
the inherited findings.

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
in the pull request template. Link the issue or scope item for non-trivial
work; write `Related issue: N/A — narrow fix` only when no prior coordination
was needed. Keep the commit history intentional and the diff limited to the
stated problem. Keep the description proportional to the change; omit session
history, full logs, and private report links. Follow the
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
