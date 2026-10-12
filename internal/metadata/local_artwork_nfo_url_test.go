package metadata_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/nfo"
)

// With Keep provider artwork off, an image URL an NFO names is not used: only
// the image cache's download may fetch it. The slot takes the remote
// provider's artwork, served directly, while local files, a sidecar or one the
// NFO names, still win their slots.
func TestNFONamedURLNeedsImageCaching(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Movies", "Art Film (2001)")
	movie := filepath.Join(dir, "Art Film (2001).mkv")
	writeFixtureFile(t, movie, "video")
	writeFixtureFile(t, filepath.Join(dir, "poster.jpg"), "sidecar-poster")
	writeFixtureFile(t, filepath.Join(dir, "Art Film (2001).nfo"), `<movie>
  <title>Art Film</title>
  <thumb aspect="clearlogo">https://img.example.org/nfo-logo.png</thumb>
  <fanart><thumb>art/backdrop.jpg</thumb></fanart>
</movie>`)
	writeFixtureFile(t, filepath.Join(dir, "art", "backdrop.jpg"), "nfo-backdrop")
	remote := &remoteEpisodeStubProvider{
		slug:     "tmdb",
		metadata: &metadata.MetadataResult{HasMetadata: true, Title: "Art Film", ProviderIDs: map[string]string{"tmdb": "10"}},
		images: []metadata.RemoteImage{
			{ProviderID: "tmdb", URL: "https://image.tmdb.org/logo.png", Type: metadata.ImageLogo, Language: "en", Rating: 9},
		},
	}

	x := metadata.NewNFOSeriesHarness()
	x.Service().SetAutoCacheImages(false)
	const movieID = "movie:tmdb:10"
	if err := x.SeedMovieSkeleton(movieID, "Art Film"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	if _, err := x.Service().ProcessWithProviders(context.Background(), metadata.ProcessRequest{
		ContentID: movieID,
		Hints: &metadata.MatchHints{
			Title: "Art Film", Type: "movie", FilePath: movie, RepresentativeFilePath: movie,
			AllGroupFilePaths: []string{movie}, PrimarySidecarSearchPaths: []string{dir},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider(), remote}); err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	item, err := x.Item(movieID)
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	if want := filepath.Join(dir, "poster.jpg"); localSourceFile(t, item.PosterSourcePath) != want {
		t.Errorf("poster source = %q, want the sidecar %q", item.PosterSourcePath, want)
	}
	if want := filepath.Join(dir, "art", "backdrop.jpg"); localSourceFile(t, item.BackdropSourcePath) != want {
		t.Errorf("backdrop source = %q, want the NFO-named file %q", item.BackdropSourcePath, want)
	}
	if item.LogoSourcePath != "https://image.tmdb.org/logo.png" || item.LogoPath != "https://image.tmdb.org/logo.png" {
		t.Errorf("logo = path %q source %q, want the provider's logo served directly", item.LogoPath, item.LogoSourcePath)
	}
	for _, job := range x.EnqueuedImageJobs() {
		if job.SourcePath == "nfo+https://img.example.org/nfo-logo.png" {
			t.Errorf("queued the NFO-named URL with caching off: %+v", job)
		}
	}
}

// A local re-apply that picks up a replaced poster hands it to the
// discovering server's copier, as a refresh does, rather than only queueing it
// for whichever drain runs first.
func TestLocalReapplyCopiesReplacedPosterOnDiscovery(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.x.RecordLocalArtworkCopies()
	f.scan(t)
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2", tmdbID("100")))
	f.scan(t)

	writeNewerFile(t, filepath.Join(f.dir, "poster.jpg"), "poster-v2, a different size")
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after replacing poster.jpg re-applied %d items, want 1", n)
	}
	source := f.item(t).PosterSourcePath
	for _, in := range f.x.LocalArtworkCopies() {
		if in.SourcePath == source {
			return
		}
	}
	t.Fatalf("copier got %+v, want the replaced poster %q", f.x.LocalArtworkCopies(), source)
}

// A local re-apply with caching off leaves out an image URL the edited NFO
// names, as a refresh does, since only the image cache could fetch it.
func TestLocalReapplySkipsNFONamedURLWithCachingOff(t *testing.T) {
	f := newMovieFixture(t, "100")
	f.x.Service().SetAutoCacheImages(false)
	f.scan(t)
	writeNewerFile(t, filepath.Join(f.dir, "movie.nfo"), movieNFO("Film NFO v2", "Plot v2",
		tmdbID("100")+`<thumb aspect="clearlogo">https://img.example.org/nfo-logo.png</thumb>`))
	if n := f.scan(t); n != 1 {
		t.Fatalf("check after editing movie.nfo re-applied %d items, want 1", n)
	}
	if got := f.item(t).LogoSourcePath; got != "" {
		t.Fatalf("logo source = %q, want no NFO-named URL with caching off", got)
	}
}
