---
name: issue-to-pr
disable-model-invocation: true
description: >-
  End-to-end implement a GitHub issue and open a ready pull request. Invoke
  explicitly as `/issue-to-pr <number|#ref|url>` (this is a side-effecting
  workflow — it creates branches and opens PRs — so it does not auto-trigger).
  It creates an isolated git worktree off the latest main, reproduces the bug
  (or scopes the feature), finds the root cause, implements the smallest correct
  change following repo conventions, verifies it (build/lint/tests), runs a Codex
  adversarial review and addresses the material findings, then commits and opens
  a ready PR. Not for pure triage (use triage-issue), pure review of an existing
  PR (use review-pr-or-mr / code-review), or analysis without code changes.
---

# Issue → PR

Turn a single GitHub issue into a verified, reviewed, ready-to-merge pull
request — autonomously — while staying honest about uncertainty and stopping
when a human decision is genuinely required.

The goal is not "produce a diff." It is: **confirm the real problem exists,
make the smallest change that correctly solves it, prove it works by execution,
have an adversary try to break it, and only then ship a PR.** Speed comes from
doing these in order, not from skipping them.

## Inputs

- The issue reference is passed as `$ARGUMENTS` — a number (`123`), a `#123`
  reference, or a full issue URL. If none is given, ask; do not guess.
- The repo is the current git repository; PRs target `origin`
  (`Silo-Server/silo-server`) and base `main`.

## Operating principles

- **Default to "maybe nothing needs fixing."** Coding agents "fix" already-correct
  or stale code in a large fraction of cases because they don't stop to confirm the
  problem is real. Confirm the issue reproduces on current `main` before changing
  anything — it may already be fixed.
- **Root cause over symptom.** A fix you can't explain is a fix you can't trust.
- **Verify by execution, not by reading.** Build/lint/tests are the primary
  correctness signal. The adversarial review is a *second* layer on top — never a
  substitute for green checks. If you can't verify it, don't ship it.
- **Smallest correct change.** Match surrounding code's idioms, naming, and comment
  density. Put code in the package that owns the behavior (CLAUDE.md); don't create
  catch-all helpers or duplicate logic — search for an existing helper before
  writing a new one, and extract shared logic instead of copying.
- **Don't trust recall for APIs.** Verify every symbol, function, and signature you
  call actually exists by grepping the codebase — agents hallucinate plausible-looking
  APIs.
- **Don't gold-plate.** A reviewer told to find gaps will always find some. Address
  what affects correctness or the issue's stated requirements; treat the rest as
  optional. Extra abstraction "just in case" is a defect, not diligence.
- **Know when to stop.** See [Stop and ask](#stop-and-ask). Autonomy is for
  execution, not for inventing product decisions or shipping unverifiable guesses.

Track the phases below with a task list (TaskCreate/TaskUpdate) so progress is
visible and nothing is skipped on long runs.

## Workflow

### 1. Understand the issue

Read everything before writing anything.

```bash
gh issue view <N> --json number,title,body,labels,state,author,url,comments
```

Determine: **bug vs. feature** (labels + body → branch prefix `fix/` vs `feat/`);
**acceptance criteria** (what does "done" mean?); **linked scope** (does it serve
an epic / sub-issue to reference as `Part of #NNN`?); and **client surface** (does
it touch API/auth/playback/session/library/metadata behavior consumed by
`silo-android` / `silo-apple`? — if so, coordinated client follow-up may be needed).

If the issue is vague, underspecified, or needs a product call, stop and ask now —
before spending a worktree on the wrong problem.

### 2. Create a worktree off the latest main

Work in isolation so the user's checkout is untouched. Create it at the **main
repository root**, not inside the current worktree.

```bash
# Resolve the primary repo root (works from inside any worktree)
MAIN_ROOT="$(cd "$(dirname "$(git rev-parse --git-common-dir)")" && pwd)"
git -C "$MAIN_ROOT" fetch origin                # always branch off fresh main

SLUG=<short-kebab-slug-from-issue-title>
BRANCH=fix/issue-<N>-$SLUG                       # use feat/ for features
WT="$MAIN_ROOT/.claude/worktrees/issue-<N>"

git -C "$MAIN_ROOT" worktree add -b "$BRANCH" "$WT" origin/main
cd "$WT"
```

If the branch/worktree already exists from a prior run, reuse it (cd in) rather
than failing — but confirm it's based on current `origin/main`; if it's stale, tell
the user before continuing. From here, **all work happens in `$WT`** (the Bash tool
keeps the working directory between calls); use absolute paths when in doubt.

### 3. Reproduce / confirm — a hard gate

This is the highest-leverage step. Do not skip it.

- **Bug:** reproduce it concretely — ideally a **test that fails on current `main`**
  (red), or a precise script/trace through the faulty path. Pin down the exact code
  path and the invariant it violates.
  - **If you cannot reproduce it, STOP and report** — do not patch. The issue may
    already be fixed, be environment-specific, or be a misunderstanding. Shipping a
    speculative fix for a non-reproducing bug is the most common way agents make
    things worse.
- **Feature:** confirm the capability is actually missing, then map where it fits —
  the owning package, patterns to follow, the data flow, the smallest set of files.

**Plan proportionally.** If you could describe the diff in one sentence (a typo, a
log line, a one-line guard), skip formal planning and make the change. Plan first
when the work is multi-file, unfamiliar, or architecturally uncertain — name the
files/interfaces involved, state what's out of scope, and end the plan with the
verification step. For anything large, confirm direction with the user before a big
build-out.

### 4. Implement

Make the change, scoped to this one concern (one concern per PR). Respect the hard
rules:

- **API:** additive-only within `/api/v1` — never rename/remove a response field,
  change a field's type, or repurpose a status code. New behavior = new
  fields/endpoints; expose capability endpoints for feature detection.
- **Migrations:** new DB changes are Goose SQL migrations via
  `make migrate-create NAME=...` (timestamped). Never `goose fix`, never paired
  `.up.sql`/`.down.sql`.
- **Maintainability:** extract shared logic instead of duplicating; change existing
  code rather than bolting on local workarounds.

For a bug, the failing test from step 3 should now pass.

### 5. Verify — the primary correctness gate

Run the checks and make them genuinely pass. The exact commands and the worktree
quirks (`GOWORK=off`, stubbing `web/dist`, the v2 golangci invocation, the flaky
GPU tests) are in **[references/verification.md](references/verification.md)** —
read it first, because plain `go build ./...` / `make lint` fail inside
`.claude/worktrees`.

At minimum: Go build + the relevant package tests pass; if `web/` changed,
`pnpm run lint` + `pnpm run format:check` + build pass; `make verify-local-paths` is
clean. Run the **full relevant suite**, not just your new test — catch regressions
in code you didn't touch. Show the command output as evidence; don't assert success.

**Never reach green by weakening the checks.** Deleting/skipping/`xfail`-ing tests,
loosening assertions, lowering coverage thresholds, or relaxing lint/CI to make a
red check pass is a blocker, full stop — if a test genuinely must change, that needs
explicit human sign-off, not a quiet edit. If you can't make the checks pass on the
merits, stop and report.

### 6. Adversarial review (Codex)

Commit your work first (so the review sees exactly what will ship), then run a Codex
adversarial review of the branch against `main`. Full invocation, output handling,
the iteration cap, and the fallback are in
**[references/codex-review.md](references/codex-review.md)** — follow it exactly.

The loop, in short:
1. Commit the implementation on the branch.
2. Run the adversarial review (`--base main`, foreground/`--wait`, extended timeout).
3. For each finding: **first confirm it's real and grounded in the actual code**
   (reviewers over-flag — don't fix phantom nits). Then **fix** material findings
   that affect correctness or the stated requirements (and re-run the review), or
   **rebut** them with a defensible, written reason. Note non-material findings as
   optional.
4. **Cap the loop at 2 iterations (3 absolute max).** Debugging effectiveness decays
   sharply after ~2–3 attempts, so thrashing won't help — if material findings still
   stand after the cap, stop and escalate to the user instead of looping.

Do not open the PR while a material, un-rebutted adversarial finding stands.

### 7. Commit, push, open the PR

Conventional Commit subjects scoped to the domain, e.g.
`fix(playback): guard against nil session on reconnect`. End commit messages with:

```
Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

Push and open a **ready** (non-draft) PR against `main`:

```bash
git push -u origin "$BRANCH"
gh pr create --base main --head "$BRANCH" --title "<conventional subject>" --body "$(cat <<'EOF'
## Problem
<what's broken / missing, grounded in the issue — and how you reproduced it>

## Approach
<what you changed and *why this approach* — the tradeoff, not a file list>

## Verification
<commands run and their result; the failing→passing test; manual checks>

## Adversarial review
<clean, or the material findings and how each was fixed/rebutted>

## Risks & follow-up
<rollout/migration/compatibility risks; any silo-android / silo-apple
follow-up; anything deferred>

Closes #<N>
<add "Part of #<epic>" if the issue serves a capability epic/sub-issue>

## AI-use disclosure
Implemented by Claude Code (issue-to-pr skill) with human oversight.
EOF
)"
```

End the PR body with:

```
🤖 Generated with [Claude Code](https://claude.com/claude-code)
```

For **UI changes**, attach before/after screenshots or a recording (the repo
requires this); if you can't capture them, explicitly flag in the PR that they're
still needed.

### 8. Report back

Give the user the PR URL, a 2–3 line summary, the verification result, the
adversarial-review outcome, any flagged risks or required client follow-up, and the
worktree path so they can inspect it.

## Long or multi-iteration runs

Performance degrades on long sessions, and context compaction can silently drop
constraints. So: keep a short progress note and a "failed approaches" list in the
scratchpad so you don't repeat dead ends; and after any compaction, re-read CLAUDE.md
and re-assert the hard guardrails (additive-only API, migration rules, no weakening
of tests) before continuing.

## Stop and ask

Pause and ask the user (don't push a PR) when:

- The issue is ambiguous, underspecified, or needs a product/UX decision.
- **The bug doesn't reproduce** on current `main`.
- The fix would require a non-additive API change, or a risky/irreversible migration.
- The change balloons into something large or architectural — propose a plan first.
- Checks can't be made to pass on the merits, or material adversarial findings remain
  after the iteration cap.
- A test/CI change would be needed to "pass" — get sign-off, don't do it silently.
- The right fix clearly belongs in a sibling repo (`silo-android`, `silo-apple`, a
  plugin/SDK repo) — see CLAUDE.md's multi-repo guidance.

In these cases, summarize what you found and what you'd do, and let the user decide.

## Notes

- Authorized to open ready PRs autonomously once checks pass and the adversarial
  review is clean — that's the durable instruction from setup. Not authorized to
  merge, force-push shared branches, or close issues by hand.
- Never invent a fix you can't verify or explain. An honest "here's what's uncertain"
  beats a confident wrong PR.
