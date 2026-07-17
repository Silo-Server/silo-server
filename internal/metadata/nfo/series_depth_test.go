package nfo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// ---------------------------------------------------------------------------
// Parser tables: <season>
// ---------------------------------------------------------------------------

func TestParseSeasonNFO(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		xml           string
		wantTitle     string
		wantOverview  string
		wantSeason    int
		wantSeasonSet bool
	}{
		{
			name:          "full season",
			xml:           `<season><title>Course A: Classic</title><plot>The classic 90-day schedule.</plot><seasonnumber>1</seasonnumber></season>`,
			wantTitle:     "Course A: Classic",
			wantOverview:  "The classic 90-day schedule.",
			wantSeason:    1,
			wantSeasonSet: true,
		},
		{
			name:          "no seasonnumber",
			xml:           `<season><title>Course B</title></season>`,
			wantTitle:     "Course B",
			wantSeasonSet: false,
		},
		{
			name:          "specials season zero",
			xml:           `<season><title>Bonus Workouts</title><seasonnumber>0</seasonnumber></season>`,
			wantTitle:     "Bonus Workouts",
			wantSeason:    0,
			wantSeasonSet: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := parseNFOData([]byte(tc.xml))
			if err != nil {
				t.Fatalf("parseNFOData: %v", err)
			}
			if p.Type != "season" {
				t.Errorf("Type = %q, want season", p.Type)
			}
			if p.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", p.Title, tc.wantTitle)
			}
			if p.Overview != tc.wantOverview {
				t.Errorf("Overview = %q, want %q", p.Overview, tc.wantOverview)
			}
			if p.SeasonSet != tc.wantSeasonSet {
				t.Errorf("SeasonSet = %v, want %v", p.SeasonSet, tc.wantSeasonSet)
			}
			if tc.wantSeasonSet && p.Season != tc.wantSeason {
				t.Errorf("Season = %d, want %d", p.Season, tc.wantSeason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Parser tables: extended <episodedetails>
// ---------------------------------------------------------------------------

func TestParseEpisodeNFO_ExtendedFields(t *testing.T) {
	t.Parallel()

	xml := `<episodedetails>
  <title>Chest and Back</title>
  <plot>Push-ups and pull-ups.</plot>
  <aired>2004-01-01</aired>
  <season>1</season>
  <episode>1</episode>
  <runtime>53</runtime>
  <ratings><rating name="imdb" max="10"><value>8.4</value></rating></ratings>
</episodedetails>`
	p, err := parseNFOData([]byte(xml))
	if err != nil {
		t.Fatalf("parseNFOData: %v", err)
	}
	if p.Type != "episode" {
		t.Errorf("Type = %q, want episode", p.Type)
	}
	if p.Title != "Chest and Back" || p.Overview != "Push-ups and pull-ups." {
		t.Errorf("title/plot = %q/%q", p.Title, p.Overview)
	}
	if p.FirstAirDate != "2004-01-01" {
		t.Errorf("FirstAirDate = %q, want 2004-01-01", p.FirstAirDate)
	}
	if !p.SeasonSet || p.Season != 1 || !p.EpisodeSet || p.Episode != 1 {
		t.Errorf("numbers = season(%v,%d) episode(%v,%d), want set 1/1", p.SeasonSet, p.Season, p.EpisodeSet, p.Episode)
	}
	if p.Runtime != 53 {
		t.Errorf("Runtime = %d, want 53", p.Runtime)
	}
	if p.RatingIMDB != 8.4 {
		t.Errorf("RatingIMDB = %v, want 8.4", p.RatingIMDB)
	}
	if p.MultiEpisode {
		t.Error("MultiEpisode = true for single-episode document")
	}
}

func TestParseEpisodeNFO_LegacyRatingAndNoNumbers(t *testing.T) {
	t.Parallel()

	p, err := parseNFOData([]byte(`<episodedetails><title>Core</title><rating>7.5</rating></episodedetails>`))
	if err != nil {
		t.Fatalf("parseNFOData: %v", err)
	}
	if p.SeasonSet || p.EpisodeSet {
		t.Errorf("SeasonSet/EpisodeSet = %v/%v, want false (numbers absent)", p.SeasonSet, p.EpisodeSet)
	}
	if p.RatingIMDB != 7.5 {
		t.Errorf("legacy RatingIMDB = %v, want 7.5", p.RatingIMDB)
	}
}

// Multi-<episodedetails> documents (multi-episode files) are out of scope in
// v1: the first block wins and the parser flags the document so the provider
// can warn.
func TestParseEpisodeNFO_MultiEpisodeDocumentTakesFirst(t *testing.T) {
	t.Parallel()

	xml := `<episodedetails><title>First Half</title><episode>1</episode></episodedetails>
<episodedetails><title>Second Half</title><episode>2</episode></episodedetails>`
	p, err := parseNFOData([]byte(xml))
	if err != nil {
		t.Fatalf("parseNFOData: %v", err)
	}
	if p.Title != "First Half" {
		t.Errorf("Title = %q, want First Half (take first)", p.Title)
	}
	if !p.MultiEpisode {
		t.Error("MultiEpisode = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Provider: GetSeasons
// ---------------------------------------------------------------------------

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func TestGetSeasons_SeasonNFOAndPoster(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	s2 := filepath.Join(root, "Season 02")
	writeTestFile(t, filepath.Join(s1, "season.nfo"),
		`<season><title>Course A: Classic</title><plot>Classic plan.</plot><seasonnumber>1</seasonnumber></season>`)
	writeTestFile(t, filepath.Join(s1, "poster.jpg"), "img")
	writeTestFile(t, filepath.Join(s2, "season.nfo"), `<season><title>Course B</title></season>`)
	// Season 2 poster lives at the series root using the seasonNN-poster form.
	writeTestFile(t, filepath.Join(root, "season02-poster.jpg"), "img")

	p := NewProvider()
	seasons, err := p.GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:     "series",
		SeriesRootPaths: []string{root},
		SeasonDirectoryPaths: map[int][]string{
			1: {s1},
			2: {s2},
		},
	})
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	if len(seasons) != 2 {
		t.Fatalf("len(seasons) = %d, want 2 (%#v)", len(seasons), seasons)
	}
	if seasons[0].SeasonNumber != 1 || seasons[0].Title != "Course A: Classic" || seasons[0].Overview != "Classic plan." {
		t.Errorf("season 1 = %#v", seasons[0])
	}
	if want := "file://" + filepath.Join(s1, "poster.jpg"); seasons[0].PosterPath != want {
		t.Errorf("season 1 poster = %q, want %q", seasons[0].PosterPath, want)
	}
	if seasons[1].SeasonNumber != 2 || seasons[1].Title != "Course B" {
		t.Errorf("season 2 = %#v", seasons[1])
	}
	if want := "file://" + filepath.Join(root, "season02-poster.jpg"); seasons[1].PosterPath != want {
		t.Errorf("season 2 poster = %q, want %q", seasons[1].PosterPath, want)
	}
}

// A mismatched <seasonnumber> is advisory: naming owns structure, so the
// directory-derived season number wins.
func TestGetSeasons_NumberConflictPrefersDirectoryNumber(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s2 := filepath.Join(root, "Season 02")
	writeTestFile(t, filepath.Join(s2, "season.nfo"),
		`<season><title>Course B</title><seasonnumber>7</seasonnumber></season>`)

	p := NewProvider()
	seasons, err := p.GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          "series",
		SeriesRootPaths:      []string{root},
		SeasonDirectoryPaths: map[int][]string{2: {s2}},
	})
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	if len(seasons) != 1 || seasons[0].SeasonNumber != 2 || seasons[0].Title != "Course B" {
		t.Fatalf("seasons = %#v, want season 2 titled Course B", seasons)
	}
}

func TestGetSeasons_NoSidecarsReturnsNothing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	if err := os.MkdirAll(s1, 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewProvider()
	seasons, err := p.GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          "series",
		SeriesRootPaths:      []string{root},
		SeasonDirectoryPaths: map[int][]string{1: {s1}},
	})
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	if len(seasons) != 0 {
		t.Fatalf("seasons = %#v, want none", seasons)
	}
}

func TestGetSeasons_NonSeriesContentTypeReturnsNothing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	writeTestFile(t, filepath.Join(s1, "season.nfo"), `<season><title>Nope</title></season>`)
	p := NewProvider()
	seasons, err := p.GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          "movie",
		SeasonDirectoryPaths: map[int][]string{1: {s1}},
	})
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	if len(seasons) != 0 {
		t.Fatalf("seasons = %#v, want none for non-series content", seasons)
	}
}

// ---------------------------------------------------------------------------
// Provider: GetEpisodes
// ---------------------------------------------------------------------------

func TestGetEpisodes_EpisodeNFOAndThumb(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	e1 := filepath.Join(s1, "P90X S01E01 - Chest and Back.mkv")
	writeTestFile(t, e1, "video")
	writeTestFile(t, filepath.Join(s1, "P90X S01E01 - Chest and Back.nfo"),
		`<episodedetails><title>Chest and Back</title><plot>Push and pull.</plot><aired>2004-01-01</aired><season>1</season><episode>1</episode><runtime>53</runtime></episodedetails>`)
	writeTestFile(t, filepath.Join(s1, "P90X S01E01 - Chest and Back-thumb.jpg"), "img")
	// Episode 2 has a media file but no NFO and no thumb: the provider must
	// return nothing for it (synthesized fallback stays in charge).
	e2 := filepath.Join(s1, "P90X S01E02 - Plyometrics.mkv")
	writeTestFile(t, e2, "video")

	p := NewProvider()
	episodes, err := p.GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:    1,
		SeriesRootPaths: []string{root},
		EpisodeFilePaths: map[int][]string{
			1: {e1},
			2: {e2},
		},
	})
	if err != nil {
		t.Fatalf("GetEpisodes: %v", err)
	}
	if len(episodes) != 1 {
		t.Fatalf("len(episodes) = %d, want 1 (%#v)", len(episodes), episodes)
	}
	ep := episodes[0]
	if ep.SeasonNumber != 1 || ep.EpisodeNumber != 1 {
		t.Errorf("numbers = S%dE%d, want S1E1", ep.SeasonNumber, ep.EpisodeNumber)
	}
	if ep.Title != "Chest and Back" || ep.Overview != "Push and pull." {
		t.Errorf("title/plot = %q/%q", ep.Title, ep.Overview)
	}
	if ep.AirDate != "2004-01-01" {
		t.Errorf("AirDate = %q, want 2004-01-01", ep.AirDate)
	}
	if ep.Runtime != 53 {
		t.Errorf("Runtime = %d, want 53", ep.Runtime)
	}
	if want := "file://" + filepath.Join(s1, "P90X S01E01 - Chest and Back-thumb.jpg"); ep.StillPath != want {
		t.Errorf("StillPath = %q, want %q", ep.StillPath, want)
	}
}

// NFO numbers are advisory: the filename-parsed SxxEyy (the request key) wins
// on conflict.
func TestGetEpisodes_NumberConflictFilenameWins(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	e3 := filepath.Join(s1, "Show S01E03.mkv")
	writeTestFile(t, e3, "video")
	writeTestFile(t, filepath.Join(s1, "Show S01E03.nfo"),
		`<episodedetails><title>Third</title><season>4</season><episode>9</episode></episodedetails>`)

	p := NewProvider()
	episodes, err := p.GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		EpisodeFilePaths: map[int][]string{3: {e3}},
	})
	if err != nil {
		t.Fatalf("GetEpisodes: %v", err)
	}
	if len(episodes) != 1 || episodes[0].SeasonNumber != 1 || episodes[0].EpisodeNumber != 3 {
		t.Fatalf("episodes = %#v, want S1E3 from filename", episodes)
	}
	if episodes[0].Title != "Third" {
		t.Errorf("Title = %q, want Third", episodes[0].Title)
	}
}

// A non-episode NFO next to the media file (e.g. a stray movie.nfo-shaped
// basename sidecar) must not inject data into an episode.
func TestGetEpisodes_RootTypeMismatchIgnored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	e1 := filepath.Join(s1, "Show S01E01.mkv")
	writeTestFile(t, e1, "video")
	writeTestFile(t, filepath.Join(s1, "Show S01E01.nfo"), `<movie><title>Not an episode</title></movie>`)

	p := NewProvider()
	episodes, err := p.GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		EpisodeFilePaths: map[int][]string{1: {e1}},
	})
	if err != nil {
		t.Fatalf("GetEpisodes: %v", err)
	}
	if len(episodes) != 0 {
		t.Fatalf("episodes = %#v, want none for mismatched root type", episodes)
	}
}
