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
// find again follows them while its sidecar is not confirmed gone, so a folder
// that is briefly unreadable, such as an extrafanart folder on a flaky share,
// keeps its backdrops. A row keeps its cached copy while it shows the same
// source.
func planExtraBackdrops(retention *localArtworkRetention, item *models.MediaItem, existing []models.ItemBackdrop, images []RemoteImage) []models.ItemBackdrop {
	if artworkLocked(item) && len(existing) > 0 {
		return existing
	}
	var sources []string
	seen := map[string]bool{strings.TrimSpace(item.BackdropSourcePath): true}
	for _, img := range images {
		url := strings.TrimSpace(img.URL)
		if img.Type != ImageBackdrop || !isLocalImageSourcePath(url) || seen[url] {
			continue
		}
		seen[url] = true
		sources = append(sources, url)
	}
	cached := make(map[string]models.ItemBackdrop, len(existing))
	for _, b := range existing {
		if isCachedImagePath(b.Path) {
			cached[b.SourcePath] = b
		}
	}
	planned := make([]models.ItemBackdrop, 0, len(sources)+len(existing))
	for _, source := range sources {
		b := models.ItemBackdrop{SourcePath: source}
		if prev, ok := cached[source]; ok {
			b.Path, b.Thumbhash = prev.Path, prev.Thumbhash
		}
		planned = append(planned, b)
	}
	for _, b := range existing {
		if seen[b.SourcePath] {
			continue
		}
		if retention.searched && isLocalImageSourcePath(b.SourcePath) && retention.extraBackdropGone(b.SourcePath) {
			continue
		}
		seen[b.SourcePath] = true
		planned = append(planned, models.ItemBackdrop{Path: b.Path, SourcePath: b.SourcePath, Thumbhash: b.Thumbhash})
	}
	if len(planned) > MaxItemBackdrops-1 {
		planned = planned[:MaxItemBackdrops-1]
	}
	return planned
}

// extraBackdropGone is sidecarGone for an extra backdrop. A backdrop from an
// extrafanart folder is also gone when that folder no longer exists while the
// folder holding it answers: a removed extrafanart folder reads as unreachable
// to sidecarGone, which would keep its backdrops forever.
func (r *localArtworkRetention) extraBackdropGone(source string) bool {
	if path, ok := localImageSourceFilePath(source); ok && filepath.IsAbs(path) {
		dir := filepath.Dir(path)
		if strings.EqualFold(filepath.Base(dir), ExtraFanartFolder) {
			if !r.folderAnswers(filepath.Dir(dir)) {
				return false
			}
			if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
				return true
			}
		}
	}
	return r.sidecarGone(source)
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
func (s *MetadataService) syncItemExtraBackdrops(ctx context.Context, retention *localArtworkRetention, item *models.MediaItem, providerIDs map[string]string, images []RemoteImage) {
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
	if planned := planExtraBackdrops(retention, item, rows, images); !sameItemBackdrops(rows, planned) {
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
