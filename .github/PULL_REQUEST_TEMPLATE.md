<!-- Follow "Write the description" in CONTRIBUTING.md: plain summary first, no restated
diff, no working history. Agents: then apply .agents/skills/unslop/SKILL.md to the
title and body before posting. -->

## Problem

Related issue: #NNN
<!-- Link an issue when one covers this work; otherwise write "Related issue: N/A".
An open issue is not required — the Problem section below must stand on its own. -->
Validation tasks: #NNN C1
<!-- Until 1.0 ships: v1.0 board tasks and cases this change unblocks or changes
(see "1.0 validation" in AGENTS.md), e.g. "unblocks #1144 C3; changes #1200 C1".
Write "none" when no validation task is affected. -->

A short plain-language summary: what goes wrong, who it affects, and what this
change does about it. Details come after.

## Approach

What changed and why this way. Mention an alternative only if a reviewer would ask
about it. Do not walk through the diff.

## Validation

One line per kind of check and its result; do not list tests by name. Name required
checks that were not run or did not pass. Include a short output excerpt only when it
explains a failure.
<!-- Do not include private domains, hostnames, IPs, Tailscale or Report Shelf URLs,
local paths, credentials, personal data, or private media details. Attach screenshots
or recordings only when the user explicitly requests them. -->

## Risks

Migration, compatibility, security, or operational impact, or "None identified".

## Checklist

- [ ] I read and can explain the complete diff.
- [ ] This pull request addresses one concern.

## AI Disclosure

- Harness: exact agent harness or application, or "none"
- Tool(s): exact tool name(s), or "none"
- Model(s): exact model identifier(s) reported by each tool, or "n/a"
- Involvement: Fully AI-generated, human verified | AI-assisted | Human-written, AI-reviewed | No AI used
- Adversarial review: scope, method, findings, and resolutions, or "n/a" when this change does not require independent or adversarial review
