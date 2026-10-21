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
		// Absolute-numbered episodes: the whole episode code has to be consumed,
		// or its last digit is read as the start of the episode title.
		"four digit episode":        "/tv/One Piece/Season 23/One Piece S23E1162 - Wano Country.mkv",
		"four digit dotted release": "/tv/One Piece/Season 23/One.Piece.S23E1162.Wano.Country.1080p.WEB-DL.x264-GROUP.mkv",
		"four digit multi episode":  "/tv/One Piece/Season 23/One Piece S23E1162E1163 - Wano Country.mkv",
	}
	wants := map[string]string{
		"standard radarr style":     "Day One",
		"dotted release":            "Day One",
		"no episode title":          "",
		"four digit episode":        "Wano Country",
		"four digit dotted release": "Wano Country",
		"four digit multi episode":  "Wano Country",
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

type validateSeriesMatchByEpisodesDecisionsCase struct {
	name       string
	episodes   map[string][]EpisodeResult
	errors     map[string]error
	hints      *MatchHints
	candidates []MatchCandidate
	wantWinner bool
	wantTMDB   string
	wantReason string
	checkErrs  bool
	wantErrs   int
	wantCalls  int // 0: not checked
}

var validateSeriesMatchByEpisodesDecisionsCases = func() []validateSeriesMatchByEpisodesDecisionsCase {
	ep := func(season, episode int, title string) EpisodeResult {
		return EpisodeResult{SeasonNumber: season, EpisodeNumber: episode, Title: title}
	}
	hints := func(title string, year int, paths ...string) *MatchHints {
		return &MatchHints{Title: title, Year: year, Type: "series", AllGroupFilePaths: paths}
	}
	tmdbCand := func(title string, year int, id string) MatchCandidate {
		return matchCand(title, year, "series", map[string]string{"tmdb": id}, "tmdb")
	}
	crimsPaths := []string{"/tv/Crims/Crims - S01E01 - Day One.mkv", "/tv/Crims/Crims - S01E02 - Day Thirty-Six.mkv"}
	crimsEpisodes := map[string][]EpisodeResult{"correct": {ep(1, 1, "Day One"), ep(1, 2, "Day Thirty-Six")}}
	crimsCandidates := []MatchCandidate{tmdbCand("Crims", 0, "rival"), tmdbCand("Crims", 0, "correct")}
	weakPaths := []string{"/tv/Shared/Shared - S01E01.mkv", "/tv/Shared/Shared - S01E02.mkv", "/tv/Shared/Shared - S01E03.mkv"}
	weakEpisodes := map[string][]EpisodeResult{"show": {ep(1, 1, "Episode 1"), ep(1, 2, "Episode 2"), ep(1, 3, "Episode 3")}}
	distinctivePaths := []string{"/tv/Shared/Shared - S02E01.mkv", "/tv/Shared/Shared - S02E04.mkv", "/tv/Shared/Shared - S02E08.mkv"}
	distinctiveEpisodes := []EpisodeResult{ep(2, 1, "Episode 1"), ep(2, 4, "Episode 4"), ep(2, 8, "Episode 8")}
	wrongPair := []EpisodeResult{ep(1, 1, "Wrong One"), ep(1, 2, "Wrong Two")}
	const coordinates3of3 = "episode_coordinate_corroboration:3_of_3"

	return []validateSeriesMatchByEpisodesDecisionsCase{
		{
			name: "leaves Memories unmatched",
			episodes: map[string][]EpisodeResult{
				"2018": {ep(1, 1, "A New Beginning"), ep(1, 2, "Home")},
				"1998": {ep(1, 1, "Old Friends"), ep(1, 2, "Reunion")},
			},
			hints: hints("Memories", 0,
				"/anime/Memories/Season 1/Memories - S01E01 - Magnetic Rose[Bluray-1080p].mkv",
				"/anime/Memories/Season 1/Memories - S01E02 - Stink Bomb[Bluray-1080p].mkv"),
			candidates: []MatchCandidate{tmdbCand("Memories", 2018, "2018"), tmdbCand("Memories", 1998, "1998")},
		},
		{
			name: "searches past former top three",
			episodes: map[string][]EpisodeResult{
				"1": wrongPair, "2": wrongPair, "3": wrongPair,
				"4": {ep(1, 1, "Right One"), ep(1, 2, "Right Two")},
			},
			hints: hints("Shared", 0,
				"/tv/Shared/Season 1/Shared - S01E01 - Right One[WEBDL-1080p].mkv",
				"/tv/Shared/Season 1/Shared - S01E02 - Right Two[WEBDL-1080p].mkv"),
			candidates: []MatchCandidate{tmdbCand("Shared", 2000, "1"), tmdbCand("Shared", 2000, "2"), tmdbCand("Shared", 2000, "3"), tmdbCand("Shared", 2000, "4")},
			wantWinner: true, wantTMDB: "4", wantCalls: 4,
		},
		{
			name:     "validates cross-provider exact-year alias",
			episodes: map[string][]EpisodeResult{"show": {ep(1, 1, "The Beginning"), ep(1, 2, "The Journey")}},
			hints: hints("Bunny Drop", 2011,
				"/tv/Bunny Drop/Bunny Drop - S01E01 - The Beginning.mkv",
				"/tv/Bunny Drop/Bunny Drop - S01E02 - The Journey.mkv"),
			candidates: []MatchCandidate{matchCand("Usagi Drop", 2011, "series", map[string]string{"tmdb": "show", "tvdb": "123"}, "tmdb", "tvdb")},
			wantWinner: true, wantTMDB: "show", checkErrs: true,
		},
		{
			name:       "accepts distinctive sole episode",
			episodes:   map[string][]EpisodeResult{"232926": {ep(1, 1, "The Fiancé Who Killed Me")}},
			hints:      hints("7th Time Loop", 0, "/tv/7th Time Loop/Season 1/7th Time Loop - S01E01 - The Fiance Who Killed Me[WEBDL-1080p].mkv"),
			candidates: []MatchCandidate{tmdbCand("7th Time Loop", 2024, "232926")},
			wantWinner: true,
		},
		{
			// A 404 for the rival's season is evaluated evidence, not an error.
			name:       "treats missing rival season as zero evidence",
			episodes:   crimsEpisodes,
			errors:     map[string]error{"rival": errors.New("tmdb: HTTP 404: season not found")},
			hints:      hints("Crims", 0, crimsPaths...),
			candidates: crimsCandidates,
			wantWinner: true, wantTMDB: "correct", checkErrs: true,
		},
		{
			name:       "operational rival error blocks promotion",
			episodes:   crimsEpisodes,
			errors:     map[string]error{"rival": errors.New("tmdb: HTTP 503: unavailable")},
			hints:      hints("Crims", 0, crimsPaths...),
			candidates: crimsCandidates,
			checkErrs:  true, wantErrs: 1,
		},
		{
			name:       "accepts multi-source coordinate corroboration",
			episodes:   weakEpisodes,
			hints:      hints("Shared", 0, weakPaths...),
			candidates: []MatchCandidate{matchCand("Shared", 0, "series", map[string]string{"tmdb": "show", "tvdb": "other"}, "tmdb", "tvdb")},
			wantWinner: true, wantReason: coordinates3of3, checkErrs: true,
		},
		{
			// Weak S01E01-E03 coordinates from one source stay unmatched.
			name:       "rejects weak single-source coordinates",
			episodes:   weakEpisodes,
			hints:      hints("Shared", 0, weakPaths...),
			candidates: []MatchCandidate{tmdbCand("Shared", 0, "show")},
		},
		{
			name:       "accepts distinctive single-source coordinates",
			episodes:   map[string][]EpisodeResult{"show": distinctiveEpisodes},
			hints:      hints("Shared", 0, distinctivePaths...),
			candidates: []MatchCandidate{tmdbCand("Shared", 0, "show")},
			wantWinner: true, wantReason: coordinates3of3,
		},
		{
			// Coordinates alone must not choose between exact-title candidates.
			name:       "rejects coordinate-only choice between exact titles",
			episodes:   map[string][]EpisodeResult{"first": distinctiveEpisodes, "second": {ep(2, 1, "Episode 1")}},
			hints:      hints("Shared", 0, distinctivePaths...),
			candidates: []MatchCandidate{tmdbCand("Shared", 0, "first"), tmdbCand("Shared", 0, "second")},
		},
		{
			name: "accepts unique multi-season shape",
			episodes: map[string][]EpisodeResult{
				"original": {ep(1, 10, "Episode 10"), ep(2, 8, "Episode 8"), ep(4, 6, "Episode 6")},
				"remake":   {ep(1, 10, "Episode 10"), ep(2, 8, "Episode 8")},
			},
			hints: hints("Shared", 0,
				"/tv/Shared/Season 1/Shared - S01E10.mkv",
				"/tv/Shared/Season 2/Shared - S02E08.mkv",
				"/tv/Shared/Season 4/Shared - S04E06.mkv"),
			candidates: []MatchCandidate{tmdbCand("Shared", 0, "remake"), tmdbCand("Shared", 0, "original")},
			wantWinner: true, wantTMDB: "original", wantReason: coordinates3of3, checkErrs: true,
		},
		{
			// Two coordinates from one season are insufficient shape evidence.
			name:       "rejects two-coordinate single-season shape",
			episodes:   map[string][]EpisodeResult{"candidate": {ep(2, 1, "Episode 1"), ep(2, 8, "Episode 8")}},
			hints:      hints("Shared", 0, "/tv/Shared/Season 2/Shared - S02E01.mkv", "/tv/Shared/Season 2/Shared - S02E08.mkv"),
			candidates: []MatchCandidate{tmdbCand("Shared", 0, "candidate")},
			checkErrs:  true,
		},
	}
}()

// TestValidateSeriesMatchByEpisodes_Decisions holds the single-provider
// validation cases that differ only in the provider's episodes, the local
// files and the candidates.
func TestValidateSeriesMatchByEpisodes_Decisions(t *testing.T) {
	t.Parallel()

	for _, tt := range validateSeriesMatchByEpisodesDecisionsCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &episodeValidationStubProvider{episodes: tt.episodes, errors: tt.errors}
			winner, errs := validateSeriesMatchByEpisodes(context.Background(), tt.hints, tt.candidates, []Provider{provider}, "en")
			if tt.checkErrs && len(errs) != tt.wantErrs {
				t.Fatalf("validation errors = %v, want %d", errs, tt.wantErrs)
			}
			if !tt.wantWinner {
				if winner != nil {
					t.Fatalf("winner = %+v, want unmatched", winner)
				}
				return
			}
			if winner == nil {
				t.Fatal("winner = nil, want a corroborated candidate")
			}
			if tt.wantTMDB != "" && winner.ProviderIDs["tmdb"] != tt.wantTMDB {
				t.Fatalf("winner = %+v, want tmdb %s", winner, tt.wantTMDB)
			}
			if tt.wantReason != "" && !containsString(winner.MatchReasons, tt.wantReason) {
				t.Fatalf("winner reasons = %v, want %s", winner.MatchReasons, tt.wantReason)
			}
			if tt.wantCalls != 0 && provider.callCount() != tt.wantCalls {
				t.Fatalf("provider calls = %d, want %d", provider.callCount(), tt.wantCalls)
			}
		})
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
