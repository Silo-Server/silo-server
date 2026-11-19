package handlers

import (
	"context"
	"log/slog"
	"sync"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/ctxerr"
	"github.com/Silo-Server/silo-server/internal/models"
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

// seriesAvailability is the season counts of a set of series: what every
// badge on their series and season cards is built from.
type seriesAvailability struct {
	seasons    map[string]map[int]catalog.SeasonAvailability
	incomplete map[string]bool
}

// usableSeasons is the season counts of a series the set counted, unless its
// episode list is incomplete.
func (a *seriesAvailability) usableSeasons(seriesID string) (map[int]catalog.SeasonAvailability, bool) {
	if a == nil || a.incomplete[seriesID] {
		return nil, false
	}
	seasons, ok := a.seasons[seriesID]
	return seasons, ok
}

// loadSeriesAvailability counts the seasons of seriesIDs. It returns nil when
// there is nothing to count or a read fails, which leaves those cards without
// a badge.
func loadSeriesAvailability(ctx context.Context, store availabilityStore, access catalog.AccessFilter, seriesIDs []string) *seriesAvailability {
	if store == nil || len(seriesIDs) == 0 {
		return nil
	}
	seasons, err := store.SeriesSeasonAvailability(ctx, seriesIDs, access)
	if err != nil {
		slog.Log(ctx, ctxerr.LogLevel(ctx, err, slog.LevelWarn), "card availability: season counts", "component", "api", "error", err)
		return nil
	}
	incomplete, err := store.EpisodeMetadataIncomplete(ctx, seriesIDs)
	if err != nil {
		slog.Log(ctx, ctxerr.LogLevel(ctx, err, slog.LevelWarn), "card availability: metadata completeness", "component", "api", "error", err)
		return nil
	}
	return &seriesAvailability{seasons: seasons, incomplete: incomplete}
}

// seriesItemIDs is the distinct series among items. A listing knows them
// before it assembles its cards, so it can count their seasons alongside its
// other card lookups instead of after them.
func seriesItemIDs(items []*models.MediaItem) []string {
	var ids []string
	seen := map[string]struct{}{}
	for _, item := range items {
		if item == nil || item.Type != itemTypeSeries || item.ContentID == "" {
			continue
		}
		if _, ok := seen[item.ContentID]; ok {
			continue
		}
		seen[item.ContentID] = struct{}{}
		ids = append(ids, item.ContentID)
	}
	return ids
}

// availabilityForCards builds the badge of each card. counted holds the
// series the caller already counted (preloaded, nil when that read found
// nothing or failed); the rest, such as the series of a season card that is
// only known once the card is assembled, are counted here.
func availabilityForCards(ctx context.Context, store availabilityStore, access catalog.AccessFilter, cards []availabilityCard, counted []string, preloaded *seriesAvailability) map[string]*catalog.Availability {
	if store == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(counted))
	for _, seriesID := range counted {
		seen[seriesID] = struct{}{}
	}
	var seriesIDs []string
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
	loaded := loadSeriesAvailability(ctx, store, access, seriesIDs)
	if loaded == nil && preloaded == nil {
		return nil
	}
	out := make(map[string]*catalog.Availability, len(cards))
	for _, card := range cards {
		seriesID, seasonNumber, ok := card.series()
		if !ok {
			continue
		}
		seasons, ok := preloaded.usableSeasons(seriesID)
		if !ok {
			seasons, ok = loaded.usableSeasons(seriesID)
		}
		if !ok {
			continue
		}
		var badge catalog.Availability
		if seasonNumber == 0 {
			badge, ok = catalog.SeriesBadge(seasons)
		} else {
			badge, ok = catalog.SeasonBadge(seasons[seasonNumber])
		}
		if ok {
			out[card.contentID] = &badge
		}
	}
	return out
}

func (h *ItemsHandler) attachItemListAvailability(ctx context.Context, access catalog.AccessFilter, items []itemListResponse, counted []string, preloaded *seriesAvailability) {
	cards := make([]availabilityCard, len(items))
	for i, item := range items {
		cards[i] = availabilityCard{contentID: item.ContentID, itemType: item.Type, seriesID: item.SeriesID, seasonNumber: item.SeasonNumber}
	}
	byID := availabilityForCards(ctx, h.itemRepo, access, cards, counted, preloaded)
	for i := range items {
		items[i].Availability = byID[items[i].ContentID]
	}
}

// sectionAvailability is the availability lookup of one section read.
type sectionAvailability struct {
	store   availabilityStore
	access  catalog.AccessFilter
	counted []string
	counts  *seriesAvailability
}

// startSectionAvailability counts the seasons of the rows' series cards on
// wg, alongside the other card lookups, since those series are known before
// the cards are assembled. It returns nil, and counts nothing, when the read
// does not carry availability.
func (h *SectionHandler) startSectionAvailability(ctx context.Context, wg *sync.WaitGroup, timings *sectionBuildTimings, include bool, access catalog.AccessFilter, items []*models.MediaItem) *sectionAvailability {
	if !include {
		return nil
	}
	lookup := &sectionAvailability{store: h.fetcher, access: access, counted: seriesItemIDs(items)}
	goSectionLookup(ctx, wg, sectionLookupAvailability, timings, func() {
		lookup.counts = loadSeriesAvailability(ctx, lookup.store, lookup.access, lookup.counted)
	})
	return lookup
}

// attach fills Availability on every card of every section, once wg is done.
// A season card's series may only be known from the assembled card, so any
// series not counted with the other lookups is counted here.
func (l *sectionAvailability) attach(ctx context.Context, sections []resolvedSectionResponse) {
	if l == nil {
		return
	}
	var cards []availabilityCard
	for _, section := range sections {
		for _, item := range section.Items {
			cards = append(cards, availabilityCard{contentID: item.ContentID, itemType: item.Type, seriesID: item.SeriesID, seasonNumber: item.SeasonNumber})
		}
	}
	byID := availabilityForCards(ctx, l.store, l.access, cards, l.counted, l.counts)
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
	byID := availabilityForCards(ctx, h.itemRepo, access, cards, nil, nil)
	for i := range seasons {
		seasons[i].Availability = byID[seasons[i].ContentID]
	}
}
