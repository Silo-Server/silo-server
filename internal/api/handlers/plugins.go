package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/uploads"
)

const (
	maxPluginUploadSize      = 256 << 20
	maxPluginUploadChunkSize = 1 << 20
	defaultPluginChunkSize   = 512 << 10
)

type PluginHandler struct {
	repositories  *plugins.RepositoryStore
	installations *plugins.InstallationStore
	configs       *plugins.RuntimeConfigStore
	service       *plugins.Service
	userConfig    *plugins.UserConfigStore
	proxy         *plugins.HTTPProxy
	chainRepo     *metadata.ChainRepository
	imageResolver *metadata.PluginImageResolver
	uploads       *uploads.Manager
	restartStatus *ServerRestartStatusTracker
}

func NewPluginHandler(
	repositories *plugins.RepositoryStore,
	installations *plugins.InstallationStore,
	configs *plugins.RuntimeConfigStore,
	service *plugins.Service,
	userConfig *plugins.UserConfigStore,
	proxy *plugins.HTTPProxy,
	chainRepo *metadata.ChainRepository,
	imageResolver *metadata.PluginImageResolver,
	restartStatus *ServerRestartStatusTracker,
) *PluginHandler {
	return &PluginHandler{
		repositories:  repositories,
		installations: installations,
		configs:       configs,
		service:       service,
		userConfig:    userConfig,
		proxy:         proxy,
		chainRepo:     chainRepo,
		imageResolver: imageResolver,
		restartStatus: restartStatus,
		uploads: uploads.NewManager(uploads.ManagerOptions{
			MaxSize:      maxPluginUploadSize,
			MaxChunkSize: maxPluginUploadChunkSize,
		}),
	}
}

type pluginRepositoryRequest struct {
	URL         string `json:"url"`
	DisplayName string `json:"display_name"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type pluginCatalogSettingsRequest struct {
	IncludeApprovedCommunityPlugins *bool `json:"include_approved_community_plugins"`
}

type pluginInstallationCreateRequest struct {
	RepositoryID *int   `json:"repository_id,omitempty"`
	PluginID     string `json:"plugin_id,omitempty"`
	Version      string `json:"version,omitempty"`
	ArchiveURL   string `json:"archive_url,omitempty"`
}

type pluginInstallationUpdateRequest struct {
	Enabled      *bool   `json:"enabled,omitempty"`
	UpdatePolicy *string `json:"update_policy,omitempty"`
}

type pluginConfigRequest struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

type pluginAuthBindingRequest struct {
	CapabilityID  string `json:"capability_id"`
	Enabled       bool   `json:"enabled"`
	DisplayOrder  int    `json:"display_order"`
	AutoProvision bool   `json:"auto_provision"`
	DefaultLogin  bool   `json:"default_login"`
}

type pluginTaskBindingRequest struct {
	Enabled bool           `json:"enabled"`
	Trigger map[string]any `json:"trigger"`
}

type userPluginSettingsRequest struct {
	Values map[string]string `json:"values"`
}

type pluginChunkedUploadCreateRequest struct {
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
	ChunkSize int64  `json:"chunk_size,omitempty"`
}

type pluginRepositoryResponse struct {
	ID            int        `json:"id"`
	URL           string     `json:"url"`
	DisplayName   string     `json:"display_name"`
	Enabled       bool       `json:"enabled"`
	SourceKind    string     `json:"source_kind"`
	Managed       bool       `json:"managed"`
	LastFetchedAt *time.Time `json:"last_fetched_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type pluginCatalogResponse struct {
	RepositoryID       int                      `json:"repository_id"`
	PluginID           string                   `json:"plugin_id"`
	Version            string                   `json:"version"`
	ArchiveURL         string                   `json:"archive_url"`
	SourceKind         string                   `json:"source_kind"`
	RepositoryName     string                   `json:"repository_name"`
	RepoURL            string                   `json:"repo_url,omitempty"`
	Presentation       *pluginPresentationJSON  `json:"presentation,omitempty"`
	Capabilities       []pluginCapabilityJSON   `json:"capabilities"`
	GlobalConfigSchema []pluginConfigSchemaJSON `json:"global_config_schema"`
	UserConfigSchema   []pluginConfigSchemaJSON `json:"user_config_schema"`
	Routes             []pluginRouteJSON        `json:"routes"`
	Assets             []pluginAssetJSON        `json:"assets"`
	Metadata           map[string]any           `json:"metadata,omitempty"`
}

type pluginInstallationResponse struct {
	ID                 int                      `json:"id"`
	RepositoryID       *int                     `json:"repository_id,omitempty"`
	PluginID           string                   `json:"plugin_id"`
	Version            string                   `json:"version"`
	InstallPath        string                   `json:"install_path"`
	Enabled            bool                     `json:"enabled"`
	Kind               string                   `json:"kind"`
	UpdatePolicy       string                   `json:"update_policy"`
	AvailableVersion   *string                  `json:"available_version,omitempty"`
	SourceKind         string                   `json:"source_kind"`
	RepositoryName     string                   `json:"repository_name,omitempty"`
	RepoURL            string                   `json:"repo_url,omitempty"`
	Presentation       *pluginPresentationJSON  `json:"presentation,omitempty"`
	UpdatesPaused      bool                     `json:"updates_paused"`
	Capabilities       []pluginCapabilityJSON   `json:"capabilities"`
	GlobalConfigSchema []pluginConfigSchemaJSON `json:"global_config_schema"`
	UserConfigSchema   []pluginConfigSchemaJSON `json:"user_config_schema"`
	Routes             []pluginRouteJSON        `json:"routes"`
	Assets             []pluginAssetJSON        `json:"assets"`
	Metadata           map[string]any           `json:"metadata,omitempty"`
	GlobalConfigs      []pluginConfigValueJSON  `json:"global_configs"`
	AuthBindings       []pluginAuthBindingJSON  `json:"auth_bindings"`
	TaskBindings       []pluginTaskBindingJSON  `json:"task_bindings"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
}

type pluginCatalogSettingsResponse struct {
	IncludeApprovedCommunityPlugins bool `json:"include_approved_community_plugins"`
	ApprovedCommunityPluginCount    int  `json:"approved_community_plugin_count"`
	InstalledCommunityPluginCount   int  `json:"installed_community_plugin_count"`
	MigratedPluginCount             int  `json:"migrated_plugin_count"`
	CommunityUpdatesPaused          bool `json:"community_updates_paused"`
}

type pluginPresentationJSON struct {
	DisplayName         string `json:"display_name"`
	Summary             string `json:"summary"`
	DescriptionMarkdown string `json:"description_markdown"`
	SetupMarkdown       string `json:"setup_markdown"`
	HomepageURL         string `json:"homepage_url"`
	SourceURL           string `json:"source_url"`
	SupportURL          string `json:"support_url"`
	ChangelogURL        string `json:"changelog_url"`
	PublisherName       string `json:"publisher_name"`
	PublisherURL        string `json:"publisher_url"`
	LicenseSPDX         string `json:"license_spdx"`
}

type pluginConfigSchemaJSON struct {
	Key         string               `json:"key"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	JSONSchema  string               `json:"json_schema"`
	Required    bool                 `json:"required"`
	AdminForm   *pluginAdminFormJSON `json:"admin_form,omitempty"`
}

type pluginAdminFormJSON struct {
	Fields      []pluginAdminFormFieldJSON   `json:"fields"`
	SubmitLabel string                       `json:"submit_label,omitempty"`
	Sections    []pluginAdminFormSectionJSON `json:"sections,omitempty"`
}

type pluginAdminFormFieldJSON struct {
	Key                 string                         `json:"key"`
	Label               string                         `json:"label"`
	Description         string                         `json:"description,omitempty"`
	Control             string                         `json:"control"`
	Placeholder         string                         `json:"placeholder,omitempty"`
	Required            bool                           `json:"required"`
	Secret              bool                           `json:"secret"`
	Multiline           bool                           `json:"multiline"`
	DefaultValue        any                            `json:"default_value,omitempty"`
	Options             []pluginAdminFormOptionJSON    `json:"options,omitempty"`
	Rows                int32                          `json:"rows,omitempty"`
	DynamicOptions      bool                           `json:"dynamic_options,omitempty"`
	ShowWhen            []pluginAdminFormConditionJSON `json:"show_when,omitempty"`
	Validation          *pluginAdminFormValidationJSON `json:"validation,omitempty"`
	ExclusiveGroupField string                         `json:"exclusive_group_field,omitempty"`
}

type pluginAdminFormOptionJSON struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type pluginAdminFormConditionJSON struct {
	Field  string   `json:"field"`
	Equals []string `json:"equals"`
}

type pluginAdminFormValidationJSON struct {
	HasMin    bool    `json:"has_min,omitempty"`
	Min       float64 `json:"min,omitempty"`
	HasMax    bool    `json:"has_max,omitempty"`
	Max       float64 `json:"max,omitempty"`
	Pattern   string  `json:"pattern,omitempty"`
	MinLength int32   `json:"min_length,omitempty"`
	MaxLength int32   `json:"max_length,omitempty"`
}

type pluginAdminFormSectionJSON struct {
	Key              string                         `json:"key"`
	Title            string                         `json:"title"`
	Description      string                         `json:"description,omitempty"`
	Collapsible      bool                           `json:"collapsible"`
	CollapsedDefault bool                           `json:"collapsed_default"`
	FieldKeys        []string                       `json:"field_keys"`
	ShowWhen         []pluginAdminFormConditionJSON `json:"show_when,omitempty"`
}

type pluginCapabilityJSON struct {
	Type          string                   `json:"type"`
	ID            string                   `json:"id"`
	DisplayName   string                   `json:"display_name"`
	Description   string                   `json:"description"`
	Subscriptions []string                 `json:"subscriptions,omitempty"`
	ConfigSchema  []pluginConfigSchemaJSON `json:"config_schema,omitempty"`
	Metadata      map[string]any           `json:"metadata,omitempty"`
}

type pluginRouteJSON struct {
	ID              string `json:"id"`
	Method          string `json:"method"`
	Path            string `json:"path"`
	Access          string `json:"access"`
	Navigable       bool   `json:"navigable"`
	NavigationLabel string `json:"navigation_label"`
	NavigationKind  string `json:"navigation_kind"`
	StaticAsset     bool   `json:"static_asset"`
}

type pluginAssetJSON struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Integrity   string `json:"integrity"`
}

type pluginConfigValueJSON struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

type pluginAuthBindingJSON struct {
	CapabilityID  string    `json:"capability_id"`
	Enabled       bool      `json:"enabled"`
	DisplayOrder  int       `json:"display_order"`
	AutoProvision bool      `json:"auto_provision"`
	DefaultLogin  bool      `json:"default_login"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type pluginTaskBindingJSON struct {
	CapabilityID string         `json:"capability_id"`
	Enabled      bool           `json:"enabled"`
	Trigger      map[string]any `json:"trigger"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type pluginUserSettingsSummary struct {
	ID               int                      `json:"id"`
	PluginID         string                   `json:"plugin_id"`
	Version          string                   `json:"version"`
	UserConfigSchema []pluginConfigSchemaJSON `json:"user_config_schema"`
	Routes           []pluginRouteJSON        `json:"routes"`
	Assets           []pluginAssetJSON        `json:"assets"`
	// Category is the manifest's optional slash-delimited grouping path
	// (e.g. "Tools/Utilities") used to group plugin entries in the
	// user-facing Apps navigation. Empty (omitted) when the manifest
	// declares no category. Additive-only per v1 API rules.
	Category string `json:"category,omitempty"`
}

type pluginUserSettingsListResponse struct {
	Installations []pluginUserSettingsSummary `json:"installations"`
}

type pluginUserSettingsDetailResponse struct {
	Installation pluginUserSettingsSummary `json:"installation"`
	Values       map[string]string         `json:"values"`
}

type pluginTaskBindingUpdateResponse struct {
	RestartRequired bool `json:"restart_required"`
}

type pluginChunkedUploadSessionResponse struct {
	UploadID       string    `json:"upload_id"`
	Filename       string    `json:"filename"`
	SizeBytes      int64     `json:"size_bytes"`
	ChunkSize      int64     `json:"chunk_size"`
	TotalChunks    int       `json:"total_chunks"`
	ReceivedChunks int       `json:"received_chunks"`
	ReceivedBytes  int64     `json:"received_bytes"`
	Complete       bool      `json:"complete"`
	ExpiresAt      time.Time `json:"expires_at"`
}

func (h *PluginHandler) HandleListRepositories(w http.ResponseWriter, r *http.Request) {
	repositories, err := h.repositories.List(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "listing plugin repositories", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list plugin repositories")
		return
	}

	response := make([]pluginRepositoryResponse, 0, len(repositories))
	for _, repository := range repositories {
		response = append(response, toPluginRepositoryResponse(repository))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *PluginHandler) HandleCreateRepository(w http.ResponseWriter, r *http.Request) {
	var req pluginRepositoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.URL) == "" || strings.TrimSpace(req.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "url and display_name are required")
		return
	}
	if req.URL == plugins.DefaultRepositoryURL || req.URL == plugins.ApprovedCommunityRepositoryURL {
		writeError(w, http.StatusBadRequest, "managed_repository", "Use catalog settings to manage built-in plugin repositories")
		return
	}

	repository, err := h.repositories.Create(r.Context(), plugins.CreateRepositoryInput{
		URL:         req.URL,
		DisplayName: req.DisplayName,
		Enabled:     req.Enabled,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "creating plugin repository", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create plugin repository")
		return
	}

	writeJSON(w, http.StatusCreated, toPluginRepositoryResponse(repository))
}

func (h *PluginHandler) HandleUpdateRepository(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid repository ID")
		return
	}

	var req pluginRepositoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	input := plugins.UpdateRepositoryInput{
		Enabled: req.Enabled,
	}
	if strings.TrimSpace(req.URL) != "" {
		input.URL = &req.URL
	}
	if strings.TrimSpace(req.DisplayName) != "" {
		input.DisplayName = &req.DisplayName
	}

	if err := h.repositories.Update(r.Context(), id, input); err != nil {
		if errors.Is(err, plugins.ErrRepositoryNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Plugin repository not found")
			return
		}
		if errors.Is(err, plugins.ErrManagedRepositoryReadOnly) {
			writeError(w, http.StatusConflict, "managed_repository", "Managed plugin repositories are controlled by catalog settings")
			return
		}
		slog.ErrorContext(r.Context(), "updating plugin repository", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update plugin repository")
		return
	}

	repository, err := h.repositories.GetByID(r.Context(), id)
	if err != nil {
		slog.ErrorContext(r.Context(), "loading updated plugin repository", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load plugin repository")
		return
	}

	writeJSON(w, http.StatusOK, toPluginRepositoryResponse(repository))
}

func (h *PluginHandler) HandleDeleteRepository(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid repository ID")
		return
	}

	if err := h.repositories.Delete(r.Context(), id); err != nil {
		if errors.Is(err, plugins.ErrRepositoryNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Plugin repository not found")
			return
		}
		if errors.Is(err, plugins.ErrManagedRepositoryReadOnly) {
			writeError(w, http.StatusConflict, "managed_repository", "Managed plugin repositories cannot be deleted")
			return
		}
		slog.ErrorContext(r.Context(), "deleting plugin repository", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete plugin repository")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *PluginHandler) HandleGetCatalogSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.repositories.GetCatalogSettings(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "loading plugin catalog settings", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load plugin catalog settings")
		return
	}
	writeJSON(w, http.StatusOK, toPluginCatalogSettingsResponse(settings))
}

func (h *PluginHandler) HandlePutCatalogSettings(w http.ResponseWriter, r *http.Request) {
	var req pluginCatalogSettingsRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "Request body must contain one JSON object")
		return
	}
	if req.IncludeApprovedCommunityPlugins == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "include_approved_community_plugins is required")
		return
	}

	settings, err := h.repositories.SetIncludeApprovedCommunity(r.Context(), *req.IncludeApprovedCommunityPlugins)
	if err != nil {
		slog.ErrorContext(r.Context(), "updating plugin catalog settings", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update plugin catalog settings")
		return
	}
	writeJSON(w, http.StatusOK, toPluginCatalogSettingsResponse(settings))
}

func (h *PluginHandler) HandleCatalog(w http.ResponseWriter, r *http.Request) {
	entries, err := h.service.FetchCatalog(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "fetching plugin catalog", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch plugin catalog")
		return
	}

	response := make([]pluginCatalogResponse, 0, len(entries))
	for _, entry := range entries {
		presentation := toPluginPresentationJSON(entry.Manifest.GetPresentation())
		repoURL := entry.RepoURL
		if repoURL == "" && presentation != nil {
			repoURL = presentation.SourceURL
		}
		response = append(response, pluginCatalogResponse{
			RepositoryID:       entry.RepositoryID,
			PluginID:           entry.Manifest.GetPluginId(),
			Version:            entry.Manifest.GetVersion(),
			ArchiveURL:         entry.ArchiveURL,
			SourceKind:         entry.SourceKind,
			RepositoryName:     entry.RepositoryDisplayName,
			RepoURL:            repoURL,
			Presentation:       presentation,
			Capabilities:       capabilitiesToJSON(entry.Manifest.GetCapabilities()),
			GlobalConfigSchema: configSchemasToJSON(entry.Manifest.GetGlobalConfigSchema()),
			UserConfigSchema:   configSchemasToJSON(entry.Manifest.GetUserConfigSchema()),
			Routes:             routesToJSON(entry.Manifest.GetHttpRoutes()),
			Assets:             assetsToJSON(entry.Manifest.GetAssets()),
			Metadata:           structToMap(entry.Manifest.GetMetadata()),
		})
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *PluginHandler) HandleListInstallations(w http.ResponseWriter, r *http.Request) {
	installations, err := h.installations.List(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "listing plugin installations", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list plugin installations")
		return
	}

	response, err := h.buildInstallationResponses(r.Context(), installations)
	if err != nil {
		slog.ErrorContext(r.Context(), "building plugin installations response", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build plugin installation response")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *PluginHandler) HandleCreateInstallation(w http.ResponseWriter, r *http.Request) {
	var req pluginInstallationCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	hasRepositoryFields := req.RepositoryID != nil || strings.TrimSpace(req.PluginID) != "" || strings.TrimSpace(req.Version) != ""

	var (
		result *plugins.InstallResult
		err    error
	)
	switch {
	case hasRepositoryFields:
		if req.RepositoryID == nil || strings.TrimSpace(req.PluginID) == "" || strings.TrimSpace(req.Version) == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "repository_id, plugin_id, and version are required")
			return
		}
		if strings.TrimSpace(req.ArchiveURL) != "" {
			writeError(w, http.StatusBadRequest, "bad_request", "archive_url cannot be combined with repository install fields")
			return
		}
		result, err = h.service.InstallCatalog(r.Context(), plugins.InstallCatalogRequest{
			RepositoryID: *req.RepositoryID,
			PluginID:     req.PluginID,
			Version:      req.Version,
		})
	default:
		if strings.TrimSpace(req.ArchiveURL) == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "archive_url is required")
			return
		}
		result, err = h.service.InstallRemote(r.Context(), plugins.InstallArchiveRequest{
			ArchiveURL:   req.ArchiveURL,
			RepositoryID: req.RepositoryID,
		})
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "installing plugin archive", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to install plugin")
		return
	}

	h.syncMetadataProviders(r.Context(), result.Installation)

	response, err := h.buildInstallationResponse(r.Context(), result.Installation, result.Manifest)
	if err != nil {
		slog.ErrorContext(r.Context(), "building installed plugin response", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build plugin installation response")
		return
	}

	writeJSON(w, http.StatusCreated, response)
}

func (h *PluginHandler) HandleUploadInstallation(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPluginUploadSize)
	if err := r.ParseMultipartForm(maxPluginUploadSize); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid plugin upload")
		return
	}

	file, header, err := r.FormFile("archive")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "archive upload is required")
		return
	}
	defer file.Close()

	tempFile, err := os.CreateTemp("", "silo-plugin-*.zip")
	if err != nil {
		slog.ErrorContext(r.Context(), "creating temp plugin upload file", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to process plugin upload")
		return
	}
	tempPath := tempFile.Name()
	defer func() {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	if _, err := io.Copy(tempFile, file); err != nil {
		slog.ErrorContext(r.Context(), "writing temp plugin upload file", "component", "api", "filename", header.Filename, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to process plugin upload")
		return
	}

	if err := tempFile.Close(); err != nil {
		slog.ErrorContext(r.Context(), "closing temp plugin upload file", "component", "api", "filename", header.Filename, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to process plugin upload")
		return
	}

	result, err := h.installUploadedPlugin(r.Context(), tempPath)
	if err != nil {
		slog.ErrorContext(r.Context(), "installing uploaded plugin", "component", "api", "filename", header.Filename, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to install uploaded plugin")
		return
	}

	h.writeUploadedPluginResponse(w, r, result)
}

func (h *PluginHandler) HandleCreateChunkedUpload(w http.ResponseWriter, r *http.Request) {
	var req pluginChunkedUploadCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.ChunkSize == 0 {
		req.ChunkSize = defaultPluginChunkSize
	}

	session, err := h.uploads.Create(uploads.CreateRequest{
		Filename:  req.Filename,
		SizeBytes: req.SizeBytes,
		ChunkSize: req.ChunkSize,
	})
	if err != nil {
		status, message := uploadErrorResponse(err)
		writeError(w, status, "upload_error", message)
		return
	}

	writeJSON(w, http.StatusCreated, toPluginChunkedUploadSessionResponse(session))
}

func (h *PluginHandler) HandleUploadChunk(w http.ResponseWriter, r *http.Request) {
	uploadID := chi.URLParam(r, "upload_id")
	chunkIndex, err := strconv.Atoi(chi.URLParam(r, "chunk_index"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid chunk index")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.uploads.MaxChunkSize()+1)
	defer r.Body.Close()

	session, err := h.uploads.PutChunk(r.Context(), uploadID, chunkIndex, r.Body, r.ContentLength)
	if err != nil {
		status, message := uploadErrorResponse(err)
		writeError(w, status, "upload_error", message)
		return
	}

	writeJSON(w, http.StatusOK, toPluginChunkedUploadSessionResponse(session))
}

func (h *PluginHandler) HandleCompleteChunkedUpload(w http.ResponseWriter, r *http.Request) {
	uploadID := chi.URLParam(r, "upload_id")
	upload, err := h.uploads.Complete(uploadID)
	if err != nil {
		status, message := uploadErrorResponse(err)
		writeError(w, status, "upload_error", message)
		return
	}
	defer upload.Cleanup()

	result, err := h.installUploadedPlugin(r.Context(), upload.Path)
	if err != nil {
		slog.ErrorContext(r.Context(), "installing chunked plugin upload", "component", "api",
			"filename", upload.Filename,
			"upload_id", upload.ID,
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to install uploaded plugin")
		return
	}

	h.writeUploadedPluginResponse(w, r, result)
}

func (h *PluginHandler) HandleCancelChunkedUpload(w http.ResponseWriter, r *http.Request) {
	if err := h.uploads.Cancel(chi.URLParam(r, "upload_id")); err != nil && !errors.Is(err, uploads.ErrNotFound) {
		status, message := uploadErrorResponse(err)
		writeError(w, status, "upload_error", message)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *PluginHandler) installUploadedPlugin(ctx context.Context, path string) (*plugins.InstallResult, error) {
	zipUpload, err := isZipUploadFile(path)
	if err != nil {
		return nil, err
	}
	if zipUpload {
		return h.service.InstallLocal(ctx, plugins.InstallArchiveRequest{
			ArchivePath: path,
		})
	}

	uploadData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read uploaded plugin binary: %w", err)
	}
	return h.service.InstallBinaryUpload(ctx, uploadData)
}

func (h *PluginHandler) writeUploadedPluginResponse(w http.ResponseWriter, r *http.Request, result *plugins.InstallResult) {
	h.syncMetadataProviders(r.Context(), result.Installation)

	response, err := h.buildInstallationResponse(r.Context(), result.Installation, result.Manifest)
	if err != nil {
		slog.ErrorContext(r.Context(), "building uploaded plugin response", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build plugin installation response")
		return
	}

	writeJSON(w, http.StatusCreated, response)
}

// syncMetadataProviders appends any metadata_provider.v1 capabilities from the
// given installation to all existing library chains.
func (h *PluginHandler) syncMetadataProviders(ctx context.Context, installation *plugins.Installation) {
	if h.chainRepo == nil {
		return
	}

	caps, err := h.installations.ListCapabilities(ctx, installation.ID)
	if err != nil {
		slog.ErrorContext(ctx, "listing capabilities for metadata provider sync", "component", "api",
			"installation_id", installation.ID, "error", err)
		return
	}

	for _, cap := range caps {
		if cap.Type != "metadata_provider.v1" {
			continue
		}
		if err := h.chainRepo.AppendProviderToAllChains(ctx, installation.ID, cap.ID, func(level string) metadata.SeedPlacement {
			return metadata.LookupSeedPlacement(ctx, h.chainRepo.Pool(), installation.ID, cap.ID, level)
		}); err != nil {
			slog.WarnContext(ctx, "failed to append provider to library chains", "component", "api",
				"installation_id", installation.ID,
				"capability_id", cap.ID,
				"error", err)
		}
	}
}

func isZipUpload(data []byte) bool {
	if len(data) < 4 {
		return false
	}

	return bytes.Equal(data[:4], []byte("PK\x03\x04")) ||
		bytes.Equal(data[:4], []byte("PK\x05\x06")) ||
		bytes.Equal(data[:4], []byte("PK\x07\x08"))
}

func isZipUploadFile(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open uploaded plugin file: %w", err)
	}
	defer file.Close()

	var header [4]byte
	n, err := io.ReadFull(file, header[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, fmt.Errorf("read uploaded plugin header: %w", err)
	}
	return isZipUpload(header[:n]), nil
}

func toPluginChunkedUploadSessionResponse(session uploads.SessionInfo) pluginChunkedUploadSessionResponse {
	return pluginChunkedUploadSessionResponse{
		UploadID:       session.ID,
		Filename:       session.Filename,
		SizeBytes:      session.SizeBytes,
		ChunkSize:      session.ChunkSize,
		TotalChunks:    session.TotalChunks,
		ReceivedChunks: session.ReceivedChunks,
		ReceivedBytes:  session.ReceivedBytes,
		Complete:       session.Complete,
		ExpiresAt:      session.ExpiresAt,
	}
}

func uploadErrorResponse(err error) (int, string) {
	var maxBytesErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxBytesErr), errors.Is(err, uploads.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "Upload exceeds the maximum allowed size"
	case errors.Is(err, uploads.ErrNotFound):
		return http.StatusNotFound, "Upload session not found"
	case errors.Is(err, uploads.ErrExpired):
		return http.StatusGone, "Upload session expired"
	case errors.Is(err, uploads.ErrIncomplete):
		return http.StatusConflict, "Upload session is incomplete"
	case errors.Is(err, uploads.ErrAlreadyCompleted):
		return http.StatusConflict, "Upload session is already complete"
	case errors.Is(err, uploads.ErrInvalidChunk), errors.Is(err, uploads.ErrInvalidRequest):
		return http.StatusBadRequest, err.Error()
	default:
		return http.StatusInternalServerError, "Failed to process upload"
	}
}

func (h *PluginHandler) HandleUpdateInstallation(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}

	var req pluginInstallationUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	currentInstallation, err := h.installations.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, plugins.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Plugin installation not found")
			return
		}
		slog.ErrorContext(r.Context(), "loading current plugin installation", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load plugin installation")
		return
	}
	if currentInstallation.IsBuiltin() {
		writeError(w, http.StatusConflict, "builtin_installation", "Built-in host providers cannot be modified")
		return
	}

	if req.Enabled != nil && !*req.Enabled && currentInstallation.Enabled && h.service != nil {
		if err := h.service.Stop(id); err != nil && !errors.Is(err, pluginhost.ErrClientNotFound) {
			slog.ErrorContext(r.Context(), "stopping plugin before disable", "component", "api", "installation_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to disable plugin installation")
			return
		}
	}

	if err := h.installations.Update(r.Context(), id, plugins.UpdateInstallationInput{
		Enabled:      req.Enabled,
		UpdatePolicy: req.UpdatePolicy,
	}); err != nil {
		if errors.Is(err, plugins.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Plugin installation not found")
			return
		}
		slog.ErrorContext(r.Context(), "updating plugin installation", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update plugin installation")
		return
	}

	// Rebuild the event dispatcher's capability-subscriber index whenever the
	// enabled state changes (enable or disable).
	if req.Enabled != nil && h.service != nil {
		h.service.OnLifecycleChange(r.Context())
	}

	installation, err := h.installations.GetByID(r.Context(), id)
	if err != nil {
		slog.ErrorContext(r.Context(), "loading updated plugin installation", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load plugin installation")
		return
	}

	response, err := h.buildInstallationResponse(r.Context(), installation, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "building updated plugin installation response", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build plugin installation response")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *PluginHandler) HandleApplyUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}
	if h.rejectBuiltinInstallation(w, r, id) {
		return
	}

	installation, err := h.service.UpdateToAvailableVersion(r.Context(), id)
	if err != nil {
		if errors.Is(err, plugins.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Plugin installation not found")
			return
		}
		slog.ErrorContext(r.Context(), "apply plugin update", "component", "api", "installation_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update plugin")
		return
	}

	h.syncMetadataProviders(r.Context(), installation)

	response, err := h.buildInstallationResponse(r.Context(), installation, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "building updated plugin installation response", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to build response")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *PluginHandler) HandlePutInstallationConfig(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}

	var req pluginConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if h.service == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Plugin service not configured")
		return
	}
	if h.rejectBuiltinInstallation(w, r, id) {
		return
	}

	if err := h.service.SetGlobalConfig(r.Context(), id, req.Key, req.Value); err != nil {
		var validationErr *plugins.ConfigValidationError
		switch {
		case errors.As(err, &validationErr):
			writeError(w, http.StatusBadRequest, "bad_request", validationErr.Error())
		case errors.Is(err, plugins.ErrInstallationNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Plugin installation not found")
		default:
			slog.ErrorContext(r.Context(), "setting plugin global config", "component", "api", "installation_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save plugin config")
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *PluginHandler) HandleTestInstallationConfig(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}
	if h.service == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Plugin service not configured")
		return
	}

	var req pluginConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "key is required")
		return
	}
	if h.rejectBuiltinInstallation(w, r, id) {
		return
	}

	if err := h.service.TestGlobalConfig(r.Context(), id, req.Key, req.Value); err != nil {
		if errors.Is(err, plugins.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Plugin installation not found")
			return
		}

		var connectionErr *plugins.ConnectionTestError
		if errors.As(err, &connectionErr) {
			writeJSON(w, http.StatusOK, connectionCheckResponse{
				Success: false,
				Message: connectionErr.Error(),
			})
			return
		}

		slog.ErrorContext(r.Context(), "testing plugin config", "component", "api", "installation_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to test plugin config")
		return
	}

	writeJSON(w, http.StatusOK, connectionCheckResponse{
		Success: true,
		Message: "Connection successful.",
	})
}

func (h *PluginHandler) HandlePutAuthBinding(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}

	if h.rejectBuiltinInstallation(w, r, id) {
		return
	}

	var req pluginAuthBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if strings.TrimSpace(req.CapabilityID) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "capability_id is required")
		return
	}

	if err := h.configs.UpsertAuthBinding(r.Context(), plugins.AuthBinding{
		InstallationID: id,
		CapabilityID:   req.CapabilityID,
		Enabled:        req.Enabled,
		DisplayOrder:   req.DisplayOrder,
		AutoProvision:  req.AutoProvision,
		DefaultLogin:   req.DefaultLogin,
	}); err != nil {
		slog.ErrorContext(r.Context(), "saving plugin auth binding", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save auth binding")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *PluginHandler) HandlePutTaskBinding(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}

	if h.rejectBuiltinInstallation(w, r, id) {
		return
	}

	capabilityID := chi.URLParam(r, "capability_id")
	if strings.TrimSpace(capabilityID) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "capability_id is required")
		return
	}

	var req pluginTaskBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if err := h.configs.UpsertTaskBinding(r.Context(), plugins.TaskBinding{
		InstallationID: id,
		CapabilityID:   capabilityID,
		Enabled:        req.Enabled,
		Trigger:        req.Trigger,
	}); err != nil {
		slog.ErrorContext(r.Context(), "saving plugin task binding", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save task binding")
		return
	}

	h.restartStatus.MarkRequired("plugin_task_binding")
	writeJSON(w, http.StatusOK, pluginTaskBindingUpdateResponse{RestartRequired: true})
}

// rejectBuiltinInstallation writes a 409 and returns true when the target
// installation is the reserved builtin row, which no plugin-management
// endpoint may mutate. Lookup errors are left to the caller's own handling.
func (h *PluginHandler) rejectBuiltinInstallation(w http.ResponseWriter, r *http.Request, id int) bool {
	installation, err := h.installations.GetByID(r.Context(), id)
	if err != nil {
		return false
	}
	if !installation.IsBuiltin() {
		return false
	}
	writeError(w, http.StatusConflict, "builtin_installation", "Built-in host providers cannot be modified")
	return true
}

func (h *PluginHandler) HandleDeleteInstallation(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}

	if err := h.installations.Delete(r.Context(), id); err != nil {
		if errors.Is(err, plugins.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Plugin installation not found")
			return
		}
		if errors.Is(err, plugins.ErrBuiltinInstallationImmutable) {
			writeError(w, http.StatusConflict, "builtin_installation", "Built-in host providers cannot be uninstalled")
			return
		}
		slog.ErrorContext(r.Context(), "deleting plugin installation", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete plugin installation")
		return
	}

	// Rebuild the event dispatcher's capability-subscriber index after removal.
	if h.service != nil {
		h.service.OnLifecycleChange(r.Context())
	}

	w.WriteHeader(http.StatusNoContent)
}

// manifestHasUserNavigableRoute returns true if the manifest declares any
// navigable HTTP route with navigation_kind="user". Such plugins should
// appear in the user-facing plugin list (and therefore in the user sidebar)
// even if they expose no user_config_schema.
func manifestHasUserNavigableRoute(manifest *pluginv1.PluginManifest) bool {
	for _, r := range manifest.GetHttpRoutes() {
		if r.GetNavigable() && r.GetNavigationKind() == "user" {
			return true
		}
	}
	return false
}

func (h *PluginHandler) HandleListUserPluginSettings(w http.ResponseWriter, r *http.Request) {
	installations, err := h.installations.ListEnabled(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "listing enabled plugin installations", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list plugin settings")
		return
	}

	response := pluginUserSettingsListResponse{
		Installations: make([]pluginUserSettingsSummary, 0, len(installations)),
	}
	for _, installation := range installations {
		// The reserved builtin row has no manifest on disk; without this skip
		// the whole user-scoped settings list would 500.
		if installation.IsBuiltin() {
			continue
		}
		manifest, err := plugins.LoadManifestFile(plugins.InstalledManifestPath(installation.InstallPath))
		if err != nil {
			slog.ErrorContext(r.Context(), "loading plugin manifest", "component", "api", "installation_id", installation.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load plugin settings")
			return
		}
		if len(manifest.GetUserConfigSchema()) == 0 && !manifestHasUserNavigableRoute(manifest) {
			continue
		}
		response.Installations = append(response.Installations, toUserPluginSettingsSummary(installation, manifest))
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *PluginHandler) HandleGetUserPluginSettings(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "installation_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}

	installation, manifest, err := h.loadUserConfigInstallation(w, r, id)
	if err != nil {
		return
	}

	userID := apimw.GetUserID(r.Context())
	values, err := h.userConfig.Get(r.Context(), userID, id)
	if err != nil {
		slog.ErrorContext(r.Context(), "loading plugin user config", "component", "api", "installation_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load plugin settings")
		return
	}

	writeJSON(w, http.StatusOK, pluginUserSettingsDetailResponse{
		Installation: toUserPluginSettingsSummary(installation, manifest),
		Values:       values,
	})
}

func (h *PluginHandler) HandlePutUserPluginSettings(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "installation_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}

	if _, _, err := h.loadUserConfigInstallation(w, r, id); err != nil {
		return
	}

	var req userPluginSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	userID := apimw.GetUserID(r.Context())
	if err := h.userConfig.Set(r.Context(), userID, id, req.Values); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *PluginHandler) loadUserConfigInstallation(
	w http.ResponseWriter,
	r *http.Request,
	installationID int,
) (*plugins.Installation, *pluginv1.PluginManifest, error) {
	installation, err := h.installations.GetByID(r.Context(), installationID)
	if err != nil {
		if errors.Is(err, plugins.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Plugin installation not found")
			return nil, nil, err
		}
		slog.ErrorContext(r.Context(), "loading plugin installation", "component", "api", "installation_id", installationID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load plugin installation")
		return nil, nil, err
	}
	if !installation.Enabled || installation.IsBuiltin() {
		writeError(w, http.StatusNotFound, "not_found", "Plugin installation not found")
		return nil, nil, plugins.ErrInstallationNotFound
	}

	manifest, err := h.loadInstallationManifest(r.Context(), installation)
	if err != nil {
		slog.ErrorContext(r.Context(), "loading plugin manifest", "component", "api", "installation_id", installationID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load plugin settings")
		return nil, nil, err
	}
	if len(manifest.GetUserConfigSchema()) == 0 && !manifestHasUserNavigableRoute(manifest) {
		writeError(w, http.StatusNotFound, "not_found", "Plugin installation does not expose user settings")
		return nil, nil, plugins.ErrInstallationNotFound
	}

	return installation, manifest, nil
}

func (h *PluginHandler) buildInstallationResponses(
	ctx context.Context,
	installations []*plugins.Installation,
) ([]pluginInstallationResponse, error) {
	repositories, err := h.repositories.List(ctx)
	if err != nil {
		return nil, err
	}
	repositoriesByID := make(map[int]*plugins.Repository, len(repositories))
	for _, repository := range repositories {
		if repository != nil {
			repositoriesByID[repository.ID] = repository
		}
	}

	authBindings, err := h.configs.ListAuthBindings(ctx)
	if err != nil {
		return nil, err
	}
	taskBindings, err := h.configs.ListTaskBindings(ctx)
	if err != nil {
		return nil, err
	}
	response := make([]pluginInstallationResponse, 0, len(installations))
	for _, installation := range installations {
		// The reserved builtin row is not a manageable plugin: old web builds
		// would render a phantom entry with uninstall/upgrade buttons that
		// error, and the chain editor does not need it in this list.
		if installation.IsBuiltin() {
			continue
		}
		item, err := h.buildInstallationResponseWithBindings(
			ctx,
			installation,
			nil,
			authBindings,
			taskBindings,
			repositoriesByID,
		)
		if err != nil {
			return nil, err
		}
		response = append(response, item)
	}
	return response, nil
}

func (h *PluginHandler) buildInstallationResponse(
	ctx context.Context,
	installation *plugins.Installation,
	manifest *pluginv1.PluginManifest,
) (pluginInstallationResponse, error) {
	repositoriesByID := make(map[int]*plugins.Repository, 1)
	if installation.RepositoryID != nil {
		repository, err := h.repositories.GetByID(ctx, *installation.RepositoryID)
		if err != nil && !errors.Is(err, plugins.ErrRepositoryNotFound) {
			return pluginInstallationResponse{}, err
		}
		if repository != nil {
			repositoriesByID[repository.ID] = repository
		}
	}

	authBindings, err := h.configs.ListAuthBindings(ctx)
	if err != nil {
		return pluginInstallationResponse{}, err
	}
	taskBindings, err := h.configs.ListTaskBindings(ctx)
	if err != nil {
		return pluginInstallationResponse{}, err
	}
	return h.buildInstallationResponseWithBindings(
		ctx,
		installation,
		manifest,
		authBindings,
		taskBindings,
		repositoriesByID,
	)
}

func (h *PluginHandler) buildInstallationResponseWithBindings(
	ctx context.Context,
	installation *plugins.Installation,
	manifest *pluginv1.PluginManifest,
	authBindings []*plugins.AuthBinding,
	taskBindings []*plugins.TaskBinding,
	repositoriesByID map[int]*plugins.Repository,
) (pluginInstallationResponse, error) {
	if manifest == nil {
		var err error
		manifest, err = h.loadInstallationManifest(ctx, installation)
		if err != nil && !errors.Is(err, plugins.ErrArchiveNotFound) {
			return pluginInstallationResponse{}, err
		}
	}

	capabilities, err := h.loadInstallationCapabilities(ctx, installation, manifest)
	if err != nil {
		return pluginInstallationResponse{}, err
	}
	configs, err := h.configs.ListGlobalConfigs(ctx, installation.ID)
	if err != nil {
		return pluginInstallationResponse{}, err
	}

	var (
		globalConfigSchema []pluginConfigSchemaJSON
		userConfigSchema   []pluginConfigSchemaJSON
		routes             []pluginRouteJSON
		assets             []pluginAssetJSON
		metadata           map[string]any
		sourceKind         = plugins.RepositorySourceExternal
		repositoryName     string
		updatesPaused      bool
	)
	if installation.RepositoryID != nil {
		repository := repositoriesByID[*installation.RepositoryID]
		if repository != nil {
			sourceKind = repository.SourceKind
			repositoryName = repository.DisplayName
			updatesPaused = repository.SourceKind == plugins.RepositorySourceApprovedCommunity && !repository.Enabled
		}
	}
	if manifest != nil {
		globalConfigSchema = configSchemasToJSON(manifest.GetGlobalConfigSchema())
		userConfigSchema = configSchemasToJSON(manifest.GetUserConfigSchema())
		routes = routesToJSON(manifest.GetHttpRoutes())
		assets = assetsToJSON(manifest.GetAssets())
		metadata = structToMap(manifest.GetMetadata())
	}
	presentation := toPluginPresentationJSON(manifest.GetPresentation())
	repoURL := ""
	if presentation != nil {
		repoURL = presentation.SourceURL
	}

	return pluginInstallationResponse{
		ID:                 installation.ID,
		RepositoryID:       installation.RepositoryID,
		PluginID:           installation.PluginID,
		Version:            installation.Version,
		InstallPath:        installation.InstallPath,
		Enabled:            installation.Enabled,
		Kind:               installation.Kind,
		UpdatePolicy:       installation.UpdatePolicy,
		AvailableVersion:   installation.AvailableVersion,
		SourceKind:         sourceKind,
		RepositoryName:     repositoryName,
		RepoURL:            repoURL,
		Presentation:       presentation,
		UpdatesPaused:      updatesPaused,
		Capabilities:       capabilities,
		GlobalConfigSchema: globalConfigSchema,
		UserConfigSchema:   userConfigSchema,
		Routes:             routes,
		Assets:             assets,
		Metadata:           metadata,
		GlobalConfigs:      configValuesToJSON(configs),
		AuthBindings:       authBindingsForInstallation(installation.ID, authBindings),
		TaskBindings:       taskBindingsForInstallation(installation.ID, taskBindings),
		CreatedAt:          installation.CreatedAt,
		UpdatedAt:          installation.UpdatedAt,
	}, nil
}

func toPluginPresentationJSON(presentation *pluginv1.PluginPresentation) *pluginPresentationJSON {
	if presentation == nil {
		return nil
	}
	return &pluginPresentationJSON{
		DisplayName:         presentation.GetDisplayName(),
		Summary:             presentation.GetSummary(),
		DescriptionMarkdown: presentation.GetDescriptionMarkdown(),
		SetupMarkdown:       presentation.GetSetupMarkdown(),
		HomepageURL:         presentation.GetHomepageUrl(),
		SourceURL:           presentation.GetSourceUrl(),
		SupportURL:          presentation.GetSupportUrl(),
		ChangelogURL:        presentation.GetChangelogUrl(),
		PublisherName:       presentation.GetPublisherName(),
		PublisherURL:        presentation.GetPublisherUrl(),
		LicenseSPDX:         presentation.GetLicenseSpdx(),
	}
}

func toPluginRepositoryResponse(repository *plugins.Repository) pluginRepositoryResponse {
	return pluginRepositoryResponse{
		ID:            repository.ID,
		URL:           repository.URL,
		DisplayName:   repository.DisplayName,
		Enabled:       repository.Enabled,
		SourceKind:    repository.SourceKind,
		Managed:       repository.ManagedKey != nil,
		LastFetchedAt: repository.LastFetchedAt,
		CreatedAt:     repository.CreatedAt,
		UpdatedAt:     repository.UpdatedAt,
	}
}

func toPluginCatalogSettingsResponse(settings plugins.CatalogSettings) pluginCatalogSettingsResponse {
	return pluginCatalogSettingsResponse{
		IncludeApprovedCommunityPlugins: settings.IncludeApprovedCommunityPlugins,
		ApprovedCommunityPluginCount:    settings.ApprovedCommunityPluginCount,
		InstalledCommunityPluginCount:   settings.InstalledCommunityPluginCount,
		MigratedPluginCount:             settings.MigratedPluginCount,
		CommunityUpdatesPaused:          settings.CommunityUpdatesPaused,
	}
}

func toUserPluginSettingsSummary(
	installation *plugins.Installation,
	manifest *pluginv1.PluginManifest,
) pluginUserSettingsSummary {
	return pluginUserSettingsSummary{
		ID:               installation.ID,
		PluginID:         installation.PluginID,
		Version:          installation.Version,
		UserConfigSchema: configSchemasToJSON(manifest.GetUserConfigSchema()),
		Routes:           routesToJSON(manifest.GetHttpRoutes()),
		Assets:           assetsToJSON(manifest.GetAssets()),
		Category:         manifest.GetCategory(),
	}
}

func configSchemasToJSON(schemas []*pluginv1.ConfigSchema) []pluginConfigSchemaJSON {
	response := make([]pluginConfigSchemaJSON, 0, len(schemas))
	for _, schema := range schemas {
		if schema == nil {
			continue
		}
		response = append(response, pluginConfigSchemaJSON{
			Key:         schema.GetKey(),
			Title:       schema.GetTitle(),
			Description: schema.GetDescription(),
			JSONSchema:  schema.GetJsonSchema(),
			Required:    schema.GetRequired(),
			AdminForm:   adminFormToJSON(schema.GetAdminForm()),
		})
	}
	return response
}

func adminFormToJSON(form *pluginv1.AdminFormDescriptor) *pluginAdminFormJSON {
	if form == nil {
		return nil
	}
	fields := make([]pluginAdminFormFieldJSON, 0, len(form.GetFields()))
	for _, field := range form.GetFields() {
		if field == nil {
			continue
		}
		options := make([]pluginAdminFormOptionJSON, 0, len(field.GetOptions()))
		for _, option := range field.GetOptions() {
			if option == nil {
				continue
			}
			options = append(options, pluginAdminFormOptionJSON{
				Value:       option.GetValue(),
				Label:       option.GetLabel(),
				Description: option.GetDescription(),
			})
		}
		var defaultValue any
		if field.GetDefaultValue() != nil {
			defaultValue = field.GetDefaultValue().AsInterface()
		}
		var validation *pluginAdminFormValidationJSON
		if v := field.GetValidation(); v != nil {
			validation = &pluginAdminFormValidationJSON{
				HasMin:    v.GetHasMin(),
				Min:       v.GetMin(),
				HasMax:    v.GetHasMax(),
				Max:       v.GetMax(),
				Pattern:   v.GetPattern(),
				MinLength: v.GetMinLength(),
				MaxLength: v.GetMaxLength(),
			}
		}
		fields = append(fields, pluginAdminFormFieldJSON{
			Key:                 field.GetKey(),
			Label:               field.GetLabel(),
			Description:         field.GetDescription(),
			Control:             strings.TrimPrefix(field.GetControl().String(), "ADMIN_FORM_CONTROL_"),
			Placeholder:         field.GetPlaceholder(),
			Required:            field.GetRequired(),
			Secret:              field.GetSecret(),
			Multiline:           field.GetMultiline(),
			DefaultValue:        defaultValue,
			Options:             options,
			Rows:                field.GetRows(),
			DynamicOptions:      field.GetDynamicOptions(),
			ShowWhen:            adminFormConditionsToJSON(field.GetShowWhen()),
			Validation:          validation,
			ExclusiveGroupField: field.GetExclusiveGroupField(),
		})
	}
	sections := make([]pluginAdminFormSectionJSON, 0, len(form.GetSections()))
	for _, section := range form.GetSections() {
		if section == nil {
			continue
		}
		sections = append(sections, pluginAdminFormSectionJSON{
			Key:              section.GetKey(),
			Title:            section.GetTitle(),
			Description:      section.GetDescription(),
			Collapsible:      section.GetCollapsible(),
			CollapsedDefault: section.GetCollapsedDefault(),
			FieldKeys:        append([]string(nil), section.GetFieldKeys()...),
			ShowWhen:         adminFormConditionsToJSON(section.GetShowWhen()),
		})
	}
	return &pluginAdminFormJSON{
		Fields:      fields,
		SubmitLabel: form.GetSubmitLabel(),
		Sections:    sections,
	}
}

func adminFormConditionsToJSON(conditions []*pluginv1.AdminFormCondition) []pluginAdminFormConditionJSON {
	if len(conditions) == 0 {
		return nil
	}
	out := make([]pluginAdminFormConditionJSON, 0, len(conditions))
	for _, condition := range conditions {
		if condition == nil {
			continue
		}
		out = append(out, pluginAdminFormConditionJSON{
			Field:  condition.GetField(),
			Equals: append([]string(nil), condition.GetEquals()...),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func capabilitiesToJSON(descriptors []*pluginv1.CapabilityDescriptor) []pluginCapabilityJSON {
	response := make([]pluginCapabilityJSON, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor == nil {
			continue
		}
		response = append(response, pluginCapabilityJSON{
			Type:          descriptor.GetType(),
			ID:            descriptor.GetId(),
			DisplayName:   descriptor.GetDisplayName(),
			Description:   descriptor.GetDescription(),
			Subscriptions: append([]string(nil), descriptor.GetSubscriptions()...),
			ConfigSchema:  configSchemasToJSON(descriptor.GetConfigSchema()),
			Metadata:      structToMap(descriptor.GetMetadata()),
		})
	}
	return response
}

func routesToJSON(routes []*pluginv1.HttpRouteDescriptor) []pluginRouteJSON {
	response := make([]pluginRouteJSON, 0, len(routes))
	for _, route := range routes {
		if route == nil {
			continue
		}
		response = append(response, pluginRouteJSON{
			ID:              route.GetId(),
			Method:          route.GetMethod(),
			Path:            route.GetPath(),
			Access:          route.GetAccess(),
			Navigable:       route.GetNavigable(),
			NavigationLabel: route.GetNavigationLabel(),
			NavigationKind:  route.GetNavigationKind(),
			StaticAsset:     route.GetStaticAsset(),
		})
	}
	return response
}

func assetsToJSON(assets []*pluginv1.PackagedAsset) []pluginAssetJSON {
	response := make([]pluginAssetJSON, 0, len(assets))
	for _, asset := range assets {
		if asset == nil {
			continue
		}
		response = append(response, pluginAssetJSON{
			Path:        asset.GetPath(),
			ContentType: asset.GetContentType(),
			Integrity:   asset.GetIntegrity(),
		})
	}
	return response
}

func configValuesToJSON(configs []*plugins.RuntimeConfig) []pluginConfigValueJSON {
	response := make([]pluginConfigValueJSON, 0, len(configs))
	for _, config := range configs {
		if config == nil {
			continue
		}
		response = append(response, pluginConfigValueJSON{
			Key:   config.Key,
			Value: config.Value,
		})
	}
	return response
}

func authBindingsForInstallation(installationID int, bindings []*plugins.AuthBinding) []pluginAuthBindingJSON {
	response := make([]pluginAuthBindingJSON, 0)
	for _, binding := range bindings {
		if binding == nil || binding.InstallationID != installationID {
			continue
		}
		response = append(response, pluginAuthBindingJSON{
			CapabilityID:  binding.CapabilityID,
			Enabled:       binding.Enabled,
			DisplayOrder:  binding.DisplayOrder,
			AutoProvision: binding.AutoProvision,
			DefaultLogin:  binding.DefaultLogin,
			CreatedAt:     binding.CreatedAt,
			UpdatedAt:     binding.UpdatedAt,
		})
	}
	return response
}

func taskBindingsForInstallation(installationID int, bindings []*plugins.TaskBinding) []pluginTaskBindingJSON {
	response := make([]pluginTaskBindingJSON, 0)
	for _, binding := range bindings {
		if binding == nil || binding.InstallationID != installationID {
			continue
		}
		response = append(response, pluginTaskBindingJSON{
			CapabilityID: binding.CapabilityID,
			Enabled:      binding.Enabled,
			Trigger:      binding.Trigger,
			CreatedAt:    binding.CreatedAt,
			UpdatedAt:    binding.UpdatedAt,
		})
	}
	return response
}

func (h *PluginHandler) loadInstallationManifest(
	ctx context.Context,
	installation *plugins.Installation,
) (*pluginv1.PluginManifest, error) {
	if installation == nil {
		return nil, errors.New("plugin installation is required")
	}
	if h.service != nil {
		manifest, err := h.service.ManifestForInstallation(ctx, installation.ID)
		if err == nil {
			return manifest, nil
		}
		if errors.Is(err, plugins.ErrArchiveNotFound) {
			return nil, plugins.ErrArchiveNotFound
		}
		if !errors.Is(err, plugins.ErrArchiveNotFound) {
			return nil, err
		}
	}
	manifest, err := loadPluginManifest(installation)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return nil, plugins.ErrArchiveNotFound
	}
	return manifest, err
}

func (h *PluginHandler) loadInstallationCapabilities(
	ctx context.Context,
	installation *plugins.Installation,
	manifest *pluginv1.PluginManifest,
) ([]pluginCapabilityJSON, error) {
	if manifest != nil {
		return capabilitiesToJSON(manifest.GetCapabilities()), nil
	}

	records, err := h.installations.ListCapabilities(ctx, installation.ID)
	if err != nil {
		return nil, err
	}

	response := make([]pluginCapabilityJSON, 0, len(records))
	for _, record := range records {
		descriptor, err := plugins.DecodeCapability(record)
		if err != nil {
			return nil, err
		}
		response = append(response, capabilitiesToJSON([]*pluginv1.CapabilityDescriptor{descriptor})...)
	}
	return response, nil
}

func loadPluginManifest(installation *plugins.Installation) (*pluginv1.PluginManifest, error) {
	return plugins.LoadManifestFile(plugins.InstalledManifestPath(installation.InstallPath))
}

func structToMap(value interface{ AsMap() map[string]any }) map[string]any {
	if value == nil {
		return nil
	}
	return value.AsMap()
}

func parseNamedIDParam(r *http.Request, name string) (int, error) {
	return strconv.Atoi(chi.URLParam(r, name))
}
