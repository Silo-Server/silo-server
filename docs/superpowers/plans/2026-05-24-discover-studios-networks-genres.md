# Discover: Studios, Networks, Genres — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Overseerr-style brand-card discovery (Studios, Networks, Genres) on top of the existing request system, with three new list endpoints, three new browse endpoints, and three new frontend carousels + a shared browse page.

**Architecture:** Additive extension of `internal/requests/` (new files: `discover_bundle.go`, `discover_logo_cache.go`, `discover_brand.go`) plus `internal/metadata/tmdb/client.go` additions (`DiscoverPage`, `GetCompany`, `GetNetwork`, `WithCompanies` / `WithNetworks` params). Frontend adds 2 components, 1 page, query hooks, and 3 routes. No new tables, no migrations, no admin UI. Caches are process-local.

**Tech Stack:** Go (chi, pgx, stdlib `singleflight`), TypeScript (React 18, React Router v6, TanStack Query, shadcn/ui, Tailwind).

**Spec:** `docs/superpowers/specs/2026-05-24-discover-studios-networks-genres-design.md` (commit `413cf7b8`).

**Worktree:** All paths below are relative to the repository root. Commands assume the repository root is the cwd.

---

## File Structure

### Created files

| Path | Responsibility |
|---|---|
| `internal/requests/discover_bundle.go` | Bundled `BundledStudio`, `BundledNetwork`, `BundledGenre` constants and slug → entity helpers. |
| `internal/requests/discover_bundle_test.go` | Slug lookup, integrity (no duplicate slugs, all required fields populated). |
| `internal/requests/discover_logo_cache.go` | In-memory 24h cache for TMDB company/network `logo_path`. Singleflight on miss. |
| `internal/requests/discover_logo_cache_test.go` | TTL expiry, miss returns `nil` path, parallel access does not double-fetch. |
| `internal/requests/discover_brand.go` | `ListStudios`, `ListNetworks`, `ListGenres`, `BrowseStudio`, `BrowseNetwork`, `BrowseGenre` service methods + response types. |
| `web/src/components/BrandCard.tsx` | Renders one studio/network/genre card (brand color + TMDB logo or gradient + text). |
| `web/src/components/BrandCarousel.tsx` | Horizontal-scroll carousel of `BrandCard`s. |
| `web/src/pages/RequestBrowse.tsx` | Shared browse page for `/requests/browse/{studio,network,genre}/:slug`. |

### Modified files

| Path | What changes |
|---|---|
| `internal/metadata/tmdb/types.go` | Extend `DiscoverParams` with `WithCompanies []int` and `WithNetworks []int`. Add `Company` and `Network` response types. |
| `internal/metadata/tmdb/client.go` | Wire new params into `buildDiscoverQuery`; add `DiscoverPage`, `GetCompany`, `GetNetwork` methods. |
| `internal/metadata/tmdb/client_test.go` | New test cases for `WithCompanies` / `WithNetworks` query construction, `DiscoverPage` happy path, `GetCompany` / `GetNetwork`. |
| `internal/requests/service.go` | Extend the `TMDBClient` interface near the top (add `DiscoverPage`, `GetCompany`, `GetNetwork`). Add `LogoCache` setter. |
| `internal/requests/service_test.go` | Extend `fakeTMDBClient` to satisfy the expanded interface. Add tests for `ListStudios`/`Networks`/`Genres`, `BrowseStudio`/`Network`/`Genre`. |
| `internal/api/handlers/requests.go` | Add 6 handler methods + extend the `RequestService` interface near the top of the file. |
| `internal/api/handlers/requests_test.go` | Tests for the 6 new endpoints. *(Create if it does not exist; otherwise extend.)* |
| `internal/api/router.go` | Wire 6 new routes under `/requests/discover/...`. |
| `web/src/api/types.ts` | Add `DiscoverStudio`, `DiscoverNetwork`, `DiscoverGenre`, `DiscoverBrowseResponse`. |
| `web/src/hooks/queries/keys.ts` | Add `requestKeys.discoverStudios()`, `discoverNetworks()`, `discoverGenres()`, `discoverBrowse(...)`. |
| `web/src/hooks/queries/requests.ts` | Add `useDiscoverStudios`, `useDiscoverNetworks`, `useDiscoverGenres`, `useRequestBrowse`. |
| `web/src/pages/Requests.tsx` | Append three `BrandCarousel` sections after the existing six discovery carousels. |
| `web/src/App.tsx` | Add 3 new routes (`/requests/browse/studio/:slug`, `/network/:slug`, `/genre/:slug`). |

---

## Task 1: TMDB DiscoverParams — add WithCompanies and WithNetworks

**Files:**
- Modify: `internal/metadata/tmdb/types.go:65-79` (`DiscoverParams` struct)
- Modify: `internal/metadata/tmdb/client.go:513-560` (`buildDiscoverQuery`)
- Test: `internal/metadata/tmdb/client_test.go` (extend existing `TestDiscoverMovieBuildsQuery`-style test)

- [ ] **Step 1: Write the failing test**

Add to `internal/metadata/tmdb/client_test.go` at the bottom of the file, just before any helper definitions:

```go
func TestDiscoverIncludesCompaniesAndNetworks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("with_companies"); got != "420,2" {
			t.Errorf("with_companies = %q, want 420,2", got)
		}
		if got := q.Get("with_networks"); got != "213,49" {
			t.Errorf("with_networks = %q, want 213,49", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":1,"total_pages":1,"total_results":0,"results":[]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	_, err := client.Discover(context.Background(), "movie", DiscoverParams{
		SortBy:        "popularity.desc",
		WithCompanies: []int{420, 2},
		WithNetworks:  []int{213, 49},
		Limit:         5,
	})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/metadata/tmdb/ -run TestDiscoverIncludesCompaniesAndNetworks -v
```

Expected: FAIL — compile error (`unknown field WithCompanies in struct literal of type DiscoverParams`).

- [ ] **Step 3: Add fields to DiscoverParams**

In `internal/metadata/tmdb/types.go`, modify the `DiscoverParams` struct to add two new fields after `WithoutGenres`:

```go
type DiscoverParams struct {
	WithGenres       []int
	WithoutGenres    []int
	WithCompanies    []int
	WithNetworks     []int
	SortBy           string
	VoteCountGte     int
	VoteAverageGte   float64
	ReleaseDateGte   string
	ReleaseDateLte   string
	Certifications   []string
	CertificationLte string
	WithRuntimeGte   int
	WithRuntimeLte   int
	OriginalLanguage string
	Limit            int
}
```

In `internal/metadata/tmdb/client.go`, modify `buildDiscoverQuery` to wire the new fields. After the `WithoutGenres` block (around line 520), insert:

```go
	if companies := joinIntSlice(params.WithCompanies, ","); companies != "" {
		values.Set("with_companies", companies)
	}
	if networks := joinIntSlice(params.WithNetworks, ","); networks != "" {
		values.Set("with_networks", networks)
	}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/metadata/tmdb/ -run TestDiscoverIncludesCompaniesAndNetworks -v
go test ./internal/metadata/tmdb/ -v
```

Expected: PASS for the new test; all other TMDB tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/metadata/tmdb/types.go internal/metadata/tmdb/client.go internal/metadata/tmdb/client_test.go
git commit -m "feat(tmdb): add with_companies and with_networks to discover params"
```

---

## Task 2: TMDB client — DiscoverPage (returns full MediaPage with sort + page)

The existing `Client.Discover` method returns `[]CollectionResult` — only `{ID, MediaType, Title}`. The browse endpoints need the full result shape (posters, overviews, vote averages). We add a new `DiscoverPage` method that wraps the same TMDB endpoint but returns `*MediaPage` (with full `MediaResult` items) and exposes single-page semantics (`page` + `sort` directly, no internal limit-based pagination).

**Files:**
- Modify: `internal/metadata/tmdb/client.go` (add `DiscoverPage` method below `Discover`)
- Test: `internal/metadata/tmdb/client_test.go` (new test cases)

- [ ] **Step 1: Write the failing test**

Add to `internal/metadata/tmdb/client_test.go`:

```go
func TestDiscoverPageMovieReturnsFullResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discover/movie" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if got := q.Get("sort_by"); got != "popularity.desc" {
			t.Errorf("sort_by = %q, want popularity.desc", got)
		}
		if got := q.Get("with_companies"); got != "420" {
			t.Errorf("with_companies = %q, want 420", got)
		}
		if got := q.Get("page"); got != "2" {
			t.Errorf("page = %q, want 2", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 2,
			"total_pages": 8,
			"total_results": 160,
			"results": [
				{"id": 24428, "title": "The Avengers", "release_date": "2012-04-25", "poster_path": "/p.jpg", "overview": "earth's mightiest", "popularity": 100.5, "vote_average": 7.7}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	page, err := client.DiscoverPage(context.Background(), "movie", DiscoverParams{
		SortBy:        "popularity.desc",
		WithCompanies: []int{420},
	}, 2)
	if err != nil {
		t.Fatalf("DiscoverPage: %v", err)
	}
	if page.Page != 2 || page.TotalPages != 8 || page.TotalResults != 160 {
		t.Fatalf("page = %+v", page)
	}
	if len(page.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(page.Results))
	}
	got := page.Results[0]
	if got.ID != 24428 || got.MediaType != "movie" || got.Title != "The Avengers" || got.Year != 2012 {
		t.Errorf("result = %+v", got)
	}
	if got.PosterPath != "/p.jpg" || got.Overview != "earth's mightiest" {
		t.Errorf("result detail mismatch: %+v", got)
	}
}

func TestDiscoverPageTVUsesFirstAirDate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discover/tv" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if got := q.Get("with_networks"); got != "213" {
			t.Errorf("with_networks = %q, want 213", got)
		}
		if got := q.Get("first_air_date.gte"); got != "" {
			t.Errorf("first_air_date.gte = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"page": 1,
			"total_pages": 1,
			"total_results": 1,
			"results": [
				{"id": 1399, "name": "Game of Thrones", "first_air_date": "2011-04-17", "poster_path": "/g.jpg"}
			]
		}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	page, err := client.DiscoverPage(context.Background(), "tv", DiscoverParams{
		SortBy:       "vote_average.desc",
		WithNetworks: []int{213},
	}, 1)
	if err != nil {
		t.Fatalf("DiscoverPage tv: %v", err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(page.Results))
	}
	got := page.Results[0]
	if got.MediaType != "series" || got.Title != "Game of Thrones" || got.Year != 2011 {
		t.Errorf("result = %+v", got)
	}
}

func TestDiscoverPageRejectsInvalidMediaType(t *testing.T) {
	client := NewClient("test-key", 1000)
	_, err := client.DiscoverPage(context.Background(), "all", DiscoverParams{SortBy: "popularity.desc"}, 1)
	if err == nil {
		t.Fatal("expected error for invalid media type")
	}
}

func TestDiscoverPageRequiresSortBy(t *testing.T) {
	client := NewClient("test-key", 1000)
	_, err := client.DiscoverPage(context.Background(), "movie", DiscoverParams{}, 1)
	if err == nil {
		t.Fatal("expected error when sort_by is empty")
	}
}

func TestDiscoverPageDefaultsToPage1(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("page"); got != "1" {
			t.Errorf("page = %q, want 1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":1,"total_pages":1,"total_results":0,"results":[]}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	if _, err := client.DiscoverPage(context.Background(), "movie", DiscoverParams{SortBy: "popularity.desc"}, 0); err != nil {
		t.Fatalf("DiscoverPage: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/metadata/tmdb/ -run TestDiscoverPage -v
```

Expected: FAIL — `client.DiscoverPage` undefined.

- [ ] **Step 3: Implement DiscoverPage**

Add to `internal/metadata/tmdb/client.go` directly after the existing `Discover` method (after line ~509):

```go
// DiscoverPage fetches a single page from TMDB's /discover/{movie,tv} endpoint
// and returns the full MediaPage shape (with posters, overviews, etc.) so the
// request system can enrich it with availability and request state. Unlike
// Discover (which is intended for collection templates and returns just IDs
// and titles), DiscoverPage exposes single-page semantics: callers control
// pagination explicitly.
func (c *Client) DiscoverPage(ctx context.Context, mediaType string, params DiscoverParams, page int) (*MediaPage, error) {
	switch mediaType {
	case "movie", "tv":
	default:
		return nil, fmt.Errorf("tmdb: invalid media type for discover: %q", mediaType)
	}
	if strings.TrimSpace(params.SortBy) == "" {
		return nil, fmt.Errorf("tmdb: discover requires sort_by")
	}
	if page <= 0 {
		page = 1
	}

	query := buildDiscoverQuery(mediaType, params) + "&page=" + strconv.Itoa(page)
	path := "/discover/" + mediaType + "?" + query

	if mediaType == "tv" {
		var resp paginatedResponse[mediaTVResponse]
		if err := c.doGet(ctx, path, &resp); err != nil {
			return nil, err
		}
		return normalizeTVPage(resp), nil
	}
	var resp paginatedResponse[mediaMovieResponse]
	if err := c.doGet(ctx, path, &resp); err != nil {
		return nil, err
	}
	return normalizeMoviePage(resp), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/metadata/tmdb/ -v
```

Expected: All TMDB tests PASS (including the 5 new `TestDiscoverPage*` tests).

- [ ] **Step 5: Commit**

```bash
git add internal/metadata/tmdb/client.go internal/metadata/tmdb/client_test.go
git commit -m "feat(tmdb): add DiscoverPage returning full MediaPage with pagination"
```

---

## Task 3: TMDB client — GetCompany and GetNetwork

We need to fetch `logo_path` per studio/network. TMDB's `/company/{id}` and `/network/{id}` endpoints return the canonical entity.

**Files:**
- Modify: `internal/metadata/tmdb/types.go` (add `Company`, `Network` types)
- Modify: `internal/metadata/tmdb/client.go` (add `GetCompany`, `GetNetwork` methods)
- Test: `internal/metadata/tmdb/client_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/metadata/tmdb/client_test.go`:

```go
func TestGetCompany(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/company/420" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":420,"name":"Marvel Studios","logo_path":"/hUze.png"}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	company, err := client.GetCompany(context.Background(), 420)
	if err != nil {
		t.Fatalf("GetCompany: %v", err)
	}
	if company.ID != 420 || company.Name != "Marvel Studios" || company.LogoPath != "/hUze.png" {
		t.Errorf("company = %+v", company)
	}
}

func TestGetCompanyMissingLogoReturnsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":420,"name":"Marvel Studios","logo_path":null}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	company, err := client.GetCompany(context.Background(), 420)
	if err != nil {
		t.Fatalf("GetCompany: %v", err)
	}
	if company.LogoPath != "" {
		t.Errorf("logo_path = %q, want empty for null", company.LogoPath)
	}
}

func TestGetNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/network/213" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":213,"name":"Netflix","logo_path":"/wuU9.png"}`))
	}))
	defer server.Close()

	client := NewClient("test-key", 1000)
	client.SetBaseURL(server.URL)

	network, err := client.GetNetwork(context.Background(), 213)
	if err != nil {
		t.Fatalf("GetNetwork: %v", err)
	}
	if network.ID != 213 || network.Name != "Netflix" || network.LogoPath != "/wuU9.png" {
		t.Errorf("network = %+v", network)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/metadata/tmdb/ -run "TestGetCompany|TestGetNetwork" -v
```

Expected: FAIL — `GetCompany` and `GetNetwork` undefined.

- [ ] **Step 3: Add Company / Network types and methods**

In `internal/metadata/tmdb/types.go`, append after the existing types:

```go
// Company is the decoded payload of TMDB's /company/{id} endpoint.
type Company struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	LogoPath string `json:"logo_path"`
}

// Network is the decoded payload of TMDB's /network/{id} endpoint.
type Network struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	LogoPath string `json:"logo_path"`
}
```

In `internal/metadata/tmdb/client.go`, append after the `DiscoverPage` method (added in Task 2):

```go
// GetCompany fetches a TMDB company (production studio) by ID. Used to
// resolve logo paths for the bundled discovery studios.
func (c *Client) GetCompany(ctx context.Context, id int) (*Company, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb: invalid company id: %d", id)
	}
	var company Company
	if err := c.doGet(ctx, fmt.Sprintf("/company/%d", id), &company); err != nil {
		return nil, err
	}
	return &company, nil
}

// GetNetwork fetches a TMDB TV network by ID. Used to resolve logo paths
// for the bundled discovery networks.
func (c *Client) GetNetwork(ctx context.Context, id int) (*Network, error) {
	if id <= 0 {
		return nil, fmt.Errorf("tmdb: invalid network id: %d", id)
	}
	var network Network
	if err := c.doGet(ctx, fmt.Sprintf("/network/%d", id), &network); err != nil {
		return nil, err
	}
	return &network, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/metadata/tmdb/ -v
```

Expected: All TMDB tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/metadata/tmdb/types.go internal/metadata/tmdb/client.go internal/metadata/tmdb/client_test.go
git commit -m "feat(tmdb): add GetCompany and GetNetwork for logo path resolution"
```

---

## Task 4: Bundled discovery registry

Create the compile-time list of studios, networks, and genres. Each has a slug, display name, TMDB ID(s), and presentation hints (brand color or gradient).

**Files:**
- Create: `internal/requests/discover_bundle.go`
- Create: `internal/requests/discover_bundle_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/requests/discover_bundle_test.go`:

```go
package requests

import (
	"strings"
	"testing"
)

func TestBundleHasExpectedCounts(t *testing.T) {
	if len(BundledStudios) != 10 {
		t.Errorf("BundledStudios = %d, want 10", len(BundledStudios))
	}
	if len(BundledNetworks) != 10 {
		t.Errorf("BundledNetworks = %d, want 10", len(BundledNetworks))
	}
	if len(BundledGenres) != 8 {
		t.Errorf("BundledGenres = %d, want 8", len(BundledGenres))
	}
}

func TestBundleStudiosHaveRequiredFields(t *testing.T) {
	for _, s := range BundledStudios {
		if s.TMDBID <= 0 {
			t.Errorf("studio %q missing TMDBID", s.Slug)
		}
		if strings.TrimSpace(s.Slug) == "" {
			t.Errorf("studio %+v missing Slug", s)
		}
		if strings.TrimSpace(s.DisplayName) == "" {
			t.Errorf("studio %q missing DisplayName", s.Slug)
		}
		if !strings.HasPrefix(s.BrandColor, "#") {
			t.Errorf("studio %q BrandColor must start with #, got %q", s.Slug, s.BrandColor)
		}
	}
}

func TestBundleNetworksHaveRequiredFields(t *testing.T) {
	for _, n := range BundledNetworks {
		if n.TMDBID <= 0 {
			t.Errorf("network %q missing TMDBID", n.Slug)
		}
		if strings.TrimSpace(n.Slug) == "" {
			t.Errorf("network %+v missing Slug", n)
		}
		if strings.TrimSpace(n.DisplayName) == "" {
			t.Errorf("network %q missing DisplayName", n.Slug)
		}
		if !strings.HasPrefix(n.BrandColor, "#") {
			t.Errorf("network %q BrandColor must start with #, got %q", n.Slug, n.BrandColor)
		}
	}
}

func TestBundleGenresHaveRequiredFields(t *testing.T) {
	for _, g := range BundledGenres {
		if strings.TrimSpace(g.Slug) == "" {
			t.Errorf("genre %+v missing Slug", g)
		}
		if strings.TrimSpace(g.DisplayName) == "" {
			t.Errorf("genre %q missing DisplayName", g.Slug)
		}
		if g.MovieID <= 0 {
			t.Errorf("genre %q must have MovieID > 0 in v1", g.Slug)
		}
		if !strings.HasPrefix(g.GradientFrom, "#") || !strings.HasPrefix(g.GradientTo, "#") {
			t.Errorf("genre %q gradient must use # hex, got from=%q to=%q", g.Slug, g.GradientFrom, g.GradientTo)
		}
	}
}

func TestBundleSlugsAreUniqueWithinKind(t *testing.T) {
	seen := map[string]string{}
	for _, s := range BundledStudios {
		key := "studio:" + s.Slug
		if prior, ok := seen[key]; ok {
			t.Errorf("duplicate studio slug %q (also %q)", s.Slug, prior)
		}
		seen[key] = s.DisplayName
	}
	for _, n := range BundledNetworks {
		key := "network:" + n.Slug
		if prior, ok := seen[key]; ok {
			t.Errorf("duplicate network slug %q (also %q)", n.Slug, prior)
		}
		seen[key] = n.DisplayName
	}
	for _, g := range BundledGenres {
		key := "genre:" + g.Slug
		if prior, ok := seen[key]; ok {
			t.Errorf("duplicate genre slug %q (also %q)", g.Slug, prior)
		}
		seen[key] = g.DisplayName
	}
}

func TestFindStudioBySlug(t *testing.T) {
	got, ok := FindStudioBySlug("marvel-studios")
	if !ok {
		t.Fatal("expected marvel-studios to exist")
	}
	if got.DisplayName != "Marvel Studios" {
		t.Errorf("display = %q, want Marvel Studios", got.DisplayName)
	}

	if _, ok := FindStudioBySlug("not-a-real-studio"); ok {
		t.Error("expected unknown slug to return false")
	}
}

func TestFindNetworkBySlug(t *testing.T) {
	got, ok := FindNetworkBySlug("netflix")
	if !ok {
		t.Fatal("expected netflix to exist")
	}
	if got.DisplayName != "Netflix" {
		t.Errorf("display = %q, want Netflix", got.DisplayName)
	}
}

func TestFindGenreBySlug(t *testing.T) {
	got, ok := FindGenreBySlug("action")
	if !ok {
		t.Fatal("expected action to exist")
	}
	if got.MovieID != 28 {
		t.Errorf("movie id = %d, want 28", got.MovieID)
	}
}

func TestGenresWithoutTVEquivalentHaveZeroSeriesID(t *testing.T) {
	horror, ok := FindGenreBySlug("horror")
	if !ok {
		t.Fatal("expected horror to exist")
	}
	if horror.SeriesID != 0 {
		t.Errorf("horror.SeriesID = %d, want 0", horror.SeriesID)
	}
	romance, ok := FindGenreBySlug("romance")
	if !ok {
		t.Fatal("expected romance to exist")
	}
	if romance.SeriesID != 0 {
		t.Errorf("romance.SeriesID = %d, want 0", romance.SeriesID)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/requests/ -run "TestBundle|TestFind|TestGenresWithout" -v
```

Expected: FAIL — symbols undefined.

- [ ] **Step 3: Implement the bundle**

Create `internal/requests/discover_bundle.go`:

```go
package requests

// BundledStudio is a curated movie studio surfaced in the request discover
// section. The TMDB ID identifies the company in /discover/movie?with_companies=.
type BundledStudio struct {
	TMDBID      int
	Slug        string
	DisplayName string
	BrandColor  string // hex, used as card background
}

// BundledNetwork is a curated TV network surfaced in the request discover
// section. The TMDB ID identifies the network in /discover/tv?with_networks=.
type BundledNetwork struct {
	TMDBID      int
	Slug        string
	DisplayName string
	BrandColor  string
}

// BundledGenre is a curated genre. MovieID is the TMDB movie genre ID;
// SeriesID is the TMDB tv genre ID, or 0 when no TV equivalent exists
// (e.g., Horror, Romance — TV has no direct match for these in TMDB).
type BundledGenre struct {
	Slug         string
	DisplayName  string
	GradientFrom string
	GradientTo   string
	MovieID      int
	SeriesID     int
}

// BundledStudios is the compile-time list of studios shown in the Studios
// carousel. Order is preservation order — render in this order.
var BundledStudios = []BundledStudio{
	{TMDBID: 2, Slug: "walt-disney-pictures", DisplayName: "Walt Disney Pictures", BrandColor: "#003087"},
	{TMDBID: 3, Slug: "pixar", DisplayName: "Pixar", BrandColor: "#0a85ca"},
	{TMDBID: 420, Slug: "marvel-studios", DisplayName: "Marvel Studios", BrandColor: "#ed1d24"},
	{TMDBID: 1, Slug: "lucasfilm", DisplayName: "Lucasfilm", BrandColor: "#000000"},
	{TMDBID: 174, Slug: "warner-bros-pictures", DisplayName: "Warner Bros. Pictures", BrandColor: "#004c97"},
	{TMDBID: 33, Slug: "universal-pictures", DisplayName: "Universal Pictures", BrandColor: "#1f2a44"},
	{TMDBID: 4, Slug: "paramount-pictures", DisplayName: "Paramount Pictures", BrandColor: "#0066b3"},
	{TMDBID: 5, Slug: "sony-pictures", DisplayName: "Sony Pictures", BrandColor: "#bf2f38"},
	{TMDBID: 25, Slug: "20th-century-studios", DisplayName: "20th Century Studios", BrandColor: "#000000"},
	{TMDBID: 10342, Slug: "studio-ghibli", DisplayName: "Studio Ghibli", BrandColor: "#1a4d2e"},
}

// BundledNetworks is the compile-time list of networks shown in the Networks
// carousel. Order is preservation order.
var BundledNetworks = []BundledNetwork{
	{TMDBID: 213, Slug: "netflix", DisplayName: "Netflix", BrandColor: "#e50914"},
	{TMDBID: 2739, Slug: "disney-plus", DisplayName: "Disney+", BrandColor: "#0e3c7d"},
	{TMDBID: 2552, Slug: "apple-tv-plus", DisplayName: "Apple TV+", BrandColor: "#000000"},
	{TMDBID: 49, Slug: "hbo", DisplayName: "HBO", BrandColor: "#000000"},
	{TMDBID: 453, Slug: "hulu", DisplayName: "Hulu", BrandColor: "#1ce783"},
	{TMDBID: 1024, Slug: "amazon-prime-video", DisplayName: "Amazon Prime Video", BrandColor: "#00a8e1"},
	{TMDBID: 3186, Slug: "max", DisplayName: "Max", BrandColor: "#002be7"},
	{TMDBID: 4330, Slug: "paramount-plus", DisplayName: "Paramount+", BrandColor: "#0064ff"},
	{TMDBID: 4, Slug: "bbc", DisplayName: "BBC", BrandColor: "#000000"},
	{TMDBID: 88, Slug: "fx", DisplayName: "FX", BrandColor: "#000000"},
}

// BundledGenres is the compile-time list of genres shown in the Genres
// carousel. SeriesID = 0 means the genre has no direct TV equivalent and
// the browse page hides the Series tab.
var BundledGenres = []BundledGenre{
	{Slug: "action", DisplayName: "Action", GradientFrom: "#dc2626", GradientTo: "#7f1d1d", MovieID: 28, SeriesID: 10759},
	{Slug: "comedy", DisplayName: "Comedy", GradientFrom: "#fbbf24", GradientTo: "#b45309", MovieID: 35, SeriesID: 35},
	{Slug: "drama", DisplayName: "Drama", GradientFrom: "#64748b", GradientTo: "#1e293b", MovieID: 18, SeriesID: 18},
	{Slug: "sci-fi", DisplayName: "Sci-Fi", GradientFrom: "#7c3aed", GradientTo: "#312e81", MovieID: 878, SeriesID: 10765},
	{Slug: "horror", DisplayName: "Horror", GradientFrom: "#7f1d1d", GradientTo: "#1f2937", MovieID: 27, SeriesID: 0},
	{Slug: "romance", DisplayName: "Romance", GradientFrom: "#ec4899", GradientTo: "#831843", MovieID: 10749, SeriesID: 0},
	{Slug: "animation", DisplayName: "Animation", GradientFrom: "#06b6d4", GradientTo: "#155e75", MovieID: 16, SeriesID: 16},
	{Slug: "documentary", DisplayName: "Documentary", GradientFrom: "#475569", GradientTo: "#0f172a", MovieID: 99, SeriesID: 99},
}

// FindStudioBySlug looks up a bundled studio by slug. Returns (zero, false)
// if not found.
func FindStudioBySlug(slug string) (BundledStudio, bool) {
	for _, s := range BundledStudios {
		if s.Slug == slug {
			return s, true
		}
	}
	return BundledStudio{}, false
}

// FindNetworkBySlug looks up a bundled network by slug.
func FindNetworkBySlug(slug string) (BundledNetwork, bool) {
	for _, n := range BundledNetworks {
		if n.Slug == slug {
			return n, true
		}
	}
	return BundledNetwork{}, false
}

// FindGenreBySlug looks up a bundled genre by slug.
func FindGenreBySlug(slug string) (BundledGenre, bool) {
	for _, g := range BundledGenres {
		if g.Slug == slug {
			return g, true
		}
	}
	return BundledGenre{}, false
}
```

> **Note on TMDB IDs:** The IDs above are the conventional Overseerr/TMDB defaults for these brands. Verify a small sample manually with `curl "https://api.themoviedb.org/3/company/420?api_key=…"` before merging — if any are wrong, swap the ID and add an `_id_verified` test if helpful. This step does not block test progress because no test asserts a specific TMDB ID.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/requests/ -run "TestBundle|TestFind|TestGenresWithout" -v
```

Expected: All 8 new bundle tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/requests/discover_bundle.go internal/requests/discover_bundle_test.go
git commit -m "feat(requests): add bundled studio/network/genre registry"
```

---

## Task 5: Logo cache with TTL and singleflight

In-memory cache for TMDB `logo_path`. Singleflight (`golang.org/x/sync/singleflight`) deduplicates concurrent misses.

**Files:**
- Create: `internal/requests/discover_logo_cache.go`
- Create: `internal/requests/discover_logo_cache_test.go`

- [ ] **Step 1: Check whether singleflight is available**

```bash
grep -r "golang.org/x/sync/singleflight" go.sum | head -1
```

If found, the dependency is already present (no go.mod change needed). If not, you'll need `go get golang.org/x/sync` — Step 3 covers this.

- [ ] **Step 2: Write the failing tests**

Create `internal/requests/discover_logo_cache_test.go`:

```go
package requests

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLogoLookup struct {
	calls   atomic.Int64
	logoFor map[int]string
	err     error
}

func (f *fakeLogoLookup) Lookup(_ context.Context, id int) (string, error) {
	f.calls.Add(1)
	if f.err != nil {
		return "", f.err
	}
	return f.logoFor[id], nil
}

func TestLogoCacheReturnsCachedValueAfterFirstCall(t *testing.T) {
	lookup := &fakeLogoLookup{logoFor: map[int]string{420: "/hUze.png"}}
	cache := newLogoCache(lookup.Lookup, time.Hour)

	for i := 0; i < 3; i++ {
		got, err := cache.Get(context.Background(), 420)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != "/hUze.png" {
			t.Errorf("got %q, want /hUze.png", got)
		}
	}
	if calls := lookup.calls.Load(); calls != 1 {
		t.Errorf("upstream calls = %d, want 1", calls)
	}
}

func TestLogoCacheExpiresAfterTTL(t *testing.T) {
	lookup := &fakeLogoLookup{logoFor: map[int]string{1: "/a.png"}}
	cache := newLogoCache(lookup.Lookup, 10*time.Millisecond)

	if _, err := cache.Get(context.Background(), 1); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := cache.Get(context.Background(), 1); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if calls := lookup.calls.Load(); calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestLogoCacheSingleflightDeduplicatesParallelMisses(t *testing.T) {
	lookup := &fakeLogoLookup{logoFor: map[int]string{1: "/a.png"}}
	cache := newLogoCache(func(ctx context.Context, id int) (string, error) {
		time.Sleep(20 * time.Millisecond)
		return lookup.Lookup(ctx, id)
	}, time.Hour)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.Get(context.Background(), 1); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls := lookup.calls.Load(); calls != 1 {
		t.Errorf("calls = %d, want 1 (singleflight should dedupe)", calls)
	}
}

func TestLogoCacheReturnsErrorAndDoesNotCacheFailure(t *testing.T) {
	failure := errors.New("upstream boom")
	lookup := &fakeLogoLookup{err: failure}
	cache := newLogoCache(lookup.Lookup, time.Hour)

	if _, err := cache.Get(context.Background(), 1); !errors.Is(err, failure) {
		t.Fatalf("first err = %v, want %v", err, failure)
	}
	if _, err := cache.Get(context.Background(), 1); !errors.Is(err, failure) {
		t.Fatalf("second err = %v, want %v", err, failure)
	}
	if calls := lookup.calls.Load(); calls != 2 {
		t.Errorf("calls = %d, want 2 (errors must not be cached)", calls)
	}
}

func TestLogoCacheEmptyPathIsCachedAsMiss(t *testing.T) {
	lookup := &fakeLogoLookup{logoFor: map[int]string{1: ""}}
	cache := newLogoCache(lookup.Lookup, time.Hour)

	for i := 0; i < 3; i++ {
		got, err := cache.Get(context.Background(), 1)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	}
	if calls := lookup.calls.Load(); calls != 1 {
		t.Errorf("calls = %d, want 1 (empty strings should still be cached)", calls)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/requests/ -run TestLogoCache -v
```

Expected: FAIL — `newLogoCache` undefined.

If singleflight is not yet in go.mod (very unlikely, but possible), fetch it now:

```bash
go get golang.org/x/sync/singleflight
```

- [ ] **Step 4: Implement the cache**

Create `internal/requests/discover_logo_cache.go`:

```go
package requests

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// LogoLookupFunc resolves a TMDB entity ID to a logo path.
type LogoLookupFunc func(ctx context.Context, id int) (string, error)

type logoCacheEntry struct {
	path      string
	expiresAt time.Time
}

// logoCache caches TMDB company/network logo paths with a TTL. Concurrent
// misses for the same ID are deduplicated via singleflight so we don't
// hammer TMDB on first-load bursts.
type logoCache struct {
	lookup LogoLookupFunc
	ttl    time.Duration
	group  singleflight.Group

	mu      sync.RWMutex
	entries map[int]logoCacheEntry
}

func newLogoCache(lookup LogoLookupFunc, ttl time.Duration) *logoCache {
	return &logoCache{
		lookup:  lookup,
		ttl:     ttl,
		entries: map[int]logoCacheEntry{},
	}
}

// Get returns the cached logo path for id, fetching it via the lookup function
// on miss. Empty strings (TMDB returned no logo) are cached as misses — we
// still avoid refetching them within the TTL window. Errors are not cached.
func (c *logoCache) Get(ctx context.Context, id int) (string, error) {
	if cached, ok := c.read(id); ok {
		return cached, nil
	}

	key := keyFromID(id)
	value, err, _ := c.group.Do(key, func() (any, error) {
		if cached, ok := c.read(id); ok {
			return cached, nil
		}
		path, err := c.lookup(ctx, id)
		if err != nil {
			return "", err
		}
		c.write(id, path)
		return path, nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func (c *logoCache) read(id int) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[id]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.path, true
}

func (c *logoCache) write(id int, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[id] = logoCacheEntry{
		path:      path,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func keyFromID(id int) string {
	return "logo:" + itoaSmall(id)
}

func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/requests/ -run TestLogoCache -v
```

Expected: All 5 logo-cache tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/requests/discover_logo_cache.go internal/requests/discover_logo_cache_test.go
git commit -m "feat(requests): add singleflight logo cache for TMDB company/network logos"
```

> Note: this task has 6 steps because of the optional `go get` check. If singleflight was already in go.mod, Step 1 was instant.

---

## Task 6: Extend TMDBClient interface and add ListStudios / ListNetworks / ListGenres

This task wires the bundle and logo cache into the service. The interface extension lets the service use `DiscoverPage` / `GetCompany` / `GetNetwork` from the TMDB client.

**Files:**
- Modify: `internal/requests/service.go:14-18` (extend `TMDBClient` interface; add `LogoCache` field; add setter)
- Create: `internal/requests/discover_brand.go` (will hold all 6 List/Browse methods; this task adds List* only)
- Modify: `internal/requests/service_test.go` (extend `fakeTMDBClient`; add tests for List* methods)

- [ ] **Step 1: Find and read the existing fakeTMDBClient**

```bash
grep -n "fakeTMDBClient" internal/requests/service_test.go | head -5
```

Look up the struct definition (likely near line 230 or in test helpers). You'll add `DiscoverPage`, `GetCompany`, `GetNetwork` methods to it in Step 4.

- [ ] **Step 2: Write the failing tests**

Append to `internal/requests/service_test.go`:

```go
func TestListStudiosReturnsBundleWithLogos(t *testing.T) {
	tmdbClient := &fakeTMDBClient{
		companies: map[int]*tmdb.Company{
			420: {ID: 420, Name: "Marvel Studios", LogoPath: "/hUze.png"},
		},
	}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	studios, err := service.ListStudios(context.Background(), testViewer(1))
	if err != nil {
		t.Fatalf("ListStudios: %v", err)
	}
	if len(studios) != len(BundledStudios) {
		t.Fatalf("len = %d, want %d", len(studios), len(BundledStudios))
	}

	var marvel *DiscoverBrandCard
	for i := range studios {
		if studios[i].Slug == "marvel-studios" {
			marvel = &studios[i]
			break
		}
	}
	if marvel == nil {
		t.Fatal("marvel-studios missing from response")
	}
	if marvel.LogoURL == nil || *marvel.LogoURL == "" {
		t.Errorf("expected non-nil logo URL for marvel, got %v", marvel.LogoURL)
	}
}

func TestListStudiosToleratesLogoLookupFailure(t *testing.T) {
	tmdbClient := &fakeTMDBClient{
		companies: map[int]*tmdb.Company{},
		companyErr: map[int]error{
			420: errors.New("tmdb down"),
		},
	}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	studios, err := service.ListStudios(context.Background(), testViewer(1))
	if err != nil {
		t.Fatalf("ListStudios should not fail wholesale: %v", err)
	}
	if len(studios) != len(BundledStudios) {
		t.Fatalf("len = %d, want %d", len(studios), len(BundledStudios))
	}
	for _, s := range studios {
		if s.Slug == "marvel-studios" {
			if s.LogoURL != nil {
				t.Errorf("expected nil logo URL on failure, got %v", *s.LogoURL)
			}
			return
		}
	}
	t.Error("marvel-studios missing")
}

func TestListNetworksReturnsBundle(t *testing.T) {
	tmdbClient := &fakeTMDBClient{
		networks: map[int]*tmdb.Network{
			213: {ID: 213, Name: "Netflix", LogoPath: "/wuU9.png"},
		},
	}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	networks, err := service.ListNetworks(context.Background(), testViewer(1))
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	if len(networks) != len(BundledNetworks) {
		t.Fatalf("len = %d, want %d", len(networks), len(BundledNetworks))
	}
}

func TestListGenresReturnsBundleWithSeriesSupportFlag(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})

	genres, err := service.ListGenres(context.Background(), testViewer(1))
	if err != nil {
		t.Fatalf("ListGenres: %v", err)
	}
	if len(genres) != len(BundledGenres) {
		t.Fatalf("len = %d, want %d", len(genres), len(BundledGenres))
	}
	for _, g := range genres {
		switch g.Slug {
		case "action", "comedy", "drama", "sci-fi", "animation", "documentary":
			if !g.SeriesSupported {
				t.Errorf("%s should support series", g.Slug)
			}
		case "horror", "romance":
			if g.SeriesSupported {
				t.Errorf("%s should not support series", g.Slug)
			}
		}
		if g.GradientFrom == "" || g.GradientTo == "" {
			t.Errorf("%s missing gradient", g.Slug)
		}
		if g.LogoURL != nil {
			t.Errorf("%s should not have a logo URL", g.Slug)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./internal/requests/ -run "TestListStudios|TestListNetworks|TestListGenres" -v
```

Expected: FAIL — `service.ListStudios`, `DiscoverBrandCard`, etc. undefined; and `fakeTMDBClient.companies` field unknown.

- [ ] **Step 4: Extend the TMDBClient interface in service.go**

In `internal/requests/service.go`, modify the `TMDBClient` interface (lines 14-18) to add three methods:

```go
type TMDBClient interface {
	SearchMedia(ctx context.Context, mediaType, query string, page int) (*tmdb.MediaPage, error)
	DiscoverSection(ctx context.Context, section string, page int) (*tmdb.MediaPage, error)
	GetMediaDetail(ctx context.Context, mediaType string, id int) (*tmdb.MediaDetail, error)
	DiscoverPage(ctx context.Context, mediaType string, params tmdb.DiscoverParams, page int) (*tmdb.MediaPage, error)
	GetCompany(ctx context.Context, id int) (*tmdb.Company, error)
	GetNetwork(ctx context.Context, id int) (*tmdb.Network, error)
}
```

The concrete `*tmdb.Client` already satisfies the expanded interface (Tasks 2 and 3 added those methods).

Add to the `Service` struct (around line 52-60):

```go
type Service struct {
	store         Store
	tmdb          TMDBClient
	presence      PresenceResolver
	secrets       SecretResolver
	movieAdapter  MovieFulfillmentAdapter
	seriesAdapter SeriesFulfillmentAdapter
	companyLogos  *logoCache
	networkLogos  *logoCache
	Now           func() time.Time
}
```

Modify `NewService` (around line 71) to initialize the caches:

```go
func NewService(store Store, tmdbClient TMDBClient, presence PresenceResolver) *Service {
	svc := &Service{
		store:    store,
		tmdb:     tmdbClient,
		presence: presence,
		Now:      func() time.Time { return time.Now().UTC() },
	}
	svc.companyLogos = newLogoCache(func(ctx context.Context, id int) (string, error) {
		company, err := tmdbClient.GetCompany(ctx, id)
		if err != nil {
			return "", err
		}
		return company.LogoPath, nil
	}, 24*time.Hour)
	svc.networkLogos = newLogoCache(func(ctx context.Context, id int) (string, error) {
		network, err := tmdbClient.GetNetwork(ctx, id)
		if err != nil {
			return "", err
		}
		return network.LogoPath, nil
	}, 24*time.Hour)
	return svc
}
```

- [ ] **Step 5: Extend the fake TMDB client in tests**

In `internal/requests/service_test.go`, find the `fakeTMDBClient` struct and extend it. Add the fields and methods (locate the struct, then add to its definition and method list):

```go
type fakeTMDBClient struct {
	page         *tmdb.MediaPage
	detail       *tmdb.MediaDetail
	externalIDs  *tmdb.ExternalIDs
	companies    map[int]*tmdb.Company
	companyErr   map[int]error
	networks     map[int]*tmdb.Network
	networkErr   map[int]error
	discoverPage *tmdb.MediaPage
	discoverErr  error
}

// (existing SearchMedia / DiscoverSection / GetMediaDetail / GetExternalIDs methods stay)

func (f *fakeTMDBClient) DiscoverPage(_ context.Context, _ string, _ tmdb.DiscoverParams, _ int) (*tmdb.MediaPage, error) {
	if f.discoverErr != nil {
		return nil, f.discoverErr
	}
	if f.discoverPage != nil {
		return f.discoverPage, nil
	}
	return &tmdb.MediaPage{Results: []tmdb.MediaResult{}}, nil
}

func (f *fakeTMDBClient) GetCompany(_ context.Context, id int) (*tmdb.Company, error) {
	if err, ok := f.companyErr[id]; ok {
		return nil, err
	}
	if c, ok := f.companies[id]; ok {
		return c, nil
	}
	return &tmdb.Company{ID: id}, nil
}

func (f *fakeTMDBClient) GetNetwork(_ context.Context, id int) (*tmdb.Network, error) {
	if err, ok := f.networkErr[id]; ok {
		return nil, err
	}
	if n, ok := f.networks[id]; ok {
		return n, nil
	}
	return &tmdb.Network{ID: id}, nil
}
```

> The existing `SearchMedia`, `DiscoverSection`, `GetMediaDetail`, and `GetExternalIDs` methods on `fakeTMDBClient` are untouched. Only add the three new methods and the new struct fields.

- [ ] **Step 6: Create discover_brand.go with response type and ListStudios/Networks/Genres**

Create `internal/requests/discover_brand.go`:

```go
package requests

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// DiscoverBrandCard is one card on the Studios / Networks / Genres carousels.
// Studios and networks carry a TMDB ID and a brand color + lazy-fetched logo.
// Genres carry no TMDB ID at this layer (they hold two — movie and series IDs —
// in the bundle), and render with a gradient + display name instead of a logo.
type DiscoverBrandCard struct {
	TMDBID          int     `json:"tmdb_id,omitempty"`
	Slug            string  `json:"slug"`
	DisplayName     string  `json:"display_name"`
	BrandColor      string  `json:"brand_color,omitempty"`
	LogoURL         *string `json:"logo_url,omitempty"`
	GradientFrom    string  `json:"gradient_from,omitempty"`
	GradientTo      string  `json:"gradient_to,omitempty"`
	SeriesSupported bool    `json:"series_supported,omitempty"`
}

// ListStudios returns the bundled studios with lazily-fetched logo URLs.
// A failed logo lookup for any individual studio yields a card with
// LogoURL = nil; the response is never failed wholesale.
func (s *Service) ListStudios(ctx context.Context, _ Viewer) ([]DiscoverBrandCard, error) {
	if s == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	out := make([]DiscoverBrandCard, 0, len(BundledStudios))
	for _, studio := range BundledStudios {
		card := DiscoverBrandCard{
			TMDBID:      studio.TMDBID,
			Slug:        studio.Slug,
			DisplayName: studio.DisplayName,
			BrandColor:  studio.BrandColor,
			LogoURL:     s.resolveLogoURL(ctx, s.companyLogos, studio.TMDBID, "company", studio.Slug),
		}
		out = append(out, card)
	}
	return out, nil
}

// ListNetworks returns the bundled TV networks with lazily-fetched logo URLs.
func (s *Service) ListNetworks(ctx context.Context, _ Viewer) ([]DiscoverBrandCard, error) {
	if s == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	out := make([]DiscoverBrandCard, 0, len(BundledNetworks))
	for _, network := range BundledNetworks {
		card := DiscoverBrandCard{
			TMDBID:      network.TMDBID,
			Slug:        network.Slug,
			DisplayName: network.DisplayName,
			BrandColor:  network.BrandColor,
			LogoURL:     s.resolveLogoURL(ctx, s.networkLogos, network.TMDBID, "network", network.Slug),
		}
		out = append(out, card)
	}
	return out, nil
}

// ListGenres returns the bundled genres. Each card carries gradient hints
// (no logo URL) and a SeriesSupported flag for the browse page to decide
// whether to show the Series tab.
func (s *Service) ListGenres(_ context.Context, _ Viewer) ([]DiscoverBrandCard, error) {
	if s == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	out := make([]DiscoverBrandCard, 0, len(BundledGenres))
	for _, genre := range BundledGenres {
		out = append(out, DiscoverBrandCard{
			Slug:            genre.Slug,
			DisplayName:     genre.DisplayName,
			GradientFrom:    genre.GradientFrom,
			GradientTo:      genre.GradientTo,
			SeriesSupported: genre.SeriesID > 0,
		})
	}
	return out, nil
}

// resolveLogoURL fetches the cached logo path and renders it as a TMDB image
// URL. Returns nil on lookup failure or when TMDB has no logo for the entity.
// "kind" is "company" or "network", used only for log messages.
func (s *Service) resolveLogoURL(ctx context.Context, cache *logoCache, id int, kind, slug string) *string {
	if cache == nil {
		return nil
	}
	path, err := cache.Get(ctx, id)
	if err != nil {
		slog.Warn("requests: logo lookup failed", "kind", kind, "slug", slug, "id", id, "error", err)
		return nil
	}
	if path == "" {
		return nil
	}
	url := tmdbImageURL(path, "w300")
	return &url
}

// tmdbImageURL is the public TMDB image CDN URL for a given file path and size.
// All TMDB poster/logo/backdrop paths begin with "/" and are valid here.
func tmdbImageURL(path, size string) string {
	return "https://image.tmdb.org/t/p/" + size + path
}
```

If `tmdbImageURL` already exists elsewhere in the codebase, use the existing one and drop the helper here. Check first:

```bash
grep -rn "image.tmdb.org/t/p" internal/ web/src/ | head
```

- [ ] **Step 7: Run tests to verify they pass**

```bash
go test ./internal/requests/ -v
```

Expected: All new tests PASS; existing tests still PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/requests/service.go internal/requests/discover_brand.go internal/requests/service_test.go
git commit -m "feat(requests): add ListStudios/ListNetworks/ListGenres with logo cache"
```

> This task has 8 steps because the interface extension, struct field, constructor, fake client, and three List methods all interlock. Each step is still 2-5 minutes.

---

## Task 7: BrowseStudio / BrowseNetwork / BrowseGenre service methods

**Files:**
- Modify: `internal/requests/discover_brand.go` (add three Browse methods + the response type)
- Modify: `internal/requests/service_test.go` (add browse tests)

- [ ] **Step 1: Write the failing tests**

Append to `internal/requests/service_test.go`:

```go
func TestBrowseStudioReturnsEnrichedMovies(t *testing.T) {
	tmdbClient := &fakeTMDBClient{discoverPage: &tmdb.MediaPage{
		Page:         1,
		TotalPages:   2,
		TotalResults: 20,
		Results: []tmdb.MediaResult{
			{ID: 24428, MediaType: "movie", Title: "The Avengers", Year: 2012, Popularity: 100.5},
		},
	}}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	resp, err := service.BrowseStudio(context.Background(), testViewer(1), "marvel-studios", "popularity", 1)
	if err != nil {
		t.Fatalf("BrowseStudio: %v", err)
	}
	if resp.Kind != "studio" || resp.Slug != "marvel-studios" || resp.MediaType != MediaTypeMovie {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Page != 1 || resp.TotalPages != 2 {
		t.Errorf("pagination = %d/%d", resp.Page, resp.TotalPages)
	}
	if len(resp.Results) != 1 || resp.Results[0].TMDBID != 24428 {
		t.Errorf("results = %+v", resp.Results)
	}
	if resp.Results[0].Availability == "" {
		t.Error("availability should be enriched")
	}
}

func TestBrowseStudioUnknownSlugReturnsNotFound(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})
	_, err := service.BrowseStudio(context.Background(), testViewer(1), "not-a-studio", "popularity", 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestBrowseStudioRejectsBadSort(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})
	_, err := service.BrowseStudio(context.Background(), testViewer(1), "marvel-studios", "made-up-sort", 1)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestBrowseStudioDefaultsBlankSortToPopularity(t *testing.T) {
	tmdbClient := &fakeTMDBClient{discoverPage: &tmdb.MediaPage{Results: []tmdb.MediaResult{}}}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	resp, err := service.BrowseStudio(context.Background(), testViewer(1), "marvel-studios", "", 1)
	if err != nil {
		t.Fatalf("BrowseStudio: %v", err)
	}
	if resp.Sort != "popularity" {
		t.Errorf("sort = %q, want popularity (default)", resp.Sort)
	}
}

func TestBrowseNetworkReturnsSeries(t *testing.T) {
	tmdbClient := &fakeTMDBClient{discoverPage: &tmdb.MediaPage{
		Page: 1, TotalPages: 1, TotalResults: 1,
		Results: []tmdb.MediaResult{
			{ID: 1399, MediaType: "series", Title: "Game of Thrones", Year: 2011},
		},
	}}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	resp, err := service.BrowseNetwork(context.Background(), testViewer(1), "netflix", "popularity", 1)
	if err != nil {
		t.Fatalf("BrowseNetwork: %v", err)
	}
	if resp.MediaType != MediaTypeSeries {
		t.Errorf("media_type = %q, want series", resp.MediaType)
	}
}

func TestBrowseGenreRequiresMediaType(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})
	_, err := service.BrowseGenre(context.Background(), testViewer(1), "action", "", "popularity", 1)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestBrowseGenreSeriesRejectedWhenUnsupported(t *testing.T) {
	service := newTestServiceWithTMDB(newFakeStore(), &fakeTMDBClient{})
	_, err := service.BrowseGenre(context.Background(), testViewer(1), "horror", "series", "popularity", 1)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput for horror+series", err)
	}
}

func TestBrowseGenreMovieReturnsResults(t *testing.T) {
	tmdbClient := &fakeTMDBClient{discoverPage: &tmdb.MediaPage{
		Results: []tmdb.MediaResult{{ID: 1, MediaType: "movie", Title: "Movie"}},
	}}
	service := newTestServiceWithTMDB(newFakeStore(), tmdbClient)

	resp, err := service.BrowseGenre(context.Background(), testViewer(1), "action", "movie", "popularity", 1)
	if err != nil {
		t.Fatalf("BrowseGenre: %v", err)
	}
	if resp.Kind != "genre" || resp.Slug != "action" || resp.MediaType != MediaTypeMovie {
		t.Errorf("resp = %+v", resp)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/requests/ -run "TestBrowse" -v
```

Expected: FAIL — undefined methods.

- [ ] **Step 3: Implement the Browse methods**

Append to `internal/requests/discover_brand.go`:

```go
// DiscoverBrowseResponse is the shape returned by the browse endpoints.
// Results share the same MediaResult shape as search and the existing
// discovery sections, so the frontend can reuse RequestPosterCard.
type DiscoverBrowseResponse struct {
	Kind        string        `json:"kind"`
	Slug        string        `json:"slug"`
	DisplayName string        `json:"display_name"`
	BrandColor  string        `json:"brand_color,omitempty"`
	LogoURL     *string       `json:"logo_url,omitempty"`
	MediaType   MediaType     `json:"media_type"`
	Sort        string        `json:"sort"`
	Page        int           `json:"page"`
	TotalPages  int           `json:"total_pages"`
	Results     []MediaResult `json:"results"`
}

// Allowed sort tokens at the API surface. Internally each maps to a TMDB
// sort_by value. All variants are descending — no ascending equivalents.
var validBrowseSorts = map[string]string{
	"popularity":   "popularity.desc",
	"vote_average": "vote_average.desc",
	"release_date": "primary_release_date.desc", // overridden to first_air_date.desc for tv
}

const defaultBrowseSort = "popularity"

// BrowseStudio returns a page of movies from a bundled studio, enriched with
// Silo availability and request state. Unknown slug → ErrNotFound. Unknown
// sort → ErrInvalidInput.
func (s *Service) BrowseStudio(ctx context.Context, viewer Viewer, slug, sort string, page int) (*DiscoverBrowseResponse, error) {
	studio, ok := FindStudioBySlug(slug)
	if !ok {
		return nil, ErrNotFound
	}
	tmdbSort, sortKey, err := normalizeBrowseSort(sort, "movie")
	if err != nil {
		return nil, err
	}
	tmdbPage, err := s.tmdb.DiscoverPage(ctx, "movie", tmdb.DiscoverParams{
		SortBy:        tmdbSort,
		WithCompanies: []int{studio.TMDBID},
		VoteCountGte:  voteCountFloorForSort(sortKey),
	}, page)
	if err != nil {
		return nil, err
	}
	enriched, err := s.enrichPage(ctx, viewer, tmdbPage)
	if err != nil {
		return nil, err
	}
	return &DiscoverBrowseResponse{
		Kind:        "studio",
		Slug:        studio.Slug,
		DisplayName: studio.DisplayName,
		BrandColor:  studio.BrandColor,
		LogoURL:     s.resolveLogoURL(ctx, s.companyLogos, studio.TMDBID, "company", studio.Slug),
		MediaType:   MediaTypeMovie,
		Sort:        sortKey,
		Page:        enriched.Page,
		TotalPages:  enriched.TotalPages,
		Results:     enriched.Results,
	}, nil
}

// BrowseNetwork returns a page of series from a bundled TV network.
func (s *Service) BrowseNetwork(ctx context.Context, viewer Viewer, slug, sort string, page int) (*DiscoverBrowseResponse, error) {
	network, ok := FindNetworkBySlug(slug)
	if !ok {
		return nil, ErrNotFound
	}
	tmdbSort, sortKey, err := normalizeBrowseSort(sort, "tv")
	if err != nil {
		return nil, err
	}
	tmdbPage, err := s.tmdb.DiscoverPage(ctx, "tv", tmdb.DiscoverParams{
		SortBy:       tmdbSort,
		WithNetworks: []int{network.TMDBID},
		VoteCountGte: voteCountFloorForSort(sortKey),
	}, page)
	if err != nil {
		return nil, err
	}
	enriched, err := s.enrichPage(ctx, viewer, tmdbPage)
	if err != nil {
		return nil, err
	}
	return &DiscoverBrowseResponse{
		Kind:        "network",
		Slug:        network.Slug,
		DisplayName: network.DisplayName,
		BrandColor:  network.BrandColor,
		LogoURL:     s.resolveLogoURL(ctx, s.networkLogos, network.TMDBID, "network", network.Slug),
		MediaType:   MediaTypeSeries,
		Sort:        sortKey,
		Page:        enriched.Page,
		TotalPages:  enriched.TotalPages,
		Results:     enriched.Results,
	}, nil
}

// BrowseGenre returns a page of movies or series from a bundled genre.
// Requires a media_type. For genres with SeriesID = 0 a media_type=series
// call returns ErrInvalidInput.
func (s *Service) BrowseGenre(ctx context.Context, viewer Viewer, slug string, rawMediaType MediaType, sort string, page int) (*DiscoverBrowseResponse, error) {
	genre, ok := FindGenreBySlug(slug)
	if !ok {
		return nil, ErrNotFound
	}
	mediaType, err := normalizeMediaType(rawMediaType)
	if err != nil {
		return nil, fmt.Errorf("%w: media_type is required for genre browse", ErrInvalidInput)
	}
	var (
		tmdbMediaType string
		genreID       int
	)
	switch mediaType {
	case MediaTypeMovie:
		tmdbMediaType = "movie"
		genreID = genre.MovieID
	case MediaTypeSeries:
		tmdbMediaType = "tv"
		genreID = genre.SeriesID
	}
	if genreID == 0 {
		return nil, fmt.Errorf("%w: %s has no %s equivalent", ErrInvalidInput, slug, mediaType)
	}
	tmdbSort, sortKey, err := normalizeBrowseSort(sort, tmdbMediaType)
	if err != nil {
		return nil, err
	}
	tmdbPage, err := s.tmdb.DiscoverPage(ctx, tmdbMediaType, tmdb.DiscoverParams{
		SortBy:       tmdbSort,
		WithGenres:   []int{genreID},
		VoteCountGte: voteCountFloorForSort(sortKey),
	}, page)
	if err != nil {
		return nil, err
	}
	enriched, err := s.enrichPage(ctx, viewer, tmdbPage)
	if err != nil {
		return nil, err
	}
	return &DiscoverBrowseResponse{
		Kind:        "genre",
		Slug:        genre.Slug,
		DisplayName: genre.DisplayName,
		MediaType:   mediaType,
		Sort:        sortKey,
		Page:        enriched.Page,
		TotalPages:  enriched.TotalPages,
		Results:     enriched.Results,
	}, nil
}

// normalizeBrowseSort accepts the API sort key, returns the TMDB sort_by
// string, the API-facing sort key (after defaulting), and an error if the
// sort key is unrecognized. TMDB tv discover uses first_air_date instead
// of primary_release_date for release_date sort.
func normalizeBrowseSort(sort, tmdbMediaType string) (string, string, error) {
	if sort == "" {
		sort = defaultBrowseSort
	}
	tmdbSort, ok := validBrowseSorts[sort]
	if !ok {
		return "", "", fmt.Errorf("%w: unknown sort %q", ErrInvalidInput, sort)
	}
	if sort == "release_date" && tmdbMediaType == "tv" {
		tmdbSort = "first_air_date.desc"
	}
	return tmdbSort, sort, nil
}

// voteCountFloorForSort applies vote_count.gte=100 to vote_average sorts so
// the top of the list is not dominated by titles with 1 perfect vote.
func voteCountFloorForSort(sortKey string) int {
	if sortKey == "vote_average" {
		return 100
	}
	return 0
}
```

- [ ] **Step 4: Verify ErrNotFound and ErrInvalidInput exist**

```bash
grep -n "ErrNotFound\|ErrInvalidInput" internal/requests/errors.go
```

Both should already exist (used by `GetDetail` and `Discover`). If not, add them to `errors.go`.

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/requests/ -v
```

Expected: All new Browse tests PASS; all existing tests still PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/requests/discover_brand.go internal/requests/service_test.go
git commit -m "feat(requests): add BrowseStudio/BrowseNetwork/BrowseGenre service methods"
```

---

## Task 8: HTTP handlers for the 6 new endpoints

**Files:**
- Modify: `internal/api/handlers/requests.go` (extend `RequestService` interface; add 6 handler methods)
- Create or modify: `internal/api/handlers/requests_test.go` — check first:

```bash
ls internal/api/handlers/requests_test.go 2>/dev/null && echo exists || echo missing
```

- [ ] **Step 1: Write the failing tests**

If `requests_test.go` exists, append. Otherwise create it with the following preamble + tests. Read the existing file first if present to match style and imports.

```go
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// fakeRequestService implements the handlers.RequestService interface for
// tests. Each method returns a value plus an error injectable by tests.
type fakeRequestService struct {
	listStudiosFn  func() ([]mediarequests.DiscoverBrandCard, error)
	listNetworksFn func() ([]mediarequests.DiscoverBrandCard, error)
	listGenresFn   func() ([]mediarequests.DiscoverBrandCard, error)
	browseFn       func(kind, slug string, mediaType mediarequests.MediaType, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
}

// ... only the new methods are implemented here. Tests that need other
// methods (Search, Discover, etc.) construct a separate fake.

func (f *fakeRequestService) ListStudios(_ context.Context, _ mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error) {
	if f.listStudiosFn != nil {
		return f.listStudiosFn()
	}
	return nil, nil
}

func (f *fakeRequestService) ListNetworks(_ context.Context, _ mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error) {
	if f.listNetworksFn != nil {
		return f.listNetworksFn()
	}
	return nil, nil
}

func (f *fakeRequestService) ListGenres(_ context.Context, _ mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error) {
	if f.listGenresFn != nil {
		return f.listGenresFn()
	}
	return nil, nil
}

func (f *fakeRequestService) BrowseStudio(_ context.Context, _ mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error) {
	return f.browseFn("studio", slug, mediarequests.MediaTypeMovie, sort, page)
}

func (f *fakeRequestService) BrowseNetwork(_ context.Context, _ mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error) {
	return f.browseFn("network", slug, mediarequests.MediaTypeSeries, sort, page)
}

func (f *fakeRequestService) BrowseGenre(_ context.Context, _ mediarequests.Viewer, slug string, mediaType mediarequests.MediaType, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error) {
	return f.browseFn("genre", slug, mediaType, sort, page)
}

// All other RequestService methods stubbed to return zero values so the
// fake satisfies the full interface. These should not be exercised by
// the discover/browse tests.
func (f *fakeRequestService) Search(context.Context, mediarequests.Viewer, string, mediarequests.MediaType, int) (*mediarequests.MediaPage, error) {
	return nil, nil
}
func (f *fakeRequestService) Discover(context.Context, mediarequests.Viewer, string, int) (*mediarequests.DiscoverySection, error) {
	return nil, nil
}
func (f *fakeRequestService) DiscoverAll(context.Context, mediarequests.Viewer) ([]mediarequests.DiscoverySection, error) {
	return nil, nil
}
func (f *fakeRequestService) GetDetail(context.Context, mediarequests.Viewer, mediarequests.MediaType, int) (*mediarequests.MediaDetail, error) {
	return nil, nil
}
func (f *fakeRequestService) CreateRequest(context.Context, mediarequests.Viewer, mediarequests.CreateRequestInput) (*mediarequests.Request, error) {
	return nil, nil
}
func (f *fakeRequestService) ListMine(context.Context, mediarequests.Viewer, mediarequests.ListFilter) ([]*mediarequests.Request, error) {
	return nil, nil
}
func (f *fakeRequestService) ListAdmin(context.Context, mediarequests.Viewer, mediarequests.ListFilter) ([]*mediarequests.Request, error) {
	return nil, nil
}
func (f *fakeRequestService) GetRequest(context.Context, mediarequests.Viewer, string) (*mediarequests.Request, error) {
	return nil, nil
}
func (f *fakeRequestService) Approve(context.Context, mediarequests.Viewer, string) (*mediarequests.Request, error) {
	return nil, nil
}
func (f *fakeRequestService) Decline(context.Context, mediarequests.Viewer, string, string) (*mediarequests.Request, error) {
	return nil, nil
}
func (f *fakeRequestService) Retry(context.Context, mediarequests.Viewer, string) (*mediarequests.Request, error) {
	return nil, nil
}
func (f *fakeRequestService) GetSettings(context.Context, mediarequests.Viewer) (mediarequests.Settings, error) {
	return mediarequests.Settings{}, nil
}
func (f *fakeRequestService) UpdateSettings(context.Context, mediarequests.Viewer, mediarequests.Settings) (mediarequests.Settings, error) {
	return mediarequests.Settings{}, nil
}
func (f *fakeRequestService) GetUserLimit(context.Context, mediarequests.Viewer, int) (*mediarequests.UserLimit, error) {
	return nil, nil
}
func (f *fakeRequestService) UpsertUserLimit(context.Context, mediarequests.Viewer, mediarequests.UserLimit) (*mediarequests.UserLimit, error) {
	return nil, nil
}
func (f *fakeRequestService) ListIntegrations(context.Context, mediarequests.Viewer) ([]mediarequests.Integration, error) {
	return nil, nil
}
func (f *fakeRequestService) UpsertIntegration(context.Context, mediarequests.Viewer, mediarequests.Integration) (*mediarequests.Integration, error) {
	return nil, nil
}
func (f *fakeRequestService) LoadIntegrationOptions(context.Context, mediarequests.Viewer, mediarequests.Integration) (*mediarequests.IntegrationOptions, error) {
	return nil, nil
}

// authedRequest is a tiny helper that injects a viewer context. The exact
// mechanism is project-specific — match whatever the existing request
// tests use. If no test helper exists yet, use the dev-friendly approach
// of adding the viewer to context via apimw.WithViewer or equivalent.
func authedRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	// Inject a viewer the request handler expects. Reference how the existing
	// HandleSearch tests do this (if any) — otherwise the simplest path is
	// to add a small helper in handlers/testing that sets the same context
	// key the production middleware sets. See `requestViewer` in this package.
	return req
}

func TestHandleListStudiosReturnsJSON(t *testing.T) {
	logo := "https://image.tmdb.org/t/p/w300/x.png"
	svc := &fakeRequestService{
		listStudiosFn: func() ([]mediarequests.DiscoverBrandCard, error) {
			return []mediarequests.DiscoverBrandCard{
				{TMDBID: 420, Slug: "marvel-studios", DisplayName: "Marvel Studios", BrandColor: "#ed1d24", LogoURL: &logo},
			}, nil
		},
	}
	h := NewRequestsHandler(svc)

	rec := httptest.NewRecorder()
	h.HandleListStudios(rec, authedRequest("GET", "/api/v1/requests/discover/studios"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Studios []mediarequests.DiscoverBrandCard `json:"studios"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Studios) != 1 || body.Studios[0].Slug != "marvel-studios" {
		t.Errorf("studios = %+v", body.Studios)
	}
}

func TestHandleBrowseStudioRejectsUnknownSort(t *testing.T) {
	svc := &fakeRequestService{
		browseFn: func(kind, slug string, _ mediarequests.MediaType, sort string, _ int) (*mediarequests.DiscoverBrowseResponse, error) {
			return nil, mediarequests.ErrInvalidInput
		},
	}
	h := NewRequestsHandler(svc)

	req := authedRequest("GET", "/api/v1/requests/discover/browse/studio/marvel-studios?sort=garbage")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("slug", "marvel-studios")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleBrowseStudio(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBrowseStudioUnknownSlugReturns404(t *testing.T) {
	svc := &fakeRequestService{
		browseFn: func(string, string, mediarequests.MediaType, string, int) (*mediarequests.DiscoverBrowseResponse, error) {
			return nil, mediarequests.ErrNotFound
		},
	}
	h := NewRequestsHandler(svc)

	req := authedRequest("GET", "/api/v1/requests/discover/browse/studio/ghosts")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("slug", "ghosts")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleBrowseStudio(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleBrowseGenreRequiresMediaType(t *testing.T) {
	svc := &fakeRequestService{
		browseFn: func(_ string, _ string, mt mediarequests.MediaType, _ string, _ int) (*mediarequests.DiscoverBrowseResponse, error) {
			if strings.TrimSpace(string(mt)) == "" {
				return nil, mediarequests.ErrInvalidInput
			}
			return &mediarequests.DiscoverBrowseResponse{Kind: "genre"}, nil
		},
	}
	h := NewRequestsHandler(svc)

	req := authedRequest("GET", "/api/v1/requests/discover/browse/genre/action")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("slug", "action")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.HandleBrowseGenre(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
```

> **Important about `authedRequest`:** the existing request handlers use a `requestViewer` helper that pulls a viewer struct from the request context (look at `internal/api/handlers/requests.go:HandleSearch` and adjacent helpers). Find how *existing* request handler tests stub the viewer (or how middleware injects it) before finishing this step. If there is no existing helper, add a minimal one to the test file that mirrors the middleware contract — usually `apimw.WithProfileContext(...)` or similar. Do not invent a fake context key; reuse the production one.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/api/handlers/ -run "TestHandleListStudios|TestHandleBrowse" -v
```

Expected: FAIL — handlers undefined; `ErrInvalidInput` / `ErrNotFound` constants are reused from `mediarequests`.

- [ ] **Step 3: Extend the RequestService interface**

In `internal/api/handlers/requests.go`, modify the `RequestService` interface (lines 17-38) to add six methods. Keep the existing methods and append:

```go
type RequestService interface {
	Search(ctx context.Context, viewer mediarequests.Viewer, query string, mediaType mediarequests.MediaType, page int) (*mediarequests.MediaPage, error)
	Discover(ctx context.Context, viewer mediarequests.Viewer, section string, page int) (*mediarequests.DiscoverySection, error)
	DiscoverAll(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverySection, error)
	GetDetail(ctx context.Context, viewer mediarequests.Viewer, mediaType mediarequests.MediaType, tmdbID int) (*mediarequests.MediaDetail, error)
	CreateRequest(ctx context.Context, viewer mediarequests.Viewer, input mediarequests.CreateRequestInput) (*mediarequests.Request, error)
	ListMine(ctx context.Context, viewer mediarequests.Viewer, filter mediarequests.ListFilter) ([]*mediarequests.Request, error)
	ListAdmin(ctx context.Context, viewer mediarequests.Viewer, filter mediarequests.ListFilter) ([]*mediarequests.Request, error)
	GetRequest(ctx context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error)
	Approve(ctx context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error)
	Decline(ctx context.Context, viewer mediarequests.Viewer, id, reason string) (*mediarequests.Request, error)
	Retry(ctx context.Context, viewer mediarequests.Viewer, id string) (*mediarequests.Request, error)
	GetSettings(ctx context.Context, viewer mediarequests.Viewer) (mediarequests.Settings, error)
	UpdateSettings(ctx context.Context, viewer mediarequests.Viewer, settings mediarequests.Settings) (mediarequests.Settings, error)
	GetUserLimit(ctx context.Context, viewer mediarequests.Viewer, userID int) (*mediarequests.UserLimit, error)
	UpsertUserLimit(ctx context.Context, viewer mediarequests.Viewer, limit mediarequests.UserLimit) (*mediarequests.UserLimit, error)
	ListIntegrations(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.Integration, error)
	UpsertIntegration(ctx context.Context, viewer mediarequests.Viewer, integration mediarequests.Integration) (*mediarequests.Integration, error)
	LoadIntegrationOptions(ctx context.Context, viewer mediarequests.Viewer, integration mediarequests.Integration) (*mediarequests.IntegrationOptions, error)

	ListStudios(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error)
	ListNetworks(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error)
	ListGenres(ctx context.Context, viewer mediarequests.Viewer) ([]mediarequests.DiscoverBrandCard, error)
	BrowseStudio(ctx context.Context, viewer mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
	BrowseNetwork(ctx context.Context, viewer mediarequests.Viewer, slug, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
	BrowseGenre(ctx context.Context, viewer mediarequests.Viewer, slug string, mediaType mediarequests.MediaType, sort string, page int) (*mediarequests.DiscoverBrowseResponse, error)
}
```

- [ ] **Step 4: Implement the six handler methods**

Append to `internal/api/handlers/requests.go`:

```go
func (h *RequestsHandler) HandleListStudios(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	studios, err := h.service.ListStudios(r.Context(), viewer)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Studios []mediarequests.DiscoverBrandCard `json:"studios"`
	}{Studios: studios})
}

func (h *RequestsHandler) HandleListNetworks(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	networks, err := h.service.ListNetworks(r.Context(), viewer)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Networks []mediarequests.DiscoverBrandCard `json:"networks"`
	}{Networks: networks})
}

func (h *RequestsHandler) HandleListGenres(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	genres, err := h.service.ListGenres(r.Context(), viewer)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Genres []mediarequests.DiscoverBrandCard `json:"genres"`
	}{Genres: genres})
}

func (h *RequestsHandler) HandleBrowseStudio(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	page, ok := parsePositiveIntQuery(w, r, "page", 1)
	if !ok {
		return
	}
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	sort := strings.TrimSpace(r.URL.Query().Get("sort"))
	resp, err := h.service.BrowseStudio(r.Context(), viewer, slug, sort, page)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *RequestsHandler) HandleBrowseNetwork(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	page, ok := parsePositiveIntQuery(w, r, "page", 1)
	if !ok {
		return
	}
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	sort := strings.TrimSpace(r.URL.Query().Get("sort"))
	resp, err := h.service.BrowseNetwork(r.Context(), viewer, slug, sort, page)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *RequestsHandler) HandleBrowseGenre(w http.ResponseWriter, r *http.Request) {
	viewer, ok := requestViewer(w, r, true)
	if !ok {
		return
	}
	page, ok := parsePositiveIntQuery(w, r, "page", 1)
	if !ok {
		return
	}
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	sort := strings.TrimSpace(r.URL.Query().Get("sort"))
	mediaType := mediarequests.MediaType(strings.TrimSpace(r.URL.Query().Get("media_type")))
	resp, err := h.service.BrowseGenre(r.Context(), viewer, slug, mediaType, sort, page)
	if err != nil {
		writeRequestServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
```

- [ ] **Step 5: Confirm `writeRequestServiceError` maps ErrNotFound → 404 and ErrInvalidInput → 400**

```bash
grep -n "writeRequestServiceError\|ErrNotFound\|ErrInvalidInput" internal/api/handlers/requests.go | head
```

The existing helper should already map these — if not, look for an `errors.Is` block on the existing handlers and confirm it handles the same cases. If it doesn't, extend it now. (The existing `HandleGetDetail` returns 404 for `ErrNotFound`, so the helper almost certainly already maps that.)

- [ ] **Step 6: Run tests to verify they pass**

```bash
go test ./internal/api/handlers/ -v
```

Expected: All new handler tests PASS; existing tests still PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/api/handlers/requests.go internal/api/handlers/requests_test.go
git commit -m "feat(api): add discover brand and browse handlers"
```

---

## Task 9: Wire the 6 new routes in router.go

**Files:**
- Modify: `internal/api/router.go:1394-1403` (the existing `requestHandler` route block)

- [ ] **Step 1: Add the routes**

In `internal/api/router.go`, find the existing block (around line 1394):

```go
if requestHandler != nil {
	r.Route("/requests", func(r chi.Router) {
		r.Get("/search", requestHandler.HandleSearch)
		r.Get("/discover", requestHandler.HandleDiscover)
		r.Get("/discover/{section}", requestHandler.HandleDiscoverSection)
		r.Get("/detail/{media_type}/{tmdb_id}", requestHandler.HandleGetDetail)
		r.Post("/", requestHandler.HandleCreate)
		r.Get("/mine", requestHandler.HandleListMine)
		r.Get("/{id}", requestHandler.HandleGet)
	})
}
```

Insert four new routes **before** the catch-all `/{id}` (so chi doesn't bind `studios` / `networks` / `genres` / `browse` to the `{id}` route):

```go
if requestHandler != nil {
	r.Route("/requests", func(r chi.Router) {
		r.Get("/search", requestHandler.HandleSearch)
		r.Get("/discover", requestHandler.HandleDiscover)
		r.Get("/discover/studios", requestHandler.HandleListStudios)
		r.Get("/discover/networks", requestHandler.HandleListNetworks)
		r.Get("/discover/genres", requestHandler.HandleListGenres)
		r.Get("/discover/browse/studio/{slug}", requestHandler.HandleBrowseStudio)
		r.Get("/discover/browse/network/{slug}", requestHandler.HandleBrowseNetwork)
		r.Get("/discover/browse/genre/{slug}", requestHandler.HandleBrowseGenre)
		r.Get("/discover/{section}", requestHandler.HandleDiscoverSection)
		r.Get("/detail/{media_type}/{tmdb_id}", requestHandler.HandleGetDetail)
		r.Post("/", requestHandler.HandleCreate)
		r.Get("/mine", requestHandler.HandleListMine)
		r.Get("/{id}", requestHandler.HandleGet)
	})
}
```

> **Route ordering matters in chi:** `/discover/studios` must come *before* `/discover/{section}` so the literal route wins. Same for the browse routes vs the trailing catch-all `/{id}`.

- [ ] **Step 2: Build to confirm the router compiles**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 3: Run the full request-system test suite**

```bash
go test ./internal/requests/... ./internal/api/... ./internal/metadata/tmdb/...
```

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/api/router.go
git commit -m "feat(api): wire discover studios/networks/genres routes"
```

---

## Task 10: Backend smoke test (manual)

**Files:** none modified.

- [ ] **Step 1: Start the backend in dev mode**

```bash
make dev-backend
```

In another terminal (or the same one once it's daemonized), capture a request token following whatever the local dev login flow looks like (the worktree should already be configured with `make dev-*` working).

- [ ] **Step 2: Probe the three list endpoints**

```bash
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/requests/discover/studios | jq '.studios | length'
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/requests/discover/networks | jq '.networks | length'
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/requests/discover/genres | jq '.genres | length'
```

Expected: 10, 10, 8.

Inspect a single studio entry:

```bash
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/requests/discover/studios | jq '.studios[0]'
```

Expected: a card with non-empty `logo_url` after the first call resolves it.

- [ ] **Step 3: Probe a browse endpoint**

```bash
curl -s -H "Authorization: Bearer $TOKEN" "http://localhost:8080/api/v1/requests/discover/browse/studio/marvel-studios?page=1&sort=popularity" | jq '.results | length, .page, .total_pages'
```

Expected: non-zero result count; page=1; total_pages > 1.

- [ ] **Step 4: Probe genre browse with each media type and an unsupported series case**

```bash
curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer $TOKEN" "http://localhost:8080/api/v1/requests/discover/browse/genre/action?media_type=movie"
curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer $TOKEN" "http://localhost:8080/api/v1/requests/discover/browse/genre/action?media_type=series"
curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer $TOKEN" "http://localhost:8080/api/v1/requests/discover/browse/genre/horror?media_type=series"
curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer $TOKEN" "http://localhost:8080/api/v1/requests/discover/browse/studio/not-real-slug"
```

Expected: 200, 200, 400, 404.

- [ ] **Step 5: Stop the dev backend**

```bash
# Use whatever mechanism `make dev-backend` uses to stop (Ctrl-C in its tmux pane, etc.).
```

No commit for this step — manual verification only.

---

## Task 11: Frontend types

**Files:**
- Modify: `web/src/api/types.ts`

- [ ] **Step 1: Add the new types**

Append to `web/src/api/types.ts` (find an appropriate location near the existing request types — likely after `RequestMediaResult` / `RequestMediaPage`):

```typescript
export interface DiscoverBrandCard {
  tmdb_id?: number;
  slug: string;
  display_name: string;
  brand_color?: string;
  logo_url?: string | null;
  gradient_from?: string;
  gradient_to?: string;
  series_supported?: boolean;
}

export interface DiscoverStudiosResponse {
  studios: DiscoverBrandCard[];
}

export interface DiscoverNetworksResponse {
  networks: DiscoverBrandCard[];
}

export interface DiscoverGenresResponse {
  genres: DiscoverBrandCard[];
}

export type DiscoverBrowseKind = "studio" | "network" | "genre";

export interface DiscoverBrowseResponse {
  kind: DiscoverBrowseKind;
  slug: string;
  display_name: string;
  brand_color?: string;
  logo_url?: string | null;
  media_type: RequestMediaType;
  sort: "popularity" | "vote_average" | "release_date";
  page: number;
  total_pages: number;
  results: RequestMediaResult[];
}
```

Make sure `RequestMediaType` and `RequestMediaResult` are already exported from this file (they should be — they're used by the existing search/discover surface).

- [ ] **Step 2: Verify types compile**

```bash
cd web && pnpm tsc --noEmit
```

Expected: no errors. If the existing `RequestMediaType` is not exported, export it now (small one-line change at its definition).

- [ ] **Step 3: Commit**

```bash
git add web/src/api/types.ts
git commit -m "feat(web): add discover brand and browse response types"
```

---

## Task 12: Frontend query keys and hooks

**Files:**
- Modify: `web/src/hooks/queries/keys.ts`
- Modify: `web/src/hooks/queries/requests.ts`

- [ ] **Step 1: Inspect existing requestKeys**

```bash
grep -n "requestKeys" web/src/hooks/queries/keys.ts
```

Read the section that defines `requestKeys` so the new helpers match style.

- [ ] **Step 2: Add the new key builders**

In `web/src/hooks/queries/keys.ts`, extend the `requestKeys` object to include:

```typescript
discoverStudios: () => [...requestKeys.all, "discover", "studios"] as const,
discoverNetworks: () => [...requestKeys.all, "discover", "networks"] as const,
discoverGenres: () => [...requestKeys.all, "discover", "genres"] as const,
discoverBrowse: (
  kind: "studio" | "network" | "genre",
  slug: string,
  mediaType: string | undefined,
  sort: string,
  page: number,
) =>
  [
    ...requestKeys.all,
    "discover",
    "browse",
    kind,
    slug,
    mediaType ?? "",
    sort,
    page,
  ] as const,
```

Match the style of the existing entries — they likely use the `[...requestKeys.all, "..."] as const` pattern.

- [ ] **Step 3: Add the new hooks**

In `web/src/hooks/queries/requests.ts`, append after the existing `useRequestDiscovery` hook:

```typescript
const DISCOVER_BRAND_STALE_TIME = 24 * 60 * 60 * 1000; // 24h, matches server cache
const BROWSE_STALE_TIME = 60 * 1000; // 60s, keeps request state fresh

export function useDiscoverStudios() {
  return useQuery({
    queryKey: requestKeys.discoverStudios(),
    queryFn: () =>
      api<DiscoverStudiosResponse>("/requests/discover/studios").then((data) => data.studios ?? []),
    staleTime: DISCOVER_BRAND_STALE_TIME,
  });
}

export function useDiscoverNetworks() {
  return useQuery({
    queryKey: requestKeys.discoverNetworks(),
    queryFn: () =>
      api<DiscoverNetworksResponse>("/requests/discover/networks").then(
        (data) => data.networks ?? [],
      ),
    staleTime: DISCOVER_BRAND_STALE_TIME,
  });
}

export function useDiscoverGenres() {
  return useQuery({
    queryKey: requestKeys.discoverGenres(),
    queryFn: () =>
      api<DiscoverGenresResponse>("/requests/discover/genres").then((data) => data.genres ?? []),
    staleTime: DISCOVER_BRAND_STALE_TIME,
  });
}

export interface UseRequestBrowseArgs {
  kind: DiscoverBrowseKind;
  slug: string;
  mediaType?: RequestMediaType;
  sort: "popularity" | "vote_average" | "release_date";
  page: number;
}

export function useRequestBrowse({ kind, slug, mediaType, sort, page }: UseRequestBrowseArgs) {
  return useQuery({
    queryKey: requestKeys.discoverBrowse(kind, slug, mediaType, sort, page),
    queryFn: () => {
      const params = new URLSearchParams({ sort, page: String(page) });
      if (mediaType) params.set("media_type", mediaType);
      return api<DiscoverBrowseResponse>(
        `/requests/discover/browse/${kind}/${encodeURIComponent(slug)}?${params}`,
      );
    },
    enabled: slug.trim().length > 0 && (kind !== "genre" || Boolean(mediaType)),
    staleTime: BROWSE_STALE_TIME,
  });
}
```

Update the imports at the top of the file to include the new types:

```typescript
import type {
  // ...existing imports...
  DiscoverBrandCard,
  DiscoverBrowseKind,
  DiscoverBrowseResponse,
  DiscoverGenresResponse,
  DiscoverNetworksResponse,
  DiscoverStudiosResponse,
  RequestMediaType,
} from "@/api/types";
```

- [ ] **Step 4: Verify types compile**

```bash
cd web && pnpm tsc --noEmit
```

Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add web/src/hooks/queries/keys.ts web/src/hooks/queries/requests.ts
git commit -m "feat(web): add discover brand and browse query hooks"
```

---

## Task 13: BrandCard and BrandCarousel components

**Files:**
- Create: `web/src/components/BrandCard.tsx`
- Create: `web/src/components/BrandCarousel.tsx`

- [ ] **Step 1: Build BrandCard**

Create `web/src/components/BrandCard.tsx`:

```typescript
import { useNavigate } from "react-router";
import type { DiscoverBrandCard, DiscoverBrowseKind } from "@/api/types";
import { cn } from "@/lib/utils";

interface BrandCardProps {
  kind: DiscoverBrowseKind;
  card: DiscoverBrandCard;
  defaultMediaTypeForGenre?: "movie" | "series";
}

export default function BrandCard({ kind, card, defaultMediaTypeForGenre = "movie" }: BrandCardProps) {
  const navigate = useNavigate();

  function handleClick() {
    const base = `/requests/browse/${kind}/${encodeURIComponent(card.slug)}`;
    if (kind === "genre") {
      const initial = card.series_supported && defaultMediaTypeForGenre === "series" ? "series" : "movie";
      navigate(`${base}?media_type=${initial}`);
    } else {
      navigate(base);
    }
  }

  const isGenre = kind === "genre";
  const background = isGenre
    ? `linear-gradient(135deg, ${card.gradient_from ?? "#475569"}, ${card.gradient_to ?? "#0f172a"})`
    : card.brand_color || "#1f2937";

  return (
    <button
      type="button"
      onClick={handleClick}
      aria-label={card.display_name}
      className={cn(
        "group relative flex h-20 w-[140px] flex-none items-center justify-center overflow-hidden rounded-lg shadow-sm",
        "ring-1 ring-white/5 transition hover:ring-white/30 focus:outline-none focus:ring-2 focus:ring-white",
      )}
      style={{ background }}
    >
      {!isGenre && card.logo_url ? (
        <img
          src={card.logo_url}
          alt={card.display_name}
          loading="lazy"
          className="max-h-[60%] max-w-[80%] object-contain"
        />
      ) : (
        <span className="px-2 text-center text-sm font-semibold leading-tight text-white drop-shadow">
          {card.display_name}
        </span>
      )}
    </button>
  );
}
```

- [ ] **Step 2: Build BrandCarousel**

Create `web/src/components/BrandCarousel.tsx`:

```typescript
import type { DiscoverBrandCard, DiscoverBrowseKind } from "@/api/types";
import { Skeleton } from "@/components/ui/skeleton";
import BrandCard from "./BrandCard";

interface BrandCarouselProps {
  kind: DiscoverBrowseKind;
  title: string;
  cards: DiscoverBrandCard[] | undefined;
  isLoading: boolean;
  isError: boolean;
  onRetry?: () => void;
}

export default function BrandCarousel({
  kind,
  title,
  cards,
  isLoading,
  isError,
  onRetry,
}: BrandCarouselProps) {
  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between px-4 sm:px-6 lg:px-10 xl:px-12">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">
          {title}
        </h2>
        {isError && onRetry && (
          <button
            type="button"
            onClick={onRetry}
            className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
          >
            Retry
          </button>
        )}
      </div>
      <div className="overflow-x-auto px-4 pb-2 sm:px-6 lg:px-10 xl:px-12">
        <div className="flex gap-3">
          {isLoading
            ? Array.from({ length: 8 }).map((_, idx) => (
                <Skeleton key={idx} className="h-20 w-[140px] flex-none rounded-lg" />
              ))
            : isError
              ? (
                <div className="text-xs text-muted-foreground">
                  Could not load {title.toLowerCase()}.
                </div>
              )
              : (cards ?? []).map((card) => (
                  <BrandCard key={card.slug} kind={kind} card={card} />
                ))}
        </div>
      </div>
    </section>
  );
}
```

- [ ] **Step 3: Verify types compile**

```bash
cd web && pnpm tsc --noEmit
```

Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add web/src/components/BrandCard.tsx web/src/components/BrandCarousel.tsx
git commit -m "feat(web): add BrandCard and BrandCarousel components"
```

---

## Task 14: Append Studios, Networks, Genres carousels to Requests.tsx

**Files:**
- Modify: `web/src/pages/Requests.tsx`

- [ ] **Step 1: Wire the new hooks at the top of the component**

In `web/src/pages/Requests.tsx`, near the existing hook calls (around line 122):

```typescript
const discovery = useRequestDiscovery();
const studios = useDiscoverStudios();
const networks = useDiscoverNetworks();
const genres = useDiscoverGenres();
const search = useRequestSearch(mediaType, submittedQuery, searchPage);
// ...
```

Update the import block at the top of the file to add the three new hooks:

```typescript
import {
  useCreateMediaRequest,
  useDiscoverGenres,
  useDiscoverNetworks,
  useDiscoverStudios,
  useMyMediaRequests,
  useRequestDiscovery,
  useRequestSearch,
} from "@/hooks/queries/requests";
import BrandCarousel from "@/components/BrandCarousel";
```

- [ ] **Step 2: Render the three new sections after the existing discovery carousels**

Find the discovery rendering block (around line 205, inside `TabsContent value="discover"`). After the existing `.map` that renders `DiscoverySectionRow` for each section, append three `BrandCarousel` instances:

```tsx
{(discovery.data ?? []).map((section) => (
  <DiscoverySectionRow
    key={section.key}
    section={section}
    // ...existing props...
  />
))}
<BrandCarousel
  kind="studio"
  title="Studios"
  cards={studios.data}
  isLoading={studios.isLoading}
  isError={studios.isError}
  onRetry={() => studios.refetch()}
/>
<BrandCarousel
  kind="network"
  title="Networks"
  cards={networks.data}
  isLoading={networks.isLoading}
  isError={networks.isError}
  onRetry={() => networks.refetch()}
/>
<BrandCarousel
  kind="genre"
  title="Genres"
  cards={genres.data}
  isLoading={genres.isLoading}
  isError={genres.isError}
  onRetry={() => genres.refetch()}
/>
```

- [ ] **Step 3: Verify the page compiles and renders**

```bash
cd web && pnpm tsc --noEmit
cd web && pnpm run lint
```

Expected: no type errors, no new lint warnings.

- [ ] **Step 4: Commit**

```bash
git add web/src/pages/Requests.tsx
git commit -m "feat(web): render studios/networks/genres carousels in Requests discover"
```

---

## Task 15: RequestBrowse page

**Files:**
- Create: `web/src/pages/RequestBrowse.tsx`

- [ ] **Step 1: Build the page**

Create `web/src/pages/RequestBrowse.tsx`:

```typescript
import { Link, useParams, useSearchParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import RequestPosterCard from "@/components/RequestPosterCard";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import {
  useCreateMediaRequest,
  useRequestBrowse,
} from "@/hooks/queries/requests";
import { requestInputFromMediaResult } from "@/lib/mediaRequests";
import type { DiscoverBrowseKind, RequestMediaResult, RequestMediaType } from "@/api/types";
import { cn } from "@/lib/utils";

const SORT_OPTIONS: { value: "popularity" | "vote_average" | "release_date"; label: string }[] = [
  { value: "popularity", label: "Popularity" },
  { value: "vote_average", label: "Rating" },
  { value: "release_date", label: "Release date" },
];

interface RequestBrowseProps {
  kind: DiscoverBrowseKind;
}

export default function RequestBrowse({ kind }: RequestBrowseProps) {
  const { slug = "" } = useParams<{ slug: string }>();
  const [searchParams, setSearchParams] = useSearchParams();

  const sort = (searchParams.get("sort") ?? "popularity") as
    | "popularity"
    | "vote_average"
    | "release_date";
  const page = Math.max(1, Number(searchParams.get("page") ?? "1") || 1);

  // Genre routes drive media_type from the URL; studio is always movie,
  // network is always series.
  const mediaTypeFromQuery = (searchParams.get("media_type") as RequestMediaType | null) ?? undefined;
  const mediaType: RequestMediaType | undefined =
    kind === "studio" ? "movie" : kind === "network" ? "series" : mediaTypeFromQuery ?? "movie";

  const browse = useRequestBrowse({ kind, slug, mediaType, sort, page });
  const createRequest = useCreateMediaRequest();

  const title = browse.data?.display_name ?? slug;
  useDocumentTitle(title ? `${title} – Requests` : "Requests");

  function updateSort(next: string) {
    const params = new URLSearchParams(searchParams);
    params.set("sort", next);
    params.set("page", "1");
    setSearchParams(params, { replace: true });
  }

  function updateMediaType(next: RequestMediaType) {
    const params = new URLSearchParams(searchParams);
    params.set("media_type", next);
    params.set("page", "1");
    setSearchParams(params, { replace: true });
  }

  function goToPage(next: number) {
    const params = new URLSearchParams(searchParams);
    params.set("page", String(next));
    setSearchParams(params, { replace: false });
    window.scrollTo({ top: 0, behavior: "smooth" });
  }

  function submitRequest(item: RequestMediaResult) {
    createRequest.mutate(requestInputFromMediaResult(item));
  }

  // We can't know from the browse response alone whether series is supported
  // for this genre (the browse endpoint only returns the data for the active
  // media_type). The carousel hides the click-through to series for
  // unsupported genres, so if a user lands here with media_type=series the
  // server already returned 400. We optimistically render both tabs and let
  // the backend 400 if a user manually edits the URL to ?media_type=series
  // for a genre that doesn't support it. v2 idea: pass series_supported
  // through the browse response so we can hide the tab proactively.

  const totalPages = browse.data?.total_pages ?? 0;
  const results = browse.data?.results ?? [];

  if (browse.isError) {
    // Surface 404s differently from generic failures.
    const status = (browse.error as { status?: number } | undefined)?.status;
    if (status === 404) {
      return (
        <div className="space-y-4 py-10 text-center">
          <p className="text-lg font-semibold">
            {kind === "studio" ? "Studio" : kind === "network" ? "Network" : "Genre"} not found.
          </p>
          <Link to="/requests" className="text-sm underline">
            Back to Requests
          </Link>
        </div>
      );
    }
  }

  return (
    <div className="space-y-6 py-6 sm:py-8">
      <div className="space-y-4 px-4 sm:px-6 lg:px-10 xl:px-12">
        <Link
          to="/requests"
          className="inline-flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="h-4 w-4" /> Back to Requests
        </Link>
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex items-center gap-4">
            <BrowseHeaderTile browse={browse.data} kind={kind} fallback={slug} />
            <div>
              <h1 className="text-2xl font-semibold">{title}</h1>
              <p className="text-sm text-muted-foreground">
                {browse.isLoading
                  ? "Loading…"
                  : results.length > 0
                    ? `Page ${page} of ${totalPages}`
                    : "No results."}
              </p>
            </div>
          </div>
          <Select value={sort} onValueChange={updateSort}>
            <SelectTrigger className="w-[180px]">
              <SelectValue placeholder="Sort" />
            </SelectTrigger>
            <SelectContent>
              {SORT_OPTIONS.map((opt) => (
                <SelectItem key={opt.value} value={opt.value}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {kind === "genre" && (
          <Tabs
            value={mediaType ?? "movie"}
            onValueChange={(value) => updateMediaType(value as RequestMediaType)}
          >
            <TabsList>
              <TabsTrigger value="movie">Movies</TabsTrigger>
              <TabsTrigger value="series">Series</TabsTrigger>
            </TabsList>
          </Tabs>
        )}
      </div>

      <div className="px-4 sm:px-6 lg:px-10 xl:px-12">
        {browse.isLoading ? (
          <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6">
            {Array.from({ length: 12 }).map((_, idx) => (
              <Skeleton key={idx} className="aspect-[2/3] w-full rounded-md" />
            ))}
          </div>
        ) : results.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            Nothing matched — try a different sort.
          </p>
        ) : (
          <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6">
            {results.map((item) => (
              <RequestPosterCard
                key={`${item.media_type}-${item.tmdb_id}`}
                item={item}
                onRequest={() => submitRequest(item)}
                isSubmitting={createRequest.isPending && createRequest.variables?.tmdb_id === item.tmdb_id}
              />
            ))}
          </div>
        )}
      </div>

      {totalPages > 1 && (
        <div className="flex items-center justify-center gap-3 px-4">
          <Button variant="outline" disabled={page <= 1} onClick={() => goToPage(page - 1)}>
            Prev
          </Button>
          <span className="text-sm text-muted-foreground">
            Page {page} of {totalPages}
          </span>
          <Button variant="outline" disabled={page >= totalPages} onClick={() => goToPage(page + 1)}>
            Next
          </Button>
        </div>
      )}
    </div>
  );
}

function BrowseHeaderTile({
  browse,
  kind,
  fallback,
}: {
  browse: DiscoverBrowseHeader | undefined;
  kind: DiscoverBrowseKind;
  fallback: string;
}) {
  if (!browse) {
    return (
      <div className="h-16 w-28 rounded-md bg-muted" aria-hidden />
    );
  }
  if (kind === "genre") {
    // Genre browse responses do not carry gradient hints (gradients live on
    // the bundle, returned only by the list endpoints). Use a neutral
    // background for the header tile here. The carousel card on /requests
    // shows the gradient.
    return (
      <div className="flex h-16 w-28 items-center justify-center rounded-md bg-muted text-center text-sm font-semibold text-foreground">
        {browse.display_name || fallback}
      </div>
    );
  }
  return (
    <div
      className={cn(
        "flex h-16 w-28 items-center justify-center overflow-hidden rounded-md",
      )}
      style={{ background: browse.brand_color || "#1f2937" }}
    >
      {browse.logo_url ? (
        <img
          src={browse.logo_url}
          alt={browse.display_name}
          className="max-h-[60%] max-w-[80%] object-contain"
        />
      ) : (
        <span className="px-2 text-center text-xs font-semibold text-white">
          {browse.display_name || fallback}
        </span>
      )}
    </div>
  );
}

type DiscoverBrowseHeader = {
  brand_color?: string;
  logo_url?: string | null;
  display_name?: string;
};
```

> The browse response does not carry `gradient_from` / `gradient_to` (those live on `DiscoverBrandCard` and are returned only by the list endpoints in Task 6). The genre browse header tile uses a neutral background; the carousel card on `/requests` is where the gradient shows. This keeps the response shapes consistent with the spec.

- [ ] **Step 2: Verify types compile**

```bash
cd web && pnpm tsc --noEmit
```

Expected: no type errors.

- [ ] **Step 3: Commit**

```bash
git add web/src/pages/RequestBrowse.tsx
git commit -m "feat(web): add RequestBrowse page for studio/network/genre browse"
```

---

## Task 16: Wire the 3 new routes in App.tsx

**Files:**
- Modify: `web/src/App.tsx`

- [ ] **Step 1: Import the new page**

In `web/src/App.tsx`, add to the imports near `import RequestDetail from "@/pages/RequestDetail";` (line 36):

```typescript
import RequestBrowse from "@/pages/RequestBrowse";
```

- [ ] **Step 2: Add the three routes**

In the route definitions, find the line for `/requests/:mediaType/:tmdbId` (around line 452) and insert three new routes after it:

```tsx
<Route path="/requests" element={<Requests />} />
<Route path="/requests/:mediaType/:tmdbId" element={<RequestDetail />} />
<Route path="/requests/browse/studio/:slug" element={<RequestBrowse kind="studio" />} />
<Route path="/requests/browse/network/:slug" element={<RequestBrowse kind="network" />} />
<Route path="/requests/browse/genre/:slug" element={<RequestBrowse kind="genre" />} />
```

> Order: place the browse routes *after* the existing `:mediaType/:tmdbId` route. Since `browse` is a literal path segment, React Router resolves the literal match first regardless of order, but keeping related routes together helps readability.

- [ ] **Step 3: Verify the app builds**

```bash
cd web && pnpm tsc --noEmit
cd web && pnpm run build
```

Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add web/src/App.tsx
git commit -m "feat(web): wire studio/network/genre browse routes"
```

---

## Task 17: Lint and full manual verification

**Files:** none modified.

- [ ] **Step 1: Run all linters**

```bash
make lint
cd web && pnpm run lint
cd web && pnpm run format:check
```

Expected: all PASS. If `format:check` reports diffs, run `pnpm run format` then re-stage and verify.

- [ ] **Step 2: Run the full Go test suite**

```bash
go test ./internal/requests/... ./internal/api/... ./internal/metadata/tmdb/...
```

Expected: all PASS.

- [ ] **Step 3: Manual verification in the browser**

```bash
make dev-backend # in one terminal
make dev-frontend # in another
```

In a browser, navigate to `http://localhost:5173/requests`. Verify:

- Three new carousels appear below the existing six.
- Studio cards show logos after the first 1-2 seconds (lazy fetch from TMDB).
- Genre cards show gradient + name.
- Clicking a studio card navigates to the browse page; results appear; sort dropdown works; pagination Prev/Next works.
- Clicking a genre card navigates to the genre browse page; Movies tab is selected by default; Series tab is hidden for Horror / Romance.
- An unknown slug (e.g., `/requests/browse/studio/garbage`) shows the "not found" message.

- [ ] **Step 4: If any cosmetic issue surfaces during manual verification, fix it, run lint, and amend the most recent commit OR create a follow-up commit**

If creating a follow-up commit:

```bash
git add <files>
git commit -m "fix(web): <one-line summary>"
```

- [ ] **Step 5: Final repo state check**

```bash
git log --oneline -20
git status
```

Expected: working tree clean; recent commits trace the implementation path. Confirm no stray `console.log`, debug prints, or temp files.

No commit for this task — it's all verification.

---

## Self-Review Notes

These were checked while writing the plan; resolved inline.

**Spec coverage:**
- All six service methods (List* + Browse*) → Tasks 6, 7
- All six handlers + routes → Tasks 8, 9
- TMDB client extensions (params + Discover + Get*) → Tasks 1, 2, 3
- Bundle + logo cache → Tasks 4, 5
- Frontend carousels, page, hooks, types, routes → Tasks 11-16
- Lint/manual verification → Task 17

**Type consistency:**
- `DiscoverBrandCard` and `DiscoverBrowseResponse` field names match across Go (`internal/requests/discover_brand.go`) and TypeScript (`web/src/api/types.ts`).
- Service method signatures match handler interface signatures (Task 8 Step 3).
- The `RequestService` interface in the handler file is the single source of truth for what the service must implement.

**Edge cases covered by tests:**
- Logo lookup failure → Task 6 test `TestListStudiosToleratesLogoLookupFailure`
- Genre without series → Tasks 4, 7 (`TestBrowseGenreSeriesRejectedWhenUnsupported`)
- Unknown slug → Task 7 (`TestBrowseStudioUnknownSlugReturnsNotFound`)
- Unknown sort → Task 7 (`TestBrowseStudioRejectsBadSort`)
- Missing media_type on genre → Task 7 (`TestBrowseGenreRequiresMediaType`)
- Logo cache TTL + singleflight + empty path → Task 5

**Known caveats deferred to runtime:**
- Bundled TMDB IDs are conventional defaults; verify a sample of them manually against `/company/{id}` and `/network/{id}` during Task 4 / Task 10 if uncertain. Adjust the constants if any are wrong — no test asserts a specific TMDB ID.
- The `BrowseHeaderTile` for genre browse uses a neutral background (not a gradient); the gradient appears on the carousel card on `/requests` where the bundle data flows through.
- **Browse response caching:** the spec mentions a 15-min "existing TMDB response cache wrapper" for browse results, but no such wrapper currently exists in the codebase (each `client.DiscoverSection` / `client.DiscoverPage` call hits TMDB directly). For v1 we lean on (a) react-query's 60s stale time on the frontend (Task 12), (b) TMDB's `retryAfterOrDefault` backoff for rate-limit recovery (existing in `tmdb/client.go`). If sustained load surfaces an issue, add a server-side LRU + TTL in front of `DiscoverPage` as a follow-up. This deviation is intentional and documented; do not block v1 on adding the wrapper.
- The Series tab on the genre browse page is always rendered (Task 15). For genres without TV equivalents (Horror, Romance), the carousel cards on `/requests` link to the movie tab by default, and the backend returns 400 for `media_type=series` — surfacing as an error toast if a user manually edits the URL. A future improvement could propagate `series_supported` through the browse response to hide the Series tab proactively.
