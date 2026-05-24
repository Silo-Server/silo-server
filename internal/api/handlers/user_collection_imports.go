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
	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// UserCollectionImportHandler exposes the user-side template gallery and
// import + sync endpoints. Authorization rules (only the creator can sync) are
// enforced here; the underlying sync.Service is intentionally unauthenticated.
type UserCollectionImportHandler struct {
	storeProvider userstore.UserStoreProvider
	sync          *usercollections.Service
	scheduler     *usercollections.Scheduler
	registry      *templates.Registry
	mdblist       *mdblist.Client
	s3GP          *s3client.Client
	frontendFS    fs.FS
	presignTTL    time.Duration
}

func NewUserCollectionImportHandler(
	provider userstore.UserStoreProvider,
	sync *usercollections.Service,
	scheduler *usercollections.Scheduler,
	registry *templates.Registry,
	mdblistClient *mdblist.Client,
	s3GP *s3client.Client,
	frontendFS fs.FS,
	presignTTL time.Duration,
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
		s3GP:          s3GP,
		frontendFS:    frontendFS,
		presignTTL:    presignTTL,
	}
}

func (h *UserCollectionImportHandler) HandleListTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.registry.Catalog())
}

type userImportSharedFields struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	Limit        *int   `json:"limit,omitempty"`
	SyncSchedule string `json:"sync_schedule"`
	IsShared     bool   `json:"is_shared"`
	PosterURL    string `json:"poster_url"`
	LibraryIDs   []int  `json:"library_ids,omitempty"`
}

type userImportMDBListRequest struct {
	userImportSharedFields
	URL string `json:"url"`
}

type userImportTMDBRequest struct {
	userImportSharedFields
	Preset     string `json:"preset"`
	MediaType  string `json:"media_type"`
	TimeWindow string `json:"time_window"`
}

type userImportTraktRequest struct {
	userImportSharedFields
	Preset    string `json:"preset"`
	MediaType string `json:"media_type"`
}

type userImportResponse struct {
	Collection collectionResponse          `json:"collection"`
	Sync       *usercollections.SyncResult `json:"sync,omitempty"`
}

func (h *UserCollectionImportHandler) HandleImportMDBList(w http.ResponseWriter, r *http.Request) {
	var req userImportMDBListRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.URL) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "title and url are required")
		return
	}
	if !validateOptionalLimit(req.Limit, w) {
		return
	}
	cfg := usercollections.SourceConfig{
		Mode:       usercollections.SourceModeMDBList,
		URL:        usercollections.NormalizeMDBListURL(req.URL),
		Limit:      req.Limit,
		LibraryIDs: req.LibraryIDs,
	}
	h.createImportedCollection(w, r, "mdblist", cfg, req.userImportSharedFields)
}

func (h *UserCollectionImportHandler) HandleImportTMDB(w http.ResponseWriter, r *http.Request) {
	var req userImportTMDBRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "title is required")
		return
	}
	preset, mediaType, timeWindow, err := normalizeTMDBPresetRequest(req.Preset, req.MediaType, req.TimeWindow)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if !validateOptionalLimit(req.Limit, w) {
		return
	}
	cfg := usercollections.SourceConfig{
		Mode:       usercollections.SourceModeTMDBPreset,
		Preset:     preset,
		MediaType:  mediaType,
		TimeWindow: timeWindow,
		Limit:      req.Limit,
		LibraryIDs: req.LibraryIDs,
	}
	h.createImportedCollection(w, r, "tmdb", cfg, req.userImportSharedFields)
}

func (h *UserCollectionImportHandler) HandleImportTrakt(w http.ResponseWriter, r *http.Request) {
	var req userImportTraktRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "title is required")
		return
	}
	profileID := apimw.GetProfileID(r.Context())
	preset, mediaType, normalizedProfileID, err := normalizeTraktPresetRequest(req.Preset, req.MediaType, profileID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if !validateOptionalLimit(req.Limit, w) {
		return
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
	h.createImportedCollection(w, r, "trakt", cfg, req.userImportSharedFields)
}

func (h *UserCollectionImportHandler) createImportedCollection(
	w http.ResponseWriter,
	r *http.Request,
	collectionType string,
	cfg usercollections.SourceConfig,
	shared userImportSharedFields,
) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	schedule, err := usercollections.ResolveSyncSchedule(shared.SyncSchedule)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	sourceConfigJSON, err := usercollections.MarshalSourceConfig(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to encode source config")
		return
	}

	collection, err := store.CreateCollection(r.Context(), userstore.CreateCollectionInput{
		CreatorProfileID: profileID,
		Name:             strings.TrimSpace(shared.Title),
		Description:      strings.TrimSpace(shared.Description),
		CollectionType:   collectionType,
		IsShared:         shared.IsShared,
		QueryDefinition:  "{}",
		SortConfig:       "{}",
		SourceURL:        cfg.DisplayURL(),
		SourceConfig:     sourceConfigJSON,
		SyncSchedule:     schedule,
		NextSyncAt:       usercollections.InitialNextSyncAt(schedule),
		PosterURL:        strings.TrimSpace(shared.PosterURL),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create collection")
		return
	}

	if err := h.storeBundledTemplatePoster(r, store, collection, strings.TrimSpace(shared.PosterURL)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to process template poster")
		return
	}

	syncResult, updated, syncErr := h.sync.RunSync(r.Context(), store, collection)
	if syncErr != nil {
		// Persist failure state inline so the UI shows the error and the user
		// can retry; the row is intentionally kept around for that retry path.
		_ = store.UpdateCollectionSyncState(r.Context(), userstore.UpdateCollectionSyncStateInput{
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

	writeJSON(w, http.StatusCreated, userImportResponse{
		Collection: h.toCollectionResponse(r, *updated),
		Sync:       syncResult,
	})
}

func (h *UserCollectionImportHandler) storeBundledTemplatePoster(
	r *http.Request,
	store userstore.UserStore,
	collection *userstore.Collection,
	posterPath string,
) error {
	if collection == nil {
		return nil
	}
	storedPath, thumbhash, stored, err := storeBundledCollectionPosterIfS3Configured(
		r.Context(),
		h.s3GP,
		h.frontendFS,
		collection.ID,
		userCollectionImagePrefix,
		posterPath,
	)
	if err != nil || !stored {
		if err != nil {
			slog.Warn("failed to store bundled user collection poster",
				"collection_id", collection.ID,
				"poster_path", posterPath,
				"error", err,
			)
		}
		return nil
	}

	if err := store.UpdateCollection(r.Context(), userstore.UpdateCollectionInput{
		ID:               collection.ID,
		RequestProfileID: collection.CreatorProfileID,
		PosterURL:        &storedPath,
		PosterThumbhash:  &thumbhash,
	}); err != nil {
		slog.Warn("failed to persist bundled user collection poster",
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

func (h *UserCollectionImportHandler) toCollectionResponse(r *http.Request, c userstore.Collection) collectionResponse {
	resp := toCollectionResponse(c)
	resp.PosterURL = h.presignCollectionPoster(r.Context(), c.PosterURL)
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
	if h.s3GP == nil {
		return ""
	}
	ttl := h.presignTTL
	if ttl <= 0 {
		ttl = 4 * time.Hour
	}
	url, err := h.s3GP.PresignGetURL(ctx, h.s3GP.Bucket(), cardThumbnailPath(path), ttl)
	if err != nil {
		return ""
	}
	return url
}

func (h *UserCollectionImportHandler) HandleSync(w http.ResponseWriter, r *http.Request) {
	collectionID := chi.URLParam(r, "id")
	if collectionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Collection ID is required")
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
		writeError(w, http.StatusForbidden, "forbidden", "Only the creator can sync this collection")
		return
	}
	if h.scheduler != nil && h.scheduler.IsInFlight(collectionID) {
		writeError(w, http.StatusConflict, "sync_in_flight", "A sync is already running for this collection")
		return
	}

	result, _, err := h.sync.RunSync(r.Context(), store, collection)
	if err != nil {
		if errors.Is(err, usercollections.ErrSyncUnsupported) {
			writeError(w, http.StatusBadRequest, "bad_request", "This collection does not support sync")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", fmt.Sprintf("Sync failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type mdblistDiscoveryResponse struct {
	Configured bool                  `json:"configured"`
	Lists      []mdblist.ListSummary `json:"lists"`
}

// mdblistConfigured returns true when the discovery client is usable. When
// false it has already written a "not configured" 200 response so callers
// can simply early-return.
func (h *UserCollectionImportHandler) mdblistConfigured(w http.ResponseWriter) bool {
	if h.mdblist != nil && h.mdblist.Configured() {
		return true
	}
	writeJSON(w, http.StatusOK, mdblistDiscoveryResponse{Configured: false, Lists: []mdblist.ListSummary{}})
	return false
}

func (h *UserCollectionImportHandler) HandleSearchMDBList(w http.ResponseWriter, r *http.Request) {
	if !h.mdblistConfigured(w) {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "q is required")
		return
	}
	lists, err := h.mdblist.Search(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("MDBList search failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, mdblistDiscoveryResponse{Configured: true, Lists: lists})
}

func (h *UserCollectionImportHandler) HandleTopMDBList(w http.ResponseWriter, r *http.Request) {
	if !h.mdblistConfigured(w) {
		return
	}
	lists, err := h.mdblist.Top(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("MDBList top failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, mdblistDiscoveryResponse{Configured: true, Lists: lists})
}

func validateOptionalLimit(limit *int, w http.ResponseWriter) bool {
	if limit == nil {
		return true
	}
	if *limit <= 0 || *limit > 200 {
		writeError(w, http.StatusBadRequest, "bad_request", "limit must be between 1 and 200")
		return false
	}
	return true
}
