package metadata

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
)

// SetLocalArtworkCopier wires the copier that copies local sidecar artwork a
// refresh discovers into the artwork store from this process. Without it,
// local artwork is only queued, for whichever server drains the queue first.
// Call it during setup, before refreshes run.
func (s *MetadataService) SetLocalArtworkCopier(copier LocalArtworkCopier) {
	s.localArtwork = copier
}

// copyLocalArtwork hands the local sidecar inputs this process can see to the
// copier and returns the inputs still to queue the usual way: the remote ones
// and the unseen local ones, or all of them when no copier is wired or it
// failed. A refresh finds only files it can read, so a discovered sidecar is
// seen; a local source kept by a refresh on a server without the mount is
// not, and the usual queueing leaves its hold or deferral as it is.
func (s *MetadataService) copyLocalArtwork(ctx context.Context, targetKind, targetID string, inputs []EnqueueImageCacheJobInput) []EnqueueImageCacheJobInput {
	if s.localArtwork == nil {
		return inputs
	}
	local, rest := splitLocalImageCacheInputs(inputs)
	var seen []EnqueueImageCacheJobInput
	visible := visibleLocalSidecars(ctx, local, rootcheck.ProbeManyWithTimeout)
	for i, in := range local {
		if visible[i] {
			seen = append(seen, in)
		} else {
			rest = append(rest, in)
		}
	}
	if len(seen) == 0 {
		return inputs
	}
	if err := s.localArtwork.CopyLocalArtwork(ctx, seen); err != nil {
		slog.WarnContext(ctx, "metadata: failed to start copying local artwork, leaving it to the queue", "component", "metadata",
			"target_kind", targetKind, "target_id", targetID, "count", len(seen), "error", err)
		return inputs
	}
	return rest
}

// folderProber probes folders with a timeout; rootcheck.ProbeManyWithTimeout
// in production.
type folderProber func(ctx context.Context, paths []string, timeout time.Duration) []rootcheck.Result

// visibleLocalSidecars reports, for each local input, whether the file it
// names exists on this server. Each folder is probed first, once, through
// probe with rootcheck's coalescing and one deadline for the whole batch, so
// shares that do not answer cost the refresh at most that timeout and count
// as unseen; the file is looked up only in a folder that answered, including
// one that denies listing (the NFO finder reads known names from such a
// folder).
func visibleLocalSidecars(ctx context.Context, local []EnqueueImageCacheJobInput, probe folderProber) []bool {
	paths := make([]string, len(local))
	folderOf := make([]int, len(local)) // index into folders, -1 when not local
	folderIndex := make(map[string]int)
	var folders []string
	for i, in := range local {
		folderOf[i] = -1
		path, ok := localImageSourceFilePath(in.SourcePath)
		if !ok {
			continue
		}
		dir := filepath.Dir(path)
		idx, ok := folderIndex[dir]
		if !ok {
			idx = len(folders)
			folderIndex[dir] = idx
			folders = append(folders, dir)
		}
		paths[i], folderOf[i] = path, idx
	}
	visible := make([]bool, len(local))
	if len(folders) == 0 {
		return visible
	}
	probeCtx, cancel := context.WithTimeout(ctx, rootcheck.DefaultProbeTimeout)
	defer cancel()
	probes := probe(probeCtx, folders, rootcheck.DefaultProbeTimeout)
	for i, path := range paths {
		if folderOf[i] < 0 {
			continue
		}
		if folder := probes[folderOf[i]]; !folder.Reachable && folder.ErrorCode != rootcheck.ErrCodePermissionDenied {
			continue
		}
		_, err := fspath.Lstat(path)
		visible[i] = err == nil
	}
	return visible
}

// applyLocalArtworkFallback fills each item slot waiting on the copy of its
// sidecar file, with nothing cached to show, from the best remote candidate.
// An image URL an NFO names gets no stand-in: its slot stays empty until the
// image cache has downloaded it. A slot with a
// cached copy keeps serving it (preserveCachedArtwork); an empty one would
// leave a new item without a poster until the copy lands. The copy replaces
// the stand-in through the source-matched update, because the slot's source
// stays local. With no remote candidate for the slot (always so for a local
// re-apply, which asks no remote provider), the remote image the stored slot
// was serving stays as the stand-in, as a cached copy would, so a sidecar
// whose copy fails never leaves the slot blank. Locked artwork gets no
// stand-in the admin did not choose.
func applyLocalArtworkFallback(item, existing *models.MediaItem, images []RemoteImage, preferredLang, fallbackLang string) {
	if artworkLocked(item) {
		return
	}
	var remote []itemArtworkField
	for i, field := range itemArtworkFields(item) {
		if strings.TrimSpace(*field.path) != "" || !isLocalSidecarSource(*field.source) {
			continue
		}
		if remote == nil {
			best := &models.MediaItem{}
			applyBestImages(best, withoutLocalImages(images), MergeReplaceUnlocked, preferredLang, fallbackLang)
			remote = itemArtworkFields(best)
		}
		fallback := *remote[i].path
		if strings.TrimSpace(fallback) == "" {
			// Clear art stored as the logo is not a wordmark, and a manual
			// refresh deliberately drops it (applyBestImages): it never
			// stands in.
			served := existingImagePath(existing, field.imageType)
			if isRemoteImageSourcePath(served) && (field.imageType != ImageLogo || !isClearArtPath(served)) {
				fallback = served
				if field.thumbhash != nil && *field.thumbhash == "" {
					*field.thumbhash = existingImageThumbhash(existing, field.imageType)
				}
			}
		}
		*field.path = localArtworkFallbackPath(*field.path, *field.source, fallback)
	}
}

// saveProvisionalLocalArtwork stores the local sidecar poster, backdrop and
// logo a run found for a pending, unmatched or ambiguous item no provider gave
// metadata; a matched item reaches this point only when its providers failed,
// and is left alone. Remote images from the run are ignored, because an
// unmatched item must not take art from an unconfirmed ID; only a remote image
// the slot already serves can stand in while the copy is pending. Otherwise
// slots follow applyBestImages and the queue preparation, so the artwork lock
// behaves as for a matched item. Only the artwork columns of slots taking a
// new local source are written, and only while the row is still provisional
// with the sources and images lock read here. Such a slot drops a cached copy
// of its previous source, so a replaced sidecar shows no image until its new
// copy lands, and a slot on its sidecar with a cached copy is settled: it is
// left alone, any other slot on its sidecar is queued again. A settled item
// costs one item read per run. A failure is logged; the outcome stands.
func (s *MetadataService) saveProvisionalLocalArtwork(ctx context.Context, contentID, language string, images []RemoteImage) {
	images = slices.DeleteFunc(slices.Clone(images), func(img RemoteImage) bool { return !isLocalImageSourcePath(img.URL) })
	if contentID == "" || len(images) == 0 {
		return
	}
	existing, err := s.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		if !errors.Is(err, catalog.ErrItemNotFound) {
			slog.WarnContext(ctx, "metadata: failed to load item for local artwork", "component", "metadata",
				"content_id", contentID, "error", err)
		}
		return
	}
	if !isProvisionalOwnershipStatus(existing.Status) {
		return
	}
	item := *existing
	fallback := s.FallbackLanguage()
	applyBestImages(&item, images, MergeFillEmpty, language, fallback)
	// Local artwork is only found with image caching on, so stored copies keep
	// serving until the new copy lands.
	prepareItemImagesForQueue(&item, existing, true, false)
	applyLocalArtworkFallback(&item, existing, images, language, fallback)

	queued := &models.MediaItem{ContentID: existing.ContentID, Type: existing.Type}
	var upd catalog.MetadataUpdate
	write := false
	stored, queue := itemArtworkFields(existing), itemArtworkFields(queued)
	for i, field := range itemArtworkFields(&item) {
		// The update has columns for the poster, backdrop and logo only.
		if isItemOnlyArtworkType(field.imageType) || !isLocalImageSourcePath(*field.source) {
			continue
		}
		if *field.source != *stored[i].source {
			dropPreviousCopy(field)
			setArtworkUpdate(&upd, field)
			write = true
		} else if isCachedImagePath(*stored[i].path) {
			continue
		}
		*queue[i].path, *queue[i].source = *field.path, *field.source
	}
	if write {
		wrote, err := s.itemRepo.UpdateProvisionalArtwork(ctx, existing, &upd)
		if err != nil {
			slog.WarnContext(ctx, "metadata: failed to save local artwork for unmatched item", "component", "metadata",
				"content_id", contentID, "error", err)
			return
		}
		if !wrote {
			// Another writer changed the row; the next run looks again.
			return
		}
	}
	_ = s.enqueueItemImages(ctx, queued, nil, images) // logged by enqueueImageCacheJobs
}

// dropPreviousCopy empties a slot taking a new source that still serves a
// cached copy, which can only be of its previous source: kept, it would make a
// lost copy job of the new source look settled on every later run.
func dropPreviousCopy(field itemArtworkField) {
	if !isCachedImagePath(*field.path) {
		return
	}
	*field.path = ""
	if field.thumbhash != nil {
		*field.thumbhash = ""
	}
}

// setArtworkUpdate sets field's path, source and thumbhash on upd.
func setArtworkUpdate(upd *catalog.MetadataUpdate, field itemArtworkField) {
	switch field.imageType {
	case ImagePoster:
		upd.PosterPath, upd.PosterSourcePath, upd.PosterThumbhash = field.path, field.source, field.thumbhash
	case ImageBackdrop:
		upd.BackdropPath, upd.BackdropSourcePath, upd.BackdropThumbhash = field.path, field.source, field.thumbhash
	case ImageLogo:
		upd.LogoPath, upd.LogoSourcePath = field.path, field.source
	}
}

// isLocalArtworkStandIn reports whether a stored slot serves a remote stand-in
// while the copy of its sidecar file is pending.
func isLocalArtworkStandIn(path, source string) bool {
	return isRemoteImageSourcePath(path) && isLocalSidecarSource(source)
}

// localArtworkFallbackPath returns fallback in place of an empty path whose
// source is a sidecar file, and path otherwise.
func localArtworkFallbackPath(path, source, fallback string) string {
	if strings.TrimSpace(path) == "" && isLocalSidecarSource(source) && isRemoteImageSourcePath(fallback) {
		return fallback
	}
	return path
}

// recordEpisodeStillFallback remembers the first remote still that a local
// still displaces or outranks while episode results merge.
func recordEpisodeStillFallback(target *EpisodeResult, incoming EpisodeResult) {
	target.stillFallback = firstNonEmpty(target.stillFallback, displacedRemoteImage(target.StillPath, incoming.StillPath))
}

// displacedRemoteImage returns the remote one of an accumulated and an
// incoming image when the other is local.
func displacedRemoteImage(accumulated, incoming string) string {
	switch {
	case isLocalImageSourcePath(accumulated) && isRemoteImageSourcePath(incoming):
		return incoming
	case isLocalImageSourcePath(incoming) && isRemoteImageSourcePath(accumulated):
		return accumulated
	}
	return ""
}
