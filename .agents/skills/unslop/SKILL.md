---
name: unslop
description: Rewrite prose to remove AI-sounding patterns while preserving meaning, evidence, citations, terminology, uncertainty, and the intended tone. Use when the user says "unslop", asks to make writing sound human or less AI-generated, or when another workflow explicitly requests a final prose pass for a human-facing issue, pull request, Discord reply, document, or status update. Do not apply to code, logs, quoted text, exact commands, or machine-readable contracts.
---

# Unslop

Make the writing sound like a thoughtful person wrote it. Protect accuracy before style.

## Preserve the contract

- Keep the meaning, factual claims, citations, uncertainty, and scope unchanged.
- Keep exact quotations, code, commands, identifiers, API names, log text, and contractual language exact.
- Do not invent personality, opinions, anecdotes, confidence, or informality that the author did not supply.
- Do not remove a hedge that communicates a real evidence limit.
- Match the audience. A Discord reply, architecture note, and incident report should not share one voice.

## Rewrite

1. Identify the audience, purpose, and expected tone from context.
2. Read [references/patterns.md](references/patterns.md).
3. Cut filler, generic framing, repetition, and promotional puffery. Preserve substantive claims; style editing is not fact-checking or permission to delete them.
4. Replace vague language with the concrete fact, mechanism, number, or action when the source supports it.
5. Prefer plain words and active voice. Keep necessary domain terminology consistent rather than cycling synonyms.
6. Vary sentence length naturally. Use formatting only when it makes the content easier to scan.
7. Read the result once as the intended recipient. Fix anything that still sounds templated, promotional, overeager, or sterile.

## Respond

When asked to rewrite, return the revised text only unless the user asks for commentary or alternatives. When asked to review rather than rewrite, identify the highest-impact problems and provide a proposed revision.
