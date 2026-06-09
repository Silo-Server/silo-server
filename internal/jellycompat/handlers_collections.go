package jellycompat

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// collectionSource is the subset of *catalog.LibraryCollectionRepository the
// compat layer relies on to expose library collections as Jellyfin BoxSets.
type collectionSource interface {
	ListAll(ctx context.Context, libraryID *int, opts catalog.ListLibraryCollectionsOptions) ([]*models.LibraryCollection, error)
	GetByID(ctx context.Context, id string) (*models.LibraryCollection, error)
	ListItems(ctx context.Context, collectionID string) ([]*models.LibraryCollectionItem, error)
}

// visibleLibraryIDs returns the set of library IDs the session may see on the
// compat surface (access-filtered and audiobook-excluded by ListUserLibraries).
func (h *ItemsHandler) visibleLibraryIDs(ctx context.Context, session *Session) (map[int]struct{}, error) {
	libraries, err := h.content.ListUserLibraries(ctx, session)
	if err != nil {
		return nil, err
	}
	visible := make(map[int]struct{}, len(libraries))
	for _, lib := range libraries {
		visible[lib.ID] = struct{}{}
	}
	return visible, nil
}

// collectionVisible reports whether any of the collection's libraries is
// visible to the session. Collections scoped only to hidden or audiobook
// libraries stay off the compat surface.
func collectionVisible(c *models.LibraryCollection, visible map[int]struct{}) bool {
	if len(c.LibraryIDs) == 0 {
		_, ok := visible[c.LibraryID]
		return ok
	}
	for _, id := range c.LibraryIDs {
		if _, ok := visible[id]; ok {
			return true
		}
	}
	return false
}

// boxSetFromCollection maps a library collection to a Jellyfin BoxSet DTO and
// seeds the image cache so /Items/{id}/Images/Primary can serve the poster.
func (h *ItemsHandler) boxSetFromCollection(ctx context.Context, c *models.LibraryCollection) baseItemDTO {
	routeID := h.codec.EncodeStringID(EncodedIDCollection, c.ID)
	imgTags := map[string]string{}
	if posterURL := h.presignCollectionPoster(ctx, c.PosterURL); posterURL != "" {
		if h.images != nil {
			h.images.RememberSized(routeID, "Primary", posterURL, compatCardImageSize)
		}
		imgTags["Primary"] = tagValue(posterURL)
	}
	dto := baseItemDTO{
		ID:                 routeID,
		Type:               "BoxSet",
		IsFolder:           true,
		Name:               c.Title,
		ServerID:           h.mapper.serverID,
		Overview:           c.Description,
		SortName:           strings.ToLower(c.Title),
		ChildCount:         c.ItemCount,
		RecursiveItemCount: c.ItemCount,
		ImageTags:          imgTags,
		UserData: &itemUserDataDTO{
			Key:    routeID,
			ItemID: routeID,
		},
	}
	if backdropURL := h.presignCollectionPoster(ctx, c.BackdropURL); backdropURL != "" {
		if h.images != nil {
			h.images.RememberSized(routeID, "Backdrop", backdropURL, compatCardImageSize)
		}
		dto.BackdropImageTags = []string{tagValue(backdropURL)}
	}
	return dto
}

// presignCollectionPoster resolves a collection artwork reference to a
// fetchable URL. Collection posters are stored as S3 keys in the
// general-purpose bucket (same bucket as library posters); absolute and
// app-relative references pass through untouched.
func (h *ItemsHandler) presignCollectionPoster(ctx context.Context, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if h.posterPresigner == nil || strings.HasPrefix(path, "/") {
		return ""
	}
	ttl := h.presignTTL
	if ttl <= 0 {
		ttl = 4 * time.Hour
	}
	url, err := h.posterPresigner.PresignGetURL(ctx, h.posterPresigner.Bucket(), path, ttl)
	if err != nil {
		return ""
	}
	return url
}

// handleBoxSetsList serves GET /Items with IncludeItemTypes=BoxSet by listing
// visible library collections, optionally scoped to one library via ParentId.
func (h *ItemsHandler) handleBoxSetsList(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	empty := queryResultDTO{Items: []baseItemDTO{}, TotalRecordCount: 0, StartIndex: query.startIndex}
	if h.collections == nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	visible, err := h.visibleLibraryIDs(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	var libFilter *int
	if query.parentLibraryID > 0 {
		if _, ok := visible[query.parentLibraryID]; !ok {
			writeJSON(w, http.StatusOK, empty)
			return
		}
		libFilter = &query.parentLibraryID
	}

	collections, err := h.collections.ListAll(r.Context(), libFilter, catalog.ListLibraryCollectionsOptions{})
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	searchTerm := strings.ToLower(strings.TrimSpace(query.searchTerm))
	items := make([]baseItemDTO, 0, len(collections))
	for _, c := range collections {
		if !collectionVisible(c, visible) {
			continue
		}
		if searchTerm != "" && !strings.Contains(strings.ToLower(c.Title), searchTerm) {
			continue
		}
		if query.namePrefix != "" && !strings.HasPrefix(strings.ToLower(c.Title), strings.ToLower(query.namePrefix)) {
			continue
		}
		items = append(items, h.boxSetFromCollection(r.Context(), c))
	}

	if query.sort == "sort_title" {
		ascending := query.order != "desc"
		sortBaseItemsByName(items, ascending)
	}

	total := len(items)
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            sliceBaseItems(items, query.startIndex, query.limit),
		TotalRecordCount: total,
		StartIndex:       query.startIndex,
	})
}

func sortBaseItemsByName(items []baseItemDTO, ascending bool) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := strings.ToLower(items[i].Name), strings.ToLower(items[j].Name)
		if ascending {
			return a < b
		}
		return a > b
	})
}

// handleBoxSetItem serves GET /Items/{id} when the ID decodes as a collection.
func (h *ItemsHandler) handleBoxSetItem(w http.ResponseWriter, r *http.Request, session *Session, collectionID string) {
	if h.collections == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	collection, err := h.collections.GetByID(r.Context(), collectionID)
	if err != nil || collection == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	visible, err := h.visibleLibraryIDs(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if !collectionVisible(collection, visible) || !strings.EqualFold(collection.Visibility, "visible") {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	writeJSON(w, http.StatusOK, h.boxSetFromCollection(r.Context(), collection))
}

// handleBoxSetChildren serves GET /Items?ParentId={boxsetId} by hydrating the
// collection's members. Without an explicit SortBy the curated collection
// position order is preserved; an explicit SortBy delegates ordering and
// paging to the catalog browse path.
func (h *ItemsHandler) handleBoxSetChildren(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	empty := queryResultDTO{Items: []baseItemDTO{}, TotalRecordCount: 0, StartIndex: query.startIndex}
	if h.collections == nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	collection, err := h.collections.GetByID(r.Context(), query.parentCollectionID)
	if err != nil || collection == nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	visible, err := h.visibleLibraryIDs(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if !collectionVisible(collection, visible) || !strings.EqualFold(collection.Visibility, "visible") {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	members, err := h.collections.ListItems(r.Context(), collection.ID)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	contentIDs := make([]string, 0, len(members))
	for _, member := range members {
		contentIDs = append(contentIDs, member.MediaItemID)
	}
	if len(contentIDs) == 0 {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	routeID := h.codec.EncodeStringID(EncodedIDCollection, collection.ID)

	if query.sortExplicit {
		// Catalog handles ordering and paging; the member list acts as an
		// access-filtered allowlist.
		params := buildBrowseParams(query)
		params.Set("content_ids", strings.Join(contentIDs, ","))
		result, browseErr := h.content.BrowseItems(r.Context(), session, params)
		if browseErr != nil {
			writeCompatUpstreamError(w, browseErr)
			return
		}
		h.rememberListImages(result.Items)
		favorites, progress, stateErr := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDsFromListItems(result.Items))
		if stateErr != nil {
			writeCompatUpstreamError(w, stateErr)
			return
		}
		items := make([]baseItemDTO, 0, len(result.Items))
		for _, item := range result.Items {
			dto := h.mapper.itemFromList(item, favorites[item.ContentID], progress[item.ContentID], query.requestedFields)
			dto.ParentID = routeID
			items = append(items, dto)
		}
		applyImageTypeLimit(items, query.imageTypeLimit)
		writeJSON(w, http.StatusOK, queryResultDTO{
			Items:            items,
			TotalRecordCount: result.Total,
			StartIndex:       query.startIndex,
		})
		return
	}

	// Position order: hydrate every member (collections are capped well below
	// the browse limit), rebuild curated order, then page locally.
	itemsByID, err := h.fetchCompatItemsByContentIDs(r.Context(), session, contentIDs, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	ordered := make([]upstreamListItem, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		if item, ok := itemsByID[contentID]; ok {
			ordered = append(ordered, item)
		}
	}
	h.rememberListImages(ordered)
	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDsFromListItems(ordered))
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	items := make([]baseItemDTO, 0, len(ordered))
	for _, item := range ordered {
		dto := h.mapper.itemFromList(item, favorites[item.ContentID], progress[item.ContentID], query.requestedFields)
		dto.ParentID = routeID
		items = append(items, dto)
	}
	applyImageTypeLimit(items, query.imageTypeLimit)
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            sliceBaseItems(items, query.startIndex, query.limit),
		TotalRecordCount: len(items),
		StartIndex:       query.startIndex,
	})
}
