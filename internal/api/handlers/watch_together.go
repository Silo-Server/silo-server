package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type WatchTogetherScopeResolver interface {
	Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error)
}

type WatchTogetherHandler struct {
	Service       *watchtogether.Service
	ScopeResolver WatchTogetherScopeResolver
	TokenService  *watchtogether.RoomTokenService
}

type createWatchTogetherRoomRequest struct {
	SelectionMode string `json:"selection_mode,omitempty"`
}

type joinWatchTogetherRoomRequest struct {
	Code      string `json:"code"`
	JoinToken string `json:"join_token"`
}

type updateWatchTogetherPolicyRequest struct {
	GuestControlPolicy watchtogether.GuestControlPolicy `json:"guest_control_policy"`
}

type selectWatchTogetherRoomItemRequest struct {
	ContentID string `json:"content_id"`
	FileID    *int   `json:"file_id"`
	LibraryID *int   `json:"library_id"`
}

type watchTogetherRoomResponse struct {
	Room            watchtogether.Snapshot `json:"room"`
	RoomAccessToken string                 `json:"room_access_token,omitempty"`
}

type createWatchTogetherSuggestionRequest struct {
	ContentID   string `json:"content_id"`
	ContentType string `json:"content_type"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle,omitempty"`
	PosterURL   string `json:"poster_url,omitempty"`
	Note        string `json:"note,omitempty"`
}

type promoteSuggestionRequest struct {
	SuggestionID string `json:"suggestion_id"`
}

type watchTogetherSuggestionsResponse struct {
	Suggestions []watchtogether.Suggestion `json:"suggestions"`
}

type watchTogetherClientMessage struct {
	Type string `json:"type"`
}

type watchTogetherAttachMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

type watchTogetherTransportRequestMessage struct {
	Type            string                        `json:"type"`
	Action          watchtogether.TransportAction `json:"action"`
	PositionSeconds *float64                      `json:"position_seconds,omitempty"`
	IsPaused        bool                          `json:"is_paused"`
}

type watchTogetherStateReportMessage struct {
	Type            string  `json:"type"`
	SessionID       string  `json:"session_id"`
	PositionSeconds float64 `json:"position_seconds"`
	IsPaused        bool    `json:"is_paused"`
}

type watchTogetherReadyMessage struct {
	Type            string  `json:"type"`
	SessionID       string  `json:"session_id"`
	PositionSeconds float64 `json:"position_seconds"`
	IsPaused        bool    `json:"is_paused"`
}

type watchTogetherBufferingMessage struct {
	Type            string  `json:"type"`
	SessionID       string  `json:"session_id"`
	PositionSeconds float64 `json:"position_seconds"`
	IsPaused        bool    `json:"is_paused"`
}

type watchTogetherPingMessage struct {
	Type         string `json:"type"`
	ClientSentAt string `json:"client_sent_at"`
}

// watchTogetherRoomConn serializes every write to the underlying gorilla
// connection. gorilla/websocket does not support concurrent writers, and room
// broadcasts arrive from other members' goroutines, so all writes — including
// pong/error replies from the read loop — must go through this wrapper.
type watchTogetherRoomConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	// pingSentAtNano is the send time of the most recent protocol-level ping,
	// used to measure round-trip latency when the pong arrives.
	pingSentAtNano atomic.Int64
}

func (c *watchTogetherRoomConn) WriteJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeWebSocketJSON(c.conn, v)
}

func (c *watchTogetherRoomConn) Close() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Close()
}

func (c *watchTogetherRoomConn) WritePing() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.pingSentAtNano.Store(time.Now().UnixNano())
	return writeWebSocketControl(c.conn, websocket.PingMessage, nil)
}

// TakePingSentAt returns and clears the send time of the last unanswered
// protocol-level ping.
func (c *watchTogetherRoomConn) TakePingSentAt() time.Time {
	nano := c.pingSentAtNano.Swap(0)
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

func (c *watchTogetherRoomConn) WriteError(code, message string) {
	_ = c.WriteJSON(map[string]string{
		"type":    "error",
		"code":    code,
		"message": message,
	})
}

func (c *watchTogetherRoomConn) writeRoomClosed(reason string) {
	_ = c.WriteJSON(map[string]string{
		"type":   "room_closed",
		"reason": reason,
	})
}

func NewWatchTogetherHandler(
	service *watchtogether.Service,
	scopeResolver WatchTogetherScopeResolver,
	tokenService *watchtogether.RoomTokenService,
) *WatchTogetherHandler {
	return &WatchTogetherHandler{
		Service:       service,
		ScopeResolver: scopeResolver,
		TokenService:  tokenService,
	}
}

func (h *WatchTogetherHandler) HandleCreateRoom(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req createWatchTogetherRoomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	room, err := h.Service.CreateRoom(r.Context(), watchtogether.CreateRoomInput{
		HostUserID:    userID,
		HostProfileID: profileID,
		SelectionMode: watchtogether.RoomSelectionMode(req.SelectionMode),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create room")
		return
	}

	snapshot, err := h.Service.Snapshot(r.Context(), room.ID, userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load room")
		return
	}

	response, err := h.buildRoomResponse(r.Context(), snapshot, userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to issue room token")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *WatchTogetherHandler) HandleJoinRoom(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req joinWatchTogetherRoomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	room, err := h.Service.JoinRoom(r.Context(), watchtogether.JoinInput{
		Code:      req.Code,
		JoinToken: req.JoinToken,
	})
	if err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrInvalidJoinRequest):
			writeError(w, http.StatusBadRequest, "bad_request", "Room code or invite token is required")
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		case errors.Is(err, watchtogether.ErrRoomClosed):
			writeError(w, http.StatusGone, "gone", "Room is no longer active")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to join room")
		}
		return
	}

	snapshot, err := h.Service.Snapshot(r.Context(), room.ID, userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load room")
		return
	}

	response, err := h.buildRoomResponse(r.Context(), snapshot, userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to issue room token")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *WatchTogetherHandler) HandleGetRoom(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	roomID := chi.URLParam(r, "room_id")
	if err := h.validateRoomAccessToken(r, roomID, userID, profileID); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "Room access token required")
		return
	}
	snapshot, err := h.Service.Snapshot(r.Context(), roomID, userID, profileID)
	if err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		case errors.Is(err, watchtogether.ErrRoomClosed):
			writeError(w, http.StatusGone, "gone", "Room is no longer active")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load room")
		}
		return
	}

	response, err := h.buildRoomResponse(r.Context(), snapshot, userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to issue room token")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *WatchTogetherHandler) HandleUpdateRoomPolicy(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	roomID := chi.URLParam(r, "room_id")

	var req updateWatchTogetherPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	snapshot, err := h.Service.UpdatePolicy(r.Context(), roomID, userID, profileID, req.GuestControlPolicy)
	if err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrRoomForbidden):
			writeError(w, http.StatusForbidden, "forbidden", "Only the host can update room policy")
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update room policy")
		}
		return
	}

	response, err := h.buildRoomResponse(r.Context(), snapshot, userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to issue room token")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *WatchTogetherHandler) HandleCloseRoom(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	roomID := chi.URLParam(r, "room_id")

	if err := h.Service.CloseRoom(r.Context(), roomID, userID, profileID); err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrRoomForbidden):
			writeError(w, http.StatusForbidden, "forbidden", "Only the host can close the room")
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to close room")
		}
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *WatchTogetherHandler) HandleSelectRoomItem(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	roomID := chi.URLParam(r, "room_id")

	var req selectWatchTogetherRoomItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	snapshot, err := h.Service.SelectItem(r.Context(), roomID, userID, profileID, watchtogether.SelectItemInput{
		ContentID: req.ContentID,
		FileID:    req.FileID,
		LibraryID: req.LibraryID,
	})
	if err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrRoomForbidden):
			writeError(w, http.StatusForbidden, "forbidden", "Only the host can start or switch room playback")
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		case errors.Is(err, watchtogether.ErrRoomClosed):
			writeError(w, http.StatusGone, "gone", "Room is no longer active")
		case errors.Is(err, watchtogether.ErrInvalidSelection):
			writeError(w, http.StatusBadRequest, "bad_request", "Content is not playable in this room")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update room selection")
		}
		return
	}

	response, err := h.buildRoomResponse(r.Context(), snapshot, userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to issue room token")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *WatchTogetherHandler) HandleListSuggestions(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	roomID := chi.URLParam(r, "room_id")

	if err := h.validateRoomAccessToken(r, roomID, userID, profileID); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "Room access token required")
		return
	}

	suggestions, err := h.Service.ListSuggestions(r.Context(), roomID, profileID)
	if err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		case errors.Is(err, watchtogether.ErrRoomClosed):
			writeError(w, http.StatusGone, "gone", "Room is no longer active")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list suggestions")
		}
		return
	}

	writeJSON(w, http.StatusOK, watchTogetherSuggestionsResponse{Suggestions: suggestions})
}

func (h *WatchTogetherHandler) HandleCreateSuggestion(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	roomID := chi.URLParam(r, "room_id")

	if err := h.validateRoomAccessToken(r, roomID, userID, profileID); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "Room access token required")
		return
	}

	var req createWatchTogetherSuggestionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.ContentID == "" || req.Title == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id and title are required")
		return
	}

	suggestions, err := h.Service.CreateSuggestion(r.Context(), roomID, userID, profileID, watchtogether.CreateSuggestionInput{
		ContentID:   req.ContentID,
		ContentType: req.ContentType,
		Title:       req.Title,
		Subtitle:    req.Subtitle,
		PosterURL:   req.PosterURL,
		Note:        req.Note,
	})
	if err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		case errors.Is(err, watchtogether.ErrRoomClosed):
			writeError(w, http.StatusGone, "gone", "Room is no longer active")
		case errors.Is(err, watchtogether.ErrInvalidSelection):
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid content type")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create suggestion")
		}
		return
	}

	writeJSON(w, http.StatusCreated, watchTogetherSuggestionsResponse{Suggestions: suggestions})
}

func (h *WatchTogetherHandler) HandleDeleteSuggestion(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	roomID := chi.URLParam(r, "room_id")
	suggestionID := chi.URLParam(r, "suggestion_id")

	if err := h.validateRoomAccessToken(r, roomID, userID, profileID); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "Room access token required")
		return
	}

	suggestions, err := h.Service.DeleteSuggestion(r.Context(), roomID, suggestionID, userID, profileID)
	if err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrSuggestionNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Suggestion not found")
		case errors.Is(err, watchtogether.ErrRoomForbidden):
			writeError(w, http.StatusForbidden, "forbidden", "Only the host or suggester can delete a suggestion")
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete suggestion")
		}
		return
	}

	writeJSON(w, http.StatusOK, watchTogetherSuggestionsResponse{Suggestions: suggestions})
}

func (h *WatchTogetherHandler) HandleVote(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	roomID := chi.URLParam(r, "room_id")
	suggestionID := chi.URLParam(r, "suggestion_id")

	if err := h.validateRoomAccessToken(r, roomID, userID, profileID); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "Room access token required")
		return
	}

	suggestions, err := h.Service.Vote(r.Context(), roomID, suggestionID, userID, profileID)
	if err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrDuplicateVote):
			writeError(w, http.StatusConflict, "conflict", "Already voted")
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to vote")
		}
		return
	}

	writeJSON(w, http.StatusOK, watchTogetherSuggestionsResponse{Suggestions: suggestions})
}

func (h *WatchTogetherHandler) HandleUnvote(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	roomID := chi.URLParam(r, "room_id")
	suggestionID := chi.URLParam(r, "suggestion_id")

	if err := h.validateRoomAccessToken(r, roomID, userID, profileID); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "Room access token required")
		return
	}

	suggestions, err := h.Service.Unvote(r.Context(), roomID, suggestionID, userID, profileID)
	if err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrNotVoted):
			writeError(w, http.StatusConflict, "conflict", "Not voted")
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to remove vote")
		}
		return
	}

	writeJSON(w, http.StatusOK, watchTogetherSuggestionsResponse{Suggestions: suggestions})
}

func (h *WatchTogetherHandler) HandlePromoteSuggestion(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	roomID := chi.URLParam(r, "room_id")

	if err := h.validateRoomAccessToken(r, roomID, userID, profileID); err != nil {
		writeError(w, http.StatusForbidden, "forbidden", "Room access token required")
		return
	}

	var req promoteSuggestionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.SuggestionID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "suggestion_id is required")
		return
	}

	snapshot, err := h.Service.PromoteSuggestion(r.Context(), roomID, req.SuggestionID, userID, profileID)
	if err != nil {
		switch {
		case errors.Is(err, watchtogether.ErrRoomForbidden):
			writeError(w, http.StatusForbidden, "forbidden", "Only the host can promote a suggestion")
		case errors.Is(err, watchtogether.ErrSuggestionNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Suggestion not found")
		case errors.Is(err, watchtogether.ErrRoomNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Room not found")
		case errors.Is(err, watchtogether.ErrInvalidSelection):
			writeError(w, http.StatusBadRequest, "bad_request", "Suggested content is not playable")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to promote suggestion")
		}
		return
	}

	response, err := h.buildRoomResponse(r.Context(), snapshot, userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to issue room token")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *WatchTogetherHandler) buildRoomResponse(
	ctx context.Context,
	snapshot watchtogether.Snapshot,
	userID int,
	profileID string,
) (watchTogetherRoomResponse, error) {
	response := watchTogetherRoomResponse{Room: snapshot}
	if h == nil || h.TokenService == nil {
		return response, nil
	}

	token, _, err := h.TokenService.Mint(watchtogether.RoomTokenClaims{
		RoomID:    snapshot.RoomID,
		UserID:    userID,
		ProfileID: profileID,
	})
	if err != nil {
		return watchTogetherRoomResponse{}, err
	}
	response.RoomAccessToken = token
	return response, nil
}

func (h *WatchTogetherHandler) validateRoomAccessToken(
	r *http.Request,
	roomID string,
	userID int,
	profileID string,
) error {
	if h == nil || h.TokenService == nil {
		return nil
	}

	claims, err := h.TokenService.Validate(r.URL.Query().Get("room_token"))
	if err != nil {
		return err
	}
	if claims.RoomID != roomID || claims.UserID != userID || claims.ProfileID != profileID {
		return watchtogether.ErrRoomForbidden
	}
	return nil
}

func (h *WatchTogetherHandler) HandleRoomWebSocket(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Service == nil || h.ScopeResolver == nil {
		http.Error(w, "watch together unavailable", http.StatusServiceUnavailable)
		return
	}

	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	roomID := chi.URLParam(r, "room_id")
	if roomID == "" {
		http.Error(w, "room_id required", http.StatusBadRequest)
		return
	}

	profileID := r.URL.Query().Get("profile_id")
	if profileID == "" {
		http.Error(w, "profile_id required", http.StatusBadRequest)
		return
	}
	if err := h.validateRoomAccessToken(r, roomID, claims.UserID, profileID); err != nil {
		http.Error(w, "room access token required", http.StatusForbidden)
		return
	}

	_, err := h.ScopeResolver.Resolve(r.Context(), access.ResolveInput{
		UserID:              claims.UserID,
		SessionID:           claims.SessionID,
		ProfileID:           profileID,
		ProfileToken:        r.URL.Query().Get("profile_token"),
		SkipPINVerification: claims.TokenType == auth.TokenTypeAPIKey,
	})
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, access.ErrProfileNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, "profile verification failed", status)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	realtimeConn := &watchTogetherRoomConn{conn: conn}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	reg, snapshot, err := h.Service.Connect(ctx, roomID, claims.UserID, profileID, realtimeConn)
	if err != nil {
		// Terminal failures use room_closed so clients stop reconnecting
		// instead of retrying a room that will never come back.
		if errors.Is(err, watchtogether.ErrRoomNotFound) {
			realtimeConn.writeRoomClosed("not_found")
		} else if errors.Is(err, watchtogether.ErrRoomClosed) {
			realtimeConn.writeRoomClosed("ended")
		} else {
			realtimeConn.WriteError("internal_error", "Failed to connect room socket")
		}
		return
	}
	defer h.Service.Disconnect(reg, false)

	configureWebSocket(conn)
	// Measure round-trip latency from protocol-level ping/pong on the server
	// clock; client-reported timestamps are subject to clock skew and cannot
	// be trusted for command scheduling.
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
		if sentAt := realtimeConn.TakePingSentAt(); !sentAt.IsZero() {
			_ = h.Service.HandlePingForConnection(ctx, reg, claims.UserID, profileID, time.Since(sentAt).Milliseconds())
		}
		return nil
	})
	startWebSocketPingLoop(ctx, realtimeConn.WritePing)
	// Prime an RTT sample right away instead of waiting for the first tick.
	_ = realtimeConn.WritePing()

	if err := realtimeConn.WriteJSON(map[string]any{
		"type": "snapshot",
		"room": snapshot,
	}); err != nil {
		return
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		if err := h.handleRoomClientMessage(ctx, realtimeConn, reg, claims.UserID, profileID, data); err != nil {
			realtimeConn.WriteError("bad_request", err.Error())
		}
	}
}

func (h *WatchTogetherHandler) handleRoomClientMessage(
	ctx context.Context,
	rc *watchTogetherRoomConn,
	reg *watchtogether.Registration,
	userID int,
	profileID string,
	data []byte,
) error {
	var base watchTogetherClientMessage
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}

	switch base.Type {
	case "attach_session":
		var msg watchTogetherAttachMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.SessionID == "" {
			return errors.New("session_id is required")
		}
		_, err := h.Service.AttachSessionForConnection(ctx, reg, userID, profileID, msg.SessionID)
		return err
	case "transport_request":
		var msg watchTogetherTransportRequestMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		_, err := h.Service.HandleTransportRequestForConnection(ctx, reg, userID, profileID, watchtogether.TransportRequest{
			Action:          msg.Action,
			PositionSeconds: msg.PositionSeconds,
			IsPaused:        msg.IsPaused,
		})
		return err
	case "state_report":
		var msg watchTogetherStateReportMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.SessionID == "" {
			return errors.New("session_id is required")
		}
		_, err := h.Service.HandleStateReportForConnection(ctx, reg, userID, profileID, watchtogether.StateReport{
			SessionID:       msg.SessionID,
			PositionSeconds: msg.PositionSeconds,
			IsPaused:        msg.IsPaused,
		})
		return err
	case "ready":
		var msg watchTogetherReadyMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.SessionID == "" {
			return errors.New("session_id is required")
		}
		_, err := h.Service.HandleReadyForConnection(ctx, reg, userID, profileID, watchtogether.StateReport{
			SessionID:       msg.SessionID,
			PositionSeconds: msg.PositionSeconds,
			IsPaused:        msg.IsPaused,
		})
		return err
	case "buffering":
		var msg watchTogetherBufferingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.SessionID == "" {
			return errors.New("session_id is required")
		}
		_, err := h.Service.HandleBufferingForConnection(ctx, reg, userID, profileID, watchtogether.StateReport{
			SessionID:       msg.SessionID,
			PositionSeconds: msg.PositionSeconds,
			IsPaused:        msg.IsPaused,
		})
		return err
	case "ping":
		var msg watchTogetherPingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return err
		}
		// The echoed timestamps only serve the client's clock-offset estimate;
		// latency for command scheduling is measured server-side from
		// protocol-level ping/pong.
		now := time.Now().UTC()
		return rc.WriteJSON(map[string]string{
			"type":               "pong",
			"client_sent_at":     msg.ClientSentAt,
			"server_received_at": now.Format(time.RFC3339Nano),
			"server_sent_at":     time.Now().UTC().Format(time.RFC3339Nano),
		})
	default:
		return errors.New("unsupported room websocket message")
	}
}
