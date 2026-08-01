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

	if posterExtension(header.Header.Get("Content-Type")) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Unsupported image type; use JPEG, PNG, or WebP")
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, profileAvatarMaxFileSize+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to read upload")
		return
	}
	if len(data) > profileAvatarMaxFileSize {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "Avatar must be under 10 MB")
		return
	}

	result, err := imageutil.GenerateSquareVariants(data, []int{256})
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid image file")
		return
	}

	bucket := h.AvatarStore.Bucket()
	originalKey := uploadedAvatarOriginalKey(userID, profileID)
	for _, variant := range result.Variants {
		key := profileAvatarPrefix(userID, profileID) + "/" + variant.Key + result.Ext
		if err := h.AvatarStore.PutObject(r.Context(), bucket, key, variant.Data); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store avatar")
			return
		}
	}

	avatarRef := profileAvatarUploadPrefix + originalKey
	if err := store.UpdateProfile(r.Context(), profileID, userstore.UpdateProfileInput{Avatar: &avatarRef}); err != nil {
		// Uploaded avatar keys are stable per profile, so rolling back by deleting the
		// prefix here can remove the avatar the profile still references after a DB failure.
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save avatar")
		return
	}

	updatedProfile, err := store.GetProfile(r.Context(), profileID)
	if err != nil || updatedProfile == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve updated profile")
		return
	}

	writeJSON(w, http.StatusOK, h.toProfileResponse(r.Context(), store, *updatedProfile))
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

	if isUploadedAvatarRef(profile.Avatar) {
		emptyRef := ""
		if err := store.UpdateProfile(r.Context(), profileID, userstore.UpdateProfileInput{Avatar: &emptyRef}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to clear avatar")
			return
		}
		_ = deleteUploadedAvatarObjects(r.Context(), h.AvatarStore, userID, profileID)
	}

	updatedProfile, err := store.GetProfile(r.Context(), profileID)
	if err != nil || updatedProfile == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve updated profile")
		return
	}

	writeJSON(w, http.StatusOK, h.toProfileResponse(r.Context(), store, *updatedProfile))
}
