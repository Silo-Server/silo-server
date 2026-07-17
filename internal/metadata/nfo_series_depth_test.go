package metadata_test

// Phase D integration tests: an NFO-only series library scans into a fully
// presented tree using the real built-in NFO provider — series metadata and
// root art (Phases A+C), season names/posters from season.nfo, and episode
// titles/plots/thumbs from <basename>.nfo (Phase D). These live in an
// external test package so they can import internal/metadata/nfo (which
// imports internal/metadata) without an import cycle; the in-package
// fakes are exposed through NFOSeriesHarness (export_nfo_test.go).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/nfo"
)

func writeFixtureFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// buildFitnessFixture lays out the P90X fitness library from the Phase D spec
// and returns (seriesRoot, episode file paths).
func buildFitnessFixture(t *testing.T) (string, []string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Fitness", "P90X")
	writeFixtureFile(t, filepath.Join(root, "tvshow.nfo"),
		`<tvshow><title>P90X</title><plot>90-day home fitness program.</plot></tvshow>`)
	writeFixtureFile(t, filepath.Join(root, "poster.jpg"), "series-poster")
	writeFixtureFile(t, filepath.Join(root, "fanart.jpg"), "series-fanart")

	s1 := filepath.Join(root, "Season 01")
	writeFixtureFile(t, filepath.Join(s1, "season.nfo"),
		`<season><title>Course A: Classic</title><plot>The classic schedule.</plot><seasonnumber>1</seasonnumber></season>`)
	writeFixtureFile(t, filepath.Join(s1, "poster.jpg"), "s1-poster")
	e1 := filepath.Join(s1, "P90X S01E01 - Chest and Back.mkv")
	writeFixtureFile(t, e1, "video")
	writeFixtureFile(t, filepath.Join(s1, "P90X S01E01 - Chest and Back.nfo"),
		`<episodedetails><title>Chest and Back</title><plot>Push and pull.</plot><season>1</season><episode>1</episode></episodedetails>`)
	writeFixtureFile(t, filepath.Join(s1, "P90X S01E01 - Chest and Back-thumb.jpg"), "e1-thumb")

	s2 := filepath.Join(root, "Season 02")
	writeFixtureFile(t, filepath.Join(s2, "season.nfo"),
		`<season><title>Course B</title><seasonnumber>2</seasonnumber></season>`)
	writeFixtureFile(t, filepath.Join(s2, "poster.jpg"), "s2-poster")
	e2 := filepath.Join(s2, "P90X S02E01 - Core.mkv")
	writeFixtureFile(t, e2, "video")
	writeFixtureFile(t, filepath.Join(s2, "P90X S02E01 - Core.nfo"),
		`<episodedetails><title>Core</title><plot>Core synergistics.</plot></episodedetails>`)
	writeFixtureFile(t, filepath.Join(s2, "P90X S02E01 - Core-thumb.jpg"), "e2-thumb")

	return root, []string{e1, e2}
}

// The headline Phase D acceptance case: an NFO-only fitness library with zero
// remote IDs anywhere scans into a matched show under a local: content id,
// seasons carrying the NFO names and local posters, episodes carrying NFO
// titles/plots and thumbs, and every image enqueued as a file:// local source.
func TestNFOOnlyFitnessLibrary_FullSeriesDepth(t *testing.T) {
	root, episodeFiles := buildFitnessFixture(t)

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const seriesID = "local-p90x"
	if err := x.SeedSeriesSkeleton(seriesID, "P90X"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}

	result, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID,
		Hints: &metadata.MatchHints{
			Title:                     "P90X",
			Type:                      "series",
			FilePath:                  episodeFiles[0],
			RepresentativeFilePath:    episodeFiles[0],
			AllGroupFilePaths:         episodeFiles,
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

	// Show: matched under the local: content id with NFO metadata, no remote IDs.
	item, err := x.Item(seriesID)
	if err != nil {
		t.Fatalf("item not found under local id: %v", err)
	}
	if item.Status != "matched" {
		t.Errorf("item status = %q, want matched", item.Status)
	}
	if item.Title != "P90X" || item.Overview != "90-day home fitness program." {
		t.Errorf("item title/overview = %q/%q", item.Title, item.Overview)
	}
	if item.TmdbID != "" || item.TvdbID != "" || item.ImdbID != "" {
		t.Errorf("NFO-only series must carry no remote ids, got tmdb=%q tvdb=%q imdb=%q",
			item.TmdbID, item.TvdbID, item.ImdbID)
	}
	if want := "file://" + filepath.Join(root, "poster.jpg"); item.PosterSourcePath != want {
		t.Errorf("item PosterSourcePath = %q, want %q", item.PosterSourcePath, want)
	}
	if want := "file://" + filepath.Join(root, "fanart.jpg"); item.BackdropSourcePath != want {
		t.Errorf("item BackdropSourcePath = %q, want %q", item.BackdropSourcePath, want)
	}

	// Seasons: NFO names and local posters.
	seasons := x.Seasons(seriesID)
	if len(seasons) != 2 {
		t.Fatalf("len(seasons) = %d, want 2 (%#v)", len(seasons), seasons)
	}
	if seasons[0].SeasonNumber != 1 || seasons[0].Title != "Course A: Classic" {
		t.Errorf("season 1 = number %d title %q, want 1 / Course A: Classic",
			seasons[0].SeasonNumber, seasons[0].Title)
	}
	if seasons[0].Overview != "The classic schedule." {
		t.Errorf("season 1 overview = %q", seasons[0].Overview)
	}
	if want := "file://" + filepath.Join(root, "Season 01", "poster.jpg"); seasons[0].PosterSourcePath != want {
		t.Errorf("season 1 PosterSourcePath = %q, want %q", seasons[0].PosterSourcePath, want)
	}
	if seasons[0].PosterPath != "" {
		t.Errorf("season 1 PosterPath = %q, want empty until the cache job lands", seasons[0].PosterPath)
	}
	if seasons[1].SeasonNumber != 2 || seasons[1].Title != "Course B" {
		t.Errorf("season 2 = number %d title %q, want 2 / Course B",
			seasons[1].SeasonNumber, seasons[1].Title)
	}
	if want := "file://" + filepath.Join(root, "Season 02", "poster.jpg"); seasons[1].PosterSourcePath != want {
		t.Errorf("season 2 PosterSourcePath = %q, want %q", seasons[1].PosterSourcePath, want)
	}

	// Episodes: NFO titles/plots and thumbs.
	episodes := x.Episodes(seriesID)
	if len(episodes) != 2 {
		t.Fatalf("len(episodes) = %d, want 2 (%#v)", len(episodes), episodes)
	}
	if episodes[0].SeasonNumber != 1 || episodes[0].EpisodeNumber != 1 ||
		episodes[0].Title != "Chest and Back" || episodes[0].Overview != "Push and pull." {
		t.Errorf("episode S1E1 = %#v", episodes[0])
	}
	if want := "file://" + filepath.Join(root, "Season 01", "P90X S01E01 - Chest and Back-thumb.jpg"); episodes[0].StillSourcePath != want {
		t.Errorf("episode S1E1 StillSourcePath = %q, want %q", episodes[0].StillSourcePath, want)
	}
	if episodes[1].SeasonNumber != 2 || episodes[1].EpisodeNumber != 1 || episodes[1].Title != "Core" {
		t.Errorf("episode S2E1 = %#v", episodes[1])
	}

	// Every image is enqueued as a file:// local source.
	jobs := x.EnqueuedImageJobs()
	wantSources := map[string]string{
		"file://" + filepath.Join(root, "poster.jpg"):                                          metadata.ImageCacheTargetItem,
		"file://" + filepath.Join(root, "fanart.jpg"):                                          metadata.ImageCacheTargetItem,
		"file://" + filepath.Join(root, "Season 01", "poster.jpg"):                             metadata.ImageCacheTargetSeason,
		"file://" + filepath.Join(root, "Season 02", "poster.jpg"):                             metadata.ImageCacheTargetSeason,
		"file://" + filepath.Join(root, "Season 01", "P90X S01E01 - Chest and Back-thumb.jpg"): metadata.ImageCacheTargetEpisode,
		"file://" + filepath.Join(root, "Season 02", "P90X S02E01 - Core-thumb.jpg"):           metadata.ImageCacheTargetEpisode,
	}
	got := make(map[string]string, len(jobs))
	for _, job := range jobs {
		got[job.SourcePath] = job.TargetType
		if !strings.HasPrefix(job.SourcePath, "file://") {
			t.Errorf("enqueued non-local source %q", job.SourcePath)
		}
		if job.ProviderID != "local" {
			t.Errorf("job for %q attributed to provider %q, want local", job.SourcePath, job.ProviderID)
		}
	}
	for source, target := range wantSources {
		if got[source] != target {
			t.Errorf("image job for %q = target %q, want %q (all jobs: %#v)", source, got[source], target, got)
		}
	}
}

// remoteEpisodeStubProvider is a canned remote series provider with
// season/episode support, standing in for TVDB in the mixed case.
type remoteEpisodeStubProvider struct {
	mu       sync.Mutex
	slug     string
	metadata *metadata.MetadataResult
	seasons  []metadata.SeasonResult
	episodes map[int][]metadata.EpisodeResult
}

func (p *remoteEpisodeStubProvider) Slug() string       { return p.slug }
func (p *remoteEpisodeStubProvider) Name() string       { return p.slug }
func (p *remoteEpisodeStubProvider) ForTypes() []string { return []string{"movie", "series"} }

func (p *remoteEpisodeStubProvider) GetMetadata(_ context.Context, _ metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.metadata != nil {
		cp := *p.metadata
		return &cp, nil
	}
	return &metadata.MetadataResult{}, nil
}

func (p *remoteEpisodeStubProvider) GetSeasons(_ context.Context, _ metadata.SeasonsRequest) ([]metadata.SeasonResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]metadata.SeasonResult, len(p.seasons))
	copy(out, p.seasons)
	return out, nil
}

func (p *remoteEpisodeStubProvider) GetEpisodes(_ context.Context, req metadata.EpisodesRequest) ([]metadata.EpisodeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]metadata.EpisodeResult, len(p.episodes[req.SeasonNumber]))
	copy(out, p.episodes[req.SeasonNumber])
	return out, nil
}

// Mixed case: a real remote show with a curated season.nfo. With the NFO
// provider at chain priority 1, the local season name wins and the remote
// provider backfills everything the NFO does not supply (air date, episodes).
func TestMixedRemoteShow_CuratedSeasonNFOWins(t *testing.T) {
	root := filepath.Join(t.TempDir(), "TV", "Real Show")
	s1 := filepath.Join(root, "Season 01")
	writeFixtureFile(t, filepath.Join(s1, "season.nfo"),
		`<season><title>The Curated Season</title></season>`)
	e1 := filepath.Join(s1, "Real Show S01E01.mkv")
	writeFixtureFile(t, e1, "video")

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const seriesID = "series:tvdb:81189"
	if err := x.SeedSeriesSkeleton(seriesID, "Real Show"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}

	remote := &remoteEpisodeStubProvider{
		slug:     "tvdb",
		metadata: &metadata.MetadataResult{HasMetadata: true, Title: "Real Show", Overview: "From TVDB."},
		seasons: []metadata.SeasonResult{
			{SeasonNumber: 1, Title: "Season 1", Overview: "Remote season overview.", AirDate: "2010-01-01"},
		},
		episodes: map[int][]metadata.EpisodeResult{
			1: {{SeasonNumber: 1, EpisodeNumber: 1, Title: "Remote Episode", Overview: "From TVDB."}},
		},
	}

	// NFO first in the chain = chain priority 1.
	result, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID,
		Hints: &metadata.MatchHints{
			Title:                     "Real Show",
			Type:                      "series",
			FilePath:                  e1,
			RepresentativeFilePath:    e1,
			AllGroupFilePaths:         []string{e1},
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

	seasons := x.Seasons(seriesID)
	if len(seasons) != 1 {
		t.Fatalf("len(seasons) = %d, want 1 (%#v)", len(seasons), seasons)
	}
	if seasons[0].Title != "The Curated Season" {
		t.Errorf("season title = %q, want The Curated Season (NFO at priority 1 wins)", seasons[0].Title)
	}
	if seasons[0].Overview != "Remote season overview." {
		t.Errorf("season overview = %q, want remote backfill", seasons[0].Overview)
	}
	if seasons[0].AirDate == nil {
		t.Error("season air date not backfilled from remote provider")
	}
	episodes := x.Episodes(seriesID)
	if len(episodes) != 1 || episodes[0].Title != "Remote Episode" {
		t.Fatalf("episodes = %#v, want the remote episode (no episode NFO present)", episodes)
	}
}

// Season NFO edits propagate on manual refresh only: a scheduled refresh
// fills empty fields but never re-applies an edited season.nfo over an
// existing name; a manual refresh replaces unlocked fields with the merged
// provider results, NFO first (same contract as item fields).
func TestSeasonNFOEdit_ManualRefreshOnlyPropagation(t *testing.T) {
	root, episodeFiles := buildFitnessFixture(t)

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const seriesID = "local-p90x"
	if err := x.SeedSeriesSkeleton(seriesID, "P90X"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}
	for i, path := range episodeFiles {
		x.SeedSeriesFile(100+i, 10, path, root, seriesID)
	}

	if _, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID,
		Hints: &metadata.MatchHints{
			Title:                     "P90X",
			Type:                      "series",
			FilePath:                  episodeFiles[0],
			RepresentativeFilePath:    episodeFiles[0],
			AllGroupFilePaths:         episodeFiles,
			PrimarySidecarSearchPaths: []string{root},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider()}); err != nil {
		t.Fatalf("initial match: %v", err)
	}
	if seasons := x.Seasons(seriesID); len(seasons) == 0 || seasons[0].Title != "Course A: Classic" {
		t.Fatalf("initial seasons = %#v, want Course A: Classic first", seasons)
	}

	// Edit the season NFO after the initial match.
	writeFixtureFile(t, filepath.Join(root, "Season 01", "season.nfo"),
		`<season><title>Course A: Renamed</title><seasonnumber>1</seasonnumber></season>`)

	// Scheduled refresh: fill-empty only, the edit must NOT propagate.
	if _, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID,
		Language:  "en",
		Mode:      metadata.ModeScheduledRefresh,
	}, []metadata.Provider{nfo.NewProvider()}); err != nil {
		t.Fatalf("scheduled refresh: %v", err)
	}
	if seasons := x.Seasons(seriesID); seasons[0].Title != "Course A: Classic" {
		t.Errorf("after scheduled refresh season title = %q, want unchanged Course A: Classic", seasons[0].Title)
	}

	// Manual refresh: the edit propagates.
	if _, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID,
		Language:  "en",
		Mode:      metadata.ModeManualRefresh,
	}, []metadata.Provider{nfo.NewProvider()}); err != nil {
		t.Fatalf("manual refresh: %v", err)
	}
	if seasons := x.Seasons(seriesID); seasons[0].Title != "Course A: Renamed" {
		t.Errorf("after manual refresh season title = %q, want Course A: Renamed", seasons[0].Title)
	}
}

// Regression: a series with no season/episode NFOs must behave exactly as
// before — the provider contributes nothing at season/episode depth and the
// synthesized fallback path stays in charge.
func TestSeriesWithoutSeasonOrEpisodeNFOs_UnchangedFallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "TV", "Plain Show")
	s1 := filepath.Join(root, "Season 01")
	e1 := filepath.Join(s1, "Plain Show S01E01.mkv")
	writeFixtureFile(t, e1, "video")

	x := metadata.NewNFOSeriesHarness()
	ctx := context.Background()
	const seriesID = "local-plain"
	if err := x.SeedSeriesSkeleton(seriesID, "Plain Show"); err != nil {
		t.Fatalf("seed skeleton: %v", err)
	}

	result, err := x.Service().ProcessWithProviders(ctx, metadata.ProcessRequest{
		ContentID: seriesID,
		Hints: &metadata.MatchHints{
			Title:                     "Plain Show",
			Type:                      "series",
			FilePath:                  e1,
			RepresentativeFilePath:    e1,
			AllGroupFilePaths:         []string{e1},
			PrimarySidecarSearchPaths: []string{root},
		},
		Language: "en",
		Mode:     metadata.ModeInitialMatch,
	}, []metadata.Provider{nfo.NewProvider()})
	if err != nil {
		t.Fatalf("ProcessWithProviders: %v", err)
	}
	// With no NFOs at all the provider finds nothing: the pipeline reports
	// no update (the worker then synthesizes fallback structure), exactly as
	// before Phase D.
	if result == nil || result.Updated {
		t.Fatalf("result = %#v, want Updated=false when the provider finds nothing", result)
	}
	if seasons := x.Seasons(seriesID); len(seasons) != 0 {
		t.Errorf("seasons = %#v, want none from the provider path (fallback synthesis owns them)", seasons)
	}
	if episodes := x.Episodes(seriesID); len(episodes) != 0 {
		t.Errorf("episodes = %#v, want none from the provider path", episodes)
	}
	if jobs := x.EnqueuedImageJobs(); len(jobs) != 0 {
		t.Errorf("image jobs = %#v, want none without sidecar art", jobs)
	}
}
