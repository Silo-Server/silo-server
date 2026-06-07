# TheIntroDB read-path correctness (Phase 1 / Layer A) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the three read-path correctness gaps in the TheIntroDB provider so it matches the
real v3 API contract: (1) honor **TVDB** ids in lookups (today they are silently dropped),
(2) capture and use the **real per-segment `confidence`** instead of a hardcoded `0.9`, and
(3) when the API returns multiple candidate segments, **pick the most-submitted / highest-confidence**
one instead of the first usable. This is Phase 1 of
[the marker sources & contribution design](../specs/2026-06-06-marker-sources-and-contribution-design.md);
it stands alone, ships value immediately (anime / TheTVDB-first libraries start getting markers),
and unblocks the "query-all / best-wins" merge in later phases.

**Architecture:** All changes are contained within `internal/markers/introdb` plus one added
field on `markers.Marker`. The resolver already populates the TVDB id into
`Request.ExternalIDs` — only the provider and HTTP client drop it. The HTTP `Client` gains a
`tvdbID` argument on `FetchEpisode`/`FetchMovie` (query preference `tmdb → tvdb → imdb`, cache
keys extended); the provider reads `ExternalIDKeyTVDB`; `segmentTimestamps` gains `confidence`
and `submission_count`; `pickMarker` uses real confidence (with a named default) and ranks
candidates. The write path (`BuildUpdatePayload` / `UpsertMarkers`) is **not** touched here.

**Out of scope (deferred to Phase 2):** threading *per-segment* confidence through
`MarkerUpdatePayload` and `UpsertMarkers`. That refactor only becomes observable once
`Registry.FetchMerged` can combine segments from different providers; with a single provider
today the existing "max confidence across segments" collapse is equivalent, and changing it now
would break `TestBuildUpdatePayloadAggregatesConfidence` for no functional gain. It lands with
the merge dispatch in Phase 2.

**Tech Stack:** Go (the `internal/markers/introdb` package is pure Go — no libvips/CGO),
PostgreSQL is not involved (no migration), no frontend changes. Tests run in a throwaway Go
container (the host has no Go toolchain); a named volume caches module downloads.

Commands assume the repository root is the cwd.

---

## Running tests (host has no Go toolchain)

`internal/markers` and `internal/markers/introdb` are pure Go, so no libvips is needed:

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/markers/... -v
```

To run a single test, append `-run TestName` and narrow the package path, e.g.
`go test ./internal/markers/introdb/ -run TestFetchEpisodeSendsTVDBWhenNoTMDB -v`.

---

## File structure

- `internal/markers/introdb/types.go` — **modify**. Add `Confidence` + `SubmissionCount` to `segmentTimestamps`; add the `defaultConfidence` const.
- `internal/markers/types.go` — **modify**. Add `SubmissionCount` to `markers.Marker`.
- `internal/markers/introdb/client.go` — **modify**. `tvdbID` arg on `FetchEpisode`/`FetchMovie`; query preference `tmdb → tvdb → imdb`; cache keys include tvdb.
- `internal/markers/introdb/provider.go` — **modify**. Read `ExternalIDKeyTVDB` and pass it through; rewrite `pickMarker` for real confidence + best-candidate.
- `internal/markers/introdb/client_test.go` — **create**. httptest-based client tests (TVDB query, TMDB preference, caching).
- `internal/markers/introdb/provider_test.go` — **create**. End-to-end provider tests (TVDB-only resolves, real/default confidence, best-candidate).

No migration, no wiring change (`cmd/silo/main.go` constructs the provider but never calls the
client directly), no frontend change.

---

## Task 1: Type plumbing — capture confidence + submission_count

**Files:**
- Modify: `internal/markers/introdb/types.go`
- Modify: `internal/markers/types.go`

This task is pure struct plumbing (no behavior yet); later tasks' tests exercise it. Verify by
compilation.

- [ ] **Step 1: Add the response fields + default constant in `introdb/types.go`**

Replace the `Algorithm` const block to add `defaultConfidence` after it:

```go
// Algorithm is the algorithm tag written alongside markers. The version
// suffix lets us invalidate or refresh markers if the upstream contract
// changes.
const Algorithm = "introdb:v3"

// defaultConfidence is applied when TheIntroDB omits a per-segment confidence
// in the /media response. Real per-segment confidence is preferred when present.
const defaultConfidence = 0.9
```

Replace the `segmentTimestamps` struct with the version that decodes the two extra fields the
v3 `/media` response carries:

```go
// segmentTimestamps is the per-occurrence shape returned by TheIntroDB.
// Either bound may be nil — for intro/recap, start may be omitted (segment
// begins at file start); for credits/preview, end may be omitted (segment
// runs to file end). Confidence and SubmissionCount are optional per-segment
// quality signals used to pick among multiple candidates.
type segmentTimestamps struct {
	StartMs         *int64   `json:"start_ms,omitempty"`
	EndMs           *int64   `json:"end_ms,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	SubmissionCount *int     `json:"submission_count,omitempty"`
}
```

- [ ] **Step 2: Add `SubmissionCount` to `markers.Marker`**

In `internal/markers/types.go`, replace the `Marker` struct:

```go
type Marker struct {
	Kind            MarkerKind
	Start           time.Duration
	End             time.Duration
	Confidence      float64
	SubmissionCount int
}
```

- [ ] **Step 3: Verify the packages still compile**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go build ./internal/markers/...
```

Expected: no output, exit 0. (Existing `write_test.go` / `types_test.go` use field-keyed
literals, so the added field does not break them.)

- [ ] **Step 4: Commit**

```bash
git add internal/markers/introdb/types.go internal/markers/types.go
git commit -m "feat(markers): decode introdb per-segment confidence + submission_count"
```

---

## Task 2: TVDB lookups (gap #1)

**Files:**
- Modify: `internal/markers/introdb/client.go`
- Modify: `internal/markers/introdb/provider.go`
- Create: `internal/markers/introdb/client_test.go`

- [ ] **Step 1: Write the failing client tests**

Create `internal/markers/introdb/client_test.go`:

```go
package introdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestFetchEpisodeSendsTVDBWhenNoTMDB(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"type":"episode"}`))
	}))
	defer srv.Close()

	c := NewClient("")
	c.SetBaseURL(srv.URL)
	if _, err := c.FetchEpisode(context.Background(), "", "55555", "tt1234567", 2, 3, 0); err != nil {
		t.Fatalf("FetchEpisode: %v", err)
	}
	if gotQuery.Get("tvdb_id") != "55555" {
		t.Errorf("tvdb_id = %q, want 55555", gotQuery.Get("tvdb_id"))
	}
	if gotQuery.Get("tmdb_id") != "" {
		t.Errorf("tmdb_id = %q, want empty", gotQuery.Get("tmdb_id"))
	}
	if gotQuery.Get("imdb_id") != "" {
		t.Errorf("imdb_id should be omitted when tvdb present, got %q", gotQuery.Get("imdb_id"))
	}
}

func TestFetchEpisodePrefersTMDBOverTVDBAndIMDB(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"type":"episode"}`))
	}))
	defer srv.Close()

	c := NewClient("")
	c.SetBaseURL(srv.URL)
	if _, err := c.FetchEpisode(context.Background(), "111", "222", "tt333", 1, 1, 0); err != nil {
		t.Fatalf("FetchEpisode: %v", err)
	}
	if gotQuery.Get("tmdb_id") != "111" {
		t.Errorf("tmdb_id = %q, want 111", gotQuery.Get("tmdb_id"))
	}
	if gotQuery.Get("tvdb_id") != "" || gotQuery.Get("imdb_id") != "" {
		t.Errorf("only tmdb_id expected, got tvdb=%q imdb=%q", gotQuery.Get("tvdb_id"), gotQuery.Get("imdb_id"))
	}
}

func TestFetchMovieSendsTVDB(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = w.Write([]byte(`{"type":"movie"}`))
	}))
	defer srv.Close()

	c := NewClient("")
	c.SetBaseURL(srv.URL)
	if _, err := c.FetchMovie(context.Background(), "", "888", "", 0); err != nil {
		t.Fatalf("FetchMovie: %v", err)
	}
	if gotQuery.Get("tvdb_id") != "888" {
		t.Errorf("tvdb_id = %q, want 888", gotQuery.Get("tvdb_id"))
	}
}

func TestFetchEpisodeCachesByID(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"type":"episode"}`))
	}))
	defer srv.Close()

	c := NewClient("")
	c.SetBaseURL(srv.URL)
	for i := 0; i < 3; i++ {
		if _, err := c.FetchEpisode(context.Background(), "", "999", "", 1, 1, 0); err != nil {
			t.Fatalf("FetchEpisode: %v", err)
		}
	}
	if hits != 1 {
		t.Fatalf("server hits = %d, want 1 (cached after first)", hits)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/markers/introdb/ -run 'TestFetch' -v
```

Expected: FAIL — compile error (`FetchEpisode`/`FetchMovie` still take the old 6/4-arg
signatures without `tvdbID`).

- [ ] **Step 3: Add `tvdbID` to `FetchEpisode` in `client.go`**

Replace the whole `FetchEpisode` method:

```go
// FetchEpisode looks up segment timestamps for a TV episode.
// At least one of tmdbID, tvdbID, or imdbID must be non-empty.
func (c *Client) FetchEpisode(ctx context.Context, tmdbID, tvdbID, imdbID string, season, episode int, durationMS int64) (*mediaResponse, error) {
	if tmdbID == "" && tvdbID == "" && imdbID == "" {
		return nil, fmt.Errorf("introdb: tmdb_id, tvdb_id, or imdb_id required")
	}
	if season <= 0 || episode <= 0 {
		return nil, fmt.Errorf("introdb: episode lookup requires season and episode > 0 (got %d/%d)", season, episode)
	}
	q := url.Values{}
	switch {
	case tmdbID != "":
		q.Set("tmdb_id", tmdbID)
	case tvdbID != "":
		q.Set("tvdb_id", tvdbID)
	default:
		q.Set("imdb_id", imdbID)
	}
	q.Set("season", strconv.Itoa(season))
	q.Set("episode", strconv.Itoa(episode))
	if durationMS > 0 {
		q.Set("duration_ms", strconv.FormatInt(durationMS, 10))
	}
	return c.fetch(ctx, q, cacheKeyEpisode(tmdbID, tvdbID, imdbID, season, episode, durationMS))
}
```

- [ ] **Step 4: Add `tvdbID` to `FetchMovie` in `client.go`**

Replace the whole `FetchMovie` method:

```go
// FetchMovie looks up segment timestamps for a movie.
// At least one of tmdbID, tvdbID, or imdbID must be non-empty.
func (c *Client) FetchMovie(ctx context.Context, tmdbID, tvdbID, imdbID string, durationMS int64) (*mediaResponse, error) {
	if tmdbID == "" && tvdbID == "" && imdbID == "" {
		return nil, fmt.Errorf("introdb: tmdb_id, tvdb_id, or imdb_id required")
	}
	q := url.Values{}
	switch {
	case tmdbID != "":
		q.Set("tmdb_id", tmdbID)
	case tvdbID != "":
		q.Set("tvdb_id", tvdbID)
	default:
		q.Set("imdb_id", imdbID)
	}
	if durationMS > 0 {
		q.Set("duration_ms", strconv.FormatInt(durationMS, 10))
	}
	return c.fetch(ctx, q, cacheKeyMovie(tmdbID, tvdbID, imdbID, durationMS))
}
```

- [ ] **Step 5: Extend the cache-key helpers in `client.go`**

Replace both `cacheKeyEpisode` and `cacheKeyMovie`:

```go
func cacheKeyEpisode(tmdbID, tvdbID, imdbID string, season, episode int, durationMS int64) string {
	switch {
	case tmdbID != "":
		return fmt.Sprintf("tmdb:%s:s%de%d:d%d", tmdbID, season, episode, durationMS)
	case tvdbID != "":
		return fmt.Sprintf("tvdb:%s:s%de%d:d%d", tvdbID, season, episode, durationMS)
	default:
		return fmt.Sprintf("imdb:%s:s%de%d:d%d", imdbID, season, episode, durationMS)
	}
}

func cacheKeyMovie(tmdbID, tvdbID, imdbID string, durationMS int64) string {
	switch {
	case tmdbID != "":
		return fmt.Sprintf("tmdb:movie:%s:d%d", tmdbID, durationMS)
	case tvdbID != "":
		return fmt.Sprintf("tvdb:movie:%s:d%d", tvdbID, durationMS)
	default:
		return fmt.Sprintf("imdb:movie:%s:d%d", imdbID, durationMS)
	}
}
```

- [ ] **Step 6: Read TVDB in the provider and pass it through (`provider.go`)**

Replace the id-extraction block in `FetchMarkers` (the `tmdbID`/`imdbID` lines and the empty
guard):

```go
	tmdbID := strings.TrimSpace(req.ExternalIDs[markers.ExternalIDKeyTMDB])
	tvdbID := strings.TrimSpace(req.ExternalIDs[markers.ExternalIDKeyTVDB])
	imdbID := strings.TrimSpace(req.ExternalIDs[markers.ExternalIDKeyIMDB])
	if tmdbID == "" && tvdbID == "" && imdbID == "" {
		return markers.Result{}, nil
	}
```

Then update the two client calls in the `switch req.Kind` block to pass `tvdbID`:

```go
		resp, err = p.client.FetchEpisode(ctx, tmdbID, tvdbID, imdbID, req.SeasonNumber, req.EpisodeNumber, durationMS)
```
```go
		resp, err = p.client.FetchMovie(ctx, tmdbID, tvdbID, imdbID, durationMS)
```

- [ ] **Step 7: Run the client tests to verify they pass**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/markers/introdb/ -run 'TestFetch' -v
```

Expected: PASS (all four `TestFetch*` tests).

- [ ] **Step 8: Commit**

```bash
git add internal/markers/introdb/client.go internal/markers/introdb/provider.go internal/markers/introdb/client_test.go
git commit -m "fix(markers): honor TVDB ids in TheIntroDB lookups"
```

---

## Task 3: Real confidence + best-candidate selection (gaps #2, #3)

**Files:**
- Modify: `internal/markers/introdb/provider.go` (`pickMarker`)
- Create: `internal/markers/introdb/provider_test.go`

- [ ] **Step 1: Write the failing provider tests**

Create `internal/markers/introdb/provider_test.go`:

```go
package introdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
)

func newProvider(t *testing.T, body string) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("")
	c.SetBaseURL(srv.URL)
	return NewProvider(c)
}

func episodeReq(ids map[string]string) markers.Request {
	return markers.Request{
		Kind:          markers.ItemKindEpisode,
		ExternalIDs:   ids,
		SeasonNumber:  1,
		EpisodeNumber: 1,
		Duration:      30 * time.Minute,
	}
}

func TestProviderResolvesTVDBOnly(t *testing.T) {
	p := newProvider(t, `{"type":"episode","intro":[{"end_ms":60000}]}`)
	res, err := p.FetchMarkers(context.Background(), episodeReq(map[string]string{markers.ExternalIDKeyTVDB: "777"}))
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if len(res.Markers) != 1 || res.Markers[0].Kind != markers.MarkerKindIntro {
		t.Fatalf("expected one intro marker, got %+v", res.Markers)
	}
}

func TestProviderUsesRealConfidence(t *testing.T) {
	p := newProvider(t, `{"type":"episode","intro":[{"end_ms":60000,"confidence":0.42}]}`)
	res, _ := p.FetchMarkers(context.Background(), episodeReq(map[string]string{markers.ExternalIDKeyTMDB: "1"}))
	if len(res.Markers) != 1 {
		t.Fatalf("want 1 marker, got %d", len(res.Markers))
	}
	if res.Markers[0].Confidence != 0.42 {
		t.Errorf("confidence = %v, want 0.42 (real value, not hardcoded)", res.Markers[0].Confidence)
	}
}

func TestProviderDefaultsConfidenceWhenAbsent(t *testing.T) {
	p := newProvider(t, `{"type":"episode","intro":[{"end_ms":60000}]}`)
	res, _ := p.FetchMarkers(context.Background(), episodeReq(map[string]string{markers.ExternalIDKeyTMDB: "1"}))
	if res.Markers[0].Confidence != defaultConfidence {
		t.Errorf("confidence = %v, want default %v", res.Markers[0].Confidence, defaultConfidence)
	}
}

func TestProviderPicksMostSubmittedCandidate(t *testing.T) {
	body := `{"type":"episode","intro":[
		{"end_ms":50000,"confidence":0.6,"submission_count":2},
		{"end_ms":61000,"confidence":0.5,"submission_count":9}
	]}`
	p := newProvider(t, body)
	res, _ := p.FetchMarkers(context.Background(), episodeReq(map[string]string{markers.ExternalIDKeyTMDB: "1"}))
	if len(res.Markers) != 1 {
		t.Fatalf("want 1 marker, got %d", len(res.Markers))
	}
	if got := res.Markers[0].End; got != 61*time.Second {
		t.Errorf("picked end = %v, want 61s (the submission_count=9 candidate)", got)
	}
	if res.Markers[0].SubmissionCount != 9 {
		t.Errorf("submission_count = %d, want 9", res.Markers[0].SubmissionCount)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/markers/introdb/ -run TestProvider -v
```

Expected: FAIL — `TestProviderUsesRealConfidence` sees `0.9` (hardcoded), `TestProviderPicksMostSubmittedCandidate`
gets the first usable (`end=50s`, `SubmissionCount=0`). (`TestProviderResolvesTVDBOnly` already
passes from Task 2 — it guards against regression.)

- [ ] **Step 3: Rewrite `pickMarker` in `provider.go`**

Replace the entire `pickMarker` function:

```go
// pickMarker selects the best usable segment from a TheIntroDB response array.
// `requireEnd` is true for segments where the end timestamp is the load-bearing
// field (intro, recap) — they're allowed to start at 0 if `start_ms` is omitted.
// For trailing segments (credits, preview) the start is required but the end
// defaults to the file duration. When several candidates are usable (e.g. no
// duration match narrowed the set), the most-submitted one wins, with higher
// confidence breaking ties; with a single candidate this is the previous
// first-usable behavior. Real per-segment confidence is used when present,
// falling back to defaultConfidence only when the API omits it.
func pickMarker(stamps []segmentTimestamps, kind markers.MarkerKind, totalDuration time.Duration, requireEnd bool) (markers.Marker, bool) {
	best := markers.Marker{}
	bestSubs := -1
	found := false
	for _, s := range stamps {
		start := time.Duration(0)
		end := totalDuration
		if s.StartMs != nil {
			start = time.Duration(*s.StartMs) * time.Millisecond
		}
		if s.EndMs != nil {
			end = time.Duration(*s.EndMs) * time.Millisecond
		}
		if requireEnd && s.EndMs == nil {
			continue
		}
		if !requireEnd && s.StartMs == nil {
			continue
		}
		if end <= start {
			continue
		}
		confidence := defaultConfidence
		if s.Confidence != nil {
			confidence = *s.Confidence
		}
		subs := 0
		if s.SubmissionCount != nil {
			subs = *s.SubmissionCount
		}
		if !found || subs > bestSubs || (subs == bestSubs && confidence > best.Confidence) {
			best = markers.Marker{Kind: kind, Start: start, End: end, Confidence: confidence, SubmissionCount: subs}
			bestSubs = subs
			found = true
		}
	}
	return best, found
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/markers/introdb/ -run TestProvider -v
```

Expected: PASS (all four `TestProvider*` tests).

- [ ] **Step 5: Commit**

```bash
git add internal/markers/introdb/provider.go internal/markers/introdb/provider_test.go
git commit -m "feat(markers): use real introdb confidence and pick best candidate"
```

---

## Task 4: Full-package regression + integration build

**Files:** none (verification only)

- [ ] **Step 1: Run the entire markers test suite**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go test ./internal/markers/... -v
```

Expected: PASS — the new `introdb` tests plus the existing `write_test.go` /
`types_test.go` (unchanged behavior: `BuildUpdatePayload` still aggregates max confidence,
`FetchFirstHit` still uses registration order).

- [ ] **Step 2: Build the binary to confirm nothing downstream broke**

```bash
docker run --rm -v "$PWD":/app -w /app -v silo-gomod:/go/pkg/mod \
  -e GOFLAGS=-mod=mod golang:1.26 \
  go build ./...
```

Expected: exit 0. (The only callers of the changed client methods are in `provider.go`, updated
in Task 2; `cmd/silo/main.go` constructs the provider but never calls the client directly.)

- [ ] **Step 3: Confirm the working tree is clean**

```bash
git status
```

Expected: clean (all changes committed in Tasks 1–3; this task adds no files).

---

## Self-review notes

- **Gap coverage:** TVDB lookups (Task 2, `TestFetch*` + `TestProviderResolvesTVDBOnly`), real
  confidence (Task 3, `TestProviderUsesRealConfidence` + `…DefaultsConfidenceWhenAbsent`),
  best-candidate (Task 3, `TestProviderPicksMostSubmittedCandidate`). Maps to design gaps #1–#3.
- **Containment:** the client signature change touches exactly one production caller
  (`provider.go`); `markers.Marker` is constructed only in `provider.go`. Verified by grep before
  planning, re-verified by the Task 4 `go build ./...`.
- **No behavior regressions:** the write path is untouched, so `BuildUpdatePayload`'s
  max-confidence aggregation and its test remain valid; per-segment confidence is deferred to
  Phase 2 where the merge actually consumes it.
- **No placeholders:** every code step shows the complete replacement; every run step shows the
  command and the expected result. No migration, no frontend, no wiring changes.
- **Default confidence:** `defaultConfidence = 0.9` preserves today's effective value for
  responses that omit confidence, so existing stored markers keep equivalent provenance.
