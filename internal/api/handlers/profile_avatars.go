package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkupload"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	profileAvatarPresetPrefix = "preset:"
	// profileAvatarUploadPrefix marks a stored avatar object. The value after
	// it is a logical artwork key.
	//
	// The artwork revision collector recognizes the same prefix when it decides
	// whether an avatar object is still referenced
	// (metadata.profileAvatarReferenceSurface). Changing it here without
	// changing it there would make live avatars collectable.
	profileAvatarUploadPrefix = "upload:"
	profileAvatarMaxFileSize  = 10 << 20

	// avatarDisplayVariant is the ladder entry clients actually render.
	avatarDisplayVariant = "w256"

	// Avatar source labels reported to clients alongside the resolved URL.
	avatarSourceUpload = "upload"
	avatarSourcePreset = "preset"
	avatarSourceNone   = "none"
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

// profileAvatarStore is the legacy per-profile avatar bucket. New avatars are
// materialized into the canonical artwork store instead; this interface stays
// so avatars uploaded before that change keep resolving and keep being cleaned
// up when their profile replaces or deletes them.
type profileAvatarStore interface {
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

// resolveProfileAvatar turns a stored avatar reference into a source label and
// a URL a client can load.
//
// Uploaded avatars live in one of two places. Avatars stored since profile
// uploads became content-addressed are objects in the canonical artwork store
// and resolve through the artwork resolver, so they work on every backend.
// Anything older is a mutable per-profile object in the legacy avatar bucket
// and keeps its presigned URL until the profile next replaces it.
func resolveProfileAvatar(
	ctx context.Context,
	resolver ArtworkURLResolver,
	legacy profileAvatarStore,
	ttl time.Duration,
	ref string,
) (source string, url string) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return avatarSourceNone, ""
	}
	if strings.HasPrefix(trimmed, profileAvatarPresetPrefix) {
		presetID := strings.TrimPrefix(trimmed, profileAvatarPresetPrefix)
		if !isKnownPresetAvatarID(presetID) {
			return avatarSourceNone, ""
		}
		return avatarSourcePreset, bundledProfileAvatarURL(presetID)
	}
	if strings.HasPrefix(trimmed, profileAvatarUploadPrefix) {
		displayKey := uploadedAvatarDisplayKey(strings.TrimPrefix(trimmed, profileAvatarUploadPrefix))
		if artworkkey.IsPortableKey(displayKey) {
			return avatarSourceUpload, resolveStoredImageURL(ctx, resolver, displayKey)
		}
		if legacy == nil {
			return avatarSourceUpload, ""
		}
		presignTTL := ttl
		if presignTTL <= 0 {
			presignTTL = 15 * time.Minute
		}
		presignedURL, err := legacy.PresignGetURL(ctx, legacy.Bucket(), displayKey, presignTTL)
		if err != nil {
			return avatarSourceUpload, ""
		}
		return avatarSourceUpload, presignedURL
	}
	if isKnownPresetAvatarID(trimmed) {
		return avatarSourcePreset, bundledProfileAvatarURL(trimmed)
	}
	return avatarSourceNone, ""
}

func resolveProfileAvatarTarget(
	ctx context.Context,
	resolver ArtworkURLResolver,
	legacy profileAvatarStore,
	ttl time.Duration,
	userID int,
	profileID string,
	ref string,
) (source string, url string) {
	trimmed := strings.TrimSpace(ref)
	if strings.HasPrefix(trimmed, profileAvatarUploadPrefix) {
		originalKey := strings.TrimPrefix(trimmed, profileAvatarUploadPrefix)
		if artworkkey.IsPortableKey(originalKey) {
			return avatarSourceUpload, resolveTargetStoredImageURL(ctx, resolver, artworkurl.Target{
				Surface: artworkurl.SurfaceProfileAvatars,
				Keys:    []string{strconv.Itoa(userID), profileID},
				Slot:    "avatar",
			}, originalKey, avatarDisplayVariant)
		}
	}
	return resolveProfileAvatar(ctx, resolver, legacy, ttl, ref)
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

// profileAvatarPrefix is the legacy per-profile object prefix. Nothing writes
// there any more; it is the sweep target for avatars uploaded before profile
// avatars became content-addressed.
func profileAvatarPrefix(userID int, profileID string) string {
	return fmt.Sprintf("profile-avatars/%d/%s", userID, profileID)
}

// uploadedAvatarDisplayKey maps an avatar's original-variant key to the sized
// variant clients render. It serves both the portable and the legacy grammar.
func uploadedAvatarDisplayKey(originalKey string) string {
	originalKey = strings.TrimSpace(originalKey)
	if display := artworkkey.Variant(originalKey, avatarDisplayVariant); display != originalKey {
		return display
	}
	// Legacy references that stored the prefix rather than the original object.
	return strings.TrimRight(originalKey, "/") + "/" + avatarDisplayVariant + ".webp"
}

// deleteUploadedAvatarObjects removes the legacy mutable avatar objects for a
// profile. Content-addressed avatar revisions are deliberately left alone: one
// revision can back several profiles, so when it stops being needed is the
// artwork collector's decision, not this handler's.
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
	if !h.ArtworkUploads.Available() {
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

	// Square variants, addressed by content, registered for collection before
	// the first byte lands — but only when the profile row lives where the
	// collector can see it (see TrackArtworkRevisions). The profile row is
	// repointed afterwards, so a failure below leaves the previous avatar
	// intact and a tracked revision is reclaimed once its grace period expires.
	stored, err := h.ArtworkUploads.Materialize(r.Context(), artworkupload.Request{
		ImageType: artworkkey.ImageTypeAvatar,
		Data:      data,
		Square:    true,
		Track:     h.TrackArtworkRevisions,
	})
	switch {
	case err == nil:
	case errors.Is(err, artworkupload.ErrInvalidImage):
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid image file")
		return
	case errors.Is(err, artworkupload.ErrStorageUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Avatar upload storage is not configured")
		return
	default:
		slog.ErrorContext(r.Context(), "storing profile avatar", "component", "api",
			"user_id", userID, "profile_id", profileID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to store avatar")
		return
	}

	previousRef := profile.Avatar
	avatarRef := profileAvatarUploadPrefix + stored.OriginalKey
	if err := store.UpdateProfile(r.Context(), profileID, userstore.UpdateProfileInput{Avatar: &avatarRef}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save avatar")
		return
	}
	// Only the legacy mutable objects need deleting by hand; a displaced
	// content-addressed revision is collected by reference.
	if avatarRefReplacesUpload(previousRef, avatarRef) {
		if cleanupErr := deleteUploadedAvatarObjects(r.Context(), h.AvatarStore, userID, profileID); cleanupErr != nil {
			slog.WarnContext(r.Context(), "legacy profile avatar cleanup failed after upload", "component", "api",
				"user_id", userID, "profile_id", profileID, "error", cleanupErr)
		}
	}

	updatedProfile, err := store.GetProfile(r.Context(), profileID)
	if err != nil || updatedProfile == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to retrieve updated profile")
		return
	}

	writeJSON(w, http.StatusOK, h.toProfileResponse(r.Context(), store, userID, *updatedProfile))
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

	writeJSON(w, http.StatusOK, h.toProfileResponse(r.Context(), store, userID, *updatedProfile))
}
