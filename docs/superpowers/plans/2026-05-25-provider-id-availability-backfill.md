# Provider ID Availability Backfill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make request availability and catalog matching work when local items are missing TMDB IDs, while repairing those missing TMDB IDs when a confident TVDB/IMDb cross-reference is found.

**Architecture:** Preserve TMDB as the public request identifier, but make internal presence checks provider-aware by hydrating TMDB results with external IDs and matching local catalog rows by TMDB, TVDB, or IMDb. Fix metadata candidate normalization so compatible provider results union their IDs instead of discarding richer candidates, then use existing refresh paths to repair already-matched catalog items. Add a small sibling plugin fix so TVDB title search emits remote IDs it already receives from TVDB.

**Tech Stack:** Go, PostgreSQL/pgx, existing Silo metadata/request/adminjob packages, TMDB client external ID endpoint, TVDB plugin provider package.

---

## Validated Findings

Commands assume the repository root is the cwd unless a task explicitly says to run from a sibling repository.

- Dev item `120983767174086659` is `The Rookie: Feds`, `type=series`, `status=matched`, with `tvdb_id=420105`, `imdb_id=tt18076310`, and empty `tmdb_id`.
- The corresponding `media_item_provider_ids` rows contain only `tvdb=420105` and `imdb=tt18076310`.
- Live TMDB resolves both `tvdb=420105` and `imdb=tt18076310` to TMDB TV ID `201992`.
- Live TVDB extended series data for `420105` includes a TMDB remote ID of `201992`.
- The request service currently checks availability with only `tmdb_id` through `requests.PresenceResolver.LookupTMDB`.
- `CreateRequest` checks availability before calling `enrichExternalIDs`, so a request can be created for an item already present locally by TVDB/IMDb.
- Metadata candidate normalization groups only exact provider ID fingerprints. A TVDB candidate with `{tvdb, imdb}` and a TMDB candidate with `{tmdb, tvdb, imdb}` remain separate, then source order can select the poorer TVDB candidate and prevent TMDB from being persisted.
- The TVDB plugin already fills remote IDs for direct ID and metadata paths, but `searchByTitle` ignores `SearchResult.RemoteIDs`.
- Quick library refresh does not schedule an item solely because `tmdb_id` is missing.

## File Structure

### Server Repository

- Modify `internal/metadata/match_candidates.go`: merge compatible candidates by overlapping non-conflicting canonical provider IDs and prefer richer provider ID sets during match scoring.
- Modify `internal/metadata/match_candidates_test.go`: cover compatible ID union and conflicting ID separation.
- Modify `internal/catalog/item_repo.go`: add provider-aware external ID lookup for enabled-library presence checks while preserving `LookupTMDBIDs`.
- Modify `internal/catalog/item_repo_test.go`: test the query helper used by provider-aware lookup.
- Modify `internal/catalog/provider_id_repo.go`: add a transactional `AttachTMDBID` repair helper that updates both `media_items.tmdb_id` and `media_item_provider_ids`.
- Modify `internal/catalog/provider_id_repo_test.go`: add normalization-level coverage for the new helper's input rules.
- Modify `internal/requests/presence.go`: replace TMDB-only presence resolution with candidate-based provider-aware presence and best-effort TMDB backfill.
- Create `internal/requests/presence_test.go`: test presence matching by TVDB and backfill behavior using fakes.
- Modify `internal/requests/service.go`: hydrate TMDB search/detail/create/reconcile candidates with external IDs before availability checks.
- Modify `internal/requests/service_test.go`: cover search availability, create blocking, and reconcile completion when only TVDB/IMDb is present locally.
- Modify `internal/metadata/refresh_debt.go`: add provider ID incomplete refresh debt.
- Modify `internal/metadata/refresh_debt_repo.go`: expose the new reason in metrics.
- Modify `internal/metadata/refresh_debt_test.go`: test provider ID incomplete debt.
- Modify `internal/adminjob/library_refresh.go`: include matched items missing TMDB IDs in quick library refresh.
- Modify `cmd/silo/main.go` and `internal/api/router.go`: wire `catalog.ProviderIDRepository` into request presence.

### TVDB Plugin Repository

- Modify `provider/provider.go`: fill remote IDs in title search results.
- Modify `provider/provider_test.go`: prove title search returns `tmdb` and `imdb` provider IDs when TVDB search payload includes `remote_ids`.

---

## Task 1: Merge Compatible Metadata Candidates

**Files:**
- Modify: `internal/metadata/match_candidates.go`
- Modify: `internal/metadata/match_candidates_test.go`

- [ ] **Step 1: Write failing tests for compatible provider ID union**

Add these cases to `TestNormalizeCandidates` in `internal/metadata/match_candidates_test.go`:

```go
{
	name: "merge compatible candidates with overlapping provider IDs",
	results: []SearchResult{
		{
			Name:        "The Rookie: Feds",
			Year:        2022,
			Provider:    "tvdb",
			ProviderIDs: map[string]string{"tvdb": "420105", "imdb": "tt18076310"},
		},
		{
			Name:        "The Rookie: Feds",
			Year:        2022,
			Provider:    "tmdb",
			ProviderIDs: map[string]string{"tmdb": "201992", "tvdb": "420105", "imdb": "tt18076310"},
		},
	},
	content: "series",
	wantLen: 1,
	check: func(t *testing.T, candidates []MatchCandidate) {
		c := candidates[0]
		if c.ProviderIDs["tmdb"] != "201992" {
			t.Fatalf("tmdb id = %q, want 201992", c.ProviderIDs["tmdb"])
		}
		if c.ProviderIDs["tvdb"] != "420105" || c.ProviderIDs["imdb"] != "tt18076310" {
			t.Fatalf("provider ids = %+v, want tvdb and imdb preserved", c.ProviderIDs)
		}
		if len(c.Sources) != 2 {
			t.Fatalf("sources = %+v, want two providers", c.Sources)
		}
	},
},
{
	name: "do not merge candidates with conflicting overlapping provider IDs",
	results: []SearchResult{
		{
			Name:        "Show A",
			Year:        2022,
			Provider:    "tvdb",
			ProviderIDs: map[string]string{"tvdb": "420105", "imdb": "tt18076310"},
		},
		{
			Name:        "Show B",
			Year:        2022,
			Provider:    "tmdb",
			ProviderIDs: map[string]string{"tmdb": "201992", "tvdb": "999999", "imdb": "tt18076310"},
		},
	},
	content: "series",
	wantLen: 2,
	check: func(t *testing.T, candidates []MatchCandidate) {
		if len(candidates) != 2 {
			t.Fatalf("len(candidates) = %d, want 2", len(candidates))
		}
	},
},
```

- [ ] **Step 2: Run the new tests and verify they fail**

Run:

```bash
go test ./internal/metadata -run TestNormalizeCandidates -count=1
```

Expected: FAIL because compatible candidates are still grouped by exact provider ID fingerprint.

- [ ] **Step 3: Implement compatible candidate grouping**

In `internal/metadata/match_candidates.go`, add these helpers near `normalizedKey`:

```go
var canonicalCandidateIDKeys = []string{"tmdb", "tvdb", "imdb"}

func compatibleProviderIDs(left, right map[string]string) bool {
	overlap := false
	for _, key := range canonicalCandidateIDKeys {
		lv := strings.TrimSpace(left[key])
		rv := strings.TrimSpace(right[key])
		if lv == "" || rv == "" {
			continue
		}
		if lv != rv {
			return false
		}
		overlap = true
	}
	return overlap
}

func providerIDRichness(ids map[string]string) int {
	score := 0
	for _, key := range canonicalCandidateIDKeys {
		if strings.TrimSpace(ids[key]) != "" {
			score++
		}
	}
	return score
}
```

Then change the bucket selection in `NormalizeCandidates` so it first reuses an existing compatible bucket before falling back to `normalizedKey`:

```go
key := ""
for _, existingKey := range ordered {
	if compatibleProviderIDs(buckets[existingKey].candidate.ProviderIDs, sr.ProviderIDs) {
		key = existingKey
		break
	}
}
if key == "" {
	key = normalizedKey(sr.ProviderIDs)
	if key == "" {
		key = sr.Provider + ":" + sr.Name + ":" + strings.Repeat("?", len(ordered))
	}
}
```

Update `scoreMatchCandidate` so richer canonical ID sets break ties without overpowering trusted ID correctness:

```go
if len(candidate.ProviderIDs) > 0 {
	score += 5
	score += float64(providerIDRichness(candidate.ProviderIDs))
}
```

- [ ] **Step 4: Run metadata tests**

Run:

```bash
go test ./internal/metadata -run 'TestNormalizeCandidates|TestSelectInitialMatchCandidate|TestRefresh' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/metadata/match_candidates.go internal/metadata/match_candidates_test.go
git commit -m "fix(metadata): merge compatible provider ids"
```

---

## Task 2: Add Provider-Aware Catalog Presence Lookup

**Files:**
- Modify: `internal/catalog/item_repo.go`
- Modify: `internal/catalog/item_repo_test.go`
- Modify: `internal/catalog/provider_id_repo.go`
- Modify: `internal/catalog/provider_id_repo_test.go`

- [ ] **Step 1: Add failing query-shape test for provider-aware lookup**

In `internal/catalog/item_repo_test.go`, add:

```go
func TestLookupExternalIDsSQLChecksProviderTableAndDirectColumns(t *testing.T) {
	sql := lookupExternalIDsSQL()

	for _, want := range []string{
		"FROM requested r",
		"JOIN media_item_provider_ids mip",
		"mip.provider = r.provider",
		"mip.provider_id = r.provider_id",
		"COALESCE(mi.tmdb_id, '') = r.provider_id",
		"COALESCE(mi.tvdb_id, '') = r.provider_id",
		"COALESCE(mi.imdb_id, '') = r.provider_id",
		"JOIN media_folders mf ON mf.id = mil.media_folder_id",
		"mf.enabled = true",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("lookupExternalIDsSQL missing %q:\n%s", want, sql)
		}
	}
}
```

- [ ] **Step 2: Run the query-shape test and verify it fails**

Run:

```bash
go test ./internal/catalog -run TestLookupExternalIDsSQLChecksProviderTableAndDirectColumns -count=1
```

Expected: FAIL because `lookupExternalIDsSQL` does not exist.

- [ ] **Step 3: Add lookup types and SQL helper**

In `internal/catalog/item_repo.go`, add these types near `MediaTMDBRow`:

```go
type ExternalIDLookupCandidate struct {
	TMDBID string
	TVDBID string
	IMDbID string
}

type ExternalIDMatchRow struct {
	QueryTMDBID     string
	MediaID         string
	MatchedProvider string
	LibraryID       string
	Title           string
}
```

Add this SQL helper below `LookupTMDBIDs`:

```go
func lookupExternalIDsSQL() string {
	return `
		WITH requested(query_tmdb_id, provider, provider_id, ord) AS (
			SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::int[])
		),
		direct_matches AS (
			SELECT r.query_tmdb_id, mi.content_id, r.provider, mil.media_folder_id::text, mi.title, r.ord,
			       CASE r.provider WHEN 'tmdb' THEN 0 WHEN 'tvdb' THEN 1 WHEN 'imdb' THEN 2 ELSE 3 END AS provider_rank
			FROM requested r
			JOIN media_items mi
			  ON mi.type = $5
			 AND (
				(r.provider = 'tmdb' AND COALESCE(mi.tmdb_id, '') = r.provider_id)
				OR (r.provider = 'tvdb' AND COALESCE(mi.tvdb_id, '') = r.provider_id)
				OR (r.provider = 'imdb' AND COALESCE(mi.imdb_id, '') = r.provider_id)
			 )
			JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			JOIN media_folders mf ON mf.id = mil.media_folder_id
			WHERE mf.enabled = true
		),
		provider_matches AS (
			SELECT r.query_tmdb_id, mi.content_id, r.provider, mil.media_folder_id::text, mi.title, r.ord,
			       CASE r.provider WHEN 'tmdb' THEN 0 WHEN 'tvdb' THEN 1 WHEN 'imdb' THEN 2 ELSE 3 END AS provider_rank
			FROM requested r
			JOIN media_item_provider_ids mip
			  ON mip.provider = r.provider
			 AND mip.provider_id = r.provider_id
			 AND mip.item_type = $5
			JOIN media_items mi ON mi.content_id = mip.content_id AND mi.type = $5
			JOIN media_item_libraries mil ON mil.content_id = mi.content_id
			JOIN media_folders mf ON mf.id = mil.media_folder_id
			WHERE mf.enabled = true
		)
		SELECT DISTINCT ON (query_tmdb_id)
		       query_tmdb_id, content_id, provider, media_folder_id, title
		FROM (
			SELECT * FROM direct_matches
			UNION ALL
			SELECT * FROM provider_matches
		) matches
		ORDER BY query_tmdb_id, provider_rank ASC, ord ASC, content_id ASC, media_folder_id ASC`
}
```

- [ ] **Step 4: Implement `LookupExternalIDs`**

Add this method to `internal/catalog/item_repo.go`:

```go
func (r *ItemRepository) LookupExternalIDs(
	ctx context.Context,
	mediaType string,
	candidates []ExternalIDLookupCandidate,
) ([]ExternalIDMatchRow, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	queryTMDBIDs := make([]string, 0, len(candidates)*3)
	providers := make([]string, 0, len(candidates)*3)
	providerIDs := make([]string, 0, len(candidates)*3)
	ordinals := make([]int32, 0, len(candidates)*3)

	appendID := func(candidate ExternalIDLookupCandidate, provider, providerID string, ordinal int) {
		providerID = strings.TrimSpace(providerID)
		if providerID == "" {
			return
		}
		queryTMDBIDs = append(queryTMDBIDs, strings.TrimSpace(candidate.TMDBID))
		providers = append(providers, provider)
		providerIDs = append(providerIDs, providerID)
		ordinals = append(ordinals, int32(ordinal))
	}

	for i, candidate := range candidates {
		appendID(candidate, "tmdb", candidate.TMDBID, i)
		appendID(candidate, "tvdb", candidate.TVDBID, i)
		appendID(candidate, "imdb", candidate.IMDbID, i)
	}
	if len(providerIDs) == 0 {
		return nil, nil
	}

	rows, err := r.pool.Query(ctx, lookupExternalIDsSQL(), queryTMDBIDs, providers, providerIDs, ordinals, mediaType)
	if err != nil {
		return nil, fmt.Errorf("lookup external ids: %w", err)
	}
	defer rows.Close()

	out := make([]ExternalIDMatchRow, 0)
	for rows.Next() {
		var row ExternalIDMatchRow
		if err := rows.Scan(&row.QueryTMDBID, &row.MediaID, &row.MatchedProvider, &row.LibraryID, &row.Title); err != nil {
			return nil, fmt.Errorf("scanning external id lookup row: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating external id lookup rows: %w", err)
	}
	return out, nil
}
```

Change `LookupTMDBIDs` to delegate to the new lookup:

```go
func (r *ItemRepository) LookupTMDBIDs(ctx context.Context, mediaType string, tmdbIDs []string) ([]MediaTMDBRow, error) {
	if len(tmdbIDs) == 0 {
		return nil, nil
	}
	candidates := make([]ExternalIDLookupCandidate, 0, len(tmdbIDs))
	for _, id := range tmdbIDs {
		if strings.TrimSpace(id) != "" {
			candidates = append(candidates, ExternalIDLookupCandidate{TMDBID: id})
		}
	}
	rows, err := r.LookupExternalIDs(ctx, mediaType, candidates)
	if err != nil {
		return nil, err
	}
	out := make([]MediaTMDBRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, MediaTMDBRow{
			MediaID:   row.MediaID,
			TMDBID:    row.QueryTMDBID,
			LibraryID: row.LibraryID,
			Title:     row.Title,
		})
	}
	return out, nil
}
```

- [ ] **Step 5: Add TMDB attach helper**

In `internal/catalog/provider_id_repo.go`, add `strconv` to imports and add:

```go
func (r *ProviderIDRepository) AttachTMDBID(ctx context.Context, contentID, itemType string, tmdbID int) error {
	contentID = strings.TrimSpace(contentID)
	itemType = strings.TrimSpace(itemType)
	if contentID == "" {
		return fmt.Errorf("content_id is required")
	}
	if itemType == "" {
		return fmt.Errorf("item_type is required")
	}
	if tmdbID <= 0 {
		return fmt.Errorf("tmdb_id must be positive")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin attach tmdb transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tmdbText := strconv.Itoa(tmdbID)
	if _, err := tx.Exec(ctx, `
		UPDATE media_items
		SET tmdb_id = COALESCE(NULLIF(tmdb_id, ''), $1),
		    updated_at = NOW()
		WHERE content_id = $2
		  AND type = $3
	`, tmdbText, contentID, itemType); err != nil {
		return fmt.Errorf("updating media item tmdb id: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id, created_at, updated_at)
		VALUES ($1, $2, 'tmdb', $3, NOW(), NOW())
		ON CONFLICT (content_id, provider) DO UPDATE
		SET item_type = EXCLUDED.item_type,
		    provider_id = EXCLUDED.provider_id,
		    updated_at = NOW()
	`, contentID, itemType, tmdbText); err != nil {
		return fmt.Errorf("upserting media item tmdb provider id: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit attach tmdb transaction: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Add helper input tests**

In `internal/catalog/provider_id_repo_test.go`, add:

```go
func TestNormalizeDurableProviderIDsKeepsTMDBFirstForBackfill(t *testing.T) {
	entries := normalizeDurableProviderIDs(map[string]string{
		"tvdb": "420105",
		"imdb": "tt18076310",
		"tmdb": "201992",
	})
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}
	if entries[0].Provider != "tmdb" || entries[0].ProviderID != "201992" {
		t.Fatalf("first entry = (%q, %q), want tmdb/201992", entries[0].Provider, entries[0].ProviderID)
	}
}
```

- [ ] **Step 7: Run catalog tests**

Run:

```bash
go test ./internal/catalog -run 'TestLookupExternalIDsSQLChecksProviderTableAndDirectColumns|TestNormalizeDurableProviderIDs' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/catalog/item_repo.go internal/catalog/item_repo_test.go internal/catalog/provider_id_repo.go internal/catalog/provider_id_repo_test.go
git commit -m "feat(catalog): lookup media presence by external ids"
```

---

## Task 3: Make Request Presence Provider-Aware and Backfill TMDB

**Files:**
- Modify: `internal/requests/presence.go`
- Create: `internal/requests/presence_test.go`
- Modify: `cmd/silo/main.go`
- Modify: `internal/api/router.go`

- [ ] **Step 1: Write failing presence tests**

Create `internal/requests/presence_test.go`:

```go
package requests

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type fakePresenceLookup struct {
	rows []catalog.ExternalIDMatchRow
	got  []catalog.ExternalIDLookupCandidate
}

func (f *fakePresenceLookup) LookupExternalIDs(_ context.Context, _ string, candidates []catalog.ExternalIDLookupCandidate) ([]catalog.ExternalIDMatchRow, error) {
	f.got = append([]catalog.ExternalIDLookupCandidate(nil), candidates...)
	return f.rows, nil
}

type fakeTMDBBackfiller struct {
	contentID string
	itemType  string
	tmdbID    int
}

func (f *fakeTMDBBackfiller) AttachTMDBID(_ context.Context, contentID, itemType string, tmdbID int) error {
	f.contentID = contentID
	f.itemType = itemType
	f.tmdbID = tmdbID
	return nil
}

func TestCatalogPresenceMatchesByTVDBAndBackfillsTMDB(t *testing.T) {
	tvdbID := 420105
	lookup := &fakePresenceLookup{rows: []catalog.ExternalIDMatchRow{{
		QueryTMDBID:     "201992",
		MediaID:         "120983767174086659",
		MatchedProvider: "tvdb",
		LibraryID:       "2",
		Title:           "The Rookie: Feds",
	}}}
	backfill := &fakeTMDBBackfiller{}
	presence := &CatalogPresence{items: lookup, tmdbBackfill: backfill}

	result, err := presence.Lookup(context.Background(), MediaTypeSeries, []PresenceCandidate{{
		TMDBID: 201992,
		TVDBID: &tvdbID,
		IMDbID: "tt18076310",
	}})
	if err != nil {
		t.Fatalf("Lookup returned error: %v", err)
	}
	if !result[201992].Available {
		t.Fatalf("available = false, want true")
	}
	if result[201992].MatchedProvider != "tvdb" {
		t.Fatalf("matched provider = %q, want tvdb", result[201992].MatchedProvider)
	}
	if backfill.contentID != "120983767174086659" || backfill.itemType != "series" || backfill.tmdbID != 201992 {
		t.Fatalf("backfill = %+v, want content 120983767174086659 series tmdb 201992", backfill)
	}
}

func TestCatalogPresenceKeepsLookupTMDBCompatibility(t *testing.T) {
	lookup := &fakePresenceLookup{rows: []catalog.ExternalIDMatchRow{{
		QueryTMDBID:     "550",
		MediaID:         "movie-1",
		MatchedProvider: "tmdb",
		LibraryID:       "1",
		Title:           "Fight Club",
	}}}
	presence := &CatalogPresence{items: lookup}

	result, err := presence.LookupTMDB(context.Background(), MediaTypeMovie, []int{550})
	if err != nil {
		t.Fatalf("LookupTMDB returned error: %v", err)
	}
	if !result[550] {
		t.Fatalf("result[550] = false, want true")
	}
	if len(lookup.got) != 1 || lookup.got[0].TMDBID != "550" {
		t.Fatalf("lookup candidates = %+v, want tmdb candidate", lookup.got)
	}
}
```

- [ ] **Step 2: Run presence tests and verify they fail**

Run:

```bash
go test ./internal/requests -run TestCatalogPresence -count=1
```

Expected: FAIL because `PresenceCandidate`, `CatalogPresence.Lookup`, and `tmdbBackfill` do not exist.

- [ ] **Step 3: Replace presence interface while keeping TMDB compatibility**

In `internal/requests/presence.go`, change the types to:

```go
type PresenceCandidate struct {
	TMDBID int
	TVDBID *int
	IMDbID string
}

type PresenceMatch struct {
	Available       bool
	ContentID        string
	MatchedProvider string
}

type PresenceResolver interface {
	Lookup(ctx context.Context, mediaType MediaType, candidates []PresenceCandidate) (map[int]PresenceMatch, error)
}

type presenceItemLookup interface {
	LookupExternalIDs(ctx context.Context, mediaType string, candidates []catalog.ExternalIDLookupCandidate) ([]catalog.ExternalIDMatchRow, error)
}

type tmdbBackfiller interface {
	AttachTMDBID(ctx context.Context, contentID, itemType string, tmdbID int) error
}

type CatalogPresence struct {
	items        presenceItemLookup
	tmdbBackfill tmdbBackfiller
}
```

Update the constructor:

```go
func NewCatalogPresence(items *catalog.ItemRepository, providerIDs ...*catalog.ProviderIDRepository) *CatalogPresence {
	var backfill tmdbBackfiller
	if len(providerIDs) > 0 {
		backfill = providerIDs[0]
	}
	return &CatalogPresence{items: items, tmdbBackfill: backfill}
}
```

Add the provider-aware lookup:

```go
func (p *CatalogPresence) Lookup(ctx context.Context, mediaType MediaType, candidates []PresenceCandidate) (map[int]PresenceMatch, error) {
	out := map[int]PresenceMatch{}
	if p == nil || p.items == nil || len(candidates) == 0 {
		return out, nil
	}

	lookupCandidates := make([]catalog.ExternalIDLookupCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.TMDBID <= 0 {
			continue
		}
		row := catalog.ExternalIDLookupCandidate{TMDBID: strconv.Itoa(candidate.TMDBID), IMDbID: strings.TrimSpace(candidate.IMDbID)}
		if candidate.TVDBID != nil && *candidate.TVDBID > 0 {
			row.TVDBID = strconv.Itoa(*candidate.TVDBID)
		}
		lookupCandidates = append(lookupCandidates, row)
	}
	if len(lookupCandidates) == 0 {
		return out, nil
	}

	rows, err := p.items.LookupExternalIDs(ctx, string(mediaType), lookupCandidates)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		id, err := strconv.Atoi(row.QueryTMDBID)
		if err != nil || id <= 0 {
			continue
		}
		out[id] = PresenceMatch{
			Available:       true,
			ContentID:        row.MediaID,
			MatchedProvider: row.MatchedProvider,
		}
		if row.MatchedProvider != "tmdb" && p.tmdbBackfill != nil {
			if err := p.tmdbBackfill.AttachTMDBID(ctx, row.MediaID, string(mediaType), id); err != nil {
				slog.Warn("requests: failed to backfill tmdb id from presence lookup",
					"content_id", row.MediaID,
					"media_type", mediaType,
					"tmdb_id", id,
					"matched_provider", row.MatchedProvider,
					"error", err)
			}
		}
	}
	return out, nil
}
```

Keep a compatibility wrapper for existing call sites and tests:

```go
func (p *CatalogPresence) LookupTMDB(ctx context.Context, mediaType MediaType, tmdbIDs []int) (map[int]bool, error) {
	candidates := make([]PresenceCandidate, 0, len(tmdbIDs))
	for _, id := range tmdbIDs {
		if id > 0 {
			candidates = append(candidates, PresenceCandidate{TMDBID: id})
		}
	}
	matches, err := p.Lookup(ctx, mediaType, candidates)
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for id, match := range matches {
		out[id] = match.Available
	}
	return out, nil
}
```

Add imports:

```go
import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)
```

- [ ] **Step 4: Wire provider ID repository into request presence**

In `internal/api/router.go`, change request service construction from:

```go
mediarequests.NewCatalogPresence(itemRepo),
```

to:

```go
mediarequests.NewCatalogPresence(itemRepo, providerIDRepo),
```

In `cmd/silo/main.go`, change reconcile service construction from:

```go
mediarequests.NewCatalogPresence(catalog.NewItemRepository(deps.DB)),
```

to:

```go
mediarequests.NewCatalogPresence(
	catalog.NewItemRepository(deps.DB),
	catalog.NewProviderIDRepository(deps.DB),
),
```

- [ ] **Step 5: Run request presence tests**

Run:

```bash
go test ./internal/requests -run TestCatalogPresence -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/requests/presence.go internal/requests/presence_test.go internal/api/router.go cmd/silo/main.go
git commit -m "feat(requests): match catalog presence by external ids"
```

---

## Task 4: Hydrate TMDB External IDs Before Availability Checks

**Files:**
- Modify: `internal/requests/service.go`
- Modify: `internal/requests/service_test.go`

- [ ] **Step 1: Update request fake presence**

In `internal/requests/service_test.go`, replace the fake presence with:

```go
type fakePresence struct {
	available map[MediaType]map[int]bool
	byTVDB    map[MediaType]map[int]int
	got       []PresenceCandidate
}

func (f *fakePresence) Lookup(_ context.Context, mediaType MediaType, candidates []PresenceCandidate) (map[int]PresenceMatch, error) {
	out := map[int]PresenceMatch{}
	f.got = append(f.got, candidates...)
	for _, candidate := range candidates {
		if f.available != nil && f.available[mediaType][candidate.TMDBID] {
			out[candidate.TMDBID] = PresenceMatch{Available: true, MatchedProvider: "tmdb"}
			continue
		}
		if candidate.TVDBID != nil && f.byTVDB != nil {
			if tmdbID, ok := f.byTVDB[mediaType][*candidate.TVDBID]; ok && tmdbID == candidate.TMDBID {
				out[candidate.TMDBID] = PresenceMatch{Available: true, MatchedProvider: "tvdb"}
			}
		}
	}
	return out, nil
}
```

- [ ] **Step 2: Update fake TMDB external IDs**

In `fakeTMDBClient`, add:

```go
externalIDsByID map[int]*tmdb.ExternalIDs
externalIDCalls []int
```

Replace `GetExternalIDs` with:

```go
func (f *fakeTMDBClient) GetExternalIDs(_ context.Context, _ string, id int) (*tmdb.ExternalIDs, error) {
	f.externalIDCalls = append(f.externalIDCalls, id)
	if f.externalIDsByID != nil {
		return f.externalIDsByID[id], nil
	}
	return f.externalIDs, nil
}
```

- [ ] **Step 3: Write failing search/create/reconcile tests**

Add these tests to `internal/requests/service_test.go`:

```go
func TestSearchMarksSeriesAvailableByHydratedTVDBID(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	tmdbClient := &fakeTMDBClient{
		page: &tmdb.MediaPage{
			Page: 1,
			Results: []tmdb.MediaResult{{
				ID:        201992,
				MediaType: "series",
				Title:     "The Rookie: Feds",
				Year:      2022,
			}},
		},
		externalIDsByID: map[int]*tmdb.ExternalIDs{
			201992: {TVDBID: 420105, IMDbID: "tt18076310"},
		},
	}
	presence := &fakePresence{byTVDB: map[MediaType]map[int]int{
		MediaTypeSeries: {420105: 201992},
	}}
	service := NewService(store, tmdbClient, presence)

	page, err := service.Search(context.Background(), testViewer(1), "rookie feds", MediaTypeSeries, 1)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if got := page.Results[0].Availability; got != AvailabilityAvailable {
		t.Fatalf("availability = %q, want available", got)
	}
	if page.Results[0].Request.Reason != "already_available" {
		t.Fatalf("request reason = %q, want already_available", page.Results[0].Request.Reason)
	}
	if len(presence.got) != 1 || presence.got[0].TVDBID == nil || *presence.got[0].TVDBID != 420105 {
		t.Fatalf("presence candidates = %+v, want hydrated tvdb id", presence.got)
	}
}

func TestCreateRequestBlocksWhenHydratedTVDBIDIsAvailable(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	tmdbClient := &fakeTMDBClient{externalIDs: &tmdb.ExternalIDs{TVDBID: 420105, IMDbID: "tt18076310"}}
	presence := &fakePresence{byTVDB: map[MediaType]map[int]int{
		MediaTypeSeries: {420105: 201992},
	}}
	service := NewService(store, tmdbClient, presence)

	_, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeSeries,
		TMDBID:    201992,
		Title:     "The Rookie: Feds",
	})
	if !errors.Is(err, ErrAlreadyAvailable) {
		t.Fatalf("err = %v, want ErrAlreadyAvailable", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("created requests = %d, want 0", len(store.created))
	}
}

func TestReconcileRequestsCompletesByStoredTVDBID(t *testing.T) {
	store := newFakeStore()
	tvdbID := 420105
	store.candidates = []*Request{{
		ID:        "req-1",
		MediaType: MediaTypeSeries,
		TMDBID:    201992,
		TVDBID:    &tvdbID,
		Status:    StatusQueued,
		Outcome:   OutcomeActive,
	}}
	presence := &fakePresence{byTVDB: map[MediaType]map[int]int{
		MediaTypeSeries: {420105: 201992},
	}}
	service := NewService(store, &fakeTMDBClient{}, presence)

	result, err := service.ReconcileRequests(context.Background(), 100)
	if err != nil {
		t.Fatalf("ReconcileRequests returned error: %v", err)
	}
	if result.Completed != 1 {
		t.Fatalf("completed = %d, want 1", result.Completed)
	}
}
```

- [ ] **Step 4: Run the new tests and verify they fail**

Run:

```bash
go test ./internal/requests -run 'TestSearchMarksSeriesAvailableByHydratedTVDBID|TestCreateRequestBlocksWhenHydratedTVDBIDIsAvailable|TestReconcileRequestsCompletesByStoredTVDBID' -count=1
```

Expected: FAIL because the service does not hydrate external IDs before availability.

- [ ] **Step 5: Add candidate hydration helpers**

In `internal/requests/service.go`, add:

```go
func (s *Service) lookupPresence(ctx context.Context, mediaType MediaType, candidates []PresenceCandidate) (map[int]PresenceMatch, error) {
	if s.presence == nil {
		return map[int]PresenceMatch{}, nil
	}
	return s.presence.Lookup(ctx, mediaType, candidates)
}

func availabilityBoolMap(matches map[int]PresenceMatch) map[int]bool {
	out := map[int]bool{}
	for id, match := range matches {
		out[id] = match.Available
	}
	return out
}

func requestPresenceCandidate(req Request) PresenceCandidate {
	candidate := PresenceCandidate{
		TMDBID: req.TMDBID,
		IMDbID: strings.TrimSpace(req.IMDbID),
	}
	if req.TVDBID != nil && *req.TVDBID > 0 {
		tvdbID := *req.TVDBID
		candidate.TVDBID = &tvdbID
	}
	return candidate
}

func createPresenceCandidate(input CreateRequestInput) PresenceCandidate {
	candidate := PresenceCandidate{
		TMDBID: input.TMDBID,
		IMDbID: strings.TrimSpace(input.IMDbID),
	}
	if input.TVDBID != nil && *input.TVDBID > 0 {
		tvdbID := *input.TVDBID
		candidate.TVDBID = &tvdbID
	}
	return candidate
}

func (s *Service) hydratePresenceCandidate(ctx context.Context, mediaType MediaType, candidate PresenceCandidate) PresenceCandidate {
	if candidate.TMDBID <= 0 {
		return candidate
	}
	client, ok := s.tmdb.(TMDBExternalIDClient)
	if !ok {
		return candidate
	}
	externalIDs, err := client.GetExternalIDs(ctx, tmdbMediaType(mediaType), candidate.TMDBID)
	if err != nil || externalIDs == nil {
		return candidate
	}
	if candidate.IMDbID == "" {
		candidate.IMDbID = strings.TrimSpace(externalIDs.IMDbID)
	}
	if candidate.TVDBID == nil && externalIDs.TVDBID > 0 {
		tvdbID := externalIDs.TVDBID
		candidate.TVDBID = &tvdbID
	}
	return candidate
}

func tmdbMediaType(mediaType MediaType) string {
	if mediaType == MediaTypeSeries {
		return "tv"
	}
	return "movie"
}
```

Replace `lookupAvailable` with:

```go
func (s *Service) lookupAvailable(ctx context.Context, mediaType MediaType, ids []int) (map[int]bool, error) {
	candidates := make([]PresenceCandidate, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			candidates = append(candidates, s.hydratePresenceCandidate(ctx, mediaType, PresenceCandidate{TMDBID: id}))
		}
	}
	matches, err := s.lookupPresence(ctx, mediaType, candidates)
	if err != nil {
		return nil, err
	}
	return availabilityBoolMap(matches), nil
}
```

- [ ] **Step 6: Reorder create availability check after external ID enrichment**

In `CreateRequest`, move:

```go
s.enrichExternalIDs(ctx, &normalized)
```

so it happens immediately after `normalizeCreateInput` and before `lookupAvailable`.

Then change the availability check to:

```go
matches, err := s.lookupPresence(ctx, normalized.MediaType, []PresenceCandidate{createPresenceCandidate(normalized)})
if err != nil {
	return nil, err
}
if matches[normalized.TMDBID].Available {
	return nil, ErrAlreadyAvailable
}
```

- [ ] **Step 7: Use stored request IDs during reconciliation**

Change `requestAvailable` to:

```go
func (s *Service) requestAvailable(ctx context.Context, req Request) (bool, error) {
	matches, err := s.lookupPresence(ctx, req.MediaType, []PresenceCandidate{requestPresenceCandidate(req)})
	if err != nil {
		return false, err
	}
	return matches[req.TMDBID].Available, nil
}
```

- [ ] **Step 8: Run request tests**

Run:

```bash
go test ./internal/requests -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/requests/service.go internal/requests/service_test.go
git commit -m "fix(requests): hydrate external ids before availability"
```

---

## Task 5: Schedule Existing Missing-TMDB Items for Refresh

**Files:**
- Modify: `internal/metadata/refresh_debt.go`
- Modify: `internal/metadata/refresh_debt_repo.go`
- Modify: `internal/metadata/refresh_debt_test.go`
- Modify: `internal/adminjob/library_refresh.go`

- [ ] **Step 1: Add failing refresh debt tests**

In `internal/metadata/refresh_debt_test.go`, add:

```go
func TestRefreshDebtReasonsForItemFlagsMissingTMDBWithOtherProviderIDs(t *testing.T) {
	item := &models.MediaItem{
		Type:   "series",
		Status: "matched",
		TvdbID: "420105",
		ImdbID: "tt18076310",
		TmdbID: "",
	}

	mask := refreshDebtReasonsForItem(item)
	if !hasRefreshDebtReason(mask, RefreshDebtReasonProviderIDIncomplete) {
		t.Fatalf("reason mask = %d, want provider id incomplete", mask)
	}
}

func TestRefreshDebtReasonsForItemDoesNotFlagProviderIDIncompleteWithoutAlternateIDs(t *testing.T) {
	item := &models.MediaItem{
		Type:   "series",
		Status: "matched",
		TmdbID: "",
	}

	mask := refreshDebtReasonsForItem(item)
	if hasRefreshDebtReason(mask, RefreshDebtReasonProviderIDIncomplete) {
		t.Fatalf("reason mask = %d, did not want provider id incomplete", mask)
	}
}
```

- [ ] **Step 2: Run refresh debt tests and verify they fail**

Run:

```bash
go test ./internal/metadata -run TestRefreshDebtReasonsForItem -count=1
```

Expected: FAIL because `RefreshDebtReasonProviderIDIncomplete` does not exist.

- [ ] **Step 3: Add provider ID incomplete reason**

In `internal/metadata/refresh_debt.go`, add the reason after `RefreshDebtReasonStaleProviderID`:

```go
RefreshDebtReasonProviderIDIncomplete
```

Update `refreshDebtPriority`:

```go
case hasRefreshDebtReason(reasonMask, RefreshDebtReasonStaleProviderID):
	return 250
case hasRefreshDebtReason(reasonMask, RefreshDebtReasonProviderIDIncomplete):
	return 240
```

Update `refreshDebtReasonsForItem`:

```go
if hasProviderIDRefreshDebt(item) {
	reasonMask |= RefreshDebtReasonProviderIDIncomplete
}
```

Add:

```go
func hasProviderIDRefreshDebt(item *models.MediaItem) bool {
	if item == nil || !strings.EqualFold(strings.TrimSpace(item.Status), "matched") {
		return false
	}
	if strings.TrimSpace(item.TmdbID) != "" {
		return false
	}
	return strings.TrimSpace(item.TvdbID) != "" || strings.TrimSpace(item.ImdbID) != ""
}
```

- [ ] **Step 4: Add metrics label**

In `internal/metadata/refresh_debt_repo.go`, add the reason definition:

```go
{reason: "provider_id_incomplete", mask: RefreshDebtReasonProviderIDIncomplete},
```

Place it after `stale_provider_id`.

- [ ] **Step 5: Include missing TMDB in quick library refresh**

In `internal/adminjob/library_refresh.go`, add this OR branch to the quick-mode second predicate:

```sql
			OR (
				COALESCE(mi.tmdb_id, '') = ''
				AND (
					COALESCE(mi.tvdb_id, '') <> ''
					OR COALESCE(mi.imdb_id, '') <> ''
				)
			)
```

The resulting quick-mode condition should still require at least one external ID through the first predicate:

```sql
		  AND (
			COALESCE(mi.tmdb_id, '') <> ''
			OR COALESCE(mi.tvdb_id, '') <> ''
			OR COALESCE(mi.imdb_id, '') <> ''
		  )
```

- [ ] **Step 6: Run metadata and adminjob tests**

Run:

```bash
go test ./internal/metadata ./internal/adminjob -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/metadata/refresh_debt.go internal/metadata/refresh_debt_repo.go internal/metadata/refresh_debt_test.go internal/adminjob/library_refresh.go
git commit -m "fix(metadata): refresh items missing tmdb ids"
```

---

## Task 6: Fix TVDB Plugin Title Search Remote IDs

**Files:**
- Modify in sibling `silo-plugin-tvdb` repository: `provider/provider.go`
- Modify in sibling `silo-plugin-tvdb` repository: `provider/provider_test.go`

- [ ] **Step 1: Switch to the TVDB plugin repository**

Run from the sibling plugin repository root:

```bash
pwd
```

Expected: path ends with `silo-plugin-tvdb`.

- [ ] **Step 2: Add failing title search test**

In `provider/provider_test.go`, add:

```go
func TestProviderSearchByTitleIncludesRemoteIDs(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{
					"token": "test-token",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/search":
			if r.URL.Query().Get("query") != "The Rookie: Feds" {
				t.Fatalf("query = %q, want The Rookie: Feds", r.URL.Query().Get("query"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": []map[string]any{{
					"name":     "The Rookie: Feds",
					"year":     "2022",
					"tvdb_id":  "420105",
					"overview": "A spinoff series.",
					"remote_ids": []map[string]any{
						{"type": 12, "id": "201992", "sourceName": "TheMovieDB.com"},
						{"type": 2, "id": "tt18076310", "sourceName": "IMDB"},
					},
				}},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(1000)
	client.SetBaseURL(server.URL)
	p := NewProviderWithClient(client)

	results, err := p.Search(context.Background(), metadata.SearchQuery{
		Title:       "The Rookie: Feds",
		ContentType: "series",
	})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	ids := results[0].ProviderIDs
	if ids["tvdb"] != "420105" || ids["tmdb"] != "201992" || ids["imdb"] != "tt18076310" {
		t.Fatalf("provider ids = %+v, want tvdb/tmdb/imdb", ids)
	}
}
```

- [ ] **Step 3: Run the TVDB plugin test and verify it fails**

Run from the TVDB plugin repository root:

```bash
go test ./provider -run TestProviderSearchByTitleIncludesRemoteIDs -count=1
```

Expected: FAIL because `searchByTitle` returns only `tvdb`.

- [ ] **Step 4: Fill remote IDs in title search**

In `provider/provider.go`, replace the body of the `for _, r := range results` loop in `searchByTitle` with:

```go
ids := map[string]string{"tvdb": r.TVDBID}
fillRemoteIDs(ids, r.RemoteIDs)
out = append(out, metadata.SearchResult{
	Name:        r.Name,
	Year:        extractYear(r.Year),
	ProviderIDs: ids,
	ImageURL:    r.ImageURL,
	Overview:    r.Overview,
	Provider:    p.Slug(),
})
```

- [ ] **Step 5: Run TVDB plugin tests**

Run from the TVDB plugin repository root:

```bash
go test ./provider -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit in the TVDB plugin repository**

Run from the TVDB plugin repository root:

```bash
git add provider/provider.go provider/provider_test.go
git commit -m "fix(tvdb): include remote ids in title search"
```

---

## Task 7: Full Verification

**Files:**
- Server repository verification only.
- TVDB plugin repository verification only.

- [ ] **Step 1: Run focused server tests**

Run from the server repository root:

```bash
go test ./internal/metadata ./internal/catalog ./internal/requests ./internal/adminjob -count=1
```

Expected: PASS.

- [ ] **Step 2: Run TVDB plugin tests**

Run from the TVDB plugin repository root:

```bash
go test ./provider -count=1
```

Expected: PASS.

- [ ] **Step 3: Build the server**

Run from the server repository root:

```bash
make build
```

Expected: build completes without Go, TypeScript, or frontend bundling errors.

- [ ] **Step 4: Validate the repair behavior in development**

After deploying or running the patched server against the development stack, trigger a quick refresh for the TV Shows library or run the reconcile task. Then query item `120983767174086659`:

```sql
SELECT content_id, title, tmdb_id, tvdb_id, imdb_id, status
FROM media_items
WHERE content_id = '120983767174086659';
```

Expected row includes:

```text
content_id=120983767174086659
title=The Rookie: Feds
tmdb_id=201992
tvdb_id=420105
imdb_id=tt18076310
status=matched
```

Also check durable provider IDs:

```sql
SELECT provider, provider_id
FROM media_item_provider_ids
WHERE content_id = '120983767174086659'
ORDER BY provider;
```

Expected rows include:

```text
imdb | tt18076310
tmdb | 201992
tvdb | 420105
```

- [ ] **Step 5: Validate request availability**

Search requests for `The Rookie: Feds`.

Expected: TMDB result `201992` is marked unavailable to request with request reason `already_available`.

---

## Execution Notes

- Do not add a migration for the refresh debt reason; reason masks are stored as integers and existing rows remain valid.
- The request API still accepts TMDB IDs only. TVDB/IMDb are internal matching hints and should not change client contracts.
- Presence backfill is best effort. Availability must still return `already_available` even if the TMDB repair write hits a uniqueness conflict or transient DB error.
- TMDB external ID hydration should fail open to TMDB-only matching when TMDB external ID lookup fails.
- If the TVDB plugin change is not released immediately, the server-side compatible candidate merge still improves cases where the TMDB plugin supplies the richer candidate.
