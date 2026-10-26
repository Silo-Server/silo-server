package metadata

import (
	"errors"
	"io/fs"
	"maps"
	"slices"
)

// ErrLocalMetadataUnavailable is returned by a local provider (the built-in
// NFO reader) when a sidecar or the folder that should hold it exists in the
// catalog but cannot be read: a dropped network mount, an I/O error, a
// permission problem. It is not "no sidecar". A pass that replaces stored
// values stops rather than let remote values overwrite what the sidecar set,
// and an initial match waits (see localMetadataTemporarilyUnavailable) rather
// than commit an identity the sidecar may contradict.
var ErrLocalMetadataUnavailable = errors.New("local metadata unavailable")

// keepStoredValuesOnLocalReadError reports whether a pass must stop because a
// local sidecar could not be read. Only passes that replace stored values
// stop; fill-empty passes leave stored values alone and carry on with what
// the local provider could read.
func keepStoredValuesOnLocalReadError(mode RefreshMode, err error) bool {
	return mode == ModeManualRefresh && errors.Is(err, ErrLocalMetadataUnavailable)
}

// localMetadataTemporarilyUnavailable reports whether a match pass met a
// local sidecar it could not read for a reason that can clear by itself: an
// I/O error, a dropped mount, a vanished folder. An initial match waits for
// those. A permission problem does not clear by itself, so the match goes
// ahead without the sidecar, as it does when there is none; a manual refresh
// still stops on it.
func localMetadataTemporarilyUnavailable(errs []error) bool {
	return slices.ContainsFunc(errs, func(err error) bool {
		return errors.Is(err, ErrLocalMetadataUnavailable) && !errors.Is(err, fs.ErrPermission)
	})
}

// localSidecarDenied reports a local sidecar the server is not allowed to
// read. That does not clear by itself, so matching treats it like a missing
// sidecar: neither a provider failure nor a reason to wait. A manual refresh
// still stops on it.
func localSidecarDenied(err error) bool {
	return errors.Is(err, ErrLocalMetadataUnavailable) && errors.Is(err, fs.ErrPermission)
}

// targetSeasonPaths narrows a series' per-season paths to the season a
// target refresh replaces, so another season's unreadable sidecar cannot
// block it. A season with no local paths gets none, as in the full pass.
func targetSeasonPaths(paths map[int][]string, seasonNumber int) map[int][]string {
	seasonPaths, ok := paths[seasonNumber]
	if !ok {
		return nil
	}
	return map[int][]string{seasonNumber: seasonPaths}
}

// showFilePaths lists every cataloged episode media file of a show, season
// by season, the evidence that a folder still holds the show when one of its
// files is gone.
func showFilePaths(episodeFilePaths map[int]map[int][]string) []string {
	var files []string
	for _, season := range slices.Sorted(maps.Keys(episodeFilePaths)) {
		episodes := episodeFilePaths[season]
		for _, episode := range slices.Sorted(maps.Keys(episodes)) {
			files = append(files, episodes[episode]...)
		}
	}
	return files
}

// targetEpisodeFilePaths narrows a season's episode files to the episode a
// target refresh replaces; episodeNumber 0 targets the whole season.
func targetEpisodeFilePaths(paths map[int][]string, episodeNumber int) map[int][]string {
	if episodeNumber <= 0 {
		return paths
	}
	return map[int][]string{episodeNumber: paths[episodeNumber]}
}
