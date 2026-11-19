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

// setMode changes path's mode and restores it when the test ends, so the
// temporary directory can be removed.
func setMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

func movieRequest(dir, file string, sidecarDirs ...string) metadata.MetadataRequest {
	return metadata.MetadataRequest{
		ContentType:               typeMovie,
		FilePath:                  filepath.Join(dir, file),
		AllGroupFilePaths:         []string{filepath.Join(dir, file)},
		PrimarySidecarSearchPaths: sidecarDirs,
	}
}

type findItemNFOPicksCase struct {
	name        string
	files       map[string]string // relative path -> content
	media       []string          // relative; requested, need not exist
	sidecarDirs []string          // relative; "." is the temp dir
	contentType string
	wantPath    string // relative; checked when set
	wantTitle   string // "" means no NFO
	ignoreErr   bool
}

var findItemNFOPicksCases = func() []findItemNFOPicksCase {
	const video = "video"

	return []findItemNFOPicksCase{
		{
			// The scanner groups parts whose separators differ, so the lookup
			// compares stack names the same way and the parts share their NFO.
			name: "stack parts with different separators",
			files: map[string]string{
				"Film-part1-1080p.mkv": video, "Film part2 1080p.mp4": video,
				"Film-1080p.nfo": `<movie><title>Film</title></movie>`,
			},
			media: []string{"Film-part1-1080p.mkv"}, contentType: typeMovie, wantTitle: "Film",
		},
		{
			// Only video files count toward a stack: a subtitle or NFO named
			// like the missing part does not make a lone part a stack.
			name: "stack counts only video parts",
			files: map[string]string{
				"Dune Part 2.mkv": video, "Dune Part 1.srt": "subtitle",
				"Dune Part 1.nfo": `<movie><title>Part One</title></movie>`,
				"Dune.nfo":        `<movie><title>Dune</title></movie>`,
			},
			media: []string{"Dune Part 2.mkv"}, contentType: typeMovie,
		},
		{
			// Every file's own NFO name comes before a stacked movie's shared
			// name, so a later part's NFO wins over the stack's.
			name: "basenames before the stack name",
			files: map[string]string{
				"Film-cd1.mkv": video, "Film-cd2.mkv": video,
				"Film-cd2.nfo": `<movie><title>Part Two</title></movie>`,
				"Film.nfo":     `<movie><title>Stack</title></movie>`,
			},
			media: []string{"Film-cd1.mkv", "Film-cd2.mkv"}, contentType: typeMovie, wantTitle: "Part Two",
		},
		{
			// An initial match carries only the representative part, so the
			// stack is recognized from the folder.
			name: "stack from folder: cd1 with cd2 on disk",
			files: map[string]string{
				"Film-cd1.mkv": video, "Film-cd2.mkv": video,
				"Film.nfo": `<movie><title>Stacked</title></movie>`,
			},
			media: []string{"Film-cd1.mkv"}, contentType: typeMovie, wantTitle: "Stacked",
		},
		{
			name: "stack from folder: two copies of one part are not a stack",
			files: map[string]string{
				"Dune Part 2.mkv": video, "Dune Part 2.mp4": video,
				"Dune.nfo": `<movie><title>Dune</title></movie>`,
			},
			media: []string{"Dune Part 2.mkv", "Dune Part 2.mp4"}, contentType: typeMovie,
		},
		{
			// A cataloged part that is no longer on disk does not make the
			// part that is left a stack.
			name:  "cd1 with cataloged cd2 gone",
			files: map[string]string{"Film-cd1.mkv": video, "Film.nfo": `<movie><title>Film</title></movie>`},
			media: []string{"Film-cd1.mkv"}, contentType: typeMovie,
		},
		{
			name:  "both parts requested, cd2 gone",
			files: map[string]string{"Film-cd1.mkv": video, "Film.nfo": `<movie><title>Film</title></movie>`},
			media: []string{"Film-cd1.mkv", "Film-cd2.mkv"}, contentType: typeMovie, ignoreErr: true,
		},
		{
			// A sidecar directory whose name has a dot ("Mr. Robot") is still
			// a directory: its tvshow.nfo is found, not "<Mr>.nfo".
			name:        "dotted sidecar directory",
			files:       map[string]string{"Mr. Robot/tvshow.nfo": `<tvshow><title>Mr. Robot</title></tvshow>`},
			sidecarDirs: []string{"Mr. Robot"}, contentType: typeSeries,
			wantPath: "Mr. Robot/tvshow.nfo", wantTitle: "Mr. Robot",
		},
		{
			// A stray movie.nfo in a series root must not shadow tvshow.nfo.
			name: "stray movie.nfo in a series root",
			files: map[string]string{
				"movie.nfo":  `<movie><title>Stray</title></movie>`,
				"tvshow.nfo": `<tvshow><title>The Show</title></tvshow>`,
			},
			sidecarDirs: []string{"."}, contentType: typeSeries, wantPath: "tvshow.nfo", wantTitle: "The Show",
		},
		{
			// A candidate that exists but fails to parse falls through to the
			// next candidate instead of masking a valid sidecar.
			name: "parse failure falls through to the next candidate",
			files: map[string]string{
				"Blade Runner (1982).mkv": video,
				"Blade Runner (1982).nfo": "not xml at all",
				"movie.nfo":               `<movie><title>Blade Runner</title></movie>`,
			},
			media: []string{"Blade Runner (1982).mkv"}, sidecarDirs: []string{"."}, contentType: typeMovie,
			wantPath: "movie.nfo", wantTitle: "Blade Runner",
		},
	}
}()

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
		setMode(t, nfoPath, 0)
		assertUnavailable(t, NewProvider(), movieRequest(dir, "Film.mkv", dir))
	})

	t.Run("directory", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "Film (2001)")
		writeFile(t, filepath.Join(dir, "movie.nfo"), `<movie><title>Locked</title></movie>`)
		writeFile(t, filepath.Join(dir, "Film.mkv"), "video")
		setMode(t, dir, 0)
		assertUnavailable(t, NewProvider(), movieRequest(dir, "Film.mkv", dir))
	})

	t.Run("episode", func(t *testing.T) {
		t.Parallel()
		seasonDir := filepath.Join(t.TempDir(), "Season 01")
		episodeNFO := filepath.Join(seasonDir, "Show S01E01.nfo")
		writeFile(t, episodeNFO, `<episodedetails><title>Locked</title></episodedetails>`)
		writeFile(t, filepath.Join(seasonDir, "Show S01E01.mkv"), "video")
		setMode(t, episodeNFO, 0)
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
	setMode(t, locked, 0o311)

	got, parsed, err := NewProvider().newLookup(context.Background()).findItemNFO(
		[]string{filepath.Join(locked, "Movie.mkv"), filepath.Join(root, "b", "Movie.mkv")}, nil, typeMovie)
	if err != nil || parsed == nil || parsed.Title != "From B" {
		t.Fatalf("findItemNFO() = %q, %+v, %v; want b/Movie.nfo", got, parsed, err)
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
		setMode(t, path, 0)
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
	setMode(t, seasonDir, 0)

	_, parsed, err := NewProvider().newLookup(context.Background()).
		findItemNFO([]string{filepath.Join(seasonDir, "Show S01E01.mkv")}, []string{root}, typeSeries)
	if parsed != nil || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("findItemNFO() = %+v, %v; want a permission ErrLocalMetadataUnavailable", parsed, err)
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
	setMode(t, locked, 0)
	mountpoint := t.TempDir() // the share is gone
	denied, gone := filepath.Join(locked, "Film (2001).mkv"), filepath.Join(mountpoint, "Film (2001).mkv")

	for _, files := range [][]string{{denied, gone}, {gone, denied}} {
		_, parsed, err := NewProvider().newLookup(context.Background()).findItemNFO(files, nil, typeMovie)
		if parsed != nil || !errors.Is(err, metadata.ErrLocalMetadataUnavailable) || errors.Is(err, fs.ErrPermission) {
			t.Errorf("findItemNFO(%v) = %+v, %v; want the temporary failure", files, parsed, err)
		}
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
	setMode(t, locked, 0)

	seasons, err := NewProvider().GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          typeSeries,
		SeasonDirectoryPaths: map[int][]string{1: {season}},
		ShowFilePaths:        []string{filepath.Join(locked, "Show S01E01.mkv")},
	})
	if err != nil || len(seasons) != 1 || seasons[0].Title != "First" {
		t.Fatalf("GetSeasons() = %+v, %v; want season 1 titled First", seasons, err)
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
