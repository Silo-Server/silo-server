package catalog

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/models"
)

// GetItemCardsByIDs resolves content ids to the subset of item detail a card
// needs: the localized base row, access-filtered, with poster, backdrop and
// logo URLs signed in one batch. It runs a fixed number of queries and one
// artwork resolution for the whole page, where GetItemDetailsByIDs runs
// per-item work (file versions, extras, folder paths, credits, work summaries
// and three separate image resolves) that a card never shows. Use it for row
// and list surfaces; use GetItemDetailsByIDs for an item page.
//
// Ids the viewer may not see are absent from the result. Series and movie
// cards carry no versions, credits or user state.
func (s *DetailService) GetItemCardsByIDs(ctx context.Context, contentIDs []string, filter AccessFilter) (map[string]*ItemDetail, error) {
	result := make(map[string]*ItemDetail, len(contentIDs))
	if len(contentIDs) == 0 || s == nil || s.itemRepo == nil {
		return result, nil
	}
	items, err := s.itemRepo.GetByIDs(ctx, contentIDs)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return result, nil
	}
	foundIDs := make([]string, 0, len(items))
	for _, item := range items {
		foundIDs = append(foundIDs, item.ContentID)
	}
	accessible, err := s.itemRepo.EnsureAccessibleIDs(ctx, foundIDs, filter)
	if err != nil {
		return nil, err
	}
	presentationOK := func(string) bool { return true }
	if filter.PresentationLibraryID != nil {
		membership, err := s.itemRepo.GetItemsInLibrary(ctx, foundIDs, *filter.PresentationLibraryID)
		if err != nil {
			return nil, err
		}
		presentationOK = func(id string) bool { return membership[id] }
	}
	visible := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if accessible[item.ContentID] && presentationOK(item.ContentID) {
			visible = append(visible, item)
		}
	}
	if len(visible) == 0 {
		return result, nil
	}
	targetByID, locByID, err := s.loadItemLocalizations(ctx, visible, filter)
	if err != nil {
		return nil, err
	}

	// Every image on the page is resolved in one batch. Paths are normalized
	// to the requested variant first so cached keys match the way the
	// per-item path would have asked for them.
	size := string(filter.ImageSize)
	paths := make([]string, 0, len(visible)*3)
	seen := make(map[string]struct{}, len(visible)*3)
	add := func(path string) string {
		if path == "" || path == "-" {
			return ""
		}
		normalized := cachedImageVariantPath(path, imageTypeOrDefault(path, artworkkey.ImagePoster), size)
		if _, ok := seen[normalized]; !ok {
			seen[normalized] = struct{}{}
			paths = append(paths, normalized)
		}
		return normalized
	}
	type cardPaths struct{ poster, backdrop, logo string }
	byID := make(map[string]cardPaths, len(visible))
	for _, item := range visible {
		byID[item.ContentID] = cardPaths{
			poster:   add(item.PosterPath),
			backdrop: addTyped(add, item.BackdropPath, artworkkey.ImageBackdrop, size),
			logo:     addTyped(add, item.LogoPath, artworkkey.ImageLogo, size),
		}
	}
	urls := s.PresignURLsWithExpiry(ctx, paths, sizeToVariant(size))

	for _, item := range visible {
		id := item.ContentID
		localized := s.localizeItemModelWith(item, targetByID[id], locByID[id])
		p := byID[id]
		result[id] = &ItemDetail{
			ContentID:         localized.ContentID,
			PlayContentID:     localized.PlayContentID,
			Type:              localized.Type,
			Title:             localized.Title,
			SortTitle:         localized.SortTitle,
			OriginalTitle:     localized.OriginalTitle,
			Year:              localized.Year,
			Overview:          localized.Overview,
			Tagline:           localized.Tagline,
			Runtime:           localized.Runtime,
			ContentRating:     localized.ContentRating,
			AdvisoryAge:       localized.AdvisoryAge,
			AdvisorySource:    localized.AdvisorySource,
			Genres:            localized.Genres,
			RatingIMDB:        localized.RatingIMDB,
			RatingTMDB:        localized.RatingTMDB,
			RatingRTCritic:    localized.RatingRTCritic,
			RatingRTAudience:  localized.RatingRTAudience,
			Studios:           localized.Studios,
			Networks:          localized.Networks,
			Countries:         localized.Countries,
			FirstAirDate:      localized.FirstAirDate,
			LastAirDate:       localized.LastAirDate,
			ReleaseDate:       localized.ReleaseDate,
			ShowStatus:        localized.ShowStatus,
			SeasonCount:       localized.SeasonCount,
			PosterThumbhash:   localized.PosterThumbhash,
			BackdropThumbhash: localized.BackdropThumbhash,
			PosterURL:         urls[p.poster].URL,
			BackdropURL:       urls[p.backdrop].URL,
			LogoURL:           urls[p.logo].URL,
			Versions:          []FileVersion{},
			PlaybackVariants:  []PlaybackVariant{},
			Subtitles:         []SubtitleInfo{},
		}
	}
	return result, nil
}

// addTyped normalizes a non-poster path with its own ladder before adding it.
func addTyped(add func(string) string, path, imageType, size string) string {
	if path == "" || path == "-" {
		return ""
	}
	// add() normalizes with the poster ladder; pre-normalize with the right
	// type so the poster rewrite is a no-op on an already-variant path.
	return add(cachedImageVariantPath(path, imageTypeOrDefault(path, imageType), size))
}

// imageTypeOrDefault reads the ladder off a cached key, falling back to the
// slot's default for keys that do not encode one.
func imageTypeOrDefault(path, fallback string) string {
	if t := imageTypeFromCachedPath(path); t != "" {
		return t
	}
	return fallback
}
