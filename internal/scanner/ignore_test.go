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

func TestIgnoreMarkerFilesSkipEntireDirectory(t *testing.T) {
	t.Parallel()

	// .nomedia is a marker whatever it holds; .ignore is one only when it has
	// no valid pattern, as in Jellyfin.
	markers := []struct{ name, content string }{
		{".nomedia", ""},
		{".nomedia", "Hidden.mkv\n"},
		{".ignore", ""},
		{".ignore", "  \n\t\n"},
		{".ignore", "# only a comment\n"},
		{".ignore", "[\n"},
	}
	for _, marker := range markers {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, "Movie.mkv"), "test")
		writeTestFile(t, filepath.Join(root, "Ignored", "Hidden.mkv"), "test")
		writeTestFile(t, filepath.Join(root, "Ignored", marker.name), marker.content)
		writeTestFile(t, filepath.Join(root, "Kept", "Episode 01.mkv"), "test")

		files := collectTestFilePaths(t, root, "series")
		assertFilePaths(t, files, root, []string{"Movie.mkv", "Kept/Episode 01.mkv"})
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
	if libraryRootSkipped(root) {
		t.Fatal("a root .ignore with patterns must not skip the root")
	}
}

func TestIgnoreFileGitignoreSemantics(t *testing.T) {
	t.Parallel()

	cases := []struct {
		content string
		path    string
		isDir   bool
		want    bool
	}{
		{"incoming", "incoming", true, true},
		{"incoming", "Show/incoming", true, true},
		{"incoming", "Show/incoming.mkv", false, false},
		{"*.nfo", "Show/Season 01/a.nfo", false, true},
		{"/Season 01", "Season 01", true, true},
		{"/Season 01", "Show/Season 01", true, false},
		{"Show/Season 01", "Show/Season 01", true, true},
		{"Show/Season 01", "Other/Show/Season 01", true, false},
		{"Extras/", "Show/Extras", true, true},
		{"Extras/", "Show/Extras", false, false},
		{"**/sample.mkv", "sample.mkv", false, true},
		{"**/sample.mkv", "a/b/sample.mkv", false, true},
		{"Show/**", "Show", true, false},
		{"Show/**", "Show/a/b.mkv", false, true},
		{"a/**/b.mkv", "a/b.mkv", false, true},
		{"a/**/b.mkv", "a/x/y/b.mkv", false, true},
		{"a/**/b.mkv", "c/b.mkv", false, false},
		{"*.mkv\n!keep.mkv", "drop.mkv", false, true},
		{"*.mkv\n!keep.mkv", "keep.mkv", false, false},
		{"!keep.mkv\n*.mkv", "keep.mkv", false, true},
		{"\\#hash.mkv", "#hash.mkv", false, true},
		{"[\n*.nfo", "a.nfo", false, true},
		{"[!a]*.mkv", "b.mkv", false, true},
		{"[!a]*.mkv", "a.mkv", false, false},
		{"[^a]*.mkv", "a.mkv", false, false},
		{"Season [[:digit:]]", "Season 1", true, true},
		{"Season [[:digit:]]", "Season X", true, false},
		{"[[:upper:][:digit:]]*.nfo", "7.nfo", false, true},
		{"[[:upper:][:digit:]]*.nfo", "a.nfo", false, false},
		{"[[:punct:]]*.mkv", "#tmp.mkv", false, true},
		{"[]x].nfo", "].nfo", false, true},
		{"[a-].nfo", "-.nfo", false, true},
		{"[-a].nfo", "-.nfo", false, true},
		{"[[:bogus:]].nfo\n*.txt", "x.nfo", false, false},
	}
	for _, tc := range cases {
		rules := []ignoreRules{{basePath: "/lib", gitPatterns: parseGitIgnorePatterns(tc.content)}}
		if got := ignoreRulesMatch(rules, filepath.Join("/lib", tc.path), tc.isDir); got != tc.want {
			t.Errorf("pattern %q, path %q (dir=%v): ignored = %v, want %v", tc.content, tc.path, tc.isDir, got, tc.want)
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

func TestNestedIgnoreFileCanReincludeParentMatches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".ignore"), "*.mkv\n")
	writeTestFile(t, filepath.Join(root, "Show", ".ignore"), "!keep.mkv\n")
	writeTestFile(t, filepath.Join(root, "keep.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Show", "keep.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Show", "drop.mkv"), "test")

	files := collectTestFilePaths(t, root, "series")
	assertFilePaths(t, files, root, []string{"Show/keep.mkv"})
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

	cases := []struct {
		name  string
		setup func(root string)
		want  bool
	}{
		{"no ignore files", func(string) {}, false},
		{"empty .ignore", func(root string) { writeTestFile(t, filepath.Join(root, ".ignore"), "") }, true},
		{".ignore with patterns", func(root string) { writeTestFile(t, filepath.Join(root, ".ignore"), ".downloads\n") }, false},
		{".nomedia", func(root string) { writeTestFile(t, filepath.Join(root, ".nomedia"), "") }, true},
		{".ignore directory", func(root string) { writeTestFile(t, filepath.Join(root, ".ignore", "x"), "") }, false},
	}
	for _, tc := range cases {
		root := t.TempDir()
		tc.setup(root)
		if got := libraryRootSkipped(root); got != tc.want {
			t.Errorf("%s: libraryRootSkipped = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSiloIgnoreSkipsMatchedFilesAndDirs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Movie.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "sample.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Extras", "Deleted Scenes.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Sub", "notes.txt"), "test")
	writeTestFile(t, filepath.Join(root, "Sub", "clip.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Sub", "nested", "Episode 01.mkv"), "test")
	writeTestFile(t, filepath.Join(root, ".siloignore"), `# comments and blanks are ignored

sample.mkv
Extras
*.txt
`)

	files := collectTestFilePaths(t, root, "series")
	assertFilePaths(t, files, root, []string{"Movie.mkv", "Sub/clip.mkv", "Sub/nested/Episode 01.mkv"})
}

func TestSiloIgnoreParentPatternsCascadeIntoSubdirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Movie.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "junk", "ignore-me.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Series", "Season 1", "Episode 01.mkv"), "test")
	writeTestFile(t, filepath.Join(root, "Series", "Season 2", "Episode 01.mkv"), "test")
	// Nested file adds its own rules on top of the inherited ones.
	writeTestFile(t, filepath.Join(root, "Series", ".siloignore"), "Season 2")
	writeTestFile(t, filepath.Join(root, ".siloignore"), "junk/*")

	files := collectTestFilePaths(t, root, "series")
	assertFilePaths(t, files, root, []string{"Movie.mkv", "Series/Season 1/Episode 01.mkv"})
}

func TestDirectoryNamedLikeMarkerIsNotAMarker(t *testing.T) {
	t.Parallel()

	for _, name := range []string{".ignore", ".nomedia"} {
		root := t.TempDir()
		writeTestFile(t, filepath.Join(root, name, "Episode 01.mkv"), "test")
		writeTestFile(t, filepath.Join(root, "Movie.mkv"), "test")

		files := collectTestFilePaths(t, root, "series")
		assertFilePaths(t, files, root, []string{name + "/Episode 01.mkv", "Movie.mkv"})
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

func TestParseIgnorePatterns(t *testing.T) {
	t.Parallel()

	patterns := parseIgnorePatterns("# comment\n\n  sample.mkv  \nExtras\n\t*.txt\n")
	want := []string{"sample.mkv", "Extras", "*.txt"}
	if len(patterns) != len(want) {
		t.Fatalf("patterns = %v, want %v", patterns, want)
	}
	for i := range want {
		if patterns[i] != want[i] {
			t.Fatalf("patterns[%d] = %q, want %q", i, patterns[i], want[i])
		}
	}
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

	scans, err := collectAudiobookRootScans(t.Context(), 1, []string{root}, nil, true)
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

func TestPodcastShowAudioFilesHonorsShowSiloIgnore(t *testing.T) {
	t.Parallel()

	show := t.TempDir()
	writeTestFile(t, filepath.Join(show, "Episode 01.mp3"), "test")
	writeTestFile(t, filepath.Join(show, "Episode 02.mp3"), "test")
	writeTestFile(t, filepath.Join(show, ".siloignore"), "Episode 02.mp3")

	files, err := listPodcastShowAudioFiles(show, nil)
	if err != nil {
		t.Fatalf("listPodcastShowAudioFiles: %v", err)
	}
	want := []string{filepath.Join(show, "Episode 01.mp3")}
	if len(files) != len(want) || files[0] != want[0] {
		t.Fatalf("audio files = %v, want %v", files, want)
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
