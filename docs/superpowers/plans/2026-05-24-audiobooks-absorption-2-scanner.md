# Audiobooks Absorption — Sub-plan 2: Scanner Integration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make silo's existing scanner recognize audiobook + podcast libraries (`media_folders.type='audiobooks'` or `'podcasts'`), walk them to find audio files, and write `media_items.type='audiobook'` / `media_items.type='podcast'` rows with chapter metadata and author/narrator role links. No external metadata enrichment in this sub-plan — file tags only.

**Architecture:** New constants (`PersonKindAuthor=7`, `PersonKindNarrator=8`, `audioExtensions`). New helpers (`isAudiobookLibraryType`, `isPodcastLibraryType`, `SupportsAudioFile`). New parser file `internal/scanner/audiobook.go` for the folder-to-media-items mapping. The chapter extraction reuses the existing `probe.go` ffprobe helper. Author/narrator names are upserted into the existing `people` and `item_people` tables via the same code paths used for movie actors. No changes to the matching/enrichment metadata queues — those are deferred.

**Tech Stack:** Go 1.26 + pgx; ffprobe via `os/exec` (already vendored as `internal/scanner/probe.go`); ID3/MP4 tag parsing — verify what silo already vendors before introducing a new library (acceptable: `github.com/dhowden/tag` if not already present, OR shell out to ffprobe which exposes tag dictionaries).

**Source spec:** `docs/superpowers/specs/2026-05-24-audiobooks-absorption-design.md`
**Discovery findings:** `docs/superpowers/plans/artifacts/2026-05-24-audiobooks-discovery-findings.md`

---

## File Structure

| Path | Created/Modified | Purpose |
|---|---|---|
| `internal/models/media.go` | Modify | Add `PersonKindAuthor = 7`, `PersonKindNarrator = 8` constants and extend `String()` and `PersonKindFromJob()`. |
| `internal/scanner/audio_extensions.go` | Create | Audio extension set + `SupportsAudioFile()` helper. |
| `internal/scanner/scanner.go` | Modify | Add `isAudiobookLibraryType` / `isPodcastLibraryType` helpers; extend `walkLogicalTree` to recognize audio files for those library types; extend `enqueueMetadataWork` switch with audiobook/podcast cases (no-op stubs in this sub-plan — actual enqueue methods come in later sub-plans). |
| `internal/scanner/audiobook.go` | Create | `parseAudiobookFolder` — walks one logical audiobook folder, extracts tags via ffprobe, produces (media_item, []media_file, []chapter, []person_link) tuple. |
| `internal/scanner/audiobook_test.go` | Create | Unit tests against fixture .m4b + multi-file folder. Uses real ffprobe (silo already runs it elsewhere). |
| `internal/scanner/testdata/audiobook_fixtures/` | Create | Two fixture audiobooks: one single .m4b and one multi-file folder. Use very short generated audio. |
| `internal/scanner/podcast.go` | Create | `parsePodcastShow` — filesystem-only path (folder = show, file = episode). RSS branch deferred to sub-plan 5. |
| `internal/scanner/podcast_test.go` | Create | Unit test against fixture podcast folder. |
| `internal/audiobooks/service.go` | Modify | Service grows a `Scanner` dep handle (constructor accepts it) but doesn't expose new methods yet — keeps the wiring in main.go ready for sub-plan 3. |

---

## Task 1: Add `PersonKindAuthor` / `PersonKindNarrator` constants

Discovery D6 confirmed `item_people.kind` is an unconstrained smallint; values 1–6 are in use. Add 7 = Author, 8 = Narrator.

**Files:**
- Modify: `internal/models/media.go`

### Step 1.1: Write the failing test

Append this test to whatever test file already exercises `PersonKind` (run `grep -lE 'TestPersonKind|PersonKindActor' internal/models/*_test.go` to find it; if no test exists for PersonKind, create `internal/models/media_test.go` with this test):

```go
func TestPersonKindAudiobookRoles(t *testing.T) {
	cases := []struct {
		kind PersonKind
		want string
	}{
		{PersonKindAuthor, "Author"},
		{PersonKindNarrator, "Narrator"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("%d.String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
```

Run: `go test ./internal/models/...`
Expected: fails because `PersonKindAuthor` and `PersonKindNarrator` are undefined.

### Step 1.2: Add the constants

Edit `internal/models/media.go` to add two new constants in the `const` block (line ~208), and extend `String()`:

```go
const (
	PersonKindActor     PersonKind = 1
	PersonKindDirector  PersonKind = 2
	PersonKindWriter    PersonKind = 3
	PersonKindProducer  PersonKind = 4
	PersonKindGuestStar PersonKind = 5
	PersonKindComposer  PersonKind = 6
	PersonKindAuthor    PersonKind = 7
	PersonKindNarrator  PersonKind = 8
)
```

In `String()` add two new cases before `default`:

```go
case PersonKindAuthor:
    return "Author"
case PersonKindNarrator:
    return "Narrator"
```

### Step 1.3: Run the test

`go test ./internal/models/...` → PASS.

### Step 1.4: Commit

```bash
git add internal/models/media.go internal/models/media_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): add Author and Narrator PersonKind constants

Discovery audit confirmed item_people.kind is unconstrained smallint
with values 1-6 in use. Reserve 7 = Author, 8 = Narrator for audiobook
people-links written by the upcoming scanner branches.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add audio-extension recognizer

**Files:**
- Create: `internal/scanner/audio_extensions.go`
- Create: `internal/scanner/audio_extensions_test.go`

### Step 2.1: Write the failing test

Create `internal/scanner/audio_extensions_test.go`:

```go
package scanner

import "testing"

func TestSupportsAudioFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"book.m4b", true},
		{"chapter1.mp3", true},
		{"audio.M4A", true},
		{"sample.flac", true},
		{"podcast.opus", true},
		{"poster.jpg", false},
		{"movie.mkv", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := SupportsAudioFile(tc.path); got != tc.want {
			t.Errorf("SupportsAudioFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
```

`go test ./internal/scanner/... -run TestSupportsAudioFile` → fails (`SupportsAudioFile` undefined).

### Step 2.2: Create the helper

Create `internal/scanner/audio_extensions.go`:

```go
package scanner

import (
	"path/filepath"
	"strings"
)

// audioExtensions is the set of file extensions the audiobook and podcast
// scanner branches recognize. Mirrors videoExtensions for video libraries.
var audioExtensions = map[string]bool{
	".m4b":  true,
	".m4a":  true,
	".mp3":  true,
	".flac": true,
	".opus": true,
	".ogg":  true,
}

// SupportsAudioFile reports whether the given path uses a recognized audio
// file extension.
func SupportsAudioFile(filePath string) bool {
	if filePath == "" {
		return false
	}
	return audioExtensions[strings.ToLower(filepath.Ext(filePath))]
}
```

### Step 2.3: Pass the test + commit

```bash
go test ./internal/scanner/... -run TestSupportsAudioFile
git add internal/scanner/audio_extensions.go internal/scanner/audio_extensions_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): add audio-extension recognizer for scanner

Mirrors the existing videoExtensions/SupportsVideoFile pair. Used by
upcoming audiobook and podcast scanner branches to filter directory
walks.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Library-type recognizers

Add `isAudiobookLibraryType` and `isPodcastLibraryType` helpers alongside the existing `isMovieLibraryType`.

**Files:**
- Modify: `internal/scanner/scanner.go` (around line 210, the existing `isMovieLibraryType` helper)

### Step 3.1: Write failing tests

Append to `internal/scanner/scanner_test.go` (or a new file `library_type_test.go`):

```go
func TestIsAudiobookLibraryType(t *testing.T) {
	if !isAudiobookLibraryType("audiobooks") {
		t.Error("expected 'audiobooks' to match")
	}
	if !isAudiobookLibraryType("Audiobook") {
		t.Error("expected 'Audiobook' (case-insensitive) to match")
	}
	if isAudiobookLibraryType("movies") {
		t.Error("'movies' should not match audiobook")
	}
}

func TestIsPodcastLibraryType(t *testing.T) {
	if !isPodcastLibraryType("podcasts") {
		t.Error("expected 'podcasts' to match")
	}
	if isPodcastLibraryType("series") {
		t.Error("'series' should not match podcast")
	}
}
```

`go test ./internal/scanner/...` → fails.

### Step 3.2: Add the helpers

Right after `isMovieLibraryType` (around line 217), add:

```go
func isAudiobookLibraryType(libraryType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "audiobook", "audiobooks":
		return true
	default:
		return false
	}
}

func isPodcastLibraryType(libraryType string) bool {
	switch strings.ToLower(strings.TrimSpace(libraryType)) {
	case "podcast", "podcasts":
		return true
	default:
		return false
	}
}
```

Run tests → PASS.

### Step 3.3: Commit

```bash
git add internal/scanner/scanner.go internal/scanner/scanner_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): library-type recognizers for scanner dispatch

isAudiobookLibraryType and isPodcastLibraryType match singular and
plural forms case-insensitively. Used by upcoming scanner walk
branches.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Extend `walkLogicalTree` to recognize audio files

Today the walk filters with `videoExtensions[...]`. For audiobook/podcast libraries it must instead filter with `audioExtensions[...]`. The cleanest shape is a small enum-style flag passed through the walk.

**Files:**
- Modify: `internal/scanner/scanner.go` (around lines 235–285 — the existing `walkLogicalTree`)

### Step 4.1: Read the current walk

Read lines 235–300 of scanner.go to understand the `movieLibrary bool` flag plumbing. The plan replaces that flag with a small typed `walkMode` (or extends to a per-library-type filter).

### Step 4.2: Refactor — replace `movieLibrary bool` with `walkMode`

At the top of scanner.go (near the extension maps), add:

```go
// walkMode tells walkLogicalTree which file extensions to surface and
// which library-specific filename heuristics (sample/extra skipping,
// audio-file suffixes) to apply.
type walkMode int

const (
	walkModeVideo walkMode = iota
	walkModeMovie
	walkModeAudiobook
	walkModePodcast
)

func walkModeFor(folderType string) walkMode {
	switch {
	case isMovieLibraryType(folderType):
		return walkModeMovie
	case isAudiobookLibraryType(folderType):
		return walkModeAudiobook
	case isPodcastLibraryType(folderType):
		return walkModePodcast
	default:
		return walkModeVideo
	}
}

func (m walkMode) acceptsExt(ext string) bool {
	switch m {
	case walkModeAudiobook, walkModePodcast:
		return audioExtensions[ext]
	default:
		return videoExtensions[ext]
	}
}
```

In `walkLogicalTree`, replace the `movieLibrary bool` parameter and signature with `mode walkMode`. Update the three call sites where the extension filter is currently used:

```go
// Was: if videoExtensions[strings.ToLower(filepath.Ext(logicalPath))] { ... }
// Now: if mode.acceptsExt(strings.ToLower(filepath.Ext(logicalPath))) { ... }
```

Where the walk currently calls `shouldSkipMovieSupplementalFile(logicalPath)` gated by `movieLibrary`, gate on `mode == walkModeMovie` instead. Audiobook and podcast walks do NOT skip "sample"/"extras" — those filenames are unlikely in audiobook trees.

Find every caller of `walkLogicalTree` and update them to pass the `walkMode` derived via `walkModeFor(folder.Type)` instead of the prior `movieLibrary` boolean.

### Step 4.3: Run the full scanner test suite

```bash
go test ./internal/scanner/...
```
Expected: all PASS. If a test references the old `movieLibrary bool` parameter, update it to the new `walkMode` style.

### Step 4.4: Commit

```bash
git add internal/scanner/scanner.go internal/scanner/scanner_test.go
git commit -m "$(cat <<'EOF'
refactor(scanner): replace movieLibrary bool with typed walkMode

Lets walkLogicalTree dispatch on multiple library shapes (video, movie,
audiobook, podcast) without proliferating boolean flags. Behavior for
video and movie libraries is unchanged; audiobook and podcast modes
will be consumed by the upcoming audiobook.go and podcast.go parsers.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Chapter extraction via ffprobe

The existing `internal/scanner/probe.go` already runs ffprobe and parses streams. Audiobook chapter extraction reuses the chapter section of ffprobe's JSON output. The `media_files.chapters` JSONB shape (locked by discovery D3) is:

```json
[{"index": 0, "title": "Chapter 1", "source": "embedded", "start_seconds": 0, "end_seconds": 27.944}, ...]
```

**Files:**
- Modify: `internal/scanner/probe.go` (extend to also surface chapters when present)
- Modify: `internal/scanner/probe_chapters_test.go` (existing — add audiobook fixture coverage)

### Step 5.1: Read probe.go's existing chapter handling

Run `grep -nE 'chapter|Chapter' internal/scanner/probe.go internal/scanner/probe_chapters_test.go` to find existing chapter logic. **If silo already extracts video chapters here, your job is to verify the same code returns sensible output for audio-only files and that the JSON shape matches D3 exactly.**

If chapter extraction already works for audio, this task is verification + adding the audiobook fixture test case. If it doesn't, write a `probeChapters(filePath string) ([]Chapter, error)` function that runs `ffprobe -v quiet -print_format json -show_chapters <file>` and decodes the result, mapping fields:

```go
type Chapter struct {
	Index        int     `json:"index"`
	Title        string  `json:"title"`
	Source       string  `json:"source"`        // always "embedded" for ffprobe results
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
}
```

(ffprobe returns `start_time` / `end_time` as strings — convert to float64.)

### Step 5.2: Add fixture coverage

Add a fixture .m4b with embedded chapters to `internal/scanner/testdata/audiobook_fixtures/single_book/book.m4b`. Use ffmpeg to generate one:

```bash
mkdir -p internal/scanner/testdata/audiobook_fixtures/single_book
# Generate a 10-second silent m4b with 2 embedded chapters
ffmpeg -y -f lavfi -i "anullsrc=r=22050:cl=mono" -t 10 -metadata title="Test Audiobook" \
  -metadata artist="Test Author" -metadata album="Test Series" \
  -metadata:s:a:0 language=eng \
  internal/scanner/testdata/audiobook_fixtures/single_book/raw.m4a
# Add chapters via a temporary metadata file
cat > /tmp/chapters.ffmeta <<'EOF'
;FFMETADATA1
[CHAPTER]
TIMEBASE=1/1000
START=0
END=5000
title=Intro
[CHAPTER]
TIMEBASE=1/1000
START=5000
END=10000
title=Outro
EOF
ffmpeg -y -i internal/scanner/testdata/audiobook_fixtures/single_book/raw.m4a \
  -i /tmp/chapters.ffmeta -map_metadata 1 -codec copy \
  internal/scanner/testdata/audiobook_fixtures/single_book/book.m4b
rm internal/scanner/testdata/audiobook_fixtures/single_book/raw.m4a /tmp/chapters.ffmeta
```

Add a test that calls the chapter-extraction function on `book.m4b` and asserts:

```go
func TestProbeChaptersAudiobookFixture(t *testing.T) {
	chapters, err := probeChapters("testdata/audiobook_fixtures/single_book/book.m4b")
	if err != nil {
		t.Fatalf("probeChapters: %v", err)
	}
	if len(chapters) != 2 {
		t.Fatalf("got %d chapters, want 2", len(chapters))
	}
	if chapters[0].Title != "Intro" || chapters[1].Title != "Outro" {
		t.Errorf("unexpected chapter titles: %+v", chapters)
	}
	if chapters[0].StartSeconds != 0 {
		t.Errorf("chapter 0 start = %v, want 0", chapters[0].StartSeconds)
	}
}
```

Run: `go test ./internal/scanner/... -run TestProbeChapters` → PASS.

### Step 5.3: Commit

```bash
git add internal/scanner/probe.go internal/scanner/probe_chapters_test.go internal/scanner/testdata/audiobook_fixtures/
git commit -m "$(cat <<'EOF'
feat(audiobooks): chapter extraction from .m4b via ffprobe

Surfaces the chapters array from ffprobe's -show_chapters output in the
exact JSON shape silo already uses for video chapters (index, title,
source, start_seconds, end_seconds — discovery D3). Adds a fixture
.m4b with 2 embedded chapters and a unit test against it.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Audiobook folder parser — single `.m4b` case

Produces a `parsedAudiobook` struct from a folder containing exactly one `.m4b`. Captures tags (title, author, narrator, series, year), chapter array, and a single `media_file` row.

**Files:**
- Create: `internal/scanner/audiobook.go`
- Create: `internal/scanner/audiobook_test.go`

### Step 6.1: Write the failing test

Create `internal/scanner/audiobook_test.go`:

```go
package scanner

import (
	"context"
	"testing"
)

func TestParseAudiobookFolderSingleM4B(t *testing.T) {
	ctx := context.Background()
	got, err := parseAudiobookFolder(ctx, "testdata/audiobook_fixtures/single_book")
	if err != nil {
		t.Fatalf("parseAudiobookFolder: %v", err)
	}
	if got.Title != "Test Audiobook" {
		t.Errorf("Title = %q, want %q", got.Title, "Test Audiobook")
	}
	if got.Author != "Test Author" {
		t.Errorf("Author = %q, want %q", got.Author, "Test Author")
	}
	if len(got.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(got.Files))
	}
	if len(got.Files[0].Chapters) != 2 {
		t.Errorf("file 0 chapters = %d, want 2", len(got.Files[0].Chapters))
	}
}
```

`go test ./internal/scanner/... -run TestParseAudiobookFolderSingleM4B` → fails.

### Step 6.2: Create `audiobook.go`

```go
package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// parsedAudiobook is what parseAudiobookFolder returns. The scanner
// caller converts this into the media_items + media_files + item_people
// rows it writes to the DB.
type parsedAudiobook struct {
	Title    string
	Author   string
	Narrator string
	Series   string
	Year     int
	Files    []parsedAudiobookFile
}

type parsedAudiobookFile struct {
	Path     string
	Chapters []Chapter
	// Future: duration, codec, bitrate — surfaced via probe.go on the
	// scanner write path, not here. parseAudiobookFolder only deals with
	// content/structure.
}

// parseAudiobookFolder reads a single audiobook folder and returns its
// structured representation. Recognized layouts:
//   - one .m4b in the folder (with or without embedded chapters)
//   - multiple audio files in the folder (chapters = one per file, in
//     filename order)
//
// If the folder contains zero audio files (per audioExtensions),
// parseAudiobookFolder returns os.ErrNotExist-wrapped error so the
// caller can skip it.
func parseAudiobookFolder(ctx context.Context, folderPath string) (*parsedAudiobook, error) {
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, fmt.Errorf("read audiobook folder %s: %w", folderPath, err)
	}

	var audioFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if SupportsAudioFile(entry.Name()) {
			audioFiles = append(audioFiles, filepath.Join(folderPath, entry.Name()))
		}
	}
	if len(audioFiles) == 0 {
		return nil, fmt.Errorf("audiobook folder %s: %w", folderPath, os.ErrNotExist)
	}
	sort.Strings(audioFiles)

	book := &parsedAudiobook{}

	if len(audioFiles) == 1 {
		// Single-file case: pull title/author/narrator/series/year from
		// the file's own tags and use the embedded chapter list as-is.
		if err := book.populateFromFile(ctx, audioFiles[0]); err != nil {
			return nil, err
		}
		chapters, err := probeChapters(audioFiles[0])
		if err != nil {
			return nil, fmt.Errorf("probe chapters %s: %w", audioFiles[0], err)
		}
		book.Files = []parsedAudiobookFile{{Path: audioFiles[0], Chapters: chapters}}
		return book, nil
	}

	// Multi-file case is Task 7. Reject here so the test for single-file
	// stays clean; Task 7 replaces this fallthrough.
	return nil, fmt.Errorf("audiobook folder %s: multi-file folders not yet supported", folderPath)
}

// populateFromFile reads tags from a single audio file via ffprobe and
// fills the audiobook header fields (Title, Author, Narrator, Series,
// Year). Falls back to folder-name parsing if tags are absent.
func (b *parsedAudiobook) populateFromFile(ctx context.Context, path string) error {
	tags, err := probeFormatTags(path)
	if err != nil {
		return fmt.Errorf("probe tags %s: %w", path, err)
	}
	b.Title = pickFirstNonEmpty(tags["title"], tags["album"])
	b.Author = pickFirstNonEmpty(tags["artist"], tags["album_artist"], tags["composer"])
	b.Narrator = pickFirstNonEmpty(tags["narrator"], tags["composer"], tags["performer"])
	b.Series = pickFirstNonEmpty(tags["album"], tags["series"], tags["mvnm"])
	if year := pickFirstNonEmpty(tags["date"], tags["year"]); year != "" {
		// Tag year strings can be "2021", "2021-05-23", or even "(2021)".
		if y := parseYear(year); y > 0 {
			b.Year = y
		}
	}
	return nil
}

func pickFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseYear(s string) int {
	s = strings.TrimSpace(s)
	if len(s) >= 4 {
		var y int
		if _, err := fmt.Sscanf(s[:4], "%d", &y); err == nil && y > 1000 && y < 9999 {
			return y
		}
	}
	return 0
}
```

### Step 6.3: Implement `probeFormatTags`

If `probe.go` doesn't already expose format-level tags, add a helper that runs:

```
ffprobe -v quiet -print_format json -show_format <file>
```

and returns the `format.tags` map as `map[string]string`. Reuse the existing ffprobe execution helper from `probe.go` — do not duplicate the os/exec wiring. Keys come from the file's metadata (MP4/M4B: `title`, `artist`, `composer`, `album`, `album_artist`, `date`, `narrator`, `series`; MP3/ID3: lowercase versions of the standard frames).

### Step 6.4: Pass test + commit

```bash
go test ./internal/scanner/... -run TestParseAudiobookFolder
git add internal/scanner/audiobook.go internal/scanner/audiobook_test.go internal/scanner/probe.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): parser for single-file audiobook folders

parseAudiobookFolder reads tags via ffprobe-format-tags, surfaces
chapters via the existing chapter-extraction path, and produces the
parsedAudiobook struct the scanner write path will consume in Task 8.
Single-file case only; multi-file folders return a placeholder error
and land in Task 7.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Audiobook folder parser — multi-file case

Folders with N audio files (one per chapter or part). Each file becomes a separate `media_files` row; the chapter list is synthesized from filename order — one chapter per file, title = filename stem.

**Files:**
- Modify: `internal/scanner/audiobook.go` (replace the multi-file fallthrough error)
- Create: `internal/scanner/testdata/audiobook_fixtures/multi_file/` — generate three 5s `.mp3` files with metadata

### Step 7.1: Write the failing test

In `internal/scanner/audiobook_test.go`, add:

```go
func TestParseAudiobookFolderMultiFile(t *testing.T) {
	ctx := context.Background()
	got, err := parseAudiobookFolder(ctx, "testdata/audiobook_fixtures/multi_file")
	if err != nil {
		t.Fatalf("parseAudiobookFolder: %v", err)
	}
	if got.Title != "Multi File Test" {
		t.Errorf("Title = %q, want %q", got.Title, "Multi File Test")
	}
	if len(got.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(got.Files))
	}
	for i, f := range got.Files {
		if len(f.Chapters) != 1 {
			t.Errorf("file %d: %d synthesized chapters, want 1", i, len(f.Chapters))
		}
	}
}
```

### Step 7.2: Generate fixture

```bash
mkdir -p internal/scanner/testdata/audiobook_fixtures/multi_file
for i in 1 2 3; do
  ffmpeg -y -f lavfi -i "anullsrc=r=22050:cl=mono" -t 5 \
    -metadata title="Multi File Test" \
    -metadata artist="Multi Author" \
    -metadata track="$i" \
    "internal/scanner/testdata/audiobook_fixtures/multi_file/part${i}.mp3"
done
```

### Step 7.3: Implement multi-file branch

Replace the placeholder error in `parseAudiobookFolder` with:

```go
// Multi-file case: read header from the first file, synthesize one
// chapter per file with title = filename stem.
if err := book.populateFromFile(ctx, audioFiles[0]); err != nil {
    return nil, err
}
book.Files = make([]parsedAudiobookFile, 0, len(audioFiles))
for i, path := range audioFiles {
    stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
    book.Files = append(book.Files, parsedAudiobookFile{
        Path: path,
        Chapters: []Chapter{{
            Index:        i,
            Title:        stem,
            Source:       "filename",
            StartSeconds: 0,
            EndSeconds:   0, // populated by probe on write if we want — leaving 0 is acceptable for a synthesized chapter
        }},
    })
}
return book, nil
```

### Step 7.4: Pass test + commit

```bash
go test ./internal/scanner/... -run TestParseAudiobookFolderMultiFile
git add internal/scanner/audiobook.go internal/scanner/audiobook_test.go internal/scanner/testdata/audiobook_fixtures/multi_file/
git commit -m "$(cat <<'EOF'
feat(audiobooks): multi-file audiobook folder support

Folders containing N audio files (one per chapter/part) get one
media_files row per file; the chapter list is synthesized as one
chapter per file with title = filename stem. Title/author taken from
the first file's tags.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Scanner write path — produce `media_items.type='audiobook'`

Wires `parseAudiobookFolder` into the scanner's reconciler so audiobook folders walked by `walkLogicalTree` actually produce `media_items` + `media_files` rows.

**Files:**
- Modify: `internal/scanner/scanner.go` (the function that processes walked file paths into media_item/media_file rows)

### Step 8.1: Discover the existing write path

Run:
```bash
grep -nE 'INSERT INTO media_items|writeMediaItem|UpsertMediaItem|reconcile.*media_item' internal/scanner/*.go | head -15
```

Note the function names that write `media_items` rows for movie/TV today. The audiobook write path will mirror that pattern but call `parseAudiobookFolder` to produce the row data instead of using the movie/TV inference.

### Step 8.2: Implement `reconcileAudiobookFolder`

Add a function in `audiobook.go`:

```go
// reconcileAudiobookFolder is called by the scanner once per audiobook
// subdirectory. It parses the folder, then upserts one media_items
// row (type='audiobook'), N media_files rows, and item_people links
// for the author and narrator if present.
func (s *Scanner) reconcileAudiobookFolder(ctx context.Context, folder *models.MediaFolder, folderPath string) error {
    parsed, err := parseAudiobookFolder(ctx, folderPath)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil // empty audiobook folder; skip silently
        }
        return fmt.Errorf("parse audiobook folder %s: %w", folderPath, err)
    }
    // Upsert media_items row (use the existing item-writer helper —
    // grep for upsertMediaItem or similar in scanner.go to find it).
    itemID, err := s.upsertAudiobookMediaItem(ctx, folder, parsed)
    if err != nil {
        return err
    }
    // Upsert media_files rows with chapters jsonb populated.
    if err := s.upsertAudiobookMediaFiles(ctx, itemID, parsed); err != nil {
        return err
    }
    // Upsert author/narrator into people + item_people.
    if err := s.upsertAudiobookPeople(ctx, itemID, parsed); err != nil {
        return err
    }
    return nil
}
```

Each helper (`upsertAudiobookMediaItem`, `upsertAudiobookMediaFiles`, `upsertAudiobookPeople`) follows the existing scanner patterns. Read `scanner.go` around the movie write path before implementing — the file repo and item repo are already wired through `s`. Use the existing repo handles, do not create new ones.

### Step 8.3: Wire reconcile into the main scan loop

In `scanner.go`'s `scanPaths` (or whichever function dispatches per-folder), branch on `folder.Type`:

```go
switch {
case isAudiobookLibraryType(folder.Type):
    return s.reconcileAudiobookFolder(ctx, folder, folderPath)
case isPodcastLibraryType(folder.Type):
    return s.reconcilePodcastShow(ctx, folder, folderPath) // Task 10
default:
    // existing movie/TV path
}
```

### Step 8.4: Integration test

Add an end-to-end test that:
1. Creates a temp folder with the `single_book` fixture symlinked or copied in.
2. Constructs a `media_folders` row with `type='audiobooks'` pointing at it.
3. Runs `s.ScanSubtree(ctx, &folder, tempDir)`.
4. Queries the test DB for the resulting `media_items` row and asserts `type='audiobook'`, `title='Test Audiobook'`.
5. Asserts the `media_files` row exists with `chapters` JSONB matching the fixture.
6. Asserts `item_people` has one Author link (kind=7).

This test requires a real Postgres. Use the existing test-DB pattern in `internal/scanner/scanner_test.go` (or whatever silo's test fixture is — grep for `TestMain` and `pgxpool`).

If silo doesn't have a test-DB harness available to this package, **STOP and ask** before writing a mock-DB version. Mocking the DB here would be misleading; the spec said "integration tests against the real silo Postgres".

### Step 8.5: Commit

```bash
git add internal/scanner/scanner.go internal/scanner/audiobook.go internal/scanner/audiobook_test.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): scanner write path produces audiobook media_items

reconcileAudiobookFolder takes a parsedAudiobook from
parseAudiobookFolder and upserts:
  - one media_items row with type='audiobook'
  - N media_files rows with chapters JSONB
  - author/narrator links via people + item_people (kind=7, kind=8)

The main scan loop dispatches to this path when
media_folders.type='audiobooks'. Integration test against the real
Postgres test DB verifies the round trip.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Podcast filesystem scanner (RSS deferred to sub-plan 5)

Folder = podcast show; each file inside = an episode. Mirrors the TV `series → episodes` shape silo already handles, but for audio.

**Files:**
- Create: `internal/scanner/podcast.go`
- Create: `internal/scanner/podcast_test.go`
- Create: `internal/scanner/testdata/podcast_fixtures/show_a/` (3 fixture episode mp3s)

### Step 9.1: Fixture + failing test

```bash
mkdir -p internal/scanner/testdata/podcast_fixtures/show_a
for i in 1 2 3; do
  ffmpeg -y -f lavfi -i "anullsrc=r=22050:cl=mono" -t 3 \
    -metadata title="Show A Episode $i" \
    -metadata artist="Show A Host" \
    -metadata album="Show A" \
    "internal/scanner/testdata/podcast_fixtures/show_a/ep${i}.mp3"
done
```

Test:

```go
func TestParsePodcastShow(t *testing.T) {
	ctx := context.Background()
	got, err := parsePodcastShow(ctx, "testdata/podcast_fixtures/show_a")
	if err != nil {
		t.Fatalf("parsePodcastShow: %v", err)
	}
	if got.Title != "Show A" {
		t.Errorf("Title = %q, want %q", got.Title, "Show A")
	}
	if len(got.Episodes) != 3 {
		t.Fatalf("got %d episodes, want 3", len(got.Episodes))
	}
	if got.Episodes[0].Title != "Show A Episode 1" {
		t.Errorf("ep 0 title = %q", got.Episodes[0].Title)
	}
}
```

### Step 9.2: Implement

`internal/scanner/podcast.go`:

```go
package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type parsedPodcastShow struct {
	Title    string
	Author   string
	Episodes []parsedPodcastEpisode
}

type parsedPodcastEpisode struct {
	Path  string
	Title string
}

func parsePodcastShow(ctx context.Context, folderPath string) (*parsedPodcastShow, error) {
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, fmt.Errorf("read podcast folder %s: %w", folderPath, err)
	}
	var audioFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if SupportsAudioFile(entry.Name()) {
			audioFiles = append(audioFiles, filepath.Join(folderPath, entry.Name()))
		}
	}
	if len(audioFiles) == 0 {
		return nil, fmt.Errorf("podcast show %s: %w", folderPath, os.ErrNotExist)
	}
	sort.Strings(audioFiles)

	show := &parsedPodcastShow{}
	for _, path := range audioFiles {
		tags, err := probeFormatTags(path)
		if err != nil {
			return nil, fmt.Errorf("probe tags %s: %w", path, err)
		}
		if show.Title == "" {
			show.Title = pickFirstNonEmpty(tags["album"], tags["show"])
			show.Author = pickFirstNonEmpty(tags["artist"], tags["album_artist"])
		}
		show.Episodes = append(show.Episodes, parsedPodcastEpisode{
			Path:  path,
			Title: pickFirstNonEmpty(tags["title"], filepath.Base(path)),
		})
	}
	return show, nil
}
```

### Step 9.3: Wire write path

Add `reconcilePodcastShow` to write one `media_items` row with `type='podcast'` + N `episodes` rows + N `media_files` rows. Mirror what `series → episodes` does in silo today (grep for `INSERT INTO episodes` and `episodes_seasons` to find the existing pattern).

### Step 9.4: Test + commit

```bash
go test ./internal/scanner/... -run TestParsePodcastShow
git add internal/scanner/podcast.go internal/scanner/podcast_test.go internal/scanner/testdata/podcast_fixtures/ internal/scanner/scanner.go
git commit -m "$(cat <<'EOF'
feat(audiobooks): filesystem podcast scanner

Walks a media_folders.type='podcasts' library, treating each
subdirectory as a podcast show and each audio file inside as an
episode. Writes type='podcast' to media_items and one row per episode
to the existing episodes table. RSS-subscribed podcasts (with
podcast_feeds rows) are handled by sub-plan 5's
podcastfeed.Refresher; this task covers filesystem-only ingestion.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Lint, vet, build, smoke

Verify the full sub-plan landed cleanly.

### Step 10.1: Full tests + build

```bash
cd /opt/silo-server
go build ./...
go test ./internal/scanner/... ./internal/models/... ./internal/audiobooks/...
go vet ./...
```

All green.

### Step 10.2: Rebuild + recreate silo container

```bash
sudo docker build -t silo:latest /opt/silo-server
sudo docker compose -p silo-prod up -d --force-recreate silo
until [ "$(sudo docker inspect -f '{{.State.Health.Status}}' silo-prod-silo-1 2>/dev/null)" = "healthy" ]; do sleep 2; done; echo healthy
```

### Step 10.3: Verify silo still serves the existing surface

```bash
curl -sS -o /dev/null -w "GET / -> HTTP %{http_code}\n" http://localhost:8090/
sudo docker logs --since 1m silo-prod-silo-1 2>&1 | grep -iE 'error|panic' | head -10
```

`GET /` → 200. No new ERROR-level lines.

### Step 10.4: Final commit if anything sweep-changed (else skip)

If `go vet` or `go build` produced changes (gofmt, imports), commit them:

```bash
git add -p
git commit -m "$(cat <<'EOF'
chore(audiobooks): sweep formatting after scanner integration

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

Otherwise: no commit needed.

---

## Self-review (planner)

**Spec coverage:**

| Spec section | Task |
|---|---|
| Recognize `media_folders.type='audiobooks'` | T3, T4, T8 |
| Recognize `media_folders.type='podcasts'` | T3, T4, T9 |
| Chapter extraction matching `media_files.chapters` shape | T5 |
| Author/narrator role kinds | T1, T8 |
| Single-file `.m4b` audiobooks | T6 |
| Multi-file audiobooks | T7 |
| Filesystem podcasts | T9 |
| RSS podcasts | deferred to sub-plan 5 |
| External enrichment (OpenLibrary/Google Books) | dropped per spec |

**Placeholder scan:** Task 8 has language like "grep for upsertMediaItem or similar in scanner.go to find it" — this is intentional. The scanner's write helpers are too deeply embedded for me to enumerate without reading more code; the implementer subagent will discover them. If a subagent reports BLOCKED on Task 8 because the write helpers aren't shaped how this plan assumes, the controller dispatches a discovery sub-task.

**Risks for sub-plan 2:**

- Task 8's integration test depends on silo having a test-DB harness for scanner_test.go. If it doesn't, the implementer is instructed to STOP and ask rather than fall back to mock-DB tests.
- The walkMode refactor in Task 4 touches the scanner.go walk plumbing. Implementer must update every caller of `walkLogicalTree` or the build breaks. Caller search is straightforward (grep for the function name) but the change is mechanically wide.
- Task 5 assumes silo's `probe.go` already runs ffprobe in a reusable way. If it doesn't expose chapters at the right level, the task may need to grow.
