package scanner

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeSidecarWalkFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(filepath.Base(path)), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func sidecarNames(names []string) []string {
	names = slices.Clone(names)
	slices.Sort(names)
	return names
}

func TestVideoWalkListsSidecarsOfEveryDirectoryItReads(t *testing.T) {
	root := t.TempDir()
	show := filepath.Join(root, "Show (2020)")
	season := filepath.Join(show, "Season 01")
	writeSidecarWalkFile(t, filepath.Join(show, "tvshow.nfo"))
	writeSidecarWalkFile(t, filepath.Join(show, "Poster.JPG"))
	writeSidecarWalkFile(t, filepath.Join(season, "Show S01E01.mkv"))
	writeSidecarWalkFile(t, filepath.Join(season, "Show S01E01.nfo"))
	writeSidecarWalkFile(t, filepath.Join(season, "Show S01E01.srt"))
	writeSidecarWalkFile(t, filepath.Join(root, "Empty", "readme.txt"))

	files, failures, listing, err := collectLogicalFilePathsAndSidecars(t.Context(), []string{root}, "series", nil)
	if err != nil || len(failures) != 0 {
		t.Fatalf("walk: err=%v failures=%v", err, failures)
	}
	if len(files) != 1 {
		t.Fatalf("files = %v, want the one episode", files)
	}
	for dir, want := range map[string][]string{
		show:                         {"Poster.JPG", "tvshow.nfo"},
		season:                       {"Show S01E01.nfo"},
		root:                         {},
		filepath.Join(root, "Empty"): {},
	} {
		got, ok := listing[dir]
		if !ok {
			t.Fatalf("listing lacks %s", dir)
		}
		if names := sidecarNames(got); !slices.Equal(names, want) {
			t.Fatalf("listing[%s] = %v, want %v", dir, names, want)
		}
	}
	if !slices.Equal(sidecarNames(ReadSidecarListing([]string{show})[show]), sidecarNames(listing[show])) {
		t.Fatal("ReadSidecarListing lists a directory differently from the walk")
	}
}

func TestBookWalkListsNoSidecars(t *testing.T) {
	root := t.TempDir()
	writeSidecarWalkFile(t, filepath.Join(root, "Author", "Book.epub"))
	writeSidecarWalkFile(t, filepath.Join(root, "Author", "cover.jpg"))

	_, _, listing, err := collectLogicalFilePathsAndSidecars(t.Context(), []string{root}, "ebooks", nil)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if listing != nil {
		t.Fatalf("an ebook walk listed sidecars: %v", listing)
	}
}

func TestMergeScanResultCombinesSidecarListings(t *testing.T) {
	dst := &ScanResult{}
	mergeScanResult(dst, &ScanResult{Sidecars: SidecarListing{"/a": {"movie.nfo"}}})
	mergeScanResult(dst, &ScanResult{Sidecars: SidecarListing{"/b": nil}})
	if len(dst.Sidecars) != 2 || len(dst.Sidecars["/a"]) != 1 {
		t.Fatalf("merged listing = %v", dst.Sidecars)
	}
}
