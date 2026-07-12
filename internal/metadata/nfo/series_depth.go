package nfo

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// Provider implements metadata.EpisodeProvider for its content types using
// the request plumbing added in Phase D: per-season directory paths and
// per-episode media file paths. NFO files never create structure — season
// and episode numbers come from directory/filename parsing upstream and the
// numbers declared inside an NFO are advisory only.

// GetSeasons reads season.nfo sidecars and season posters for each
// directory-derived season. A season with neither a season.nfo nor a poster
// contributes nothing, leaving remote providers (or fallback synthesis) in
// charge.
func (p *Provider) GetSeasons(ctx context.Context, req metadata.SeasonsRequest) ([]metadata.SeasonResult, error) {
	if req.ContentType != "" && req.ContentType != typeSeries {
		return nil, nil
	}
	if len(req.SeasonDirectoryPaths) == 0 {
		return nil, nil
	}
	seriesRoots := compactNFOPaths(req.SeriesRootPaths)
	seasonNumbers := make([]int, 0, len(req.SeasonDirectoryPaths))
	for number := range req.SeasonDirectoryPaths {
		seasonNumbers = append(seasonNumbers, number)
	}
	sort.Ints(seasonNumbers)

	var seasons []metadata.SeasonResult
	for _, number := range seasonNumbers {
		dirs := compactNFOPaths(req.SeasonDirectoryPaths[number])
		parsed := parseSeasonSidecar(ctx, dirs, number)
		poster := findSeasonPoster(dirs, seriesRoots, number)
		if parsed == nil && poster == "" {
			continue
		}
		season := metadata.SeasonResult{SeasonNumber: number}
		if parsed != nil {
			season.Title = parsed.Title
			season.Overview = parsed.Overview
		}
		if poster != "" {
			season.PosterPath = "file://" + poster
		}
		seasons = append(seasons, season)
	}
	return seasons, nil
}

// parseSeasonSidecar loads the first parseable season.nfo across the season's
// candidate directories. A declared <seasonnumber> that disagrees with the
// directory-derived number is advisory: naming owns structure, so the
// directory number wins with a warning.
func parseSeasonSidecar(ctx context.Context, seasonDirs []string, seasonNumber int) *parsedNFO {
	for _, dir := range seasonDirs {
		path := filepath.Join(dir, "season.nfo")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		parsed, err := parseNFOData(data)
		if err != nil || parsed.Type != typeSeason {
			continue
		}
		if parsed.SeasonSet && parsed.Season != seasonNumber {
			slog.WarnContext(ctx, "nfo: season.nfo seasonnumber disagrees with directory; preferring directory number",
				"component", "metadata", "path", path,
				"nfo_season", parsed.Season, "directory_season", seasonNumber)
		}
		return parsed
	}
	return nil
}

// findSeasonPoster locates season artwork: poster/folder/cover inside the
// season directory first, then the Kodi-style seasonNN-poster form in the
// series root.
func findSeasonPoster(seasonDirs []string, seriesRoots []string, seasonNumber int) string {
	for _, name := range []string{"poster", "folder", "cover"} {
		for _, dir := range seasonDirs {
			if path := findLocalArtworkFile(dir, name); path != "" {
				return path
			}
		}
	}
	rootNames := []string{fmt.Sprintf("season%02d-poster", seasonNumber)}
	if seasonNumber == 0 {
		rootNames = append(rootNames, "season-specials-poster")
	}
	for _, name := range rootNames {
		for _, dir := range seriesRoots {
			if path := findLocalArtworkFile(dir, name); path != "" {
				return path
			}
		}
	}
	return ""
}

// GetEpisodes reads <basename>.nfo and <basename>-thumb sidecars for each
// episode media file of the requested season. Episodes with neither
// contribute nothing (synthesized fallback stays in charge of them).
func (p *Provider) GetEpisodes(ctx context.Context, req metadata.EpisodesRequest) ([]metadata.EpisodeResult, error) {
	if len(req.EpisodeFilePaths) == 0 {
		return nil, nil
	}
	episodeNumbers := make([]int, 0, len(req.EpisodeFilePaths))
	for number := range req.EpisodeFilePaths {
		episodeNumbers = append(episodeNumbers, number)
	}
	sort.Ints(episodeNumbers)

	var episodes []metadata.EpisodeResult
	for _, number := range episodeNumbers {
		paths := compactNFOPaths(req.EpisodeFilePaths[number])
		parsed, thumb := findEpisodeSidecars(ctx, paths, req.SeasonNumber, number)
		if parsed == nil && thumb == "" {
			continue
		}
		episode := metadata.EpisodeResult{
			SeasonNumber:  req.SeasonNumber,
			EpisodeNumber: number,
		}
		if parsed != nil {
			episode.Title = parsed.Title
			episode.Overview = parsed.Overview
			episode.AirDate = parsed.FirstAirDate
			episode.Runtime = parsed.Runtime
			episode.Ratings = metadata.Ratings{
				IMDB:       parsed.RatingIMDB,
				TMDB:       parsed.RatingTMDB,
				RTCritic:   parsed.RatingRTCritic,
				RTAudience: parsed.RatingRTAudience,
			}
		}
		if thumb != "" {
			episode.StillPath = "file://" + thumb
		}
		episodes = append(episodes, episode)
	}
	return episodes, nil
}

// findEpisodeSidecars locates the episode's <basename>.nfo and
// <basename>-thumb image across its media file paths. Numbers declared in
// the NFO are checked against the filename-derived SxxEyy, which wins on
// conflict (naming owns structure).
func findEpisodeSidecars(ctx context.Context, mediaPaths []string, seasonNumber, episodeNumber int) (*parsedNFO, string) {
	var parsed *parsedNFO
	thumb := ""
	for _, mediaPath := range mediaPaths {
		dir := filepath.Dir(mediaPath)
		base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
		if parsed == nil {
			parsed = parseEpisodeSidecar(ctx, filepath.Join(dir, base+".nfo"), seasonNumber, episodeNumber)
		}
		if thumb == "" {
			thumb = findLocalArtworkFile(dir, base+"-thumb")
		}
		if parsed != nil && thumb != "" {
			break
		}
	}
	return parsed, thumb
}

func parseEpisodeSidecar(ctx context.Context, path string, seasonNumber, episodeNumber int) *parsedNFO {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	parsed, err := parseNFOData(data)
	if err != nil || parsed.Type != typeEpisode {
		return nil
	}
	if parsed.MultiEpisode {
		slog.WarnContext(ctx, "nfo: multi-episode NFO documents are not supported; using the first <episodedetails> block",
			"component", "metadata", "path", path)
	}
	if (parsed.SeasonSet && parsed.Season != seasonNumber) ||
		(parsed.EpisodeSet && parsed.Episode != episodeNumber) {
		slog.WarnContext(ctx, "nfo: episode NFO numbers disagree with filename; preferring filename numbers",
			"component", "metadata", "path", path,
			"nfo_season", parsed.Season, "nfo_episode", parsed.Episode,
			"file_season", seasonNumber, "file_episode", episodeNumber)
	}
	return parsed
}
