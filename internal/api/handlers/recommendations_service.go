package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// Recommendation seams: the profile-scoped /recommendations reads the v1
// handlers and the v2 operations share. Each returns the view the v1 handler
// writes verbatim and, on failure, an *APIError. The *Cards variants render
// the scored identifiers the engine answers with into the section cards the
// discover and section reads already build, so a v2 listing is the same card
// surface as discover rather than a list of bare identifiers.

// DiscoverView is the discover page: its rows with their cards.
type DiscoverView = discoverResponse

// DiscoverRowView is one recommendation row with its cards.
type DiscoverRowView = discoverRowResponse

// SectionDetailView is one recommendation section in full.
type SectionDetailView = sectionDetailResponse

const (
	recommendationsDefaultLimit = 20
	// discoverClusterRowType is the row type of every taste-cluster row,
	// including the main For You row.
	discoverClusterRowType = "cluster"
)

func recommendationsUnavailable(message string) *APIError {
	return apiError(http.StatusInternalServerError, "internal_error", message)
}

// BecauseWatched answers "because you watched {item}" candidates, minus the
// profile's watched and low-rated items. An unavailable engine answers an
// empty list.
func (h *RecommendationsHandler) BecauseWatched(ctx context.Context, userID int, profileID, itemID string, limit int) ([]recommendations.ScoredItem, error) {
	if h.engineUnavailable() {
		return []recommendations.ScoredItem{}, nil
	}
	if limit <= 0 {
		limit = recommendationsDefaultLimit
	}
	items, err := h.engine.BecauseYouWatched(ctx, userID, profileID, itemID, limit)
	if err != nil {
		return nil, recommendationsUnavailable("Failed to fetch recommendations")
	}
	items = h.filterRecommendations(ctx, userID, profileID, items)
	if items == nil {
		items = []recommendations.ScoredItem{}
	}
	return h.excludeWatchedRecommendations(ctx, userID, profileID, items), nil
}

// ForYouMain answers the profile's main For You row; nil when the reader is
// not wired or has nothing cached yet.
func (h *RecommendationsHandler) ForYouMain(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (*recommendations.ForYouRow, error) {
	if h.reader == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = recommendationsDefaultLimit
	}
	row, err := h.reader.GetForYouMain(ctx, userID, profileID, limit, filter)
	if err != nil {
		slog.ErrorContext(ctx, "ForYouMain failed", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		return nil, recommendationsUnavailable("Failed to fetch recommendations")
	}
	return row, nil
}

// ForYouRows answers the profile's For You rows; empty, never nil.
func (h *RecommendationsHandler) ForYouRows(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]recommendations.ForYouRow, error) {
	if h.reader == nil {
		return []recommendations.ForYouRow{}, nil
	}
	if limit <= 0 {
		limit = recommendationsDefaultLimit
	}
	rows, err := h.reader.GetForYouRows(ctx, userID, profileID, limit, filter)
	if err != nil {
		slog.ErrorContext(ctx, "ForYouRows failed", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		return nil, recommendationsUnavailable("Failed to fetch recommendations")
	}
	if rows == nil {
		rows = []recommendations.ForYouRow{}
	}
	return rows, nil
}

// PopularItems answers the server-wide popular items of the last days,
// minus what the profile has watched.
func (h *RecommendationsHandler) PopularItems(ctx context.Context, userID int, profileID string, days, limit int) ([]recommendations.ScoredItem, error) {
	items, err := h.recsRepo.GetPopularItems(ctx, days, limit)
	if err != nil {
		return nil, recommendationsUnavailable("Failed to fetch popular items")
	}
	if items == nil {
		items = []recommendations.ScoredItem{}
	}
	return h.excludeWatchedRecommendations(ctx, userID, profileID, items), nil
}

// RecentlyAddedItems answers the items added in the last days, minus what
// the profile has watched.
func (h *RecommendationsHandler) RecentlyAddedItems(ctx context.Context, userID int, profileID string, days, limit int) ([]recommendations.ScoredItem, error) {
	items, err := h.recsRepo.GetRecentlyAddedItems(ctx, days, limit)
	if err != nil {
		return nil, recommendationsUnavailable("Failed to fetch recently added items")
	}
	if items == nil {
		items = []recommendations.ScoredItem{}
	}
	return h.excludeWatchedRecommendations(ctx, userID, profileID, items), nil
}

// Discover answers the discover page: the cached rows blended with upcoming
// airings and rendered as cards. Rows with no visible card are dropped.
func (h *RecommendationsHandler) Discover(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter) (DiscoverView, error) {
	if h.reader == nil || h.Fetcher == nil {
		return discoverResponse{Rows: []discoverRowResponse{}}, nil
	}
	rows, err := h.reader.GetDiscoverRows(ctx, userID, profileID, recommendationsDefaultLimit, filter)
	if err != nil {
		slog.ErrorContext(ctx, "Discover failed", "component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		return discoverResponse{}, recommendationsUnavailable("Failed to fetch recommendations")
	}

	discoverRows := discoverRowModelsFromRecommendations(rows)
	discoverRows, upcomingErr := h.blendUpcomingIntoDiscoverRows(ctx, userID, profileID, filter, discoverRows, rows)
	if upcomingErr != nil {
		slog.WarnContext(ctx,
			"Discover: schedule-aware blending unavailable; falling back to legacy rows", "component", "api",
			"user_id", userID,
			"profile_id", profileID,
			"error", upcomingErr,
		)
	}
	if len(discoverRows) == 0 {
		return discoverResponse{Rows: []discoverRowResponse{}}, nil
	}
	rendered, err := h.renderDiscoverRows(ctx, userID, profileID, filter, discoverRows)
	if err != nil {
		return discoverResponse{}, err
	}
	return discoverResponse{Rows: rendered}, nil
}

// Section answers one recommendation row in full. An unknown or empty
// section is an empty row carrying the kind and key asked for.
func (h *RecommendationsHandler) Section(ctx context.Context, userID int, profileID, kind, key string, limit int, filter catalog.AccessFilter) (SectionDetailView, error) {
	if h.reader == nil || h.Fetcher == nil {
		return sectionDetailResponse{}, apiError(http.StatusNotFound, "not_found", "Section not found")
	}
	if limit <= 0 || limit > recommendations.CacheCandidateLimit {
		limit = recommendations.CacheCandidateLimit
	}
	row, err := h.reader.GetSection(ctx, userID, profileID, kind, key, limit, filter)
	if err != nil {
		slog.ErrorContext(ctx,
			"Section failed", "component", "api",
			"user_id", userID,
			"profile_id", profileID,
			"kind", kind,
			"key", key,
			"error", err,
		)
		return sectionDetailResponse{}, recommendationsUnavailable("Failed to fetch section")
	}
	if row == nil {
		return sectionDetailResponse{Kind: kind, Key: key, Items: []sectionItemResponse{}}, nil
	}
	rendered, err := h.renderDiscoverRows(ctx, userID, profileID, filter, []discoverRowModel{{Type: row.Type, Label: row.Label, Items: row.Items}})
	if err != nil {
		return sectionDetailResponse{}, err
	}
	items := []sectionItemResponse{}
	if len(rendered) > 0 {
		items = rendered[0].Items
	}
	return sectionDetailResponse{Kind: kind, Key: key, Type: row.Type, Label: row.Label, Items: items}, nil
}

// renderDiscoverRows loads every row's items once and renders each row's
// cards; a row with no visible card is dropped.
func (h *RecommendationsHandler) renderDiscoverRows(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter, discoverRows []discoverRowModel) ([]discoverRowResponse, error) {
	seen := make(map[string]struct{})
	var allIDs []string
	for _, row := range discoverRows {
		for _, item := range row.Items {
			if _, ok := seen[item.MediaItemID]; ok {
				continue
			}
			seen[item.MediaItemID] = struct{}{}
			allIDs = append(allIDs, item.MediaItemID)
		}
	}
	enrichment, err := h.loadItemEnrichment(ctx, userID, profileID, filter, allIDs)
	if err != nil {
		return nil, recommendationsUnavailable("Failed to fetch item details")
	}
	out := make([]discoverRowResponse, 0, len(discoverRows))
	for _, row := range discoverRows {
		kind, key := discoverRowSectionKey(row.Type, row.Label, row.ClusterIndex)
		respRow := discoverRowResponse{
			Type:        row.Type,
			Label:       row.Label,
			SectionKind: kind,
			SectionKey:  key,
			Items:       h.buildSectionItems(ctx, row.Items, enrichment, row.UpcomingEvents),
		}
		if len(respRow.Items) > 0 {
			out = append(out, respRow)
		}
	}
	return out, nil
}

// cardsOf renders scored identifiers as cards; an identifier the viewer may
// not see, or that no longer exists, is dropped.
func (h *RecommendationsHandler) cardsOf(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter, items []recommendations.ScoredItem) ([]SectionItemView, error) {
	rendered, err := h.renderDiscoverRows(ctx, userID, profileID, filter, []discoverRowModel{{Items: items}})
	if err != nil {
		return nil, err
	}
	if len(rendered) == 0 {
		return []SectionItemView{}, nil
	}
	return rendered[0].Items, nil
}

// BecauseWatchedCards is BecauseWatched rendered as cards.
func (h *RecommendationsHandler) BecauseWatchedCards(ctx context.Context, userID int, profileID, itemID string, limit int, filter catalog.AccessFilter) ([]SectionItemView, error) {
	items, err := h.BecauseWatched(ctx, userID, profileID, itemID, limit)
	if err != nil {
		return nil, err
	}
	return h.cardsOf(ctx, userID, profileID, filter, items)
}

// PopularCards is PopularItems rendered as cards.
func (h *RecommendationsHandler) PopularCards(ctx context.Context, userID int, profileID string, days, limit int, filter catalog.AccessFilter) ([]SectionItemView, error) {
	items, err := h.PopularItems(ctx, userID, profileID, days, limit)
	if err != nil {
		return nil, err
	}
	return h.cardsOf(ctx, userID, profileID, filter, items)
}

// RecentlyAddedCards is RecentlyAddedItems rendered as cards.
func (h *RecommendationsHandler) RecentlyAddedCards(ctx context.Context, userID int, profileID string, days, limit int, filter catalog.AccessFilter) ([]SectionItemView, error) {
	items, err := h.RecentlyAddedItems(ctx, userID, profileID, days, limit)
	if err != nil {
		return nil, err
	}
	return h.cardsOf(ctx, userID, profileID, filter, items)
}

// ForYouMainCards is ForYouMain rendered as a row of cards. A profile with
// no For You row yet gets the row empty rather than absent.
func (h *RecommendationsHandler) ForYouMainCards(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (DiscoverRowView, error) {
	empty := discoverRowResponse{Type: discoverClusterRowType, Label: discoverForYouLabel, SectionKind: recommendations.SectionKindForYouMain, Items: []sectionItemResponse{}}
	row, err := h.ForYouMain(ctx, userID, profileID, limit, filter)
	if err != nil {
		return discoverRowResponse{}, err
	}
	if row == nil {
		return empty, nil
	}
	rendered, err := h.renderDiscoverRows(ctx, userID, profileID, filter, discoverRowModelsFromRecommendations([]recommendations.ForYouRow{*row}))
	if err != nil {
		return discoverRowResponse{}, err
	}
	if len(rendered) == 0 {
		return empty, nil
	}
	return rendered[0], nil
}

// ForYouRowCards is ForYouRows rendered as rows of cards; rows with no
// visible card are dropped.
func (h *RecommendationsHandler) ForYouRowCards(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]DiscoverRowView, error) {
	rows, err := h.ForYouRows(ctx, userID, profileID, limit, filter)
	if err != nil {
		return nil, err
	}
	return h.renderDiscoverRows(ctx, userID, profileID, filter, discoverRowModelsFromRecommendations(rows))
}
