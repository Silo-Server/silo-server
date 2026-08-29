package jellycompat

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// presignCompatListItems presigns every list item's poster/backdrop/logo/still
// image using exactly four batched PresignImageURLsWithExpiry calls (one per
// image type) for the whole page, independent of item count. Each call dedupes
// its paths and singleflights the underlying plugin resolution, so a 40-item
// rail costs four resolver invocations instead of 4×40. This is the single
// shared implementation for both directContentService and ItemsHandler; do not
// reintroduce a per-item presign body.
func presignCompatListItems(ctx context.Context, detailSvc *catalog.DetailService, items []upstreamListItem) {
	if len(items) == 0 {
		return
	}
	for i := range items {
		ensureListItemImagePaths(&items[i])
	}
	if detailSvc == nil {
		return
	}

	presignCompatTargetSlot(ctx, detailSvc, items, "poster", func(item *upstreamListItem) *string { return &item.PosterURL })
	presignCompatTargetSlot(ctx, detailSvc, items, "backdrop", func(item *upstreamListItem) *string { return &item.BackdropURL })
	presignCompatTargetSlot(ctx, detailSvc, items, "logo", func(item *upstreamListItem) *string { return &item.LogoURL })
	presignCompatTargetSlot(ctx, detailSvc, items, "still", func(item *upstreamListItem) *string { return &item.StillURL })
}

func presignCompatTargetSlot(
	ctx context.Context,
	detailSvc *catalog.DetailService,
	items []upstreamListItem,
	slot string,
	field func(*upstreamListItem) *string,
) {
	targets := make([]artworkurl.Target, 0, len(items))
	byIndex := make([]artworkurl.Target, len(items))
	for i := range items {
		reference := *field(&items[i])
		if reference == "" {
			continue
		}
		target := compatListArtworkTarget(items[i], slot).WithReference(reference)
		targets = append(targets, target)
		byIndex[i] = target
	}
	resolved := detailSvc.PresignArtworkTargetsWithExpiry(ctx, targets, catalog.ArtworkVariantForSize(slot, compatCardImageSize))
	for i := range items {
		target := byIndex[i]
		if target.Surface == "" {
			*field(&items[i]) = ""
			continue
		}
		*field(&items[i]) = resolved[target.CacheKey()].URL
	}
}

func compatListArtworkTarget(item upstreamListItem, slot string) artworkurl.Target {
	target := artworkurl.Target{Slot: slot, Keys: []string{item.ContentID}}
	switch {
	case item.Type == compatItemEpisode && (slot == compatArtworkPoster || slot == compatArtworkStill):
		target.Surface = artworkurl.SurfaceEpisodeStills
		target.Slot = compatArtworkStill
	case item.Type == compatItemEpisode && item.SeriesID != "":
		target.Surface = itemSurfaceForSlot(slot)
		target.Keys[0] = item.SeriesID
	case item.Type == compatItemSeason && slot == compatArtworkPoster:
		target.Surface = artworkurl.SurfaceSeasonPosters
	default:
		target.Surface = itemSurfaceForSlot(slot)
	}
	return target
}

func itemSurfaceForSlot(slot string) string {
	switch slot {
	case compatArtworkBackdrop:
		return artworkurl.SurfaceItemBackdrops
	case compatArtworkLogo:
		return artworkurl.SurfaceItemLogos
	default:
		return artworkurl.SurfaceItemPosters
	}
}

// ensureListItemImagePaths mirrors each presign-source URL into its retained
// *Path field so image tags survive after the URL has been rewritten to a
// resolved value.
func ensureListItemImagePaths(item *upstreamListItem) {
	if item.PosterPath == "" {
		item.PosterPath = item.PosterURL
	}
	if item.BackdropPath == "" {
		item.BackdropPath = item.BackdropURL
	}
	if item.LogoPath == "" {
		item.LogoPath = item.LogoURL
	}
	if item.StillPath == "" {
		item.StillPath = item.StillURL
	}
}

// collectImagePaths returns the de-duplicated, non-empty image paths picked from
// items in first-seen order, ready for a single batched presign call. It is
// generic so list items, seasons, and episodes share one collection routine.
func collectImagePaths[T any](items []T, pick func(T) string) []string {
	paths := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		path := pick(item)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

// resolvedListImageURL looks up a presigned URL by its original input path,
// returning "" for empty or unresolved paths (matching the singular presign
// behavior for those cases).
func resolvedListImageURL(resolved map[string]catalog.ResolvedImageURL, path string) string {
	if path == "" {
		return ""
	}
	if value, ok := resolved[path]; ok {
		return value.URL
	}
	return ""
}
