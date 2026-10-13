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
	"github.com/Silo-Server/silo-server/internal/naming"
)

// Provider implements metadata.EpisodeProvider for its content types using
// the request plumbing added in Phase D: per-season directory paths and
// per-episode media file paths. NFO files never create structure — season
// and episode numbers come from directory/filename parsing upstream and the
// numbers declared inside an NFO are advisory only.

// GetSeasons reads season.nfo sidecars and season posters for each
// directory-derived season, with what the show's tvshow.nfo says about the
// season: its <namedseason> title and its <thumb season="N"> poster. A
// season.nfo title and a poster file win over them. A season with none of
// these contributes nothing, leaving remote providers (or fallback synthesis)
// in charge. A season.nfo that cannot be read skips only that season's NFO:
// the call returns the seasons it could read together with the first error
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
	// The show's tvshow.nfo, found and checked as the show-level lookup does:
	// a root that has lost the show's files (an unmounted share's mountpoint
	// holding an old tvshow.nfo) supplies nothing, and an unreadable one
	// makes a replacing season refresh stop rather than take remote names.
	showPath, show, readErr := lookup.findItemNFO(compactNFOPaths(req.ShowFilePaths), seriesRoots, typeSeries)
	var seasons []metadata.SeasonResult
	for _, number := range seasonNumbers {
		dirs := compactNFOPaths(req.SeasonDirectoryPaths[number])
		_, parsed, err := parseSeasonSidecar(ctx, lookup, dirs, req.ShowFilePaths, number)
		readErr = keepReadErr(readErr, err)
		season := metadata.SeasonResult{SeasonNumber: number}
		if parsed != nil {
			season.Title = parsed.Title
			season.Overview = parsed.Overview
			season.AirDate = parsed.FirstAirDate
		}
		season.PosterPath = finder.findSeasonPoster(dirs, seriesRoots, number)
		if show != nil {
			if season.Title == "" {
				season.Title = show.SeasonNames[number]
			}
			if season.PosterPath == "" && len(show.Art) > 0 {
				resolver := newArtResolver(finder, showPath, append(slices.Clone(seriesRoots), dirs...))
				season.PosterPath = firstArtSource(show.Art, artPoster, number, true, resolver)
			}
		}
		if parsed == nil && season.Title == "" && season.PosterPath == "" {
			continue
		}
		seasons = append(seasons, season)
	}
	return seasons, readErr
}

// parseSeasonSidecar loads the first parseable season.nfo (any case) across
// the season's candidate directories and returns its path and contents. A
// declared <seasonnumber> that disagrees with the directory-derived number is
// advisory: naming owns structure, so the directory number wins with a
// warning.
func parseSeasonSidecar(ctx context.Context, lookup *sidecarLookup, seasonDirs, showFiles []string, seasonNumber int) (string, *parsedNFO, error) {
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
		return path, parsed, nil
	}
	return "", nil, readErr
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
// An episode with no thumb file takes the <thumb> its NFO names. Like
// GetSeasons, an episode NFO that cannot be read skips only that episode's
// NFO, and the call returns the rest with the first error.
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
	showFiles := episodeShowFiles(req)
	seriesRoots := compactNFOPaths(req.SeriesRootPaths)
	var episodes []metadata.EpisodeResult
	// nfoEpisodes counts the episodes each NFO file describes: one file
	// beside a multi-episode video serves every episode in it.
	nfoEpisodes := make(map[string][]int)
	var readErr error
	for _, number := range episodeNumbers {
		paths := compactNFOPaths(req.EpisodeFilePaths[number])
		nfoPath, parsed, thumb, err := findEpisodeSidecars(ctx, lookup, finder, paths, showFiles, req.SeasonNumber, number)
		readErr = keepReadErr(readErr, err)
		if parsed == nil && thumb == "" {
			continue
		}
		episode := metadata.EpisodeResult{
			SeasonNumber:  req.SeasonNumber,
			EpisodeNumber: number,
			StillPath:     thumb,
		}
		if parsed != nil {
			episode.Title = parsed.Title
			episode.Overview = parsed.Overview
			episode.AirDate = parsed.FirstAirDate
			episode.Runtime = parsed.Runtime
			// The Rotten Tomatoes values ride along, but episodes have no
			// column for them, so only IMDb and TMDB are stored.
			episode.Ratings = metadata.Ratings{
				IMDB:       parsed.RatingIMDB,
				TMDB:       parsed.RatingTMDB,
				RTCritic:   parsed.RatingRTCritic,
				RTAudience: parsed.RatingRTAudience,
			}
			episode.ProviderIDs = episodeProviderIDs(nfoPath, parsed, req.SeasonNumber, number)
			nfoEpisodes[nfoPath] = append(nfoEpisodes[nfoPath], len(episodes))
			if episode.StillPath == "" && len(parsed.Art) > 0 {
				folders := append(slices.Clone(seriesRoots), filepath.Dir(nfoPath))
				episode.StillPath = firstArtSource(parsed.Art, artStill, 0, false, newArtResolver(finder, nfoPath, folders))
			}
		}
		episodes = append(episodes, episode)
	}
	// An episode id names one episode; a file serving several can't say
	// which its ids belong to.
	for _, indexes := range nfoEpisodes {
		if len(indexes) > 1 {
			for _, i := range indexes {
				episodes[i].ProviderIDs = nil
			}
		}
	}
	return episodes, readErr
}

// episodeShowFiles returns every cataloged file of the show, to tell a
// deleted episode from a missing share even when the request names only one
// episode and its folder holds other seasons too.
func episodeShowFiles(req metadata.EpisodesRequest) []string {
	showFiles := slices.Clone(req.ShowFilePaths)
	for _, paths := range req.EpisodeFilePaths {
		showFiles = append(showFiles, paths...)
	}
	return compactNFOPaths(showFiles)
}

// findEpisodeSidecars locates the episode's <basename>.nfo (any case) and its
// thumbnail across its media file paths: <basename>-thumb first, then an image
// named <basename> itself. Numbers declared in the NFO are checked against the
// filename-derived SxxEyy, which wins on conflict (naming owns structure). It
// returns the NFO's path with its contents.
func findEpisodeSidecars(ctx context.Context, lookup *sidecarLookup, finder *sidecarFinder, mediaPaths, showFiles []string, seasonNumber, episodeNumber int) (string, *parsedNFO, string, error) {
	var nfoPath string
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
			nfoPath, parsed = path, checkEpisodeSidecar(ctx, path, found, seasonNumber, episodeNumber)
			readErr = nil // another file of the episode supplied its NFO
			break
		}
	}
	for _, suffix := range []string{"-thumb", ""} {
		for _, mediaPath := range mediaPaths {
			if thumb := finder.find(filepath.Dir(mediaPath), mediaBasename(mediaPath)+suffix); thumb != "" {
				return nfoPath, parsed, thumb, readErr
			}
		}
	}
	return nfoPath, parsed, "", readErr
}

// servesSeveralEpisodes reports whether an episode NFO sits beside a video
// whose name holds an episode range (Show S01E01-E02.nfo), read the way the
// scanner reads the video's name. Its ids can't say which episode they
// belong to, whichever episodes a request asks about: a targeted refresh
// names only one of them.
func servesSeveralEpisodes(nfoPath string) bool {
	hints := naming.ParseVariantHints(nfoPath, typeSeries)
	return hints.MultiEpisodeEnd > hints.MultiEpisodeStart
}

// episodeProviderIDs returns the ids the episode NFO at nfoPath gives the
// episode it sits beside (placeholder ids are already dropped by idSlots).
// The ids name one episode, so they are dropped when the file describes
// several (a multi-episode file), sits beside a video named for several
// (servesSeveralEpisodes), or declares numbers other than the filename's:
// each way they may belong to another episode, and an episode id is unique
// across the catalog.
func episodeProviderIDs(nfoPath string, parsed *parsedNFO, seasonNumber, episodeNumber int) map[string]string {
	if parsed.MultiEpisode || servesSeveralEpisodes(nfoPath) ||
		(parsed.SeasonSet && parsed.Season != seasonNumber) ||
		(parsed.EpisodeSet && parsed.Episode != episodeNumber) {
		return nil
	}
	if ids := parsed.providerIDs(); len(ids) > 0 {
		return ids
	}
	return nil
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
