# Contributing to Silo

> [!IMPORTANT]
> The most helpful way to contribute to Silo right now is a clear, accurate
> issue. Pull requests are welcome from contributors who've had one merged in a
> Silo repository, or when a maintainer asks for one. We close other pull
> requests without review.

## Why we're asking for issues

Pull requests now arrive faster than we can review them carefully. Reviewing a
change properly means reading every line, checking it against work already in
flight, re-running validation, and owning the result after it merges. Writing
the change ourselves from a precise issue takes less time, and it keeps every
change on one workflow with the same tests and the same review.

This is about review capacity, not the quality of anyone's work, and it applies
whether or not you used AI. We'll revisit it as Silo approaches 1.0. Thank you
for taking the time to write things up well.

## Write a useful issue

Use the [GitHub issue forms](https://github.com/Silo-Server/silo-server/issues/new/choose).
Problems that only affect a native client belong in
[`silo-apple`](https://github.com/Silo-Server/silo-apple) or
[`silo-android`](https://github.com/Silo-Server/silo-android). Search first; if
an issue already covers the problem, add what's new there.

Using Claude Code or Codex? The
[Silo troubleshooting skill](https://github.com/Silo-Server/silo-troubleshooting-skill)
helps you collect these details and drafts the issue, then has independent
reviewers check it against your evidence before you post it.

- One problem or proposal per issue.
- Describe what you observed before any theory about the cause.
- Give exact steps to reproduce, expected and actual behavior, the Silo version
  or commit, your deployment, and the clients involved.
- Paste raw logs rather than a summary. Redact credentials, tokens, personal
  data, and private media details, mark each redaction, and leave the rest
  untouched.
- For a feature, describe the problem it solves and who it affects, and read
  [Project non-goals](docs/non-goals.md) first.
- If you found the cause or have a fix in mind, put it under Technical notes,
  apart from what you observed. Point to the files involved; a short code
  excerpt is fine. We may implement it differently.
- Disclose AI use, as described in
  [AI-assisted contributions](#ai-assisted-contributions).

Report security vulnerabilities privately with **Report a vulnerability** on
the repository's Security tab, not in a public issue.

## Pull requests

Pull requests are welcome from contributors who've had a pull request merged in
a Silo repository, and from anyone a maintainer has asked for one, usually in
an issue comment. The rest of this guide applies to those pull requests.

We close other pull requests without review. That isn't a judgment of the
work. If the problem still matters, open an issue for it and link the closed
pull request; we may use the code as a reference.

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

Documentation in this repository is for people and coding agents changing the
code: architecture, invariants, API contracts, and development setup. Guides
for installing, configuring, operating, or troubleshooting Silo belong in the
[user manual](https://siloserver.org/docs), which lives in
[Silo-Server/siloserver.org](https://github.com/Silo-Server/siloserver.org).
This repository does not accept operator or user guides; open those pull
requests against the website repository instead.

Choose the repository that owns the behavior before implementation begins.
This repository owns the backend, web app, native API, Jellyfin compatibility,
and plugin host. Client-only work belongs in `silo-apple` or `silo-android`;
plugin contracts belong in `silo-plugin-sdk`; provider behavior belongs in the
individual plugin repository. Cross-repository changes should identify all
affected repositories in the issue and pull request.

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
pnpm run budget:check           # launch bundle size, vendor chunk, render-blocking resources
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
make verify-case-collisions
```

PR CI selects Go or Web jobs from a complete diff. Shared contracts, generated
bindings, workflow changes, unknown inputs, and unavailable diffs run both
groups. Ordinary Markdown documentation runs the documentation checks. Pushes
and manual runs keep the full gate. The `CI result` job rejects failed, canceled,
missing, or unexpectedly skipped work; only jobs excluded by the selection may
skip. Selection tools come from the trusted reusable workflow's main branch.

`Go test` runs the full Go suite, which owns the ledger, scenario,
offline-route, spec, and fixture assertions. `Go lint` runs the contract checks
after lint: the generator freshness checks and the semantic API comparison. The
contract checks pass `CONTRACT_GO_TESTS=0` to avoid repeating the suite's
assertions; local verify targets remain complete by default. `Go integration`
runs its race tests against two databases of its own: a PostgreSQL 17 database
for the Watch Party, history import, and marker claim migration tests, and a
migrated pgvector database for the tests that need the full schema. `Go DB pins`
and `Go DB external auth` each start a separate pgvector database, and the unit
suite has no database. No CI job sets `SILO_SCENARIO_DATABASE_URL`, so the
scenario executor, which truncates its database, gets none of these databases.

Touching `internal/apiv2` registrations? Run `make apiv2-openapi` and
`make apiv2-fixtures` and commit what they write; the gates above fail on a
stale artifact or fixture tree.

`make test-go` has no database, so every DB-backed test in it skips. The
`Go DB pins` CI job covers the DB-backed pins listed in
[scripts/ci/db-pins.txt](scripts/ci/db-pins.txt): it migrates a fresh database
and runs `make test-db-pins`, which checks those budgets and then the database
contracts in `scripts/ci/db-contracts.txt`. Both lists fail when a named test is
missing, skipped or failing. A test that pins a statement count or query plan
belongs in the budget list, added in the same change. Run this target when you
change database or query code or add a pin. It needs a disposable, migrated
database; with the PostgreSQL service from
[DEVELOPMENT.md](DEVELOPMENT.md#local-development)
running under the Compose defaults:

```sh
docker compose exec postgres createdb -U silo silo_pins
export SILO_TEST_DATABASE_URL='postgres://silo:silo@localhost:5432/silo_pins?sslmode=disable'
DATABASE_URL="$SILO_TEST_DATABASE_URL" SECRET_KEY="$(openssl rand -base64 48)" \
  go run ./cmd/silo/ --migrate-only
make test-db-pins
```

When retiring a duplicate test, name the surviving test that exercises the
production behavior. Preserve unique assertions there before deleting the
duplicate. If that keeper needs Postgres, it must run in CI: query budgets
belong in `scripts/ci/db-pins.txt`; other database contracts belong in
`scripts/ci/db-contracts.txt`. `make test-db-pins` is the CI entry point for both
lists. To run only the database contracts, use `make test-db-contracts` against
the same disposable, migrated database.

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

## Update the user manual

The user manual and feature pages on [siloserver.org](https://siloserver.org)
live in a separate repository,
[Silo-Server/siloserver.org](https://github.com/Silo-Server/siloserver.org),
and nothing updates them automatically. To write or fix a guide, open a pull
request there. When a code change here leaves the site wrong or incomplete,
open an issue there.

A change needs a docs issue when it:

- adds, renames, or removes a setting, menu item, or screen, or changes a
  default;
- changes what a feature does or supports, or how to set it up, including
  installation, Docker, environment variables, storage, and plugins;
- makes anything else on the site inaccurate, such as a feature description.

Fixes that make Silo behave the way the manual already says, internal
refactors, and changes users cannot see do not need one. If you are unsure,
search the site for the feature.

Open the issue when the pull request is ready for review, or as soon as you
notice that an already merged change needs one. Reference the pull request as
`Silo-Server/silo-server#NNN` so GitHub links the two, and say that the site
update waits for it to merge. In the issue:

- say what changed for users;
- list the pages to update and what each now gets wrong, with labels and
  defaults copied from the app source;
- note what is out of scope, such as apps that need no change.

[#29](https://github.com/Silo-Server/siloserver.org/issues/29) and
[#30](https://github.com/Silo-Server/siloserver.org/issues/30) are examples.
If an open issue already covers the same pages, comment on it instead. Update
the issue when review changes the behavior, and close it if the pull request
closes without merging.

## AI-assisted contributions

AI-assisted work is welcome; most of Silo was written with AI assistance.
Whoever submits the work is responsible for understanding it, testing it, and
explaining it; that applies to maintainers and external contributors alike.

Disclose AI use in every issue and pull request, or state "No AI used" when true.
The [AI-assisted contribution policy](docs/ai-contributions.md) covers contributor
responsibility, evidence, and enforcement. Use the disclosure fields in the PR
template or issue form.

## Open the pull request

Use a [Conventional Commit](https://www.conventionalcommits.org/) title and fill
in the pull request template. For each issue the change fully resolves, add a
`Closes #NNN` line (`Closes Silo-Server/<repo>#NNN` for another repository) so
GitHub closes the issue when the pull request merges into `main`; the
`Related issue:` line alone does not close anything. Name an epic, scope item,
or partly addressed issue on `Related issue:`, and write `Related issue: N/A`
when none applies. Either way, the Problem section has to stand on its own. Keep the commit history intentional and the diff
limited to the stated problem. Keep the description proportional to the change;
omit session history, full logs, and private report links other than an
`Evidence:` line. Follow the
[public-content and media rules](AGENTS.md#pull-requests), and include evidence
for every change users can see, as [Show visible changes](#show-visible-changes)
describes.

### Write the description

Write for a maintainer who knows Silo but has not seen your working session or
the diff. The first paragraph should tell them what is broken and what this
change does; the rest should help them decide how closely to review.

- Open the Problem section with a short plain-language summary: what goes
  wrong, who it affects, and what this change does about it. Identifiers,
  numbers, and mechanism come after that.
- Use the names the codebase already uses, or plain words. Do not carry over
  terms you coined while working. If a new name is unavoidable, define it once
  and keep using it.
- Do not restate the diff. Skip per-test lists, walkthroughs of each function,
  and paraphrases of code comments. Say what the tests cover and what they do
  not.
- Leave out how you got here: earlier designs, dead ends, and how an
  investigation or replay was run. Mention a rejected alternative only when a
  reviewer would otherwise ask about it, in one sentence.
- Let the change set the length. A small fix needs a few lines; a risky or
  subtle change can take more. There is no word limit, so do not count words
  or trim to a target. Long supporting evidence, such as tables or
  measurements, can go in a `<details>` block after the summary.

### Show visible changes

A pull request that changes what a user sees must show the change in its
Evidence section, so reviewers can see it without building the branch. In this
repository that means the web app and web admin, and every client that renders
server data. A change is visible when it alters any of these:

- layout, styling, copy, navigation, focus, empty and error states;
- which items a screen shows, or in what order: search results, home sections,
  recommendations, library browsing, collections, sorting, or filtering;
- what an item shows: titles, artwork, descriptions, ratings, badges, episode
  grouping, or availability;
- playback behavior a user notices, such as default audio or subtitle tracks,
  markers, controls, or resume position;
- a native API or jellycompat response field that clients render.

Provide evidence that fits the change:

- **Changes to a screen:** before-and-after screenshots of the same screen with
  the same data, one pair per affected surface. Add a short recording when
  motion, timing, focus movement, or a multi-step flow matters.
- **Web app and web admin:** desktop and mobile web are separate surfaces. When
  the change is also visible at a phone-width viewport, include before-and-after
  captures for both desktop and mobile web (or a short recording that covers
  both). Desktop-only screenshots are not enough unless the pull request shows
  the change is desktop-only and mobile layout is unaffected.
- **Server changes no client shows yet:** before-and-after excerpts of the API
  response for the same request, trimmed to the fields that changed, such as the
  ordered list of result titles.
- Name the surface and the build or commit each capture came from.

Capture against a test library or public-domain media where you can, and keep
passwords, tokens, and API keys out of every capture. Then put the evidence in
one of two places:

- **On GitHub:** attach the screenshots or recordings, or paste response
  excerpts in a code block, under the pull request's Evidence heading.
  Everything on GitHub is public, so crop or blur hostnames, URLs, account
  names, and personal library contents.
- **On [evidence.siloserver.org](https://evidence.siloserver.org/) (optional):**
  only you and Silo maintainers can open what you publish there, after signing
  in with GitHub, so captures need no cropping or blurring. Captions that start
  with `Before:` and `After:` become a side-by-side comparison, and recordings
  get a player. Upload from the Details link of the pull request's `Evidence`
  check, or with the command line (Node.js 22 or later):
  `npx @silo-server/evidence login` once on each computer, then
  `npx @silo-server/evidence publish <folder> --pr <number>`. The
  [package README](https://www.npmjs.com/package/@silo-server/evidence) describes
  the folder. Then write
  `Evidence: https://evidence.siloserver.org/r/silo-server/pr-<number>/` under
  the Evidence heading. Until a pull request of yours has merged here, a
  maintainer approves you once before your first upload.

The pull request's `Evidence` check passes once evidence is published or
attached. It asks for evidence when the change touches the web app, the native
API, or jellycompat, or when a maintainer adds the `evidence-required` label; a
maintainer adds `evidence-not-needed` when nothing visible changed. Changes
users cannot see write `Evidence: none, no user-visible change`. If you could
not capture evidence, say why; the reviewer decides whether the pull request
can merge without it.

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
