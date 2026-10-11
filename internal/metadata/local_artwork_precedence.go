package metadata

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

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

// refreshWritesLocalizationOnly reports whether a refresh of req.ContentID in
// req.Language writes only localization rows (canonicalWriteLanguage). Local
// sidecar art is language-neutral and belongs to the base rows, so such a
// refresh sets local season and episode art aside and the localization rows
// take the provider's localized art, as item localization rows already do.
func (s *MetadataService) refreshWritesLocalizationOnly(ctx context.Context, req ProcessRequest) bool {
	if s == nil || s.itemRepo == nil || req.ContentID == "" {
		return false
	}
	item, err := s.itemRepo.GetByID(ctx, req.ContentID)
	if err != nil || item == nil {
		return false
	}
	_, canonical := canonicalWriteLanguage(req, item, intSliceToFields(item.LockedFields))
	return !canonical
}

// localArtworkRetention keeps cached local artwork through a refresh that did
// not rediscover it. A library mount that is down, or a sidecar that is
// briefly unreadable, yields no local candidate; without this, rated remote
// artwork would replace the cached local copy. The local copy gives way only
// when the refresh can confirm the sidecar is gone (see sidecarGone).
type localArtworkRetention struct {
	ctx       context.Context
	contentID string
	// probes memoizes folder probes, so the episodes of one season folder
	// cost one probe.
	probes map[string]rootcheck.Result
}

func newLocalArtworkRetention(ctx context.Context, contentID string) *localArtworkRetention {
	return &localArtworkRetention{ctx: ctx, contentID: contentID, probes: make(map[string]rootcheck.Result)}
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
func keepUnconfirmedLocalItemArtwork(ctx context.Context, item, existing *models.MediaItem) {
	if item == nil || existing == nil {
		return
	}
	retention := newLocalArtworkRetention(ctx, existing.ContentID)
	stored := itemArtworkFields(existing)
	for i, field := range itemArtworkFields(item) {
		if retention.keep(*field.path, *stored[i].path, *stored[i].source) {
			*field.path = *stored[i].path
		}
	}
}

// sidecarGone reports whether the refresh can confirm that a local source no
// longer supplies artwork. Only the sidecar's own folder can confirm it: the
// folder must answer and list entries, which shows the library is mounted.
// Then the file is either missing, or still on disk and passed over by
// discovery (it no longer qualifies, another sidecar now wins, or local
// artwork is off). An unmounted share leaves the folder missing or its mount
// point empty, and an unreadable or unresponsive folder proves nothing, so in
// those cases the local copy stays. The folder is probed through rootcheck,
// with its timeout, before the file itself is looked up.
func (r *localArtworkRetention) sidecarGone(source string) bool {
	path, ok := localImageSourceFilePath(source)
	if !ok || !filepath.IsAbs(path) {
		return true
	}
	dir := filepath.Dir(path)
	folder, ok := r.probes[dir]
	if !ok {
		folder = rootcheck.ProbeWithTimeout(r.ctx, dir, rootcheck.DefaultProbeTimeout)
		r.probes[dir] = folder
	}
	if !folder.Reachable || folder.Empty {
		return false
	}
	_, err := os.Lstat(path)
	return err == nil || errors.Is(err, fs.ErrNotExist)
}
