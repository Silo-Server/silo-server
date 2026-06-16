# Unified Literary Works Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a server-owned literary work identity that links ebooks, audiobooks, and future literary formats without collapsing their existing `media_items` identities.

**Architecture:** Add neutral `literary_works` and `literary_work_items` tables, then build an `internal/literaryworks` package for repository, matching, and response assembly. Existing item detail and catalog APIs remain item-based by default, with additive work fields and opt-in grouped catalog results.

**Tech Stack:** Go, pgx, PostgreSQL goose migrations, existing catalog/detail handlers, existing ebook reader progress and audiobook playback progress storage, chi routes, standard Go tests.

---

## File Structure

- Create `migrations/sql/20260609150000_literary_works.sql`: work tables, indexes, and cleanup trigger.
- Create `internal/literaryworks/types.go`: API/domain response structs and constants.
- Create `internal/literaryworks/normalize.go`: title/author/series normalization helpers.
- Create `internal/literaryworks/repository.go`: SQL read/write methods.
- Create `internal/literaryworks/matcher.go`: candidate scoring and automatic linking rules.
- Create `internal/literaryworks/service.go`: work detail assembly and admin orchestration.
- Create `internal/literaryworks/*_test.go`: focused repository, matcher, and service tests.
- Modify `internal/catalog/detail.go`: add optional work fields to `ItemDetail` and a tiny `WorkSummaryProvider` interface.
- Modify `internal/api/handlers/catalog_resources.go`: ensure work fields are enriched on item detail responses.
- Create `internal/api/handlers/literary_works.go`: public work detail and admin link/match endpoints.
- Modify `internal/api/router.go`: register `/api/v1/works/{work_id}` and admin literary work routes.
- Modify catalog request/query files for `group=work` only after the core work endpoint passes.

## Task 1: Prepare The Correct Feature Base

**Files:**
- No source files changed.

- [ ] **Step 1: Create an implementation worktree from the ebook stack head**

Run:

```bash
cd /Users/jimcole/projects/silo/silo-server
git fetch origin
git worktree add ../silo-server-literary-works origin/work/ebook-reader-ruler-profiles
cd ../silo-server-literary-works
git switch -c feat/literary-works
```

Expected: new branch `feat/literary-works` exists and includes `internal/ebooks`, `internal/scanner/ebook_scan.go`, and `internal/api/handlers/ebook_reader.go`.

- [ ] **Step 2: Copy the approved spec into the feature worktree if absent**

Run:

```bash
test -f docs/superpowers/specs/2026-06-09-literary-works-design.md || \
  cp ../silo-server/docs/superpowers/specs/2026-06-09-literary-works-design.md docs/superpowers/specs/
```

Expected: `docs/superpowers/specs/2026-06-09-literary-works-design.md` exists in the worktree.

- [ ] **Step 3: Verify the starting point**

Run:

```bash
go test ./internal/catalog ./internal/scanner ./internal/ebooks -count=1
```

Expected: packages pass before literary work changes begin. If this fails, stop and fix/rebase the base branch first.

## Task 2: Add The Literary Work Schema

**Files:**
- Create: `migrations/sql/20260609150000_literary_works.sql`
- Test: migration smoke through package tests that load migrations.

- [ ] **Step 1: Write the migration**

Create `migrations/sql/20260609150000_literary_works.sql`:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS literary_works (
    work_id TEXT PRIMARY KEY,
    canonical_title TEXT NOT NULL,
    sort_title TEXT,
    normalized_title TEXT NOT NULL,
    primary_author_key TEXT NOT NULL DEFAULT '',
    primary_cover_content_id TEXT REFERENCES media_items(content_id) ON DELETE SET NULL,
    description TEXT,
    published_date DATE,
    publisher TEXT,
    genres TEXT[] NOT NULL DEFAULT '{}',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS literary_work_items (
    work_id TEXT NOT NULL REFERENCES literary_works(work_id) ON DELETE CASCADE,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    format_type TEXT NOT NULL,
    link_source TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 1,
    confirmed_at TIMESTAMPTZ,
    ignored_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (work_id, content_id),
    UNIQUE (content_id),
    CHECK (format_type IN ('ebook', 'audiobook', 'comic', 'manga')),
    CHECK (link_source IN ('manual', 'external_id', 'metadata_match', 'series_match', 'scan_seed')),
    CHECK (confidence >= 0 AND confidence <= 1)
);

CREATE TABLE IF NOT EXISTS literary_work_match_decisions (
    source_content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    target_content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    decision TEXT NOT NULL,
    created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_content_id, target_content_id),
    CHECK (source_content_id <> target_content_id),
    CHECK (decision IN ('confirmed', 'ignored'))
);

CREATE INDEX IF NOT EXISTS idx_literary_works_normalized
    ON literary_works (normalized_title, primary_author_key);

CREATE INDEX IF NOT EXISTS idx_literary_work_items_content
    ON literary_work_items (content_id);

CREATE INDEX IF NOT EXISTS idx_literary_work_items_format
    ON literary_work_items (format_type, work_id);

CREATE INDEX IF NOT EXISTS idx_literary_work_match_decisions_target
    ON literary_work_match_decisions (target_content_id, decision);

CREATE OR REPLACE FUNCTION delete_empty_literary_works()
RETURNS TRIGGER AS $$
BEGIN
    DELETE FROM literary_works lw
    WHERE lw.work_id = OLD.work_id
      AND NOT EXISTS (
          SELECT 1 FROM literary_work_items lwi WHERE lwi.work_id = OLD.work_id
      );
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_delete_empty_literary_works ON literary_work_items;
CREATE TRIGGER trg_delete_empty_literary_works
AFTER DELETE ON literary_work_items
FOR EACH ROW EXECUTE FUNCTION delete_empty_literary_works();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_delete_empty_literary_works ON literary_work_items;
DROP FUNCTION IF EXISTS delete_empty_literary_works();
DROP TABLE IF EXISTS literary_work_match_decisions;
DROP TABLE IF EXISTS literary_work_items;
DROP TABLE IF EXISTS literary_works;
-- +goose StatementEnd
```

- [ ] **Step 2: Run migration-aware tests**

Run:

```bash
go test ./migrations -count=1
```

Expected: migrations package passes.

- [ ] **Step 3: Commit**

Run:

```bash
git add migrations/sql/20260609150000_literary_works.sql
git commit -m "feat(literary): add work link schema"
```

## Task 3: Add Domain Types And Normalization

**Files:**
- Create: `internal/literaryworks/types.go`
- Create: `internal/literaryworks/normalize.go`
- Test: `internal/literaryworks/normalize_test.go`

- [ ] **Step 1: Write failing normalization tests**

Create `internal/literaryworks/normalize_test.go`:

```go
package literaryworks

import "testing"

func TestNormalizeKey(t *testing.T) {
	tests := map[string]string{
		"Project Hail Mary":              "project hail mary",
		"The Last Adventure: A Novel":    "last adventure novel",
		"  A   Constance-Verity Tale!  ": "constance verity tale",
	}
	for input, want := range tests {
		if got := normalizeKey(input); got != want {
			t.Fatalf("normalizeKey(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPersonKey(t *testing.T) {
	if got := personKey([]string{" Andy Weir ", "Other"}); got != "andy weir" {
		t.Fatalf("personKey = %q, want primary normalized author", got)
	}
}
```

- [ ] **Step 2: Run the failing tests**

Run:

```bash
go test ./internal/literaryworks -run 'TestNormalizeKey|TestPersonKey' -count=1
```

Expected: FAIL because package/files do not exist.

- [ ] **Step 3: Add types and normalization**

Create `internal/literaryworks/types.go`:

```go
package literaryworks

import "time"

const (
	FormatEbook     = "ebook"
	FormatAudiobook = "audiobook"
	FormatComic     = "comic"
	FormatManga     = "manga"

	LinkManual        = "manual"
	LinkExternalID    = "external_id"
	LinkMetadataMatch = "metadata_match"
	LinkSeriesMatch   = "series_match"
	LinkScanSeed      = "scan_seed"

	DecisionConfirmed = "confirmed"
	DecisionIgnored   = "ignored"
)

type Work struct {
	WorkID                string
	CanonicalTitle        string
	SortTitle             string
	NormalizedTitle       string
	PrimaryAuthorKey      string
	PrimaryCoverContentID string
	Description           string
	PublishedDate         *time.Time
	Publisher             string
	Genres                []string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type WorkItem struct {
	WorkID      string
	ContentID   string
	FormatType  string
	LinkSource  string
	Confidence  float64
	ConfirmedAt *time.Time
	IgnoredAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Candidate struct {
	SourceContentID string            `json:"source_content_id"`
	TargetContentID string            `json:"target_content_id"`
	TargetWorkID    string            `json:"target_work_id,omitempty"`
	Score           float64           `json:"score"`
	LinkSource      string            `json:"link_source"`
	Evidence        map[string]string `json:"evidence"`
}

type WorkSummary struct {
	WorkID  string              `json:"work_id,omitempty"`
	Title   string              `json:"work_title,omitempty"`
	Formats []WorkFormatSummary `json:"work_formats,omitempty"`
}

type WorkFormatSummary struct {
	Type      string `json:"type"`
	ContentID string `json:"content_id"`
	LibraryID int   `json:"library_id,omitempty"`
}
```

Create `internal/literaryworks/normalize.go`:

```go
package literaryworks

import (
	"strings"
	"unicode"
)

var leadingArticles = map[string]struct{}{
	"a":   {},
	"an":  {},
	"the": {},
}

func normalizeKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastSpace := true
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	parts := strings.Fields(b.String())
	if len(parts) > 0 {
		if _, ok := leadingArticles[parts[0]]; ok {
			parts = parts[1:]
		}
	}
	return strings.Join(parts, " ")
}

func personKey(names []string) string {
	for _, name := range names {
		if key := normalizeKey(name); key != "" {
			return key
		}
	}
	return ""
}
```

- [ ] **Step 4: Run the tests**

Run:

```bash
go test ./internal/literaryworks -run 'TestNormalizeKey|TestPersonKey' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/literaryworks
git commit -m "feat(literary): add work domain primitives"
```

## Task 4: Implement Repository CRUD

**Files:**
- Create: `internal/literaryworks/repository.go`
- Test: `internal/literaryworks/repository_test.go`

- [ ] **Step 1: Write repository tests**

Create `internal/literaryworks/repository_test.go` with tests that use the repo test database helper pattern already present in catalog tests. The core assertions must be:

```go
func TestRepositoryLinkAndFetchSummary(t *testing.T) {
	ctx := context.Background()
	pool := newLiteraryWorksTestPool(t)
	seedLiteraryMediaItem(t, pool, "ebook-1", "ebook", "Project Hail Mary")
	seedLiteraryMediaItem(t, pool, "audio-1", "audiobook", "Project Hail Mary")

	repo := NewRepository(pool)
	work, err := repo.CreateWork(ctx, CreateWorkParams{
		WorkID: "work-1", CanonicalTitle: "Project Hail Mary",
		NormalizedTitle: "project hail mary", PrimaryAuthorKey: "andy weir",
	})
	if err != nil {
		t.Fatal(err)
	}
	if work.WorkID != "work-1" {
		t.Fatalf("work id = %q", work.WorkID)
	}
	if err := repo.LinkItems(ctx, "work-1", []LinkItemParams{
		{ContentID: "ebook-1", FormatType: FormatEbook, LinkSource: LinkManual, Confidence: 1},
		{ContentID: "audio-1", FormatType: FormatAudiobook, LinkSource: LinkManual, Confidence: 1},
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := repo.GetSummaryForContentID(ctx, "ebook-1")
	if err != nil {
		t.Fatal(err)
	}
	if summary.WorkID != "work-1" || len(summary.Formats) != 2 {
		t.Fatalf("summary = %#v, want work with two formats", summary)
	}
}
```

- [ ] **Step 2: Run the failing test**

Run:

```bash
go test ./internal/literaryworks -run TestRepositoryLinkAndFetchSummary -count=1
```

Expected: FAIL because repository functions are undefined.

- [ ] **Step 3: Implement repository methods**

Create `internal/literaryworks/repository.go` with:

```go
package literaryworks

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

type CreateWorkParams struct {
	WorkID           string
	CanonicalTitle   string
	SortTitle        string
	NormalizedTitle  string
	PrimaryAuthorKey string
	Description      string
	Publisher        string
	Genres           []string
}

type LinkItemParams struct {
	ContentID  string
	FormatType string
	LinkSource string
	Confidence float64
}

func (r *Repository) CreateWork(ctx context.Context, p CreateWorkParams) (*Work, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO literary_works (
			work_id, canonical_title, sort_title, normalized_title,
			primary_author_key, description, publisher, genres
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (work_id) DO UPDATE SET
			canonical_title = EXCLUDED.canonical_title,
			sort_title = EXCLUDED.sort_title,
			normalized_title = EXCLUDED.normalized_title,
			primary_author_key = EXCLUDED.primary_author_key,
			description = EXCLUDED.description,
			publisher = EXCLUDED.publisher,
			genres = EXCLUDED.genres,
			updated_at = NOW()
		RETURNING work_id, canonical_title, COALESCE(sort_title, ''), normalized_title,
			primary_author_key, COALESCE(primary_cover_content_id, ''),
			COALESCE(description, ''), NULL::timestamptz, COALESCE(publisher, ''),
			genres, created_at, updated_at
	`, p.WorkID, p.CanonicalTitle, p.SortTitle, p.NormalizedTitle, p.PrimaryAuthorKey, p.Description, p.Publisher, p.Genres)
	return scanWork(row)
}

func (r *Repository) LinkItems(ctx context.Context, workID string, items []LinkItemParams) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, item := range items {
		if item.Confidence == 0 {
			item.Confidence = 1
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO literary_work_items (work_id, content_id, format_type, link_source, confidence, confirmed_at)
			VALUES ($1,$2,$3,$4,$5, CASE WHEN $4 = 'manual' THEN NOW() ELSE NULL END)
			ON CONFLICT (content_id) DO UPDATE SET
				work_id = EXCLUDED.work_id,
				format_type = EXCLUDED.format_type,
				link_source = EXCLUDED.link_source,
				confidence = EXCLUDED.confidence,
				confirmed_at = EXCLUDED.confirmed_at,
				ignored_at = NULL,
				updated_at = NOW()
		`, workID, item.ContentID, item.FormatType, item.LinkSource, item.Confidence)
		if err != nil {
			return fmt.Errorf("linking %s to work %s: %w", item.ContentID, workID, err)
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) GetSummaryForContentID(ctx context.Context, contentID string) (*WorkSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT lw.work_id, lw.canonical_title, lwi.format_type, lwi.content_id,
		       COALESCE(MIN(mil.media_folder_id), 0)::int
		FROM literary_work_items anchor
		JOIN literary_works lw ON lw.work_id = anchor.work_id
		JOIN literary_work_items lwi ON lwi.work_id = lw.work_id
		LEFT JOIN media_item_libraries mil ON mil.content_id = lwi.content_id
		WHERE anchor.content_id = $1
		GROUP BY lw.work_id, lw.canonical_title, lwi.format_type, lwi.content_id
		ORDER BY lwi.format_type, lwi.content_id
	`, contentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summary *WorkSummary
	for rows.Next() {
		var format WorkFormatSummary
		var workID, title string
		if err := rows.Scan(&workID, &title, &format.Type, &format.ContentID, &format.LibraryID); err != nil {
			return nil, err
		}
		if summary == nil {
			summary = &WorkSummary{WorkID: workID, Title: title}
		}
		summary.Formats = append(summary.Formats, format)
	}
	return summary, rows.Err()
}

func scanWork(row pgx.Row) (*Work, error) {
	var w Work
	if err := row.Scan(
		&w.WorkID, &w.CanonicalTitle, &w.SortTitle, &w.NormalizedTitle,
		&w.PrimaryAuthorKey, &w.PrimaryCoverContentID, &w.Description,
		&w.PublishedDate, &w.Publisher, &w.Genres, &w.CreatedAt, &w.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &w, nil
}
```

- [ ] **Step 4: Run repository tests**

Run:

```bash
go test ./internal/literaryworks -run TestRepositoryLinkAndFetchSummary -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/literaryworks
git commit -m "feat(literary): persist work links"
```

## Task 5: Implement Matching Rules

**Files:**
- Create: `internal/literaryworks/matcher.go`
- Test: `internal/literaryworks/matcher_test.go`

- [ ] **Step 1: Write matcher tests**

Create tests for these concrete cases:

```go
func TestMatcherSameTitleAuthorLinks(t *testing.T) {
	ebook := MatchItem{ContentID: "e1", Type: FormatEbook, Title: "Project Hail Mary", Authors: []string{"Andy Weir"}}
	audio := MatchItem{ContentID: "a1", Type: FormatAudiobook, Title: "Project Hail Mary", Authors: []string{"Andy Weir"}, Narrators: []string{"Ray Porter"}}
	candidate := ScoreCandidate(ebook, audio)
	if candidate.Score < AutoLinkThreshold || candidate.LinkSource != LinkMetadataMatch {
		t.Fatalf("candidate = %#v, want metadata auto-link", candidate)
	}
}

func TestMatcherSameTitleDifferentAuthorDoesNotLink(t *testing.T) {
	ebook := MatchItem{ContentID: "e1", Type: FormatEbook, Title: "The Last Adventure", Authors: []string{"A. Lee"}}
	audio := MatchItem{ContentID: "a1", Type: FormatAudiobook, Title: "The Last Adventure", Authors: []string{"B. Lee"}}
	candidate := ScoreCandidate(ebook, audio)
	if candidate.Score >= AutoLinkThreshold {
		t.Fatalf("score = %v, want below threshold", candidate.Score)
	}
}

func TestMatcherSeriesIndexLinksSubtitleVariants(t *testing.T) {
	ebook := MatchItem{ContentID: "e1", Type: FormatEbook, Title: "The Last Adventure", Authors: []string{"A. Lee"}, SeriesName: "Constance Verity", SeriesIndex: floatPtr(1)}
	audio := MatchItem{ContentID: "a1", Type: FormatAudiobook, Title: "Constance Verity 1 - The Last Adventure of Constance Verity", Authors: []string{"A. Lee"}, SeriesName: "Constance Verity", SeriesIndex: floatPtr(1)}
	candidate := ScoreCandidate(ebook, audio)
	if candidate.Score < AutoLinkThreshold || candidate.LinkSource != LinkSeriesMatch {
		t.Fatalf("candidate = %#v, want series auto-link", candidate)
	}
}
```

- [ ] **Step 2: Run failing matcher tests**

Run:

```bash
go test ./internal/literaryworks -run 'TestMatcher' -count=1
```

Expected: FAIL because matcher types/functions are undefined.

- [ ] **Step 3: Implement matcher**

Create `internal/literaryworks/matcher.go`:

```go
package literaryworks

const AutoLinkThreshold = 0.86

type MatchItem struct {
	ContentID   string
	Type        string
	Title       string
	Authors     []string
	Narrators   []string
	SeriesName  string
	SeriesIndex *float64
	ExternalIDs map[string]string
	Publisher   string
	Year        int
}

func ScoreCandidate(source, target MatchItem) Candidate {
	if source.ContentID == "" || target.ContentID == "" || source.ContentID == target.ContentID {
		return Candidate{Score: 0, Evidence: map[string]string{"reason": "same_or_missing_content_id"}}
	}
	evidence := map[string]string{}
	sourceAuthor := personKey(source.Authors)
	targetAuthor := personKey(target.Authors)
	titleMatch := normalizeKey(source.Title) != "" && normalizeKey(source.Title) == normalizeKey(target.Title)
	authorMatch := sourceAuthor != "" && sourceAuthor == targetAuthor
	seriesMatch := normalizeKey(source.SeriesName) != "" &&
		normalizeKey(source.SeriesName) == normalizeKey(target.SeriesName) &&
		source.SeriesIndex != nil && target.SeriesIndex != nil &&
		*source.SeriesIndex == *target.SeriesIndex

	if provider, id, ok := sharedExternalID(source.ExternalIDs, target.ExternalIDs); ok {
		evidence["external_id"] = provider + ":" + id
		return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.98, LinkSource: LinkExternalID, Evidence: evidence}
	}
	if seriesMatch && authorMatch {
		evidence["series"] = source.SeriesName
		evidence["author"] = sourceAuthor
		return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.92, LinkSource: LinkSeriesMatch, Evidence: evidence}
	}
	if titleMatch && authorMatch {
		evidence["title"] = source.Title
		evidence["author"] = sourceAuthor
		return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.9, LinkSource: LinkMetadataMatch, Evidence: evidence}
	}
	if titleMatch && sourceAuthor != "" && targetAuthor != "" && sourceAuthor != targetAuthor {
		evidence["conflict"] = "author"
		return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.2, LinkSource: LinkMetadataMatch, Evidence: evidence}
	}
	return Candidate{SourceContentID: source.ContentID, TargetContentID: target.ContentID, Score: 0.4, LinkSource: LinkMetadataMatch, Evidence: evidence}
}

func sharedExternalID(a, b map[string]string) (string, string, bool) {
	for provider, aID := range a {
		if aID == "" || provider == "asin" {
			continue
		}
		if bID := b[provider]; bID != "" && bID == aID {
			return provider, aID, true
		}
	}
	return "", "", false
}
```

- [ ] **Step 4: Run matcher tests**

Run:

```bash
go test ./internal/literaryworks -run 'TestMatcher' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/literaryworks
git commit -m "feat(literary): score work matches"
```

## Task 6: Add Work Summary To Existing Item Detail

**Files:**
- Modify: `internal/catalog/detail.go`
- Test: `internal/catalog/detail_literary_work_test.go`

- [ ] **Step 1: Write item detail test**

Create `internal/catalog/detail_literary_work_test.go`:

```go
package catalog

import (
	"context"
	"testing"
)

type fakeWorkSummaryProvider struct{}

func (fakeWorkSummaryProvider) GetSummaryForContentID(ctx context.Context, contentID string) (*WorkSummary, error) {
	return &WorkSummary{
		WorkID: "work-1",
		Title:  "Project Hail Mary",
		Formats: []WorkFormatSummary{
			{Type: "ebook", ContentID: contentID, LibraryID: 1},
			{Type: "audiobook", ContentID: "audio-1", LibraryID: 2},
		},
	}, nil
}

func TestItemDetailIncludesWorkSummaryWhenProviderConfigured(t *testing.T) {
	detail := &ItemDetail{ContentID: "ebook-1", Type: "ebook", Title: "Project Hail Mary"}
	applyWorkSummary(context.Background(), detail, fakeWorkSummaryProvider{})
	if detail.WorkID != "work-1" || len(detail.WorkFormats) != 2 {
		t.Fatalf("detail work fields = %#v", detail)
	}
}
```

- [ ] **Step 2: Run failing test**

Run:

```bash
go test ./internal/catalog -run TestItemDetailIncludesWorkSummaryWhenProviderConfigured -count=1
```

Expected: FAIL because work fields/types/functions are undefined.

- [ ] **Step 3: Add catalog-facing types and helper**

In `internal/catalog/detail.go`, add fields to `ItemDetail`:

```go
WorkID      string              `json:"work_id,omitempty"`
WorkTitle   string              `json:"work_title,omitempty"`
WorkFormats []WorkFormatSummary `json:"work_formats,omitempty"`
```

Add catalog-local interface/types near the detail service interfaces:

```go
type WorkSummaryProvider interface {
	GetSummaryForContentID(ctx context.Context, contentID string) (*WorkSummary, error)
}

type WorkSummary struct {
	WorkID  string
	Title   string
	Formats []WorkFormatSummary
}

type WorkFormatSummary struct {
	Type      string `json:"type"`
	ContentID string `json:"content_id"`
	LibraryID int   `json:"library_id,omitempty"`
}

func applyWorkSummary(ctx context.Context, detail *ItemDetail, provider WorkSummaryProvider) {
	if detail == nil || provider == nil || detail.ContentID == "" {
		return
	}
	summary, err := provider.GetSummaryForContentID(ctx, detail.ContentID)
	if err != nil || summary == nil {
		return
	}
	detail.WorkID = summary.WorkID
	detail.WorkTitle = summary.Title
	detail.WorkFormats = summary.Formats
}
```

Add `workSummaryProvider WorkSummaryProvider` to `DetailService`, plus setter:

```go
func (s *DetailService) SetWorkSummaryProvider(provider WorkSummaryProvider) {
	if s != nil {
		s.workSummaryProvider = provider
	}
}
```

Call `applyWorkSummary(ctx, detail, s.workSummaryProvider)` before returning media item details.

- [ ] **Step 4: Run detail tests**

Run:

```bash
go test ./internal/catalog -run 'TestItemDetailIncludesWorkSummary|Audiobook|Ebook' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/catalog/detail.go internal/catalog/detail_literary_work_test.go
git commit -m "feat(catalog): include literary work summary on item detail"
```

## Task 7: Add Public Work Detail Endpoint

**Files:**
- Create: `internal/api/handlers/literary_works.go`
- Modify: `internal/api/router.go`
- Modify: `cmd/silo/main.go`
- Test: `internal/api/handlers/literary_works_test.go`

- [ ] **Step 1: Write handler test**

Create a handler test asserting `GET /api/v1/works/work-1` returns a work response with separate format entries:

```go
func TestLiteraryWorkHandlerGetWork(t *testing.T) {
	handler := &LiteraryWorkHandler{Service: fakeLiteraryWorkService{
		work: &literaryworks.DetailResponse{
			WorkID: "work-1", WorkTitle: "Project Hail Mary",
			Formats: []literaryworks.FormatResponse{
				{Type: "ebook", ContentID: "ebook-1"},
				{Type: "audiobook", ContentID: "audio-1"},
			},
		},
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/works/work-1", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("work_id", "work-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	handler.HandleGetWork(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"formats"`) || !strings.Contains(rec.Body.String(), `"audiobook"`) {
		t.Fatalf("body = %s, want work formats", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run failing handler test**

Run:

```bash
go test ./internal/api/handlers -run TestLiteraryWorkHandlerGetWork -count=1
```

Expected: FAIL because handler/service response types are undefined.

- [ ] **Step 3: Add response types and handler**

Extend `internal/literaryworks/types.go` with:

```go
type DetailResponse struct {
	WorkID          string           `json:"work_id"`
	WorkTitle       string           `json:"work_title"`
	Authors         []PersonResponse `json:"authors"`
	Formats         []FormatResponse `json:"formats"`
	PrimaryCoverURL string           `json:"primary_cover_url,omitempty"`
	Metadata        WorkMetadata     `json:"metadata"`
}

type PersonResponse struct {
	PersonID string `json:"person_id,omitempty"`
	Name     string `json:"name"`
}

type FormatResponse struct {
	Type           string             `json:"type"`
	ContentID      string             `json:"content_id"`
	LibraryID      int                `json:"library_id,omitempty"`
	AvailableFiles []FileResponse     `json:"available_files"`
	Progress       *ProgressResponse  `json:"progress,omitempty"`
}

type FileResponse struct {
	FileID          int     `json:"file_id"`
	OriginalName    string  `json:"original_filename"`
	Format          string  `json:"format"`
	MIMEType        string  `json:"mime_type,omitempty"`
	Size            int64   `json:"size,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
}

type ProgressResponse struct {
	Kind            string   `json:"kind"`
	Progress        *float64 `json:"progress,omitempty"`
	PositionSeconds *float64 `json:"position_seconds,omitempty"`
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

type WorkMetadata struct {
	Description   string       `json:"description,omitempty"`
	Series        *SeriesInfo  `json:"series,omitempty"`
	Genres        []string     `json:"genres"`
	PublishedDate string       `json:"published_date,omitempty"`
	Publisher     string       `json:"publisher,omitempty"`
}

type SeriesInfo struct {
	Name  string   `json:"name"`
	Index *float64 `json:"index,omitempty"`
}
```

Create `internal/api/handlers/literary_works.go`:

```go
package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/literaryworks"
)

type LiteraryWorkService interface {
	GetWork(ctx context.Context, workID string, filter catalog.AccessFilter) (*literaryworks.DetailResponse, error)
}

type LiteraryWorkHandler struct {
	Service LiteraryWorkService
}

func (h *LiteraryWorkHandler) HandleGetWork(w http.ResponseWriter, r *http.Request) {
	workID := strings.TrimSpace(chi.URLParam(r, "work_id"))
	if workID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "work_id is required")
		return
	}
	if h == nil || h.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Literary works are not configured")
		return
	}
	resp, err := h.Service.GetWork(r.Context(), workID, requestAccessFilter(r))
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Work not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load work")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
```

- [ ] **Step 4: Register routes and dependencies**

In `internal/api/router.go`, add authenticated route:

```go
r.Get("/works/{work_id}", literaryWorkHandler.HandleGetWork)
```

In `cmd/silo/main.go`, construct `literaryworks.Repository` and `literaryworks.Service`, set it on `DetailService`, and pass a `LiteraryWorkHandler` into router dependency construction following existing handler patterns.

- [ ] **Step 5: Run handler and build tests**

Run:

```bash
go test ./internal/api/handlers ./internal/literaryworks ./internal/catalog -count=1
go test ./cmd/silo -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run:

```bash
git add internal/api/handlers/literary_works.go internal/api/router.go cmd/silo/main.go internal/literaryworks internal/catalog/detail.go
git commit -m "feat(literary): expose work detail API"
```

## Task 8: Assemble Work Detail With Files And Progress

**Files:**
- Create/modify: `internal/literaryworks/service.go`
- Modify: `internal/literaryworks/repository.go`
- Test: `internal/literaryworks/service_test.go`

- [ ] **Step 1: Write service tests**

Write `TestServiceGetWorkReturnsSeparateFormatProgress` that seeds one ebook, one audiobook, `media_files`, ebook progress, and audiobook progress. Assert:

```go
if len(resp.Formats) != 2 {
	t.Fatalf("formats = %d, want 2", len(resp.Formats))
}
if ebook.Progress == nil || ebook.Progress.Kind != "reading" {
	t.Fatalf("ebook progress = %#v, want reading", ebook.Progress)
}
if audio.Progress == nil || audio.Progress.Kind != "listening" {
	t.Fatalf("audio progress = %#v, want listening", audio.Progress)
}
if ebook.AvailableFiles[0].Format != "epub" || audio.AvailableFiles[0].DurationSeconds == 0 {
	t.Fatalf("files = %#v %#v, want ebook format and audio duration", ebook.AvailableFiles, audio.AvailableFiles)
}
```

- [ ] **Step 2: Run failing test**

Run:

```bash
go test ./internal/literaryworks -run TestServiceGetWorkReturnsSeparateFormatProgress -count=1
```

Expected: FAIL because work detail assembly is not implemented.

- [ ] **Step 3: Implement service assembly**

Create `internal/literaryworks/service.go`:

```go
package literaryworks

import (
	"context"
	"mime"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) GetWork(ctx context.Context, workID string, filter catalog.AccessFilter) (*DetailResponse, error) {
	work, items, err := s.repo.GetWorkWithItems(ctx, workID, filter)
	if err != nil {
		return nil, err
	}
	resp := &DetailResponse{
		WorkID:    work.WorkID,
		WorkTitle: work.CanonicalTitle,
		Metadata: WorkMetadata{
			Description: work.Description,
			Genres:      work.Genres,
			Publisher:   work.Publisher,
		},
	}
	for _, item := range items {
		format := FormatResponse{
			Type:           item.FormatType,
			ContentID:      item.ContentID,
			LibraryID:      item.LibraryID,
			AvailableFiles: filesToResponse(item.Files),
			Progress:       item.Progress,
		}
		resp.Formats = append(resp.Formats, format)
	}
	return resp, nil
}

func filesToResponse(files []WorkFile) []FileResponse {
	out := make([]FileResponse, 0, len(files))
	for _, f := range files {
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(f.FilePath)), ".")
		mimeType := mime.TypeByExtension("." + ext)
		out = append(out, FileResponse{
			FileID:          f.FileID,
			OriginalName:    filepath.Base(f.FilePath),
			Format:          ext,
			MIMEType:        mimeType,
			Size:            f.Size,
			DurationSeconds: f.DurationSeconds,
		})
	}
	return out
}
```

Add repository methods/types for `GetWorkWithItems`, `WorkItemDetail`, and `WorkFile`:

```go
type WorkItemDetail struct {
	ContentID  string
	FormatType string
	LibraryID  int
	Files      []WorkFile
	Progress   *ProgressResponse
}

type WorkFile struct {
	FileID          int
	FilePath        string
	Size            int64
	DurationSeconds float64
}

func (r *Repository) GetWorkWithItems(ctx context.Context, workID string, filter catalog.AccessFilter) (*Work, []WorkItemDetail, error) {
	work, err := r.GetWork(ctx, workID)
	if err != nil {
		return nil, nil, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT lwi.content_id, lwi.format_type, COALESCE(MIN(mil.media_folder_id), 0)::int
		FROM literary_work_items lwi
		JOIN media_items mi ON mi.content_id = lwi.content_id
		LEFT JOIN media_item_libraries mil ON mil.content_id = lwi.content_id
		WHERE lwi.work_id = $1
		GROUP BY lwi.content_id, lwi.format_type
		ORDER BY lwi.format_type, lwi.content_id
	`, workID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var items []WorkItemDetail
	for rows.Next() {
		var item WorkItemDetail
		if err := rows.Scan(&item.ContentID, &item.FormatType, &item.LibraryID); err != nil {
			return nil, nil, err
		}
		item.Files, err = r.ListFiles(ctx, item.ContentID)
		if err != nil {
			return nil, nil, err
		}
		item.Progress, err = r.GetProgress(ctx, item.ContentID, item.FormatType, filter)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	return work, items, rows.Err()
}
```

Implement `ListFiles` with:

```sql
SELECT id, file_path, COALESCE(file_size, 0), COALESCE(duration, 0)::double precision / 1000
FROM media_files
WHERE content_id = $1 AND missing_since IS NULL
ORDER BY file_path ASC
```

Implement `GetProgress` so `format_type = 'ebook'` reads `ebook_reader_progress` by `filter.UserID` and `filter.ProfileID`; `format_type = 'audiobook'` reads the same progress source currently used by audiobook item user state. If audiobook progress lookup is not exposed as a repository yet, add a narrow interface to `Service` rather than querying a second progress implementation directly from handlers.

- [ ] **Step 4: Run service tests**

Run:

```bash
go test ./internal/literaryworks -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/literaryworks
git commit -m "feat(literary): assemble work detail"
```

## Task 9: Add Admin Match/Link Primitives

**Files:**
- Modify: `internal/api/handlers/literary_works.go`
- Modify: `internal/literaryworks/service.go`
- Test: `internal/api/handlers/literary_works_admin_test.go`

- [ ] **Step 1: Write admin handler tests**

Create `internal/api/handlers/literary_works_admin_test.go` with explicit request/response tests:

```go
func TestAdminLiteraryWorkLinkRequiresTwoItems(t *testing.T) {
	handler := &LiteraryWorkHandler{Service: &fakeAdminLiteraryWorkService{}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/literary-works/link", strings.NewReader(`{"content_ids":["ebook-1"]}`))
	rec := httptest.NewRecorder()
	handler.HandleAdminLink(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rec.Code, rec.Body.String())
	}
}

func TestAdminLiteraryWorkLinkCreatesManualWork(t *testing.T) {
	svc := &fakeAdminLiteraryWorkService{}
	handler := &LiteraryWorkHandler{Service: svc}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/literary-works/link", strings.NewReader(`{"content_ids":["ebook-1","audio-1"]}`))
	rec := httptest.NewRecorder()
	handler.HandleAdminLink(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if !reflect.DeepEqual(svc.linkedIDs, []string{"ebook-1", "audio-1"}) {
		t.Fatalf("linked ids = %#v", svc.linkedIDs)
	}
	if !strings.Contains(rec.Body.String(), `"work_id":"work-1"`) {
		t.Fatalf("body = %s, want work id", rec.Body.String())
	}
}

func TestAdminLiteraryWorkIgnoreStoresDecision(t *testing.T) {
	svc := &fakeAdminLiteraryWorkService{}
	handler := &LiteraryWorkHandler{Service: svc}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/literary-works/matches/ignore", strings.NewReader(`{"source_content_id":"ebook-1","target_content_id":"audio-1"}`))
	rec := httptest.NewRecorder()
	handler.HandleAdminIgnoreMatch(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s, want 204", rec.Code, rec.Body.String())
	}
	if svc.ignoredSource != "ebook-1" || svc.ignoredTarget != "audio-1" {
		t.Fatalf("ignored = %q/%q", svc.ignoredSource, svc.ignoredTarget)
	}
}
```

- [ ] **Step 2: Run failing admin tests**

Run:

```bash
go test ./internal/api/handlers -run 'TestAdminLiteraryWork' -count=1
```

Expected: FAIL because admin handlers are missing.

- [ ] **Step 3: Implement admin endpoints**

Add request structs:

```go
type literaryWorkLinkRequest struct {
	ContentIDs []string `json:"content_ids"`
}

type literaryWorkDecisionRequest struct {
	SourceContentID string `json:"source_content_id"`
	TargetContentID string `json:"target_content_id"`
}
```

Add handler methods with these signatures:

```go
func (h *LiteraryWorkHandler) HandleAdminLink(w http.ResponseWriter, r *http.Request)
func (h *LiteraryWorkHandler) HandleAdminUnlink(w http.ResponseWriter, r *http.Request)
func (h *LiteraryWorkHandler) HandleAdminMatches(w http.ResponseWriter, r *http.Request)
func (h *LiteraryWorkHandler) HandleAdminIgnoreMatch(w http.ResponseWriter, r *http.Request)
func (h *LiteraryWorkHandler) HandleAdminConfirmMatch(w http.ResponseWriter, r *http.Request)
```

Each method validates IDs before calling the service. `HandleAdminLink` rejects fewer than two IDs. `HandleAdminIgnoreMatch` and `HandleAdminConfirmMatch` reject blank IDs or identical IDs. Not-found service errors use `404`; other service errors use `500`; successful ignore/unlink returns `204`; successful link/confirm returns JSON containing `work_id`.

- [ ] **Step 4: Register admin routes**

In `internal/api/router.go`, inside admin route group:

```go
r.Get("/literary-works/items/{content_id}/matches", literaryWorkHandler.HandleAdminMatches)
r.Post("/literary-works/link", literaryWorkHandler.HandleAdminLink)
r.Post("/literary-works/{work_id}/unlink", literaryWorkHandler.HandleAdminUnlink)
r.Post("/literary-works/matches/ignore", literaryWorkHandler.HandleAdminIgnoreMatch)
r.Post("/literary-works/matches/confirm", literaryWorkHandler.HandleAdminConfirmMatch)
```

- [ ] **Step 5: Run admin tests**

Run:

```bash
go test ./internal/api/handlers ./internal/literaryworks -run 'LiteraryWork|Matcher|Repository' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run:

```bash
git add internal/api/handlers/literary_works.go internal/api/router.go internal/literaryworks
git commit -m "feat(literary): add admin work linking primitives"
```

## Task 10: Add Opt-In Work-Grouped Catalog Results

**Files:**
- Modify: `internal/catalog/catalog_request.go`
- Modify: `internal/catalog/catalog_parser.go`
- Modify: `internal/catalog/query_executor.go`
- Modify: `internal/api/handlers/catalog.go`
- Test: `internal/catalog/catalog_parser_test.go`
- Test: `internal/catalog/query_executor_literary_work_test.go`

- [ ] **Step 1: Write parser test**

Add:

```go
func TestParseCatalogRequestGroupWork(t *testing.T) {
	req, err := ParseCatalogRequest(url.Values{
		"type":  {"reading"},
		"group": {"work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Group != "work" {
		t.Fatalf("Group = %q, want work", req.Group)
	}
}
```

- [ ] **Step 2: Run failing parser test**

Run:

```bash
go test ./internal/catalog -run TestParseCatalogRequestGroupWork -count=1
```

Expected: FAIL because `CatalogRequest.Group` is missing.

- [ ] **Step 3: Add request parsing**

Add to `CatalogRequest`:

```go
Group string
```

In `ParseCatalogRequest`, accept only `group=work` or empty:

```go
if group := strings.ToLower(strings.TrimSpace(values.Get("group"))); group != "" {
	if group != "work" {
		return CatalogRequest{}, fmt.Errorf("unsupported catalog group %q", group)
	}
	req.Group = group
}
```

- [ ] **Step 4: Implement grouped query projection**

Add a grouped query path only when `req.Group == "work"` and media scope is reading/literary. Start with this CTE shape and adapt aliases to the existing `QueryExecutor` plan builder:

```sql
WITH scoped_items AS (
    SELECT mi.*
    FROM media_items mi
    WHERE mi.type IN ('ebook', 'audiobook')
),
grouped AS (
    SELECT
        COALESCE(lwi.work_id, si.content_id) AS group_id,
        ARRAY_AGG(DISTINCT si.type ORDER BY si.type) AS work_formats,
        (ARRAY_AGG(si.content_id ORDER BY
            CASE si.type WHEN 'ebook' THEN 0 WHEN 'audiobook' THEN 1 ELSE 2 END,
            si.content_id
        ))[1] AS representative_content_id
    FROM scoped_items si
    LEFT JOIN literary_work_items lwi ON lwi.content_id = si.content_id
    GROUP BY COALESCE(lwi.work_id, si.content_id)
)
SELECT mi.*, grouped.group_id, grouped.work_formats
FROM grouped
JOIN media_items mi ON mi.content_id = grouped.representative_content_id
```

The default item query SQL remains unchanged when `group` is empty. The grouped result should add response fields `work_id` and `work_formats` without removing `content_id`, so existing card rendering can still navigate to a representative item until the client adopts `/api/v1/works/{work_id}`.

- [ ] **Step 5: Run catalog tests**

Run:

```bash
go test ./internal/catalog ./internal/api/handlers -run 'Catalog|Work' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run:

```bash
git add internal/catalog internal/api/handlers
git commit -m "feat(catalog): support work-grouped literary browsing"
```

## Task 11: Wire Automatic Matching After Scan/Enrichment

**Files:**
- Modify: `internal/scanner/ebook_scan.go`
- Modify: `internal/scanner/audiobook_scan.go`
- Modify: `internal/ebooks/enrichment.go`
- Modify: `internal/audiobooks/enrichment.go`
- Test: scanner/enrichment tests with fake matcher.

- [ ] **Step 1: Add tiny matcher interface near scanners/enrichers**

Use an interface to avoid importing handler code:

```go
type LiteraryWorkMatcher interface {
	MatchContentID(ctx context.Context, contentID string) error
}
```

- [ ] **Step 2: Invoke matcher after successful item/file/people/series persistence**

For ebook and audiobook scan paths, call:

```go
if s.literaryMatcher != nil {
	if err := s.literaryMatcher.MatchContentID(ctx, contentID); err != nil {
		slog.Warn("literary work match failed", "content_id", contentID, "error", err)
	}
}
```

Do the same after successful metadata enrichment so improved external IDs and authors can link works.

- [ ] **Step 3: Test non-fatal matcher failure**

Add tests that make the fake matcher return an error and assert the scan/enrichment still succeeds.

- [ ] **Step 4: Run scanner/enrichment tests**

Run:

```bash
go test ./internal/scanner ./internal/ebooks ./internal/audiobooks -run 'Ebook|Audiobook|Literary' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```bash
git add internal/scanner internal/ebooks internal/audiobooks
git commit -m "feat(literary): match works after literary metadata updates"
```

## Task 12: Final Verification

**Files:**
- No planned source edits unless verification exposes defects.

- [ ] **Step 1: Run focused backend tests**

Run:

```bash
go test ./internal/literaryworks ./internal/catalog ./internal/api/handlers ./internal/scanner ./internal/ebooks ./internal/audiobooks ./migrations -count=1
```

Expected: PASS.

- [ ] **Step 2: Run broader server tests**

Run:

```bash
go test ./internal/... ./cmd/silo ./migrations -count=1
```

Expected: PASS.

- [ ] **Step 3: Run frontend tests if work-grouped catalog UI is touched**

Run:

```bash
cd web
pnpm test --run
```

Expected: PASS. If `node_modules` is unavailable, run `pnpm install --frozen-lockfile` first or document the exact failure.

- [ ] **Step 4: Build the Docker image**

Run:

```bash
docker buildx build --load -t silo-server:literary-works .
```

Expected: image builds successfully, including frontend build.

- [ ] **Step 5: Prepare PR**

Run:

```bash
git status --short
git log --oneline origin/work/ebook-reader-ruler-profiles..HEAD
```

Expected: clean or intentionally documented working tree, with commits from this plan only.
