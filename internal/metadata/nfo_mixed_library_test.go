package metadata_test

// Mixed sports library tests (plan §2a win 3): one library of type "mixed"
// holding WWE pay-per-view events as movies alongside "WWE SmackDown" as a
// show, metadata driven mainly by NFO (sports content often has no TVDB/TMDB
// presence). The classification contract: naming decides movie-vs-series per
// file before any metadata provider runs; NFOs supply metadata and identity,
// never type (the provider's ContentType guard ignores a tvshow.nfo next to
// a movie-classified file). These tests reuse the NFOSeriesHarness bridge
// and the real built-in NFO provider, mirroring the fitness-library tests.

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/nfo"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// ---------------------------------------------------------------------------
// (a) Classification pin: naming decides type in a mixed library.
// ---------------------------------------------------------------------------

// Sports-flavored classification cases for the mixed default branch
// (internal/naming/filename.go): SxxEyy/season-folder routes to the series
// lane, everything else to the movie lane. NFO contents never participate.
func TestMixedLibrary_NamingDecidesType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		path        string
		wantType    string
		wantSeason  int
		wantEpisode int
	}{
		{
			name:     "PPV event in its own movie folder",
			path:     "/sports/WWE/WrestleMania 41 (2025)/WrestleMania 41 (2025).mkv",
			wantType: "movie",
		},
		{
			name:     "bare PPV event file defaults to the movie lane",
			path:     "/sports/WWE/Royal Rumble 2025.mkv",
			wantType: "movie",
		},
		{
			name:        "weekly show with season folder and SxxEyy",
			path:        "/sports/WWE/WWE SmackDown/Season 27/WWE SmackDown S27E15.mkv",
			wantType:    "series",
			wantSeason:  27,
			wantEpisode: 15,
		},
		{
			name:        "SxxEyy without a season folder still routes to series",
			path:        "/sports/WWE/WWE SmackDown/WWE SmackDown S27E15.mkv",
			wantType:    "series",
			wantSeason:  27,
			wantEpisode: 15,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := naming.ResolvePathContext(tc.path, "mixed")
			if ctx.Type != tc.wantType {
				t.Fatalf("ResolvePathContext(%q, mixed).Type = %q, want %q", tc.path, ctx.Type, tc.wantType)
			}
			if ctx.SeasonNum != tc.wantSeason || ctx.EpisodeNum != tc.wantEpisode {
				t.Errorf("season/episode = S%dE%d, want S%dE%d",
					ctx.SeasonNum, ctx.EpisodeNum, tc.wantSeason, tc.wantEpisode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// (b) Movie lane: an NFO-only PPV event.
// ---------------------------------------------------------------------------

func TestMixedLibrary_MovieLane_NFOOnlyPPV(t *testing.T) {
	root := filepath.Join(t.TempDir(), "WWE", "WrestleMania 41 (2025)")
	movieFile := filepath.Join(root, "WrestleMania 41 (2025).mkv")
	writeFixtureFile(t, movieFile, "video")
	writeFixtureFile(t, filepath.Join(root, "movie.nfo"),
		`<movie><title>WrestleMania 41</title><plot>Two nights from Las Vegas.</plot><year>2025</year></movie>`)
	writeFixtureFile(t, filepath.Join(root, "poster.jpg"), "ppv-poster")

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const movieID = "local-wm41"
	if err := x.SeedMovieSkeleton(movieID, "WrestleMania 41"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}

	result, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: movieID,
		Hints: &metadata.MatchHints{
			Title:                     "WrestleMania 41",
			Year:                      2025,
			Type:                      "movie",
			FilePath:                  movieFile,
			RepresentativeFilePath:    movieFile,
			AllGroupFilePaths:         []string{movieFile},
			PrimarySidecarSearchPaths: []string{root},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider()})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	item, err := x.Item(movieID)
	if err != nil {
		t.Fatalf("PPV movie not found under local id: %v", err)
	}
	if item.Status != "matched" {
		t.Errorf("item status = %q, want matched", item.Status)
	}
	if item.Type != "movie" {
		t.Errorf("item type = %q, want movie (NFO must not flip type)", item.Type)
	}
	if item.Title != "WrestleMania 41" || item.Overview != "Two nights from Las Vegas." {
		t.Errorf("item title/overview = %q/%q", item.Title, item.Overview)
	}
	if item.TmdbID != "" || item.TvdbID != "" || item.ImdbID != "" {
		t.Errorf("title-only PPV must carry no remote ids, got tmdb=%q tvdb=%q imdb=%q",
			item.TmdbID, item.TvdbID, item.ImdbID)
	}
	if want := "file://" + filepath.Join(root, "poster.jpg"); item.PosterSourcePath != want {
		t.Errorf("item PosterSourcePath = %q, want %q", item.PosterSourcePath, want)
	}

	jobs := x.EnqueuedImageJobs()
	found := false
	for _, job := range jobs {
		if job.SourcePath == "file://"+filepath.Join(root, "poster.jpg") {
			found = true
			if job.TargetType != metadata.ImageCacheTargetItem || job.ProviderID != "local" {
				t.Errorf("poster job = target %q provider %q, want item/local", job.TargetType, job.ProviderID)
			}
		}
		if !strings.HasPrefix(job.SourcePath, "file://") {
			t.Errorf("enqueued non-local source %q", job.SourcePath)
		}
	}
	if !found {
		t.Errorf("PPV poster not enqueued as a file:// local source (jobs: %#v)", jobs)
	}
}

// ---------------------------------------------------------------------------
// (c) Series lane: the weekly show next to the PPVs, full NFO depth.
// ---------------------------------------------------------------------------

func TestMixedLibrary_SeriesLane_WeeklyShow(t *testing.T) {
	root := filepath.Join(t.TempDir(), "WWE", "WWE SmackDown")
	writeFixtureFile(t, filepath.Join(root, "tvshow.nfo"),
		`<tvshow><title>WWE SmackDown</title><plot>Friday night wrestling.</plot></tvshow>`)
	s27 := filepath.Join(root, "Season 27")
	writeFixtureFile(t, filepath.Join(s27, "season.nfo"),
		`<season><title>SmackDown 2025</title><seasonnumber>27</seasonnumber></season>`)
	writeFixtureFile(t, filepath.Join(s27, "poster.jpg"), "s27-poster")
	episodeFile := filepath.Join(s27, "WWE SmackDown S27E15.mkv")
	writeFixtureFile(t, episodeFile, "video")
	writeFixtureFile(t, filepath.Join(s27, "WWE SmackDown S27E15.nfo"),
		`<episodedetails><title>SmackDown: April 11, 2025</title><plot>Go-home show before Mania.</plot></episodedetails>`)
	writeFixtureFile(t, filepath.Join(s27, "WWE SmackDown S27E15-thumb.jpg"), "e15-thumb")

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const seriesID = "local-smackdown"
	if err := x.SeedSeriesSkeleton(seriesID, "WWE SmackDown"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}

	result, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID,
		Hints: &metadata.MatchHints{
			Title:                     "WWE SmackDown",
			Type:                      "series",
			FilePath:                  episodeFile,
			RepresentativeFilePath:    episodeFile,
			AllGroupFilePaths:         []string{episodeFile},
			PrimarySidecarSearchPaths: []string{root},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider()})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}

	item, err := x.Item(seriesID)
	if err != nil {
		t.Fatalf("show not found under local id: %v", err)
	}
	if item.Type != "series" || item.Title != "WWE SmackDown" {
		t.Errorf("item = type %q title %q, want series / WWE SmackDown", item.Type, item.Title)
	}

	seasons := x.Seasons(seriesID)
	if len(seasons) != 1 || seasons[0].SeasonNumber != 27 || seasons[0].Title != "SmackDown 2025" {
		t.Fatalf("seasons = %#v, want season 27 titled SmackDown 2025", seasons)
	}
	if want := "file://" + filepath.Join(s27, "poster.jpg"); seasons[0].PosterSourcePath != want {
		t.Errorf("season PosterSourcePath = %q, want %q", seasons[0].PosterSourcePath, want)
	}

	episodes := x.Episodes(seriesID)
	if len(episodes) != 1 || episodes[0].SeasonNumber != 27 || episodes[0].EpisodeNumber != 15 {
		t.Fatalf("episodes = %#v, want S27E15", episodes)
	}
	if episodes[0].Title != "SmackDown: April 11, 2025" || episodes[0].Overview != "Go-home show before Mania." {
		t.Errorf("episode title/plot = %q/%q", episodes[0].Title, episodes[0].Overview)
	}
	if want := "file://" + filepath.Join(s27, "WWE SmackDown S27E15-thumb.jpg"); episodes[0].StillSourcePath != want {
		t.Errorf("episode StillSourcePath = %q, want %q", episodes[0].StillSourcePath, want)
	}
}

// ---------------------------------------------------------------------------
// (d) Cross-type guard: a tvshow.nfo next to a movie-classified file.
// ---------------------------------------------------------------------------

// A tvshow.nfo adjacent to a movie-classified PPV file must be ignored by
// the ContentType guard: no series metadata is injected into the movie, and
// with nothing else to match against the item stays an unmatched skeleton
// (pipeline-level mirror of the Phase B provider guard tests).
func TestMixedLibrary_TVShowNFONextToMovieIgnored(t *testing.T) {
	root := filepath.Join(t.TempDir(), "WWE", "WrestleMania 41 (2025)")
	movieFile := filepath.Join(root, "WrestleMania 41 (2025).mkv")
	writeFixtureFile(t, movieFile, "video")
	// Only a series-shaped NFO — e.g. an export tool misplaced it.
	writeFixtureFile(t, filepath.Join(root, "tvshow.nfo"),
		`<tvshow><title>WWE SmackDown</title><plot>Wrong sidecar.</plot></tvshow>`)

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const movieID = "local-wm41-guard"
	if err := x.SeedMovieSkeleton(movieID, "WrestleMania 41"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}

	result, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: movieID,
		Hints: &metadata.MatchHints{
			Title:                     "WrestleMania 41",
			Year:                      2025,
			Type:                      "movie",
			FilePath:                  movieFile,
			RepresentativeFilePath:    movieFile,
			AllGroupFilePaths:         []string{movieFile},
			PrimarySidecarSearchPaths: []string{root},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider()})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result != nil && result.Updated {
		t.Fatalf("result = %#v, want Updated=false (mismatched NFO must not produce a match)", result)
	}

	item, err := x.Item(movieID)
	if err != nil {
		t.Fatalf("load skeleton: %v", err)
	}
	if item.Type != "movie" {
		t.Errorf("item type = %q, want movie (tvshow.nfo must not flip type)", item.Type)
	}
	if item.Title != "WrestleMania 41" {
		t.Errorf("item title = %q, want untouched WrestleMania 41 (no series title injection)", item.Title)
	}
	if item.Overview != "" {
		t.Errorf("item overview = %q, want empty (series plot must not be injected)", item.Overview)
	}
	if item.Status != "pending_match" {
		t.Errorf("item status = %q, want pending_match (skeleton unchanged)", item.Status)
	}
}

// ---------------------------------------------------------------------------
// (e) Mixed identity: an ID-bearing PPV next to NFO-only ones.
// ---------------------------------------------------------------------------

// remoteMovieStubProvider is a canned remote movie provider that records the
// provider IDs it was asked to fetch metadata for (Phase A harness shape).
type remoteMovieStubProvider struct {
	mu          sync.Mutex
	slug        string
	metadata    *metadata.MetadataResult
	lastMetaIDs map[string]string
}

func (p *remoteMovieStubProvider) Slug() string       { return p.slug }
func (p *remoteMovieStubProvider) Name() string       { return p.slug }
func (p *remoteMovieStubProvider) ForTypes() []string { return []string{"movie", "series"} }

func (p *remoteMovieStubProvider) Search(_ context.Context, _ metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return nil, nil // remote search knows nothing about obscure sports events
}

func (p *remoteMovieStubProvider) GetMetadata(_ context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastMetaIDs = make(map[string]string, len(req.ProviderIDs))
	for k, v := range req.ProviderIDs {
		p.lastMetaIDs[k] = v
	}
	if p.metadata != nil {
		cp := *p.metadata
		return &cp, nil
	}
	return &metadata.MetadataResult{}, nil
}

func (p *remoteMovieStubProvider) lastMetadataIDs() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastMetaIDs
}

// A PPV whose movie.nfo carries a tmdb <uniqueid> anchors a remote match via
// the trusted-hint phase even when remote search-by-title finds nothing —
// remote-capable events enrich fully while NFO-only events (case b) fall
// back locally, side by side in the same mixed library.
func TestMixedLibrary_PPVWithUniqueIDAnchorsRemoteMatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "WWE", "WrestleMania 41 (2025)")
	movieFile := filepath.Join(root, "WrestleMania 41 (2025).mkv")
	writeFixtureFile(t, movieFile, "video")
	writeFixtureFile(t, filepath.Join(root, "movie.nfo"),
		`<movie><title>WrestleMania 41</title><year>2025</year><uniqueid type="tmdb">424242</uniqueid></movie>`)

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()

	remote := &remoteMovieStubProvider{
		slug: "tmdb",
		metadata: &metadata.MetadataResult{
			HasMetadata: true,
			Title:       "WrestleMania 41",
			Year:        2025,
			Overview:    "From TMDB.",
			ProviderIDs: map[string]string{"tmdb": "424242"},
		},
	}

	result, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		Hints: &metadata.MatchHints{
			Title:                     "WrestleMania 41",
			Year:                      2025,
			Type:                      "movie",
			FilePath:                  movieFile,
			RepresentativeFilePath:    movieFile,
			AllGroupFilePaths:         []string{movieFile},
			PrimarySidecarSearchPaths: []string{root},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider(), remote})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	if result == nil || !result.Updated {
		t.Fatalf("result = %#v, want Updated=true", result)
	}
	if got := remote.lastMetadataIDs()["tmdb"]; got != "424242" {
		t.Errorf("remote GetMetadata tmdb id = %q, want 424242 (NFO uniqueid must anchor identity)", got)
	}

	item, err := x.Item(result.ContentID)
	if err != nil {
		t.Fatalf("load matched item: %v", err)
	}
	if item.TmdbID != "424242" {
		t.Errorf("item tmdb id = %q, want 424242", item.TmdbID)
	}
	if item.Overview != "From TMDB." {
		t.Errorf("item overview = %q, want remote enrichment applied", item.Overview)
	}
}
