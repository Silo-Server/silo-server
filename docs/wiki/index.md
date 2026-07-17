---
title: Silo Wiki
description: User-facing Silo documentation written in Markdown for in-repo use and Wiki.js sync.
summary: Index of digestible Silo docs by audience and subject.
tags:
  - silo
  - docs
  - wiki
audience:
  - end-user
  - operator
last_reviewed: 2026-04-11
related: []
---

# Silo Wiki

This directory is the repo-local home for user-digestible Silo documentation. Pages here
should stay portable Markdown so they can render cleanly in GitHub and sync into Wiki.js without
rewriting.

## Sections

## Getting Started

- No pages yet.

## Features

- No pages yet.

## Admin

- [Supported Media Folder Structures and Naming](admin/media-folder-and-naming.md) - Accurate
  reference for the folder layouts and filenames Silo can scan and match today.
- [Collection Templates](admin/collection-templates.md) - Curated, one-click starting points for
  synced library collections sourced from TMDB, Trakt, and MDBList.
- [Local NFO Metadata](admin/nfo-local-metadata.md) - Supported NFO sidecar fields, how they merge
  with online providers, and the naming-supplies-structure contract.

## Deployment

- No pages yet.

## Troubleshooting

- No pages yet.

## Editing Rules

- Keep pages in `docs/wiki/` digestible for the intended audience.
- Prefer updating existing pages over creating duplicates.
- Use YAML frontmatter and portable Markdown.
- Add `## Source References` sections instead of copying code into docs.
- Reserve `docs/wiki/` for end-user and operator docs. Keep architecture, specs, and plans in
  `docs/architecture/` or `docs/superpowers/`.
- When a page is added, replace the matching `No pages yet.` line with bullet entries in this form:
  `- [Page Title](section/file.md) - one-line summary`
- When a section already has pages, append a new bullet instead of adding prose.
