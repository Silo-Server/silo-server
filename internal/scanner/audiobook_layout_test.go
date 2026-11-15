package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

var audiobookGroupingCases = []struct {
	name     string
	files    []string
	ignore   string   // library root .siloignore content
	locked   string   // folder made unreadable after the files are written; skipped as root
	dangling []string // symlinks to a missing target, which fail to resolve even as root
	nested   []string // configured roots inside the library root
	// books maps each expected book root (relative to the library root)
	// to its parts in order.
	books map[string][]string
}{
	{
		name:  "disc folders form one book, own files first then by disc number",
		files: []string{"A/Book/Disc 10/01.mp3", "A/Book/Disc 2/02.mp3", "A/Book/Disc 2/01.mp3", "A/Book/CD1/01.mp3", "A/Book/intro.mp3"},
		books: map[string][]string{"A/Book": {"A/Book/intro.mp3", "A/Book/CD1/01.mp3", "A/Book/Disc 2/01.mp3", "A/Book/Disc 2/02.mp3", "A/Book/Disc 10/01.mp3"}},
	},
	{
		name:  "a folder's own audio is one book, and a book folder below it is found",
		files: []string{"A/Loose One.m4b", "A/Loose Two.m4b", "A/Proper Book/book.m4b"},
		books: map[string][]string{
			"A":             {"A/Loose One.m4b", "A/Loose Two.m4b"},
			"A/Proper Book": {"A/Proper Book/book.m4b"},
		},
	},
	{
		name:  "an audio subfolder of a book is a book of its own and leaves the book whole",
		files: []string{"Book/01.mp3", "Book/02.mp3", "Book/Author Interview/talk.mp3"},
		books: map[string][]string{
			"Book":                  {"Book/01.mp3", "Book/02.mp3"},
			"Book/Author Interview": {"Book/Author Interview/talk.mp3"},
		},
	},
	{
		name:  "loose files in the library root are books of their own",
		files: []string{"one.m4b", "two.mp3"},
		books: map[string][]string{"one.m4b": {"one.m4b"}, "two.mp3": {"two.mp3"}},
	},
	{
		name:  "a folder holding only audio stays one multi-part book",
		files: []string{"A/Book/ch2.mp3", "A/Book/ch10.mp3", "A/Book/ch1.mp3"},
		books: map[string][]string{"A/Book": {"A/Book/ch1.mp3", "A/Book/ch2.mp3", "A/Book/ch10.mp3"}},
	},
	{
		name:  "a bonus folder below a book is not walked",
		files: []string{"A/Book/book.m4b", "A/Book/Bonus/interview.mp3"},
		books: map[string][]string{"A/Book": {"A/Book/book.m4b"}},
	},
	{
		name:  "a disc-named folder in the library root is a book, not a disc of the root",
		files: []string{"CD1/01.mp3", "loose.m4b"},
		books: map[string][]string{"CD1": {"CD1/01.mp3"}, "loose.m4b": {"loose.m4b"}},
	},
	{
		name:  "disc 0 is not a disc",
		files: []string{"A/Book/Disc 0/p.mp3", "A/Book/Disc 1/01.mp3"},
		books: map[string][]string{"A/Book": {"A/Book/Disc 1/01.mp3"}, "A/Book/Disc 0": {"A/Book/Disc 0/p.mp3"}},
	},
	{
		name:  "a disc-named folder without audio of its own is walked as usual",
		files: []string{"Series/Disc 1/Book A/a.m4b", "Series/Disc 1/Book B/b.m4b"},
		books: map[string][]string{"Series/Disc 1/Book A": {"Series/Disc 1/Book A/a.m4b"}, "Series/Disc 1/Book B": {"Series/Disc 1/Book B/b.m4b"}},
	},
	{
		name:   "an ignored, unreadable disc folder does not fail its book",
		files:  []string{"A/Book/CD1/01.mp3", "A/Book/CD2/01.mp3"},
		ignore: "A/Book/CD2\n",
		locked: "A/Book/CD2",
		books:  map[string][]string{"A/Book": {"A/Book/CD1/01.mp3"}},
	},
	{
		name:   "an unreadable disc folder holds its book back",
		files:  []string{"A/Book/CD1/01.mp3", "A/Book/CD2/01.mp3", "A/Other/o.m4b"},
		locked: "A/Book/CD2",
		books:  map[string][]string{"A/Other": {"A/Other/o.m4b"}},
	},
	{
		name:   "extras stay unwalked while every disc of their book is unreadable",
		files:  []string{"A/Book/CD1/01.mp3", "A/Book/Bonus/b.mp3"},
		locked: "A/Book/CD1",
		books:  map[string][]string{},
	},
	{
		name:   "an unreadable subfolder does not hold its parent's book",
		files:  []string{"A/loose.mp3", "A/Other/o.m4b"},
		locked: "A/Other",
		books:  map[string][]string{"A": {"A/loose.mp3"}},
	},
	{
		name:   "a held folder adds no book found inside one of its readable discs",
		files:  []string{"A/Book/CD1/01.mp3", "A/Book/CD1/Inner Book/i.m4b", "A/Book/CD2/01.mp3"},
		locked: "A/Book/CD2",
		books:  map[string][]string{},
	},
	{
		name:     "an entry that does not resolve holds its folder",
		files:    []string{"A/Loose One.m4b", "A/Loose Two.m4b", "A/Proper Book/book.m4b", "B/b.m4b"},
		dangling: []string{"A/Gone"},
		books:    map[string][]string{"B": {"B/b.m4b"}},
	},
	{
		name:   "a configured root is not a disc of its parent",
		files:  []string{"A/Book/CD1/01.mp3", "A/Book/CD2/01.mp3"},
		nested: []string{"A/Book/CD1"},
		books:  map[string][]string{"A/Book": {"A/Book/CD2/01.mp3"}, "A/Book/CD1/01.mp3": {"A/Book/CD1/01.mp3"}},
	},
	{
		name:  "a book below a disc folder is still found",
		files: []string{"A/Book/CD1/01.mp3", "A/Book/CD1/Other Book/o.m4b", "A/Book/CD1/Bonus/b.mp3"},
		books: map[string][]string{"A/Book": {"A/Book/CD1/01.mp3"}, "A/Book/CD1/Other Book": {"A/Book/CD1/Other Book/o.m4b"}},
	},
	{
		name:  "an extras-named folder in the library root is walked",
		files: []string{"loose.m4b", "Extras/x.mp3"},
		books: map[string][]string{"loose.m4b": {"loose.m4b"}, "Extras": {"Extras/x.mp3"}},
	},
}

func TestAudiobookFolderGrouping(t *testing.T) {
	for _, tc := range audiobookGroupingCases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, file := range tc.files {
				path := filepath.Join(root, file)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.ignore != "" {
				writeTestFile(t, filepath.Join(root, ".siloignore"), tc.ignore)
			}
			for _, link := range tc.dangling {
				if err := os.Symlink(filepath.Join(root, "missing-target"), filepath.Join(root, link)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.locked != "" {
				if os.Getuid() == 0 {
					t.Skip("permission-based read failure cannot be simulated as root")
				}
				locked := filepath.Join(root, tc.locked)
				if err := os.Chmod(locked, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
			}
			libraryRoots := []string{root}
			for _, nested := range tc.nested {
				libraryRoots = append(libraryRoots, filepath.Join(root, nested))
			}
			scans, err := collectAudiobookRootScans(t.Context(), 1, []string{root}, libraryRoots, true, "")
			if err != nil {
				t.Fatal(err)
			}
			_, seen, _ := splitAudiobookReconcileRoots(scans)
			rel := func(path string) string {
				r, err := filepath.Rel(root, path)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			got := make(map[string][]string)
			inventory := newAudiobookInventory(seen, scans)
			for _, aliases := range groupAudiobookCandidates(scans) {
				candidate := aliases[0]
				parts, err := audiobookBookParts(candidate, seen)
				if err != nil {
					t.Fatalf("parts of %s: %v", candidate, err)
				}
				// The parts listed from the walk's reads match a fresh read.
				if walked, err := inventory.book(candidate).parts(candidate, seen); err != nil || !slices.Equal(walked, parts) {
					t.Errorf("parts of %s from the walk = %v, %v; from disk %v", rel(candidate), walked, err, parts)
				}
				for _, part := range parts {
					got[rel(candidate)] = append(got[rel(candidate)], rel(part))
				}
			}
			if !reflect.DeepEqual(got, tc.books) {
				t.Errorf("books = %v, want %v", got, tc.books)
			}
		})
	}
}

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

// Each case puts audio files under a library root L (and any nested configured
// roots) and checks the folder a scoped scan of scope walks.
func TestAudiobookScopeRoot(t *testing.T) {
	for _, tc := range []struct {
		name, scope, want string
		nested            []string
		files             []string
	}{
		{name: "a file widens to its folder", files: []string{"A/Book/b.m4b"}, scope: "A/Book/b.m4b", want: "A/Book"},
		{name: "a file in a disc widens to the book", files: []string{"A/Book/CD 2/01.mp3"}, scope: "A/Book/CD 2/01.mp3", want: "A/Book"},
		{name: "a scope inside a disc widens to the book", files: []string{"A/Book/CD 2/01.mp3", "A/Book/CD 2/Sub/s.mp3"}, scope: "A/Book/CD 2/Sub", want: "A/Book"},
		{name: "a scope inside Extras widens to the book", files: []string{"A/Book/CD1/01.mp3", "A/Book/Bonus/Interviews/i.mp3"}, scope: "A/Book/Bonus/Interviews/i.mp3", want: "A/Book"},
		{name: "a scope below a folder's own audio stays put", files: []string{"A/a.mp3", "A/Series/New Book/n.mp3"}, scope: "A/Series/New Book", want: "A/Series/New Book"},
		{name: "a disc folder in the library root stays put", files: []string{"CD1/01.mp3"}, scope: "CD1", want: "CD1"},
		{name: "an extras folder in the library root stays put", files: []string{"Extras/e.mp3", "root.mp3"}, scope: "Extras", want: "Extras"},
		{name: "a vanished scope stays put", scope: "Gone Book", want: "Gone Book"},
		{name: "a disc-named nested root stays put", nested: []string{"A/Book/CD1"}, files: []string{"A/Book/CD1/01.mp3", "A/Book/CD2/01.mp3"}, scope: "A/Book/CD1/01.mp3", want: "A/Book/CD1"},
		{name: "a nested root stays put above loose audio", nested: []string{"A/Collection"}, files: []string{"A/a.mp3", "A/Collection/Book/b.m4b"}, scope: "A/Collection", want: "A/Collection"},
		{name: "a scope below a nested root does not cross it", nested: []string{"A/Collection"}, files: []string{"A/a.mp3", "A/Collection/Book/b.m4b"}, scope: "A/Collection/Book", want: "A/Collection/Book"},
		{name: "an extras-named nested root stays put", nested: []string{"A/Book/Extras"}, files: []string{"A/Book/b.m4b", "A/Book/Extras/e.mp3"}, scope: "A/Book/Extras", want: "A/Book/Extras"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, file := range tc.files {
				writeTestFile(t, filepath.Join(root, file), "audio")
			}
			libraryRoots := []string{root}
			for _, nested := range tc.nested {
				libraryRoots = append(libraryRoots, filepath.Join(root, nested))
			}
			got, _ := audiobookScopeRoot(filepath.Join(root, tc.scope), libraryRoots)
			if want := filepath.Join(root, tc.want); got != want {
				t.Errorf("audiobookScopeRoot(%s) = %s, want %s", tc.scope, got, want)
			}
		})
	}
}

func TestAudiobookLooseFileTitleComesFromItsFile(t *testing.T) {
	file := filepath.Join("lib", "Author", "Loose One.m4b")
	if got := deriveAudiobookTitleFromFilesystem(file, []string{file}); got != "Loose One" {
		t.Errorf("title = %q, want %q", got, "Loose One")
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
