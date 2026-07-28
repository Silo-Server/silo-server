package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/invitations"
	"github.com/Silo-Server/silo-server/internal/models"
)

// AdminInvitationHandler handles admin endpoints for emailed invitations.
type AdminInvitationHandler struct {
	service *invitations.Service
}

// NewAdminInvitationHandler creates a new AdminInvitationHandler.
func NewAdminInvitationHandler(service *invitations.Service) *AdminInvitationHandler {
	return &AdminInvitationHandler{service: service}
}

// --- Request/Response types ---

type createInvitationRequest struct {
	Email         string `json:"email"`
	Role          string `json:"role"`
	AccessGroupID *int64 `json:"access_group_id"`
	LibraryIDs    []int  `json:"library_ids"`
	CreateProfile *bool  `json:"create_profile"`
	ShowTour      *bool  `json:"show_tour"`
	Note          string `json:"note"`
}

type invitationResponse struct {
	ID            int64     `json:"id"`
	Email         string    `json:"email"`
	Role          string    `json:"role"`
	AccessGroupID *int64    `json:"access_group_id,omitempty"`
	LibraryIDs    []int     `json:"library_ids,omitempty"`
	CreateProfile bool      `json:"create_profile"`
	ShowTour      bool      `json:"show_tour"`
	Note          string    `json:"note,omitempty"`
	InvitedBy     int64     `json:"invited_by"`
	InvitedByName string    `json:"invited_by_name,omitempty"`
	Status        string    `json:"status"`
	ExpiresAt     time.Time `json:"expires_at"`
	AcceptedAt    *string   `json:"accepted_at,omitempty"`
	AcceptedUser  *int64    `json:"accepted_user_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type sendInvitationResponse struct {
	Invitation invitationResponse `json:"invitation"`
	EmailSent  bool               `json:"email_sent"`
	// ClaimURL embeds the single-use token, so this response is the only
	// chance to read it — the server keeps just the hash. Returned even when
	// the email sent, so the admin can also deliver the link directly.
	ClaimURL string `json:"claim_url,omitempty"`
}

func toInvitationResponse(inv *models.Invitation, now time.Time) invitationResponse {
	resp := invitationResponse{
		ID:            inv.ID,
		Email:         inv.Email,
		Role:          inv.Role,
		AccessGroupID: inv.AccessGroupID,
		LibraryIDs:    inv.LibraryIDs,
		CreateProfile: inv.CreateProfile,
		ShowTour:      inv.ShowTour,
		Note:          inv.Note,
		InvitedBy:     inv.InvitedBy,
		InvitedByName: inv.InvitedByName,
		Status:        inv.Status(now),
		ExpiresAt:     inv.ExpiresAt,
		AcceptedUser:  inv.AcceptedUserID,
		CreatedAt:     inv.CreatedAt,
	}
	if inv.AcceptedAt != nil {
		accepted := inv.AcceptedAt.Format(time.RFC3339)
		resp.AcceptedAt = &accepted
	}
	return resp
}

// HandleListInvitations handles GET /admin/invitations.
func (h *AdminInvitationHandler) HandleListInvitations(w http.ResponseWriter, r *http.Request) {
	list, err := h.service.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list invitations")
		return
	}
	now := time.Now()
	resp := make([]invitationResponse, 0, len(list))
	for _, inv := range list {
		resp = append(resp, toInvitationResponse(inv, now))
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleCreateInvitation handles POST /admin/invitations.
func (h *AdminInvitationHandler) HandleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}

	var req createInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	createProfile := true
	if req.CreateProfile != nil {
		createProfile = *req.CreateProfile
	}
	showTour := true
	if req.ShowTour != nil {
		showTour = *req.ShowTour
	}

	result, err := h.service.Send(r.Context(), invitations.SendInput{
		Email:         req.Email,
		Role:          req.Role,
		AccessGroupID: req.AccessGroupID,
		LibraryIDs:    req.LibraryIDs,
		CreateProfile: createProfile,
		ShowTour:      showTour,
		Note:          req.Note,
		InvitedBy:     int64(claims.UserID),
	})
	if err != nil {
		writeInvitationSendError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, buildSendResponse(result))
}

// HandleResendInvitation handles POST /admin/invitations/{id}/resend.
func (h *AdminInvitationHandler) HandleResendInvitation(w http.ResponseWriter, r *http.Request) {
	claims := apimw.GetClaims(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid invitation ID")
		return
	}

	result, err := h.service.Resend(r.Context(), id, int64(claims.UserID))
	if err != nil {
		if errors.Is(err, invitations.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Invitation not found")
			return
		}
		writeInvitationSendError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, buildSendResponse(result))
}

// HandleRevokeInvitation handles DELETE /admin/invitations/{id}.
func (h *AdminInvitationHandler) HandleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid invitation ID")
		return
	}
	if err := h.service.Revoke(r.Context(), id); err != nil {
		if errors.Is(err, invitations.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Invitation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to revoke invitation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func buildSendResponse(result *invitations.SendResult) sendInvitationResponse {
	resp := sendInvitationResponse{
		Invitation: toInvitationResponse(result.Invitation, time.Now()),
		EmailSent:  result.EmailSent,
		ClaimURL:   result.ClaimURL,
	}
	return resp
}

func writeInvitationSendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, invitations.ErrInvalidEmail):
		writeError(w, http.StatusBadRequest, "invalid_email", "Invalid email address")
	case errors.Is(err, invitations.ErrEmailTaken):
		writeError(w, http.StatusConflict, "email_taken", "An account with this email already exists")
	case errors.Is(err, invitations.ErrRoleNotAllowed):
		writeError(w, http.StatusForbidden, "role_not_allowed", "You may not grant this role")
	case errors.Is(err, invitations.ErrNoLinkBase):
		writeError(w, http.StatusConflict, "no_link_base",
			"Configure notifications.email.external_url (or a server public URL) so invitation links can be built")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to send invitation")
	}
}
