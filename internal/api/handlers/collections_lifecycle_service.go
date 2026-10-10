package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type PersonalCollectionUpdateCommand struct {
	UserID       int
	ProfileID    string
	CollectionID string
	Request      PersonalCollectionUpdateRequest
	PosterFile   func() ([]byte, error)
	// V1Rules: see PersonalCollectionPreviewRequest.
	V1Rules bool
}

func (h *CollectionHandler) UpdatePersonalCollection(ctx context.Context, cmd PersonalCollectionUpdateCommand) (PersonalCollectionView, error) {
	var none PersonalCollectionView
	userID, profileID, collectionID, req := cmd.UserID, cmd.ProfileID, cmd.CollectionID, cmd.Request

	if collectionID == "" {
		return none, apiError(http.StatusBadRequest, "bad_request", "Collection ID is required")
	}

	store, existing, err := h.personalCollectionStore(ctx, userID, profileID, collectionID, true)
	if err != nil {
		return none, err
	}
	if strings.EqualFold(strings.TrimSpace(existing.CollectionType), "trakt") &&
		(req.SourceURL != nil || req.MaxItems != nil || req.LibraryIDs != nil) {
		return none, apiError(http.StatusConflict, "legacy_source_immutable", "Legacy Trakt collection sources cannot be changed")
	}

	if cmd.PosterFile != nil || req.PosterSourceURL != nil {
		if err := collectionFeatureError(store, "artwork"); err != nil {
			return none, err
		}
	}
	if req.SourceURL != nil || req.MaxItems != nil || req.LibraryIDs != nil || req.SyncSchedule != nil {
		if err := collectionFeatureError(store, "imports"); err != nil {
			return none, err
		}
	}
	if req.GroupID.Set() {
		if err := collectionFeatureError(store, "groups"); err != nil {
			if group := req.GroupID.Value(); group != nil && strings.TrimSpace(*group) != "" {
				return none, err
			}
			// A null or blank group_id asks for no group, which every
			// collection already has on a store without groups.
			req.GroupID = optionalNullableString{}
		}
	}
	input := userstore.UpdateCollectionInput{
		ExpectedRevision:           collectionExpectedRevision(ctx),
		ID:                         collectionID,
		RequestProfileID:           profileID,
		Name:                       req.Name,
		Description:                req.Description,
		IncludeInServerCollections: req.IncludeInServerCollections,
	}
	if req.DisplayQueryDefinition != nil {
		displayQueryDefinition, err := catalog.NormalizeDisplayQueryFragment(req.DisplayQueryDefinition)
		if err != nil {
			return none, fieldError("display_query_definition", err.Error())
		}
		input.DisplayQueryDefinition = &displayQueryDefinition
	}
	if len(req.QueryDefinition) > 0 {
		normalized, err := normalizeSmartCollectionQueryDefinitionJSON(req.QueryDefinition, true, true, cmd.V1Rules)
		if err != nil {
			return none, fieldError("query_definition", "Invalid query_definition")
		}
		value := string(normalized)
		input.QueryDefinition = &value
	}
	if len(req.SortConfig) > 0 {
		value, err := NormalizeCollectionSortConfig(req.SortConfig, true)
		if err != nil {
			return none, fieldError("sort_config", err.Error())
		}
		input.SortConfig = &value
	}
	input.IsShared = req.IsShared
	if req.GroupID.Set() {
		groupID := req.GroupID.Value()
		if groupID != nil && strings.TrimSpace(*groupID) == "" {
			groupID = nil
		}
		if groupID != nil {
			if err := store.EnsureCollectionGroup(ctx, *groupID); err != nil {
				if errors.Is(err, userstore.ErrCollectionGroupNotFound) {
					return none, fieldError("group_id", "Collection group not found")
				}
				return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to validate collection group")
			}
		}
		input.GroupID = &groupID
	}

	// Merge only requested source members in the store UPDATE so concurrent
	// changes to independent settings cannot overwrite one another.
	if req.SourceURL != nil || req.MaxItems != nil || req.LibraryIDs != nil {
		patch := map[string]any{}
		if req.LibraryIDs != nil {
			if !catalog.IsSyncableType(existing.CollectionType) {
				return none, fieldError("library_ids", "library_ids can only be edited for imported collections")
			}
			if err := validateOptionalLibraryIDs(*req.LibraryIDs); err != nil {
				return none, err
			}
			patch["library_ids"] = *req.LibraryIDs
			emptyQuery := "{}"
			input.QueryDefinition = &emptyQuery
		}
		if req.MaxItems != nil {
			if *req.MaxItems < 0 {
				return none, fieldError("max_items", "max_items must be zero or positive")
			}
			if *req.MaxItems == 0 {
				patch["limit"] = nil
			} else {
				patch["limit"] = *req.MaxItems
			}
		}
		if req.SourceURL != nil {
			normalized, err := normalizeEditableSourceURL(existing, *req.SourceURL)
			if err != nil {
				return none, err
			}
			patch["url"] = normalized
			input.SourceURL = new(normalized)
		}
		raw, err := json.Marshal(patch)
		if err != nil {
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to encode source config")
		}
		input.SourceConfigPatch = new(string(raw))
	}

	if req.SyncSchedule != nil {
		if !catalog.IsSyncableType(existing.CollectionType) {
			return none, fieldError("sync_schedule", "sync_schedule can only be set on a synced list")
		}
		schedule, err := usercollections.ResolveSyncSchedule(*req.SyncSchedule)
		if err != nil {
			return none, fieldError("sync_schedule", err.Error())
		}
		if schedule == nil {
			input.ClearSyncSchedule, input.ClearNextSyncAt = true, true
		} else {
			input.SyncSchedule, input.NextSyncAt = schedule, usercollections.InitialNextSyncAt(schedule)
		}
	}

	if err := store.UpdateCollection(ctx, input); err != nil {
		if errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
			return none, err
		}
		if strings.Contains(err.Error(), "creator") {
			return none, apiError(http.StatusForbidden, "forbidden", "Only the creator can edit this collection")
		}
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to update collection")
	}

	posterSource := pointerStringValue(req.PosterSourceURL)
	if _, err := h.processCollectionPoster(ctx, store, collectionID, profileID, cmd.PosterFile, posterSource); err != nil {
		if errors.Is(err, errCollectionForbidden) {
			return none, apiError(http.StatusForbidden, "forbidden", "Only the creator can edit this collection")
		}
		return none, apiError(http.StatusInternalServerError, "internal_error", err.Error())
	}

	// Re-read the collection to return updated state.
	collection, err := store.GetCollection(ctx, collectionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			return none, apiError(http.StatusNotFound, "not_found", "Collection not found")
		}
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to retrieve updated collection")
	}
	h.refreshCollage(ctx, store, userID, collection)

	return h.collectionView(ctx, store, userID, profileID, *collection)
}

func (h *CollectionHandler) PreviewPersonalCollection(ctx context.Context, req PersonalCollectionPreviewRequest, filter catalog.AccessFilter) (PersonalCollectionPreviewView, error) {
	var none PersonalCollectionPreviewView
	if h.Executor == nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Preview is not configured")
	}

	var def catalog.QueryDefinition
	if len(req.QueryDefinition) > 0 {
		normalized, err := normalizeQueryDefinitionJSON(req.QueryDefinition, true, true, req.V1Rules)
		if err != nil {
			return none, fieldError("query_definition", "Invalid query_definition")
		}
		if err := json.Unmarshal(normalized, &def); err != nil {
			return none, fieldError("query_definition", "Invalid query_definition")
		}
	}

	items, total, err := h.Executor.Preview(ctx, def, filter, req.Limit)
	if err != nil {
		return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
	}

	var posters map[string]string
	if req.WithPosters {
		posters = h.previewPosterURLs(ctx, items)
	}
	resp := PersonalCollectionPreviewView{Items: make([]PersonalCollectionPreviewItemView, 0, len(items)), Total: total}
	for _, item := range items {
		resp.Items = append(resp.Items, PersonalCollectionPreviewItemView{
			ContentID: item.ContentID,
			Title:     item.Title,
			Type:      item.Type,
			PosterURL: posters[item.PosterPath],
		})
	}
	return resp, nil
}

// previewPosterURLs signs the card-size posters of the previewed items in one
// batch, keyed by poster path. Items without a poster are not looked up.
func (h *CollectionHandler) previewPosterURLs(ctx context.Context, items []*models.MediaItem) map[string]string {
	if h.ItemPosters == nil {
		return nil
	}
	paths := make([]string, 0, len(items))
	for _, item := range items {
		if item.PosterPath != "" {
			paths = append(paths, item.PosterPath)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return h.ItemPosters.PresignImageURLs(ctx, paths, "poster", "small")
}

func (h *CollectionHandler) DeletePersonalCollection(ctx context.Context, userID int, profileID, collectionID string) error {

	if collectionID == "" {
		return apiError(http.StatusBadRequest, "bad_request", "Collection ID is required")
	}

	store, _, err := h.personalCollectionStore(ctx, userID, profileID, collectionID, true)
	if err != nil {
		return err
	}

	if err := deleteCollectionWithRevision(ctx, store, collectionID); err != nil {
		if errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
			return err
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete collection")
	}

	return nil
}

// ListPersonalCollectionItems preserves the unpaged bridge response. V2 callers
// must use the bounded continuation service instead. It lists the stored
// members the reader can access: for another profile's collection, only those
// its owner can access too, as on every other read.
func (h *CollectionHandler) ListPersonalCollectionItems(ctx context.Context, userID int, profileID, collectionID string) (PersonalCollectionItemsView, error) {
	var none PersonalCollectionItemsView

	if collectionID == "" {
		return none, apiError(http.StatusBadRequest, "bad_request", "Collection ID is required")
	}

	store, collection, err := h.personalCollectionStore(ctx, userID, profileID, collectionID, false)
	if err != nil {
		return none, err
	}

	items, err := store.ListCollectionItems(ctx, collectionID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to list collection items")
	}
	items, err = h.accessibleCollectionItems(ctx, userID, profileID, collection.CreatorProfileID, items)
	if err != nil {
		return none, err
	}

	resp := PersonalCollectionItemsView{
		Items: make([]PersonalCollectionItemView, 0, len(items)),
	}
	for _, ci := range items {
		resp.Items = append(resp.Items, PersonalCollectionItemView{
			CollectionID: ci.CollectionID,
			MediaItemID:  ci.MediaItemID,
			Position:     ci.Position,
			AddedAt:      ci.AddedAt,
		})
	}

	return resp, nil
}

// accessibleCollectionItems keeps the stored members of ownerProfileID's
// collection that profileID may see (catalog.PersonalCollectionFilter), in
// their stored order. Hidden members stay stored.
func (h *CollectionHandler) accessibleCollectionItems(ctx context.Context, userID int, profileID, ownerProfileID string, items []userstore.CollectionItem) ([]userstore.CollectionItem, error) {
	if len(items) == 0 {
		return items, nil
	}
	filter, err := catalog.PersonalCollectionFilter(ctx, h.CollectionOwners, AccessFilterFromContext(ctx, ""), userID, profileID, ownerProfileID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list collection items")
	}
	reader, err := h.itemReader()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.MediaItemID)
	}
	visible, err := reader.GetByIDsWithAccess(ctx, ids, filter)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list collection items")
	}
	allowed := make(map[string]bool, len(visible))
	for _, item := range visible {
		if item != nil {
			allowed[item.ContentID] = true
		}
	}
	return slices.DeleteFunc(items, func(item userstore.CollectionItem) bool { return !allowed[item.MediaItemID] }), nil
}

func (h *CollectionHandler) AddPersonalCollectionItem(ctx context.Context, userID int, profileID, collectionID, itemID string, position int) error {

	if collectionID == "" || itemID == "" {
		return apiError(http.StatusBadRequest, "bad_request", "Collection ID and item ID are required")
	}

	store, collection, err := h.personalCollectionStore(ctx, userID, profileID, collectionID, true)
	if err != nil {
		return err
	}

	if collection.CollectionType != collectionManagementModeManual {
		return apiError(http.StatusConflict, "collection_not_manual", "Only manual collection membership can be edited")
	}
	if err := h.requireVisibleCollectionItem(ctx, itemID); err != nil {
		return err
	}

	if err := store.AddCollectionItem(ctx, collectionID, itemID, position); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to add item to collection")
	}
	h.refreshCollage(ctx, store, userID, collection)

	return nil
}

func (h *CollectionHandler) ReorderPersonalCollectionItems(ctx context.Context, userID int, profileID, collectionID string, orderedIDs []string) error {

	if collectionID == "" {
		return apiError(http.StatusBadRequest, "bad_request", "Collection ID is required")
	}

	store, collection, err := h.personalCollectionStore(ctx, userID, profileID, collectionID, true)
	if err != nil {
		return err
	}

	if collection.CollectionType != collectionManagementModeManual {
		return apiError(http.StatusConflict, "collection_not_manual", "Only manual collection membership can be edited")
	}

	if err := reorderCollectionItemsWithRevision(ctx, store, collectionID, orderedIDs); err != nil {
		if errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
			return err
		}
		return apiError(http.StatusBadRequest, "bad_request", err.Error())
	}
	h.refreshCollage(ctx, store, userID, collection)

	return nil
}

func (h *CollectionHandler) RemovePersonalCollectionItem(ctx context.Context, userID int, profileID, collectionID, itemID string) error {

	if collectionID == "" || itemID == "" {
		return apiError(http.StatusBadRequest, "bad_request", "Collection ID and item ID are required")
	}

	store, collection, err := h.personalCollectionStore(ctx, userID, profileID, collectionID, true)
	if err != nil {
		return err
	}

	if collection.CollectionType != collectionManagementModeManual {
		return apiError(http.StatusConflict, "collection_not_manual", "Only manual collection membership can be edited")
	}

	if err := store.RemoveCollectionItem(ctx, collectionID, itemID); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to remove item from collection")
	}
	h.refreshCollage(ctx, store, userID, collection)

	return nil
}

func (h *CollectionHandler) DeletePersonalCollectionImage(ctx context.Context, userID int, profileID, collectionID, imageType string) error {
	if collectionID == "" {
		return apiError(http.StatusBadRequest, "bad_request", "Collection ID is required")
	}
	if imageType != collectionImagePoster {
		return apiError(http.StatusBadRequest, "bad_request", `type must be "poster"`)
	}

	store, collection, err := h.personalCollectionStore(ctx, userID, profileID, collectionID, true)
	if err != nil {
		return err
	}
	if err := collectionFeatureError(store, "artwork"); err != nil {
		return err
	}

	if err := removeCollectionImageVariants(ctx, h.ArtworkStore, userCollectionImagePrefix, collectionID, imageType); err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to delete images")
	}
	empty := ""
	if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
		ID:               collectionID,
		RequestProfileID: profileID,
		PosterURL:        &empty,
		PosterThumbhash:  &empty,
	}); err != nil {
		if strings.Contains(err.Error(), "creator") {
			return apiError(http.StatusForbidden, "forbidden", "Only the creator can edit this collection")
		}
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to clear poster")
	}
	// The collection shows its collage again.
	collection.PosterURL = ""
	h.refreshCollage(ctx, store, userID, collection)
	return nil
}

// refreshCollage builds, in the background, the collage the acting profile
// sees for c when c has no uploaded or imported poster, so it is ready before
// the next read. Only the creator changes a collection, so the acting
// profile's filter is the owner's. Stores kept outside Postgres have no
// collage.
func (h *CollectionHandler) refreshCollage(ctx context.Context, store userstore.UserStore, userID int, c *userstore.Collection) {
	usercollections.RefreshCollage(h.Collages, store, userID, c, AccessFilterFromContext(ctx, ""))
}

// personalCollectionStore checks the account and profile before exposing or changing a collection.
func (h *CollectionHandler) personalCollectionStore(ctx context.Context, userID int, profileID, id string, mutate bool) (userstore.UserStore, *userstore.Collection, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, nil, apiError(500, "internal_error", "Failed to access user store")
	}
	c, err := store.GetCollection(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			return nil, nil, apiError(404, "not_found", "Collection not found")
		}
		return nil, nil, apiError(500, "internal_error", "Failed to load collection")
	}
	if c == nil || profileID == "" || !catalog.ProfileCanAccessCollection(c, profileID) {
		return nil, nil, apiError(404, "not_found", "Collection not found")
	}
	if mutate && c.CreatorProfileID != profileID {
		return nil, nil, apiError(403, "forbidden", "Only the creator can edit this collection")
	}
	return store, c, nil
}

// GetPersonalCollection returns one collection visible to the selected profile.
func (h *CollectionHandler) GetPersonalCollection(ctx context.Context, userID int, profileID, id string) (PersonalCollectionView, error) {
	store, c, err := h.personalCollectionStore(ctx, userID, profileID, id, false)
	if err != nil {
		return PersonalCollectionView{}, err
	}
	return h.collectionView(ctx, store, userID, profileID, *c)
}

// UploadPersonalCollectionPoster stores uploaded image bytes for a collection's
// creator. The transport must bound the request body before calling this service.
func (h *CollectionHandler) UploadPersonalCollectionPoster(ctx context.Context, userID int, profileID, collectionID string, data []byte) (PersonalCollectionView, error) {
	store, _, err := h.personalCollectionStore(ctx, userID, profileID, collectionID, true)
	if err != nil {
		return PersonalCollectionView{}, err
	}
	if err := collectionFeatureError(store, "artwork"); err != nil {
		return PersonalCollectionView{}, err
	}
	if len(data) == 0 {
		return PersonalCollectionView{}, fieldError("body", "Poster image is required")
	}
	if _, err := h.processCollectionPoster(ctx, store, collectionID, profileID, func() ([]byte, error) { return data, nil }, ""); err != nil {
		return PersonalCollectionView{}, collectionArtworkError(err, "Failed to store collection artwork")
	}
	return h.GetPersonalCollection(ctx, userID, profileID, collectionID)
}

// SetPersonalCollectionPosterSource fetches artwork outside guarded definition writes.
func (h *CollectionHandler) SetPersonalCollectionPosterSource(ctx context.Context, userID int, profileID, id, source string) (PersonalCollectionView, error) {
	store, _, err := h.personalCollectionStore(ctx, userID, profileID, id, true)
	if err != nil {
		return PersonalCollectionView{}, err
	}
	if err := collectionFeatureError(store, "artwork"); err != nil {
		return PersonalCollectionView{}, err
	}
	if strings.TrimSpace(source) == "" {
		return PersonalCollectionView{}, fieldError("source_url", "An image URL is required")
	}
	if _, err := h.processCollectionPoster(ctx, store, id, profileID, nil, source); err != nil {
		return PersonalCollectionView{}, collectionArtworkError(err, "Failed to store collection artwork")
	}
	return h.GetPersonalCollection(ctx, userID, profileID, id)
}

// normalizeEditableSourceURL validates a new source URL for the collections
// whose source is a user-supplied list: MDBList lists and TMDB lists.
func normalizeEditableSourceURL(existing *userstore.Collection, raw string) (string, error) {
	if existing.CollectionType == collectionTypeMDBList {
		normalized, err := usercollections.CanonicalMDBListURL(raw)
		if err != nil {
			return "", fieldError("source_url", "source_url must be an MDBList list (https://mdblist.com/lists/...)")
		}
		return normalized, nil
	}
	if cfg, err := usercollections.ParseSourceConfig(existing.SourceConfig); err == nil && cfg.Mode == usercollections.SourceModeTMDBList {
		normalized, err := collectionutil.CanonicalTMDBListURL(raw)
		if err != nil {
			return "", fieldError("source_url", "source_url must be a TMDB list (https://www.themoviedb.org/list/...)")
		}
		return normalized, nil
	}
	return "", fieldError("source_url", "source_url can only be edited for MDBList and TMDB list collections")
}
