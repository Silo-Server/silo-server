package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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
	CountByFolder(ctx context.Context, folderID int) (int, error)
	ListByFolder(ctx context.Context, folderID int, limit int, offset int) ([]models.MovieMatchQueueEntry, int, error)
}

type librarySeriesMatchQueue interface {
	SyncForFolder(ctx context.Context, folderID int) error
	DeleteByFolder(ctx context.Context, folderID int) (int, error)
	CountByFolder(ctx context.Context, folderID int) (int, error)
	ListByFolder(ctx context.Context, folderID int, limit int, offset int) ([]models.SeriesRootMatchQueueEntry, int, error)
}

type libraryRawMatchBacklog interface {
	CountUnmatchedMatchBacklogByFolder(ctx context.Context, folderID int, mode scanner.RawMatchBacklogMode) (int, error)
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
				slog.Error("looking up user for library access", "error", userErr)
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to look up user")
				return
			}

			if user.LibraryIDs != nil {
				folders, err = h.folderRepo.ListByIDs(r.Context(), user.LibraryIDs)
			} else {
				folders, err = h.folderRepo.GetEnabled(r.Context())
			}
		} else {
			folders, err = h.folderRepo.GetEnabled(r.Context())
		}
	}

	if err != nil {
		slog.Error("listing user libraries", "error", err)
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
	folders, err := h.folderRepo.List(r.Context())
	if err != nil {
		slog.Error("listing libraries", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list libraries")
		return
	}

	resp := make([]libraryResponse, 0, len(folders))
	for _, f := range folders {
		resp = append(resp, h.toLibraryResponseWithPoster(r.Context(), f))
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
	if err := h.folderRepo.Reorder(r.Context(), req.Entries); err != nil {
		slog.Error("reordering libraries", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to reorder libraries")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleListSkippedRoots handles GET /libraries/skipped-roots.
func (h *LibraryHandler) HandleListSkippedRoots(w http.ResponseWriter, r *http.Request) {
	if h.SkippedRootRepo == nil {
		writeJSON(w, http.StatusOK, []librarySkippedRootResponse{})
		return
	}

	folders, err := h.folderRepo.List(r.Context())
	if err != nil {
		slog.Error("listing libraries for skipped roots", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list libraries")
		return
	}

	folderNames := make(map[int]string, len(folders))
	for _, folder := range folders {
		folderNames[folder.ID] = folder.Name
	}

	roots, err := h.SkippedRootRepo.ListAll(r.Context())
	if err != nil {
		slog.Error("listing skipped roots", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list skipped roots")
		return
	}

	resp := make([]librarySkippedRootResponse, 0, len(roots))
	for _, root := range roots {
		resp = append(resp, librarySkippedRootResponse{
			LibraryID:      root.MediaFolderID,
			LibraryName:    folderNames[root.MediaFolderID],
			RootPath:       root.RootPath,
			Reason:         root.Reason,
			SampleFilePath: root.SampleFilePath,
			FileCount:      root.FileCount,
			FirstSeenAt:    root.FirstSeenAt,
			LastSeenAt:     root.LastSeenAt,
		})
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

	if len(req.Paths) == 0 || req.Type == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Paths, type, and name are required")
		return
	}
	if req.MetadataLanguage != "" && !validMetadataLanguages[req.MetadataLanguage] {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid metadata_language; must be a valid ISO 639-1 code")
		return
	}
	if req.ChapterThumbnailsEnabled && h.S3Meta == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Chapter thumbnails require configured public asset S3 storage")
		return
	}

	folder, err := h.folderRepo.Create(r.Context(), catalog.CreateFolderInput{
		Paths:                    req.Paths,
		Type:                     req.Type,
		Name:                     req.Name,
		MetadataLanguage:         req.MetadataLanguage,
		ChapterThumbnailsEnabled: req.ChapterThumbnailsEnabled,
		IntroDetectionEnabled:    req.IntroDetectionEnabled,
	})
	if err != nil {
		if errors.Is(err, catalog.ErrDuplicatePath) {
			writeError(w, http.StatusConflict, "conflict", "A library with this path already exists")
			return
		}
		slog.Error("creating library", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create library")
		return
	}

	// Seed default sections for the new library.
	if h.SectionRepo != nil {
		if seedErr := h.SectionRepo.SeedDefaults(r.Context(), "library", &folder.ID, sections.DefaultLibrarySectionsForType(&folder.ID, folder.Type)); seedErr != nil {
			slog.Warn("seed default sections for new library", "library_id", folder.ID, "error", seedErr)
		}
		if sections.IsAudiobookLibraryType(folder.Type) {
			if _, seedErr := h.SectionRepo.EnsureHomeContinueListeningSection(r.Context()); seedErr != nil {
				slog.Warn("ensure home continue listening section", "library_id", folder.ID, "error", seedErr)
			}
		}
		if _, seedErr := h.SectionRepo.CreateGeneratedHomeLibraryRecentSections(r.Context(), folder.ID, folder.Name, folder.Type); seedErr != nil {
			slog.Warn("seed generated home sections for new library", "library_id", folder.ID, "error", seedErr)
		}
	}

	// Seed default provider chain from plugin manifest defaults.
	if h.ChainRepo != nil {
		entries := h.seedDefaultChain(r.Context(), req.Type)
		if len(entries) > 0 {
			if seedErr := h.ChainRepo.SetChain(r.Context(), folder.ID, entries); seedErr != nil {
				slog.Warn("seed default chain failed", "folder_id", folder.ID, "error", seedErr)
			}
		}
	}

	// Kick off an initial scan so content appears immediately.
	if h.ScanQueue != nil {
		if _, err := h.ScanQueue.EnqueueLibraryScan(r.Context(), folder.ID, "library_created"); err != nil {
			slog.Warn("queue initial library scan failed", "library_id", folder.ID, "error", err)
		}
	} else {
		initialScanID := ulid.Make().String()
		h.recordAcceptedScan(initialScanID, &scantrigger.Target{
			Folder:  folder,
			Mode:    scantrigger.ModeLibrary,
			Trigger: "library_created",
		})
		h.runFolderScanAsync(initialScanID, folder, "library_created")
	}

	writeJSON(w, http.StatusCreated, h.toLibraryResponseWithPoster(r.Context(), folder))
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
	if req.MetadataLanguage != nil && *req.MetadataLanguage != "" && !validMetadataLanguages[*req.MetadataLanguage] {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid metadata_language; must be a valid ISO 639-1 code")
		return
	}
	if req.ChapterThumbnailsEnabled != nil && *req.ChapterThumbnailsEnabled && h.S3Meta == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Chapter thumbnails require configured public asset S3 storage")
		return
	}

	// Fetch the folder before updating so we can detect path changes.
	oldFolder, err := h.folderRepo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		slog.Error("fetching library for update", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	err = h.folderRepo.Update(r.Context(), id, catalog.UpdateFolderInput{
		Paths:                    req.Paths,
		Type:                     req.Type,
		Name:                     req.Name,
		Enabled:                  req.Enabled,
		MetadataLanguage:         req.MetadataLanguage,
		AutoTranslateMetadata:    req.AutoTranslateMetadata,
		ChapterThumbnailsEnabled: req.ChapterThumbnailsEnabled,
		IntroDetectionEnabled:    req.IntroDetectionEnabled,
	})
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		if errors.Is(err, catalog.ErrDuplicatePath) {
			writeError(w, http.StatusConflict, "conflict", "A library with this path already exists")
			return
		}
		slog.Error("updating library", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update library")
		return
	}

	// Fetch the updated folder to return it.
	folder, err := h.folderRepo.GetByID(r.Context(), id)
	if err != nil {
		slog.Error("fetching updated library", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch updated library")
		return
	}

	if h.SectionRepo != nil && oldFolder.Name != folder.Name {
		if syncErr := h.SectionRepo.SyncGeneratedHomeLibraryRecentTitles(r.Context(), id, oldFolder.Name, folder.Name); syncErr != nil {
			slog.Warn("sync generated home section titles", "library_id", id, "error", syncErr)
		}
	}

	// Re-fetch metadata when the library's metadata language changed, so
	// existing items adopt the new language instead of keeping the one
	// stamped at first match. Quick mode suffices: the refresh item lister
	// includes complete-but-language-mismatched items.
	if h.JobRepo != nil && !strings.EqualFold(strings.TrimSpace(oldFolder.MetadataLanguage), strings.TrimSpace(folder.MetadataLanguage)) {
		job, jobErr := h.JobRepo.CreateLibraryRefresh(r.Context(), currentAdminUserID(r), adminjob.LibraryRefreshRequest{
			LibraryID:   folder.ID,
			LibraryName: folder.Name,
			Mode:        adminjob.LibraryRefreshModeQuick,
		}, "Queued metadata refresh after library language change")
		if jobErr != nil {
			var conflict *adminjob.ActiveJobConflictError
			if !errors.As(jobErr, &conflict) {
				slog.Warn("queue language-change metadata refresh failed", "library_id", folder.ID, "error", jobErr)
			}
		} else {
			publishEventJob(r.Context(), h.EventsHub, "job.created", job)
		}
	}

	// Rescan when paths have changed (folders added or removed).
	if req.Paths != nil && !slices.Equal(oldFolder.Paths, *req.Paths) {
		if h.ScanQueue != nil {
			if _, err := h.ScanQueue.EnqueueLibraryScan(r.Context(), folder.ID, "library_paths_changed"); err != nil {
				slog.Warn("queue library path-change scan failed", "library_id", folder.ID, "error", err)
			}
		} else {
			updateScanID := ulid.Make().String()
			h.recordAcceptedScan(updateScanID, &scantrigger.Target{
				Folder:  folder,
				Mode:    scantrigger.ModeLibrary,
				Trigger: "library_paths_changed",
			})
			h.runFolderScanAsync(updateScanID, folder, "library_paths_changed")
		}
	}

	writeJSON(w, http.StatusOK, h.toLibraryResponseWithPoster(r.Context(), folder))
}

// HandleDeleteLibrary handles DELETE /libraries/{id}.
func (h *LibraryHandler) HandleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid library ID")
		return
	}

	if h.JobRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Library delete jobs are not configured")
		return
	}

	folder, err := h.folderRepo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		slog.Error("fetching library before delete", "library_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load library")
		return
	}

	wasEnabled := folder.Enabled
	if wasEnabled {
		disabled := false
		if err := h.folderRepo.Update(r.Context(), folder.ID, catalog.UpdateFolderInput{Enabled: &disabled}); err != nil {
			slog.Error("disabling library before delete", "library_id", folder.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to prepare library deletion")
			return
		}
		folder.Enabled = false
	}

	job, err := h.JobRepo.Create(r.Context(), adminjob.CreateJobInput{
		JobType:         adminjob.JobTypeDeleteLibrary,
		CreatedByUserID: currentAdminUserID(r),
		RequestPayload: adminjob.DeleteLibraryRequest{
			LibraryID:   folder.ID,
			LibraryName: folder.Name,
		},
		Message: "Queued library deletion",
	})
	if err != nil {
		if wasEnabled {
			enabled := true
			if revertErr := h.folderRepo.Update(r.Context(), folder.ID, catalog.UpdateFolderInput{Enabled: &enabled}); revertErr != nil {
				slog.Error("re-enabling library after failed delete queue",
					"library_id", folder.ID,
					"queue_error", err,
					"revert_error", revertErr,
				)
			}
		}
		var conflict *adminjob.ActiveJobConflictError
		if errors.As(err, &conflict) {
			jobsHandler := NewAdminJobsHandler(nil, nil)
			writeAdminJobConflict(w, "A library deletion is already queued or running", conflict.Job, jobsHandler, r)
			return
		}
		slog.Error("queuing library delete job", "library_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to queue library delete")
		return
	}

	if h.ingester != nil {
		cancelled := h.ingester.CancelLibrary(folder.ID)
		slog.Info("library delete: cancelled running scans", "library_id", folder.ID, "cancelled", cancelled)
	}
	if h.ScanQueue != nil {
		queuedCancelled, err := h.ScanQueue.CancelAcceptedByLibrary(r.Context(), folder.ID)
		if err != nil {
			slog.Warn("library delete: failed to cancel queued scans", "library_id", folder.ID, "error", err)
		} else if queuedCancelled > 0 {
			slog.Info("library delete: cancelled queued scans", "library_id", folder.ID, "cancelled", queuedCancelled)
		}
	}
	publishEventJob(r.Context(), h.EventsHub, "job.created", job)

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

	folder, err := h.folderRepo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		slog.Error("fetching library for mount check", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	resp := checkLibraryMount(folder)
	if resp.Healthy && folder.ScanWarningCode != nil && *folder.ScanWarningCode == "empty_root" {
		if err := h.folderRepo.ClearScanWarning(r.Context(), folder.ID); err != nil {
			if errors.Is(err, catalog.ErrFolderNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "Library not found")
				return
			}
			slog.Error("clearing empty-root warning after successful mount check", "library_id", folder.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear library warning")
			return
		}
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
		slog.Error("resolving scan target", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve scan target")
		return
	}

	if h.ScanQueue != nil {
		if _, err := h.ScanQueue.EnqueueScan(r.Context(), target.Folder.ID, target.Mode, target.Path, target.Trigger); err != nil {
			slog.Error("queueing library scan", "library_id", target.Folder.ID, "error", err)
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
			slog.Error("cancel library scans", "library_id", req.LibraryID, "error", err)
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
	slog.Info("scan: cancelled running scans",
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

func checkLibraryMount(folder *models.MediaFolder) libraryMountCheckResponse {
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
	for _, path := range folder.Paths {
		root := libraryMountCheckRootResponse{
			Path:      path,
			Reachable: true,
		}

		info, err := os.Stat(path)
		if err != nil {
			code, message := classifyMountCheckError(err, false)
			root.Reachable = false
			root.ErrorCode = stringPtr(code)
			root.ErrorMessage = stringPtr(message)
		} else if !info.IsDir() {
			root.Reachable = false
			root.ErrorCode = stringPtr("not_directory")
			root.ErrorMessage = stringPtr("Path is not a directory")
		} else if _, err := os.ReadDir(path); err != nil {
			code, message := classifyMountCheckError(err, true)
			root.Reachable = false
			root.ErrorCode = stringPtr(code)
			root.ErrorMessage = stringPtr(message)
		}

		if !root.Reachable {
			unreachable++
			resp.Healthy = false
		}
		resp.Roots = append(resp.Roots, root)
	}

	switch unreachable {
	case 0:
		resp.Summary = "All configured roots are reachable"
	case 1:
		resp.Summary = fmt.Sprintf("1 of %d roots unreachable", len(folder.Paths))
	default:
		resp.Summary = fmt.Sprintf("%d of %d roots unreachable", unreachable, len(folder.Paths))
	}

	return resp
}

func classifyMountCheckError(err error, isRead bool) (string, string) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "not_found", "Path does not exist"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied", "Permission denied"
	case isRead:
		return "read_failed", "Failed to read directory"
	default:
		return "stat_failed", "Failed to stat path"
	}
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
	Movies   []libraryMovieMatchQueueEntryResponse  `json:"movies"`
	Series   []librarySeriesMatchQueueEntryResponse `json:"series"`
	RawFiles []libraryRawMatchBacklogEntryResponse  `json:"raw_files"`
}

type libraryMovieMatchQueueEntryResponse struct {
	MediaFileID     int        `json:"media_file_id"`
	MediaFolderID   int        `json:"media_folder_id"`
	FilePath        string     `json:"file_path"`
	FirstQueuedAt   time.Time  `json:"first_queued_at"`
	AvailableAt     time.Time  `json:"available_at"`
	LastAttemptedAt *time.Time `json:"last_attempted_at,omitempty"`
	AttemptCount    int        `json:"attempt_count"`
	LastError       string     `json:"last_error,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type librarySeriesMatchQueueEntryResponse struct {
	MediaFolderID    int        `json:"media_folder_id"`
	ObservedRootPath string     `json:"observed_root_path"`
	FirstQueuedAt    time.Time  `json:"first_queued_at"`
	AvailableAt      time.Time  `json:"available_at"`
	LastAttemptedAt  *time.Time `json:"last_attempted_at,omitempty"`
	AttemptCount     int        `json:"attempt_count"`
	LastError        string     `json:"last_error,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
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
	if h.folderRepo == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Library repository is not configured")
		return
	}

	folders, err := h.folderRepo.List(r.Context())
	if err != nil {
		slog.Error("metadata queue: failed to list libraries", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list metadata matcher queues")
		return
	}

	resp := make([]libraryMetadataMatchQueueStatusResponse, 0, len(folders))
	for _, folder := range folders {
		if folder == nil {
			continue
		}
		status, err := h.metadataMatchQueueStatus(r.Context(), folder.ID)
		if err != nil {
			slog.Error("metadata queue: failed to load queue status", "library_id", folder.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load metadata matcher queue")
			return
		}
		resp = append(resp, status)
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
	if _, err := h.folderRepo.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	limit := 10
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		if parsed, parseErr := strconv.Atoi(value); parseErr == nil && parsed > 0 && parsed <= 50 {
			limit = parsed
		}
	}

	status, err := h.metadataMatchQueueStatus(r.Context(), id)
	if err != nil {
		slog.Error("metadata queue: failed to load queue status", "library_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load metadata matcher queue")
		return
	}

	resp := libraryMetadataMatchQueueDetailResponse{
		libraryMetadataMatchQueueStatusResponse: status,
		Movies:                                  []libraryMovieMatchQueueEntryResponse{},
		Series:                                  []librarySeriesMatchQueueEntryResponse{},
		RawFiles:                                []libraryRawMatchBacklogEntryResponse{},
	}
	if h.MovieMatchQueueRepo != nil {
		movies, _, err := h.MovieMatchQueueRepo.ListByFolder(r.Context(), id, limit, 0)
		if err != nil {
			slog.Error("metadata queue: failed to list movie queue", "library_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list metadata matcher queue")
			return
		}
		for _, entry := range movies {
			resp.Movies = append(resp.Movies, libraryMovieMatchQueueEntryResponse{
				MediaFileID:     entry.MediaFileID,
				MediaFolderID:   entry.MediaFolderID,
				FilePath:        entry.FilePath,
				FirstQueuedAt:   entry.FirstQueuedAt,
				AvailableAt:     entry.AvailableAt,
				LastAttemptedAt: entry.LastAttemptedAt,
				AttemptCount:    entry.AttemptCount,
				LastError:       entry.LastError,
				UpdatedAt:       entry.UpdatedAt,
			})
		}
	}
	if h.SeriesMatchQueueRepo != nil {
		series, _, err := h.SeriesMatchQueueRepo.ListByFolder(r.Context(), id, limit, 0)
		if err != nil {
			slog.Error("metadata queue: failed to list series queue", "library_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list metadata matcher queue")
			return
		}
		for _, entry := range series {
			resp.Series = append(resp.Series, librarySeriesMatchQueueEntryResponse{
				MediaFolderID:    entry.MediaFolderID,
				ObservedRootPath: entry.ObservedRootPath,
				FirstQueuedAt:    entry.FirstQueuedAt,
				AvailableAt:      entry.AvailableAt,
				LastAttemptedAt:  entry.LastAttemptedAt,
				AttemptCount:     entry.AttemptCount,
				LastError:        entry.LastError,
				UpdatedAt:        entry.UpdatedAt,
			})
		}
	}
	if h.RawMatchBacklogRepo != nil {
		rawFiles, _, err := h.RawMatchBacklogRepo.ListUnmatchedMatchBacklogByFolder(r.Context(), id, h.rawMatchBacklogMode(), limit, 0)
		if err != nil {
			slog.Error("metadata queue: failed to list raw backlog", "library_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list metadata matcher backlog")
			return
		}
		for _, file := range rawFiles {
			if file == nil {
				continue
			}
			resp.RawFiles = append(resp.RawFiles, libraryRawMatchBacklogEntryResponse{
				MediaFileID:     file.ID,
				MediaFolderID:   file.MediaFolderID,
				FilePath:        file.FilePath,
				BaseTitle:       file.BaseTitle,
				BaseYear:        file.BaseYear,
				BaseType:        file.BaseType,
				LastAttemptedAt: file.MatchAttemptedAt,
				CreatedAt:       file.CreatedAt,
				UpdatedAt:       file.UpdatedAt,
			})
		}
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
	if _, err := h.folderRepo.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	if h.SeriesMatchQueueRepo != nil {
		if err := h.SeriesMatchQueueRepo.SyncForFolder(r.Context(), id); err != nil {
			slog.Error("metadata queue: failed to retry series queue", "library_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retry metadata matcher")
			return
		}
	}
	if h.MovieMatchQueueRepo != nil {
		if err := h.MovieMatchQueueRepo.SyncForFolder(r.Context(), id); err != nil {
			slog.Error("metadata queue: failed to retry movie queue", "library_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retry metadata matcher")
			return
		}
	}
	rawFileRetried := 0
	if h.RawMatchBacklogRepo != nil {
		rawFileRetried, err = h.RawMatchBacklogRepo.RetryUnmatchedMatchBacklogByFolder(r.Context(), id, h.rawMatchBacklogMode())
		if err != nil {
			slog.Error("metadata queue: failed to retry raw backlog", "library_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retry metadata matcher")
			return
		}
	}

	status, err := h.metadataMatchQueueStatus(r.Context(), id)
	if err != nil {
		slog.Error("metadata queue: failed to load retried queue status", "library_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load metadata matcher queue")
		return
	}
	writeJSON(w, http.StatusOK, libraryMetadataMatchQueueActionResponse{
		Status:         "queued",
		LibraryID:      id,
		RawFileRetried: rawFileRetried,
		Queue:          status,
	})
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
	if _, err := h.folderRepo.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	seriesCancelled := 0
	if h.SeriesMatchQueueRepo != nil {
		seriesCancelled, err = h.SeriesMatchQueueRepo.DeleteByFolder(r.Context(), id)
		if err != nil {
			slog.Error("metadata queue: failed to cancel series queue", "library_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to cancel metadata matcher")
			return
		}
	}
	movieCancelled := 0
	if h.MovieMatchQueueRepo != nil {
		movieCancelled, err = h.MovieMatchQueueRepo.DeleteByFolder(r.Context(), id)
		if err != nil {
			slog.Error("metadata queue: failed to cancel movie queue", "library_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to cancel metadata matcher")
			return
		}
	}
	rawFileCancelled := 0
	if h.RawMatchBacklogRepo != nil {
		rawFileCancelled, err = h.RawMatchBacklogRepo.SuppressUnmatchedMatchBacklogByFolder(r.Context(), id, h.rawMatchBacklogMode())
		if err != nil {
			slog.Error("metadata queue: failed to suppress raw backlog", "library_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to cancel metadata matcher")
			return
		}
	}

	status, err := h.metadataMatchQueueStatus(r.Context(), id)
	if err != nil {
		slog.Error("metadata queue: failed to load cancelled queue status", "library_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load metadata matcher queue")
		return
	}
	writeJSON(w, http.StatusOK, libraryMetadataMatchQueueActionResponse{
		Status:           "cancelled",
		LibraryID:        id,
		MovieCancelled:   movieCancelled,
		SeriesCancelled:  seriesCancelled,
		RawFileCancelled: rawFileCancelled,
		TotalCancelled:   movieCancelled + seriesCancelled + rawFileCancelled,
		Queue:            status,
	})
}

func (h *LibraryHandler) metadataMatchBacklogConfigured() bool {
	return h.folderRepo != nil &&
		(h.MovieMatchQueueRepo != nil || h.SeriesMatchQueueRepo != nil || h.RawMatchBacklogRepo != nil)
}

func (h *LibraryHandler) metadataMatchQueueStatus(ctx context.Context, libraryID int) (libraryMetadataMatchQueueStatusResponse, error) {
	resp := libraryMetadataMatchQueueStatusResponse{LibraryID: libraryID}
	if h.MovieMatchQueueRepo != nil {
		count, err := h.MovieMatchQueueRepo.CountByFolder(ctx, libraryID)
		if err != nil {
			return resp, err
		}
		resp.MovieCount = count
	}
	if h.SeriesMatchQueueRepo != nil {
		count, err := h.SeriesMatchQueueRepo.CountByFolder(ctx, libraryID)
		if err != nil {
			return resp, err
		}
		resp.SeriesCount = count
	}
	if h.RawMatchBacklogRepo != nil {
		count, err := h.RawMatchBacklogRepo.CountUnmatchedMatchBacklogByFolder(ctx, libraryID, h.rawMatchBacklogMode())
		if err != nil {
			return resp, err
		}
		resp.RawFileCount = count
	}
	resp.TotalCount = resp.MovieCount + resp.SeriesCount + resp.RawFileCount
	return resp, nil
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

	folder, err := h.folderRepo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	job, err := h.JobRepo.CreateLibraryRefresh(r.Context(), currentAdminUserID(r), adminjob.LibraryRefreshRequest{
		LibraryID:   folder.ID,
		LibraryName: folder.Name,
		Mode:        mode,
	}, fmt.Sprintf("Queued %s library metadata refresh", mode))
	if err != nil {
		var conflict *adminjob.ActiveJobConflictError
		if errors.As(err, &conflict) {
			jobsHandler := NewAdminJobsHandler(nil, nil)
			writeAdminJobConflict(w, "A metadata refresh is already queued or running for this library", conflict.Job, jobsHandler, r)
			return
		}
		slog.Error("library: queue library refresh failed", "library_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to queue library metadata refresh")
		return
	}
	publishEventJob(r.Context(), h.EventsHub, "job.created", job)

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

	if err := h.folderRepo.AllowEmptyCleanupOnce(r.Context(), id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to confirm cleanup")
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

	folder, err := h.folderRepo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	// Parse multipart form (max 10 MB).
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid multipart form")
		return
	}

	file, header, err := r.FormFile("poster")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Missing poster file")
		return
	}
	defer file.Close()

	// Validate content type.
	ct := header.Header.Get("Content-Type")
	ext := posterExtension(ct)
	if ext == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Unsupported image type; use JPEG, PNG, or WebP")
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, 10<<20+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read upload")
		return
	}
	if len(data) > 10<<20 {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Poster must be under 10 MB")
		return
	}

	// Delete old poster if it exists and has a different key.
	s3Key := fmt.Sprintf("library-posters/%d%s", id, ext)
	if folder.PosterPath != "" && folder.PosterPath != s3Key {
		_ = h.S3Meta.DeleteObject(r.Context(), h.S3Meta.Bucket(), folder.PosterPath)
	}

	if err := h.S3Meta.PutObject(r.Context(), h.S3Meta.Bucket(), s3Key, data); err != nil {
		slog.Error("uploading library poster", "library_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to upload poster")
		return
	}

	if err := h.folderRepo.SetPosterPath(r.Context(), id, s3Key); err != nil {
		slog.Error("saving library poster path", "library_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save poster")
		return
	}

	folder.PosterPath = s3Key
	writeJSON(w, http.StatusOK, h.toLibraryResponseWithPoster(r.Context(), folder))
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

	folder, err := h.folderRepo.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	if folder.PosterPath != "" {
		_ = h.S3Meta.DeleteObject(r.Context(), h.S3Meta.Bucket(), folder.PosterPath)
		if err := h.folderRepo.ClearPosterPath(r.Context(), id); err != nil {
			slog.Error("clearing library poster path", "library_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear poster")
			return
		}
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

	// Verify the library exists.
	if _, err := h.folderRepo.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		slog.Error("fetching library for provider chain", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	entries, err := h.ChainRepo.GetAllChainEntries(r.Context(), id)
	if err != nil {
		slog.Error("getting provider chain", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get provider chain")
		return
	}

	// Group by content level — capability_id is the provider slug.
	levels := make(map[string][]chainLevelEntry)
	for _, e := range entries {
		levels[e.ContentLevel] = append(levels[e.ContentLevel], chainLevelEntry{
			PluginInstallationID: e.PluginInstallationID,
			CapabilityID:         e.CapabilityID,
			ProviderSlug:         e.CapabilityID,
			Priority:             e.Priority,
			Enabled:              e.Enabled,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"levels": levels})
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

	// Verify the library exists.
	if _, err := h.folderRepo.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, catalog.ErrFolderNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Library not found")
			return
		}
		slog.Error("fetching library for provider chain update", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch library")
		return
	}

	var req setChainLevelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	var entries []metadata.ChainEntry
	for level, inputs := range req.Levels {
		for _, input := range inputs {
			entries = append(entries, metadata.ChainEntry{
				PluginInstallationID: input.PluginInstallationID,
				CapabilityID:         input.CapabilityID,
				CapabilityType:       "metadata_provider.v1",
				ContentLevel:         level,
				Priority:             input.Priority,
				Enabled:              input.Enabled,
			})
		}
	}

	if err := h.ChainRepo.SetChain(r.Context(), id, entries); err != nil {
		slog.Error("setting provider chain", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to set provider chain")
		return
	}
	if h.chainCacheInvalidator != nil {
		h.chainCacheInvalidator.InvalidateChainCache()
	}

	w.WriteHeader(http.StatusNoContent)
}

// seedDefaultChain builds a default provider chain from plugin manifest defaults
// for the given library type. Returns entries for all applicable content levels.
func (h *LibraryHandler) seedDefaultChain(ctx context.Context, libraryType string) []metadata.ChainEntry {
	if h.ChainRepo == nil {
		return nil
	}

	caps, err := metadata.ListEnabledMetadataCapabilities(ctx, h.ChainRepo.Pool())
	if err != nil {
		slog.Warn("seed chain: failed to list metadata capabilities", "error", err)
		return nil
	}

	levels := metadataContentLevelsForLibraryType(libraryType)
	if len(levels) == 0 {
		return nil
	}

	var entries []metadata.ChainEntry
	for _, level := range levels {
		type candidate struct {
			installationID int
			capabilityID   string
			priority       int
			enabled        bool
		}
		var candidates []candidate

		for _, c := range caps {
			defaultPriority := metadata.LookupDefaultPriority(ctx, h.ChainRepo.Pool(), c.PluginInstallationID, c.CapabilityID, level)
			if defaultPriority > 0 {
				candidates = append(candidates, candidate{
					installationID: c.PluginInstallationID,
					capabilityID:   c.CapabilityID,
					priority:       defaultPriority,
					enabled:        true,
				})
			} else {
				// Plugin doesn't declare this level — include but disabled.
				candidates = append(candidates, candidate{
					installationID: c.PluginInstallationID,
					capabilityID:   c.CapabilityID,
					priority:       999,
					enabled:        false,
				})
			}
		}

		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].priority < candidates[j].priority
		})

		for i, cand := range candidates {
			entries = append(entries, metadata.ChainEntry{
				PluginInstallationID: cand.installationID,
				CapabilityID:         cand.capabilityID,
				CapabilityType:       "metadata_provider.v1",
				ContentLevel:         level,
				Priority:             i,
				Enabled:              cand.enabled,
			})
		}
	}

	return entries
}

func metadataContentLevelsForLibraryType(libraryType string) []string {
	switch libraryType {
	case "series":
		return []string{"series", "season", "episode"}
	case "movies", "movie":
		return []string{"movie"}
	case "audiobooks", "audiobook":
		return []string{"audiobook"}
	case "ebooks", "ebook":
		return []string{"ebook"}
	case "manga":
		return []string{"manga"}
	case "mixed":
		return []string{"movie", "series", "season", "episode", "audiobook", "ebook"}
	default:
		return nil
	}
}

// HandleListStaleIDs handles GET /libraries/stale-ids.
func (h *LibraryHandler) HandleListStaleIDs(w http.ResponseWriter, r *http.Request) {
	if h.StaleIDRepo == nil {
		writeJSON(w, http.StatusOK, []staleMediaIDResponse{})
		return
	}

	staleIDs, err := h.StaleIDRepo.ListAll(r.Context())
	if err != nil {
		slog.Error("listing stale media IDs", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list stale IDs")
		return
	}
	if len(staleIDs) == 0 {
		writeJSON(w, http.StatusOK, []staleMediaIDResponse{})
		return
	}

	// Collect unique content IDs for batch lookup.
	contentIDs := make([]string, 0, len(staleIDs))
	seen := make(map[string]bool, len(staleIDs))
	for _, s := range staleIDs {
		if !seen[s.ContentID] {
			contentIDs = append(contentIDs, s.ContentID)
			seen[s.ContentID] = true
		}
	}

	// Batch-load item metadata and library associations.
	type itemInfo struct {
		Title       string
		Year        int
		ContentType string
		LibraryID   int
		LibraryName string
	}
	items := make(map[string]itemInfo, len(contentIDs))

	rows, err := h.pool.Query(r.Context(), `
		SELECT mi.content_id, mi.title, mi.year, mi.type,
		       COALESCE(mf_lib.folder_id, 0),
		       COALESCE(mf_lib.folder_name, '')
		FROM media_items mi
		LEFT JOIN LATERAL (
			SELECT mf2.media_folder_id AS folder_id, f.name AS folder_name
			FROM media_files mf2
			JOIN media_folders f ON f.id = mf2.media_folder_id
			WHERE mf2.content_id = mi.content_id
			LIMIT 1
		) mf_lib ON true
		WHERE mi.content_id = ANY($1)
	`, contentIDs)
	if err != nil {
		slog.Error("loading items for stale IDs", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load item data")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var cid, title, ctype, libName string
		var year, libID int
		if err := rows.Scan(&cid, &title, &year, &ctype, &libID, &libName); err != nil {
			slog.Error("scanning item for stale IDs", "error", err)
			continue
		}
		items[cid] = itemInfo{Title: title, Year: year, ContentType: ctype, LibraryID: libID, LibraryName: libName}
	}

	resp := make([]staleMediaIDResponse, 0, len(staleIDs))
	for _, s := range staleIDs {
		info := items[s.ContentID]
		resp = append(resp, staleMediaIDResponse{
			ContentID:   s.ContentID,
			LibraryID:   info.LibraryID,
			LibraryName: info.LibraryName,
			Title:       info.Title,
			Year:        info.Year,
			ContentType: info.ContentType,
			Provider:    s.Provider,
			ProviderID:  s.ProviderID,
			FirstSeenAt: s.FirstSeenAt.Format("2006-01-02T15:04:05Z"),
			LastSeenAt:  s.LastSeenAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleRematchStaleID handles POST /libraries/stale-ids/{contentID}/rematch.
// Deprecated: prefer the explicit admin match search/apply flow via
// POST /admin/items/{id}/match/search and POST /admin/items/{id}/match/apply.
func (h *LibraryHandler) HandleRematchStaleID(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "contentID")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Missing content ID")
		return
	}

	// Clear the stale external IDs from the media_items row.
	// We clear all provider IDs so the re-match starts fresh from title/year.
	_, err := h.pool.Exec(r.Context(), `
		UPDATE media_items
		SET tmdb_id = '', tvdb_id = '', imdb_id = ''
		WHERE content_id = $1
	`, contentID)
	if err != nil {
		slog.Error("clearing stale IDs from media item", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear IDs")
		return
	}

	// Remove stale_media_ids records.
	if h.StaleIDRepo != nil {
		if err := h.StaleIDRepo.DeleteByContentID(r.Context(), contentID); err != nil {
			slog.Error("deleting stale media ID records", "content_id", contentID, "error", err)
		}
	}

	// Re-trigger metadata match.
	if h.refresher != nil {
		go func() {
			if err := h.refresher.RefreshItem(h.appCtx, contentID); err != nil {
				slog.Warn("metadata: rematch refresh failed", "content_id", contentID, "error", err)
			}
		}()
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *LibraryHandler) HandleListRoots(w http.ResponseWriter, r *http.Request) {
	if h.ScannedGroupRepo == nil || h.folderRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Group snapshots not configured")
		return
	}

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
	state := strings.TrimSpace(q.Get("state"))

	folder, err := h.folderRepo.GetByID(r.Context(), libraryID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Library not found")
		return
	}

	groups, total, err := h.ScannedGroupRepo.ListByFolder(r.Context(), libraryID, state, limit, offset)
	if err != nil {
		slog.Error("listing scanned groups", "library_id", libraryID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list roots")
		return
	}

	overrideByGroup := map[string]models.MediaGroupOverride{}
	if h.GroupOverrideRepo != nil {
		overrides, err := h.GroupOverrideRepo.ListByFolder(r.Context(), libraryID)
		if err != nil {
			slog.Warn("listing group overrides", "library_id", libraryID, "error", err)
		} else {
			for _, override := range overrides {
				overrideByGroup[groupOverrideLookupKey(override.GroupKeyVersion, override.ContentGroupKey)] = override
			}
		}
	}

	items := make([]libraryRootResponse, 0, len(groups))
	for _, group := range groups {
		rootPath := strings.TrimSpace(group.SampleObservedRootPath)
		if rootPath == "" {
			rootPath = filepath.Dir(group.SampleFilePath)
		}
		resp := libraryRootResponse{
			LibraryID:      libraryID,
			LibraryName:    folder.Name,
			RootPath:       rootPath,
			State:          group.State,
			InferredType:   group.InferredType,
			TypeConfidence: group.TypeConfidence,
			Title:          group.BaseTitle,
			Year:           group.BaseYear,
			TmdbID:         group.TmdbID,
			ImdbID:         group.ImdbID,
			TvdbID:         group.TvdbID,
			ObservedFiles:  group.ObservedFileCount,
			SampleFilePath: group.SampleFilePath,
			Evidence:       append(json.RawMessage(nil), group.EvidenceJSON...),
			OverrideSource: group.OverrideSource,
			FirstSeenAt:    group.FirstSeenAt,
			LastSeenAt:     group.LastSeenAt,
		}
		if override, ok := overrideByGroup[groupOverrideLookupKey(group.GroupKeyVersion, group.ContentGroupKey)]; ok {
			resp.ActiveOverride = &rootOverride{
				ForcedType:   override.ForcedType,
				ForcedTitle:  override.ForcedTitle,
				ForcedYear:   override.ForcedYear,
				ForcedTmdbID: override.ForcedTmdbID,
				ForcedImdbID: override.ForcedImdbID,
				ForcedTvdbID: override.ForcedTvdbID,
				Note:         override.Note,
			}
		}
		items = append(items, resp)
	}

	writeJSON(w, http.StatusOK, libraryRootsListResponse{Items: items, Total: total})
}

func (h *LibraryHandler) HandleUpsertRootOverride(w http.ResponseWriter, r *http.Request) {
	if h.GroupOverrideRepo == nil || h.ObservedLocationRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Root overrides not configured")
		return
	}

	var req rootOverrideUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	req.RootPath = filepath.Clean(strings.TrimSpace(req.RootPath))
	if req.LibraryID <= 0 || req.RootPath == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "library_id and root_path are required")
		return
	}
	location, err := h.ObservedLocationRepo.Get(r.Context(), req.LibraryID, req.RootPath)
	if err != nil {
		slog.Error("loading observed media location", "library_id", req.LibraryID, "root_path", req.RootPath, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load root")
		return
	}
	if location == nil || location.PrimaryContentGroupKey == "" {
		if location != nil && location.ContentGroupCount > 1 {
			writeError(w, http.StatusConflict, "ambiguous_root", "Root contains multiple logical groups; override the group after splitting or selecting a specific item")
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "Root not found")
		return
	}

	userID := apimw.GetUserID(r.Context())
	override := models.MediaGroupOverride{
		MediaFolderID:   req.LibraryID,
		GroupKeyVersion: location.PrimaryGroupKeyVersion,
		ContentGroupKey: location.PrimaryContentGroupKey,
		ForcedType:      strings.TrimSpace(req.ForcedType),
		ForcedTitle:     strings.TrimSpace(req.ForcedTitle),
		ForcedYear:      req.ForcedYear,
		ForcedTmdbID:    strings.TrimSpace(req.ForcedTmdbID),
		ForcedImdbID:    strings.TrimSpace(req.ForcedImdbID),
		ForcedTvdbID:    strings.TrimSpace(req.ForcedTvdbID),
		Note:            strings.TrimSpace(req.Note),
		CreatedByUserID: nil,
		UpdatedByUserID: nil,
	}
	if userID > 0 {
		override.CreatedByUserID = &userID
		override.UpdatedByUserID = &userID
	}
	if err := h.GroupOverrideRepo.Upsert(r.Context(), override); err != nil {
		slog.Error("upserting group override", "library_id", req.LibraryID, "root_path", req.RootPath, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save override")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LibraryHandler) HandleDeleteRootOverride(w http.ResponseWriter, r *http.Request) {
	if h.GroupOverrideRepo == nil || h.ObservedLocationRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Root overrides not configured")
		return
	}

	var req rootOverrideDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	req.RootPath = filepath.Clean(strings.TrimSpace(req.RootPath))
	if req.LibraryID <= 0 || req.RootPath == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "library_id and root_path are required")
		return
	}
	location, err := h.ObservedLocationRepo.Get(r.Context(), req.LibraryID, req.RootPath)
	if err != nil {
		slog.Error("loading observed media location", "library_id", req.LibraryID, "root_path", req.RootPath, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load root")
		return
	}
	if location == nil || location.PrimaryContentGroupKey == "" {
		if location != nil && location.ContentGroupCount > 1 {
			writeError(w, http.StatusConflict, "ambiguous_root", "Root contains multiple logical groups; delete the override from a specific group instead")
			return
		}
		writeError(w, http.StatusNotFound, "not_found", "Root not found")
		return
	}

	if err := h.GroupOverrideRepo.Delete(r.Context(), req.LibraryID, location.PrimaryGroupKeyVersion, location.PrimaryContentGroupKey); err != nil {
		slog.Error("deleting group override", "library_id", req.LibraryID, "root_path", req.RootPath, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete override")
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
	if h.pool == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Database not configured")
		return
	}

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

	// Optional case-insensitive search across title, library name, type, and
	// status. Applied server-side so it spans the whole table, not just the
	// current page. The displayed folder still comes from the lateral join below,
	// but the search predicate checks every membership so multi-library items are
	// found when any linked library name matches.
	search := strings.TrimSpace(q.Get("q"))
	filter := ""
	filterArgs := []any{}
	if search != "" {
		filterArgs = append(filterArgs, "%"+search+"%")
		filter = ` AND (
			mi.title ILIKE $1
			OR mi.type ILIKE $1
			OR mi.status ILIKE $1
			OR EXISTS (
				SELECT 1
				FROM media_item_libraries search_mil
				JOIN media_folders search_f ON search_f.id = search_mil.media_folder_id
				WHERE search_mil.content_id = mi.content_id
				  AND search_f.name ILIKE $1
			)
		)`
	}

	// Manga chapters carry their series' match state; the chapter rows
	// themselves stay 'pending' and are resolved through the manga series,
	// so they must not surface as actionable unmatched items here.
	mangaChapterGuard := ` AND ` + catalog.MangaChapterExclusionWhere("mi")

	countSQL := `
		SELECT COUNT(*)
		FROM media_items mi
		WHERE mi.status IN ('unmatched', 'pending', 'ambiguous')` + mangaChapterGuard
	countSQL += filter

	var total int
	if err := h.pool.QueryRow(r.Context(), countSQL, filterArgs...).Scan(&total); err != nil {
		slog.Error("counting unmatched items", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to count unmatched items")
		return
	}

	listArgs := append(append([]any{}, filterArgs...), limit, offset)
	listSQL := fmt.Sprintf(`
		SELECT mi.content_id, mi.title, mi.year, mi.type, mi.status,
		       COALESCE(lib.folder_id, 0),
		       COALESCE(lib.folder_name, '')
		FROM media_items mi
		LEFT JOIN LATERAL (
			SELECT mil.media_folder_id AS folder_id, f.name AS folder_name
			FROM media_item_libraries mil
			JOIN media_folders f ON f.id = mil.media_folder_id
			WHERE mil.content_id = mi.content_id
			LIMIT 1
		) lib ON true
		WHERE mi.status IN ('unmatched', 'pending', 'ambiguous')%s%s
		ORDER BY mi.title ASC, mi.content_id ASC
		LIMIT $%d OFFSET $%d
	`, mangaChapterGuard, filter, len(filterArgs)+1, len(filterArgs)+2)

	rows, err := h.pool.Query(r.Context(), listSQL, listArgs...)
	if err != nil {
		slog.Error("listing unmatched items", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list unmatched items")
		return
	}
	defer rows.Close()

	items := make([]unmatchedItemResponse, 0)
	for rows.Next() {
		var item unmatchedItemResponse
		if err := rows.Scan(&item.ContentID, &item.Title, &item.Year, &item.ContentType, &item.Status, &item.LibraryID, &item.LibraryName); err != nil {
			slog.Error("scanning unmatched item", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to scan item")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		slog.Error("iterating unmatched items", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to iterate items")
		return
	}

	writeJSON(w, http.StatusOK, unmatchedItemsListResponse{
		Items: items,
		Total: total,
	})
}

// parseIDParam extracts and parses the "id" URL parameter as an integer.
func parseIDParam(r *http.Request) (int, error) {
	idStr := chi.URLParam(r, "id")
	return strconv.Atoi(idStr)
}
