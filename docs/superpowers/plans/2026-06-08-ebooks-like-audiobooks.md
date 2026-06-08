# Ebooks Like Audiobooks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add ebooks as a core silo media type by mirroring the audiobook scanner and metadata enrichment architecture in `main`.

**Architecture:** Ebooks are scanned by core `internal/scanner` code, then enriched by a core `internal/ebooks.Enricher` task that resolves plugin metadata providers with `content_level = 'ebook'`. The first-party `silo-plugin-ebook-metadata` provides lookup data; there is no ebook scanner plugin and no ebook details repository/table.

**Tech Stack:** Go, PostgreSQL Goose migrations, Silo scanner/catalog repositories, Silo metadata plugin chain, taskmanager scheduled tasks.

---

## File Structure

- `internal/scanner/ebook.go`: local ebook parser and format support.
- `internal/scanner/ebook_scan.go`: ebook scan flow matching `audiobook_scan.go`.
- `internal/scanner/scanner.go`: route `ebook(s)` libraries to the ebook scanner and add ebook walk mode.
- `migrations/sql/20260608000100_ebook_series.sql`: `ebook_series` table equivalent to `audiobook_series`.
- `internal/ebooks/enrichment.go`: ebook metadata sweep equivalent to `internal/audiobooks/enrichment.go`.
- `internal/taskmanager/tasks/sync_ebook_metadata.go`: scheduled/manual metadata task equivalent to `sync_audiobook_metadata.go`.
- `cmd/silo/main.go`: wire `ebooks.NewEnricher` next to `audiobooks.NewEnricher`.
- Tests live beside changed files and should pin “authors only, ISBN only, content_level ebook.”

### Task 1: Scanner Routing And Local Parser

**Files:**
- Modify: `internal/scanner/scanner.go`
- Modify: `internal/scanner/scanner_test.go`
- Create/modify: `internal/scanner/ebook.go`
- Create/modify: `internal/scanner/ebook_test.go`

- [ ] **Step 1: Write or keep failing scanner/parser tests**

Ensure these tests exist:

```go
func TestIsEbookLibraryType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"ebooks", true},
		{"ebook", true},
		{"Ebook", true},
		{"  EBOOKS  ", true},
		{"audiobooks", false},
		{"movies", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isEbookLibraryType(tc.in); got != tc.want {
			t.Errorf("isEbookLibraryType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestWalkModeForEbookLibraryTypes(t *testing.T) {
	for _, libraryType := range []string{"ebook", "ebooks", " EBOOKS "} {
		if got := walkModeFor(libraryType); got != walkModeEbook {
			t.Fatalf("walkModeFor(%q) = %v, want walkModeEbook", libraryType, got)
		}
	}
}

func TestSupportsEbookFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"book.epub", true},
		{"book.PDF", true},
		{"book.azw3", true},
		{"book.mp3", false},
		{"movie.mkv", false},
	}
	for _, tc := range cases {
		if got := SupportsEbookFile(tc.path); got != tc.want {
			t.Errorf("SupportsEbookFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run scanner/parser red test**

Run:

```bash
go test ./internal/scanner -run 'Test(IsEbook|WalkModeForEbook|WalkModeEbook|SupportsEbook|ParseEbook)' -count=1
```

Expected before implementation: fail for missing ebook mode/parser functions. Expected after existing parser work is kept: pass.

- [ ] **Step 3: Implement minimal parser and walk mode**

Implement:

```go
var ebookExtensions = map[string]bool{
	".epub": true,
	".pdf":  true,
	".mobi": true,
	".azw":  true,
	".azw3": true,
	".fb2":  true,
}

func SupportsEbookFile(filePath string) bool {
	return ebookExtensions[strings.ToLower(filepath.Ext(filePath))]
}

func isEbookLibraryType(libraryType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "ebook", "ebooks":
		return true
	default:
		return false
	}
}
```

Add `walkModeEbook` and make `walkMode.acceptsExt` call `SupportsEbookFile`.

- [ ] **Step 4: Run scanner/parser green test**

Run the same command as Step 2. Expected: `ok github.com/Silo-Server/silo-server/internal/scanner`.

- [ ] **Step 5: Commit Task 1**

```bash
git add internal/scanner/scanner.go internal/scanner/scanner_test.go internal/scanner/ebook.go internal/scanner/ebook_test.go
git commit -m "feat: add ebook scanner parser foundation"
```

### Task 2: Ebook Core Scan Persistence

**Files:**
- Create: `internal/scanner/ebook_scan.go`
- Modify: `internal/scanner/ebook_test.go`
- Modify: `internal/scanner/scanner.go`

- [ ] **Step 1: Write failing tests for audiobook-shaped ebook helpers**

Add tests for:

```go
func TestEbookIdentityConfidenceReflectsMetadataCompleteness(t *testing.T) {
	book := &parsedEbook{Title: "Tagged Ebook", Authors: []string{"Author"}, Year: 2024, ISBN: "9780306406157"}
	if got := ebookIdentityConfidence(book); got != "high" {
		t.Fatalf("complete metadata confidence = %q, want high", got)
	}
	book = &parsedEbook{Title: "Tagged Ebook", Authors: []string{"Author"}, Year: 2024}
	if got := ebookIdentityConfidence(book); got != "medium" {
		t.Fatalf("partial metadata confidence = %q, want medium", got)
	}
	book = &parsedEbook{}
	if got := ebookIdentityConfidence(book); got != "low" {
		t.Fatalf("empty metadata confidence = %q, want low", got)
	}
}

func TestResolveEbookMediaItemCreatesNewWhenRootHasNoClaim(t *testing.T) {
	finder := &fakeRootContentFinder{}
	writer := &fakeFilesystemItemWriter{}
	got, err := resolveEbookMediaItem(context.Background(), finder, writer, 7, "/library/Author/Book.epub", &parsedEbook{
		Title: "Book", Year: 2024, Authors: []string{"Author"}, Description: "Overview", Publisher: "Publisher", Genres: []string{"Fiction"}, Language: "en",
	})
	if err != nil {
		t.Fatalf("resolveEbookMediaItem: %v", err)
	}
	if got == "" || len(writer.upserts) != 1 {
		t.Fatalf("contentID/upserts = %q/%d, want id and one upsert", got, len(writer.upserts))
	}
	item := writer.upserts[0]
	if item.Type != "ebook" || item.Title != "Book" || item.Year != 2024 || item.Overview != "Overview" || item.OriginalLanguage != "en" {
		t.Fatalf("upserted ebook item = %+v", item)
	}
}
```

- [ ] **Step 2: Run red helper tests**

Run:

```bash
go test ./internal/scanner -run 'Test(EbookIdentity|ResolveEbook|EbookPeople)' -count=1
```

Expected before implementation: fail with undefined ebook scan helper functions.

- [ ] **Step 3: Implement ebook scan helpers mirroring audiobook names**

Create `internal/scanner/ebook_scan.go` with functions shaped after `audiobook_scan.go`:

```go
func (s *Scanner) ScanEbookFolder(ctx context.Context, folder *models.MediaFolder) error
func (s *Scanner) reconcileEbookFile(ctx context.Context, folder *models.MediaFolder, filePath string, skipped *int64) error
func (s *Scanner) ebookFileShouldSkip(ctx context.Context, folder *models.MediaFolder, filePath string, size int64, modifiedAt time.Time) (bool, error)
func (s *Scanner) upsertEbookMediaItem(ctx context.Context, folderID int, filePath string, book *parsedEbook) (string, error)
func resolveEbookMediaItem(ctx context.Context, rootFinder filesystemRootContentFinder, itemWriter filesystemMediaItemWriter, folderID int, filePath string, book *parsedEbook) (string, error)
func createEbookMediaItem(ctx context.Context, itemWriter filesystemMediaItemWriter, book *parsedEbook, cleanTitle string) (string, error)
func applyEbookToMediaItem(item *models.MediaItem, book *parsedEbook)
func (s *Scanner) upsertEbookMediaFile(ctx context.Context, folder *models.MediaFolder, contentID string, filePath string, size int64, modifiedAt time.Time, book *parsedEbook) error
func (s *Scanner) upsertEbookPeople(ctx context.Context, contentID string, book *parsedEbook) error
func (s *Scanner) upsertEbookSeries(ctx context.Context, contentID string, book *parsedEbook) error
func ebookIdentityConfidence(book *parsedEbook) string
```

Behavior:
- `ScanEbookFolder` walks `folder.Paths`, collects files where `SupportsEbookFile` is true, processes in a worker pool like `ScanAudiobookFolder`, logs `ebook scan: starting/progress/completed/indexed`.
- `upsertEbookMediaItem` reuses `FindContentIDByRootPath(ctx, folderID, filePath, "ebook")`, then finds by existing `media_files.file_path`, then creates a new `media_items` row.
- `applyEbookToMediaItem` sets type/title/year/overview/publisher-as-studio/genres/release date/original language.
- `upsertEbookMediaFile` writes one `media_files` row with `CanonicalRootPath`, `ObservedRootPath`, `ContentGroupKey`, `FilePath` all tied to the ebook file path or content ID in the same style as audiobook file persistence.
- `upsertEbookPeople` writes author credits only with `models.PersonKindAuthor`; it never writes `models.PersonKindNarrator`.
- `reconcileEbookFile` writes ISBN provider ID with:

```sql
INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type)
VALUES ($1, 'isbn', $2, 'ebook')
ON CONFLICT DO NOTHING
```

- [ ] **Step 4: Route ebook libraries in `ScanFolder`**

Add this block beside audiobook/podcast routing:

```go
if isEbookLibraryType(folder.Type) {
	if err := s.ScanEbookFolder(watchCtx, folder); err != nil {
		return nil, err
	}
	return &ScanResult{}, nil
}
```

- [ ] **Step 5: Run green helper tests**

Run:

```bash
go test ./internal/scanner -run 'Test(EbookIdentity|ResolveEbook|EbookPeople|ScanEbook)' -count=1
```

Expected: scanner tests pass.

- [ ] **Step 6: Commit Task 2**

```bash
git add internal/scanner/scanner.go internal/scanner/ebook_scan.go internal/scanner/ebook_test.go
git commit -m "feat: scan ebook libraries in core"
```

### Task 3: Ebook Series Migration

**Files:**
- Create: `migrations/sql/20260608000100_ebook_series.sql`
- Modify: `internal/scanner/ebook_scan.go`

- [ ] **Step 1: Add migration mirroring `audiobook_series`**

Create:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE ebook_series (
    content_id TEXT PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
    series_name TEXT NOT NULL,
    series_index NUMERIC,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ebook_series_name_lower
    ON ebook_series (LOWER(series_name), series_index NULLS LAST);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ebook_series;
-- +goose StatementEnd
```

- [ ] **Step 2: Add scanner series upsert SQL**

Use the same write/delete/skip-identical shape as `upsertAudiobookSeries`, with table name `ebook_series` and `book.SeriesIndex` parsed through existing `parseSeriesIndex`.

- [ ] **Step 3: Run migration and scanner tests**

Run:

```bash
go test ./internal/scanner -run 'Test(Ebook|ResolveEbook)' -count=1
```

Expected: pass.

- [ ] **Step 4: Commit Task 3**

```bash
git add migrations/sql/20260608000100_ebook_series.sql internal/scanner/ebook_scan.go
git commit -m "feat: persist ebook series membership"
```

### Task 4: Ebook Metadata Enricher

**Files:**
- Create: `internal/ebooks/enrichment.go`
- Create: `internal/ebooks/enrichment_test.go`

- [ ] **Step 1: Copy audiobook enricher structure with ebook domain changes**

Create `internal/ebooks/enrichment.go` from `internal/audiobooks/enrichment.go` and change:
- package `ebooks`
- log prefix `ebook enrichment`
- default env var `SILO_EBOOK_ENRICH_WORKERS`
- batch query `WHERE mi.type = 'ebook'`
- chain resolution `metadata.ResolveChain(ctx, item.FolderID, "ebook", e.chainRepo, e.resolver)`
- search query `ContentType: "ebook"`
- metadata request `ContentType: "ebook"`
- people persistence filters to author credits only; discard narrator credits if a plugin returns them
- no local ffmpeg audiobook cover fallback

- [ ] **Step 2: Add tests for chain content level and authors-only people**

Add tests that instantiate an `Enricher` with fake provider behavior where possible, or unit-test narrow helpers:

```go
func TestEbookEnricherContentType(t *testing.T) {
	if got := ebookContentType(); got != "ebook" {
		t.Fatalf("ebookContentType() = %q, want ebook", got)
	}
}

func TestFilterEbookPeopleKeepsAuthorsOnly(t *testing.T) {
	in := []models.ItemPerson{
		{Person: models.Person{Name: "Author"}, Kind: models.PersonKindAuthor},
		{Person: models.Person{Name: "Narrator"}, Kind: models.PersonKindNarrator},
	}
	got := filterEbookPeople(in)
	if len(got) != 1 || got[0].Kind != models.PersonKindAuthor || got[0].Person.Name != "Author" {
		t.Fatalf("filterEbookPeople() = %+v, want author only", got)
	}
}
```

- [ ] **Step 3: Run red/green ebook enricher tests**

Run:

```bash
go test ./internal/ebooks -count=1
```

Expected after implementation: pass.

- [ ] **Step 4: Commit Task 4**

```bash
git add internal/ebooks/enrichment.go internal/ebooks/enrichment_test.go
git commit -m "feat: add ebook metadata enricher"
```

### Task 5: Ebook Metadata Task And Main Wiring

**Files:**
- Create: `internal/taskmanager/tasks/sync_ebook_metadata.go`
- Modify: `cmd/silo/main.go`

- [ ] **Step 1: Add sync task matching audiobook task**

Create task:

```go
type SyncEbookMetadataTask struct {
	enricher *ebooks.Enricher
}

func NewSyncEbookMetadataTask(enricher *ebooks.Enricher) *SyncEbookMetadataTask {
	return &SyncEbookMetadataTask{enricher: enricher}
}

func (t *SyncEbookMetadataTask) Key() string  { return "sync_ebook_metadata" }
func (t *SyncEbookMetadataTask) Name() string { return "Sync Ebook Metadata" }
func (t *SyncEbookMetadataTask) Description() string {
	return "Fetches metadata (cover art, overview, authors) for ebooks that have not yet been enriched"
}
func (t *SyncEbookMetadataTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *SyncEbookMetadataTask) IsHidden() bool { return false }
func (t *SyncEbookMetadataTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 5 * 60 * 1000}}
}
```

- [ ] **Step 2: Wire `ebooks.NewEnricher` beside audiobook enricher**

In `cmd/silo/main.go`:
- add import `github.com/Silo-Server/silo-server/internal/ebooks`
- add `var ebookEnricher *ebooks.Enricher`
- construct it with the same dependencies as `audiobooks.NewEnricher`
- set image cacher if object storage is available and the ebook enricher supports remote image caching
- register `tasks.NewSyncEbookMetadataTask(ebookEnricher)` beside `NewSyncAudiobookMetadataTask`

- [ ] **Step 3: Run task/main build tests**

Run:

```bash
go test ./internal/taskmanager/tasks ./internal/ebooks -count=1
go test ./cmd/silo -run TestNonExistent -count=1
```

Expected: packages compile and tests pass.

- [ ] **Step 4: Commit Task 5**

```bash
git add cmd/silo/main.go internal/taskmanager/tasks/sync_ebook_metadata.go
git commit -m "feat: wire ebook metadata sync task"
```

### Task 6: Verification And PR Cleanup

**Files:**
- Review all modified files.

- [ ] **Step 1: Run focused Go tests**

```bash
go test ./internal/scanner ./internal/ebooks ./internal/taskmanager/tasks -count=1
```

Expected: pass.

- [ ] **Step 2: Run broader compile/test set**

```bash
go test ./internal/catalog ./internal/metadata ./cmd/silo -count=1
```

Expected: pass, or record unrelated baseline failures with exact package/error.

- [ ] **Step 3: Check no wrong architecture remains**

Run:

```bash
rg -n "ebook_details|silo-plugin-ebooks|PersonKindNarrator|asin" internal/scanner internal/ebooks docs/superpowers/specs docs/superpowers/plans
```

Expected:
- no `ebook_details`
- no scanner dependency on `silo-plugin-ebooks`
- no ebook code writing `PersonKindNarrator`
- no ebook code writing ASIN

- [ ] **Step 4: Commit final cleanup if needed**

```bash
git status --short
git add <only ebook-related cleanup files>
git commit -m "test: verify ebook audiobook-parity path"
```

Only commit if Step 1-3 caused file changes.
