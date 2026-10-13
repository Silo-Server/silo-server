package nfo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

// skipIfPermissionsIgnored skips tests that rely on a mode-000 file being
// unreadable, which root (some CI containers) bypasses.
func skipIfPermissionsIgnored(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions do not block reads")
	}
}

func movieRequest(dir, file string, sidecarDirs ...string) metadata.MetadataRequest {
	return metadata.MetadataRequest{
		ContentType:               typeMovie,
		FilePath:                  filepath.Join(dir, file),
		AllGroupFilePaths:         []string{filepath.Join(dir, file)},
		PrimarySidecarSearchPaths: sidecarDirs,
	}
}

func TestFindItemNFO_Lookup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		files       map[string]string // relative path -> content
		mediaFiles  []string
		sidecarDirs []string // relative; "." is the temp dir
		contentType string
		wantPath    string // relative; "" means no NFO
		wantTitle   string
	}{
		{
			name:        "uppercase Movie.NFO in the movie folder",
			files:       map[string]string{"Movie.NFO": `<movie><title>Upper</title></movie>`},
			mediaFiles:  []string{"Film (1999).mkv"},
			sidecarDirs: []string{"."},
			contentType: typeMovie,
			wantPath:    "Movie.NFO",
			wantTitle:   "Upper",
		},
		{
			name:        "uppercase basename extension",
			files:       map[string]string{"Film (1999).NFO": `<movie><title>Base</title></movie>`},
			mediaFiles:  []string{"Film (1999).mkv"},
			contentType: typeMovie,
			wantPath:    "Film (1999).NFO",
			wantTitle:   "Base",
		},
		{
			name:        "basename in a different case than the media file",
			files:       map[string]string{"film (1999).nfo": `<movie><title>Lower</title></movie>`},
			mediaFiles:  []string{"Film (1999).mkv"},
			contentType: typeMovie,
			wantPath:    "film (1999).nfo",
			wantTitle:   "Lower",
		},
		{
			name:        "TVSHOW.nfo in the series root",
			files:       map[string]string{"TVSHOW.nfo": `<tvshow><title>Show</title></tvshow>`},
			sidecarDirs: []string{"."},
			contentType: typeSeries,
			wantPath:    "TVSHOW.nfo",
			wantTitle:   "Show",
		},
		{
			name: "exact spelling wins among case variants",
			files: map[string]string{
				"MOVIE.nfo": `<movie><title>Shouting</title></movie>`,
				"movie.nfo": `<movie><title>Exact</title></movie>`,
			},
			mediaFiles:  []string{"Film.mkv"},
			sidecarDirs: []string{"."},
			contentType: typeMovie,
			wantPath:    "movie.nfo",
			wantTitle:   "Exact",
		},
		{
			name: "basename wins over movie.nfo (Kodi order)",
			files: map[string]string{
				"movie.nfo":       `<movie><title>Folder</title></movie>`,
				"Film (1999).nfo": `<movie><title>Basename</title></movie>`,
			},
			mediaFiles:  []string{"Film (1999).mkv"},
			sidecarDirs: []string{"."},
			contentType: typeMovie,
			wantPath:    "Film (1999).nfo",
			wantTitle:   "Basename",
		},
		{
			name:        "stacked parts use the stack base name",
			files:       map[string]string{"Film (1999).nfo": `<movie><title>Stacked</title></movie>`},
			mediaFiles:  []string{"Film (1999)-cd1.mkv", "Film (1999)-cd2.mkv"},
			contentType: typeMovie,
			wantPath:    "Film (1999).nfo",
			wantTitle:   "Stacked",
		},
		{
			name: "a part's own NFO wins over the stack base name",
			files: map[string]string{
				"Film (1999).nfo":     `<movie><title>Stack</title></movie>`,
				"Film (1999)-cd1.nfo": `<movie><title>Part</title></movie>`,
			},
			mediaFiles:  []string{"Film (1999)-cd1.mkv"},
			contentType: typeMovie,
			wantPath:    "Film (1999)-cd1.nfo",
			wantTitle:   "Part",
		},
		{
			name:        "flat multi-movie folder: movie.nfo is not a sidecar of its files",
			files:       map[string]string{"movie.nfo": `<movie><title>Stray</title></movie>`},
			mediaFiles:  []string{"Fight Club (1999).mkv", "Se7en (1995).mkv"},
			contentType: typeMovie,
		},
		{
			name:        "a show's episode NFOs are never its tvshow NFO",
			files:       map[string]string{"Season 1/Show S01E01.nfo": `<tvshow><title>Wrong</title></tvshow>`},
			mediaFiles:  []string{"Season 1/Show S01E01.mkv"},
			contentType: typeSeries,
		},
		{
			name: "a lone part does not read the NFO its name shortens to",
			files: map[string]string{
				"Dune.nfo": `<movie><title>Dune</title></movie>`,
			},
			mediaFiles:  []string{"Dune Part 2.mkv"},
			contentType: typeMovie,
		},
		{
			name:        "URL-only TV link is not a movie's NFO",
			files:       map[string]string{"Film.nfo": "https://www.themoviedb.org/tv/1396"},
			mediaFiles:  []string{"Film.mkv"},
			contentType: typeMovie,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for _, rel := range tc.mediaFiles {
				writeFile(t, filepath.Join(root, rel), "video")
			}
			for rel, content := range tc.files {
				writeFile(t, filepath.Join(root, rel), content)
			}
			abs := func(rels []string) []string {
				out := make([]string, 0, len(rels))
				for _, rel := range rels {
					out = append(out, filepath.Join(root, rel))
				}
				return out
			}
			got, parsed, err := NewProvider().newLookup(context.Background()).
				findItemNFO(abs(tc.mediaFiles), abs(tc.sidecarDirs), tc.contentType)
			if err != nil {
				t.Fatalf("findItemNFO() error = %v", err)
			}
			if tc.wantPath == "" {
				if parsed != nil {
					t.Fatalf("findItemNFO() = %q (%q), want no NFO", got, parsed.Title)
				}
				return
			}
			if got != filepath.Join(root, tc.wantPath) || parsed == nil || parsed.Title != tc.wantTitle {
				t.Fatalf("findItemNFO() = %q, %+v; want %q titled %q", got, parsed, tc.wantPath, tc.wantTitle)
			}
		})
	}
}

func TestStackBaseName(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"Movie-cd1":                     "Movie",
		"Movie (2009) - part1 - 1080p":  "Movie (2009) - 1080p",
		"Movie.Part.2":                  "Movie",
		"Movie disc 1":                  "Movie",
		"Movie-pt2":                     "Movie",
		"CD1.Feature":                   "Feature",
		"Disc 2 - Feature Film (2001)":  "Feature Film (2001)",
		"Part 1":                        "",
		"...And Justice for All-cd1":    "...And Justice for All",
		"Movie_dvd2":                    "",
		"Film-cd0":                      "",
		"Movie.2002.DVD9":               "",
		"Movie-pt.b":                    "",
		"The Departed (2006)":           "",
		"Movie - Part Deux":             "",
		"cd1":                           "",
		"Toy Story 4 (2019)":            "",
		"Jurassic Park (1993)":          "",
		"Star Wars - Episode IV (1977)": "",
	}
	for in, want := range tests {
		if got := stackBaseName(in); got != want {
			t.Errorf("stackBaseName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A stray movie.nfo in a flat multi-movie folder must not name every movie
// in it: without a single-content sidecar directory the provider gives no
// hints and no metadata.
func TestProvider_FlatFolderMovieNFOAppliesToNoMovie(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "movie.nfo"), `<movie><title>Pulp Fiction</title><uniqueid type="tmdb">680</uniqueid></movie>`)
	p := NewProvider()
	for _, file := range []string{"Fight Club (1999).mkv", "Se7en (1995).mkv"} {
		writeFile(t, filepath.Join(dir, file), "video")
		req := movieRequest(dir, file)
		hints := p.IdentityHints(context.Background(), metadata.SearchQuery{
			ContentType: req.ContentType, FilePath: req.FilePath, AllGroupFilePaths: req.AllGroupFilePaths,
		})
		if len(hints) != 0 {
			t.Errorf("%s: hints = %v, want none", file, hints)
		}
		result, err := p.GetMetadata(context.Background(), req)
		if err != nil || result.HasMetadata {
			t.Errorf("%s: GetMetadata() = %+v, %v; want no metadata", file, result, err)
		}
	}
}

func TestProvider_SkipsOversizedAndSymlinkedNFO(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.nfo")
	writeFile(t, target, `<movie><title>Outside</title><uniqueid type="tmdb">1</uniqueid></movie>`)
	if err := os.Symlink(target, filepath.Join(dir, "Film.nfo")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	big := `<movie><title>Huge</title><plot>` + strings.Repeat("x", maxNFOBytes) + `</plot></movie>`
	writeFile(t, filepath.Join(dir, "movie.nfo"), big)
	writeFile(t, filepath.Join(dir, "Film.mkv"), "video")

	result, err := NewProvider().GetMetadata(context.Background(), movieRequest(dir, "Film.mkv", dir))
	if err != nil {
		t.Fatalf("GetMetadata() error = %v", err)
	}
	if result.HasMetadata {
		t.Fatalf("GetMetadata() = %q, want the symlinked and the oversized NFO both skipped", result.Title)
	}
}

// An NFO that exists but cannot be read is not "no NFO": the provider
// reports metadata.ErrLocalMetadataUnavailable so a manual refresh keeps the
// stored values instead of replacing them with remote ones.
func TestProvider_UnreadableNFOReportsUnavailable(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	t.Run("file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		nfoPath := filepath.Join(dir, "movie.nfo")
		writeFile(t, nfoPath, `<movie><title>Locked</title><uniqueid type="tmdb">7</uniqueid></movie>`)
		writeFile(t, filepath.Join(dir, "Film.mkv"), "video")
		if err := os.Chmod(nfoPath, 0); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
		assertUnavailable(t, NewProvider(), movieRequest(dir, "Film.mkv", dir))
	})

	t.Run("directory", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "Film (2001)")
		writeFile(t, filepath.Join(dir, "movie.nfo"), `<movie><title>Locked</title></movie>`)
		writeFile(t, filepath.Join(dir, "Film.mkv"), "video")
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		assertUnavailable(t, NewProvider(), movieRequest(dir, "Film.mkv", dir))
	})

	t.Run("episode", func(t *testing.T) {
		t.Parallel()
		seasonDir := filepath.Join(t.TempDir(), "Season 01")
		episodeNFO := filepath.Join(seasonDir, "Show S01E01.nfo")
		writeFile(t, episodeNFO, `<episodedetails><title>Locked</title></episodedetails>`)
		writeFile(t, filepath.Join(seasonDir, "Show S01E01.mkv"), "video")
		if err := os.Chmod(episodeNFO, 0); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
		_, err := NewProvider().GetEpisodes(context.Background(), metadata.EpisodesRequest{
			SeasonNumber:     1,
			EpisodeFilePaths: map[int][]string{1: {filepath.Join(seasonDir, "Show S01E01.mkv")}},
		})
		if !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
			t.Fatalf("GetEpisodes() error = %v, want ErrLocalMetadataUnavailable", err)
		}
	})
}

func assertUnavailable(t *testing.T, p *Provider, req metadata.MetadataRequest) {
	t.Helper()
	if _, err := p.GetMetadata(context.Background(), req); !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Errorf("GetMetadata() error = %v, want ErrLocalMetadataUnavailable", err)
	}
	query := metadata.SearchQuery{
		ContentType: req.ContentType, FilePath: req.FilePath,
		AllGroupFilePaths: req.AllGroupFilePaths, PrimarySidecarSearchPaths: req.PrimarySidecarSearchPaths,
	}
	if _, err := p.Search(context.Background(), query); !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Errorf("Search() error = %v, want ErrLocalMetadataUnavailable", err)
	}
	if hints := p.IdentityHints(context.Background(), query); hints != nil {
		t.Errorf("IdentityHints() = %v, want none", hints)
	}
}

// A show's lookup reads only its series root: an unreadable season
// directory does not fail it, because episode files are not probed.
func TestProvider_SeriesLookupDoesNotProbeEpisodes(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "tvshow.nfo"), `<tvshow><title>Show</title><uniqueid type="tvdb">81189</uniqueid></tvshow>`)
	seasonDir := filepath.Join(root, "Season 01")
	writeFile(t, filepath.Join(seasonDir, "Show S01E01.nfo"), `<episodedetails><title>Pilot</title></episodedetails>`)
	if err := os.Chmod(seasonDir, 0); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(seasonDir, 0o755) })

	hints := NewProvider().IdentityHints(context.Background(), metadata.SearchQuery{
		ContentType:               typeSeries,
		FilePath:                  filepath.Join(seasonDir, "Show S01E01.mkv"),
		AllGroupFilePaths:         []string{filepath.Join(seasonDir, "Show S01E01.mkv"), filepath.Join(seasonDir, "Show S01E02.mkv")},
		PrimarySidecarSearchPaths: []string{root},
	})
	if hints["tvdb"] != "81189" {
		t.Fatalf("IdentityHints() = %v, want tvdb 81189 from tvshow.nfo", hints)
	}
}

func TestProvider_URLOnlyNFOGivesHintsButNoFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Film (1982).nfo"), "https://www.themoviedb.org/movie/78-blade-runner\n")
	writeFile(t, filepath.Join(dir, "Film (1982).mkv"), "video")
	p := NewProvider()
	req := movieRequest(dir, "Film (1982).mkv")
	hints := p.IdentityHints(context.Background(), metadata.SearchQuery{
		ContentType: typeMovie, FilePath: req.FilePath, AllGroupFilePaths: req.AllGroupFilePaths,
	})
	if len(hints) != 1 || hints["tmdb"] != "78" {
		t.Fatalf("IdentityHints() = %v, want tmdb 78 only", hints)
	}
	result, err := p.GetMetadata(context.Background(), req)
	if err != nil || result.HasMetadata {
		t.Fatalf("GetMetadata() = %+v, %v; want no metadata from a URL-only NFO", result, err)
	}
}

func TestProvider_SeasonAndEpisodeNFONamesIgnoreCase(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	seasonDir := filepath.Join(root, "Season 01")
	writeFile(t, filepath.Join(seasonDir, "Season.NFO"), `<season><title>First</title><seasonnumber>1</seasonnumber></season>`)
	writeFile(t, filepath.Join(seasonDir, "Show S01E01.NFO"), `<episodedetails><title>Pilot</title><episode>one</episode></episodedetails>`)
	writeFile(t, filepath.Join(seasonDir, "Show S01E01.mkv"), "video")
	p := NewProvider()

	seasons, err := p.GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          typeSeries,
		SeasonDirectoryPaths: map[int][]string{1: {seasonDir}},
	})
	if err != nil || len(seasons) != 1 || seasons[0].Title != "First" {
		t.Fatalf("GetSeasons() = %+v, %v; want season 1 titled First", seasons, err)
	}
	episodes, err := p.GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		EpisodeFilePaths: map[int][]string{1: {filepath.Join(seasonDir, "Show S01E01.mkv")}},
	})
	if err != nil || len(episodes) != 1 || episodes[0].Title != "Pilot" {
		t.Fatalf("GetEpisodes() = %+v, %v; want episode 1 titled Pilot", episodes, err)
	}
}

// The provider parses unchanged content once across the phases of a pass
// and re-parses a file whose content changes, even when its size and
// modification time don't (a rewrite within a coarse filesystem's timestamp
// tick). Results handed out are copies.
func TestProvider_ParseCache(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	nfoPath := filepath.Join(dir, "movie.nfo")
	writeFile(t, nfoPath, `<movie><title>AAAA</title><genre>Drama</genre></movie>`)
	writeFile(t, filepath.Join(dir, "Film.mkv"), "video")
	stamp := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(nfoPath, stamp, stamp); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	p := NewProvider()
	req := movieRequest(dir, "Film.mkv", dir)

	first, err := p.GetMetadata(context.Background(), req)
	if err != nil || first.Title != "AAAA" {
		t.Fatalf("GetMetadata() = %+v, %v", first, err)
	}
	first.Genres[0] = "mutated by a caller"
	cached, err := p.GetMetadata(context.Background(), req)
	if err != nil || cached.Title != "AAAA" || cached.Genres[0] != "Drama" {
		t.Fatalf("GetMetadata() = %q %v, %v; want AAAA with unshared genres", cached.Title, cached.Genres, err)
	}
	if got := len(p.parses.entries); got != 1 {
		t.Fatalf("parse cache holds %d entries after two reads of one file, want 1", got)
	}

	// Same size and modification time, new content: re-parsed.
	writeFile(t, nfoPath, `<movie><title>BBBB</title><genre>Drama</genre></movie>`)
	if err := os.Chtimes(nfoPath, stamp, stamp); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	fresh, err := p.GetMetadata(context.Background(), req)
	if err != nil || fresh.Title != "BBBB" {
		t.Fatalf("GetMetadata() = %q, %v; want the re-parsed BBBB", fresh.Title, err)
	}
}

// Every directory the provider looks in holds a media file the catalog lists
// as present, so a directory that is gone (an unmounted share often leaves an
// empty mountpoint) is unavailable, not "no NFO".
func TestFindItemNFO_VanishedMediaDirectoryIsUnavailable(t *testing.T) {
	t.Parallel()

	gone := filepath.Join(t.TempDir(), "gone")
	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(gone, "Film.mkv")}, []string{gone}, typeMovie)
	if parsed != nil || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("findItemNFO() = %+v, %v; want ErrLocalMetadataUnavailable", parsed, err)
	}
}

// A version in an unreadable folder does not hide the NFO of a version in a
// readable one.
func TestFindItemNFO_ReadableVersionWinsOverUnreadableOne(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	root := t.TempDir()
	locked := filepath.Join(root, "a")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeFile(t, filepath.Join(locked, "Movie.mkv"), "video")
	writeFile(t, filepath.Join(root, "b", "Movie.mkv"), "video")
	writeFile(t, filepath.Join(root, "b", "Movie.nfo"), `<movie><title>From B</title></movie>`)
	if err := os.Chmod(locked, 0o311); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	got, parsed, err := NewProvider().newLookup(context.Background()).findItemNFO(
		[]string{filepath.Join(locked, "Movie.mkv"), filepath.Join(root, "b", "Movie.mkv")}, nil, typeMovie)
	if err != nil || parsed == nil || parsed.Title != "From B" {
		t.Fatalf("findItemNFO() = %q, %+v, %v; want b/Movie.nfo", got, parsed, err)
	}
}

// A folder the server may traverse but not list still has its exact NFO
// names read, as before the listing-based lookup.
func TestFindItemNFO_TraverseOnlyFolderFallsBackToExactNames(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	dir := filepath.Join(t.TempDir(), "Film (2001)")
	writeFile(t, filepath.Join(dir, "movie.nfo"), `<movie><title>Exact</title></movie>`)
	writeFile(t, filepath.Join(dir, "Film.mkv"), "video")
	if err := os.Chmod(dir, 0o311); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(dir, "Film.mkv")}, []string{dir}, typeMovie)
	if err != nil || parsed == nil || parsed.Title != "Exact" {
		t.Fatalf("findItemNFO() = %+v, %v; want movie.nfo by its exact name", parsed, err)
	}
}

// The provider reuses a directory listing across calls until the directory
// changes, so a flat folder of many movies is listed once, not once per item
// and phase. A change is seen even when a copy tool restores the folder's
// modification time, since the folder's change time keys the listing too.
func TestProvider_DirectoryListingFollowsChanges(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Film.mkv"), "video")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	p := NewProvider()
	first, err := p.listDir(dir)
	if err != nil {
		t.Fatalf("listDir() error = %v", err)
	}
	if second, _ := p.listDir(dir); second != first {
		t.Fatalf("an unchanged folder was listed again")
	}

	writeFile(t, filepath.Join(dir, "movie.nfo"), `<movie><title>Added</title></movie>`)
	if err := os.Chtimes(dir, old, old); err != nil { // a copy tool restores the time
		t.Fatalf("Chtimes() error = %v", err)
	}
	third, err := p.listDir(dir)
	if err != nil || third == first || third.index["movie.nfo"] == nil {
		t.Fatalf("listDir() after an added file and a restored time = %+v, %v; want a fresh listing with movie.nfo", third, err)
	}
}

// A file added later in the same timestamp tick leaves a directory's times
// unchanged, so a listing is cached only once its directory has settled: a
// few seconds for a whole-millisecond (coarse) time, 20 ms otherwise, and
// never for a time ahead of this server's clock.
func TestProvider_RecentListingIsNotCached(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		stamp      time.Time
		wantCached bool
	}{
		{"coarse and recent", time.Now().Truncate(time.Second), false},
		{"fine and a second old", time.Now().Add(-time.Second).Truncate(time.Millisecond).Add(123456), true},
		{"ahead of the clock", time.Now().Add(time.Hour).Truncate(time.Second).Add(123456789), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "Film.mkv"), "video")
			if err := os.Chtimes(dir, tc.stamp, tc.stamp); err != nil {
				t.Fatalf("Chtimes() error = %v", err)
			}
			p := NewProvider()
			first, err := p.listDir(dir)
			if err != nil {
				t.Fatalf("listDir() error = %v", err)
			}
			second, _ := p.listDir(dir)
			if cached := second == first; cached != tc.wantCached {
				t.Fatalf("listing cached = %v, want %v", cached, tc.wantCached)
			}
		})
	}
}

// A show whose folders span two roots checks each root for its own
// episodes: when the second root's share is gone (an empty mountpoint), its
// tvshow.nfo is unavailable, not absent, even though the first root's
// episode is still there.
func TestFindItemNFO_SeriesChecksEveryRoot(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	first, second := filepath.Join(base, "a", "Show"), filepath.Join(base, "b", "Show")
	writeFile(t, filepath.Join(first, "Season 01", "Show S01E01.mkv"), "video")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	episodes := []string{
		filepath.Join(first, "Season 01", "Show S01E01.mkv"),
		filepath.Join(second, "Season 02", "Show S02E01.mkv"),
	}
	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO(episodes, []string{first, second}, typeSeries)
	if parsed != nil || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("findItemNFO() = %+v, %v; want ErrLocalMetadataUnavailable for the missing root", parsed, err)
	}
}

// The scanner also groups parts whose token leads the name, so CD1.Feature
// and CD2.Feature share Feature.nfo.
func TestFindItemNFO_StackWithLeadingToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "CD1.Feature.mkv"), "video")
	writeFile(t, filepath.Join(dir, "CD2.Feature.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Feature.nfo"), `<movie><title>Feature</title></movie>`)
	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(dir, "CD1.Feature.mkv")}, nil, typeMovie)
	if err != nil || parsed == nil || parsed.Title != "Feature" {
		t.Fatalf("findItemNFO(CD1.Feature.mkv) = %+v, %v; want Feature.nfo", parsed, err)
	}
}

// The scanner catalogs only files, following symlinks, so a directory (or a
// symlink to one) named like a part does not make a lone part a stack.
func TestFindItemNFO_StackIgnoresDirectoriesNamedLikeParts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Film-cd1.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Film.nfo"), `<movie><title>Film</title></movie>`)
	if err := os.Mkdir(filepath.Join(dir, "Film-cd2.mp4"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, "Film-cd3.mkv")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(dir, "Film-cd1.mkv")}, nil, typeMovie)
	if err != nil || parsed != nil {
		t.Fatalf("findItemNFO(lone part beside part-named directories) = %+v, %v; want no NFO", parsed, err)
	}

	writeFile(t, filepath.Join(target, "real.mkv"), "video")
	if err := os.Symlink(filepath.Join(target, "real.mkv"), filepath.Join(dir, "Film-cd4.mkv")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(dir, "Film-cd1.mkv")}, nil, typeMovie); err != nil || parsed == nil || parsed.Title != "Film" {
		t.Fatalf("findItemNFO(part beside a symlinked part file) = %+v, %v; want Film.nfo", parsed, err)
	}
}

// One unreadable season.nfo or episode NFO skips only that entry: the call
// returns what it could read together with ErrLocalMetadataUnavailable, so a
// fill-empty pass keeps the rest and a replacing pass can stop.
func TestProvider_UnreadableSeasonOrEpisodeNFOSkipsOnlyThatEntry(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	root := t.TempDir()
	s1, s2 := filepath.Join(root, "Season 01"), filepath.Join(root, "Season 02")
	writeFile(t, filepath.Join(s1, "season.nfo"), `<season><title>One</title></season>`)
	writeFile(t, filepath.Join(s2, "season.nfo"), `<season><title>Two</title></season>`)
	writeFile(t, filepath.Join(s1, "Show S01E01.nfo"), `<episodedetails><title>Pilot</title></episodedetails>`)
	writeFile(t, filepath.Join(s1, "Show S01E02.nfo"), `<episodedetails><title>Locked</title></episodedetails>`)
	writeFile(t, filepath.Join(s1, "Show S01E01.mkv"), "video")
	writeFile(t, filepath.Join(s1, "Show S01E02.mkv"), "video")
	for _, path := range []string{filepath.Join(s2, "season.nfo"), filepath.Join(s1, "Show S01E02.nfo")} {
		if err := os.Chmod(path, 0); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
	}
	p := NewProvider()

	seasons, err := p.GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          typeSeries,
		SeasonDirectoryPaths: map[int][]string{1: {s1}, 2: {s2}},
	})
	if !errors.Is(err, metadata.ErrLocalMetadataUnavailable) || len(seasons) != 1 || seasons[0].Title != "One" {
		t.Errorf("GetSeasons() = %+v, %v; want season 1 and ErrLocalMetadataUnavailable", seasons, err)
	}
	episodes, err := p.GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber: 1,
		EpisodeFilePaths: map[int][]string{
			1: {filepath.Join(s1, "Show S01E01.mkv")},
			2: {filepath.Join(s1, "Show S01E02.mkv")},
		},
	})
	if !errors.Is(err, metadata.ErrLocalMetadataUnavailable) || len(episodes) != 1 || episodes[0].Title != "Pilot" {
		t.Errorf("GetEpisodes() = %+v, %v; want episode 1 and ErrLocalMetadataUnavailable", episodes, err)
	}
}

// An initial match carries only the representative part, so the stack is
// recognized from the folder: Movie-cd1 reads Movie.nfo when Movie-cd2 sits
// beside it, but two copies of one part are not a stack.
func TestFindItemNFO_StackFromFolder(t *testing.T) {
	t.Parallel()

	stacked := t.TempDir()
	writeFile(t, filepath.Join(stacked, "Film-cd1.mkv"), "video")
	writeFile(t, filepath.Join(stacked, "Film-cd2.mkv"), "video")
	writeFile(t, filepath.Join(stacked, "Film.nfo"), `<movie><title>Stacked</title></movie>`)
	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(stacked, "Film-cd1.mkv")}, nil, typeMovie)
	if err != nil || parsed == nil || parsed.Title != "Stacked" {
		t.Fatalf("findItemNFO(cd1 with cd2 on disk) = %+v, %v; want Film.nfo", parsed, err)
	}

	copies := t.TempDir()
	writeFile(t, filepath.Join(copies, "Dune Part 2.mkv"), "video")
	writeFile(t, filepath.Join(copies, "Dune Part 2.mp4"), "video")
	writeFile(t, filepath.Join(copies, "Dune.nfo"), `<movie><title>Dune</title></movie>`)
	_, parsed, err = NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(copies, "Dune Part 2.mkv"), filepath.Join(copies, "Dune Part 2.mp4")}, nil, typeMovie)
	if err != nil || parsed != nil {
		t.Fatalf("findItemNFO(two copies of one part) = %+v, %v; want no NFO", parsed, err)
	}
}

// A season that spans two folders still gets the readable season.nfo when the
// other folder's copy cannot be read.
func TestGetSeasons_ReadableSeasonNFOInAnotherFolderWins(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	root := t.TempDir()
	first, second := filepath.Join(root, "a", "Season 01"), filepath.Join(root, "b", "Season 01")
	writeFile(t, filepath.Join(first, "season.nfo"), `<season><title>Locked</title></season>`)
	writeFile(t, filepath.Join(second, "season.nfo"), `<season><title>Readable</title></season>`)
	writeFile(t, filepath.Join(first, "Show S01E01.mkv"), "video")
	writeFile(t, filepath.Join(second, "Show S01E02.mkv"), "video")
	if err := os.Chmod(filepath.Join(first, "season.nfo"), 0); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	seasons, err := NewProvider().GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          typeSeries,
		SeasonDirectoryPaths: map[int][]string{1: {first, second}},
		ShowFilePaths:        []string{filepath.Join(first, "Show S01E01.mkv"), filepath.Join(second, "Show S01E02.mkv")},
	})
	if err != nil || len(seasons) != 1 || seasons[0].Title != "Readable" {
		t.Fatalf("GetSeasons() = %+v, %v; want the readable season.nfo", seasons, err)
	}
}

// An unmounted share often leaves an empty mountpoint that lists without
// error. A folder that no longer holds the media file the catalog lists says
// nothing about its sidecars, so the lookup reports it unavailable rather
// than "no NFO".
func TestFindItemNFO_EmptyMountpointIsUnavailable(t *testing.T) {
	t.Parallel()

	mountpoint := t.TempDir() // exists, but the share's files are gone
	for _, contentType := range []string{typeMovie, typeSeries} {
		_, parsed, err := NewProvider().newLookup(context.Background()).
			findItemNFO([]string{filepath.Join(mountpoint, "Film (2001).mkv")}, []string{mountpoint}, contentType)
		if parsed != nil || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
			t.Errorf("%s: findItemNFO() = %+v, %v; want ErrLocalMetadataUnavailable", contentType, parsed, err)
		}
	}
	_, err := NewProvider().GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		EpisodeFilePaths: map[int][]string{1: {filepath.Join(mountpoint, "Show S01E01.mkv")}},
	})
	if !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Errorf("GetEpisodes() error = %v, want ErrLocalMetadataUnavailable", err)
	}
}

// Only video files count toward a stack:
// a subtitle or NFO named like the missing part does not make a lone part a
// stack.
func TestFindItemNFO_StackCountsOnlyVideoParts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Dune Part 2.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Dune Part 1.srt"), "subtitle")
	writeFile(t, filepath.Join(dir, "Dune Part 1.nfo"), `<movie><title>Part One</title></movie>`)
	writeFile(t, filepath.Join(dir, "Dune.nfo"), `<movie><title>Dune</title></movie>`)
	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(dir, "Dune Part 2.mkv")}, nil, typeMovie)
	if err != nil || parsed != nil {
		t.Fatalf("findItemNFO(lone part beside a part-named subtitle and NFO) = %+v, %v; want no NFO", parsed, err)
	}
}

// A cataloged media file is present while its path is still a file, as the
// scanner sees it: a symlink to a file counts, while a directory (or a
// symlink to one) at that path, such as one left on the mountpoint under an
// unmounted share, does not.
func TestMediaPresent_RequiresAFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Film.mkv"), "video")
	if err := os.Mkdir(filepath.Join(dir, "Folder.mkv"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "Film.mkv"), filepath.Join(dir, "Linked.mkv")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "Folder.mkv"), filepath.Join(dir, "LinkedFolder.mkv")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	for name, wantPresent := range map[string]bool{
		"Film.mkv":         true,
		"Linked.mkv":       true,
		"Folder.mkv":       false,
		"LinkedFolder.mkv": false,
		"Gone.mkv":         false,
	} {
		err := mediaPresent(filepath.Join(dir, name))
		if wantPresent && err != nil {
			t.Errorf("mediaPresent(%s) = %v, want present", name, err)
		}
		if !wantPresent && !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
			t.Errorf("mediaPresent(%s) = %v, want ErrLocalMetadataUnavailable", name, err)
		}
	}
}

// In a folder the server may traverse but not list, only the exact name can
// be probed, so a miss there is not proof that no NFO exists: a differently
// cased MOVIE.NFO may be present. The lookup reports the folder unreadable.
func TestFindItemNFO_TraverseOnlyFolderMissIsUnavailable(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	dir := filepath.Join(t.TempDir(), "Film (2001)")
	writeFile(t, filepath.Join(dir, "MOVIE.NFO"), `<movie><title>Hidden</title></movie>`)
	writeFile(t, filepath.Join(dir, "Film.mkv"), "video")
	if err := os.Chmod(dir, 0o311); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(dir, "Film.mkv")}, []string{dir}, typeMovie)
	if parsed != nil || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("findItemNFO() = %+v, %v; want a permission ErrLocalMetadataUnavailable", parsed, err)
	}
}

// Parts of one stack may differ in container, as the scanner allows, but
// only video files count as parts.
func TestFindItemNFO_StackAcrossContainers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Film-cd1.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Film-cd2.mp4"), "video")
	writeFile(t, filepath.Join(dir, "Film.nfo"), `<movie><title>Mixed</title></movie>`)
	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(dir, "Film-cd1.mkv")}, nil, typeMovie)
	if err != nil || parsed == nil || parsed.Title != "Mixed" {
		t.Fatalf("findItemNFO(cd1.mkv beside cd2.mp4) = %+v, %v; want Film.nfo", parsed, err)
	}
}

// A season folder comes from the episode files the catalog lists in it, so
// an empty one (an unmounted share's mountpoint) is unavailable, not a
// season without season.nfo.
func TestGetSeasons_EmptySeasonFolderIsUnavailable(t *testing.T) {
	t.Parallel()

	empty := filepath.Join(t.TempDir(), "Season 01")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	_, err := NewProvider().GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          typeSeries,
		SeasonDirectoryPaths: map[int][]string{1: {empty}},
		ShowFilePaths:        []string{filepath.Join(empty, "Show S01E01.mkv")},
	})
	if !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("GetSeasons(empty season folder) error = %v, want ErrLocalMetadataUnavailable", err)
	}
}

// A mountpoint can hold leftovers such as .keep, lost+found, or an unrelated
// video while its share is gone; a season folder is read only while one of
// the season's cataloged episode files is in it.
func TestGetSeasons_SeasonFolderWithoutItsEpisodesIsUnavailable(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "Season 01")
	writeFile(t, filepath.Join(dir, ".keep"), "")
	if err := os.Mkdir(filepath.Join(dir, "lost+found"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeFile(t, filepath.Join(dir, "Unrelated.mkv"), "video")
	writeFile(t, filepath.Join(dir, "season.nfo"), `<season><title>First</title></season>`)
	episode := filepath.Join(dir, "Show S01E01.mkv")
	p := NewProvider()
	req := metadata.SeasonsRequest{
		ContentType:          typeSeries,
		SeasonDirectoryPaths: map[int][]string{1: {dir}},
		ShowFilePaths:        []string{episode},
	}
	if _, err := p.GetSeasons(context.Background(), req); !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("GetSeasons(season folder without its episode) error = %v, want ErrLocalMetadataUnavailable", err)
	}

	writeFile(t, episode, "video")
	seasons, err := p.GetSeasons(context.Background(), req)
	if err != nil || len(seasons) != 1 || seasons[0].Title != "First" {
		t.Fatalf("GetSeasons() = %+v, %v; want season 1 titled First once its episode is back", seasons, err)
	}
}

// A root that no longer holds its cataloged media says nothing about its
// sidecars, so a stale movie.nfo or tvshow.nfo left on the mountpoint
// underneath an unmounted share is not applied; the lookup reports the root
// unavailable instead.
func TestFindItemNFO_StaleDirectoryNFOOnMountpointIsNotRead(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ contentType, nfoName, nfo, media string }{
		{typeMovie, "movie.nfo", `<movie><title>Stale</title></movie>`, "Film (2001).mkv"},
		{typeSeries, "tvshow.nfo", `<tvshow><title>Stale</title></tvshow>`, filepath.Join("Season 01", "Show S01E01.mkv")},
	} {
		mountpoint := t.TempDir()
		writeFile(t, filepath.Join(mountpoint, tc.nfoName), tc.nfo)
		_, parsed, err := NewProvider().newLookup(context.Background()).
			findItemNFO([]string{filepath.Join(mountpoint, tc.media)}, []string{mountpoint}, tc.contentType)
		if parsed != nil || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
			t.Errorf("%s: findItemNFO() = %+v, %v; want ErrLocalMetadataUnavailable and no stale NFO", tc.contentType, parsed, err)
		}
	}
}

// The scanner groups parts whose separators differ, so the lookup compares
// stack names the same way and the parts share their NFO.
func TestFindItemNFO_StackPartsWithDifferentSeparators(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Film-part1-1080p.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Film part2 1080p.mp4"), "video")
	writeFile(t, filepath.Join(dir, "Film-1080p.nfo"), `<movie><title>Film</title></movie>`)
	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(dir, "Film-part1-1080p.mkv")}, nil, typeMovie)
	if err != nil || parsed == nil || parsed.Title != "Film" {
		t.Fatalf("findItemNFO(Film-part1-1080p.mkv) = %+v, %v; want Film-1080p.nfo", parsed, err)
	}
}

// A cataloged part that is no longer on disk does not make the part that is
// left a stack.
func TestFindItemNFO_StackIgnoresMissingCatalogedParts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Film-cd1.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Film.nfo"), `<movie><title>Film</title></movie>`)
	files := []string{filepath.Join(dir, "Film-cd1.mkv"), filepath.Join(dir, "Film-cd2.mkv")}
	_, parsed, err := NewProvider().newLookup(context.Background()).findItemNFO(files[:1], nil, typeMovie)
	if err != nil || parsed != nil {
		t.Fatalf("findItemNFO(cd1 with cataloged cd2 gone) = %+v, %v; want no NFO", parsed, err)
	}
	_, parsed, _ = NewProvider().newLookup(context.Background()).findItemNFO(files, nil, typeMovie)
	if parsed != nil {
		t.Fatalf("findItemNFO(both parts requested, cd2 gone) = %+v; want no stack NFO", parsed)
	}
}

// An NFO replaced by an editor's atomic save is read: it is still a regular
// file. A symlink or a FIFO swapped in is reported, not read as a missing
// NFO, whatever inode number it gets (a deleted file's number can be reused
// at once), and the open does not block on a FIFO.
func TestReadNFOFile_ReplacedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "movie.nfo")
	writeFile(t, path, `<movie><title>Old</title></movie>`)

	writeFile(t, filepath.Join(dir, "new.nfo"), `<movie><title>New</title></movie>`)
	if err := os.Rename(filepath.Join(dir, "new.nfo"), path); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if data, err := readNFOFile(path); err != nil || !strings.Contains(string(data), "New") {
		t.Fatalf("readNFOFile(atomically replaced) = %q, %v; want the new file", data, err)
	}

	target := filepath.Join(t.TempDir(), "elsewhere.nfo")
	writeFile(t, target, `<movie><title>Elsewhere</title></movie>`)
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := readNFOFile(path); !errors.Is(err, errNFOReplaced) {
		t.Fatalf("readNFOFile(swapped for a symlink) error = %v, want errNFOReplaced", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Skipf("Mkfifo() unsupported: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readNFOFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errNFOReplaced) {
			t.Fatalf("readNFOFile(FIFO) error = %v, want errNFOReplaced", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readNFOFile(FIFO) blocked")
	}
}

// A show whose episode can't be reached for permission still has its root
// searched; when that root has no tvshow.nfo, the lookup reports the
// permission problem rather than "no NFO".
func TestFindItemNFO_SeriesPermissionErrorWithoutNFOIsUnavailable(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	root := t.TempDir()
	seasonDir := filepath.Join(root, "Season 01")
	writeFile(t, filepath.Join(seasonDir, "Show S01E01.mkv"), "video")
	if err := os.Chmod(seasonDir, 0); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(seasonDir, 0o755) })

	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(seasonDir, "Show S01E01.mkv")}, []string{root}, typeSeries)
	if parsed != nil || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("findItemNFO() = %+v, %v; want a permission ErrLocalMetadataUnavailable", parsed, err)
	}
}

// A cataloged file deleted before the next scan, from a folder that still
// holds the item's other files, is just gone: the share is mounted, so the
// lookup reports no NFO rather than an unreadable one.
func TestLookup_FileDeletedFromLiveFolderIsNotUnavailable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Film (2001) - 1080p.mkv"), "video")
	versions := []string{filepath.Join(dir, "Film (2001) - 2160p.mkv"), filepath.Join(dir, "Film (2001) - 1080p.mkv")}
	_, parsed, err := NewProvider().newLookup(context.Background()).findItemNFO(versions, []string{dir}, typeMovie)
	if err != nil || parsed != nil {
		t.Fatalf("findItemNFO(one version deleted) = %+v, %v; want no NFO and no error", parsed, err)
	}

	season := filepath.Join(t.TempDir(), "Season 01")
	writeFile(t, filepath.Join(season, "Show S01E01.mkv"), "video")
	writeFile(t, filepath.Join(season, "Show S01E01.nfo"), `<episodedetails><title>Pilot</title></episodedetails>`)
	episodes, err := NewProvider().GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber: 1,
		EpisodeFilePaths: map[int][]string{
			1: {filepath.Join(season, "Show S01E01.mkv")},
			2: {filepath.Join(season, "Show S01E02.mkv")}, // deleted before the scan
		},
	})
	if err != nil || len(episodes) != 1 || episodes[0].Title != "Pilot" {
		t.Fatalf("GetEpisodes(one episode deleted) = %+v, %v; want the other episode and no error", episodes, err)
	}
}

// A folder check looks past a cataloged path that is not a file and finds
// the item's other file; only an error about the share itself stops it.
func TestAnyMediaPresent_LooksPastAPerFileError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "Old.mkv"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	writeFile(t, filepath.Join(dir, "New.mkv"), "video")
	if err := anyMediaPresent(dir, []string{filepath.Join(dir, "Old.mkv"), filepath.Join(dir, "New.mkv")}); err != nil {
		t.Fatalf("anyMediaPresent() = %v, want the present file found", err)
	}
}

// When one version can't be reached for permission and another is on a
// share that is gone, the lookup reports the temporary failure, whatever the
// order of the paths: matching waits for a missing share but goes ahead past
// a permission problem.
func TestFindItemNFO_TemporaryFailureOutranksPermission(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	locked := filepath.Join(t.TempDir(), "Locked")
	writeFile(t, filepath.Join(locked, "Film (2001).mkv"), "video")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	mountpoint := t.TempDir() // the share is gone
	denied, gone := filepath.Join(locked, "Film (2001).mkv"), filepath.Join(mountpoint, "Film (2001).mkv")

	for _, files := range [][]string{{denied, gone}, {gone, denied}} {
		_, parsed, err := NewProvider().newLookup(context.Background()).findItemNFO(files, nil, typeMovie)
		if parsed != nil || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) || errors.Is(err, fs.ErrPermission) {
			t.Errorf("findItemNFO(%v) = %+v, %v; want the temporary failure", files, parsed, err)
		}
	}
}

// A targeted refresh names one episode, but the season's other files still
// show the share is mounted, so a deleted target is skipped, not unreadable.
func TestGetEpisodes_TargetedDeletedEpisodeUsesSeasonFiles(t *testing.T) {
	t.Parallel()

	season := filepath.Join(t.TempDir(), "Season 01")
	writeFile(t, filepath.Join(season, "Show S01E02.mkv"), "video")
	deleted, live := filepath.Join(season, "Show S01E01.mkv"), filepath.Join(season, "Show S01E02.mkv")
	_, err := NewProvider().GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		EpisodeFilePaths: map[int][]string{1: {deleted}},
		ShowFilePaths:    []string{deleted, live},
	})
	if err != nil {
		t.Fatalf("GetEpisodes(deleted target, live sibling) error = %v, want none", err)
	}
	_, err = NewProvider().GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		EpisodeFilePaths: map[int][]string{1: {deleted}},
	})
	if !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("GetEpisodes(deleted target, no siblings known) error = %v, want ErrLocalMetadataUnavailable", err)
	}
}

// The stack index is built once per cached listing and shared by later
// lookups on the same provider.
func TestProvider_StackIndexIsSharedAcrossLookups(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Film-cd1.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Film-cd2.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Film.nfo"), `<movie><title>Film</title></movie>`)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	p := NewProvider()
	first := p.newLookup(context.Background())
	if _, parsed, err := first.findItemNFO([]string{filepath.Join(dir, "Film-cd1.mkv")}, nil, typeMovie); err != nil || parsed == nil {
		t.Fatalf("findItemNFO() = %+v, %v; want Film.nfo", parsed, err)
	}
	second := p.newLookup(context.Background())
	if _, parsed, err := second.findItemNFO([]string{filepath.Join(dir, "Film-cd2.mkv")}, nil, typeMovie); err != nil || parsed == nil {
		t.Fatalf("findItemNFO() = %+v, %v; want Film.nfo", parsed, err)
	}
	if first.listings[dir] != second.listings[dir] || len(first.listings[dir].stacks) == 0 {
		t.Fatalf("lookups did not share one listing with a built stack index")
	}
}

// One present file proves a folder whatever order the files come in, even
// after a file that fails with an I/O-style error (here a symlink loop).
func TestAnyMediaPresent_IndependentOfOrder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	loop := filepath.Join(dir, "Loop.mkv")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	present := filepath.Join(dir, "Film.mkv")
	writeFile(t, present, "video")
	for _, files := range [][]string{{loop, present}, {present, loop}} {
		if err := anyMediaPresent(dir, files); err != nil {
			t.Errorf("anyMediaPresent(%v) = %v, want present", files, err)
		}
	}
	if err := anyMediaPresent(filepath.Join(dir, "gone"), []string{filepath.Join(dir, "gone", "Film.mkv")}); !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Errorf("anyMediaPresent(missing folder) = %v, want ErrLocalMetadataUnavailable", err)
	}
}

// A lookup checks each folder's liveness once, so a season of missing
// episodes does not stat the whole season again for every one of them.
func TestDeletedFromLiveFolder_CheckedOncePerFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gone, later := filepath.Join(dir, "Show S01E01.mkv"), filepath.Join(dir, "Show S01E02.mkv")
	lookup := NewProvider().newLookup(context.Background())
	_, err := os.Stat(gone)
	if lookup.deletedFromLiveFolder(err, gone, []string{gone, later}) {
		t.Fatalf("deletedFromLiveFolder(empty folder) = true, want false")
	}
	writeFile(t, later, "video")
	if lookup.deletedFromLiveFolder(err, gone, []string{gone, later}) {
		t.Fatalf("deletedFromLiveFolder() = true on the second call; want the folder's first answer reused")
	}
	if !NewProvider().newLookup(context.Background()).deletedFromLiveFolder(err, gone, []string{gone, later}) {
		t.Fatalf("deletedFromLiveFolder(new lookup, live sibling) = false, want true")
	}
}

// In a flat show folder the seasons share one directory, so a live file of
// another season proves it is mounted when the targeted season's files are
// all gone.
func TestFlatShowFolderUsesOtherSeasonsAsPresence(t *testing.T) {
	t.Parallel()

	show := t.TempDir()
	writeFile(t, filepath.Join(show, "Show S06E01.mkv"), "video")
	deleted, live := filepath.Join(show, "Show S05E03.mkv"), filepath.Join(show, "Show S06E01.mkv")
	p := NewProvider()
	if _, err := p.GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          typeSeries,
		SeasonDirectoryPaths: map[int][]string{5: {show}},
		ShowFilePaths:        []string{deleted, live},
	}); err != nil {
		t.Fatalf("GetSeasons(season 5 deleted, season 6 live) error = %v, want none", err)
	}
	if _, err := p.GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     5,
		EpisodeFilePaths: map[int][]string{3: {deleted}},
		ShowFilePaths:    []string{deleted, live},
	}); err != nil {
		t.Fatalf("GetEpisodes(season 5 deleted, season 6 live) error = %v, want none", err)
	}
}

// A season folder whose episode can't be reached for permission still has
// its season.nfo read; as everywhere, a readable NFO wins over the error.
func TestGetSeasons_PermissionDeniedEpisodeStillReadsSeasonNFO(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	season := filepath.Join(t.TempDir(), "Season 01")
	writeFile(t, filepath.Join(season, "season.nfo"), `<season><title>First</title></season>`)
	locked := filepath.Join(season, "locked")
	writeFile(t, filepath.Join(locked, "Show S01E01.mkv"), "video")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	seasons, err := NewProvider().GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          typeSeries,
		SeasonDirectoryPaths: map[int][]string{1: {season}},
		ShowFilePaths:        []string{filepath.Join(locked, "Show S01E01.mkv")},
	})
	if err != nil || len(seasons) != 1 || seasons[0].Title != "First" {
		t.Fatalf("GetSeasons() = %+v, %v; want season 1 titled First", seasons, err)
	}
}

// Every file's own NFO name comes before a stacked movie's shared name, so a
// later part's NFO wins over the stack's.
func TestFindItemNFO_BasenamesBeforeStackName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Film-cd1.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Film-cd2.mkv"), "video")
	writeFile(t, filepath.Join(dir, "Film-cd2.nfo"), `<movie><title>Part Two</title></movie>`)
	writeFile(t, filepath.Join(dir, "Film.nfo"), `<movie><title>Stack</title></movie>`)
	parts := []string{filepath.Join(dir, "Film-cd1.mkv"), filepath.Join(dir, "Film-cd2.mkv")}
	_, parsed, err := NewProvider().newLookup(context.Background()).findItemNFO(parts, nil, typeMovie)
	if err != nil || parsed == nil || parsed.Title != "Part Two" {
		t.Fatalf("findItemNFO() = %+v, %v; want the part's own NFO first", parsed, err)
	}
}

// An NFO rewritten in place while it is read is read again; one that keeps
// changing is reported unavailable rather than parsed from a partial read.
// Not parallel: it sets the package's read hook.
func TestReadNFOFile_RewrittenDuringRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.nfo")
	writeFile(t, path, `<movie><title>Old</title></movie>`)

	rewrites := 0
	rewrite := func(limit int) func() {
		return func() {
			if rewrites < limit {
				rewrites++
				later := time.Now().Add(time.Duration(rewrites) * time.Minute)
				writeFile(t, path, fmt.Sprintf(`<movie><title>New %d, a longer title</title></movie>`, rewrites))
				_ = os.Chtimes(path, later, later)
			}
		}
	}
	t.Cleanup(func() { testHookBeforeNFORead = nil })

	testHookBeforeNFORead = rewrite(1)
	data, err := readNFOFile(path)
	if err != nil || !strings.Contains(string(data), "New 1") {
		t.Fatalf("readNFOFile(rewritten once) = %q, %v; want the settled new content", data, err)
	}

	rewrites = 0
	testHookBeforeNFORead = rewrite(2)
	if _, err := readNFOFile(path); !errors.Is(err, errNFOReplaced) {
		t.Fatalf("readNFOFile(still changing) error = %v, want errNFOReplaced", err)
	}
}

// A share that drops while a lookup runs exposes the mountpoint underneath,
// so each lookup re-checks the media afterwards and reports the folder
// unavailable instead of using what it found there. Not parallel: it sets the
// package's read hook.
func TestLookups_RecheckMediaAfterReading(t *testing.T) {
	t.Cleanup(func() { testHookBeforeNFORead = nil })
	dropOnRead := func(media string) {
		testHookBeforeNFORead = func() { _ = os.Remove(media) }
	}

	movieDir := t.TempDir()
	movie := filepath.Join(movieDir, "Film.mkv")
	writeFile(t, movie, "video")
	writeFile(t, filepath.Join(movieDir, "Film.nfo"), `<movie><title>Film</title></movie>`)
	dropOnRead(movie)
	if _, parsed, err := NewProvider().newLookup(context.Background()).findItemNFO([]string{movie}, nil, typeMovie); parsed != nil || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Errorf("findItemNFO(share dropped during the read) = %+v, %v; want ErrLocalMetadataUnavailable", parsed, err)
	}

	season := t.TempDir()
	episode := filepath.Join(season, "Show S01E01.mkv")
	writeFile(t, episode, "video")
	writeFile(t, filepath.Join(season, "season.nfo"), `<season><title>First</title></season>`)
	dropOnRead(episode)
	if seasons, err := NewProvider().GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          typeSeries,
		SeasonDirectoryPaths: map[int][]string{1: {season}},
		ShowFilePaths:        []string{episode},
	}); len(seasons) != 0 || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Errorf("GetSeasons(share dropped during the read) = %+v, %v; want ErrLocalMetadataUnavailable", seasons, err)
	}

	writeFile(t, episode, "video")
	writeFile(t, filepath.Join(season, "Show S01E01.nfo"), `<episodedetails><title>Pilot</title></episodedetails>`)
	dropOnRead(episode)
	if episodes, err := NewProvider().GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		EpisodeFilePaths: map[int][]string{1: {episode}},
	}); len(episodes) != 0 || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Errorf("GetEpisodes(share dropped during the read) = %+v, %v; want ErrLocalMetadataUnavailable", episodes, err)
	}
}

// A same-size rewrite within a coarse timestamp tick leaves the file's size
// and time unchanged, so a recently written file with a coarse time is read
// twice and a difference means it changed. Not parallel: it sets the
// package's read hook.
func TestReadNFOFile_SameSizeRewriteOnCoarseTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.nfo")
	writeFile(t, path, `<movie><title>Old</title></movie>`)
	stamp := time.Now().Truncate(time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	reads := 0
	testHookBeforeNFORead = func() {
		reads++
		if reads == 2 { // between the read and its re-read
			writeFile(t, path, `<movie><title>New</title></movie>`)
			_ = os.Chtimes(path, stamp, stamp)
		}
	}
	t.Cleanup(func() { testHookBeforeNFORead = nil })

	data, err := readNFOFile(path)
	if err != nil || !strings.Contains(string(data), "New") {
		t.Fatalf("readNFOFile(rewritten within the tick) = %q, %v; want the rewritten content", data, err)
	}
}

// Seasons sharing one folder check its media once per call.
func TestParseSeasonSidecar_ChecksSharedFolderOnce(t *testing.T) {
	t.Parallel()

	show := t.TempDir()
	episode := filepath.Join(show, "Show S01E01.mkv")
	lookup := NewProvider().newLookup(context.Background())
	if _, _, err := parseSeasonSidecar(context.Background(), lookup, []string{show}, []string{episode}, 1); !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("parseSeasonSidecar(empty folder) error = %v, want ErrLocalMetadataUnavailable", err)
	}
	writeFile(t, episode, "video")
	if _, _, err := parseSeasonSidecar(context.Background(), lookup, []string{show}, []string{episode}, 2); !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("parseSeasonSidecar(second season, same call) error = %v; want the folder's first answer reused", err)
	}
}

// A change during the re-read of a recently written coarse-timestamp file is
// measured from after that re-read, so one rewrite costs one retry, not an
// error. Not parallel: it sets the package's read hook.
func TestReadNFOFile_ChangeDuringReReadRetriesFromItsState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.nfo")
	writeFile(t, path, `<movie><title>Old</title></movie>`)
	stamp := time.Now().Truncate(time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	reads := 0
	testHookBeforeNFORead = func() {
		reads++
		if reads == 2 { // during the re-read: new size and a new coarse time
			writeFile(t, path, `<movie><title>New, and a longer title</title></movie>`)
			later := stamp.Add(time.Second)
			_ = os.Chtimes(path, later, later)
		}
	}
	t.Cleanup(func() { testHookBeforeNFORead = nil })

	data, err := readNFOFile(path)
	if err != nil || !strings.Contains(string(data), "longer title") {
		t.Fatalf("readNFOFile(rewritten during the re-read) = %q, %v; want the settled new content", data, err)
	}
}
