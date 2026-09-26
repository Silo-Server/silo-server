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

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// CollectionHandler handles personal collection CRUD endpoints.
type CollectionHandler struct {
	storeProvider      userstore.UserStoreProvider
	LibraryCollections collectionPreferenceLibraryReader
	Executor           *catalog.QueryExecutor
	ItemReader         collectionMutationItemReader
	ArtworkStore       blobstore.Store
	ArtworkResolver    artworkurl.Resolver
	HTTPClient         *http.Client
}

// NewCollectionHandler creates a new CollectionHandler.
func NewCollectionHandler(provider userstore.UserStoreProvider) *CollectionHandler {
	return &CollectionHandler{storeProvider: provider}
}

// --- Request/Response types ---

type PersonalCollectionCreateRequest struct {
	Name                       string          `json:"name"`
	CollectionType             string          `json:"collection_type"`
	IsShared                   bool            `json:"is_shared"`
	AllowedProfileIDs          []string        `json:"allowed_profile_ids"`
	QueryDefinition            json.RawMessage `json:"query_definition"`
	SortConfig                 json.RawMessage `json:"sort_config"`
	DisplayQueryDefinition     json.RawMessage `json:"display_query_definition"`
	IncludeInServerCollections bool            `json:"include_in_server_collections"`
	PosterSourceURL            string          `json:"poster_source_url"`
}

type PersonalCollectionUpdateRequest struct {
	Name                       *string                `json:"name"`
	Description                *string                `json:"description"`
	IsShared                   *bool                  `json:"is_shared"`
	AllowedProfileIDs          *[]string              `json:"allowed_profile_ids"`
	QueryDefinition            json.RawMessage        `json:"query_definition"`
	SortConfig                 json.RawMessage        `json:"sort_config"`
	SourceURL                  *string                `json:"source_url"`
	MaxItems                   *int                   `json:"max_items"`
	LibraryIDs                 *[]int                 `json:"library_ids"`
	DisplayQueryDefinition     json.RawMessage        `json:"display_query_definition"`
	IncludeInServerCollections *bool                  `json:"include_in_server_collections"`
	PosterSourceURL            *string                `json:"poster_source_url"`
	GroupID                    optionalNullableString `json:"group_id"`
}

type collectionItemRequest struct {
	Position int `json:"position"`
}

type PersonalCollectionView struct {
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
	DisplayQueryDefinition     json.RawMessage `json:"display_query_definition,omitempty"`
	ItemCount                  int             `json:"item_count"`
	IncludeInServerCollections bool            `json:"include_in_server_collections"`
	PosterURL                  string          `json:"poster_url,omitempty"`
	PosterThumbhash            string          `json:"poster_thumbhash,omitempty"`
	CreatedAt                  string          `json:"created_at"`
	UpdatedAt                  string          `json:"updated_at"`
}

type PersonalCollectionListView struct {
	Collections []PersonalCollectionView `json:"collections"`
	Groups      []CollectionGroupView    `json:"groups"`
}

type CollectionCapabilitiesView struct {
	// DisplayFilterFields are the catalog query fields a personal-collection
	// display filter may use. Clients build a display_query_definition fragment
	// from these rather than a bespoke enum.
	DisplayFilterFields       []string                           `json:"display_filter_fields"`
	DisplayFilterPresets      CollectionDisplayFilterPresetsView `json:"display_filter_presets"`
	CollectionDefaultSort     bool                               `json:"collection_default_sort"`
	CollectionSortPreferences bool                               `json:"collection_sort_preferences"`
	EffectiveCollectionSort   bool                               `json:"effective_collection_sort"`
	// SortPreferenceKinds are the collection_kind values this server accepts on
	// the sort-preference endpoints. CollectionSortPreferences alone cannot
	// distinguish a server that also stores the personal-list kinds
	// ('watchlist', 'favorites') from one that rejects them.
	SortPreferenceKinds []string `json:"sort_preference_kinds"`
}

type CollectionDisplayFilterPresetsView struct {
	Watched []string `json:"watched"`
	Media   []string `json:"media"`
}

type CollectionGroupView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	DefaultSortMode string `json:"default_sort_mode"`
	SortOrder       int    `json:"sort_order"`
}

type CollectionGroupCreateRequest struct {
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	DefaultSortMode string `json:"default_sort_mode"`
}

type CollectionGroupUpdateRequest struct {
	Name            *string `json:"name"`
	Slug            *string `json:"slug"`
	DefaultSortMode *string `json:"default_sort_mode"`
}

type reorderCollectionGroupsRequest struct {
	OrderedIDs []string `json:"ordered_ids"`
}

type PersonalCollectionItemView struct {
	Title        string `json:"-"` // Catalog title for v2 membership; v1 stays unchanged.
	CollectionID string `json:"collection_id"`
	MediaItemID  string `json:"media_item_id"`
	Position     int    `json:"position"`
	AddedAt      string `json:"added_at"`
}

type PersonalCollectionItemsView struct {
	Items []PersonalCollectionItemView `json:"items"`
}

type PersonalCollectionPreviewRequest struct {
	QueryDefinition json.RawMessage `json:"query_definition"`
	Limit           int             `json:"limit"`
}

type PersonalCollectionPreviewView struct {
	Items []PersonalCollectionPreviewItemView `json:"items"`
	Total int                                 `json:"total"`
}

type PersonalCollectionPreviewItemView struct {
	ContentID string `json:"content_id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
}

// --- Handler methods ---

// HandleListCollections handles GET /collections.
func (h *CollectionHandler) HandleListCollections(w http.ResponseWriter, r *http.Request) {
	resp, err := h.ListPersonalCollections(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleCapabilities exposes additive feature support for collection clients.
func (h *CollectionHandler) HandleCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.Capabilities())
}

// HandleCreateCollection handles POST /collections.
func (h *CollectionHandler) HandleCreateCollection(w http.ResponseWriter, r *http.Request) {
	var req PersonalCollectionCreateRequest
	if err := decodeJSONOrMultipart(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	created, err := h.CreatePersonalCollection(r.Context(), PersonalCollectionCreateCommand{
		UserID:     apimw.GetUserID(r.Context()),
		ProfileID:  apimw.GetProfileID(r.Context()),
		Request:    req,
		PosterFile: posterFileReader(r),
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// HandleUpdateCollection handles PUT /collections/{id}.
func (h *CollectionHandler) HandleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	var req PersonalCollectionUpdateRequest
	if err := decodeJSONOrMultipart(r, &req); err != nil {
		writeError(w, 400, "bad_request", "Invalid request body")
		return
	}
	resp, err := h.UpdatePersonalCollection(r.Context(), PersonalCollectionUpdateCommand{UserID: apimw.GetUserID(r.Context()), ProfileID: apimw.GetProfileID(r.Context()), CollectionID: chi.URLParam(r, "id"), Request: req, PosterFile: posterFileReader(r)})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *CollectionHandler) HandlePreviewCollection(w http.ResponseWriter, r *http.Request) {
	var req PersonalCollectionPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad_request", "Invalid request body")
		return
	}
	resp, err := h.PreviewPersonalCollection(r.Context(), req, requestAccessFilter(r))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleDeleteCollection handles DELETE /collections/{id}.
func (h *CollectionHandler) HandleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	if err := h.DeletePersonalCollection(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), chi.URLParam(r, "id")); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleListCollectionItems handles GET /collections/{id}/items.
func (h *CollectionHandler) HandleListCollectionItems(w http.ResponseWriter, r *http.Request) {
	resp, err := h.ListPersonalCollectionItems(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleAddCollectionItem handles PUT /collections/{id}/items/{item_id}.
func (h *CollectionHandler) HandleAddCollectionItem(w http.ResponseWriter, r *http.Request) {
	var req collectionItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Position = 0
	}
	if err := h.AddPersonalCollectionItem(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "item_id"), req.Position); err != nil {
		writeAPIError(w, err)
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
	var req reorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := h.ReorderPersonalCollections(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), req.GroupID, req.OrderedIDs); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleCreateCollectionGroup handles POST /collections/groups.
func (h *CollectionHandler) HandleCreateCollectionGroup(w http.ResponseWriter, r *http.Request) {
	var req CollectionGroupCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	group, err := h.CreateCollectionGroup(r.Context(), apimw.GetUserID(r.Context()), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, group)
}

// HandleUpdateCollectionGroup handles PUT /collections/groups/{id}.
func (h *CollectionHandler) HandleUpdateCollectionGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	var req CollectionGroupUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	group, err := h.UpdateCollectionGroup(r.Context(), apimw.GetUserID(r.Context()), id, req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, group)
}

// HandleDeleteCollectionGroup handles DELETE /collections/groups/{id}.
func (h *CollectionHandler) HandleDeleteCollectionGroup(w http.ResponseWriter, r *http.Request) {
	if err := h.DeleteCollectionGroup(r.Context(), apimw.GetUserID(r.Context()), chi.URLParam(r, "id")); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleReorderCollectionGroups handles PUT /collections/groups/order.
func (h *CollectionHandler) HandleReorderCollectionGroups(w http.ResponseWriter, r *http.Request) {
	var req reorderCollectionGroupsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := h.ReorderCollectionGroups(r.Context(), apimw.GetUserID(r.Context()), req.OrderedIDs); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleReorderCollectionItems handles PUT /collections/{id}/items/order.
// The body must contain every item currently in the collection.
func (h *CollectionHandler) HandleReorderCollectionItems(w http.ResponseWriter, r *http.Request) {
	var req reorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "bad_request", "Invalid request body")
		return
	}
	if err := h.ReorderPersonalCollectionItems(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), chi.URLParam(r, "id"), req.OrderedIDs); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleRemoveCollectionItem handles DELETE /collections/{id}/items/{item_id}.
func (h *CollectionHandler) HandleRemoveCollectionItem(w http.ResponseWriter, r *http.Request) {
	if err := h.RemovePersonalCollectionItem(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "item_id")); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Helpers ---

func toCollectionResponse(c userstore.Collection) PersonalCollectionView {
	queryDefinition := defaultJSON([]byte(c.QueryDefinition))
	sortConfig := defaultJSON([]byte(c.SortConfig))
	resp := PersonalCollectionView{
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
	if strings.TrimSpace(c.DisplayQueryDefinition) != "" {
		resp.DisplayQueryDefinition = json.RawMessage(c.DisplayQueryDefinition)
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
	if err := h.DeletePersonalCollectionImage(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), chi.URLParam(r, "id"), r.URL.Query().Get("type")); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// posterInputProvided reports whether the request body or form contained
// poster artwork inputs that the upload pipeline would act on.
// processCollectionPoster persists an uploaded or sourced poster image on the
// given user collection. posterFile reads the uploaded part (nil when the
// request carried no multipart body; http.ErrMissingFile when the part is
// absent). It is a no-op, answering false, when no poster input was provided.
// The caller is responsible for ensuring the request profile owns the
// collection.
func (h *CollectionHandler) processCollectionPoster(
	ctx context.Context,
	store userstore.UserStore,
	collectionID, requestProfileID string,
	posterFile func() ([]byte, error),
	sourceURL string,
) (bool, error) {
	source := strings.TrimSpace(sourceURL)

	var fileData []byte
	if posterFile != nil {
		data, err := posterFile()
		switch {
		case err == nil:
			fileData = data
		case errors.Is(err, http.ErrMissingFile):
			// fall through to source URL handling
		default:
			return true, fmt.Errorf("poster: %w", err)
		}
	}
	if fileData == nil {
		if source == "" {
			return false, nil
		}
		downloaded, err := downloadCollectionImageURL(ctx, h.HTTPClient, source)
		if err != nil {
			return true, fmt.Errorf("poster source: %w", err)
		}
		fileData = downloaded
	}

	artwork := h.ArtworkStore
	if artwork == nil {
		return true, fmt.Errorf("poster upload requires configured artwork storage")
	}
	if err := removeCollectionImageVariants(ctx, artwork, userCollectionImagePrefix, collectionID, "poster"); err != nil {
		return true, fmt.Errorf("clearing previous poster: %w", err)
	}
	s3Path, thumbhash, err := uploadCollectionImageVariants(ctx, artwork, userCollectionImagePrefix, collectionID, "poster", fileData)
	if err != nil {
		return true, fmt.Errorf("poster: %w", err)
	}

	if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
		ID:               collectionID,
		RequestProfileID: requestProfileID,
		PosterURL:        &s3Path,
		PosterThumbhash:  &thumbhash,
	}); err != nil {
		if strings.Contains(err.Error(), "creator") {
			return true, errCollectionForbidden
		}
		return true, fmt.Errorf("persisting poster: %w", err)
	}
	return true, nil
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
	if h.ArtworkResolver == nil {
		return ""
	}
	key := cardThumbnailPath(path)
	return h.ArtworkResolver.ResolveURLs(ctx, []string{key})[key].URL
}

// previewCollectionRequest is shared with the library collection bridge handler.
type previewCollectionRequest = PersonalCollectionPreviewRequest
