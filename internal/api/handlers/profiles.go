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
}

// NewProfileHandler creates a new ProfileHandler.
func NewProfileHandler(provider userstore.UserStoreProvider) *ProfileHandler {
	return &ProfileHandler{
		storeProvider: provider,
		AvatarTTL:     15 * time.Minute,
	}
}

// --- Request/Response types ---

type createProfileRequest struct {
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

type updateProfileRequest struct {
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

type profileResponse struct {
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

type profileListResponse struct {
	Profiles            []profileResponse `json:"profiles"`
	AvatarUploadEnabled bool              `json:"avatar_upload_enabled"`
}

type verifyPINResponse struct {
	Valid        bool   `json:"valid"`
	ProfileToken string `json:"profile_token,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty"`
}

// canManageHouseholdProfiles reports whether the caller may create/update/delete
// profiles belonging to their user. Server admins always can. Otherwise the
// caller's active profile must be the primary profile for the household.
//
// When that primary profile has a PIN, management also requires a valid
// X-Profile-Token from `/profiles/{id}/verify-pin`; otherwise a client could
// bypass the profile lock by sending only X-Profile-Id.
func (h *ProfileHandler) canManageHouseholdProfiles(r *http.Request, store userstore.UserStore) (bool, error) {
	ctx := r.Context()
	if apimw.IsAdmin(ctx) {
		return true, nil
	}
	activeProfileID := apimw.GetProfileID(ctx)
	if activeProfileID == "" {
		activeProfileID = r.Header.Get("X-Profile-Id")
	}
	if activeProfileID == "" {
		return false, nil
	}
	active, err := store.GetProfile(ctx, activeProfileID)
	if err != nil {
		return false, err
	}
	if active == nil {
		return false, nil
	}
	if !active.IsPrimary {
		return false, nil
	}
	if active.PINHash == "" {
		return true, nil
	}
	if err := h.requireVerifiedProfileToken(r, active.ID); err != nil {
		return false, err
	}
	return true, nil
}

func (h *ProfileHandler) requireVerifiedProfileToken(r *http.Request, profileID string) error {
	if h.UserRepo == nil || h.ProfileTokens == nil {
		return access.ErrProfileUnverified
	}

	claims := apimw.GetClaims(r.Context())
	if claims == nil || claims.SessionID == "" {
		return access.ErrProfileUnverified
	}

	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		return access.ErrProfileUnverified
	}

	user, err := h.UserRepo.GetByID(r.Context(), userID)
	if err != nil {
		return fmt.Errorf("loading user policy: %w", err)
	}
	if user == nil {
		return access.ErrProfileUnverified
	}

	profileClaims, err := h.ProfileTokens.Validate(r.Header.Get("X-Profile-Token"))
	if err != nil {
		return err
	}
	if profileClaims.UserID != userID ||
		profileClaims.SessionID != claims.SessionID ||
		profileClaims.ProfileID != profileID ||
		profileClaims.PolicyRevision != user.AccessPolicyRevision {
		return access.ErrProfileUnverified
	}

	return nil
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
func isAllowedSelfServiceProfileUpdate(req updateProfileRequest) bool {
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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	profiles, err := store.ListProfiles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list profiles")
		return
	}

	resp := profileListResponse{
		Profiles:            make([]profileResponse, 0, len(profiles)),
		AvatarUploadEnabled: h.AvatarStore != nil,
	}
	for _, p := range profiles {
		resp.Profiles = append(resp.Profiles, h.toProfileResponse(r.Context(), p))
	}

	writeJSON(w, http.StatusOK, resp)
}

// HandleCreateProfile handles POST /profiles.
func (h *ProfileHandler) HandleCreateProfile(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req createProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Profile name is required")
		return
	}
	avatarRef, err := normalizePresetAvatarReference(req.Avatar)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	maxPlaybackQuality, ok := access.ParsePlaybackQualityPreset(req.MaxPlaybackQuality)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid max_playback_quality")
		return
	}

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	existingProfiles, err := store.ListProfiles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list profiles")
		return
	}
	// The very first profile on a user can be bootstrapped without
	// primary/admin privileges (it becomes the primary); everything after
	// requires either the server admin role or the caller's active profile
	// being primary.
	isBootstrap := len(existingProfiles) == 0
	if !isBootstrap {
		allowed, err := h.canManageHouseholdProfiles(r, store)
		if err != nil {
			writeProfileManagementPermissionError(w, err)
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "forbidden", "Profile management requires the primary profile or admin access")
			return
		}
	}
	// Access-policy fields only make sense when set by a manager on a managed
	// profile. On bootstrap the caller is becoming primary themselves, so non-
	// admin bootstrap creations must leave those fields at their defaults.
	if isBootstrap && !apimw.IsAdmin(r.Context()) &&
		(req.IsChild || req.MaxContentRating != "" ||
			req.LibraryRestrictionsEnabled || len(req.AllowedLibraryIDs) > 0 ||
			req.MaxPlaybackQuality != "") {
		writeError(
			w,
			http.StatusForbidden,
			"forbidden",
			"Profile access settings require the primary profile or admin access",
		)
		return
	}
	if h.UserRepo != nil {
		user, err := h.UserRepo.GetByID(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load user")
			return
		}
		if user != nil && user.MaxProfiles >= 1 && len(existingProfiles) >= user.MaxProfiles {
			writeError(
				w,
				http.StatusConflict,
				"profile_limit_reached",
				fmt.Sprintf("This account has reached its profile limit (%d)", user.MaxProfiles),
			)
			return
		}
	}

	if profileNameConflicts(existingProfiles, req.Name, "") {
		writeError(
			w,
			http.StatusConflict,
			"name_conflict",
			"A profile with this name already exists",
		)
		return
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

	if err := store.CreateProfile(r.Context(), profile); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create profile")
		return
	}

	// Fetch the created profile directly by ID (no race condition).
	createdPtr, err := store.GetProfile(r.Context(), profileID)
	if err != nil || createdPtr == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve created profile")
		return
	}
	created := *createdPtr

	// If PIN was provided, update the profile to set it.
	if req.PIN != "" {
		if err := store.UpdateProfile(r.Context(), created.ID, userstore.UpdateProfileInput{
			PIN: &req.PIN,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to set profile PIN")
			return
		}
		// Re-read the profile to get the updated state.
		p, err := store.GetProfile(r.Context(), created.ID)
		if err != nil || p == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve profile after PIN set")
			return
		}
		created = *p
	}
	if req.ShowForcedSubtitles != nil && !*req.ShowForcedSubtitles {
		if err := store.UpdateProfile(r.Context(), created.ID, userstore.UpdateProfileInput{
			ShowForcedSubtitles: req.ShowForcedSubtitles,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to set forced subtitle preference")
			return
		}
		p, err := store.GetProfile(r.Context(), created.ID)
		if err != nil || p == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve profile after forced subtitle update")
			return
		}
		created = *p
	}

	writeJSON(w, http.StatusCreated, h.toProfileResponse(r.Context(), created))
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

	var req updateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	var avatarRef *string
	if req.Avatar != nil {
		normalized, err := normalizePresetAvatarReference(*req.Avatar)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		avatarRef = &normalized
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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	currentProfile, err := store.GetProfile(r.Context(), profileID)
	if err != nil || currentProfile == nil {
		writeError(w, http.StatusNotFound, "not_found", "Profile not found")
		return
	}

	canManage, err := h.canManageHouseholdProfiles(r, store)
	if err != nil {
		writeProfileManagementPermissionError(w, err)
		return
	}
	if !canManage {
		// Non-managers may only update their own active profile and only a
		// narrow set of playback preferences.
		activeProfileID := apimw.GetProfileID(r.Context())
		if activeProfileID == "" {
			activeProfileID = r.Header.Get("X-Profile-Id")
		}
		if activeProfileID == "" || activeProfileID != profileID {
			writeError(
				w,
				http.StatusForbidden,
				"forbidden",
				"You can only update the active profile's playback preferences",
			)
			return
		}
		if !isAllowedSelfServiceProfileUpdate(req) {
			writeError(
				w,
				http.StatusForbidden,
				"forbidden",
				"Profile access settings require the primary profile or admin access",
			)
			return
		}
	}

	if req.Name != nil {
		// Normalize to the trimmed form up front: the conflict check compares
		// it and the store persists it, so " Laura " never lands verbatim.
		trimmedName := strings.TrimSpace(*req.Name)
		if trimmedName == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "Profile name is required")
			return
		}
		req.Name = &trimmedName
		existingProfiles, err := store.ListProfiles(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list profiles")
			return
		}
		if profileNameConflicts(existingProfiles, *req.Name, profileID) {
			writeError(
				w,
				http.StatusConflict,
				"name_conflict",
				"A profile with this name already exists",
			)
			return
		}
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

	if err := store.UpdateProfile(r.Context(), profileID, input); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Profile not found")
		return
	}
	if currentProfile.Avatar != "" && avatarRef != nil && avatarRefReplacesUpload(currentProfile.Avatar, *avatarRef) {
		if cleanupErr := deleteUploadedAvatarObjects(r.Context(), h.AvatarStore, userID, profileID); cleanupErr != nil {
			slog.Warn("profile avatar cleanup failed after update", "user_id", userID, "profile_id", profileID, "error", cleanupErr)
		}
	}

	// Re-read the profile to return the updated state.
	profile, err := store.GetProfile(r.Context(), profileID)
	if err != nil || profile == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve updated profile")
		return
	}

	writeJSON(w, http.StatusOK, h.toProfileResponse(r.Context(), *profile))
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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	allowed, err := h.canManageHouseholdProfiles(r, store)
	if err != nil {
		writeProfileManagementPermissionError(w, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "Profile management requires the primary profile or admin access")
		return
	}
	profile, err := store.GetProfile(r.Context(), profileID)
	if err != nil || profile == nil {
		writeError(w, http.StatusNotFound, "not_found", "Profile not found")
		return
	}
	if profile.IsPrimary {
		writeError(
			w,
			http.StatusConflict,
			"primary_profile_protected",
			"The primary profile cannot be deleted. Delete the user account instead.",
		)
		return
	}

	if err := store.DeleteProfile(r.Context(), profileID); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Profile not found")
		return
	}
	if isUploadedAvatarRef(profile.Avatar) {
		if cleanupErr := deleteUploadedAvatarObjects(r.Context(), h.AvatarStore, userID, profileID); cleanupErr != nil {
			slog.Warn("profile avatar cleanup failed after delete", "user_id", userID, "profile_id", profileID, "error", cleanupErr)
		}
	}
	if h.DeviceLibraryPurger != nil {
		purgeCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		defer cancel()
		if purgeErr := h.DeviceLibraryPurger.PurgeProfileDevices(purgeCtx, userID, profileID); purgeErr != nil {
			slog.Warn("profile device-library purge failed after delete", "user_id", userID, "profile_id", profileID, "error", purgeErr)
		}
	}

	w.WriteHeader(http.StatusNoContent)
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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}

	valid, err := store.VerifyPIN(r.Context(), profileID, req.PIN)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Profile not found or has no PIN")
		return
	}
	if !valid || h.UserRepo == nil || h.ProfileTokens == nil {
		writeJSON(w, http.StatusOK, verifyPINResponse{Valid: valid})
		return
	}

	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	user, err := h.UserRepo.GetByID(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load user policy")
		return
	}

	token, expiresAt, err := h.ProfileTokens.Mint(access.ProfileTokenClaims{
		UserID:         userID,
		SessionID:      claims.SessionID,
		ProfileID:      profileID,
		PolicyRevision: user.AccessPolicyRevision,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to issue profile token")
		return
	}

	resp := verifyPINResponse{
		Valid:        true,
		ProfileToken: token,
	}
	if !expiresAt.IsZero() {
		resp.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, resp)
}

// --- Helpers ---

func (h *ProfileHandler) toProfileResponse(ctx context.Context, p userstore.Profile) profileResponse {
	avatarSource, avatarURL := resolveProfileAvatar(ctx, h.AvatarStore, h.AvatarTTL, p.Avatar)
	return profileResponse{
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
		Language:                   p.Language,
		PreferredMetadataLanguage:  p.PreferredMetadataLanguage,
		SubtitleLanguage:           p.SubtitleLanguage,
		SubtitleMode:               p.SubtitleMode,
		AutoSkipIntro:              p.AutoSkipIntro,
		AutoSkipCredits:            p.AutoSkipCredits,
		AutoSkipRecap:              p.AutoSkipRecap,
		AutoPlayNextPreview:        p.AutoPlayNextPreview,
		ShowForcedSubtitles:        p.ShowForcedSubtitles,
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

	store, err := h.storeProvider.ForUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to access user store")
		return
	}
	allowed, err := h.canManageHouseholdProfiles(r, store)
	if err != nil {
		writeProfileManagementPermissionError(w, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "forbidden", "Profile management requires the primary profile or admin access")
		return
	}
	if h.SessionsReader == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Playback sessions are not configured")
		return
	}

	sessions, err := h.SessionsReader.Load(r.Context(), r, PlaybackSessionsQuery{UserID: userID})
	if err != nil {
		slog.Error("failed to list household playback sessions", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list playback sessions")
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}
