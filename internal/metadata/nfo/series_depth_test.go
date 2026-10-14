package nfo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestParseEpisodeNFO_LegacyRatingIgnoredBesideRatingsBlock(t *testing.T) {
	t.Parallel()

	p, err := parseNFOData([]byte(`<episodedetails><title>Core</title>` +
		`<ratings><rating name="tmdb" max="10" default="true"><value>8.1</value></rating></ratings>` +
		`<rating>8.1</rating></episodedetails>`))
	if err != nil {
		t.Fatalf("parseNFOData: %v", err)
	}
	if p.RatingIMDB != 0 {
		t.Errorf("RatingIMDB = %v, want 0 (bare rating repeats the tmdb default)", p.RatingIMDB)
	}
	if p.RatingTMDB != 8.1 {
		t.Errorf("RatingTMDB = %v, want 8.1", p.RatingTMDB)
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
	writeTestFile(t, filepath.Join(s1, "Show S01E01.mkv"), "video")
	writeTestFile(t, filepath.Join(s2, "season.nfo"), `<season><title>Course B</title></season>`)
	writeTestFile(t, filepath.Join(s2, "Show S02E01.mkv"), "video")
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
	if want := filepath.Join(s1, "poster.jpg"); sidecarPath(t, seasons[0].PosterPath) != want {
		t.Errorf("season 1 poster = %q, want %q", seasons[0].PosterPath, want)
	}
	if seasons[1].SeasonNumber != 2 || seasons[1].Title != "Course B" {
		t.Errorf("season 2 = %#v", seasons[1])
	}
	if want := filepath.Join(root, "season02-poster.jpg"); sidecarPath(t, seasons[1].PosterPath) != want {
		t.Errorf("season 2 poster = %q, want %q", seasons[1].PosterPath, want)
	}
}

// A mismatched <seasonnumber> is advisory: naming owns structure, so the
// directory-derived season number wins.
func TestGetSeasons_NumberConflictPrefersDirectoryNumber(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s2 := filepath.Join(root, "Season 02")
	writeTestFile(t, filepath.Join(s2, "Show S02E01.mkv"), "video")
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
	if err := os.WriteFile(filepath.Join(s1, "Show S01E01.mkv"), []byte("video"), 0o644); err != nil {
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

func TestGetSeasons_PosterNamesIgnoreCase(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	s2 := filepath.Join(root, "Season 02")
	specials := filepath.Join(root, "Specials")
	writeTestFile(t, filepath.Join(s1, "Folder.JPG"), "img")
	writeTestFile(t, filepath.Join(s2, "episode.mkv"), "video")
	writeTestFile(t, filepath.Join(root, "Season02-Poster.PNG"), "img")
	writeTestFile(t, filepath.Join(specials, "episode.mkv"), "video")
	writeTestFile(t, filepath.Join(root, "Season-Specials-Poster.jpg"), "img")

	p := NewProvider()
	seasons, err := p.GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:     "series",
		SeriesRootPaths: []string{root},
		SeasonDirectoryPaths: map[int][]string{
			0: {specials},
			1: {s1},
			2: {s2},
		},
	})
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	want := map[int]string{
		0: filepath.Join(root, "Season-Specials-Poster.jpg"),
		1: filepath.Join(s1, "Folder.JPG"),
		2: filepath.Join(root, "Season02-Poster.PNG"),
	}
	if len(seasons) != len(want) {
		t.Fatalf("seasons = %#v, want %d", seasons, len(want))
	}
	for _, season := range seasons {
		if got := sidecarPath(t, season.PosterPath); got != want[season.SeasonNumber] {
			t.Errorf("season %d poster = %q, want %q", season.SeasonNumber, got, want[season.SeasonNumber])
		}
	}
}

// In a flat layout the episodes sit in the series root, so the root's
// poster.jpg is the series poster and must not become every season's poster.
// The root's seasonNN-poster still applies.
func TestGetSeasons_FlatLayoutKeepsSeriesPosterOffSeasons(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "Show S01E01.mkv"), "video")
	writeTestFile(t, filepath.Join(root, "Show S02E01.mkv"), "video")
	writeTestFile(t, filepath.Join(root, "poster.jpg"), "img")
	writeTestFile(t, filepath.Join(root, "season02-poster.jpg"), "img")

	p := NewProvider()
	seasons, err := p.GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          "series",
		SeriesRootPaths:      []string{root},
		SeasonDirectoryPaths: map[int][]string{1: {root}, 2: {root}},
	})
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	if len(seasons) != 1 || seasons[0].SeasonNumber != 2 {
		t.Fatalf("seasons = %#v, want only season 2 from season02-poster.jpg", seasons)
	}
	if got := sidecarPath(t, seasons[0].PosterPath); got != filepath.Join(root, "season02-poster.jpg") {
		t.Fatalf("season 2 poster = %q", got)
	}
}

// Season folder art does not depend on the series root being known: a show
// whose root is not a validated sidecar location still finds it.
func TestGetSeasons_SeasonFolderPosterWithoutSeriesRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	writeTestFile(t, filepath.Join(s1, "Cover.jpg"), "img")

	p := NewProvider()
	seasons, err := p.GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          "series",
		SeasonDirectoryPaths: map[int][]string{1: {s1}},
	})
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	if len(seasons) != 1 || sidecarPath(t, seasons[0].PosterPath) != filepath.Join(s1, "Cover.jpg") {
		t.Fatalf("seasons = %#v, want Season 01/Cover.jpg", seasons)
	}
}

// ---------------------------------------------------------------------------
// Provider: GetEpisodes
// ---------------------------------------------------------------------------

func TestGetEpisodes_ThumbnailNames(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	e1 := filepath.Join(s1, "Show S01E01.mkv")
	e2 := filepath.Join(s1, "Show S01E02.mkv")
	e3 := filepath.Join(s1, "Show S01E03.mkv")
	for _, path := range []string{e1, e2, e3} {
		writeTestFile(t, path, "video")
	}
	// -thumb beats the bare basename image; either name matches ignoring case.
	writeTestFile(t, filepath.Join(s1, "Show S01E01-thumb.jpg"), "img")
	writeTestFile(t, filepath.Join(s1, "Show S01E01.jpg"), "img")
	writeTestFile(t, filepath.Join(s1, "Show S01E02.PNG"), "img")
	writeTestFile(t, filepath.Join(s1, "show s01e03-THUMB.jpg"), "img")

	p := NewProvider()
	episodes, err := p.GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		SeriesRootPaths:  []string{root},
		EpisodeFilePaths: map[int][]string{1: {e1}, 2: {e2}, 3: {e3}},
	})
	if err != nil {
		t.Fatalf("GetEpisodes: %v", err)
	}
	want := map[int]string{
		1: filepath.Join(s1, "Show S01E01-thumb.jpg"),
		2: filepath.Join(s1, "Show S01E02.PNG"),
		3: filepath.Join(s1, "show s01e03-THUMB.jpg"),
	}
	if len(episodes) != len(want) {
		t.Fatalf("episodes = %#v, want %d", episodes, len(want))
	}
	for _, ep := range episodes {
		if got := sidecarPath(t, ep.StillPath); got != want[ep.EpisodeNumber] {
			t.Errorf("episode %d still = %q, want %q", ep.EpisodeNumber, got, want[ep.EpisodeNumber])
		}
	}
}

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
	if want := filepath.Join(s1, "P90X S01E01 - Chest and Back-thumb.jpg"); sidecarPath(t, ep.StillPath) != want {
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

// A tvshow.nfo's <namedseason> titles a season that has no season.nfo title,
// and its <thumb season="N"> posters a season with no poster file. A
// season.nfo title and a poster file win over them.
func TestGetSeasons_ShowNFONamesAndPostersSeasons(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	s2 := filepath.Join(root, "Season 02")
	s3 := filepath.Join(root, "Season 03")
	writeTestFile(t, filepath.Join(root, "tvshow.nfo"), `<tvshow>
  <title>Show</title>
  <namedseason number="1">Named One</namedseason>
  <namedseason number="2">Named Two</namedseason>
  <thumb aspect="poster" type="season" season="1">https://img.example.org/s1.jpg</thumb>
  <thumb aspect="poster" type="season" season="2">art/season02.jpg</thumb>
  <thumb aspect="banner" type="season" season="3">art/banner03.jpg</thumb>
</tvshow>`)
	writeTestFile(t, filepath.Join(root, "art", "season02.jpg"), "img")
	writeTestFile(t, filepath.Join(root, "art", "banner03.jpg"), "img")
	writeTestFile(t, filepath.Join(s1, "season.nfo"),
		`<season><title>Season NFO Title</title><premiered>2001-01-01</premiered></season>`)
	writeTestFile(t, filepath.Join(s1, "poster.jpg"), "img")
	for i, dir := range []string{s1, s2, s3} {
		writeTestFile(t, filepath.Join(dir, fmt.Sprintf("Show S%02dE01.mkv", i+1)), "video")
	}

	seasons, err := NewProvider().GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          "series",
		SeriesRootPaths:      []string{root},
		SeasonDirectoryPaths: map[int][]string{1: {s1}, 2: {s2}, 3: {s3}},
	})
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	want := []metadata.SeasonResult{
		{SeasonNumber: 1, Title: "Season NFO Title", AirDate: "2001-01-01", PosterPath: filepath.Join(s1, "poster.jpg")},
		{SeasonNumber: 2, Title: "Named Two", PosterPath: filepath.Join(root, "art", "season02.jpg")},
	}
	for i := range seasons {
		seasons[i].PosterPath = artSourcePath(t, seasons[i].PosterPath)
	}
	if !reflect.DeepEqual(seasons, want) {
		t.Fatalf("seasons = %#v\nwant %#v", seasons, want)
	}
}

// Without a poster file, the show's NFO poster URL for the season applies.
func TestGetSeasons_ShowNFOSeasonPosterURL(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	writeTestFile(t, filepath.Join(root, "tvshow.nfo"),
		`<tvshow><title>Show</title><thumb aspect="poster" season="1">https://img.example.org/s1.jpg</thumb></tvshow>`)
	writeTestFile(t, filepath.Join(s1, "Show S01E01.mkv"), "video")
	seasons, err := NewProvider().GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          "series",
		SeriesRootPaths:      []string{root},
		SeasonDirectoryPaths: map[int][]string{1: {s1}},
	})
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	if len(seasons) != 1 || seasons[0].PosterPath != "nfo+https://img.example.org/s1.jpg" || seasons[0].Title != "" {
		t.Fatalf("seasons = %#v, want season 1 with the NFO poster URL", seasons)
	}
}

// An episode NFO's <uniqueid>s come back as the episode's provider ids, and
// its <thumb> stills an episode that has no thumb file.
func TestGetEpisodes_ProviderIDsAndNFOThumb(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	e1 := filepath.Join(s1, "Show S01E01.mkv")
	e2 := filepath.Join(s1, "Show S01E02.mkv")
	e3 := filepath.Join(s1, "Show S01E03.mkv")
	writeTestFile(t, e1, "video")
	writeTestFile(t, e2, "video")
	writeTestFile(t, e3, "video")
	writeTestFile(t, filepath.Join(s1, "Show S01E01.nfo"), `<episodedetails>
  <title>One</title><premiered>2020-02-02</premiered>
  <uniqueid type="tvdb" default="true">7654321</uniqueid>
  <uniqueid type="tmdb">998877</uniqueid>
  <uniqueid type="imdb">tt1234567</uniqueid>
  <thumb>stills/one.jpg</thumb>
</episodedetails>`)
	writeTestFile(t, filepath.Join(s1, "stills", "one.jpg"), "img")
	writeTestFile(t, filepath.Join(s1, "Show S01E02.nfo"),
		`<episodedetails><title>Two</title><thumb>https://img.example.org/two.jpg</thumb></episodedetails>`)
	writeTestFile(t, filepath.Join(s1, "Show S01E02-thumb.jpg"), "img")
	writeTestFile(t, filepath.Join(s1, "Show S01E03.nfo"),
		`<episodedetails><title>Three</title><thumb>https://img.example.org/three.jpg</thumb></episodedetails>`)

	episodes, err := NewProvider().GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		SeriesRootPaths:  []string{root},
		EpisodeFilePaths: map[int][]string{1: {e1}, 2: {e2}, 3: {e3}},
	})
	if err != nil {
		t.Fatalf("GetEpisodes: %v", err)
	}
	if len(episodes) != 3 {
		t.Fatalf("episodes = %#v, want 3", episodes)
	}
	if want := map[string]string{"tvdb": "7654321", "tmdb": "998877", "imdb": "tt1234567"}; !reflect.DeepEqual(episodes[0].ProviderIDs, want) {
		t.Errorf("episode 1 ProviderIDs = %#v, want %#v", episodes[0].ProviderIDs, want)
	}
	if episodes[0].AirDate != "2020-02-02" {
		t.Errorf("episode 1 AirDate = %q, want the premiered date", episodes[0].AirDate)
	}
	if want := filepath.Join(s1, "stills", "one.jpg"); artSourcePath(t, episodes[0].StillPath) != want {
		t.Errorf("episode 1 StillPath = %q, want %q", episodes[0].StillPath, want)
	}
	if want := filepath.Join(s1, "Show S01E02-thumb.jpg"); artSourcePath(t, episodes[1].StillPath) != want {
		t.Errorf("episode 2 StillPath = %q, want the thumb file %q", episodes[1].StillPath, want)
	}
	if episodes[1].ProviderIDs != nil {
		t.Errorf("episode 2 ProviderIDs = %#v, want nil", episodes[1].ProviderIDs)
	}
	if episodes[2].StillPath != "nfo+https://img.example.org/three.jpg" {
		t.Errorf("episode 3 StillPath = %q, want the NFO thumb URL", episodes[2].StillPath)
	}
}

// An episode NFO's ids reach only the episode it describes: not the second
// episode of a multi-episode file, not an episode whose numbers disagree with
// the NFO's, and never a placeholder zero.
func TestEpisodeProviderIDsNameOneEpisode(t *testing.T) {
	t.Parallel()

	parse := func(data string) *parsedNFO {
		t.Helper()
		p, err := parseNFOData([]byte(data))
		if err != nil {
			t.Fatalf("parseNFOData: %v", err)
		}
		return p
	}
	one := parse(`<episodedetails><season>1</season><episode>2</episode>
<uniqueid type="tvdb">4001</uniqueid><uniqueid type="tmdb">0</uniqueid><uniqueid type="imdb">tt1234567</uniqueid></episodedetails>`)
	if got := episodeProviderIDs("Show S01E02.nfo", one, 1, 2); !reflect.DeepEqual(got, map[string]string{"tvdb": "4001", "imdb": "tt1234567"}) {
		t.Errorf("matching episode ids = %#v", got)
	}
	if got := episodeProviderIDs("Show S01E02.nfo", one, 1, 3); got != nil {
		t.Errorf("mismatched episode ids = %#v, want none", got)
	}
	undeclared := parse(`<episodedetails><uniqueid type="tvdb">4001</uniqueid></episodedetails>`)
	if got := episodeProviderIDs("Show S05E09.nfo", undeclared, 5, 9); got["tvdb"] != "4001" {
		t.Errorf("ids without declared numbers = %#v, want the tvdb id", got)
	}
	multi := parse(`<episodedetails><season>1</season><episode>1</episode><uniqueid type="tvdb">4001</uniqueid></episodedetails>
<episodedetails><season>1</season><episode>2</episode><uniqueid type="tvdb">4002</uniqueid></episodedetails>`)
	if got := episodeProviderIDs("Show S01E01.nfo", multi, 1, 1); got != nil {
		t.Errorf("multi-episode file ids = %#v, want none", got)
	}
	zero := parse(`<episodedetails><uniqueid type="tvdb">000</uniqueid></episodedetails>`)
	if got := episodeProviderIDs("Show S01E01.nfo", zero, 1, 1); got != nil {
		t.Errorf("placeholder ids = %#v, want none", got)
	}
}

// One NFO beside a multi-episode video describes every episode in it, so its
// ids name none of them in particular.
func TestGetEpisodes_SharedNFODropsProviderIDs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	video := filepath.Join(s1, "Show S01E01-E02.mkv")
	writeTestFile(t, video, "video")
	writeTestFile(t, filepath.Join(s1, "Show S01E01-E02.nfo"),
		`<episodedetails><title>Double</title><uniqueid type="tvdb">4001</uniqueid></episodedetails>`)

	episodes, err := NewProvider().GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		SeriesRootPaths:  []string{root},
		EpisodeFilePaths: map[int][]string{1: {video}, 2: {video}},
	})
	if err != nil {
		t.Fatalf("GetEpisodes: %v", err)
	}
	if len(episodes) != 2 {
		t.Fatalf("episodes = %#v, want 2", episodes)
	}
	for _, episode := range episodes {
		if episode.Title != "Double" || episode.ProviderIDs != nil {
			t.Errorf("episode %d = %q with ids %#v, want the title and no ids", episode.EpisodeNumber, episode.Title, episode.ProviderIDs)
		}
	}

	// A targeted refresh asks about one of the episodes; the file still
	// serves both.
	targeted, err := NewProvider().GetEpisodes(context.Background(), metadata.EpisodesRequest{
		SeasonNumber:     1,
		SeriesRootPaths:  []string{root},
		EpisodeFilePaths: map[int][]string{2: {video}},
	})
	if err != nil {
		t.Fatalf("GetEpisodes(targeted): %v", err)
	}
	if len(targeted) != 1 || targeted[0].Title != "Double" || targeted[0].ProviderIDs != nil {
		t.Fatalf("targeted episodes = %#v, want episode 2 with no ids", targeted)
	}
}

// A tvshow.nfo that exists but cannot be read may name the seasons, so
// GetSeasons reports it unavailable: a season refresh that replaces stored
// values stops instead of taking remote names over them.
func TestGetSeasons_UnreadableShowNFOReportsUnavailable(t *testing.T) {
	t.Parallel()
	skipIfPermissionsIgnored(t)

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	writeTestFile(t, filepath.Join(s1, "Show S01E01.mkv"), "video")
	writeTestFile(t, filepath.Join(s1, "season.nfo"), `<season><title>Kept</title></season>`)
	showNFO := filepath.Join(root, "tvshow.nfo")
	writeTestFile(t, showNFO, `<tvshow><title>Show</title><namedseason number="1">Named</namedseason></tvshow>`)
	if err := os.Chmod(showNFO, 0); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	seasons, err := NewProvider().GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          "series",
		SeriesRootPaths:      []string{root},
		SeasonDirectoryPaths: map[int][]string{1: {s1}},
		ShowFilePaths:        []string{filepath.Join(s1, "Show S01E01.mkv")},
	})
	if !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("GetSeasons() error = %v, want ErrLocalMetadataUnavailable", err)
	}
	// A fill-empty pass still gets what could be read.
	if len(seasons) != 1 || seasons[0].Title != "Kept" {
		t.Fatalf("seasons = %#v, want season 1 from its season.nfo", seasons)
	}
}

// A show split across two roots whose second share is gone may leave an old
// tvshow.nfo on the empty mountpoint. GetSeasons reads the show NFO the way
// the show-level lookup does, so that stale file names no season and the
// lookup reports the share unavailable.
func TestGetSeasons_StaleShowNFOOnLostShareIsNotRead(t *testing.T) {
	t.Parallel()

	live := t.TempDir()
	lost := t.TempDir()
	s1 := filepath.Join(live, "Season 01")
	e1 := filepath.Join(s1, "Show S01E01.mkv")
	writeTestFile(t, e1, "video")
	writeTestFile(t, filepath.Join(lost, "tvshow.nfo"),
		`<tvshow><title>Show</title><namedseason number="1">Stale Name</namedseason></tvshow>`)
	gone := filepath.Join(lost, "Season 02", "Show S02E01.mkv") // cataloged, but the share is empty

	seasons, err := NewProvider().GetSeasons(context.Background(), metadata.SeasonsRequest{
		ContentType:          "series",
		SeriesRootPaths:      []string{live, lost},
		SeasonDirectoryPaths: map[int][]string{1: {s1}},
		ShowFilePaths:        []string{e1, gone},
	})
	if !errors.Is(err, metadata.ErrLocalMetadataUnavailable) {
		t.Fatalf("GetSeasons() error = %v, want ErrLocalMetadataUnavailable", err)
	}
	for _, season := range seasons {
		if season.Title == "Stale Name" {
			t.Fatalf("seasons = %#v, took a name from the lost share's tvshow.nfo", seasons)
		}
	}
}
