package scanner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCollectAudiobookRootScansSymlinks(t *testing.T) {
	for _, kind := range []string{"author", "relative author", "root", "audio file", "directory with audio extension", "aliases", "cycle", "multipart book", "loose root audio"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "library")
			target := filepath.Join(base, "storage", "Author")
			book := filepath.Join(target, "Book")
			mkdir := func(path string) {
				t.Helper()
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			link := func(target, path string) {
				t.Helper()
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			mkdir(root)
			mkdir(book)
			if err := os.WriteFile(filepath.Join(book, "chapter.mp3"), []byte("audio"), 0o644); err != nil {
				t.Fatal(err)
			}
			wantCandidates := []string{filepath.Join(root, "Author", "Book")}
			wantSeen := map[string]bool{filepath.Join(root, "Author", "Book", "chapter.mp3"): true}
			switch kind {
			case "author":
				link(target, filepath.Join(root, "Author"))
			case "relative author":
				link(filepath.Join("..", "storage", "Author"), filepath.Join(root, "Author"))
			case "root":
				root = filepath.Join(base, "linked-library")
				link(filepath.Dir(target), root)
				wantCandidates = []string{filepath.Join(root, "Author", "Book")}
				wantSeen = map[string]bool{filepath.Join(root, "Author", "Book", "chapter.mp3"): true}
			case "audio file":
				logicalBook := filepath.Join(root, "Book")
				mkdir(logicalBook)
				link(filepath.Join(book, "chapter.mp3"), filepath.Join(logicalBook, "chapter.mp3"))
				wantCandidates = []string{logicalBook}
				wantSeen = map[string]bool{filepath.Join(logicalBook, "chapter.mp3"): true}
			case "directory with audio extension":
				link(target, filepath.Join(root, "Author.mp3"))
				wantCandidates = []string{filepath.Join(root, "Author.mp3", "Book")}
				wantSeen = map[string]bool{filepath.Join(root, "Author.mp3", "Book", "chapter.mp3"): true}
			case "aliases":
				link(target, filepath.Join(root, "Author"))
				link(target, filepath.Join(root, "Other Author"))
				wantCandidates = append(wantCandidates, filepath.Join(root, "Other Author", "Book"))
				wantSeen[filepath.Join(root, "Other Author", "Book", "chapter.mp3")] = true
			case "cycle":
				link(target, filepath.Join(root, "Author"))
				link(root, filepath.Join(target, "Back to library"))
			case "multipart book":
				link(target, filepath.Join(root, "Author"))
				part := filepath.Join(book, "Part Two")
				mkdir(part)
				if err := os.WriteFile(filepath.Join(part, "chapter.mp3"), []byte("audio"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "loose root audio":
				link(target, filepath.Join(root, "Author"))
				if err := os.WriteFile(filepath.Join(root, "loose.m4b"), []byte("audio"), 0o644); err != nil {
					t.Fatal(err)
				}
				wantCandidates = append([]string{root}, wantCandidates...)
				wantSeen[filepath.Join(root, "loose.m4b")] = true
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			scans, err := collectAudiobookRootScans(ctx, 11, []string{root}, nil, true)
			if err != nil {
				t.Fatalf("collectAudiobookRootScans: %v", err)
			}
			if len(scans) != 1 {
				t.Fatalf("got %d root scans, want 1", len(scans))
			}
			scan := scans[0]
			if scan.failed() {
				t.Fatalf("scan failed: rootErr=%v walkFailures=%v", scan.rootErr, scan.walkFailures)
			}
			if !reflect.DeepEqual(scan.candidates, wantCandidates) {
				t.Errorf("candidates = %v, want %v", scan.candidates, wantCandidates)
			}
			if !reflect.DeepEqual(scan.seenPaths, wantSeen) {
				t.Errorf("seenPaths = %v, want %v", scan.seenPaths, wantSeen)
			}
		})
	}
}

func TestCollectAudiobookRootScansBrokenSymlinkProtectsReconciliation(t *testing.T) {
	for _, name := range []string{"Missing Author", "missing.mp3", "cover.jpg", "notes.txt"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "available.mp3"), []byte("audio"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "missing-target"), filepath.Join(root, name)); err != nil {
				t.Fatal(err)
			}
			scans, err := collectAudiobookRootScans(t.Context(), 11, []string{root}, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(scans) != 1 || !scans[0].failed() {
				t.Fatalf("broken symlink must mark root incomplete: %+v", scans)
			}
			if !reflect.DeepEqual(scans[0].candidates, []string{root}) {
				t.Errorf("readable audio should remain a candidate: %v", scans[0].candidates)
			}
			if !reflect.DeepEqual(scans[0].seenPaths, map[string]bool{filepath.Join(root, "available.mp3"): true}) {
				t.Errorf("seen paths should contain only readable audio: %v", scans[0].seenPaths)
			}
			roots, seen, protected := splitAudiobookReconcileRoots(scans)
			if !reflect.DeepEqual(roots, []string{root}) || !reflect.DeepEqual(seen, scans[0].seenPaths) {
				t.Fatalf("healthy paths must remain eligible for reconciliation: roots=%v seen=%v", roots, seen)
			}
			failedPath := filepath.Join(root, name)
			if !reflect.DeepEqual(protected, []string{failedPath}) {
				t.Fatalf("protected paths = %v, want only %s", protected, failedPath)
			}
			for _, path := range []string{failedPath, filepath.Join(failedPath, "Book", "chapter.mp3")} {
				if !pathWithinAnyRoot(path, protected) {
					t.Errorf("failed path or descendant is unprotected: %s", path)
				}
			}
			for _, path := range []string{filepath.Join(root, "available.mp3"), filepath.Join(root, "removed.mp3"), failedPath + "-other"} {
				if pathWithinAnyRoot(path, protected) {
					t.Errorf("unrelated path must remain eligible for reconciliation: %s", path)
				}
			}
		})
	}
}
