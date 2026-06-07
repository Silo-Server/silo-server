# ABS Smart Collections Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land rule-based dynamic audiobook collections on the silo ABS surface. DSL package + 6 endpoints + 1 migration + bookmark-store extension for batch count hydration.

**Architecture:** New `internal/audiobooks/smartcoll/` package holds the DSL types + evaluator (ported from continuum). HTTP handlers in `internal/audiobooks/abs/` follow the same shape as manual collections from sub-project 2. Eval is pure-Go in-memory; per-user state hydrated in 2 batched SQL calls.

**Tech Stack:** Go, `chi/v5`, `pgx/v5`, `oklog/ulid/v2`, internal `package abs` + new `package smartcoll`.

**Commands assume `/opt/silo-server` as cwd.**

**Source spec:** `docs/superpowers/specs/2026-05-26-abs-smart-collections-design.md`. Re-read sections 4-9 before each task.

**Predecessor plans:** `docs/superpowers/plans/2026-05-26-abs-bookmarks-implementation.md`, `docs/superpowers/plans/2026-05-26-abs-collections-playlists-implementation.md`. The TDD ordering, test harness reuse, commit-message style, and "don't stage pre-existing modifications" rule all carry over.

**Continuum reference paths** (READ these — do NOT copy continuum-specific imports/types verbatim; adapt):
- `/opt/continuum_plugins_bak/continuum-plugin-audiobooks/internal/smartcoll/query.go` (326 lines)
- `/opt/continuum_plugins_bak/continuum-plugin-audiobooks/internal/smartcoll/evaluator.go` (554 lines)
- `/opt/continuum_plugins_bak/continuum-plugin-audiobooks/internal/smartcoll/evaluator_test.go` (266 lines)
- `/opt/continuum_plugins_bak/continuum-plugin-audiobooks/internal/abs/smart_collection_handler.go` (301 lines)

---

## File map

**Create:**
- `migrations/153_abs_smart_collections.up.sql` + `.down.sql`
- `internal/audiobooks/smartcoll/query.go`
- `internal/audiobooks/smartcoll/query_test.go`
- `internal/audiobooks/smartcoll/evaluator.go`
- `internal/audiobooks/smartcoll/evaluator_test.go`
- `internal/audiobooks/abs/smart_collections.go` — interface + types + serialiser
- `internal/audiobooks/abs/smart_collections_handler.go` — 6 handlers
- `internal/audiobooks/abs/smart_collections_handler_test.go`
- `internal/audiobooks/abs/smart_collections_envelope_test.go`
- `internal/audiobooks/abs_smart_collection_store.go` — pgx-backed store

**Modify:**
- `internal/audiobooks/abs/bookmarks.go` — add `CountByUser` method to `BookmarkStore` interface
- `internal/audiobooks/abs/bookmarks_handler_test.go` — extend `memBookmarkStore` with `CountByUser`
- `internal/audiobooks/abs_bookmark_store.go` — implement `CountByUser` on `ABSBookmarkStore`
- `internal/audiobooks/abs/handler.go` — add `SmartCollectionStore` field; register 6 routes
- `internal/audiobooks/service.go` — wire `&ABSSmartCollectionStore{...}` in `BuildABSHandler`

---

## Task 1: Migration 153 + bookmark-store extension

**Files:**
- Create: `migrations/153_abs_smart_collections.up.sql` + `.down.sql`
- Modify: `internal/audiobooks/abs/bookmarks.go`
- Modify: `internal/audiobooks/abs/bookmarks_handler_test.go`
- Modify: `internal/audiobooks/abs_bookmark_store.go`

- [ ] **Step 1: Write the migration**

`migrations/153_abs_smart_collections.up.sql`:

```sql
-- Smart Collections — rule-based dynamic groupings of audiobooks.
-- The query_def JSONB column stores the DSL tree (see
-- internal/audiobooks/smartcoll/query.go). Profile-scoped per the
-- established convention; is_public allows cross-user reads with
-- personalization stripped at eval time.

CREATE TABLE IF NOT EXISTS public.abs_smart_collections (
    id          text PRIMARY KEY,
    user_id     integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    profile_id  uuid,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',
    color       text NOT NULL DEFAULT '',
    is_public   boolean NOT NULL DEFAULT false,
    is_pinned   boolean NOT NULL DEFAULT false,
    query_def   jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS abs_smart_collections_user_profile_idx
    ON public.abs_smart_collections (
        user_id,
        COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
    );
```

`migrations/153_abs_smart_collections.down.sql`:

```sql
DROP INDEX IF EXISTS public.abs_smart_collections_user_profile_idx;
DROP TABLE IF EXISTS public.abs_smart_collections;
```

- [ ] **Step 2: Extend `BookmarkStore` interface**

Edit `internal/audiobooks/abs/bookmarks.go`. Append a new method on the `BookmarkStore` interface, between `Delete` and the closing brace:

```go
	// CountByUser returns a map of library_item_id -> bookmark count
	// for the given (user, profile). Empty map (never nil) when none.
	// Used by the smart-collection items evaluator to hydrate the
	// `bookmark_count` personalized rule in one SQL pass.
	CountByUser(ctx context.Context, userID, profileID string) (map[string]int, error)
```

- [ ] **Step 3: Extend `memBookmarkStore` test fake**

Edit `internal/audiobooks/abs/bookmarks_handler_test.go`. Append this method to the `memBookmarkStore` type (find the existing `Delete` method, append the new method right after it):

```go
func (m *memBookmarkStore) CountByUser(_ context.Context, userID, profileID string) (map[string]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{}
	prefix := userID + "|" + profileID + "|"
	for k := range m.rows {
		if strings.HasPrefix(k, prefix) {
			// key format: userID|profileID|itemID|time
			rest := k[len(prefix):]
			// Find the next "|" — that delimits itemID from time.
			sep := -1
			for i, c := range rest {
				if c == '|' {
					sep = i
					break
				}
			}
			if sep < 0 {
				continue
			}
			itemID := rest[:sep]
			out[itemID]++
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Implement `CountByUser` on `ABSBookmarkStore`**

Edit `internal/audiobooks/abs_bookmark_store.go`. Append a new method:

```go
// CountByUser returns a map of library_item_id -> bookmark count for
// the given (user, profile). One SQL query; used by the
// smart-collection items evaluator for batch hydration.
func (s *ABSBookmarkStore) CountByUser(ctx context.Context, userID, profileID string) (map[string]int, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT library_item_id, COUNT(*)
		FROM abs_bookmarks
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		GROUP BY library_item_id`,
		uid, profileArg(profileID),
	)
	if err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: count-by-user: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var itemID string
		var count int
		if err := rows.Scan(&itemID, &count); err != nil {
			return nil, fmt.Errorf("abs_bookmark_store: count-by-user scan: %w", err)
		}
		out[itemID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_bookmark_store: count-by-user rows: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 5: Apply migration locally and verify build**

```bash
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/153_abs_smart_collections.up.sql
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_smart_collections"
go build ./...
go test ./internal/audiobooks/... -count=1 | tail -5
```

Expected: clean build, all tests still pass.

- [ ] **Step 6: Commit**

IMPORTANT: pre-existing unrelated modifications in working tree must NOT be staged.

```bash
git add migrations/153_abs_smart_collections.up.sql migrations/153_abs_smart_collections.down.sql \
        internal/audiobooks/abs/bookmarks.go internal/audiobooks/abs/bookmarks_handler_test.go \
        internal/audiobooks/abs_bookmark_store.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): migration 153 + BookmarkStore.CountByUser extension

Migration 153 backs the upcoming smart-collections surface.
BookmarkStore.CountByUser returns per-item counts in one SQL pass —
used by the smart-collection items evaluator to hydrate the
bookmark_count personalized rule without N+1 queries.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: smartcoll/query.go — DSL types + Normalize + Validate

**Files:**
- Create: `internal/audiobooks/smartcoll/query.go`
- Create: `internal/audiobooks/smartcoll/query_test.go`

- [ ] **Step 1: Read the continuum reference**

```bash
cat /opt/continuum_plugins_bak/continuum-plugin-audiobooks/internal/smartcoll/query.go
```

Continuum's `query.go` is the canonical reference. The audiobook field/sort catalogs (lines 99-136 of the reference) and the Normalize/Validate logic are domain-correct; port verbatim.

- [ ] **Step 2: Write `internal/audiobooks/smartcoll/query.go`**

Port continuum's `query.go` verbatim with one change: drop the package comment's reference to "the host's QueryDefinition" (continuum-specific). Keep all field types, the field/sort catalogs, the alias maps, Normalize, Validate, NormalizeSort, MarshalJSON, FieldDefs, SortFields, normalizeMatch, normalizeLibraryIDs.

The first lines should be:

```go
// Package smartcoll implements the rule-based Smart Collection DSL for
// silo's ABS audiobook surface. Audiobook-domain field catalog
// (title, author, narrator, series, genre, year, rating, language,
// publisher, added_at, duration_seconds, plus personalized: finished,
// in_progress, last_played, abandoned, bookmark_count).
//
// All evaluation happens Go-side (see evaluator.go); SQL pushdown is
// a deferred follow-up (parent spec §10).
package smartcoll

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)
```

Everything else (the types, catalogs, functions) port verbatim from continuum's `query.go`.

- [ ] **Step 3: Port the tests**

```bash
cat /opt/continuum_plugins_bak/continuum-plugin-audiobooks/internal/smartcoll/evaluator_test.go | head -40
```

Continuum's repo has tests in `evaluator_test.go` only (there's no separate `query_test.go`). For silo, split the test coverage: create `internal/audiobooks/smartcoll/query_test.go` with the Normalize/Validate-focused tests, leaving `evaluator_test.go` for Evaluate-focused tests (Task 3).

Write `internal/audiobooks/smartcoll/query_test.go` covering:

```go
package smartcoll

import "testing"

func TestNormalize_DefaultsMatchToAll(t *testing.T) {
	q := QueryDefinition{}
	n := q.Normalize()
	if n.Match != "all" {
		t.Errorf("Match = %q, want all", n.Match)
	}
}

func TestNormalize_LowercaseAndTrimsFields(t *testing.T) {
	q := QueryDefinition{
		Match: "  ALL ",
		Groups: []QueryGroup{{Match: "  Any ", Rules: []QueryRule{{Field: "  Title ", Op: "  IS ", Value: "x"}}}},
	}
	n := q.Normalize()
	if n.Match != "all" || n.Groups[0].Match != "any" {
		t.Errorf("match normalization broken: %+v", n)
	}
	if n.Groups[0].Rules[0].Field != "title" || n.Groups[0].Rules[0].Op != "is" {
		t.Errorf("rule normalization broken: %+v", n.Groups[0].Rules[0])
	}
}

func TestNormalize_AppliesFieldAliases(t *testing.T) {
	for raw, want := range map[string]string{"authors": "author", "narrators": "narrator", "genres": "genre"} {
		q := QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Field: raw, Op: "is", Value: "x"}}}}}
		n := q.Normalize()
		if n.Groups[0].Rules[0].Field != want {
			t.Errorf("alias %q -> %q, got %q", raw, want, n.Groups[0].Rules[0].Field)
		}
	}
}

func TestNormalize_DedupesAndSortsLibraryIDs(t *testing.T) {
	q := QueryDefinition{LibraryIDs: []int64{3, 1, 2, 1, 3}}
	n := q.Normalize()
	want := []int64{1, 2, 3}
	if len(n.LibraryIDs) != 3 {
		t.Fatalf("LibraryIDs = %v, want %v", n.LibraryIDs, want)
	}
	for i, id := range want {
		if n.LibraryIDs[i] != id {
			t.Errorf("LibraryIDs[%d] = %d, want %d", i, n.LibraryIDs[i], id)
		}
	}
}

func TestNormalizeSort_DefaultsField(t *testing.T) {
	s := NormalizeSort(QuerySort{})
	if s.Field != "added_at" {
		t.Errorf("default sort.field = %q, want added_at", s.Field)
	}
}

func TestNormalizeSort_DefaultOrderPerField(t *testing.T) {
	if NormalizeSort(QuerySort{Field: "title"}).Order != "asc" {
		t.Errorf("default order for 'title' should be 'asc'")
	}
	if NormalizeSort(QuerySort{Field: "added_at"}).Order != "desc" {
		t.Errorf("default order for 'added_at' should be 'desc'")
	}
}

func TestValidate_RejectsUnknownField(t *testing.T) {
	q := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "nonsense", Op: "is", Value: 1}}}}}
	if err := q.Validate(true); err == nil {
		t.Errorf("expected error for unknown field")
	}
}

func TestValidate_RejectsInvalidOpForField(t *testing.T) {
	q := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "title", Op: "between", Value: []any{1, 2}}}}}}
	if err := q.Validate(true); err == nil {
		t.Errorf("expected error for invalid op on title")
	}
}

func TestValidate_PersonalizedWithoutScope(t *testing.T) {
	q := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "finished", Op: "is", Value: true}}}}}
	if err := q.Validate(false); err == nil {
		t.Errorf("expected error for personalized without scope")
	}
}

func TestValidate_PersonalizedWithScope(t *testing.T) {
	q := QueryDefinition{Match: "all", Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "finished", Op: "is", Value: true}}}}}
	if err := q.Validate(true); err != nil {
		t.Errorf("expected no error with scope, got %v", err)
	}
}

func TestValidate_RejectsBadMatch(t *testing.T) {
	q := QueryDefinition{Match: "maybe"}
	if err := q.Validate(true); err == nil {
		t.Errorf("expected error for bad top-level match")
	}
}

func TestValidate_RejectsBadSort(t *testing.T) {
	q := QueryDefinition{Match: "all", Sort: QuerySort{Field: "nonsense"}}
	if err := q.Validate(true); err == nil {
		t.Errorf("expected error for unknown sort field")
	}
}

func TestValidate_RejectsNegativeLimit(t *testing.T) {
	limit := -1
	q := QueryDefinition{Match: "all", Limit: &limit}
	if err := q.Validate(true); err == nil {
		t.Errorf("expected error for negative limit")
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/audiobooks/smartcoll/ -v -count=1
go build ./...
```

Expected: all tests PASS; clean build.

- [ ] **Step 4: Commit**

```bash
git add internal/audiobooks/smartcoll/query.go internal/audiobooks/smartcoll/query_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): smartcoll DSL — types + Normalize + Validate

Ports continuum's QueryDefinition / QueryGroup / QueryRule / QuerySort
types and the audiobook-domain field/sort catalogs to a new
internal/audiobooks/smartcoll/ package. Covers Normalize (alias +
default + dedupe) and Validate (unknown fields, invalid ops, sort,
personalization scope).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: smartcoll/evaluator.go — `Evaluate` + ported tests

**Files:**
- Create: `internal/audiobooks/smartcoll/evaluator.go`
- Create: `internal/audiobooks/smartcoll/evaluator_test.go`

- [ ] **Step 1: Read the continuum reference**

```bash
cat /opt/continuum_plugins_bak/continuum-plugin-audiobooks/internal/smartcoll/evaluator.go
cat /opt/continuum_plugins_bak/continuum-plugin-audiobooks/internal/smartcoll/evaluator_test.go
```

- [ ] **Step 2: Define `smartcoll.Item` as the domain type**

silo's `*models.MediaItem` doesn't directly carry author/narrator/series — those are joined from `item_people` and `audiobook_series`. To keep the evaluator decoupled from silo's catalog model, define a local `Item` struct that mirrors continuum's `backend.AudiobookSummary` in shape. The handler builds these from silo data via an adapter (Task 8).

Write `internal/audiobooks/smartcoll/evaluator.go`:

```go
package smartcoll

import (
	"context"
	"hash/fnv"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Item is the audiobook-domain projection the evaluator walks. The
// handler builds these from silo's *models.MediaItem + people/series
// lookups (see siloItemToSmartcollItem in smart_collections_handler.go).
type Item struct {
	ID              string
	Title           string
	Authors         []string
	Narrators       []string
	Series          []string
	Genres          []string
	Year            int
	Rating          float64
	Language        string
	Publisher       string
	AddedAt         time.Time
	DurationSeconds int
}

// Candidate is the input shape to Evaluate — one Item plus the optional
// per-user state needed by personalized rules.
type Candidate struct {
	Item           Item
	IsFinished     bool
	ProgressPct    float32
	CurrentSeconds int
	LastPlayedAt   time.Time
	BookmarkCount  int
	PlayCount      int
}

// EvaluateOptions controls non-rule aspects of evaluation.
type EvaluateOptions struct {
	AllowPersonalized bool
	UserSeed          string
	Now               time.Time
	AbandonedAfter    time.Duration
}

// Evaluate filters the candidate list by qd's rule tree and sorts the
// survivors by qd.Sort. Pure function — no side effects, no I/O.
func Evaluate(ctx context.Context, qd QueryDefinition, candidates []Candidate, opts EvaluateOptions) []Candidate {
	_ = ctx
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.AbandonedAfter == 0 {
		opts.AbandonedAfter = 60 * 24 * time.Hour
	}
	qd = qd.Normalize()
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if matchDefinition(qd, c, opts) {
			out = append(out, c)
		}
	}
	sortCandidates(out, qd.Sort, opts)
	if qd.Limit != nil && *qd.Limit > 0 && *qd.Limit < len(out) {
		out = out[:*qd.Limit]
	}
	return out
}

func matchDefinition(qd QueryDefinition, c Candidate, opts EvaluateOptions) bool {
	if len(qd.Groups) == 0 {
		return true
	}
	if qd.Match == "any" {
		for _, g := range qd.Groups {
			if matchGroup(g, c, opts) {
				return true
			}
		}
		return false
	}
	for _, g := range qd.Groups {
		if !matchGroup(g, c, opts) {
			return false
		}
	}
	return true
}

func matchGroup(g QueryGroup, c Candidate, opts EvaluateOptions) bool {
	if len(g.Rules) == 0 {
		return true
	}
	if g.Match == "any" {
		for _, r := range g.Rules {
			if matchRule(r, c, opts) {
				return true
			}
		}
		return false
	}
	for _, r := range g.Rules {
		if !matchRule(r, c, opts) {
			return false
		}
	}
	return true
}

// matchRule dispatches per-field. Personalized fields evaluated as
// false when opts.AllowPersonalized is false (silently dropped).
func matchRule(r QueryRule, c Candidate, opts EvaluateOptions) bool {
	def, ok := queryFieldDefs[r.Field]
	if !ok {
		return false
	}
	if def.personalized && !opts.AllowPersonalized {
		return false
	}
	switch r.Field {
	case "title":
		return matchString(c.Item.Title, r.Op, r.Value)
	case "author":
		return matchStringSlice(c.Item.Authors, r.Op, r.Value)
	case "narrator":
		return matchStringSlice(c.Item.Narrators, r.Op, r.Value)
	case "series":
		return matchStringSlice(c.Item.Series, r.Op, r.Value)
	case "genre":
		return matchStringSlice(c.Item.Genres, r.Op, r.Value)
	case "year":
		return matchInt(c.Item.Year, r.Op, r.Value)
	case "rating":
		return matchFloat(c.Item.Rating, r.Op, r.Value)
	case "language":
		return matchString(c.Item.Language, r.Op, r.Value)
	case "publisher":
		return matchString(c.Item.Publisher, r.Op, r.Value)
	case "added_at":
		return matchTime(c.Item.AddedAt, r.Op, r.Value, opts.Now)
	case "duration_seconds":
		return matchInt(c.Item.DurationSeconds, r.Op, r.Value)
	case "finished":
		return matchBool(c.IsFinished, r.Op, r.Value)
	case "in_progress":
		inProg := !c.IsFinished && c.CurrentSeconds > 0
		return matchBool(inProg, r.Op, r.Value)
	case "last_played":
		return matchTime(c.LastPlayedAt, r.Op, r.Value, opts.Now)
	case "abandoned":
		inProg := !c.IsFinished && c.CurrentSeconds > 0
		ab := inProg && !c.LastPlayedAt.IsZero() && opts.Now.Sub(c.LastPlayedAt) > opts.AbandonedAfter
		return matchBool(ab, r.Op, r.Value)
	case "bookmark_count":
		return matchInt(c.BookmarkCount, r.Op, r.Value)
	}
	return false
}

func matchString(field string, op string, val any) bool {
	s, ok := stringValue(val)
	if !ok {
		return false
	}
	a, b := strings.ToLower(field), strings.ToLower(s)
	switch op {
	case "is":
		return a == b
	case "is_not":
		return a != b
	case "contains":
		return strings.Contains(a, b)
	}
	return false
}

func matchStringSlice(field []string, op string, val any) bool {
	s, ok := stringValue(val)
	if !ok {
		return false
	}
	target := strings.ToLower(s)
	switch op {
	case "is":
		for _, v := range field {
			if strings.ToLower(v) == target {
				return true
			}
		}
		return false
	case "is_not":
		for _, v := range field {
			if strings.ToLower(v) == target {
				return false
			}
		}
		return true
	case "contains":
		for _, v := range field {
			if strings.Contains(strings.ToLower(v), target) {
				return true
			}
		}
		return false
	}
	return false
}

func matchInt(field int, op string, val any) bool {
	switch op {
	case "between":
		low, high, ok := pairValue(val)
		if !ok {
			return false
		}
		l, lok := numericValue(low)
		h, hok := numericValue(high)
		if !lok || !hok {
			return false
		}
		f := float64(field)
		return f >= l && f <= h
	}
	n, ok := numericValue(val)
	if !ok {
		return false
	}
	f := float64(field)
	switch op {
	case "is":
		return f == n
	case "is_not":
		return f != n
	case "gt":
		return f > n
	case "gte":
		return f >= n
	case "lt":
		return f < n
	case "lte":
		return f <= n
	}
	return false
}

func matchFloat(field float64, op string, val any) bool {
	switch op {
	case "between":
		low, high, ok := pairValue(val)
		if !ok {
			return false
		}
		l, lok := numericValue(low)
		h, hok := numericValue(high)
		if !lok || !hok {
			return false
		}
		return field >= l && field <= h
	}
	n, ok := numericValue(val)
	if !ok {
		return false
	}
	switch op {
	case "gt":
		return field > n
	case "gte":
		return field >= n
	case "lt":
		return field < n
	case "lte":
		return field <= n
	}
	return false
}

func matchBool(field bool, op string, val any) bool {
	if op != "is" {
		return false
	}
	b, ok := val.(bool)
	if !ok {
		return false
	}
	return field == b
}

func matchTime(field time.Time, op string, val any, now time.Time) bool {
	switch op {
	case "in_last":
		s, ok := stringValue(val)
		if !ok {
			return false
		}
		d, err := parseDurationLoose(s)
		if err != nil {
			return false
		}
		if field.IsZero() {
			return false
		}
		return now.Sub(field) <= d
	case "between":
		low, high, ok := pairValue(val)
		if !ok {
			return false
		}
		lt, lok := timeValue(low)
		ht, hok := timeValue(high)
		if !lok || !hok || field.IsZero() {
			return false
		}
		return !field.Before(lt) && !field.After(ht)
	}
	t, ok := timeValue(val)
	if !ok || field.IsZero() {
		return false
	}
	switch op {
	case "gt":
		return field.After(t)
	case "gte":
		return field.After(t) || field.Equal(t)
	case "lt":
		return field.Before(t)
	case "lte":
		return field.Before(t) || field.Equal(t)
	}
	return false
}

func sortCandidates(out []Candidate, s QuerySort, opts EvaluateOptions) {
	field := s.Field
	if field == "" {
		field = defaultSortField
	}
	desc := s.Order == "desc"
	if field == "random" {
		// Deterministic shuffle seeded by UserSeed.
		seed := uint64(0)
		if opts.UserSeed != "" {
			h := fnv.New64a()
			_, _ = h.Write([]byte(opts.UserSeed))
			seed = h.Sum64()
		}
		rng := rand.New(rand.NewSource(int64(seed)))
		rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return
	}
	sort.SliceStable(out, func(i, j int) bool {
		less := compareCandidates(out[i], out[j], field)
		if desc {
			return !less && !equalCandidates(out[i], out[j], field)
		}
		return less
	})
}

func compareCandidates(a, b Candidate, field string) bool {
	switch field {
	case "title":
		return strings.ToLower(a.Item.Title) < strings.ToLower(b.Item.Title)
	case "added_at":
		return a.Item.AddedAt.Before(b.Item.AddedAt)
	case "year":
		return a.Item.Year < b.Item.Year
	case "duration_seconds":
		return a.Item.DurationSeconds < b.Item.DurationSeconds
	case "rating":
		return a.Item.Rating < b.Item.Rating
	case "progress":
		return a.ProgressPct < b.ProgressPct
	case "last_played":
		return a.LastPlayedAt.Before(b.LastPlayedAt)
	case "plays":
		return a.PlayCount < b.PlayCount
	}
	return false
}

func equalCandidates(a, b Candidate, field string) bool {
	switch field {
	case "title":
		return strings.EqualFold(a.Item.Title, b.Item.Title)
	case "added_at":
		return a.Item.AddedAt.Equal(b.Item.AddedAt)
	case "year":
		return a.Item.Year == b.Item.Year
	case "duration_seconds":
		return a.Item.DurationSeconds == b.Item.DurationSeconds
	case "rating":
		return a.Item.Rating == b.Item.Rating
	case "progress":
		return a.ProgressPct == b.ProgressPct
	case "last_played":
		return a.LastPlayedAt.Equal(b.LastPlayedAt)
	case "plays":
		return a.PlayCount == b.PlayCount
	}
	return false
}

// stringValue coerces an `any` value to a string. Accepts bare string,
// fmt.Stringer, and numbers (rendered via strconv).
func stringValue(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	}
	return "", false
}

func numericValue(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

func timeValue(v any) (time.Time, bool) {
	s, ok := stringValue(v)
	if !ok {
		return time.Time{}, false
	}
	// Accept RFC3339 first; fall back to unix-seconds integer.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

func pairValue(v any) (any, any, bool) {
	switch x := v.(type) {
	case []any:
		if len(x) == 2 {
			return x[0], x[1], true
		}
	case [2]any:
		return x[0], x[1], true
	}
	return nil, nil, false
}

// parseDurationLoose accepts standard Go durations ("24h", "7m") plus
// "Nd" (days) and "Nw" (weeks).
func parseDurationLoose(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if len(s) >= 2 {
		suffix := s[len(s)-1]
		body := s[:len(s)-1]
		if suffix == 'd' {
			n, err := strconv.Atoi(body)
			if err == nil {
				return time.Duration(n) * 24 * time.Hour, nil
			}
		}
		if suffix == 'w' {
			n, err := strconv.Atoi(body)
			if err == nil {
				return time.Duration(n) * 7 * 24 * time.Hour, nil
			}
		}
	}
	return time.ParseDuration(s)
}
```

- [ ] **Step 3: Write the evaluator tests**

Write `internal/audiobooks/smartcoll/evaluator_test.go`:

```go
package smartcoll

import (
	"context"
	"testing"
	"time"
)

func sampleCandidates() []Candidate {
	return []Candidate{
		{Item: Item{ID: "1", Title: "Mistborn", Authors: []string{"Brandon Sanderson"}, Genres: []string{"Fantasy"}, Year: 2006, Rating: 4.5, AddedAt: time.Now().Add(-30 * 24 * time.Hour), DurationSeconds: 60000}},
		{Item: Item{ID: "2", Title: "Project Hail Mary", Authors: []string{"Andy Weir"}, Genres: []string{"Sci-Fi"}, Year: 2021, Rating: 4.8, AddedAt: time.Now().Add(-10 * 24 * time.Hour), DurationSeconds: 50000}},
		{Item: Item{ID: "3", Title: "Dune", Authors: []string{"Frank Herbert"}, Genres: []string{"Sci-Fi"}, Year: 1965, Rating: 4.3, AddedAt: time.Now().Add(-365 * 24 * time.Hour), DurationSeconds: 75000}},
	}
}

func TestEvaluate_EmptyRulesMatchesAll(t *testing.T) {
	got := Evaluate(context.Background(), QueryDefinition{}, sampleCandidates(), EvaluateOptions{})
	if len(got) != 3 {
		t.Errorf("matched %d, want 3", len(got))
	}
}

func TestEvaluate_GenreContains(t *testing.T) {
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "genre", Op: "contains", Value: "Sci"}}}},
	}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{})
	if len(got) != 2 {
		t.Errorf("matched %d, want 2 (sci-fi books)", len(got))
	}
}

func TestEvaluate_YearBetween(t *testing.T) {
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "year", Op: "between", Value: []any{2000, 2025}}}}},
	}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{})
	if len(got) != 2 {
		t.Errorf("matched %d, want 2 (Mistborn + Project Hail Mary)", len(got))
	}
}

func TestEvaluate_AddedInLast14d(t *testing.T) {
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "added_at", Op: "in_last", Value: "14d"}}}},
	}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{Now: time.Now()})
	if len(got) != 1 {
		t.Errorf("matched %d, want 1 (only Project Hail Mary within 14d)", len(got))
	}
}

func TestEvaluate_MatchAny(t *testing.T) {
	qd := QueryDefinition{
		Match: "any",
		Groups: []QueryGroup{
			{Match: "all", Rules: []QueryRule{{Field: "author", Op: "is", Value: "Andy Weir"}}},
			{Match: "all", Rules: []QueryRule{{Field: "year", Op: "lt", Value: 1970}}},
		},
	}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{})
	if len(got) != 2 {
		t.Errorf("matched %d, want 2 (Project Hail Mary + Dune)", len(got))
	}
}

func TestEvaluate_PersonalizedDroppedWithoutScope(t *testing.T) {
	cands := sampleCandidates()
	cands[0].IsFinished = true
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "finished", Op: "is", Value: true}}}},
	}
	got := Evaluate(context.Background(), qd, cands, EvaluateOptions{AllowPersonalized: false})
	// Personalized rule silently dropped -> rule evaluates false -> nothing matches.
	if len(got) != 0 {
		t.Errorf("matched %d with personalization dropped, want 0", len(got))
	}
	got2 := Evaluate(context.Background(), qd, cands, EvaluateOptions{AllowPersonalized: true})
	if len(got2) != 1 {
		t.Errorf("matched %d with personalization on, want 1 (Mistborn)", len(got2))
	}
}

func TestEvaluate_BookmarkCountGT(t *testing.T) {
	cands := sampleCandidates()
	cands[0].BookmarkCount = 5
	cands[1].BookmarkCount = 0
	cands[2].BookmarkCount = 2
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "bookmark_count", Op: "gt", Value: 0}}}},
	}
	got := Evaluate(context.Background(), qd, cands, EvaluateOptions{AllowPersonalized: true})
	if len(got) != 2 {
		t.Errorf("matched %d, want 2", len(got))
	}
}

func TestEvaluate_SortTitleAsc(t *testing.T) {
	qd := QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{})
	if len(got) != 3 || got[0].Item.Title != "Dune" || got[2].Item.Title != "Project Hail Mary" {
		t.Errorf("sort wrong: %v %v %v", got[0].Item.Title, got[1].Item.Title, got[2].Item.Title)
	}
}

func TestEvaluate_RandomDeterministicPerSeed(t *testing.T) {
	qd := QueryDefinition{Sort: QuerySort{Field: "random"}}
	a := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{UserSeed: "u1:c1"})
	b := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{UserSeed: "u1:c1"})
	for i := range a {
		if a[i].Item.ID != b[i].Item.ID {
			t.Errorf("random sort not deterministic at index %d: %v vs %v", i, a[i].Item.ID, b[i].Item.ID)
		}
	}
}

func TestEvaluate_LimitTrims(t *testing.T) {
	limit := 2
	qd := QueryDefinition{Limit: &limit, Sort: QuerySort{Field: "title", Order: "asc"}}
	got := Evaluate(context.Background(), qd, sampleCandidates(), EvaluateOptions{})
	if len(got) != 2 {
		t.Errorf("limit didn't apply: len = %d, want 2", len(got))
	}
}

func TestEvaluate_AbandonedRule(t *testing.T) {
	cands := sampleCandidates()
	// Make Mistborn in-progress 90 days ago.
	cands[0].CurrentSeconds = 1000
	cands[0].LastPlayedAt = time.Now().Add(-90 * 24 * time.Hour)
	qd := QueryDefinition{
		Match: "all",
		Groups: []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "abandoned", Op: "is", Value: true}}}},
	}
	got := Evaluate(context.Background(), qd, cands, EvaluateOptions{AllowPersonalized: true, AbandonedAfter: 60 * 24 * time.Hour, Now: time.Now()})
	if len(got) != 1 {
		t.Errorf("matched %d, want 1 (Mistborn abandoned 90d ago)", len(got))
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/audiobooks/smartcoll/ -v -count=1
go build ./...
```

Expected: all PASS; clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/smartcoll/evaluator.go internal/audiobooks/smartcoll/evaluator_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): smartcoll DSL evaluator + tests

In-memory rule evaluator over Candidate{Item, IsFinished, ProgressPct,
CurrentSeconds, LastPlayedAt, BookmarkCount, PlayCount}. Covers all
15 fields + 7 operators + 9 sort keys including deterministic
seeded random. Personalized rules drop to false (silent) when
opts.AllowPersonalized is false. Decoupled from silo's catalog
model via the local Item struct — handler adapts.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: `SmartCollection` types, interface, envelope test

**Files:**
- Create: `internal/audiobooks/abs/smart_collections.go`
- Create: `internal/audiobooks/abs/smart_collections_envelope_test.go`

- [ ] **Step 1: Write the failing envelope test**

Create `internal/audiobooks/abs/smart_collections_envelope_test.go`:

```go
package abs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSmartCollectionEnvelope_HasRequiredKeys(t *testing.T) {
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	out := smartCollectionToABS(SmartCollection{
		ID:          "01HSC",
		UserID:      "1",
		Name:        "x",
		Description: "",
		Color:       "",
		IsPublic:    false,
		IsPinned:    false,
		QueryDef:    []byte(`{"match":"all","groups":[]}`),
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	body, _ := json.Marshal(out)
	js := string(body)
	for _, key := range []string{
		`"id":`, `"userId":`, `"name":`, `"description":`, `"color":`,
		`"isPublic":`, `"isPinned":`, `"queryDef":`, `"createdAt":`, `"updatedAt":`,
	} {
		if !strings.Contains(js, key) {
			t.Errorf("envelope missing %s; got %s", key, js)
		}
	}
	// queryDef must be a nested object, not a string.
	if _, ok := out["queryDef"].(map[string]any); !ok {
		t.Errorf("queryDef should marshal as nested object, got %T: %v", out["queryDef"], out["queryDef"])
	}
}

func TestSmartCollectionEnvelope_EmptyQueryDef(t *testing.T) {
	out := smartCollectionToABS(SmartCollection{
		ID: "x", UserID: "1", Name: "y",
		QueryDef:  nil,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	// queryDef must still be present, defaulted to an object.
	if _, has := out["queryDef"]; !has {
		t.Errorf("queryDef missing when stored bytes are nil")
	}
}
```

- [ ] **Step 2: Run to verify failure**

```bash
go test ./internal/audiobooks/abs/ -run TestSmartCollectionEnvelope -v
```

Expected: compile failure (`undefined: smartCollectionToABS` / `undefined: SmartCollection`).

- [ ] **Step 3: Implement types + interface + serialiser**

Create `internal/audiobooks/abs/smart_collections.go`:

```go
package abs

import (
	"context"
	"encoding/json"
	"time"
)

// SmartCollectionStore is the narrow slice of abs_smart_collections
// the handlers need. Implemented by ABSSmartCollectionStore in
// internal/audiobooks/abs_smart_collection_store.go.
type SmartCollectionStore interface {
	ListUserSmartCollections(ctx context.Context, userID, profileID string) ([]SmartCollection, error)
	GetSmartCollection(ctx context.Context, id string) (SmartCollection, error)
	CreateSmartCollection(ctx context.Context, c SmartCollection) error
	UpdateSmartCollection(ctx context.Context, c SmartCollection) error
	DeleteSmartCollection(ctx context.Context, id string) error
}

// SmartCollection mirrors an abs_smart_collections row.
// QueryDef holds the raw JSONB bytes (decoded only on the /items
// route where the rules are actually evaluated).
type SmartCollection struct {
	ID          string
	UserID      string
	ProfileID   string
	Name        string
	Description string
	Color       string
	IsPublic    bool
	IsPinned    bool
	QueryDef    []byte
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// smartCollectionToABS shapes a SmartCollection in the ABS wire format.
// QueryDef is emitted as a nested JSON object (not the raw bytes) so
// clients consume it without an extra decode step. Empty/nil QueryDef
// becomes the empty object {}.
func smartCollectionToABS(c SmartCollection) map[string]any {
	var qd any = map[string]any{}
	if len(c.QueryDef) > 0 {
		var decoded any
		if err := json.Unmarshal(c.QueryDef, &decoded); err == nil {
			qd = decoded
		}
	}
	return map[string]any{
		"id":          c.ID,
		"userId":      c.UserID,
		"name":        c.Name,
		"description": c.Description,
		"color":       c.Color,
		"isPublic":    c.IsPublic,
		"isPinned":    c.IsPinned,
		"queryDef":    qd,
		"createdAt":   c.CreatedAt.UnixMilli(),
		"updatedAt":   c.UpdatedAt.UnixMilli(),
	}
}
```

- [ ] **Step 4: Run tests + build**

```bash
go test ./internal/audiobooks/abs/ -run TestSmartCollection -v
go build ./...
```

Expected: PASS; clean.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/smart_collections.go internal/audiobooks/abs/smart_collections_envelope_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): SmartCollectionStore interface + envelope helper

Defines the storage contract and wire-shape serialiser. queryDef is
emitted as a nested JSON object on the wire (decoded from the JSONB
bytes once at serialisation time, not raw bytes — clients consume
the parsed shape).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Test harness + handleCreateSmartCollection

**Files:**
- Modify: `internal/audiobooks/abs/handler.go` (add SmartCollectionStore field)
- Create: `internal/audiobooks/abs/smart_collections_handler.go`
- Create: `internal/audiobooks/abs/smart_collections_handler_test.go`

- [ ] **Step 1: Add `SmartCollectionStore` field to `Dependencies`**

In `internal/audiobooks/abs/handler.go`, in the `Dependencies` struct, add after `PlaylistStore`:

```go
	// SmartCollectionStore persists abs_smart_collections rows
	// (migration 153). May be nil; handlers respond 503 when unset.
	SmartCollectionStore SmartCollectionStore
```

Build:

```bash
go build ./...
```

- [ ] **Step 2: Create the failing test + in-memory fake**

Create `internal/audiobooks/abs/smart_collections_handler_test.go`:

```go
package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type memSmartCollectionStore struct {
	mu   sync.Mutex
	rows map[string]SmartCollection
}

func newMemSmartCollectionStore() *memSmartCollectionStore {
	return &memSmartCollectionStore{rows: map[string]SmartCollection{}}
}

func (m *memSmartCollectionStore) ListUserSmartCollections(_ context.Context, userID, profileID string) ([]SmartCollection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SmartCollection, 0)
	for _, c := range m.rows {
		if c.UserID == userID && c.ProfileID == profileID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memSmartCollectionStore) GetSmartCollection(_ context.Context, id string) (SmartCollection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[id]
	if !ok {
		return SmartCollection{}, ErrNotFound
	}
	return c, nil
}

func (m *memSmartCollectionStore) CreateSmartCollection(_ context.Context, c SmartCollection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[c.ID] = c
	return nil
}

func (m *memSmartCollectionStore) UpdateSmartCollection(_ context.Context, c SmartCollection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.rows[c.ID]
	if !ok {
		return ErrNotFound
	}
	existing.Name = c.Name
	existing.Description = c.Description
	existing.Color = c.Color
	existing.IsPublic = c.IsPublic
	existing.IsPinned = c.IsPinned
	existing.QueryDef = c.QueryDef
	existing.UpdatedAt = time.Now()
	m.rows[c.ID] = existing
	return nil
}

func (m *memSmartCollectionStore) DeleteSmartCollection(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, id)
	return nil
}

type smartCollectionsHarness struct {
	H    *Handler
	SC   *memSmartCollectionStore
	Coll *memCollectionStore // unused for smart collections, but present to allow shared handler construction; nil-safe
}

func newSmartCollectionsHarness(t *testing.T, knownItems ...string) *smartCollectionsHarness {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = nil
	}
	store := newMemSmartCollectionStore()
	h := New(Dependencies{
		MediaStore:           &stubMediaStore{known: known},
		SmartCollectionStore: store,
	})
	return &smartCollectionsHarness{H: h, SC: store}
}

func createSmartCollectionForUser(t *testing.T, hb *smartCollectionsHarness, userID, profileID, body string) string {
	t.Helper()
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/smart-collections", nil, []byte(body), userID, profileID, hb.H.handleCreateSmartCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed POST status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("seed POST returned no id; body=%s", rec.Body.String())
	}
	return id
}

func TestSmartCollection_Create_ReturnsFullShape(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	body := []byte(`{"name":"x","description":"d","color":"#fff","isPublic":true,"isPinned":true,"query_def":{"match":"all","groups":[{"match":"all","rules":[{"field":"title","op":"is","value":"test"}]}]}}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/smart-collections", nil, body, "1", "", hb.H.handleCreateSmartCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "x" {
		t.Errorf("name = %v, want x", got["name"])
	}
	if got["isPublic"] != true {
		t.Errorf("isPublic = %v, want true", got["isPublic"])
	}
	if got["isPinned"] != true {
		t.Errorf("isPinned = %v, want true", got["isPinned"])
	}
	qd, ok := got["queryDef"].(map[string]any)
	if !ok {
		t.Fatalf("queryDef not nested object: %T %v", got["queryDef"], got["queryDef"])
	}
	if qd["match"] != "all" {
		t.Errorf("queryDef.match = %v, want all", qd["match"])
	}
}

func TestSmartCollection_Create_NameRequired_400(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/smart-collections", nil, []byte(`{}`), "1", "", hb.H.handleCreateSmartCollection)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSmartCollection_Create_InvalidQueryDef_400(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	body := []byte(`{"name":"x","query_def":{"match":"all","groups":[{"match":"all","rules":[{"field":"nonsense","op":"is","value":1}]}]}}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/smart-collections", nil, body, "1", "", hb.H.handleCreateSmartCollection)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestSmartCollection_Create_InvalidBody_400(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	rec := dispatchABSWithParams(http.MethodPost, "/api/me/smart-collections", nil, []byte(`{not json`), "1", "", hb.H.handleCreateSmartCollection)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
```

- [ ] **Step 3: Run tests to confirm compile failure**

```bash
go test ./internal/audiobooks/abs/ -run TestSmartCollection_Create -v
```

Expected: compile failure (`h.handleCreateSmartCollection undefined`).

- [ ] **Step 4: Implement `handleCreateSmartCollection`**

Create `internal/audiobooks/abs/smart_collections_handler.go`:

```go
package abs

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/audiobooks/smartcoll"
)

// smartCollectionBody is the JSON body for POST and PATCH
// /me/smart-collections[/{id}]. Pointer fields support partial PATCH.
type smartCollectionBody struct {
	Name        *string                    `json:"name"`
	Description *string                    `json:"description"`
	Color       *string                    `json:"color"`
	IsPublic    *bool                      `json:"isPublic"`
	IsPinned    *bool                      `json:"isPinned"`
	QueryDef    *smartcoll.QueryDefinition `json:"query_def"`
}

// handleCreateSmartCollection — POST /me/smart-collections.
// Body: {name, description?, color?, isPublic?, isPinned?, query_def}.
// Returns the created collection in full-shape.
func (h *Handler) handleCreateSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection store unavailable", http.StatusServiceUnavailable)
		return
	}

	var body smartCollectionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == nil || *body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	c := SmartCollection{
		ID:        ulid.Make().String(),
		UserID:    a.UserID,
		ProfileID: a.ProfileID,
		Name:      *body.Name,
	}
	if body.Description != nil {
		c.Description = *body.Description
	}
	if body.Color != nil {
		c.Color = *body.Color
	}
	if body.IsPublic != nil {
		c.IsPublic = *body.IsPublic
	}
	if body.IsPinned != nil {
		c.IsPinned = *body.IsPinned
	}

	// Normalize + validate the query_def, then marshal to bytes for storage.
	qd := smartcoll.QueryDefinition{}
	if body.QueryDef != nil {
		qd = *body.QueryDef
	}
	qd = qd.Normalize()
	if err := qd.Validate(true); err != nil {
		http.Error(w, "invalid query_def: "+err.Error(), http.StatusBadRequest)
		return
	}
	qdBytes, err := json.Marshal(qd)
	if err != nil {
		slog.Error("abs smart collection marshal query_def failed", "err", err)
		http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
		return
	}
	c.QueryDef = qdBytes

	if err := h.deps.SmartCollectionStore.CreateSmartCollection(r.Context(), c); err != nil {
		slog.Error("abs smart collection create failed", "err", err, "user", a.UserID)
		http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
		return
	}

	persisted, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), c.ID)
	if errors.Is(err, ErrNotFound) || err != nil {
		persisted = c
	}
	writeJSON(w, http.StatusOK, smartCollectionToABS(persisted))
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/audiobooks/abs/ -run TestSmartCollection_Create -v
go test ./internal/audiobooks/abs/ -count=1 | tail -5
```

Expected: all four PASS, full package PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/audiobooks/abs/handler.go internal/audiobooks/abs/smart_collections_handler.go internal/audiobooks/abs/smart_collections_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): POST /me/smart-collections — create + DSL validation

First handler of the smart collections surface. Body decoded via
smartCollectionBody (pointer fields support partial PATCH later).
query_def is normalized + validated (with allowPersonalized=true)
before marshalling to JSONB bytes for storage. Adds the
memSmartCollectionStore harness.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: List + Get + Update + Delete handlers

**Files:**
- Modify: `internal/audiobooks/abs/smart_collections_handler.go`
- Modify: `internal/audiobooks/abs/smart_collections_handler_test.go`

- [ ] **Step 1: Append failing tests**

Append to `smart_collections_handler_test.go`:

```go

func TestSmartCollection_List_WrappedAsItems(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	_ = createSmartCollectionForUser(t, hb, "1", "", `{"name":"a"}`)
	_ = createSmartCollectionForUser(t, hb, "1", "", `{"name":"b"}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections", nil, nil, "1", "", hb.H.handleListSmartCollections)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, ok := env["items"].([]any)
	if !ok {
		t.Fatalf("response missing 'items' key (wrapped envelope); body=%s", rec.Body.String())
	}
	if len(list) != 2 {
		t.Errorf("list len = %d, want 2", len(list))
	}
}

func TestSmartCollection_List_DoesNotLeakOtherUsers(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	_ = createSmartCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections", nil, nil, "2", "", hb.H.handleListSmartCollections)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	list, _ := env["items"].([]any)
	if len(list) != 0 {
		t.Errorf("user 2 sees %d, want 0", len(list))
	}
}

func TestSmartCollection_Get_Owner_ReturnsFullShape(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleGetSmartCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "mine" {
		t.Errorf("name = %v", got["name"])
	}
}

func TestSmartCollection_Get_NonOwner_Private_404(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"private"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleGetSmartCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSmartCollection_Get_NonOwner_Public_OK(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"public","isPublic":true}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleGetSmartCollection)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestSmartCollection_Get_Unknown_404(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/01HZZZ", map[string]string{"id": "01HZZZ"}, nil, "1", "", hb.H.handleGetSmartCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSmartCollection_Patch_OwnerUpdates(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"old"}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/me/smart-collections/"+id, map[string]string{"id": id}, []byte(`{"name":"new","isPinned":true}`), "1", "", hb.H.handleUpdateSmartCollection)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["name"] != "new" || got["isPinned"] != true {
		t.Errorf("PATCH didn't apply: %v", got)
	}
}

func TestSmartCollection_Patch_NonOwner_404(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/me/smart-collections/"+id, map[string]string{"id": id}, []byte(`{"name":"hijack"}`), "2", "", hb.H.handleUpdateSmartCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	c, _ := hb.SC.GetSmartCollection(context.Background(), id)
	if c.Name != "mine" {
		t.Errorf("non-owner leak: %q", c.Name)
	}
}

func TestSmartCollection_Patch_InvalidQueryDef_400(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"x"}`)
	body := []byte(`{"query_def":{"match":"all","groups":[{"match":"all","rules":[{"field":"nonsense","op":"is","value":1}]}]}}`)
	rec := dispatchABSWithParams(http.MethodPatch, "/api/me/smart-collections/"+id, map[string]string{"id": id}, body, "1", "", hb.H.handleUpdateSmartCollection)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSmartCollection_Delete_Owner_204(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"x"}`)
	rec := dispatchABSWithParams(http.MethodDelete, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleDeleteSmartCollection)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	rec2 := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "1", "", hb.H.handleGetSmartCollection)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("post-delete GET status = %d, want 404", rec2.Code)
	}
}

func TestSmartCollection_Delete_NonOwner_404(t *testing.T) {
	hb := newSmartCollectionsHarness(t)
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"mine"}`)
	rec := dispatchABSWithParams(http.MethodDelete, "/api/me/smart-collections/"+id, map[string]string{"id": id}, nil, "2", "", hb.H.handleDeleteSmartCollection)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if _, err := hb.SC.GetSmartCollection(context.Background(), id); err != nil {
		t.Errorf("non-owner DELETE leaked: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to confirm compile failure**

```bash
go test ./internal/audiobooks/abs/ -run TestSmartCollection -v
```

Expected: compile failure for the four new handler refs.

- [ ] **Step 3: Implement the four handlers**

Append to `smart_collections_handler.go`:

```go

// handleListSmartCollections — GET /me/smart-collections.
// Returns owner's smart collections wrapped in {"items": [...]}.
func (h *Handler) handleListSmartCollections(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	rows, err := h.deps.SmartCollectionStore.ListUserSmartCollections(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		slog.Error("abs smart collection list failed", "err", err, "user", a.UserID)
		http.Error(w, "smart collection list failed", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, smartCollectionToABS(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleGetSmartCollection — GET /me/smart-collections/{id}.
func (h *Handler) handleGetSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), chiURLID(r))
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID && !c.IsPublic) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection get failed", "err", err)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, smartCollectionToABS(c))
}

// handleUpdateSmartCollection — PATCH /me/smart-collections/{id}.
// Owner-only. Partial body. query_def re-validated if present.
func (h *Handler) handleUpdateSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection get-for-update failed", "err", err, "id", id)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}

	var body smartCollectionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name != nil {
		c.Name = *body.Name
	}
	if body.Description != nil {
		c.Description = *body.Description
	}
	if body.Color != nil {
		c.Color = *body.Color
	}
	if body.IsPublic != nil {
		c.IsPublic = *body.IsPublic
	}
	if body.IsPinned != nil {
		c.IsPinned = *body.IsPinned
	}
	if body.QueryDef != nil {
		qd := body.QueryDef.Normalize()
		if err := qd.Validate(true); err != nil {
			http.Error(w, "invalid query_def: "+err.Error(), http.StatusBadRequest)
			return
		}
		qdBytes, mErr := json.Marshal(qd)
		if mErr != nil {
			slog.Error("abs smart collection marshal query_def failed", "err", mErr)
			http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
			return
		}
		c.QueryDef = qdBytes
	}
	if err := h.deps.SmartCollectionStore.UpdateSmartCollection(r.Context(), c); err != nil {
		slog.Error("abs smart collection update failed", "err", err, "id", id)
		http.Error(w, "smart collection persist failed", http.StatusInternalServerError)
		return
	}
	persisted, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if err != nil {
		persisted = c
	}
	writeJSON(w, http.StatusOK, smartCollectionToABS(persisted))
}

// handleDeleteSmartCollection — DELETE /me/smart-collections/{id}.
func (h *Handler) handleDeleteSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection get-for-delete failed", "err", err, "id", id)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}
	if err := h.deps.SmartCollectionStore.DeleteSmartCollection(r.Context(), id); err != nil {
		slog.Error("abs smart collection delete failed", "err", err, "id", id)
		http.Error(w, "smart collection delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Run tests + verify**

```bash
go test ./internal/audiobooks/abs/ -run TestSmartCollection -v
go test ./internal/audiobooks/abs/ -count=1 | tail -5
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs/smart_collections_handler.go internal/audiobooks/abs/smart_collections_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): list/get/patch/delete smart collections

Four CRUD handlers + tests. Anti-enumeration 404 on non-owner
private. List envelope is {"items": [...]} (matches continuum's
smart-collection wrap key — DIFFERENT from manual-collections
{"collections": [...]}). PATCH re-validates query_def when present.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: handleSmartCollectionItems (eval + hydration)

**Files:**
- Modify: `internal/audiobooks/abs/smart_collections_handler.go`
- Modify: `internal/audiobooks/abs/smart_collections_handler_test.go`

The eval handler is the big one. It needs `MediaStore.ListAudiobooks` + `ProgressStore.ListProgressForAudiobooks` + `BookmarkStore.CountByUser` to hydrate the candidate list.

- [ ] **Step 1: Extend the harness so it can wire all three stores**

The current `smartCollectionsHarness` only wires `MediaStore` + `SmartCollectionStore`. The /items handler needs ProgressStore + BookmarkStore too. Modify `newSmartCollectionsHarness` to also wire these (using the existing `fakeProgressStore` from `play_resume_test.go` and `memBookmarkStore` from `bookmarks_handler_test.go`):

Replace `newSmartCollectionsHarness` in `smart_collections_handler_test.go` with:

```go
type smartCollectionsHarness struct {
	H    *Handler
	SC   *memSmartCollectionStore
	Prog *fakeProgressStore   // exposes per-test setup of progress rows
	Book *memBookmarkStore    // exposes per-test setup of bookmark rows
}

func newSmartCollectionsHarness(t *testing.T, knownItems ...string) *smartCollectionsHarness {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = &models.MediaItem{ContentID: id, Title: "Title-" + id}
	}
	store := newMemSmartCollectionStore()
	prog := &fakeProgressStore{}
	book := newMemBookmarkStore()
	h := New(Dependencies{
		MediaStore:           &itemListStubMediaStore{stubMediaStore: stubMediaStore{known: known}, items: itemListFromKnown(known)},
		SmartCollectionStore: store,
		ProgressStore:        prog,
		BookmarkStore:        book,
	})
	return &smartCollectionsHarness{H: h, SC: store, Prog: prog, Book: book}
}

// itemListStubMediaStore extends stubMediaStore with a working
// ListAudiobooks so the smart-collection items handler can build
// candidates. ListAudiobookLibraries returns a single virtual library
// so the handler's library-loop runs once.
type itemListStubMediaStore struct {
	stubMediaStore
	items []*models.MediaItem
}

func (s *itemListStubMediaStore) ListAudiobooks(_ context.Context, _ int64, _, _ int) ([]*models.MediaItem, int, error) {
	return s.items, len(s.items), nil
}

func (s *itemListStubMediaStore) ListAudiobookLibraries(_ context.Context) ([]AudiobookLibrary, error) {
	return []AudiobookLibrary{{ID: 9, Name: "Audiobooks", Type: "audiobooks"}}, nil
}

func itemListFromKnown(known map[string]*models.MediaItem) []*models.MediaItem {
	out := make([]*models.MediaItem, 0, len(known))
	for _, it := range known {
		if it != nil {
			out = append(out, it)
		}
	}
	return out
}
```

- [ ] **Step 2: Append items tests**

Append to `smart_collections_handler_test.go`:

```go

func TestSmartCollection_Items_OwnerEvaluatesRules(t *testing.T) {
	hb := newSmartCollectionsHarness(t, "book-a", "book-b", "book-c")
	id := createSmartCollectionForUser(t, hb, "1", "",
		`{"name":"a-only","query_def":{"match":"all","groups":[{"match":"all","rules":[{"field":"title","op":"contains","value":"book-a"}]}]}}`)

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id+"/items",
		map[string]string{"id": id}, nil, "1", "", hb.H.handleSmartCollectionItems)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	// Note: titles are "Title-book-a" etc. — the `contains` op matches "book-a".
	results, _ := env["results"].([]any)
	if len(results) != 1 {
		t.Errorf("results len = %d, want 1; body=%s", len(results), rec.Body.String())
	}
}

func TestSmartCollection_Items_PaginatedEnvelope(t *testing.T) {
	hb := newSmartCollectionsHarness(t, "book-a", "book-b")
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"all"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id+"/items",
		map[string]string{"id": id}, nil, "1", "", hb.H.handleSmartCollectionItems)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	for _, k := range []string{"results", "total", "limit", "page", "sortBy", "sortDesc", "filterBy", "minified", "include"} {
		if _, has := env[k]; !has {
			t.Errorf("envelope missing %q", k)
		}
	}
}

func TestSmartCollection_Items_NonOwnerPrivate_404(t *testing.T) {
	hb := newSmartCollectionsHarness(t, "book-a")
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"private"}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id+"/items",
		map[string]string{"id": id}, nil, "2", "", hb.H.handleSmartCollectionItems)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestSmartCollection_Items_NonOwnerPublic_OK(t *testing.T) {
	hb := newSmartCollectionsHarness(t, "book-a")
	id := createSmartCollectionForUser(t, hb, "1", "", `{"name":"public","isPublic":true}`)
	rec := dispatchABSWithParams(http.MethodGet, "/api/me/smart-collections/"+id+"/items",
		map[string]string{"id": id}, nil, "2", "", hb.H.handleSmartCollectionItems)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
```

- [ ] **Step 3: Run tests to confirm failure**

```bash
go test ./internal/audiobooks/abs/ -run TestSmartCollection_Items -v
```

Expected: compile failure (`h.handleSmartCollectionItems undefined`).

- [ ] **Step 4: Implement the items handler**

Append to `smart_collections_handler.go`:

```go

// handleSmartCollectionItems — GET /me/smart-collections/{id}/items.
// Evaluates the collection's query_def against the audiobook catalog
// and returns a paged envelope. When the caller is the owner, per-user
// state is hydrated; non-owner viewing a public collection sees
// personalized rules silently dropped (privacy-preserving).
func (h *Handler) handleSmartCollectionItems(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.deps.SmartCollectionStore == nil {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	id := chiURLID(r)
	c, err := h.deps.SmartCollectionStore.GetSmartCollection(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.UserID != a.UserID && !c.IsPublic) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("abs smart collection items get failed", "err", err, "id", id)
		http.Error(w, "smart collection get failed", http.StatusInternalServerError)
		return
	}

	var qd smartcoll.QueryDefinition
	if len(c.QueryDef) > 0 {
		if uErr := json.Unmarshal(c.QueryDef, &qd); uErr != nil {
			slog.Error("abs smart collection invalid stored query_def", "err", uErr, "id", id)
			http.Error(w, "smart collection get failed", http.StatusInternalServerError)
			return
		}
	}
	qd = qd.Normalize()

	limit, page := readPagedQuery(r, 30)
	if r.URL.Query().Get("limit") == "" && qd.Limit != nil && *qd.Limit > 0 {
		limit = *qd.Limit
	}

	// Resolve target libraries.
	allLibs, err := h.deps.MediaStore.ListAudiobookLibraries(r.Context())
	if err != nil {
		slog.Warn("abs smart collection libraries fetch failed", "err", err, "id", id)
		allLibs = nil
	}
	libByID := make(map[int64]AudiobookLibrary, len(allLibs))
	for _, lib := range allLibs {
		libByID[lib.ID] = lib
	}
	var targetLibs []AudiobookLibrary
	if len(qd.LibraryIDs) > 0 {
		for _, lid := range qd.LibraryIDs {
			if lib, ok := libByID[lid]; ok {
				targetLibs = append(targetLibs, lib)
			}
		}
	} else {
		targetLibs = allLibs
	}

	owner := c.UserID == a.UserID
	// Hydrate per-user state once for the owner.
	progressByID := map[string]ProgressRow{}
	bookmarkCountByID := map[string]int{}
	if owner {
		if h.deps.ProgressStore != nil {
			if rows, perr := h.deps.ProgressStore.ListProgressForAudiobooks(r.Context(), a.UserID, a.ProfileID, 10000); perr == nil {
				for _, p := range rows {
					progressByID[p.ContentID] = p
				}
			}
		}
		if h.deps.BookmarkStore != nil {
			if counts, berr := h.deps.BookmarkStore.CountByUser(r.Context(), a.UserID, a.ProfileID); berr == nil {
				bookmarkCountByID = counts
			}
		}
	}

	// Build candidates.
	candidates := make([]smartcoll.Candidate, 0, 256)
	for _, lib := range targetLibs {
		items, _, lerr := h.deps.MediaStore.ListAudiobooks(r.Context(), lib.ID, 5000, 0)
		if lerr != nil {
			slog.Warn("abs smart collection list-audiobooks failed", "err", lerr, "library", lib.ID)
			continue
		}
		for _, mi := range items {
			cand := smartcoll.Candidate{Item: siloItemToSmartcollItem(mi)}
			if owner {
				if p, ok := progressByID[mi.ContentID]; ok {
					cand.IsFinished = p.IsFinished
					cand.ProgressPct = float32(p.ProgressPct)
					cand.CurrentSeconds = int(p.CurrentSeconds)
					cand.LastPlayedAt = p.UpdatedAt
				}
				cand.BookmarkCount = bookmarkCountByID[mi.ContentID]
			}
			candidates = append(candidates, cand)
		}
	}

	matched := smartcoll.Evaluate(r.Context(), qd, candidates, smartcoll.EvaluateOptions{
		AllowPersonalized: owner,
		UserSeed:          a.UserID + ":" + c.ID,
	})

	total := len(matched)
	start := page * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	pageSlice := matched[start:end]

	// Hydrate the page into wire-shape LibraryItem entries.
	libDefault := h.resolveDefaultLibrary(r.Context())
	libDefaultID := audiobookLibraryID(libDefault)
	results := make([]map[string]any, 0, len(pageSlice))
	for _, cand := range pageSlice {
		entry := map[string]any{
			"id":        cand.Item.ID,
			"libraryId": libDefaultID,
			"media": map[string]any{
				"metadata": map[string]any{"title": cand.Item.Title},
			},
		}
		results = append(results, entry)
	}

	writeJSON(w, http.StatusOK, pagedEnvelope(results, total, limit, page, qd.Sort.Field, qd.Sort.Order == "desc", "", false, ""))
}

// siloItemToSmartcollItem maps a silo *models.MediaItem into the
// audiobook-domain Item shape the smartcoll evaluator walks.
// Authors / Narrators / Series / Publisher / DurationSeconds are best-
// effort; when silo's MediaItem doesn't carry them inline, they
// surface as empty/zero — rules referencing them then evaluate false.
// Hydrating those from people/series tables is a Phase-4 follow-up.
func siloItemToSmartcollItem(mi *models.MediaItem) smartcoll.Item {
	if mi == nil {
		return smartcoll.Item{}
	}
	it := smartcoll.Item{
		ID:       mi.ContentID,
		Title:    mi.Title,
		Genres:   mi.Genres,
		Year:     mi.Year,
		Language: mi.OriginalLanguage,
	}
	if mi.RatingIMDB != nil {
		it.Rating = *mi.RatingIMDB
	}
	if mi.AddedAt != nil {
		it.AddedAt = *mi.AddedAt
	}
	return it
}
```

This needs `models` imported in the handler. Update the import block:

```go
import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/audiobooks/smartcoll"
	"github.com/Silo-Server/silo-server/internal/models"
)
```

- [ ] **Step 5: Run tests + verify**

```bash
go test ./internal/audiobooks/abs/ -run TestSmartCollection_Items -v
go test ./internal/audiobooks/abs/ -count=1 | tail -5
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/audiobooks/abs/smart_collections_handler.go internal/audiobooks/abs/smart_collections_handler_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): GET /me/smart-collections/{id}/items — eval + page

Items handler evaluates the stored query_def against the audiobook
catalog. Per-user state hydrated in 2 batched calls (progress list +
bookmark counts) when caller is the owner; non-owner viewing a
public collection sees personalized rules silently dropped via
EvaluateOptions.AllowPersonalized: false. Results paginated post-eval.
siloItemToSmartcollItem adapter maps silo's MediaItem onto the
audiobook-domain Item shape; author/narrator/series/publisher/
duration_seconds left as zero-values for v1 (Phase 4 follow-up).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: ABSSmartCollectionStore (pgx) + wire + register routes

**Files:**
- Create: `internal/audiobooks/abs_smart_collection_store.go`
- Modify: `internal/audiobooks/service.go`
- Modify: `internal/audiobooks/abs/handler.go`

- [ ] **Step 1: Implement the concrete store**

Create `internal/audiobooks/abs_smart_collection_store.go`:

```go
package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// ABSSmartCollectionStore implements abs.SmartCollectionStore against
// the abs_smart_collections table (migration 153).
type ABSSmartCollectionStore struct {
	Pool *pgxpool.Pool
}

var _ abs.SmartCollectionStore = (*ABSSmartCollectionStore)(nil)

func (s *ABSSmartCollectionStore) ListUserSmartCollections(ctx context.Context, userID, profileID string) ([]abs.SmartCollection, error) {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_smart_collection_store: invalid user id %q: %w", userID, err)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, profile_id, name, description, color, is_public, is_pinned, query_def, created_at, updated_at
		FROM abs_smart_collections
		WHERE user_id = $1
		  AND COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'::uuid)
		      = COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		ORDER BY created_at DESC`,
		uid, profileArg(profileID),
	)
	if err != nil {
		return nil, fmt.Errorf("abs_smart_collection_store: list: %w", err)
	}
	defer rows.Close()
	out := make([]abs.SmartCollection, 0)
	for rows.Next() {
		var c abs.SmartCollection
		var uidScan int
		var profileScan *string
		if err := rows.Scan(&c.ID, &uidScan, &profileScan, &c.Name, &c.Description, &c.Color, &c.IsPublic, &c.IsPinned, &c.QueryDef, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("abs_smart_collection_store: list scan: %w", err)
		}
		c.UserID = strconv.Itoa(uidScan)
		if profileScan != nil {
			c.ProfileID = *profileScan
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_smart_collection_store: list rows: %w", err)
	}
	return out, nil
}

func (s *ABSSmartCollectionStore) GetSmartCollection(ctx context.Context, id string) (abs.SmartCollection, error) {
	var c abs.SmartCollection
	var uidScan int
	var profileScan *string
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, name, description, color, is_public, is_pinned, query_def, created_at, updated_at
		FROM abs_smart_collections WHERE id = $1`, id)
	if err := row.Scan(&c.ID, &uidScan, &profileScan, &c.Name, &c.Description, &c.Color, &c.IsPublic, &c.IsPinned, &c.QueryDef, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.SmartCollection{}, abs.ErrNotFound
		}
		return abs.SmartCollection{}, fmt.Errorf("abs_smart_collection_store: get: %w", err)
	}
	c.UserID = strconv.Itoa(uidScan)
	if profileScan != nil {
		c.ProfileID = *profileScan
	}
	return c, nil
}

func (s *ABSSmartCollectionStore) CreateSmartCollection(ctx context.Context, c abs.SmartCollection) error {
	uid, err := strconv.Atoi(c.UserID)
	if err != nil {
		return fmt.Errorf("abs_smart_collection_store: invalid user id %q: %w", c.UserID, err)
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO abs_smart_collections (id, user_id, profile_id, name, description, color, is_public, is_pinned, query_def)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, $7, $8, $9::jsonb)`,
		c.ID, uid, profileArg(c.ProfileID), c.Name, c.Description, c.Color, c.IsPublic, c.IsPinned, c.QueryDef,
	); err != nil {
		return fmt.Errorf("abs_smart_collection_store: create: %w", err)
	}
	return nil
}

func (s *ABSSmartCollectionStore) UpdateSmartCollection(ctx context.Context, c abs.SmartCollection) error {
	if _, err := s.Pool.Exec(ctx, `
		UPDATE abs_smart_collections
		   SET name = $2, description = $3, color = $4, is_public = $5, is_pinned = $6, query_def = $7::jsonb, updated_at = now()
		 WHERE id = $1`,
		c.ID, c.Name, c.Description, c.Color, c.IsPublic, c.IsPinned, c.QueryDef,
	); err != nil {
		return fmt.Errorf("abs_smart_collection_store: update: %w", err)
	}
	return nil
}

func (s *ABSSmartCollectionStore) DeleteSmartCollection(ctx context.Context, id string) error {
	if _, err := s.Pool.Exec(ctx, `DELETE FROM abs_smart_collections WHERE id = $1`, id); err != nil {
		return fmt.Errorf("abs_smart_collection_store: delete: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Wire in BuildABSHandler**

In `internal/audiobooks/service.go`, after the `playlistStore` block, add:

```go
	var smartCollectionStore abs.SmartCollectionStore
	if deps.Pool != nil {
		smartCollectionStore = &ABSSmartCollectionStore{Pool: deps.Pool}
	}
```

In the `abs.New(abs.Dependencies{...})` call, after `PlaylistStore: playlistStore,`:

```go
		SmartCollectionStore: smartCollectionStore,
```

- [ ] **Step 3: Register routes in mountRoutes**

In `internal/audiobooks/abs/handler.go`, inside the Stage 4 `bearerAuth` `for _, prefix := range` loop, after the playlist routes, append:

```go
			// Smart collections — rule-based dynamic groupings.
			r.Get(prefix+"/me/smart-collections", h.handleListSmartCollections)
			r.Post(prefix+"/me/smart-collections", h.handleCreateSmartCollection)
			r.Get(prefix+"/me/smart-collections/{id}", h.handleGetSmartCollection)
			r.Get(prefix+"/me/smart-collections/{id}/items", h.handleSmartCollectionItems)
			r.Patch(prefix+"/me/smart-collections/{id}", h.handleUpdateSmartCollection)
			r.Delete(prefix+"/me/smart-collections/{id}", h.handleDeleteSmartCollection)
```

- [ ] **Step 4: Build + test**

```bash
go build ./...
go test ./internal/audiobooks/... -count=1 | tail -10
```

- [ ] **Step 5: Commit**

```bash
git add internal/audiobooks/abs_smart_collection_store.go internal/audiobooks/service.go internal/audiobooks/abs/handler.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): wire ABSSmartCollectionStore + mount routes

Pgx-backed store + service wiring + six routes registered under both
/abs/api and /api prefixes inside the existing bearerAuth group.
JSONB column written via $9::jsonb cast for query_def.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Full verification

- [ ] **Step 1: Full test suite**

```bash
go test ./... 2>&1 | grep -E '^FAIL' | head -5
go build ./...
```

- [ ] **Step 2: Frontend gates (per project memory)**

```bash
cd /opt/silo-server/web && pnpm run build 2>&1 | tail -5 ; cd ..
make verify-local-paths
```

- [ ] **Step 3: Migration roundtrip**

```bash
docker compose exec -T postgres psql -U silo -d silo -c "\d abs_smart_collections"
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/153_abs_smart_collections.down.sql
docker compose exec -T postgres psql -U silo -d silo -f - < migrations/153_abs_smart_collections.up.sql
```

- [ ] **Step 4: Live smoke — operator step (skip)**

Document live smoke remains for the operator (per spec §10.3).

- [ ] **Step 5: No commit unless something needed fixing**

If anything failed and was attributable to this sub-project, fix and commit. Otherwise the branch is READY-FOR-OPERATOR-SMOKE.

---

## Out of scope (deferred)

- Author / narrator / series / publisher / duration_seconds hydration on `siloItemToSmartcollItem` — fields stay zero-valued for v1; rules referencing them evaluate false. A follow-up wires the people/series/file-aggregate joins.
- SQL pushdown evaluator.
- GIN index on `query_def`.
- Background materialisation cache.
