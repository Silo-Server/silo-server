package jellycompat

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// collectionSource is the subset of *catalog.LibraryCollectionRepository the
// compat layer relies on to expose library collections as Jellyfin BoxSets.
type collectionSource interface {
	ListAll(ctx context.Context, libraryID *int, opts catalog.ListLibraryCollectionsOptions) ([]*models.LibraryCollection, error)
	GetByID(ctx context.Context, id string) (*models.LibraryCollection, error)
	ListItems(ctx context.Context, collectionID string) ([]*models.LibraryCollectionItem, error)
	ListContainingItem(ctx context.Context, mediaItemID string) ([]*models.LibraryCollection, error)
	AnyVisibleInLibraries(ctx context.Context, libraryIDs []int) (bool, error)
}

// CollectionPosterResolver picks the poster a viewer sees for each server
// collection (*catalog.LibraryCollectionService). A collection without an
// uploaded or template poster shows a collage of the members that viewer can
// access, so its poster differs by viewer.
type CollectionPosterResolver interface {
	CollectionPosters(ctx context.Context, collections []*models.LibraryCollection, access catalog.AccessFilter) map[string]catalog.CollectionPoster
	CollectionCollage(ctx context.Context, collectionID, key string) (catalog.CollectionPoster, bool, error)
}

// viewerCollectionPosters returns each collection's poster for the session's
// viewer, keyed by collection ID. Without a resolver or the viewer's access
// only uploaded and template posters are returned, never another viewer's
// collage.
func viewerCollectionPosters(ctx context.Context, resolver CollectionPosterResolver, access func() catalog.AccessFilter, collections []*models.LibraryCollection) map[string]catalog.CollectionPoster {
	if resolver != nil && access != nil {
		return resolver.CollectionPosters(ctx, collections, access())
	}
	posters := make(map[string]catalog.CollectionPoster, len(collections))
	for _, c := range collections {
		if poster, ok := catalog.AssignedCollectionPoster(c); ok {
			posters[c.ID] = poster
		}
	}
	return posters
}

// collectionsViewID is the canonical Jellyfin "Collections" (boxsets)
// CollectionFolder GUID. It is stable across all Jellyfin servers, so clients
// recognise it as the box-set library; Silo reuses the same constant rather
// than minting a per-server ID. Emitted in the compact 32-char form Jellyfin
// uses for these views; isCollectionsViewID tolerates the dashed form clients
// may echo back as a ParentId.
const collectionsViewID = "9d7ad6afe9afa2dab1a2f6e00ad28fa6"

var collectionsViewUUID = uuid.MustParse(collectionsViewID)

// isCollectionsViewID reports whether raw refers to the synthetic Collections
// view, comparing parsed UUIDs so the compact and dashed forms both match.
func isCollectionsViewID(raw string) bool {
	if raw == "" {
		return false
	}
	parsed, err := uuid.Parse(raw)
	return err == nil && parsed == collectionsViewUUID
}

// idsRequestCollectionsView reports whether a raw Ids= param references the
// synthetic Collections view. The sentinel decodes to neither an item nor a
// collection, so parseItemsQuery drops it; this lets the /Items?Ids= path
// re-hydrate the CollectionFolder the same way clients re-hydrate libraries.
func idsRequestCollectionsView(r *http.Request) bool {
	for _, raw := range newCaseInsensitiveQuery(r.URL.Query()).Values("Ids") {
		for part := range strings.SplitSeq(raw, ",") {
			if isCollectionsViewID(strings.TrimSpace(part)) {
				return true
			}
		}
	}
	return false
}

// collectionsView builds the synthetic CollectionFolder that wraps the server's
// library collections, exposing them as a top-level Jellyfin library whose
// children are BoxSets (CollectionType "boxsets"). It holds no per-collection
// state and never touches the database; the empty-tab gate lives in
// collectionsViewVisible. ChildCount is intentionally left zero (omitempty):
// counting members would re-run the heavy ListAll on every /UserViews, and an
// unwatched badge would need per-user state across every collection member.
func (h *ItemsHandler) collectionsView() baseItemDTO {
	// Advertise a Primary image tag so clients fetch the generated "Collections"
	// gradient tile; the seed matches serveCollectionsViewImage.
	primaryTag := h.mapper.imageTagSigner.Tag(
		imageTagSeed(collectionsViewID, "Primary", compatCardImageSize, generatedPosterSeed(collectionsViewCaption), "", time.Time{}),
		generatedPosterSeed(collectionsViewCaption),
	)
	posterAspect := 2.0 / 3.0 // portrait tile; match the generated poster so clients don't square-crop
	return baseItemDTO{
		ID:                      collectionsViewID,
		Type:                    "CollectionFolder",
		CollectionType:          "boxsets",
		MediaType:               "Unknown",
		IsFolder:                true,
		Name:                    "Collections",
		ServerID:                h.mapper.serverID,
		SortName:                "collections",
		DisplayPreferencesID:    displayPreferencesID(collectionsViewID),
		PrimaryImageAspectRatio: &posterAspect,
		ImageTags:               map[string]string{"Primary": primaryTag},
		UserData: &itemUserDataDTO{
			Key:    collectionsViewID,
			ItemID: collectionsViewID,
		},
	}
}

// collectionsViewVisible reports whether the Collections view should appear in
// the session's library list. It is shown only when at least one collection is
// visible to the session, via an index-only EXISTS probe scoped to the
// libraries the session can already see. A probe error fails closed (no tab)
// rather than failing the whole /UserViews response.
func (h *ItemsHandler) collectionsViewVisible(ctx context.Context, libraries []upstreamUserLibrary) bool {
	if h.collections == nil {
		return false
	}
	ids := make([]int, 0, len(libraries))
	for _, lib := range libraries {
		ids = append(ids, lib.ID)
	}
	visible, err := h.collections.AnyVisibleInLibraries(ctx, ids)
	if err != nil {
		slog.DebugContext(ctx, "jellycompat collections view existence check failed", "component", "jellycompat", "error", err)
		return false
	}
	return visible
}

// smartCollectionQueryExecutor resolves a smart (live-query) collection's members
// at read time. Backed by *catalog.QueryExecutor in production; an interface so
// the BoxSet children path is unit-testable without a database.
type smartCollectionQueryExecutor interface {
	Preview(ctx context.Context, def catalog.QueryDefinition, access catalog.AccessFilter, limit int) ([]*models.MediaItem, int, error)
	PreviewPage(ctx context.Context, def catalog.QueryDefinition, access catalog.AccessFilter, limit, offset int, includeTotal bool) ([]*models.MediaItem, int, bool, error)
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

// boxSetsFromCollections maps collections to BoxSet DTOs carrying the posters
// the session's viewer sees.
func (h *ItemsHandler) boxSetsFromCollections(ctx context.Context, session *Session, collections []*models.LibraryCollection) []baseItemDTO {
	var access func() catalog.AccessFilter
	if session != nil && h.accessFilter != nil {
		access = func() catalog.AccessFilter { return h.resolveAccessFilter(ctx, session) }
	}
	posters := viewerCollectionPosters(ctx, h.collectionPosters, access, collections)
	items := make([]baseItemDTO, 0, len(collections))
	for _, c := range collections {
		items = append(items, h.boxSetFromCollection(ctx, c, posters[c.ID]))
	}
	return items
}

// boxSetFromCollection maps a library collection to a Jellyfin BoxSet DTO
// showing poster, the one its viewer sees. Image tags are signed from the
// stable artwork key (like library views) so they survive restarts and presign
// rotation. A collage's tag also names the collage, so an image request that
// carries only the tag can find it.
func (h *ItemsHandler) boxSetFromCollection(ctx context.Context, c *models.LibraryCollection, poster catalog.CollectionPoster) baseItemDTO {
	routeID := h.codec.EncodeStringID(EncodedIDCollection, c.ID)
	imgTags := map[string]string{}
	posterURL := h.presignCollectionPoster(ctx, poster.Path)
	switch {
	case posterURL != "" && poster.CollageKey != "":
		// Not seeded into the shared image cache: the collage is this viewer's.
		imgTags["Primary"] = collageImageTag(h.mapper.imageTagSigner, routeID, poster.CollageKey)
	case posterURL != "":
		if h.images != nil {
			h.images.RememberSized(routeID, "Primary", posterURL, compatCardImageSize)
		}
		imgTags["Primary"] = h.mapper.imageTagSigner.Tag(
			imageTagSeed(routeID, "Primary", compatCardImageSize, poster.Path, "", time.Time{}),
			posterURL,
		)
	default:
		// No stored poster: advertise a Primary tag anyway so clients request the
		// generated gradient fallback instead of showing a blank card. The seed
		// matches collectionImageTagSeed's generated branch.
		imgTags["Primary"] = h.mapper.imageTagSigner.Tag(
			imageTagSeed(routeID, "Primary", compatCardImageSize, generatedPosterSeed(c.Title), "", time.Time{}),
			generatedPosterSeed(c.Title),
		)
	}
	posterAspect := 2.0 / 3.0 // portrait poster; without it clients square-crop the card
	dto := baseItemDTO{
		ID:                      routeID,
		Type:                    "BoxSet",
		IsFolder:                true,
		Name:                    c.Title,
		ServerID:                h.mapper.serverID,
		Overview:                c.Description,
		SortName:                strings.ToLower(c.Title),
		ChildCount:              c.ItemCount,
		RecursiveItemCount:      c.ItemCount,
		ImageTags:               imgTags,
		PrimaryImageAspectRatio: &posterAspect,
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
	collections := make([]*models.LibraryCollection, 0, len(collectionIDs))
	for _, id := range collectionIDs {
		collection, err := h.loadVisibleCollection(ctx, session, id)
		if err != nil {
			return nil, err
		}
		if collection == nil {
			continue
		}
		collections = append(collections, collection)
	}
	return h.boxSetsFromCollections(ctx, session, collections), nil
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

	// Box-set/collection search is an in-memory filter over every collection
	// (not the Meilisearch-backed /Items media search), so short type-ahead
	// terms are gated before any rows are loaded.
	if auxSearchTermTooShort(query.searchTerm) {
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
		if query.nameLessThan != "" && title >= strings.ToLower(query.nameLessThan) {
			continue
		}
		if query.nameStartsWithOrGreater != "" && title < strings.ToLower(query.nameStartsWithOrGreater) {
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

	// A search term makes this a guarded aux search path, so cap results like
	// the other guarded handlers; an empty term is a browse/list request and
	// keeps the client-requested paging window.
	pageLimit := query.limit
	if strings.TrimSpace(query.searchTerm) != "" {
		pageLimit = clampAuxSearchLimit(query.limit)
	}
	page := slicePage(matched, query.startIndex, pageLimit)
	if query.countOnly {
		page = nil
	}
	items := h.boxSetsFromCollections(r.Context(), session, page)
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
		return []T{}
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
	writeJSON(w, http.StatusOK, h.boxSetsFromCollections(r.Context(), session, []*models.LibraryCollection{collection})[0])
}

// HandleItemCollections serves GET /Items/{id}/Collections (Jellyfin 12.0+,
// the "Included In" row on item details): the visible BoxSets that contain the
// item, ordered by name and paged by StartIndex/Limit. Visibility is checked
// before membership, so an item the viewer cannot see answers 404 whether or
// not it belongs to a collection. Only movies and series can be members; a
// visible episode, or a season, returns an empty result.
func (h *ItemsHandler) HandleItemCollections(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	q := newCaseInsensitiveQuery(r.URL.Query())
	if userID := q.Get("UserId"); userID != "" && !validatePseudoUser(w, userID, session) {
		return
	}
	startIndex := parsePositiveInt(q.Get("StartIndex"), 0)
	limit := parsePositiveInt(q.Get("Limit"), 0)

	rawID := chi.URLParam(r, "id")
	contentID, err := decodeItemID(h.codec, rawID)
	if err != nil {
		if _, seasonErr := h.codec.DecodeStringID(EncodedIDSeason, rawID); seasonErr == nil {
			writeJSON(w, http.StatusOK, emptyQueryResult(startIndex))
			return
		}
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	items, err := h.fetchCompatItemsByContentIDs(r.Context(), session, []string{contentID}, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if _, ok := items[contentID]; !ok {
		episodes, episodeErr := h.fetchCompatEpisodeTargetsByContentIDs(r.Context(), session, []string{contentID}, nil)
		if episodeErr != nil {
			writeCompatUpstreamError(w, episodeErr)
			return
		}
		if _, ok := episodes[contentID]; ok {
			writeJSON(w, http.StatusOK, emptyQueryResult(startIndex))
			return
		}
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	if h.collections == nil {
		writeJSON(w, http.StatusOK, emptyQueryResult(startIndex))
		return
	}

	containing, err := h.collections.ListContainingItem(r.Context(), contentID)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	visible, err := h.visibleLibraryIDs(r.Context(), session)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	matched := make([]*models.LibraryCollection, 0, len(containing))
	for _, c := range containing {
		if collectionVisible(c, visible) {
			matched = append(matched, c)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		a, b := strings.ToLower(matched[i].Title), strings.ToLower(matched[j].Title)
		if a != b {
			return a < b
		}
		return matched[i].Title < matched[j].Title
	})
	page := slicePage(matched, startIndex, limit)
	dtos := h.boxSetsFromCollections(r.Context(), session, page)
	applyItemsResponseOptions(dtos, parseItemsQuery(r, h.codec))
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            dtos,
		TotalRecordCount: len(matched),
		StartIndex:       startIndex,
	})
}

// handleBoxSetChildren serves GET /Items?ParentId={boxsetId} by hydrating the
// collection's members, or its playable leaves for Play all and Shuffle.
// Without an explicit SortBy the curated collection position order is
// preserved; an explicit SortBy delegates ordering and paging to the catalog
// browse path, except for episode-scoped smart collections, whose episodes
// catalog browse cannot see.
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
	leafType, membersAreLeaves := smartCollectionLeafType(collection)

	// Play all / Shuffle asks for the collection's playable leaves. A smart
	// collection scoped to movies or episodes already lists only leaves, so
	// unless the client shuffles it pages in SQL like a plain listing.
	if query.wantsCollectionLeaves() {
		if !membersAreLeaves || (query.sortExplicit && query.sort == compatBrowseRandomSort) {
			h.handleBoxSetLeaves(w, r, session, query, collection, leafType)
			return
		}
		if !query.allowsItemType(leafType) || !query.allowsVideo() {
			writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
			return
		}
		h.writeSmartCollectionPage(w, r, session, query, collection)
		return
	}

	// Smart (live-query) collections have no curated position order — their
	// members come straight from the query's own ordering, and their membership
	// is deliberately uncapped (an admin decision). Without an explicit client
	// sort we page that query directly in SQL rather than resolving the entire
	// membership and slicing one page locally, so per-request work stays
	// proportional to the page size instead of the collection size. The
	// explicit-sort case falls through to the browse allowlist path below, which
	// re-sorts the whole membership.
	if catalog.IsLiveQueryType(collection.CollectionType) && !query.sortExplicit && !query.hasItemTypeFilter && !query.hasMemberFilters() {
		h.writeSmartCollectionPage(w, r, session, query, collection)
		return
	}

	contentIDs, err := h.collectionMemberIDs(r.Context(), session, collection)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if len(contentIDs) == 0 {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
		return
	}

	routeID := h.codec.EncodeStringID(EncodedIDCollection, collection.ID)

	if query.sortExplicit || query.hasItemTypeFilter || query.hasMemberFilters() {
		// Catalog browse reads media_items only, so an episode-scoped smart
		// collection sorts its own members instead. Catalog and user-state
		// filters still need the browse path.
		if leafType == compatEpisodeType && !query.hasMemberFilters() {
			if !query.allowsItemType(leafType) || !query.allowsVideo() {
				writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
				return
			}
			sorted := contentIDs
			if query.sortExplicit {
				var sortErr error
				if sorted, sortErr = h.sortEpisodeMembers(r.Context(), contentIDs, query); sortErr != nil {
					writeCompatUpstreamError(w, sortErr)
					return
				}
			}
			ordered, episodeTargets, hydrateErr := h.hydrateCollectionMembers(r.Context(), session, slicePage(sorted, query.startIndex, query.limit), true)
			if hydrateErr != nil {
				writeCompatUpstreamError(w, hydrateErr)
				return
			}
			h.writeCollectionItemsPage(w, r, session, query, routeID, ordered, episodeTargets, len(sorted))
			return
		}

		// Catalog handles ordering and paging; the member list acts as an
		// access-filtered allowlist.
		params := buildBrowseParams(query)
		params.Set("content_ids", strings.Join(contentIDs, ","))
		result, browseErr := h.content.BrowseItems(r.Context(), session, params)
		if browseErr != nil {
			writeCompatUpstreamError(w, browseErr)
			return
		}
		h.writeCollectionItemsPage(w, r, session, query, routeID, result.Items, nil, result.Total)
		return
	}

	// Position order: hydrate the surviving members (collections are capped
	// well below the browse limit), rebuild curated order, then page locally
	// before building DTOs.
	ordered, episodeTargets, err := h.hydrateCollectionMembers(r.Context(), session, contentIDs, catalog.IsLiveQueryType(collection.CollectionType))
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	page := slicePage(ordered, query.startIndex, query.limit)
	h.writeCollectionItemsPage(w, r, session, query, routeID, page, episodeTargets, len(ordered))
}

// writeSmartCollectionPage pages a smart collection's query in SQL and writes
// that page of members.
func (h *ItemsHandler) writeSmartCollectionPage(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery, collection *models.LibraryCollection) {
	pageIDs, total, ok, err := h.smartCollectionContentIDPage(r.Context(), session, collection, query.startIndex, query.limit)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
		return
	}
	if len(pageIDs) == 0 {
		// Empty membership, or a page past the end: preserve the real total
		// so clients that paged beyond the last item still see the size.
		result := emptyQueryResult(query.startIndex)
		result.TotalRecordCount = total
		writeJSON(w, http.StatusOK, result)
		return
	}
	ordered, episodeTargets, err := h.hydrateCollectionMembers(r.Context(), session, pageIDs, true)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	routeID := h.codec.EncodeStringID(EncodedIDCollection, collection.ID)
	h.writeCollectionItemsPage(w, r, session, query, routeID, ordered, episodeTargets, total)
}

// sortEpisodeMembers orders episode members by the client's SortBy without
// changing which episodes belong to the collection, so a smart query's own
// limit still decides membership. Keys the episode rows cannot supply keep the
// query's order; ties keep it too, as does a handler without an episode
// repository.
func (h *ItemsHandler) sortEpisodeMembers(ctx context.Context, contentIDs []string, query itemsQuery) ([]string, error) {
	if h.episodeRepo == nil {
		return contentIDs, nil
	}
	episodes, err := h.episodeRepo.GetByIDs(ctx, contentIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*models.Episode, len(episodes))
	for _, episode := range episodes {
		byID[episode.ContentID] = episode
	}
	ordered := make([]*models.Episode, 0, len(byID))
	for _, contentID := range contentIDs {
		if episode, ok := byID[contentID]; ok {
			ordered = append(ordered, episode)
		}
	}

	if query.sort == compatBrowseRandomSort {
		rand.Shuffle(len(ordered), func(i, j int) { ordered[i], ordered[j] = ordered[j], ordered[i] })
	} else if compare := episodeSortCompare(query.sort); compare != nil {
		if query.order == catalog.BrowseOrderDescending {
			ascending := compare
			compare = func(a, b *models.Episode) int { return ascending(b, a) }
		}
		slices.SortStableFunc(ordered, compare)
	}
	ids := make([]string, 0, len(ordered))
	for _, episode := range ordered {
		ids = append(ids, episode.ContentID)
	}
	return ids, nil
}

// episodeSortCompare returns an ascending comparison for a mapped browse sort
// key, or nil when episode rows carry no matching field. Missing dates and
// ratings sort first ascending, so they trail a descending sort.
func episodeSortCompare(sortKey string) func(a, b *models.Episode) int {
	switch sortKey {
	case catalog.BrowseSortTitle:
		return func(a, b *models.Episode) int {
			return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		}
	case catalog.BrowseSortReleaseDate, catalog.BrowseSortYear:
		return func(a, b *models.Episode) int { return compareOptional(a.AirDate, b.AirDate, time.Time.Compare) }
	case catalog.BrowseSortCreatedAt:
		return func(a, b *models.Episode) int { return a.CreatedAt.Compare(b.CreatedAt) }
	case catalog.BrowseSortRatingIMDB:
		return func(a, b *models.Episode) int {
			return compareOptional(a.RatingIMDB, b.RatingIMDB, cmp.Compare[float64])
		}
	default:
		return nil
	}
}

// compareOptional orders nil before any value, then compares values.
func compareOptional[T any](a, b *T, compare func(T, T) int) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return compare(*a, *b)
}

// smartCollectionLeafType returns the item type of a smart collection whose
// query is scoped to movies or episodes, whose members are therefore all
// playable leaves. Episode-scoped queries only match episodes with a live file
// in a library the viewer may access.
func smartCollectionLeafType(c *models.LibraryCollection) (string, bool) {
	if !catalog.IsLiveQueryType(c.CollectionType) || len(c.QueryDefinition) == 0 {
		return "", false
	}
	var def catalog.QueryDefinition
	if err := json.Unmarshal(c.QueryDefinition, &def); err != nil {
		return "", false
	}
	switch scope := def.Normalize().MediaScope; scope {
	case compatMovieType, compatEpisodeType:
		return scope, true
	default:
		return "", false
	}
}

// Native item types the collection paths branch on.
const (
	compatMovieType   = "movie"
	compatSeriesType  = "series"
	compatEpisodeType = "episode"
)

// collectionMemberIDs returns a collection's member content IDs in collection
// order. Smart (live-query) collections derive membership from a query at read
// time and store no rows in library_collection_items, so ListItems returns
// nothing for them — that previously left smart-collection BoxSets showing a
// non-zero ChildCount but no browsable children. Resolve them via the query
// executor; stored collections keep the materialized ListItems path.
func (h *ItemsHandler) collectionMemberIDs(ctx context.Context, session *Session, collection *models.LibraryCollection) ([]string, error) {
	if catalog.IsLiveQueryType(collection.CollectionType) {
		return h.smartCollectionContentIDs(ctx, session, collection)
	}
	members, err := h.collections.ListItems(ctx, collection.ID)
	if err != nil {
		return nil, err
	}
	contentIDs := make([]string, 0, len(members))
	for _, member := range members {
		contentIDs = append(contentIDs, member.MediaItemID)
	}
	return contentIDs, nil
}

// wantsCollectionLeaves reports whether a BoxSet-children request asks for the
// collection's playable leaves rather than its direct members. Play all and
// Shuffle send Recursive=true with Filters=IsNotFolder (jellyfin-web) or with
// IncludeItemTypes naming Episode but no Series or Season (Android TV,
// Wholphin). Other recursive requests, including ones that only exclude types
// or that also ask for series, keep the member listing so collection pages
// don't fill with episodes. User-state, search, and catalog filters stay on
// the member paths, which already support them.
func (q itemsQuery) wantsCollectionLeaves() bool {
	return q.recursive && (q.isNotFolder || q.includesOnlyLeafTypes) && !q.hasMemberFilters()
}

// handleBoxSetLeaves serves a collection's playable leaves: member movies and
// episodes as they are, and member series expanded to the episodes the viewer
// can see, in collection order. Within a series, regular seasons play in order
// and specials (season 0) follow them. SortBy=Random shuffles the whole set;
// any other sort keeps collection order. leafType is set for smart collections
// scoped to movies or episodes, whose members are all leaves already.
func (h *ItemsHandler) handleBoxSetLeaves(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery, collection *models.LibraryCollection, leafType string) {
	ctx := r.Context()
	if !query.allowsVideo() || (leafType != "" && !query.allowsItemType(leafType)) {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
		return
	}
	contentIDs, err := h.collectionMemberIDs(ctx, session, collection)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	leaves := contentIDs
	if leafType == "" {
		if leaves, err = h.expandCollectionLeaves(ctx, session, query, contentIDs); err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
	}
	if query.sortExplicit && query.sort == compatBrowseRandomSort {
		leaves = slices.Clone(leaves)
		rand.Shuffle(len(leaves), func(i, j int) { leaves[i], leaves[j] = leaves[j], leaves[i] })
	}

	listItems, episodeTargets, err := h.hydrateCollectionMembers(ctx, session, slicePage(leaves, query.startIndex, query.limit), true)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	routeID := h.codec.EncodeStringID(EncodedIDCollection, collection.ID)
	h.writeCollectionItemsPage(w, r, session, query, routeID, listItems, episodeTargets, len(leaves))
}

// expandCollectionLeaves turns movie and series members into playable leaf
// IDs in collection order: movies as they are, series as their visible
// episodes. Members missing from media_items are hidden from this session;
// only episode-scoped smart collections hold episodes, and those skip
// expansion.
func (h *ItemsHandler) expandCollectionLeaves(ctx context.Context, session *Session, query itemsQuery, contentIDs []string) ([]string, error) {
	members, err := h.loadCompatItemsByContentIDs(ctx, session, contentIDs, nil)
	if err != nil {
		return nil, err
	}
	memberTypes := make(map[string]string, len(members))
	for _, member := range members {
		memberTypes[member.ContentID] = strings.ToLower(member.Type)
	}
	var seriesEpisodes map[string][]string
	if query.allowsItemType(compatEpisodeType) {
		if seriesEpisodes, err = h.collectionSeriesEpisodeIDs(ctx, session, members); err != nil {
			return nil, err
		}
	}

	leaves := make([]string, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		switch itemType := memberTypes[contentID]; {
		case itemType == compatSeriesType:
			leaves = append(leaves, seriesEpisodes[contentID]...)
		case isPlayableItemType(itemType) && query.allowsItemType(itemType):
			leaves = append(leaves, contentID)
		}
	}
	return leaves, nil
}

// visibleEpisodeIDs reports which episodes the viewer can see: those with a
// live file in a library the viewer may access (episode_libraries tracks live
// files per library). This is the rule the catalog's episode queries apply, so
// expanded series episodes match the episodes of an episode-scoped smart
// collection. Playback-quality limits apply at PlaybackInfo, as for every
// listing. Without a database pool it falls back to live-file presence.
func (h *ItemsHandler) visibleEpisodeIDs(ctx context.Context, session *Session, episodeIDs []string) (map[string]bool, error) {
	if len(episodeIDs) == 0 {
		return map[string]bool{}, nil
	}
	pool := h.compatPool()
	if pool == nil {
		if h.episodeRepo == nil {
			return map[string]bool{}, nil
		}
		return h.episodeRepo.HasFilesByIDs(ctx, episodeIDs)
	}

	access := h.resolveAccessFilter(ctx, session)
	conditions := []string{"el.episode_id = ANY($1)"}
	args := []any{episodeIDs}
	if access.AllowedLibraryIDs != nil {
		args = append(args, access.AllowedLibraryIDs)
		conditions = append(conditions, fmt.Sprintf("el.media_folder_id = ANY($%d)", len(args)))
	}
	if len(access.DisabledLibraryIDs) > 0 {
		// Like the catalog's episode queries, membership in any hidden library
		// hides the episode, even when another membership is allowed.
		args = append(args, access.DisabledLibraryIDs)
		conditions = append(conditions, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM episode_libraries el_out WHERE el_out.episode_id = el.episode_id AND el_out.media_folder_id = ANY($%d))",
			len(args)))
	}
	rows, err := pool.Query(ctx, "SELECT DISTINCT el.episode_id FROM episode_libraries el WHERE "+strings.Join(conditions, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	visible := make(map[string]bool, len(episodeIDs))
	for rows.Next() {
		var episodeID string
		if err := rows.Scan(&episodeID); err != nil {
			return nil, err
		}
		visible[episodeID] = true
	}
	return visible, rows.Err()
}

// collectionSeriesEpisodeIDs lists the visible episode IDs of each series
// member, regular seasons first and specials last. Series members were already
// access-filtered; each episode is checked too, since a series can span
// libraries with different access.
func (h *ItemsHandler) collectionSeriesEpisodeIDs(ctx context.Context, session *Session, members []upstreamListItem) (map[string][]string, error) {
	var seriesIDs []string
	for _, member := range members {
		if strings.EqualFold(member.Type, compatSeriesType) {
			seriesIDs = append(seriesIDs, member.ContentID)
		}
	}
	if len(seriesIDs) == 0 || h.episodeRepo == nil {
		return nil, nil
	}

	episodesBySeries, err := h.episodeRepo.ListBySeriesIDs(ctx, seriesIDs)
	if err != nil {
		return nil, err
	}
	var allIDs []string
	for _, episodes := range episodesBySeries {
		for _, episode := range episodes {
			allIDs = append(allIDs, episode.ContentID)
		}
	}
	visible, err := h.visibleEpisodeIDs(ctx, session, allIDs)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]string, len(episodesBySeries))
	for seriesID, episodes := range episodesBySeries {
		kept := make([]*models.Episode, 0, len(episodes))
		for _, episode := range episodes {
			if visible[episode.ContentID] {
				kept = append(kept, episode)
			}
		}
		slices.SortStableFunc(kept, func(a, b *models.Episode) int {
			if aSpecial, bSpecial := a.SeasonNumber == 0, b.SeasonNumber == 0; aSpecial != bSpecial {
				if aSpecial {
					return 1
				}
				return -1
			}
			if c := cmp.Compare(a.SeasonNumber, b.SeasonNumber); c != 0 {
				return c
			}
			return cmp.Compare(a.EpisodeNumber, b.EpisodeNumber)
		})
		ids := make([]string, 0, len(kept))
		for _, episode := range kept {
			ids = append(ids, episode.ContentID)
		}
		result[seriesID] = ids
	}
	return result, nil
}

// hydrateCollectionMembers resolves collection members to list items in the
// given order, dropping any the session cannot see. Movies and series come
// from media_items. When episodesPossible, members missing there resolve as
// episodes, which reach a collection through episode-scoped smart queries or
// series expansion for Play all; the returned episode targets carry their
// season and series context. Stored collections pass false, since their
// members only reference media_items.
func (h *ItemsHandler) hydrateCollectionMembers(ctx context.Context, session *Session, contentIDs []string, episodesPossible bool) ([]upstreamListItem, map[string]compatEpisodeTarget, error) {
	itemsByID, err := h.fetchCompatItemsByContentIDs(ctx, session, contentIDs, nil)
	if err != nil {
		return nil, nil, err
	}
	var missing []string
	if episodesPossible {
		for _, contentID := range contentIDs {
			if _, ok := itemsByID[contentID]; !ok {
				missing = append(missing, contentID)
			}
		}
	}
	var episodeTargets map[string]compatEpisodeTarget
	if len(missing) > 0 {
		episodeTargets, err = h.fetchCompatEpisodeTargetsByContentIDsWithDurations(ctx, session, missing, nil)
		if err != nil {
			return nil, nil, err
		}
	}
	ordered := make([]upstreamListItem, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		if item, ok := itemsByID[contentID]; ok {
			ordered = append(ordered, item)
		} else if target, ok := episodeTargets[contentID]; ok {
			ordered = append(ordered, target.Item)
		}
	}
	return ordered, episodeTargets, nil
}

// writeCollectionItemsPage hydrates user state for one page of collection
// members and writes the /Items result with ParentId stamped on each child.
// Detail-level Fields (MediaSources, Path, ...) upgrade each child to its
// detail DTO, as the library-parent browse path does; Infuse treats children
// without them as unplayable. Episode members also get their season and
// series context from episodeTargets.
func (h *ItemsHandler) writeCollectionItemsPage(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery, routeID string, listItems []upstreamListItem, episodeTargets map[string]compatEpisodeTarget, total int) {
	h.rememberListImages(listItems)
	contentIDs := contentIDsFromListItems(listItems)
	favorites, progress, err := resolveUserStateForContentIDs(r.Context(), session, h.userData, contentIDs)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	var detailsByID map[string]*upstreamItemDetail
	if query.needsDetailFields {
		// Members may span libraries, so the detail fetch is not library-scoped.
		detailsByID = h.batchListItemDetails(r.Context(), session, contentIDs, nil)
	}
	items := make([]baseItemDTO, 0, len(listItems))
	for _, item := range listItems {
		var dto baseItemDTO
		if detail, ok := detailsByID[item.ContentID]; ok && detail != nil {
			h.rememberDetailImages(*detail)
			dto = h.mapper.itemFromDetailWithFields(*detail, favorites[item.ContentID], progress[item.ContentID], query.requestedFields)
		} else {
			dto = h.mapper.itemFromList(item, favorites[item.ContentID], progress[item.ContentID], query.requestedFields)
		}
		dto.ParentID = routeID
		if target, ok := episodeTargets[item.ContentID]; ok {
			h.applyCompatEpisodeTarget(&dto, target)
		}
		items = append(items, dto)
	}
	h.applyListMediaSourceCounts(r.Context(), session, items, query)
	applyItemsResponseOptions(items, query)
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: total,
		StartIndex:       query.startIndex,
	})
}

// prepareSmartCollectionQuery resolves a smart (live-query) collection's stored
// query definition into an executable form: normalized, validated, item-limited,
// and intersected with the collection's own bound library scope, plus the
// session access filter. ok is false when the collection has no executable
// query — no executor wired, a malformed or invalid definition, or an empty
// library intersection — so callers degrade to no children rather than error
// and a single bad collection never 500s a browse.
func (h *ItemsHandler) prepareSmartCollectionQuery(ctx context.Context, session *Session, c *models.LibraryCollection) (catalog.QueryDefinition, catalog.AccessFilter, bool) {
	if h.queryExecutor == nil {
		return catalog.QueryDefinition{}, catalog.AccessFilter{}, false
	}

	var def catalog.QueryDefinition
	if len(c.QueryDefinition) > 0 {
		if err := json.Unmarshal(c.QueryDefinition, &def); err != nil {
			slog.DebugContext(ctx, "jellycompat smart collection query definition unmarshal failed", "component", "jellycompat",
				"collection_id", c.ID, "error", err)
			return catalog.QueryDefinition{}, catalog.AccessFilter{}, false
		}
	}
	def = def.Normalize()
	if err := def.ValidateWithOptions(false, false); err != nil {
		slog.DebugContext(ctx, "jellycompat smart collection query definition invalid", "component", "jellycompat",
			"collection_id", c.ID, "error", err)
		return catalog.QueryDefinition{}, catalog.AccessFilter{}, false
	}
	def = catalog.ApplySmartCollectionItemLimit(def)

	switch {
	case len(c.LibraryIDs) > 0:
		def.LibraryIDs = catalog.IntersectCollectionLibraryIDs(def.LibraryIDs, c.LibraryIDs)
		if len(def.LibraryIDs) == 0 {
			return catalog.QueryDefinition{}, catalog.AccessFilter{}, false
		}
	case c.LibraryID > 0:
		def.LibraryIDs = catalog.IntersectCollectionLibraryIDs(def.LibraryIDs, []int{c.LibraryID})
		if len(def.LibraryIDs) == 0 {
			return catalog.QueryDefinition{}, catalog.AccessFilter{}, false
		}
	}

	return def, h.resolveAccessFilter(ctx, session), true
}

// smartCollectionContentIDPage resolves a single page of a smart collection's
// member content IDs directly in SQL (OFFSET/LIMIT over the query's own order),
// plus the total membership count. This bounds per-request work to one page
// regardless of collection size — the membership is uncapped, so materializing
// every member to serve one browse page would scale memory and latency with the
// collection. ok is false when the collection has no executable query.
func (h *ItemsHandler) smartCollectionContentIDPage(ctx context.Context, session *Session, c *models.LibraryCollection, offset, limit int) (contentIDs []string, total int, ok bool, err error) {
	def, access, prepared := h.prepareSmartCollectionQuery(ctx, session, c)
	if !prepared {
		return nil, 0, false, nil
	}

	items, total, _, err := h.queryExecutor.PreviewPage(ctx, def, access, limit, offset, true)
	if err != nil {
		return nil, 0, false, err
	}

	contentIDs = make([]string, 0, len(items))
	for _, item := range items {
		contentIDs = append(contentIDs, item.ContentID)
	}
	return contentIDs, total, true, nil
}

// smartCollectionContentIDs resolves a live-query (smart) collection's full
// member set at read time, mirroring the web API's loadLiveCollectionItems. The
// returned content IDs are in the smart query's own order and access-filtered
// for the session. Used by the explicit-sort browse path, which re-sorts and
// paginates the membership as an allowlist; the default (no explicit sort) path
// uses smartCollectionContentIDPage to page directly in SQL.
func (h *ItemsHandler) smartCollectionContentIDs(ctx context.Context, session *Session, c *models.LibraryCollection) ([]string, error) {
	def, access, ok := h.prepareSmartCollectionQuery(ctx, session, c)
	if !ok {
		return nil, nil
	}

	items, total, err := h.queryExecutor.Preview(ctx, def, access, 1)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, nil
	}
	if total > len(items) {
		items, _, err = h.queryExecutor.Preview(ctx, def, access, total)
		if err != nil {
			return nil, err
		}
	}

	contentIDs := make([]string, 0, len(items))
	for _, item := range items {
		contentIDs = append(contentIDs, item.ContentID)
	}
	return contentIDs, nil
}
