# Autoscan Rewrite-Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A "Sync rewrites from arr" action that suggests autoscan `path_rewrites` for an instance by matching its arr root folders to Silo media folders on shared trailing path segments, previewed and committed by the admin.

**Architecture:** A pure `suggestRewrites` matcher in `internal/autoscan`, a `Service.SuggestRewrites` method backed by two new injected deps (an arr root-folder client and a Silo folder lister), one read-only admin endpoint returning a preview, and a "Sync from arr" button in the existing Autoscan tab that merges proposals into the editor (admin saves via the existing per-source PUT). Manual rewrites stay the source of truth.

**Tech Stack:** Go (standard `testing`), React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-06-02-autoscan-rewrite-sync-design.md`

**Commands assume the repository root is the cwd.** Go tests: `go test ./internal/autoscan/...`. Frontend: `cd web && pnpm exec tsc -b && pnpm run lint`. Ensure the Go toolchain is on `PATH` (prepend its `bin` directory if `go` is missing). `internal/api`/`internal/api/handlers` cannot be compiled without libvips; verify those via `gofmt` + the Docker build.

---

## Phase 0 — Branch

- [ ] **Step 0.1: Confirm branch**

```bash
git rev-parse --abbrev-ref HEAD   # expect: feat/autoscan-arr-polling
```
This feature extends the autoscan branch. If you're elsewhere: `git checkout feat/autoscan-arr-polling`.

---

## Phase 1 — Core matcher (pure, TDD)

### Task 1: suggestRewrites + types

**Files:**
- Create: `internal/autoscan/suggest.go`
- Create: `internal/autoscan/suggest_test.go`

- [ ] **Step 1.1: Write the failing test**

`internal/autoscan/suggest_test.go`:
```go
package autoscan

import (
	"reflect"
	"testing"
)

func TestSuggestRewrites(t *testing.T) {
	silo := []string{
		"/mnt/media/happy/storage2/tvshows1",
		"/mnt/media/happy4k/4ktv7",
		"/mnt/media/storage/Anime/Subs",
		"/mnt/media/storage/Anime2/Subs",
		"/library/Films",
		"/tank/television/Show",
	}

	t.Run("multi-segment unique match", func(t *testing.T) {
		got := suggestRewrites([]string{"/mnt/happy/storage2/tvshows1"}, silo, nil)
		if len(got.Proposed) != 1 || got.Proposed[0].To != "/mnt/media/happy/storage2/tvshows1" || got.Proposed[0].MatchDepth != 2 {
			t.Fatalf("proposed=%+v", got.Proposed)
		}
	})

	t.Run("single-segment unique match (different parents)", func(t *testing.T) {
		got := suggestRewrites([]string{"/mnt/kodama/storage2/4ktv7"}, silo, nil)
		if len(got.Proposed) != 1 || got.Proposed[0].To != "/mnt/media/happy4k/4ktv7" || got.Proposed[0].MatchDepth != 1 {
			t.Fatalf("proposed=%+v", got.Proposed)
		}
	})

	t.Run("longest-suffix disambiguation", func(t *testing.T) {
		got := suggestRewrites([]string{"/mnt/kodama/storage1/Anime/Subs"}, silo, nil)
		if len(got.Proposed) != 1 || got.Proposed[0].To != "/mnt/media/storage/Anime/Subs" || got.Proposed[0].MatchDepth != 2 {
			t.Fatalf("expected Anime/Subs (depth 2), got %+v", got.Proposed)
		}
		if len(got.Ambiguous) != 0 {
			t.Fatalf("should not be ambiguous: %+v", got.Ambiguous)
		}
	})

	t.Run("unmatched when no shared segment", func(t *testing.T) {
		got := suggestRewrites([]string{"/data/Movies"}, silo, nil)
		if len(got.Unmatched) != 1 || got.Unmatched[0] != "/data/Movies" || len(got.Proposed) != 0 {
			t.Fatalf("got=%+v", got)
		}
	})

	t.Run("leaf match across unlike layouts", func(t *testing.T) {
		got := suggestRewrites([]string{"/srv/tv/Show"}, silo, nil)
		if len(got.Proposed) != 1 || got.Proposed[0].To != "/tank/television/Show" {
			t.Fatalf("got=%+v", got)
		}
	})

	t.Run("ambiguous tie", func(t *testing.T) {
		// a root whose only shared segment is "Subs" ties two folders
		got := suggestRewrites([]string{"/foo/bar/Subs"}, silo, nil)
		if len(got.Ambiguous) != 1 || len(got.Ambiguous[0].Candidates) != 2 {
			t.Fatalf("expected ambiguous with 2 candidates, got %+v", got.Ambiguous)
		}
	})

	t.Run("covered by existing rule", func(t *testing.T) {
		existing := []PathRewrite{{From: "/mnt/happy", To: "/mnt/media/happy"}}
		got := suggestRewrites([]string{"/mnt/happy/storage2/tvshows1"}, silo, existing)
		if len(got.Covered) != 1 || len(got.Proposed) != 0 {
			t.Fatalf("expected covered, got %+v", got)
		}
	})

	t.Run("normalization: trailing slash, backslashes, dup slashes", func(t *testing.T) {
		got := suggestRewrites([]string{`\mnt\happy\\storage2\tvshows1\`}, silo, nil)
		if len(got.Proposed) != 1 || got.Proposed[0].From != "/mnt/happy/storage2/tvshows1" || got.Proposed[0].To != "/mnt/media/happy/storage2/tvshows1" {
			t.Fatalf("normalization failed: %+v", got.Proposed)
		}
	})
}

func TestCommonSuffixLen(t *testing.T) {
	cases := []struct {
		a, b []string
		want int
	}{
		{[]string{"a", "b", "c"}, []string{"x", "b", "c"}, 2},
		{[]string{"4ktv7"}, []string{"happy4k", "4ktv7"}, 1},
		{[]string{"4ktv7"}, []string{"4ktv70"}, 0}, // full-segment, not substring
		{[]string{"a"}, []string{"b"}, 0},
	}
	for _, tc := range cases {
		if got := commonSuffixLen(tc.a, tc.b); got != tc.want {
			t.Fatalf("commonSuffixLen(%v,%v)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
```

- [ ] **Step 1.2: Run it — expect failure**

```bash
go test ./internal/autoscan/ -run 'TestSuggestRewrites|TestCommonSuffixLen' -v
```
Expected: FAIL (`suggestRewrites` undefined).

- [ ] **Step 1.3: Implement** `internal/autoscan/suggest.go`:
```go
package autoscan

import "strings"

// RewriteSuggestions is the result of matching arr root folders to Silo folders.
type RewriteSuggestions struct {
	Proposed  []ProposedRewrite `json:"proposed"`
	Unmatched []string          `json:"unmatched"`
	Ambiguous []AmbiguousRoot   `json:"ambiguous"`
	Covered   []string          `json:"covered"`
}

// ProposedRewrite is a suggested rewrite plus its confidence (shared trailing segments).
type ProposedRewrite struct {
	From       string `json:"from"`
	To         string `json:"to"`
	MatchDepth int    `json:"match_depth"`
}

// AmbiguousRoot is an arr root that tied across multiple Silo folders.
type AmbiguousRoot struct {
	Root       string   `json:"root"`
	Candidates []string `json:"candidates"`
}

// normalizePath makes a path comparable: backslashes -> '/', collapse duplicate
// slashes, strip a trailing slash (but keep a bare "/").
func normalizePath(p string) string {
	p = normalizeSeparators(strings.TrimSpace(p))
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

func segments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// commonSuffixLen counts equal trailing segments (full-segment equality).
func commonSuffixLen(a, b []string) int {
	i, j, n := len(a)-1, len(b)-1, 0
	for i >= 0 && j >= 0 && a[i] == b[j] {
		n++
		i--
		j--
	}
	return n
}

// coveredBy reports whether an existing rewrite already matches root (same
// boundary rule as applyRewrites).
func coveredBy(root string, existing []PathRewrite) bool {
	for _, rw := range existing {
		from := strings.TrimRight(strings.TrimSpace(rw.From), "/")
		if from == "" {
			continue
		}
		if root == from || strings.HasPrefix(root, from+"/") {
			return true
		}
	}
	return false
}

// suggestRewrites matches each arr root to the Silo folder sharing the most
// trailing path segments (unique winner). Roots already handled by an existing
// rewrite are reported as Covered; roots with no shared segment as Unmatched;
// ties as Ambiguous. Pure: no I/O, no deployment constants.
func suggestRewrites(arrRoots, siloFolderPaths []string, existing []PathRewrite) RewriteSuggestions {
	siloNorm := make([]string, 0, len(siloFolderPaths))
	siloSegs := make([][]string, 0, len(siloFolderPaths))
	for _, p := range siloFolderPaths {
		n := normalizePath(p)
		if n == "" {
			continue
		}
		siloNorm = append(siloNorm, n)
		siloSegs = append(siloSegs, segments(n))
	}

	var out RewriteSuggestions
	for _, raw := range arrRoots {
		root := normalizePath(raw)
		if root == "" {
			continue
		}
		if coveredBy(root, existing) {
			out.Covered = append(out.Covered, root)
			continue
		}
		rootSegs := segments(root)
		best := 0
		var winners []string
		for i, segs := range siloSegs {
			n := commonSuffixLen(rootSegs, segs)
			if n == 0 {
				continue
			}
			if n > best {
				best, winners = n, []string{siloNorm[i]}
			} else if n == best {
				winners = append(winners, siloNorm[i])
			}
		}
		switch {
		case best == 0:
			out.Unmatched = append(out.Unmatched, root)
		case len(winners) == 1:
			out.Proposed = append(out.Proposed, ProposedRewrite{From: root, To: winners[0], MatchDepth: best})
		default:
			out.Ambiguous = append(out.Ambiguous, AmbiguousRoot{Root: root, Candidates: winners})
		}
	}
	return out
}
```

- [ ] **Step 1.4: Run it — expect pass; commit**

```bash
go test ./internal/autoscan/ -run 'TestSuggestRewrites|TestCommonSuffixLen' -v
gofmt -l internal/autoscan/suggest.go internal/autoscan/suggest_test.go
git add internal/autoscan/suggest.go internal/autoscan/suggest_test.go
git commit -m "feat(autoscan): suffix-match rewrite suggester"
```

---

## Phase 2 — Service support

### Task 2: Repository.GetSource

**Files:**
- Modify: `internal/autoscan/repository.go`

- [ ] **Step 2.1: Implement** `GetSource` (single-source join, includes disabled). Add after `ListEnabledSources`:
```go
// GetSource returns one instance's autoscan state joined with its
// request_integrations row (kind/base_url/api_key_ref), regardless of enabled.
func (r *Repository) GetSource(ctx context.Context, integrationID string) (*Source, error) {
	row := r.pool.QueryRow(ctx, sourceSelect+` WHERE ri.id = $1`, integrationID)
	src, err := scanSource(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrIntegrationNotFound, integrationID)
		}
		return nil, fmt.Errorf("get autoscan source: %w", err)
	}
	return &src, nil
}
```
(`errors`, `pgx`, `fmt`, `ErrIntegrationNotFound`, and `sourceSelect`/`scanSource` already exist in the file.)

- [ ] **Step 2.2: Build + commit**

```bash
go build ./internal/autoscan/... && go test ./internal/autoscan/...
git add internal/autoscan/repository.go
git commit -m "feat(autoscan): GetSource single-source lookup"
```

### Task 3: RootFolderClient + FolderLister adapters

**Files:**
- Create: `internal/autoscan/suggest_deps.go`

- [ ] **Step 3.1: Implement the two adapters + interfaces**
```go
package autoscan

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/requests/arrclient"
)

// RootFolderClient lists a Radarr/Sonarr instance's configured root folder paths.
type RootFolderClient interface {
	RootFolders(ctx context.Context, baseURL, apiKey string) ([]string, error)
}

// FolderLister lists every Silo media-folder path.
type FolderLister interface {
	ListFolderPaths(ctx context.Context) ([]string, error)
}

type arrRootFolderClient struct{ httpClient *http.Client }

// NewArrRootFolderClient returns a RootFolderClient backed by the shared arrclient.
func NewArrRootFolderClient(httpClient *http.Client) RootFolderClient {
	return &arrRootFolderClient{httpClient: httpClient}
}

func (c *arrRootFolderClient) RootFolders(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	client := arrclient.New(baseURL, apiKey, c.httpClient)
	folders, err := arrclient.ListRootFolders(ctx, client)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(folders))
	for _, f := range folders {
		if f.Path != "" {
			paths = append(paths, f.Path)
		}
	}
	return paths, nil
}

type catalogFolderLister struct{ repo *catalog.FolderRepository }

// NewCatalogFolderLister adapts catalog.FolderRepository to FolderLister.
func NewCatalogFolderLister(repo *catalog.FolderRepository) FolderLister {
	return &catalogFolderLister{repo: repo}
}

func (l *catalogFolderLister) ListFolderPaths(ctx context.Context) ([]string, error) {
	folders, err := l.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, f := range folders {
		paths = append(paths, f.Paths...)
	}
	return paths, nil
}
```
Note: `autoscan` already depends on `catalog` (via `scantrigger`) and on `requests/arrclient` (via `history.go`), so these imports add no new module-level coupling and no cycle (`catalog` does not import `autoscan`).

- [ ] **Step 3.2: Build + commit**

```bash
go build ./internal/autoscan/...
git add internal/autoscan/suggest_deps.go
git commit -m "feat(autoscan): arr root-folder client + Silo folder lister"
```

### Task 4: Service.SuggestRewrites

**Files:**
- Modify: `internal/autoscan/service.go`
- Modify: `internal/autoscan/service_test.go`

- [ ] **Step 4.1: Write the failing test** (append to `service_test.go`):
```go
type fakeRootFolders struct {
	paths []string
	err   error
}

func (f fakeRootFolders) RootFolders(context.Context, string, string) ([]string, error) {
	return f.paths, f.err
}

type fakeFolderLister struct{ paths []string }

func (f fakeFolderLister) ListFolderPaths(context.Context) ([]string, error) { return f.paths, nil }

type sourceGetterStore struct {
	fakeStore
	src *Source
}

func (s *sourceGetterStore) GetSource(context.Context, string) (*Source, error) { return s.src, nil }

func TestSuggestRewritesService(t *testing.T) {
	store := &sourceGetterStore{src: &Source{IntegrationID: "i1", Kind: "sonarr", BaseURL: "http://x", APIKeyRef: "k"}}
	svc := NewService(store, &fakeHistory{}, fakeResolver{}, &recordingQueuer{}, allowSuppressor{}, nil)
	svc.SetRewriteResolvers(
		fakeRootFolders{paths: []string{"/mnt/happy/storage2/tvshows1", "/data/Movies"}},
		fakeFolderLister{paths: []string{"/mnt/media/happy/storage2/tvshows1"}},
	)
	got, err := svc.SuggestRewrites(context.Background(), "i1")
	if err != nil {
		t.Fatalf("SuggestRewrites: %v", err)
	}
	if len(got.Proposed) != 1 || got.Proposed[0].To != "/mnt/media/happy/storage2/tvshows1" {
		t.Fatalf("proposed=%+v", got.Proposed)
	}
	if len(got.Unmatched) != 1 || got.Unmatched[0] != "/data/Movies" {
		t.Fatalf("unmatched=%+v", got.Unmatched)
	}
}
```
> The `Store` interface needs a `GetSource` method so the service can call it. Add `GetSource(ctx context.Context, integrationID string) (*Source, error)` to the `Store` interface in `service.go`. `*Repository` already implements it (Task 2). The test's `sourceGetterStore` embeds the existing `fakeStore` and adds `GetSource`. If `fakeStore` does not satisfy the widened interface on its own, that's fine — only `sourceGetterStore` is used here; but ensure the package still compiles (other tests construct `fakeStore` directly and pass it where `Store` is required — add a `GetSource` method to `fakeStore` returning `(nil, nil)` so it still satisfies `Store`).

- [ ] **Step 4.2: Run it — expect failure**

```bash
go test ./internal/autoscan/ -run TestSuggestRewritesService -v
```
Expected: FAIL (`SetRewriteResolvers`/`SuggestRewrites` undefined).

- [ ] **Step 4.3: Implement** in `service.go`. Add fields + setter + method, and add `GetSource` to `Store`:
```go
// add to the Store interface:
//   GetSource(ctx context.Context, integrationID string) (*Source, error)

// add fields to Service struct:
//   rootFolders RootFolderClient
//   folders     FolderLister

// SetRewriteResolvers wires the deps used by SuggestRewrites (optional; only the
// admin-facing service needs them).
func (s *Service) SetRewriteResolvers(rootFolders RootFolderClient, folders FolderLister) {
	s.rootFolders = rootFolders
	s.folders = folders
}

// SuggestRewrites matches an instance's arr root folders to Silo media folders.
func (s *Service) SuggestRewrites(ctx context.Context, integrationID string) (RewriteSuggestions, error) {
	if s.rootFolders == nil || s.folders == nil {
		return RewriteSuggestions{}, fmt.Errorf("autoscan: rewrite suggestion not configured")
	}
	src, err := s.store.GetSource(ctx, integrationID)
	if err != nil {
		return RewriteSuggestions{}, err
	}
	apiKey := src.APIKeyRef
	if s.secrets != nil && apiKey != "" {
		if resolved, rerr := s.secrets.Get(ctx, apiKey); rerr == nil && resolved != "" {
			apiKey = resolved
		}
	}
	arrRoots, err := s.rootFolders.RootFolders(ctx, src.BaseURL, apiKey)
	if err != nil {
		return RewriteSuggestions{}, fmt.Errorf("autoscan: list arr root folders: %w", err)
	}
	siloPaths, err := s.folders.ListFolderPaths(ctx)
	if err != nil {
		return RewriteSuggestions{}, fmt.Errorf("autoscan: list silo folders: %w", err)
	}
	return suggestRewrites(arrRoots, siloPaths, src.PathRewrites), nil
}
```
Add `GetSource` to `fakeStore` in `service_test.go` (`func (f *fakeStore) GetSource(context.Context, string) (*Source, error) { return nil, nil }`).

- [ ] **Step 4.4: Run tests — expect pass; commit**

```bash
go test ./internal/autoscan/...
gofmt -l internal/autoscan/service.go internal/autoscan/service_test.go
git add internal/autoscan/service.go internal/autoscan/service_test.go
git commit -m "feat(autoscan): Service.SuggestRewrites"
```

---

## Phase 3 — API endpoint

### Task 5: Handler + route

**Files:**
- Modify: `internal/api/handlers/autoscan.go`
- Modify: `internal/api/router.go`
- Modify: `internal/api/handlers/autoscan_test.go`

- [ ] **Step 5.1: Extend the handler's service interface + add the handler method**

In `autoscan.go`, add `SuggestRewrites` to the `autoscanTriggerer` interface:
```go
type autoscanTriggerer interface {
	PollOnce(ctx context.Context) error
	SuggestRewrites(ctx context.Context, integrationID string) (autoscan.RewriteSuggestions, error)
}
```
Add the handler:
```go
func (h *AutoscanHandler) HandleRewriteSuggestions(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	suggestions, err := h.svc.SuggestRewrites(r.Context(), id)
	if err != nil {
		if errors.Is(err, autoscan.ErrIntegrationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Autoscan source not found")
			return
		}
		// arr unreachable / bad key / folder list failure
		writeError(w, http.StatusBadGateway, "arr_unreachable", "Could not load root folders from the arr instance")
		return
	}
	writeJSON(w, http.StatusOK, suggestions)
}
```
(`errors`, `autoscan`, `chi`, `strings`, `writeError`, `writeJSON` already imported.)

- [ ] **Step 5.2: Mount the route** in `router.go`, inside the autoscan route block:
```go
r.Get("/autoscan/sources/{id}/rewrite-suggestions", autoscanHandler.HandleRewriteSuggestions)
```

- [ ] **Step 5.3: Wire the service deps** in `router.go` where `autoscanSvc` is built (after `autoscan.NewService(...)`):
```go
autoscanSvc.SetRewriteResolvers(
	autoscan.NewArrRootFolderClient(nil),
	autoscan.NewCatalogFolderLister(deps.FolderRepo),
)
```
(This is inside the existing `if deps.FolderRepo != nil && deps.LibraryScanQueue != nil` block, so `deps.FolderRepo` is non-nil here.)

- [ ] **Step 5.4: Update the handler test fake**

In `autoscan_test.go`, the fake satisfying `autoscanTriggerer` (e.g. `fakeAutoscanTriggerer`) gains:
```go
func (f *fakeAutoscanTriggerer) SuggestRewrites(context.Context, string) (autoscan.RewriteSuggestions, error) {
	return autoscan.RewriteSuggestions{}, nil
}
```
Add a test asserting `HandleRewriteSuggestions` returns 200 with JSON for a known id (mirror the existing handler tests).

- [ ] **Step 5.5: Verify (limited) + commit**

```bash
# ensure the Go toolchain is on PATH
go vet ./internal/autoscan/...
gofmt -l internal/api/handlers/autoscan.go internal/api/router.go internal/api/handlers/autoscan_test.go
# internal/api cannot compile here (libvips) — Docker verifies later.
git add internal/api/handlers/autoscan.go internal/api/router.go internal/api/handlers/autoscan_test.go
git commit -m "feat(autoscan): rewrite-suggestions endpoint"
```

---

## Phase 4 — Frontend

### Task 6: Types + hook

**Files:**
- Modify: `web/src/api/types.ts`
- Modify: `web/src/hooks/queries/useAutoscan.ts`

- [ ] **Step 6.1: Types** — add to `types.ts`:
```ts
export interface AutoscanProposedRewrite {
  from: string;
  to: string;
  match_depth: number;
}
export interface AutoscanAmbiguousRoot {
  root: string;
  candidates: string[];
}
export interface AutoscanRewriteSuggestions {
  proposed: AutoscanProposedRewrite[];
  unmatched: string[];
  ambiguous: AutoscanAmbiguousRoot[];
  covered: string[];
}
```

- [ ] **Step 6.2: Hook** — add to `useAutoscan.ts` a mutation that fetches suggestions for an id:
```ts
export function useAutoscanRewriteSuggestions() {
  return useMutation({
    mutationFn: (id: string) =>
      api<AutoscanRewriteSuggestions>(`/admin/autoscan/sources/${id}/rewrite-suggestions`),
    onError: (err) =>
      toast.error(err instanceof Error ? err.message : "Could not sync rewrites from the arr instance"),
  });
}
```
Match the import/`api`/`toast`/`useMutation` conventions already in the file (it imports `api` and `toast` already; reuse them; import the new type).

- [ ] **Step 6.3: Lint + commit**

```bash
cd web && pnpm exec tsc -b && pnpm run lint && pnpm exec prettier --check src/api/types.ts src/hooks/queries/useAutoscan.ts
git add web/src/api/types.ts web/src/hooks/queries/useAutoscan.ts
git commit -m "feat(web): autoscan rewrite-suggestions types and hook"
```

### Task 7: Sync button + preview in AutoscanSourceEditor

**Files:**
- Modify: `web/src/pages/AdminRequests.tsx`

- [ ] **Step 7.1: Add the button + preview**

In `AutoscanSourceEditor`:
- Call `const suggest = useAutoscanRewriteSuggestions();` and add local state `const [preview, setPreview] = useState<AutoscanRewriteSuggestions | null>(null);` and `const [selected, setSelected] = useState<Set<string>>(new Set());`.
- Add a **"Sync from arr"** `Button` beside Save: `onClick={async () => { const s = await suggest.mutateAsync(source.integration_id); setPreview(s); setSelected(new Set(s.proposed.map((p) => p.from))); }}` (disabled while `suggest.isPending`).
- When `preview` is set, render a panel (reuse the file's `Dialog` or an inline bordered panel) with three sections:
  - **Proposed:** each `proposed[]` row — a checkbox bound to `selected.has(p.from)` (toggle updates the set), `from → to`, and a `Badge` showing confidence: `match_depth >= 2 ? "${match_depth} segments" : "1 segment — weak"` (destructive/secondary variant for weak).
  - **Unmatched:** muted list of `unmatched[]` ("no Silo match — add manually if needed").
  - **Ambiguous:** list of `ambiguous[]` (`root` + its `candidates` joined) — info only.
  - An **"Add selected to rewrites"** button that merges the checked proposals into the editor's existing rewrites state, deduped by `from` (skip any whose `from` already exists), then `setPreview(null)`. Do NOT auto-save — the admin then clicks the existing **Save**.
- Keep the existing manual path-rewrite editor unchanged; sync only appends rows to it.

Reuse existing `Button`/`Badge`/`Field`/`Dialog` primitives and the editor's existing rewrites-state setter.

- [ ] **Step 7.2: Lint + commit**

```bash
cd web && pnpm exec tsc -b && pnpm run lint && pnpm exec prettier --check src/pages/AdminRequests.tsx
git add web/src/pages/AdminRequests.tsx
git commit -m "feat(web): autoscan sync-rewrites preview"
```

---

## Phase 5 — Verification

### Task 8: Full verification

- [ ] **Step 8.1: Go**

```bash
# ensure the Go toolchain is on PATH
go test ./internal/autoscan/... 2>&1 | tail
go vet ./internal/autoscan/...
gofmt -l internal/autoscan internal/api/handlers/autoscan.go internal/api/router.go
```
Expected: all pass; vet/gofmt clean.

- [ ] **Step 8.2: Frontend**

```bash
cd web && pnpm exec tsc -b && pnpm run lint && pnpm run format:check
```

- [ ] **Step 8.3: Docker build (closes the libvips gap)**

```bash
docker build --build-arg BUILD_REVISION=$(git rev-parse HEAD) --build-arg BUILD_DIRTY=false -t silo-server:autoscan .
```
Expected: exit 0 (handlers + router + frontend compile).

- [ ] **Step 8.4: Smoke (optional)**

On a deploy with arr instances configured: open an instance in the Autoscan tab, click **Sync from arr**, confirm the preview shows proposed (with confidence) + unmatched + ambiguous, "Add selected" appends deduped rows, and Save persists them.

---

## Self-review notes (resolved)

- **Spec §1 matcher** → Task 1 (incl. normalization, covered-first, suffix depth, unlike-deployment fixtures). **§2 endpoint/service** → Tasks 2–5 (`GetSource`, `RootFolderClient`/`FolderLister`, `SuggestRewrites`, endpoint, 404/502 mapping). **§3 frontend** → Tasks 6–7. **§4 testing** → Tasks 1,4,5 + Task 8.
- **Manual-preserving**: sync only *appends* deduped rows to the editor; the admin saves via the existing `PUT` (Task 7). Never auto-writes.
- **Generic**: `suggestRewrites` takes plain string slices, no deployment constants; tests include `/data/Movies` and `/srv/tv` fixtures (Task 1).
- **Optional deps**: `SetRewriteResolvers` keeps `main.go`'s poll service untouched; `SuggestRewrites` errors if unset (Task 4).
- **Type consistency**: `RewriteSuggestions`/`ProposedRewrite`/`AmbiguousRoot` (Go) ↔ `AutoscanRewriteSuggestions`/`AutoscanProposedRewrite`/`AutoscanAmbiguousRoot` (TS); `match_depth` JSON tag matches.
