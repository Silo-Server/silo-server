package nfo

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

func TestFindNFO_FilePathFallsBackToDirectoryLevelSidecar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Blade Runner (1982).mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(file) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.nfo"), []byte("<movie><title>Dir</title></movie>"), 0o644); err != nil {
		t.Fatalf("WriteFile(movie.nfo) error = %v", err)
	}

	want := filepath.Join(dir, "movie.nfo")
	if got, _ := findNFO([]string{filePath}, ""); got != want {
		t.Fatalf("findNFO(file path) = %q, want %q", got, want)
	}
}

func TestFindNFO_DirectoryPathUsesDirectoryLevelSidecar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	want := filepath.Join(dir, "movie.nfo")
	if err := os.WriteFile(want, []byte("<movie><title>Dir</title></movie>"), 0o644); err != nil {
		t.Fatalf("WriteFile(movie.nfo) error = %v", err)
	}

	if got, _ := findNFO([]string{dir}, ""); got != want {
		t.Fatalf("findNFO(directory path) = %q, want %q", got, want)
	}
}

func TestFindNFO_FilePathUsesBasenameMatchedNFO(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Blade Runner (1982).mkv")
	want := filepath.Join(dir, "Blade Runner (1982).nfo")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(file) error = %v", err)
	}
	if err := os.WriteFile(want, []byte("<movie><title>File</title></movie>"), 0o644); err != nil {
		t.Fatalf("WriteFile(file.nfo) error = %v", err)
	}

	if got, _ := findNFO([]string{filePath}, ""); got != want {
		t.Fatalf("findNFO(file path) = %q, want %q", got, want)
	}
}

func TestFindNFO_ExtensionlessFilePathUsesDirectoryLevelSidecar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Blade Runner (1982)")
	want := filepath.Join(dir, "movie.nfo")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(file) error = %v", err)
	}
	if err := os.WriteFile(want, []byte("<movie><title>Dir</title></movie>"), 0o644); err != nil {
		t.Fatalf("WriteFile(movie.nfo) error = %v", err)
	}

	if got, _ := findNFO([]string{filePath}, ""); got != want {
		t.Fatalf("findNFO(extensionless file path) = %q, want %q", got, want)
	}
}

func TestFindNFO_ExtensionlessFilePathUsesBasenameMatchedNFO(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Blade Runner (1982)")
	want := filepath.Join(dir, "Blade Runner (1982).nfo")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(file) error = %v", err)
	}
	if err := os.WriteFile(want, []byte("<movie><title>File</title></movie>"), 0o644); err != nil {
		t.Fatalf("WriteFile(file.nfo) error = %v", err)
	}

	if got, _ := findNFO([]string{filePath}, ""); got != want {
		t.Fatalf("findNFO(extensionless file path) = %q, want %q", got, want)
	}
}

func TestNFOCandidatesForMissingDottedDirectoryPathIncludeDirectoryLevelSidecars(t *testing.T) {
	t.Parallel()

	path := filepath.Join("/media/shows", "Mr. Robot")
	got := nfoCandidatesForPath(path)

	wantMovie := filepath.Join(path, "movie.nfo")
	wantTV := filepath.Join(path, "tvshow.nfo")
	if !slices.Contains(got, wantMovie) {
		t.Fatalf("nfoCandidatesForPath(%q) missing %q in %#v", path, wantMovie, got)
	}
	if !slices.Contains(got, wantTV) {
		t.Fatalf("nfoCandidatesForPath(%q) missing %q in %#v", path, wantTV, got)
	}
}

// A tvshow.nfo sitting next to a movie file must not inject series data into
// a movie item at top priority: GetMetadata carries the same ContentType
// guard Search has.
func TestGetMetadata_TVShowNFOBesideMovieFileDoesNotInject(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Home Movie (2021).mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(file) error = %v", err)
	}
	nfoXML := `<tvshow><title>Some Series</title><uniqueid type="tvdb">123</uniqueid></tvshow>`
	if err := os.WriteFile(filepath.Join(dir, "tvshow.nfo"), []byte(nfoXML), 0o644); err != nil {
		t.Fatalf("WriteFile(tvshow.nfo) error = %v", err)
	}

	p := NewProvider()
	result, err := p.GetMetadata(context.Background(), metadata.MetadataRequest{
		ContentType: "movie",
		FilePath:    filePath,
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if result.HasMetadata {
		t.Fatalf("GetMetadata() = %#v, want no metadata (series NFO must not inject into a movie)", result)
	}
}

// A stray movie.nfo in a series root must not shadow tvshow.nfo: findNFO
// falls through to the next candidate on type mismatch.
func TestFindNFO_StrayMovieNFOInSeriesRootFallsThroughToTVShow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "movie.nfo"), []byte(`<movie><title>Stray</title></movie>`), 0o644); err != nil {
		t.Fatalf("WriteFile(movie.nfo) error = %v", err)
	}
	want := filepath.Join(dir, "tvshow.nfo")
	if err := os.WriteFile(want, []byte(`<tvshow><title>The Show</title></tvshow>`), 0o644); err != nil {
		t.Fatalf("WriteFile(tvshow.nfo) error = %v", err)
	}

	got, parsed := findNFO([]string{dir}, "series")
	if got != want {
		t.Fatalf("findNFO(series root) = %q, want %q", got, want)
	}
	if parsed == nil || parsed.Title != "The Show" {
		t.Fatalf("parsed = %#v, want tvshow content", parsed)
	}
}

// A candidate that exists but fails to parse falls through to the next
// candidate instead of masking a valid sidecar.
func TestFindNFO_ParseFailureFallsThroughToNextCandidate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Blade Runner (1982).mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(file) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.nfo"), []byte("not xml at all"), 0o644); err != nil {
		t.Fatalf("WriteFile(movie.nfo) error = %v", err)
	}
	want := filepath.Join(dir, "Blade Runner (1982).nfo")
	if err := os.WriteFile(want, []byte(`<movie><title>Blade Runner</title></movie>`), 0o644); err != nil {
		t.Fatalf("WriteFile(basename nfo) error = %v", err)
	}

	got, parsed := findNFO([]string{filePath}, "movie")
	if got != want {
		t.Fatalf("findNFO(parse-failure fallthrough) = %q, want %q", got, want)
	}
	if parsed == nil || parsed.Title != "Blade Runner" {
		t.Fatalf("parsed = %#v, want valid movie content", parsed)
	}
}

// GetMetadata surfaces the full Phase-B field set from a curated movie NFO.
func TestGetMetadata_FullMovieFieldSet(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Blade Runner (1982).mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(file) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.nfo"), []byte(fullMovieNFO), 0o644); err != nil {
		t.Fatalf("WriteFile(movie.nfo) error = %v", err)
	}

	p := NewProvider()
	result, err := p.GetMetadata(context.Background(), metadata.MetadataRequest{
		ContentType: "movie",
		FilePath:    filePath,
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if !result.HasMetadata {
		t.Fatal("HasMetadata = false, want true")
	}
	if result.OriginalTitle != "Blade Runner: The Original" {
		t.Errorf("OriginalTitle = %q", result.OriginalTitle)
	}
	if result.Tagline != "Man has made his match" {
		t.Errorf("Tagline = %q", result.Tagline)
	}
	if result.Runtime != 117 {
		t.Errorf("Runtime = %d", result.Runtime)
	}
	if result.ReleaseDate != "1982-06-25" {
		t.Errorf("ReleaseDate = %q", result.ReleaseDate)
	}
	if result.ContentRating != "R" {
		t.Errorf("ContentRating = %q", result.ContentRating)
	}
	if len(result.Genres) != 2 || len(result.Studios) != 2 || len(result.Countries) != 2 || len(result.Keywords) != 2 {
		t.Errorf("collections = genres %d studios %d countries %d keywords %d, want 2 each",
			len(result.Genres), len(result.Studios), len(result.Countries), len(result.Keywords))
	}
	if result.Ratings.IMDB != 8.1 || result.Ratings.TMDB != 7.9 || result.Ratings.RTCritic != 89 || result.Ratings.RTAudience != 91 {
		t.Errorf("Ratings = %#v", result.Ratings)
	}
	if len(result.People) != 5 {
		t.Errorf("People len = %d, want 5", len(result.People))
	}
	if result.FirstAirDate != "" {
		t.Errorf("FirstAirDate = %q, want empty for a movie", result.FirstAirDate)
	}
}

// A minimal NFO must not emit placeholder collections: empty slices stay nil
// so MergeFillEmpty's early-return lets remote providers fill them.
func TestGetMetadata_MinimalNFOEmitsNoPlaceholders(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "Home Movie (2021).mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("WriteFile(file) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.nfo"), []byte(`<movie><title>Home Movie</title></movie>`), 0o644); err != nil {
		t.Fatalf("WriteFile(movie.nfo) error = %v", err)
	}

	p := NewProvider()
	result, err := p.GetMetadata(context.Background(), metadata.MetadataRequest{
		ContentType: "movie",
		FilePath:    filePath,
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if result.Genres != nil || result.Studios != nil || result.Countries != nil || result.Keywords != nil || result.People != nil {
		t.Errorf("placeholder collections emitted: %#v", result)
	}
	if result.Runtime != 0 || result.ReleaseDate != "" || result.ContentRating != "" {
		t.Errorf("placeholder scalars emitted: %#v", result)
	}
}

// The series GetMetadata path maps aired/premiered onto FirstAirDate.
func TestGetMetadata_SeriesFirstAirDate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tvshow.nfo"), []byte(fullTVShowNFO), 0o644); err != nil {
		t.Fatalf("WriteFile(tvshow.nfo) error = %v", err)
	}

	p := NewProvider()
	result, err := p.GetMetadata(context.Background(), metadata.MetadataRequest{
		ContentType:               "series",
		PrimarySidecarSearchPaths: []string{dir},
	})
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if !result.HasMetadata {
		t.Fatal("HasMetadata = false, want true")
	}
	if result.FirstAirDate != "2015-06-24" {
		t.Errorf("FirstAirDate = %q, want 2015-06-24", result.FirstAirDate)
	}
	if result.ReleaseDate != "" {
		t.Errorf("ReleaseDate = %q, want empty for a series", result.ReleaseDate)
	}
}
