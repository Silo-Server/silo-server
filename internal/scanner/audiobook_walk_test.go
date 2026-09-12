package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestCollectAudiobookRootScansParallelWalk pins the traversal semantics of
// the level-parallel walker against the previous serial WalkDir behavior:
// a directory containing audio files is a candidate and is NOT descended
// into; audio-free interior directories are traversed; non-audio files are
// ignored; unreadable directories count as walk failures.
func TestCollectAudiobookRootScansParallelWalk(t *testing.T) {
	root := t.TempDir()
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}
	touch := func(dir, name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		return p
	}

	// Author A: two books, one with two audio files.
	bookA1 := mk("Author A", "Book One (2001)")
	a1f1 := touch(bookA1, "part1.mp3")
	a1f2 := touch(bookA1, "part2.mp3")
	touch(bookA1, "cover.jpg") // non-audio, ignored
	bookA2 := mk("Author A", "Book Two (2002)")
	a2f1 := touch(bookA2, "book.m4b")
	// A book with a nested extras dir: candidate at the book level, no descent.
	nested := mk("Author A", "Book Three (2003)", "extras")
	touch(nested, "bonus.mp3") // must NOT be seen: parent is the candidate
	bookA3 := filepath.Join(root, "Author A", "Book Three (2003)")
	a3f1 := touch(bookA3, "main.mp3")
	// Author B: deeper nesting before the book level.
	bookB1 := mk("Author B", "Some Series", "Book Four (2004)")
	b1f1 := touch(bookB1, "four.flac")
	// Empty and audio-free directories: traversed, never candidates.
	mk("Author C", "Empty Book")
	pdfDir := mk("Author C", "PDF Only")
	touch(pdfDir, "book.pdf")

	scans, err := collectAudiobookRootScans(context.Background(), 1, []string{root})
	if err != nil {
		t.Fatalf("collectAudiobookRootScans: %v", err)
	}
	if len(scans) != 1 {
		t.Fatalf("scans = %d, want 1", len(scans))
	}
	scan := scans[0]
	if scan.failed() {
		t.Fatalf("scan failed: rootErr=%v walkFailures=%d", scan.rootErr, scan.walkFailures)
	}

	gotCandidates := append([]string(nil), scan.candidates...)
	sort.Strings(gotCandidates)
	wantCandidates := []string{bookA1, bookA2, bookA3, bookB1}
	sort.Strings(wantCandidates)
	if len(gotCandidates) != len(wantCandidates) {
		t.Fatalf("candidates = %v, want %v", gotCandidates, wantCandidates)
	}
	for i := range wantCandidates {
		if gotCandidates[i] != wantCandidates[i] {
			t.Fatalf("candidates = %v, want %v", gotCandidates, wantCandidates)
		}
	}

	wantSeen := []string{a1f1, a1f2, a2f1, a3f1, b1f1}
	if len(scan.seenPaths) != len(wantSeen) {
		t.Fatalf("seenPaths = %v, want %v", scan.seenPaths, wantSeen)
	}
	for _, p := range wantSeen {
		if !scan.seenPaths[p] {
			t.Fatalf("seenPaths missing %s (have %v)", p, scan.seenPaths)
		}
	}
}

func TestCollectAudiobookRootScansCountsUnreadableDirs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based unreadable-dir test is meaningless as root")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "Author", "Locked Book")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	scans, err := collectAudiobookRootScans(context.Background(), 1, []string{root})
	if err != nil {
		t.Fatalf("collectAudiobookRootScans: %v", err)
	}
	if len(scans) != 1 || scans[0].walkFailures == 0 {
		t.Fatalf("walkFailures = %d, want > 0 for unreadable dir", scans[0].walkFailures)
	}
}
