package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/adminjob"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/rootcheck"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// LibraryHandler handles HTTP endpoints for library (media folder) management
// and scan triggering.
type LibraryHandler struct {
	folderRepo            *catalog.FolderRepository
	ingester              libraryIngester
	userRepo              *auth.UserRepository
	AccessGroups          access.GroupPolicyProvider // optional; resolves inherited library access when no scope is in context
	pool                  *pgxpool.Pool
	refresher             AdminMetadataRefresher
	chainCacheInvalidator interface{ InvalidateChainCache() }
	JobRepo               AdminJobCreator
	ChainRepo             *metadata.ChainRepository
	PluginInstallations   pluginInstallationLister
	SkippedRootRepo       *metadata.SkippedRootRepository
	StaleIDRepo           *metadata.StaleMediaIDRepository
	MovieMatchQueueRepo   libraryMovieMatchQueue
	SeriesMatchQueueRepo  librarySeriesMatchQueue
	RawMatchBacklogRepo   libraryRawMatchBacklog
	TVSeriesRootQueue     bool
	ScannedGroupRepo      *scanner.ScannedGroupRepository
	GroupOverrideRepo     *scanner.MediaGroupOverrideRepository
	ObservedLocationRepo  *scanner.ObservedLocationRepository
	SectionRepo           *sections.Repository
	StoreProvider         userstore.UserStoreProvider
	S3Meta                LibraryImageStore
	PresignTTL            time.Duration
	appCtx                context.Context
	EventBus              cache.EventBus
	EventsHub             *evt.Hub
	ScanRegistry          *evt.ScanRegistry
	ScanQueue             libraryScanQueuer
}

// LibraryImageStore provides S3 operations for library poster images.
type LibraryImageStore interface {
	PutObject(ctx context.Context, bucket, key string, data []byte) error
	DeleteObject(ctx context.Context, bucket, key string) error
	PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error)
	Bucket() string
}

// pluginInstallationLister provides access to plugin installations and capabilities
// for seeding default provider chains from manifest metadata.
type pluginInstallationLister interface {
	ListEnabled(ctx context.Context) ([]*plugins.Installation, error)
	ListCapabilities(ctx context.Context, installationID int) ([]*plugins.Capability, error)
}

type libraryIngester interface {
	IngestFolder(ctx context.Context, folder *models.MediaFolder) (*libraryingest.Result, error)
	IngestSubtree(ctx context.Context, folder *models.MediaFolder, subtreePath string) (*libraryingest.Result, error)
	IngestFile(ctx context.Context, folder *models.MediaFolder, filePath string) (*libraryingest.Result, error)
	CancelLibrary(folderID int) int
}

type libraryScanQueuer interface {
	EnqueueLibraryScan(ctx context.Context, folderID int, trigger string) (bool, error)
	EnqueueScan(ctx context.Context, folderID int, mode, path, trigger string) (bool, error)
	CancelAcceptedByLibrary(ctx context.Context, libraryID int) (int, error)
	CancelByLibrary(ctx context.Context, libraryID int) (int, error)
}

type libraryMovieMatchQueue interface {
	SyncForFolder(ctx context.Context, folderID int) error
	DeleteByFolder(ctx context.Context, folderID int) (int, error)
	CountStatesByFolder(ctx context.Context, folderID int) (pending int, parked int, err error)
	CountStatesByFolders(ctx context.Context, folderIDs []int) (map[int]metadata.MatchQueueStateCounts, error)
	ListByFolder(ctx context.Context, folderID int, limit int, offset int) ([]models.MovieMatchQueueEntry, int, error)
	RetryNowByFolder(ctx context.Context, folderID int) (int, error)
}

type librarySeriesMatchQueue interface {
	SyncForFolder(ctx context.Context, folderID int) error
	DeleteByFolder(ctx context.Context, folderID int) (int, error)
	CountStatesByFolder(ctx context.Context, folderID int) (pending int, parked int, err error)
	CountStatesByFolders(ctx context.Context, folderIDs []int) (map[int]metadata.MatchQueueStateCounts, error)
	ListByFolder(ctx context.Context, folderID int, limit int, offset int) ([]models.SeriesRootMatchQueueEntry, int, error)
	RetryNowByFolder(ctx context.Context, folderID int) (int, error)
}

type libraryRawMatchBacklog interface {
	CountUnmatchedMatchBacklogByFolder(ctx context.Context, folderID int, mode scanner.RawMatchBacklogMode) (int, error)
	CountUnmatchedMatchBacklogByFolders(ctx context.Context, folderIDs []int, mode scanner.RawMatchBacklogMode) (map[int]int, error)
	ListUnmatchedMatchBacklogByFolder(ctx context.Context, folderID int, mode scanner.RawMatchBacklogMode, limit int, offset int) ([]*models.MediaFile, int, error)
	SuppressUnmatchedMatchBacklogByFolder(ctx context.Context, folderID int, mode scanner.RawMatchBacklogMode) (int, error)
	RetryUnmatchedMatchBacklogByFolder(ctx context.Context, folderID int, mode scanner.RawMatchBacklogMode) (int, error)
}

// NewLibraryHandler creates a new LibraryHandler backed by the given folder
// repository and ingest executor. The ingester may be nil if scan endpoints are not needed.
func NewLibraryHandler(
	folderRepo *catalog.FolderRepository,
	ingester libraryIngester,
	userRepo *auth.UserRepository,
	pool *pgxpool.Pool,
	refresher AdminMetadataRefresher,
	appCtx ...context.Context,
) *LibraryHandler {
	ctx := context.Background()
	if len(appCtx) > 0 && appCtx[0] != nil {
		ctx = appCtx[0]
	}
	var scannedGroupRepo *scanner.ScannedGroupRepository
	var groupOverrideRepo *scanner.MediaGroupOverrideRepository
	var observedLocationRepo *scanner.ObservedLocationRepository
	if pool != nil {
		scannedGroupRepo = scanner.NewScannedGroupRepository(pool)
		groupOverrideRepo = scanner.NewMediaGroupOverrideRepository(pool)
		observedLocationRepo = scanner.NewObservedLocationRepository(pool)
	}
	return &LibraryHandler{
		folderRepo:           folderRepo,
		ingester:             ingester,
		userRepo:             userRepo,
		pool:                 pool,
		refresher:            refresher,
		ScannedGroupRepo:     scannedGroupRepo,
		GroupOverrideRepo:    groupOverrideRepo,
		ObservedLocationRepo: observedLocationRepo,
		appCtx:               ctx,
	}
}

func (h *LibraryHandler) SetChainCacheInvalidator(invalidator interface{ InvalidateChainCache() }) {
	if h == nil {
		return
	}
	h.chainCacheInvalidator = invalidator
}

// validMetadataLanguages is the set of ISO 639-1 codes accepted for
// per-library metadata language. Kept in sync with the frontend LANGUAGES list.
var validMetadataLanguages = map[string]bool{
	"en": true, "es": true, "fr": true, "de": true, "it": true, "pt": true,
	"nl": true, "pl": true, "ru": true, "zh": true, "ja": true, "ko": true,
	"ar": true, "tr": true, "sv": true, "da": true, "no": true, "fi": true,
	"hu": true, "cs": true, "ro": true, "he": true, "th": true, "vi": true,
	"el": true, "bg": true, "hr": true, "sk": true, "sl": true, "uk": true,
	"id": true, "ms": true, "hi": true, "ta": true, "te": true, "bn": true,
	"fa": true,
}

// --- Request/Response types ---

// createLibraryRequest represents the JSON body for POST /libraries.
type createLibraryRequest struct {
	Paths                    []string `json:"paths"`
	Type                     string   `json:"type"`
	Name                     string   `json:"name"`
	MetadataLanguage         string   `json:"metadata_language,omitempty"`
	ChapterThumbnailsEnabled bool     `json:"chapter_thumbnails_enabled,omitempty"`
	IntroDetectionEnabled    bool     `json:"intro_detection_enabled,omitempty"`
	// TrailerKinds is the allow-list of remote video kinds fetched during
	// metadata refresh; omitted = default (all provider kinds).
	TrailerKinds []string `json:"trailer_kinds,omitempty"`
}

// updateLibraryRequest represents the JSON body for PUT /libraries/{id}.
type updateLibraryRequest struct {
	Paths                    *[]string `json:"paths,omitempty"`
	Type                     *string   `json:"type,omitempty"`
	Name                     *string   `json:"name,omitempty"`
	Enabled                  *bool     `json:"enabled,omitempty"`
	MetadataLanguage         *string   `json:"metadata_language,omitempty"`
	AutoTranslateMetadata    *bool     `json:"auto_translate_metadata,omitempty"`
	ChapterThumbnailsEnabled *bool     `json:"chapter_thumbnails_enabled,omitempty"`
	IntroDetectionEnabled    *bool     `json:"intro_detection_enabled,omitempty"`
	// TrailerKinds is the allow-list of remote video kinds fetched during
	// metadata refresh (ExtraKind values); empty array disables remote videos.
	TrailerKinds *[]string `json:"trailer_kinds,omitempty"`
}

// scanRequest represents the JSON body for POST /scan.
type scanRequest struct {
	LibraryID *int   `json:"library_id,omitempty"`
	Path      string `json:"path,omitempty"`
}

type scanResponse struct {
	Status    string `json:"status"`
	Mode      string `json:"mode"`
	LibraryID int    `json:"library_id"`
}

// scanCancelRequest represents the JSON body for POST /scan/cancel.
type scanCancelRequest struct {
	LibraryID int `json:"library_id"`
}

type scanCancelResponse struct {
	Cancelled int `json:"cancelled"`
	LibraryID int `json:"library_id"`
}

// libraryResponse represents a library (media folder) in JSON responses.
type libraryResponse struct {
	ID                         int        `json:"id"`
	Paths                      []string   `json:"paths"`
	Type                       string     `json:"type"`
	Name                       string     `json:"name"`
	Enabled                    bool       `json:"enabled"`
	MetadataLanguage           string     `json:"metadata_language"`
	AutoTranslateMetadata      bool       `json:"auto_translate_metadata"`
	ChapterThumbnailsEnabled   bool       `json:"chapter_thumbnails_enabled"`
	ChapterThumbnailsSupported bool       `json:"chapter_thumbnails_supported"`
	IntroDetectionEnabled      bool       `json:"intro_detection_enabled"`
	TrailerKinds               []string   `json:"trailer_kinds"`
	SortOrder                  int        `json:"sort_order"`
	PosterURL                  string     `json:"poster_url,omitempty"`
	LastScannedAt              *time.Time `json:"last_scanned_at,omitempty"`
	ScanWarningCode            *string    `json:"scan_warning_code,omitempty"`
	ScanWarningMessage         *string    `json:"scan_warning_message,omitempty"`
	ScanWarningAt              *time.Time `json:"scan_warning_at,omitempty"`
}

type libraryMountCheckRootResponse struct {
	Path         string  `json:"path"`
	Reachable    bool    `json:"reachable"`
	ErrorCode    *string `json:"error_code"`
	ErrorMessage *string `json:"error_message"`
	// SuspectEmpty is set when the root is reachable but the library holds
	// only missing-marked files under it — the signature of a lost mount
	// exposing an empty mountpoint directory, which a reachability probe
	// alone cannot detect. Additive field; absent/false for healthy roots.
	SuspectEmpty bool `json:"suspect_empty"`
}

type libraryMountCheckResponse struct {
	Status      string                          `json:"status"`
	LibraryID   int                             `json:"library_id"`
	LibraryName string                          `json:"library_name"`
	Healthy     bool                            `json:"healthy"`
	CheckedAt   time.Time                       `json:"checked_at"`
	Summary     string                          `json:"summary"`
	Roots       []libraryMountCheckRootResponse `json:"roots"`
}

type librarySkippedRootResponse struct {
	LibraryID      int       `json:"library_id"`
	LibraryName    string    `json:"library_name"`
	RootPath       string    `json:"root_path"`
	Reason         string    `json:"reason"`
	SampleFilePath string    `json:"sample_file_path"`
	FileCount      int       `json:"file_count"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
}

type staleMediaIDResponse struct {
	ContentID   string `json:"content_id"`
	LibraryID   int    `json:"library_id"`
	LibraryName string `json:"library_name"`
	Title       string `json:"title"`
	Year        int    `json:"year"`
	ContentType string `json:"content_type"`
	Provider    string `json:"provider"`
	ProviderID  string `json:"provider_id"`
	FirstSeenAt string `json:"first_seen_at"`
	LastSeenAt  string `json:"last_seen_at"`
	// FirstSeen and LastSeen carry the instants the formatted strings above
	// render, for the v2 view; they are not part of the v1 document.
	FirstSeen time.Time `json:"-"`
	LastSeen  time.Time `json:"-"`
}

type libraryRootResponse struct {
	LibraryID      int             `json:"library_id"`
	LibraryName    string          `json:"library_name"`
	RootPath       string          `json:"root_path"`
	State          string          `json:"state"`
	InferredType   string          `json:"inferred_type"`
	TypeConfidence string          `json:"type_confidence"`
	Title          string          `json:"title"`
	Year           int             `json:"year"`
	TmdbID         string          `json:"tmdb_id,omitempty"`
	ImdbID         string          `json:"imdb_id,omitempty"`
	TvdbID         string          `json:"tvdb_id,omitempty"`
	ObservedFiles  int             `json:"observed_file_count"`
	SampleFilePath string          `json:"sample_file_path,omitempty"`
	Evidence       json.RawMessage `json:"evidence_json,omitempty"`
	OverrideSource string          `json:"override_source,omitempty"`
	FirstSeenAt    time.Time       `json:"first_seen_at"`
	LastSeenAt     time.Time       `json:"last_seen_at"`
	ActiveOverride *rootOverride   `json:"active_override,omitempty"`
	// ContentID is the catalog item this group matched to, when known — it
	// lets the admin UI jump from an ambiguous root to the item's split flow.
	ContentID string `json:"content_id,omitempty"`
}

type rootOverride struct {
	ForcedType   string `json:"forced_type,omitempty"`
	ForcedTitle  string `json:"forced_title,omitempty"`
	ForcedYear   int    `json:"forced_year,omitempty"`
	ForcedTmdbID string `json:"forced_tmdb_id,omitempty"`
	ForcedImdbID string `json:"forced_imdb_id,omitempty"`
	ForcedTvdbID string `json:"forced_tvdb_id,omitempty"`
	Note         string `json:"note,omitempty"`
}

type libraryRootsListResponse struct {
	Items []libraryRootResponse `json:"items"`
	Total int                   `json:"total"`
}

type rootOverrideUpsertRequest struct {
	LibraryID    int    `json:"library_id"`
	RootPath     string `json:"root_path"`
	ForcedType   string `json:"forced_type,omitempty"`
	ForcedTitle  string `json:"forced_title,omitempty"`
	ForcedYear   int    `json:"forced_year,omitempty"`
	ForcedTmdbID string `json:"forced_tmdb_id,omitempty"`
	ForcedImdbID string `json:"forced_imdb_id,omitempty"`
	ForcedTvdbID string `json:"forced_tvdb_id,omitempty"`
	Note         string `json:"note,omitempty"`
}

type rootOverrideDeleteRequest struct {
	LibraryID int    `json:"library_id"`
	RootPath  string `json:"root_path"`
}

func groupOverrideLookupKey(groupKeyVersion int, contentGroupKey string) string {
	return strconv.Itoa(groupKeyVersion) + "|" + contentGroupKey
}

// toLibraryResponse converts a MediaFolder model to a libraryResponse.
func toLibraryResponse(f *models.MediaFolder) libraryResponse {
	paths := f.Paths
	if paths == nil {
		paths = []string{}
	}
	trailerKinds := f.TrailerKinds
	if trailerKinds == nil {
		trailerKinds = []string{}
	}
	return libraryResponse{
		ID:                         f.ID,
		Paths:                      paths,
		Type:                       f.Type,
		Name:                       f.Name,
		Enabled:                    f.Enabled,
		MetadataLanguage:           f.MetadataLanguage,
		AutoTranslateMetadata:      f.AutoTranslateMetadata,
		ChapterThumbnailsEnabled:   f.ChapterThumbnailsEnabled,
		ChapterThumbnailsSupported: false,
		IntroDetectionEnabled:      f.IntroDetectionEnabled,
		TrailerKinds:               trailerKinds,
		SortOrder:                  f.SortOrder,
		LastScannedAt:              f.LastScannedAt,
		ScanWarningCode:            f.ScanWarningCode,
		ScanWarningMessage:         f.ScanWarningMessage,
		ScanWarningAt:              f.ScanWarningAt,
	}
}

// toLibraryResponseWithPoster converts a MediaFolder model to a libraryResponse
// and presigns the poster URL if a poster path is set.
func (h *LibraryHandler) toLibraryResponseWithPoster(ctx context.Context, f *models.MediaFolder) libraryResponse {
	resp := toLibraryResponse(f)
	resp.ChapterThumbnailsSupported = h.S3Meta != nil
	if f.PosterPath != "" && h.S3Meta != nil {
		ttl := h.PresignTTL
		if ttl <= 0 {
			ttl = 4 * time.Hour
		}
		url, err := h.S3Meta.PresignGetURL(ctx, h.S3Meta.Bucket(), f.PosterPath, ttl)
		if err == nil {
			resp.PosterURL = url
		}
	}
	return resp
}

// userLibraryResponse is a simplified library view for non-admin users.
type userLibraryResponse struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	SortOrder int    `json:"sort_order"`
	PosterURL string `json:"poster_url,omitempty"`
}

// --- Handler methods ---

// HandleListUserLibraries handles GET /user/libraries.
// It returns only enabled libraries the current user has access to, with
// simplified fields (no paths, last scan metadata, etc.).
func (h *LibraryHandler) HandleListUserLibraries(w http.ResponseWriter, r *http.Request) {
	var folders []*models.MediaFolder
	var err error
	if scope, ok := access.GetScope(r.Context()); ok {
		if scope.LibrariesRestricted {
			folders, err = h.folderRepo.ListByIDs(r.Context(), scope.AllowedLibraryIDs)
		} else {
			folders, err = h.folderRepo.GetEnabled(r.Context())
		}
	} else {
		userID := apimw.GetUserID(r.Context())

		if h.userRepo != nil {
			user, userErr := h.userRepo.GetByID(r.Context(), userID)
			if userErr != nil {
				slog.ErrorContext(r.Context(), "looking up user for library access", "component", "api", "error", userErr)
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to look up user")
				return
			}

			effective, policyErr := access.EffectivePolicyForUser(r.Context(), user, h.AccessGroups)
			if policyErr != nil {
				slog.ErrorContext(r.Context(), "resolving user policy for library access", "component", "api", "error", policyErr)
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve user access")
				return
			}
			if effective.LibraryIDs != nil {
				folders, err = h.folderRepo.ListByIDs(r.Context(), effective.LibraryIDs)
			} else {
				folders, err = h.folderRepo.GetEnabled(r.Context())
			}
		} else {
			folders, err = h.folderRepo.GetEnabled(r.Context())
		}
	}

	if err != nil {
		slog.ErrorContext(r.Context(), "listing user libraries", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list libraries")
		return
	}

	resp := make([]userLibraryResponse, 0, len(folders))
	for _, f := range folders {
		entry := userLibraryResponse{
			ID:        f.ID,
			Name:      f.Name,
			Type:      f.Type,
			SortOrder: f.SortOrder,
		}
		if f.PosterPath != "" && h.S3Meta != nil {
			ttl := h.PresignTTL
			if ttl <= 0 {
				ttl = 4 * time.Hour
			}
			if url, err := h.S3Meta.PresignGetURL(r.Context(), h.S3Meta.Bucket(), f.PosterPath, ttl); err == nil {
				entry.PosterURL = url
			}
		}
		resp = append(resp, entry)
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleListLibraries handles GET /libraries.
func (h *LibraryHandler) HandleListLibraries(w http.ResponseWriter, r *http.Request) {
	resp, err := h.ListLibraries(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// reorderLibrariesRequest is the JSON body for PUT /libraries/reorder.
type reorderLibrariesRequest struct {
	Entries []catalog.FolderReorderEntry `json:"entries"`
}

// HandleReorderLibraries handles PUT /libraries/reorder.
func (h *LibraryHandler) HandleReorderLibraries(w http.ResponseWriter, r *http.Request) {
	var req reorderLibrariesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := h.ReorderLibraries(r.Context(), req.Entries); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleListSkippedRoots handles GET /libraries/skipped-roots.
func (h *LibraryHandler) HandleListSkippedRoots(w http.ResponseWriter, r *http.Request) {
	resp, err := h.ListSkippedRoots(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleCreateLibrary handles POST /libraries.
func (h *LibraryHandler) HandleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	var req createLibraryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	resp, err := h.CreateLibrary(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

// HandleUpdateLibrary handles PUT /libraries/{id}.
func (h *LibraryHandler) HandleUpdateLibrary(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}
	var req updateLibraryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	resp, err := h.UpdateLibrary(r.Context(), id, currentAdminUserID(r), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleDeleteLibrary handles DELETE /libraries/{id}.
func (h *LibraryHandler) HandleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}
	job, err := h.DeleteLibrary(r.Context(), id, currentAdminUserID(r))
	if err != nil {
		var conflict *adminjob.ActiveJobConflictError
		var apiErr *APIError
		if errors.As(err, &conflict) && errors.As(err, &apiErr) {
			writeAdminJobConflict(w, apiErr.Message, conflict.Job, NewAdminJobsHandler(nil, nil), r)
			return
		}
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, adminJobToResponse(r, job, nil))
}

// HandleCheckLibraryMount handles POST /libraries/{id}/check-mount.
// It verifies that each configured library root exists and can be listed.
func (h *LibraryHandler) HandleCheckLibraryMount(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}
	resp, err := h.CheckLibraryMount(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleScan handles POST /scan. It accepts either a library_id, a path, or both
// and dispatches to full-library, subtree, or single-file scanning.
func (h *LibraryHandler) HandleScan(w http.ResponseWriter, r *http.Request) {
	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	target, err := scantrigger.NewResolver(h.folderRepo).Resolve(r.Context(), scantrigger.Request{
		LibraryID: req.LibraryID,
		Path:      req.Path,
	})
	if err != nil {
		var reqErr *scantrigger.RequestError
		if errors.As(err, &reqErr) {
			writeError(w, reqErr.Status, reqErr.Code, reqErr.Message)
			return
		}
		slog.ErrorContext(r.Context(), "resolving scan target", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve scan target")
		return
	}

	if h.ScanQueue != nil {
		if _, err := h.ScanQueue.EnqueueScan(r.Context(), target.Folder.ID, target.Mode, target.Path, target.Trigger); err != nil {
			slog.ErrorContext(r.Context(), "queueing library scan", "component", "api", "library_id", target.Folder.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to queue scan")
			return
		}
	} else if h.ingester != nil {
		scanID := ulid.Make().String()
		h.recordAcceptedScan(scanID, target)
		switch target.Mode {
		case scantrigger.ModeFile:
			h.runFileScanAsync(scanID, target.Folder, target.Path, target.Trigger)
		case scantrigger.ModeSubtree:
			h.runSubtreeScanAsync(scanID, target.Folder, target.Path, target.Trigger)
		default:
			h.runFolderScanAsync(scanID, target.Folder, target.Trigger)
		}
	} else {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Scanner not available")
		return
	}

	writeJSON(w, http.StatusAccepted, scanResponse{
		Status:    "accepted",
		Mode:      target.Mode,
		LibraryID: target.Folder.ID,
	})
}

// HandleScanCancel handles POST /scan/cancel. It cancels all running scans
// for a given library.
func (h *LibraryHandler) HandleScanCancel(w http.ResponseWriter, r *http.Request) {
	var req scanCancelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.LibraryID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "library_id is required")
		return
	}
	if h.ingester == nil && h.ScanQueue == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Scanner not available")
		return
	}

	cancelled := 0
	if h.ScanQueue != nil {
		queuedCancelled, err := h.ScanQueue.CancelByLibrary(r.Context(), req.LibraryID)
		if err != nil {
			slog.ErrorContext(r.Context(), "cancel library scans", "component", "api", "library_id", req.LibraryID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to cancel scans")
			return
		}
		cancelled += queuedCancelled
	}
	if h.ingester != nil {
		cancelled += h.ingester.CancelLibrary(req.LibraryID)
	}
	for _, run := range h.cancelActiveScans(req.LibraryID) {
		h.publishScanEvent(r.Context(), "scan.cancelled", run)
	}
	slog.InfoContext(r.Context(), "scan: cancelled running scans", "component", "api",
		"library_id", req.LibraryID,
		"cancelled", cancelled,
	)

	writeJSON(w, http.StatusOK, scanCancelResponse{
		Cancelled: cancelled,
		LibraryID: req.LibraryID,
	})
}

func (h *LibraryHandler) runFolderScanAsync(scanID string, folder *models.MediaFolder, trigger string) {
	go func() {
		h.markScanRunning(scanID)
		slog.Info("scan: starting library scan",
			"trigger", trigger,
			"library_id", folder.ID,
			"name", folder.Name,
			"paths", folder.Paths,
		)

		start := time.Now()

		result, ingestErr := h.ingester.IngestFolder(h.appCtx, folder)
		if ingestErr != nil {
			if errors.Is(ingestErr, context.Canceled) {
				h.markScanCancelled(scanID)
				slog.Info("scan: library scan canceled",
					"trigger", trigger,
					"library_id", folder.ID,
					"elapsed", time.Since(start).Round(time.Millisecond),
				)
				return
			}
			h.markScanFailed(scanID, ingestErr)
			slog.Error("scan: library ingest failed",
				"trigger", trigger,
				"library_id", folder.ID,
				"paths", folder.Paths,
				"error", ingestErr,
				"elapsed", time.Since(start).Round(time.Millisecond),
			)
			return
		}
		h.markScanCompleted(scanID, result)

		slog.Info("scan: library ingest complete",
			"trigger", trigger,
			"library_id", folder.ID,
			"name", folder.Name,
			"new", scanMetric(result, func(r *scanner.ScanResult) int { return r.New }),
			"updated", scanMetric(result, func(r *scanner.ScanResult) int { return r.Updated }),
			"unchanged", scanMetric(result, func(r *scanner.ScanResult) int { return r.Unchanged }),
			"missing", scanMetric(result, func(r *scanner.ScanResult) int { return r.Missing }),
			"files_deleted", scanMetric(result, func(r *scanner.ScanResult) int { return r.FilesDeleted }),
			"memberships_removed", scanMetric(result, func(r *scanner.ScanResult) int { return r.MembershipsRemoved }),
			"items_deleted", scanMetric(result, func(r *scanner.ScanResult) int { return r.ItemsDeleted }),
			"empty_root_guarded", scanBoolMetric(result, func(r *scanner.ScanResult) bool { return r.EmptyRootGuarded }),
			"errors", scanMetric(result, func(r *scanner.ScanResult) int { return r.Errors }),
			"matched_files", result.MatchedFiles,
			"retried_items", result.RetriedItems,
			"still_unmatched_warnings", result.StillUnmatchedWarnings,
			"skipped", result.Skipped,
			"elapsed", time.Since(start).Round(time.Millisecond),
		)
	}()
}

func (h *LibraryHandler) runSubtreeScanAsync(scanID string, folder *models.MediaFolder, subtreePath, trigger string) {
	go func() {
		h.markScanRunning(scanID)
		slog.Info("scan: starting subtree scan",
			"trigger", trigger,
			"library_id", folder.ID,
			"name", folder.Name,
			"path", subtreePath,
		)

		start := time.Now()

		result, ingestErr := h.ingester.IngestSubtree(h.appCtx, folder, subtreePath)
		if ingestErr != nil {
			if errors.Is(ingestErr, context.Canceled) {
				h.markScanCancelled(scanID)
				slog.Info("scan: subtree scan canceled",
					"trigger", trigger,
					"library_id", folder.ID,
					"path", subtreePath,
					"elapsed", time.Since(start).Round(time.Millisecond),
				)
				return
			}
			h.markScanFailed(scanID, ingestErr)
			slog.Error("scan: subtree ingest failed",
				"trigger", trigger,
				"library_id", folder.ID,
				"path", subtreePath,
				"error", ingestErr,
				"elapsed", time.Since(start).Round(time.Millisecond),
			)
			return
		}
		h.markScanCompleted(scanID, result)

		slog.Info("scan: subtree ingest complete",
			"trigger", trigger,
			"library_id", folder.ID,
			"name", folder.Name,
			"path", subtreePath,
			"new", scanMetric(result, func(r *scanner.ScanResult) int { return r.New }),
			"updated", scanMetric(result, func(r *scanner.ScanResult) int { return r.Updated }),
			"unchanged", scanMetric(result, func(r *scanner.ScanResult) int { return r.Unchanged }),
			"missing", scanMetric(result, func(r *scanner.ScanResult) int { return r.Missing }),
			"files_deleted", scanMetric(result, func(r *scanner.ScanResult) int { return r.FilesDeleted }),
			"memberships_removed", scanMetric(result, func(r *scanner.ScanResult) int { return r.MembershipsRemoved }),
			"items_deleted", scanMetric(result, func(r *scanner.ScanResult) int { return r.ItemsDeleted }),
			"errors", scanMetric(result, func(r *scanner.ScanResult) int { return r.Errors }),
			"matched_files", result.MatchedFiles,
			"retried_items", result.RetriedItems,
			"still_unmatched_warnings", result.StillUnmatchedWarnings,
			"skipped", result.Skipped,
			"elapsed", time.Since(start).Round(time.Millisecond),
		)
	}()
}

func (h *LibraryHandler) runFileScanAsync(scanID string, folder *models.MediaFolder, filePath, trigger string) {
	go func() {
		h.markScanRunning(scanID)
		slog.Info("scan: starting file scan",
			"trigger", trigger,
			"library_id", folder.ID,
			"name", folder.Name,
			"path", filePath,
		)

		result, ingestErr := h.ingester.IngestFile(h.appCtx, folder, filePath)
		if ingestErr != nil {
			if errors.Is(ingestErr, context.Canceled) {
				h.markScanCancelled(scanID)
				slog.Info("scan: file scan canceled",
					"trigger", trigger,
					"library_id", folder.ID,
					"path", filePath,
				)
				return
			}
			h.markScanFailed(scanID, ingestErr)
			slog.Error("scan: file ingest failed",
				"trigger", trigger,
				"library_id", folder.ID,
				"path", filePath,
				"error", ingestErr,
			)
			return
		}
		h.markScanCompleted(scanID, result)

		slog.Info("scan: file ingest complete",
			"trigger", trigger,
			"library_id", folder.ID,
			"name", folder.Name,
			"path", filePath,
			"matched_files", result.MatchedFiles,
			"retried_items", result.RetriedItems,
			"still_unmatched_warnings", result.StillUnmatchedWarnings,
			"skipped", result.Skipped,
		)
	}()
}

func (h *LibraryHandler) recordAcceptedScan(scanID string, target *scantrigger.Target) {
	if h == nil || h.ScanRegistry == nil || target == nil || target.Folder == nil {
		return
	}
	h.ScanRegistry.Upsert(evt.ScanRun{
		ID:        scanID,
		LibraryID: target.Folder.ID,
		Mode:      target.Mode,
		Path:      target.Path,
		Trigger:   target.Trigger,
		Status:    "accepted",
	})
	if run, ok := h.ScanRegistry.Get(scanID); ok {
		h.publishScanEvent(context.Background(), "scan.accepted", run)
	}
}

func (h *LibraryHandler) markScanRunning(scanID string) {
	if h == nil || h.ScanRegistry == nil {
		return
	}
	run, ok := h.ScanRegistry.Get(scanID)
	if !ok {
		return
	}
	now := time.Now().UTC()
	run.Status = "running"
	run.StartedAt = &now
	h.ScanRegistry.Upsert(run)
	h.publishScanEvent(context.Background(), "scan.started", run)
}

func (h *LibraryHandler) markScanCompleted(scanID string, result *libraryingest.Result) {
	if h == nil || h.ScanRegistry == nil {
		return
	}
	run, ok := h.ScanRegistry.Get(scanID)
	if !ok {
		return
	}
	now := time.Now().UTC()
	run.Status = "completed"
	run.CompletedAt = &now
	run.Result = scanRunResultFromIngest(result)
	h.ScanRegistry.MarkTerminal(run)
	h.publishScanEvent(context.Background(), "scan.completed", run)
}

func (h *LibraryHandler) markScanFailed(scanID string, err error) {
	if h == nil || h.ScanRegistry == nil {
		return
	}
	run, ok := h.ScanRegistry.Get(scanID)
	if !ok {
		return
	}
	now := time.Now().UTC()
	run.Status = "failed"
	run.CompletedAt = &now
	if err != nil {
		run.ErrorMessage = err.Error()
	}
	h.ScanRegistry.MarkTerminal(run)
	h.publishScanEvent(context.Background(), "scan.failed", run)
}

func (h *LibraryHandler) markScanCancelled(scanID string) {
	if h == nil || h.ScanRegistry == nil {
		return
	}
	run, ok := h.ScanRegistry.Get(scanID)
	if !ok {
		return
	}
	now := time.Now().UTC()
	run.Status = "cancelled"
	run.CompletedAt = &now
	h.ScanRegistry.MarkTerminal(run)
	h.publishScanEvent(context.Background(), "scan.cancelled", run)
}

func (h *LibraryHandler) cancelActiveScans(libraryID int) []evt.ScanRun {
	if h == nil || h.ScanRegistry == nil {
		return nil
	}
	return h.ScanRegistry.CancelLibrary(libraryID, time.Now().UTC())
}

func (h *LibraryHandler) publishScanEvent(ctx context.Context, eventName string, run evt.ScanRun) {
	if h == nil || h.EventsHub == nil {
		return
	}
	_ = h.EventsHub.PublishJSON(ctx, evt.ChannelScans, eventName, run, evt.PublishOptions{
		AdminOnly: true,
	})
}

func scanRunResultFromIngest(result *libraryingest.Result) *evt.ScanRunResult {
	if result == nil {
		return nil
	}
	resp := &evt.ScanRunResult{
		MatchedFiles:           result.MatchedFiles,
		RetriedItems:           result.RetriedItems,
		StillUnmatchedWarnings: result.StillUnmatchedWarnings,
	}
	if result.Skipped {
		resp.Skipped = 1
	}
	if result.ScanResult != nil {
		resp.New = result.ScanResult.New
		resp.Updated = result.ScanResult.Updated
		resp.Unchanged = result.ScanResult.Unchanged
		resp.Missing = result.ScanResult.Missing
		resp.MissingSkippedProtected = result.ScanResult.MissingSkippedProtected
		resp.FilesDeleted = result.ScanResult.FilesDeleted
		resp.MembershipsRemoved = result.ScanResult.MembershipsRemoved
		resp.ItemsDeleted = result.ScanResult.ItemsDeleted
		resp.Errors = result.ScanResult.Errors
	}
	return resp
}

func scanMetric(result *libraryingest.Result, pick func(*scanner.ScanResult) int) int {
	if result == nil || result.ScanResult == nil {
		return 0
	}
	return pick(result.ScanResult)
}

func scanBoolMetric(result *libraryingest.Result, pick func(*scanner.ScanResult) bool) bool {
	if result == nil || result.ScanResult == nil {
		return false
	}
	return pick(result.ScanResult)
}

func (h *LibraryHandler) checkLibraryMount(ctx context.Context, folder *models.MediaFolder) libraryMountCheckResponse {
	resp := libraryMountCheckResponse{
		Status:      "ok",
		LibraryID:   folder.ID,
		LibraryName: folder.Name,
		Healthy:     true,
		CheckedAt:   time.Now().UTC(),
		Roots:       make([]libraryMountCheckRootResponse, 0, len(folder.Paths)),
	}

	if len(folder.Paths) == 0 {
		resp.Healthy = false
		resp.Summary = "Library has no configured roots"
		return resp
	}

	unreachable := 0
	emptyPaths := make([]string, 0, len(folder.Paths))
	probes := rootcheck.ProbeManyWithTimeout(ctx, folder.Paths, rootcheck.DefaultProbeTimeout)
	for i, path := range folder.Paths {
		probe := probes[i]
		root := libraryMountCheckRootResponse{
			Path:      path,
			Reachable: probe.Reachable,
		}
		if !probe.Reachable {
			root.ErrorCode = stringPtr(probe.ErrorCode)
			root.ErrorMessage = stringPtr(probe.ErrorMessage)
			unreachable++
			resp.Healthy = false
		} else if probe.Empty {
			emptyPaths = append(emptyPaths, path)
		}
		resp.Roots = append(resp.Roots, root)
	}

	// A reachable but literally empty root can be a lost mount that left its
	// bare mountpoint behind. Cross-check against the catalog: an empty root
	// holding only missing-marked files is suspect, and reporting it healthy
	// would both mislead the operator and wrongly clear the dead_root
	// warning. Best-effort — probe results stand on a query error.
	suspect := 0
	if h.pool != nil && len(emptyPaths) > 0 {
		suspectRoots, err := scanner.NewFileRepository(h.pool).ListRootsWithOnlyMissingFiles(ctx, folder.ID, emptyPaths)
		if err != nil {
			slog.WarnContext(ctx, "mount check: suspect-empty root query failed", "component", "api", "library_id", folder.ID, "error", err)
		} else if len(suspectRoots) > 0 {
			suspectSet := make(map[string]bool, len(suspectRoots))
			for _, root := range suspectRoots {
				suspectSet[root] = true
			}
			for i := range resp.Roots {
				if resp.Roots[i].Reachable && suspectSet[resp.Roots[i].Path] {
					resp.Roots[i].SuspectEmpty = true
					suspect++
					resp.Healthy = false
				}
			}
		}
	}

	switch {
	case unreachable == 0 && suspect == 0:
		resp.Summary = "All configured roots are reachable"
	case unreachable == 0:
		resp.Summary = fmt.Sprintf("%d of %d roots reachable but empty while the library still has cataloged files (lost mount?)", suspect, len(folder.Paths))
	case suspect > 0:
		resp.Summary = fmt.Sprintf("%d of %d roots unreachable; %d more reachable but empty while the library still has cataloged files", unreachable, len(folder.Paths), suspect)
	case unreachable == 1:
		resp.Summary = fmt.Sprintf("1 of %d roots unreachable", len(folder.Paths))
	default:
		resp.Summary = fmt.Sprintf("%d of %d roots unreachable", unreachable, len(folder.Paths))
	}

	return resp
}

func stringPtr(v string) *string {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

func (h *LibraryHandler) publishCatalogStatsInvalidation(eventType, payload string) {
	if h.EventBus == nil {
		return
	}
	if err := h.EventBus.Publish(h.appCtx, cache.ChannelCatalog, cache.Event{Type: eventType, Payload: payload}); err != nil {
		slog.Warn("scan: failed to publish catalog invalidation event",
			"type", eventType,
			"payload", payload,
			"error", err,
		)
	}
}

type refreshLibraryMetadataRequest struct {
	Mode string `json:"mode"`
}

type libraryMetadataMatchQueueStatusResponse struct {
	LibraryID    int `json:"library_id"`
	MovieCount   int `json:"movie_count"`
	SeriesCount  int `json:"series_count"`
	RawFileCount int `json:"raw_file_count"`
	TotalCount   int `json:"total_count"`
	PendingCount int `json:"pending_count"`
	ParkedCount  int `json:"parked_count"`
}

type libraryMetadataMatchQueueActionResponse struct {
	Status           string                                  `json:"status"`
	LibraryID        int                                     `json:"library_id"`
	MovieCancelled   int                                     `json:"movie_cancelled,omitempty"`
	SeriesCancelled  int                                     `json:"series_cancelled,omitempty"`
	RawFileCancelled int                                     `json:"raw_file_cancelled,omitempty"`
	RawFileRetried   int                                     `json:"raw_file_retried,omitempty"`
	TotalCancelled   int                                     `json:"total_cancelled,omitempty"`
	Queue            libraryMetadataMatchQueueStatusResponse `json:"queue"`
}

type libraryMetadataMatchQueueDetailResponse struct {
	libraryMetadataMatchQueueStatusResponse
	Limit    int                                    `json:"limit"`
	Offset   int                                    `json:"offset"`
	Movies   []libraryMovieMatchQueueEntryResponse  `json:"movies"`
	Series   []librarySeriesMatchQueueEntryResponse `json:"series"`
	RawFiles []libraryRawMatchBacklogEntryResponse  `json:"raw_files"`
	// The list totals are not on the v1 wire; the v2 listener pages on them.
	MovieTotal   int `json:"-"`
	SeriesTotal  int `json:"-"`
	RawFileTotal int `json:"-"`
}

type libraryMovieMatchQueueEntryResponse struct {
	MediaFileID               int             `json:"media_file_id"`
	MediaFolderID             int             `json:"media_folder_id"`
	FilePath                  string          `json:"file_path"`
	FirstQueuedAt             time.Time       `json:"first_queued_at"`
	AvailableAt               time.Time       `json:"available_at"`
	LastAttemptedAt           *time.Time      `json:"last_attempted_at,omitempty"`
	AttemptCount              int             `json:"attempt_count"`
	LastError                 string          `json:"last_error,omitempty"`
	State                     string          `json:"state"`
	FailureKind               string          `json:"failure_kind,omitempty"`
	FailureDetail             json.RawMessage `json:"failure_detail,omitempty"`
	DeterministicAttemptCount int             `json:"deterministic_attempt_count"`
	MatcherRevision           int             `json:"matcher_revision"`
	ParkedAt                  *time.Time      `json:"parked_at,omitempty"`
	UpdatedAt                 time.Time       `json:"updated_at"`
}

type librarySeriesMatchQueueEntryResponse struct {
	MediaFolderID             int             `json:"media_folder_id"`
	ObservedRootPath          string          `json:"observed_root_path"`
	FirstQueuedAt             time.Time       `json:"first_queued_at"`
	AvailableAt               time.Time       `json:"available_at"`
	LastAttemptedAt           *time.Time      `json:"last_attempted_at,omitempty"`
	AttemptCount              int             `json:"attempt_count"`
	LastError                 string          `json:"last_error,omitempty"`
	State                     string          `json:"state"`
	FailureKind               string          `json:"failure_kind,omitempty"`
	FailureDetail             json.RawMessage `json:"failure_detail,omitempty"`
	DeterministicAttemptCount int             `json:"deterministic_attempt_count"`
	MatcherRevision           int             `json:"matcher_revision"`
	ParkedAt                  *time.Time      `json:"parked_at,omitempty"`
	UpdatedAt                 time.Time       `json:"updated_at"`
}

type libraryRawMatchBacklogEntryResponse struct {
	MediaFileID     int        `json:"media_file_id"`
	MediaFolderID   int        `json:"media_folder_id"`
	FilePath        string     `json:"file_path"`
	BaseTitle       string     `json:"base_title,omitempty"`
	BaseYear        int        `json:"base_year,omitempty"`
	BaseType        string     `json:"base_type,omitempty"`
	LastAttemptedAt *time.Time `json:"last_attempted_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (h *LibraryHandler) HandleListMetadataMatchQueues(w http.ResponseWriter, r *http.Request) {
	resp, err := h.ListMetadataMatchQueues(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *LibraryHandler) HandleGetMetadataMatchQueue(w http.ResponseWriter, r *http.Request) {
	if !h.metadataMatchBacklogConfigured() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Metadata matcher backlog is not configured")
		return
	}

	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	limit := 10
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		if parsed, parseErr := strconv.Atoi(value); parseErr == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}
	offset := 0
	if value := strings.TrimSpace(r.URL.Query().Get("offset")); value != "" {
		if parsed, parseErr := strconv.Atoi(value); parseErr == nil && parsed >= 0 {
			offset = parsed
		}
	}

	resp, err := h.GetMetadataMatchQueue(r.Context(), id, limit, offset)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *LibraryHandler) HandleRetryMetadataMatchQueue(w http.ResponseWriter, r *http.Request) {
	if !h.metadataMatchBacklogConfigured() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Metadata matcher backlog is not configured")
		return
	}

	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}
	resp, err := h.RetryMetadataMatchQueue(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *LibraryHandler) HandleCancelMetadataMatchQueue(w http.ResponseWriter, r *http.Request) {
	if !h.metadataMatchBacklogConfigured() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Metadata matcher backlog is not configured")
		return
	}

	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}
	resp, err := h.CancelMetadataMatchQueue(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *LibraryHandler) metadataMatchBacklogConfigured() bool {
	return h.folderRepo != nil &&
		(h.MovieMatchQueueRepo != nil || h.SeriesMatchQueueRepo != nil || h.RawMatchBacklogRepo != nil)
}

func (h *LibraryHandler) metadataMatchQueueStatus(ctx context.Context, libraryID int) (libraryMetadataMatchQueueStatusResponse, error) {
	statuses, err := h.metadataMatchQueueStatuses(ctx, []int{libraryID})
	if err != nil {
		return libraryMetadataMatchQueueStatusResponse{LibraryID: libraryID}, err
	}
	return statuses[libraryID], nil
}

func (h *LibraryHandler) metadataMatchQueueStatuses(ctx context.Context, libraryIDs []int) (map[int]libraryMetadataMatchQueueStatusResponse, error) {
	statuses := make(map[int]libraryMetadataMatchQueueStatusResponse, len(libraryIDs))
	for _, libraryID := range libraryIDs {
		statuses[libraryID] = libraryMetadataMatchQueueStatusResponse{LibraryID: libraryID}
	}
	if h.MovieMatchQueueRepo != nil {
		counts, err := h.MovieMatchQueueRepo.CountStatesByFolders(ctx, libraryIDs)
		if err != nil {
			return nil, err
		}
		for libraryID, count := range counts {
			status := statuses[libraryID]
			status.MovieCount = count.Pending + count.Parked
			status.PendingCount += count.Pending
			status.ParkedCount += count.Parked
			statuses[libraryID] = status
		}
	}
	if h.SeriesMatchQueueRepo != nil {
		counts, err := h.SeriesMatchQueueRepo.CountStatesByFolders(ctx, libraryIDs)
		if err != nil {
			return nil, err
		}
		for libraryID, count := range counts {
			status := statuses[libraryID]
			status.SeriesCount = count.Pending + count.Parked
			status.PendingCount += count.Pending
			status.ParkedCount += count.Parked
			statuses[libraryID] = status
		}
	}
	if h.RawMatchBacklogRepo != nil {
		counts, err := h.RawMatchBacklogRepo.CountUnmatchedMatchBacklogByFolders(ctx, libraryIDs, h.rawMatchBacklogMode())
		if err != nil {
			return nil, err
		}
		for libraryID, count := range counts {
			status := statuses[libraryID]
			status.RawFileCount = count
			status.PendingCount += count
			statuses[libraryID] = status
		}
	}
	for libraryID, status := range statuses {
		status.TotalCount = status.MovieCount + status.SeriesCount + status.RawFileCount
		statuses[libraryID] = status
	}
	return statuses, nil
}

func (h *LibraryHandler) rawMatchBacklogMode() scanner.RawMatchBacklogMode {
	if h.TVSeriesRootQueue && h.MovieMatchQueueRepo != nil {
		return scanner.RawMatchBacklogMixed
	}
	if h.TVSeriesRootQueue {
		return scanner.RawMatchBacklogNonSeries
	}
	return scanner.RawMatchBacklogGeneric
}

// HandleRefreshLibraryMetadata handles POST /libraries/{id}/refresh-metadata.
// It queues a background admin job to refresh metadata for items in the
// specified library. Quick mode is the default.
func (h *LibraryHandler) HandleRefreshLibraryMetadata(w http.ResponseWriter, r *http.Request) {
	if h.JobRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Library refresh jobs are not configured")
		return
	}

	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	mode := adminjob.LibraryRefreshModeQuick
	if r.Body != nil {
		var req refreshLibraryMetadataRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
			return
		}
		if req.Mode != "" {
			switch adminjob.LibraryRefreshMode(req.Mode) {
			case adminjob.LibraryRefreshModeQuick, adminjob.LibraryRefreshModeFull:
				mode = adminjob.LibraryRefreshMode(req.Mode)
			default:
				writeError(w, http.StatusBadRequest, "bad_request", "Invalid refresh mode")
				return
			}
		}
	}

	job, err := h.RefreshLibraryMetadata(r.Context(), id, currentAdminUserID(r), mode)
	if err != nil {
		var conflict *adminjob.ActiveJobConflictError
		var apiErr *APIError
		if errors.As(err, &conflict) && errors.As(err, &apiErr) {
			writeAdminJobConflict(w, apiErr.Message, conflict.Job, NewAdminJobsHandler(nil, nil), r)
			return
		}
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, adminJobToResponse(r, job, nil))
}

// HandleConfirmEmptyRootCleanup handles POST /libraries/{id}/confirm-empty-root-cleanup.
// It arms the next empty-root scan for destructive cleanup.
func (h *LibraryHandler) HandleConfirmEmptyRootCleanup(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	if err := h.ConfirmEmptyRootCleanup(r.Context(), id); err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "Empty-root cleanup confirmed for next scan",
	})
}

// --- Library poster handlers ---

// HandleUploadPoster handles PUT /libraries/{id}/poster.
// Accepts a multipart form upload with a single "poster" file field.
func (h *LibraryHandler) HandleUploadPoster(w http.ResponseWriter, r *http.Request) {
	if h.S3Meta == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Image storage is not configured")
		return
	}

	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	if _, err := h.folderRepo.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	// Parse multipart form (max 10 MB).
	if err := r.ParseMultipartForm(maxLibraryPosterBytes); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid multipart form")
		return
	}

	file, header, err := r.FormFile("poster")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Missing poster file")
		return
	}
	defer file.Close()

	// Validate content type before reading the bytes, as v1 always has.
	ct := header.Header.Get("Content-Type")
	if posterExtension(ct) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Unsupported image type; use JPEG, PNG, or WebP")
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, maxLibraryPosterBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read upload")
		return
	}

	resp, err := h.UploadLibraryPoster(r.Context(), id, ct, data)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleDeletePoster handles DELETE /libraries/{id}/poster.
func (h *LibraryHandler) HandleDeletePoster(w http.ResponseWriter, r *http.Request) {
	if h.S3Meta == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Image storage is not configured")
		return
	}

	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	if err := h.DeleteLibraryPoster(r.Context(), id); err != nil {
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// posterExtension returns the file extension for a valid poster content type.
func posterExtension(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

// --- Provider chain types ---

// chainLevelEntry represents a single entry in a per-level provider chain response.
type chainLevelEntry struct {
	PluginInstallationID int    `json:"plugin_installation_id"`
	CapabilityID         string `json:"capability_id"`
	ProviderSlug         string `json:"provider_slug"`
	Priority             int    `json:"priority"`
	Enabled              bool   `json:"enabled"`
}

// setChainLevelRequest is the JSON body for PUT /libraries/{id}/providers.
type setChainLevelRequest struct {
	Levels map[string][]chainEntryInput `json:"levels"`
}

// chainEntryInput is a single entry in a set-chain request.
type chainEntryInput struct {
	PluginInstallationID int    `json:"plugin_installation_id"`
	CapabilityID         string `json:"capability_id"`
	Priority             int    `json:"priority"`
	Enabled              bool   `json:"enabled"`
}

// HandleGetLibraryProviders handles GET /libraries/{id}/providers.
// It returns the provider chain for the given library grouped by content level.
func (h *LibraryHandler) HandleGetLibraryProviders(w http.ResponseWriter, r *http.Request) {
	if h.ChainRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Provider chain management is not configured")
		return
	}

	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	levels, err := h.LibraryProviders(r.Context(), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"levels": levels})
}

// HandleGetLibraryProviderDefaults handles GET /libraries/provider-defaults.
// It returns the provider chain that would be seeded for a new library of the
// given type, grouped by content level and in seeded order. The admin UI
// renders this while creating a library instead of re-deriving the chain from
// plugin manifests client-side, so the displayed defaults and the chain the
// server seeds on create can never disagree.
func (h *LibraryHandler) HandleGetLibraryProviderDefaults(w http.ResponseWriter, r *http.Request) {
	out, err := h.LibraryProviderDefaults(r.Context(), r.URL.Query().Get("library_type"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"levels": out})
}

// HandleSetLibraryProviders handles PUT /libraries/{id}/providers.
// It replaces the entire provider chain for the given library.
func (h *LibraryHandler) HandleSetLibraryProviders(w http.ResponseWriter, r *http.Request) {
	if h.ChainRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Provider chain management is not configured")
		return
	}

	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	// Verify the library exists before reading the body, as v1 always has.
	if _, err := h.folderRepo.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		slog.ErrorContext(r.Context(), "fetching library for provider chain update", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	var req setChainLevelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if err := h.SetLibraryProviders(r.Context(), id, req.Levels); err != nil {
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *LibraryHandler) wakeMetadataMatcher(ctx context.Context, libraryID int) {
	if h.MovieMatchQueueRepo != nil {
		if _, err := h.MovieMatchQueueRepo.RetryNowByFolder(ctx, libraryID); err != nil {
			slog.WarnContext(ctx, "wake movie metadata matcher", "component", "api", "library_id", libraryID, "error", err)
		}
	}
	if h.SeriesMatchQueueRepo != nil {
		if _, err := h.SeriesMatchQueueRepo.RetryNowByFolder(ctx, libraryID); err != nil {
			slog.WarnContext(ctx, "wake series metadata matcher", "component", "api", "library_id", libraryID, "error", err)
		}
	}
}

// seedDefaultChain builds a default provider chain from plugin manifest defaults
// for the given library type. Returns entries for all applicable content levels.
func (h *LibraryHandler) seedDefaultChain(ctx context.Context, libraryType string) []metadata.ChainEntry {
	if h.ChainRepo == nil {
		return nil
	}

	caps, err := metadata.ListEnabledMetadataCapabilities(ctx, h.ChainRepo.Pool())
	if err != nil {
		slog.WarnContext(ctx, "seed chain: failed to list metadata capabilities", "component", "api", "error", err)
		return nil
	}

	levels := metadataContentLevelsForLibraryType(libraryType)
	if len(levels) == 0 {
		return nil
	}

	var entries []metadata.ChainEntry
	for _, level := range levels {
		candidates := make([]seedCandidate, 0, len(caps))
		for _, c := range caps {
			p := metadata.LookupSeedPlacement(ctx, h.ChainRepo.Pool(), c.PluginInstallationID, c.CapabilityID, level)
			candidates = append(candidates, seedCandidate{
				installationID:   c.PluginInstallationID,
				capabilityID:     c.CapabilityID,
				supportsLevel:    p.SupportsLevel,
				declaredPriority: p.DefaultPriority,
				defaultEnabled:   p.DefaultEnabled,
			})
		}
		entries = append(entries, buildSeededChainEntries(level, candidates)...)
	}

	return entries
}

// seedCandidate is a metadata provider under consideration for a freshly seeded
// chain at one content level, carrying the manifest-declared values that decide
// its placement.
type seedCandidate struct {
	installationID   int
	capabilityID     string
	supportsLevel    bool // provider handles this content level (declared it, or is a legacy catch-all)
	declaredPriority int  // manifest default_priority for this level; 0 = level not declared
	defaultEnabled   bool // manifest default_enabled; false = specialist opts out of auto-enable
}

// buildSeededChainEntries orders the providers for one content level and assigns
// positional priorities. Providers that do not handle this level are dropped
// outright — a single-purpose provider (e.g. audiobook/ebook/manga metadata)
// never clutters a library type it cannot serve. Of the remaining providers, one
// that declares this level (declaredPriority>0) is placed by that priority and
// seeded enabled unless it opted out via default_enabled; a legacy provider that
// declares no levels at all is parked last and disabled. Keeping an opted-out
// provider at its declared priority (rather than forcing it last) means that when
// a user does enable it, it slots in where the manifest intends instead of
// jumping to the top of the chain.
func buildSeededChainEntries(level string, candidates []seedCandidate) []metadata.ChainEntry {
	ranked := make([]seedCandidate, 0, len(candidates))
	for _, c := range candidates {
		if !c.supportsLevel {
			continue
		}
		if c.declaredPriority > 0 {
			ranked = append(ranked, c)
		} else {
			// Legacy provider that declares no levels — park last, disabled.
			ranked = append(ranked, seedCandidate{installationID: c.installationID, capabilityID: c.capabilityID, supportsLevel: true, declaredPriority: 999, defaultEnabled: false})
		}
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].declaredPriority < ranked[j].declaredPriority
	})

	entries := make([]metadata.ChainEntry, len(ranked))
	for i, c := range ranked {
		entries[i] = metadata.ChainEntry{
			PluginInstallationID: c.installationID,
			CapabilityID:         c.capabilityID,
			CapabilityType:       "metadata_provider.v1",
			ContentLevel:         level,
			Priority:             i,
			Enabled:              c.declaredPriority > 0 && c.defaultEnabled,
		}
	}
	return entries
}

func metadataContentLevelsForLibraryType(libraryType string) []string {
	return metadata.ContentLevelsForLibraryType(libraryType)
}

// HandleListStaleIDs handles GET /libraries/stale-ids.
func (h *LibraryHandler) HandleListStaleIDs(w http.ResponseWriter, r *http.Request) {
	resp, err := h.ListStaleIDs(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleRematchStaleID handles POST /libraries/stale-ids/{contentID}/rematch.
// Deprecated: prefer the explicit admin match search/apply flow via
// POST /admin/items/{id}/match/search and POST /admin/items/{id}/match/apply.
func (h *LibraryHandler) HandleRematchStaleID(w http.ResponseWriter, r *http.Request) {
	if err := h.RematchStaleID(r.Context(), chi.URLParam(r, "contentID")); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LibraryHandler) HandleListRoots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	libraryID, err := strconv.Atoi(q.Get("library_id"))
	if err != nil || libraryID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "library_id is required")
		return
	}
	limit := 100
	if value := strings.TrimSpace(q.Get("limit")); value != "" {
		if parsed, parseErr := strconv.Atoi(value); parseErr == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	offset := 0
	if value := strings.TrimSpace(q.Get("offset")); value != "" {
		if parsed, parseErr := strconv.Atoi(value); parseErr == nil && parsed >= 0 {
			offset = parsed
		}
	}
	items, total, err := h.ListLibraryRoots(r.Context(), libraryID, strings.TrimSpace(q.Get("state")), limit, offset)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, libraryRootsListResponse{Items: items, Total: total})
}

func (h *LibraryHandler) HandleUpsertRootOverride(w http.ResponseWriter, r *http.Request) {
	var req rootOverrideUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := h.SetRootOverride(r.Context(), apimw.GetUserID(r.Context()), req); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LibraryHandler) HandleDeleteRootOverride(w http.ResponseWriter, r *http.Request) {
	var req rootOverrideDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := h.DeleteRootOverride(r.Context(), req); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unmatchedItemResponse represents an item in the unmatched-items list.
type unmatchedItemResponse struct {
	ContentID   string `json:"content_id"`
	Title       string `json:"title"`
	Year        int    `json:"year"`
	ContentType string `json:"content_type"`
	LibraryID   int    `json:"library_id"`
	LibraryName string `json:"library_name"`
	Status      string `json:"status"`
}

type unmatchedItemsListResponse struct {
	Items []unmatchedItemResponse `json:"items"`
	Total int                     `json:"total"`
}

// HandleListUnmatchedItems handles GET /libraries/unmatched-items.
// Returns items that are in unmatched, pending, or ambiguous status, enriched with
// library context so the admin maintenance page can link to them.
func (h *LibraryHandler) HandleListUnmatchedItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	items, total, err := h.ListUnmatchedItems(r.Context(), q.Get("q"), limit, offset)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, unmatchedItemsListResponse{Items: items, Total: total})
}

// parseIDParam extracts and parses the "id" URL parameter as an integer.
func parseIDParam(r *http.Request) (int, error) {
	idStr := chi.URLParam(r, "id")
	return strconv.Atoi(idStr)
}
