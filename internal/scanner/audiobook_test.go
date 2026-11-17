package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeRootContentFinder struct {
	contentID string
	err       error
	calls     int
}

func (f *fakeRootContentFinder) FindContentIDByRootPath(context.Context, int, string, string) (string, error) {
	f.calls++
	return f.contentID, f.err
}

type fakeFilesystemItemWriter struct {
	upserts []*models.MediaItem
}

func (f *fakeFilesystemItemWriter) Upsert(_ context.Context, item *models.MediaItem) error {
	cp := *item
	f.upserts = append(f.upserts, &cp)
	return nil
}

type fakeAudiobookPosterExec struct {
	calls int
	query string
	args  []any
}

func (f *fakeAudiobookPosterExec) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	f.calls++
	f.query = query
	f.args = append([]any(nil), args...)
	return pgconn.CommandTag{}, nil
}

type fakeAudiobookPosterReader struct {
	posterPath string
	err        error
	calls      int
}

func (f *fakeAudiobookPosterReader) GetPosterPath(context.Context, string) (string, error) {
	f.calls++
	return f.posterPath, f.err
}

type fakeScannerCoverCacher struct {
	calls     int
	data      []byte
	contentID string
}

func (f *fakeScannerCoverCacher) CacheAudiobookCover(_ context.Context, data []byte, contentID string) (string, string, error) {
	f.calls++
	f.data = append([]byte(nil), data...)
	f.contentID = contentID
	return "local/audiobooks/" + contentID + "/poster/original.test-revision.webp", "thumbhash", nil
}

func TestApplyAudiobookEmbeddedCoverStoresPosterDuringScan(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	writeFakeTool(t, ffmpegPath, "#!/bin/sh\nprintf cover-bytes\n")

	exec := &fakeAudiobookPosterExec{}
	cacher := &fakeScannerCoverCacher{}

	applied, err := applyAudiobookEmbeddedCover(
		context.Background(),
		&fakeAudiobookPosterReader{},
		exec,
		ffmpegPath,
		cacher,
		"/library/Author/Book/book.m4b",
		"content-1",
	)
	if err != nil {
		t.Fatalf("applyAudiobookEmbeddedCover: %v", err)
	}
	if !applied {
		t.Fatal("applied = false, want true")
	}
	if string(cacher.data) != "cover-bytes" || cacher.contentID != "content-1" {
		t.Fatalf("cacher got data=%q contentID=%q", string(cacher.data), cacher.contentID)
	}
	if exec.calls != 1 {
		t.Fatalf("Exec calls = %d, want 1", exec.calls)
	}
	if len(exec.args) != 3 {
		t.Fatalf("Exec args = %#v, want 3 args", exec.args)
	}
	if exec.args[0] != "local/audiobooks/content-1/poster/original.test-revision.webp" {
		t.Fatalf("poster arg = %#v", exec.args[0])
	}
	if exec.args[1] != "thumbhash" || exec.args[2] != "content-1" {
		t.Fatalf("thumb/content args = %#v", exec.args)
	}
	if !strings.Contains(exec.query, "poster_path = $1") {
		t.Fatalf("query did not update poster_path: %s", exec.query)
	}
}

func TestApplyAudiobookEmbeddedCoverPreservesExistingPoster(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	writeFakeTool(t, ffmpegPath, "#!/bin/sh\nprintf cover-bytes\n")

	reader := &fakeAudiobookPosterReader{posterPath: "local/audiobooks/content-1/poster/original.webp"}
	exec := &fakeAudiobookPosterExec{}
	cacher := &fakeScannerCoverCacher{}

	applied, err := applyAudiobookEmbeddedCover(
		context.Background(),
		reader,
		exec,
		ffmpegPath,
		cacher,
		"/library/Author/Book/book.m4b",
		"content-1",
	)
	if err != nil {
		t.Fatalf("applyAudiobookEmbeddedCover: %v", err)
	}
	if applied {
		t.Fatal("applied = true, want false")
	}
	if reader.calls != 1 {
		t.Fatalf("GetPosterPath calls = %d, want 1", reader.calls)
	}
	if cacher.calls != 0 {
		t.Fatalf("cache calls = %d, want 0", cacher.calls)
	}
	if exec.calls != 0 {
		t.Fatalf("Exec calls = %d, want 0", exec.calls)
	}
}

func TestPopulateFromTags_SeriesPrefersExplicitTag(t *testing.T) {
	b := &parsedAudiobook{}
	b.populateFromTags(map[string]string{
		"title":  "The Way of Kings",
		"album":  "The Way of Kings",
		"series": "The Stormlight Archive",
	})
	if b.Series != "The Stormlight Archive" {
		t.Fatalf("expected Series=%q, got %q", "The Stormlight Archive", b.Series)
	}
}

func TestPopulateFromTags_SeriesFallsBackToMovementName(t *testing.T) {
	b := &parsedAudiobook{}
	b.populateFromTags(map[string]string{
		"title": "The Way of Kings",
		"mvnm":  "The Stormlight Archive",
	})
	if b.Series != "The Stormlight Archive" {
		t.Fatalf("expected Series=%q (from mvnm fallback), got %q", "The Stormlight Archive", b.Series)
	}
}

func TestParseAudiobookFolderSingleM4B(t *testing.T) {
	ffprobePath := FFprobePathFromFFmpeg("ffmpeg")
	if _, err := exec.LookPath(ffprobePath); err != nil {
		ffprobePath = "ffprobe"
		if _, err := exec.LookPath(ffprobePath); err != nil {
			t.Skip("ffprobe not available")
		}
	}

	ctx := context.Background()
	got, err := parseAudiobookFolder(ctx, ffprobePath, "testdata/audiobook_fixtures/single_book", nil, nil)
	if err != nil {
		t.Fatalf("parseAudiobookFolder: %v", err)
	}
	if got.Title != "Test Audiobook" {
		t.Errorf("Title = %q, want %q", got.Title, "Test Audiobook")
	}
	if got.Author != "Test Author" {
		t.Errorf("Author = %q, want %q", got.Author, "Test Author")
	}
	// The fixture has `album: Test Series` but no real `series` or
	// `mvnm` tag. Series must stay empty — previously the parser
	// fell back to album, which polluted audiobook_series with one
	// fake singleton per book on real libraries.
	if got.Series != "" {
		t.Errorf("Series = %q, want %q (album must NOT be used as a fallback)", got.Series, "")
	}
	if got.Year != 2024 {
		t.Errorf("Year = %d, want 2024", got.Year)
	}
	if len(got.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(got.Files))
	}
	if len(got.Files[0].Chapters) != 2 {
		t.Fatalf("file 0 chapters = %d, want 2", len(got.Files[0].Chapters))
	}
	if got.Files[0].Chapters[0].Title != "Intro" || got.Files[0].Chapters[1].Title != "Outro" {
		t.Fatalf("chapter titles = %q / %q, want Intro / Outro", got.Files[0].Chapters[0].Title, got.Files[0].Chapters[1].Title)
	}
}

func TestParseAudiobookFolderEmptyFolderSignalsNoMedia(t *testing.T) {
	_, err := parseAudiobookFolder(t.Context(), "ffprobe", t.TempDir(), nil, nil)
	if !errors.Is(err, errFolderHasNoMedia) {
		t.Fatalf("empty folder error = %v, want errFolderHasNoMedia", err)
	}
}

// A missing or misconfigured ffprobe binary must not look like an empty
// folder: exec wraps fs.ErrNotExist when the binary does not exist, so a
// reconcile that skipped on os.ErrNotExist silently indexed nothing while
// reporting processed=N failed=0.
func TestParseAudiobookFolderUnusableFFprobeIsNotSkippable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "part1.m4b"), []byte("not really audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := parseAudiobookFolder(t.Context(), "/nonexistent/bin/ffprobe", dir, nil, nil)
	if err == nil {
		t.Fatal("parseAudiobookFolder with an unusable ffprobe returned no error")
	}
	if errors.Is(err, errFolderHasNoMedia) {
		t.Fatalf("error = %v, must not be reported as an empty folder", err)
	}
}

// A folder that disappears between the scan walk and the parse is a normal
// mid-scan rename/delete race, not a scan failure: it must stay skippable.
func TestParseAudiobookFolderVanishedFolderSignalsNoMedia(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "renamed-away")

	_, err := parseAudiobookFolder(t.Context(), "ffprobe", gone, nil, nil)
	if !errors.Is(err, errFolderHasNoMedia) {
		t.Fatalf("vanished folder error = %v, want errFolderHasNoMedia", err)
	}
}

func TestParseAudiobookFolderMultiFile(t *testing.T) {
	ffprobePath := FFprobePathFromFFmpeg("ffmpeg")
	if _, err := exec.LookPath(ffprobePath); err != nil {
		ffprobePath = "ffprobe"
		if _, err := exec.LookPath(ffprobePath); err != nil {
			t.Skip("ffprobe not available")
		}
	}

	ctx := context.Background()
	got, err := parseAudiobookFolder(ctx, ffprobePath, "testdata/audiobook_fixtures/multi_file", nil, nil)
	if err != nil {
		t.Fatalf("parseAudiobookFolder: %v", err)
	}
	if got.Title != "Multi File Test" {
		t.Errorf("Title = %q, want %q", got.Title, "Multi File Test")
	}
	if got.Author != "Multi Author" {
		t.Errorf("Author = %q, want %q", got.Author, "Multi Author")
	}
	if got.Year != 2023 {
		t.Errorf("Year = %d, want 2023", got.Year)
	}
	if len(got.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(got.Files))
	}
	for i, f := range got.Files {
		if len(f.Chapters) != 1 {
			t.Errorf("file %d: %d synthesized chapters, want 1", i, len(f.Chapters))
			continue
		}
		wantStem := fmt.Sprintf("part%d", i+1)
		if f.Chapters[0].Title != wantStem {
			t.Errorf("file %d: chapter title = %q, want %q", i, f.Chapters[0].Title, wantStem)
		}
		if f.Chapters[0].StartSeconds != 0 || f.Chapters[0].EndSeconds <= f.Chapters[0].StartSeconds {
			t.Errorf("file %d: synthesized chapter range = %.3f..%.3f, want positive probed duration", i, f.Chapters[0].StartSeconds, f.Chapters[0].EndSeconds)
		}
	}
}

func TestApplyAudiobookFilesystemFallbacksUsesFolderNameWhenTagsAreBlank(t *testing.T) {
	book := &parsedAudiobook{}
	book.applyFilesystemFallbacks(
		"/library/Calibre_Audio_Library/Dean Koontz/Devoted (2799)",
		[]string{"/library/Calibre_Audio_Library/Dean Koontz/Devoted (2799)/Devoted - Dean Koontz.mp3"},
	)
	if book.Title != "Devoted" {
		t.Fatalf("Title = %q, want Devoted", book.Title)
	}
}

func TestApplyAudiobookFilesystemFallbacksPreservesTaggedTitle(t *testing.T) {
	book := &parsedAudiobook{Title: "Tagged Title"}
	book.applyFilesystemFallbacks(
		"/library/Bad.Folder.Name-AudioBook",
		[]string{"/library/Bad.Folder.Name-AudioBook/part01.mp3"},
	)
	if book.Title != "Tagged Title" {
		t.Fatalf("Title = %q, want tagged title", book.Title)
	}
}

func TestCleanAudiobookFilesystemTitleRemovesCommonReleaseNoise(t *testing.T) {
	got := cleanAudiobookFilesystemTitle("Dan.Brown-Robert.Langdon.Bk.2-The.DaVinci.Code.NMR-AudioBook")
	want := "Dan Brown-Robert Langdon Bk 2-The DaVinci Code"
	if got != want {
		t.Fatalf("cleanAudiobookFilesystemTitle = %q, want %q", got, want)
	}
}

func TestAudiobookIdentityConfidenceReflectsMetadataCompleteness(t *testing.T) {
	book := &parsedAudiobook{Title: "Tagged Book", Author: "Author", Narrator: "Narrator", Year: 2024}
	file := parsedAudiobookFile{Chapters: []ChapterInfo{{Title: "One", StartSeconds: 0, EndSeconds: 10}}}
	if got := audiobookIdentityConfidence(book, file); got != "high" {
		t.Fatalf("complete metadata confidence = %q, want high", got)
	}

	book = &parsedAudiobook{Title: "Tagged Book"}
	file = parsedAudiobookFile{Chapters: []ChapterInfo{{Title: "Unknown duration"}}}
	if got := audiobookIdentityConfidence(book, file); got != "medium" {
		t.Fatalf("partial metadata confidence = %q, want medium", got)
	}

	book = &parsedAudiobook{}
	file = parsedAudiobookFile{}
	if got := audiobookIdentityConfidence(book, file); got != "low" {
		t.Fatalf("empty metadata confidence = %q, want low", got)
	}
}

func TestScanAudiobookFolderReturnsErrorWhenEveryReconcileFails(t *testing.T) {
	root := t.TempDir()
	bookDir := filepath.Join(root, "bad-book")
	if err := os.Mkdir(bookDir, 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bookDir, "chapter.mp3"), []byte("not real audio"), 0o644); err != nil {
		t.Fatalf("write fake audio: %v", err)
	}

	s := &Scanner{ffprobePath: "definitely-missing-ffprobe"}
	err := s.ScanAudiobookFolder(context.Background(), &models.MediaFolder{ID: 42, Paths: []string{root}}, true)
	if err == nil {
		t.Fatal("ScanAudiobookFolder returned nil, want aggregate failure")
	}
	if !strings.Contains(err.Error(), "folder_id=42") {
		t.Fatalf("error = %q, want folder id", err)
	}
}

func TestScanAudiobookFolderReturnsCanceledContext(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &Scanner{}
	err := s.ScanAudiobookFolder(ctx, &models.MediaFolder{ID: 42, Paths: []string{root}}, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanAudiobookFolder error = %v, want context.Canceled", err)
	}
}

func TestCollectAudiobookRootScansKeepsSiblingFoldersWhenRootHasLooseAudio(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "loose.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(root, "Sibling Book")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "part1.m4b"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	scans, err := collectAudiobookRootScans(t.Context(), 1, []string{root}, []string{root}, true, "")
	if err != nil {
		t.Fatalf("collectAudiobookRootScans: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("got %d root scans, want 1", len(scans))
	}
	got := make(map[string]bool, len(scans[0].candidates))
	for _, candidate := range scans[0].candidates {
		got[candidate] = true
	}
	if loose := filepath.Join(root, "loose.m4b"); !got[loose] {
		t.Fatalf("candidates = %#v, want loose file candidate %q", scans[0].candidates, loose)
	}
	if !got[sibling] {
		t.Fatalf("candidates = %#v, want sibling candidate %q", scans[0].candidates, sibling)
	}
}

func TestSplitAudiobookReconcileRootsProtectsOnlyFailedPaths(t *testing.T) {
	scans := []audiobookRootScan{
		{
			root: "/library/clean",
			seenPaths: map[string]bool{
				"/library/clean/book/track.mp3": true,
			},
		},
		{
			root: "/library/partial",
			candidates: []string{
				"/library/partial/found-book",
			},
			seenPaths: map[string]bool{
				"/library/partial/found-book/track.mp3": true,
			},
			walkFailures: []string{"/library/partial/unreadable"},
		},
		{
			root:    "/library/missing",
			rootErr: os.ErrNotExist,
		},
		{
			root:         "/library/unreadable",
			walkFailures: []string{"/library/unreadable"},
		},
	}

	roots, seen, protected := splitAudiobookReconcileRoots(scans)
	if !reflect.DeepEqual(roots, []string{"/library/clean", "/library/partial"}) {
		t.Fatalf("roots = %#v, want clean and partial roots", roots)
	}
	if !seen["/library/clean/book/track.mp3"] {
		t.Fatalf("seen missing clean-root track: %#v", seen)
	}
	if !seen["/library/partial/found-book/track.mp3"] {
		t.Fatalf("seen missing readable partial-root track: %#v", seen)
	}
	wantProtected := []string{"/library/partial/unreadable", "/library/missing", "/library/unreadable"}
	if !reflect.DeepEqual(protected, wantProtected) {
		t.Fatalf("protected = %#v, want %#v", protected, wantProtected)
	}
}

func TestScanSubtreeAudiobookLibraryUsesAudiobookPipeline(t *testing.T) {
	root := t.TempDir()
	bookDir := filepath.Join(root, "bad-book")
	if err := os.Mkdir(bookDir, 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bookDir, "chapter.mp3"), []byte("not real audio"), 0o644); err != nil {
		t.Fatalf("write fake audio: %v", err)
	}

	s := &Scanner{ffprobePath: "definitely-missing-ffprobe"}
	_, err := s.ScanSubtree(context.Background(), &models.MediaFolder{ID: 42, Type: "audiobooks", Paths: []string{root}}, bookDir)
	if err == nil {
		t.Fatal("ScanSubtree returned nil, want audiobook parse failure")
	}
	if strings.Contains(err.Error(), "getting existing files") {
		t.Fatalf("ScanSubtree used generic file pipeline: %v", err)
	}
	if !strings.Contains(err.Error(), "folder_id=42") {
		t.Fatalf("error = %q, want audiobook scanner aggregate failure", err)
	}
}

func TestScanSubtreeAudiobookLibraryRejectsInvalidScopedRoot(t *testing.T) {
	s := &Scanner{}

	// A relative root lets the scope pass the library check and reach the
	// audiobook scan-root validation.
	_, err := s.ScanSubtree(context.Background(), &models.MediaFolder{ID: 42, Type: "audiobooks", Paths: []string{"."}}, "")
	if err == nil {
		t.Fatal("ScanSubtree returned nil, want invalid root error")
	}
	if !strings.Contains(err.Error(), "invalid audiobook scan root") {
		t.Fatalf("error = %q, want invalid audiobook scan root", err)
	}
}

func TestScanFileAudiobookLibraryUsesAudiobookPipeline(t *testing.T) {
	root := t.TempDir()
	bookDir := filepath.Join(root, "bad-book")
	if err := os.Mkdir(bookDir, 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	filePath := filepath.Join(bookDir, "chapter.mp3")
	if err := os.WriteFile(filePath, []byte("not real audio"), 0o644); err != nil {
		t.Fatalf("write fake audio: %v", err)
	}

	s := &Scanner{ffprobePath: "definitely-missing-ffprobe"}
	err := s.ScanFile(context.Background(), filePath, &models.MediaFolder{ID: 42, Type: "audiobooks", Paths: []string{root}})
	if err == nil {
		t.Fatal("ScanFile returned nil, want audiobook parse failure")
	}
	if strings.Contains(err.Error(), "unrecognized video extension") {
		t.Fatalf("ScanFile used video extension gate: %v", err)
	}
	if !strings.Contains(err.Error(), "folder_id=42") {
		t.Fatalf("error = %q, want audiobook scanner aggregate failure", err)
	}
}

// A file change from real-time monitoring or autoscan rebuilds the books under
// the file's folder with the library's ignore files, as a library scan does. For
// a file directly in the library root, that is every book in the library.
func TestScanFileAudiobookHonorsIgnoreFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".siloignore"), "Drafts\n")
	writeTestFile(t, filepath.Join(root, "intro.mp3"), "audio")
	writeTestFile(t, filepath.Join(root, "Kept Book", "01.mp3"), "audio")
	writeTestFile(t, filepath.Join(root, "Drafts", "Draft Book", "01.mp3"), "audio")
	writeTestFile(t, filepath.Join(root, "Hidden Book", ".nomedia"), "")
	writeTestFile(t, filepath.Join(root, "Hidden Book", "01.mp3"), "audio")

	// Every book fails to parse without ffprobe, so the error names each book
	// the scan rebuilt.
	s := &Scanner{ffprobePath: "definitely-missing-ffprobe"}
	err := s.ScanFile(t.Context(), filepath.Join(root, "intro.mp3"), &models.MediaFolder{ID: 42, Type: "audiobooks", Paths: []string{root}})
	if err == nil {
		t.Fatal("ScanFile returned nil, want audiobook parse failures")
	}
	if !strings.Contains(err.Error(), "Kept Book") {
		t.Errorf("error = %q, want Kept Book rebuilt", err)
	}
	for _, excluded := range []string{"Draft Book", "Hidden Book"} {
		if strings.Contains(err.Error(), excluded) {
			t.Errorf("error = %q, want excluded %s left alone", err, excluded)
		}
	}
}

// An admin's request for an excluded file's exact path still scans it.
func TestScanFileAudiobookExplicitRequestScansExcludedFile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".siloignore"), "Drafts\n")
	file := filepath.Join(root, "Drafts", "Draft Book", "01.mp3")
	writeTestFile(t, file, "audio")

	s := &Scanner{ffprobePath: "definitely-missing-ffprobe"}
	err := s.ScanFile(WithExplicitFileRequest(t.Context()), file, &models.MediaFolder{ID: 42, Type: "audiobooks", Paths: []string{root}})
	if err == nil || !strings.Contains(err.Error(), "Draft Book") {
		t.Fatalf("ScanFile error = %v, want Draft Book rebuilt", err)
	}
}

func TestScanFileAudiobookLibraryRejectsRelativeScopedRoot(t *testing.T) {
	s := &Scanner{}

	// A relative root lets the path pass the library check and reach the
	// audiobook scan-root validation.
	err := s.ScanFile(context.Background(), "chapter.mp3", &models.MediaFolder{ID: 42, Type: "audiobooks", Paths: []string{"."}})
	if err == nil {
		t.Fatal("ScanFile returned nil, want invalid root error")
	}
	if !strings.Contains(err.Error(), "invalid audiobook scan root") {
		t.Fatalf("error = %q, want invalid audiobook scan root", err)
	}
}

func TestResolveAudiobookMediaItemReusesRootScopedContentID(t *testing.T) {
	finder := &fakeRootContentFinder{contentID: "book-root-id"}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveAudiobookMediaItem(
		context.Background(),
		finder,
		writer,
		7,
		"/library/Author/Same Title",
		&parsedAudiobook{Title: "Same Title", Year: 0, Author: "Author A"},
	)
	if err != nil {
		t.Fatalf("resolveAudiobookMediaItem: %v", err)
	}
	if got != "book-root-id" {
		t.Fatalf("contentID = %q, want root-scoped id", got)
	}
	if finder.calls != 1 {
		t.Fatalf("root finder calls = %d, want 1", finder.calls)
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("unexpected item upsert for existing root: %d", len(writer.upserts))
	}
}

func TestResolveAudiobookMediaItemCreatesNewWhenRootHasNoClaim(t *testing.T) {
	finder := &fakeRootContentFinder{}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveAudiobookMediaItem(
		context.Background(),
		finder,
		writer,
		7,
		"/library/Other Author/Same Title",
		&parsedAudiobook{Title: "Same Title", Year: 0, Author: "Author B"},
	)
	if err != nil {
		t.Fatalf("resolveAudiobookMediaItem: %v", err)
	}
	if got == "" {
		t.Fatal("contentID is empty")
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(writer.upserts))
	}
	if writer.upserts[0].ContentID != got || writer.upserts[0].Type != "audiobook" {
		t.Fatalf("upserted item = %+v, contentID %q", writer.upserts[0], got)
	}
}

func TestStripNarratorSuffix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Solitaire Read By Holly Gibbs", "Solitaire"},
		{"The Skyward Series 3 - Cytonic (UK Version: Read by Sophie Aldred)", "The Skyward Series 3 - Cytonic"},
		{"Mick Stranahan (unabridged) 1 - Skin Tight", "Mick Stranahan 1 - Skin Tight"},
		{"An Evening with Alan Titchmarsh (Unabridged)", "An Evening with Alan Titchmarsh"},
		// "Read by Celebrities" is a series name here, not a narrator credit
		{"Classic Stories and Tales Read by Celebrities 1 - Classics of Childhood, Volume One",
			"Classic Stories and Tales Read by Celebrities 1 - Classics of Childhood, Volume One"},
		{"Old Harry's Game", "Old Harry's Game"}, // unchanged
	}
	for _, c := range cases {
		got := stripNarratorSuffix(c.in)
		if got != c.want {
			t.Errorf("stripNarratorSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAudiobookDedupeTitlesMatchPunctuationVariants(t *testing.T) {
	if !audiobookDedupeTitlesMatch(
		"Witch vs. Witch: F/F Paranormal Romance Novella",
		"Witch vs. Witch - F/F Paranormal Romance Novella",
	) {
		t.Fatal("expected punctuation-only title variants to match")
	}
}

func TestAudiobookDedupeTitlesKeepDifferentBooksSeparate(t *testing.T) {
	if audiobookDedupeTitlesMatch(
		"Adventure of Constance Verity 1 - The Last Adventure of Constance Verity",
		"Adventure of Constance Verity 2 - Constance Verity Saves the World",
	) {
		t.Fatal("expected different series entries to remain separate")
	}
}

// A release date tag that is not a calendar date must not reach Postgres,
// which rejects it and fails the whole book; the year is kept.
func TestNormalizeReleaseDateForSQL(t *testing.T) {
	for in, want := range map[string]string{
		"2015-03-04":           "2015-03-04",
		"2015-03-04T00:00:00Z": "2015-03-04",
		"2015":                 "2015-01-01",
		"2015-00-00":           "2015-01-01",
		"2015-02-30":           "2015-01-01",
		"2015-13-01 extra":     "2015-01-01",
		"0000-00-00":           "",
		"abcd-01-01":           "",
		"":                     "",
	} {
		if got := normalizeReleaseDateForSQL(in); got != want {
			t.Errorf("normalizeReleaseDateForSQL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyBookToMediaItemDoesNotBlankAllNarratorSuffixTitle(t *testing.T) {
	item := &models.MediaItem{}
	applyBookToMediaItem(item, &parsedAudiobook{Title: "Read by Jim Dale"}, false)
	if item.Title != "Read by Jim Dale" {
		t.Fatalf("Title = %q, want raw title fallback", item.Title)
	}
}

// The default sort title follows a title the tags change. A sort title in
// any other form, or under the Name lock, stays.
func TestApplyBookToMediaItemSortTitleFollowsTitle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		sortTitle string
		locked    []int
		tagTitle  string
		want      string
	}{
		{"derived from the old title, new title has no article", "Saga Name 2 - Book Title, The", nil, "Book Title", ""},
		{"derived from the old title, new title has an article", "Saga Name 2 - Book Title, The", nil, "A Book Title", "Book Title, A"},
		{"set by someone", "Saga Name 02", nil, "The Book Title", "Saga Name 02"},
		{"name locked", "Saga Name 2 - Book Title, The", []int{lockedFieldName}, "Book Title", "Saga Name 2 - Book Title, The"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := &models.MediaItem{Title: "The Saga Name 2 - Book Title", SortTitle: tc.sortTitle, LockedFields: tc.locked}
			applyBookToMediaItem(item, &parsedAudiobook{Title: tc.tagTitle}, false)
			if item.SortTitle != tc.want {
				t.Errorf("SortTitle = %q, want %q", item.SortTitle, tc.want)
			}
		})
	}
}

// An admin's locked fields survive a changed-book rescan: tags only fill what
// the editor has not pinned. The item's overview starts empty, as after an
// admin clears a bad tag description.
func TestApplyBookToMediaItemKeepsLockedFields(t *testing.T) {
	book := &parsedAudiobook{
		Title:       "Tagged Title",
		Year:        2001,
		ReleaseDate: "2001-02-03",
		Genres:      []string{"Tagged Genre"},
		Publisher:   "Tagged Press",
		Overview:    "Tagged overview",
	}
	edited := "2020-05-06"
	newItem := func(locked ...int) *models.MediaItem {
		return &models.MediaItem{
			Title:        "Admin Title",
			Year:         2020,
			ReleaseDate:  &edited,
			Genres:       []string{"Admin Genre"},
			Studios:      []string{"Admin Studio"},
			LockedFields: locked,
		}
	}

	for _, tc := range []struct {
		name         string
		locked       []int
		matched      bool
		wantTitle    string
		wantYear     int
		wantGenres   []string
		wantStudios  []string
		wantOverview string
	}{
		{"nothing locked", nil, false, "Tagged Title", 2001, []string{"Admin Genre", "Tagged Genre"}, []string{"Admin Studio", "Tagged Press"}, "Tagged overview"},
		{"title locked", []int{lockedFieldName}, false, "Admin Title", 2001, []string{"Admin Genre", "Tagged Genre"}, []string{"Admin Studio", "Tagged Press"}, "Tagged overview"},
		{"release dates locked", []int{lockedFieldReleaseDates}, false, "Tagged Title", 2020, []string{"Admin Genre", "Tagged Genre"}, []string{"Admin Studio", "Tagged Press"}, "Tagged overview"},
		{"genres and studios locked", []int{lockedFieldGenres, lockedFieldStudios}, false, "Tagged Title", 2001, []string{"Admin Genre"}, []string{"Admin Studio"}, "Tagged overview"},
		{"overview locked", []int{lockedFieldOverview}, false, "Tagged Title", 2001, []string{"Admin Genre", "Tagged Genre"}, []string{"Admin Studio", "Tagged Press"}, ""},
		// A provider match owns year, genres and studios; tags keep the title
		// and only fill what is empty.
		{"provider matched", nil, true, "Tagged Title", 2020, []string{"Admin Genre"}, []string{"Admin Studio"}, "Tagged overview"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := newItem(tc.locked...)
			applyBookToMediaItem(item, book, tc.matched)
			if item.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", item.Title, tc.wantTitle)
			}
			if item.Year != tc.wantYear {
				t.Errorf("Year = %d, want %d", item.Year, tc.wantYear)
			}
			if item.ReleaseDate == nil || *item.ReleaseDate != edited {
				t.Errorf("ReleaseDate = %v, want %q kept", item.ReleaseDate, edited)
			}
			if !slices.Equal(item.Genres, tc.wantGenres) {
				t.Errorf("Genres = %v, want %v", item.Genres, tc.wantGenres)
			}
			if !slices.Equal(item.Studios, tc.wantStudios) {
				t.Errorf("Studios = %v, want %v", item.Studios, tc.wantStudios)
			}
			if item.Overview != tc.wantOverview {
				t.Errorf("Overview = %q, want %q", item.Overview, tc.wantOverview)
			}
		})
	}
}

func TestAudiobookFolderUnchanged(t *testing.T) {
	now := time.Now().UTC()
	earlier := now.Add(-time.Hour)
	const a, b = "/lib/Author/Book/a.m4b", "/lib/Author/Book/b.m4b"
	partA := []models.MediaChapter{{Index: 0, Title: "a", EndSeconds: 60}}
	partB := []models.MediaChapter{{Index: 1, Title: "b", EndSeconds: 60}}
	for _, tc := range []struct {
		name   string
		files  []*models.MediaFile
		onDisk []audiobookDiskFile
		want   bool
	}{
		{name: "sizes and mtimes match",
			files:  []*models.MediaFile{{FilePath: a, FileSize: 100, FileModifiedAt: &now, Chapters: partA}, {FilePath: b, FileSize: 200, FileModifiedAt: &now, Chapters: partB}},
			onDisk: []audiobookDiskFile{{Path: a, Size: 100, ModTime: now}, {Path: b, Size: 200, ModTime: now}},
			want:   true},
		{name: "single file without chapters matches",
			files:  []*models.MediaFile{{FilePath: a, FileSize: 100, FileModifiedAt: &now, Chapters: []models.MediaChapter{}}},
			onDisk: []audiobookDiskFile{{Path: a, Size: 100, ModTime: now}},
			want:   true},
		{name: "multipart part whose chapters were wiped is reprocessed",
			files:  []*models.MediaFile{{FilePath: a, FileSize: 100, FileModifiedAt: &now, Chapters: partA}, {FilePath: b, FileSize: 200, FileModifiedAt: &now, Chapters: []models.MediaChapter{}}},
			onDisk: []audiobookDiskFile{{Path: a, Size: 100, ModTime: now}, {Path: b, Size: 200, ModTime: now}}},
		{name: "legacy cover-art rows bypass the fast path",
			files: []*models.MediaFile{{
				FilePath: a, FileSize: 100, FileModifiedAt: &now,
				BaseType: "audiobook", CodecVideo: "mjpeg", CodecAudio: "aac",
				VideoTracks: []models.VideoTrack{{Codec: "mjpeg"}}, AudioTracks: []models.AudioTrack{{Codec: "aac"}},
			}},
			onDisk: []audiobookDiskFile{{Path: a, Size: 100, ModTime: now}}},
		{name: "size differs",
			files:  []*models.MediaFile{{FilePath: a, FileSize: 100, FileModifiedAt: &now}},
			onDisk: []audiobookDiskFile{{Path: a, Size: 101, ModTime: now}}},
		{name: "new file appeared",
			files:  []*models.MediaFile{{FilePath: a, FileSize: 100, FileModifiedAt: &now}},
			onDisk: []audiobookDiskFile{{Path: a, Size: 100, ModTime: now}, {Path: b, Size: 200, ModTime: now}}},
		{name: "mtime differs",
			files:  []*models.MediaFile{{FilePath: a, FileSize: 100, FileModifiedAt: &earlier}},
			onDisk: []audiobookDiskFile{{Path: a, Size: 100, ModTime: now}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := audiobookFolderUnchanged(tc.files, tc.onDisk); got != tc.want {
				t.Fatalf("audiobookFolderUnchanged = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAudiobookPeopleCreditsEqual(t *testing.T) {
	tests := []struct {
		name     string
		existing []models.ItemPerson
		desired  []audiobookCredit
		want     bool
	}{
		{
			name: "equal sets in same order",
			existing: []models.ItemPerson{
				{Person: models.Person{Name: "Author A"}, Kind: models.PersonKindAuthor, SortOrder: 0},
				{Person: models.Person{Name: "Narrator N"}, Kind: models.PersonKindNarrator, SortOrder: 1},
			},
			desired: []audiobookCredit{
				{Name: "Author A", Kind: models.PersonKindAuthor},
				{Name: "Narrator N", Kind: models.PersonKindNarrator},
			},
			want: true,
		},
		{
			name: "extra existing credit",
			existing: []models.ItemPerson{
				{Person: models.Person{Name: "Author A"}, Kind: models.PersonKindAuthor, SortOrder: 0},
				{Person: models.Person{Name: "Narrator N"}, Kind: models.PersonKindNarrator, SortOrder: 1},
			},
			desired: []audiobookCredit{
				{Name: "Author A", Kind: models.PersonKindAuthor},
			},
			want: false,
		},
		{
			name: "name case differs - still equal",
			existing: []models.ItemPerson{
				{Person: models.Person{Name: "AUTHOR A"}, Kind: models.PersonKindAuthor, SortOrder: 0},
			},
			desired: []audiobookCredit{
				{Name: "Author A", Kind: models.PersonKindAuthor},
			},
			want: true,
		},
		{
			name: "different kind",
			existing: []models.ItemPerson{
				{Person: models.Person{Name: "X"}, Kind: models.PersonKindAuthor, SortOrder: 0},
			},
			desired: []audiobookCredit{
				{Name: "X", Kind: models.PersonKindNarrator},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := audiobookPeopleCreditsEqual(tt.existing, tt.desired); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// A monitor or webhook scan scoped to a book folder that was deleted is an
// empty scope, so the book's files can be reconciled. A missing library root,
// or a missing parent above the book, stays an unreadable path.
// A duplicate candidate must carry every tagged credit, and may carry more:
// a provider match stores fuller credits, and renaming that book's folder
// must keep its item and progress.
func TestAudiobookCreditsContain(t *testing.T) {
	person := func(name string, kind models.PersonKind) models.ItemPerson {
		return models.ItemPerson{Person: models.Person{Name: name}, Kind: kind}
	}
	tags := []audiobookCredit{
		{Name: "Author A", Kind: models.PersonKindAuthor},
		{Name: "Reader One", Kind: models.PersonKindNarrator},
		{Name: "Reader Two", Kind: models.PersonKindNarrator},
	}
	tests := []struct {
		name     string
		existing []models.ItemPerson
		want     bool
	}{
		{"equal sets", []models.ItemPerson{
			person("author a", models.PersonKindAuthor), person("Reader One", models.PersonKindNarrator), person("Reader Two", models.PersonKindNarrator),
		}, true},
		{"provider credits are a superset", []models.ItemPerson{
			person("Author A", models.PersonKindAuthor), person("Reader One", models.PersonKindNarrator),
			person("Reader Two", models.PersonKindNarrator), person("Reader Three", models.PersonKindNarrator),
		}, true},
		{"shares only the first narrator", []models.ItemPerson{
			person("Author A", models.PersonKindAuthor), person("Reader One", models.PersonKindNarrator),
		}, false},
	}
	for _, tc := range tests {
		if got := audiobookCreditsContain(tc.existing, tags); got != tc.want {
			t.Errorf("%s: audiobookCreditsContain = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A moved or renamed folder keeps its item; two copies on disk are two items.
func TestPickAudiobookDuplicate(t *testing.T) {
	tags := []audiobookCredit{
		{Name: "Author A", Kind: models.PersonKindAuthor},
		{Name: "Reader One", Kind: models.PersonKindNarrator},
	}
	provider := map[string][]models.ItemPerson{"old": {
		{Person: models.Person{Name: "Author A"}, Kind: models.PersonKindAuthor},
		{Person: models.Person{Name: "Reader One"}, Kind: models.PersonKindNarrator},
		{Person: models.Person{Name: "Reader Two"}, Kind: models.PersonKindNarrator},
	}}
	files := map[string][]string{"old": {"/lib/Old Folder/part1.mp3", "/lib/Old Folder/part2.mp3"}}
	statWith := func(err error) func(string) (os.FileInfo, error) {
		return func(string) (os.FileInfo, error) { return nil, err }
	}
	tests := []struct {
		name string
		stat func(string) (os.FileInfo, error)
		want string
	}{
		{"moved folder with provider credits", statWith(os.ErrNotExist), "old"},
		{"second copy while the first exists", statWith(nil), ""},
		{"old path cannot be checked", statWith(os.ErrPermission), ""},
	}
	for _, tc := range tests {
		if got := pickAudiobookDuplicate([]string{"old"}, provider, files, tags, tc.stat); got != tc.want {
			t.Errorf("%s: pickAudiobookDuplicate = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCollectAudiobookRootScansTreatsVanishedBookFolderAsEmpty(t *testing.T) {
	library := t.TempDir()
	// The encoded scope must also decode its parent before checking it.
	author := filepath.Join(library, "A\xe5thor")
	if err := os.MkdirAll(author, 0o755); err != nil {
		t.Fatal(err)
	}
	missingLibrary := filepath.Join(t.TempDir(), "unmounted")
	// A dangling book symlink with a non-UTF-8 name: the scope is the encoded
	// path, and the link still exists, so it stays protected.
	danglingLegacy := filepath.Join(author, "B\xe5k")
	if err := os.Symlink(filepath.Join(library, "missing-target"), danglingLegacy); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		scope      string
		libRoot    string
		wantFailed bool
	}{
		{"deleted book folder below encoded parent", fspath.Encode(filepath.Join(author, "Gone Book")), library, false},
		{"deleted author and book folders", filepath.Join(library, "Gone Author", "Gone Book"), library, true},
		{"missing library root", missingLibrary, missingLibrary, true},
		{"dangling legacy-named book symlink", fspath.Encode(danglingLegacy), library, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scans, err := collectAudiobookRootScans(t.Context(), 1, []string{tc.scope}, []string{tc.libRoot}, true, "")
			if err != nil {
				t.Fatalf("collectAudiobookRootScans: %v", err)
			}
			if len(scans) != 1 {
				t.Fatalf("got %d root scans, want 1", len(scans))
			}
			if got := scans[0].failed(); got != tc.wantFailed {
				t.Fatalf("failed() = %v (rootErr %v), want %v", got, scans[0].rootErr, tc.wantFailed)
			}
			if len(scans[0].candidates) != 0 {
				t.Fatalf("candidates = %v, want none", scans[0].candidates)
			}
		})
	}
}

func TestSplitAudiobookCredits(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"Stephen King", []string{"Stephen King"}},
		{"Reader One; Reader Two", []string{"Reader One", "Reader Two"}},
		{"First Writer, Second Writer", []string{"First Writer", "Second Writer"}},
		{"Douglas Preston & Lincoln Child", []string{"Douglas Preston", "Lincoln Child"}},
		{"Jeff VanderMeer and Ann VanderMeer, Neil Gaiman", []string{"Jeff VanderMeer", "Ann VanderMeer", "Neil Gaiman"}},
		// One-word pieces mean the separator sits inside one name.
		{"King, Stephen", []string{"King, Stephen"}},
		{"Martin Luther King, Jr.", []string{"Martin Luther King, Jr."}},
		{"Simon & Schuster", []string{"Simon & Schuster"}},
		{"Reader One; reader one", []string{"Reader One"}},
		{"", nil},
	}
	for _, tc := range cases {
		if got := splitAudiobookCredits(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitAudiobookCredits(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	// A crafted tag cannot fan out into unbounded person writes.
	many := make([]string, 50)
	for i := range many {
		many[i] = fmt.Sprintf("Reader %d", i)
	}
	if got := splitAudiobookCredits(strings.Join(many, ";")); len(got) != maxAudiobookCreditsPerRole {
		t.Errorf("50 narrators split into %d credits, want the cap of %d", len(got), maxAudiobookCreditsPerRole)
	}
}

func TestParseSeriesIndex(t *testing.T) {
	cases := map[string]float64{
		"5": 5, "1.5": 1.5, "2 of 8": 2, "Book 3": 3, "#3": 3, "Vol. 3": 3, "Volume 12": 12, "III": 3, "xiv": 14,
	}
	for raw, want := range cases {
		got := parseSeriesIndex(raw)
		if got == nil || *got != want {
			t.Errorf("parseSeriesIndex(%q) = %v, want %v", raw, got, want)
		}
	}
	for _, raw := range []string{"", "Prequel", "IIII", "Book"} {
		if got := parseSeriesIndex(raw); got != nil {
			t.Errorf("parseSeriesIndex(%q) = %v, want nil", raw, *got)
		}
	}
}

func TestPopulateFromTagsReadsGroupingSeriesAndLongDescription(t *testing.T) {
	cases := []struct {
		tags              map[string]string
		wantName, wantPos string
	}{
		{map[string]string{"grouping": "Grouping Saga #2"}, "Grouping Saga", "#2"},
		{map[string]string{"grouping": "The Expanse, Book 4; Other #1"}, "The Expanse", "Book 4"},
		// Grouping without a position is not a series: it often holds a store
		// category or a work name.
		{map[string]string{"grouping": "Science Fiction"}, "", ""},
		// It is the series when album_artist repeats it and does not name
		// the author, as inAudible writes it.
		{map[string]string{"artist": "Lee Child", "album_artist": "Jack Reacher", "grouping": "Jack Reacher"}, "Jack Reacher", ""},
		{map[string]string{"artist": "Ann Author", "album_artist": "Ann Author", "grouping": "Ann Author"}, "", ""},
		{map[string]string{"album_artist": "Ann Author", "grouping": "Ann Author"}, "", ""},
		// A grouping that names the author is the author with a series note.
		{map[string]string{"artist": "Stephen King", "album_artist": "Stephen King (The Dark Tower)", "grouping": "Stephen King (The Dark Tower)"}, "", ""},
		{map[string]string{"artist": "King, Stephen", "album_artist": "Stephen King (The Dark Tower)", "grouping": "Stephen King (The Dark Tower)"}, "", ""},
		// An explicit series tag wins; grouping fills its missing position.
		{map[string]string{"series": "Saga", "grouping": "Saga Vol. 3"}, "Saga", "Vol. 3"},
		{map[string]string{"series": "Saga", "grouping": "Other #3"}, "Saga", ""},
	}
	for _, tc := range cases {
		b := &parsedAudiobook{}
		b.populateFromTags(tc.tags)
		if b.Series != tc.wantName || b.SeriesPosition != tc.wantPos {
			t.Errorf("tags %v: series %q pos %q, want %q %q", tc.tags, b.Series, b.SeriesPosition, tc.wantName, tc.wantPos)
		}
	}

	b := &parsedAudiobook{}
	b.populateFromTags(map[string]string{"description": "short", "synopsis": "the long one"})
	if b.Overview != "the long one" {
		t.Errorf("Overview = %q, want the synopsis", b.Overview)
	}
}

func TestParseGenresFromTags(t *testing.T) {
	cases := []struct {
		tags map[string]string
		want []string
	}{
		{map[string]string{"genre": "Mystery, Thriller & Suspense/Thriller & Suspense"}, []string{"Mystery, Thriller & Suspense", "Thriller & Suspense"}},
		{map[string]string{"genre": "Fantasy;Sci-Fi"}, []string{"Fantasy", "Sci-Fi"}},
		{map[string]string{"genre": "Fantasy, Horror"}, []string{"Fantasy", "Horror"}},
		// Audible joins categories with commas; its comma names stay whole.
		{map[string]string{"genre": "Mystery, Thriller & Suspense, Cozy"}, []string{"Mystery, Thriller & Suspense", "Cozy"}},
		{map[string]string{"genre": "Cozy, Mystery"}, []string{"Cozy", "Mystery"}},
		{map[string]string{"genre": "Mystery,Thriller  & Suspense,Cozy"}, []string{"Mystery, Thriller & Suspense", "Cozy"}},
		// tmp_genreN tags hold one genre each and replace the genre tag.
		{
			map[string]string{"genre": "Mystery, Thriller & Suspense, Cozy", "tmp_genre1": "Mystery, Thriller & Suspense;", "tmp_genre2": "Cozy", "tmp_genre3": "cozy;"},
			[]string{"Mystery, Thriller & Suspense", "Cozy"},
		},
		{map[string]string{"genre": "Fantasy", "tmp_genre2": " Epic ; "}, []string{"Epic"}},
	}
	for _, tc := range cases {
		if got := parseGenresFromTags(tc.tags); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseGenresFromTags(%q) = %q, want %q", tc.tags, got, tc.want)
		}
	}
}

func TestAudiobookTagTitle(t *testing.T) {
	cases := []struct {
		title, album, series, position, want string
	}{
		{"Book Title", "", "", "", "Book Title"},
		{"", "Book Title", "", "", "Book Title"},
		{"Book Title", "book title", "", "", "Book Title"},
		// The title is the album behind a series prefix.
		{"Saga Name 2 - Book Title", "Book Title", "Saga Name", "", "Book Title"},
		{"Saga Name 2 - Book Title", "Book Title", "", "", "Book Title"},
		{"Saga Name, Book 2.5: Book Title", "Book Title", "", "", "Book Title"},
		{"Saga Name #3 - Book Title (Unabridged)", "Book Title", "", "", "Book Title"},
		{"Saga Name - Book Title", "Book Title", "Saga Name", "", "Book Title"},
		{"Saga Name III - Book Title", "Book Title", "Saga Name", "", "Book Title"},
		// Behind the series prefix, the title repeats the series or the album
		// after the album.
		{"Saga Name 2 - Book Title Saga Name", "Book Title", "Saga Name", "", "Book Title"},
		{"Saga Name 2 - Book Title Book Title", "Book Title", "Saga Name", "", "Book Title"},
		{"Saga Name 2 - Book Title Other Words", "Book Title", "Saga Name", "", "Saga Name 2 - Book Title Other Words"},
		{"Saga Name 2 - Book Titles", "Book Title", "Saga Name", "", "Saga Name 2 - Book Titles"},
		// Behind the series prefix, the album goes on to name the series or
		// a position, or adds a subtitle.
		{"Saga Name 2 - Book Title", "Book Title: Saga Name 2", "Saga Name", "", "Book Title"},
		{"Saga Name 2 - Book Title (Unabridged)", "Book Title, Part 2", "Saga Name", "", "Book Title"},
		{"Saga Name 2 - Book Title", "Book Title: The Long Subtitle", "Saga Name", "", "Book Title: The Long Subtitle"},
		{"Saga Name 2 - Book Title", "Book Title Collected", "Saga Name", "", "Saga Name 2 - Book Title"},
		{"Saga Name 2 - ", "Book Title", "Saga Name", "", "Saga Name 2 -"},
		// The album agrees with the title whatever its punctuation.
		{"Saga Name 2 - Her Friends Book", "Her Friend\u2019s Book", "Saga Name", "", "Her Friend\u2019s Book"},
		{"Saga Name 2 - Her Friends Book Saga Name", "Her Friend\u2019s Book", "Saga Name", "", "Her Friend\u2019s Book"},
		// An album that repeats the whole title leaves the prefix to the
		// position tag.
		{"Saga Name 2 - Book Title", "Saga Name 2 - Book Title", "Saga Name", "2", "Book Title"},
		{"Saga Name 2 - Book Title", "Saga Name 2 - Book Title", "Saga Name", "", "Saga Name 2 - Book Title"},
		{"Saga Name 2 - Book Title", "Saga Name 2 - Book Title: The Long Subtitle", "Saga Name", "2", "Book Title: The Long Subtitle"},
		{"Saga Name 2 - Book Title", "Saga Name 2 - Book Title: Saga Name, Book 2", "Saga Name", "2", "Book Title"},
		// A numbered prefix that names the series among other words.
		{"The Saga Name 2 - Book Title", "Book Title", "Saga Name;", "", "Book Title"},
		// A prefix carrying the position tag's number was composed from the
		// tags: it is cut off whatever the album says, and a prefix with
		// another number is not.
		{"Saga Name 2 - Book Title", "Other Words", "Saga Name", "2", "Book Title"},
		{"Saga Name 2 - Book Title", "First Part: Book Title", "Saga Name", "2.0", "Book Title"},
		{"Saga Name 2 - Book Title Other Words", "Book Title", "Saga Name", "2", "Book Title Other Words"},
		{"Saga Name 2 - Book Title", "Other Words", "Saga Name", "3", "Saga Name 2 - Book Title"},
		{"Saga Name - Book Title", "Other Words", "Saga Name", "2", "Saga Name - Book Title"},
		// The position tag may carry a marker or be a Roman numeral, and the
		// album may be missing.
		{"Saga Name 2 - Book Title", "Other Words", "Saga Name", "Book #2", "Book Title"},
		{"Saga Name III - Book Title", "Other Words", "Saga Name", "3", "Book Title"},
		{"Saga Name 2 - Book Title", "", "Saga Name", "2", "Book Title"},
		{"Saga Name 2 - Book Title", "", "Saga Name", "", "Saga Name 2 - Book Title"},
		{"Saga Name 2 -", "", "Saga Name", "2", "Saga Name 2 -"},
		// With a series tag only that series is a prefix, so a number in the
		// work's own name stays; words that look like Roman numerals are not
		// positions.
		{"Other Words - Book Title", "Book Title", "Saga Name", "", "Other Words - Book Title"},
		{"Apollo 13 - The Mission", "The Mission", "Space Saga", "", "Apollo 13 - The Mission"},
		{"Saga Civil - Book Title", "Book Title", "Saga", "", "Saga Civil - Book Title"},
		// The album adds a subtitle to the title.
		{"Book Title", "Book Title: The Long Subtitle", "", "", "Book Title: The Long Subtitle"},
		{"Book Title", "Book Title - The Long Subtitle", "", "", "Book Title - The Long Subtitle"},
		// An album that adds the series is not a subtitle.
		{"Book Title", "Book Title: Saga Name, Book 1", "", "", "Book Title"},
		{"Book Title", "Book Title: Saga Name, Book III", "", "", "Book Title"},
		{"Book Title", "Book Title: Saga Name (Book 1)", "", "", "Book Title"},
		// "Civil" is letters a Roman numeral uses, not a numeral.
		{"Book Title", "Book Title: A Volume Civil", "", "", "Book Title: A Volume Civil"},
		{"Book Title", "Book Title - Saga Name", "Saga Name", "", "Book Title"},
		// The series must appear as whole words, not inside one.
		{"Book Title", "Book Title: A Little Adventure", "It", "", "Book Title: A Little Adventure"},
		// Words in any script count: an album adding a non-Latin series is
		// not a subtitle, and a non-Latin series prefix is still a prefix.
		{"星河", "星河 - 远山书系", "远山书系", "", "星河"},
		{"远山书系 3 - 星河", "星河", "远山书系", "", "星河"},
		// Unrelated tags keep the title: a single file's album may be its
		// series or a collection.
		{"Book Title", "Saga Name", "Saga Name", "", "Book Title"},
		{"Book Title", "Book Titles Collected", "", "", "Book Title"},
		{"Prologue: Book Title", "Book Title", "", "", "Prologue: Book Title"},
	}
	for _, tc := range cases {
		if got := audiobookTagTitle(tc.title, tc.album, tc.series, tc.position); got != tc.want {
			t.Errorf("audiobookTagTitle(%q, %q, %q, %q) = %q, want %q", tc.title, tc.album, tc.series, tc.position, got, tc.want)
		}
	}

	// The position comes from the book's series position tag.
	b := &parsedAudiobook{}
	b.populateFromTags(map[string]string{"title": "Saga Name 2 - Book Title", "album": "Other Words", "series": "Saga Name", "series-part": "2"})
	if b.Title != "Book Title" {
		t.Errorf("title read from tags = %q, want %q", b.Title, "Book Title")
	}
}

func TestPopulateFromTagsAuthors(t *testing.T) {
	cases := []struct {
		tags map[string]string
		want []string
	}{
		// artist names the author; album_artist may hold the series, as
		// inAudible writes it, or the author with a series note.
		{map[string]string{"artist": "Lee Child", "album_artist": "Jack Reacher", "composer": "Jeff Harding"}, []string{"Lee Child"}},
		{map[string]string{"artist": "Stephen King", "album_artist": "Stephen King (The Dark Tower)"}, []string{"Stephen King"}},
		// album_artist is the author when artist is empty.
		{map[string]string{"album_artist": "Ann Author", "composer": "Ned Narrator"}, []string{"Ann Author"}},
		{map[string]string{"artist": " ", "album_artist": "Ann Author"}, []string{"Ann Author"}},
		// artist that adds the narrator to album_artist's author.
		{map[string]string{"artist": "Ann Author, Ned Narrator", "album_artist": "Ann Author"}, []string{"Ann Author"}},
		{map[string]string{"artist": "Homer, Ian McKellen", "album_artist": "Homer"}, []string{"Homer"}},
		// Co-authors in artist are kept when album_artist names someone else.
		{map[string]string{"artist": "Ann Author & Bea Writer", "album_artist": "Saga Name"}, []string{"Ann Author", "Bea Writer"}},
		// composer is the last resort, as before.
		{map[string]string{"composer": "Ann Author"}, []string{"Ann Author"}},
	}
	for _, tc := range cases {
		b := &parsedAudiobook{}
		b.populateFromTags(tc.tags)
		if !reflect.DeepEqual(b.Authors, tc.want) {
			t.Errorf("tags %v: authors %q, want %q", tc.tags, b.Authors, tc.want)
		}
	}
}

func TestPopulateFromTagsNarrators(t *testing.T) {
	cases := []struct {
		tags map[string]string
		want []string
	}{
		// performer names the author; composer names the narrator.
		{map[string]string{"album_artist": "Ann Author", "artist": "Ann Author, Ned Narrator", "performer": "Ann Author", "composer": "Ned Narrator"}, []string{"Ned Narrator"}},
		{map[string]string{"album_artist": "Ann Author", "performer": "Ann Author", "narratedby": "Ned Narrator"}, []string{"Ned Narrator"}},
		// A book read by its author keeps the author.
		{map[string]string{"album_artist": "Ann Author", "performer": "Ann Author", "composer": "Ann Author"}, []string{"Ann Author"}},
		{map[string]string{"album_artist": "Ann Author", "performer": "Ann Author"}, []string{"Ann Author"}},
		// A narrator tag with no usable name does not hide narratedby.
		{map[string]string{"album_artist": "Ann Author", "narrator": ";", "narratedby": "Ned Narrator", "composer": "Other Reader"}, []string{"Ned Narrator"}},
		// An explicit narrator tag wins even when it names the author.
		{map[string]string{"album_artist": "Ann Author", "narrator": "Ann Author", "composer": "Film Composer"}, []string{"Ann Author"}},
		// The first source naming someone else wins, as before.
		{map[string]string{"album_artist": "Ann Author", "narrator": "Ned Narrator", "composer": "Other Reader"}, []string{"Ned Narrator"}},
		{map[string]string{"album_artist": "Ann Author", "performer": "Ann Author & Ned Narrator", "composer": "Other Reader"}, []string{"Ann Author", "Ned Narrator"}},
	}
	for _, tc := range cases {
		b := &parsedAudiobook{}
		b.populateFromTags(tc.tags)
		if !reflect.DeepEqual(b.Narrators, tc.want) {
			t.Errorf("tags %v: narrators %q, want %q", tc.tags, b.Narrators, tc.want)
		}
	}
}

func TestAudiobookPartOrder(t *testing.T) {
	tag := func(disc, track string) map[string]string { return map[string]string{"disc": disc, "track": track} }
	cases := []struct {
		name        string
		tags        []map[string]string
		folderDiscs []int
		want        []int
		trust       bool
	}{
		{"tracks", []map[string]string{tag("", "4/4"), tag("", "1/4"), tag("", "3/4"), tag("", "2/4")}, nil, []int{1, 3, 2, 0}, true},
		{"discs then tracks", []map[string]string{tag("2/2", "1"), tag("1/2", "2"), tag("1/2", "1")}, nil, []int{2, 1, 0}, true},
		{"missing track", []map[string]string{tag("", "2"), tag("", "")}, nil, nil, false},
		{"duplicate track", []map[string]string{tag("", "1"), tag("", "1")}, nil, nil, false},
		{"disc on some parts", []map[string]string{tag("1", "1"), tag("", "2")}, nil, nil, false},
		// Disc folders CD1 and CD2, each numbering its tracks from 1, with
		// no disc tags: the folder number stands in for the disc.
		{"disc folder for a missing disc tag", []map[string]string{tag("", "1"), tag("", "2"), tag("", "1"), tag("", "2")}, []int{1, 1, 2, 2}, []int{0, 1, 2, 3}, true},
		{"disc tag wins over its folder", []map[string]string{tag("2", "1"), tag("1", "1")}, []int{1, 2}, []int{1, 0}, true},
		{"loose file beside disc folders without a disc tag", []map[string]string{tag("", "1"), tag("", "1")}, []int{0, 1}, nil, false},
	}
	for _, tc := range cases {
		got, ok := audiobookPartOrder(tc.tags, tc.folderDiscs)
		if ok != tc.trust || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: order %v ok %v, want %v %v", tc.name, got, ok, tc.want, tc.trust)
		}
	}
}

func TestPartTitlesNameParts(t *testing.T) {
	if !partTitlesNameParts([]string{"Chapter 01", "Chapter 02"}) {
		t.Error("differing part titles must name the parts")
	}
	if !partTitlesNameParts([]string{"Track 1"}) {
		t.Error("a track-style title must name the part")
	}
	if partTitlesNameParts([]string{"Book Title", "book title"}) {
		t.Error("one shared title names the book")
	}
	if partTitlesNameParts([]string{"Book Title", "", "Book Title"}) {
		t.Error("a part without a title tag must not make the titles differ")
	}
}

// mp4Box builds an MP4 box with a 32-bit size.
func mp4Box(boxType string, body ...[]byte) []byte {
	payload := slices.Concat(body...)
	size := 8 + len(payload)
	return slices.Concat([]byte{byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)}, []byte(boxType), payload)
}

func mp4Data(dataType byte, value []byte) []byte {
	return mp4Box("data", []byte{0, 0, 0, dataType, 0, 0, 0, 0}, value)
}

func TestReadMP4ItemListReadsMovementAtoms(t *testing.T) {
	ilst := mp4Box("ilst",
		mp4Box("\xa9nam", mp4Data(1, []byte("Title"))),
		mp4Box("\xa9mvn", mp4Data(1, []byte("Movement Saga"))),
		mp4Box("\xa9mvi", mp4Data(21, []byte{0, 4})),
		mp4Box("\xa9nrt", mp4Data(1, []byte("A Narrator"))),
	)
	meta := mp4Box("meta", []byte{0, 0, 0, 0}, mp4Box("hdlr", make([]byte, 25)), ilst)
	// mdat precedes moov, as in a file not optimized for streaming.
	file := slices.Concat(mp4Box("ftyp", []byte("M4B ")), mp4Box("mdat", make([]byte, 64)), mp4Box("moov", mp4Box("mvhd", make([]byte, 100)), mp4Box("udta", meta)))

	got := readMP4ItemList(strings.NewReader(string(file)), int64(len(file)))
	want := map[string]string{"mvnm": "Movement Saga", "mvin": "4", "narrator": "A Narrator"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readMP4ItemList = %v, want %v", got, want)
	}
	// A truncated file yields what it can without failing.
	if got := readMP4ItemList(strings.NewReader(string(file[:len(file)-10])), int64(len(file)-10)); got != nil {
		t.Fatalf("truncated moov = %v, want nil", got)
	}
}

// Files written into a library cannot grow the rejected-part list without bound.
func TestRejectedAudiobookPartsAreCapped(t *testing.T) {
	now := time.Now()
	t.Cleanup(func() {
		rejectedAudiobookParts.Lock()
		for path := range rejectedAudiobookParts.byPath {
			if strings.HasPrefix(path, "/capped/") {
				delete(rejectedAudiobookParts.byPath, path)
			}
		}
		rejectedAudiobookParts.Unlock()
	})
	for i := range maxRejectedAudiobookParts + 10 {
		rememberRejectedAudiobookPart(fmt.Sprintf("/capped/%d.mp3", i), 1, now)
	}
	rejectedAudiobookParts.Lock()
	n := len(rejectedAudiobookParts.byPath)
	rejectedAudiobookParts.Unlock()
	if n > maxRejectedAudiobookParts {
		t.Fatalf("remembered %d rejected parts, want at most %d", n, maxRejectedAudiobookParts)
	}
	last := fmt.Sprintf("/capped/%d.mp3", maxRejectedAudiobookParts+9)
	if !audiobookPartRejected(last, 1, now) {
		t.Fatal("the newest rejected part was not remembered")
	}
}

// A damaged part must not keep the rest of the book out of the library.
func TestParseAudiobookFolderSkipsRejectedPart(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
	dir := t.TempDir()
	for _, name := range []string{"part1.mp3", "part3.mp3"} {
		data, err := os.ReadFile(filepath.Join("testdata/audiobook_fixtures/multi_file", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bad := filepath.Join(dir, "part2.mp3")
	if err := os.WriteFile(bad, []byte("not audio at all, just bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := parseAudiobookFolder(t.Context(), "ffprobe", dir, nil, nil)
	if err != nil {
		t.Fatalf("parseAudiobookFolder: %v", err)
	}
	if len(got.Files) != 2 || !reflect.DeepEqual(got.UnreadableParts, []string{bad}) {
		t.Fatalf("files %d unreadable %v, want 2 files and %s", len(got.Files), got.UnreadableParts, bad)
	}
	info, err := os.Stat(bad)
	if err != nil {
		t.Fatal(err)
	}
	if !audiobookPartRejected(bad, info.Size(), normalizeFileModifiedAt(info.ModTime())) {
		t.Fatal("the rejected part is not remembered, so every scan would probe it again")
	}
	if err := os.Remove(filepath.Join(dir, "part1.mp3")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "part3.mp3")); err != nil {
		t.Fatal(err)
	}
	// The caller retires rows indexed for these files, so the error must
	// carry them.
	_, err = parseAudiobookFolder(t.Context(), "ffprobe", dir, nil, nil)
	var unreadable *noReadableAudiobookPartError
	if !errors.As(err, &unreadable) || !reflect.DeepEqual(unreadable.paths, []string{bad}) {
		t.Fatalf("book with no readable part: err = %v, want noReadableAudiobookPartError for %s", err, bad)
	}
}

// Progress reaches the admin on the clock while a book takes longer than the
// interval, and nothing is written after stop.
func TestReportProgressEveryWritesUntilStopped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var reports []int
		ctx := WithProgressReporter(t.Context(), func(u ProgressUpdate) {
			reports = append(reports, u.FilesProcessed)
		})
		var processed atomic.Int64
		processed.Store(1)
		stop := reportProgressEvery(ctx, audiobookScanProgressInterval, func() ProgressUpdate {
			return audiobookScanProgress(1, 10, int(processed.Load()), 0, 0)
		})

		time.Sleep(audiobookScanProgressInterval + time.Millisecond) //nolint:forbidigo // synctest fake clock: advances one tick instantly.
		synctest.Wait()
		processed.Store(2)
		time.Sleep(audiobookScanProgressInterval) //nolint:forbidigo // synctest fake clock: advances one more tick.
		synctest.Wait()
		if !slices.Equal(reports, []int{1, 2}) {
			t.Fatalf("reports = %v, want [1 2]", reports)
		}

		stop()
		processed.Store(3)
		time.Sleep(2 * audiobookScanProgressInterval) //nolint:forbidigo // synctest fake clock: ticks that would fire if stop leaked.
		synctest.Wait()
		if len(reports) != 2 {
			t.Fatalf("reports after stop = %v, want none added", reports)
		}
	})
}
