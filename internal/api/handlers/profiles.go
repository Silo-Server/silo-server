package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ProfileHandler handles profile CRUD endpoints.
type ProfileHandler struct {
	storeProvider  userstore.UserStoreProvider
	SessionsReader playbackSessionsReader
	UserRepo       interface {
		GetByID(ctx context.Context, id int) (*models.User, error)
	}
	ProfileTokens *access.ProfileTokenService
	AvatarStore   profileAvatarStore
	AvatarTTL     time.Duration
	// DeviceLibraryPurger removes a deleted profile's device rows (and, via
	// cascade, its managed downloads and subscriptions). Profiles may live
	// outside Postgres, so no FK cascade covers these shared tables.
	DeviceLibraryPurger interface {
		PurgeProfileDevices(ctx context.Context, userID int, profileID string) error
	}
	// EventsHub, when set, receives a user_settings.changed event for every
	// canonical setting row a profile mutation syncs (see
	// profiles_settings_sync.go). Nil (as in tests) simply skips publishing.
	EventsHub *evt.Hub
}

// NewProfileHandler creates a new ProfileHandler.
func NewProfileHandler(provider userstore.UserStoreProvider) *ProfileHandler {
	return &ProfileHandler{
		storeProvider: provider,
		AvatarTTL:     15 * time.Minute,
	}
}

// --- Request/Response types ---

// ProfileCreateRequest is the v1 POST /profiles body; v2 createProfile lowers
// its own body onto it.
type ProfileCreateRequest struct {
	Name                       string `json:"name"`
	Avatar                     string `json:"avatar,omitempty"`
	PIN                        string `json:"pin,omitempty"`
	IsChild                    bool   `json:"is_child"`
	MaxContentRating           string `json:"max_content_rating,omitempty"`
	QualityPreference          string `json:"quality_preference,omitempty"`
	Language                   string `json:"language,omitempty"`
	PreferredMetadataLanguage  string `json:"preferred_metadata_language,omitempty"`
	SubtitleLanguage           string `json:"subtitle_language,omitempty"`
	SubtitleMode               string `json:"subtitle_mode,omitempty"`
	AutoSkipIntro              bool   `json:"auto_skip_intro"`
	AutoSkipCredits            bool   `json:"auto_skip_credits"`
	AutoSkipRecap              bool   `json:"auto_skip_recap"`
	AutoPlayNextPreview        bool   `json:"auto_play_next_preview"`
	ShowForcedSubtitles        *bool  `json:"show_forced_subtitles,omitempty"`
	LibraryRestrictionsEnabled bool   `json:"library_restrictions_enabled"`
	AllowedLibraryIDs          []int  `json:"allowed_library_ids"`
	MaxPlaybackQuality         string `json:"max_playback_quality"`
}

type ProfileUpdateRequest struct {
	Name                       *string `json:"name,omitempty"`
	Avatar                     *string `json:"avatar,omitempty"`
	PIN                        *string `json:"pin,omitempty"`
	IsChild                    *bool   `json:"is_child,omitempty"`
	MaxContentRating           *string `json:"max_content_rating,omitempty"`
	QualityPreference          *string `json:"quality_preference,omitempty"`
	Language                   *string `json:"language,omitempty"`
	PreferredMetadataLanguage  *string `json:"preferred_metadata_language,omitempty"`
	SubtitleLanguage           *string `json:"subtitle_language,omitempty"`
	SubtitleMode               *string `json:"subtitle_mode,omitempty"`
	AutoSkipIntro              *bool   `json:"auto_skip_intro,omitempty"`
	AutoSkipCredits            *bool   `json:"auto_skip_credits,omitempty"`
	AutoSkipRecap              *bool   `json:"auto_skip_recap,omitempty"`
	AutoPlayNextPreview        *bool   `json:"auto_play_next_preview,omitempty"`
	ShowForcedSubtitles        *bool   `json:"show_forced_subtitles,omitempty"`
	LibraryRestrictionsEnabled *bool   `json:"library_restrictions_enabled,omitempty"`
	AllowedLibraryIDs          *[]int  `json:"allowed_library_ids,omitempty"`
	MaxPlaybackQuality         *string `json:"max_playback_quality,omitempty"`
}

type verifyPINRequest struct {
	PIN string `json:"pin"`
}

type ProfileView struct {
	ID                         string `json:"id"`
	Name                       string `json:"name"`
	Avatar                     string `json:"avatar,omitempty"`
	AvatarURL                  string `json:"avatar_url,omitempty"`
	AvatarSource               string `json:"avatar_source,omitempty"`
	HasPIN                     bool   `json:"has_pin"`
	IsChild                    bool   `json:"is_child"`
	IsPrimary                  bool   `json:"is_primary"`
	MaxContentRating           string `json:"max_content_rating,omitempty"`
	QualityPreference          string `json:"quality_preference,omitempty"`
	Language                   string `json:"language,omitempty"`
	PreferredMetadataLanguage  string `json:"preferred_metadata_language,omitempty"`
	SubtitleLanguage           string `json:"subtitle_language,omitempty"`
	SubtitleMode               string `json:"subtitle_mode,omitempty"`
	AutoSkipIntro              bool   `json:"auto_skip_intro"`
	AutoSkipCredits            bool   `json:"auto_skip_credits"`
	AutoSkipRecap              bool   `json:"auto_skip_recap"`
	AutoPlayNextPreview        bool   `json:"auto_play_next_preview"`
	ShowForcedSubtitles        bool   `json:"show_forced_subtitles"`
	LibraryRestrictionsEnabled bool   `json:"library_restrictions_enabled"`
	AllowedLibraryIDs          []int  `json:"allowed_library_ids"`
	MaxPlaybackQuality         string `json:"max_playback_quality"`
	CreatedAt                  string `json:"created_at"`
	UpdatedAt                  string `json:"updated_at"`
}

// ProfileListView is the v1 GET /profiles response.
type ProfileListView struct {
	Profiles            []ProfileView `json:"profiles"`
	AvatarUploadEnabled bool          `json:"avatar_upload_enabled"`
}

type verifyPINResponse struct {
	Valid        bool   `json:"valid"`
	ProfileToken string `json:"profile_token,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

// canManageHouseholdProfiles reports whether the caller may create/update/delete
// profiles belonging to their user.
//
// The rule lives in household.go so the settings routes can apply the same one:
// a household parent who may edit a child's profile may also edit that child's
// device settings, and two definitions of "is this the household parent" would
// eventually disagree.
func (h *ProfileHandler) canManageHouseholdProfiles(r *http.Request, store userstore.UserStore) (bool, error) {
	return canManageHousehold(r, store, h.userLookupOrNil(), h.ProfileTokens)
}

// userLookupOrNil returns UserRepo as the narrow interface the household check
// wants, preserving nil-ness: a typed nil in a non-nil interface would defeat
// the fail-closed check there.
func (h *ProfileHandler) userLookupOrNil() userLookup {
	if h.UserRepo == nil {
		return nil
	}
	return h.UserRepo
}

func writeProfileManagementPermissionError(w http.ResponseWriter, err error) {
	if errors.Is(err, access.ErrProfileUnverified) {
		writeError(w, http.StatusForbidden, "forbidden", "Profile management requires verifying the primary profile PIN")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Failed to check profile permissions")
}

// profileNameConflicts reports whether a profile other than excludeID already
// uses name within this account's store, comparing the trimmed forms
// case-insensitively so "Laura" and " laura " count as the same household
// member. Scoping is per account by construction: callers pass the profile
// list of a single user's store, so another account's profiles can never
// conflict.
//
// This is a check-then-write guard with no store-level uniqueness constraint
// (the userstore's dual Postgres/SQLite backends carry no unique index on
// name), so two concurrent requests can both pass and insert duplicates —
// the same window the profile_limit_reached check accepts. Good enough for
// interactive profile management; a functional unique index is the fix if
// that ever stops being true.
func profileNameConflicts(profiles []userstore.Profile, name, excludeID string) bool {
	trimmed := strings.TrimSpace(name)
	for _, p := range profiles {
		if p.ID == excludeID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(p.Name), trimmed) {
			return true
		}
	}
	return false
}

// isAllowedSelfServiceProfileUpdate reports whether a non-admin update request
// only touches fields the user is allowed to change on their own profiles.
// Admin-only fields (access policy: library restrictions, content rating,
// playback-quality cap, child-profile flag) must be rejected for non-admins.
func isAllowedSelfServiceProfileUpdate(req ProfileUpdateRequest) bool {
	return req.IsChild == nil &&
		req.MaxContentRating == nil &&
		req.LibraryRestrictionsEnabled == nil &&
		req.AllowedLibraryIDs == nil &&
		req.MaxPlaybackQuality == nil
}

// --- Handler methods ---

// HandleListProfiles handles GET /profiles.
func (h *ProfileHandler) HandleListProfiles(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	resp, err := h.ListProfiles(r.Context(), userID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListProfiles lists the account's household. v1 GET /profiles and v2
// listProfiles both call it; a failure is an *APIError carrying the v1
// status, code and message.
func (h *ProfileHandler) ListProfiles(ctx context.Context, userID int) (ProfileListView, error) {
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return ProfileListView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	profiles, err := store.ListProfiles(ctx)
	if err != nil {
		return ProfileListView{}, apiError(http.StatusInternalServerError, "internal_error", "Failed to list profiles")
	}
	return ProfileListView{
		Profiles:            h.toProfileResponses(ctx, store, profiles),
		AvatarUploadEnabled: h.AvatarStore != nil,
	}, nil
}

// HandleCreateProfile handles POST /profiles.
func (h *ProfileHandler) HandleCreateProfile(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req ProfileCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	created, err := h.CreateProfile(r.Context(), ProfileCreateCommand{
		UserID:          userID,
		ActiveProfileID: activeProfileIDOf(r),
		Request:         req,
		VerifyProfile: func(id string) error {
			return verifyProfileToken(r, h.userLookupOrNil(), h.ProfileTokens, id)
		},
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == codeProfileManagement {
			writeProfileManagementPermissionError(w, apiErr.cause)
			return
		}
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// ProfileCreateCommand is a profile creation with its request already parsed
// and its caller already reduced to an identity.
type ProfileCreateCommand struct {
	UserID int
	// ActiveProfileID is the profile the caller acts as ("" when none).
	ActiveProfileID string
	Request         ProfileCreateRequest
	// VerifyProfile confirms a PIN-locked primary profile is verified for
	// this request; it returns access.ErrProfileUnverified when it is not.
	VerifyProfile func(profileID string) error
}

// CreateProfile creates a household profile: validation, the bootstrap or
// household-manager authorization, the profile limit, the name-conflict
// check, the atomic column and canonical-settings write, and the re-read.
// v1 POST /profiles and v2 createProfile both call it; a failure is an
// *APIError carrying the v1 status, code and message.
func (h *ProfileHandler) CreateProfile(ctx context.Context, cmd ProfileCreateCommand) (ProfileView, error) {
	var none ProfileView
	req := cmd.Request
	userID := cmd.UserID
	if strings.TrimSpace(req.Name) == "" {
		return none, fieldError("name", "Profile name is required")
	}
	avatarRef, err := normalizePresetAvatarReference(req.Avatar)
	if err != nil {
		return none, fieldError("avatar", err.Error())
	}

	maxPlaybackQuality, ok := access.ParsePlaybackQualityPreset(req.MaxPlaybackQuality)
	if !ok {
		return none, fieldError("max_playback_quality", "Invalid max_playback_quality")
	}

	// Planned before anything is written: a preference value the canonical
	// store would refuse must fail the request while it is still a no-op.
	settingsSync, err := planCreateProfileSettingsSync(req)
	if err != nil {
		return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
	}

	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	existingProfiles, err := store.ListProfiles(ctx)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to list profiles")
	}
	// The very first profile on a user can be bootstrapped without
	// primary/admin privileges (it becomes the primary); everything after
	// requires either the server admin role or the caller's active profile
	// being primary.
	isBootstrap := len(existingProfiles) == 0
	if !isBootstrap {
		allowed, err := canManageHouseholdAs(ctx, store, cmd.ActiveProfileID, cmd.VerifyProfile)
		if err != nil {
			return none, profileManagementError(err)
		}
		if !allowed {
			return none, apiError(http.StatusForbidden, "forbidden", "Profile management requires the primary profile or admin access")
		}
	}
	// Access-policy fields only make sense when set by a manager on a managed
	// profile. On bootstrap the caller is becoming primary themselves, so non-
	// admin bootstrap creations must leave those fields at their defaults.
	if isBootstrap && !apimw.IsAdmin(ctx) &&
		(req.IsChild || req.MaxContentRating != "" ||
			req.LibraryRestrictionsEnabled || len(req.AllowedLibraryIDs) > 0 ||
			req.MaxPlaybackQuality != "") {
		return none, apiError(http.StatusForbidden, "forbidden", "Profile access settings require the primary profile or admin access")
	}
	if h.UserRepo != nil {
		user, err := h.UserRepo.GetByID(ctx, userID)
		if err != nil {
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to load user")
		}
		if user != nil && user.MaxProfiles >= 1 && len(existingProfiles) >= user.MaxProfiles {
			return none, apiError(http.StatusConflict, "profile_limit_reached",
				fmt.Sprintf("This account has reached its profile limit (%d)", user.MaxProfiles))
		}
	}

	if profileNameConflicts(existingProfiles, req.Name, "") {
		return none, apiError(http.StatusConflict, "name_conflict", "A profile with this name already exists")
	}

	showForcedSubtitles := true
	if req.ShowForcedSubtitles != nil {
		showForcedSubtitles = *req.ShowForcedSubtitles
	}

	profileID := uuid.New().String()
	profile := userstore.Profile{
		ID: profileID,
		// Store the trimmed form the conflict check compared, so " Laura "
		// doesn't persist with stray whitespace.
		Name:                       strings.TrimSpace(req.Name),
		Avatar:                     avatarRef,
		IsChild:                    req.IsChild,
		MaxContentRating:           req.MaxContentRating,
		QualityPreference:          req.QualityPreference,
		Language:                   req.Language,
		PreferredMetadataLanguage:  req.PreferredMetadataLanguage,
		SubtitleLanguage:           req.SubtitleLanguage,
		SubtitleMode:               req.SubtitleMode,
		AutoSkipIntro:              req.AutoSkipIntro,
		AutoSkipCredits:            req.AutoSkipCredits,
		AutoSkipRecap:              req.AutoSkipRecap,
		AutoPlayNextPreview:        req.AutoPlayNextPreview,
		ShowForcedSubtitles:        showForcedSubtitles,
		LibraryRestrictionsEnabled: req.LibraryRestrictionsEnabled,
		AllowedLibraryIDs:          req.AllowedLibraryIDs,
		MaxPlaybackQuality:         maxPlaybackQuality,
	}

	if err := h.createProfileWithSettingsSync(ctx, store, userID, profile, settingsSync); err != nil {
		slog.ErrorContext(ctx, "profile create failed to sync canonical settings",
			"component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to store profile preferences")
	}

	// Fetch the created profile directly by ID (no race condition).
	createdPtr, err := store.GetProfile(ctx, profileID)
	if err != nil || createdPtr == nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to retrieve created profile")
	}
	created := *createdPtr

	// If PIN was provided, update the profile to set it.
	if req.PIN != "" {
		if err := store.UpdateProfile(ctx, created.ID, userstore.UpdateProfileInput{
			PIN: &req.PIN,
		}); err != nil {
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to set profile PIN")
		}
		// Re-read the profile to get the updated state.
		p, err := store.GetProfile(ctx, created.ID)
		if err != nil || p == nil {
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to retrieve profile after PIN set")
		}
		created = *p
	}
	if req.ShowForcedSubtitles != nil && !*req.ShowForcedSubtitles {
		if err := store.UpdateProfile(ctx, created.ID, userstore.UpdateProfileInput{
			ShowForcedSubtitles: req.ShowForcedSubtitles,
		}); err != nil {
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to set forced subtitle preference")
		}
		p, err := store.GetProfile(ctx, created.ID)
		if err != nil || p == nil {
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to retrieve profile after forced subtitle update")
		}
		created = *p
	}

	return h.toProfileResponse(ctx, store, created), nil
}

// HandleUpdateProfile handles PUT /profiles/{id}.
func (h *ProfileHandler) HandleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	profileID := chi.URLParam(r, "id")
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Profile ID is required")
		return
	}

	var req ProfileUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	resp, err := h.UpdateProfile(r.Context(), ProfileUpdateCommand{
		UserID:          userID,
		ProfileID:       profileID,
		ActiveProfileID: activeProfileIDOf(r),
		Request:         req,
		VerifyProfile: func(id string) error {
			return verifyProfileToken(r, h.userLookupOrNil(), h.ProfileTokens, id)
		},
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == codeProfileManagement {
			writeProfileManagementPermissionError(w, apiErr.cause)
			return
		}
		writeAPIError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// ProfileUpdateCommand is a profile update with its request already parsed
// and its caller already reduced to an identity.
type ProfileUpdateCommand struct {
	UserID    int
	ProfileID string
	// ActiveProfileID is the profile the caller acts as ("" when none).
	ActiveProfileID string
	Request         ProfileUpdateRequest
	// VerifyProfile confirms a PIN-locked primary profile is verified for
	// this request; it returns access.ErrProfileUnverified when it is not.
	VerifyProfile func(profileID string) error
}

// UpdateProfile applies a profile update: authorization (household manager
// or self-service), validation, the name-conflict check, the atomic column
// and canonical-settings write, and the re-read. v1 PUT /profiles/{id} and v2
// updateProfile both call it; a failure is an *APIError carrying the v1
// status, code and message.
func (h *ProfileHandler) UpdateProfile(ctx context.Context, cmd ProfileUpdateCommand) (ProfileView, error) {
	var none ProfileView
	req := cmd.Request
	userID, profileID := cmd.UserID, cmd.ProfileID
	var avatarRef *string
	if req.Avatar != nil {
		normalized, err := normalizePresetAvatarReference(*req.Avatar)
		if err != nil {
			return none, fieldError("avatar", err.Error())
		}
		avatarRef = &normalized
	}

	var maxPlaybackQuality *string
	if req.MaxPlaybackQuality != nil {
		normalized, ok := access.ParsePlaybackQualityPreset(*req.MaxPlaybackQuality)
		if !ok {
			return none, fieldError("max_playback_quality", "Invalid max_playback_quality")
		}
		maxPlaybackQuality = &normalized
	}

	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	currentProfile, err := store.GetProfile(ctx, profileID)
	if err != nil || currentProfile == nil {
		return none, apiError(http.StatusNotFound, "not_found", "Profile not found")
	}

	canManage, err := canManageHouseholdAs(ctx, store, cmd.ActiveProfileID, cmd.VerifyProfile)
	if err != nil {
		return none, profileManagementError(err)
	}
	if !canManage {
		// Non-managers may only update their own active profile and only a
		// narrow set of playback preferences.
		if cmd.ActiveProfileID == "" || cmd.ActiveProfileID != profileID {
			return none, apiError(http.StatusForbidden, "forbidden", "You can only update the active profile's playback preferences")
		}
		if !isAllowedSelfServiceProfileUpdate(req) {
			return none, apiError(http.StatusForbidden, "forbidden", "Profile access settings require the primary profile or admin access")
		}
	}

	if req.Name != nil {
		// Normalize to the trimmed form up front: the conflict check compares
		// it and the store persists it, so " Laura " never lands verbatim.
		trimmedName := strings.TrimSpace(*req.Name)
		if trimmedName == "" {
			return none, fieldError("name", "Profile name is required")
		}
		req.Name = &trimmedName
		existingProfiles, err := store.ListProfiles(ctx)
		if err != nil {
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to list profiles")
		}
		if profileNameConflicts(existingProfiles, *req.Name, profileID) {
			return none, apiError(http.StatusConflict, "name_conflict", "A profile with this name already exists")
		}
	}

	// Planned before the transaction so an invalid preference fails while the
	// request is still a no-op.
	settingsSync, err := planUpdateProfileSettingsSync(req)
	if err != nil {
		return none, apiError(http.StatusBadRequest, "bad_request", err.Error())
	}

	input := userstore.UpdateProfileInput{
		Name:                       req.Name,
		Avatar:                     avatarRef,
		PIN:                        req.PIN,
		IsChild:                    req.IsChild,
		MaxContentRating:           req.MaxContentRating,
		QualityPreference:          req.QualityPreference,
		Language:                   req.Language,
		PreferredMetadataLanguage:  req.PreferredMetadataLanguage,
		SubtitleLanguage:           req.SubtitleLanguage,
		SubtitleMode:               req.SubtitleMode,
		AutoSkipIntro:              req.AutoSkipIntro,
		AutoSkipCredits:            req.AutoSkipCredits,
		AutoSkipRecap:              req.AutoSkipRecap,
		AutoPlayNextPreview:        req.AutoPlayNextPreview,
		ShowForcedSubtitles:        req.ShowForcedSubtitles,
		LibraryRestrictionsEnabled: req.LibraryRestrictionsEnabled,
		AllowedLibraryIDs:          req.AllowedLibraryIDs,
		MaxPlaybackQuality:         maxPlaybackQuality,
	}

	// The profile columns and their canonical projections commit together. A
	// failure cannot leave a 500 response whose legacy values look saved while
	// canonical readers continue serving the previous preference.
	if err := h.applyProfileUpdateSettingsSync(
		ctx, store, userID, profileID, input, settingsSync,
	); err != nil {
		slog.ErrorContext(ctx, "profile update failed to sync canonical settings",
			"component", "api", "user_id", userID, "profile_id", profileID, "error", err)
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to store profile preferences")
	}
	if currentProfile.Avatar != "" && avatarRef != nil && avatarRefReplacesUpload(currentProfile.Avatar, *avatarRef) {
		if cleanupErr := deleteUploadedAvatarObjects(ctx, h.AvatarStore, userID, profileID); cleanupErr != nil {
			slog.WarnContext(ctx, "profile avatar cleanup failed after update", "component", "api", "user_id", userID, "profile_id", profileID, "error", cleanupErr)
		}
	}

	// Re-read the profile to return the updated state.
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil || profile == nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to retrieve updated profile")
	}

	return h.toProfileResponse(ctx, store, *profile), nil
}

// codeProfileManagement is the error code of a household-management check
// the caller did not pass; the v1 handlers branch on it to render the
// PIN-verification message.
const codeProfileManagement = "profile_management"

// profileManagementError wraps a household-permission failure so the v1
// handler can keep its exact wording (writeProfileManagementPermissionError)
// and the v2 listener still sees the status.
func profileManagementError(err error) *APIError {
	out := &APIError{Status: http.StatusInternalServerError, Code: codeProfileManagement, Message: "Failed to check profile permissions", cause: err}
	if errors.Is(err, access.ErrProfileUnverified) {
		out.Status = http.StatusForbidden
		out.Message = "Profile management requires verifying the primary profile PIN"
	}
	return out
}

// HandleDeleteProfile handles DELETE /profiles/{id}.
func (h *ProfileHandler) HandleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	profileID := chi.URLParam(r, "id")
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Profile ID is required")
		return
	}

	err := h.DeleteProfile(r.Context(), ProfileDeleteCommand{
		UserID:          userID,
		ProfileID:       profileID,
		ActiveProfileID: activeProfileIDOf(r),
		VerifyProfile: func(id string) error {
			return verifyProfileToken(r, h.userLookupOrNil(), h.ProfileTokens, id)
		},
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == codeProfileManagement {
			writeProfileManagementPermissionError(w, apiErr.cause)
			return
		}
		writeAPIError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ProfileDeleteCommand is a profile deletion with its caller already reduced
// to an identity.
type ProfileDeleteCommand struct {
	UserID    int
	ProfileID string
	// ActiveProfileID is the profile the caller acts as ("" when none).
	ActiveProfileID string
	// VerifyProfile confirms a PIN-locked primary profile is verified for
	// this request; it returns access.ErrProfileUnverified when it is not.
	VerifyProfile func(profileID string) error
}

// DeleteProfile deletes a household profile: the household-manager
// authorization, the primary-profile guard, the delete, and the best-effort
// avatar and device-library cleanup. v1 DELETE /profiles/{id} and v2
// deleteProfile both call it; a failure is an *APIError carrying the v1
// status, code and message.
func (h *ProfileHandler) DeleteProfile(ctx context.Context, cmd ProfileDeleteCommand) error {
	userID, profileID := cmd.UserID, cmd.ProfileID
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	allowed, err := canManageHouseholdAs(ctx, store, cmd.ActiveProfileID, cmd.VerifyProfile)
	if err != nil {
		return profileManagementError(err)
	}
	if !allowed {
		return apiError(http.StatusForbidden, "forbidden", "Profile management requires the primary profile or admin access")
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil || profile == nil {
		return apiError(http.StatusNotFound, "not_found", "Profile not found")
	}
	if profile.IsPrimary {
		return apiError(http.StatusConflict, "primary_profile_protected", "The primary profile cannot be deleted. Delete the user account instead.")
	}

	if err := store.DeleteProfile(ctx, profileID); err != nil {
		return apiError(http.StatusNotFound, "not_found", "Profile not found")
	}
	if isUploadedAvatarRef(profile.Avatar) {
		if cleanupErr := deleteUploadedAvatarObjects(ctx, h.AvatarStore, userID, profileID); cleanupErr != nil {
			slog.WarnContext(ctx, "profile avatar cleanup failed after delete", "component", "api", "user_id", userID, "profile_id", profileID, "error", cleanupErr)
		}
	}
	if h.DeviceLibraryPurger != nil {
		purgeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if purgeErr := h.DeviceLibraryPurger.PurgeProfileDevices(purgeCtx, userID, profileID); purgeErr != nil {
			slog.WarnContext(ctx, "profile device-library purge failed after delete", "component", "api", "user_id", userID, "profile_id", profileID, "error", purgeErr)
		}
	}
	return nil
}

// HandleVerifyPIN handles POST /profiles/{id}/verify-pin.
func (h *ProfileHandler) HandleVerifyPIN(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	profileID := chi.URLParam(r, "id")
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Profile ID is required")
		return
	}

	var req verifyPINRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.PIN == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "PIN is required")
		return
	}

	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	result, err := h.VerifyPIN(r.Context(), ProfileVerifyPINCommand{
		UserID: userID, SessionID: claims.SessionID, ProfileID: profileID, PIN: req.PIN,
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}

	resp := verifyPINResponse{
		Valid:        result.Valid,
		ProfileToken: result.ProfileToken,
	}
	if !result.ExpiresAt.IsZero() {
		resp.ExpiresAt = result.ExpiresAt.UTC().Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, resp)
}

// ProfileVerifyPINCommand is a PIN check with its request already parsed
// and its caller already reduced to an identity and login session.
type ProfileVerifyPINCommand struct {
	UserID    int
	SessionID string
	ProfileID string
	PIN       string
}

// ProfileVerification is the outcome of a PIN check: whether the PIN
// matched and, when it did and the server can mint one, the profile token
// bound to the caller's login session with its expiry (zero when the token
// does not expire).
type ProfileVerification struct {
	Valid        bool
	ProfileToken string
	ExpiresAt    time.Time
}

// VerifyPIN checks a profile's PIN and, on a match, mints the X-Profile-Token
// the profile gates accept. v1 POST /profiles/{id}/verify-pin and v2
// verifyProfilePIN both call it; a failure is an *APIError carrying the v1
// status, code and message.
func (h *ProfileHandler) VerifyPIN(ctx context.Context, cmd ProfileVerifyPINCommand) (ProfileVerification, error) {
	var none ProfileVerification
	store, err := h.storeProvider.ForUser(ctx, cmd.UserID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}

	valid, err := store.VerifyPIN(ctx, cmd.ProfileID, cmd.PIN)
	if err != nil {
		return none, apiError(http.StatusNotFound, "not_found", "Profile not found or has no PIN")
	}
	if !valid || h.UserRepo == nil || h.ProfileTokens == nil {
		return ProfileVerification{Valid: valid}, nil
	}

	user, err := h.UserRepo.GetByID(ctx, cmd.UserID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to load user policy")
	}

	token, expiresAt, err := h.ProfileTokens.Mint(access.ProfileTokenClaims{
		UserID:         cmd.UserID,
		SessionID:      cmd.SessionID,
		ProfileID:      cmd.ProfileID,
		PolicyRevision: user.AccessPolicyRevision,
	})
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to issue profile token")
	}
	return ProfileVerification{Valid: true, ProfileToken: token, ExpiresAt: expiresAt}, nil
}

// --- Helpers ---

// toProfileResponse serializes one profile, resolving its preference block on
// its own. Callers serializing several profiles must use toProfileResponses
// instead so the whole list costs one store read.
func (h *ProfileHandler) toProfileResponse(
	ctx context.Context, store userstore.UserStore, p userstore.Profile,
) ProfileView {
	prefs := resolveProfilePreferences(ctx, store, []string{p.ID})
	return h.profileResponseWith(ctx, p, prefs[p.ID])
}

// toProfileResponses serializes a whole household, resolving every profile's
// preference block in one store read rather than one per profile.
func (h *ProfileHandler) toProfileResponses(
	ctx context.Context, store userstore.UserStore, profiles []userstore.Profile,
) []ProfileView {
	ids := make([]string, 0, len(profiles))
	for _, p := range profiles {
		ids = append(ids, p.ID)
	}
	prefs := resolveProfilePreferences(ctx, store, ids)

	out := make([]ProfileView, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, h.profileResponseWith(ctx, p, prefs[p.ID]))
	}
	return out
}

// profileResponseWith builds the DTO from a profile row and its already
// resolved preferences.
//
// The preference fields come from prefs rather than from p: those five are
// canonical now, and the legacy columns behind them are written but no longer
// read (see profiles_settings_sync.go). Everything else is still column-backed.
func (h *ProfileHandler) profileResponseWith(
	ctx context.Context, p userstore.Profile, prefs profilePreferences,
) ProfileView {
	avatarSource, avatarURL := resolveProfileAvatar(ctx, h.AvatarStore, h.AvatarTTL, p.Avatar)
	return ProfileView{
		ID:                         p.ID,
		Name:                       p.Name,
		Avatar:                     p.Avatar,
		AvatarURL:                  avatarURL,
		AvatarSource:               avatarSource,
		HasPIN:                     p.PINHash != "",
		IsChild:                    p.IsChild,
		IsPrimary:                  p.IsPrimary,
		MaxContentRating:           p.MaxContentRating,
		QualityPreference:          p.QualityPreference,
		Language:                   prefs.AudioLanguage,
		PreferredMetadataLanguage:  prefs.MetadataLanguage,
		SubtitleLanguage:           prefs.SubtitleLanguage,
		SubtitleMode:               prefs.SubtitleMode,
		AutoSkipIntro:              p.AutoSkipIntro,
		AutoSkipCredits:            p.AutoSkipCredits,
		AutoSkipRecap:              p.AutoSkipRecap,
		AutoPlayNextPreview:        p.AutoPlayNextPreview,
		ShowForcedSubtitles:        prefs.ShowForcedSubtitles,
		LibraryRestrictionsEnabled: p.LibraryRestrictionsEnabled,
		AllowedLibraryIDs:          append([]int(nil), p.AllowedLibraryIDs...),
		MaxPlaybackQuality:         access.NormalizePlaybackQuality(p.MaxPlaybackQuality),
		CreatedAt:                  p.CreatedAt,
		UpdatedAt:                  p.UpdatedAt,
	}
}

// HandleListHouseholdSessions handles GET /profiles/household/sessions.
func (h *ProfileHandler) HandleListHouseholdSessions(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	sessions, err := h.ListHouseholdSessions(r.Context(), HouseholdSessionsQuery{
		UserID:          userID,
		ActiveProfileID: activeProfileIDOf(r),
		VerifyProfile: func(id string) error {
			return verifyProfileToken(r, h.userLookupOrNil(), h.ProfileTokens, id)
		},
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == codeProfileManagement {
			writeProfileManagementPermissionError(w, apiErr.cause)
			return
		}
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

// PlaybackSessionView is one live playback session as the session listings
// serialize it.
type PlaybackSessionView = playbackSessionRow

// HouseholdSessionsQuery is a household session listing with its caller
// already reduced to an identity.
type HouseholdSessionsQuery struct {
	UserID int
	// ActiveProfileID is the profile the caller acts as ("" when none).
	ActiveProfileID string
	// VerifyProfile confirms a PIN-locked primary profile is verified for
	// this request; it returns access.ErrProfileUnverified when it is not.
	VerifyProfile func(profileID string) error
}

// ListHouseholdSessions lists the account's live playback sessions for a
// household manager. v1 GET /profiles/household/sessions and v2
// listHouseholdSessions both call it; a failure is an *APIError carrying
// the v1 status, code and message.
func (h *ProfileHandler) ListHouseholdSessions(ctx context.Context, q HouseholdSessionsQuery) ([]PlaybackSessionView, error) {
	store, err := h.storeProvider.ForUser(ctx, q.UserID)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	allowed, err := canManageHouseholdAs(ctx, store, q.ActiveProfileID, q.VerifyProfile)
	if err != nil {
		return nil, profileManagementError(err)
	}
	if !allowed {
		return nil, apiError(http.StatusForbidden, "forbidden", "Profile management requires the primary profile or admin access")
	}
	if h.SessionsReader == nil {
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Playback sessions are not configured")
	}

	sessions, err := h.SessionsReader.Load(ctx, PlaybackSessionsQuery{UserID: q.UserID})
	if err != nil {
		slog.ErrorContext(ctx, "failed to list household playback sessions", "component", "api", "user_id", q.UserID, "error", err)
		return nil, apiError(http.StatusInternalServerError, "internal_error", "Failed to list playback sessions")
	}
	return sessions, nil
}
