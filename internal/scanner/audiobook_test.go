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

// An admin's locked fields survive a changed-book rescan, and the fields left
// unlocked follow the new tags: a retagged date moves the year and the release
// date together. The item's overview starts empty, as after an
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
	tagged := "2001-02-03"
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
		wantRelease  string
		wantGenres   []string
		wantStudios  []string
		wantOverview string
	}{
		{"nothing locked", nil, false, "Tagged Title", 2001, tagged, []string{"Admin Genre", "Tagged Genre"}, []string{"Admin Studio", "Tagged Press"}, "Tagged overview"},
		{"title locked", []int{lockedFieldName}, false, "Admin Title", 2001, tagged, []string{"Admin Genre", "Tagged Genre"}, []string{"Admin Studio", "Tagged Press"}, "Tagged overview"},
		{"release dates locked", []int{lockedFieldReleaseDates}, false, "Tagged Title", 2020, edited, []string{"Admin Genre", "Tagged Genre"}, []string{"Admin Studio", "Tagged Press"}, "Tagged overview"},
		{"genres and studios locked", []int{lockedFieldGenres, lockedFieldStudios}, false, "Tagged Title", 2001, tagged, []string{"Admin Genre"}, []string{"Admin Studio"}, "Tagged overview"},
		{"overview locked", []int{lockedFieldOverview}, false, "Tagged Title", 2001, tagged, []string{"Admin Genre", "Tagged Genre"}, []string{"Admin Studio", "Tagged Press"}, ""},
		// A provider match owns the year, release date, genres and studios;
		// tags keep the title and only fill what is empty.
		{"provider matched", nil, true, "Tagged Title", 2020, edited, []string{"Admin Genre"}, []string{"Admin Studio"}, "Tagged overview"},
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
			if item.ReleaseDate == nil || *item.ReleaseDate != tc.wantRelease {
				t.Errorf("ReleaseDate = %v, want %q", item.ReleaseDate, tc.wantRelease)
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
