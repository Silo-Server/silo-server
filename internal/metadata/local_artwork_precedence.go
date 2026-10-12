package metadata

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
)

// Local sidecar artwork outranks remote providers regardless of where the
// local provider sits in the chain, as item artwork already does in
// applyBestImages. Season and episode results otherwise merge first-provider-
// wins, so a remote poster from an earlier provider would hide a local one.

// preferLocalSeasonPoster settles the poster before source merges into target
// with MergeFillEmpty. A local poster in source replaces target's remote one,
// and the first remote poster beside a local one is kept for localization rows
// (SeasonResult.remotePosterPath). Once settled, source's poster is cleared so
// the merge cannot fill a local poster's empty thumbhash with a remote one.
func preferLocalSeasonPoster(target, source *SeasonResult) {
	sourceLocal := isLocalImageSourcePath(source.PosterPath)
	targetLocal := isLocalImageSourcePath(target.PosterPath)
	switch {
	case sourceLocal && !targetLocal:
		if target.PosterPath != "" {
			target.remotePosterPath, target.remotePosterThumbhash = target.PosterPath, target.PosterThumbhash
		}
		target.PosterPath, target.PosterThumbhash = source.PosterPath, source.PosterThumbhash
	case targetLocal && !sourceLocal && source.PosterPath != "":
		if target.remotePosterPath == "" {
			target.remotePosterPath, target.remotePosterThumbhash = source.PosterPath, source.PosterThumbhash
		}
	default:
		return
	}
	source.PosterPath, source.PosterThumbhash = "", ""
}

// preferLocalEpisodeStill settles the still before source merges into target,
// as preferLocalSeasonPoster does for season posters: a local still beats a
// remote one, and its thumbhash never comes from the remote still.
func preferLocalEpisodeStill(target, source *EpisodeResult) {
	sourceLocal := isLocalImageSourcePath(source.StillPath)
	if sourceLocal == isLocalImageSourcePath(target.StillPath) {
		return
	}
	if sourceLocal {
		target.StillPath, target.StillThumbhash = source.StillPath, source.StillThumbhash
	}
	source.StillPath, source.StillThumbhash = "", ""
}

type localArtworkSearchedKey struct{}

// localArtworkLevel names the artwork levels a refresh can search for local
// sidecars. Provider chains are configured per content level, so the local
// provider can run for one level and not another.
type localArtworkLevel uint8

const (
	localArtworkItem localArtworkLevel = 1 << iota
	localArtworkSeason
	localArtworkEpisode
)

// withLocalArtworkSearched marks ctx as serving a refresh that asked the local
// provider for sidecar artwork at levels. Only such a refresh can tell that
// cached local art at a level went undiscovered. At an unmarked level (local
// metadata off for it, an Identify, or local artwork unusable), cached local
// art is stored artwork like any other and no library folder is probed, so a
// wedged share costs a refresh that never searched it nothing.
func withLocalArtworkSearched(ctx context.Context, levels localArtworkLevel) context.Context {
	if levels == 0 {
		return ctx
	}
	return context.WithValue(ctx, localArtworkSearchedKey{}, localArtworkSearchedLevels(ctx)|levels)
}

func localArtworkSearchedLevels(ctx context.Context) localArtworkLevel {
	levels, _ := ctx.Value(localArtworkSearchedKey{}).(localArtworkLevel)
	return levels
}

type localMetadataUnreadableKey struct{}

// withLocalMetadataUnreadable marks ctx as serving a fill-empty refresh that
// went on without a local sidecar (an NFO) it could not read at levels. An
// image URL that NFO names cannot be confirmed gone there, so its cached copy
// stays (see sidecarGone).
func withLocalMetadataUnreadable(ctx context.Context, levels localArtworkLevel) context.Context {
	if levels == 0 {
		return ctx
	}
	return context.WithValue(ctx, localMetadataUnreadableKey{}, localMetadataUnreadableLevels(ctx)|levels)
}

func localMetadataUnreadableLevels(ctx context.Context) localArtworkLevel {
	levels, _ := ctx.Value(localMetadataUnreadableKey{}).(localArtworkLevel)
	return levels
}

// localArtworkRetention keeps cached local artwork through a refresh that did
// not rediscover it. A library mount that is down, or a sidecar that is
// briefly unreadable, yields no local candidate; without this, rated remote
// artwork would replace the cached local copy. The local copy gives way only
// when the refresh can confirm the sidecar is gone (see sidecarGone).
type localArtworkRetention struct {
	ctx       context.Context
	contentID string
	// searched is set when the refresh asked the local provider for artwork
	// at this retention's level (withLocalArtworkSearched); otherwise nothing
	// is kept.
	searched bool
	// metadataUnreadable is set when the refresh could not read the local
	// metadata at this level (withLocalMetadataUnreadable).
	metadataUnreadable bool
	// probes memoizes folder probes, so the episodes of one season folder
	// cost one probe. Retentions made with atLevel share it.
	probes map[string]rootcheck.Result
}

func newLocalArtworkRetention(ctx context.Context, contentID string, level localArtworkLevel) *localArtworkRetention {
	return &localArtworkRetention{
		ctx: ctx, contentID: contentID, searched: localArtworkSearchedLevels(ctx)&level != 0,
		metadataUnreadable: localMetadataUnreadableLevels(ctx)&level != 0,
		probes:             make(map[string]rootcheck.Result),
	}
}

// atLevel returns a retention for another level of the same content that
// shares r's folder probes, so a season's poster and its episodes' stills,
// which usually sit in the same folder, cost one probe.
func (r *localArtworkRetention) atLevel(level localArtworkLevel) *localArtworkRetention {
	other := *r
	other.searched = localArtworkSearchedLevels(r.ctx)&level != 0
	other.metadataUnreadable = localMetadataUnreadableLevels(r.ctx)&level != 0
	return &other
}

// keep reports whether a slot of imageType whose stored source is a local
// sidecar should ignore the remote incoming path this refresh chose. The slot
// serves the sidecar's cached copy or, while that copy is pending, the image
// it served before or a remote stand-in; either way the local source must
// survive a refresh that cannot see the sidecar.
func (r *localArtworkRetention) keep(imageType ImageType, incoming, existingSource string) bool {
	gone := r.sidecarGone
	if imageType == ImageBackdrop {
		gone = r.backdropSidecarGone
	}
	if !r.searched ||
		!isLocalImageSourcePath(existingSource) ||
		!isRemoteImageSourcePath(incoming) ||
		gone(existingSource) {
		return false
	}
	slog.DebugContext(r.ctx, "metadata: keeping cached local artwork the refresh did not rediscover", "component", "metadata",
		"content_id", r.contentID, "source", existingSource, "remote", incoming)
	return true
}

// providerPath returns the path a slot of imageType should treat as the
// provider's choice: the stored local source when keep holds, else incoming.
func (r *localArtworkRetention) providerPath(imageType ImageType, incoming, existingSource string) string {
	if r.keep(imageType, incoming, existingSource) {
		return existingSource
	}
	return incoming
}

// keepUnconfirmedLocalItemArtwork puts the stored local source back in each
// item artwork slot that applyBestImages moved from local art to a remote
// image, when keep holds. prepareItemImagesForQueue then routes it into the
// source column and keeps the slot's served path. A slot left on its remote
// stand-in by a refresh that cannot judge the sidecar (artwork locked, or
// local art not searched at this level) keeps its local source too, as a slot
// left on its cached copy does. retention is the item level's
// (localArtworkItem).
func keepUnconfirmedLocalItemArtwork(retention *localArtworkRetention, item, existing *models.MediaItem) {
	if item == nil || existing == nil {
		return
	}
	locked := artworkLocked(existing)
	stored := itemArtworkFields(existing)
	for i, field := range itemArtworkFields(item) {
		storedPath, storedSource := *stored[i].path, *stored[i].source
		if (locked || !retention.searched) && *field.path == storedPath && isLocalArtworkStandIn(storedPath, storedSource) {
			*field.path = storedSource
			continue
		}
		*field.path = retention.providerPath(field.imageType, *field.path, storedSource)
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
	if _, ok := localMetadataImageURL(source); ok {
		// An image URL an NFO names is gone once the refresh read the NFO and
		// it no longer offers the URL; an NFO it could not read confirms
		// nothing.
		return !r.metadataUnreadable
	}
	path, ok := localImageSourceFilePath(source)
	if !ok || !filepath.IsAbs(path) {
		return true
	}
	if !r.folderAnswers(filepath.Dir(path)) {
		return false
	}
	return sidecarFileGone(path)
}

// backdropSidecarGone is sidecarGone for a backdrop. Only backdrops come from
// an extrafanart folder beside the item, so only a backdrop there is judged by
// extraFanartSidecarGone; any other slot keeps the plain rule even when its
// folder, a library mount point for instance, happens to be named extrafanart.
func (r *localArtworkRetention) backdropSidecarGone(source string) bool {
	path, ok := localImageSourceFilePath(source)
	if ok && filepath.IsAbs(path) && strings.EqualFold(filepath.Base(filepath.Dir(path)), ExtraFanartFolder) {
		return r.extraFanartSidecarGone(path)
	}
	return r.sidecarGone(source)
}

// extraFanartSidecarGone is sidecarGone for an image in an extrafanart folder,
// which can supply the primary backdrop as well as extra ones. It is judged
// from the folder holding the extrafanart folder: once that answers, a missing
// or emptied extrafanart folder, or a missing file in it, means the image is
// gone, which the plain rule reads as an unmounted share and keeps forever. An
// extrafanart folder that cannot be listed keeps its images.
func (r *localArtworkRetention) extraFanartSidecarGone(path string) bool {
	dir := filepath.Dir(path)
	if !r.folderAnswers(filepath.Dir(dir)) {
		return false
	}
	// A missing extrafanart folder means a missing file; one that can't be
	// listed proves nothing, and neither does a symlinked one whose target
	// can't be reached, which is a share that is down rather than removed.
	if folder := r.probe(dir); !folder.Reachable {
		if folder.ErrorCode != rootcheck.ErrCodeNotFound {
			return false
		}
		if link, err := os.Lstat(dir); err == nil && link.Mode()&fs.ModeSymlink != 0 {
			return false
		}
	}
	return sidecarFileGone(path)
}

// sidecarFileGone reports whether a sidecar in a folder that answers is gone:
// missing, or still on disk but passed over by discovery. Any other error
// proves nothing.
func sidecarFileGone(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || errors.Is(err, fs.ErrNotExist)
}

// folderAnswers reports whether dir answered its probe and listed entries.
func (r *localArtworkRetention) folderAnswers(dir string) bool {
	folder := r.probe(dir)
	return folder.Reachable && !folder.Empty
}

// probe probes dir once per refresh through rootcheck, with its timeout.
func (r *localArtworkRetention) probe(dir string) rootcheck.Result {
	folder, ok := r.probes[dir]
	if !ok {
		folder = rootcheck.ProbeWithTimeout(r.ctx, dir, rootcheck.DefaultProbeTimeout)
		r.probes[dir] = folder
	}
	return folder
}
