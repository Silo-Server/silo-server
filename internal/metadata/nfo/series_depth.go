package nfo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"

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
// charge. A season.nfo that cannot be read skips only that season's NFO: the
// call returns the seasons it could read together with the first error
// (metadata.ErrLocalMetadataUnavailable), so a pass that replaces stored
// values can stop and a fill-empty pass can keep the rest.
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

	finder := newSidecarFinder(ctx)
	lookup := p.newLookup(ctx)
	var seasons []metadata.SeasonResult
	var readErr error
	for _, number := range seasonNumbers {
		dirs := compactNFOPaths(req.SeasonDirectoryPaths[number])
		parsed, err := parseSeasonSidecar(ctx, lookup, dirs, req.ShowFilePaths, number)
		readErr = keepReadErr(readErr, err)
		poster := finder.findSeasonPoster(dirs, seriesRoots, number)
		if parsed == nil && poster == "" {
			continue
		}
		season := metadata.SeasonResult{SeasonNumber: number}
		if parsed != nil {
			season.Title = parsed.Title
			season.Overview = parsed.Overview
		}
		season.PosterPath = poster
		seasons = append(seasons, season)
	}
	return seasons, readErr
}

// parseSeasonSidecar loads the first parseable season.nfo (any case) across
// the season's candidate directories. A declared <seasonnumber> that
// disagrees with the directory-derived number is advisory: naming owns
// structure, so the directory number wins with a warning.
func parseSeasonSidecar(ctx context.Context, lookup *sidecarLookup, seasonDirs, showFiles []string, seasonNumber int) (*parsedNFO, error) {
	var readErr error
	for _, dir := range seasonDirs {
		// Media the server may not reach for permission is still there, so
		// the folder is still read, as in findItemNFO.
		if err := lookup.folderMedia(dir, showFiles); err != nil {
			readErr = keepReadErr(readErr, err)
			if !errors.Is(err, fs.ErrPermission) {
				continue
			}
		}
		path, parsed, err := lookup.find(dir, "season.nfo", typeSeason)
		readErr = keepReadErr(readErr, err)
		if err := confirmMounted(dir, showFiles); err != nil {
			readErr = keepReadErr(readErr, err)
			continue
		}
		if parsed == nil {
			continue
		}
		if parsed.SeasonSet && parsed.Season != seasonNumber {
			slog.WarnContext(ctx, "nfo: season.nfo seasonnumber disagrees with directory; preferring directory number",
				"component", "metadata", "path", path,
				"nfo_season", parsed.Season, "directory_season", seasonNumber)
		}
		return parsed, nil
	}
	return nil, readErr
}

// findSeasonPoster returns the source of a season's artwork: poster, folder,
// or cover inside the season folder first, then the Kodi-style seasonNN-poster
// (or season-specials-poster) in the series root. Names match ignoring case.
// In a flat layout the episodes' folder is the series root itself, whose
// poster.jpg is the series' own art, so it is not searched as a season folder.
func (f *sidecarFinder) findSeasonPoster(seasonDirs []string, seriesRoots []string, seasonNumber int) string {
	for _, name := range []string{"poster", "folder", "cover"} {
		for _, dir := range seasonDirs {
			if slices.Contains(seriesRoots, dir) {
				continue
			}
			if source := f.find(dir, name); source != "" {
				return source
			}
		}
	}
	rootNames := []string{fmt.Sprintf("season%02d-poster", seasonNumber)}
	if seasonNumber == 0 {
		rootNames = append(rootNames, "season-specials-poster")
	}
	for _, name := range rootNames {
		for _, dir := range seriesRoots {
			if source := f.find(dir, name); source != "" {
				return source
			}
		}
	}
	return ""
}

// GetEpisodes reads <basename>.nfo and <basename>-thumb (or <basename>) image
// sidecars for each episode media file of the requested season. Episodes with
// neither contribute nothing (synthesized fallback stays in charge of them).
// Like GetSeasons, an episode NFO that cannot be read skips only that
// episode's NFO, and the call returns the rest with the first error.
func (p *Provider) GetEpisodes(ctx context.Context, req metadata.EpisodesRequest) ([]metadata.EpisodeResult, error) {
	if len(req.EpisodeFilePaths) == 0 {
		return nil, nil
	}
	episodeNumbers := make([]int, 0, len(req.EpisodeFilePaths))
	for number := range req.EpisodeFilePaths {
		episodeNumbers = append(episodeNumbers, number)
	}
	sort.Ints(episodeNumbers)

	finder := newSidecarFinder(ctx)
	lookup := p.newLookup(ctx)
	// Every cataloged file of the show, to tell a deleted episode from a
	// missing share even when the request names only one episode and its
	// folder holds other seasons too.
	showFiles := slices.Clone(req.ShowFilePaths)
	for _, paths := range req.EpisodeFilePaths {
		showFiles = append(showFiles, paths...)
	}
	showFiles = compactNFOPaths(showFiles)
	var episodes []metadata.EpisodeResult
	var readErr error
	for _, number := range episodeNumbers {
		paths := compactNFOPaths(req.EpisodeFilePaths[number])
		parsed, thumb, err := findEpisodeSidecars(ctx, lookup, finder, paths, showFiles, req.SeasonNumber, number)
		readErr = keepReadErr(readErr, err)
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
		episode.StillPath = thumb
		episodes = append(episodes, episode)
	}
	return episodes, readErr
}

// findEpisodeSidecars locates the episode's <basename>.nfo (any case) and its
// thumbnail across its media file paths: <basename>-thumb first, then an image
// named <basename> itself. Numbers declared in the NFO are checked against the
// filename-derived SxxEyy, which wins on conflict (naming owns structure).
func findEpisodeSidecars(ctx context.Context, lookup *sidecarLookup, finder *sidecarFinder, mediaPaths, showFiles []string, seasonNumber, episodeNumber int) (*parsedNFO, string, error) {
	var parsed *parsedNFO
	var readErr error
	for _, mediaPath := range mediaPaths {
		dir := filepath.Dir(mediaPath)
		if err := mediaPresent(mediaPath); err != nil {
			if !lookup.deletedFromLiveFolder(err, mediaPath, showFiles) {
				readErr = keepReadErr(readErr, err)
			}
			continue
		}
		path, found, err := lookup.find(dir, mediaBasename(mediaPath)+".nfo", typeEpisode)
		readErr = keepReadErr(readErr, err)
		if err := confirmMounted(dir, []string{mediaPath}); err != nil {
			readErr = keepReadErr(readErr, err)
			continue
		}
		if found != nil {
			parsed = checkEpisodeSidecar(ctx, path, found, seasonNumber, episodeNumber)
			readErr = nil // another file of the episode supplied its NFO
			break
		}
	}
	for _, suffix := range []string{"-thumb", ""} {
		for _, mediaPath := range mediaPaths {
			if thumb := finder.find(filepath.Dir(mediaPath), mediaBasename(mediaPath)+suffix); thumb != "" {
				return parsed, thumb, readErr
			}
		}
	}
	return parsed, "", readErr
}

// checkEpisodeSidecar warns about the episode NFO forms naming overrides: a
// multi-episode document and numbers that disagree with the filename.
func checkEpisodeSidecar(ctx context.Context, path string, parsed *parsedNFO, seasonNumber, episodeNumber int) *parsedNFO {
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
