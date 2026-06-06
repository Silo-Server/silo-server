# Audiobooks Stacked PR Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the oversized audiobook PR with a stacked series of smaller, reviewable PRs.

**Architecture:** Preserve the existing `feat/audiobooks` work as the source branch, then create branch cut points that expose one logical layer at a time. Each PR targets the previous branch so reviewers see only the incremental diff.

**Tech Stack:** Git, GitHub CLI, Go, pnpm/Vite, PostgreSQL migrations.

---

### Task 1: Close The Oversized PR

**Files:**
- Modify: GitHub PR #17 only.

- [ ] **Step 1: Post a replacement note**

Run:

```bash
gh pr comment 17 --repo Silo-Server/silo-server --body 'Closing this oversized PR in favor of a stacked series of smaller PRs. The existing branch is preserved as the source-of-truth while the stack is rebuilt into logical review units.'
```

Expected: GitHub prints the created comment URL.

- [ ] **Step 2: Close PR #17 without deleting the branch**

Run:

```bash
gh pr close 17 --repo Silo-Server/silo-server
```

Expected: PR #17 state becomes `CLOSED`; branch `feat/audiobooks` remains available.

### Task 2: Create Stacked Branches

**Files:**
- Modify: remote Git branches only.

- [ ] **Step 1: Create branch 1, foundation**

Branch name: `stack/audiobooks-foundation`

Scope:
- audiobook feature flag/settings
- audiobook and podcast schema foundations
- scanner support
- audiobook media item write path
- no native UI
- no ABS listener

- [ ] **Step 2: Create branch 2, native UI MVP**

Branch name: `stack/audiobooks-native-ui`

Base: `stack/audiobooks-foundation`

Scope:
- `/api/v1/audiobooks`
- audiobook detail/progress endpoints
- audiobook frontend route/sidebar/player MVP

- [ ] **Step 3: Create branch 3, ABS core**

Branch name: `stack/audiobooks-abs-core`

Base: `stack/audiobooks-native-ui`

Scope:
- ABS auth/login/refresh/logout
- ABS listener and feature-flag listener gating
- ABS library/items/play/progress
- ABS access control and security hardening

- [ ] **Step 4: Create branch 4, ABS collections**

Branch name: `stack/audiobooks-abs-collections`

Base: `stack/audiobooks-abs-core`

Scope:
- ABS bookmarks
- ABS collections
- ABS playlists
- ABS smart collections
- unified collection migration

- [ ] **Step 5: Create branch 5, extras and polish**

Branch name: `stack/audiobooks-extras`

Base: `stack/audiobooks-abs-collections`

Scope:
- podcasts/RSS
- stats
- author/series extras
- catalog audiobook filters/typeahead
- performance and enrichment cleanup

### Task 3: Open The Stacked PRs

**Files:**
- Modify: GitHub PRs only.

- [ ] **Step 1: Open PR 1**

Run:

```bash
gh pr create --repo Silo-Server/silo-server --base main --head stack/audiobooks-foundation --title 'feat(audiobooks): foundation and scanner support' --body 'First PR in the audiobook stack. Adds the schema/settings/scanner foundation without native UI or ABS compatibility.'
```

- [ ] **Step 2: Open PR 2**

Run:

```bash
gh pr create --repo Silo-Server/silo-server --base stack/audiobooks-foundation --head stack/audiobooks-native-ui --title 'feat(audiobooks): native API and player MVP' --body 'Second PR in the audiobook stack. Adds native Silo audiobook endpoints and the MVP web player UI.'
```

- [ ] **Step 3: Open PR 3**

Run:

```bash
gh pr create --repo Silo-Server/silo-server --base stack/audiobooks-native-ui --head stack/audiobooks-abs-core --title 'feat(audiobooks): Audiobookshelf compatibility core' --body 'Third PR in the audiobook stack. Adds ABS auth, listener, library/items/play/progress, and security hardening.'
```

- [ ] **Step 4: Open PR 4**

Run:

```bash
gh pr create --repo Silo-Server/silo-server --base stack/audiobooks-abs-core --head stack/audiobooks-abs-collections --title 'feat(audiobooks): ABS collections, playlists, and smart collections' --body 'Fourth PR in the audiobook stack. Adds ABS bookmarks, collections, playlists, smart collections, and unified collection storage.'
```

- [ ] **Step 5: Open PR 5**

Run:

```bash
gh pr create --repo Silo-Server/silo-server --base stack/audiobooks-abs-collections --head stack/audiobooks-extras --title 'feat(audiobooks): podcasts, catalog filters, and enrichment polish' --body 'Final PR in the audiobook stack. Adds podcast/RSS extras, audiobook catalog filters/typeahead, and scan/enrichment performance polish.'
```

### Task 4: Verify And Report

**Files:**
- Modify: none.

- [ ] **Step 1: Verify PR states**

Run:

```bash
gh pr list --repo Silo-Server/silo-server --state open --search 'audiobooks in:title' --json number,title,headRefName,baseRefName,url
```

Expected: five open stacked PRs with the base/head chain shown above.

- [ ] **Step 2: Run branch verification where feasible**

Run:

```bash
go test ./...
cd web && pnpm run build
```

Expected: both commands pass on the final stack tip.

