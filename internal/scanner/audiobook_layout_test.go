package scanner

import (
	"errors"
	"os"
	"path/filepath"

	"slices"
	"testing"
)

// A part deleted after the walk listed its book is left out, as a fresh read
// would leave it out, instead of failing the book.
func TestAudiobookPartGoneSinceTheWalkIsLeftOut(t *testing.T) {
	for _, tc := range []struct {
		name, gone  string
		files, want []string
	}{
		{"in the book folder", "02.mp3", []string{"01.mp3", "02.mp3"}, []string{"01.mp3"}},
		{"in a disc folder", "CD2/01.mp3", []string{"CD1/01.mp3", "CD2/01.mp3"}, []string{"CD1/01.mp3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			book := filepath.Join(root, "Book")
			for _, name := range tc.files {
				writeTestFile(t, filepath.Join(book, name), "audio")
			}
			scans, err := collectAudiobookRootScans(t.Context(), 1, []string{root}, []string{root}, true, "")
			if err != nil {
				t.Fatal(err)
			}
			_, seen, _ := splitAudiobookReconcileRoots(scans)
			walked := newAudiobookInventory(seen, scans).book(book)
			if walked == nil || !walked.listed {
				t.Fatal("the walk kept no listing of the book")
			}
			if err := os.Remove(filepath.Join(book, tc.gone)); err != nil {
				t.Fatal(err)
			}
			var got []string
			err = walked.partsOrRelist(book, seen, func(parts []string) error {
				onDisk, _, err := statAudiobookDiskFiles(parts)
				got = nil
				for _, f := range onDisk {
					rel, _ := filepath.Rel(book, f.Path)
					got = append(got, rel)
				}
				return err
			})
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("parts = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// A symlinked part whose target is gone is no part; one that cannot be
// inspected fails the listing, so the book is not rebuilt without it.
func TestAudiobookBookPartsSymlinkErrors(t *testing.T) {
	book := filepath.Join(t.TempDir(), "Book")
	writeTestFile(t, filepath.Join(book, "01.mp3"), "audio")
	if err := os.Symlink(filepath.Join(book, "missing.mp3"), filepath.Join(book, "02.mp3")); err != nil {
		t.Fatal(err)
	}
	parts, err := audiobookBookParts(book, nil)
	if err != nil || len(parts) != 2 {
		// A nil inventory admits the dangling link's name; discovery would
		// not, but the listing itself must not fail on it.
		t.Fatalf("dangling link: parts %v, err %v; want the listing to succeed", parts, err)
	}
	if os.Getuid() == 0 {
		t.Skip("permission-based inspection failure cannot be simulated as root")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	writeTestFile(t, filepath.Join(locked, "03.mp3"), "audio")
	if err := os.Symlink(filepath.Join(locked, "03.mp3"), filepath.Join(book, "03.mp3")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if parts, err := audiobookBookParts(book, nil); err == nil || errors.Is(err, errFolderHasNoMedia) {
		t.Fatalf("unreadable link: parts %v, err %v; want an inspection error", parts, err)
	}
}
