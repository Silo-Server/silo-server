package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// cardsCastFetcher fetches cast credits for a batch of media items.
type cardsCastFetcher interface {
	ListForItems(ctx context.Context, contentIDs []string) (map[string][]models.ItemPerson, error)
}

// swipeCardCastMember is a single cast member on a swipe card.
type swipeCardCastMember struct {
	Name      string `json:"name"`
	Character string `json:"character,omitempty"`
	PhotoURL  string `json:"photo_url,omitempty"`
}

// swipeCardResponse extends sectionItemResponse with cast and runtime for the card back.
type swipeCardResponse struct {
	sectionItemResponse
	WatchTonightSource string                `json:"watch_tonight_source"`
	Runtime            int                   `json:"runtime,omitempty"`
	Cast               []swipeCardCastMember `json:"cast"`
}

// swipeCardsPageResponse is the response for GET /recommendations/watch-tonight/cards.
type swipeCardsPageResponse struct {
	Cards   []swipeCardResponse `json:"cards"`
	HasMore bool                `json:"has_more"`
	IsCold  bool                `json:"is_cold"`
}

const (
	swipeDefaultLimit   = 12
	swipeMaxLimit       = 20
	maxExcludeIDs       = 200
	maxCastPerCard      = 4
	discoverMinPoolSize = 120
	discoverMaxPoolSize = 500
)

// knownGenres is the validated set of genre names accepted by the cards endpoint.
var knownGenres = map[string]struct{}{
	"Action": {}, "Adventure": {}, "Animation": {}, "Comedy": {},
	"Crime": {}, "Documentary": {}, "Drama": {}, "Family": {},
	"Fantasy": {}, "History": {}, "Horror": {}, "Music": {},
	"Mystery": {}, "Romance": {}, "Science Fiction": {}, "Thriller": {},
	"War": {}, "Western": {},
}

// HandleWatchTonightCards handles GET /recommendations/watch-tonight/cards.
// It returns paginated, genre-filterable swipe cards with cast data for the
// gamified Watch Tonight experience.
func (h *RecommendationsHandler) HandleWatchTonightCards(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	filter := requestAccessFilter(r)

	mode := r.URL.Query().Get("mode")
	if mode != "continue" && mode != "discover" {
		writeError(w, http.StatusBadRequest, "invalid_mode", `mode must be "continue" or "discover"`)
		return
	}

	genres := parseGenreParams(r.URL.Query()["genres[]"])
	excludeIDs := parseExcludeIDs(r.URL.Query()["exclude_ids[]"])
	limit := swipeDefaultLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= swipeMaxLimit {
			limit = n
		}
	}

	var merged []mergedItem
	var isCold bool

	if mode == "continue" {
		merged, isCold = h.buildContinueCards(r, userID, profileID, filter, excludeIDs, limit)
	} else {
		merged, isCold = h.buildDiscoverCards(r, userID, profileID, genres, excludeIDs, limit)
	}

	resp := swipeCardsPageResponse{
		Cards:  make([]swipeCardResponse, 0, limit),
		IsCold: isCold,
	}

	if len(merged) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Over-fetch for enrichment so we can still fill a full page after item
	// lookups drop inaccessible or missing entries. Trim to limit+buffer to
	// control enrichment cost.
	enrichLimit := limit + 10
	if len(merged) > enrichLimit {
		merged = merged[:enrichLimit]
	}

	// Collect content IDs for enrichment.
	contentIDs := make([]string, len(merged))
	for i, m := range merged {
		contentIDs[i] = m.scored.MediaItemID
	}

	itemMap, overlayMap, stateMap, enrichedEpMeta := h.enrichItems(r, userID, profileID, filter, contentIDs)

	// Determine if there are more items beyond what we return.
	resp.HasMore = len(merged) > limit
	if len(merged) > limit {
		merged = merged[:limit]
	}

	// First pass: build enriched items and collect cast lookup IDs.
	type enrichedCard struct {
		item    sectionItemResponse
		source  string
		runtime int
	}
	enrichedCards := make([]enrichedCard, 0, len(merged))
	castLookupIDs := make([]string, 0, len(merged))
	castLookupSeen := make(map[string]struct{}, len(merged))
	// Map content ID → cast lookup ID for the second pass.
	contentToCastLookup := make(map[string]string, len(merged))

	for _, m := range merged {
		mi, ok := itemMap[m.scored.MediaItemID]
		if !ok || mi == nil {
			continue
		}

		item := h.buildSectionItem(r, mi, overlayMap, stateMap)
		item.ItemSource = m.source

		// Apply CW/Next-Up metadata.
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

		// Resolve cast lookup ID: use series ID for episodes.
		castLookupID := m.scored.MediaItemID
		if item.SeriesID != "" {
			castLookupID = item.SeriesID
		}
		contentToCastLookup[m.scored.MediaItemID] = castLookupID
		if _, seen := castLookupSeen[castLookupID]; !seen {
			castLookupSeen[castLookupID] = struct{}{}
			castLookupIDs = append(castLookupIDs, castLookupID)
		}

		enrichedCards = append(enrichedCards, enrichedCard{
			item:    item,
			source:  m.source,
			runtime: mi.Runtime,
		})
	}

	// Fetch cast using the properly resolved lookup IDs.
	castMap := h.fetchCastByIDs(r.Context(), castLookupIDs)

	// Second pass: assemble response cards with cast.
	for i, ec := range enrichedCards {
		lookupID := contentToCastLookup[merged[i].scored.MediaItemID]
		cast := buildCastSlice(castMap, lookupID)

		resp.Cards = append(resp.Cards, swipeCardResponse{
			sectionItemResponse: ec.item,
			WatchTonightSource:  ec.source,
			Runtime:             ec.runtime,
			Cast:                cast,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// buildContinueCards returns CW + Next Up items as swipe cards.
func (h *RecommendationsHandler) buildContinueCards(r *http.Request, userID int, profileID string, filter catalog.AccessFilter, excludeIDs map[string]struct{}, limit int) ([]mergedItem, bool) {
	cwItems, nextUpItems, err := h.fetchLiveCWAndNextUp(r, userID, profileID, filter, limit+len(excludeIDs))
	if err != nil {
		slog.WarnContext(r.Context(), "WatchTonightCards: CW/NextUp fetch failed", "component", "api", "user_id", userID, "error", err)
		return nil, false
	}

	merged := make([]mergedItem, 0, len(cwItems)+len(nextUpItems))

	for _, cw := range cwItems {
		if _, excluded := excludeIDs[cw.contentID]; excluded {
			continue
		}
		merged = append(merged, mergedItem{
			scored: recommendations.ScoredItem{
				MediaItemID: cw.contentID,
				Score:       baseCWScore,
				Reason:      "continue_watching",
			},
			source: "continue_watching",
			meta:   &cw.meta,
		})
	}

	for _, nu := range nextUpItems {
		if _, excluded := excludeIDs[nu.contentID]; excluded {
			continue
		}
		merged = append(merged, mergedItem{
			scored: recommendations.ScoredItem{
				MediaItemID: nu.contentID,
				Score:       baseNextUpScore,
				Reason:      "next_up",
			},
			source: "next_up",
			meta:   &nu.meta,
		})
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].scored.Score > merged[j].scored.Score
	})

	isCold := len(merged) == 0
	return merged, isCold
}

// buildDiscoverCards returns recommendation candidates for discover mode.
// It searches the user's taste profile against the full embedded library,
// optionally requiring at least one selected genre match, and falls back to
// cold-start caches when no taste profile is available.
func (h *RecommendationsHandler) buildDiscoverCards(r *http.Request, userID int, profileID string, genres []string, excludeIDs map[string]struct{}, limit int) ([]mergedItem, bool) {
	if h.recsRepo == nil {
		return nil, true
	}

	ctx := r.Context()
	filter := requestAccessFilter(r)
	poolSize := discoverCandidatePoolSize(limit, len(genres))
	embedding, err := h.recsRepo.GetTasteProfile(ctx, userID, profileID)
	if err != nil {
		slog.WarnContext(r.Context(), "WatchTonightCards: taste profile fetch failed", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
	}

	candidates := []recommendations.ScoredItem{}
	genreMap := map[string][]string{}
	isCold := embedding == nil

	if embedding != nil {
		candidateItems, candidateGenres, err := h.recsRepo.FindTasteProfileCandidates(
			ctx,
			embedding,
			mapKeys(excludeIDs),
			genres,
			poolSize,
			filter,
		)
		if err != nil {
			slog.WarnContext(r.Context(), "WatchTonightCards: taste candidate query failed", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		} else {
			candidates = candidateItems
			genreMap = candidateGenres
		}
	}

	if len(candidates) == 0 {
		isCold = true
		candidates = h.buildColdStartDiscoverCandidates(ctx)
		if len(candidates) > 0 {
			candidateIDs := make([]string, len(candidates))
			for i, item := range candidates {
				candidateIDs[i] = item.MediaItemID
			}
			genreMap, _ = h.recsRepo.GetItemAllGenres(ctx, candidateIDs)
		}
	}

	candidates = h.filterRecommendations(r, userID, profileID, candidates)
	candidates = recommendations.FilterAndRankGenreMatches(candidates, genres, genreMap)

	return recommendationCardItems(candidates, excludeIDs), isCold
}

func recommendationCardItems(candidates []recommendations.ScoredItem, excludeIDs map[string]struct{}) []mergedItem {
	merged := make([]mergedItem, 0, len(candidates))
	for _, item := range candidates {
		if _, excluded := excludeIDs[item.MediaItemID]; excluded {
			continue
		}
		merged = append(merged, mergedItem{
			scored: item,
			source: "recommendation",
		})
	}
	return merged
}

func (h *RecommendationsHandler) buildColdStartDiscoverCandidates(ctx context.Context) []recommendations.ScoredItem {
	byID := make(map[string]recommendations.ScoredItem)

	popular, err := h.recsRepo.GetRecommendationCache(
		ctx,
		recommendations.GlobalCacheUserID,
		recommendations.GlobalCacheProfileID,
		recommendations.RecTypePopular,
		"",
	)
	if err != nil {
		slog.WarnContext(ctx, "WatchTonightCards: popular cache unavailable", "component", "api", "error", err)
	}
	for _, item := range popular {
		byID[item.MediaItemID] = item
	}

	recentlyAdded, err := h.recsRepo.GetRecommendationCache(
		ctx,
		recommendations.GlobalCacheUserID,
		recommendations.GlobalCacheProfileID,
		recommendations.RecTypeRecentlyAdded,
		"",
	)
	if err != nil {
		slog.WarnContext(ctx, "WatchTonightCards: recently added cache unavailable", "component", "api", "error", err)
	}
	for _, item := range recentlyAdded {
		if existing, exists := byID[item.MediaItemID]; !exists || item.Score > existing.Score {
			byID[item.MediaItemID] = item
		}
	}

	candidates := make([]recommendations.ScoredItem, 0, len(byID))
	for _, item := range byID {
		candidates = append(candidates, item)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].MediaItemID < candidates[j].MediaItemID
	})

	return candidates
}

// fetchCastByIDs fetches cast members for the given pre-resolved lookup IDs.
func (h *RecommendationsHandler) fetchCastByIDs(ctx context.Context, lookupIDs []string) map[string][]models.ItemPerson {
	if h.CastFetcher == nil || len(lookupIDs) == 0 {
		return nil
	}
	castMap, err := h.CastFetcher.ListForItems(ctx, lookupIDs)
	if err != nil {
		slog.ErrorContext(ctx, "WatchTonightCards: cast fetch failed", "component", "api", "error", err)
		return nil
	}
	return castMap
}

// buildCastSlice builds a capped list of actor cast members for a card.
func buildCastSlice(castMap map[string][]models.ItemPerson, lookupID string) []swipeCardCastMember {
	if castMap == nil {
		return []swipeCardCastMember{}
	}
	people, ok := castMap[lookupID]
	if !ok || len(people) == 0 {
		return []swipeCardCastMember{}
	}

	cast := make([]swipeCardCastMember, 0, maxCastPerCard)
	for _, p := range people {
		if p.Kind != models.PersonKindActor {
			continue
		}
		cast = append(cast, swipeCardCastMember{
			Name:      p.Name,
			Character: p.Character,
		})
		if len(cast) >= maxCastPerCard {
			break
		}
	}
	return cast
}

// parseGenreParams validates and deduplicates genre query params.
func parseGenreParams(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	genres := make([]string, 0, len(raw))
	for _, g := range raw {
		g = strings.TrimSpace(g)
		if _, valid := knownGenres[g]; !valid {
			continue
		}
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		genres = append(genres, g)
	}
	return genres
}

func discoverCandidatePoolSize(limit int, genreCount int) int {
	multiplier := 15
	if genreCount > 0 {
		multiplier = 20
	}

	size := limit * multiplier
	if size < discoverMinPoolSize {
		size = discoverMinPoolSize
	}
	if size > discoverMaxPoolSize {
		size = discoverMaxPoolSize
	}
	return size
}

func mapKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// parseExcludeIDs extracts and caps the exclude_ids query params.
func parseExcludeIDs(raw []string) map[string]struct{} {
	if len(raw) == 0 {
		return nil
	}
	ids := make(map[string]struct{}, len(raw))
	for _, id := range raw {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		ids[id] = struct{}{}
		if len(ids) >= maxExcludeIDs {
			break
		}
	}
	return ids
}
