package catalog

import (
	"context"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
)

type itemBackdropLister interface {
	ListByContentIDs(ctx context.Context, contentIDs []string) (map[string][]models.ItemBackdrop, error)
}

// copyExtraArtwork copies a movie's or series' banner, thumb, clear art and
// disc art paths onto its detail, with a thumbhash only beside its path.
// Jellyfin batch detail reads only the paths; attachExtraBackdrops resolves
// the URLs for single-item detail.
func copyExtraArtwork(detail *ItemDetail, item *models.MediaItem) {
	detail.BannerPath, detail.ThumbPath = item.BannerPath, item.ThumbPath
	detail.ClearArtPath, detail.DiscPath = item.ClearArtPath, item.DiscPath
	if item.BannerPath != "" {
		detail.BannerThumbhash = item.BannerThumbhash
	}
	if item.ThumbPath != "" {
		detail.ThumbThumbhash = item.ThumbThumbhash
	}
}

// presignExtraArtwork resolves the banner, thumb, clear art and disc art URLs
// of a detail filled by copyExtraArtwork, keeping a thumbhash only beside its
// URL. Only single-item detail returns these URLs, so attachExtraBackdrops
// calls this only when it presigns.
func (s *DetailService) presignExtraArtwork(ctx context.Context, detail *ItemDetail, size string) {
	detail.BannerURL = s.PresignImageURL(ctx, detail.BannerPath, artworkkey.ImageBanner, size)
	detail.ThumbURL = s.PresignImageURL(ctx, detail.ThumbPath, artworkkey.ImageThumb, size)
	detail.ClearArtURL = s.PresignImageURL(ctx, detail.ClearArtPath, artworkkey.ImageClearArt, size)
	detail.DiscURL = s.PresignImageURL(ctx, detail.DiscPath, artworkkey.ImageDisc, size)
	if detail.BannerURL == "" {
		detail.BannerThumbhash = ""
	}
	if detail.ThumbURL == "" {
		detail.ThumbThumbhash = ""
	}
}

// attachExtraBackdrops gives each movie or series detail the cached paths of
// its backdrops after the primary, with one query for all of them. With
// presign it also resolves the banner, thumb, clear art and disc art URLs
// (presignExtraArtwork) and lists every backdrop URL in display order, the
// primary (BackdropURL) first; batch reads skip that, since only single-item
// detail returns the URLs. A backdrop still waiting for the image cache has no
// path yet and is left out. Backdrops resolve at the backdrop slot's size and
// the other artwork at the response-wide size.
func (s *DetailService) attachExtraBackdrops(ctx context.Context, details []*ItemDetail, art imagesize.Request, presign bool) {
	ids := make([]string, 0, len(details))
	for _, detail := range details {
		if hasItemBackdrops(detail) {
			ids = append(ids, detail.ContentID)
		}
	}
	if len(ids) == 0 {
		return
	}
	extras, err := s.ExtraBackdropPaths(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "catalog: loading item backdrops failed; listing the primary only", "component", "catalog", "error", err)
	}
	for _, detail := range details {
		if !hasItemBackdrops(detail) {
			continue
		}
		detail.ExtraBackdropPaths = extras[detail.ContentID]
		if !presign {
			continue
		}
		s.presignExtraArtwork(ctx, detail, string(art.Size))
		var urls []string
		if detail.BackdropURL != "" {
			urls = append(urls, detail.BackdropURL)
		}
		for _, path := range detail.ExtraBackdropPaths {
			if url := s.PresignImageURL(ctx, path, artworkkey.ImageBackdrop, string(art.For(imagesize.SlotBackdrop))); url != "" {
				urls = append(urls, url)
			}
		}
		detail.BackdropURLs = urls
	}
}

// hasItemBackdrops reports whether detail is a movie or series, the items
// whose own row carries backdrops.
func hasItemBackdrops(detail *ItemDetail) bool {
	return detail != nil && (detail.Type == playableTypeMovie || detail.Type == playableTypeSeries)
}

// ExtraBackdropPaths returns the cached artwork paths of each item's backdrops
// after the primary, in display order. A backdrop still waiting for the image
// cache has no path yet and is left out.
func (s *DetailService) ExtraBackdropPaths(ctx context.Context, contentIDs []string) (map[string][]string, error) {
	if s == nil || s.backdropRepo == nil || len(contentIDs) == 0 {
		return nil, nil
	}
	rows, err := s.backdropRepo.ListByContentIDs(ctx, contentIDs)
	if err != nil {
		return nil, err
	}
	paths := make(map[string][]string, len(rows))
	for contentID, backdrops := range rows {
		for _, b := range backdrops {
			if b.Path != "" {
				paths[contentID] = append(paths[contentID], b.Path)
			}
		}
	}
	return paths, nil
}
