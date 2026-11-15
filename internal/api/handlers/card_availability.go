package handlers

import (
	"context"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/ctxerr"
)

const itemTypeSeason = "season"

type availabilityCard struct {
	contentID    string
	itemType     string
	seriesID     string
	seasonNumber *int
}

// series is the series whose seasons the card's badge counts, and the season
// number for a season card (0 for a series card). ok is false for a card that
// carries no badge.
func (c availabilityCard) series() (seriesID string, seasonNumber int, ok bool) {
	switch c.itemType {
	case itemTypeSeries:
		return c.contentID, 0, c.contentID != ""
	case itemTypeSeason:
		if c.seriesID == "" || c.seasonNumber == nil || *c.seasonNumber <= 0 {
			return "", 0, false
		}
		return c.seriesID, *c.seasonNumber, true
	default:
		return "", 0, false
	}
}

type availabilityStore interface {
	SeriesSeasonAvailability(ctx context.Context, seriesContentIDs []string, filter catalog.AccessFilter) (map[string]map[int]catalog.SeasonAvailability, error)
	EpisodeMetadataIncomplete(ctx context.Context, seriesContentIDs []string) (map[string]bool, error)
}

func availabilityForCards(ctx context.Context, store availabilityStore, access catalog.AccessFilter, cards []availabilityCard) map[string]*catalog.Availability {
	if store == nil {
		return nil
	}
	seriesIDs := make([]string, 0, len(cards))
	seen := map[string]struct{}{}
	for _, card := range cards {
		seriesID, _, ok := card.series()
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
	bySeries, err := store.SeriesSeasonAvailability(ctx, seriesIDs, access)
	if err != nil {
		slog.Log(ctx, ctxerr.LogLevel(ctx, err, slog.LevelWarn), "card availability: season counts", "component", "api", "error", err)
		return nil
	}
	incomplete, err := store.EpisodeMetadataIncomplete(ctx, seriesIDs)
	if err != nil {
		slog.Log(ctx, ctxerr.LogLevel(ctx, err, slog.LevelWarn), "card availability: metadata completeness", "component", "api", "error", err)
		return nil
	}
	out := make(map[string]*catalog.Availability, len(cards))
	for _, card := range cards {
		seriesID, seasonNumber, ok := card.series()
		if !ok || incomplete[seriesID] {
			continue
		}
		var badge catalog.Availability
		if seasonNumber == 0 {
			badge, ok = catalog.SeriesBadge(bySeries[seriesID])
		} else {
			badge, ok = catalog.SeasonBadge(bySeries[seriesID][seasonNumber])
		}
		if ok {
			out[card.contentID] = &badge
		}
	}
	return out
}

func (h *ItemsHandler) attachItemListAvailability(ctx context.Context, access catalog.AccessFilter, items []itemListResponse) {
	cards := make([]availabilityCard, len(items))
	for i, item := range items {
		cards[i] = availabilityCard{contentID: item.ContentID, itemType: item.Type, seriesID: item.SeriesID, seasonNumber: item.SeasonNumber}
	}
	byID := availabilityForCards(ctx, h.itemRepo, access, cards)
	for i := range items {
		items[i].Availability = byID[items[i].ContentID]
	}
}

// attachSectionAvailability fills Availability on every card of every
// section with one lookup.
func attachSectionAvailability(ctx context.Context, store availabilityStore, access catalog.AccessFilter, sections []resolvedSectionResponse) {
	var cards []availabilityCard
	for _, section := range sections {
		for _, item := range section.Items {
			cards = append(cards, availabilityCard{contentID: item.ContentID, itemType: item.Type, seriesID: item.SeriesID, seasonNumber: item.SeasonNumber})
		}
	}
	byID := availabilityForCards(ctx, store, access, cards)
	for si := range sections {
		for ii := range sections[si].Items {
			sections[si].Items[ii].Availability = byID[sections[si].Items[ii].ContentID]
		}
	}
}

func (h *ItemsHandler) attachSeasonListAvailability(ctx context.Context, access catalog.AccessFilter, seriesID string, seasons []seasonResponse) {
	cards := make([]availabilityCard, 0, len(seasons))
	for i := range seasons {
		if seasons[i].IsSpecials || seasons[i].SeasonNumber <= 0 {
			continue
		}
		number := seasons[i].SeasonNumber
		cards = append(cards, availabilityCard{
			contentID:    seasons[i].ContentID,
			itemType:     itemTypeSeason,
			seriesID:     seriesID,
			seasonNumber: &number,
		})
	}
	byID := availabilityForCards(ctx, h.itemRepo, access, cards)
	for i := range seasons {
		seasons[i].Availability = byID[seasons[i].ContentID]
	}
}
