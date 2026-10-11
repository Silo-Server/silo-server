package metadata

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
)

// Local sidecar artwork outranks remote providers regardless of where the
// local provider sits in the chain, as item artwork already does in
// applyBestImages. Season and episode results otherwise merge first-provider-
// wins, so a remote poster from an earlier provider would hide a local one.

// preferLocalSeasonPoster gives target the local poster from source when
// target's accumulated poster is not local.
func preferLocalSeasonPoster(target *SeasonResult, source SeasonResult) {
	if isLocalImageSourcePath(source.PosterPath) && !isLocalImageSourcePath(target.PosterPath) {
		target.PosterPath = source.PosterPath
		target.PosterThumbhash = source.PosterThumbhash
	}
}

// preferLocalEpisodeStill gives target the local still from source when
// target's accumulated still is not local.
func preferLocalEpisodeStill(target *EpisodeResult, source EpisodeResult) {
	if isLocalImageSourcePath(source.StillPath) && !isLocalImageSourcePath(target.StillPath) {
		target.StillPath = source.StillPath
		target.StillThumbhash = source.StillThumbhash
	}
}

// localArtworkRetention keeps cached local artwork through a refresh that did
// not rediscover it. A library mount that is down, or a sidecar that is
// briefly unreadable, yields no local candidate; without this, rated remote
// artwork would replace the cached local copy. The local copy gives way only
// when the refresh can confirm the sidecar is gone (see sidecarGone).
type localArtworkRetention struct {
	ctx       context.Context
	service   *MetadataService
	contentID string

	rootsLoaded bool
	roots       []string
	// probes memoizes folder probes, so the episodes of one season folder
	// cost one probe.
	probes map[string]rootcheck.Result
}

func (s *MetadataService) newLocalArtworkRetention(ctx context.Context, contentID string) *localArtworkRetention {
	return &localArtworkRetention{ctx: ctx, service: s, contentID: contentID, probes: make(map[string]rootcheck.Result)}
}

// keep reports whether a slot serving a cached copy of a local sidecar should
// ignore the remote incoming path this refresh chose.
func (r *localArtworkRetention) keep(incoming, existingPath, existingSource string) bool {
	if !artworkkey.IsLocal(existingPath) ||
		!isLocalImageSourcePath(existingSource) ||
		!isRemoteImageSourcePath(incoming) ||
		r.sidecarGone(existingSource) {
		return false
	}
	slog.DebugContext(r.ctx, "metadata: keeping cached local artwork the refresh did not rediscover", "component", "metadata",
		"content_id", r.contentID, "source", existingSource, "remote", incoming)
	return true
}

// providerPath returns the path a season or episode slot should treat as the
// provider's choice: the stored local source when keep holds, else incoming.
func (r *localArtworkRetention) providerPath(incoming, existingPath, existingSource string) string {
	if r.keep(incoming, existingPath, existingSource) {
		return existingSource
	}
	return incoming
}

// keepUnconfirmedLocalItemArtwork restores the cached path of each item
// artwork slot that applyBestImages moved from cached local art to a remote
// image, when keep holds. prepareItemImagesForQueue then keeps the stored
// local source.
func (s *MetadataService) keepUnconfirmedLocalItemArtwork(ctx context.Context, item, existing *models.MediaItem) {
	if item == nil || existing == nil {
		return
	}
	retention := s.newLocalArtworkRetention(ctx, existing.ContentID)
	stored := itemArtworkFields(existing)
	for i, field := range itemArtworkFields(item) {
		if retention.keep(*field.path, *stored[i].path, *stored[i].source) {
			*field.path = *stored[i].path
		}
	}
}

// sidecarGone reports whether the refresh can confirm that a local source no
// longer supplies artwork:
//   - the file is still on disk, so discovery saw it and passed it over (it no
//     longer qualifies, another sidecar now wins, or local artwork is off);
//   - the file is missing and the nearest existing folder above it, searching
//     no higher than the library root, lists entries: the library is mounted
//     and the file or its folder was removed.
//
// An unmounted share leaves its mount point empty or missing, so a missing file
// under an empty or missing folder keeps the local copy, as does a folder that
// cannot be read or does not answer in time. Stopping at the library root
// means a parent of several mounts cannot vouch for one of them. Without a
// known library root only the file's own folder counts. Folders are probed
// through rootcheck, with its timeout, before the file itself is looked up.
func (r *localArtworkRetention) sidecarGone(source string) bool {
	path, ok := localImageSourceFilePath(source)
	if !ok || !filepath.IsAbs(path) {
		return true
	}
	folder := r.probeFolder(filepath.Dir(path))
	switch {
	case folder.Reachable:
		_, err := os.Lstat(path)
		if err == nil {
			return true
		}
		return errors.Is(err, fs.ErrNotExist) && !folder.Empty
	case folder.ErrorCode == rootcheck.ErrCodeNotFound:
		return r.nearestFolderListsEntries(filepath.Dir(filepath.Dir(path)), r.rootsContaining(path))
	default:
		return false
	}
}

// probeFolder probes dir once per retention.
func (r *localArtworkRetention) probeFolder(dir string) rootcheck.Result {
	if probe, ok := r.probes[dir]; ok {
		return probe
	}
	probe := rootcheck.ProbeWithTimeout(r.ctx, dir, rootcheck.DefaultProbeTimeout)
	r.probes[dir] = probe
	return probe
}

// nearestFolderListsEntries walks up from dir to the first folder that exists,
// going no higher than the library roots, and reports whether it lists
// entries.
func (r *localArtworkRetention) nearestFolderListsEntries(dir string, roots []string) bool {
	if len(roots) == 0 {
		return false
	}
	for {
		probe := r.probeFolder(dir)
		if probe.Reachable {
			return !probe.Empty
		}
		if probe.ErrorCode != rootcheck.ErrCodeNotFound || slices.Contains(roots, dir) {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// rootsContaining returns the item's library roots that hold path.
func (r *localArtworkRetention) rootsContaining(path string) []string {
	if !r.rootsLoaded {
		r.rootsLoaded = true
		roots, err := r.service.configuredNamingRootsForContent(r.ctx, r.contentID, 0)
		if err != nil {
			slog.DebugContext(r.ctx, "metadata: cannot load library roots to check local artwork", "component", "metadata",
				"content_id", r.contentID, "error", err)
		}
		for _, root := range roots {
			if root = filepath.Clean(strings.TrimSpace(root)); filepath.IsAbs(root) {
				r.roots = append(r.roots, root)
			}
		}
	}
	var containing []string
	for _, root := range r.roots {
		if localImagePathWithinRoots(path, []string{root}) {
			containing = append(containing, root)
		}
	}
	return containing
}
