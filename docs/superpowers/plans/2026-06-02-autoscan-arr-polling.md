# Autoscan Arr Polling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A periodic task that polls autoscan-enabled Radarr/Sonarr instances for imported files and enqueues targeted Silo library scans for the affected folders.

**Architecture:** A new `internal/autoscan` package: a `Repository` (settings + sources, joining `request_integrations` for URL/key/kind), a `HistoryClient` (reuses `arrclient` to read `/api/v3/history/since`), pure path-rewrite/dedupe helpers, and a `Service.PollOnce` that resolves imported paths via the existing `scantrigger.Resolver` and enqueues into the existing `scanqueue`, with a Redis suppression key to avoid re-scanning a folder back-to-back. Runs as a `taskmanager.Task`.

**Tech Stack:** Go (pgx, `github.com/redis/go-redis/v9`, standard `testing`/`httptest`), PostgreSQL (paired numbered migrations), React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-06-02-autoscan-arr-polling-design.md`

**Commands assume the repository root is the cwd.** Go tests: `go test ./internal/autoscan/...`. Full lint: `make lint`. Frontend: `cd web && pnpm run lint`. Ensure the Go toolchain is on `PATH` (prepend its `bin` directory if `go` is not found). Use the project's disposable test DB for migrations; never touch a live database.

---

## Phase 0 — Branch

- [ ] **Step 0.1: Confirm feature branch**

```bash
git rev-parse --abbrev-ref HEAD   # expect: feat/autoscan-arr-polling
```
If not on it: `git checkout main && git pull && git checkout -b feat/autoscan-arr-polling`.

---

## Phase 1 — Data model

### Task 1: Migration

**Files:**
- Create: `migrations/<NNN>_autoscan.up.sql`
- Create: `migrations/<NNN>_autoscan.down.sql`

`<NNN>` is the next sequential migration number. Find it with:
```bash
ls migrations/ | grep -oE '^[0-9]+' | sort -n | tail -1
```
Use that number + 1 (zero-padded to the same width as neighbors).

- [ ] **Step 1.1: Write the up migration**

```sql
CREATE TABLE IF NOT EXISTS public.autoscan_settings (
    id boolean PRIMARY KEY DEFAULT true,
    enabled boolean NOT NULL DEFAULT false,
    poll_interval_minutes integer NOT NULL DEFAULT 10,
    debounce_seconds integer NOT NULL DEFAULT 60,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT autoscan_settings_singleton CHECK (id),
    CONSTRAINT autoscan_settings_interval_positive CHECK (poll_interval_minutes > 0),
    CONSTRAINT autoscan_settings_debounce_nonneg CHECK (debounce_seconds >= 0)
);

INSERT INTO public.autoscan_settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS public.autoscan_sources (
    integration_id text PRIMARY KEY
        REFERENCES public.request_integrations(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT false,
    path_rewrites jsonb NOT NULL DEFAULT '[]'::jsonb,
    last_poll_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
```

- [ ] **Step 1.2: Write the down migration**

```sql
DROP TABLE IF EXISTS public.autoscan_sources;
DROP TABLE IF EXISTS public.autoscan_settings;
```

- [ ] **Step 1.3: Verify on the disposable DB**

Apply all migrations (including this one) to the throwaway DB used in prior plans, then:
```bash
# (reset throwaway DB to baseline + run migrations via the project's migrate path)
psql "$CI_DATABASE_URL" -c "\d public.autoscan_sources"
psql "$CI_DATABASE_URL" -c "\d public.autoscan_settings"
```
Expected: both tables exist; `autoscan_sources.integration_id` FKs `request_integrations` with `ON DELETE CASCADE`. Also apply the `.down.sql` and confirm it drops cleanly.

- [ ] **Step 1.4: Commit**

```bash
git add migrations/<NNN>_autoscan.up.sql migrations/<NNN>_autoscan.down.sql
git commit -m "feat(autoscan): settings and sources schema"
```

### Task 2: Go types

**Files:**
- Create: `internal/autoscan/types.go`

- [ ] **Step 2.1: Write the types**

```go
package autoscan

import "time"

// Settings is the global autoscan configuration (singleton row).
type Settings struct {
	Enabled             bool      `json:"enabled"`
	PollIntervalMinutes int       `json:"poll_interval_minutes"`
	DebounceSeconds     int       `json:"debounce_seconds"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// PathRewrite is an optional prefix translation from an arr path to a Silo path.
type PathRewrite struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Source is an autoscan-enabled Radarr/Sonarr instance. Kind/BaseURL/APIKeyRef/Name
// are read from request_integrations; the rest live in autoscan_sources.
type Source struct {
	IntegrationID string        `json:"integration_id"`
	Kind          string        `json:"kind"`
	Name          string        `json:"name"`
	BaseURL       string        `json:"-"`
	APIKeyRef     string        `json:"-"`
	Enabled       bool          `json:"enabled"`
	PathRewrites  []PathRewrite `json:"path_rewrites"`
	LastPollAt    *time.Time    `json:"last_poll_at,omitempty"`
}

// SourceUpdate is the admin-editable subset of a source.
type SourceUpdate struct {
	Enabled      bool          `json:"enabled"`
	PathRewrites []PathRewrite `json:"path_rewrites"`
}
```

- [ ] **Step 2.2: Build + commit**

```bash
go build ./internal/autoscan/... && git add internal/autoscan/types.go && git commit -m "feat(autoscan): core types"
```

---

## Phase 2 — Pure helpers (path rewrite + dedupe)

### Task 3: Path rewrite

**Files:**
- Create: `internal/autoscan/rewrite.go`
- Create: `internal/autoscan/rewrite_test.go`

- [ ] **Step 3.1: Write the failing test**

```go
package autoscan

import "testing"

func TestApplyRewrites(t *testing.T) {
	rw := []PathRewrite{{From: "/data/media", To: "/mnt/media"}}
	cases := []struct{ in, want string }{
		{"/data/media/Movies/Dune/Dune.mkv", "/mnt/media/Movies/Dune/Dune.mkv"}, // prefix match
		{"/other/path/file.mkv", "/other/path/file.mkv"},                         // no match -> passthrough
	}
	for _, tc := range cases {
		if got := applyRewrites(tc.in, rw); got != tc.want {
			t.Fatalf("applyRewrites(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// first match wins
	multi := []PathRewrite{{From: "/data", To: "/A"}, {From: "/data/media", To: "/B"}}
	if got := applyRewrites("/data/media/x", multi); got != "/A/media/x" {
		t.Fatalf("first-match: got %q", got)
	}
	// empty rewrites -> passthrough
	if got := applyRewrites("/data/media/x", nil); got != "/data/media/x" {
		t.Fatalf("nil rewrites: got %q", got)
	}
}
```

- [ ] **Step 3.2: Run it — expect failure**

```bash
go test ./internal/autoscan/ -run TestApplyRewrites -v
```
Expected: FAIL (`applyRewrites` undefined).

- [ ] **Step 3.3: Implement**

`internal/autoscan/rewrite.go`:
```go
package autoscan

import "strings"

// applyRewrites returns path with the first matching prefix rewrite applied,
// or path unchanged when none match.
func applyRewrites(path string, rewrites []PathRewrite) string {
	for _, rw := range rewrites {
		from := strings.TrimSpace(rw.From)
		if from == "" {
			continue
		}
		if strings.HasPrefix(path, from) {
			return strings.TrimSpace(rw.To) + strings.TrimPrefix(path, from)
		}
	}
	return path
}
```

- [ ] **Step 3.4: Run it — expect pass; commit**

```bash
go test ./internal/autoscan/ -run TestApplyRewrites -v
git add internal/autoscan/rewrite.go internal/autoscan/rewrite_test.go
git commit -m "feat(autoscan): path rewrite helper"
```

### Task 4: Dedupe imported paths to unique parent folders

**Files:**
- Create: `internal/autoscan/dedupe.go`
- Create: `internal/autoscan/dedupe_test.go`

- [ ] **Step 4.1: Write the failing test**

```go
package autoscan

import (
	"reflect"
	"sort"
	"testing"
)

func TestUniqueParentDirs(t *testing.T) {
	in := []string{
		"/mnt/media/Show/Season 01/E01.mkv",
		"/mnt/media/Show/Season 01/E02.mkv", // same dir -> collapse
		"/mnt/media/Movie/Movie.mkv",
		"", // empty -> ignored
	}
	got := uniqueParentDirs(in)
	sort.Strings(got)
	want := []string{"/mnt/media/Movie", "/mnt/media/Show/Season 01"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueParentDirs = %v, want %v", got, want)
	}
}
```

- [ ] **Step 4.2: Run it — expect failure**

```bash
go test ./internal/autoscan/ -run TestUniqueParentDirs -v
```
Expected: FAIL (`uniqueParentDirs` undefined).

- [ ] **Step 4.3: Implement**

`internal/autoscan/dedupe.go`:
```go
package autoscan

import "path/filepath"

// uniqueParentDirs maps imported file paths to their distinct parent directories,
// dropping empties. A season's episodes in one folder collapse to one entry.
func uniqueParentDirs(paths []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		dir := filepath.Dir(p)
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	return out
}
```

- [ ] **Step 4.4: Run it — expect pass; commit**

```bash
go test ./internal/autoscan/ -run TestUniqueParentDirs -v
git add internal/autoscan/dedupe.go internal/autoscan/dedupe_test.go
git commit -m "feat(autoscan): dedupe imported paths to parent folders"
```

---

## Phase 3 — Arr history client

### Task 5: ImportedPaths from /api/v3/history/since

**Files:**
- Create: `internal/autoscan/history.go`
- Create: `internal/autoscan/history_test.go`

- [ ] **Step 5.1: Write the failing test (httptest)**

```go
package autoscan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

func TestArrHistoryImportedPaths(t *testing.T) {
	// Both Radarr and Sonarr return history records with a string eventType and
	// data.importedPath on downloadFolderImported events.
	body := `[
	  {"eventType":"downloadFolderImported","data":{"importedPath":"/mnt/media/Movies/Dune (2021)/Dune.mkv"}},
	  {"eventType":"grabbed","data":{"importedPath":"/should/be/ignored"}},
	  {"eventType":"downloadFolderImported","data":{"importedPath":"/mnt/media/Show/S01/E01.mkv"}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/history/since" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("date") == "" {
			t.Errorf("missing date param")
		}
		if r.Header.Get("X-Api-Key") != "k" {
			t.Errorf("missing api key header")
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewArrHistoryClient(nil)
	paths, err := c.ImportedPaths(context.Background(), srv.URL, "k", time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("ImportedPaths: %v", err)
	}
	sort.Strings(paths)
	want := []string{"/mnt/media/Movies/Dune (2021)/Dune.mkv", "/mnt/media/Show/S01/E01.mkv"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("ImportedPaths = %v, want %v", paths, want)
	}
}
```

> Verify the `arrclient` sets the API key as the `X-Api-Key` header (read `internal/requests/arrclient/client.go` `DoJSON`). If it uses a query param instead, adjust the test's assertion to match — do not change the client.

- [ ] **Step 5.2: Run it — expect failure**

```bash
go test ./internal/autoscan/ -run TestArrHistoryImportedPaths -v
```
Expected: FAIL (`NewArrHistoryClient` undefined).

- [ ] **Step 5.3: Implement**

`internal/autoscan/history.go`:
```go
package autoscan

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Silo-Server/silo-server/internal/requests/arrclient"
)

// HistoryClient reads recently-imported file paths from a Radarr/Sonarr instance.
type HistoryClient interface {
	ImportedPaths(ctx context.Context, baseURL, apiKey string, since time.Time) ([]string, error)
}

type historyRecord struct {
	EventType string `json:"eventType"`
	Data      struct {
		ImportedPath string `json:"importedPath"`
	} `json:"data"`
}

const importedEventType = "downloadFolderImported"

type arrHistoryClient struct {
	httpClient *http.Client
}

// NewArrHistoryClient returns a HistoryClient backed by the shared arrclient.
func NewArrHistoryClient(httpClient *http.Client) HistoryClient {
	return &arrHistoryClient{httpClient: httpClient}
}

func (c *arrHistoryClient) ImportedPaths(ctx context.Context, baseURL, apiKey string, since time.Time) ([]string, error) {
	client := arrclient.New(baseURL, apiKey, c.httpClient)
	q := url.Values{}
	q.Set("date", since.UTC().Format(time.RFC3339))
	var records []historyRecord
	if err := client.GetJSON(ctx, "/api/v3/history/since?"+q.Encode(), &records); err != nil {
		return nil, fmt.Errorf("autoscan: poll history: %w", err)
	}
	var paths []string
	for _, rec := range records {
		if rec.EventType != importedEventType {
			continue
		}
		if rec.Data.ImportedPath != "" {
			paths = append(paths, rec.Data.ImportedPath)
		}
	}
	return paths, nil
}
```

- [ ] **Step 5.4: Run it — expect pass; commit**

```bash
go test ./internal/autoscan/ -run TestArrHistoryImportedPaths -v
git add internal/autoscan/history.go internal/autoscan/history_test.go
git commit -m "feat(autoscan): arr import-history client"
```

---

## Phase 4 — Repository

### Task 6: autoscan repository

**Files:**
- Create: `internal/autoscan/repository.go`

- [ ] **Step 6.1: Implement the repository**

```go
package autoscan

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) GetSettings(ctx context.Context) (Settings, error) {
	var s Settings
	err := r.pool.QueryRow(ctx, `
		SELECT enabled, poll_interval_minutes, debounce_seconds, updated_at
		FROM autoscan_settings WHERE id = true`).
		Scan(&s.Enabled, &s.PollIntervalMinutes, &s.DebounceSeconds, &s.UpdatedAt)
	if err != nil {
		return Settings{}, fmt.Errorf("get autoscan settings: %w", err)
	}
	return s, nil
}

func (r *Repository) UpdateSettings(ctx context.Context, s Settings) (Settings, error) {
	var out Settings
	err := r.pool.QueryRow(ctx, `
		UPDATE autoscan_settings
		SET enabled = $1, poll_interval_minutes = $2, debounce_seconds = $3, updated_at = now()
		WHERE id = true
		RETURNING enabled, poll_interval_minutes, debounce_seconds, updated_at`,
		s.Enabled, s.PollIntervalMinutes, s.DebounceSeconds).
		Scan(&out.Enabled, &out.PollIntervalMinutes, &out.DebounceSeconds, &out.UpdatedAt)
	if err != nil {
		return Settings{}, fmt.Errorf("update autoscan settings: %w", err)
	}
	return out, nil
}

// sourceColumns selects an autoscan source joined with its request_integrations row.
// LEFT JOIN so a source row whose instance was deleted still surfaces (it will be
// pruned by the FK cascade in practice, but the join stays defensive).
const sourceSelect = `
	SELECT ri.id, ri.kind, ri.name, ri.base_url, ri.api_key_ref,
	       COALESCE(s.enabled, false), COALESCE(s.path_rewrites, '[]'::jsonb), s.last_poll_at
	FROM request_integrations ri
	LEFT JOIN autoscan_sources s ON s.integration_id = ri.id`

func scanSource(row interface{ Scan(...any) error }) (Source, error) {
	var src Source
	var rewritesRaw []byte
	var lastPoll *time.Time
	if err := row.Scan(&src.IntegrationID, &src.Kind, &src.Name, &src.BaseURL, &src.APIKeyRef,
		&src.Enabled, &rewritesRaw, &lastPoll); err != nil {
		return Source{}, err
	}
	if len(rewritesRaw) > 0 {
		if err := json.Unmarshal(rewritesRaw, &src.PathRewrites); err != nil {
			return Source{}, fmt.Errorf("unmarshal path_rewrites for %s: %w", src.IntegrationID, err)
		}
	}
	src.LastPollAt = lastPoll
	return src, nil
}

// ListAllSources returns every Radarr/Sonarr instance with its autoscan state
// (for the admin UI — includes disabled).
func (r *Repository) ListAllSources(ctx context.Context) ([]Source, error) {
	rows, err := r.pool.Query(ctx, sourceSelect+` ORDER BY ri.kind, ri.name`)
	if err != nil {
		return nil, fmt.Errorf("list autoscan sources: %w", err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// ListEnabledSources returns only autoscan-enabled instances whose underlying
// integration is itself enabled (the poll set).
func (r *Repository) ListEnabledSources(ctx context.Context) ([]Source, error) {
	rows, err := r.pool.Query(ctx, sourceSelect+
		` WHERE s.enabled = true AND ri.enabled = true ORDER BY ri.kind, ri.name`)
	if err != nil {
		return nil, fmt.Errorf("list enabled autoscan sources: %w", err)
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// UpsertSource sets the per-instance autoscan toggle + rewrites.
func (r *Repository) UpsertSource(ctx context.Context, integrationID string, u SourceUpdate) (*Source, error) {
	rewrites, err := json.Marshal(u.PathRewrites)
	if err != nil {
		return nil, fmt.Errorf("marshal path_rewrites: %w", err)
	}
	if u.PathRewrites == nil {
		rewrites = []byte("[]")
	}
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO autoscan_sources (integration_id, enabled, path_rewrites, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (integration_id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			path_rewrites = EXCLUDED.path_rewrites,
			updated_at = now()`,
		integrationID, u.Enabled, rewrites); err != nil {
		return nil, fmt.Errorf("upsert autoscan source: %w", err)
	}
	row := r.pool.QueryRow(ctx, sourceSelect+` WHERE ri.id = $1`, integrationID)
	src, err := scanSource(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("integration not found: %s", integrationID)
		}
		return nil, err
	}
	return &src, nil
}

// AdvanceLastPoll sets last_poll_at for a source (creating the row if needed).
func (r *Repository) AdvanceLastPoll(ctx context.Context, integrationID string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO autoscan_sources (integration_id, last_poll_at, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (integration_id) DO UPDATE SET last_poll_at = $2, updated_at = now()`,
		integrationID, at)
	if err != nil {
		return fmt.Errorf("advance autoscan last_poll: %w", err)
	}
	return nil
}
```

- [ ] **Step 6.2: Build + commit**

```bash
go build ./internal/autoscan/...
git add internal/autoscan/repository.go
git commit -m "feat(autoscan): settings + sources repository"
```

> The repository is exercised end-to-end by the `PollOnce` service tests (Task 8) via a fake store, and by the migration check (Task 1). A DB-backed repo test is optional; if the project has a Postgres test harness, add `repository_test.go` mirroring the requests repo tests.

---

## Phase 5 — Service

### Task 7: Suppression (Redis) seam

**Files:**
- Create: `internal/autoscan/suppress.go`

- [ ] **Step 7.1: Implement the suppressor**

```go
package autoscan

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Suppressor prevents re-enqueuing a scan for the same folder within a window.
type Suppressor interface {
	// ShouldScan atomically claims the folder for scanning: returns true and sets
	// a TTL key if no claim exists, false if a recent claim is still live.
	ShouldScan(ctx context.Context, folderID int, ttl time.Duration) (bool, error)
}

type redisSuppressor struct{ client *redis.Client }

func NewRedisSuppressor(client *redis.Client) Suppressor { return &redisSuppressor{client: client} }

func (s *redisSuppressor) ShouldScan(ctx context.Context, folderID int, ttl time.Duration) (bool, error) {
	if s.client == nil || ttl <= 0 {
		return true, nil // no suppression configured -> always scan
	}
	key := fmt.Sprintf("autoscan:scanned:%d", folderID)
	ok, err := s.client.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return true, nil // fail open: a Redis hiccup should not block scanning
	}
	return ok, nil
}
```

- [ ] **Step 7.2: Build + commit**

```bash
go build ./internal/autoscan/...
git add internal/autoscan/suppress.go
git commit -m "feat(autoscan): redis scan-suppression seam"
```

### Task 8: Service.PollOnce

**Files:**
- Create: `internal/autoscan/service.go`
- Create: `internal/autoscan/service_test.go`

- [ ] **Step 8.1: Write the failing test**

```go
package autoscan

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

type fakeStore struct {
	settings  Settings
	sources   []Source
	advanced  map[string]time.Time
}

func (f *fakeStore) GetSettings(context.Context) (Settings, error)        { return f.settings, nil }
func (f *fakeStore) ListEnabledSources(context.Context) ([]Source, error) { return f.sources, nil }
func (f *fakeStore) AdvanceLastPoll(_ context.Context, id string, at time.Time) error {
	if f.advanced == nil {
		f.advanced = map[string]time.Time{}
	}
	f.advanced[id] = at
	return nil
}

type fakeHistory struct {
	paths map[string][]string // baseURL -> imported paths
	err   error
}

func (f *fakeHistory) ImportedPaths(_ context.Context, baseURL, _ string, _ time.Time) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.paths[baseURL], nil
}

type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	// Resolve any /mnt/media path to folder 7; anything else is unresolvable.
	if len(req.Path) >= 11 && req.Path[:11] == "/mnt/media/" {
		return &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: req.Path, Trigger: req.Trigger}, nil
	}
	return nil, nil
}

type recordingQueuer struct{ enqueued []scantrigger.Target }

func (q *recordingQueuer) EnqueueScans(_ context.Context, targets []scantrigger.Target) error {
	q.enqueued = append(q.enqueued, targets...)
	return nil
}

type allowSuppressor struct{}

func (allowSuppressor) ShouldScan(context.Context, int, time.Duration) (bool, error) { return true, nil }

func TestPollOnceEnqueuesDedupedFolders(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, PollIntervalMinutes: 10, DebounceSeconds: 60},
		sources: []Source{{
			IntegrationID: "i1", Kind: "sonarr", BaseURL: "http://sonarr", APIKeyRef: "k", Enabled: true,
		}},
	}
	hist := &fakeHistory{paths: map[string][]string{
		"http://sonarr": {
			"/mnt/media/Show/S01/E01.mkv",
			"/mnt/media/Show/S01/E02.mkv", // same folder -> dedup
			"/outside/lib/x.mkv",          // unresolvable -> skipped
		},
	}}
	q := &recordingQueuer{}
	svc := NewService(store, hist, fakeResolver{}, q, allowSuppressor{}, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("expected 1 deduped folder scan, got %d: %+v", len(q.enqueued), q.enqueued)
	}
	if q.enqueued[0].Trigger != "autoscan" || q.enqueued[0].Folder.ID != 7 {
		t.Fatalf("unexpected target: %+v", q.enqueued[0])
	}
	if _, ok := store.advanced["i1"]; !ok {
		t.Fatalf("expected last_poll advanced for i1")
	}
}

func TestPollOnceDisabledNoop(t *testing.T) {
	store := &fakeStore{settings: Settings{Enabled: false}}
	q := &recordingQueuer{}
	svc := NewService(store, &fakeHistory{}, fakeResolver{}, q, allowSuppressor{}, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("disabled autoscan should enqueue nothing, got %d", len(q.enqueued))
	}
}
```

- [ ] **Step 8.2: Run it — expect failure**

```bash
go test ./internal/autoscan/ -run TestPollOnce -v
```
Expected: FAIL (`NewService` undefined).

- [ ] **Step 8.3: Implement**

`internal/autoscan/service.go`:
```go
package autoscan

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

const scanTrigger = "autoscan"

// Store is the persistence the service needs.
type Store interface {
	GetSettings(ctx context.Context) (Settings, error)
	ListEnabledSources(ctx context.Context) ([]Source, error)
	AdvanceLastPoll(ctx context.Context, integrationID string, at time.Time) error
}

// Resolver maps a filesystem path to a Silo scan target.
type Resolver interface {
	Resolve(ctx context.Context, req scantrigger.Request) (*scantrigger.Target, error)
}

// Queuer enqueues resolved scan targets.
type Queuer interface {
	EnqueueScans(ctx context.Context, targets []scantrigger.Target) error
}

// SecretResolver decrypts an api_key_ref into a usable key.
type SecretResolver interface {
	Get(ctx context.Context, key string) (string, error)
}

type Service struct {
	store    Store
	history  HistoryClient
	resolver Resolver
	queue    Queuer
	suppress Suppressor
	secrets  SecretResolver
	now      func() time.Time
}

func NewService(store Store, history HistoryClient, resolver Resolver, queue Queuer, suppress Suppressor, secrets SecretResolver) *Service {
	return &Service{
		store: store, history: history, resolver: resolver, queue: queue,
		suppress: suppress, secrets: secrets,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// PollOnce runs one autoscan cycle. Per-source failures are logged and skipped;
// only the overall settings/listing errors propagate.
func (s *Service) PollOnce(ctx context.Context) error {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.Enabled {
		return nil
	}
	sources, err := s.store.ListEnabledSources(ctx)
	if err != nil {
		return err
	}
	ttl := time.Duration(settings.DebounceSeconds) * time.Second

	for _, src := range sources {
		cycleStart := s.now()
		// First enable: last_poll_at null -> floor at cycleStart (don't replay history).
		since := cycleStart
		if src.LastPollAt != nil {
			since = *src.LastPollAt
		}

		apiKey := src.APIKeyRef
		if s.secrets != nil && apiKey != "" {
			if resolved, rerr := s.secrets.Get(ctx, apiKey); rerr == nil && resolved != "" {
				apiKey = resolved
			}
		}

		paths, perr := s.history.ImportedPaths(ctx, src.BaseURL, apiKey, since)
		if perr != nil {
			slog.WarnContext(ctx, "autoscan: source poll failed", "integration_id", src.IntegrationID, "err", perr)
			continue // do not advance last_poll -> retry window next cycle
		}

		// rewrite -> dedupe -> resolve -> suppress -> enqueue
		rewritten := make([]string, 0, len(paths))
		for _, p := range paths {
			rewritten = append(rewritten, applyRewrites(p, src.PathRewrites))
		}
		var targets []scantrigger.Target
		for _, dir := range uniqueParentDirs(rewritten) {
			target, rerr := s.resolver.Resolve(ctx, scantrigger.Request{Path: dir, Trigger: scanTrigger})
			if rerr != nil {
				slog.WarnContext(ctx, "autoscan: resolve failed", "path", dir, "err", rerr)
				continue
			}
			if target == nil || target.Folder == nil {
				continue // outside Silo's media folders
			}
			ok, serr := s.suppress.ShouldScan(ctx, target.Folder.ID, ttl)
			if serr != nil || !ok {
				continue
			}
			target.Trigger = scanTrigger
			targets = append(targets, *target)
		}
		if len(targets) > 0 {
			if eerr := s.queue.EnqueueScans(ctx, targets); eerr != nil {
				slog.WarnContext(ctx, "autoscan: enqueue failed", "integration_id", src.IntegrationID, "err", eerr)
				continue // do not advance -> retry
			}
		}
		if aerr := s.store.AdvanceLastPoll(ctx, src.IntegrationID, cycleStart); aerr != nil {
			slog.WarnContext(ctx, "autoscan: advance last_poll failed", "integration_id", src.IntegrationID, "err", aerr)
		}
	}
	return nil
}
```

- [ ] **Step 8.4: Run tests — expect pass**

```bash
go test ./internal/autoscan/... -v
```
Expected: all PASS. (`Service.PollOnce`, rewrite, dedupe, history.)

- [ ] **Step 8.5: Commit**

```bash
git add internal/autoscan/service.go internal/autoscan/service_test.go
git commit -m "feat(autoscan): PollOnce poll cycle"
```

---

## Phase 6 — Task + wiring

### Task 9: Poll task + main.go wiring

**Files:**
- Create: `internal/taskmanager/tasks/autoscan_poll.go`
- Modify: `cmd/silo/main.go` (construct the service + register the task)
- Modify: `internal/api/router.go` (if the service/handler is built there instead)

- [ ] **Step 9.1: Implement the task** (mirror `reconcile_requests.go`)

```go
package tasks

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type AutoscanPoller interface {
	PollOnce(ctx context.Context) error
	PollIntervalMinutes(ctx context.Context) int
}

type AutoscanPollTask struct {
	poller          AutoscanPoller
	intervalMinutes int
}

func NewAutoscanPollTask(poller AutoscanPoller, intervalMinutes int) *AutoscanPollTask {
	if intervalMinutes <= 0 {
		intervalMinutes = 10
	}
	return &AutoscanPollTask{poller: poller, intervalMinutes: intervalMinutes}
}

func (t *AutoscanPollTask) Key() string  { return "autoscan_poll" }
func (t *AutoscanPollTask) Name() string { return "Autoscan Poll" }
func (t *AutoscanPollTask) Description() string {
	return "Polls autoscan-enabled Radarr/Sonarr instances for imported files and scans the affected folders"
}
func (t *AutoscanPollTask) Category() taskmanager.TaskCategory { return taskmanager.TaskCategoryLibrary }
func (t *AutoscanPollTask) IsHidden() bool                     { return false }

func (t *AutoscanPollTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(t.intervalMinutes) * 60 * 1000},
	}
}

func (t *AutoscanPollTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Polling arr instances")
	if t.poller == nil {
		progress.Report(100, "Autoscan unavailable")
		return nil
	}
	if err := t.poller.PollOnce(ctx); err != nil {
		return fmt.Errorf("autoscan poll: %w", err)
	}
	progress.Report(100, "Autoscan poll complete")
	return nil
}
```

> Add a `PollIntervalMinutes(ctx)` helper to the autoscan `Service` (reads `GetSettings`, falls back to 10) so `main.go` can seed `DefaultTriggers`. Keep it tolerant of errors (return 10 on failure).

- [ ] **Step 9.2: Construct the service and register the task in `main.go`**

Find where `libraryScanQueue` (`deps.LibraryScanQueue`), `deps.FolderRepo`, `deps.RedisClient`, `settingsRepo`, and `taskMgr.Register(...)` are in scope (around the other `taskMgr.Register` calls). Build:

```go
autoscanResolver := scantrigger.NewResolver(deps.FolderRepo) // FolderRepo satisfies scantrigger.FolderRepository
autoscanSvc := autoscan.NewService(
	autoscan.NewRepository(deps.DB),
	autoscan.NewArrHistoryClient(nil),
	autoscanResolver,
	deps.LibraryScanQueue, // *scanqueue.Service satisfies autoscan.Queuer (EnqueueScans)
	autoscan.NewRedisSuppressor(deps.RedisClient),
	settingsRepo,          // ServerSettingsRepo satisfies autoscan.SecretResolver (Get)
)
taskMgr.Register(tasks.NewAutoscanPollTask(autoscanSvc, autoscanSvc.PollIntervalMinutes(ctx)))
```

Guard each dependency for nil exactly like neighboring registrations (e.g. only register when `deps.FolderRepo != nil && deps.LibraryScanQueue != nil`). If `scantrigger.NewResolver` needs a concrete repo type, pass `deps.FolderRepo` (it already satisfies `GetByID`/`List`). Confirm `*scanqueue.Service` has `EnqueueScans(ctx, []scantrigger.Target) error` (it does) so it satisfies `autoscan.Queuer`.

- [ ] **Step 9.3: Build the whole server**

```bash
go build ./...   # requires libvips locally; if unavailable, build ./internal/... ./cmd/... minus the bimg-dependent packages, and rely on CI/Docker for the full build
go vet ./internal/autoscan/... ./internal/taskmanager/...
```
Expected: autoscan + taskmanager packages build/vet clean.

- [ ] **Step 9.4: Commit**

```bash
git add internal/taskmanager/tasks/autoscan_poll.go cmd/silo/main.go
git commit -m "feat(autoscan): poll task and wiring"
```

---

## Phase 7 — Admin API

### Task 10: Autoscan handlers + routes

**Files:**
- Create: `internal/api/handlers/autoscan.go`
- Create: `internal/api/handlers/autoscan_test.go`
- Modify: `internal/api/router.go` (mount routes)

- [ ] **Step 10.1: Service methods the handler needs**

Add to `internal/autoscan/service.go` (admin-facing, thin wrappers over the repo; the service already holds the store as the narrow `Store` interface — widen the concrete service to also expose these by holding the `*Repository` or adding methods to `Store`). Simplest: have the handler take the `*autoscan.Repository` directly for reads/writes and the `*autoscan.Service` for `PollOnce`. Implement handler methods:
- `GET /autoscan/settings` → `repo.GetSettings`
- `PUT /autoscan/settings` → validate (`poll_interval_minutes > 0`, `debounce_seconds >= 0`) → `repo.UpdateSettings`
- `GET /autoscan/sources` → `repo.ListAllSources` (map to a response that omits `BaseURL`/`APIKeyRef` — they have `json:"-"` already, so the `Source` struct is safe to return directly)
- `PUT /autoscan/sources/{id}` → decode `SourceUpdate` → `repo.UpsertSource`
- `POST /autoscan/trigger` → `service.PollOnce` (run in a short-lived goroutine or inline; return 202)
- `GET /autoscan/status` → `{enabled, sources:[{integration_id, name, last_poll_at}]}` from `repo.GetSettings` + `repo.ListAllSources`

Follow the existing admin handler style in `internal/api/handlers/requests.go` (viewer/admin extraction, `writeJSON`, error mapping helper, chi `URLParam`).

- [ ] **Step 10.2: Mount routes in `router.go`**

Near the request-integration routes (admin group), add:
```go
autoscanHandler := handlers.NewAutoscanHandler(autoscanRepo, autoscanSvc)
r.Route("/autoscan", func(r chi.Router) {
	r.Get("/settings", autoscanHandler.HandleGetSettings)
	r.Put("/settings", autoscanHandler.HandleUpdateSettings)
	r.Get("/sources", autoscanHandler.HandleListSources)
	r.Put("/sources/{id}", autoscanHandler.HandleUpsertSource)
	r.Post("/trigger", autoscanHandler.HandleTrigger)
	r.Get("/status", autoscanHandler.HandleStatus)
})
```
Apply the same admin auth + a rate limiter on `/trigger` (tight per-admin cap) consistent with other admin mutation routes.

- [ ] **Step 10.3: Handler test**

Add `autoscan_test.go` with a fake repo/service asserting: GET settings returns JSON; PUT validates a non-positive interval (400); `/sources` response never contains `base_url`/`api_key_ref`; `/trigger` invokes `PollOnce`. Mirror `requests_test.go`'s fake-service pattern.

- [ ] **Step 10.4: Build (handlers pkg needs libvips — verify in Docker/CI), gofmt, commit**

```bash
gofmt -l internal/api/handlers/autoscan.go internal/api/router.go
git add internal/api/handlers/autoscan.go internal/api/handlers/autoscan_test.go internal/api/router.go
git commit -m "feat(autoscan): admin API endpoints"
```

---

## Phase 8 — Frontend

> Follow existing patterns in `web/src/pages/AdminRequests.tsx`, `web/src/api/types.ts`, and `web/src/hooks/queries/useRequests.ts`. Each task ends with `cd web && pnpm exec tsc -b` (the REAL typecheck — `tsc --noEmit` checks nothing), `pnpm run lint`, `pnpm exec prettier --check <files>`.

### Task 11: Types + hooks

**Files:**
- Modify: `web/src/api/types.ts`
- Create: `web/src/hooks/queries/useAutoscan.ts`

- [ ] **Step 11.1: Types**

Add to `types.ts`:
```ts
export interface AutoscanSettings {
  enabled: boolean;
  poll_interval_minutes: number;
  debounce_seconds: number;
  updated_at?: string;
}
export interface AutoscanPathRewrite {
  from: string;
  to: string;
}
export interface AutoscanSource {
  integration_id: string;
  kind: string;
  name: string;
  enabled: boolean;
  path_rewrites: AutoscanPathRewrite[];
  last_poll_at?: string | null;
}
```

- [ ] **Step 11.2: Hooks**

`useAutoscan.ts`: `useAutoscanSettings()` (GET `/admin/autoscan/settings`), `useUpdateAutoscanSettings()` (PUT), `useAutoscanSources()` (GET `/admin/autoscan/sources`), `useUpdateAutoscanSource()` (PUT `/admin/autoscan/sources/{id}`), `useTriggerAutoscan()` (POST `/admin/autoscan/trigger`). Match the mutation/query/invalidation/toast conventions in `useRequests.ts`.

- [ ] **Step 11.3: Lint + commit**

```bash
cd web && pnpm exec tsc -b && pnpm run lint && pnpm exec prettier --check src/api/types.ts src/hooks/queries/useAutoscan.ts
git add web/src/api/types.ts web/src/hooks/queries/useAutoscan.ts
git commit -m "feat(web): autoscan types and hooks"
```

### Task 12: Autoscan tab

**Files:**
- Modify: `web/src/pages/AdminRequests.tsx`

- [ ] **Step 12.1: Add the tab**

Add `"autoscan"` to `ADMIN_REQUEST_TABS`, a `<TabsTrigger value="autoscan">Autoscan</TabsTrigger>`, and a `<TabsContent value="autoscan"><AutoscanTab /></TabsContent>`. Implement `AutoscanTab`:
- Global settings card: enable `SwitchField`, poll-interval `Input` (number, min 1), debounce-seconds `Input` (number, min 0), Save button (`useUpdateAutoscanSettings`).
- Per-source list (`useAutoscanSources`): one row per instance — name + kind badge, an autoscan `SwitchField`, a collapsible path-rewrite editor (add/remove `from → to` rows), read-only "Last polled" (`last_poll_at`), and a Save button (`useUpdateAutoscanSource`).
- A "Poll now" button (`useTriggerAutoscan`) with a success toast.

Reuse `Field`, `SwitchField`, `Input`, `Button`, `Badge` already in the file.

- [ ] **Step 12.2: Lint + commit**

```bash
cd web && pnpm exec tsc -b && pnpm run lint && pnpm exec prettier --check src/pages/AdminRequests.tsx
git add web/src/pages/AdminRequests.tsx
git commit -m "feat(web): autoscan admin tab"
```

---

## Phase 9 — Verification

### Task 13: Full verification

- [ ] **Step 13.1: Go**

```bash
go test ./internal/autoscan/... ./internal/taskmanager/... 2>&1 | tail
go vet ./internal/autoscan/...
gofmt -l internal/autoscan internal/taskmanager/tasks/autoscan_poll.go
```
Expected: all tests pass; vet + gofmt clean.

- [ ] **Step 13.2: Frontend**

```bash
cd web && pnpm exec tsc -b && pnpm run lint && pnpm run format:check
```

- [ ] **Step 13.3: Docker build (closes the libvips gap)**

```bash
docker build --build-arg BUILD_REVISION=$(git rev-parse HEAD) --build-arg BUILD_DIRTY=false -t silo-server:autoscan .
```
Expected: exit 0 (proves the handlers package + full vite build compile).

- [ ] **Step 13.4: Smoke (optional, local deploy)**

Enable autoscan in the Autoscan tab, toggle a Radarr/Sonarr source on, import something in that arr (or "Poll now"), and confirm a scan with `trigger=autoscan` appears in the scan history and the folder is scanned.

- [ ] **Step 13.5: `make verify-local-paths`**

```bash
make verify-local-paths
```

---

## Self-review notes (resolved)

- **Spec §1 data model** → Tasks 1–2, 6. **§2 poll cycle** → Task 8. **§3 path resolution** → Tasks 3, 4, 8 (reuses `scantrigger`/`scanqueue` unchanged). **§4 scheduling** → Task 9. **§5 API/UI** → Tasks 10–12. **§6 testing** → Tasks 3,4,5,8,10,11,12 + Task 13.
- **First-enable floor** (spec §2.3.3): `since = cycleStart` when `LastPollAt == nil` (Task 8.3) — does not replay history.
- **Reuse, not duplicate**: history client uses `arrclient`; resolution uses `scantrigger.Resolver`; enqueue uses `scanqueue` (`autoscan.Queuer` is satisfied by `*scanqueue.Service`). Credentials come from `request_integrations` via the join (Task 6) + the shared `SecretResolver` (Task 8).
- **No fan-out guard / retry queue** by design — per-source failure skips advancing `last_poll_at` so the window retries (Task 8.3).
- **Type consistency**: `Source`, `Settings`, `SourceUpdate`, `PathRewrite`, `HistoryClient.ImportedPaths`, `Suppressor.ShouldScan`, `Service.PollOnce`, `Store`/`Resolver`/`Queuer`/`SecretResolver` interfaces are used consistently across Tasks 2–10.
