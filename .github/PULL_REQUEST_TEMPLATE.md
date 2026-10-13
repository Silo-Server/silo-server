<!-- Follow "Write the description" in CONTRIBUTING.md: plain summary first, no restated
diff, no working history. Agents: then apply .agents/skills/unslop/SKILL.md to the
title and body before posting. -->

## Problem

Closes #NNN
<!-- One "Closes #NNN" line per issue this change fully resolves, so GitHub closes it
when the PR merges into main. Use "Closes Silo-Server/<repo>#NNN" for an issue in
another repository. Delete the line when the PR only partly addresses an issue. -->
Related issue: #NNN
<!-- An epic, sub-issue, or partly addressed issue this work serves and that should
stay open, or "N/A". An open issue is not required — the Problem section below must
stand on its own. -->
Validation tasks: #NNN C1
<!-- Until 1.0 ships: v1.0 board tasks and cases this change unblocks or changes,
e.g. "unblocks #1144 C3; changes #1200 C1". Write "none" when no validation task is
affected. -->

A short plain-language summary: what goes wrong, who it affects, and what this
change does about it. Details come after.

## Approach

What changed and why this way, and which surfaces and repositories it affects.
Mention an alternative only if a reviewer would ask about it. Do not walk through
the diff.

## Validation

One line per kind of check and its result; do not list tests by name. Name required
checks that were not run or did not pass. Include a short output excerpt only when it
explains a failure.
<!-- Do not include private domains, hostnames, IPs, Tailscale or Report Shelf URLs,
local paths, credentials, personal data, or private media details. -->

## Evidence

| Before | After |
| ------ | ----- |
| | |

Surface and build:
<!-- Required when this change alters what a user sees: UI, or which items or what
item details a screen shows, such as search results, home sections, recommendations,
metadata, artwork, sorting, or filtering. See "Show visible changes" in
CONTRIBUTING.md. Use before-and-after screenshots of the same screen and data for each
affected surface, and a short recording when motion, timing, or focus matters. For web
app and web admin changes also visible at phone width, include desktop and mobile web
captures (or show that mobile is unaffected). Crop or blur private details in
anything attached here. To keep captures private instead, publish them to
evidence.siloserver.org, where only you and Silo maintainers can open them, and
replace the table with
`Evidence: https://evidence.siloserver.org/r/silo-server/pr-<number>/`. When nothing
visible changes, replace this section's content with "Evidence: none, no user-visible
change". -->

## Risks

Migration, compatibility, security, operational, or release impact, or "None
identified".

## Checklist

- [ ] I read and can explain the complete diff.
- [ ] This pull request addresses one concern.
- [ ] The Evidence section shows every change a user can see, or says there is none.

## AI Disclosure

- Harness: exact agent harness or application, or "none"
- Tool(s): exact tool name(s), or "none"
- Model(s): exact model identifier(s) reported by each tool, or "n/a"
- Involvement: Fully AI-generated, human verified | AI-assisted | Human-written, AI-reviewed | No AI used
- Adversarial review: scope, method, findings, and resolutions, or "n/a" when this change does not require independent or adversarial review
