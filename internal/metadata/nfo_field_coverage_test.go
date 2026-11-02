package metadata_test

// End-to-end coverage for the NFO fields the built-in provider reads beyond
// the basics: show status, sort title and networks on the item, <namedseason>
// and season.nfo dates on seasons, <uniqueid> and <thumb> on episodes, and
// NFO-named artwork ranked between sidecar files and remote providers.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/nfo"
)

// A movie's NFO-named artwork beats a remote provider's for the slots no
// sidecar file fills, local files and URLs alike; a sidecar file still wins
// its slot.
func TestNFONamedArtworkPrecedence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Movies", "Art Film (2001)")
	movie := filepath.Join(dir, "Art Film (2001).mkv")
	writeFixtureFile(t, movie, "video")
	writeFixtureFile(t, filepath.Join(dir, "poster.jpg"), "sidecar-poster")
	writeFixtureFile(t, filepath.Join(dir, "Art Film (2001).nfo"), `<movie>
  <title>Art Film</title>
  <thumb aspect="poster">https://img.example.org/nfo-poster.jpg</thumb>
  <thumb aspect="clearlogo">https://img.example.org/nfo-logo.png</thumb>
  <fanart><thumb>art/backdrop.jpg</thumb></fanart>
</movie>`)
	writeFixtureFile(t, filepath.Join(dir, "art", "backdrop.jpg"), "nfo-backdrop")

	includesText := true
	remote := &remoteEpisodeStubProvider{
		slug:     "tmdb",
		metadata: &metadata.MetadataResult{HasMetadata: true, Title: "Art Film", ProviderIDs: map[string]string{"tmdb": "10"}},
		images: []metadata.RemoteImage{
			{ProviderID: "tmdb", URL: "https://image.tmdb.org/poster.jpg", Type: metadata.ImagePoster, Language: "en", Rating: 9, IncludesText: &includesText},
			{ProviderID: "tmdb", URL: "https://image.tmdb.org/backdrop.jpg", Type: metadata.ImageBackdrop, Rating: 9},
			{ProviderID: "tmdb", URL: "https://image.tmdb.org/logo.png", Type: metadata.ImageLogo, Language: "en", Rating: 9},
		},
	}

	x := metadata.NewNFOSeriesHarness()
	const movieID = "movie:tmdb:10"
	if err := x.SeedMovieSkeleton(movieID, "Art Film"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	_, err := x.Service().ProcessWithProviders(context.Background(), metadata.ProcessRequest{
		ContentID: movieID,
		Hints: &metadata.MatchHints{
			Title:                     "Art Film",
			Type:                      "movie",
			FilePath:                  movie,
			RepresentativeFilePath:    movie,
			AllGroupFilePaths:         []string{movie},
			PrimarySidecarSearchPaths: []string{dir},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider(), remote})
	if err != nil {
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
		t.Errorf("backdrop source = %q, want the NFO-named backdrop %q", item.BackdropSourcePath, want)
	}
	// The remote logo is tagged with the library language, which would win
	// any ranking among remote images.
	if item.LogoSourcePath != "nfo+https://img.example.org/nfo-logo.png" {
		t.Errorf("logo source = %q, want the NFO-named logo over the remote one", item.LogoSourcePath)
	}
	// An NFO-named URL is never served before the image cache copies it.
	if item.LogoPath != "" {
		t.Errorf("logo path = %q, want empty until the cache job lands", item.LogoPath)
	}
	jobs := map[string]bool{}
	for _, job := range x.EnqueuedImageJobs() {
		jobs[job.SourcePath] = true
	}
	if !jobs["nfo+https://img.example.org/nfo-logo.png"] || !jobs[item.BackdropSourcePath] {
		t.Errorf("NFO-named artwork not queued for the image cache: %v", jobs)
	}
}
