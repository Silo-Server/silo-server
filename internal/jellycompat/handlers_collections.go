package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/usercollections"
)

// collectionSource is the subset of *catalog.LibraryCollectionRepository the
// compat layer relies on to expose library collections as Jellyfin BoxSets.
type collectionSource interface {
	ListAll(ctx context.Context, libraryID *int, opts catalog.ListLibraryCollectionsOptions) ([]*models.LibraryCollection, error)
	GetByID(ctx context.Context, id string) (*models.LibraryCollection, error)
	ListItems(ctx context.Context, collectionID string) ([]*models.LibraryCollectionItem, error)
	AnyVisibleInLibraries(ctx context.Context, libraryIDs []int) (bool, error)
}

// userCollectionSource is the subset of *usercollections.Store the compat layer
// relies on to expose the session owner's own personal collections — the ones
// they opted into their server Collections — as Jellyfin BoxSets. Every method
// takes the owning user and viewing profile: these rows are private, and the
// store's ACL is the privacy boundary for normal browse reads; ImageCandidates
// is used only behind a signed image capability check.
type userCollectionSource interface {
	List(ctx context.Context, userID int, profileID string, visibleLibraryIDs []int) ([]usercollections.ServerVisibleCollection, error)
	Get(ctx context.Context, userID int, profileID, key string, visibleLibraryIDs []int) (*usercollections.ServerVisibleCollection, error)
	AnyVisible(ctx context.Context, userID int, profileID string, visibleLibraryIDs []int) (bool, error)
	ImageCandidates(ctx context.Context, key string) ([]usercollections.ServerVisibleCollection, error)
}

// compatCollection is one collection on the BoxSet surface. Personal
// collections are adapted to the library collection shape so artwork, DTO
// mapping, search, sorting and paging stay on a single code path; only where
// their members come from differs.
type compatCollection struct {
	*models.LibraryCollection
	// personal marks a collection owned by the session user rather than the
	// server, so its members come from user_personal_collection_items.
	personal bool
}

// libraryCollectionFromUser adapts a personal collection to the library
// collection shape. Personal collections carry no library binding — they
// resolve against everything their owner can see — so LibraryID/LibraryIDs stay
// empty and collectionVisible is not consulted for them; the store's ownership
// and profile ACL already decided visibility, hence "visible".
func libraryCollectionFromUser(c usercollections.ServerVisibleCollection) *models.LibraryCollection {
	return &models.LibraryCollection{
		ID:              c.ID,
		Title:           c.Name,
		Description:     c.Description,
		CollectionType:  c.CollectionType,
		Visibility:      catalog.LibraryCollectionVisibilityVisible,
		PosterURL:       c.PosterPath,
		PosterThumbhash: c.PosterThumbhash,
	}
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
		PrimaryImageAspectRatio: &posterAspect,
		ImageTags:               map[string]string{"Primary": primaryTag},
		UserData: &itemUserDataDTO{
			Key:    collectionsViewID,
			ItemID: collectionsViewID,
		},
	}
}

// collectionsViewVisible reports whether the Collections view should appear in
// the session's library list. It is shown when at least one collection is
// visible to the session — a library collection scoped to a library the session
// can already see, or one of the session owner's own opted-in personal
// collections — via index-only EXISTS probes. A probe error fails closed (no
// tab) rather than failing the whole /UserViews response.
func (h *ItemsHandler) collectionsViewVisible(ctx context.Context, session *Session, libraries []upstreamUserLibrary) bool {
	ids := make([]int, 0, len(libraries))
	for _, lib := range libraries {
		ids = append(ids, lib.ID)
	}
	if h.collections != nil {
		visible, err := h.collections.AnyVisibleInLibraries(ctx, ids)
		if err != nil {
			slog.DebugContext(ctx, "jellycompat collections view existence check failed", "component", "jellycompat", "error", err)
		} else if visible {
			return true
		}
	}
	if h.userCollections == nil {
		return false
	}
	// Without this a user whose only collections are personal would never see
	// the Collections tab, and so could never reach them.
	visible, err := h.userCollections.AnyVisible(ctx, session.StreamAppUserID, session.ProfileID, ids)
	if err != nil {
		slog.DebugContext(ctx, "jellycompat user collections view existence check failed", "component", "jellycompat", "error", err)
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

type personalCollectionCatalogResolver interface {
	Resolve(ctx context.Context, req catalog.CatalogRequest, access catalog.AccessFilter) (*catalog.CatalogResult, error)
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

func libraryIDSlice(ids map[int]struct{}) []int {
	out := make([]int, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
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

// loadVisibleCollection resolves a source-tagged BoxSet route ID to the
// collection behind it, applying the compat visibility rules. Returns
// (nil, nil) when the collection does not exist or the session may not see it —
// the two are deliberately indistinguishable, which is what keeps another
// user's personal collection private. Infrastructure errors propagate so
// transient failures don't masquerade as 404s.
func (h *ItemsHandler) loadVisibleCollection(ctx context.Context, session *Session, collectionID string, personalRoute bool) (*compatCollection, error) {
	if !personalRoute {
		collection, err := h.loadVisibleLibraryCollection(ctx, session, collectionID)
		if err != nil || collection == nil {
			return nil, err
		}
		return &compatCollection{LibraryCollection: collection}, nil
	}
	if h.userCollections == nil {
		return nil, nil
	}
	visible, err := h.visibleLibraryIDs(ctx, session)
	if err != nil {
		return nil, err
	}
	personal, err := h.userCollections.Get(ctx, session.StreamAppUserID, session.ProfileID, collectionID, libraryIDSlice(visible))
	if err != nil {
		return nil, err
	}
	if personal == nil {
		return nil, nil
	}
	return &compatCollection{
		LibraryCollection: libraryCollectionFromUser(*personal),
		personal:          true,
	}, nil
}

// loadVisibleLibraryCollection fetches a library collection and applies the
// compat visibility rules, returning (nil, nil) when it does not exist or the
// session may not see it.
func (h *ItemsHandler) loadVisibleLibraryCollection(ctx context.Context, session *Session, collectionID string) (*models.LibraryCollection, error) {
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

func (h *ItemsHandler) boxSetFromCompatCollection(ctx context.Context, c *compatCollection) baseItemDTO {
	kind := EncodedIDCollection
	if c.personal {
		kind = EncodedIDUserCollection
	}
	routeID := h.codec.EncodeStringID(kind, c.ID)
	imgTags := map[string]string{}
	if posterURL := h.presignCollectionPoster(ctx, c.PosterURL); posterURL != "" {
		// Personal artwork resolves durably; never expose its untrusted URLs
		// through the global legacy-tag cache, which bypasses that resolver.
		if h.images != nil && !c.personal {
			h.images.RememberSized(routeID, "Primary", posterURL, compatCardImageSize)
		}
		imgTags["Primary"] = h.mapper.imageTagSigner.Tag(
			imageTagSeed(routeID, "Primary", compatCardImageSize, c.PosterURL, "", time.Time{}),
			posterURL,
		)
	} else {
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
		if h.images != nil && !c.personal {
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
func (h *ItemsHandler) boxSetsByIDs(ctx context.Context, session *Session, collectionIDs, personalCollectionIDs []string) ([]baseItemDTO, error) {
	if len(collectionIDs) == 0 && len(personalCollectionIDs) == 0 {
		return nil, nil
	}
	type collectionRef struct {
		id       string
		personal bool
	}
	refs := make([]collectionRef, 0, len(collectionIDs)+len(personalCollectionIDs))
	for _, id := range collectionIDs {
		refs = append(refs, collectionRef{id: id})
	}
	for _, id := range personalCollectionIDs {
		refs = append(refs, collectionRef{id: id, personal: true})
	}
	items := make([]baseItemDTO, 0, len(refs))
	for _, ref := range refs {
		collection, err := h.loadVisibleCollection(ctx, session, ref.id, ref.personal)
		if err != nil {
			return nil, err
		}
		if collection == nil {
			continue
		}
		items = append(items, h.boxSetFromCompatCollection(ctx, collection))
	}
	return items, nil
}

// handleBoxSetsList serves GET /Items with IncludeItemTypes=BoxSet by listing
// visible library collections, optionally scoped to one library via ParentId.
// Filtering, sorting, and paging happen on the lightweight collection rows;
// DTOs (with artwork presigning) are built only for the returned page.
func (h *ItemsHandler) handleBoxSetsList(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	if h.collections == nil && h.userCollections == nil {
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

	var collections []*models.LibraryCollection
	if h.collections != nil {
		var err error
		collections, err = h.collections.ListAll(r.Context(), libFilter, catalog.ListLibraryCollectionsOptions{})
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
	}

	searchTerm := strings.ToLower(strings.TrimSpace(query.searchTerm))
	namePrefix := strings.ToLower(query.namePrefix)
	matched := make([]*compatCollection, 0, len(collections))
	for _, c := range collections {
		if !collectionVisible(c, visible) {
			continue
		}
		if !collectionTitleMatches(c.Title, searchTerm, namePrefix) {
			continue
		}
		matched = append(matched, &compatCollection{LibraryCollection: c})
	}

	// The session owner's own opted-in personal collections list alongside the
	// server's. They carry no library binding, so collectionVisible does not
	// apply — the store scoped them to this user, this profile and (when the
	// request names one) this library.
	if h.userCollections != nil {
		visibleIDs := libraryIDSlice(visible)
		if libFilter != nil {
			visibleIDs = []int{*libFilter}
		}
		personal, err := h.userCollections.List(r.Context(), session.StreamAppUserID, session.ProfileID, visibleIDs)
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
		for _, c := range personal {
			if !collectionTitleMatches(c.Name, searchTerm, namePrefix) {
				continue
			}
			matched = append(matched, &compatCollection{LibraryCollection: libraryCollectionFromUser(c), personal: true})
		}
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
	items := make([]baseItemDTO, 0, len(page))
	for _, c := range page {
		items = append(items, h.boxSetFromCompatCollection(r.Context(), c))
	}
	writeJSON(w, http.StatusOK, queryResultDTO{
		Items:            items,
		TotalRecordCount: len(matched),
		StartIndex:       query.startIndex,
	})
}

// collectionTitleMatches applies the BoxSet listing's search-term and
// name-prefix filters. Both filters are expected pre-lowercased.
func collectionTitleMatches(title, searchTerm, namePrefix string) bool {
	title = strings.ToLower(title)
	if searchTerm != "" && !strings.Contains(title, searchTerm) {
		return false
	}
	return namePrefix == "" || strings.HasPrefix(title, namePrefix)
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
func (h *ItemsHandler) handleBoxSetItem(w http.ResponseWriter, r *http.Request, session *Session, collectionID string, personalRoute bool) {
	collection, err := h.loadVisibleCollection(r.Context(), session, collectionID, personalRoute)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if collection == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}
	writeJSON(w, http.StatusOK, h.boxSetFromCompatCollection(r.Context(), collection))
}

// handleBoxSetChildren serves GET /Items?ParentId={boxsetId} by hydrating the
// collection's members. Without an explicit SortBy the curated collection
// position order is preserved; an explicit SortBy delegates ordering and
// paging to the catalog browse path.
func (h *ItemsHandler) handleBoxSetChildren(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery) {
	personalRoute := query.parentPersonalCollectionID != ""
	collectionID := query.parentCollectionID
	if personalRoute {
		collectionID = query.parentPersonalCollectionID
	}
	collection, err := h.loadVisibleCollection(r.Context(), session, collectionID, personalRoute)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if collection == nil {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
		return
	}
	// Personal collections resolve entirely through the catalog resolver, which
	// already owns their membership, display filter, sorting and paging. Without
	// it they have no members to serve, the same way a nil collections source
	// yields no library collections.
	if collection.personal {
		if h.collectionResolver == nil {
			writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
			return
		}
		h.handlePersonalBoxSetChildren(w, r, session, query, collection)
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
	if catalog.IsLiveQueryType(collection.CollectionType) && !query.sortExplicit {
		routeID := h.codec.EncodeStringID(EncodedIDCollection, collection.ID)
		pageIDs, total, ok, pageErr := h.smartCollectionContentIDPage(
			r.Context(), session, collection.LibraryCollection, query.startIndex, query.limit)
		if pageErr != nil {
			writeCompatUpstreamError(w, pageErr)
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
		itemsByID, fetchErr := h.fetchCompatItemsByContentIDs(r.Context(), session, pageIDs, nil)
		if fetchErr != nil {
			writeCompatUpstreamError(w, fetchErr)
			return
		}
		ordered := make([]upstreamListItem, 0, len(pageIDs))
		for _, contentID := range pageIDs {
			if item, itemOK := itemsByID[contentID]; itemOK {
				ordered = append(ordered, item)
			}
		}
		h.writeCollectionItemsPage(w, r, session, query, routeID, ordered, total)
		return
	}

	// Smart (live-query) collections derive membership from a query at read
	// time and store no rows in library_collection_items, so ListItems returns
	// nothing for them — that previously left smart-collection BoxSets showing a
	// non-zero ChildCount but no browsable children. Resolve them via the query
	// executor; stored collections keep the materialized ListItems path, from
	// user_personal_collection_items when the collection is the session owner's.
	var contentIDs []string
	if catalog.IsLiveQueryType(collection.CollectionType) {
		contentIDs, err = h.smartCollectionContentIDs(r.Context(), session, collection.LibraryCollection)
		if err != nil {
			writeCompatUpstreamError(w, err)
			return
		}
	} else {
		members, listErr := h.collections.ListItems(r.Context(), collection.ID)
		if listErr != nil {
			writeCompatUpstreamError(w, listErr)
			return
		}
		contentIDs = make([]string, 0, len(members))
		for _, member := range members {
			contentIDs = append(contentIDs, member.MediaItemID)
		}
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

//nolint:goconst // Keep Jellyfin sort and filter vocabulary beside its protocol translation.
func (h *ItemsHandler) handlePersonalBoxSetChildren(w http.ResponseWriter, r *http.Request, session *Session, query itemsQuery, collection *compatCollection) {
	if (query.hasItemTypeFilter && len(query.itemTypes) == 0) ||
		(query.mediaTypesExplicit && !query.mediaTypesSet["video"]) {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
		return
	}
	def := catalog.QueryDefinition{}
	randomize := false
	if query.sortExplicit {
		sortField := query.sort
		switch sortField {
		case "sort_title":
			sortField = "title"
		case "created_at":
			sortField = "added_at"
		case "random":
			randomize = true
		}
		if !randomize {
			resolved, ok := catalog.NormalizeCollectionSort(sortField, query.order, true)
			if !ok {
				writeError(w, http.StatusBadRequest, "BadRequest", "Unsupported collection sort")
				return
			}
			def.Sort = resolved
		}
	}
	itemTypes := query.itemTypes
	if !query.hasItemTypeFilter {
		itemTypes = compatVideoTypeList
	}
	// Use exact type rules: the catalog's episode scope can expand collections
	// or fall back to their top-level members instead of filtering them.
	typeRules := make([]catalog.QueryRule, 0, len(itemTypes))
	for _, itemType := range itemTypes {
		if slices.Contains(compatVideoTypeList, itemType) {
			typeRules = append(typeRules, catalog.QueryRule{Field: "type", Op: "is", Value: itemType})
		}
	}
	if len(typeRules) == 0 {
		writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
		return
	}
	def.Groups = append(def.Groups, catalog.QueryGroup{Match: "any", Rules: typeRules})
	var rules []catalog.QueryRule
	if query.genreName != "" {
		rules = append(rules, catalog.QueryRule{Field: "genre", Op: "contains", Value: query.genreName})
	}
	if query.isPlayed != nil {
		rules = append(rules, catalog.QueryRule{Field: "watched", Op: "is", Value: *query.isPlayed})
	}
	if query.isFavorite {
		rules = append(rules, catalog.QueryRule{Field: "favorited", Op: "is", Value: true})
	}
	if query.isResumable {
		rules = append(rules, catalog.QueryRule{Field: "in_progress", Op: "is", Value: true})
	}
	if len(rules) > 0 {
		def.Groups = append(def.Groups, catalog.QueryGroup{Match: "all", Rules: rules})
	}
	access := h.resolveAccessFilter(r.Context(), session)
	access.MaxContentRating = clampMaxContentRating(access.MaxContentRating, query.maxOfficialRating)
	result, err := h.collectionResolver.Resolve(r.Context(), catalog.CatalogRequest{
		Source:          catalog.CatalogSourceUserCollection,
		CollectionID:    collection.ID,
		PersonID:        query.personID,
		NamePrefix:      query.namePrefix,
		SearchQuery:     query.searchTerm,
		Query:           def,
		Limit:           query.limit,
		Offset:          query.startIndex,
		UseSourceOrder:  !query.sortExplicit || randomize,
		RequireBackdrop: query.requireBackdrop,
		Randomize:       randomize,
	}, access)
	if err != nil {
		if errors.Is(err, catalog.ErrCatalogSourceNotFound) {
			writeJSON(w, http.StatusOK, emptyQueryResult(query.startIndex))
			return
		}
		writeCompatUpstreamError(w, err)
		return
	}
	listItems := h.compatListItemsFromModels(r.Context(), access, result.Items)
	routeID := h.codec.EncodeStringID(EncodedIDUserCollection, collection.ID)
	h.writeCollectionItemsPage(w, r, session, query, routeID, listItems, result.Total)
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
