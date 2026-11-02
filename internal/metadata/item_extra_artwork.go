package metadata

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

// Banner, thumb, clear art and disc art, and the backdrops after the primary,
// come from local sidecars today: no metadata plugin offers them. These helpers
// carry them through a refresh, which otherwise rebuilds an item from provider
// results that never mention them.

// MaxItemBackdrops caps the backdrops an item keeps, the primary included.
// Jellyfin reads at most 20 numbered local backdrops per family.
const MaxItemBackdrops = 20

// ExtraFanartFolder is the folder beside the media that holds any number of
// extra backdrops (Kodi). Jellyfin's backdrops folder holds theme videos.
const ExtraFanartFolder = "extrafanart"

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

// isItemOnlyArtworkType reports whether imageType is one of the item slots
// that itemOnlyArtworkFields lists.
func isItemOnlyArtworkType(imageType ImageType) bool {
	switch imageType {
	case ImageBanner, ImageThumb, ImageClearArt, ImageDisc:
		return true
	default:
		return false
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

// dropGoneItemOnlyArtwork runs after prepareItemImagesForQueue, which keeps a
// slot's cached copy whenever the refresh offered nothing for it. When the
// refresh searched the item level for sidecars (withLocalArtworkSearched), a
// banner, thumb, clear art or disc slot that holds a local sidecar the search
// did not return is cleared once the sidecar's folder answers (sidecarGone),
// and kept while it does not. A refresh that did not search leaves the slots
// as stored.
func dropGoneItemOnlyArtwork(retention *localArtworkRetention, item *models.MediaItem, images []RemoteImage) {
	if item == nil || !retention.searched || artworkLocked(item) {
		return
	}
	for _, field := range itemOnlyArtworkFields(item) {
		if hasImageOfType(images, field.imageType) || !isLocalImageSourcePath(*field.source) || !retention.sidecarGone(*field.source) {
			continue
		}
		*field.path, *field.source = "", ""
		if field.thumbhash != nil {
			*field.thumbhash = ""
		}
	}
}

func hasImageOfType(images []RemoteImage, imageType ImageType) bool {
	for _, img := range images {
		if img.Type == imageType && strings.TrimSpace(img.URL) != "" {
			return true
		}
	}
	return false
}

// planExtraBackdrops decides the item's backdrops after the primary: the local
// backdrops discovery listed, in its display order, other than the one the
// item shows as its primary. applyBestImages makes the first one the primary
// unless locked artwork keeps another. A stored backdrop the refresh did not
// find again stays while its sidecar is not confirmed gone, so a folder that
// is briefly unreadable, such as an extrafanart folder on a flaky share, keeps
// its backdrops; while it does, the stored order holds so the backdrops don't
// reshuffle. A backdrop keeps its cached copy while its file is the same, even
// when the file was replaced, until the image cache stores the new copy.
func planExtraBackdrops(retention *localArtworkRetention, item *models.MediaItem, existing []models.ItemBackdrop, images []RemoteImage) []models.ItemBackdrop {
	if artworkLocked(item) && len(existing) > 0 {
		return existing
	}
	// Sources are matched by file, without the size and mtime fingerprint.
	file := func(source string) string {
		path, _ := localImageSourceFilePath(strings.TrimSpace(source))
		return path
	}
	primary := file(item.BackdropSourcePath)
	discovered := make(map[string]string) // file -> source
	var order []string
	for _, img := range images {
		url := strings.TrimSpace(img.URL)
		if img.Type != ImageBackdrop || !isLocalImageSourcePath(url) {
			continue
		}
		if f := file(url); f != "" && f != primary && discovered[f] == "" {
			discovered[f] = url
			order = append(order, f)
		}
	}
	stored := make(map[string]models.ItemBackdrop, len(existing))
	var storedOrder []string
	keep := make(map[string]bool) // stored files the refresh did not find again but keeps
	for _, b := range existing {
		f := file(b.SourcePath)
		if f == "" || f == primary || stored[f].SourcePath != "" {
			continue
		}
		stored[f] = b
		storedOrder = append(storedOrder, f)
		if discovered[f] == "" && (!retention.searched || !retention.backdropSidecarGone(b.SourcePath)) {
			keep[f] = true
		}
	}
	plan := func(f string) models.ItemBackdrop {
		b := models.ItemBackdrop{SourcePath: discovered[f]}
		prev, ok := stored[f]
		if b.SourcePath == "" {
			b.SourcePath = prev.SourcePath
		}
		if ok && isCachedImagePath(prev.Path) {
			b.Path, b.Thumbhash = prev.Path, prev.Thumbhash
		}
		return b
	}
	var files []string
	if len(keep) == 0 {
		files = order
	} else {
		for _, f := range storedOrder {
			if keep[f] || discovered[f] != "" {
				files = append(files, f)
			}
		}
		for _, f := range order {
			if stored[f].SourcePath == "" {
				files = append(files, f)
			}
		}
	}
	if len(files) > MaxItemBackdrops-1 {
		files = files[:MaxItemBackdrops-1]
	}
	planned := make([]models.ItemBackdrop, 0, len(files))
	for _, f := range files {
		planned = append(planned, plan(f))
	}
	return planned
}

// sameItemBackdrops reports whether the stored rows already hold the planned
// sources in order. The cached path and thumbhash are left out: Replace takes
// them from the stored row with the same source, so they cannot differ.
func sameItemBackdrops(stored, planned []models.ItemBackdrop) bool {
	if len(stored) != len(planned) {
		return false
	}
	for i := range stored {
		if stored[i].Ordinal != i+1 || stored[i].SourcePath != planned[i].SourcePath {
			return false
		}
	}
	return true
}

// syncItemExtraBackdrops stores the item's backdrops after the primary and
// queues a cache job for each, as enqueueItemImages does for the slots on the
// item row. It runs after the item is persisted, because the rows reference it.
// A failure is logged and returned.
func (s *MetadataService) syncItemExtraBackdrops(ctx context.Context, retention *localArtworkRetention, item *models.MediaItem, providerIDs map[string]string, images []RemoteImage) error {
	if s == nil || s.itemBackdropRepo == nil || item == nil || item.ContentID == "" {
		return nil
	}
	stored, err := s.itemBackdropRepo.ListByContentIDs(ctx, []string{item.ContentID})
	if err != nil {
		slog.WarnContext(ctx, "metadata: loading item backdrops failed", "component", "metadata",
			"content_id", item.ContentID, "error", err)
		return err
	}
	rows := stored[item.ContentID]
	// Extra backdrops are local art only. With caching off the item keeps
	// none, unless its artwork is locked.
	var planned []models.ItemBackdrop
	if s.autoCacheImages.Load() || artworkLocked(item) {
		planned = planExtraBackdrops(retention, item, rows, images)
	}
	if !sameItemBackdrops(rows, planned) {
		rows, err = s.itemBackdropRepo.Replace(ctx, item.ContentID, planned)
		if err != nil {
			slog.WarnContext(ctx, "metadata: storing item backdrops failed", "component", "metadata",
				"content_id", item.ContentID, "error", err)
			return err
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
	return s.enqueueImageCacheJobs(ctx, "item backdrop", item.ContentID, inputs)
}
