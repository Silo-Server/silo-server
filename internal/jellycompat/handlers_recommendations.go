package jellycompat

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

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

// RecommendationRowReader reads the recommendation rows cached for a profile,
// filtered for the viewer. *recommendations.Reader implements it.
type RecommendationRowReader interface {
	// GetForYouPage returns the recommendations-page rows, main row first.
	GetForYouPage(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]recommendations.ForYouRow, error)
	// GetBecauseYouWatchedRows returns Because You Watched rows, one per
	// anchor, the most recent first.
	GetBecauseYouWatchedRows(ctx context.Context, userID int, profileID string, maxRows, limit int, filter catalog.AccessFilter) ([]recommendations.ForYouRow, error)
}

// Jellyfin's recommendation types. Clients word a category's heading from its
// type and BaselineItemName: jellyfin-web shows "Because you watched {0}" and
// "Because you like {0}".
const (
	recommendationSimilarToRecentlyPlayed = "SimilarToRecentlyPlayed"
	recommendationSimilarToLikedItem      = "SimilarToLikedItem"
)

// compatBecauseWatchedRows is how many Because You Watched categories a
// response carries at most.
const compatBecauseWatchedRows = 2

// compatMaxCategories caps categoryLimit: the categories a response can hold
// are a profile's Because You Watched rows and taste clusters.
const compatMaxCategories = 20

// compatRecommendationExcludedTypes are the recommendable media types
// /Movies/Recommendations leaves out. Jellyfin answers it with movies only,
// each category built from a movie the user played or liked, and clients
// show it on a movie library's Suggestions tab.
var compatRecommendationExcludedTypes = []string{compatSeriesType}

// recommendationItemLoader loads catalog items with the viewer's access
// applied. *catalog.ItemRepository implements it.
type recommendationItemLoader interface {
	GetByIDsWithAccess(ctx context.Context, contentIDs []string, access catalog.AccessFilter) ([]*models.MediaItem, error)
}

// RecommendationsHandler serves the Jellyfin Movies/Recommendations endpoint
// from the same cached rows the native API reads, keeping only their movies
// and, for a library ParentId, only that library's. Only rows a Jellyfin
// heading describes truthfully are sent: Because You Watched rows anchored on
// a movie as SimilarToRecentlyPlayed with the anchor's title, then
// taste-cluster rows as SimilarToLikedItem with the cluster's genre label.
// The main For You row, Similar Users and the server-wide rows have no item
// or genre behind them and are left out.
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
	// The Because You Watched rows and the page rows each leave out what the
	// profile watched; read that once.
	r = r.WithContext(recommendations.WithWatchedSetMemo(r.Context()))

	if h.reader == nil || h.items == nil {
		writeJSON(w, http.StatusOK, []recommendationDTO{})
		return
	}

	q := newCaseInsensitiveQuery(r.URL.Query())

	// Both limits size allocations, so they are capped: a category cannot
	// hold more than a cached row's titles, and a profile has few rows a
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
			itemLimit = min(n, recommendations.CacheCandidateLimit)
		}
	}

	// The rows are read already filtered for the viewer and kept to movies,
	// so a restricted profile still gets full rows of movies it can see, and
	// a Because You Watched row anchored on a series finds no anchor and is
	// left out. They are read with headroom for the titles an earlier
	// category already shows.
	filter := catalog.AccessFilter{UserID: session.StreamAppUserID, ProfileID: session.ProfileID}
	if h.accessFilter != nil {
		filter = h.accessFilter(r.Context(), session.StreamAppUserID, session.ProfileID)
	}
	filter.ExcludedMediaTypes = append(slices.Clone(filter.ExcludedMediaTypes), compatRecommendationExcludedTypes...)
	// Clients pass the library they show suggestions for as ParentId. A
	// parent that is not a library the viewer can browse has nothing to
	// recommend.
	if raw := strings.TrimSpace(q.Get("ParentId")); raw != "" {
		libraryID, err := h.codec.DecodeIntID(EncodedIDLibrary, raw)
		if err != nil || libraryID <= 0 || !narrowAccessToLibrary(&filter, int(libraryID)) {
			writeJSON(w, http.StatusOK, []recommendationDTO{})
			return
		}
	}
	// Every anchor's row is read: one the response cannot show (a series, or a
	// title outside ParentId) gives way to the next. Each row is read deep
	// enough to fill its category after the categories before it took their
	// titles, up to a cached row's length.
	// (The package's own max takes a fallback, so it is not used here.)
	rowDepth := categoryLimit * itemLimit
	if rowDepth < 2*itemLimit {
		rowDepth = 2 * itemLimit
	}
	rowDepth = min(rowDepth, recommendations.CacheCandidateLimit)
	rows, err := h.categoryRows(r.Context(), session, recommendations.BecauseYouWatchedAnchors, rowDepth, filter)
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
	// The anchors are fetched with the items, under the same access.
	fetchIDs := slices.Clone(contentIDs)
	for _, row := range rows {
		if row.AnchorItemID != "" {
			fetchIDs = append(fetchIDs, row.AnchorItemID)
		}
	}

	// Batch fetch media items, applying viewer access in the same query so we
	// avoid a per-item EnsureAccessible fan-out (audit 2026-05-01 §3.3). The
	// rows were filtered already; this also applies the compat media-type
	// exclusions.
	mediaItems, err := h.items.GetByIDsWithAccess(r.Context(), fetchIDs, filter)
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

	// Build Jellyfin recommendation categories. A title appears in the first
	// category that has it.
	result := make([]recommendationDTO, 0, categoryLimit)
	shown := make(map[string]struct{})
	watchedCategories := 0
	for _, row := range rows {
		if len(result) >= categoryLimit {
			break
		}
		if row.AnchorItemID != "" && watchedCategories >= compatBecauseWatchedRows {
			continue
		}
		category, ok := recommendationCategory(row, itemsByID)
		if !ok {
			continue
		}

		items := make([]baseItemDTO, 0, itemLimit)
		for _, scored := range row.Items {
			listItem, ok := itemsByID[scored.MediaItemID]
			if !ok {
				continue
			}
			if _, dup := shown[scored.MediaItemID]; dup {
				continue
			}
			shown[scored.MediaItemID] = struct{}{}
			items = append(items, h.mapper.itemFromList(listItem, favorites[scored.MediaItemID], progress[scored.MediaItemID], nil))
			if len(items) >= itemLimit {
				break
			}
		}
		if len(items) == 0 {
			continue
		}
		if row.AnchorItemID != "" {
			watchedCategories++
		}
		category.Items = items
		result = append(result, category)
	}

	writeJSON(w, http.StatusOK, result)
}

// categoryRows returns the rows a Jellyfin category can describe truthfully,
// in Jellyfin's order: Because You Watched rows first, each naming its
// anchor, then the taste-cluster rows that carry a genre subject. It reads a
// Because You Watched row for every anchor, up to maxRows, since an anchor
// the response cannot show (a series, or a title outside ParentId) gives
// way to the next.
func (h *RecommendationsHandler) categoryRows(ctx context.Context, session *Session, maxRows, limit int, filter catalog.AccessFilter) ([]recommendations.ForYouRow, error) {
	watched, err := h.reader.GetBecauseYouWatchedRows(ctx, session.StreamAppUserID, session.ProfileID, maxRows, limit, filter)
	if err != nil {
		return nil, err
	}
	page, err := h.reader.GetForYouPage(ctx, session.StreamAppUserID, session.ProfileID, limit, filter)
	if err != nil {
		return nil, err
	}
	rows := make([]recommendations.ForYouRow, 0, len(watched)+len(page))
	for _, row := range watched {
		if row.AnchorItemID != "" {
			rows = append(rows, row)
		}
	}
	for _, row := range page {
		if row.Type == "cluster" && row.Subject != "" {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// recommendationCategory is the heading of the category row becomes: a
// Because You Watched row is SimilarToRecentlyPlayed with its anchor's title,
// a taste-cluster row SimilarToLikedItem with its genre label. It reports
// false for an anchor that was not loaded, one the viewer cannot see or the
// response does not show, so the row is left out rather than shown under a
// title the viewer may not know of or that is not a movie.
func recommendationCategory(row recommendations.ForYouRow, itemsByID map[string]upstreamListItem) (recommendationDTO, bool) {
	if row.AnchorItemID == "" {
		return recommendationDTO{
			RecommendationType: recommendationSimilarToLikedItem,
			BaselineItemName:   row.Subject,
			CategoryID:         deterministicCategoryID("cluster:" + strconv.Itoa(row.ClusterIndex)),
		}, true
	}
	anchor, visible := itemsByID[row.AnchorItemID]
	if !visible || anchor.Title == "" {
		return recommendationDTO{}, false
	}
	return recommendationDTO{
		RecommendationType: recommendationSimilarToRecentlyPlayed,
		BaselineItemName:   anchor.Title,
		CategoryID:         deterministicCategoryID("because_watched:" + row.AnchorItemID),
	}, true
}

// deterministicCategoryID generates a stable UUID for the recommendation
// category the name identifies.
func deterministicCategoryID(name string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}
