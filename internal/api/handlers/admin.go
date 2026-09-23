package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
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
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/adminjob"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingsmigrate"
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
	// ListPage returns up to limit users with id above afterID, in id order.
	ListPage(ctx context.Context, afterID, limit int, identity string) ([]*models.User, error)
	Create(ctx context.Context, input models.CreateUserInput) (*models.User, error)
	Update(ctx context.Context, id int, input models.UpdateUserInput) error
	Delete(ctx context.Context, id int) error
	GetByID(ctx context.Context, id int) (*models.User, error)
}

type AccessGroupValidator interface {
	Get(ctx context.Context, id int64) (*access.Group, error)
	List(ctx context.Context) ([]access.Group, error)
	GetPolicyForUser(ctx context.Context, userID int) (*access.GroupPolicy, error)
}

// ServerSettingsStore provides access to server-wide admin settings.
type ServerSettingsStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string) error
	GetAll(ctx context.Context) (map[string]string, error)
}

type serverSettingsAtomicUpdater interface {
	UpdateAtomic(
		ctx context.Context,
		update func(current map[string]string) (map[string]string, error),
	) error
}

func updateServerSettingsAtomically(
	ctx context.Context,
	store ServerSettingsStore,
	update func(current map[string]string) (map[string]string, error),
) error {
	if updater, ok := store.(serverSettingsAtomicUpdater); ok {
		return updater.UpdateAtomic(ctx, update)
	}
	return errors.New("settings store does not support atomic updates")
}

type DiagnosticsEnablementStore interface {
	PutStream(ctx context.Context, bucket, key string, r io.Reader, contentType string) error
	DeleteObject(ctx context.Context, bucket, key string) error
	Bucket() string
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
	userRepo           UserRepository
	pool               *pgxpool.Pool
	SessionsLoader     *PlaybackSessionsLoader
	storeProv          userstore.UserStoreProvider
	accountProvisioner *auth.AccountProvisioner
	DetailSvc          *catalog.DetailService
	StatsSource        AdminStatsSource
	// WatchProviders lists the registered watch providers for the uncached
	// stats path. Nil on a deployment without the watchsync registry, which
	// degrades to "only providers with stored activity".
	WatchProviders               WatchProviderLister
	PlaybackActivitySource       AdminPlaybackActivitySource
	TopActivitySource            AdminTopActivitySource
	TimeseriesSource             AdminTimeseriesSource
	DownloadsStatsSource         AdminDownloadsStatsSource
	RedisClient                  *redis.Client // health reporting only; nil means this deployment runs without Redis
	Config                       *config.Config
	ArtworkBackend               string // resolved artwork backend for the settings status; empty when unopened
	EventBus                     cache.EventBus
	EventsHub                    *evt.Hub
	SettingsRepo                 ServerSettingsStore
	DiagnosticsStore             DiagnosticsEnablementStore
	JobRepo                      AdminJobCreator
	ItemRefreshResolver          ItemRefreshScopeResolver
	ImpersonationService         ImpersonationService
	RealtimeHub                  *notifications.Hub
	AccessGroups                 AccessGroupValidator
	BootstrapSensitiveConfigured map[string]bool
	BootstrapSensitiveValues     map[string]string
	RedisBootstrapAvailable      bool
	OnUserSessionsRevoked        func(ctx context.Context, userID int)
	OnServerSettingUpdated       func(ctx context.Context, key, value string)
	RestartStatus                *ServerRestartStatusTracker
	CatalogSearchStatus          catalog.CatalogSearchStatusProvider
	// logLevelCounts caches the 24h error/warning tallies served on
	// /admin/server/status. The dashboard polls that route every 15s, and the
	// counts are only ever read as a rough signal, so re-counting per request
	// would be pure waste. Nil when the handler was built without the
	// constructor (tests): the counts are then simply uncached.
	logLevelCounts *cache.TTLCache[adminLogLevelCounts]
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
		logLevelCounts:     cache.NewTTLCache[adminLogLevelCounts](),
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
	MaxPlaybackQuality       *string                `json:"max_playback_quality,omitempty"`
	MaxStreams               *int                   `json:"max_streams,omitempty"`
	MaxTranscodes            *int                   `json:"max_transcodes,omitempty"`
	TranscodeAllowed         *bool                  `json:"transcode_allowed,omitempty"`
	AudioTranscodeAllowed    *bool                  `json:"audio_transcode_allowed,omitempty"`
	MaxProfiles              *int                   `json:"max_profiles,omitempty"`
	DownloadAllowed          *bool                  `json:"download_allowed,omitempty"`
	DownloadTranscodeAllowed *bool                  `json:"download_transcode_allowed,omitempty"`
	RequestsAllowed          *bool                  `json:"requests_allowed,omitempty"`
	AccessGroupID            *int64                 `json:"access_group_id,omitempty"`
}

// optionalField is a tri-state JSON field for nullable policy columns: absent
// leaves the column alone, explicit null clears it back to "inherit from the
// access group", and a value stores an explicit override.
type optionalField[T any] struct {
	Set   bool
	Value *T
}

func (f *optionalField[T]) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Value = nil
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.Value = &value
	return nil
}

func (f optionalField[T]) Optional() models.Optional[T] {
	return models.Optional[T]{Set: f.Set, Value: f.Value}
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
	LibraryIDs               optionalField[[]int]   `json:"library_ids,omitempty"`
	MaxPlaybackQuality       optionalField[string]  `json:"max_playback_quality,omitempty"`
	MaxStreams               optionalField[int]     `json:"max_streams,omitempty"`
	MaxTranscodes            optionalField[int]     `json:"max_transcodes,omitempty"`
	TranscodeAllowed         optionalField[bool]    `json:"transcode_allowed,omitempty"`
	AudioTranscodeAllowed    optionalField[bool]    `json:"audio_transcode_allowed,omitempty"`
	MaxProfiles              *int                   `json:"max_profiles,omitempty"`
	DownloadAllowed          optionalField[bool]    `json:"download_allowed,omitempty"`
	DownloadTranscodeAllowed optionalField[bool]    `json:"download_transcode_allowed,omitempty"`
	RequestsAllowed          optionalField[bool]    `json:"requests_allowed,omitempty"`
	AccessGroupID            optionalField[int64]   `json:"access_group_id,omitempty"`
}

// libraryIDsOptional maps library_ids to the repository tri-state: null (or
// absent) clears the override so the account inherits the group's libraries;
// an array — including an empty one — is an explicit override. The slice is
// copied so the stored override never aliases the decoded request body.
func (r *updateUserRequest) libraryIDsOptional() models.Optional[[]int] {
	optional := r.LibraryIDs.Optional()
	if optional.Value != nil {
		value := append([]int{}, *optional.Value...)
		optional.Value = &value
	}
	return optional
}

// AdminUserView represents a user in admin JSON responses.
//
// The policy fields carry the account's stored overrides: null means the
// field is inherited from the access group. EffectivePolicy is the resolved
// value the server enforces (override when set, otherwise the group's value,
// otherwise the permissive no-group default).
type AdminUserView struct {
	ID                         int                 `json:"id"`
	Username                   string              `json:"username"`
	Email                      string              `json:"email"`
	Role                       string              `json:"role"`
	Permissions                []string            `json:"permissions"`
	Enabled                    bool                `json:"enabled"`
	LibraryIDs                 []int               `json:"library_ids"`
	MaxPlaybackQuality         *string             `json:"max_playback_quality"`
	MaxStreams                 *int                `json:"max_streams"`
	MaxTranscodes              *int                `json:"max_transcodes"`
	MaxRemoteStreamBitrateKbps *int                `json:"-"`
	MaxLocalStreamBitrateKbps  *int                `json:"-"`
	TranscodeAllowed           *bool               `json:"transcode_allowed"`
	AudioTranscodeAllowed      *bool               `json:"audio_transcode_allowed"`
	MaxProfiles                int                 `json:"max_profiles"`
	DownloadAllowed            *bool               `json:"download_allowed"`
	DownloadTranscodeAllowed   *bool               `json:"download_transcode_allowed"`
	RequestsAllowed            *bool               `json:"requests_allowed"`
	AccessGroupID              *int64              `json:"access_group_id"`
	EffectivePolicy            EffectivePolicyView `json:"effective_policy"`
	CreatedAt                  time.Time           `json:"created_at"`
	UpdatedAt                  time.Time           `json:"updated_at"`
	LastActiveAt               *time.Time          `json:"last_active_at,omitempty"`
}

// EffectivePolicyView is the resolved policy block on admin user responses.
type EffectivePolicyView struct {
	LibraryIDs                 []int    `json:"library_ids"`
	MaxPlaybackQuality         string   `json:"max_playback_quality"`
	MaxStreams                 int      `json:"max_streams"`
	MaxTranscodes              int      `json:"max_transcodes"`
	MaxRemoteStreamBitrateKbps int      `json:"-"`
	MaxLocalStreamBitrateKbps  int      `json:"-"`
	TranscodeAllowed           bool     `json:"transcode_allowed"`
	AudioTranscodeAllowed      bool     `json:"audio_transcode_allowed"`
	DownloadAllowed            bool     `json:"download_allowed"`
	DownloadTranscodeAllowed   bool     `json:"download_transcode_allowed"`
	RequestsAllowed            bool     `json:"requests_allowed"`
	Permissions                []string `json:"permissions"`
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

// toAdminUserResponse converts a User model to an admin API response. group
// is the user's access-group policy (nil when ungrouped or unknown).
func toAdminUserResponse(u *models.User, group *access.GroupPolicy) AdminUserView {
	effective := access.ApplyGroupPolicy(u, group)
	resp := AdminUserView{
		ID:                         u.ID,
		Username:                   u.Username,
		Email:                      u.Email,
		Role:                       u.Role,
		Permissions:                append([]string{}, u.Permissions...),
		Enabled:                    u.Enabled,
		LibraryIDs:                 cloneIntSlice(u.LibraryIDs),
		MaxPlaybackQuality:         normalizedQualityPtr(u.MaxPlaybackQuality),
		MaxStreams:                 clonePtr(u.MaxStreams),
		MaxTranscodes:              clonePtr(u.MaxTranscodes),
		MaxRemoteStreamBitrateKbps: clonePtr(u.MaxRemoteStreamBitrateKbps),
		MaxLocalStreamBitrateKbps:  clonePtr(u.MaxLocalStreamBitrateKbps),
		TranscodeAllowed:           clonePtr(u.TranscodeAllowed),
		AudioTranscodeAllowed:      clonePtr(u.AudioTranscodeAllowed),
		MaxProfiles:                u.MaxProfiles,
		DownloadAllowed:            clonePtr(u.DownloadAllowed),
		DownloadTranscodeAllowed:   clonePtr(u.DownloadTranscodeAllowed),
		RequestsAllowed:            clonePtr(u.RequestsAllowed),
		AccessGroupID:              clonePtr(u.AccessGroupID),
		EffectivePolicy: EffectivePolicyView{
			LibraryIDs:                 effective.LibraryIDs,
			MaxPlaybackQuality:         effective.MaxPlaybackQuality,
			MaxStreams:                 effective.MaxStreams,
			MaxTranscodes:              effective.MaxTranscodes,
			MaxRemoteStreamBitrateKbps: effective.MaxRemoteStreamBitrateKbps,
			MaxLocalStreamBitrateKbps:  effective.MaxLocalStreamBitrateKbps,
			TranscodeAllowed:           effective.TranscodeAllowed,
			AudioTranscodeAllowed:      effective.AudioTranscodeAllowed,
			DownloadAllowed:            effective.DownloadAllowed,
			DownloadTranscodeAllowed:   effective.DownloadTranscodeAllowed,
			RequestsAllowed:            effective.RequestsAllowed,
			Permissions:                append([]string{}, effective.Permissions...),
		},
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
	return resp
}

func normalizedQualityPtr(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := access.NormalizePlaybackQuality(*value)
	return &normalized
}

// cloneIntSlice preserves the nil/empty distinction: nil stays nil (JSON
// null = inherit) and an empty override stays an empty array.
func cloneIntSlice(values []int) []int {
	if values == nil {
		return nil
	}
	out := make([]int, len(values))
	copy(out, values)
	return out
}

// roleAdmin is the server-wide admin account role.
const roleAdmin = models.RoleAdmin

// validateStreamLimits rejects negative concurrency caps. nil means "inherit
// from the access group" and 0 means an explicit "unlimited" override, so only
// a negative value is meaningless.
func validateStreamLimits(maxStreams, maxTranscodes *int) error {
	if (maxStreams != nil && *maxStreams < 0) || (maxTranscodes != nil && *maxTranscodes < 0) {
		return errors.New("max_streams and max_transcodes must be 0 (unlimited) or positive")
	}
	return nil
}

// actorIsScopedAPIKey reports whether the request is authenticated by an API
// key that carries scopes. Unscoped keys and JWT sessions are not constrained:
// an unscoped key already acts with its owner's full authority, and a JWT
// actor is the admin themselves.
func actorIsScopedAPIKey(ctx context.Context) bool {
	claims := apimw.GetClaims(ctx)
	return claims != nil && len(claims.APIKeyScopes) > 0
}

// rejectScopedAPIKeyCreate stops a scoped API key from minting an admin
// account, which it could then log into for an unscoped session. Provisioning
// ordinary accounts — password included — stays in scope. It reports whether
// it wrote a response.
func rejectScopedAPIKeyCreate(w http.ResponseWriter, r *http.Request, role string) bool {
	if !actorIsScopedAPIKey(r.Context()) || role != roleAdmin {
		return false
	}
	writeError(w, http.StatusForbidden, "insufficient_scope",
		"A scoped API key may not create an admin account")
	return true
}

// rejectScopedAPIKeyUpdate stops a scoped API key from escalating through an
// existing account: it may neither grant the admin role nor touch the
// credentials or role of an account that is already an admin — either would
// hand it an unscoped admin session. Editing an ordinary account, password
// included, stays in scope.
//
// It returns the target account when it had to load one, so the caller can
// reuse it as the pre-update snapshot instead of reading the row twice, and
// reports whether it wrote a response.
func (h *AdminHandler) rejectScopedAPIKeyUpdate(
	w http.ResponseWriter,
	r *http.Request,
	id int,
	req *updateUserRequest,
) (*models.User, bool) {
	if !actorIsScopedAPIKey(r.Context()) {
		return nil, false
	}
	if req.Role != nil && *req.Role == roleAdmin {
		writeError(w, http.StatusForbidden, "insufficient_scope",
			"A scoped API key may not grant the admin role")
		return nil, true
	}
	if req.Password == nil && req.Role == nil {
		return nil, false
	}

	target, blocked := h.loadTargetUser(w, r, id)
	if blocked {
		return nil, true
	}
	if target.Role == roleAdmin {
		writeError(w, http.StatusForbidden, "insufficient_scope",
			"A scoped API key may not change the password or role of an admin account")
		return nil, true
	}
	return target, false
}

// loadTargetUser reads the account an admin write targets, writing 404/500 on
// failure. It reports whether it wrote a response.
func (h *AdminHandler) loadTargetUser(w http.ResponseWriter, r *http.Request, id int) (*models.User, bool) {
	target, err := h.userRepo.GetByID(r.Context(), id)
	if err != nil {
		if auth.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "User not found")
			return nil, true
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to fetch user")
		return nil, true
	}
	return target, false
}

// rejectGroupedAdmin refuses an update that would leave an admin account in
// an access group: a group named in the request together with the admin role,
// or for an account that already holds it. Writes that only change the role
// are not its concern — the repository clears the group on promote and falls
// back to the default group on demote. It loads the target when the role is
// not in the request and returns it for reuse as the pre-update snapshot.
func (h *AdminHandler) rejectGroupedAdmin(
	w http.ResponseWriter,
	r *http.Request,
	id int,
	req *updateUserRequest,
	current *models.User,
) (*models.User, bool) {
	if !req.AccessGroupID.Set || req.AccessGroupID.Value == nil {
		return current, false
	}
	var role string
	switch {
	case req.Role != nil:
		role = *req.Role
	case current != nil:
		role = current.Role
	default:
		var blocked bool
		if current, blocked = h.loadTargetUser(w, r, id); blocked {
			return nil, true
		}
		role = current.Role
	}
	if role == roleAdmin {
		writeError(w, http.StatusUnprocessableEntity, "unprocessable_entity",
			"Admin accounts cannot belong to an access group")
		return current, true
	}
	return current, false
}

// clonePtr copies a policy override pointer so a response never aliases the
// stored model. A nil pointer stays nil (JSON null = inherit).
func clonePtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

// groupPolicyProvider exposes the access-group store as a policy provider,
// preserving a nil interface when access groups are not configured.
func (h *AdminHandler) groupPolicyProvider() access.GroupPolicyProvider {
	if h == nil || h.AccessGroups == nil {
		return nil
	}
	return h.AccessGroups
}

// groupPolicies loads every access group's policy keyed by ID so a list of
// users can be resolved without a query per user. A lookup failure is an
// error, not an empty map: rendering a group-restricted user against
// NoGroupPolicy would report a fully permissive effective_policy.
func (h *AdminHandler) groupPolicies(ctx context.Context) (map[int64]access.GroupPolicy, error) {
	policies := map[int64]access.GroupPolicy{}
	if h == nil || h.AccessGroups == nil {
		return policies, nil
	}
	groups, err := h.AccessGroups.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading access groups for effective policy: %w", err)
	}
	for _, group := range groups {
		policies[group.ID] = group.Policy()
	}
	return policies, nil
}

// groupPolicyFor returns the user's group policy, or nil when the user is
// ungrouped (or the group row is gone — the FK clears membership on delete,
// so a residual not-found is treated as ungrouped, not an error).
func (h *AdminHandler) groupPolicyFor(ctx context.Context, u *models.User) (*access.GroupPolicy, error) {
	if !access.GroupApplies(u) || h == nil || h.AccessGroups == nil {
		return nil, nil
	}
	group, err := h.AccessGroups.Get(ctx, *u.AccessGroupID)
	if err != nil {
		if errors.Is(err, access.ErrGroupNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("loading access group for effective policy: %w", err)
	}
	policy := group.Policy()
	return &policy, nil
}

func lookupGroupPolicy(policies map[int64]access.GroupPolicy, u *models.User) *access.GroupPolicy {
	if !access.GroupApplies(u) {
		return nil
	}
	policy, ok := policies[*u.AccessGroupID]
	if !ok {
		return nil
	}
	return &policy
}

func (h *AdminHandler) loadUserLastActiveAt(ctx context.Context, userIDs []int) (map[int]time.Time, error) {
	lastActive := make(map[int]time.Time)
	if h == nil || h.pool == nil || len(userIDs) == 0 {
		return lastActive, nil
	}

	rows, err := h.pool.Query(ctx, `
		SELECT user_id, MAX("timestamp") AS last_active_at
		FROM activity_log
		WHERE user_id = ANY($1::bigint[])
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

func applyLastActiveAt(resp *AdminUserView, lastActive map[int]time.Time) {
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
	resp, err := h.ListAdminUsers(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// ListAdminUsers builds the administrator's view of every account. v1 GET
// /admin/users calls it; a failure is an *APIError.
func (h *AdminHandler) ListAdminUsers(ctx context.Context) ([]AdminUserView, error) {
	users, err := h.userRepo.List(ctx)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list users")
	}
	return h.adminUserViews(ctx, users)
}

// ListAdminUsersPage is the keyset page v2 listAdminUsers uses: up to limit
// accounts with id above afterID, in id order, enriched the same way as
// ListAdminUsers, plus whether more accounts follow.
func (h *AdminHandler) ListAdminUsersPage(ctx context.Context, afterID, limit int, identity string) ([]AdminUserView, bool, error) {
	users, err := h.userRepo.ListPage(ctx, afterID, limit+1, identity)
	if err != nil {
		return nil, false, apiError(http.StatusInternalServerError, "internal_error", "Failed to list users")
	}
	hasMore := len(users) > limit
	if hasMore {
		users = users[:limit]
	}
	views, err := h.adminUserViews(ctx, users)
	if err != nil {
		return nil, false, err
	}
	return views, hasMore, nil
}

// adminUserViews resolves the effective policy and last activity for a batch
// of accounts, in the batch's order.
func (h *AdminHandler) adminUserViews(ctx context.Context, users []*models.User) ([]AdminUserView, error) {
	policies, err := h.groupPolicies(ctx)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to resolve effective policy")
	}
	resp := make([]AdminUserView, 0, len(users))
	userIDs := make([]int, 0, len(users))
	for _, u := range users {
		userIDs = append(userIDs, u.ID)
		resp = append(resp, toAdminUserResponse(u, lookupGroupPolicy(policies, u)))
	}
	lastActive, err := h.loadUserLastActiveAt(ctx, userIDs)
	if err != nil {
		slog.WarnContext(ctx, "failed to load admin user last activity", "component", "api", "error", err)
	}
	for i := range resp {
		applyLastActiveAt(&resp[i], lastActive)
	}
	return resp, nil
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

	groupPolicy, err := h.groupPolicyFor(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve effective policy")
		return
	}
	resp := toAdminUserResponse(user, groupPolicy)
	lastActive, err := h.loadUserLastActiveAt(r.Context(), []int{user.ID})
	if err != nil {
		slog.WarnContext(r.Context(), "failed to load admin user last activity", "component", "api", "user_id", user.ID, "error", err)
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

	if rejectScopedAPIKeyCreate(w, r, req.Role) {
		return
	}

	if req.Username == "" || req.Email == "" || req.Password == "" || req.Role == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Username, email, password, and role are required")
		return
	}
	if req.Role == roleAdmin && req.AccessGroupID != nil {
		writeError(w, http.StatusUnprocessableEntity, "unprocessable_entity",
			"Admin accounts cannot belong to an access group")
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
	if err := validateStreamLimits(req.MaxStreams, req.MaxTranscodes); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
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
	if req.AccessGroupID != nil {
		if h.AccessGroups == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Access groups are not configured")
			return
		}
		if _, err := h.AccessGroups.Get(r.Context(), *req.AccessGroupID); err != nil {
			if errors.Is(err, access.ErrGroupNotFound) {
				writeError(w, http.StatusUnprocessableEntity, "unprocessable_entity", "Invalid access_group_id")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to validate access group")
			return
		}
	}

	user, err := h.accountProvisioner.CreateAccount(r.Context(), auth.CreateAccountInput{
		User: models.CreateUserInput{
			Username:                 req.Username,
			Email:                    req.Email,
			Password:                 req.Password,
			Role:                     req.Role,
			Permissions:              permissions,
			AccessGroupID:            req.AccessGroupID,
			LibraryIDs:               req.LibraryIDs,
			MaxPlaybackQuality:       maxPlaybackQuality,
			MaxStreams:               req.MaxStreams,
			MaxTranscodes:            req.MaxTranscodes,
			TranscodeAllowed:         req.TranscodeAllowed,
			AudioTranscodeAllowed:    req.AudioTranscodeAllowed,
			MaxProfiles:              req.MaxProfiles,
			DownloadAllowed:          req.DownloadAllowed,
			DownloadTranscodeAllowed: req.DownloadTranscodeAllowed,
			RequestsAllowed:          req.RequestsAllowed,
		},
		DefaultProfile: auth.DefaultProfileOptions{
			Enabled: req.CreateDefaultProfile,
			Name:    req.DefaultProfileName,
		},
	})
	if err != nil {
		if auth.IsDuplicate(err) {
			writeError(w, http.StatusConflict, "duplicate", "A user with that username or email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create user")
		return
	}
	h.invalidateStats(r.Context(), cache.ChannelAdmin, cache.EventAdminStatsInvalidated, strconv.Itoa(user.ID))

	createdGroupPolicy, err := h.groupPolicyFor(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve effective policy")
		return
	}
	writeJSON(w, http.StatusCreated, toAdminUserResponse(user, createdGroupPolicy))
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

	currentUser, blocked := h.rejectScopedAPIKeyUpdate(w, r, id, &req)
	if blocked {
		return
	}

	maxPlaybackQuality := req.MaxPlaybackQuality.Optional()
	if maxPlaybackQuality.Value != nil {
		normalized, ok := access.ParsePlaybackQualityPreset(*maxPlaybackQuality.Value)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid max_playback_quality")
			return
		}
		maxPlaybackQuality.Value = &normalized
	}
	if req.MaxProfiles != nil && *req.MaxProfiles < 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "max_profiles must be at least 1")
		return
	}
	if err := validateStreamLimits(req.MaxStreams.Value, req.MaxTranscodes.Value); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.AccessGroupID.Set {
		if req.AccessGroupID.Value != nil && *req.AccessGroupID.Value <= 0 {
			writeError(w, http.StatusUnprocessableEntity, "unprocessable_entity", "Invalid access_group_id")
			return
		}
		currentUser, blocked = h.rejectGroupedAdmin(w, r, id, &req, currentUser)
		if blocked {
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
		LibraryIDs:               req.libraryIDsOptional(),
		MaxPlaybackQuality:       maxPlaybackQuality,
		MaxStreams:               req.MaxStreams.Optional(),
		MaxTranscodes:            req.MaxTranscodes.Optional(),
		TranscodeAllowed:         req.TranscodeAllowed.Optional(),
		AudioTranscodeAllowed:    req.AudioTranscodeAllowed.Optional(),
		MaxProfiles:              req.MaxProfiles,
		DownloadAllowed:          req.DownloadAllowed.Optional(),
		DownloadTranscodeAllowed: req.DownloadTranscodeAllowed.Optional(),
		RequestsAllowed:          req.RequestsAllowed.Optional(),
		AccessGroupID:            req.AccessGroupID.Optional(),
	}

	if currentUser == nil && updateMayRequireSessionRevocation(updateInput) {
		if currentUser, blocked = h.loadTargetUser(w, r, id); blocked {
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

	updatedGroupPolicy, err := h.groupPolicyFor(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to resolve effective policy")
		return
	}
	writeJSON(w, http.StatusOK, toAdminUserResponse(user, updatedGroupPolicy))
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
		if auth.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "User not found")
			return
		}
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

	writeJSON(w, http.StatusOK, buildLoginResponse(pair, effectiveUser, effectiveDownloadAllowed(r.Context(), effectiveUser, h.groupPolicyProvider()), impersonator))
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
	return loader.Load(ctx, PlaybackSessionsQuery{})
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
		FROM admin_playback_history h
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
		input.MaxPlaybackQuality.Set ||
		input.AccessGroupID.Set
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
	if input.MaxPlaybackQuality.Set && !qualityOverrideEqual(input.MaxPlaybackQuality.Value, current.MaxPlaybackQuality) {
		return true
	}
	if input.AccessGroupID.Set && !accessGroupIDEqual(input.AccessGroupID.Value, current.AccessGroupID) {
		return true
	}
	return false
}

func qualityOverrideEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return access.NormalizePlaybackQuality(*a) == access.NormalizePlaybackQuality(*b)
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

	files, err := h.listUnmatchedFiles(r.Context(), limit, offset, nil)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

type AdminUnmatchedFileView = unmatchedFileRow

// ListAdminUnmatchedFiles returns a live ID-ordered SQL page, excluding extras.
func (h *AdminHandler) ListAdminUnmatchedFiles(ctx context.Context, limit, after int) ([]AdminUnmatchedFileView, bool, error) {
	if h == nil || h.pool == nil {
		return nil, false, apiError(http.StatusServiceUnavailable, "unavailable", "Database not configured")
	}
	if limit < 1 || limit > 200 || after < 0 {
		return nil, false, apiError(http.StatusBadRequest, "bad_request", "Invalid unmatched file page")
	}
	rows, err := h.listUnmatchedFiles(ctx, limit+1, 0, new(after))
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	return rows[:min(len(rows), limit)], more, nil
}

func (h *AdminHandler) listUnmatchedFiles(ctx context.Context, limit, offset int, after *int) ([]unmatchedFileRow, error) {
	rows, err := h.pool.Query(ctx,
		`SELECT id, media_folder_id, file_path, file_size, container
		 FROM media_files
		 WHERE content_id IS NULL AND extra_id IS NULL AND ($3::bigint IS NULL OR id > $3)
		 ORDER BY id ASC
		 LIMIT $1 OFFSET $2`, limit, offset, after)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list unmatched files")
	}
	defer rows.Close()

	files := make([]unmatchedFileRow, 0)
	for rows.Next() {
		var f unmatchedFileRow
		if err := rows.Scan(&f.ID, &f.MediaFolderID, &f.FilePath, &f.FileSize, &f.Container); err != nil {
			return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to scan file")
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to iterate files")
	}

	return files, nil
}

var (
	errAdminStatsRead       = errors.New("failed to get stats")
	errAdminStatsCountUsers = errors.New("failed to count users")
)

// HandleGetStats handles GET /admin/stats.
// Returns system statistics for the admin dashboard.
func (h *AdminHandler) HandleGetStats(w http.ResponseWriter, r *http.Request) {
	resp, err := h.ReadAdminStats(r.Context(), isTruthyQuery(r.URL.Query().Get("refresh")))
	if err != nil {
		message := "Failed to get stats"
		if errors.Is(err, errAdminStatsCountUsers) {
			message = "Failed to count users"
		}
		writeError(w, http.StatusInternalServerError, "internal_error", message)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ReadAdminStats shares the existing cache, PostgreSQL and account-repository fallback.
func (h *AdminHandler) ReadAdminStats(ctx context.Context, refresh bool) (AdminStats, error) {
	if h.StatsSource != nil {
		if refresh {
			h.StatsSource.Invalidate()
		}
		stats, err := h.StatsSource.Get(ctx)
		if err != nil {
			return AdminStats{}, errAdminStatsRead
		}
		return stats, nil
	}
	if h.pool != nil {
		stats, err := queryAdminStats(ctx, h.pool, h.WatchProviders)
		if err != nil {
			return AdminStats{}, errAdminStatsRead
		}
		return stats, nil
	}
	if h.userRepo == nil {
		return AdminStats{}, errAdminStatsCountUsers
	}
	users, err := h.userRepo.List(ctx)
	if err != nil {
		return AdminStats{}, errAdminStatsCountUsers
	}
	return AdminStats{TotalUsers: len(users)}, nil
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
		slog.WarnContext(ctx, "admin: failed to publish stats invalidation event", "component", "api",
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

	job, err := h.CreateItemMetadataRefresh(r.Context(), contentID, mode, currentAdminUserID(r))
	if err != nil {
		writeAPIError(w, err)
		return
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
	detail, err := h.UpdateCatalogItemMetadata(r.Context(), contentID, req)
	if err != nil {
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, detail)
}

// --- Server Settings endpoints ---

// sensitiveSettingKeys is the audited allowlist of secret-bearing settings
// keys, shared with the at-rest encryption decorator so redaction and
// encryption can never drift apart. See catalog.SensitiveSettingKeys.
var sensitiveSettingKeys = catalog.SensitiveSettingKeys

// machineManagedSettingKeys contains durable internal state that shares the
// server_settings store but is not part of the administrator settings API.
var machineManagedSettingKeys = map[string]bool{
	config.ArtworkStorageReconcileCheckpointKey: true,
	config.ArtworkStorageSweepCheckpointKey:     true,
	blobstore.IdentitySettingKey:                true,
}

// Setting keys that decide where artwork lives. The s3 keys are the canonical
// names; the effective settings map already applies the operational aliases.
const (
	artworkStorageBackendKey = "artwork.storage_backend"
	artworkLocalPathKey      = "artwork.local_path"
	s3PublicEndpointKey      = "s3.public_endpoint"
	s3PublicBucketKey        = "s3.public_bucket"
	s3PublicKeyPrefixKey     = "s3.public_key_prefix"
	s3OperationalBucketKey   = "s3.operational_bucket"
)

// artworkStorageLocked reports whether the artwork storage location can still
// change. The first artwork write records the store identity; after that the
// catalog's keys live in exactly one place and there is no migrator, so every
// setting that selects that place is read-only.
func artworkStorageLocked(stored map[string]string) bool {
	return strings.TrimSpace(stored[blobstore.IdentitySettingKey]) != ""
}

var errArtworkStorageLocked = &APIError{
	Status:  http.StatusConflict,
	Code:    "artwork_storage_locked",
	Message: "artwork storage cannot change once artwork has been stored; the catalog's artwork keys belong to the recorded storage",
}

// artworkIdentityInputs names the effective settings that decide where
// artwork lives: the resolved backend and, for that backend, the fields the
// store folds into its identity. The s3 keys are the canonical names; the
// effective map already applies the legacy operational aliases.
func artworkIdentityInputs(effective map[string]string) (backend string, inputs map[string]string) {
	backend = strings.ToLower(strings.TrimSpace(effective[artworkStorageBackendKey]))
	if backend == "" || backend == config.ArtworkBackendAuto {
		backend = blobstore.BackendLocal
		if strings.TrimSpace(effective[s3PublicBucketKey]) != "" {
			backend = blobstore.BackendS3
		}
	}
	inputs = map[string]string{}
	switch backend {
	case blobstore.BackendS3:
		for _, key := range []string{s3PublicEndpointKey, s3PublicBucketKey, s3PublicKeyPrefixKey} {
			inputs[key] = strings.TrimSpace(effective[key])
		}
	default:
		inputs[artworkLocalPathKey] = strings.TrimSpace(effective[artworkLocalPathKey])
	}
	return backend, inputs
}

// rejectArtworkIdentityChange refuses a write that would move artwork storage
// after it has been recorded: a backend other than the recorded one, or a
// different value for any setting that backend's identity is built from.
// Writes that leave the location as it is, including re-saving the same
// values or adding a bucket an explicit local backend ignores, are allowed so
// a settings form that includes the keys can still submit.
func rejectArtworkIdentityChange(recorded string, before, after map[string]string) error {
	recordedBackend, _, _ := strings.Cut(strings.TrimSpace(recorded), "|")
	_, beforeInputs := artworkIdentityInputs(before)
	afterBackend, afterInputs := artworkIdentityInputs(after)
	if afterBackend != recordedBackend {
		return errArtworkStorageLocked
	}
	for key, value := range beforeInputs {
		if afterInputs[key] != value {
			return errArtworkStorageLocked
		}
	}
	return nil
}

func redactAdminSettings(values map[string]string) {
	for key := range sensitiveSettingKeys {
		delete(values, key)
	}
	for key := range machineManagedSettingKeys {
		delete(values, key)
	}
}

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
	redactAdminSettings(all)
	writeJSON(w, http.StatusOK, all)
}

// HandleGetEffectiveSettings handles GET /admin/settings/effective. Unlike the
// legacy raw endpoint, missing rows are populated with the exact defaults used
// by runtime readers so an untouched form always describes active behavior.
func (h *AdminHandler) HandleGetEffectiveSettings(w http.ResponseWriter, r *http.Request) {
	if h.SettingsRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Settings store not configured")
		return
	}
	all, err := h.SettingsRepo.GetAll(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load settings")
		return
	}
	effective := h.effectiveAdminSettings(all)
	redactAdminSettings(effective)
	writeJSON(w, http.StatusOK, effective)
}

type restartKeysResponse struct {
	Keys     []string `json:"keys"`
	Prefixes []string `json:"prefixes"`
}

// HandleGetRestartKeys handles GET /admin/settings/restart-keys. The registry
// is compiled into the binary (internal/config), so the response only changes
// across deploys; the admin UI caches it and uses it to badge the fields whose
// saved value waits on a restart.
func (h *AdminHandler) HandleGetRestartKeys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, restartKeysResponse{
		Keys:     config.RestartRequiredKeys(),
		Prefixes: config.RestartRequiredPrefixes(),
	})
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
	status := h.adminSensitiveSettingsStatus(all)
	writeJSON(w, http.StatusOK, sensitiveStatusResponse{
		Configured:   status.Configured,
		ManagedByEnv: status.ManagedByEnv,
	})
}

type adminSettingResponse = AdminSettingValue

type AdminSettingValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// RestartRequired reports whether the value only takes effect after a
	// server restart. It is populated from the compiled restart-key registry
	// on read responses as well as on updates, so the admin UI never has to
	// hand-copy the list into hint text.
	RestartRequired bool `json:"restart_required,omitempty"`
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

type adminDeviceSummaryResponse = AdminDeviceSummaryView

type AdminDeviceSummaryView struct {
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

type adminDeviceDetailResponse = AdminDeviceDetailView

type AdminDeviceDetailView struct {
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

// HandleListDevices preserves the frozen administrator device list.
func (h *AdminHandler) HandleListDevices(w http.ResponseWriter, r *http.Request) {
	views, err := h.ReadAdminDevices(r.Context())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, adminDevicesListResponse{Devices: views})
}

// HandleGetDevice preserves the bridge's legacy settings array.
func (h *AdminHandler) HandleGetDevice(w http.ResponseWriter, r *http.Request) {
	view, err := h.ReadAdminDevice(r.Context(), chi.URLParam(r, "user_id"), chi.URLParam(r, "device_id"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func listRegisteredDevices(ctx context.Context, store userstore.UserStore) ([]userstore.DeviceEntry, error) {
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		return nil, nil
	}
	return registry.ListDevices(ctx)
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
	canonicalValues []userstore.SettingValue,
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
		key := canonicalAdminDeviceSettingKey(entry.Key)
		if profileID != "" && key != "" {
			current.keys[profileID+":"+key] = struct{}{}
		}
		profile := ensureProfile(current, profileID, entry.UpdatedAt)
		if profile != nil && key != "" {
			profile.keys[key] = struct{}{}
		}
	}

	// Canonical profile_device rows are the authoritative overrides after the
	// settings cutover. Merge them by (profile,key) with the still-mounted
	// legacy rows so a mirrored value counts once while a canonical-only write
	// remains visible to fleet management.
	for _, value := range canonicalValues {
		if value.Scope != settingscontract.ScopeProfileDevice {
			continue
		}
		deviceID := strings.TrimSpace(value.DeviceID)
		profileID := strings.TrimSpace(value.ProfileID)
		current := ensureDevice(deviceID, "", "", value.UpdatedAt)
		if current == nil {
			continue
		}
		key := canonicalAdminDeviceSettingKey(value.Key)
		if profileID != "" && key != "" {
			current.keys[profileID+":"+key] = struct{}{}
		}
		profile := ensureProfile(current, profileID, value.UpdatedAt)
		if profile != nil && key != "" {
			profile.keys[key] = struct{}{}
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

// canonicalAdminDeviceSettingKey reduces a stored key to the preference it
// expresses, so fleet counts describe overrides rather than rows.
//
// Two reductions, because a key can be spelled twice for two different reasons.
// The migration's rename table folds a pre-cutover spelling onto its contract
// name, which is what makes an appearance row left in the legacy table count
// once. The mirror then folds a deprecated key onto its replacement, which is
// what keeps a household's single intro-skip choice from raising this device's
// override count — and the anomaly thresholds and count filters built on it —
// by two for the length of the overlap window.
func canonicalAdminDeviceSettingKey(key string) string {
	return settingscontract.LogicalKey(settingsmigrate.CanonicalKey(strings.TrimSpace(key)))
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
	value, err := h.ReadAdminSetting(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

type updateSettingRequest struct {
	Value string `json:"value"`
}

type updateSettingsRequest struct {
	Values map[string]string `json:"values"`
}

type updateSettingsResponse struct {
	// CommittedSnapshot is captured under the settings write lock and never serialized.
	CommittedSnapshot *AdminSettingsSnapshot `json:"-"`

	Values              map[string]string `json:"values"`
	RestartRequired     bool              `json:"restart_required"`
	RestartRequiredKeys []string          `json:"restart_required_keys,omitempty"`
}

const errCodeStorageUnavailable = "storage_unavailable"

func (h *AdminHandler) normalizeBatchSetting(
	ctx context.Context,
	key, value string,
) (string, string, error) {
	if strings.HasPrefix(key, "ratelimit.") {
		return "", "bad_request", fmt.Errorf("%s is managed by /admin/rate-limits/config", key)
	}
	normalized, err := config.NormalizeAdminSetting(key, value)
	if err != nil {
		return "", "bad_request", err
	}

	switch key {
	case markers.SettingMode, markers.SettingLazyPlayback, markers.SettingOnlineStorage:
		normalized, err = markers.NormalizeSetting(key, normalized)
	case clientip.SettingTrustedProxies:
		normalized, err = clientip.NormalizeCIDRList(normalized)
		if err != nil {
			err = fmt.Errorf("clientip.trusted_proxies must be a comma-separated list of CIDRs: %w", err)
		}
	case diagnostics.KeyUploadsEnabled:
		if normalized == "true" {
			if err = h.validateDiagnosticsUploadsEnabled(ctx); err != nil {
				return "", errCodeStorageUnavailable, err
			}
		}
	case diagnostics.KeyMaxBundleBytes,
		diagnostics.KeyMaxUncompressedBytes,
		diagnostics.KeyMaxReportsPerUserDay,
		diagnostics.KeyRetentionDays,
		diagnostics.KeyMaxBytesPerUser:
		var numericValue int64
		numericValue, err = normalizeDiagnosticsNumericSettingValue(key, normalized)
		if err == nil {
			normalized = strconv.FormatInt(numericValue, 10)
		}
	case diagnostics.KeyConsentNoticeVersion:
		var n int
		n, err = strconv.Atoi(normalized)
		if err == nil && n < 1 {
			err = fmt.Errorf("%s must be an integer greater than 0", key)
		}
		if err == nil {
			normalized = strconv.Itoa(n)
		}
	case notifications.SettingPushRelayURL,
		notifications.SettingPushRelayDeploymentID,
		notifications.SettingPushRelayAPIKey,
		notifications.SettingPushRelayExpiresAt,
		notifications.SettingPushRelayKeyPrefix,
		notifications.SettingPushRelayReregister:
		err = fmt.Errorf("%s is managed by the push relay registration flow", key)
	case catalog.SearchSettingMeilisearchIndex:
		if normalized == "" {
			err = fmt.Errorf("%s is required", key)
		}
	case catalog.SearchSettingMeilisearchIndexTypes:
		var itemTypes []string
		itemTypes, err = catalog.NormalizeCatalogSearchIndexTypesValue(normalized)
		if err == nil {
			normalized = catalog.FormatCatalogSearchIndexTypesValue(itemTypes)
		}
	case catalog.SearchSettingMeilisearchEmbedder:
		normalized, err = catalog.NormalizeCatalogSearchEmbedderName(normalized)
	}
	if err != nil {
		return "", "bad_request", err
	}
	return normalized, "", nil
}

func validateProspectiveAdminSettings(values map[string]string, redisBootstrapAvailable bool) error {
	if err := config.ValidateAdminSettingsWithCapabilities(values, config.AdminSettingsCapabilities{
		RedisBootstrapAvailable: redisBootstrapAvailable,
	}); err != nil {
		return err
	}
	if _, err := catalog.CatalogSearchSettingsFromMap(values); err != nil {
		return err
	}
	return validateProspectiveDiagnosticsSettings(values)
}

var adminSettingDependencyGroups = [][]string{
	{"auth.access_token_expiry", "auth.refresh_token_expiry"},
	{"playback.watched_threshold", "playback.min_resume_threshold"},
	{config.PlaybackRoutingRemuxExecutionSettingKey, config.PlaybackRoutingRemuxEgressSettingKey},
	{config.PlaybackRoutingVideoTranscodeExecutionSettingKey, config.PlaybackRoutingVideoTranscodeEgressSettingKey},
	{"s3.public_endpoint", "s3.public_bucket"},
	{artworkStorageBackendKey, s3PublicEndpointKey, s3PublicBucketKey},
	{"s3.public_access_key", "s3.public_secret_key"},
	{"s3.private_endpoint", "s3.private_bucket"},
	{"s3.private_access_key", "s3.private_secret_key"},
	{"s3.public_url_auth", "s3.public_read_endpoint", "s3.public_token_secret"},
	{"email.enabled", "email.smtp_host", "email.from_address"},
	{"watchsync.trakt.client_id", "watchsync.trakt.client_secret"},
	{"watchsync.simkl.client_id", "watchsync.simkl.client_secret"},
	{"ratelimit.backend", "redis.url"},
	{"download.max_per_period", "download.period_duration"},
	{"matcher.enable_tv_series_root_queue", "matcher.enable_tv_series_group_queue"},
	{"ai.max_concurrent_jobs", "subtitle_ai.max_concurrent_jobs"},
	{
		diagnostics.KeyMaxBundleBytes,
		diagnostics.KeyMaxUncompressedBytes,
		diagnostics.KeyMaxReportsPerUserDay,
		diagnostics.KeyRetentionDays,
		diagnostics.KeyMaxBytesPerUser,
	},
}

func isPlaybackRoutingPairSetting(key string) bool {
	switch key {
	case config.PlaybackRoutingRemuxExecutionSettingKey,
		config.PlaybackRoutingRemuxEgressSettingKey,
		config.PlaybackRoutingVideoTranscodeExecutionSettingKey,
		config.PlaybackRoutingVideoTranscodeEgressSettingKey:
		return true
	default:
		return false
	}
}

// adminSettingsValidationSnapshot validates exactly the requested changes and
// the current values they depend on. The legacy single-key endpoint predates
// cross-field validation, so any relationship (or catalog value) can already
// be invalid in storage. Untouched legacy state must not poison an unrelated
// batch or the routing-pair checks that protect restartable configuration,
// while touching any member pulls the complete dependency group into the
// snapshot so a new or still-invalid relationship is rejected.
func adminSettingsValidationSnapshot(
	prospective map[string]string,
	changed map[string]string,
) map[string]string {
	snapshot := config.EffectiveAdminSettings(nil)
	effectiveProspective := config.EffectiveAdminSettings(prospective)
	for key, value := range changed {
		snapshot[key] = value
	}
	for _, group := range adminSettingDependencyGroups {
		touched := false
		for _, key := range group {
			if _, ok := changed[key]; ok {
				touched = true
				break
			}
		}
		if !touched {
			continue
		}
		for _, key := range group {
			snapshot[key] = effectiveProspective[key]
		}
	}

	// Operational S3 values are legacy fallbacks shared by the public and
	// private configurations. When one changes, validate the canonical values
	// that LoadFromDB will actually consume, including unchanged legacy peers.
	legacyS3Changed := false
	for key := range changed {
		if strings.HasPrefix(key, "s3.operational_") {
			legacyS3Changed = true
			break
		}
	}
	if legacyS3Changed {
		//nolint:goconst // Keep the complete canonical validation set readable as a contract.
		for _, key := range []string{
			"s3.public_endpoint",
			"s3.public_read_endpoint",
			"s3.public_region",
			"s3.public_path_style",
			"s3.public_bucket",
			"s3.public_key_prefix",
			"s3.public_access_key",
			"s3.public_secret_key",
			"s3.public_url_auth",
			"s3.public_token_secret",
			"s3.public_token_param",
			"s3.public_token_ttl",
			"s3.private_endpoint",
			"s3.private_region",
			"s3.private_path_style",
			"s3.private_bucket",
			"s3.private_key_prefix",
			"s3.private_access_key",
			"s3.private_secret_key",
		} {
			snapshot[key] = effectiveProspective[key]
		}
	}
	return snapshot
}

// activeAdminSettings overlays values owned by the process environment onto a
// stored snapshot. Updates never persist these values, but cross-field
// validation and effective-value comparisons must use the same configuration
// the runtime is actually consuming.
func (h *AdminHandler) activeAdminSettings(stored map[string]string) map[string]string {
	active := make(map[string]string, len(stored)+len(h.BootstrapSensitiveValues))
	for key, value := range stored {
		active[key] = value
	}
	for key, value := range h.BootstrapSensitiveValues {
		if h.BootstrapSensitiveConfigured[key] && value != "" {
			active[key] = value
		}
	}
	return active
}

func (h *AdminHandler) effectiveAdminSettings(stored map[string]string) map[string]string {
	return config.EffectiveAdminSettings(h.activeAdminSettings(stored))
}

func shouldPersistAdminSetting(stored map[string]string, key, normalized string, effectiveChanged bool) bool {
	current, exists := stored[key]
	if exists {
		return current != normalized
	}
	// Do not create a row merely because a client resubmitted an untouched
	// runtime default. Non-default values still need a row, while clearing an
	// already-absent override is a storage no-op.
	return normalized != "" && effectiveChanged
}

// HandleUpdateSettings handles PUT /admin/settings. Every requested value is
// normalized and validated with the prospective values it depends on before
// SetMany performs one transaction, so a multi-field save is all-or-nothing.
func (h *AdminHandler) HandleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if h.SettingsRepo == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Settings store not configured")
		return
	}

	var req updateSettingsRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	result, err := h.UpdateAdminSettings(r.Context(), req.Values, nil)
	if err != nil {
		writeAdminSettingsServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) UpdateAdminSettings(ctx context.Context, values map[string]string, guard func(AdminSettingsSnapshot) error) (AdminSettingsUpdateResult, error) {
	if h.SettingsRepo == nil {
		return AdminSettingsUpdateResult{}, &APIError{Status: 500, Code: "internal_error", Message: "Settings store not configured"}
	}
	req := updateSettingsRequest{Values: values}

	if len(req.Values) == 0 {
		return AdminSettingsUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "At least one setting is required"}
	}
	if len(req.Values) > 250 {
		return AdminSettingsUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "A settings update may contain at most 250 values"}
	}

	if guard != nil {
		stored, err := h.SettingsRepo.GetAll(ctx)
		if err != nil {
			return AdminSettingsUpdateResult{}, err
		}
		if err := guard(h.adminSettingsSnapshot(stored)); err != nil {
			return AdminSettingsUpdateResult{}, err
		}
	}

	keys := make([]string, 0, len(req.Values))
	for key := range req.Values {
		if strings.TrimSpace(key) == "" {
			return AdminSettingsUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Setting key is required"}
		}
		if machineManagedSettingKeys[key] {
			return AdminSettingsUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: key + " is managed internally"}
		}
		if h.BootstrapSensitiveConfigured[key] {
			return AdminSettingsUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "managed_by_environment", Message: key + " is managed by an environment variable"}
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	normalized := make(map[string]string, len(req.Values))
	for _, key := range keys {
		value, code, err := h.normalizeBatchSetting(ctx, key, req.Values[key])
		if err != nil {
			return AdminSettingsUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: code, Message: err.Error()}
		}
		normalized[key] = value
	}

	var (
		after            map[string]string
		effectiveChanges map[string]bool
		validationErr    error
		validationCode   string
	)
	var preconditionErr error
	var committedSnapshot *AdminSettingsSnapshot
	err := updateServerSettingsAtomically(ctx, h.SettingsRepo,
		func(stored map[string]string) (map[string]string, error) {
			if guard != nil {
				if err := guard(h.adminSettingsSnapshot(stored)); err != nil {
					preconditionErr = err
					return nil, err
				}
			}

			prospective := maps.Clone(stored)
			for key, value := range normalized {
				prospective[key] = value
			}
			if artworkStorageLocked(stored) {
				if err := rejectArtworkIdentityChange(stored[blobstore.IdentitySettingKey], h.effectiveAdminSettings(stored), h.effectiveAdminSettings(prospective)); err != nil {
					preconditionErr = err
					return nil, err
				}
			}
			activeProspective := h.activeAdminSettings(prospective)
			before := h.effectiveAdminSettings(stored)
			after = h.effectiveAdminSettings(prospective)
			// Cross-field checks run against the complete prospective state, so a
			// value the batch clears is gone even when the store still has it and
			// the current process is still running on it.
			validationSnapshot := adminSettingsValidationSnapshot(activeProspective, normalized)
			if err := validateProspectiveAdminSettings(validationSnapshot, h.RedisBootstrapAvailable); err != nil {
				validationErr = err
				validationCode = "invalid_settings"
				return nil, err
			}
			writes := make(map[string]string, len(normalized))
			effectiveChanges = make(map[string]bool, len(normalized))
			for key, value := range normalized {
				effectiveChanged := before[key] != after[key]
				if shouldPersistAdminSetting(stored, key, value, effectiveChanged) {
					writes[key] = value
				}
				if effectiveChanged {
					effectiveChanges[key] = true
				}
			}
			if guard != nil {
				committed := maps.Clone(stored)
				maps.Copy(committed, writes)
				committedSnapshot = new(h.adminSettingsSnapshot(committed))
			}
			return writes, nil
		})
	if preconditionErr != nil {
		return AdminSettingsUpdateResult{}, preconditionErr
	}
	if validationErr != nil {
		return AdminSettingsUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: validationCode, Message: validationErr.Error()}
	}
	if err != nil {
		return AdminSettingsUpdateResult{}, &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to update settings"}
	}

	responseValues := make(map[string]string, len(normalized))
	restartKeys := make([]string, 0, len(normalized))
	for _, key := range keys {
		if !sensitiveSettingKeys[key] {
			responseValues[key] = after[key]
		}
		if !effectiveChanges[key] {
			continue
		}
		if h.EventBus != nil {
			_ = h.EventBus.Publish(ctx, cache.ChannelAdmin,
				cache.Event{Type: cache.EventSettingsChanged, Payload: key})
		}
		if h.OnServerSettingUpdated != nil {
			h.OnServerSettingUpdated(ctx, key, after[key])
		}
		if config.RestartRequired(key) {
			restartKeys = append(restartKeys, key)
		}
	}
	// Per-key reasons ("setting:<key>") so the admin UI can scope a pending
	// restart to the subsystem the key belongs to instead of warning on every
	// tile for any settings save.
	for _, restartKey := range restartKeys {
		h.markServerRestartRequired("setting:" + restartKey)
	}
	return updateSettingsResponse{
		CommittedSnapshot:   committedSnapshot,
		Values:              responseValues,
		RestartRequired:     len(restartKeys) > 0,
		RestartRequiredKeys: restartKeys,
	}, nil
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
	if machineManagedSettingKeys[key] {
		writeError(w, http.StatusBadRequest, "bad_request", key+" is managed internally")
		return
	}
	if h.BootstrapSensitiveConfigured[key] {
		writeError(w, http.StatusBadRequest, "managed_by_environment", key+" is managed by an environment variable")
		return
	}
	if strings.HasPrefix(key, "ratelimit.") {
		writeError(w, http.StatusBadRequest, "bad_request", key+" is managed by /admin/rate-limits/config")
		return
	}

	var req updateSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	result, err := h.UpdateAdminSetting(r.Context(), key, req.Value, nil)
	if err != nil {
		writeAdminSettingsServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) UpdateAdminSetting(ctx context.Context, key, value string, guard func(AdminSettingsSnapshot) error) (AdminSettingUpdateResult, error) {
	req := updateSettingRequest{Value: value}

	if h.SettingsRepo == nil {
		return AdminSettingUpdateResult{}, &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Settings store not configured"}
	}

	if key == "" {
		return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "Setting key is required"}
	}
	if machineManagedSettingKeys[key] {
		return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: key + " is managed internally"}
	}
	if h.BootstrapSensitiveConfigured[key] {
		return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "managed_by_environment", Message: key + " is managed by an environment variable"}
	}
	if strings.HasPrefix(key, "ratelimit.") {
		return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: key + " is managed by /admin/rate-limits/config"}
	}

	if guard != nil {
		stored, err := h.SettingsRepo.GetAll(ctx)
		if err != nil {
			return AdminSettingUpdateResult{}, err
		}
		if err := guard(h.adminSettingsSnapshot(stored)); err != nil {
			return AdminSettingUpdateResult{}, err
		}
	}

	if normalized, err := config.NormalizeAdminSetting(key, req.Value); err != nil {
		return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	} else {
		req.Value = normalized
	}

	switch key {
	case markers.SettingMode, markers.SettingLazyPlayback, markers.SettingOnlineStorage:
		if normalized, err := markers.NormalizeSetting(key, req.Value); err != nil {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
		} else {
			req.Value = normalized
		}
	case clientip.SettingTrustedProxies:
		normalized, err := clientip.NormalizeCIDRList(req.Value)
		if err != nil {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "clientip.trusted_proxies must be a comma-separated list of CIDRs: " + err.Error()}
		}
		req.Value = normalized
	case "ai.base_url", "ai.asr_base_url", "ai.chat_model", "ai.asr_model":
		req.Value = strings.TrimSpace(req.Value)
	case "metadata_ai.on_view":
		switch req.Value {
		case "off", "button", "auto":
		default:
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "metadata_ai.on_view must be off, button, or auto"}
		}
	case policy.SettingDecisionLogVerbosity:
		switch strings.TrimSpace(strings.ToLower(req.Value)) {
		case policy.DecisionLogVerbosityDigest, policy.DecisionLogVerbosityVerbose:
			req.Value = strings.TrimSpace(strings.ToLower(req.Value))
		default:
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "policy.decision_log_verbosity must be digest or verbose"}
		}
	case "policy.editor_enabled":
		enabled, err := strconv.ParseBool(strings.TrimSpace(req.Value))
		if err != nil {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "policy.editor_enabled must be true or false"}
		}
		req.Value = strconv.FormatBool(enabled)
	case diagnostics.KeyUploadsEnabled:
		enabled, err := strconv.ParseBool(strings.TrimSpace(req.Value))
		if err != nil {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: diagnostics.KeyUploadsEnabled + " must be true or false"}
		}
		req.Value = strconv.FormatBool(enabled)
		if enabled {
			if err := h.validateDiagnosticsUploadsEnabled(ctx); err != nil {
				return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: errCodeStorageUnavailable, Message: err.Error()}
			}
		}
	case diagnostics.KeyMaxBundleBytes,
		diagnostics.KeyMaxUncompressedBytes,
		diagnostics.KeyMaxReportsPerUserDay,
		diagnostics.KeyRetentionDays,
		diagnostics.KeyMaxBytesPerUser:
		normalized, err := h.normalizeDiagnosticsNumericSetting(ctx, key, req.Value)
		if err != nil {
			if _, readFailed := errors.AsType[*adminSettingsReadError](err); guard != nil && readFailed {
				return AdminSettingUpdateResult{}, err
			}
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
		}
		req.Value = normalized
	case diagnostics.KeyConsentNoticeVersion:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n < 1 {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: diagnostics.KeyConsentNoticeVersion + " must be an integer greater than 0"}
		}
		req.Value = strconv.Itoa(n)
	case policy.SettingDecisionLogScopeSampleRate:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n <= 0 {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "policy.decision_log_scope_sample_rate must be an integer greater than 0"}
		}
		req.Value = strconv.Itoa(n)
	case policy.SettingDecisionLogRetentionDays:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n <= 0 {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "policy.decision_log_retention_days must be an integer greater than 0"}
		}
		req.Value = strconv.Itoa(n)
	case "subtitle_ai.transcribe_quota_jobs":
		if n, err := strconv.Atoi(req.Value); err != nil || n < 0 {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "subtitle_ai.transcribe_quota_jobs must be an integer >= 0 (0 = unlimited)"}
		}
	case "subtitle_ai.transcribe_quota_period":
		if !subtitleai.ValidQuotaPeriod(req.Value) {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "subtitle_ai.transcribe_quota_period must be day, week, or month"}
		}
	case notifications.SettingApplePushDeliveryEnabled,
		notifications.SettingAndroidPushDeliveryEnabled:
		enabled, err := strconv.ParseBool(strings.TrimSpace(req.Value))
		if err != nil {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: key + " must be true or false"}
		}
		req.Value = strconv.FormatBool(enabled)
	case notifications.SettingPushRelayURL,
		notifications.SettingPushRelayDeploymentID,
		notifications.SettingPushRelayAPIKey,
		notifications.SettingPushRelayExpiresAt,
		notifications.SettingPushRelayKeyPrefix,
		notifications.SettingPushRelayReregister:
		// The registration flow persists the relay URL, deployment id, and API
		// key together; a direct write to any of them desyncs the stored URL
		// from the credentials the relay minted for it (and feeds an arbitrary
		// id into the next rotation request).
		return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: key + " is managed by the push relay registration flow; use POST /admin/notifications/push/relay/register"}
	case catalog.SearchSettingProvider:
		switch strings.TrimSpace(strings.ToLower(req.Value)) {
		case catalog.SearchProviderPostgres, catalog.SearchProviderMeilisearch:
			req.Value = strings.TrimSpace(strings.ToLower(req.Value))
		default:
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.provider must be postgres or meilisearch"}
		}
	case catalog.SearchSettingMeilisearchURL:
		value := strings.TrimSpace(req.Value)
		if value != "" {
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.meilisearch.url must include scheme and host"}
			}
		}
		req.Value = value
	case catalog.SearchSettingMeilisearchIndex:
		req.Value = strings.TrimSpace(req.Value)
		if req.Value == "" {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.meilisearch.index is required"}
		}
	case catalog.SearchSettingMeilisearchTimeoutMS:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n <= 0 {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.meilisearch.timeout_ms must be an integer greater than 0"}
		}
		req.Value = strconv.Itoa(n)
	case catalog.SearchSettingMeilisearchMatchingStrategy:
		switch strings.TrimSpace(strings.ToLower(req.Value)) {
		case "last", "all":
			req.Value = strings.TrimSpace(strings.ToLower(req.Value))
		default:
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.meilisearch.matching_strategy must be last or all"}
		}
	case catalog.SearchSettingMeilisearchSyncBatchSize:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n < 1 || n > catalog.MaxMeilisearchSyncBatchSize {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.meilisearch.sync_batch_size must be an integer between 1 and 10000"}
		}
		req.Value = strconv.Itoa(n)
	case catalog.SearchSettingMeilisearchRebuildBatchSize:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n < 1 || n > catalog.MaxMeilisearchRebuildBatchSize {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.meilisearch.rebuild_batch_size must be an integer between 1 and 25000"}
		}
		req.Value = strconv.Itoa(n)
	case catalog.SearchSettingMeilisearchRebuildQueue:
		n, err := strconv.Atoi(strings.TrimSpace(req.Value))
		if err != nil || n < 1 || n > catalog.MaxMeilisearchRebuildQueueDepth {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.meilisearch.rebuild_task_queue_depth must be an integer between 1 and 16"}
		}
		req.Value = strconv.Itoa(n)
	case catalog.SearchSettingMeilisearchIndexTypes:
		itemTypes, err := catalog.NormalizeCatalogSearchIndexTypesValue(req.Value)
		if err != nil {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
		}
		req.Value = catalog.FormatCatalogSearchIndexTypesValue(itemTypes)
	case catalog.SearchSettingMeilisearchSemanticEnabled:
		enabled, err := strconv.ParseBool(strings.TrimSpace(req.Value))
		if err != nil {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.meilisearch.semantic_enabled must be true or false"}
		}
		req.Value = strconv.FormatBool(enabled)
	case catalog.SearchSettingMeilisearchSemanticRatio:
		ratio, err := strconv.ParseFloat(strings.TrimSpace(req.Value), 64)
		if err != nil || math.IsNaN(ratio) || ratio < 0 || ratio > 1 {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.meilisearch.semantic_ratio must be a number between 0 and 1"}
		}
		req.Value = strconv.FormatFloat(ratio, 'f', -1, 64)
	case catalog.SearchSettingMeilisearchEmbedder:
		embedder, err := catalog.NormalizeCatalogSearchEmbedderName(req.Value)
		if err != nil {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
		}
		req.Value = embedder
	case catalog.SearchSettingMeilisearchBinaryQuantized:
		enabled, err := strconv.ParseBool(strings.TrimSpace(req.Value))
		if err != nil {
			return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Message: "catalog.search.meilisearch.binary_quantized must be true or false"}
		}
		req.Value = strconv.FormatBool(enabled)
	}

	var (
		after            map[string]string
		effectiveChanged bool
		validationErr    error
		validationCode   string
	)
	var preconditionErr error
	err := updateServerSettingsAtomically(ctx, h.SettingsRepo,
		func(stored map[string]string) (map[string]string, error) {
			if guard != nil {
				if err := guard(h.adminSettingsSnapshot(stored)); err != nil {
					preconditionErr = err
					return nil, err
				}
			}

			prospective := maps.Clone(stored)
			prospective[key] = req.Value
			if artworkStorageLocked(stored) {
				if err := rejectArtworkIdentityChange(stored[blobstore.IdentitySettingKey], h.effectiveAdminSettings(stored), h.effectiveAdminSettings(prospective)); err != nil {
					preconditionErr = err
					return nil, err
				}
			}
			if isPlaybackRoutingPairSetting(key) {
				changed := map[string]string{key: req.Value}
				validationSnapshot := adminSettingsValidationSnapshot(h.activeAdminSettings(prospective), changed)
				if err := validateProspectiveAdminSettings(validationSnapshot, h.RedisBootstrapAvailable); err != nil {
					validationErr = err
					validationCode = "invalid_settings"
					return nil, err
				}
			}
			// This legacy route can only change one key, so enforcing every
			// cross-field invariant would make paired settings impossible to
			// establish or clear one write at a time. Per-key validation above
			// remains strict; restart-fatal routing pairs and durable prerequisites
			// are the exceptions — a single-key write may not break them.
			if key == "redis.url" {
				if err := config.ValidateRedisRateLimitTransport(
					h.activeAdminSettings(prospective),
					h.RedisBootstrapAvailable,
				); err != nil {
					validationErr = err
					validationCode = "invalid_settings"
					return nil, err
				}
			}
			// The artwork backend is the other durable prerequisite: an explicit
			// S3 backend without a public bucket cannot open after restart, whether
			// this write selects the backend or clears the bucket under it.
			if key == artworkStorageBackendKey || key == s3PublicBucketKey || key == s3OperationalBucketKey {
				if err := config.ValidateArtworkStorageSettings(h.effectiveAdminSettings(prospective)); err != nil {
					validationErr = err
					validationCode = "invalid_settings"
					return nil, err
				}
			}

			before := h.effectiveAdminSettings(stored)
			after = h.effectiveAdminSettings(prospective)
			effectiveChanged = before[key] != after[key]
			if shouldPersistAdminSetting(stored, key, req.Value, effectiveChanged) {
				return map[string]string{key: req.Value}, nil
			}
			return nil, nil
		})
	if preconditionErr != nil {
		return AdminSettingUpdateResult{}, preconditionErr
	}
	if validationErr != nil {
		return AdminSettingUpdateResult{}, &APIError{Status: http.StatusBadRequest, Code: validationCode, Message: validationErr.Error()}
	}
	if err != nil {
		return AdminSettingUpdateResult{}, &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to update setting"}
	}
	if effectiveChanged {
		if h.EventBus != nil {
			_ = h.EventBus.Publish(ctx, cache.ChannelAdmin,
				cache.Event{Type: cache.EventSettingsChanged, Payload: key})
		}
		if h.OnServerSettingUpdated != nil {
			h.OnServerSettingUpdated(ctx, key, after[key])
		}
	}
	restartRequired := effectiveChanged && config.RestartRequired(key)
	if restartRequired {
		h.markServerRestartRequired("setting:" + key)
	}
	if sensitiveSettingKeys[key] {
		return adminSettingResponse{Key: key, RestartRequired: restartRequired}, nil
	}
	return adminSettingResponse{Key: key, Value: after[key], RestartRequired: restartRequired}, nil
}

func (h *AdminHandler) validateDiagnosticsUploadsEnabled(ctx context.Context) error {
	if h.DiagnosticsStore == nil || strings.TrimSpace(h.DiagnosticsStore.Bucket()) == "" {
		return errors.New("diagnostics uploads require configured private object storage")
	}
	const probeKey = "diagnostics/.probe"
	if err := h.DiagnosticsStore.PutStream(
		ctx,
		h.DiagnosticsStore.Bucket(),
		probeKey,
		strings.NewReader("ok"),
		"application/octet-stream",
	); err != nil {
		return fmt.Errorf("diagnostics storage probe write failed: %w", err)
	}
	if err := h.DiagnosticsStore.DeleteObject(ctx, h.DiagnosticsStore.Bucket(), probeKey); err != nil {
		return fmt.Errorf("diagnostics storage probe delete failed: %w", err)
	}
	return nil
}

// Keep the frozen bridge's diagnostic text while allowing guarded writes to
// distinguish a storage failure from invalid caller input.
type adminSettingsReadError struct{ cause error }

func (e *adminSettingsReadError) Error() string {
	return "load diagnostics settings: " + e.cause.Error()
}
func (e *adminSettingsReadError) Unwrap() error { return e.cause }

func (h *AdminHandler) normalizeDiagnosticsNumericSetting(ctx context.Context, key, raw string) (string, error) {
	value, err := normalizeDiagnosticsNumericSettingValue(key, raw)
	if err != nil {
		return "", err
	}

	settings := diagnostics.DefaultSettings()
	if h.SettingsRepo != nil {
		loaded, loadErr := diagnostics.LoadSettings(ctx, h.SettingsRepo)
		if loadErr != nil {
			return "", &adminSettingsReadError{cause: loadErr}
		}
		settings = loaded
	}

	switch key {
	case diagnostics.KeyMaxBundleBytes:
		if value > settings.MaxUncompressedBytes {
			return "", fmt.Errorf("%s must not exceed %s (%d bytes)", key, diagnostics.KeyMaxUncompressedBytes, settings.MaxUncompressedBytes)
		}
		// A single bundle can never exceed the per-user byte cap, or every upload
		// at this size would fail quota; keep the two bounds consistent.
		if value > settings.MaxBytesPerUser {
			return "", fmt.Errorf("%s must not exceed %s (%d bytes)", key, diagnostics.KeyMaxBytesPerUser, settings.MaxBytesPerUser)
		}
	case diagnostics.KeyMaxUncompressedBytes:
		if value < settings.MaxBundleBytes {
			return "", fmt.Errorf("%s must be at least %s (%d bytes)", key, diagnostics.KeyMaxBundleBytes, settings.MaxBundleBytes)
		}
	case diagnostics.KeyMaxReportsPerUserDay, diagnostics.KeyRetentionDays:
		// These settings have only independent bounds, which were checked above.
	case diagnostics.KeyMaxBytesPerUser:
		// The per-user cap must leave room for at least one max-size bundle, or
		// /diagnostics/status would advertise a bundle size InsertReceiving always
		// rejects as quota_exceeded.
		if value < settings.MaxBundleBytes {
			return "", fmt.Errorf("%s must be at least %s (%d bytes)", key, diagnostics.KeyMaxBundleBytes, settings.MaxBundleBytes)
		}
	default:
		return "", fmt.Errorf("unsupported diagnostics numeric setting %s", key)
	}

	return strconv.FormatInt(value, 10), nil
}

const (
	diagnosticsMiB = int64(1024 * 1024)
	diagnosticsGiB = 1024 * diagnosticsMiB
)

func normalizeDiagnosticsNumericSettingValue(key, raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}

	switch key {
	case diagnostics.KeyMaxBundleBytes:
		if value < diagnosticsMiB || value > 256*diagnosticsMiB {
			return 0, fmt.Errorf(
				"%s must be between 1 MiB (%d bytes) and 256 MiB (%d bytes)",
				key,
				diagnosticsMiB,
				256*diagnosticsMiB,
			)
		}
	case diagnostics.KeyMaxUncompressedBytes:
		if value < diagnosticsMiB || value > diagnosticsGiB {
			return 0, fmt.Errorf(
				"%s must be between 1 MiB (%d bytes) and 1 GiB (%d bytes)",
				key,
				diagnosticsMiB,
				diagnosticsGiB,
			)
		}
	case diagnostics.KeyMaxReportsPerUserDay:
		if value < 1 || value > 1000 {
			return 0, fmt.Errorf("%s must be between 1 and 1000", key)
		}
	case diagnostics.KeyRetentionDays:
		if value < 1 || value > 365 {
			return 0, fmt.Errorf("%s must be between 1 and 365", key)
		}
	case diagnostics.KeyMaxBytesPerUser:
		if value < 10*diagnosticsMiB || value > 10*diagnosticsGiB {
			return 0, fmt.Errorf(
				"%s must be between 10 MiB (%d bytes) and 10 GiB (%d bytes)",
				key,
				10*diagnosticsMiB,
				10*diagnosticsGiB,
			)
		}
	default:
		return 0, fmt.Errorf("unsupported diagnostics numeric setting %s", key)
	}
	return value, nil
}

func validateProspectiveDiagnosticsSettings(values map[string]string) error {
	settings := diagnostics.DefaultSettings()
	targets := []struct {
		key    string
		assign func(int64)
	}{
		{diagnostics.KeyMaxBundleBytes, func(value int64) { settings.MaxBundleBytes = value }},
		{diagnostics.KeyMaxUncompressedBytes, func(value int64) { settings.MaxUncompressedBytes = value }},
		{diagnostics.KeyMaxReportsPerUserDay, func(value int64) { settings.MaxReportsPerUserDay = int(value) }},
		{diagnostics.KeyRetentionDays, func(value int64) { settings.RetentionDays = int(value) }},
		{diagnostics.KeyMaxBytesPerUser, func(value int64) { settings.MaxBytesPerUser = value }},
	}
	for _, target := range targets {
		raw := values[target.key]
		if raw == "" {
			continue
		}
		value, err := normalizeDiagnosticsNumericSettingValue(target.key, raw)
		if err != nil {
			return err
		}
		target.assign(value)
	}

	if settings.MaxBundleBytes > settings.MaxUncompressedBytes {
		return fmt.Errorf(
			"%s must not exceed %s (%d bytes)",
			diagnostics.KeyMaxBundleBytes,
			diagnostics.KeyMaxUncompressedBytes,
			settings.MaxUncompressedBytes,
		)
	}
	if settings.MaxBundleBytes > settings.MaxBytesPerUser {
		return fmt.Errorf(
			"%s must not exceed %s (%d bytes)",
			diagnostics.KeyMaxBundleBytes,
			diagnostics.KeyMaxBytesPerUser,
			settings.MaxBytesPerUser,
		)
	}
	return nil
}

// These aliases expose the established receipts without changing bridge JSON.
type AdminSettingsUpdateResult = updateSettingsResponse
type AdminSettingUpdateResult = adminSettingResponse

func writeAdminSettingsServiceError(w http.ResponseWriter, err error) {
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update settings")
}
