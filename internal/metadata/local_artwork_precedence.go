package metadata

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/models"
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
}

func (s *MetadataService) newLocalArtworkRetention(ctx context.Context, contentID string) *localArtworkRetention {
	return &localArtworkRetention{ctx: ctx, service: s, contentID: contentID}
}

// keep reports whether a slot holding cached artwork from existingSource, a
// local sidecar, should ignore the remote incoming path this refresh chose.
func (r *localArtworkRetention) keep(incoming, existingPath, existingSource string) bool {
	if !isCachedImagePath(existingPath) ||
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
//   - the file is missing while its folder, or the library root holding it,
//     lists entries, so the library is mounted and the file was removed.
//
// A missing file under an empty or missing folder and root is what an
// unmounted library looks like, so the local copy stays.
func (r *localArtworkRetention) sidecarGone(source string) bool {
	path, ok := localImageSourceFilePath(source)
	if !ok || !filepath.IsAbs(path) {
		return true
	}
	_, err := os.Lstat(path)
	if err == nil {
		return true
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if dirListsEntries(filepath.Dir(path)) {
		return true
	}
	for _, root := range r.libraryRoots() {
		if localImagePathWithinRoots(path, []string{root}) && dirListsEntries(root) {
			return true
		}
	}
	return false
}

func (r *localArtworkRetention) libraryRoots() []string {
	if r.rootsLoaded {
		return r.roots
	}
	r.rootsLoaded = true
	roots, err := r.service.configuredNamingRootsForContent(r.ctx, r.contentID, 0)
	if err != nil {
		slog.DebugContext(r.ctx, "metadata: cannot load library roots to check local artwork", "component", "metadata",
			"content_id", r.contentID, "error", err)
		return nil
	}
	r.roots = roots
	return roots
}

// dirListsEntries reports whether dir can be read and holds at least one
// entry. An unmounted mount point is typically an empty directory.
func dirListsEntries(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(1)
	return err == nil && len(names) > 0
}
