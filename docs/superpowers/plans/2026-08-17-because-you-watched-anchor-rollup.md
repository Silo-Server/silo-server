# Because You Watched Automatic Anchor Rollup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make automatically selected Because You Watched anchors roll completed episodes up to their parent series, deduplicate by canonical ID, and apply the anchor limit afterward.

**Architecture:** Keep automatic-anchor semantics at the recommendation signal boundary. The SQL fallback will rank distinct canonical `watched_activity.item_id` values directly, while the user-store path will resolve each page of completed leaves to canonical IDs and retain only the newest distinct candidates. Existing worker and reader consumers will continue using the returned IDs unchanged, ensuring cache writes and reads share the same series keys.

**Tech Stack:** Go 1.26.4, PostgreSQL/pgx, standard `testing` package

## Global Constraints

- The fix applies only to automatically selected `because_you_watched` anchors; explicit anchors retain current semantics.
- Episodes resolve to their parent series; movies, series, audiobooks, ebooks, and unknown non-episode IDs remain unchanged.
- Results are ordered by newest completion activity, then canonical item ID ascending for deterministic ties.
- Deduplication happens before applying the requested limit.
- `/api/v1`, section configuration, and Web UI contracts do not change.
- Commands assume the repository root is the current working directory.

---

### Task 1: Canonical repository-backed automatic anchors

**Files:**
- Modify: `internal/recommendations/repo.go`
- Test: `internal/recommendations/repo_test.go`

**Interfaces:**
- Consumes: `watchedActivityCTE`, whose `item_id` already rolls episode leaves up to `episodes.series_id`.
- Produces: `(*Repo).GetRecentCompletedItemIDs(ctx context.Context, userID int, profileID string, limit int) ([]string, error)` returning distinct canonical IDs in recency order.
- Produces: `(*Repo).ResolveCanonicalItemIDs(ctx context.Context, itemIDs []string) (map[string]string, error)` for ordered user-store normalization in Task 2.

- [ ] **Step 1: Write a failing query-shape test**

Add this test before defining `recentCompletedItemIDsQuery`:

```go
func TestRecentCompletedItemIDsQueryCanonicalizesBeforeLimit(t *testing.T) {
	query := strings.Join(strings.Fields(recentCompletedItemIDsQuery), " ")

	assertQueryTermsInOrder(t, query,
		"SELECT item_id",
		"FROM watched_activity",
		"WHERE user_id = $1 AND profile_id = $2 AND completed = true",
		"GROUP BY item_id",
		"ORDER BY MAX(updated_at) DESC, item_id ASC",
		"LIMIT $3",
	)
	if strings.Contains(query, "SELECT leaf_item_id") {
		t.Fatalf("automatic anchors must not use episode leaf IDs: %s", query)
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./internal/recommendations -run TestRecentCompletedItemIDsQueryCanonicalizesBeforeLimit -count=1
```

Expected: build FAIL because `recentCompletedItemIDsQuery` does not exist yet.

- [ ] **Step 3: Implement the canonical repository query**

Define and use this query in `GetRecentCompletedItemIDs`:

```go
var recentCompletedItemIDsQuery = fmt.Sprintf(`
	WITH %s
	SELECT item_id
	FROM   watched_activity
	WHERE  user_id = $1 AND profile_id = $2 AND completed = true
	GROUP  BY item_id
	ORDER  BY MAX(updated_at) DESC, item_id ASC
	LIMIT  $3
`, watchedActivityCTE)
```

Keep the existing `limit <= 0` behavior and error wrapping, but call `r.pool.Query(ctx, recentCompletedItemIDsQuery, userID, profileID, limit)`.

- [ ] **Step 4: Write a failing test for ordered episode resolution**

Add a focused test around a pure mapping seam by extending the repository with the ordered resolver signature shown in **Interfaces**. The test fixture should make the expected mapping explicit:

```go
func TestResolveCanonicalItemIDsDefaultsToInputAndRollsEpisodesUp(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := context.Background()
	prefix := "t612-anchor-"
	seriesID := prefix + "series"
	episodeID := prefix + "episode"
	movieID := prefix + "movie"

	cleanupRecoMediaItems(t, pool, prefix)
	seedRecoMediaItem(t, pool, seriesID, "series", "matched")
	seedRecoMediaItem(t, pool, movieID, "movie", "matched")
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $2, 1, 1, 'Episode 1')
	`, episodeID, seriesID); err != nil {
		t.Fatalf("seed episode: %v", err)
	}

	got, err := NewRepo(pool).ResolveCanonicalItemIDs(ctx, []string{episodeID, movieID, "unknown-id"})
	if err != nil {
		t.Fatalf("ResolveCanonicalItemIDs: %v", err)
	}
	want := map[string]string{episodeID: seriesID, movieID: movieID, "unknown-id": "unknown-id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved IDs = %#v, want %#v", got, want)
	}
}
```

Add `reflect` to the test imports. If the test database is unavailable, `newEngineTestPool` will skip this integration test; Task 2's unit tests will exercise the same contract without PostgreSQL.

- [ ] **Step 5: Run the resolver test and verify RED**

Run:

```bash
go test ./internal/recommendations -run TestResolveCanonicalItemIDsDefaultsToInputAndRollsEpisodesUp -count=1
```

Expected: build FAIL because `ResolveCanonicalItemIDs` does not exist.

- [ ] **Step 6: Implement the ordered canonical resolver and reuse it for set resolution**

Add:

```go
func (r *Repo) ResolveCanonicalItemIDs(ctx context.Context, itemIDs []string) (map[string]string, error) {
	resolved := make(map[string]string, len(itemIDs))
	for _, itemID := range itemIDs {
		resolved[itemID] = itemID
	}
	if len(itemIDs) == 0 {
		return resolved, nil
	}

	rows, err := r.pool.Query(ctx, `
		SELECT content_id, series_id
		FROM episodes
		WHERE content_id = ANY($1)
	`, itemIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve canonical item IDs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var contentID, seriesID string
		if err := rows.Scan(&contentID, &seriesID); err != nil {
			return nil, fmt.Errorf("scan canonical item ID: %w", err)
		}
		resolved[contentID] = seriesID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate canonical item IDs: %w", err)
	}
	return resolved, nil
}
```

Refactor `ResolveCanonicalItemIDSet` to call this method and build its set from the resolved map values, keeping its public behavior unchanged.

- [ ] **Step 7: Run Task 1 tests and verify GREEN**

Run:

```bash
go test ./internal/recommendations -run 'TestRecentCompletedItemIDsQueryCanonicalizesBeforeLimit|TestResolveCanonicalItemIDsDefaultsToInputAndRollsEpisodesUp' -count=1
```

Expected: PASS, with the database-backed resolver test either passing or explicitly skipped only when `SILO_TEST_DATABASE_URL` is unset.

- [ ] **Step 8: Commit Task 1**

```bash
git add internal/recommendations/repo.go internal/recommendations/repo_test.go
git commit -m "fix(recommendations): canonicalize repository anchors"
```

---

### Task 2: Canonical user-store anchors with deduplication before limit

**Files:**
- Modify: `internal/recommendations/signals.go`
- Test: `internal/recommendations/signals_test.go`

**Interfaces:**
- Consumes: `signalRepo.ResolveCanonicalItemIDs(ctx context.Context, itemIDs []string) (map[string]string, error)` from Task 1.
- Consumes: `userstore.UserStore.ListProgress(ctx, profileID, "completed", limit, offset)` ordered by newest update first.
- Produces: identical canonical ordering semantics from `SignalReader.RecentCompletedItemIDs` for both repository fallback and user-store providers.

- [ ] **Step 1: Write a failing unit test for rollup, recency, and limit semantics**

```go
func TestSignalReaderRecentCompletedCanonicalizesBeforeDedupAndLimit(t *testing.T) {
	store := &fakeSignalStore{progress: []userstore.WatchProgress{
		{ProfileID: "p1", MediaItemID: "episode-a2", Completed: true, UpdatedAt: "2026-08-05T10:00:00Z"},
		{ProfileID: "p1", MediaItemID: "episode-a1", Completed: true, UpdatedAt: "2026-08-04T10:00:00Z"},
		{ProfileID: "p1", MediaItemID: "movie-b", Completed: true, UpdatedAt: "2026-08-03T10:00:00Z"},
		{ProfileID: "p1", MediaItemID: "episode-c1", Completed: true, UpdatedAt: "2026-08-02T10:00:00Z"},
	}}
	repo := &fakeSignalRepo{canonical: map[string]string{
		"episode-a2": "series-a",
		"episode-a1": "series-a",
		"episode-c1": "series-c",
	}}
	reader := NewSignalReader(repo, fakeSignalProvider{store: store})

	ids, err := reader.RecentCompletedItemIDs(context.Background(), 7, "p1", 3)
	if err != nil {
		t.Fatalf("RecentCompletedItemIDs returned error: %v", err)
	}
	want := []string{"series-a", "movie-b", "series-c"}
	if !slices.Equal(ids, want) {
		t.Fatalf("recent completed = %#v, want %#v", ids, want)
	}
}
```

- [ ] **Step 2: Write a failing pagination regression test**

Add a test that creates `signalPageSize` newest episode rows all mapped to `series-a`, followed by two older movies. Request three anchors and assert `[]string{"series-a", "movie-b", "movie-c"}`. This proves the implementation reads beyond a duplicate-filled first page rather than over-fetching by an arbitrary multiplier.

```go
func TestSignalReaderRecentCompletedPagesUntilDistinctLimitIsFilled(t *testing.T) {
	progress := make([]userstore.WatchProgress, 0, signalPageSize+2)
	canonical := make(map[string]string, signalPageSize)
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < signalPageSize; i++ {
		id := fmt.Sprintf("episode-a-%04d", i)
		progress = append(progress, userstore.WatchProgress{
			ProfileID: "p1", MediaItemID: id, Completed: true,
			UpdatedAt: base.Add(-time.Duration(i) * time.Second).Format(time.RFC3339Nano),
		})
		canonical[id] = "series-a"
	}
	progress = append(progress,
		userstore.WatchProgress{ProfileID: "p1", MediaItemID: "movie-b", Completed: true, UpdatedAt: base.Add(-2000 * time.Second).Format(time.RFC3339Nano)},
		userstore.WatchProgress{ProfileID: "p1", MediaItemID: "movie-c", Completed: true, UpdatedAt: base.Add(-2001 * time.Second).Format(time.RFC3339Nano)},
	)

	reader := NewSignalReader(
		&fakeSignalRepo{canonical: canonical},
		fakeSignalProvider{store: &fakeSignalStore{progress: progress}},
	)
	ids, err := reader.RecentCompletedItemIDs(context.Background(), 7, "p1", 3)
	if err != nil {
		t.Fatalf("RecentCompletedItemIDs returned error: %v", err)
	}
	if want := []string{"series-a", "movie-b", "movie-c"}; !slices.Equal(ids, want) {
		t.Fatalf("recent completed = %#v, want %#v", ids, want)
	}
}
```

Add `fmt` to the test imports.

- [ ] **Step 3: Run both regression tests and verify RED**

Run:

```bash
go test ./internal/recommendations -run 'TestSignalReaderRecentCompletedCanonicalizesBeforeDedupAndLimit|TestSignalReaderRecentCompletedPagesUntilDistinctLimitIsFilled' -count=1
```

Expected: FAIL because the store path currently returns the first raw leaf IDs and requests only `limit` rows.

- [ ] **Step 4: Add the ordered resolver contract and implement canonicalization**

Extend `signalRepo`:

```go
ResolveCanonicalItemIDs(ctx context.Context, contentIDs []string) (map[string]string, error)
```

Implement it in `fakeSignalRepo` using the existing `canonical` fixture map while defaulting every unmapped ID to itself. Keep `ResolveCanonicalItemIDSet` and have it build a set from the ordered resolver so existing watched-set tests retain their behavior.

Add focused helpers in `signals.go`:

```go
func canonicalizeCompletedRows(ctx context.Context, repo signalRepo, rows []WatchProgressRow) error {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.MediaItemID)
	}
	resolved, err := repo.ResolveCanonicalItemIDs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range rows {
		if canonicalID := resolved[rows[i].MediaItemID]; canonicalID != "" {
			rows[i].MediaItemID = canonicalID
		}
	}
	return nil
}

func recentDistinctCompletedRows(rows []WatchProgressRow, limit int) []WatchProgressRow {
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
		}
		return rows[i].MediaItemID < rows[j].MediaItemID
	})
	seen := make(map[string]struct{}, len(rows))
	result := make([]WatchProgressRow, 0, min(limit, len(rows)))
	for _, row := range rows {
		if _, ok := seen[row.MediaItemID]; ok {
			continue
		}
		seen[row.MediaItemID] = struct{}{}
		result = append(result, row)
		if len(result) == limit {
			break
		}
	}
	return result
}
```

Replace the current one-page store branch of `RecentCompletedItemIDs` with this exact flow:

```go
	candidates := make([]WatchProgressRow, 0, limit)
	ebookProgress, err := s.repo.GetEbookReaderProgressForUser(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	for _, wp := range ebookProgress {
		if wp.Completed {
			candidates = append(candidates, wp)
		}
	}
	if err := canonicalizeCompletedRows(ctx, s.repo, candidates); err != nil {
		return nil, fmt.Errorf("resolve recent completed item IDs: %w", err)
	}
	candidates = recentDistinctCompletedRows(candidates, limit)

	offset := 0
	for {
		progress, err := store.ListProgress(ctx, profileID, "completed", signalPageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("list completed progress from store: %w", err)
		}

		page := make([]WatchProgressRow, 0, len(progress))
		oldestPageTime := time.Time{}
		for _, wp := range progress {
			if !wp.Completed {
				continue
			}
			updatedAt := parseSignalTime(wp.UpdatedAt, time.Time{})
			page = append(page, WatchProgressRow{
				MediaItemID: wp.MediaItemID,
				Completed:   true,
				UpdatedAt:   updatedAt,
			})
			if oldestPageTime.IsZero() || updatedAt.Before(oldestPageTime) {
				oldestPageTime = updatedAt
			}
		}
		if err := canonicalizeCompletedRows(ctx, s.repo, page); err != nil {
			return nil, fmt.Errorf("resolve recent completed item IDs: %w", err)
		}
		candidates = recentDistinctCompletedRows(append(candidates, page...), limit)

		if len(progress) < signalPageSize {
			break
		}
		offset += len(progress)
		if len(candidates) == limit && !oldestPageTime.IsZero() &&
			oldestPageTime.Before(candidates[len(candidates)-1].UpdatedAt) {
			break
		}
	}

	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.MediaItemID)
	}
	return ids, nil
```

The strict `Before` cutoff continues across equal timestamps so canonical-ID tie ordering remains exact.

Wrap resolver failures as `resolve recent completed item IDs: %w`. Preserve `limit <= 0`, unfinished-row filtering, ebook merging, and the existing repository fallback.

- [ ] **Step 5: Run the signal tests and verify GREEN**

Run:

```bash
go test ./internal/recommendations -run 'TestSignalReaderRecentCompleted' -count=1
```

Expected: PASS for the new canonicalization and pagination tests plus the existing ordering and ebook tests.

- [ ] **Step 6: Run the whole recommendation package**

Run:

```bash
go test ./internal/recommendations -count=1
```

Expected: PASS, with only environment-dependent integration tests explicitly skipped when their documented prerequisites are absent.

- [ ] **Step 7: Commit Task 2**

```bash
git add internal/recommendations/signals.go internal/recommendations/signals_test.go
git commit -m "fix(recommendations): roll episode anchors up to series"
```

---

### Task 3: Verify cache-key consumers and repository quality gates

**Files:**
- Verify: `internal/recommendations/worker.go`
- Verify: `internal/recommendations/reader.go`
- Verify: `docs/superpowers/specs/2026-08-17-because-you-watched-anchor-rollup-design.md`

**Interfaces:**
- Consumes: canonical IDs returned by `SignalReader.RecentCompletedItemIDs`.
- Verifies: the worker passes each canonical ID unchanged to `BecauseYouWatched` and `UpsertRecommendationCache`.
- Verifies: the reader passes each canonical ID unchanged to `GetRecommendationCache`.

- [ ] **Step 1: Confirm both automatic-anchor consumers share the signal boundary**

Run:

```bash
rg -n -A18 -B4 'RecentCompletedItemIDs' internal/recommendations/worker.go internal/recommendations/reader.go
```

Expected: both call `RecentCompletedItemIDs`; the worker uses the returned `sourceItemID` for generation and cache upsert, and the reader uses the returned `sourceID` for cache lookup. Do not add duplicate normalization in either consumer.

- [ ] **Step 2: Format and inspect the patch**

Run:

```bash
gofmt -w internal/recommendations/repo.go internal/recommendations/repo_test.go internal/recommendations/signals.go internal/recommendations/signals_test.go
git diff --check
git diff -- internal/recommendations/repo.go internal/recommendations/repo_test.go internal/recommendations/signals.go internal/recommendations/signals_test.go
```

Expected: formatting succeeds, `git diff --check` exits 0, and the diff contains no Web UI, API contract, or explicit-anchor changes.

- [ ] **Step 3: Run focused and full verification**

Run:

```bash
go test ./internal/recommendations -count=1
make test-go
make lint
make verify-local-paths
```

Expected: recommendation and full Go tests pass; local-path verification passes. The full-tree linter may report pre-existing findings documented by `AGENTS.md`; record any output and confirm no new finding is on a changed line.

- [ ] **Step 4: Review requirements against the spec**

Confirm every requirement in `docs/superpowers/specs/2026-08-17-because-you-watched-anchor-rollup-design.md` has evidence:

- automatic episodes resolve to series IDs;
- newest canonical activity wins;
- duplicates collapse before the limit;
- older distinct anchors fill remaining slots;
- non-episodes and unknown IDs remain unchanged;
- repository and user-store paths match;
- worker writes and reader reads the same canonical cache key; and
- explicit anchors and public contracts are untouched.

- [ ] **Step 5: Commit any verification-only adjustments**

If formatting or a test-only correction changed files, commit only those scoped adjustments:

```bash
git add internal/recommendations/repo.go internal/recommendations/repo_test.go internal/recommendations/signals.go internal/recommendations/signals_test.go
git commit -m "test(recommendations): verify automatic anchor rollup"
```
