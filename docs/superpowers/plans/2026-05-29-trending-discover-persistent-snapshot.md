# Trending Discover Persistent Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the in-process 1-hour cache behind the `trending_discover` home section with a background-refreshed, persisted snapshot so reads never call the upstream provider and refresh history is observable.

**Architecture:** A new `trending_discover_snapshots` table holds one row per canonical `(source, window)` with the ordered, catalog-resolved content IDs. A scheduled task (`refresh_trending_discover`) discovers which combos are used by enabled sections, fetches TMDB/Trakt, resolves external IDs, and upserts. The section read path only reads the snapshot row; per-viewer access filtering stays at read time.

**Tech Stack:** Go, pgx/pgxpool, PostgreSQL, the in-house `taskmanager` framework. Spec: `docs/superpowers/specs/2026-05-29-trending-discover-persistent-snapshot-design.md`.

**Baseline note:** The working tree already contains a clean refactor of the trending code (`newTrendingEntry`, `orderMediaItems`, tidied `fetchTrendingDiscoverEntries`/`resolveTrendingDiscoverIDs`, and a `singleflight` wrapper in `loadTrendingDiscoverContentIDs`). This plan assumes that working-tree state. The free helpers `trendingDiscoverEntry`, `newTrendingEntry`, and `orderedTrendingContentIDs` in `internal/sections/fetcher.go` are reused (not duplicated); the `*Fetcher` methods `fetchTrendingDiscoverEntries` / `resolveTrendingDiscoverIDs` and the fields `TMDBTrending` / `TraktTrending` / `ItemRepo` are removed in Task 6.

**Conventions used below**
- Run all commands from the repository root.
- Lint: `make lint` (golangci-lint). Format: `gofmt -w <file>` before committing.
- Tests in `internal/sections` are pure unit tests — they construct `&Fetcher{...}` / structs directly with fakes and never open a DB pool. Follow that pattern.

---

## Task 1: Migration — `trending_discover_snapshots`

**Files:**
- Create: `migrations/166_trending_discover_snapshots.up.sql`
- Create: `migrations/166_trending_discover_snapshots.down.sql`

Note: `166` is the next free number (current max on this branch is `165`). The column is named `time_window` (not `window`) to match the existing collection vocabulary (`source_config.time_window`) and avoid any keyword friction.

- [ ] **Step 1: Write the up migration**

Create `migrations/166_trending_discover_snapshots.up.sql`:

```sql
-- Persisted snapshot of external global trending (TMDB / Trakt) for the
-- trending_discover home section. One row per canonical (source, time_window):
-- content_ids are already resolved to library catalog content IDs and ordered
-- by trending rank. A background task refreshes these rows; the section read
-- path only reads them, so a slow or down provider never blocks the home page.
CREATE TABLE public.trending_discover_snapshots (
    source          text        NOT NULL,            -- 'tmdb' | 'trakt'
    time_window     text        NOT NULL,            -- 'day' | 'week' (trakt pinned to 'week')
    content_ids     text[]      NOT NULL DEFAULT '{}'::text[],
    entry_count     integer     NOT NULL DEFAULT 0,  -- raw provider entries fetched
    refreshed_at    timestamptz,                     -- last successful refresh
    last_attempt_at timestamptz,                     -- last attempt (success or failure)
    last_status     text        NOT NULL DEFAULT '', -- 'ok' | 'empty' | 'error'
    last_error      text        NOT NULL DEFAULT '',
    PRIMARY KEY (source, time_window)
);
```

- [ ] **Step 2: Write the down migration**

Create `migrations/166_trending_discover_snapshots.down.sql`:

```sql
DROP TABLE IF EXISTS public.trending_discover_snapshots;
```

- [ ] **Step 3: Verify the SQL parses by applying it to the dev DB**

Migrations are applied on backend startup. Apply manually to confirm the SQL is valid (dev DB role/db is `continuum`, per workspace notes):

Run: `psql "$DATABASE_URL" -f migrations/166_trending_discover_snapshots.up.sql && psql "$DATABASE_URL" -f migrations/166_trending_discover_snapshots.down.sql`
Expected: `CREATE TABLE` then `DROP TABLE` with no errors. (Re-apply the up migration afterward, or let the next server start apply it, so later manual testing has the table.)

If `psql`/`$DATABASE_URL` is not configured in this environment, skip this step — Task 8 starts the dev backend, which applies the migration through the normal embedded-migration path.

- [ ] **Step 4: Commit**

```bash
git add migrations/166_trending_discover_snapshots.up.sql migrations/166_trending_discover_snapshots.down.sql
git commit -m "feat(sections): add trending_discover_snapshots table"
```

---

## Task 2: Snapshot model, canonical key, and repository

**Files:**
- Create: `internal/sections/trending_snapshot.go`
- Test: `internal/sections/trending_snapshot_test.go`

This task adds the persisted model, the `canonicalTrendingKey` helper (shared by the read path, the refresher, and the repo), and the `TrendingSnapshotRepository`. Only `canonicalTrendingKey` is unit-tested (the repo is a thin pgx wrapper exercised end-to-end in Task 8, matching how other repos in this package are tested).

- [ ] **Step 1: Write the failing test for `canonicalTrendingKey`**

Create `internal/sections/trending_snapshot_test.go`:

```go
package sections

import "testing"

func TestCanonicalTrendingKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		src, win         string
		wantSrc, wantWin string
	}{
		{"tmdb", "day", "tmdb", "day"},
		{"tmdb", "week", "tmdb", "week"},
		{"tmdb", "", "tmdb", "week"},
		{"", "day", "tmdb", "day"},
		{"", "", "tmdb", "week"},
		{"trakt", "day", "trakt", "week"},
		{"trakt", "week", "trakt", "week"},
		{"trakt", "", "trakt", "week"},
		{"bogus", "bogus", "tmdb", "week"},
	}
	for _, c := range cases {
		gotSrc, gotWin := canonicalTrendingKey(c.src, c.win)
		if gotSrc != c.wantSrc || gotWin != c.wantWin {
			t.Errorf("canonicalTrendingKey(%q, %q) = (%q, %q); want (%q, %q)",
				c.src, c.win, gotSrc, gotWin, c.wantSrc, c.wantWin)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/sections/ -run TestCanonicalTrendingKey`
Expected: FAIL — `undefined: canonicalTrendingKey`.

- [ ] **Step 3: Write `internal/sections/trending_snapshot.go`**

```go
package sections

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TrendingSnapshot is the persisted result of one external-trending refresh for
// a canonical (Source, Window). ContentIDs are resolved to library catalog
// content IDs and ordered by trending rank. The list is viewer-agnostic;
// per-viewer access filtering happens at read time.
type TrendingSnapshot struct {
	Source        string
	Window        string
	ContentIDs    []string
	EntryCount    int
	RefreshedAt   *time.Time
	LastAttemptAt *time.Time
	LastStatus    string
	LastError     string
}

// canonicalTrendingKey normalizes a section's configured source/window into the
// snapshot key space. Source is "trakt" only when explicitly set; everything
// else collapses to "tmdb". Trakt ignores the time window, so it is pinned to
// "week" to avoid duplicate identical rows. For TMDB, "day" is honored only
// when explicitly set; anything else is "week".
func canonicalTrendingKey(source, window string) (string, string) {
	if source != "trakt" {
		source = "tmdb"
	}
	if source == "trakt" {
		return "trakt", "week"
	}
	if window != "day" {
		window = "week"
	}
	return "tmdb", window
}

// TrendingSnapshotRepository persists and reads trending_discover_snapshots.
type TrendingSnapshotRepository struct {
	pool *pgxpool.Pool
}

// NewTrendingSnapshotRepository creates a new TrendingSnapshotRepository.
func NewTrendingSnapshotRepository(pool *pgxpool.Pool) *TrendingSnapshotRepository {
	return &TrendingSnapshotRepository{pool: pool}
}

// Get returns the snapshot for the canonical (source, window). found is false
// when no row exists yet (before the first refresh).
func (r *TrendingSnapshotRepository) Get(ctx context.Context, source, window string) (TrendingSnapshot, bool, error) {
	source, window = canonicalTrendingKey(source, window)
	row := r.pool.QueryRow(ctx, `
		SELECT source, time_window, content_ids, entry_count,
		       refreshed_at, last_attempt_at, last_status, last_error
		FROM trending_discover_snapshots
		WHERE source = $1 AND time_window = $2`, source, window)

	var s TrendingSnapshot
	err := row.Scan(&s.Source, &s.Window, &s.ContentIDs, &s.EntryCount,
		&s.RefreshedAt, &s.LastAttemptAt, &s.LastStatus, &s.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return TrendingSnapshot{}, false, nil
	}
	if err != nil {
		return TrendingSnapshot{}, false, fmt.Errorf("getting trending snapshot: %w", err)
	}
	return s, true, nil
}

// SaveSuccess records a completed refresh, replacing the content list. status is
// "ok" when at least one entry matched the catalog and "empty" when the provider
// returned entries but none matched. Used only when the provider actually
// returned data; see RecordAttempt for the no-data / failure paths.
func (r *TrendingSnapshotRepository) SaveSuccess(ctx context.Context, source, window string, contentIDs []string, entryCount int, status string, at time.Time) error {
	source, window = canonicalTrendingKey(source, window)
	if contentIDs == nil {
		contentIDs = []string{}
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO trending_discover_snapshots
			(source, time_window, content_ids, entry_count, refreshed_at, last_attempt_at, last_status, last_error)
		VALUES ($1, $2, $3, $4, $5, $5, $6, '')
		ON CONFLICT (source, time_window) DO UPDATE SET
			content_ids     = EXCLUDED.content_ids,
			entry_count     = EXCLUDED.entry_count,
			refreshed_at    = EXCLUDED.refreshed_at,
			last_attempt_at = EXCLUDED.last_attempt_at,
			last_status     = EXCLUDED.last_status,
			last_error      = ''`,
		source, window, contentIDs, entryCount, at, status)
	if err != nil {
		return fmt.Errorf("saving trending snapshot: %w", err)
	}
	return nil
}

// RecordAttempt records an attempt that produced no new content (an upstream
// failure or an unconfigured/empty provider) WITHOUT clearing the last-good
// content_ids. status is "error" or "empty". If no row exists yet it inserts a
// placeholder so the attempt is still observable.
func (r *TrendingSnapshotRepository) RecordAttempt(ctx context.Context, source, window, status, message string, at time.Time) error {
	source, window = canonicalTrendingKey(source, window)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO trending_discover_snapshots
			(source, time_window, last_attempt_at, last_status, last_error)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (source, time_window) DO UPDATE SET
			last_attempt_at = EXCLUDED.last_attempt_at,
			last_status     = EXCLUDED.last_status,
			last_error      = EXCLUDED.last_error`,
		source, window, at, status, message)
	if err != nil {
		return fmt.Errorf("recording trending snapshot attempt: %w", err)
	}
	return nil
}

// ListAll returns every snapshot row, ordered, for inspection and tests.
func (r *TrendingSnapshotRepository) ListAll(ctx context.Context) ([]TrendingSnapshot, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT source, time_window, content_ids, entry_count,
		       refreshed_at, last_attempt_at, last_status, last_error
		FROM trending_discover_snapshots
		ORDER BY source, time_window`)
	if err != nil {
		return nil, fmt.Errorf("listing trending snapshots: %w", err)
	}
	defer rows.Close()

	var out []TrendingSnapshot
	for rows.Next() {
		var s TrendingSnapshot
		if err := rows.Scan(&s.Source, &s.Window, &s.ContentIDs, &s.EntryCount,
			&s.RefreshedAt, &s.LastAttemptAt, &s.LastStatus, &s.LastError); err != nil {
			return nil, fmt.Errorf("scanning trending snapshot: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/sections/ -run TestCanonicalTrendingKey`
Expected: PASS.

- [ ] **Step 5: Verify the package compiles**

Run: `go build ./internal/sections/`
Expected: no output (success).

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/sections/trending_snapshot.go internal/sections/trending_snapshot_test.go
git add internal/sections/trending_snapshot.go internal/sections/trending_snapshot_test.go
git commit -m "feat(sections): add trending snapshot model and repository"
```

---

## Task 3: Enumerate used `(source, window)` combos

**Files:**
- Modify: `internal/sections/repo.go` (add method near the other list methods, e.g. after `ListByScopeAll`)

The refresher needs the config JSON of every enabled `trending_discover` section across all scopes/libraries. `repo.go` already imports `encoding/json` and `fmt` and the `Repository` has a `pool *pgxpool.Pool` field.

- [ ] **Step 1: Add `ListTrendingDiscoverConfigs` to `internal/sections/repo.go`**

Insert this method (place it after the `ListByScopeAll` method, around line 138):

```go
// ListTrendingDiscoverConfigs returns the config JSON of every enabled
// trending_discover section across all scopes and libraries. The trending
// refresh task uses this to discover which (source, window) combinations need a
// snapshot, so dormant configs (no enabled sections) trigger zero upstream work.
func (r *Repository) ListTrendingDiscoverConfigs(ctx context.Context) ([]json.RawMessage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT config FROM page_sections
		WHERE section_type = $1 AND enabled = true`, string(SectionTrendingDiscover))
	if err != nil {
		return nil, fmt.Errorf("listing trending_discover configs: %w", err)
	}
	defer rows.Close()

	var out []json.RawMessage
	for rows.Next() {
		var cfg json.RawMessage
		if err := rows.Scan(&cfg); err != nil {
			return nil, fmt.Errorf("scanning trending_discover config: %w", err)
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}
```

- [ ] **Step 2: Verify the package compiles**

Run: `go build ./internal/sections/`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
gofmt -w internal/sections/repo.go
git add internal/sections/repo.go
git commit -m "feat(sections): list enabled trending_discover section configs"
```

---

## Task 4: `TrendingRefresher`

**Files:**
- Create: `internal/sections/trending_refresher.go`
- Test: `internal/sections/trending_refresher_test.go`

The refresher owns the upstream call and external-ID resolution via consumer-side interfaces (so it is unit-testable with fakes). It reuses the free helpers `trendingDiscoverEntry`, `newTrendingEntry`, and `orderedTrendingContentIDs` that live in `fetcher.go` (same package). Its `fetchEntries` / `resolveIDs` bodies are copied from the soon-to-be-removed `*Fetcher` methods; those originals are deleted in Task 6 (transient duplication, resolved within this plan).

- [ ] **Step 1: Write the failing tests**

Create `internal/sections/trending_refresher_test.go`:

```go
package sections

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

type fakeSectionLister struct {
	configs []json.RawMessage
	err     error
}

func (f fakeSectionLister) ListTrendingDiscoverConfigs(context.Context) ([]json.RawMessage, error) {
	return f.configs, f.err
}

type savedSnap struct {
	contentIDs []string
	entryCount int
	status     string
}

type attemptRec struct {
	status  string
	message string
}

type fakeSnapshotStore struct {
	saved    map[string]savedSnap
	attempts map[string]attemptRec
}

func newFakeSnapshotStore() *fakeSnapshotStore {
	return &fakeSnapshotStore{saved: map[string]savedSnap{}, attempts: map[string]attemptRec{}}
}

func (f *fakeSnapshotStore) SaveSuccess(_ context.Context, source, window string, contentIDs []string, entryCount int, status string, _ time.Time) error {
	f.saved[source+"|"+window] = savedSnap{contentIDs: contentIDs, entryCount: entryCount, status: status}
	return nil
}

func (f *fakeSnapshotStore) RecordAttempt(_ context.Context, source, window, status, message string, _ time.Time) error {
	f.attempts[source+"|"+window] = attemptRec{status: status, message: message}
	return nil
}

type fakeTMDB struct {
	entries []catalog.TMDBCollectionEntry
	err     error
}

func (f fakeTMDB) GetCollectionPreset(context.Context, string, string, string, int) ([]catalog.TMDBCollectionEntry, error) {
	return f.entries, f.err
}

type fakeResolver struct {
	byType map[string]*catalog.ExternalIDLookup
}

func (f fakeResolver) GetByExternalIDs(_ context.Context, _ catalog.ExternalIDBatch, itemType string) (*catalog.ExternalIDLookup, error) {
	if lk, ok := f.byType[itemType]; ok {
		return lk, nil
	}
	return &catalog.ExternalIDLookup{ByTMDB: map[string]string{}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}}, nil
}

func tmdbConfig(t *testing.T, source, window string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(recipes.TrendingDiscoverParams{Source: source, Window: window})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return raw
}

func TestRefresherSavesOrderedContentIDs(t *testing.T) {
	store := newFakeSnapshotStore()
	r := &TrendingRefresher{
		Sections:  fakeSectionLister{configs: []json.RawMessage{tmdbConfig(t, "tmdb", "week")}},
		Snapshots: store,
		Resolver: fakeResolver{byType: map[string]*catalog.ExternalIDLookup{
			"movie":  {ByTMDB: map[string]string{"10": "c-movie"}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}},
			"series": {ByTMDB: map[string]string{"20": "c-series"}, ByIMDb: map[string]string{}, ByTVDB: map[string]string{}},
		}},
		TMDBTrending: fakeTMDB{entries: []catalog.TMDBCollectionEntry{
			{ID: 10, MediaType: "movie"},
			{ID: 20, MediaType: "tv"},
		}},
		Clock: recipes.FixedClock(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)),
	}

	data, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	var result TrendingRefreshResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.Combos != 1 || result.Refreshed != 1 || result.Failed != 0 || result.Empty != 0 {
		t.Fatalf("result = %+v; want {Combos:1 Refreshed:1 Empty:0 Failed:0}", result)
	}

	got := store.saved["tmdb|week"]
	want := []string{"c-movie", "c-series"}
	if len(got.contentIDs) != len(want) || got.contentIDs[0] != want[0] || got.contentIDs[1] != want[1] {
		t.Fatalf("saved content IDs = %v; want %v", got.contentIDs, want)
	}
	if got.status != "ok" || got.entryCount != 2 {
		t.Fatalf("saved snap = %+v; want status ok, entryCount 2", got)
	}
}

func TestRefresherFailurePreservesLastGood(t *testing.T) {
	store := newFakeSnapshotStore()
	r := &TrendingRefresher{
		Sections:     fakeSectionLister{configs: []json.RawMessage{tmdbConfig(t, "tmdb", "week")}},
		Snapshots:    store,
		Resolver:     fakeResolver{},
		TMDBTrending: fakeTMDB{err: errors.New("tmdb 503")},
		Clock:        recipes.FixedClock(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)),
	}

	data, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if _, ok := store.saved["tmdb|week"]; ok {
		t.Fatal("SaveSuccess must not be called on fetch failure (would clear last-good)")
	}
	att, ok := store.attempts["tmdb|week"]
	if !ok || att.status != "error" {
		t.Fatalf("attempt = %+v, ok=%v; want status error", att, ok)
	}

	var result TrendingRefreshResult
	_ = json.Unmarshal(data, &result)
	if result.Failed != 1 {
		t.Fatalf("result.Failed = %d; want 1", result.Failed)
	}
}

func TestRefresherEmptyProviderPreservesLastGood(t *testing.T) {
	store := newFakeSnapshotStore()
	r := &TrendingRefresher{
		Sections:  fakeSectionLister{configs: []json.RawMessage{tmdbConfig(t, "tmdb", "week")}},
		Snapshots: store,
		Resolver:  fakeResolver{},
		// TMDBTrending nil => provider unconfigured => empty entries, no error.
		Clock: recipes.FixedClock(time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)),
	}

	data, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if _, ok := store.saved["tmdb|week"]; ok {
		t.Fatal("SaveSuccess must not be called when provider returns no entries")
	}
	att := store.attempts["tmdb|week"]
	if att.status != "empty" {
		t.Fatalf("attempt status = %q; want empty", att.status)
	}
	var result TrendingRefreshResult
	_ = json.Unmarshal(data, &result)
	if result.Empty != 1 {
		t.Fatalf("result.Empty = %d; want 1", result.Empty)
	}
}

func TestDistinctTrendingCombosCollapsesTrakt(t *testing.T) {
	configs := []json.RawMessage{
		tmdbConfig(t, "trakt", "day"),
		tmdbConfig(t, "trakt", "week"),
		tmdbConfig(t, "tmdb", "day"),
		tmdbConfig(t, "tmdb", "day"),
	}
	got := distinctTrendingCombos(configs)
	if len(got) != 2 {
		t.Fatalf("distinctTrendingCombos len = %d (%+v); want 2", len(got), got)
	}
	seen := map[trendingCombo]bool{}
	for _, c := range got {
		seen[c] = true
	}
	if !seen[trendingCombo{"trakt", "week"}] || !seen[trendingCombo{"tmdb", "day"}] {
		t.Fatalf("combos = %+v; want {trakt week} and {tmdb day}", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/sections/ -run 'TestRefresher|TestDistinctTrendingCombos'`
Expected: FAIL — `undefined: TrendingRefresher`, `undefined: TrendingRefreshResult`, `undefined: distinctTrendingCombos`, `undefined: trendingCombo`.

- [ ] **Step 3: Write `internal/sections/trending_refresher.go`**

```go
package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// trendingFetchCap is the over-fetch size for each refresh. Library-only
// matching drops globally-trending titles the server does not own, so we fetch
// well beyond any section's display limit and store the matched, ordered list.
const trendingFetchCap = 200

// trendingSectionConfigLister enumerates enabled trending_discover section
// configs. Satisfied by *Repository.
type trendingSectionConfigLister interface {
	ListTrendingDiscoverConfigs(ctx context.Context) ([]json.RawMessage, error)
}

// trendingSnapshotStore is the write side of the snapshot table. Satisfied by
// *TrendingSnapshotRepository.
type trendingSnapshotStore interface {
	SaveSuccess(ctx context.Context, source, window string, contentIDs []string, entryCount int, status string, at time.Time) error
	RecordAttempt(ctx context.Context, source, window, status, message string, at time.Time) error
}

// trendingExternalIDResolver resolves external IDs to library content IDs.
// Satisfied by *catalog.ItemRepository.
type trendingExternalIDResolver interface {
	GetByExternalIDs(ctx context.Context, batch catalog.ExternalIDBatch, itemType string) (*catalog.ExternalIDLookup, error)
}

// TrendingRefresher fetches external global trending (TMDB/Trakt), resolves it
// to library content IDs, and persists one snapshot per canonical
// (source, window). It is driven by a TaskManager task on an interval.
type TrendingRefresher struct {
	Sections      trendingSectionConfigLister
	Snapshots     trendingSnapshotStore
	Resolver      trendingExternalIDResolver
	TMDBTrending  catalog.TMDBCollectionFetcher
	TraktTrending catalog.TraktCollectionFetcher

	// Clock defaults to recipes.RealClock{}. Tests inject recipes.FixedClock.
	Clock  recipes.Clock
	logger *slog.Logger
}

// NewTrendingRefresher creates a refresher with real-clock and default logger.
func NewTrendingRefresher(
	sectionsRepo trendingSectionConfigLister,
	snapshots trendingSnapshotStore,
	resolver trendingExternalIDResolver,
	tmdb catalog.TMDBCollectionFetcher,
	trakt catalog.TraktCollectionFetcher,
) *TrendingRefresher {
	return &TrendingRefresher{
		Sections:      sectionsRepo,
		Snapshots:     snapshots,
		Resolver:      resolver,
		TMDBTrending:  tmdb,
		TraktTrending: trakt,
		Clock:         recipes.RealClock{},
		logger:        slog.Default(),
	}
}

func (r *TrendingRefresher) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

// TrendingRefreshResult is the JSON summary attached to the task execution.
type TrendingRefreshResult struct {
	Combos    int `json:"combos"`
	Refreshed int `json:"refreshed"`
	Empty     int `json:"empty"`
	Failed    int `json:"failed"`
}

type trendingCombo struct {
	source string
	window string
}

// distinctTrendingCombos parses section configs and returns the deduplicated set
// of canonical (source, window) pairs that need a snapshot.
func distinctTrendingCombos(configs []json.RawMessage) []trendingCombo {
	seen := make(map[trendingCombo]struct{}, len(configs))
	out := make([]trendingCombo, 0, len(configs))
	for _, raw := range configs {
		var p recipes.TrendingDiscoverParams
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &p)
		}
		source, window := canonicalTrendingKey(p.Source, p.Window)
		c := trendingCombo{source: source, window: window}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

// RunOnce refreshes every (source, window) used by an enabled trending_discover
// section. Per-combo failures are recorded and never abort the others. The JSON
// summary is suitable for task result data.
func (r *TrendingRefresher) RunOnce(ctx context.Context) (json.RawMessage, error) {
	configs, err := r.Sections.ListTrendingDiscoverConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing trending_discover sections: %w", err)
	}

	combos := distinctTrendingCombos(configs)
	result := TrendingRefreshResult{Combos: len(combos)}
	for _, c := range combos {
		switch r.refreshCombo(ctx, c.source, c.window) {
		case "ok":
			result.Refreshed++
		case "empty":
			result.Empty++
		default:
			result.Failed++
		}
	}

	data, _ := json.Marshal(result)
	return data, nil
}

// refreshCombo refreshes a single canonical (source, window) and returns its
// outcome: "ok", "empty", or "error". A fetch failure or an unconfigured/empty
// provider preserves the last-good content list (RecordAttempt). When the
// provider returns entries, the list is replaced even if nothing matched the
// catalog ("empty" status with an empty list) — that genuinely reflects current
// trending having no library matches.
func (r *TrendingRefresher) refreshCombo(ctx context.Context, source, window string) string {
	now := r.now()

	entries, err := r.fetchEntries(ctx, source, window, trendingFetchCap)
	if err != nil {
		r.logger.Error("trending refresh: fetch failed", "source", source, "window", window, "error", err)
		_ = r.Snapshots.RecordAttempt(ctx, source, window, "error", err.Error(), now)
		return "error"
	}
	if len(entries) == 0 {
		// Provider unconfigured or returned nothing: keep last-good, mark empty.
		_ = r.Snapshots.RecordAttempt(ctx, source, window, "empty", "", now)
		return "empty"
	}

	contentIDs, err := r.resolveIDs(ctx, entries)
	if err != nil {
		r.logger.Error("trending refresh: resolve failed", "source", source, "window", window, "error", err)
		_ = r.Snapshots.RecordAttempt(ctx, source, window, "error", err.Error(), now)
		return "error"
	}

	status := "ok"
	if len(contentIDs) == 0 {
		status = "empty"
	}
	if err := r.Snapshots.SaveSuccess(ctx, source, window, contentIDs, len(entries), status, now); err != nil {
		r.logger.Error("trending refresh: save failed", "source", source, "window", window, "error", err)
		return "error"
	}
	return status
}

// fetchEntries pulls the raw trending list from the configured provider. A
// nil/unconfigured provider yields an empty list (no error).
func (r *TrendingRefresher) fetchEntries(ctx context.Context, source, window string, fetchLimit int) ([]trendingDiscoverEntry, error) {
	if source == "trakt" {
		if r.TraktTrending == nil {
			return nil, nil
		}
		movies, movieErr := r.TraktTrending.GetCollectionPreset(ctx, "trending", "movie", fetchLimit, "")
		shows, showErr := r.TraktTrending.GetCollectionPreset(ctx, "trending", "tv", fetchLimit, "")
		if movieErr != nil && showErr != nil {
			return nil, fmt.Errorf("trakt trending: %v / %v", movieErr, showErr)
		}
		out := make([]trendingDiscoverEntry, 0, len(movies)+len(shows))
		for _, e := range movies {
			out = append(out, newTrendingEntry(e.TMDBID, e.TVDBID, e.IMDbID, e.MediaType))
		}
		for _, e := range shows {
			out = append(out, newTrendingEntry(e.TMDBID, e.TVDBID, e.IMDbID, e.MediaType))
		}
		return out, nil
	}

	if r.TMDBTrending == nil {
		return nil, nil
	}
	entries, err := r.TMDBTrending.GetCollectionPreset(ctx, "trending", "all", window, fetchLimit)
	if err != nil {
		return nil, err
	}
	out := make([]trendingDiscoverEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, newTrendingEntry(e.ID, e.TVDBID, e.IMDbID, e.MediaType))
	}
	return out, nil
}

// resolveIDs matches trending entries to library content IDs via two batched
// external-ID lookups (movies, series), preserving trending order.
func (r *TrendingRefresher) resolveIDs(ctx context.Context, entries []trendingDiscoverEntry) ([]string, error) {
	if r.Resolver == nil {
		return nil, fmt.Errorf("trending_discover: external ID resolver not configured")
	}
	var movieBatch, seriesBatch catalog.ExternalIDBatch
	for _, e := range entries {
		batch := &movieBatch
		if e.mediaType == "tv" {
			batch = &seriesBatch
		}
		if e.tmdbID != "" {
			batch.TMDBIDs = append(batch.TMDBIDs, e.tmdbID)
		}
		if e.imdbID != "" {
			batch.IMDbIDs = append(batch.IMDbIDs, e.imdbID)
		}
		if e.tvdbID != "" {
			batch.TVDBIDs = append(batch.TVDBIDs, e.tvdbID)
		}
	}
	movieLookup, err := r.Resolver.GetByExternalIDs(ctx, movieBatch, "movie")
	if err != nil {
		return nil, err
	}
	seriesLookup, err := r.Resolver.GetByExternalIDs(ctx, seriesBatch, "series")
	if err != nil {
		return nil, err
	}
	return orderedTrendingContentIDs(entries, movieLookup, seriesLookup), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/sections/ -run 'TestRefresher|TestDistinctTrendingCombos'`
Expected: PASS (all four tests).

- [ ] **Step 5: Verify the package compiles**

Run: `go build ./internal/sections/`
Expected: no output (success). Note: `fetcher.go` still defines its own `fetchTrendingDiscoverEntries`/`resolveTrendingDiscoverIDs` at this point — that is expected and removed in Task 6.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/sections/trending_refresher.go internal/sections/trending_refresher_test.go
git add internal/sections/trending_refresher.go internal/sections/trending_refresher_test.go
git commit -m "feat(sections): add trending refresher with persisted snapshots"
```

---

## Task 5: `RefreshTrendingDiscoverTask`

**Files:**
- Create: `internal/taskmanager/tasks/refresh_trending_discover.go`

Mirrors `internal/taskmanager/tasks/sync_collections.go`. Triggers on startup (so the first snapshot lands quickly) and hourly thereafter.

- [ ] **Step 1: Write `internal/taskmanager/tasks/refresh_trending_discover.go`**

```go
package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// TrendingDiscoverRefresher runs a single pass of the trending refresh.
// Satisfied by *sections.TrendingRefresher.
type TrendingDiscoverRefresher interface {
	RunOnce(ctx context.Context) (json.RawMessage, error)
}

// RefreshTrendingDiscoverTask refreshes the persisted external-trending
// snapshots used by trending_discover home sections.
type RefreshTrendingDiscoverTask struct {
	refresher TrendingDiscoverRefresher
}

// NewRefreshTrendingDiscoverTask creates a new RefreshTrendingDiscoverTask.
func NewRefreshTrendingDiscoverTask(refresher TrendingDiscoverRefresher) *RefreshTrendingDiscoverTask {
	return &RefreshTrendingDiscoverTask{refresher: refresher}
}

func (t *RefreshTrendingDiscoverTask) Key() string  { return "refresh_trending_discover" }
func (t *RefreshTrendingDiscoverTask) Name() string { return "Refresh Trending Discover" }
func (t *RefreshTrendingDiscoverTask) Description() string {
	return "Refreshes the persisted external trending list (TMDB/Trakt) for the Trending Discover home section"
}

func (t *RefreshTrendingDiscoverTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}

func (t *RefreshTrendingDiscoverTask) IsHidden() bool { return false }

func (t *RefreshTrendingDiscoverTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 60 * 60 * 1000}, // hourly
	}
}

func (t *RefreshTrendingDiscoverTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Refreshing trending discover")

	resultData, err := t.refresher.RunOnce(ctx)
	if err != nil {
		return fmt.Errorf("trending discover refresh: %w", err)
	}
	if resultData != nil {
		progress.SetResultData(resultData)
	}

	progress.Report(100, "Trending discover refresh complete")
	return nil
}
```

- [ ] **Step 2: Verify the package compiles**

Run: `go build ./internal/taskmanager/...`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
gofmt -w internal/taskmanager/tasks/refresh_trending_discover.go
git add internal/taskmanager/tasks/refresh_trending_discover.go
git commit -m "feat(tasks): add refresh_trending_discover task"
```

---

## Task 6: Rewire the Fetcher read path to the snapshot

**Files:**
- Modify: `internal/sections/fetcher.go`
- Test: `internal/sections/trending_read_test.go` (create)

This task: (a) adds the `TrendingSnapshots` read dependency to `Fetcher`; (b) removes the `TMDBTrending`, `TraktTrending`, and `ItemRepo` fields plus the `fetchTrendingDiscoverEntries` / `resolveTrendingDiscoverIDs` methods (now owned by the refresher); (c) rewrites `loadTrendingDiscoverContentIDs` to read the snapshot; (d) simplifies `fetchTrendingDiscover`. The free helpers `trendingDiscoverEntry`, `newTrendingEntry`, `orderedTrendingContentIDs` stay.

- [ ] **Step 1: Write the failing read-path test**

Create `internal/sections/trending_read_test.go`:

```go
package sections

import (
	"context"
	"testing"
)

type fakeSnapshotGetter struct {
	snap  TrendingSnapshot
	found bool
	err   error
}

func (f fakeSnapshotGetter) Get(context.Context, string, string) (TrendingSnapshot, bool, error) {
	return f.snap, f.found, f.err
}

func TestLoadTrendingDiscoverContentIDsReadsSnapshot(t *testing.T) {
	f := &Fetcher{TrendingSnapshots: fakeSnapshotGetter{
		snap:  TrendingSnapshot{ContentIDs: []string{"a", "b"}},
		found: true,
	}}
	ids, err := f.loadTrendingDiscoverContentIDs(context.Background(), "tmdb", "week")
	if err != nil {
		t.Fatalf("loadTrendingDiscoverContentIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids = %v; want [a b]", ids)
	}
}

func TestLoadTrendingDiscoverContentIDsNilGetter(t *testing.T) {
	f := &Fetcher{}
	ids, err := f.loadTrendingDiscoverContentIDs(context.Background(), "tmdb", "week")
	if err != nil {
		t.Fatalf("loadTrendingDiscoverContentIDs: %v", err)
	}
	if ids != nil {
		t.Fatalf("ids = %v; want nil for nil getter", ids)
	}
}

func TestLoadTrendingDiscoverContentIDsNotFound(t *testing.T) {
	f := &Fetcher{TrendingSnapshots: fakeSnapshotGetter{found: false}}
	ids, err := f.loadTrendingDiscoverContentIDs(context.Background(), "tmdb", "week")
	if err != nil {
		t.Fatalf("loadTrendingDiscoverContentIDs: %v", err)
	}
	if ids != nil {
		t.Fatalf("ids = %v; want nil when no snapshot exists", ids)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/sections/ -run TestLoadTrendingDiscoverContentIDs`
Expected: FAIL — `unknown field 'TrendingSnapshots' in struct literal` / `loadTrendingDiscoverContentIDs` signature mismatch.

- [ ] **Step 3: Add the snapshot reader interface and field to `Fetcher`**

In `internal/sections/fetcher.go`, replace the trending dependency fields in the `Fetcher` struct. Find this block (around lines 71-77):

```go
	// ItemRepo resolves external IDs (TMDB/Trakt) to library content IDs for
	// the trending_discover section. Nil disables external-trending matching.
	ItemRepo *catalog.ItemRepository
	// TMDBTrending and TraktTrending fetch external global trending lists. Each
	// is nil when that provider is not configured.
	TMDBTrending  catalog.TMDBCollectionFetcher
	TraktTrending catalog.TraktCollectionFetcher
```

Replace it with:

```go
	// TrendingSnapshots reads the persisted external-trending snapshots that
	// back the trending_discover section. Nil renders that section empty.
	// Snapshots are produced out-of-band by TrendingRefresher, so the read path
	// never calls the upstream provider.
	TrendingSnapshots trendingSnapshotGetter
```

Then add the interface declaration just above the `Fetcher` struct definition (above `// Fetcher runs section queries against the database.`):

```go
// trendingSnapshotGetter is the read side of the trending snapshot table.
// Satisfied by *TrendingSnapshotRepository.
type trendingSnapshotGetter interface {
	Get(ctx context.Context, source, window string) (TrendingSnapshot, bool, error)
}
```

- [ ] **Step 4: Rewrite `loadTrendingDiscoverContentIDs`**

In `internal/sections/fetcher.go`, replace the entire `loadTrendingDiscoverContentIDs` method (the version that uses `ensureEditorialCandidateCache` / `candidateGroup.Do`) with:

```go
// loadTrendingDiscoverContentIDs returns the persisted, catalog-resolved content
// IDs for the canonical (source, window). It reads only the snapshot table; the
// upstream fetch happens out-of-band in TrendingRefresher. Returns nil when no
// snapshot reader is configured or no snapshot exists yet.
func (f *Fetcher) loadTrendingDiscoverContentIDs(ctx context.Context, source, window string) ([]string, error) {
	if f.TrendingSnapshots == nil {
		return nil, nil
	}
	snap, found, err := f.TrendingSnapshots.Get(ctx, source, window)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return snap.ContentIDs, nil
}
```

- [ ] **Step 5: Simplify `fetchTrendingDiscover`**

In `internal/sections/fetcher.go`, in `fetchTrendingDiscover`, replace the source/window normalization and the `fetchLimit` block. Find:

```go
	source := p.Source
	if source != "trakt" {
		source = "tmdb"
	}
	window := p.Window
	if window != "day" {
		window = "week"
	}

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}
	// Over-fetch: library-only matching drops globally-trending titles the
	// server does not own, so request more candidates than the display limit.
	fetchLimit := limit * 5
	if fetchLimit < 50 {
		fetchLimit = 50
	}
	if fetchLimit > 200 {
		fetchLimit = 200
	}

	orderedIDs, err := f.loadTrendingDiscoverContentIDs(ctx, source, window, fetchLimit)
```

Replace with:

```go
	source, window := canonicalTrendingKey(p.Source, p.Window)

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}

	orderedIDs, err := f.loadTrendingDiscoverContentIDs(ctx, source, window)
```

- [ ] **Step 6: Delete the now-orphaned fetcher methods**

In `internal/sections/fetcher.go`, delete the two methods `func (f *Fetcher) fetchTrendingDiscoverEntries(...)` and `func (f *Fetcher) resolveTrendingDiscoverIDs(...)` in their entirety (their logic now lives on `TrendingRefresher`). Keep `trendingDiscoverEntry`, `newTrendingEntry`, and `orderedTrendingContentIDs`.

- [ ] **Step 7: Build and fix any leftover references**

Run: `go build ./internal/sections/`
Expected: success. If the compiler reports `catalog` imported and not used, confirm `catalog` is still referenced elsewhere in `fetcher.go` (it is — `catalog.AccessFilter` and others). Do not remove the import. If it reports the removed methods are still referenced, ensure Step 4/5 fully replaced the old call site.

- [ ] **Step 8: Run the read-path tests**

Run: `go test ./internal/sections/ -run TestLoadTrendingDiscoverContentIDs`
Expected: PASS (all three).

- [ ] **Step 9: Run the full sections package test suite**

Run: `go test ./internal/sections/`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
gofmt -w internal/sections/fetcher.go internal/sections/trending_read_test.go
git add internal/sections/fetcher.go internal/sections/trending_read_test.go
git commit -m "refactor(sections): read trending_discover from persisted snapshot"
```

---

## Task 7: Wiring — construct the refresher, register the task, set the reader

**Files:**
- Modify: `internal/api/router.go` (around lines 883-885)
- Modify: `cmd/silo/main.go` (declare near line 1235, build near 1238-1244, register near 1287-1289)

- [ ] **Step 1: Point the section Fetcher at the snapshot reader (router.go)**

In `internal/api/router.go`, replace the three trending-wiring lines (currently lines 883-885):

```go
		sectionFetcher.ItemRepo = itemRepo
		sectionFetcher.TMDBTrending = libraryCollectionService.TMDBCollections
		sectionFetcher.TraktTrending = libraryCollectionService.TraktCollections
```

with:

```go
		// trending_discover reads its list from the persisted snapshot table;
		// the upstream fetch happens out-of-band in the refresh task.
		sectionFetcher.TrendingSnapshots = sections.NewTrendingSnapshotRepository(deps.DB)
```

(The surrounding comment "Wire external-trending fetchers into the section fetcher..." can be updated to "Wire the trending snapshot reader into the section fetcher...".)

- [ ] **Step 2: Verify the router compiles**

Run: `go build ./internal/api/`
Expected: success. If `itemRepo` becomes unused in router.go after this change, the build will report it; in that case keep `itemRepo` only if other code uses it (search `grep -n "itemRepo" internal/api/router.go`) — it is used elsewhere (e.g. the library collection handler at ~line 888), so no removal is needed.

- [ ] **Step 3: Declare the refresher variable (main.go)**

In `cmd/silo/main.go`, find the declaration near line 1235:

```go
	var collectionSyncScheduler *catalog.CollectionSyncScheduler
```

Add directly below it:

```go
	var trendingRefresher *sections.TrendingRefresher
```

- [ ] **Step 4: Build the refresher (main.go)**

In `cmd/silo/main.go`, find where `collectionSyncScheduler` is assigned (around line 1244):

```go
		collectionSyncScheduler = catalog.NewCollectionSyncScheduler(collectionRepo, collectionService, slog.Default())
```

Add directly below it:

```go
		trendingRefresher = sections.NewTrendingRefresher(
			sectionRepo,
			sections.NewTrendingSnapshotRepository(pool),
			catalog.NewItemRepository(deps.DB),
			collectionService.TMDBCollections,
			collectionService.TraktCollections,
		)
```

(`sectionRepo` is in scope from line 1178; `collectionService` from line 1241. `collectionService.TraktCollections` may be nil — the refresher handles a nil provider by recording an "empty" attempt, so passing nil is safe.)

- [ ] **Step 5: Register the task (main.go)**

In `cmd/silo/main.go`, find the collection task registration (around line 1287-1289):

```go
		if collectionSyncScheduler != nil {
			taskMgr.Register(tasks.NewSyncCollectionsTask(collectionSyncScheduler))
		}
```

Add directly below it:

```go
		if trendingRefresher != nil {
			taskMgr.Register(tasks.NewRefreshTrendingDiscoverTask(trendingRefresher))
		}
```

- [ ] **Step 6: Verify imports and build the whole binary**

`cmd/silo/main.go` already imports `github.com/Silo-Server/silo-server/internal/sections` (used for `sectionRepo`) and `.../internal/catalog` and `.../internal/taskmanager/tasks`. No new imports needed.

Run: `go build ./...`
Expected: success (no output).

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/api/router.go cmd/silo/main.go
git add internal/api/router.go cmd/silo/main.go
git commit -m "feat: wire trending refresh task and snapshot reader"
```

---

## Task 8: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: success.

- [ ] **Step 2: Vet**

Run: `go vet ./internal/sections/... ./internal/taskmanager/... ./internal/api/... ./cmd/...`
Expected: no findings.

- [ ] **Step 3: Run the affected test packages**

Run: `go test ./internal/sections/... ./internal/taskmanager/...`
Expected: PASS.

- [ ] **Step 4: Lint**

Run: `make lint`
Expected: no new findings in the files touched by this plan. (Pre-existing findings elsewhere are out of scope.)

- [ ] **Step 5: End-to-end smoke test against dev**

Start the dev backend (this applies migration 166 via the embedded-migration path) and confirm the task runs and persists a snapshot:

Run: `make dev-backend` (in a separate shell), then once it is up:
- Trigger or wait for the `refresh_trending_discover` task (it has a startup trigger).
- Verify a row exists: `psql "$DATABASE_URL" -c "SELECT source, time_window, array_length(content_ids,1), entry_count, last_status, refreshed_at FROM trending_discover_snapshots;"`
Expected: at least one row per used `(source, window)` with `last_status` of `ok`/`empty` and a recent `refreshed_at` (for `ok`). If no `trending_discover` section is enabled, expect zero rows (and zero upstream calls) — enable one in the admin UI to exercise the path.
- Load a home page that includes the trending section and confirm it renders the same items as before.

- [ ] **Step 6: Final commit (if any formatting/cleanup remains)**

```bash
git add -A
git commit -m "chore(sections): finalize trending snapshot verification" || echo "nothing to commit"
```

---

## Self-Review

**1. Spec coverage**
- Persisted snapshot table (`trending_discover_snapshots`) → Task 1. ✓
- Reads never call upstream (snapshot read only) → Task 6 (`loadTrendingDiscoverContentIDs` reads `TrendingSnapshots.Get`; Fetcher loses all upstream fetcher fields). ✓
- Background refresh task (hourly + startup) → Task 5 (`TriggerTypeStartup` + hourly interval). ✓
- Enumerate used `(source, window)` from enabled sections; dormant feature = zero upstream calls → Task 3 + Task 4 (`ListTrendingDiscoverConfigs`, `distinctTrendingCombos`, `RunOnce` over combos). ✓
- Reliability invariant: failed/empty refresh preserves last-good `content_ids` → Task 2 (`RecordAttempt` vs `SaveSuccess`) + Task 4 (`refreshCombo` routing) + tests `TestRefresherFailurePreservesLastGood`, `TestRefresherEmptyProviderPreservesLastGood`. ✓
- Observability (refreshed_at/last_status/last_error/entry_count + task run summary) → Task 1 columns + Task 4 `TrendingRefreshResult` + Task 5 `SetResultData`. ✓
- Cap-200 over-fetch stored, read truncates to ItemLimit → Task 4 (`trendingFetchCap`) + Task 6 (`fetchTrendingDiscover` truncation retained). ✓
- Trakt day/week collapse to one row → Task 2 (`canonicalTrendingKey`) + test `TestDistinctTrendingCombosCollapsesTrakt`. ✓
- First-boot: empty until first sync, startup kick → Task 5 startup trigger; Task 6 nil/not-found returns nil → empty render. ✓
- Refresher lives in `internal/sections/` → Task 4. ✓
- Wiring (construct refresher, register task, set reader, remove old trending wiring) → Task 7. ✓

**2. Placeholder scan:** No TBD/TODO; every code step contains full code; every test step has assertions and an expected result. ✓

**3. Type consistency:**
- `canonicalTrendingKey(source, window string) (string, string)` — defined Task 2, used Tasks 2/4/6. ✓
- `TrendingSnapshot{Source, Window, ContentIDs, EntryCount, RefreshedAt, LastAttemptAt, LastStatus, LastError}` — Task 2, used Tasks 4/6 tests. ✓
- Repo methods `Get(ctx, source, window) (TrendingSnapshot, bool, error)`, `SaveSuccess(ctx, source, window, contentIDs, entryCount, status, at)`, `RecordAttempt(ctx, source, window, status, message, at)`, `ListAll(ctx)` — Task 2; interfaces `trendingSnapshotStore` (Task 4) and `trendingSnapshotGetter` (Task 6) match these signatures exactly. ✓
- `ListTrendingDiscoverConfigs(ctx) ([]json.RawMessage, error)` — Task 3; interface `trendingSectionConfigLister` (Task 4) matches. ✓
- `GetByExternalIDs(ctx, catalog.ExternalIDBatch, string) (*catalog.ExternalIDLookup, error)` — interface `trendingExternalIDResolver` (Task 4) matches `*catalog.ItemRepository`. ✓
- `TrendingRefresher.RunOnce(ctx) (json.RawMessage, error)` — Task 4; interface `TrendingDiscoverRefresher` (Task 5) matches. ✓
- `NewTrendingRefresher(lister, store, resolver, tmdb, trakt)` arg order — Task 4 definition matches Task 7 call site. ✓
```
