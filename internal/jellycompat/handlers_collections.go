package jellycompat

import (
	"context"
	"errors"
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

// visibleLibraryIDSet returns the set of library IDs the session may see on
// the compat surface (access-filtered and ABS-library-excluded by
// ListUserLibraries).
func visibleLibraryIDSet(ctx context.Context, content ContentService, session *Session) (map[int]struct{}, error) {
	libraries, err := content.ListUserLibraries(ctx, session)
	if err != nil {
		return nil, err
	}
	visible := make(map[int]struct{}, len(libraries))
	for _, lib := range libraries {
		visible[lib.ID] = struct{}{}
	}
	return visible, nil
}

func (h *ItemsHandler) visibleLibraryIDs(ctx context.Context, session *Session) (map[int]struct{}, error) {
	return visibleLibraryIDSet(ctx, h.content, session)
}

// collectionVisible reports whether any of the collection's libraries is
// visible to the session. Collections scoped only to hidden or ABS-surface
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

// loadVisibleCollection fetches a collection and applies the compat
// visibility rules. Returns (nil, nil) when the collection does not exist or
// the session may not see it; infrastructure errors propagate so transient
// failures don't masquerade as 404s.
func (h *ItemsHandler) loadVisibleCollection(ctx context.Context, session *Session, collectionID string) (*models.LibraryCollection, error) {
	if h.collections == nil {
		return nil, nil
	}
	collection, err := h.collections.GetByID(ctx, collectionID)
	if err != nil {
		if errors.Is(err, catalog.ErrLibraryCollectionNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if collection == nil || !strings.EqualFold(collection.Visibility, "visible") {
		return nil, nil
	}
	visible, err := h.visibleLibraryIDs(ctx, session)
	if err != nil {
		return nil, err
	}
	if !collectionVisible(collection, visible) {
		return nil, nil
	}
	return collection, nil
}

// boxSetFromCollection maps a library collection to a Jellyfin BoxSet DTO.
// Image tags are signed from the stable artwork key (like library views) so
// they survive restarts and presign rotation; the in-memory image cache is
// seeded as the warm path.
func (h *ItemsHandler) boxSetFromCollection(ctx context.Context, c *models.LibraryCollection) baseItemDTO {
	routeID := h.codec.EncodeStringID(EncodedIDCollection, c.ID)
	imgTags := map[string]string{}
	if posterURL := h.presignCollectionPoster(ctx, c.PosterURL); posterURL != "" {
		if h.images != nil {
			h.images.RememberSized(routeID, "Primary", posterURL, compatCardImageSize)
		}
		imgTags["Primary"] = h.mapper.imageTagSigner.Tag(
			imageTagSeed(routeID, "Primary", compatCardImageSize, c.PosterURL, "", time.Time{}),
			posterURL,
		)
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
		dto.BackdropImageTags = []string{h.mapper.imageTagSigner.Tag(
			imageTagSeed(routeID, "Backdrop", compatCardImageSize, c.BackdropURL, "", time.Time{}),
			backdropURL,
		)}
	}
	return dto
}

// presignCollectionPoster resolves a collection artwork reference to a
// fetchable URL. Collection posters are stored as S3 keys in the
// general-purpose bucket (same bucket as library posters); absolute and
// app-relative references pass through untouched (matching the main API's
// presignGPURL semantics).
func (h *ItemsHandler) presignCollectionPoster(ctx context.Context, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "/") {
		return path
	}
	if h.posterPresigner == nil {
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

// boxSetsByIDs maps the given collection IDs to BoxSet DTOs, skipping any the
// session may not see. Used by /Items?Ids= re-hydration.
func (h *ItemsHandler) boxSetsByIDs(ctx context.Context, session *Session, collectionIDs []string) ([]baseItemDTO, error) {
	if len(collectionIDs) == 0 || h.collections == nil {
		return nil, nil
	}
	items := make([]baseItemDTO, 0, len(collectionIDs))
	for _, id := range collectionIDs {
		collection, err := h.loadVisibleCollection(ctx, session, id)
		if err != nil {
			return nil, err
		}
		if collection == nil {
			continue
		}
		items = append(items, h.boxSetFromCollection(ctx, collection))
	}
	return items, nil
}

// handleBoxSetsList serves GET /Items with IncludeItemTypes=BoxSet by listing
// visible library collections, optionally scoped to one library via ParentId.
// Filtering, sorting, and paging happen on the lightweight collection rows;
// DTOs (with artwork presigning) are built only for the returned page.
func (h *ItemsHandler) handleBoxSetsList(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	if h.collections == nil {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
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
			writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
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
	namePrefix := strings.ToLower(query.namePrefix)
	matched := make([]*models.LibraryCollection, 0, len(collections))
	for _, c := range collections {
		if !collectionVisible(c, visible) {
			continue
		}
		title := strings.ToLower(c.Title)
		if searchTerm != "" && !strings.Contains(title, searchTerm) {
			continue
		}
		if namePrefix != "" && !strings.HasPrefix(title, namePrefix) {
			continue
		}
		matched = append(matched, c)
	}

	if query.sort == "sort_title" {
		ascending := query.order != "desc"
		sort.SliceStable(matched, func(i, j int) bool {
			a, b := strings.ToLower(matched[i].Title), strings.ToLower(matched[j].Title)
			if ascending {
				return a < b
			}
			return a > b
		})
	}

	page := slicePage(matched, query.startIndex, query.limit)
	items := make([]baseItemDTO, 0, len(page))
	for _, c := range page {
		items = append(items, h.boxSetFromCollection(r.Context(), c))
	}
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(matched),
		StartIndex:       query.startIndex,
	})
}

// slicePage returns the [startIndex, startIndex+limit) window of items;
// limit <= 0 means no cap.
func slicePage[T any](items []T, startIndex, limit int) []T {
	if startIndex < 0 {
		startIndex = 0
	}
	if startIndex >= len(items) {
		return nil
	}
	if limit <= 0 {
		limit = len(items)
	}
	end := min(startIndex+limit, len(items))
	return items[startIndex:end]
}

// handleBoxSetItem serves GET /Items/{id} when the ID decodes as a collection.
func (h *ItemsHandler) handleBoxSetItem(w http.ResponseWriter, r *http.Request, session *Session, collectionID string) {
	collection, err := h.loadVisibleCollection(r.Context(), session, collectionID)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if collection == nil {
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
	collection, err := h.loadVisibleCollection(r.Context(), session, query.parentCollectionID)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if collection == nil {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
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
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
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
		h.writeCollectionItemsPage(w, r, session, query, routeID, result.Items, result.Total)
		return
	}

	// Position order: hydrate the surviving members (collections are capped
	// well below the browse limit), rebuild curated order, then page locally
	// before building DTOs.
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
	page := slicePage(ordered, query.startIndex, query.limit)
	h.writeCollectionItemsPage(w, r, session, query, routeID, page, len(ordered))
}

// writeCollectionItemsPage hydrates user state for one page of collection
// members and writes the /Items result with ParentId stamped on each child.
func (h *ItemsHandler) writeCollectionItemsPage(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery, routeID string, listItems []upstreamListItem, total int) {
	h.rememberListImages(listItems)
	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDsFromListItems(listItems))
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	items := make([]baseItemDTO, 0, len(listItems))
	for _, item := range listItems {
		dto := h.mapper.itemFromList(item, favorites[item.ContentID], progress[item.ContentID], query.requestedFields)
		dto.ParentID = routeID
		items = append(items, dto)
	}
	applyImageTypeLimit(items, query.imageTypeLimit)
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: total,
		StartIndex:       query.startIndex,
	})
}
