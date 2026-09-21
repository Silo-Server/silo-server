package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// UserCollectionImportHandler exposes the user-side template gallery and
// import + sync endpoints. Authorization rules (only the creator can sync) are
// enforced here; the underlying sync.Service is intentionally unauthenticated.
type UserCollectionImportHandler struct {
	storeProvider   userstore.UserStoreProvider
	sync            *usercollections.Service
	scheduler       *usercollections.Scheduler
	registry        *templates.Registry
	mdblist         *mdblist.Client
	ArtworkStore    artworkstore.Store
	ArtworkResolver artworkurl.Resolver
	frontendFS      fs.FS
}

func NewUserCollectionImportHandler(
	provider userstore.UserStoreProvider,
	sync *usercollections.Service,
	scheduler *usercollections.Scheduler,
	registry *templates.Registry,
	mdblistClient *mdblist.Client,
	frontendFS fs.FS,
) *UserCollectionImportHandler {
	if registry == nil {
		registry = templates.Default
	}
	return &UserCollectionImportHandler{
		storeProvider: provider,
		sync:          sync,
		scheduler:     scheduler,
		registry:      registry,
		mdblist:       mdblistClient,
		frontendFS:    frontendFS,
	}
}

func (h *UserCollectionImportHandler) HandleListTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.registry.Catalog())
}

type UserImportSharedFields struct {
	Title                  string          `json:"title"`
	Description            string          `json:"description"`
	Limit                  *int            `json:"limit,omitempty"`
	SyncSchedule           string          `json:"sync_schedule"`
	IsShared               bool            `json:"is_shared"`
	PosterURL              string          `json:"poster_url"`
	LibraryIDs             []int           `json:"library_ids,omitempty"`
	DisplayQueryDefinition json.RawMessage `json:"display_query_definition,omitempty"`
	SortConfig             json.RawMessage `json:"sort_config,omitempty"`
}

type UserImportMDBListRequest struct {
	UserImportSharedFields
	URL string `json:"url"`
}

type UserImportTMDBRequest struct {
	UserImportSharedFields
	Preset     string `json:"preset"`
	MediaType  string `json:"media_type"`
	TimeWindow string `json:"time_window"`
}

type UserImportTraktRequest struct {
	UserImportSharedFields
	Preset    string `json:"preset"`
	MediaType string `json:"media_type"`
}

type UserImportView struct {
	Collection PersonalCollectionView      `json:"collection"`
	Sync       *usercollections.SyncResult `json:"sync,omitempty"`
}

func (h *UserCollectionImportHandler) HandleImportMDBList(w http.ResponseWriter, r *http.Request) {
	var req UserImportMDBListRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	resp, err := h.ImportMDBList(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (h *UserCollectionImportHandler) HandleImportTMDB(w http.ResponseWriter, r *http.Request) {
	var req UserImportTMDBRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	resp, err := h.ImportTMDB(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (h *UserCollectionImportHandler) HandleImportTrakt(w http.ResponseWriter, r *http.Request) {
	var req UserImportTraktRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	resp, err := h.ImportTrakt(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// ImportMDBList creates an MDBList-backed collection for the profile and
// runs its first sync. v1 POST /collections/import/mdblist and v2
// importMDBListCollection both call it; a failure is an *APIError.
func (h *UserCollectionImportHandler) ImportMDBList(ctx context.Context, userID int, profileID string, req UserImportMDBListRequest) (UserImportView, error) {
	var none UserImportView
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.URL) == "" {
		return none, apiError(http.StatusBadRequest, policyErrorBadRequest, "title and url are required")
	}
	canonicalURL, err := usercollections.CanonicalMDBListURL(req.URL)
	if err != nil {
		return none, fieldError("url", "url must be an MDBList list (https://mdblist.com/lists/...)")
	}
	if err := validateOptionalLimit(req.Limit); err != nil {
		return none, err
	}
	cfg := usercollections.SourceConfig{
		Mode:       usercollections.SourceModeMDBList,
		URL:        canonicalURL,
		Limit:      req.Limit,
		LibraryIDs: req.LibraryIDs,
	}
	return h.createImportedCollection(ctx, userID, profileID, "mdblist", cfg, req.UserImportSharedFields)
}

// ImportTMDB creates a TMDB-preset collection for the profile and runs its
// first sync.
func (h *UserCollectionImportHandler) ImportTMDB(ctx context.Context, userID int, profileID string, req UserImportTMDBRequest) (UserImportView, error) {
	var none UserImportView
	if strings.TrimSpace(req.Title) == "" {
		return none, fieldError("title", "title is required")
	}
	preset, mediaType, timeWindow, err := normalizeTMDBPresetRequest(req.Preset, req.MediaType, req.TimeWindow)
	if err != nil {
		return none, apiError(http.StatusBadRequest, policyErrorBadRequest, err.Error())
	}
	if err := validateOptionalLimit(req.Limit); err != nil {
		return none, err
	}
	cfg := usercollections.SourceConfig{
		Mode:       usercollections.SourceModeTMDBPreset,
		Preset:     preset,
		MediaType:  mediaType,
		TimeWindow: timeWindow,
		Limit:      req.Limit,
		LibraryIDs: req.LibraryIDs,
	}
	return h.createImportedCollection(ctx, userID, profileID, "tmdb", cfg, req.UserImportSharedFields)
}

// ImportTrakt creates a Trakt-preset collection for the profile and runs its
// first sync.
func (h *UserCollectionImportHandler) ImportTrakt(ctx context.Context, userID int, profileID string, req UserImportTraktRequest) (UserImportView, error) {
	var none UserImportView
	return none, apiError(http.StatusGone, "unsupported_source", "new Trakt collections are not supported")
	/* Legacy implementation retained below so existing Trakt collections can
	continue to sync while creation is disabled.
	if strings.TrimSpace(req.Title) == "" {
		return none, fieldError("title", "title is required")
	}
	preset, mediaType, normalizedProfileID, err := normalizeTraktPresetRequest(req.Preset, req.MediaType, profileID)
	if err != nil {
		return none, apiError(http.StatusBadRequest, policyErrorBadRequest, err.Error())
	}
	if err := validateOptionalLimit(req.Limit); err != nil {
		return none, err
	}
	cfg := usercollections.SourceConfig{
		Mode:       usercollections.SourceModeTraktPreset,
		Provider:   "trakt",
		Preset:     preset,
		MediaType:  mediaType,
		ProfileID:  normalizedProfileID,
		Limit:      req.Limit,
		LibraryIDs: req.LibraryIDs,
	}
	return h.createImportedCollection(ctx, userID, profileID, "trakt", cfg, req.UserImportSharedFields)
	*/
}

func (h *UserCollectionImportHandler) createImportedCollection(
	ctx context.Context,
	userID int,
	profileID string,
	collectionType string,
	cfg usercollections.SourceConfig,
	shared UserImportSharedFields,
) (UserImportView, error) {
	var none UserImportView
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := collectionFeatureError(store, "imports"); err != nil {
		return UserImportView{}, err
	}

	schedule, err := usercollections.ResolveSyncSchedule(shared.SyncSchedule)
	if err != nil {
		return none, fieldError("sync_schedule", err.Error())
	}
	if err := validateOptionalLibraryIDs(cfg.LibraryIDs); err != nil {
		return none, err
	}

	sourceConfigJSON, err := usercollections.MarshalSourceConfig(cfg)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to encode source config")
	}
	displayQueryDefinition, err := catalog.NormalizeDisplayQueryFragment(shared.DisplayQueryDefinition)
	if err != nil {
		return none, fieldError("display_query_definition", err.Error())
	}
	sortConfig, err := NormalizeCollectionSortConfig(shared.SortConfig, true)
	if err != nil {
		return none, fieldError("sort_config", err.Error())
	}

	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID:       profileID,
		Name:                   strings.TrimSpace(shared.Title),
		Description:            strings.TrimSpace(shared.Description),
		CollectionType:         collectionType,
		IsShared:               shared.IsShared,
		QueryDefinition:        "{}",
		SortConfig:             sortConfig,
		SourceURL:              cfg.DisplayURL(),
		SourceConfig:           sourceConfigJSON,
		SyncSchedule:           schedule,
		NextSyncAt:             usercollections.InitialNextSyncAt(schedule),
		DisplayQueryDefinition: displayQueryDefinition,
		PosterURL:              strings.TrimSpace(shared.PosterURL),
	})
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to create collection")
	}

	if err := h.storeBundledTemplatePoster(ctx, store, collection, strings.TrimSpace(shared.PosterURL)); err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to process template poster")
	}

	syncResult, updated, syncErr := h.sync.RunSync(ctx, store, collection)
	if syncErr != nil {
		// Persist failure state inline so the UI shows the error and the user
		// can retry; the row is intentionally kept around for that retry path.
		_ = store.UpdateCollectionSyncState(ctx, userstore.UpdateCollectionSyncStateInput{
			ID:         collection.ID,
			Status:     "failed",
			Message:    syncErr.Error(),
			LastSyncAt: time.Now().UTC(),
			NextSyncAt: usercollections.InitialNextSyncAt(schedule),
		})
		updated = collection
		updated.LastSyncStatus = "failed"
		updated.LastSyncMessage = syncErr.Error()
	}

	return UserImportView{
		Collection: h.collectionView(ctx, *updated),
		Sync:       syncResult,
	}, nil
}

func (h *UserCollectionImportHandler) storeBundledTemplatePoster(
	ctx context.Context,
	store userstore.UserStore,
	collection *userstore.Collection,
	posterPath string,
) error {
	if collection == nil {
		return nil
	}
	storedPath, thumbhash, stored, err := storeBundledCollectionPosterIfS3Configured(
		ctx,
		h.artworkBackend(),
		h.frontendFS,
		collection.ID,
		userCollectionImagePrefix,
		posterPath,
	)
	if err != nil || !stored {
		if err != nil {
			slog.WarnContext(ctx, "failed to store bundled user collection poster", "component", "api",
				"collection_id", collection.ID,
				"poster_path", posterPath,
				"error", err,
			)
		}
		return nil
	}

	if err := store.UpdateCollection(ctx, userstore.UpdateCollectionInput{
		ID:               collection.ID,
		RequestProfileID: collection.CreatorProfileID,
		PosterURL:        &storedPath,
		PosterThumbhash:  &thumbhash,
	}); err != nil {
		slog.WarnContext(ctx, "failed to persist bundled user collection poster", "component", "api",
			"collection_id", collection.ID,
			"poster_path", posterPath,
			"stored_path", storedPath,
			"error", err,
		)
		return nil
	}
	collection.PosterURL = storedPath
	collection.PosterThumbhash = thumbhash
	return nil
}

func (h *UserCollectionImportHandler) artworkBackend() artworkstore.Store {
	return h.ArtworkStore
}

// collectionView renders a stored collection with its poster presigned.
func (h *UserCollectionImportHandler) collectionView(ctx context.Context, c userstore.Collection) PersonalCollectionView {
	resp := toCollectionResponse(c)
	resp.PosterURL = h.presignCollectionPoster(ctx, c.PosterURL)
	return resp
}

func (h *UserCollectionImportHandler) presignCollectionPoster(ctx context.Context, path string) string {
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

func (h *UserCollectionImportHandler) HandleSync(w http.ResponseWriter, r *http.Request) {
	result, err := h.SyncPersonalCollection(r.Context(), apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type MDBListDiscoveryView struct {
	Configured bool                  `json:"configured"`
	Lists      []mdblist.ListSummary `json:"lists"`
}

// mdblistConfigured returns true when the discovery client is usable. When
// false it has already written a "not configured" 200 response so callers
// can simply early-return.
// SearchMDBList answers the MDBList lists matching query. An unconfigured
// MDBList client answers configured=false and no lists rather than an error,
// so clients can hide the search box. v1 GET /collections/import/mdblist/search
// and v2 searchMDBListLists both call it.
func (h *UserCollectionImportHandler) SearchMDBList(ctx context.Context, query string) (MDBListDiscoveryView, error) {
	if h.mdblist == nil || !h.mdblist.Configured() {
		return MDBListDiscoveryView{Configured: false, Lists: []mdblist.ListSummary{}}, nil
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return MDBListDiscoveryView{}, fieldError("q", "q is required")
	}
	lists, err := h.mdblist.Search(ctx, query)
	if err != nil {
		return MDBListDiscoveryView{}, apiError(http.StatusBadGateway, "upstream_error", fmt.Sprintf("MDBList search failed: %v", err))
	}
	return MDBListDiscoveryView{Configured: true, Lists: lists}, nil
}

// TopMDBList answers MDBList's most-liked lists; see SearchMDBList for the
// unconfigured answer.
func (h *UserCollectionImportHandler) TopMDBList(ctx context.Context) (MDBListDiscoveryView, error) {
	if h.mdblist == nil || !h.mdblist.Configured() {
		return MDBListDiscoveryView{Configured: false, Lists: []mdblist.ListSummary{}}, nil
	}
	lists, err := h.mdblist.Top(ctx)
	if err != nil {
		return MDBListDiscoveryView{}, apiError(http.StatusBadGateway, "upstream_error", fmt.Sprintf("MDBList top failed: %v", err))
	}
	return MDBListDiscoveryView{Configured: true, Lists: lists}, nil
}

func (h *UserCollectionImportHandler) HandleSearchMDBList(w http.ResponseWriter, r *http.Request) {
	resp, err := h.SearchMDBList(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *UserCollectionImportHandler) HandleTopMDBList(w http.ResponseWriter, r *http.Request) {
	resp, err := h.TopMDBList(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func validateOptionalLimit(limit *int) error {
	if limit == nil {
		return nil
	}
	if *limit <= 0 || *limit > collectionutil.MaxExplicitItemLimit {
		return fieldError("limit", fmt.Sprintf("limit must be between 1 and %d", collectionutil.MaxExplicitItemLimit))
	}
	return nil
}

func validateOptionalLibraryIDs(libraryIDs []int) error {
	for _, id := range libraryIDs {
		if id <= 0 {
			return fieldError("library_ids", "library_ids must contain positive IDs")
		}
	}
	return nil
}

func (h *UserCollectionImportHandler) SyncPersonalCollection(ctx context.Context, userID int, profileID, collectionID string) (*usercollections.SyncResult, error) {
	if h.sync == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Collection sync is unavailable")
	}
	if collectionID == "" {
		return nil, apiError(http.StatusBadRequest, "bad_request", "Collection ID is required")
	}

	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	if err := collectionFeatureError(store, "imports"); err != nil {
		return nil, err
	}
	collection, err := store.GetCollection(ctx, collectionID)
	if err != nil {
		return nil, apiError(http.StatusNotFound, "not_found", "Collection not found")
	}
	if collection.CreatorProfileID != profileID {
		return nil, apiError(http.StatusForbidden, "forbidden", "Only the creator can sync this collection")
	}
	if h.scheduler != nil && h.scheduler.IsInFlight(collectionID) {
		return nil, apiError(http.StatusConflict, "sync_in_flight", "A sync is already running for this collection")
	}

	result, _, err := h.sync.RunSync(ctx, store, collection)
	if err != nil {
		if errors.Is(err, usercollections.ErrSyncUnsupported) {
			return nil, apiError(http.StatusBadRequest, "bad_request", "This collection does not support sync")
		}
		return nil, apiError(http.StatusInternalServerError, "internal_error", fmt.Sprintf("Sync failed: %v", err))
	}
	return result, nil
}

func (h *UserCollectionImportHandler) CollectionTemplates() templates.Catalog {
	return h.registry.Catalog()
}
