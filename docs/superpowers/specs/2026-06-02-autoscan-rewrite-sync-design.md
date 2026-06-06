# Autoscan: Sync Path Rewrites from Arr Root Folders

**Status:** Design approved, ready for implementation plan
**Date:** 2026-06-02
**Area:** `internal/autoscan`, `internal/api`, `web/src/pages/AdminRequests.tsx`
**Extends:** the autoscan feature (`docs/superpowers/specs/2026-06-02-autoscan-arr-polling-design.md`)

## Summary

Autoscan resolves an imported file path against Silo's media folders, but
Radarr/Sonarr often report files under paths that differ from how Silo mounts
the same content — so operators must hand-author per-root `path_rewrites`. For a
multi-instance, many-root deployment this can be dozens of rows.

This adds a **"Sync rewrites from arr"** convenience: for one instance, query its
root folders, match each root to a Silo media folder by **shared trailing path
segments**, and present the resulting `{from, to}` rewrites as a **preview** the
admin reviews and commits. Manual rewrites remain the source of truth; sync only
*fills the editor* — nothing persists until the admin saves.

The matcher is purely structural (no assumptions about any particular path
layout) so it helps any deployment whose arr and Silo share folder names at some
depth, and degrades gracefully (no shared names → "unmatched" → manual entry)
when they do not.

## Goals

- One-click suggestion of `path_rewrites` for an instance from its arr root folders.
- Correct disambiguation when multiple Silo folders share a leaf name.
- Preview-then-commit: the admin sees proposed/unmatched/ambiguous and saves
  through the existing per-source save. Manual rows are never removed or overwritten.
- Deployment-agnostic: no hard-coded paths; works for any structure; surfaces
  match confidence so weak matches can be rejected.

## Non-Goals

- No automatic/background writing of rewrites — the admin always commits.
- No new persistence path — apply is the existing `PUT /sources/{id}`.
- No case-insensitive matching (case-sensitive segments; correct on Linux). A
  toggle can be added later if needed.
- No "sync all instances at once" — per-instance, matching the editor UI.

## Design

### 1. Matching function (core, pure)

A single side-effect-free function in `internal/autoscan`:

```go
func suggestRewrites(arrRoots []string, siloFolderPaths []string, existing []PathRewrite) RewriteSuggestions
```

```go
type RewriteSuggestions struct {
    Proposed  []ProposedRewrite `json:"proposed"`
    Unmatched []string          `json:"unmatched"`
    Ambiguous []AmbiguousRoot   `json:"ambiguous"`
    Covered   []string          `json:"covered"`
}
type ProposedRewrite struct {
    From       string `json:"from"`         // arr root
    To         string `json:"to"`           // matched Silo folder path
    MatchDepth int    `json:"match_depth"`  // trailing segments shared (confidence)
}
type AmbiguousRoot struct {
    Root       string   `json:"root"`
    Candidates []string `json:"candidates"`
}
```

**Normalization** (applied to every path before comparison): backslashes → `/`
(reuse `normalizeSeparators`), strip a trailing `/`, collapse duplicate slashes.
This makes any path format (POSIX, Windows, single- or many-root) comparable.

**Per arr root:**
1. **Covered-first:** if any `existing` rewrite's `From` already matches the root
   (exact, or root starts with `From + "/"`, using the same boundary rule as
   `applyRewrites`), classify as **Covered** and skip — so sync never re-proposes
   what a manual or broad rule already handles.
2. Split both the root and each Silo folder path into segments; compute the
   **longest common trailing-segment suffix** (full-segment equality, so `4ktv7`
   does not match `4ktv70`).
3. Among Silo folders, take the maximum suffix length `> 0` and the folders that
   achieve it:
   - exactly one → **Proposed** `{From: root, To: folder, MatchDepth: n}`
   - more than one → **Ambiguous** `{root, candidates}`
   - zero → **Unmatched**

No deployment constants appear in the function; it operates only on the string
slices it is given.

### 2. Preview endpoint & service

**Endpoint:** `GET /api/v1/admin/autoscan/sources/{id}/rewrite-suggestions` →
`RewriteSuggestions` JSON (admin-gated, like the other autoscan routes).

**Service method** `Service.SuggestRewrites(ctx, integrationID) (RewriteSuggestions, error)`:
1. `Repository.GetSource(id)` — a new single-source variant of the existing join
   that returns `kind`/`base_url`/`api_key_ref`/`path_rewrites` even when the
   source is disabled. Not found → `ErrIntegrationNotFound`.
2. Decrypt the API key via the existing `SecretResolver`.
3. `RootFolderClient.RootFolders(ctx, baseURL, apiKey)` → arr root paths.
4. `FolderLister.ListFolderPaths(ctx)` → all Silo media-folder paths.
5. Return `suggestRewrites(arrRoots, siloPaths, source.PathRewrites)`.

**Two new narrow `Service` dependencies** (injected like `history`/`resolver`):
- `RootFolderClient.RootFolders(ctx, baseURL, apiKey string) ([]string, error)` —
  an `arrclient`-backed impl reusing `arrclient.ListRootFolders` (returns paths only).
- `FolderLister.ListFolderPaths(ctx) ([]string, error)` — a thin adapter over
  `catalog.FolderRepository.List()` flattening each folder's `Paths`.

**Wiring:** both constructed where the autoscan handler is built in `router.go`
(`deps.FolderRepo` already in scope; the arr client is trivial). The handler's
service interface gains `SuggestRewrites`.

**Errors:** instance not found → 404; arr unreachable / bad key → 502 with the
arr error surfaced; Silo folder-list failure → 500.

No new write path — the frontend merges `proposed` into the editor and the admin
saves via the existing `PUT /sources/{id}`.

### 3. Frontend (preview UI)

In `AutoscanSourceEditor` (`web/src/pages/AdminRequests.tsx`), add a **"Sync from
arr"** button beside Save. On click, a new hook
(`useAutoscanRewriteSuggestions`) calls the endpoint and opens a preview panel:

- **Proposed** — each row: `From → To` + a **confidence badge** from `match_depth`
  (e.g. "3 segments" strong, "1 segment — weak"), with a checkbox (default checked).
- **Unmatched** — info-only list of arr roots with no Silo match ("add manually").
- **Ambiguous** — arr root + candidate Silo paths (info-only; admin adds a manual row).

**"Add selected to rewrites"** merges the checked proposals into the editor's
existing rewrite rows, **deduped by `from`, never removing manual rows**. The
admin then clicks the normal **Save**. New types in `web/src/api/types.ts`
(`AutoscanRewriteSuggestions`, `ProposedRewrite`, `AmbiguousRoot`) and hook in
`web/src/hooks/queries/useAutoscan.ts`. Reuse existing
`Button`/`Badge`/`Dialog`/`Field` primitives.

### 4. Testing

- **`suggestRewrites`** — table-driven, fixtures spanning layouts: multi-segment
  match, single-segment unique, longest-suffix disambiguation (`Anime/Subs` vs
  `Anime2/Subs`), unmatched (no shared segment), already-covered via a broad
  prefix rule, an ambiguous tie, plus **unlike-this-deployment** cases
  (`/data/Movies` arr vs `/library/Films` Silo → unmatched; `/srv/tv/Show` vs
  `/tank/television/Show` → leaf match) and normalization (trailing slash,
  backslashes, duplicate slashes).
- **`Service.SuggestRewrites`** — fakes for source store / `RootFolderClient` /
  `FolderLister` / secrets: asserts proposed/unmatched/ambiguous; arr-unreachable
  surfaces the error.
- **Handler** — returns the JSON; 404 for missing instance; 502 when the arr is
  unreachable.
- **Frontend** — the button hits the endpoint; the preview renders all three
  buckets with confidence badges; "Add selected" merges deduped rows; Save
  persists via the existing mutation.

## Risks & open considerations

- **Weak (1-segment) matches** can be coincidental across very different layouts;
  mitigated by surfacing `match_depth` so the admin rejects them before saving.
- **Case-sensitivity** means case-mismatched mounts produce Unmatched rather than
  a wrong auto-match — a deliberate safe default; revisit only if requested.
- **Arr reachability** is required for sync (unlike the rest of autoscan which
  polls on a schedule); a 502 with the arr error tells the admin to check the
  instance, and manual entry still works.

## References

- Reused: `internal/requests/arrclient.ListRootFolders`,
  `internal/catalog/FolderRepository.List`, `internal/autoscan` (`PathRewrite`,
  `normalizeSeparators`, `Repository`, `Service`, the existing
  `PUT /autoscan/sources/{id}` save).
