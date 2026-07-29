package jellycompat

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// recommendationDTO mirrors Jellyfin's RecommendationDto.
type recommendationDTO struct {
	Items              []baseItemDTO `json:"Items"`
	RecommendationType string        `json:"RecommendationType"`
	BaselineItemName   string        `json:"BaselineItemName,omitempty"`
	CategoryID         string        `json:"CategoryId"`
}

// RecommendationsHandler serves the Jellyfin Movies/Recommendations endpoint
// using the Silo recommendation engine.
type RecommendationsHandler struct {
	recommender  recommendations.Recommender
	itemRepo     *catalog.ItemRepository
	detailSvc    *catalog.DetailService
	content      ContentService
	userData     UserDataService
	codec        *ResourceIDCodec
	mapper       *mapper
	accessFilter AccessFilterResolver
}

// NewRecommendationsHandler creates a new compat recommendations handler.
func NewRecommendationsHandler(
	recommender recommendations.Recommender,
	itemRepo *catalog.ItemRepository,
	detailSvc *catalog.DetailService,
	content ContentService,
	userData UserDataService,
	codec *ResourceIDCodec,
	cfg *config.Config,
	accessFilter AccessFilterResolver,
) *RecommendationsHandler {
	return &RecommendationsHandler{
		recommender:  recommender,
		itemRepo:     itemRepo,
		detailSvc:    detailSvc,
		content:      content,
		userData:     userData,
		codec:        codec,
		mapper:       newMapper(codec, cfg),
		accessFilter: accessFilter,
	}
}

// HandleRecommendations serves GET /Movies/Recommendations.
func (h *RecommendationsHandler) HandleRecommendations(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	if h.recommender == nil || h.itemRepo == nil {
		writeJSON(w, http.StatusOK, []recommendationDTO{})
		return
	}

	q := newCaseInsensitiveQuery(r.URL.Query())

	categoryLimit := 5
	if v := q.Get("categoryLimit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			categoryLimit = n
		}
	}

	itemLimit := 8
	if v := q.Get("itemLimit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			itemLimit = n
		}
	}

	resp, err := h.recommender.ForYou(r.Context(), session.StreamAppUserID, session.ProfileID, itemLimit)
	if err != nil {
		writeJSON(w, http.StatusOK, []recommendationDTO{})
		return
	}
	if resp == nil || len(resp.Rows) == 0 {
		writeJSON(w, http.StatusOK, []recommendationDTO{})
		return
	}

	// Collect all unique item IDs for batch fetch.
	idSet := make(map[string]struct{})
	for _, row := range resp.Rows {
		for _, item := range row.Items {
			idSet[item.MediaItemID] = struct{}{}
		}
	}
	contentIDs := make([]string, 0, len(idSet))
	for id := range idSet {
		contentIDs = append(contentIDs, id)
	}

	// Batch fetch media items, applying viewer access in the same query so we
	// avoid a per-item EnsureAccessible fan-out (audit 2026-05-01 §3.3).
	filter := catalog.AccessFilter{}
	if h.accessFilter != nil {
		filter = h.accessFilter(r.Context(), session.StreamAppUserID, session.ProfileID)
	}
	mediaItems, err := h.itemRepo.GetByIDsWithAccess(r.Context(), contentIDs, filter)
	if err != nil {
		writeJSON(w, http.StatusOK, []recommendationDTO{})
		return
	}
	itemsByID := make(map[string]upstreamListItem, len(mediaItems))
	for _, mi := range mediaItems {
		itemsByID[mi.ContentID] = mediaItemToListItem(mi)
	}
	listItems := make([]upstreamListItem, 0, len(itemsByID))
	for _, item := range itemsByID {
		listItems = append(listItems, item)
	}
	fillListItemDurations(r.Context(), h.detailSvc, listItems)
	for _, item := range listItems {
		itemsByID[item.ContentID] = item
	}

	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDs)
	if err != nil {
		favorites = map[string]bool{}
		progress = map[string]*upstreamProgress{}
	}

	// Build Jellyfin recommendation categories.
	result := make([]recommendationDTO, 0, categoryLimit)
	for _, row := range resp.Rows {
		if len(result) >= categoryLimit {
			break
		}

		items := make([]baseItemDTO, 0, len(row.Items))
		for _, scored := range row.Items {
			listItem, ok := itemsByID[scored.MediaItemID]
			if !ok {
				continue
			}
			items = append(items, h.mapper.itemFromList(listItem, favorites[scored.MediaItemID], progress[scored.MediaItemID], nil))
		}
		if len(items) == 0 {
			continue
		}

		result = append(result, recommendationDTO{
			Items:              items,
			RecommendationType: mapRecommendationType(row.Type),
			BaselineItemName:   row.Label,
			CategoryID:         deterministicCategoryID(row.Type, row.ClusterIndex),
		})
	}

	writeJSON(w, http.StatusOK, result)
}

// mapRecommendationType converts our row types to Jellyfin RecommendationType values.
func mapRecommendationType(rowType string) string {
	switch rowType {
	case "because_watched":
		return "SimilarToRecentlyPlayed"
	case "similar_users":
		return "SimilarToLikedItem"
	case "popular", "recently_added":
		return "SimilarToRecentlyPlayed"
	case "top_rated":
		return "SimilarToLikedItem"
	default:
		// for_you clusters and genre samplers
		return "SimilarToRecentlyPlayed"
	}
}

// deterministicCategoryID generates a stable UUID for a recommendation category.
func deterministicCategoryID(rowType string, clusterIdx int) string {
	name := rowType
	if clusterIdx > 0 {
		name += ":" + strconv.Itoa(clusterIdx)
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}
