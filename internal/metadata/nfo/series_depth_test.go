package nfo

import (
	"context"
	"errors"

	"path/filepath"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// ---------------------------------------------------------------------------
// Parser tables: <season>
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Parser tables: extended <episodedetails>
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Parser tables: extended <episodedetails>
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Provider: GetSeasons
// ---------------------------------------------------------------------------

// getSeasons and getEpisodes ask a fresh provider and fail the test on error.
func getSeasons(t *testing.T, req metadata.SeasonsRequest) []metadata.SeasonResult {
	t.Helper()
	seasons, err := NewProvider().GetSeasons(context.Background(), req)
	if err != nil {
		t.Fatalf("GetSeasons: %v", err)
	}
	return seasons
}

func getEpisodes(t *testing.T, req metadata.EpisodesRequest) []metadata.EpisodeResult {
	t.Helper()
	episodes, err := NewProvider().GetEpisodes(context.Background(), req)
	if err != nil {
		t.Fatalf("GetEpisodes: %v", err)
	}
	return episodes
}

// A mismatched <seasonnumber> is advisory: naming owns structure, so the
// directory-derived season number wins.
func TestGetSeasons_NumberConflictPrefersDirectoryNumber(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s2 := filepath.Join(root, "Season 02")
	writeFile(t, filepath.Join(s2, "Show S02E01.mkv"), "video")
	writeFile(t, filepath.Join(s2, "season.nfo"),
		`<season><title>Course B</title><seasonnumber>7</seasonnumber></season>`)

	seasons := getSeasons(t, metadata.SeasonsRequest{
		ContentType:          "series",
		SeriesRootPaths:      []string{root},
		SeasonDirectoryPaths: map[int][]string{2: {s2}},
	})
	if len(seasons) != 1 || seasons[0].SeasonNumber != 2 || seasons[0].Title != "Course B" {
		t.Fatalf("seasons = %#v, want season 2 titled Course B", seasons)
	}
}

// ---------------------------------------------------------------------------
// Provider: GetEpisodes
// ---------------------------------------------------------------------------

// NFO numbers are advisory: the filename-parsed SxxEyy (the request key) wins
// on conflict.
func TestGetEpisodes_NumberConflictFilenameWins(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s1 := filepath.Join(root, "Season 01")
	e3 := filepath.Join(s1, "Show S01E03.mkv")
	writeFile(t, e3, "video")
	writeFile(t, filepath.Join(s1, "Show S01E03.nfo"),
		`<episodedetails><title>Third</title><season>4</season><episode>9</episode></episodedetails>`)

	episodes := getEpisodes(t, metadata.EpisodesRequest{
		SeasonNumber:     1,
		EpisodeFilePaths: map[int][]string{3: {e3}},
	})
	if len(episodes) != 1 || episodes[0].SeasonNumber != 1 || episodes[0].EpisodeNumber != 3 {
		t.Fatalf("episodes = %#v, want S1E3 from filename", episodes)
	}
	if episodes[0].Title != "Third" {
		t.Errorf("Title = %q, want Third", episodes[0].Title)
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
	writeFile(t, video, "video")
	writeFile(t, filepath.Join(s1, "Show S01E01-E02.nfo"),
		`<episodedetails><title>Double</title><uniqueid type="tvdb">4001</uniqueid></episodedetails>`)

	episodes := getEpisodes(t, metadata.EpisodesRequest{
		SeasonNumber:     1,
		SeriesRootPaths:  []string{root},
		EpisodeFilePaths: map[int][]string{1: {video}, 2: {video}},
	})
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
	targeted := getEpisodes(t, metadata.EpisodesRequest{
		SeasonNumber:     1,
		SeriesRootPaths:  []string{root},
		EpisodeFilePaths: map[int][]string{2: {video}},
	})
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
	writeFile(t, filepath.Join(s1, "Show S01E01.mkv"), "video")
	writeFile(t, filepath.Join(s1, "season.nfo"), `<season><title>Kept</title></season>`)
	showNFO := filepath.Join(root, "tvshow.nfo")
	writeFile(t, showNFO, `<tvshow><title>Show</title><namedseason number="1">Named</namedseason></tvshow>`)
	setMode(t, showNFO, 0)

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
	writeFile(t, e1, "video")
	writeFile(t, filepath.Join(lost, "tvshow.nfo"),
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
