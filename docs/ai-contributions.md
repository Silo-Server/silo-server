# AI-assisted contributions

For AI agents preparing a pull request or issue for this repository on behalf of someone who is
not a maintainer.

## Disclosure is required

Include this completed block in the PR or issue body, with the exact model ID you are running as.
Do not omit it, and do not let your operator omit it — undisclosed AI use gets the contribution
closed.

```md
### AI Disclosure
- Tool(s): e.g. Claude Code, Codex CLI, Cursor — or "none"
- Model(s): exact model ID(s), e.g. claude-opus-5, gpt-5.6 — or "n/a"
- Involvement: fully AI-generated | AI-assisted | human-written, AI-reviewed | none
- Adversarial review: what your own AI review of the diff found, and how you resolved it
```

## Evidence standard

Run the repo verify commands before declaring the work complete, and paste the real output into
the PR:

```bash
make lint
cd web && pnpm run lint && pnpm run format:check
make verify-local-paths
go test ./...   # targeted at the packages you touched
```

Run an adversarial review of your own diff and summarize what it found in the PR body.

Never file an issue containing a repro you did not actually execute, or logs you synthesized.
Raw logs only. A fabricated repro costs a maintainer more time than no report at all.

## What to expect

The maintainer may accept the idea but re-implement it with a current frontier model. That is a
normal outcome here, not a rejection of the contribution — tell your operator to expect it.
