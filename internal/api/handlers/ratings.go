package handlers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// ratingsRepository defines the data access interface for user ratings.
type ratingsRepository interface {
	Set(ctx context.Context, userID int, profileID, mediaItemID string, rating int) error
	Get(ctx context.Context, userID int, profileID, mediaItemID string) (*catalog.UserRating, error)
	Delete(ctx context.Context, userID int, profileID, mediaItemID string) error
	List(ctx context.Context, userID int, profileID string, limit, offset int) ([]catalog.UserRating, error)
	ListCommunity(ctx context.Context, viewerUserID int, viewerProfileID, mediaItemID string, limit, offset int) ([]catalog.CommunityRating, error)
	SetCommunityReaction(ctx context.Context, viewerUserID int, viewerProfileID, mediaItemID string, targetUserID int, targetProfileID string, reaction int) (bool, error)
	DeleteCommunityReaction(ctx context.Context, viewerUserID int, viewerProfileID, mediaItemID string, targetUserID int, targetProfileID string) error
}

// RatingsHandler handles user rating operations.
type RatingsHandler struct {
	ratingsRepo             ratingsRepository
	itemRepo                personalDataItemRepository
	profileStaler           ProfileStaler
	profileRefreshRequester ProfileRefreshRequester
	AvatarStore             profileAvatarStore
	AvatarTTL               time.Duration
	AvatarTokenSecret       []byte
}

// NewRatingsHandler creates a new RatingsHandler.
func NewRatingsHandler(ratingsRepo ratingsRepository, itemRepo personalDataItemRepository) *RatingsHandler {
	return &RatingsHandler{ratingsRepo: ratingsRepo, itemRepo: itemRepo}
}

// SetProfileStaler configures an optional staleness trigger for taste profiles.
func (h *RatingsHandler) SetProfileStaler(ps ProfileStaler) {
	h.profileStaler = ps
}

// SetProfileRefreshRequester configures an optional background refresh queue for taste profiles.
func (h *RatingsHandler) SetProfileRefreshRequester(requester ProfileRefreshRequester) {
	h.profileRefreshRequester = requester
}

func (h *RatingsHandler) markStale(ctx context.Context, userID int, profileID string) {
	triggerProfileRefresh(ctx, h.profileStaler, h.profileRefreshRequester, userID, profileID)
}

// --- Response types ---

type ratingResponse struct {
	Rating  int    `json:"rating"`
	RatedAt string `json:"rated_at"`
}

type ratingListItem struct {
	MediaItemID string `json:"media_item_id"`
	Rating      int    `json:"rating"`
	RatedAt     string `json:"rated_at"`
}

type ratingListResponse struct {
	Ratings []ratingListItem `json:"ratings"`
}

type communityRatingListItem struct {
	Key            string `json:"key"`
	DisplayName    string `json:"display_name"`
	AvatarURL      string `json:"avatar_url,omitempty"`
	Rating         int    `json:"rating"`
	RatedAt        string `json:"rated_at"`
	UpCount        int    `json:"up_count"`
	DownCount      int    `json:"down_count"`
	ViewerReaction string `json:"viewer_reaction,omitempty"`
	IsViewer       bool   `json:"is_viewer"`
}

type communityRatingsResponse struct {
	AverageRating *float64                  `json:"average_rating"`
	VoteCount     int                       `json:"vote_count"`
	Ratings       []communityRatingListItem `json:"ratings"`
}

// --- Request types ---

type setRatingRequest struct {
	Rating int `json:"rating"`
}

type setCommunityRatingReactionRequest struct {
	Reaction string `json:"reaction"`
}

type communityAvatarTokenPayload struct {
	ObjectKey string `json:"o"`
	Version   int64  `json:"v"`
	ExpiresAt int64  `json:"e"`
}

// HandleSetRating handles PUT /ratings/{item_id}.
// Accepts {"rating": N} where N is 1-5. Returns 204 on success.
func (h *RatingsHandler) HandleSetRating(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	var req setRatingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if req.Rating < 1 || req.Rating > 5 {
		writeError(w, http.StatusBadRequest, "bad_request", "Rating must be between 1 and 5")
		return
	}

	if err := h.itemRepo.EnsureAccessible(r.Context(), itemID, requestAccessFilter(r)); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	if err := h.ratingsRepo.Set(r.Context(), userID, profileID, itemID, req.Rating); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to set rating")
		return
	}

	h.markStale(r.Context(), userID, profileID)
	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteRating handles DELETE /ratings/{item_id}.
// Returns 204 on success.
func (h *RatingsHandler) HandleDeleteRating(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	if err := h.ratingsRepo.Delete(r.Context(), userID, profileID, itemID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete rating")
		return
	}

	h.markStale(r.Context(), userID, profileID)
	w.WriteHeader(http.StatusNoContent)
}

// HandleGetRating handles GET /ratings/{item_id}.
// Returns the rating or 404 if not found.
func (h *RatingsHandler) HandleGetRating(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")

	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}

	rating, err := h.ratingsRepo.Get(r.Context(), userID, profileID, itemID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get rating")
		return
	}

	if rating == nil {
		writeError(w, http.StatusNotFound, "not_found", "Rating not found")
		return
	}

	writeJSON(w, http.StatusOK, ratingResponse{
		Rating:  rating.Rating,
		RatedAt: rating.RatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

// HandleListRatings handles GET /ratings/.
// Returns paginated ratings for the current user+profile.
func (h *RatingsHandler) HandleListRatings(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	limit, offset := parsePagination(r)

	ratings, err := h.ratingsRepo.List(r.Context(), userID, profileID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list ratings")
		return
	}

	items := make([]ratingListItem, 0, len(ratings))
	for _, ur := range ratings {
		items = append(items, ratingListItem{
			MediaItemID: ur.MediaItemID,
			Rating:      ur.Rating,
			RatedAt:     ur.RatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	writeJSON(w, http.StatusOK, ratingListResponse{Ratings: items})
}

// HandleCapabilities advertises the optional server-local rating surface without
// changing the existing personal-rating contract used by older clients.
func (h *RatingsHandler) HandleCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"community_ratings":          true,
		"community_rating_reactions": true,
	})
}

// HandleListCommunityRatings handles GET /ratings/{item_id}/community.
func (h *RatingsHandler) HandleListCommunityRatings(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	itemID := chi.URLParam(r, "item_id")
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID is required")
		return
	}
	if err := h.itemRepo.EnsureAccessible(r.Context(), itemID, requestAccessFilter(r)); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return
	}

	limit, _ := parsePagination(r)
	if limit > 100 {
		limit = 100
	}
	ratings, err := h.ratingsRepo.ListCommunity(r.Context(), userID, profileID, itemID, limit, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list community ratings")
		return
	}

	items := make([]communityRatingListItem, 0, len(ratings))
	for _, rating := range ratings {
		avatarURL := h.communityAvatarURL(r.Context(), rating)
		items = append(items, communityRatingListItem{
			Key:            communityRatingKey(itemID, rating),
			DisplayName:    abbreviateProfileName(rating.ProfileName),
			AvatarURL:      avatarURL,
			Rating:         rating.Rating,
			RatedAt:        rating.RatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			UpCount:        rating.UpCount,
			DownCount:      rating.DownCount,
			ViewerReaction: communityReactionName(rating.ViewerReaction),
			IsViewer:       rating.UserID == userID && rating.ProfileID == profileID,
		})
	}

	var average *float64
	voteCount := 0
	if len(ratings) > 0 {
		value := ratings[0].AverageRating
		average = &value
		voteCount = ratings[0].TotalVoteCount
	}
	writeJSON(w, http.StatusOK, communityRatingsResponse{
		AverageRating: average,
		VoteCount:     voteCount,
		Ratings:       items,
	})
}

// HandleCommunityRatingAvatar serves an uploaded avatar through a short-lived,
// opaque capability. It keeps storage keys (which contain account/profile IDs)
// out of community API responses while still allowing an ordinary <img> tag to
// load the asset without copying account credentials into a URL.
func (h *RatingsHandler) HandleCommunityRatingAvatar(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.parseCommunityAvatarToken(chi.URLParam(r, "token"))
	if !ok || h.AvatarStore == nil {
		http.NotFound(w, r)
		return
	}
	data, err := h.AvatarStore.GetObject(r.Context(), h.AvatarStore.Bucket(), payload.ObjectKey)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=900")
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// HandleSetCommunityRatingReaction handles
// PUT /ratings/{item_id}/community/{rating_key}/reaction.
func (h *RatingsHandler) HandleSetCommunityRatingReaction(w http.ResponseWriter, r *http.Request) {
	var req setCommunityRatingReactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	var reaction int
	switch strings.ToLower(strings.TrimSpace(req.Reaction)) {
	case "up":
		reaction = 1
	case "down":
		reaction = -1
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "Reaction must be up or down")
		return
	}

	target, ok := h.communityReactionTarget(w, r)
	if !ok {
		return
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	inserted, err := h.ratingsRepo.SetCommunityReaction(
		r.Context(),
		userID,
		profileID,
		chi.URLParam(r, "item_id"),
		target.UserID,
		target.ProfileID,
		reaction,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to set rating reaction")
		return
	}
	if !inserted {
		writeError(w, http.StatusNotFound, "not_found", "Rating not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteCommunityRatingReaction handles
// DELETE /ratings/{item_id}/community/{rating_key}/reaction.
func (h *RatingsHandler) HandleDeleteCommunityRatingReaction(w http.ResponseWriter, r *http.Request) {
	target, ok := h.communityReactionTarget(w, r)
	if !ok {
		return
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if err := h.ratingsRepo.DeleteCommunityReaction(
		r.Context(),
		userID,
		profileID,
		chi.URLParam(r, "item_id"),
		target.UserID,
		target.ProfileID,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete rating reaction")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RatingsHandler) communityReactionTarget(w http.ResponseWriter, r *http.Request) (catalog.CommunityRating, bool) {
	itemID := chi.URLParam(r, "item_id")
	ratingKey := chi.URLParam(r, "rating_key")
	if itemID == "" || ratingKey == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Item ID and rating key are required")
		return catalog.CommunityRating{}, false
	}
	if err := h.itemRepo.EnsureAccessible(r.Context(), itemID, requestAccessFilter(r)); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Item not found")
		return catalog.CommunityRating{}, false
	}

	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	ratings, err := h.ratingsRepo.ListCommunity(r.Context(), userID, profileID, itemID, 100, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to find rating")
		return catalog.CommunityRating{}, false
	}
	for _, rating := range ratings {
		if communityRatingKey(itemID, rating) == ratingKey || legacyCommunityRatingKey(rating) == ratingKey {
			return rating, true
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "Rating not found")
	return catalog.CommunityRating{}, false
}

func communityRatingKey(mediaItemID string, rating catalog.CommunityRating) string {
	// A card represents one profile's rating of one item. Keep its opaque key
	// stable when the stars or rated_at value change so clients update the
	// existing card instead of treating an edit as a new card.
	raw := mediaItemID + "\x00" + strconv.Itoa(rating.UserID) + "\x00" + rating.ProfileID
	digest := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(digest[:16])
}

// legacyCommunityRatingKey keeps reactions from an already-open page working
// during a rolling deployment from the timestamp-scoped key format.
func legacyCommunityRatingKey(rating catalog.CommunityRating) string {
	raw := strconv.Itoa(rating.UserID) + "\x00" + rating.ProfileID + "\x00" + rating.RatedAt.UTC().Format(time.RFC3339Nano)
	digest := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(digest[:16])
}

func abbreviateProfileName(name string) string {
	runes := []rune(strings.TrimSpace(name))
	if len(runes) == 0 {
		return "User***"
	}
	return string(runes[0]) + strings.Repeat("*", len(runes)-1)
}

func communityReactionName(reaction int) string {
	switch reaction {
	case 1:
		return "up"
	case -1:
		return "down"
	default:
		return ""
	}
}

func (h *RatingsHandler) communityAvatarURL(ctx context.Context, rating catalog.CommunityRating) string {
	if !isUploadedAvatarRef(rating.Avatar) {
		_, avatarURL := resolveProfileAvatar(ctx, h.AvatarStore, h.AvatarTTL, rating.Avatar)
		return avatarURL
	}
	if h.AvatarStore == nil || len(h.AvatarTokenSecret) == 0 {
		return ""
	}
	ttl := h.AvatarTTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	payload := communityAvatarTokenPayload{
		ObjectKey: uploadedAvatarDisplayKey(strings.TrimPrefix(rating.Avatar, profileAvatarUploadPrefix)),
		Version:   rating.ProfileUpdatedAt.UnixNano(),
		ExpiresAt: time.Now().Add(ttl).Truncate(5 * time.Minute).Unix(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	block, err := aes.NewCipher(communityAvatarEncryptionKey(h.AvatarTokenSecret))
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ""
	}
	nonceMAC := hmac.New(sha256.New, communityAvatarNonceKey(h.AvatarTokenSecret))
	_, _ = nonceMAC.Write(raw)
	nonce := nonceMAC.Sum(nil)[:gcm.NonceSize()]
	sealed := gcm.Seal(nil, nonce, raw, nil)
	token := append(append([]byte(nil), nonce...), sealed...)
	return "/api/v1/ratings/community-avatar/" + base64.RawURLEncoding.EncodeToString(token)
}

func (h *RatingsHandler) parseCommunityAvatarToken(token string) (communityAvatarTokenPayload, bool) {
	var payload communityAvatarTokenPayload
	if token == "" || len(token) > 2048 || len(h.AvatarTokenSecret) == 0 {
		return payload, false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return payload, false
	}
	block, err := aes.NewCipher(communityAvatarEncryptionKey(h.AvatarTokenSecret))
	if err != nil {
		return payload, false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(sealed) <= gcm.NonceSize() {
		return payload, false
	}
	raw, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil || json.Unmarshal(raw, &payload) != nil {
		return communityAvatarTokenPayload{}, false
	}
	if !strings.HasPrefix(payload.ObjectKey, "profile-avatars/") ||
		!strings.HasSuffix(payload.ObjectKey, "/w256.webp") ||
		strings.Contains(payload.ObjectKey, "..") ||
		payload.ExpiresAt < time.Now().Unix() {
		return communityAvatarTokenPayload{}, false
	}
	return payload, true
}

func communityAvatarEncryptionKey(secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("silo-community-avatar-encryption-v1"))
	return mac.Sum(nil)
}

func communityAvatarNonceKey(secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("silo-community-avatar-nonce-v1"))
	return mac.Sum(nil)
}
