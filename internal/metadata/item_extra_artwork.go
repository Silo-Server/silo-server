package metadata

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
)

// Banner, thumb, clear art and disc art, and the backdrops after the primary,
// come from local sidecars today: no metadata plugin offers them. These helpers
// carry them through a refresh, which otherwise rebuilds an item from provider
// results that never mention them.

// MaxItemBackdrops caps the backdrops an item keeps, the primary included.
// Jellyfin reads at most 20 numbered local backdrops per family.
const MaxItemBackdrops = 20

type metadataItemBackdropRepo interface {
	ListByContentIDs(ctx context.Context, contentIDs []string) (map[string][]models.ItemBackdrop, error)
	Replace(ctx context.Context, contentID string, backdrops []models.ItemBackdrop) ([]models.ItemBackdrop, error)
}

// itemOnlyArtworkFields are the item artwork slots localizations do not carry.
func itemOnlyArtworkFields(item *models.MediaItem) []itemArtworkField {
	if item == nil {
		return nil
	}
	return []itemArtworkField{
		{imageType: ImageBanner, path: &item.BannerPath, source: &item.BannerSourcePath, thumbhash: &item.BannerThumbhash},
		{imageType: ImageThumb, path: &item.ThumbPath, source: &item.ThumbSourcePath, thumbhash: &item.ThumbThumbhash},
		{imageType: ImageClearArt, path: &item.ClearArtPath, source: &item.ClearArtSourcePath},
		{imageType: ImageDisc, path: &item.DiscPath, source: &item.DiscSourcePath},
	}
}

// carryItemOnlyArtwork starts the rebuilt item from the stored banner, thumb,
// clear art and disc slots, as the merge does for the poster, backdrop and
// logo. Without it, every write would clear them, including localization and
// enrichment writes that never look at artwork.
func carryItemOnlyArtwork(item, existing *models.MediaItem) {
	stored := itemOnlyArtworkFields(existing)
	if stored == nil {
		return
	}
	for i, field := range itemOnlyArtworkFields(item) {
		*field.path = *stored[i].path
		*field.source = *stored[i].source
		if field.thumbhash != nil {
			*field.thumbhash = *stored[i].thumbhash
		}
	}
}

// settleItemOnlyArtwork runs after applyBestImages. A banner, thumb, clear art
// or disc slot that holds a local sidecar and got no candidate this refresh is
// cleared when the sidecar is gone: when local discovery ran and did not
// return it, or, when it did not run (Identify, local artwork off), when the
// file is confirmed missing. An unreachable library keeps it. Then the
// thumbhash follows the path, as for the poster and backdrop.
func settleItemOnlyArtwork(ctx context.Context, item, existing *models.MediaItem, images []RemoteImage) {
	if item == nil {
		return
	}
	discovered := hasLocalImage(images)
	locked := artworkLocked(item)
	var retention *localArtworkRetention
	for _, field := range itemOnlyArtworkFields(item) {
		if locked || hasImageOfType(images, field.imageType) || !isLocalImageSourcePath(*field.source) {
			continue
		}
		if !discovered {
			if retention == nil {
				retention = newLocalArtworkRetention(ctx, item.ContentID)
			}
			if !retention.sidecarMissing(*field.source) {
				continue
			}
		}
		*field.path, *field.source = "", ""
	}
	for _, field := range itemOnlyArtworkFields(item) {
		if field.thumbhash != nil {
			*field.thumbhash = mergedImageThumbhash(
				existingImagePath(existing, field.imageType),
				existingImageThumbhash(existing, field.imageType),
				*field.path,
				"",
			)
		}
	}
}

func hasLocalImage(images []RemoteImage) bool {
	for _, img := range images {
		if isLocalImageSourcePath(img.URL) {
			return true
		}
	}
	return false
}

func hasImageOfType(images []RemoteImage, imageType ImageType) bool {
	for _, img := range images {
		if img.Type == imageType && strings.TrimSpace(img.URL) != "" {
			return true
		}
	}
	return false
}

// sidecarMissing reports whether a local source's file is confirmed gone: its
// folder answers and the file is not there. Unlike sidecarGone, a file still on
// disk counts as present, because a refresh that did not run local discovery
// cannot say whether it still qualifies.
func (r *localArtworkRetention) sidecarMissing(source string) bool {
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
	return errors.Is(err, fs.ErrNotExist)
}

// planExtraBackdrops decides the item's backdrops after the primary. Local
// discovery lists backdrops in display order and applyBestImages made the
// first one the primary, so the rest follow it. A row keeps its cached copy
// while it shows the same source.
func planExtraBackdrops(ctx context.Context, item *models.MediaItem, existing []models.ItemBackdrop, images []RemoteImage) []models.ItemBackdrop {
	if artworkLocked(item) && len(existing) > 0 {
		return existing
	}
	var sources []string
	seen := make(map[string]bool)
	for _, img := range images {
		url := strings.TrimSpace(img.URL)
		if img.Type != ImageBackdrop || !isLocalImageSourcePath(url) || seen[url] {
			continue
		}
		seen[url] = true
		sources = append(sources, url)
	}
	switch {
	case len(sources) > 0:
		sources = sources[1:]
	case hasLocalImage(images):
		// Discovery ran and found no backdrop.
	default:
		// No local discovery this refresh: keep each row unless its sidecar is
		// confirmed missing.
		retention := newLocalArtworkRetention(ctx, item.ContentID)
		kept := make([]models.ItemBackdrop, 0, len(existing))
		for _, b := range existing {
			if isLocalImageSourcePath(b.SourcePath) && retention.sidecarMissing(b.SourcePath) {
				continue
			}
			kept = append(kept, models.ItemBackdrop{Path: b.Path, SourcePath: b.SourcePath, Thumbhash: b.Thumbhash})
		}
		return kept
	}
	if len(sources) > MaxItemBackdrops-1 {
		sources = sources[:MaxItemBackdrops-1]
	}
	cached := make(map[string]models.ItemBackdrop, len(existing))
	for _, b := range existing {
		if isCachedImagePath(b.Path) {
			cached[b.SourcePath] = b
		}
	}
	planned := make([]models.ItemBackdrop, 0, len(sources))
	for _, source := range sources {
		b := models.ItemBackdrop{SourcePath: source}
		if prev, ok := cached[source]; ok {
			b.Path, b.Thumbhash = prev.Path, prev.Thumbhash
		}
		planned = append(planned, b)
	}
	return planned
}

func sameItemBackdrops(stored, planned []models.ItemBackdrop) bool {
	if len(stored) != len(planned) {
		return false
	}
	for i := range stored {
		if stored[i].Ordinal != i+1 ||
			stored[i].Path != planned[i].Path ||
			stored[i].SourcePath != planned[i].SourcePath ||
			stored[i].Thumbhash != planned[i].Thumbhash {
			return false
		}
	}
	return true
}

// syncItemExtraBackdrops stores the item's backdrops after the primary and
// queues a cache job for each, as enqueueItemImages does for the slots on the
// item row. It runs after the item is persisted, because the rows reference it.
func (s *MetadataService) syncItemExtraBackdrops(ctx context.Context, item *models.MediaItem, providerIDs map[string]string, images []RemoteImage) {
	if s == nil || s.itemBackdropRepo == nil || item == nil || item.ContentID == "" {
		return
	}
	stored, err := s.itemBackdropRepo.ListByContentIDs(ctx, []string{item.ContentID})
	if err != nil {
		slog.WarnContext(ctx, "metadata: loading item backdrops failed", "component", "metadata",
			"content_id", item.ContentID, "error", err)
		return
	}
	rows := stored[item.ContentID]
	if planned := planExtraBackdrops(ctx, item, rows, images); !sameItemBackdrops(rows, planned) {
		rows, err = s.itemBackdropRepo.Replace(ctx, item.ContentID, planned)
		if err != nil {
			slog.WarnContext(ctx, "metadata: storing item backdrops failed", "component", "metadata",
				"content_id", item.ContentID, "error", err)
			return
		}
	}
	inputs := make([]EnqueueImageCacheJobInput, 0, len(rows))
	for _, b := range rows {
		sourcePath := strings.TrimSpace(b.SourcePath)
		if !isCacheableImageSourcePath(sourcePath) {
			continue
		}
		providerID, providerContentID := itemImageCacheAttribution(item, providerIDs, images, sourcePath)
		inputs = append(inputs, EnqueueImageCacheJobInput{
			TargetType:        ImageCacheTargetItemBackdrop,
			TargetContentID:   strconv.FormatInt(b.ID, 10),
			SeriesID:          item.ContentID,
			SourcePath:        sourcePath,
			ProviderID:        providerID,
			ProviderContentID: providerContentID,
			ContentType:       imageCacheContentType(item.Type),
			ImageType:         ImageCacheImageBackdrop,
			RequeueSucceeded:  !isCachedImagePath(b.Path),
		})
	}
	s.enqueueImageCacheJobs(ctx, "item backdrop", item.ContentID, inputs)
}
