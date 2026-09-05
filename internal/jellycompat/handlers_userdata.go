package jellycompat

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
)

// UserDataHandler serves Jellyfin favorites and played-state routes.
type UserDataHandler struct {
	content  ContentService
	userData UserDataService
	codec    *ResourceIDCodec
	mapper   *mapper
}

// NewUserDataHandler creates a new user-data handler.
func NewUserDataHandler(content ContentService, userData UserDataService, codec *ResourceIDCodec, cfg *config.Config) *UserDataHandler {
	return &UserDataHandler{
		content:  content,
		userData: userData,
		codec:    codec,
		mapper:   newMapper(codec, cfg),
	}
}

// HandleGetUserData serves GET /UserItems/{itemId}/UserData.
func (h *UserDataHandler) HandleGetUserData(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	if !validateOptionalUser(w, r, session) {
		return
	}
	contentID, err := decodeContentID(h.codec, chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}

	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	favMap, progressMap, err := resolveUserStateForContentIDs(
		r.Context(), session, h.userData, []string{detail.ContentID},
	)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	isFavorite := favMap[detail.ContentID]
	progress := progressMap[detail.ContentID]

	writeJSON(w, http.StatusOK, h.mapper.itemFromDetail(*detail, isFavorite, progress).UserData)
}

// HandleAddFavorite serves POST /UserFavoriteItems/{itemId}.
func (h *UserDataHandler) HandleAddFavorite(w http.ResponseWriter, r *http.Request) {
	h.handleFavoriteMutation(w, r, true)
}

// HandleRemoveFavorite serves DELETE /UserFavoriteItems/{itemId}.
func (h *UserDataHandler) HandleRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	h.handleFavoriteMutation(w, r, false)
}

// HandleAddFavoriteLegacy serves POST /Users/{userId}/FavoriteItems/{itemId}.
func (h *UserDataHandler) HandleAddFavoriteLegacy(w http.ResponseWriter, r *http.Request) {
	if !validatePseudoUser(w, chi.URLParam(r, "userId"), SessionFromContext(r.Context())) {
		return
	}
	h.handleFavoriteMutation(w, r, true)
}

// HandleRemoveFavoriteLegacy serves DELETE /Users/{userId}/FavoriteItems/{itemId}.
func (h *UserDataHandler) HandleRemoveFavoriteLegacy(w http.ResponseWriter, r *http.Request) {
	if !validatePseudoUser(w, chi.URLParam(r, "userId"), SessionFromContext(r.Context())) {
		return
	}
	h.handleFavoriteMutation(w, r, false)
}

// HandleMarkPlayed serves POST /UserPlayedItems/{itemId}.
func (h *UserDataHandler) HandleMarkPlayed(w http.ResponseWriter, r *http.Request) {
	h.handlePlayedMutation(w, r, true)
}

// HandleMarkUnplayed serves DELETE /UserPlayedItems/{itemId}.
func (h *UserDataHandler) HandleMarkUnplayed(w http.ResponseWriter, r *http.Request) {
	h.handlePlayedMutation(w, r, false)
}

// HandleMarkPlayedLegacy serves POST /Users/{userId}/PlayedItems/{itemId}.
func (h *UserDataHandler) HandleMarkPlayedLegacy(w http.ResponseWriter, r *http.Request) {
	if !validatePseudoUser(w, chi.URLParam(r, "userId"), SessionFromContext(r.Context())) {
		return
	}
	h.handlePlayedMutation(w, r, true)
}

// HandleMarkUnplayedLegacy serves DELETE /Users/{userId}/PlayedItems/{itemId}.
func (h *UserDataHandler) HandleMarkUnplayedLegacy(w http.ResponseWriter, r *http.Request) {
	if !validatePseudoUser(w, chi.URLParam(r, "userId"), SessionFromContext(r.Context())) {
		return
	}
	h.handlePlayedMutation(w, r, false)
}

// HandleGetUserDataLegacy serves GET /Users/{userId}/Items/{itemId}/UserData.
func (h *UserDataHandler) HandleGetUserDataLegacy(w http.ResponseWriter, r *http.Request) {
	if !validatePseudoUser(w, chi.URLParam(r, "userId"), SessionFromContext(r.Context())) {
		return
	}
	// Rewrite "id" param to "itemId" for the shared handler.
	h.HandleGetUserData(w, r)
}

// HandleUpdateUserDataLegacy serves POST /Users/{userId}/Items/{itemId}/UserData.
func (h *UserDataHandler) HandleUpdateUserDataLegacy(w http.ResponseWriter, r *http.Request) {
	if !validatePseudoUser(w, chi.URLParam(r, "userId"), SessionFromContext(r.Context())) {
		return
	}
	h.HandleUpdateUserData(w, r)
}

func (h *UserDataHandler) handleFavoriteMutation(w http.ResponseWriter, r *http.Request, favorite bool) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	contentID, err := decodeContentID(h.codec, chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}

	if !validateOptionalUser(w, r, session) {
		return
	}
	if _, err := h.content.GetItemDetail(r.Context(), session, contentID, nil); err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	if favorite {
		err = h.userData.AddFavorite(r.Context(), session, contentID)
	} else {
		err = h.userData.RemoveFavorite(r.Context(), session, contentID)
	}
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	favMap, progressMap, err := resolveUserStateForContentIDs(
		r.Context(), session, h.userData, []string{detail.ContentID},
	)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	isFavorite := favMap[detail.ContentID]
	progress := progressMap[detail.ContentID]

	writeJSON(w, http.StatusOK, h.mapper.itemFromDetail(*detail, isFavorite, progress).UserData)
}

func (h *UserDataHandler) handlePlayedMutation(w http.ResponseWriter, r *http.Request, played bool) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	targets, err := h.resolvePlayedTargets(r, session, chi.URLParam(r, "itemId"))
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	if !validateOptionalUser(w, r, session) {
		return
	}
	if raw := newCaseInsensitiveQuery(r.URL.Query()).Get("datePlayed"); played && raw != "" {
		date, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr != nil {
			writeError(w, 400, "BadRequest", "Invalid datePlayed")
			return
		}
		writer, ok := h.userData.(interface {
			MarkPlayedBatchAt(context.Context, *Session, []string, time.Time) error
		})
		if !ok {
			writeError(w, 501, "NotImplemented", "Dated watch state unavailable")
			return
		}
		err = writer.MarkPlayedBatchAt(r.Context(), session, targets, date)
	} else if played {
		err = h.userData.MarkPlayedBatch(r.Context(), session, targets)
	} else {
		err = h.userData.MarkUnplayedBatch(r.Context(), session, targets)
	}
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.HandleGetUserData(w, r)
}

func (h *UserDataHandler) resolvePlayedTargets(r *http.Request, session *Session, rawItemID string) ([]string, error) {
	if contentID, err := decodeItemID(h.codec, rawItemID); err == nil {
		return h.resolvePlayedTargetsForItem(r, session, contentID)
	}

	contentID, err := h.codec.DecodeStringID(EncodedIDSeason, rawItemID)
	if err != nil {
		return nil, &HTTPError{StatusCode: http.StatusNotFound, Message: "Item not found"}
	}
	return h.resolvePlayedTargetsForSeason(r, session, contentID)
}

func (h *UserDataHandler) resolvePlayedTargetsForItem(r *http.Request, session *Session, contentID string) ([]string, error) {
	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(detail.Type) {
	case "movie", "episode":
		return []string{detail.ContentID}, nil
	case "season":
		return h.resolvePlayedTargetsForSeason(r, session, detail.ContentID)
	case "series":
		seasons, err := h.content.ListSeasons(r.Context(), session, detail.ContentID, nil)
		if err != nil {
			return nil, err
		}
		targets := make([]string, 0)
		seen := make(map[string]struct{})
		for _, season := range seasons {
			episodes, err := h.content.ListEpisodes(r.Context(), session, detail.ContentID, season.SeasonNumber, nil)
			if err != nil {
				return nil, err
			}
			targets = appendUniqueContentIDs(targets, seen, episodeContentIDs(episodes)...)
		}
		return targets, nil
	default:
		return nil, &HTTPError{StatusCode: http.StatusNotFound, Message: "Item not found"}
	}
}

func (h *UserDataHandler) resolvePlayedTargetsForSeason(r *http.Request, session *Session, contentID string) ([]string, error) {
	episodes, err := h.content.ListEpisodesBySeasonID(r.Context(), session, contentID, nil)
	if err != nil {
		return nil, err
	}
	return episodeContentIDs(episodes), nil
}

func episodeContentIDs(episodes []upstreamEpisode) []string {
	targets := make([]string, 0, len(episodes))
	seen := make(map[string]struct{}, len(episodes))
	for _, episode := range episodes {
		targets = appendUniqueContentIDs(targets, seen, episode.ContentID)
	}
	return targets
}

func appendUniqueContentIDs(targets []string, seen map[string]struct{}, contentIDs ...string) []string {
	for _, contentID := range contentIDs {
		if contentID == "" {
			continue
		}
		if _, ok := seen[contentID]; ok {
			continue
		}
		seen[contentID] = struct{}{}
		targets = append(targets, contentID)
	}
	return targets
}

// updateUserDataRequest uses pointers so omitted and null fields preserve state.
type updateUserDataRequest struct {
	PlaybackPositionTicks *int64     `json:"PlaybackPositionTicks"`
	PlayedPercentage      *float64   `json:"PlayedPercentage"`
	Played                *bool      `json:"Played"`
	IsFavorite            *bool      `json:"IsFavorite"`
	LastPlayedDate        *time.Time `json:"LastPlayedDate"`
	PlayCount             *int       `json:"PlayCount"`
	Rating                *float64   `json:"Rating"`
	Likes                 *bool      `json:"Likes"`
}

func (h *UserDataHandler) HandleUpdateUserData(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, 401, "Unauthorized", "Missing authentication token")
		return
	}
	if !validateOptionalUser(w, r, session) {
		return
	}
	var req updateUserDataRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, 400, "BadRequest", "Invalid request body")
		return
	}
	if req.Rating != nil || req.Likes != nil || (req.PlayCount != nil && (*req.PlayCount < 0 || *req.PlayCount > 1)) {
		writeError(w, 400, "BadRequest", "Unsupported user data field or play count (supported: 0 or 1)")
		return
	}
	if req.PlaybackPositionTicks != nil && *req.PlaybackPositionTicks < 0 || req.PlayedPercentage != nil && (*req.PlayedPercentage < 0 || *req.PlayedPercentage > 100) {
		writeError(w, 400, "BadRequest", "Invalid playback position")
		return
	}
	contentID, err := decodeContentID(h.codec, chi.URLParam(r, "itemId"))
	if err != nil {
		writeError(w, 404, "NotFound", "Item not found")
		return
	}
	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}

	if detail.Type == "series" || detail.Type == "season" {
		if req.PlaybackPositionTicks != nil || req.PlayedPercentage != nil || req.LastPlayedDate != nil {
			writeError(w, 400, "BadRequest", "Progress fields require a playable item")
			return
		}
		if req.PlayCount != nil && req.Played == nil {
			req.Played = new(*req.PlayCount > 0)
		}
		if req.Played != nil {
			targets, err := h.resolvePlayedTargets(r, session, chi.URLParam(r, "itemId"))
			if err != nil {
				writeCompatUpstreamError(w, err)
				return
			}
			if *req.Played {
				err = h.userData.MarkPlayedBatch(r.Context(), session, targets)
			} else {
				err = h.userData.MarkUnplayedBatch(r.Context(), session, targets)
			}
			if err != nil {
				writeCompatUpstreamError(w, err)
				return
			}
		}
		if req.IsFavorite != nil {
			if *req.IsFavorite {
				err = h.userData.AddFavorite(r.Context(), session, detail.ContentID)
			} else {
				err = h.userData.RemoveFavorite(r.Context(), session, detail.ContentID)
			}
			if err != nil {
				writeCompatUpstreamError(w, err)
				return
			}
		}
		h.HandleGetUserData(w, r)
		return
	}
	writer, ok := h.userData.(interface {
		UpdateUserData(context.Context, *Session, string, float64, updateUserDataRequest) error
	})
	if !ok {
		writeError(w, 501, "NotImplemented", "User data updates unavailable")
		return
	}
	if err := writer.UpdateUserData(r.Context(), session, detail.ContentID, float64(detail.Runtime)*60, req); err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	h.HandleGetUserData(w, r)
}
