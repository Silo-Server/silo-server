package nfo

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

// The methods below implement metadata.LocalSidecarProvider. Each returns the
// sidecar files the matching provider method applies for the same request,
// found by the same lookups, so a change to any of them can be detected
// without restating the lookup rules elsewhere. MetadataErr is the lookup's
// error: no NFO applies, and one exists that cannot be read or sits on a
// share that is not there (metadata.ErrLocalMetadataUnavailable), so it may
// be the one that should apply.

// ItemSidecarPaths returns the NFO file GetMetadata reads and the artwork
// GetImages finds for an item.
func (p *Provider) ItemSidecarPaths(ctx context.Context, req metadata.MetadataRequest) metadata.LocalSidecars {
	mediaFiles, sidecarDirs := requestPaths(req)
	nfoPath, _, err := p.newLookup(ctx).findItemNFO(mediaFiles, sidecarDirs, req.ContentType)
	found := metadata.LocalSidecars{MetadataErr: err}
	if nfoPath != "" {
		found.Metadata = []string{nfoPath}
	}
	images, _ := p.GetImages(ctx, metadata.ImageRequest{
		ContentType:               req.ContentType,
		RepresentativeFilePath:    req.RepresentativeFilePath,
		AllGroupFilePaths:         req.AllGroupFilePaths,
		PrimarySidecarSearchPaths: req.PrimarySidecarSearchPaths,
	})
	for _, image := range images {
		if path, ok := artworkFile(image.URL); ok {
			found.Artwork = append(found.Artwork, path)
		}
	}
	return found
}

// artworkFingerprint matches the "?v=<size>-<mtime>" suffix the image cache
// may key a local artwork source on.
var artworkFingerprint = regexp.MustCompile(`\?v=\d+--?\d+$`)

// artworkFile returns the file an artwork source names. A source is a plain
// path or a file:// URL, which may end in a fingerprint; any other URL names
// no local file.
func artworkFile(source string) (string, bool) {
	if path, ok := strings.CutPrefix(source, "file://"); ok {
		return artworkFingerprint.ReplaceAllString(path, ""), true
	}
	if source == "" || strings.Contains(source, "://") {
		return "", false
	}
	return source, true
}

// SeasonSidecarPaths returns, per season number, the files GetSeasons
// applies: the season.nfo, the season poster, and the show's tvshow.nfo where
// it names the season or its poster. Every requested season has an
// entry, empty when it has no sidecars.
func (p *Provider) SeasonSidecarPaths(ctx context.Context, req metadata.SeasonsRequest) map[int]metadata.LocalSidecars {
	if (req.ContentType != "" && req.ContentType != typeSeries) || len(req.SeasonDirectoryPaths) == 0 {
		return nil
	}
	seriesRoots := compactNFOPaths(req.SeriesRootPaths)
	finder := newSidecarFinder(ctx)
	lookup := p.newLookup(ctx)
	showPath, show, showErr := lookup.findItemNFO(compactNFOPaths(req.ShowFilePaths), seriesRoots, typeSeries)
	out := make(map[int]metadata.LocalSidecars, len(req.SeasonDirectoryPaths))
	for number, dirs := range req.SeasonDirectoryPaths {
		dirs = compactNFOPaths(dirs)
		nfoPath, parsed, err := parseSeasonSidecar(ctx, lookup, dirs, req.ShowFilePaths, number)
		found := metadata.LocalSidecars{MetadataErr: keepReadErr(showErr, err)}
		if nfoPath != "" {
			found.Metadata = append(found.Metadata, nfoPath)
		}
		// The show's NFO counts only where it names the season or its poster,
		// as GetSeasons uses it; when it starts or stops doing so, the set of
		// files changes.
		showNames := show != nil && (parsed == nil || parsed.Title == "") && show.SeasonNames[number] != ""
		poster := finder.findSeasonPoster(dirs, seriesRoots, number)
		if poster == "" && show != nil && len(show.Art) > 0 {
			resolver := newArtResolver(finder, showPath, append(slices.Clone(seriesRoots), dirs...))
			if poster = firstArtSource(show.Art, artPoster, number, true, resolver); poster != "" {
				showNames = true
			}
		}
		if showNames {
			found.Metadata = append(found.Metadata, showPath)
		}
		if poster, ok := artworkFile(poster); ok {
			found.Artwork = []string{poster}
		}
		out[number] = found
	}
	return out
}

// EpisodeSidecarPaths returns, per episode number of the requested season, the
// <basename>.nfo and still GetEpisodes applies: a thumb file, or the image
// the NFO names. Every requested episode has an entry, empty when it has no
// sidecars.
func (p *Provider) EpisodeSidecarPaths(ctx context.Context, req metadata.EpisodesRequest) map[int]metadata.LocalSidecars {
	finder := newSidecarFinder(ctx)
	lookup := p.newLookup(ctx)
	showFiles := episodeShowFiles(req)
	seriesRoots := compactNFOPaths(req.SeriesRootPaths)
	out := make(map[int]metadata.LocalSidecars, len(req.EpisodeFilePaths))
	for number, mediaPaths := range req.EpisodeFilePaths {
		mediaPaths = compactNFOPaths(mediaPaths)
		nfoPath, parsed, thumb, err := findEpisodeSidecars(ctx, lookup, finder, mediaPaths, showFiles, req.SeasonNumber, number)
		found := metadata.LocalSidecars{MetadataErr: err}
		if nfoPath != "" {
			found.Metadata = []string{nfoPath}
		}
		if thumb == "" && parsed != nil && len(parsed.Art) > 0 {
			folders := append(slices.Clone(seriesRoots), filepath.Dir(nfoPath))
			thumb = firstArtSource(parsed.Art, artStill, 0, false, newArtResolver(finder, nfoPath, folders))
		}
		if thumb, ok := artworkFile(thumb); ok {
			found.Artwork = []string{thumb}
		}
		out[number] = found
	}
	return out
}
