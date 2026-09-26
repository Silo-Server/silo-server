package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
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
}

const (
	collectionTypeMDBList  = "mdblist"
	collectionTypeSmart    = "smart"
	collectionImagePoster  = "poster"
	collectionFilterType   = "type"
	collectionFilterAll    = "all"
	collectionFilterSeries = "series"
)

// ListPersonalCollections answers the profile's visible collections and the
// account's groups, as v1 GET /collections does.
func (h *CollectionHandler) ListPersonalCollections(ctx context.Context, userID int, profileID string) (PersonalCollectionListView, error) {
	var none PersonalCollectionListView
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}

	collectionsCh := make(chan []userstore.Collection, 1)
	groupsCh := make(chan []userstore.CollectionGroup, 1)
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		collections, err := store.ListCollections(egCtx, profileID)
		if err != nil {
			return err
		}
		collectionsCh <- collections
		return nil
	})
	eg.Go(func() error {
		groups, err := store.ListCollectionGroups(egCtx)
		if err != nil {
			return err
		}
		groupsCh <- groups
		return nil
	})
	if err := eg.Wait(); err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to list collections")
	}
	collections := <-collectionsCh
	groups := <-groupsCh

	resp := PersonalCollectionListView{
		Collections: make([]PersonalCollectionView, 0, len(collections)),
		Groups:      make([]CollectionGroupView, 0, len(groups)),
	}
	for _, c := range collections {
		resp.Collections = append(resp.Collections, h.collectionView(ctx, c))
	}
	for _, g := range groups {
		resp.Groups = append(resp.Groups, collectionGroupView(g))
	}
	return resp, nil
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
	queryDefinitionJSON := defaultJSON(req.QueryDefinition)
	collectionType := firstNonEmptyCollection(req.CollectionType, "manual")
	if collectionType == collectionTypeSmart {
		queryDefinitionJSON, err = normalizeSmartCollectionQueryDefinitionJSON(queryDefinitionJSON, true, true)
		if err != nil {
			return none, fieldError("query_definition", "Invalid query_definition")
		}
	} else if len(req.QueryDefinition) > 0 {
		queryDefinitionJSON, err = normalizeQueryDefinitionJSON(queryDefinitionJSON, true, true)
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
		CollectionType:             collectionType,
		IsShared:                   req.IsShared,
		AllowedProfileIDs:          req.AllowedProfileIDs,
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
	return h.collectionView(ctx, *collection), nil
}

// ReorderPersonalCollections replaces the order of one group's collections.
// orderedIDs must name every collection in scope exactly once.
func (h *CollectionHandler) ReorderPersonalCollections(ctx context.Context, userID int, profileID string, groupID *string, orderedIDs []string) error {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := collectionFeatureError(store, "item_reorder"); err != nil {
		return err
	}
	if err := reorderCollectionsWithRevision(ctx, store, profileID, groupID, orderedIDs); err != nil {
		if errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
			return err
		}
		if errors.Is(err, collectionutil.ErrOrderedIDsMismatch) {
			return fieldError("ordered_ids", "ordered_ids must include every visible collection in the group exactly once")
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

// collectionView renders a stored collection with its poster presigned.
func (h *CollectionHandler) collectionView(ctx context.Context, c userstore.Collection) PersonalCollectionView {
	resp := toCollectionResponse(c)
	resp.PosterURL = h.presignUserCollectionPoster(ctx, c.PosterURL)
	return resp
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
	if features, ok := store.(userstore.CollectionFeatureProvider); ok {
		return features.CollectionFeatures(), nil
	}
	return userstore.CollectionFeatures{}, nil
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
	}
	if !supported {
		return apiError(http.StatusNotImplemented, "unsupported", "The acting account does not support collection "+feature)
	}
	return nil
}
