package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/errgroup"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// CollectionHandler handles personal collection CRUD endpoints.
type CollectionHandler struct {
	storeProvider userstore.UserStoreProvider
	Executor      *catalog.QueryExecutor
	S3GP          *s3client.Client
	HTTPClient    *http.Client
	PresignTTL    time.Duration
}

// NewCollectionHandler creates a new CollectionHandler.
func NewCollectionHandler(provider userstore.UserStoreProvider) *CollectionHandler {
	return &CollectionHandler{storeProvider: provider}
}

// --- Request/Response types ---

type createCollectionRequest struct {
	Name                       string          `json:"name"`
	CollectionType             string          `json:"collection_type"`
	IsShared                   bool            `json:"is_shared"`
	AllowedProfileIDs          []string        `json:"allowed_profile_ids"`
	QueryDefinition            json.RawMessage `json:"query_definition"`
	SortConfig                 json.RawMessage `json:"sort_config"`
	IncludeInServerCollections bool            `json:"include_in_server_collections"`
	PosterSourceURL            string          `json:"poster_source_url"`
}

type updateCollectionRequest struct {
	Name                       *string                `json:"name"`
	Description                *string                `json:"description"`
	IsShared                   *bool                  `json:"is_shared"`
	AllowedProfileIDs          *[]string              `json:"allowed_profile_ids"`
	QueryDefinition            json.RawMessage        `json:"query_definition"`
	SortConfig                 json.RawMessage        `json:"sort_config"`
	SourceURL                  *string                `json:"source_url"`
	MaxItems                   *int                   `json:"max_items"`
	IncludeInServerCollections *bool                  `json:"include_in_server_collections"`
	PosterSourceURL            *string                `json:"poster_source_url"`
	GroupID                    optionalNullableString `json:"group_id"`
}

type collectionItemRequest struct {
	Position int `json:"position"`
}

type collectionResponse struct {
	ID                         string          `json:"id"`
	ProfileID                  string          `json:"profile_id"`
	CreatorProfileID           string          `json:"creator_profile_id"`
	Name                       string          `json:"name"`
	Description                string          `json:"description,omitempty"`
	CollectionType             string          `json:"collection_type"`
	IsShared                   bool            `json:"is_shared"`
	AllowedProfileIDs          []string        `json:"allowed_profile_ids"`
	QueryDefinition            json.RawMessage `json:"query_definition"`
	SortConfig                 json.RawMessage `json:"sort_config"`
	SortOrder                  int             `json:"sort_order"`
	GroupID                    *string         `json:"group_id"`
	SourceURL                  string          `json:"source_url,omitempty"`
	SourceConfig               json.RawMessage `json:"source_config,omitempty"`
	SyncSchedule               string          `json:"sync_schedule,omitempty"`
	NextSyncAt                 string          `json:"next_sync_at,omitempty"`
	LastSyncAt                 string          `json:"last_sync_at,omitempty"`
	LastSyncStatus             string          `json:"last_sync_status,omitempty"`
	LastSyncMessage            string          `json:"last_sync_message,omitempty"`
	ItemCount                  int             `json:"item_count"`
	IncludeInServerCollections bool            `json:"include_in_server_collections"`
	PosterURL                  string          `json:"poster_url,omitempty"`
	PosterThumbhash            string          `json:"poster_thumbhash,omitempty"`
	CreatedAt                  string          `json:"created_at"`
	UpdatedAt                  string          `json:"updated_at"`
}

type collectionListResponse struct {
	Collections []collectionResponse      `json:"collections"`
	Groups      []collectionGroupResponse `json:"groups"`
}

type collectionGroupResponse struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	DefaultSortMode string `json:"default_sort_mode"`
	SortOrder       int    `json:"sort_order"`
}

type createCollectionGroupRequest struct {
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	DefaultSortMode string `json:"default_sort_mode"`
}

type updateCollectionGroupRequest struct {
	Name            *string `json:"name"`
	Slug            *string `json:"slug"`
	DefaultSortMode *string `json:"default_sort_mode"`
}

type reorderCollectionGroupsRequest struct {
	OrderedIDs []string `json:"ordered_ids"`
}

type collectionItemResponse struct {
	CollectionID string `json:"collection_id"`
	MediaItemID  string `json:"media_item_id"`
	Position     int    `json:"position"`
	AddedAt      string `json:"added_at"`
}

type collectionItemsListResponse struct {
	Items []collectionItemResponse `json:"items"`
}

type previewCollectionRequest struct {
	QueryDefinition json.RawMessage `json:"query_definition"`
	Limit           int             `json:"limit"`
}

type previewCollectionResponse struct {
	Items []previewCollectionItemResponse `json:"items"`
	Total int                             `json:"total"`
}

type previewCollectionItemResponse struct {
	ContentID string `json:"content_id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
}

// --- Handler methods ---

// HandleListCollections handles GET /collections.
func (h *CollectionHandler) HandleListCollections(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	collectionsCh := make(chan []userstore.Collection, 1)
	groupsCh := make(chan []userstore.CollectionGroup, 1)
	eg, egCtx := errgroup.WithContext(r.Context())
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
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list collections")
		return
	}
	collections := <-collectionsCh
	groups := <-groupsCh

	resp := collectionListResponse{
		Collections: make([]collectionResponse, 0, len(collections)),
		Groups:      make([]collectionGroupResponse, 0, len(groups)),
	}
	for _, c := range collections {
		resp.Collections = append(resp.Collections, h.toCollectionResponse(r, c))
	}
	for _, g := range groups {
		resp.Groups = append(resp.Groups, collectionGroupResponse{
			ID:              g.ID,
			Name:            g.Name,
			Slug:            g.Slug,
			DefaultSortMode: string(g.DefaultSortMode),
			SortOrder:       g.SortOrder,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleCreateCollection handles POST /collections.
func (h *CollectionHandler) HandleCreateCollection(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req createCollectionRequest
	if err := decodeJSONOrMultipart(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Collection name is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	queryDefinitionJSON := defaultJSON(req.QueryDefinition)
	collectionType := firstNonEmptyCollection(req.CollectionType, "manual")
	if collectionType == "smart" {
		queryDefinitionJSON, err = normalizeSmartCollectionQueryDefinitionJSON(queryDefinitionJSON, true, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid query_definition")
			return
		}
	} else if len(req.QueryDefinition) > 0 {
		queryDefinitionJSON, err = normalizeQueryDefinitionJSON(queryDefinitionJSON, true, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid query_definition")
			return
		}
	}
	queryDefinition := string(queryDefinitionJSON)
	sortConfig := string(defaultJSON(req.SortConfig))
	collection, err := store.CreateCollection(r.Context(), userstore.CreateCollectionInput{
		CreatorProfileID:           profileID,
		Name:                       req.Name,
		CollectionType:             collectionType,
		IsShared:                   req.IsShared,
		AllowedProfileIDs:          req.AllowedProfileIDs,
		QueryDefinition:            queryDefinition,
		SortConfig:                 sortConfig,
		IncludeInServerCollections: req.IncludeInServerCollections,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create collection")
		return
	}

	if err := h.processCollectionPoster(r, store, collection.ID, profileID, req.PosterSourceURL); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if h.posterInputProvided(r, req.PosterSourceURL) {
		if refreshed, err := store.GetCollection(r.Context(), collection.ID); err == nil {
			collection = refreshed
		}
	}

	writeJSON(w, http.StatusCreated, h.toCollectionResponse(r, *collection))
}

// HandleUpdateCollection handles PUT /collections/{id}.
func (h *CollectionHandler) HandleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	collectionID := chi.URLParam(r, "id")

	if collectionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Collection ID is required")
		return
	}

	var req updateCollectionRequest
	if err := decodeJSONOrMultipart(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	profileID := apimw.GetProfileID(r.Context())
	input := userstore.UpdateCollectionInput{
		ID:                         collectionID,
		RequestProfileID:           profileID,
		Name:                       req.Name,
		Description:                req.Description,
		AllowedProfileIDs:          req.AllowedProfileIDs,
		IncludeInServerCollections: req.IncludeInServerCollections,
	}
	if len(req.QueryDefinition) > 0 {
		normalized, err := normalizeSmartCollectionQueryDefinitionJSON(req.QueryDefinition, true, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid query_definition")
			return
		}
		value := string(normalized)
		input.QueryDefinition = &value
	}
	if len(req.SortConfig) > 0 {
		value := string(req.SortConfig)
		input.SortConfig = &value
	}
	input.IsShared = req.IsShared
	if req.GroupID.Set() {
		groupID := req.GroupID.Value()
		if groupID != nil && strings.TrimSpace(*groupID) == "" {
			groupID = nil
		}
		if groupID != nil {
			if err := store.EnsureCollectionGroup(r.Context(), *groupID); err != nil {
				if errors.Is(err, userstore.ErrCollectionGroupNotFound) {
					writeError(w, http.StatusBadRequest, "bad_request", "Collection group not found")
					return
				}
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to validate collection group")
				return
			}
		}
		input.GroupID = &groupID
	}

	// Source URL and max items both live inside source_config (Limit / URL).
	// Load the existing collection and re-marshal so the unaffected fields
	// (preset, media_type, etc.) survive untouched.
	if req.SourceURL != nil || req.MaxItems != nil {
		existing, err := store.GetCollection(r.Context(), collectionID)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				writeError(w, http.StatusNotFound, "not_found", "Collection not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load collection")
			return
		}
		cfg, err := usercollections.ParseSourceConfig(existing.SourceConfig)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to parse source config")
			return
		}
		if req.MaxItems != nil {
			if *req.MaxItems < 0 {
				writeError(w, http.StatusBadRequest, "bad_request", "max_items must be zero or positive")
				return
			}
			if *req.MaxItems == 0 {
				cfg.Limit = nil
			} else {
				value := *req.MaxItems
				cfg.Limit = &value
			}
		}
		if req.SourceURL != nil {
			if existing.CollectionType != "mdblist" {
				writeError(w, http.StatusBadRequest, "bad_request", "source_url can only be edited for MDBList collections")
				return
			}
			normalized := usercollections.NormalizeMDBListURL(*req.SourceURL)
			if normalized == "" {
				writeError(w, http.StatusBadRequest, "bad_request", "source_url is required")
				return
			}
			cfg.URL = normalized
			topURL := normalized
			input.SourceURL = &topURL
		}
		raw, err := usercollections.MarshalSourceConfig(cfg)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to encode source config")
			return
		}
		input.SourceConfig = &raw
	}

	if err := store.UpdateCollection(r.Context(), input); err != nil {
		if strings.Contains(err.Error(), "creator") {
			writeError(w, http.StatusForbidden, "forbidden", "Only the creator can edit this collection")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update collection")
		return
	}

	posterSource := pointerStringValue(req.PosterSourceURL)
	if err := h.processCollectionPoster(r, store, collectionID, profileID, posterSource); err != nil {
		if errors.Is(err, errCollectionForbidden) {
			writeError(w, http.StatusForbidden, "forbidden", "Only the creator can edit this collection")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	// Re-read the collection to return updated state.
	collection, err := store.GetCollection(r.Context(), collectionID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, "not_found", "Collection not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve updated collection")
		return
	}

	writeJSON(w, http.StatusOK, h.toCollectionResponse(r, *collection))
}

func (h *CollectionHandler) HandlePreviewCollection(w http.ResponseWriter, r *http.Request) {
	if h.Executor == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Preview is not configured")
		return
	}

	var req previewCollectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	var def catalog.QueryDefinition
	if len(req.QueryDefinition) > 0 {
		normalized, err := normalizeQueryDefinitionJSON(req.QueryDefinition, true, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid query_definition")
			return
		}
		if err := json.Unmarshal(normalized, &def); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid query_definition")
			return
		}
	}

	items, total, err := h.Executor.Preview(r.Context(), def, requestAccessFilter(r), req.Limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	resp := previewCollectionResponse{Items: make([]previewCollectionItemResponse, 0, len(items)), Total: total}
	for _, item := range items {
		resp.Items = append(resp.Items, previewCollectionItemResponse{
			ContentID: item.ContentID,
			Title:     item.Title,
			Type:      item.Type,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleDeleteCollection handles DELETE /collections/{id}.
func (h *CollectionHandler) HandleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	collectionID := chi.URLParam(r, "id")

	if collectionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Collection ID is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.DeleteCollection(r.Context(), collectionID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete collection")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleListCollectionItems handles GET /collections/{id}/items.
func (h *CollectionHandler) HandleListCollectionItems(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	collectionID := chi.URLParam(r, "id")

	if collectionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Collection ID is required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	items, err := store.ListCollectionItems(r.Context(), collectionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list collection items")
		return
	}

	resp := collectionItemsListResponse{
		Items: make([]collectionItemResponse, 0, len(items)),
	}
	for _, ci := range items {
		resp.Items = append(resp.Items, collectionItemResponse{
			CollectionID: ci.CollectionID,
			MediaItemID:  ci.MediaItemID,
			Position:     ci.Position,
			AddedAt:      ci.AddedAt,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleAddCollectionItem handles PUT /collections/{id}/items/{item_id}.
func (h *CollectionHandler) HandleAddCollectionItem(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	collectionID := chi.URLParam(r, "id")
	itemID := chi.URLParam(r, "item_id")

	if collectionID == "" || itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Collection ID and item ID are required")
		return
	}

	var req collectionItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Default position to 0 if body is empty or invalid.
		req.Position = 0
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.AddCollectionItem(r.Context(), collectionID, itemID, req.Position); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to add item to collection")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type reorderRequest struct {
	OrderedIDs []string `json:"ordered_ids"`
	// GroupID scopes the reorder to one section. nil/absent targets Ungrouped.
	GroupID *string `json:"group_id,omitempty"`
}

// HandleReorderCollections handles PUT /collections/order.
// The body must contain every collection in scope; concurrent edits that
// would silently drop one are rejected.
func (h *CollectionHandler) HandleReorderCollections(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req reorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.ReorderCollections(r.Context(), profileID, req.GroupID, req.OrderedIDs); err != nil {
		if errors.Is(err, collectionutil.ErrOrderedIDsMismatch) {
			writeError(w, http.StatusBadRequest, "bad_request", "ordered_ids must include every visible collection in the group exactly once")
			return
		}
		if strings.Contains(err.Error(), "ordered_ids contains duplicates") {
			writeError(w, http.StatusBadRequest, "bad_request", "ordered_ids contains duplicates")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to reorder collections")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleCreateCollectionGroup handles POST /collections/groups.
func (h *CollectionHandler) HandleCreateCollectionGroup(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	var req createCollectionGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(req.Slug)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	sortMode := userstore.GroupSortMode(req.DefaultSortMode)
	group, err := store.CreateCollectionGroup(r.Context(), req.Name, req.Slug, sortMode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, collectionGroupResponse{
		ID:              group.ID,
		Name:            group.Name,
		Slug:            group.Slug,
		DefaultSortMode: string(group.DefaultSortMode),
		SortOrder:       group.SortOrder,
	})
}

// HandleUpdateCollectionGroup handles PUT /collections/groups/{id}.
func (h *CollectionHandler) HandleUpdateCollectionGroup(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	var req updateCollectionGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		req.Name = &name
		if name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name cannot be empty")
			return
		}
	}
	if req.Slug != nil {
		slug := strings.TrimSpace(*req.Slug)
		req.Slug = &slug
	}
	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	var sortMode *userstore.GroupSortMode
	if req.DefaultSortMode != nil {
		mode := userstore.GroupSortMode(*req.DefaultSortMode)
		sortMode = &mode
	}
	group, err := store.UpdateCollectionGroup(r.Context(), id, req.Name, req.Slug, sortMode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, collectionGroupResponse{
		ID:              group.ID,
		Name:            group.Name,
		Slug:            group.Slug,
		DefaultSortMode: string(group.DefaultSortMode),
		SortOrder:       group.SortOrder,
	})
}

// HandleDeleteCollectionGroup handles DELETE /collections/groups/{id}.
func (h *CollectionHandler) HandleDeleteCollectionGroup(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	if err := store.DeleteCollectionGroup(r.Context(), id); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleReorderCollectionGroups handles PUT /collections/groups/order.
func (h *CollectionHandler) HandleReorderCollectionGroups(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	var req reorderCollectionGroupsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	if err := store.ReorderCollectionGroups(r.Context(), req.OrderedIDs); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleReorderCollectionItems handles PUT /collections/{id}/items/order.
// The body must contain every item currently in the collection.
func (h *CollectionHandler) HandleReorderCollectionItems(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	collectionID := chi.URLParam(r, "id")

	if collectionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Collection ID is required")
		return
	}

	var req reorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.ReorderCollectionItems(r.Context(), collectionID, req.OrderedIDs); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleRemoveCollectionItem handles DELETE /collections/{id}/items/{item_id}.
func (h *CollectionHandler) HandleRemoveCollectionItem(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	collectionID := chi.URLParam(r, "id")
	itemID := chi.URLParam(r, "item_id")

	if collectionID == "" || itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Collection ID and item ID are required")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	if err := store.RemoveCollectionItem(r.Context(), collectionID, itemID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to remove item from collection")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Helpers ---

func toCollectionResponse(c userstore.Collection) collectionResponse {
	queryDefinition := defaultJSON([]byte(c.QueryDefinition))
	sortConfig := defaultJSON([]byte(c.SortConfig))
	resp := collectionResponse{
		ID:                         c.ID,
		ProfileID:                  c.ProfileID,
		CreatorProfileID:           c.CreatorProfileID,
		Name:                       c.Name,
		Description:                c.Description,
		CollectionType:             c.CollectionType,
		IsShared:                   c.IsShared,
		AllowedProfileIDs:          append([]string(nil), c.AllowedProfileIDs...),
		QueryDefinition:            queryDefinition,
		SortConfig:                 sortConfig,
		SortOrder:                  c.SortOrder,
		GroupID:                    c.GroupID,
		SourceURL:                  c.SourceURL,
		LastSyncStatus:             c.LastSyncStatus,
		LastSyncMessage:            c.LastSyncMessage,
		ItemCount:                  c.ItemCount,
		IncludeInServerCollections: c.IncludeInServerCollections,
		PosterURL:                  c.PosterURL,
		PosterThumbhash:            c.PosterThumbhash,
		CreatedAt:                  c.CreatedAt,
		UpdatedAt:                  c.UpdatedAt,
	}
	if c.SourceConfig != "" && c.SourceConfig != "{}" {
		resp.SourceConfig = json.RawMessage(c.SourceConfig)
	}
	if c.SyncSchedule != nil {
		resp.SyncSchedule = *c.SyncSchedule
	}
	if c.NextSyncAt != nil {
		resp.NextSyncAt = c.NextSyncAt.UTC().Format(time.RFC3339)
	}
	if c.LastSyncAt != nil {
		resp.LastSyncAt = c.LastSyncAt.UTC().Format(time.RFC3339)
	}
	return resp
}

func defaultJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func normalizeQueryDefinitionJSON(raw []byte, allowPersonalizedSorts, allowPersonalizedFields bool) (json.RawMessage, error) {
	var def catalog.QueryDefinition
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &def); err != nil {
			return nil, err
		}
	}
	def = def.Normalize()
	if err := def.ValidateWithOptions(allowPersonalizedSorts, allowPersonalizedFields); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func normalizeSmartCollectionQueryDefinitionJSON(raw []byte, allowPersonalizedSorts, allowPersonalizedFields bool) (json.RawMessage, error) {
	normalized, err := normalizeQueryDefinitionJSON(raw, allowPersonalizedSorts, allowPersonalizedFields)
	if err != nil {
		return nil, err
	}
	var def catalog.QueryDefinition
	if err := json.Unmarshal(normalized, &def); err != nil {
		return nil, err
	}
	def = catalog.ApplySmartCollectionItemLimit(def)
	out, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func firstNonEmptyCollection(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// errCollectionForbidden is returned by the artwork pipeline when the caller
// is not the collection's creator. It surfaces as a 403 to the client.
var errCollectionForbidden = errors.New("only the creator can edit this collection")

// HandleDeleteCollectionImage clears the poster on a personal collection.
// The query parameter "type" is required and currently only "poster" is
// supported.
func (h *CollectionHandler) HandleDeleteCollectionImage(w http.ResponseWriter, r *http.Request) {
	collectionID := chi.URLParam(r, "id")
	if collectionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Collection ID is required")
		return
	}
	imageType := r.URL.Query().Get("type")
	if imageType != "poster" {
		writeError(w, http.StatusBadRequest, "bad_request", `type must be "poster"`)
		return
	}

	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	collection, err := store.GetCollection(r.Context(), collectionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Collection not found")
		return
	}
	if collection.CreatorProfileID != profileID {
		writeError(w, http.StatusForbidden, "forbidden", "Only the creator can edit this collection")
		return
	}

	if err := removeCollectionImageVariants(r.Context(), h.S3GP, userCollectionImagePrefix, collectionID, imageType); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete images")
		return
	}
	empty := ""
	if err := store.UpdateCollection(r.Context(), userstore.UpdateCollectionInput{
		ID:               collectionID,
		RequestProfileID: profileID,
		PosterURL:        &empty,
		PosterThumbhash:  &empty,
	}); err != nil {
		if strings.Contains(err.Error(), "creator") {
			writeError(w, http.StatusForbidden, "forbidden", "Only the creator can edit this collection")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear poster")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// posterInputProvided reports whether the request body or form contained
// poster artwork inputs that the upload pipeline would act on.
func (h *CollectionHandler) posterInputProvided(r *http.Request, sourceURL string) bool {
	if strings.TrimSpace(sourceURL) != "" {
		return true
	}
	if r.MultipartForm == nil {
		return false
	}
	_, ok := r.MultipartForm.File["poster"]
	return ok
}

// processCollectionPoster persists a uploaded or sourced poster image on the
// given user collection. It is a no-op when no poster input was provided.
// The caller is responsible for ensuring the request profile owns the
// collection.
func (h *CollectionHandler) processCollectionPoster(
	r *http.Request,
	store userstore.UserStore,
	collectionID, requestProfileID, sourceURL string,
) error {
	source := strings.TrimSpace(sourceURL)
	isMultipart := strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/")

	var fileData []byte
	if isMultipart {
		data, err := readCollectionImageMultipart(r, "poster")
		switch {
		case err == nil:
			fileData = data
		case err == http.ErrMissingFile:
			// fall through to source URL handling
		default:
			return fmt.Errorf("poster: %w", err)
		}
	}
	if fileData == nil {
		if source == "" {
			return nil
		}
		downloaded, err := downloadCollectionImageURL(r.Context(), h.HTTPClient, source)
		if err != nil {
			return fmt.Errorf("poster source: %w", err)
		}
		fileData = downloaded
	}

	if h.S3GP == nil {
		return fmt.Errorf("poster upload requires configured object storage")
	}
	if err := removeCollectionImageVariants(r.Context(), h.S3GP, userCollectionImagePrefix, collectionID, "poster"); err != nil {
		return fmt.Errorf("clearing previous poster: %w", err)
	}
	s3Path, thumbhash, err := uploadCollectionImageVariants(r.Context(), h.S3GP, userCollectionImagePrefix, collectionID, "poster", fileData)
	if err != nil {
		return fmt.Errorf("poster: %w", err)
	}

	if err := store.UpdateCollection(r.Context(), userstore.UpdateCollectionInput{
		ID:               collectionID,
		RequestProfileID: requestProfileID,
		PosterURL:        &s3Path,
		PosterThumbhash:  &thumbhash,
	}); err != nil {
		if strings.Contains(err.Error(), "creator") {
			return errCollectionForbidden
		}
		return fmt.Errorf("persisting poster: %w", err)
	}
	return nil
}

// presignUserCollectionPoster returns a presigned URL for the card-sized
// variant of the stored poster, mirroring the admin pipeline. Empty paths
// return "".
func (h *CollectionHandler) presignUserCollectionPoster(ctx context.Context, path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	if h.S3GP == nil {
		return ""
	}
	ttl := h.PresignTTL
	if ttl <= 0 {
		ttl = 4 * time.Hour
	}
	url, err := h.S3GP.PresignGetURL(ctx, h.S3GP.Bucket(), cardThumbnailPath(path), ttl)
	if err != nil {
		return ""
	}
	return url
}

// toCollectionResponse mirrors the package-level helper but presigns artwork
// URLs using the handler's S3 client. Use this method when serving HTTP
// responses; the package-level function is reserved for callers without a
// presign capability.
func (h *CollectionHandler) toCollectionResponse(r *http.Request, c userstore.Collection) collectionResponse {
	resp := toCollectionResponse(c)
	resp.PosterURL = h.presignUserCollectionPoster(r.Context(), c.PosterURL)
	return resp
}
