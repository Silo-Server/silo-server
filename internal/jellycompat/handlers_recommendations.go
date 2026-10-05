package jellycompat

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// recommendationDTO mirrors Jellyfin's RecommendationDto.
type recommendationDTO struct {
	Items              []baseItemDTO `json:"Items"`
	RecommendationType string        `json:"RecommendationType"`
	BaselineItemName   string        `json:"BaselineItemName,omitempty"`
	CategoryID         string        `json:"CategoryId"`
}

// RecommendationRowReader reads the recommendations-page rows cached for a
// profile, main row first, filtered for the viewer.
// *recommendations.Reader implements it.
type RecommendationRowReader interface {
	GetForYouPage(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]recommendations.ForYouRow, error)
}

// recommendationItemLoader loads catalog items with the viewer's access
// applied. *catalog.ItemRepository implements it.
type recommendationItemLoader interface {
	GetByIDsWithAccess(ctx context.Context, contentIDs []string, access catalog.AccessFilter) ([]*models.MediaItem, error)
}

// compatMaxCategories caps categoryLimit: the categories a response can hold
// are a profile's Because You Watched rows and taste clusters.
const compatMaxCategories = 20

// RecommendationsHandler serves the Jellyfin Movies/Recommendations endpoint
// from the same cached rows the native API reads.
type RecommendationsHandler struct {
	reader       RecommendationRowReader
	items        recommendationItemLoader
	durations    probedDurationSource
	userData     UserDataService
	codec        *ResourceIDCodec
	mapper       *mapper
	accessFilter AccessFilterResolver
}

// NewRecommendationsHandler creates a new compat recommendations handler.
func NewRecommendationsHandler(
	reader RecommendationRowReader,
	itemRepo *catalog.ItemRepository,
	detailSvc *catalog.DetailService,
	userData UserDataService,
	codec *ResourceIDCodec,
	cfg *config.Config,
	accessFilter AccessFilterResolver,
) *RecommendationsHandler {
	h := &RecommendationsHandler{
		reader:       reader,
		userData:     userData,
		codec:        codec,
		mapper:       newMapper(codec, cfg),
		accessFilter: accessFilter,
	}
	if itemRepo != nil {
		h.items = itemRepo
	}
	if detailSvc != nil {
		h.durations = detailSvc
	}
	return h
}

// HandleRecommendations serves GET /Movies/Recommendations.
func (h *RecommendationsHandler) HandleRecommendations(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	if h.reader == nil || h.items == nil {
		writeJSON(w, http.StatusOK, []recommendationDTO{})
		return
	}

	q := newCaseInsensitiveQuery(r.URL.Query())

	// Both limits size allocations, so they are capped: a category cannot
	// hold more titles than a row read returns, and a profile has few rows a
	// category can describe.
	categoryLimit := 5
	if v := q.Get("categoryLimit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			categoryLimit = min(n, compatMaxCategories)
		}
	}

	itemLimit := 8
	if v := q.Get("itemLimit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			itemLimit = min(n, recommendations.MaxRowReadLimit)
		}
	}

	// The rows are read already filtered for the viewer, including the compat
	// media-type exclusions, so a restricted profile still gets full rows of
	// titles it can see. They are read with headroom for titles the item
	// load below can still drop.
	filter := catalog.AccessFilter{UserID: session.StreamAppUserID, ProfileID: session.ProfileID}
	if h.accessFilter != nil {
		filter = h.accessFilter(r.Context(), session.StreamAppUserID, session.ProfileID)
	}
	rows, err := h.reader.GetForYouPage(r.Context(), session.StreamAppUserID, session.ProfileID, min(2*itemLimit, recommendations.MaxRowReadLimit), filter)
	if err != nil {
		slog.WarnContext(r.Context(), "jellycompat: recommendation rows failed", "component", "jellycompat",
			"user_id", session.StreamAppUserID, "profile_id", session.ProfileID, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Failed to load recommendations")
		return
	}
	if len(rows) == 0 {
		writeJSON(w, http.StatusOK, []recommendationDTO{})
		return
	}

	// Collect all unique item IDs for batch fetch.
	idSet := make(map[string]struct{})
	for _, row := range rows {
		for _, item := range row.Items {
			idSet[item.MediaItemID] = struct{}{}
		}
	}
	contentIDs := make([]string, 0, len(idSet))
	for id := range idSet {
		contentIDs = append(contentIDs, id)
	}

	// Batch fetch media items, applying viewer access in the same query so we
	// avoid a per-item EnsureAccessible fan-out (audit 2026-05-01 §3.3). The
	// rows were filtered already; this also applies the compat media-type
	// exclusions.
	mediaItems, err := h.items.GetByIDsWithAccess(r.Context(), contentIDs, filter)
	if err != nil {
		slog.WarnContext(r.Context(), "jellycompat: recommendation items failed", "component", "jellycompat",
			"user_id", session.StreamAppUserID, "profile_id", session.ProfileID, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Failed to load recommendations")
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
	fillListItemDurations(r.Context(), h.durations, listItems)
	for _, item := range listItems {
		itemsByID[item.ContentID] = item
	}

	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDs)
	if err != nil {
		slog.WarnContext(r.Context(), "jellycompat: recommendation user state failed; serving without it", "component", "jellycompat",
			"user_id", session.StreamAppUserID, "profile_id", session.ProfileID, "error", err)
		favorites = map[string]bool{}
		progress = map[string]*upstreamProgress{}
	}

	// Build Jellyfin recommendation categories.
	result := make([]recommendationDTO, 0, categoryLimit)
	for _, row := range rows {
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
			if len(items) >= itemLimit {
				break
			}
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
