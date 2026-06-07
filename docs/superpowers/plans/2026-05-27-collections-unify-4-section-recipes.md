# Collections Unification — Sub-project 4: Section Recipes for Audiobooks

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `page_sections` media-type-parameterized so existing recipes can include audiobooks, and add two new audiobook-flavored recipes (`continue_listening`, `by_audiobook_series`).

**Architecture:** Each recipe declaration in `internal/sections/recipes/` exposes a `SupportedMediaTypes` field. `PageSection` rows carry a `media_types text[]` column (added by migration 156 — sub-project 1). The fetchers add `WHERE mi.type = ANY($media_types)` to their queries. Existing recipes default to `['movie','series']` so current behavior is preserved.

**Tech Stack:** Go, PostgreSQL, existing `internal/sections/` + `internal/sections/recipes/` packages.

**Commands assume the repository root is the cwd.**

**Source spec:** `docs/superpowers/specs/2026-05-27-unified-audiobook-collections-design.md` §4.4.

**Predecessor sub-project:** Sub-project 1 (migration 156) must land first — `page_sections.media_types` is required.

---

## File map

**Modify:**
- `internal/sections/types.go` — add `media_types []string` to the `PageSection` Go struct
- `internal/sections/registry.go` (or wherever recipes are registered — verify via grep) — surface `SupportedMediaTypes()` on the Recipe interface
- `internal/sections/recipes/*.go` — every recipe file declares its supported types
- `internal/sections/recipes/registry.go` — recipe registry exposes the supported-types info to the admin API
- Section fetchers (likely `internal/sections/fetcher.go` or similar) — wire `media_types` into the SQL `WHERE` clause

**Create:**
- `internal/sections/recipes/continue_listening.go` + test
- `internal/sections/recipes/by_audiobook_series.go` + test

**Add to admin API:**
- `internal/api/handlers/recipes.go` — the `GET /api/sections/recipes` endpoint surfaces the per-recipe supported-media-types in its response

---

## Task 1: Audit current recipe shape

This task is read-only — produces a working note for the rest of the plan.

- [ ] **Step 1: Map the current recipe interface**

```bash
grep -nE "type Recipe interface|type RecipeDefinition|func.*Type\(\) string|func.*Definition\(\)|SupportedMediaTypes" internal/sections/recipes/*.go internal/sections/types.go internal/sections/registry.go 2>/dev/null
```

Read the `Recipe` interface and any current registration code. Note:
- The exact method signatures recipes already implement.
- How a recipe is registered (`init()` calls? explicit `Register`? slice literal?).
- Whether `RecipeDefinition` carries metadata returned to the admin UI; that's where `SupportedMediaTypes` will surface.

- [ ] **Step 2: Map the fetcher's current SQL**

```bash
grep -nE "FetchOne|FetchSection|SELECT.*FROM media_items|type Fetcher" internal/sections/*.go 2>/dev/null | head -20
```

Find the function that turns a `ResolvedSection` into a list of `media_items`. Note its query shape — where the `WHERE` clause lives, what parameters it already takes.

- [ ] **Step 3: Note the section persistence shape**

```bash
grep -nE "INSERT INTO page_sections|UPDATE page_sections|SELECT.*FROM page_sections" internal/sections/*.go internal/api/handlers/sections*.go 2>/dev/null | head -10
```

Find where `page_sections` rows are read and written. The `media_types` column needs to be read/written there too.

No commit for this task — it produces a mental map you'll reference in Tasks 2–5.

---

## Task 2: Add `media_types` to the `PageSection` struct + serde

**Files:**
- Modify: `internal/sections/types.go` (`PageSection` struct)
- Modify: wherever `page_sections` rows are scanned and written (from Task 1 Step 3)

- [ ] **Step 1: Write failing test for `media_types` round-trip**

The test goes in whichever package owns the `PageSection` repository — likely `internal/sections/` itself. If a repository test file exists, append; otherwise create one:

```go
func TestPageSectionMediaTypesRoundTrip(t *testing.T) {
	if testing.Short() { t.Skip("requires test DB") }
	ctx := context.Background()
	pool := newTestPool(t) // adapt to project's test pool harness

	repo := NewSectionRepository(pool)

	sec := &PageSection{
		Name:        "Audiobook test",
		RecipeType:  "library_staples",
		MediaTypes:  []string{"audiobook"},
		// ...fill in other required fields from the struct definition
	}
	if err := repo.Create(ctx, sec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetByID(ctx, sec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !reflect.DeepEqual(got.MediaTypes, []string{"audiobook"}) {
		t.Errorf("MediaTypes = %v, want [audiobook]", got.MediaTypes)
	}
}
```

Adapt the constructor name, struct fields, and DB harness to actual project shape (found in Task 1).

- [ ] **Step 2: Run the test to confirm failure**

Run: `go test ./internal/sections/ -run TestPageSectionMediaTypesRoundTrip -v`
Expected: FAIL — the `MediaTypes` field doesn't exist yet.

- [ ] **Step 3: Add the field to `PageSection`**

In `internal/sections/types.go`, find the `PageSection` struct (or whatever it's called per Task 1's audit) and add:

```go
type PageSection struct {
    // ...existing fields
    MediaTypes []string `json:"media_types" db:"media_types"`
}
```

If the project's struct tagging convention differs, follow that convention. The DB column is `text[]`; the Go type is `[]string`. pgx's default decoder handles this directly.

- [ ] **Step 4: Update the repository's read/write SQL**

For each `INSERT`/`UPDATE` against `page_sections`, add `media_types` to the column list and bind parameter list. For each `SELECT`, add it to the projection and scan into the new field.

Example pattern (adapt to actual code):

```go
const insertSQL = `
    INSERT INTO page_sections (id, name, recipe_type, ..., media_types)
    VALUES ($1, $2, $3, ..., $N::text[])
`
// ...
_, err := pool.Exec(ctx, insertSQL, sec.ID, sec.Name, sec.RecipeType, ..., sec.MediaTypes)
```

For SELECTs, add `media_types` to the projection and `&sec.MediaTypes` to the scan list.

- [ ] **Step 5: Run the test, verify pass**

Run: `go test ./internal/sections/ -run TestPageSectionMediaTypesRoundTrip -v`
Expected: PASS.

- [ ] **Step 6: Build + broader test**

```bash
go build ./...
go test ./internal/sections/ ./internal/api/handlers/ -short -timeout 90s
```

Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add internal/sections/types.go internal/sections/*.go internal/api/handlers/sections*.go
git commit -m "feat(sections): expose page_sections.media_types in Go

Adds MediaTypes []string to PageSection + repo read/write paths.
Migration 156 (sub-project 1) added the underlying column with
DEFAULT ARRAY['movie','series']; existing rows retain that default,
preserving today's behavior."
```

Only stage the files you actually modified. Verify with `git diff --cached --stat` before commit.

---

## Task 3: Recipe interface — `SupportedMediaTypes`

**Files:**
- Modify: `internal/sections/recipes/registry.go` (interface declaration)
- Modify: every `*.go` recipe file in `internal/sections/recipes/` (each gains a one-line method)

- [ ] **Step 1: Find the Recipe interface**

```bash
grep -n "type Recipe " internal/sections/recipes/*.go internal/sections/*.go
```

Note the file + line of the interface declaration.

- [ ] **Step 2: Add a `SupportedMediaTypes()` method to the interface**

In whichever file declares the interface:

```go
type Recipe interface {
    // ...existing methods
    SupportedMediaTypes() []string
}
```

- [ ] **Step 3: Implement on every existing recipe**

For each `*Recipe` struct in `internal/sections/recipes/`, add the method. Existing recipes default to movies+series to preserve behavior:

```go
func (libStaple) SupportedMediaTypes() []string         { return []string{"movie", "series"} }
func (moodRecipe) SupportedMediaTypes() []string        { return []string{"movie", "series"} }
func (handPickedRecipe) SupportedMediaTypes() []string  { return []string{"movie", "series"} }
// ...and so on for every recipe in the directory
```

Use this enumeration as a checklist (from `ls internal/sections/recipes/*.go`, excluding tests):

- admin_curated_list
- custom
- discovery
- editorial
- hand_picked
- library_staples
- mood
- personalized

Each gets one method. Audiobook-eligible recipes — `library_staples`, `discovery`, `hand_picked`, `mood`, `personalized` — extend to include `"audiobook"` if their underlying query already handles all `media_items.type` values transparently. Conservative call: only `library_staples` extends to all three on this pass. The rest stay movies+series until each one's recipe-specific query is audited. **Bias toward the conservative default.**

Specifically:

```go
func (libStaple) SupportedMediaTypes() []string { return []string{"movie", "series", "audiobook"} }
```

- [ ] **Step 4: Build**

```bash
go build ./...
```

Expected: clean. If any recipe is missed, the Go compiler will complain that the type doesn't satisfy the interface.

- [ ] **Step 5: Commit**

```bash
git add internal/sections/recipes/
git commit -m "feat(recipes): SupportedMediaTypes per recipe

Adds the SupportedMediaTypes() method to the Recipe interface and
implements it on each existing recipe. Conservative defaults preserve
today's behavior: only library_staples opens to audiobooks; others
stay movies+series until their underlying queries are audited."
```

---

## Task 4: Fetcher applies the `media_types` filter

**Files:**
- Modify: the section fetcher implementation file (found in Task 1 Step 2)

- [ ] **Step 1: Write a failing test**

Append to (or create) the fetcher's test file. Pattern:

```go
func TestFetcherFiltersByMediaTypes(t *testing.T) {
	if testing.Short() { t.Skip("requires test DB") }
	ctx := context.Background()
	pool := newTestPool(t)

	// Seed: one movie, one audiobook, both eligible for the same recipe.
	seedTestMediaItem(t, pool, "mov-1", "movie", "Test Movie")
	seedTestMediaItem(t, pool, "ab-1", "audiobook", "Test Book")

	fetcher := NewFetcher(pool)

	t.Run("media_types movies only", func(t *testing.T) {
		got, _ := fetcher.FetchOne(ctx, ResolvedSection{
			SectionType: "library_staples",
			MediaTypes:  []string{"movie"},
		}, nil, nil, 1, "", catalog.AccessFilter{})
		assertContains(t, got.Items, "mov-1")
		assertExcludes(t, got.Items, "ab-1")
	})

	t.Run("media_types audiobook only", func(t *testing.T) {
		got, _ := fetcher.FetchOne(ctx, ResolvedSection{
			SectionType: "library_staples",
			MediaTypes:  []string{"audiobook"},
		}, nil, nil, 1, "", catalog.AccessFilter{})
		assertContains(t, got.Items, "ab-1")
		assertExcludes(t, got.Items, "mov-1")
	})

	t.Run("media_types both", func(t *testing.T) {
		got, _ := fetcher.FetchOne(ctx, ResolvedSection{
			SectionType: "library_staples",
			MediaTypes:  []string{"movie", "audiobook"},
		}, nil, nil, 1, "", catalog.AccessFilter{})
		assertContains(t, got.Items, "mov-1", "ab-1")
	})
}
```

Adapt `ResolvedSection`, `FetchOne` signature, `seedTestMediaItem`, `assertContains` / `assertExcludes` to the project's actual shapes. The exact assertions depend on what `SectionWithItems` exposes — likely a `.Items []SectionItem` slice.

- [ ] **Step 2: Run the test, verify fail**

```bash
go test ./internal/sections/ -run TestFetcherFiltersByMediaTypes -v
```

Expected: FAIL — `ResolvedSection.MediaTypes` field doesn't exist yet OR the fetcher SQL doesn't filter.

- [ ] **Step 3: Add `MediaTypes` to `ResolvedSection`**

In `internal/sections/types.go` (or wherever `ResolvedSection` lives):

```go
type ResolvedSection struct {
    // ...existing fields
    MediaTypes []string
}
```

The resolver code that builds `ResolvedSection` from a `PageSection` row needs one more line to pass `MediaTypes` through. Find it:

```bash
grep -n "ResolvedSection{" internal/sections/*.go internal/api/handlers/*.go
```

For each instance that builds one from a `PageSection`, add `MediaTypes: section.MediaTypes,`.

- [ ] **Step 4: Wire the filter into the fetcher SQL**

In the fetcher implementation, find the query that pulls `media_items`. Add to its `WHERE` clause:

```go
const query = `
    SELECT ...
    FROM media_items mi
    WHERE ...
      AND mi.type = ANY($N::text[])
    ...
`
// ...
rows, err := pool.Query(ctx, query, ..., mediaTypes)
```

If the fetcher dispatches to per-recipe resolvers, the filter goes inside each resolver's query. Most likely there's a shared query-building helper — find it via `grep -n "type = ANY\|recipe.*Resolve" internal/sections/`.

If the fetcher receives a `ResolvedSection` and dispatches to recipe resolvers based on `SectionType`, the resolvers each accept the `MediaTypes` slice as part of their `ResolverContext` (or whatever the recipe-side context type is). Add a field to that context type and thread it through.

**Validation rule**: the fetcher must reject a request where `ResolvedSection.MediaTypes` contains a type not in the recipe's `SupportedMediaTypes()`. Add this guard in the dispatch path:

```go
recipe, ok := registry.Get(resolved.SectionType)
if !ok { return result, fmt.Errorf("unknown recipe %q", resolved.SectionType) }
supported := setOf(recipe.SupportedMediaTypes())
for _, mt := range resolved.MediaTypes {
    if _, ok := supported[mt]; !ok {
        return result, fmt.Errorf("recipe %q does not support media type %q", resolved.SectionType, mt)
    }
}
```

`setOf` is a 3-line helper that returns `map[string]struct{}` from a slice.

- [ ] **Step 5: Run the test, verify pass**

```bash
go test ./internal/sections/ -run TestFetcherFiltersByMediaTypes -v
```

Expected: PASS for all three subtests.

- [ ] **Step 6: Commit**

```bash
git add internal/sections/
git commit -m "feat(sections): fetcher honors media_types filter

ResolvedSection carries a MediaTypes slice. Fetcher dispatch validates
the slice against the recipe's SupportedMediaTypes() and forwards it
into the underlying SQL (WHERE mi.type = ANY(\$N::text[]))."
```

---

## Task 5: New recipe — `continue_listening`

**Files:**
- Create: `internal/sections/recipes/continue_listening.go`
- Create: `internal/sections/recipes/continue_listening_test.go`

The audiobook analog of `continue_watching`. Surfaces audiobook items the user has progress on but hasn't finished.

- [ ] **Step 1: Read the existing `continue_watching` recipe (if present)**

```bash
ls internal/sections/recipes/ | grep -i continue
grep -rln "continue_watching\|user_watch_progress" internal/sections/recipes/ internal/sections/
```

Identify the closest analog. Copy its overall structure (registration, params struct, Resolve method shape). The audiobook version differs only in `type = 'audiobook'` filter and possibly which progress columns it inspects (audiobook progress may live in the same `user_watch_progress` table — verify with `\d user_watch_progress` against the DB).

- [ ] **Step 2: Write a failing test**

```go
package recipes

import (
	"context"
	"testing"
)

func TestContinueListeningRecipeRegistered(t *testing.T) {
	r, ok := registry.Get("continue_listening")
	if !ok {
		t.Fatal("continue_listening recipe not registered")
	}
	got := r.SupportedMediaTypes()
	want := []string{"audiobook"}
	if !equalStringSlices(got, want) {
		t.Errorf("SupportedMediaTypes = %v, want %v", got, want)
	}
}

func TestContinueListeningResolvesProgressedAudiobooks(t *testing.T) {
	if testing.Short() { t.Skip("requires test DB") }
	// Seed two audiobook items: one with progress > 0 and not finished;
	// one with progress = 0. Run the recipe's Resolve method. Assert only
	// the first is returned.
	t.Skip("Implement once existing continue_watching test pattern is read")
}
```

- [ ] **Step 3: Implement the recipe**

```go
// Package recipes
package recipes

import (
	"context"
	"encoding/json"
	"time"
)

type continueListeningRecipe struct{}

type ContinueListeningParams struct {
	// e.g. max items, library scope; mirror continue_watching's shape
	MaxItems int `json:"max_items"`
}

func (continueListeningRecipe) Type() string                   { return "continue_listening" }
func (continueListeningRecipe) NewParams() any                 { return &ContinueListeningParams{} }
func (continueListeningRecipe) DefaultCacheTTL() time.Duration { return 5 * time.Minute }
func (continueListeningRecipe) SupportedMediaTypes() []string  { return []string{"audiobook"} }

func (continueListeningRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	const q = `
		SELECT mi.content_id, mi.title, mi.year, mi.poster_path
		FROM media_items mi
		JOIN user_watch_progress uwp ON uwp.content_id = mi.content_id
		WHERE mi.type = 'audiobook'
		  AND uwp.user_id = $1
		  AND uwp.position_seconds > 0
		  AND COALESCE(uwp.completed, false) = false
		ORDER BY uwp.updated_at DESC
		LIMIT $2
	`
	params := rc.Params.(*ContinueListeningParams)
	limit := params.MaxItems
	if limit <= 0 || limit > 50 { limit = 20 }

	rows, err := rc.Pool.Query(rc.Ctx, q, rc.UserID, limit)
	if err != nil { return ResolvedItems{}, fmt.Errorf("continue_listening: %w", err) }
	defer rows.Close()

	var items []ResolvedItem
	for rows.Next() {
		var it ResolvedItem
		if err := rows.Scan(&it.ContentID, &it.Title, &it.Year, &it.PosterPath); err != nil {
			return ResolvedItems{}, fmt.Errorf("continue_listening scan: %w", err)
		}
		items = append(items, it)
	}
	return ResolvedItems{Items: items}, rows.Err()
}

func (continueListeningRecipe) Validate(raw json.RawMessage) error {
	var p ContinueListeningParams
	return json.Unmarshal(raw, &p)
}

func (continueListeningRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:        "continue_listening",
		Name:        "Continue Listening",
		Description: "Audiobooks you've started but haven't finished.",
	}
}

func init() {
	registry.Register(continueListeningRecipe{})
}
```

**Adapt the field names** (`PosterPath`, `Pool`, `UserID`, `Ctx`, `Params`, etc.) to whatever the existing recipes use. **Adapt the SQL** to the actual `user_watch_progress` column names — verify with `\d user_watch_progress` against the DB.

- [ ] **Step 4: Run tests, verify pass**

```bash
go test ./internal/sections/recipes/ -run TestContinueListening -v
```

Expected: registration test PASS; the resolve test still skipped (or implemented and passing).

- [ ] **Step 5: Commit**

```bash
git add internal/sections/recipes/continue_listening.go internal/sections/recipes/continue_listening_test.go
git commit -m "feat(recipes): continue_listening audiobook rail

Audiobook analog of continue_watching. Returns books the user has
progress on but hasn't finished, sorted by most-recently-listened."
```

---

## Task 6: New recipe — `by_audiobook_series`

**Files:**
- Create: `internal/sections/recipes/by_audiobook_series.go`
- Create: `internal/sections/recipes/by_audiobook_series_test.go`

Surfaces books grouped by series, drawing from the `audiobook_series` table populated by the scanner.

- [ ] **Step 1: Write the registration test**

```go
func TestByAudiobookSeriesRecipeRegistered(t *testing.T) {
	r, ok := registry.Get("by_audiobook_series")
	if !ok { t.Fatal("by_audiobook_series not registered") }
	if got := r.SupportedMediaTypes(); !equalStringSlices(got, []string{"audiobook"}) {
		t.Errorf("SupportedMediaTypes = %v, want [audiobook]", got)
	}
}
```

- [ ] **Step 2: Implement the recipe**

```go
type byAudiobookSeriesRecipe struct{}

type ByAudiobookSeriesParams struct {
	SeriesName string `json:"series_name"`
}

func (byAudiobookSeriesRecipe) Type() string                   { return "by_audiobook_series" }
func (byAudiobookSeriesRecipe) NewParams() any                 { return &ByAudiobookSeriesParams{} }
func (byAudiobookSeriesRecipe) DefaultCacheTTL() time.Duration { return 30 * time.Minute }
func (byAudiobookSeriesRecipe) SupportedMediaTypes() []string  { return []string{"audiobook"} }

func (byAudiobookSeriesRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	const q = `
		SELECT mi.content_id, mi.title, mi.year, mi.poster_path
		FROM media_items mi
		JOIN audiobook_series s ON s.content_id = mi.content_id
		WHERE mi.type = 'audiobook'
		  AND s.series_name = $1
		ORDER BY COALESCE(s.series_index, 9999), mi.title
	`
	params := rc.Params.(*ByAudiobookSeriesParams)
	if params.SeriesName == "" {
		return ResolvedItems{}, fmt.Errorf("by_audiobook_series: series_name required")
	}
	rows, err := rc.Pool.Query(rc.Ctx, q, params.SeriesName)
	if err != nil { return ResolvedItems{}, fmt.Errorf("by_audiobook_series: %w", err) }
	defer rows.Close()

	var items []ResolvedItem
	for rows.Next() {
		var it ResolvedItem
		if err := rows.Scan(&it.ContentID, &it.Title, &it.Year, &it.PosterPath); err != nil {
			return ResolvedItems{}, fmt.Errorf("by_audiobook_series scan: %w", err)
		}
		items = append(items, it)
	}
	return ResolvedItems{Items: items}, rows.Err()
}

func (byAudiobookSeriesRecipe) Validate(raw json.RawMessage) error {
	var p ByAudiobookSeriesParams
	if err := json.Unmarshal(raw, &p); err != nil { return err }
	if p.SeriesName == "" { return fmt.Errorf("series_name is required") }
	return nil
}

func (byAudiobookSeriesRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:        "by_audiobook_series",
		Name:        "By Audiobook Series",
		Description: "Books in a specific audiobook series, ordered by series position.",
	}
}

func init() {
	registry.Register(byAudiobookSeriesRecipe{})
}
```

- [ ] **Step 3: Run test, verify pass**

```bash
go test ./internal/sections/recipes/ -run TestByAudiobookSeries -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/sections/recipes/by_audiobook_series.go internal/sections/recipes/by_audiobook_series_test.go
git commit -m "feat(recipes): by_audiobook_series rail

Surfaces books in a named series ordered by series_index. Backed by
the audiobook_series table populated by the scanner."
```

---

## Task 7: Surface `SupportedMediaTypes` in admin API response

**Files:**
- Modify: `internal/api/handlers/recipes.go` (`GET /api/sections/recipes` handler)

The admin section-builder UI needs to know which recipes support audiobooks so it can offer the media-types multi-select to the operator. The handler's response shape adds one field per recipe.

- [ ] **Step 1: Read the current handler**

```bash
cat internal/api/handlers/recipes.go
```

Note the response struct shape — probably `[]RecipeInfo` or similar with `Type`, `Name`, `Description`.

- [ ] **Step 2: Add `SupportedMediaTypes` to the response**

In the response struct definition, add:

```go
type recipeInfo struct {
    Type                string   `json:"type"`
    Name                string   `json:"name"`
    Description         string   `json:"description"`
    SupportedMediaTypes []string `json:"supported_media_types"`
}
```

In the handler body, set `SupportedMediaTypes: recipe.SupportedMediaTypes()`.

- [ ] **Step 3: Test**

If the project has a handler test for `GET /api/sections/recipes`, extend it. Otherwise the manual verification step covers this:

```bash
curl -sH "Authorization: Bearer $TOKEN" http://localhost:8090/api/sections/recipes | jq '.[] | select(.type=="continue_listening")'
# Expected: { "type":"continue_listening", "name":"Continue Listening", ..., "supported_media_types":["audiobook"] }
```

- [ ] **Step 4: Commit**

```bash
git add internal/api/handlers/recipes.go
git commit -m "feat(api): expose supported_media_types in /api/sections/recipes

Admin UI section-builder uses this to gate the media-types
multi-select per recipe."
```

---

## Verification (after merge)

1. Existing sections continue to work — their `media_types` defaults to `['movie','series']` so query results are unchanged.
2. Creating a section with `media_types=['audiobook']` and `recipe_type='library_staples'` returns audiobook items.
3. Two new recipes registered:
   ```bash
   curl -sH "Authorization: Bearer $TOKEN" http://localhost:8090/api/sections/recipes | jq '[.[] | .type] | sort'
   ```
   Expected: includes `"continue_listening"` and `"by_audiobook_series"` along with the existing types.
4. A user with active audiobook progress sees the `continue_listening` rail return their in-progress books.
5. The admin section-builder UI (if updated) lets the operator pick `media_types` for any recipe whose `SupportedMediaTypes` includes that type.

---

## Self-Review

**Spec coverage:**
- `page_sections.media_types` Go-side serde ✓ (Task 2)
- `Recipe.SupportedMediaTypes()` on every recipe ✓ (Task 3)
- Fetcher filter by `media_types` ✓ (Task 4)
- `continue_listening` recipe ✓ (Task 5)
- `by_audiobook_series` recipe ✓ (Task 6)
- Admin API exposes per-recipe types ✓ (Task 7)

**Placeholder scan:** Test bodies in Tasks 5 and 6 are partly stubbed with `t.Skip(...)` to be filled in by reading the existing `continue_watching` test pattern. That's a deliberate "read-existing-then-write" instruction, not a TBD. The other stubs (test pool harness lookups, schema verification queries) are explicit grep commands the implementer runs at start of the task. No abstract "implement appropriately."

**Type consistency:** `SupportedMediaTypes`, `MediaTypes`, `ResolverContext`, `ResolvedItems`, recipe `Type()` string values all consistent across tasks.

**Risk:** Lowest-risk of the 4 sub-projects. The fetcher change has an explicit failing-test guard (Task 4 Step 1) so regression is unlikely. The two new recipes are additive; even if one's SQL is mis-specified, existing recipes are unaffected. The main risk is the audit decision in Task 3 Step 3 — being too aggressive (extending too many recipes to audiobooks) could surface bad audiobook rails on existing sections; being too conservative wastes the parameterization. Conservative is the safer default.
