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
	// probes memoizes folder probes, so the episodes of one season folder
	// cost one probe. Retentions made with atLevel share it.
	probes map[string]rootcheck.Result
}

func newLocalArtworkRetention(ctx context.Context, contentID string, level localArtworkLevel) *localArtworkRetention {
	return &localArtworkRetention{
		ctx: ctx, contentID: contentID, searched: localArtworkSearchedLevels(ctx)&level != 0,
		probes: make(map[string]rootcheck.Result),
	}
}

// atLevel returns a retention for another level of the same content that
// shares r's folder probes, so a season's poster and its episodes' stills,
// which usually sit in the same folder, cost one probe.
func (r *localArtworkRetention) atLevel(level localArtworkLevel) *localArtworkRetention {
	other := *r
	other.searched = localArtworkSearchedLevels(r.ctx)&level != 0
	return &other
}

// keep reports whether a slot serving a cached copy of a local sidecar should
// ignore the remote incoming path this refresh chose.
func (r *localArtworkRetention) keep(incoming, existingPath, existingSource string) bool {
	if !r.searched ||
		!artworkkey.IsLocal(existingPath) ||
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
	retention := newLocalArtworkRetention(ctx, existing.ContentID, localArtworkItem)
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
