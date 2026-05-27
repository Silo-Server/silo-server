# TMDB Duplicate Tie-Breaker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Auto-match duplicate same-title/year provider candidates when one candidate has clearly richer metadata, while still refusing uncertain matches.

**Architecture:** Keep the existing title/year/ID scorer as the primary gate. Add a secondary detail-score path that runs only for near-tied duplicate candidates, enriches those candidates through the configured metadata provider chain, and accepts a winner only when the richness gap is strong. Manual match search can keep showing all candidates unchanged.

**Tech Stack:** Go, PostgreSQL-backed metadata service, existing metadata provider interfaces, `go test`.

---

## File Structure

- Modify `internal/metadata/match_candidates.go`
  - Add detail-score fields to `MatchCandidate`.
  - Add pure helper functions for duplicate-tie detection and metadata richness scoring.
  - Update `selectInitialMatchCandidate` to use detail score only when the normal score gap rejects an otherwise duplicate tie.

- Modify `internal/metadata/match_candidates_test.go`
  - Add focused unit tests for TMDB duplicate tie resolution.
  - Add tests proving detail score does not override non-duplicate near matches.

- Modify `internal/metadata/service.go`
  - Enrich candidate detail scores before selecting an initial match.
  - Use the existing configured provider chain and `MetadataProvider.GetMetadata`, so the behavior works with installed TMDB/TVDB plugins instead of hard-coding a TMDB client.

- Modify `internal/metadata/service_test.go` or the nearest existing service-level test file if `service_test.go` already contains `MetadataService.Process` fakes
  - Add one integration-style unit test proving two identical TMDB candidates can be disambiguated after detail enrichment.

Do not add migrations. Do not add frontend changes. Do not persist the detail score; it is a transient matching decision signal.

---

### Task 1: Add Detail-Score Tie-Breaker Unit Tests

**Files:**
- Modify: `internal/metadata/match_candidates_test.go`

- [ ] **Step 1: Add failing tests for duplicate tie selection**

Append these tests after `TestSelectInitialMatchCandidate_AcceptsProviderTitleWithRepeatedYear`:

```go
func TestSelectInitialMatchCandidate_UsesDetailScoreForDuplicateProviderTie(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "UFC 4 Revenge of the Warriors",
			Year:  1994,
			Type:  "movie",
		},
		[]MatchCandidate{
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "1558410"},
				Sources:     []string{"tmdb"},
				DetailScore: 18,
			},
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "17508", "imdb": "tt0487980"},
				Sources:     []string{"tmdb"},
				DetailScore: 46,
			},
		},
	)
	if !ok || winner == nil {
		t.Fatal("expected richer duplicate TMDB candidate to be accepted")
	}
	if got := winner.ProviderIDs["tmdb"]; got != "17508" {
		t.Fatalf("winner tmdb = %q, want 17508", got)
	}
}

func TestSelectInitialMatchCandidate_RejectsDuplicateTieWithoutClearDetailGap(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "UFC 4 Revenge of the Warriors",
			Year:  1994,
			Type:  "movie",
		},
		[]MatchCandidate{
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "1558410"},
				Sources:     []string{"tmdb"},
				DetailScore: 28,
			},
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "17508"},
				Sources:     []string{"tmdb"},
				DetailScore: 34,
			},
		},
	)
	if ok || winner != nil {
		t.Fatal("expected duplicate tie without clear detail gap to remain unmatched")
	}
}

func TestSelectInitialMatchCandidate_DetailScoreDoesNotOverrideDifferentTitleTie(t *testing.T) {
	winner, ok := selectInitialMatchCandidate(
		&MatchHints{
			Title: "UFC 4 Revenge of the Warriors",
			Year:  1994,
			Type:  "movie",
		},
		[]MatchCandidate{
			{
				Title:       "UFC 4: Revenge of the Warriors",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "17508"},
				Sources:     []string{"tmdb"},
				DetailScore: 22,
			},
			{
				Title:       "UFC 4: The Alternate Fights",
				Year:        1994,
				ContentType: "movie",
				ProviderIDs: map[string]string{"tmdb": "999999"},
				Sources:     []string{"tmdb"},
				DetailScore: 80,
			},
		},
	)
	if ok || winner != nil {
		t.Fatal("expected richer different-title candidate to be rejected")
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
go test ./internal/metadata -run 'TestSelectInitialMatchCandidate_UsesDetailScoreForDuplicateProviderTie|TestSelectInitialMatchCandidate_RejectsDuplicateTieWithoutClearDetailGap|TestSelectInitialMatchCandidate_DetailScoreDoesNotOverrideDifferentTitleTie' -count=1
```

Expected: fail because `MatchCandidate.DetailScore` does not exist.

- [ ] **Step 3: Commit the failing tests**

```bash
git add internal/metadata/match_candidates_test.go
git commit -m "test(metadata): cover duplicate candidate tie breaking"
```

---

### Task 2: Implement Pure Detail-Score Selection

**Files:**
- Modify: `internal/metadata/match_candidates.go`

- [ ] **Step 1: Add transient detail fields to `MatchCandidate`**

Update the struct near the top of `internal/metadata/match_candidates.go`:

```go
type MatchCandidate struct {
	Title          string            `json:"title"`
	Year           int               `json:"year"`
	ContentType    string            `json:"content_type"`
	ProviderIDs    map[string]string `json:"provider_ids"`
	ImageURL       string            `json:"image_url,omitempty"`
	Overview       string            `json:"overview,omitempty"`
	Sources        []string          `json:"sources"`
	AgreementHints []string          `json:"agreement_hints"`
	DetailScore    int               `json:"-"`
}
```

- [ ] **Step 2: Add duplicate-tie helper constants and functions**

Add these helpers after `providerIDRichness`:

```go
const (
	minimumDetailTieBreakScore = 20
	minimumDetailTieBreakGap   = 12
)

func duplicateTieBreakWinner(hints *MatchHints, scoredCandidates []scoredMatchCandidate) (*MatchCandidate, bool) {
	if hints == nil || len(scoredCandidates) < 2 {
		return nil, false
	}
	best := scoredCandidates[0]
	if best.candidate.DetailScore < minimumDetailTieBreakScore {
		return nil, false
	}

	contenders := []scoredMatchCandidate{best}
	for i := 1; i < len(scoredCandidates); i++ {
		next := scoredCandidates[i]
		if best.score-next.score >= 15 {
			break
		}
		if duplicateTieBreakComparable(hints, best.candidate, next.candidate) {
			contenders = append(contenders, next)
		}
	}
	if len(contenders) < 2 {
		return nil, false
	}

	sort.SliceStable(contenders, func(i, j int) bool {
		return contenders[i].candidate.DetailScore > contenders[j].candidate.DetailScore
	})
	if contenders[0].candidate.DetailScore-contenders[1].candidate.DetailScore < minimumDetailTieBreakGap {
		return nil, false
	}
	return &contenders[0].candidate, true
}

func duplicateTieBreakComparable(hints *MatchHints, left, right MatchCandidate) bool {
	if left.Year != 0 && right.Year != 0 && left.Year != right.Year {
		return false
	}
	if hints.Year != 0 {
		if left.Year != 0 && left.Year != hints.Year {
			return false
		}
		if right.Year != 0 && right.Year != hints.Year {
			return false
		}
	}
	if strings.TrimSpace(left.ContentType) != "" &&
		strings.TrimSpace(right.ContentType) != "" &&
		!strings.EqualFold(left.ContentType, right.ContentType) {
		return false
	}
	if inferTitleSimilarity(left.Title, right.Title, hints.Year) != 1 {
		return false
	}
	if inferTitleSimilarity(hints.Title, left.Title, hints.Year) != 1 {
		return false
	}
	if inferTitleSimilarity(hints.Title, right.Title, hints.Year) != 1 {
		return false
	}
	return samePrimaryProvider(left.ProviderIDs, right.ProviderIDs)
}

func samePrimaryProvider(left, right map[string]string) bool {
	for _, key := range canonicalCandidateIDKeys {
		leftValue := strings.TrimSpace(left[key])
		rightValue := strings.TrimSpace(right[key])
		if leftValue != "" && rightValue != "" {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Promote the local scored type so helpers can use it**

Move the `scored` type out of `selectInitialMatchCandidate` and rename it:

```go
type scoredMatchCandidate struct {
	candidate MatchCandidate
	score     float64
}
```

Place it immediately above `selectInitialMatchCandidate`.

- [ ] **Step 4: Update `selectInitialMatchCandidate` to use the helper**

Replace the first half of `selectInitialMatchCandidate` with:

```go
func selectInitialMatchCandidate(hints *MatchHints, candidates []MatchCandidate) (*MatchCandidate, bool) {
	if len(candidates) == 0 {
		return nil, false
	}

	scoredCandidates := make([]scoredMatchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		scoredCandidates = append(scoredCandidates, scoredMatchCandidate{
			candidate: candidate,
			score:     scoreMatchCandidate(hints, candidate),
		})
	}
	sort.SliceStable(scoredCandidates, func(i, j int) bool {
		return scoredCandidates[i].score > scoredCandidates[j].score
	})

	best := scoredCandidates[0]
	if trustedHintIDsPresent(hints) {
		if candidateMatchesTrustedIDs(hints, best.candidate) {
			return &best.candidate, true
		}
		return nil, false
	}

	if best.score < 55 {
		return nil, false
	}
	if len(scoredCandidates) == 1 {
		if best.score < 70 {
			return nil, false
		}
		return &best.candidate, true
	}
	if best.score-scoredCandidates[1].score < 15 {
		return duplicateTieBreakWinner(hints, scoredCandidates)
	}
	return &best.candidate, true
}
```

- [ ] **Step 5: Run focused tests and verify they pass**

Run:

```bash
go test ./internal/metadata -run 'TestSelectInitialMatchCandidate_UsesDetailScoreForDuplicateProviderTie|TestSelectInitialMatchCandidate_RejectsDuplicateTieWithoutClearDetailGap|TestSelectInitialMatchCandidate_DetailScoreDoesNotOverrideDifferentTitleTie' -count=1
```

Expected: pass.

- [ ] **Step 6: Run nearby candidate tests**

Run:

```bash
go test ./internal/metadata -run 'TestSelectInitialMatchCandidate|TestSelectRefreshMatchCandidate' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit pure selector change**

```bash
git add internal/metadata/match_candidates.go internal/metadata/match_candidates_test.go
git commit -m "fix(metadata): resolve rich duplicate candidate ties"
```

---

### Task 3: Add Metadata Completeness Scoring

**Files:**
- Modify: `internal/metadata/match_candidates.go`
- Modify: `internal/metadata/match_candidates_test.go`

- [ ] **Step 1: Add failing tests for metadata completeness**

Append these tests near the other scoring tests in `internal/metadata/match_candidates_test.go`:

```go
func TestMetadataCompletenessScorePrefersExternalIDsAndRichFields(t *testing.T) {
	rich := &MetadataResult{
		HasMetadata:   true,
		ProviderIDs:   map[string]string{"tmdb": "17508", "imdb": "tt0487980"},
		Title:         "UFC 4: Revenge of the Warriors",
		Overview:      "UFC 4 was a mixed martial arts event.",
		Year:          1994,
		Runtime:       99,
		PosterPath:    "tmdb://poster/17508.jpg",
		BackdropPath:  "tmdb://backdrop/17508.jpg",
		Tagline:       "Revenge of the Warriors",
		OriginalTitle: "UFC 4: Revenge of the Warriors",
		Studios:       []string{"Ultimate Fighting Championship"},
		Keywords:      []string{"mixed martial arts"},
		Ratings:       Ratings{TMDB: 7.4},
		People: []models.ItemPerson{
			{Name: "Royce Gracie", Role: "Self", Type: "actor", OrderIndex: 0},
			{Name: "Dan Severn", Role: "Self", Type: "actor", OrderIndex: 1},
		},
	}
	thin := &MetadataResult{
		HasMetadata: true,
		ProviderIDs: map[string]string{"tmdb": "1558410"},
		Title:       "UFC 4: Revenge of the Warriors",
		Overview:    "UFC 4 used an eight-man tournament format.",
		Year:        1994,
		Runtime:     90,
		PosterPath:  "tmdb://poster/1558410.jpg",
		People: []models.ItemPerson{
			{Name: "Marcus Bossett", Type: "actor", OrderIndex: 0},
		},
	}

	richScore := metadataCompletenessScore(rich)
	thinScore := metadataCompletenessScore(thin)
	if richScore-thinScore < minimumDetailTieBreakGap {
		t.Fatalf("richScore - thinScore = %d, want at least %d; rich=%d thin=%d",
			richScore-thinScore, minimumDetailTieBreakGap, richScore, thinScore)
	}
}

func TestMetadataCompletenessScoreHandlesNilAndEmptyMetadata(t *testing.T) {
	if got := metadataCompletenessScore(nil); got != 0 {
		t.Fatalf("nil score = %d, want 0", got)
	}
	if got := metadataCompletenessScore(&MetadataResult{}); got != 0 {
		t.Fatalf("empty score = %d, want 0", got)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
go test ./internal/metadata -run 'TestMetadataCompletenessScore' -count=1
```

Expected: fail because `metadataCompletenessScore` is undefined.

- [ ] **Step 3: Add completeness scoring helper**

Add this helper after `providerIDRichness` in `internal/metadata/match_candidates.go`:

```go
func metadataCompletenessScore(result *MetadataResult) int {
	if result == nil || !result.HasMetadata {
		return 0
	}
	score := 0
	if strings.TrimSpace(result.ProviderIDs["imdb"]) != "" {
		score += 18
	}
	if strings.TrimSpace(result.ProviderIDs["tvdb"]) != "" {
		score += 18
	}
	if strings.TrimSpace(result.ProviderIDs["tmdb"]) != "" {
		score += 4
	}
	if strings.TrimSpace(result.Title) != "" {
		score += 4
	}
	if strings.TrimSpace(result.OriginalTitle) != "" {
		score += 2
	}
	if strings.TrimSpace(result.Overview) != "" {
		score += 6
	}
	if result.Year != 0 {
		score += 4
	}
	if result.Runtime > 0 {
		score += 3
	}
	if strings.TrimSpace(result.PosterPath) != "" {
		score += 4
	}
	if strings.TrimSpace(result.BackdropPath) != "" {
		score += 5
	}
	if strings.TrimSpace(result.Homepage) != "" {
		score += 3
	}
	if len(result.Studios) > 0 {
		score += 3
	}
	if len(result.Networks) > 0 {
		score += 2
	}
	if len(result.Countries) > 0 {
		score += 2
	}
	if len(result.Keywords) > 0 {
		score += 2
	}
	if result.Ratings.TMDB > 0 {
		score += 2
	}
	if strings.TrimSpace(result.ContentRating) != "" {
		score += 2
	}
	score += boundedCountScore(len(result.People), 10)
	return score
}

func boundedCountScore(count, max int) int {
	if count <= 0 {
		return 0
	}
	if count > max {
		return max
	}
	return count
}
```

- [ ] **Step 4: Run completeness tests**

Run:

```bash
go test ./internal/metadata -run 'TestMetadataCompletenessScore' -count=1
```

Expected: pass.

- [ ] **Step 5: Run all candidate tests**

Run:

```bash
go test ./internal/metadata -run 'TestSelectInitialMatchCandidate|TestSelectRefreshMatchCandidate|TestMetadataCompletenessScore' -count=1
```

Expected: pass.

- [ ] **Step 6: Commit completeness scoring**

```bash
git add internal/metadata/match_candidates.go internal/metadata/match_candidates_test.go
git commit -m "fix(metadata): score candidate metadata completeness"
```

---

### Task 4: Enrich Near-Duplicate Candidates Before Initial Selection

**Files:**
- Modify: `internal/metadata/service.go`

- [ ] **Step 1: Add candidate enrichment call in initial match flow**

In `internal/metadata/service.go`, inside the `ModeInitialMatch` case, find:

```go
candidates := NormalizeCandidates(allResults, contentType)
if winner, ok := selectInitialMatchCandidate(req.Hints, candidates); ok && winner != nil {
	for k, v := range winner.ProviderIDs {
		if v != "" {
			accumulatedIDs[k] = v
		}
	}
}
```

Replace it with:

```go
candidates := NormalizeCandidates(allResults, contentType)
s.enrichInitialMatchDuplicateCandidates(ctx, req, itemChain, candidates)
if winner, ok := selectInitialMatchCandidate(req.Hints, candidates); ok && winner != nil {
	for k, v := range winner.ProviderIDs {
		if v != "" {
			accumulatedIDs[k] = v
		}
	}
}
```

- [ ] **Step 2: Add enrichment helpers**

Add these helpers near `processInternal` helper functions in `internal/metadata/service.go`:

```go
func (s *MetadataService) enrichInitialMatchDuplicateCandidates(
	ctx context.Context,
	req ProcessRequest,
	itemChain []Provider,
	candidates []MatchCandidate,
) {
	if req.Hints == nil || len(candidates) < 2 {
		return
	}
	indexes := candidateIndexesNeedingDetailScores(req.Hints, candidates)
	if len(indexes) < 2 {
		return
	}
	for _, index := range indexes {
		candidates[index].DetailScore = s.detailScoreForCandidate(ctx, req, itemChain, candidates[index])
	}
}

func candidateIndexesNeedingDetailScores(hints *MatchHints, candidates []MatchCandidate) []int {
	if hints == nil || len(candidates) < 2 {
		return nil
	}
	scoredCandidates := make([]scoredMatchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		scoredCandidates = append(scoredCandidates, scoredMatchCandidate{
			candidate: candidate,
			score:     scoreMatchCandidate(hints, candidate),
		})
	}
	sort.SliceStable(scoredCandidates, func(i, j int) bool {
		return scoredCandidates[i].score > scoredCandidates[j].score
	})
	if scoredCandidates[0].score < 55 {
		return nil
	}
	if len(scoredCandidates) < 2 || scoredCandidates[0].score-scoredCandidates[1].score >= 15 {
		return nil
	}

	indexes := make([]int, 0, len(candidates))
	for index, candidate := range candidates {
		if duplicateTieBreakComparable(hints, scoredCandidates[0].candidate, candidate) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func (s *MetadataService) detailScoreForCandidate(
	ctx context.Context,
	req ProcessRequest,
	itemChain []Provider,
	candidate MatchCandidate,
) int {
	accumulator := &MetadataResult{
		ProviderIDs: copyMap(candidate.ProviderIDs),
	}
	for _, provider := range itemChain {
		metadataProvider, ok := provider.(MetadataProvider)
		if !ok {
			continue
		}
		result, err := metadataProvider.GetMetadata(ctx, MetadataRequest{
			ProviderIDs:               copyMap(accumulator.ProviderIDs),
			ContentType:               candidate.ContentType,
			Language:                  req.Language,
			FilePath:                  req.Hints.FilePath,
			RepresentativeFilePath:    req.Hints.RepresentativeFilePath,
			ObservedRootPath:          req.Hints.ObservedRootPath,
			AllGroupFilePaths:         append([]string(nil), req.Hints.AllGroupFilePaths...),
			PrimarySidecarSearchPaths: append([]string(nil), req.Hints.PrimarySidecarSearchPaths...),
			GroupTitle:                req.Hints.Title,
			GroupYear:                 req.Hints.Year,
		})
		if err != nil || result == nil || !result.HasMetadata {
			continue
		}
		mergeProviderIDs(accumulator, result)
		mergeMetadataResult(accumulator, result)
	}
	return metadataCompletenessScore(accumulator)
}
```

- [ ] **Step 3: Add `sort` import if needed**

If `internal/metadata/service.go` does not already import `sort`, add it to the existing import block:

```go
import (
	"sort"
)
```

Do not create a second import block.

- [ ] **Step 4: Run compile-focused metadata tests**

Run:

```bash
go test ./internal/metadata -run 'TestSelectInitialMatchCandidate|TestMetadataCompletenessScore' -count=1
```

Expected: pass.

- [ ] **Step 5: Run package tests**

Run:

```bash
go test ./internal/metadata -count=1
```

Expected: pass.

- [ ] **Step 6: Commit service enrichment**

```bash
git add internal/metadata/service.go internal/metadata/match_candidates.go internal/metadata/match_candidates_test.go
git commit -m "fix(metadata): enrich duplicate candidates before auto match"
```

---

### Task 5: Add Service-Level Regression Test

**Files:**
- Modify: `internal/metadata/service_test.go` if it exists
- Otherwise modify the existing metadata service test file that already defines fake metadata providers

- [ ] **Step 1: Locate existing service fake providers**

Run:

```bash
rg -n "type .*Provider|GetMetadata\\(|Search\\(" internal/metadata/*test.go
```

Expected: output includes existing fake provider definitions. Use the file that already tests `MetadataService.Process`.

- [ ] **Step 2: Add a fake provider if the selected test file does not already have one**

Add this fake to the selected test file:

```go
type duplicateSearchAndMetadataProvider struct {
	searchResults []SearchResult
	metadataByID map[string]*MetadataResult
}

func (p *duplicateSearchAndMetadataProvider) Slug() string { return "tmdb" }

func (p *duplicateSearchAndMetadataProvider) Name() string { return "TMDB" }

func (p *duplicateSearchAndMetadataProvider) ForTypes() []string {
	return []string{"movie"}
}

func (p *duplicateSearchAndMetadataProvider) Search(context.Context, SearchQuery) ([]SearchResult, error) {
	return append([]SearchResult(nil), p.searchResults...), nil
}

func (p *duplicateSearchAndMetadataProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	tmdbID := req.ProviderIDs["tmdb"]
	if result, ok := p.metadataByID[tmdbID]; ok {
		clone := *result
		clone.ProviderIDs = copyMap(result.ProviderIDs)
		clone.People = append([]models.ItemPerson(nil), result.People...)
		return &clone, nil
	}
	return nil, ErrMetadataNotFound
}
```

- [ ] **Step 3: Add regression test for UFC 4 duplicate selection**

Add this test to the selected file, adapting only the existing service-construction helper name if the file already has one:

```go
func TestProcessInitialMatchSelectsRicherDuplicateTMDBCandidate(t *testing.T) {
	ctx := context.Background()
	provider := &duplicateSearchAndMetadataProvider{
		searchResults: []SearchResult{
			{
				Name:        "UFC 4: Revenge of the Warriors",
				Year:        1994,
				Provider:    "tmdb",
				ProviderIDs: map[string]string{"tmdb": "1558410"},
				ImageURL:    "tmdb://poster/1558410.jpg",
				Overview:    "UFC 4 used an eight-man tournament format.",
			},
			{
				Name:        "UFC 4: Revenge of the Warriors",
				Year:        1994,
				Provider:    "tmdb",
				ProviderIDs: map[string]string{"tmdb": "17508"},
				ImageURL:    "tmdb://poster/17508.jpg",
				Overview:    "UFC 4 was a mixed martial arts event.",
			},
		},
		metadataByID: map[string]*MetadataResult{
			"1558410": {
				HasMetadata: true,
				ProviderIDs: map[string]string{"tmdb": "1558410"},
				Title:       "UFC 4: Revenge of the Warriors",
				Overview:    "UFC 4 used an eight-man tournament format.",
				Year:        1994,
				Runtime:     90,
				PosterPath:  "tmdb://poster/1558410.jpg",
			},
			"17508": {
				HasMetadata:  true,
				ProviderIDs:  map[string]string{"tmdb": "17508", "imdb": "tt0487980"},
				Title:        "UFC 4: Revenge of the Warriors",
				Overview:     "UFC 4 was a mixed martial arts event.",
				Year:         1994,
				Runtime:      99,
				PosterPath:   "tmdb://poster/17508.jpg",
				BackdropPath: "tmdb://backdrop/17508.jpg",
				Homepage:     "http://www.ufc.com/index.cfm?fa=eventdetail.fightCard&eid=5",
				People: []models.ItemPerson{
					{Name: "Royce Gracie", Role: "Self", Type: "actor", OrderIndex: 0},
					{Name: "Dan Severn", Role: "Self", Type: "actor", OrderIndex: 1},
					{Name: "Keith Hackney", Role: "Self", Type: "actor", OrderIndex: 2},
				},
			},
		},
	}

	service := newTestMetadataService(t, []Provider{provider})
	result, err := service.Process(ctx, ProcessRequest{
		ContentID: "local-ufc-4",
		FolderID:  "7",
		Mode:      ModeInitialMatch,
		Hints: &MatchHints{
			ContentID: "local-ufc-4",
			Title:     "UFC 4 Revenge of the Warriors",
			Year:      1994,
			Type:      "movie",
			FilePath:  "/sports/movies/UFC/UFC 4 Revenge of the Warriors (1994)/UFC 4 Revenge of the Warriors (1994) SDTV.avi",
		},
	})
	if err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("Process result = %#v, want updated result", result)
	}

	item := mustGetTestMediaItem(t, service, "local-ufc-4")
	if item.TmdbID != "17508" {
		t.Fatalf("item.TmdbID = %q, want 17508", item.TmdbID)
	}
	if item.ImdbID != "tt0487980" {
		t.Fatalf("item.ImdbID = %q, want tt0487980", item.ImdbID)
	}
}
```

If the repository uses differently named helpers, keep the same assertions and wire the provider into the existing helper. The test must assert the persisted item has TMDB `17508` and IMDb `tt0487980`.

- [ ] **Step 4: Run the new service test and verify it fails before helper wiring is complete**

Run:

```bash
go test ./internal/metadata -run 'TestProcessInitialMatchSelectsRicherDuplicateTMDBCandidate' -count=1
```

Expected: fail if helper names are not wired yet, or pass if the selected test harness already supports fake chains.

- [ ] **Step 5: Wire the test to existing metadata service test helpers**

Use the selected file’s existing constructors and repositories. The final test must use a real `MetadataService.Process` call, not a direct call to `selectInitialMatchCandidate`.

- [ ] **Step 6: Run the service regression test**

Run:

```bash
go test ./internal/metadata -run 'TestProcessInitialMatchSelectsRicherDuplicateTMDBCandidate' -count=1
```

Expected: pass.

- [ ] **Step 7: Commit regression coverage**

```bash
git add internal/metadata/*test.go
git commit -m "test(metadata): verify rich TMDB duplicate auto match"
```

---

### Task 6: Verify on the Dev Server

**Files:**
- No code files

- [ ] **Step 1: Run targeted local verification**

Run:

```bash
go test ./internal/metadata -run 'TestSelectInitialMatchCandidate|TestSelectRefreshMatchCandidate|TestMetadataCompletenessScore|TestProcessInitialMatchSelectsRicherDuplicateTMDBCandidate' -count=1
```

Expected: pass.

- [ ] **Step 2: Run broader affected package verification**

Run:

```bash
go test ./internal/metadata ./internal/scanner ./internal/libraryingest ./internal/taskmanager -count=1
```

Expected: pass.

- [ ] **Step 3: Deploy to dev**

Run:

```bash
make dev-deploy
```

Expected: build succeeds and Docker Compose restarts the dev server.

- [ ] **Step 4: Confirm dev readiness**

Run:

```bash
ssh root@100.86.116.20 'curl -s http://localhost:8090/api/v1/ready'
```

Expected:

```json
{"status":"ok"}
```

- [ ] **Step 5: Requeue the UFC 4 movie row**

Run:

```bash
ssh root@100.86.116.20 "docker exec silo-postgres-1 psql -U continuum -d continuum -P pager=off -c \"UPDATE movie_match_queue SET available_at = now() - interval '1 hour', last_attempted_at = NULL, updated_at = now() WHERE media_file_id = 2425791;\""
```

Expected:

```text
UPDATE 1
```

- [ ] **Step 6: Trigger or wait for metadata matching**

Run:

```bash
ssh root@100.86.116.20 "docker exec silo-postgres-1 psql -U continuum -d continuum -P pager=off -c \"SELECT media_file_id, available_at, last_attempted_at, attempt_count, last_error FROM movie_match_queue WHERE media_file_id = 2425791;\""
```

Expected after the worker claims the row: `last_attempted_at` is non-null and newer than the requeue time.

- [ ] **Step 7: Verify the item matched to TMDB 17508**

Run:

```bash
ssh root@100.86.116.20 "docker exec silo-postgres-1 psql -U continuum -d continuum -P pager=off -c \"SELECT mf.id AS file_id, mi.content_id, mi.title, mi.year, mi.status, mi.tmdb_id, mi.imdb_id FROM media_files mf JOIN media_items mi ON mi.content_id = mf.content_id WHERE mf.id = 2425791;\""
```

Expected row:

```text
 file_id |     content_id     |             title             | year | status  | tmdb_id |  imdb_id
---------+--------------------+-------------------------------+------+---------+---------+-----------
 2425791 | 126715023410790404 | UFC 4: Revenge of the Warriors | 1994 | matched | 17508   | tt0487980
```

- [ ] **Step 8: Commit any deployment-only notes are not needed**

No commit for dev verification output. Keep the repository clean except for code/test changes.

---

## Self-Review

Spec coverage:
- Auto-match still refuses uncertain duplicate ties: Task 2.
- Correct TMDB duplicate can be selected when metadata richness is clearly better: Tasks 2, 3, 4, 5.
- No hard dependency on TMDB-only client code: Task 4 uses `MetadataProvider`.
- Runtime is weak and does not override richer metadata: Task 3 weights runtime at `3`, external IDs and rich fields higher.
- Manual search remains unchanged: no frontend/API candidate response change is planned.

Placeholder scan:
- No `TBD`, `TODO`, `implement later`, or "write tests for the above" placeholders remain.
- The one service-test helper adaptation step is constrained to existing test harness names and includes exact required assertions.

Type consistency:
- `MatchCandidate.DetailScore` is defined before selector tests use it.
- `scoredMatchCandidate` is used by both `selectInitialMatchCandidate` and service enrichment.
- `metadataCompletenessScore` accepts `*MetadataResult`, matching provider `GetMetadata` results.
