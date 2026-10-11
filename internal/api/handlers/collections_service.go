package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The personal-collection seams. v1 HTTP handlers and the v2 operations both
// call them, so the two surfaces share one decision; a failure is an
// *APIError carrying the v1 status, code and message (and the rejected
// member where one is named).

// PersonalCollectionCreateCommand is a collection creation with its request
// already parsed and its caller reduced to an identity.
type PersonalCollectionCreateCommand struct {
	UserID    int
	ProfileID string
	Request   PersonalCollectionCreateRequest
	// PosterFile reads the uploaded poster part; nil when the request
	// carried no multipart body. It answers http.ErrMissingFile when the
	// part is absent.
	PosterFile func() ([]byte, error)
	// V1Rules: see PersonalCollectionPreviewRequest.
	V1Rules bool
}

const (
	collectionTypeMDBList  = "mdblist"
	collectionTypeSmart    = "smart"
	collectionImagePoster  = "poster"
	collectionFilterType   = "type"
	collectionFilterAll    = "all"
	collectionFilterSeries = "series"
)

// ListPersonalCollections answers the collections the profile may see, as
// v1 GET /collections does: its own in its order, then other profiles' shared
// collections grouped by owner. Personal collection groups no longer exist,
// so Groups is always empty.
func (h *CollectionHandler) ListPersonalCollections(ctx context.Context, userID int, profileID string) (PersonalCollectionListView, error) {
	var none PersonalCollectionListView
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	collections, err := store.ListCollections(ctx, profileID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to list collections")
	}
	return PersonalCollectionListView{
		Collections: h.collectionViews(ctx, store, userID, profileID, collections),
		Groups:      []CollectionGroupView{},
	}, nil
}

// PersonalCollectionsHoldingItem returns the ids of profileID's own manual
// collections that hold itemID. It returns none when the request's viewer
// cannot access the title, so the answer never reveals that a hidden title
// exists or which collections still hold it.
func (h *CollectionHandler) PersonalCollectionsHoldingItem(ctx context.Context, userID int, profileID, itemID string) (map[string]bool, error) {
	if profileID == "" || itemID == "" {
		return map[string]bool{}, nil
	}
	if err := h.requireVisibleCollectionItem(ctx, itemID); err != nil {
		if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status == http.StatusNotFound {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	reader, ok := store.(userstore.CollectionMembershipReader)
	if !ok {
		return nil, apiError(http.StatusNotImplemented, "unsupported", "This store cannot report collection membership")
	}
	ids, err := reader.ManualCollectionsHolding(ctx, profileID, itemID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to read collection membership")
	}
	holding := make(map[string]bool, len(ids))
	for _, id := range ids {
		holding[id] = true
	}
	return holding, nil
}

// Capabilities is the additive feature support collection clients detect.
func (h *CollectionHandler) Capabilities() CollectionCapabilitiesView {
	return CollectionCapabilitiesView{
		DisplayFilterFields: []string{collectionFilterType, "watched"},
		DisplayFilterPresets: CollectionDisplayFilterPresetsView{
			Watched: []string{collectionFilterAll, "watched", "unwatched"},
			Media:   []string{collectionFilterAll, itemTypeMovie, collectionFilterSeries},
		},
		CollectionDefaultSort:     true,
		CollectionSortPreferences: true,
		EffectiveCollectionSort:   true,
		SortPreferenceKinds:       sortPreferenceKinds,
		PosterCollages:            h.Collages != nil,
	}
}

// CreatePersonalCollection creates a collection for the profile: validation,
// query and sort normalization, the store write, and the optional poster.
func (h *CollectionHandler) CreatePersonalCollection(ctx context.Context, cmd PersonalCollectionCreateCommand) (PersonalCollectionView, error) {
	var none PersonalCollectionView
	req := cmd.Request
	if req.Name == "" {
		return none, fieldError("name", "Collection name is required")
	}

	store, err := h.storeProvider.ForUser(ctx, cmd.UserID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}

	if cmd.PosterFile != nil || req.PosterSourceURL != "" {
		if err := collectionFeatureError(store, "artwork"); err != nil {
			return none, err
		}
	}
	if req.Description != "" {
		if err := collectionFeatureError(store, "description"); err != nil {
			return none, err
		}
	}
	queryDefinitionJSON := defaultJSON(req.QueryDefinition)
	collectionType := firstNonEmptyCollection(req.CollectionType, "manual")
	if collectionType == collectionTypeSmart {
		queryDefinitionJSON, err = normalizeSmartCollectionQueryDefinitionJSON(queryDefinitionJSON, true, true, cmd.V1Rules)
		if err != nil {
			return none, fieldError("query_definition", "Invalid query_definition")
		}
	} else if len(req.QueryDefinition) > 0 {
		queryDefinitionJSON, err = normalizeQueryDefinitionJSON(queryDefinitionJSON, true, true, cmd.V1Rules)
		if err != nil {
			return none, fieldError("query_definition", "Invalid query_definition")
		}
	}
	queryDefinition := string(queryDefinitionJSON)
	sortConfig, err := NormalizeCollectionSortConfig(req.SortConfig, true)
	if err != nil {
		return none, fieldError("sort_config", err.Error())
	}
	displayQueryDefinition, err := catalog.NormalizeDisplayQueryFragment(req.DisplayQueryDefinition)
	if err != nil {
		return none, fieldError("display_query_definition", err.Error())
	}
	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID:           cmd.ProfileID,
		Name:                       req.Name,
		Description:                req.Description,
		CollectionType:             collectionType,
		IsShared:                   req.IsShared,
		QueryDefinition:            queryDefinition,
		SortConfig:                 sortConfig,
		DisplayQueryDefinition:     displayQueryDefinition,
		IncludeInServerCollections: req.IncludeInServerCollections,
	})
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to create collection")
	}

	posterProvided, err := h.processCollectionPoster(ctx, store, collection.ID, cmd.ProfileID, cmd.PosterFile, req.PosterSourceURL)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", err.Error())
	}
	if posterProvided {
		if refreshed, err := store.GetCollection(ctx, collection.ID); err == nil {
			collection = refreshed
		}
	}
	h.refreshCollage(ctx, store, cmd.UserID, collection)
	return h.collectionView(ctx, store, cmd.UserID, cmd.ProfileID, *collection)
}

// ReorderPersonalCollections replaces the order of the profile's own
// collections. orderedIDs must name each of them exactly once; another
// profile's collection, even a shared one, is a validation failure.
func (h *CollectionHandler) ReorderPersonalCollections(ctx context.Context, userID int, profileID string, orderedIDs []string) error {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := collectionFeatureError(store, "item_reorder"); err != nil {
		return err
	}
	if err := reorderCollectionsWithRevision(ctx, store, profileID, orderedIDs); err != nil {
		if errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
			return err
		}
		if errors.Is(err, collectionutil.ErrOrderedIDsMismatch) {
			return fieldError("ordered_ids", "ordered_ids must name each of your own collections exactly once")
		}
		if strings.Contains(err.Error(), "ordered_ids contains duplicates") {
			return fieldError("ordered_ids", "ordered_ids contains duplicates")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to reorder collections")
	}
	return nil
}

// CreateCollectionGroup creates an account-wide collection group. Name and
// slug are trimmed; an empty slug is derived from the name by the store.
func (h *CollectionHandler) CreateCollectionGroup(ctx context.Context, userID int, req CollectionGroupCreateRequest) (CollectionGroupView, error) {
	var none CollectionGroupView
	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(req.Slug)
	if req.Name == "" {
		return none, fieldError("name", "name is required")
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := collectionFeatureError(store, "groups"); err != nil {
		return CollectionGroupView{}, err
	}
	group, err := store.CreateCollectionGroup(ctx, req.Name, req.Slug, userstore.GroupSortMode(req.DefaultSortMode))
	if err != nil {
		return none, apiError(http.StatusBadRequest, policyErrorBadRequest, err.Error())
	}
	return collectionGroupView(*group), nil
}

// UpdateCollectionGroup applies the present members of req to the group.
func (h *CollectionHandler) UpdateCollectionGroup(ctx context.Context, userID int, id string, req CollectionGroupUpdateRequest) (CollectionGroupView, error) {
	var none CollectionGroupView
	if id == "" {
		return none, fieldError("id", "id is required")
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		req.Name = &name
		if name == "" {
			return none, fieldError("name", "name cannot be empty")
		}
	}
	if req.Slug != nil {
		slug := strings.TrimSpace(*req.Slug)
		req.Slug = &slug
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := collectionFeatureError(store, "groups"); err != nil {
		return CollectionGroupView{}, err
	}
	var sortMode *userstore.GroupSortMode
	if req.DefaultSortMode != nil {
		mode := userstore.GroupSortMode(*req.DefaultSortMode)
		sortMode = &mode
	}
	group, err := updateCollectionGroupWithRevision(ctx, store, id, req.Name, req.Slug, sortMode)
	if err != nil {
		if errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
			return none, err
		}
		return none, apiError(http.StatusBadRequest, policyErrorBadRequest, err.Error())
	}
	return collectionGroupView(*group), nil
}

// DeleteCollectionGroup removes a group; its collections become ungrouped.
func (h *CollectionHandler) DeleteCollectionGroup(ctx context.Context, userID int, id string) error {
	if id == "" {
		return fieldError("id", "id is required")
	}
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := collectionFeatureError(store, "groups"); err != nil {
		return err
	}
	if err := deleteCollectionGroupWithRevision(ctx, store, id); err != nil {
		if errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
			return err
		}
		return apiError(http.StatusBadRequest, policyErrorBadRequest, err.Error())
	}
	return nil
}

// ReorderCollectionGroups replaces the order of the account's groups.
func (h *CollectionHandler) ReorderCollectionGroups(ctx context.Context, userID int, orderedIDs []string) error {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := collectionFeatureError(store, "groups"); err != nil {
		return err
	}
	if err := reorderCollectionGroupsWithRevision(ctx, store, orderedIDs); err != nil {
		if errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
			return err
		}
		return apiError(http.StatusBadRequest, policyErrorBadRequest, err.Error())
	}
	return nil
}

// collectionViews renders stored collections with their posters presigned and
// item_count set to the members profileID can see: for another profile's
// collection, only those its owner can access too. On /api/v2 a collection
// without an uploaded or imported poster shows its collage for that profile,
// once built. A collection whose owner cannot be resolved is left out.
func (h *CollectionHandler) collectionViews(ctx context.Context, store userstore.UserStore, userID int, profileID string, collections []userstore.Collection) []PersonalCollectionView {
	var reads ownedCollectionReads
	if userstore.HasCatalogSQLState(store) {
		sources := make([]ownedCollectionDefinition, 0, len(collections))
		for _, c := range collections {
			sources = append(sources, ownedCollectionDefinition{
				PersonalCollectionDefinition: usercollections.CollectionDefinition(c),
				CreatorProfileID:             c.CreatorProfileID,
				WantsCollage:                 strings.TrimSpace(c.PosterURL) == "",
			})
		}
		reads = ownerScopedCollectionReads(ctx, h.Executor, h.CollectionOwners, collagesForRead(ctx, h.Collages), userID, profileID, sources, AccessFilterFromContext(ctx, ""))
	}
	views := make([]PersonalCollectionView, 0, len(collections))
	for _, c := range collections {
		if reads.unavailable[c.ID] {
			continue
		}
		if n, ok := reads.counts[c.ID]; ok {
			c.ItemCount = n
		}
		resp := toCollectionResponse(c)
		posterPath := c.PosterURL
		if collage, ok := reads.posters[c.ID]; ok {
			posterPath, resp.PosterThumbhash, resp.PosterIsCollage = collage.Path, collage.Thumbhash, true
		}
		resp.PosterURL = h.presignUserCollectionPoster(ctx, posterPath)
		views = append(views, resp)
	}
	return views
}

// collagesForRead is the collage service a read of personal collections uses:
// /api/v2 reads show collages, while the frozen /api/v1 bridge keeps showing
// only uploaded and imported posters.
func collagesForRead(ctx context.Context, collages *catalog.PersonalCollectionCollages) *catalog.PersonalCollectionCollages {
	if !isNativeAPIV2(ctx) {
		return nil
	}
	return collages
}

// collectionView renders one stored collection as collectionViews does, and
// fails when its owner's access cannot be resolved.
func (h *CollectionHandler) collectionView(ctx context.Context, store userstore.UserStore, userID int, profileID string, c userstore.Collection) (PersonalCollectionView, error) {
	views := h.collectionViews(ctx, store, userID, profileID, []userstore.Collection{c})
	if len(views) == 0 {
		return PersonalCollectionView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load collection")
	}
	return views[0], nil
}

func collectionGroupView(g userstore.CollectionGroup) CollectionGroupView {
	return CollectionGroupView{
		ID:              g.ID,
		Name:            g.Name,
		Slug:            g.Slug,
		DefaultSortMode: string(g.DefaultSortMode),
		SortOrder:       g.SortOrder,
	}
}

// posterFileReader is the multipart poster part of a v1 request; nil when
// the request is not multipart.
func posterFileReader(r *http.Request) func() ([]byte, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		return nil
	}
	return func() ([]byte, error) { return readCollectionImageMultipart(r, collectionImagePoster) }
}

// PersonalCollectionFeatures describes the acting account's storage support.
func (h *CollectionHandler) PersonalCollectionFeatures(ctx context.Context, userID int) (userstore.CollectionFeatures, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return userstore.CollectionFeatures{}, apiError(500, "internal_error", "Failed to access user store")
	}
	features := userstore.CollectionFeatures{}
	if provider, ok := store.(userstore.CollectionFeatureProvider); ok {
		features = provider.CollectionFeatures()
	}
	// Without artwork storage no poster can be uploaded or composed, as
	// server collections report it.
	features.Artwork = features.Artwork && h.ArtworkStore != nil
	return features, nil
}

func collectionFeatureError(store userstore.UserStore, feature string) error {
	provider, ok := store.(userstore.CollectionFeatureProvider)
	if !ok {
		return nil
	}
	f := provider.CollectionFeatures()
	supported := false
	switch feature {
	case "groups":
		supported = f.Groups
	case "imports":
		supported = f.Imports
	case "artwork":
		supported = f.Artwork
	case "item_reorder":
		supported = f.ItemReorder
	case "description":
		supported = f.Description
	}
	if !supported {
		return apiError(http.StatusNotImplemented, "unsupported", "The acting account does not support collection "+feature)
	}
	return nil
}
