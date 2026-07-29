package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/invitations"
)

// InvitationHandler handles the public (unauthenticated) claim endpoints.
type InvitationHandler struct {
	service *invitations.Service
}

// NewInvitationHandler creates a new InvitationHandler.
func NewInvitationHandler(service *invitations.Service) *InvitationHandler {
	return &InvitationHandler{service: service}
}

type invitationLookupResponse struct {
	Email       string    `json:"email"`
	InviterName string    `json:"inviter_name,omitempty"`
	ServerName  string    `json:"server_name"`
	ExpiresAt   time.Time `json:"expires_at"`
	ShowTour    bool      `json:"show_tour"`
}

type acceptInvitationRequest struct {
	Password string `json:"password"`
}

// HandleLookupInvitation handles GET /invitations/{token}. Unknown, expired,
// revoked, and accepted tokens return an identical 404 so a probe learns
// nothing about which.
func (h *InvitationHandler) HandleLookupInvitation(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Lookup(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "This invitation is invalid or has expired")
		return
	}
	writeJSON(w, http.StatusOK, invitationLookupResponse{
		Email:       result.Email,
		InviterName: result.InviterName,
		ServerName:  result.ServerName,
		ExpiresAt:   result.ExpiresAt,
		ShowTour:    result.ShowTour,
	})
}

// HandleAcceptInvitation handles POST /invitations/{token}/accept. On
// success it returns the same login response shape as signup, so clients
// reuse their existing session plumbing.
func (h *InvitationHandler) HandleAcceptInvitation(w http.ResponseWriter, r *http.Request) {
	var req acceptInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "weak_password", "Password must be at least 8 characters")
		return
	}

	pair, user, err := h.service.Accept(
		r.Context(),
		chi.URLParam(r, "token"),
		req.Password,
		r.UserAgent(),
		clientip.FromContext(r.Context()),
	)
	if err != nil {
		switch {
		case errors.Is(err, invitations.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "This invitation is invalid or has expired")
		case errors.Is(err, invitations.ErrNotClaimable):
			writeError(w, http.StatusConflict, "already_used", "This invitation has already been used")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "An unexpected error occurred")
		}
		return
	}

	writeJSON(w, http.StatusCreated, buildLoginResponse(pair, user, nil))
}
