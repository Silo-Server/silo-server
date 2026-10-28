package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	tasteSeedDefaultLimit = 30
	tasteSeedMaxLimit     = 60
	tasteSeedMaxPicks     = 200
)

type tasteSeedItemsResponse struct {
	Items      []sectionItemResponse `json:"items"`
	NextOffset *int                  `json:"next_offset,omitempty"`
}

type tasteSeedSubmitRequest struct {
	ItemIDs []string `json:"item_ids"`
}

type tasteSeedSubmitResponse struct {
	Added int `json:"added"`
}

func (h *RecommendationsHandler) resolveTasteSeedUserStates(ctx context.Context, userID int, profileID string, mediaItems []*models.MediaItem) map[string]*itemUserStateResponse {
	if h.storeProvider == nil || len(mediaItems) == 0 {
		return nil
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil || store == nil {
		return nil
	}
	states, err := resolveItemUserStatesWithOptions(ctx, store, profileID, h.EpisodeRepo, mediaItems, itemUserStateOptions{
		UserID:             userID,
		EbookProgressStore: h.EbookProgress,
	})
	if err != nil {
		return nil
	}
	return states
}

// tasteSeedSectionItem builds the trimmed section item used by the seed grid
// from its resolved poster (see tasteSeedPosterURLs). We only populate poster
// fields — the picker doesn't need overlays, ratings, or progress.
func tasteSeedSectionItem(mi *models.MediaItem, stateMap map[string]*itemUserStateResponse, posterURL string) sectionItemResponse {
	item := sectionItemResponse{
		ContentID:       mi.ContentID,
		Type:            mi.Type,
		Title:           mi.Title,
		Year:            mi.Year,
		Genres:          mi.Genres,
		Status:          mi.Status,
		PosterThumbhash: mi.PosterThumbhash,
	}
	if item.Genres == nil {
		item.Genres = []string{}
	}
	if item.Keywords == nil {
		item.Keywords = []string{}
	}
	item.PosterURL = posterURL
	if stateMap != nil {
		item.UserState = stateMap[mi.ContentID]
	}
	return item
}

// tasteSeedPosterURLs resolves the seed grid's card posters in one batch, at
// the request's poster size or width, keyed by content ID.
func (h *RecommendationsHandler) tasteSeedPosterURLs(ctx context.Context, items []*models.MediaItem, art imagesize.Request) map[string]string {
	urls := make(map[string]string, len(items))
	if h.DetailSvc == nil || len(items) == 0 {
		return urls
	}
	size := art.For(imagesize.SlotPoster)
	sized := make(map[string]string, len(items))
	paths := make([]string, 0, len(items))
	for _, mi := range items {
		if mi == nil {
			continue
		}
		path := sizedCardPath(mi.PosterPath, artworkkey.ImagePoster, size)
		sized[mi.ContentID] = path
		paths = append(paths, path)
	}
	resolved := h.DetailSvc.PresignURLsWithExpiry(ctx, paths, requestVariantHint("card", size))
	for contentID, path := range sized {
		urls[contentID] = resolved[path].URL
	}
	return urls
}
