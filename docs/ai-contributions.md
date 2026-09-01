# AI-assisted contributions

AI-assisted code, documentation, review, and debugging are welcome. The person
who submits the work owns it: its accuracy, safety, scope, and maintainability.
This applies to pull requests and issues written by a person, an agent, or both.

## Required disclosure

> [!IMPORTANT]
> Every pull request and issue must say whether AI was involved: the exact
> harness, tool, and model identifiers, the level of involvement, and a summary
> of the independent or adversarial review. "No AI used" is a complete answer
> when it is true.

The pull request template uses this block. The required issue forms collect
the same fields individually:

```md
## AI Disclosure

- Harness: exact agent harness or application, or "none"
- Tool(s): exact tool name(s), or "none"
- Model(s): exact model identifier(s) reported by each tool, or "n/a"
- Involvement: Fully AI-generated, human verified | AI-assisted | Human-written, AI-reviewed | No AI used
- Adversarial review: scope, method, findings, and resolutions, or "n/a" when this change does not require independent or adversarial review
```

Disclosure is about provenance, not judgment. AI use is never by itself a reason
to reject a contribution. Exact model identifiers tell maintainers what produced
the work, let them reproduce the workflow when needed, and help them decide
between line-by-line review and a separate implementation.

## Contributor responsibility

Before submitting AI-assisted work:

1. Read every line of the final diff.
2. Understand the behavior well enough to explain the reasoning and tradeoffs.
3. Confirm that the APIs, configuration, schemas, commands, and repository paths
   it references exist.
4. Add and run focused tests for the changed behavior.
5. Run the repository gate in
   [CONTRIBUTING.md](../CONTRIBUTING.md#validate-your-change) against the
   complete diff, including `golangci-lint` and `make verify-local-paths`.
6. Exercise user-facing behavior manually when practical.
7. Look beyond the edited files for silent behavior changes, dead code,
   security regressions, and cross-layer interactions.
8. Run an independent or adversarial review and resolve its findings. For
   non-trivial changes, state the scope and method; a bare "no findings" is
   not a review summary.

"The AI suggested it" is not an explanation.

## Evidence standard

Use real commands and real observations. Paste the actual output into the pull
request and name any command that was not run or did not pass. Passing tests,
including AI-generated ones, do not replace code review, manual verification, or
thinking about system-level effects.

For bug reports, reproduce the problem on a real deployment before filing, keep
what you observed separate from what you suspect, and paste raw logs. Redact
credentials, tokens, personal data, and private media details, mark each
redaction, and do not paraphrase the rest. AI-generated analysis goes under
Technical notes, after the reproduction. The
[issue forms](https://github.com/Silo-Server/silo-server/issues/new/choose)
enforce the required fields.

## Prose pass

Before submitting, run a final readability pass over the pull request or issue
body using the repository's [Writing policy](../AGENTS.md#writing). Lead with
the outcome, use concrete plain language, and cut filler, repetition, stock
framing, and promotional claims. Preserve meaning, evidence, citations,
uncertainty, and established terminology; leave exact quotations, commands,
logs, identifiers, API names, and contractual language unchanged.

This is a readability step, not concealment. It must not alter facts, pasted
command output, or logs, and it does not loosen the disclosure requirement
above in any way.

## Integrity and enforcement

> [!WARNING]
> Fabricated evidence is an immediate block, including on a first offense:
> invented APIs, reproduction steps that were never run, synthesized logs,
> imagined vulnerabilities, unobserved bugs, and false test results.

Undisclosed AI use discovered after submission gets the contribution closed.
Repeated non-disclosure gets the contributor blocked. The violation is the
missing disclosure, not the AI.

## Review outcomes

Maintainers review the idea, the implementation, the evidence, and the fit with
the current codebase. They may request changes, narrow the scope, decline the
contribution, or accept the idea and implement it differently. A separate
implementation is a normal outcome and does not mean the proposal was wrong.
