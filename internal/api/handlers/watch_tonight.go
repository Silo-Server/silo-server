package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// watchTonightSectionFetcher provides access to live Continue Watching
// and Next Up data for the Watch Tonight handler.
type watchTonightSectionFetcher interface {
	FetchNextUpItems(ctx context.Context, userID int, profileID string, libraryID *int, libraryIDs []int, filter catalog.AccessFilter, limit int) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error)
}

type watchTonightItemResponse struct {
	sectionItemResponse
	WatchTonightSource string `json:"watch_tonight_source"`
}

type watchTonightResponse struct {
	Items  []watchTonightItemResponse `json:"items"`
	IsCold bool                       `json:"is_cold"`
}

// Source boost multipliers for ranking.
const (
	boostCWInTaste    = 1.40 // in-progress AND in taste pool
	boostNextInTaste  = 1.25 // next-up AND in taste pool
	baseCWScore       = 2.00 // in-progress items — always rank above pure recommendations (max ~1.2)
	baseNextUpScore   = 1.50 // next-up items — rank above recommendations but below in-progress
	watchTonightLimit = 5
)

// HandleWatchTonight handles GET /recommendations/watch-tonight.
// It merges live Continue Watching / Next Up items with recommendation-engine
// candidates, applies taste-profile boosts, enriches, and returns a unified list.
func (h *RecommendationsHandler) HandleWatchTonight(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	filter := requestAccessFilter(r)

	limit := watchTonightLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 20 {
			limit = n
		}
	}

	// Fan out: recommendation candidates + live CW/Next-Up in parallel.
	var (
		recResult recommendations.WatchTonightResult
		recErr    error

		cwItems     []scoredCWItem
		nextUpItems []scoredCWItem
		liveErr     error
	)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if h.reader != nil {
			recResult, recErr = h.reader.GetWatchTonight(r.Context(), userID, profileID, limit*3, filter)
		}
	}()

	go func() {
		defer wg.Done()
		cwItems, nextUpItems, liveErr = h.fetchLiveCWAndNextUp(r, userID, profileID, filter, limit)
	}()

	wg.Wait()

	if recErr != nil {
		slog.Error("WatchTonight: recommendations fetch failed", "user_id", userID, "error", recErr)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch recommendations")
		return
	}
	if liveErr != nil {
		slog.Warn("WatchTonight: live CW/NextUp fetch failed, continuing with recs only", "user_id", userID, "error", liveErr)
	}

	// Filter out already-watched and low-rated items from rec candidates.
	recResult.Items = h.filterRecommendations(r, userID, profileID, recResult.Items)

	// Build taste set from recommendation items for boost lookups.
	tasteSet := make(map[string]struct{}, len(recResult.Items))
	for _, item := range recResult.Items {
		tasteSet[item.MediaItemID] = struct{}{}
	}

	// Merge everything into a single scored list.
	byID := make(map[string]mergedItem, len(recResult.Items)+len(cwItems)+len(nextUpItems))

	// Insert recommendation items first.
	for _, item := range recResult.Items {
		byID[item.MediaItemID] = mergedItem{
			scored: item,
			source: "recommendation",
		}
	}

	// Overlay CW items — these should always rank highly since the user
	// actively started watching them.
	for _, cw := range cwItems {
		existing, exists := byID[cw.contentID]
		if exists {
			// Item is in both CW and recommendations — boost the rec score.
			existing.scored.Score *= boostCWInTaste
			existing.source = "continue_watching"
			existing.meta = &cw.meta
			byID[cw.contentID] = existing
		} else {
			// Item is in CW only — still high priority.
			byID[cw.contentID] = mergedItem{
				scored: recommendations.ScoredItem{
					MediaItemID: cw.contentID,
					Score:       baseCWScore,
					Reason:      "continue_watching",
				},
				source: "continue_watching",
				meta:   &cw.meta,
			}
		}
	}

	// Overlay Next-Up items — next episode of something the user watched.
	for _, nu := range nextUpItems {
		existing, exists := byID[nu.contentID]
		if exists {
			if existing.source != "continue_watching" {
				// Item is in both next-up and recommendations — boost.
				existing.scored.Score *= boostNextInTaste
				existing.source = "next_up"
				existing.meta = &nu.meta
				byID[nu.contentID] = existing
			}
			// CW wins over next-up for same item
		} else {
			// Item is in next-up only — still high priority.
			byID[nu.contentID] = mergedItem{
				scored: recommendations.ScoredItem{
					MediaItemID: nu.contentID,
					Score:       baseNextUpScore,
					Reason:      "next_up",
				},
				source: "next_up",
				meta:   &nu.meta,
			}
		}
	}

	// Sort and trim.
	merged := make([]mergedItem, 0, len(byID))
	for _, m := range byID {
		merged = append(merged, m)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].scored.Score > merged[j].scored.Score
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}

	// Collect content IDs for enrichment.
	contentIDs := make([]string, len(merged))
	for i, m := range merged {
		contentIDs[i] = m.scored.MediaItemID
	}

	// Enrich with full media item details.
	resp := watchTonightResponse{
		Items:  make([]watchTonightItemResponse, 0, len(merged)),
		IsCold: recResult.IsCold && len(cwItems) == 0 && len(nextUpItems) == 0,
	}

	if len(contentIDs) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	itemMap, overlayMap, stateMap, enrichedEpMeta := h.enrichItems(r, userID, profileID, filter, contentIDs)

	for _, m := range merged {
		mi, ok := itemMap[m.scored.MediaItemID]
		if !ok || mi == nil {
			continue
		}

		item := h.buildSectionItem(r, mi, overlayMap, stateMap)
		item.ItemSource = m.source

		// Apply CW/Next-Up metadata (position, duration, progress).
		if m.meta != nil {
			item.PositionSeconds = m.meta.PositionSeconds
			item.DurationSeconds = m.meta.DurationSeconds
			item.ProgressUpdatedAt = m.meta.ProgressUpdatedAt
			if m.meta.SeriesID != nil {
				item.SeriesID = *m.meta.SeriesID
			}
			if m.meta.SeriesTitle != "" {
				item.SeriesTitle = m.meta.SeriesTitle
			}
			if m.meta.SeasonNumber != nil {
				item.SeasonNumber = m.meta.SeasonNumber
			}
			if m.meta.EpisodeNumber != nil {
				item.EpisodeNumber = m.meta.EpisodeNumber
			}
		}

		// Fill in episode metadata from the enrichment step for items that came
		// through the episodes table (CW/Next-Up episodes).
		if epMeta, ok := enrichedEpMeta[m.scored.MediaItemID]; ok {
			if epMeta.SeriesID != nil && item.SeriesID == "" {
				item.SeriesID = *epMeta.SeriesID
			}
			if epMeta.SeriesTitle != "" && item.SeriesTitle == "" {
				item.SeriesTitle = epMeta.SeriesTitle
			}
			if epMeta.SeasonNumber != nil && item.SeasonNumber == nil {
				item.SeasonNumber = epMeta.SeasonNumber
			}
			if epMeta.EpisodeNumber != nil && item.EpisodeNumber == nil {
				item.EpisodeNumber = epMeta.EpisodeNumber
			}
		}

		resp.Items = append(resp.Items, watchTonightItemResponse{
			sectionItemResponse: item,
			WatchTonightSource:  m.source,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

type mergedItem struct {
	scored recommendations.ScoredItem
	source string // "continue_watching", "next_up", "recommendation"
	meta   *sections.SectionItemMeta
}

type scoredCWItem struct {
	contentID string
	meta      sections.SectionItemMeta
}

// fetchLiveCWAndNextUp fetches live Continue Watching and Next Up items from the user store.
func (h *RecommendationsHandler) fetchLiveCWAndNextUp(r *http.Request, userID int, profileID string, filter catalog.AccessFilter, limit int) (cwItems, nextUpItems []scoredCWItem, err error) {
	if h.storeProvider == nil || userID <= 0 || profileID == "" {
		return nil, nil, nil
	}

	ctx := r.Context()

	// Fetch in-progress items from user store.
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, nil, err
	}

	progressEntries, err := store.ListProgress(ctx, profileID, "in_progress", limit, 0)
	if err != nil {
		return nil, nil, err
	}

	for _, entry := range progressEntries {
		position := entry.PositionSeconds
		duration := entry.DurationSeconds
		updatedAt := entry.UpdatedAt
		cwItems = append(cwItems, scoredCWItem{
			contentID: entry.MediaItemID,
			meta: sections.SectionItemMeta{
				PositionSeconds:   &position,
				DurationSeconds:   &duration,
				ProgressUpdatedAt: &updatedAt,
				ItemSource:        "in_progress",
			},
		})
	}

	// Fetch next-up items.
	if h.WatchTonightFetcher != nil {
		nuItems, nuMeta, nuErr := h.WatchTonightFetcher.FetchNextUpItems(ctx, userID, profileID, nil, nil, filter, limit)
		if nuErr != nil {
			slog.Warn("WatchTonight: next-up fetch failed", "user_id", userID, "error", nuErr)
		} else {
			for _, item := range nuItems {
				meta, ok := nuMeta[item.ContentID]
				if !ok {
					continue
				}
				nextUpItems = append(nextUpItems, scoredCWItem{
					contentID: item.ContentID,
					meta:      meta,
				})
			}
		}
	}

	return cwItems, nextUpItems, nil
}

// enrichItems fetches full MediaItem objects, overlay summaries, and user states
// for the given content IDs. Handles both movies/series (from media_items) and
// episodes (from episodes table) since CW/Next-Up items are episode content IDs.
func (h *RecommendationsHandler) enrichItems(r *http.Request, userID int, profileID string, filter catalog.AccessFilter, contentIDs []string) (
	itemMap map[string]*models.MediaItem,
	overlayMap map[string]*models.OverlaySummary,
	stateMap map[string]*itemUserStateResponse,
	episodeMeta map[string]sections.SectionItemMeta,
) {
	if h.Fetcher == nil || len(contentIDs) == 0 {
		return nil, nil, nil, nil
	}

	ctx := r.Context()

	// Fetch movies/series from media_items table.
	mediaItems, err := h.Fetcher.FetchItemsByContentIDs(ctx, contentIDs, filter)
	if err != nil {
		slog.Error("WatchTonight: fetch items failed", "error", err)
		return nil, nil, nil, nil
	}

	itemMap = make(map[string]*models.MediaItem, len(mediaItems)*2)
	for _, mi := range mediaItems {
		itemMap[mi.ContentID] = mi
	}

	// Also fetch episodes — CW/Next-Up content IDs are typically episode IDs.
	episodeItems, epMeta, epErr := h.Fetcher.FetchEpisodesByContentIDs(ctx, contentIDs, filter)
	if epErr != nil {
		slog.Error("WatchTonight: fetch episodes failed", "error", epErr)
	} else {
		for _, item := range episodeItems {
			itemMap[item.ContentID] = item
		}
		episodeMeta = epMeta
		mediaItems = append(mediaItems, episodeItems...)
	}

	overlays, err := h.Fetcher.ListOverlaySummaries(ctx, contentIDs, filter)
	if err != nil {
		slog.Error("WatchTonight: overlay summaries failed", "error", err)
	} else {
		overlayMap = overlays
	}

	if h.storeProvider != nil {
		store, storeErr := h.storeProvider.ForUser(ctx, userID)
		if storeErr == nil && store != nil {
			states, stateErr := resolveItemUserStatesWithOptions(ctx, store, profileID, h.EpisodeRepo, mediaItems, itemUserStateOptions{
				UserID:             userID,
				EbookProgressStore: h.EbookProgress,
			})
			if stateErr == nil {
				stateMap = states
			}
		}
	}

	return itemMap, overlayMap, stateMap, episodeMeta
}

// buildSectionItem constructs a sectionItemResponse from a MediaItem,
// applying presigning and overlay/state lookups.
func (h *RecommendationsHandler) buildSectionItem(r *http.Request, mi *models.MediaItem, overlayMap map[string]*models.OverlaySummary, stateMap map[string]*itemUserStateResponse) sectionItemResponse {
	item := sectionItemResponse{
		ContentID:         mi.ContentID,
		Type:              mi.Type,
		Title:             mi.Title,
		Year:              mi.Year,
		Genres:            mi.Genres,
		Status:            mi.Status,
		RatingIMDB:        mi.RatingIMDB,
		RatingTMDB:        mi.RatingTMDB,
		RatingRTCritic:    mi.RatingRTCritic,
		RatingRTAudience:  mi.RatingRTAudience,
		OriginalLanguage:  mi.OriginalLanguage,
		Overview:          mi.Overview,
		PosterThumbhash:   mi.PosterThumbhash,
		BackdropThumbhash: mi.BackdropThumbhash,
	}
	if item.Genres == nil {
		item.Genres = []string{}
	}
	if h.DetailSvc != nil {
		item.PosterURL = h.DetailSvc.PresignURL(r.Context(), featuredPosterPath(mi.PosterPath), "featured")
		item.BackdropURL = h.DetailSvc.PresignURL(r.Context(), featuredBackdropPath(mi.BackdropPath), "featured")
		item.LogoURL = h.DetailSvc.PresignURL(r.Context(), mi.LogoPath, "featured")
	}
	if overlayMap != nil {
		item.OverlaySummary = overlayMap[mi.ContentID]
	}
	if stateMap != nil {
		item.UserState = stateMap[mi.ContentID]
	}
	return item
}
