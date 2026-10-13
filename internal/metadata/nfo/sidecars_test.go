package nfo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

func writeSidecarTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestItemSidecarPathsReturnsTheAppliedFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Film (2020)")
	video := filepath.Join(dir, "Film (2020).mkv")
	writeSidecarTestFile(t, video, "video")
	writeSidecarTestFile(t, filepath.Join(dir, "movie.nfo"), `<movie><title>Film</title></movie>`)
	// A tvshow.nfo beside a movie is not applied, so it is not reported.
	writeSidecarTestFile(t, filepath.Join(dir, "tvshow.nfo"), `<tvshow><title>Show</title></tvshow>`)
	writeSidecarTestFile(t, filepath.Join(dir, "poster.jpg"), "poster")
	writeSidecarTestFile(t, filepath.Join(dir, "Film (2020)-fanart.jpg"), "fanart")
	writeSidecarTestFile(t, filepath.Join(dir, "notes.txt"), "not a sidecar")

	req := metadata.MetadataRequest{
		ContentType:               typeMovie,
		FilePath:                  video,
		RepresentativeFilePath:    video,
		AllGroupFilePaths:         []string{video},
		PrimarySidecarSearchPaths: []string{dir},
	}
	got := NewProvider().ItemSidecarPaths(context.Background(), req)
	if got.MetadataErr != nil {
		t.Fatalf("ItemSidecarPaths: %v", got.MetadataErr)
	}
	slices.Sort(got.Artwork)
	if want := []string{filepath.Join(dir, "movie.nfo")}; !slices.Equal(got.Metadata, want) {
		t.Fatalf("metadata sidecars = %v, want %v", got.Metadata, want)
	}
	if want := []string{filepath.Join(dir, "Film (2020)-fanart.jpg"), filepath.Join(dir, "poster.jpg")}; !slices.Equal(got.Artwork, want) {
		t.Fatalf("artwork sidecars = %v, want %v", got.Artwork, want)
	}

	// <basename>.nfo comes first; while it cannot be read, movie.nfo applies,
	// as it does for GetMetadata.
	writeSidecarTestFile(t, filepath.Join(dir, "Film (2020).nfo"), `<movie><title>Other</title></movie>`)
	denyReads(t, filepath.Join(dir, "Film (2020).nfo"))
	if got := NewProvider().ItemSidecarPaths(context.Background(), req); got.MetadataErr != nil || !slices.Equal(got.Metadata, []string{filepath.Join(dir, "movie.nfo")}) {
		t.Fatalf("ItemSidecarPaths beside an unreadable <basename>.nfo = %+v; want movie.nfo", got)
	}
	if err := os.Chmod(filepath.Join(dir, "Film (2020).nfo"), 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if got := NewProvider().ItemSidecarPaths(context.Background(), req); got.MetadataErr != nil || !slices.Equal(got.Metadata, []string{filepath.Join(dir, "Film (2020).nfo")}) {
		t.Fatalf("ItemSidecarPaths with a readable <basename>.nfo = %+v; want it", got)
	}

	// With no NFO left that can be read, the read error is reported rather
	// than a removed file, and the artwork is still listed.
	if err := os.Remove(filepath.Join(dir, "Film (2020).nfo")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	denyReads(t, filepath.Join(dir, "movie.nfo"))
	got = NewProvider().ItemSidecarPaths(context.Background(), req)
	if !errors.Is(got.MetadataErr, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("ItemSidecarPaths with an unreadable NFO = %+v, want ErrLocalMetadataUnavailable", got)
	}
	if len(got.Artwork) != 2 {
		t.Fatalf("artwork beside an unreadable NFO = %v, want both images", got.Artwork)
	}
}

// denyReads makes path unreadable for the rest of the test.
func denyReads(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("file permissions do not stop root from reading")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
}

func TestSeasonAndEpisodeSidecarPathsReturnTheAppliedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Show")
	season := filepath.Join(root, "Season 01")
	e1 := filepath.Join(season, "Show S01E01.mkv")
	e2 := filepath.Join(season, "Show S01E02.mkv")
	writeSidecarTestFile(t, e1, "video")
	writeSidecarTestFile(t, e2, "video")
	writeSidecarTestFile(t, filepath.Join(season, "season.nfo"), `<season><title>One</title></season>`)
	writeSidecarTestFile(t, filepath.Join(root, "season01-poster.jpg"), "poster")
	writeSidecarTestFile(t, filepath.Join(season, "Show S01E01.nfo"), `<episodedetails><title>Pilot</title></episodedetails>`)
	writeSidecarTestFile(t, filepath.Join(season, "Show S01E01-thumb.jpg"), "thumb")
	provider := NewProvider()
	ctx := context.Background()

	seasonReq := metadata.SeasonsRequest{
		ContentType:          typeSeries,
		SeriesRootPaths:      []string{root},
		SeasonDirectoryPaths: map[int][]string{1: {season}, 2: {filepath.Join(root, "Season 02")}},
	}
	seasons := provider.SeasonSidecarPaths(ctx, seasonReq)
	if got, want := seasons[1], (metadata.LocalSidecars{Metadata: []string{filepath.Join(season, "season.nfo")}, Artwork: []string{filepath.Join(root, "season01-poster.jpg")}}); !slices.Equal(got.Metadata, want.Metadata) || !slices.Equal(got.Artwork, want.Artwork) || got.MetadataErr != nil {
		t.Fatalf("season 1 sidecars = %+v, want %+v", got, want)
	}
	if got, ok := seasons[2]; !ok || len(got.Metadata)+len(got.Artwork) != 0 {
		t.Fatalf("season 2 sidecars = %+v (present %v), want an empty entry", got, ok)
	}

	episodeReq := metadata.EpisodesRequest{
		SeasonNumber:     1,
		SeriesRootPaths:  []string{root},
		EpisodeFilePaths: map[int][]string{1: {e1}, 2: {e2}},
	}
	episodes := provider.EpisodeSidecarPaths(ctx, episodeReq)
	if got := episodes[1]; !slices.Equal(got.Metadata, []string{filepath.Join(season, "Show S01E01.nfo")}) || !slices.Equal(got.Artwork, []string{filepath.Join(season, "Show S01E01-thumb.jpg")}) || got.MetadataErr != nil {
		t.Fatalf("episode 1 sidecars = %+v", got)
	}
	if got, ok := episodes[2]; !ok || len(got.Metadata)+len(got.Artwork) != 0 {
		t.Fatalf("episode 2 sidecars = %+v (present %v), want an empty entry", got, ok)
	}

	denyReads(t, filepath.Join(season, "season.nfo"))
	if got := provider.SeasonSidecarPaths(ctx, seasonReq)[1]; !errors.Is(got.MetadataErr, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("SeasonSidecarPaths with an unreadable season.nfo = %+v, want ErrLocalMetadataUnavailable", got)
	}
	denyReads(t, filepath.Join(season, "Show S01E01.nfo"))
	if got := provider.EpisodeSidecarPaths(ctx, episodeReq)[1]; !errors.Is(got.MetadataErr, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("EpisodeSidecarPaths with an unreadable episode NFO = %+v, want ErrLocalMetadataUnavailable", got)
	}
}

// A scan lists the sidecars this package reads; an extension it reads but a
// scan does not list would never trigger change detection.
func TestSidecarExtensionsCoverNFOReads(t *testing.T) {
	for _, ext := range append([]string{".nfo"}, localArtworkExtensions...) {
		if !scanner.IsLocalMetadataSidecar("file" + ext) {
			t.Errorf("scanner does not list %s sidecars", ext)
		}
	}
}

func TestArtworkFile(t *testing.T) {
	for _, tc := range []struct {
		source, want string
		ok           bool
	}{
		{"/media/Film/poster.jpg", "/media/Film/poster.jpg", true},
		{"file:///media/Film/poster.jpg", "/media/Film/poster.jpg", true},
		{"file:///media/Film/poster.jpg?v=48213-1712345678", "/media/Film/poster.jpg", true},
		{"file:///media/Film/poster.jpg?v=48213--1", "/media/Film/poster.jpg", true},
		{"file:///media/Why?v=1/poster.jpg", "/media/Why?v=1/poster.jpg", true},
		{"https://image.example/poster.jpg", "", false},
		{"", "", false},
	} {
		got, ok := artworkFile(tc.source)
		if got != tc.want || ok != tc.ok {
			t.Errorf("artworkFile(%q) = %q, %v; want %q, %v", tc.source, got, ok, tc.want, tc.ok)
		}
	}
}
