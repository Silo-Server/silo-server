package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func collectTestFilePaths(t *testing.T, root string, libraryType string) []string {
	t.Helper()
	files, walkFailures, err := collectLogicalFilePaths(t.Context(), []string{root}, libraryType, nil)
	if err != nil {
		t.Fatalf("collect logical paths: %v", err)
	}
	if len(walkFailures) != 0 {
		t.Fatalf("walkFailures = %v, want none for a fully readable tree", walkFailures)
	}
	sort.Strings(files)
	return files
}

func assertFilePaths(t *testing.T, got []string, root string, wantRel []string) {
	t.Helper()
	want := make([]string, 0, len(wantRel))
	for _, rel := range wantRel {
		want = append(want, filepath.Join(root, rel))
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("files[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseGitIgnorePatternsDropsMalformedBrackets(t *testing.T) {
	t.Parallel()

	for _, content := range []string{"[[:bogus:]].nfo", "[abc", "[[:digit:].nfo", "foo\\"} {
		if patterns := parseGitIgnorePatterns(content); len(patterns) != 0 {
			t.Errorf("parseGitIgnorePatterns(%q) = %+v, want no valid pattern", content, patterns)
		}
	}
}

func TestIgnoreFileDirectoryPatternMatchesSymlinkedDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := t.TempDir()
	writeTestFile(t, filepath.Join(target, "Episode 01.mkv"), "test")
	if err := os.Symlink(target, filepath.Join(root, "Linked")); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}
	writeTestFile(t, filepath.Join(root, "Movie.mkv"), "test")
	writeTestFile(t, filepath.Join(root, ".ignore"), "Linked/\n")

	files := collectTestFilePaths(t, root, "series")
	assertFilePaths(t, files, root, []string{"Movie.mkv"})
}

func TestLibraryRootSkipped(t *testing.T) {
	t.Parallel()

	type rootCase struct {
		name    string
		setup   func(root string)
		want    bool
		wantErr bool
	}
	cases := []rootCase{
		{"no ignore files", func(string) {}, false, false},
		{"empty .ignore", func(root string) { writeTestFile(t, filepath.Join(root, ".ignore"), "") }, true, false},
		{".ignore with patterns", func(root string) { writeTestFile(t, filepath.Join(root, ".ignore"), ".downloads\n") }, false, false},
		{".nomedia", func(root string) { writeTestFile(t, filepath.Join(root, ".nomedia"), "") }, true, false},
		{".ignore directory", func(root string) { writeTestFile(t, filepath.Join(root, ".ignore", "x"), "") }, false, false},
	}
	if os.Geteuid() != 0 { // permission bits do not deny root
		cases = append(cases, rootCase{"unreadable .ignore", func(root string) {
			writeTestFile(t, filepath.Join(root, ".ignore"), "")
			if err := os.Chmod(filepath.Join(root, ".ignore"), 0o000); err != nil {
				t.Fatal(err)
			}
		}, false, true})
	}
	for _, tc := range cases {
		root := t.TempDir()
		tc.setup(root)
		got, err := libraryRootSkipped(root)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: libraryRootSkipped = %v, %v; want %v, error %v", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}

// Names are fspath-encoded when listed; a pattern holding the raw bytes of a
// non-UTF-8 name must match it, as it did before names were encoded.
func TestIgnoreFilesMatchRawNonUTF8Names(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "B\xe5rd"), 0o755); err != nil {
		t.Skipf("file system rejects non-UTF-8 names: %v", err)
	}
	writeTestFile(t, filepath.Join(root, "Movie.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Ylvis\xe5ker.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "B\xe5rd", "Episode 01.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Other", "S\xf8n.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Other", "Keep.mkv"), "test")
	writeTestFile(t, filepath.Join(root, ".siloignore"), "Ylvis\xe5ker.mkv\nB\xe5rd\n")
	writeTestFile(t, filepath.Join(root, "Other", ".ignore"), "S?n.mkv\n")

	files := collectTestFilePaths(t, root, "series")
	assertFilePaths(t, files, root, []string{"Movie.mkv", "Other/Keep.mkv"})
}

func TestDirectoryNamedLikeMarkerIsNotAMarker(t *testing.T) {
	t.Parallel()

	for _, name := range []string{".ignore", ".nomedia"} {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, name, "Episode 01.mkv"), "test")
		writeTestFile(t, filepath.Join(root, "Movie.mkv"), "test")

		// The directory is skipped as a hidden one, but it is no marker, so
		// its parent is still scanned.
		files := collectTestFilePaths(t, root, "series")
		assertFilePaths(t, files, root, []string{"Movie.mkv"})
	}
}

func TestSymlinkedMarkerAndPatternFilesAreNotHonored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "marker-target")
	if err := os.WriteFile(target, []byte("test"), 0o644); err != nil {
		t.Fatalf("write marker target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, ".ignore")); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}
	writeTestFile(t, filepath.Join(root, "Episode 01.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Sub", "Episode 02.mkv"), "test")
	patternTarget := filepath.Join(root, "patterns")
	if err := os.WriteFile(patternTarget, []byte("Sub\n"), 0o644); err != nil {
		t.Fatalf("write pattern target: %v", err)
	}
	if err := os.Symlink(patternTarget, filepath.Join(root, ".siloignore")); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	// Neither the symlinked marker nor the symlinked pattern file is honored:
	// every media file stays scannable.
	files := collectTestFilePaths(t, root, "series")
	assertFilePaths(t, files, root, []string{"Episode 01.mkv", "Sub/Episode 02.mkv"})
}

func TestSiloIgnoreDoesNotMatchAcrossPathSeparators(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Movie.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Sub", "Episode 01.mkv"), "test")
	writeTestFile(t, filepath.Join(root, ".siloignore"), "*Episode*")

	files := collectTestFilePaths(t, root, "series")
	assertFilePaths(t, files, root, []string{"Movie.mkv", "Sub/Episode 01.mkv"})
}

func TestAudiobookScanHonorsIgnoreFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Book One", "Chapter 01.mp3"), "test")
	writeTestFile(t, filepath.Join(root, "Book Two", "Chapter 01.mp3"), "test")
	writeTestFile(t, filepath.Join(root, "Book Two", ".ignore"), "")
	writeTestFile(t, filepath.Join(root, "sample", "Sample.mp3"), "test")
	writeTestFile(t, filepath.Join(root, "junk", "Ignore Me.mp3"), "test")
	writeTestFile(t, filepath.Join(root, ".siloignore"), "junk/*\nsample\n")

	scans, err := collectAudiobookRootScans(t.Context(), 1, []string{root}, []string{root}, true, "")
	if err != nil {
		t.Fatalf("collect audiobook root scans: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("scans len = %d, want 1", len(scans))
	}
	if scans[0].failed() {
		t.Fatalf("scan failed: %v", scans[0].rootErr)
	}
	want := map[string]bool{filepath.Join(root, "Book One", "Chapter 01.mp3"): true}
	if len(scans[0].seenPaths) != len(want) {
		t.Fatalf("seenPaths = %v, want %v", scans[0].seenPaths, want)
	}
	for path := range want {
		if !scans[0].seenPaths[path] {
			t.Fatalf("seenPaths missing %q", path)
		}
	}
}

func TestPodcastShowAudioFilesSkipsIgnoreMarkers(t *testing.T) {
	t.Parallel()

	show := t.TempDir()
	writeTestFile(t, filepath.Join(show, "Episode 01.mp3"), "test")
	writeTestFile(t, filepath.Join(show, ".nomedia"), "")

	_, err := listPodcastShowAudioFiles(show, nil)
	if err == nil || !errors.Is(err, errFolderHasNoMedia) {
		t.Fatalf("listPodcastShowAudioFiles err = %v, want errFolderHasNoMedia", err)
	}
	if !strings.Contains(err.Error(), show) {
		t.Fatalf("error should mention the show folder: %v", err)
	}
}

func TestPodcastShowAudioFilesAppliesInheritedRootPatterns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	show := filepath.Join(root, "Show")
	writeTestFile(t, filepath.Join(show, "Episode 01.mp3"), "test")
	writeTestFile(t, filepath.Join(show, "Episode 02.mp3"), "test")
	rules := []ignoreRules{{basePath: root, patterns: []string{"Show/Episode 02.mp3"}}}

	files, err := listPodcastShowAudioFiles(show, rules)
	if err != nil {
		t.Fatalf("listPodcastShowAudioFiles: %v", err)
	}
	want := []string{filepath.Join(show, "Episode 01.mp3")}
	if len(files) != len(want) || files[0] != want[0] {
		t.Fatalf("audio files = %v, want %v", files, want)
	}
}

// A library root nested inside another is walked by a full scan as an
// ordinary folder of the outer root, so the outer root's ignore files apply
// below it. Scoped walks, which subtree and Autoscan scans use, and the
// ebook full scan, which walks every configured root, must agree, or a
// folder the outer ignore file excludes is imported and then retired again.
func TestOuterRootIgnoreRulesApplyBelowANestedRoot(t *testing.T) {
	t.Parallel()

	lib := t.TempDir()
	nested := filepath.Join(lib, "nested")
	writeTestFile(t, filepath.Join(lib, ".siloignore"), "nested/Excluded*\n")
	writeTestFile(t, filepath.Join(nested, "Excluded Movie (2024)", "Excluded Movie (2024).mkv"), "test")
	writeTestFile(t, filepath.Join(nested, "Kept (2024)", "Kept (2024).mkv"), "test")
	writeTestFile(t, filepath.Join(nested, "Excluded Book.epub"), "test")
	writeTestFile(t, filepath.Join(nested, "Kept Book.epub"), "test")
	roots := []string{nested, lib}

	full, failures, err := collectLogicalFilePaths(t.Context(), compactScanRoots(roots), "movies", roots)
	if err != nil || len(failures) != 0 {
		t.Fatalf("full walk: %v, failures: %v", err, failures)
	}
	sort.Strings(full)
	assertFilePaths(t, full, lib, []string{"nested/Kept (2024)/Kept (2024).mkv"})

	for scope, want := range map[string][]string{
		nested: {"nested/Kept (2024)/Kept (2024).mkv"},
		filepath.Join(nested, "Excluded Movie (2024)"): nil,
	} {
		files, failures, err := collectLogicalFilePaths(t.Context(), []string{scope}, "movies", roots)
		if err != nil || len(failures) != 0 {
			t.Fatalf("scoped walk of %s: %v, failures: %v", scope, err, failures)
		}
		sort.Strings(files)
		assertFilePaths(t, files, lib, want)
	}

	scans, err := collectEbookRootScans(t.Context(), 1, roots, roots)
	if err != nil {
		t.Fatalf("ebook scan: %v", err)
	}
	var books []string
	for _, scan := range scans {
		books = append(books, scan.files...)
	}
	sort.Strings(books)
	assertFilePaths(t, books, lib, []string{"nested/Kept Book.epub"})

	// An explicit single-file scan (ScanFile) still ignores ignore files.
	excludedBook := filepath.Join(nested, "Excluded Book.epub")
	scans, err = collectEbookRootScans(t.Context(), 1, []string{excludedBook}, roots)
	if err != nil || len(scans) != 1 || len(scans[0].files) != 1 || scans[0].files[0] != excludedBook {
		t.Fatalf("explicit ebook file scan = %+v, %v; want %s", scans, err, excludedBook)
	}
}

// TestIgnoreFilePatternsAtLibraryRootSkipOnlyMatches reproduces a production
// layout: every library root holds an .ignore listing download and recycle
// folders. Only those folders may be skipped, never the root's media, for a
// full walk or a scoped walk beneath the root.
func TestIgnoreFilePatternsAtLibraryRootSkipOnlyMatches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".ignore"), ".recyclebin\n.downloads\n.inbound\nincoming\n")
	writeTestFile(t, filepath.Join(root, "Show", "Season 01", "Episode 01.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Show", "incoming", "Episode 02.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "incoming", "Other", "Episode 01.mkv"), "test")

	files := collectTestFilePaths(t, root, "series")
	assertFilePaths(t, files, root, []string{"Show/Season 01/Episode 01.mkv"})

	scope := filepath.Join(root, "Show", "Season 01")
	files, walkFailures, err := collectLogicalFilePaths(t.Context(), []string{scope}, "series", []string{root})
	if err != nil || len(walkFailures) != 0 {
		t.Fatalf("scoped walk: %v, failures: %v", err, walkFailures)
	}
	assertFilePaths(t, files, root, []string{"Show/Season 01/Episode 01.mkv"})
	if skipped, err := libraryRootSkipped(root); err != nil || skipped {
		t.Fatalf("a root .ignore with patterns must not skip the root: skipped=%v err=%v", skipped, err)
	}
}
