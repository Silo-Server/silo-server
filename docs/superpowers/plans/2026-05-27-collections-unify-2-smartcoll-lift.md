# Collections Unification — Sub-project 2: Smartcoll Engine Lift

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the smart-collection rule engine from `internal/audiobooks/smartcoll/` to `internal/smartcoll/` so all media types (movie, series, audiobook) share one evaluator. Pure code move + import update; no logic changes.

**Architecture:** The 4-file package (~1060 LOC) currently lives in `internal/audiobooks/smartcoll/`. We relocate it verbatim and rewire its imports. The existing in-place audiobook caller (`internal/audiobooks/abs_smart_collection_store.go`) updates its import path. Sub-projects 3 and 4 will later call the engine from new sites (ABS adapter rewrites, section recipes); those plans cover their own wiring.

**Tech Stack:** Go. Standard `gopls`-style refactor: move, fix imports, run tests.

**Commands assume the repository root is the cwd.**

**Source spec:** `docs/superpowers/specs/2026-05-27-unified-audiobook-collections-design.md` §4.2, §4.6.

**Predecessor sub-project:** None — this lands independent of sub-project 1. Order doesn't matter.

---

## File map

**Move (4 files):**
- `internal/audiobooks/smartcoll/evaluator.go` → `internal/smartcoll/evaluator.go`
- `internal/audiobooks/smartcoll/evaluator_test.go` → `internal/smartcoll/evaluator_test.go`
- `internal/audiobooks/smartcoll/query.go` → `internal/smartcoll/query.go`
- `internal/audiobooks/smartcoll/query_test.go` → `internal/smartcoll/query_test.go`

**Modify (import path):**
- Every file that imports `github.com/Silo-Server/silo-server/internal/audiobooks/smartcoll` — change to `github.com/Silo-Server/silo-server/internal/smartcoll`.

**Add (new test, cross-type coverage):**
- `internal/smartcoll/cross_type_test.go` — three tests verifying audiobook-specific rules no-op against non-audiobook items.

---

## Task 1: Move the package directory

**Files:** see "Move" list above.

- [ ] **Step 1: Pre-check current contents**

```bash
ls internal/audiobooks/smartcoll/
wc -l internal/audiobooks/smartcoll/*.go
```

Expected: 4 files (evaluator.go, evaluator_test.go, query.go, query_test.go). If the contents differ, pause and report — the plan assumes this exact set.

- [ ] **Step 2: Verify nothing outside the package references it (yet) besides the known caller**

```bash
grep -rln '"github.com/Silo-Server/silo-server/internal/audiobooks/smartcoll"' --include="*.go" .
```

Expected: `internal/audiobooks/abs_smart_collection_store.go` and possibly its test, plus the package's own test files. If you find any other importer, pause and add it to the import-update list in Task 2.

- [ ] **Step 3: Create the new directory and move the files**

```bash
mkdir -p internal/smartcoll
git mv internal/audiobooks/smartcoll/evaluator.go      internal/smartcoll/evaluator.go
git mv internal/audiobooks/smartcoll/evaluator_test.go internal/smartcoll/evaluator_test.go
git mv internal/audiobooks/smartcoll/query.go          internal/smartcoll/query.go
git mv internal/audiobooks/smartcoll/query_test.go     internal/smartcoll/query_test.go
rmdir internal/audiobooks/smartcoll
```

`git mv` preserves blame history.

- [ ] **Step 4: Update the package declaration in each moved file**

The package name was `smartcoll`; it stays `smartcoll`. Open each moved file and **verify** the `package smartcoll` line is unchanged. If any file declares `package audiobooks_smartcoll` or similar, fix it to `package smartcoll`.

```bash
head -1 internal/smartcoll/*.go
```

Expected: every line reads `package smartcoll`. If any differ, fix with sed:

```bash
sed -i '1s/^package .*/package smartcoll/' internal/smartcoll/<filename>.go
```

- [ ] **Step 5: Build — this WILL fail until Task 2 runs**

```bash
go build ./...
```

Expected: compilation error in `internal/audiobooks/abs_smart_collection_store.go` because its import path now points at a non-existent directory. That's expected — Task 2 fixes it. **Do not commit yet.**

---

## Task 2: Update all imports

**Files:**
- Modify: every file from Task 1 Step 2's grep result.

- [ ] **Step 1: Update each importer**

The typical pattern in the existing caller is:

```go
import (
    // ...
    "github.com/Silo-Server/silo-server/internal/audiobooks/smartcoll"
)
```

Becomes:

```go
import (
    // ...
    "github.com/Silo-Server/silo-server/internal/smartcoll"
)
```

For each file in the Task 1 Step 2 grep result, run:

```bash
sed -i 's|"github.com/Silo-Server/silo-server/internal/audiobooks/smartcoll"|"github.com/Silo-Server/silo-server/internal/smartcoll"|' <file>
```

Or edit by hand using your editor's find-and-replace.

- [ ] **Step 2: Verify no stale import remains**

```bash
grep -rln '"github.com/Silo-Server/silo-server/internal/audiobooks/smartcoll"' --include="*.go" . || echo "clean"
```

Expected: `clean` (no matches).

- [ ] **Step 3: Build + test**

```bash
go build ./...
go test ./internal/smartcoll/ ./internal/audiobooks/... -short -timeout 60s
go vet ./internal/smartcoll/ ./internal/audiobooks/...
```

Expected: all green. The moved tests still pass because the engine logic is unchanged; the package path changed but the test code itself is package-local.

- [ ] **Step 4: Commit the move + import fix**

```bash
git add internal/smartcoll/ internal/audiobooks/
git commit -m "refactor(smartcoll): lift engine to internal/smartcoll

Moves the smart-collection rule engine out of internal/audiobooks/
so movie/TV smart collections can share the same evaluator.

No logic changes. Tests move with their package and continue to pass."
```

`git log --follow internal/smartcoll/evaluator.go` should show the prior history at the old path.

---

## Task 3: Cross-type smoke tests

**Files:**
- Create: `internal/smartcoll/cross_type_test.go`

The existing tests cover audiobook-specific rule evaluation. Sub-project 4 will start asking the engine to evaluate against movies + TV, so we add three small tests that nail down the no-op contract for audiobook-specific predicates against non-audiobook items.

- [ ] **Step 1: Find the audiobook-specific rule kinds**

```bash
grep -nE 'narrator|series_position|"type":"audiobook"' internal/smartcoll/query.go | head -10
```

Expected: rule-kind constants named like `RuleKindNarrator`, `RuleKindSeriesPosition`, or similar. Note the exact names — the test below references them. **If the rule kinds are not actually present (rule registration happens elsewhere), pause and report — the spec assumed they exist.**

- [ ] **Step 2: Read the existing query/evaluator test fixtures**

```bash
sed -n '/func Test/,/^func /p' internal/smartcoll/evaluator_test.go | head -60
```

Note the harness pattern used (how an evaluator is constructed in tests, how it's given items, how match-results are asserted). Mirror that pattern in the new tests.

- [ ] **Step 3: Write the failing tests**

Create `internal/smartcoll/cross_type_test.go`:

```go
package smartcoll

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestNarratorRuleNoopOnMovie verifies that an audiobook-specific narrator
// rule treats movie items as a no-op (matches everything, since the rule
// can't apply). This is the cross-type contract that lets the engine
// evaluate against mixed-type item sets without errors.
func TestNarratorRuleNoopOnMovie(t *testing.T) {
	// Adapt to the actual evaluator-construction API discovered in Step 2.
	// Expected: build an evaluator with a narrator-name predicate, evaluate
	// it against a movie item, assert no error AND the item is matched
	// (or excluded — pick whichever the existing audiobook tests treat as
	// "no-op" and document it).
	t.Skip("Implement once the evaluator API is read from existing tests")
}

// TestSeriesPositionRuleNoopOnSeries verifies the audiobook series_position
// rule has no effect on a TV series item (which has its own series concept,
// distinct from book series).
func TestSeriesPositionRuleNoopOnSeries(t *testing.T) {
	t.Skip("Implement once the evaluator API is read from existing tests")
}

// TestLibraryIdFilterAppliesToAllTypes verifies that the simple library_id
// predicate (the silo-native query_definition subset) evaluates correctly
// against any media type. This locks in compat with existing
// user_personal_collections rows after migration 156.
func TestLibraryIdFilterAppliesToAllTypes(t *testing.T) {
	t.Skip("Implement once the evaluator API is read from existing tests")
}

var _ = models.MediaItem{} // import-stability marker; remove once tests are real
```

- [ ] **Step 4: Fill in the test bodies**

Replace each `t.Skip(...)` with a real test using the harness pattern from the existing tests. The exact code depends on the evaluator's public surface (constructor, evaluate method, predicate registration). **Read `internal/smartcoll/evaluator.go` and the existing test file once before writing the bodies** — don't guess at the API.

If the evaluator's no-op behavior for type-mismatched rules turns out to be "exclude the item" rather than "match the item", adjust the assertions to reflect that — but document the chosen semantic in a comment above each test so future readers know which it is.

- [ ] **Step 5: Run the new tests**

```bash
go test ./internal/smartcoll/ -run "Cross|Noop|LibraryIdFilter" -v
```

Expected: all three PASS, and the existing tests in the package continue to pass.

- [ ] **Step 6: Commit**

```bash
git add internal/smartcoll/cross_type_test.go
git commit -m "test(smartcoll): cross-type evaluator contract

Locks in the no-op semantics for audiobook-specific rules evaluated
against non-audiobook items, plus library_id filter compat across
all types."
```

---

## Verification (after merge)

1. `git log --follow internal/smartcoll/evaluator.go` shows the prior history at `internal/audiobooks/smartcoll/evaluator.go` — confirms blame preserved.
2. `go test ./internal/smartcoll/ -v` runs both the moved tests and the three new cross-type tests, all passing.
3. `grep -rln 'internal/audiobooks/smartcoll' --include="*.go" .` returns empty.
4. No production behavior change. Smart collections in the ABS API still resolve via the engine; the engine just lives at a new path.

---

## Self-Review

**Spec coverage:**
- Move package to `internal/smartcoll/` ✓ (Task 1)
- Update import paths ✓ (Task 2)
- Cross-type evaluator tests ✓ (Task 3)

**Placeholder scan:** Task 3's `t.Skip(...)` lines are explicit failing-test stubs that Step 4 fills in. The instructions in Step 4 say to read the existing tests first rather than guess. Not a TBD — it's a "read-existing, then write" workflow with explicit guidance.

**Type consistency:** Package name `smartcoll` consistent. Import path `github.com/Silo-Server/silo-server/internal/smartcoll` consistent. Rule-kind names depend on what Step 1 of Task 3 discovers — the plan flags this explicitly.

**Risk:** Pure refactor. The chance of breakage is low (Go's import system catches missed renames at compile time). The cross-type tests are new functionality, but they assert behavior that already exists in the engine (no-op on type-mismatch) so they should pass without code changes.
