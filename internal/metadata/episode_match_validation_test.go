//nolint:goconst // Repeated provider IDs and episode titles make these fixtures readable.
package metadata

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type episodeValidationStubProvider struct {
	mu       sync.Mutex
	slug     string
	episodes map[string][]EpisodeResult
	errors   map[string]error
	calls    []string
}

type episodeValidationPipelineProvider struct {
	*episodeValidationStubProvider
	searchResults []SearchResult
}

func (p *episodeValidationPipelineProvider) Search(_ context.Context, _ SearchQuery) ([]SearchResult, error) {
	return append([]SearchResult(nil), p.searchResults...), nil
}

func (p *episodeValidationPipelineProvider) GetMetadata(_ context.Context, req MetadataRequest) (*MetadataResult, error) {
	id := req.ProviderIDs["tmdb"]
	year := 0
	switch id {
	case "2015":
		year = 2015
	case "2020":
		year = 2020
	}
	return &MetadataResult{
		HasMetadata: true,
		Title:       "Crims",
		Year:        year,
		ProviderIDs: map[string]string{"tmdb": id},
	}, nil
}

func (p *episodeValidationStubProvider) Slug() string {
	if p.slug == "" {
		return "tmdb"
	}
	return p.slug
}
func (p *episodeValidationStubProvider) Name() string       { return p.Slug() }
func (p *episodeValidationStubProvider) ForTypes() []string { return []string{"series"} }
func (p *episodeValidationStubProvider) GetSeasons(context.Context, SeasonsRequest) ([]SeasonResult, error) {
	return nil, nil
}
func (p *episodeValidationStubProvider) GetEpisodes(_ context.Context, req EpisodesRequest) ([]EpisodeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := req.ProviderIDs[p.Slug()]
	p.calls = append(p.calls, id)
	if err := p.errors[id]; err != nil {
		return nil, err
	}
	return append([]EpisodeResult(nil), p.episodes[id]...), nil
}

func (p *episodeValidationStubProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func TestExtractEpisodeMatchTitle(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"standard radarr style": "/tv/Crims/Season 1/Crims - S01E01 - Day One[WEBDL-1080p.AAC.h264.NTb].mp4",
		"dotted release":        "/tv/Crims/Season 1/Crims.S01E01.Day.One.1080p.WEB-DL.x264-GROUP.mkv",
		"no episode title":      "/tv/Crims/Season 1/Crims.S01E01.1080p.WEB.x264-GROUP.mkv",
	}
	wants := map[string]string{
		"standard radarr style": "Day One",
		"dotted release":        "Day One",
		"no episode title":      "",
	}
	for name, path := range tests {
		name, path := name, path
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := extractEpisodeMatchTitle(path); got != wants[name] {
				t.Fatalf("extractEpisodeMatchTitle() = %q, want %q", got, wants[name])
			}
		})
	}
}

func TestValidateSeriesMatchByEpisodes_SelectsSecondExactTitleCandidate(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"2020": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Arrival"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Departure"},
		},
		"2015": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Day One"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Crime and Punishment"},
		},
	}}
	hints := &MatchHints{
		Title: "Crims", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Crims/Season 1/Crims - S01E01 - Day One[WEBDL-1080p].mkv",
			"/tv/Crims/Season 1/Crims - S01E02 - Crime and Punishment[WEBDL-1080p].mkv",
		},
	}
	candidates := []MatchCandidate{
		{Title: "Crims", Year: 2020, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "2020"}},
		{Title: "Crims", Year: 2015, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "2015"}},
	}

	winner, errs := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if len(errs) != 0 {
		t.Fatalf("validation errors = %v", errs)
	}
	if winner == nil || winner.ProviderIDs["tmdb"] != "2015" {
		t.Fatalf("winner = %+v, want 2015 candidate", winner)
	}
	if !containsString(winner.MatchReasons, "episode_title_corroboration:2_of_2") {
		t.Fatalf("winner reasons = %v", winner.MatchReasons)
	}
}

func TestValidateSeriesMatchByEpisodes_LeavesMemoriesUnmatched(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"2018": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "A New Beginning"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Home"},
		},
		"1998": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Old Friends"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Reunion"},
		},
	}}
	hints := &MatchHints{
		Title: "Memories", Type: "series",
		AllGroupFilePaths: []string{
			"/anime/Memories/Season 1/Memories - S01E01 - Magnetic Rose[Bluray-1080p].mkv",
			"/anime/Memories/Season 1/Memories - S01E02 - Stink Bomb[Bluray-1080p].mkv",
		},
	}
	candidates := []MatchCandidate{
		{Title: "Memories", Year: 2018, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "2018"}},
		{Title: "Memories", Year: 1998, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "1998"}},
	}

	winner, _ := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if winner != nil {
		t.Fatalf("winner = %+v, want unmatched type conflict", winner)
	}
}

func TestValidateSeriesMatchByEpisodes_SearchesPastFormerTopThree(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"1": {{SeasonNumber: 1, EpisodeNumber: 1, Title: "Wrong One"}, {SeasonNumber: 1, EpisodeNumber: 2, Title: "Wrong Two"}},
		"2": {{SeasonNumber: 1, EpisodeNumber: 1, Title: "Wrong One"}, {SeasonNumber: 1, EpisodeNumber: 2, Title: "Wrong Two"}},
		"3": {{SeasonNumber: 1, EpisodeNumber: 1, Title: "Wrong One"}, {SeasonNumber: 1, EpisodeNumber: 2, Title: "Wrong Two"}},
		"4": {{SeasonNumber: 1, EpisodeNumber: 1, Title: "Right One"}, {SeasonNumber: 1, EpisodeNumber: 2, Title: "Right Two"}},
	}}
	hints := &MatchHints{
		Title: "Shared", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Shared/Season 1/Shared - S01E01 - Right One[WEBDL-1080p].mkv",
			"/tv/Shared/Season 1/Shared - S01E02 - Right Two[WEBDL-1080p].mkv",
		},
	}
	candidates := make([]MatchCandidate, 0, 4)
	for _, id := range []string{"1", "2", "3", "4"} {
		candidates = append(candidates, MatchCandidate{
			Title: "Shared", Year: 2000, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": id},
		})
	}

	winner, _ := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if winner == nil || winner.ProviderIDs["tmdb"] != "4" {
		t.Fatalf("winner = %+v, want corroborated fourth candidate", winner)
	}
	if got := provider.callCount(); got != len(candidates) {
		t.Fatalf("provider calls = %d, want %d", got, len(candidates))
	}
}

func TestValidateSeriesMatchByEpisodes_BoundsCandidateFetches(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{}}
	hints := &MatchHints{
		Title: "Shared", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Shared/Shared - S01E01 - Right One.mkv",
			"/tv/Shared/Shared - S01E02 - Right Two.mkv",
		},
	}
	candidates := make([]MatchCandidate, 0, maxEpisodeValidationCandidates+4)
	for i := 0; i < maxEpisodeValidationCandidates+4; i++ {
		id := string(rune('a' + i))
		provider.episodes[id] = []EpisodeResult{
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Wrong One"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Wrong Two"},
		}
		candidates = append(candidates, MatchCandidate{
			Title: "Shared", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": id},
		})
	}

	validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if got := provider.callCount(); got != maxEpisodeValidationCandidates {
		t.Fatalf("provider calls = %d, want bounded %d", got, maxEpisodeValidationCandidates)
	}
}

func TestValidateSeriesMatchByEpisodes_ValidatesCrossProviderExactYearAlias(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"show": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "The Beginning"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "The Journey"},
		},
	}}
	hints := &MatchHints{
		Title: "Bunny Drop", Year: 2011, Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Bunny Drop/Bunny Drop - S01E01 - The Beginning.mkv",
			"/tv/Bunny Drop/Bunny Drop - S01E02 - The Journey.mkv",
		},
	}
	candidates := []MatchCandidate{{
		Title: "Usagi Drop", Year: 2011, ContentType: "series", Sources: []string{"tmdb", "tvdb"},
		ProviderIDs: map[string]string{"tmdb": "show", "tvdb": "123"},
	}}

	winner, errs := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if len(errs) != 0 || winner == nil || winner.ProviderIDs["tmdb"] != "show" {
		t.Fatalf("winner = %+v errors = %v, want exact-year alias corroborated", winner, errs)
	}
}

func TestValidateSeriesMatchByEpisodes_AcceptsDistinctiveSoleEpisode(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"232926": {{SeasonNumber: 1, EpisodeNumber: 1, Title: "The Fiancé Who Killed Me"}},
	}}
	hints := &MatchHints{
		Title: "7th Time Loop", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/7th Time Loop/Season 1/7th Time Loop - S01E01 - The Fiance Who Killed Me[WEBDL-1080p].mkv",
		},
	}
	candidates := []MatchCandidate{{
		Title: "7th Time Loop", Year: 2024, ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "232926"},
	}}

	winner, _ := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if winner == nil {
		t.Fatal("distinctive exact episode title should corroborate the sole candidate")
	}
}

func TestValidateSeriesMatchByEpisodes_FallsBackWhenPreferredProviderHasGenericTitles(t *testing.T) {
	t.Parallel()
	tmdb := &episodeValidationStubProvider{slug: "tmdb", episodes: map[string][]EpisodeResult{
		"61834": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Episode 1"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Episode 2"},
		},
	}}
	tvdb := &episodeValidationStubProvider{slug: "tvdb", episodes: map[string][]EpisodeResult{
		"289456": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Day One"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Day Thirty-Six"},
		},
	}}
	hints := &MatchHints{
		Title: "Crims", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Crims/Season 1/Crims - S01E01 - Day One.WEBDL-1080p.mkv",
			"/tv/Crims/Season 1/Crims - S01E02 - Day Thirty-Six.WEBDL-1080p.mkv",
		},
	}
	candidates := []MatchCandidate{{
		Title: "Crims", Year: 2015, ContentType: "series", Sources: []string{"tmdb", "tvdb"},
		ProviderIDs: map[string]string{"tmdb": "61834", "tvdb": "289456"},
	}}

	winner, errs := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{tvdb, tmdb}, "en")
	if len(errs) != 0 {
		t.Fatalf("validation errors = %v", errs)
	}
	if winner == nil || winner.ProviderIDs["tvdb"] != "289456" {
		t.Fatalf("winner = %+v, want TVDB-corroborated Crims", winner)
	}
	if tmdb.callCount() != 1 || tvdb.callCount() != 1 {
		t.Fatalf("provider calls tmdb=%d tvdb=%d, want one each", tmdb.callCount(), tvdb.callCount())
	}
}

func TestValidateSeriesMatchByEpisodes_TreatsMissingRivalSeasonAsZeroEvidence(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{
		episodes: map[string][]EpisodeResult{
			"correct": {
				{SeasonNumber: 1, EpisodeNumber: 1, Title: "Day One"},
				{SeasonNumber: 1, EpisodeNumber: 2, Title: "Day Thirty-Six"},
			},
		},
		errors: map[string]error{"rival": errors.New("tmdb: HTTP 404: season not found")},
	}
	hints := &MatchHints{
		Title: "Crims", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Crims/Crims - S01E01 - Day One.mkv",
			"/tv/Crims/Crims - S01E02 - Day Thirty-Six.mkv",
		},
	}
	candidates := []MatchCandidate{
		{Title: "Crims", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "rival"}},
		{Title: "Crims", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "correct"}},
	}

	winner, errs := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if len(errs) != 0 {
		t.Fatalf("validation errors = %v, want missing season treated as evaluated evidence", errs)
	}
	if winner == nil || winner.ProviderIDs["tmdb"] != "correct" {
		t.Fatalf("winner = %+v, want corroborated candidate", winner)
	}
}

func TestValidateSeriesMatchByEpisodes_OperationalRivalErrorBlocksPromotion(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{
		episodes: map[string][]EpisodeResult{
			"correct": {
				{SeasonNumber: 1, EpisodeNumber: 1, Title: "Day One"},
				{SeasonNumber: 1, EpisodeNumber: 2, Title: "Day Thirty-Six"},
			},
		},
		errors: map[string]error{"rival": errors.New("tmdb: HTTP 503: unavailable")},
	}
	hints := &MatchHints{
		Title: "Crims", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Crims/Crims - S01E01 - Day One.mkv",
			"/tv/Crims/Crims - S01E02 - Day Thirty-Six.mkv",
		},
	}
	candidates := []MatchCandidate{
		{Title: "Crims", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "rival"}},
		{Title: "Crims", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "correct"}},
	}

	winner, errs := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if winner != nil {
		t.Fatalf("winner = %+v, want operationally unavailable rival to block promotion", winner)
	}
	if len(errs) != 1 {
		t.Fatalf("validation errors = %v, want one operational error", errs)
	}
}

func TestValidateSeriesMatchByEpisodes_AcceptsMultiSourceCoordinateCorroboration(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"show": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Episode 1"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Episode 2"},
			{SeasonNumber: 1, EpisodeNumber: 3, Title: "Episode 3"},
		},
	}}
	hints := &MatchHints{
		Title: "Shared", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Shared/Shared - S01E01.mkv",
			"/tv/Shared/Shared - S01E02.mkv",
			"/tv/Shared/Shared - S01E03.mkv",
		},
	}
	candidates := []MatchCandidate{{
		Title: "Shared", ContentType: "series", Sources: []string{"tmdb", "tvdb"},
		ProviderIDs: map[string]string{"tmdb": "show", "tvdb": "other"},
	}}

	winner, errs := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if len(errs) != 0 {
		t.Fatalf("validation errors = %v", errs)
	}
	if winner == nil || !containsString(winner.MatchReasons, "episode_coordinate_corroboration:3_of_3") {
		t.Fatalf("winner = %+v, want coordinate corroboration", winner)
	}
}

func TestValidateSeriesMatchByEpisodes_RejectsWeakSingleSourceCoordinates(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"show": {
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Episode 1"},
			{SeasonNumber: 1, EpisodeNumber: 2, Title: "Episode 2"},
			{SeasonNumber: 1, EpisodeNumber: 3, Title: "Episode 3"},
		},
	}}
	hints := &MatchHints{
		Title: "Shared", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Shared/Shared - S01E01.mkv",
			"/tv/Shared/Shared - S01E02.mkv",
			"/tv/Shared/Shared - S01E03.mkv",
		},
	}
	candidates := []MatchCandidate{{
		Title: "Shared", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "show"},
	}}

	winner, _ := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if winner != nil {
		t.Fatalf("winner = %+v, weak S01E01-E03 coordinates from one source must stay unmatched", winner)
	}
}

func TestValidateSeriesMatchByEpisodes_AcceptsDistinctiveSingleSourceCoordinates(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"show": {
			{SeasonNumber: 2, EpisodeNumber: 1, Title: "Episode 1"},
			{SeasonNumber: 2, EpisodeNumber: 4, Title: "Episode 4"},
			{SeasonNumber: 2, EpisodeNumber: 8, Title: "Episode 8"},
		},
	}}
	hints := &MatchHints{
		Title: "Shared", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Shared/Shared - S02E01.mkv",
			"/tv/Shared/Shared - S02E04.mkv",
			"/tv/Shared/Shared - S02E08.mkv",
		},
	}
	candidates := []MatchCandidate{{
		Title: "Shared", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "show"},
	}}

	winner, _ := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if winner == nil || !containsString(winner.MatchReasons, "episode_coordinate_corroboration:3_of_3") {
		t.Fatalf("winner = %+v, want distinctive coordinate corroboration", winner)
	}
}

func TestValidateSeriesMatchByEpisodes_RejectsCoordinateOnlyChoiceBetweenExactTitles(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"first": {
			{SeasonNumber: 2, EpisodeNumber: 1, Title: "Episode 1"},
			{SeasonNumber: 2, EpisodeNumber: 4, Title: "Episode 4"},
			{SeasonNumber: 2, EpisodeNumber: 8, Title: "Episode 8"},
		},
		"second": {
			{SeasonNumber: 2, EpisodeNumber: 1, Title: "Episode 1"},
		},
	}}
	hints := &MatchHints{
		Title: "Shared", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Shared/Shared - S02E01.mkv",
			"/tv/Shared/Shared - S02E04.mkv",
			"/tv/Shared/Shared - S02E08.mkv",
		},
	}
	candidates := []MatchCandidate{
		{Title: "Shared", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "first"}},
		{Title: "Shared", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "second"}},
	}

	winner, _ := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if winner != nil {
		t.Fatalf("winner = %+v, coordinates alone must not choose between exact-title candidates", winner)
	}
}

func TestSelectLocalEpisodeCoordinateHints_SamplesLastEpisodeAcrossSeasons(t *testing.T) {
	t.Parallel()

	hints := selectLocalEpisodeCoordinateHints([]string{
		"/tv/Shared/Season 1/Shared - S01E01.mkv",
		"/tv/Shared/Season 1/Shared - S01E10.mkv",
		"/tv/Shared/Season 2/Shared - S02E08.mkv",
		"/tv/Shared/Season 3/Shared - S03E12.mkv",
		"/tv/Shared/Season 4/Shared - S04E04.mkv",
	})

	want := []localEpisodeMatchHint{
		{SeasonNumber: 1, EpisodeNumber: 10},
		{SeasonNumber: 3, EpisodeNumber: 12},
		{SeasonNumber: 4, EpisodeNumber: 4},
	}
	if len(hints) != len(want) {
		t.Fatalf("hints = %+v, want %+v", hints, want)
	}
	for i := range want {
		if hints[i].SeasonNumber != want[i].SeasonNumber || hints[i].EpisodeNumber != want[i].EpisodeNumber {
			t.Fatalf("hints = %+v, want %+v", hints, want)
		}
	}
}

func TestValidateSeriesMatchByEpisodes_AcceptsUniqueMultiSeasonShape(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"original": {
			{SeasonNumber: 1, EpisodeNumber: 10, Title: "Episode 10"},
			{SeasonNumber: 2, EpisodeNumber: 8, Title: "Episode 8"},
			{SeasonNumber: 4, EpisodeNumber: 6, Title: "Episode 6"},
		},
		"remake": {
			{SeasonNumber: 1, EpisodeNumber: 10, Title: "Episode 10"},
			{SeasonNumber: 2, EpisodeNumber: 8, Title: "Episode 8"},
		},
	}}
	hints := &MatchHints{
		Title: "Shared", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Shared/Season 1/Shared - S01E10.mkv",
			"/tv/Shared/Season 2/Shared - S02E08.mkv",
			"/tv/Shared/Season 4/Shared - S04E06.mkv",
		},
	}
	candidates := []MatchCandidate{
		{Title: "Shared", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "remake"}},
		{Title: "Shared", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": "original"}},
	}

	winner, errs := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if len(errs) != 0 {
		t.Fatalf("validation errors = %v", errs)
	}
	if winner == nil || winner.ProviderIDs["tmdb"] != "original" {
		t.Fatalf("winner = %+v, want original multi-season shape", winner)
	}
	if !containsString(winner.MatchReasons, "episode_coordinate_corroboration:3_of_3") {
		t.Fatalf("winner reasons = %v", winner.MatchReasons)
	}
}

func TestValidateSeriesMatchByEpisodes_RejectsTwoCoordinateSingleSeasonShape(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
		"candidate": {
			{SeasonNumber: 2, EpisodeNumber: 1, Title: "Episode 1"},
			{SeasonNumber: 2, EpisodeNumber: 8, Title: "Episode 8"},
		},
	}}
	hints := &MatchHints{
		Title: "Shared", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Shared/Season 2/Shared - S02E01.mkv",
			"/tv/Shared/Season 2/Shared - S02E08.mkv",
		},
	}
	candidates := []MatchCandidate{{
		Title: "Shared", ContentType: "series", Sources: []string{"tmdb"},
		ProviderIDs: map[string]string{"tmdb": "candidate"},
	}}

	winner, errs := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if len(errs) != 0 {
		t.Fatalf("validation errors = %v", errs)
	}
	if winner != nil {
		t.Fatalf("winner = %+v, two coordinates from one season are insufficient shape evidence", winner)
	}
}

func TestValidateSeriesMatchByEpisodes_RejectsShapeWhenCandidatesAreTruncated(t *testing.T) {
	t.Parallel()
	provider := &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{}}
	hints := &MatchHints{
		Title: "Shared", Type: "series",
		AllGroupFilePaths: []string{
			"/tv/Shared/Season 1/Shared - S01E10.mkv",
			"/tv/Shared/Season 2/Shared - S02E08.mkv",
		},
	}
	candidates := make([]MatchCandidate, 0, maxEpisodeValidationCandidates+1)
	for i := 0; i < maxEpisodeValidationCandidates+1; i++ {
		id := string(rune('a' + i))
		provider.episodes[id] = []EpisodeResult{{SeasonNumber: 1, EpisodeNumber: 10, Title: "Episode 10"}}
		if i == 0 {
			provider.episodes[id] = append(provider.episodes[id], EpisodeResult{SeasonNumber: 2, EpisodeNumber: 8, Title: "Episode 8"})
		}
		candidates = append(candidates, MatchCandidate{
			Title: "Shared", ContentType: "series", Sources: []string{"tmdb"}, ProviderIDs: map[string]string{"tmdb": id},
		})
	}

	winner, _ := validateSeriesMatchByEpisodes(context.Background(), hints, candidates, []Provider{provider}, "en")
	if winner != nil {
		t.Fatalf("winner = %+v, truncated candidate set must stay unmatched", winner)
	}
}

func TestInitialMatchPipelineUsesEpisodeCorroboration(t *testing.T) {
	t.Parallel()
	harness := newTestHarness()
	harness.service.seasonRepo = newFakeSeasonRepo()
	harness.service.episodeRepo = newFakeEpisodeRepo()
	provider := &episodeValidationPipelineProvider{
		episodeValidationStubProvider: &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
			"2020": {
				{SeasonNumber: 1, EpisodeNumber: 1, Title: "Arrival"},
				{SeasonNumber: 1, EpisodeNumber: 2, Title: "Departure"},
			},
			"2015": {
				{SeasonNumber: 1, EpisodeNumber: 1, Title: "Day One"},
				{SeasonNumber: 1, EpisodeNumber: 2, Title: "Crime and Punishment"},
			},
		}},
		searchResults: []SearchResult{
			{Name: "Crims", Year: 2020, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "2020"}},
			{Name: "Crims", Year: 2015, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "2015"}},
		},
	}

	result, err := harness.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints: &MatchHints{
			Title: "Crims", Type: "series",
			AllGroupFilePaths: []string{
				"/tv/Crims/Season 1/Crims - S01E01 - Day One[WEBDL-1080p].mkv",
				"/tv/Crims/Season 1/Crims - S01E02 - Crime and Punishment[WEBDL-1080p].mkv",
			},
		},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders() error = %v", err)
	}
	if result == nil || !result.Updated || result.Decision == nil || result.Decision.Outcome != "matched" {
		t.Fatalf("result = %#v, want matched update", result)
	}
	item, err := harness.itemRepo.GetByID(context.Background(), result.ContentID)
	if err != nil {
		t.Fatalf("load matched item: %v", err)
	}
	if item.TmdbID != "2015" || item.Year != 2015 {
		t.Fatalf("matched item = tmdb:%q year:%d, want tmdb:2015 year:2015", item.TmdbID, item.Year)
	}
	foundReason := false
	for _, candidate := range result.Decision.TopCandidates {
		if candidate.ProviderIDs["tmdb"] == "2015" && containsString(candidate.Reasons, "episode_title_corroboration:2_of_2") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Fatalf("decision = %+v, want episode corroboration reason on selected candidate", result.Decision)
	}
}

func TestInitialMatchPipelineDoesNotAddEpisodeValidationFetchForDecisiveMatch(t *testing.T) {
	t.Parallel()
	harness := newTestHarness()
	harness.service.seasonRepo = newFakeSeasonRepo()
	harness.service.episodeRepo = newFakeEpisodeRepo()
	provider := &episodeValidationPipelineProvider{
		episodeValidationStubProvider: &episodeValidationStubProvider{episodes: map[string][]EpisodeResult{
			"2015": {
				{SeasonNumber: 1, EpisodeNumber: 1, Title: "Day One"},
				{SeasonNumber: 1, EpisodeNumber: 2, Title: "Crime and Punishment"},
			},
		}},
		searchResults: []SearchResult{
			{Name: "Crims", Year: 2015, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "2015"}},
		},
	}

	result, err := harness.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints: &MatchHints{
			Title: "Crims", Year: 2015, Type: "series",
			AllGroupFilePaths: []string{
				"/tv/Crims/Season 1/Crims - S01E01 - Day One.mkv",
				"/tv/Crims/Season 1/Crims - S01E02 - Crime and Punishment.mkv",
			},
		},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders() error = %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != "matched" {
		t.Fatalf("result = %#v, want decisive match", result)
	}
	// The single call is the normal post-match episode enrichment. An episode
	// validation fallback would add a second provider fetch before enrichment.
	if calls := provider.callCount(); calls != 1 {
		t.Fatalf("episode provider calls = %d, want only the normal enrichment fetch", calls)
	}
}

func TestInitialMatchPipelinePreservesCandidateRejectionForPermanentEpisodeValidationError(t *testing.T) {
	t.Parallel()
	harness := newTestHarness()
	provider := &episodeValidationPipelineProvider{
		episodeValidationStubProvider: &episodeValidationStubProvider{errors: map[string]error{
			"first":  errors.New("tmdb: HTTP 400: bad request"),
			"second": errors.New("tmdb: HTTP 400: bad request"),
		}},
		searchResults: []SearchResult{
			{Name: "Shared", Year: 2020, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "first"}},
			{Name: "Shared", Year: 2015, Provider: "tmdb", ProviderIDs: map[string]string{"tmdb": "second"}},
		},
	}

	result, err := harness.service.ProcessWithProviders(context.Background(), ProcessRequest{
		Hints: &MatchHints{
			Title: "Shared", Type: "series",
			AllGroupFilePaths: []string{
				"/tv/Shared/Shared - S01E01 - First Episode.mkv",
				"/tv/Shared/Shared - S01E02 - Second Episode.mkv",
			},
		},
		Language: "en",
		Mode:     ModeInitialMatch,
	}, []Provider{provider})
	if err != nil {
		t.Fatalf("ProcessWithProviders() error = %v", err)
	}
	if result == nil || result.Decision == nil || result.Decision.Outcome != "candidate_rejected" {
		t.Fatalf("result = %#v decision = %+v, want deterministic candidate rejection", result, result.Decision)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
