package handlers

import (
	"context"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type availabilityCard struct {
	contentID    string
	itemType     string
	seriesID     string
	seasonNumber *int
}

func (c availabilityCard) seriesAndKind() (seriesID string, seasonNumber int, kind string, ok bool) {
	switch c.itemType {
	case "series":
		if c.contentID == "" {
			return "", 0, "", false
		}
		return c.contentID, 0, "series", true
	case "season":
		if c.seriesID == "" || c.seasonNumber == nil || *c.seasonNumber <= 0 {
			return "", 0, "", false
		}
		return c.seriesID, *c.seasonNumber, "season", true
	default:
		return "", 0, "", false
	}
}

type availabilityStore interface {
	SeriesSeasonAvailability(ctx context.Context, seriesContentIDs []string) (map[string]map[int]catalog.SeasonAvailability, error)
	EpisodeMetadataIncomplete(ctx context.Context, seriesContentIDs []string) (map[string]bool, error)
}

func availabilityForCards(ctx context.Context, store availabilityStore, cards []availabilityCard) map[string]*catalog.Availability {
	if store == nil {
		return nil
	}
	seriesIDs := make([]string, 0, len(cards))
	seen := map[string]struct{}{}
	for _, card := range cards {
		seriesID, _, _, ok := card.seriesAndKind()
		if !ok {
			continue
		}
		if _, ok := seen[seriesID]; ok {
			continue
		}
		seen[seriesID] = struct{}{}
		seriesIDs = append(seriesIDs, seriesID)
	}
	if len(seriesIDs) == 0 {
		return nil
	}
	bySeries, err := store.SeriesSeasonAvailability(ctx, seriesIDs)
	if err != nil {
		slog.WarnContext(ctx, "card availability: season counts", "component", "api", "error", err)
		return nil
	}
	incomplete, err := store.EpisodeMetadataIncomplete(ctx, seriesIDs)
	if err != nil {
		slog.WarnContext(ctx, "card availability: metadata completeness", "component", "api", "error", err)
		return nil
	}
	out := make(map[string]*catalog.Availability, len(cards))
	for _, card := range cards {
		seriesID, seasonNumber, kind, ok := card.seriesAndKind()
		if !ok || incomplete[seriesID] {
			continue
		}
		var badge catalog.Availability
		switch kind {
		case "series":
			badge, ok = catalog.SeriesBadge(bySeries[seriesID])
		case "season":
			badge, ok = catalog.SeasonBadge(bySeries[seriesID][seasonNumber])
		}
		if !ok {
			continue
		}
		copied := badge
		out[card.contentID] = &copied
	}
	return out
}

func (h *ItemsHandler) attachItemListAvailability(ctx context.Context, items []itemListResponse) {
	cards := make([]availabilityCard, len(items))
	for i, item := range items {
		cards[i] = availabilityCard{contentID: item.ContentID, itemType: item.Type, seriesID: item.SeriesID, seasonNumber: item.SeasonNumber}
	}
	byID := availabilityForCards(ctx, h.itemRepo, cards)
	for i := range items {
		items[i].Availability = byID[items[i].ContentID]
	}
}

func fillSectionAvailability(ctx context.Context, store availabilityStore, items []sectionItemResponse) {
	cards := make([]availabilityCard, len(items))
	for i, item := range items {
		cards[i] = availabilityCard{contentID: item.ContentID, itemType: item.Type, seriesID: item.SeriesID, seasonNumber: item.SeasonNumber}
	}
	byID := availabilityForCards(ctx, store, cards)
	for i := range items {
		items[i].Availability = byID[items[i].ContentID]
	}
}

func (h *ItemsHandler) attachSeasonListAvailability(ctx context.Context, seriesID string, seasons []seasonResponse) {
	cards := make([]availabilityCard, 0, len(seasons))
	for i := range seasons {
		if seasons[i].IsSpecials || seasons[i].SeasonNumber <= 0 {
			continue
		}
		number := seasons[i].SeasonNumber
		cards = append(cards, availabilityCard{
			contentID:    seasons[i].ContentID,
			itemType:     "season",
			seriesID:     seriesID,
			seasonNumber: &number,
		})
	}
	byID := availabilityForCards(ctx, h.itemRepo, cards)
	for i := range seasons {
		seasons[i].Availability = byID[seasons[i].ContentID]
	}
}
