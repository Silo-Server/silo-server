package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	profileAvatarPresetPrefix = "preset:"
	profileAvatarUploadPrefix = "upload:"
	profileAvatarMaxFileSize  = 10 << 20
)

var legacyProfileAvatarIDs = map[string]struct{}{
	"avatar-1": {},
	"avatar-2": {},
	"avatar-3": {},
	"avatar-4": {},
	"avatar-5": {},
	"avatar-6": {},
	"avatar-7": {},
	"avatar-8": {},
}

var supportedDiceBearAvatarStyles = map[string]struct{}{
	"identicon":         {},
	"initials":          {},
	"bottts-neutral":    {},
	"fun-emoji":         {},
	"pixel-art-neutral": {},
}

type profileAvatarStore interface {
	PutObject(ctx context.Context, bucket, key string, data []byte) error
	DeleteObject(ctx context.Context, bucket, key string) error
	ListObjects(ctx context.Context, bucket, prefix string) ([]string, error)
	PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error)
	Bucket() string
}

func normalizePresetAvatarReference(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, profileAvatarUploadPrefix) {
		return "", fmt.Errorf("custom upload avatar references are not allowed in JSON profile updates")
	}
	if strings.HasPrefix(value, profileAvatarPresetPrefix) {
		presetID := strings.TrimPrefix(value, profileAvatarPresetPrefix)
		if !isKnownPresetAvatarID(presetID) {
			return "", fmt.Errorf("unknown avatar preset")
		}
		return profileAvatarPresetPrefix + presetID, nil
	}
	if !isKnownPresetAvatarID(value) {
		return "", fmt.Errorf("unknown avatar preset")
	}
	return profileAvatarPresetPrefix + value, nil
}

func isKnownPresetAvatarID(id string) bool {
	if _, ok := legacyProfileAvatarIDs[id]; ok {
		return true
	}
	_, _, ok := parseDiceBearPresetID(id)
	return ok
}

func isUploadedAvatarRef(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), profileAvatarUploadPrefix)
}

func avatarRefReplacesUpload(currentRef, nextRef string) bool {
	return isUploadedAvatarRef(currentRef) && strings.TrimSpace(currentRef) != strings.TrimSpace(nextRef)
}

func resolveProfileAvatar(ctx context.Context, store profileAvatarStore, ttl time.Duration, ref string) (source string, url string) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "none", ""
	}
	if strings.HasPrefix(trimmed, profileAvatarPresetPrefix) {
		presetID := strings.TrimPrefix(trimmed, profileAvatarPresetPrefix)
		if !isKnownPresetAvatarID(presetID) {
			return "none", ""
		}
		return "preset", bundledProfileAvatarURL(presetID)
	}
	if strings.HasPrefix(trimmed, profileAvatarUploadPrefix) {
		if store == nil {
			return "upload", ""
		}
		displayKey := uploadedAvatarDisplayKey(strings.TrimPrefix(trimmed, profileAvatarUploadPrefix))
		presignTTL := ttl
		if presignTTL <= 0 {
			presignTTL = 15 * time.Minute
		}
		presignedURL, err := store.PresignGetURL(ctx, store.Bucket(), displayKey, presignTTL)
		if err != nil {
			return "upload", ""
		}
		return "upload", presignedURL
	}
	if isKnownPresetAvatarID(trimmed) {
		return "preset", bundledProfileAvatarURL(trimmed)
	}
	return "none", ""
}

func bundledProfileAvatarURL(id string) string {
	if style, seed, ok := parseDiceBearPresetID(id); ok {
		return diceBearAvatarURL(style, seed)
	}
	return "/profile-avatars/" + id + ".svg"
}

func parseDiceBearPresetID(id string) (style string, seed string, ok bool) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 || parts[0] != "dicebear" {
		return "", "", false
	}
	style = strings.TrimSpace(parts[1])
	seed = strings.TrimSpace(parts[2])
	if _, supported := supportedDiceBearAvatarStyles[style]; !supported {
		return "", "", false
	}
	if !isSafeAvatarSeed(seed) {
		return "", "", false
	}
	return style, seed, true
}

func isSafeAvatarSeed(seed string) bool {
	if seed == "" || len(seed) > 64 {
		return false
	}
	for _, char := range seed {
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '-':
		default:
			return false
		}
	}
	return true
}

func diceBearAvatarURL(style string, seed string) string {
	query := url.Values{}
	query.Set("seed", seed)
	query.Set("size", "128")
	query.Set("radius", "24")
	query.Set("backgroundType", "gradientLinear")
	return "https://api.dicebear.com/9.x/" + style + "/svg?" + query.Encode()
}

func profileAvatarPrefix(userID int, profileID string) string {
	return fmt.Sprintf("profile-avatars/%d/%s", userID, profileID)
}

func uploadedAvatarOriginalKey(userID int, profileID string) string {
	return profileAvatarPrefix(userID, profileID) + "/original.webp"
}

func uploadedAvatarDisplayKey(originalKey string) string {
	if strings.HasSuffix(originalKey, "/original.webp") {
		return strings.TrimSuffix(originalKey, "/original.webp") + "/w256.webp"
	}
	return strings.TrimRight(originalKey, "/") + "/w256.webp"
}

func deleteUploadedAvatarObjects(ctx context.Context, store profileAvatarStore, userID int, profileID string) error {
	if store == nil {
		return nil
	}
	keys, err := store.ListObjects(ctx, store.Bucket(), profileAvatarPrefix(userID, profileID)+"/")
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := store.DeleteObject(ctx, store.Bucket(), key); err != nil {
			return err
		}
	}
	return nil
}

func (h *ProfileHandler) HandleUploadAvatar(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h.AvatarStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Avatar upload storage is not configured")
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
	profile, err := store.GetProfile(r.Context(), profileID)
	if err != nil || profile == nil {
		writeError(w, http.StatusNotFound, "not_found", "Profile not found")
		return
	}

	if err := r.ParseMultipartForm(profileAvatarMaxFileSize); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid multipart form")
		return
	}

	file, header, err := r.FormFile("avatar")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Missing avatar file")
		return
	}
	defer file.Close()

	view, err := h.UploadAvatar(r.Context(), ProfileAvatarUpload{
		UserID: userID, ProfileID: profileID, ContentType: header.Header.Get("Content-Type"), File: file,
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// ProfileAvatarUpload is an avatar upload with its multipart part already
// extracted and its caller already reduced to an identity.
type ProfileAvatarUpload struct {
	UserID    int
	ProfileID string
	// ContentType is the part's declared media type; JPEG, PNG and WebP are
	// accepted.
	ContentType string
	File        io.Reader
}

// UploadAvatar stores a profile's uploaded avatar: the store guard, the
// profile lookup, the media-type and size checks, the resized variants, the
// object writes and the profile reference. v1 PUT /profiles/{id}/avatar and
// v2 uploadProfileAvatar both call it; a failure is an *APIError carrying
// the v1 status, code and message. The caller must have checked that the
// upload store is configured (AvatarUploadEnabled) so a missing store is a
// 503 rather than a bug.
func (h *ProfileHandler) UploadAvatar(ctx context.Context, up ProfileAvatarUpload) (ProfileView, error) {
	var none ProfileView
	if h.AvatarStore == nil {
		return none, apiError(http.StatusServiceUnavailable, "unavailable", "Avatar upload storage is not configured")
	}
	userID, profileID := up.UserID, up.ProfileID
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil || profile == nil {
		return none, apiError(http.StatusNotFound, "not_found", "Profile not found")
	}

	if posterExtension(up.ContentType) == "" {
		return none, apiError(http.StatusBadRequest, "bad_request", "Unsupported image type; use JPEG, PNG, or WebP")
	}

	data, err := io.ReadAll(io.LimitReader(up.File, profileAvatarMaxFileSize+1))
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to read upload")
	}
	if len(data) > profileAvatarMaxFileSize {
		return none, apiError(http.StatusRequestEntityTooLarge, "too_large", "Avatar must be under 10 MB")
	}

	result, err := imageutil.GenerateSquareVariants(data, []int{256})
	if err != nil {
		return none, apiError(http.StatusBadRequest, "bad_request", "Invalid image file")
	}

	bucket := h.AvatarStore.Bucket()
	originalKey := uploadedAvatarOriginalKey(userID, profileID)
	for _, variant := range result.Variants {
		key := profileAvatarPrefix(userID, profileID) + "/" + variant.Key + result.Ext
		if err := h.AvatarStore.PutObject(ctx, bucket, key, variant.Data); err != nil {
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to store avatar")
		}
	}

	avatarRef := profileAvatarUploadPrefix + originalKey
	if err := store.UpdateProfile(ctx, profileID, userstore.UpdateProfileInput{Avatar: &avatarRef}); err != nil {
		// Uploaded avatar keys are stable per profile, so rolling back by deleting the
		// prefix here can remove the avatar the profile still references after a DB failure.
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to save avatar")
	}

	updatedProfile, err := store.GetProfile(ctx, profileID)
	if err != nil || updatedProfile == nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to retrieve updated profile")
	}
	return h.toProfileResponse(ctx, store, *updatedProfile), nil
}

func (h *ProfileHandler) HandleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
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

	view, err := h.DeleteAvatar(r.Context(), userID, profileID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// DeleteAvatar clears a profile's uploaded avatar (a preset reference is
// left alone) and removes its objects. v1 DELETE /profiles/{id}/avatar and
// v2 deleteProfileAvatar both call it; a failure is an *APIError carrying
// the v1 status, code and message.
func (h *ProfileHandler) DeleteAvatar(ctx context.Context, userID int, profileID string) (ProfileView, error) {
	var none ProfileView
	store, err := h.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to access user store")
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil || profile == nil {
		return none, apiError(http.StatusNotFound, "not_found", "Profile not found")
	}

	if isUploadedAvatarRef(profile.Avatar) {
		emptyRef := ""
		if err := store.UpdateProfile(ctx, profileID, userstore.UpdateProfileInput{Avatar: &emptyRef}); err != nil {
			return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to clear avatar")
		}
		_ = deleteUploadedAvatarObjects(ctx, h.AvatarStore, userID, profileID)
	}

	updatedProfile, err := store.GetProfile(ctx, profileID)
	if err != nil || updatedProfile == nil {
		return none, apiError(http.StatusInternalServerError, "internal_error", "Failed to retrieve updated profile")
	}
	return h.toProfileResponse(ctx, store, *updatedProfile), nil
}
