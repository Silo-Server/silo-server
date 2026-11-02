package jellycompat

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// compatUserID formats a pseudo-user id as Jellyfin serializes a Guid in JSON:
// 32 lowercase hex digits without dashes. Clients that validate the shape
// (Seerr's avatar proxy accepts only /^[a-f0-9]{32}$/) reject the dashed form.
// Only user ids take this form; item and server ids keep theirs, because
// clients store those and a format change would orphan the stored copies.
func compatUserID(id uuid.UUID) string {
	return hex.EncodeToString(id[:])
}

// isPseudoUser reports whether raw names id. It accepts the 32-digit form
// Silo now emits and the dashed form it emitted before, which clients that
// signed in earlier still send in routes and queries.
func isPseudoUser(raw string, id uuid.UUID) bool {
	parsed, err := uuid.Parse(strings.TrimSpace(raw))
	return err == nil && parsed == id
}

// pseudoUserOwner is the account and household profile a pseudo-user id was
// derived from (see PseudoUserID).
type pseudoUserOwner struct {
	UserID    int
	ProfileID string
}

// pseudoUserTokenLookup is implemented by session persistence that can find
// the sessions of a pseudo-user id across replicas.
type pseudoUserTokenLookup interface {
	PseudoUserTokens(ctx context.Context, id uuid.UUID, now time.Time, limit int) ([]string, error)
}

// maxUserAvatarBytes bounds the display variant read into memory. Uploads are
// re-encoded to a 256px WebP, a few tens of kilobytes.
const maxUserAvatarBytes = 2 << 20

// userAvatarCacheControl lets clients and proxies keep an avatar briefly.
// Clients that carry the PrimaryImageTag in the URL pick up a new upload at
// once; the rest within the hour.
const userAvatarCacheControl = "public, max-age=3600"

// compatUserAvatars serves a Jellyfin user's avatar. A Jellyfin user is a
// Silo household profile (the pseudo-user id hashes the account and profile),
// so its avatar is that profile's uploaded avatar. Preset avatars are SVGs
// most Jellyfin clients cannot draw, so they are not offered; such a user has
// no PrimaryImageTag and its image route answers 404, as Jellyfin answers for
// a user without an image.
type compatUserAvatars struct {
	owners *SessionStore
	stores userstore.UserStoreProvider
	blobs  blobstore.Store
}

func newCompatUserAvatars(owners *SessionStore, stores userstore.UserStoreProvider, blobs blobstore.Store) *compatUserAvatars {
	if stores == nil || blobs == nil {
		return nil
	}
	return &compatUserAvatars{owners: owners, stores: stores, blobs: blobs}
}

// primaryImageTag returns the PrimaryImageTag for a profile, or "" when it has
// no avatar this listener serves. The tag changes with the profile row, so a
// new upload under the same blob key still busts client caches.
func (a *compatUserAvatars) primaryImageTag(profile *userstore.Profile) string {
	if a == nil || profile == nil {
		return ""
	}
	key, ok := userstore.UploadedAvatarDisplayKey(profile.Avatar)
	if !ok {
		return ""
	}
	return tagValue("user-avatar:" + key + ":" + profile.UpdatedAt)
}

// errNoUserAvatar reports a pseudo-user with no servable avatar: unknown id,
// deleted profile, or no uploaded image.
var errNoUserAvatar = errors.New("user avatar not found")

type userAvatarImage struct {
	data        []byte
	contentType string
	modTime     time.Time
}

func (a *compatUserAvatars) load(ctx context.Context, id uuid.UUID) (userAvatarImage, error) {
	var none userAvatarImage
	if a == nil || a.owners == nil {
		return none, errNoUserAvatar
	}
	owner, err := a.owners.PseudoUserOwner(ctx, id)
	if errors.Is(err, ErrSessionNotFound) {
		return none, errNoUserAvatar
	}
	if err != nil {
		return none, err
	}
	store, err := a.stores.ForUser(ctx, owner.UserID)
	if err != nil {
		return none, fmt.Errorf("open user store: %w", err)
	}
	profile, err := store.GetProfile(ctx, owner.ProfileID)
	if err != nil {
		return none, fmt.Errorf("load profile: %w", err)
	}
	if profile == nil {
		return none, errNoUserAvatar
	}
	key, ok := userstore.UploadedAvatarDisplayKey(profile.Avatar)
	if !ok {
		return none, errNoUserAvatar
	}
	body, info, err := a.blobs.Get(ctx, key)
	if errors.Is(err, blobstore.ErrNotFound) {
		return none, errNoUserAvatar
	}
	if err != nil {
		return none, fmt.Errorf("read avatar: %w", err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, maxUserAvatarBytes+1))
	if err != nil {
		return none, fmt.Errorf("read avatar: %w", err)
	}
	if len(data) > maxUserAvatarBytes {
		return none, fmt.Errorf("avatar %s exceeds %d bytes", key, maxUserAvatarBytes)
	}
	contentType := mime.TypeByExtension(path.Ext(key))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	return userAvatarImage{data: data, contentType: contentType, modTime: info.ModTime}, nil
}

// HandleUserImage serves a Jellyfin user's avatar.
//
// Jellyfin user-image GETs are anonymous: clients fetch avatars through plain
// image loaders that carry no auth, and Seerr's avatar proxy fetches
// /UserImage?UserId= server-side. The pseudo-user id is resolved through the
// compat session rows, so any replica can answer. A user without an uploaded
// avatar answers 404, which clients and Seerr treat as "draw the default".
func (h *ImagesHandler) HandleUserImage(w http.ResponseWriter, r *http.Request) {
	raw := chiURLParam(r, "id")
	if raw == "" {
		// The modern /UserImage route carries the id as a query parameter.
		raw = newCaseInsensitiveQuery(r.URL.Query()).Get("userId")
	}
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "User image not found")
		return
	}
	image, err := h.userAvatars.load(r.Context(), id)
	if errors.Is(err, errNoUserAvatar) {
		writeError(w, http.StatusNotFound, "NotFound", "User image not found")
		return
	}
	if err != nil {
		slog.WarnContext(r.Context(), "jellycompat user avatar load failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "ServiceUnavailable", "User image unavailable")
		return
	}
	w.Header().Set("Content-Type", image.contentType)
	w.Header().Set("Cache-Control", userAvatarCacheControl)
	http.ServeContent(w, r, "", image.modTime, bytes.NewReader(image.data))
}
