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
	Key          string         `json:"key"`
	Value        map[string]any `json:"value"`
	ClearSecrets []string       `json:"clear_secrets,omitempty"`
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

type PluginCatalogEntryView struct {
	RepositoryID       int                      `json:"repository_id"`
	PluginID           string                   `json:"plugin_id"`
	Version            string                   `json:"version"`
	ArchiveURL         string                   `json:"archive_url"`
	SourceKind         string                   `json:"source_kind"`
	RepositoryName     string                   `json:"repository_name"`
	RepoURL            string                   `json:"repo_url,omitempty"`
	Presentation       *PluginPresentationView  `json:"presentation,omitempty"`
	Capabilities       []PluginCapabilityView   `json:"capabilities"`
	GlobalConfigSchema []PluginConfigSchemaView `json:"global_config_schema"`
	UserConfigSchema   []PluginConfigSchemaView `json:"user_config_schema"`
	Routes             []PluginRouteView        `json:"routes"`
	Assets             []PluginAssetView        `json:"assets"`
	Metadata           map[string]any           `json:"metadata,omitempty"`
}

type PluginInstallationView struct {
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
	Presentation       *PluginPresentationView  `json:"presentation,omitempty"`
	UpdatesPaused      bool                     `json:"updates_paused"`
	Capabilities       []PluginCapabilityView   `json:"capabilities"`
	GlobalConfigSchema []PluginConfigSchemaView `json:"global_config_schema"`
	UserConfigSchema   []PluginConfigSchemaView `json:"user_config_schema"`
	Routes             []PluginRouteView        `json:"routes"`
	Assets             []PluginAssetView        `json:"assets"`
	Metadata           map[string]any           `json:"metadata,omitempty"`
	GlobalConfigs      []PluginConfigValueView  `json:"global_configs"`
	AuthBindings       []PluginAuthBindingView  `json:"auth_bindings"`
	TaskBindings       []PluginTaskBindingView  `json:"task_bindings"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	// Runtime is the process state the v2 contract exposes as `runtime`. The
	// frozen v1 bridge does not carry it.
	Runtime *PluginRuntimeView `json:"-"`
}

// PluginRuntimeView is one installation's process state. Resident marks a
// plugin the supervisor keeps running (network access providers); State is
// the supervisor's machine for those and running/stopped for lazily started
// plugins.
type PluginRuntimeView struct {
	Resident      bool
	State         string
	RestartCount  int
	LastError     string
	LastStartedAt *time.Time
	NextRestartAt *time.Time
}

type pluginCatalogSettingsResponse struct {
	IncludeApprovedCommunityPlugins bool `json:"include_approved_community_plugins"`
	ApprovedCommunityPluginCount    int  `json:"approved_community_plugin_count"`
	InstalledCommunityPluginCount   int  `json:"installed_community_plugin_count"`
	MigratedPluginCount             int  `json:"migrated_plugin_count"`
	CommunityUpdatesPaused          bool `json:"community_updates_paused"`
}

type PluginPresentationView struct {
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

type PluginConfigSchemaView = plugins.ConfigSchemaView
type pluginAdminFormJSON = plugins.AdminFormView
type pluginAdminFormFieldJSON = plugins.AdminFormFieldView
type pluginAdminFormSectionJSON = plugins.AdminFormSectionView

type PluginCapabilityView struct {
	Type          string                   `json:"type"`
	ID            string                   `json:"id"`
	DisplayName   string                   `json:"display_name"`
	Description   string                   `json:"description"`
	Subscriptions []string                 `json:"subscriptions,omitempty"`
	ConfigSchema  []PluginConfigSchemaView `json:"config_schema,omitempty"`
	Metadata      map[string]any           `json:"metadata,omitempty"`
}

type PluginRouteView struct {
	ID              string `json:"id"`
	Method          string `json:"method"`
	Path            string `json:"path"`
	Access          string `json:"access"`
	Navigable       bool   `json:"navigable"`
	NavigationLabel string `json:"navigation_label"`
	NavigationKind  string `json:"navigation_kind"`
	StaticAsset     bool   `json:"static_asset"`
}

type PluginAssetView struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Integrity   string `json:"integrity"`
}

type PluginConfigValueView struct {
	Key               string         `json:"key"`
	Value             map[string]any `json:"value"`
	ConfiguredSecrets []string       `json:"configured_secrets,omitempty"`
}

type PluginAuthBindingView struct {
	CapabilityID  string    `json:"capability_id"`
	Enabled       bool      `json:"enabled"`
	DisplayOrder  int       `json:"display_order"`
	AutoProvision bool      `json:"auto_provision"`
	DefaultLogin  bool      `json:"default_login"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type PluginTaskBindingView struct {
	CapabilityID string         `json:"capability_id"`
	Enabled      bool           `json:"enabled"`
	Trigger      map[string]any `json:"trigger"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type PluginUserSettingsView struct {
	ID               int                      `json:"id"`
	PluginID         string                   `json:"plugin_id"`
	Version          string                   `json:"version"`
	UserConfigSchema []PluginConfigSchemaView `json:"user_config_schema"`
	Routes           []PluginRouteView        `json:"routes"`
	Assets           []PluginAssetView        `json:"assets"`
	// Category is the manifest's optional slash-delimited grouping path
	// (e.g. "Tools/Utilities") used to group plugin entries in the
	// user-facing Apps navigation. Empty (omitted) when the manifest
	// declares no category. Additive-only per v1 API rules.
	Category string `json:"category,omitempty"`
}

type pluginUserSettingsListResponse struct {
	Installations []PluginUserSettingsView `json:"installations"`
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
	response, err := h.ListAdminPluginCatalog(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "fetching plugin catalog", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch plugin catalog")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// ListAdminPluginCatalog fetches every enabled repository index and returns
// the discoverable catalog, one entry per plugin ID. Both listeners read this
// one projection. The fetch is a live remote read and records last_fetched_at
// on each repository it reaches.
func (h *PluginHandler) ListAdminPluginCatalog(ctx context.Context) ([]PluginCatalogEntryView, error) {
	if h == nil || h.service == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Plugin service not configured")
	}
	entries, err := h.service.FetchCatalog(ctx)
	if err != nil {
		return nil, err
	}
	response := make([]PluginCatalogEntryView, 0, len(entries))
	for _, entry := range entries {
		response = append(response, pluginCatalogEntryView(entry))
	}
	return response, nil
}

func pluginCatalogEntryView(entry plugins.CatalogEntry) PluginCatalogEntryView {
	presentation := toPluginPresentationJSON(entry.Manifest.GetPresentation())
	repoURL := entry.RepoURL
	if repoURL == "" && presentation != nil {
		repoURL = presentation.SourceURL
	}
	return PluginCatalogEntryView{
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
	}
}

func (h *PluginHandler) HandleListInstallations(w http.ResponseWriter, r *http.Request) {
	response, err := h.ListAdminPluginInstallations(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "listing plugin installations", "component", "api", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list plugin installations")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// ListAdminPluginInstallations returns every manageable installation with its
// manifest surface, redacted global configuration and bindings. The reserved
// builtin row is excluded. Both listeners read this one projection, so the
// secret redaction in configValuesToJSON applies to each.
func (h *PluginHandler) ListAdminPluginInstallations(ctx context.Context) ([]PluginInstallationView, error) {
	if h == nil || h.installations == nil || h.repositories == nil || h.configs == nil {
		return nil, apiError(http.StatusServiceUnavailable, "unavailable", "Plugin stores not configured")
	}
	installations, err := h.installations.List(ctx)
	if err != nil {
		return nil, err
	}
	return h.buildInstallationResponses(ctx, installations)
}

func (h *PluginHandler) HandleCreateInstallation(w http.ResponseWriter, r *http.Request) {
	var req pluginInstallationCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	response, err := h.CreateAdminPluginInstallation(r.Context(), PluginInstallationCreateInput(req))
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest {
			writeError(w, http.StatusBadRequest, "bad_request", apiErr.Message)
			return
		}
		writePluginLifecycleError(w, r, err, "install")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *PluginHandler) HandleUploadInstallation(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPluginUploadSize)
	response, err := h.InstallAdminPluginUpload(r.Context(), r)
	if err != nil {
		// Frozen bridge: every multipart parse failure, including an oversize
		// body, is the v1 400 bad_request. Only v2 distinguishes 413.
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusRequestEntityTooLarge) {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid plugin upload")
			return
		}
		writePluginUploadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *PluginHandler) HandleCreateChunkedUpload(w http.ResponseWriter, r *http.Request) {
	var req pluginChunkedUploadCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	session, err := h.CreateAdminPluginUpload(r.Context(), PluginChunkedUploadCreateInput(req))
	if err != nil {
		writePluginUploadError(w, r, err)
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
	session, err := h.PutAdminPluginUploadChunk(r.Context(), uploadID, chunkIndex, r.Body, r.ContentLength)
	if err != nil {
		writePluginUploadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPluginChunkedUploadSessionResponse(session))
}

func (h *PluginHandler) HandleCompleteChunkedUpload(w http.ResponseWriter, r *http.Request) {
	response, err := h.CompleteAdminPluginUpload(r.Context(), chi.URLParam(r, "upload_id"))
	if err != nil {
		writePluginUploadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *PluginHandler) HandleCancelChunkedUpload(w http.ResponseWriter, r *http.Request) {
	if err := h.CancelAdminPluginUpload(r.Context(), chi.URLParam(r, "upload_id")); err != nil {
		writePluginUploadError(w, r, err)
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
	case errors.Is(err, uploads.ErrChunkBusy):
		return http.StatusConflict, "This chunk is already being uploaded"
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
	response, err := h.UpdateAdminPluginInstallation(r.Context(), id, PluginInstallationUpdateInput(req))
	if err != nil {
		writePluginLifecycleError(w, r, err, "update")
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
	response, err := h.ApplyAdminPluginUpdate(r.Context(), id)
	if err != nil {
		writePluginLifecycleError(w, r, err, "update")
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

	if err := h.service.SetGlobalConfigWithClears(
		r.Context(), id, req.Key, req.Value, req.ClearSecrets,
	); err != nil {
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

	if err := h.service.TestGlobalConfigWithClears(
		r.Context(), id, req.Key, req.Value, req.ClearSecrets,
	); err != nil {
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

	h.restartStatus.MarkRequired("plugin_auth_binding")
	w.Header().Set("X-Silo-Restart-Required", "true")
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

// ErrPluginBuiltinInstallation reports a mutation aimed at the reserved
// builtin-host row, which no plugin-management route may modify (409).
var ErrPluginBuiltinInstallation = errors.New("builtin installation cannot be modified")

// PluginConfigInput is one global configuration entry write or probe.
type PluginConfigInput struct {
	Key          string
	Value        map[string]any
	ClearSecrets []string
}

// PluginConnectionCheckResult is the outcome of a configuration probe. A
// false Success carries the plugin's or validator's message; it is a
// completed check, not a transport failure.
type PluginConnectionCheckResult struct {
	Success bool
	Message string
}

// PluginAuthBindingInput is the whole-row auth binding assignment.
type PluginAuthBindingInput struct {
	CapabilityID  string
	Enabled       bool
	DisplayOrder  int
	AutoProvision bool
	DefaultLogin  bool
}

// PluginTaskBindingInput is the whole-row task binding assignment.
type PluginTaskBindingInput struct {
	Enabled bool
	Trigger map[string]any
}

// pluginMutationTarget resolves an installation for a mutation: unknown
// rows are ErrInstallationNotFound and the reserved builtin row is refused.
func (h *PluginHandler) pluginMutationTarget(ctx context.Context, id int) error {
	if h == nil || h.installations == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Plugin stores not configured")
	}
	installation, err := h.installations.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if installation.IsBuiltin() {
		return ErrPluginBuiltinInstallation
	}
	return nil
}

// SetAdminPluginConfig validates and persists one global configuration
// entry, then stops the running plugin so it rebinds on next use. Both
// listeners call this seam. Errors: *plugins.ConfigValidationError,
// plugins.ErrInstallationNotFound, ErrPluginBuiltinInstallation.
func (h *PluginHandler) SetAdminPluginConfig(ctx context.Context, id int, in PluginConfigInput) error {
	if h == nil || h.service == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Plugin service not configured")
	}
	if strings.TrimSpace(in.Key) == "" {
		return &plugins.ConfigValidationError{Message: "config key is required"}
	}
	if err := h.pluginMutationTarget(ctx, id); err != nil {
		return err
	}
	return h.service.SetGlobalConfigWithClears(ctx, id, in.Key, in.Value, in.ClearSecrets)
}

// TestAdminPluginConfig probes one prospective configuration by starting a
// temporary plugin instance and running its connection check. Errors:
// plugins.ErrInstallationNotFound, ErrPluginBuiltinInstallation; a failed
// probe is a Success=false result, not an error.
func (h *PluginHandler) TestAdminPluginConfig(ctx context.Context, id int, in PluginConfigInput) (PluginConnectionCheckResult, error) {
	if h == nil || h.service == nil {
		return PluginConnectionCheckResult{}, apiError(http.StatusServiceUnavailable, "unavailable", "Plugin service not configured")
	}
	if strings.TrimSpace(in.Key) == "" {
		return PluginConnectionCheckResult{}, &plugins.ConfigValidationError{Message: "config key is required"}
	}
	if err := h.pluginMutationTarget(ctx, id); err != nil {
		return PluginConnectionCheckResult{}, err
	}
	err := h.service.TestGlobalConfigWithClears(ctx, id, in.Key, in.Value, in.ClearSecrets)
	var connectionErr *plugins.ConnectionTestError
	switch {
	case err == nil:
		return PluginConnectionCheckResult{Success: true, Message: "Connection successful."}, nil
	case errors.As(err, &connectionErr):
		return PluginConnectionCheckResult{Success: false, Message: connectionErr.Error()}, nil
	default:
		return PluginConnectionCheckResult{}, err
	}
}

// SetAdminPluginAuthBinding replaces one auth binding row and marks a
// server restart required. Errors: plugins.ErrInstallationNotFound,
// ErrPluginBuiltinInstallation.
func (h *PluginHandler) SetAdminPluginAuthBinding(ctx context.Context, id int, in PluginAuthBindingInput) error {
	if h == nil || h.configs == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Plugin stores not configured")
	}
	if err := h.pluginMutationTarget(ctx, id); err != nil {
		return err
	}
	if err := h.configs.UpsertAuthBinding(ctx, plugins.AuthBinding{InstallationID: id, CapabilityID: in.CapabilityID, Enabled: in.Enabled, DisplayOrder: in.DisplayOrder, AutoProvision: in.AutoProvision, DefaultLogin: in.DefaultLogin}); err != nil {
		return err
	}
	h.restartStatus.MarkRequired("plugin_auth_binding")
	return nil
}

// SetAdminPluginTaskBinding replaces one task binding row and marks a
// server restart required. Errors as SetAdminPluginAuthBinding.
func (h *PluginHandler) SetAdminPluginTaskBinding(ctx context.Context, id int, capabilityID string, in PluginTaskBindingInput) error {
	if h == nil || h.configs == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Plugin stores not configured")
	}
	if err := h.pluginMutationTarget(ctx, id); err != nil {
		return err
	}
	if err := h.configs.UpsertTaskBinding(ctx, plugins.TaskBinding{InstallationID: id, CapabilityID: capabilityID, Enabled: in.Enabled, Trigger: in.Trigger}); err != nil {
		return err
	}
	h.restartStatus.MarkRequired("plugin_task_binding")
	return nil
}

func (h *PluginHandler) HandleDeleteInstallation(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}
	if err := h.DeleteAdminPluginInstallation(r.Context(), id); err != nil {
		writePluginLifecycleError(w, r, err, "uninstall")
		return
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
	views, err := h.ListUserPluginSettings(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pluginUserSettingsListResponse{Installations: views})
}

// ListUserPluginSettings lists the enabled, non-builtin installations that
// expose user settings or a user-navigable route. v1 GET /settings/plugins
// and v2 listPluginSettings both answer from it; the slice is never nil.
func (h *PluginHandler) ListUserPluginSettings(ctx context.Context) ([]PluginUserSettingsView, error) {
	installations, err := h.installations.ListEnabled(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "listing enabled plugin installations", "component", "api", "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list plugin settings")
	}

	views := make([]PluginUserSettingsView, 0, len(installations))
	for _, installation := range installations {
		// The reserved builtin row has no manifest on disk; without this skip
		// the whole user-scoped settings list would 500.
		if installation.IsBuiltin() {
			continue
		}
		manifest, err := plugins.LoadManifestFile(plugins.InstalledManifestPath(installation.InstallPath))
		if err != nil {
			slog.ErrorContext(ctx, "loading plugin manifest", "component", "api", "installation_id", installation.ID, "error", err)
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to load plugin settings")
		}
		if len(manifest.GetUserConfigSchema()) == 0 && !manifestHasUserNavigableRoute(manifest) {
			continue
		}
		views = append(views, toUserPluginSettingsSummary(installation, manifest))
	}
	return views, nil
}

func (h *PluginHandler) HandleGetUserPluginSettings(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "installation_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}

	view, err := h.GetUserPluginSettings(r.Context(), apimw.GetUserID(r.Context()), id)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, view)
}

// PluginUserSettingsDetailView is one installation's user settings surface
// plus the account's stored values for it.
type PluginUserSettingsDetailView struct {
	Installation PluginUserSettingsView `json:"installation"`
	Values       map[string]string      `json:"values"`
}

// GetUserPluginSettings answers the installation's user settings surface and
// the account's values. v1 GET /settings/plugins/{installation_id} and v2
// getPluginSettings both answer from it; an installation that is unknown,
// disabled, builtin, or without user settings is a 404.
func (h *PluginHandler) GetUserPluginSettings(ctx context.Context, userID, installationID int) (PluginUserSettingsDetailView, error) {
	installation, manifest, err := h.loadUserConfigInstallation(ctx, installationID)
	if err != nil {
		return PluginUserSettingsDetailView{}, err
	}

	values, err := h.userConfig.Get(ctx, userID, installationID)
	if err != nil {
		slog.ErrorContext(ctx, "loading plugin user config", "component", "api", "installation_id", installationID, "error", err)
		return PluginUserSettingsDetailView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to load plugin settings")
	}

	return PluginUserSettingsDetailView{
		Installation: toUserPluginSettingsSummary(installation, manifest),
		Values:       values,
	}, nil
}

func (h *PluginHandler) HandlePutUserPluginSettings(w http.ResponseWriter, r *http.Request) {
	id, err := parseNamedIDParam(r, "installation_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid installation ID")
		return
	}

	if _, _, err := h.loadUserConfigInstallation(r.Context(), id); err != nil {
		writeAPIError(w, err)
		return
	}

	var req userPluginSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if err := h.SetUserPluginSettings(r.Context(), apimw.GetUserID(r.Context()), id, req.Values); err != nil {
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// SetUserPluginSettings replaces the account's values for the installation
// after the same installation checks the read performs. v1 PUT
// /settings/plugins/{installation_id} and v2 updatePluginSettings both go
// through it; a value the plugin's schema rejects is a 400 naming values.
func (h *PluginHandler) SetUserPluginSettings(ctx context.Context, userID, installationID int, values map[string]string) error {
	if _, _, err := h.loadUserConfigInstallation(ctx, installationID); err != nil {
		return err
	}
	if err := h.userConfig.Set(ctx, userID, installationID, values); err != nil {
		return fieldError("values", err.Error())
	}
	return nil
}

// loadUserConfigInstallation resolves an installation that exposes user
// settings. Every refusal is a *APIError the caller renders.
func (h *PluginHandler) loadUserConfigInstallation(
	ctx context.Context,
	installationID int,
) (*plugins.Installation, *pluginv1.PluginManifest, error) {
	installation, err := h.installations.GetByID(ctx, installationID)
	if err != nil {
		if errors.Is(err, plugins.ErrInstallationNotFound) {
			return nil, nil, &APIError{Status: http.StatusNotFound, Code: policyErrorNotFound, Message: "Plugin installation not found", cause: err}
		}
		slog.ErrorContext(ctx, "loading plugin installation", "component", "api", "installation_id", installationID, "error", err)
		return nil, nil, &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal, Message: "Failed to load plugin installation", cause: err}
	}
	if !installation.Enabled || installation.IsBuiltin() {
		return nil, nil, &APIError{Status: http.StatusNotFound, Code: policyErrorNotFound, Message: "Plugin installation not found", cause: plugins.ErrInstallationNotFound}
	}

	manifest, err := h.loadInstallationManifest(ctx, installation)
	if err != nil {
		slog.ErrorContext(ctx, "loading plugin manifest", "component", "api", "installation_id", installationID, "error", err)
		return nil, nil, &APIError{Status: http.StatusInternalServerError, Code: policyErrorInternal, Message: "Failed to load plugin settings", cause: err}
	}
	if len(manifest.GetUserConfigSchema()) == 0 && !manifestHasUserNavigableRoute(manifest) {
		return nil, nil, &APIError{Status: http.StatusNotFound, Code: policyErrorNotFound, Message: "Plugin installation does not expose user settings", cause: plugins.ErrInstallationNotFound}
	}

	return installation, manifest, nil
}

func (h *PluginHandler) buildInstallationResponses(
	ctx context.Context,
	installations []*plugins.Installation,
) ([]PluginInstallationView, error) {
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
	response := make([]PluginInstallationView, 0, len(installations))
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
) (PluginInstallationView, error) {
	repositoriesByID := make(map[int]*plugins.Repository, 1)
	if installation.RepositoryID != nil {
		repository, err := h.repositories.GetByID(ctx, *installation.RepositoryID)
		if err != nil && !errors.Is(err, plugins.ErrRepositoryNotFound) {
			return PluginInstallationView{}, err
		}
		if repository != nil {
			repositoriesByID[repository.ID] = repository
		}
	}

	authBindings, err := h.configs.ListAuthBindings(ctx)
	if err != nil {
		return PluginInstallationView{}, err
	}
	taskBindings, err := h.configs.ListTaskBindings(ctx)
	if err != nil {
		return PluginInstallationView{}, err
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
) (PluginInstallationView, error) {
	if manifest == nil {
		var err error
		manifest, err = h.loadInstallationManifest(ctx, installation)
		if err != nil && !errors.Is(err, plugins.ErrArchiveNotFound) {
			return PluginInstallationView{}, err
		}
	}

	capabilities, err := h.loadInstallationCapabilities(ctx, installation, manifest)
	if err != nil {
		return PluginInstallationView{}, err
	}
	configs, err := h.configs.ListGlobalConfigs(ctx, installation.ID)
	if err != nil {
		return PluginInstallationView{}, err
	}

	var (
		globalConfigSchema []PluginConfigSchemaView
		userConfigSchema   []PluginConfigSchemaView
		routes             []PluginRouteView
		assets             []PluginAssetView
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
	var runtime *PluginRuntimeView
	if h.service != nil {
		state := h.service.RuntimeState(installation.ID)
		runtime = &PluginRuntimeView{Resident: state.Resident, State: string(state.State), RestartCount: state.RestartCount, LastError: state.LastError, LastStartedAt: state.LastStartedAt, NextRestartAt: state.NextRestartAt}
		// Before the supervisor arms (boot) the capability says what will be
		// resident. Once armed, its entries are the truth: an enabled
		// installation it deliberately does not own (a duplicate provider
		// slug) is not resident, so the page must not offer a restart that
		// would be a no-op.
		if !state.Resident && !h.service.ResidentsArmed() {
			for _, capability := range capabilities {
				if plugins.IsResidentCapabilityType(capability.Type) {
					runtime.Resident = true
				}
			}
		}
	}

	return PluginInstallationView{
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
		GlobalConfigs:      configValuesToJSON(configs, manifest),
		AuthBindings:       authBindingsForInstallation(installation.ID, authBindings),
		TaskBindings:       taskBindingsForInstallation(installation.ID, taskBindings),
		CreatedAt:          installation.CreatedAt,
		UpdatedAt:          installation.UpdatedAt,
		Runtime:            runtime,
	}, nil
}

func toPluginPresentationJSON(presentation *pluginv1.PluginPresentation) *PluginPresentationView {
	if presentation == nil {
		return nil
	}
	return &PluginPresentationView{
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
) PluginUserSettingsView {
	return PluginUserSettingsView{
		ID:               installation.ID,
		PluginID:         installation.PluginID,
		Version:          installation.Version,
		UserConfigSchema: configSchemasToJSON(manifest.GetUserConfigSchema()),
		Routes:           routesToJSON(manifest.GetHttpRoutes()),
		Assets:           assetsToJSON(manifest.GetAssets()),
		Category:         manifest.GetCategory(),
	}
}

func configSchemasToJSON(schemas []*pluginv1.ConfigSchema) []PluginConfigSchemaView {
	return plugins.ConfigSchemaViews(schemas)
}

func adminFormToJSON(form *pluginv1.AdminFormDescriptor) *pluginAdminFormJSON {
	return plugins.AdminFormViewFromProto(form)
}

func capabilitiesToJSON(descriptors []*pluginv1.CapabilityDescriptor) []PluginCapabilityView {
	response := make([]PluginCapabilityView, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor == nil {
			continue
		}
		response = append(response, PluginCapabilityView{
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

func routesToJSON(routes []*pluginv1.HttpRouteDescriptor) []PluginRouteView {
	response := make([]PluginRouteView, 0, len(routes))
	for _, route := range routes {
		if route == nil {
			continue
		}
		response = append(response, PluginRouteView{
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

func assetsToJSON(assets []*pluginv1.PackagedAsset) []PluginAssetView {
	response := make([]PluginAssetView, 0, len(assets))
	for _, asset := range assets {
		if asset == nil {
			continue
		}
		response = append(response, PluginAssetView{
			Path:        asset.GetPath(),
			ContentType: asset.GetContentType(),
			Integrity:   asset.GetIntegrity(),
		})
	}
	return response
}

func configValuesToJSON(
	configs []*plugins.RuntimeConfig,
	manifest *pluginv1.PluginManifest,
) []PluginConfigValueView {
	response := make([]PluginConfigValueView, 0, len(configs))
	for _, config := range configs {
		if config == nil {
			continue
		}
		value := make(map[string]any)
		configuredSecrets := make([]string, 0)
		if manifest == nil || !plugins.HasGlobalConfigSchema(manifest, config.Key) {
			// Without a manifest there is no trustworthy sensitivity schema.
			// A row can also outlive a renamed/removed schema after an upgrade;
			// fail closed rather than returning a potentially secret object.
		} else {
			publicFields, secretFields := plugins.GlobalConfigFieldSets(manifest, config.Key)
			for _, field := range publicFields {
				if saved, ok := config.Value[field]; ok {
					value[field] = saved
				}
			}
			for _, field := range secretFields {
				if saved, ok := config.Value[field]; ok && pluginSecretConfigured(saved) {
					configuredSecrets = append(configuredSecrets, field)
				}
			}
		}
		response = append(response, PluginConfigValueView{
			Key:               config.Key,
			Value:             value,
			ConfiguredSecrets: configuredSecrets,
		})
	}
	return response
}

func pluginSecretConfigured(value any) bool {
	if value == nil {
		return false
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) != ""
	}
	return true
}

func authBindingsForInstallation(installationID int, bindings []*plugins.AuthBinding) []PluginAuthBindingView {
	response := make([]PluginAuthBindingView, 0)
	for _, binding := range bindings {
		if binding == nil || binding.InstallationID != installationID {
			continue
		}
		response = append(response, PluginAuthBindingView{
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

func taskBindingsForInstallation(installationID int, bindings []*plugins.TaskBinding) []PluginTaskBindingView {
	response := make([]PluginTaskBindingView, 0)
	for _, binding := range bindings {
		if binding == nil || binding.InstallationID != installationID {
			continue
		}
		response = append(response, PluginTaskBindingView{
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
) ([]PluginCapabilityView, error) {
	if manifest != nil {
		return capabilitiesToJSON(manifest.GetCapabilities()), nil
	}

	records, err := h.installations.ListCapabilities(ctx, installation.ID)
	if err != nil {
		return nil, err
	}

	response := make([]PluginCapabilityView, 0, len(records))
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
