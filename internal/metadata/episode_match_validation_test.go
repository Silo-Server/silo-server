//nolint:goconst // Repeated provider IDs and episode titles make these fixtures readable.
package metadata

import (
	"errors"
)

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
