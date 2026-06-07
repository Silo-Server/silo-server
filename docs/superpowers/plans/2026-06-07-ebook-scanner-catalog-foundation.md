# Ebook Scanner Catalog Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add first-party ebook library scanning and catalog visibility for local EPUB/PDF/MOBI/AZW/AZW3/FB2 files.

**Architecture:** Ebooks become normal `media_items` with `type = 'ebook'`, one `media_files` row per ebook file, author credits in `item_people`, ISBN/ASIN in `media_item_provider_ids`, and ebook-only fields in a new `ebook_details` table. The scanner mirrors audiobook folder handling where useful, but identity is file-scoped and never title/year-scoped.

**Tech Stack:** Go, pgx/PostgreSQL migrations, existing Silo scanner/catalog repositories, React/TypeScript API type updates.

---

## File Structure

- Create `migrations/181_ebook_details.up.sql` and `migrations/181_ebook_details.down.sql`
  - Defines the `ebook_details` table and useful indexes.
- Create `internal/models/ebook.go`
  - `EbookDetails` model used by scanner/catalog tests and repository code.
- Create `internal/catalog/ebook_details_repo.go`
  - Small pgx repository for upserting/loading `ebook_details`.
- Create `internal/scanner/ebook.go`
  - Supported extension checks, parsed ebook structs, metadata normalization helpers, and parser dispatcher.
- Create `internal/scanner/ebook_scan.go`
  - Library walk, unchanged skip, item/file/details/people/provider-id/cover upserts, missing-file marking, and aggregate scan errors.
- Create `internal/scanner/ebook_test.go`
  - Parser helper tests and scanner behavior tests with fake repositories where practical.
- Modify `internal/scanner/scanner.go`
  - Route `ebook`/`ebooks` libraries to `ScanEbookFolder`.
- Modify `internal/scanner/file_repo.go`
  - Add exact-path and folder/type lookup helpers for ebook unchanged-skip and missing-file reconciliation.
- Modify `internal/catalog/catalog_parser.go`, `internal/catalog/catalog_resolver.go`, `internal/catalog/browse.go`, `internal/catalog/query_builder.go`, and `internal/catalog/query_definition.go`
  - Accept `ebook` as a media scope/type and expose ebook author/series facets.
- Modify `web/src/api/types.ts`, `web/src/lib/querySortOptions.ts`, and affected catalog UI tests
  - Add `ebook` to API unions and keep sorting/filtering valid.

## Task 1: Migration And Details Repository

**Files:**
- Create: `migrations/181_ebook_details.up.sql`
- Create: `migrations/181_ebook_details.down.sql`
- Create: `internal/models/ebook.go`
- Create: `internal/catalog/ebook_details_repo.go`
- Test: `migrations/ebook_details_test.go`
- Test: `internal/catalog/ebook_details_repo_test.go`

- [ ] **Step 1: Write migration test**

Create `migrations/ebook_details_test.go`:

```go
package migrations

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEbookDetailsMigrationFilesExist(t *testing.T) {
	for _, suffix := range []string{"up.sql", "down.sql"} {
		path := filepath.Join("..", "migrations", "181_ebook_details."+suffix)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected migration file %s: %v", path, err)
		}
	}
}
```

Run: `go test ./migrations -run TestEbookDetailsMigrationFilesExist -count=1`
Expected: FAIL until the migration files exist.

- [ ] **Step 2: Add migration files**

Create `migrations/181_ebook_details.up.sql`:

```sql
CREATE TABLE IF NOT EXISTS public.ebook_details (
    content_id text PRIMARY KEY REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    format text NOT NULL DEFAULT '',
    isbn text NOT NULL DEFAULT '',
    asin text NOT NULL DEFAULT '',
    publisher text NOT NULL DEFAULT '',
    page_count integer NOT NULL DEFAULT 0,
    series_name text NOT NULL DEFAULT '',
    series_index text NOT NULL DEFAULT '',
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ebook_details_series_name
    ON public.ebook_details (lower(series_name))
    WHERE series_name <> '';

CREATE INDEX IF NOT EXISTS idx_ebook_details_format
    ON public.ebook_details (format)
    WHERE format <> '';
```

Create `migrations/181_ebook_details.down.sql`:

```sql
DROP INDEX IF EXISTS public.idx_ebook_details_format;
DROP INDEX IF EXISTS public.idx_ebook_details_series_name;
DROP TABLE IF EXISTS public.ebook_details;
```

Run: `go test ./migrations -run TestEbookDetailsMigrationFilesExist -count=1`
Expected: PASS.

- [ ] **Step 3: Add model and repository test**

Create `internal/models/ebook.go`:

```go
package models

import "time"

type EbookDetails struct {
	ContentID    string
	Format       string
	ISBN         string
	ASIN         string
	Publisher    string
	PageCount    int
	SeriesName   string
	SeriesIndex  string
	MetadataJSON []byte
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
```

Create `internal/catalog/ebook_details_repo_test.go`:

```go
package catalog

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestEbookDetailsRepositoryUpsertRequiresContentID(t *testing.T) {
	repo := &EbookDetailsRepository{}
	err := repo.Upsert(context.Background(), models.EbookDetails{})
	if err == nil {
		t.Fatal("expected error for empty content id")
	}
}
```

Run: `go test ./internal/catalog -run TestEbookDetailsRepositoryUpsertRequiresContentID -count=1`
Expected: FAIL with missing `EbookDetailsRepository`.

- [ ] **Step 4: Implement repository**

Create `internal/catalog/ebook_details_repo.go`:

```go
package catalog

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

type EbookDetailsRepository struct {
	pool *pgxpool.Pool
}

func NewEbookDetailsRepository(pool *pgxpool.Pool) *EbookDetailsRepository {
	return &EbookDetailsRepository{pool: pool}
}

func (r *EbookDetailsRepository) Upsert(ctx context.Context, details models.EbookDetails) error {
	if details.ContentID == "" {
		return fmt.Errorf("ebook details content_id is required")
	}
	if r == nil || r.pool == nil {
		return fmt.Errorf("ebook details repository not configured")
	}
	if len(details.MetadataJSON) == 0 {
		details.MetadataJSON = []byte("{}")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO ebook_details
		    (content_id, format, isbn, asin, publisher, page_count, series_name, series_index, metadata_json)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (content_id) DO UPDATE SET
		    format = EXCLUDED.format,
		    isbn = EXCLUDED.isbn,
		    asin = EXCLUDED.asin,
		    publisher = EXCLUDED.publisher,
		    page_count = EXCLUDED.page_count,
		    series_name = EXCLUDED.series_name,
		    series_index = EXCLUDED.series_index,
		    metadata_json = EXCLUDED.metadata_json,
		    updated_at = now()
	`, details.ContentID, details.Format, details.ISBN, details.ASIN, details.Publisher,
		details.PageCount, details.SeriesName, details.SeriesIndex, details.MetadataJSON)
	if err != nil {
		return fmt.Errorf("upsert ebook details: %w", err)
	}
	return nil
}
```

Run: `go test ./internal/catalog -run TestEbookDetailsRepositoryUpsertRequiresContentID -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add migrations/181_ebook_details.* internal/models/ebook.go internal/catalog/ebook_details_repo.go internal/catalog/ebook_details_repo_test.go migrations/ebook_details_test.go
git commit -m "feat(ebooks): add ebook details schema"
```

## Task 2: Ebook Parser Foundation

**Files:**
- Create: `internal/scanner/ebook.go`
- Create/modify: `internal/scanner/ebook_test.go`

- [ ] **Step 1: Write extension and sanitization tests**

Create `internal/scanner/ebook_test.go`:

```go
package scanner

import "testing"

func TestSupportsEbookFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"book.epub", true},
		{"book.pdf", true},
		{"book.mobi", true},
		{"book.azw", true},
		{"book.azw3", true},
		{"book.fb2", true},
		{"book.txt", false},
		{"book.mp3", false},
	}
	for _, tt := range tests {
		if got := SupportsEbookFile(tt.path); got != tt.want {
			t.Fatalf("SupportsEbookFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestParsedEbookSanitizeBoundsFields(t *testing.T) {
	book := parsedEbook{
		Format:    "EPUB",
		Title:     "  Title  ",
		Authors:   []string{" Alice ", "", "Alice", "Bob"},
		Genres:    []string{" Fiction ", "fiction", ""},
		PageCount: -5,
	}
	book.sanitize()
	if book.Format != "epub" {
		t.Fatalf("format = %q, want epub", book.Format)
	}
	if book.Title != "Title" {
		t.Fatalf("title = %q, want Title", book.Title)
	}
	if len(book.Authors) != 2 || book.Authors[0] != "Alice" || book.Authors[1] != "Bob" {
		t.Fatalf("authors = %#v, want Alice/Bob", book.Authors)
	}
	if len(book.Genres) != 1 || book.Genres[0] != "Fiction" {
		t.Fatalf("genres = %#v, want Fiction", book.Genres)
	}
	if book.PageCount != 0 {
		t.Fatalf("page count = %d, want 0", book.PageCount)
	}
}
```

Run: `go test ./internal/scanner -run 'TestSupportsEbookFile|TestParsedEbookSanitizeBoundsFields' -count=1`
Expected: FAIL with missing symbols.

- [ ] **Step 2: Implement parser types and helpers**

Create `internal/scanner/ebook.go`:

```go
package scanner

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

var ebookExtensions = map[string]bool{
	".epub": true,
	".pdf":  true,
	".mobi": true,
	".azw":  true,
	".azw3": true,
	".fb2":  true,
}

type parsedEbook struct {
	Format      string
	Title       string
	Authors     []string
	Description string
	Publisher   string
	PublishedAt time.Time
	Year        int
	Language    string
	ISBN        string
	ASIN        string
	Series      string
	SeriesIndex string
	Genres      []string
	PageCount   int
	Cover       *parsedEbookCover
}

type parsedEbookCover struct {
	ContentType string
	Bytes       []byte
}

func SupportsEbookFile(filePath string) bool {
	return ebookExtensions[strings.ToLower(filepath.Ext(filePath))]
}

func (b *parsedEbook) sanitize() {
	b.Format = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(b.Format)), ".")
	b.Title = strings.TrimSpace(b.Title)
	b.Description = strings.TrimSpace(b.Description)
	b.Publisher = strings.TrimSpace(b.Publisher)
	b.Language = strings.TrimSpace(b.Language)
	b.ISBN = normalizeEbookExternalID(b.ISBN)
	b.ASIN = strings.TrimSpace(b.ASIN)
	b.Series = strings.TrimSpace(b.Series)
	b.SeriesIndex = strings.TrimSpace(b.SeriesIndex)
	b.Authors = uniqueTrimmedStrings(b.Authors)
	b.Genres = uniqueTrimmedStrings(b.Genres)
	if b.PageCount < 0 {
		b.PageCount = 0
	}
	if b.Year == 0 && !b.PublishedAt.IsZero() {
		b.Year = b.PublishedAt.Year()
	}
}

func uniqueTrimmedStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		key := strings.ToLower(trimmed)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func normalizeEbookExternalID(value string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "-", ""), " ", ""))
}

func parseEbookFile(path string) (book parsedEbook, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic parsing ebook %s: %v", path, r)
		}
	}()
	book = parsedEbook{Format: strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")}
	book.sanitize()
	return book, nil
}
```

Run: `go test ./internal/scanner -run 'TestSupportsEbookFile|TestParsedEbookSanitizeBoundsFields' -count=1`
Expected: PASS.

- [ ] **Step 3: Add EPUB and FB2 parser tests**

Add tests that build tiny valid EPUB/FB2 files in a temp directory so this repo does not depend on plugin fixtures:

```go
func TestParseEbookEPUBReadsOPFMetadata(t *testing.T) {
	path := writeTinyEPUB(t, map[string]string{
		"dc:title":       "EPUB Title",
		"dc:creator":     "Author A",
		"dc:language":    "en",
		"dc:identifier":  "978-1-4028-9462-6",
		"dc:publisher":   "Publisher P",
		"dc:description": "Description D",
		"dc:date":        "2020-01-02",
	})
	book, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	if book.Title != "EPUB Title" || len(book.Authors) != 1 || book.Authors[0] != "Author A" {
		t.Fatalf("book metadata = %+v", book)
	}
	if book.ISBN != "9781402894626" || book.Year != 2020 || book.Language != "en" {
		t.Fatalf("book identifiers/year/language = %+v", book)
	}
}

func TestParseEbookFB2ReadsDescriptionMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.fb2")
	xml := `<?xml version="1.0" encoding="utf-8"?>
<FictionBook><description><title-info>
<genre>fiction</genre><author><first-name>Alice</first-name><last-name>Author</last-name></author>
<book-title>FB2 Title</book-title><lang>en</lang><sequence name="Series S" number="2"/>
</title-info><publish-info><isbn>9781402894626</isbn><publisher>Publisher P</publisher><year>2021</year></publish-info></description></FictionBook>`
	if err := os.WriteFile(path, []byte(xml), 0o644); err != nil {
		t.Fatalf("write fb2: %v", err)
	}
	book, err := parseEbookFile(path)
	if err != nil {
		t.Fatalf("parseEbookFile: %v", err)
	}
	if book.Title != "FB2 Title" || book.Series != "Series S" || book.SeriesIndex != "2" {
		t.Fatalf("book metadata = %+v", book)
	}
}
```

Run: `go test ./internal/scanner -run TestParseEbook -count=1`
Expected: FAIL until `parseEbookFile` calls format-specific parsers.

- [ ] **Step 4: Implement minimal format parsers**

Implement helpers in `internal/scanner/ebook.go`. EPUB and FB2 must satisfy the tests in Step 3. PDF/MOBI/AZW/AZW3 may initially return format-only metadata with no hard failure so supported files can be cataloged while deeper metadata extraction is added incrementally.

```go
func parseEbookFile(path string) (book parsedEbook, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic parsing ebook %s: %v", path, r)
		}
	}()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".epub":
		book, err = parseEbookEPUB(path)
	case ".pdf":
		book, err = parseEbookPDF(path)
	case ".mobi", ".azw", ".azw3":
		book, err = parseEbookMOBI(path)
	case ".fb2":
		book, err = parseEbookFB2(path)
	default:
		err = fmt.Errorf("unsupported ebook format: %s", filepath.Ext(path))
	}
	book.sanitize()
	return book, err
}
```

For EPUB, read `META-INF/container.xml`, locate the OPF package file, parse Dublin Core metadata, and map:

```go
title -> Title
creator -> Authors
language -> Language
identifier containing ISBN digits -> ISBN
publisher -> Publisher
description -> Description
date -> PublishedAt/Year
meta name="calibre:series" -> Series
meta name="calibre:series_index" -> SeriesIndex
```

For FB2, parse `<description><title-info>` and `<publish-info>` and map the fields shown in the Step 3 test.

Run: `go test ./internal/scanner -run TestParseEbook -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scanner/ebook.go internal/scanner/ebook_test.go
git commit -m "feat(ebooks): add local ebook parser foundation"
```

## Task 3: Scanner Routing And Walk Skeleton

**Files:**
- Modify: `internal/scanner/scanner.go`
- Create/modify: `internal/scanner/ebook_scan.go`
- Test: `internal/scanner/ebook_test.go`

- [ ] **Step 1: Write routing test**

Add to `internal/scanner/scanner_test.go`:

```go
func TestWalkModeForEbookLibraries(t *testing.T) {
	for _, typ := range []string{"ebook", "ebooks"} {
		if got := walkModeFor(typ); got != walkModeEbook {
			t.Fatalf("walkModeFor(%q) = %v, want walkModeEbook", typ, got)
		}
	}
}
```

Run: `go test ./internal/scanner -run TestWalkModeForEbookLibraries -count=1`
Expected: FAIL with missing `walkModeEbook`.

- [ ] **Step 2: Add scan route**

Modify `internal/scanner/scanner.go`:

```go
const (
	walkModeVideo walkMode = iota
	walkModeMovie
	walkModeAudiobook
	walkModePodcast
	walkModeEbook
)

func walkModeFor(folderType string) walkMode {
	switch strings.ToLower(strings.TrimSpace(folderType)) {
	case "audiobook", "audiobooks":
		return walkModeAudiobook
	case "podcast", "podcasts":
		return walkModePodcast
	case "ebook", "ebooks":
		return walkModeEbook
	default:
		return walkModeVideo
	}
}
```

In `ScanFolder`, route before the video walk:

```go
if walkModeFor(folder.Type) == walkModeEbook {
	if err := s.ScanEbookFolder(watchCtx, folder); err != nil {
		return nil, err
	}
	return &ScanResult{}, nil
}
```

Run: `go test ./internal/scanner -run TestWalkModeForEbookLibraries -count=1`
Expected: FAIL until `ScanEbookFolder` exists.

- [ ] **Step 3: Add scanner skeleton**

Create `internal/scanner/ebook_scan.go`:

```go
package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/Silo-Server/silo-server/internal/models"
)

func (s *Scanner) ScanEbookFolder(ctx context.Context, folder *models.MediaFolder) error {
	if s == nil || folder == nil {
		return fmt.Errorf("ScanEbookFolder: nil scanner or folder")
	}
	var candidates []string
	var walkHadErrors atomic.Bool
	for _, root := range folder.Paths {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				walkHadErrors.Store(true)
				slog.Warn("ebook scan: walk error", "path", path, "error", walkErr)
				return nil
			}
			if d.IsDir() || !SupportsEbookFile(path) {
				return nil
			}
			candidates = append(candidates, path)
			return nil
		})
		if err != nil {
			walkHadErrors.Store(true)
			slog.Warn("ebook scan: walk root failed", "root", root, "error", err)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	var failed int64
	var failures []error
	var mu sync.Mutex
	for _, path := range candidates {
		if err := s.reconcileEbookFile(ctx, folder, path); err != nil {
			atomic.AddInt64(&failed, 1)
			mu.Lock()
			failures = append(failures, fmt.Errorf("%s: %w", path, err))
			mu.Unlock()
		}
	}
	if failed == int64(len(candidates)) {
		return fmt.Errorf("ebook scan failed for every candidate folder_id=%d candidates=%d: %w", folder.ID, len(candidates), errors.Join(failures...))
	}
	if walkHadErrors.Load() {
		slog.Warn("ebook scan: skipped missing-file cleanup because walk had errors", "folder_id", folder.ID)
	}
	return nil
}

func (s *Scanner) reconcileEbookFile(ctx context.Context, folder *models.MediaFolder, filePath string) error {
	return fmt.Errorf("not implemented")
}
```

Add `errors` to imports.

Run: `go test ./internal/scanner -run TestWalkModeForEbookLibraries -count=1`
Expected: PASS after compile issues are fixed.

- [ ] **Step 4: Add all-failed scan test**

Add to `internal/scanner/ebook_test.go`:

```go
func TestScanEbookFolderReturnsErrorWhenEveryReconcileFails(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bad.epub")
	if err := os.WriteFile(path, []byte("not an epub"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	s := &Scanner{}
	err := s.ScanEbookFolder(context.Background(), &models.MediaFolder{ID: 99, Paths: []string{root}})
	if err == nil {
		t.Fatal("ScanEbookFolder returned nil, want aggregate failure")
	}
	if !strings.Contains(err.Error(), "folder_id=99") {
		t.Fatalf("error = %q, want folder id", err)
	}
}
```

Run: `go test ./internal/scanner -run TestScanEbookFolderReturnsErrorWhenEveryReconcileFails -count=1`
Expected: PASS with the skeleton because `reconcileEbookFile` fails every candidate.

- [ ] **Step 5: Commit**

```bash
git add internal/scanner/scanner.go internal/scanner/scanner_test.go internal/scanner/ebook_scan.go internal/scanner/ebook_test.go
git commit -m "feat(ebooks): route ebook libraries to scanner"
```

## Task 4: Ebook Item/File Upsert

**Files:**
- Modify: `internal/scanner/ebook_scan.go`
- Modify: `internal/scanner/ebook_test.go`

- [ ] **Step 1: Write resolver tests**

Add fake root finder/item writer tests mirroring audiobook root identity:

```go
func TestResolveEbookMediaItemReusesRootScopedContentID(t *testing.T) {
	finder := &fakeRootContentFinder{contentID: "ebook-root-id"}
	writer := &fakeFilesystemItemWriter{}
	got, err := resolveEbookMediaItem(context.Background(), finder, writer, 7, "/books/A/Book.epub", &parsedEbook{Title: "Book"})
	if err != nil {
		t.Fatalf("resolveEbookMediaItem: %v", err)
	}
	if got != "ebook-root-id" {
		t.Fatalf("contentID = %q, want ebook-root-id", got)
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("unexpected upserts: %d", len(writer.upserts))
	}
}

func TestResolveEbookMediaItemCreatesNewWhenRootHasNoClaim(t *testing.T) {
	finder := &fakeRootContentFinder{}
	writer := &fakeFilesystemItemWriter{}
	got, err := resolveEbookMediaItem(context.Background(), finder, writer, 7, "/books/B/Book.epub", &parsedEbook{Title: "Book", Year: 2020})
	if err != nil {
		t.Fatalf("resolveEbookMediaItem: %v", err)
	}
	if got == "" || len(writer.upserts) != 1 || writer.upserts[0].Type != "ebook" {
		t.Fatalf("contentID=%q upserts=%#v", got, writer.upserts)
	}
}
```

Run: `go test ./internal/scanner -run TestResolveEbookMediaItem -count=1`
Expected: FAIL with missing resolver.

- [ ] **Step 2: Implement media item resolver**

Add to `internal/scanner/ebook_scan.go`:

```go
func resolveEbookMediaItem(ctx context.Context, rootFinder filesystemRootContentFinder, itemWriter filesystemMediaItemWriter, folderID int, filePath string, book *parsedEbook) (string, error) {
	if rootFinder == nil {
		return "", fmt.Errorf("root content finder not configured")
	}
	if itemWriter == nil {
		return "", fmt.Errorf("media item writer not configured")
	}
	existingID, err := rootFinder.FindContentIDByRootPath(ctx, folderID, filePath, "ebook")
	if err != nil {
		return "", fmt.Errorf("find ebook by root path: %w", err)
	}
	if existingID != "" {
		return existingID, nil
	}
	return createEbookMediaItem(ctx, itemWriter, book)
}

func createEbookMediaItem(ctx context.Context, itemWriter filesystemMediaItemWriter, book *parsedEbook) (string, error) {
	id, err := idgen.NextID()
	if err != nil {
		return "", fmt.Errorf("generate content_id: %w", err)
	}
	title := book.Title
	if title == "" {
		title = "Unknown Ebook"
	}
	releaseDate := ""
	if !book.PublishedAt.IsZero() {
		releaseDate = book.PublishedAt.Format("2006-01-02")
	}
	item := &models.MediaItem{
		ContentID:        id,
		Type:             "ebook",
		Title:            title,
		SortTitle:        titleutil.DeriveDefaultSortTitle(title),
		Year:             book.Year,
		Overview:         book.Description,
		Genres:           book.Genres,
		Studios:          []string{},
		OriginalLanguage: book.Language,
	}
	if book.Publisher != "" {
		item.Studios = []string{book.Publisher}
	}
	if releaseDate != "" {
		item.ReleaseDate = &releaseDate
	}
	if err := itemWriter.Upsert(ctx, item); err != nil {
		return "", err
	}
	return id, nil
}
```

Add imports: `idgen`, `titleutil`.

Run: `go test ./internal/scanner -run TestResolveEbookMediaItem -count=1`
Expected: PASS.

- [ ] **Step 3: Implement file/details/provider/people upsert helpers**

Add helper signatures to `internal/scanner/ebook_scan.go`:

```go
func (s *Scanner) upsertEbookMediaFile(ctx context.Context, folder *models.MediaFolder, contentID, filePath string, info fs.FileInfo, book *parsedEbook) error
func (s *Scanner) upsertEbookDetails(ctx context.Context, contentID string, book *parsedEbook) error
func (s *Scanner) upsertEbookPeople(ctx context.Context, contentID string, book *parsedEbook) error
func (s *Scanner) upsertEbookProviderIDs(ctx context.Context, contentID string, book *parsedEbook) error
```

Use `models.MediaFile`:

```go
mf := models.MediaFile{
	ContentID:          contentID,
	MediaFolderID:      folder.ID,
	CanonicalRootPath:  filePath,
	ObservedRootPath:   filePath,
	ContentGroupKey:    contentID,
	GroupKeyVersion:    1,
	BaseTitle:          book.Title,
	BaseYear:           book.Year,
	BaseType:           "ebook",
	IdentityConfidence: ebookIdentityConfidence(book),
	FilePath:           filePath,
	FileSize:           info.Size(),
	FileModifiedAt:     ptrTime(normalizeFileModifiedAt(info.ModTime())),
	Container:          book.Format,
	ProbeSource:        "local",
}
```

For details, initialize `Scanner` with `ebookDetailsRepo *catalog.EbookDetailsRepository` in `scanner.go`, then upsert `models.EbookDetails`.

Run: `go test ./internal/scanner -run TestResolveEbookMediaItem -count=1`
Expected: PASS after compile fixes.

- [ ] **Step 4: Wire reconcile**

Replace `reconcileEbookFile` body:

```go
func (s *Scanner) reconcileEbookFile(ctx context.Context, folder *models.MediaFolder, filePath string) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat ebook file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	book, err := parseEbookFile(filePath)
	if err != nil {
		return fmt.Errorf("parse ebook file: %w", err)
	}
	contentID, err := resolveEbookMediaItem(ctx, s.fileRepo, s.itemRepo, folder.ID, filePath, &book)
	if err != nil {
		return fmt.Errorf("upsert ebook item: %w", err)
	}
	if err := s.upsertEbookMediaFile(ctx, folder, contentID, filePath, info, &book); err != nil {
		return fmt.Errorf("upsert ebook file: %w", err)
	}
	if err := s.upsertEbookDetails(ctx, contentID, &book); err != nil {
		return fmt.Errorf("upsert ebook details: %w", err)
	}
	if err := s.upsertEbookPeople(ctx, contentID, &book); err != nil {
		return fmt.Errorf("upsert ebook people: %w", err)
	}
	if err := s.upsertEbookProviderIDs(ctx, contentID, &book); err != nil {
		return fmt.Errorf("upsert ebook provider ids: %w", err)
	}
	return s.libraryRepo.Upsert(ctx, contentID, folder.ID, time.Now())
}
```

Run: `go test ./internal/scanner -run 'TestResolveEbookMediaItem|TestScanEbookFolderReturnsErrorWhenEveryReconcileFails' -count=1`
Expected: all-failed test still PASS for missing repos or corrupt parse; resolver tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scanner/ebook_scan.go internal/scanner/ebook_test.go internal/scanner/scanner.go
git commit -m "feat(ebooks): upsert ebook catalog rows"
```

## Task 5: Unchanged Skip, Missing Files, Covers

**Files:**
- Modify: `internal/scanner/ebook_scan.go`
- Modify: `internal/scanner/ebook_test.go`

- [ ] **Step 1: Write unchanged helper tests**

Add:

```go
func TestEbookFileUnchanged(t *testing.T) {
	now := time.Now().UTC()
	mf := &models.MediaFile{FilePath: "/books/a.epub", FileSize: 10, FileModifiedAt: &now}
	if !ebookFileUnchanged(mf, 10, now) {
		t.Fatal("expected unchanged")
	}
	if ebookFileUnchanged(mf, 11, now) {
		t.Fatal("expected changed when size differs")
	}
}
```

Run: `go test ./internal/scanner -run TestEbookFileUnchanged -count=1`
Expected: FAIL with missing helper.

- [ ] **Step 2: Implement unchanged helper and skip path**

Add:

```go
func ebookFileUnchanged(existing *models.MediaFile, size int64, modTime time.Time) bool {
	if existing == nil || existing.FileModifiedAt == nil {
		return false
	}
	return existing.FileSize == size && sameFileModifiedAt(existing.FileModifiedAt, normalizeFileModifiedAt(modTime))
}
```

Before parsing in `reconcileEbookFile`, load existing active files by exact path. Add this helper to `internal/scanner/file_repo.go`:

```go
func (r *FileRepository) FindActiveByPath(ctx context.Context, folderID int, filePath string) (*models.MediaFile, error)
```

It should query `media_files` by `media_folder_id`, `file_path`, and `missing_since IS NULL`, returning nil on `pgx.ErrNoRows`.

Run: `go test ./internal/scanner -run TestEbookFileUnchanged -count=1`
Expected: PASS.

- [ ] **Step 3: Implement missing-file marking**

At the start of `ScanEbookFolder`, list existing active ebook files for each folder path using a repository helper:

```go
func (r *FileRepository) ListActiveByFolderAndType(ctx context.Context, folderID int, baseType string) ([]*models.MediaFile, error)
```

After the walk, if no walk errors occurred, mark unseen existing ebook file rows missing:

```go
now := time.Now()
for _, existing := range existingFiles {
	if _, ok := seenPaths[existing.FilePath]; ok {
		continue
	}
	if existing.MissingSince == nil {
		if err := s.fileRepo.MarkMissing(ctx, existing.ID, now); err != nil {
			slog.Warn("ebook scan: mark missing failed", "file_id", existing.ID, "error", err)
		}
	}
}
```

Run: `go test ./internal/scanner -run TestScanEbookFolder -count=1`
Expected: PASS after compile fixes.

- [ ] **Step 4: Write and implement cover preference test**

Add a helper-level test:

```go
func TestSelectEbookCoverPrefersEmbeddedOverSidecar(t *testing.T) {
	book := &parsedEbook{Cover: &parsedEbookCover{ContentType: "image/jpeg", Bytes: []byte("embedded")}}
	got, contentType, source := selectEbookCover(book, t.TempDir())
	if string(got) != "embedded" || contentType != "image/jpeg" || source != "embedded" {
		t.Fatalf("cover = %q %q %q", string(got), contentType, source)
	}
}
```

Implement:

```go
func selectEbookCover(book *parsedEbook, dir string) ([]byte, string, string) {
	if book != nil && book.Cover != nil && len(book.Cover.Bytes) > 0 {
		return book.Cover.Bytes, book.Cover.ContentType, "embedded"
	}
	if bytes, contentType, ok := findEbookSidecarCover(dir); ok {
		return bytes, contentType, "sidecar"
	}
	return nil, "", ""
}
```

Use `imageCacher` to cache selected cover as poster art using the audiobook cover pattern in `internal/scanner/audiobook_cover.go`.

Run: `go test ./internal/scanner -run 'TestEbookFileUnchanged|TestSelectEbookCover' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scanner/ebook_scan.go internal/scanner/ebook_test.go internal/scanner/file_repo.go
git commit -m "feat(ebooks): skip unchanged files and cache covers"
```

## Task 6: Catalog Scope And Facets

**Files:**
- Modify: `internal/catalog/catalog_parser.go`
- Modify: `internal/catalog/catalog_resolver.go`
- Modify: `internal/catalog/browse.go`
- Modify: `internal/catalog/query_builder.go`
- Tests: relevant catalog tests

- [ ] **Step 1: Write parser test**

Add to `internal/catalog/catalog_parser_test.go`:

```go
func TestCatalogParserAcceptsEbookType(t *testing.T) {
	got := parseCatalogMediaScope("ebook")
	if got != "ebook" {
		t.Fatalf("scope = %q, want ebook", got)
	}
}
```

This test lives in package `catalog`, so it can call the unexported `parseCatalogMediaScope` directly.

Run: `go test ./internal/catalog -run TestCatalogParserAcceptsEbookType -count=1`
Expected: FAIL until `ebook` is added.

- [ ] **Step 2: Accept ebook type/scope**

Update the existing switch in `internal/catalog/catalog_parser.go` from:

```go
case "movie", "series", "episode", "audiobook":
```

to:

```go
case "movie", "series", "episode", "audiobook", "ebook":
```

Run: `go test ./internal/catalog -run TestCatalogParserAcceptsEbookType -count=1`
Expected: PASS.

- [ ] **Step 3: Add ebook series facet tests**

Add resolver tests using the existing `stubFacetFetcher`, extending it with:

```go
func (s *stubFacetFetcher) EbookSeries(ctx context.Context, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error) {
	return []string{"Series A"}, nil
}
```

Assert that ebook-scoped facets include authors and series but not narrators.

Run: `go test ./internal/catalog -run Ebook -count=1`
Expected: FAIL until resolver supports ebook series.

- [ ] **Step 4: Implement ebook facets**

In `internal/catalog/catalog_resolver.go`, add ebook-native series plumbing parallel to audiobook series but reading `ebook_details.series_name`.

In `internal/catalog/browse.go`, add:

```go
func listDistinctEbookSeriesWithSource(ctx context.Context, pool *pgxpool.Pool, filters BrowseFilters, baseRelation string, mediaScope string) ([]string, error)
func searchDistinctEbookSeriesWithSource(ctx context.Context, pool *pgxpool.Pool, filters BrowseFilters, baseRelation string, mediaScope string, prefix string, limit int) ([]string, bool, error)
```

The SQL should join scoped results to `ebook_details ed ON ed.content_id = mi.content_id`, filter `ed.series_name <> ''`, and order by lowercased name.

Run: `go test ./internal/catalog -run Ebook -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/catalog
git commit -m "feat(ebooks): expose ebooks in catalog facets"
```

## Task 7: Web API Types And Sort Scope

**Files:**
- Modify: `web/src/api/types.ts`
- Modify: `web/src/lib/querySortOptions.ts`
- Tests: `web/src/**/*.test.ts`

- [ ] **Step 1: Update type unions**

In `web/src/api/types.ts`, add `"ebook"` to every media type/scope union that currently includes `"audiobook"` for catalog items:

```ts
type: "movie" | "series" | "season" | "episode" | "audiobook" | "ebook";
media_scope?: "movie" | "series" | "episode" | "audiobook" | "ebook";
```

Run: `pnpm --dir web run build`
Expected: FAIL until all narrow unions and switch checks are updated.

- [ ] **Step 2: Update sort scopes**

In `web/src/lib/querySortOptions.ts`, add `ebook` as an applicable media scope for generic sorts only: title, release/year, recently added, rating when backed by `media_items`, and relevance. Do not make audiobook-only duration/narrator sorts apply to ebooks.

Run: `pnpm --dir web run build`
Expected: PASS.

- [ ] **Step 3: Update focused frontend tests**

Run:

```bash
pnpm --dir web exec vitest run src/pages/libraryPageSearchParams.test.ts src/lib/querySortOptions.test.ts
```

Expected: PASS after updating expected arrays that enumerate all supported catalog scopes to include `ebook`.

- [ ] **Step 4: Commit**

```bash
git add web/src/api/types.ts web/src/lib/querySortOptions.ts web/src/**/*.test.ts
git commit -m "feat(ebooks): add ebook web catalog typing"
```

## Task 8: End-To-End Verification

**Files:**
- No new files unless fixing failures from prior tasks.

- [ ] **Step 1: Run scanner/catalog package tests**

```bash
go test ./internal/scanner ./internal/catalog ./internal/api/handlers ./migrations
```

Expected: PASS.

- [ ] **Step 2: Build frontend**

```bash
pnpm --dir web install --frozen-lockfile
pnpm --dir web run build
```

Expected: PASS. Chunk-size warnings are acceptable; TypeScript errors are not.

- [ ] **Step 3: Run full Go suite after web build**

```bash
go test ./...
```

Expected: PASS. The web build must happen first so `web/embed.go` has `web/dist`.

- [ ] **Step 4: Inspect branch diff**

```bash
git status --short
git log --oneline origin/main..HEAD
git diff --stat origin/main..HEAD
```

Expected: only ebook foundation changes plus the already-intended audiobooks merge base and specs on this branch. No generated `web/dist`, `node_modules`, or TypeScript build info should be tracked.

- [ ] **Step 5: Commit any verification fixes**

When verification exposes implementation mistakes, commit only the corrected source/test files:

```bash
git add <fixed files>
git commit -m "fix(ebooks): complete scanner catalog verification"
```

If no fixes were needed, do not create an empty commit.

## Self-Review Notes

- Spec coverage:
  - Scanner routing: Tasks 2-5.
  - Supported formats and local metadata: Task 2.
  - Shared table persistence: Tasks 1 and 4.
  - Ebook details table: Task 1.
  - Identity without title/year merge: Task 4.
  - Unchanged skip, missing files, and all-failed error: Tasks 3 and 5.
  - Covers: Task 5.
  - Catalog visibility and web typing: Tasks 6 and 7.
  - Verification: Task 8.
- Deliberately deferred:
  - external metadata enrichment
  - reader/OPDS/Kobo/Kindle integrations
  - reading progress/annotations
  - request workflows
