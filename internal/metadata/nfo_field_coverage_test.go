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

func TestNFOSeriesFieldsPersist(t *testing.T) {
	root := filepath.Join(t.TempDir(), "TV", "Field Show")
	writeFixtureFile(t, filepath.Join(root, "tvshow.nfo"), `<tvshow>
  <title>Field Show</title>
  <sorttitle>Show, Field</sorttitle>
  <status>Ended</status>
  <studio>Network A / Network B</studio>
  <namedseason number="1">Named One</namedseason>
  <namedseason number="2">Named Two</namedseason>
  <thumb aspect="poster" type="season" season="2">art/season02.jpg</thumb>
</tvshow>`)
	writeFixtureFile(t, filepath.Join(root, "art", "season02.jpg"), "season-2-poster")
	s1 := filepath.Join(root, "Season 01")
	writeFixtureFile(t, filepath.Join(s1, "season.nfo"),
		`<season><title>Season File Title</title><premiered>2010-01-05</premiered></season>`)
	e1 := filepath.Join(s1, "Field Show S01E01.mkv")
	writeFixtureFile(t, e1, "video")
	writeFixtureFile(t, filepath.Join(s1, "Field Show S01E01.nfo"), `<episodedetails>
  <title>Pilot</title>
  <releasedate>2010-01-05</releasedate>
  <uniqueid type="tvdb" default="true">4001</uniqueid>
  <uniqueid type="tmdb">5001</uniqueid>
  <uniqueid type="imdb">tt7000001</uniqueid>
  <thumb>stills/pilot.jpg</thumb>
</episodedetails>`)
	writeFixtureFile(t, filepath.Join(s1, "stills", "pilot.jpg"), "pilot-still")
	s2 := filepath.Join(root, "Season 02")
	e2 := filepath.Join(s2, "Field Show S02E01.mkv")
	writeFixtureFile(t, e2, "video")

	x := metadata.NewNFOSeriesHarness()
	const seriesID = "local-field-show"
	if err := x.SeedSeriesSkeleton(seriesID, "Field Show"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	_, err := x.Service().ProcessWithProviders(context.Background(), metadata.ProcessRequest{
		ContentID: seriesID,
		Hints: &metadata.MatchHints{
			Title:                     "Field Show",
			Type:                      "series",
			FilePath:                  e1,
			RepresentativeFilePath:    e1,
			AllGroupFilePaths:         []string{e1, e2},
			PrimarySidecarSearchPaths: []string{root},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider()})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}

	item, err := x.Item(seriesID)
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	if item.SortTitle != "Show, Field" || item.ShowStatus != "ended" {
		t.Errorf("sort title / status = %q / %q", item.SortTitle, item.ShowStatus)
	}
	if len(item.Networks) != 2 || item.Networks[0] != "Network A" || item.Networks[1] != "Network B" {
		t.Errorf("Networks = %#v", item.Networks)
	}

	seasons := x.Seasons(seriesID)
	if len(seasons) != 2 {
		t.Fatalf("seasons = %#v, want 2", seasons)
	}
	// season.nfo outranks the show's <namedseason> for season 1.
	if seasons[0].Title != "Season File Title" || seasons[0].AirDate == nil || seasons[0].AirDate.Format("2006-01-02") != "2010-01-05" {
		t.Errorf("season 1 = %q / %v", seasons[0].Title, seasons[0].AirDate)
	}
	if seasons[1].Title != "Named Two" {
		t.Errorf("season 2 title = %q, want the <namedseason> title", seasons[1].Title)
	}
	if want := filepath.Join(root, "art", "season02.jpg"); localSourceFile(t, seasons[1].PosterSourcePath) != want {
		t.Errorf("season 2 poster source = %q, want %q", seasons[1].PosterSourcePath, want)
	}

	episodes := x.Episodes(seriesID)
	if len(episodes) == 0 || episodes[0].SeasonNumber != 1 || episodes[0].EpisodeNumber != 1 {
		t.Fatalf("episodes = %#v", episodes)
	}
	pilot := episodes[0]
	if pilot.TvdbID != "4001" || pilot.TmdbID != "5001" || pilot.ImdbID != "tt7000001" {
		t.Errorf("pilot ids tvdb=%q tmdb=%q imdb=%q, want the NFO's <uniqueid>s", pilot.TvdbID, pilot.TmdbID, pilot.ImdbID)
	}
	if pilot.AirDate == nil || pilot.AirDate.Format("2006-01-02") != "2010-01-05" {
		t.Errorf("pilot air date = %v, want the <releasedate>", pilot.AirDate)
	}
	if want := filepath.Join(s1, "stills", "pilot.jpg"); localSourceFile(t, pilot.StillSourcePath) != want {
		t.Errorf("pilot still source = %q, want %q", pilot.StillSourcePath, want)
	}
}

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
