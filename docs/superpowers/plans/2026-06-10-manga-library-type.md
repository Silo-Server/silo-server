# Manga Library Type Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `manga` library type (forked from `ebooks`) that detects a series from the folder tree, groups `.cbz`/`.cbr` chapters under a first-class `type='manga'` series item, and exposes the clean series name for metadata enrichment.

**Architecture:** Manga libraries reuse the ebook scanner/reader; chapters stay readable `ebook` items. A new manga scan path derives the series from the parent folder, find-or-creates one `type='manga'` series item per series, and links each chapter via a new `manga_chapters` table (ordered by a parsed volume/chapter index). Browse renders `manga` as series cards; the series item is the enrichment target at content level `manga`.

**Tech Stack:** Go 1.26 (`internal/scanner`, `internal/catalog`, `internal/api`), PostgreSQL (Goose migrations), pgx. Build/test in a `golang:1.26 + libvips-dev` container (see CLAUDE.md; bare-host `go test ./...` silently skips CGO/libvips packages).

Commands assume the repository root is the cwd. Run Go tests with:
`docker run --rm -v "$PWD":/src -w /src -e GOFLAGS=-mod=mod golang:1.26 sh -c 'apt-get update -qq && apt-get install -y -qq libvips-dev pkg-config && go test <pkg>'`

---

## File structure

- Create `internal/scanner/manga_parse.go` — pure parser: folder + filename → series name, volume token, numeric index. No I/O.
- Create `internal/scanner/manga_parse_test.go` — table tests over a real-filename corpus.
- Create `internal/scanner/manga_scan.go` — manga scan path (forked from `ebook_scan.go`): series detection, series-item upsert, chapter linking.
- Create `internal/scanner/manga_chapters_repo.go` — upsert/query the `manga_chapters` link table.
- Create `internal/scanner/manga_chapters_repo_test.go`.
- Create `migrations/sql/<ts>_manga_chapters.sql` — link table.
- Modify `internal/scanner/scanner.go` — route `manga` libraries to the manga path.
- Modify the library-type helpers (the `sections` package used by `isEbookLibraryType` / `IsAudiobookLibraryType`) — add `isMangaLibraryType`.
- Modify `internal/api/handlers/libraries.go` (`seedDefaultChain`, ~line 2021) — map `manga` → content levels `["manga"]`.
- Modify `internal/catalog/browse.go` / `query_builder.go` — `manga` browses as series cards.
- Modify `internal/catalog/detail.go` — manga series detail lists chapters from `manga_chapters`.
- Create `cmd/mangabackfill/main.go` (throwaway, not shipped) OR an admin endpoint — convert library 8 `ebooks → manga` + re-scan.

Implement in order; each task ends green + committed.

---

## Phase 1 — The manga parser (pure, highest value)

This is the riskiest, regression-prone unit. Build it first and in isolation.

### Task 1: Numeric index + volume from a manga filename

**Files:**
- Create: `internal/scanner/manga_parse.go`
- Test: `internal/scanner/manga_parse_test.go`

- [ ] **Step 1: Write the failing test**

```go
package scanner

import "testing"

func TestParseMangaIndex(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		wantVol  string
		wantIdx  float64
		wantHas  bool
	}{
		{"bare chapter", "One-Punch Man 178 (2023) (Digital) (LuCaZ)", "", 178, true},
		{"volume", "Bakuman v13 (2012) (Digital) (aKraa)", "v13", 13, true},
		{"chapter c-prefix", "Dead Mount Death Play c128 (2025) (Digital) (UP!) (Oak)", "", 128, true},
		{"vol-year issue", "Berserk Vol.2003 #04 (July, 2004)", "Vol.2003", 4, true},
		{"decimal chapter", "Kindergarten WARS 109.1 (2025) (Digital) (Rillant)", "", 109.1, true},
		{"subtitle then volume", "The Ancient Magus' Bride - Wizard's Blue v04 (2022) (Digital)", "v04", 4, true},
		{"no number", "Some Oneshot (2020) (Digital) (grp)", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vol, idx, has := parseMangaIndex(tc.file)
			if vol != tc.wantVol || has != tc.wantHas || (has && idx != tc.wantIdx) {
				t.Fatalf("parseMangaIndex(%q) = (%q,%v,%v), want (%q,%v,%v)", tc.file, vol, idx, has, tc.wantVol, tc.wantIdx, tc.wantHas)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scanner/ -run TestParseMangaIndex`
Expected: FAIL — `undefined: parseMangaIndex`.

- [ ] **Step 3: Write minimal implementation**

```go
package scanner

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	mangaVolYearIssue = regexp.MustCompile(`(?i)\b(Vol\.?\s*\d{4})\b.*?#\s*(\d+(?:\.\d+)?)`)
	mangaVolume       = regexp.MustCompile(`(?i)\bv(?:ol\.?)?\s*(\d+(?:\.\d+)?)\b`)
	mangaChapterC     = regexp.MustCompile(`(?i)\bc(?:h\.?)?\s*(\d+(?:\.\d+)?)\b`)
	mangaBareNumber   = regexp.MustCompile(`\b(\d+(?:\.\d+)?)\b`)
	mangaParenNoise   = regexp.MustCompile(`\([^)]*\)`) // (year) (Digital) (group) (Month, Year)
)

// parseMangaIndex extracts the ordering index (volume or chapter number) and the
// raw volume token from a manga release filename (extension already stripped).
// Returns has=false when no number is present (e.g. a one-shot).
func parseMangaIndex(name string) (volume string, index float64, has bool) {
	// Vol.YYYY #NN — the YYYY is a volume label, NN is the issue.
	if m := mangaVolYearIssue.FindStringSubmatch(name); m != nil {
		if n, err := strconv.ParseFloat(m[2], 64); err == nil {
			return strings.TrimSpace(m[1]), n, true
		}
	}
	// Strip parenthetical noise before scanning for loose numbers.
	clean := strings.TrimSpace(mangaParenNoise.ReplaceAllString(name, " "))
	if m := mangaVolume.FindStringSubmatch(clean); m != nil {
		if n, err := strconv.ParseFloat(m[1], 64); err == nil {
			return "v" + m[1], n, true
		}
	}
	if m := mangaChapterC.FindStringSubmatch(clean); m != nil {
		if n, err := strconv.ParseFloat(m[1], 64); err == nil {
			return "", n, true
		}
	}
	if m := mangaBareNumber.FindStringSubmatch(clean); m != nil {
		if n, err := strconv.ParseFloat(m[1], 64); err == nil {
			return "", n, true
		}
	}
	return "", 0, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/scanner/ -run TestParseMangaIndex -v`
Expected: PASS (all sub-cases).

- [ ] **Step 5: Commit**

```bash
git add internal/scanner/manga_parse.go internal/scanner/manga_parse_test.go
git commit -m "feat(scanner): manga filename index/volume parser"
```

### Task 2: Series name from the folder path (skip volume folders)

**Files:**
- Modify: `internal/scanner/manga_parse.go`
- Test: `internal/scanner/manga_parse_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestMangaSeriesFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		// series folder is the nearest ancestor that is not a volume marker
		{"/m/manga/Official/Kurosagi Corpse Delivery Service/V2006/Kurosagi … #10.cbz", "Kurosagi Corpse Delivery Service"},
		{"/m/manga/One-Punch Man/One-Punch Man 178 (2023) (Digital) (LuCaZ).cbz", "One-Punch Man"},
		{"/m/manga/Bakuman/v13/Bakuman v13 (2012).cbz", "Bakuman"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := mangaSeriesFromPath(tc.path); got != tc.want {
				t.Fatalf("mangaSeriesFromPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scanner/ -run TestMangaSeriesFromPath`
Expected: FAIL — `undefined: mangaSeriesFromPath`.

- [ ] **Step 3: Write minimal implementation**

```go
import "path/filepath"

// mangaVolumeFolder matches directory names that are volume markers, not series.
var mangaVolumeFolder = regexp.MustCompile(`(?i)^v(?:ol(?:ume)?\.?)?\s*\d+$`)

// mangaSeriesFromPath returns the series name: the nearest ancestor directory of
// the file whose name is not a volume marker.
func mangaSeriesFromPath(filePath string) string {
	dir := filepath.Dir(filePath)
	for dir != "" && dir != "." && dir != string(filepath.Separator) {
		base := filepath.Base(dir)
		if !mangaVolumeFolder.MatchString(strings.TrimSpace(base)) {
			return strings.TrimSpace(base)
		}
		dir = filepath.Dir(dir)
	}
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/scanner/ -run TestMangaSeriesFromPath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scanner/manga_parse.go internal/scanner/manga_parse_test.go
git commit -m "feat(scanner): manga series-name-from-folder detection"
```

### Task 3: Corpus regression test from the live library

**Files:** Modify `internal/scanner/manga_parse_test.go`

- [ ] **Step 1:** Pull ~50 real filenames from the live Manga library (folder id 8) and add a `TestParseMangaIndexCorpus` table asserting `has==true` for ≥95% (one-shots may legitimately fail). Source:
  `docker exec silo-postgres-1 psql -U silo -d silo -t -A -c "SELECT regexp_replace(file_path,'^.*/','') FROM media_files WHERE media_folder_id=8 ORDER BY random() LIMIT 50;"`
- [ ] **Step 2:** Run; investigate any miss; tighten the regexes in `manga_parse.go` if a real pattern is unhandled.
- [ ] **Step 3:** Commit `test(scanner): manga parser corpus regression`.

---

## Phase 2 — Schema + link repository

### Task 4: `manga_chapters` migration

**Files:** Create `migrations/sql/<ts>_manga_chapters.sql` via `make migrate-create NAME=manga_chapters` (do not hand-name; do not run `goose fix`).

- [ ] **Step 1:** Generate the file: `make migrate-create NAME=manga_chapters`.
- [ ] **Step 2:** Fill the up/down:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE manga_chapters (
    chapter_content_id TEXT PRIMARY KEY REFERENCES media_items(content_id) ON DELETE CASCADE,
    series_content_id  TEXT NOT NULL    REFERENCES media_items(content_id) ON DELETE CASCADE,
    chapter_index      NUMERIC,
    volume             TEXT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX manga_chapters_series ON manga_chapters (series_content_id, chapter_index NULLS LAST);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS manga_chapters;
-- +goose StatementEnd
```

- [ ] **Step 3:** Apply against a scratch DB and verify: `make migrate-up` then `make migrate-status`. Expected: the migration shows applied.
- [ ] **Step 4:** Commit `feat(db): manga_chapters link table`.

### Task 5: `manga_chapters` repository (thin SQL) + pure group-key

**Pattern note:** `internal/scanner` has **no DB-backed tests** — DB writes are thin SQL whose *decision* logic is extracted into pure functions (see `planEbookSeriesWrite`, tested with no DB at `ebook_test.go:843`). Follow that: unit-test the pure pieces; the thin SQL upsert/list is validated by the live re-scan in Phase 6. Do NOT introduce a Postgres test harness.

**Files:**
- Create: `internal/scanner/manga_chapters_repo.go`
- Test: `internal/scanner/manga_chapters_repo_test.go`

Thin DB helpers (mirroring `upsertEbookSeries`, which uses `s.fileRepo.Pool()`):

```go
// upsertMangaChapter links a chapter item to its series with an ordering index.
func upsertMangaChapter(ctx context.Context, pool *pgxpool.Pool, chapterID, seriesID string, index *float64, volume string) error
// listMangaChapters returns a series' chapter content_ids in order.
func listMangaChapters(ctx context.Context, pool *pgxpool.Pool, seriesID string) ([]string, error)
```

The only non-trivial logic is normalizing the parser output into the `chapter_index`/`volume` write — extract it pure and test it:

```go
// mangaChapterWrite turns a parsed (volume, index, has) into the (index, volume)
// values to persist: index is nil when has=false, volume is "" when absent.
func mangaChapterWrite(volume string, index float64, has bool) (idx *float64, vol string)
```

- [ ] **Step 1:** Write a pure unit test for `mangaChapterWrite`: `(has=true, idx=178)` → non-nil 178 + the volume token; `(has=false)` → nil index. No DB.
- [ ] **Step 2:** Run, verify FAIL (undefined).
- [ ] **Step 3:** Implement `mangaChapterWrite` (pure) and the two thin SQL helpers (`INSERT … ON CONFLICT (chapter_content_id) DO UPDATE SET series_content_id=…, chapter_index=…, volume=…, updated_at=NOW()`; `SELECT chapter_content_id FROM manga_chapters WHERE series_content_id=$1 ORDER BY chapter_index NULLS LAST`).
- [ ] **Step 4:** Run the `mangaChapterWrite` test, verify PASS; `go build ./internal/scanner/` to confirm the SQL helpers compile.
- [ ] **Step 5:** Commit `feat(scanner): manga_chapters repository + pure chapter-write mapping`.

---

## Phase 3 — Library type recognition + metadata levels

### Task 6: `isMangaLibraryType` helper

**Files:** Modify the library-type helper file that defines `isEbookLibraryType` / `IsAudiobookLibraryType` (find it: `grep -rn "func isEbookLibraryType\|func IsAudiobookLibraryType" internal/`). Add alongside, accepting `"manga"`:

```go
func isMangaLibraryType(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "manga":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 1:** Write a unit test asserting `isMangaLibraryType("manga")==true` and `isMangaLibraryType("ebooks")==false`.
- [ ] **Step 2:** Run, verify FAIL.
- [ ] **Step 3:** Add the function.
- [ ] **Step 4:** Run, verify PASS.
- [ ] **Step 5:** Commit `feat(scanner): recognize manga library type`.

### Task 7: Map `manga` → content level in `seedDefaultChain`

**Files:** Modify `internal/api/handlers/libraries.go` (`seedDefaultChain`, ~line 2034 `switch libraryType`). Add:

```go
	case "manga":
		levels = []string{"manga"}
```

Also update the library-type → metadata-levels mapping covered by `internal/api/handlers/libraries_metadata_levels_test.go` (the test enumerates `ebooks → ["ebook"]`).

- [ ] **Step 1:** Add a case to `libraries_metadata_levels_test.go`: `{name: "manga", libraryType: "manga", want: []string{"manga"}}`.
- [ ] **Step 2:** Run `go test ./internal/api/handlers/ -run MetadataLevels`; verify FAIL.
- [ ] **Step 3:** Add the `case "manga"` to both `seedDefaultChain` and the levels-mapping function the test exercises.
- [ ] **Step 4:** Run, verify PASS.
- [ ] **Step 5:** Commit `feat(api): manga library content level`.

---

## Phase 4 — Manga scan path

### Task 8: Route manga libraries to a manga scan

**Files:** Modify `internal/scanner/scanner.go` (the dispatch at ~line 247/276 where `isEbookLibraryType(folder.Type)` calls `ScanEbookFolder`).

- [ ] **Step 1:** Add a routing branch *before* the ebook branch:

```go
	if isMangaLibraryType(folder.Type) {
		if err := s.ScanMangaFolder(watchCtx, folder); err != nil {
			// mirror the ebook branch's error handling exactly
			...
		}
		return
	}
```

(Replicate the existing ebook branch's surrounding logic — full-scan vs incremental, scoped roots at ~line 276 — for manga.)

- [ ] **Step 2:** Create `ScanMangaFolder` in `internal/scanner/manga_scan.go` as a thin fork of `ScanEbookFolder` (`internal/scanner/ebook_scan.go`): same root collection, worker pool, reconcile, and missing-file reconciliation, but call `reconcileMangaFile` per file instead of `reconcileEbookFile`. Keep DRY where practical by extracting shared helpers; duplicate only the per-file step that differs.
- [ ] **Step 3:** Build only (no behavior test yet): `go build ./internal/scanner/`. Expected: compiles.
- [ ] **Step 4:** Commit `feat(scanner): route manga libraries to manga scan`.

### Task 9: `reconcileMangaFile` — series item + chapter link

**Pattern note:** no DB test. Extract the pure decisions and unit-test those; the find-or-create + upsert orchestration is thin and validated by the Phase 6 live re-scan.

**Files:**
- Modify `internal/scanner/manga_scan.go`
- Modify `internal/scanner/manga_parse.go` (the pure series group-key)
- Test `internal/scanner/manga_parse_test.go`

- [ ] **Step 1: Write the failing pure test** in `manga_parse_test.go` for the stable series identity key — the thing that guarantees all chapters of one series resolve to one series item, and that re-scans are idempotent:

```go
func TestMangaSeriesGroupKey(t *testing.T) {
	a := mangaSeriesGroupKey(8, "One-Punch Man")
	b := mangaSeriesGroupKey(8, "  one-punch man ")          // case/space-insensitive → same series
	c := mangaSeriesGroupKey(8, "Bakuman")
	d := mangaSeriesGroupKey(9, "One-Punch Man")             // different library → different series
	if a == "" || a != b {
		t.Fatalf("same series must yield same key: %q vs %q", a, b)
	}
	if a == c || a == d {
		t.Fatalf("different series/library must differ: a=%q c=%q d=%q", a, c, d)
	}
}
```

- [ ] **Step 2:** Run `-run TestMangaSeriesGroupKey`; verify FAIL (`undefined: mangaSeriesGroupKey`).
- [ ] **Step 3a: Implement `mangaSeriesGroupKey`** in `manga_parse.go` (pure; reuse the existing `normalizeEbookIdentityPart` if suitable, else lower+trim+collapse spaces):

```go
// mangaSeriesGroupKey is the stable identity of a manga series item: a function
// of (library, normalized series name) only — no per-file component — so every
// chapter of a series resolves to one series item and re-scans are idempotent.
func mangaSeriesGroupKey(folderID int, seriesName string) string {
	n := strings.ToLower(strings.Join(strings.Fields(seriesName), " "))
	if n == "" {
		return ""
	}
	return fmt.Sprintf("manga:series:%d:%s", folderID, n)
}
```

- [ ] **Step 3b: Implement `reconcileMangaFile`** in `manga_scan.go` (thin orchestration, no new test — validated live):
  1. `parsed, _ := parseEbookFile(filePath)` (cover, page count) — reuse.
  2. `seriesName := mangaSeriesFromPath(filePath)`; if empty, fall back to a filename-derived name (`ebookTitleFromPath`).
  3. `vol, idx, has := parseMangaIndex(strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)))`.
  4. Find-or-create the `type='manga'` series item keyed on `mangaSeriesGroupKey(folder.ID, seriesName)`; set `title = seriesName`; mark it needing enrichment (mirror how `upsertEbookMediaItem` sets a skeleton/pending status so the metadata worker enriches `type='manga'`).
  5. Upsert the chapter as today (`upsertEbookMediaItem` with the per-file group key) → `chapterID`.
  6. `idxPtr, volOut := mangaChapterWrite(vol, idx, has)`; `upsertMangaChapter(ctx, pool, chapterID, seriesID, idxPtr, volOut)`.
- [ ] **Step 4:** Run `-run TestMangaSeriesGroupKey` (PASS) and `go build ./internal/scanner/` (compiles).
- [ ] **Step 5:** Commit `feat(scanner): group manga chapters under a series item`.

### Task 10: Idempotency (covered by the pure key) + live validation note

Idempotency is guaranteed structurally: `mangaSeriesGroupKey` has no per-file component, so re-scanning re-finds the same series item, and `upsertMangaChapter`'s `ON CONFLICT (chapter_content_id)` makes chapter links idempotent. The Task 9 `TestMangaSeriesGroupKey` already pins the stable-key property.

- [ ] **Step 1:** Confirm `TestMangaSeriesGroupKey` asserts `a == b` for the same series (re-scan stability). If not already, strengthen it.
- [ ] **Step 2:** Add a `// live-validation:` comment in `manga_scan.go` noting that grouping/idempotency is verified by the Phase 6 re-scan (counts: one series per folder, no duplicate links), since there is no scanner DB test harness.
- [ ] **Step 3:** Commit `docs(scanner): note manga scan idempotency is key-derived + live-validated`.

---

## Phase 5 — Catalog browse + detail

### Task 11: Browse `manga` as series cards

**Files:** Modify `internal/catalog/browse.go` / `internal/catalog/query_builder.go`.

Manga libraries must browse the `type='manga'` series items (not the `type='ebook'` chapters). Confirm the browse type filter (`query_builder.go` Type filter, ~line 228) already restricts by the library's media scope/type; the series items are `type='manga'`, chapters `type='ebook'`, so a manga-scope browse that selects `type='manga'` yields series cards naturally.

- [ ] **Step 1:** Write a browse test (mirror existing browse tests): seed one `manga` series + two linked `ebook` chapters in a manga folder; assert a manga-scope browse returns 1 result (the series), not 3.
- [ ] **Step 2:** Run, verify FAIL or PASS — if chapters leak in, add a manga-scope rule that selects `type='manga'` for manga libraries (and excludes `type='ebook'` items that have a `manga_chapters` row).
- [ ] **Step 3:** Implement the scope rule minimally.
- [ ] **Step 4:** Run, verify PASS.
- [ ] **Step 5:** Commit `feat(catalog): browse manga libraries as series`.

### Task 12: Manga series detail lists chapters

**Files:** Modify `internal/catalog/detail.go` (`fetchEbookSeries` / `fetchBookSeries` pattern at ~line 1271).

- [ ] **Step 1:** Write a detail test: for a `manga` series item, the detail returns its chapters from `manga_chapters` ordered by `chapter_index NULLS LAST`.
- [ ] **Step 2:** Run, verify FAIL.
- [ ] **Step 3:** Add `fetchMangaChapters(ctx, seriesContentID)` querying `manga_chapters JOIN media_items` ordered by `chapter_index NULLS LAST, sort_title`, wired into the detail builder for `type='manga'`.
- [ ] **Step 4:** Run, verify PASS.
- [ ] **Step 5:** Commit `feat(catalog): manga series detail lists chapters`.

---

## Phase 6 — Backfill (convert library 8)

### Task 13: Convert + re-scan library 8

**Files:** Create `cmd/mangabackfill/main.go` (throwaway, not shipped — delete after; mirror the documented one-shot pattern that builds a DB pool from `DATABASE_URL`).

- [ ] **Step 1:** Implement: set `media_folders.type='manga'` for library 8, then trigger `ScanMangaFolder` for it (full scan). The scan rebuilds series + links from the existing files. Reconcile the now-orphaned flat `ebook` items that became chapters (they keep their `content_id`, gain a `manga_chapters` link, and the series items are new).
- [ ] **Step 2:** Dry-run against a restored copy of the production DB (the latest `/opt/silo-data/backups/silo-predeploy-*.sql.gz`) in a scratch Postgres; verify series counts are sane (~ thousands, not 32k) and chapters link.
- [ ] **Step 3:** Document the live run procedure (build static binary, `docker cp` into `silo-silo-1`, run with the container's `DATABASE_URL`, then `docker compose restart silo`). Do **not** run against production in this plan — that is an operator step gated on review.
- [ ] **Step 4:** Commit the throwaway under `cmd/mangabackfill/` with a header noting it is not product code.

---

## Phase 7 — Verify + hand off

### Task 14: Full gates + hand-off note

- [ ] **Step 1:** Run the full affected packages in the libvips container: `go vet ./internal/scanner/ ./internal/catalog/ ./internal/api/...` and `go test ./internal/scanner/ ./internal/catalog/ ./internal/api/...`. Expected: green.
- [ ] **Step 2:** `make verify-local-paths`; frontend untouched (no web changes) — skip unless a browse/detail API shape changed (then update `web/src/api/types.ts` + the manga library view in a follow-up task).
- [ ] **Step 3:** Confirm the hand-off precondition for sub-project 2: after a manga scan, each series is a `type='manga'` item with a clean folder-derived `title`, requesting enrichment at content level `manga`. Sub-project 2 (the `silo-plugin-ebook-metadata` manga source) is a separate spec/plan.
- [ ] **Step 4:** Commit any final fixes.

---

## Self-review notes

- **Spec coverage:** library type (Task 6/7), data model (Task 4/5/9), scanner series detection + parsing (Task 1/2/3/8/9), catalog browse+detail (Task 11/12), enrichment hand-off (Task 7/14), backfill (Task 13), testing (parser corpus Task 3, scanner integration Task 9/10). All spec sections map to a task.
- **Frontend:** the spec keeps the reader/chapter UI unchanged; if browse/detail responses gain a manga shape, a frontend follow-up is needed (flagged in Task 14) — out of this plan's host scope otherwise.
- **Riskiest tasks:** 2 (series-folder heuristic) and 9 (series-item identity/idempotency) — both have dedicated tests (corpus + idempotency).
