package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/policy"
	subtitleai "github.com/Silo-Server/silo-server/internal/subtitles/ai"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// AdminMetadataRefresher can refresh metadata for individual items.
type AdminMetadataRefresher interface {
	RefreshItem(ctx context.Context, contentID string) error
}

// UserRepository defines the operations the AdminHandler needs on users.
type UserRepository interface {
	List(ctx context.Context) ([]*models.User, error)
	Create(ctx context.Context, input models.CreateUserInput) (*models.User, error)
	Update(ctx context.Context, id int, input models.UpdateUserInput) error
	Delete(ctx context.Context, id int) error
	GetByID(ctx context.Context, id int) (*models.User, error)
}

type AccessGroupValidator interface {
	Get(ctx context.Context, id int64) (*access.Group, error)
}

// ServerSettingsStore provides access to server-wide admin settings.
type ServerSettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	GetAll(ctx context.Context) (map[string]string, error)
}

type AdminJobCreator interface {
	Create(ctx context.Context, input adminjob.CreateJobInput) (*models.AdminJob, error)
	CreateLibraryRefresh(
		ctx context.Context,
		createdByUserID int,
		req adminjob.LibraryRefreshRequest,
		message string,
	) (*models.AdminJob, error)
}

type ItemRefreshScopeResolver interface {
	Resolve(ctx context.Context, contentID string) (*adminjob.ItemRefreshRequest, error)
	ResolveWithMode(ctx context.Context, contentID string, mode adminjob.ItemRefreshMode) (*adminjob.ItemRefreshRequest, error)
}

type ImpersonationService interface {
	StartImpersonation(ctx context.Context, adminUserID, targetUserID int, deviceName, ip string) (*auth.TokenPair, *models.User, *models.User, error)
}

// AdminHandler handles admin-only HTTP endpoints for user management,
// session listing, unmatched files, and system stats.
type AdminHandler struct {
	userRepo                     UserRepository
	pool                         *pgxpool.Pool
	SessionsLoader               *PlaybackSessionsLoader
	storeProv                    userstore.UserStoreProvider
	accountProvisioner           *auth.AccountProvisioner
	DetailSvc                    *catalog.DetailService
	StatsSource                  AdminStatsSource
	Config                       *config.Config
	EventBus                     cache.EventBus
	EventsHub                    *evt.Hub
	SettingsRepo                 ServerSettingsStore
	JobRepo                      AdminJobCreator
	ItemRefreshResolver          ItemRefreshScopeResolver
	ImpersonationService         ImpersonationService
	RealtimeHub                  *notifications.Hub
	AccessGroups                 AccessGroupValidator
	BootstrapSensitiveConfigured map[string]bool
	BootstrapSensitiveValues     map[string]string
	OnUserSessionsRevoked        func(ctx context.Context, userID int)
	OnServerSettingUpdated       func(ctx context.Context, key, value string)
	RestartStatus                *ServerRestartStatusTracker
	CatalogSearchStatus          catalog.CatalogSearchStatusProvider
}

// NewAdminHandler creates a new AdminHandler backed by the given
// user repository and database pool.
func NewAdminHandler(
	userRepo UserRepository,
	pool *pgxpool.Pool,
	storeProv userstore.UserStoreProvider,
) *AdminHandler {
	return &AdminHandler{
		userRepo:           userRepo,
		pool:               pool,
		storeProv:          storeProv,
		accountProvisioner: auth.NewAccountProvisioner(userRepo, storeProv),
	}
}

// --- Request/Response types ---

// createUserRequest represents the JSON body for POST /admin/users.
type createUserRequest struct {
	Username                 string                 `json:"username"`
	Email                    string                 `json:"email"`
	Password                 string                 `json:"password"`
	Role                     string                 `json:"role"`
	Permissions              createStringSliceField `json:"permissions"`
	CreateDefaultProfile     bool                   `json:"create_default_profile"`
	DefaultProfileName       string                 `json:"default_profile_name,omitempty"`
	LibraryIDs               []int                  `json:"library_ids"`
	MaxPlaybackQuality       string                 `json:"max_playback_quality"`
	MaxStreams               *int                   `json:"max_streams,omitempty"`
	MaxTranscodes            *int                   `json:"max_transcodes,omitempty"`
	MaxProfiles              *int                   `json:"max_profiles,omitempty"`
	DownloadAllowed          *bool                  `json:"download_allowed,omitempty"`
	DownloadTranscodeAllowed *bool                  `json:"download_transcode_allowed,omitempty"`
}

type createStringSliceField struct {
	Set   bool
	Value []string
}

func (f *createStringSliceField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Value = []string{}
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}

type updateLibraryIDsField struct {
	Set   bool
	Value []int
}

func (f *updateLibraryIDsField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Value = nil
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}

func (f updateLibraryIDsField) Ptr() *[]int {
	if !f.Set {
		return nil
	}
	value := append([]int(nil), f.Value...)
	return &value
}

type updateStringSliceField struct {
	Set   bool
	Value []string
}

func (f *updateStringSliceField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Value = []string{}
		return nil
	}
	return json.Unmarshal(data, &f.Value)
}

func (f updateStringSliceField) Ptr() *[]string {
	if !f.Set {
		return nil
	}
	value := append([]string(nil), f.Value...)
	return &value
}

// updateUserRequest represents the JSON body for PUT /admin/users/{id}.
type updateUserRequest struct {
	Username                 *string                `json:"username,omitempty"`
	Email                    *string                `json:"email,omitempty"`
	Password                 *string                `json:"password,omitempty"`
	Role                     *string                `json:"role,omitempty"`
	Permissions              updateStringSliceField `json:"permissions,omitempty"`
	Enabled                  *bool                  `json:"enabled,omitempty"`
	LibraryIDs               updateLibraryIDsField  `json:"library_ids,omitempty"`
	MaxPlaybackQuality       *string                `json:"max_playback_quality,omitempty"`
	MaxStreams               *int                   `json:"max_streams,omitempty"`
	MaxTranscodes            *int                   `json:"max_transcodes,omitempty"`
	MaxProfiles              *int                   `json:"max_profiles,omitempty"`
	DownloadAllowed          *bool                  `json:"download_allowed,omitempty"`
	DownloadTranscodeAllowed *bool                  `json:"download_transcode_allowed,omitempty"`
	AccessGroupID            updateAccessGroupField `json:"access_group_id,omitempty"`
}

type updateAccessGroupField struct {
	Set   bool
	Value *int64
}

func (f *updateAccessGroupField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Value = nil
		return nil
	}
	var value int64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.Value = &value
	return nil
}

// adminUserResponse represents a user in admin JSON responses.
type adminUserResponse struct {
	ID                       int        `json:"id"`
	Username                 string     `json:"username"`
	Email                    string     `json:"email"`
	Role                     string     `json:"role"`
	Permissions              []string   `json:"permissions"`
	Enabled                  bool       `json:"enabled"`
	LibraryIDs               []int      `json:"library_ids"`
	MaxPlaybackQuality       string     `json:"max_playback_quality"`
	MaxStreams               int        `json:"max_streams"`
	MaxTranscodes            int        `json:"max_transcodes"`
	MaxProfiles              int        `json:"max_profiles"`
	DownloadAllowed          bool       `json:"download_allowed"`
	DownloadTranscodeAllowed bool       `json:"download_transcode_allowed"`
	AccessGroupID            *int64     `json:"access_group_id"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	LastActiveAt             *time.Time `json:"last_active_at,omitempty"`
}

type adminPlaybackHistoryRow struct {
	SessionID       string    `json:"session_id"`
	UserID          int       `json:"user_id"`
	Username        string    `json:"username"`
	ProfileID       string    `json:"profile_id"`
	ProfileName     string    `json:"profile_name"`
	MediaItemID     string    `json:"media_item_id"`
	MediaFileID     int       `json:"media_file_id"`
	MediaTitle      string    `json:"media_title"`
	MediaType       string    `json:"media_type"`
	PlayMethod      string    `json:"play_method"`
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at"`
	WatchedSeconds  float64   `json:"watched_seconds"`
	DurationSeconds *float64  `json:"duration_seconds"`
	Completed       bool      `json:"completed"`
}

type adminUserProfileRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// unmatchedFileRow represents a media file with no content_id.
type unmatchedFileRow struct {
	ID            int    `json:"id"`
	MediaFolderID int    `json:"media_folder_id"`
	FilePath      string `json:"file_path"`
	FileSize      int64  `json:"file_size"`
	Container     string `json:"container"`
}

// --- Helper ---

// presignPosterURL generates a presigned poster URL for admin sessions.
// Returns empty string if no detail service is configured or the path is empty.
func (h *AdminHandler) presignPosterURL(r *http.Request, path string) string {
	if h.DetailSvc != nil {
		return h.DetailSvc.PresignURL(r.Context(), cardThumbnailPath(path), "card")
	}
	return ""
}

// toAdminUserResponse converts a User model to an admin API response.
func toAdminUserResponse(u *models.User) adminUserResponse {
	resp := adminUserResponse{
		ID:                       u.ID,
		Username:                 u.Username,
		Email:                    u.Email,
		Role:                     u.Role,
		Permissions:              append([]string{}, u.Permissions...),
		Enabled:                  u.Enabled,
		LibraryIDs:               append([]int(nil), u.LibraryIDs...),
		MaxPlaybackQuality:       access.NormalizePlaybackQuality(u.MaxPlaybackQuality),
		MaxStreams:               u.MaxStreams,
		MaxTranscodes:            u.MaxTranscodes,
		MaxProfiles:              u.MaxProfiles,
		DownloadAllowed:          u.DownloadAllowed,
		DownloadTranscodeAllowed: u.DownloadTranscodeAllowed,
		CreatedAt:                u.CreatedAt,
		UpdatedAt:                u.UpdatedAt,
	}
	if u.AccessGroupID != nil {
		id := *u.AccessGroupID
		resp.AccessGroupID = &id
	}
	return resp
}

func (h *AdminHandler) loadUserLastActiveAt(ctx context.Context, userIDs []int) (map[int]time.Time, error) {
	lastActive := make(map[int]time.Time)
	if h == nil || h.pool == nil || len(userIDs) == 0 {
		return lastActive, nil
	}

	rows, err := h.pool.Query(ctx, `
		SELECT user_id, MAX("timestamp") AS last_active_at
		FROM activity_log
		WHERE user_id = ANY($1::int[])
		GROUP BY user_id`, userIDs)
	if err != nil {
		return lastActive, fmt.Errorf("loading user last activity: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var userID int
		var timestamp time.Time
		if err := rows.Scan(&userID, &timestamp); err != nil {
			return lastActive, fmt.Errorf("scanning user last activity: %w", err)
		}
		lastActive[userID] = timestamp
	}
	if err := rows.Err(); err != nil {
		return lastActive, fmt.Errorf("iterating user last activity: %w", err)
	}

	return lastActive, nil
}

func applyLastActiveAt(resp *adminUserResponse, lastActive map[int]time.Time) {
	if resp == nil {
		return
	}
	if timestamp, ok := lastActive[resp.ID]; ok {
		resp.LastActiveAt = &timestamp
	}
}

// --- Handler methods ---

// HandleListUsers handles GET /admin/users.
func (h *AdminHandler) HandleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.userRepo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list users")
		return
	}

	resp := make([]adminUserResponse, 0, len(users))
	userIDs := make([]int, 0, len(users))
	for _, u := range users {
		userIDs = append(userIDs, u.ID)
		resp = append(resp, toAdminUserResponse(u))
	}
	lastActive, err := h.loadUserLastActiveAt(r.Context(), userIDs)
	if err != nil {
		slog.Warn("failed to load admin user last activity", "error", err)
	}
	for i := range resp {
		applyLastActiveAt(&resp[i], lastActive)
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleGetUser handles GET /admin/users/{id}.
func (h *AdminHandler) HandleGetUser(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid user ID")
		return
	}

	user, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "User not found")
		return
	}

	resp := toAdminUserResponse(user)
	lastActive, err := h.loadUserLastActiveAt(r.Context(), []int{user.ID})
	if err != nil {
		slog.Warn("failed to load admin user last activity", "user_id", user.ID, "error", err)
	}
	applyLastActiveAt(&resp, lastActive)

	writeJSON(w, http.StatusOK, resp)
}

// HandleCreateUser handles POST /admin/users.
func (h *AdminHandler) HandleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	req.Username = auth.NormalizeUsername(req.Username)
	req.Email = auth.NormalizeEmail(req.Email)

	if req.Username == "" || req.Email == "" || req.Password == "" || req.Role == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Username, email, password, and role are required")
		return
	}

	maxPlaybackQuality, ok := access.ParsePlaybackQualityPreset(req.MaxPlaybackQuality)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid max_playback_quality")
		return
	}
	if req.MaxProfiles != nil && *req.MaxProfiles < 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "max_profiles must be at least 1")
		return
	}
	permissions := auth.DefaultUserPermissions()
	if req.Permissions.Set {
		permissions = req.Permissions.Value
	}
	permissions, err := auth.NormalizePermissions(permissions)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	user, err := h.accountProvisioner.CreateAccount(r.Context(), auth.CreateAccountInput{
		User: models.CreateUserInput{
			Username:                 req.Username,
			Email:                    req.Email,
			Password:                 req.Password,
			Role:                     req.Role,
			Permissions:              permissions,
			LibraryIDs:               req.LibraryIDs,
			MaxPlaybackQuality:       maxPlaybackQuality,
			MaxStreams:               req.MaxStreams,
			MaxTranscodes:            req.MaxTranscodes,
			MaxProfiles:              req.MaxProfiles,
			DownloadAllowed:          req.DownloadAllowed,
			DownloadTranscodeAllowed: req.DownloadTranscodeAllowed,
		},
		DefaultProfile: auth.DefaultProfileOptions{
			Enabled: req.CreateDefaultProfile,
			Name:    req.DefaultProfileName,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create user")
		return
	}
	h.invalidateStats(r.Context(), cache.ChannelAdmin, cache.EventAdminStatsInvalidated, strconv.Itoa(user.ID))

	writeJSON(w, http.StatusCreated, toAdminUserResponse(user))
}

// HandleUpdateUser handles PUT /admin/users/{id}.
func (h *AdminHandler) HandleUpdateUser(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid user ID")
		return
	}

	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	var maxPlaybackQuality *string
	if req.MaxPlaybackQuality != nil {
		normalized, ok := access.ParsePlaybackQualityPreset(*req.MaxPlaybackQuality)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid max_playback_quality")
			return
		}
		maxPlaybackQuality = &normalized
	}
	if req.MaxProfiles != nil && *req.MaxProfiles < 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "max_profiles must be at least 1")
		return
	}
	if req.AccessGroupID.Set {
		if req.AccessGroupID.Value != nil && *req.AccessGroupID.Value <= 0 {
			writeError(w, http.StatusUnprocessableEntity, "unprocessable_entity", "Invalid access_group_id")
			return
		}
		if req.AccessGroupID.Value != nil {
			if h.AccessGroups == nil {
				writeError(w, http.StatusInternalServerError, "internal_error", "Access groups are not configured")
				return
			}
			if _, err := h.AccessGroups.Get(r.Context(), *req.AccessGroupID.Value); err != nil {
				if errors.Is(err, access.ErrGroupNotFound) {
					writeError(w, http.StatusUnprocessableEntity, "unprocessable_entity", "Invalid access_group_id")
					return
				}
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to validate access group")
				return
			}
		}
	}
	var permissions *[]string
	if req.Permissions.Set {
		normalized, err := auth.NormalizePermissions(req.Permissions.Value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		permissions = &normalized
	}

	updateInput := models.UpdateUserInput{
		Username:                 req.Username,
		Email:                    req.Email,
		Password:                 req.Password,
		Role:                     req.Role,
		Permissions:              permissions,
		Enabled:                  req.Enabled,
		LibraryIDs:               req.LibraryIDs.Ptr(),
		MaxPlaybackQuality:       maxPlaybackQuality,
		MaxStreams:               req.MaxStreams,
		MaxTranscodes:            req.MaxTranscodes,
		MaxProfiles:              req.MaxProfiles,
		DownloadAllowed:          req.DownloadAllowed,
		DownloadTranscodeAllowed: req.DownloadTranscodeAllowed,
		AccessGroupIDSet:         req.AccessGroupID.Set,
		AccessGroupID:            req.AccessGroupID.Value,
	}

	var currentUser *models.User
	if updateMayRequireSessionRevocation(updateInput) {
		currentUser, err = h.userRepo.GetByID(r.Context(), id)
		if err != nil {
			if auth.IsNotFound(err) {
				writeError(w, http.StatusNotFound, "not_found", "User not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch user")
			return
		}
	}

	err = h.userRepo.Update(r.Context(), id, updateInput)
	if err != nil {
		if auth.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "User not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update user")
		return
	}
	if updateRequiresSessionRevocation(currentUser, updateInput) {
		if err := h.revokeUserSessions(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to revoke updated user sessions")
			return
		}
	}

	user, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch updated user")
		return
	}

	writeJSON(w, http.StatusOK, toAdminUserResponse(user))
}

// HandleDeleteUser handles DELETE /admin/users/{id}.
func (h *AdminHandler) HandleDeleteUser(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid user ID")
		return
	}

	err = h.userRepo.Delete(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete user")
		return
	}
	if err := h.revokeUserSessions(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to revoke deleted user sessions")
		return
	}
	h.invalidateStats(r.Context(), cache.ChannelAdmin, cache.EventAdminStatsInvalidated, strconv.Itoa(id))

	w.WriteHeader(http.StatusNoContent)
}

// HandleImpersonateUser handles POST /admin/users/{id}/impersonate.
func (h *AdminHandler) HandleImpersonateUser(w http.ResponseWriter, r *http.Request) {
	if h.ImpersonationService == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Impersonation service unavailable")
		return
	}

	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if claims.TokenType == auth.TokenTypeAPIKey || claims.SessionID == "" {
		writeError(w, http.StatusForbidden, "impersonation_not_allowed", "Impersonation is not allowed")
		return
	}

	targetID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid user ID")
		return
	}

	pair, impersonator, effectiveUser, err := h.ImpersonationService.StartImpersonation(
		auth.WithClaims(r.Context(), claims),
		claims.UserID,
		targetID,
		r.UserAgent(),
		clientip.FromContext(r.Context()),
	)
	if err != nil {
		if auth.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "User not found")
			return
		}
		if errors.Is(err, auth.ErrAlreadyImpersonating) {
			writeError(w, http.StatusConflict, "already_impersonating", "An impersonation session is already active")
			return
		}
		if errors.Is(err, auth.ErrImpersonationNotAllowed) {
			writeError(w, http.StatusForbidden, "impersonation_not_allowed", "Impersonation is not allowed")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start impersonation")
		return
	}

	writeJSON(w, http.StatusOK, buildLoginResponse(pair, effectiveUser, impersonator))
}

// HandleListSessions handles GET /admin/sessions.
// Lists active playback sessions enriched with user and media information.
func (h *AdminHandler) HandleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.loadPlaybackSessions(r.Context(), r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list sessions")
		return
	}

	writeJSON(w, http.StatusOK, sessions)
}

func (h *AdminHandler) loadPlaybackSessions(ctx context.Context, r *http.Request) ([]playbackSessionRow, error) {
	loader, err := resolvePlaybackSessionsLoader(h.SessionsLoader, h.pool, h.storeProv, h.DetailSvc)
	if err != nil {
		return nil, err
	}
	return loader.Load(ctx, r, PlaybackSessionsQuery{})
}

// HandleListPlaybackHistory handles GET /admin/playback-history.
func (h *AdminHandler) HandleListPlaybackHistory(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Database not configured")
		return
	}

	limit, offset := parsePagination(r)
	q := r.URL.Query()

	var (
		args       []any
		conditions []string
		argIndex   = 1
	)

	if userIDStr := strings.TrimSpace(q.Get("user_id")); userIDStr != "" {
		userID, err := strconv.Atoi(userIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid user_id")
			return
		}
		conditions = append(conditions, "h.user_id = $"+strconv.Itoa(argIndex))
		args = append(args, userID)
		argIndex++
	}

	if profileID := strings.TrimSpace(q.Get("profile_id")); profileID != "" {
		conditions = append(conditions, "h.profile_id = $"+strconv.Itoa(argIndex))
		args = append(args, profileID)
		argIndex++
	}

	if mediaItemID := strings.TrimSpace(q.Get("media_item_id")); mediaItemID != "" {
		conditions = append(conditions, "h.media_item_id = $"+strconv.Itoa(argIndex))
		args = append(args, mediaItemID)
		argIndex++
	}

	switch strings.TrimSpace(q.Get("completed")) {
	case "", "all":
	case "true":
		conditions = append(conditions, "h.completed = TRUE")
	case "false":
		conditions = append(conditions, "h.completed = FALSE")
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid completed filter")
		return
	}

	query := `
		SELECT
			h.session_id,
			h.user_id,
			COALESCE(u.username, ''),
			h.profile_id,
			COALESCE(NULLIF(h.profile_name, ''), h.profile_id),
			h.media_item_id,
			h.media_file_id,
			COALESCE(ep.title, mi.title, ''),
			COALESCE(CASE WHEN ep.content_id IS NOT NULL THEN 'episode' ELSE mi.type END, ''),
			h.play_method,
			h.started_at,
			h.ended_at,
			h.watched_seconds,
			h.duration_seconds,
			h.completed
		FROM playback_history_admin h
		LEFT JOIN users u ON u.id = h.user_id
		LEFT JOIN media_items mi ON mi.content_id = h.media_item_id
		LEFT JOIN episodes ep ON ep.content_id = h.media_item_id
	`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY h.ended_at DESC"
	query += " LIMIT $" + strconv.Itoa(argIndex)
	args = append(args, limit)
	argIndex++
	query += " OFFSET $" + strconv.Itoa(argIndex)
	args = append(args, offset)

	rows, err := h.pool.Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list playback history")
		return
	}
	defer rows.Close()

	history := make([]adminPlaybackHistoryRow, 0)
	for rows.Next() {
		var row adminPlaybackHistoryRow
		if err := rows.Scan(
			&row.SessionID,
			&row.UserID,
			&row.Username,
			&row.ProfileID,
			&row.ProfileName,
			&row.MediaItemID,
			&row.MediaFileID,
			&row.MediaTitle,
			&row.MediaType,
			&row.PlayMethod,
			&row.StartedAt,
			&row.EndedAt,
			&row.WatchedSeconds,
			&row.DurationSeconds,
			&row.Completed,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to scan playback history row")
			return
		}
		history = append(history, row)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to iterate playback history")
		return
	}

	writeJSON(w, http.StatusOK, history)
}

// HandleListUserProfiles handles GET /admin/users/{id}/profiles.
func (h *AdminHandler) HandleListUserProfiles(w http.ResponseWriter, r *http.Request) {
	if h.storeProv == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "User store not configured")
		return
	}

	idStr := chi.URLParam(r, "id")
	userID, err := strconv.Atoi(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid user ID")
		return
	}

	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	if store == nil {
		writeError(w, http.StatusNotFound, "not_found", "User store not found")
		return
	}

	profiles, err := store.ListProfiles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list profiles")
		return
	}

	resp := make([]adminUserProfileRow, 0, len(profiles))
	for _, profile := range profiles {
		resp = append(resp, adminUserProfileRow{ID: profile.ID, Name: profile.Name})
	}

	writeJSON(w, http.StatusOK, resp)
}

func updateMayRequireSessionRevocation(input models.UpdateUserInput) bool {
	return input.Password != nil ||
		input.Role != nil ||
		input.Enabled != nil ||
		input.Permissions != nil ||
		input.MaxPlaybackQuality != nil ||
		input.AccessGroupIDSet
}

func updateRequiresSessionRevocation(current *models.User, input models.UpdateUserInput) bool {
	if input.Password != nil {
		return true
	}
	if current == nil {
		return updateMayRequireSessionRevocation(input)
	}
	if input.Role != nil && *input.Role != current.Role {
		return true
	}
	if input.Enabled != nil && *input.Enabled != current.Enabled {
		return true
	}
	if input.Permissions != nil && !slices.Equal(*input.Permissions, current.Permissions) {
		return true
	}
	if input.MaxPlaybackQuality != nil &&
		access.NormalizePlaybackQuality(*input.MaxPlaybackQuality) != access.NormalizePlaybackQuality(current.MaxPlaybackQuality) {
		return true
	}
	if input.AccessGroupIDSet && !accessGroupIDEqual(input.AccessGroupID, current.AccessGroupID) {
		return true
	}
	return false
}

func accessGroupIDEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (h *AdminHandler) revokeUserSessions(ctx context.Context, userID int) error {
	if h.pool == nil {
		return nil
	}
	sessionRepo := auth.NewSessionRepository(h.pool)
	if err := sessionRepo.RevokeAllByUser(ctx, userID); err != nil {
		return err
	}
	if err := sessionRepo.RevokeAllByImpersonator(ctx, userID); err != nil {
		return err
	}
	if h.OnUserSessionsRevoked != nil {
		h.OnUserSessionsRevoked(ctx, userID)
	}
	return nil
}

// HandleListUnmatched handles GET /admin/unmatched.
// Lists media files that have not been matched to content (content_id IS NULL).
func (h *AdminHandler) HandleListUnmatched(w http.ResponseWriter, r *http.Request) {
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

	rows, err := h.pool.Query(r.Context(),
		`SELECT id, media_folder_id, file_path, file_size, container
		 FROM media_files
		 WHERE content_id IS NULL
		 ORDER BY id ASC
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list unmatched files")
		return
	}
	defer rows.Close()

	files := make([]unmatchedFileRow, 0)
	for rows.Next() {
		var f unmatchedFileRow
		if err := rows.Scan(&f.ID, &f.MediaFolderID, &f.FilePath, &f.FileSize, &f.Container); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to scan file")
			return
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to iterate files")
		return
	}

	writeJSON(w, http.StatusOK, files)
}

// HandleGetStats handles GET /admin/stats.
// Returns system statistics for the admin dashboard.
func (h *AdminHandler) HandleGetStats(w http.ResponseWriter, r *http.Request) {
	var resp AdminStats

	if h.StatsSource != nil {
		if isTruthyQuery(r.URL.Query().Get("refresh")) {
			h.StatsSource.Invalidate()
		}
		stats, err := h.StatsSource.Get(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get stats")
			return
		}
		resp = stats
	} else if h.pool != nil {
		stats, err := queryAdminStats(r.Context(), h.pool)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get stats")
			return
		}
		resp = stats
	} else {
		// Fallback: use the user repository when PG pool is not available.
		users, err := h.userRepo.List(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to count users")
			return
		}
		resp.TotalUsers = len(users)
	}

	writeJSON(w, http.StatusOK, resp)
}

func isTruthyQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (h *AdminHandler) invalidateStats(ctx context.Context, channel, eventType, payload string) {
	if h.StatsSource != nil {
		h.StatsSource.Invalidate()
	}
	h.publishStatsEvent(ctx, channel, eventType, payload)
}

func (h *AdminHandler) publishStatsEvent(ctx context.Context, channel, eventType, payload string) {
	if h.EventBus == nil {
		return
	}
	if err := h.EventBus.Publish(ctx, channel, cache.Event{Type: eventType, Payload: payload}); err != nil {
		slog.Warn("admin: failed to publish stats invalidation event",
			"channel", channel,
			"type", eventType,
			"error", err,
		)
	}
}

type refreshItemMetadataRequest struct {
	Mode string `json:"mode"`
}

// HandleRefreshItemMetadata handles POST /admin/items/{id}/refresh-metadata.
func (h *AdminHandler) HandleRefreshItemMetadata(w http.ResponseWriter, r *http.Request) {
	if h.JobRepo == nil || h.ItemRefreshResolver == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Item refresh jobs are not configured")
		return
	}

	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	mode := adminjob.ItemRefreshModeQuick
	if r.Body != nil {
		var req refreshItemMetadataRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
			return
		}
		if req.Mode != "" {
			switch adminjob.ItemRefreshMode(req.Mode) {
			case adminjob.ItemRefreshModeQuick, adminjob.ItemRefreshModeComplete:
				mode = adminjob.ItemRefreshMode(req.Mode)
			default:
				writeError(w, http.StatusBadRequest, "bad_request", "Invalid refresh mode")
				return
			}
		}
	}

	payload, err := h.ItemRefreshResolver.ResolveWithMode(r.Context(), contentID, mode)
	if err != nil {
		var scopeErr *adminjob.ScopeResolutionError
		if errors.As(err, &scopeErr) {
			code := "bad_request"
			if scopeErr.StatusCode == http.StatusNotFound {
				code = "not_found"
			} else if scopeErr.StatusCode >= http.StatusConflict {
				code = "conflict"
			}
			writeError(w, scopeErr.StatusCode, code, scopeErr.Message)
			return
		}
		slog.Error("admin: resolve item refresh scope failed", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve item refresh scope")
		return
	}

	job, err := h.JobRepo.Create(r.Context(), adminjob.CreateJobInput{
		JobType:         adminjob.JobTypeItemRefresh,
		CreatedByUserID: currentAdminUserID(r),
		RequestPayload:  payload,
		Message:         "Queued item metadata refresh",
	})
	if err != nil {
		slog.Error("admin: create item refresh job failed", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to queue item metadata refresh")
		return
	}
	if h.RealtimeHub != nil {
		publishEventJob(r.Context(), h.RealtimeHub.EventsHub(), "job.created", job)
	}

	writeJSON(w, http.StatusAccepted, adminJobToResponseForClaims(r, job, nil, apimw.GetClaims(r.Context())))
}

// UpdateItemMetadataRequest contains the fields that can be updated via
// PATCH /admin/items/{id}/metadata.
type UpdateItemMetadataRequest struct {
	Title            *string   `json:"title"`
	SortTitle        *string   `json:"sort_title"`
	OriginalTitle    *string   `json:"original_title"`
	Overview         *string   `json:"overview"`
	Tagline          *string   `json:"tagline"`
	ContentRating    *string   `json:"content_rating"`
	Year             *int      `json:"year"`
	Runtime          *int      `json:"runtime"`
	Genres           *[]string `json:"genres"`
	Studios          *[]string `json:"studios"`
	Networks         *[]string `json:"networks"`
	Countries        *[]string `json:"countries"`
	ReleaseDate      *string   `json:"release_date"`
	FirstAirDate     *string   `json:"first_air_date"`
	LastAirDate      *string   `json:"last_air_date"`
	AirTime          *string   `json:"air_time"`
	AirTimezone      *string   `json:"air_timezone"`
	AirDate          *string   `json:"air_date"`
	Status           *string   `json:"status"`
	RatingIMDB       *float64  `json:"rating_imdb"`
	RatingTMDB       *float64  `json:"rating_tmdb"`
	RatingRTCritic   *int      `json:"rating_rt_critic"`
	RatingRTAudience *int      `json:"rating_rt_audience"`
	ImdbID           *string   `json:"imdb_id"`
	TmdbID           *string   `json:"tmdb_id"`
	TvdbID           *string   `json:"tvdb_id"`
	SeasonNumber     *int      `json:"season_number"`
	EpisodeNumber    *int      `json:"episode_number"`
	LockedFields     *[]int    `json:"locked_fields"`
}

// HandleUpdateItemMetadata handles PATCH /admin/items/{id}/metadata.
func (h *AdminHandler) HandleUpdateItemMetadata(w http.ResponseWriter, r *http.Request) {
	contentID := chi.URLParam(r, "id")
	if contentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	var req UpdateItemMetadataRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.AirTimezone != nil {
		trimmed := strings.TrimSpace(*req.AirTimezone)
		req.AirTimezone = &trimmed
		if !catalog.ValidateAirTimezone(trimmed) {
			writeError(w, http.StatusBadRequest, "bad_request", "air_timezone must be a valid IANA timezone")
			return
		}
	}

	upd := catalog.MetadataUpdate{
		Title: req.Title, SortTitle: req.SortTitle, OriginalTitle: req.OriginalTitle,
		Overview: req.Overview, Tagline: req.Tagline, ContentRating: req.ContentRating,
		Year: req.Year, Runtime: req.Runtime,
		Genres: req.Genres, Studios: req.Studios, Networks: req.Networks, Countries: req.Countries,
		ReleaseDate: req.ReleaseDate, FirstAirDate: req.FirstAirDate, LastAirDate: req.LastAirDate,
		AirTime: req.AirTime, AirTimezone: req.AirTimezone,
		AirDate: req.AirDate, Status: req.Status,
		RatingIMDB: req.RatingIMDB, RatingTMDB: req.RatingTMDB,
		RatingRTCritic: req.RatingRTCritic, RatingRTAudience: req.RatingRTAudience,
		ImdbID: req.ImdbID, TmdbID: req.TmdbID, TvdbID: req.TvdbID,
		SeasonNumber: req.SeasonNumber, EpisodeNumber: req.EpisodeNumber,
		LockedFields: req.LockedFields,
	}

	// Try media_items first, then seasons, then episodes.
	if err := h.DetailSvc.UpdateMediaItemMetadata(r.Context(), contentID, &upd); err != nil {
		if !errors.Is(err, catalog.ErrItemNotFound) {
			slog.Error("admin: update item metadata failed", "content_id", contentID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update metadata")
			return
		}
		if err := h.DetailSvc.UpdateSeasonMetadata(r.Context(), contentID, &upd); err != nil {
			if !errors.Is(err, catalog.ErrSeasonNotFound) {
				slog.Error("admin: update season metadata failed", "content_id", contentID, "error", err)
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update metadata")
				return
			}
			if err := h.DetailSvc.UpdateEpisodeMetadata(r.Context(), contentID, &upd); err != nil {
				if errors.Is(err, catalog.ErrEpisodeNotFound) {
					writeError(w, http.StatusNotFound, "not_found", "Item not found")
					return
				}
				slog.Error("admin: update episode metadata failed", "content_id", contentID, "error", err)
				writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update metadata")
				return
			}
		}
	}

	if h.EventBus != nil {
		_ = h.EventBus.Publish(r.Context(), cache.ChannelAdmin,
			cache.Event{Type: "item:updated", Payload: contentID})
	}
	if h.RealtimeHub != nil {
		publishEventMetadataUpdate(r.Context(), h.RealtimeHub.EventsHub(), 0, contentID)
	}

	detail, err := h.DetailSvc.GetItemDetail(r.Context(), contentID, catalog.AccessFilter{})
	if err != nil {
		slog.Error("admin: fetch updated detail failed", "content_id", contentID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Updated but failed to fetch result")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// --- Server Settings endpoints ---

// sensitiveSettingKeys is the audited allowlist of secret-bearing settings
// keys, shared with the at-rest encryption decorator so redaction and
// encryption can never drift apart. See catalog.SensitiveSettingKeys.
var sensitiveSettingKeys = catalog.SensitiveSettingKeys

// HandleGetSettings handles GET /admin/settings.
func (h *AdminHandler) HandleGetSettings(w http.ResponseWriter, r *http.Request) {
	if h.SettingsRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Settings store not configured")
		return
	}
	all, err := h.SettingsRepo.GetAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load settings")
		return
	}
	for key := range sensitiveSettingKeys {
		delete(all, key)
	}
	writeJSON(w, http.StatusOK, all)
}

type sensitiveStatusResponse struct {
	Configured   []string `json:"configured"`
	ManagedByEnv []string `json:"managed_by_env,omitempty"`
}

// HandleGetSensitiveStatus handles GET /admin/settings/sensitive-status.
// Returns which sensitive keys are configured and which are managed by env.
func (h *AdminHandler) HandleGetSensitiveStatus(w http.ResponseWriter, r *http.Request) {
	if h.SettingsRepo == nil {
		writeError(w, http.StatusInternalServerError, "settings_error", "settings not configured")
		return
	}
	all, err := h.SettingsRepo.GetAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "settings_error", err.Error())
		return
	}
	configuredSet := make(map[string]struct{})
	for key := range sensitiveSettingKeys {
		if v, ok := all[key]; ok && v != "" {
			configuredSet[key] = struct{}{}
		}
	}
	for key, configured := range h.BootstrapSensitiveConfigured {
		if configured && sensitiveSettingKeys[key] {
			configuredSet[key] = struct{}{}
		}
	}
	for key, value := range h.BootstrapSensitiveValues {
		if value != "" && sensitiveSettingKeys[key] {
			configuredSet[key] = struct{}{}
		}
	}
	configured := make([]string, 0, len(configuredSet))
	for key := range configuredSet {
		configured = append(configured, key)
	}
	sort.Strings(configured)

	managedByEnv := make([]string, 0, len(h.BootstrapSensitiveConfigured))
	for key, configured := range h.BootstrapSensitiveConfigured {
		if configured && sensitiveSettingKeys[key] {
			managedByEnv = append(managedByEnv, key)
		}
	}
	sort.Strings(managedByEnv)

	writeJSON(w, http.StatusOK, sensitiveStatusResponse{
		Configured:   configured,
		ManagedByEnv: managedByEnv,
	})
}

type adminSettingResponse struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// RestartRequired reports whether the saved value only takes effect
	// after a server restart (set on update responses only).
	RestartRequired bool `json:"restart_required,omitempty"`
}

type adminSettingsListResponse struct {
	Settings []adminSettingResponse `json:"settings"`
}

type adminDeviceSettingResponse struct {
	UserID         int    `json:"user_id"`
	ProfileID      string `json:"profile_id"`
	ProfileName    string `json:"profile_name,omitempty"`
	DeviceID       string `json:"device_id"`
	DeviceName     string `json:"device_name"`
	DevicePlatform string `json:"device_platform"`
	Key            string `json:"key"`
	Value          string `json:"value"`
	UpdatedAt      string `json:"updated_at"`
}

type adminDeviceSettingsListResponse struct {
	Settings []adminDeviceSettingResponse `json:"settings"`
}

type adminDeviceProfileSummary struct {
	ProfileID     string `json:"profile_id"`
	ProfileName   string `json:"profile_name"`
	OverrideCount int    `json:"override_count"`
	LastUpdated   string `json:"last_updated"`
}

type adminDeviceSummaryResponse struct {
	UserID         int                         `json:"user_id"`
	Username       string                      `json:"username"`
	Email          string                      `json:"email"`
	DeviceID       string                      `json:"device_id"`
	DeviceName     string                      `json:"device_name"`
	DevicePlatform string                      `json:"device_platform"`
	OverrideCount  int                         `json:"override_count"`
	ProfileCount   int                         `json:"profile_count"`
	Profiles       []adminDeviceProfileSummary `json:"profiles"`
	LastUpdated    string                      `json:"last_updated"`
}

type adminDevicesListResponse struct {
	Devices []adminDeviceSummaryResponse `json:"devices"`
}

type adminDeviceDetailResponse struct {
	UserID         int                          `json:"user_id"`
	Username       string                       `json:"username"`
	Email          string                       `json:"email"`
	DeviceID       string                       `json:"device_id"`
	DeviceName     string                       `json:"device_name"`
	DevicePlatform string                       `json:"device_platform"`
	OverrideCount  int                          `json:"override_count"`
	ProfileCount   int                          `json:"profile_count"`
	Profiles       []adminDeviceProfileSummary  `json:"profiles"`
	LastUpdated    string                       `json:"last_updated"`
	Settings       []adminDeviceSettingResponse `json:"settings"`
}

// HandleListUserSettings handles GET /admin/users/{id}/settings.
func (h *AdminHandler) HandleListUserSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	entries, err := store.ListSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list settings")
		return
	}
	resp := adminSettingsListResponse{
		Settings: make([]adminSettingResponse, 0, len(entries)),
	}
	for _, entry := range entries {
		if !keyUsesUserScope(entry.Key) {
			continue
		}
		resp.Settings = append(resp.Settings, adminSettingResponse{
			Key:   entry.Key,
			Value: entry.Value,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleGetUserSetting handles GET /admin/users/{id}/settings/{key}.
func (h *AdminHandler) HandleGetUserSetting(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if !keyUsesUserScope(key) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%s is not a %s setting", key, scopeUser))
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	value, err := store.GetSetting(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load setting")
		return
	}
	if value == "" {
		entries, err := store.ListSettings(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load setting")
			return
		}
		found := false
		for _, entry := range entries {
			if entry.Key == key {
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, "not_found", "Setting not found")
			return
		}
	}
	writeJSON(w, http.StatusOK, adminSettingResponse{Key: key, Value: value})
}

// HandleUpdateUserSetting handles PUT /admin/users/{id}/settings/{key}.
func (h *AdminHandler) HandleUpdateUserSetting(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if !keyUsesUserScope(key) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%s is not a %s setting", key, scopeUser))
		return
	}
	var req updateSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := validateRegisteredSetting(key, req.Value, scopeUser); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	if err := store.SetSetting(r.Context(), key, req.Value); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update setting")
		return
	}
	writeJSON(w, http.StatusOK, adminSettingResponse{Key: key, Value: req.Value})
}

// HandleDeleteUserSetting handles DELETE /admin/users/{id}/settings/{key}.
func (h *AdminHandler) HandleDeleteUserSetting(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if !keyUsesUserScope(key) {
		writeError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("%s is not a %s setting", key, scopeUser))
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	if err := store.DeleteSetting(r.Context(), key); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete setting")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleListUserDeviceSettings handles GET /admin/users/{id}/device-settings.
func (h *AdminHandler) HandleListUserDeviceSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	entries, err := store.ListAllDeviceSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list device settings")
		return
	}
	profileNames, err := listProfileNamesByID(r.Context(), store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list profiles")
		return
	}
	writeJSON(w, http.StatusOK, buildAdminDeviceSettingsResponse(userID, profileNames, entries))
}

// HandleListUserDeviceSettingsByKey handles GET /admin/users/{id}/device-settings/{key}.
func (h *AdminHandler) HandleListUserDeviceSettingsByKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	entries, err := store.ListDeviceSettings(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list device settings")
		return
	}
	profileNames, err := listProfileNamesByID(r.Context(), store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list profiles")
		return
	}
	writeJSON(w, http.StatusOK, buildAdminDeviceSettingsResponse(userID, profileNames, entries))
}

// HandleUpdateUserDeviceSetting handles PUT /admin/users/{id}/profiles/{profile_id}/device-settings/{key}/{device_id}.
func (h *AdminHandler) HandleUpdateUserDeviceSetting(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return
	}
	profileID := strings.TrimSpace(chi.URLParam(r, "profile_id"))
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	deviceID := strings.TrimSpace(chi.URLParam(r, "device_id"))
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Profile id is required")
		return
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Device id is required")
		return
	}
	var req updateSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if err := validateRegisteredSetting(key, req.Value, scopeDevice); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	if !adminProfileExists(w, r, store, profileID) {
		return
	}
	existing, err := store.GetDeviceSetting(r.Context(), profileID, deviceID, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load device setting")
		return
	}
	entry := userstore.DeviceSettingEntry{
		ProfileID: profileID,
		DeviceID:  deviceID,
		Key:       key,
		Value:     req.Value,
	}
	if existing != nil {
		entry.DeviceName = existing.DeviceName
		entry.DevicePlatform = existing.DevicePlatform
	} else if registered, err := registeredDeviceForProfile(r.Context(), store, profileID, deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load device")
		return
	} else if registered != nil {
		entry.DeviceName = registered.DeviceName
		entry.DevicePlatform = registered.DevicePlatform
	}
	if err := store.SetDeviceSetting(r.Context(), entry); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update device setting")
		return
	}
	writeJSON(w, http.StatusOK, adminSettingResponse{Key: key, Value: req.Value})
}

// HandleDeleteUserDeviceSetting handles DELETE /admin/users/{id}/profiles/{profile_id}/device-settings/{key}/{device_id}.
func (h *AdminHandler) HandleDeleteUserDeviceSetting(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return
	}
	profileID := strings.TrimSpace(chi.URLParam(r, "profile_id"))
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	deviceID := strings.TrimSpace(chi.URLParam(r, "device_id"))
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Profile id is required")
		return
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Device id is required")
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	if !adminProfileExists(w, r, store, profileID) {
		return
	}
	if err := store.DeleteDeviceSetting(r.Context(), profileID, deviceID, key); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete device setting")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteAllUserDeviceSettings handles DELETE /admin/users/{id}/profiles/{profile_id}/devices/{device_id}/settings.
func (h *AdminHandler) HandleDeleteAllUserDeviceSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return
	}
	profileID := strings.TrimSpace(chi.URLParam(r, "profile_id"))
	deviceID := strings.TrimSpace(chi.URLParam(r, "device_id"))
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Profile id is required")
		return
	}
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Device id is required")
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	if !adminProfileExists(w, r, store, profileID) {
		return
	}
	if err := store.DeleteAllDeviceSettings(r.Context(), profileID, deviceID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete device settings")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteUserDeviceSettingsByKey handles DELETE /admin/users/{id}/device-settings/{key}.
func (h *AdminHandler) HandleDeleteUserDeviceSettingsByKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := parseAdminUserIDParam(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(chi.URLParam(r, "key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	if err := store.DeleteDeviceSettingsByKey(r.Context(), key); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete device settings")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleListDevices handles GET /admin/devices.
func (h *AdminHandler) HandleListDevices(w http.ResponseWriter, r *http.Request) {
	if h.userRepo == nil || h.storeProv == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Device settings not configured")
		return
	}

	users, err := h.userRepo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list devices")
		return
	}

	perUser := make([][]adminDeviceSummaryResponse, len(users))
	g, gctx := errgroup.WithContext(r.Context())
	g.SetLimit(8)
	for i, user := range users {
		i, user := i, user
		g.Go(func() error {
			store, err := h.storeProv.ForUser(gctx, user.ID)
			if err != nil {
				return fmt.Errorf("user store: %w", err)
			}
			entries, err := store.ListAllDeviceSettings(gctx)
			if err != nil {
				return fmt.Errorf("list device settings: %w", err)
			}
			devices, err := listRegisteredDevices(gctx, store)
			if err != nil {
				return fmt.Errorf("list devices: %w", err)
			}
			profileNames, err := listProfileNamesByID(gctx, store)
			if err != nil {
				slog.Warn("admin list devices profile lookup failed",
					"user_id", user.ID,
					"error", err,
				)
				profileNames = map[string]string{}
			}
			perUser[i] = buildAdminDeviceSummaries(
				user.ID,
				user.Username,
				user.Email,
				entries,
				devices,
				profileNames,
			)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		slog.Error("admin list devices failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list devices")
		return
	}

	devices := make([]adminDeviceSummaryResponse, 0)
	for _, batch := range perUser {
		devices = append(devices, batch...)
	}

	sort.Slice(devices, func(i, j int) bool {
		if devices[i].LastUpdated != devices[j].LastUpdated {
			return devices[i].LastUpdated > devices[j].LastUpdated
		}
		if devices[i].Username != devices[j].Username {
			return devices[i].Username < devices[j].Username
		}
		if devices[i].DeviceName != devices[j].DeviceName {
			return devices[i].DeviceName < devices[j].DeviceName
		}
		return devices[i].DeviceID < devices[j].DeviceID
	})

	writeJSON(w, http.StatusOK, adminDevicesListResponse{Devices: devices})
}

// HandleGetDevice handles GET /admin/devices/{user_id}/{device_id}.
func (h *AdminHandler) HandleGetDevice(w http.ResponseWriter, r *http.Request) {
	if h.userRepo == nil || h.storeProv == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Device settings not configured")
		return
	}

	userIDRaw := strings.TrimSpace(chi.URLParam(r, "user_id"))
	userID, err := strconv.Atoi(userIDRaw)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid user id")
		return
	}
	deviceID := strings.TrimSpace(chi.URLParam(r, "device_id"))
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Device id is required")
		return
	}

	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil || user == nil {
		writeError(w, http.StatusNotFound, "not_found", "User not found")
		return
	}
	store, ok := h.adminUserStore(w, r, userID)
	if !ok {
		return
	}
	entries, err := store.ListAllDeviceSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load device")
		return
	}
	registeredDevices, err := listRegisteredDevices(r.Context(), store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load device")
		return
	}
	profileNames, err := listProfileNamesByID(r.Context(), store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list profiles")
		return
	}

	deviceEntries := make([]userstore.DeviceSettingEntry, 0)
	for _, entry := range entries {
		if entry.DeviceID == deviceID {
			deviceEntries = append(deviceEntries, entry)
		}
	}
	deviceRegistrations := make([]userstore.DeviceEntry, 0)
	for _, entry := range registeredDevices {
		if entry.DeviceID == deviceID {
			deviceRegistrations = append(deviceRegistrations, entry)
		}
	}
	summaries := buildAdminDeviceSummaries(
		user.ID,
		user.Username,
		user.Email,
		deviceEntries,
		deviceRegistrations,
		profileNames,
	)
	if len(summaries) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "Device not found")
		return
	}

	summary := summaries[0]
	writeJSON(w, http.StatusOK, adminDeviceDetailResponse{
		UserID:         user.ID,
		Username:       user.Username,
		Email:          user.Email,
		DeviceID:       summary.DeviceID,
		DeviceName:     summary.DeviceName,
		DevicePlatform: summary.DevicePlatform,
		OverrideCount:  summary.OverrideCount,
		ProfileCount:   summary.ProfileCount,
		Profiles:       summary.Profiles,
		LastUpdated:    summary.LastUpdated,
		Settings:       buildAdminDeviceSettingsResponse(user.ID, profileNames, deviceEntries).Settings,
	})
}

func listRegisteredDevices(ctx context.Context, store userstore.UserStore) ([]userstore.DeviceEntry, error) {
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		return nil, nil
	}
	return registry.ListDevices(ctx)
}

func registeredDeviceForProfile(
	ctx context.Context,
	store userstore.UserStore,
	profileID string,
	deviceID string,
) (*userstore.DeviceEntry, error) {
	devices, err := listRegisteredDevices(ctx, store)
	if err != nil {
		return nil, err
	}
	for _, device := range devices {
		if device.ProfileID == profileID && device.DeviceID == deviceID {
			matched := device
			return &matched, nil
		}
	}
	return nil, nil
}

func buildAdminDeviceSettingsResponse(userID int, profileNames map[string]string, entries []userstore.DeviceSettingEntry) adminDeviceSettingsListResponse {
	resp := adminDeviceSettingsListResponse{
		Settings: make([]adminDeviceSettingResponse, 0, len(entries)),
	}
	for _, entry := range entries {
		resp.Settings = append(resp.Settings, adminDeviceSettingResponse{
			UserID:         userID,
			ProfileID:      entry.ProfileID,
			ProfileName:    profileNames[entry.ProfileID],
			DeviceID:       entry.DeviceID,
			DeviceName:     entry.DeviceName,
			DevicePlatform: entry.DevicePlatform,
			Key:            entry.Key,
			Value:          entry.Value,
			UpdatedAt:      entry.UpdatedAt,
		})
	}
	return resp
}

func buildAdminDeviceSummaries(
	userID int,
	username string,
	email string,
	entries []userstore.DeviceSettingEntry,
	registeredDevices []userstore.DeviceEntry,
	profileNames map[string]string,
) []adminDeviceSummaryResponse {
	type profileAccumulator struct {
		summary adminDeviceProfileSummary
		keys    map[string]struct{}
	}
	type summary struct {
		device   adminDeviceSummaryResponse
		keys     map[string]struct{}
		profiles map[string]*profileAccumulator
	}

	byDevice := make(map[string]*summary)

	ensureDevice := func(deviceID, deviceName, devicePlatform, lastUpdated string) *summary {
		if deviceID == "" {
			return nil
		}
		current, ok := byDevice[deviceID]
		if !ok {
			current = &summary{
				device: adminDeviceSummaryResponse{
					UserID:         userID,
					Username:       username,
					Email:          email,
					DeviceID:       deviceID,
					DeviceName:     deviceName,
					DevicePlatform: devicePlatform,
					LastUpdated:    lastUpdated,
				},
				keys:     make(map[string]struct{}),
				profiles: make(map[string]*profileAccumulator),
			}
			byDevice[deviceID] = current
		}
		if current.device.DeviceName == "" && deviceName != "" {
			current.device.DeviceName = deviceName
		}
		if current.device.DevicePlatform == "" && devicePlatform != "" {
			current.device.DevicePlatform = devicePlatform
		}
		if lastUpdated > current.device.LastUpdated {
			current.device.LastUpdated = lastUpdated
			if deviceName != "" {
				current.device.DeviceName = deviceName
			}
			if devicePlatform != "" {
				current.device.DevicePlatform = devicePlatform
			}
		}
		return current
	}

	ensureProfile := func(current *summary, profileID, lastUpdated string) *profileAccumulator {
		if current == nil || profileID == "" {
			return nil
		}
		profile, exists := current.profiles[profileID]
		if !exists {
			profile = &profileAccumulator{
				summary: adminDeviceProfileSummary{
					ProfileID:   profileID,
					ProfileName: profileNames[profileID],
					LastUpdated: lastUpdated,
				},
				keys: make(map[string]struct{}),
			}
			current.profiles[profileID] = profile
			return profile
		}
		if lastUpdated > profile.summary.LastUpdated {
			profile.summary.LastUpdated = lastUpdated
		}
		return profile
	}

	for _, device := range registeredDevices {
		deviceID := strings.TrimSpace(device.DeviceID)
		profileID := strings.TrimSpace(device.ProfileID)
		current := ensureDevice(
			deviceID,
			device.DeviceName,
			device.DevicePlatform,
			device.LastSeenAt,
		)
		ensureProfile(current, profileID, device.LastSeenAt)
	}

	for _, entry := range entries {
		deviceID := strings.TrimSpace(entry.DeviceID)
		profileID := strings.TrimSpace(entry.ProfileID)
		current := ensureDevice(
			deviceID,
			entry.DeviceName,
			entry.DevicePlatform,
			entry.UpdatedAt,
		)
		if current == nil {
			continue
		}
		if profileID != "" && entry.Key != "" {
			current.keys[profileID+":"+entry.Key] = struct{}{}
		}
		profile := ensureProfile(current, profileID, entry.UpdatedAt)
		if profile != nil && entry.Key != "" {
			profile.keys[entry.Key] = struct{}{}
		}
	}

	devices := make([]adminDeviceSummaryResponse, 0, len(byDevice))
	for _, current := range byDevice {
		current.device.OverrideCount = len(current.keys)
		current.device.ProfileCount = len(current.profiles)
		profiles := make([]adminDeviceProfileSummary, 0, len(current.profiles))
		for _, profile := range current.profiles {
			profile.summary.OverrideCount = len(profile.keys)
			profiles = append(profiles, profile.summary)
		}
		sort.Slice(profiles, func(i, j int) bool {
			if profiles[i].LastUpdated != profiles[j].LastUpdated {
				return profiles[i].LastUpdated > profiles[j].LastUpdated
			}
			a := profiles[i].ProfileName
			b := profiles[j].ProfileName
			if a == "" {
				a = profiles[i].ProfileID
			}
			if b == "" {
				b = profiles[j].ProfileID
			}
			return a < b
		})
		current.device.Profiles = profiles
		devices = append(devices, current.device)
	}
	return devices
}

func listProfileNamesByID(ctx context.Context, store userstore.UserStore) (map[string]string, error) {
	profiles, err := store.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	profileNames := make(map[string]string, len(profiles))
	for _, profile := range profiles {
		profileNames[profile.ID] = strings.TrimSpace(profile.Name)
	}
	return profileNames, nil
}

func adminProfileExists(w http.ResponseWriter, r *http.Request, store userstore.UserStore, profileID string) bool {
	profile, err := store.GetProfile(r.Context(), profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load profile")
		return false
	}
	if profile == nil {
		writeError(w, http.StatusNotFound, "not_found", "Profile not found")
		return false
	}
	return true
}

func (h *AdminHandler) adminUserStore(w http.ResponseWriter, r *http.Request, userID int) (userstore.UserStore, bool) {
	if h.storeProv == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "User store not configured")
		return nil, false
	}
	store, err := h.storeProv.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return nil, false
	}
	if store == nil {
		writeError(w, http.StatusNotFound, "not_found", "User store not found")
		return nil, false
	}
	return store, true
}

func parseAdminUserIDParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := chi.URLParam(r, "id")
	userID, err := strconv.Atoi(raw)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid user id")
		return 0, false
	}
	return userID, true
}

// HandleGetSetting handles GET /admin/settings/{key}.
func (h *AdminHandler) HandleGetSetting(w http.ResponseWriter, r *http.Request) {
	if h.SettingsRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Settings store not configured")
		return
	}

	key := chi.URLParam(r, "key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}

	if sensitiveSettingKeys[key] {
		writeError(w, http.StatusNotFound, "not_found", "Setting not found")
		return
	}

	if value, ok := h.BootstrapSensitiveValues[key]; ok && value != "" {
		writeJSON(w, http.StatusOK, adminSettingResponse{Key: key, Value: value})
		return
	}

	value, err := h.SettingsRepo.Get(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load setting")
		return
	}
	if value == "" {
		writeError(w, http.StatusNotFound, "not_found", "Setting not found")
		return
	}

	writeJSON(w, http.StatusOK, adminSettingResponse{Key: key, Value: value})
}

type updateSettingRequest struct {
	Value string `json:"value"`
}

// HandleUpdateSetting handles PUT /admin/settings/{key}.
func (h *AdminHandler) HandleUpdateSetting(w http.ResponseWriter, r *http.Request) {
	if h.SettingsRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Settings store not configured")
		return
	}

	key := chi.URLParam(r, "key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Setting key is required")
		return
	}

	var req updateSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	switch key {
	case markers.SettingMode, markers.SettingLazyPlayback:
		if normalized, err := markers.NormalizeSetting(key, req.Value); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		} else {
			req.Value = normalized
		}
	case clientip.SettingTrustedProxies:
		normalized, err := clientip.NormalizeCIDRList(req.Value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request",
				"clientip.trusted_proxies must be a comma-separated list of CIDRs: "+err.Error())
			return
		}
		req.Value = normalized
	case "ai.asr_base_url":
		if llm.IsChatOnlyGateway(req.Value) {
			writeError(w, http.StatusBadRequest, "bad_request",
				"This endpoint cannot produce timestamped transcriptions (chat-only gateway). "+
					"Use a self-hosted Whisper server (faster-whisper/speaches), api.groq.com/openai, or api.openai.com.")
			return
		}
	case "metadata_ai.on_view":
		switch req.Value {
		case "off", "button", "auto":
		default:
			writeError(w, http.StatusBadRequest, "bad_request",
				"metadata_ai.on_view must be off, button, or auto")
			return
		}
	case policy.SettingDecisionLogVerbosity:
		switch strings.TrimSpace(strings.ToLower(req.Value)) {
		case policy.DecisionLogVerbosityDigest, policy.DecisionLogVerbosityVerbose:
			req.Value = strings.TrimSpace(strings.ToLower(req.Value))
		default:
			writeError(w, http.StatusBadRequest, "bad_request",
				"policy.decision_log_verbosity must be digest or verbose")
			return
		}
	case "policy.editor_enabled":
		enabled, err := strconv.ParseBool(strings.TrimSpace(req.Value))
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "policy.editor_enabled must be true or false")
			return
		}
		req.Value = strconv.FormatBool(enabled)
	case policy.SettingDecisionLogScopeSampleRate:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request",
				"policy.decision_log_scope_sample_rate must be an integer greater than 0")
			return
		}
		req.Value = strconv.Itoa(n)
	case policy.SettingDecisionLogRetentionDays:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request",
				"policy.decision_log_retention_days must be an integer greater than 0")
			return
		}
		req.Value = strconv.Itoa(n)
	case "subtitle_ai.transcribe_quota_jobs":
		if n, err := strconv.Atoi(req.Value); err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "bad_request",
				"subtitle_ai.transcribe_quota_jobs must be an integer >= 0 (0 = unlimited)")
			return
		}
	case "subtitle_ai.transcribe_quota_period":
		if !subtitleai.ValidQuotaPeriod(req.Value) {
			writeError(w, http.StatusBadRequest, "bad_request",
				"subtitle_ai.transcribe_quota_period must be day, week, or month")
			return
		}
	case notifications.SettingApplePushDeliveryEnabled:
		enabled, err := strconv.ParseBool(strings.TrimSpace(req.Value))
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "notifications.apple_push_delivery_enabled must be true or false")
			return
		}
		req.Value = strconv.FormatBool(enabled)
	case notifications.SettingPushRelayURL, notifications.SettingPushRelayDeploymentID, notifications.SettingPushRelayAPIKey:
		// The registration flow persists the relay URL, deployment id, and API
		// key together; a direct write to any of them desyncs the stored URL
		// from the credentials the relay minted for it (and feeds an arbitrary
		// id into the next rotation request).
		writeError(w, http.StatusBadRequest, "bad_request",
			key+" is managed by the push relay registration flow; use POST /admin/notifications/push/relay/register")
		return
	case catalog.SearchSettingProvider:
		switch strings.TrimSpace(strings.ToLower(req.Value)) {
		case catalog.SearchProviderPostgres, catalog.SearchProviderMeilisearch:
			req.Value = strings.TrimSpace(strings.ToLower(req.Value))
		default:
			writeError(w, http.StatusBadRequest, "bad_request", "catalog.search.provider must be postgres or meilisearch")
			return
		}
	case catalog.SearchSettingMeilisearchURL:
		value := strings.TrimSpace(req.Value)
		if value != "" {
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				writeError(w, http.StatusBadRequest, "bad_request", "catalog.search.meilisearch.url must include scheme and host")
				return
			}
		}
		req.Value = value
	case catalog.SearchSettingMeilisearchIndex:
		req.Value = strings.TrimSpace(req.Value)
		if req.Value == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "catalog.search.meilisearch.index is required")
			return
		}
	case catalog.SearchSettingMeilisearchTimeoutMS:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "catalog.search.meilisearch.timeout_ms must be an integer greater than 0")
			return
		}
		req.Value = strconv.Itoa(n)
	case catalog.SearchSettingMeilisearchMatchingStrategy:
		switch strings.TrimSpace(strings.ToLower(req.Value)) {
		case "last", "all":
			req.Value = strings.TrimSpace(strings.ToLower(req.Value))
		default:
			writeError(w, http.StatusBadRequest, "bad_request", "catalog.search.meilisearch.matching_strategy must be last or all")
			return
		}
	case catalog.SearchSettingMeilisearchSyncBatchSize:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n < 1 || n > catalog.MaxMeilisearchSyncBatchSize {
			writeError(w, http.StatusBadRequest, "bad_request", "catalog.search.meilisearch.sync_batch_size must be an integer between 1 and 10000")
			return
		}
		req.Value = strconv.Itoa(n)
	case catalog.SearchSettingMeilisearchRebuildBatchSize:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n < 1 || n > catalog.MaxMeilisearchRebuildBatchSize {
			writeError(w, http.StatusBadRequest, "bad_request", "catalog.search.meilisearch.rebuild_batch_size must be an integer between 1 and 25000")
			return
		}
		req.Value = strconv.Itoa(n)
	case catalog.SearchSettingMeilisearchRebuildQueue:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n < 1 || n > catalog.MaxMeilisearchRebuildQueueDepth {
			writeError(w, http.StatusBadRequest, "bad_request", "catalog.search.meilisearch.rebuild_task_queue_depth must be an integer between 1 and 16")
			return
		}
		req.Value = strconv.Itoa(n)
	case catalog.SearchSettingMeilisearchIndexTypes:
		itemTypes, err := catalog.NormalizeCatalogSearchIndexTypesValue(req.Value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		req.Value = catalog.FormatCatalogSearchIndexTypesValue(itemTypes)
	case catalog.SearchSettingMeilisearchSemanticEnabled:
		enabled, err := strconv.ParseBool(strings.TrimSpace(req.Value))
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "catalog.search.meilisearch.semantic_enabled must be true or false")
			return
		}
		req.Value = strconv.FormatBool(enabled)
	case catalog.SearchSettingMeilisearchSemanticRatio:
		ratio, err := strconv.ParseFloat(strings.TrimSpace(req.Value), 64)
		if err != nil || math.IsNaN(ratio) || ratio < 0 || ratio > 1 {
			writeError(w, http.StatusBadRequest, "bad_request", "catalog.search.meilisearch.semantic_ratio must be a number between 0 and 1")
			return
		}
		req.Value = strconv.FormatFloat(ratio, 'f', -1, 64)
	case catalog.SearchSettingMeilisearchEmbedder:
		embedder, err := catalog.NormalizeCatalogSearchEmbedderName(req.Value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		req.Value = embedder
	}

	if err := h.SettingsRepo.Set(r.Context(), key, req.Value); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update setting")
		return
	}

	if h.EventBus != nil {
		_ = h.EventBus.Publish(r.Context(), cache.ChannelAdmin,
			cache.Event{Type: cache.EventSettingsChanged, Payload: key})
	}
	if h.OnServerSettingUpdated != nil {
		h.OnServerSettingUpdated(r.Context(), key, req.Value)
	}
	restartRequired := config.RestartRequired(key)
	if restartRequired {
		h.markServerRestartRequired("server_settings")
	}
	if sensitiveSettingKeys[key] {
		writeJSON(w, http.StatusOK, adminSettingResponse{Key: key, RestartRequired: restartRequired})
		return
	}
	writeJSON(w, http.StatusOK, adminSettingResponse{Key: key, Value: req.Value, RestartRequired: restartRequired})
}
