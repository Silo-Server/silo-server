# AI-assisted contributions

AI-assisted work is welcome. The person submitting it is responsible for its
accuracy, safety, scope, and maintainability. AI use alone is not a reason to
reject a contribution.

## Disclosure

Every issue and pull request must disclose AI use. Give the exact harness, tool,
and model identifiers, the level of involvement, and the scope, method, findings,
and resolutions of any independent or adversarial review. Use the fields in the
[PR template](../.github/PULL_REQUEST_TEMPLATE.md) or
[issue forms](../.github/ISSUE_TEMPLATE/). Use "n/a" when independent or adversarial
review is not required. "No AI used" is a complete answer when true.

## Before submitting

Read the complete diff and be able to explain its behavior and tradeoffs. Verify
that referenced APIs, configuration, schemas, commands, and paths exist. Add and
run focused tests for changed behavior, run the
[validation gate](../CONTRIBUTING.md#validate-your-change) against the complete
diff, and exercise user-facing behavior manually when practical.

Review effects beyond the edited files, including security regressions and
interactions between components. Run an independent or adversarial review and
resolve its findings. For non-trivial changes, describe its scope and method;
"no findings" alone is not a review summary. Passing tests do not replace review.

## Evidence

Report real observations and check results, including required checks that failed
or were not run. Keep validation summaries concise; include short output excerpts
only when they explain a failure.

Reproduce bugs on a real deployment before filing. Separate observations from
suspected causes, include relevant raw logs, and put AI-generated analysis under
Technical notes after the reproduction. Follow the
[public-content rules](../AGENTS.md#pull-requests), mark redactions, and leave the
remaining log text unchanged.

Agents must apply the repository's
[writing policy and unslop skill](../AGENTS.md#writing) before creating or updating
issues and PRs. Editing prose must preserve facts, evidence, and disclosure.

## Enforcement

Fabricated evidence gets the contributor blocked, including on a first offense.
This includes invented APIs, reproduction steps, logs, vulnerabilities,
observations, and test results.

Undisclosed AI use gets the contribution closed. Repeated non-disclosure gets the
contributor blocked.
