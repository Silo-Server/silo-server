package jellycompat

import (
	"context"

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

	posterURLs := detailSvc.PresignImageURLsWithExpiry(ctx, collectImagePaths(items, func(item upstreamListItem) string { return item.PosterURL }), "poster", compatCardImageSize)
	backdropURLs := detailSvc.PresignImageURLsWithExpiry(ctx, collectImagePaths(items, func(item upstreamListItem) string { return item.BackdropURL }), "backdrop", compatCardImageSize)
	logoURLs := detailSvc.PresignImageURLsWithExpiry(ctx, collectImagePaths(items, func(item upstreamListItem) string { return item.LogoURL }), "logo", compatCardImageSize)
	stillURLs := detailSvc.PresignImageURLsWithExpiry(ctx, collectImagePaths(items, func(item upstreamListItem) string { return item.StillURL }), "still", compatCardImageSize)

	for i := range items {
		items[i].PosterURL = resolvedListImageURL(posterURLs, items[i].PosterURL)
		items[i].BackdropURL = resolvedListImageURL(backdropURLs, items[i].BackdropURL)
		items[i].LogoURL = resolvedListImageURL(logoURLs, items[i].LogoURL)
		items[i].StillURL = resolvedListImageURL(stillURLs, items[i].StillURL)
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
